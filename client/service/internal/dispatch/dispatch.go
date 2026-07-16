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
	ErrDomainFrozen    = errors.New("串行域存在 suspect,冻结中(法条4)")
	ErrIdemFrozen      = errors.New("幂等键被 suspect 冻结(法条3)")
	ErrNotSuspect      = errors.New("命令不在 suspect 状态")
	ErrVerdictNotReady = errors.New("对账未完成,不许人裁(法条5前置):手在线同代或离线不足时长")
)

// Sender:把已构造的信封发给某手的当前连接,并查其会话/关连接/在线时长。hub 实现。
type Sender interface {
	SendEnvelope(handID string, env protocol.Envelope) error
	HandSession(handID string) (session, bootID string, ok bool)
	CloseHand(handID, reason string)   // ackTimeout 的唯一动作:关连接触发重连(离线手 no-op)
	HandOfflineMs(handID string) int64 // 离线时长(毫秒);在线返回 0
}

// domainOf:命令的串行域键。业务命令用 accountRef([S/X]);debug 用每手 debug 域。
// name 形参为 [S/X] 业务路由预留(届时按 context.accountRef 分域)。
func domainOf(handID, _ string) string {
	return "debug:" + handID
}

type Dispatcher struct {
	st            *store.Store
	sender        Sender
	manualDelayMs int64

	wmu    sync.Mutex
	wedged map[string]int // handId → 连续 ackTimeout 关连接次数(任一 ack 正常清零)
}

func New(st *store.Store, sender Sender) *Dispatcher {
	return &Dispatcher{
		st: st, sender: sender,
		manualDelayMs: protocol.DefaultSuspectManualDelayMs,
		wedged:        map[string]int{},
	}
}

// SetManualDelayMs:覆盖人工裁决前置的离线时长门槛(测试用短值)。
func (d *Dispatcher) SetManualDelayMs(ms int64) { d.manualDelayMs = ms }

// Dispatch:向某手派发一条原语命令。先记账(queued)再发送(sent)。
// 返回命令 msgId。手不在线返回 ErrHandOffline(不记账,巡检下轮再来)。
func (d *Dispatcher) Dispatch(handID, name string, args json.RawMessage) (string, error) {
	meta, ok := protocol.Primitives[name]
	if !ok {
		return "", fmt.Errorf("未知原语 %q", name)
	}
	session, bootID, online := d.sender.HandSession(handID)
	if !online {
		return "", ErrHandOffline
	}

	// 冻结闸(法条3/4):该串行域或幂等键存在 suspect 时,拒绝新 effectful/intrusive。
	// readonly 不进串行域,不受冻结约束(可自由探测)。
	domain := domainOf(handID, name)
	if meta.Class == protocol.ClassEffectful || meta.Class == protocol.ClassIntrusive {
		if frozen, _ := d.st.HasSuspectInDomain(domain); frozen {
			return "", ErrDomainFrozen
		}
	}

	msgID := ids.NewMsgID()
	now := time.Now()
	deadlineMs := now.UnixMilli() + effectiveDeadlineMs(meta)
	idemKey := ""
	if meta.Class == protocol.ClassEffectful {
		// debug 形态幂等键(intents 表确定性派生为 [X])。
		idemKey = fmt.Sprintf("ik1:debug:%s:%s:-:%s", handID, name, ids.NewMsgID())
		if frozen, _ := d.st.HasSuspectIdemKey(idemKey); frozen {
			return "", ErrIdemFrozen
		}
	}

	// 1) 先记账(write-ahead:账本永远是手所见命令的超集)。
	rec := &store.CmdRecord{
		MsgID: msgID, Name: name, Class: string(meta.Class), IdemKey: idemKey,
		Domain: domain, Args: string(args),
		HandID: handID, Session: session, BootIDAtDispatch: bootID,
		Status: store.CmdQueued, DeadlineMs: deadlineMs, ExecBudgetMs: effectiveBudgetMs(meta),
	}
	if err := d.st.CreateCmd(rec); err != nil {
		return "", fmt.Errorf("记账: %w", err)
	}

	// 2) 构造并发送 cmd。
	body := protocol.CmdBody{
		Name: name, Ver: meta.Ver, Args: args, IdemKey: idemKey,
		Deadline: deadlineMs, ExecBudgetMs: rec.ExecBudgetMs,
	}
	env := d.envelope(protocol.KindCmd, msgID, &session, body)
	if err := d.sender.SendEnvelope(handID, env); err != nil {
		// 发送失败:留在 queued,由 2.5 重投处理;happy path 记日志。
		slog.Warn("cmd 发送失败,留 queued", "handId", handID, "msgId", msgID, "err", err)
		return msgID, err
	}

	// 3) 记账 sent(记 SentAt 作 ackTimeout 判定锚点)。
	_ = d.st.MutateCmd(msgID, func(r *store.CmdRecord) error {
		if r.Status == store.CmdQueued {
			r.Status = store.CmdSent
			r.Attempt++
			t := time.Now()
			r.SentAt = &t
		}
		return nil
	})
	slog.Info("已派发", "handId", handID, "name", name, "msgId", msgID, "class", meta.Class)
	return msgID, nil
}

// OnAck:处理手的 ack。accepted/duplicate 推进到 accepted;rejected 落终局(2.4 只处理
// 协议性拒绝 → rejected 终局;瞬态拒绝的 queued 回退在 2.5)。
func (d *Dispatcher) OnAck(handID string, ack protocol.AckBody) {
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
		}
	case protocol.AckStatusRejected:
		code := ""
		if ack.Error != nil {
			code = string(ack.Error.Code)
		}
		// 瞬态拒绝(QUEUE_FULL/STALE_SESSION)回 queued 待重投;协议性拒绝落 rejected 终局。
		if isTransientReject(protocol.ErrorCode(code)) {
			_ = d.st.MutateCmd(ack.Ref, func(r *store.CmdRecord) error {
				if r.Status == store.CmdSent {
					r.Status = store.CmdQueued
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
	}
}

func isTransientReject(code protocol.ErrorCode) bool {
	return code == protocol.ErrCodeQueueFull || code == protocol.ErrCodeStaleSession
}

// resultOutcome:终局化结果分类(用于审计与日志)。
type resultOutcome int

const (
	ocDone           resultOutcome = iota // 正常终局
	ocLate                                // 命中已终局/void:不改账,审计
	ocOrphan                              // ref 无对应命令
	ocSuspectCleared                      // 法条6:suspect 收迟到 result 自动核销
	ocSuspectKept                         // suspect 收 possible/confirmed 迟到 result:落证据不销案(F7)
	ocEffSuspect                          // effectful result possible → suspect(法条1)
)

// OnResult:处理手的 result。终局化 → 去重登记 → 回 ack(静默去重是禁令 §4.4)。
// 顺序修订(红队 F10):终局化不 gate 于去重表——终局化幂等,重传会再次尝试并被 Terminal
// 守卫吸收,故即便某次终局化失败,后续重传仍能补上;去重表仅作观测与审计分类,不吞结果。
func (d *Dispatcher) OnResult(handID, resultMsgID string, res protocol.ResultBody) {
	oc := d.terminalizeResult(res)
	already, _ := d.st.MarkProcessed(resultMsgID, "result", handID)
	d.ackResult(handID, resultMsgID)

	switch oc {
	case ocOrphan:
		d.st.Audit("orphan_result", handID, res.Ref, "ref 无对应命令")
	case ocLate:
		if !already { // 重传的迟到不重复刷审计
			d.st.Audit("late_result", handID, res.Ref, "终局/void 后收到 result(§8.1 迟到帧总则)")
		}
	case ocSuspectCleared:
		d.st.Audit("suspect_cleared", handID, res.Ref, "迟到 result 自动核销 suspect(法条6)")
		slog.Info("suspect 自动核销", "handId", handID, "ref", res.Ref, "status", res.Status)
	case ocSuspectKept:
		d.st.Audit("suspect_kept", handID, res.Ref, "suspect 迟到 result 仍 possible/confirmed,保持 suspect(F7)")
	case ocEffSuspect:
		d.st.Audit("suspect", handID, res.Ref, "result.sideEffect=possible")
		slog.Warn("effectful 结果不确定,转 suspect", "handId", handID, "ref", res.Ref)
	default:
		slog.Info("命令终局", "handId", handID, "ref", res.Ref, "status", res.Status)
	}
}

// terminalizeResult:账本终局化(ref → 对应命令),返回分类。
func (d *Dispatcher) terminalizeResult(res protocol.ResultBody) resultOutcome {
	body, _ := json.Marshal(res)
	oc := ocDone
	err := d.st.MutateCmd(res.Ref, func(r *store.CmdRecord) error {
		switch {
		case r.Status == store.CmdSuspect:
			// 法条6:suspect 收迟到 result。但若 result 本身仍是 possible/confirmed 歧义 →
			// 保持 suspect(只落证据,不销案),歧义只转人工不抹平(红队 F7)。
			if res.Status == protocol.ResultStatusFailed && res.Error != nil &&
				(res.Error.SideEffect == protocol.SideEffectPossible || res.Error.SideEffect == protocol.SideEffectConfirmed) {
				r.ResultBody = string(body)
				applyResultError(r, res)
				oc = ocSuspectKept
				return nil
			}
			r.Status = mapResultStatus(res.Status)
			r.ResultBody = string(body)
			applyResultError(r, res)
			oc = ocSuspectCleared
		case r.Status.Terminal():
			oc = ocLate // 已终局/void:不改账(Save 无副作用),只标记
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
			oc = ocDone
		}
		return nil
	})
	if err != nil {
		return ocOrphan // ref 无对应命令(A 修复后不再有 BUSY 类失败)
	}
	return oc
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

var errAlreadyTerminal = errors.New("命令已终局")

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
