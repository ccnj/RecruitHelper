package handreload

import (
	"context"
	"encoding/json"
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
	h.dispatcher.afterDispatch = func() { h.arriveNewBoot(protocol.ContractHash, true, []string{Capability()}) }
	orchestrator := &Orchestrator{
		Store: h.store, Registry: registry, Dispatcher: h.dispatcher, Feeds: h.feeds,
		Trigger: TriggerAuto, Timeout: time.Second, Poll: time.Millisecond,
	}
	h.auto = NewAutoReloader(orchestrator, h.store, time.Hour)
	return h
}

func (h *harness) arriveNewBoot(contractHash string, match bool, caps []string) {
	h.registry.OnlineWithBuild(
		testHand, "session-new", testNewBoot, caps, nil,
		contractHash, match, "0.2.2", time.Now(),
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
		h.arriveNewBoot("sha256:still-stale", false, []string{Capability()})
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
		h.arriveNewBoot("sha256:still-stale", false, []string{Capability()})
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
