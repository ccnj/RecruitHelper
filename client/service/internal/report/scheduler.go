package report

import (
	"context"
	"log/slog"
	"time"
)

// 每日任务的时刻与顺延窗口(2026-07-31 补充裁决,2026-09-04 改为按档期打散)。
//
// 凌晨这几分钟落在统一业务运行窗口 [07:00,24:00) 之外,不跟业务抢。但 24 点边界
// 裁决允许"已发出首条可见动作的链自然收束到终局",所以这时可能还有收尾在写库,
// 而快照与 VACUUM 都要抢 SetMaxOpenConns(1) 那唯一的写连接 —— 因此不是到点就干,
// 先看静默。
//
// 具体时刻由调用方传入的 Slot(本机当天的档期起点)加 Offset(本任务在档期内的
// 偏移)决定,见 slot.go。改造前是钉死的 00:05 / 00:10 / 00:20。
const (
	// 顺延放弃窗口。改造前钉死在 02:00,档期打散后必须跟着触发时刻走 —— 档期抽到
	// 01:30 时,固定的 02:00 只剩半小时顺延,会凭空多出一批"没等到静默"的失败。
	deferWindow     = 2 * time.Hour
	deferRetryEvery = 10 * time.Minute
)

// SchedulerDeps 用函数注入而不是接口,与本包既有的 SnapshotFunc 一致:调度器
// 只管"每天到点、等静默、干一次",不认识 store、workflow 这些业务类型。
type SchedulerDeps struct {
	// Slot 返回本机在指定本地日期的档期起点,Offset 是本任务在档期内的偏移。
	// Slot 必须显式传 —— 没有零值兜底,漏传会被 RunScheduler 响亮拒绝并停掉该任务,
	// 而不是静默变成每天 00:00 跑(那种错误在日志里看不出来)。
	Slot   SlotPicker
	Offset time.Duration
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

// nextDailyRun 返回 now 之后最近的一次触发时刻(本地时间)。
// 单独抽出来是因为它是这套东西里唯一容易算错的地方:等于当天时刻时必须顺延到
// 次日,否则会在同一秒里反复触发。档期逐日重抽,所以今天的过了要去问明天的,
// 不能像改造前那样把当天时刻直接加一天。
func nextDailyRun(now time.Time, slot SlotPicker, offset time.Duration) time.Time {
	today := slot(now).Add(offset)
	if today.After(now) {
		return today
	}
	return slot(now.AddDate(0, 0, 1)).Add(offset)
}

// deferDeadline 返回本次触发的放弃时刻(触发后 deferWindow)。
func deferDeadline(runAt time.Time) time.Time {
	return runAt.Add(deferWindow)
}

// RunScheduler 阻塞运行每日任务循环，直到 ctx 结束。
//
// 纪律(全部来自裁决,不要"顺手优化"掉):有开关的任务每轮都重读——人在诊断台
// 关掉之后,下一轮就不该再干;不静默就顺延,过 deferWindow 放弃且当日不补做;
// 客户端没运行而错过的那天不补做——所以这里没有"上次跑到哪天"的追赶逻辑;
// 失败不重试。
func RunScheduler(ctx context.Context, deps SchedulerDeps) {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Slot == nil {
		// 装配漏了档期。停掉这个任务而不是按 00:00 跑:静默地每天半夜零点开跑
		// 是最难发现的那种错。
		slog.Error(deps.Label+":未装配档期，本任务不启动", "errorCode", "dailyTaskSlotMissing")
		return
	}
	for {
		runAt := nextDailyRun(deps.Now(), deps.Slot, deps.Offset)
		// 记下今天抽到的档期。没有这一条,"今天到底该几点传"就只能靠重算哈希,
		// 而排障的人手里往往只有日志。
		slog.Info(deps.Label+":本轮档期已定", "runAt", runAt.Format(time.RFC3339))
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
