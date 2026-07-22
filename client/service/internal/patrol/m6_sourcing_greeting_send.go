package patrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

var (
	ErrAutomaticGreetingRunnerUnavailable = errors.New("自动招呼 runner 尚未接线")
	ErrSourcingGreetingTargetNotFound     = errors.New("正式批次招呼目标未在有界推荐窗口中定位")
	ErrSourcingGreetingWindowStopped      = errors.New("正式批次招呼推荐窗口停止前进")
	ErrSourcingGreetingWindowRepeated     = errors.New("正式批次招呼推荐窗口重复")
	ErrSourcingGreetingPositionChanged    = errors.New("正式批次招呼职位上下文已变化")
)

type sourcingGreetingGeneration struct {
	key         store.AccountKey
	handID      string
	fingerprint string
	session     string
	bootID      string
}

// SendSelectedSourcingGreetings is the narrow production orchestrator for a
// completed M6 selection. It only locates an unbound target; once the source is
// linked to a WAL intent, every replay skips the page scan and collects that
// same logical dispatch.
func (m *Manager) SendSelectedSourcingGreetings(
	ctx context.Context,
	batchID string,
) (*store.SourcingBatchGreetingSendProgress, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return nil, store.ErrSourcingGreetingEffectInvalid
	}

	m.greetingMu.Lock()
	defer m.greetingMu.Unlock()

	for {
		progress, err := m.store.SourcingBatchGreetingSendProgress(batchID)
		if err != nil {
			return nil, err
		}
		if progress.Completed || progress.SuspectCount > 0 {
			return progress, nil
		}
		target, err := m.store.NextSourcingGreetingSendTarget(batchID)
		if err != nil {
			return progress, err
		}
		if target == nil {
			return progress, ErrSourcingGreetingTargetNotFound
		}

		var generation *sourcingGreetingGeneration
		if target.EffectIntentID == nil {
			// 每个全新候选人的 effect intent 形成前由脑侧加入随机节奏；
			// 已绑定 WAL 的恢复路径不得再次等待或重新定位。
			if err := m.config.SourcingPaceWait(ctx); err != nil {
				return progress, err
			}
			generation, err = m.currentSourcingGreetingGeneration(ctx, *target)
			if err != nil {
				return progress, err
			}
			if err := m.locateSourcingGreetingTarget(ctx, *generation, *target); err != nil {
				return progress, err
			}
		}
		if err := m.runAutomaticSourcingGreeting(ctx, generation, AutomaticGreetingRequest{
			BatchID: target.BatchID, InvocationID: target.InvocationID,
		}); err != nil {
			latest, progressErr := m.store.SourcingBatchGreetingSendProgress(batchID)
			if progressErr != nil {
				return progress, errors.Join(err, progressErr)
			}
			return latest, err
		}
	}
}

func (m *Manager) currentSourcingGreetingGeneration(
	ctx context.Context,
	target store.SourcingGreetingSendTarget,
) (*sourcingGreetingGeneration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	generation := sourcingGreetingGeneration{
		key: store.AccountKey{Platform: target.Platform, AccountRef: target.AccountRef},
	}
	if err := m.validateSourcingGreetingGenerationLocked(ctx, &generation, true); err != nil {
		return nil, err
	}
	return &generation, nil
}

// validateSourcingGreetingGenerationLocked intentionally ignores the daily
// patrol gate: formal collection pauses the account when its target is met.
// It admits only the currently verified bound hand generation.
func (m *Manager) validateSourcingGreetingGenerationLocked(
	ctx context.Context,
	generation *sourcingGreetingGeneration,
	capture bool,
) error {
	if generation == nil {
		return ErrActorGenerationChanged
	}
	account, err := m.store.AccountByKey(generation.key)
	if err != nil {
		return err
	}
	if account == nil {
		return store.ErrAccountNotFound
	}
	if account.IdentityState != store.IdentityVerified || account.BoundHandID == "" ||
		account.PrincipalFingerprint == nil || strings.TrimSpace(*account.PrincipalFingerprint) == "" {
		return store.ErrAccountIdentityNotCurrent
	}
	hand, err := m.hands.State(ctx, account.BoundHandID)
	if err != nil {
		return err
	}
	if !hand.Online || hand.Session == "" || hand.BootID == "" ||
		account.IdentitySession != hand.Session || account.IdentityBootID != hand.BootID {
		return store.ErrAccountIdentityNotCurrent
	}
	if capture {
		generation.handID = account.BoundHandID
		generation.fingerprint = *account.PrincipalFingerprint
		generation.session = hand.Session
		generation.bootID = hand.BootID
		return nil
	}
	if account.BoundHandID != generation.handID || *account.PrincipalFingerprint != generation.fingerprint ||
		hand.Session != generation.session || hand.BootID != generation.bootID {
		return ErrActorGenerationChanged
	}
	return nil
}

func (m *Manager) locateSourcingGreetingTarget(
	ctx context.Context,
	generation sourcingGreetingGeneration,
	target store.SourcingGreetingSendTarget,
) error {
	seenWindows := make(map[string]struct{}, m.config.MaxPages)
	move := protocol.SourcingWindowMoveReset
	for page := 0; page < m.config.MaxPages; page++ {
		if page > 0 {
			if err := m.config.InteractionPaceWait(ctx); err != nil {
				return err
			}
		}
		window, err := m.readSourcingGreetingWindow(ctx, generation, move)
		if err != nil {
			return err
		}
		if window.PositionRef != target.PositionRef {
			return ErrSourcingGreetingPositionChanged
		}
		if move == protocol.SourcingWindowMoveNext && !window.Moved {
			return ErrSourcingGreetingWindowStopped
		}
		windowKey := strings.Join(window.PlatformUserRefs, "\x00")
		if _, repeated := seenWindows[windowKey]; repeated {
			return ErrSourcingGreetingWindowRepeated
		}
		seenWindows[windowKey] = struct{}{}
		matches := 0
		for _, ref := range window.PlatformUserRefs {
			if ref == target.PlatformUserRef {
				matches++
			}
		}
		if matches == 1 {
			return m.config.InteractionPaceWait(ctx)
		}
		if matches > 1 {
			return ErrSourcingGreetingWindowRepeated
		}
		move = protocol.SourcingWindowMoveNext
	}
	return ErrSourcingGreetingTargetNotFound
}

func (m *Manager) readSourcingGreetingWindow(
	ctx context.Context,
	generation sourcingGreetingGeneration,
	move protocol.SourcingWindowMove,
) (protocol.CandidateReadSourcingWindowData, error) {
	var zero protocol.CandidateReadSourcingWindowData
	meta, ok := protocol.Primitives[protocol.PrimCandidateReadSourcingWindow]
	if !ok || meta.Ver == 0 {
		return zero, fmt.Errorf("未激活原语 %q", protocol.PrimCandidateReadSourcingWindow)
	}
	args, err := protocol.Encode(protocol.CandidateReadSourcingWindowArgs{Move: move})
	if err != nil {
		return zero, err
	}
	if err := protocol.ValidatePrimitiveArgs(protocol.PrimCandidateReadSourcingWindow, meta.Ver, args); err != nil {
		return zero, err
	}

	m.mu.Lock()
	if err := m.validateSourcingGreetingGenerationLocked(ctx, &generation, false); err != nil {
		m.mu.Unlock()
		return zero, err
	}
	handle, err := m.runner.Start(ctx, RunRequest{
		HandID: generation.handID, ExpectedSession: generation.session, ExpectedBootID: generation.bootID,
		Platform: generation.key.Platform, AccountRef: generation.key.AccountRef,
		ExpectedPrincipalFingerprint: generation.fingerprint,
		Name:                         protocol.PrimCandidateReadSourcingWindow, Version: meta.Ver, Args: args,
	})
	m.mu.Unlock()
	if err != nil {
		return zero, err
	}
	if handle == nil || handle.LogicalDispatchID() == "" {
		return zero, errors.New("推荐窗口读取未返回持久逻辑派发引用")
	}
	raw, waitErr := handle.Wait(ctx)
	m.mu.Lock()
	gateErr := m.validateSourcingGreetingGenerationLocked(ctx, &generation, false)
	m.mu.Unlock()
	if gateErr != nil {
		return zero, gateErr
	}
	if waitErr != nil {
		return zero, waitErr
	}
	if err := protocol.ValidatePrimitiveData(protocol.PrimCandidateReadSourcingWindow, meta.Ver, raw); err != nil {
		return zero, err
	}
	if err := json.Unmarshal(raw, &zero); err != nil {
		return zero, err
	}
	return zero, nil
}

func (m *Manager) runAutomaticSourcingGreeting(
	ctx context.Context,
	generation *sourcingGreetingGeneration,
	req AutomaticGreetingRequest,
) error {
	runner, ok := m.runner.(AutomaticGreetingRunner)
	if !ok {
		return ErrAutomaticGreetingRunnerUnavailable
	}
	m.mu.Lock()
	if generation != nil {
		if err := m.validateSourcingGreetingGenerationLocked(ctx, generation, false); err != nil {
			m.mu.Unlock()
			return err
		}
	}
	handle, err := runner.StartAutomaticGreeting(ctx, req)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if handle == nil {
		return errors.New("自动招呼未返回等待句柄")
	}
	waitErr := handle.Wait(ctx)
	if generation == nil {
		return waitErr
	}
	m.mu.Lock()
	gateErr := m.validateSourcingGreetingGenerationLocked(ctx, generation, false)
	m.mu.Unlock()
	if gateErr != nil {
		return gateErr
	}
	return waitErr
}
