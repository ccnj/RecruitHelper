package store

import (
	"errors"
	"testing"
	"time"
)

// 当日职位计划份额 override(AGENTS.md 2026-09-01)进筛选的三条性质:
// 份额生效且允许低于配置 TargetMin、份额固化进汇总行后重放不依赖计划表、
// 草稿计划(未定稿)走到筛选是响亮冲突而不是回落配置配额。

func insertDailyPlanForSelection(
	t *testing.T,
	s *Store,
	key AccountKey,
	planStatus, revisionHash string,
	quota int,
	at time.Time,
) DailyJobPlan {
	t.Helper()
	plan := DailyJobPlan{
		PlanID: "djp-sel-" + revisionHash, Platform: key.Platform, AccountRef: key.AccountRef,
		LocalDate: "2026-09-01", Status: planStatus,
		TotalQuota: 87, QuotaSourceJobID: "11", TargetMin: 80, TargetMax: 90,
		JobCount: 5, CreatedAt: at, UpdatedAt: at,
	}
	if err := s.db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	entry := DailyJobPlanEntry{
		EntryID: plan.PlanID + "-e01", PlanID: plan.PlanID, Seq: 1,
		BackendJobID: "11", JobName: "合成职位", RevisionHash: revisionHash,
		Quota: quota, Status: DailyJobPlanEntryPending,
		CreatedAt: at, UpdatedAt: at,
	}
	if err := s.db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestSelectCompletedSourcingBatchUsesDailyPlanQuotaBelowConfigMin(t *testing.T) {
	base := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	s, key := prepareSourcingSelectionStore(t, "plan-quota", 5, 80, 90, 50, base)
	plan := insertDailyPlanForSelection(t, s, key, DailyJobPlanActive, "plan-quota", 2, base)
	fixtures := []selectionRunFixture{
		{RunID: "run-a", Score: intPointer(9)},
		{RunID: "run-b", Score: intPointer(8)},
		{RunID: "run-c", Score: intPointer(7)},
		{RunID: "run-d", Score: intPointer(6)},
	}
	insertCompletedSelectionBatch(t, s, key, "batch-plan-quota", "plan-quota", base, fixtures)

	selection, err := s.SelectCompletedSourcingBatch("batch-plan-quota", base.Add(time.Hour))
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	// 份额 2 覆盖配置 [80,90];男性上限 = 2×50/100 = 1(小份额量化,甲方知情)。
	if selection.TargetCount != 2 || selection.PlanQuota == nil || *selection.PlanQuota != 2 ||
		selection.TargetMin != 80 || selection.TargetMax != 90 || selection.MaleLimit != 1 {
		t.Fatalf("份额 override 未生效: %+v", selection)
	}
	if selection.SelectedCount != 2 {
		t.Fatalf("选中人数应恰为份额: %+v", selection)
	}
	outcomes := sourcingSelectionOutcomes(t, s, selection.BatchID)
	if outcomes["run-c"].Outcome != SourcingSelectionQuotaFull ||
		outcomes["run-d"].Outcome != SourcingSelectionQuotaFull {
		t.Fatalf("超出份额者应 QuotaFull: %+v", outcomes)
	}

	// 计划终局后重放:汇总行 PlanQuota 自证,不得因 TargetCount<TargetMin 判非法。
	if err := s.CompleteDailyJobPlan(plan.PlanID, base.Add(2*time.Hour)); err != nil {
		t.Fatalf("complete plan: %v", err)
	}
	replayed, err := s.SelectCompletedSourcingBatch("batch-plan-quota", base.Add(3*time.Hour))
	if err != nil {
		t.Fatalf("计划终局后的筛选重放失败: %v", err)
	}
	if replayed.TargetCount != 2 || replayed.SelectedCount != 2 || replayed.PlanQuota == nil {
		t.Fatalf("重放摘要漂移: %+v", replayed)
	}
}

func TestSelectCompletedSourcingBatchRejectsDraftPlanLoudly(t *testing.T) {
	base := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	s, key := prepareSourcingSelectionStore(t, "plan-draft", 5, 80, 90, 50, base)
	insertDailyPlanForSelection(t, s, key, DailyJobPlanDraft, "plan-draft", 0, base)
	insertCompletedSelectionBatch(t, s, key, "batch-plan-draft", "plan-draft", base,
		[]selectionRunFixture{{RunID: "run-x", Score: intPointer(9)}})

	// 定稿挂在批前状态闸上,采集通过即已定稿;草稿计划走到筛选只可能是状态被
	// 越过,必须响亮冲突,绝不回落配置配额(回落方向是多发)。
	if _, err := s.SelectCompletedSourcingBatch("batch-plan-draft", base.Add(time.Hour)); !errors.Is(err, ErrSourcingSelectionConflict) {
		t.Fatalf("草稿计划应冲突: %v", err)
	}
}

// done 条目的份额已被自己的批次消费完;同 revision 的第二个批次(管理面显式
// 启动)走到筛选必须冲突,不得再次授予份额(否则单职位 2×份额、当日超总量)。
func TestSelectCompletedSourcingBatchRejectsDoneEntryReuse(t *testing.T) {
	base := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	s, key := prepareSourcingSelectionStore(t, "plan-done", 5, 80, 90, 50, base)
	plan := insertDailyPlanForSelection(t, s, key, DailyJobPlanActive, "plan-done", 2, base)
	if err := s.db.Model(&DailyJobPlanEntry{}).
		Where("plan_id = ?", plan.PlanID).
		Update("status", DailyJobPlanEntryDone).Error; err != nil {
		t.Fatal(err)
	}
	insertCompletedSelectionBatch(t, s, key, "batch-plan-done", "plan-done", base,
		[]selectionRunFixture{{RunID: "run-y", Score: intPointer(9)}})

	if _, err := s.SelectCompletedSourcingBatch("batch-plan-done", base.Add(time.Hour)); !errors.Is(err, ErrSourcingSelectionConflict) {
		t.Fatalf("done 条目复用应冲突: %v", err)
	}
}

// 首批上限按草稿临时份额落库,定稿份额变大后续采必须能把上限抬到 3×定稿份额,
// 否则该职位当日结构性采不满(审查修复回归)。
func TestReopenSourcingBatchRaisesCaptureLimitToPlanLimit(t *testing.T) {
	base := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	s, key := prepareSourcingSelectionStore(t, "plan-cap", 5, 80, 90, 50, base)
	insertCompletedSelectionBatch(t, s, key, "batch-plan-cap", "plan-cap", base,
		[]selectionRunFixture{
			{RunID: "run-cap-a", Score: intPointer(9)},
			{RunID: "run-cap-b", Score: intPointer(8)},
		})
	// 模拟草稿期落库的紧上限:target 2 == captureLimit 2。
	if err := s.db.Model(&SourcingBatch{}).
		Where("batch_id = ?", "batch-plan-cap").
		Update("capture_limit", 2).Error; err != nil {
		t.Fatal(err)
	}
	// 不带 Limit:顶到上限,续采被拒。
	if _, err := s.ReopenSourcingBatchForCapture(ReopenSourcingBatchForCaptureRequest{
		BatchID: "batch-plan-cap", Step: 1, ReopenAt: base.Add(time.Hour),
	}); !errors.Is(err, ErrSourcingBatchStateConflict) {
		t.Fatalf("顶上限不带抬升应冲突: %v", err)
	}
	// 带定稿计划上限:上限抬到 6,目标 2+1=3,回到采集态。
	reopened, err := s.ReopenSourcingBatchForCapture(ReopenSourcingBatchForCaptureRequest{
		BatchID: "batch-plan-cap", Step: 1, Limit: 6, ReopenAt: base.Add(time.Hour),
	})
	if err != nil || reopened.Status != SourcingBatchCollecting ||
		reopened.TargetCount != 3 || reopened.CaptureLimit != 6 {
		t.Fatalf("上限未抬升: %+v err=%v", reopened, err)
	}
	// 只升不降:更小的 Limit 不缩上限。
	if err := s.db.Model(&SourcingBatch{}).
		Where("batch_id = ?", "batch-plan-cap").
		Updates(map[string]any{"status": SourcingBatchCompleted, "ended_at": base.Add(2 * time.Hour), "target_count": 2}).Error; err != nil {
		t.Fatal(err)
	}
	shrunk, err := s.ReopenSourcingBatchForCapture(ReopenSourcingBatchForCaptureRequest{
		BatchID: "batch-plan-cap", Step: 1, Limit: 3, ReopenAt: base.Add(3 * time.Hour),
	})
	if err != nil || shrunk.CaptureLimit != 6 || shrunk.TargetCount != 3 {
		t.Fatalf("Limit 只升不降被违反: %+v err=%v", shrunk, err)
	}
}
