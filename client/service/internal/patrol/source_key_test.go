package patrol

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

func TestPatrolRoundPersistsThreadMessageSourceKey(t *testing.T) {
	h := newHarness(t)
	oldKey := strings.Repeat("1", 64)
	newKey := strings.Repeat("2", 64)
	oldDraft := draftText("old")
	oldDraft.SourceKey = &oldKey
	conversationKey := seedTracked(t, h, "conversation-source-key", "peer-source-key", []store.MessageDraft{oldDraft})

	oldMessage := threadText(0, "old")
	oldMessage.SourceKey = oldKey
	newMessage := threadText(1, "new")
	newMessage.SourceKey = newKey
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary(conversationKey.ConversationRef, "peer-source-key", "new", 1),
				},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			return protocol.ChatReadThreadData{
				Messages:      []protocol.ThreadMessage{oldMessage, newMessage},
				Peer:          ptr(protocol.PeerSummary{DisplayName: "候选人", PlatformUserRef: "peer-source-key"}),
				Complete:      true,
				AnchorMatched: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("sourceKey 巡检轮失败: result=%+v err=%v", result, err)
	}
	if result.ProjectionCount() != 1 || len(result.Rounds[0].Projections) != 1 ||
		len(result.Rounds[0].Projections[0].Messages) != 1 {
		t.Fatalf("新消息投影错误: %+v", result.Rounds[0].Projections)
	}
	projected := result.Rounds[0].Projections[0].Messages[0]
	if projected.SourceKey == nil || *projected.SourceKey != newKey {
		t.Fatalf("ThreadMessage.sourceKey 未穿透 round 投影: %+v", projected.SourceKey)
	}

	messages, err := h.db.MessagesForConversation(conversationKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].SourceKey == nil || *messages[0].SourceKey != oldKey ||
		messages[1].SourceKey == nil || *messages[1].SourceKey != newKey {
		t.Fatalf("sourceKey 未按会话账本持久化: %+v", messages)
	}
}

func TestPatrolRoundSourceKeySemanticConflictStopsAccountForManualReview(t *testing.T) {
	h := newHarness(t)
	sourceKey := strings.Repeat("3", 64)
	oldDraft := draftText("old")
	oldDraft.SourceKey = &sourceKey
	conversationKey := seedTracked(t, h, "conversation-source-conflict", "peer-source-conflict", []store.MessageDraft{oldDraft})

	conflicting := threadText(0, "changed")
	conflicting.SourceKey = sourceKey
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary(conversationKey.ConversationRef, "peer-source-conflict", "changed", 1),
				},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			return protocol.ChatReadThreadData{
				Messages:      []protocol.ThreadMessage{conflicting},
				Peer:          ptr(protocol.PeerSummary{DisplayName: "候选人", PlatformUserRef: "peer-source-conflict"}),
				Complete:      true,
				AnchorMatched: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 ||
		!errors.Is(result.Rounds[0].Err, syncledger.ErrSourceKeySemanticConflict) {
		t.Fatalf("冲突轮必须响亮失败: result=%+v err=%v", result, err)
	}
	if result.ProjectionCount() != 0 {
		t.Fatalf("冲突不得 append 或投影: %+v", result.Rounds[0].Projections)
	}
	messages, err := h.db.MessagesForConversation(conversationKey)
	if err != nil || len(messages) != 1 || messages[0].ContentHash != oldDraft.ContentHash {
		t.Fatalf("冲突不得追加或重建基线: messages=%+v err=%v", messages, err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account.StoppedAt == nil || account.PausedReason != PauseHandManualReview {
		t.Fatalf("冲突必须收敛到账号级人工处理: account=%+v err=%v", account, err)
	}
	audits, err := h.db.AuditEntries(20)
	if err != nil {
		t.Fatal(err)
	}
	foundAudit := false
	for _, audit := range audits {
		if audit.Category != "conversation_source_identity_conflict" {
			continue
		}
		foundAudit = true
		if audit.ConversationRef != conversationKey.ConversationRef || strings.Contains(audit.Detail, sourceKey) {
			t.Fatalf("冲突审计作用域错误或泄露等值键: %+v", audit)
		}
	}
	if !foundAudit {
		t.Fatal("冲突必须留下不含等值键的审计")
	}

	callCount := len(h.runner.names())
	h.clock.Add(h.config.PatrolInterval + time.Minute)
	next, err := h.manager.Tick(context.Background())
	if err != nil || len(next.Rounds) != 0 || len(h.runner.names()) != callCount {
		t.Fatalf("人工处理前不得下一轮自动重试: next=%+v err=%v calls=%v", next, err, h.runner.names())
	}
}

func TestSnapshotMessagesCarriesMissingSourceKeyAsMissing(t *testing.T) {
	message := threadText(0, "legacy")
	snapshot := snapshotMessages([]protocol.ThreadMessage{message})
	if len(snapshot) != 1 || snapshot[0].SourceKey != "" {
		t.Fatalf("无 sourceKey 的旧快照不得伪造等值键: %+v", snapshot)
	}
}

func TestPatrolClassificationCorrectionPausesSuccessfullyBeforeM5(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5AdviceFixture(t, h)
	legacyText := "我暂时不考虑，祝你早日找到合适的人"
	timestamp := h.clock.Now().UnixMilli()
	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef, ConversationRef: fixture.conversationRef,
	}
	before, err := h.db.MessagesForConversation(key)
	if err != nil || len(before) != 2 {
		t.Fatalf("M5 fixture 活动账本错误: messages=%+v err=%v", before, err)
	}
	if _, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: before[len(before)-1].Seq,
		NewMessages: []store.MessageDraft{{
			Direction: "system", Kind: "system", ContentHash: syncledger.HashText(legacyText),
			Text: &legacyText, TsApproxMs: &timestamp, Origin: "external",
		}},
		SyncedAt: h.clock.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	ledger, err := h.db.MessagesForConversation(key)
	if err != nil || len(ledger) != 3 {
		t.Fatalf("无法预置旧 system 尾: messages=%+v err=%v", ledger, err)
	}
	thread := make([]protocol.ThreadMessage, len(ledger))
	for index := range ledger {
		thread[index] = protocolThreadMessageFromLedger(ledger[index], index)
	}
	correctedKey := strings.Repeat("4", 64)
	thread[len(thread)-1].Direction = protocol.MessageDirectionIn
	thread[len(thread)-1].Kind = protocol.MessageKindText
	thread[len(thread)-1].SourceKey = correctedKey
	conversation, err := h.db.ConversationByKey(key)
	if err != nil {
		t.Fatal(err)
	}
	h.manager.advice = &recordingAdviceExecutor{}
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary(key.ConversationRef, conversation.PlatformUserRef, legacyText, 1),
				},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			return protocol.ChatReadThreadData{
				Messages: thread,
				Peer: ptr(protocol.PeerSummary{
					DisplayName: "候选人", PlatformUserRef: conversation.PlatformUserRef,
				}),
				Complete: true, ReachedTop: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil ||
		result.Rounds[0].Status != "ok" {
		t.Fatalf("分类修正巡检轮必须成功收尾: result=%+v err=%v", result, err)
	}
	if result.ProjectionCount() != 0 || len(result.Rounds[0].Projections) != 0 {
		t.Fatalf("分类修正不是新业务消息，不得投影: %+v", result.Rounds[0].Projections)
	}
	active, err := h.db.MessagesForConversation(key)
	if err != nil || len(active) != 3 || active[0].Seq != 1 || active[1].Seq != 2 || active[2].Seq != 4 ||
		active[2].Direction != "in" || active[2].Kind != "text" || active[2].SourceKey == nil ||
		*active[2].SourceKey != correctedKey {
		t.Fatalf("巡检修正后的稀疏活动账本错误: messages=%+v err=%v", active, err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account.StoppedAt == nil || account.PausedReason != PauseUserRequested {
		t.Fatalf("成功修正后必须 userPaused 等待人工继续: account=%+v err=%v", account, err)
	}
	turn, err := h.db.DialogueTurnByID(fixture.turn.TurnID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnCollected {
		t.Fatalf("成功修正后不得进入同轮 M5: turn=%+v err=%v", turn, err)
	}
	if len(h.manager.advice.(*recordingAdviceExecutor).requests) != 0 {
		t.Fatal("成功修正后不得调用 AI 建议层")
	}
	if names := h.runner.names(); len(names) != 2 || names[0] != protocol.PrimChatReadList ||
		names[1] != protocol.PrimChatReadThread {
		t.Fatalf("成功修正后不得继续派发其他原语: %v", names)
	}
}

func TestPatrolUnsafeClassificationCorrectionStopsForManualReview(t *testing.T) {
	h := newHarness(t)
	legacyText := "我暂时不考虑，祝你早日找到合适的人"
	timestamp := h.clock.Now().UnixMilli()
	legacy := store.MessageDraft{
		Direction: "system", Kind: "system", ContentHash: syncledger.HashText(legacyText),
		Text: &legacyText, TsApproxMs: &timestamp, Origin: "external",
	}
	key := seedTracked(t, h, "conversation-unsafe-correction", "peer-unsafe-correction", []store.MessageDraft{legacy})
	corrected := protocol.ThreadMessage{
		Idx: 0, Direction: protocol.MessageDirectionIn, Kind: protocol.MessageKindText,
		Text: &legacyText, ContentHash: legacy.ContentHash, TsApprox: nil,
		SourceKey: strings.Repeat("5", 64),
	}
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary(key.ConversationRef, "peer-unsafe-correction", legacyText, 1),
				},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{corrected},
				Peer: ptr(protocol.PeerSummary{
					DisplayName: "候选人", PlatformUserRef: "peer-unsafe-correction",
				}),
				Complete: true, ReachedTop: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 ||
		!errors.Is(result.Rounds[0].Err, syncledger.ErrUnsafeMessageClassificationCorrection) {
		t.Fatalf("证据不全的候选修正必须响亮失败: result=%+v err=%v", result, err)
	}
	active, err := h.db.MessagesForConversation(key)
	if err != nil || len(active) != 1 || active[0].Direction != "system" || active[0].RetractedAt != nil {
		t.Fatalf("证据不全不得修正或重建基线: messages=%+v err=%v", active, err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account.StoppedAt == nil || account.PausedReason != PauseHandManualReview {
		t.Fatalf("unsafe 修正必须暂停账号等待人工: account=%+v err=%v", account, err)
	}
	audits, err := h.db.AuditEntries(20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, audit := range audits {
		if audit.Category != "conversation_classification_correction_unsafe" {
			continue
		}
		found = true
		for _, secret := range []string{corrected.SourceKey, legacy.ContentHash, legacyText} {
			if strings.Contains(audit.Detail, secret) {
				t.Fatalf("unsafe 审计泄露等值键/哈希/正文: %+v", audit)
			}
		}
	}
	if !found {
		t.Fatal("unsafe 修正必须留下独立人工收敛审计")
	}
	callCount := len(h.runner.names())
	h.clock.Add(h.config.PatrolInterval + time.Minute)
	next, err := h.manager.Tick(context.Background())
	if err != nil || len(next.Rounds) != 0 || len(h.runner.names()) != callCount {
		t.Fatalf("人工处理前不得自动重试 unsafe 修正: next=%+v err=%v calls=%v", next, err, h.runner.names())
	}
}

func TestConvergeStoreSourceKeyConflictStopsForManualReview(t *testing.T) {
	h := newHarness(t)
	actor := &roundActor{manager: h.manager, account: &store.Account{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
	}, roundID: "round-store-source-conflict"}
	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef, ConversationRef: "conversation-store-source-conflict",
	}
	err := actor.convergeReconcileFailure(key, store.ErrMessageSourceKeyConflict)
	if !errors.Is(err, store.ErrMessageSourceKeyConflict) {
		t.Fatalf("store sourceKey 冲突必须保留原错误: %v", err)
	}
	account, lookupErr := h.db.AccountByKey(h.key)
	if lookupErr != nil || account.StoppedAt == nil || account.PausedReason != PauseHandManualReview {
		t.Fatalf("ApplyPlan 的 store sourceKey 冲突必须暂停人工: account=%+v err=%v", account, lookupErr)
	}
	audits, _ := h.db.AuditEntries(20)
	found := false
	for _, audit := range audits {
		if audit.Category == "conversation_source_identity_conflict" &&
			audit.ConversationRef == key.ConversationRef {
			found = true
		}
	}
	if !found {
		t.Fatal("store sourceKey 冲突必须走稳定身份冲突审计")
	}
}

func protocolThreadMessageFromLedger(message store.Message, index int) protocol.ThreadMessage {
	sourceKey := ""
	if message.SourceKey != nil {
		sourceKey = *message.SourceKey
	}
	return protocol.ThreadMessage{
		Idx: index, Direction: protocol.MessageDirection(message.Direction),
		Kind: protocol.MessageKind(message.Kind), Text: message.Text,
		ContentHash: message.ContentHash, TsApprox: message.TsApproxMs, SourceKey: sourceKey,
	}
}
