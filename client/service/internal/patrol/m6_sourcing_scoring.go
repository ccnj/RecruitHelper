package patrol

import (
	"context"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
)

// scorePendingSourcingRun consumes at most one captured resume per round. The
// store reservation is the only authorization to call the provider; every
// existing reservation, including an interrupted one, permanently suppresses
// another call for the same run.
func (a *roundActor) scorePendingSourcingRun(ctx context.Context) (bool, error) {
	if !a.account.SourcingEnabled || a.account.SourcingContextRevisionHash == "" {
		return false, nil
	}
	run, err := a.manager.store.NextSourcingRunWithoutScore(a.key(), a.account.SourcingContextRevisionHash)
	if err != nil || run == nil {
		return false, err
	}
	// A missing local provider is recoverable configuration state. Keep this
	// run pending and stop collecting more candidates until configuration is
	// available instead of creating an unbounded unscored backlog.
	if a.manager.advice == nil {
		return true, nil
	}
	if err := a.setStage("scoringSourcingResume"); err != nil {
		return true, err
	}
	revision, err := a.manager.store.JobAIContextRevisionByHash(run.ContextRevisionHash)
	if err != nil {
		return true, err
	}
	if revision == nil {
		return true, store.ErrJobAIContextRevisionNotFound
	}
	view, err := m5ai.DeriveSourcingView(revision.SourcePackage)
	if err != nil {
		return true, err
	}

	content, renderErr := m5ai.RenderScoringPrompt(view.ScoringPrompt, run.ResumeJSON)
	inputHash := sha256Hex(content)
	if renderErr != nil {
		// Render failures contain no provider response, but still need a stable
		// one-shot reservation so later patrols cannot spin on the same run.
		inputHash = sha256Hex(view.ScoringPrompt + "\x00" + run.ResumeJSON)
	}
	invocationID := stableM5ID("score-invocation", run.RunID, run.ContextRevisionHash, run.ContentHash)
	reserved, err := a.manager.store.ReserveSourcingScore(store.ReserveSourcingScoreRequest{
		InvocationID: invocationID, RunID: run.RunID,
		ContextRevisionHash: run.ContextRevisionHash, RunContentHash: run.ContentHash,
		Provider: a.manager.advice.ProviderName(), Model: a.manager.advice.ModelName(),
		InputHash: inputHash, StartedAt: a.manager.now(),
	})
	if err != nil {
		return true, err
	}
	if !reserved.Created {
		return true, nil
	}
	if renderErr != nil {
		_, err = a.manager.store.CompleteSourcingScore(store.CompleteSourcingScoreRequest{
			Completion: store.AIInvocationCompletion{
				InvocationID: invocationID, Status: store.AIInvocationBudgetBlocked,
				ErrorClass: "scoringInputBudgetBlocked", FinishedAt: a.manager.now(),
			},
		})
		return true, err
	}

	started := time.Now()
	var response m5ai.CompletionResponse
	var callErr error
	func() {
		a.manager.mu.Unlock()
		defer a.manager.mu.Lock()
		response, callErr = a.manager.advice.CompleteJSON(ctx, m5ai.CompletionRequest{
			Purpose: m5ai.PurposeScoring, UserContent: content,
			MaxOutputTokens: m5ai.ScoringOutputTokenLimit,
		})
	}()
	completion := m5CompletionFromProvider(
		invocationID, response, callErr, time.Since(started), a.manager.now(),
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
	_, err = a.manager.store.CompleteSourcingScore(store.CompleteSourcingScoreRequest{
		Completion: completion, Score: score,
	})
	return true, err
}
