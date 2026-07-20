package dispatch

import (
	"fmt"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

type leaseState struct {
	leaseMs      int64
	expiresAt    time.Time
	deadline     time.Time
	cancelMsg    string
	cancelSentAt time.Time
}

// startLease 只在目标 cmd 的 accepted/duplicate ack 推进成功后调用。租约使用脑的
// 接收时刻，并钳制在绝对 deadline，手钟与 progress 内容都不能延长硬上限。
func (d *Dispatcher) startLease(msgID string) {
	cmd, err := d.st.CmdByMsgID(msgID)
	if err != nil || cmd == nil || cmd.Status != store.CmdAccepted {
		return
	}
	meta, ok := protocol.Primitives[cmd.Name]
	if !ok || meta.LeaseMs == 0 {
		return
	}
	now := time.Now()
	deadline := time.UnixMilli(cmd.DeadlineMs)
	expires := now.Add(time.Duration(meta.LeaseMs) * time.Millisecond)
	if expires.After(deadline) {
		expires = deadline
	}
	d.leaseMu.Lock()
	d.leases[msgID] = &leaseState{leaseMs: meta.LeaseMs, expiresAt: expires, deadline: deadline}
	d.leaseMu.Unlock()
}

// OnProgress 处理一条已经过 generated validator 的 progress。只有来源 hand 匹配、
// 命令仍 accepted 且租约尚未到期时才续租并返回 true；未知、迟到、未启/已过期租约
// 或未协商 progress/1 的上报只留审计，不能复活物理命令。
func (d *Dispatcher) OnProgress(handID string, progress protocol.ProgressBody) bool {
	if raw, err := protocol.Encode(progress); err != nil || protocol.ValidateKindBody(protocol.KindProgress, raw) != nil {
		d.st.Audit("progress_invalid", handID, progress.Ref, "progress body 契约校验失败")
		return false
	}
	_, features, ok := d.sender.HandNegotiation(handID)
	if !ok || !contains(features, string(protocol.FeatureProgress1)) {
		d.st.Audit("progress_unnegotiated", handID, progress.Ref, string(protocol.FeatureProgress1))
		return false
	}
	cmd, err := d.st.CmdByMsgID(progress.Ref)
	if err != nil || cmd == nil || cmd.HandID != handID || cmd.Status != store.CmdAccepted {
		d.st.Audit("late_progress", handID, progress.Ref, "未知、异手或非 accepted 命令")
		return false
	}
	now := time.Now()
	d.leaseMu.Lock()
	lease := d.leases[progress.Ref]
	renewed := lease != nil && now.Before(lease.expiresAt)
	if renewed {
		next := now.Add(time.Duration(lease.leaseMs) * time.Millisecond)
		if next.After(lease.deadline) {
			next = lease.deadline
		}
		lease.expiresAt = next
	}
	d.leaseMu.Unlock()
	if lease == nil {
		d.st.Audit("progress_without_lease", handID, progress.Ref, "ack accepted 尚未建立租约或原语无租约")
		return false
	}
	if !renewed {
		d.st.Audit("progress_after_lease", handID, progress.Ref, "租约已到期，迟到 progress 不得复活命令")
		return false
	}
	return true
}

// sweepLeases 返回本轮已由租约轨道处理的 cmd，避免同一 sweep 又落入 deadline 轨道。
func (d *Dispatcher) sweepLeases(now time.Time) map[string]bool {
	type action struct {
		msgID      string
		sendCancel bool
		settle     bool
		reason     protocol.CancelReason
	}
	var actions []action
	d.leaseMu.Lock()
	for msgID, lease := range d.leases {
		if now.Before(lease.expiresAt) {
			continue
		}
		a := action{msgID: msgID, reason: protocol.CancelReasonLeaseExpired}
		if lease.cancelMsg == "" {
			a.sendCancel = true
			lease.cancelMsg = "pending" // 先占位，防并发 sweep 重复发送
			lease.cancelSentAt = now
		} else if !lease.cancelSentAt.IsZero() &&
			!now.Before(lease.cancelSentAt.Add(time.Duration(protocol.DefaultCancelSettleMs)*time.Millisecond)) {
			a.settle = true
		}
		actions = append(actions, a)
	}
	d.leaseMu.Unlock()

	handled := make(map[string]bool, len(actions))
	for _, a := range actions {
		handled[a.msgID] = true
		if a.sendCancel {
			d.sendLeaseCancel(a.msgID, a.reason)
		}
		// cancel 后保留有界竞态窗；下一轮 sweep 才允许 void/replacement，避免旧
		// handler 尚在执行时新 intrusive 与它并发驱动同账号。
		if a.settle {
			d.settleLeaseGap(a.msgID, now)
		}
	}
	return handled
}

func (d *Dispatcher) sendLeaseCancel(target string, reason protocol.CancelReason) {
	cmd, err := d.st.CmdByMsgID(target)
	if err != nil || cmd == nil || cmd.Status.Terminal() {
		d.clearLease(target)
		return
	}
	_, features, negotiated := d.sender.HandNegotiation(cmd.HandID)
	if !negotiated || !contains(features, string(protocol.FeatureCancel1)) {
		d.st.Audit("cancel_unnegotiated", cmd.HandID, target, string(protocol.FeatureCancel1))
		return
	}
	session, _, online := d.sender.HandSession(cmd.HandID)
	if !online {
		return
	}
	cancelMsgID := ids.NewMsgID()
	body := protocol.CancelBody{Ref: target, Reason: reason}
	env := d.envelope(protocol.KindCancel, cancelMsgID, &session, body)

	d.leaseMu.Lock()
	if lease := d.leases[target]; lease != nil {
		lease.cancelMsg = cancelMsgID
		d.cancelRef[cancelMsgID] = target
	}
	d.leaseMu.Unlock()
	if err := d.sender.SendEnvelope(cmd.HandID, env); err != nil {
		d.st.Audit("cancel_send_failed", cmd.HandID, target, err.Error())
		return
	}
	d.st.Audit("cancel_sent", cmd.HandID, target, fmt.Sprintf("cancelMsgId=%s reason=%s", cancelMsgID, reason))
}

func (d *Dispatcher) settleLeaseGap(msgID string, observedAt time.Time) {
	d.leaseMu.Lock()
	lease := d.leases[msgID]
	if lease != nil && lease.expiresAt.After(observedAt) {
		d.leaseMu.Unlock()
		return
	}
	d.leaseMu.Unlock()
	cmd, err := d.st.CmdByMsgID(msgID)
	if err != nil || cmd == nil || cmd.Status.Terminal() {
		d.clearLease(msgID)
		return
	}
	d.clearLease(msgID)
	if cmd.Class == string(protocol.ClassEffectful) {
		if cmd.IntentID != "" {
			if err := d.st.MoveEffectToVerification(cmd.MsgID, "lease gap 无目标 result", observedAt); err == nil {
				d.kickVerification(cmd.MsgID)
			}
		} else {
			d.markSuspect(*cmd, "lease gap 无目标 result")
		}
		return
	}
	d.voidAndRedispatch(*cmd, "lease gap 无目标 result", redispatchBackoff(cmd.RedispatchN+1))
}

func (d *Dispatcher) onCancelAck(handID string, ack protocol.AckBody) bool {
	d.leaseMu.Lock()
	target, ok := d.cancelRef[ack.Ref]
	d.leaseMu.Unlock()
	if !ok {
		return false
	}
	cmd, err := d.st.CmdByMsgID(target)
	if err != nil || cmd == nil || cmd.HandID != handID {
		d.st.Audit("cancel_ack_source_mismatch", handID, target, fmt.Sprintf("cancelMsgId=%s", ack.Ref))
		return true
	}
	d.leaseMu.Lock()
	delete(d.cancelRef, ack.Ref)
	d.leaseMu.Unlock()
	// cancel 的 ack 只证明请求收妥；目标 result 是唯一能产生 canceled 的信号。
	d.st.Audit("cancel_ack", handID, target, fmt.Sprintf("cancelMsgId=%s status=%s", ack.Ref, ack.Status))
	return true
}

func (d *Dispatcher) clearLease(target string) {
	d.leaseMu.Lock()
	if lease := d.leases[target]; lease != nil && lease.cancelMsg != "" && lease.cancelMsg != "pending" {
		delete(d.cancelRef, lease.cancelMsg)
	}
	delete(d.leases, target)
	d.leaseMu.Unlock()
}
