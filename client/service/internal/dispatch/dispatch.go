// Package dispatch:命令派发器(协议规格 §7-§8)。
// 派发 happy path(2.4):先记账后发送、ack 三态、result 终局化 + msgId 去重 + 回 ack。
// 故障轨道(2.5):超时引擎(ackTimeout 关连接、deadline+宽限 void/suspect)、重连收编、
// 脑重启扫描、suspect 六法条、人工裁决。真实 SX 额外使用 witness/1
// journal/outbox 四阶段对账与结构化验证读，永不在歧义下盲重投。
package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

var (
	ErrHandOffline        = errors.New("手不在线")
	ErrStaleSession       = errors.New("手会话已更替")
	ErrDomainFrozen       = errors.New("串行域存在 suspect,冻结中(法条4)")
	ErrIdemFrozen         = errors.New("幂等键被 suspect 冻结(法条3)")
	ErrNotSuspect         = errors.New("命令不在 suspect 状态")
	ErrVerdictNotReady    = errors.New("对账未完成,不许人裁(法条5前置):手在线同代或离线不足时长")
	ErrCapability         = errors.New("手未声明原语能力")
	ErrFeature            = errors.New("手未协商协议特性")
	ErrContractMismatch   = errors.New("手与脑的协议契约不一致")
	ErrRecoveryBarrier    = errors.New("手的副作用恢复屏障尚未收束")
	ErrWitnessUnavailable = errors.New("手未提供可用的持久证词库")
	errResultSource       = errors.New("result 来源手与命令不一致")
)

const contractMismatchBeforeSendCode = "BRAIN_CONTRACT_MISMATCH"

type HandWitness struct {
	StoreID       string
	OutboxPending int
	JournalOpen   int
}

// Sender:把已构造的信封发给某手的当前连接,并查其会话/关连接/在线时长。hub 实现。
type Sender interface {
	SendEnvelope(handID string, env protocol.Envelope) error
	HandSession(handID string) (session, bootID string, ok bool)
	HandContractMatch(handID string) (matched, ok bool)
	HandNegotiation(handID string) (caps, features []string, ok bool)
	CloseHand(handID, expectedSession, reason string) bool // 仅关闭超时命令所属 session；已顶替则 no-op
	HandOfflineMs(handID string) int64                     // 离线时长(毫秒);在线返回 0
}

type witnessSender interface {
	HandWitness(handID string) (HandWitness, bool)
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

	verifyMu      sync.Mutex
	verifyRunning map[string]bool // SX cmd msgId → 验证读 goroutine
	verifier      EffectVerifier

	handGateMu sync.Mutex
	handGates  map[string]*sync.Mutex // welcome→收编 与新派发的每手线性化门
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
		verifyRunning: map[string]bool{},
		handGates:     map[string]*sync.Mutex{},
	}
}

func (d *Dispatcher) lockHandGate(handID string) func() {
	d.handGateMu.Lock()
	gate := d.handGates[handID]
	if gate == nil {
		gate = &sync.Mutex{}
		d.handGates[handID] = gate
	}
	d.handGateMu.Unlock()
	gate.Lock()
	return gate.Unlock
}

// BeginHandTakeover 供 Hub 在 welcome 可见之前占住收编门。调用方
// 必须在 OnReconnectWitnessUnderGate 返回后调用 release。
func (d *Dispatcher) BeginHandTakeover(handID string) (release func()) {
	return d.lockHandGate(handID)
}

// SetManualDelayMs:覆盖人工裁决前置的离线时长门槛(测试用短值)。
func (d *Dispatcher) SetManualDelayMs(ms int64) { d.manualDelayMs = ms }

// Dispatch 保留里程碑1 debug 调试入口。业务调度必须使用 DispatchStructured，显式携带
// generated CmdContext；此 wrapper 不改变既有 debug 测试域和自动幂等键行为。
func (d *Dispatcher) Dispatch(handID, name string, args json.RawMessage) (string, error) {
	if !strings.HasPrefix(name, "debug.") {
		return "", errors.New("通用调试入口只允许 debug.* 原语")
	}
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
		if cmd.Status != store.CmdQueued && cmd.Status != store.CmdSent {
			d.st.Audit("late_ack", handID, ack.Ref, "rejected 命中非 queued/sent 状态")
			return
		}
		// 真实 SX 的自动再投授权只有“同 witness store 的
		// report=unknown”这一条。即使拒绝码通常属于瞬态，也不能让
		// 通用 queued 轨绕开证词闸自动重发；本 intent 明确失败后由
		// 产品层决定是否产生新的真人意图。
		if cmd.IntentID != "" {
			if err := d.st.RejectEffectCommand(ack.Ref, code, "effect command ack rejected", time.Now()); err != nil {
				slog.Error("真实 SX ack rejected 入账失败", "handId", handID, "ref", ack.Ref, "err", err)
				return
			}
			d.st.Audit("cmd_rejected", handID, ack.Ref, code)
			d.clearLease(ack.Ref)
			d.notifyByMsgID(ack.Ref)
			return
		}
		// 瞬态拒绝(QUEUE_FULL/STALE_SESSION)回 queued 待重投;协议性拒绝落 rejected 终局。
		if isTransientReject(protocol.ErrorCode(code)) {
			if !allowsAutomaticRedispatch(cmd.Name) {
				_ = d.st.MutateCmd(ack.Ref, func(r *store.CmdRecord) error {
					if !r.Status.Terminal() {
						r.Status = store.CmdRejected
						r.ErrorCode = code
					}
					return nil
				})
				d.st.Audit("cmd_reject_no_redispatch", handID, ack.Ref, code)
				d.clearLease(ack.Ref)
				d.notifyByMsgID(ack.Ref)
				return
			}
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
		if cmd.VerificationForMsgID != "" {
			d.kickVerification(cmd.VerificationForMsgID)
		}
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
	ocHumanVerdictKept                      // 人裁后迟到 possible 不得重开已解锁旧意图
)

// OnResult 只在 result 终局、processed_msgs 证词和可选 replacement 已同事务
// 持久化后回 ack。因此脑在任一崩溃点要么全部看见，要么让手保留 result 重传。
func (d *Dispatcher) OnResult(handID, resultMsgID string, res protocol.ResultBody) {
	oc, replacement, persistErr := d.applyResultMessageKind(handID, resultMsgID, string(protocol.KindResult), res)
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
	ackStatus := protocol.AckStatusAccepted
	if oc == ocAlreadyProcessed {
		ackStatus = protocol.AckStatusDuplicate
	}
	d.ackResult(handID, resultMsgID, ackStatus)
	if oc == ocEffSuspect {
		d.kickVerification(res.Ref)
	}
	// 任一真实 SX 终结/转验证都可能让同手其他账号已获
	// unknown 证明的命令解除全手屏障；重跑是幂等的。
	d.releaseSafeRecoveries(handID)
	if child, _ := d.st.CmdByMsgID(res.Ref); child != nil && child.VerificationForMsgID != "" {
		// 首轮 waiter 可能已 ctx 超时；child 的持久终局主动唤醒 parent，
		// 新 verifier 会复用该 logical child 结果并继续本轮分页。
		d.kickVerification(child.VerificationForMsgID)
	}

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
	case ocHumanVerdictKept:
		d.st.Audit("late_possible_after_verdict", handID, res.Ref,
			"人工终局后迟到 possible，保留裁决且不重开已解锁旧意图")
	default:
		slog.Info("命令终局", "handId", handID, "ref", res.Ref, "status", res.Status)
	}
}

func (d *Dispatcher) applyResultMessage(handID, resultMsgID string, res protocol.ResultBody) (resultOutcome, *store.CmdRecord, error) {
	return d.applyResultMessageKind(handID, resultMsgID, string(protocol.KindResult), res)
}

// applyResultMessageKind 是 result 与 report=done 共用的唯一落账路径。
// generated validator 在事务回调内复核完整 result（含 evidence）；
// 真实 SX 的畸形“成功”一律降为 sideEffect=possible 后验证，绝不会
// 伪造 sideEffect=none 授权重投。
func (d *Dispatcher) applyResultMessageKind(handID, resultMsgID, kind string, res protocol.ResultBody) (resultOutcome, *store.CmdRecord, error) {
	oc := ocDone
	now := time.Now()
	session, bootID, online := d.sender.HandSession(handID)
	primitiveValidationDetail := ""
	result, err := d.st.ApplyResultMessage(res.Ref, resultMsgID, kind, handID,
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
			isRealEffect := r.IntentID != "" && r.Class == string(protocol.ClassEffectful)
			if isRealEffect {
				return d.realEffectResultPlan(r, res, body, now, session, bootID, online, plan, &oc)
			}
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
	meta, ok := protocol.Primitives[cmd.Name]
	var validationErr error
	switch {
	case !ok || meta.Ver == 0:
		validationErr = fmt.Errorf("未知原语 %s", cmd.Name)
	default:
		raw, err := protocol.Encode(res)
		if err != nil {
			validationErr = err
		} else {
			validationErr = protocol.ValidatePrimitiveResult(cmd.Name, meta.Ver, raw)
		}
	}
	// generated schema 不表达跨字段等式；真实发送还要确认 data
	// 指向本命令的会话和正文指纹。
	if validationErr == nil && cmd.Name == protocol.PrimChatSendMessage && res.Status == protocol.ResultStatusOk {
		var args protocol.ChatSendMessageArgs
		var data protocol.ChatSendMessageData
		if err := json.Unmarshal([]byte(cmd.Args), &args); err != nil {
			validationErr = fmt.Errorf("解析发送 args: %w", err)
		} else if err := json.Unmarshal(res.Data, &data); err != nil {
			validationErr = fmt.Errorf("解析发送 data: %w", err)
		} else if data.ConversationRef != args.ConversationRef || data.ContentHash != syncledger.HashText(args.Text) {
			validationErr = errors.New("发送 result 的 conversationRef/contentHash 与原始意图不一致")
		}
	}
	if validationErr == nil && cmd.Name == protocol.PrimChatSendGreeting && res.Status == protocol.ResultStatusOk {
		var args protocol.ChatSendGreetingArgs
		var data protocol.ChatSendGreetingData
		if err := json.Unmarshal([]byte(cmd.Args), &args); err != nil {
			validationErr = fmt.Errorf("解析招呼 args: %w", err)
		} else if err := json.Unmarshal(res.Data, &data); err != nil {
			validationErr = fmt.Errorf("解析招呼 data: %w", err)
		} else if data.PlatformUserRef != args.PlatformUserRef || data.PositionRef != args.PositionRef ||
			data.ContentHash != syncledger.HashText(args.Text) {
			validationErr = errors.New("招呼 result 的候选人/职位/contentHash 与原始意图不一致")
		}
	}
	if validationErr == nil && cmd.Name == protocol.PrimChatSendWechatInvite && res.Status == protocol.ResultStatusOk {
		var args protocol.ChatSendWechatInviteArgs
		var data protocol.ChatSendWechatInviteData
		switch {
		case json.Unmarshal([]byte(cmd.Args), &args) != nil:
			validationErr = errors.New("换微信邀请 args 无法解析")
		case json.Unmarshal(res.Data, &data) != nil:
			validationErr = errors.New("换微信邀请 data 无法解析")
		case data.ConversationRef != args.ConversationRef:
			validationErr = errors.New("换微信邀请 result 的 conversationRef 与命令不一致")
		case !validLowerHex64(data.ContentHash) ||
			data.ContentHash != syncledger.WechatExchangeContentHash():
			validationErr = errors.New("换微信邀请 result 的 contentHash 非法")
		case !validLowerHex64(data.SourceKey):
			validationErr = errors.New("换微信邀请 result 的 sourceKey 非法")
		default:
			validationErr = validateSingleEvidence(
				res.Evidence,
				string(protocol.SendWechatInviteEvidenceTypeOutboundWechatInviteObserved),
			)
		}
	}
	if validationErr == nil && cmd.Name == protocol.PrimChatSendInviteCard && res.Status == protocol.ResultStatusOk {
		var args protocol.ChatSendInviteCardArgs
		var data protocol.ChatSendInviteCardData
		switch {
		case json.Unmarshal([]byte(cmd.Args), &args) != nil:
			validationErr = errors.New("邀面卡 args 无法解析")
		case json.Unmarshal(res.Data, &data) != nil:
			validationErr = errors.New("邀面卡 data 无法解析")
		case data.ConversationRef != args.ConversationRef:
			validationErr = errors.New("邀面卡 result 的 conversationRef 与命令不一致")
		case !validInterviewDetails(args.Interview) || data.Interview != args.Interview:
			validationErr = errors.New("邀面卡 result 的参数与命令不一致")
		case !validLowerHex64(data.ContentHash) ||
			data.ContentHash != syncledger.InterviewInviteContentHash(
				args.Interview.StartsAt,
				args.Interview.EndsAt,
				string(args.Interview.Method),
			):
			validationErr = errors.New("邀面卡 result 的 contentHash 非法")
		case !validLowerHex64(data.SourceKey):
			validationErr = errors.New("邀面卡 result 的 sourceKey 非法")
		default:
			validationErr = validateSingleEvidence(
				res.Evidence,
				string(protocol.SendInviteCardEvidenceTypeOutboundInterviewInviteObserved),
			)
		}
	}
	if validationErr == nil && cmd.Name == protocol.PrimCandidateReadResume && res.Status == protocol.ResultStatusOk {
		var args protocol.CandidateReadResumeArgs
		var data protocol.CandidateReadResumeData
		if err := json.Unmarshal([]byte(cmd.Args), &args); err != nil {
			validationErr = errors.New("简历读取 args 无法解析")
		} else if err := json.Unmarshal(res.Data, &data); err != nil {
			validationErr = errors.New("简历读取 data 无法解析")
		} else if data.ConversationRef != args.ConversationRef || data.PlatformUserRef != args.PlatformUserRef {
			validationErr = errors.New("简历读取 result 的目标引用与原命令不一致")
		}
	}
	if validationErr == nil && cmd.Name == protocol.PrimCandidateReadSourcingResume && res.Status == protocol.ResultStatusOk {
		var args protocol.CandidateReadSourcingResumeArgs
		var data protocol.CandidateReadSourcingResumeData
		if err := json.Unmarshal([]byte(cmd.Args), &args); err != nil {
			validationErr = errors.New("采集简历 args 无法解析")
		} else if err := json.Unmarshal(res.Data, &data); err != nil {
			validationErr = errors.New("采集简历 data 无法解析")
		} else {
			for _, excluded := range args.ExcludePlatformUserRefs {
				if data.PlatformUserRef == excluded {
					validationErr = errors.New("采集结果返回了脑已排除的候选人")
					break
				}
			}
		}
	}
	if validationErr == nil && cmd.Name == protocol.PrimCandidateReadSourcingTargetResume && res.Status == protocol.ResultStatusOk {
		var args protocol.CandidateReadSourcingTargetResumeArgs
		var data protocol.CandidateReadSourcingResumeData
		if err := json.Unmarshal([]byte(cmd.Args), &args); err != nil {
			validationErr = errors.New("定点采集简历 args 无法解析")
		} else if err := json.Unmarshal(res.Data, &data); err != nil {
			validationErr = errors.New("定点采集简历 data 无法解析")
		} else if data.PlatformUserRef != args.PlatformUserRef || data.PositionRef != args.PositionRef {
			validationErr = errors.New("定点采集简历 result 的候选人或职位与原命令不一致")
		}
	}
	if validationErr == nil && cmd.Name == protocol.PrimCandidateReadSourcingWindow && res.Status == protocol.ResultStatusOk {
		var data protocol.CandidateReadSourcingWindowData
		if err := json.Unmarshal(res.Data, &data); err != nil {
			validationErr = errors.New("采集窗口 data 无法解析")
		} else {
			seen := make(map[string]struct{}, len(data.PlatformUserRefs))
			for _, platformUserRef := range data.PlatformUserRefs {
				if _, duplicated := seen[platformUserRef]; duplicated {
					validationErr = errors.New("采集窗口 result 含重复候选人身份")
					break
				}
				seen[platformUserRef] = struct{}{}
			}
		}
	}
	if validationErr == nil {
		return res, ""
	}
	sideEffect := protocol.SideEffectNone
	if cmd.IntentID != "" && cmd.Class == string(protocol.ClassEffectful) {
		sideEffect = protocol.SideEffectPossible
	}
	return protocol.ResultBody{
		Ref: res.Ref, Status: protocol.ResultStatusFailed, ExecMs: res.ExecMs, Replayed: res.Replayed,
		Error: &protocol.ErrorBody{
			Code: protocol.ErrCodeInternalHand, Message: "原语结果不符合生成契约",
			Retryable: protocol.RetryableNo, SideEffect: sideEffect,
		},
	}, validationErr.Error()
}

func validLowerHex64(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		char := value[i]
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validInterviewDetails(details protocol.InterviewDetails) bool {
	return details.StartsAt > 0 &&
		details.EndsAt > details.StartsAt &&
		details.Method == protocol.InterviewMethodWechatVideo
}

func validateSingleEvidence(evidence []protocol.Evidence, expectedType string) error {
	if len(evidence) != 1 ||
		evidence[0].Type != expectedType ||
		evidence[0].Blob != "" ||
		evidence[0].Text != "" {
		return errors.New("卡片 result 必须且只能携带唯一正确 evidence")
	}
	return nil
}

func (d *Dispatcher) realEffectResultPlan(
	r *store.CmdRecord,
	res protocol.ResultBody,
	body []byte,
	now time.Time,
	session, bootID string,
	online bool,
	plan store.ResultCommandMutation,
	oc *resultOutcome,
) (store.ResultCommandMutation, error) {
	switch r.Name {
	case protocol.PrimChatSendMessage:
		return d.realSendMessageResultPlan(r, res, body, now, session, bootID, online, plan, oc)
	case protocol.PrimChatSendGreeting:
		return d.realGreetingResultPlan(r, res, body, now, plan, oc)
	case protocol.PrimChatSendWechatInvite, protocol.PrimChatSendInviteCard:
		return d.realCardResultPlan(r, res, body, now, plan, oc)
	default:
		return store.ResultCommandMutation{}, fmt.Errorf("未知真实副作用原语 %q", r.Name)
	}
}

func (d *Dispatcher) realCardResultPlan(
	r *store.CmdRecord,
	res protocol.ResultBody,
	body []byte,
	now time.Time,
	plan store.ResultCommandMutation,
	oc *resultOutcome,
) (store.ResultCommandMutation, error) {
	var card store.CardResultMutation
	switch r.Name {
	case protocol.PrimChatSendWechatInvite:
		var args protocol.ChatSendWechatInviteArgs
		if err := json.Unmarshal([]byte(r.Args), &args); err != nil {
			return store.ResultCommandMutation{}, err
		}
		card = store.CardResultMutation{
			ConversationRef: args.ConversationRef,
			CardType:        "wechatExchange", CardState: "pending",
			ContentHash: syncledger.WechatExchangeContentHash(),
		}
	case protocol.PrimChatSendInviteCard:
		var args protocol.ChatSendInviteCardArgs
		if err := json.Unmarshal([]byte(r.Args), &args); err != nil {
			return store.ResultCommandMutation{}, err
		}
		startsAt, endsAt, method := args.Interview.StartsAt, args.Interview.EndsAt, string(args.Interview.Method)
		card = store.CardResultMutation{
			ConversationRef: args.ConversationRef,
			CardType:        "interviewInvite", CardState: "unknown",
			ContentHash:         syncledger.InterviewInviteContentHash(startsAt, endsAt, method),
			InterviewStartsAtMs: &startsAt, InterviewEndsAtMs: &endsAt, InterviewMethod: &method,
		}
	default:
		return store.ResultCommandMutation{}, fmt.Errorf("未知卡片原语 %q", r.Name)
	}
	resultEffect := func(status store.EffectIntentStatus, reason string) *store.EffectResultMutation {
		return &store.EffectResultMutation{
			IntentStatus: status, ContentHash: card.ContentHash, Reason: reason,
		}
	}

	wasHumanResolved := r.Status == store.CmdResolvedOk || r.Status == store.CmdResolvedFailed
	wasSuspect := r.Status == store.CmdSuspect
	if r.Status.Terminal() && !wasHumanResolved && r.Status != store.CmdSuspect {
		*oc = ocLate
		plan.Save = false
		return plan, nil
	}

	switch res.Status {
	case protocol.ResultStatusOk:
		switch r.Name {
		case protocol.PrimChatSendWechatInvite:
			var data protocol.ChatSendWechatInviteData
			if err := json.Unmarshal(res.Data, &data); err != nil {
				return store.ResultCommandMutation{}, err
			}
			card.SourceKey = data.SourceKey
		case protocol.PrimChatSendInviteCard:
			var data protocol.ChatSendInviteCardData
			if err := json.Unmarshal(res.Data, &data); err != nil {
				return store.ResultCommandMutation{}, err
			}
			card.SourceKey = data.SourceKey
		}
		r.Status = store.CmdOk
		r.TerminalAt = &now
		r.ResultBody = string(body)
		r.SuspectReason = ""
		applyResultError(r, res)
		plan.Effect = resultEffect(store.EffectIntentOk, "")
		plan.Effect.Card = &card
		if wasHumanResolved || wasSuspect {
			*oc = ocSuspectCleared
		}
		return plan, nil
	case protocol.ResultStatusFailed:
		if res.Error == nil {
			return store.ResultCommandMutation{}, errors.New("effectful failed 缺少 error")
		}
		r.ResultBody = string(body)
		applyResultError(r, res)
		switch res.Error.SideEffect {
		case protocol.SideEffectPossible, protocol.SideEffectConfirmed:
			if wasHumanResolved {
				plan.Save = false
				*oc = ocHumanVerdictKept
				return plan, nil
			}
			r.Status = store.CmdVerifying
			r.TerminalAt = nil
			r.VerificationReason = "result.sideEffect=" + string(res.Error.SideEffect)
			r.VerificationNextAt = &now
			r.ReviewReady = false
			r.ReviewAfterMs = 0
			plan.KeepCommandOpen = true
			plan.Effect = resultEffect(store.EffectIntentVerifying, r.VerificationReason)
			*oc = ocEffSuspect
			return plan, nil
		case protocol.SideEffectNone:
			r.Status = store.CmdFailed
			r.TerminalAt = &now
			r.SuspectReason = ""
			plan.Effect = resultEffect(store.EffectIntentFailed, "")
			if wasHumanResolved || wasSuspect {
				*oc = ocSuspectCleared
			}
			return plan, nil
		default:
			return store.ResultCommandMutation{}, errors.New("effectful result 缺少 sideEffect")
		}
	case protocol.ResultStatusCanceled, protocol.ResultStatusExpired:
		r.Status = mapResultStatus(res.Status)
		r.TerminalAt = &now
		r.ResultBody = string(body)
		applyResultError(r, res)
		plan.Effect = resultEffect(store.EffectIntentFailed, string(res.Status))
		if wasHumanResolved || wasSuspect {
			*oc = ocSuspectCleared
		}
		return plan, nil
	default:
		return store.ResultCommandMutation{}, errors.New("未知 result status")
	}
}

func (d *Dispatcher) realSendMessageResultPlan(
	r *store.CmdRecord,
	res protocol.ResultBody,
	body []byte,
	now time.Time,
	session, bootID string,
	online bool,
	plan store.ResultCommandMutation,
	oc *resultOutcome,
) (store.ResultCommandMutation, error) {
	var args protocol.ChatSendMessageArgs
	if err := json.Unmarshal([]byte(r.Args), &args); err != nil {
		return store.ResultCommandMutation{}, err
	}
	fingerprint := syncledger.HashText(args.Text)
	resultEffect := func(status store.EffectIntentStatus, appendMessage bool, observedAt int64, reason string) *store.EffectResultMutation {
		return &store.EffectResultMutation{
			IntentStatus: status, Append: appendMessage, Text: args.Text,
			ContentHash: fingerprint, ObservedAtMs: observedAt, Reason: reason,
		}
	}

	// durable 平台 result 是比人工裁决更强的事实。resolved* 后迟到
	// ok/confirmed 会纠正账本；明确 failed+none 也会纠正为失败。
	wasHumanResolved := r.Status == store.CmdResolvedOk || r.Status == store.CmdResolvedFailed
	wasSuspect := r.Status == store.CmdSuspect
	if r.Status.Terminal() && !wasHumanResolved && r.Status != store.CmdSuspect {
		*oc = ocLate
		plan.Save = false
		return plan, nil
	}

	switch res.Status {
	case protocol.ResultStatusOk:
		var data protocol.ChatSendMessageData
		if err := json.Unmarshal(res.Data, &data); err != nil {
			return store.ResultCommandMutation{}, err
		}
		r.Status = store.CmdOk
		r.TerminalAt = &now
		r.ResultBody = string(body)
		r.SuspectReason = ""
		applyResultError(r, res)
		plan.Effect = resultEffect(store.EffectIntentOk, true, data.ObservedAt, "")
		if wasHumanResolved || wasSuspect {
			*oc = ocSuspectCleared
		}
		return plan, nil
	case protocol.ResultStatusFailed:
		if res.Error == nil {
			return store.ResultCommandMutation{}, errors.New("effectful failed 缺少 error")
		}
		r.ResultBody = string(body)
		applyResultError(r, res)
		switch res.Error.SideEffect {
		case protocol.SideEffectConfirmed:
			r.Status = store.CmdFailed
			r.TerminalAt = &now
			r.SuspectReason = "result 失败但已确认发生副作用"
			plan.Effect = resultEffect(store.EffectIntentOk, true, now.UnixMilli(), r.SuspectReason)
			*oc = ocSuspectCleared
			return plan, nil
		case protocol.SideEffectPossible:
			if wasHumanResolved {
				// 人裁后串行域可能已签发后继同文 intent。用迟到 possible
				// 重开旧命令，验证器就可能把后继消息错认为旧命令。
				plan.Save = false
				*oc = ocHumanVerdictKept
				return plan, nil
			}
			// 未经人裁的 possible 必须进结构化验证。
			r.Status = store.CmdVerifying
			r.TerminalAt = nil
			r.VerificationReason = "result.sideEffect=possible"
			r.VerificationNextAt = &now
			r.ReviewReady = false
			r.ReviewAfterMs = 0
			plan.KeepCommandOpen = true
			plan.Effect = resultEffect(store.EffectIntentVerifying, false, 0, r.VerificationReason)
			*oc = ocEffSuspect
			return plan, nil
		case protocol.SideEffectNone:
			r.Status = store.CmdFailed
			r.TerminalAt = &now
			r.SuspectReason = ""
			plan.Effect = resultEffect(store.EffectIntentFailed, false, 0, "")
			plan.Effect.Retract = wasHumanResolved
			// 真实 SX 不接受手侧 retryable 授权，也绝不铸造 replacement
			// msgId。唯一自动恢复是 witness 同库 unknown 后重投原 msgId。
			if wasHumanResolved || wasSuspect {
				*oc = ocSuspectCleared
			}
			return plan, nil
		default:
			return store.ResultCommandMutation{}, errors.New("effectful result 缺少 sideEffect")
		}
	case protocol.ResultStatusCanceled, protocol.ResultStatusExpired:
		r.Status = mapResultStatus(res.Status)
		r.TerminalAt = &now
		r.ResultBody = string(body)
		applyResultError(r, res)
		plan.Effect = resultEffect(store.EffectIntentFailed, false, 0, string(res.Status))
		// canceled/expired 与 failed+none 一样是推翻“已经发出”的
		// 权威安全终局。若先前人工 resolvedOk 铸造了 self 消息，必须
		// 与 Cmd+Intent 纠正同事务撤回，不能留下错误沉默锚。
		plan.Effect.Retract = wasHumanResolved
		if wasHumanResolved || wasSuspect {
			*oc = ocSuspectCleared
		}
		return plan, nil
	default:
		return store.ResultCommandMutation{}, errors.New("未知 result status")
	}
}

func (d *Dispatcher) realGreetingResultPlan(
	r *store.CmdRecord,
	res protocol.ResultBody,
	body []byte,
	now time.Time,
	plan store.ResultCommandMutation,
	oc *resultOutcome,
) (store.ResultCommandMutation, error) {
	var args protocol.ChatSendGreetingArgs
	if err := json.Unmarshal([]byte(r.Args), &args); err != nil {
		return store.ResultCommandMutation{}, err
	}
	fingerprint := syncledger.HashText(args.Text)
	resultEffect := func(status store.EffectIntentStatus, reason string) *store.EffectResultMutation {
		return &store.EffectResultMutation{
			IntentStatus: status, Text: args.Text, ContentHash: fingerprint, Reason: reason,
		}
	}

	wasHumanResolved := r.Status == store.CmdResolvedOk || r.Status == store.CmdResolvedFailed ||
		isGreetingManualVerdictVerification(*r)
	wasSuspect := r.Status == store.CmdSuspect
	if r.Status.Terminal() && !wasHumanResolved && r.Status != store.CmdSuspect {
		*oc = ocLate
		plan.Save = false
		return plan, nil
	}

	switch res.Status {
	case protocol.ResultStatusOk:
		var data protocol.ChatSendGreetingData
		if err := json.Unmarshal(res.Data, &data); err != nil {
			return store.ResultCommandMutation{}, err
		}
		r.Status = store.CmdOk
		r.TerminalAt = &now
		r.ResultBody = string(body)
		r.SuspectReason = ""
		applyResultError(r, res)
		plan.Effect = resultEffect(store.EffectIntentOk, "")
		plan.Effect.Greeting = &store.GreetingResultMutation{
			PlatformUserRef: data.PlatformUserRef, PositionRef: data.PositionRef,
			ConversationRef: data.ConversationRef, Text: args.Text,
			ContentHash: data.ContentHash, ObservedAtMs: data.ObservedAt,
		}
		if wasHumanResolved || wasSuspect {
			*oc = ocSuspectCleared
		}
		return plan, nil
	case protocol.ResultStatusFailed:
		if res.Error == nil {
			return store.ResultCommandMutation{}, errors.New("effectful failed 缺少 error")
		}
		r.ResultBody = string(body)
		applyResultError(r, res)
		switch res.Error.SideEffect {
		case protocol.SideEffectPossible, protocol.SideEffectConfirmed:
			if wasHumanResolved {
				plan.Save = false
				*oc = ocHumanVerdictKept
				return plan, nil
			}
			r.Status = store.CmdVerifying
			r.TerminalAt = nil
			r.VerificationReason = "result.sideEffect=" + string(res.Error.SideEffect)
			r.VerificationNextAt = &now
			r.ReviewReady = false
			r.ReviewAfterMs = 0
			plan.KeepCommandOpen = true
			plan.Effect = resultEffect(store.EffectIntentVerifying, r.VerificationReason)
			*oc = ocEffSuspect
			return plan, nil
		case protocol.SideEffectNone:
			r.Status = store.CmdFailed
			r.TerminalAt = &now
			r.SuspectReason = ""
			plan.Effect = resultEffect(store.EffectIntentFailed, "")
			if res.Error.Code == protocol.ErrCodeGreetingRejected {
				plan.Effect.Greeting = &store.GreetingResultMutation{Rejected: true}
			}
			if wasHumanResolved || wasSuspect {
				*oc = ocSuspectCleared
			}
			return plan, nil
		default:
			return store.ResultCommandMutation{}, errors.New("effectful result 缺少 sideEffect")
		}
	case protocol.ResultStatusCanceled, protocol.ResultStatusExpired:
		r.Status = mapResultStatus(res.Status)
		r.TerminalAt = &now
		r.ResultBody = string(body)
		applyResultError(r, res)
		plan.Effect = resultEffect(store.EffectIntentFailed, string(res.Status))
		if wasHumanResolved || wasSuspect {
			*oc = ocSuspectCleared
		}
		return plan, nil
	default:
		return store.ResultCommandMutation{}, errors.New("未知 result status")
	}
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
	if !allowsAutomaticRedispatch(cmd.Name) {
		return plan, ocRetryExhausted
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
		return protocol.DefaultRedispatchCapEffectful
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

// ackResult:首次持久化回 accepted，processed_msgs 已存在的补投回 duplicate；
// 手对二者等价地删除本地 result 队列/outbox。
func (d *Dispatcher) ackResult(handID, resultMsgID string, status protocol.AckStatus) {
	session, _, online := d.sender.HandSession(handID)
	if !online {
		return
	}
	var sp *string
	if session != "" {
		sp = &session
	}
	ackEnv := d.envelope(protocol.KindAck, ids.NewMsgID(), sp, protocol.AckBody{
		Ref: resultMsgID, Status: status,
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
