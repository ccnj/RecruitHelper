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

	activeContext, err := a.manager.store.ActiveProfileAIContext(target.Profile.ProfileID)
	if err != nil {
		return err
	}
	if activeContext == nil {
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
			ContextRevisionHash: activeContext.Revision.RevisionHash,
			ResumeSnapshotID:    snapshot.SnapshotID, RecommendedTimeText: recommended,
			RenderFormatVersion: m5ai.DialogueRenderFormatVersion, FrozenAt: a.now,
		})
		if freezeErr != nil {
			if errors.Is(freezeErr, store.ErrDialogueTurnBinding) {
				return a.manager.store.MarkActiveM5TrialManualRequired(
					target.Profile.ProfileID, "turnBoundaryChanged", a.manager.now(),
				)
			}
			if errors.Is(freezeErr, store.ErrDialogueTurnBudget) {
				return a.manager.store.MarkActiveM5TrialManualRequired(
					target.Profile.ProfileID, "monthlyTurnBudgetBlocked", a.manager.now(),
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
			if material.inputKind == store.DialogueTurnInputResumeAttachment {
				if turn.IntentLabel != m5ai.IntentInterested || turn.IntentSource != store.DialogueIntentBusinessEvent {
					return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "reducerStateConflict", a.manager.now())
				}
				decision, reduceErr := reduceM5ResumeTurn(turn, material, communication.ReplyAdvice{State: communication.AdviceAbsent})
				if reduceErr != nil || !m5ResumeReplyAdviceAuthorized(decision) {
					return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "reducerStateConflict", a.manager.now())
				}
				if err := a.runM5ReplyAdvice(
					ctx, turn, material, facts, communication.IntentAdvice{State: communication.AdviceAbsent},
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
			if err := a.runM5ReplyAdvice(ctx, turn, material, facts, intent); err != nil {
				return err
			}
		case store.DialogueTurnAdviceReady:
			if _, ok := a.manager.runner.(AutomaticReplyRunner); !ok {
				// Batch 5 has not wired a real provider yet. Pure reducer tests may
				// intentionally stop at the persisted action seam.
				return nil
			}
			return a.dispatchM5Reply(ctx, turn)
		case store.DialogueTurnManualRequired, store.DialogueTurnSuperseded,
			store.DialogueTurnDispatching, store.DialogueTurnCompleted:
			return nil
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

func (a *roundActor) dispatchM5Reply(ctx context.Context, turn store.DialogueTurn) error {
	action, err := a.manager.store.CommunicationActionByTurn(turn.TurnID)
	if err != nil {
		return err
	}
	if action == nil || action.Kind != store.CommunicationActionReplyText ||
		action.Status != store.CommunicationActionPlanned || action.EffectIntentID != nil {
		return a.manager.store.MarkDialogueTurnManualRequired(
			turn.TurnID, "automaticActionUnavailable", a.manager.now(),
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
	latest, err := a.manager.store.LatestEffectIntent(
		profile.Platform, profile.AccountRef, turn.ConversationRef,
	)
	if err != nil {
		return err
	}
	previousIntentID := ""
	if latest != nil {
		previousIntentID = latest.IntentID
	}
	if err := a.ensureDispatchAllowed(ctx); err != nil {
		if closeErr := a.manager.store.MarkM5AutomaticActionManualRequired(
			action.ActionID, "automaticDispatchNotAllowed", a.manager.now(),
		); closeErr != nil {
			return errors.Join(err, closeErr)
		}
		return nil
	}
	runner := a.manager.runner.(AutomaticReplyRunner)
	handle, err := runner.StartAutomaticReply(ctx, AutomaticReplyRequest{
		ActionID: action.ActionID, IntentID: intentID, PreviousIntentID: previousIntentID,
		ExpectedSession: a.hand.Session, ExpectedBootID: a.hand.BootID,
		Platform: profile.Platform, AccountRef: profile.AccountRef,
		ConversationRef: turn.ConversationRef, Text: action.Text,
	})
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
	settled, err := a.manager.store.CommunicationActionByTurn(turn.TurnID)
	if err != nil {
		return err
	}
	if settled == nil || (settled.Status != store.CommunicationActionSent &&
		settled.Status != store.CommunicationActionManualRequired) {
		return store.ErrCommunicationActionConflict
	}
	return nil
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
	active, err := a.manager.store.ActiveProfileAIContext(turn.ProfileID)
	if err != nil || active == nil || active.Binding.RevisionHash != turn.ContextRevisionHash {
		return m5TurnMaterial{}, store.ErrDialogueTurnBinding
	}
	revision, err := a.manager.store.JobAIContextRevisionByHash(turn.ContextRevisionHash)
	if err != nil || revision == nil {
		return m5TurnMaterial{}, store.ErrDialogueTurnBinding
	}
	snapshot, err := a.manager.store.CandidateResumeSnapshotByID(turn.ProfileID, turn.ResumeSnapshotID)
	if err != nil || snapshot == nil {
		return m5TurnMaterial{}, store.ErrDialogueTurnBinding
	}
	key := store.ConversationKey{Platform: profile.Platform, AccountRef: profile.AccountRef, ConversationRef: turn.ConversationRef}
	messages, err := a.manager.store.MessagesForConversation(key)
	if err != nil || len(messages) == 0 || messages[len(messages)-1].Seq != turn.InboundThroughSeq {
		return m5TurnMaterial{}, store.ErrDialogueTurnBinding
	}
	material := m5TurnMaterial{profile: *profile, revision: *revision, snapshot: *snapshot}
	var currentMessages []store.Message
	for _, message := range messages {
		if message.Seq > turn.InboundThroughSeq {
			continue
		}
		if message.Seq >= turn.InboundFromSeq && message.Seq <= turn.InboundThroughSeq {
			currentMessages = append(currentMessages, message)
			material.currentFacts = append(material.currentFacts, communication.LedgerMessageFact{
				Seq: message.Seq, Direction: message.Direction, Kind: message.Kind, Text: message.Text,
				CardType: message.CardType, CardState: message.CardState, Origin: message.Origin,
				TsApproxMs: message.TsApproxMs,
			})
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
		if errors.Is(err, store.ErrAIInvocationBudget) {
			return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "dailyProviderBudgetBlocked", a.manager.now())
		}
		return err
	}
	if !reserved.Created {
		if reserved.Invocation.FinishedAt != nil {
			return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "invocationStateConflict", a.manager.now())
		}
		return a.finishInterruptedM5Advice(turn, material, facts, purpose, intent, reserved.Invocation)
	}

	request := m5ai.CompletionRequest{Purpose: purpose, UserContent: content}
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
				completion.Status = store.AIInvocationInvalidOutput
				completion.ErrorClass = "invalidOutput"
			}
		}
		if callErr == nil && !reasoningUsageSafe(completion) {
			manualReason = "reasoningUsageUnsafe"
		} else if completion.Status == store.AIInvocationBudgetBlocked {
			manualReason = "inputBudgetBlocked"
		}
		decision, reduceErr := communication.Reduce(communication.ReduceInput{
			Turn: facts, Intent: advice, Reply: communication.ReplyAdvice{State: communication.AdviceAbsent},
		})
		if reduceErr != nil {
			manualReason = "reducerRejected"
		}
		return a.completeM5Intent(turn.TurnID, completion, decision, manualReason)
	}

	reply := communication.ReplyAdvice{State: communication.AdviceFailed}
	if callErr == nil {
		suggestion, parseErr := m5ai.ParseReplySuggestion(response.JSONText)
		if parseErr == nil {
			reply = communication.ReplyAdvice{State: communication.AdviceOK, Suggestion: suggestion}
		} else {
			completion.Status = store.AIInvocationInvalidOutput
			completion.ErrorClass = "invalidOutput"
		}
	}
	decision, reduceErr := reduceM5ReplyDecision(turn, material, facts, intent, reply)
	if reduceErr != nil {
		decision = communication.Decision{TurnID: turn.TurnID, TurnStatus: communication.TurnManualRequired, ManualReason: "reducerRejected"}
	} else if callErr == nil && !reasoningUsageSafe(completion) {
		decision = communication.Decision{
			TurnID: turn.TurnID, TurnStatus: communication.TurnManualRequired,
			ManualReason: "reasoningUsageUnsafe",
		}
	}
	return a.completeM5Reply(turn.TurnID, completion, decision)
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
	decision, err := reduceM5ReplyDecision(
		turn, material, facts, intent, communication.ReplyAdvice{State: communication.AdviceFailed},
	)
	if err != nil {
		return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "reducerRejected", a.manager.now())
	}
	return a.completeM5Reply(turn.TurnID, completion, decision)
}

func reduceM5ReplyDecision(
	turn store.DialogueTurn,
	material m5TurnMaterial,
	facts communication.FrozenTurnFacts,
	intent communication.IntentAdvice,
	reply communication.ReplyAdvice,
) (communication.Decision, error) {
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
) error {
	request := store.CompleteReplyInvocationRequest{Completion: completion, PlannedAt: a.manager.now()}
	if decision.Action != nil {
		request.ActionID = stableM5ID("action", turnID, string(decision.Action.Kind))
		request.Text = decision.Action.Text
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
		EstimatedCostMicros: m5ai.EstimatedCostMicros(response.Usage), FinishedAt: finishedAt,
	}
	if response.Usage.ReasoningTokens == nil {
		completion.UsageShape = store.AIInvocationReasoningFieldAbsent
	} else {
		completion.UsageShape = store.AIInvocationUsageComplete
	}
	if callErr != nil {
		completion.Status, completion.ErrorClass = m5ProviderFailure(callErr)
		completion.OutputHash = ""
		completion.UsageShape = ""
		completion.ReasoningTokens = nil
		completion.InputTokens = 0
		completion.CachedInputTokens = 0
		completion.OutputTokens = 0
		completion.EstimatedCostMicros = 0
		completion.ReasoningContentEmpty = false
	}
	return completion
}

func m5ProviderFailure(err error) (store.AIInvocationStatus, string) {
	var providerErr *m5ai.ProviderError
	if !errors.As(err, &providerErr) {
		return store.AIInvocationTransportFailed, "transport"
	}
	switch providerErr.Class {
	case "budgetBlocked":
		return store.AIInvocationBudgetBlocked, "budgetBlocked"
	case "authentication", "rateLimited", "providerRejected":
		return store.AIInvocationProviderRejected, providerErr.Class
	case "responseInvalid":
		return store.AIInvocationInvalidOutput, "responseInvalid"
	case "timeout", "transport", "providerUnavailable", "requestInvalid":
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
