package patrol

import (
	"context"
	"errors"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/store"
)

const (
	communicationV4CardTransitionReadLimit   = 500
	communicationV4ManualCardProfileMismatch = "cardTransitionProfileMismatch"
)

// processCommunicationV4CardTransitions drains the M2 append-only card
// transition outbox before ordinary profile enumeration. Applying the
// normalized event and acknowledging the outbox remain intentionally separate:
// a crash between them replays the immutable ProjectionApplication, then acks.
func (a *roundActor) processCommunicationV4CardTransitions(ctx context.Context) error {
	return a.processCommunicationV4CardTransitionsForProfile(ctx, "")
}

func (a *roundActor) processCommunicationV4CardTransitionsForProfile(
	ctx context.Context,
	profileID string,
) error {
	pending, err := a.manager.store.PendingCardTransitionsForAccount(
		a.key(),
		communicationV4CardTransitionReadLimit,
	)
	if err != nil {
		return err
	}
	for index := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		item := pending[index]
		key := store.ConversationKey{
			Platform:        item.Transition.Platform,
			AccountRef:      item.Transition.AccountRef,
			ConversationRef: item.Transition.ConversationRef,
		}
		profile, err := a.manager.store.CandidateProfileByConversation(key)
		if err != nil {
			return err
		}
		if profile == nil {
			// A transition may predate M4 profile adoption. It remains pending
			// until a real profile/root exists; absence is not authority to
			// consume or reinterpret the fact.
			continue
		}
		if profileID != "" && profile.ProfileID != profileID {
			continue
		}
		aggregate, err := a.manager.store.CommunicationV4AggregateByProfile(profile.ProfileID)
		if err != nil {
			if errors.Is(err, store.ErrCommunicationV4Missing) {
				continue
			}
			return err
		}
		if err := a.processCommunicationV4CardTransition(item, *profile, *aggregate); err != nil {
			return err
		}
	}
	return nil
}

func (a *roundActor) processCommunicationV4CardTransition(
	pending store.PendingCardTransition,
	profile store.CandidateProfile,
	aggregate store.CommunicationV4Aggregate,
) error {
	if !communicationV4CardTransitionMatchesProfile(pending, profile) {
		if err := a.manager.store.MarkCommunicationV4AutomationManualRequired(
			profile.ProfileID,
			communicationV4ManualCardProfileMismatch,
			a.manager.now(),
		); err != nil {
			return err
		}
		_, err := a.manager.store.AcknowledgeCardTransition(
			pending.Transition.Key(),
			a.manager.now(),
		)
		return err
	}

	if aggregate.ProjectedThroughSeq == pending.Transition.MessageSeq-1 {
		cardBeforeTransition := pending.Message
		cardBeforeTransition.CardState = pending.Transition.FromState
		event, err := communication.NormalizeLedgerMessage(
			communication.LedgerMessageFact{
				Seq: cardBeforeTransition.Seq, Direction: cardBeforeTransition.Direction,
				Kind: cardBeforeTransition.Kind, Text: cardBeforeTransition.Text,
				CardType: cardBeforeTransition.CardType, CardState: cardBeforeTransition.CardState,
				Origin: cardBeforeTransition.Origin, TsApproxMs: cardBeforeTransition.TsApproxMs,
			},
		)
		if err != nil {
			return err
		}
		result, err := a.manager.store.ApplyCommunicationV4BusinessEvent(
			store.ApplyCommunicationV4BusinessEventRequest{
				ProfileID: profile.ProfileID,
				Event:     event,
				AppliedAt: a.manager.now(),
			},
		)
		if err != nil {
			return err
		}
		aggregate = result.Aggregate
	}
	if aggregate.ProjectedThroughSeq < pending.Transition.MessageSeq {
		return store.ErrCommunicationV4Conflict
	}

	occurredAt := pending.Transition.CreatedAt
	event, err := communication.NormalizeCardTransition(
		communication.LedgerCardTransitionFact{
			MessageSeq: pending.Transition.MessageSeq,
			CardType:   pending.Transition.CardType,
			FromState:  pending.Transition.FromState,
			ToState:    pending.Transition.ToState,
			OccurredAt: &occurredAt,
		},
	)
	if err != nil {
		return err
	}
	if _, err := a.manager.store.ApplyCommunicationV4BusinessEvent(
		store.ApplyCommunicationV4BusinessEventRequest{
			ProfileID: profile.ProfileID,
			Event:     event,
			AppliedAt: a.manager.now(),
		},
	); err != nil {
		return err
	}
	_, err = a.manager.store.AcknowledgeCardTransition(
		pending.Transition.Key(),
		a.manager.now(),
	)
	return err
}

func communicationV4CardTransitionMatchesProfile(
	pending store.PendingCardTransition,
	profile store.CandidateProfile,
) bool {
	transition := pending.Transition
	message := pending.Message
	return profile.ProfileID != "" &&
		profile.ConversationRef != nil &&
		profile.Platform == transition.Platform &&
		profile.AccountRef == transition.AccountRef &&
		*profile.ConversationRef == transition.ConversationRef &&
		message.Platform == transition.Platform &&
		message.AccountRef == transition.AccountRef &&
		message.ConversationRef == transition.ConversationRef &&
		message.Seq == transition.MessageSeq
}
