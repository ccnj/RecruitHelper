package patrol

import (
	"context"
	"errors"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
)

const (
	communicationV4ManualInterleavedOutbound = "interleavedOutboundBoundary"
	communicationV4ManualMissingOutbound     = "outboundBoundaryMissing"
)

// processCommunicationV4Targets is the production successor to the single
// M5TrialSelection slot. Targets are durable profile roots, so one account
// round can advance every ready profile independently and a restart simply
// re-enumerates the same set.
func (a *roundActor) processCommunicationV4Targets(ctx context.Context) error {
	if err := a.processCommunicationV4CardTransitions(ctx); err != nil {
		return err
	}
	// Card-transition projection may have just materialized fixed receipts and
	// dependent cards. Drain them before taking profile snapshots, so no target
	// is processed against state made stale by a newly confirmed effect.
	if err := a.drainCommunicationV4EventActions(ctx); err != nil {
		return err
	}
	targets, err := a.manager.store.CommunicationTargetsForAccount(a.key())
	if err != nil {
		return err
	}
	for index := range targets {
		if err := a.ensureDispatchAllowed(ctx); err != nil {
			return err
		}
		if err := a.processCommunicationV4Target(ctx, targets[index]); err != nil {
			return err
		}
	}
	// Dialogue and non-candidate-tail projection can materialize event actions
	// during the profile loop. They are dispatched only after all snapshots
	// have been consumed; no profile flow follows this drain in the same round.
	return a.drainCommunicationV4EventActions(ctx)
}

// processCommunicationV4Profile advances exactly one page-observed profile
// through the same card, event, dialogue and effect rails used by the explicit
// current-conversation entrypoint. It never enumerates account-wide targets.
func (a *roundActor) processCommunicationV4Profile(
	ctx context.Context,
	profileID string,
) error {
	if err := a.processCommunicationV4CardTransitionsForProfile(ctx, profileID); err != nil {
		return err
	}
	if err := a.drainCommunicationV4EventActionsForProfile(ctx, profileID); err != nil {
		return err
	}
	target, ready, err := a.manager.store.CommunicationTargetForProfile(profileID)
	if err != nil {
		if errors.Is(err, store.ErrCommunicationV4Missing) {
			return nil
		}
		return err
	}
	if !ready || target == nil {
		return nil
	}
	if err := a.processCommunicationV4Target(ctx, *target); err != nil {
		return err
	}
	return a.drainCommunicationV4EventActionsForProfile(ctx, profileID)
}

func (a *roundActor) processCommunicationV4Target(
	ctx context.Context,
	target store.CommunicationTarget,
) error {
	archived, err := a.processCommunicationV4ScheduleArchive(target, true)
	if err != nil || archived {
		return err
	}
	latest, err := a.manager.store.LatestDialogueTurnForProfile(target.Profile.ProfileID)
	if err != nil {
		return err
	}
	if latest != nil {
		v4Owned, err := a.manager.store.CommunicationV4OwnsTurn(latest.TurnID)
		if err != nil {
			return err
		}
		switch latest.Status {
		case store.DialogueTurnCollected, store.DialogueTurnClassified, store.DialogueTurnAdviceReady:
			if !v4Owned {
				return a.manager.store.MarkCommunicationV4AutomationManualRequired(
					target.Profile.ProfileID,
					"legacyTurnUnfinished",
					a.manager.now(),
				)
			}
			current, err := a.manager.store.RecheckDialogueTurnCurrent(
				latest.TurnID,
				a.manager.now(),
			)
			if err != nil || !current {
				return err
			}
			if err := a.setStage("advising"); err != nil {
				return err
			}
			return a.advanceM5Turn(ctx, *latest)
		case store.DialogueTurnDispatching:
			// A constructed effect is owned by the persistent recovery rail.
			return nil
		case store.DialogueTurnManualRequired:
			if v4Owned {
				// The aggregate should already be manual and disappear from
				// the next enumeration.
				return nil
			}
			reason := latest.FailureReason
			if reason == "" {
				reason = "legacyTurnManual"
			}
			return a.manager.store.MarkCommunicationV4AutomationManualRequired(
				target.Profile.ProfileID,
				reason,
				a.manager.now(),
			)
		case store.DialogueTurnCompleted, store.DialogueTurnSuperseded:
			// A later ledger boundary may open the next turn.
		default:
			return store.ErrDialogueTurnState
		}
	}

	key := store.ConversationKey{
		Platform: target.Profile.Platform, AccountRef: target.Profile.AccountRef,
		ConversationRef: target.Conversation.ConversationRef,
	}
	messages, err := a.manager.store.MessagesForConversation(key)
	if err != nil {
		return err
	}
	if len(messages) == 0 ||
		messages[len(messages)-1].Seq <= target.Aggregate.ProjectedThroughSeq {
		_, err := a.processCommunicationV4ScheduleArchive(target, false)
		return err
	}

	cursorIndex := -1
	for index := range messages {
		if messages[index].Seq == target.Aggregate.ProjectedThroughSeq {
			cursorIndex = index
			break
		}
	}
	if cursorIndex < 0 || messages[cursorIndex].Direction != "out" {
		return a.manager.store.MarkCommunicationV4AutomationManualRequired(
			target.Profile.ProfileID,
			communicationV4ManualMissingOutbound,
			a.manager.now(),
		)
	}
	boundary := messages[cursorIndex+1:]
	if len(boundary) == 0 {
		return nil
	}
	hasCandidateInput := false
	hasOutbound := false
	for index := range boundary {
		message := boundary[index]
		switch {
		case message.Direction == "system":
		case message.Direction == "in" && message.Kind == "system":
		case message.Direction == "in":
			hasCandidateInput = true
		case message.Direction == "out":
			hasOutbound = true
		default:
			return a.manager.store.MarkCommunicationV4AutomationManualRequired(
				target.Profile.ProfileID,
				communicationV4ManualInterleavedOutbound,
				a.manager.now(),
			)
		}
	}
	if hasCandidateInput && hasOutbound {
		return a.manager.store.MarkCommunicationV4AutomationManualRequired(
			target.Profile.ProfileID,
			communicationV4ManualInterleavedOutbound,
			a.manager.now(),
		)
	}
	if !hasCandidateInput {
		target, err = a.projectCommunicationV4NonCandidateTail(target, boundary)
		if err != nil || target.Aggregate.AutomationStatus != store.ProfileCommunicationAutomationActive {
			return err
		}
		_, err := a.processCommunicationV4ScheduleArchive(target, false)
		return err
	}
	material, materialReady, err := a.manager.store.CommunicationAIMaterialForProfile(
		target.Profile.ProfileID,
	)
	if err != nil {
		return err
	}
	if !materialReady {
		return nil
	}
	inbound, validBoundary := store.DialogueTurnCandidateMessages(boundary)
	if !validBoundary {
		return a.manager.store.MarkCommunicationV4AutomationManualRequired(
			target.Profile.ProfileID,
			communicationV4ManualInterleavedOutbound,
			a.manager.now(),
		)
	}

	digest, turnID, err := store.DialogueTurnIdentity(
		target.Profile.ProfileID,
		messages[cursorIndex],
		inbound,
	)
	if err != nil {
		return a.manager.store.MarkCommunicationV4AutomationManualRequired(
			target.Profile.ProfileID,
			communicationV4ManualInterleavedOutbound,
			a.manager.now(),
		)
	}
	recommended, err := m5ai.FreezeRecommendedTimeText(
		a.manager.now(),
		m5ai.GenerateDefaultSlots(a.manager.now()),
	)
	if err != nil {
		return a.manager.store.MarkCommunicationV4AutomationManualRequired(
			target.Profile.ProfileID,
			"scheduleRenderFailed",
			a.manager.now(),
		)
	}
	frozen, err := a.manager.store.FreezeCommunicationV4Turn(store.FreezeDialogueTurnRequest{
		TurnID: turnID, ProfileID: target.Profile.ProfileID,
		ConversationRef: target.Conversation.ConversationRef,
		InputDigest:     digest, HistoryThroughSeq: messages[cursorIndex].Seq,
		InboundFromSeq: inbound[0].Seq, InboundThroughSeq: boundary[len(boundary)-1].Seq,
		ContextRevisionHash: material.ContextRevision.RevisionHash,
		ResumeSnapshotID:    material.ResumeSnapshot.SnapshotID,
		RecommendedTimeText: recommended,
		RenderFormatVersion: m5ai.DialogueRenderFormatVersion,
		FrozenAt:            a.manager.now(),
	})
	if err != nil {
		if errors.Is(err, store.ErrDialogueTurnBinding) {
			return a.manager.store.MarkCommunicationV4AutomationManualRequired(
				target.Profile.ProfileID,
				"turnBoundaryChanged",
				a.manager.now(),
			)
		}
		if errors.Is(err, store.ErrDialogueTurnBudget) {
			return a.manager.store.MarkCommunicationV4AutomationManualRequired(
				target.Profile.ProfileID,
				"monthlyTurnBudgetBlocked",
				a.manager.now(),
			)
		}
		return err
	}
	if err := a.setStage("advising"); err != nil {
		return err
	}
	return a.advanceM5Turn(ctx, frozen.Turn)
}

func (a *roundActor) projectCommunicationV4NonCandidateTail(
	target store.CommunicationTarget,
	messages []store.Message,
) (store.CommunicationTarget, error) {
	for index := range messages {
		message := messages[index]
		event, err := communication.NormalizeLedgerMessage(communication.LedgerMessageFact{
			Seq: message.Seq, Direction: message.Direction, Kind: message.Kind,
			Text: message.Text, CardType: message.CardType, CardState: message.CardState,
			Origin: message.Origin, TsApproxMs: message.TsApproxMs,
		})
		if err != nil {
			return target, err
		}
		result, err := a.manager.store.ApplyCommunicationV4BusinessEvent(
			store.ApplyCommunicationV4BusinessEventRequest{
				ProfileID: target.Profile.ProfileID,
				Event:     event,
				AppliedAt: a.manager.now(),
			},
		)
		if err != nil {
			return target, err
		}
		target.Aggregate = result.Aggregate
		if target.Aggregate.AutomationStatus != store.ProfileCommunicationAutomationActive {
			return target, nil
		}
	}
	return target, nil
}

func (a *roundActor) processCommunicationV4ScheduleArchive(
	target store.CommunicationTarget,
	hasPendingDialogue bool,
) (bool, error) {
	evaluatedAt := a.manager.now()
	decision, err := communication.EvaluateV4Schedule(communication.V4ScheduleInput{
		ProfileKey:          target.Profile.ProfileID,
		State:               target.Aggregate.State,
		ProjectedThroughSeq: target.Aggregate.ProjectedThroughSeq,
		Now:                 evaluatedAt,
		HasPendingDialogue:  hasPendingDialogue,
		Reply:               communication.ReplyAdvice{State: communication.AdviceAbsent},
	})
	if err != nil {
		return false, err
	}
	if decision.Status != communication.V4ScheduleActionsPlanned {
		return false, nil
	}
	if len(decision.Actions) != 1 || decision.Actions[0].Kind != communication.V4ActionArchive {
		return false, nil
	}
	result, err := a.manager.store.ApplyCommunicationV4ArchiveAction(
		store.ApplyCommunicationV4ArchiveActionRequest{
			ProfileID:                   target.Profile.ProfileID,
			ConversationRef:             target.Conversation.ConversationRef,
			ExpectedRevision:            target.Aggregate.Revision,
			ExpectedProjectedThroughSeq: target.Aggregate.ProjectedThroughSeq,
			HasPendingDialogue:          hasPendingDialogue,
			Action:                      decision.Actions[0],
			EvaluatedAt:                 evaluatedAt,
			AppliedAt:                   a.manager.now(),
		},
	)
	return err == nil && result != nil, err
}
