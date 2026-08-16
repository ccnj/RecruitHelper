package patrol

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
)

var (
	ErrSourcingGreetingProviderUnavailable = errors.New("批次招呼语 provider 尚未配置")
	ErrSourcingGreetingIncomplete          = errors.New("批次招呼语仍有未终局成员")
	ErrSourcingGreetingPlatformDefault     = errors.New("批次招呼语配置要求使用平台默认话术")
)

// GenerateSelectedSourcingGreetings is the sole production orchestrator for
// generating greeting text for a completed selection. It only persists model
// suggestions: it never reaches a hand, opens a candidate, or constructs a
// greeting effect. Members are driven by a bounded concurrent pool with
// classified retries (2026-07-28 adjudication); interruption leaves inFlight
// reservations that the next invocation resumes.
func (m *Manager) GenerateSelectedSourcingGreetings(
	ctx context.Context,
	batchID string,
) (*store.SourcingBatchGreetingProgress, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return nil, store.ErrSourcingBatchInvalid
	}

	m.greetingMu.Lock()
	defer m.greetingMu.Unlock()

	progress, err := m.store.SourcingBatchGreetingProgress(batchID)
	if err != nil {
		return nil, err
	}
	if progress.Completed {
		return progress, nil
	}
	advice := m.currentAdvice()
	if advice == nil {
		return progress, ErrSourcingGreetingProviderUnavailable
	}
	provider, model := advice.ProviderName(), advice.ModelName()
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(model) == "" {
		return progress, ErrSourcingGreetingProviderUnavailable
	}
	// 刻意不比对 progress.Provider/Model:与评分同理(2026-08-12 甲方裁决),
	// 半批换模型继续推进,不许卡成停摆。

	revision, err := m.store.SourcingGreetingRevision(batchID)
	if err != nil {
		return progress, err
	}
	if revision == nil {
		return progress, store.ErrJobAIContextRevisionNotFound
	}
	view, err := m5ai.DeriveSourcingView(revision.SourcePackage)
	if err != nil {
		return progress, err
	}
	if view.UsePlatformDefaultGreeting {
		return progress, ErrSourcingGreetingPlatformDefault
	}

	items, err := m.store.PendingSourcingGreetingWork(batchID, revision.RevisionHash)
	if err != nil {
		return progress, err
	}
	poolErr := m.runSourcingAIMemberPool(ctx, len(items), func(index int) error {
		return m.driveSourcingGreetingMember(
			ctx, advice, view.GreetingPrompt, items[index], provider, model,
		)
	})

	progress, err = m.store.SourcingBatchGreetingProgress(batchID)
	if err != nil {
		return nil, err
	}
	if poolErr != nil {
		return progress, poolErr
	}
	if err := ctx.Err(); err != nil {
		return progress, err
	}
	if !progress.Completed {
		return progress, ErrSourcingGreetingIncomplete
	}
	return progress, nil
}

// driveSourcingGreetingMember drives one selected member's reservation to its
// single terminal fact, mirroring driveSourcingScoreMember's retry contract.
func (m *Manager) driveSourcingGreetingMember(
	ctx context.Context,
	advice AdviceExecutor,
	prompt string,
	item store.SourcingGreetingWorkItem,
	provider, model string,
) error {
	material := item.Material
	input, inputErr := m5ai.RenderGreetingInputV1(material.ResumeJSON)
	content := ""
	renderErr := inputErr
	if renderErr == nil {
		content, renderErr = m5ai.RenderGreetingPrompt(prompt, input)
	}
	inputHash := sha256Hex(prompt + "\x00" + m5ai.GreetingInputFormatVersion + "\x00" + material.RunContentHash)
	if renderErr == nil {
		inputHash = sha256Hex(content)
	}

	invocationID := stableM5ID(
		"greeting-invocation", material.RunID, material.ContextRevisionHash, material.RunContentHash,
	)
	invocation := item.Invocation
	if invocation == nil {
		reserved, err := m.store.ReserveSourcingGreeting(store.ReserveSourcingGreetingRequest{
			InvocationID: invocationID, BatchID: material.BatchID, RunID: material.RunID,
			ProfileID: material.ProfileID, ContextRevisionHash: material.ContextRevisionHash,
			RunContentHash: material.RunContentHash, Provider: provider, Model: model,
			InputHash: inputHash, StartedAt: m.now(),
		})
		if err != nil {
			return err
		}
		reservation := reserved.Invocation
		if !reserved.Created && reservation.FinishedAt != nil {
			return nil
		}
		invocation = &reservation
	} else {
		// 接手 inFlight 行前核对调用身份；漂移必须响亮冲突。provider/model
		// 刻意不参与身份：引擎运行期可换代，旧引擎预留的行由新引擎接手收尾。
		if invocation.InvocationID != invocationID ||
			invocation.BatchID != material.BatchID ||
			invocation.ProfileID != material.ProfileID ||
			invocation.ContextRevisionHash != material.ContextRevisionHash ||
			invocation.RunContentHash != material.RunContentHash ||
			invocation.InputHash != inputHash {
			return store.ErrAIInvocationConflict
		}
	}

	if renderErr != nil {
		errorClass := "greetingInputBudgetBlocked"
		if inputErr != nil {
			errorClass = "greetingInputInvalid"
		}
		completion := store.AIInvocationCompletion{
			InvocationID: invocationID, Status: store.AIInvocationBudgetBlocked,
			ErrorClass: errorClass, FailureStage: m5ai.FailureStageRequestBuild,
			ErrorDetailCode: errorClass, FinishedAt: m.now(),
		}
		logAIInvocationOutcome(advice, m5ai.PurposeGreeting, completion, "")
		_, err := m.store.CompleteSourcingGreeting(store.CompleteSourcingGreetingRequest{
			Completion: completion,
		})
		if err != nil {
			logAIInvocationPersistenceFailure(advice, m5ai.PurposeGreeting, completion)
		}
		return err
	}

	retrySequence := 0
	consecutiveRateLimited := 0
	nextAttemptBudgeted := true
	var lastFailedCompletion *store.AIInvocationCompletion
	for {
		if ctx.Err() != nil {
			return nil
		}
		if nextAttemptBudgeted &&
			invocation.BudgetedAttemptCount >= sourcingAIBudgetedAttemptLimit {
			completion := store.AIInvocationCompletion{
				InvocationID: invocationID, Status: store.AIInvocationTransportFailed,
				ErrorClass: "transport", FailureStage: m5ai.FailureStageTransport,
				ErrorDetailCode: "attemptBudgetExhausted", FinishedAt: m.now(),
			}
			if lastFailedCompletion != nil {
				completion = *lastFailedCompletion
				completion.FinishedAt = m.now()
			}
			logAIInvocationOutcome(advice, m5ai.PurposeGreeting, completion, "")
			_, err := m.store.CompleteSourcingGreeting(store.CompleteSourcingGreetingRequest{
				Completion: completion,
			})
			if err != nil {
				logAIInvocationPersistenceFailure(advice, m5ai.PurposeGreeting, completion)
			}
			return err
		}
		updated, err := m.store.RecordSourcingGreetingAttempt(invocationID, nextAttemptBudgeted)
		if err != nil {
			return err
		}
		invocation = updated

		started := time.Now()
		response, callErr := advice.CompleteJSON(ctx, m5ai.CompletionRequest{
			InvocationID:        sourcingAIAttemptID(invocationID, invocation.AttemptCount),
			Purpose:             m5ai.PurposeGreeting,
			ContextRevisionHash: material.ContextRevisionHash,
			PromptRevision:      m5ai.GreetingInputFormatVersion,
			UserContent:         content,
			MaxOutputTokens:     m5ai.GreetingOutputTokenLimit,
		})
		completion := m5CompletionFromProvider(
			invocationID, response, callErr, time.Since(started), m.now(),
		)
		greetingText := ""
		contentHash := ""
		terminalOK := false
		retryClass := sourcingAIRetryNone
		if callErr == nil {
			suggestion, parseErr := m5ai.ParseGreetingSuggestion(response.JSONText)
			switch {
			case parseErr != nil:
				markBusinessParseFailure(&completion, parseErr)
				retryClass = sourcingAIRetryBudgeted
			default:
				greetingText = suggestion.Text
				contentHash = sha256Hex(greetingText)
				terminalOK = true
			}
		} else {
			if ctx.Err() != nil {
				return nil
			}
			retryClass = classifySourcingAIFailure(callErr)
		}
		logAIInvocationOutcome(
			advice, m5ai.PurposeGreeting, completion, response.Diagnostics.TraceErrorCode,
		)
		if terminalOK || retryClass == sourcingAIRetryNone {
			_, err = m.store.CompleteSourcingGreeting(store.CompleteSourcingGreetingRequest{
				Completion: completion, GreetingText: greetingText, ContentHash: contentHash,
			})
			if err != nil {
				logAIInvocationPersistenceFailure(advice, m5ai.PurposeGreeting, completion)
			}
			return err
		}

		failed := completion
		lastFailedCompletion = &failed
		nextAttemptBudgeted = retryClass != sourcingAIRetryUnlimited
		if retryClass == sourcingAIRetryUnlimited {
			consecutiveRateLimited++
			if consecutiveRateLimited%sourcingAIRateLimitAlertEvery == 0 {
				slog.Warn("招呼语生成连续限速重试中",
					slog.String("invocationId", invocationID),
					slog.Int("consecutiveRateLimited", consecutiveRateLimited),
					slog.Int("attemptCount", invocation.AttemptCount),
				)
			}
		} else {
			consecutiveRateLimited = 0
		}
		retrySequence++
		if err := m.config.SourcingAIRetryWait(
			ctx, retryClass == sourcingAIRetryUnlimited, retrySequence,
		); err != nil {
			return nil
		}
		if err := m.mayStartNextWorkflowMember(); err != nil {
			// 闸关闭：保留 inFlight，交由下次驱动续跑。
			return err
		}
	}
}
