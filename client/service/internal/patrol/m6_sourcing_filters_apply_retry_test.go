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

// 手侧 2026-09-04 起把"选项点不动/自定义下拉没出来"判为瞬时(CTX_NOT_READY +
// afterRecovery),判定现场随消息带出。
func notReadyFilterSurface() error {
	return &RunError{
		Code: protocol.ErrCodeCtxNotReady, Reason: protocol.NotReadyReasonPageBroken,
		Retryable: protocol.RetryableAfterRecovery, SideEffect: protocol.SideEffectNone,
		Cause: errors.New("智联筛选面或推荐列表尚未稳定（option_click_exhausted；age/自定义=选中）"),
	}
}

// 筛选面未就绪时同轮重试,第二次成功即止。2026-09-03 与 09-04 客户机各出一次
// 点年龄「自定义」落空,09-04 那次把当日剩余 3 个职位共 53 个名额一起废掉。
func TestSourcingFiltersApplyRetriesOnceWhenSurfaceNotReady(t *testing.T) {
	h := newHarness(t)
	batch := startPreparingSourcingBatch(t, h, "filters-apply-retry")

	applyCalls := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimCandidateApplySourcingFilters:
			applyCalls++
			if applyCalls == 1 {
				return nil, notReadyFilterSurface()
			}
			return defaultHandler(request)
		case protocol.PrimCandidateReadSourcingWindow:
			// 筛选重试成功即达本测试出口;用可恢复取消止住后续绑定。
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
		protocol.PrimCandidateApplySourcingFilters,
		protocol.PrimCandidateApplySourcingFilters,
		protocol.PrimCandidateReadSourcingWindow,
	}
	if got := h.runner.names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("未按 状态闸→select→filters→filters→window 同轮重试: got=%v", got)
	}
	if h.runner.count(protocol.PrimNavEnsureSurface) != 0 {
		t.Fatalf("pageBroken 不应触发 IM 救场链: %v", h.runner.names())
	}
	stored, err := h.db.SourcingBatchByID(batch.BatchID)
	if err != nil || stored == nil || stored.Status != store.SourcingBatchPreparing || stored.Reason != "" {
		t.Fatalf("重试成功后批次应仍是 preparing: batch=%+v err=%v", stored, err)
	}
}

// 重试耗尽(1 次首发 + sourcingFiltersApplyMaxRetries 次重试)仍未就绪就按原路径
// 拦停批次:不加持久化计数、不无界重试。此后由当日职位计划按跳过类跳过该职位。
func TestSourcingFiltersApplyBlocksAfterRetriesExhausted(t *testing.T) {
	h := newHarness(t)
	batch := startPreparingSourcingBatch(t, h, "filters-apply-retry-exhausted")

	applyCalls := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimCandidateApplySourcingFilters {
			applyCalls++
			return nil, notReadyFilterSurface()
		}
		return defaultHandler(request)
	}

	result, tickErr := h.manager.Tick(context.Background())
	if tickErr != nil {
		t.Fatalf("Tick: %v", tickErr)
	}
	if len(result.Rounds) != 1 || result.Rounds[0].Err == nil {
		t.Fatalf("重试耗尽仍未就绪应以错误收束: %+v", result.Rounds)
	}
	if want := 1 + sourcingFiltersApplyMaxRetries; applyCalls != want {
		t.Fatalf("应恰好尝试 %d 次筛选覆盖,实际 %d 次: %v", want, applyCalls, h.runner.names())
	}
	stored, err := h.db.SourcingBatchByID(batch.BatchID)
	if err != nil || stored == nil || stored.Status != store.SourcingBatchBlocked ||
		stored.Reason != sourcingBlockFiltersApply {
		t.Fatalf("重试耗尽后批次应按 filtersApplyFailed blocked: batch=%+v err=%v", stored, err)
	}
	// 留痕:原因码收窄前的判定现场(错误码/原因/手报原话含 reason 与格位)随批次行落库。
	if !strings.Contains(stored.ReasonDetail, "CTX_NOT_READY/pageBroken") ||
		!strings.Contains(stored.ReasonDetail, "option_click_exhausted") ||
		!strings.Contains(stored.ReasonDetail, "age/自定义=选中") {
		t.Fatalf("批次留痕应带判定现场: %q", stored.ReasonDetail)
	}
}

// 手判 manualOnly 的筛选失败(如平台改版导致选项集不认识)一次都不重试:
// transientPageNotReady 只认 CTX_NOT_READY 且 afterRecovery/yes。
func TestSourcingFiltersApplyDoesNotRetryManualOnlyFailure(t *testing.T) {
	h := newHarness(t)
	batch := startPreparingSourcingBatch(t, h, "filters-apply-manual-only")

	applyCalls := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimCandidateApplySourcingFilters {
			applyCalls++
			return nil, &RunError{
				Code: protocol.ErrCodeElementUnresolved, Retryable: protocol.RetryableManualOnly,
				SideEffect: protocol.SideEffectNone,
				Cause: errors.New(
					"智联筛选条件无法完整覆盖并回读确认（option_set_mismatch）"),
			}
		}
		return defaultHandler(request)
	}

	if _, tickErr := h.manager.Tick(context.Background()); tickErr != nil {
		t.Fatalf("Tick: %v", tickErr)
	}
	if applyCalls != 1 {
		t.Fatalf("manualOnly 不得重试,实际尝试 %d 次: %v", applyCalls, h.runner.names())
	}
	stored, err := h.db.SourcingBatchByID(batch.BatchID)
	if err != nil || stored == nil || stored.Status != store.SourcingBatchBlocked ||
		stored.Reason != sourcingBlockFiltersApply {
		t.Fatalf("manualOnly 失败仍按 filtersApplyFailed blocked: batch=%+v err=%v", stored, err)
	}
}
