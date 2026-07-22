package patrol

import (
	"context"
	"errors"

	"recruithelper/client/service/internal/store"
	"recruithelper/internal/ids"
)

const sourcingGreetingText = "你好"

func (a *roundActor) decidePendingSourcingCandidate() (*store.DecideSourcingCandidateResult, error) {
	if !a.account.SourcingEnabled || a.account.SourcingContextRevisionHash == "" {
		return nil, nil
	}
	run, err := a.manager.store.NextSourcingRunWithoutSelection(a.key(), a.account.SourcingContextRevisionHash)
	if err != nil || run == nil {
		return nil, err
	}
	return a.decideSourcingCandidate(run.RunID)
}

func (a *roundActor) decideSourcingCandidate(runID string) (*store.DecideSourcingCandidateResult, error) {
	if err := a.setStage("selectingSourcingCandidate"); err != nil {
		return nil, err
	}
	return a.manager.store.DecideSourcingCandidate(store.DecideSourcingCandidateRequest{
		RunID: runID, ProfileID: ids.NewProfileID(), DecidedAt: a.manager.now(),
	})
}

func (a *roundActor) sendSelectedSourcingGreeting(
	ctx context.Context,
	selection *store.DecideSourcingCandidateResult,
) (bool, error) {
	if selection == nil || selection.Decision.Outcome != store.SourcingSelectionSelected || selection.Profile == nil {
		return false, nil
	}
	if err := a.setStage("sendingSourcingGreeting"); err != nil {
		return false, err
	}
	if err := a.ensureDispatchAllowed(ctx); err != nil {
		return false, err
	}
	runner, ok := a.manager.runner.(AutomaticGreetingRunner)
	if !ok {
		return false, errors.New("巡检 runner 未实现自动招呼接缝")
	}
	intentID := stableM5ID("greeting-intent", selection.Profile.ProfileID, "v1")
	handle, err := runner.StartAutomaticGreeting(ctx, AutomaticGreetingRequest{
		ProfileID: selection.Profile.ProfileID, IntentID: intentID, Text: sourcingGreetingText,
	})
	if err != nil {
		return true, err
	}
	if handle == nil {
		return true, errors.New("自动招呼未返回等待句柄")
	}
	func() {
		a.manager.mu.Unlock()
		defer a.manager.mu.Lock()
		err = handle.Wait(ctx)
	}()
	return true, err
}
