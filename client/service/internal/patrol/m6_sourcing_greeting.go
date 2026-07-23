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
	ErrSourcingGreetingProviderUnavailable = errors.New("批次招呼语 provider 尚未配置")
	ErrSourcingGreetingIncomplete          = errors.New("批次招呼语仍有未终局成员")
	ErrSourcingGreetingPlatformDefault     = errors.New("批次招呼语配置要求使用平台默认话术")
)

// GenerateSelectedSourcingGreetings is the sole production orchestrator for
// generating greeting text for a completed selection. It only persists model
// suggestions: it never reaches a hand, opens a candidate, or constructs a
// greeting effect.
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
	if m.advice == nil {
		return progress, ErrSourcingGreetingProviderUnavailable
	}
	provider, model := m.advice.ProviderName(), m.advice.ModelName()
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(model) == "" {
		return progress, ErrSourcingGreetingProviderUnavailable
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
	if view.UsePlatformDefaultGreeting {
		return progress, ErrSourcingGreetingPlatformDefault
	}

	for {
		if err := ctx.Err(); err != nil {
			progress, _ = m.store.SourcingBatchGreetingProgress(batchID)
			return progress, err
		}
		material, err := m.store.NextSelectedSourcingGreetingMaterial(batchID)
		if err != nil {
			return progress, err
		}
		if material == nil {
			progress, err = m.store.SourcingBatchGreetingProgress(batchID)
			if err != nil {
				return nil, err
			}
			if !progress.Completed {
				return progress, ErrSourcingGreetingIncomplete
			}
			return progress, nil
		}

		if err := m.generateSourcingGreetingMember(
			ctx, view.GreetingPrompt, *material, provider, model,
		); err != nil {
			progress, _ = m.store.SourcingBatchGreetingProgress(batchID)
			return progress, err
		}
	}
}

func (m *Manager) generateSourcingGreetingMember(
	ctx context.Context,
	prompt string,
	material store.SourcingGreetingMaterial,
	provider, model string,
) error {
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
	reserved, err := m.store.ReserveSourcingGreeting(store.ReserveSourcingGreetingRequest{
		InvocationID: invocationID, BatchID: material.BatchID, RunID: material.RunID,
		ProfileID: material.ProfileID, ContextRevisionHash: material.ContextRevisionHash,
		RunContentHash: material.RunContentHash, Provider: provider, Model: model,
		InputHash: inputHash, StartedAt: m.now(),
	})
	if err != nil {
		return err
	}
	if !reserved.Created {
		return nil
	}
	if renderErr != nil {
		errorClass := "greetingInputBudgetBlocked"
		if inputErr != nil {
			errorClass = "greetingInputInvalid"
		}
		_, err = m.store.CompleteSourcingGreeting(store.CompleteSourcingGreetingRequest{
			Completion: store.AIInvocationCompletion{
				InvocationID: invocationID, Status: store.AIInvocationBudgetBlocked,
				ErrorClass: errorClass, FinishedAt: m.now(),
			},
		})
		return err
	}

	started := time.Now()
	response, callErr := m.advice.CompleteJSON(ctx, m5ai.CompletionRequest{
		InvocationID: invocationID, Purpose: m5ai.PurposeGreeting,
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
	if callErr == nil {
		suggestion, parseErr := m5ai.ParseGreetingSuggestion(response.JSONText)
		switch {
		case parseErr != nil:
			completion.Status = store.AIInvocationInvalidOutput
			completion.ErrorClass = "invalidOutput"
		case !reasoningUsageSafe(completion):
			completion.Status = store.AIInvocationInvalidOutput
			completion.ErrorClass = "reasoningUsageUnsafe"
		default:
			greetingText = suggestion.Text
			contentHash = sha256Hex(greetingText)
		}
	}
	_, err = m.store.CompleteSourcingGreeting(store.CompleteSourcingGreetingRequest{
		Completion: completion, GreetingText: greetingText, ContentHash: contentHash,
	})
	return err
}
