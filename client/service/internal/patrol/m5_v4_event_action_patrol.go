package patrol

import (
	"context"
	"errors"
	"strings"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/store"
)

type communicationV4EventDependencyState uint8

const (
	communicationV4EventDependencyReady communicationV4EventDependencyState = iota
	communicationV4EventDependencyWaiting
	communicationV4EventDependencyUnavailable
)

// drainCommunicationV4EventActions is the sole patrol bridge from immutable
// V4 event-action facts into the existing M5 effect runners. The runners still
// construct the WAL atomically through the Store binding added in the prior
// batch; this loop only chooses deterministic order and supplies the frozen
// arguments.
func (a *roundActor) drainCommunicationV4EventActions(ctx context.Context) error {
	unresolved, err :=
		a.manager.store.CommunicationV4EventActionsNeedingProfileManualForAccount(a.key())
	if err != nil {
		return err
	}
	isolatedProfiles := make(map[string]struct{})
	for index := range unresolved {
		action := unresolved[index]
		if _, isolated := isolatedProfiles[action.ProfileID]; isolated {
			continue
		}
		if err := a.manager.store.MarkCommunicationV4AutomationManualRequired(
			action.ProfileID,
			action.FailureReason,
			a.manager.now(),
		); err != nil {
			return err
		}
		isolatedProfiles[action.ProfileID] = struct{}{}
	}

	actions, err := a.manager.store.PlannedCommunicationV4EventActionsForAccount(a.key())
	if err != nil {
		return err
	}
	stoppedProfiles := make(map[string]struct{})
	for index := range actions {
		action := actions[index]
		if _, stopped := stoppedProfiles[action.ProfileID]; stopped {
			continue
		}
		stopProfile, err := a.dispatchCommunicationV4EventAction(ctx, action)
		if err != nil {
			return err
		}
		if stopProfile {
			stoppedProfiles[action.ProfileID] = struct{}{}
		}
	}
	return nil
}

// dispatchCommunicationV4EventAction returns stopProfile when this profile was
// deliberately transferred to manual handling. A dependency that is still
// owned by the WAL recovery rail merely waits for a later patrol.
func (a *roundActor) dispatchCommunicationV4EventAction(
	ctx context.Context,
	action store.CommunicationV4EventAction,
) (bool, error) {
	var replyRunner AutomaticReplyRunner
	var cardRunner AutomaticCardRunner
	switch {
	case action.EffectKind == store.CommunicationV4EventEffectReplyText &&
		(action.V4Kind == communication.V4ActionWechatReceipt ||
			action.V4Kind == communication.V4ActionInterviewAcceptedReceipt) &&
		action.DependsOnActionID == nil &&
		strings.TrimSpace(action.Text) != "" &&
		strings.TrimSpace(action.ContentHash) != "":
		var ok bool
		replyRunner, ok = a.manager.runner.(AutomaticReplyRunner)
		if !ok {
			return a.markCommunicationV4EventActionManual(
				action,
				store.CommunicationV4EventActionFailureRunnerUnavailable,
			)
		}
	case action.EffectKind == store.CommunicationV4EventEffectInviteWechat &&
		action.V4Kind == communication.V4ActionInviteWechat &&
		action.DependsOnActionID != nil &&
		strings.TrimSpace(action.ContentHash) != "":
		var ok bool
		cardRunner, ok = a.manager.runner.(AutomaticCardRunner)
		if !ok {
			return a.markCommunicationV4EventActionManual(
				action,
				store.CommunicationV4EventActionFailureRunnerUnavailable,
			)
		}
	default:
		return a.markCommunicationV4EventActionManual(
			action,
			store.CommunicationV4EventActionFailureActionInvalid,
		)
	}

	profile, err := a.manager.store.CandidateProfileByID(action.ProfileID)
	if err != nil {
		return false, err
	}
	if profile == nil ||
		profile.Platform != a.account.Platform ||
		profile.AccountRef != a.account.AccountRef ||
		profile.ConversationRef == nil ||
		strings.TrimSpace(*profile.ConversationRef) == "" {
		return a.markCommunicationV4EventActionManual(
			action,
			store.CommunicationV4EventActionFailureBindingUnavailable,
		)
	}

	previousIntentID := ""
	if action.DependsOnActionID == nil {
		latest, latestErr := a.manager.store.LatestEffectIntent(
			profile.Platform,
			profile.AccountRef,
			*profile.ConversationRef,
		)
		if latestErr != nil {
			return false, latestErr
		}
		if latest != nil {
			previousIntentID = latest.IntentID
		}
	} else {
		dependencyState, dependencyIntentID, dependencyErr :=
			a.communicationV4EventDependency(*action.DependsOnActionID)
		if dependencyErr != nil {
			return false, dependencyErr
		}
		switch dependencyState {
		case communicationV4EventDependencyWaiting:
			return false, nil
		case communicationV4EventDependencyUnavailable:
			return a.markCommunicationV4EventActionManual(
				action,
				store.CommunicationV4EventActionFailureDependencyUnavailable,
			)
		case communicationV4EventDependencyReady:
			previousIntentID = dependencyIntentID
		default:
			return false, store.ErrCommunicationV4EventActionConflict
		}
	}

	intentID, err := store.M5AutomaticIntentID(action.ActionID)
	if err != nil {
		return a.markCommunicationV4EventActionManual(
			action,
			store.CommunicationV4EventActionFailureActionInvalid,
		)
	}
	if err := a.waitSourcingDelay(ctx, a.manager.config.InteractionPaceWait); err != nil {
		// A process stop, account pause or hand-generation change during the
		// pacing wait is a recoverable pre-WAL interruption. Leave the durable
		// action planned for the next authorized patrol.
		return false, err
	}

	var handle interface {
		Wait(context.Context) error
	}
	switch action.EffectKind {
	case store.CommunicationV4EventEffectReplyText:
		handle, err = replyRunner.StartAutomaticReply(
			ctx,
			AutomaticReplyRequest{
				ActionID: action.ActionID, IntentID: intentID,
				PreviousIntentID: previousIntentID,
				ExpectedSession:  a.hand.Session, ExpectedBootID: a.hand.BootID,
				Platform: profile.Platform, AccountRef: profile.AccountRef,
				ConversationRef: *profile.ConversationRef, Text: action.Text,
			},
		)
	case store.CommunicationV4EventEffectInviteWechat:
		handle, err = cardRunner.StartAutomaticCard(
			ctx,
			AutomaticCardRequest{
				ActionID: action.ActionID, IntentID: intentID,
				PreviousIntentID: previousIntentID,
				ExpectedSession:  a.hand.Session, ExpectedBootID: a.hand.BootID,
				Platform: profile.Platform, AccountRef: profile.AccountRef,
				ConversationRef: *profile.ConversationRef,
				Kind:            store.CommunicationActionInviteWechat,
			},
		)
	}
	if err != nil || handle == nil {
		stopProfile, closeErr := a.markCommunicationV4EventActionManual(
			action,
			store.CommunicationV4EventActionFailureDispatchNotConstructed,
		)
		if closeErr != nil {
			return stopProfile, errors.Join(err, closeErr)
		}
		return stopProfile, nil
	}

	func() {
		a.manager.mu.Unlock()
		defer a.manager.mu.Lock()
		err = handle.Wait(ctx)
	}()
	if err != nil {
		// Once Start returned a handle, the persistent effect rail exclusively
		// owns recovery and terminal convergence.
		return false, err
	}
	settled, err := a.manager.store.CommunicationV4EventActionByID(action.ActionID)
	if err != nil {
		return false, err
	}
	if settled == nil {
		return false, store.ErrCommunicationV4EventActionConflict
	}
	switch settled.Status {
	case store.CommunicationV4EventActionSent:
		return false, nil
	case store.CommunicationV4EventActionManualRequired:
		return true, nil
	default:
		return false, store.ErrCommunicationV4EventActionConflict
	}
}

func (a *roundActor) communicationV4EventDependency(
	actionID string,
) (communicationV4EventDependencyState, string, error) {
	eventAction, err := a.manager.store.CommunicationV4EventActionByID(actionID)
	if err != nil {
		return communicationV4EventDependencyUnavailable, "", err
	}
	legacyAction, err := a.manager.store.CommunicationActionByID(actionID)
	if err != nil {
		return communicationV4EventDependencyUnavailable, "", err
	}
	if (eventAction == nil) == (legacyAction == nil) {
		if eventAction == nil {
			return communicationV4EventDependencyUnavailable, "", nil
		}
		return communicationV4EventDependencyUnavailable, "",
			store.ErrCommunicationV4EventActionConflict
	}
	if eventAction != nil {
		switch eventAction.Status {
		case store.CommunicationV4EventActionSent:
			if eventAction.EffectIntentID == nil ||
				strings.TrimSpace(*eventAction.EffectIntentID) == "" {
				return communicationV4EventDependencyUnavailable, "", nil
			}
			return communicationV4EventDependencyReady, *eventAction.EffectIntentID, nil
		case store.CommunicationV4EventActionPlanned,
			store.CommunicationV4EventActionEffectPending:
			return communicationV4EventDependencyWaiting, "", nil
		case store.CommunicationV4EventActionManualRequired:
			if eventAction.EffectIntentID != nil {
				return communicationV4EventDependencyWaiting, "", nil
			}
			return communicationV4EventDependencyUnavailable, "", nil
		case store.CommunicationV4EventActionDeferred:
			return communicationV4EventDependencyUnavailable, "", nil
		default:
			return communicationV4EventDependencyUnavailable, "",
				store.ErrCommunicationV4EventActionConflict
		}
	}

	switch legacyAction.Status {
	case store.CommunicationActionSent:
		if legacyAction.EffectIntentID == nil ||
			strings.TrimSpace(*legacyAction.EffectIntentID) == "" {
			return communicationV4EventDependencyUnavailable, "", nil
		}
		return communicationV4EventDependencyReady, *legacyAction.EffectIntentID, nil
	case store.CommunicationActionPlanned,
		store.CommunicationActionEffectPending:
		return communicationV4EventDependencyWaiting, "", nil
	case store.CommunicationActionManualRequired:
		if legacyAction.EffectIntentID != nil {
			return communicationV4EventDependencyWaiting, "", nil
		}
		return communicationV4EventDependencyUnavailable, "", nil
	case store.CommunicationActionSuperseded:
		return communicationV4EventDependencyUnavailable, "", nil
	default:
		return communicationV4EventDependencyUnavailable, "",
			store.ErrCommunicationV4EventActionConflict
	}
}

func (a *roundActor) markCommunicationV4EventActionManual(
	action store.CommunicationV4EventAction,
	reason string,
) (bool, error) {
	err := a.manager.store.MarkCommunicationV4EventActionManualRequired(
		action.ActionID,
		reason,
		a.manager.now(),
	)
	return true, err
}
