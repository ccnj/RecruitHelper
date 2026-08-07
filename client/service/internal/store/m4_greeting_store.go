package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

var (
	ErrCandidateGreetingCASConflict = errors.New("档案最新招呼意图已变化")
	ErrCandidateGreetingHeadCorrupt = errors.New("候选人招呼 head 缺失或损坏")
	ErrCandidateGreetingFrozen      = errors.New("候选人招呼意图仍在途、已成功或待裁决")
	ErrCandidateProfileNotSelected  = errors.New("候选人档案不在 selected 状态")
)

type CandidateGreetingCASConflictError struct {
	PreviousIntentID string
	Current          *EffectIntent
}

func (e *CandidateGreetingCASConflictError) Error() string {
	current := "<none>"
	previous := ""
	if e != nil {
		previous = e.PreviousIntentID
		if e.Current != nil {
			current = e.Current.IntentID
		}
	}
	return fmt.Sprintf("%s: previous=%q current=%q", ErrCandidateGreetingCASConflict, previous, current)
}

func (e *CandidateGreetingCASConflictError) Unwrap() error { return ErrCandidateGreetingCASConflict }

type CreateGreetingEffectIntentRequest struct {
	Intent           EffectIntent
	Command          CmdRecord
	PreviousIntentID string
	SourcingSource   *SourcingGreetingEffectSource
	Now              time.Time
}

// GreetingPreparation 是真人确认招呼前的只读快照。它只负责让脑从
// profile 账本派生线上 args/context；真正授权仍由
// CreateGreetingEffectIntentAndCmd 的单写事务重查。
type GreetingPreparation struct {
	Account Account
	Profile CandidateProfile
}

func (s *Store) PrepareGreeting(profileID string) (*GreetingPreparation, error) {
	if profileID == "" {
		return nil, ErrCandidateProfileNotFound
	}
	var out GreetingPreparation
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&out.Profile, "profile_id = ?", profileID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCandidateProfileNotFound
			}
			return err
		}
		if out.Profile.MainStatus != CandidateProfileSelected || out.Profile.EndReason != nil ||
			out.Profile.SuccessfulGreetingIntentID != nil || out.Profile.ConversationRef != nil ||
			out.Profile.GreetedAt != nil {
			if out.Profile.MainStatus != CandidateProfileSelected {
				return ErrCandidateProfileNotSelected
			}
			return ErrCandidateProfileState
		}
		if err := tx.First(&out.Account,
			"platform = ? AND account_ref = ?", out.Profile.Platform, out.Profile.AccountRef).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAccountNotFound
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateGreetingEffectIntentAndCmd 是真人确认招呼后的账本铸造事务。
// 它只建立 EffectIntent/CmdRecord/CandidateGreetingHead，不发送 socket；
// profile 成功推进与 conversation/message 绑定留给批次 4 的完整结果事务。
func (s *Store) CreateGreetingEffectIntentAndCmd(
	req CreateGreetingEffectIntentRequest,
) (*CreateEffectIntentResult, error) {
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	i, c := req.Intent, req.Command
	if err := normalizeAndValidateGreetingIntent(&i, &c, req.Now); err != nil {
		return nil, err
	}
	prepareRootCmd(&c)
	out := &CreateEffectIntentResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var existing EffectIntent
		err := tx.First(&existing, "intent_id = ?", i.IntentID).Error
		if err == nil {
			if !sameEffectIntentMaterial(existing, i) || existing.Primitive != primitiveChatSendGreeting {
				return ErrEffectIntentConflict
			}
			// 精确 HTTP 重试可以返回旧代 intent，但 head 自身必须仍然可证。
			if _, _, err := candidateGreetingHeadTx(tx, i.TargetRef); err != nil {
				return err
			}
			var existingCmd CmdRecord
			if err := tx.First(&existingCmd, "msg_id = ?", existing.RootMsgID).Error; err != nil {
				return fmt.Errorf("%w: 招呼意图根命令丢失", ErrEffectIntentConflict)
			}
			if req.SourcingSource != nil {
				if err := validateSourcingGreetingEffectReplayTx(
					tx, *req.SourcingSource, existing, existingCmd,
				); err != nil {
					return err
				}
			}
			out.Intent, out.Command, out.Created = existing, existingCmd, false
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		head, latest, err := candidateGreetingHeadTx(tx, i.TargetRef)
		if err != nil {
			return err
		}
		currentID := ""
		if latest != nil {
			currentID = latest.IntentID
		}
		if currentID != req.PreviousIntentID {
			return &CandidateGreetingCASConflictError{
				PreviousIntentID: req.PreviousIntentID,
				Current:          latest,
			}
		}
		if latest != nil && latest.Status != EffectIntentFailed && latest.Status != EffectIntentResolvedFailed {
			return ErrCandidateGreetingFrozen
		}
		if head != nil && head.Generation >= maxSQLiteEffectHeadGeneration {
			return fmt.Errorf("%w: generation 已达 SQLite 上限", ErrCandidateGreetingHeadCorrupt)
		}

		var profile CandidateProfile
		if req.SourcingSource != nil {
			material, err := validateSourcingGreetingEffectCreationTx(
				tx, *req.SourcingSource, i, c,
			)
			if err != nil {
				return err
			}
			profile = material.Profile
		} else if err := tx.First(&profile, "profile_id = ?", i.TargetRef).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCandidateProfileNotFound
			}
			return err
		}
		if profile.Platform != i.Platform || profile.AccountRef != i.AccountRef ||
			profile.MainStatus != CandidateProfileSelected || profile.EndReason != nil ||
			profile.SuccessfulGreetingIntentID != nil || profile.ConversationRef != nil || profile.GreetedAt != nil {
			if profile.MainStatus != CandidateProfileSelected {
				return ErrCandidateProfileNotSelected
			}
			return ErrCandidateProfileState
		}

		var account Account
		if err := tx.First(&account, "platform = ? AND account_ref = ?", i.Platform, i.AccountRef).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAccountNotFound
			}
			return err
		}
		if account.IdentityState != IdentityVerified || account.PrincipalFingerprint == nil ||
			*account.PrincipalFingerprint == "" || account.BoundHandID != c.HandID ||
			account.IdentitySession != c.Session || account.IdentityBootID != c.BootIDAtDispatch ||
			*account.PrincipalFingerprint != c.ExpectedPrincipalFingerprint {
			return ErrAccountIdentityNotCurrent
		}
		var busy int64
		if err := tx.Model(&CmdRecord{}).Where("domain = ? AND status IN ?", c.Domain, nonTerminalStatuses).
			Count(&busy).Error; err != nil {
			return err
		}
		if busy != 0 {
			return ErrDomainBusy
		}

		if err := tx.Create(&i).Error; err != nil {
			return err
		}
		if err := createRootCmd(tx, &c); err != nil {
			return err
		}
		if head == nil {
			head = &CandidateGreetingHead{
				ProfileID: i.TargetRef, LatestIntentID: i.IntentID, Generation: 1,
			}
			if err := tx.Create(head).Error; err != nil {
				return err
			}
		} else {
			nextGeneration := head.Generation + 1
			updated := tx.Model(&CandidateGreetingHead{}).
				Where("profile_id = ? AND latest_intent_id = ? AND generation = ?",
					head.ProfileID, head.LatestIntentID, head.Generation).
				Updates(map[string]any{"latest_intent_id": i.IntentID, "generation": nextGeneration})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrCandidateGreetingCASConflict
			}
		}
		if req.SourcingSource != nil {
			if err := bindSourcingGreetingEffectTx(
				tx, *req.SourcingSource, i.IntentID, req.Now,
			); err != nil {
				return err
			}
		}
		out.Intent, out.Command, out.Created = i, c, true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func normalizeAndValidateGreetingIntent(i *EffectIntent, c *CmdRecord, now time.Time) error {
	if i == nil || c == nil || i.IntentID == "" || i.IdemKey == "" || i.Platform == "" ||
		i.AccountRef == "" || i.Primitive != primitiveChatSendGreeting || i.TargetRef == "" ||
		i.PayloadHash == "" || i.GuardsHash == "" || i.SendFingerprint == "" ||
		c.MsgID == "" || c.IntentID != i.IntentID || c.IdemKey != i.IdemKey ||
		c.Platform != i.Platform || c.AccountRef != i.AccountRef || c.Name != i.Primitive ||
		c.Class != "effectful" || c.Domain != i.Platform+":"+i.AccountRef {
		return errors.New("招呼意图/命令缺少一致的必填字段")
	}
	if i.RootMsgID == "" {
		i.RootMsgID = c.MsgID
	}
	if i.RootMsgID != c.MsgID || i.ResultConversationRef != nil {
		return ErrEffectIntentConflict
	}
	if i.Status == "" {
		i.Status = EffectIntentDispatching
	}
	if i.Status != EffectIntentDispatching {
		return ErrEffectIntentConflict
	}
	if i.DeadlineMs <= now.UnixMilli() || c.DeadlineMs <= now.UnixMilli() {
		return errors.New("招呼意图已过期")
	}
	return nil
}

func candidateGreetingHeadTx(
	tx *gorm.DB, profileID string,
) (*CandidateGreetingHead, *EffectIntent, error) {
	var head CandidateGreetingHead
	err := tx.First(&head, "profile_id = ?", profileID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		var orphaned int64
		if countErr := tx.Model(&EffectIntent{}).
			Where("primitive = ? AND target_ref = ?", primitiveChatSendGreeting, profileID).
			Count(&orphaned).Error; countErr != nil {
			return nil, nil, countErr
		}
		if orphaned != 0 {
			return nil, nil, fmt.Errorf("%w: 发现 %d 条无 head 招呼意图", ErrCandidateGreetingHeadCorrupt, orphaned)
		}
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if head.LatestIntentID == "" || head.Generation == 0 {
		return nil, nil, fmt.Errorf("%w: 空 latestIntentId 或零 generation", ErrCandidateGreetingHeadCorrupt)
	}
	var intent EffectIntent
	if err := tx.First(&intent, "intent_id = ?", head.LatestIntentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, fmt.Errorf("%w: head 指向不存在的 intent %q", ErrCandidateGreetingHeadCorrupt, head.LatestIntentID)
		}
		return nil, nil, err
	}
	if intent.Primitive != primitiveChatSendGreeting || intent.TargetRef != profileID {
		return nil, nil, fmt.Errorf("%w: head 与招呼 intent 目标不一致", ErrCandidateGreetingHeadCorrupt)
	}
	return &head, &intent, nil
}

func (s *Store) LatestGreetingEffectIntent(profileID string) (*EffectIntent, error) {
	if profileID == "" {
		return nil, errors.New("查询最新招呼意图缺少 profileId")
	}
	_, intent, err := candidateGreetingHeadTx(s.db, profileID)
	return intent, err
}

const greetingTrackedRequestedBy = "system:greeting"

// GreetingResultMutation 是 chat.sendGreeting 的业务结果计划。Rejected
// 只用于 GREETING_REJECTED/none；成功允许先由可见关系正证推进 greeted，
// ConversationRef 仅在同次已取得稳定会话引用时携带。
type GreetingResultMutation struct {
	Rejected bool
	// RejectReason 是平台拒绝原话,只在 Rejected 时有意义;空串表示手侧没能
	// 给出原话,档案仍按 greetingFailed 收场,只是客户端少一句说明。
	RejectReason    string
	PlatformUserRef string
	PositionRef     string
	ConversationRef string
	Text            string
	ContentHash     string
	ObservedAtMs    int64
}

// applyGreetingResultTx 与通用 result WAL 事务共用同一个 tx。它不终结
// Cmd/EffectIntent；调用方在同一事务中完成那两项写入。
func applyGreetingResultTx(
	tx *gorm.DB,
	intent *EffectIntent,
	mutation GreetingResultMutation,
	at time.Time,
) (*Message, error) {
	if intent == nil || intent.Primitive != primitiveChatSendGreeting || intent.TargetRef == "" {
		return nil, ErrEffectIntentConflict
	}
	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", intent.TargetRef).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrCandidateProfileNotFound
		}
		return nil, err
	}
	if profile.Platform != intent.Platform || profile.AccountRef != intent.AccountRef {
		return nil, ErrEffectIntentConflict
	}

	if mutation.Rejected {
		if mutation.PlatformUserRef != "" || mutation.PositionRef != "" || mutation.ConversationRef != "" ||
			mutation.Text != "" || mutation.ContentHash != "" || mutation.ObservedAtMs != 0 {
			return nil, ErrEffectIntentConflict
		}
		if profile.MainStatus != CandidateProfileSelected || profile.EndReason != nil ||
			profile.SuccessfulGreetingIntentID != nil || profile.ConversationRef != nil || profile.GreetedAt != nil {
			return nil, ErrCandidateProfileState
		}
		reason := CandidateProfileEndGreetingFailed
		changes := map[string]any{"main_status": CandidateProfileEnded, "end_reason": reason}
		if rejectReason := strings.TrimSpace(mutation.RejectReason); rejectReason != "" {
			// 平台原话给客户端看,截断防止异常长文案撑爆投影。
			truncated, _ := truncateRunesForApp(rejectReason, 200)
			changes["greeting_reject_reason"] = truncated
		}
		updated := tx.Model(&CandidateProfile{}).
			Where("profile_id = ? AND main_status = ? AND end_reason IS NULL AND successful_greeting_intent_id IS NULL AND conversation_ref IS NULL AND greeted_at IS NULL",
				profile.ProfileID, CandidateProfileSelected).
			Updates(changes)
		if updated.Error != nil {
			return nil, updated.Error
		}
		if updated.RowsAffected != 1 {
			return nil, ErrCandidateProfileState
		}
		return nil, nil
	}

	if mutation.PlatformUserRef == "" || mutation.PositionRef == "" || mutation.Text == "" ||
		mutation.ContentHash == "" || intent.SendFingerprint != mutation.ContentHash ||
		profile.PlatformUserRef != mutation.PlatformUserRef || profile.PositionRef != mutation.PositionRef {
		return nil, ErrEffectIntentConflict
	}
	if profile.MainStatus == CandidateProfileGreeted && profile.SuccessfulGreetingIntentID != nil &&
		*profile.SuccessfulGreetingIntentID == intent.IntentID && mutation.ConversationRef == "" &&
		profile.ConversationRef == nil {
		if _, _, err := applyCommunicationV4RootTx(
			tx, profile.ProfileID, intent.IntentID, 0, at,
		); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if profile.MainStatus == CandidateProfileGreeted && profile.SuccessfulGreetingIntentID != nil &&
		*profile.SuccessfulGreetingIntentID == intent.IntentID && mutation.ConversationRef != "" &&
		profile.ConversationRef != nil && *profile.ConversationRef == mutation.ConversationRef {
		var existing Message
		if err := tx.First(&existing, "outbound_intent_id = ?", intent.IntentID).Error; err != nil {
			return nil, err
		}
		if _, _, err := applyCommunicationV4RootTx(
			tx, profile.ProfileID, intent.IntentID, existing.Seq, at,
		); err != nil {
			return nil, err
		}
		return &existing, nil
	}
	if profile.MainStatus != CandidateProfileSelected || profile.EndReason != nil ||
		profile.SuccessfulGreetingIntentID != nil || profile.ConversationRef != nil || profile.GreetedAt != nil {
		return nil, ErrCandidateProfileState
	}

	if mutation.ConversationRef == "" {
		greetedAt := at
		profileUpdated := tx.Model(&CandidateProfile{}).
			Where("profile_id = ? AND main_status = ? AND end_reason IS NULL AND successful_greeting_intent_id IS NULL AND conversation_ref IS NULL AND greeted_at IS NULL",
				profile.ProfileID, CandidateProfileSelected).
			Updates(map[string]any{
				"main_status":                   CandidateProfileGreeted,
				"successful_greeting_intent_id": intent.IntentID,
				"greeted_at":                    greetedAt,
			})
		if profileUpdated.Error != nil {
			return nil, profileUpdated.Error
		}
		if profileUpdated.RowsAffected != 1 {
			return nil, ErrCandidateProfileState
		}
		if _, _, err := applyCommunicationV4RootTx(
			tx, profile.ProfileID, intent.IntentID, 0, at,
		); err != nil {
			return nil, err
		}
		intent.ResultConversationRef = nil
		intent.ResultMessageSeq = nil
		intent.ResolvedAt = &at
		return nil, nil
	}

	var candidate Candidate
	if err := tx.First(&candidate, "platform = ? AND platform_user_ref = ?",
		profile.Platform, profile.PlatformUserRef).Error; err != nil {
		return nil, err
	}
	key := ConversationKey{
		Platform: profile.Platform, AccountRef: profile.AccountRef, ConversationRef: mutation.ConversationRef,
	}
	var conversation Conversation
	err := tx.Where(conversationWhere(key), conversationArgs(key)...).First(&conversation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		peerName := ""
		if candidate.DisplayName != nil {
			peerName = *candidate.DisplayName
		}
		conversation = Conversation{
			Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: key.ConversationRef,
			PlatformUserRef: profile.PlatformUserRef, PeerDisplayName: peerName,
			TrackingState: TrackingAdopted, AdoptedBoundarySeq: 0, LastMessageSeq: 0,
		}
		if err := tx.Create(&conversation).Error; err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else {
		if conversation.PlatformUserRef != profile.PlatformUserRef || conversation.LastMessageSeq != 0 ||
			conversation.TrackingState != TrackingUntracked {
			return nil, ErrCandidateProfileState
		}
	}

	var tracked TrackedIntent
	err = tx.Where(conversationWhere(key), conversationArgs(key)...).First(&tracked).Error
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		if err != nil {
			return nil, err
		}
		return nil, ErrCandidateProfileState
	}
	adoptedAt := at
	tracked = TrackedIntent{
		Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: key.ConversationRef,
		Status: TrackingAdopted, RequestedBy: greetingTrackedRequestedBy,
		RequestedAt: at, AdoptedAt: &adoptedAt,
	}
	if err := tx.Create(&tracked).Error; err != nil {
		return nil, err
	}

	intentID := intent.IntentID
	text := mutation.Text
	// ObservedAtMs 是招呼后置条件被观察到的时刻，不是平台消息的
	// 发送时刻；缺少平台时间证据时 self 消息时间保持未知。
	message := &Message{
		Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: key.ConversationRef,
		Seq: 1, Direction: "out", Kind: "text", ContentHash: mutation.ContentHash,
		Text: &text, TsApproxMs: nil, Origin: "self", OutboundIntentID: &intentID,
	}
	if err := tx.Create(message).Error; err != nil {
		return nil, err
	}
	conversationUpdates := map[string]any{
		"tracking_state": TrackingAdopted, "adopted_boundary_seq": int64(0),
		"last_message_seq": int64(1), "last_message_direction": "out",
		"last_message_kind": "text", "last_message_preview": mutation.Text,
		"last_synced_at": at,
	}
	if err := tx.Model(&Conversation{}).Where(conversationWhere(key), conversationArgs(key)...).
		Updates(conversationUpdates).Error; err != nil {
		return nil, err
	}

	greetedAt := at
	profileUpdated := tx.Model(&CandidateProfile{}).
		Where("profile_id = ? AND main_status = ? AND end_reason IS NULL AND successful_greeting_intent_id IS NULL AND conversation_ref IS NULL AND greeted_at IS NULL",
			profile.ProfileID, CandidateProfileSelected).
		Updates(map[string]any{
			"main_status":                   CandidateProfileGreeted,
			"successful_greeting_intent_id": intent.IntentID,
			"conversation_ref":              mutation.ConversationRef,
			"greeted_at":                    greetedAt,
		})
	if profileUpdated.Error != nil {
		return nil, profileUpdated.Error
	}
	if profileUpdated.RowsAffected != 1 {
		return nil, ErrCandidateProfileState
	}
	if _, _, err := applyCommunicationV4RootTx(
		tx, profile.ProfileID, intent.IntentID, message.Seq, at,
	); err != nil {
		return nil, err
	}
	conversationRef := mutation.ConversationRef
	intent.ResultConversationRef = &conversationRef
	intent.ResultMessageSeq = &message.Seq
	intent.ResolvedAt = &at
	return message, nil
}

type VerifiedGreetingSuccess struct {
	Ref              string
	ProfileID        string
	PlatformUserRef  string
	PositionRef      string
	ConversationRef  string
	Text             string
	ContentHash      string
	ObservedAtMs     int64
	ResultBody       string
	ResolutionReason string
	At               time.Time
}

// ResolveGreetingSuspectFailed 收编真人对未知招呼的“未发生”裁决。
// 招呼的 TargetRef 是 ProfileID，不是 conversationRef；因此这里只原子
// 终结原 Cmd/Intent，绝不进入会话消息账本。CandidateProfile 保持
// selected，真人之后可沿 greeting head 显式铸造新意图。
func (s *Store) ResolveGreetingSuspectFailed(ref string, at time.Time) error {
	if ref == "" {
		return ErrRecoveryStateConflict
	}
	if at.IsZero() {
		at = time.Now()
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var cmd CmdRecord
		if err := tx.First(&cmd, "msg_id = ?", ref).Error; err != nil {
			return err
		}
		if cmd.Name != primitiveChatSendGreeting || cmd.IntentID == "" || cmd.Status != CmdSuspect {
			return ErrRecoveryStateConflict
		}
		var intent EffectIntent
		if err := tx.First(&intent, "intent_id = ?", cmd.IntentID).Error; err != nil {
			return err
		}
		if intent.Primitive != primitiveChatSendGreeting || intent.Status != EffectIntentSuspect ||
			intent.IdemKey != cmd.IdemKey || intent.RootMsgID != cmd.LogicalDispatchID {
			return ErrEffectIntentConflict
		}

		cmd.Status = CmdResolvedFailed
		cmd.RecoveryAuthorized = false
		cmd.VerificationNextAt = nil
		cmd.VerificationChildMsgID = ""
		cmd.ReviewReady = false
		cmd.ReviewAfterMs = 0
		cmd.TerminalAt = &at
		if err := tx.Save(&cmd).Error; err != nil {
			return err
		}
		intent.Status = EffectIntentResolvedFailed
		intent.SuspectReason = cmd.SuspectReason
		intent.ResolvedAt = &at
		return tx.Save(&intent).Error
	})
}

// BeginGreetingManualVerification 把真人的 resolvedOk 操作解释为“一次
// 正证读取授权”，而不是“凭布尔值补写成功”。只有已经完成自动验证预算
// 的 review-ready suspect 才能进入；VerificationN 不重置，reason 作为
// 脑重启后仍可辨认的一次性在途标记。
func (s *Store) BeginGreetingManualVerification(ref, reason string, at time.Time) error {
	if ref == "" || reason == "" {
		return ErrRecoveryStateConflict
	}
	if at.IsZero() {
		at = time.Now()
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var cmd CmdRecord
		if err := tx.First(&cmd, "msg_id = ?", ref).Error; err != nil {
			return err
		}
		if cmd.Name != primitiveChatSendGreeting || cmd.IntentID == "" || cmd.Status != CmdSuspect ||
			!cmd.ReviewReady || cmd.VerificationChildMsgID != "" {
			return ErrRecoveryStateConflict
		}
		var intent EffectIntent
		if err := tx.First(&intent, "intent_id = ?", cmd.IntentID).Error; err != nil {
			return err
		}
		if intent.Primitive != primitiveChatSendGreeting || intent.Status != EffectIntentSuspect ||
			intent.IdemKey != cmd.IdemKey || intent.RootMsgID != cmd.LogicalDispatchID {
			return ErrEffectIntentConflict
		}

		cmd.Status = CmdVerifying
		cmd.VerificationReason = reason
		cmd.VerificationNextAt = &at
		cmd.RecoveryAuthorized = false
		cmd.ReviewReady = false
		cmd.TerminalAt = nil
		if err := tx.Save(&cmd).Error; err != nil {
			return err
		}
		intent.Status = EffectIntentVerifying
		intent.SuspectReason = reason
		return tx.Save(&intent).Error
	})
}

// RestoreGreetingManualVerificationSuspect 收束一次真人触发的阴性/失败
// 正证读取。它不增加 VerificationN、不安排下一轮，因而不会把三轮自动
// 验证预算偷偷扩成第四轮自动重试；再次读取只能由真人再次触发。
func (s *Store) RestoreGreetingManualVerificationSuspect(
	ref, expectedReason, reason string,
	at time.Time,
) error {
	if ref == "" || expectedReason == "" || reason == "" {
		return ErrRecoveryStateConflict
	}
	if at.IsZero() {
		at = time.Now()
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var cmd CmdRecord
		if err := tx.First(&cmd, "msg_id = ?", ref).Error; err != nil {
			return err
		}
		if cmd.Name != primitiveChatSendGreeting || cmd.IntentID == "" || cmd.Status != CmdVerifying ||
			cmd.VerificationReason != expectedReason {
			return ErrRecoveryStateConflict
		}
		var intent EffectIntent
		if err := tx.First(&intent, "intent_id = ?", cmd.IntentID).Error; err != nil {
			return err
		}
		if intent.Primitive != primitiveChatSendGreeting || intent.Status != EffectIntentVerifying ||
			intent.IdemKey != cmd.IdemKey || intent.RootMsgID != cmd.LogicalDispatchID {
			return ErrEffectIntentConflict
		}

		cmd.Status = CmdSuspect
		cmd.SuspectReason = reason
		cmd.VerificationReason = reason
		cmd.VerificationNextAt = nil
		cmd.VerificationChildMsgID = ""
		cmd.RecoveryAuthorized = false
		cmd.ReviewReady = true
		cmd.TerminalAt = &at
		if err := tx.Save(&cmd).Error; err != nil {
			return err
		}
		intent.Status = EffectIntentSuspect
		intent.SuspectReason = reason
		return tx.Save(&intent).Error
	})
}

// ResolveGreetingVerified 把配套验证读的唯一正证送入与直接 result
// 相同的 applyGreetingResultTx；Cmd、Intent、Profile、Conversation、
// TrackedIntent、Message 在同一个 SQLite 事务里收束。
func (s *Store) ResolveGreetingVerified(req VerifiedGreetingSuccess) (*Message, error) {
	if req.At.IsZero() {
		req.At = time.Now()
	}
	if req.Ref == "" || req.ProfileID == "" || req.PlatformUserRef == "" || req.PositionRef == "" ||
		req.Text == "" || req.ContentHash == "" {
		return nil, errors.New("招呼验证成功缺少关联字段")
	}
	var message *Message
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var cmd CmdRecord
		if err := tx.First(&cmd, "msg_id = ?", req.Ref).Error; err != nil {
			return err
		}
		if cmd.IntentID == "" {
			return ErrRecoveryStateConflict
		}
		if cmd.Status == CmdOk || cmd.Status == CmdResolvedOk {
			var existing Message
			if err := tx.First(&existing, "outbound_intent_id = ?", cmd.IntentID).Error; err != nil {
				return err
			}
			message = &existing
			return nil
		}
		if cmd.Status != CmdVerifying && cmd.Status != CmdPendingReconcile && cmd.Status != CmdSuspect {
			return ErrRecoveryStateConflict
		}
		var intent EffectIntent
		if err := tx.First(&intent, "intent_id = ?", cmd.IntentID).Error; err != nil {
			return err
		}
		if intent.TargetRef != req.ProfileID || intent.SendFingerprint != req.ContentHash ||
			intent.Primitive != primitiveChatSendGreeting {
			return ErrEffectIntentConflict
		}
		created, err := applyGreetingResultTx(tx, &intent, GreetingResultMutation{
			PlatformUserRef: req.PlatformUserRef, PositionRef: req.PositionRef,
			ConversationRef: req.ConversationRef, Text: req.Text,
			ContentHash: req.ContentHash, ObservedAtMs: req.ObservedAtMs,
		}, req.At)
		if err != nil {
			return err
		}
		message = created
		cmd.Status = CmdOk
		cmd.ResultBody = req.ResultBody
		cmd.SuspectReason = req.ResolutionReason
		cmd.SideEffect = "confirmed"
		cmd.TerminalAt = &req.At
		cmd.VerificationChildMsgID = ""
		if err := tx.Save(&cmd).Error; err != nil {
			return err
		}
		intent.Status = EffectIntentOk
		intent.SuspectReason = req.ResolutionReason
		intent.ResolvedAt = &req.At
		return tx.Save(&intent).Error
	})
	if err != nil {
		return nil, err
	}
	return message, nil
}
