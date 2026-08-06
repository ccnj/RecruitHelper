package patrol

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

type wechatCollectFixture struct {
	profileID       string
	conversationRef string
	inviteSourceKey string
	actor           *roundActor
}

// seedExchangedWechatConversation 复现两种真机形态。
// 我方发起(withOutboundInvite=true):我方发出邀请卡(out/pending,带稳定键),
// 平台不翻转它;候选人同意表现为新增一条归属候选人方向的结果卡(in/accepted)。
// withOutboundInvite=false 时只落 in/accepted 结果卡、不落任何请求卡,用于验证
// 无锚时本触发器不介入。候选人主动发起的形态由
// seedCandidateInitiatedWechatConversation 单独构造(请求卡 in、结果卡 out)。
func seedExchangedWechatConversation(
	t *testing.T,
	h *harness,
	suffix string,
	withOutboundInvite bool,
) wechatCollectFixture {
	t.Helper()
	target := seedCommunicationV4PatrolTarget(t, h, "wechat-collect-"+suffix, "我想了解这个岗位")
	key := store.ConversationKey{
		Platform:        h.key.Platform,
		AccountRef:      h.key.AccountRef,
		ConversationRef: target.conversationRef,
	}
	roundID := "round-wechat-collect-" + suffix
	beginCommunicationV4PatrolRound(t, h, roundID)

	project := func(message store.Message, at time.Time) {
		t.Helper()
		event, err := communication.NormalizeLedgerMessage(communication.LedgerMessageFact{
			Seq: message.Seq, Direction: message.Direction, Kind: message.Kind,
			Text: message.Text, CardType: message.CardType, CardState: message.CardState,
			Origin: message.Origin, TsApproxMs: message.TsApproxMs,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.db.ApplyCommunicationV4BusinessEvent(
			store.ApplyCommunicationV4BusinessEventRequest{
				ProfileID: target.profileID, Event: event, AppliedAt: at,
			},
		); err != nil {
			t.Fatalf("投影 %s 卡失败: %v", message.Direction, err)
		}
	}

	// 先把种子里的候选人文字投影掉:消息来源事件必须严格接在投影游标之后。
	inbound, err := h.db.MessageBySeq(key, target.inboundSeq)
	if err != nil || inbound == nil {
		t.Fatalf("读取候选人消息失败: message=%+v err=%v", inbound, err)
	}
	project(*inbound, h.clock.Now())

	tailSeq := target.inboundSeq
	inviteSourceKey := ""
	if withOutboundInvite {
		inviteText := "[换微信请求]"
		inviteSourceKey = syncledger.HashText("wechat-invite-" + suffix)
		result, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
			Key: key, RoundID: roundID, ExpectedTailSeq: tailSeq,
			NewMessages: []store.MessageDraft{{
				Direction: "out", Kind: "card",
				ContentHash: syncledger.HashText("invite\x1f" + suffix),
				Text:        &inviteText, CardType: "wechatExchange", CardState: "pending",
				Origin: "self", SourceKey: &inviteSourceKey,
			}},
			SyncedAt: h.clock.Now().Add(time.Minute),
		})
		if err != nil || len(result.Inserted) != 1 {
			t.Fatalf("追加我方邀请卡: result=%+v err=%v", result, err)
		}
		project(result.Inserted[0], h.clock.Now().Add(time.Minute))
		tailSeq = result.Inserted[0].Seq
	}

	acceptText := "[微信交换成功]"
	acceptSourceKey := syncledger.HashText("wechat-accept-" + suffix)
	accepted, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, RoundID: roundID, ExpectedTailSeq: tailSeq,
		NewMessages: []store.MessageDraft{{
			Direction: "in", Kind: "card",
			ContentHash: syncledger.HashText("accept\x1f" + suffix),
			Text:        &acceptText, CardType: "wechatExchange", CardState: "accepted",
			Origin: "external", SourceKey: &acceptSourceKey,
		}},
		SyncedAt: h.clock.Now().Add(2 * time.Minute),
	})
	if err != nil || len(accepted.Inserted) != 1 {
		t.Fatalf("追加候选人同意卡: result=%+v err=%v", accepted, err)
	}
	project(accepted.Inserted[0], h.clock.Now().Add(2*time.Minute))

	aggregate, err := h.db.CommunicationV4AggregateByProfile(target.profileID)
	if err != nil || aggregate.State.WechatState != communication.V4WechatExchanged {
		t.Fatalf("微信线未进入已换号: aggregate=%+v err=%v", aggregate, err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("账号读取失败: account=%+v err=%v", account, err)
	}
	return wechatCollectFixture{
		profileID: target.profileID, conversationRef: target.conversationRef,
		inviteSourceKey: inviteSourceKey,
		actor: &roundActor{
			manager: h.manager, account: account,
			hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
			roundID: roundID, now: h.clock.Now(),
		},
	}
}

// 候选人主动发起(形态 A):请求卡是 in/pending,交换结果卡归属点同意的一方——
// 我方——所以是 out/accepted。2026-07-28 生产页面直读。收号锚必须是那张 in
// 请求卡;若误取 out 结果卡,原语锚不到 105,收号永远阴性。
func seedCandidateInitiatedWechatConversation(
	t *testing.T,
	h *harness,
	suffix string,
) wechatCollectFixture {
	t.Helper()
	target := seedCommunicationV4PatrolTarget(t, h, "wechat-collect-"+suffix, "我想了解这个岗位")
	key := store.ConversationKey{
		Platform:        h.key.Platform,
		AccountRef:      h.key.AccountRef,
		ConversationRef: target.conversationRef,
	}
	roundID := "round-wechat-collect-" + suffix
	beginCommunicationV4PatrolRound(t, h, roundID)

	requestText := "[交换微信请求]"
	requestSourceKey := syncledger.HashText("wechat-request-" + suffix)
	resultText := "[微信交换成功]"
	resultSourceKey := syncledger.HashText("wechat-result-" + suffix)
	appended, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, RoundID: roundID, ExpectedTailSeq: target.inboundSeq,
		NewMessages: []store.MessageDraft{
			{
				Direction: "in", Kind: "card",
				ContentHash: syncledger.HashText("request\x1f" + suffix),
				Text:        &requestText, CardType: "wechatExchange", CardState: "pending",
				Origin: "external", SourceKey: &requestSourceKey,
			},
			{
				Direction: "out", Kind: "card",
				ContentHash: syncledger.HashText("result\x1f" + suffix),
				Text:        &resultText, CardType: "wechatExchange", CardState: "accepted",
				Origin: "external", SourceKey: &resultSourceKey,
			},
		},
		SyncedAt: h.clock.Now().Add(time.Minute),
	})
	if err != nil || len(appended.Inserted) != 2 {
		t.Fatalf("追加形态 A 请求卡与结果卡: result=%+v err=%v", appended, err)
	}
	// 投影游标必须逐条推进：种子里的候选人文字 → in 请求卡 → out 结果卡。
	project := func(message store.Message, at time.Time) communication.BusinessEvent {
		t.Helper()
		event, err := communication.NormalizeLedgerMessage(communication.LedgerMessageFact{
			Seq: message.Seq, Direction: message.Direction, Kind: message.Kind,
			Text: message.Text, CardType: message.CardType, CardState: message.CardState,
			Origin: message.Origin, TsApproxMs: message.TsApproxMs,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.db.ApplyCommunicationV4BusinessEvent(
			store.ApplyCommunicationV4BusinessEventRequest{
				ProfileID: target.profileID, Event: event, AppliedAt: at,
			},
		); err != nil {
			t.Fatalf("投影 seq=%d 失败: %v", message.Seq, err)
		}
		return event
	}
	inbound, err := h.db.MessageBySeq(key, target.inboundSeq)
	if err != nil || inbound == nil {
		t.Fatalf("读取候选人消息失败: message=%+v err=%v", inbound, err)
	}
	project(*inbound, h.clock.Now())
	project(appended.Inserted[0], h.clock.Now().Add(time.Minute))
	if event := project(appended.Inserted[1], h.clock.Now().Add(2*time.Minute)); //
	event.Kind != communication.EventWechatExchanged {
		t.Fatalf("出站交换结果卡未被读成已换号: event=%+v", event)
	}
	aggregate, err := h.db.CommunicationV4AggregateByProfile(target.profileID)
	if err != nil || aggregate.State.WechatState != communication.V4WechatExchanged {
		t.Fatalf("形态 A 微信线未进入已换号: aggregate=%+v err=%v", aggregate, err)
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

func runCollect(t *testing.T, h *harness, fixture wechatCollectFixture) error {
	t.Helper()
	h.manager.mu.Lock()
	defer h.manager.mu.Unlock()
	return fixture.actor.collectExchangedWechatContact(context.Background(), fixture.profileID)
}

// 我方发起、对方接受:平台从不翻转我方卡,收号必须由已持久的"已换号"状态驱动;
// 收编成功后运营通知同事务入队。
func TestCollectExchangedWechatContactRecordsAssetAndEnqueues(t *testing.T) {
	h := newHarness(t)
	fixture := seedExchangedWechatConversation(t, h, "happy", true)
	exchangeSourceKey := strings.Repeat("e", 64)
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadWechatExchangeOutcome {
			return protocol.ChatReadWechatExchangeOutcomeData{
				Confirmed: true, ExchangeSourceKey: exchangeSourceKey,
				PeerWechat: "synthetic-peer-wechat", ObservedAt: h.clock.Now().UnixMilli(),
			}, nil
		}
		return defaultHandler(request)
	}

	if err := runCollect(t, h, fixture); err != nil {
		t.Fatal(err)
	}
	assets, err := h.db.ContactAssetsByProfile(fixture.profileID)
	if err != nil || len(assets) != 1 ||
		assets[0].RequestSourceKey != fixture.inviteSourceKey ||
		assets[0].SourceKey != exchangeSourceKey ||
		assets[0].Value != "synthetic-peer-wechat" ||
		assets[0].EffectIntentID != nil {
		t.Fatalf("微信资产未按只读收编落账: assets=%+v err=%v", assets, err)
	}
	pending, err := h.db.NotificationsNeedingCapture(fixture.profileID)
	if err != nil || len(pending) != 1 ||
		pending[0].NotifyType != store.NotificationTypeWechatAdded {
		t.Fatalf("收编未同事务入队运营通知: pending=%+v err=%v", pending, err)
	}
	if h.runner.count(protocol.PrimChatReadWechatExchangeOutcome) != 1 {
		t.Fatalf("收号读次数不符: %v", h.runner.names())
	}
	for _, name := range h.runner.names() {
		if name != protocol.PrimChatReadThread &&
			name != protocol.PrimChatReadWechatExchangeOutcome {
			t.Fatalf("收号链不得触发候选人可见动作: %v", h.runner.names())
		}
	}

	// 幂等:资产已在,第二轮不再重复读、不再重复入队。
	before := h.runner.count(protocol.PrimChatReadWechatExchangeOutcome)
	if err := runCollect(t, h, fixture); err != nil {
		t.Fatal(err)
	}
	if h.runner.count(protocol.PrimChatReadWechatExchangeOutcome) != before {
		t.Fatalf("资产已存在仍重复收号: %v", h.runner.names())
	}
	assetsAfter, _ := h.db.ContactAssetsByProfile(fixture.profileID)
	if len(assetsAfter) != 1 {
		t.Fatalf("重复收号增生资产: %+v", assetsAfter)
	}
}

// 形态 A 兜底:接受动作当场没取到号(259 晚到)时,本触发器补收。
// 2026-08-06 甲方裁决后不再把请求锚传给手(页面首屏窗口未必还挂着那张卡),
// 但账本里有请求卡时,资产留档仍记它——与 acceptWechat 落账口径一致。
func TestCollectExchangedWechatContactCollectsCandidateInitiatedForm(t *testing.T) {
	h := newHarness(t)
	fixture := seedCandidateInitiatedWechatConversation(t, h, "candidate-initiated")
	exchangeSourceKey := strings.Repeat("d", 64)
	var anchored string
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadWechatExchangeOutcome {
			var args protocol.ChatReadWechatExchangeOutcomeArgs
			if err := json.Unmarshal(request.Args, &args); err != nil {
				t.Errorf("收号读参数解析失败: %v", err)
				return nil, errors.New("bad args")
			}
			anchored = args.RequestSourceKey
			return protocol.ChatReadWechatExchangeOutcomeData{
				Confirmed: true, ExchangeSourceKey: exchangeSourceKey,
				PeerWechat: "synthetic-peer-wechat-a", ObservedAt: h.clock.Now().UnixMilli(),
			}, nil
		}
		return defaultHandler(request)
	}
	if err := runCollect(t, h, fixture); err != nil {
		t.Fatal(err)
	}
	if anchored != "" {
		t.Fatalf("收号读不得再携带请求锚: got=%q", anchored)
	}
	assets, err := h.db.ContactAssetsByProfile(fixture.profileID)
	if err != nil || len(assets) != 1 ||
		assets[0].RequestSourceKey != fixture.inviteSourceKey ||
		assets[0].Value != "synthetic-peer-wechat-a" {
		t.Fatalf("形态 A 微信资产未落账: assets=%+v err=%v", assets, err)
	}
	pending, err := h.db.NotificationsNeedingCapture(fixture.profileID)
	if err != nil || len(pending) != 1 ||
		pending[0].NotifyType != store.NotificationTypeWechatAdded {
		t.Fatalf("形态 A 收编未同事务入队运营通知: pending=%+v err=%v", pending, err)
	}
}

// 账本里没有任何请求卡时照常收号(2026-08-06 甲方裁决):号的载体是结果消息,
// 请求卡只用于资产留档;拿不到留档锚就退化为结果消息自身,不因此放弃收号。
// 旧行为(无锚即完全不介入)会让这类会话永远收不到号。
func TestCollectExchangedWechatContactWithoutRequestCardStillCollects(t *testing.T) {
	h := newHarness(t)
	fixture := seedExchangedWechatConversation(t, h, "no-request-card", false)
	exchangeSourceKey := strings.Repeat("e", 64)
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadWechatExchangeOutcome {
			var args protocol.ChatReadWechatExchangeOutcomeArgs
			if err := json.Unmarshal(request.Args, &args); err != nil {
				t.Errorf("收号读参数解析失败: %v", err)
				return nil, errors.New("bad args")
			}
			if args.RequestSourceKey != "" {
				t.Errorf("收号读不得携带请求锚: %q", args.RequestSourceKey)
			}
			return protocol.ChatReadWechatExchangeOutcomeData{
				Confirmed: true, ExchangeSourceKey: exchangeSourceKey,
				PeerWechat: "synthetic-peer-wechat-b", ObservedAt: h.clock.Now().UnixMilli(),
			}, nil
		}
		return defaultHandler(request)
	}
	if err := runCollect(t, h, fixture); err != nil {
		t.Fatal(err)
	}
	assets, err := h.db.ContactAssetsByProfile(fixture.profileID)
	if err != nil || len(assets) != 1 ||
		assets[0].Value != "synthetic-peer-wechat-b" {
		t.Fatalf("无请求卡时仍应收编资产: assets=%+v err=%v", assets, err)
	}
	if assets[0].RequestSourceKey != exchangeSourceKey {
		t.Fatalf("无请求卡时留档锚应退化为结果锚: got=%q want=%q",
			assets[0].RequestSourceKey, exchangeSourceKey)
	}
}

// 正证不足(未确认/字段缺失)只记为"本轮没收到号":不建资产、不入队、不报错,
// 下一轮巡检自然重试。
func TestCollectExchangedWechatContactUnconfirmedStaysSilent(t *testing.T) {
	testCases := []struct {
		name string
		data protocol.ChatReadWechatExchangeOutcomeData
	}{
		{"未确认", protocol.ChatReadWechatExchangeOutcomeData{Confirmed: false}},
		{"确认但缺字段", protocol.ChatReadWechatExchangeOutcomeData{Confirmed: true}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			h := newHarness(t)
			fixture := seedExchangedWechatConversation(t, h, "unconfirmed-"+testCase.name, true)
			data := testCase.data
			data.ObservedAt = h.clock.Now().UnixMilli()
			h.runner.handler = func(request RunRequest) (any, error) {
				if request.Name == protocol.PrimChatReadWechatExchangeOutcome {
					return data, nil
				}
				return defaultHandler(request)
			}
			if err := runCollect(t, h, fixture); err != nil {
				t.Fatalf("正证不足不得让巡检失败: %v", err)
			}
			assets, err := h.db.ContactAssetsByProfile(fixture.profileID)
			if err != nil || len(assets) != 0 {
				t.Fatalf("正证不足不得建资产: assets=%+v err=%v", assets, err)
			}
			pending, err := h.db.NotificationsNeedingCapture(fixture.profileID)
			if err != nil || len(pending) != 0 {
				t.Fatalf("正证不足不得入队通知: pending=%+v err=%v", pending, err)
			}
		})
	}
}

// 收号读失败(页面未就绪等)同样只跳过本轮,不阻塞巡检收束。
func TestCollectExchangedWechatContactPrimitiveFailureDoesNotBlockPatrol(t *testing.T) {
	h := newHarness(t)
	fixture := seedExchangedWechatConversation(t, h, "primitive-failure", true)
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadWechatExchangeOutcome {
			return nil, errors.New("页面尚未就绪")
		}
		return defaultHandler(request)
	}
	if err := runCollect(t, h, fixture); err != nil {
		t.Fatalf("收号失败不得让巡检失败: %v", err)
	}
	assets, _ := h.db.ContactAssetsByProfile(fixture.profileID)
	if len(assets) != 0 {
		t.Fatalf("收号失败不得建资产: %+v", assets)
	}
}
