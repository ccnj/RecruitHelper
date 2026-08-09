package patrol

import (
	"context"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

// 卡片插话集成模拟(战役出口第五件测试,四幕只钉文本):候选人插话夹在
// 入站消息与我方邀请卡之间,同一窗口里卡片还发生了 pending→accepted 跃迁。
// 身份判新必须同时做到:插话按身份捞回收编、卡片跃迁按 sourceKey 配对不受
// 位置漂移影响;位置影子引擎在此裁弃插话,分歧留审计。第二轮重读同一页面
// 必须收敛幂等,跃迁不增生。
func TestSimulationCardInterjectionRescuedAndTransitionPairs(t *testing.T) {
	h := newHarness(t)
	inbound := "想先了解一下再说"
	cardHash := syncledger.WechatExchangeContentHash()
	cardKey := syncledger.HashText("identity-card-interleave")
	cardText := "合成换微信卡"
	cardDraft := store.MessageDraft{
		Direction: "out", Kind: "card", ContentHash: cardHash,
		Text: ptr(cardText), CardType: "wechatExchange", CardState: "pending",
		Origin: "self", SourceKey: ptr(cardKey),
	}
	key := seedTracked(t, h, "card-interleave", "peer-card-interleave", []store.MessageDraft{
		draftText(inbound), cardDraft,
	})

	interjection := "先不加微信,谢谢"
	acceptedState := protocol.CardStateAccepted
	cardType := protocol.CardTypeWechatExchange
	page := func() []protocol.ThreadMessage {
		interjectionText := interjection
		rows := []protocol.ThreadMessage{
			threadText(0, inbound),
			{
				Idx: 1, Direction: protocol.MessageDirectionIn, Kind: protocol.MessageKindText,
				Text: &interjectionText, ContentHash: syncledger.HashText(interjection),
				SourceKey: fixtureSourceKey(interjection),
			},
			{
				Idx: 2, Direction: protocol.MessageDirectionOut, Kind: protocol.MessageKindCard,
				ContentHash: cardHash, CardType: &cardType, CardState: &acceptedState,
				SourceKey: cardKey,
			},
		}
		return rows
	}
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary("card-interleave", "peer-card-interleave", interjection, 1),
				},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			return protocol.ChatReadThreadData{
				Messages: page(),
				Peer:     ptr(protocol.PeerSummary{DisplayName: "候选人", PlatformUserRef: "peer-card-interleave"}),
				Complete: true, ReachedTop: true, AnchorMatched: false,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	h.clock.Add(31 * time.Minute)
	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("卡片插话对账失败: result=%+v err=%v", result, err)
	}
	if result.ProjectionCount() != 2 {
		t.Fatalf("应恰好投影 插话消息+卡片跃迁 两项: %d", result.ProjectionCount())
	}
	if len(result.Rounds[0].Projections) != 1 ||
		len(result.Rounds[0].Projections[0].CardTransitions) != 1 ||
		result.Rounds[0].Projections[0].CardTransitions[0].From != "pending" ||
		result.Rounds[0].Projections[0].CardTransitions[0].To != "accepted" {
		t.Fatalf("卡片跃迁必须按身份配对并投影: %+v", result.Rounds[0].Projections)
	}

	messages, err := h.db.MessagesForConversation(key)
	if err != nil || len(messages) != 3 {
		t.Fatalf("账本应为 入站+卡+捞回插话 三行: messages=%+v err=%v", messages, err)
	}
	tail := messages[len(messages)-1]
	if tail.Text == nil || *tail.Text != interjection || tail.SourceKey == nil {
		t.Fatalf("插话必须带身份收编在尾部: %+v", tail)
	}
	var cardRow *store.Message
	for i := range messages {
		if messages[i].Kind == "card" {
			cardRow = &messages[i]
		}
	}
	if cardRow == nil || cardRow.CardState != "accepted" {
		t.Fatalf("卡片状态未跃迁到 accepted: %+v", cardRow)
	}
	if got := countContextDiscardAudits(t, h, "card-interleave"); got != 0 {
		t.Fatalf("身份判新不得裁弃插话: discard=%d", got)
	}
	audits, err := h.db.AuditEntries(50)
	if err != nil || countAudit(audits, "identity_shadow_divergence") == 0 {
		t.Fatalf("位置影子裁弃插话的分歧必须留审计: err=%v", err)
	}

	// 第二轮重读同一页面:收敛幂等,零新增、跃迁不增生。
	h.clock.Add(31 * time.Minute)
	result, err = h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("第二轮对账失败: result=%+v err=%v", result, err)
	}
	if result.ProjectionCount() != 0 {
		t.Fatalf("已收敛页面不得再投影: %d", result.ProjectionCount())
	}
	after, err := h.db.MessagesForConversation(key)
	if err != nil || len(after) != 3 {
		t.Fatalf("第二轮账本必须不变: messages=%+v err=%v", after, err)
	}
}
