package patrol

import (
	"context"
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
		return a.manager.store.MarkCommunicationV4AutomationManualRequired(
			target.Profile.ProfileID,
			communicationV4ManualScheduleMaterialUnavailable,
			a.manager.now(),
		)
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
	if a.manager.advice == nil {
		return a.manager.store.MarkCommunicationV4AutomationManualRequired(
			target.Profile.ProfileID,
			communicationV4ManualScheduleProviderUnavailable,
			a.manager.now(),
		)
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
	prompt, err := m5ai.SilenceFollowupPrompt(contextRevision)
	if err != nil {
		return a.manager.store.MarkCommunicationV4AutomationManualRequired(
			target.Profile.ProfileID,
			"silenceFollowupPromptUnavailable",
			a.manager.now(),
		)
	}
	resumeJSON, err := m5ai.RenderResumeJSON(material.ResumeSnapshot.ResumeJSON)
	if err != nil {
		return a.manager.store.MarkCommunicationV4AutomationManualRequired(
			target.Profile.ProfileID,
			"resumeRenderFailed",
			a.manager.now(),
		)
	}
	content, err := m5ai.RenderSilenceFollowupPrompt(prompt, resumeJSON)
	if err != nil {
		return a.manager.store.MarkCommunicationV4AutomationManualRequired(
			target.Profile.ProfileID,
			"silenceFollowupRenderFailed",
			a.manager.now(),
		)
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
				Provider:                    a.manager.advice.ProviderName(),
				Model:                       a.manager.advice.ModelName(),
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
			response, callErr = a.manager.advice.CompleteJSON(ctx, request)
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
		if callErr == nil && !reasoningUsageSafe(completion) {
			markReasoningUsageInvalidOutput(&completion)
			suggestionText = ""
		}
		logAIInvocationOutcome(
			a.manager.advice,
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
				a.manager.advice,
				m5ai.PurposeSilenceFollowup,
				completion,
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
		!communicationV4ScheduleReasoningSafe(invocation) ||
		invocation.SuggestionText == "" {
		reason := "silenceFollowupFailed"
		if !communicationV4ScheduleReasoningSafe(invocation) {
			reason = "reasoningUsageUnsafe"
		}
		return a.manager.store.MarkCommunicationV4AutomationManualRequired(
			target.Profile.ProfileID,
			reason,
			a.manager.now(),
		)
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

func communicationV4ScheduleReasoningSafe(
	invocation store.CommunicationV4ScheduleAIInvocation,
) bool {
	completion := store.AIInvocationCompletion{
		UsageShape:            invocation.UsageShape,
		ReasoningTokens:       invocation.ReasoningTokens,
		ReasoningContentEmpty: invocation.ReasoningContentEmpty,
	}
	return reasoningUsageSafe(completion)
}
