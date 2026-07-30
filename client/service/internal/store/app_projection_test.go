package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/contract/gen/go/protocol"
)

func TestAppConfirmationProjectsOnlyCurrentExplicitBatch(t *testing.T) {
	s := openTest(t)
	now := time.Date(2026, 7, 25, 10, 0, 0, 0, time.Local)
	platform, accountRef := "zhilian", "app-confirm-account"
	createM4Account(t, s, platform, accountRef)

	batchID, runID, profileID := "B-app-confirm", "R-app-confirm", "P-app-confirm"
	positionRef, positionTitle, backendJobID := "position-app", "合成职位", "job-app"
	endedAt := now.Add(-time.Hour)
	if err := s.db.Create(&SourcingBatch{
		BatchID: batchID, Platform: platform, AccountRef: accountRef,
		ContextRevisionHash: "revision-app", BackendJobID: &backendJobID,
		TargetCount: 1, PositionRef: &positionRef, PositionTitle: &positionTitle,
		Status: SourcingBatchCompleted, StartedAt: now.Add(-2 * time.Hour), EndedAt: &endedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	displayName := "候选人甲"
	memberBatchID := batchID
	if err := s.db.Create(&SourcingCandidateRun{
		RunID: runID, BatchID: &memberBatchID, Platform: platform, AccountRef: accountRef,
		ContextRevisionHash: "revision-app", PlatformUserRef: "opaque-user-app",
		DisplayName: &displayName, PositionRef: positionRef, PositionTitle: &positionTitle,
		ContactState: "unestablished", SourceLogicalDispatchID: "logical-app",
		ObservedAt: now.UnixMilli(), CapturedAt: now.Add(-90 * time.Minute),
		SchemaVersion: 1, ContentHash: strings.Repeat("a", 64), ResumeJSON: `{"basic":[],"expectations":[]}`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&Candidate{
		Platform: platform, PlatformUserRef: "opaque-user-app", DisplayName: &displayName,
		FirstSeenAt: now.Add(-90 * time.Minute), LastSeenAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&CandidateProfile{
		ProfileID: profileID, Platform: platform, AccountRef: accountRef,
		PlatformUserRef: "opaque-user-app", PositionRef: positionRef, PositionTitle: &positionTitle,
		BackendJobID: &backendJobID, MainStatus: CandidateProfileSelected,
		ResumeCaptureState: ResumeCaptureUnattempted,
	}).Error; err != nil {
		t.Fatal(err)
	}
	score := 92
	if err := s.db.Create(&SourcingScoreInvocation{
		InvocationID: "score-app", RunID: runID, ContextRevisionHash: "revision-app",
		RunContentHash: strings.Repeat("a", 64), Provider: "fixture", Model: "fixture",
		InputHash: strings.Repeat("b", 64), Status: AIInvocationOK, Score: &score,
		StartedAt: now.Add(-80 * time.Minute), FinishedAt: appPtrTime(now.Add(-79 * time.Minute)),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&SourcingBatchSelection{
		BatchID: batchID, ContextRevisionHash: "revision-app",
		AlgorithmVersion: SourcingSelectionAlgorithmVersion,
		MinScore:         5, TargetMin: 1, TargetMax: 1, TargetCount: 1,
		MaleRatioLimit: 100, MaleLimit: 1, PoolCount: 1, EligibleCount: 1,
		SelectedCount: 1, CompletedAt: now.Add(-70 * time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&SourcingSelectionDecision{
		RunID: runID, ContextRevisionHash: "revision-app", Score: &score, MinScore: 5,
		Outcome: SourcingSelectionSelected, ProfileID: &profileID,
		DecidedAt: now.Add(-70 * time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	greeting := "您好，想和您沟通一下岗位机会。"
	if err := s.db.Create(&SourcingGreetingInvocation{
		InvocationID: "greeting-app", BatchID: batchID, RunID: runID, ProfileID: profileID,
		ContextRevisionHash: "revision-app", RunContentHash: strings.Repeat("a", 64),
		Provider: "fixture", Model: "fixture", InputHash: strings.Repeat("c", 64),
		Status: AIInvocationOK, GreetingText: greeting, ContentHash: strings.Repeat("d", 64),
		StartedAt: now.Add(-60 * time.Minute), FinishedAt: appPtrTime(now.Add(-59 * time.Minute)),
	}).Error; err != nil {
		t.Fatal(err)
	}

	got, err := s.AppConfirmation(batchID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Available || !got.Ready || got.SelectableCount != 1 || len(got.Candidates) != 1 {
		t.Fatalf("unexpected confirmation: %+v", got)
	}
	item := got.Candidates[0]
	if item.ProfileID != profileID || item.DisplayName != displayName || item.Score == nil ||
		*item.Score != score || item.GreetingText != greeting || item.Status != "ready" || !item.Selectable {
		t.Fatalf("unexpected candidate: %+v", item)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"opaque-user-app", "logical-app", "revision-app"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("产品确认投影泄漏内部引用 %q: %s", forbidden, encoded)
		}
	}

	invalidatedAt := now
	if err := s.db.Model(&Account{}).
		Where("platform = ? AND account_ref = ?", platform, accountRef).
		Update("sourcing_feed_invalidated_at", invalidatedAt).Error; err != nil {
		t.Fatal(err)
	}
	invalidated, err := s.AppConfirmation(batchID)
	if err != nil {
		t.Fatal(err)
	}
	if invalidated.SelectableCount != 0 ||
		len(invalidated.Candidates) != 1 ||
		invalidated.Candidates[0].Status != "abandoned" {
		t.Fatalf("unexpected invalidated confirmation: %+v", invalidated)
	}
	funnel, err := appFunnelTx(s.db, batchID, platform, accountRef)
	if err != nil {
		t.Fatal(err)
	}
	if funnel.PendingConfirm != 0 || funnel.Stage != "completed" {
		t.Fatalf("invalidated funnel did not settle: %+v", funnel)
	}
}

func TestAppCandidateListAndDetailUseProfileProjection(t *testing.T) {
	s := openTest(t)
	now := time.Date(2026, 7, 25, 11, 0, 0, 0, time.Local)
	platform, accountRef, conversationRef := "zhilian", "app-list-account", "opaque-conversation"
	displayName, positionTitle := "候选人乙", "测试工程师"
	profileID, userRef := "P-app-detail", "opaque-user-detail"
	if err := s.db.Create(&Candidate{
		Platform: platform, PlatformUserRef: userRef, DisplayName: &displayName,
		FirstSeenAt: now.Add(-24 * time.Hour), LastSeenAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	snapshotID := "snapshot-app-detail"
	if err := s.db.Create(&CandidateProfile{
		ProfileID: profileID, Platform: platform, AccountRef: accountRef,
		PlatformUserRef: userRef, PositionRef: "position-detail", PositionTitle: &positionTitle,
		MainStatus: CandidateProfileCommunicating, ConversationRef: &conversationRef,
		ResumeCaptureState: ResumeCaptureCaptured, ActiveResumeSnapshotID: &snapshotID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	lastMs := now.UnixMilli()
	if err := s.db.Create(&Conversation{
		Platform: platform, AccountRef: accountRef, ConversationRef: conversationRef,
		PlatformUserRef: userRef, PeerDisplayName: displayName, UnreadCount: 2,
		LastMessageDirection: "in", LastMessageKind: "text", LastMessagePreview: "可以了解一下",
		LastActivityMs: &lastMs, TrackingState: TrackingAdopted, LastMessageSeq: 2,
	}).Error; err != nil {
		t.Fatal(err)
	}
	rootIntent := "intent-app-detail"
	if err := s.db.Create(&CommunicationV4Aggregate{
		ProfileID: profileID, RootGreetingIntentID: rootIntent, StateSchemaVersion: 1,
		State:            communication.NewV4GreetedState(appPtrTime(now.Add(-time.Hour))),
		AutomationStatus: ProfileCommunicationAutomationManualRequired,
		ManualReason:     "operatorReview",
	}).Error; err != nil {
		t.Fatal(err)
	}
	text := "可以了解一下"
	for _, message := range []Message{
		{
			Platform: platform, AccountRef: accountRef, ConversationRef: conversationRef, Seq: 1,
			Direction: "out", Kind: "text", ContentHash: strings.Repeat("e", 64),
			Text: appPtrString("您好"), Origin: "self",
		},
		{
			Platform: platform, AccountRef: accountRef, ConversationRef: conversationRef, Seq: 2,
			Direction: "in", Kind: "text", ContentHash: strings.Repeat("f", 64),
			Text: &text, Origin: "external", TsApproxMs: &lastMs,
		},
	} {
		if err := s.db.Create(&message).Error; err != nil {
			t.Fatal(err)
		}
	}
	resumeJSON, _ := json.Marshal(struct {
		Basic           []protocol.CandidateResumeLabelValue `json:"basic"`
		Expectations    []protocol.CandidateResumeLabelValue `json:"expectations"`
		SelfEvaluation  string                               `json:"selfEvaluation"`
		Education       string                               `json:"education"`
		WorkExperiences string                               `json:"workExperiences"`
	}{
		Basic:          []protocol.CandidateResumeLabelValue{{Label: "工作年限", Value: "5年"}},
		SelfEvaluation: "执行力强",
	})
	if err := s.db.Create(&CandidateResumeSnapshot{
		SnapshotID: snapshotID, ProfileID: profileID, SourceKind: "fixture",
		SourceConversationRef: conversationRef, SourceLogicalDispatchID: "opaque-logical-detail",
		ObservedAt: now.UnixMilli(), CapturedAt: now, SchemaVersion: 1,
		ContentHash: strings.Repeat("1", 64), ResumeJSON: string(resumeJSON),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&ContactAsset{
		AssetID: "asset-app-detail", ProfileID: profileID, Platform: platform,
		AccountRef: accountRef, ConversationRef: conversationRef, Kind: contactAssetKindWechat,
		SourceKey: strings.Repeat("2", 64), RequestSourceKey: strings.Repeat("3", 64),
		Value: "candidate_wechat", ObservedAtMs: now.UnixMilli(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	foreignAccountRef := "app-list-foreign-account"
	createM4Account(t, s, platform, foreignAccountRef)
	foreignUserRef := "opaque-user-foreign"
	foreignDisplayName := "其他账号候选人"
	if err := s.db.Create(&Candidate{
		Platform: platform, PlatformUserRef: foreignUserRef, DisplayName: &foreignDisplayName,
		FirstSeenAt: now.Add(-time.Hour), LastSeenAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&CandidateProfile{
		ProfileID: "P-app-foreign", Platform: platform, AccountRef: foreignAccountRef,
		PlatformUserRef: foreignUserRef, PositionRef: "position-foreign",
		MainStatus:         CandidateProfileCommunicating,
		ResumeCaptureState: ResumeCaptureUnattempted,
	}).Error; err != nil {
		t.Fatal(err)
	}

	list, err := s.AppCandidates(AppCandidateListQuery{
		Platform: platform, AccountRef: accountRef,
		View: AppCandidateViewCommunicating, Search: "候选人",
	})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].ProfileID != profileID ||
		list.Items[0].Wechat == nil || *list.Items[0].Wechat != "candidate_wechat" ||
		!list.Items[0].ManualRequired {
		t.Fatalf("unexpected list projection: %+v", list)
	}
	detail, err := s.AppCandidateDetail(AppCandidateDetailQuery{
		Platform: platform, AccountRef: accountRef, ProfileID: profileID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !detail.Resume.Available || len(detail.Resume.Basic) != 1 ||
		len(detail.Messages) != 2 || detail.Messages[1].Text == nil ||
		*detail.Messages[1].Text != text {
		t.Fatalf("unexpected detail projection: %+v", detail)
	}
	encoded, _ := json.Marshal(detail)
	for _, forbidden := range []string{userRef, conversationRef, "opaque-logical-detail"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("产品详情投影泄漏内部引用 %q: %s", forbidden, encoded)
		}
	}
	if _, err := s.AppCandidateDetail(AppCandidateDetailQuery{
		Platform: platform, AccountRef: foreignAccountRef, ProfileID: profileID,
	}); !errors.Is(err, ErrAppCandidateNotFound) {
		t.Fatalf("其他账号不得探测候选人详情: %v", err)
	}
}

func TestAppOverviewMarksUnavailableMetricsInsteadOfGuessing(t *testing.T) {
	s := openTest(t)
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.Local)
	finished := now.Add(-time.Minute)
	score := 88
	if err := s.db.Create(&SourcingCandidateRun{
		RunID: "run-overview", Platform: "zhilian", AccountRef: "overview-account",
		ContextRevisionHash: "revision-overview", PlatformUserRef: "overview-candidate",
		PositionRef: "overview-position", ContactState: "unestablished",
		SourceLogicalDispatchID: "overview-logical", ObservedAt: now.UnixMilli(),
		CapturedAt: now.Add(-3 * time.Minute), SchemaVersion: 1,
		ContentHash: strings.Repeat("3", 64), ResumeJSON: `{"basic":[],"expectations":[]}`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&SourcingScoreInvocation{
		InvocationID: "score-overview", RunID: "run-overview",
		ContextRevisionHash: "revision-overview", RunContentHash: strings.Repeat("4", 64),
		Provider: "fixture", Model: "fixture", InputHash: strings.Repeat("5", 64),
		Status: AIInvocationOK, Score: &score, StartedAt: now.Add(-2 * time.Minute),
		FinishedAt: &finished,
	}).Error; err != nil {
		t.Fatal(err)
	}
	revision := JobAIContextRevision{
		RevisionHash: "revision-overview", ContextID: "context-overview",
		SourceKind: legacyJobConfigSourceKind, SourceJobRef: "job-overview",
		DisplayName: "产品经理", Environment: "production",
		SourcePackage: m5ai.JobConfigDocumentPackage{},
		Communication: m5ai.CommunicationView{},
		CreatedAt:     now.Add(-time.Hour),
	}
	if err := s.db.Create(&revision).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&JobAIContextHead{
		SourceKind: legacyJobConfigSourceKind, SourceJobRef: "job-overview",
		ContextID: revision.ContextID, RevisionHash: revision.RevisionHash,
		ActivationCurrent: false, LastSyncedAt: now.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatal(err)
	}
	otherRevision := revision
	otherRevision.RevisionHash = "revision-overview-other"
	otherRevision.ContextID = "context-overview-other"
	otherRevision.SourceJobRef = "job-overview-other"
	otherRevision.DisplayName = "数据分析师"
	otherRevision.CreatedAt = now.Add(-30 * time.Minute)
	if err := s.db.Create(&otherRevision).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&JobAIContextHead{
		SourceKind: legacyJobConfigSourceKind, SourceJobRef: otherRevision.SourceJobRef,
		ContextID: otherRevision.ContextID, RevisionHash: otherRevision.RevisionHash,
		ActivationCurrent: true, LastSyncedAt: now.Add(-30 * time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	got, err := s.AppOverview(AppOverviewRequest{
		Now: now, Platform: "zhilian", AccountRef: "overview-account",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Job.Available || got.Job.Name != "数据分析师" ||
		got.Job.BackendJobID != otherRevision.SourceJobRef ||
		got.Statistics.TodayRated.Value == nil || *got.Statistics.TodayRated.Value != 1 {
		t.Fatalf("unexpected overview: %+v", got)
	}
	if !got.Statistics.TotalInterviewed.Exact ||
		got.Statistics.TodayCompletedInterviews.Exact {
		t.Fatalf("历史状态可精确计数、缺少跃迁时刻的今日指标不得猜测: %+v", got.Statistics)
	}

	backendJobID := revision.SourceJobRef
	if err := s.db.Create(&SourcingBatch{
		BatchID: "batch-overview-bound", Platform: "zhilian", AccountRef: "overview-account",
		ContextRevisionHash: revision.RevisionHash, BackendJobID: &backendJobID,
		TargetCount: 30, Status: SourcingBatchPreparing, StartedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	got, err = s.AppOverview(AppOverviewRequest{
		Now: now, CurrentBatchID: "batch-overview-bound",
		Platform: "zhilian", AccountRef: "overview-account",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Job.BackendJobID != backendJobID || got.Job.Name != "产品经理" {
		t.Fatalf("当前批次必须盖过全局较新职位: %+v", got.Job)
	}

	currentRevision := revision
	currentRevision.RevisionHash = "revision-overview-current"
	currentRevision.ContextID = "context-overview-current"
	currentRevision.DisplayName = "高级产品经理"
	currentRevision.CreatedAt = now
	if err := s.db.Create(&currentRevision).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&JobAIContextHead{}).
		Where("source_kind = ? AND source_job_ref = ?", legacyJobConfigSourceKind, backendJobID).
		Updates(map[string]any{
			"context_id":     currentRevision.ContextID,
			"revision_hash":  currentRevision.RevisionHash,
			"last_synced_at": now,
		}).Error; err != nil {
		t.Fatal(err)
	}
	got, err = s.AppOverview(AppOverviewRequest{
		Now: now, CurrentBatchID: "batch-overview-bound",
		Platform: "zhilian", AccountRef: "overview-account",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Job.BackendJobID != backendJobID || got.Job.Name != "高级产品经理" {
		t.Fatalf("批次职位应显示同一 Job.ID 的最新配置: %+v", got.Job)
	}
	if _, err := s.AppOverview(AppOverviewRequest{
		Now: now, CurrentBatchID: "batch-overview-bound",
		Platform: "zhilian", AccountRef: "other-account",
	}); !errors.Is(err, ErrAppProjectionConflict) {
		t.Fatalf("跨账号批次不得回退到全局职位: %v", err)
	}
}

// 改期后必须只认最新那张邀面卡。首页日程一度先按日期筛卡、再在当天的卡
// 里挑最新，于是已经作废的旧时段留在今日日程里，而候选人列表按 seq DESC
// 取最新卡，同一个人在两个页面显示两个时间；改期到明天时更是明明今天没
// 有面试却仍在列。
func TestAppTodayInterviewsFollowsLatestInviteCard(t *testing.T) {
	s := openTest(t)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.Local)
	platform, accountRef := "zhilian", "today-interview-account"
	createM4Account(t, s, platform, accountRef)

	seed := func(userRef, profileID, conversationRef string) {
		t.Helper()
		displayName := "候选人" + profileID
		if err := s.db.Create(&Candidate{
			Platform: platform, PlatformUserRef: userRef, DisplayName: &displayName,
			FirstSeenAt: now.Add(-48 * time.Hour), LastSeenAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		positionTitle := "测试职位"
		if err := s.db.Create(&CandidateProfile{
			ProfileID: profileID, Platform: platform, AccountRef: accountRef,
			PlatformUserRef: userRef, PositionRef: "position-" + profileID,
			PositionTitle: &positionTitle, MainStatus: CandidateProfileInterviewed,
			ConversationRef: &conversationRef, ResumeCaptureState: ResumeCaptureUnattempted,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	card := func(conversationRef string, seq int64, startsAt time.Time) {
		t.Helper()
		starts := startsAt.UnixMilli()
		tsApprox := now.Add(-time.Hour).UnixMilli()
		if err := s.db.Create(&Message{
			Platform: platform, AccountRef: accountRef, ConversationRef: conversationRef,
			Seq: seq, Direction: "out", Kind: "card", CardType: "interviewInvite",
			CardState:   "accepted",
			ContentHash: strings.Repeat("a", 62) + string(rune('0'+seq)) + conversationRef[len(conversationRef)-1:],
			InterviewStartsAtMs: &starts, TsApproxMs: &tsApprox, Origin: "self",
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	// 甲：约今天 10:00 后改期到今天 15:00。
	seed("U-move", "P-move", "C-move")
	card("C-move", 1, time.Date(2026, 7, 30, 10, 0, 0, 0, time.Local))
	card("C-move", 2, time.Date(2026, 7, 30, 15, 0, 0, 0, time.Local))
	// 乙：约今天 11:00 后改期到明天，今天已经不该有他。
	seed("U-post", "P-post", "C-post")
	card("C-post", 1, time.Date(2026, 7, 30, 11, 0, 0, 0, time.Local))
	card("C-post", 2, time.Date(2026, 7, 31, 9, 0, 0, 0, time.Local))
	// 丙：昨天约过，改期到今天 09:00，今天必须出现。
	seed("U-pull", "P-pull", "C-pull")
	card("C-pull", 1, time.Date(2026, 7, 29, 16, 0, 0, 0, time.Local))
	card("C-pull", 2, time.Date(2026, 7, 30, 9, 0, 0, 0, time.Local))

	overview, err := s.AppOverview(AppOverviewRequest{
		Now: now, Platform: platform, AccountRef: accountRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string, len(overview.TodayInterviews))
	for _, item := range overview.TodayInterviews {
		got[item.ProfileID] = time.UnixMilli(item.StartsAtMs).In(time.Local).Format("15:04")
	}
	want := map[string]string{"P-move": "15:00", "P-pull": "09:00"}
	if len(got) != len(want) {
		t.Fatalf("今日面试应只剩最新卡落在今天的候选人，实得 %v", got)
	}
	for profileID, clock := range want {
		if got[profileID] != clock {
			t.Fatalf("%s 今日面试时间应为 %s，实得 %q（全量 %v）", profileID, clock, got[profileID], got)
		}
	}

	// 与候选人列表同口径：两处都取最新卡，不得各说各话。
	list, err := s.AppCandidates(AppCandidateListQuery{
		Platform: platform, AccountRef: accountRef, View: AppCandidateViewInterviewed,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list.Items {
		if item.InterviewStartsAtMs == nil {
			t.Fatalf("%s 列表缺少面试时间", item.ProfileID)
		}
		listClock := time.UnixMilli(*item.InterviewStartsAtMs).In(time.Local).Format("15:04")
		if summaryClock, ok := got[item.ProfileID]; ok && summaryClock != listClock {
			t.Fatalf("%s 首页日程 %s 与列表 %s 不一致", item.ProfileID, summaryClock, listClock)
		}
	}
}

func appPtrTime(value time.Time) *time.Time { return &value }
func appPtrString(value string) *string     { return &value }
