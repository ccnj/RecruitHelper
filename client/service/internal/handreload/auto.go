package handreload

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"recruithelper/client/service/internal/session"
	"recruithelper/client/service/internal/workflow"
	"recruithelper/contract/gen/go/protocol"
)

// DefaultInterval:插件对不上时要么业务停住,要么正带着旧代码跑,所以评估要够勤;
// 但每轮要读一次库和一次 manifest,也没必要更密。
//
// 代价是换代最多要等这么久才被发现:2026-07-30 真机首验里,客户端启动到插件换代
// 完成隔了 30 秒,其间 chrome://extensions 显示的仍是旧版本号。
const DefaultInterval = 30 * time.Second

// AutoReloader 在客户端换代之后替人点掉那一下 Chrome 重载。
//
// 它解决的是一个具体场景:装完新版包、客户端重启、pluginSeed 已经把新插件写进固定
// 目录,但 Chrome 里跑的还是旧代码,而现场没人知道要去 chrome://extensions 点一次
// 刷新。这个场景有两副面孔:
//
//   - 改了契约的版本:contractMatch 为假,effectful 全部禁派 —— 安全,但业务静止;
//   - 没改契约的版本(绝大多数):什么都不挡,业务带着旧插件代码照常跑,更隐蔽。
//
// 两者都要认出来,判据见 staleReason。
//
// 两道闸决定了它绝不会没完没了地重载:
//
//  1. 同一 (handID, bootID) 至多自动派发一次。手没换代过就不会被重复打扰。
//  2. 命令已经派发之后的任何失败(超时、换代后契约仍不一致、能力丢失)都让这只手
//     就此停手,交人工。这是协议规格 §14.1 的直接落地:维护命令的超时与歧义不进
//     通用重试链。
//
// 两道闸都只在内存里。脑重启会清空它们 —— 这是有意的:脑重启本身是人工事件(装
// 了新版或手工重启),那时重新评估一次是对的,而不是继承上一条命的判断。
type AutoReloader struct {
	orchestrator *Orchestrator
	store        Store
	interval     time.Duration
	// wake:手刚 ready 的提醒,缓冲 1。它只说"该看一眼了",不带 handID ——
	// 每次评估都全量扫描注册表,所以合并或丢弃提醒都不会漏掉任何一只手。
	wake chan struct{}

	mu           sync.Mutex
	attempted    map[string]string // handID -> 已经为哪个 bootID 派发过
	halted       map[string]string // handID -> 停手原因(等人工处理)
	lastExpected string            // 上次读到的磁盘插件版本,只为日志去重
	expectedSeen bool
}

// NewAutoReloader。磁盘上的插件版本由 orchestrator.PluginDir 决定 —— 触发判断与
// 成功判断因此共用同一个来源,不会各看各的。
func NewAutoReloader(
	orchestrator *Orchestrator, st Store, interval time.Duration,
) *AutoReloader {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &AutoReloader{
		orchestrator: orchestrator,
		store:        st,
		interval:     interval,
		wake:         make(chan struct{}, 1),
		attempted:    map[string]string{},
		halted:       map[string]string{},
	}
}

// NotifyHandReady 接在 session.Hub 的 ready 钩子上,让换代不必干等下一个 tick。
//
// 非阻塞:提醒已在队列里就直接丢掉。它跑在手的读循环上,多花一纳秒都是在拖慢
// 整条链路;而丢掉是安全的 —— 队列里那个提醒会带来同样的全量扫描。
func (a *AutoReloader) NotifyHandReady(string) {
	if a == nil || a.wake == nil {
		return
	}
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// Run 周期评估,直到 ctx 取消。
//
// 两路触发共用一个 goroutine,因此 EvaluateOnce 永远串行 —— tick 与 wake 撞在
// 一起只是"先做一个,回来再做另一个"。第二次评估必然空转:该做的第一次已经做完,
// 再来一次会被 staleReason(已经对上)、halted 或 attempted 挡住。
func (a *AutoReloader) Run(ctx context.Context) {
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.EvaluateOnce(ctx)
		case <-a.wake:
			a.EvaluateOnce(ctx)
		}
	}
}

// Outcome 是一轮评估的结论,给测试和日志用。
type Outcome struct {
	// Triggered 为真表示这轮真的派发了一次重载。
	Triggered bool
	HandID    string
	// Reason 是触发本次重载的信号(契约还是版本)。它同时进日志 —— 两者的排查
	// 方向完全不同,报串会让排查一开始就走错方向。
	Reason string
	Result Result
	Err    *Error
	// Skipped 说明这轮为什么没动手(没有目标时为空)。
	Skipped string
}

// EvaluateOnce 评估一轮,至多处理一只手。
//
// 一轮只动一只手:重载会终止该手的旧推荐流,连着处理多只手意味着一次评估造成多处
// 业务中断,而下一轮 30 秒后就到。
func (a *AutoReloader) EvaluateOnce(ctx context.Context) Outcome {
	if a.orchestrator == nil || a.orchestrator.Registry == nil || a.store == nil {
		return Outcome{Skipped: "编排未就绪"}
	}

	// 活跃工作流期间绝不自动重载:重载会作废旧推荐流,等于把用户正在跑的这一批
	// 丢掉。人工点按钮时人自己承担这个后果,自动触发不行。
	run, err := a.store.ActiveProductWorkflowRun()
	if err != nil {
		slog.Warn("自动重载:读取活跃工作流失败，本轮跳过", "err", err)
		return Outcome{Skipped: "读取活跃工作流失败"}
	}
	if run != nil && (run.Status == workflow.StatusRunning ||
		run.Status == workflow.StatusAwaitingConfirmation) {
		return Outcome{Skipped: "存在活跃产品工作流"}
	}

	// 磁盘上是哪一版,是壳安置插件之后的既成事实(pluginSeed 在起脑之前就跑完了)。
	// 读文件放在锁外。
	expected := a.orchestrator.expectedVersion()

	target, reason, skipped := a.pickTarget(expected)
	if target == nil {
		return Outcome{Skipped: skipped}
	}

	a.mu.Lock()
	a.attempted[target.HandID] = target.BootID
	a.mu.Unlock()

	// 契约与版本是两个独立信号,日志必须说清是哪一个触发的:报串会让排查一开始
	// 就走错方向 —— 2026-07-30 真机首验时,契约明明一致(两个 hash 一模一样)而
	// 日志却写着"契约不一致",正是这条的实证。
	slog.Warn("插件需要换代，自动重载",
		"reason", reason, "handId", target.HandID, "bootId", target.BootID,
		"handVersion", target.ExtVersion, "diskVersion", expected,
		"handContract", target.ContractHash, "brainContract", protocol.ContractHash)

	result, reloadErr := a.orchestrator.Reload(ctx, target.HandID)
	if reloadErr != nil {
		if reloadErr.Kind.Dispatched() {
			a.mu.Lock()
			a.halted[target.HandID] = reloadErr.Message
			a.mu.Unlock()
			slog.Error("自动重载已派发但未收敛，就此停手交人工处理",
				"handId", target.HandID, "msgId", reloadErr.MsgID,
				"kind", string(reloadErr.Kind), "err", reloadErr.Message)
		} else {
			// 派发前被判据挡下:是暂时条件,下轮重来。清掉尝试记录,否则这只手会
			// 卡在"已尝试过这个 bootID"上再也轮不到。
			a.mu.Lock()
			delete(a.attempted, target.HandID)
			a.mu.Unlock()
			slog.Info("自动重载本轮未通过前置判据，稍后重试",
				"handId", target.HandID, "kind", string(reloadErr.Kind), "err", reloadErr.Message)
		}
		return Outcome{Triggered: true, HandID: target.HandID, Reason: reason, Err: reloadErr}
	}

	slog.Info("插件已自动换代",
		"handId", result.HandID, "previousBootId", result.PreviousBootID,
		"bootId", result.BootID, "extVersion", result.ExtensionVersion)
	return Outcome{Triggered: true, HandID: target.HandID, Reason: reason, Result: result}
}

// staleReason 说明这只手为什么算过时,空字符串表示不过时。两个独立信号:
//
//  1. 契约对不上。只有改了契约的版本才会让它变,但它是硬信号 —— 此时 effectful
//     已经被禁派,业务是停住的。
//  2. 版本对不上。磁盘上已经换成新版而手还报着旧版号。绝大多数更新不动契约,
//     这一条才是常态;而且此时业务并没有停,是带着旧插件代码照常跑,更隐蔽。
//
// 返回的是原因而不是布尔,因为日志必须说清是哪个信号触发的 —— 两者的排查方向
// 完全不同。
//
// 任一方"不知道"就不算过时:磁盘版本读不到(expected 为空)或手根本没报版本,
// 都是缺证据,不是反证。宁可不重载,也不为一个读不出来的文件去动推荐流。
func staleReason(state session.HandState, expectedVersion string) string {
	if !state.ContractMatch || state.ContractHash != protocol.ContractHash {
		return "契约与当前脑不一致"
	}
	if expectedVersion != "" && state.ExtVersion != "" &&
		state.ExtVersion != expectedVersion {
		return "版本落后于磁盘上的插件"
	}
	return ""
}

// pickTarget 找出这轮该重载的手与原因,顺便清理已经恢复正常的手的记录。
func (a *AutoReloader) pickTarget(
	expectedVersion string,
) (*session.HandState, string, string) {
	capability := Capability()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.noteExpectedVersion(expectedVersion)

	var target *session.HandState
	targetReason := ""
	skipped := ""
	for _, state := range a.orchestrator.Registry.Snapshot() {
		reason := staleReason(state, expectedVersion)
		if reason == "" {
			// 这只手已经跑在对的插件上,不管是自动修好的还是人工重载的,记录都
			// 该清干净,这样下次换代时它能重新参与。
			delete(a.attempted, state.HandID)
			delete(a.halted, state.HandID)
			continue
		}
		if !state.Online || state.Health != session.HealthReady {
			continue
		}
		if !hasString(state.Caps, capability) {
			// 老插件没有 debug.reload@1,只能人工重载最后一次。
			skipped = "目标手不具备一键重载能力"
			continue
		}
		if haltReason, stopped := a.halted[state.HandID]; stopped {
			skipped = "该手已停手待人工处理：" + haltReason
			continue
		}
		if a.attempted[state.HandID] == state.BootID {
			skipped = "该 bootID 已自动重载过"
			continue
		}
		if target == nil {
			copied := state
			target = &copied
			targetReason = reason
		}
	}
	return target, targetReason, skipped
}

// noteExpectedVersion 只在磁盘版本读数变化时说一句话 —— 每 30 秒重复同一行
// 只会把日志刷成噪音。调用方持有 a.mu。
func (a *AutoReloader) noteExpectedVersion(expected string) {
	if a.expectedSeen && expected == a.lastExpected {
		return
	}
	a.lastExpected = expected
	a.expectedSeen = true
	if expected == "" {
		if a.orchestrator.PluginDir != "" {
			slog.Warn("读不到固定目录里的插件版本，只按契约判断是否需要重载",
				"pluginDir", a.orchestrator.PluginDir)
		}
		return
	}
	slog.Info("固定目录里的插件版本", "version", expected, "pluginDir", a.orchestrator.PluginDir)
}

// Halted 报告某只手是否已经停手待人工。诊断用。
func (a *AutoReloader) Halted(handID string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	reason, ok := a.halted[handID]
	return reason, ok
}
