package patrol

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

func notReadyRecommendPage() error {
	return &RunError{
		Code: protocol.ErrCodeCtxNotReady, Reason: protocol.NotReadyReasonPageBroken,
		Retryable: protocol.RetryableAfterRecovery, SideEffect: protocol.SideEffectNone,
		Cause: errors.New("智联推荐页在期限内未就绪"),
	}
}

func startPreparingSourcingBatch(t *testing.T, h *harness, tag string) *store.SourcingBatch {
	t.Helper()
	revisionHash := seedStartableSourcingRevision(t, h, tag)
	started, err := h.db.StartSourcingBatch(store.StartSourcingBatchRequest{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ContextRevisionHash: revisionHash, TargetCount: 1, StartedAt: h.clock.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &started.Batch
}

// 切职位时推荐页未就绪(手自证 afterRecovery)只在同轮多试一次;不走 ensureSurface
// 救场链(它只认 pageAbsent/contentScriptDead 且导航去 IM 页)。2026-09-02 尚虹02
// 真机:一次 9.8s 的慢加载让第二个职位当日份额全丢。
func TestSourcingPositionSelectRetriesOnceWhenRecommendPageNotReady(t *testing.T) {
	h := newHarness(t)
	batch := startPreparingSourcingBatch(t, h, "position-select-retry")

	selectCalls := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimCandidateSelectSourcingPosition:
			selectCalls++
			if selectCalls == 1 {
				return nil, notReadyRecommendPage()
			}
			return defaultHandler(request)
		case protocol.PrimCandidateApplySourcingFilters:
			// 第二次职位选择成功即达本测试出口;用可恢复取消止住后续绑定。
			return nil, context.Canceled
		default:
			return defaultHandler(request)
		}
	}

	result, tickErr := h.manager.Tick(context.Background())
	if tickErr != nil {
		t.Fatalf("Tick: %v", tickErr)
	}
	if len(result.Rounds) != 1 || !errors.Is(result.Rounds[0].Err, context.Canceled) {
		t.Fatalf("重试成功后的定向停止结果不符: %+v", result.Rounds)
	}
	want := []string{
		protocol.PrimJobReadPublishedList,
		protocol.PrimCandidateSelectSourcingPosition,
		protocol.PrimCandidateSelectSourcingPosition,
		protocol.PrimCandidateApplySourcingFilters,
	}
	if got := h.runner.names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("未按 状态闸→select→select→filters 同轮重试: got=%v", got)
	}
	if h.runner.count(protocol.PrimNavEnsureSurface) != 0 {
		t.Fatalf("pageBroken 不应触发 IM 救场链: %v", h.runner.names())
	}
	stored, err := h.db.SourcingBatchByID(batch.BatchID)
	if err != nil || stored == nil || stored.Status != store.SourcingBatchPreparing || stored.Reason != "" {
		t.Fatalf("重试成功后批次应仍是 preparing: batch=%+v err=%v", stored, err)
	}
}

// 第二次仍未就绪就按原路径拦停批次:只多一次,不加计数、不无界重试。
func TestSourcingPositionSelectBlocksAfterSecondNotReady(t *testing.T) {
	h := newHarness(t)
	batch := startPreparingSourcingBatch(t, h, "position-select-retry-exhausted")

	selectCalls := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimCandidateSelectSourcingPosition {
			selectCalls++
			return nil, notReadyRecommendPage()
		}
		return defaultHandler(request)
	}

	result, tickErr := h.manager.Tick(context.Background())
	if tickErr != nil {
		t.Fatalf("Tick: %v", tickErr)
	}
	if len(result.Rounds) != 1 || result.Rounds[0].Err == nil {
		t.Fatalf("两次未就绪应以错误收束: %+v", result.Rounds)
	}
	if selectCalls != 2 {
		t.Fatalf("应恰好尝试两次职位选择,实际 %d 次: %v", selectCalls, h.runner.names())
	}
	stored, err := h.db.SourcingBatchByID(batch.BatchID)
	if err != nil || stored == nil || stored.Status != store.SourcingBatchBlocked {
		t.Fatalf("第二次未就绪后批次应 blocked: batch=%+v err=%v", stored, err)
	}
	if !strings.Contains(result.Rounds[0].Err.Error(), "智联推荐页在期限内未就绪") {
		t.Fatalf("轮错误应带手报原话: %v", result.Rounds[0].Err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil || account.PausedReason != PauseSourcingBlocked {
		t.Fatalf("批次拦停后账号应暂停 sourcingBlocked: %+v err=%v", account, err)
	}
}

// 非瞬时失败(manualOnly/no)一次都不重试:重试只认手的瞬时证词。
func TestSourcingPositionSelectDoesNotRetryNonTransientFailure(t *testing.T) {
	h := newHarness(t)
	batch := startPreparingSourcingBatch(t, h, "position-select-no-retry")

	selectCalls := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimCandidateSelectSourcingPosition {
			selectCalls++
			return nil, &RunError{
				Code: protocol.ErrCodeTargetNotFound, Retryable: protocol.RetryableManualOnly,
				SideEffect: protocol.SideEffectNone,
				Cause:      errors.New("后台绑定职位不在当前智联职位列表中"),
			}
		}
		return defaultHandler(request)
	}

	if _, tickErr := h.manager.Tick(context.Background()); tickErr != nil {
		t.Fatalf("Tick: %v", tickErr)
	}
	if selectCalls != 1 {
		t.Fatalf("manualOnly 不得重试,实际 %d 次: %v", selectCalls, h.runner.names())
	}
	stored, err := h.db.SourcingBatchByID(batch.BatchID)
	if err != nil || stored == nil || stored.Status != store.SourcingBatchBlocked ||
		stored.Reason != store.SourcingBatchGateReasonPositionSelect {
		t.Fatalf("非瞬时失败应按 positionSelectFailed 拦停: batch=%+v err=%v", stored, err)
	}
}
