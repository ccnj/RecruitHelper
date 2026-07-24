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
// completed M6 selection. A page pass sends every currently visible target in
// page order without resetting between candidates. Once a source is linked to
// a WAL intent, every replay skips the page scan and collects that same logical
// dispatch.
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

	for pass := 0; pass < 2; pass++ {
		// 已绑定 WAL 的来源必须先独立收编。页面顺序可能与采集顺序不同，
		// 不能让一次崩溃留下的在途来源被后续页面扫描越过。
		for {
			progress, err := m.store.SourcingBatchGreetingSendProgress(batchID)
			if err != nil {
				return nil, err
			}
			if progress.Completed || progress.SuspectCount > 0 {
				return progress, nil
			}
			plan, err := m.store.SourcingGreetingSendScanPlan(batchID)
			if err != nil {
				return progress, err
			}
			var linked *store.SourcingGreetingSendTarget
			for i := range plan.Targets {
				if plan.Targets[i].EffectIntentID != nil {
					target := plan.Targets[i]
					linked = &target
					break
				}
			}
			if linked == nil {
				if len(plan.Targets) == 0 {
					return progress, ErrSourcingGreetingTargetNotFound
				}
				break
			}
			if err := m.runAutomaticSourcingGreeting(ctx, nil, AutomaticGreetingRequest{
				BatchID: linked.BatchID, InvocationID: linked.InvocationID,
			}); err != nil {
				latest, progressErr := m.store.SourcingBatchGreetingSendProgress(batchID)
				if progressErr != nil {
					return progress, errors.Join(err, progressErr)
				}
				return latest, err
			}
		}

		progress, err := m.store.SourcingBatchGreetingSendProgress(batchID)
		if err != nil {
			return nil, err
		}
		if progress.Completed || progress.SuspectCount > 0 {
			return progress, nil
		}
		plan, err := m.store.SourcingGreetingSendScanPlan(batchID)
		if err != nil {
			return progress, err
		}
		if len(plan.Targets) == 0 {
			return progress, ErrSourcingGreetingTargetNotFound
		}
		if pass > 0 {
			// 第一遍向下扫描未完全命中时，只允许这一回顶部兜底。
			if err := m.config.InteractionPaceWait(ctx); err != nil {
				return progress, err
			}
		}
		generation, err := m.currentSourcingGreetingGeneration(ctx, plan.Targets[0])
		if err != nil {
			return progress, err
		}
		if err := m.scanSourcingGreetingPass(ctx, *generation, *plan); err != nil {
			latest, progressErr := m.store.SourcingBatchGreetingSendProgress(batchID)
			if progressErr != nil {
				return progress, errors.Join(err, progressErr)
			}
			return latest, err
		}
	}
	progress, err := m.store.SourcingBatchGreetingSendProgress(batchID)
	if err != nil {
		return nil, err
	}
	if progress.Completed || progress.SuspectCount > 0 {
		return progress, nil
	}
	return progress, ErrSourcingGreetingTargetNotFound
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

func (m *Manager) scanSourcingGreetingPass(
	ctx context.Context,
	generation sourcingGreetingGeneration,
	plan store.SourcingGreetingSendScanPlan,
) error {
	if plan.BatchID == "" || plan.Platform == "" || plan.AccountRef == "" ||
		plan.PositionRef == "" || plan.CapturedCount <= 0 || plan.BatchTailAnchor == "" {
		return store.ErrSourcingGreetingEffectConflict
	}
	targets := make(map[string]store.SourcingGreetingSendTarget, len(plan.Targets))
	for i := range plan.Targets {
		target := plan.Targets[i]
		if target.BatchID != plan.BatchID || target.Platform != plan.Platform ||
			target.AccountRef != plan.AccountRef || target.PositionRef != plan.PositionRef ||
			target.PlatformUserRef == "" || target.EffectIntentID != nil {
			return store.ErrSourcingGreetingEffectConflict
		}
		if _, duplicate := targets[target.PlatformUserRef]; duplicate {
			return store.ErrSourcingGreetingEffectConflict
		}
		targets[target.PlatformUserRef] = target
	}
	if len(targets) == 0 {
		return nil
	}

	seenWindows := make(map[string]struct{}, m.config.MaxPages)
	seenIdentities := make(map[string]struct{}, plan.CapturedCount*2)
	move := protocol.SourcingWindowMoveReset
	tailSeen := false
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
		if err := validateSourcingGreetingWindow(window, plan.PositionRef); err != nil {
			return err
		}
		if move == protocol.SourcingWindowMoveNext && !window.Moved {
			return nil
		}
		windowKey := strings.Join(window.PlatformUserRefs, "\x00")
		if _, repeated := seenWindows[windowKey]; repeated {
			return nil
		}
		seenWindows[windowKey] = struct{}{}

		wasTailSeen := tailSeen
		for _, ref := range window.PlatformUserRefs {
			seenIdentities[ref] = struct{}{}
			if ref == plan.BatchTailAnchor {
				tailSeen = true
			}
		}
		for _, ref := range window.PlatformUserRefs {
			target, matched := targets[ref]
			if !matched {
				continue
			}
			// 每个全新候选人的 effect intent 形成前都保留候选人级随机
			// 节奏；窗口只是定位提示，最终授权仍由 Store 和手端 evaluator
			// 在既有正式轨道内独立重验。
			if err := m.config.SourcingPaceWait(ctx); err != nil {
				return err
			}
			// 节奏等待和前序真实发送都可能让虚拟列表重排。必须在 WAL
			// 形成前复读当前稳定窗口；目标已经卸载只表示本轮未定位，
			// 不能把它终局成一次 sideEffect=none 的失败意图。
			current, err := m.readSourcingGreetingWindow(
				ctx, generation, protocol.SourcingWindowMoveCurrent,
			)
			if err != nil {
				return err
			}
			if err := validateSourcingGreetingWindow(current, plan.PositionRef); err != nil {
				return err
			}
			if !sourcingGreetingWindowContains(current.PlatformUserRefs, ref) {
				continue
			}
			if err := m.runAutomaticSourcingGreeting(ctx, &generation, AutomaticGreetingRequest{
				BatchID: target.BatchID, InvocationID: target.InvocationID,
			}); err != nil {
				return err
			}
			delete(targets, ref)
			if len(targets) == 0 {
				return nil
			}
		}
		if wasTailSeen || (!tailSeen && len(seenIdentities) >= plan.CapturedCount*2) {
			return nil
		}
		move = protocol.SourcingWindowMoveNext
	}
	return nil
}

func validateSourcingGreetingWindow(
	window protocol.CandidateReadSourcingWindowData,
	positionRef string,
) error {
	if window.PositionRef != positionRef {
		return ErrSourcingGreetingPositionChanged
	}
	identityCounts := make(map[string]int, len(window.PlatformUserRefs))
	for _, ref := range window.PlatformUserRefs {
		identityCounts[ref]++
		if ref == "" || identityCounts[ref] != 1 {
			return ErrSourcingGreetingWindowRepeated
		}
	}
	return nil
}

func sourcingGreetingWindowContains(refs []string, target string) bool {
	for _, ref := range refs {
		if ref == target {
			return true
		}
	}
	return false
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
