package patrol

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

type communicationV4CardTransitionFixture struct {
	target    communicationV4PatrolFixture
	key       store.ConversationKey
	cardSeq   int64
	pending   store.PendingCardTransition
	profile   store.CandidateProfile
	actor     *roundActor
	revision  uint64
	sourceKey string
}

func seedCommunicationV4PendingInterviewTransition(
	t *testing.T,
	h *harness,
	suffix string,
	toState string,
) communicationV4CardTransitionFixture {
	return seedCommunicationV4PendingInterviewTransitionWithCardProjection(
		t,
		h,
		suffix,
		toState,
		true,
	)
}

func seedCommunicationV4PendingInterviewTransitionWithCardProjection(
	t *testing.T,
	h *harness,
	suffix string,
	toState string,
	projectCard bool,
) communicationV4CardTransitionFixture {
	return seedCommunicationV4PendingCardTransitionWithCardProjection(
		t,
		h,
		suffix,
		"interviewInvite",
		toState,
		projectCard,
	)
}

func seedCommunicationV4PendingCardTransitionWithCardProjection(
	t *testing.T,
	h *harness,
	suffix string,
	cardType string,
	toState string,
	projectCard bool,
) communicationV4CardTransitionFixture {
	t.Helper()
	target := seedCommunicationV4PatrolTarget(
		t,
		h,
		"card-transition-"+suffix,
		"我想继续了解岗位",
	)
	key := store.ConversationKey{
		Platform:        h.key.Platform,
		AccountRef:      h.key.AccountRef,
		ConversationRef: target.conversationRef,
	}
	inbound, err := h.db.MessageBySeq(key, target.inboundSeq)
	if err != nil || inbound == nil {
		t.Fatalf("读取候选人消息失败: message=%+v err=%v", inbound, err)
	}
	inboundEvent, err := communication.NormalizeLedgerMessage(
		communication.LedgerMessageFact{
			Seq: inbound.Seq, Direction: inbound.Direction, Kind: inbound.Kind,
			Text: inbound.Text, CardType: inbound.CardType, CardState: inbound.CardState,
			Origin: inbound.Origin, TsApproxMs: inbound.TsApproxMs,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.ApplyCommunicationV4BusinessEvent(
		store.ApplyCommunicationV4BusinessEventRequest{
			ProfileID: target.profileID,
			Event:     inboundEvent,
			AppliedAt: h.clock.Now(),
		},
	); err != nil {
		t.Fatalf("投影候选人消息: %v", err)
	}

	roundID := "round-v4-card-transition-" + suffix
	beginCommunicationV4PatrolRound(t, h, roundID)
	cardText := "合成卡片"
	cardHash := syncledger.HashText("card\x1f" + cardType + "\x1f" + suffix)
	sourceKey := syncledger.HashText("source-key-" + suffix)
	appendResult, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, RoundID: roundID, ExpectedTailSeq: target.inboundSeq,
		NewMessages: []store.MessageDraft{{
			Direction: "out", Kind: "card", ContentHash: cardHash,
			Text: &cardText, CardType: cardType, CardState: "pending",
			Origin: "self", SourceKey: &sourceKey,
		}},
		SyncedAt: h.clock.Now().Add(time.Minute),
	})
	if err != nil || len(appendResult.Inserted) != 1 {
		t.Fatalf("追加邀面卡: result=%+v err=%v", appendResult, err)
	}
	card := appendResult.Inserted[0]
	cardEvent, err := communication.NormalizeLedgerMessage(
		communication.LedgerMessageFact{
			Seq: card.Seq, Direction: card.Direction, Kind: card.Kind,
			Text: card.Text, CardType: card.CardType, CardState: card.CardState,
			Origin: card.Origin, TsApproxMs: card.TsApproxMs,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if projectCard {
		if _, err := h.db.ApplyCommunicationV4BusinessEvent(
			store.ApplyCommunicationV4BusinessEventRequest{
				ProfileID: target.profileID,
				Event:     cardEvent,
				AppliedAt: h.clock.Now().Add(time.Minute),
			},
		); err != nil {
			t.Fatalf("投影邀面卡: %v", err)
		}
	}
	transitionAt := h.clock.Now().Add(2 * time.Minute)
	change := store.ApplyConversationChangesRequest{
		Key: key, RoundID: roundID, ExpectedTailSeq: card.Seq,
		CardChanges: []store.CardStateChange{{
			Seq: card.Seq, ContentHash: cardHash,
			FromState: "pending", CardState: toState,
		}},
		SyncedAt: transitionAt,
	}
	if _, err := h.db.ApplyConversationChanges(change); err != nil {
		t.Fatalf("追加卡片跃迁: %v", err)
	}
	// 同一页面事实可能在下一轮重复对账；消息 sourceKey 与跃迁主键必须
	// 共同保证它不会增生第二条 pending 或第二次 V4 输入。
	if _, err := h.db.ApplyConversationChanges(change); err != nil {
		t.Fatalf("重复卡片对账必须幂等: %v", err)
	}
	pending, err := h.db.PendingCardTransitionsForAccount(h.key, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("卡片跃迁 pending 不唯一: pending=%+v err=%v", pending, err)
	}
	if pending[0].Message.SourceKey == nil || *pending[0].Message.SourceKey != sourceKey {
		t.Fatalf("活动卡片稳定 sourceKey 未保留: %+v", pending[0].Message)
	}
	profile, err := h.db.CandidateProfileByID(target.profileID)
	if err != nil || profile == nil {
		t.Fatalf("读取卡片档案: profile=%+v err=%v", profile, err)
	}
	aggregate, err := h.db.CommunicationV4AggregateByProfile(target.profileID)
	if err != nil {
		t.Fatal(err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("读取账号: account=%+v err=%v", account, err)
	}
	return communicationV4CardTransitionFixture{
		target: target, key: key, cardSeq: card.Seq,
		pending: pending[0], profile: *profile,
		actor: &roundActor{
			manager: h.manager,
			account: account,
			hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
			roundID: roundID,
			now:     h.clock.Now(),
		},
		revision:  aggregate.Revision,
		sourceKey: sourceKey,
	}
}

func TestCommunicationV4CardTransitionProjectsAcceptedAndRejectedThenAcknowledges(
	t *testing.T,
) {
	for _, testCase := range []struct {
		name    string
		toState string
		assert  func(*testing.T, communication.V4State, int64)
	}{
		{
			name: "accepted", toState: "accepted",
			assert: func(t *testing.T, state communication.V4State, cardSeq int64) {
				t.Helper()
				if state.MainStatus != communication.V4StatusInterviewed {
					t.Fatalf("接受邀面卡未进入 interviewed: %+v", state)
				}
				for _, group := range state.InterviewGroups {
					if group.MessageSeq == cardSeq && group.Active {
						t.Fatalf("接受邀面卡后对应组仍 active: %+v", group)
					}
				}
			},
		},
		{
			name: "rejected", toState: "rejected",
			assert: func(t *testing.T, state communication.V4State, cardSeq int64) {
				t.Helper()
				if state.MainStatus != communication.V4StatusInvited {
					t.Fatalf("拒绝邀面卡不应改写主状态: %+v", state)
				}
				for _, group := range state.InterviewGroups {
					if group.MessageSeq == cardSeq {
						if group.Active || !group.Rejected {
							t.Fatalf("拒绝邀面卡未作废对应组: %+v", group)
						}
						return
					}
				}
				t.Fatalf("拒绝邀面卡未找到对应组: %+v", state.InterviewGroups)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			h := newHarness(t)
			fixture := seedCommunicationV4PendingInterviewTransition(
				t,
				h,
				testCase.name,
				testCase.toState,
			)
			if err := fixture.actor.processCommunicationV4Targets(context.Background()); err != nil {
				t.Fatal(err)
			}
			aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.target.profileID)
			if err != nil {
				t.Fatal(err)
			}
			if aggregate.Revision != fixture.revision+1 {
				t.Fatalf("卡片跃迁必须只形成一次 V4 输入: before=%d after=%d",
					fixture.revision, aggregate.Revision)
			}
			testCase.assert(t, aggregate.State, fixture.cardSeq)
			pending, err := h.db.PendingCardTransitionsForAccount(h.key, 10)
			if err != nil || len(pending) != 0 {
				t.Fatalf("成功投影后必须 ack: pending=%+v err=%v", pending, err)
			}
			retained, err := h.db.CardTransitionByKey(fixture.pending.Transition.Key())
			if err != nil || retained == nil || retained.AcknowledgedAt == nil {
				t.Fatalf("ack 必须保留原事实: fact=%+v err=%v", retained, err)
			}
		})
	}
}

func TestCommunicationV4CardTransitionApplyBeforeAckReplaysWithoutGrowth(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PendingInterviewTransition(t, h, "replay", "accepted")
	occurredAt := fixture.pending.Transition.CreatedAt
	event, err := communication.NormalizeCardTransition(communication.LedgerCardTransitionFact{
		MessageSeq: fixture.pending.Transition.MessageSeq,
		CardType:   fixture.pending.Transition.CardType,
		FromState:  fixture.pending.Transition.FromState,
		ToState:    fixture.pending.Transition.ToState,
		OccurredAt: &occurredAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := h.db.ApplyCommunicationV4BusinessEvent(
		store.ApplyCommunicationV4BusinessEventRequest{
			ProfileID: fixture.target.profileID,
			Event:     event, AppliedAt: h.clock.Now(),
		},
	)
	if err != nil || !first.Applied {
		t.Fatalf("模拟 apply 后崩溃前置失败: result=%+v err=%v", first, err)
	}
	revisionAfterApply := first.Aggregate.Revision
	if err := fixture.actor.processCommunicationV4Targets(context.Background()); err != nil {
		t.Fatal(err)
	}
	replayed, err := h.db.CommunicationV4AggregateByProfile(fixture.target.profileID)
	if err != nil || replayed.Revision != revisionAfterApply {
		t.Fatalf("apply→ack 崩溃重放不得增生: aggregate=%+v err=%v", replayed, err)
	}
	pending, err := h.db.PendingCardTransitionsForAccount(h.key, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("重放 immutable application 后应补 ack: pending=%+v err=%v", pending, err)
	}
}

func TestCommunicationV4CardTransitionProjectsPredecessorCardBeforeTransition(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PendingInterviewTransitionWithCardProjection(
		t,
		h,
		"unprojected-card",
		"accepted",
		false,
	)
	if fixture.cardSeq != fixture.target.inboundSeq+1 {
		t.Fatalf("fixture 卡片必须正好位于 V4 游标下一条: card=%d inbound=%d",
			fixture.cardSeq, fixture.target.inboundSeq)
	}
	if err := fixture.actor.processCommunicationV4Targets(context.Background()); err != nil {
		t.Fatal(err)
	}
	aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.target.profileID)
	if err != nil ||
		aggregate.ProjectedThroughSeq != fixture.cardSeq ||
		aggregate.Revision != fixture.revision+2 ||
		aggregate.State.MainStatus != communication.V4StatusInterviewed {
		t.Fatalf("必须先投影卡片再投影接受事实: aggregate=%+v err=%v", aggregate, err)
	}
	pending, err := h.db.PendingCardTransitionsForAccount(h.key, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("顺序收敛后必须 ack: pending=%+v err=%v", pending, err)
	}
}

func TestCommunicationV4WechatAcceptedCollectsContactBeforeEventProjection(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PendingCardTransitionWithCardProjection(
		t,
		h,
		"wechat-contact-success",
		"wechatExchange",
		"accepted",
		true,
	)
	exchangeSourceKey := strings.Repeat("8", 64)
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadWechatExchangeOutcome:
			args := decodeArgs[protocol.ChatReadWechatExchangeOutcomeArgs](t, request)
			if args.ConversationRef != fixture.key.ConversationRef ||
				args.RequestSourceKey != fixture.sourceKey {
				t.Fatalf("微信结果回读未锚定原卡片: %+v", args)
			}
			return protocol.ChatReadWechatExchangeOutcomeData{
				Confirmed:         true,
				ExchangeSourceKey: exchangeSourceKey,
				PeerWechat:        "synthetic-wechat-origin-one",
				ObservedAt:        h.clock.Now().UnixMilli(),
			}, nil
		default:
			return defaultHandler(request)
		}
	}
	messagesBefore, err := h.db.MessagesForConversation(fixture.key)
	if err != nil {
		t.Fatal(err)
	}
	aggregateBefore, err := h.db.CommunicationV4AggregateByProfile(fixture.target.profileID)
	if err != nil {
		t.Fatal(err)
	}
	h.manager.mu.Lock()
	err = fixture.actor.processCommunicationV4CardTransition(
		context.Background(),
		fixture.pending,
		fixture.profile,
		*aggregateBefore,
	)
	h.manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	assets, err := h.db.ContactAssetsByProfile(fixture.target.profileID)
	if err != nil || len(assets) != 1 ||
		assets[0].RequestSourceKey != fixture.sourceKey ||
		assets[0].SourceKey != exchangeSourceKey ||
		assets[0].Value != "synthetic-wechat-origin-one" ||
		assets[0].EffectIntentID != nil {
		t.Fatalf("origin1 联系方式未在事件前收编: assets=%+v err=%v", assets, err)
	}
	aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.target.profileID)
	if err != nil ||
		aggregate.State.WechatState != communication.V4WechatExchanged ||
		aggregate.Revision != aggregateBefore.Revision+1 {
		t.Fatalf("收号后微信接受事件未投影: aggregate=%+v err=%v", aggregate, err)
	}
	messagesAfter, err := h.db.MessagesForConversation(fixture.key)
	if err != nil || len(messagesAfter) != len(messagesBefore) {
		t.Fatalf("只读收号不得增生候选人可见消息: before=%d after=%d err=%v",
			len(messagesBefore), len(messagesAfter), err)
	}
	if got := h.runner.names(); len(got) != 2 ||
		got[0] != protocol.PrimChatReadThread ||
		got[1] != protocol.PrimChatReadWechatExchangeOutcome {
		t.Fatalf("origin1 收号只应走两条只读原语: %+v", got)
	}
}

func TestCommunicationV4WechatContactUnconfirmedGoesManualAndAcknowledges(t *testing.T) {
	testCases := []struct {
		name string
		data protocol.ChatReadWechatExchangeOutcomeData
	}{
		{
			name: "confirmed-false",
			data: protocol.ChatReadWechatExchangeOutcomeData{Confirmed: false},
		},
		{
			name: "confirmed-with-missing-fields",
			data: protocol.ChatReadWechatExchangeOutcomeData{Confirmed: true},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			h := newHarness(t)
			fixture := seedCommunicationV4PendingCardTransitionWithCardProjection(
				t,
				h,
				"wechat-contact-"+testCase.name,
				"wechatExchange",
				"accepted",
				true,
			)
			testCase.data.ObservedAt = h.clock.Now().UnixMilli()
			h.runner.handler = func(request RunRequest) (any, error) {
				switch request.Name {
				case protocol.PrimChatReadWechatExchangeOutcome:
					return testCase.data, nil
				default:
					return defaultHandler(request)
				}
			}
			before, err := h.db.CommunicationV4AggregateByProfile(
				fixture.target.profileID,
			)
			if err != nil {
				t.Fatal(err)
			}
			h.manager.mu.Lock()
			err = fixture.actor.processCommunicationV4CardTransition(
				context.Background(),
				fixture.pending,
				fixture.profile,
				*before,
			)
			h.manager.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			aggregate, err := h.db.CommunicationV4AggregateByProfile(
				fixture.target.profileID,
			)
			if err != nil ||
				aggregate.AutomationStatus != store.ProfileCommunicationAutomationManualRequired ||
				aggregate.ManualReason != communicationV4ManualWechatContactRead ||
				aggregate.Revision != before.Revision ||
				aggregate.State.WechatState != communication.V4WechatInvited {
				t.Fatalf("联系方式未确认没有保守转人工: aggregate=%+v err=%v",
					aggregate, err)
			}
			assets, err := h.db.ContactAssetsByProfile(fixture.target.profileID)
			if err != nil || len(assets) != 0 {
				t.Fatalf("联系方式未确认不得建资产: assets=%+v err=%v", assets, err)
			}
			actions, err := h.db.CommunicationV4EventActionsByProfile(
				fixture.target.profileID,
			)
			if err != nil || len(actions) != 0 {
				t.Fatalf("收号失败不得物化固定回执/effect: actions=%+v err=%v",
					actions, err)
			}
			pending, err := h.db.PendingCardTransitionsForAccount(h.key, 10)
			if err != nil || len(pending) != 0 {
				t.Fatalf("人工收敛后跃迁必须 ack，不能永久重试: pending=%+v err=%v",
					pending, err)
			}
			namesBeforeReplay := len(h.runner.names())
			h.manager.mu.Lock()
			err = fixture.actor.processCommunicationV4CardTransitions(context.Background())
			h.manager.mu.Unlock()
			if err != nil || len(h.runner.names()) != namesBeforeReplay {
				t.Fatalf("已 ack 的人工事实被自动重试: names=%+v err=%v",
					h.runner.names(), err)
			}
			for _, name := range h.runner.names() {
				if name != protocol.PrimChatReadThread &&
					name != protocol.PrimChatReadWechatExchangeOutcome {
					t.Fatalf("联系方式未确认触发候选人可见 effect: %+v",
						h.runner.names())
				}
			}
		})
	}
}

func TestCommunicationV4CardTransitionAppliesAfterCursorPassedOlderCard(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PendingInterviewTransition(t, h, "older-card", "accepted")
	systemHash := syncledger.HashText("system-notice-after-interview-card")
	changes, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: fixture.key, RoundID: fixture.actor.roundID, ExpectedTailSeq: fixture.cardSeq,
		NewMessages: []store.MessageDraft{{
			Direction: "system", Kind: "system", ContentHash: systemHash, Origin: "external",
		}},
		SyncedAt: h.clock.Now().Add(3 * time.Minute),
	})
	if err != nil || len(changes.Inserted) != 1 {
		t.Fatalf("追加卡片后的系统消息: changes=%+v err=%v", changes, err)
	}
	system := changes.Inserted[0]
	systemEvent, err := communication.NormalizeLedgerMessage(communication.LedgerMessageFact{
		Seq: system.Seq, Direction: system.Direction, Kind: system.Kind,
		Origin: system.Origin, TsApproxMs: system.TsApproxMs,
	})
	if err != nil {
		t.Fatal(err)
	}
	projected, err := h.db.ApplyCommunicationV4BusinessEvent(
		store.ApplyCommunicationV4BusinessEventRequest{
			ProfileID: fixture.target.profileID,
			Event:     systemEvent,
			AppliedAt: h.clock.Now().Add(3 * time.Minute),
		},
	)
	if err != nil || projected.Aggregate.ProjectedThroughSeq != system.Seq {
		t.Fatalf("后续消息投影失败: result=%+v err=%v", projected, err)
	}
	revisionBeforeTransition := projected.Aggregate.Revision

	if err := fixture.actor.processCommunicationV4Targets(context.Background()); err != nil {
		t.Fatal(err)
	}
	aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.target.profileID)
	if err != nil ||
		aggregate.ProjectedThroughSeq != system.Seq ||
		aggregate.Revision != revisionBeforeTransition+1 ||
		aggregate.State.MainStatus != communication.V4StatusInterviewed {
		t.Fatalf("旧卡片晚到跃迁必须在当前游标后收敛: aggregate=%+v err=%v", aggregate, err)
	}
	pending, err := h.db.PendingCardTransitionsForAccount(h.key, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("旧卡片跃迁收敛后必须 ack: pending=%+v err=%v", pending, err)
	}
}

func TestCommunicationV4UnknownCardTransitionGoesManualAndAcknowledges(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PendingInterviewTransition(t, h, "unknown", "expired")
	if err := fixture.actor.processCommunicationV4Targets(context.Background()); err != nil {
		t.Fatal(err)
	}
	aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.target.profileID)
	if err != nil ||
		aggregate.AutomationStatus != store.ProfileCommunicationAutomationManualRequired ||
		aggregate.ManualReason != string(communication.V4ManualUnknownPlatformEvent) {
		t.Fatalf("未知平台语义必须转人工: aggregate=%+v err=%v", aggregate, err)
	}
	pending, err := h.db.PendingCardTransitionsForAccount(h.key, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("未知语义已保守收敛后必须 ack: pending=%+v err=%v", pending, err)
	}
	if len(h.runner.names()) != 0 {
		t.Fatalf("未知语义不得派发 effect: %+v", h.runner.names())
	}
}

func TestCommunicationV4CardTransitionMismatchManualZeroEffectAndAck(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PendingInterviewTransition(t, h, "mismatch", "accepted")
	wrongConversation := fixture.pending.Transition.ConversationRef + "-other"
	mismatched := fixture.profile
	mismatched.ConversationRef = &wrongConversation
	before, err := h.db.CommunicationV4AggregateByProfile(fixture.target.profileID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.actor.processCommunicationV4CardTransition(
		context.Background(),
		fixture.pending,
		mismatched,
		*before,
	); err != nil {
		t.Fatal(err)
	}
	aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.target.profileID)
	if err != nil ||
		aggregate.AutomationStatus != store.ProfileCommunicationAutomationManualRequired ||
		aggregate.ManualReason != communicationV4ManualCardProfileMismatch ||
		aggregate.Revision != fixture.revision {
		t.Fatalf("错绑必须仅转人工且零投影/effect: aggregate=%+v err=%v", aggregate, err)
	}
	pending, err := h.db.PendingCardTransitionsForAccount(h.key, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("错绑已保守收敛后必须 ack: pending=%+v err=%v", pending, err)
	}
	if len(h.runner.names()) != 0 {
		t.Fatalf("错绑不得派发 effect: %+v", h.runner.names())
	}
}

func TestCommunicationV4InvalidCardTransitionStopsWithoutAck(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PendingInterviewTransition(t, h, "invalid", "invalid-state")
	err := fixture.actor.processCommunicationV4Targets(context.Background())
	if !errors.Is(err, communication.ErrInvalidBusinessEventInput) {
		t.Fatalf("损坏跃迁必须停轮: %v", err)
	}
	pending, readErr := h.db.PendingCardTransitionsForAccount(h.key, 10)
	if readErr != nil || len(pending) != 1 || pending[0].Transition.Key() != fixture.pending.Transition.Key() {
		t.Fatalf("失败不得 ack: pending=%+v err=%v", pending, readErr)
	}
	aggregate, aggregateErr := h.db.CommunicationV4AggregateByProfile(fixture.target.profileID)
	if aggregateErr != nil || aggregate.Revision != fixture.revision {
		t.Fatalf("失败不得推进聚合: aggregate=%+v err=%v", aggregate, aggregateErr)
	}
}
