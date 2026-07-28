package patrol

import (
	"context"
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

// 形态 A 接受取得正证后，本轮定向重对账必须把 259 结果卡补进账本；
// 否则微信线状态与客户端统计要等下一轮才跟上，与已发出的运营通知自相矛盾。
func TestReconcileAfterWechatAcceptedPullsExchangeResultSameRound(t *testing.T) {
	h := newHarness(t)
	fixture := seedCandidateInitiatedWechatConversationWithoutResult(t, h, "accept-reconcile")
	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: fixture.conversationRef,
	}

	resultText := "[微信交换成功]"
	resultSourceKey := syncledger.HashText("wechat-result-accept-reconcile")
	reads := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name != protocol.PrimChatReadThread {
			return defaultHandler(request)
		}
		reads++
		cardType := protocol.CardTypeWechatExchange
		cardState := protocol.CardStateAccepted
		messages := echoLedgerAsThread(t, h, key)
		messages = append(messages, protocol.ThreadMessage{
			Idx: len(messages), Direction: protocol.MessageDirectionOut,
			Kind: protocol.MessageKindCard, Text: &resultText,
			ContentHash: syncledger.WechatExchangeContentHash(),
			CardType:    &cardType,
			CardState:   &cardState,
			SourceKey:   resultSourceKey,
		})
		return protocol.ChatReadThreadData{
			Messages: messages, Peer: nil, Complete: true, ReachedTop: true,
		}, nil
	}

	// 未登记接受正证时不得重对账：这不是每轮都跑的通用读。
	if err := runAcceptReconcile(t, h, fixture); err != nil {
		t.Fatal(err)
	}
	if reads != 0 {
		t.Fatalf("未登记接受正证却发起了重对账: reads=%d", reads)
	}

	fixture.actor.wechatAcceptedProfiles = map[string]struct{}{fixture.profileID: {}}
	if err := runAcceptReconcile(t, h, fixture); err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Fatalf("定向重对账读次数不符: reads=%d", reads)
	}
	messages, err := h.db.MessagesForConversation(key)
	if err != nil {
		t.Fatal(err)
	}
	tail := messages[len(messages)-1]
	if tail.CardType != "wechatExchange" || tail.CardState != "accepted" ||
		tail.Direction != "out" || tail.SourceKey == nil || *tail.SourceKey != resultSourceKey {
		t.Fatalf("交换结果卡未在本轮进入账本: tail=%+v", tail)
	}
	// 补进账本还不够：微信线必须当轮就是"已换号"，随后的收号与运营通知才跑得起来。
	aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || aggregate.State.WechatState != communication.V4WechatExchanged {
		t.Fatalf("微信线未在本轮推进到已换号: aggregate=%+v err=%v", aggregate, err)
	}

	// 登记只消费一次：重对账是"让本轮跟上"的加速，不是每轮的必经汇点。
	if err := runAcceptReconcile(t, h, fixture); err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Fatalf("接受登记被重复消费: reads=%d", reads)
	}
}

// 重对账失败只回退为"下轮再说"：不返回错误、不阻断收号与通知、不重试。
func TestReconcileAfterWechatAcceptedFailureDoesNotBlockRound(t *testing.T) {
	h := newHarness(t)
	fixture := seedCandidateInitiatedWechatConversationWithoutResult(t, h, "accept-reconcile-fail")
	fixture.actor.wechatAcceptedProfiles = map[string]struct{}{fixture.profileID: {}}
	reads := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name != protocol.PrimChatReadThread {
			return defaultHandler(request)
		}
		reads++
		return nil, errors.New("synthetic read failure")
	}
	if err := runAcceptReconcile(t, h, fixture); err != nil {
		t.Fatalf("重对账失败必须降级而不是上抛: %v", err)
	}
	if reads != 1 {
		t.Fatalf("重对账失败后不得重试: reads=%d", reads)
	}
}

// 把账本原样回声成页面快照。必须带上卡片类型/状态：对账按
// direction+kind+hash+cardType+sourceKey 比对前缀，漏字段会被判成零重叠。
func echoLedgerAsThread(
	t *testing.T,
	h *harness,
	key store.ConversationKey,
) []protocol.ThreadMessage {
	t.Helper()
	rows, err := h.db.MessagesForConversation(key)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]protocol.ThreadMessage, len(rows))
	for index := range rows {
		row := rows[index]
		out[index] = protocol.ThreadMessage{
			Idx: index, Direction: protocol.MessageDirection(row.Direction),
			Kind: protocol.MessageKind(row.Kind), Text: row.Text,
			ContentHash: row.ContentHash,
		}
		if row.CardType != "" {
			cardType := protocol.CardType(row.CardType)
			out[index].CardType = &cardType
		}
		if row.CardState != "" {
			cardState := protocol.CardState(row.CardState)
			out[index].CardState = &cardState
		}
		if row.SourceKey != nil {
			out[index].SourceKey = *row.SourceKey
		}
		if row.TsApproxMs != nil {
			value := *row.TsApproxMs
			out[index].TsApprox = &value
		}
	}
	return out
}

func runAcceptReconcile(t *testing.T, h *harness, fixture wechatCollectFixture) error {
	t.Helper()
	h.manager.mu.Lock()
	defer h.manager.mu.Unlock()
	return fixture.actor.reconcileAfterWechatAccepted(context.Background(), fixture.profileID)
}

// 接受动作刚取得正证的现场：账本里只有候选人那张 in 请求卡，259 结果尚未对账。
func seedCandidateInitiatedWechatConversationWithoutResult(
	t *testing.T,
	h *harness,
	suffix string,
) wechatCollectFixture {
	t.Helper()
	target := seedCommunicationV4PatrolTarget(t, h, "wechat-accept-"+suffix, "我想了解这个岗位")
	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: target.conversationRef,
	}
	roundID := "round-wechat-accept-" + suffix
	beginCommunicationV4PatrolRound(t, h, roundID)

	requestText := "[交换微信请求]"
	requestSourceKey := syncledger.HashText("wechat-request-" + suffix)
	appended, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, RoundID: roundID, ExpectedTailSeq: target.inboundSeq,
		NewMessages: []store.MessageDraft{{
			Direction: "in", Kind: "card",
			ContentHash: syncledger.WechatExchangeContentHash(),
			Text:        &requestText, CardType: "wechatExchange", CardState: "pending",
			Origin: "external", SourceKey: &requestSourceKey,
		}},
		SyncedAt: h.clock.Now().Add(time.Minute),
	})
	if err != nil || len(appended.Inserted) != 1 {
		t.Fatalf("追加候选人请求卡: result=%+v err=%v", appended, err)
	}
	// 真机现场里候选人这一侧已经在接受动作之前被消费掉了(请求卡开的轮已冻结)，
	// 投影游标停在请求卡上。若留着未投影，重对账后的边界会同时含 in 与 out，
	// 被既有交错守卫判成 manualRequired——那是测试造型失真，不是产品行为。
	inbound, err := h.db.MessageBySeq(key, target.inboundSeq)
	if err != nil || inbound == nil {
		t.Fatalf("读取候选人消息失败: message=%+v err=%v", inbound, err)
	}
	for _, message := range []store.Message{*inbound, appended.Inserted[0]} {
		event, normalizeErr := communication.NormalizeLedgerMessage(communication.LedgerMessageFact{
			Seq: message.Seq, Direction: message.Direction, Kind: message.Kind,
			Text: message.Text, CardType: message.CardType, CardState: message.CardState,
			Origin: message.Origin, TsApproxMs: message.TsApproxMs,
		})
		if normalizeErr != nil {
			t.Fatal(normalizeErr)
		}
		if _, applyErr := h.db.ApplyCommunicationV4BusinessEvent(
			store.ApplyCommunicationV4BusinessEventRequest{
				ProfileID: target.profileID, Event: event,
				AppliedAt: h.clock.Now().Add(time.Minute),
			},
		); applyErr != nil {
			t.Fatalf("投影 seq=%d 失败: %v", message.Seq, applyErr)
		}
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("账号读取失败: account=%+v err=%v", account, err)
	}
	return wechatCollectFixture{
		profileID: target.profileID, conversationRef: target.conversationRef,
		inviteSourceKey: requestSourceKey,
		actor: &roundActor{
			manager: h.manager, account: account,
			hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
			roundID: roundID, now: h.clock.Now(),
		},
	}
}
