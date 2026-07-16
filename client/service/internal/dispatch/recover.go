package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// OnReconnect:手重连时的收编(协议规格 §7.2)。由 hub 在会话建立后调用。
//   - bootId 未变(手记忆连续)→ 同 msgId 重发(新会话),手侧去重表吸收重复。
//   - bootId 变了(手已失忆)→ effectful 转 suspect(法条1);readonly/intrusive void+重派。
func (d *Dispatcher) OnReconnect(handID, newBootID string) {
	cmds, err := d.st.NonTerminalCmdsForHand(handID)
	if err != nil || len(cmds) == 0 {
		return
	}
	session, _, online := d.sender.HandSession(handID)
	if !online {
		return
	}
	// 两阶段(修红队 F4 残漏):第一趟先把换代 effectful 全部 suspect 冻结相关域,
	// 第二趟处理 intrusive void+重派时,冻结复查才能看到同趟 effectful,不会把新 intrusive
	// 派进即将冻结的域。否则结果取决于 created_at 枚举顺序。
	for _, cmd := range cmds {
		if cmd.BootIDAtDispatch != newBootID && cmd.Class == string(protocol.ClassEffectful) {
			d.markSuspect(cmd, "bootId 换代且无 result")
		}
	}
	for _, cmd := range cmds {
		switch {
		case cmd.BootIDAtDispatch == newBootID:
			d.resendCmd(cmd, session)
		case cmd.Class == string(protocol.ClassEffectful):
			// 第一趟已处理
		default:
			d.voidAndRedispatch(cmd, "bootId 换代")
		}
	}
}

// resendCmd:同 msgId 重发(§7.2 规则2)。更新会话/attempt/SentAt;queued→sent,其余不动状态。
func (d *Dispatcher) resendCmd(cmd store.CmdRecord, session string) {
	meta, ok := protocol.Primitives[cmd.Name]
	if !ok {
		return
	}
	body := protocol.CmdBody{
		Name: cmd.Name, Ver: meta.Ver, Args: json.RawMessage(cmd.Args), IdemKey: cmd.IdemKey,
		Deadline: cmd.DeadlineMs, ExecBudgetMs: cmd.ExecBudgetMs,
	}
	env := d.envelope(protocol.KindCmd, cmd.MsgID, &session, body) // 同 msgId
	if err := d.sender.SendEnvelope(cmd.HandID, env); err != nil {
		return
	}
	now := time.Now()
	_ = d.st.MutateCmd(cmd.MsgID, func(r *store.CmdRecord) error {
		if r.Status.Terminal() {
			return errAlreadyTerminal
		}
		r.Session = session
		r.Attempt++
		r.SentAt = &now
		if r.Status == store.CmdQueued {
			r.Status = store.CmdSent
		}
		return nil
	})
	d.st.Audit("resend", cmd.HandID, cmd.MsgID, "bootId 未变,同 msgId 重发")
}

// Recover:脑重启扫描(§8.1)。启动即扫在途:readonly/intrusive 作废,effectful 转 suspect。
// 在任何 WS 服务开始前调用一次。
func (d *Dispatcher) Recover() {
	cmds, err := d.st.NonTerminalCmds()
	if err != nil {
		slog.Error("脑重启扫描失败", "err", err)
		return
	}
	for _, cmd := range cmds {
		if cmd.Class == string(protocol.ClassEffectful) {
			d.markSuspect(cmd, "脑重启扫描:在途 effectful")
			continue
		}
		_ = d.st.MutateCmd(cmd.MsgID, func(r *store.CmdRecord) error {
			if r.Status.Terminal() {
				return errAlreadyTerminal
			}
			r.Status = store.CmdVoid
			r.SuspectReason = "脑重启扫描"
			return nil
		})
		d.st.Audit("cmd_void", cmd.HandID, cmd.MsgID, "脑重启扫描:在途 readonly/intrusive")
	}
	if len(cmds) > 0 {
		slog.Info("脑重启扫描完成", "在途命令数", len(cmds))
	}
}

// Verdict:人工对 suspect 命令二选一裁决(法条5)。
// resolvedOk=确认已发生(补记完成);resolvedFailed=确认未发生(解锁,允许重派)。
// 前置:对账未完成不许人裁——手在线且同代(result 可能在途)拒绝;手离线须持续 ≥ manualDelay。
func (d *Dispatcher) Verdict(msgID string, verdict store.CmdStatus) error {
	if verdict != store.CmdResolvedOk && verdict != store.CmdResolvedFailed {
		return errors.New("非法裁决,只能 resolvedOk / resolvedFailed")
	}
	cmd, err := d.st.CmdByMsgID(msgID)
	if err != nil {
		return err
	}
	if cmd == nil || cmd.Status != store.CmdSuspect {
		return ErrNotSuspect
	}
	_, bootID, online := d.sender.HandSession(cmd.HandID)
	switch {
	case online && bootID == cmd.BootIDAtDispatch:
		return ErrVerdictNotReady // 同代在线,迟到 result 可能仍在途
	case online && bootID != cmd.BootIDAtDispatch:
		// 手换代失忆,不会再有该命令的 result → 放行
	default: // 离线
		if d.sender.HandOfflineMs(cmd.HandID) < d.manualDelayMs {
			return ErrVerdictNotReady
		}
	}
	err = d.st.MutateCmd(msgID, func(r *store.CmdRecord) error {
		if r.Status != store.CmdSuspect {
			return ErrNotSuspect // 竞争:迟到 result 已核销(法条6),人裁作罢
		}
		r.Status = verdict
		return nil
	})
	if err != nil {
		return err
	}
	d.st.Audit("suspect_verdict", cmd.HandID, msgID, string(verdict))
	slog.Info("suspect 人工裁决", "handId", cmd.HandID, "msgId", msgID, "verdict", verdict)
	return nil
}

// noteAckTimeout / resetWedged:HAND_WEDGED 计数(§11 行为能力)。
// 连续 ackTimeout 关连接达 3 次(无正常 ack 打断)→ 告警转人工。
func (d *Dispatcher) noteAckTimeout(handID string) {
	d.wmu.Lock()
	d.wedged[handID]++
	n := d.wedged[handID]
	d.wmu.Unlock()
	if n >= 3 {
		d.st.Audit("hand_wedged", handID, "", fmt.Sprintf("连续 %d 次 ackTimeout,疑似手残废", n))
		slog.Warn("HAND_WEDGED:连续 ackTimeout,告警转人工", "handId", handID, "n", n)
	}
}

func (d *Dispatcher) resetWedged(handID string) {
	d.wmu.Lock()
	if d.wedged[handID] != 0 {
		d.wedged[handID] = 0
	}
	d.wmu.Unlock()
}
