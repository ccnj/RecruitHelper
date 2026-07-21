package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// messageBeforeRetraction 是本批之前的 messages 持久形状，用真旧表
// 验证 AutoMigrate，不在新 Message 表里写空值伪装升级。
type messageBeforeRetraction struct {
	Platform        string `gorm:"primaryKey;index:idx_messages_conversation,priority:1"`
	AccountRef      string `gorm:"primaryKey;index:idx_messages_conversation,priority:2"`
	ConversationRef string `gorm:"primaryKey;index:idx_messages_conversation,priority:3"`
	Seq             int64  `gorm:"primaryKey;autoIncrement:false"`

	Direction        string `gorm:"not null"`
	Kind             string `gorm:"not null"`
	ContentHash      string `gorm:"not null;index"`
	Text             *string
	BlobRef          string
	CardType         string
	CardState        string
	TsApproxMs       *int64
	Origin           string
	FirstSeenRoundID string  `gorm:"index"`
	OutboundIntentID *string `gorm:"uniqueIndex"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

func (messageBeforeRetraction) TableName() string { return "messages" }

func testOutboundIntent(key ConversationKey, intentID string) EffectIntent {
	return EffectIntent{
		IntentID: intentID, Platform: key.Platform, AccountRef: key.AccountRef,
		Primitive: primitiveChatSendMessage, TargetRef: key.ConversationRef,
	}
}

func appendTestOutbound(
	t *testing.T,
	s *Store,
	intent *EffectIntent,
	text string,
	at time.Time,
) *Message {
	t.Helper()
	var message *Message
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		message, err = appendOutboundMessageTx(tx, intent, text, "hash-"+intent.IntentID, at.UnixMilli(), at)
		return err
	})
	if err != nil {
		t.Fatalf("appendOutboundMessageTx: %v", err)
	}
	return message
}

func retractTestOutbound(
	t *testing.T,
	s *Store,
	intent *EffectIntent,
	at time.Time,
	reason string,
) {
	t.Helper()
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		return retractOutboundMessageTx(tx, intent, at, reason)
	}); err != nil {
		t.Fatalf("retractOutboundMessageTx: %v", err)
	}
}

func physicalMessageFacts(t *testing.T, s *Store, key ConversationKey) []Message {
	t.Helper()
	var facts []Message
	if err := s.db.Where(conversationWhere(key), conversationArgs(key)...).Order("seq").Find(&facts).Error; err != nil {
		t.Fatal(err)
	}
	return facts
}

func TestRetractedOutboundFactStaysAuditableAndOutsideActiveReads(t *testing.T) {
	s := openTest(t)
	key := seedEffectHeadTarget(t, s)
	firstAt := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	retractedAt := firstAt.Add(time.Minute)
	intent := testOutboundIntent(key, "intent-retracted")

	if message := appendTestOutbound(t, s, &intent, "mistaken self", firstAt); message.Seq != 2 {
		t.Fatalf("预置出站事实 seq=%d, want 2", message.Seq)
	}
	retractTestOutbound(t, s, &intent, retractedAt, messageRetractionReasonAuthoritativeSafeTerminal)

	active, err := s.MessagesForConversation(key)
	if err != nil || len(active) != 1 || active[0].Seq != 1 {
		t.Fatalf("活动账本必须排除撤回行: messages=%+v err=%v", active, err)
	}
	recent, err := s.RecentMessagesForConversation(key, 1)
	if err != nil || len(recent) != 1 || recent[0].Seq != 1 {
		t.Fatalf("limit 必须在过滤撤回行后作用: recent=%+v err=%v", recent, err)
	}
	if hidden, err := s.MessageBySeq(key, 2); err != nil || hidden != nil {
		t.Fatalf("业务按 seq 读不得返回撤回行: message=%+v err=%v", hidden, err)
	}
	preparation, err := s.PrepareSend(key, 5)
	if err != nil || len(preparation.Tail) != 1 || preparation.Tail[0].Seq != 1 {
		t.Fatalf("发送 guard 尾不得包含撤回行: preparation=%+v err=%v", preparation, err)
	}

	facts := physicalMessageFacts(t, s, key)
	if len(facts) != 2 || facts[1].Seq != 2 || facts[1].RetractedAt == nil ||
		!facts[1].RetractedAt.Equal(retractedAt) ||
		facts[1].RetractionReason != messageRetractionReasonAuthoritativeSafeTerminal ||
		facts[1].OutboundIntentID == nil || *facts[1].OutboundIntentID != intent.IntentID {
		t.Fatalf("物理事实必须保留首次撤回证词与 intent 绑定: %+v", facts)
	}
	conversation, err := s.ConversationByKey(key)
	if err != nil || conversation.LastMessageSeq != 1 || conversation.LastMessageDirection != "in" ||
		conversation.LastMessageKind != "text" || conversation.LastMessagePreview != "history" {
		t.Fatalf("会话尾/摘要必须回到最新活动消息: conversation=%+v err=%v", conversation, err)
	}

	// 重复撤回不覆盖首次原因或时间。
	retractTestOutbound(t, s, &intent, retractedAt.Add(time.Hour), messageRetractionReasonManualResolvedFailed)
	facts = physicalMessageFacts(t, s, key)
	if facts[1].RetractedAt == nil || !facts[1].RetractedAt.Equal(retractedAt) ||
		facts[1].RetractionReason != messageRetractionReasonAuthoritativeSafeTerminal {
		t.Fatalf("重复撤回覆盖了首次证词: %+v", facts[1])
	}

	// 唯一 outboundIntentId 仍占位，既不复活标记行，也不创建第二条。
	err = s.db.Transaction(func(tx *gorm.DB) error {
		_, err := appendOutboundMessageTx(tx, &intent, "mistaken self", "hash-"+intent.IntentID, 0, firstAt)
		return err
	})
	if !errors.Is(err, ErrRecoveryStateConflict) {
		t.Fatalf("已撤回 intent 再 append 必须冲突: %v", err)
	}

	// 物理 seq=2 不得复用；新的真实出站事实必须从 3 继续。
	nextIntent := testOutboundIntent(key, "intent-after-retraction")
	if message := appendTestOutbound(t, s, &nextIntent, "real next", firstAt.Add(2*time.Hour)); message.Seq != 3 {
		t.Fatalf("撤回后出站 seq=%d, want 3", message.Seq)
	}
	active, err = s.RecentMessagesForConversation(key, 2)
	if err != nil || len(active) != 2 || active[0].Seq != 1 || active[1].Seq != 3 {
		t.Fatalf("稀疏活动尾读取错误: messages=%+v err=%v", active, err)
	}
}

func TestAllMessageAppendPathsAllocateAfterRetractedPhysicalTail(t *testing.T) {
	tests := []struct {
		name   string
		append func(*testing.T, *Store, ConversationKey) int64
	}{
		{
			name: "conversation changes",
			append: func(t *testing.T, s *Store, key ConversationKey) int64 {
				text := "patrol next"
				result, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{
					Key: key, ExpectedTailSeq: 1, PlatformUserRef: "candidate-head",
					NewMessages: []MessageDraft{{
						Direction: "in", Kind: "text", ContentHash: "patrol-next-hash", Text: &text, Origin: "external",
					}},
				})
				if err != nil {
					t.Fatal(err)
				}
				return result.Inserted[0].Seq
			},
		},
		{
			name: "historical rebaseline",
			append: func(t *testing.T, s *Store, key ConversationKey) int64 {
				const roundID = "round-after-retraction"
				if err := s.CreatePatrolRound(&PatrolRound{
					Platform: key.Platform, AccountRef: key.AccountRef, RoundID: roundID,
				}); err != nil {
					t.Fatal(err)
				}
				text := "deep next"
				result, err := s.RebuildConversationBaseline(RebuildConversationBaselineRequest{
					Key: key, RoundID: roundID, ExpectedTailSeq: 1, PlatformUserRef: "candidate-head",
					Historical: []MessageDraft{{
						Direction: "in", Kind: "text", ContentHash: "deep-next-hash", Text: &text, Origin: "external",
					}},
				})
				if err != nil {
					t.Fatal(err)
				}
				if result.HistoricalFromSeq != 3 || result.HistoricalThroughSeq != 3 {
					t.Fatalf("重建范围必须使用实际物理分配: %+v", result)
				}
				return result.Inserted[0].Seq
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := openTest(t)
			key := seedEffectHeadTarget(t, s)
			at := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
			intent := testOutboundIntent(key, "intent-hole-"+test.name)
			appendTestOutbound(t, s, &intent, "false tail", at)
			retractTestOutbound(t, s, &intent, at.Add(time.Minute), messageRetractionReasonAuthoritativeSafeTerminal)

			if seq := test.append(t, s, key); seq != 3 {
				t.Fatalf("撤回物理尾后新 seq=%d, want 3", seq)
			}
			facts := physicalMessageFacts(t, s, key)
			if len(facts) != 3 || facts[1].RetractedAt == nil || facts[2].Seq != 3 {
				t.Fatalf("三条物理事实不完整: %+v", facts)
			}
		})
	}
}

func TestRetractingMiddleFactKeepsLaterActiveTailAndSummary(t *testing.T) {
	s := openTest(t)
	key := seedEffectHeadTarget(t, s)
	at := time.Date(2026, 7, 21, 10, 30, 0, 0, time.UTC)
	oldIntent := testOutboundIntent(key, "intent-middle-retracted")
	newIntent := testOutboundIntent(key, "intent-middle-successor")
	appendTestOutbound(t, s, &oldIntent, "false middle", at)
	appendTestOutbound(t, s, &newIntent, "active tail", at.Add(time.Minute))

	retractTestOutbound(t, s, &oldIntent, at.Add(2*time.Minute), messageRetractionReasonAuthoritativeSafeTerminal)
	active, err := s.MessagesForConversation(key)
	if err != nil || len(active) != 2 || active[0].Seq != 1 || active[1].Seq != 3 {
		t.Fatalf("中间撤回后活动账本应保留稀疏尾: messages=%+v err=%v", active, err)
	}
	conversation, err := s.ConversationByKey(key)
	if err != nil || conversation.LastMessageSeq != 3 || conversation.LastMessageDirection != "out" ||
		conversation.LastMessageKind != "text" || conversation.LastMessagePreview != "active tail" {
		t.Fatalf("撤回中间行不得回退较新活动尾/摘要: conversation=%+v err=%v", conversation, err)
	}
	lastIntent := testOutboundIntent(key, "intent-after-middle-retraction")
	if message := appendTestOutbound(t, s, &lastIntent, "after sparse tail", at.Add(3*time.Minute)); message.Seq != 4 {
		t.Fatalf("中间撤回后追加 seq=%d, want 4", message.Seq)
	}
}

func TestRetractedMessageCorrectionRollsBackWithCmdIntentAndSummary(t *testing.T) {
	s := openTest(t)
	key := seedEffectHeadTarget(t, s)
	now := time.Date(2026, 7, 21, 11, 0, 0, 0, time.UTC)
	created, err := createHeadIntent(t, s, key, "intent-retraction-rollback", "", now, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&CmdRecord{}).Where("msg_id = ?", created.Command.MsgID).
		Updates(map[string]any{"status": CmdSuspect}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&EffectIntent{}).Where("intent_id = ?", created.Intent.IntentID).
		Updates(map[string]any{"status": EffectIntentSuspect}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveSuspectVerdict(ResolveSuspectVerdictRequest{
		Ref: created.Command.MsgID, Verdict: CmdResolvedOk, ConversationKey: key,
		Text: "human confirmed", ContentHash: created.Intent.SendFingerprint, At: now.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	forced := errors.New("forced intent update failure")
	callbackName := "test:fail_retraction_intent_update"
	if err := s.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "EffectIntent" {
			tx.AddError(forced)
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, applyErr := s.ApplyResultMessage(
		created.Command.MsgID, "result-retraction-rollback", "result", "hand-head",
		func(command *CmdRecord) (ResultCommandMutation, error) {
			terminalAt := now.Add(2 * time.Minute)
			command.Status = CmdFailed
			command.TerminalAt = &terminalAt
			return ResultCommandMutation{Save: true, Effect: &EffectResultMutation{
				IntentStatus: EffectIntentFailed, Retract: true,
			}}, nil
		},
	)
	if err := s.db.Callback().Update().Remove(callbackName); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(applyErr, forced) {
		t.Fatalf("预期事务末步失败: %v", applyErr)
	}

	cmd, _ := s.CmdByMsgID(created.Command.MsgID)
	intent, _ := s.EffectIntentByID(created.Intent.IntentID)
	conversation, _ := s.ConversationByKey(key)
	facts := physicalMessageFacts(t, s, key)
	var processed int64
	if err := s.db.Model(&ProcessedMsg{}).Where("msg_id = ?", "result-retraction-rollback").Count(&processed).Error; err != nil {
		t.Fatal(err)
	}
	if cmd == nil || cmd.Status != CmdResolvedOk || intent == nil || intent.Status != EffectIntentResolvedOk ||
		intent.ResultMessageSeq == nil || *intent.ResultMessageSeq != 2 || conversation.LastMessageSeq != 2 ||
		conversation.LastMessagePreview != "human confirmed" || len(facts) != 2 || facts[1].RetractedAt != nil || processed != 0 {
		t.Fatalf("撤回事务失败后出现半态: cmd=%+v intent=%+v conversation=%+v facts=%+v processed=%d",
			cmd, intent, conversation, facts, processed)
	}
}

func TestMessageRetractionSchemaUsesExplicitFieldsWithoutSoftDelete(t *testing.T) {
	s := openTest(t)
	type tableColumn struct {
		Name string
	}
	var columns []tableColumn
	if err := s.db.Raw("PRAGMA table_info('messages')").Scan(&columns).Error; err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, column := range columns {
		seen[column.Name] = true
	}
	if !seen["retracted_at"] || !seen["retraction_reason"] || seen["deleted_at"] {
		t.Fatalf("消息表必须用显式撤回字段且不得有 soft delete: %+v", columns)
	}
}

func TestMessageRetractionAutoMigratePreservesLegacyRowsAndIntentUniqueness(t *testing.T) {
	dir := t.TempDir()
	legacyDB, err := gorm.Open(sqlite.Open("file:"+filepath.Join(dir, "brain.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacyDB.AutoMigrate(&messageBeforeRetraction{}); err != nil {
		t.Fatal(err)
	}
	text := "legacy"
	intentID := "legacy-outbound-intent"
	legacy := messageBeforeRetraction{
		Platform: "zhilian", AccountRef: "legacy-account", ConversationRef: "legacy-conversation", Seq: 1,
		Direction: "out", Kind: "text", ContentHash: "legacy-hash", Text: &text, Origin: "self",
		OutboundIntentID: &intentID,
	}
	if err := legacyDB.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	legacySQL, err := legacyDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := legacySQL.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("升级旧 messages 表: %v", err)
	}
	defer s.Close()
	key := ConversationKey{Platform: legacy.Platform, AccountRef: legacy.AccountRef, ConversationRef: legacy.ConversationRef}
	active, err := s.MessagesForConversation(key)
	if err != nil || len(active) != 1 || active[0].RetractedAt != nil || active[0].RetractionReason != "" {
		t.Fatalf("旧消息升级后必须保持活动: messages=%+v err=%v", active, err)
	}
	duplicateIntent := intentID
	duplicate := Message{
		Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: key.ConversationRef, Seq: 2,
		Direction: "out", Kind: "text", ContentHash: "duplicate", Origin: "self",
		OutboundIntentID: &duplicateIntent,
	}
	if err := s.db.Create(&duplicate).Error; err == nil {
		t.Fatal("升级不得丢失 outboundIntentId 唯一索引")
	}
}
