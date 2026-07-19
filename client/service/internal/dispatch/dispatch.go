// Package dispatch:命令派发器(协议规格 §7-§8)。
// 派发 happy path(2.4):先记账后发送、ack 三态、result 终局化 + msgId 去重 + 回 ack。
// 故障轨道(2.5):超时引擎(ackTimeout 关连接、deadline+宽限 void/suspect)、重连收编、
// 脑重启扫描、suspect 六法条、人工裁决。手侧证词 journal/outbox 为 [X],v1 不做。
package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

var (
	ErrHandOffline     = errors.New("手不在线")
	ErrStaleSession    = errors.New("手会话已更替")
	ErrDomainFrozen    = errors.New("串行域存在 suspect,冻结中(法条4)")
	ErrIdemFrozen      = errors.New("幂等键被 suspect 冻结(法条3)")
	ErrNotSuspect      = errors.New("命令不在 suspect 状态")
	ErrVerdictNotReady = errors.New("对账未完成,不许人裁(法条5前置):手在线同代或离线不足时长")
	ErrCapability      = errors.New("手未声明原语能力")
	ErrFeature         = errors.New("手未协商协议特性")
	errResultSource    = errors.New("result 来源手与命令不一致")
)

// Sender:把已构造的信封发给某手的当前连接,并查其会话/关连接/在线时长。hub 实现。
type Sender interface {
	SendEnvelope(handID string, env protocol.Envelope) error
	HandSession(handID string) (session, bootID string, ok bool)
	HandNegotiation(handID string) (caps, features []string, ok bool)
	CloseHand(handID, expectedSession, reason string) bool // 仅关闭超时命令所属 session；已顶替则 no-op
	HandOfflineMs(handID string) int64                     // 离线时长(毫秒);在线返回 0
}

// domainOf:无业务 context 命令的串行域键。首次绑定前 probe.platform 尚无
// accountRef，按手落独立探测域；debug 命令仍用每手 debug 域。有 context 的
// [S/X] 命令会在结构化派发入口覆盖为 platform:accountRef。
func domainOf(handID, name string) string {
	if name == protocol.PrimProbePlatform {
		return "probe:" + handID
	}
	return "debug:" + handID
}

type Dispatcher struct {
	st            *store.Store
	sender        Sender
	manualDelayMs int64

	wmu    sync.Mutex
	wedged map[string]int // handId → 连续 ackTimeout 关连接次数(任一 ack 正常清零)

	waitMu sync.Mutex
	waits  map[string]*logicalWait // logicalDispatchId → 状态变化广播沿

	leaseMu   sync.Mutex
	leases    map[string]*leaseState // cmd msgId → 运行期租约(重启由 Recover 收编)
	cancelRef map[string]string      // cancel msgId → target cmd msgId
}

type logicalWait struct {
	changed chan struct{}
	refs    int
}

func New(st *store.Store, sender Sender) *Dispatcher {
	return &Dispatcher{
		st: st, sender: sender,
		manualDelayMs: protocol.DefaultSuspectManualDelayMs,
		wedged:        map[string]int{},
		waits:         map[string]*logicalWait{},
		leases:        map[string]*leaseState{},
		cancelRef:     map[string]string{},
	}
}

// SetManualDelayMs:覆盖人工裁决前置的离线时长门槛(测试用短值)。
func (d *Dispatcher) SetManualDelayMs(ms int64) { d.manualDelayMs = ms }

// Dispatch 保留里程碑1 debug 调试入口。业务调度必须使用 DispatchStructured，显式携带
// generated CmdContext；此 wrapper 不改变既有 debug 测试域和自动幂等键行为。
func (d *Dispatcher) Dispatch(handID, name string, args json.RawMessage) (string, error) {
	return d.dispatch(DispatchRequest{HandID: handID, Name: name, Args: args}, dispatchOptions{legacyDebug: true})
}

// OnAck:处理手的 ack。accepted/duplicate 推进到 accepted;rejected 落终局(2.4 只处理
// 协议性拒绝 → rejected 终局;瞬态拒绝的 queued 回退在 2.5)。
func (d *Dispatcher) OnAck(handID string, ack protocol.AckBody) {
	if d.onCancelAck(handID, ack) {
		return
	}
	cmd, lookupErr := d.st.CmdByMsgID(ack.Ref)
	if lookupErr != nil {
		slog.Error("ack 来源校验读取账本失败", "handId", handID, "ref", ack.Ref, "err", lookupErr)
		return
	}
	if cmd == nil || cmd.HandID != handID {
		d.st.Audit("ack_source_mismatch", handID, ack.Ref, "ack ref 不存在或不属于来源手")
		return
	}
	switch ack.Status {
	case protocol.AckStatusAccepted, protocol.AckStatusDuplicate:
		d.resetWedged(handID) // 正常应答打断 HAND_WEDGED 连续计数
		var advanced bool
		_ = d.st.MutateCmd(ack.Ref, func(r *store.CmdRecord) error {
			// accepted 是 queued/sent 的合法后继。放宽守卫收下"先发送后记 sent"竞态下
			// 抢在 sent 记账前到达的快 ack(红队 F3)。
			if r.Status == store.CmdQueued || r.Status == store.CmdSent {
				r.Status = store.CmdAccepted
				advanced = true
			}
			return nil
		})
		if !advanced { // 命中终局/void/suspect 的迟到 ack:审计不静默(§8.1 迟到帧总则,F9)
			d.st.Audit("late_ack", handID, ack.Ref, "accepted/duplicate 命中非 queued/sent 状态")
		} else {
			d.startLease(ack.Ref)
		}
	case protocol.AckStatusRejected:
		code := ""
		if ack.Error != nil {
			code = string(ack.Error.Code)
		}
		// 瞬态拒绝(QUEUE_FULL/STALE_SESSION)回 queued 待重投;协议性拒绝落 rejected 终局。
		if isTransientReject(protocol.ErrorCode(code)) {
			notBefore := time.Now().Add(redispatchBackoff(cmd.Attempt))
			_ = d.st.MutateCmd(ack.Ref, func(r *store.CmdRecord) error {
				if r.Status == store.CmdSent {
					r.Status = store.CmdQueued
					r.NotBeforeAt = &notBefore
				}
				return nil
			})
			d.st.Audit("cmd_reject_transient", handID, ack.Ref, code)
			return
		}
		_ = d.st.MutateCmd(ack.Ref, func(r *store.CmdRecord) error {
			if !r.Status.Terminal() {
				r.Status = store.CmdRejected
				r.ErrorCode = code
			}
			return nil
		})
		d.st.Audit("cmd_rejected", handID, ack.Ref, code)
		d.clearLease(ack.Ref)
		d.notifyByMsgID(ack.Ref)
	}
}

func isTransientReject(code protocol.ErrorCode) bool {
	return code == protocol.ErrCodeQueueFull || code == protocol.ErrCodeStaleSession
}

// resultOutcome:终局化结果分类(用于审计与日志)。
type resultOutcome int

const (
	ocDone             resultOutcome = iota // 正常终局
	ocLate                                  // 命中已终局/void:不改账,审计
	ocOrphan                                // ref 无对应命令
	ocSuspectCleared                        // 法条6:suspect 收迟到 result 自动核销
	ocSuspectKept                           // suspect 收 possible/confirmed 迟到 result:落证据不销案(F7)
	ocEffSuspect                            // effectful result possible → suspect(法条1)
	ocRetryScheduled                        // failed+retryable=yes+none 已原子铸造退避 replacement
	ocRetryDeferred                         // afterRecovery 由上层 actor 恢复资源后重新生产命令
	ocRetryExhausted                        // 安全重派达到类别封顶
	ocAlreadyProcessed                      // 同一上行 msgId 重传
)

// OnResult 只在 result 终局、processed_msgs 证词和可选 replacement 已同事务
// 持久化后回 ack。因此脑在任一崩溃点要么全部看见，要么让手保留 result 重传。
func (d *Dispatcher) OnResult(handID, resultMsgID string, res protocol.ResultBody) {
	oc, replacement, persistErr := d.applyResultMessage(handID, resultMsgID, res)
	if persistErr != nil {
		slog.Error("result 持久化失败，保留手侧重传", "handId", handID, "ref", res.Ref, "err", persistErr)
		return
	}
	d.clearLease(res.Ref)
	if replacement != nil {
		d.notifyLogical(replacement.LogicalDispatchID)
	} else {
		d.notifyByMsgID(res.Ref)
	}
	d.ackResult(handID, resultMsgID)

	switch oc {
	case ocOrphan:
		d.st.Audit("orphan_result", handID, res.Ref, "ref 无对应命令")
	case ocLate:
		d.st.Audit("late_result", handID, res.Ref, "终局/void 后收到 result(§8.1 迟到帧总则)")
	case ocSuspectCleared:
		d.st.Audit("suspect_cleared", handID, res.Ref, "迟到 result 自动核销 suspect(法条6)")
	case ocSuspectKept:
		d.st.Audit("suspect_kept", handID, res.Ref, "suspect 迟到 result 仍 possible/confirmed,保持 suspect(F7)")
	case ocEffSuspect:
		d.st.Audit("suspect", handID, res.Ref, "result.sideEffect=possible")
	case ocRetryScheduled:
		d.st.Audit("result_retry_scheduled", handID, res.Ref,
			fmt.Sprintf("replacement=%s notBefore=%v", replacement.MsgID, replacement.NotBeforeAt))
	case ocRetryDeferred:
		d.st.Audit("result_retry_after_recovery", handID, res.Ref, "交由账号 actor 恢复资源后重算")
	case ocRetryExhausted:
		d.st.Audit("result_retry_exhausted", handID, res.Ref, "result 安全重派已达封顶或手已离线")
	case ocAlreadyProcessed:
		// 原子去重已证明首次处理与命令终局同事务成功，只重回 ack。
	default:
		slog.Info("命令终局", "handId", handID, "ref", res.Ref, "status", res.Status)
	}
}

func (d *Dispatcher) applyResultMessage(handID, resultMsgID string, res protocol.ResultBody) (resultOutcome, *store.CmdRecord, error) {
	oc := ocDone
	now := time.Now()
	session, bootID, online := d.sender.HandSession(handID)
	primitiveValidationDetail := ""
	result, err := d.st.ApplyResultMessage(res.Ref, resultMsgID, string(protocol.KindResult), handID,
		func(r *store.CmdRecord) (store.ResultCommandMutation, error) {
			if r.HandID != handID {
				return store.ResultCommandMutation{}, errResultSource
			}
			var validationDetail string
			res, validationDetail = validatePrimitiveResult(*r, res)
			if validationDetail != "" {
				// 事务回调内不能反入 Store(单连接 SQLite 会自锁)；详情在
				// 事务成功后由外层审计。
				primitiveValidationDetail = validationDetail
			}
			body, _ := json.Marshal(res)
			plan := store.ResultCommandMutation{Save: true}
			switch {
			case r.Status == store.CmdSuspect:
				if res.Status == protocol.ResultStatusFailed && res.Error != nil &&
					(res.Error.SideEffect == protocol.SideEffectPossible || res.Error.SideEffect == protocol.SideEffectConfirmed) {
					r.ResultBody = string(body)
					applyResultError(r, res)
					oc = ocSuspectKept
					return plan, nil
				}
				r.Status = mapResultStatus(res.Status)
				r.ResultBody = string(body)
				applyResultError(r, res)
				oc = ocSuspectCleared
			case r.Status.Terminal():
				oc = ocLate
				plan.Save = false
			case r.Class == string(protocol.ClassEffectful) && res.Status == protocol.ResultStatusFailed &&
				res.Error != nil && res.Error.SideEffect == protocol.SideEffectPossible:
				r.Status = store.CmdSuspect
				r.SuspectReason = "result.sideEffect=possible"
				r.ResultBody = string(body)
				applyResultError(r, res)
				oc = ocEffSuspect
			default:
				r.Status = mapResultStatus(res.Status)
				r.ResultBody = string(body)
				applyResultError(r, res)
				plan, oc = d.resultRetryPlan(*r, res, session, bootID, online, now, plan)
			}
			return plan, nil
		})
	if err != nil {
		if errors.Is(err, errResultSource) {
			d.st.Audit("result_source_mismatch", handID, res.Ref, "result ref 不属于来源手")
		}
		return oc, nil, err
	}
	if primitiveValidationDetail != "" {
		d.st.Audit("primitive_data_invalid", handID, res.Ref, primitiveValidationDetail)
	}
	if result.AlreadyProcessed {
		return ocAlreadyProcessed, nil, nil
	}
	if !result.CommandFound {
		return ocOrphan, nil, nil
	}
	return oc, result.Replacement, nil
}

func validatePrimitiveResult(cmd store.CmdRecord, res protocol.ResultBody) (protocol.ResultBody, string) {
	if res.Status != protocol.ResultStatusOk {
		return res, ""
	}
	meta, ok := protocol.Primitives[cmd.Name]
	var validationErr error
	switch {
	case !ok || meta.Ver == 0:
		validationErr = fmt.Errorf("未知原语 %s", cmd.Name)
	case res.DataBlobRef != nil:
		validationErr = errors.New("当前原语不接受 blob result")
	default:
		validationErr = protocol.ValidatePrimitiveData(cmd.Name, meta.Ver, res.Data)
	}
	if validationErr == nil {
		return res, ""
	}
	return protocol.ResultBody{
		Ref: res.Ref, Status: protocol.ResultStatusFailed, ExecMs: res.ExecMs, Replayed: res.Replayed,
		Error: &protocol.ErrorBody{
			Code: protocol.ErrCodeInternalHand, Message: "原语结果不符合生成契约",
			Retryable: protocol.RetryableNo, SideEffect: protocol.SideEffectNone,
		},
	}, validationErr.Error()
}

func (d *Dispatcher) resultRetryPlan(
	cmd store.CmdRecord,
	res protocol.ResultBody,
	session, bootID string,
	online bool,
	now time.Time,
	plan store.ResultCommandMutation,
) (store.ResultCommandMutation, resultOutcome) {
	if res.Status != protocol.ResultStatusFailed || res.Error == nil || res.Error.SideEffect != protocol.SideEffectNone {
		return plan, ocDone
	}
	if res.Error.Retryable == protocol.RetryableAfterRecovery {
		// 通用派发器不能猜“恢复”的业务含义。例如 CTX_NOT_READY 必须由
		// account actor 先执行 ensureSurface，然后才能生产新命令。
		return plan, ocRetryDeferred
	}
	if res.Error.Retryable != protocol.RetryableYes {
		return plan, ocDone
	}
	cap := resultRedispatchCap(cmd.Class)
	if cmd.RedispatchN >= cap || !online {
		return plan, ocRetryExhausted
	}
	meta, ok := protocol.Primitives[cmd.Name]
	if !ok || meta.Ver == 0 {
		return plan, ocRetryExhausted
	}
	delay := redispatchBackoff(cmd.RedispatchN + 1)
	notBefore := now.Add(delay)
	child := &store.CmdRecord{
		MsgID: ids.NewMsgID(), HandID: cmd.HandID, Session: session, BootIDAtDispatch: bootID,
		Status: store.CmdQueued, NotBeforeAt: &notBefore,
		DeadlineMs: notBefore.UnixMilli() + effectiveDeadlineMs(meta), ExecBudgetMs: cmd.ExecBudgetMs,
	}
	plan.Replacement = child
	plan.ReplacementReason = fmt.Sprintf("result retryable=yes, backoff=%s", delay)
	return plan, ocRetryScheduled
}

func resultRedispatchCap(class string) int {
	switch class {
	case string(protocol.ClassReadonly):
		return protocol.DefaultRedispatchCapReadonly
	case string(protocol.ClassIntrusive):
		return protocol.DefaultRedispatchCapIntrusive
	case string(protocol.ClassEffectful):
		return 1 // 明确 failed+none 的安全结果才允许同 idemKey 重发一次。
	default:
		return 0
	}
}

func redispatchBackoff(nextRedispatch int) time.Duration {
	if len(protocol.DefaultRedispatchBackoffMs) == 0 {
		return 0
	}
	index := nextRedispatch - 1
	if index < 0 {
		index = 0
	}
	if index >= len(protocol.DefaultRedispatchBackoffMs) {
		index = len(protocol.DefaultRedispatchBackoffMs) - 1
	}
	return time.Duration(protocol.DefaultRedispatchBackoffMs[index]) * time.Millisecond
}

// ackResult:对 result 回 ack(accepted)。手据此删本地队列条目。
func (d *Dispatcher) ackResult(handID, resultMsgID string) {
	session, _, online := d.sender.HandSession(handID)
	if !online {
		return
	}
	var sp *string
	if session != "" {
		sp = &session
	}
	ackEnv := d.envelope(protocol.KindAck, ids.NewMsgID(), sp, protocol.AckBody{
		Ref: resultMsgID, Status: protocol.AckStatusAccepted,
	})
	_ = d.sender.SendEnvelope(handID, ackEnv)
}

// applyResultError:把 result.error 的错误码与副作用标注落账。
func applyResultError(r *store.CmdRecord, res protocol.ResultBody) {
	if res.Error != nil {
		r.ErrorCode = string(res.Error.Code)
		r.SideEffect = string(res.Error.SideEffect)
	}
}

var (
	errAlreadyTerminal = errors.New("命令已终局")
	errSessionAdvanced = errors.New("命令已被新会话收编")
)

func (d *Dispatcher) envelope(kind protocol.Kind, msgID string, session *string, body any) protocol.Envelope {
	raw, _ := protocol.Encode(body)
	return protocol.Envelope{
		Proto: protocol.ProtoVersion, Kind: kind, MsgID: msgID,
		Session: session, Ts: time.Now().UnixMilli(), Attempt: 1, Body: raw,
	}
}

func mapResultStatus(s protocol.ResultStatus) store.CmdStatus {
	switch s {
	case protocol.ResultStatusOk:
		return store.CmdOk
	case protocol.ResultStatusFailed:
		return store.CmdFailed
	case protocol.ResultStatusCanceled:
		return store.CmdCanceled
	case protocol.ResultStatusExpired:
		return store.CmdExpired
	default:
		return store.CmdFailed
	}
}

func effectiveDeadlineMs(m protocol.PrimitiveMeta) int64 {
	if m.DeadlineMs > 0 {
		return m.DeadlineMs
	}
	return 2 * effectiveBudgetMs(m)
}

func effectiveBudgetMs(m protocol.PrimitiveMeta) int64 {
	if m.ExecBudgetMs > 0 {
		return m.ExecBudgetMs
	}
	switch m.Class {
	case protocol.ClassEffectful:
		return protocol.DefaultExecBudgetDefaultMsEffectful
	default:
		return protocol.DefaultExecBudgetDefaultMsReadonly
	}
}
