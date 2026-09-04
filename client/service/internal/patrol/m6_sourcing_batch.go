package patrol

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"

	"recruithelper/client/service/internal/jobconfig"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/textcanon"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

const sourcingWindowNoProgressLimit = 3

const (
	sourcingBlockInvalidState       = "invalidBatchState"
	sourcingBlockPositionSelect     = store.SourcingBatchGateReasonPositionSelect
	sourcingBlockFiltersApply       = "filtersApplyFailed"
	sourcingBlockWindowReadFailed   = "windowReadFailed"
	sourcingBlockPositionBindFailed = "positionBindFailed"
	sourcingBlockPositionChanged    = "positionChanged"
	sourcingBlockMemberReadFailed   = "memberReadFailed"
	sourcingBlockTargetReadFailed   = "targetReadFailed"
	sourcingBlockTargetMismatch     = "targetResultMismatch"
	sourcingBlockTargetCommitFailed = "targetCommitFailed"
	// sourcingBlockMoveUnconfirmed 自 2026-08-05 起不再产生：滚不动已并入
	// windowNoProgress 计数。常量保留，存量 blocked 行仍带这个原因。
	sourcingBlockMoveUnconfirmed = "windowMoveUnconfirmed"
	sourcingBlockNoProgress      = "windowNoProgress"
	sourcingBlockJobStatusRead   = store.SourcingBatchGateReasonStatusRead
	sourcingBlockJobNotOnline    = store.SourcingBatchGateReasonJobNotOnline
	// sourcingBlockRecommendPageNotReady:切职位时推荐页未就绪且同轮重试 5 次仍
	// 未就绪(手自证瞬时);从 positionSelectFailed 拆出以便 UI 精确提示。
	sourcingBlockRecommendPageNotReady = store.SourcingBatchGateReasonRecommendPageNotReady
	// sourcingBlockPlanFinalize:当日职位计划定稿失败(AGENTS.md 2026-09-01)。
	// 不属于跳过类原因——定稿失败是计划级故障,由编排器收口扫描终止整个计划。
	sourcingBlockPlanFinalize = store.SourcingBatchGateReasonPlanFinalize
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

		// 采集开启闸(2026-08-12 甲方裁决,AGENTS.md「职位平台状态上报」):批次
		// 首读推荐窗口之前——也只有这个时点,读职位管理页不会打断推荐流——先读
		// 平台职位状态分区,当前职位不在「在线中」分区就不开采集;读不到同样不开,
		// 不确认就不开工。拦下即写成终局(见 stopSourcingBatchAtGate),用户重新
		// 点开始会新开一批、重新过本闸。
		if err := a.setStage("checkingJobPostingStatus"); err != nil {
			return a.stopSourcingBatchAtGate(batch.BatchID, sourcingBlockJobStatusRead, err)
		}
		published, err := invokePrimitive[protocol.JobReadPublishedListData](
			ctx, a, protocol.PrimJobReadPublishedList, protocol.JobReadPublishedListArgs{},
		)
		if err != nil {
			return a.stopSourcingBatchAtGate(batch.BatchID, sourcingBlockJobStatusRead, err)
		}
		if report := a.manager.config.ReportJobStatus; report != nil {
			// 观察用上报,fire-and-forget:成败都不改变下面的采集裁决。
			report(published)
		}
		// 当日职位计划定稿钩子(AGENTS.md 2026-09-01):计划草稿挂在当日第一次
		// 成功的状态闸读取上定稿(N=在线∩合格、份额落库、离线条目标跳过)。
		// 定稿失败按闸失败同向收场——不开批,计划由编排器收口扫描终止。
		if err := a.finalizeDailyJobPlanFromGate(published); err != nil {
			return a.stopSourcingBatchAtGate(batch.BatchID, sourcingBlockPlanFinalize, err)
		}
		if status := jobconfig.FindPostingStatus(positionTitle, published.Sections); status != jobconfig.PostingStatusLabelOnline {
			return a.stopSourcingBatchAtGate(batch.BatchID, sourcingBlockJobNotOnline,
				fmt.Errorf("职位「%s」当前平台状态为「%s」,未在线,不开始采集", positionTitle, status))
		}
		if err := a.waitSourcingInteractionPace(ctx); err != nil {
			return a.stopSourcingBatchAtGate(batch.BatchID, sourcingBlockJobStatusRead, err)
		}

		if err := a.setStage("selectingSourcingPosition"); err != nil {
			return a.failSourcingBatch(batch.BatchID, sourcingBlockPositionSelect, err)
		}
		selected, err := a.selectSourcingPositionWithRetries(ctx, batch.BatchID, positionTitle)
		if err != nil {
			reason := sourcingBlockPositionSelect
			if transientPageNotReady(err) {
				reason = sourcingBlockRecommendPageNotReady
			}
			return a.failSourcingBatch(batch.BatchID, reason, err)
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
		applied, err := a.applySourcingFiltersWithRetries(ctx, batch.BatchID, filterArgs)
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

		// moved=false 与“确认推进了但全是已处理身份”对脑是同一件事：本轮
		// 没有取得新候选人。手侧的 next 已经等到窗口连续稳定才敢报 moved
		// =false，那一次读取返回的身份仍然有效，因此先消费身份、再判进展。
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
				return a.settleOrBlockSourcingBatch(batch.BatchID)
			}
		}
		window = next
		// 滚动本身是一次平台交互。即使当前窗全部是旧批次成员、此前没有
		// 尝试候选人，下一窗首人也必须走候选人级 4～8 秒节奏，不能紧跟滚动打开。
		attemptedTarget = true
	}
}

// waitSourcingPace 在脑侧 actor 决定两次候选人动作之间的节奏。等待期间
// 释放 Manager 短锁，使真人暂停、账号改绑与传感事件仍可生效；醒来后必须
// 重新通过同一派发门禁，不能把等待前的授权带到等待后。
func (a *roundActor) waitSourcingPace(ctx context.Context) error {
	return a.waitSourcingDelay(ctx, a.manager.config.SourcingPaceWait)
}

// sourcingPositionSelectMaxRetries:切职位时推荐页未就绪(手自证瞬时)在同轮
// 最多再发几次同款命令。2026-09-03 甲方裁决自 1 次提到 5 次(共 6 次尝试),同批
// 把该原语的推荐页就绪等待放宽到 60 秒(AGENTS.md 条件等待上限同日修订)。
const sourcingPositionSelectMaxRetries = 5

// selectSourcingPositionWithRetries 在 preparing 阶段、推荐流尚未绑定时切换
// 职位:手报 CTX_NOT_READY 且自证瞬时(retryable=afterRecovery/yes)就等一个交互
// 节奏后同轮再发一次同款命令——原语自己会重新导航到推荐页并再等一个条件等待
// 上限。至多重试 sourcingPositionSelectMaxRetries 次,不加持久化计数;最后一次
// 仍失败按原路径拦停批次。
// 立案:2026-09-02 尚虹02 真机切第二个职位时推荐页 9.8s 未就绪,整日计划因此
// 少跑一个职位(47 份额);同款切换前一批 7.1s 成功。甲方 2026-09-02 裁决:这不是
// AGENTS.md「批间接续失败……不自动重试」所指的自动重试,规格不改。本函数只服务
// 职位尚未选定、推荐流尚未绑定的这一步,不得挪到之后任何会刷新推荐流的位置。
func (a *roundActor) selectSourcingPositionWithRetries(
	ctx context.Context,
	batchID string,
	positionTitle string,
) (protocol.CandidateSelectSourcingPositionData, error) {
	args := protocol.CandidateSelectSourcingPositionArgs{PositionTitle: positionTitle}
	for attempt := 0; ; attempt++ {
		selected, err := invokePrimitive[protocol.CandidateSelectSourcingPositionData](
			ctx, a, protocol.PrimCandidateSelectSourcingPosition, args,
		)
		if err == nil || !transientPageNotReady(err) || attempt >= sourcingPositionSelectMaxRetries {
			return selected, err
		}
		// 留痕(「错误收敛必须留痕」):每一次未就绪的完整判定现场进日志,最后
		// 一次的结果走原有失败路径落批次原因。
		slog.Warn("推荐页未就绪,切换职位同轮再试",
			"batchId", batchID, "positionTitle", positionTitle,
			"attempt", attempt+1, "maxRetries", sourcingPositionSelectMaxRetries,
			"err", err.Error())
		if paceErr := a.waitSourcingInteractionPace(ctx); paceErr != nil {
			return selected, paceErr
		}
	}
}

// sourcingFiltersApplyMaxRetries:筛选面这一趟没准备好时同轮最多再发几次同款
// 命令。刻意比切职位的 5 次少:切职位单趟只有几秒,筛选整条要开抽屉、覆盖六组、
// 提交、二次回读再取消,真机成功用时约 33 秒、失败也要 33 秒,4 趟已是两分多钟;
// 而手侧原语内部已先对每个点不动的选项补点三次(zhilian.ts clickOption),本层是
// 它之上的第二道,不必再堆次数。
const sourcingFiltersApplyMaxRetries = 3

// applySourcingFiltersWithRetries 在 preparing 阶段、推荐流尚未绑定时覆盖筛选:
// 手报 CTX_NOT_READY 且自证瞬时(retryable=afterRecovery/yes)就等一个交互节奏后
// 同轮再跑一整条原语——它自己会重开抽屉、从当前实际状态重新覆盖。至多重试
// sourcingFiltersApplyMaxRetries 次,不加持久化计数;最后一次仍失败按原路径拦停
// 批次(此后由当日职位计划按跳过类跳过该职位、接续下一条目)。
// 立案:2026-09-03 与 09-04 客户机各一次点年龄「自定义」落空,09-04 那次把当日
// 剩余 3 个职位共 53 个名额全废掉。与切职位同理,这不是 AGENTS.md「批间接续
// 失败……不自动重试」所指的自动重试(2026-09-02 甲方裁决口径,2026-09-04 写进
// 条文);本原语 platformSideEffect=none,重跑不产生任何候选人可见动作。
// 与切职位同一条边界:只服务推荐流尚未绑定的这一步,不得挪到之后任何会刷新
// 推荐流的位置。
func (a *roundActor) applySourcingFiltersWithRetries(
	ctx context.Context,
	batchID string,
	args protocol.CandidateApplySourcingFiltersArgs,
) (protocol.CandidateApplySourcingFiltersData, error) {
	for attempt := 0; ; attempt++ {
		applied, _, err := invokePrimitiveDirectWithLogicalID[protocol.CandidateApplySourcingFiltersData](
			ctx, a, protocol.PrimCandidateApplySourcingFilters, args,
		)
		if err == nil || !transientPageNotReady(err) || attempt >= sourcingFiltersApplyMaxRetries {
			return applied, err
		}
		// 留痕(「错误收敛必须留痕」):每一次未就绪的完整判定现场进日志,最后
		// 一次的结果走原有失败路径落批次原因。
		slog.Warn("筛选面未就绪,同轮再试",
			"batchId", batchID, "positionTitle", args.PositionTitle,
			"attempt", attempt+1, "maxRetries", sourcingFiltersApplyMaxRetries,
			"err", err.Error())
		if paceErr := a.waitSourcingInteractionPace(ctx); paceErr != nil {
			return applied, paceErr
		}
	}
}

// transientPageNotReady 只认手的协议级证词:CTX_NOT_READY 且 retryable 为
// afterRecovery/yes 才算瞬时;no/manualOnly 与证词缺席都不算,不猜。
func transientPageNotReady(err error) bool {
	typed := runError(err)
	if typed == nil || typed.Code != protocol.ErrCodeCtxNotReady {
		return false
	}
	return typed.Retryable == protocol.RetryableAfterRecovery ||
		typed.Retryable == protocol.RetryableYes
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

// settleOrBlockSourcingBatch 处理“采不下去了”。已经采到人就按脑侧扫描预算
// 收口，让漏斗拿现有候选人走完后续流程；一个人都没采到时不谎称采完，仍旧
// 阻塞转人工——那种情形通常是职位、筛选或页面有问题，继续往下走没有意义。
// 收口事务自己暂停账号，这里不再二次暂停。
func (a *roundActor) settleOrBlockSourcingBatch(batchID string) error {
	_, err := a.manager.store.SettleSourcingBatch(store.SettleSourcingBatchRequest{
		BatchID: batchID, SettleAt: a.manager.now(),
	})
	if err == nil {
		return nil
	}
	blockErr := a.blockAndPauseSourcingBatch(batchID, sourcingBlockNoProgress, gateReasonDetail(err))
	if errors.Is(err, store.ErrSourcingBatchStateConflict) {
		return blockErr
	}
	return errors.Join(err, blockErr)
}

func (a *roundActor) blockAndPauseSourcingBatch(batchID, reason, detail string) error {
	_, blockErr := a.manager.store.BlockSourcingBatch(store.BlockSourcingBatchRequest{
		BatchID: batchID, Reason: reason, Detail: detail, BlockedAt: a.manager.now(),
	})
	pauseErr := a.manager.pauseAccount(a.key(), PauseSourcingBlocked, a.manager.now())
	return errors.Join(blockErr, pauseErr)
}

// stopSourcingBatchAtGate 把采集开启闸拦下的批次直接写成终局,而不是可恢复
// 阻塞。此时批次尚未绑定推荐流、零成员,留成 blocked 毫无可保之物,反而让
// 「只回复消息」被"存在未终局采集批次"闸挡住——2026-08-13 真机暴露:职位
// 不上线期间客户连回消息都用不了。终局化后回消息立刻可用;重新点开始会新开
// 一批、重新过闸,用户可见的循环不变。
func (a *roundActor) stopSourcingBatchAtGate(batchID, reason string, cause error) error {
	if preservesSourcingBatch(cause) {
		return cause
	}
	_, stopErr := a.manager.store.StopSourcingBatch(store.StopSourcingBatchRequest{
		BatchID: batchID, Reason: reason, Detail: gateReasonDetail(cause), StoppedAt: a.manager.now(),
	})
	pauseErr := a.manager.pauseAccount(a.key(), PauseSourcingBlocked, a.manager.now())
	return errors.Join(cause, stopErr, pauseErr)
}

// gateReasonDetail 把拦停原因收窄前的判定现场取成留痕文本:RunError 渲染为
// "错误码/原因: 手报原话",脑侧错误取其文本;截断由 store 统一做。
func gateReasonDetail(cause error) string {
	if cause == nil {
		return ""
	}
	return cause.Error()
}

func (a *roundActor) failSourcingBatch(batchID, reason string, cause error) error {
	if preservesSourcingBatch(cause) {
		return cause
	}
	stateErr := a.blockAndPauseSourcingBatch(batchID, reason, gateReasonDetail(cause))
	return errors.Join(cause, stateErr)
}

// 普通账号暂停、每日边界与服务关闭只停止新派发，不改变正式采集批次。
// 重新开启账号后仍从同一个 preparing/collecting 批次继续。
func preservesSourcingBatch(err error) bool {
	return errors.Is(err, ErrActorPaused) || errors.Is(err, ErrDailyWindowExpired) ||
		errors.Is(err, ErrActorGenerationChanged) ||
		errors.Is(err, ErrRoundSupersededBySourcingBatch) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// finalizeDailyJobPlanFromGate 用状态闸刚读回的分区清单为当日职位计划定稿。
// 名字匹配与发布幂等同一套归一化口径(jobconfig.FindPostingStatus 内部完成);
// 计划不存在或已定稿是常态直通。定稿结果整体落一行日志留痕(留痕条款):
// 每条目的在线判定与份额此后也可在计划表原样查到。
func (a *roundActor) finalizeDailyJobPlanFromGate(published protocol.JobReadPublishedListData) error {
	plan, entries, err := a.manager.store.ActiveDailyJobPlan(a.key())
	if err != nil {
		return err
	}
	if plan == nil || plan.Status != store.DailyJobPlanDraft {
		return nil
	}
	observations := make([]store.DailyJobPlanGateObservation, 0, len(entries))
	for index := range entries {
		if entries[index].Status != store.DailyJobPlanEntryPending {
			continue
		}
		label := jobconfig.FindPostingStatus(entries[index].JobName, published.Sections)
		observations = append(observations, store.DailyJobPlanGateObservation{
			Seq:         entries[index].Seq,
			Online:      label == jobconfig.PostingStatusLabelOnline,
			StatusLabel: label,
		})
	}
	finalized, err := a.manager.store.FinalizeDailyJobPlan(plan.PlanID, observations, a.manager.now())
	if err != nil {
		return err
	}
	quotas := make([]string, 0, len(finalized.Entries))
	for index := range finalized.Entries {
		entry := finalized.Entries[index]
		quotas = append(quotas, fmt.Sprintf("%d:%s=%d/%s%s",
			entry.Seq, entry.JobName, entry.Quota, entry.Status,
			map[bool]string{true: "(" + entry.SkipReason + ")", false: ""}[entry.SkipReason != ""]))
	}
	slog.Info("当日职位计划已定稿",
		"planId", finalized.Plan.PlanID,
		"totalQuota", finalized.Plan.TotalQuota,
		"jobCount", finalized.Plan.JobCount,
		"entries", strings.Join(quotas, "; "))
	return nil
}
