package store

import (
	"errors"
	"strings"

	"gorm.io/gorm"
)

// ErrPendingVisibleDispatchInvalid 是本文件唯一的参数错误:会话键三项缺一。
var ErrPendingVisibleDispatchInvalid = errors.New("待派发可见动作查询的会话键无效")

// 本文件回答巡检判脏的一个问题:这个会话有没有"等着派发的候选人可见动作"。
//
// 立案背景(2026-08-04 真机):发送系原语只认已经打开的会话页(按标签页 URL
// 定位、自身绝不导航),而页面导航只发生在判脏之后的 reconcileConversation
// 里。判脏原有四条依据(跟踪态 pending、账本为空、未读面强制、列表指纹失配)
// 都不包含"这个人有一条回复正等着发",于是"候选人安静下来之后才要发的回复"
// 必然撞 CTX_NOT_READY/pageAbsent。真机上两个候选人各自连续 20 代重试全败,
// 直到会话在平台列表里被动了一下才自愈,期间回复分别迟了 3 小时 35 分和 4
// 小时。本文件让待派发动作成为第五条读取理由。
//
// 收窄三条,每条都有真机依据:
//   - 只算链首(depends_on_action_id 为空)。链内后续项搭链首打开的页面,不必
//     自己开;而"等父项正证"可以长期挂着(同批数据里有 08-01 的 inviteWechat
//     至今在等),不排除会让该会话每轮被无意义重读。
//   - 只算聚合 active 的候选人。派发枚举本身只列 active,被冻结档案的 planned
//     残留永远不会被遭遇(同批数据里有一条 08-01 的 replyText 正是如此),不排
//     除同样是每轮白读且永远清不掉。
//   - 排除 notification(运营通知 webhook)。它不是候选人可见动作,不经页面。
//
// 已知盲区(2026-08-04 甲方选定接受):判脏发生在派发之前,当轮才开轮、当轮才
// 铸动作的链首(真机王龙跃那例:开轮与铸动作相隔 1 秒、都在派发前 4 秒内)在
// 判脏时刻库里还没有行,查不到。这类首条仍会失败一次,由 §8.4 重试代在下一轮
// 命中本判据后自愈,失败一次的事实由派发失败日志暴露。

// visibleCommunicationActionKinds 是 legacy 对话轨里需要页面的动作类型。
// 显式列举而不是"排除某几个":将来新增不可见动作时,漏改的方向是少读一次
// 页面(该动作自己失败一次并重试),而不是每轮多读一批会话。
var visibleCommunicationActionKinds = []CommunicationActionKind{
	CommunicationActionReplyText,
	CommunicationActionInviteWechat,
	CommunicationActionInterviewInvite,
	CommunicationActionAcceptWechat,
}

// ConversationHasPlannedVisibleDispatch 报告该会话是否挂着等待派发的候选人
// 可见动作链首。两条动作轨(legacy communication_actions 与 v4
// communication_v4_event_actions)任一命中即为真。
//
// 调用方是巡检判脏,每轮每会话至多一次;两条查询都走既有 status 索引并 LIMIT 1。
func (s *Store) ConversationHasPlannedVisibleDispatch(key ConversationKey) (bool, error) {
	key.Platform = strings.TrimSpace(key.Platform)
	key.AccountRef = strings.TrimSpace(key.AccountRef)
	key.ConversationRef = strings.TrimSpace(key.ConversationRef)
	if key.Platform == "" || key.AccountRef == "" || key.ConversationRef == "" {
		return false, ErrPendingVisibleDispatchInvalid
	}
	pending, err := conversationHasPlannedLegacyDispatch(s.db, key)
	if err != nil || pending {
		return pending, err
	}
	return conversationHasPlannedV4EventDispatch(s.db, key)
}

func conversationHasPlannedLegacyDispatch(db *gorm.DB, key ConversationKey) (bool, error) {
	var found int
	err := db.
		Table("communication_actions AS action").
		Select("1").
		Joins("JOIN dialogue_turns AS turn ON turn.turn_id = action.turn_id").
		Joins("JOIN candidate_profiles AS profile ON profile.profile_id = turn.profile_id").
		Joins("JOIN communication_v4_aggregates AS aggregate ON aggregate.profile_id = turn.profile_id").
		Where(
			"profile.platform = ? AND profile.account_ref = ? AND turn.conversation_ref = ?",
			key.Platform, key.AccountRef, key.ConversationRef,
		).
		Where("action.status = ?", CommunicationActionPlanned).
		Where("action.depends_on_action_id IS NULL OR action.depends_on_action_id = ''").
		Where("action.kind IN ?", visibleCommunicationActionKinds).
		Where("aggregate.automation_status = ?", ProfileCommunicationAutomationActive).
		Limit(1).
		Scan(&found).Error
	return found == 1, err
}

func conversationHasPlannedV4EventDispatch(db *gorm.DB, key ConversationKey) (bool, error) {
	var found int
	err := db.
		Table("communication_v4_event_actions AS action").
		Select("1").
		Joins("JOIN candidate_profiles AS profile ON profile.profile_id = action.profile_id").
		Joins("JOIN communication_v4_aggregates AS aggregate ON aggregate.profile_id = action.profile_id").
		Where(
			"profile.platform = ? AND profile.account_ref = ? AND profile.conversation_ref = ?",
			key.Platform, key.AccountRef, key.ConversationRef,
		).
		Where("action.status = ?", CommunicationV4EventActionPlanned).
		Where("action.depends_on_action_id IS NULL OR action.depends_on_action_id = ''").
		Where("action.effect_kind <> ?", CommunicationV4EventEffectNotification).
		Where("aggregate.automation_status = ?", ProfileCommunicationAutomationActive).
		Limit(1).
		Scan(&found).Error
	return found == 1, err
}
