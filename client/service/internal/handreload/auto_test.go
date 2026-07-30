package handreload

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"recruithelper/client/service/internal/session"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/workflow"
	"recruithelper/contract/gen/go/protocol"
)

const (
	testHand    = "hand-auto"
	testOldBoot = "boot-old"
	testNewBoot = "boot-new"
)

type fakeStore struct {
	pending map[string][]store.CmdRecord
	run     *store.ProductWorkflowRun
	runErr  error
}

func (f *fakeStore) NonTerminalCmdsForHand(handID string) ([]store.CmdRecord, error) {
	return f.pending[handID], nil
}

func (f *fakeStore) ActiveProductWorkflowRun() (*store.ProductWorkflowRun, error) {
	return f.run, f.runErr
}

// fakeDispatcher 记下派发次数,并按 afterDispatch 模拟插件换代后的新 hello。
type fakeDispatcher struct {
	calls         int
	afterDispatch func()
	err           error
}

func (f *fakeDispatcher) Dispatch(handID, name string, args json.RawMessage) (string, error) {
	f.calls++
	if f.err != nil {
		return "msg-" + name, f.err
	}
	if f.afterDispatch != nil {
		f.afterDispatch()
	}
	return "msg-" + name, nil
}

type fakeFeeds struct {
	calls    int
	triggers []string
}

func (f *fakeFeeds) InvalidateSourcingFeedsForHand(_ string, trigger string, _ time.Time) error {
	f.calls++
	f.triggers = append(f.triggers, trigger)
	return nil
}

// harness 把一只「契约落后、其余条件全部就绪」的手摆好,各测试只改自己关心的
// 那一个条件,这样每条判据的作用都是被单独证明的。
type harness struct {
	registry   *session.Registry
	store      *fakeStore
	dispatcher *fakeDispatcher
	feeds      *fakeFeeds
	auto       *AutoReloader
	pluginDir  string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	registry := session.NewRegistry(protocol.DefaultHbGraceMs)
	registry.OnlineWithBuild(
		testHand, "session-old", testOldBoot,
		[]string{Capability()}, nil,
		"sha256:stale-plugin", false, "0.2.1", time.Now(),
	)
	h := &harness{
		registry:   registry,
		store:      &fakeStore{pending: map[string][]store.CmdRecord{}},
		dispatcher: &fakeDispatcher{},
		feeds:      &fakeFeeds{},
	}
	// 默认让派发立刻带出一只契约正确的新手,代表"换代成功"。
	h.dispatcher.afterDispatch = func() {
		h.arriveNewBoot(protocol.ContractHash, true, []string{Capability()}, "0.2.2")
	}
	orchestrator := &Orchestrator{
		Store: h.store, Registry: registry, Dispatcher: h.dispatcher, Feeds: h.feeds,
		Trigger: TriggerAuto, Timeout: time.Second, Poll: time.Millisecond,
	}
	h.auto = NewAutoReloader(orchestrator, h.store, time.Hour)
	return h
}

// withPluginDir 造一个只含 manifest.json 的固定插件目录,代表"磁盘上是哪一版"。
func (h *harness) withPluginDir(t *testing.T, manifest string) {
	t.Helper()
	dir := t.TempDir()
	if manifest != "" {
		if err := os.WriteFile(
			filepath.Join(dir, "manifest.json"), []byte(manifest), 0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	h.pluginDir = dir
	h.auto.orchestrator.PluginDir = dir
}

// contractIsFine 让手的契约与脑一致,这样触发与否只取决于版本比对。
func (h *harness) contractIsFine(extVersion string) {
	h.registry.OnlineWithBuild(
		testHand, "session-old", testOldBoot,
		[]string{Capability()}, nil,
		protocol.ContractHash, true, extVersion, time.Now(),
	)
}

// arriveNewBoot 模拟重载之后新一代插件的 hello。extVersion 必须由调用方指定 ——
// 换代后手报的是哪一版,正是"这次换代到底成没成"的判据。
func (h *harness) arriveNewBoot(
	contractHash string, match bool, caps []string, extVersion string,
) {
	h.registry.OnlineWithBuild(
		testHand, "session-new", testNewBoot, caps, nil,
		contractHash, match, extVersion, time.Now(),
	)
}

func (h *harness) evaluate() Outcome {
	return h.auto.EvaluateOnce(context.Background())
}

func TestAutoReloadTriggersWhenPluginContractDrifts(t *testing.T) {
	h := newHarness(t)
	outcome := h.evaluate()
	if !outcome.Triggered || outcome.Err != nil {
		t.Fatalf("契约落后的手应被自动重载: %+v", outcome)
	}
	if h.dispatcher.calls != 1 {
		t.Fatalf("应恰好派发一次 debug.reload，实际 %d 次", h.dispatcher.calls)
	}
	if outcome.Result.PreviousBootID != testOldBoot || outcome.Result.BootID != testNewBoot {
		t.Fatalf("换代证词不完整: %+v", outcome.Result)
	}
	if h.feeds.calls != 1 {
		t.Fatalf("重载前应终止旧推荐流一次，实际 %d 次", h.feeds.calls)
	}
	// 这个标记会落进批次记录。自动路径若冒用人工路径的标记，事后排查会把两者混为一谈。
	if h.feeds.triggers[0] != TriggerAuto {
		t.Fatalf("自动路径应报自己的终止原因，实际 %q", h.feeds.triggers[0])
	}
}

func TestAutoReloadSkipsWhileProductWorkflowActive(t *testing.T) {
	// 重载会作废旧推荐流。用户正在跑的批次不能被自动更新顺手丢掉。
	for _, status := range []workflow.Status{
		workflow.StatusRunning, workflow.StatusAwaitingConfirmation,
	} {
		t.Run(string(status), func(t *testing.T) {
			h := newHarness(t)
			h.store.run = &store.ProductWorkflowRun{RunID: "run-1", Status: status}
			outcome := h.evaluate()
			if outcome.Triggered || h.dispatcher.calls != 0 {
				t.Fatalf("活跃工作流期间不得自动重载: %+v calls=%d", outcome, h.dispatcher.calls)
			}
			if h.feeds.calls != 0 {
				t.Fatalf("被判据挡下时不得触碰推荐流，实际 %d 次", h.feeds.calls)
			}
		})
	}
}

func TestAutoReloadProceedsWhenWorkflowParkedOrFinished(t *testing.T) {
	// 暂停、等窗口与终局都不是"正在跑",此时换代正是该做的事。
	for _, status := range []workflow.Status{
		workflow.StatusPaused, workflow.StatusWaitingDailyWindow,
		workflow.StatusCompleted, workflow.StatusFailed,
	} {
		t.Run(string(status), func(t *testing.T) {
			h := newHarness(t)
			h.store.run = &store.ProductWorkflowRun{RunID: "run-1", Status: status}
			if outcome := h.evaluate(); !outcome.Triggered || outcome.Err != nil {
				t.Fatalf("非活跃工作流不应挡住换代: %+v", outcome)
			}
		})
	}
}

func TestAutoReloadSkipsWhenCommandsStillInFlight(t *testing.T) {
	h := newHarness(t)
	h.store.pending[testHand] = []store.CmdRecord{{MsgID: "cmd-in-flight"}}
	outcome := h.evaluate()
	if outcome.Err == nil || outcome.Err.Kind != KindCommandsInFlight {
		t.Fatalf("未收束命令必须挡下重载: %+v", outcome)
	}
	if h.dispatcher.calls != 0 || h.feeds.calls != 0 {
		t.Fatalf("判据挡下时不得派发或动推荐流: dispatch=%d feeds=%d",
			h.dispatcher.calls, h.feeds.calls)
	}
}

func TestAutoReloadRetriesAfterPreDispatchRejection(t *testing.T) {
	// 派发前被挡是暂时条件。条件消失后必须还能轮到这只手,否则一次未收束命令就
	// 让它永远卡在"已尝试过这个 bootID"上。
	h := newHarness(t)
	h.store.pending[testHand] = []store.CmdRecord{{MsgID: "cmd-in-flight"}}
	if outcome := h.evaluate(); outcome.Err == nil {
		t.Fatalf("首轮应被未收束命令挡下: %+v", outcome)
	}
	delete(h.store.pending, testHand)
	if outcome := h.evaluate(); !outcome.Triggered || outcome.Err != nil {
		t.Fatalf("命令收束后应重新尝试: %+v", outcome)
	}
	if h.dispatcher.calls != 1 {
		t.Fatalf("应只在条件满足后派发一次，实际 %d 次", h.dispatcher.calls)
	}
}

func TestAutoReloadSkipsHandWithoutReloadCapability(t *testing.T) {
	// 老插件没有 debug.reload@1,只能人工重载最后一次。
	h := newHarness(t)
	h.registry.OnlineWithBuild(
		testHand, "session-old", testOldBoot, nil, nil,
		"sha256:stale-plugin", false, "0.1.0", time.Now(),
	)
	if outcome := h.evaluate(); outcome.Triggered || h.dispatcher.calls != 0 {
		t.Fatalf("无重载能力的手不得被自动派发: %+v", outcome)
	}
}

func TestAutoReloadSkipsOfflineHand(t *testing.T) {
	h := newHarness(t)
	h.registry.Offline(testHand, "session-old", testOldBoot)
	if outcome := h.evaluate(); outcome.Triggered || h.dispatcher.calls != 0 {
		t.Fatalf("离线的手不得被自动派发: %+v", outcome)
	}
}

func TestAutoReloadSkipsHandAlreadyMatchingContract(t *testing.T) {
	h := newHarness(t)
	h.registry.OnlineWithBuild(
		testHand, "session-old", testOldBoot,
		[]string{Capability()}, nil,
		protocol.ContractHash, true, "0.2.2", time.Now(),
	)
	if outcome := h.evaluate(); outcome.Triggered || h.dispatcher.calls != 0 {
		t.Fatalf("契约已经一致的手不该被打扰: %+v", outcome)
	}
}

func TestAutoReloadDispatchesAtMostOncePerBootID(t *testing.T) {
	// 闸一:同一 (handID, bootID) 只自动派发一次。这里让换代"没生效"(手仍报旧
	// bootID),证明第二轮不会再来一次。
	h := newHarness(t)
	h.dispatcher.afterDispatch = nil // 换代不发生,Reload 会超时
	h.auto.orchestrator.Timeout = 20 * time.Millisecond

	first := h.evaluate()
	if first.Err == nil || first.Err.Kind != KindTimeout {
		t.Fatalf("换代未发生时应超时: %+v", first)
	}
	if h.dispatcher.calls != 1 {
		t.Fatalf("首轮应派发一次，实际 %d 次", h.dispatcher.calls)
	}
	// 超时属于"已派发",两道闸此刻都拦得住。先清掉停手记录,让这里单独证明
	// (handID, bootID) 闸本身也能挡住重复派发。
	h.auto.mu.Lock()
	delete(h.auto.halted, testHand)
	h.auto.mu.Unlock()

	for i := 0; i < 3; i++ {
		if outcome := h.evaluate(); outcome.Triggered {
			t.Fatalf("同一 bootID 不得重复自动派发，第 %d 轮又动手了: %+v", i+2, outcome)
		}
	}
	if h.dispatcher.calls != 1 {
		t.Fatalf("累计仍应只有一次派发，实际 %d 次", h.dispatcher.calls)
	}
}

func TestAutoReloadHaltsAfterContractStillMismatchedPostReload(t *testing.T) {
	// 闸二(防循环的核心):命令已经派发、插件也确实换代了,但契约仍然对不上 ——
	// 说明磁盘上的插件根本不是期望的那一版(例如 Chrome 占着目录导致 pluginSeed
	// 没换成)。此时必须就此停手交人工,否则每来一个新 bootID 就会再重载一次。
	h := newHarness(t)
	h.dispatcher.afterDispatch = func() {
		h.arriveNewBoot("sha256:still-stale", false, []string{Capability()}, "0.2.2")
	}
	first := h.evaluate()
	if first.Err == nil || first.Err.Kind != KindContractMismatch {
		t.Fatalf("换代后契约仍不一致应如实报告: %+v", first)
	}
	if reason, halted := h.auto.Halted(testHand); !halted || reason == "" {
		t.Fatalf("应记下停手原因，实际 halted=%t reason=%q", halted, reason)
	}
	for i := 0; i < 3; i++ {
		if outcome := h.evaluate(); outcome.Triggered {
			t.Fatalf("停手后不得再自动重载，第 %d 轮又动手了: %+v", i+2, outcome)
		}
	}
	if h.dispatcher.calls != 1 {
		t.Fatalf("累计仍应只有一次派发，实际 %d 次", h.dispatcher.calls)
	}
}

func TestAutoReloadClearsHaltAfterContractRecovers(t *testing.T) {
	// 人工修好之后,这只手要能重新参与下一次换代 —— 否则一次停手等于永久退出,
	// 而重启脑才能恢复。
	h := newHarness(t)
	h.dispatcher.afterDispatch = func() {
		h.arriveNewBoot("sha256:still-stale", false, []string{Capability()}, "0.2.2")
	}
	if outcome := h.evaluate(); outcome.Err == nil {
		t.Fatalf("首轮应以契约不一致收场: %+v", outcome)
	}
	if _, halted := h.auto.Halted(testHand); !halted {
		t.Fatal("首轮后应处于停手态")
	}

	// 人工重载成功:契约对上了。
	h.registry.OnlineWithBuild(
		testHand, "session-fixed", "boot-fixed",
		[]string{Capability()}, nil,
		protocol.ContractHash, true, "0.2.2", time.Now(),
	)
	h.evaluate()
	if _, halted := h.auto.Halted(testHand); halted {
		t.Fatal("契约恢复后应清掉停手记录")
	}
}

func TestAutoReloadHaltsWhenDispatchRejected(t *testing.T) {
	// 派发被拒时命令可能已经出去了,按 §14.1 同样不进自动重试。
	h := newHarness(t)
	h.dispatcher.err = errWireClosed
	first := h.evaluate()
	if first.Err == nil || first.Err.Kind != KindDispatchRejected {
		t.Fatalf("派发失败应如实报告: %+v", first)
	}
	if _, halted := h.auto.Halted(testHand); !halted {
		t.Fatal("派发失败后应停手交人工")
	}
	if outcome := h.evaluate(); outcome.Triggered {
		t.Fatalf("停手后不得再动手: %+v", outcome)
	}
}

var errWireClosed = &Error{Kind: KindDispatchRejected, Message: "连接已关闭"}

// —— 版本比对:绝大多数更新不动契约,这条路径才是常态 ——

func TestAutoReloadTriggersWhenDiskVersionIsNewerThanRunningPlugin(t *testing.T) {
	// 契约没变,业务本来照常在跑 —— 跑的却是旧插件代码。这正是最隐蔽的那种。
	h := newHarness(t)
	h.withPluginDir(t, `{"version":"0.2.3","manifest_version":3}`)
	h.contractIsFine("0.2.2")
	h.dispatcher.afterDispatch = func() {
		h.arriveNewBoot(protocol.ContractHash, true, []string{Capability()}, "0.2.3")
	}

	outcome := h.evaluate()
	if !outcome.Triggered || outcome.Err != nil {
		t.Fatalf("磁盘版本更新时应自动重载: %+v", outcome)
	}
	if h.dispatcher.calls != 1 {
		t.Fatalf("应恰好派发一次，实际 %d 次", h.dispatcher.calls)
	}
	// 契约明明一致,原因就必须说是版本。2026-07-30 真机首验时日志把这两者报串,
	// 排查会一开始就朝契约方向走 —— 所以这条断言比"是否触发"更值得盯。
	if outcome.Reason != "版本落后于磁盘上的插件" {
		t.Fatalf("触发原因应指向版本，实际 %q", outcome.Reason)
	}
}

func TestAutoReloadSkipsWhenRunningPluginMatchesDisk(t *testing.T) {
	h := newHarness(t)
	h.withPluginDir(t, `{"version":"0.2.2","manifest_version":3}`)
	h.contractIsFine("0.2.2")
	if outcome := h.evaluate(); outcome.Triggered || h.dispatcher.calls != 0 {
		t.Fatalf("版本一致时不该重载: %+v", outcome)
	}
}

func TestAutoReloadStaysQuietWhenDiskVersionUnknown(t *testing.T) {
	// 读不到磁盘版本是"缺证据",不是"手过时了"的反证。误判的代价是无谓重载并
	// 顺带作废推荐流,所以一律降级为只看契约。
	for _, tc := range []struct {
		name     string
		manifest string
		noDir    bool
	}{
		{name: "目录未配置", noDir: true},
		{name: "目录里没有manifest", manifest: ""},
		{name: "manifest不是JSON", manifest: `{{{`},
		{name: "version缺失", manifest: `{"manifest_version":3}`},
		{name: "version不是纯数字段", manifest: `{"version":"0.2.3-beta"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			if !tc.noDir {
				h.withPluginDir(t, tc.manifest)
			}
			h.contractIsFine("0.0.1") // 一个明显旧的版本号
			if outcome := h.evaluate(); outcome.Triggered || h.dispatcher.calls != 0 {
				t.Fatalf("磁盘版本不可知时不得据此重载: %+v", outcome)
			}
		})
	}
}

func TestAutoReloadStaysQuietWhenHandReportsNoVersion(t *testing.T) {
	// 手报不出版本同样是缺证据,与磁盘读不到对称。
	h := newHarness(t)
	h.withPluginDir(t, `{"version":"0.2.3","manifest_version":3}`)
	h.contractIsFine("")
	if outcome := h.evaluate(); outcome.Triggered || h.dispatcher.calls != 0 {
		t.Fatalf("手未上报版本时不得据此重载: %+v", outcome)
	}
}

func TestAutoReloadSkipsWhenPluginSeedFailedAndDiskStillOld(t *testing.T) {
	// pluginSeed 换代失败时磁盘留在旧版,与手上跑的是同一版 —— 重载出来还是它,
	// 白白作废一次推荐流。这一支必须安静。
	h := newHarness(t)
	h.withPluginDir(t, `{"version":"0.2.2","manifest_version":3}`)
	h.contractIsFine("0.2.2")
	if outcome := h.evaluate(); outcome.Triggered || h.feeds.calls != 0 {
		t.Fatalf("换代失败后磁盘仍是旧版，不该重载: %+v feeds=%d", outcome, h.feeds.calls)
	}
}

func TestAutoReloadStillTriggersOnContractDriftRegardlessOfVersion(t *testing.T) {
	// 契约是硬信号:即便版本号看起来一致(比如忘了升号),契约对不上也必须重载。
	h := newHarness(t)
	h.withPluginDir(t, `{"version":"0.2.2","manifest_version":3}`)
	h.registry.OnlineWithBuild(
		testHand, "session-old", testOldBoot,
		[]string{Capability()}, nil,
		"sha256:stale-plugin", false, "0.2.2", time.Now(),
	)
	outcome := h.evaluate()
	if !outcome.Triggered || outcome.Err != nil {
		t.Fatalf("契约漂移应始终触发重载: %+v", outcome)
	}
	if outcome.Reason != "契约与当前脑不一致" {
		t.Fatalf("触发原因应指向契约，实际 %q", outcome.Reason)
	}
}

// —— 手就绪即评估:换代不必干等一个 tick ——

func TestAutoReloadEvaluatesOnHandReadyWithoutWaitingForTick(t *testing.T) {
	h := newHarness(t)
	h.withPluginDir(t, `{"version":"0.2.3","manifest_version":3}`)
	h.contractIsFine("0.2.2")

	dispatched := make(chan struct{}, 1)
	h.dispatcher.afterDispatch = func() {
		h.arriveNewBoot(protocol.ContractHash, true, []string{Capability()}, "0.2.3")
		dispatched <- struct{}{}
	}

	// interval 一小时:这个用例里唯一可能推动评估的就是 ready 提醒本身。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.auto.Run(ctx)

	h.auto.NotifyHandReady(testHand)
	select {
	case <-dispatched:
	case <-time.After(2 * time.Second):
		t.Fatal("手就绪后应立即评估并派发重载，而不是等下一个 tick")
	}
}

func TestNotifyHandReadyNeverBlocks(t *testing.T) {
	// 它跑在手的读循环上。一旦阻塞,读循环就停了 —— 而重载编排等的下一次 hello
	// 正需要这个读循环去处理,当场自锁。这里没有消费者,连续提醒必须照样立刻返回。
	h := newHarness(t)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			h.auto.NotifyHandReady(testHand)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("NotifyHandReady 阻塞了")
	}
}

func TestNotifyHandReadyToleratesUnwiredReloader(t *testing.T) {
	var nilReloader *AutoReloader
	nilReloader.NotifyHandReady(testHand) // 未接线时不得 panic
	(&AutoReloader{}).NotifyHandReady(testHand)
}

func TestAutoReloadHaltsWhenVersionStillStaleAfterReload(t *testing.T) {
	// 成功判据必须与触发判据对称。换代之后手仍报旧版本,说明 Chrome 读到的并不是
	// 磁盘上那一版 —— 这不能算成功,否则下一轮又判该重载,白白多一次重载和一次
	// 推荐流终止,直到防循环闸才兜住。
	h := newHarness(t)
	h.withPluginDir(t, `{"version":"0.2.3","manifest_version":3}`)
	h.contractIsFine("0.2.2")
	h.dispatcher.afterDispatch = func() {
		h.arriveNewBoot(protocol.ContractHash, true, []string{Capability()}, "0.2.2")
	}

	first := h.evaluate()
	if first.Err == nil || first.Err.Kind != KindVersionMismatch {
		t.Fatalf("换代后版本仍落后应如实报告失败: %+v", first)
	}
	if _, halted := h.auto.Halted(testHand); !halted {
		t.Fatal("这属于已派发之后的失败，应就此停手交人工")
	}
	for i := 0; i < 3; i++ {
		if outcome := h.evaluate(); outcome.Triggered {
			t.Fatalf("停手后不得再自动重载，第 %d 轮又动手了: %+v", i+2, outcome)
		}
	}
	if h.dispatcher.calls != 1 || h.feeds.calls != 1 {
		t.Fatalf("累计仍应只有一次派发与一次推荐流终止: dispatch=%d feeds=%d",
			h.dispatcher.calls, h.feeds.calls)
	}
}

func TestAutoReloadSecondEvaluationRightAfterSuccessIsNoop(t *testing.T) {
	// tick 与 ready 提醒撞在一起时,Run 会连着评估两次。第二次必须空转 ——
	// 否则一次换代会连带作废两次推荐流。
	h := newHarness(t)
	h.withPluginDir(t, `{"version":"0.2.3","manifest_version":3}`)
	h.contractIsFine("0.2.2")
	h.dispatcher.afterDispatch = func() {
		h.arriveNewBoot(protocol.ContractHash, true, []string{Capability()}, "0.2.3")
	}

	if outcome := h.evaluate(); !outcome.Triggered || outcome.Err != nil {
		t.Fatalf("首次评估应完成换代: %+v", outcome)
	}
	second := h.evaluate()
	if second.Triggered {
		t.Fatalf("紧接着的第二次评估必须空转: %+v", second)
	}
	if h.dispatcher.calls != 1 || h.feeds.calls != 1 {
		t.Fatalf("两次评估合计仍应只有一次派发与一次推荐流终止: dispatch=%d feeds=%d",
			h.dispatcher.calls, h.feeds.calls)
	}
}

func TestExpectedPluginVersionReadsDiskManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "manifest.json"),
		[]byte(`{"name":"x","version":"1.2.3.4","manifest_version":3}`), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	if got := ExpectedPluginVersion(dir); got != "1.2.3.4" {
		t.Fatalf("四段版本号应被接受，得到 %q", got)
	}
	if got := ExpectedPluginVersion(""); got != "" {
		t.Fatalf("空目录应返回未知，得到 %q", got)
	}
}
