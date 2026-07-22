package patrol

import (
	"context"
	"errors"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

const sourcingWindowNoProgressLimit = 3

const (
	sourcingBlockInvalidState       = "invalidBatchState"
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

	var window protocol.CandidateReadSourcingWindowData
	if batch.Status == store.SourcingBatchPreparing {
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
	for {
		if batch.PositionRef == nil || window.PositionRef != *batch.PositionRef {
			return a.failSourcingBatch(batch.BatchID, sourcingBlockPositionChanged, store.ErrSourcingBinding)
		}

		for _, platformUserRef := range window.PlatformUserRefs {
			if _, found := completed[platformUserRef]; found {
				continue
			}
			if err := a.setStage("readingSourcingTargetResume"); err != nil {
				return a.failSourcingBatch(batch.BatchID, sourcingBlockTargetReadFailed, err)
			}
			data, logicalID, err := invokePrimitiveDirectWithLogicalID[protocol.CandidateReadSourcingResumeData](
				ctx, a, protocol.PrimCandidateReadSourcingTargetResume,
				protocol.CandidateReadSourcingTargetResumeArgs{
					PlatformUserRef: platformUserRef,
					PositionRef:     *batch.PositionRef,
				},
			)
			if err != nil {
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
			if _, found := completed[ref]; !found {
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
	}
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
		errors.Is(err, ErrActorGenerationChanged) || errors.Is(err, ErrManualQuietActive) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
