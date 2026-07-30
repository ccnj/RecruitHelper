package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/workflow"
)

type fakeGateStore struct {
	run     *store.ProductWorkflowRun
	pending []store.CmdRecord
	// settleAfter 次查询之后命令自然收敛,模拟 End 之后在途命令陆续终局。
	settleAfter int
	queries     int
}

func (f *fakeGateStore) ActiveProductWorkflowRun() (*store.ProductWorkflowRun, error) {
	return f.run, nil
}

func (f *fakeGateStore) NonTerminalCmds() ([]store.CmdRecord, error) {
	f.queries++
	if f.settleAfter > 0 && f.queries >= f.settleAfter {
		return nil, nil
	}
	return f.pending, nil
}

type fakeEnder struct{ calls int }

func (f *fakeEnder) End(context.Context) error {
	f.calls++
	return nil
}

// installHarness 摆好"包已备好、无任务在跑、无在途命令"这个可以安装的基线,
// 各用例只改自己关心的那一条。
type installHarness struct {
	dir     string
	gate    *InstallGate
	store   *fakeGateStore
	ender   *fakeEnder
	checker *Checker
}

func newInstallHarness(t *testing.T, version string, body []byte) *installHarness {
	t.Helper()
	dir := t.TempDir()
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])

	checker := &Checker{
		CurrentVersion: "0.2.4", DownloadDir: dir,
		status:      Status{CurrentVersion: "0.2.4", Available: true, Version: version, Ready: true},
		readySHA256: sha,
		failures:    map[string]int{},
	}
	if err := os.WriteFile(checker.PackagePath(version), body, 0o600); err != nil {
		t.Fatal(err)
	}
	h := &installHarness{
		dir: dir, store: &fakeGateStore{}, ender: &fakeEnder{}, checker: checker,
	}
	h.gate = &InstallGate{
		Store: h.store, Workflow: h.ender, Checker: checker, StateDir: dir,
		Timeout: 300 * time.Millisecond, Poll: 10 * time.Millisecond,
	}
	return h
}

func TestPrepareReturnsPackageWhenEverythingIsQuiet(t *testing.T) {
	h := newInstallHarness(t, "0.2.5", []byte("installer"))
	path, err := h.gate.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if path != h.checker.PackagePath("0.2.5") {
		t.Fatalf("返回路径不对: %s", path)
	}
	if h.ender.calls != 0 {
		t.Fatal("没有运行中的工作流时不该调用结束")
	}
	// 字条必须落地,否则装崩之后没有任何依据判断该不该停手。
	pending, err := h.gate.readPending()
	if err != nil || pending == nil || pending.Version != "0.2.5" || pending.Attempts != 1 {
		t.Fatalf("安装意图未正确记录: %+v err=%v", pending, err)
	}
}

func TestPrepareRefusesWhenPackageWasTamperedWith(t *testing.T) {
	// 下载与安装之间隔着任意长的时间。这是个马上要静默执行的文件,必须重验。
	h := newInstallHarness(t, "0.2.5", []byte("installer"))
	if err := os.WriteFile(h.checker.PackagePath("0.2.5"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := h.gate.Prepare(context.Background())
	if !errors.Is(err, ErrPackageTampered) {
		t.Fatalf("被改动的包必须拒绝执行，得到 %v", err)
	}
	if h.ender.calls != 0 {
		t.Fatal("包都不可信了，绝不该先去结束用户的任务")
	}
}

func TestPrepareChecksPackageBeforeTouchingBusiness(t *testing.T) {
	// 顺序是有意的:一次注定失败的更新不该白白把用户正在跑的批次结束掉。
	h := newInstallHarness(t, "0.2.5", []byte("installer"))
	h.checker.status.Ready = false // 没有可装的
	h.store.run = &store.ProductWorkflowRun{RunID: "run-1", Status: workflow.StatusRunning}

	if _, err := h.gate.Prepare(context.Background()); !errors.Is(err, ErrNoPackageReady) {
		t.Fatalf("没有已备好的包应如实报告，得到 %v", err)
	}
	if h.ender.calls != 0 {
		t.Fatal("没有可装的东西时不该结束用户的任务")
	}
}

func TestPrepareEndsRunningWorkflowThenWaits(t *testing.T) {
	h := newInstallHarness(t, "0.2.5", []byte("installer"))
	h.store.run = &store.ProductWorkflowRun{RunID: "run-1", Status: workflow.StatusRunning}
	h.store.pending = []store.CmdRecord{{MsgID: "cmd-1"}}
	h.store.settleAfter = 3 // 第三次查询时命令收敛

	if _, err := h.gate.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.ender.calls != 1 {
		t.Fatalf("运行中的工作流应被结束一次，实际 %d 次", h.ender.calls)
	}
	if h.store.queries < 3 {
		t.Fatalf("应等到命令收敛才返回，只查了 %d 次", h.store.queries)
	}
}

func TestPrepareRefusesWhenCommandsNeverSettle(t *testing.T) {
	// 宁可不装,也不把一条正在发送的消息杀成 suspect。
	h := newInstallHarness(t, "0.2.5", []byte("installer"))
	h.store.pending = []store.CmdRecord{{MsgID: "cmd-stuck"}}

	_, err := h.gate.Prepare(context.Background())
	if !errors.Is(err, ErrCommandsInFlight) {
		t.Fatalf("命令不收敛时必须放弃本次安装，得到 %v", err)
	}
	if pending, _ := h.gate.readPending(); pending != nil {
		t.Fatal("没交出安装器就不该留下安装意图")
	}
}

func TestPrepareSkipsEndForParkedWorkflow(t *testing.T) {
	// 暂停、等窗口、终局都不是"正在跑",不必也不该去结束它。
	for _, status := range []workflow.Status{
		workflow.StatusPaused, workflow.StatusWaitingDailyWindow,
		workflow.StatusCompleted, workflow.StatusFailed,
	} {
		t.Run(string(status), func(t *testing.T) {
			h := newInstallHarness(t, "0.2.5", []byte("installer"))
			h.store.run = &store.ProductWorkflowRun{RunID: "run-1", Status: status}
			if _, err := h.gate.Prepare(context.Background()); err != nil {
				t.Fatal(err)
			}
			if h.ender.calls != 0 {
				t.Fatalf("非运行中的工作流不该被结束，实际调用 %d 次", h.ender.calls)
			}
		})
	}
}

func TestPrepareStopsAfterRepeatedInstallFailures(t *testing.T) {
	// 装-崩-装的循环比停在旧版严重得多 —— 旧版至少还能用。
	h := newInstallHarness(t, "0.2.5", []byte("installer"))
	for i := 0; i < maxInstallAttempts; i++ {
		if _, err := h.gate.Prepare(context.Background()); err != nil {
			t.Fatalf("第 %d 次应当放行: %v", i+1, err)
		}
	}
	if _, err := h.gate.Prepare(context.Background()); !errors.Is(err, ErrInstallHalted) {
		t.Fatalf("连续失败到上限后必须停手，得到 %v", err)
	}
}

func TestPrepareToleratesNilGate(t *testing.T) {
	// 一个 nil 的具体指针塞进接口字段会变成"非 nil 的接口值"，调用方的 nil 检查
	// 就形同虚设。这里不能 panic。
	var gate *InstallGate
	if _, err := gate.Prepare(context.Background()); err == nil {
		t.Fatal("未接线时应返回错误而不是 panic")
	}
}

func TestConfirmPendingInstallDetectsSuccess(t *testing.T) {
	h := newInstallHarness(t, "0.2.5", []byte("installer"))
	if _, err := h.gate.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 重启后跑的正是期待的那一版。
	result, err := ConfirmPendingInstall(h.dir, "0.2.5")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Attempted || !result.Succeeded {
		t.Fatalf("应判定安装成功: %+v", result)
	}
	// 字条要清掉,否则下次启动会拿一段陈年往事当依据。
	if _, statErr := os.Stat(filepath.Join(h.dir, pendingInstallFile)); !os.IsNotExist(statErr) {
		t.Fatal("成功后应删除安装意图记录")
	}
}

func TestConfirmPendingInstallDetectsFailureAndHalts(t *testing.T) {
	h := newInstallHarness(t, "0.2.5", []byte("installer"))
	for i := 0; i < maxInstallAttempts; i++ {
		if _, err := h.gate.Prepare(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// 重启后版本没变 —— 装完自启失败、安装器被杀、覆盖到一半，表现都是这个。
	result, err := ConfirmPendingInstall(h.dir, "0.2.4")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Attempted || result.Succeeded || !result.Halted {
		t.Fatalf("应判定失败并停手: %+v", result)
	}
	if _, statErr := os.Stat(filepath.Join(h.dir, pendingInstallFile)); os.IsNotExist(statErr) {
		t.Fatal("失败时要保留记录，否则下次又会从头再试")
	}
}

func TestConfirmPendingInstallQuietWithoutRecord(t *testing.T) {
	result, err := ConfirmPendingInstall(t.TempDir(), "0.2.4")
	if err != nil || result.Attempted {
		t.Fatalf("没有安装记录时应安静返回: %+v err=%v", result, err)
	}
}
