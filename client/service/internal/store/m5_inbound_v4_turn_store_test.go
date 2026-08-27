package store

import (
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
)

func TestInboundConversationV4FirstTurnUsesFactRootWithoutFakeOutbound(t *testing.T) {
	s := openTest(t)
	fixture := seedInboundV4RootFixture(t, s)
	rootAt := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	aggregate, created, err := s.EnsureInboundConversationV4Root(
		fixture.ProfileID,
		rootAt,
	)
	if err != nil || aggregate == nil || !created {
		t.Fatalf("建立主动来聊事实根失败: aggregate=%+v created=%v err=%v", aggregate, created, err)
	}
	key := ConversationKey{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		ConversationRef: fixture.ConversationRef,
	}
	messages, err := s.MessagesForConversation(key)
	if err != nil {
		t.Fatal(err)
	}
	inbound, valid := DialogueTurnCandidateMessages(messages)
	if !valid || len(inbound) != 1 || inbound[0].Seq != 2 {
		t.Fatalf("主动来聊首轮边界错误: messages=%+v inbound=%+v", messages, inbound)
	}
	digest, turnID, err := DialogueTurnIdentityFromInboundRoot(
		fixture.ProfileID,
		aggregate.RootGreetingIntentID,
		inbound,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	material, ready, err := s.CommunicationAIMaterialForProfile(fixture.ProfileID)
	if err != nil || !ready {
		t.Fatalf("主动来聊 AI 材料不可用: ready=%v err=%v", ready, err)
	}
	recommended, err := m5ai.FreezeRecommendedTimeText(
		rootAt,
		m5ai.GenerateDefaultSlots(rootAt),
	)
	if err != nil {
		t.Fatal(err)
	}
	request := FreezeDialogueTurnRequest{
		TurnID: turnID, ProfileID: fixture.ProfileID,
		ConversationRef: fixture.ConversationRef,
		InputDigest:     digest, HistoryThroughSeq: inbound[0].Seq - 1,
		InboundFromSeq: inbound[0].Seq, InboundThroughSeq: inbound[len(inbound)-1].Seq,
		ExpectedProjectedThroughSeq: aggregate.ProjectedThroughSeq,
		OutboundAnchorSeq:           0,
		ContextRevisionHash:         material.ContextRevision.RevisionHash,
		ResumeSnapshotID:            material.ResumeSnapshot.SnapshotID,
		RecommendedTimeText:         recommended,
		RenderFormatVersion:         m5ai.DialogueRenderFormatVersion,
		FrozenAt:                    rootAt.Add(time.Second),
	}
	frozen, err := s.FreezeCommunicationV4Turn(request)
	if err != nil || frozen == nil || !frozen.Created ||
		frozen.Turn.HistoryThroughSeq != inbound[0].Seq-1 ||
		frozen.Aggregate.ProjectedThroughSeq != messages[len(messages)-1].Seq ||
		frozen.Aggregate.State.MainStatus != communication.V4StatusCommunicating ||
		frozen.Aggregate.State.RealMessageRound != 1 ||
		frozen.Aggregate.State.LastRealMessageSeq != inbound[0].Seq {
		t.Fatalf("主动来聊首轮未由真实入站推进: result=%+v err=%v", frozen, err)
	}
	current, err := s.RecheckDialogueTurnCurrent(
		frozen.Turn.TurnID,
		rootAt.Add(2*time.Second),
	)
	if err != nil || !current {
		t.Fatalf("主动来聊首轮不能通过统一重验: current=%v err=%v", current, err)
	}
	replayed, err := s.FreezeCommunicationV4Turn(request)
	if err != nil || replayed == nil || replayed.Created ||
		replayed.Turn.TurnID != frozen.Turn.TurnID ||
		replayed.Aggregate.Revision != frozen.Aggregate.Revision {
		t.Fatalf("主动来聊首轮重放发生增生: result=%+v err=%v", replayed, err)
	}
	var effectN, outboundN int64
	if err := s.db.Model(&EffectIntent{}).Count(&effectN).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&Message{}).
		Where(
			"platform = ? AND account_ref = ? AND conversation_ref = ? AND direction = ? AND retracted_at IS NULL",
			fixture.Platform,
			fixture.AccountRef,
			fixture.ConversationRef,
			"out",
		).
		Count(&outboundN).Error; err != nil {
		t.Fatal(err)
	}
	if effectN != 0 || outboundN != 0 {
		t.Fatalf("冻结首轮不得伪造发送事实: effects=%d outbound=%d", effectN, outboundN)
	}
}
