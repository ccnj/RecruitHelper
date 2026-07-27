package appbridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/patrol"
	"recruithelper/client/service/internal/store"
)

func TestGreetingRateLimitedRetriesThenResumeAfterCancel(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-a"}})
	batchID := prepareSelectedSourcingBatch(t, h, 1)
	revision, err := h.store.SourcingGreetingRevision(batchID)
	if err != nil || revision == nil {
		t.Fatalf("缺少招呼语配置: revision=%+v err=%v", revision, err)
	}
	work, err := h.store.PendingSourcingGreetingWork(batchID, revision.RevisionHash)
	if err != nil || len(work) != 1 || work[0].Invocation != nil {
		t.Fatalf("生成前待驱动成员错误: work=%+v err=%v", work, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	advice := &sourcingBatchGreetingAdvice{
		respond: func(_ string, attempt int) (m5ai.CompletionResponse, error) {
			switch attempt {
			case 1:
				return m5ai.CompletionResponse{}, &m5ai.ProviderError{
					Class: "rateLimited", FailureStage: m5ai.FailureStageProviderHTTP,
					DetailCode: "rateLimited",
				}
			case 2:
				cancel()
				return m5ai.CompletionResponse{}, &m5ai.ProviderError{
					Class: "rateLimited", FailureStage: m5ai.FailureStageProviderHTTP,
					DetailCode: "rateLimited",
				}
			default:
				return greetingFixtureResponse(`{"招呼语":"你好，方便聊聊这个职位吗？"}`), nil
			}
		},
	}
	generator, err := patrol.NewManager(
		h.store, PatrolRunner{Dispatcher: h.sender.dispatcher}, sourcingActorHands{},
		patrol.Config{Clock: h.clock, Location: time.UTC, SourcingAIRetryWait: noSourcingAIRetryWait},
		advice,
	)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := generator.GenerateSelectedSourcingGreetings(ctx, batchID)
	if !errors.Is(err, context.Canceled) || progress == nil || progress.InFlightCount != 1 {
		t.Fatalf("取消未保留 inFlight: progress=%+v err=%v", progress, err)
	}
	invocation, err := h.store.SourcingGreetingByProfileID(work[0].Material.ProfileID)
	if err != nil || invocation == nil || invocation.FinishedAt != nil ||
		invocation.AttemptCount != 2 || invocation.BudgetedAttemptCount != 1 {
		t.Fatalf("取消后的预留形态错误(429 不占预算): invocation=%+v err=%v", invocation, err)
	}

	resumedWork, err := h.store.PendingSourcingGreetingWork(batchID, revision.RevisionHash)
	if err != nil || len(resumedWork) != 1 || resumedWork[0].Invocation == nil {
		t.Fatalf("inFlight 成员未进入续驱动清单: work=%+v err=%v", resumedWork, err)
	}
	progress, err = generator.GenerateSelectedSourcingGreetings(context.Background(), batchID)
	if err != nil || !progress.Completed || progress.OKCount != 1 {
		t.Fatalf("续驱动未成功终局: progress=%+v err=%v", progress, err)
	}
	invocation, err = h.store.SourcingGreetingByProfileID(work[0].Material.ProfileID)
	if err != nil || invocation == nil || invocation.Status != store.AIInvocationOK ||
		invocation.GreetingText == "" ||
		invocation.AttemptCount != 3 || invocation.BudgetedAttemptCount != 2 {
		t.Fatalf("续驱动计数或正文错误: invocation=%+v err=%v", invocation, err)
	}
	requests := advice.requestsSnapshot()
	if len(requests) != 3 || strings.Contains(requests[0].InvocationID, "#a") ||
		!strings.HasSuffix(requests[1].InvocationID, "#a2") ||
		!strings.HasSuffix(requests[2].InvocationID, "#a3") {
		t.Fatalf("attempt 追踪身份错误: %+v", requests)
	}
}

func TestGreetingParseFailureRetriesWithinBudget(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-a"}})
	batchID := prepareSelectedSourcingBatch(t, h, 1)
	advice := &sourcingBatchGreetingAdvice{
		respond: func(_ string, attempt int) (m5ai.CompletionResponse, error) {
			if attempt <= 2 {
				return greetingFixtureResponse(`{"错误键":"不可解析"}`), nil
			}
			return greetingFixtureResponse(`{"招呼语":"你好，想和你聊聊这个机会。"}`), nil
		},
	}
	generator, err := patrol.NewManager(
		h.store, PatrolRunner{Dispatcher: h.sender.dispatcher}, sourcingActorHands{},
		patrol.Config{Clock: h.clock, Location: time.UTC, SourcingAIRetryWait: noSourcingAIRetryWait},
		advice,
	)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := generator.GenerateSelectedSourcingGreetings(context.Background(), batchID)
	if err != nil || !progress.Completed || progress.OKCount != 1 || progress.FailedCount != 0 {
		t.Fatalf("解析失败重试后未成功: progress=%+v err=%v", progress, err)
	}
	if advice.requestCount() != 3 {
		t.Fatalf("解析失败重试次数错误: %d", advice.requestCount())
	}
}
