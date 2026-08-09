package store

import (
	"errors"
	"testing"
	"time"

	"recruithelper/contract/gen/go/protocol"
)

// setSourcingBatchCaptureLimit 直接落库设置采集上限。正式路径由
// StartSourcingBatch 写入,这里只为把批次摆到分轮所需的初态。
func setSourcingBatchCaptureLimit(t *testing.T, s *Store, batchID string, limit int) {
	t.Helper()
	if err := s.db.Model(&SourcingBatch{}).
		Where("batch_id = ?", batchID).
		Update("capture_limit", limit).Error; err != nil {
		t.Fatal(err)
	}
}

// completeSourcingBatchAfterRound 模拟一轮采集采满:真实路径由
// CompleteSourcingBatchCandidateRun 在成员达标事务里收口,测试直接落库。
func completeSourcingBatchAfterRound(t *testing.T, s *Store, batchID string, endedAt time.Time) {
	t.Helper()
	if err := s.db.Model(&SourcingBatch{}).
		Where("batch_id = ?", batchID).
		Updates(map[string]any{
			"status":   SourcingBatchCompleted,
			"ended_at": endedAt,
		}).Error; err != nil {
		t.Fatal(err)
	}
}

// appendSelectionRoundMembers 往已有批次追加一轮采集到的成员。评分要等批次
// 重新转 completed 之后才能预约,与真实顺序一致,所以这里只建成员事实。
func appendSelectionRoundMembers(
	t *testing.T,
	s *Store,
	key AccountKey,
	batchID, revisionHash string,
	base time.Time,
	fixtures []selectionRunFixture,
) {
	t.Helper()
	var batch SourcingBatch
	if err := s.db.First(&batch, "batch_id = ?", batchID).Error; err != nil {
		t.Fatal(err)
	}
	for i, fixture := range fixtures {
		capturedAt := fixture.CapturedAt
		if capturedAt.IsZero() {
			capturedAt = base.Add(time.Duration(i) * time.Minute)
		}
		memberBatchID := batchID
		run := SourcingCandidateRun{
			RunID: fixture.RunID, BatchID: &memberBatchID,
			Platform: key.Platform, AccountRef: key.AccountRef,
			ContextRevisionHash: revisionHash, PlatformUserRef: "user-" + fixture.RunID,
			DisplayName: fixture.DisplayName, PositionRef: "position-" + batchID,
			ContactState:            string(protocol.CandidateContactStateUnestablished),
			SourceLogicalDispatchID: "logical-" + fixture.RunID,
			ObservedAt:              capturedAt.UnixMilli(), CapturedAt: capturedAt,
			SchemaVersion: 1, ContentHash: "content-" + fixture.RunID,
			ResumeJSON: `{"basic":[],"expectations":[],"selfEvaluation":"","education":"","workExperiences":""}`,
		}
		if err := s.db.Create(&run).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// scoreSelectionRoundMembers 给指定成员补上终局评分。必须在批次转 completed
// 之后调用:ReserveSourcingScore 只认已采满的批次。
func scoreSelectionRoundMembers(
	t *testing.T,
	s *Store,
	batchID string,
	base time.Time,
	fixtures []selectionRunFixture,
) {
	t.Helper()
	for i, fixture := range fixtures {
		var run SourcingCandidateRun
		if err := s.db.First(&run, "run_id = ?", fixture.RunID).Error; err != nil {
			t.Fatal(err)
		}
		reserveSelectionScore(t, s, batchID, run, fixture,
			base.Add(time.Duration(i)*time.Minute).Add(time.Second))
	}
}

// TestSelectCompletedSourcingBatchAcrossRoundsDecidesEachRunOnce 是分轮采集的
// 防重复回归:第二轮筛选只裁决新采到的人,已经落定的裁决与已经建立的档案原样
// 不动。防重复的硬约束是 SourcingSelectionDecision 的 RunID 主键,本用例证明
// 增量裁决没有绕开它。
func TestSelectCompletedSourcingBatchAcrossRoundsDecidesEachRunOnce(t *testing.T) {
	base := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	revisionHash := "rounds-dedup"
	// 选中目标固定为 3(targetMin=targetMax),男性上限不参与本用例。
	s, key := prepareSourcingSelectionStore(t, revisionHash, 5, 3, 3, 50, base)
	batchID := "batch-rounds-dedup"

	firstRound := []selectionRunFixture{
		{RunID: "r1", Score: intPointer(8)},
		{RunID: "r2", Score: intPointer(7)},
		{RunID: "r3", Score: intPointer(3)},
		{RunID: "r4", Score: intPointer(2)},
	}
	insertCompletedSelectionBatch(t, s, key, batchID, revisionHash, base, firstRound)
	setSourcingBatchCaptureLimit(t, s, batchID, 8)

	first, err := s.SelectCompletedSourcingBatch(batchID, base.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if first.SelectedCount != 2 || first.PoolCount != 4 {
		t.Fatalf("首轮筛选应选中 2 人、池 4 人,实际 selected=%d pool=%d",
			first.SelectedCount, first.PoolCount)
	}
	if first.TargetCount != 3 {
		t.Fatalf("选中目标应为 3,实际 %d", first.TargetCount)
	}
	firstDecisions := sourcingSelectionOutcomes(t, s, batchID)
	if len(firstDecisions) != 4 {
		t.Fatalf("首轮应产生 4 条裁决,实际 %d", len(firstDecisions))
	}

	// 选中 2 < 目标 3,退回采集再采一轮。
	reopened, err := s.ReopenSourcingBatchForCapture(ReopenSourcingBatchForCaptureRequest{
		BatchID: batchID, Step: 4, ReopenAt: base.Add(3 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Status != SourcingBatchCollecting || reopened.EndedAt != nil {
		t.Fatalf("回退后应回到采集态,实际 status=%s endedAt=%v",
			reopened.Status, reopened.EndedAt)
	}
	if reopened.TargetCount != 8 {
		t.Fatalf("采集额度应抬到 8,实际 %d", reopened.TargetCount)
	}

	secondRound := []selectionRunFixture{
		{RunID: "r5", Score: intPointer(9)},
		{RunID: "r6", Score: intPointer(6)},
		{RunID: "r7", Score: intPointer(4)},
		{RunID: "r8", Score: intPointer(1)},
	}
	appendSelectionRoundMembers(t, s, key, batchID, revisionHash, base.Add(4*time.Hour), secondRound)
	completeSourcingBatchAfterRound(t, s, batchID, base.Add(5*time.Hour))
	scoreSelectionRoundMembers(t, s, batchID, base.Add(4*time.Hour), secondRound)

	second, err := s.SelectCompletedSourcingBatch(batchID, base.Add(6*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if second.PoolCount != 8 {
		t.Fatalf("续裁后池应累加到 8,实际 %d", second.PoolCount)
	}
	// 目标 3:首轮已选 2,第二轮最高分 r5 补第 3 个,r6 虽然过线但配额已满。
	if second.SelectedCount != 3 {
		t.Fatalf("累计选中应为 3,实际 %d", second.SelectedCount)
	}

	secondDecisions := sourcingSelectionOutcomes(t, s, batchID)
	if len(secondDecisions) != 8 {
		t.Fatalf("两轮共应有 8 条裁决(每人恰好一条),实际 %d", len(secondDecisions))
	}
	for runID, before := range firstDecisions {
		after, ok := secondDecisions[runID]
		if !ok {
			t.Fatalf("首轮裁决 %s 在续裁后消失", runID)
		}
		if after.Outcome != before.Outcome || !after.DecidedAt.Equal(before.DecidedAt) {
			t.Fatalf("首轮裁决 %s 被重裁:outcome %s→%s, decidedAt %v→%v",
				runID, before.Outcome, after.Outcome, before.DecidedAt, after.DecidedAt)
		}
		if (before.ProfileID == nil) != (after.ProfileID == nil) {
			t.Fatalf("首轮裁决 %s 的建档状态被改写", runID)
		}
		if before.ProfileID != nil && *before.ProfileID != *after.ProfileID {
			t.Fatalf("首轮选中的 %s 被重复建档:%s→%s",
				runID, *before.ProfileID, *after.ProfileID)
		}
	}
	if got := secondDecisions["r5"].Outcome; got != SourcingSelectionSelected {
		t.Fatalf("r5 应补进名单,实际 %s", got)
	}
	if got := secondDecisions["r6"].Outcome; got != SourcingSelectionQuotaFull {
		t.Fatalf("r6 应因配额已满落选,实际 %s", got)
	}

	var profiles int64
	if err := s.db.Model(&CandidateProfile{}).Count(&profiles).Error; err != nil {
		t.Fatal(err)
	}
	if profiles != 3 {
		t.Fatalf("建档数应等于累计选中数 3,实际 %d", profiles)
	}

	// 同一轮内重放不产生任何新事实。
	replay, err := s.SelectCompletedSourcingBatch(batchID, base.Add(7*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if replay.PoolCount != 8 || replay.SelectedCount != 3 {
		t.Fatalf("重放应原样返回,实际 pool=%d selected=%d",
			replay.PoolCount, replay.SelectedCount)
	}
	if len(sourcingSelectionOutcomes(t, s, batchID)) != 8 {
		t.Fatal("重放不得新增裁决")
	}
}

// TestReopenSourcingBatchForCaptureRejectsIneligibleBatches 锁住回退入口的四道
// 拒绝:撞底、不分轮、额度用尽、成员数与额度对不上。它们各自防的是一种“再采
// 一轮没有意义或不安全”的局面。
func TestReopenSourcingBatchForCaptureRejectsIneligibleBatches(t *testing.T) {
	base := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)

	t.Run("撞底收口的批次不再回退", func(t *testing.T) {
		revisionHash := "rounds-settled"
		s, key := prepareSourcingSelectionStore(t, revisionHash, 5, 3, 3, 50, base)
		batchID := "batch-settled"
		insertCompletedSelectionBatch(t, s, key, batchID, revisionHash, base,
			[]selectionRunFixture{{RunID: "s1", Score: intPointer(8)}})
		setSourcingBatchCaptureLimit(t, s, batchID, 8)
		if err := s.db.Model(&SourcingBatch{}).Where("batch_id = ?", batchID).
			Update("reason", SourcingNoNewCandidatesReason+":target=4").Error; err != nil {
			t.Fatal(err)
		}
		_, err := s.ReopenSourcingBatchForCapture(ReopenSourcingBatchForCaptureRequest{
			BatchID: batchID, Step: 4, ReopenAt: base.Add(time.Hour),
		})
		if !errors.Is(err, ErrSourcingBatchStateConflict) {
			t.Fatalf("撞底批次应拒绝回退,实际 %v", err)
		}
	})

	t.Run("不分轮的批次不回退", func(t *testing.T) {
		revisionHash := "rounds-single"
		s, key := prepareSourcingSelectionStore(t, revisionHash, 5, 3, 3, 50, base)
		batchID := "batch-single"
		insertCompletedSelectionBatch(t, s, key, batchID, revisionHash, base,
			[]selectionRunFixture{{RunID: "g1", Score: intPointer(8)}})
		_, err := s.ReopenSourcingBatchForCapture(ReopenSourcingBatchForCaptureRequest{
			BatchID: batchID, Step: 4, ReopenAt: base.Add(time.Hour),
		})
		if !errors.Is(err, ErrSourcingBatchStateConflict) {
			t.Fatalf("CaptureLimit 为 0 的批次应拒绝回退,实际 %v", err)
		}
	})

	t.Run("额度已用尽的批次不回退", func(t *testing.T) {
		revisionHash := "rounds-exhausted"
		s, key := prepareSourcingSelectionStore(t, revisionHash, 5, 3, 3, 50, base)
		batchID := "batch-exhausted"
		insertCompletedSelectionBatch(t, s, key, batchID, revisionHash, base,
			[]selectionRunFixture{{RunID: "e1", Score: intPointer(8)}})
		setSourcingBatchCaptureLimit(t, s, batchID, 1)
		_, err := s.ReopenSourcingBatchForCapture(ReopenSourcingBatchForCaptureRequest{
			BatchID: batchID, Step: 4, ReopenAt: base.Add(time.Hour),
		})
		if !errors.Is(err, ErrSourcingBatchStateConflict) {
			t.Fatalf("额度用尽应拒绝回退,实际 %v", err)
		}
	})

	t.Run("成员数与额度对不上不回退", func(t *testing.T) {
		revisionHash := "rounds-mismatch"
		s, key := prepareSourcingSelectionStore(t, revisionHash, 5, 3, 3, 50, base)
		batchID := "batch-mismatch"
		insertCompletedSelectionBatch(t, s, key, batchID, revisionHash, base,
			[]selectionRunFixture{{RunID: "m1", Score: intPointer(8)}})
		setSourcingBatchCaptureLimit(t, s, batchID, 8)
		// 额度说 2 人,实到 1 人:那道“run 数精确等于 TargetCount”的不变式已破。
		if err := s.db.Model(&SourcingBatch{}).Where("batch_id = ?", batchID).
			Update("target_count", 2).Error; err != nil {
			t.Fatal(err)
		}
		_, err := s.ReopenSourcingBatchForCapture(ReopenSourcingBatchForCaptureRequest{
			BatchID: batchID, Step: 4, ReopenAt: base.Add(time.Hour),
		})
		if !errors.Is(err, ErrSourcingBatchConflict) {
			t.Fatalf("成员数对不上应拒绝回退,实际 %v", err)
		}
	})

	t.Run("已在采集态是幂等重放", func(t *testing.T) {
		revisionHash := "rounds-replay"
		s, key := prepareSourcingSelectionStore(t, revisionHash, 5, 3, 3, 50, base)
		batchID := "batch-replay"
		insertCompletedSelectionBatch(t, s, key, batchID, revisionHash, base,
			[]selectionRunFixture{{RunID: "p1", Score: intPointer(8)}})
		setSourcingBatchCaptureLimit(t, s, batchID, 8)
		first, err := s.ReopenSourcingBatchForCapture(ReopenSourcingBatchForCaptureRequest{
			BatchID: batchID, Step: 4, ReopenAt: base.Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		second, err := s.ReopenSourcingBatchForCapture(ReopenSourcingBatchForCaptureRequest{
			BatchID: batchID, Step: 4, ReopenAt: base.Add(2 * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		if second.TargetCount != first.TargetCount {
			t.Fatalf("重放不得再次抬档:%d→%d", first.TargetCount, second.TargetCount)
		}
	})
}
