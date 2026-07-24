package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/contract/gen/go/protocol"
)

type selectionRunFixture struct {
	RunID       string
	Score       *int
	ScoreFailed bool
	Pending     bool
	InFlight    bool
	Contact     string
	DisplayName *string
	Basic       []protocol.CandidateResumeLabelValue
	CapturedAt  time.Time
}

func sourcingSelectionRevision(
	at time.Time,
	revisionHash string,
	minScore, targetMin, targetMax, maleRatioLimit int,
) m5ai.ContextRevision {
	documents := []m5ai.JobConfigDocument{
		{DocType: "候选人筛选", Content: fmt.Sprintf(
			`{"minScore":%d,"targetMin":%d,"targetMax":%d,"maleRatioLimit":%d}`,
			minScore, targetMin, targetMax, maleRatioLimit,
		)},
		{DocType: "多轮沟通", Content: "reply"},
		{DocType: "客户事实库", Content: "facts"},
		{DocType: "意向判断", Content: "intent"},
		{DocType: "打分", Content: "score {resume_json}"},
		{DocType: "招呼语", Content: `{"prompt":"{career_state} {resume_summary_json}"}`},
		{DocType: "职位筛选", Content: `[]`},
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].DocType < documents[j].DocType })
	return m5ai.ContextRevision{
		ContextID: "context-" + revisionHash, RevisionHash: revisionHash,
		SourceKind: "localImport", SourceJobRef: "11", DisplayName: "合成职位",
		SourcePackage: m5ai.JobConfigDocumentPackage{Documents: documents},
		Communication: m5ai.CommunicationView{
			ReplyPrompt: "reply", IntentPrompt: "intent", CustomerFacts: "facts",
			MappingVersion: m5ai.MappingVersion,
		},
		CreatedAt: at,
	}
}

func prepareSourcingSelectionStore(
	t *testing.T,
	revisionHash string,
	minScore, targetMin, targetMax, maleRatioLimit int,
	base time.Time,
) (*Store, AccountKey) {
	t.Helper()
	s := openTest(t)
	key := AccountKey{Platform: "zhilian", AccountRef: "account-" + revisionHash}
	createM4Account(t, s, key.Platform, key.AccountRef)
	if _, _, err := s.SaveJobAIContextRevision(sourcingSelectionRevision(
		base.Add(-time.Hour), revisionHash, minScore, targetMin, targetMax, maleRatioLimit,
	)); err != nil {
		t.Fatal(err)
	}
	return s, key
}

func insertCompletedSelectionBatch(
	t *testing.T,
	s *Store,
	key AccountKey,
	batchID, revisionHash string,
	base time.Time,
	fixtures []selectionRunFixture,
) []SourcingCandidateRun {
	t.Helper()
	endedAt := base.Add(time.Hour)
	backendJobID := "11"
	if err := s.db.Create(&SourcingBatch{
		BatchID: batchID, Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revisionHash, BackendJobID: &backendJobID,
		TargetCount: len(fixtures),
		Status:      SourcingBatchCompleted, StartedAt: base.Add(-time.Minute), EndedAt: &endedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	runs := make([]SourcingCandidateRun, len(fixtures))
	for i, fixture := range fixtures {
		contact := fixture.Contact
		if contact == "" {
			contact = string(protocol.CandidateContactStateUnestablished)
		}
		capturedAt := fixture.CapturedAt
		if capturedAt.IsZero() {
			capturedAt = base.Add(time.Duration(i) * time.Minute)
		}
		resumeJSON, err := json.Marshal(struct {
			Basic           []protocol.CandidateResumeLabelValue `json:"basic"`
			Expectations    []protocol.CandidateResumeLabelValue `json:"expectations"`
			SelfEvaluation  string                               `json:"selfEvaluation"`
			Education       string                               `json:"education"`
			WorkExperiences string                               `json:"workExperiences"`
		}{Basic: fixture.Basic, Expectations: []protocol.CandidateResumeLabelValue{}})
		if err != nil {
			t.Fatal(err)
		}
		memberBatchID := batchID
		run := SourcingCandidateRun{
			RunID: fixture.RunID, BatchID: &memberBatchID,
			Platform: key.Platform, AccountRef: key.AccountRef,
			ContextRevisionHash: revisionHash, PlatformUserRef: "user-" + fixture.RunID,
			DisplayName: fixture.DisplayName, PositionRef: "position-" + batchID,
			ContactState: contact, SourceLogicalDispatchID: "logical-" + fixture.RunID,
			ObservedAt: capturedAt.UnixMilli(), CapturedAt: capturedAt,
			SchemaVersion: 1, ContentHash: "content-" + fixture.RunID,
			ResumeJSON: string(resumeJSON),
		}
		if err := s.db.Create(&run).Error; err != nil {
			t.Fatal(err)
		}
		runs[i] = run
	}
	for i, fixture := range fixtures {
		if fixture.Pending {
			continue
		}
		reserveSelectionScore(t, s, batchID, runs[i], fixture, runs[i].CapturedAt.Add(time.Second))
	}
	return runs
}

func reserveSelectionScore(
	t *testing.T,
	s *Store,
	batchID string,
	run SourcingCandidateRun,
	fixture selectionRunFixture,
	at time.Time,
) {
	t.Helper()
	reservation := sourcingScoreReservation(run, "invocation-"+run.RunID, at)
	reservation.BatchID = batchID
	if _, err := s.ReserveSourcingScore(reservation); err != nil {
		t.Fatal(err)
	}
	if fixture.InFlight {
		return
	}
	if fixture.ScoreFailed {
		if _, err := s.CompleteSourcingScore(CompleteSourcingScoreRequest{Completion: AIInvocationCompletion{
			InvocationID: reservation.InvocationID, Status: AIInvocationProviderRejected,
			OutputHash: "failed-" + run.RunID, ErrorClass: "providerRejected",
			FinishedAt: at.Add(time.Second),
		}}); err != nil {
			t.Fatal(err)
		}
		return
	}
	if fixture.Score == nil {
		t.Fatal("成功评分 fixture 缺少 score")
	}
	zero := 0
	if _, err := s.CompleteSourcingScore(CompleteSourcingScoreRequest{
		Completion: AIInvocationCompletion{
			InvocationID: reservation.InvocationID, Status: AIInvocationOK,
			OutputHash: "output-" + run.RunID, InputTokens: 2, OutputTokens: 1,
			ReasoningTokens: &zero, UsageShape: AIInvocationUsageComplete,
			ReasoningContentEmpty: true, FinishedAt: at.Add(time.Second),
		},
		Score: fixture.Score,
	}); err != nil {
		t.Fatal(err)
	}
}

func intPointer(value int) *int { return &value }

func sourcingSelectionOutcomes(t *testing.T, s *Store, batchID string) map[string]SourcingSelectionDecision {
	t.Helper()
	var decisions []SourcingSelectionDecision
	if err := s.db.Table("sourcing_selection_decisions AS decision").
		Select("decision.*").
		Joins("JOIN sourcing_candidate_runs AS run ON run.run_id = decision.run_id").
		Where("run.batch_id = ?", batchID).
		Find(&decisions).Error; err != nil {
		t.Fatal(err)
	}
	out := make(map[string]SourcingSelectionDecision, len(decisions))
	for _, decision := range decisions {
		out[decision.RunID] = decision
	}
	return out
}

func TestSelectCompletedSourcingBatchUsesScoreOrderAndStableTieBreak(t *testing.T) {
	base := time.Date(2026, 7, 22, 16, 0, 0, 0, time.UTC)
	s, key := prepareSourcingSelectionStore(t, "selection-order", 5, 2, 2, 100, base)
	sameCapturedAt := base.Add(time.Minute)
	fixtures := []selectionRunFixture{
		{RunID: "run-low-early", Score: intPointer(7), CapturedAt: base},
		{RunID: "run-tie-b", Score: intPointer(9), CapturedAt: sameCapturedAt},
		{RunID: "run-tie-a", Score: intPointer(9), CapturedAt: sameCapturedAt},
		{RunID: "run-high-late", Score: intPointer(10), CapturedAt: base.Add(3 * time.Minute)},
		{RunID: "run-below", Score: intPointer(4), CapturedAt: base.Add(4 * time.Minute)},
	}
	insertCompletedSelectionBatch(t, s, key, "batch-selection-order", "selection-order", base, fixtures)

	decidedAt := base.Add(2 * time.Hour)
	selection, err := s.SelectCompletedSourcingBatch("batch-selection-order", decidedAt)
	if err != nil {
		t.Fatal(err)
	}
	if selection.AlgorithmVersion != SourcingSelectionAlgorithmVersion || selection.TargetCount != 2 ||
		selection.PoolCount != 5 || selection.EligibleCount != 4 || selection.SelectedCount != 2 ||
		selection.MaleSelectedCount != 0 || selection.UnknownGenderCount != 5 ||
		!selection.CompletedAt.Equal(decidedAt) {
		t.Fatalf("筛选摘要错误: %+v", selection)
	}
	outcomes := sourcingSelectionOutcomes(t, s, selection.BatchID)
	want := map[string]SourcingSelectionOutcome{
		"run-high-late": SourcingSelectionSelected,
		"run-tie-a":     SourcingSelectionSelected,
		"run-tie-b":     SourcingSelectionQuotaFull,
		"run-low-early": SourcingSelectionQuotaFull,
		"run-below":     SourcingSelectionScoreBelowThreshold,
	}
	for runID, outcome := range want {
		if outcomes[runID].Outcome != outcome {
			t.Fatalf("%s outcome=%s want=%s", runID, outcomes[runID].Outcome, outcome)
		}
	}
}

func TestSelectCompletedSourcingBatchCoversGenderAndTerminalBranches(t *testing.T) {
	base := time.Date(2026, 7, 22, 17, 0, 0, 0, time.UTC)
	s, key := prepareSourcingSelectionStore(t, "selection-branches", 5, 5, 5, 20, base)
	fixtures := []selectionRunFixture{
		{RunID: "run-existing", Score: intPointer(10), DisplayName: textPointer("已有档案")},
		{RunID: "run-male-explicit", Score: intPointer(10), Basic: []protocol.CandidateResumeLabelValue{{Label: "性别", Value: "男"}}},
		{RunID: "run-male-title", Score: intPointer(9), DisplayName: textPointer("候选人先生")},
		{RunID: "run-female-explicit", Score: intPointer(8), Basic: []protocol.CandidateResumeLabelValue{{Label: "性别", Value: "女"}}},
		{RunID: "run-female-title", Score: intPointer(8), DisplayName: textPointer("候选人女士")},
		{RunID: "run-unknown", Score: intPointer(7), DisplayName: textPointer("候选人甲")},
		{RunID: "run-explicit-unknown", Score: intPointer(6), DisplayName: textPointer("不应回退先生"), Basic: []protocol.CandidateResumeLabelValue{{Label: "性别", Value: "未知"}}},
		{RunID: "run-failed", ScoreFailed: true},
		{RunID: "run-contacted", Score: intPointer(10), Contact: "established"},
		{RunID: "run-low-score", Score: intPointer(4)},
	}
	runs := insertCompletedSelectionBatch(
		t, s, key, "batch-selection-branches", "selection-branches", base, fixtures,
	)
	var existingRun SourcingCandidateRun
	for _, run := range runs {
		if run.RunID == "run-existing" {
			existingRun = run
			break
		}
	}
	if _, err := s.SelectCandidateProfile(SelectCandidateProfileRequest{
		ProfileID: "profile-existing", Scope: CandidateProfileScope{
			Platform: existingRun.Platform, AccountRef: existingRun.AccountRef,
			PlatformUserRef: existingRun.PlatformUserRef, PositionRef: "position-existing-other",
		},
		DisplayName: existingRun.DisplayName, ObservedAt: base.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	selection, err := s.SelectCompletedSourcingBatch("batch-selection-branches", base.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if selection.TargetCount != 5 || selection.MaleLimit != 1 || selection.EligibleCount != 6 ||
		selection.SelectedCount != 5 || selection.MaleSelectedCount != 1 || selection.UnknownGenderCount != 6 {
		t.Fatalf("分支摘要错误: %+v", selection)
	}
	outcomes := sourcingSelectionOutcomes(t, s, selection.BatchID)
	want := map[string]SourcingSelectionOutcome{
		"run-existing":         SourcingSelectionExistingProfile,
		"run-male-explicit":    SourcingSelectionSelected,
		"run-male-title":       SourcingSelectionMaleRatioLimited,
		"run-female-explicit":  SourcingSelectionSelected,
		"run-female-title":     SourcingSelectionSelected,
		"run-unknown":          SourcingSelectionSelected,
		"run-explicit-unknown": SourcingSelectionSelected,
		"run-failed":           SourcingSelectionScoringFailed,
		"run-contacted":        SourcingSelectionContactStateRejected,
		"run-low-score":        SourcingSelectionScoreBelowThreshold,
	}
	for runID, outcome := range want {
		if outcomes[runID].Outcome != outcome {
			t.Fatalf("%s outcome=%s want=%s", runID, outcomes[runID].Outcome, outcome)
		}
	}
}

func TestSelectCompletedSourcingBatchRequiresCompleteTargetBatchAndIsolatesOtherBatches(t *testing.T) {
	base := time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC)
	s, key := prepareSourcingSelectionStore(t, "selection-isolation", 5, 1, 1, 100, base)
	targetRuns := insertCompletedSelectionBatch(t, s, key, "batch-selection-target", "selection-isolation", base,
		[]selectionRunFixture{{RunID: "run-target-pending", Pending: true}})
	insertCompletedSelectionBatch(t, s, key, "batch-selection-other", "selection-isolation", base.Add(time.Hour),
		[]selectionRunFixture{{RunID: "run-other", Score: intPointer(9)}})

	if selection, err := s.SelectCompletedSourcingBatch("batch-selection-target", base.Add(3*time.Hour)); selection != nil || !errors.Is(err, ErrSourcingSelectionNotReady) {
		t.Fatalf("评分未完成应拒绝: selection=%+v err=%v", selection, err)
	}
	if got := sourcingSelectionOutcomes(t, s, "batch-selection-other"); len(got) != 0 {
		t.Fatalf("目标批次失败不应消费其他批次: %+v", got)
	}
	fixture := selectionRunFixture{RunID: targetRuns[0].RunID, Score: intPointer(8)}
	reserveSelectionScore(t, s, "batch-selection-target", targetRuns[0], fixture, base.Add(4*time.Hour))
	if _, err := s.SelectCompletedSourcingBatch("batch-selection-target", base.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := sourcingSelectionOutcomes(t, s, "batch-selection-target"); len(got) != 1 {
		t.Fatalf("目标批次没有形成完整决策: %+v", got)
	}
	if got := sourcingSelectionOutcomes(t, s, "batch-selection-other"); len(got) != 0 {
		t.Fatalf("同 revision 其他批次被误选: %+v", got)
	}
}

func TestSelectCompletedSourcingBatchRollsBackAllFactsOnMiddleWriteFailure(t *testing.T) {
	base := time.Date(2026, 7, 22, 19, 0, 0, 0, time.UTC)
	s, key := prepareSourcingSelectionStore(t, "selection-rollback", 5, 3, 3, 100, base)
	insertCompletedSelectionBatch(t, s, key, "batch-selection-rollback", "selection-rollback", base,
		[]selectionRunFixture{
			{RunID: "run-rollback-first", Score: intPointer(10)},
			{RunID: "run-rollback-second", Score: intPointer(9)},
			{RunID: "run-rollback-third", Score: intPointer(8)},
		})
	if err := s.db.Exec(`CREATE TRIGGER fail_second_selection_decision
		BEFORE INSERT ON sourcing_selection_decisions
		WHEN NEW.run_id = 'run-rollback-second'
		BEGIN SELECT RAISE(ABORT, 'forced selection failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if selection, err := s.SelectCompletedSourcingBatch("batch-selection-rollback", base.Add(time.Hour)); selection != nil || err == nil {
		t.Fatalf("注入中途失败未返回错误: selection=%+v err=%v", selection, err)
	}
	var decisions, profiles, candidates, summaries int64
	_ = s.db.Model(&SourcingSelectionDecision{}).Count(&decisions).Error
	_ = s.db.Model(&CandidateProfile{}).Count(&profiles).Error
	_ = s.db.Model(&Candidate{}).Count(&candidates).Error
	_ = s.db.Model(&SourcingBatchSelection{}).Count(&summaries).Error
	if decisions != 0 || profiles != 0 || candidates != 0 || summaries != 0 {
		t.Fatalf("中途失败留下半批事实: decisions=%d profiles=%d candidates=%d summaries=%d",
			decisions, profiles, candidates, summaries)
	}
}

func TestSelectCompletedSourcingBatchReplayIsIdempotentAndCreatesNoSendFacts(t *testing.T) {
	base := time.Date(2026, 7, 22, 20, 0, 0, 0, time.UTC)
	s, key := prepareSourcingSelectionStore(t, "selection-replay", 5, 1, 1, 100, base)
	insertCompletedSelectionBatch(t, s, key, "batch-selection-replay", "selection-replay", base,
		[]selectionRunFixture{{RunID: "run-replay", Score: intPointer(9)}})
	first, err := s.SelectCompletedSourcingBatch("batch-selection-replay", base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := s.SelectCompletedSourcingBatch("batch-selection-replay", base.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if replayed.BatchID != first.BatchID || replayed.TargetCount != first.TargetCount ||
		!replayed.CompletedAt.Equal(first.CompletedAt) {
		t.Fatalf("重放没有返回首次摘要: first=%+v replayed=%+v", first, replayed)
	}
	projected, err := s.SourcingBatchSelectionByBatchID(first.BatchID)
	if err != nil || projected == nil || projected.BatchID != first.BatchID || projected.SelectedCount != 1 {
		t.Fatalf("安全摘要投影错误: projection=%+v err=%v", projected, err)
	}
	var decisions, profiles, summaries, intents, heads, commands int64
	_ = s.db.Model(&SourcingSelectionDecision{}).Count(&decisions).Error
	_ = s.db.Model(&CandidateProfile{}).Count(&profiles).Error
	_ = s.db.Model(&SourcingBatchSelection{}).Count(&summaries).Error
	_ = s.db.Model(&EffectIntent{}).Count(&intents).Error
	_ = s.db.Model(&CandidateGreetingHead{}).Count(&heads).Error
	_ = s.db.Model(&CmdRecord{}).Count(&commands).Error
	if decisions != 1 || profiles != 1 || summaries != 1 || intents != 0 || heads != 0 || commands != 0 {
		t.Fatalf("重放增生或越界创建发送事实: decisions=%d profiles=%d summaries=%d intents=%d heads=%d commands=%d",
			decisions, profiles, summaries, intents, heads, commands)
	}
	var profile CandidateProfile
	if err := s.db.First(&profile).Error; err != nil ||
		profile.BackendJobID == nil || *profile.BackendJobID != "11" {
		t.Fatalf("selected 档案未继承后台职位 ID: profile=%+v err=%v", profile, err)
	}
}

func TestStableSourcingSelectionTargetIsDeterministicAndBounded(t *testing.T) {
	first := stableSourcingSelectionTarget("batch-target", "revision-target", 80, 90)
	for i := 0; i < 20; i++ {
		if got := stableSourcingSelectionTarget("batch-target", "revision-target", 80, 90); got != first {
			t.Fatalf("稳定目标漂移: first=%d got=%d", first, got)
		}
	}
	if first < 80 || first > 90 || stableSourcingSelectionTarget("batch-fixed", "revision", 7, 7) != 7 {
		t.Fatalf("稳定目标越界: %d", first)
	}
}
