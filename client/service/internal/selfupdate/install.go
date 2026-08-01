package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/workflow"
)

const (
	// settleTimeout:请求结束工作流之后,等业务真正停下来的上限。
	//
	// 为什么必须等:杀进程时若还有 effectful 命令在途,它的 WAL 会停在 attempting,
	// 下次启动的恢复轨把它收敛成 suspect 转人工 —— 一条本来能正常完成的消息,变成
	// 了要人去判定"到底发出去没有"。
	//
	// 为什么等这么久:Manager.End() 只是往库里登记 PendingAction,真正执行要等下一
	// 轮 AdvanceOnce 并经巡检边界;而且它只关闭**下一个**候选人的边界 —— 当前候选人
	// 会把已授权的建议、动作和 effect WAL 全部跑完(见 MayStartNextConversation 的
	// 注释)。那可能是一次 AI 调用加多气泡逐条发送再加验证读,几十秒起步。
	//
	// 2026-08-01 的教训:原先只等 NonTerminalCmds 归零、上限 60 秒。那个查询看的是
	// **已派发**的命令,恰好落在"AI 调用中、命令尚未派发"的空档就会读到空、立刻放行,
	// 随后派发的那条命令正好被杀。日志上表现为 End 与"准备安装"只隔 2 毫秒。
	settleTimeout = 5 * time.Minute
	settlePoll    = 500 * time.Millisecond

	// pendingInstallFile 记录"已经把安装器交出去了,期待重启后变成这一版"。
	// 它必须跨进程重启存活,否则装崩之后没人知道该停手。
	pendingInstallFile = "pending-install.json"
	// maxInstallAttempts:同一版本装两次都没换上就停手交人工。装-崩-装的循环比
	// 停在旧版严重得多 —— 旧版至少还能用。
	maxInstallAttempts = 2
)

var (
	ErrNoPackageReady   = errors.New("当前没有已备好的新版安装包")
	ErrPackageTampered  = errors.New("本地安装包与下载时的校验值不符，已拒绝执行")
	ErrBusinessInFlight = errors.New(
		"当前任务尚未收尾，本次不安装；等它跑完再试")
	ErrInstallHalted = errors.New("该版本已连续安装失败，已停止自动安装")
)

// GateStore 是安装闸要问账本的两个问题。
type GateStore interface {
	ActiveProductWorkflowRun() (*store.ProductWorkflowRun, error)
	NonTerminalCmds() ([]store.CmdRecord, error)
}

// WorkflowEnder 让安装闸能把正在跑的工作流干净收尾。用户点"立即更新"时已经
// 在二次确认里知道这一步的后果。
type WorkflowEnder interface {
	End(context.Context) error
}

// InstallGate 决定"现在能不能把安装器交出去"。
//
// 它自己不执行安装 —— 脑杀不掉自己,执行必须由壳来做(spawn 安装器后退出)。
// 这里只负责把世界收拾到可以安全重启的状态,然后交出一个已经重新校验过的路径。
type InstallGate struct {
	Store    GateStore
	Workflow WorkflowEnder
	Checker  *Checker
	// StateDir 放 pending-install.json,与安装包同目录。
	StateDir string

	Timeout time.Duration
	Poll    time.Duration
	Now     func() time.Time
}

// PendingInstall 是交出安装器时留下的字条,供下次启动核对。
type PendingInstall struct {
	Version   string    `json:"version"`
	From      string    `json:"from"`
	StartedAt time.Time `json:"startedAt"`
	Attempts  int       `json:"attempts"`
}

func (g *InstallGate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *InstallGate) timeout() time.Duration {
	if g.Timeout > 0 {
		return g.Timeout
	}
	return settleTimeout
}

func (g *InstallGate) poll() time.Duration {
	if g.Poll > 0 {
		return g.Poll
	}
	return settlePoll
}

func (g *InstallGate) pendingPath() string {
	return filepath.Join(g.StateDir, pendingInstallFile)
}

// Prepare 把世界收拾到可以重启的状态,返回可执行的安装包路径。
//
// 顺序是有意的:先确认有东西可装、且它没被动过,再去动业务。反过来的话,一次
// 因为包坏了而注定失败的更新,会白白把用户正在跑的批次结束掉。
func (g *InstallGate) Prepare(ctx context.Context) (string, error) {
	// g 自己可能是 nil:一个 nil 的具体指针塞进接口字段会变成"非 nil 的接口值",
	// 调用方的 nil 检查就形同虚设。判在解引用之前,nil receiver 是合法的。
	if g == nil || g.Store == nil || g.Checker == nil || g.StateDir == "" {
		return "", errors.New("安装编排尚未就绪")
	}

	packagePath, wantSHA, version, ok := g.Checker.ReadyPackage()
	if !ok {
		return "", ErrNoPackageReady
	}
	// 下载与安装之间隔着任意长的时间。批 1 下载时校验过,但那是过去的事,而这是
	// 个马上要以静默方式执行的文件 —— 再验一次。
	if err := verifyFile(packagePath, wantSHA); err != nil {
		return "", fmt.Errorf("%w: %v", ErrPackageTampered, err)
	}
	if pending, err := g.readPending(); err == nil && pending != nil &&
		pending.Version == version && pending.Attempts >= maxInstallAttempts {
		return "", ErrInstallHalted
	}

	if err := g.endActiveWorkflow(ctx); err != nil {
		return "", err
	}
	if err := g.waitForBusinessToSettle(ctx); err != nil {
		return "", err
	}

	if err := g.writePending(PendingInstall{
		Version: version, From: g.Checker.CurrentVersion, StartedAt: g.now(),
	}); err != nil {
		// 字条写不下就不该交出安装器:装崩之后没有任何依据判断该不该停手,
		// 会形成装-崩-装的循环。
		return "", fmt.Errorf("记录安装意图失败，已放弃本次安装: %w", err)
	}
	slog.Warn("准备安装新版客户端，即将退出",
		"version", version, "from", g.Checker.CurrentVersion)
	return packagePath, nil
}

// endActiveWorkflow 只是**请求**结束:Manager.End() 往库里登记 PendingAction 就
// 返回,真正执行要等下一轮 AdvanceOnce 并经巡检边界。所以调完它业务并没有停,
// 停没停一律由 settleBlocker 说了算。
func (g *InstallGate) endActiveWorkflow(ctx context.Context) error {
	run, err := g.Store.ActiveProductWorkflowRun()
	if err != nil {
		return err
	}
	if run == nil || (run.Status != workflow.StatusRunning &&
		run.Status != workflow.StatusAwaitingConfirmation) {
		return nil
	}
	if g.Workflow == nil {
		return errors.New("当前有运行中的任务，但工作流控制尚未就绪")
	}
	slog.Warn("用户确认更新，先结束当前运行", "runId", run.RunID, "status", string(run.Status))
	return g.Workflow.End(ctx)
}

// waitForBusinessToSettle 等业务真正停下来。超时就放弃本次安装 —— 宁可不装,
// 也不把一条正在发送的消息杀成 suspect。
func (g *InstallGate) waitForBusinessToSettle(ctx context.Context) error {
	deadline := time.NewTimer(g.timeout())
	defer deadline.Stop()
	ticker := time.NewTicker(g.poll())
	defer ticker.Stop()
	for {
		blocker, err := g.settleBlocker()
		if err != nil {
			return err
		}
		if blocker == "" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("%w（%s）", ErrBusinessInFlight, blocker)
		case <-ticker.C:
		}
	}
}

// settleBlocker 返回还拦着安装的那件事,空字符串表示可以动手了。
//
// 三个条件缺一不可,而且顺序有讲究:
//
//   - PendingAction 非空 —— 结束请求登记了但还没执行。这一条最容易被漏:End()
//     写完就返回,此刻 Status 仍是 running,只看 Status 会以为还没结束、只看
//     "结束调用成功了"又会以为已经结束。
//   - Status 仍在跑 —— 工作流还没走到终局。
//   - 账本上还有未收束命令 —— 已经派发出去的那些。
//
// 只查第三条是不够的:它看的是**已派发**的命令,而当前候选人可能正卡在 AI 调用里,
// 命令还没铸出来,账本此刻就是空的。前两条挡住的正是这个空档。
func (g *InstallGate) settleBlocker() (string, error) {
	run, err := g.Store.ActiveProductWorkflowRun()
	if err != nil {
		return "", err
	}
	if run != nil {
		if run.PendingAction != "" {
			return "结束请求尚未执行完", nil
		}
		if run.Status == workflow.StatusRunning ||
			run.Status == workflow.StatusAwaitingConfirmation {
			return "工作流仍在运行", nil
		}
	}
	pending, err := g.Store.NonTerminalCmds()
	if err != nil {
		return "", err
	}
	if len(pending) != 0 {
		return fmt.Sprintf("仍有 %d 条未收束命令", len(pending)), nil
	}
	return "", nil
}

func (g *InstallGate) readPending() (*PendingInstall, error) {
	raw, err := os.ReadFile(g.pendingPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var pending PendingInstall
	if err := json.Unmarshal(raw, &pending); err != nil {
		return nil, err
	}
	return &pending, nil
}

func (g *InstallGate) writePending(pending PendingInstall) error {
	if err := os.MkdirAll(g.StateDir, 0o755); err != nil {
		return err
	}
	// 保留已有的失败计数:这次可能正是第二次尝试。
	if existing, err := g.readPending(); err == nil && existing != nil &&
		existing.Version == pending.Version {
		pending.Attempts = existing.Attempts
	}
	pending.Attempts++
	raw, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	return os.WriteFile(g.pendingPath(), raw, 0o600)
}

// ConfirmResult 是启动时核对的结论。
type ConfirmResult struct {
	Attempted bool
	Version   string
	Succeeded bool
	Attempts  int
	// Halted 为真表示同一版本已经失败到上限，不再自动安装。
	Halted bool
}

// ConfirmPendingInstall 在脑启动时核对上一次安装到底成没成。
//
// 判据只有一个:当前跑着的版本是不是当初期待的那一版。装完自启失败、安装器被
// 杀、覆盖到一半 —— 表现都是"版本没变",不必区分,处置一样。
func ConfirmPendingInstall(stateDir, currentVersion string) (ConfirmResult, error) {
	gate := &InstallGate{StateDir: stateDir}
	pending, err := gate.readPending()
	if err != nil {
		// 字条读不动就当没有:它只是诊断与防循环的依据,不该拦住脑启动。
		return ConfirmResult{}, err
	}
	if pending == nil {
		return ConfirmResult{}, nil
	}
	result := ConfirmResult{
		Attempted: true, Version: pending.Version, Attempts: pending.Attempts,
	}
	if CompareVersions(currentVersion, pending.Version) == 0 {
		result.Succeeded = true
		slog.Info("上次自动安装已生效", "version", pending.Version, "from", pending.From)
		return result, os.Remove(gate.pendingPath())
	}
	result.Halted = pending.Attempts >= maxInstallAttempts
	if result.Halted {
		slog.Error("自动安装连续失败，已停止自动重试，请人工处理",
			"version", pending.Version, "current", currentVersion, "attempts", pending.Attempts)
	} else {
		slog.Warn("上次自动安装未生效，仍在旧版",
			"expected", pending.Version, "current", currentVersion, "attempts", pending.Attempts)
	}
	return result, nil
}
