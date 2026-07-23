package patrol

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"recruithelper/client/service/internal/aitrace"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
)

const m5DryRunDataDirEnv = "RECRUITHELPER_M5_DRYRUN_DATA_DIR"

// realProviderDryRunExecutor adapts the production HTTP provider to the
// persisted patrol seam without making the production adapter depend on the
// patrol package. It intentionally exists only in this opt-in integration
// test.
type realProviderDryRunExecutor struct {
	provider     *m5ai.OpenAICompatibleProvider
	providerName string
	modelName    string
	purposes     []m5ai.CompletionPurpose
	responses    []m5ai.CompletionResponse
}

func (e *realProviderDryRunExecutor) ProviderName() string { return e.providerName }
func (e *realProviderDryRunExecutor) ModelName() string    { return e.modelName }

func (e *realProviderDryRunExecutor) CompleteJSON(
	ctx context.Context,
	request m5ai.CompletionRequest,
) (m5ai.CompletionResponse, error) {
	e.purposes = append(e.purposes, request.Purpose)
	response, err := e.provider.CompleteJSON(ctx, request)
	if err == nil {
		e.responses = append(e.responses, response)
	}
	return response, err
}

func TestM5RealProviderDryRun(t *testing.T) {
	dataDir := strings.TrimSpace(os.Getenv(m5DryRunDataDirEnv))
	if dataDir == "" {
		t.Skip("未显式启用 M5 真实 provider dry-run")
	}

	configStore, err := m5ai.NewProviderConfigStore(dataDir)
	if err != nil {
		t.Fatal("M5 真实 provider 配置入口无效")
	}
	config, err := configStore.Load()
	if err != nil || config == nil {
		t.Fatal("M5 真实 provider 配置尚未就绪")
	}
	traceStore, err := aitrace.Open(t.TempDir())
	if err != nil {
		t.Fatal("M5 真实 provider trace 存储未就绪")
	}
	defer traceStore.Close()
	provider, err := m5ai.NewOpenAICompatibleProvider(*config, nil, traceStore)
	if err != nil {
		t.Fatal("M5 真实 provider 配置未通过生产校验")
	}
	executor := &realProviderDryRunExecutor{
		provider: provider, providerName: config.Provider, modelName: config.Model,
	}

	// newHarness always opens a temporary Store and its fake runner does not
	// implement AutomaticReplyRunner. The real provider can therefore persist
	// intent/reply advice, but the turn must stop before any Chrome command.
	h := newHarness(t)
	fixture := seedM5AdviceFixture(t, h)
	h.manager.advice = executor
	beforeCommands, err := h.db.RecentCmds(100)
	if err != nil {
		t.Fatal("读取 dry-run 前命令计数失败")
	}

	actor := &roundActor{manager: h.manager, now: h.clock.Now()}
	h.manager.mu.Lock()
	err = actor.advanceM5Turn(context.Background(), fixture.turn)
	h.manager.mu.Unlock()
	if err != nil {
		t.Fatal("M5 真实 provider dry-run 编排失败，停止事实门")
	}

	invocations, err := h.db.AIInvocationsForTurn(fixture.turn.TurnID)
	if err != nil {
		t.Fatal("读取 M5 dry-run invocation 失败")
	}
	if len(executor.purposes) != 2 || len(invocations) != 2 ||
		executor.purposes[0] != m5ai.PurposeIntent || executor.purposes[1] != m5ai.PurposeReply {
		for index, response := range executor.responses {
			t.Logf("incomplete responseIndex=%d reasoningContentEmpty=%t", index, response.ReasoningContentEmpty)
		}
		for _, invocation := range invocations {
			reasoning := "absent"
			if invocation.ReasoningTokens != nil {
				reasoning = fmt.Sprintf("%d", *invocation.ReasoningTokens)
			}
			t.Logf(
				"incomplete purpose=%s status=%s errorClass=%s usageShape=%s reasoningTokens=%s inputTokens=%d cachedInputTokens=%d outputTokens=%d latencyMs=%d estimatedCostMicros=%d inputHash=%s outputHash=%s",
				invocation.Purpose, invocation.Status, invocation.ErrorClass, invocation.UsageShape,
				reasoning, invocation.InputTokens, invocation.CachedInputTokens, invocation.OutputTokens,
				invocation.LatencyMs, invocation.EstimatedCostMicros, invocation.InputHash, invocation.OutputHash,
			)
		}
		if stoppedTurn, readErr := h.db.DialogueTurnByID(fixture.turn.TurnID); readErr == nil && stoppedTurn != nil {
			t.Logf(
				"incomplete turnStatus=%s failureReason=%s intentLabel=%s intentSource=%s",
				stoppedTurn.Status, stoppedTurn.FailureReason, stoppedTurn.IntentLabel, stoppedTurn.IntentSource,
			)
		}
		t.Fatalf(
			"真实 provider 未形成 intent→reply 两次独立调用，停止事实门: calls=%v invocations=%d",
			executor.purposes, len(invocations),
		)
	}
	expectedPurposes := []m5ai.CompletionPurpose{m5ai.PurposeIntent, m5ai.PurposeReply}
	for index, invocation := range invocations {
		reasoning := "absent"
		usageSafe := invocation.UsageShape == store.AIInvocationReasoningFieldAbsent && invocation.ReasoningTokens == nil
		if invocation.ReasoningTokens != nil {
			reasoning = fmt.Sprintf("%d", *invocation.ReasoningTokens)
			usageSafe = invocation.UsageShape == store.AIInvocationUsageComplete && *invocation.ReasoningTokens == 0
		}
		if invocation.Purpose != expectedPurposes[index] || invocation.Attempt != 1 ||
			invocation.Provider != config.Provider || invocation.Model != config.Model ||
			invocation.Status != store.AIInvocationOK || invocation.FinishedAt == nil ||
			!usageSafe || len(executor.responses) != 2 || !executor.responses[index].ReasoningContentEmpty ||
			invocation.InputTokens <= 0 || invocation.OutputTokens <= 0 ||
			invocation.CachedInputTokens < 0 || invocation.CachedInputTokens > invocation.InputTokens ||
			invocation.EstimatedCostMicros <= 0 ||
			invocation.RequestBytes <= 0 || invocation.ResponseBytes <= 0 ||
			invocation.TraceStatus != m5ai.TraceStatusComplete ||
			strings.TrimSpace(invocation.InputHash) == "" || strings.TrimSpace(invocation.OutputHash) == "" {
			t.Fatal("真实 provider invocation 字段缺失、非法或 reasoning 非零，停止事实门")
		}
		trace, traceErr := traceStore.Get(context.Background(), invocation.InvocationID)
		if traceErr != nil || trace.InvocationID != invocation.InvocationID ||
			trace.Purpose != string(invocation.Purpose) ||
			trace.Provider != invocation.Provider || trace.Model != invocation.Model ||
			trace.ContextRevisionHash != invocation.ContextRevisionHash ||
			trace.PromptRevision != fixture.turn.RenderFormatVersion ||
			trace.TraceState != aitrace.TraceStateCompleted ||
			trace.HTTPStatus == nil || *trace.HTTPStatus != 200 ||
			!trace.ResponsePresent || trace.RequestBytes != int64(invocation.RequestBytes) ||
			trace.ResponseBytes != int64(invocation.ResponseBytes) ||
			len(trace.ConfigHash) != 64 || bytes.Contains(trace.RequestJSON, []byte(config.APIKey)) {
			t.Fatal("真实 provider 的 brain invocation 与原文 trace 未以同一 invocationId 收敛")
		}
		t.Logf(
			"purpose=%s provider=%s model=%s inputTokens=%d cachedInputTokens=%d outputTokens=%d usageShape=%s reasoningTokens=%s reasoningContentEmpty=%t latencyMs=%d estimatedCostMicros=%d inputHash=%s outputHash=%s",
			invocation.Purpose, invocation.Provider, invocation.Model,
			invocation.InputTokens, invocation.CachedInputTokens, invocation.OutputTokens,
			invocation.UsageShape, reasoning, executor.responses[index].ReasoningContentEmpty,
			invocation.LatencyMs, invocation.EstimatedCostMicros,
			invocation.InputHash, invocation.OutputHash,
		)
	}

	action, err := h.db.CommunicationActionByTurn(fixture.turn.TurnID)
	if err != nil || action == nil || action.Status != store.CommunicationActionPlanned || action.EffectIntentID != nil {
		t.Fatal("真实 provider 建议未停在唯一 planned action，停止事实门")
	}
	turn, err := h.db.DialogueTurnByID(fixture.turn.TurnID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnAdviceReady {
		t.Fatal("真实 provider dry-run 未停在 adviceReady，停止事实门")
	}
	afterCommands, err := h.db.RecentCmds(100)
	if err != nil || len(afterCommands) != len(beforeCommands) || len(h.runner.names()) != 0 {
		t.Fatal("真实 provider dry-run 产生了 Chrome 命令，停止事实门")
	}
}
