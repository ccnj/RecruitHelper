package store

import (
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
)

func ensureSourcingScoreRevision(t *testing.T, s *Store, revisionHash string, at time.Time) {
	t.Helper()
	existing, err := s.JobAIContextRevisionByHash(revisionHash)
	if err != nil {
		t.Fatal(err)
	}
	if existing != nil {
		return
	}
	revision := contextRevisionFixture(
		"context-"+revisionHash, revisionHash, at.Add(-time.Hour),
	)
	revision.SourceKind = legacyJobConfigSourceKind
	revision.SourceJobRef = "11"
	if _, err := s.SaveCurrentLegacyJobAIContext(
		[]m5ai.ContextRevision{revision}, at.Add(-time.Hour),
	); err != nil {
		t.Fatal(err)
	}
}

func seedCompletedSourcingScoreBatch(
	t *testing.T,
	s *Store,
	batchID string,
	key AccountKey,
	revisionHash string,
	runIDs []string,
	capturedAt time.Time,
) []SourcingCandidateRun {
	t.Helper()
	ensureSourcingScoreRevision(t, s, revisionHash, capturedAt)
	endedAt := capturedAt.Add(time.Duration(len(runIDs)+1) * time.Minute)
	backendJobID := "11"
	if err := s.db.Create(&SourcingBatch{
		BatchID: batchID, Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revisionHash, BackendJobID: &backendJobID, TargetCount: len(runIDs),
		Status: SourcingBatchCompleted, StartedAt: capturedAt.Add(-time.Minute), EndedAt: &endedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	runs := make([]SourcingCandidateRun, len(runIDs))
	for index, runID := range runIDs {
		memberBatchID := batchID
		runAt := capturedAt.Add(time.Duration(index) * time.Minute)
		runs[index] = SourcingCandidateRun{
			RunID: runID, BatchID: &memberBatchID, Platform: key.Platform, AccountRef: key.AccountRef,
			ContextRevisionHash: revisionHash, PlatformUserRef: "user-" + runID,
			PositionRef: "position-" + batchID, ContactState: "unestablished",
			SourceLogicalDispatchID: "logical-" + runID, ObservedAt: runAt.UnixMilli(),
			CapturedAt: runAt, SchemaVersion: 1, ContentHash: "content-" + runID,
			ResumeJSON: `{"basic":[],"expectations":[],"selfEvaluation":"","education":"","workExperiences":""}`,
		}
		if err := s.db.Create(&runs[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	return runs
}

func seedSourcingScoreRun(t *testing.T, s *Store, runID string, key AccountKey, revisionHash string, capturedAt time.Time) SourcingCandidateRun {
	t.Helper()
	return seedCompletedSourcingScoreBatch(
		t, s, "batch-"+runID, key, revisionHash, []string{runID}, capturedAt,
	)[0]
}

func sourcingScoreReservation(run SourcingCandidateRun, invocationID string, startedAt time.Time) ReserveSourcingScoreRequest {
	batchID := ""
	if run.BatchID != nil {
		batchID = *run.BatchID
	}
	return ReserveSourcingScoreRequest{
		InvocationID: invocationID, BatchID: batchID, RunID: run.RunID,
		ContextRevisionHash: run.ContextRevisionHash, RunContentHash: run.ContentHash,
		Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-" + run.RunID,
		StartedAt: startedAt,
	}
}

func scorePointer(value int) *int { return &value }

func TestSourcingScoreStageKeepsFirstInvocationRevisionAfterHeadAdvances(t *testing.T) {
	s := openTest(t)
	key := AccountKey{Platform: "zhilian", AccountRef: "account-score-stage-revision"}
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	runs := seedCompletedSourcingScoreBatch(
		t, s, "batch-score-stage-revision", key, "score-stage-a",
		[]string{"run-score-stage-a", "run-score-stage-b"}, base,
	)
	first := sourcingScoreReservation(runs[0], "score-stage-first", base.Add(time.Minute))
	if result, err := s.ReserveSourcingScore(first); err != nil || result == nil || !result.Created {
		t.Fatalf("首条评分预留失败: result=%+v err=%v", result, err)
	}

	newer := contextRevisionFixture("context-score-stage-b", "score-stage-b", base.Add(time.Hour))
	newer.SourceKind = legacyJobConfigSourceKind
	newer.SourceJobRef = "11"
	if _, err := s.SaveCurrentLegacyJobAIContext(
		[]m5ai.ContextRevision{newer}, base.Add(time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	resolved, err := s.SourcingScoringRevision("batch-score-stage-revision")
	if err != nil || resolved == nil || resolved.RevisionHash != "score-stage-a" {
		t.Fatalf("head 推进后评分阶段换版: revision=%+v err=%v", resolved, err)
	}
	second := sourcingScoreReservation(runs[1], "score-stage-second", base.Add(2*time.Minute))
	if result, err := s.ReserveSourcingScore(second); err != nil || result == nil || !result.Created {
		t.Fatalf("余下成员未沿用首条 revision: result=%+v err=%v", result, err)
	}
	wrong := second
	wrong.InvocationID = "score-stage-wrong"
	wrong.ContextRevisionHash = newer.RevisionHash
	if result, err := s.ReserveSourcingScore(wrong); result != nil ||
		!errors.Is(err, ErrAIInvocationConflict) {
		t.Fatalf("评分阶段混入新 head: result=%+v err=%v", result, err)
	}
}

func TestNextSourcingBatchRunWithoutScoreUsesOnlyCompletedTargetBatch(t *testing.T) {
	s := openTest(t)
	key := AccountKey{Platform: "zhilian", AccountRef: "account-score-next"}
	revisionHash := "revision-score-next"
	base := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	target := seedCompletedSourcingScoreBatch(
		t, s, "batch-score-target", key, revisionHash,
		[]string{"run-score-first", "run-score-second"}, base,
	)
	seedCompletedSourcingScoreBatch(
		t, s, "batch-score-other", key, revisionHash, []string{"run-score-other-batch"}, base.Add(-time.Hour),
	)
	legacy := SourcingCandidateRun{
		RunID: "run-score-legacy", Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revisionHash, PlatformUserRef: "user-score-legacy",
		PositionRef: "position-score-legacy", ContactState: "unestablished",
		SourceLogicalDispatchID: "logical-score-legacy", ObservedAt: base.Add(-2 * time.Hour).UnixMilli(),
		CapturedAt: base.Add(-2 * time.Hour), SchemaVersion: 1, ContentHash: "content-score-legacy", ResumeJSON: "{}",
	}
	if err := s.db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	openBatchID := "batch-score-open"
	if err := s.db.Create(&SourcingBatch{
		BatchID: openBatchID, Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revisionHash, TargetCount: 1,
		Status: SourcingBatchCollecting, StartedAt: base,
	}).Error; err != nil {
		t.Fatal(err)
	}
	openRun := SourcingCandidateRun{
		RunID: "run-score-open", BatchID: &openBatchID, Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revisionHash, PlatformUserRef: "user-score-open",
		PositionRef: "position-score-open", ContactState: "unestablished",
		SourceLogicalDispatchID: "logical-score-open", ObservedAt: base.UnixMilli(), CapturedAt: base,
		SchemaVersion: 1, ContentHash: "content-score-open", ResumeJSON: "{}",
	}
	if err := s.db.Create(&openRun).Error; err != nil {
		t.Fatal(err)
	}

	next, err := s.NextSourcingBatchRunWithoutScore("batch-score-target")
	if err != nil || next == nil || next.RunID != target[0].RunID {
		t.Fatalf("未返回目标批次最早成员: next=%+v err=%v", next, err)
	}
	request := sourcingScoreReservation(target[0], "score-invocation-first", base.Add(2*time.Minute))
	reserved, err := s.ReserveSourcingScore(request)
	if err != nil || reserved == nil || !reserved.Created || reserved.Invocation.FinishedAt != nil {
		t.Fatalf("首次评分预留失败: result=%+v err=%v", reserved, err)
	}
	replay := request
	replay.InvocationID = "score-invocation-must-not-be-created"
	replayed, err := s.ReserveSourcingScore(replay)
	if err != nil || replayed == nil || replayed.Created || replayed.Invocation.InvocationID != request.InvocationID {
		t.Fatalf("同 RunID 重复预留未复用原事实: result=%+v err=%v", replayed, err)
	}
	next, err = s.NextSourcingBatchRunWithoutScore("batch-score-target")
	if err != nil || next == nil || next.RunID != target[1].RunID {
		t.Fatalf("目标批次第二成员读取错误: next=%+v err=%v", next, err)
	}
	if next, err := s.NextSourcingBatchRunWithoutScore(openBatchID); next != nil || !errors.Is(err, ErrSourcingBatchStateConflict) {
		t.Fatalf("未完成批次不得评分: next=%+v err=%v", next, err)
	}
}

func TestSourcingBatchScoreScopeRequiresExactTargetMembers(t *testing.T) {
	s := openTest(t)
	base := time.Date(2026, 7, 22, 10, 30, 0, 0, time.UTC)
	key := AccountKey{Platform: "zhilian", AccountRef: "account-score-count"}
	ensureSourcingScoreRevision(t, s, "revision-score-count", base)
	endedAt := base.Add(time.Minute)
	if err := s.db.Create(&SourcingBatch{
		BatchID: "batch-score-count", Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: "revision-score-count", TargetCount: 2,
		Status: SourcingBatchCompleted, StartedAt: base.Add(-time.Minute), EndedAt: &endedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	batchID := "batch-score-count"
	run := SourcingCandidateRun{
		RunID: "run-score-count", BatchID: &batchID, Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: "revision-score-count", PlatformUserRef: "user-score-count",
		PositionRef: "position-score-count", ContactState: "unestablished",
		SourceLogicalDispatchID: "logical-score-count", ObservedAt: base.UnixMilli(), CapturedAt: base,
		SchemaVersion: 1, ContentHash: "content-score-count", ResumeJSON: "{}",
	}
	if err := s.db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	if next, err := s.NextSourcingBatchRunWithoutScore(batchID); next != nil || !errors.Is(err, ErrSourcingBatchConflict) {
		t.Fatalf("成员数不足不得进入评分: next=%+v err=%v", next, err)
	}
	if progress, err := s.SourcingBatchScoringProgress(batchID); progress != nil || !errors.Is(err, ErrSourcingBatchConflict) {
		t.Fatalf("成员数不足不得伪造进度: progress=%+v err=%v", progress, err)
	}
}

func TestReserveSourcingScoreRechecksBatchRevisionAndRunMaterial(t *testing.T) {
	s := openTest(t)
	base := time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC)
	key := AccountKey{Platform: "zhilian", AccountRef: "account-score-binding"}
	run := seedSourcingScoreRun(t, s, "run-score-binding", key, "revision-score-binding", base)
	other := seedSourcingScoreRun(t, s, "run-score-binding-other", key, "revision-score-binding", base.Add(time.Minute))
	request := sourcingScoreReservation(run, "score-invocation-binding", base.Add(2*time.Minute))

	wrongBatch := request
	wrongBatch.BatchID = *other.BatchID
	if result, err := s.ReserveSourcingScore(wrongBatch); result != nil || !errors.Is(err, ErrSourcingBinding) {
		t.Fatalf("跨批次 run 未拒绝: result=%+v err=%v", result, err)
	}
	wrongRevision := request
	wrongRevision.ContextRevisionHash = "different-revision"
	if result, err := s.ReserveSourcingScore(wrongRevision); result != nil || !errors.Is(err, ErrSourcingBinding) {
		t.Fatalf("调用方覆盖批次 revision 未拒绝: result=%+v err=%v", result, err)
	}
	wrongContent := request
	wrongContent.RunContentHash = "different-content"
	if result, err := s.ReserveSourcingScore(wrongContent); result != nil || !errors.Is(err, ErrSourcingBinding) {
		t.Fatalf("content hash 变化未拒绝: result=%+v err=%v", result, err)
	}
	if _, err := s.ReserveSourcingScore(request); err != nil {
		t.Fatal(err)
	}
	conflict := request
	conflict.Provider = "other-provider"
	if result, err := s.ReserveSourcingScore(conflict); result != nil || !errors.Is(err, ErrAIInvocationConflict) {
		t.Fatalf("既有预留材料冲突未拒绝: result=%+v err=%v", result, err)
	}
}

func TestSourcingBatchScoringProgressAndProviderModelFreeze(t *testing.T) {
	s := openTest(t)
	base := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	key := AccountKey{Platform: "zhilian", AccountRef: "account-score-progress"}
	runs := seedCompletedSourcingScoreBatch(
		t, s, "batch-score-progress", key, "revision-score-progress",
		[]string{"run-score-progress-a", "run-score-progress-b", "run-score-progress-c"}, base,
	)
	progress, err := s.SourcingBatchScoringProgress("batch-score-progress")
	if err != nil || progress.PendingCount != 3 || progress.Completed || progress.Provider != "" || progress.Model != "" {
		t.Fatalf("初始进度错误: progress=%+v err=%v", progress, err)
	}

	first := sourcingScoreReservation(runs[0], "invocation-progress-a", base.Add(3*time.Minute))
	if _, err := s.ReserveSourcingScore(first); err != nil {
		t.Fatal(err)
	}
	progress, err = s.SourcingBatchScoringProgress("batch-score-progress")
	if err != nil || progress.InFlightCount != 1 || progress.PendingCount != 2 ||
		progress.Provider != first.Provider || progress.Model != first.Model || progress.Completed {
		t.Fatalf("预留后的进度错误: progress=%+v err=%v", progress, err)
	}

	mixed := sourcingScoreReservation(runs[1], "invocation-progress-b", base.Add(4*time.Minute))
	mixed.Model = "other-model"
	if result, err := s.ReserveSourcingScore(mixed); result != nil || !errors.Is(err, ErrAIInvocationConflict) {
		t.Fatalf("批内 provider/model 混用未拒绝: result=%+v err=%v", result, err)
	}
	zero := 0
	if _, err := s.CompleteSourcingScore(CompleteSourcingScoreRequest{
		Completion: AIInvocationCompletion{
			InvocationID: first.InvocationID, Status: AIInvocationOK, OutputHash: "output-progress-a",
			InputTokens: 2, OutputTokens: 1, ReasoningTokens: &zero,
			UsageShape: AIInvocationUsageComplete, ReasoningContentEmpty: true,
			FinishedAt: base.Add(5 * time.Minute),
		},
		Score: scorePointer(8),
	}); err != nil {
		t.Fatal(err)
	}
	second := sourcingScoreReservation(runs[1], "invocation-progress-b", base.Add(6*time.Minute))
	if _, err := s.ReserveSourcingScore(second); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteSourcingScore(CompleteSourcingScoreRequest{Completion: AIInvocationCompletion{
		InvocationID: second.InvocationID, Status: AIInvocationProviderRejected,
		ErrorClass: "providerRejected", FinishedAt: base.Add(7 * time.Minute),
	}}); err != nil {
		t.Fatal(err)
	}
	third := sourcingScoreReservation(runs[2], "invocation-progress-c", base.Add(8*time.Minute))
	if _, err := s.ReserveSourcingScore(third); err != nil {
		t.Fatal(err)
	}
	progress, err = s.SourcingBatchScoringProgress("batch-score-progress")
	if err != nil || progress.TargetCount != 3 || progress.OKCount != 1 || progress.FailedCount != 1 ||
		progress.InFlightCount != 1 || progress.PendingCount != 0 || progress.Completed {
		t.Fatalf("混合终局进度错误: progress=%+v err=%v", progress, err)
	}

	recovered, err := s.RecoverInterruptedAIInvocations(base.Add(9 * time.Minute))
	if err != nil || recovered != 1 {
		t.Fatalf("评分中断收敛失败: recovered=%d err=%v", recovered, err)
	}
	progress, err = s.SourcingBatchScoringProgress("batch-score-progress")
	if err != nil || progress.OKCount != 1 || progress.FailedCount != 2 || progress.InFlightCount != 0 ||
		progress.PendingCount != 0 || !progress.Completed {
		t.Fatalf("中断终局后的完成进度错误: progress=%+v err=%v", progress, err)
	}
	stored, err := s.SourcingScoreByRunID(runs[2].RunID)
	if err != nil || stored == nil || stored.FinishedAt == nil ||
		stored.Status != AIInvocationTransportFailed || stored.ErrorClass != "processInterrupted" || stored.Score != nil {
		t.Fatalf("中断评分未形成明确失败: invocation=%+v err=%v", stored, err)
	}
	if next, err := s.NextSourcingBatchRunWithoutScore("batch-score-progress"); err != nil || next != nil {
		t.Fatalf("中断评分被错误重新授权: next=%+v err=%v", next, err)
	}
}

func TestSourcingBatchScoringProgressRejectsPersistedProviderMix(t *testing.T) {
	s := openTest(t)
	base := time.Date(2026, 7, 22, 13, 0, 0, 0, time.UTC)
	key := AccountKey{Platform: "zhilian", AccountRef: "account-score-mixed"}
	runs := seedCompletedSourcingScoreBatch(
		t, s, "batch-score-mixed", key, "revision-score-mixed",
		[]string{"run-score-mixed-a", "run-score-mixed-b", "run-score-mixed-c"}, base,
	)
	first := sourcingScoreReservation(runs[0], "invocation-mixed-a", base.Add(2*time.Minute))
	if _, err := s.ReserveSourcingScore(first); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&SourcingScoreInvocation{
		InvocationID: "invocation-mixed-b", RunID: runs[1].RunID,
		ContextRevisionHash: runs[1].ContextRevisionHash, RunContentHash: runs[1].ContentHash,
		Provider: "other-provider", Model: "other-model", InputHash: "input-mixed-b",
		Status: AIInvocationTransportFailed, StartedAt: base.Add(3 * time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	third := sourcingScoreReservation(runs[2], "invocation-mixed-c", base.Add(4*time.Minute))
	if result, err := s.ReserveSourcingScore(third); result != nil || !errors.Is(err, ErrAIInvocationConflict) {
		t.Fatalf("已有混用事实时不得继续授权 provider: result=%+v err=%v", result, err)
	}
	if progress, err := s.SourcingBatchScoringProgress("batch-score-mixed"); progress != nil || !errors.Is(err, ErrAIInvocationConflict) {
		t.Fatalf("持久 provider/model 混用未响亮冲突: progress=%+v err=%v", progress, err)
	}
}

func TestCompleteSourcingScoreCASValidationAndIdempotency(t *testing.T) {
	s := openTest(t)
	key := AccountKey{Platform: "zhilian", AccountRef: "account-score-complete"}
	base := time.Date(2026, 7, 22, 14, 0, 0, 0, time.UTC)
	run := seedSourcingScoreRun(t, s, "run-score-complete", key, "revision-score-complete", base)
	reservation := sourcingScoreReservation(run, "score-invocation-complete", base.Add(time.Minute))
	if _, err := s.ReserveSourcingScore(reservation); err != nil {
		t.Fatal(err)
	}
	zero := 0
	completion := AIInvocationCompletion{
		InvocationID: reservation.InvocationID, Status: AIInvocationOK, OutputHash: "score-output-hash",
		InputTokens: 20, CachedInputTokens: 3, OutputTokens: 5, ReasoningTokens: &zero,
		UsageShape: AIInvocationUsageComplete, ReasoningContentEmpty: true,
		LatencyMs: 30, EstimatedCostMicros: 9, FinishedAt: base.Add(2 * time.Minute),
	}
	done, err := s.CompleteSourcingScore(CompleteSourcingScoreRequest{Completion: completion, Score: scorePointer(8)})
	if err != nil || done == nil || done.FinishedAt == nil || done.Score == nil || *done.Score != 8 || done.Status != AIInvocationOK {
		t.Fatalf("合法评分未完成: done=%+v err=%v", done, err)
	}
	replayed, err := s.CompleteSourcingScore(CompleteSourcingScoreRequest{Completion: completion, Score: scorePointer(8)})
	if err != nil || replayed == nil || replayed.Score == nil || *replayed.Score != 8 {
		t.Fatalf("相同完成未幂等收编: replayed=%+v err=%v", replayed, err)
	}
	if result, err := s.CompleteSourcingScore(CompleteSourcingScoreRequest{
		Completion: completion, Score: scorePointer(7),
	}); result != nil || !errors.Is(err, ErrAIInvocationConflict) {
		t.Fatalf("完成后的不同 score 未冲突: result=%+v err=%v", result, err)
	}

	unsafeRun := seedSourcingScoreRun(t, s, "run-score-unsafe", key, run.ContextRevisionHash, base.Add(time.Minute))
	unsafeReservation := sourcingScoreReservation(unsafeRun, "score-invocation-unsafe", base.Add(3*time.Minute))
	if _, err := s.ReserveSourcingScore(unsafeReservation); err != nil {
		t.Fatal(err)
	}
	one := 1
	unsafe := completion
	unsafe.InvocationID = unsafeReservation.InvocationID
	unsafe.ReasoningTokens = &one
	unsafe.FinishedAt = base.Add(4 * time.Minute)
	if result, err := s.CompleteSourcingScore(CompleteSourcingScoreRequest{
		Completion: unsafe, Score: scorePointer(6),
	}); result != nil || !errors.Is(err, ErrAIInvocationInvalid) {
		t.Fatalf("reasoning 不安全的成功评分未拒绝: result=%+v err=%v", result, err)
	}

	failedRun := seedSourcingScoreRun(t, s, "run-score-failed", key, run.ContextRevisionHash, base.Add(2*time.Minute))
	failedReservation := sourcingScoreReservation(failedRun, "score-invocation-failed", base.Add(5*time.Minute))
	if _, err := s.ReserveSourcingScore(failedReservation); err != nil {
		t.Fatal(err)
	}
	failedCompletion := AIInvocationCompletion{
		InvocationID: failedReservation.InvocationID, Status: AIInvocationTransportFailed,
		LatencyMs: 40, ErrorClass: "timeout", FinishedAt: base.Add(6 * time.Minute),
	}
	if result, err := s.CompleteSourcingScore(CompleteSourcingScoreRequest{
		Completion: failedCompletion, Score: scorePointer(1),
	}); result != nil || !errors.Is(err, ErrAIInvocationInvalid) {
		t.Fatalf("失败评分携带 score 未拒绝: result=%+v err=%v", result, err)
	}
	failed, err := s.CompleteSourcingScore(CompleteSourcingScoreRequest{Completion: failedCompletion})
	if err != nil || failed == nil || failed.Score != nil || failed.Status != AIInvocationTransportFailed || failed.FinishedAt == nil {
		t.Fatalf("失败评分未以 score=nil 终局: failed=%+v err=%v", failed, err)
	}
}

func TestPendingSourcingScoreWorkReturnsUnreservedAndInFlight(t *testing.T) {
	s := openTest(t)
	key := AccountKey{Platform: "zhilian", AccountRef: "account-score-pending-work"}
	base := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	runs := seedCompletedSourcingScoreBatch(
		t, s, "batch-score-pending-work", key, "revision-score-pending-work",
		[]string{"run-pw-a", "run-pw-b", "run-pw-c"}, base,
	)
	reserveB := sourcingScoreReservation(runs[1], "pw-invocation-b", base.Add(time.Minute))
	if result, err := s.ReserveSourcingScore(reserveB); err != nil || result == nil || !result.Created {
		t.Fatalf("inFlight 预留失败: result=%+v err=%v", result, err)
	}
	reserveC := sourcingScoreReservation(runs[2], "pw-invocation-c", base.Add(time.Minute))
	if result, err := s.ReserveSourcingScore(reserveC); err != nil || result == nil || !result.Created {
		t.Fatalf("终局预留失败: result=%+v err=%v", result, err)
	}
	if _, err := s.CompleteSourcingScore(CompleteSourcingScoreRequest{
		Completion: AIInvocationCompletion{
			InvocationID: "pw-invocation-c", Status: AIInvocationTransportFailed,
			ErrorClass: "transport", FinishedAt: base.Add(2 * time.Minute),
		},
	}); err != nil {
		t.Fatal(err)
	}

	items, err := s.PendingSourcingScoreWork("batch-score-pending-work")
	if err != nil || len(items) != 2 {
		t.Fatalf("待驱动成员集错误: items=%d err=%v", len(items), err)
	}
	if items[0].Run.RunID != "run-pw-a" || items[0].Invocation != nil {
		t.Fatalf("未预留成员形态错误: %+v", items[0])
	}
	if items[1].Run.RunID != "run-pw-b" || items[1].Invocation == nil ||
		items[1].Invocation.InvocationID != "pw-invocation-b" ||
		items[1].Invocation.FinishedAt != nil {
		t.Fatalf("inFlight 成员形态错误: %+v", items[1])
	}
}

func TestRecordSourcingScoreAttemptCountsAndRejectsFinished(t *testing.T) {
	s := openTest(t)
	key := AccountKey{Platform: "zhilian", AccountRef: "account-score-attempt"}
	base := time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC)
	run := seedSourcingScoreRun(t, s, "run-attempt-count", key, "revision-attempt-count", base)
	reservation := sourcingScoreReservation(run, "attempt-count-invocation", base.Add(time.Minute))
	if _, err := s.ReserveSourcingScore(reservation); err != nil {
		t.Fatal(err)
	}
	first, err := s.RecordSourcingScoreAttempt("attempt-count-invocation", true)
	if err != nil || first.AttemptCount != 1 || first.BudgetedAttemptCount != 1 {
		t.Fatalf("首次尝试计数错误: invocation=%+v err=%v", first, err)
	}
	second, err := s.RecordSourcingScoreAttempt("attempt-count-invocation", false)
	if err != nil || second.AttemptCount != 2 || second.BudgetedAttemptCount != 1 {
		t.Fatalf("非预算尝试计数错误: invocation=%+v err=%v", second, err)
	}
	if _, err := s.CompleteSourcingScore(CompleteSourcingScoreRequest{
		Completion: AIInvocationCompletion{
			InvocationID: "attempt-count-invocation", Status: AIInvocationTransportFailed,
			ErrorClass: "transport", FinishedAt: base.Add(2 * time.Minute),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordSourcingScoreAttempt("attempt-count-invocation", true); !errors.Is(err, ErrAIInvocationConflict) {
		t.Fatalf("终局行不得再登记尝试: err=%v", err)
	}
	if _, err := s.RecordSourcingScoreAttempt("attempt-count-missing", true); !errors.Is(err, ErrAIInvocationConflict) {
		t.Fatalf("缺失预留不得登记尝试: err=%v", err)
	}
}
