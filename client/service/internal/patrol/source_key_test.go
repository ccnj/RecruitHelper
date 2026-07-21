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
