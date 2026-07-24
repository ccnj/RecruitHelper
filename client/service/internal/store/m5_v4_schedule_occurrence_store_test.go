package store

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
)

func communicationV4ArchiveRequestForTest(
	t *testing.T,
	s *Store,
	aggregate CommunicationV4Aggregate,
	evaluatedAt time.Time,
	hasPendingDialogue bool,
) ApplyCommunicationV4ArchiveActionRequest {
	t.Helper()
	profile, err := s.CandidateProfileByID(aggregate.ProfileID)
	if err != nil || profile == nil || profile.ConversationRef == nil {
		t.Fatalf("读取归档 fixture 档案失败: profile=%+v err=%v", profile, err)
	}
	decision, err := communication.EvaluateV4Schedule(communication.V4ScheduleInput{
		ProfileKey:          aggregate.ProfileID,
		State:               aggregate.State,
		ProjectedThroughSeq: aggregate.ProjectedThroughSeq,
		Now:                 evaluatedAt,
		HasPendingDialogue:  hasPendingDialogue,
		Reply:               communication.ReplyAdvice{State: communication.AdviceAbsent},
	})
	if err != nil ||
		decision.Status != communication.V4ScheduleActionsPlanned ||
		len(decision.Actions) != 1 ||
		decision.Actions[0].Kind != communication.V4ActionArchive {
		t.Fatalf("fixture 未产生归档动作: decision=%+v err=%v", decision, err)
	}
	return ApplyCommunicationV4ArchiveActionRequest{
		ProfileID:                   aggregate.ProfileID,
		ConversationRef:             *profile.ConversationRef,
		ExpectedRevision:            aggregate.Revision,
		ExpectedProjectedThroughSeq: aggregate.ProjectedThroughSeq,
		HasPendingDialogue:          hasPendingDialogue,
		Action:                      decision.Actions[0],
		EvaluatedAt:                 evaluatedAt,
		AppliedAt:                   evaluatedAt,
	}
}

func TestCommunicationV4ArchiveOccurrenceReplayDoesNotGrow(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-occurrence-replay")
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || aggregate.State.LastBodyAt == nil {
		t.Fatalf("读取归档前聚合失败: aggregate=%+v err=%v", aggregate, err)
	}
	evaluatedAt := aggregate.State.LastBodyAt.Add(8 * 24 * time.Hour)
	req := communicationV4ArchiveRequestForTest(
		t,
		s,
		*aggregate,
		evaluatedAt,
		false,
	)
	first, err := s.ApplyCommunicationV4ArchiveAction(req)
	if err != nil || first == nil || !first.Applied ||
		first.Occurrence.Status != CommunicationV4ScheduleOccurrenceApplied ||
		first.Occurrence.BasisRevision != aggregate.Revision ||
		first.Occurrence.BasisProjectedThroughSeq != aggregate.ProjectedThroughSeq ||
		first.Occurrence.AnchorMessageSeq != aggregate.ProjectedThroughSeq {
		t.Fatalf("首次 occurrence 没有与归档原子落地: result=%+v err=%v", first, err)
	}

	replayReq := req
	replayReq.EvaluatedAt = evaluatedAt.Add(time.Hour)
	replayReq.AppliedAt = evaluatedAt.Add(time.Hour)
	replayed, err := s.ApplyCommunicationV4ArchiveAction(replayReq)
	if err != nil || replayed == nil || replayed.Applied ||
		replayed.Aggregate.Revision != first.Aggregate.Revision ||
		replayed.Occurrence.OccurrenceID != first.Occurrence.OccurrenceID {
		t.Fatalf("同次评估重放发生增生: result=%+v err=%v", replayed, err)
	}
	assertCommunicationV4ArchiveFactCounts(t, s, fixture.ProfileID, 1)
}

func TestCommunicationV4ArchiveOccurrenceAllowsSameReasonAfterRealWakeup(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-occurrence-wakeup")
	before, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || before.State.LastBodyAt == nil {
		t.Fatalf("读取首次归档前聚合失败: aggregate=%+v err=%v", before, err)
	}
	firstAt := before.State.LastBodyAt.Add(8 * 24 * time.Hour)
	first, err := s.ApplyCommunicationV4ArchiveAction(
		communicationV4ArchiveRequestForTest(t, s, *before, firstAt, false),
	)
	if err != nil || first == nil || !first.Applied {
		t.Fatalf("首次归档失败: result=%+v err=%v", first, err)
	}

	key := ConversationKey{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		ConversationRef: fixture.ConversationRef,
	}
	wakeupText := "fixture wakeup"
	changes, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: first.Aggregate.ProjectedThroughSeq,
		NewMessages: []MessageDraft{{
			Direction: "in", Kind: "text", ContentHash: "occurrence-wakeup-inbound",
			Text: &wakeupText, Origin: "external",
		}},
		SyncedAt: firstAt.Add(time.Minute),
	})
	if err != nil || len(changes.Inserted) != 1 {
		t.Fatalf("追加唤醒消息失败: changes=%+v err=%v", changes, err)
	}
	wakeupSeq := changes.Inserted[0].Seq
	wakeupAt := firstAt.Add(time.Minute)
	woken, err := s.ApplyCommunicationV4BusinessEvent(
		ApplyCommunicationV4BusinessEventRequest{
			ProfileID: fixture.ProfileID,
			Event: communication.BusinessEvent{
				Key:        fmt.Sprintf("message:%d", wakeupSeq),
				Kind:       communication.EventCandidateExpressionReceived,
				Source:     communication.EventSourceMessage,
				MessageSeq: wakeupSeq,
				OccurredAt: &wakeupAt,
			},
			AppliedAt: wakeupAt,
		},
	)
	if err != nil || woken.Aggregate.State.MainStatus != communication.V4StatusCommunicating ||
		woken.Aggregate.State.RealMessageRound != before.State.RealMessageRound+1 {
		t.Fatalf("真实文字没有唤醒并开启新轮: result=%+v err=%v", woken, err)
	}

	secondReq := communicationV4ArchiveRequestForTest(
		t,
		s,
		woken.Aggregate,
		wakeupAt,
		true,
	)
	second, err := s.ApplyCommunicationV4ArchiveAction(secondReq)
	if err != nil || second == nil || !second.Applied ||
		second.Aggregate.State.MainStatus != communication.V4StatusEnded ||
		second.Aggregate.State.EndReason != communication.V4EndFallback ||
		second.Occurrence.OccurrenceID == first.Occurrence.OccurrenceID ||
		second.Occurrence.OccurrenceKey == first.Occurrence.OccurrenceKey {
		t.Fatalf("唤醒后的同原因第二次归档没有形成独立事实: first=%+v second=%+v err=%v",
			first, second, err)
	}
	assertCommunicationV4ArchiveFactCounts(t, s, fixture.ProfileID, 2)
}

func TestCommunicationV4ArchiveOccurrenceStaleCASLeavesNoHalfState(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-occurrence-stale")
	before, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || before.State.LastBodyAt == nil {
		t.Fatalf("读取 stale fixture 失败: aggregate=%+v err=%v", before, err)
	}
	evaluatedAt := before.State.LastBodyAt.Add(8 * 24 * time.Hour)
	req := communicationV4ArchiveRequestForTest(t, s, *before, evaluatedAt, false)

	key := ConversationKey{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		ConversationRef: fixture.ConversationRef,
	}
	changes, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: before.ProjectedThroughSeq,
		NewMessages: []MessageDraft{{
			Direction: "system", Kind: "system",
			ContentHash: "occurrence-stale-system", Origin: "external",
		}},
		SyncedAt: evaluatedAt,
	})
	if err != nil || len(changes.Inserted) != 1 {
		t.Fatalf("追加并发系统事实失败: changes=%+v err=%v", changes, err)
	}
	systemSeq := changes.Inserted[0].Seq
	if _, err := s.ApplyCommunicationV4BusinessEvent(
		ApplyCommunicationV4BusinessEventRequest{
			ProfileID: fixture.ProfileID,
			Event: communication.BusinessEvent{
				Key:        fmt.Sprintf("message:%d", systemSeq),
				Kind:       communication.EventSystemNotice,
				Source:     communication.EventSourceMessage,
				MessageSeq: systemSeq,
			},
			AppliedAt: evaluatedAt,
		},
	); err != nil {
		t.Fatalf("推进并发聚合事实失败: %v", err)
	}
	if _, err := s.ApplyCommunicationV4ArchiveAction(req); !errors.Is(err, ErrCommunicationV4Conflict) {
		t.Fatalf("stale CAS 未拒绝旧评估: %v", err)
	}
	assertCommunicationV4ArchiveFactCounts(t, s, fixture.ProfileID, 0)
}

func TestCommunicationV4FallbackTailAllowsInboundButRejectsOutbound(t *testing.T) {
	tests := []struct {
		name      string
		direction string
		kind      string
		wantOK    bool
	}{
		{name: "pending inbound remains eligible", direction: "in", kind: "text", wantOK: true},
		{name: "unprojected outbound invalidates body clock", direction: "out", kind: "text"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := openTest(t)
			fixture := seedReadyCommunicationTarget(t, s, "profile-v4-occurrence-tail-"+test.direction)
			aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
			if err != nil || aggregate.State.LastBodyAt == nil {
				t.Fatalf("读取 tail fixture 失败: aggregate=%+v err=%v", aggregate, err)
			}
			evaluatedAt := aggregate.State.LastBodyAt.Add(8 * 24 * time.Hour)
			req := communicationV4ArchiveRequestForTest(t, s, *aggregate, evaluatedAt, true)
			key := ConversationKey{
				Platform: fixture.Platform, AccountRef: fixture.AccountRef,
				ConversationRef: fixture.ConversationRef,
			}
			text := "fixture boundary"
			changes, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{
				Key: key, ExpectedTailSeq: aggregate.ProjectedThroughSeq,
				NewMessages: []MessageDraft{{
					Direction: test.direction, Kind: test.kind,
					ContentHash: "occurrence-tail-" + test.direction,
					Text:        &text, Origin: "external",
				}},
				SyncedAt: evaluatedAt,
			})
			if err != nil || len(changes.Inserted) != 1 {
				t.Fatalf("追加未投影尾部失败: changes=%+v err=%v", changes, err)
			}
			result, err := s.ApplyCommunicationV4ArchiveAction(req)
			if test.wantOK {
				if err != nil || result == nil || !result.Applied {
					t.Fatalf("七天兜底错误拒绝未投影入站: result=%+v err=%v", result, err)
				}
				assertCommunicationV4ArchiveFactCounts(t, s, fixture.ProfileID, 1)
				return
			}
			if !errors.Is(err, ErrCommunicationV4Conflict) {
				t.Fatalf("未投影出站没有作废旧正文时钟: result=%+v err=%v", result, err)
			}
			assertCommunicationV4ArchiveFactCounts(t, s, fixture.ProfileID, 0)
		})
	}
}

func assertCommunicationV4ArchiveFactCounts(
	t *testing.T,
	s *Store,
	profileID string,
	want int64,
) {
	t.Helper()
	var occurrences int64
	if err := s.db.Model(&CommunicationV4ScheduleOccurrence{}).
		Where("profile_id = ?", profileID).
		Count(&occurrences).Error; err != nil {
		t.Fatal(err)
	}
	var applications int64
	if err := s.db.Model(&CommunicationV4ProjectionApplication{}).
		Where("profile_id = ? AND input_kind = ?", profileID, CommunicationV4InputArchiveAction).
		Count(&applications).Error; err != nil {
		t.Fatal(err)
	}
	if occurrences != want || applications != want {
		t.Fatalf("归档事实数量不一致: occurrences=%d applications=%d want=%d",
			occurrences, applications, want)
	}
}
