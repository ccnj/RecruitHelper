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
	return nil
}

func (a *roundActor) processCommunicationV4Target(
	ctx context.Context,
	target store.CommunicationTarget,
) error {
	archived, err := a.processCommunicationV4Fallback(target)
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

	if a.manager.advice == nil {
		// Provider configuration is a process dependency. Do not freeze a
		// durable turn until this process can continue it.
		return nil
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
		return nil
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
	for index := range boundary {
		message := boundary[index]
		switch {
		case message.Direction == "system":
		case message.Direction == "in" && message.Kind == "system":
		case message.Direction == "in":
			hasCandidateInput = true
		default:
			return a.manager.store.MarkCommunicationV4AutomationManualRequired(
				target.Profile.ProfileID,
				communicationV4ManualInterleavedOutbound,
				a.manager.now(),
			)
		}
	}
	if !hasCandidateInput {
		// A system-only tail is not a candidate turn. Keep it in the
		// ledger and wait for a real inbound message.
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
		ContextRevisionHash: target.ContextRevision.RevisionHash,
		ResumeSnapshotID:    target.ResumeSnapshot.SnapshotID,
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

func (a *roundActor) processCommunicationV4Fallback(
	target store.CommunicationTarget,
) (bool, error) {
	decision, err := communication.EvaluateV4Schedule(communication.V4ScheduleInput{
		ProfileKey:         target.Profile.ProfileID,
		State:              target.Aggregate.State,
		Now:                a.manager.now(),
		HasPendingDialogue: true,
		Reply:              communication.ReplyAdvice{State: communication.AdviceAbsent},
	})
	if err != nil {
		return false, err
	}
	if decision.Status != communication.V4ScheduleActionsPlanned {
		return false, nil
	}
	if len(decision.Actions) != 1 ||
		decision.Actions[0].Kind != communication.V4ActionArchive ||
		decision.Actions[0].EndReason != communication.V4EndFallback {
		return false, store.ErrCommunicationV4Conflict
	}
	_, _, err = a.manager.store.ApplyCommunicationV4ArchiveAction(
		target.Profile.ProfileID,
		target.Aggregate.Revision,
		decision.Actions[0],
		a.manager.now(),
	)
	return err == nil, err
}
