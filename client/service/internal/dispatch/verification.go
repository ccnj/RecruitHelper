package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/logreport"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
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

	// handThreadPageSourceUnavailable 必须与手侧 zhilian.ts 抛出的错误名逐字一致：
	// 页面两级取数通道(timeline 组件 props 与任意组件的 Vuex getter)都拿不到数据时
	// 抛出它。手侧错误名进 result.Error.Message(前 500 字符原样保留)，经 RunError
	// 包装后仍在 verifyErr.Error() 内，故此处用子串匹配。
	//
	// 用字符串而非 ErrorCode 是知情的折衷：新增 code 会再次改动契约与 contractHash。
	// 匹配失败(错误名漂移、消息被改写)的后果是退回 recordMiss 转人工，即恢复原有的
	// 保守行为，不会造成多发，故该脆弱性方向安全。改手侧错误名时必须同步此处。
	handThreadPageSourceUnavailable = "thread_page_source_unavailable"
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
	case protocol.PrimChatSendWechatInvite:
		var args protocol.ChatSendWechatInviteArgs
		if err := json.Unmarshal([]byte(cmd.Args), &args); err != nil {
			recordMiss("验证读无法解析换微信邀请 args: " + err.Error())
			return
		}
		if err := json.Unmarshal([]byte(cmd.Guards), &request.Guards); err != nil {
			recordMiss("验证读无法解析换微信邀请 guards: " + err.Error())
			return
		}
		request.WechatInviteArgs = &args
	case protocol.PrimChatSendInviteCard:
		var args protocol.ChatSendInviteCardArgs
		if err := json.Unmarshal([]byte(cmd.Args), &args); err != nil {
			recordMiss("验证读无法解析邀面卡 args: " + err.Error())
			return
		}
		if err := json.Unmarshal([]byte(cmd.Guards), &request.Guards); err != nil {
			recordMiss("验证读无法解析邀面卡 guards: " + err.Error())
			return
		}
		request.InviteCardArgs = &args
	case protocol.PrimChatAcceptWechat:
		var args protocol.ChatAcceptWechatArgs
		if err := json.Unmarshal([]byte(cmd.Args), &args); err != nil {
			recordMiss("验证读无法解析接受微信请求 args: " + err.Error())
			return
		}
		if err := json.Unmarshal([]byte(cmd.Guards), &request.Guards); err != nil {
			recordMiss("验证读无法解析接受微信请求 guards: " + err.Error())
			return
		}
		request.AcceptWechatArgs = &args
	case protocol.PrimChatSendGreeting:
		var greetingArgs protocol.ChatSendGreetingArgs
		if err := json.Unmarshal([]byte(cmd.Args), &greetingArgs); err != nil {
			recordMiss("验证读无法解析招呼 args: " + err.Error())
			return
		}
		request.GreetingArgs = &greetingArgs
	case protocol.PrimJobPublishDraft:
		var args protocol.JobPrepareDraftArgs
		if err := json.Unmarshal([]byte(cmd.Args), &args); err != nil {
			recordMiss("验证读无法解析职位发布 args: " + err.Error())
			return
		}
		request.PublishDraftArgs = &args
	case protocol.PrimJobTakeOffline:
		var args protocol.JobTakeOfflineArgs
		if err := json.Unmarshal([]byte(cmd.Args), &args); err != nil {
			recordMiss("验证读无法解析职位下线 args: " + err.Error())
			return
		}
		request.TakeOfflineArgs = &args
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
		// 2026-08-03 甲方裁决:chat.sendMessage 的验证读,当页面两级取数通道整体
		// 失效、拿不到任何数据时判成功并收编,不重发、不转人工。
		//
		// 判据严格限定为"数据源不可用"这一种错误:能读到数据而不满足判据的一律
		// 走下面的 recordMiss 转人工——两者的差别是通道好不好,不是结论对不对。
		// 本例外只适用 chat.sendMessage,不扩及招呼语与其他原语。已知代价
		// (多气泡链、账本污染、无人工信号)与被否决的替代方案见
		// docs/验证读数据源失效判成功裁决-2026-08-03.md。
		if cmd.Name == protocol.PrimChatSendMessage &&
			strings.Contains(verifyErr.Error(), handThreadPageSourceUnavailable) {
			d.st.Audit("effect_verification_optimistic_ok", cmd.HandID, ref, "")
			observation = VerificationObservation{
				Confirmed:   true,
				ContentHash: intent.SendFingerprint,
				ObservedAt:  time.Now().UnixMilli(),
				Reason:      "验证读数据源整体失效,按 2026-08-03 裁决乐观判定为已发送",
			}
		} else {
			recordMiss("验证读失败: " + verifyErr.Error())
			return
		}
	}
	if observation.DeliveryRejectedTs != nil {
		// 「拒收通知判失败」(AGENTS 防护成本预算第 9 条,2026-08-11 甲方裁决):
		// 平台明确呈现的可信失败,确定性收场,不走 miss→suspect。
		d.resolveDeliveryRejected(*cmd, *intent, *observation.DeliveryRejectedTs, observation.Reason)
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
		// 命中行的 sourceKey(契约 §4.5)随正证收编;乐观判定路径的观察没有
		// 页面数据,这里保持空、落账 NULL。字段是 optional,非法值只弃不阻断。
		sendSourceKey := ""
		if validLowerHex64(observation.SourceKey) {
			sendSourceKey = observation.SourceKey
		}
		result := protocol.ResultBody{
			Ref: ref, Status: protocol.ResultStatusOk, ExecMs: 0,
			Data: mustEncode(protocol.ChatSendMessageData{
				ConversationRef: intent.TargetRef, ContentHash: intent.SendFingerprint,
				SourceKey: sendSourceKey, ObservedAt: observation.ObservedAt,
				TsApprox: observation.PlatformTsMs,
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
			PlatformTsMs: observation.PlatformTsMs, SourceKey: sendSourceKey,
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
	case protocol.PrimChatSendWechatInvite:
		if request.WechatInviteArgs == nil || !validLowerHex64(observation.SourceKey) ||
			observation.Interview != nil {
			recordMiss("换微信邀请验证正证缺少稳定卡片身份或携带了非法邀面参数")
			return
		}
		data := protocol.ChatSendWechatInviteData{
			ConversationRef: request.WechatInviteArgs.ConversationRef,
			ContentHash:     observation.ContentHash,
			SourceKey:       observation.SourceKey,
			ObservedAt:      observation.ObservedAt,
			TsApprox:        observation.PlatformTsMs,
		}
		result := protocol.ResultBody{
			Ref: ref, Status: protocol.ResultStatusOk, ExecMs: 0,
			Data: mustEncode(data),
			Evidence: []protocol.Evidence{{
				Type: string(protocol.SendWechatInviteEvidenceTypeOutboundWechatInviteObserved),
			}},
		}
		resultRaw, err := protocol.Encode(result)
		if err != nil {
			recordMiss("换微信邀请验证成功证词编码失败: " + err.Error())
			return
		}
		_, commitErr = d.st.ResolveCardVerified(store.VerifiedCardSuccess{
			Ref: ref,
			ConversationKey: store.ConversationKey{
				Platform: intent.Platform, AccountRef: intent.AccountRef, ConversationRef: intent.TargetRef,
			},
			Card: store.CardResultMutation{
				ConversationRef: request.WechatInviteArgs.ConversationRef,
				CardType:        "wechatExchange", CardState: "pending",
				ContentHash: observation.ContentHash, SourceKey: observation.SourceKey,
				PlatformTsMs: observation.PlatformTsMs,
			},
			ResultBody: string(resultRaw), ResolutionReason: "verification wechat card uniquely matched",
			At: time.Now(),
		})
	case protocol.PrimChatSendInviteCard:
		if request.InviteCardArgs == nil || !validLowerHex64(observation.SourceKey) ||
			observation.Interview == nil || *observation.Interview != request.InviteCardArgs.Interview {
			recordMiss("邀面卡验证正证缺少稳定卡片身份或参数不一致")
			return
		}
		interview := request.InviteCardArgs.Interview
		data := protocol.ChatSendInviteCardData{
			ConversationRef: request.InviteCardArgs.ConversationRef,
			ContentHash:     observation.ContentHash,
			SourceKey:       observation.SourceKey,
			Interview:       interview,
			ObservedAt:      observation.ObservedAt,
			TsApprox:        observation.PlatformTsMs,
		}
		result := protocol.ResultBody{
			Ref: ref, Status: protocol.ResultStatusOk, ExecMs: 0,
			Data: mustEncode(data),
			Evidence: []protocol.Evidence{{
				Type: string(protocol.SendInviteCardEvidenceTypeOutboundInterviewInviteObserved),
			}},
		}
		resultRaw, err := protocol.Encode(result)
		if err != nil {
			recordMiss("邀面卡验证成功证词编码失败: " + err.Error())
			return
		}
		startsAt, endsAt, method := interview.StartsAt, interview.EndsAt, string(interview.Method)
		_, commitErr = d.st.ResolveCardVerified(store.VerifiedCardSuccess{
			Ref: ref,
			ConversationKey: store.ConversationKey{
				Platform: intent.Platform, AccountRef: intent.AccountRef, ConversationRef: intent.TargetRef,
			},
			Card: store.CardResultMutation{
				ConversationRef: request.InviteCardArgs.ConversationRef,
				CardType:        "interviewInvite", CardState: "unknown",
				ContentHash: observation.ContentHash, SourceKey: observation.SourceKey,
				InterviewStartsAtMs: &startsAt,
				InterviewEndsAtMs:   syncledger.OptionalEndsAt(endsAt),
				InterviewMethod:     &method,
				PlatformTsMs:        observation.PlatformTsMs,
			},
			ResultBody: string(resultRaw), ResolutionReason: "verification interview card uniquely matched",
			At: time.Now(),
		})
	case protocol.PrimChatAcceptWechat:
		if request.AcceptWechatArgs == nil ||
			!validLowerHex64(observation.SourceKey) ||
			strings.TrimSpace(observation.PeerWechat) == "" ||
			observation.Interview != nil {
			recordMiss("接受微信请求验证正证缺少稳定交换身份或候选人联系方式")
			return
		}
		data := protocol.ChatAcceptWechatData{
			ConversationRef:   request.AcceptWechatArgs.ConversationRef,
			RequestSourceKey:  request.AcceptWechatArgs.RequestSourceKey,
			ExchangeSourceKey: observation.SourceKey,
			PeerWechat:        observation.PeerWechat,
			ObservedAt:        observation.ObservedAt,
		}
		result := protocol.ResultBody{
			Ref: ref, Status: protocol.ResultStatusOk, ExecMs: 0,
			Data: mustEncode(data),
			Evidence: []protocol.Evidence{{
				Type: string(protocol.AcceptWechatEvidenceTypeCandidateWechatRequestAcceptedObserved),
			}},
		}
		resultRaw, err := protocol.Encode(result)
		if err != nil {
			recordMiss("接受微信请求验证成功证词编码失败: " + err.Error())
			return
		}
		_, commitErr = d.st.ResolveWechatAcceptVerified(
			store.VerifiedWechatAcceptSuccess{
				Ref: ref,
				ConversationKey: store.ConversationKey{
					Platform: intent.Platform, AccountRef: intent.AccountRef,
					ConversationRef: intent.TargetRef,
				},
				RequestSourceKey:  request.AcceptWechatArgs.RequestSourceKey,
				ExchangeSourceKey: observation.SourceKey,
				PeerWechat:        observation.PeerWechat,
				ObservedAtMs:      observation.ObservedAt,
				ResultBody:        string(resultRaw),
				ResolutionReason:  "verification wechat exchange uniquely matched",
				At:                time.Now(),
			},
		)
	}
	if commitErr != nil {
		if errors.Is(commitErr, store.ErrRecoveryStateConflict) {
			// 迟到 result 或人工裁决已抢先给出权威终局，本轮验证让位。
			return
		}
		d.st.Audit("effect_verification_commit_failed", cmd.HandID, ref, commitErr.Error())
		// 正证已经读到，入账失败只是本轮没能落库。必须退避并计入轮次上限：
		// 裸 return 会让命令留在 verifying 且 nextAt 停在过去，sweep 下一轮
		// 立刻判到期再验一次，形成每秒一轮、永不收敛也永不转人工的永动机
		// （2026-08-01 客户机 195 轮 readThread 事故）。轮次耗尽照常转
		// suspect 交人工，理由写明消息已在页面读到，避免人工把方向判反去补发。
		recordMiss("验证正证已读到但入账失败(消息已发出，勿重发): " + commitErr.Error())
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
	if suspect && cmd.Name == protocol.PrimJobTakeOffline {
		// 甲方 2026-08-13 裁决:下线只是锦上添花,失败记一笔即可。它是唯一不产
		// 人工票据的 effectful 原语——失效方向是"职位仍在线",少做而非多做副作用,
		// 且平台上重复下线不可能发生(已下线的行没有下线入口)。这里把刚落成的
		// suspect 就地自动裁决为 resolvedFailed,不留待人裁。
		d.autoResolveTakeOfflineUnconfirmed(cmd, reason)
		return
	}
	if suspect {
		d.clearLease(cmd.MsgID)
		d.notifyByMsgID(cmd.MsgID)
		d.releaseSafeRecoveries(cmd.HandID)
		// 与 fault.go markSuspect 的 suspect.created 同一命名事件:验证耗尽转
		// suspect 此前只有 Audit 行,日志上报看不见。handError 随行手侧终局的
		// error.message——招呼 suspect 的现场纪要(平台浮层原话、弹窗状态)就在
		// 这里。页面文本可进普通日志与日志上报,偶发夹带候选人信息不扫描
		// 不过滤(2026-08-07 甲方裁决)。
		attrs := []any{
			logreport.Event(logreport.EventSuspectCreated),
			"handId", cmd.HandID, "msgId", cmd.MsgID, "name", cmd.Name,
			"idemKey", cmd.IdemKey, "reason", "verification exhausted: " + reason,
		}
		if msg := handResultErrorMessage(cmd.ResultBody); msg != "" {
			attrs = append(attrs, "handError", msg)
		}
		slog.Warn("命令转 suspect(验证耗尽,永不自动重试,待人工裁决)", attrs...)
		if suspectSceneCaptureWanted(cmd.Name) {
			// 现场截图取证(2026-08-07 甲方裁决):异步、独立超时、失败即弃;
			// 不阻塞批次、不重试、不影响任何业务判定。
			go d.captureSuspectScene(cmd)
		}
	}
}

// autoResolveTakeOfflineUnconfirmed 把下线的"结果未确认"就地收成终局失败。
//
// 它走的是与人工裁决同一个 store 入口,只是裁决者是脑而不是人:落账形态、
// idemKey 解冻与审计口径都与人点一次「判失败」完全一致,不新增第二套状态。
//
// 判 failed 而实际已下线的账面误差,甲方 2026-08-13 知情接受:下线是幂等的,
// 判错也不触发任何后续动作——本原语没有任何自动重试路径,而人若照账去手动
// 下线,平台上那一行早已没有下线入口。
func (d *Dispatcher) autoResolveTakeOfflineUnconfirmed(cmd store.CmdRecord, reason string) {
	now := time.Now()
	if err := d.st.ResolveJobPublishSuspectVerdict(cmd.MsgID, store.CmdResolvedFailed, now); err != nil {
		// 自动裁决没落上就让它留在 suspect:那是安全的一侧(有人能看见),
		// 但必须响亮报出来,否则队列里会悄悄多一条没人认领的票。
		d.st.Audit("job_offline_auto_resolve_failed", cmd.HandID, cmd.MsgID, err.Error())
		slog.Warn("职位下线自动收束失败,该条留在 suspect 待人工",
			"handId", cmd.HandID, "msgId", cmd.MsgID, "err", err)
		d.clearLease(cmd.MsgID)
		d.notifyByMsgID(cmd.MsgID)
		d.releaseSafeRecoveries(cmd.HandID)
		return
	}
	d.st.Audit("job_offline_unconfirmed", cmd.HandID, cmd.MsgID, reason)
	d.clearLease(cmd.MsgID)
	d.notifyByMsgID(cmd.MsgID)
	d.releaseSafeRecoveries(cmd.HandID)
	slog.Warn("职位下线未取得正证,按裁决记一笔终局失败(不转人工)",
		"handId", cmd.HandID, "msgId", cmd.MsgID, "idemKey", cmd.IdemKey, "reason", reason)
}

// resolveDeliveryRejected 把「拒收通知判失败」(AGENTS 防护成本预算第 9 条,
// 2026-08-11 甲方裁决)落为确定性失败终局,并触发「被拉黑」业务事件(推进态
// 就地归档「拉黑」,服务态与归档态无操作,见沟通逻辑规格 v4 事件表)。事件
// 应用在终局事务之外:中间崩溃只丢事件不丢终局,该候选人下一次发送会再次
// 判拒收并补上事件,方向是少做。
func (d *Dispatcher) resolveDeliveryRejected(
	cmd store.CmdRecord,
	intent store.EffectIntent,
	noticeTs int64,
	reason string,
) {
	if err := d.st.ResolveEffectDeliveryRejected(cmd.MsgID, reason, time.Now()); err != nil {
		// 终局落不下就退回原路径按 miss 记账:耗尽后 suspect 转人工,不多发。
		d.recordVerificationMiss(cmd, "拒收终局落账失败: "+err.Error())
		return
	}
	d.st.Audit("effect_delivery_rejected", cmd.HandID, cmd.MsgID,
		fmt.Sprintf("idemKey=%s noticeTs=%d", cmd.IdemKey, noticeTs))
	slog.Warn("发送被平台拒收(候选人已拉黑),确定性失败收场,不产生 suspect",
		"handId", cmd.HandID, "msgId", cmd.MsgID, "name", cmd.Name,
		"idemKey", cmd.IdemKey, "noticeTs", noticeTs)
	d.clearLease(cmd.MsgID)
	d.notifyByMsgID(cmd.MsgID)
	d.releaseSafeRecoveries(cmd.HandID)

	key := store.ConversationKey{
		Platform: intent.Platform, AccountRef: intent.AccountRef, ConversationRef: intent.TargetRef,
	}
	profile, err := d.st.CandidateProfileByConversation(key)
	if err != nil || profile == nil {
		detail := "会话无对应档案"
		if err != nil {
			detail = "档案查询失败: " + err.Error()
		}
		d.st.Audit("candidate_blacklisted_event_skipped", cmd.HandID, cmd.MsgID, detail)
		return
	}
	occurred := time.UnixMilli(noticeTs)
	if _, err := d.st.ApplyCommunicationV4BusinessEvent(store.ApplyCommunicationV4BusinessEventRequest{
		ProfileID: profile.ProfileID,
		Event: communication.BusinessEvent{
			Key:        "blacklisted:" + intent.IdemKey,
			Kind:       communication.EventCandidateBlacklisted,
			Source:     communication.EventSourcePlatformStatus,
			OccurredAt: &occurred,
		},
		AppliedAt: time.Now(),
	}); err != nil {
		d.st.Audit("candidate_blacklisted_event_failed", cmd.HandID, cmd.MsgID, err.Error())
		return
	}
	d.st.Audit("candidate_blacklisted", cmd.HandID, cmd.MsgID, "profileId="+profile.ProfileID)
}

// handResultErrorMessage 从命令终局 result 的审计 JSON 里取手侧 error.message。
// 解析失败或字段缺失都返回空串:它只服务日志随行,不参与任何裁决。
func handResultErrorMessage(resultBody string) string {
	if resultBody == "" {
		return ""
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(resultBody), &body); err != nil {
		return ""
	}
	return body.Error.Message
}

func mustEncode(value any) json.RawMessage {
	raw, err := protocol.Encode(value)
	if err != nil {
		panic(err)
	}
	return raw
}
