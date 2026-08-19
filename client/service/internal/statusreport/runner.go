package statusreport

import (
	"context"
	"log/slog"
	"time"
)

const (
	// Interval 是上报间隔(裁决)。不做夜间降频:心跳的价值一半在"我还活着",
	// 降频会让离线判定的阈值跟着变复杂。
	Interval = 5 * time.Minute
	// failureDigestEvery 是持续失败时的汇总间隔。见 noiseGate。
	failureDigestEvery = time.Hour
)

// RunnerDeps 是常驻上报循环的依赖。
type RunnerDeps struct {
	Deps
	// Target 给出上报去处与身份。ready=false 表示授权还没就绪(没绑定、没
	// licenseToken),此时**静默跳过**:全新安装到激活之间会有一段时间,那不是
	// 故障,不该每 5 分钟往日志里写一条。
	Target func() (target Target, ready bool)
	// Upload 默认走 HTTP;测试替换它。
	Upload func(context.Context, *Payload, Target) error
	// Interval 零值用默认 5 分钟。只有测试会设它。
	Interval time.Duration
}

func (d RunnerDeps) interval() time.Duration {
	if d.Interval > 0 {
		return d.Interval
	}
	return Interval
}

func (d RunnerDeps) upload(ctx context.Context, payload *Payload, target Target) error {
	if d.Upload != nil {
		return d.Upload(ctx, payload, target)
	}
	return Upload(ctx, payload, target)
}

// Run 阻塞运行上报循环,直到 ctx 结束。
//
// 纪律(来自裁决,不要"顺手优化"掉):不重试 —— 失败就等下一轮,反正报的是当前
// 累计快照,丢一次由下一次自愈;不受统一业务运行窗口约束 —— 上报不是候选人可见
// 动作,[00:00,07:00) 照常报;不设开关 —— 没有可以关掉它的地方。
func Run(ctx context.Context, deps RunnerDeps) {
	gate := &noiseGate{digestEvery: failureDigestEvery}
	ticker := time.NewTicker(deps.interval())
	defer ticker.Stop()

	// 启动即报一次:客户端刚起来那五分钟也该在管理前台看得见。
	runOnce(ctx, deps, gate)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce(ctx, deps, gate)
		}
	}
}

func runOnce(ctx context.Context, deps RunnerDeps, gate *noiseGate) {
	if deps.Target == nil {
		return
	}
	target, ready := deps.Target()
	if !ready {
		return
	}
	payload, err := Collect(deps.Deps)
	if err == nil {
		err = deps.upload(ctx, payload, target)
	}
	log(gate.decide(err == nil, deps.now()), err)
}

func log(action noiseAction, err error) {
	switch action.kind {
	case noiseError:
		slog.Error("工作状态上报失败", "errorCode", "statusReportFailed", "err", errText(err))
	case noiseDigest:
		slog.Warn("工作状态上报持续失败",
			"errorCode", "statusReportFailed",
			"failures", action.failures,
			"err", errText(err),
		)
	case noiseRecovered:
		slog.Info("工作状态上报已恢复", "failures", action.failures)
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type noiseKind int

const (
	noiseSilent noiseKind = iota
	noiseError
	noiseDigest
	noiseRecovered
)

type noiseAction struct {
	kind     noiseKind
	failures int
}

// noiseGate 决定这一轮该不该写日志。
//
// 立这道闸的原因很具体:常开 + 5 分钟一轮,后台一旦连不上就是一天 288 条 ERROR,
// 而 brain.log 32MB 就轮转 —— 上报的噪音会把真正的业务日志挤出保留窗口,那正是
// 出问题时要翻的东西。所以:失败第一次响亮报(状态翻转),之后每小时汇总一条,
// 恢复时报一条。中间的沉默不丢信息,汇总里带着累计次数。
type noiseGate struct {
	failing      bool
	failures     int
	lastLoggedAt time.Time
	digestEvery  time.Duration
}

func (g *noiseGate) decide(ok bool, now time.Time) noiseAction {
	if ok {
		if !g.failing {
			return noiseAction{kind: noiseSilent}
		}
		failures := g.failures
		g.failing = false
		g.failures = 0
		g.lastLoggedAt = time.Time{}
		return noiseAction{kind: noiseRecovered, failures: failures}
	}

	g.failures++
	if !g.failing {
		g.failing = true
		g.lastLoggedAt = now
		return noiseAction{kind: noiseError, failures: g.failures}
	}
	if g.digestEvery > 0 && !now.Before(g.lastLoggedAt.Add(g.digestEvery)) {
		g.lastLoggedAt = now
		return noiseAction{kind: noiseDigest, failures: g.failures}
	}
	return noiseAction{kind: noiseSilent}
}
