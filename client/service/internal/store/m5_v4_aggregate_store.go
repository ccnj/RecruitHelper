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
	state, err := communication.ApplyV4ConfirmedAction(aggregate.State, action)
	if err != nil {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, err
	}
	next := aggregate
	next.State = state
	next.Revision++
	next.UpdatedAt = appliedAt
	application := CommunicationV4ProjectionApplication{
		ProfileID: profileID, InputKind: CommunicationV4InputConfirmedAction, InputKey: action.ActionKey,
		InputDigest: digest, SemanticKind: string(action.Kind), MessageSeq: action.MessageSeq,
		FromRevision: aggregate.Revision, ToRevision: next.Revision,
		Outcome:   CommunicationV4ApplicationOutcome{Dialogue: communication.V4DialogueNone},
		AppliedAt: appliedAt,
	}
	if err := persistCommunicationV4TransitionTx(tx, aggregate, next, application); err != nil {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, err
	}
	return next, application, true, nil
}

// applyCommunicationV4ArchiveActionTx persists a previously planned local
// archive. It is also package-private so a controller cannot invent terminal
// state without the schedule/action transaction that owns ActionKey.
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
