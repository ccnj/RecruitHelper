package store

import (
	"errors"
	"testing"
	"time"
)

func seedSourcingScoreRun(t *testing.T, s *Store, runID string, key AccountKey, revisionHash string, capturedAt time.Time) SourcingCandidateRun {
	t.Helper()
	run := SourcingCandidateRun{
		RunID: runID, Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revisionHash, PlatformUserRef: "user-" + runID,
		PositionRef: "position-" + runID, ContactState: "unestablished",
		SourceLogicalDispatchID: "logical-" + runID, ObservedAt: capturedAt.UnixMilli(),
		CapturedAt: capturedAt, SchemaVersion: 1, ContentHash: "content-" + runID,
		ResumeJSON: `{"basic":[],"expectations":[],"selfEvaluation":"","education":"","workExperiences":""}`,
	}
	if err := s.db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	return run
}

func sourcingScoreReservation(run SourcingCandidateRun, invocationID string, startedAt time.Time) ReserveSourcingScoreRequest {
	return ReserveSourcingScoreRequest{
		InvocationID: invocationID, RunID: run.RunID,
		ContextRevisionHash: run.ContextRevisionHash, RunContentHash: run.ContentHash,
		Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-" + run.RunID,
		StartedAt: startedAt,
	}
}

func scorePointer(value int) *int { return &value }

func TestNextSourcingRunWithoutScoreAndReservationAreAtMostOnce(t *testing.T) {
	s := openTest(t)
	key := AccountKey{Platform: "zhilian", AccountRef: "account-score-next"}
	revisionHash := "revision-score-next"
	base := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	first := seedSourcingScoreRun(t, s, "run-score-first", key, revisionHash, base)
	second := seedSourcingScoreRun(t, s, "run-score-second", key, revisionHash, base.Add(time.Minute))
	seedSourcingScoreRun(t, s, "run-other-revision", key, "revision-other", base.Add(-time.Hour))

	next, err := s.NextSourcingRunWithoutScore(key, revisionHash)
	if err != nil || next == nil || next.RunID != first.RunID {
		t.Fatalf("LEFT JOIN 未返回最早无 invocation run: next=%+v err=%v", next, err)
	}
	request := sourcingScoreReservation(first, "score-invocation-first", base.Add(2*time.Minute))
	reserved, err := s.ReserveSourcingScore(request)
	if err != nil || reserved == nil || !reserved.Created || reserved.Invocation.FinishedAt != nil ||
		reserved.Invocation.Status != AIInvocationTransportFailed {
		t.Fatalf("首次评分预留失败: result=%+v err=%v", reserved, err)
	}
	replay := request
	replay.InvocationID = "score-invocation-must-not-be-created"
	replayed, err := s.ReserveSourcingScore(replay)
	if err != nil || replayed == nil || replayed.Created || replayed.Invocation.InvocationID != request.InvocationID {
		t.Fatalf("同 RunID 重复预留未复用原事实: result=%+v err=%v", replayed, err)
	}
	var count int64
	if err := s.db.Model(&SourcingScoreInvocation{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("重复预留增生 invocation: count=%d err=%v", count, err)
	}
	next, err = s.NextSourcingRunWithoutScore(key, revisionHash)
	if err != nil || next == nil || next.RunID != second.RunID {
		t.Fatalf("已有任意 invocation 的 run 必须被 LEFT JOIN 排除: next=%+v err=%v", next, err)
	}
}

func TestReserveSourcingScoreRechecksFrozenRunMaterial(t *testing.T) {
	s := openTest(t)
	key := AccountKey{Platform: "zhilian", AccountRef: "account-score-binding"}
	run := seedSourcingScoreRun(t, s, "run-score-binding", key, "revision-score-binding", time.Now())
	request := sourcingScoreReservation(run, "score-invocation-binding", time.Now())
	wrongContent := request
	wrongContent.RunContentHash = "different-content"
	if result, err := s.ReserveSourcingScore(wrongContent); result != nil || !errors.Is(err, ErrSourcingBinding) {
		t.Fatalf("content hash 变化未在事务内拒绝: result=%+v err=%v", result, err)
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

func TestCompleteSourcingScoreCASValidationAndIdempotency(t *testing.T) {
	s := openTest(t)
	key := AccountKey{Platform: "zhilian", AccountRef: "account-score-complete"}
	base := time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC)
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
