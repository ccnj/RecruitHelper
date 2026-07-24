package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/contract/gen/go/protocol"
)

func prepareSourcingGreetingBatch(
	t *testing.T,
	batchID, revisionHash string,
	targetCount int,
	base time.Time,
	fixtures []selectionRunFixture,
) (*Store, []SourcingCandidateRun, map[string]SourcingSelectionDecision) {
	t.Helper()
	s, key := prepareSourcingSelectionStore(t, revisionHash, 5, targetCount, targetCount, 100, base)
	runs := insertCompletedSelectionBatch(t, s, key, batchID, revisionHash, base, fixtures)
	positionRef := "position-" + batchID
	boundAt := base.Add(-time.Second)
	if err := s.db.Model(&SourcingBatch{}).Where("batch_id = ?", batchID).Updates(map[string]any{
		"position_ref": positionRef, "position_bound_at": boundAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectCompletedSourcingBatch(batchID, base.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	return s, runs, sourcingSelectionOutcomes(t, s, batchID)
}

func greetingReservation(
	batchID string,
	run SourcingCandidateRun,
	decision SourcingSelectionDecision,
	invocationID string,
	startedAt time.Time,
) ReserveSourcingGreetingRequest {
	profileID := ""
	if decision.ProfileID != nil {
		profileID = *decision.ProfileID
	}
	return ReserveSourcingGreetingRequest{
		InvocationID: invocationID, BatchID: batchID, RunID: run.RunID, ProfileID: profileID,
		ContextRevisionHash: run.ContextRevisionHash, RunContentHash: run.ContentHash,
		Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "greeting-input-" + run.RunID,
		StartedAt: startedAt,
	}
}

func runByID(t *testing.T, runs []SourcingCandidateRun, runID string) SourcingCandidateRun {
	t.Helper()
	for _, run := range runs {
		if run.RunID == runID {
			return run
		}
	}
	t.Fatalf("run 不存在: %s", runID)
	return SourcingCandidateRun{}
}

func TestSourcingGreetingRequiresCompleteSelectionAndExactSelectedBindings(t *testing.T) {
	base := time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC)

	t.Run("尚未筛选", func(t *testing.T) {
		s, key := prepareSourcingSelectionStore(t, "greeting-not-selected", 5, 1, 1, 100, base)
		fixtures := []selectionRunFixture{{RunID: "run-not-selected", Score: intPointer(9)}}
		insertCompletedSelectionBatch(t, s, key, "batch-not-selected", "greeting-not-selected", base, fixtures)
		positionRef := "position-batch-not-selected"
		if err := s.db.Model(&SourcingBatch{}).Where("batch_id = ?", "batch-not-selected").
			Update("position_ref", positionRef).Error; err != nil {
			t.Fatal(err)
		}
		if next, err := s.NextSelectedSourcingGreetingMaterial(
			"batch-not-selected", "greeting-not-selected",
		); next != nil ||
			!errors.Is(err, ErrSourcingSelectionNotReady) {
			t.Fatalf("未筛选批次进入招呼生成: next=%+v err=%v", next, err)
		}
		if progress, err := s.SourcingBatchGreetingProgress("batch-not-selected"); progress != nil ||
			!errors.Is(err, ErrSourcingSelectionNotReady) {
			t.Fatalf("未筛选批次伪造招呼进度: progress=%+v err=%v", progress, err)
		}
	})

	t.Run("筛选摘要或决策不完整", func(t *testing.T) {
		s, _, decisions := prepareSourcingGreetingBatch(t, "batch-incomplete-selection", "greeting-incomplete-selection", 1, base,
			[]selectionRunFixture{{RunID: "run-incomplete-selection", Score: intPointer(9)}})
		decision := decisions["run-incomplete-selection"]
		if err := s.db.Model(&SourcingSelectionDecision{}).Where("run_id = ?", decision.RunID).
			Update("context_revision_hash", "wrong-revision").Error; err != nil {
			t.Fatal(err)
		}
		if next, err := s.NextSelectedSourcingGreetingMaterial(
			"batch-incomplete-selection", "greeting-incomplete-selection",
		); next != nil ||
			!errors.Is(err, ErrSourcingSelectionConflict) {
			t.Fatalf("错 revision 决策未阻断: next=%+v err=%v", next, err)
		}
	})

	t.Run("档案不再selected", func(t *testing.T) {
		s, runs, decisions := prepareSourcingGreetingBatch(t, "batch-profile-state", "greeting-profile-state", 1, base,
			[]selectionRunFixture{{RunID: "run-profile-state", Score: intPointer(9)}})
		profileID := *decisions["run-profile-state"].ProfileID
		if err := s.db.Model(&CandidateProfile{}).Where("profile_id = ?", profileID).
			Update("main_status", CandidateProfileGreeted).Error; err != nil {
			t.Fatal(err)
		}
		if next, err := s.NextSelectedSourcingGreetingMaterial(
			"batch-profile-state", "greeting-profile-state",
		); next != nil ||
			!errors.Is(err, ErrSourcingBinding) {
			t.Fatalf("非 selected 档案未阻断: next=%+v err=%v", next, err)
		}
		reservation := greetingReservation(
			"batch-profile-state", runs[0], decisions[runs[0].RunID],
			"greeting-profile-state", base.Add(time.Hour),
		)
		if result, err := s.ReserveSourcingGreeting(reservation); result != nil ||
			!errors.Is(err, ErrSourcingBinding) {
			t.Fatalf("非 selected 档案获得最终调用预留: result=%+v err=%v", result, err)
		}
		var invocations int64
		if err := s.db.Model(&SourcingGreetingInvocation{}).Count(&invocations).Error; err != nil || invocations != 0 {
			t.Fatalf("拒绝预留后产生调用事实: count=%d err=%v", invocations, err)
		}
	})
}

func TestNextSelectedSourcingGreetingMaterialExcludesNonSelectedAndUsesCaptureOrder(t *testing.T) {
	base := time.Date(2026, 7, 22, 19, 0, 0, 0, time.UTC)
	fixtures := []selectionRunFixture{
		{RunID: "run-selected-late", Score: intPointer(10), CapturedAt: base.Add(3 * time.Minute)},
		{RunID: "run-selected-early", Score: intPointer(9), CapturedAt: base.Add(time.Minute)},
		{RunID: "run-quota-full", Score: intPointer(8), CapturedAt: base},
	}
	s, runs, decisions := prepareSourcingGreetingBatch(t, "batch-greeting-next", "greeting-next", 2, base, fixtures)

	next, err := s.NextSelectedSourcingGreetingMaterial("batch-greeting-next", "greeting-next")
	if err != nil || next == nil || next.RunID != "run-selected-early" ||
		next.ProfileID != *decisions["run-selected-early"].ProfileID || next.ResumeJSON == "" {
		t.Fatalf("未按 selected 的采集顺序返回材料: next=%+v err=%v", next, err)
	}
	quotaRun := runByID(t, runs, "run-quota-full")
	quotaRequest := greetingReservation("batch-greeting-next", quotaRun, decisions[quotaRun.RunID], "greeting-quota", base)
	quotaRequest.ProfileID = *decisions["run-selected-early"].ProfileID
	if result, err := s.ReserveSourcingGreeting(quotaRequest); result != nil || !errors.Is(err, ErrSourcingBinding) {
		t.Fatalf("非 selected run 获得调用预留: result=%+v err=%v", result, err)
	}
}

func TestGreetingStageKeepsFirstInvocationRevisionAfterHeadAdvances(t *testing.T) {
	base := time.Date(2026, 7, 24, 14, 0, 0, 0, time.UTC)
	fixtures := []selectionRunFixture{
		{RunID: "run-greeting-stage-a", Score: intPointer(10)},
		{RunID: "run-greeting-stage-b", Score: intPointer(9)},
	}
	s, runs, decisions := prepareSourcingGreetingBatch(
		t, "batch-greeting-stage", "greeting-stage-capture", 2, base, fixtures,
	)
	greetingRevision := sourcingSelectionRevision(
		base.Add(time.Hour), "greeting-stage-current", 5, 2, 2, 100,
	)
	if _, err := s.SaveCurrentLegacyJobAIContext(
		[]m5ai.ContextRevision{greetingRevision}, base.Add(time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	resolved, err := s.SourcingGreetingRevision("batch-greeting-stage")
	if err != nil || resolved == nil || resolved.RevisionHash != greetingRevision.RevisionHash {
		t.Fatalf("招呼阶段未取 current head: revision=%+v err=%v", resolved, err)
	}
	firstRun := runByID(t, runs, "run-greeting-stage-a")
	first := greetingReservation(
		"batch-greeting-stage", firstRun, decisions[firstRun.RunID],
		"greeting-stage-first", base.Add(2*time.Hour),
	)
	first.ContextRevisionHash = greetingRevision.RevisionHash
	if result, err := s.ReserveSourcingGreeting(first); err != nil || result == nil || !result.Created {
		t.Fatalf("首条招呼预留失败: result=%+v err=%v", result, err)
	}

	newer := sourcingSelectionRevision(base.Add(3*time.Hour), "greeting-stage-newer", 5, 2, 2, 100)
	if _, err := s.SaveCurrentLegacyJobAIContext(
		[]m5ai.ContextRevision{newer}, base.Add(3*time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	resolved, err = s.SourcingGreetingRevision("batch-greeting-stage")
	if err != nil || resolved == nil || resolved.RevisionHash != greetingRevision.RevisionHash {
		t.Fatalf("head 推进后招呼阶段换版: revision=%+v err=%v", resolved, err)
	}
	secondRun := runByID(t, runs, "run-greeting-stage-b")
	second := greetingReservation(
		"batch-greeting-stage", secondRun, decisions[secondRun.RunID],
		"greeting-stage-second", base.Add(4*time.Hour),
	)
	second.ContextRevisionHash = greetingRevision.RevisionHash
	if result, err := s.ReserveSourcingGreeting(second); err != nil || result == nil || !result.Created {
		t.Fatalf("余下招呼未沿用首条 revision: result=%+v err=%v", result, err)
	}
}

func TestReserveSourcingGreetingChecksCrossBatchMaterialUniquenessAndProviderFreeze(t *testing.T) {
	base := time.Date(2026, 7, 22, 20, 0, 0, 0, time.UTC)
	fixtures := []selectionRunFixture{
		{RunID: "run-reserve-a", Score: intPointer(10)},
		{RunID: "run-reserve-b", Score: intPointer(9)},
	}
	s, runs, decisions := prepareSourcingGreetingBatch(t, "batch-greeting-reserve", "greeting-reserve", 2, base, fixtures)
	firstRun := runByID(t, runs, "run-reserve-a")
	secondRun := runByID(t, runs, "run-reserve-b")
	first := greetingReservation("batch-greeting-reserve", firstRun, decisions[firstRun.RunID], "greeting-invocation-a", base.Add(time.Hour))

	otherFixtures := []selectionRunFixture{{RunID: "run-reserve-other-batch", Score: intPointer(10)}}
	otherRuns := insertCompletedSelectionBatch(t, s,
		AccountKey{Platform: "zhilian", AccountRef: "account-greeting-reserve"},
		"batch-greeting-reserve-other", "greeting-reserve", base.Add(10*time.Minute), otherFixtures,
	)
	otherPositionRef := "position-batch-greeting-reserve-other"
	if err := s.db.Model(&SourcingBatch{}).Where("batch_id = ?", "batch-greeting-reserve-other").
		Update("position_ref", otherPositionRef).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectCompletedSourcingBatch("batch-greeting-reserve-other", base.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(otherRuns) != 1 {
		t.Fatal("另一个批次 fixture 未建立")
	}
	wrongBatch := first
	wrongBatch.BatchID = "batch-greeting-reserve-other"
	if result, err := s.ReserveSourcingGreeting(wrongBatch); result != nil || !errors.Is(err, ErrSourcingBinding) {
		t.Fatalf("跨批材料未拒绝: result=%+v err=%v", result, err)
	}
	wrongContent := first
	wrongContent.RunContentHash = "wrong-content"
	if result, err := s.ReserveSourcingGreeting(wrongContent); result != nil || !errors.Is(err, ErrSourcingBinding) {
		t.Fatalf("错 content hash 未拒绝: result=%+v err=%v", result, err)
	}
	wrongProfile := first
	wrongProfile.ProfileID = *decisions[secondRun.RunID].ProfileID
	if result, err := s.ReserveSourcingGreeting(wrongProfile); result != nil || !errors.Is(err, ErrSourcingBinding) {
		t.Fatalf("run/profile 错绑未拒绝: result=%+v err=%v", result, err)
	}

	reserved, err := s.ReserveSourcingGreeting(first)
	if err != nil || reserved == nil || !reserved.Created || reserved.Invocation.BatchID != first.BatchID ||
		reserved.Invocation.FinishedAt != nil {
		t.Fatalf("首次预留失败: result=%+v err=%v", reserved, err)
	}
	duplicateRun := reserved.Invocation
	duplicateRun.InvocationID = "duplicate-run-invocation"
	duplicateRun.ProfileID = "different-profile"
	if err := s.db.Create(&duplicateRun).Error; err == nil {
		t.Fatal("RunID 数据库唯一约束未生效")
	}
	duplicateProfile := reserved.Invocation
	duplicateProfile.InvocationID = "duplicate-profile-invocation"
	duplicateProfile.RunID = "different-run"
	if err := s.db.Create(&duplicateProfile).Error; err == nil {
		t.Fatal("ProfileID 数据库唯一约束未生效")
	}
	replay := first
	replay.InvocationID = "must-not-create-second-invocation"
	replayed, err := s.ReserveSourcingGreeting(replay)
	if err != nil || replayed == nil || replayed.Created || replayed.Invocation.InvocationID != first.InvocationID {
		t.Fatalf("同 run/profile 未重放原预留: result=%+v err=%v", replayed, err)
	}

	second := greetingReservation("batch-greeting-reserve", secondRun, decisions[secondRun.RunID], "greeting-invocation-b", base.Add(time.Hour+time.Minute))
	mixed := second
	mixed.Model = "other-model"
	if result, err := s.ReserveSourcingGreeting(mixed); result != nil || !errors.Is(err, ErrAIInvocationConflict) {
		t.Fatalf("批内 provider/model 混用未拒绝: result=%+v err=%v", result, err)
	}
	var count int64
	if err := s.db.Model(&SourcingGreetingInvocation{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("失败预留未回滚: count=%d err=%v", count, err)
	}
	if _, err := s.ReserveSourcingGreeting(second); err != nil {
		t.Fatal(err)
	}
	if next, err := s.NextSelectedSourcingGreetingMaterial(
		first.BatchID, first.ContextRevisionHash,
	); err != nil || next != nil {
		t.Fatalf("已有预留的 selected 被再次授权: next=%+v err=%v", next, err)
	}
}

func TestCompleteSourcingGreetingPersistsTextOnlyForSuccessfulCASAndReplays(t *testing.T) {
	base := time.Date(2026, 7, 22, 21, 0, 0, 0, time.UTC)
	fixtures := []selectionRunFixture{
		{RunID: "run-complete-ok", Score: intPointer(10)},
		{RunID: "run-complete-failed", Score: intPointer(9)},
	}
	s, runs, decisions := prepareSourcingGreetingBatch(t, "batch-greeting-complete", "greeting-complete", 2, base, fixtures)
	okRun := runByID(t, runs, "run-complete-ok")
	failedRun := runByID(t, runs, "run-complete-failed")
	okReservation := greetingReservation("batch-greeting-complete", okRun, decisions[okRun.RunID], "greeting-complete-ok", base.Add(time.Hour))
	failedReservation := greetingReservation("batch-greeting-complete", failedRun, decisions[failedRun.RunID], "greeting-complete-failed", base.Add(time.Hour+time.Minute))
	if _, err := s.ReserveSourcingGreeting(okReservation); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveSourcingGreeting(failedReservation); err != nil {
		t.Fatal(err)
	}

	zero := 0
	text := "您好，看到您的经历与岗位很匹配，想和您聊聊。"
	okCompletion := AIInvocationCompletion{
		InvocationID: okReservation.InvocationID, Status: AIInvocationOK,
		OutputHash: "provider-output-ok", InputTokens: 100, CachedInputTokens: 20, OutputTokens: 18,
		ReasoningTokens: &zero, UsageShape: AIInvocationUsageComplete, ReasoningContentEmpty: true,
		LatencyMs: 120, EstimatedCostMicros: 30, FinishedAt: base.Add(2 * time.Hour),
	}
	badHashRequest := CompleteSourcingGreetingRequest{
		Completion: okCompletion, GreetingText: text, ContentHash: strings.Repeat("0", 64),
	}
	if result, err := s.CompleteSourcingGreeting(badHashRequest); result != nil || !errors.Is(err, ErrAIInvocationInvalid) {
		t.Fatalf("调用方伪造正文 hash 未拒绝: result=%+v err=%v", result, err)
	}
	stored, err := s.SourcingGreetingByProfileID(okReservation.ProfileID)
	if err != nil || stored == nil || stored.FinishedAt != nil || stored.GreetingText != "" || stored.ContentHash != "" {
		t.Fatalf("非法完成污染预留: stored=%+v err=%v", stored, err)
	}

	okRequest := CompleteSourcingGreetingRequest{
		Completion: okCompletion, GreetingText: text, ContentHash: sourcingGreetingContentHash(text),
	}
	done, err := s.CompleteSourcingGreeting(okRequest)
	if err != nil || done == nil || done.Status != AIInvocationOK || done.GreetingText != text ||
		done.ContentHash != sourcingGreetingContentHash(text) || done.FinishedAt == nil {
		t.Fatalf("成功正文未持久化: done=%+v err=%v", done, err)
	}
	replayed, err := s.CompleteSourcingGreeting(okRequest)
	if err != nil || replayed == nil || replayed.GreetingText != text {
		t.Fatalf("同一成功完成未幂等重放: replayed=%+v err=%v", replayed, err)
	}
	otherText := "另一条合法正文"
	conflicting := okRequest
	conflicting.GreetingText = otherText
	conflicting.ContentHash = sourcingGreetingContentHash(otherText)
	if result, err := s.CompleteSourcingGreeting(conflicting); result != nil || !errors.Is(err, ErrAIInvocationConflict) {
		t.Fatalf("不同完成覆盖正文: result=%+v err=%v", result, err)
	}

	failedCompletion := AIInvocationCompletion{
		InvocationID: failedReservation.InvocationID, Status: AIInvocationProviderRejected,
		OutputHash: "provider-output-failed", ErrorClass: "providerRejected",
		LatencyMs: 80, FinishedAt: base.Add(2*time.Hour + time.Minute),
	}
	if result, err := s.CompleteSourcingGreeting(CompleteSourcingGreetingRequest{
		Completion: failedCompletion, GreetingText: "不应保存", ContentHash: sourcingGreetingContentHash("不应保存"),
	}); result != nil || !errors.Is(err, ErrAIInvocationInvalid) {
		t.Fatalf("失败终局携带正文未拒绝: result=%+v err=%v", result, err)
	}
	failed, err := s.CompleteSourcingGreeting(CompleteSourcingGreetingRequest{Completion: failedCompletion})
	if err != nil || failed == nil || failed.Status != AIInvocationProviderRejected || failed.FinishedAt == nil ||
		failed.GreetingText != "" || failed.ContentHash != "" {
		t.Fatalf("失败终局保存了正文或未收敛: failed=%+v err=%v", failed, err)
	}

	progress, err := s.SourcingBatchGreetingProgress("batch-greeting-complete")
	if err != nil || progress.SelectedCount != 2 || progress.OKCount != 1 || progress.FailedCount != 1 ||
		progress.InFlightCount != 0 || progress.PendingCount != 0 || !progress.Completed ||
		progress.Provider != okReservation.Provider || progress.Model != okReservation.Model ||
		progress.InputTokens != 100 || progress.CachedInputTokens != 20 || progress.OutputTokens != 18 ||
		progress.EstimatedCostMicros != 30 {
		t.Fatalf("终局聚合错误: progress=%+v err=%v", progress, err)
	}
}

func TestRecoverInterruptedSourcingGreetingIsTerminalAndNeverReauthorizes(t *testing.T) {
	base := time.Date(2026, 7, 22, 22, 0, 0, 0, time.UTC)
	s, runs, decisions := prepareSourcingGreetingBatch(t, "batch-greeting-recovery", "greeting-recovery", 1, base,
		[]selectionRunFixture{{RunID: "run-greeting-recovery", Score: intPointer(9)}})
	run := runs[0]
	reservation := greetingReservation("batch-greeting-recovery", run, decisions[run.RunID], "greeting-recovery", base.Add(time.Hour))
	if _, err := s.ReserveSourcingGreeting(reservation); err != nil {
		t.Fatal(err)
	}

	recoveredAt := base.Add(2 * time.Hour)
	recovered, err := s.RecoverInterruptedAIInvocations(recoveredAt)
	if err != nil || recovered != 1 {
		t.Fatalf("中断招呼调用未收敛: recovered=%d err=%v", recovered, err)
	}
	stored, err := s.SourcingGreetingByProfileID(reservation.ProfileID)
	if err != nil || stored == nil || stored.Status != AIInvocationTransportFailed ||
		stored.ErrorClass != "processInterrupted" || stored.FinishedAt == nil ||
		!stored.FinishedAt.Equal(recoveredAt) || stored.GreetingText != "" || stored.ContentHash != "" {
		t.Fatalf("中断终局事实错误: stored=%+v err=%v", stored, err)
	}
	if next, err := s.NextSelectedSourcingGreetingMaterial(
		reservation.BatchID, reservation.ContextRevisionHash,
	); err != nil || next != nil {
		t.Fatalf("中断调用被重新授权: next=%+v err=%v", next, err)
	}
	replay := reservation
	replay.InvocationID = "must-not-recover-retry"
	result, err := s.ReserveSourcingGreeting(replay)
	if err != nil || result == nil || result.Created || result.Invocation.InvocationID != reservation.InvocationID {
		t.Fatalf("恢复后未只读重放原终局: result=%+v err=%v", result, err)
	}
	progress, err := s.SourcingBatchGreetingProgress(reservation.BatchID)
	if err != nil || progress.FailedCount != 1 || !progress.Completed {
		t.Fatalf("恢复终局未进入完成聚合: progress=%+v err=%v", progress, err)
	}
}

func TestSourcingGreetingZeroSelectedCompletesWithoutInvocationOrSendFacts(t *testing.T) {
	base := time.Date(2026, 7, 22, 23, 0, 0, 0, time.UTC)
	s, key := prepareSourcingSelectionStore(t, "greeting-zero-selected", 10, 1, 1, 100, base)
	fixtures := []selectionRunFixture{{RunID: "run-zero-selected", Score: intPointer(5)}}
	insertCompletedSelectionBatch(t, s, key, "batch-zero-selected", "greeting-zero-selected", base, fixtures)
	positionRef := "position-batch-zero-selected"
	if err := s.db.Model(&SourcingBatch{}).Where("batch_id = ?", "batch-zero-selected").
		Update("position_ref", positionRef).Error; err != nil {
		t.Fatal(err)
	}
	selection, err := s.SelectCompletedSourcingBatch("batch-zero-selected", base.Add(time.Hour))
	if err != nil || selection.SelectedCount != 0 {
		t.Fatalf("零 selected 筛选失败: selection=%+v err=%v", selection, err)
	}
	progress, err := s.SourcingBatchGreetingProgress(selection.BatchID)
	if err != nil || progress.SelectedCount != 0 || !progress.Completed || progress.Provider != "" || progress.Model != "" {
		t.Fatalf("零 selected 未空完成: progress=%+v err=%v", progress, err)
	}
	if next, err := s.NextSelectedSourcingGreetingMaterial(
		selection.BatchID, selection.ContextRevisionHash,
	); err != nil || next != nil {
		t.Fatalf("零 selected 返回了材料: next=%+v err=%v", next, err)
	}
	for _, model := range []any{&SourcingGreetingInvocation{}, &EffectIntent{}, &CandidateGreetingHead{}, &CmdRecord{}} {
		var count int64
		if err := s.db.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("招呼生成阶段产生发送事实 %T: count=%d err=%v", model, count, err)
		}
	}
}

func TestCompleteSourcingGreetingRejectsUnsafeOrUnnormalizedSuccess(t *testing.T) {
	base := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	s, runs, decisions := prepareSourcingGreetingBatch(t, "batch-greeting-validation", "greeting-validation", 1, base,
		[]selectionRunFixture{{RunID: "run-greeting-validation", Score: intPointer(9), Basic: []protocol.CandidateResumeLabelValue{{Label: "求职状态", Value: "在职"}}}})
	run := runs[0]
	reservation := greetingReservation("batch-greeting-validation", run, decisions[run.RunID], "greeting-validation", base.Add(time.Hour))
	if _, err := s.ReserveSourcingGreeting(reservation); err != nil {
		t.Fatal(err)
	}
	one := 1
	completion := AIInvocationCompletion{
		InvocationID: reservation.InvocationID, Status: AIInvocationOK, OutputHash: "output-validation",
		ReasoningTokens: &one, UsageShape: AIInvocationUsageComplete, ReasoningContentEmpty: true,
		FinishedAt: base.Add(2 * time.Hour),
	}
	text := "合法正文"
	if result, err := s.CompleteSourcingGreeting(CompleteSourcingGreetingRequest{
		Completion: completion, GreetingText: text, ContentHash: sourcingGreetingContentHash(text),
	}); result != nil || !errors.Is(err, ErrAIInvocationInvalid) {
		t.Fatalf("正 reasoning token 未阻断: result=%+v err=%v", result, err)
	}
	zero := 0
	completion.ReasoningTokens = &zero
	for _, invalidText := range []string{" 前后空白", strings.Repeat("中", 683)} {
		if result, err := s.CompleteSourcingGreeting(CompleteSourcingGreetingRequest{
			Completion: completion, GreetingText: invalidText, ContentHash: sourcingGreetingContentHash(invalidText),
		}); result != nil || !errors.Is(err, ErrAIInvocationInvalid) {
			t.Fatalf("非法正文未阻断 bytes=%d: result=%+v err=%v", len([]byte(invalidText)), result, err)
		}
	}
	stored, err := s.SourcingGreetingByProfileID(reservation.ProfileID)
	if err != nil || stored == nil || stored.FinishedAt != nil {
		t.Fatalf("非法完成后预留不再可完成: stored=%+v err=%v", stored, err)
	}
}
