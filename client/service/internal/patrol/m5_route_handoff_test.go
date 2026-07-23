package patrol

import (
	"testing"

	"recruithelper/client/service/internal/store"
)

func TestM5TargetNeedsRouteHandoff(t *testing.T) {
	outbound := store.Message{Seq: 1, Direction: "out", Kind: "text"}
	inboundText := "候选人回复"
	inbound := store.Message{Seq: 2, Direction: "in", Kind: "text", Text: &inboundText}
	replied := store.Message{Seq: 3, Direction: "out", Kind: "text"}

	tests := []struct {
		name         string
		alreadyDirty bool
		captureState store.ResumeCaptureState
		ledger       []store.Message
		want         bool
	}{
		{name: "dirty target needs exclusive handoff", alreadyDirty: true, captureState: store.ResumeCaptureCaptured, want: true},
		{name: "unattempted capture needs target route", captureState: store.ResumeCaptureUnattempted, want: true},
		{name: "inflight capture needs target route", captureState: store.ResumeCaptureInFlight, want: true},
		{
			name: "captured pending inbound needs reply route", captureState: store.ResumeCaptureCaptured,
			ledger: []store.Message{outbound, inbound}, want: true,
		},
		{
			name: "captured idle trial does not steal page", captureState: store.ResumeCaptureCaptured,
			ledger: []store.Message{outbound}, want: false,
		},
		{
			name: "already replied inbound is not pending", captureState: store.ResumeCaptureCaptured,
			ledger: []store.Message{outbound, inbound, replied}, want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := m5TargetNeedsRouteHandoff(test.alreadyDirty, test.captureState, test.ledger); got != test.want {
				t.Fatalf("m5TargetNeedsRouteHandoff() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestIsolateActiveM5TargetDefersUnrelatedDirtyConversations(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5ResumeAdviceFixture(t, h)
	target, err := h.db.ActiveM5TrialForAccount(h.key)
	if err != nil || target == nil {
		t.Fatalf("读取 active M5 目标失败: target=%+v err=%v", target, err)
	}
	targetKey := store.ConversationKey{
		Platform: target.Conversation.Platform, AccountRef: target.Conversation.AccountRef,
		ConversationRef: target.Conversation.ConversationRef,
	}
	targetLedger, err := h.db.MessagesForConversation(targetKey)
	if err != nil {
		t.Fatal(err)
	}
	unrelated := dirtyConversation{conversation: store.Conversation{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: "conversation-unrelated-stale", TrackingState: store.TrackingAdopted,
	}}
	targetDirty := dirtyConversation{conversation: target.Conversation, ledger: targetLedger}

	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("读取测试账号失败: account=%+v err=%v", account, err)
	}
	actor := &roundActor{manager: h.manager, account: account}
	isolated, err := actor.isolateActiveM5Target([]dirtyConversation{unrelated, targetDirty})
	if err != nil || len(isolated) != 1 ||
		isolated[0].conversation.ConversationRef != fixture.conversationRef {
		t.Fatalf("active 试运行仍会被无关 dirty 会话阻断: isolated=%+v err=%v", isolated, err)
	}
}
