package patrol

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"recruithelper/client/service/internal/m5ai"
)

// 2026-07-28 并行重试裁决：评分与招呼语生成按并发池驱动，错误分三档。
// 非 429 失败共享"初次+3 次重试"的持久预算；429 重试次数无限但受
// ctx/成员闸约束。发送阶段不经过本文件。
const (
	sourcingAIPoolSize             = 20
	sourcingAIBudgetedAttemptLimit = 4
	sourcingAIBudgetedWaitCap      = 4 * time.Second
	sourcingAIUnlimitedWaitCap     = 60 * time.Second
	sourcingAIRateLimitAlertEvery  = 5
)

type sourcingAIRetryClass int

const (
	sourcingAIRetryNone sourcingAIRetryClass = iota
	sourcingAIRetryBudgeted
	sourcingAIRetryUnlimited
)

// classifySourcingAIFailure 只按 provider 错误类分档；ctx 取消引发的失败
// 由调用方在分类前用 ctx.Err() 分流，不消耗预算。
func classifySourcingAIFailure(callErr error) sourcingAIRetryClass {
	var providerErr *m5ai.ProviderError
	if !errors.As(callErr, &providerErr) {
		return sourcingAIRetryBudgeted
	}
	switch providerErr.Class {
	case "rateLimited":
		return sourcingAIRetryUnlimited
	case "timeout", "transport", "providerUnavailable", "responseInvalid":
		return sourcingAIRetryBudgeted
	default:
		// requestInvalid/requestPayloadTooLarge/budgetBlocked/
		// inputTokenBudgetExceeded/authentication/providerRejected：
		// 确定性拒绝，重试无意义。
		return sourcingAIRetryNone
	}
}

func defaultSourcingAIRetryWait(ctx context.Context, unlimited bool, retrySequence int) error {
	if retrySequence < 1 {
		retrySequence = 1
	}
	shift := retrySequence - 1
	if shift > 6 {
		shift = 6
	}
	wait := time.Second << shift
	waitCap := sourcingAIBudgetedWaitCap
	if unlimited {
		waitCap = sourcingAIUnlimitedWaitCap
	}
	if wait > waitCap {
		wait = waitCap
	}
	// ±20% 抖动，避免整批重试同步打点。
	wait = wait*4/5 + time.Duration(rand.Int64N(int64(wait*2/5)+1))
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// runSourcingAIMemberPool 以固定并发上限驱动一批成员。派发每个成员前
// 检查 ctx 与工作流成员闸；闸拒绝、ctx 取消或任一成员返回错误即停止
// 派发，等待在飞成员收束后返回首个错误。drive 对闸中断/取消应返回
// nil 或闸错误并保留 inFlight，不得写终局。
func (m *Manager) runSourcingAIMemberPool(
	ctx context.Context,
	count int,
	drive func(index int) error,
) error {
	sem := make(chan struct{}, sourcingAIPoolSize)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	setErr := func(err error) {
		if err == nil {
			return
		}
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}
	hasErr := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return firstErr != nil
	}
	for i := 0; i < count; i++ {
		if ctx.Err() != nil || hasErr() {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		if hasErr() {
			<-sem
			break
		}
		if err := m.mayStartNextWorkflowMember(); err != nil {
			<-sem
			setErr(err)
			break
		}
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			defer func() { <-sem }()
			setErr(drive(index))
		}(i)
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	return firstErr
}

// sourcingAIAttemptID 给第 attempt 次真实 HTTP 尝试派生追踪身份。首次
// 沿用裸 invocationID（与单次成功场景的既有形态一致），后续加 attempt
// 后缀，满足追踪规格 §118 的 attempt 身份要求。
func sourcingAIAttemptID(invocationID string, attempt int) string {
	if attempt <= 1 {
		return invocationID
	}
	return fmt.Sprintf("%s#a%d", invocationID, attempt)
}
