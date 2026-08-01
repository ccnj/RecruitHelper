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
	"recruithelper/client/service/internal/m5ai"

	"gorm.io/gorm"
)

const (
	communicationV4StateSchemaVersion = 1
	communicationV4InputDigestPrefix  = "communication-v4-input-v1|"
	communicationV4ArchiveSuperseded  = "scheduleArchivedBeforeEffect"
)

var (
	ErrCommunicationV4Invalid            = errors.New("V4 沟通聚合输入无效")
	ErrCommunicationV4Missing            = errors.New("V4 沟通聚合不存在")
	ErrCommunicationV4Conflict           = errors.New("V4 沟通聚合或投影冲突")
	ErrCommunicationV4Corrupt            = errors.New("V4 沟通聚合损坏")
	ErrCommunicationV4AnchorUnresolvable = errors.New("V4 沟通轮出站锚不可解析")
)

// CommunicationV4DirectSendBlocked 判定一个会话是否禁止 admin 冒烟直发候选人
// 可见卡片（2026-07-27 甲方批准的冒烟生产者附带闸）：仅当档案的 V4 沟通自动化
// 仍为 active 时阻止——active 档案的轮出站锚只认动作轨自己的出站，计划外出站会
// 在候选人下次回话时判 outboundBoundaryMissing 挂人工。无档案、无聚合或已挂
// 人工的会话放行。
func (s *Store) CommunicationV4DirectSendBlocked(key ConversationKey) (bool, error) {
	profile, err := s.CandidateProfileByConversation(key)
	if err != nil || profile == nil {
		return false, err
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(profile.ProfileID)
	if errors.Is(err, ErrCommunicationV4Missing) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return aggregate.AutomationStatus == ProfileCommunicationAutomationActive, nil
}

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
	if projectedThroughSeq > 0 {
		state.LastOutboundMessageSeq = projectedThroughSeq
	}
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

// bindCommunicationV4RootConversationTx advances the ledger projection
// boundary and materializes the outbound turn anchor when a previously
// unbound successful greeting becomes message 1 during late conversation
// adoption. The greeting is already part of the root state, so this is not a
// new reducer input and does not increment Revision.
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
	state := aggregate.State
	state.LastOutboundMessageSeq = messageSeq
	if err := communication.ValidateV4State(state); err != nil {
		return ErrCommunicationV4Corrupt
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return err
	}
	updated := tx.Model(&CommunicationV4Aggregate{}).
		Where("profile_id = ? AND revision = 0 AND projected_through_seq = 0", profileID).
		Updates(map[string]any{
			"projected_through_seq": messageSeq,
			"state":                 string(stateJSON),
			"updated_at":            now.UTC(),
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrCommunicationV4Conflict
	}
	return nil
}

// communicationV4OutboundAnchorSeqTx resolves the turn-boundary outbound
// anchor for one aggregate. State carries the anchor from root creation or
// the latest confirmed action; pre-decoupling greeting roots never recorded
// it, so the greeting message is recovered through its immutable intent
// linkage instead of guessing from the newest outbound row. Candidate-
// initiated roots legitimately have no outbound anchor.
func communicationV4OutboundAnchorSeqTx(
	tx *gorm.DB,
	aggregate CommunicationV4Aggregate,
) (int64, error) {
	if aggregate.State.LastOutboundMessageSeq > 0 {
		return aggregate.State.LastOutboundMessageSeq, nil
	}
	if IsInboundConversationV4Root(aggregate.RootGreetingIntentID) {
		return 0, nil
	}
	var greeting Message
	if err := tx.First(
		&greeting,
		"outbound_intent_id = ? AND retracted_at IS NULL",
		aggregate.RootGreetingIntentID,
	).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrCommunicationV4AnchorUnresolvable
		}
		return 0, err
	}
	if greeting.Direction != "out" || greeting.Seq <= 0 {
		return 0, ErrCommunicationV4AnchorUnresolvable
	}
	return greeting.Seq, nil
}

// CommunicationV4OutboundAnchorSeq exposes the turn-boundary outbound anchor
// to the patrol layer. Zero means a candidate-initiated root; greeting roots
// without a resolvable anchor return ErrCommunicationV4AnchorUnresolvable.
func (s *Store) CommunicationV4OutboundAnchorSeq(profileID string) (int64, error) {
	if strings.TrimSpace(profileID) == "" {
		return 0, ErrCommunicationV4Invalid
	}
	var seq int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		aggregate, err := communicationV4AggregateTx(tx, profileID)
		if err != nil {
			return err
		}
		anchor, anchorErr := communicationV4OutboundAnchorSeqTx(tx, aggregate)
		if anchorErr != nil {
			return anchorErr
		}
		seq = anchor
		return nil
	})
	return seq, err
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
	return applyCommunicationV4ConfirmedActionWithContinuationTx(
		tx,
		profileID,
		action,
		nil,
		appliedAt,
	)
}

// communicationV4WechatContinuation carries the frozen turn a confirmed action
// re-opens for its follow-up dialogue. Advice selects the shape: V4AdviceReply
// is the candidate-initiated wechat acceptance (2026-07-26), V4AdviceServiceReply
// is the post-interview fixed-segment suffix (2026-07-31 spec §5(3)).
type communicationV4WechatContinuation struct {
	Turn                 DialogueTurn
	ExpectedFromRevision uint64
	Advice               communication.V4AdvicePurpose
}

func applyCommunicationV4ConfirmedActionWithContinuationTx(
	tx *gorm.DB,
	profileID string,
	action communication.V4ConfirmedAction,
	continuation *communicationV4WechatContinuation,
	appliedAt time.Time,
) (CommunicationV4Aggregate, CommunicationV4ProjectionApplication, bool, error) {
	if tx == nil || strings.TrimSpace(profileID) == "" || strings.TrimSpace(action.ActionKey) == "" ||
		appliedAt.IsZero() {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Invalid
	}
	if continuation != nil {
		if continuation.Turn.ProfileID != profileID ||
			strings.TrimSpace(continuation.Turn.TurnID) == "" {
			return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Invalid
		}
		switch continuation.Advice {
		case communication.V4AdviceReply:
			if action.Kind != communication.V4ActionAcceptWechat || action.MessageSeq != 0 {
				return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Invalid
			}
		case communication.V4AdviceServiceReply:
			if action.MessageSeq <= 0 {
				return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Invalid
			}
		default:
			return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Invalid
		}
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
		if continuation != nil {
			switch continuation.Advice {
			case communication.V4AdviceReply:
				if existing.Outcome.Dialogue != communication.V4DialogueWechatContinuation ||
					existing.Outcome.DialogueStatus != communication.V4DialogueWaitingAdvice ||
					existing.Outcome.NextAdvice != communication.V4AdviceReply ||
					existing.Outcome.IntentLabel != m5ai.IntentInterested ||
					existing.Outcome.IntentSource != communication.IntentSourceBusinessEvent {
					return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Conflict
				}
			case communication.V4AdviceServiceReply:
				if existing.Outcome.Dialogue != communication.V4DialogueServiceReply ||
					existing.Outcome.DialogueStatus != communication.V4DialogueWaitingAdvice ||
					existing.Outcome.NextAdvice != communication.V4AdviceServiceReply ||
					existing.Outcome.IntentLabel != "" ||
					existing.Outcome.IntentSource != "" {
					return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Conflict
				}
			}
		}
		return aggregate, existing, false, nil
	}
	if aggregate.Revision == ^uint64(0) {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Conflict
	}
	if action.MessageSeq > 0 {
		if action.MessageSeq <= aggregate.ProjectedThroughSeq {
			return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Conflict
		}
		// 确认投影不得越过任何候选人输入、另一条我方出站或账本缺行；
		// 游标与确认消息之间的每个 seq 都必须存在且是平台中性 system 行
		// （0727当日计划3）。中性行本就是 no-op 事件，被越过与逐条投影
		// 同终态。
		if action.MessageSeq != aggregate.ProjectedThroughSeq+1 {
			var profile CandidateProfile
			if err := tx.First(&profile, "profile_id = ?", profileID).Error; err != nil {
				return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, err
			}
			if profile.ConversationRef == nil {
				return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Conflict
			}
			var neutral int64
			if err := tx.Model(&Message{}).
				Where(
					"platform = ? AND account_ref = ? AND conversation_ref = ? AND retracted_at IS NULL "+
						"AND seq > ? AND seq < ? AND (direction = ? OR (direction = ? AND kind = ?))",
					profile.Platform,
					profile.AccountRef,
					*profile.ConversationRef,
					aggregate.ProjectedThroughSeq,
					action.MessageSeq,
					"system",
					"in",
					"system",
				).
				Count(&neutral).Error; err != nil {
				return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, err
			}
			if neutral != action.MessageSeq-aggregate.ProjectedThroughSeq-1 {
				return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Conflict
			}
		}
	}
	if continuation != nil && continuation.ExpectedFromRevision != aggregate.Revision {
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
	outcome := CommunicationV4ApplicationOutcome{
		Dialogue:                  communication.V4DialogueNone,
		StateBeforeAction:         &stateBeforeAction,
		ProjectedThroughSeqBefore: &projectedThroughSeqBefore,
	}
	if continuation != nil {
		switch continuation.Advice {
		case communication.V4AdviceReply:
			outcome.Dialogue = communication.V4DialogueWechatContinuation
			outcome.DialogueStatus = communication.V4DialogueWaitingAdvice
			outcome.NextAdvice = communication.V4AdviceReply
			outcome.IntentLabel = m5ai.IntentInterested
			outcome.IntentSource = communication.IntentSourceBusinessEvent
		case communication.V4AdviceServiceReply:
			outcome.Dialogue = communication.V4DialogueServiceReply
			outcome.DialogueStatus = communication.V4DialogueWaitingAdvice
			outcome.NextAdvice = communication.V4AdviceServiceReply
		}
	}
	application := CommunicationV4ProjectionApplication{
		ProfileID: profileID, InputKind: CommunicationV4InputConfirmedAction, InputKey: action.ActionKey,
		InputDigest: digest, SemanticKind: string(action.Kind), MessageSeq: action.MessageSeq,
		FromRevision: aggregate.Revision, ToRevision: next.Revision,
		Outcome:   outcome,
		AppliedAt: appliedAt,
	}
	if err := persistCommunicationV4TransitionTx(tx, aggregate, next, application); err != nil {
		return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, err
	}
	if continuation != nil {
		expectedLabel, expectedSource := m5ai.IntentInterested, DialogueIntentBusinessEvent
		if continuation.Advice == communication.V4AdviceServiceReply {
			expectedLabel, expectedSource = "", ""
		}
		updated := tx.Model(&DialogueTurn{}).
			Where(
				"turn_id = ? AND profile_id = ? AND status = ? AND intent_label = ? AND intent_source = ?",
				continuation.Turn.TurnID,
				profileID,
				DialogueTurnCollected,
				expectedLabel,
				expectedSource,
			).
			Updates(map[string]any{
				"status":         DialogueTurnClassified,
				"classified_at":  appliedAt,
				"failure_reason": "",
				"updated_at":     appliedAt,
			})
		if updated.Error != nil {
			return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, updated.Error
		}
		if updated.RowsAffected != 1 {
			return CommunicationV4Aggregate{}, CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Conflict
		}
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
		var tail *CommunicationAction
		switch {
		case turn.Status != DialogueTurnAdviceReady && len(actions) == 0:
		case turn.Status == DialogueTurnAdviceReady && len(actions) == 1:
			tail = &actions[0]
		case turn.Status == DialogueTurnAdviceReady && len(actions) > 1:
			validatedTail, err := communicationV4ArchiveMultiBubbleTailTx(
				tx,
				turn,
				actions,
			)
			if err != nil {
				return err
			}
			tail = validatedTail
		default:
			return ErrCommunicationV4Corrupt
		}
		if tail != nil {
			if tail.Status != CommunicationActionPlanned ||
				tail.FailureReason != "" ||
				tail.EffectIntentID != nil ||
				tail.EffectStartedAt != nil ||
				tail.SentAt != nil {
				return ErrCommunicationV4Corrupt
			}
			updated := tx.Model(&CommunicationAction{}).
				Where(
					"action_id = ? AND status = ? AND effect_intent_id IS NULL "+
						"AND effect_started_at IS NULL AND sent_at IS NULL",
					tail.ActionID,
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

// communicationV4ArchiveMultiBubbleTailTx recognizes the only post-effect
// dialogue shape that a seven-day fallback may safely close: a contiguous
// prefix already backed by positive effect facts and one not-yet-started
// materialized tail. It deliberately does not require the last confirmed
// message to remain the current conversation tail; a later candidate inbound
// is precisely one of the cases where the seven-day fallback must still win.
func communicationV4ArchiveMultiBubbleTailTx(
	tx *gorm.DB,
	turn DialogueTurn,
	actions []CommunicationAction,
) (*CommunicationAction, error) {
	if tx == nil ||
		turn.Status != DialogueTurnAdviceReady ||
		len(actions) < 2 {
		return nil, ErrCommunicationV4Corrupt
	}
	head, owned, err := communicationV4TurnHeadApplicationTx(tx, turn)
	if err != nil {
		return nil, err
	}
	if !owned ||
		head.Outcome.DialogueStatus != communication.V4DialogueActionsPlanned ||
		head.Outcome.NextAdvice != communication.V4AdviceNone ||
		head.Outcome.ManualReason != "" ||
		!validPersistedCommunicationV4Plans(head.Outcome.PlannedActions) ||
		len(actions) > len(head.Outcome.PlannedActions) {
		return nil, ErrCommunicationV4Corrupt
	}

	byID := make(map[string]CommunicationAction, len(actions))
	for index := range actions {
		action := actions[index]
		if _, duplicate := byID[action.ActionID]; duplicate {
			return nil, ErrCommunicationV4Corrupt
		}
		byID[action.ActionID] = action
	}
	ordered := make([]CommunicationAction, len(actions))
	for index := range ordered {
		plan := head.Outcome.PlannedActions[index]
		action, found := byID[plan.ActionKey]
		if !found {
			return nil, ErrCommunicationV4Corrupt
		}
		expectedText, ready := communicationV4PlanText(
			turn,
			head.Outcome.PlannedActions,
			index,
			action.Text,
		)
		if !ready ||
			!communicationActionMatchesV4Plan(
				action,
				plan,
				expectedText,
				communicationV4ExpectedParentActionID(
					head.Outcome.PlannedActions,
					index,
				),
			) {
			return nil, ErrCommunicationV4Corrupt
		}
		ordered[index] = action
	}

	tail := ordered[len(ordered)-1]
	if tail.Status != CommunicationActionPlanned ||
		tail.FailureReason != "" ||
		tail.EffectIntentID != nil ||
		tail.EffectStartedAt != nil ||
		tail.SentAt != nil {
		return nil, ErrCommunicationV4Corrupt
	}
	for index := 0; index < len(ordered)-1; index++ {
		if err := validateCommunicationV4ArchiveSentPrefixTx(
			tx,
			turn,
			ordered[index],
			head.Outcome.PlannedActions[index],
		); err != nil {
			return nil, err
		}
	}
	return &tail, nil
}

func validateCommunicationV4ArchiveSentPrefixTx(
	tx *gorm.DB,
	turn DialogueTurn,
	action CommunicationAction,
	plan communication.V4PlannedAction,
) error {
	if action.Status != CommunicationActionSent ||
		action.FailureReason != "" ||
		action.EffectIntentID == nil ||
		action.EffectStartedAt == nil ||
		action.SentAt == nil {
		return ErrCommunicationV4Corrupt
	}
	var intent EffectIntent
	if err := tx.First(
		&intent,
		"intent_id = ?",
		*action.EffectIntentID,
	).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrCommunicationV4Corrupt
		}
		return err
	}
	if intent.Status != EffectIntentOk &&
		intent.Status != EffectIntentResolvedOk {
		return ErrCommunicationV4Corrupt
	}
	if err := validateM5AutomaticIntentLinkTx(
		tx,
		action.ActionID,
		intent,
	); err != nil {
		return ErrCommunicationV4Corrupt
	}
	if intent.ResultMessageSeq == nil {
		return ErrCommunicationV4Corrupt
	}

	var messages []Message
	if err := tx.Where("outbound_intent_id = ?", intent.IntentID).
		Limit(2).
		Find(&messages).Error; err != nil {
		return err
	}
	if len(messages) != 1 {
		return ErrCommunicationV4Corrupt
	}
	message := messages[0]
	if message.RetractedAt != nil ||
		message.OutboundIntentID == nil ||
		*message.OutboundIntentID != intent.IntentID ||
		message.Origin != "self" ||
		message.ConversationRef != turn.ConversationRef ||
		message.Seq != *intent.ResultMessageSeq ||
		!communicationActionMatchesMessage(action, message) {
		return ErrCommunicationV4Corrupt
	}

	confirmedAt := action.SentAt
	if message.TsApproxMs != nil {
		value := time.UnixMilli(*message.TsApproxMs).UTC()
		confirmedAt = &value
	}
	confirmed := communication.V4ConfirmedAction{
		ActionKey:      plan.ActionKey,
		Kind:           plan.Kind,
		MessageSeq:     message.Seq,
		CardMessageSeq: plan.CardMessageSeq,
		SentAt:         confirmedAt,
		Round:          plan.Round,
		Stage:          plan.Stage,
	}
	digest, err := communicationV4InputDigest(confirmed)
	if err != nil {
		return err
	}
	application, found, err := communicationV4ApplicationTx(
		tx,
		turn.ProfileID,
		CommunicationV4InputConfirmedAction,
		plan.ActionKey,
	)
	if err != nil {
		return err
	}
	if !found ||
		application.InputDigest != digest ||
		application.SemanticKind != string(plan.Kind) ||
		application.MessageSeq != message.Seq ||
		application.FromRevision == ^uint64(0) ||
		application.ToRevision != application.FromRevision+1 ||
		application.Outcome.Dialogue != communication.V4DialogueNone ||
		application.Outcome.StateBeforeAction == nil ||
		application.Outcome.ProjectedThroughSeqBefore == nil ||
		*application.Outcome.ProjectedThroughSeqBefore+1 != message.Seq ||
		communication.ValidateV4State(
			*application.Outcome.StateBeforeAction,
		) != nil {
		return ErrCommunicationV4Corrupt
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
	if err != nil || !sameCandidateProfileProjection(profile, status, endReason) {
		return CommunicationV4Aggregate{}, ErrCommunicationV4Corrupt
	}
	if IsInboundConversationV4Root(aggregate.RootGreetingIntentID) {
		if err := validateInboundConversationV4RootTx(tx, aggregate, profile); err != nil {
			return CommunicationV4Aggregate{}, ErrCommunicationV4Corrupt
		}
	} else if profile.SuccessfulGreetingIntentID == nil ||
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
	// 首次进入已约面的时刻。只写一次:规格 §45 允许归档后点旧卡再次生效
	// (ended→interviewed),若每次都覆盖,同一人会反复计入"今日新约面"。
	if status == CandidateProfileInterviewed && profile.InterviewedAt == nil {
		profileUpdates["interviewed_at"] = application.AppliedAt
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
	// 约面成功的权威时点=主线跨入 interviewed 的这次持久化。本函数是全部 V4
	// 聚合转换的唯一持久化汇点(事件应用、inbound 轮冻结、建议应用、动作
	// 确认/回退各路径都经此),在此入队才覆盖真实链路——真机上候选人接受
	// 表现为 in 方向 accepted 卡消息,走 inbound 轮冻结,不产生卡片跃迁事实。
	// event_key 幂等保证每候选人终身一次(2026-07-28 裁决,照抄旧项目)。
	if current.State.MainStatus != communication.V4StatusInterviewed &&
		next.State.MainStatus == communication.V4StatusInterviewed {
		if err := enqueueNotificationTx(
			tx,
			NotificationTypeInterviewAccepted,
			"interviewAccepted:"+next.ProfileID,
			next.ProfileID,
			application.AppliedAt,
		); err != nil {
			return err
		}
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
