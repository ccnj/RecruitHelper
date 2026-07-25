package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
)

type inboundV4RootFixture struct {
	ProfileID       string
	Platform        string
	AccountRef      string
	ConversationRef string
	SourceKey       string
	SnapshotID      string
	LogicalID       string
	FirstRealAt     time.Time
}

func seedInboundV4RootFixture(t *testing.T, s *Store) inboundV4RootFixture {
	t.Helper()
	at := time.Date(2026, 7, 26, 9, 15, 0, 0, time.UTC)
	req := inboundAdoptionRequest(at)
	seedInboundConversation(
		t,
		s,
		req.Platform,
		req.AccountRef,
		req.ConversationRef,
		req.PlatformUserRef,
		"候选人甲",
		at,
	)
	saveInboundLegacyJob(t, s, "job-42", "客户 经理", at.Add(-2*time.Minute))
	result, err := s.AdoptInboundConversationProfile(req)
	if err != nil || result == nil || result.Profile == nil {
		t.Fatalf("建立主动来聊档案: result=%+v err=%v", result, err)
	}
	firstRealAt := at.Add(-time.Minute)
	firstRealAtMs := firstRealAt.UnixMilli()
	sourceKey := strings.Repeat("a", 64)
	text := "候选人主动发来的普通消息"
	changes, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{
		Key: ConversationKey{
			Platform: req.Platform, AccountRef: req.AccountRef,
			ConversationRef: req.ConversationRef,
		},
		ExpectedTailSeq: 0,
		PlatformUserRef: req.PlatformUserRef,
		Adopt:           true,
		SyncedAt:        at,
		NewMessages: []MessageDraft{
			{
				Direction: "system", Kind: "system", ContentHash: "system-before-inbound",
				Origin: "external",
			},
			{
				Direction: "in", Kind: "text", ContentHash: "inbound-content-hash",
				Text: &text, Origin: "external", SourceKey: &sourceKey,
				TsApproxMs: &firstRealAtMs,
			},
		},
	})
	if err != nil || changes == nil || len(changes.Inserted) != 2 {
		t.Fatalf("收编主动来聊消息: result=%+v err=%v", changes, err)
	}
	fixture := inboundV4RootFixture{
		ProfileID: result.Profile.ProfileID,
		Platform:  req.Platform, AccountRef: req.AccountRef,
		ConversationRef: req.ConversationRef,
		SourceKey:       sourceKey,
		SnapshotID:      "snapshot-inbound-root",
		LogicalID:       "logical-inbound-root",
		FirstRealAt:     firstRealAt,
	}
	if err := s.db.Create(&CandidateResumeSnapshot{
		SnapshotID: fixture.SnapshotID, ProfileID: fixture.ProfileID,
		SourceKind: resumeSnapshotSourceIM, SourceConversationRef: fixture.ConversationRef,
		SourceLogicalDispatchID: fixture.LogicalID,
		ObservedAt:              firstRealAtMs, CapturedAt: at,
		SchemaVersion: resumeSnapshotSchemaV1,
		ContentHash:   "resume-inbound-root", ResumeJSON: `{"basic":[]}`,
		CreatedAt: at,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&CandidateProfile{}).
		Where("profile_id = ?", fixture.ProfileID).
		Updates(map[string]any{
			"resume_capture_state":               ResumeCaptureCaptured,
			"resume_capture_logical_dispatch_id": fixture.LogicalID,
			"active_resume_snapshot_id":          fixture.SnapshotID,
		}).Error; err != nil {
		t.Fatal(err)
	}
	return fixture
}

func saveNewerInboundJobRevision(t *testing.T, s *Store, at time.Time) string {
	t.Helper()
	revisionHash := "inbound-revision-newest"
	revision := contextRevisionFixture("inbound-context-job-42", revisionHash, at)
	revision.SourceKind = legacyJobConfigSourceKind
	revision.SourceJobRef = "job-42"
	// 职位展示名后续可更新；历史入站根只绑定 Job.ID，不冻结旧标题。
	revision.DisplayName = "客户经理（新版）"
	if _, err := s.SaveCurrentLegacyJobAIContext(
		[]m5ai.ContextRevision{revision},
		at,
	); err != nil {
		t.Fatal(err)
	}
	return revisionHash
}

func TestEnsureInboundConversationV4RootCreatesHonestRootAndReusesLatestAIMaterial(t *testing.T) {
	s := openTest(t)
	fixture := seedInboundV4RootFixture(t, s)
	rootAt := time.Date(2026, 7, 26, 9, 17, 0, 0, time.UTC)

	aggregate, created, err := s.EnsureInboundConversationV4Root(
		fixture.ProfileID,
		rootAt,
	)
	if err != nil || aggregate == nil || !created {
		t.Fatalf("建立主动来聊 V4 根: aggregate=%+v created=%v err=%v", aggregate, created, err)
	}
	expectedRef, err := InboundConversationV4RootRef(
		fixture.Platform,
		fixture.AccountRef,
		fixture.ConversationRef,
		fixture.SourceKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.RootGreetingIntentID != expectedRef ||
		!IsInboundConversationV4Root(aggregate.RootGreetingIntentID) ||
		aggregate.Revision != 0 ||
		aggregate.ProjectedThroughSeq != 0 ||
		aggregate.State.MainStatus != communication.V4StatusCommunicating ||
		aggregate.State.RealMessageRound != 0 ||
		aggregate.State.LastRealMessageSeq != 0 ||
		aggregate.State.LastOutboundAt != nil ||
		aggregate.State.LastBodyAt != nil {
		t.Fatalf("主动来聊根伪造了招呼或提前投影: %+v", aggregate)
	}
	for _, raw := range []string{
		fixture.Platform,
		fixture.AccountRef,
		fixture.ConversationRef,
		fixture.SourceKey,
	} {
		if strings.Contains(aggregate.RootGreetingIntentID, raw) {
			t.Fatalf("rootRef 泄漏原始绑定事实: %q", aggregate.RootGreetingIntentID)
		}
	}

	profile, err := s.CandidateProfileByID(fixture.ProfileID)
	if err != nil || profile == nil ||
		profile.MainStatus != CandidateProfileCommunicating ||
		profile.SuccessfulGreetingIntentID != nil ||
		profile.GreetedAt != nil ||
		profile.FirstRealMessageSeq == nil ||
		*profile.FirstRealMessageSeq != 2 ||
		profile.CommunicatingAt == nil ||
		!profile.CommunicatingAt.Equal(fixture.FirstRealAt) {
		t.Fatalf("主动来聊档案投影不诚实: profile=%+v err=%v", profile, err)
	}
	var effects, outbound int64
	if err := s.db.Model(&EffectIntent{}).Count(&effects).Error; err != nil {
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
		Count(&outbound).Error; err != nil {
		t.Fatal(err)
	}
	if effects != 0 || outbound != 0 {
		t.Fatalf("主动来聊根不得伪造副作用事实: effects=%d outbound=%d", effects, outbound)
	}

	latestRevision := saveNewerInboundJobRevision(
		t,
		s,
		rootAt.Add(time.Minute),
	)
	afterRename, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || afterRename == nil ||
		afterRename.RootGreetingIntentID != aggregate.RootGreetingIntentID {
		t.Fatalf("职位改名后历史入站根不应损坏: aggregate=%+v err=%v", afterRename, err)
	}
	targets, err := s.CommunicationTargetsForAccount(AccountKey{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
	})
	if err != nil || len(targets) != 1 ||
		targets[0].Profile.ProfileID != fixture.ProfileID {
		t.Fatalf("生产目标查询未识别主动来聊根: targets=%+v err=%v", targets, err)
	}
	material, ready, err := s.CommunicationAIMaterialForProfile(fixture.ProfileID)
	if err != nil || !ready ||
		material.ContextRevision.RevisionHash != latestRevision ||
		material.ContextRevision.SourceJobRef != "job-42" ||
		material.ResumeSnapshot.SnapshotID != fixture.SnapshotID {
		t.Fatalf("主动来聊未复用最新职位配置与已捕获简历: material=%+v ready=%v err=%v", material, ready, err)
	}

	replayed, wasCreated, err := s.EnsureInboundConversationV4Root(
		fixture.ProfileID,
		rootAt.Add(time.Hour),
	)
	if err != nil || wasCreated || replayed == nil ||
		replayed.RootGreetingIntentID != aggregate.RootGreetingIntentID {
		t.Fatalf("主动来聊根重放不幂等: aggregate=%+v created=%v err=%v", replayed, wasCreated, err)
	}
	var aggregateCount int64
	if err := s.db.Model(&CommunicationV4Aggregate{}).
		Where("profile_id = ?", fixture.ProfileID).
		Count(&aggregateCount).Error; err != nil || aggregateCount != 1 {
		t.Fatalf("重放产生重复根: count=%d err=%v", aggregateCount, err)
	}
}

func TestEnsureInboundConversationV4RootRejectsUnstableOrContaminatedBoundaryAtomically(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(t *testing.T, s *Store, fixture inboundV4RootFixture)
	}{
		{
			name: "missing stable source key",
			mutate: func(t *testing.T, s *Store, fixture inboundV4RootFixture) {
				t.Helper()
				if err := s.db.Model(&Message{}).
					Where(
						"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ?",
						fixture.Platform,
						fixture.AccountRef,
						fixture.ConversationRef,
						2,
					).
					Update("source_key", nil).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "wrong adoption provenance",
			mutate: func(t *testing.T, s *Store, fixture inboundV4RootFixture) {
				t.Helper()
				if err := s.db.Model(&TrackedIntent{}).
					Where(
						"platform = ? AND account_ref = ? AND conversation_ref = ?",
						fixture.Platform,
						fixture.AccountRef,
						fixture.ConversationRef,
					).
					Update("requested_by", "user").Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "existing outbound message",
			mutate: func(t *testing.T, s *Store, fixture inboundV4RootFixture) {
				t.Helper()
				text := "不应存在的出站消息"
				if err := s.db.Create(&Message{
					Platform: fixture.Platform, AccountRef: fixture.AccountRef,
					ConversationRef: fixture.ConversationRef,
					Seq:             3, Direction: "out", Kind: "text",
					ContentHash: "unexpected-outbound", Text: &text, Origin: "self",
				}).Error; err != nil {
					t.Fatal(err)
				}
				if err := s.db.Model(&Conversation{}).
					Where(
						"platform = ? AND account_ref = ? AND conversation_ref = ?",
						fixture.Platform,
						fixture.AccountRef,
						fixture.ConversationRef,
					).
					Update("last_message_seq", int64(3)).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "job binding drift",
			mutate: func(t *testing.T, s *Store, fixture inboundV4RootFixture) {
				t.Helper()
				if err := s.db.Model(&CandidateProfile{}).
					Where("profile_id = ?", fixture.ProfileID).
					Update("position_ref", "other-job").Error; err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			s := openTest(t)
			fixture := seedInboundV4RootFixture(t, s)
			testCase.mutate(t, s, fixture)

			aggregate, created, err := s.EnsureInboundConversationV4Root(
				fixture.ProfileID,
				time.Date(2026, 7, 26, 9, 20, 0, 0, time.UTC),
			)
			if aggregate != nil || created || !errors.Is(err, ErrCommunicationV4Conflict) {
				t.Fatalf("不稳定事实必须拒绝: aggregate=%+v created=%v err=%v", aggregate, created, err)
			}
			profile, queryErr := s.CandidateProfileByID(fixture.ProfileID)
			if queryErr != nil || profile == nil ||
				profile.MainStatus != CandidateProfileSelected ||
				profile.FirstRealMessageSeq != nil ||
				profile.CommunicatingAt != nil {
				t.Fatalf("失败后档案被部分推进: profile=%+v err=%v", profile, queryErr)
			}
			var count int64
			if err := s.db.Model(&CommunicationV4Aggregate{}).
				Where("profile_id = ?", fixture.ProfileID).
				Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("失败后泄漏聚合根: count=%d err=%v", count, err)
			}
		})
	}
}

func TestInboundConversationV4RootRefIsVersionedAndStrict(t *testing.T) {
	sourceKey := strings.Repeat("f", 64)
	first, err := InboundConversationV4RootRef(
		"zhilian",
		"account",
		"conversation",
		sourceKey,
	)
	if err != nil || !IsInboundConversationV4Root(first) {
		t.Fatalf("合法根引用未生成: ref=%q err=%v", first, err)
	}
	second, err := InboundConversationV4RootRef(
		"zhilian",
		"account",
		"conversation-other",
		sourceKey,
	)
	if err != nil || second == first {
		t.Fatalf("会话维度未进入摘要: first=%q second=%q err=%v", first, second, err)
	}
	if _, err := InboundConversationV4RootRef(
		"zhilian",
		"account",
		"conversation",
		"unstable",
	); !errors.Is(err, ErrCommunicationV4Invalid) {
		t.Fatalf("不稳定 sourceKey 必须拒绝: %v", err)
	}
	if IsInboundConversationV4Root("intent-greeting") ||
		IsInboundConversationV4Root(inboundConversationV4RootPrefix+"not-a-digest") {
		t.Fatal("普通招呼或畸形摘要不得被识别为主动来聊根")
	}
}
