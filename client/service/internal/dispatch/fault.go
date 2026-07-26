package dispatch

import (
	"context"
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
	d.sweepEffectRecovery(now)
	cmds, err := d.st.NonTerminalCmds()
	if err != nil {
		slog.Error("扫描非终局命令失败", "err", err)
		return
	}
	nowMs := now.UnixMilli()
	leaseHandled := d.sweepLeases(now)
	grace := int64(protocol.DefaultSuspectGraceMs)
	// 第一趟先收束过期 effectful；其原 idemKey/业务动作由 suspect 隔离，
	// suspect 本身不再占用账号串行域。
	for _, cmd := range cmds {
		if leaseHandled[cmd.MsgID] {
			continue
		}
		if nowMs > cmd.DeadlineMs+grace && cmd.Class == string(protocol.ClassEffectful) {
			if cmd.IntentID != "" {
				if cmd.Status != store.CmdVerifying {
					_ = d.st.MoveEffectToVerification(cmd.MsgID, "deadline+宽限 无终局", now)
				}
				d.kickVerification(cmd.MsgID)
			} else {
				d.markSuspect(cmd, "deadline+宽限 无终局")
			}
		}
	}

	closedThisSweep := map[string]bool{} // 每手每 sweep 至多关一次连接(红队 F2/F6/F11)
	ackTimeoutMs := time.Duration(protocol.DefaultAckTimeoutMs) * time.Millisecond
	for _, cmd := range cmds {
		if leaseHandled[cmd.MsgID] {
			continue
		}
		if cmd.Status == store.CmdPendingReconcile || cmd.Status == store.CmdVerifying {
			continue
		}
		// deadline+宽限:优先于其余(过期命令不必再等)。
		if nowMs > cmd.DeadlineMs+grace {
			if cmd.Class == string(protocol.ClassEffectful) {
				continue // 第一趟已处理
			}
			d.voidAndRedispatch(cmd, "deadline+宽限 无终局", redispatchBackoff(cmd.RedispatchN+1))
			continue
		}
		// queued 且手在线 → 重投驱动(§7.2.4):发送失败或瞬态拒绝(QUEUE_FULL/STALE_SESSION)
		// 回 queued 的命令,在存活连接上同 msgId 再投,不再滞留到 deadline 被误判 suspect(红队 F5/F8)。
		if cmd.Status == store.CmdQueued {
			if !allowsAutomaticRedispatch(cmd.Name) {
				d.terminalizeVoid(cmd, "该维护原语禁止普通自动重派")
				continue
			}
			if cmd.NotBeforeAt != nil && now.Before(*cmd.NotBeforeAt) {
				continue
			}
			if session, _, online := d.sender.HandSession(cmd.HandID); online {
				d.resendCmdAt(cmd, session, now)
			}
			continue
		}
		// ackTimeout:sent 且手在线 且超时 → 关连接触发重连(§7.2.1)。
		if cmd.Status == store.CmdSent && cmd.SentAt != nil && now.Sub(*cmd.SentAt) > ackTimeoutMs {
			if closedThisSweep[cmd.HandID] {
				continue
			}
			// 复读当前状态,避免快照过期(OnAck/resendCmd 已推进)误关健康连接(红队 F2)。
			fresh, _ := d.st.CmdByMsgID(cmd.MsgID)
			if fresh == nil || fresh.Status != store.CmdSent || fresh.SentAt == nil || now.Sub(*fresh.SentAt) <= ackTimeoutMs {
				continue
			}
			currentSession, _, online := d.sender.HandSession(cmd.HandID)
			if !online || currentSession != fresh.Session {
				continue
			}
			if !d.sender.CloseHand(cmd.HandID, fresh.Session, "ackTimeout") {
				continue
			}
			closedThisSweep[cmd.HandID] = true
			d.st.Audit("ack_timeout", cmd.HandID, cmd.MsgID, "sent 超 ackTimeout 无应答,关连接")
			slog.Warn("ackTimeout,关连接", "handId", cmd.HandID, "msgId", cmd.MsgID, "session", fresh.Session)
			d.noteAckTimeout(cmd.HandID)
		}
	}
}

// markSuspect:effectful 命令转 suspect(法条1/2)。原 idemKey 与业务动作继续隔离，
// 账号串行域释放；此处只落状态、审计、告警(v1 通知=日志+审计,UI 队列在 2.6)。
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
	d.clearLease(cmd.MsgID)
	d.notifyByMsgID(cmd.MsgID)
	slog.Warn("命令转 suspect(永不自动重试,待人工裁决)", "handId", cmd.HandID, "msgId", cmd.MsgID, "reason", reason)
}

// voidAndRedispatch:readonly/intrusive 命令作废并重派。存在 child 时必须走 Store.ReplaceCmd，
// 原子写入 parent void + replacement leaf，逻辑等待者看不到“中间 void、孩子未入账”。
func (d *Dispatcher) voidAndRedispatch(cmd store.CmdRecord, reason string, delay time.Duration) {
	if !allowsAutomaticRedispatch(cmd.Name) {
		d.terminalizeVoid(cmd, reason)
		d.st.Audit("redispatch_disabled", cmd.HandID, cmd.MsgID, "该维护原语只允许管理编排层显式重试")
		return
	}
	cap := redispatchCap(cmd.Class)
	if cmd.RedispatchN >= cap {
		d.terminalizeVoid(cmd, reason)
		// 重派耗尽:readonly→标手 suspect 告警;intrusive→能力级告警。v1 都到告警为止(ensureSurface 自愈为 [S])。
		d.st.Audit("redispatch_exhausted", cmd.HandID, cmd.MsgID,
			fmt.Sprintf("class=%s 重派耗尽(%d/%d)", cmd.Class, cmd.RedispatchN, cap))
		slog.Warn("重派耗尽,健康告警", "handId", cmd.HandID, "class", cmd.Class, "n", cmd.RedispatchN)
		return
	}
	d.redispatchFrom(cmd, reason, delay)
}

// debug.reload 成功路径会主动杀死当前 SW；把它放进普通同 msgId/新 msgId
// 恢复轨会形成重载循环。当前只有这一条已立案的窄例外，不扩成通用框架。
func allowsAutomaticRedispatch(name string) bool {
	return name != protocol.PrimDebugReload
}

// redispatchFrom:基于旧命令铸造新命令(新 msgId,RedispatchN+1,新 deadline)重派。
// 手离线则不重派(命令是状态投影,不排队);上线/巡检自然重来。
func (d *Dispatcher) redispatchFrom(old store.CmdRecord, reason string, delay time.Duration) {
	meta, ok := protocol.Primitives[old.Name]
	if !ok {
		d.terminalizeVoid(old, reason)
		return
	}
	session, bootID, online := d.sender.HandSession(old.HandID)
	if !online {
		d.terminalizeVoid(old, reason)
		return
	}
	msgID := ids.NewMsgID()
	notBefore := time.Now().Add(delay)
	deadlineMs := notBefore.UnixMilli() + effectiveDeadlineMs(meta)
	rec := &store.CmdRecord{
		MsgID:  msgID,
		HandID: old.HandID, Session: session, BootIDAtDispatch: bootID,
		Status:     store.CmdQueued,
		DeadlineMs: deadlineMs, ExecBudgetMs: old.ExecBudgetMs,
	}
	if delay > 0 {
		rec.NotBeforeAt = &notBefore
	}
	if err := d.st.ReplaceCmd(old.MsgID, store.CmdVoid, reason, rec); err != nil {
		slog.Error("重派记账失败", "err", err)
		return
	}
	d.clearLease(old.MsgID)
	d.st.Audit("cmd_void", old.HandID, old.MsgID, reason)
	d.notifyLogical(rec.LogicalDispatchID)
	if rec.NotBeforeAt != nil {
		d.st.Audit("redispatch_scheduled", old.HandID, msgID,
			fmt.Sprintf("from=%s n=%d notBefore=%s", old.MsgID, rec.RedispatchN, rec.NotBeforeAt.Format(time.RFC3339Nano)))
		return
	}
	body, err := d.commandBody(*rec)
	if err != nil {
		d.st.Audit("redispatch_invalid", old.HandID, msgID, err.Error())
		return
	}
	env := d.envelope(protocol.KindCmd, msgID, &session, body)
	if err := d.sender.SendEnvelope(old.HandID, env); err != nil {
		return // 留 queued,下轮再来
	}
	d.markSent(msgID, session, session)
	d.st.Audit("redispatch", old.HandID, msgID, fmt.Sprintf("from=%s n=%d", old.MsgID, rec.RedispatchN))
	slog.Info("重派", "handId", old.HandID, "from", old.MsgID, "to", msgID, "n", rec.RedispatchN)
}

func (d *Dispatcher) terminalizeVoid(cmd store.CmdRecord, reason string) {
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
	d.clearLease(cmd.MsgID)
	d.st.Audit("cmd_void", cmd.HandID, cmd.MsgID, reason)
	d.notifyByMsgID(cmd.MsgID)
	if cmd.VerificationForMsgID != "" {
		d.kickVerification(cmd.VerificationForMsgID)
	}
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
