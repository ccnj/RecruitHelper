package store

import (
	"strings"
	"testing"
	"time"
)

// 收号锚必须是"请求"卡而不是"结果"卡，且两种发起形态通用。
// 2026-07-29 起形态 A 的交换结果卡也投影为 out 方向的 wechatExchange，若查询
// 只按 direction=out 取，就会把结果卡误当成我方邀请，锚到非 105 消息上，收号
// 原语必然阴性——这正是形态 B 修复代码在形态 A 上的失效点。
func TestLatestWechatExchangeRequestSourceKeyPicksRequestNotResult(t *testing.T) {
	at := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)

	appendCard := func(
		t *testing.T,
		s *Store,
		key ConversationKey,
		tailSeq int64,
		direction string,
		cardState string,
		sourceKey string,
		suffix string,
	) int64 {
		t.Helper()
		text := "[" + direction + " " + cardState + "]"
		origin := "external"
		if direction == "out" {
			origin = "self"
		}
		result, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{
			Key: key, RoundID: "round-anchor", ExpectedTailSeq: tailSeq,
			NewMessages: []MessageDraft{{
				Direction: direction, Kind: "card",
				ContentHash: strings.Repeat("c", 40) + suffix,
				Text:        &text, CardType: "wechatExchange", CardState: cardState,
				Origin: origin, SourceKey: &sourceKey,
			}},
			SyncedAt: at.Add(time.Duration(tailSeq) * time.Minute),
		})
		if err != nil || len(result.Inserted) != 1 {
			t.Fatalf("追加 %s/%s 卡失败: result=%+v err=%v", direction, cardState, result, err)
		}
		return result.Inserted[0].Seq
	}

	cases := []struct {
		name string
		// 依次追加的卡片：方向 + 状态；最后期望命中的 sourceKey 后缀。
		cards     [][2]string
		wantIndex int
	}{
		{
			name:      "我方发起：out/pending 请求 + in/accepted 结果",
			cards:     [][2]string{{"out", "pending"}, {"in", "accepted"}},
			wantIndex: 0,
		},
		{
			name:      "候选人发起：in/pending 请求 + out/accepted 结果",
			cards:     [][2]string{{"in", "pending"}, {"out", "accepted"}},
			wantIndex: 0,
		},
		{
			name: "多轮：取最近一张请求卡，结果卡永不作锚",
			cards: [][2]string{
				{"out", "pending"}, {"in", "accepted"},
				{"in", "pending"}, {"out", "accepted"},
			},
			wantIndex: 2,
		},
	}
	for index, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			s := openTest(t)
			suffix := string(rune('a' + index))
			profileID := "profile-anchor-" + suffix
			conversationRef := "conversation-anchor-" + suffix
			fixture, _ := seedSuccessfulV4Greeting(t, s, profileID, conversationRef, at)
			key := ConversationKey{
				Platform: fixture.Platform, AccountRef: fixture.AccountRef,
				ConversationRef: conversationRef,
			}
			if err := s.CreatePatrolRound(&PatrolRound{
				Platform: key.Platform, AccountRef: key.AccountRef, RoundID: "round-anchor",
			}); err != nil {
				t.Fatal(err)
			}
			messages, err := s.MessagesForConversation(key)
			if err != nil || len(messages) == 0 {
				t.Fatalf("种子账本为空: messages=%+v err=%v", messages, err)
			}
			tailSeq := messages[len(messages)-1].Seq

			sourceKeys := make([]string, len(testCase.cards))
			for cardIndex := range testCase.cards {
				sourceKeys[cardIndex] = strings.Repeat(string(rune('0'+cardIndex)), 64)
				tailSeq = appendCard(
					t, s, key, tailSeq,
					testCase.cards[cardIndex][0], testCase.cards[cardIndex][1],
					sourceKeys[cardIndex], suffix+string(rune('0'+cardIndex)),
				)
			}

			got, found, err := s.LatestWechatExchangeRequestSourceKey(key)
			if err != nil || !found || got != sourceKeys[testCase.wantIndex] {
				t.Fatalf("收号锚不符: got=%q found=%v want=%q err=%v",
					got, found, sourceKeys[testCase.wantIndex], err)
			}
		})
	}

	// 只有结果卡、没有任何请求卡时无锚可用，必须诚实返回未找到而不是拿结果卡凑数。
	t.Run("只有结果卡时无锚", func(t *testing.T) {
		s := openTest(t)
		fixture, _ := seedSuccessfulV4Greeting(t, s, "profile-anchor-none", "conversation-anchor-none", at)
		key := ConversationKey{
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			ConversationRef: "conversation-anchor-none",
		}
		if err := s.CreatePatrolRound(&PatrolRound{
			Platform: key.Platform, AccountRef: key.AccountRef, RoundID: "round-anchor",
		}); err != nil {
			t.Fatal(err)
		}
		messages, err := s.MessagesForConversation(key)
		if err != nil || len(messages) == 0 {
			t.Fatalf("种子账本为空: messages=%+v err=%v", messages, err)
		}
		appendCard(t, s, key, messages[len(messages)-1].Seq,
			"out", "accepted", strings.Repeat("f", 64), "none")
		if got, found, err := s.LatestWechatExchangeRequestSourceKey(key); err != nil || found {
			t.Fatalf("结果卡不得充当收号锚: got=%q found=%v err=%v", got, found, err)
		}
	})
}
