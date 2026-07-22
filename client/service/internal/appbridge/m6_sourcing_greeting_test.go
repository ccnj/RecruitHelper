package appbridge

import (
	"context"
	"fmt"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/patrol"
)

type sourcingGreetingScoringAdvice struct {
	requests []m5ai.CompletionRequest
}

func (*sourcingGreetingScoringAdvice) ProviderName() string {
	return "fixture-selection-score-provider"
}
func (*sourcingGreetingScoringAdvice) ModelName() string { return "fixture-selection-score-model" }
func (a *sourcingGreetingScoringAdvice) CompleteJSON(
	_ context.Context,
	request m5ai.CompletionRequest,
) (m5ai.CompletionResponse, error) {
	a.requests = append(a.requests, request)
	zero := 0
	return m5ai.CompletionResponse{
		JSONText: `{"score":8}`,
		Usage: m5ai.CompletionUsage{
			InputTokens: 10, CachedInputTokens: 2, OutputTokens: 3, ReasoningTokens: &zero,
		},
		ReasoningContentEmpty: true,
	}, nil
}

type sourcingBatchGreetingAdvice struct {
	requests []m5ai.CompletionRequest
}

func (*sourcingBatchGreetingAdvice) ProviderName() string { return "fixture-greeting-provider" }
func (*sourcingBatchGreetingAdvice) ModelName() string    { return "fixture-greeting-model" }
func (a *sourcingBatchGreetingAdvice) CompleteJSON(
	_ context.Context,
	request m5ai.CompletionRequest,
) (m5ai.CompletionResponse, error) {
	a.requests = append(a.requests, request)
	if len(a.requests) == 2 {
		return m5ai.CompletionResponse{}, fmt.Errorf("fixture greeting transport failed")
	}
	if len(a.requests) > 2 {
		return m5ai.CompletionResponse{}, fmt.Errorf("发生未授权的第 %d 次招呼语调用", len(a.requests))
	}
	zero := 0
	return m5ai.CompletionResponse{
		JSONText: `{"招呼语":"你好，看到你的经历很匹配，方便聊聊吗？"}`,
		Usage: m5ai.CompletionUsage{
			InputTokens: 20, CachedInputTokens: 3, OutputTokens: 8, ReasoningTokens: &zero,
		},
		ReasoningContentEmpty: true,
	}, nil
}

func prepareSelectedSourcingBatch(t *testing.T, h *sourcingActorHarness, targetCount int) string {
	t.Helper()
	if err := h.manager.StartSourcing(h.key, h.revision.RevisionHash, targetCount); err != nil {
		t.Fatal(err)
	}
	batch, err := h.store.ActiveSourcingBatch(h.key)
	if err != nil || batch == nil {
		t.Fatalf("启动后缺少正式批次: batch=%+v err=%v", batch, err)
	}
	if _, err := h.manager.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	scoringAdvice := &sourcingGreetingScoringAdvice{}
	scorer, err := patrol.NewManager(
		h.store, PatrolRunner{Dispatcher: h.sender.dispatcher}, sourcingActorHands{},
		patrol.Config{Clock: h.clock, Location: time.UTC}, scoringAdvice,
	)
	if err != nil {
		t.Fatal(err)
	}
	if progress, err := scorer.ScoreCompletedSourcingBatch(context.Background(), batch.BatchID); err != nil ||
		!progress.Completed || progress.OKCount != int64(targetCount) {
		t.Fatalf("筛选前评分未完成: progress=%+v err=%v", progress, err)
	}
	selection, err := h.store.SelectCompletedSourcingBatch(batch.BatchID, h.clock.Now())
	if err != nil || selection == nil || selection.SelectedCount != targetCount {
		t.Fatalf("未形成完整 selected 批次: selection=%+v err=%v", selection, err)
	}
	return batch.BatchID
}

func TestSelectedSourcingBatchGeneratesGreetingsWithoutTouchingHand(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-a", "candidate-b"}})
	batchID := prepareSelectedSourcingBatch(t, h, 2)
	beforeHandCalls := len(h.sender.order)

	advice := &sourcingBatchGreetingAdvice{}
	generator, err := patrol.NewManager(
		h.store, PatrolRunner{Dispatcher: h.sender.dispatcher}, sourcingActorHands{},
		patrol.Config{Clock: h.clock, Location: time.UTC}, advice,
	)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := generator.GenerateSelectedSourcingGreetings(context.Background(), batchID)
	if err != nil {
		t.Fatal(err)
	}
	if !progress.Completed || progress.SelectedCount != 2 || progress.OKCount != 1 ||
		progress.FailedCount != 1 || progress.InFlightCount != 0 || progress.PendingCount != 0 ||
		progress.Provider != advice.ProviderName() || progress.Model != advice.ModelName() {
		t.Fatalf("招呼语批次没有完整终局: %+v", progress)
	}
	if len(advice.requests) != 2 {
		t.Fatalf("provider 调用次数错误: %d", len(advice.requests))
	}
	for _, request := range advice.requests {
		if request.Purpose != m5ai.PurposeGreeting || request.MaxOutputTokens != m5ai.GreetingOutputTokenLimit {
			t.Fatalf("招呼语请求越界: %+v", request)
		}
	}
	if len(h.sender.order) != beforeHandCalls {
		t.Fatalf("生成招呼语触碰了 hand: before=%d after=%d order=%v", beforeHandCalls, len(h.sender.order), h.sender.order)
	}

	replayed, err := generator.GenerateSelectedSourcingGreetings(context.Background(), batchID)
	if err != nil || !replayed.Completed || len(advice.requests) != 2 {
		t.Fatalf("重复生成产生了新调用: progress=%+v requests=%d err=%v", replayed, len(advice.requests), err)
	}
}

func TestSelectedSourcingBatchWithoutProviderCreatesNoGreetingReservation(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-a"}})
	batchID := prepareSelectedSourcingBatch(t, h, 1)
	beforeHandCalls := len(h.sender.order)

	generator, err := patrol.NewManager(
		h.store, PatrolRunner{Dispatcher: h.sender.dispatcher}, sourcingActorHands{},
		patrol.Config{Clock: h.clock, Location: time.UTC},
	)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := generator.GenerateSelectedSourcingGreetings(context.Background(), batchID)
	if progress == nil || progress.PendingCount != 1 || err != patrol.ErrSourcingGreetingProviderUnavailable {
		t.Fatalf("缺少 provider 未响亮拒绝: progress=%+v err=%v", progress, err)
	}
	progress, err = h.store.SourcingBatchGreetingProgress(batchID)
	if err != nil || progress.PendingCount != 1 || progress.InFlightCount != 0 || progress.Completed {
		t.Fatalf("缺少 provider 仍创建了预留: progress=%+v err=%v", progress, err)
	}
	if len(h.sender.order) != beforeHandCalls {
		t.Fatalf("缺少 provider 时触碰了 hand: before=%d after=%d order=%v", beforeHandCalls, len(h.sender.order), h.sender.order)
	}
}
