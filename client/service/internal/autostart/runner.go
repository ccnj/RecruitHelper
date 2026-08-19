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
	// 触发时刻的随机范围 [07:05:00, 07:30:00],裁决写死,不设配置面。
	slotBaseHour      = 7
	slotBaseMinute    = 5
	slotSpreadSeconds = 25 * 60
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
	if prev.IsZero() {
		// 进程首个检查点只建立基线。脑晚于抽取时刻启动时,下一拍的
		// prev 已在 slot 之后,跨点条件永远不成立 —— 当日自然跳过。
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

func (r *Runner) fire(ctx context.Context, now time.Time) {
	setting, err := r.store.AutoStartSetting()
	if err != nil {
		slog.Warn("每日自动开始:配置读取失败,本日放弃", "err", err)
		return
	}
	if !setting.Enabled {
		return
	}
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	outcome, detail := r.attempt(attemptCtx)
	slog.Info("每日自动开始:当日尝试已收场",
		"outcome", outcome, "detail", detail, "slot", r.slot.Format("15:04:05"))
	if err := r.store.RecordAutoStartAttempt(now, outcome, detail); err != nil {
		slog.Warn("每日自动开始:尝试结果落库失败", "err", err)
	}
}

// attempt 做当日唯一一次完整发起,返回封闭结果码与给设置页看的中文原因。
func (r *Runner) attempt(ctx context.Context) (string, string) {
	active, err := r.store.ActiveProductWorkflowRun()
	if err != nil {
		return store.AutoStartOutcomeError, "读取当前运行状态失败"
	}
	if active != nil {
		// 漏斗跨日挂起:替人点「继续」。其余活跃运行一律不碰 —— 尤其是
		// 沟通阶段的活跃 run,对它调 Start 会被记成「追加采集」而不是开始。
		if active.Status == workflow.StatusWaitingDailyWindow {
			if err := r.control.Resume(ctx); err != nil {
				return store.AutoStartOutcomeResumeFailed, "恢复失败:" + err.Error()
			}
			return store.AutoStartOutcomeResumed, ""
		}
		return store.AutoStartOutcomeSkippedRun, "当前已有运行中的任务"
	}
	latest, err := r.store.LatestProductWorkflowRun()
	if err != nil {
		return store.AutoStartOutcomeError, "读取运行历史失败"
	}
	if latest != nil && localDateOf(latest.StartedAt.In(r.location)) == localDateOf(r.now().In(r.location)) {
		return store.AutoStartOutcomeSkippedToday, "今天已经运行过"
	}
	job, err := r.store.AppCurrentJob()
	if err != nil || !job.Available || job.BackendJobID == "" {
		return store.AutoStartOutcomeStartFailed, "当前没有已绑定职位"
	}
	if err := r.control.Start(ctx, "full", job.BackendJobID); err != nil {
		return store.AutoStartOutcomeStartFailed, productapp.StartFailureText(err)
	}
	return store.AutoStartOutcomeStarted, ""
}

func localDateOf(now time.Time) string {
	return now.Format("2006-01-02")
}
