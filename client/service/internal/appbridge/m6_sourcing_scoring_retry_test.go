package appbridge

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/patrol"
	"recruithelper/client/service/internal/store"
)

func newScoringRetryScorer(
	t *testing.T,
	h *sourcingActorHarness,
	advice *sourcingBatchScoringAdvice,
) *patrol.Manager {
	t.Helper()
	scorer, err := patrol.NewManager(
		h.store, PatrolRunner{Dispatcher: h.sender.dispatcher}, sourcingActorHands{},
		patrol.Config{Clock: h.clock, Location: time.UTC, SourcingAIRetryWait: noSourcingAIRetryWait},
		advice,
	)
	if err != nil {
		t.Fatal(err)
	}
	return scorer
}

func scoredSourcingBatch(t *testing.T, h *sourcingActorHarness, members int) (string, []store.SourcingScoreWorkItem) {
	t.Helper()
	if err := h.manager.StartSourcing(h.key, h.revision.RevisionHash, members); err != nil {
		t.Fatal(err)
	}
	batch, err := h.store.ActiveSourcingBatch(h.key)
	if err != nil || batch == nil {
		t.Fatalf("启动后缺少正式批次: batch=%+v err=%v", batch, err)
	}
	if _, err := h.manager.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	work, err := h.store.PendingSourcingScoreWork(batch.BatchID)
	if err != nil || len(work) != members {
		t.Fatalf("采集后待评分成员错误: work=%d err=%v", len(work), err)
	}
	return batch.BatchID, work
}

func TestScoringRateLimitedRetriesUnboundedWithoutBudget(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-a"}})
	batchID, work := scoredSourcingBatch(t, h, 1)
	advice := &sourcingBatchScoringAdvice{
		respond: func(_ string, attempt int) (m5ai.CompletionResponse, error) {
			if attempt <= 6 {
				return m5ai.CompletionResponse{}, &m5ai.ProviderError{
					Class: "rateLimited", FailureStage: m5ai.FailureStageProviderHTTP,
					DetailCode: "rateLimited",
				}
			}
			return scoringFixtureResponse(`{"score":9}`), nil
		},
	}
	scorer := newScoringRetryScorer(t, h, advice)
	progress, err := scorer.ScoreCompletedSourcingBatch(context.Background(), batchID)
	if err != nil || !progress.Completed || progress.OKCount != 1 {
		t.Fatalf("429 重试后未成功终局: progress=%+v err=%v", progress, err)
	}
	invocation, err := h.store.SourcingScoreByRunID(work[0].Run.RunID)
	if err != nil || invocation == nil || invocation.Status != store.AIInvocationOK ||
		invocation.Score == nil || *invocation.Score != 9 ||
		invocation.AttemptCount != 7 || invocation.BudgetedAttemptCount != 1 {
		t.Fatalf("429 尝试不得计入预算: invocation=%+v err=%v", invocation, err)
	}
	if advice.requestCount() != 7 {
		t.Fatalf("429 重试次数错误: %d", advice.requestCount())
	}
}

func TestScoringCancellationLeavesInFlightAndResumeCompletes(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-a"}})
	batchID, work := scoredSourcingBatch(t, h, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	advice := &sourcingBatchScoringAdvice{
		respond: func(_ string, attempt int) (m5ai.CompletionResponse, error) {
			if attempt == 1 {
				cancel()
				return m5ai.CompletionResponse{}, &m5ai.ProviderError{
					Class: "rateLimited", FailureStage: m5ai.FailureStageProviderHTTP,
					DetailCode: "rateLimited",
				}
			}
			return scoringFixtureResponse(`{"score":6}`), nil
		},
	}
	scorer := newScoringRetryScorer(t, h, advice)
	progress, err := scorer.ScoreCompletedSourcingBatch(ctx, batchID)
	if !errors.Is(err, context.Canceled) || progress == nil || progress.InFlightCount != 1 {
		t.Fatalf("取消未保留 inFlight: progress=%+v err=%v", progress, err)
	}
	invocation, err := h.store.SourcingScoreByRunID(work[0].Run.RunID)
	if err != nil || invocation == nil || invocation.FinishedAt != nil ||
		invocation.AttemptCount != 1 || invocation.BudgetedAttemptCount != 1 {
		t.Fatalf("取消后的预留形态错误: invocation=%+v err=%v", invocation, err)
	}

	progress, err = scorer.ScoreCompletedSourcingBatch(context.Background(), batchID)
	if err != nil || !progress.Completed || progress.OKCount != 1 {
		t.Fatalf("续驱动未成功终局: progress=%+v err=%v", progress, err)
	}
	invocation, err = h.store.SourcingScoreByRunID(work[0].Run.RunID)
	if err != nil || invocation == nil || invocation.Status != store.AIInvocationOK ||
		invocation.AttemptCount != 2 || invocation.BudgetedAttemptCount != 2 {
		t.Fatalf("接手续驱动计数错误: invocation=%+v err=%v", invocation, err)
	}
	requests := advice.requestsSnapshot()
	if len(requests) != 2 || strings.Contains(requests[0].InvocationID, "#a") ||
		!strings.HasSuffix(requests[1].InvocationID, "#a2") {
		t.Fatalf("attempt 追踪身份错误: %+v", requests)
	}
}

func TestScoringGateClosureDuringRetryLeavesInFlight(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-a"}})
	batchID, work := scoredSourcingBatch(t, h, 1)
	advice := &sourcingBatchScoringAdvice{
		respond: func(_ string, attempt int) (m5ai.CompletionResponse, error) {
			if attempt == 1 {
				return m5ai.CompletionResponse{}, &m5ai.ProviderError{
					Class: "providerUnavailable", FailureStage: m5ai.FailureStageProviderHTTP,
					DetailCode: "providerUnavailable",
				}
			}
			return scoringFixtureResponse(`{"score":7}`), nil
		},
	}
	scorer := newScoringRetryScorer(t, h, advice)
	blocked := errors.New("fixture gate closed during retry")
	gateCalls := 0
	scorer.SetWorkflowMemberGate(func() error {
		gateCalls++
		if gateCalls >= 2 {
			return blocked
		}
		return nil
	})
	progress, err := scorer.ScoreCompletedSourcingBatch(context.Background(), batchID)
	if !errors.Is(err, blocked) || progress == nil || progress.InFlightCount != 1 {
		t.Fatalf("闸中断未保留 inFlight: progress=%+v err=%v", progress, err)
	}
	invocation, err := h.store.SourcingScoreByRunID(work[0].Run.RunID)
	if err != nil || invocation == nil || invocation.FinishedAt != nil ||
		invocation.AttemptCount != 1 {
		t.Fatalf("闸中断后的预留形态错误: invocation=%+v err=%v", invocation, err)
	}

	scorer.SetWorkflowMemberGate(func() error { return nil })
	progress, err = scorer.ScoreCompletedSourcingBatch(context.Background(), batchID)
	if err != nil || !progress.Completed || progress.OKCount != 1 {
		t.Fatalf("闸放开后续驱动失败: progress=%+v err=%v", progress, err)
	}
	invocation, err = h.store.SourcingScoreByRunID(work[0].Run.RunID)
	if err != nil || invocation == nil || invocation.Status != store.AIInvocationOK ||
		invocation.AttemptCount != 2 || invocation.BudgetedAttemptCount != 2 {
		t.Fatalf("闸放开后计数错误: invocation=%+v err=%v", invocation, err)
	}
}

func TestScoringPoolRunsMembersConcurrently(t *testing.T) {
	refs := []string{"c-one", "c-two", "c-three", "c-four", "c-five", "c-six"}
	h := newSourcingActorHarness(t, [][]string{refs})
	batchID, _ := scoredSourcingBatch(t, h, len(refs))
	var inFlight, peak int32
	advice := &sourcingBatchScoringAdvice{
		respond: func(string, int) (m5ai.CompletionResponse, error) {
			current := atomic.AddInt32(&inFlight, 1)
			for {
				observed := atomic.LoadInt32(&peak)
				if current <= observed || atomic.CompareAndSwapInt32(&peak, observed, current) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
			return scoringFixtureResponse(`{"score":7}`), nil
		},
	}
	scorer := newScoringRetryScorer(t, h, advice)
	progress, err := scorer.ScoreCompletedSourcingBatch(context.Background(), batchID)
	if err != nil || !progress.Completed || progress.OKCount != int64(len(refs)) {
		t.Fatalf("并发评分未完整成功: progress=%+v err=%v", progress, err)
	}
	if observed := atomic.LoadInt32(&peak); observed < 2 || observed > 20 {
		t.Fatalf("并发峰值越界(应并行且不超过 20): peak=%d", observed)
	}
}
