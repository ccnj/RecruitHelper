package appbridge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/patrol"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
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

func prepareGeneratedSourcingGreeting(
	t *testing.T,
	h *sourcingActorHarness,
) (*patrol.Manager, string, *store.SourcingGreetingSendTarget) {
	t.Helper()
	batchID := prepareSelectedSourcingBatch(t, h, 1)
	advice := &sourcingBatchGreetingAdvice{}
	manager, err := patrol.NewManager(
		h.store, PatrolRunner{Dispatcher: h.sender.dispatcher}, sourcingActorHands{},
		patrol.Config{Clock: h.clock, Location: time.UTC, MaxPages: 4}, advice,
	)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := manager.GenerateSelectedSourcingGreetings(context.Background(), batchID)
	if err != nil || !progress.Completed || progress.OKCount != 1 {
		t.Fatalf("单人正式招呼语未完成: progress=%+v err=%v", progress, err)
	}
	target, err := h.store.NextSourcingGreetingSendTarget(batchID)
	if err != nil || target == nil || target.EffectIntentID != nil {
		t.Fatalf("缺少未绑定发送目标: target=%+v err=%v", target, err)
	}
	return manager, batchID, target
}

func TestSelectedSourcingGreetingLocatesByResetThenNextAndReplaysCompletedBatch(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-selected"}})
	manager, batchID, target := prepareGeneratedSourcingGreeting(t, h)

	h.sender.windows = [][]string{{"other-visible-candidate"}, {target.PlatformUserRef}}
	h.sender.window = 1
	h.sender.moves = nil
	progress, err := manager.SendSelectedSourcingGreetings(context.Background(), batchID)
	if err != nil || !progress.Completed || progress.SentCount != 1 || progress.PendingCount != 0 ||
		progress.InFlightCount != 0 || progress.SuspectCount != 0 {
		t.Fatalf("列表直接招呼未成功收敛: progress=%+v err=%v", progress, err)
	}
	if got, want := fmt.Sprint(h.sender.moves), fmt.Sprint([]protocol.SourcingWindowMove{
		protocol.SourcingWindowMoveReset, protocol.SourcingWindowMoveNext,
	}); got != want {
		t.Fatalf("定位必须固定 reset→next: got=%s want=%s", got, want)
	}
	if h.sender.greetingCount() != 1 {
		t.Fatalf("应只调用一次 chat.sendGreeting: %d", h.sender.greetingCount())
	}

	beforeMoves, beforeGreetings := len(h.sender.moves), h.sender.greetingCount()
	replayed, err := manager.SendSelectedSourcingGreetings(context.Background(), batchID)
	if err != nil || !replayed.Completed || replayed.SentCount != 1 {
		t.Fatalf("完成态重放失败: progress=%+v err=%v", replayed, err)
	}
	if len(h.sender.moves) != beforeMoves || h.sender.greetingCount() != beforeGreetings {
		t.Fatalf("完成态重复调用仍读列表或发送: moves=%d/%d greetings=%d/%d",
			len(h.sender.moves), beforeMoves, h.sender.greetingCount(), beforeGreetings)
	}
}

func TestSelectedSourcingGreetingMissingTargetCreatesNoEffect(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-selected"}})
	manager, batchID, target := prepareGeneratedSourcingGreeting(t, h)
	h.sender.windows = [][]string{{"other-visible-candidate"}}
	h.sender.window = 0
	h.sender.moves = nil

	progress, err := manager.SendSelectedSourcingGreetings(context.Background(), batchID)
	if !errors.Is(err, patrol.ErrSourcingGreetingWindowStopped) || progress == nil ||
		progress.PendingCount != 1 || progress.InFlightCount != 0 || progress.SentCount != 0 {
		t.Fatalf("未定位目标没有保守停止: progress=%+v err=%v", progress, err)
	}
	if h.sender.greetingCount() != 0 {
		t.Fatalf("未定位目标仍调用了 effect runner: %d", h.sender.greetingCount())
	}
	intentID, err := store.SourcingGreetingEffectIntentID(target.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := h.store.EffectIntentByID(intentID)
	if err != nil || intent != nil {
		t.Fatalf("未定位目标仍形成 effect intent: intent=%+v err=%v", intent, err)
	}
}

type greetingStartSignalRunner struct {
	PatrolRunner
	started chan struct{}
	once    sync.Once
}

func (r *greetingStartSignalRunner) StartAutomaticGreeting(
	ctx context.Context,
	req patrol.AutomaticGreetingRequest,
) (patrol.AutomaticGreetingHandle, error) {
	handle, err := r.PatrolRunner.StartAutomaticGreeting(ctx, req)
	r.once.Do(func() { close(r.started) })
	return handle, err
}

type sourcingOfflineHands struct {
	mu    sync.Mutex
	calls int
}

func (h *sourcingOfflineHands) State(context.Context, string) (patrol.HandState, error) {
	h.mu.Lock()
	h.calls++
	h.mu.Unlock()
	return patrol.HandState{}, nil
}

func (h *sourcingOfflineHands) callCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

func TestBoundSourcingGreetingSkipsRelocationAndConvergesAfterContextLoss(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-selected"}})
	manager, batchID, target := prepareGeneratedSourcingGreeting(t, h)
	h.sender.windows = [][]string{{target.PlatformUserRef}}
	h.sender.window = 0
	h.sender.moves = nil
	h.sender.mu.Lock()
	h.sender.holdGreeting = true
	h.sender.mu.Unlock()

	firstCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	first, firstErr := manager.SendSelectedSourcingGreetings(firstCtx, batchID)
	cancel()
	if !errors.Is(firstErr, context.DeadlineExceeded) || first == nil || first.InFlightCount != 1 {
		t.Fatalf("首次在途招呼未留下唯一可收编 WAL: progress=%+v err=%v", first, firstErr)
	}
	if h.sender.greetingCount() != 1 {
		t.Fatalf("首次在途招呼发送次数错误: %d", h.sender.greetingCount())
	}
	movesAfterBinding := len(h.sender.moves)

	h.sender.setOnline(false)
	offlineHands := &sourcingOfflineHands{}
	signalRunner := &greetingStartSignalRunner{
		PatrolRunner: PatrolRunner{Dispatcher: h.sender.dispatcher},
		started:      make(chan struct{}),
	}
	restarted, err := patrol.NewManager(
		h.store, signalRunner, offlineHands,
		patrol.Config{Clock: h.clock, Location: time.UTC, MaxPages: 4},
	)
	if err != nil {
		t.Fatal(err)
	}
	type sendResult struct {
		progress *store.SourcingBatchGreetingSendProgress
		err      error
	}
	done := make(chan sendResult, 1)
	go func() {
		progress, err := restarted.SendSelectedSourcingGreetings(context.Background(), batchID)
		done <- sendResult{progress: progress, err: err}
	}()
	select {
	case <-signalRunner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("绑定来源没有进入同一 WAL 收编")
	}
	if err := h.sender.completeHeldGreeting(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil || result.progress == nil || !result.progress.Completed || result.progress.SentCount != 1 {
			t.Fatalf("context 丢失后未收编同一 WAL: progress=%+v err=%v", result.progress, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("context 丢失后的 WAL 收编未结束")
	}
	if len(h.sender.moves) != movesAfterBinding || h.sender.greetingCount() != 1 {
		t.Fatalf("绑定来源重跑发生重新定位或二次发送: moves=%d/%d greetings=%d",
			len(h.sender.moves), movesAfterBinding, h.sender.greetingCount())
	}
	if offlineHands.callCount() != 0 {
		t.Fatalf("绑定来源仍要求已丢失的页面代际: handStateCalls=%d", offlineHands.callCount())
	}
}
