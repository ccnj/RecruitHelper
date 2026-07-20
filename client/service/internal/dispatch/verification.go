package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

const (
	recoveryQueryTimeout = 5 * time.Second
	recoveryQueryMax     = 3
	verificationTimeout  = 5 * time.Minute
)

// sweepEffectRecovery 同时保证 query/report 阶段与验证阶段有活性。
// 它只会重发无副作用的 query 和 readThread，从不因超时重发 SX。
func (d *Dispatcher) sweepEffectRecovery(now time.Time) {
	retry, verify, err := d.st.ExpireRecoveryQueries(now.Add(-recoveryQueryTimeout), now, recoveryQueryMax)
	if err != nil {
		d.st.Audit("reconcile_query_sweep_failed", "", "", err.Error())
		return
	}
	hands := make(map[string]struct{}, len(retry))
	for i := range retry {
		hands[retry[i].HandID] = struct{}{}
	}
	ready, readyErr := d.st.RecoveryQueriesReady(now)
	if readyErr != nil {
		d.st.Audit("reconcile_query_ready_scan_failed", "", "", readyErr.Error())
	} else {
		for i := range ready {
			hands[ready[i].HandID] = struct{}{}
		}
	}
	for handID := range hands {
		session, bootID, online := d.sender.HandSession(handID)
		witness, ok := d.handWitness(handID)
		if online && ok && witness.StoreID != "" && witness.OutboxPending == 0 {
			d.sendRecoveryQueriesAt(handID, session, bootID, witness.StoreID, now)
		}
	}
	for i := range verify {
		d.st.Audit("reconcile_report_timeout", verify[i].HandID, verify[i].MsgID,
			"query/report 重试耗尽，转结构化验证")
		d.kickVerification(verify[i].MsgID)
	}
	due, err := d.st.VerifyingEffectCommandsDue(now)
	if err != nil {
		d.st.Audit("effect_verification_scan_failed", "", "", err.Error())
		return
	}
	for i := range due {
		d.kickVerification(due[i].MsgID)
	}
}

func (d *Dispatcher) kickVerification(ref string) {
	if ref == "" {
		return
	}
	if d.verifier == nil {
		reason := "结构化验证器未接线，禁止在未知副作用下继续"
		if err := d.st.MarkEffectSuspect(ref, reason, time.Now()); err == nil {
			d.st.Audit("effect_verifier_unavailable", "", ref, reason)
			d.clearLease(ref)
			d.notifyByMsgID(ref)
			if cmd, _ := d.st.CmdByMsgID(ref); cmd != nil {
				d.releaseSafeRecoveries(cmd.HandID)
			}
		}
		return
	}
	d.verifyMu.Lock()
	if d.verifyRunning[ref] {
		d.verifyMu.Unlock()
		return
	}
	d.verifyRunning[ref] = true
	d.verifyMu.Unlock()
	go func() {
		defer func() {
			d.verifyMu.Lock()
			delete(d.verifyRunning, ref)
			d.verifyMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), verificationTimeout)
		defer cancel()
		d.verifyEffect(ctx, ref)
	}()
}

func (d *Dispatcher) verifyEffect(ctx context.Context, ref string) {
	cmd, err := d.st.CmdByMsgID(ref)
	if err != nil || cmd == nil || cmd.Status != store.CmdVerifying || cmd.IntentID == "" {
		return
	}
	intent, err := d.st.EffectIntentByID(cmd.IntentID)
	if err != nil || intent == nil {
		d.recordVerificationMiss(*cmd, "验证读缺少权威 intent")
		return
	}
	var args protocol.ChatSendMessageArgs
	var guards protocol.ChatSendMessageGuards
	if err := json.Unmarshal([]byte(cmd.Args), &args); err != nil {
		d.recordVerificationMiss(*cmd, "验证读无法解析原始 args: "+err.Error())
		return
	}
	if err := json.Unmarshal([]byte(cmd.Guards), &guards); err != nil {
		d.recordVerificationMiss(*cmd, "验证读无法解析原始 guards: "+err.Error())
		return
	}
	observation, verifyErr := d.verifier.Verify(ctx, VerificationRequest{
		Command: *cmd, Intent: *intent, Args: args, Guards: guards,
	})
	if verifyErr != nil {
		if errors.Is(verifyErr, context.Canceled) || errors.Is(verifyErr, context.DeadlineExceeded) ||
			errors.Is(verifyErr, store.ErrVerificationAlreadyRunning) {
			if child, _ := d.st.VerificationChildForParent(ref); child != nil {
				nextAt := time.Now().Add(verificationRetryDelay)
				if err := d.st.DeferEffectVerification(ref, "验证 child 仍在途，等待持久终局", nextAt); err == nil {
					d.st.Audit("effect_verification_child_pending", cmd.HandID, ref, child.MsgID)
					return
				}
			}
		}
		d.recordVerificationMiss(*cmd, "验证读失败: "+verifyErr.Error())
		return
	}
	if !observation.Confirmed || observation.ContentHash != intent.SendFingerprint {
		reason := observation.Reason
		if reason == "" {
			reason = "完整窗口未唯一命中目标正文"
		}
		if observation.Confirmed {
			reason = "验证命中指纹与权威 intent 不一致"
		}
		d.recordVerificationMiss(*cmd, reason)
		return
	}
	result := protocol.ResultBody{
		Ref: ref, Status: protocol.ResultStatusOk, ExecMs: 0,
		Data: mustEncode(protocol.ChatSendMessageData{
			ConversationRef: intent.TargetRef, ContentHash: intent.SendFingerprint, ObservedAt: observation.ObservedAt,
		}),
		Evidence: []protocol.Evidence{{Type: string(protocol.SendMessageEvidenceTypeOutboundMessageObserved)}},
	}
	resultRaw, err := protocol.Encode(result)
	if err != nil {
		d.recordVerificationMiss(*cmd, "验证成功证词编码失败: "+err.Error())
		return
	}
	_, err = d.st.ResolveEffectVerified(store.VerifiedEffectSuccess{
		Ref: ref,
		ConversationKey: store.ConversationKey{
			Platform: intent.Platform, AccountRef: intent.AccountRef, ConversationRef: intent.TargetRef,
		},
		Text: args.Text, ContentHash: intent.SendFingerprint, ObservedAtMs: observation.ObservedAt,
		ResultBody: string(resultRaw), ResolutionReason: "verification fingerprint uniquely matched", At: time.Now(),
	})
	if err != nil {
		if !errors.Is(err, store.ErrRecoveryStateConflict) {
			d.st.Audit("effect_verification_commit_failed", cmd.HandID, ref, err.Error())
		}
		return
	}
	d.st.Audit("effect_verification_confirmed", cmd.HandID, ref, observation.Reason)
	d.clearLease(ref)
	d.notifyByMsgID(ref)
	d.releaseSafeRecoveries(cmd.HandID)
}

func (d *Dispatcher) recordVerificationMiss(cmd store.CmdRecord, reason string) {
	meta, ok := protocol.Primitives[cmd.Name]
	maxAttempts := protocol.DefaultVerificationMaxRounds
	if ok && meta.VerificationMaxRounds > 0 {
		maxAttempts = meta.VerificationMaxRounds
	}
	now := time.Now()
	reviewAfter := time.UnixMilli(cmd.DeadlineMs + int64(protocol.DefaultSuspectGraceMs))
	suspect, err := d.st.RecordVerificationMiss(cmd.MsgID, reason, now.Add(verificationRetryDelay), reviewAfter, now, maxAttempts)
	if err != nil {
		if !errors.Is(err, store.ErrRecoveryStateConflict) {
			d.st.Audit("effect_verification_record_failed", cmd.HandID, cmd.MsgID, err.Error())
		}
		return
	}
	d.st.Audit("effect_verification_miss", cmd.HandID, cmd.MsgID,
		fmt.Sprintf("attempt=%d/%d reason=%s", cmd.VerificationN+1, maxAttempts, reason))
	if suspect {
		d.clearLease(cmd.MsgID)
		d.notifyByMsgID(cmd.MsgID)
		d.releaseSafeRecoveries(cmd.HandID)
	}
}

func mustEncode(value any) json.RawMessage {
	raw, err := protocol.Encode(value)
	if err != nil {
		panic(err)
	}
	return raw
}
