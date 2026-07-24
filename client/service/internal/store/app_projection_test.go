package store

import (
	"encoding/json"
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

	list, err := s.AppCandidates(AppCandidateListQuery{
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
	detail, err := s.AppCandidateDetail(profileID)
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
		LastSyncedAt: now.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatal(err)
	}
	got, err := s.AppOverview(AppOverviewRequest{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Job.Available || got.Job.Name != "产品经理" ||
		got.Statistics.TodayRated.Value == nil || *got.Statistics.TodayRated.Value != 1 {
		t.Fatalf("unexpected overview: %+v", got)
	}
	if !got.Statistics.TotalInterviewed.Exact ||
		got.Statistics.TodayCompletedInterviews.Exact {
		t.Fatalf("历史状态可精确计数、缺少跃迁时刻的今日指标不得猜测: %+v", got.Statistics)
	}
}

func appPtrTime(value time.Time) *time.Time { return &value }
func appPtrString(value string) *string     { return &value }
