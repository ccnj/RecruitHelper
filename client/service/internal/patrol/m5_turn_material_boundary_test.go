package patrol

import (
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
)

// appendM5RowAfterTurn 在轮区间之后往账本追加一行,返回新尾 seq。
func appendM5RowAfterTurn(
	t *testing.T,
	h *harness,
	fixture m5AdviceFixture,
	tailSeq int64,
	draft store.MessageDraft,
) int64 {
	t.Helper()
	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: fixture.conversationRef,
	}
	changes, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: tailSeq,
		NewMessages: []store.MessageDraft{draft},
		SyncedAt:    h.clock.Now().Add(time.Minute),
	})
	if err != nil || len(changes.Inserted) != 1 {
		t.Fatalf("追加轮后账本行失败: changes=%+v err=%v", changes, err)
	}
	return changes.Inserted[0].Seq
}

func textDraft(direction, origin, text string) store.MessageDraft {
	return store.MessageDraft{
		Direction: direction, Kind: "text", Text: &text,
		ContentHash: syncledger.HashText(text), Origin: origin,
	}
}

// 2026-09-09:材料装配的边界判据与巡检层统一为 communicationV4TurnBoundaryMoved
// (2026-08-27 口径)。本轮链内自产的出站行(固定回执、已发气泡)不是边界移动,
// 装配照常通过,且这些行渲染进对话历史(throughTurn),不进本轮输入(current)
// 与冻结时历史(history)。此前装配自带的 0727 老规则把任何我方出站都判失效,
// "回执正证后再让 AI 回一句"的轮每轮跳过、永不回复(2026-09-08 BOSS 真机)。
func TestLoadM5TurnMaterialToleratesOwnOutboundAfterTurn(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5AdviceFixture(t, h)
	receipt := "好了,晚点加你,咱们微信上聊。"
	receiptSeq := appendM5RowAfterTurn(
		t, h, fixture, fixture.turn.InboundThroughSeq, textDraft("out", "self", receipt),
	)
	actor := &roundActor{manager: h.manager, now: h.clock.Now()}
	material, err := actor.loadM5TurnMaterial(fixture.turn)
	if err != nil {
		t.Fatalf("本轮自己发出的回执不得判成边界失效: %v", err)
	}
	found := false
	for _, message := range material.throughTurn {
		if message.Seq == receiptSeq {
			if message.Direction != "outbound" || message.Text != receipt {
				t.Fatalf("回执行渲染形态错误: %+v", message)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("已发回执必须渲染进对话历史: throughTurn=%+v", material.throughTurn)
	}
	for _, message := range material.current {
		if message.Seq == receiptSeq {
			t.Fatalf("轮后行不得混进本轮输入: current=%+v", material.current)
		}
	}
	for _, message := range material.history {
		if message.Seq == receiptSeq {
			t.Fatalf("轮后行不得混进冻结时历史: history=%+v", material.history)
		}
	}
	if len(material.current) != 1 || material.current[0].Seq != fixture.turn.InboundThroughSeq {
		t.Fatalf("本轮输入应只含轮区间内的候选人消息: current=%+v", material.current)
	}
}

// 承重墙不变:候选人新输入与真人手发出站仍是边界移动,装配照旧报绑定失效,
// 交给巡检层作废重开。
func TestLoadM5TurnMaterialStillRejectsBoundaryMovingRowsAfterTurn(t *testing.T) {
	cases := []struct {
		name  string
		draft store.MessageDraft
	}{
		{name: "candidate_text", draft: textDraft("in", "external", "我再补一句")},
		{name: "human_outbound", draft: textDraft("out", "external", "真人手打的一句")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			fixture := seedM5AdviceFixture(t, h)
			appendM5RowAfterTurn(t, h, fixture, fixture.turn.InboundThroughSeq, tc.draft)
			actor := &roundActor{manager: h.manager, now: h.clock.Now()}
			if _, err := actor.loadM5TurnMaterial(fixture.turn); !errors.Is(err, store.ErrDialogueTurnBinding) {
				t.Fatalf("边界移动行必须让装配报绑定失效: err=%v", err)
			}
		})
	}
}

// 删掉的特判被统一判据覆盖:平台 system 行与交换结果卡(out/card/accepted,
// 平台产物、origin external)仍被容忍,交换结果卡照旧渲染进历史。
func TestLoadM5TurnMaterialToleratesSystemRowAndAcceptedExchangeCard(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5AdviceFixture(t, h)
	systemText := "对方已开启在线状态"
	tail := appendM5RowAfterTurn(t, h, fixture, fixture.turn.InboundThroughSeq, store.MessageDraft{
		Direction: "system", Kind: "system", Text: &systemText,
		ContentHash: syncledger.HashText(systemText), Origin: "external",
	})
	cardText := "[微信交换成功]"
	cardSeq := appendM5RowAfterTurn(t, h, fixture, tail, store.MessageDraft{
		Direction: "out", Kind: "card", CardType: "wechatExchange", CardState: "accepted",
		Text: &cardText, ContentHash: syncledger.HashText(cardText), Origin: "external",
	})
	actor := &roundActor{manager: h.manager, now: h.clock.Now()}
	material, err := actor.loadM5TurnMaterial(fixture.turn)
	if err != nil {
		t.Fatalf("system 行与交换结果卡不得判成边界失效: %v", err)
	}
	found := false
	for _, message := range material.throughTurn {
		if message.Seq == cardSeq {
			found = true
		}
		if message.Direction != "inbound" && message.Direction != "outbound" {
			t.Fatalf("system 行不得渲染进历史: %+v", message)
		}
	}
	if !found {
		t.Fatalf("交换结果卡必须渲染进对话历史: throughTurn=%+v", material.throughTurn)
	}
}
