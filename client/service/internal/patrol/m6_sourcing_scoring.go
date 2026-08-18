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
	ErrSourcingScoringProviderUnavailable = errors.New("统一评分 provider 尚未配置")
	ErrSourcingScoringIncomplete          = errors.New("统一评分仍有未终局成员")
)

// ScoreCompletedSourcingBatch is the sole production orchestrator for batch
// scoring. It is deliberately independent from the account patrol actor and
// never reaches a hand, an IM surface, selection, or effect dispatch.
// Members are driven by a bounded concurrent pool with classified retries
// (2026-07-28 adjudication); interruption leaves inFlight reservations that
// the next invocation resumes.
func (m *Manager) ScoreCompletedSourcingBatch(
	ctx context.Context,
	batchID string,
) (*store.SourcingBatchScoringProgress, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return nil, store.ErrSourcingBatchInvalid
	}

	m.scoreMu.Lock()
	defer m.scoreMu.Unlock()

	progress, err := m.store.SourcingBatchScoringProgress(batchID)
	if err != nil {
		return nil, err
	}
	if progress.Completed {
		return progress, nil
	}
	advice := m.currentAdvice()
	if advice == nil {
		return progress, ErrSourcingScoringProviderUnavailable
	}
	provider, model := advice.ProviderName(), advice.ModelName()
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(model) == "" {
		return progress, ErrSourcingScoringProviderUnavailable
	}
	// 刻意不比对 progress.Provider/Model:引擎运行期可换代(2026-08-12 甲方
	// 裁决),半批换模型按混模型批次继续推进,每次调用用了哪个模型各自如实记账。
	// 比对并拒绝只会把批次卡成每秒重试的停摆,而混模型分数已被甲方明示接受。

	revision, err := m.store.SourcingScoringRevision(batchID)
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

	items, err := m.store.PendingSourcingScoreWork(batchID)
	if err != nil {
		return progress, err
	}
	poolErr := m.runSourcingAIMemberPool(ctx, len(items), func(index int) error {
		return m.driveSourcingScoreMember(
			ctx, advice, batchID, view.ScoringPrompt, revision.RevisionHash,
			items[index], provider, model,
		)
	})

	progress, err = m.store.SourcingBatchScoringProgress(batchID)
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
		return progress, ErrSourcingScoringIncomplete
	}
	return progress, nil
}

// driveSourcingScoreMember drives one member's reservation to its single
// terminal fact: reserve (or adopt the inFlight row), then retry the provider
// call per the classified budget. Gate closure and ctx cancellation return
// without completing so the reservation stays resumable.
func (m *Manager) driveSourcingScoreMember(
	ctx context.Context,
	advice AdviceExecutor,
	batchID, prompt, contextRevisionHash string,
	item store.SourcingScoreWorkItem,
	provider, model string,
) error {
	run := item.Run
	scoringInput, inputErr := m5ai.RenderScoringInputV1(run.ResumeJSON)
	content := ""
	renderErr := inputErr
	if renderErr == nil {
		content, renderErr = m5ai.RenderScoringPrompt(prompt, scoringInput)
	}
	inputHash := sha256Hex(prompt + "\x00scoring-input-v1\x00" + run.ContentHash)
	if renderErr == nil {
		inputHash = sha256Hex(content)
	}

	invocationID := stableM5ID(
		"score-invocation", run.RunID, contextRevisionHash, run.ContentHash,
	)
	invocation := item.Invocation
	if invocation == nil {
		reserved, err := m.store.ReserveSourcingScore(store.ReserveSourcingScoreRequest{
			InvocationID: invocationID, BatchID: batchID, RunID: run.RunID,
			ContextRevisionHash: contextRevisionHash, RunContentHash: run.ContentHash,
			Provider: provider, Model: model, InputHash: inputHash, StartedAt: m.now(),
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
		// 接手 inFlight 行前核对调用身份；漂移说明预留与当前批次事实
		// 不再一致，必须响亮冲突而不是按新身份重调。provider/model 刻意不参与
		// 身份：引擎运行期可换代，旧引擎预留的行由新引擎接手收尾，行上的
		// provider/model 保留预留时刻的事实。
		if invocation.InvocationID != invocationID ||
			invocation.ContextRevisionHash != contextRevisionHash ||
			invocation.RunContentHash != run.ContentHash ||
			invocation.InputHash != inputHash {
			return store.ErrAIInvocationConflict
		}
	}

	if renderErr != nil {
		errorClass := "scoringInputBudgetBlocked"
		if inputErr != nil {
			errorClass = "scoringInputInvalid"
		}
		completion := store.AIInvocationCompletion{
			InvocationID: invocationID, Status: store.AIInvocationBudgetBlocked,
			ErrorClass: errorClass, FailureStage: m5ai.FailureStageRequestBuild,
			ErrorDetailCode: errorClass, FinishedAt: m.now(),
		}
		logAIInvocationOutcome(advice, m5ai.PurposeScoring, completion, "")
		_, err := m.store.CompleteSourcingScore(store.CompleteSourcingScoreRequest{
			Completion: completion,
		})
		if err != nil {
			logAIInvocationPersistenceFailure(advice, m5ai.PurposeScoring, completion, err)
		}
		return err
	}

	retrySequence := 0
	consecutiveRateLimited := 0
	// 首次与接手后的第一次尝试都保守计入预算；只有确认为 429 的失败
	// 才让下一次尝试免预算。
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
			logAIInvocationOutcome(advice, m5ai.PurposeScoring, completion, "")
			_, err := m.store.CompleteSourcingScore(store.CompleteSourcingScoreRequest{
				Completion: completion,
			})
			if err != nil {
				logAIInvocationPersistenceFailure(advice, m5ai.PurposeScoring, completion, err)
			}
			return err
		}
		updated, err := m.store.RecordSourcingScoreAttempt(invocationID, nextAttemptBudgeted)
		if err != nil {
			return err
		}
		invocation = updated

		started := time.Now()
		response, callErr := advice.CompleteJSON(ctx, m5ai.CompletionRequest{
			InvocationID:        sourcingAIAttemptID(invocationID, invocation.AttemptCount),
			Purpose:             m5ai.PurposeScoring,
			ContextRevisionHash: contextRevisionHash,
			PromptRevision:      m5ai.ScoringInputFormatVersion,
			UserContent:         content,
			MaxOutputTokens:     m5ai.ScoringOutputTokenLimit,
		})
		completion := m5CompletionFromProvider(
			invocationID, response, callErr, time.Since(started), m.now(),
		)
		var score *int
		retryClass := sourcingAIRetryNone
		if callErr == nil {
			suggestion, parseErr := m5ai.ParseScoringSuggestion(response.JSONText)
			switch {
			case parseErr != nil:
				markBusinessParseFailure(&completion, parseErr)
				retryClass = sourcingAIRetryBudgeted
			default:
				value := suggestion.Score
				score = &value
			}
		} else {
			if ctx.Err() != nil {
				// 取消引发的传输失败不是成员事实，留 inFlight 待续驱动。
				return nil
			}
			retryClass = classifySourcingAIFailure(callErr)
		}
		logAIInvocationOutcome(
			advice, m5ai.PurposeScoring, completion, response.Diagnostics.TraceErrorCode,
		)
		if score != nil || retryClass == sourcingAIRetryNone {
			_, err = m.store.CompleteSourcingScore(store.CompleteSourcingScoreRequest{
				Completion: completion, Score: score,
			})
			if err != nil {
				logAIInvocationPersistenceFailure(advice, m5ai.PurposeScoring, completion, err)
			}
			return err
		}

		failed := completion
		lastFailedCompletion = &failed
		nextAttemptBudgeted = retryClass != sourcingAIRetryUnlimited
		if retryClass == sourcingAIRetryUnlimited {
			consecutiveRateLimited++
			if consecutiveRateLimited%sourcingAIRateLimitAlertEvery == 0 {
				slog.Warn("评分调用连续限速重试中",
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
