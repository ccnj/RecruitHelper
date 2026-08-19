package report

import (
	"context"
	"log/slog"
	"time"
)

// 每日任务的时刻与顺延窗口(2026-07-31 补充裁决)。
//
// 凌晨这几分钟落在统一业务运行窗口 [07:00,24:00) 之外,不跟业务抢。但 24 点边界
// 裁决允许"已发出首条可见动作的链自然收束到终局",所以这时可能还有收尾在写库,
// 而快照与 VACUUM 都要抢 SetMaxOpenConns(1) 那唯一的写连接 —— 因此不是到点就干,
// 先看静默。
//
// 具体时刻由调用方传入(SchedulerDeps.Hour/Minute):现场上报是 00:10,命令审计
// 留存清理是 00:05。清理排在上报前面,好让当天上传的包已经是瘦身后的。
const (
	deferUntilHour  = 2 // 顺延到 02:00 仍不静默就放弃,当日不补做
	deferRetryEvery = 10 * time.Minute
)

// SchedulerDeps 用函数注入而不是接口,与本包既有的 SnapshotFunc 一致:调度器
// 只管"每天到点、等静默、干一次",不认识 store、workflow 这些业务类型。
type SchedulerDeps struct {
	// Hour/Minute 是每日触发时刻(客户端本地时间)。必须显式传 —— 不给零值兜底,
	// 否则漏传会静默变成 00:00 跑,而这种错误在日志里看不出来。
	Hour   int
	Minute int
	// Label 是日志里的任务名,例如"现场上报""命令审计留存"。
	Label string
	// Enabled 读开关。默认关闭是硬约束,读失败一律当关。常开任务传 nil。
	Enabled func() (bool, error)
	// Quiet 判断此刻能不能动库:无活跃工作流、无未收束命令。
	// 返回 false 时 reason 说明卡在哪,进日志与诊断台。
	Quiet func() (quiet bool, reason string)
	// RunOnce 执行一次完整的打包上传。
	RunOnce func(context.Context) error
	// Record 记录本次结果,供诊断台显示。
	Record func(at time.Time, ok bool, reason string)
	Now    func() time.Time
	// RetryEvery 是不静默时的复查间隔,零值用默认 10 分钟。只有测试会设它。
	RetryEvery time.Duration
}

func (d SchedulerDeps) retryEvery() time.Duration {
	if d.RetryEvery > 0 {
		return d.RetryEvery
	}
	return deferRetryEvery
}

// nextDailyRun 返回 now 之后最近的一个每日触发时刻(本地时间)。
// 单独抽出来是因为它是这套东西里唯一容易算错的地方:等于当天时刻时必须顺延到
// 次日,否则会在同一秒里反复触发。
func nextDailyRun(now time.Time, hour, minute int) time.Time {
	today := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if today.After(now) {
		return today
	}
	return today.AddDate(0, 0, 1)
}

// deferDeadline 返回本次触发的放弃时刻(同日 02:00)。
func deferDeadline(runAt time.Time) time.Time {
	return time.Date(runAt.Year(), runAt.Month(), runAt.Day(), deferUntilHour, 0, 0, 0, runAt.Location())
}

// RunScheduler 阻塞运行每日任务循环，直到 ctx 结束。
//
// 纪律(全部来自裁决,不要"顺手优化"掉):有开关的任务每轮都重读——人在诊断台
// 关掉之后,下一轮就不该再干;不静默就顺延,过 02:00 放弃且当日不补做;客户端
// 没运行而错过的那天不补做——所以这里没有"上次跑到哪天"的追赶逻辑;失败不重试。
func RunScheduler(ctx context.Context, deps SchedulerDeps) {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	for {
		runAt := nextDailyRun(deps.Now(), deps.Hour, deps.Minute)
		if !sleepUntil(ctx, deps.Now, runAt) {
			return
		}
		runDailyOnce(ctx, deps, runAt)
	}
}

func runDailyOnce(ctx context.Context, deps SchedulerDeps, runAt time.Time) {
	if deps.Enabled != nil {
		enabled, err := deps.Enabled()
		if err != nil {
			// 读不出开关就当关着。宁可不干,也不能因为读库出错就把候选人明文发出去。
			slog.Warn(deps.Label+":读取开关失败，本轮跳过", "errorCode", "dailyTaskSettingUnavailable", "err", err)
			return
		}
		if !enabled {
			return
		}
	}

	deadline := deferDeadline(runAt)
	for {
		quiet, reason := deps.Quiet()
		if quiet {
			break
		}
		next := deps.Now().Add(deps.retryEvery())
		if !next.Before(deadline) {
			// 顺延到头。这不是失败,是"今天不合适",但仍要让人看得见。
			slog.Warn(deps.Label+":顺延超时，当日放弃", "reason", reason)
			record(deps, deps.Now(), false, "顺延超时："+reason)
			return
		}
		if !sleepUntil(ctx, deps.Now, next) {
			return
		}
	}

	if err := deps.RunOnce(ctx); err != nil {
		// 失败不重试(裁决)。响亮报一笔:无人值守时这是唯一会被看到的地方。
		slog.Error(deps.Label+":执行失败", "errorCode", "dailyTaskFailed", "err", err.Error())
		record(deps, deps.Now(), false, err.Error())
		return
	}
	slog.Info(deps.Label + ":完成")
	record(deps, deps.Now(), true, "")
}

func record(deps SchedulerDeps, at time.Time, ok bool, reason string) {
	if deps.Record == nil {
		return
	}
	deps.Record(at, ok, reason)
}

// sleepUntil 等到 target；ctx 结束返回 false。返回 false 时调用方必须退出循环。
func sleepUntil(ctx context.Context, now func() time.Time, target time.Time) bool {
	wait := target.Sub(now())
	if wait <= 0 {
		return true
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
