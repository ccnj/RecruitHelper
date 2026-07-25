package patrol

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/textcanon"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

const sourcingWindowNoProgressLimit = 3

const (
	sourcingBlockInvalidState       = "invalidBatchState"
	sourcingBlockPositionSelect     = "positionSelectFailed"
	sourcingBlockFiltersApply       = "filtersApplyFailed"
	sourcingBlockWindowReadFailed   = "windowReadFailed"
	sourcingBlockPositionBindFailed = "positionBindFailed"
	sourcingBlockPositionChanged    = "positionChanged"
	sourcingBlockMemberReadFailed   = "memberReadFailed"
	sourcingBlockTargetReadFailed   = "targetReadFailed"
	sourcingBlockTargetMismatch     = "targetResultMismatch"
	sourcingBlockTargetCommitFailed = "targetCommitFailed"
	sourcingBlockMoveUnconfirmed    = "windowMoveUnconfirmed"
	sourcingBlockNoProgress         = "windowNoProgress"
)

// runSourcingBatch 是正式批采的唯一生产 actor。窗口引用只在当前调用栈内
// 使用；持久进度完全由 SourcingBatch 与批内成员构成，不另造窗口游标、
// attempt 或 in-flight 恢复槽。
func (a *roundActor) runSourcingBatch(ctx context.Context, batch *store.SourcingBatch) error {
	if batch == nil {
		return nil
	}
	if batch.Status == store.SourcingBatchBlocked {
		return a.manager.pauseAccount(a.key(), PauseSourcingBlocked, a.manager.now())
	}
	if batch.Status != store.SourcingBatchPreparing && batch.Status != store.SourcingBatchCollecting {
		return a.failSourcingBatch(batch.BatchID, sourcingBlockInvalidState, store.ErrSourcingBatchStateConflict)
	}
	members, err := a.manager.store.SourcingBatchExcludedPlatformUserRefs(batch.BatchID)
	if err != nil {
		return a.failSourcingBatch(batch.BatchID, sourcingBlockMemberReadFailed, err)
	}
	completed := make(map[string]struct{}, len(members))
	for _, ref := range members {
		completed[ref] = struct{}{}
	}
	// 个别候选人的定点读取以明确机器三元组终局时不应拖死整批。只在
	// 本轮内记住该身份；重启后最多多读一次，不新增第二份业务事实。
	unreadable := make(map[string]struct{})
	handledInRound := func(ref string) bool {
		if _, found := completed[ref]; found {
			return true
		}
		_, found := unreadable[ref]
		return found
	}

	var window protocol.CandidateReadSourcingWindowData
	if batch.Status == store.SourcingBatchPreparing {
		revision, revisionErr := a.manager.store.JobAIContextRevisionByHash(batch.ContextRevisionHash)
		if revisionErr != nil || revision == nil || batch.BackendJobID == nil ||
			strings.TrimSpace(*batch.BackendJobID) == "" ||
			strings.TrimSpace(*batch.BackendJobID) != strings.TrimSpace(revision.SourceJobRef) ||
			textcanon.Normalize(revision.DisplayName) == "" {
			return a.failSourcingBatch(
				batch.BatchID, sourcingBlockPositionSelect,
				errors.Join(store.ErrSourcingBinding, revisionErr),
			)
		}
		view, viewErr := m5ai.DeriveSourcingView(revision.SourcePackage)
		if viewErr != nil {
			return a.failSourcingBatch(batch.BatchID, sourcingBlockFiltersApply, viewErr)
		}
		positionTitle := textcanon.Normalize(revision.DisplayName)
		if err := a.setStage("selectingSourcingPosition"); err != nil {
			return a.failSourcingBatch(batch.BatchID, sourcingBlockPositionSelect, err)
		}
		selected, err := invokePrimitive[protocol.CandidateSelectSourcingPositionData](
			ctx, a, protocol.PrimCandidateSelectSourcingPosition,
			protocol.CandidateSelectSourcingPositionArgs{PositionTitle: positionTitle},
		)
		if err != nil {
			return a.failSourcingBatch(batch.BatchID, sourcingBlockPositionSelect, err)
		}
		if selected.PositionTitle != positionTitle {
			return a.failSourcingBatch(batch.BatchID, sourcingBlockPositionSelect, store.ErrSourcingBinding)
		}
		if err := a.waitSourcingInteractionPace(ctx); err != nil {
			return a.failSourcingBatch(batch.BatchID, sourcingBlockPositionSelect, err)
		}
		if err := a.setStage("applyingSourcingFilters"); err != nil {
			return a.failSourcingBatch(batch.BatchID, sourcingBlockFiltersApply, err)
		}
		filterArgs := protocol.CandidateApplySourcingFiltersArgs{
			PositionRef: selected.PositionRef, PositionTitle: selected.PositionTitle,
			Filters: view.JobFilters,
		}
		applied, _, err := invokePrimitiveDirectWithLogicalID[protocol.CandidateApplySourcingFiltersData](
			ctx, a, protocol.PrimCandidateApplySourcingFilters, filterArgs,
		)
		if err != nil {
			return a.failSourcingBatch(batch.BatchID, sourcingBlockFiltersApply, err)
		}
		if applied.PositionRef != selected.PositionRef ||
			applied.PositionTitle != positionTitle ||
			!reflect.DeepEqual(applied.Filters, view.JobFilters) {
			return a.failSourcingBatch(batch.BatchID, sourcingBlockFiltersApply, store.ErrSourcingBinding)
		}
		if err := a.waitSourcingInteractionPace(ctx); err != nil {
			return a.failSourcingBatch(batch.BatchID, sourcingBlockFiltersApply, err)
		}
		if err := a.setStage("bindingSourcingPosition"); err != nil {
			return a.failSourcingBatch(batch.BatchID, sourcingBlockPositionBindFailed, err)
		}
		var windowLogicalID string
		window, windowLogicalID, err = invokePrimitiveDirectWithLogicalID[protocol.CandidateReadSourcingWindowData](
			ctx, a, protocol.PrimCandidateReadSourcingWindow,
			protocol.CandidateReadSourcingWindowArgs{Move: protocol.SourcingWindowMoveCurrent},
		)
		if err != nil {
			return a.failSourcingBatch(batch.BatchID, sourcingBlockWindowReadFailed, err)
		}
		if window.PositionRef != selected.PositionRef || window.PositionTitle == nil ||
			*window.PositionTitle != positionTitle {
			return a.failSourcingBatch(batch.BatchID, sourcingBlockPositionBindFailed, store.ErrSourcingBinding)
		}
		batch, err = a.manager.store.BindSourcingBatchPosition(store.BindSourcingBatchPositionRequest{
			BatchID: batch.BatchID, LogicalDispatchID: windowLogicalID,
		})
		if err != nil {
			return a.failSourcingBatch(batch.BatchID, sourcingBlockPositionBindFailed, err)
		}
	} else {
		if batch.PositionRef == nil || *batch.PositionRef == "" {
			return a.failSourcingBatch(batch.BatchID, sourcingBlockInvalidState, store.ErrSourcingBinding)
		}
		window, err = a.readSourcingWindow(ctx, protocol.SourcingWindowMoveCurrent)
		if err != nil {
			return a.failSourcingBatch(batch.BatchID, sourcingBlockWindowReadFailed, err)
		}
	}

	noProgressMoves := 0
	attemptedTarget := false
	for {
		if batch.PositionRef == nil || window.PositionRef != *batch.PositionRef {
			return a.failSourcingBatch(batch.BatchID, sourcingBlockPositionChanged, store.ErrSourcingBinding)
		}

		for _, platformUserRef := range window.PlatformUserRefs {
			if handledInRound(platformUserRef) {
				continue
			}
			if attemptedTarget {
				if err := a.waitSourcingPace(ctx); err != nil {
					return err
				}
			}
			if err := a.setStage("readingSourcingTargetResume"); err != nil {
				return a.failSourcingBatch(batch.BatchID, sourcingBlockTargetReadFailed, err)
			}
			attemptedTarget = true
			data, logicalID, err := invokePrimitiveDirectWithLogicalID[protocol.CandidateReadSourcingResumeData](
				ctx, a, protocol.PrimCandidateReadSourcingTargetResume,
				protocol.CandidateReadSourcingTargetResumeArgs{
					PlatformUserRef: platformUserRef,
					PositionRef:     *batch.PositionRef,
				},
			)
			if err != nil {
				if skipsUnreadableSourcingTarget(err) {
					unreadable[platformUserRef] = struct{}{}
					continue
				}
				return a.failSourcingBatch(batch.BatchID, sourcingBlockTargetReadFailed, err)
			}
			if data.PlatformUserRef != platformUserRef || data.PositionRef != *batch.PositionRef {
				return a.failSourcingBatch(batch.BatchID, sourcingBlockTargetMismatch, store.ErrSourcingBinding)
			}
			result, err := a.manager.store.CompleteSourcingBatchCandidateRun(
				store.CompleteSourcingBatchCandidateRunRequest{
					BatchID: batch.BatchID, RunID: ids.NewSourcingRunID(),
					LogicalDispatchID: logicalID, Data: data,
				},
			)
			if err != nil {
				return a.failSourcingBatch(batch.BatchID, sourcingBlockTargetCommitFailed, err)
			}
			completed[platformUserRef] = struct{}{}
			if result.BatchCompleted {
				// Store 已在成员达标事务里把批次 completed 并以
				// sourcingTargetReached 暂停账号；这里不能二次覆盖时刻。
				return nil
			}
		}

		if err := a.setStage("advancingSourcingWindow"); err != nil {
			return a.failSourcingBatch(batch.BatchID, sourcingBlockWindowReadFailed, err)
		}
		if err := a.waitSourcingInteractionPace(ctx); err != nil {
			return err
		}
		next, err := a.readSourcingWindow(ctx, protocol.SourcingWindowMoveNext)
		if err != nil {
			return a.failSourcingBatch(batch.BatchID, sourcingBlockWindowReadFailed, err)
		}
		if next.PositionRef != *batch.PositionRef {
			return a.failSourcingBatch(batch.BatchID, sourcingBlockPositionChanged, store.ErrSourcingBinding)
		}
		if !next.Moved {
			return a.blockAndPauseSourcingBatch(batch.BatchID, sourcingBlockMoveUnconfirmed)
		}

		hasNewIdentity := false
		for _, ref := range next.PlatformUserRefs {
			if !handledInRound(ref) {
				hasNewIdentity = true
				break
			}
		}
		if hasNewIdentity {
			noProgressMoves = 0
		} else {
			noProgressMoves++
			if noProgressMoves >= sourcingWindowNoProgressLimit {
				return a.blockAndPauseSourcingBatch(batch.BatchID, sourcingBlockNoProgress)
			}
		}
		window = next
		// 滚动本身是一次平台交互。即使当前窗全部是旧批次成员、此前没有
		// 尝试候选人，下一窗首人也必须走候选人级 2–4 秒节奏，不能紧跟滚动打开。
		attemptedTarget = true
	}
}

// waitSourcingPace 在脑侧 actor 决定两次候选人动作之间的节奏。等待期间
// 释放 Manager 短锁，使真人暂停、账号改绑与传感事件仍可生效；醒来后必须
// 重新通过同一派发门禁，不能把等待前的授权带到等待后。
func (a *roundActor) waitSourcingPace(ctx context.Context) error {
	return a.waitSourcingDelay(ctx, a.manager.config.SourcingPaceWait)
}

func (a *roundActor) waitSourcingInteractionPace(ctx context.Context) error {
	return a.waitSourcingDelay(ctx, a.manager.config.InteractionPaceWait)
}

func (a *roundActor) waitSourcingDelay(ctx context.Context, wait func(context.Context) error) error {
	var waitErr error
	func() {
		a.manager.mu.Unlock()
		defer a.manager.mu.Lock()
		waitErr = wait(ctx)
	}()
	if waitErr != nil {
		return waitErr
	}
	return a.ensureDispatchAllowed(ctx)
}

func skipsUnreadableSourcingTarget(err error) bool {
	var runErr *RunError
	return errors.As(err, &runErr) && runErr.Code == protocol.ErrCodeElementUnresolved &&
		runErr.Retryable == protocol.RetryableManualOnly && runErr.SideEffect == protocol.SideEffectNone
}

func (a *roundActor) readSourcingWindow(
	ctx context.Context,
	move protocol.SourcingWindowMove,
) (protocol.CandidateReadSourcingWindowData, error) {
	data, _, err := invokePrimitiveDirectWithLogicalID[protocol.CandidateReadSourcingWindowData](
		ctx, a, protocol.PrimCandidateReadSourcingWindow,
		protocol.CandidateReadSourcingWindowArgs{Move: move},
	)
	return data, err
}

func (a *roundActor) blockAndPauseSourcingBatch(batchID, reason string) error {
	_, blockErr := a.manager.store.BlockSourcingBatch(store.BlockSourcingBatchRequest{
		BatchID: batchID, Reason: reason, BlockedAt: a.manager.now(),
	})
	pauseErr := a.manager.pauseAccount(a.key(), PauseSourcingBlocked, a.manager.now())
	return errors.Join(blockErr, pauseErr)
}

func (a *roundActor) failSourcingBatch(batchID, reason string, cause error) error {
	if preservesSourcingBatch(cause) {
		return cause
	}
	stateErr := a.blockAndPauseSourcingBatch(batchID, reason)
	return errors.Join(cause, stateErr)
}

// 普通账号暂停、每日边界与服务关闭只停止新派发，不改变正式采集批次。
// 重新开启账号后仍从同一个 preparing/collecting 批次继续。
func preservesSourcingBatch(err error) bool {
	return errors.Is(err, ErrActorPaused) || errors.Is(err, ErrDailyWindowExpired) ||
		errors.Is(err, ErrActorGenerationChanged) ||
		errors.Is(err, ErrRoundSupersededBySourcingBatch) ||
		errors.Is(err, ErrManualQuietActive) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
