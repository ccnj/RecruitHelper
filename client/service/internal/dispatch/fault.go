package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

const faultSweepInterval = 1 * time.Second

// RunFaultLoop:超时引擎(协议规格 §8.2)。周期扫描非终局命令:
//   - status=sent 且超 ackTimeout 无应答 → 关连接(唯一动作;离线手跳过,天然去重)。
//   - 任何非终局超 deadline+suspectGrace 无终局 → effectful 转 suspect;readonly/intrusive void+重派。
//
// v1 脑侧唯一超时定时器就是 deadline;execBudgetMs 只是手侧自中止预算,不作脑侧定时器。
func (d *Dispatcher) RunFaultLoop(ctx context.Context) {
	t := time.NewTicker(faultSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			d.sweepFaults(now)
		}
	}
}

// sweepFaults:一次超时扫描。抽出供测试直接触发(传入 now)。
func (d *Dispatcher) sweepFaults(now time.Time) {
	cmds, err := d.st.NonTerminalCmds()
	if err != nil {
		slog.Error("扫描非终局命令失败", "err", err)
		return
	}
	nowMs := now.UnixMilli()
	for _, cmd := range cmds {
		// deadline+宽限:优先于 ackTimeout(过期命令不必再等 ack)。
		if nowMs > cmd.DeadlineMs+protocol.DefaultSuspectGraceMs {
			if cmd.Class == string(protocol.ClassEffectful) {
				d.markSuspect(cmd, "deadline+宽限 无终局")
			} else {
				d.voidAndRedispatch(cmd, "deadline+宽限 无终局")
			}
			continue
		}
		// ackTimeout:仅 sent 且手在线;关连接触发重连(§7.2.1)。离线手已断,跳过(去重)。
		if cmd.Status == store.CmdSent && cmd.SentAt != nil &&
			now.Sub(*cmd.SentAt) > time.Duration(protocol.DefaultAckTimeoutMs)*time.Millisecond {
			if _, _, online := d.sender.HandSession(cmd.HandID); online {
				d.st.Audit("ack_timeout", cmd.HandID, cmd.MsgID, "sent 超 ackTimeout 无应答,关连接")
				slog.Warn("ackTimeout,关连接", "handId", cmd.HandID, "msgId", cmd.MsgID)
				d.sender.CloseHand(cmd.HandID, "ackTimeout")
				d.noteAckTimeout(cmd.HandID)
			}
		}
	}
}

// markSuspect:effectful 命令转 suspect(法条1/2)。冻结由后续 Dispatch 查询 suspect 存在性达成
// (法条3/4);此处只落状态、审计、告警(v1 通知=日志+审计,UI 队列在 2.6)。
func (d *Dispatcher) markSuspect(cmd store.CmdRecord, reason string) {
	err := d.st.MutateCmd(cmd.MsgID, func(r *store.CmdRecord) error {
		if r.Status.Terminal() {
			return errAlreadyTerminal // 与 OnResult 竞争:对方已终局化则不覆盖
		}
		r.Status = store.CmdSuspect
		r.SuspectReason = reason
		return nil
	})
	if err != nil {
		return
	}
	d.st.Audit("suspect", cmd.HandID, cmd.MsgID, reason)
	slog.Warn("命令转 suspect(永不自动重试,待人工裁决)", "handId", cmd.HandID, "msgId", cmd.MsgID, "reason", reason)
}

// voidAndRedispatch:readonly/intrusive 命令作废并重派(未超 cap 且手在线时)。
func (d *Dispatcher) voidAndRedispatch(cmd store.CmdRecord, reason string) {
	err := d.st.MutateCmd(cmd.MsgID, func(r *store.CmdRecord) error {
		if r.Status.Terminal() {
			return errAlreadyTerminal
		}
		r.Status = store.CmdVoid
		r.SuspectReason = reason
		return nil
	})
	if err != nil {
		return
	}
	d.st.Audit("cmd_void", cmd.HandID, cmd.MsgID, reason)

	cap := redispatchCap(cmd.Class)
	if cmd.RedispatchN >= cap {
		// 重派耗尽:readonly→标手 suspect 告警;intrusive→能力级告警。v1 都到告警为止(ensureSurface 自愈为 [S])。
		d.st.Audit("redispatch_exhausted", cmd.HandID, cmd.MsgID,
			fmt.Sprintf("class=%s 重派耗尽(%d/%d)", cmd.Class, cmd.RedispatchN, cap))
		slog.Warn("重派耗尽,健康告警", "handId", cmd.HandID, "class", cmd.Class, "n", cmd.RedispatchN)
		return
	}
	d.redispatchFrom(cmd)
}

// redispatchFrom:基于旧命令铸造新命令(新 msgId,RedispatchN+1,新 deadline)重派。
// 手离线则不重派(命令是状态投影,不排队);上线/巡检自然重来。
func (d *Dispatcher) redispatchFrom(old store.CmdRecord) {
	meta, ok := protocol.Primitives[old.Name]
	if !ok {
		return
	}
	session, bootID, online := d.sender.HandSession(old.HandID)
	if !online {
		return
	}
	msgID := ids.NewMsgID()
	deadlineMs := time.Now().UnixMilli() + effectiveDeadlineMs(meta)
	rec := &store.CmdRecord{
		MsgID: msgID, Name: old.Name, Class: old.Class, Domain: old.Domain, Args: old.Args,
		HandID: old.HandID, Session: session, BootIDAtDispatch: bootID,
		Status: store.CmdQueued, RedispatchN: old.RedispatchN + 1,
		DeadlineMs: deadlineMs, ExecBudgetMs: old.ExecBudgetMs,
	}
	if err := d.st.CreateCmd(rec); err != nil {
		slog.Error("重派记账失败", "err", err)
		return
	}
	body := protocol.CmdBody{
		Name: old.Name, Ver: meta.Ver, Args: json.RawMessage(old.Args),
		Deadline: deadlineMs, ExecBudgetMs: rec.ExecBudgetMs,
	}
	env := d.envelope(protocol.KindCmd, msgID, &session, body)
	if err := d.sender.SendEnvelope(old.HandID, env); err != nil {
		return // 留 queued,下轮再来
	}
	_ = d.st.MutateCmd(msgID, func(r *store.CmdRecord) error {
		if r.Status == store.CmdQueued {
			r.Status = store.CmdSent
			r.Attempt++
			t := time.Now()
			r.SentAt = &t
		}
		return nil
	})
	d.st.Audit("redispatch", old.HandID, msgID, fmt.Sprintf("from=%s n=%d", old.MsgID, rec.RedispatchN))
	slog.Info("重派", "handId", old.HandID, "from", old.MsgID, "to", msgID, "n", rec.RedispatchN)
}

func redispatchCap(class string) int {
	switch class {
	case string(protocol.ClassReadonly):
		return protocol.DefaultRedispatchCapReadonly
	case string(protocol.ClassIntrusive):
		return protocol.DefaultRedispatchCapIntrusive
	default:
		return protocol.DefaultRedispatchCapEffectful // 0:effectful 从不重派
	}
}
