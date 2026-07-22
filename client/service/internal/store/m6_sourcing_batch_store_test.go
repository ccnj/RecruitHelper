package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"recruithelper/contract/gen/go/protocol"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func seedSourcingBatchDependencies(t *testing.T, s *Store, suffix string) (AccountKey, string) {
	t.Helper()
	revisionHash := "revision-batch-" + suffix
	if _, _, err := s.SaveJobAIContextRevision(contextRevisionFixture(
		"context-batch-"+suffix, revisionHash, time.Now().Add(-time.Hour),
	)); err != nil {
		t.Fatal(err)
	}
	key := AccountKey{Platform: "zhilian", AccountRef: "account-batch-" + suffix}
	if err := s.CreateAccount(&Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatal(err)
	}
	return key, revisionHash
}

func enableSourcingBatchAccount(t *testing.T, s *Store, key AccountKey) {
	t.Helper()
	if err := s.BindAccountPrincipal(
		key, "hand-"+key.AccountRef, "principal-"+key.AccountRef,
		"session-"+key.AccountRef, "boot-"+key.AccountRef, time.Now(),
	); err != nil {
		t.Fatal(err)
	}
}

func seedSourcingWindowProof(
	t *testing.T,
	s *Store,
	key AccountKey,
	logicalID, positionRef string,
	positionTitle *string,
	terminalAt time.Time,
) {
	t.Helper()
	argsRaw, _ := protocol.Encode(protocol.CandidateReadSourcingWindowArgs{Move: protocol.SourcingWindowMoveCurrent})
	contextRaw, _ := protocol.Encode(protocol.CmdContext{
		Platform: key.Platform, AccountRef: key.AccountRef,
		ExpectedPrincipalFingerprint: "principal-" + key.AccountRef,
	})
	dataRaw, _ := protocol.Encode(protocol.CandidateReadSourcingWindowData{
		PositionRef: positionRef, PositionTitle: positionTitle,
		PlatformUserRefs: []string{"window-user"}, Moved: false, ObservedAt: terminalAt.UnixMilli(),
	})
	resultRaw, _ := protocol.Encode(protocol.ResultBody{
		Ref: logicalID, Status: protocol.ResultStatusOk, Data: dataRaw, ExecMs: 1,
	})
	if err := s.CreateCmd(&CmdRecord{
		MsgID: logicalID, LogicalDispatchID: logicalID,
		Name: protocol.PrimCandidateReadSourcingWindow, Class: string(protocol.ClassIntrusive),
		Domain: key.Platform + ":" + key.AccountRef, Platform: key.Platform, AccountRef: key.AccountRef,
		ExpectedPrincipalFingerprint: "principal-" + key.AccountRef,
		ContextJSON:                  string(contextRaw), Args: string(argsRaw),
		HandID: "hand-" + key.AccountRef, Session: "session-" + key.AccountRef,
		BootIDAtDispatch: "boot-" + key.AccountRef,
		Status:           CmdOk, ResultBody: string(resultRaw), TerminalAt: &terminalAt,
		CreatedAt: terminalAt.Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
}

func seedSourcingTargetProof(
	t *testing.T,
	s *Store,
	key AccountKey,
	logicalID string,
	data protocol.CandidateReadSourcingResumeData,
	terminalAt time.Time,
) {
	t.Helper()
	argsRaw, _ := protocol.Encode(protocol.CandidateReadSourcingTargetResumeArgs{
		PlatformUserRef: data.PlatformUserRef, PositionRef: data.PositionRef,
	})
	contextRaw, _ := protocol.Encode(protocol.CmdContext{
		Platform: key.Platform, AccountRef: key.AccountRef,
		ExpectedPrincipalFingerprint: "principal-" + key.AccountRef,
	})
	dataRaw, _ := protocol.Encode(data)
	resultRaw, _ := protocol.Encode(protocol.ResultBody{
		Ref: logicalID, Status: protocol.ResultStatusOk, Data: dataRaw, ExecMs: 1,
	})
	if err := s.CreateCmd(&CmdRecord{
		MsgID: logicalID, LogicalDispatchID: logicalID,
		Name: protocol.PrimCandidateReadSourcingTargetResume, Class: string(protocol.ClassIntrusive),
		Domain: key.Platform + ":" + key.AccountRef, Platform: key.Platform, AccountRef: key.AccountRef,
		ExpectedPrincipalFingerprint: "principal-" + key.AccountRef,
		ContextJSON:                  string(contextRaw), Args: string(argsRaw),
		HandID: "hand-" + key.AccountRef, Session: "session-" + key.AccountRef,
		BootIDAtDispatch: "boot-" + key.AccountRef,
		Status:           CmdOk, ResultBody: string(resultRaw), TerminalAt: &terminalAt,
		CreatedAt: terminalAt.Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
}

func formalSourcingResumeData(userRef, positionRef, marker string, observedAt time.Time) protocol.CandidateReadSourcingResumeData {
	return protocol.CandidateReadSourcingResumeData{
		PlatformUserRef: userRef, PositionRef: positionRef,
		ContactState: protocol.CandidateContactStateUnestablished, ObservedAt: observedAt.UnixMilli(),
		Basic:        []protocol.CandidateResumeLabelValue{{Label: "marker", Value: marker}},
		Expectations: []protocol.CandidateResumeLabelValue{}, SelfEvaluation: marker,
		Education: marker, WorkExperiences: marker,
	}
}

func sourcingBatchRun(batchID *string, runID, platformUserRef string, capturedAt time.Time) SourcingCandidateRun {
	return SourcingCandidateRun{
		RunID: runID, BatchID: batchID, Platform: "zhilian", AccountRef: "account-batch-members",
		ContextRevisionHash: "revision-batch-members", PlatformUserRef: platformUserRef,
		PositionRef: "position-members", ContactState: "unestablished",
		SourceLogicalDispatchID: "logical-" + runID, ObservedAt: capturedAt.UnixMilli(), CapturedAt: capturedAt,
		SchemaVersion: 1, ContentHash: "hash-" + runID, ResumeJSON: "{}",
	}
}

func TestStartSourcingBatchIsIdempotentAndProtectsOpenScope(t *testing.T) {
	s := openTest(t)
	key, revisionHash := seedSourcingBatchDependencies(t, s, "start")
	startedAt := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	req := StartSourcingBatchRequest{
		BatchID: "batch-start", Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revisionHash, TargetCount: 150, StartedAt: startedAt,
	}
	first, err := s.StartSourcingBatch(req)
	if err != nil || first == nil || !first.Created || first.Batch.Status != SourcingBatchPreparing ||
		first.Batch.TargetCount != 150 || first.Batch.PositionRef != nil || !first.Batch.StartedAt.Equal(startedAt) {
		t.Fatalf("首次启动批次错误: result=%+v err=%v", first, err)
	}

	replayed, err := s.StartSourcingBatch(req)
	if err != nil || replayed == nil || replayed.Created || replayed.Batch.BatchID != first.Batch.BatchID {
		t.Fatalf("相同 batchId/material 未幂等复用: result=%+v err=%v", replayed, err)
	}
	retryWithFreshID := req
	retryWithFreshID.BatchID = "batch-retry-fresh-id"
	retried, err := s.StartSourcingBatch(retryWithFreshID)
	if err != nil || retried == nil || retried.Created || retried.Batch.BatchID != first.Batch.BatchID {
		t.Fatalf("同账号同材料重试未复用非终态批次: result=%+v err=%v", retried, err)
	}

	conflicting := req
	conflicting.BatchID = "batch-conflict"
	conflicting.TargetCount = 151
	if result, err := s.StartSourcingBatch(conflicting); result != nil || !errors.Is(err, ErrSourcingBatchConflict) {
		t.Fatalf("同账号非终态批次不得被不同目标数覆盖: result=%+v err=%v", result, err)
	}
	if err := s.db.Create(&SourcingBatch{
		BatchID: "batch-direct-conflict", Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revisionHash, TargetCount: 1, Status: SourcingBatchPreparing, StartedAt: startedAt,
	}).Error; err == nil {
		t.Fatal("数据库唯一约束未阻止同账号第二个非终态批次")
	}

	stopped, err := s.StopSourcingBatch(StopSourcingBatchRequest{
		BatchID: first.Batch.BatchID, Reason: "用户结束本轮", StoppedAt: startedAt.Add(time.Hour),
	})
	if err != nil || stopped.Status != SourcingBatchStopped || stopped.EndedAt == nil {
		t.Fatalf("停止批次失败: batch=%+v err=%v", stopped, err)
	}
	active, err := s.ActiveSourcingBatch(key)
	if err != nil || active != nil {
		t.Fatalf("终态批次仍占用账号非终态槽: active=%+v err=%v", active, err)
	}
	newBatch, err := s.StartSourcingBatch(StartSourcingBatchRequest{
		Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revisionHash, TargetCount: 80, StartedAt: startedAt.Add(2 * time.Hour),
	})
	if err != nil || newBatch == nil || !newBatch.Created || !strings.HasPrefix(newBatch.Batch.BatchID, "sb-") {
		t.Fatalf("终态后未能生成新批次: result=%+v err=%v", newBatch, err)
	}
}

func TestInvalidateSourcingFeedAtomicallyStopsActiveBatchAndIsMonotonic(t *testing.T) {
	s := openTest(t)
	key, revisionHash := seedSourcingBatchDependencies(t, s, "feed-invalidation")
	startedAt := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	started, err := s.StartSourcingBatch(StartSourcingBatchRequest{
		BatchID: "batch-feed-invalidation", Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revisionHash, TargetCount: 30, StartedAt: startedAt,
	})
	if err != nil || started == nil || !started.Created {
		t.Fatalf("建立 active 批次失败: result=%+v err=%v", started, err)
	}
	invalidatedAt := startedAt.Add(5 * time.Minute)
	first, err := s.InvalidateSourcingFeed(InvalidateSourcingFeedRequest{
		Platform: key.Platform, AccountRef: key.AccountRef,
		Trigger: "pluginReload", At: invalidatedAt,
	})
	if err != nil || first == nil || !first.MarkerAdvanced || !first.BatchStopped {
		t.Fatalf("推荐流换代未原子终止 active 批次: result=%+v err=%v", first, err)
	}
	batch, err := s.SourcingBatchByID(started.Batch.BatchID)
	if err != nil || batch == nil || batch.Status != SourcingBatchStopped ||
		batch.Reason != SourcingFeedChangedReason || batch.EndedAt == nil || !batch.EndedAt.Equal(invalidatedAt) {
		t.Fatalf("换代后的批次终态错误: batch=%+v err=%v", batch, err)
	}
	account, err := s.AccountByKey(key)
	if err != nil || account == nil || account.SourcingFeedInvalidatedAt == nil ||
		!account.SourcingFeedInvalidatedAt.Equal(invalidatedAt) || account.StoppedAt == nil ||
		!account.StoppedAt.Equal(invalidatedAt) || account.PausedReason != SourcingFeedChangedReason ||
		!account.DirtyHint {
		t.Fatalf("换代 marker 与账号暂停状态未同事务收敛: account=%+v err=%v", account, err)
	}
	audits, err := s.AuditEntries(20)
	if err != nil {
		t.Fatal(err)
	}
	matchingAudits := 0
	for _, audit := range audits {
		if audit.Category != "sourcing_feed_invalidated" || audit.Platform != key.Platform ||
			audit.AccountRef != key.AccountRef {
			continue
		}
		matchingAudits++
		if !audit.At.Equal(invalidatedAt) || audit.Detail != "trigger=pluginReload;batchStopped=true" {
			t.Fatalf("换代审计内容错误: audit=%+v", audit)
		}
	}
	if matchingAudits != 1 {
		t.Fatalf("首次换代审计数量错误: count=%d audits=%+v", matchingAudits, audits)
	}

	for _, replayAt := range []time.Time{invalidatedAt, invalidatedAt.Add(-time.Minute)} {
		replayed, err := s.InvalidateSourcingFeed(InvalidateSourcingFeedRequest{
			Platform: key.Platform, AccountRef: key.AccountRef,
			Trigger: "olderReplay", At: replayAt,
		})
		if err != nil || replayed == nil || replayed.MarkerAdvanced || replayed.BatchStopped {
			t.Fatalf("重复或更老换代事件未幂等: at=%v result=%+v err=%v", replayAt, replayed, err)
		}
	}
	account, err = s.AccountByKey(key)
	if err != nil || account == nil || account.SourcingFeedInvalidatedAt == nil ||
		!account.SourcingFeedInvalidatedAt.Equal(invalidatedAt) || account.PausedReason != SourcingFeedChangedReason {
		t.Fatalf("重复事件回退了 marker 或暂停原因: account=%+v err=%v", account, err)
	}
	audits, err = s.AuditEntries(20)
	if err != nil {
		t.Fatal(err)
	}
	matchingAudits = 0
	for _, audit := range audits {
		if audit.Category == "sourcing_feed_invalidated" && audit.Platform == key.Platform &&
			audit.AccountRef == key.AccountRef {
			matchingAudits++
		}
	}
	if matchingAudits != 1 {
		t.Fatalf("幂等重放产生重复审计: count=%d audits=%+v", matchingAudits, audits)
	}
}

func TestSourcingBatchPositionBlockResumeAndStopTransitions(t *testing.T) {
	s := openTest(t)
	key, revisionHash := seedSourcingBatchDependencies(t, s, "transitions")
	enableSourcingBatchAccount(t, s, key)
	startedAt := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	result, err := s.StartSourcingBatch(StartSourcingBatchRequest{
		BatchID: "batch-transitions", Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revisionHash, TargetCount: 3, StartedAt: startedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	title := "首次展示标题"
	boundAt := startedAt.Add(time.Minute)
	seedSourcingWindowProof(t, s, key, "window-bind-first", "position-stable", &title, boundAt)
	bound, err := s.BindSourcingBatchPosition(BindSourcingBatchPositionRequest{
		BatchID: result.Batch.BatchID, LogicalDispatchID: "window-bind-first",
	})
	if err != nil || bound.Status != SourcingBatchCollecting || bound.PositionRef == nil ||
		*bound.PositionRef != "position-stable" || bound.PositionTitle == nil || *bound.PositionTitle != title ||
		bound.PositionBoundAt == nil || !bound.PositionBoundAt.Equal(boundAt) {
		t.Fatalf("首次职位绑定错误: batch=%+v err=%v", bound, err)
	}

	changedTitle := "标题刷新但不参与身份"
	seedSourcingWindowProof(t, s, key, "window-bind-replay", "position-stable", &changedTitle, boundAt.Add(time.Second))
	replayed, err := s.BindSourcingBatchPosition(BindSourcingBatchPositionRequest{
		BatchID: result.Batch.BatchID, LogicalDispatchID: "window-bind-replay",
	})
	if err != nil || replayed.PositionTitle == nil || *replayed.PositionTitle != title {
		t.Fatalf("同职位重放不应覆盖首次标题快照: batch=%+v err=%v", replayed, err)
	}
	seedSourcingWindowProof(t, s, key, "window-bind-conflict", "position-other", nil, boundAt.Add(2*time.Second))
	if _, err := s.BindSourcingBatchPosition(BindSourcingBatchPositionRequest{
		BatchID: result.Batch.BatchID, LogicalDispatchID: "window-bind-conflict",
	}); !errors.Is(err, ErrSourcingBatchConflict) {
		t.Fatalf("职位换绑未被拒绝: %v", err)
	}

	blockedAt := startedAt.Add(2 * time.Minute)
	blocked, err := s.BlockSourcingBatch(BlockSourcingBatchRequest{
		BatchID: result.Batch.BatchID, Reason: "窗口身份不足", BlockedAt: blockedAt,
	})
	if err != nil || blocked.Status != SourcingBatchBlocked || blocked.Reason != "窗口身份不足" ||
		blocked.LastAttemptAt == nil || !blocked.LastAttemptAt.Equal(blockedAt) {
		t.Fatalf("阻塞状态错误: batch=%+v err=%v", blocked, err)
	}
	if err := s.MarkSourcingBatchAttempt(result.Batch.BatchID, blockedAt.Add(time.Second)); !errors.Is(err, ErrSourcingBatchStateConflict) {
		t.Fatalf("blocked 状态不得记录新的自动尝试: %v", err)
	}
	restarted, err := s.StartSourcingBatch(StartSourcingBatchRequest{
		BatchID: "batch-transitions-retry", Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revisionHash, TargetCount: 3,
	})
	if err != nil || restarted == nil || restarted.Created || restarted.Batch.BatchID != result.Batch.BatchID ||
		restarted.Batch.Status != SourcingBatchBlocked {
		t.Fatalf("相同 start 重试不得隐式恢复 blocked: result=%+v err=%v", restarted, err)
	}
	resumed, err := s.ResumeSourcingBatch(ResumeSourcingBatchRequest{
		BatchID: result.Batch.BatchID,
	})
	if err != nil || resumed.Status != SourcingBatchCollecting || resumed.Reason != "" {
		t.Fatalf("已有职位的批次恢复状态错误: batch=%+v err=%v", resumed, err)
	}

	stoppedAt := startedAt.Add(4 * time.Minute)
	stopped, err := s.StopSourcingBatch(StopSourcingBatchRequest{
		BatchID: result.Batch.BatchID, Reason: "真人停止", StoppedAt: stoppedAt,
	})
	if err != nil || stopped.Status != SourcingBatchStopped || stopped.EndedAt == nil || !stopped.EndedAt.Equal(stoppedAt) {
		t.Fatalf("停止终态错误: batch=%+v err=%v", stopped, err)
	}
	repeatedStop, err := s.StopSourcingBatch(StopSourcingBatchRequest{
		BatchID: result.Batch.BatchID, Reason: "真人停止", StoppedAt: stoppedAt.Add(time.Hour),
	})
	if err != nil || repeatedStop.EndedAt == nil || !repeatedStop.EndedAt.Equal(stoppedAt) {
		t.Fatalf("相同停止请求未幂等保留首次终态: batch=%+v err=%v", repeatedStop, err)
	}
	if _, err := s.StopSourcingBatch(StopSourcingBatchRequest{
		BatchID: result.Batch.BatchID, Reason: "篡改终态原因",
	}); !errors.Is(err, ErrSourcingBatchConflict) {
		t.Fatalf("停止终态原因可被覆盖: %v", err)
	}
	if _, err := s.ResumeSourcingBatch(ResumeSourcingBatchRequest{BatchID: result.Batch.BatchID}); !errors.Is(err, ErrSourcingBatchStateConflict) {
		t.Fatalf("终态批次可被恢复: %v", err)
	}
}

func TestSourcingBatchMembersAreNullableLegacyScopedAndCounted(t *testing.T) {
	s := openTest(t)
	key, revisionHash := seedSourcingBatchDependencies(t, s, "members")
	startedAt := time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC)
	result, err := s.StartSourcingBatch(StartSourcingBatchRequest{
		BatchID: "batch-members", Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revisionHash, TargetCount: 3, StartedAt: startedAt,
	})
	if err != nil {
		t.Fatal(err)
	}

	legacyOne := sourcingBatchRun(nil, "run-legacy-one", "user-repeated-legacy", startedAt)
	legacyTwo := sourcingBatchRun(nil, "run-legacy-two", "user-repeated-legacy", startedAt.Add(time.Second))
	if err := s.db.Create(&legacyOne).Error; err != nil {
		t.Fatalf("首条 BatchID NULL 历史行失败: %v", err)
	}
	if err := s.db.Create(&legacyTwo).Error; err != nil {
		t.Fatalf("BatchID NULL 历史行被批内唯一键误伤: %v", err)
	}

	batchID := result.Batch.BatchID
	memberOne := sourcingBatchRun(&batchID, "run-member-one", "user-one", startedAt.Add(2*time.Second))
	memberTwo := sourcingBatchRun(&batchID, "run-member-two", "user-two", startedAt.Add(3*time.Second))
	if err := s.db.Create(&memberOne).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&memberTwo).Error; err != nil {
		t.Fatal(err)
	}
	duplicate := sourcingBatchRun(&batchID, "run-member-duplicate", "user-one", startedAt.Add(4*time.Second))
	if err := s.db.Create(&duplicate).Error; err == nil {
		t.Fatal("同批次相同 platformUserRef 未被数据库唯一约束阻止")
	}
	orphanID := "batch-does-not-exist"
	orphan := sourcingBatchRun(&orphanID, "run-orphan", "user-orphan", startedAt.Add(5*time.Second))
	if err := s.db.Create(&orphan).Error; err == nil {
		t.Fatal("不存在的 batchId 未被外键约束阻止")
	}

	progress, err := s.SourcingBatchProgressByID(batchID)
	if err != nil || progress == nil || progress.CapturedCount != 2 || progress.RemainingCount != 1 ||
		progress.BatchID != batchID || progress.ContextRevisionHash != revisionHash || progress.TargetCount != 3 {
		t.Fatalf("批次聚合计数错误: progress=%+v err=%v", progress, err)
	}
	progressJSON, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"position-members", "user-one", "user-two", "account-batch-members"} {
		if strings.Contains(string(progressJSON), private) {
			t.Fatalf("批次状态投影泄漏平台身份或职位: %q in %s", private, progressJSON)
		}
	}
	refs, err := s.SourcingBatchExcludedPlatformUserRefs(batchID)
	if err != nil || len(refs) != 2 || refs[0] != "user-one" || refs[1] != "user-two" {
		t.Fatalf("完整批次成员查询错误: refs=%+v err=%v", refs, err)
	}
	for userRef, want := range map[string]bool{"user-one": true, "user-two": true, "user-repeated-legacy": false} {
		got, err := s.SourcingBatchHasMember(batchID, userRef)
		if err != nil || got != want {
			t.Fatalf("批次成员判断错误: ref=%s got=%v want=%v err=%v", userRef, got, want, err)
		}
	}
	if _, err := s.SourcingBatchExcludedPlatformUserRefs("missing-batch"); !errors.Is(err, ErrSourcingBatchNotFound) {
		t.Fatalf("不存在批次的成员查询未响亮失败: %v", err)
	}
}

func TestCompleteSourcingBatchCandidateRunIsAtomicIdempotentAndStopsAtTarget(t *testing.T) {
	s := openTest(t)
	key, revisionHash := seedSourcingBatchDependencies(t, s, "formal-complete")
	enableSourcingBatchAccount(t, s, key)
	startedAt := time.Now().Add(-time.Hour)
	started, err := s.StartSourcingBatch(StartSourcingBatchRequest{
		BatchID: "batch-formal-complete", Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revisionHash, TargetCount: 2, StartedAt: startedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	positionTitle := "仅展示职位"
	windowAt := startedAt.Add(time.Minute)
	seedSourcingWindowProof(t, s, key, "window-formal-complete", "position-formal", &positionTitle, windowAt)
	if _, err := s.BindSourcingBatchPosition(BindSourcingBatchPositionRequest{
		BatchID: started.Batch.BatchID, LogicalDispatchID: "window-formal-complete",
	}); err != nil {
		t.Fatal(err)
	}

	firstAt := startedAt.Add(2 * time.Minute)
	firstData := formalSourcingResumeData("formal-user-one", "position-formal", "first", firstAt)
	seedSourcingTargetProof(t, s, key, "target-formal-one", firstData, firstAt)
	first, err := s.CompleteSourcingBatchCandidateRun(CompleteSourcingBatchCandidateRunRequest{
		BatchID: started.Batch.BatchID, RunID: "formal-run-one",
		LogicalDispatchID: "target-formal-one", Data: firstData,
	})
	if err != nil || first == nil || !first.Created || first.CapturedCount != 1 || first.BatchCompleted ||
		first.Run.BatchID == nil || *first.Run.BatchID != started.Batch.BatchID {
		t.Fatalf("首个正式成员收编错误: result=%+v err=%v", first, err)
	}
	lookedUp, err := s.SourcingRunByID(first.Run.RunID)
	if err != nil || lookedUp == nil || lookedUp.BatchID == nil || *lookedUp.BatchID != started.Batch.BatchID {
		t.Fatalf("按随机 runId 读取批次成员失败: run=%+v err=%v", lookedUp, err)
	}
	account, err := s.AccountByKey(key)
	if err != nil || account == nil || account.StoppedAt != nil || account.PausedReason != "" {
		t.Fatalf("未达目标不应暂停账号: account=%+v err=%v", account, err)
	}

	replayed, err := s.CompleteSourcingBatchCandidateRun(CompleteSourcingBatchCandidateRunRequest{
		BatchID: started.Batch.BatchID, RunID: "ignored-replay-run",
		LogicalDispatchID: "target-formal-one", Data: firstData,
	})
	if err != nil || replayed == nil || replayed.Created || replayed.Run.RunID != first.Run.RunID || replayed.CapturedCount != 1 {
		t.Fatalf("同 logical 重放未复用首次成员: result=%+v err=%v", replayed, err)
	}

	duplicateAt := startedAt.Add(3 * time.Minute)
	duplicateData := formalSourcingResumeData("formal-user-one", "position-formal", "updated-page-snapshot", duplicateAt)
	seedSourcingTargetProof(t, s, key, "target-formal-duplicate-user", duplicateData, duplicateAt)
	duplicate, err := s.CompleteSourcingBatchCandidateRun(CompleteSourcingBatchCandidateRunRequest{
		BatchID: started.Batch.BatchID, RunID: "ignored-duplicate-member-run",
		LogicalDispatchID: "target-formal-duplicate-user", Data: duplicateData,
	})
	if err != nil || duplicate == nil || duplicate.Created || duplicate.Run.RunID != first.Run.RunID ||
		duplicate.CapturedCount != 1 {
		t.Fatalf("新 logical 的重复候选人未按批内成员幂等: result=%+v err=%v", duplicate, err)
	}

	secondAt := startedAt.Add(4 * time.Minute)
	secondData := formalSourcingResumeData("formal-user-two", "position-formal", "second", secondAt)
	seedSourcingTargetProof(t, s, key, "target-formal-two", secondData, secondAt)
	second, err := s.CompleteSourcingBatchCandidateRun(CompleteSourcingBatchCandidateRunRequest{
		BatchID: started.Batch.BatchID, RunID: "formal-run-two",
		LogicalDispatchID: "target-formal-two", Data: secondData,
	})
	if err != nil || second == nil || !second.Created || second.CapturedCount != 2 || !second.BatchCompleted {
		t.Fatalf("达标成员未原子完成批次: result=%+v err=%v", second, err)
	}
	progress, err := s.SourcingBatchProgressByID(started.Batch.BatchID)
	if err != nil || progress == nil || progress.Status != SourcingBatchCompleted || progress.EndedAt == nil ||
		!progress.EndedAt.Equal(secondAt) || progress.CapturedCount != 2 || progress.RemainingCount != 0 {
		t.Fatalf("达标后的批次状态错误: progress=%+v err=%v", progress, err)
	}
	latest, err := s.LatestSourcingBatchProgress(key)
	if err != nil || latest == nil || latest.BatchID != started.Batch.BatchID ||
		latest.Status != SourcingBatchCompleted || latest.CapturedCount != 2 {
		t.Fatalf("active 消失后管理状态未返回最近 completed: latest=%+v err=%v", latest, err)
	}
	account, err = s.AccountByKey(key)
	if err != nil || account == nil || account.StoppedAt == nil || !account.StoppedAt.Equal(secondAt) ||
		account.PausedReason != "sourcingTargetReached" || !account.DirtyHint {
		t.Fatalf("达标事务未暂停账号调度: account=%+v err=%v", account, err)
	}

	replayedTerminal, err := s.CompleteSourcingBatchCandidateRun(CompleteSourcingBatchCandidateRunRequest{
		BatchID: started.Batch.BatchID, RunID: "ignored-terminal-replay",
		LogicalDispatchID: "target-formal-two", Data: secondData,
	})
	if err != nil || replayedTerminal == nil || replayedTerminal.Created ||
		replayedTerminal.Run.RunID != second.Run.RunID || !replayedTerminal.BatchCompleted || replayedTerminal.CapturedCount != 2 {
		t.Fatalf("使批次达标的 logical 在 completed 后未幂等: result=%+v err=%v", replayedTerminal, err)
	}

	thirdAt := startedAt.Add(5 * time.Minute)
	thirdData := formalSourcingResumeData("formal-user-three", "position-formal", "third", thirdAt)
	seedSourcingTargetProof(t, s, key, "target-formal-after-complete", thirdData, thirdAt)
	if result, err := s.CompleteSourcingBatchCandidateRun(CompleteSourcingBatchCandidateRunRequest{
		BatchID: started.Batch.BatchID, RunID: "formal-run-after-complete",
		LogicalDispatchID: "target-formal-after-complete", Data: thirdData,
	}); result != nil || !errors.Is(err, ErrSourcingBatchStateConflict) {
		t.Fatalf("completed 后仍可新增 target+1 成员: result=%+v err=%v", result, err)
	}
	progress, err = s.SourcingBatchProgressByID(started.Batch.BatchID)
	if err != nil || progress.CapturedCount != 2 {
		t.Fatalf("target+1 拒绝后计数被污染: progress=%+v err=%v", progress, err)
	}
}

func TestCompleteSourcingBatchCandidateRunRollsBackMemberWhenTargetPauseFails(t *testing.T) {
	s := openTest(t)
	key, revisionHash := seedSourcingBatchDependencies(t, s, "formal-rollback")
	enableSourcingBatchAccount(t, s, key)
	startedAt := time.Now().Add(-time.Hour)
	started, err := s.StartSourcingBatch(StartSourcingBatchRequest{
		BatchID: "batch-formal-rollback", Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revisionHash, TargetCount: 1, StartedAt: startedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	windowAt := startedAt.Add(time.Minute)
	seedSourcingWindowProof(t, s, key, "window-formal-rollback", "position-rollback", nil, windowAt)
	if _, err := s.BindSourcingBatchPosition(BindSourcingBatchPositionRequest{
		BatchID: started.Batch.BatchID, LogicalDispatchID: "window-formal-rollback",
	}); err != nil {
		t.Fatal(err)
	}
	targetAt := startedAt.Add(2 * time.Minute)
	data := formalSourcingResumeData("formal-user-rollback", "position-rollback", "rollback", targetAt)
	seedSourcingTargetProof(t, s, key, "target-formal-rollback", data, targetAt)
	if err := s.db.Exec(`
		CREATE TRIGGER fail_sourcing_target_pause
		BEFORE UPDATE OF stopped_at ON accounts
		WHEN NEW.paused_reason = 'sourcingTargetReached'
		BEGIN
			SELECT RAISE(ABORT, 'fixture pause failed');
		END
	`).Error; err != nil {
		t.Fatal(err)
	}
	if result, err := s.CompleteSourcingBatchCandidateRun(CompleteSourcingBatchCandidateRunRequest{
		BatchID: started.Batch.BatchID, RunID: "formal-run-rollback",
		LogicalDispatchID: "target-formal-rollback", Data: data,
	}); result != nil || err == nil {
		t.Fatalf("账号暂停写失败时 Complete 应整体失败: result=%+v err=%v", result, err)
	}
	progress, err := s.SourcingBatchProgressByID(started.Batch.BatchID)
	if err != nil || progress.Status != SourcingBatchCollecting || progress.CapturedCount != 0 || progress.EndedAt != nil {
		t.Fatalf("失败事务留下成员或 completed: progress=%+v err=%v", progress, err)
	}
	account, err := s.AccountByKey(key)
	if err != nil || account == nil || account.StoppedAt != nil || account.PausedReason != "" {
		t.Fatalf("失败事务污染账号调度状态: account=%+v err=%v", account, err)
	}
	candidate, err := s.CandidateByKey(CandidateKey{Platform: key.Platform, PlatformUserRef: data.PlatformUserRef})
	if err != nil || candidate != nil {
		t.Fatalf("失败事务留下候选人人根: candidate=%+v err=%v", candidate, err)
	}
}

func TestSourcingBatchStartRequiresExistingAccountRevisionAndExplicitTarget(t *testing.T) {
	s := openTest(t)
	key, revisionHash := seedSourcingBatchDependencies(t, s, "validation")
	if latest, err := s.LatestSourcingBatchProgress(key); err != nil || latest != nil {
		t.Fatalf("无批次账号应返回 nil 最新状态: latest=%+v err=%v", latest, err)
	}
	base := StartSourcingBatchRequest{
		Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revisionHash, TargetCount: 1,
	}
	invalidTarget := base
	invalidTarget.TargetCount = 0
	if _, err := s.StartSourcingBatch(invalidTarget); !errors.Is(err, ErrSourcingBatchInvalid) {
		t.Fatalf("targetCount=0 未拒绝: %v", err)
	}
	missingAccount := base
	missingAccount.AccountRef = "missing-account"
	if _, err := s.StartSourcingBatch(missingAccount); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("不存在账号未拒绝: %v", err)
	}
	missingRevision := base
	missingRevision.ContextRevisionHash = "missing-revision"
	if _, err := s.StartSourcingBatch(missingRevision); !errors.Is(err, ErrJobAIContextRevisionNotFound) {
		t.Fatalf("不存在 revision 未拒绝: %v", err)
	}
}

type sourcingCandidateRunBeforeBatch struct {
	RunID               string `gorm:"primaryKey"`
	Platform            string `gorm:"not null;index:idx_sourcing_account_revision,priority:1"`
	AccountRef          string `gorm:"not null;index:idx_sourcing_account_revision,priority:2"`
	ContextRevisionHash string `gorm:"not null;index:idx_sourcing_account_revision,priority:3"`

	PlatformUserRef string `gorm:"not null"`
	DisplayName     *string
	PositionRef     string `gorm:"not null"`
	PositionTitle   *string
	ContactState    string `gorm:"not null"`

	SourceLogicalDispatchID string    `gorm:"not null;uniqueIndex"`
	ObservedAt              int64     `gorm:"not null"`
	CapturedAt              time.Time `gorm:"not null;index"`
	SchemaVersion           int       `gorm:"not null"`
	ContentHash             string    `gorm:"not null"`
	ResumeJSON              string    `gorm:"not null"`
	CreatedAt               time.Time
}

func (sourcingCandidateRunBeforeBatch) TableName() string { return "sourcing_candidate_runs" }

func TestSourcingBatchAutoMigrationKeepsLegacyRunsUnassigned(t *testing.T) {
	dir := t.TempDir()
	legacyDB, err := gorm.Open(sqlite.Open("file:"+filepath.Join(dir, "brain.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacyDB.AutoMigrate(&sourcingCandidateRunBeforeBatch{}); err != nil {
		t.Fatal(err)
	}
	legacy := sourcingCandidateRunBeforeBatch{
		RunID: "legacy-before-batch", Platform: "zhilian", AccountRef: "legacy-account",
		ContextRevisionHash: "legacy-revision", PlatformUserRef: "legacy-user",
		PositionRef: "legacy-position", ContactState: "unestablished",
		SourceLogicalDispatchID: "legacy-logical", ObservedAt: 1,
		CapturedAt: time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC), SchemaVersion: 1,
		ContentHash: "legacy-hash", ResumeJSON: "{}",
	}
	if err := legacyDB.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	legacySQL, err := legacyDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := legacySQL.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("批次 schema 加法迁移失败: %v", err)
	}
	defer s.Close()
	var migrated SourcingCandidateRun
	if err := s.db.First(&migrated, "run_id = ?", legacy.RunID).Error; err != nil {
		t.Fatal(err)
	}
	if migrated.BatchID != nil || migrated.PlatformUserRef != legacy.PlatformUserRef ||
		migrated.SourceLogicalDispatchID != legacy.SourceLogicalDispatchID || migrated.ResumeJSON != legacy.ResumeJSON {
		t.Fatalf("旧采集事实被伪造归批或改写: %+v", migrated)
	}
	if !s.db.Migrator().HasColumn(&SourcingCandidateRun{}, "BatchID") {
		t.Fatal("迁移后缺少 nullable batch_id")
	}
}
