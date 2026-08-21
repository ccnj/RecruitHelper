// Package autostart 实现「每日自动开始」(2026-08-19 甲方裁决,AGENTS.md 统一
// 业务运行窗口条款):用户在产品设置页显式开启后,脑每日在 07:05~07:30 内随机
// 抽一个时刻,仅在运行中跨过该时刻的检查点替人点一次「开始全流程」;漏斗跨日
// 挂起(waitingDailyWindow)时改点「继续」。每日至多完整尝试一次,成败皆止,
// 不重试、不补发;脑晚于抽取时刻启动或当日已有任何运行,一律跳过,隔日不补。
//
// 发起复用与人工点击完全相同的控制面入口(productapp.Controller),所有前置闸
// (窗口、插件在线、登录、微信配置、职位一致与在线、活跃互斥)自动继承,本包
// 不新增任何绕闸路径;失效方向永远是"不开工"。
package autostart

import (
	"context"
	"log/slog"
	"math/rand"
	"time"

	"recruithelper/client/service/internal/productapp"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/workflow"
)

const (
	defaultTickInterval = 30 * time.Second
	// 触发时刻的随机范围 [窗口开点+5 分钟, +30 分钟],即当前 [07:05, 07:30]。
	// 基点从 DailyStartHour 推导:窗口起点若再修宪,触发带自动跟随,
	// 不会留下"每天在窗口外发起、每天被拦"的暗账。
	slotBaseHour      = workflow.DailyStartHour
	slotBaseMinute    = 5
	slotSpreadSeconds = 25 * 60
	// 相邻检查点的最大可信间隔。超过它说明进程刚经历睡眠/挂起补拍,
	// "运行中跨过该时刻"不成立——睡过触发时刻的一律按晚启动对待,
	// 否则下午唤醒会在计划外时刻无人在场开工(方向朝多做)。
	staleTickGap = 2 * time.Minute
	// 一次完整发起的兜底上限。控制面内部的探测与页面读取各有自己的超时,
	// 这里只防整条链悬死把检查点循环卡住,不是节奏参数。
	attemptTimeout = 5 * time.Minute
)

// Control 是人工「开始/继续」按钮背后的同一控制面子集。
type Control interface {
	Start(ctx context.Context, mode string, backendJobID string) error
	Resume(ctx context.Context) error
}

// Store 是本包需要的最小只读/记录面。
type Store interface {
	AutoStartSetting() (store.AutoStartSetting, error)
	RecordAutoStartAttempt(at time.Time, outcome, detail string) error
	ActiveProductWorkflowRun() (*store.ProductWorkflowRun, error)
	LatestProductWorkflowRun() (*store.ProductWorkflowRun, error)
	AppCurrentJob() (store.AppJobProjection, error)
}

type Runner struct {
	store    Store
	control  Control
	now      func() time.Time
	location *time.Location
	tick     time.Duration
	rng      *rand.Rand

	// 进程内状态。lastTick 为零值表示本进程还没有过检查点基线;slotDate 标记
	// slot 是为哪个本地日期抽的。都不持久化:脑重启后按"晚启动跳过"自然收敛,
	// 不需要跨进程幂等。
	lastTick time.Time
	slotDate string
	slot     time.Time
	// firedDate 挡住同日重复发起(理论上跨点只发生一次,这是对时钟回拨等
	// 异常的保守兜底,方向是少做)。
	firedDate string
}

type Config struct {
	Store    Store
	Control  Control
	Now      func() time.Time
	Location *time.Location
	Tick     time.Duration
	// Seed 只为测试注入确定性;生产装配传 0 时用时钟播种。
	Seed int64
}

func NewRunner(cfg Config) *Runner {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	location := cfg.Location
	if location == nil {
		location = time.Local
	}
	tick := cfg.Tick
	if tick <= 0 {
		tick = defaultTickInterval
	}
	seed := cfg.Seed
	if seed == 0 {
		seed = now().UnixNano()
	}
	return &Runner{
		store:    cfg.Store,
		control:  cfg.Control,
		now:      now,
		location: location,
		tick:     tick,
		rng:      rand.New(rand.NewSource(seed)),
	}
}

func (r *Runner) Run(ctx context.Context) {
	ticker := time.NewTicker(r.tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.TickOnce(ctx)
		}
	}
}

// TickOnce 是单个检查点。导出给测试用注入时钟逐拍驱动,生产只经 Run 调用。
func (r *Runner) TickOnce(ctx context.Context) {
	now := r.now().In(r.location)
	prev := r.lastTick
	r.lastTick = now
	r.ensureSlot(now)
	if prev.IsZero() || now.Sub(prev) > staleTickGap {
		// 基线缺失(进程首拍)或检查点断流(睡眠/挂起后补拍):本拍只重建
		// 基线,"运行中跨过该时刻"不成立。若时刻已被错过,当日跳过,但要
		// 把"为什么没开"落库 —— 设置页对无人值守早晨的唯一交代在这里。
		if !now.Before(r.slot) {
			r.recordMissedSlot(now)
		}
		return
	}
	if !prev.Before(r.slot) || now.Before(r.slot) {
		return
	}
	today := localDateOf(now)
	if r.firedDate == today {
		return
	}
	r.firedDate = today
	r.fire(ctx, now)
}

// ensureSlot 为当前本地日期抽取当日触发时刻;跨日重抽。
func (r *Runner) ensureSlot(now time.Time) {
	date := localDateOf(now)
	if r.slotDate == date {
		return
	}
	r.slotDate = date
	offset := time.Duration(r.rng.Intn(slotSpreadSeconds+1)) * time.Second
	r.slot = time.Date(
		now.Year(), now.Month(), now.Day(),
		slotBaseHour, slotBaseMinute, 0, 0, r.location,
	).Add(offset)
}

// recordMissedSlot 给"到点时脑没在运行"的当日跳过留痕。只在开关开、当日
// 尚无尝试记录时写一次;它消耗当日(与真实尝试同款按日幂等),不触发任何发起。
func (r *Runner) recordMissedSlot(now time.Time) {
	today := localDateOf(now)
	if r.firedDate == today {
		return
	}
	r.firedDate = today
	setting, err := r.store.AutoStartSetting()
	if err != nil || !setting.Enabled {
		return
	}
	if setting.LastAttemptAt != nil &&
		localDateOf(setting.LastAttemptAt.In(r.location)) == today {
		return
	}
	slog.Info("每日自动开始:错过当日触发时刻,已跳过", "slot", r.slot.Format("15:04:05"))
	if err := r.store.RecordAutoStartAttempt(
		now, store.AutoStartOutcomeMissedSlot, "到点时客户端未在运行",
	); err != nil {
		slog.Warn("每日自动开始:跳过记录落库失败", "err", err)
	}
}

func (r *Runner) fire(ctx context.Context, now time.Time) {
	setting, err := r.store.AutoStartSetting()
	if err != nil {
		slog.Warn("每日自动开始:配置读取失败,本日放弃", "err", err)
		return
	}
	if !setting.Enabled {
		return
	}
	if setting.LastAttemptAt != nil &&
		localDateOf(setting.LastAttemptAt.In(r.location)) == localDateOf(now) {
		// 进程外已消耗过当日尝试:脑在触发带内重启会重抽时刻,若只靠
		// 进程内状态,失败的尝试会被重启变成重试。"每日至多尝试一次,
		// 成败皆止"以落库记录为准。
		return
	}
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	outcome, detail := r.attempt(attemptCtx, now)
	slog.Info("每日自动开始:当日尝试已收场",
		"outcome", outcome, "detail", detail, "slot", r.slot.Format("15:04:05"))
	if err := r.store.RecordAutoStartAttempt(now, outcome, detail); err != nil {
		slog.Warn("每日自动开始:尝试结果落库失败", "err", err)
	}
}

// attempt 做当日唯一一次完整发起,返回封闭结果码与给设置页看的中文原因。
// 原因文案与人工路径同一张映射表,底层错误链只进日志、不进产品面。
func (r *Runner) attempt(ctx context.Context, now time.Time) (string, string) {
	active, err := r.store.ActiveProductWorkflowRun()
	if err != nil {
		slog.Warn("每日自动开始:读取当前运行状态失败", "err", err)
		return store.AutoStartOutcomeError, "读取当前运行状态失败"
	}
	if active != nil {
		// 漏斗跨日挂起:替人点「继续」。其余活跃运行一律不碰 —— 尤其是
		// 沟通阶段的活跃 run,对它调 Start 会被记成「追加采集」而不是开始。
		if active.Status == workflow.StatusWaitingDailyWindow {
			if err := r.control.Resume(ctx); err != nil {
				slog.Warn("每日自动开始:自动恢复失败", "err", err)
				return store.AutoStartOutcomeResumeFailed, "当前状态无法恢复工作流"
			}
			return store.AutoStartOutcomeResumed, ""
		}
		return store.AutoStartOutcomeSkippedRun, "当前已有未完成的任务"
	}
	latest, err := r.store.LatestProductWorkflowRun()
	if err != nil {
		slog.Warn("每日自动开始:读取运行历史失败", "err", err)
		return store.AutoStartOutcomeError, "读取运行历史失败"
	}
	// 「当日已运行」只认开始时刻(2026-08-21 甲方裁决)。曾按审查意见把
	// "终局时刻在今天"也算作今天运行过,真机首日即翻车:跨日沟通运行永远
	// 在午夜后零点几秒被收编(dailyWindowClosed),终局必然落在"今天",
	// 于是每个正常干满到 24 点的工作日,次日早晨都被跳过。甲方裁决砍掉
	// 该概念,接受它曾保护的窄场景(清晨人工恢复又收场后机器再开一轮 ——
	// 发送仍卡在候选确认等人,人在场随手可停)。
	if latest != nil && localDateOf(latest.StartedAt.In(r.location)) == localDateOf(now) {
		return store.AutoStartOutcomeSkippedToday, "今天已经运行过"
	}
	job, err := r.store.AppCurrentJob()
	if err != nil {
		slog.Warn("每日自动开始:读取当前职位失败", "err", err)
		return store.AutoStartOutcomeError, "读取当前职位失败"
	}
	if !job.Available || job.BackendJobID == "" {
		return store.AutoStartOutcomeStartFailed, "当前没有已绑定职位"
	}
	if err := r.control.Start(ctx, string(workflow.ModeFull), job.BackendJobID); err != nil {
		slog.Warn("每日自动开始:开始被拒", "err", err)
		return store.AutoStartOutcomeStartFailed, productapp.StartFailureText(err)
	}
	return store.AutoStartOutcomeStarted, ""
}

func localDateOf(now time.Time) string {
	return now.Format("2006-01-02")
}
