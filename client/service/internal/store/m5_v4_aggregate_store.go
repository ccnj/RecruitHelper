package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"recruithelper/client/service/internal/communication"

	"gorm.io/gorm"
)

const (
	communicationV4StateSchemaVersion = 1
	communicationV4InputDigestPrefix  = "communication-v4-input-v1|"
	communicationV4ArchiveSuperseded  = "scheduleArchivedBeforeEffect"
)

var (
	ErrCommunicationV4Invalid  = errors.New("V4 沟通聚合输入无效")
	ErrCommunicationV4Missing  = errors.New("V4 沟通聚合不存在")
	ErrCommunicationV4Conflict = errors.New("V4 沟通聚合或投影冲突")
	ErrCommunicationV4Corrupt  = errors.New("V4 沟通聚合损坏")
)

func (s *Store) CommunicationV4AggregateByProfile(
	profileID string,
) (*CommunicationV4Aggregate, error) {
	if strings.TrimSpace(profileID) == "" {
		return nil, ErrCommunicationV4Invalid
	}
	var out CommunicationV4Aggregate
	err := s.db.Transaction(func(tx *gorm.DB) error {
		aggregate, err := communicationV4AggregateTx(tx, profileID)
		if err != nil {
			return err
		}
		out = aggregate
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// EnsureCommunicationV4RootForGreetedProfile activates a pre-M5-B greeted
// profile without guessing any later dialogue state. A bound conversation must
// still contain the unique successful greeting as its first active message;
// unbound profiles receive only the root and wait for normal late binding.
func (s *Store) EnsureCommunicationV4RootForGreetedProfile(
	profileID string,
	now time.Time,
) (*CommunicationV4Aggregate, bool, error) {
	if strings.TrimSpace(profileID) == "" {
		return nil, false, ErrCommunicationV4Invalid
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	var out CommunicationV4Aggregate
	created := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var profile CandidateProfile
		if err := tx.First(&profile, "profile_id = ?", profileID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCommunicationV4Missing
			}
			return err
		}
		if profile.MainStatus != CandidateProfileGreeted || profile.EndReason != nil ||
			profile.SuccessfulGreetingIntentID == nil || profile.GreetedAt == nil {
			return ErrCommunicationV4Conflict
		}
		var greetingIntent EffectIntent
		if err := tx.First(
			&greetingIntent, "intent_id = ?", *profile.SuccessfulGreetingIntentID,
		).Error; err != nil {
			return ErrCommunicationV4Conflict
		}
		if greetingIntent.Primitive != primitiveChatSendGreeting ||
			greetingIntent.TargetRef != profile.ProfileID ||
			greetingIntent.Platform != profile.Platform || greetingIntent.AccountRef != profile.AccountRef ||
			(greetingIntent.Status != EffectIntentOk && greetingIntent.Status != EffectIntentResolvedOk) {
			return ErrCommunicationV4Conflict
		}
		projectedThroughSeq := int64(0)
		if profile.ConversationRef != nil {
			var greeting Message
			if err := tx.First(
				&greeting,
				"outbound_intent_id = ? AND retracted_at IS NULL",
				*profile.SuccessfulGreetingIntentID,
			).Error; err != nil {
				return ErrCommunicationV4Conflict
			}
			if greeting.Platform != profile.Platform || greeting.AccountRef != profile.AccountRef ||
				greeting.ConversationRef != *profile.ConversationRef || greeting.Seq != 1 ||
				greeting.Direction != "out" || greeting.Kind != "text" {
				return ErrCommunicationV4Conflict
			}
			projectedThroughSeq = greeting.Seq
		}
		aggregate, wasCreated, err := applyCommunicationV4RootTx(
			tx, profile.ProfileID, *profile.SuccessfulGreetingIntentID, projectedThroughSeq, now,
		)
		if err != nil {
			return err
		}
		out = *aggregate
		created = wasCreated
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return &out, created, nil
}

// applyCommunicationV4RootTx creates the aggregate only after the successful
// greeting fact is durable in the same transaction. Existing roots are
// idempotent only for the exact greeting intent; an alternate root is a ledger
// conflict, never a replacement.
func applyCommunicationV4RootTx(
	tx *gorm.DB,
	profileID string,
	greetingIntentID string,
	projectedThroughSeq int64,
	now time.Time,
) (*CommunicationV4Aggregate, bool, error) {
	if tx == nil || strings.TrimSpace(profileID) == "" || strings.TrimSpace(greetingIntentID) == "" ||
		projectedThroughSeq < 0 || now.IsZero() {
		return nil, false, ErrCommunicationV4Invalid
	}
	var existing CommunicationV4Aggregate
	err := tx.First(&existing, "profile_id = ?", profileID).Error
	switch {
	case err == nil:
		if _, err := communicationV4AggregateTx(tx, profileID); err != nil {
			return nil, false, err
		}
		if existing.RootGreetingIntentID != greetingIntentID {
			return nil, false, ErrCommunicationV4Conflict
		}
		return &existing, false, nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, false, err
	}

	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", profileID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, ErrCommunicationV4Missing
		}
		return nil, false, err
	}
	if profile.MainStatus != CandidateProfileGreeted || profile.EndReason != nil ||
		profile.SuccessfulGreetingIntentID == nil || *profile.SuccessfulGreetingIntentID != greetingIntentID ||
		profile.GreetedAt == nil {
		return nil, false, ErrCommunicationV4Conflict
	}

	state := communication.NewV4GreetedState(profile.GreetedAt)
	if err := communication.ValidateV4State(state); err != nil {
		return nil, false, ErrCommunicationV4Corrupt
	}
	now = now.UTC()
	aggregate := CommunicationV4Aggregate{
		ProfileID: profileID, RootGreetingIntentID: greetingIntentID,
		StateSchemaVersion: communicationV4StateSchemaVersion,
		Revision:           0, ProjectedThroughSeq: projectedThroughSeq, State: state,
		AutomationStatus: ProfileCommunicationAutomationActive,
		CreatedAt:        now, UpdatedAt: now,
	}
	if err := tx.Create(&aggregate).Error; err != nil {
		return nil, false, err
	}
	return &aggregate, true, nil
}

// bindCommunicationV4RootConversationTx advances only the ledger projection
// boundary when a previously unbound successful greeting is materialized as
// message 1 during late conversation adoption. The greeting is already part
// of the root state, so this is not a new reducer input and does not increment
// Revision.
func bindCommunicationV4RootConversationTx(
	tx *gorm.DB,
	profileID string,
	greetingIntentID string,
	messageSeq int64,
	now time.Time,
) error {
	if tx == nil || strings.TrimSpace(profileID) == "" || strings.TrimSpace(greetingIntentID) == "" ||
		messageSeq != 1 || now.IsZero() {
		return ErrCommunicationV4Invalid
	}
	var aggregate CommunicationV4Aggregate
	if err := tx.First(&aggregate, "profile_id = ?", profileID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Pre-M5-B greeted rows are rooted by the account activation pass
			// immediately after late binding.
			return nil
		}
		return err
	}
	if _, err := communicationV4AggregateTx(tx, profileID); err != nil {
		return err
	}
	if aggregate.RootGreetingIntentID != greetingIntentID {
		return ErrCommunicationV4Conflict
	}
	if aggregate.ProjectedThroughSeq == messageSeq {
		return nil
	}
	if aggregate.Revision != 0 || aggregate.ProjectedThroughSeq != 0 ||
		aggregate.State.MainStatus != communication.V4StatusGreeted {
		return ErrCommunicationV4Conflict
	}
	updated := tx.Model(&CommunicationV4Aggregate{}).
		Where("profile_id = ? AND revision = 0 AND projected_through_seq = 0", profileID).
		Updates(map[string]any{"projected_through_seq": messageSeq, "updated_at": now.UTC()})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrCommunicationV4Conflict
	}
	return nil
}

type ApplyCommunicationV4BusinessEventRequest struct {
	ProfileID string
	Event     communication.BusinessEvent
	AppliedAt time.Time
}

type ApplyCommunicationV4BusinessEventResult struct {
	Aggregate   CommunicationV4Aggregate
	Application CommunicationV4ProjectionApplication
	Applied     bool
}

func (s *Store) UnrootedGreetedProfileIDsForAccount(key AccountKey) ([]string, error) {
	if strings.TrimSpace(key.Platform) == "" || strings.TrimSpace(key.AccountRef) == "" {
		return nil, ErrCommunicationV4Invalid
	}
	var profileIDs []string
	err := s.db.Table("candidate_profiles AS p").
		Select("p.profile_id").
		Joins("LEFT JOIN communication_v4_aggregates AS v4 ON v4.profile_id = p.profile_id").
		Where(
			"p.platform = ? AND p.account_ref = ? AND p.main_status = ? AND p.end_reason IS NULL "+
				"AND p.successful_greeting_intent_id IS NOT NULL AND p.greeted_at IS NOT NULL AND v4.profile_id IS NULL",
			key.Platform, key.AccountRef, CandidateProfileGreeted,
		).
		Order("p.profile_id").
		Scan(&profileIDs).Error
	if err != nil {
		return nil, err
	}
	return profileIDs, nil
}

// ApplyCommunicationV4BusinessEvent persists the reducer result and its
// orchestration outcome atomically. Replaying an identical normalized event
// returns the immutable receipt; reusing its key for different facts fails
// closed.
func (s *Store) ApplyCommunicationV4BusinessEvent(
	req ApplyCommunicationV4BusinessEventRequest,
) (*ApplyCommunicationV4BusinessEventResult, error) {
	if strings.TrimSpace(req.ProfileID) == "" || strings.TrimSpace(req.Event.Key) == "" {
		return nil, ErrCommunicationV4Invalid
	}
	if req.AppliedAt.IsZero() {
		req.AppliedAt = time.Now()
	}
	req.AppliedAt = req.AppliedAt.UTC()
	digest, err := communicationV4InputDigest(req.Event)
	if err != nil {
		return nil, err
	}

	out := &ApplyCommunicationV4BusinessEventResult{}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		aggregate, err := communicationV4AggregateTx(tx, req.ProfileID)
		if err != nil {
			return err
		}
		existing, found, err := communicationV4ApplicationTx(
			tx, req.ProfileID, CommunicationV4InputBusinessEvent, req.Event.Key,
		)
		if err != nil {
			return err
		}
		if found {
			if existing.InputDigest != digest || existing.SemanticKind != string(req.Event.Kind) ||
				existing.MessageSeq != req.Event.MessageSeq {
				return ErrCommunicationV4Conflict
			}
			if _, _, err := materializeCommunicationV4EventActionsTx(
				tx,
				existing,
				existing.AppliedAt,
			); err != nil {
				return err
			}
			out.Aggregate = aggregate
			out.Application = existing
			return nil
		}
		if err := validateNewCommunicationV4EventBoundary(aggregate, req.Event); err != nil {
			return err
		}
		if aggregate.Revision == ^uint64(0) {
			return ErrCommunicationV4Conflict
		}

		decision, err := communication.ApplyV4BusinessEvent(aggregate.State, req.Event)
		if err != nil {
			return err
		}
		outcome := CommunicationV4ApplicationOutcome{
			Dialogue: decision.Dialogue, DialogueAfterActions: decision.DialogueAfterActions,
			Actions:      append([]communication.V4EventAction(nil), decision.Actions...),
			ManualReason: decision.ManualReason,
		}
		next := aggregate
		next.State = decision.State
		next.Revision++
		if req.Event.Source == communication.EventSourceMessage {
			next.ProjectedThroughSeq = req.Event.MessageSeq
		}
		next.UpdatedAt = req.AppliedAt
		if decision.ManualReason != "" && next.AutomationStatus == ProfileCommunicationAutomationActive {
			manualAt := req.AppliedAt
			next.AutomationStatus = ProfileCommunicationAutomationManualRequired
			next.ManualReason = string(decision.ManualReason)
			next.ManualRequiredAt = &manualAt
		}

		application := CommunicationV4ProjectionApplication{
			ProfileID: req.ProfileID, InputKind: CommunicationV4InputBusinessEvent, InputKey: req.Event.Key,
			InputDigest: digest, SemanticKind: string(req.Event.Kind), MessageSeq: req.Event.MessageSeq,
			FromRevision: aggregate.Revision, ToRevision: next.Revision,
			Outcome: outcome, AppliedAt: req.AppliedAt,
		}
		if err := persistCommunicationV4TransitionTx(tx, aggregate, next, application); err != nil {
			return err
		}
		if _, _, err := materializeCommunicationV4EventActionsTx(
			tx,
			application,
			application.AppliedAt,
		); err != nil {
			return err
		}
		out.Aggregate = next
		out.Application = application
		out.Applied = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// MarkCommunicationV4AutomationManualRequired isolates one profile without
// stopping the account worker. It exposes the existing aggregate gate to the
// production orchestrator; it does not create a second manual-state mechanism.
func (s *Store) MarkCommunicationV4AutomationManualRequired(
	profileID string,
	reason string,
	at time.Time,
) error {
	if strings.TrimSpace(profileID) == "" || strings.TrimSpace(reason) == "" {
		return ErrCommunicationV4Invalid
	}
	if at.IsZero() {
		at = time.Now()
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		return markCommunicationV4AutomationManualTx(
			tx,
			profileID,
			reason,
			at.UTC(),
		)
	})
}

// applyCommunicationV4ConfirmedActionTx is intentionally package-private. It
// may only be called from a transaction that has already proved the matching
// CommunicationAction, EffectIntent positive terminal and unique outbound
// Message. No controller may advance "sent" facts by calling a public boolean
// endpoint.
func applyCommunicationV4ConfirmedActionTx(
	tx *gorm.DB,
	profileID string,
	action communication.V4ConfirmedAction,
	appliedAt time.Time,
) (CommunicationV4Aggregate, CommunicationV4ProjectionApplication, bool, error) {
	if tx == nil || strings.TrimSpace(profileID) == "" || strings.TrimSpace(action.ActionKey) == "" ||
		appliedAt.IsZero() {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Invalid
	}
	appliedAt = appliedAt.UTC()
	digest, err := communicationV4InputDigest(action)
	if err != nil {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, err
	}
	aggregate, err := communicationV4AggregateTx(tx, profileID)
	if err != nil {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, err
	}
	existing, found, err := communicationV4ApplicationTx(
		tx, profileID, CommunicationV4InputConfirmedAction, action.ActionKey,
	)
	if err != nil {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, err
	}
	if found {
		if existing.InputDigest != digest || existing.SemanticKind != string(action.Kind) ||
			existing.MessageSeq != action.MessageSeq {
			return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Conflict
		}
		return aggregate, existing, false, nil
	}
	if aggregate.Revision == ^uint64(0) {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Conflict
	}
	if action.MessageSeq > 0 && action.MessageSeq != aggregate.ProjectedThroughSeq+1 {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Conflict
	}
	state, err := communication.ApplyV4ConfirmedAction(aggregate.State, action)
	if err != nil {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, err
	}
	next := aggregate
	next.State = state
	next.Revision++
	if action.MessageSeq > 0 {
		next.ProjectedThroughSeq = action.MessageSeq
	}
	next.UpdatedAt = appliedAt
	stateBeforeAction := aggregate.State
	projectedThroughSeqBefore := aggregate.ProjectedThroughSeq
	application := CommunicationV4ProjectionApplication{
		ProfileID: profileID, InputKind: CommunicationV4InputConfirmedAction, InputKey: action.ActionKey,
		InputDigest: digest, SemanticKind: string(action.Kind), MessageSeq: action.MessageSeq,
		FromRevision: aggregate.Revision, ToRevision: next.Revision,
		Outcome: CommunicationV4ApplicationOutcome{
			Dialogue:                  communication.V4DialogueNone,
			StateBeforeAction:         &stateBeforeAction,
			ProjectedThroughSeqBefore: &projectedThroughSeqBefore,
		},
		AppliedAt: appliedAt,
	}
	if err := persistCommunicationV4TransitionTx(tx, aggregate, next, application); err != nil {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, err
	}
	return next, application, true, nil
}

type communicationV4RetractedActionInput struct {
	ActionKey  string                     `json:"actionKey"`
	Kind       communication.V4ActionKind `json:"kind"`
	MessageSeq int64                      `json:"messageSeq"`
}

// retractCommunicationV4ConfirmedActionTx appends the sole compensation
// allowed for a visible action: a durable safe terminal has retracted the
// exact self Message that previously backed a positive confirmation. The
// original receipt remains immutable; its pre-action snapshot restores the
// aggregate without inventing an inverse for every V4 action kind.
func retractCommunicationV4ConfirmedActionTx(
	tx *gorm.DB,
	profileID string,
	action communication.V4ConfirmedAction,
	reason string,
	appliedAt time.Time,
) (CommunicationV4Aggregate, CommunicationV4ProjectionApplication, bool, error) {
	if tx == nil || strings.TrimSpace(profileID) == "" ||
		strings.TrimSpace(action.ActionKey) == "" ||
		action.MessageSeq <= 0 || strings.TrimSpace(reason) == "" ||
		appliedAt.IsZero() {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Invalid
	}
	appliedAt = appliedAt.UTC()
	confirmed, found, err := communicationV4ApplicationTx(
		tx,
		profileID,
		CommunicationV4InputConfirmedAction,
		action.ActionKey,
	)
	if err != nil {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, err
	}
	if !found ||
		confirmed.SemanticKind != string(action.Kind) ||
		confirmed.MessageSeq != action.MessageSeq ||
		confirmed.Outcome.StateBeforeAction == nil ||
		confirmed.Outcome.ProjectedThroughSeqBefore == nil ||
		*confirmed.Outcome.ProjectedThroughSeqBefore < 0 ||
		communication.ValidateV4State(*confirmed.Outcome.StateBeforeAction) != nil {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Corrupt
	}
	input := communicationV4RetractedActionInput{
		ActionKey:  action.ActionKey,
		Kind:       action.Kind,
		MessageSeq: action.MessageSeq,
	}
	digest, err := communicationV4InputDigest(input)
	if err != nil {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, err
	}
	aggregate, err := communicationV4AggregateTx(tx, profileID)
	if err != nil {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, err
	}
	existing, retracted, err := communicationV4ApplicationTx(
		tx,
		profileID,
		CommunicationV4InputRetractedAction,
		action.ActionKey,
	)
	if err != nil {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, err
	}
	if retracted {
		if existing.InputDigest != digest ||
			existing.SemanticKind != string(action.Kind) ||
			existing.MessageSeq != action.MessageSeq {
			return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Conflict
		}
		return aggregate, existing, false, nil
	}
	if aggregate.Revision != confirmed.ToRevision ||
		aggregate.ProjectedThroughSeq != action.MessageSeq ||
		aggregate.Revision == ^uint64(0) {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Conflict
	}
	next := aggregate
	next.State = *confirmed.Outcome.StateBeforeAction
	next.Revision++
	next.ProjectedThroughSeq = *confirmed.Outcome.ProjectedThroughSeqBefore
	next.UpdatedAt = appliedAt
	application := CommunicationV4ProjectionApplication{
		ProfileID: profileID, InputKind: CommunicationV4InputRetractedAction,
		InputKey: action.ActionKey, InputDigest: digest,
		SemanticKind: string(action.Kind), MessageSeq: action.MessageSeq,
		FromRevision: aggregate.Revision, ToRevision: next.Revision,
		Outcome: CommunicationV4ApplicationOutcome{
			Dialogue:     communication.V4DialogueNone,
			ManualReason: communication.V4ManualReason(reason),
		},
		AppliedAt: appliedAt,
	}
	if err := persistCommunicationV4TransitionTx(tx, aggregate, next, application); err != nil {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, err
	}
	return next, application, true, nil
}

// ApplyCommunicationV4ArchiveAction persists one deterministic schedule
// archive against the exact aggregate revision that was evaluated. Replaying
// the same ActionKey is idempotent; a different transition that wins the race
// must be re-evaluated by the caller instead of archiving stale state.
func (s *Store) ApplyCommunicationV4ArchiveAction(
	profileID string,
	expectedRevision uint64,
	action communication.V4PlannedAction,
	appliedAt time.Time,
) (CommunicationV4Aggregate, bool, error) {
	if strings.TrimSpace(profileID) == "" || strings.TrimSpace(action.ActionKey) == "" ||
		action.Kind != communication.V4ActionArchive || appliedAt.IsZero() {
		return CommunicationV4Aggregate{}, false, ErrCommunicationV4Invalid
	}
	var next CommunicationV4Aggregate
	var applied bool
	err := s.db.Transaction(func(tx *gorm.DB) error {
		_, found, err := communicationV4ApplicationTx(
			tx,
			profileID,
			CommunicationV4InputArchiveAction,
			action.ActionKey,
		)
		if err != nil {
			return err
		}
		if !found {
			current, err := communicationV4AggregateTx(tx, profileID)
			if err != nil {
				return err
			}
			if current.Revision != expectedRevision {
				return ErrCommunicationV4Conflict
			}
		}
		next, _, applied, err = applyCommunicationV4ArchiveActionTx(
			tx,
			profileID,
			action,
			appliedAt,
		)
		return err
	})
	return next, applied, err
}

// applyCommunicationV4ArchiveActionTx persists a previously planned local
// archive. It remains package-private so a controller cannot invent terminal
// state without first passing the deterministic schedule evaluator.
func applyCommunicationV4ArchiveActionTx(
	tx *gorm.DB,
	profileID string,
	action communication.V4PlannedAction,
	appliedAt time.Time,
) (CommunicationV4Aggregate, CommunicationV4ProjectionApplication, bool, error) {
	if tx == nil || strings.TrimSpace(profileID) == "" || strings.TrimSpace(action.ActionKey) == "" ||
		action.Kind != communication.V4ActionArchive || appliedAt.IsZero() {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Invalid
	}
	appliedAt = appliedAt.UTC()
	digest, err := communicationV4InputDigest(action)
	if err != nil {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, err
	}
	aggregate, err := communicationV4AggregateTx(tx, profileID)
	if err != nil {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, err
	}
	existing, found, err := communicationV4ApplicationTx(
		tx, profileID, CommunicationV4InputArchiveAction, action.ActionKey,
	)
	if err != nil {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, err
	}
	if found {
		if existing.InputDigest != digest || existing.SemanticKind != string(action.Kind) {
			return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Conflict
		}
		return aggregate, existing, false, nil
	}
	if aggregate.Revision == ^uint64(0) {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Conflict
	}
	state, err := communication.ApplyV4ArchiveAction(aggregate.State, action)
	if err != nil {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, err
	}
	if err := supersedeCommunicationV4PreEffectTurnForArchiveTx(
		tx,
		profileID,
		appliedAt,
	); err != nil {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, err
	}
	next := aggregate
	next.State = state
	next.Revision++
	next.UpdatedAt = appliedAt
	application := CommunicationV4ProjectionApplication{
		ProfileID: profileID, InputKind: CommunicationV4InputArchiveAction, InputKey: action.ActionKey,
		InputDigest: digest, SemanticKind: string(action.Kind),
		FromRevision: aggregate.Revision, ToRevision: next.Revision,
		Outcome:   CommunicationV4ApplicationOutcome{Dialogue: communication.V4DialogueNone},
		AppliedAt: appliedAt,
	}
	if err := persistCommunicationV4TransitionTx(tx, aggregate, next, application); err != nil {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, err
	}
	return next, application, true, nil
}

func supersedeCommunicationV4PreEffectTurnForArchiveTx(
	tx *gorm.DB,
	profileID string,
	at time.Time,
) error {
	var turns []DialogueTurn
	if err := tx.Where(
		"profile_id = ? AND status IN ?",
		profileID,
		[]DialogueTurnStatus{
			DialogueTurnCollected,
			DialogueTurnClassified,
			DialogueTurnAdviceReady,
		},
	).Order("created_at, turn_id").Find(&turns).Error; err != nil {
		return err
	}
	ownedCount := 0
	for index := range turns {
		turn := turns[index]
		_, owned, err := communicationV4TurnApplicationTx(tx, turn)
		if err != nil {
			return err
		}
		if !owned {
			continue
		}
		ownedCount++
		if ownedCount > 1 {
			return ErrCommunicationV4Corrupt
		}

		var actions []CommunicationAction
		if err := tx.Where("turn_id = ?", turn.TurnID).
			Order("action_id").
			Find(&actions).Error; err != nil {
			return err
		}
		if (turn.Status == DialogueTurnAdviceReady) != (len(actions) == 1) ||
			len(actions) > 1 {
			return ErrCommunicationV4Corrupt
		}
		for actionIndex := range actions {
			action := actions[actionIndex]
			if action.Status != CommunicationActionPlanned ||
				action.EffectIntentID != nil ||
				action.EffectStartedAt != nil ||
				action.SentAt != nil {
				return ErrCommunicationV4Corrupt
			}
			updated := tx.Model(&CommunicationAction{}).
				Where(
					"action_id = ? AND status = ? AND effect_intent_id IS NULL",
					action.ActionID,
					CommunicationActionPlanned,
				).
				Updates(map[string]any{
					"status":         CommunicationActionSuperseded,
					"failure_reason": communicationV4ArchiveSuperseded,
					"updated_at":     at,
				})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrCommunicationV4Conflict
			}
		}
		updated := tx.Model(&DialogueTurn{}).
			Where("turn_id = ? AND status = ?", turn.TurnID, turn.Status).
			Updates(map[string]any{
				"status":         DialogueTurnSuperseded,
				"failure_reason": communicationV4ArchiveSuperseded,
				"updated_at":     at,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrCommunicationV4Conflict
		}
	}
	return nil
}

func communicationV4AggregateTx(
	tx *gorm.DB,
	profileID string,
) (CommunicationV4Aggregate, error) {
	var aggregate CommunicationV4Aggregate
	if err := tx.First(&aggregate, "profile_id = ?", profileID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return CommunicationV4Aggregate{}, ErrCommunicationV4Missing
		}
		return CommunicationV4Aggregate{}, err
	}
	if aggregate.StateSchemaVersion != communicationV4StateSchemaVersion ||
		strings.TrimSpace(aggregate.RootGreetingIntentID) == "" || aggregate.ProjectedThroughSeq < 0 ||
		communication.ValidateV4State(aggregate.State) != nil ||
		!validProfileCommunicationAutomation(
			aggregate.AutomationStatus, aggregate.ManualReason, aggregate.ManualRequiredAt,
		) {
		return CommunicationV4Aggregate{}, ErrCommunicationV4Corrupt
	}
	var applicationBounds struct {
		Count       int64
		MinRevision uint64
		MaxRevision uint64
	}
	if err := tx.Model(&CommunicationV4ProjectionApplication{}).
		Where("profile_id = ?", profileID).
		Select("COUNT(*) AS count, COALESCE(MIN(to_revision), 0) AS min_revision, COALESCE(MAX(to_revision), 0) AS max_revision").
		Scan(&applicationBounds).Error; err != nil {
		return CommunicationV4Aggregate{}, err
	}
	if applicationBounds.Count < 0 || uint64(applicationBounds.Count) != aggregate.Revision ||
		(aggregate.Revision == 0 && (applicationBounds.MinRevision != 0 || applicationBounds.MaxRevision != 0)) ||
		(aggregate.Revision > 0 && (applicationBounds.MinRevision != 1 || applicationBounds.MaxRevision != aggregate.Revision)) {
		return CommunicationV4Aggregate{}, ErrCommunicationV4Corrupt
	}
	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", profileID).Error; err != nil {
		return CommunicationV4Aggregate{}, ErrCommunicationV4Corrupt
	}
	status, endReason, err := candidateProfileProjection(aggregate.State)
	if err != nil || !sameCandidateProfileProjection(profile, status, endReason) ||
		profile.SuccessfulGreetingIntentID == nil ||
		*profile.SuccessfulGreetingIntentID != aggregate.RootGreetingIntentID {
		return CommunicationV4Aggregate{}, ErrCommunicationV4Corrupt
	}
	return aggregate, nil
}

func communicationV4ApplicationTx(
	tx *gorm.DB,
	profileID string,
	inputKind CommunicationV4InputKind,
	inputKey string,
) (CommunicationV4ProjectionApplication, bool, error) {
	var application CommunicationV4ProjectionApplication
	err := tx.First(
		&application,
		"profile_id = ? AND input_kind = ? AND input_key = ?",
		profileID, inputKind, inputKey,
	).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CommunicationV4ProjectionApplication{}, false, nil
	}
	if err != nil {
		return CommunicationV4ProjectionApplication{}, false, err
	}
	if application.ToRevision != application.FromRevision+1 || !validCommunicationV4Digest(application.InputDigest) {
		return CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Corrupt
	}
	return application, true, nil
}

func validateNewCommunicationV4EventBoundary(
	aggregate CommunicationV4Aggregate,
	event communication.BusinessEvent,
) error {
	switch event.Source {
	case communication.EventSourceMessage:
		if event.MessageSeq <= 0 || event.Key != fmt.Sprintf("message:%d", event.MessageSeq) ||
			event.MessageSeq != aggregate.ProjectedThroughSeq+1 {
			return ErrCommunicationV4Conflict
		}
	case communication.EventSourceCardTransition:
		if event.MessageSeq <= 0 {
			return ErrCommunicationV4Invalid
		}
		if event.MessageSeq > aggregate.ProjectedThroughSeq {
			return ErrCommunicationV4Conflict
		}
	case communication.EventSourcePlatformStatus:
		if event.MessageSeq != 0 {
			return ErrCommunicationV4Invalid
		}
	default:
		return ErrCommunicationV4Invalid
	}
	return nil
}

func persistCommunicationV4TransitionTx(
	tx *gorm.DB,
	current CommunicationV4Aggregate,
	next CommunicationV4Aggregate,
	application CommunicationV4ProjectionApplication,
) error {
	if next.ProfileID != current.ProfileID || next.Revision != current.Revision+1 ||
		application.ProfileID != current.ProfileID ||
		application.FromRevision != current.Revision || application.ToRevision != next.Revision ||
		communication.ValidateV4State(next.State) != nil {
		return ErrCommunicationV4Invalid
	}
	status, endReason, err := candidateProfileProjection(next.State)
	if err != nil {
		return err
	}
	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", current.ProfileID).Error; err != nil {
		return ErrCommunicationV4Corrupt
	}
	if err := tx.Create(&application).Error; err != nil {
		return err
	}
	stateJSON, err := json.Marshal(next.State)
	if err != nil {
		return err
	}
	updated := tx.Model(&CommunicationV4Aggregate{}).
		Where("profile_id = ? AND revision = ?", current.ProfileID, current.Revision).
		Updates(map[string]any{
			"state":                 string(stateJSON),
			"revision":              next.Revision,
			"projected_through_seq": next.ProjectedThroughSeq,
			"automation_status":     next.AutomationStatus,
			"manual_reason":         next.ManualReason,
			"manual_required_at":    next.ManualRequiredAt,
			"updated_at":            next.UpdatedAt,
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrCommunicationV4Conflict
	}
	profileUpdates := map[string]any{"main_status": status, "end_reason": endReason}
	if profile.FirstRealMessageSeq == nil && next.State.LastRealMessageSeq > 0 {
		profileUpdates["first_real_message_seq"] = next.State.LastRealMessageSeq
		if profile.CommunicatingAt == nil {
			profileUpdates["communicating_at"] = application.AppliedAt
		}
	}
	profileUpdated := tx.Model(&CandidateProfile{}).
		Where("profile_id = ?", current.ProfileID).
		Updates(profileUpdates)
	if profileUpdated.Error != nil {
		return profileUpdated.Error
	}
	if profileUpdated.RowsAffected != 1 {
		return ErrCommunicationV4Corrupt
	}
	return nil
}

func communicationV4InputDigest(input any) (string, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(communicationV4InputDigestPrefix))
	_, _ = digest.Write(body)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func validCommunicationV4Digest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validProfileCommunicationAutomation(
	status ProfileCommunicationAutomationStatus,
	reason string,
	manualAt *time.Time,
) bool {
	switch status {
	case ProfileCommunicationAutomationActive:
		return strings.TrimSpace(reason) == "" && manualAt == nil
	case ProfileCommunicationAutomationManualRequired:
		return strings.TrimSpace(reason) != "" && manualAt != nil && !manualAt.IsZero()
	default:
		return false
	}
}

func candidateProfileProjection(
	state communication.V4State,
) (CandidateProfileStatus, *CandidateProfileEndReason, error) {
	var status CandidateProfileStatus
	switch state.MainStatus {
	case communication.V4StatusGreeted:
		status = CandidateProfileGreeted
	case communication.V4StatusCommunicating:
		status = CandidateProfileCommunicating
	case communication.V4StatusInvited:
		status = CandidateProfileInvited
	case communication.V4StatusInterviewed:
		status = CandidateProfileInterviewed
	case communication.V4StatusEnded:
		status = CandidateProfileEnded
	case communication.V4StatusEliminated:
		status = CandidateProfileEliminated
	default:
		return "", nil, ErrCommunicationV4Invalid
	}
	if state.EndReason == "" {
		return status, nil, nil
	}
	var reason CandidateProfileEndReason
	switch state.EndReason {
	case communication.V4EndRejected:
		reason = CandidateProfileEndRejected
	case communication.V4EndBlacklisted:
		reason = CandidateProfileEndBlacklisted
	case communication.V4EndFallback:
		reason = CandidateProfileEndFallbackArchive
	case communication.V4EndSilentInterview:
		reason = CandidateProfileEndSilentInterviewPending
	case communication.V4EndSilentWechatInvited:
		reason = CandidateProfileEndSilentWechatInvited
	case communication.V4EndSilentWechatExchanged:
		reason = CandidateProfileEndSilentWechatExchanged
	case communication.V4EndSilent:
		reason = CandidateProfileEndSilent
	default:
		return "", nil, ErrCommunicationV4Invalid
	}
	return status, &reason, nil
}

func sameCandidateProfileProjection(
	profile CandidateProfile,
	status CandidateProfileStatus,
	reason *CandidateProfileEndReason,
) bool {
	if profile.MainStatus != status {
		return false
	}
	if profile.EndReason == nil || reason == nil {
		return profile.EndReason == nil && reason == nil
	}
	return *profile.EndReason == *reason
}
