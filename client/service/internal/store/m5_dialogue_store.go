package store

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/textcanon"
	"recruithelper/contract/gen/go/protocol"

	"gorm.io/gorm"
)

var (
	ErrDialogueTurnInvalid         = errors.New("沟通轮输入无效")
	ErrDialogueTurnConflict        = errors.New("沟通轮事实冲突")
	ErrDialogueTurnNotFound        = errors.New("沟通轮不存在")
	ErrDialogueTurnState           = errors.New("沟通轮状态不允许当前操作")
	ErrDialogueTurnBinding         = errors.New("沟通轮冻结边界或档案绑定已变化")
	ErrDialogueTurnBudget          = errors.New("当月自动沟通轮预算已用尽")
	ErrAIInvocationInvalid         = errors.New("AI 调用事实无效")
	ErrAIInvocationConflict        = errors.New("AI 调用事实冲突")
	ErrAIInvocationNotFound        = errors.New("AI 调用事实不存在")
	ErrAIInvocationBudget          = errors.New("当日 provider 调用预算已用尽")
	ErrCommunicationActionInvalid  = errors.New("沟通动作事实无效")
	ErrCommunicationActionConflict = errors.New("沟通动作事实冲突")
)

const (
	m5DailyProviderCallLimit = int64(20)
	m5MonthlyTurnLimit       = int64(100)
)

type FreezeDialogueTurnRequest struct {
	TurnID              string
	ProfileID           string
	ConversationRef     string
	InputDigest         string
	HistoryThroughSeq   int64
	InboundFromSeq      int64
	InboundThroughSeq   int64
	ContextRevisionHash string
	ResumeSnapshotID    string
	RecommendedTimeText string
	RenderFormatVersion string
	FrozenAt            time.Time
}

type FreezeDialogueTurnResult struct {
	Turn    DialogueTurn
	Created bool
}

type MarkProfileCommunicatingRequest struct {
	ProfileID       string
	ConversationRef string
	MessageSeq      int64
	ObservedAt      time.Time
}

type MarkProfileCommunicatingResult struct {
	Profile CandidateProfile
	Changed bool
}

// MarkProfileCommunicating 只确认正式活动账本里的候选人真实消息。媒体消息同样
// 推进主状态，但不因此创建半成品 turn、调用 AI 或产生动作。
func (s *Store) MarkProfileCommunicating(req MarkProfileCommunicatingRequest) (*MarkProfileCommunicatingResult, error) {
	if strings.TrimSpace(req.ProfileID) == "" || strings.TrimSpace(req.ConversationRef) == "" || req.MessageSeq <= 0 {
		return nil, ErrDialogueTurnInvalid
	}
	if req.ObservedAt.IsZero() {
		req.ObservedAt = time.Now()
	}
	out := &MarkProfileCommunicatingResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&out.Profile, "profile_id = ?", req.ProfileID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCandidateProfileNotFound
			}
			return err
		}
		profile := out.Profile
		if profile.ConversationRef == nil || *profile.ConversationRef != req.ConversationRef || profile.EndReason != nil {
			return ErrDialogueTurnBinding
		}
		if profile.MainStatus == CandidateProfileCommunicating {
			if profile.CommunicatingAt == nil || profile.FirstRealMessageSeq == nil {
				return ErrDialogueTurnConflict
			}
			return nil
		}
		if profile.MainStatus != CandidateProfileGreeted {
			return ErrDialogueTurnState
		}
		var selection M5TrialSelection
		if err := tx.First(&selection, "profile_id = ? AND status = ? AND active_slot = ?",
			req.ProfileID, M5TrialSelectionActive, m5TrialActiveSlot).Error; err != nil {
			return ErrDialogueTurnBinding
		}
		key := ConversationKey{Platform: profile.Platform, AccountRef: profile.AccountRef, ConversationRef: req.ConversationRef}
		var conversation Conversation
		if err := tx.Where(conversationWhere(key), conversationArgs(key)...).First(&conversation).Error; err != nil ||
			conversation.TrackingState != TrackingAdopted {
			return ErrDialogueTurnBinding
		}
		var tracked TrackedIntent
		if err := tx.Where(conversationWhere(key), conversationArgs(key)...).First(&tracked).Error; err != nil ||
			tracked.Status != TrackingAdopted {
			return ErrDialogueTurnBinding
		}
		var message Message
		if err := tx.First(&message,
			"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ? AND retracted_at IS NULL",
			profile.Platform, profile.AccountRef, req.ConversationRef, req.MessageSeq).Error; err != nil {
			return ErrDialogueTurnBinding
		}
		if !IsM5RealCandidateMessage(message) {
			return ErrDialogueTurnBinding
		}
		communicatingAt := message.CreatedAt
		if communicatingAt.IsZero() {
			communicatingAt = req.ObservedAt
		}
		updated := tx.Model(&CandidateProfile{}).
			Where("profile_id = ? AND main_status = ? AND communicating_at IS NULL AND first_real_message_seq IS NULL",
				req.ProfileID, CandidateProfileGreeted).
			Updates(map[string]any{
				"main_status": CandidateProfileCommunicating, "communicating_at": communicatingAt,
				"first_real_message_seq": req.MessageSeq,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrDialogueTurnConflict
		}
		if err := tx.First(&out.Profile, "profile_id = ?", req.ProfileID).Error; err != nil {
			return err
		}
		out.Changed = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// FreezeDialogueTurn 在同一事务中重验活动消息尾、简历与职位 revision 绑定，
// 冻结唯一 turn，并在首次真实候选人事实到达时推进 greeted→communicating。
func (s *Store) FreezeDialogueTurn(req FreezeDialogueTurnRequest) (*FreezeDialogueTurnResult, error) {
	if strings.TrimSpace(req.TurnID) == "" || strings.TrimSpace(req.ProfileID) == "" ||
		strings.TrimSpace(req.ConversationRef) == "" || strings.TrimSpace(req.InputDigest) == "" ||
		strings.TrimSpace(req.ContextRevisionHash) == "" || strings.TrimSpace(req.ResumeSnapshotID) == "" ||
		strings.TrimSpace(req.RecommendedTimeText) == "" || strings.TrimSpace(req.RenderFormatVersion) == "" ||
		req.HistoryThroughSeq < 0 || req.InboundFromSeq <= req.HistoryThroughSeq ||
		req.InboundThroughSeq < req.InboundFromSeq {
		return nil, ErrDialogueTurnInvalid
	}
	if req.FrozenAt.IsZero() {
		req.FrozenAt = time.Now()
	}
	wanted := DialogueTurn{
		TurnID: req.TurnID, ProfileID: req.ProfileID, ConversationRef: req.ConversationRef,
		InputDigest: req.InputDigest, HistoryThroughSeq: req.HistoryThroughSeq,
		InboundFromSeq: req.InboundFromSeq, InboundThroughSeq: req.InboundThroughSeq,
		ContextRevisionHash: req.ContextRevisionHash, ResumeSnapshotID: req.ResumeSnapshotID,
		RecommendedTimeText: req.RecommendedTimeText, RenderFormatVersion: req.RenderFormatVersion,
		Status: DialogueTurnCollected, CreatedAt: req.FrozenAt, UpdatedAt: req.FrozenAt,
	}
	out := &FreezeDialogueTurnResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if existing, found, err := dialogueTurnByIdentityTx(tx, req.TurnID, req.ProfileID, req.InputDigest); err != nil {
			return err
		} else if found {
			if !sameFrozenDialogueTurn(existing, wanted) {
				return ErrDialogueTurnConflict
			}
			out.Turn = existing
			return nil
		}

		var profile CandidateProfile
		if err := tx.First(&profile, "profile_id = ?", req.ProfileID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCandidateProfileNotFound
			}
			return err
		}
		if profile.ConversationRef == nil || *profile.ConversationRef != req.ConversationRef ||
			profile.EndReason != nil || (profile.MainStatus != CandidateProfileGreeted && profile.MainStatus != CandidateProfileCommunicating) ||
			profile.ActiveResumeSnapshotID == nil || *profile.ActiveResumeSnapshotID != req.ResumeSnapshotID {
			return ErrDialogueTurnBinding
		}
		var selection M5TrialSelection
		if err := tx.First(&selection, "profile_id = ? AND status = ? AND active_slot = ?",
			req.ProfileID, M5TrialSelectionActive, m5TrialActiveSlot).Error; err != nil {
			return ErrDialogueTurnBinding
		}
		currentRevision, currentReady, err := currentCommunicationJobAIContextTx(tx, profile)
		if err != nil {
			return err
		}
		if !currentReady || currentRevision.RevisionHash != req.ContextRevisionHash {
			return ErrDialogueTurnBinding
		}
		var snapshot CandidateResumeSnapshot
		if err := tx.First(&snapshot, "snapshot_id = ?", req.ResumeSnapshotID).Error; err != nil ||
			snapshot.ProfileID != req.ProfileID || snapshot.SourceConversationRef != req.ConversationRef {
			return ErrDialogueTurnBinding
		}
		key := ConversationKey{Platform: profile.Platform, AccountRef: profile.AccountRef, ConversationRef: req.ConversationRef}
		var conversation Conversation
		if err := tx.Where(conversationWhere(key), conversationArgs(key)...).First(&conversation).Error; err != nil ||
			conversation.TrackingState != TrackingAdopted {
			return ErrDialogueTurnBinding
		}
		var tracked TrackedIntent
		if err := tx.Where(conversationWhere(key), conversationArgs(key)...).First(&tracked).Error; err != nil ||
			tracked.Status != TrackingAdopted {
			return ErrDialogueTurnBinding
		}

		var tail int64
		if err := tx.Model(&Message{}).
			Where("platform = ? AND account_ref = ? AND conversation_ref = ? AND retracted_at IS NULL",
				profile.Platform, profile.AccountRef, req.ConversationRef).
			Select("COALESCE(MAX(seq), 0)").Scan(&tail).Error; err != nil || tail != req.InboundThroughSeq {
			return ErrDialogueTurnBinding
		}
		var outboundTail int64
		if err := tx.Model(&Message{}).
			Where("platform = ? AND account_ref = ? AND conversation_ref = ? AND retracted_at IS NULL AND direction = ?",
				profile.Platform, profile.AccountRef, req.ConversationRef, "out").
			Select("COALESCE(MAX(seq), 0)").Scan(&outboundTail).Error; err != nil || outboundTail != req.HistoryThroughSeq {
			return ErrDialogueTurnBinding
		}
		var lastOutbound Message
		if err := tx.First(&lastOutbound,
			"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ? AND retracted_at IS NULL",
			profile.Platform, profile.AccountRef, req.ConversationRef, req.HistoryThroughSeq).Error; err != nil ||
			lastOutbound.Direction != "out" {
			return ErrDialogueTurnBinding
		}
		var inbound []Message
		if err := tx.Where(
			"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq > ? AND seq <= ? AND retracted_at IS NULL",
			profile.Platform, profile.AccountRef, req.ConversationRef, req.HistoryThroughSeq, req.InboundThroughSeq,
		).Order("seq").Find(&inbound).Error; err != nil {
			return err
		}
		if len(inbound) == 0 || inbound[0].Seq != req.InboundFromSeq || inbound[len(inbound)-1].Seq != req.InboundThroughSeq {
			return ErrDialogueTurnBinding
		}
		if _, ok := DialogueTurnInputKindOf(inbound); !ok {
			return ErrDialogueTurnBinding
		}
		if digest, _, err := DialogueTurnIdentity(req.ProfileID, lastOutbound, inbound); err != nil ||
			digest != req.InputDigest {
			return ErrDialogueTurnBinding
		}
		monthStart, nextMonth := localMonthBounds(req.FrozenAt)
		var monthlyTurns int64
		if err := tx.Model(&DialogueTurn{}).
			Where("created_at >= ? AND created_at < ?", monthStart, nextMonth).
			Count(&monthlyTurns).Error; err != nil {
			return err
		}
		if monthlyTurns >= m5MonthlyTurnLimit {
			return ErrDialogueTurnBudget
		}

		if err := tx.Create(&wanted).Error; err != nil {
			return err
		}
		if profile.MainStatus == CandidateProfileGreeted {
			communicatingAt := inbound[0].CreatedAt
			if communicatingAt.IsZero() {
				communicatingAt = req.FrozenAt
			}
			updated := tx.Model(&CandidateProfile{}).
				Where("profile_id = ? AND main_status = ? AND communicating_at IS NULL AND first_real_message_seq IS NULL",
					req.ProfileID, CandidateProfileGreeted).
				Updates(map[string]any{
					"main_status": CandidateProfileCommunicating, "communicating_at": communicatingAt,
					"first_real_message_seq": inbound[0].Seq,
				})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrDialogueTurnConflict
			}
		} else if profile.CommunicatingAt == nil || profile.FirstRealMessageSeq == nil {
			return ErrDialogueTurnConflict
		}
		out.Turn = wanted
		out.Created = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func dialogueTurnByIdentityTx(tx *gorm.DB, turnID, profileID, inputDigest string) (DialogueTurn, bool, error) {
	var byID DialogueTurn
	err := tx.First(&byID, "turn_id = ?", turnID).Error
	if err == nil {
		return byID, true, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return DialogueTurn{}, false, err
	}
	var byDigest DialogueTurn
	err = tx.First(&byDigest, "profile_id = ? AND input_digest = ?", profileID, inputDigest).Error
	if err == nil {
		return byDigest, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return DialogueTurn{}, false, nil
	}
	return DialogueTurn{}, false, err
}

func sameFrozenDialogueTurn(existing, wanted DialogueTurn) bool {
	return existing.ProfileID == wanted.ProfileID && existing.ConversationRef == wanted.ConversationRef &&
		existing.InputDigest == wanted.InputDigest && existing.HistoryThroughSeq == wanted.HistoryThroughSeq &&
		existing.InboundFromSeq == wanted.InboundFromSeq && existing.InboundThroughSeq == wanted.InboundThroughSeq &&
		existing.ContextRevisionHash == wanted.ContextRevisionHash && existing.ResumeSnapshotID == wanted.ResumeSnapshotID &&
		existing.RecommendedTimeText == wanted.RecommendedTimeText && existing.RenderFormatVersion == wanted.RenderFormatVersion
}

// validateDialogueTurnCurrentTx 在每个 AI 边界重验冻结引用仍指向当前活动事实。
// 它不比较正文，只比较脑内不可变引用、活动消息边界和后台职位归属。
func validateDialogueTurnCurrentTx(tx *gorm.DB, turn DialogueTurn) error {
	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", turn.ProfileID).Error; err != nil {
		return err
	}
	application, v4Turn, err := communicationV4TurnHeadApplicationTx(tx, turn)
	if err != nil {
		return err
	}
	if v4Turn {
		aggregate, err := communicationV4AggregateTx(tx, turn.ProfileID)
		if err != nil {
			return err
		}
		if aggregate.AutomationStatus != ProfileCommunicationAutomationActive ||
			aggregate.Revision != application.ToRevision ||
			aggregate.ProjectedThroughSeq != turn.InboundThroughSeq {
			return ErrDialogueTurnBinding
		}
	}
	if profile.ConversationRef == nil || *profile.ConversationRef != turn.ConversationRef ||
		profile.ActiveResumeSnapshotID == nil || *profile.ActiveResumeSnapshotID != turn.ResumeSnapshotID ||
		(!v4Turn && (profile.MainStatus != CandidateProfileCommunicating ||
			profile.EndReason != nil || profile.CommunicatingAt == nil ||
			profile.FirstRealMessageSeq == nil)) {
		return ErrDialogueTurnBinding
	}
	if !v4Turn {
		var selection M5TrialSelection
		if err := tx.First(&selection, "profile_id = ? AND status = ? AND active_slot = ?",
			turn.ProfileID, M5TrialSelectionActive, m5TrialActiveSlot).Error; err != nil {
			return ErrDialogueTurnBinding
		}
	}
	if _, err := frozenCommunicationJobAIContextTx(
		tx,
		profile,
		turn.ContextRevisionHash,
	); err != nil {
		return ErrDialogueTurnBinding
	}
	var snapshot CandidateResumeSnapshot
	if err := tx.First(&snapshot, "snapshot_id = ?", turn.ResumeSnapshotID).Error; err != nil ||
		snapshot.ProfileID != turn.ProfileID || snapshot.SourceConversationRef != turn.ConversationRef {
		return ErrDialogueTurnBinding
	}
	key := ConversationKey{Platform: profile.Platform, AccountRef: profile.AccountRef, ConversationRef: turn.ConversationRef}
	var conversation Conversation
	if err := tx.Where(conversationWhere(key), conversationArgs(key)...).First(&conversation).Error; err != nil ||
		conversation.TrackingState != TrackingAdopted {
		return ErrDialogueTurnBinding
	}
	var tracked TrackedIntent
	if err := tx.Where(conversationWhere(key), conversationArgs(key)...).First(&tracked).Error; err != nil ||
		tracked.Status != TrackingAdopted {
		return ErrDialogueTurnBinding
	}
	var activeTail, outboundTail int64
	base := tx.Model(&Message{}).Where(
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND retracted_at IS NULL",
		profile.Platform, profile.AccountRef, turn.ConversationRef,
	)
	if err := base.Select("COALESCE(MAX(seq), 0)").Scan(&activeTail).Error; err != nil || activeTail != turn.InboundThroughSeq {
		return ErrDialogueTurnBinding
	}
	if err := base.Where("direction = ?", "out").Select("COALESCE(MAX(seq), 0)").Scan(&outboundTail).Error; err != nil ||
		outboundTail != turn.HistoryThroughSeq {
		return ErrDialogueTurnBinding
	}
	var lastOutbound Message
	if err := tx.First(&lastOutbound,
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ? AND retracted_at IS NULL",
		profile.Platform, profile.AccountRef, turn.ConversationRef, turn.HistoryThroughSeq).Error; err != nil ||
		lastOutbound.Direction != "out" {
		return ErrDialogueTurnBinding
	}
	var boundary []Message
	if err := tx.Where(
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq > ? AND seq <= ? AND retracted_at IS NULL",
		profile.Platform, profile.AccountRef, turn.ConversationRef, turn.HistoryThroughSeq, turn.InboundThroughSeq,
	).Order("seq").Find(&boundary).Error; err != nil {
		return err
	}
	if len(boundary) == 0 || boundary[len(boundary)-1].Seq != turn.InboundThroughSeq {
		return ErrDialogueTurnBinding
	}
	inbound := boundary
	if v4Turn {
		var valid bool
		inbound, valid = DialogueTurnCandidateMessages(boundary)
		if !valid {
			return ErrDialogueTurnBinding
		}
	}
	if len(inbound) == 0 || inbound[0].Seq != turn.InboundFromSeq ||
		(!v4Turn && inbound[len(inbound)-1].Seq != turn.InboundThroughSeq) {
		return ErrDialogueTurnBinding
	}
	if !v4Turn {
		if _, ok := DialogueTurnInputKindOf(inbound); !ok {
			return ErrDialogueTurnBinding
		}
	}
	if digest, _, err := DialogueTurnIdentity(turn.ProfileID, lastOutbound, inbound); err != nil ||
		digest != turn.InputDigest {
		return ErrDialogueTurnBinding
	}
	return nil
}

func validateDialogueTurnAIAdviceTx(
	tx *gorm.DB,
	turn DialogueTurn,
	purpose m5ai.CompletionPurpose,
) error {
	application, v4Turn, err := communicationV4TurnHeadApplicationTx(tx, turn)
	if err != nil {
		return err
	}
	if v4Turn {
		if !communicationV4OutcomeAuthorizesAdvice(application.Outcome, purpose) {
			return ErrDialogueTurnState
		}
	}
	return validateDialogueTurnCurrentTx(tx, turn)
}

func markDialogueOwnerManualTx(tx *gorm.DB, turn DialogueTurn, reason string, at time.Time) error {
	_, v4Turn, err := communicationV4TurnApplicationTx(tx, turn)
	if err != nil {
		return err
	}
	if v4Turn {
		return markCommunicationV4AutomationManualTx(tx, turn.ProfileID, reason, at)
	}
	return markM5TrialManualRequiredTx(tx, turn.ProfileID, reason, at)
}

func markM5TrialManualRequiredTx(tx *gorm.DB, profileID, reason string, at time.Time) error {
	updated := tx.Model(&M5TrialSelection{}).
		Where("profile_id = ? AND status = ? AND active_slot = ?", profileID, M5TrialSelectionActive, m5TrialActiveSlot).
		Updates(map[string]any{
			"status": M5TrialSelectionManualRequired, "active_slot": nil,
			"reason": reason, "ended_at": at,
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected == 1 {
		return nil
	}
	var existing int64
	if err := tx.Model(&M5TrialSelection{}).
		Where("profile_id = ? AND status = ?", profileID, M5TrialSelectionManualRequired).
		Count(&existing).Error; err != nil {
		return err
	}
	if existing == 0 {
		return ErrDialogueTurnBinding
	}
	return nil
}

func markDialogueTurnManualTx(tx *gorm.DB, turn *DialogueTurn, reason string, at time.Time) error {
	if turn.Status == DialogueTurnManualRequired {
		if turn.FailureReason != reason {
			return ErrDialogueTurnConflict
		}
		return markDialogueOwnerManualTx(tx, *turn, reason, at)
	}
	switch turn.Status {
	case DialogueTurnCollected, DialogueTurnClassified, DialogueTurnAdviceReady:
	default:
		return ErrDialogueTurnState
	}
	if err := tx.Model(&CommunicationAction{}).
		Where("turn_id = ? AND status = ?", turn.TurnID, CommunicationActionPlanned).
		Updates(map[string]any{
			"status": CommunicationActionManualRequired, "failure_reason": reason, "updated_at": at,
		}).Error; err != nil {
		return err
	}
	updated := tx.Model(&DialogueTurn{}).
		Where("turn_id = ? AND status = ?", turn.TurnID, turn.Status).
		Updates(map[string]any{"status": DialogueTurnManualRequired, "failure_reason": reason, "updated_at": at})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrDialogueTurnConflict
	}
	if err := markDialogueOwnerManualTx(tx, *turn, reason, at); err != nil {
		return err
	}
	return tx.First(turn, "turn_id = ?", turn.TurnID).Error
}

func (s *Store) DialogueTurnByID(turnID string) (*DialogueTurn, error) {
	var turn DialogueTurn
	err := s.db.First(&turn, "turn_id = ?", turnID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &turn, nil
}

func (s *Store) LatestDialogueTurnForProfile(profileID string) (*DialogueTurn, error) {
	if strings.TrimSpace(profileID) == "" {
		return nil, ErrDialogueTurnInvalid
	}
	var turn DialogueTurn
	err := s.db.Where("profile_id = ?", profileID).
		Order("inbound_through_seq DESC, turn_id DESC").First(&turn).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &turn, nil
}

func (s *Store) CandidateResumeSnapshotByID(profileID, snapshotID string) (*CandidateResumeSnapshot, error) {
	if strings.TrimSpace(profileID) == "" || strings.TrimSpace(snapshotID) == "" {
		return nil, ErrDialogueTurnInvalid
	}
	var snapshot CandidateResumeSnapshot
	err := s.db.First(&snapshot, "profile_id = ? AND snapshot_id = ?", profileID, snapshotID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

type CodeClassificationRequest struct {
	TurnID       string
	Label        m5ai.IntentLabel
	ClassifiedAt time.Time
}

func (s *Store) ApplyCodeClassification(req CodeClassificationRequest) (*DialogueTurn, error) {
	if strings.TrimSpace(req.TurnID) == "" || !validIntentLabel(req.Label) {
		return nil, ErrDialogueTurnInvalid
	}
	if req.ClassifiedAt.IsZero() {
		req.ClassifiedAt = time.Now()
	}
	var out DialogueTurn
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&out, "turn_id = ?", req.TurnID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrDialogueTurnNotFound
			}
			return err
		}
		wantedStatus := DialogueTurnClassified
		failureReason := ""
		if req.Label == m5ai.IntentRejected {
			wantedStatus = DialogueTurnManualRequired
			failureReason = "intentRejected"
		}
		if out.Status != DialogueTurnCollected {
			if out.Status == wantedStatus && out.IntentLabel == req.Label &&
				out.IntentSource == DialogueIntentCodeShortCircuit {
				return nil
			}
			return ErrDialogueTurnState
		}
		updated := tx.Model(&DialogueTurn{}).
			Where("turn_id = ? AND status = ?", req.TurnID, DialogueTurnCollected).
			Updates(map[string]any{
				"status": wantedStatus, "intent_label": req.Label,
				"intent_source": DialogueIntentCodeShortCircuit, "classified_at": req.ClassifiedAt,
				"failure_reason": failureReason,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrDialogueTurnConflict
		}
		if wantedStatus == DialogueTurnManualRequired {
			if err := markDialogueOwnerManualTx(tx, out, failureReason, req.ClassifiedAt); err != nil {
				return err
			}
		}
		return tx.First(&out, "turn_id = ?", req.TurnID).Error
	})
	return &out, err
}

// ApplyResumeBusinessClassification is the narrow transactional bridge from
// the v4 resumeSubmitted business event to M5's persisted turn lifecycle. It
// accepts no caller-provided label or source: a current, single resume-card
// boundary can only become interested/businessEvent, and no AI invocation is
// involved in that transition.
func (s *Store) ApplyResumeBusinessClassification(turnID string, classifiedAt time.Time) (*DialogueTurn, error) {
	if strings.TrimSpace(turnID) == "" {
		return nil, ErrDialogueTurnInvalid
	}
	if classifiedAt.IsZero() {
		classifiedAt = time.Now()
	}
	var out DialogueTurn
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&out, "turn_id = ?", turnID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrDialogueTurnNotFound
			}
			return err
		}
		if err := validateDialogueTurnCurrentTx(tx, out); err != nil {
			if !errors.Is(err, ErrDialogueTurnBinding) {
				return err
			}
			return markDialogueTurnManualTx(tx, &out, "inputBoundaryChanged", classifiedAt)
		}
		var profile CandidateProfile
		if err := tx.First(&profile, "profile_id = ?", out.ProfileID).Error; err != nil {
			return err
		}
		var inbound []Message
		if err := tx.Where(
			"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq >= ? AND seq <= ? AND retracted_at IS NULL",
			profile.Platform, profile.AccountRef, out.ConversationRef, out.InboundFromSeq, out.InboundThroughSeq,
		).Order("seq").Find(&inbound).Error; err != nil {
			return err
		}
		if candidate, ok := DialogueTurnCandidateMessages(inbound); ok {
			inbound = candidate
		}
		kind, ok := DialogueTurnInputKindOf(inbound)
		if !ok || kind != DialogueTurnInputResumeAttachment {
			return ErrDialogueTurnBinding
		}
		if out.Status != DialogueTurnCollected {
			if out.Status == DialogueTurnClassified && out.IntentLabel == m5ai.IntentInterested &&
				out.IntentSource == DialogueIntentBusinessEvent {
				return nil
			}
			return ErrDialogueTurnState
		}
		updated := tx.Model(&DialogueTurn{}).
			Where("turn_id = ? AND status = ?", out.TurnID, DialogueTurnCollected).
			Updates(map[string]any{
				"status": DialogueTurnClassified, "intent_label": m5ai.IntentInterested,
				"intent_source": DialogueIntentBusinessEvent, "classified_at": classifiedAt,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrDialogueTurnConflict
		}
		return tx.First(&out, "turn_id = ?", out.TurnID).Error
	})
	return &out, err
}

type ReserveAIInvocationRequest struct {
	InvocationID string
	TurnID       string
	Purpose      m5ai.CompletionPurpose
	Attempt      int
	Provider     string
	Model        string
	InputHash    string
	CreatedAt    time.Time
}

type ReserveAIInvocationResult struct {
	Invocation AIInvocation
	Created    bool
}

// ReserveAIInvocation 是 provider 调用的唯一授权点。Created=false 只表示
// 既有事实可收编，绝不授权重放网络调用。
func (s *Store) ReserveAIInvocation(req ReserveAIInvocationRequest) (*ReserveAIInvocationResult, error) {
	if strings.TrimSpace(req.InvocationID) == "" || strings.TrimSpace(req.TurnID) == "" ||
		(req.Purpose != m5ai.PurposeIntent && req.Purpose != m5ai.PurposeReply) || req.Attempt != 1 ||
		strings.TrimSpace(req.Provider) == "" || strings.TrimSpace(req.Model) == "" || strings.TrimSpace(req.InputHash) == "" {
		return nil, ErrAIInvocationInvalid
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now()
	}
	out := &ReserveAIInvocationResult{}
	var boundaryChanged bool
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var turn DialogueTurn
		if err := tx.First(&turn, "turn_id = ?", req.TurnID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrDialogueTurnNotFound
			}
			return err
		}
		wanted := AIInvocation{
			InvocationID: req.InvocationID, TurnID: req.TurnID, Purpose: req.Purpose, Attempt: req.Attempt,
			Provider: req.Provider, Model: req.Model, ContextRevisionHash: turn.ContextRevisionHash,
			InputHash: req.InputHash, Status: AIInvocationTransportFailed, CreatedAt: req.CreatedAt,
		}
		var existing AIInvocation
		err := tx.First(&existing, "invocation_id = ?", req.InvocationID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = tx.First(&existing, "turn_id = ? AND purpose = ? AND attempt = ?", req.TurnID, req.Purpose, req.Attempt).Error
		}
		if err == nil {
			if !sameInvocationReservation(existing, wanted) {
				return ErrAIInvocationConflict
			}
			if err := validateDialogueTurnAIAdviceTx(tx, turn, req.Purpose); err != nil {
				if !errors.Is(err, ErrDialogueTurnBinding) {
					return err
				}
				if err := markDialogueTurnManualTx(tx, &turn, "inputBoundaryChanged", req.CreatedAt); err != nil {
					return err
				}
				boundaryChanged = true
			}
			out.Invocation = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if (req.Purpose == m5ai.PurposeIntent && turn.Status != DialogueTurnCollected) ||
			(req.Purpose == m5ai.PurposeReply && (turn.Status != DialogueTurnClassified || turn.IntentLabel == m5ai.IntentRejected)) {
			return ErrDialogueTurnState
		}
		if err := validateDialogueTurnAIAdviceTx(tx, turn, req.Purpose); err != nil {
			if !errors.Is(err, ErrDialogueTurnBinding) {
				return err
			}
			if err := markDialogueTurnManualTx(tx, &turn, "inputBoundaryChanged", req.CreatedAt); err != nil {
				return err
			}
			boundaryChanged = true
			return nil
		}
		dayStart, nextDay := localDayBounds(req.CreatedAt)
		var dialogueCalls, scheduleCalls int64
		if err := tx.Model(&AIInvocation{}).
			Where("created_at >= ? AND created_at < ?", dayStart, nextDay).
			Count(&dialogueCalls).Error; err != nil {
			return err
		}
		if err := tx.Model(&CommunicationV4ScheduleAIInvocation{}).
			Where("created_at >= ? AND created_at < ?", dayStart, nextDay).
			Count(&scheduleCalls).Error; err != nil {
			return err
		}
		if dialogueCalls+scheduleCalls >= m5DailyProviderCallLimit {
			return ErrAIInvocationBudget
		}
		if err := tx.Create(&wanted).Error; err != nil {
			return err
		}
		out.Invocation = wanted
		out.Created = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if boundaryChanged {
		return nil, ErrDialogueTurnBinding
	}
	return out, nil
}

func localDayBounds(at time.Time) (time.Time, time.Time) {
	local := at.In(at.Location())
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	return start, start.AddDate(0, 0, 1)
}

func localMonthBounds(at time.Time) (time.Time, time.Time) {
	local := at.In(at.Location())
	start := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, local.Location())
	return start, start.AddDate(0, 1, 0)
}

func sameInvocationReservation(existing, wanted AIInvocation) bool {
	return existing.TurnID == wanted.TurnID && existing.Purpose == wanted.Purpose &&
		existing.Attempt == wanted.Attempt && existing.Provider == wanted.Provider &&
		existing.Model == wanted.Model && existing.ContextRevisionHash == wanted.ContextRevisionHash &&
		existing.InputHash == wanted.InputHash
}

type AIInvocationCompletion struct {
	InvocationID          string
	Status                AIInvocationStatus
	OutputHash            string
	InputTokens           int
	CachedInputTokens     int
	OutputTokens          int
	ReasoningTokens       *int
	UsageShape            AIInvocationUsageShape
	ReasoningContentEmpty bool
	LatencyMs             int64
	ErrorClass            string
	FailureStage          string
	ErrorDetailCode       string
	ProviderHTTPStatus    *int
	RequestBytes          int
	ResponseBytes         int
	TraceStatus           m5ai.TraceStatus
	EstimatedCostMicros   int64
	FinishedAt            time.Time
}

type CompleteIntentInvocationRequest struct {
	Completion   AIInvocationCompletion
	Label        m5ai.IntentLabel
	Source       DialogueIntentSource
	ManualReason string
}

// CompleteIntentInvocation 以 invocation 未完成预留事实的 CAS 为锁，同事务写入终局
// 计量与唯一分类；输入边界失效时可只收编 invocation 并把 turn 转人工。
func (s *Store) CompleteIntentInvocation(req CompleteIntentInvocationRequest) (*DialogueTurn, error) {
	if err := validateInvocationCompletion(req.Completion); err != nil {
		return nil, err
	}
	preserveRejectedClassification := req.ManualReason == "intentRejected"
	if preserveRejectedClassification && (req.Completion.Status != AIInvocationOK ||
		req.Label != m5ai.IntentRejected || req.Source != DialogueIntentLLM) {
		return nil, ErrAIInvocationInvalid
	}
	if req.ManualReason == "" {
		if req.Completion.Status == AIInvocationOK {
			if !validIntentLabel(req.Label) || req.Source != DialogueIntentLLM {
				return nil, ErrAIInvocationInvalid
			}
		} else if req.Label != m5ai.IntentNeutral || req.Source != DialogueIntentLLMFailure {
			return nil, ErrAIInvocationInvalid
		}
	}
	var out DialogueTurn
	err := s.db.Transaction(func(tx *gorm.DB) error {
		invocation, err := finishAIInvocationTx(tx, req.Completion, m5ai.PurposeIntent)
		if err != nil {
			return err
		}
		if err := tx.First(&out, "turn_id = ?", invocation.TurnID).Error; err != nil {
			return err
		}
		if _, v4Turn, err := communicationV4TurnApplicationTx(tx, out); err != nil {
			return err
		} else if v4Turn {
			label, source, manualReason := req.Label, req.Source, req.ManualReason
			if req.Completion.Status == AIInvocationOK && !reasoningCompletionSafe(req.Completion) {
				label, source, manualReason = "", "", "reasoningUsageUnsafe"
			}
			err := completeCommunicationV4IntentTx(
				tx,
				&out,
				invocation,
				label,
				source,
				manualReason,
				req.Completion.FinishedAt.UTC(),
			)
			return settleCommunicationV4AdviceErrorTx(
				tx,
				&out,
				err,
				req.Completion.FinishedAt.UTC(),
			)
		}
		if out.Status != DialogueTurnManualRequired {
			if err := validateDialogueTurnAIAdviceTx(tx, out, m5ai.PurposeIntent); err != nil {
				if !errors.Is(err, ErrDialogueTurnBinding) {
					return err
				}
				return markDialogueTurnManualTx(tx, &out, "inputBoundaryChanged", req.Completion.FinishedAt)
			}
		}
		wantedStatus := DialogueTurnClassified
		label, source, reason := req.Label, req.Source, ""
		if req.ManualReason != "" {
			wantedStatus, reason = DialogueTurnManualRequired, req.ManualReason
			if !preserveRejectedClassification {
				label, source = "", ""
			}
		} else if req.Completion.Status == AIInvocationOK && !reasoningCompletionSafe(req.Completion) {
			wantedStatus, label, source, reason = DialogueTurnManualRequired, "", "", "reasoningUsageUnsafe"
		}
		if out.Status != DialogueTurnCollected {
			if out.Status == wantedStatus && out.IntentLabel == label && out.IntentSource == source && out.FailureReason == reason {
				return nil
			}
			return ErrDialogueTurnState
		}
		if wantedStatus == DialogueTurnManualRequired && preserveRejectedClassification {
			updated := tx.Model(&DialogueTurn{}).
				Where("turn_id = ? AND status = ?", out.TurnID, DialogueTurnCollected).
				Updates(map[string]any{
					"status": DialogueTurnManualRequired, "intent_label": req.Label,
					"intent_source": req.Source, "classified_at": req.Completion.FinishedAt,
					"failure_reason": req.ManualReason, "updated_at": req.Completion.FinishedAt,
				})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrDialogueTurnConflict
			}
			if err := markDialogueOwnerManualTx(tx, out, req.ManualReason, req.Completion.FinishedAt); err != nil {
				return err
			}
			return tx.First(&out, "turn_id = ?", out.TurnID).Error
		}
		if wantedStatus == DialogueTurnManualRequired {
			return markDialogueTurnManualTx(tx, &out, reason, req.Completion.FinishedAt)
		}
		updated := tx.Model(&DialogueTurn{}).
			Where("turn_id = ? AND status = ?", out.TurnID, DialogueTurnCollected).
			Updates(map[string]any{
				"status": wantedStatus, "intent_label": label, "intent_source": source,
				"classified_at": req.Completion.FinishedAt, "failure_reason": reason,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrDialogueTurnConflict
		}
		return tx.First(&out, "turn_id = ?", out.TurnID).Error
	})
	return &out, err
}

type CompleteReplyInvocationRequest struct {
	Completion   AIInvocationCompletion
	ActionID     string
	Phrases      []string
	Text         string
	Action       m5ai.ReplyAction
	MeetingTime  string
	ContentHash  string
	ManualReason string
	PlannedAt    time.Time
}

// CompleteReplyInvocation 在一个事务内终结 reply invocation，并且只在
// reasoning 用量通过非思考闸时创建唯一 planned action；否则显式转人工。
func (s *Store) CompleteReplyInvocation(req CompleteReplyInvocationRequest) (*CommunicationAction, error) {
	if err := validateInvocationCompletion(req.Completion); err != nil {
		return nil, err
	}
	canPlan := req.Completion.Status == AIInvocationOK && req.ManualReason == "" &&
		reasoningCompletionSafe(req.Completion)
	if canPlan && (strings.TrimSpace(req.ActionID) == "" || strings.TrimSpace(req.Text) == "" ||
		strings.TrimSpace(req.ContentHash) == "") {
		return nil, ErrCommunicationActionInvalid
	}
	if req.PlannedAt.IsZero() {
		req.PlannedAt = req.Completion.FinishedAt
	}
	var out *CommunicationAction
	err := s.db.Transaction(func(tx *gorm.DB) error {
		invocation, err := finishAIInvocationTx(tx, req.Completion, m5ai.PurposeReply)
		if err != nil {
			return err
		}
		var turn DialogueTurn
		if err := tx.First(&turn, "turn_id = ?", invocation.TurnID).Error; err != nil {
			return err
		}
		if _, v4Turn, err := communicationV4TurnApplicationTx(tx, turn); err != nil {
			return err
		} else if v4Turn {
			manualReason := req.ManualReason
			if req.Completion.Status == AIInvocationOK && !reasoningCompletionSafe(req.Completion) {
				manualReason = "reasoningUsageUnsafe"
			}
			out, err = completeCommunicationV4ReplyTx(
				tx,
				&turn,
				invocation,
				m5ai.ReplySuggestion{
					Phrases:     append([]string(nil), req.Phrases...),
					Text:        req.Text,
					Action:      req.Action,
					MeetingTime: req.MeetingTime,
				},
				req.ContentHash,
				manualReason,
				req.PlannedAt.UTC(),
			)
			return settleCommunicationV4AdviceErrorTx(
				tx,
				&turn,
				err,
				req.Completion.FinishedAt.UTC(),
			)
		}
		if turn.Status != DialogueTurnManualRequired {
			if err := validateDialogueTurnAIAdviceTx(tx, turn, m5ai.PurposeReply); err != nil {
				if !errors.Is(err, ErrDialogueTurnBinding) {
					return err
				}
				return markDialogueTurnManualTx(tx, &turn, "inputBoundaryChanged", req.Completion.FinishedAt)
			}
		}
		if !canPlan {
			reason := req.ManualReason
			if reason == "" {
				if req.Completion.Status == AIInvocationOK {
					reason = "reasoningUsageUnsafe"
				} else {
					reason = "reply" + string(req.Completion.Status)
				}
			}
			return markDialogueTurnManualTx(tx, &turn, reason, req.Completion.FinishedAt)
		}
		if turn.Status != DialogueTurnClassified || turn.IntentLabel == m5ai.IntentRejected {
			if turn.Status == DialogueTurnAdviceReady {
				var existing CommunicationAction
				if err := tx.First(&existing, "turn_id = ? AND kind = ?", turn.TurnID, CommunicationActionReplyText).Error; err == nil &&
					sameCommunicationAction(existing, req) {
					out = &existing
					return nil
				}
			}
			return ErrDialogueTurnState
		}
		var active int64
		if err := tx.Table("communication_actions AS action").
			Joins("JOIN dialogue_turns AS turn ON turn.turn_id = action.turn_id").
			Where("turn.profile_id = ? AND action.status IN ?", turn.ProfileID,
				[]CommunicationActionStatus{CommunicationActionPlanned, CommunicationActionEffectPending}).
			Count(&active).Error; err != nil {
			return err
		}
		if active != 0 {
			return ErrCommunicationActionConflict
		}
		action := CommunicationAction{
			ActionID: req.ActionID, TurnID: turn.TurnID, Kind: CommunicationActionReplyText,
			Text: req.Text, ContentHash: req.ContentHash, Status: CommunicationActionPlanned,
			PlannedAt: req.PlannedAt, CreatedAt: req.PlannedAt, UpdatedAt: req.PlannedAt,
		}
		if err := tx.Create(&action).Error; err != nil {
			return err
		}
		updated := tx.Model(&DialogueTurn{}).
			Where("turn_id = ? AND status = ?", turn.TurnID, DialogueTurnClassified).
			Update("status", DialogueTurnAdviceReady)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrDialogueTurnConflict
		}
		out = &action
		return nil
	})
	return out, err
}

func reasoningCompletionSafe(completion AIInvocationCompletion) bool {
	if !completion.ReasoningContentEmpty {
		return false
	}
	if completion.UsageShape == AIInvocationUsageComplete {
		return completion.ReasoningTokens != nil && *completion.ReasoningTokens == 0
	}
	return completion.UsageShape == AIInvocationReasoningFieldAbsent && completion.ReasoningTokens == nil
}

func settleCommunicationV4AdviceErrorTx(
	tx *gorm.DB,
	turn *DialogueTurn,
	adviceErr error,
	at time.Time,
) error {
	if adviceErr == nil {
		return nil
	}
	if !errors.Is(adviceErr, ErrDialogueTurnBinding) {
		return adviceErr
	}
	if turn.Status == DialogueTurnManualRequired {
		return nil
	}
	return markDialogueTurnManualTx(tx, turn, "inputBoundaryChanged", at)
}

func sameCommunicationAction(existing CommunicationAction, req CompleteReplyInvocationRequest) bool {
	return existing.ActionID == req.ActionID && existing.Kind == CommunicationActionReplyText &&
		existing.Text == req.Text && existing.ContentHash == req.ContentHash
}

func validateInvocationCompletion(completion AIInvocationCompletion) error {
	if strings.TrimSpace(completion.InvocationID) == "" || completion.FinishedAt.IsZero() || completion.LatencyMs < 0 ||
		completion.InputTokens < 0 || completion.CachedInputTokens < 0 || completion.OutputTokens < 0 ||
		completion.EstimatedCostMicros < 0 || completion.RequestBytes < 0 || completion.ResponseBytes < 0 ||
		!validProviderHTTPStatus(completion.ProviderHTTPStatus) ||
		!validAIInvocationFailureStage(completion.FailureStage) ||
		!validAIInvocationErrorDetailCode(completion.ErrorDetailCode) ||
		!validAIInvocationTraceStatus(completion.TraceStatus) ||
		(completion.ReasoningTokens != nil && *completion.ReasoningTokens < 0) {
		return ErrAIInvocationInvalid
	}
	switch completion.Status {
	case AIInvocationOK, AIInvocationTransportFailed, AIInvocationProviderRejected,
		AIInvocationInvalidOutput, AIInvocationBudgetBlocked:
	default:
		return ErrAIInvocationInvalid
	}
	if completion.Status == AIInvocationOK {
		if strings.TrimSpace(completion.OutputHash) == "" || strings.TrimSpace(completion.ErrorClass) != "" ||
			(completion.UsageShape != AIInvocationUsageComplete && completion.UsageShape != AIInvocationReasoningFieldAbsent) ||
			(completion.UsageShape == AIInvocationUsageComplete && completion.ReasoningTokens == nil) ||
			(completion.UsageShape == AIInvocationReasoningFieldAbsent && completion.ReasoningTokens != nil) {
			return ErrAIInvocationInvalid
		}
	} else {
		if strings.TrimSpace(completion.ErrorClass) == "" {
			return ErrAIInvocationInvalid
		}
		switch completion.UsageShape {
		case "":
			if completion.ReasoningTokens != nil {
				return ErrAIInvocationInvalid
			}
		case AIInvocationUsageComplete:
			if completion.ReasoningTokens == nil {
				return ErrAIInvocationInvalid
			}
		case AIInvocationReasoningFieldAbsent:
			if completion.ReasoningTokens != nil {
				return ErrAIInvocationInvalid
			}
		default:
			return ErrAIInvocationInvalid
		}
	}
	return nil
}

const (
	AIInvocationFailureStageRequestBuild   = "requestBuild"
	AIInvocationFailureStageTransport      = "transport"
	AIInvocationFailureStageProviderHTTP   = "providerHTTP"
	AIInvocationFailureStageResponseDecode = "responseDecode"
	AIInvocationFailureStageBusinessParse  = "businessParse"
	AIInvocationFailureStageReducer        = "reducer"
	AIInvocationFailureStagePersistence    = "persistence"
	maxAIInvocationErrorDetailCodeBytes    = 128
)

func validProviderHTTPStatus(status *int) bool {
	return status == nil || (*status >= 100 && *status <= 599)
}

func validAIInvocationFailureStage(stage string) bool {
	switch stage {
	case "", AIInvocationFailureStageRequestBuild, AIInvocationFailureStageTransport,
		AIInvocationFailureStageProviderHTTP, AIInvocationFailureStageResponseDecode,
		AIInvocationFailureStageBusinessParse, AIInvocationFailureStageReducer,
		AIInvocationFailureStagePersistence:
		return true
	default:
		return false
	}
}

func validAIInvocationErrorDetailCode(code string) bool {
	if code == "" {
		return true
	}
	if len(code) > maxAIInvocationErrorDetailCodeBytes {
		return false
	}
	for _, char := range code {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func validAIInvocationTraceStatus(status m5ai.TraceStatus) bool {
	switch status {
	case "", m5ai.TraceStatusComplete, m5ai.TraceStatusUnavailable,
		m5ai.TraceStatusResponseUnavailable:
		return true
	default:
		return false
	}
}

func finishAIInvocationTx(tx *gorm.DB, completion AIInvocationCompletion, purpose m5ai.CompletionPurpose) (AIInvocation, error) {
	var invocation AIInvocation
	if err := tx.First(&invocation, "invocation_id = ?", completion.InvocationID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return AIInvocation{}, ErrAIInvocationNotFound
		}
		return AIInvocation{}, err
	}
	if invocation.Purpose != purpose {
		return AIInvocation{}, ErrAIInvocationConflict
	}
	if invocation.FinishedAt != nil {
		if sameInvocationCompletion(invocation, completion) {
			return invocation, nil
		}
		return AIInvocation{}, ErrAIInvocationConflict
	}
	updates := map[string]any{
		"status": completion.Status, "output_hash": completion.OutputHash,
		"input_tokens": completion.InputTokens, "cached_input_tokens": completion.CachedInputTokens,
		"output_tokens": completion.OutputTokens, "reasoning_tokens": completion.ReasoningTokens,
		"usage_shape": completion.UsageShape, "latency_ms": completion.LatencyMs,
		"error_class": completion.ErrorClass, "failure_stage": completion.FailureStage,
		"error_detail_code":    completion.ErrorDetailCode,
		"provider_http_status": completion.ProviderHTTPStatus,
		"request_bytes":        completion.RequestBytes, "response_bytes": completion.ResponseBytes,
		"trace_status":          completion.TraceStatus,
		"estimated_cost_micros": completion.EstimatedCostMicros,
		"finished_at":           completion.FinishedAt,
	}
	updated := tx.Model(&AIInvocation{}).
		Where("invocation_id = ? AND finished_at IS NULL", completion.InvocationID).
		Updates(updates)
	if updated.Error != nil {
		return AIInvocation{}, updated.Error
	}
	if updated.RowsAffected != 1 {
		return AIInvocation{}, ErrAIInvocationConflict
	}
	if err := tx.First(&invocation, "invocation_id = ?", completion.InvocationID).Error; err != nil {
		return AIInvocation{}, err
	}
	return invocation, nil
}

func sameInvocationCompletion(existing AIInvocation, completion AIInvocationCompletion) bool {
	return existing.Status == completion.Status && existing.OutputHash == completion.OutputHash &&
		existing.InputTokens == completion.InputTokens && existing.CachedInputTokens == completion.CachedInputTokens &&
		existing.OutputTokens == completion.OutputTokens && sameOptionalInt(existing.ReasoningTokens, completion.ReasoningTokens) &&
		existing.UsageShape == completion.UsageShape && existing.LatencyMs == completion.LatencyMs &&
		existing.ErrorClass == completion.ErrorClass && existing.FailureStage == completion.FailureStage &&
		existing.ErrorDetailCode == completion.ErrorDetailCode &&
		sameOptionalInt(existing.ProviderHTTPStatus, completion.ProviderHTTPStatus) &&
		existing.RequestBytes == completion.RequestBytes && existing.ResponseBytes == completion.ResponseBytes &&
		existing.TraceStatus == completion.TraceStatus &&
		existing.EstimatedCostMicros == completion.EstimatedCostMicros &&
		existing.FinishedAt != nil && existing.FinishedAt.Equal(completion.FinishedAt)
}

func sameOptionalInt(left, right *int) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

// RecoverInterruptedAIInvocations 收敛崩溃前已预留但未完成的调用。它只写
// processInterrupted 终局，绝不再次触碰 provider：intent 落一次 neutral
// fallback，reply 转人工并释放试运行 active slot；采集评分与批量招呼语
// 生成只形成明确失败，之后只继续尚无预留的成员。
func (s *Store) RecoverInterruptedAIInvocations(at time.Time) (int, error) {
	if at.IsZero() {
		at = time.Now()
	}
	recovered := 0
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var pending []AIInvocation
		if err := tx.Where("finished_at IS NULL").Order("created_at, invocation_id").Find(&pending).Error; err != nil {
			return err
		}
		for i := range pending {
			invocation := pending[i]
			if invocation.Purpose != m5ai.PurposeIntent && invocation.Purpose != m5ai.PurposeReply {
				return ErrAIInvocationConflict
			}
			updated := tx.Model(&AIInvocation{}).
				Where("invocation_id = ? AND finished_at IS NULL", invocation.InvocationID).
				Updates(map[string]any{
					"status": AIInvocationTransportFailed, "error_class": "processInterrupted", "finished_at": at,
				})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected == 0 {
				continue
			}
			recovered++
			var turn DialogueTurn
			if err := tx.First(&turn, "turn_id = ?", invocation.TurnID).Error; err != nil {
				return err
			}
			if err := tx.First(&invocation, "invocation_id = ?", invocation.InvocationID).Error; err != nil {
				return err
			}
			if _, v4Turn, err := communicationV4TurnApplicationTx(tx, turn); err != nil {
				return err
			} else if v4Turn {
				if invocation.Purpose == m5ai.PurposeReply {
					_, err := completeCommunicationV4ReplyTx(
						tx,
						&turn,
						invocation,
						m5ai.ReplySuggestion{},
						"",
						"replyProcessInterrupted",
						at.UTC(),
					)
					if err := settleCommunicationV4AdviceErrorTx(
						tx,
						&turn,
						err,
						at.UTC(),
					); err != nil {
						return err
					}
				} else {
					err := completeCommunicationV4IntentTx(
						tx,
						&turn,
						invocation,
						m5ai.IntentNeutral,
						DialogueIntentLLMFailure,
						"",
						at.UTC(),
					)
					if err := settleCommunicationV4AdviceErrorTx(
						tx,
						&turn,
						err,
						at.UTC(),
					); err != nil {
						return err
					}
				}
				continue
			}
			if invocation.Purpose == m5ai.PurposeReply {
				if err := markDialogueTurnManualTx(tx, &turn, "replyProcessInterrupted", at); err != nil {
					return err
				}
				continue
			}
			if turn.Status == DialogueTurnClassified && turn.IntentLabel == m5ai.IntentNeutral &&
				turn.IntentSource == DialogueIntentLLMFailure {
				continue
			}
			if turn.Status != DialogueTurnCollected {
				return ErrDialogueTurnState
			}
			bindingErr := validateDialogueTurnCurrentTx(tx, turn)
			if bindingErr != nil && !errors.Is(bindingErr, ErrDialogueTurnBinding) {
				return bindingErr
			}
			if bindingErr != nil {
				if err := markDialogueTurnManualTx(tx, &turn, "inputBoundaryChanged", at); err != nil {
					return err
				}
				continue
			}
			updates := map[string]any{
				"intent_label": m5ai.IntentNeutral, "intent_source": DialogueIntentLLMFailure,
				"classified_at": at,
			}
			if err := tx.Model(&DialogueTurn{}).
				Where("turn_id = ? AND status = ?", turn.TurnID, DialogueTurnCollected).
				Updates(updates).Error; err != nil {
				return err
			}
			if err := tx.First(&turn, "turn_id = ?", turn.TurnID).Error; err != nil {
				return err
			}
			updated = tx.Model(&DialogueTurn{}).
				Where("turn_id = ? AND status = ?", turn.TurnID, DialogueTurnCollected).
				Update("status", DialogueTurnClassified)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrDialogueTurnConflict
			}
		}
		var pendingScores []SourcingScoreInvocation
		if err := tx.Where("finished_at IS NULL").Order("started_at, invocation_id").Find(&pendingScores).Error; err != nil {
			return err
		}
		for i := range pendingScores {
			invocation := pendingScores[i]
			updated := tx.Model(&SourcingScoreInvocation{}).
				Where("invocation_id = ? AND finished_at IS NULL", invocation.InvocationID).
				Updates(map[string]any{
					"status": AIInvocationTransportFailed, "score": nil,
					"error_class": "processInterrupted", "finished_at": at,
				})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected == 0 {
				continue
			}
			recovered++
		}
		var pendingGreetings []SourcingGreetingInvocation
		if err := tx.Where("finished_at IS NULL").Order("started_at, invocation_id").Find(&pendingGreetings).Error; err != nil {
			return err
		}
		for i := range pendingGreetings {
			invocation := pendingGreetings[i]
			if invocation.Status != AIInvocationTransportFailed || invocation.GreetingText != "" || invocation.ContentHash != "" {
				return ErrAIInvocationConflict
			}
			updated := tx.Model(&SourcingGreetingInvocation{}).
				Where("invocation_id = ? AND finished_at IS NULL AND status = ?",
					invocation.InvocationID, AIInvocationTransportFailed).
				Updates(map[string]any{
					"status": AIInvocationTransportFailed, "greeting_text": "", "content_hash": "",
					"error_class": "processInterrupted", "finished_at": at,
				})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected == 0 {
				continue
			}
			recovered++
		}
		return nil
	})
	return recovered, err
}

func validIntentLabel(label m5ai.IntentLabel) bool {
	return label == m5ai.IntentInterested || label == m5ai.IntentRejected || label == m5ai.IntentNeutral
}

func (s *Store) MarkActiveM5TrialManualRequired(profileID, reason string, at time.Time) error {
	if strings.TrimSpace(profileID) == "" || strings.TrimSpace(reason) == "" {
		return ErrDialogueTurnInvalid
	}
	if at.IsZero() {
		at = time.Now()
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var profile CandidateProfile
		if err := tx.First(&profile, "profile_id = ?", profileID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCandidateProfileNotFound
			}
			return err
		}
		return markM5TrialManualRequiredTx(tx, profileID, reason, at)
	})
}

// RecheckDialogueTurnCurrent uses the same canonical message evaluator as
// freeze/reserve. A stale pre-effect turn and its planned action are atomically
// removed from automatic eligibility; their immutable facts remain queryable.
func (s *Store) RecheckDialogueTurnCurrent(turnID string, at time.Time) (bool, error) {
	if strings.TrimSpace(turnID) == "" {
		return false, ErrDialogueTurnInvalid
	}
	if at.IsZero() {
		at = time.Now()
	}
	current := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var turn DialogueTurn
		if err := tx.First(&turn, "turn_id = ?", turnID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrDialogueTurnNotFound
			}
			return err
		}
		switch turn.Status {
		case DialogueTurnCollected, DialogueTurnClassified, DialogueTurnAdviceReady:
		case DialogueTurnManualRequired, DialogueTurnSuperseded:
			return nil
		default:
			return ErrDialogueTurnState
		}
		var planned []CommunicationAction
		if turn.Status == DialogueTurnAdviceReady {
			if err := tx.Where(
				"turn_id = ? AND status = ?",
				turn.TurnID,
				CommunicationActionPlanned,
			).Find(&planned).Error; err != nil {
				return err
			}
		}
		var currentErr error
		if len(planned) == 1 && planned[0].DependsOnActionID != nil {
			_, currentErr = validateM5DependentActionCurrentTx(tx, turn, planned[0])
		} else {
			currentErr = validateDialogueTurnCurrentTx(tx, turn)
		}
		if currentErr != nil {
			if !errors.Is(currentErr, ErrDialogueTurnBinding) &&
				!errors.Is(currentErr, ErrCommunicationActionConflict) {
				return currentErr
			}
			return markDialogueTurnManualTx(tx, &turn, "inputBoundaryChanged", at)
		}
		current = true
		return nil
	})
	return current, err
}

// MarkDialogueTurnManualRequired 保留 turn/action 事实，只把仍可自动处理的
// 状态显式收敛到人工；不会物理删除或另造动作。
func (s *Store) MarkDialogueTurnManualRequired(turnID, reason string, at time.Time) error {
	if strings.TrimSpace(turnID) == "" || strings.TrimSpace(reason) == "" {
		return ErrDialogueTurnInvalid
	}
	if at.IsZero() {
		at = time.Now()
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var turn DialogueTurn
		if err := tx.First(&turn, "turn_id = ?", turnID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrDialogueTurnNotFound
			}
			return err
		}
		return markDialogueTurnManualTx(tx, &turn, reason, at)
	})
}

func (s *Store) AIInvocationsForTurn(turnID string) ([]AIInvocation, error) {
	var invocations []AIInvocation
	err := s.db.Where("turn_id = ?", turnID).Order("purpose, attempt").Find(&invocations).Error
	return invocations, err
}

func (s *Store) CommunicationActionByTurn(turnID string) (*CommunicationAction, error) {
	var action CommunicationAction
	err := s.db.Where("turn_id = ? AND kind = ?", turnID, CommunicationActionReplyText).
		Order("planned_at, created_at, action_id").
		First(&action).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &action, nil
}

func (s *Store) CommunicationActionByID(actionID string) (*CommunicationAction, error) {
	var action CommunicationAction
	err := s.db.First(&action, "action_id = ?", actionID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &action, nil
}

func (s *Store) CommunicationActionsByTurn(turnID string) ([]CommunicationAction, error) {
	var actions []CommunicationAction
	err := s.db.Where("turn_id = ?", turnID).Order("planned_at, created_at, action_id").Find(&actions).Error
	return actions, err
}

func (s *Store) PlannedCommunicationActionByTurn(turnID string) (*CommunicationAction, error) {
	var action CommunicationAction
	err := s.db.Where("turn_id = ? AND status = ?", turnID, CommunicationActionPlanned).
		Order("planned_at, created_at, action_id").
		First(&action).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &action, nil
}

func communicationV4PlannedActionTx(
	tx *gorm.DB,
	turn DialogueTurn,
	action CommunicationAction,
) (communication.V4PlannedAction, bool, error) {
	head, v4Turn, err := communicationV4TurnHeadApplicationTx(tx, turn)
	if err != nil || !v4Turn {
		return communication.V4PlannedAction{}, v4Turn, err
	}
	if head.Outcome.DialogueStatus != communication.V4DialogueActionsPlanned ||
		head.Outcome.NextAdvice != communication.V4AdviceNone ||
		head.Outcome.ManualReason != "" ||
		!validPersistedCommunicationV4Plans(head.Outcome.PlannedActions) {
		return communication.V4PlannedAction{}, true, ErrCommunicationActionConflict
	}
	planIndex := -1
	for index := range head.Outcome.PlannedActions {
		if head.Outcome.PlannedActions[index].ActionKey == action.ActionID {
			planIndex = index
			break
		}
	}
	if planIndex < 0 {
		return communication.V4PlannedAction{}, true, ErrCommunicationActionConflict
	}
	plan := head.Outcome.PlannedActions[planIndex]
	expectedParent := communicationV4ExpectedParentActionID(
		head.Outcome.PlannedActions,
		planIndex,
	)
	expectedText, ready := communicationV4PlanText(
		turn,
		head.Outcome.PlannedActions,
		planIndex,
		action.Text,
	)
	if !ready || !communicationActionMatchesV4Plan(
		action,
		plan,
		expectedText,
		expectedParent,
	) {
		return communication.V4PlannedAction{}, true, ErrCommunicationActionConflict
	}
	return plan, true, nil
}

func communicationActionMatchesV4Plan(
	action CommunicationAction,
	plan communication.V4PlannedAction,
	expectedText string,
	expectedParent *string,
) bool {
	switch action.Kind {
	case CommunicationActionReplyText:
		return supportedCommunicationV4TextKind(plan.Kind) &&
			strings.TrimSpace(expectedText) != "" &&
			action.Text == expectedText &&
			action.ContentHash == textcanon.Hash(action.Text) &&
			sameOptionalString(action.DependsOnActionID, expectedParent) &&
			action.InterviewStartsAtMs == nil &&
			action.InterviewEndsAtMs == nil &&
			action.InterviewMethod == nil
	case CommunicationActionInviteWechat:
		return plan.Kind == communication.V4ActionInviteWechat &&
			action.Text == "" &&
			action.ContentHash == communicationWechatInviteContentHash() &&
			expectedText == "" &&
			expectedParent != nil &&
			sameOptionalString(action.DependsOnActionID, expectedParent) &&
			action.InterviewStartsAtMs == nil &&
			action.InterviewEndsAtMs == nil &&
			action.InterviewMethod == nil
	case CommunicationActionInterviewInvite:
		return plan.Kind == communication.V4ActionInterviewInvite &&
			action.Text == "" &&
			expectedText == "" &&
			expectedParent != nil &&
			sameOptionalString(action.DependsOnActionID, expectedParent) &&
			action.InterviewStartsAtMs != nil &&
			action.InterviewEndsAtMs != nil &&
			action.InterviewMethod != nil &&
			plan.InterviewStartsAtMs != nil &&
			plan.InterviewEndsAtMs != nil &&
			plan.InterviewMethod != nil &&
			*action.InterviewStartsAtMs == *plan.InterviewStartsAtMs &&
			*action.InterviewEndsAtMs == *plan.InterviewEndsAtMs &&
			*action.InterviewMethod == *plan.InterviewMethod &&
			*action.InterviewStartsAtMs > 0 &&
			*action.InterviewEndsAtMs ==
				*action.InterviewStartsAtMs+communication.V4InterviewDurationMs &&
			*action.InterviewMethod == "wechatVideo" &&
			action.ContentHash == communicationInterviewInviteContentHash(
				*action.InterviewStartsAtMs,
				*action.InterviewEndsAtMs,
				*action.InterviewMethod,
			)
	default:
		return false
	}
}

func validPersistedCommunicationV4Plans(
	plans []communication.V4PlannedAction,
) bool {
	if len(plans) < 1 || len(plans) > m5ai.ReplyPhraseMaxItems+1 {
		return false
	}
	keys := make(map[string]struct{}, len(plans))
	textCount := 0
	var textKind communication.V4ActionKind
	for index, plan := range plans {
		if strings.TrimSpace(plan.ActionKey) == "" || plan.Text != "" {
			return false
		}
		if _, duplicate := keys[plan.ActionKey]; duplicate {
			return false
		}
		keys[plan.ActionKey] = struct{}{}
		if supportedCommunicationV4TextKind(plan.Kind) &&
			plan.InterviewStartsAtMs == nil &&
			plan.InterviewEndsAtMs == nil &&
			plan.InterviewMethod == nil {
			if textCount >= m5ai.ReplyPhraseMaxItems ||
				(textCount > 0 && plan.Kind != textKind) {
				return false
			}
			if textCount == 0 {
				textKind = plan.Kind
			}
			textCount++
			continue
		}
		if index != len(plans)-1 || textCount == 0 ||
			!supportedCommunicationV4CardPlan(plan) ||
			!approvedCommunicationV4VisibleCombination(textKind, plan.Kind) {
			return false
		}
	}
	return textCount > 0
}

func communicationV4ExpectedParentActionID(
	plans []communication.V4PlannedAction,
	index int,
) *string {
	if index <= 0 || index >= len(plans) {
		return nil
	}
	parent := plans[index-1].ActionKey
	return &parent
}

func communicationV4PlanText(
	turn DialogueTurn,
	plans []communication.V4PlannedAction,
	index int,
	legacyText string,
) (string, bool) {
	if index < 0 || index >= len(plans) {
		return "", false
	}
	textOrdinal := -1
	textCount := 0
	for planIndex, plan := range plans {
		if !supportedCommunicationV4TextKind(plan.Kind) {
			break
		}
		if planIndex == index {
			textOrdinal = textCount
		}
		textCount++
	}
	if textOrdinal < 0 {
		return "", true
	}
	if len(turn.ReplyPhrases) == textCount {
		text := turn.ReplyPhrases[textOrdinal]
		return text, m5ai.ValidateSendText(text) == nil
	}
	// Pre-migration V4 turns have no ReplyPhrases and could contain only one
	// text action. Its already-materialized action remains the immutable body
	// source; no future text can be invented from a redacted projection.
	if len(turn.ReplyPhrases) == 0 && textCount == 1 &&
		strings.TrimSpace(legacyText) != "" &&
		m5ai.ValidateSendText(legacyText) == nil {
		return legacyText, true
	}
	return "", false
}

func nextCommunicationV4PlanTx(
	tx *gorm.DB,
	turn DialogueTurn,
	actionKey string,
) (*communication.V4PlannedAction, error) {
	head, found, err := communicationV4TurnHeadApplicationTx(tx, turn)
	if err != nil {
		return nil, err
	}
	if !found ||
		head.Outcome.DialogueStatus != communication.V4DialogueActionsPlanned ||
		head.Outcome.NextAdvice != communication.V4AdviceNone ||
		head.Outcome.ManualReason != "" ||
		!validPersistedCommunicationV4Plans(head.Outcome.PlannedActions) {
		return nil, ErrCommunicationActionConflict
	}
	for index := range head.Outcome.PlannedActions {
		if head.Outcome.PlannedActions[index].ActionKey != actionKey {
			continue
		}
		if index+1 == len(head.Outcome.PlannedActions) {
			return nil, nil
		}
		next := head.Outcome.PlannedActions[index+1]
		return &next, nil
	}
	return nil, ErrCommunicationActionConflict
}

func materializeDependentCommunicationActionTx(
	tx *gorm.DB,
	turn DialogueTurn,
	parentBeforeUpdate CommunicationAction,
	plan communication.V4PlannedAction,
	at time.Time,
) error {
	var parent CommunicationAction
	if tx == nil {
		return ErrCommunicationActionConflict
	}
	if err := tx.First(&parent, "action_id = ?", parentBeforeUpdate.ActionID).Error; err != nil {
		return err
	}
	head, found, err := communicationV4TurnHeadApplicationTx(tx, turn)
	if err != nil {
		return err
	}
	if !found || !validPersistedCommunicationV4Plans(head.Outcome.PlannedActions) {
		return ErrCommunicationActionConflict
	}
	planIndex := -1
	for index := range head.Outcome.PlannedActions {
		if head.Outcome.PlannedActions[index].ActionKey == plan.ActionKey {
			planIndex = index
			break
		}
	}
	if planIndex <= 0 ||
		!sameCommunicationV4Plan(head.Outcome.PlannedActions[planIndex], plan) ||
		head.Outcome.PlannedActions[planIndex-1].ActionKey != parent.ActionID ||
		parent.TurnID != turn.TurnID ||
		parent.Status != CommunicationActionSent {
		return ErrCommunicationActionConflict
	}
	text, textReady := communicationV4PlanText(
		turn,
		head.Outcome.PlannedActions,
		planIndex,
		"",
	)
	if !textReady {
		return ErrCommunicationActionConflict
	}
	action := CommunicationAction{
		ActionID:          plan.ActionKey,
		TurnID:            turn.TurnID,
		Status:            CommunicationActionPlanned,
		DependsOnActionID: &parent.ActionID,
		PlannedAt:         at,
		CreatedAt:         at,
		UpdatedAt:         at,
	}
	switch plan.Kind {
	case communication.V4ActionReplyText,
		communication.V4ActionServiceReply,
		communication.V4ActionRejectionRetention,
		communication.V4ActionRejectionClosing,
		communication.V4ActionWechatReceipt,
		communication.V4ActionInterviewAcceptedReceipt,
		communication.V4ActionInterviewRejectionReply:
		action.Kind = CommunicationActionReplyText
		action.Text = text
		action.ContentHash = textcanon.Hash(text)
	case communication.V4ActionInviteWechat:
		action.Kind = CommunicationActionInviteWechat
		action.ContentHash = communicationWechatInviteContentHash()
	case communication.V4ActionInterviewInvite:
		action.Kind = CommunicationActionInterviewInvite
		action.InterviewStartsAtMs = cloneOptionalInt64(plan.InterviewStartsAtMs)
		action.InterviewEndsAtMs = cloneOptionalInt64(plan.InterviewEndsAtMs)
		action.InterviewMethod = cloneOptionalString(plan.InterviewMethod)
		action.ContentHash = communicationInterviewInviteContentHash(
			*action.InterviewStartsAtMs,
			*action.InterviewEndsAtMs,
			*action.InterviewMethod,
		)
	default:
		return ErrCommunicationActionConflict
	}
	var existing CommunicationAction
	err = tx.First(&existing, "action_id = ?", action.ActionID).Error
	if err == nil {
		if !sameMaterializedCommunicationAction(existing, action) {
			return ErrCommunicationActionConflict
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return tx.Create(&action).Error
}

func sameCommunicationV4Plan(
	left communication.V4PlannedAction,
	right communication.V4PlannedAction,
) bool {
	return left.ActionKey == right.ActionKey &&
		left.Kind == right.Kind &&
		left.Text == right.Text &&
		left.CardMessageSeq == right.CardMessageSeq &&
		left.AnchorMessageSeq == right.AnchorMessageSeq &&
		sameOptionalInt64(left.InterviewStartsAtMs, right.InterviewStartsAtMs) &&
		sameOptionalInt64(left.InterviewEndsAtMs, right.InterviewEndsAtMs) &&
		sameOptionalString(left.InterviewMethod, right.InterviewMethod) &&
		left.Round == right.Round &&
		left.Stage == right.Stage &&
		left.EndReason == right.EndReason &&
		sameOptionalTime(left.DueAt, right.DueAt)
}

func sameOptionalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func communicationWechatInviteContentHash() string {
	return textcanon.Hash("card\x1fwechatExchange")
}

func communicationInterviewInviteContentHash(
	startsAtMs int64,
	endsAtMs int64,
	method string,
) string {
	return textcanon.Hash(
		"card\x1finterviewInvite\x1f" +
			strconv.FormatInt(startsAtMs, 10) +
			"\x1f" +
			strconv.FormatInt(endsAtMs, 10) +
			"\x1f" +
			method,
	)
}

func sameMaterializedCommunicationAction(left, right CommunicationAction) bool {
	return left.ActionID == right.ActionID &&
		left.TurnID == right.TurnID &&
		left.Kind == right.Kind &&
		left.Text == right.Text &&
		left.ContentHash == right.ContentHash &&
		sameOptionalString(left.DependsOnActionID, right.DependsOnActionID) &&
		sameOptionalInt64(left.InterviewStartsAtMs, right.InterviewStartsAtMs) &&
		sameOptionalInt64(left.InterviewEndsAtMs, right.InterviewEndsAtMs) &&
		sameOptionalString(left.InterviewMethod, right.InterviewMethod)
}

func communicationActionMatchesMessage(action CommunicationAction, message Message) bool {
	if message.Direction != "out" || message.ContentHash != action.ContentHash {
		return false
	}
	switch action.Kind {
	case CommunicationActionReplyText:
		return message.Kind == "text"
	case CommunicationActionInviteWechat:
		return message.Kind == "card" &&
			message.CardType == "wechatExchange" &&
			message.CardState == "pending" &&
			message.InterviewStartsAtMs == nil &&
			message.InterviewEndsAtMs == nil &&
			message.InterviewMethod == nil
	case CommunicationActionInterviewInvite:
		return message.Kind == "card" &&
			message.CardType == "interviewInvite" &&
			message.CardState == "unknown" &&
			sameOptionalInt64(message.InterviewStartsAtMs, action.InterviewStartsAtMs) &&
			sameOptionalInt64(message.InterviewEndsAtMs, action.InterviewEndsAtMs) &&
			sameOptionalString(message.InterviewMethod, action.InterviewMethod)
	default:
		return false
	}
}

// bindM5AutomaticActionTx is the M5 intent-construction authorization point.
// It runs inside CreateEffectIntentAndCmd's WAL/head transaction, so an action
// can never become effectPending without the matching EffectIntent and Cmd.
func bindM5AutomaticActionTx(
	tx *gorm.DB,
	actionID string,
	previousIntentID string,
	intent *EffectIntent,
	command *CmdRecord,
	at time.Time,
) error {
	if tx == nil || intent == nil || command == nil || strings.TrimSpace(actionID) == "" {
		return ErrCommunicationActionInvalid
	}
	expectedIntentID, err := M5AutomaticIntentID(actionID)
	if err != nil || intent.IntentID != expectedIntentID ||
		command.IntentID != intent.IntentID || command.Name != intent.Primitive {
		return ErrCommunicationActionInvalid
	}
	var action CommunicationAction
	legacyErr := tx.First(&action, "action_id = ?", actionID).Error
	var eventAction CommunicationV4EventAction
	eventErr := tx.First(&eventAction, "action_id = ?", actionID).Error
	if legacyErr != nil && !errors.Is(legacyErr, gorm.ErrRecordNotFound) {
		return legacyErr
	}
	if eventErr != nil && !errors.Is(eventErr, gorm.ErrRecordNotFound) {
		return eventErr
	}
	legacyFound := legacyErr == nil
	eventFound := eventErr == nil
	if legacyFound && eventFound {
		return ErrCommunicationActionConflict
	}
	if eventFound {
		return bindCommunicationV4EventActionTx(
			tx,
			eventAction,
			previousIntentID,
			intent,
			command,
			at,
		)
	}
	if !legacyFound {
		return ErrCommunicationActionInvalid
	}
	if action.Status != CommunicationActionPlanned ||
		action.EffectIntentID != nil ||
		strings.TrimSpace(action.ContentHash) == "" ||
		action.ContentHash != intent.SendFingerprint ||
		communicationActionPrimitive(action.Kind) != intent.Primitive {
		return ErrCommunicationActionConflict
	}
	var turn DialogueTurn
	if err := tx.First(&turn, "turn_id = ?", action.TurnID).Error; err != nil {
		return err
	}
	if _, _, err := communicationV4PlannedActionTx(tx, turn, action); err != nil {
		return err
	}
	if turn.Status != DialogueTurnAdviceReady || turn.ConversationRef != intent.TargetRef {
		return ErrDialogueTurnState
	}
	if action.DependsOnActionID == nil {
		if err := validateDialogueTurnCurrentTx(tx, turn); err != nil {
			return err
		}
	} else if err := validateM5ActionDependencyTx(
		tx,
		turn,
		action,
		previousIntentID,
		intent,
	); err != nil {
		return err
	}
	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", turn.ProfileID).Error; err != nil {
		return err
	}
	if profile.Platform != intent.Platform || profile.AccountRef != intent.AccountRef ||
		profile.ConversationRef == nil || *profile.ConversationRef != intent.TargetRef {
		return ErrDialogueTurnBinding
	}
	if err := validateM5AutomaticCommand(action, turn.ConversationRef, *command); err != nil {
		return ErrCommunicationActionConflict
	}
	intentID := intent.IntentID
	updated := tx.Model(&CommunicationAction{}).
		Where("action_id = ? AND status = ? AND effect_intent_id IS NULL", action.ActionID, CommunicationActionPlanned).
		Updates(map[string]any{
			"status": CommunicationActionEffectPending, "effect_intent_id": intentID,
			"effect_started_at": at, "updated_at": at,
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrCommunicationActionConflict
	}
	updated = tx.Model(&DialogueTurn{}).
		Where("turn_id = ? AND status = ?", turn.TurnID, DialogueTurnAdviceReady).
		Updates(map[string]any{"status": DialogueTurnDispatching, "updated_at": at})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrDialogueTurnConflict
	}
	return nil
}

func validateM5AutomaticIntentLinkTx(tx *gorm.DB, actionID string, intent EffectIntent) error {
	expectedIntentID, err := M5AutomaticIntentID(actionID)
	if err != nil || expectedIntentID != intent.IntentID {
		return ErrCommunicationActionInvalid
	}
	var action CommunicationAction
	legacyErr := tx.First(&action, "action_id = ?", actionID).Error
	var eventAction CommunicationV4EventAction
	eventErr := tx.First(&eventAction, "action_id = ?", actionID).Error
	if legacyErr != nil && !errors.Is(legacyErr, gorm.ErrRecordNotFound) {
		return legacyErr
	}
	if eventErr != nil && !errors.Is(eventErr, gorm.ErrRecordNotFound) {
		return eventErr
	}
	legacyFound := legacyErr == nil
	eventFound := eventErr == nil
	if legacyFound && eventFound {
		return ErrCommunicationActionConflict
	}
	if eventFound {
		return validateCommunicationV4EventActionIntentLinkTx(
			tx,
			eventAction,
			intent,
		)
	}
	if !legacyFound {
		return ErrCommunicationActionInvalid
	}
	if action.EffectIntentID == nil || *action.EffectIntentID != intent.IntentID ||
		action.EffectStartedAt == nil ||
		action.ContentHash != intent.SendFingerprint ||
		communicationActionPrimitive(action.Kind) != intent.Primitive {
		return ErrCommunicationActionConflict
	}
	switch action.Status {
	case CommunicationActionEffectPending, CommunicationActionSent, CommunicationActionManualRequired:
	default:
		return ErrCommunicationActionConflict
	}
	var turn DialogueTurn
	if err := tx.First(&turn, "turn_id = ?", action.TurnID).Error; err != nil {
		return err
	}
	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", turn.ProfileID).Error; err != nil {
		return err
	}
	if turn.ConversationRef != intent.TargetRef || profile.Platform != intent.Platform ||
		profile.AccountRef != intent.AccountRef {
		return ErrCommunicationActionConflict
	}
	return nil
}

func bindCommunicationV4EventActionTx(
	tx *gorm.DB,
	action CommunicationV4EventAction,
	previousIntentID string,
	intent *EffectIntent,
	command *CmdRecord,
	at time.Time,
) error {
	if action.Status != CommunicationV4EventActionPlanned ||
		action.EffectIntentID != nil ||
		action.EffectStartedAt != nil ||
		action.SentAt != nil ||
		action.FailureReason != "" ||
		strings.TrimSpace(action.ContentHash) == "" ||
		action.ContentHash != intent.SendFingerprint ||
		communicationV4EventActionPrimitive(action) != intent.Primitive {
		return ErrCommunicationActionConflict
	}
	sourceInfo, err := communicationV4EventActionSourceTx(tx, action)
	if err != nil {
		return err
	}
	source := sourceInfo.Action
	if source.ActionKey != action.SemanticActionKey ||
		source.Kind != action.V4Kind ||
		source.CardMessageSeq != action.CardMessageSeq {
		return ErrCommunicationActionConflict
	}
	var confirmed CommunicationV4ProjectionApplication
	confirmedErr := tx.First(
		&confirmed,
		"profile_id = ? AND input_kind = ? AND input_key = ?",
		action.ProfileID,
		CommunicationV4InputConfirmedAction,
		action.SemanticActionKey,
	).Error
	if confirmedErr == nil {
		return ErrCommunicationActionConflict
	}
	if !errors.Is(confirmedErr, gorm.ErrRecordNotFound) {
		return confirmedErr
	}
	profile, aggregate, conversation, err :=
		communicationV4EventActionCurrentProfileTx(tx, action, *intent)
	if err != nil {
		return err
	}
	if aggregate.AutomationStatus != ProfileCommunicationAutomationActive ||
		aggregate.ProjectedThroughSeq != conversation.LastMessageSeq {
		return ErrDialogueTurnBinding
	}
	if action.SourceInputKind == CommunicationV4InputSchedulePlan &&
		sourceInfo.ConversationRef != conversation.ConversationRef {
		return ErrDialogueTurnBinding
	}
	if action.DependsOnActionID == nil {
		if aggregate.Revision != sourceInfo.BasisRevision {
			return ErrCommunicationActionConflict
		}
		switch action.EffectKind {
		case CommunicationV4EventEffectReplyText:
		case CommunicationV4EventEffectInviteWechat:
			if action.SourceInputKind != CommunicationV4InputSchedulePlan ||
				action.V4Kind != communication.V4ActionColdWechatInvite {
				return ErrCommunicationActionConflict
			}
		case CommunicationV4EventEffectAcceptWechat:
			if action.V4Kind != communication.V4ActionAcceptWechat {
				return ErrCommunicationActionConflict
			}
		default:
			return ErrCommunicationActionConflict
		}
	} else if err := validateCommunicationV4EventActionDependencyTx(
		tx,
		action,
		sourceInfo,
		previousIntentID,
		intent,
		profile,
		aggregate,
		conversation,
	); err != nil {
		return err
	}
	if err := validateCommunicationV4EventActionCommand(
		tx,
		action,
		conversation.ConversationRef,
		*command,
	); err != nil {
		return err
	}
	intentID := intent.IntentID
	updated := tx.Model(&CommunicationV4EventAction{}).
		Where(
			"action_id = ? AND status = ? AND effect_intent_id IS NULL",
			action.ActionID,
			CommunicationV4EventActionPlanned,
		).
		Updates(map[string]any{
			"status":            CommunicationV4EventActionEffectPending,
			"effect_intent_id":  intentID,
			"effect_started_at": at,
			"updated_at":        at,
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrCommunicationActionConflict
	}
	return nil
}

func validateCommunicationV4EventActionIntentLinkTx(
	tx *gorm.DB,
	action CommunicationV4EventAction,
	intent EffectIntent,
) error {
	expectedIntentID, err := M5AutomaticIntentID(action.ActionID)
	if err != nil ||
		expectedIntentID != intent.IntentID ||
		action.EffectIntentID == nil ||
		*action.EffectIntentID != intent.IntentID ||
		action.EffectStartedAt == nil ||
		action.ContentHash != intent.SendFingerprint ||
		communicationV4EventActionPrimitive(action) != intent.Primitive {
		return ErrCommunicationActionConflict
	}
	switch action.Status {
	case CommunicationV4EventActionEffectPending,
		CommunicationV4EventActionSent,
		CommunicationV4EventActionManualRequired:
	default:
		return ErrCommunicationActionConflict
	}
	if _, err := communicationV4EventActionSourceTx(tx, action); err != nil {
		return err
	}
	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", action.ProfileID).Error; err != nil {
		return err
	}
	if profile.Platform != intent.Platform ||
		profile.AccountRef != intent.AccountRef ||
		profile.ConversationRef == nil ||
		*profile.ConversationRef != intent.TargetRef {
		return ErrCommunicationActionConflict
	}
	return nil
}

type communicationV4EventActionSource struct {
	Action          communication.V4EventAction
	Actions         []communication.V4EventAction
	BasisRevision   uint64
	ConversationRef string
	Round           uint64
	Stage           uint8
}

func communicationV4EventActionSourceTx(
	tx *gorm.DB,
	action CommunicationV4EventAction,
) (
	communicationV4EventActionSource,
	error,
) {
	if tx == nil ||
		strings.TrimSpace(action.ActionID) == "" ||
		strings.TrimSpace(action.ProfileID) == "" ||
		strings.TrimSpace(action.SourceInputKey) == "" ||
		action.SourceOrdinal < 0 ||
		!validCommunicationV4EventActionDisposition(action) {
		return communicationV4EventActionSource{},
			ErrCommunicationActionConflict
	}
	expectedActionID, err := CommunicationV4EventActionID(
		action.ProfileID,
		action.SemanticActionKey,
	)
	if err != nil || expectedActionID != action.ActionID {
		return communicationV4EventActionSource{},
			ErrCommunicationActionConflict
	}
	if action.SourceInputKind == CommunicationV4InputSchedulePlan {
		plan, found, err := communicationV4SchedulePlanTx(
			tx,
			action.SourceInputKey,
		)
		if err != nil {
			return communicationV4EventActionSource{}, err
		}
		if !found ||
			plan.ProfileID != action.ProfileID ||
			action.SourceOrdinal >= len(plan.PlannedActions) ||
			!communicationV4ScheduleEventActionMatches(
				action,
				plan,
				plan.PlannedActions[action.SourceOrdinal],
				action.SourceOrdinal,
			) {
			return communicationV4EventActionSource{},
				ErrCommunicationActionConflict
		}
		actions := make([]communication.V4EventAction, len(plan.PlannedActions))
		for index, planned := range plan.PlannedActions {
			actions[index] = communication.V4EventAction{
				ActionKey:      planned.ActionKey,
				Kind:           planned.Kind,
				CardMessageSeq: planned.CardMessageSeq,
			}
		}
		return communicationV4EventActionSource{
			Action:          actions[action.SourceOrdinal],
			Actions:         actions,
			BasisRevision:   plan.BasisRevision,
			ConversationRef: plan.ConversationRef,
			Round:           plan.PlannedActions[action.SourceOrdinal].Round,
			Stage:           plan.PlannedActions[action.SourceOrdinal].Stage,
		}, nil
	}
	if action.SourceInputKind != CommunicationV4InputBusinessEvent &&
		action.SourceInputKind != CommunicationV4InputDialogueTurn {
		return communicationV4EventActionSource{}, ErrCommunicationActionConflict
	}
	application, found, err := communicationV4ApplicationTx(
		tx,
		action.ProfileID,
		action.SourceInputKind,
		action.SourceInputKey,
	)
	if err != nil {
		return communicationV4EventActionSource{}, err
	}
	if !found ||
		action.SourceOrdinal >= len(application.Outcome.Actions) {
		return communicationV4EventActionSource{}, ErrCommunicationActionConflict
	}
	source := application.Outcome.Actions[action.SourceOrdinal]
	if source.ActionKey != action.SemanticActionKey ||
		source.Kind != action.V4Kind ||
		source.CardMessageSeq != action.CardMessageSeq {
		return communicationV4EventActionSource{}, ErrCommunicationActionConflict
	}
	return communicationV4EventActionSource{
		Action:        source,
		Actions:       application.Outcome.Actions,
		BasisRevision: application.ToRevision,
	}, nil
}

func communicationV4EventActionPrimitive(
	action CommunicationV4EventAction,
) string {
	switch action.EffectKind {
	case CommunicationV4EventEffectReplyText:
		switch action.V4Kind {
		case communication.V4ActionWechatReceipt,
			communication.V4ActionInterviewAcceptedReceipt,
			communication.V4ActionColdPrompt,
			communication.V4ActionColdWechatText,
			communication.V4ActionInterviewFollowup:
			return primitiveChatSendMessage
		}
	case CommunicationV4EventEffectInviteWechat:
		if action.V4Kind == communication.V4ActionInviteWechat ||
			action.V4Kind == communication.V4ActionColdWechatInvite {
			return primitiveChatSendWechatInvite
		}
	case CommunicationV4EventEffectAcceptWechat:
		if action.V4Kind == communication.V4ActionAcceptWechat {
			return primitiveChatAcceptWechat
		}
	}
	return ""
}

func communicationV4EventActionCurrentProfileTx(
	tx *gorm.DB,
	action CommunicationV4EventAction,
	intent EffectIntent,
) (
	CandidateProfile,
	CommunicationV4Aggregate,
	Conversation,
	error,
) {
	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", action.ProfileID).Error; err != nil {
		return CandidateProfile{}, CommunicationV4Aggregate{}, Conversation{}, err
	}
	if profile.Platform != intent.Platform ||
		profile.AccountRef != intent.AccountRef ||
		profile.ConversationRef == nil ||
		*profile.ConversationRef != intent.TargetRef {
		return CandidateProfile{},
			CommunicationV4Aggregate{},
			Conversation{},
			ErrDialogueTurnBinding
	}
	aggregate, err := communicationV4AggregateTx(tx, action.ProfileID)
	if err != nil {
		return CandidateProfile{}, CommunicationV4Aggregate{}, Conversation{}, err
	}
	var conversation Conversation
	key := ConversationKey{
		Platform:        profile.Platform,
		AccountRef:      profile.AccountRef,
		ConversationRef: *profile.ConversationRef,
	}
	if err := tx.Where(
		conversationWhere(key),
		conversationArgs(key)...,
	).First(&conversation).Error; err != nil {
		return CandidateProfile{}, CommunicationV4Aggregate{}, Conversation{}, err
	}
	return profile, aggregate, conversation, nil
}

func validateCommunicationV4EventActionCommand(
	tx *gorm.DB,
	action CommunicationV4EventAction,
	conversationRef string,
	command CmdRecord,
) error {
	switch action.EffectKind {
	case CommunicationV4EventEffectReplyText:
		var args protocol.ChatSendMessageArgs
		if err := json.Unmarshal([]byte(command.Args), &args); err != nil ||
			args.ConversationRef != conversationRef ||
			args.Text != action.Text {
			return ErrCommunicationActionConflict
		}
	case CommunicationV4EventEffectInviteWechat:
		var args protocol.ChatSendWechatInviteArgs
		if err := json.Unmarshal([]byte(command.Args), &args); err != nil ||
			args.ConversationRef != conversationRef {
			return ErrCommunicationActionConflict
		}
	case CommunicationV4EventEffectAcceptWechat:
		var args protocol.ChatAcceptWechatArgs
		if err := json.Unmarshal([]byte(command.Args), &args); err != nil ||
			args.ConversationRef != conversationRef {
			return ErrCommunicationActionConflict
		}
		expectedSourceKey, err := communicationV4AcceptWechatRequestSourceTx(
			tx,
			action,
			conversationRef,
		)
		if err != nil || args.RequestSourceKey != expectedSourceKey {
			return ErrCommunicationActionConflict
		}
	default:
		return ErrCommunicationActionInvalid
	}
	return nil
}

type communicationV4PositiveActionParent struct {
	actionID          string
	semanticActionKey string
	profileID         string
	v4Kind            communication.V4ActionKind
	cardMessageSeq    int64
	contentHash       string
	effectIntentID    string
	sentAt            time.Time
}

func validateCommunicationV4EventActionDependencyTx(
	tx *gorm.DB,
	action CommunicationV4EventAction,
	sourceInfo communicationV4EventActionSource,
	previousIntentID string,
	childIntent *EffectIntent,
	profile CandidateProfile,
	aggregate CommunicationV4Aggregate,
	conversation Conversation,
) error {
	if (action.V4Kind != communication.V4ActionInviteWechat &&
		action.V4Kind != communication.V4ActionColdWechatInvite) ||
		action.EffectKind != CommunicationV4EventEffectInviteWechat ||
		action.DependsOnActionID == nil ||
		strings.TrimSpace(*action.DependsOnActionID) == "" ||
		childIntent == nil {
		return ErrCommunicationActionConflict
	}
	var expectedParent *communication.V4EventAction
	if action.SourceInputKind == CommunicationV4InputSchedulePlan {
		if action.V4Kind != communication.V4ActionColdWechatInvite ||
			action.SourceOrdinal <= 0 ||
			action.SourceOrdinal >= len(sourceInfo.Actions) {
			return ErrCommunicationActionConflict
		}
		candidate := sourceInfo.Actions[action.SourceOrdinal-1]
		if candidate.Kind != communication.V4ActionColdWechatText {
			return ErrCommunicationActionConflict
		}
		expectedParent = &candidate
	} else {
		for index := range sourceInfo.Actions {
			candidate := sourceInfo.Actions[index]
			if candidate.Kind != communication.V4ActionInterviewAcceptedReceipt {
				continue
			}
			if expectedParent != nil {
				return ErrCommunicationActionConflict
			}
			copy := candidate
			expectedParent = &copy
		}
		if expectedParent == nil ||
			expectedParent.CardMessageSeq != action.CardMessageSeq {
			return ErrCommunicationActionConflict
		}
	}
	parent, err := communicationV4PositiveActionParentTx(
		tx,
		*action.DependsOnActionID,
		action,
		*expectedParent,
	)
	if err != nil {
		return err
	}
	if parent.profileID != action.ProfileID ||
		parent.semanticActionKey != expectedParent.ActionKey ||
		parent.v4Kind != expectedParent.Kind ||
		parent.cardMessageSeq != expectedParent.CardMessageSeq ||
		previousIntentID != parent.effectIntentID {
		return ErrCommunicationActionConflict
	}
	var parentIntent EffectIntent
	if err := tx.First(
		&parentIntent,
		"intent_id = ?",
		parent.effectIntentID,
	).Error; err != nil {
		return err
	}
	if (parentIntent.Status != EffectIntentOk &&
		parentIntent.Status != EffectIntentResolvedOk) ||
		parentIntent.Platform != profile.Platform ||
		parentIntent.AccountRef != profile.AccountRef ||
		parentIntent.TargetRef != conversation.ConversationRef ||
		parentIntent.SendFingerprint != parent.contentHash ||
		parentIntent.ResultMessageSeq == nil {
		return ErrCommunicationActionConflict
	}
	var message Message
	if err := tx.First(
		&message,
		"outbound_intent_id = ?",
		parentIntent.IntentID,
	).Error; err != nil {
		return err
	}
	if message.RetractedAt != nil ||
		message.Seq != *parentIntent.ResultMessageSeq ||
		message.Direction != "out" ||
		message.Kind != "text" ||
		message.ContentHash != parent.contentHash ||
		conversation.LastMessageSeq != message.Seq ||
		aggregate.ProjectedThroughSeq != message.Seq ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive {
		return ErrDialogueTurnBinding
	}
	confirmed, found, err := communicationV4ApplicationTx(
		tx,
		action.ProfileID,
		CommunicationV4InputConfirmedAction,
		parent.semanticActionKey,
	)
	if err != nil {
		return err
	}
	if !found ||
		confirmed.SemanticKind != string(parent.v4Kind) ||
		confirmed.MessageSeq != message.Seq ||
		aggregate.Revision != confirmed.ToRevision {
		return ErrCommunicationActionConflict
	}
	head, latest, err := effectIntentHeadTx(
		tx,
		childIntent.Platform,
		childIntent.AccountRef,
		childIntent.TargetRef,
	)
	if err != nil {
		return err
	}
	if head == nil ||
		latest == nil ||
		head.LatestIntentID != parentIntent.IntentID ||
		latest.IntentID != parentIntent.IntentID {
		return ErrDialogueTurnBinding
	}
	return nil
}

func communicationV4PositiveActionParentTx(
	tx *gorm.DB,
	parentActionID string,
	child CommunicationV4EventAction,
	expected communication.V4EventAction,
) (communicationV4PositiveActionParent, error) {
	var legacy CommunicationAction
	legacyErr := tx.First(&legacy, "action_id = ?", parentActionID).Error
	var event CommunicationV4EventAction
	eventErr := tx.First(&event, "action_id = ?", parentActionID).Error
	if legacyErr != nil && !errors.Is(legacyErr, gorm.ErrRecordNotFound) {
		return communicationV4PositiveActionParent{}, legacyErr
	}
	if eventErr != nil && !errors.Is(eventErr, gorm.ErrRecordNotFound) {
		return communicationV4PositiveActionParent{}, eventErr
	}
	legacyFound := legacyErr == nil
	eventFound := eventErr == nil
	if legacyFound == eventFound {
		return communicationV4PositiveActionParent{}, ErrCommunicationActionConflict
	}
	if eventFound {
		if event.ProfileID != child.ProfileID ||
			event.SourceInputKind != child.SourceInputKind ||
			event.SourceInputKey != child.SourceInputKey ||
			event.SemanticActionKey != expected.ActionKey ||
			event.V4Kind != expected.Kind ||
			event.CardMessageSeq != expected.CardMessageSeq ||
			event.EffectKind != CommunicationV4EventEffectReplyText ||
			event.Status != CommunicationV4EventActionSent ||
			event.EffectIntentID == nil ||
			event.EffectStartedAt == nil ||
			event.SentAt == nil ||
			event.FailureReason != "" {
			return communicationV4PositiveActionParent{}, ErrCommunicationActionConflict
		}
		if _, err := communicationV4EventActionSourceTx(tx, event); err != nil {
			return communicationV4PositiveActionParent{}, err
		}
		return communicationV4PositiveActionParent{
			actionID: event.ActionID, semanticActionKey: event.SemanticActionKey,
			profileID: event.ProfileID, v4Kind: event.V4Kind,
			cardMessageSeq: event.CardMessageSeq,
			contentHash:    event.ContentHash,
			effectIntentID: *event.EffectIntentID,
			sentAt:         *event.SentAt,
		}, nil
	}
	var turn DialogueTurn
	if err := tx.First(&turn, "turn_id = ?", legacy.TurnID).Error; err != nil {
		return communicationV4PositiveActionParent{}, err
	}
	plan, v4Turn, err := communicationV4PlannedActionTx(tx, turn, legacy)
	if err != nil {
		return communicationV4PositiveActionParent{}, err
	}
	if !v4Turn ||
		child.SourceInputKind != CommunicationV4InputDialogueTurn ||
		child.SourceInputKey != turn.TurnID ||
		turn.ProfileID != child.ProfileID ||
		plan.ActionKey != expected.ActionKey ||
		plan.Kind != expected.Kind ||
		plan.CardMessageSeq != expected.CardMessageSeq ||
		legacy.Kind != CommunicationActionReplyText ||
		legacy.Status != CommunicationActionSent ||
		legacy.EffectIntentID == nil ||
		legacy.EffectStartedAt == nil ||
		legacy.SentAt == nil ||
		legacy.FailureReason != "" {
		return communicationV4PositiveActionParent{}, ErrCommunicationActionConflict
	}
	return communicationV4PositiveActionParent{
		actionID: legacy.ActionID, semanticActionKey: plan.ActionKey,
		profileID: turn.ProfileID, v4Kind: plan.Kind,
		cardMessageSeq: plan.CardMessageSeq,
		contentHash:    legacy.ContentHash,
		effectIntentID: *legacy.EffectIntentID,
		sentAt:         *legacy.SentAt,
	}, nil
}

func communicationActionPrimitive(kind CommunicationActionKind) string {
	switch kind {
	case CommunicationActionReplyText:
		return primitiveChatSendMessage
	case CommunicationActionInviteWechat:
		return primitiveChatSendWechatInvite
	case CommunicationActionInterviewInvite:
		return primitiveChatSendInviteCard
	default:
		return ""
	}
}

func validateM5AutomaticCommand(
	action CommunicationAction,
	conversationRef string,
	command CmdRecord,
) error {
	switch action.Kind {
	case CommunicationActionReplyText:
		var args protocol.ChatSendMessageArgs
		if err := json.Unmarshal([]byte(command.Args), &args); err != nil ||
			args.ConversationRef != conversationRef ||
			args.Text != action.Text {
			return ErrCommunicationActionConflict
		}
	case CommunicationActionInviteWechat:
		var args protocol.ChatSendWechatInviteArgs
		if err := json.Unmarshal([]byte(command.Args), &args); err != nil ||
			args.ConversationRef != conversationRef {
			return ErrCommunicationActionConflict
		}
	case CommunicationActionInterviewInvite:
		var args protocol.ChatSendInviteCardArgs
		if err := json.Unmarshal([]byte(command.Args), &args); err != nil ||
			args.ConversationRef != conversationRef ||
			action.InterviewStartsAtMs == nil ||
			action.InterviewEndsAtMs == nil ||
			action.InterviewMethod == nil ||
			*action.InterviewEndsAtMs !=
				*action.InterviewStartsAtMs+communication.V4InterviewDurationMs ||
			args.Interview.StartsAt != *action.InterviewStartsAtMs ||
			args.Interview.EndsAt != *action.InterviewEndsAtMs ||
			string(args.Interview.Method) != *action.InterviewMethod {
			return ErrCommunicationActionConflict
		}
	default:
		return ErrCommunicationActionInvalid
	}
	return nil
}

func validateM5ActionDependencyTx(
	tx *gorm.DB,
	turn DialogueTurn,
	action CommunicationAction,
	previousIntentID string,
	childIntent *EffectIntent,
) error {
	dependency, err := validateM5DependentActionCurrentTx(tx, turn, action)
	if err != nil {
		return err
	}
	if childIntent == nil {
		return ErrCommunicationActionConflict
	}
	if previousIntentID != dependency.intent.IntentID {
		return ErrEffectIntentCASConflict
	}
	head, latest, err := effectIntentHeadTx(
		tx,
		childIntent.Platform,
		childIntent.AccountRef,
		childIntent.TargetRef,
	)
	if err != nil {
		return err
	}
	if head == nil ||
		latest == nil ||
		head.LatestIntentID != dependency.intent.IntentID ||
		latest.IntentID != dependency.intent.IntentID {
		return ErrDialogueTurnBinding
	}
	return nil
}

type m5DependentActionCurrent struct {
	intent  EffectIntent
	message Message
}

// validateM5DependentActionCurrentTx is the narrow current evaluator for the
// card half of an approved text→card combination. The original inbound turn
// boundary is expected to be behind the confirmed parent text at this point;
// only that positive parent fact and the exact active ledger tail authorize
// continuation.
func validateM5DependentActionCurrentTx(
	tx *gorm.DB,
	turn DialogueTurn,
	action CommunicationAction,
) (m5DependentActionCurrent, error) {
	var out m5DependentActionCurrent
	if action.DependsOnActionID == nil || strings.TrimSpace(*action.DependsOnActionID) == "" {
		return out, ErrCommunicationActionConflict
	}
	var planned []CommunicationAction
	if err := tx.Where(
		"turn_id = ? AND status = ?",
		turn.TurnID,
		CommunicationActionPlanned,
	).Find(&planned).Error; err != nil {
		return out, err
	}
	if len(planned) != 1 || planned[0].ActionID != action.ActionID {
		return out, ErrCommunicationActionConflict
	}
	if _, v4Turn, err := communicationV4PlannedActionTx(tx, turn, action); err != nil {
		return out, err
	} else if !v4Turn {
		return out, ErrCommunicationActionConflict
	}
	var parent CommunicationAction
	if err := tx.First(&parent, "action_id = ?", *action.DependsOnActionID).Error; err != nil {
		return out, err
	}
	if parent.TurnID != turn.TurnID ||
		parent.Kind != CommunicationActionReplyText ||
		parent.Status != CommunicationActionSent ||
		parent.EffectIntentID == nil ||
		parent.SentAt == nil {
		return out, ErrCommunicationActionConflict
	}
	var intent EffectIntent
	if err := tx.First(&intent, "intent_id = ?", *parent.EffectIntentID).Error; err != nil {
		return out, err
	}
	if intent.Status != EffectIntentOk && intent.Status != EffectIntentResolvedOk {
		return out, ErrCommunicationActionConflict
	}
	var message Message
	if err := tx.First(&message, "outbound_intent_id = ?", intent.IntentID).Error; err != nil {
		return out, err
	}
	if message.RetractedAt != nil ||
		message.Direction != "out" ||
		message.Kind != "text" ||
		message.ContentHash != parent.ContentHash {
		return out, ErrCommunicationActionConflict
	}
	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", turn.ProfileID).Error; err != nil {
		return out, err
	}
	if profile.ConversationRef == nil || *profile.ConversationRef != turn.ConversationRef {
		return out, ErrDialogueTurnBinding
	}
	var activeTail int64
	if err := tx.Model(&Message{}).
		Where(
			"platform = ? AND account_ref = ? AND conversation_ref = ? AND retracted_at IS NULL",
			profile.Platform,
			profile.AccountRef,
			turn.ConversationRef,
		).
		Select("COALESCE(MAX(seq), 0)").
		Scan(&activeTail).Error; err != nil {
		return out, err
	}
	if activeTail != message.Seq {
		return out, ErrDialogueTurnBinding
	}
	confirmed, found, err := communicationV4ApplicationTx(
		tx,
		turn.ProfileID,
		CommunicationV4InputConfirmedAction,
		parent.ActionID,
	)
	if err != nil {
		return out, err
	}
	if !found ||
		confirmed.SemanticKind == "" ||
		confirmed.MessageSeq != message.Seq {
		return out, ErrCommunicationActionConflict
	}
	aggregate, err := communicationV4AggregateTx(tx, turn.ProfileID)
	if err != nil {
		return out, err
	}
	if aggregate.Revision != confirmed.ToRevision ||
		aggregate.ProjectedThroughSeq != message.Seq ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive {
		return out, ErrDialogueTurnBinding
	}
	out.intent = intent
	out.message = message
	return out, nil
}

func (s *Store) ValidateM5AutomaticIntentLink(actionID, intentID string) error {
	if strings.TrimSpace(actionID) == "" || strings.TrimSpace(intentID) == "" {
		return ErrCommunicationActionInvalid
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var intent EffectIntent
		if err := tx.First(&intent, "intent_id = ?", intentID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrEffectIntentNotFound
			}
			return err
		}
		return validateM5AutomaticIntentLinkTx(tx, actionID, intent)
	})
}

// MarkM5AutomaticActionManualRequired closes only a pre-WAL action. Once the
// action is linked to an EffectIntent, the existing effect safety rail alone
// owns its terminal outcome and this method deliberately becomes a no-op.
func (s *Store) MarkM5AutomaticActionManualRequired(actionID, reason string, at time.Time) error {
	if strings.TrimSpace(actionID) == "" || strings.TrimSpace(reason) == "" {
		return ErrCommunicationActionInvalid
	}
	if at.IsZero() {
		at = time.Now()
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var action CommunicationAction
		if err := tx.First(&action, "action_id = ?", actionID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCommunicationActionInvalid
			}
			return err
		}
		if action.EffectIntentID != nil || action.Status == CommunicationActionEffectPending ||
			action.Status == CommunicationActionSent {
			return nil
		}
		if action.Status == CommunicationActionManualRequired {
			return nil
		}
		if action.Status != CommunicationActionPlanned {
			return ErrCommunicationActionConflict
		}
		var turn DialogueTurn
		if err := tx.First(&turn, "turn_id = ?", action.TurnID).Error; err != nil {
			return err
		}
		return markDialogueTurnManualTx(tx, &turn, reason, at)
	})
}

func communicationV4WechatContinuationForAcceptedActionTx(
	tx *gorm.DB,
	action CommunicationV4EventAction,
	intent EffectIntent,
	asset ContactAsset,
) (*communicationV4WechatContinuation, bool, error) {
	if tx == nil ||
		action.V4Kind != communication.V4ActionAcceptWechat ||
		action.EffectKind != CommunicationV4EventEffectAcceptWechat ||
		action.SourceInputKind != CommunicationV4InputDialogueTurn ||
		strings.TrimSpace(action.SourceInputKey) == "" ||
		action.ProfileID == "" ||
		action.EffectIntentID == nil ||
		*action.EffectIntentID != intent.IntentID ||
		asset.EffectIntentID == nil ||
		*asset.EffectIntentID != intent.IntentID {
		return nil, false, nil
	}
	initial, found, err := communicationV4ApplicationTx(
		tx,
		action.ProfileID,
		CommunicationV4InputDialogueTurn,
		action.SourceInputKey,
	)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, ErrCommunicationV4Corrupt
	}
	if initial.Outcome.Dialogue != communication.V4DialogueWechatContinuation ||
		!initial.Outcome.DialogueAfterActions ||
		initial.Outcome.DialogueStatus != communication.V4DialogueWaitingPrerequisite ||
		initial.Outcome.NextAdvice != communication.V4AdviceNone ||
		initial.Outcome.IntentLabel != m5ai.IntentInterested ||
		initial.Outcome.IntentSource != communication.IntentSourceBusinessEvent {
		return nil, false, ErrCommunicationV4Corrupt
	}
	var turn DialogueTurn
	if err := tx.First(&turn, "turn_id = ?", action.SourceInputKey).Error; err != nil {
		return nil, false, err
	}
	if turn.ProfileID != action.ProfileID {
		return nil, false, ErrCommunicationV4Corrupt
	}
	_, alreadyConfirmed, err := communicationV4ApplicationTx(
		tx,
		action.ProfileID,
		CommunicationV4InputConfirmedAction,
		action.SemanticActionKey,
	)
	if err != nil {
		return nil, false, err
	}
	if !alreadyConfirmed &&
		(turn.Status != DialogueTurnCollected ||
			turn.IntentLabel != m5ai.IntentInterested ||
			turn.IntentSource != DialogueIntentBusinessEvent) {
		return nil, false, ErrCommunicationV4Corrupt
	}
	return &communicationV4WechatContinuation{
		Turn:                 turn,
		ExpectedFromRevision: initial.ToRevision,
	}, true, nil
}

func applyCommunicationV4EventActionEffectStatusTx(
	tx *gorm.DB,
	action CommunicationV4EventAction,
	intent *EffectIntent,
	at time.Time,
) error {
	if err := validateCommunicationV4EventActionIntentLinkTx(
		tx,
		action,
		*intent,
	); err != nil {
		return err
	}
	sourceInfo, err := communicationV4EventActionSourceTx(tx, action)
	if err != nil {
		return err
	}
	if at.IsZero() {
		at = time.Now()
	}
	switch intent.Status {
	case EffectIntentDispatching, EffectIntentReconciling, EffectIntentVerifying:
		return nil
	case EffectIntentOk, EffectIntentResolvedOk:
		if action.EffectKind == CommunicationV4EventEffectAcceptWechat {
			if action.V4Kind != communication.V4ActionAcceptWechat ||
				intent.ResultMessageSeq != nil {
				return ErrCommunicationActionConflict
			}
			asset, err := contactAssetByEffectIntentTx(tx, intent.IntentID)
			if err != nil {
				return err
			}
			if asset == nil ||
				asset.ProfileID != action.ProfileID ||
				asset.Platform != intent.Platform ||
				asset.AccountRef != intent.AccountRef ||
				asset.ConversationRef != intent.TargetRef ||
				asset.Kind != contactAssetKindWechat {
				return ErrCommunicationActionConflict
			}
			sentAt := action.SentAt
			if sentAt == nil {
				sentAt = &at
			}
			updated := tx.Model(&CommunicationV4EventAction{}).
				Where("action_id = ? AND status = ?", action.ActionID, action.Status).
				Updates(map[string]any{
					"status":         CommunicationV4EventActionSent,
					"failure_reason": "",
					"sent_at":        sentAt,
					"updated_at":     at,
				})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrCommunicationActionConflict
			}
			confirmed := communication.V4ConfirmedAction{
				ActionKey:      action.SemanticActionKey,
				Kind:           action.V4Kind,
				MessageSeq:     0,
				CardMessageSeq: action.CardMessageSeq,
				SentAt:         sentAt,
			}
			continuation, continuationReady, err :=
				communicationV4WechatContinuationForAcceptedActionTx(
					tx,
					action,
					*intent,
					*asset,
				)
			if err != nil {
				return err
			}
			if continuationReady {
				_, _, _, err = applyCommunicationV4ConfirmedActionWithContinuationTx(
					tx,
					action.ProfileID,
					confirmed,
					continuation,
					at,
				)
				return err
			}
			_, _, _, err = applyCommunicationV4ConfirmedActionTx(
				tx,
				action.ProfileID,
				confirmed,
				at,
			)
			if err != nil {
				return err
			}
			// Historical/business-event-only sources have no frozen dialogue
			// turn to continue. Keep the old conservative terminal for those
			// rows instead of inventing a new AI input boundary.
			return markCommunicationV4AutomationManualTx(
				tx, action.ProfileID, string(communication.V4ManualWechatContinuation), at,
			)
		}
		if intent.ResultMessageSeq == nil {
			return ErrCommunicationActionConflict
		}
		var message Message
		if err := tx.First(
			&message,
			"outbound_intent_id = ?",
			intent.IntentID,
		).Error; err != nil {
			return err
		}
		if message.RetractedAt != nil ||
			message.Seq != *intent.ResultMessageSeq ||
			!communicationV4EventActionMatchesMessage(action, message) {
			return ErrCommunicationActionConflict
		}
		sentAt := action.SentAt
		if sentAt == nil {
			sentAt = &at
		}
		updated := tx.Model(&CommunicationV4EventAction{}).
			Where("action_id = ? AND status = ?", action.ActionID, action.Status).
			Updates(map[string]any{
				"status":         CommunicationV4EventActionSent,
				"failure_reason": "",
				"sent_at":        sentAt,
				"updated_at":     at,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrCommunicationActionConflict
		}
		confirmedAt := sentAt
		if message.TsApproxMs != nil {
			value := time.UnixMilli(*message.TsApproxMs).UTC()
			confirmedAt = &value
		}
		_, _, _, err = applyCommunicationV4ConfirmedActionTx(
			tx,
			action.ProfileID,
			communication.V4ConfirmedAction{
				ActionKey:      action.SemanticActionKey,
				Kind:           action.V4Kind,
				MessageSeq:     message.Seq,
				CardMessageSeq: action.CardMessageSeq,
				SentAt:         confirmedAt,
				Round:          sourceInfo.Round,
				Stage:          sourceInfo.Stage,
			},
			at,
		)
		if err != nil {
			return err
		}
		if action.SourceInputKind != CommunicationV4InputSchedulePlan {
			return nil
		}
		plan, found, err := communicationV4SchedulePlanTx(
			tx,
			action.SourceInputKey,
		)
		if err != nil {
			return err
		}
		if !found ||
			action.SourceOrdinal < 0 ||
			action.SourceOrdinal >= len(plan.PlannedActions) ||
			!communicationV4ScheduleEventActionMatches(
				action,
				plan,
				plan.PlannedActions[action.SourceOrdinal],
				action.SourceOrdinal,
			) {
			return ErrCommunicationActionConflict
		}
		nextOrdinal := action.SourceOrdinal + 1
		if nextOrdinal >= len(plan.PlannedActions) {
			return nil
		}
		_, _, err = materializeCommunicationV4ScheduleActionTx(
			tx,
			plan,
			nextOrdinal,
			at,
		)
		return err
	case EffectIntentFailed, EffectIntentSuspect, EffectIntentResolvedFailed:
		reason := "effectFailed"
		if intent.Status == EffectIntentSuspect {
			reason = "effectSuspect"
		} else if intent.Status == EffectIntentResolvedFailed {
			reason = "effectResolvedFailed"
		}
		if action.Status == CommunicationV4EventActionSent {
			var retracted Message
			if err := tx.First(
				&retracted,
				"outbound_intent_id = ?",
				intent.IntentID,
			).Error; err != nil {
				return err
			}
			if retracted.RetractedAt == nil ||
				!communicationV4EventActionMatchesMessage(action, retracted) {
				return ErrCommunicationActionConflict
			}
			if _, _, _, err := retractCommunicationV4ConfirmedActionTx(
				tx,
				action.ProfileID,
				communication.V4ConfirmedAction{
					ActionKey:      action.SemanticActionKey,
					Kind:           action.V4Kind,
					MessageSeq:     retracted.Seq,
					CardMessageSeq: action.CardMessageSeq,
				},
				reason,
				at,
			); err != nil {
				return err
			}
		}
		updated := tx.Model(&CommunicationV4EventAction{}).
			Where("action_id = ? AND status = ?", action.ActionID, action.Status).
			Updates(map[string]any{
				"status":         CommunicationV4EventActionManualRequired,
				"failure_reason": reason,
				"sent_at":        nil,
				"updated_at":     at,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrCommunicationActionConflict
		}
		aggregate, err := communicationV4AggregateTx(tx, action.ProfileID)
		if err != nil {
			return err
		}
		if aggregate.AutomationStatus == ProfileCommunicationAutomationManualRequired {
			return nil
		}
		return markCommunicationV4AutomationManualTx(
			tx,
			action.ProfileID,
			reason,
			at,
		)
	default:
		return ErrCommunicationActionConflict
	}
}

func communicationV4EventActionMatchesMessage(
	action CommunicationV4EventAction,
	message Message,
) bool {
	if message.Direction != "out" ||
		message.ContentHash != action.ContentHash {
		return false
	}
	switch action.EffectKind {
	case CommunicationV4EventEffectReplyText:
		return message.Kind == "text"
	case CommunicationV4EventEffectInviteWechat:
		return message.Kind == "card" &&
			message.CardType == "wechatExchange" &&
			message.CardState == "pending" &&
			message.InterviewStartsAtMs == nil &&
			message.InterviewEndsAtMs == nil &&
			message.InterviewMethod == nil
	default:
		return false
	}
}

// applyM5AutomaticEffectStatusTx mirrors the authoritative EffectIntent
// terminal into its optional M5 action. Callers already own the transaction
// that writes Cmd, EffectIntent and (for success) the unique self Message.
func applyM5AutomaticEffectStatusTx(tx *gorm.DB, intent *EffectIntent, at time.Time) error {
	if tx == nil || intent == nil || intent.IntentID == "" {
		return ErrEffectIntentNotFound
	}
	var action CommunicationAction
	legacyErr := tx.First(&action, "effect_intent_id = ?", intent.IntentID).Error
	var eventAction CommunicationV4EventAction
	eventErr := tx.First(
		&eventAction,
		"effect_intent_id = ?",
		intent.IntentID,
	).Error
	if legacyErr != nil && !errors.Is(legacyErr, gorm.ErrRecordNotFound) {
		return legacyErr
	}
	if eventErr != nil && !errors.Is(eventErr, gorm.ErrRecordNotFound) {
		return eventErr
	}
	legacyFound := legacyErr == nil
	eventFound := eventErr == nil
	if legacyFound && eventFound {
		return ErrCommunicationActionConflict
	}
	if eventFound {
		return applyCommunicationV4EventActionEffectStatusTx(
			tx,
			eventAction,
			intent,
			at,
		)
	}
	if !legacyFound {
		return nil
	}
	if err := validateM5AutomaticIntentLinkTx(tx, action.ActionID, *intent); err != nil {
		return err
	}
	var turn DialogueTurn
	if err := tx.First(&turn, "turn_id = ?", action.TurnID).Error; err != nil {
		return err
	}
	v4Plan, v4Turn, err := communicationV4PlannedActionTx(tx, turn, action)
	if err != nil {
		return err
	}
	var selection M5TrialSelection
	if !v4Turn {
		if err := tx.Where("profile_id = ?", turn.ProfileID).
			Order("selected_at DESC, selection_id DESC").First(&selection).Error; err != nil {
			return err
		}
	}
	if at.IsZero() {
		at = time.Now()
	}
	switch intent.Status {
	case EffectIntentDispatching, EffectIntentReconciling, EffectIntentVerifying:
		return nil
	case EffectIntentOk, EffectIntentResolvedOk:
		if intent.ResultMessageSeq == nil {
			return ErrCommunicationActionConflict
		}
		var message Message
		if err := tx.First(&message, "outbound_intent_id = ?", intent.IntentID).Error; err != nil {
			return err
		}
		if message.RetractedAt != nil ||
			message.Seq != *intent.ResultMessageSeq ||
			!communicationActionMatchesMessage(action, message) {
			return ErrCommunicationActionConflict
		}
		switch turn.Status {
		case DialogueTurnDispatching, DialogueTurnManualRequired, DialogueTurnCompleted:
		default:
			return ErrDialogueTurnState
		}
		if !v4Turn {
			switch selection.Status {
			case M5TrialSelectionActive, M5TrialSelectionManualRequired, M5TrialSelectionCompleted:
			default:
				return ErrDialogueTurnState
			}
		}
		sentAt := action.SentAt
		if sentAt == nil {
			sentAt = &at
		}
		updated := tx.Model(&CommunicationAction{}).
			Where("action_id = ? AND status = ?", action.ActionID, action.Status).
			Updates(map[string]any{
				"status": CommunicationActionSent, "failure_reason": "", "sent_at": sentAt,
				"updated_at": at,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrCommunicationActionConflict
		}
		if v4Turn {
			confirmedAt := sentAt
			if message.TsApproxMs != nil {
				value := time.UnixMilli(*message.TsApproxMs).UTC()
				confirmedAt = &value
			}
			// A platform timestamp is preferable when available. Otherwise the
			// brain-side positive-evidence confirmation time is a conservative
			// silence anchor: it can only delay a due action, never make one
			// fire early. The Message timestamp remains nil because this does
			// not claim to be the platform's send time.
			_, _, _, err := applyCommunicationV4ConfirmedActionTx(
				tx,
				turn.ProfileID,
				communication.V4ConfirmedAction{
					ActionKey: v4Plan.ActionKey, Kind: v4Plan.Kind,
					MessageSeq: message.Seq, CardMessageSeq: v4Plan.CardMessageSeq,
					SentAt: confirmedAt, Round: v4Plan.Round, Stage: v4Plan.Stage,
				},
				at,
			)
			if err != nil {
				return err
			}
			nextPlan, err := nextCommunicationV4PlanTx(tx, turn, v4Plan.ActionKey)
			if err != nil {
				return err
			}
			nextStatus := DialogueTurnCompleted
			if nextPlan != nil {
				if err := materializeDependentCommunicationActionTx(
					tx,
					turn,
					action,
					*nextPlan,
					at,
				); err != nil {
					return err
				}
				nextStatus = DialogueTurnAdviceReady
			}
			updated = tx.Model(&DialogueTurn{}).
				Where("turn_id = ? AND status = ?", turn.TurnID, turn.Status).
				Updates(map[string]any{
					"status": nextStatus, "failure_reason": "", "updated_at": at,
				})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrDialogueTurnConflict
			}
			return nil
		}
		updated = tx.Model(&DialogueTurn{}).
			Where("turn_id = ? AND status = ?", turn.TurnID, turn.Status).
			Updates(map[string]any{
				"status": DialogueTurnCompleted, "failure_reason": "", "updated_at": at,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrDialogueTurnConflict
		}
		endedAt := selection.EndedAt
		if endedAt == nil || selection.Status != M5TrialSelectionCompleted {
			endedAt = &at
		}
		updated = tx.Model(&M5TrialSelection{}).
			Where("selection_id = ? AND status = ?", selection.SelectionID, selection.Status).
			Updates(map[string]any{
				"status": M5TrialSelectionCompleted, "active_slot": nil,
				"reason": "automaticReplySentResidualManual", "ended_at": endedAt,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrDialogueTurnConflict
		}
		return nil
	case EffectIntentFailed, EffectIntentSuspect, EffectIntentResolvedFailed:
		switch turn.Status {
		case DialogueTurnDispatching, DialogueTurnManualRequired, DialogueTurnCompleted:
		default:
			return ErrDialogueTurnState
		}
		if !v4Turn {
			switch selection.Status {
			case M5TrialSelectionActive, M5TrialSelectionManualRequired, M5TrialSelectionCompleted:
			default:
				return ErrDialogueTurnState
			}
		}
		reason := "effectFailed"
		if intent.Status == EffectIntentSuspect {
			reason = "effectSuspect"
		} else if intent.Status == EffectIntentResolvedFailed {
			reason = "effectResolvedFailed"
		}
		if v4Turn && action.Status == CommunicationActionSent {
			var retracted Message
			if err := tx.First(
				&retracted,
				"outbound_intent_id = ?",
				intent.IntentID,
			).Error; err != nil {
				return err
			}
			if retracted.RetractedAt == nil ||
				!communicationActionMatchesMessage(action, retracted) {
				return ErrCommunicationActionConflict
			}
			if _, _, _, err := retractCommunicationV4ConfirmedActionTx(
				tx,
				turn.ProfileID,
				communication.V4ConfirmedAction{
					ActionKey: v4Plan.ActionKey, Kind: v4Plan.Kind,
					MessageSeq: retracted.Seq, CardMessageSeq: v4Plan.CardMessageSeq,
					Round: v4Plan.Round, Stage: v4Plan.Stage,
				},
				reason,
				at,
			); err != nil {
				return err
			}
		}
		updated := tx.Model(&CommunicationAction{}).
			Where("action_id = ? AND status = ?", action.ActionID, action.Status).
			Updates(map[string]any{
				"status": CommunicationActionManualRequired, "failure_reason": reason,
				"sent_at": nil, "updated_at": at,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrCommunicationActionConflict
		}
		updated = tx.Model(&DialogueTurn{}).
			Where("turn_id = ? AND status = ?", turn.TurnID, turn.Status).
			Updates(map[string]any{
				"status": DialogueTurnManualRequired, "failure_reason": reason, "updated_at": at,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrDialogueTurnConflict
		}
		if v4Turn {
			aggregate, err := communicationV4AggregateTx(tx, turn.ProfileID)
			if err != nil {
				return err
			}
			if aggregate.AutomationStatus == ProfileCommunicationAutomationManualRequired {
				return nil
			}
			return markCommunicationV4AutomationManualTx(tx, turn.ProfileID, reason, at)
		}
		endedAt := selection.EndedAt
		if endedAt == nil || selection.Status != M5TrialSelectionManualRequired {
			endedAt = &at
		}
		updated = tx.Model(&M5TrialSelection{}).
			Where("selection_id = ? AND status = ?", selection.SelectionID, selection.Status).
			Updates(map[string]any{
				"status": M5TrialSelectionManualRequired, "active_slot": nil,
				"reason": reason, "ended_at": endedAt,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrDialogueTurnConflict
		}
		return nil
	default:
		return ErrCommunicationActionConflict
	}
}

func applyM5AutomaticEffectStatusByIDTx(tx *gorm.DB, intentID string, at time.Time) error {
	var intent EffectIntent
	if err := tx.First(&intent, "intent_id = ?", intentID).Error; err != nil {
		return err
	}
	return applyM5AutomaticEffectStatusTx(tx, &intent, at)
}
