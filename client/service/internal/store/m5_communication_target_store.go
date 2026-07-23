package store

import (
	"errors"
	"strings"

	"recruithelper/client/service/internal/communication"
	"recruithelper/contract/gen/go/protocol"

	"gorm.io/gorm"
)

var (
	ErrCommunicationTargetInvalid  = errors.New("沟通目标输入无效")
	ErrCommunicationTargetConflict = errors.New("沟通目标事实冲突")
)

// CommunicationTarget 是账号 worker 处理事件层、既有动作恢复与沉默时刻表
// 所需的基础事实。AI 正文材料按需通过 CommunicationAIMaterialForProfile
// 加载，不能反向成为基础目标枚举的前置条件。
type CommunicationTarget struct {
	Profile      CandidateProfile
	Account      Account
	Conversation Conversation
	Aggregate    CommunicationV4Aggregate
}

// CommunicationAIMaterial 是冻结一个新 AI 对话轮所需的完整材料引用。
// 候选人正文和职位正文仍保留在各自业务事实中，不复制到调度记录。
type CommunicationAIMaterial struct {
	ContextBinding  ProfileAIContextBinding
	ContextRevision JobAIContextRevision
	ResumeSnapshot  CandidateResumeSnapshot
}

// CommunicationTargetsForAccount 先枚举账号下全部 V4 根，再逐档案区分：
// 人工、已淘汰或尚未绑定会话的档案正常跳过；已结束档案仍须进入事件层，
// 以便真实文字或旧邀面卡事实唤醒。AI 材料准备度不参与本查询。
//
// 本查询与 unread、dirty、当前消息轮以及旧 M5TrialSelection 均无关，
// 因而既能承载沉默时刻表，也能在重启后恢复已有 turn/action。
func (s *Store) CommunicationTargetsForAccount(key AccountKey) ([]CommunicationTarget, error) {
	if strings.TrimSpace(key.Platform) == "" || strings.TrimSpace(key.AccountRef) == "" {
		return nil, ErrCommunicationTargetInvalid
	}
	var targets []CommunicationTarget
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var profileIDs []string
		if err := tx.Table("communication_v4_aggregates AS v4").
			Select("v4.profile_id").
			Joins("JOIN candidate_profiles AS p ON p.profile_id = v4.profile_id").
			Where("p.platform = ? AND p.account_ref = ?", key.Platform, key.AccountRef).
			Order("v4.profile_id").
			Scan(&profileIDs).Error; err != nil {
			return err
		}
		targets = make([]CommunicationTarget, 0, len(profileIDs))
		for _, profileID := range profileIDs {
			target, ready, err := communicationTargetTx(tx, profileID)
			if err != nil {
				return err
			}
			if ready {
				targets = append(targets, target)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return targets, nil
}

func communicationTargetTx(tx *gorm.DB, profileID string) (CommunicationTarget, bool, error) {
	aggregate, err := communicationV4AggregateTx(tx, profileID)
	if err != nil {
		return CommunicationTarget{}, false, err
	}
	if aggregate.AutomationStatus != ProfileCommunicationAutomationActive {
		return CommunicationTarget{}, false, nil
	}
	switch aggregate.State.MainStatus {
	case communication.V4StatusGreeted, communication.V4StatusCommunicating,
		communication.V4StatusInvited, communication.V4StatusInterviewed,
		communication.V4StatusEnded:
	case communication.V4StatusEliminated:
		return CommunicationTarget{}, false, nil
	default:
		return CommunicationTarget{}, false, ErrCommunicationTargetConflict
	}

	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", profileID).Error; err != nil {
		return CommunicationTarget{}, false, ErrCommunicationTargetConflict
	}
	if profile.ConversationRef == nil || strings.TrimSpace(*profile.ConversationRef) == "" {
		return CommunicationTarget{}, false, nil
	}
	key := ConversationKey{
		Platform: profile.Platform, AccountRef: profile.AccountRef,
		ConversationRef: *profile.ConversationRef,
	}
	var account Account
	if err := tx.First(
		&account,
		"platform = ? AND account_ref = ?",
		profile.Platform,
		profile.AccountRef,
	).Error; err != nil {
		return CommunicationTarget{}, false, ErrCommunicationTargetConflict
	}
	var conversation Conversation
	if err := tx.Where(conversationWhere(key), conversationArgs(key)...).
		First(&conversation).Error; err != nil {
		return CommunicationTarget{}, false, ErrCommunicationTargetConflict
	}
	if conversation.PlatformUserRef != profile.PlatformUserRef ||
		conversation.TrackingState != TrackingAdopted ||
		conversation.LastMessageSeq < aggregate.ProjectedThroughSeq {
		return CommunicationTarget{}, false, ErrCommunicationTargetConflict
	}
	var tracked TrackedIntent
	if err := tx.Where(conversationWhere(key), conversationArgs(key)...).
		First(&tracked).Error; err != nil {
		return CommunicationTarget{}, false, ErrCommunicationTargetConflict
	}
	if tracked.Status != TrackingAdopted || tracked.AdoptedAt == nil {
		return CommunicationTarget{}, false, ErrCommunicationTargetConflict
	}
	if aggregate.ProjectedThroughSeq > 0 {
		var projected Message
		if err := tx.First(
			&projected,
			"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ? AND retracted_at IS NULL",
			key.Platform,
			key.AccountRef,
			key.ConversationRef,
			aggregate.ProjectedThroughSeq,
		).Error; err != nil {
			return CommunicationTarget{}, false, ErrCommunicationTargetConflict
		}
	}

	var greeting EffectIntent
	if err := tx.First(&greeting, "intent_id = ?", aggregate.RootGreetingIntentID).Error; err != nil {
		return CommunicationTarget{}, false, ErrCommunicationTargetConflict
	}
	if greeting.Primitive != protocol.PrimChatSendGreeting ||
		greeting.TargetRef != profile.ProfileID ||
		greeting.Platform != profile.Platform || greeting.AccountRef != profile.AccountRef ||
		(greeting.Status != EffectIntentOk && greeting.Status != EffectIntentResolvedOk) ||
		greeting.ResultConversationRef == nil || *greeting.ResultConversationRef != key.ConversationRef ||
		greeting.ResultMessageSeq == nil {
		return CommunicationTarget{}, false, ErrCommunicationTargetConflict
	}
	var greetingMessage Message
	if err := tx.First(
		&greetingMessage,
		"outbound_intent_id = ? AND retracted_at IS NULL",
		greeting.IntentID,
	).Error; err != nil {
		return CommunicationTarget{}, false, ErrCommunicationTargetConflict
	}
	if greetingMessage.Platform != key.Platform ||
		greetingMessage.AccountRef != key.AccountRef ||
		greetingMessage.ConversationRef != key.ConversationRef ||
		greetingMessage.Seq != *greeting.ResultMessageSeq ||
		greetingMessage.Direction != "out" || greetingMessage.Kind != "text" {
		return CommunicationTarget{}, false, ErrCommunicationTargetConflict
	}

	return CommunicationTarget{
		Profile: profile, Account: account, Conversation: conversation,
		Aggregate: aggregate,
	}, true, nil
}

// CommunicationAIMaterialForProfile loads AI material only when patrol is
// ready to freeze a new dialogue turn. A normal preparation gap returns
// ready=false; a fact that claims to be ready but is dangling or mismatched
// fails loudly.
func (s *Store) CommunicationAIMaterialForProfile(
	profileID string,
) (CommunicationAIMaterial, bool, error) {
	if strings.TrimSpace(profileID) == "" {
		return CommunicationAIMaterial{}, false, ErrCommunicationTargetInvalid
	}
	var material CommunicationAIMaterial
	ready := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		target, targetReady, err := communicationTargetTx(tx, profileID)
		if err != nil || !targetReady {
			return err
		}
		material, ready, err = communicationAIMaterialTx(tx, target)
		return err
	})
	if err != nil {
		return CommunicationAIMaterial{}, false, err
	}
	return material, ready, nil
}

func communicationAIMaterialTx(
	tx *gorm.DB,
	target CommunicationTarget,
) (CommunicationAIMaterial, bool, error) {
	profile := target.Profile
	key := ConversationKey{
		Platform: profile.Platform, AccountRef: profile.AccountRef,
		ConversationRef: target.Conversation.ConversationRef,
	}

	var binding ProfileAIContextBinding
	err := tx.First(
		&binding,
		"profile_id = ? AND status = ?",
		profile.ProfileID,
		ProfileAIContextBindingActive,
	).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CommunicationAIMaterial{}, false, nil
	}
	if err != nil {
		return CommunicationAIMaterial{}, false, err
	}
	var revision JobAIContextRevision
	if err := tx.First(&revision, "revision_hash = ?", binding.RevisionHash).Error; err != nil {
		return CommunicationAIMaterial{}, false, ErrCommunicationTargetConflict
	}
	if revision.ContextID != binding.ContextID {
		return CommunicationAIMaterial{}, false, ErrCommunicationTargetConflict
	}
	var sourcingInvocation SourcingGreetingInvocation
	err = tx.First(&sourcingInvocation, "profile_id = ?", profile.ProfileID).Error
	switch {
	case err == nil:
		if sourcingInvocation.Status != AIInvocationOK ||
			sourcingInvocation.FinishedAt == nil ||
			sourcingInvocation.EffectIntentID == nil ||
			*sourcingInvocation.EffectIntentID != target.Aggregate.RootGreetingIntentID ||
			sourcingInvocation.ContextRevisionHash != binding.RevisionHash {
			return CommunicationAIMaterial{}, false, ErrCommunicationTargetConflict
		}
	case errors.Is(err, gorm.ErrRecordNotFound):
		// 允许由 M5-A 真人绑定形成的非 M6 历史档案。
	default:
		return CommunicationAIMaterial{}, false, err
	}

	if profile.ResumeCaptureState != ResumeCaptureCaptured {
		return CommunicationAIMaterial{}, false, nil
	}
	if profile.ActiveResumeSnapshotID == nil ||
		profile.ResumeCaptureLogicalDispatchID == nil ||
		strings.TrimSpace(*profile.ResumeCaptureLogicalDispatchID) == "" {
		return CommunicationAIMaterial{}, false, ErrCommunicationTargetConflict
	}
	var snapshot CandidateResumeSnapshot
	if err := tx.First(
		&snapshot,
		"profile_id = ? AND snapshot_id = ?",
		profile.ProfileID,
		*profile.ActiveResumeSnapshotID,
	).Error; err != nil {
		return CommunicationAIMaterial{}, false, ErrCommunicationTargetConflict
	}
	if snapshot.SourceConversationRef != key.ConversationRef ||
		snapshot.SourceLogicalDispatchID != *profile.ResumeCaptureLogicalDispatchID ||
		snapshot.SchemaVersion != resumeSnapshotSchemaV1 ||
		strings.TrimSpace(snapshot.ContentHash) == "" ||
		strings.TrimSpace(snapshot.ResumeJSON) == "" {
		return CommunicationAIMaterial{}, false, ErrCommunicationTargetConflict
	}

	return CommunicationAIMaterial{
		ContextBinding: binding, ContextRevision: revision, ResumeSnapshot: snapshot,
	}, true, nil
}
