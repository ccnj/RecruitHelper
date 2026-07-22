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

	// manualGreetingVerdictVerificationReason 是复用现有持久字段的一次性
	// 在途标记：它区分真人触发的单次正证读取与最多三轮自动验证。
	// 不新增协议/表字段，脑重启后 sweep 仍只恢复这一次读取。
	manualGreetingVerdictVerificationReason = "人工 resolvedOk 触发一次招呼正证读取"
)

// sweepEffectRecovery 同时保证 query/report 阶段与验证阶段有活性。
// 它只会重发无副作用的 query 和 metadata 指定的配套验证读，
// 从不因超时重发 SX。
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
		if cmd, _ := d.st.CmdByMsgID(ref); cmd != nil && isGreetingManualVerdictVerification(*cmd) {
			d.restoreGreetingManualVerification(*cmd, reason)
			return
		}
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
	manualGreetingVerdict := isGreetingManualVerdictVerification(*cmd)
	recordMiss := func(reason string) {
		if manualGreetingVerdict {
			d.restoreGreetingManualVerification(*cmd, reason)
			return
		}
		d.recordVerificationMiss(*cmd, reason)
	}
	intent, err := d.st.EffectIntentByID(cmd.IntentID)
	if err != nil || intent == nil {
		recordMiss("验证读缺少权威 intent")
		return
	}
	request := VerificationRequest{Command: *cmd, Intent: *intent}
	switch cmd.Name {
	case protocol.PrimChatSendMessage:
		if err := json.Unmarshal([]byte(cmd.Args), &request.Args); err != nil {
			recordMiss("验证读无法解析原始 args: " + err.Error())
			return
		}
		if err := json.Unmarshal([]byte(cmd.Guards), &request.Guards); err != nil {
			recordMiss("验证读无法解析原始 guards: " + err.Error())
			return
		}
	case protocol.PrimChatSendGreeting:
		var greetingArgs protocol.ChatSendGreetingArgs
		if err := json.Unmarshal([]byte(cmd.Args), &greetingArgs); err != nil {
			recordMiss("验证读无法解析招呼 args: " + err.Error())
			return
		}
		request.GreetingArgs = &greetingArgs
	default:
		recordMiss("真实副作用原语没有验证实现")
		return
	}
	observation, verifyErr := d.verifier.Verify(ctx, request)
	if verifyErr != nil {
		// 自动验证可等待同一持久 child；真人 resolvedOk 只授权恰好
		// 一次读取，超时/child pending 也必须回 suspect，不能安排 nextAt。
		if !manualGreetingVerdict && (errors.Is(verifyErr, context.Canceled) ||
			errors.Is(verifyErr, context.DeadlineExceeded) ||
			errors.Is(verifyErr, store.ErrVerificationAlreadyRunning)) {
			if child, _ := d.st.VerificationChildForParent(ref); child != nil {
				nextAt := time.Now().Add(verificationRetryDelay)
				if err := d.st.DeferEffectVerification(ref, "验证 child 仍在途，等待持久终局", nextAt); err == nil {
					d.st.Audit("effect_verification_child_pending", cmd.HandID, ref, child.MsgID)
					return
				}
			}
		}
		recordMiss("验证读失败: " + verifyErr.Error())
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
		recordMiss(reason)
		return
	}
	var commitErr error
	switch cmd.Name {
	case protocol.PrimChatSendMessage:
		result := protocol.ResultBody{
			Ref: ref, Status: protocol.ResultStatusOk, ExecMs: 0,
			Data: mustEncode(protocol.ChatSendMessageData{
				ConversationRef: intent.TargetRef, ContentHash: intent.SendFingerprint, ObservedAt: observation.ObservedAt,
			}),
			Evidence: []protocol.Evidence{{Type: string(protocol.SendMessageEvidenceTypeOutboundMessageObserved)}},
		}
		resultRaw, err := protocol.Encode(result)
		if err != nil {
			recordMiss("验证成功证词编码失败: " + err.Error())
			return
		}
		_, commitErr = d.st.ResolveEffectVerified(store.VerifiedEffectSuccess{
			Ref: ref,
			ConversationKey: store.ConversationKey{
				Platform: intent.Platform, AccountRef: intent.AccountRef, ConversationRef: intent.TargetRef,
			},
			Text: request.Args.Text, ContentHash: intent.SendFingerprint, ObservedAtMs: observation.ObservedAt,
			ResultBody: string(resultRaw), ResolutionReason: "verification fingerprint uniquely matched", At: time.Now(),
		})
	case protocol.PrimChatSendGreeting:
		if request.GreetingArgs == nil {
			recordMiss("招呼验证正证缺少原始目标")
			return
		}
		result := protocol.ResultBody{
			Ref: ref, Status: protocol.ResultStatusOk, ExecMs: 0,
			Data: mustEncode(protocol.ChatSendGreetingData{
				PlatformUserRef: request.GreetingArgs.PlatformUserRef,
				PositionRef:     request.GreetingArgs.PositionRef,
				ConversationRef: observation.ConversationRef,
				ContentHash:     intent.SendFingerprint, ObservedAt: observation.ObservedAt,
			}),
			Evidence: []protocol.Evidence{{Type: string(protocol.SendGreetingEvidenceTypeOutboundGreetingObserved)}},
		}
		resultRaw, err := protocol.Encode(result)
		if err != nil {
			recordMiss("招呼验证成功证词编码失败: " + err.Error())
			return
		}
		_, commitErr = d.st.ResolveGreetingVerified(store.VerifiedGreetingSuccess{
			Ref: ref, ProfileID: intent.TargetRef,
			PlatformUserRef: request.GreetingArgs.PlatformUserRef,
			PositionRef:     request.GreetingArgs.PositionRef,
			ConversationRef: observation.ConversationRef,
			Text:            request.GreetingArgs.Text, ContentHash: intent.SendFingerprint,
			ObservedAtMs: observation.ObservedAt, ResultBody: string(resultRaw),
			ResolutionReason: "verification greeting relationship matched", At: time.Now(),
		})
	}
	if commitErr != nil {
		if !errors.Is(commitErr, store.ErrRecoveryStateConflict) {
			d.st.Audit("effect_verification_commit_failed", cmd.HandID, ref, commitErr.Error())
			if manualGreetingVerdict {
				d.restoreGreetingManualVerification(*cmd, "招呼正证入账失败: "+commitErr.Error())
			}
		}
		return
	}
	d.st.Audit("effect_verification_confirmed", cmd.HandID, ref, observation.Reason)
	d.clearLease(ref)
	d.notifyByMsgID(ref)
	d.releaseSafeRecoveries(cmd.HandID)
}

func isGreetingManualVerdictVerification(cmd store.CmdRecord) bool {
	return cmd.Name == protocol.PrimChatSendGreeting && cmd.Status == store.CmdVerifying &&
		cmd.VerificationReason == manualGreetingVerdictVerificationReason
}

func (d *Dispatcher) restoreGreetingManualVerification(cmd store.CmdRecord, reason string) {
	err := d.st.RestoreGreetingManualVerificationSuspect(
		cmd.MsgID, manualGreetingVerdictVerificationReason, reason, time.Now(),
	)
	if err != nil {
		// 迟到 result 若已抢先给出权威终局，恢复事务必须输；不覆盖它。
		if !errors.Is(err, store.ErrRecoveryStateConflict) {
			d.st.Audit("greeting_manual_verification_restore_failed", cmd.HandID, cmd.MsgID, err.Error())
		}
		return
	}
	d.st.Audit("greeting_manual_verification_miss", cmd.HandID, cmd.MsgID, reason)
	d.clearLease(cmd.MsgID)
	d.notifyByMsgID(cmd.MsgID)
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
