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
	// settleTimeout:结束工作流之后等在途命令收敛的上限。
	//
	// 为什么必须等:End 只是把工作流置为终局,立刻就返回,手上可能还有命令在跑。
	// 这时候杀进程,effectful 命令的 WAL 停在 attempting,下次启动的恢复轨会把它
	// 收敛成 suspect 转人工 —— 一条本来能正常完成的消息,变成了要人去判定"到底
	// 发出去没有"。等待是有界的:End 之后不再产生新命令,在途的会自然收敛。
	settleTimeout = 60 * time.Second
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
	ErrCommandsInFlight = errors.New("仍有未收束的命令，本次不安装")
	ErrInstallHalted    = errors.New("该版本已连续安装失败，已停止自动安装")
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
	if err := g.waitForCommandsToSettle(ctx); err != nil {
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

// waitForCommandsToSettle 等在途命令归零。超时就放弃本次安装 —— 宁可不装,
// 也不把一条正在发送的消息杀成 suspect。
func (g *InstallGate) waitForCommandsToSettle(ctx context.Context) error {
	deadline := time.NewTimer(g.timeout())
	defer deadline.Stop()
	ticker := time.NewTicker(g.poll())
	defer ticker.Stop()
	for {
		pending, err := g.Store.NonTerminalCmds()
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("%w（仍有 %d 条）", ErrCommandsInFlight, len(pending))
		case <-ticker.C:
		}
	}
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
