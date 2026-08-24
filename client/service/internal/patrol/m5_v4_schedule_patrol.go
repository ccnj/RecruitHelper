package patrol

import (
	"context"
	"log/slog"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
)

const communicationV4ManualScheduleMaterialUnavailable = "scheduleMaterialUnavailable"
const communicationV4ManualScheduleProviderUnavailable = "scheduleProviderUnavailable"

// processCommunicationV4Schedule handles only a profile whose current ledger
// tail contains no candidate input. Archive remains a separate atomic path;
// every candidate-visible tier is frozen through SchedulePlan and then drained
// by the ordinary EventAction/WAL rail.
func (a *roundActor) processCommunicationV4Schedule(
	ctx context.Context,
	target store.CommunicationTarget,
) error {
	archived, err := a.processCommunicationV4ScheduleArchive(target, false)
	if err != nil || archived {
		return err
	}
	evaluatedAt := a.manager.now()
	preflight, err := communication.EvaluateV4Schedule(
		communication.V4ScheduleInput{
			ProfileKey:          target.Profile.ProfileID,
			State:               target.Aggregate.State,
			ProjectedThroughSeq: target.Aggregate.ProjectedThroughSeq,
			Now:                 evaluatedAt,
			HasPendingDialogue:  false,
			Reply:               communication.ReplyAdvice{State: communication.AdviceAbsent},
			// 预评估与冻结同源传入跟催文案；冻结事务内仍会带称呼重渲染。
			InterviewFollowupTexts: communicationV4InterviewFollowupTexts,
		},
	)
	if err != nil {
		return err
	}
	if preflight.Status == communication.V4ScheduleNoAction {
		return nil
	}
	if preflight.Status == communication.V4ScheduleManualRequired &&
		preflight.ManualReason == communication.V4ManualScheduleClockUnknown {
		// A missing, approximate or future platform timestamp does not prove a
		// follow-up tier is due. Preserve the existing conservative behavior:
		// wait for a later projection instead of isolating the whole profile.
		return nil
	}
	if preflight.Status == communication.V4ScheduleActionsPlanned &&
		len(preflight.Actions) == 1 &&
		preflight.Actions[0].Kind == communication.V4ActionArchive {
		return store.ErrCommunicationV4Conflict
	}

	material, ready, err := a.manager.store.CommunicationAIMaterialForProfile(
		target.Profile.ProfileID,
	)
	if err != nil {
		return err
	}
	if !ready {
		// 材料未就绪是本机短缺(2026-08-02 甲方裁决):不冻结候选人,本轮
		// 跳过,材料补齐后下一巡检轮自然续跑。
		slog.Warn("时刻表轮跳过:AI 材料未就绪,等下轮巡检重试",
			"profileId", target.Profile.ProfileID,
			"reason", communicationV4ManualScheduleMaterialUnavailable)
		return nil
	}
	baseRequest := store.FreezeCommunicationV4SchedulePlanRequest{
		ProfileID:                   target.Profile.ProfileID,
		ConversationRef:             target.Conversation.ConversationRef,
		ExpectedRevision:            target.Aggregate.Revision,
		ExpectedProjectedThroughSeq: target.Aggregate.ProjectedThroughSeq,
		ContextRevisionHash:         material.ContextRevision.RevisionHash,
		HasPendingDialogue:          false,
		Reply:                       communication.ReplyAdvice{State: communication.AdviceAbsent},
		InterviewFollowupTexts:      communicationV4InterviewFollowupTexts,
		EvaluatedAt:                 evaluatedAt,
		FrozenAt:                    a.manager.now(),
	}
	result, err := a.manager.store.FreezeCommunicationV4SchedulePlan(baseRequest)
	if err != nil {
		return err
	}
	switch result.Decision.Status {
	case communication.V4ScheduleNoAction,
		communication.V4ScheduleActionsPlanned:
		return nil
	case communication.V4ScheduleManualRequired:
		if result.Decision.ManualReason == communication.V4ManualScheduleClockUnknown {
			// 预检路径(本函数开头)对同一原因是优雅等待;冻结路径不得同因
			// 不同罚(2026-08-02 甲方裁决)。平台时钟不确定只说明本轮无法证明
			// 跟催已到期,等下一次投影即可。
			slog.Warn("时刻表轮跳过:平台时钟不确定,等下轮巡检重试",
				"profileId", target.Profile.ProfileID,
				"reason", string(result.Decision.ManualReason))
			return nil
		}
		return a.manager.store.MarkCommunicationV4AutomationManualRequired(
			target.Profile.ProfileID,
			string(result.Decision.ManualReason),
			a.manager.now(),
		)
	case communication.V4ScheduleWaitingAdvice:
		if result.Decision.NextAdvice != communication.V4AdviceSilenceFollowup ||
			result.Decision.AdviceKey == "" {
			return store.ErrCommunicationV4Conflict
		}
		return a.processCommunicationV4SilenceAdvice(
			ctx,
			target,
			material,
			baseRequest,
		)
	default:
		return store.ErrCommunicationV4Conflict
	}
}

func (a *roundActor) processCommunicationV4SilenceAdvice(
	ctx context.Context,
	target store.CommunicationTarget,
	material store.CommunicationAIMaterial,
	freezeRequest store.FreezeCommunicationV4SchedulePlanRequest,
) error {
	// 沉默追问属回复族:次聪明已装配即走次聪明,未配置回落客户级引擎快照
	// (AGENTS.md 次聪明段)。快照从头用到尾,账本记的就是实际调用的引擎。
	engine := a.manager.adviceFor(m5ai.PurposeSilenceFollowup)
	if engine == nil {
		// provider 配置缺失是本机短缺(2026-08-02 甲方裁决):不冻结候选人,
		// 配置补齐重启后下一巡检轮自然续跑。
		slog.Warn("沉默追问跳过:AI provider 未配置,等下轮巡检重试",
			"profileId", target.Profile.ProfileID,
			"reason", communicationV4ManualScheduleProviderUnavailable)
		return nil
	}
	contextRevision := m5ai.ContextRevision{
		ContextID:     material.ContextRevision.ContextID,
		RevisionHash:  material.ContextRevision.RevisionHash,
		SourceKind:    material.ContextRevision.SourceKind,
		SourceJobRef:  material.ContextRevision.SourceJobRef,
		DisplayName:   material.ContextRevision.DisplayName,
		Environment:   material.ContextRevision.Environment,
		SourcePackage: material.ContextRevision.SourcePackage,
		Communication: material.ContextRevision.Communication,
		CreatedAt:     material.ContextRevision.CreatedAt,
	}
	// 提示词与简历的渲染失败都是"世界干净"的纯计算失败(2026-08-02 甲方
	// 裁决):不冻结候选人,本轮跳过,配置或快照修复后下一巡检轮自然重试。
	prompt, err := m5ai.SilenceFollowupPrompt(contextRevision)
	if err != nil {
		slog.Warn("沉默追问跳过:提示词装配失败,等下轮巡检重试",
			"profileId", target.Profile.ProfileID,
			"reason", "silenceFollowupPromptUnavailable", "err", err)
		return nil
	}
	resumeJSON, err := m5ai.RenderResumeJSON(material.ResumeSnapshot.ResumeJSON)
	if err != nil {
		slog.Warn("沉默追问跳过:简历渲染失败,等下轮巡检重试",
			"profileId", target.Profile.ProfileID,
			"reason", "resumeRenderFailed", "err", err)
		return nil
	}
	content, err := m5ai.RenderSilenceFollowupPrompt(prompt, resumeJSON)
	if err != nil {
		slog.Warn("沉默追问跳过:提示词渲染失败,等下轮巡检重试",
			"profileId", target.Profile.ProfileID,
			"reason", "silenceFollowupRenderFailed", "err", err)
		return nil
	}
	reserved, err :=
		a.manager.store.ReserveCommunicationV4ScheduleAIInvocation(
			store.ReserveCommunicationV4ScheduleAIInvocationRequest{
				ProfileID:                   target.Profile.ProfileID,
				ConversationRef:             target.Conversation.ConversationRef,
				ExpectedRevision:            target.Aggregate.Revision,
				ExpectedProjectedThroughSeq: target.Aggregate.ProjectedThroughSeq,
				ContextRevisionHash:         material.ContextRevision.RevisionHash,
				ResumeSnapshotID:            material.ResumeSnapshot.SnapshotID,
				EvaluatedAt:                 freezeRequest.EvaluatedAt,
				Provider:                    engine.ProviderName(),
				Model:                       engine.ModelName(),
				InputHash:                   sha256Hex(content),
				CreatedAt:                   a.manager.now(),
			},
		)
	if err != nil {
		return err
	}
	invocation := reserved.Invocation
	if reserved.Created {
		if err := a.ensureDispatchAllowed(ctx); err != nil {
			return err
		}
		request := m5ai.CompletionRequest{
			InvocationID:        invocation.InvocationID,
			Purpose:             m5ai.PurposeSilenceFollowup,
			ContextRevisionHash: invocation.ContextRevisionHash,
			PromptRevision:      m5ai.SilenceFollowupRenderVersion,
			UserContent:         content,
			MaxOutputTokens:     m5ai.SilenceFollowupOutputTokenLimit,
		}
		startedAt := time.Now()
		var response m5ai.CompletionResponse
		var callErr error
		func() {
			a.manager.mu.Unlock()
			defer a.manager.mu.Lock()
			response, callErr = engine.CompleteJSON(ctx, request)
		}()
		completion := m5CompletionFromProvider(
			invocation.InvocationID,
			response,
			callErr,
			time.Since(startedAt),
			a.manager.now(),
		)
		suggestionText := ""
		if callErr == nil {
			suggestion, parseErr := m5ai.ParseSilenceFollowupSuggestion(
				response.JSONText,
			)
			if parseErr != nil {
				markBusinessParseFailure(&completion, parseErr)
			} else {
				suggestionText = suggestion.Text
			}
		}
		logAIInvocationOutcome(
			engine,
			m5ai.PurposeSilenceFollowup,
			completion,
			response.Diagnostics.TraceErrorCode,
		)
		completed, completeErr :=
			a.manager.store.CompleteCommunicationV4ScheduleAIInvocation(
				store.CompleteCommunicationV4ScheduleAIInvocationRequest{
					Completion:     completion,
					SuggestionText: suggestionText,
				},
			)
		if completeErr != nil {
			logAIInvocationPersistenceFailure(
				engine,
				m5ai.PurposeSilenceFollowup,
				completion,
				completeErr,
			)
			return completeErr
		}
		invocation = *completed
	} else if invocation.FinishedAt == nil {
		completed, completeErr :=
			a.manager.store.CompleteCommunicationV4ScheduleAIInvocation(
				store.CompleteCommunicationV4ScheduleAIInvocationRequest{
					Completion: store.AIInvocationCompletion{
						InvocationID: invocation.InvocationID,
						Status:       store.AIInvocationTransportFailed,
						ErrorClass:   "processInterrupted",
						FinishedAt:   a.manager.now(),
					},
				},
			)
		if completeErr != nil {
			return completeErr
		}
		invocation = *completed
	}
	if invocation.Status != store.AIInvocationOK ||
		invocation.SuggestionText == "" {
		reason := "silenceFollowupFailed"
		// AI 调用失败/输出可疑不再冻结候选人(2026-08-02 甲方裁决)。调用
		// 记账仍按 AdviceKey 单发即停:同一档期不会再触碰 provider,收敛靠
		// 候选人回复或时刻表推进到下一档期铸出新 AdviceKey。
		slog.Warn("沉默追问跳过:AI 调用未产出可用话术,本轮不冻结",
			"profileId", target.Profile.ProfileID, "reason", reason)
		return nil
	}
	freezeRequest.EvaluatedAt = invocation.EvaluatedAt
	freezeRequest.FrozenAt = a.manager.now()
	freezeRequest.Reply = communication.ReplyAdvice{
		State:      communication.AdviceOK,
		Suggestion: m5ai.ReplySuggestion{Text: invocation.SuggestionText},
	}
	result, err := a.manager.store.FreezeCommunicationV4SchedulePlan(
		freezeRequest,
	)
	if err != nil {
		return err
	}
	if result.Decision.Status != communication.V4ScheduleActionsPlanned ||
		result.Plan == nil ||
		len(result.Actions) == 0 {
		return store.ErrCommunicationV4Conflict
	}
	return nil
}
