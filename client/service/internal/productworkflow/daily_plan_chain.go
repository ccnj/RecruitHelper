// 当日职位计划的运行编排(AGENTS.md「当日职位计划与招呼配额分摊」,2026-09-01
// 甲方裁决):建计划开跑、批间自动接续、无活跃运行时的收口扫描。接续复用
// 「再采一批」的 PendingAction 机器与巡检边界;一切失效方向朝少发——跳过类
// 失败只跳过该职位,其余一律终止当日计划,次日重来,不自动重试。
package productworkflow

import (
	"errors"
	"log/slog"
	"strings"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/workflow"
)

const (
	planEndReasonChainInterrupted = "chainInterrupted"
	planEndReasonUserEnded        = "userEnded"
	planEndReasonDayClosed        = "dayClosed"
	planEndReasonRunFailed        = "runFailed"

	planBatchStopReasonAborted = "dailyPlanAborted"
)

var ErrDailyPlanQuotaExhausted = errors.New("当日职位计划没有可执行条目")

// planSkipClassBatchReason:批前闸口径由 store 统一导出,只有这三类允许
// "跳过该条目、接续下一条目"(甲方裁决清单第 4 条+出口「允许的失败」);
// 其余原因可能是账号级故障(掉登录、手离线),逐条目盲试只会连环空转,
// 一律终止计划(dailyPlanFinalizeFailed 同理,是计划级故障)。
func planSkipClassBatchReason(reason string) bool {
	switch reason {
	case store.SourcingBatchGateReasonJobNotOnline,
		store.SourcingBatchGateReasonStatusRead,
		store.SourcingBatchGateReasonPositionSelect,
		store.SourcingBatchGateReasonRecommendPageNotReady:
		return true
	}
	return false
}

// StartFullDailyPlan 是完整流程的产品入口(2026-09-01 起唯一入口):活跃运行
// 幂等返回(原「沟通期再采一批」入口随当日计划停用,见设计文档);有未终局
// 批次沿既有恢复语义收养;否则建立当日计划并开跑计划序第一个条目。计划创建
// 与首个运行创建在同一把 Manager.mu 下完成,收口扫描不会目击半成品。
func (m *Manager) StartFullDailyPlan(key store.AccountKey) (*store.ProductWorkflowRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key.Platform = strings.TrimSpace(key.Platform)
	key.AccountRef = strings.TrimSpace(key.AccountRef)
	if key.Platform == "" || key.AccountRef == "" {
		return nil, store.ErrProductWorkflowInvalid
	}
	now := m.clock.Now()
	current, err := m.store.ActiveProductWorkflowRun()
	if err != nil {
		return nil, err
	}
	if current != nil {
		if current.Platform != key.Platform || current.AccountRef != key.AccountRef {
			return nil, ErrWorkflowScopeConflict
		}
		return m.activeForStart(key, workflow.ModeFull, now)
	}
	batch, err := m.store.ActiveSourcingBatch(key)
	if err != nil {
		return nil, err
	}
	if batch != nil {
		// 未终局批次优先收养:那批人可能已经采下来、建了档,换计划会让他们
		// 搁浅。批次若属于某个活跃计划,筛选照旧按其条目份额走。
		return m.startFullLocked(key, batch.ContextRevisionHash, now)
	}
	localDate := now.In(m.location).Format("2006-01-02")
	created, err := m.store.CreateDailyJobPlan(key, localDate, now)
	if err != nil {
		return nil, err
	}
	m.planSweepArmed.Store(true)
	next := store.NextPendingDailyJobPlanEntry(created.Entries)
	if next == nil {
		abortErr := m.store.AbortDailyJobPlan(created.Plan.PlanID, planEndReasonChainInterrupted, now)
		return nil, errors.Join(ErrDailyPlanQuotaExhausted, abortErr)
	}
	slog.Info("当日职位计划已建立",
		"planId", created.Plan.PlanID, "localDate", localDate,
		"totalQuota", created.Plan.TotalQuota, "jobs", len(created.Entries))
	return m.startFullLocked(key, next.RevisionHash, now)
}

// planEntryForRunLocked 找出运行当前批次对应的计划条目(按批次 revision)。
func (m *Manager) planEntryForRunLocked(
	run *store.ProductWorkflowRun,
	entries []store.DailyJobPlanEntry,
) (*store.DailyJobPlanEntry, error) {
	if run == nil || run.SourcingBatchID == nil || strings.TrimSpace(*run.SourcingBatchID) == "" {
		return nil, nil
	}
	batch, err := m.store.SourcingBatchByID(*run.SourcingBatchID)
	if err != nil {
		if errors.Is(err, store.ErrSourcingBatchNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if batch == nil {
		return nil, nil
	}
	return store.DailyJobPlanEntryByRevision(entries, batch.ContextRevisionHash), nil
}

// maintainDailyPlanInCommunication 在沟通阶段维护计划:还有下一条目就登记
// 接续(复用 PendingActionSourcing,巡检边界消费),并返回 true 表示本 tick
// 不应开启巡检回复(甲方知情接受回复延迟);当前是末条目则把它落成 done。
// 调用方持有 Manager.mu。
func (m *Manager) maintainDailyPlanInCommunication(
	run *store.ProductWorkflowRun,
	now time.Time,
) (bool, *store.ProductWorkflowRun, error) {
	// 沟通阶段每秒一 tick、可持续数小时;计划维护一旦收尾(末条目已 done,
	// 或本运行不属任何计划)就短路,不再逐 tick 查计划三连。
	if m.planCommunicationSettledRunID == run.RunID {
		return false, run, nil
	}
	key := store.AccountKey{Platform: run.Platform, AccountRef: run.AccountRef}
	plan, entries, err := m.store.ActiveDailyJobPlan(key)
	if err != nil {
		return false, run, err
	}
	if plan == nil {
		m.planCommunicationSettledRunID = run.RunID
		return false, run, nil
	}
	entry, err := m.planEntryForRunLocked(run, entries)
	if err != nil {
		return false, run, err
	}
	if entry == nil {
		m.planCommunicationSettledRunID = run.RunID
		return false, run, nil
	}
	next := store.NextPendingDailyJobPlanEntryAfter(entries, entry.Seq)
	if next == nil {
		// 末条目:发送已全部终局(进入沟通即证),落成 done 后照常开巡检。
		if entry.Status == store.DailyJobPlanEntryPending {
			batchID := ""
			if run.SourcingBatchID != nil {
				batchID = *run.SourcingBatchID
			}
			if err := m.store.MarkDailyJobPlanEntryDone(plan.PlanID, entry.Seq, batchID, now); err != nil {
				return false, run, err
			}
		}
		m.planCommunicationSettledRunID = run.RunID
		return false, run, nil
	}
	if run.PendingAction != "" {
		return true, run, nil
	}
	requested, err := m.store.RequestProductWorkflowPendingAction(
		store.RequestProductWorkflowPendingActionRequest{
			RunID:               run.RunID,
			Action:              store.ProductWorkflowPendingActionSourcing,
			ContextRevisionHash: next.RevisionHash,
			RequestedAt:         now,
		},
	)
	if err != nil {
		return true, run, err
	}
	slog.Info("当日职位计划批间接续已登记",
		"planId", plan.PlanID, "doneSeq", entry.Seq, "nextSeq", next.Seq,
		"nextJob", next.JobName, "nextQuota", next.Quota)
	return true, requested, nil
}

// markDailyPlanEntryDoneBeforeChain 在接续消费点把当前条目落成 done。放在
// 完结旧 run 之前:中途崩溃时条目已 done、pending 仍在,重放会跳过重复标记,
// 幂等续做。调用方持有 Manager.mu。
func (m *Manager) markDailyPlanEntryDoneBeforeChain(
	run *store.ProductWorkflowRun,
	now time.Time,
) error {
	key := store.AccountKey{Platform: run.Platform, AccountRef: run.AccountRef}
	plan, entries, err := m.store.ActiveDailyJobPlan(key)
	if err != nil || plan == nil {
		return err
	}
	entry, err := m.planEntryForRunLocked(run, entries)
	if err != nil || entry == nil {
		return err
	}
	if entry.Status != store.DailyJobPlanEntryPending {
		return nil
	}
	batchID := ""
	if run.SourcingBatchID != nil {
		batchID = *run.SourcingBatchID
	}
	return m.store.MarkDailyJobPlanEntryDone(plan.PlanID, entry.Seq, batchID, now)
}

// reconcileDailyPlansWithoutRun 是无活跃运行时的计划收口扫描:按最近一次运行
// 的终局方式分类——跳过类失败接续下一条目,其余按设计文档「恢复与终止」表
// 收口(完成或终止)。终止时顺带终局化仍未收尾的计划批次,防止次日被当作
// 存量批次收养后按配置全额跑掉(超发方向,必须堵死)。
func (m *Manager) reconcileDailyPlansWithoutRun() error {
	// 内存熔断:从未建过计划(或上次扫描已确认零活跃计划)时不查库——
	// AdvanceOnce 每秒空转,这里是闲置系统的最热路径。
	if !m.planSweepArmed.Load() {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// mu 下复查:活跃运行可能在无锁窗口出现。
	if run, err := m.store.ActiveProductWorkflowRun(); err != nil || run != nil {
		return err
	}
	plans, err := m.store.ActiveDailyJobPlans()
	if err != nil {
		return err
	}
	if len(plans) == 0 {
		m.planSweepArmed.Store(false)
		return nil
	}
	for index := range plans {
		if err := m.reconcileOneDailyPlanLocked(plans[index]); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) reconcileOneDailyPlanLocked(bundle store.DailyJobPlanWithEntries) error {
	plan := bundle.Plan
	entries := bundle.Entries
	key := store.AccountKey{Platform: plan.Platform, AccountRef: plan.AccountRef}
	now := m.clock.Now()
	pendingRemain := store.NextPendingDailyJobPlanEntry(entries) != nil

	abort := func(reason string) error {
		if err := m.terminalizeDailyPlanBatchLocked(key, entries, now); err != nil {
			return err
		}
		if err := m.store.AbortDailyJobPlan(plan.PlanID, reason, now); err != nil {
			return err
		}
		slog.Warn("当日职位计划已终止", "planId", plan.PlanID, "reason", reason)
		return nil
	}
	complete := func() error {
		if err := m.store.CompleteDailyJobPlan(plan.PlanID, now); err != nil {
			return err
		}
		slog.Info("当日职位计划已完成", "planId", plan.PlanID)
		return nil
	}

	// 只看本账号最近一次 full 运行:replyOnly 插曲(用户在收口 tick 前点了
	// 「只处理消息」又结束)或将来其他账号的运行,都不得决定本计划生死。
	latest, err := m.store.LatestFullProductWorkflowRun(key)
	if err != nil {
		return err
	}
	if latest == nil || latest.StartedAt.Before(plan.CreatedAt) {
		// 计划从未取得配套运行(建计划后开跑失败且已被清场):按接续中断收口。
		return abort(planEndReasonChainInterrupted)
	}
	entry, err := m.planEntryForRunLocked(latest, entries)
	if err != nil {
		return err
	}

	switch latest.Status {
	case workflow.StatusFailed:
		// 条目 pending 或已 skipped 都走跳过接续:计划序第一个职位离线时,
		// 定稿钩子会先把该条目标成 skipped、随后同一次闸读取才把批次拦停
		// (jobNotOnline)——这是生产上最常见的入场时序,条目此刻已不是
		// pending;SkipDailyJobPlanEntry 对已跳过条目幂等,接续照常。只有
		// done 条目的失败(不可达)与非跳过类原因才终止计划。
		if entry != nil && entry.Status != store.DailyJobPlanEntryDone {
			skipReason, skippable, batchErr := m.dailyPlanSkipReasonLocked(latest)
			if batchErr != nil {
				return batchErr
			}
			if skippable {
				return m.skipEntryAndChainLocked(plan, key, entry, skipReason, now, abort, complete)
			}
		}
		return abort(planEndReasonRunFailed)
	case workflow.StatusCompleted:
		switch latest.EndReason {
		case productWorkflowEndReasonUserEnded:
			if pendingRemain {
				return abort(planEndReasonUserEnded)
			}
			return complete()
		case productWorkflowEndReasonDailyWindowClosed:
			if pendingRemain {
				return abort(planEndReasonDayClosed)
			}
			return complete()
		default:
			// additionalBatch 半消费崩溃、或其他批间空档:按接续中断收口,
			// 不自动重试(出口「允许的失败」)。
			if pendingRemain {
				return abort(planEndReasonChainInterrupted)
			}
			return complete()
		}
	default:
		// 理论上不可达(非终局运行必占 ActiveSlot);保守不动,等下一 tick。
		return nil
	}
}

// dailyPlanSkipReasonLocked 读最近运行的批次终局原因并判定是否跳过类。
func (m *Manager) dailyPlanSkipReasonLocked(
	latest *store.ProductWorkflowRun,
) (string, bool, error) {
	if latest.SourcingBatchID == nil || strings.TrimSpace(*latest.SourcingBatchID) == "" {
		return "", false, nil
	}
	batch, err := m.store.SourcingBatchByID(*latest.SourcingBatchID)
	if err != nil {
		if errors.Is(err, store.ErrSourcingBatchNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	if batch == nil || !planSkipClassBatchReason(batch.Reason) {
		return "", false, nil
	}
	// 「batch:<原因码>|<判定现场>」:码前缀供 UI 归类,竖线后是批次留痕的原话
	// (设计文档「留痕与可见性」:跳过原因必须带判定现场)。
	reason := "batch:" + batch.Reason
	if detail := strings.TrimSpace(batch.ReasonDetail); detail != "" {
		reason += "|" + detail
	}
	return reason, true, nil
}

func (m *Manager) skipEntryAndChainLocked(
	plan store.DailyJobPlan,
	key store.AccountKey,
	entry *store.DailyJobPlanEntry,
	skipReason string,
	now time.Time,
	abort func(string) error,
	complete func() error,
) error {
	if err := m.store.SkipDailyJobPlanEntry(plan.PlanID, entry.Seq, skipReason, now); err != nil {
		return err
	}
	slog.Warn("当日职位计划条目已跳过",
		"planId", plan.PlanID, "seq", entry.Seq, "job", entry.JobName, "reason", skipReason)
	// 被闸拦下的批次多数已终局(stopped);positionSelectFailed 族是 blocked
	// 未终局,必须收尾,否则会被下一次开始当存量批次收养、按配置全额跑掉。
	if err := m.terminalizeDailyPlanBatchLocked(key, nil, now); err != nil {
		return err
	}
	_, entries, err := m.store.ActiveDailyJobPlan(key)
	if err != nil {
		return err
	}
	next := store.NextPendingDailyJobPlanEntry(entries)
	if next == nil {
		return complete()
	}
	started, startErr := m.startFullLocked(key, next.RevisionHash, now)
	if startErr != nil {
		// 窗口已关的判定不依赖错误哨兵身份:开跑链路里 patrol 用自己的
		// ErrDailyWindowNotOpen,不是 workflow.ErrDailyWindowClosed;按当下
		// 窗口实况分类,跨点终止才会被正确记成 dayClosed 而非 chainInterrupted。
		if open, windowErr := m.dailyWindow.Evaluate(now, m.location); windowErr == nil && !open {
			return abort(planEndReasonDayClosed)
		}
		if errors.Is(startErr, workflow.ErrDailyWindowClosed) {
			return abort(planEndReasonDayClosed)
		}
		return errors.Join(abort(planEndReasonChainInterrupted), startErr)
	}
	slog.Info("当日职位计划已接续下一条目",
		"planId", plan.PlanID, "seq", next.Seq, "job", next.JobName,
		"runId", started.RunID)
	return nil
}

// terminalizeDailyPlanBatchLocked 终局化账号上仍未收尾、且属于计划条目的批次。
// entries 传 nil 时按"任何活跃批次都不该在无运行时存活"处理——收口扫描只在
// 无活跃运行时执行,此时未终局批次要么是计划批次要么是中断残留,一律收尾。
func (m *Manager) terminalizeDailyPlanBatchLocked(
	key store.AccountKey,
	entries []store.DailyJobPlanEntry,
	now time.Time,
) error {
	batch, err := m.store.ActiveSourcingBatch(key)
	if err != nil || batch == nil {
		return err
	}
	if entries != nil && store.DailyJobPlanEntryByRevision(entries, batch.ContextRevisionHash) == nil {
		return nil
	}
	if _, err := m.store.StopSourcingBatch(store.StopSourcingBatchRequest{
		BatchID: batch.BatchID, Reason: planBatchStopReasonAborted, StoppedAt: now,
	}); err != nil && !errors.Is(err, store.ErrSourcingBatchStateConflict) {
		return err
	}
	return nil
}
