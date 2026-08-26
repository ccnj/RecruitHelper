package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"recruithelper/client/service/internal/communication"

	"gorm.io/gorm"
)

const communicationV4ScheduleOccurrenceIDPrefix = "communication-v4-schedule-occurrence-v1|"

type ApplyCommunicationV4ArchiveActionRequest struct {
	ProfileID                   string
	ConversationRef             string
	ExpectedRevision            uint64
	ExpectedProjectedThroughSeq int64
	HasPendingDialogue          bool
	Action                      communication.V4PlannedAction
	EvaluatedAt                 time.Time
	AppliedAt                   time.Time
}

type ApplyCommunicationV4ArchiveActionResult struct {
	Aggregate  CommunicationV4Aggregate
	Occurrence CommunicationV4ScheduleOccurrence
	Applied    bool
}

// ApplyCommunicationV4ArchiveAction re-evaluates and freezes one local archive
// against the exact aggregate and ledger boundary seen by patrol. The applied
// occurrence, immutable projection application and aggregate transition are
// one SQLite transaction, so an archive can never be left planned halfway.
func (s *Store) ApplyCommunicationV4ArchiveAction(
	req ApplyCommunicationV4ArchiveActionRequest,
) (*ApplyCommunicationV4ArchiveActionResult, error) {
	if strings.TrimSpace(req.ProfileID) == "" ||
		strings.TrimSpace(req.ConversationRef) == "" ||
		req.ExpectedProjectedThroughSeq < 0 ||
		strings.TrimSpace(req.Action.ActionKey) == "" ||
		req.Action.Kind != communication.V4ActionArchive ||
		req.Action.AnchorMessageSeq < 0 ||
		req.Action.DueAt == nil ||
		req.Action.DueAt.IsZero() ||
		req.EvaluatedAt.IsZero() ||
		req.AppliedAt.IsZero() {
		return nil, ErrCommunicationV4Invalid
	}
	req.EvaluatedAt = req.EvaluatedAt.UTC()
	req.AppliedAt = req.AppliedAt.UTC()
	if req.AppliedAt.Before(req.EvaluatedAt) {
		return nil, ErrCommunicationV4Invalid
	}

	out := &ApplyCommunicationV4ArchiveActionResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		aggregate, err := communicationV4AggregateTx(tx, req.ProfileID)
		if err != nil {
			return err
		}
		occurrenceID := communicationV4ScheduleOccurrenceID(
			req.ProfileID,
			req.Action.ActionKey,
		)
		existing, found, err := communicationV4ScheduleOccurrenceTx(tx, occurrenceID)
		if err != nil {
			return err
		}
		if found {
			if err := validateCommunicationV4ArchiveOccurrenceReplayTx(
				tx,
				existing,
				req,
			); err != nil {
				return err
			}
			out.Aggregate = aggregate
			out.Occurrence = existing
			return nil
		}

		if aggregate.Revision != req.ExpectedRevision ||
			aggregate.ProjectedThroughSeq != req.ExpectedProjectedThroughSeq ||
			aggregate.Revision == ^uint64(0) {
			return ErrCommunicationV4Conflict
		}
		profile, conversation, activeTail, err := communicationV4ArchiveBoundaryTx(
			tx,
			req.ProfileID,
			req.ConversationRef,
		)
		if err != nil {
			return err
		}
		if profile.Platform != conversation.Platform ||
			profile.AccountRef != conversation.AccountRef ||
			profile.ConversationRef == nil ||
			*profile.ConversationRef != conversation.ConversationRef ||
			// 会话尾计数与活动尾的差(已撤回尾行)交由下方按档归档校验的
			// C5 新鲜度判定统一处置,这里只守方向性不变量。
			activeTail < aggregate.ProjectedThroughSeq {
			return ErrCommunicationV4Conflict
		}
		if err := validateCommunicationV4ArchiveTailTx(
			tx,
			profile,
			conversation,
			aggregate,
			activeTail,
			req.Action.EndReason,
		); err != nil {
			return err
		}

		decision, err := communication.EvaluateV4Schedule(communication.V4ScheduleInput{
			ProfileKey:          req.ProfileID,
			State:               aggregate.State,
			ProjectedThroughSeq: aggregate.ProjectedThroughSeq,
			Now:                 req.EvaluatedAt,
			HasPendingDialogue:  req.HasPendingDialogue,
			Reply:               communication.ReplyAdvice{State: communication.AdviceAbsent},
		})
		if err != nil ||
			decision.Status != communication.V4ScheduleActionsPlanned ||
			len(decision.Actions) != 1 ||
			decision.Actions[0].Kind != communication.V4ActionArchive {
			return ErrCommunicationV4Conflict
		}
		plannedDigest, err := communicationV4InputDigest(decision.Actions[0])
		if err != nil {
			return err
		}
		requestDigest, err := communicationV4InputDigest(req.Action)
		if err != nil {
			return err
		}
		if plannedDigest != requestDigest ||
			req.Action.AnchorMessageSeq != aggregate.ProjectedThroughSeq ||
			req.Action.Round != aggregate.State.RealMessageRound {
			return ErrCommunicationV4Conflict
		}

		next, application, applied, err := applyCommunicationV4ArchiveActionTx(
			tx,
			req.ProfileID,
			req.Action,
			req.AppliedAt,
		)
		if err != nil {
			return err
		}
		if !applied {
			// A projection application without the same atomic occurrence can
			// only be a pre-occurrence legacy row or partial corruption. It is
			// retained for compatibility but cannot be silently reinterpreted
			// as a newly frozen schedule fact.
			return ErrCommunicationV4Conflict
		}
		occurrence := CommunicationV4ScheduleOccurrence{
			OccurrenceID: occurrenceID, OccurrenceKey: req.Action.ActionKey,
			ProfileID: req.ProfileID, Kind: req.Action.Kind,
			BasisRevision:            req.ExpectedRevision,
			BasisProjectedThroughSeq: req.ExpectedProjectedThroughSeq,
			ConversationRef:          req.ConversationRef,
			AnchorMessageSeq:         req.Action.AnchorMessageSeq,
			DueAt:                    req.Action.DueAt.UTC(), EvaluatedAt: req.EvaluatedAt,
			Round: req.Action.Round, Stage: req.Action.Stage,
			CardMessageSeq: req.Action.CardMessageSeq,
			EndReason:      req.Action.EndReason,
			Status:         CommunicationV4ScheduleOccurrenceApplied,
			AppliedAt:      req.AppliedAt, CreatedAt: req.AppliedAt, UpdatedAt: req.AppliedAt,
		}
		if application.InputKey != occurrence.OccurrenceKey ||
			application.FromRevision != occurrence.BasisRevision ||
			application.ToRevision != next.Revision {
			return ErrCommunicationV4Corrupt
		}
		if err := tx.Create(&occurrence).Error; err != nil {
			return err
		}
		out.Aggregate = next
		out.Occurrence = occurrence
		out.Applied = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func communicationV4ScheduleOccurrenceID(profileID, occurrenceKey string) string {
	sum := sha256.Sum256([]byte(
		communicationV4ScheduleOccurrenceIDPrefix + profileID + "\x00" + occurrenceKey,
	))
	return hex.EncodeToString(sum[:])
}

func communicationV4ScheduleOccurrenceTx(
	tx *gorm.DB,
	occurrenceID string,
) (CommunicationV4ScheduleOccurrence, bool, error) {
	var occurrence CommunicationV4ScheduleOccurrence
	err := tx.First(
		&occurrence,
		"occurrence_id = ?",
		occurrenceID,
	).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CommunicationV4ScheduleOccurrence{}, false, nil
	}
	if err != nil {
		return CommunicationV4ScheduleOccurrence{}, false, err
	}
	if occurrence.Status != CommunicationV4ScheduleOccurrenceApplied ||
		occurrence.FailureReason != "" ||
		strings.TrimSpace(occurrence.ProfileID) == "" ||
		strings.TrimSpace(occurrence.OccurrenceKey) == "" ||
		strings.TrimSpace(occurrence.ConversationRef) == "" ||
		occurrence.Kind != communication.V4ActionArchive ||
		occurrence.BasisProjectedThroughSeq < 0 ||
		occurrence.AnchorMessageSeq < 0 ||
		occurrence.Round == 0 ||
		!validCommunicationV4OccurrenceTimes(occurrence) {
		return CommunicationV4ScheduleOccurrence{}, false, ErrCommunicationV4Corrupt
	}
	return occurrence, true, nil
}

func validCommunicationV4OccurrenceTimes(
	occurrence CommunicationV4ScheduleOccurrence,
) bool {
	return !occurrence.DueAt.IsZero() &&
		!occurrence.EvaluatedAt.IsZero() &&
		!occurrence.AppliedAt.IsZero() &&
		!occurrence.CreatedAt.IsZero() &&
		!occurrence.UpdatedAt.IsZero() &&
		!occurrence.EvaluatedAt.Before(occurrence.DueAt) &&
		!occurrence.AppliedAt.Before(occurrence.EvaluatedAt)
}

func validateCommunicationV4ArchiveOccurrenceReplayTx(
	tx *gorm.DB,
	existing CommunicationV4ScheduleOccurrence,
	req ApplyCommunicationV4ArchiveActionRequest,
) error {
	if existing.OccurrenceID != communicationV4ScheduleOccurrenceID(
		req.ProfileID,
		req.Action.ActionKey,
	) ||
		existing.ProfileID != req.ProfileID ||
		existing.OccurrenceKey != req.Action.ActionKey ||
		existing.ConversationRef != req.ConversationRef ||
		existing.BasisRevision != req.ExpectedRevision ||
		existing.BasisProjectedThroughSeq != req.ExpectedProjectedThroughSeq ||
		existing.AnchorMessageSeq != req.Action.AnchorMessageSeq ||
		existing.Kind != req.Action.Kind ||
		existing.Round != req.Action.Round ||
		existing.Stage != req.Action.Stage ||
		existing.CardMessageSeq != req.Action.CardMessageSeq ||
		existing.EndReason != req.Action.EndReason ||
		req.Action.DueAt == nil ||
		!existing.DueAt.Equal(req.Action.DueAt.UTC()) {
		return ErrCommunicationV4Conflict
	}
	application, found, err := communicationV4ApplicationTx(
		tx,
		req.ProfileID,
		CommunicationV4InputArchiveAction,
		req.Action.ActionKey,
	)
	if err != nil {
		return err
	}
	if !found ||
		application.FromRevision != existing.BasisRevision ||
		application.ToRevision != existing.BasisRevision+1 ||
		application.SemanticKind != string(communication.V4ActionArchive) {
		return ErrCommunicationV4Corrupt
	}
	digest, err := communicationV4InputDigest(req.Action)
	if err != nil {
		return err
	}
	if application.InputDigest != digest {
		return ErrCommunicationV4Conflict
	}
	return nil
}

func communicationV4ArchiveBoundaryTx(
	tx *gorm.DB,
	profileID string,
	conversationRef string,
) (CandidateProfile, Conversation, int64, error) {
	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", profileID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return CandidateProfile{}, Conversation{}, 0, ErrCommunicationV4Missing
		}
		return CandidateProfile{}, Conversation{}, 0, err
	}
	if profile.ConversationRef == nil ||
		*profile.ConversationRef != conversationRef {
		return CandidateProfile{}, Conversation{}, 0, ErrCommunicationV4Conflict
	}
	key := ConversationKey{
		Platform:        profile.Platform,
		AccountRef:      profile.AccountRef,
		ConversationRef: conversationRef,
	}
	var conversation Conversation
	if err := tx.Where(
		conversationWhere(key),
		conversationArgs(key)...,
	).First(&conversation).Error; err != nil {
		return CandidateProfile{}, Conversation{}, 0, ErrCommunicationV4Conflict
	}
	var activeTail struct {
		Seq int64
	}
	if err := tx.Model(&Message{}).
		Where(conversationWhere(key), conversationArgs(key)...).
		Where(activeMessageCondition).
		Select("COALESCE(MAX(seq), 0) AS seq").
		Scan(&activeTail).Error; err != nil {
		return CandidateProfile{}, Conversation{}, 0, err
	}
	return profile, conversation, activeTail.Seq, nil
}

func validateCommunicationV4ArchiveTailTx(
	tx *gorm.DB,
	profile CandidateProfile,
	conversation Conversation,
	aggregate CommunicationV4Aggregate,
	activeTail int64,
	reason communication.V4EndReason,
) error {
	if reason != communication.V4EndFallback {
		// Ordinary 36-hour archive is the last schedule tier and must be
		// evaluated only after the whole active ledger tail was projected.
		// 无主 system/已撤回后缀按 C5(2026-08-27 甲方裁决)放行,与派发闸
		// 的 Q7 容忍同一把尺子。
		fresh, err := communicationV4ScheduleTailFreshTx(
			tx, "36小时归档", profile.Platform, profile.AccountRef,
			conversation.ConversationRef,
			aggregate.ProjectedThroughSeq, activeTail, conversation.LastMessageSeq,
		)
		if err != nil {
			return err
		}
		if !fresh {
			return ErrCommunicationV4Conflict
		}
		return nil
	}

	// Seven-day fallback deliberately outranks pending dialogue. Candidate
	// inbound and system rows after the projected cursor therefore do not make
	// the evaluation stale. Any outbound does: a human body may have slid the
	// authoritative LastBodyAt even though the old aggregate has not projected
	// it yet, so archiving against that snapshot would be wrong.
	var outboundCount int64
	if err := tx.Model(&Message{}).
		Where(
			"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq > ? AND direction = ?",
			profile.Platform,
			profile.AccountRef,
			conversation.ConversationRef,
			aggregate.ProjectedThroughSeq,
			"out",
		).
		Where(activeMessageCondition).
		Count(&outboundCount).Error; err != nil {
		return err
	}
	if outboundCount != 0 {
		return ErrCommunicationV4Conflict
	}
	return nil
}
