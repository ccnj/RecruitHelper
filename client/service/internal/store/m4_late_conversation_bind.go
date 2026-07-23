package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"recruithelper/contract/gen/go/protocol"

	"gorm.io/gorm"
)

const lateGreetingTrackedRequestedBy = "system:greeting-late-bind"

type LateGreetingConversationObservation struct {
	ConversationRef string
	PlatformUserRef string
}

type LateBindGreetedConversationsRequest struct {
	Platform      string
	AccountRef    string
	RoundID       string
	ObservedAt    time.Time
	Conversations []LateGreetingConversationObservation
}

// LateBindGreetedConversations 把“招呼关系正证已成立、但当时尚未取得会话引用”
// 的 greeted 档案，与本轮列表刚观察到的稳定平台身份做晚到回绑。只有同账号下
// platformUserRef 两侧都唯一、且原招呼成功事实仍未绑定时才推进；姓名、预览和
// resumeNumber 均不参与身份判断。
//
// 这里从已经安全终局的原招呼命令复原唯一 self 招呼事实，再直接建立
// adopted(boundary=1)，而不是 pending：我方招呼是已知历史边界，下一次 readThread
// 只把它之后的候选人消息作为新业务事件进入账本。
func (s *Store) LateBindGreetedConversations(req LateBindGreetedConversationsRequest) (int, error) {
	if req.Platform == "" || req.AccountRef == "" {
		return 0, errors.New("晚到回绑 platform/accountRef 不能为空")
	}
	if req.ObservedAt.IsZero() {
		req.ObservedAt = time.Now()
	}
	byPeer := make(map[string][]string, len(req.Conversations))
	seenConversation := make(map[string]struct{}, len(req.Conversations))
	for _, observation := range req.Conversations {
		if observation.ConversationRef == "" || observation.PlatformUserRef == "" {
			return 0, errors.New("晚到回绑 conversationRef/platformUserRef 不能为空")
		}
		if _, exists := seenConversation[observation.ConversationRef]; exists {
			return 0, fmt.Errorf("%w: %s", ErrDuplicateConversationEntry, observation.ConversationRef)
		}
		seenConversation[observation.ConversationRef] = struct{}{}
		byPeer[observation.PlatformUserRef] = append(byPeer[observation.PlatformUserRef], observation.ConversationRef)
	}

	bound := 0
	err := s.db.Transaction(func(tx *gorm.DB) error {
		key := AccountKey{Platform: req.Platform, AccountRef: req.AccountRef}
		if err := requireAccount(tx, key); err != nil {
			return err
		}
		if req.RoundID != "" {
			if err := requirePatrolRound(tx, req.Platform, req.AccountRef, req.RoundID); err != nil {
				return err
			}
		}

		var profiles []CandidateProfile
		if err := tx.Where(
			"platform = ? AND account_ref = ? AND main_status = ? AND end_reason IS NULL AND conversation_ref IS NULL",
			req.Platform, req.AccountRef, CandidateProfileGreeted,
		).Order("profile_id").Find(&profiles).Error; err != nil {
			return err
		}
		profilesByPeer := make(map[string][]CandidateProfile, len(profiles))
		for _, profile := range profiles {
			profilesByPeer[profile.PlatformUserRef] = append(profilesByPeer[profile.PlatformUserRef], profile)
		}

		for platformUserRef, matchingProfiles := range profilesByPeer {
			conversationRefs := byPeer[platformUserRef]
			// 档案或会话任一侧多义时都不猜测，也不改任何一侧事实。
			if len(matchingProfiles) != 1 || len(conversationRefs) != 1 {
				continue
			}
			profile := matchingProfiles[0]
			if profile.SuccessfulGreetingIntentID == nil || *profile.SuccessfulGreetingIntentID == "" ||
				profile.GreetedAt == nil {
				continue
			}

			conversationKey := ConversationKey{
				Platform: req.Platform, AccountRef: req.AccountRef, ConversationRef: conversationRefs[0],
			}
			var conversation Conversation
			if err := tx.Where(conversationWhere(conversationKey), conversationArgs(conversationKey)...).
				First(&conversation).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					continue
				}
				return err
			}
			if conversation.PlatformUserRef != platformUserRef ||
				conversation.TrackingState != TrackingUntracked || conversation.AdoptedBoundarySeq != 0 ||
				conversation.LastMessageSeq != 0 {
				continue
			}
			var messageN int64
			if err := tx.Model(&Message{}).
				Where(conversationWhere(conversationKey), conversationArgs(conversationKey)...).
				Count(&messageN).Error; err != nil {
				return err
			}
			if messageN != 0 {
				continue
			}

			var trackedN int64
			if err := tx.Model(&TrackedIntent{}).
				Where(conversationWhere(conversationKey), conversationArgs(conversationKey)...).
				Count(&trackedN).Error; err != nil {
				return err
			}
			if trackedN != 0 {
				continue
			}

			var occupiedN int64
			if err := tx.Model(&CandidateProfile{}).
				Where("platform = ? AND account_ref = ? AND conversation_ref = ?",
					req.Platform, req.AccountRef, conversationKey.ConversationRef).
				Count(&occupiedN).Error; err != nil {
				return err
			}
			if occupiedN != 0 {
				continue
			}

			var greeting EffectIntent
			if err := tx.First(&greeting, "intent_id = ?", *profile.SuccessfulGreetingIntentID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					continue
				}
				return err
			}
			if greeting.Primitive != primitiveChatSendGreeting || greeting.TargetRef != profile.ProfileID ||
				greeting.Platform != req.Platform || greeting.AccountRef != req.AccountRef ||
				(greeting.Status != EffectIntentOk && greeting.Status != EffectIntentResolvedOk) ||
				greeting.ResultConversationRef != nil || greeting.ResultMessageSeq != nil {
				continue
			}
			var root CmdRecord
			if err := tx.First(&root, "msg_id = ?", greeting.RootMsgID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					continue
				}
				return err
			}
			meta, metaExists := protocol.Primitives[protocol.PrimChatSendGreeting]
			argsRaw := json.RawMessage(root.Args)
			var args protocol.ChatSendGreetingArgs
			if !metaExists || meta.Ver == 0 ||
				protocol.ValidatePrimitiveArgs(protocol.PrimChatSendGreeting, meta.Ver, argsRaw) != nil ||
				json.Unmarshal(argsRaw, &args) != nil ||
				root.Name != primitiveChatSendGreeting || root.Class != "effectful" ||
				root.IntentID != greeting.IntentID || root.MsgID != greeting.RootMsgID ||
				root.Platform != req.Platform || root.AccountRef != req.AccountRef ||
				(root.Status != CmdOk && root.Status != CmdResolvedOk) ||
				root.TerminalAt == nil || args.PlatformUserRef != platformUserRef ||
				args.PositionRef != profile.PositionRef || args.Text == "" ||
				sourcingGreetingSendFingerprint(args.Text) != greeting.SendFingerprint {
				continue
			}

			profileUpdated := tx.Model(&CandidateProfile{}).
				Where("profile_id = ? AND platform = ? AND account_ref = ? AND platform_user_ref = ? AND main_status = ? AND end_reason IS NULL AND conversation_ref IS NULL AND successful_greeting_intent_id = ? AND greeted_at IS NOT NULL",
					profile.ProfileID, req.Platform, req.AccountRef, platformUserRef, CandidateProfileGreeted,
					*profile.SuccessfulGreetingIntentID).
				Update("conversation_ref", conversationKey.ConversationRef)
			if profileUpdated.Error != nil {
				return profileUpdated.Error
			}
			if profileUpdated.RowsAffected != 1 {
				return ErrCandidateProfileState
			}

			intentUpdated := tx.Model(&EffectIntent{}).
				Where("intent_id = ? AND primitive = ? AND target_ref = ? AND platform = ? AND account_ref = ? AND status IN ? AND result_conversation_ref IS NULL AND result_message_seq IS NULL",
					greeting.IntentID, primitiveChatSendGreeting, profile.ProfileID, req.Platform, req.AccountRef,
					[]EffectIntentStatus{EffectIntentOk, EffectIntentResolvedOk}).
				Updates(map[string]any{
					"result_conversation_ref": conversationKey.ConversationRef,
					"result_message_seq":      int64(1),
				})
			if intentUpdated.Error != nil {
				return intentUpdated.Error
			}
			if intentUpdated.RowsAffected != 1 {
				return ErrCandidateProfileState
			}

			greetingText := args.Text
			greetingIntentID := greeting.IntentID
			if err := tx.Create(&Message{
				Platform: req.Platform, AccountRef: req.AccountRef, ConversationRef: conversationKey.ConversationRef,
				Seq: 1, Direction: "out", Kind: "text", ContentHash: greeting.SendFingerprint,
				Text: &greetingText, Origin: "self", OutboundIntentID: &greetingIntentID,
			}).Error; err != nil {
				return err
			}

			adoptedAt := req.ObservedAt
			if err := tx.Create(&TrackedIntent{
				Platform: req.Platform, AccountRef: req.AccountRef, ConversationRef: conversationKey.ConversationRef,
				Status: TrackingAdopted, RequestedBy: lateGreetingTrackedRequestedBy,
				RequestedAt: req.ObservedAt, AdoptedAt: &adoptedAt,
			}).Error; err != nil {
				return err
			}
			conversationUpdated := tx.Model(&Conversation{}).
				Where("platform = ? AND account_ref = ? AND conversation_ref = ? AND platform_user_ref = ? AND tracking_state = ? AND adopted_boundary_seq = 0 AND last_message_seq = 0",
					req.Platform, req.AccountRef, conversationKey.ConversationRef, platformUserRef, TrackingUntracked).
				Updates(map[string]any{
					"tracking_state": TrackingAdopted, "adopted_boundary_seq": int64(1),
					"last_message_seq": int64(1),
				})
			if conversationUpdated.Error != nil {
				return conversationUpdated.Error
			}
			if conversationUpdated.RowsAffected != 1 {
				return ErrCandidateProfileState
			}
			if err := tx.Create(&AuditEntry{
				At: req.ObservedAt, Category: "conversation_adopted", Platform: req.Platform,
				AccountRef: req.AccountRef, ConversationRef: conversationKey.ConversationRef,
				RoundID: req.RoundID, Detail: "adoptedBoundarySeq=1 source=lateGreetingBind",
			}).Error; err != nil {
				return err
			}
			bound++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return bound, nil
}
