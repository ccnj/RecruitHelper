package store

import (
	"testing"
	"time"
)

// 聊天记录上报投影（AGENTS.md「全局约定·聊天记录上报」，2026-08-19 甲方裁决）。
// 真库测试：档案投影的姓名连接、毫秒时刻换算、换微信时刻、最新未撤回邀面卡；
// 消息投影的出身线索连接与游标水位语义。

func chatReportSeedStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("建库: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func strPtr(v string) *string { return &v }

func TestChatReportProfileRowsProjection(t *testing.T) {
	s := chatReportSeedStore(t)
	greeted := time.Date(2026, 8, 18, 9, 30, 0, 0, time.Local)
	communicating := greeted.Add(2 * time.Hour)

	if err := s.db.Create(&Candidate{
		Platform: "zhilian", PlatformUserRef: "u-1", DisplayName: strPtr("张三"),
		FirstSeenAt: greeted, LastSeenAt: greeted,
	}).Error; err != nil {
		t.Fatalf("建候选人: %v", err)
	}
	conv := "conv-1"
	if err := s.db.Create(&CandidateProfile{
		ProfileID: "p-1", Platform: "zhilian", AccountRef: "acc-1",
		PlatformUserRef: "u-1", PositionRef: "pos-1",
		PositionTitle: strPtr("平安健康保障顾问"), BackendJobID: strPtr("16"),
		MainStatus: CandidateProfileCommunicating, ConversationRef: &conv,
		GreetedAt: &greeted, CommunicatingAt: &communicating,
	}).Error; err != nil {
		t.Fatalf("建档案: %v", err)
	}
	if err := s.db.Create(&ContactAsset{
		AssetID: "asset-1", ProfileID: "p-1", Platform: "zhilian",
		AccountRef: "acc-1", ConversationRef: conv, Kind: "wechat",
		SourceKey: "sk-1", RequestSourceKey: "rk-1", Value: "wx-secret",
		ObservedAtMs: 1755600000000,
	}).Error; err != nil {
		t.Fatalf("建联系资产: %v", err)
	}
	startsAt := int64(1755700000000)
	method := "onsite"
	// 旧卡已撤回、新卡未撤回：投影必须取新卡。
	retractedAt := greeted
	for seq, msg := range []Message{
		{Seq: 3, RetractedAt: &retractedAt, RetractionReason: "改期重发"},
		{Seq: 4},
	} {
		card := msg
		card.Platform, card.AccountRef, card.ConversationRef = "zhilian", "acc-1", conv
		card.Direction, card.Kind, card.CardType, card.CardState = "out", "card", "interviewInvite", "pending"
		card.ContentHash, card.Origin = "card-hash", "self"
		starts := startsAt + int64(seq)
		card.InterviewStartsAtMs, card.InterviewMethod = &starts, &method
		if err := s.db.Create(&card).Error; err != nil {
			t.Fatalf("建邀面卡: %v", err)
		}
	}

	rows, err := s.ChatReportProfileRows()
	if err != nil {
		t.Fatalf("读档案投影: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("想要 1 行档案，得 %d", len(rows))
	}
	row := rows[0]
	if row.DisplayName == nil || *row.DisplayName != "张三" {
		t.Fatalf("姓名连接错误: %+v", row.DisplayName)
	}
	if row.JobName == nil || *row.JobName != "平安健康保障顾问" {
		t.Fatalf("职位名应取 position_title: %+v", row.JobName)
	}
	if row.GreetedAtMs == nil || *row.GreetedAtMs != greeted.UnixMilli() {
		t.Fatalf("招呼时刻换算错误: %+v", row.GreetedAtMs)
	}
	if row.CommunicatingAtMs == nil || *row.CommunicatingAtMs != communicating.UnixMilli() {
		t.Fatalf("进入沟通时刻换算错误: %+v", row.CommunicatingAtMs)
	}
	if row.InterviewedAtMs != nil {
		t.Fatalf("未约面不应有约面时刻: %+v", row.InterviewedAtMs)
	}
	if row.WechatAtMs == nil || *row.WechatAtMs != 1755600000000 {
		t.Fatalf("换微信时刻应取资产收编毫秒: %+v", row.WechatAtMs)
	}
	if row.UpcomingInterviewStartsAtMs == nil || *row.UpcomingInterviewStartsAtMs != startsAt+1 {
		t.Fatalf("应取最新未撤回邀面卡(seq=4): %+v", row.UpcomingInterviewStartsAtMs)
	}
	if row.UpcomingInterviewMethod == nil || *row.UpcomingInterviewMethod != "onsite" {
		t.Fatalf("面试方式缺失: %+v", row.UpcomingInterviewMethod)
	}
}

func TestChatReportPendingMessagesAndCursor(t *testing.T) {
	s := chatReportSeedStore(t)
	conv := "conv-1"
	now := time.Now()
	if err := s.db.Create(&Candidate{
		Platform: "zhilian", PlatformUserRef: "u-1", DisplayName: strPtr("张三"),
		FirstSeenAt: now, LastSeenAt: now,
	}).Error; err != nil {
		t.Fatalf("建候选人: %v", err)
	}
	if err := s.db.Create(&CandidateProfile{
		ProfileID: "p-1", Platform: "zhilian", AccountRef: "acc-1",
		PlatformUserRef: "u-1", PositionRef: "pos-1",
		MainStatus: CandidateProfileCommunicating, ConversationRef: &conv,
	}).Error; err != nil {
		t.Fatalf("建档案: %v", err)
	}
	intentID := "intent-1"
	if err := s.db.Create(&EffectIntent{
		IntentID: intentID, IdemKey: "idem-1", Platform: "zhilian", AccountRef: "acc-1",
		Primitive: "chat.sendMessage", TargetRef: conv, PayloadHash: "ph", GuardsHash: "gh",
		RootMsgID: "root-1", Status: "resolvedSuccess", DeadlineMs: 1,
	}).Error; err != nil {
		t.Fatalf("建意图: %v", err)
	}
	if err := s.db.Create(&CommunicationV4EventAction{
		ActionID: "ev-1", ProfileID: "p-1", SourceInputKind: "message", SourceInputKey: "k1",
		SourceOrdinal: 0, SemanticActionKey: "sem-1", V4Kind: "coldPromptFixed",
		CardMessageSeq: 0, EffectKind: "replyText", Status: "sent",
		EffectIntentID: &intentID, PlannedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("建事件动作: %v", err)
	}
	text := "还在考虑吗"
	messages := []Message{
		{Seq: 1, Direction: "in", Kind: "text", ContentHash: "h1", Origin: "external", Text: strPtr("在吗")},
		{Seq: 2, Direction: "out", Kind: "text", ContentHash: "h2", Origin: "self", Text: &text, OutboundIntentID: &intentID},
		{Seq: 3, Direction: "out", Kind: "text", ContentHash: "h3", Origin: "external", Text: strPtr("人工手发")},
	}
	for _, msg := range messages {
		msg.Platform, msg.AccountRef, msg.ConversationRef = "zhilian", "acc-1", conv
		if err := s.db.Create(&msg).Error; err != nil {
			t.Fatalf("建消息 seq=%d: %v", msg.Seq, err)
		}
	}

	rows, err := s.ChatReportPendingMessages(10)
	if err != nil {
		t.Fatalf("读待传消息: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("无水位应全量待传，得 %d", len(rows))
	}
	if rows[0].ProfileID == nil || *rows[0].ProfileID != "p-1" {
		t.Fatalf("档案连接错误: %+v", rows[0].ProfileID)
	}
	if rows[1].EventKind != "coldPromptFixed" || rows[1].Primitive != "chat.sendMessage" {
		t.Fatalf("出身线索连接错误: event=%q primitive=%q", rows[1].EventKind, rows[1].Primitive)
	}
	if rows[2].OutboundIntentID != nil {
		t.Fatalf("人工行不应有意图: %+v", rows[2].OutboundIntentID)
	}

	if err := s.AdvanceChatReportCursor("zhilian", "acc-1", conv, 2); err != nil {
		t.Fatalf("推进水位: %v", err)
	}
	rows, err = s.ChatReportPendingMessages(10)
	if err != nil {
		t.Fatalf("水位后读待传: %v", err)
	}
	if len(rows) != 1 || rows[0].Seq != 3 {
		t.Fatalf("水位 2 之后应只剩 seq=3: %+v", rows)
	}

	// 水位只进不退：试图回拨到 1 应保持 2。
	if err := s.AdvanceChatReportCursor("zhilian", "acc-1", conv, 1); err != nil {
		t.Fatalf("回拨推进: %v", err)
	}
	rows, err = s.ChatReportPendingMessages(10)
	if err != nil {
		t.Fatalf("回拨后读待传: %v", err)
	}
	if len(rows) != 1 || rows[0].Seq != 3 {
		t.Fatalf("水位不得回退，应仍只剩 seq=3: %+v", rows)
	}
}
