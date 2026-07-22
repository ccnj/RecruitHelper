package store

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"recruithelper/client/service/internal/m5ai"
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
		if message.Direction != "in" || !isRealCandidateMessageKind(message.Kind) {
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

func isRealCandidateMessageKind(kind string) bool {
	switch kind {
	case "text", "image", "voice", "file":
		return true
	default:
		return false
	}
}

// FreezeDialogueTurn 在同一事务中重验活动消息尾、简历与职位 revision 绑定，
// 冻结唯一 turn，并在首次真实文字到达时推进 greeted→communicating。
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
		var binding ProfileAIContextBinding
		if err := tx.First(&binding, "profile_id = ? AND status = ?",
			req.ProfileID, ProfileAIContextBindingActive).Error; err != nil || binding.RevisionHash != req.ContextRevisionHash {
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
		for i := range inbound {
			if inbound[i].Direction != "in" || inbound[i].Kind != "text" || inbound[i].Text == nil {
				return ErrDialogueTurnBinding
			}
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
// 它不比较正文，只比较脑内不可变引用、活动消息边界和正式绑定。
func validateDialogueTurnCurrentTx(tx *gorm.DB, turn DialogueTurn) error {
	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", turn.ProfileID).Error; err != nil {
		return err
	}
	if profile.MainStatus != CandidateProfileCommunicating || profile.EndReason != nil ||
		profile.ConversationRef == nil || *profile.ConversationRef != turn.ConversationRef ||
		profile.ActiveResumeSnapshotID == nil || *profile.ActiveResumeSnapshotID != turn.ResumeSnapshotID ||
		profile.CommunicatingAt == nil || profile.FirstRealMessageSeq == nil {
		return ErrDialogueTurnBinding
	}
	var selection M5TrialSelection
	if err := tx.First(&selection, "profile_id = ? AND status = ? AND active_slot = ?",
		turn.ProfileID, M5TrialSelectionActive, m5TrialActiveSlot).Error; err != nil {
		return ErrDialogueTurnBinding
	}
	var binding ProfileAIContextBinding
	if err := tx.First(&binding, "profile_id = ? AND status = ?",
		turn.ProfileID, ProfileAIContextBindingActive).Error; err != nil || binding.RevisionHash != turn.ContextRevisionHash {
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
	var inbound []Message
	if err := tx.Where(
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq > ? AND seq <= ? AND retracted_at IS NULL",
		profile.Platform, profile.AccountRef, turn.ConversationRef, turn.HistoryThroughSeq, turn.InboundThroughSeq,
	).Order("seq").Find(&inbound).Error; err != nil {
		return err
	}
	if len(inbound) == 0 || inbound[0].Seq != turn.InboundFromSeq || inbound[len(inbound)-1].Seq != turn.InboundThroughSeq {
		return ErrDialogueTurnBinding
	}
	for i := range inbound {
		if inbound[i].Direction != "in" || inbound[i].Kind != "text" || inbound[i].Text == nil {
			return ErrDialogueTurnBinding
		}
	}
	if digest, _, err := DialogueTurnIdentity(turn.ProfileID, lastOutbound, inbound); err != nil ||
		digest != turn.InputDigest {
		return ErrDialogueTurnBinding
	}
	return nil
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
		return markM5TrialManualRequiredTx(tx, turn.ProfileID, reason, at)
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
	if err := markM5TrialManualRequiredTx(tx, turn.ProfileID, reason, at); err != nil {
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
			if err := markM5TrialManualRequiredTx(tx, out.ProfileID, failureReason, req.ClassifiedAt); err != nil {
				return err
			}
		}
		return tx.First(&out, "turn_id = ?", req.TurnID).Error
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
			if err := validateDialogueTurnCurrentTx(tx, turn); err != nil {
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
		if err := validateDialogueTurnCurrentTx(tx, turn); err != nil {
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
		var dailyCalls int64
		if err := tx.Model(&AIInvocation{}).
			Where("created_at >= ? AND created_at < ?", dayStart, nextDay).
			Count(&dailyCalls).Error; err != nil {
			return err
		}
		if dailyCalls >= m5DailyProviderCallLimit {
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
		if out.Status != DialogueTurnManualRequired {
			if err := validateDialogueTurnCurrentTx(tx, out); err != nil {
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
			if err := markM5TrialManualRequiredTx(tx, out.ProfileID, req.ManualReason, req.Completion.FinishedAt); err != nil {
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
	Text         string
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
		if turn.Status != DialogueTurnManualRequired {
			if err := validateDialogueTurnCurrentTx(tx, turn); err != nil {
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

func sameCommunicationAction(existing CommunicationAction, req CompleteReplyInvocationRequest) bool {
	return existing.ActionID == req.ActionID && existing.Kind == CommunicationActionReplyText &&
		existing.Text == req.Text && existing.ContentHash == req.ContentHash
}

func validateInvocationCompletion(completion AIInvocationCompletion) error {
	if strings.TrimSpace(completion.InvocationID) == "" || completion.FinishedAt.IsZero() || completion.LatencyMs < 0 ||
		completion.InputTokens < 0 || completion.CachedInputTokens < 0 || completion.OutputTokens < 0 ||
		completion.EstimatedCostMicros < 0 ||
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
		"error_class": completion.ErrorClass, "estimated_cost_micros": completion.EstimatedCostMicros,
		"finished_at": completion.FinishedAt,
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
		existing.ErrorClass == completion.ErrorClass && existing.EstimatedCostMicros == completion.EstimatedCostMicros &&
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
		if err := validateDialogueTurnCurrentTx(tx, turn); err != nil {
			if !errors.Is(err, ErrDialogueTurnBinding) {
				return err
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
	err := s.db.First(&action, "turn_id = ? AND kind = ?", turnID, CommunicationActionReplyText).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &action, nil
}

// bindM5AutomaticActionTx is the M5 intent-construction authorization point.
// It runs inside CreateEffectIntentAndCmd's WAL/head transaction, so an action
// can never become effectPending without the matching EffectIntent and Cmd.
func bindM5AutomaticActionTx(
	tx *gorm.DB,
	actionID string,
	intent *EffectIntent,
	command *CmdRecord,
	at time.Time,
) error {
	if tx == nil || intent == nil || command == nil || strings.TrimSpace(actionID) == "" {
		return ErrCommunicationActionInvalid
	}
	expectedIntentID, err := M5AutomaticIntentID(actionID)
	if err != nil || intent.IntentID != expectedIntentID || intent.Primitive != primitiveChatSendMessage ||
		command.IntentID != intent.IntentID || command.Name != primitiveChatSendMessage {
		return ErrCommunicationActionInvalid
	}
	var action CommunicationAction
	if err := tx.First(&action, "action_id = ?", actionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrCommunicationActionInvalid
		}
		return err
	}
	if action.Kind != CommunicationActionReplyText || action.Status != CommunicationActionPlanned ||
		action.EffectIntentID != nil || strings.TrimSpace(action.Text) == "" ||
		strings.TrimSpace(action.ContentHash) == "" || action.ContentHash != intent.SendFingerprint {
		return ErrCommunicationActionConflict
	}
	var turn DialogueTurn
	if err := tx.First(&turn, "turn_id = ?", action.TurnID).Error; err != nil {
		return err
	}
	if turn.Status != DialogueTurnAdviceReady || turn.ConversationRef != intent.TargetRef {
		return ErrDialogueTurnState
	}
	if err := validateDialogueTurnCurrentTx(tx, turn); err != nil {
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
	var args protocol.ChatSendMessageArgs
	if err := json.Unmarshal([]byte(command.Args), &args); err != nil ||
		args.ConversationRef != turn.ConversationRef || args.Text != action.Text {
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
	if err != nil || expectedIntentID != intent.IntentID || intent.Primitive != primitiveChatSendMessage {
		return ErrCommunicationActionInvalid
	}
	var action CommunicationAction
	if err := tx.First(&action, "action_id = ?", actionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrCommunicationActionInvalid
		}
		return err
	}
	if action.EffectIntentID == nil || *action.EffectIntentID != intent.IntentID ||
		action.EffectStartedAt == nil || action.Kind != CommunicationActionReplyText ||
		action.ContentHash != intent.SendFingerprint {
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

// applyM5AutomaticEffectStatusTx mirrors the authoritative EffectIntent
// terminal into its optional M5 action. Callers already own the transaction
// that writes Cmd, EffectIntent and (for success) the unique self Message.
func applyM5AutomaticEffectStatusTx(tx *gorm.DB, intent *EffectIntent, at time.Time) error {
	if tx == nil || intent == nil || intent.IntentID == "" {
		return ErrEffectIntentNotFound
	}
	var action CommunicationAction
	err := tx.First(&action, "effect_intent_id = ?", intent.IntentID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := validateM5AutomaticIntentLinkTx(tx, action.ActionID, *intent); err != nil {
		return err
	}
	var turn DialogueTurn
	if err := tx.First(&turn, "turn_id = ?", action.TurnID).Error; err != nil {
		return err
	}
	var selection M5TrialSelection
	if err := tx.Where("profile_id = ?", turn.ProfileID).
		Order("selected_at DESC, selection_id DESC").First(&selection).Error; err != nil {
		return err
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
		if message.RetractedAt != nil || message.Seq != *intent.ResultMessageSeq || message.Direction != "out" ||
			message.ContentHash != action.ContentHash {
			return ErrCommunicationActionConflict
		}
		switch turn.Status {
		case DialogueTurnDispatching, DialogueTurnManualRequired, DialogueTurnCompleted:
		default:
			return ErrDialogueTurnState
		}
		switch selection.Status {
		case M5TrialSelectionActive, M5TrialSelectionManualRequired, M5TrialSelectionCompleted:
		default:
			return ErrDialogueTurnState
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
		switch selection.Status {
		case M5TrialSelectionActive, M5TrialSelectionManualRequired, M5TrialSelectionCompleted:
		default:
			return ErrDialogueTurnState
		}
		reason := "effectFailed"
		if intent.Status == EffectIntentSuspect {
			reason = "effectSuspect"
		} else if intent.Status == EffectIntentResolvedFailed {
			reason = "effectResolvedFailed"
		}
		updated := tx.Model(&CommunicationAction{}).
			Where("action_id = ? AND status = ?", action.ActionID, action.Status).
			Updates(map[string]any{
				"status": CommunicationActionManualRequired, "failure_reason": reason,
				"updated_at": at,
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
