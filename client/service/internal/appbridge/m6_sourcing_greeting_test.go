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

type sourcingBatchGreetingAllSuccessAdvice struct {
	requests []m5ai.CompletionRequest
}

func (*sourcingBatchGreetingAllSuccessAdvice) ProviderName() string {
	return "fixture-greeting-all-success-provider"
}
func (*sourcingBatchGreetingAllSuccessAdvice) ModelName() string {
	return "fixture-greeting-all-success-model"
}
func (a *sourcingBatchGreetingAllSuccessAdvice) CompleteJSON(
	_ context.Context,
	request m5ai.CompletionRequest,
) (m5ai.CompletionResponse, error) {
	a.requests = append(a.requests, request)
	zero := 0
	return m5ai.CompletionResponse{
		JSONText: fmt.Sprintf(
			`{"招呼语":"你好，这是页面续扫测试中的第 %d 条招呼。"}`,
			len(a.requests),
		),
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

func TestSelectedSourcingGreetingPostResponseTokenBudgetKeepsUsageWithoutText(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-a"}})
	batchID := prepareSelectedSourcingBatch(t, h, 1)
	revision, err := h.store.SourcingGreetingRevision(batchID)
	if err != nil || revision == nil {
		t.Fatalf("招呼前缺少配置: revision=%+v err=%v", revision, err)
	}
	material, err := h.store.NextSelectedSourcingGreetingMaterial(batchID, revision.RevisionHash)
	if err != nil || material == nil {
		t.Fatalf("招呼前缺少 selected 材料: material=%+v err=%v", material, err)
	}
	beforeHandCalls := len(h.sender.order)
	zero := 0
	usage := m5ai.CompletionUsage{
		InputTokens:       m5ai.GreetingInputTokenLimit + 1,
		CachedInputTokens: 401,
		OutputTokens:      9,
		ReasoningTokens:   &zero,
	}
	advice := &postResponseInputBudgetAdvice{
		response: m5ai.CompletionResponse{
			JSONText: `{"招呼语":"不得持久化"}`, Usage: usage, ReasoningContentEmpty: true,
		},
		delay: 2 * time.Millisecond,
	}
	generator, err := patrol.NewManager(
		h.store, PatrolRunner{Dispatcher: h.sender.dispatcher}, sourcingActorHands{},
		patrol.Config{Clock: h.clock, Location: time.UTC}, advice,
	)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := generator.GenerateSelectedSourcingGreetings(context.Background(), batchID)
	if err != nil || !progress.Completed || progress.OKCount != 0 || progress.FailedCount != 1 ||
		progress.InputTokens != int64(usage.InputTokens) ||
		progress.CachedInputTokens != int64(usage.CachedInputTokens) ||
		progress.OutputTokens != int64(usage.OutputTokens) ||
		progress.EstimatedCostMicros != m5ai.EstimatedCostMicros(usage) ||
		len(advice.requests) != 1 {
		t.Fatalf("超 token 招呼未形成带计量单次失败终局: progress=%+v calls=%d err=%v",
			progress, len(advice.requests), err)
	}
	invocation, err := h.store.SourcingGreetingByProfileID(material.ProfileID)
	if err != nil || invocation == nil ||
		invocation.Status != store.AIInvocationBudgetBlocked ||
		invocation.ErrorClass != "inputTokenBudgetExceeded" ||
		invocation.GreetingText != "" || invocation.ContentHash != "" ||
		invocation.OutputHash == "" ||
		invocation.InputTokens != usage.InputTokens ||
		invocation.CachedInputTokens != usage.CachedInputTokens ||
		invocation.OutputTokens != usage.OutputTokens ||
		invocation.UsageShape != store.AIInvocationUsageComplete ||
		invocation.ReasoningTokens == nil || *invocation.ReasoningTokens != 0 ||
		invocation.LatencyMs < 1 ||
		invocation.EstimatedCostMicros != m5ai.EstimatedCostMicros(usage) {
		t.Fatalf("超 token 招呼计量或零正文事实错误: invocation=%+v err=%v", invocation, err)
	}
	target, err := h.store.NextSourcingGreetingSendTarget(batchID)
	if err != nil || target != nil {
		t.Fatalf("超 token 招呼不得形成发送目标: target=%+v err=%v", target, err)
	}
	if len(h.sender.order) != beforeHandCalls {
		t.Fatalf("超 token 招呼生成触碰了 hand: before=%d after=%d", beforeHandCalls, len(h.sender.order))
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
) (*patrol.Manager, string, *store.SourcingGreetingSendTarget, *int) {
	t.Helper()
	manager, batchID, plan, paceWaits := prepareGeneratedSourcingGreetings(t, h, 1)
	if len(plan.Targets) != 1 {
		t.Fatalf("单人正式招呼目标数量错误: %+v", plan)
	}
	target := plan.Targets[0]
	return manager, batchID, &target, paceWaits
}

func prepareGeneratedSourcingGreetings(
	t *testing.T,
	h *sourcingActorHarness,
	targetCount int,
) (*patrol.Manager, string, *store.SourcingGreetingSendScanPlan, *int) {
	t.Helper()
	batchID := prepareSelectedSourcingBatch(t, h, targetCount)
	advice := &sourcingBatchGreetingAllSuccessAdvice{}
	paceWaits := 0
	manager, err := patrol.NewManager(
		h.store, PatrolRunner{Dispatcher: h.sender.dispatcher}, sourcingActorHands{},
		patrol.Config{
			Clock: h.clock, Location: time.UTC, MaxPages: 4,
			InteractionPaceWait: func(ctx context.Context) error {
				return ctx.Err()
			},
			SourcingPaceWait: func(ctx context.Context) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				paceWaits++
				return nil
			},
		}, advice,
	)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := manager.GenerateSelectedSourcingGreetings(context.Background(), batchID)
	if err != nil || !progress.Completed || progress.OKCount != int64(targetCount) ||
		len(advice.requests) != targetCount {
		t.Fatalf("正式招呼语批次未完成: progress=%+v calls=%d err=%v",
			progress, len(advice.requests), err)
	}
	plan, err := h.store.SourcingGreetingSendScanPlan(batchID)
	if err != nil || plan == nil || len(plan.Targets) != targetCount {
		t.Fatalf("缺少未绑定发送续扫投影: plan=%+v err=%v", plan, err)
	}
	return manager, batchID, plan, &paceWaits
}

func sourcingGreetingPlatformRefs(sender *sourcingActorSender) []string {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	refs := make([]string, len(sender.greetings))
	for i := range sender.greetings {
		refs[i] = sender.greetings[i].args.PlatformUserRef
	}
	return refs
}

func TestSelectedSourcingGreetingLocatesByResetThenNextAndReplaysCompletedBatch(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-selected"}})
	manager, batchID, target, paceWaits := prepareGeneratedSourcingGreeting(t, h)

	h.sender.windows = [][]string{{"other-visible-candidate"}, {target.PlatformUserRef}}
	h.sender.window = 1
	h.sender.moves = nil
	progress, err := manager.SendSelectedSourcingGreetings(context.Background(), batchID)
	if err != nil || !progress.Completed || progress.SentCount != 1 || progress.PendingCount != 0 ||
		progress.InFlightCount != 0 || progress.SuspectCount != 0 {
		t.Fatalf("列表直接招呼未成功收敛: progress=%+v err=%v", progress, err)
	}
	if got, want := fmt.Sprint(h.sender.moves), fmt.Sprint([]protocol.SourcingWindowMove{
		protocol.SourcingWindowMoveReset,
		protocol.SourcingWindowMoveNext,
		protocol.SourcingWindowMoveCurrent,
	}); got != want {
		t.Fatalf("定位必须固定 reset→next: got=%s want=%s", got, want)
	}
	if h.sender.greetingCount() != 1 {
		t.Fatalf("应只调用一次 chat.sendGreeting: %d", h.sender.greetingCount())
	}
	if *paceWaits != 1 {
		t.Fatalf("全新自动招呼必须恰好等待一次: %d", *paceWaits)
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
	if *paceWaits != 1 {
		t.Fatalf("完成态重放不得再次等待: %d", *paceWaits)
	}
}

func TestSelectedSourcingGreetingRefreshesStaleSessionOnSamePluginBoot(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-selected"}})
	manager, batchID, _, _ := prepareGeneratedSourcingGreeting(t, h)
	if err := h.store.MutateAccount(h.key, func(account *store.Account) error {
		account.IdentitySession = "session-before-brain-restart"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := len(h.sender.order)

	progress, err := manager.SendSelectedSourcingGreetings(context.Background(), batchID)
	if err != nil || progress == nil || !progress.Completed || progress.SentCount != 1 {
		t.Fatalf("同 boot 重连后未恢复招呼发送: progress=%+v err=%v", progress, err)
	}
	if got := h.sender.order[before:]; len(got) == 0 || got[0] != protocol.PrimProbePlatform {
		t.Fatalf("同 boot 重连没有先做只读身份探针: %v", got)
	}
	account, err := h.store.AccountByKey(h.key)
	if err != nil || account == nil ||
		account.IdentitySession != "session-sourcing-actor" ||
		account.IdentityBootID != "boot-sourcing-actor" {
		t.Fatalf("身份会话未更新到当前代际: account=%+v err=%v", account, err)
	}
}

func TestSelectedSourcingGreetingDoesNotRepairPluginBootChange(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-selected"}})
	manager, batchID, _, _ := prepareGeneratedSourcingGreeting(t, h)
	if err := h.store.MutateAccount(h.key, func(account *store.Account) error {
		account.IdentitySession = "session-before-plugin-reload"
		account.IdentityBootID = "boot-before-plugin-reload"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := len(h.sender.order)

	progress, err := manager.SendSelectedSourcingGreetings(context.Background(), batchID)
	if !errors.Is(err, store.ErrAccountIdentityNotCurrent) ||
		progress == nil || progress.SentCount != 0 || progress.PendingCount != 1 {
		t.Fatalf("plugin boot 变化未保守停止: progress=%+v err=%v", progress, err)
	}
	if got := h.sender.order[before:]; len(got) != 0 {
		t.Fatalf("plugin boot 变化仍触碰页面: %v", got)
	}
	if h.sender.greetingCount() != 0 {
		t.Fatalf("plugin boot 变化仍创建招呼动作: %d", h.sender.greetingCount())
	}
}

func TestSelectedSourcingGreetingRejectsReconnectProbeFingerprintMismatch(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-selected"}})
	manager, batchID, _, _ := prepareGeneratedSourcingGreeting(t, h)
	if err := h.store.MutateAccount(h.key, func(account *store.Account) error {
		account.IdentitySession = "session-before-brain-restart"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.sender.probeFingerprint = "different-principal"
	before := len(h.sender.order)

	progress, err := manager.SendSelectedSourcingGreetings(context.Background(), batchID)
	if !errors.Is(err, store.ErrAccountIdentityNotCurrent) ||
		progress == nil || progress.SentCount != 0 || progress.PendingCount != 1 {
		t.Fatalf("指纹冲突未保守停止: progress=%+v err=%v", progress, err)
	}
	if got := h.sender.order[before:]; len(got) != 1 || got[0] != protocol.PrimProbePlatform {
		t.Fatalf("指纹冲突触碰了探针以外页面动作: %v", got)
	}
	if h.sender.greetingCount() != 0 {
		t.Fatalf("指纹冲突仍创建招呼动作: %d", h.sender.greetingCount())
	}
	account, accountErr := h.store.AccountByKey(h.key)
	if accountErr != nil || account == nil ||
		account.IdentitySession != "session-before-brain-restart" {
		t.Fatalf("指纹冲突仍更新身份: account=%+v err=%v", account, accountErr)
	}
}

func TestSelectedSourcingGreetingsScanOnceAndSendInCurrentPageOrder(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{
		{"candidate-a", "candidate-b"},
		{"candidate-b", "candidate-c"},
	})
	manager, batchID, plan, paceWaits := prepareGeneratedSourcingGreetings(t, h, 3)
	byRef := make(map[string]store.SourcingGreetingSendTarget, len(plan.Targets))
	for _, target := range plan.Targets {
		byRef[target.PlatformUserRef] = target
	}
	for _, ref := range []string{"candidate-a", "candidate-b", "candidate-c"} {
		if _, ok := byRef[ref]; !ok {
			t.Fatalf("续扫投影缺少候选人 %q: %+v", ref, plan)
		}
	}

	h.sender.windows = [][]string{
		{"other-visible-a", "candidate-b", "candidate-a"},
		{"other-visible-b", "candidate-c"},
	}
	h.sender.window = 1
	h.sender.moves = nil
	progress, err := manager.SendSelectedSourcingGreetings(context.Background(), batchID)
	if err != nil || progress == nil || !progress.Completed ||
		progress.SentCount != 3 || progress.PendingCount != 0 {
		t.Fatalf("页面顺序续扫未完整发送: progress=%+v err=%v", progress, err)
	}
	if got, want := fmt.Sprint(h.sender.moves), fmt.Sprint([]protocol.SourcingWindowMove{
		protocol.SourcingWindowMoveReset,
		protocol.SourcingWindowMoveCurrent,
		protocol.SourcingWindowMoveCurrent,
		protocol.SourcingWindowMoveNext,
		protocol.SourcingWindowMoveCurrent,
	}); got != want {
		t.Fatalf("批量招呼不应为每个目标回到顶部: got=%s want=%s", got, want)
	}
	if got, want := fmt.Sprint(sourcingGreetingPlatformRefs(h.sender)),
		fmt.Sprint([]string{"candidate-b", "candidate-a", "candidate-c"}); got != want {
		t.Fatalf("发送顺序没有服从当前页面顺序: got=%s want=%s", got, want)
	}
	if *paceWaits != 3 {
		t.Fatalf("三个全新候选人必须分别等待一次: %d", *paceWaits)
	}
}

func TestSelectedSourcingGreetingsRecheckCurrentWindowBeforeCreatingNextIntent(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-a", "candidate-b"}})
	manager, batchID, plan, _ := prepareGeneratedSourcingGreetings(t, h, 2)
	var candidateB store.SourcingGreetingSendTarget
	for _, target := range plan.Targets {
		if target.PlatformUserRef == "candidate-b" {
			candidateB = target
			break
		}
	}
	if candidateB.InvocationID == "" {
		t.Fatalf("续扫投影缺少 candidate-b: %+v", plan)
	}
	h.sender.windows = [][]string{{"candidate-a", "candidate-b"}}
	h.sender.window = 0
	h.sender.moves = nil
	h.sender.afterFirstGreeting = [][]string{{"candidate-a"}}

	progress, err := manager.SendSelectedSourcingGreetings(context.Background(), batchID)
	if !errors.Is(err, patrol.ErrSourcingGreetingTargetNotFound) || progress == nil ||
		progress.SentCount != 1 || progress.PendingCount != 1 ||
		progress.InFlightCount != 0 || progress.FailedCount != 0 {
		t.Fatalf("重排后消失目标没有保持 pending: progress=%+v err=%v", progress, err)
	}
	if got, want := fmt.Sprint(sourcingGreetingPlatformRefs(h.sender)),
		fmt.Sprint([]string{"candidate-a"}); got != want {
		t.Fatalf("重排后仍尝试了已消失目标: got=%s want=%s", got, want)
	}
	if got, want := fmt.Sprint(h.sender.moves), fmt.Sprint([]protocol.SourcingWindowMove{
		protocol.SourcingWindowMoveReset,
		protocol.SourcingWindowMoveCurrent,
		protocol.SourcingWindowMoveCurrent,
		protocol.SourcingWindowMoveNext,
		protocol.SourcingWindowMoveReset,
		protocol.SourcingWindowMoveNext,
	}); got != want {
		t.Fatalf("重排复核或顶部兜底命令序列错误: got=%s want=%s", got, want)
	}
	intentID, err := store.SourcingGreetingEffectIntentID(candidateB.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := h.store.EffectIntentByID(intentID)
	if err != nil || intent != nil {
		t.Fatalf("已消失目标不应形成 WAL intent: intent=%+v err=%v", intent, err)
	}
}

func TestSelectedSourcingGreetingMissingTargetCreatesNoEffect(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-selected"}})
	manager, batchID, target, _ := prepareGeneratedSourcingGreeting(t, h)
	h.sender.windows = [][]string{{"other-visible-candidate"}}
	h.sender.window = 0
	h.sender.moves = nil

	progress, err := manager.SendSelectedSourcingGreetings(context.Background(), batchID)
	if !errors.Is(err, patrol.ErrSourcingGreetingTargetNotFound) || progress == nil ||
		progress.PendingCount != 1 || progress.InFlightCount != 0 || progress.SentCount != 0 {
		t.Fatalf("未定位目标没有保守停止: progress=%+v err=%v", progress, err)
	}
	if got, want := fmt.Sprint(h.sender.moves), fmt.Sprint([]protocol.SourcingWindowMove{
		protocol.SourcingWindowMoveReset,
		protocol.SourcingWindowMoveNext,
		protocol.SourcingWindowMoveReset,
		protocol.SourcingWindowMoveNext,
	}); got != want {
		t.Fatalf("漏项应且仅应执行一次顶部兜底: got=%s want=%s", got, want)
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

func TestSelectedSourcingGreetingsStopOneWindowAfterCapturedTail(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{
		{"candidate-a", "candidate-b"},
		{"candidate-b", "candidate-c"},
	})
	manager, batchID, _, _ := prepareGeneratedSourcingGreetings(t, h, 3)
	h.sender.windows = [][]string{
		{"candidate-a", "candidate-c"},
		{"one-window-after-tail"},
		{"candidate-b"},
	}
	h.sender.window = 2
	h.sender.moves = nil

	progress, err := manager.SendSelectedSourcingGreetings(context.Background(), batchID)
	if !errors.Is(err, patrol.ErrSourcingGreetingTargetNotFound) || progress == nil ||
		progress.SentCount != 2 || progress.PendingCount != 1 {
		t.Fatalf("尾锚边界没有留下超过一窗的漏项: progress=%+v err=%v", progress, err)
	}
	if got, want := fmt.Sprint(h.sender.moves), fmt.Sprint([]protocol.SourcingWindowMove{
		protocol.SourcingWindowMoveReset,
		protocol.SourcingWindowMoveCurrent,
		protocol.SourcingWindowMoveCurrent,
		protocol.SourcingWindowMoveNext,
		protocol.SourcingWindowMoveReset,
		protocol.SourcingWindowMoveNext,
	}); got != want {
		t.Fatalf("尾锚后扫描超过一窗或兜底次数错误: got=%s want=%s", got, want)
	}
	if got, want := fmt.Sprint(sourcingGreetingPlatformRefs(h.sender)),
		fmt.Sprint([]string{"candidate-a", "candidate-c"}); got != want {
		t.Fatalf("尾锚边界内的发送集合错误: got=%s want=%s", got, want)
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
	manager, batchID, target, _ := prepareGeneratedSourcingGreeting(t, h)
	h.sender.windows = [][]string{{target.PlatformUserRef}}
	h.sender.window = 0
	h.sender.moves = nil
	h.sender.mu.Lock()
	h.sender.holdGreeting = true
	h.sender.mu.Unlock()

	type sendResult struct {
		progress *store.SourcingBatchGreetingSendProgress
		err      error
	}
	firstCtx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan sendResult, 1)
	go func() {
		progress, err := manager.SendSelectedSourcingGreetings(firstCtx, batchID)
		firstDone <- sendResult{progress: progress, err: err}
	}()
	deadline := time.After(2 * time.Second)
	for h.sender.greetingCount() != 1 {
		select {
		case <-deadline:
			t.Fatal("首次招呼未进入已持久化 WAL")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	firstResult := <-firstDone
	first, firstErr := firstResult.progress, firstResult.err
	if !errors.Is(firstErr, context.Canceled) || first == nil || first.InFlightCount != 1 {
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
