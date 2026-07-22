package patrol

import (
	"context"
	"errors"
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
	if m.advice == nil {
		return progress, ErrSourcingScoringProviderUnavailable
	}
	provider, model := m.advice.ProviderName(), m.advice.ModelName()
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(model) == "" {
		return progress, ErrSourcingScoringProviderUnavailable
	}
	if (progress.Provider != "" && progress.Provider != provider) ||
		(progress.Model != "" && progress.Model != model) {
		return progress, store.ErrAIInvocationConflict
	}

	revision, err := m.store.JobAIContextRevisionByHash(progress.ContextRevisionHash)
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

	for {
		if err := ctx.Err(); err != nil {
			progress, _ = m.store.SourcingBatchScoringProgress(batchID)
			return progress, err
		}
		run, err := m.store.NextSourcingBatchRunWithoutScore(batchID)
		if err != nil {
			return progress, err
		}
		if run == nil {
			progress, err = m.store.SourcingBatchScoringProgress(batchID)
			if err != nil {
				return nil, err
			}
			if !progress.Completed {
				return progress, ErrSourcingScoringIncomplete
			}
			return progress, nil
		}

		if err := m.scoreSourcingBatchMember(ctx, batchID, view.ScoringPrompt, *run, provider, model); err != nil {
			progress, _ = m.store.SourcingBatchScoringProgress(batchID)
			return progress, err
		}
	}
}

func (m *Manager) scoreSourcingBatchMember(
	ctx context.Context,
	batchID, prompt string,
	run store.SourcingCandidateRun,
	provider, model string,
) error {
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

	invocationID := stableM5ID("score-invocation", run.RunID, run.ContextRevisionHash, run.ContentHash)
	reserved, err := m.store.ReserveSourcingScore(store.ReserveSourcingScoreRequest{
		InvocationID: invocationID, BatchID: batchID, RunID: run.RunID,
		ContextRevisionHash: run.ContextRevisionHash, RunContentHash: run.ContentHash,
		Provider: provider, Model: model, InputHash: inputHash, StartedAt: m.now(),
	})
	if err != nil {
		return err
	}
	if !reserved.Created {
		return nil
	}
	if renderErr != nil {
		errorClass := "scoringInputBudgetBlocked"
		if inputErr != nil {
			errorClass = "scoringInputInvalid"
		}
		_, err = m.store.CompleteSourcingScore(store.CompleteSourcingScoreRequest{
			Completion: store.AIInvocationCompletion{
				InvocationID: invocationID, Status: store.AIInvocationBudgetBlocked,
				ErrorClass: errorClass, FinishedAt: m.now(),
			},
		})
		return err
	}

	started := time.Now()
	response, callErr := m.advice.CompleteJSON(ctx, m5ai.CompletionRequest{
		Purpose: m5ai.PurposeScoring, UserContent: content,
		MaxOutputTokens: m5ai.ScoringOutputTokenLimit,
	})
	completion := m5CompletionFromProvider(
		invocationID, response, callErr, time.Since(started), m.now(),
	)
	var score *int
	if callErr == nil {
		suggestion, parseErr := m5ai.ParseScoringSuggestion(response.JSONText)
		switch {
		case parseErr != nil:
			completion.Status = store.AIInvocationInvalidOutput
			completion.ErrorClass = "invalidOutput"
		case !reasoningUsageSafe(completion):
			completion.Status = store.AIInvocationInvalidOutput
			completion.ErrorClass = "reasoningUsageUnsafe"
		default:
			value := suggestion.Score
			score = &value
		}
	}
	_, err = m.store.CompleteSourcingScore(store.CompleteSourcingScoreRequest{
		Completion: completion, Score: score,
	})
	return err
}
