package store

import (
	"time"
)

// 聊天记录上报（AGENTS.md「全局约定·聊天记录上报」，2026-08-19 甲方裁决）的
// 读侧投影与上传水位。
//
// ChatReportCursor 是按会话的"已成功上传序号"水位。它是基础设施记录，不是业务
// 事实：服务端持有全量数据，整表删除的后果只是下次全量重传（服务端幂等，不产生
// 重复行），因此不受"业务事实行禁止物理 DELETE"约束。
type ChatReportCursor struct {
	Platform           string `gorm:"primaryKey"`
	AccountRef         string `gorm:"primaryKey"`
	ConversationRef    string `gorm:"primaryKey"`
	UploadedThroughSeq int64  `gorm:"not null"`
	UpdatedAt          time.Time
}

// ChatReportProfileRow 是候选人档案行的上报投影。JobName 取 position_title
// （职位维度按职位名区分，2026-08-19 甲方裁决；BackendJobID 仅作辅助定位列）。
type ChatReportProfileRow struct {
	ProfileID       string
	Platform        string
	AccountRef      string
	ConversationRef *string
	DisplayName     *string
	BackendJobID    *string
	JobName         *string
	MainStatus      string
	EndReason       *string

	GreetedAtMs       *int64
	CommunicatingAtMs *int64
	InterviewedAtMs   *int64
	WechatAtMs        *int64

	UpcomingInterviewStartsAtMs *int64
	UpcomingInterviewEndsAtMs   *int64
	UpcomingInterviewMethod     *string
}

// ChatReportMessageRow 是消息行的上报投影，连带业务出身推导所需的三层线索
// （沟通事件动作 V4Kind > AI 轮动作 Kind > 意图原语名），推导本身在 chatreport
// 包做——这里只负责把账本里已有的事实原样取出来。
type ChatReportMessageRow struct {
	Platform        string
	AccountRef      string
	ConversationRef string
	Seq             int64
	ProfileID       *string

	Direction string
	Kind      string
	Text      *string
	CardType  string
	CardState string

	InterviewStartsAtMs *int64
	InterviewEndsAtMs   *int64
	InterviewMethod     *string
	TsApproxMs          *int64

	OutboundIntentID *string
	EventKind        string
	ActionKind       string
	Primitive        string

	RetractedAt      *time.Time
	RetractionReason string
}

// ChatReportProfileRows 全量列出候选人档案投影（每日全量 UPSERT 刷新是条款
// 语义：业务终态以档案行为准，消息行不回改）。量级是千行级，全量扫可接受。
func (s *Store) ChatReportProfileRows() ([]ChatReportProfileRow, error) {
	type profileScan struct {
		ProfileID       string
		Platform        string
		AccountRef      string
		ConversationRef *string
		DisplayName     *string
		BackendJobID    *string
		PositionTitle   *string
		MainStatus      string
		EndReason       *string
		GreetedAt       *time.Time
		CommunicatingAt *time.Time
		InterviewedAt   *time.Time
	}
	var profiles []profileScan
	if err := s.db.Table("candidate_profiles AS p").
		Select("p.profile_id, p.platform, p.account_ref, p.conversation_ref, "+
			"c.display_name, p.backend_job_id, p.position_title, "+
			"p.main_status, p.end_reason, "+
			"p.greeted_at, p.communicating_at, p.interviewed_at").
		Joins("LEFT JOIN candidates AS c ON c.platform = p.platform "+
			"AND c.platform_user_ref = p.platform_user_ref").
		Order("p.profile_id").
		Scan(&profiles).Error; err != nil {
		return nil, err
	}

	// 换微信时刻：权威 ContactAsset 的收编时刻（毫秒整数列，与产品端"换微信
	// 时间"列同源）。同一档案理论上只会有一条 wechat 资产，MIN 只是防御。
	type wechatScan struct {
		ProfileID string
		Ms        int64
	}
	var wechatRows []wechatScan
	if err := s.db.Table("contact_assets").
		Select("profile_id, MIN(observed_at_ms) AS ms").
		Where("kind = ?", "wechat").
		Group("profile_id").
		Scan(&wechatRows).Error; err != nil {
		return nil, err
	}
	wechatByProfile := make(map[string]int64, len(wechatRows))
	for _, row := range wechatRows {
		wechatByProfile[row.ProfileID] = row.Ms
	}

	// 最新未撤回邀面卡的约定时段与方式。口径同 appInterviewCardRowsTx（日报
	// "未来待面试名单"）：每会话只看 seq 最大的一张未撤回邀面卡，不按时间筛。
	type cardScan struct {
		ProfileID  string
		StartsAtMs *int64
		EndsAtMs   *int64
		Method     *string
	}
	var cardRows []cardScan
	if err := s.db.Table("messages AS m").
		Select("p.profile_id, m.interview_starts_at_ms AS starts_at_ms, "+
			"m.interview_ends_at_ms AS ends_at_ms, m.interview_method AS method").
		Joins("JOIN candidate_profiles AS p ON p.platform = m.platform "+
			"AND p.account_ref = m.account_ref AND p.conversation_ref = m.conversation_ref").
		Where("m.direction = ? AND m.kind = ? AND m.card_type = ? AND m.retracted_at IS NULL",
			"out", "card", "interviewInvite").
		Where("m.seq = (SELECT MAX(latest.seq) FROM messages AS latest "+
			"WHERE latest.platform = m.platform AND latest.account_ref = m.account_ref "+
			"AND latest.conversation_ref = m.conversation_ref AND latest.direction = 'out' "+
			"AND latest.kind = 'card' AND latest.card_type = 'interviewInvite' "+
			"AND latest.retracted_at IS NULL)").
		Scan(&cardRows).Error; err != nil {
		return nil, err
	}
	cardByProfile := make(map[string]cardScan, len(cardRows))
	for _, row := range cardRows {
		cardByProfile[row.ProfileID] = row
	}

	out := make([]ChatReportProfileRow, 0, len(profiles))
	for _, p := range profiles {
		row := ChatReportProfileRow{
			ProfileID:       p.ProfileID,
			Platform:        p.Platform,
			AccountRef:      p.AccountRef,
			ConversationRef: p.ConversationRef,
			DisplayName:     p.DisplayName,
			BackendJobID:    p.BackendJobID,
			JobName:         p.PositionTitle,
			MainStatus:      p.MainStatus,
			EndReason:       p.EndReason,
			GreetedAtMs:     msOf(p.GreetedAt),
			CommunicatingAtMs: msOf(p.CommunicatingAt),
			InterviewedAtMs:   msOf(p.InterviewedAt),
		}
		if ms, ok := wechatByProfile[p.ProfileID]; ok {
			wechatMs := ms
			row.WechatAtMs = &wechatMs
		}
		if card, ok := cardByProfile[p.ProfileID]; ok {
			row.UpcomingInterviewStartsAtMs = card.StartsAtMs
			row.UpcomingInterviewEndsAtMs = card.EndsAtMs
			row.UpcomingInterviewMethod = card.Method
		}
		out = append(out, row)
	}
	return out, nil
}

// ChatReportPendingMessages 按会话水位取还没上传的消息行，全库统一按
// (platform, account_ref, conversation_ref, seq) 排序、限量分批。Seq 在账本里
// 是纯追加分配（nextPhysicalMessageSeqTx = MAX+1），水位之后只增不插，游标因此
// 安全；已上传行的后续 UPDATE（撤回、卡片跃迁、source_key 回配）按条款不回改。
func (s *Store) ChatReportPendingMessages(limit int) ([]ChatReportMessageRow, error) {
	if limit <= 0 {
		limit = 500
	}
	var rows []ChatReportMessageRow
	if err := s.db.Table("messages AS m").
		Select("m.platform, m.account_ref, m.conversation_ref, m.seq, "+
			"p.profile_id, m.direction, m.kind, m.text, m.card_type, m.card_state, "+
			"m.interview_starts_at_ms, m.interview_ends_at_ms, m.interview_method, "+
			"m.ts_approx_ms, m.outbound_intent_id, "+
			"COALESCE(ev.v4_kind, '') AS event_kind, "+
			"COALESCE(ca.kind, '') AS action_kind, "+
			"COALESCE(ei.primitive, '') AS primitive, "+
			"m.retracted_at, m.retraction_reason").
		Joins("LEFT JOIN chat_report_cursors AS cur ON cur.platform = m.platform "+
			"AND cur.account_ref = m.account_ref AND cur.conversation_ref = m.conversation_ref").
		Joins("LEFT JOIN candidate_profiles AS p ON p.platform = m.platform "+
			"AND p.account_ref = m.account_ref AND p.conversation_ref = m.conversation_ref").
		Joins("LEFT JOIN effect_intents AS ei ON ei.intent_id = m.outbound_intent_id").
		Joins("LEFT JOIN communication_v4_event_actions AS ev ON ev.effect_intent_id = m.outbound_intent_id").
		Joins("LEFT JOIN communication_actions AS ca ON ca.effect_intent_id = m.outbound_intent_id").
		Where("m.seq > COALESCE(cur.uploaded_through_seq, 0)").
		Order("m.platform, m.account_ref, m.conversation_ref, m.seq").
		Limit(limit).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// AdvanceChatReportCursor 把某会话的水位单调推进到 throughSeq。只进不退：
// 并发或乱序推进时取两者较大值，绝不回拨——回拨会导致已上传行重传（服务端幂等
// 兜得住），但更重要的是防止把水位错写小之后掩盖"漏传"。
func (s *Store) AdvanceChatReportCursor(platform, accountRef, conversationRef string, throughSeq int64) error {
	return s.db.Exec(
		`INSERT INTO chat_report_cursors
		   (platform, account_ref, conversation_ref, uploaded_through_seq, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(platform, account_ref, conversation_ref)
		 DO UPDATE SET
		   uploaded_through_seq = MAX(uploaded_through_seq, excluded.uploaded_through_seq),
		   updated_at = excluded.updated_at`,
		platform, accountRef, conversationRef, throughSeq, time.Now(),
	).Error
}

func msOf(t *time.Time) *int64 {
	if t == nil || t.IsZero() {
		return nil
	}
	ms := t.UnixMilli()
	return &ms
}
