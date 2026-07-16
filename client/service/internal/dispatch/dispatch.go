// Package dispatch:命令派发器(协议规格 §8)。
// 2.4 范围(happy path):先记账后发送、ack 三态、result 终局化 + msgId 去重 + 回 ack。
// 超时/重发/suspect/脑重启扫描在 2.5 接入。
package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

var ErrHandOffline = errors.New("手不在线")

// Sender:把已构造的信封发给某手的当前连接,并查其会话。hub 实现。
type Sender interface {
	SendEnvelope(handID string, env protocol.Envelope) error
	HandSession(handID string) (session, bootID string, ok bool)
}

type Dispatcher struct {
	st     *store.Store
	sender Sender
}

func New(st *store.Store, sender Sender) *Dispatcher {
	return &Dispatcher{st: st, sender: sender}
}

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

	msgID := ids.NewMsgID()
	now := time.Now()
	deadlineMs := now.UnixMilli() + effectiveDeadlineMs(meta)
	idemKey := ""
	if meta.Class == protocol.ClassEffectful {
		// debug 形态幂等键(2.5 换成从 intents 表确定性派生)。
		idemKey = fmt.Sprintf("ik1:debug:%s:%s:-:%s", handID, name, ids.NewMsgID())
	}

	// 1) 先记账(write-ahead:账本永远是手所见命令的超集)。
	rec := &store.CmdRecord{
		MsgID: msgID, Name: name, Class: string(meta.Class), IdemKey: idemKey,
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

	// 3) 记账 sent。
	_ = d.st.MutateCmd(msgID, func(r *store.CmdRecord) error {
		if r.Status == store.CmdQueued {
			r.Status = store.CmdSent
			r.Attempt++
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
		_ = d.st.MutateCmd(ack.Ref, func(r *store.CmdRecord) error {
			if r.Status == store.CmdSent {
				r.Status = store.CmdAccepted
			}
			return nil
		})
	case protocol.AckStatusRejected:
		code := ""
		if ack.Error != nil {
			code = string(ack.Error.Code)
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

// OnResult:处理手的 result。msgId 去重 + 回 ac(即使重复也回 ack,静默去重是禁令)+
// 首见时账本终局化。resultMsgID 是 result 消息自身的 msgId(去重键)。
func (d *Dispatcher) OnResult(handID, resultMsgID string, res protocol.ResultBody) {
	already, err := d.st.MarkProcessed(resultMsgID, "result", handID)
	if err != nil {
		slog.Error("去重记录失败", "err", err)
		return
	}
	// 无论首见还是重复,都回 ack(accepted)——手据此删本地队列条目。
	session, _, online := d.sender.HandSession(handID)
	if online {
		var sp *string
		if session != "" {
			sp = &session
		}
		ackEnv := d.envelope(protocol.KindAck, ids.NewMsgID(), sp, protocol.AckBody{
			Ref: resultMsgID, Status: protocol.AckStatusAccepted,
		})
		_ = d.sender.SendEnvelope(handID, ackEnv)
	}
	if already {
		return // 已终局化过,只补回 ack
	}

	// 账本终局化(ref → 对应命令)。
	body, _ := json.Marshal(res)
	err = d.st.MutateCmd(res.Ref, func(r *store.CmdRecord) error {
		if r.Status.Terminal() {
			// 终局态收到迟到 result:审计不静默(2.5 的 suspect 核销走此路,happy path 罕见)。
			return errAlreadyTerminal
		}
		r.Status = mapResultStatus(res.Status)
		r.ResultBody = string(body)
		if res.Error != nil {
			r.ErrorCode = string(res.Error.Code)
			r.SideEffect = string(res.Error.SideEffect)
		}
		return nil
	})
	switch {
	case errors.Is(err, errAlreadyTerminal):
		d.st.Audit("late_result", handID, res.Ref, "终局后收到 result")
	case err != nil:
		// ref 找不到对应命令:账本缺口,响亮记审计。
		d.st.Audit("orphan_result", handID, res.Ref, err.Error())
	default:
		slog.Info("命令终局", "handId", handID, "ref", res.Ref, "status", res.Status)
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
