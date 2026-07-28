package patrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

const m5InvocationAttempt = 1

const m5ResumeAttachmentHistoryText = "候选人已投递简历"

type m5PendingTurn struct {
	lastOutbound *store.Message
	inbound      []store.Message
	firstReal    *store.Message
	manualReason string
}

type m5TurnMaterial struct {
	profile      store.CandidateProfile
	revision     store.JobAIContextRevision
	snapshot     store.CandidateResumeSnapshot
	inputKind    store.DialogueTurnInputKind
	currentFacts []communication.LedgerMessageFact
	history      []m5ai.AdviceMessage
	current      []m5ai.AdviceMessage
	throughTurn  []m5ai.AdviceMessage
	sentGreeting string
}

// processM5Trial runs only after the account's M2 reconciliation has committed.
// Provider calls and hand waits release the manager's short actor lock; every
// authorization boundary is rechecked by the Store transaction that follows.
func (a *roundActor) processM5Trial(ctx context.Context) error {
	target, err := a.manager.store.ActiveM5TrialForAccount(a.key())
	if err != nil || target == nil {
		return err
	}
	key := store.ConversationKey{
		Platform: target.Profile.Platform, AccountRef: target.Profile.AccountRef,
		ConversationRef: target.Conversation.ConversationRef,
	}
	messages, err := a.manager.store.MessagesForConversation(key)
	if err != nil {
		return err
	}
	pending := inspectM5Pending(messages)
	if pending.firstReal != nil {
		changed, markErr := a.manager.store.MarkProfileCommunicating(store.MarkProfileCommunicatingRequest{
			ProfileID: target.Profile.ProfileID, ConversationRef: key.ConversationRef,
			MessageSeq: pending.firstReal.Seq, ObservedAt: a.manager.now(),
		})
		if markErr != nil {
			return markErr
		}
		target.Profile = changed.Profile
	}
	if pending.manualReason == "" {
		cardPending, cardErr := a.manager.store.HasPendingCardTransitionAfter(key, pending.lastOutbound.Seq)
		if cardErr != nil {
			return cardErr
		}
		if cardPending {
			pending.manualReason = "unsupportedCardTransition"
		}
	}

	latest, err := a.manager.store.LatestDialogueTurnForProfile(target.Profile.ProfileID)
	if err != nil {
		return err
	}
	if latest != nil && (latest.Status == store.DialogueTurnDispatching || latest.Status == store.DialogueTurnCompleted) {
		return nil
	}
	if latest != nil && dialogueTurnCanBecomeStale(latest.Status) {
		current, currentErr := a.manager.store.RecheckDialogueTurnCurrent(latest.TurnID, a.manager.now())
		if currentErr != nil {
			return currentErr
		}
		if !current {
			return nil
		}
	}
	if pending.manualReason != "" {
		if latest != nil && dialogueTurnCanBecomeStale(latest.Status) {
			return a.manager.store.MarkDialogueTurnManualRequired(latest.TurnID, pending.manualReason, a.manager.now())
		}
		return a.manager.store.MarkActiveM5TrialManualRequired(
			target.Profile.ProfileID, pending.manualReason, a.manager.now(),
		)
	}

	// Preserve batch 2A's pre-capture behavior even before a candidate replies.
	// When a capture succeeds, force a fresh patrol instead of freezing a turn
	// from the message snapshot read before the intrusive command.
	if target.Profile.ResumeCaptureState != store.ResumeCaptureCaptured {
		reused, reuseErr := a.manager.store.ReuseSourcingResumeForActiveM5Trial(
			target.Profile.ProfileID, a.manager.now(),
		)
		if reuseErr != nil {
			if errors.Is(reuseErr, store.ErrResumeCaptureBinding) {
				return a.manager.store.MarkActiveM5TrialManualRequired(
					target.Profile.ProfileID, "sourcingResumeBindingMismatch", a.manager.now(),
				)
			}
			return reuseErr
		}
		if reused.Status == store.SourcingResumeReuseAdopted {
			return a.scheduleM5Continuation()
		}
		if err := a.captureTrialResume(ctx); err != nil {
			return err
		}
		after, reloadErr := a.manager.store.ActiveM5TrialForAccount(a.key())
		if reloadErr != nil {
			return reloadErr
		}
		if after != nil && after.Profile.ResumeCaptureState == store.ResumeCaptureCaptured {
			return a.scheduleM5Continuation()
		}
		return nil
	}
	if len(pending.inbound) == 0 {
		return nil
	}
	// A missing local provider configuration is dependency unavailability, not
	// a test mode. Do not freeze a turn until the next client restart loads a
	// complete M5-A configuration.
	if a.manager.advice == nil {
		return nil
	}

	if target.Profile.BackendJobID == nil || strings.TrimSpace(*target.Profile.BackendJobID) == "" {
		return a.manager.store.MarkActiveM5TrialManualRequired(
			target.Profile.ProfileID, "contextNotBound", a.manager.now(),
		)
	}
	currentContext, err := a.manager.store.CurrentLegacyJobAIContextByBackendJobID(
		strings.TrimSpace(*target.Profile.BackendJobID),
	)
	if err != nil {
		return err
	}
	if currentContext == nil {
		return a.manager.store.MarkActiveM5TrialManualRequired(
			target.Profile.ProfileID, "contextNotBound", a.manager.now(),
		)
	}
	if target.Profile.ActiveResumeSnapshotID == nil {
		return a.manager.store.MarkActiveM5TrialManualRequired(
			target.Profile.ProfileID, "resumeSnapshotMissing", a.manager.now(),
		)
	}
	snapshot, err := a.manager.store.CandidateResumeSnapshotByID(target.Profile.ProfileID, *target.Profile.ActiveResumeSnapshotID)
	if err != nil {
		return err
	}
	if snapshot == nil {
		return a.manager.store.MarkActiveM5TrialManualRequired(
			target.Profile.ProfileID, "resumeSnapshotMissing", a.manager.now(),
		)
	}

	digest, turnID, err := m5TurnIdentity(target.Profile.ProfileID, pending)
	if err != nil {
		return err
	}
	turn, err := a.manager.store.DialogueTurnByID(turnID)
	if err != nil {
		return err
	}
	if turn == nil {
		recommended, freezeErr := m5ai.FreezeRecommendedTimeText(
			a.now, m5ai.GenerateDefaultSlots(a.now),
		)
		if freezeErr != nil {
			return a.manager.store.MarkActiveM5TrialManualRequired(
				target.Profile.ProfileID, "scheduleRenderFailed", a.manager.now(),
			)
		}
		frozen, freezeErr := a.manager.store.FreezeDialogueTurn(store.FreezeDialogueTurnRequest{
			TurnID: turnID, ProfileID: target.Profile.ProfileID, ConversationRef: key.ConversationRef,
			InputDigest: digest, HistoryThroughSeq: pending.lastOutbound.Seq,
			InboundFromSeq:      pending.inbound[0].Seq,
			InboundThroughSeq:   pending.inbound[len(pending.inbound)-1].Seq,
			ContextRevisionHash: currentContext.RevisionHash,
			ResumeSnapshotID:    snapshot.SnapshotID, RecommendedTimeText: recommended,
			RenderFormatVersion: m5ai.DialogueRenderFormatVersion, FrozenAt: a.now,
		})
		if freezeErr != nil {
			if errors.Is(freezeErr, store.ErrDialogueTurnBinding) {
				return a.manager.store.MarkActiveM5TrialManualRequired(
					target.Profile.ProfileID, "turnBoundaryChanged", a.manager.now(),
				)
			}
			return freezeErr
		}
		turn = &frozen.Turn
	}
	if err := a.setStage("advising"); err != nil {
		return err
	}
	return a.advanceM5Turn(ctx, *turn)
}

func inspectM5Pending(messages []store.Message) m5PendingTurn {
	result := m5PendingTurn{}
	for index := range messages {
		message := &messages[index]
		if message.Direction == "out" {
			result.lastOutbound = message
		}
		if store.IsM5RealCandidateMessage(*message) && result.firstReal == nil {
			copy := *message
			result.firstReal = &copy
		}
	}
	if result.lastOutbound == nil {
		result.manualReason = "sentGreetingMissing"
		return result
	}
	for index := range messages {
		message := messages[index]
		if message.Seq <= result.lastOutbound.Seq {
			continue
		}
		result.inbound = append(result.inbound, message)
	}
	if _, ok := store.DialogueTurnInputKindOf(result.inbound); !ok && len(result.inbound) > 0 {
		result.manualReason = m5UnsupportedTurnReason(result.inbound)
	}
	if result.manualReason != "" {
		result.inbound = nil
	}
	return result
}

func m5UnsupportedTurnReason(messages []store.Message) string {
	for i := range messages {
		if messages[i].Kind == "image" || messages[i].Kind == "voice" || messages[i].Kind == "file" {
			return "unsupportedMedia"
		}
	}
	return "unsupportedSemantic"
}

func dialogueTurnCanBecomeStale(status store.DialogueTurnStatus) bool {
	return status == store.DialogueTurnCollected || status == store.DialogueTurnClassified ||
		status == store.DialogueTurnAdviceReady
}

func (a *roundActor) scheduleM5Continuation() error {
	now := a.manager.now()
	return a.manager.store.MutateAccount(a.key(), func(account *store.Account) error {
		account.DirtyHint = true
		if account.NextPatrolAt == nil || account.NextPatrolAt.After(now) {
			account.NextPatrolAt = timePointer(now)
		}
		return nil
	})
}

func m5TurnIdentity(profileID string, pending m5PendingTurn) (string, string, error) {
	if strings.TrimSpace(profileID) == "" || pending.lastOutbound == nil || len(pending.inbound) == 0 {
		return "", "", store.ErrDialogueTurnInvalid
	}
	return store.DialogueTurnIdentity(profileID, *pending.lastOutbound, pending.inbound)
}

func (a *roundActor) advanceM5Turn(ctx context.Context, initial store.DialogueTurn) error {
	turn := initial
	for step := 0; step < 3; step++ {
		switch turn.Status {
		case store.DialogueTurnManualRequired, store.DialogueTurnSuperseded,
			store.DialogueTurnDispatching, store.DialogueTurnCompleted:
			return nil
		}
		if err := a.mayAdvanceM5Turn(ctx); err != nil {
			return err
		}
		nextV4Advice, v4Owned, err := a.manager.store.CommunicationV4NextAdvice(turn.TurnID)
		if err != nil {
			return err
		}
		if turn.Status == store.DialogueTurnAdviceReady {
			return a.dispatchM5Action(ctx, turn, false)
		}
		if turn.Status == store.DialogueTurnCollected &&
			v4Owned && nextV4Advice == communication.V4AdviceNone {
			// An event-driven branch is durably waiting for a prerequisite
			// action. Neither AI material nor provider configuration is part
			// of that deterministic continuation.
			return nil
		}
		material, err := a.loadM5TurnMaterial(turn)
		if err != nil {
			return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "renderInputUnavailable", a.manager.now())
		}
		facts := communication.FrozenTurnFacts{TurnID: turn.TurnID}
		for _, message := range material.current {
			facts.Messages = append(facts.Messages, communication.FrozenInboundMessage{
				Seq: message.Seq, Kind: communication.FrozenMessageKind(message.Kind), Text: message.Text,
			})
		}

		switch turn.Status {
		case store.DialogueTurnCollected:
			if material.inputKind == store.DialogueTurnInputResumeAttachment {
				decision, reduceErr := reduceM5ResumeTurn(turn, material, communication.ReplyAdvice{State: communication.AdviceAbsent})
				if reduceErr != nil || !m5ResumeReplyAdviceAuthorized(decision) {
					return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "reducerRejected", a.manager.now())
				}
				if _, err := a.manager.store.ApplyResumeBusinessClassification(turn.TurnID, a.manager.now()); err != nil {
					return err
				}
				break
			}
			decision, reduceErr := communication.Reduce(communication.ReduceInput{
				Turn: facts, Intent: communication.IntentAdvice{State: communication.AdviceAbsent},
				Reply: communication.ReplyAdvice{State: communication.AdviceAbsent},
			})
			if reduceErr != nil {
				return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "reducerRejected", a.manager.now())
			}
			if decision.NextAdvice == m5ai.PurposeIntent {
				if a.manager.advice == nil {
					return nil
				}
				if err := a.runM5IntentAdvice(ctx, turn, material, facts); err != nil {
					return err
				}
			} else {
				if _, err := a.manager.store.ApplyCodeClassification(store.CodeClassificationRequest{
					TurnID: turn.TurnID, Label: decision.IntentLabel, ClassifiedAt: a.manager.now(),
				}); err != nil {
					return err
				}
			}
		case store.DialogueTurnClassified:
			if v4Owned && nextV4Advice == communication.V4AdviceServiceReply {
				if a.manager.advice == nil {
					return nil
				}
				if err := a.runM5ReplyAdvice(
					ctx,
					turn,
					material,
					facts,
					communication.IntentAdvice{State: communication.AdviceAbsent},
					nextV4Advice,
				); err != nil {
					return err
				}
				break
			}
			if material.inputKind == store.DialogueTurnInputResumeAttachment {
				if turn.IntentLabel != m5ai.IntentInterested || turn.IntentSource != store.DialogueIntentBusinessEvent {
					return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "reducerStateConflict", a.manager.now())
				}
				decision, reduceErr := reduceM5ResumeTurn(turn, material, communication.ReplyAdvice{State: communication.AdviceAbsent})
				if reduceErr != nil || !m5ResumeReplyAdviceAuthorized(decision) {
					return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "reducerStateConflict", a.manager.now())
				}
				if a.manager.advice == nil {
					return nil
				}
				if err := a.runM5ReplyAdvice(
					ctx,
					turn,
					material,
					facts,
					communication.IntentAdvice{State: communication.AdviceAbsent},
					communication.V4AdviceReply,
				); err != nil {
					return err
				}
				break
			}
			intent := intentAdviceFromTurn(turn)
			decision, reduceErr := communication.Reduce(communication.ReduceInput{
				Turn: facts, Intent: intent,
				Reply: communication.ReplyAdvice{State: communication.AdviceAbsent},
			})
			if reduceErr != nil || decision.NextAdvice != m5ai.PurposeReply {
				return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "reducerStateConflict", a.manager.now())
			}
			if a.manager.advice == nil {
				return nil
			}
			if err := a.runM5ReplyAdvice(
				ctx,
				turn,
				material,
				facts,
				intent,
				communication.V4AdviceReply,
			); err != nil {
				return err
			}
		default:
			return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "turnStateUnknown", a.manager.now())
		}
		reloaded, err := a.manager.store.DialogueTurnByID(turn.TurnID)
		if err != nil || reloaded == nil {
			return err
		}
		turn = *reloaded
	}
	return nil
}

// mayAdvanceM5Turn calls the literal workflow member gate also used by the
// scoring, greeting-generation and greeting-send loops. It releases the actor
// lock before entering that gate to preserve the control lock order
// (workflow -> actor), then rechecks the actor generation after reacquiring it.
// Consequently a pause which linearizes while one provider call is in flight
// lets that call persist its result, but cannot authorize the next advice
// stage or a new pre-WAL action.
func (a *roundActor) mayAdvanceM5Turn(ctx context.Context) error {
	a.manager.gateMu.RLock()
	installed := a.manager.workflowMemberGate != nil
	a.manager.gateMu.RUnlock()
	if !installed {
		return nil
	}
	var gateErr error
	func() {
		a.manager.mu.Unlock()
		defer a.manager.mu.Lock()
		gateErr = a.manager.mayStartNextWorkflowMember()
	}()
	if gateErr != nil {
		return gateErr
	}
	return a.ensureDispatchAllowed(ctx)
}

func reduceM5ResumeTurn(
	turn store.DialogueTurn,
	material m5TurnMaterial,
	reply communication.ReplyAdvice,
) (communication.V4InboundTurnDecision, error) {
	if material.inputKind != store.DialogueTurnInputResumeAttachment || len(material.currentFacts) != 1 {
		return communication.V4InboundTurnDecision{}, communication.ErrInvalidV4StateTransition
	}
	return communication.ReduceV4InboundTurn(communication.V4InboundTurnInput{
		State:  communication.NewV4GreetedState(material.profile.GreetedAt),
		TurnID: turn.TurnID, Messages: material.currentFacts,
		Intent: communication.IntentAdvice{State: communication.AdviceAbsent}, Reply: reply,
	})
}

func m5ResumeReplyAdviceAuthorized(decision communication.V4InboundTurnDecision) bool {
	return decision.ManualReason == "" && len(decision.EventActions) == 0 &&
		decision.Dialogue.Status == communication.V4DialogueWaitingAdvice &&
		decision.Dialogue.NextAdvice == communication.V4AdviceReply &&
		decision.Dialogue.IntentLabel == m5ai.IntentInterested &&
		decision.Dialogue.IntentSource == communication.IntentSourceBusinessEvent &&
		len(decision.Dialogue.Actions) == 0
}

// dispatchM5Action 派发 turn 当前唯一的 planned 动作。withinChain 表示本次
// 调用是同一 turn 内紧接前项正证的链内推进：按《24点边界裁决-2026-07-28》
// 链内只做 ensureChainDispatchAllowed 复核（不查日窗口/日界），链首与次日
// 恢复轨调用传 false，仍走 workflow 成员闸与完整日界复核。
func (a *roundActor) dispatchM5Action(
	ctx context.Context,
	turn store.DialogueTurn,
	withinChain bool,
) error {
	action, err := a.manager.store.PlannedCommunicationActionByTurn(turn.TurnID)
	if err != nil {
		return err
	}
	if action == nil ||
		action.Status != store.CommunicationActionPlanned ||
		action.EffectIntentID != nil {
		return a.manager.store.MarkDialogueTurnManualRequired(
			turn.TurnID, "automaticActionUnavailable", a.manager.now(),
		)
	}
	switch action.Kind {
	case store.CommunicationActionReplyText:
		if _, ok := a.manager.runner.(AutomaticReplyRunner); !ok {
			// Pure reducer/store tests may intentionally stop at the
			// persisted action seam.
			return nil
		}
	case store.CommunicationActionInviteWechat, store.CommunicationActionInterviewInvite:
		if _, ok := a.manager.runner.(AutomaticCardRunner); !ok {
			return nil
		}
	default:
		return a.manager.store.MarkM5AutomaticActionManualRequired(
			action.ActionID,
			"automaticActionUnsupported",
			a.manager.now(),
		)
	}
	profile, err := a.manager.store.CandidateProfileByID(turn.ProfileID)
	if err != nil || profile == nil || profile.ConversationRef == nil ||
		*profile.ConversationRef != turn.ConversationRef {
		return a.manager.store.MarkM5AutomaticActionManualRequired(
			action.ActionID, "automaticBindingUnavailable", a.manager.now(),
		)
	}
	intentID, err := store.M5AutomaticIntentID(action.ActionID)
	if err != nil {
		return a.manager.store.MarkM5AutomaticActionManualRequired(
			action.ActionID, "automaticIntentInvalid", a.manager.now(),
		)
	}
	previousIntentID := ""
	if action.DependsOnActionID != nil {
		parent, parentErr := a.manager.store.CommunicationActionByID(
			*action.DependsOnActionID,
		)
		if parentErr != nil {
			return parentErr
		}
		if parent == nil ||
			parent.EffectIntentID == nil ||
			parent.Status != store.CommunicationActionSent {
			return a.manager.store.MarkM5AutomaticActionManualRequired(
				action.ActionID,
				"automaticDependencyUnavailable",
				a.manager.now(),
			)
		}
		previousIntentID = *parent.EffectIntentID
	} else {
		latest, latestErr := a.manager.store.LatestEffectIntent(
			profile.Platform,
			profile.AccountRef,
			turn.ConversationRef,
		)
		if latestErr != nil {
			return latestErr
		}
		if latest != nil {
			previousIntentID = latest.IntentID
		}
	}
	// The visible send interaction shares the same brain-owned pacing and
	// post-wait authorization recheck as the sourcing workflow. The hand still
	// receives one command and owns no business timer. Within a chain the
	// post-wait recheck drops only the daily-boundary clauses.
	paceWait := a.waitSourcingDelay
	if withinChain {
		paceWait = a.waitChainDelay
	}
	if err := paceWait(ctx, a.manager.config.InteractionPaceWait); err != nil {
		if preservesM5PlannedAction(err) {
			return err
		}
		if closeErr := a.manager.store.MarkM5AutomaticActionManualRequired(
			action.ActionID, "automaticDispatchNotAllowed", a.manager.now(),
		); closeErr != nil {
			return errors.Join(err, closeErr)
		}
		return err
	}
	var handle interface {
		Wait(context.Context) error
	}
	switch action.Kind {
	case store.CommunicationActionReplyText:
		handle, err = a.manager.runner.(AutomaticReplyRunner).StartAutomaticReply(
			ctx,
			AutomaticReplyRequest{
				ActionID: action.ActionID, IntentID: intentID, PreviousIntentID: previousIntentID,
				ExpectedSession: a.hand.Session, ExpectedBootID: a.hand.BootID,
				Platform: profile.Platform, AccountRef: profile.AccountRef,
				ConversationRef: turn.ConversationRef, Text: action.Text,
			},
		)
	case store.CommunicationActionInviteWechat:
		handle, err = a.manager.runner.(AutomaticCardRunner).StartAutomaticCard(
			ctx,
			AutomaticCardRequest{
				ActionID: action.ActionID, IntentID: intentID, PreviousIntentID: previousIntentID,
				ExpectedSession: a.hand.Session, ExpectedBootID: a.hand.BootID,
				Platform: profile.Platform, AccountRef: profile.AccountRef,
				ConversationRef: turn.ConversationRef, Kind: action.Kind,
			},
		)
	case store.CommunicationActionInterviewInvite:
		if action.InterviewStartsAtMs == nil ||
			action.InterviewEndsAtMs == nil ||
			action.InterviewMethod == nil ||
			*action.InterviewEndsAtMs !=
				*action.InterviewStartsAtMs+communication.V4InterviewDurationMs ||
			*action.InterviewMethod != string(protocol.InterviewMethodWechatVideo) {
			return a.manager.store.MarkM5AutomaticActionManualRequired(
				action.ActionID,
				"automaticActionInvalid",
				a.manager.now(),
			)
		}
		interview := &protocol.InterviewDetails{
			StartsAt: *action.InterviewStartsAtMs,
			EndsAt:   *action.InterviewEndsAtMs,
			Method:   protocol.InterviewMethod(*action.InterviewMethod),
		}
		handle, err = a.manager.runner.(AutomaticCardRunner).StartAutomaticCard(
			ctx,
			AutomaticCardRequest{
				ActionID: action.ActionID, IntentID: intentID, PreviousIntentID: previousIntentID,
				ExpectedSession: a.hand.Session, ExpectedBootID: a.hand.BootID,
				Platform: profile.Platform, AccountRef: profile.AccountRef,
				ConversationRef: turn.ConversationRef, Kind: action.Kind, Interview: interview,
			},
		)
	}
	if err != nil || handle == nil {
		if closeErr := a.manager.store.MarkM5AutomaticActionManualRequired(
			action.ActionID, "automaticDispatchNotConstructed", a.manager.now(),
		); closeErr != nil {
			return errors.Join(err, closeErr)
		}
		return nil
	}
	func() {
		a.manager.mu.Unlock()
		defer a.manager.mu.Lock()
		err = handle.Wait(ctx)
	}()
	if err != nil {
		// A constructed effect is exclusively owned by the persistent recovery
		// rail. Never turn a wait interruption into a second business intent.
		return err
	}
	settled, err := a.manager.store.CommunicationActionByID(action.ActionID)
	if err != nil {
		return err
	}
	if settled == nil || (settled.Status != store.CommunicationActionSent &&
		settled.Status != store.CommunicationActionManualRequired) {
		return store.ErrCommunicationActionConflict
	}
	if settled.Status == store.CommunicationActionSent {
		next, nextErr := a.manager.store.PlannedCommunicationActionByTurn(turn.TurnID)
		if nextErr != nil {
			return nextErr
		}
		if next != nil {
			// The positive parent result has atomically materialized the only
			// dependent child. Keep the current conversation surface and reuse
			// the exact same dispatch path instead of yielding to another
			// page-driven patrol, which may switch the IM route first.
			//
			// This is still a new candidate-visible action with its own
			// action, WAL/idemKey, witness and positive-evidence boundary.
			// Per《24点边界裁决-2026-07-28》the chain interior does not
			// re-enter the workflow member gate: its daily-window clause is
			// exactly what a started chain is allowed to cross, and a user
			// pause closes the account projection which the chain recheck
			// still honors. Cancel and hand handover cut the chain too.
			if err := a.ensureChainDispatchAllowed(ctx); err != nil {
				return err
			}
			return a.dispatchM5Action(ctx, turn, true)
		}
	}
	return nil
}

// waitChainDelay keeps the brain-owned interaction pacing for a chain-interior
// action but rechecks with ensureChainDispatchAllowed afterwards, so a chain
// finishing across midnight is not cut by the daily-boundary clauses while a
// pause or handover arriving during the wait still cuts it.
func (a *roundActor) waitChainDelay(ctx context.Context, wait func(context.Context) error) error {
	var waitErr error
	func() {
		a.manager.mu.Unlock()
		defer a.manager.mu.Lock()
		waitErr = wait(ctx)
	}()
	if waitErr != nil {
		return waitErr
	}
	return a.ensureChainDispatchAllowed(ctx)
}

// A pause, daily boundary or process shutdown before WAL construction removes
// only the current dispatch authorization. The durable planned action remains
// the resume point; converting it to manualRequired would make an ordinary
// pause irreversibly consume otherwise valid work.
func preservesM5PlannedAction(err error) bool {
	return errors.Is(err, ErrActorPaused) ||
		errors.Is(err, ErrDailyWindowExpired) ||
		errors.Is(err, ErrRoundSupersededBySourcingBatch) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

func intentAdviceFromTurn(turn store.DialogueTurn) communication.IntentAdvice {
	switch turn.IntentSource {
	case store.DialogueIntentLLM:
		return communication.IntentAdvice{
			State: communication.AdviceOK, Suggestion: m5ai.IntentSuggestion{Label: turn.IntentLabel},
		}
	case store.DialogueIntentLLMFailure:
		return communication.IntentAdvice{State: communication.AdviceFailed}
	case store.DialogueIntentCodeShortCircuit:
		return communication.IntentAdvice{State: communication.AdviceAbsent}
	case store.DialogueIntentBusinessEvent:
		return communication.IntentAdvice{State: communication.AdviceAbsent}
	default:
		return communication.IntentAdvice{State: communication.AdviceState("invalid")}
	}
}

func (a *roundActor) loadM5TurnMaterial(turn store.DialogueTurn) (m5TurnMaterial, error) {
	profile, err := a.manager.store.CandidateProfileByID(turn.ProfileID)
	if err != nil || profile == nil || profile.ConversationRef == nil || *profile.ConversationRef != turn.ConversationRef ||
		profile.ActiveResumeSnapshotID == nil || *profile.ActiveResumeSnapshotID != turn.ResumeSnapshotID {
		return m5TurnMaterial{}, store.ErrDialogueTurnBinding
	}
	revision, err := a.manager.store.JobAIContextRevisionByHash(turn.ContextRevisionHash)
	if err != nil || revision == nil ||
		profile.BackendJobID == nil ||
		strings.TrimSpace(*profile.BackendJobID) == "" ||
		revision.SourceJobRef != strings.TrimSpace(*profile.BackendJobID) {
		return m5TurnMaterial{}, store.ErrDialogueTurnBinding
	}
	snapshot, err := a.manager.store.CandidateResumeSnapshotByID(turn.ProfileID, turn.ResumeSnapshotID)
	if err != nil || snapshot == nil {
		return m5TurnMaterial{}, store.ErrDialogueTurnBinding
	}
	key := store.ConversationKey{Platform: profile.Platform, AccountRef: profile.AccountRef, ConversationRef: turn.ConversationRef}
	messages, err := a.manager.store.MessagesForConversation(key)
	if err != nil || len(messages) == 0 ||
		messages[len(messages)-1].Seq < turn.InboundThroughSeq {
		return m5TurnMaterial{}, store.ErrDialogueTurnBinding
	}
	// 轮区间之后允许平台中性 system 行滞留（0727当日计划3）；出现新的
	// 候选人消息或我方出站仍视为边界失效。
	for index := range messages {
		message := messages[index]
		if message.Seq <= turn.InboundThroughSeq {
			continue
		}
		if message.Direction == "system" ||
			(message.Direction == "in" && message.Kind == "system") {
			continue
		}
		return m5TurnMaterial{}, store.ErrDialogueTurnBinding
	}
	material := m5TurnMaterial{profile: *profile, revision: *revision, snapshot: *snapshot}
	var currentBoundary []store.Message
	for _, message := range messages {
		if message.Seq > turn.InboundThroughSeq {
			continue
		}
		if message.Seq >= turn.InboundFromSeq && message.Seq <= turn.InboundThroughSeq {
			currentBoundary = append(currentBoundary, message)
		}
		if message.Direction == "system" || (message.Direction == "in" && message.Kind == "system") {
			continue
		}
		text := ""
		if message.Direction == "in" && message.Kind == "card" && message.CardType == "resumeAttachment" {
			text = m5ResumeAttachmentHistoryText
		} else if message.Text != nil {
			text = strings.TrimSpace(*message.Text)
		}
		if text == "" {
			continue
		}
		direction := "inbound"
		if message.Direction == "out" {
			direction = "outbound"
		}
		kind := message.Kind
		if direction == "outbound" && message.OutboundIntentID != nil &&
			profile.SuccessfulGreetingIntentID != nil && *message.OutboundIntentID == *profile.SuccessfulGreetingIntentID {
			kind = "greeting"
			material.sentGreeting = *message.Text
		}
		advice := m5ai.AdviceMessage{Seq: message.Seq, Direction: direction, Kind: kind, Text: text}
		material.throughTurn = append(material.throughTurn, advice)
		if message.Seq <= turn.HistoryThroughSeq {
			material.history = append(material.history, advice)
		}
		if message.Seq >= turn.InboundFromSeq && message.Seq <= turn.InboundThroughSeq {
			material.current = append(material.current, advice)
		}
	}
	// 不以 HistoryThroughSeq==0 判来聊根：解耦后来聊根首轮在前置 system
	// 已投影时渲染边界大于零。凡找不到已发招呼，一律经聚合根裁决。
	if material.sentGreeting == "" {
		aggregate, aggregateErr := a.manager.store.CommunicationV4AggregateByProfile(
			turn.ProfileID,
		)
		if aggregateErr != nil || aggregate == nil ||
			!store.IsInboundConversationV4Root(aggregate.RootGreetingIntentID) {
			return m5TurnMaterial{}, store.ErrDialogueTurnBinding
		}
		material.sentGreeting = m5ai.InboundConversationNoGreetingText
	}
	currentMessages, validBoundary := store.DialogueTurnCandidateMessages(currentBoundary)
	if !validBoundary {
		return m5TurnMaterial{}, store.ErrDialogueTurnBinding
	}
	for index := range currentMessages {
		message := currentMessages[index]
		material.currentFacts = append(material.currentFacts, communication.LedgerMessageFact{
			Seq: message.Seq, Direction: message.Direction, Kind: message.Kind, Text: message.Text,
			CardType: message.CardType, CardState: message.CardState, Origin: message.Origin,
			TsApproxMs: message.TsApproxMs,
		})
	}
	inputKind, ok := store.DialogueTurnInputKindOf(currentMessages)
	if !ok {
		return m5TurnMaterial{}, store.ErrDialogueTurnBinding
	}
	material.inputKind = inputKind
	if material.sentGreeting == "" || len(material.current) == 0 || len(material.currentFacts) == 0 {
		return m5TurnMaterial{}, store.ErrDialogueTurnBinding
	}
	return material, nil
}

func (a *roundActor) runM5IntentAdvice(
	ctx context.Context,
	turn store.DialogueTurn,
	material m5TurnMaterial,
	facts communication.FrozenTurnFacts,
) error {
	content, _, err := m5ai.RenderIntentPrompt(
		material.revision.Communication.IntentPrompt, material.sentGreeting,
		material.history, material.current,
	)
	if err != nil {
		return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "intentRenderFailed", a.manager.now())
	}
	return a.executeM5Advice(ctx, turn, material, facts, m5ai.PurposeIntent, content, communication.IntentAdvice{})
}

func (a *roundActor) runM5ReplyAdvice(
	ctx context.Context,
	turn store.DialogueTurn,
	material m5TurnMaterial,
	facts communication.FrozenTurnFacts,
	intent communication.IntentAdvice,
	v4Purpose communication.V4AdvicePurpose,
) error {
	resumeJSON, err := m5ai.RenderResumeJSON(material.snapshot.ResumeJSON)
	if err != nil {
		return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "resumeRenderFailed", a.manager.now())
	}
	history, err := m5ai.RenderHistory(material.throughTurn)
	if err != nil {
		return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "historyRenderFailed", a.manager.now())
	}
	content, err := m5ai.RenderReplyPromptFrozen(
		material.revision.Communication.ReplyPrompt, resumeJSON, history,
		turn.RecommendedTimeText, material.revision.Communication.CustomerFacts,
	)
	if err != nil {
		return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "replyRenderFailed", a.manager.now())
	}
	switch v4Purpose {
	case communication.V4AdviceReply:
	case communication.V4AdviceServiceReply:
		content, err = m5ai.AppendServiceReplyPolicy(content)
	default:
		err = communication.ErrInvalidV4StateTransition
	}
	if err != nil {
		return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "replyRenderFailed", a.manager.now())
	}
	return a.executeM5Advice(ctx, turn, material, facts, m5ai.PurposeReply, content, intent)
}

func (a *roundActor) executeM5Advice(
	ctx context.Context,
	turn store.DialogueTurn,
	material m5TurnMaterial,
	facts communication.FrozenTurnFacts,
	purpose m5ai.CompletionPurpose,
	content string,
	intent communication.IntentAdvice,
) error {
	inputHash := sha256Hex(content)
	invocationID := stableM5ID("invocation", turn.TurnID, string(purpose), "1")
	reserved, err := a.manager.store.ReserveAIInvocation(store.ReserveAIInvocationRequest{
		InvocationID: invocationID, TurnID: turn.TurnID, Purpose: purpose, Attempt: m5InvocationAttempt,
		Provider: a.manager.advice.ProviderName(), Model: a.manager.advice.ModelName(),
		InputHash: inputHash, CreatedAt: a.manager.now(),
	})
	if err != nil {
		if errors.Is(err, store.ErrDialogueTurnBinding) {
			return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "inputBoundaryChanged", a.manager.now())
		}
		return err
	}
	if !reserved.Created {
		if reserved.Invocation.FinishedAt != nil {
			if purpose != m5ai.PurposeReply ||
				!isLegacyM5ReplyBudgetFalsePositive(reserved.Invocation) {
				return a.manager.store.MarkDialogueTurnManualRequired(
					turn.TurnID, "invocationStateConflict", a.manager.now(),
				)
			}
			recovery, recoveryErr := a.manager.store.ReserveAuthorizedM5ReplyBudgetRecovery(
				store.ReserveM5ReplyBudgetRecoveryRequest{
					InvocationID: stableM5ID(
						"invocation", turn.TurnID, string(m5ai.PurposeReply), "2",
					),
					TurnID: turn.TurnID, Provider: a.manager.advice.ProviderName(),
					Model: a.manager.advice.ModelName(), InputHash: inputHash,
					CreatedAt: a.manager.now(),
				},
			)
			if recoveryErr != nil {
				switch {
				case errors.Is(recoveryErr, store.ErrDialogueTurnBinding):
					return a.manager.store.MarkDialogueTurnManualRequired(
						turn.TurnID, "inputBoundaryChanged", a.manager.now(),
					)
				case errors.Is(recoveryErr, store.ErrM5ReplyBudgetRecoveryUnsafe):
					return a.manager.store.MarkDialogueTurnManualRequired(
						turn.TurnID, "replyBudgetRecoveryUnsafe", a.manager.now(),
					)
				default:
					return recoveryErr
				}
			}
			reserved = recovery
			if reserved.Invocation.FinishedAt != nil {
				return a.manager.store.MarkDialogueTurnManualRequired(
					turn.TurnID, "replyBudgetRecoveryAlreadyFinished", a.manager.now(),
				)
			}
		}
		if !reserved.Created {
			return a.finishInterruptedM5Advice(turn, material, facts, purpose, intent, reserved.Invocation)
		}
	}

	request := m5ai.CompletionRequest{
		InvocationID:        reserved.Invocation.InvocationID,
		Purpose:             purpose,
		ContextRevisionHash: turn.ContextRevisionHash,
		PromptRevision:      turn.RenderFormatVersion,
		UserContent:         content,
	}
	if purpose == m5ai.PurposeIntent {
		request.MaxOutputTokens = m5ai.IntentOutputTokenLimit
	} else {
		request.MaxOutputTokens = m5ai.ReplyOutputTokenLimit
	}
	started := time.Now()
	var response m5ai.CompletionResponse
	var callErr error
	func() {
		a.manager.mu.Unlock()
		defer a.manager.mu.Lock()
		response, callErr = a.manager.advice.CompleteJSON(ctx, request)
	}()
	completion := m5CompletionFromProvider(reserved.Invocation.InvocationID, response, callErr, time.Since(started), a.manager.now())

	if purpose == m5ai.PurposeIntent {
		advice := communication.IntentAdvice{State: communication.AdviceFailed}
		manualReason := ""
		if callErr == nil {
			suggestion, parseErr := m5ai.ParseIntentSuggestion(response.JSONText)
			if parseErr == nil {
				advice = communication.IntentAdvice{State: communication.AdviceOK, Suggestion: suggestion}
			} else {
				markBusinessParseFailure(&completion, parseErr)
			}
		}
		if callErr == nil && !reasoningUsageSafe(completion) {
			markReasoningUsageUnsafe(&completion)
			manualReason = "reasoningUsageUnsafe"
		} else if completion.Status == store.AIInvocationBudgetBlocked {
			manualReason = "inputBudgetBlocked"
		}
		decision, reduceErr := communication.Reduce(communication.ReduceInput{
			Turn: facts, Intent: advice, Reply: communication.ReplyAdvice{State: communication.AdviceAbsent},
		})
		if reduceErr != nil {
			markReducerRejected(&completion)
			manualReason = "reducerRejected"
		}
		logAIInvocationOutcome(
			a.manager.advice, purpose, completion, response.Diagnostics.TraceErrorCode,
		)
		err := a.completeM5Intent(turn.TurnID, completion, decision, manualReason)
		if err != nil {
			logAIInvocationPersistenceFailure(a.manager.advice, purpose, completion)
		}
		return err
	}

	reply := communication.ReplyAdvice{State: communication.AdviceFailed}
	if callErr == nil {
		suggestion, parseErr := m5ai.ParseReplySuggestion(response.JSONText)
		if parseErr == nil {
			reply = communication.ReplyAdvice{State: communication.AdviceOK, Suggestion: suggestion}
		} else {
			markBusinessParseFailure(&completion, parseErr)
		}
	}
	decision, reduceErr := a.reduceM5ReplyDecision(turn, material, facts, intent, reply)
	if reduceErr != nil {
		markReducerRejected(&completion)
		decision = communication.Decision{TurnID: turn.TurnID, TurnStatus: communication.TurnManualRequired, ManualReason: "reducerRejected"}
	} else if callErr == nil && !reasoningUsageSafe(completion) {
		markReasoningUsageUnsafe(&completion)
		decision = communication.Decision{
			TurnID: turn.TurnID, TurnStatus: communication.TurnManualRequired,
			ManualReason: "reasoningUsageUnsafe",
		}
	}
	logAIInvocationOutcome(
		a.manager.advice, purpose, completion, response.Diagnostics.TraceErrorCode,
	)
	err = a.completeM5Reply(
		turn.TurnID,
		completion,
		decision,
		reply.Suggestion,
	)
	if err != nil {
		logAIInvocationPersistenceFailure(a.manager.advice, purpose, completion)
	}
	return err
}

func isLegacyM5ReplyBudgetFalsePositive(invocation store.AIInvocation) bool {
	return invocation.Purpose == m5ai.PurposeReply &&
		invocation.Attempt == 1 &&
		invocation.Status == store.AIInvocationBudgetBlocked &&
		invocation.ErrorClass == "budgetBlocked" &&
		invocation.FinishedAt != nil &&
		invocation.OutputHash == "" &&
		invocation.InputTokens == 0 &&
		invocation.CachedInputTokens == 0 &&
		invocation.OutputTokens == 0 &&
		invocation.ReasoningTokens == nil &&
		invocation.UsageShape == "" &&
		invocation.LatencyMs == 0 &&
		invocation.EstimatedCostMicros == 0
}

func (a *roundActor) finishInterruptedM5Advice(
	turn store.DialogueTurn,
	material m5TurnMaterial,
	facts communication.FrozenTurnFacts,
	purpose m5ai.CompletionPurpose,
	intent communication.IntentAdvice,
	invocation store.AIInvocation,
) error {
	completion := store.AIInvocationCompletion{
		InvocationID: invocation.InvocationID, Status: store.AIInvocationTransportFailed,
		ErrorClass: "processInterrupted", FinishedAt: a.manager.now(),
	}
	if purpose == m5ai.PurposeIntent {
		decision, err := communication.Reduce(communication.ReduceInput{
			Turn: facts, Intent: communication.IntentAdvice{State: communication.AdviceFailed},
			Reply: communication.ReplyAdvice{State: communication.AdviceAbsent},
		})
		if err != nil {
			return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "reducerRejected", a.manager.now())
		}
		return a.completeM5Intent(turn.TurnID, completion, decision, "")
	}
	decision, err := a.reduceM5ReplyDecision(
		turn, material, facts, intent, communication.ReplyAdvice{State: communication.AdviceFailed},
	)
	if err != nil {
		return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "reducerRejected", a.manager.now())
	}
	return a.completeM5Reply(
		turn.TurnID,
		completion,
		decision,
		m5ai.ReplySuggestion{},
	)
}

func (a *roundActor) reduceM5ReplyDecision(
	turn store.DialogueTurn,
	material m5TurnMaterial,
	facts communication.FrozenTurnFacts,
	intent communication.IntentAdvice,
	reply communication.ReplyAdvice,
) (communication.Decision, error) {
	v4Owned, err := a.manager.store.CommunicationV4OwnsTurn(turn.TurnID)
	if err != nil {
		return communication.Decision{}, err
	}
	if v4Owned {
		decision := communication.Decision{TurnID: turn.TurnID}
		switch reply.State {
		case communication.AdviceFailed:
			decision.TurnStatus = communication.TurnManualRequired
			decision.ManualReason = communication.ManualReplyFailed
			return decision, nil
		case communication.AdviceOK:
			if err := m5ai.ValidateSendText(reply.Suggestion.Text); err != nil {
				decision.TurnStatus = communication.TurnManualRequired
				decision.ManualReason = communication.ManualReplyInvalid
				return decision, nil
			}
			decision.TurnStatus = communication.TurnAdviceReady
			decision.Action = &communication.CommunicationActionPlan{
				TurnID: turn.TurnID, Kind: communication.CommunicationActionReplyText,
				Text: reply.Suggestion.Text, Status: communication.CommunicationActionPlanned,
			}
			return decision, nil
		default:
			return communication.Decision{}, communication.ErrInvalidV4StateTransition
		}
	}
	if material.inputKind != store.DialogueTurnInputResumeAttachment {
		return communication.Reduce(communication.ReduceInput{Turn: facts, Intent: intent, Reply: reply})
	}
	if turn.IntentLabel != m5ai.IntentInterested || turn.IntentSource != store.DialogueIntentBusinessEvent ||
		intent.State != communication.AdviceAbsent {
		return communication.Decision{}, communication.ErrInvalidV4StateTransition
	}
	v4, err := reduceM5ResumeTurn(turn, material, reply)
	if err != nil || v4.Dialogue.IntentLabel != m5ai.IntentInterested ||
		v4.Dialogue.IntentSource != communication.IntentSourceBusinessEvent || len(v4.EventActions) != 0 {
		return communication.Decision{}, communication.ErrInvalidV4StateTransition
	}
	decision := communication.Decision{
		TurnID: turn.TurnID, IntentLabel: m5ai.IntentInterested,
		IntentSource: communication.IntentSourceBusinessEvent,
	}
	switch v4.Dialogue.Status {
	case communication.V4DialogueActionsPlanned:
		if v4.Dialogue.NextAdvice != communication.V4AdviceNone || len(v4.Dialogue.Actions) != 1 {
			return communication.Decision{}, communication.ErrInvalidV4StateTransition
		}
		action := v4.Dialogue.Actions[0]
		if action.Kind != communication.V4ActionReplyText || action.CardMessageSeq != material.currentFacts[0].Seq ||
			strings.TrimSpace(action.Text) == "" {
			return communication.Decision{}, communication.ErrInvalidV4StateTransition
		}
		decision.TurnStatus = communication.TurnAdviceReady
		decision.Action = &communication.CommunicationActionPlan{
			TurnID: turn.TurnID, Kind: communication.CommunicationActionReplyText,
			Text: action.Text, Status: communication.CommunicationActionPlanned,
		}
	case communication.V4DialogueManualRequired:
		if v4.Dialogue.NextAdvice != communication.V4AdviceNone || len(v4.Dialogue.Actions) != 0 ||
			(v4.Dialogue.ManualReason != communication.V4ManualReplyFailed &&
				v4.Dialogue.ManualReason != communication.V4ManualReplyInvalid) {
			return communication.Decision{}, communication.ErrInvalidV4StateTransition
		}
		decision.TurnStatus = communication.TurnManualRequired
		decision.ManualReason = communication.ManualReason(v4.Dialogue.ManualReason)
	default:
		return communication.Decision{}, communication.ErrInvalidV4StateTransition
	}
	return decision, nil
}

func (a *roundActor) completeM5Intent(
	turnID string,
	completion store.AIInvocationCompletion,
	decision communication.Decision,
	manualReason string,
) error {
	if manualReason == "" && decision.TurnStatus == communication.TurnManualRequired {
		manualReason = string(decision.ManualReason)
	}
	source := store.DialogueIntentSource(decision.IntentSource)
	label := decision.IntentLabel
	if manualReason != "" && manualReason != "intentRejected" {
		label = ""
		source = ""
	}
	_, err := a.manager.store.CompleteIntentInvocation(store.CompleteIntentInvocationRequest{
		Completion: completion, Label: label, Source: source, ManualReason: manualReason,
	})
	if errors.Is(err, store.ErrDialogueTurnBinding) {
		return a.manager.store.MarkDialogueTurnManualRequired(turnID, "inputBoundaryChanged", a.manager.now())
	}
	return err
}

func (a *roundActor) completeM5Reply(
	turnID string,
	completion store.AIInvocationCompletion,
	decision communication.Decision,
	suggestion m5ai.ReplySuggestion,
) error {
	request := store.CompleteReplyInvocationRequest{Completion: completion, PlannedAt: a.manager.now()}
	if decision.Action != nil {
		request.ActionID = stableM5ID("action", turnID, string(decision.Action.Kind))
		request.Phrases = append([]string(nil), suggestion.Phrases...)
		request.Text = decision.Action.Text
		request.Action = suggestion.Action
		request.MeetingTime = suggestion.MeetingTime
		request.ContentHash = syncledger.HashText(decision.Action.Text)
	} else {
		request.ManualReason = string(decision.ManualReason)
		if request.ManualReason == "" {
			request.ManualReason = "replyFailed"
		}
	}
	_, err := a.manager.store.CompleteReplyInvocation(request)
	if errors.Is(err, store.ErrDialogueTurnBinding) {
		return a.manager.store.MarkDialogueTurnManualRequired(turnID, "inputBoundaryChanged", a.manager.now())
	}
	return err
}

func m5CompletionFromProvider(
	invocationID string,
	response m5ai.CompletionResponse,
	callErr error,
	latency time.Duration,
	finishedAt time.Time,
) store.AIInvocationCompletion {
	completion := store.AIInvocationCompletion{
		InvocationID: invocationID, Status: store.AIInvocationOK,
		OutputHash: sha256Hex(response.JSONText), InputTokens: response.Usage.InputTokens,
		CachedInputTokens: response.Usage.CachedInputTokens, OutputTokens: response.Usage.OutputTokens,
		ReasoningTokens: response.Usage.ReasoningTokens, ReasoningContentEmpty: response.ReasoningContentEmpty,
		LatencyMs:           latency.Milliseconds(),
		ProviderHTTPStatus:  response.Diagnostics.ProviderHTTPStatus,
		RequestBytes:        response.Diagnostics.RequestBytes,
		ResponseBytes:       response.Diagnostics.ResponseBytes,
		TraceStatus:         response.Diagnostics.TraceStatus,
		EstimatedCostMicros: m5ai.EstimatedCostMicros(response.Usage), FinishedAt: finishedAt,
	}
	if response.Usage.ReasoningTokens == nil {
		completion.UsageShape = store.AIInvocationReasoningFieldAbsent
	} else {
		completion.UsageShape = store.AIInvocationUsageComplete
	}
	if callErr != nil {
		completion.Status, completion.ErrorClass = m5ProviderFailure(callErr)
		completion.FailureStage, completion.ErrorDetailCode = m5ProviderFailureDiagnostics(callErr)
		var providerErr *m5ai.ProviderError
		if errors.As(callErr, &providerErr) && providerErr.Class == "inputTokenBudgetExceeded" {
			return completion
		}
		completion.OutputHash = ""
		completion.UsageShape = ""
		completion.ReasoningTokens = nil
		completion.InputTokens = 0
		completion.CachedInputTokens = 0
		completion.OutputTokens = 0
		completion.EstimatedCostMicros = 0
		completion.ReasoningContentEmpty = false
	} else if completion.TraceStatus != "" && completion.TraceStatus != m5ai.TraceStatusComplete {
		completion.FailureStage = m5ai.FailureStagePersistence
		completion.ErrorDetailCode = safeTraceErrorCode(response.Diagnostics.TraceErrorCode)
	}
	return completion
}

func m5ProviderFailure(err error) (store.AIInvocationStatus, string) {
	var providerErr *m5ai.ProviderError
	if !errors.As(err, &providerErr) {
		return store.AIInvocationTransportFailed, "transport"
	}
	switch providerErr.Class {
	case "budgetBlocked", "inputTokenBudgetExceeded":
		return store.AIInvocationBudgetBlocked, providerErr.Class
	case "authentication", "rateLimited", "providerRejected":
		return store.AIInvocationProviderRejected, providerErr.Class
	case "responseInvalid":
		return store.AIInvocationInvalidOutput, "responseInvalid"
	case "timeout", "transport", "providerUnavailable", "requestInvalid", "requestPayloadTooLarge":
		return store.AIInvocationTransportFailed, providerErr.Class
	default:
		return store.AIInvocationTransportFailed, "providerFailure"
	}
}

func reasoningUsageSafe(completion store.AIInvocationCompletion) bool {
	if !completion.ReasoningContentEmpty {
		return false
	}
	if completion.UsageShape == store.AIInvocationUsageComplete {
		return completion.ReasoningTokens != nil && *completion.ReasoningTokens == 0
	}
	return completion.UsageShape == store.AIInvocationReasoningFieldAbsent && completion.ReasoningTokens == nil
}

func sha256Hex(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func stableM5ID(kind string, parts ...string) string {
	return kind + "-" + sha256Hex(strings.Join(parts, "\x00"))
}
