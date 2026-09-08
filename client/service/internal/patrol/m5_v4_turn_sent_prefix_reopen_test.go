package patrol

import (
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
)

// 2026-09-08 甲方裁决(规格 v4 §一"旧轮失效"、§四"照发完只在同一轮内成立"):
// 多气泡链在一轮内只发出首条(第二条被日界掐断留成 planned),下一轮账本长出
// 候选人回复。此前巡检层判边界已变→settle→已发前缀算案底→轮与候选人一起
// 冻在 inputBoundaryChanged 等人。裁决后:旧轮收 completed、未发第二条作废、
// 已发首条零触碰、聚合保持 active,并且当轮就按最新账本边界重开新轮、正常出
// 建议并发送——候选人的新话进入裁决,剩余旧话术不续发。
func TestCommunicationV4PatrolReopensSentPrefixTurnOnCandidateReply(t *testing.T) {
	s := newStalePlannedDialogueHarness(t, "sent-prefix-reopen", true)
	s.interruptAfterFirstBubble(t)

	key := store.ConversationKey{
		Platform: s.h.key.Platform, AccountRef: s.h.key.AccountRef,
		ConversationRef: s.fixture.conversationRef,
	}
	messages, err := s.h.db.MessagesForConversation(key)
	if err != nil || len(messages) == 0 {
		t.Fatalf("读取账本失败: messages=%d err=%v", len(messages), err)
	}
	var tail int64
	for index := range messages {
		if messages[index].Seq > tail {
			tail = messages[index].Seq
		}
	}
	s.h.clock.Add(3 * time.Minute)
	replySeq := appendCommunicationV4CandidateText(
		t, s.h, s.fixture, "sent-prefix-reopen", tail, "看到了,我这边下午方便",
	)

	if err := s.runRound(t, s.manager, "round-sent-prefix-reopen"); err != nil {
		t.Fatal(err)
	}

	stale, err := s.h.db.DialogueTurnByID(s.turnID)
	if err != nil || stale == nil || stale.Status != store.DialogueTurnCompleted ||
		stale.FailureReason != "boundarySuperseded" {
		t.Fatalf("已发前缀轮未按 2026-09-08 裁决收 completed/boundarySuperseded: turn=%+v err=%v", stale, err)
	}
	first, err := s.h.db.CommunicationActionByID(s.firstID)
	if err != nil || first == nil || first.Status != store.CommunicationActionSent ||
		first.EffectIntentID == nil || first.SentAt == nil {
		t.Fatalf("已发首条被触碰: action=%+v err=%v", first, err)
	}
	second, err := s.h.db.CommunicationActionByID(s.secondID)
	if err != nil || second == nil ||
		second.Status != store.CommunicationActionSuperseded ||
		second.FailureReason != "boundarySuperseded" ||
		second.EffectIntentID != nil || second.SentAt != nil {
		t.Fatalf("未发第二条未作废或被补发: action=%+v err=%v", second, err)
	}
	reopened, err := s.h.db.LatestDialogueTurnForProfile(s.fixture.profileID)
	if err != nil || reopened == nil || reopened.TurnID == s.turnID ||
		reopened.InboundFromSeq != replySeq || reopened.InboundThroughSeq != replySeq ||
		reopened.Status != store.DialogueTurnCompleted || reopened.FailureReason != "" {
		t.Fatalf("未按候选人新话当轮重开并完成新轮: turn=%+v err=%v", reopened, err)
	}
	// 新轮的两条合成气泡都发出:首轮 1 条 + 新轮 2 条。
	if s.hand.commandCount() != 3 {
		t.Fatalf("新轮应正常发送,不补发旧轮残留: sends=%d", s.hand.commandCount())
	}
	aggregate, err := s.h.db.CommunicationV4AggregateByProfile(s.fixture.profileID)
	if err != nil ||
		aggregate.AutomationStatus != store.ProfileCommunicationAutomationActive ||
		aggregate.ManualReason != "" || aggregate.ProjectedThroughSeq <= replySeq {
		t.Fatalf("候选人不得被冻结且游标应推进: aggregate=%+v err=%v", aggregate, err)
	}
}
