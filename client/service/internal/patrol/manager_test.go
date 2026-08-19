package patrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/client/service/internal/testfixture"
	"recruithelper/client/service/internal/workflow"
	"recruithelper/contract/gen/go/protocol"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// Now 每次读取前进 1 毫秒。冻结时钟会让同一处理轮里的"规划"与"确认"
// 落在完全相同的瞬间,planned_at 排序退化到 action_id 字典序,偏离生产
// 单调时钟下的真实顺序;单调假时钟保持确定性,且与真实挂钟无关。
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Millisecond)
	return c.now
}

func (c *fakeClock) Add(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}

type fakeHands struct {
	mu    sync.Mutex
	state HandState
	err   error
}

func (h *fakeHands) State(context.Context, string) (HandState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state, h.err
}

func (h *fakeHands) set(state HandState) {
	h.mu.Lock()
	h.state = state
	h.mu.Unlock()
}

type fakeRunner struct {
	mu        sync.Mutex
	calls     []RunRequest
	handler   func(RunRequest) (any, error)
	startHook func(RunRequest)
	// unreadBadge 是模拟页面角标的世界模型:served 为真时 chat.readUnreadTotal
	// 一律由 value 作答、绕过 handler,让既有用例继续以 setUnreadHintForTest
	// 为唯一旋钮。value=nil 模拟角标节点缺席(读不到)。要为该原语定制失败,
	// 把 served 置假后在自己的 handler 里接管。
	unreadBadgeServed bool
	unreadBadgeValue  *int
}

func (r *fakeRunner) setUnreadBadge(value *int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unreadBadgeServed = true
	if value == nil {
		r.unreadBadgeValue = nil
		return
	}
	v := *value
	r.unreadBadgeValue = &v
}

type fakeRunHandle struct {
	handler func(RunRequest) (any, error)
	request RunRequest
}

func (h *fakeRunHandle) LogicalDispatchID() string { return "fake-" + h.request.Name }

func (r *fakeRunner) Start(_ context.Context, request RunRequest) (RunHandle, error) {
	r.mu.Lock()
	r.calls = append(r.calls, request)
	handler := r.handler
	hook := r.startHook
	if request.Name == protocol.PrimChatReadUnreadTotal && r.unreadBadgeServed {
		var value *int
		if r.unreadBadgeValue != nil {
			v := *r.unreadBadgeValue
			value = &v
		}
		handler = func(RunRequest) (any, error) {
			return protocol.ChatReadUnreadTotalData{Total: value, ObservedAt: 1}, nil
		}
	}
	r.mu.Unlock()
	if hook != nil {
		hook(request)
	}
	return &fakeRunHandle{handler: handler, request: request}, nil
}

func (h *fakeRunHandle) Wait(_ context.Context) (json.RawMessage, error) {
	value, err := h.handler(h.request)
	if err != nil {
		return nil, err
	}
	if raw, ok := value.(json.RawMessage); ok {
		return raw, nil
	}
	return protocol.Encode(value)
}

func (r *fakeRunner) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	for i := range r.calls {
		out[i] = r.calls[i].Name
	}
	return out
}

// businessNames 滤掉两类例行边界读:未读角标现场读(轮首与每个会话边界例行
// 出现)与临走看一眼的当前会话识别(每次切换到脏会话前例行出现),数量都随
// 边界数波动。主题无关的命令序列断言用它,免得每个用例都背着这层噪音;未读
// 判定与临走检查本身的用例仍用 names() 或自记序列显式断言时机。
func (r *fakeRunner) businessNames() []string {
	out := make([]string, 0)
	for _, name := range r.names() {
		if name == protocol.PrimChatReadUnreadTotal ||
			name == protocol.PrimChatIdentifyCurrentConversation {
			continue
		}
		out = append(out, name)
	}
	return out
}

func (r *fakeRunner) count(name string) int {
	n := 0
	for _, got := range r.names() {
		if got == name {
			n++
		}
	}
	return n
}

type harness struct {
	db      *store.Store
	dataDir string
	clock   *fakeClock
	hands   *fakeHands
	runner  *fakeRunner
	manager *Manager
	key     store.AccountKey
	config  Config
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dataDir := t.TempDir()
	db, err := store.Open(dataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := &fakeClock{now: time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)}
	// Store 内部自取的业务时间戳(如 result 应用的 effectAt→SentAt→
	// LastOutboundAt)也必须走假时钟,否则统一业务窗口断言随真实时刻漂移。
	db.SetNowFunc(clock.Now)
	hands := &fakeHands{state: HandState{Online: true, Session: "session-1", BootID: "boot-1"}}
	runner := &fakeRunner{}
	runner.handler = defaultHandler
	// 默认世界:角标读不到(与旧默认"传感缺席"同向,不插队)。用例经
	// setUnreadHintForTest 声明页面角标后才会进未读子轮。
	runner.unreadBadgeServed = true
	key := store.AccountKey{Platform: "zhilian", AccountRef: "account-1"}
	if err := db.CreateAccount(&store.Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := db.BindAccountPrincipal(key, "hand-1", "principal-1", "session-1", "boot-1", clock.Now()); err != nil {
		t.Fatalf("BindAccountPrincipal: %v", err)
	}
	sequence := 0
	config := Config{
		Clock: clock, Location: time.UTC, PatrolInterval: 5 * time.Minute,
		IdentityFreshFor: time.Hour, CoalesceWindow: 25 * time.Second,
		MinimumRoundGap: time.Minute, MaxPages: 16,
		// 显式钉住到期对账间隔：用例验证的是"到期即强制核对"这个机制，
		// 不是生产默认值的大小。跟着默认值走会让"推进一个间隔"跨过本地
		// 日界，撞上 ensureWithinDailyWindow 而失败。
		TrackedReconcileInterval: 30 * time.Minute,
		// 既有用例的现场都在交接之后，闸设在测试时钟（07-17）之前，
		// 让它们照常建档；闸自身的边界由专门用例覆盖。
		InboundHandoverCutoff: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		NewRoundID: func() string {
			sequence++
			return fmt.Sprintf("round-%03d", sequence)
		},
		// 两档拟人节奏都注入无等待实现：用例验证的是脑侧的裁决与顺序，
		// 不是等待时长本身。漏掉任何一档都会让整包退化成按真实秒数空转。
		InteractionPaceWait: func(ctx context.Context) error {
			return ctx.Err()
		},
		SourcingPaceWait: func(ctx context.Context) error {
			return ctx.Err()
		},
	}
	manager, err := NewManager(db, runner, hands, config)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := manager.EnableToday(key); err != nil {
		t.Fatalf("EnableToday: %v", err)
	}
	return &harness{
		db: db, dataDir: dataDir, clock: clock, hands: hands, runner: runner,
		manager: manager, key: key, config: config,
	}
}

func TestProductWorkflowConversationAndPatrolBoundaries(t *testing.T) {
	h := newHarness(t)

	h.manager.SetWorkflowConversationGate(func() (bool, error) {
		return false, nil
	})
	allowed, err := h.manager.mayStartNextConversation()
	if err != nil || allowed {
		t.Fatalf("conversation gate = %v, %v; want false, nil", allowed, err)
	}

	h.manager.tickMu.Lock()
	actionStarted := make(chan struct{})
	actionDone := make(chan error, 1)
	go func() {
		actionDone <- h.manager.RunAtPatrolBoundary(func() error {
			close(actionStarted)
			return nil
		})
	}()
	select {
	case <-actionStarted:
		t.Fatal("boundary action ran before the current patrol boundary")
	case <-time.After(20 * time.Millisecond):
	}
	h.manager.tickMu.Unlock()
	select {
	case err := <-actionDone:
		if err != nil {
			t.Fatalf("RunAtPatrolBoundary: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("boundary action did not run after patrol released")
	}
}

func TestConversationGateDoesNotInvertWorkflowAndPatrolLocks(t *testing.T) {
	h := newHarness(t)
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("读取账号: account=%+v err=%v", account, err)
	}
	actor := &roundActor{
		manager: h.manager,
		account: account,
		hand: HandState{
			Online: true, Session: "session-1", BootID: "boot-1",
		},
		now: h.clock.Now(),
	}

	var workflowMu sync.Mutex
	workflowHeld := make(chan struct{})
	beginPause := make(chan struct{})
	pauseDone := make(chan error, 1)
	go func() {
		workflowMu.Lock()
		close(workflowHeld)
		<-beginPause
		pauseDone <- h.manager.PauseNow(h.key)
		workflowMu.Unlock()
	}()
	<-workflowHeld

	gateEntered := make(chan struct{})
	h.manager.SetWorkflowConversationGate(func() (bool, error) {
		close(gateEntered)
		workflowMu.Lock()
		workflowMu.Unlock()
		return true, nil
	})
	patrolHeld := make(chan struct{})
	gateDone := make(chan error, 1)
	go func() {
		h.manager.mu.Lock()
		close(patrolHeld)
		_, gateErr := actor.mayStartNextConversation(context.Background())
		h.manager.mu.Unlock()
		gateDone <- gateErr
	}()
	<-patrolHeld
	<-gateEntered
	close(beginPause)

	select {
	case pauseErr := <-pauseDone:
		if pauseErr != nil {
			t.Fatalf("PauseNow: %v", pauseErr)
		}
	case <-time.After(time.Second):
		t.Fatal("workflow.mu → patrol.mu 与 patrol.mu → workflow.mu 发生锁序死锁")
	}
	select {
	case gateErr := <-gateDone:
		if !errors.Is(gateErr, ErrActorPaused) {
			t.Fatalf("gate 返回后没有复核暂停状态: %v", gateErr)
		}
	case <-time.After(time.Second):
		t.Fatal("会话 gate 在暂停完成后未返回")
	}
}

func TestSourcingUserPauseInFlightPreservesPreparingBatch(t *testing.T) {
	h := newHarness(t)
	documents := []m5ai.JobConfigDocument{
		{DocType: "多轮沟通", Content: "reply"},
		{DocType: "意向判断", Content: "intent"},
		{DocType: "客户事实库", Content: "facts"},
		{DocType: "候选人筛选", Content: `{"minScore":5}`},
		{DocType: "打分", Content: "请评分 {resume_json}"},
		{DocType: "招呼语", Content: `{"prompt":"状态={career_state};简历={resume_summary_json}"}`},
		{DocType: "职位筛选", Content: testfixture.SourcingFiltersDocument},
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].DocType < documents[j].DocType })
	revision := m5ai.ContextRevision{
		ContextID: "context-pause-sourcing", RevisionHash: "revision-pause-sourcing",
		SourceKind: "localImport", SourceJobRef: "17", DisplayName: "synthetic-position",
		SourcePackage: m5ai.JobConfigDocumentPackage{Documents: documents},
		Communication: m5ai.CommunicationView{
			ReplyPrompt: "reply", IntentPrompt: "intent", CustomerFacts: "facts", MappingVersion: m5ai.MappingVersion,
		},
		CreatedAt: h.clock.Now(),
	}
	if _, _, err := h.db.SaveJobAIContextRevision(revision); err != nil {
		t.Fatal(err)
	}
	started, err := h.db.StartSourcingBatch(store.StartSourcingBatchRequest{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ContextRevisionHash: revision.RevisionHash, TargetCount: 2, StartedAt: h.clock.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	waiting := make(chan struct{})
	release := make(chan struct{})
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name != protocol.PrimCandidateReadSourcingWindow {
			return defaultHandler(request)
		}
		close(waiting)
		<-release
		return protocol.CandidateReadSourcingWindowData{
			PositionRef: "position-pause", PlatformUserRefs: []string{"candidate-pause"},
			Moved: false, ObservedAt: h.clock.Now().UnixMilli(),
		}, nil
	}
	tickDone := make(chan TickResult, 1)
	go func() {
		result, _ := h.manager.Tick(context.Background())
		tickDone <- result
	}()
	<-waiting
	if err := h.manager.PauseNow(h.key); err != nil {
		t.Fatal(err)
	}
	close(release)
	result := <-tickDone
	if len(result.Rounds) != 1 || !errors.Is(result.Rounds[0].Err, ErrActorPaused) {
		t.Fatalf("在途暂停未以 actor paused 收束: %+v", result)
	}
	batch, err := h.db.SourcingBatchByID(started.Batch.BatchID)
	if err != nil || batch == nil || batch.Status != store.SourcingBatchPreparing || batch.Reason != "" || batch.EndedAt != nil {
		t.Fatalf("普通暂停改写了正式批次: batch=%+v err=%v", batch, err)
	}
}

// 采集开启闸(2026-08-12 甲方裁决):职位不在「在线中」分区就不开批次,
// 且一条平台命令都不该再往下派;批次直接写成终局、原因可读,重新点
// 开始会新开一批重新过闸。
func TestSourcingBlockedWhenJobNotOnline(t *testing.T) {
	h := newHarness(t)
	revisionHash := seedStartableSourcingRevision(t, h, "job-not-online")
	started, err := h.db.StartSourcingBatch(store.StartSourcingBatchRequest{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ContextRevisionHash: revisionHash, TargetCount: 1, StartedAt: h.clock.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimJobReadPublishedList {
			return protocol.JobReadPublishedListData{
				Sections: []protocol.JobPostingSection{
					{Label: "在线中", Names: []string{"另一个职位"}},
					{Label: "未上线", Names: []string{"synthetic-position"}},
				},
				ObservedAt: time.Now().UnixMilli(),
			}, nil
		}
		return defaultHandler(request)
	}

	result, tickErr := h.manager.Tick(context.Background())
	if tickErr != nil {
		t.Fatalf("Tick: %v", tickErr)
	}
	if len(result.Rounds) != 1 || result.Rounds[0].Err == nil {
		t.Fatalf("职位未在线应以错误收束: %+v", result.Rounds)
	}
	if h.runner.count(protocol.PrimCandidateSelectSourcingPosition) != 0 {
		t.Fatalf("职位未在线不得继续选择职位: %v", h.runner.names())
	}
	batch, err := h.db.SourcingBatchByID(started.Batch.BatchID)
	if err != nil || batch == nil || batch.Status != store.SourcingBatchStopped ||
		batch.Reason != "jobNotOnline" || batch.EndedAt == nil {
		t.Fatalf("职位未在线应把批次写成终局并记录原因: batch=%+v err=%v", batch, err)
	}
	// 终局而不是 blocked:闸拦下的批次零成员,留成未终局只会把「只回复消息」
	// 一起挡住(2026-08-13 真机暴露)。
	if active, err := h.db.ActiveSourcingBatch(h.key); err != nil || active != nil {
		t.Fatalf("闸拦下后不应残留未终局批次: active=%+v err=%v", active, err)
	}
}

// 状态读取失败同样不开工:不确认就不开,方向是少做。
func TestSourcingBlockedWhenJobStatusReadFails(t *testing.T) {
	h := newHarness(t)
	revisionHash := seedStartableSourcingRevision(t, h, "job-status-read-failed")
	started, err := h.db.StartSourcingBatch(store.StartSourcingBatchRequest{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ContextRevisionHash: revisionHash, TargetCount: 1, StartedAt: h.clock.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimJobReadPublishedList {
			return nil, wrapRunError(
				protocol.ErrCodeElementUnresolved, "", errors.New("职位分区读取失败"),
			)
		}
		return defaultHandler(request)
	}

	result, tickErr := h.manager.Tick(context.Background())
	if tickErr != nil {
		t.Fatalf("Tick: %v", tickErr)
	}
	if len(result.Rounds) != 1 || result.Rounds[0].Err == nil {
		t.Fatalf("状态读取失败应以错误收束: %+v", result.Rounds)
	}
	if h.runner.count(protocol.PrimCandidateSelectSourcingPosition) != 0 {
		t.Fatalf("状态读取失败不得继续选择职位: %v", h.runner.names())
	}
	batch, err := h.db.SourcingBatchByID(started.Batch.BatchID)
	if err != nil || batch == nil || batch.Status != store.SourcingBatchStopped ||
		batch.Reason != "jobStatusReadFailed" || batch.EndedAt == nil {
		t.Fatalf("状态读取失败应把批次写成终局并记录原因: batch=%+v err=%v", batch, err)
	}
}

func TestSourcingPositionSelectUsesExistingSurfaceRecoveryWithoutAnotherUserStart(t *testing.T) {
	h := newHarness(t)
	documents := []m5ai.JobConfigDocument{
		{DocType: "多轮沟通", Content: "reply"},
		{DocType: "意向判断", Content: "intent"},
		{DocType: "客户事实库", Content: "facts"},
		{DocType: "候选人筛选", Content: `{"minScore":5}`},
		{DocType: "打分", Content: "请评分 {resume_json}"},
		{DocType: "招呼语", Content: `{"prompt":"状态={career_state};简历={resume_summary_json}"}`},
		{DocType: "职位筛选", Content: testfixture.SourcingFiltersDocument},
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].DocType < documents[j].DocType })
	revision := m5ai.ContextRevision{
		ContextID: "context-sourcing-surface-recovery", RevisionHash: "revision-sourcing-surface-recovery",
		SourceKind: "localImport", SourceJobRef: "17", DisplayName: "synthetic-position",
		SourcePackage: m5ai.JobConfigDocumentPackage{Documents: documents},
		Communication: m5ai.CommunicationView{
			ReplyPrompt: "reply", IntentPrompt: "intent", CustomerFacts: "facts",
			MappingVersion: m5ai.MappingVersion,
		},
		CreatedAt: h.clock.Now(),
	}
	if _, _, err := h.db.SaveJobAIContextRevision(revision); err != nil {
		t.Fatal(err)
	}
	started, err := h.db.StartSourcingBatch(store.StartSourcingBatchRequest{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ContextRevisionHash: revision.RevisionHash, TargetCount: 1, StartedAt: h.clock.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	selectCalls := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimCandidateSelectSourcingPosition:
			selectCalls++
			if selectCalls == 1 {
				return nil, wrapRunError(
					protocol.ErrCodeCtxNotReady,
					protocol.NotReadyReasonPageAbsent,
					ErrEnsureNotReady,
				)
			}
			return defaultHandler(request)
		case protocol.PrimCandidateApplySourcingFilters:
			// 第二次职位选择已经成功即达到本测试出口；用可恢复取消阻止后续
			// 绑定逻辑把本测试扩大成完整采集夹具。
			return nil, context.Canceled
		default:
			return defaultHandler(request)
		}
	}

	result, tickErr := h.manager.Tick(context.Background())
	if tickErr != nil {
		t.Fatalf("Tick: %v", tickErr)
	}
	if len(result.Rounds) != 1 || !errors.Is(result.Rounds[0].Err, context.Canceled) {
		t.Fatalf("恢复后的定向停止结果不符: %+v", result.Rounds)
	}
	if selectCalls != 2 || h.runner.count(protocol.PrimNavEnsureSurface) != 1 ||
		h.runner.count(protocol.PrimProbePlatform) != 1 {
		t.Fatalf("页面恢复链不符: %v", h.runner.names())
	}
	wantPrefix := []string{
		protocol.PrimJobReadPublishedList,
		protocol.PrimCandidateSelectSourcingPosition,
		protocol.PrimNavEnsureSurface,
		protocol.PrimProbePlatform,
		protocol.PrimCandidateSelectSourcingPosition,
		protocol.PrimCandidateApplySourcingFilters,
	}
	if got := h.runner.names(); !reflect.DeepEqual(got, wantPrefix) {
		t.Fatalf("同一次开始未按 状态闸→select→ensure→probe→select 继续: got=%v", got)
	}
	batch, err := h.db.SourcingBatchByID(started.Batch.BatchID)
	if err != nil || batch == nil || batch.Status != store.SourcingBatchPreparing ||
		batch.Reason != "" || batch.EndedAt != nil {
		t.Fatalf("页面恢复后取消不应破坏 preparing 批次: batch=%+v err=%v", batch, err)
	}
}

func defaultHandler(request RunRequest) (any, error) {
	switch request.Name {
	case protocol.PrimJobReadPublishedList:
		// 采集开启闸的默认世界:夹具职位在线。闸本身的行为由专门用例覆盖。
		return protocol.JobReadPublishedListData{
			Sections: []protocol.JobPostingSection{
				{Label: "在线中", Names: []string{"synthetic-position"}},
				{Label: "未上线", Names: []string{}},
			},
			ObservedAt: time.Now().UnixMilli(),
		}, nil
	case protocol.PrimProbePlatform:
		fingerprint := "principal-1"
		return protocol.ProbePlatformData{
			ContentScriptOk: true, LoginState: protocol.LoginStateIn, PageKind: protocol.PageKindIm,
			PrincipalFingerprint: &fingerprint, Surface: &protocol.PlatformSurface{ImListVisible: true},
		}, nil
	case protocol.PrimNavEnsureSurface:
		return protocol.NavEnsureSurfaceData{Ready: true, LoginState: protocol.LoginStateIn}, nil
	case protocol.PrimCandidateSelectSourcingPosition:
		return protocol.CandidateSelectSourcingPositionData{
			PositionRef: "position-fixture", PositionTitle: "synthetic-position",
			ObservedAt: time.Now().UnixMilli(),
		}, nil
	case protocol.PrimCandidateApplySourcingFilters:
		var args protocol.CandidateApplySourcingFiltersArgs
		if err := json.Unmarshal(request.Args, &args); err != nil {
			return nil, err
		}
		return protocol.CandidateApplySourcingFiltersData{
			PositionRef: args.PositionRef, PositionTitle: args.PositionTitle,
			Filters: args.Filters, ObservedAt: time.Now().UnixMilli(),
		}, nil
	case protocol.PrimChatIdentifyCurrentConversation:
		// 默认世界:页面未打开任何会话。契约规定此时识别就是失败("URL 无
		// 当前会话……均失败"),临走检查据此零动作放行。
		return nil, &RunError{
			Code: protocol.ErrCodeTargetNotFound, Retryable: protocol.RetryableYes,
			SideEffect: protocol.SideEffectNone, Cause: errors.New("默认世界:页面未打开会话"),
		}
	case protocol.PrimChatReadList:
		return protocol.ChatReadListData{Sessions: []protocol.ConversationSummary{}, Complete: true}, nil
	case protocol.PrimChatReadThread:
		return protocol.ChatReadThreadData{
			Messages: []protocol.ThreadMessage{}, Peer: nil, Complete: true, ReachedTop: true,
		}, nil
	default:
		return nil, fmt.Errorf("unexpected primitive %s", request.Name)
	}
}

func decodeArgs[T any](t *testing.T, request RunRequest) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(request.Args, &out); err != nil {
		t.Fatalf("decode args %s: %v", request.Name, err)
	}
	return out
}

func ptr[T any](value T) *T { return &value }

func summary(ref, peer, preview string, unread int) protocol.ConversationSummary {
	return protocol.ConversationSummary{
		ConversationRef: ref, Peer: protocol.PeerSummary{DisplayName: "候选人-" + ref, PlatformUserRef: peer},
		UnreadCount: unread,
		LastMessage: protocol.LastMessageSummary{
			Direction: protocol.MessageDirectionIn, Kind: protocol.MessageKindText, TextPreview: preview,
		},
	}
}

// fixtureSourceKey 是测试夹具的确定性消息身份:同文即同一条消息(回显/重读
// 恒等),模拟真机每行必有的 idServer 派生键。要在同一会话造"同文的两条不同
// 消息",必须显式给行赋不同 SourceKey,不得复用本助手。
func fixtureSourceKey(text string) string {
	return syncledger.HashText("fixture-source|" + text)
}

func threadText(index int, text string) protocol.ThreadMessage {
	return protocol.ThreadMessage{
		Idx: index, Direction: protocol.MessageDirectionIn, Kind: protocol.MessageKindText,
		Text: ptr(text), BlobRef: nil, ContentHash: syncledger.HashText(text),
		SourceKey: fixtureSourceKey(text),
	}
}

func draftText(text string) store.MessageDraft {
	return store.MessageDraft{
		Direction: "in", Kind: "text", Text: ptr(text), ContentHash: syncledger.HashText(text), Origin: "external",
		SourceKey: ptr(fixtureSourceKey(text)),
	}
}

func seedTracked(t *testing.T, h *harness, conversationRef, peerRef string, adopted []store.MessageDraft) store.ConversationKey {
	t.Helper()
	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef, ConversationRef: conversationRef,
	}
	roundID := "seed-" + conversationRef
	if err := h.db.CreatePatrolRound(&store.PatrolRound{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef, RoundID: roundID,
		Trigger: "seed", Status: "running", StartedAt: h.clock.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("CreatePatrolRound seed: %v", err)
	}
	if err := h.db.SaveConversationList(store.SaveConversationListRequest{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef, RoundID: roundID,
		ObservedAt: h.clock.Now().Add(-time.Hour), Complete: true,
		Entries: []store.ListIndexEntry{{
			ConversationRef: conversationRef, PlatformUserRef: peerRef, PeerDisplayName: "候选人",
			LastMessageDirection: "in", LastMessageKind: "text", LastMessagePreview: "old",
		}},
	}); err != nil {
		t.Fatalf("SaveConversationList seed: %v", err)
	}
	if _, err := h.db.TrackConversation(key, "test", h.clock.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("TrackConversation: %v", err)
	}
	if adopted != nil {
		if _, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
			Key: key, RoundID: roundID, ExpectedTailSeq: 0, PlatformUserRef: peerRef,
			NewMessages: adopted, Adopt: true, SyncedAt: h.clock.Now(),
		}); err != nil {
			t.Fatalf("ApplyConversationChanges seed: %v", err)
		}
	}
	if err := h.db.MutatePatrolRound(h.key.Platform, h.key.AccountRef, roundID, func(round *store.PatrolRound) error {
		round.Status = "ok"
		round.Stage = "finished"
		finished := h.clock.Now().Add(-time.Hour)
		round.FinishedAt = &finished
		return nil
	}); err != nil {
		t.Fatalf("finish seed round: %v", err)
	}
	return key
}

func TestHandLogBypassesAccountGateAndRunsNoCommand(t *testing.T) {
	// handLog 必须在账号解析之前分流。手侧故障恰恰常发生在账号未绑定、掉登录或
	// 状态不正常的时候 —— 按账号门禁拒收,等于丢掉最该看的那一条。
	h := newHarness(t)
	raw, err := protocol.Encode(protocol.HandLogEventData{
		Level: protocol.HandLogLevelFatal, Code: "witnessUnavailable",
		Message: "证词库不可用，真实外部副作用能力停用", At: h.clock.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// 既没有 context,handId 也不是绑定的那只手 —— 两道门禁都该被绕过。
	body := protocol.EventBody{
		Name: protocol.EventHandLog, ObservedAt: h.clock.Now().UnixMilli(), Data: raw,
	}
	if err := h.manager.HandleEvent("某个没绑定的手", body); err != nil {
		t.Fatalf("handLog 不该被账号门禁拒收: %v", err)
	}
	if len(h.runner.names()) != 0 {
		t.Fatal("handLog 不得触发任何业务命令")
	}
}

func TestNonHandLogEventStillRequiresContext(t *testing.T) {
	// context 自 handLog 起在 schema 层是可选的,拦截责任因此落到分流处:
	// 除 handLog 外缺 context 一律拒收,否则下游解引用会让脑 panic。
	h := newHarness(t)
	raw, err := protocol.Encode(protocol.PageNavigatedEventData{
		At: h.clock.Now().UnixMilli(), PageKind: protocol.PageKindIm,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = h.manager.HandleEvent("hand-1", protocol.EventBody{
		Name: protocol.EventPageNavigated, ObservedAt: h.clock.Now().UnixMilli(), Data: raw,
	})
	if !errors.Is(err, ErrEventContextMissing) {
		t.Fatalf("缺 context 的传感事件应被拒收,实得: %v", err)
	}
}

func eventBody(t *testing.T, h *harness, name protocol.EventName, data any) protocol.EventBody {
	t.Helper()
	raw, err := protocol.Encode(data)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.EventBody{
		Context: &protocol.EventContext{Platform: h.key.Platform, AccountRef: h.key.AccountRef},
		Name:    name, ObservedAt: h.clock.Now().UnixMilli(), Data: raw,
	}
}

func seedActiveSourcingBatchForFeedInvalidation(
	t *testing.T,
	h *harness,
	batchID string,
) *store.SourcingBatch {
	t.Helper()
	documents := []m5ai.JobConfigDocument{
		{DocType: "多轮沟通", Content: "reply"},
		{DocType: "客户事实库", Content: "facts"},
		{DocType: "意向判断", Content: "intent"},
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].DocType < documents[j].DocType })
	revision := m5ai.ContextRevision{
		ContextID: "context-" + batchID, RevisionHash: "revision-" + batchID,
		SourceKind: "localImport", SourceJobRef: "17", DisplayName: "synthetic-position",
		SourcePackage: m5ai.JobConfigDocumentPackage{Documents: documents},
		Communication: m5ai.CommunicationView{
			ReplyPrompt: "reply", IntentPrompt: "intent", CustomerFacts: "facts",
			MappingVersion: m5ai.MappingVersion,
		},
		CreatedAt: h.clock.Now().Add(-time.Hour),
	}
	if _, _, err := h.db.SaveJobAIContextRevision(revision); err != nil {
		t.Fatal(err)
	}
	started, err := h.db.StartSourcingBatch(store.StartSourcingBatchRequest{
		BatchID: batchID, Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ContextRevisionHash: revision.RevisionHash, TargetCount: 30,
		StartedAt: h.clock.Now().Add(-time.Minute),
	})
	if err != nil || started == nil || !started.Created {
		t.Fatalf("建立 active 采集批次失败: result=%+v err=%v", started, err)
	}
	return &started.Batch
}

func seedStartableSourcingRevision(
	t *testing.T,
	h *harness,
	suffix string,
) string {
	t.Helper()
	documents := []m5ai.JobConfigDocument{
		{DocType: "多轮沟通", Content: "reply"},
		{DocType: "意向判断", Content: "intent"},
		{DocType: "客户事实库", Content: "facts"},
		{DocType: "候选人筛选", Content: `{"minScore":5}`},
		{DocType: "打分", Content: "请评分 {resume_json}"},
		{DocType: "招呼语", Content: `{"prompt":"状态={career_state};简历={resume_summary_json}"}`},
		{DocType: "职位筛选", Content: testfixture.SourcingFiltersDocument},
	}
	sort.Slice(documents, func(i, j int) bool {
		return documents[i].DocType < documents[j].DocType
	})
	revision := m5ai.ContextRevision{
		ContextID: "context-handoff-" + suffix, RevisionHash: "revision-handoff-" + suffix,
		SourceKind: "localImport", SourceJobRef: "17", DisplayName: "synthetic-position",
		SourcePackage: m5ai.JobConfigDocumentPackage{Documents: documents},
		Communication: m5ai.CommunicationView{
			ReplyPrompt: "reply", IntentPrompt: "intent", CustomerFacts: "facts",
			MappingVersion: m5ai.MappingVersion,
		},
		CreatedAt: h.clock.Now(),
	}
	if _, _, err := h.db.SaveJobAIContextRevision(revision); err != nil {
		t.Fatal(err)
	}
	return revision.RevisionHash
}

func countAudit(entries []store.AuditEntry, category string) int {
	count := 0
	for _, entry := range entries {
		if entry.Category == category {
			count++
		}
	}
	return count
}

func TestFirstAdoptionPaginatesAndProjectsNoHistory(t *testing.T) {
	h := newHarness(t)
	conversationKey := seedTracked(t, h, "conversation-1", "peer-1", nil)
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Move == protocol.ListWindowMoveReset {
				return protocol.ChatReadListData{
					Sessions: []protocol.ConversationSummary{summary("other", "peer-other", "irrelevant", 0)},
					Complete: false,
				}, nil
			}
			if args.Move != protocol.ListWindowMoveNext {
				t.Fatalf("unexpected list move %q", args.Move)
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{summary("conversation-1", "peer-1", "old-2", 1)}, Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
			if args.Window.Deep {
				t.Fatal("first adoption must not need deep")
			}
			if args.Cursor == "" {
				next := "thread-page-2"
				return protocol.ChatReadThreadData{
					Messages: []protocol.ThreadMessage{threadText(0, "old-2")}, Peer: ptr(protocol.PeerSummary{DisplayName: "候选人", PlatformUserRef: "peer-1"}),
					Complete: false, NextCursor: &next,
				}, nil
			}
			if args.Cursor != "thread-page-2" {
				t.Fatalf("unexpected thread cursor %q", args.Cursor)
			}
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{threadText(0, "old-1")}, Peer: nil,
				Complete: true, ReachedTop: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("round = %+v", result.Rounds)
	}
	if got := result.ProjectionCount(); got != 0 {
		t.Fatalf("first adoption projected %d historical events", got)
	}
	messages, err := h.db.MessagesForConversation(conversationKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Text == nil || *messages[0].Text != "old-1" || *messages[1].Text != "old-2" {
		t.Fatalf("thread pages were not prepended chronologically: %+v", messages)
	}
	conversation, _ := h.db.ConversationByKey(conversationKey)
	if conversation.TrackingState != store.TrackingAdopted || conversation.AdoptedBoundarySeq != 2 {
		t.Fatalf("adoption boundary = %+v", conversation)
	}
	wantNames := []string{
		protocol.PrimChatReadList,
		protocol.PrimChatReadList,
		protocol.PrimChatReadThread,
		protocol.PrimChatReadThread,
	}
	if got := h.runner.businessNames(); fmt.Sprint(got) != fmt.Sprint(wantNames) {
		t.Fatalf("command order = %v, want %v", got, wantNames)
	}
}

func TestEmptyFirstAdoptionStaysPendingAndRecoveredHistoryIsNotProjected(t *testing.T) {
	h := newHarness(t)
	conversationKey := seedTracked(t, h, "conversation-empty-adoption", "peer-empty-adoption", nil)
	threadReads := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary(conversationKey.ConversationRef, "peer-empty-adoption", "history", 1),
				},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			threadReads++
			if threadReads == 1 {
				return protocol.ChatReadThreadData{
					Messages: []protocol.ThreadMessage{}, Complete: true, ReachedTop: true,
				}, nil
			}
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{threadText(0, "history")},
				Peer:     ptr(protocol.PeerSummary{DisplayName: "候选人", PlatformUserRef: "peer-empty-adoption"}),
				Complete: true, ReachedTop: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	first, err := h.manager.Tick(context.Background())
	// 空首次快照是页面观察异常（2026-07-27 裁决归瞬时）：本轮跳过该会话，
	// 轮正常收尾，下轮自然重读。
	if err != nil || len(first.Rounds) != 1 || first.Rounds[0].Err != nil {
		t.Fatalf("空首次快照不得停轮: result=%+v err=%v", first, err)
	}
	conversation, _ := h.db.ConversationByKey(conversationKey)
	if conversation.TrackingState != store.TrackingPending || conversation.AdoptedBoundarySeq != 0 || conversation.LastMessageSeq != 0 {
		t.Fatalf("空快照不得完成收编: %+v", conversation)
	}
	if conversation.PatrolQuarantinedAt != nil {
		t.Fatalf("空快照是瞬时错误，不得隔离: %+v", conversation)
	}

	h.clock.Add(h.config.MinimumRoundGap)
	if err := h.manager.RequestImmediate(h.key); err != nil {
		t.Fatal(err)
	}
	second, err := h.manager.Tick(context.Background())
	if err != nil || len(second.Rounds) != 1 || second.Rounds[0].Err != nil {
		t.Fatalf("历史恢复后的收编失败: result=%+v err=%v", second, err)
	}
	if second.ProjectionCount() != 0 {
		t.Fatalf("恢复出的首次历史不得投影为新增: %+v", second.Rounds[0].Projections)
	}
	conversation, _ = h.db.ConversationByKey(conversationKey)
	if conversation.TrackingState != store.TrackingAdopted || conversation.AdoptedBoundarySeq != 1 || conversation.LastMessageSeq != 1 {
		t.Fatalf("恢复历史后收编边界错误: %+v", conversation)
	}
}

func TestThreadAnchorAcrossProtocolPagesStopsAndAlignsInBrain(t *testing.T) {
	h := newHarness(t)
	conversationKey := seedTracked(t, h, "conversation-cross-page-anchor", "peer-cross-page", []store.MessageDraft{
		draftText("old-1"), draftText("old-2"),
	})
	threadCalls := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{summary(conversationKey.ConversationRef, "peer-cross-page", "new", 1)},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			threadCalls++
			args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
			switch threadCalls {
			case 1:
				if args.Cursor != "" {
					t.Fatalf("首页 cursor=%q", args.Cursor)
				}
				next := "older-page"
				return protocol.ChatReadThreadData{
					Messages: []protocol.ThreadMessage{threadText(0, "old-2"), threadText(1, "new")},
					Peer:     ptr(protocol.PeerSummary{DisplayName: "候选人", PlatformUserRef: "peer-cross-page"}),
					Complete: false, NextCursor: &next,
				}, nil
			case 2:
				if args.Cursor != "older-page" {
					t.Fatalf("旧页 cursor=%q", args.Cursor)
				}
				next := "must-not-be-read"
				return protocol.ChatReadThreadData{
					Messages: []protocol.ThreadMessage{threadText(0, "older-context"), threadText(1, "old-1")},
					Complete: false, NextCursor: &next,
				}, nil
			default:
				t.Fatal("完整 anchor 已跨页聚合后不得继续读取更老页面")
			}
		default:
			return defaultHandler(request)
		}
		return nil, errors.New("unreachable")
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("Tick cross-page anchor = %+v, %v", result, err)
	}
	if threadCalls != 2 || result.ProjectionCount() != 1 {
		t.Fatalf("threadCalls=%d projection=%d", threadCalls, result.ProjectionCount())
	}
	messages, err := h.db.MessagesForConversation(conversationKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[2].Text == nil || *messages[2].Text != "new" {
		t.Fatalf("跨页 anchor 后账本错误: %+v", messages)
	}
	for _, message := range messages {
		if message.Text != nil && *message.Text == "older-context" {
			t.Fatal("锚点前上下文不得重复写入账本")
		}
	}
}

func TestLoginInitialInPreservesBindingThenProbeOnSessionChange(t *testing.T) {
	h := newHarness(t)
	if err := h.manager.HandleEvent("hand-1", eventBody(t, h, protocol.EventLoginStateChanged, protocol.LoginStateChangedEventData{
		At: h.clock.Now().UnixMilli(), Stable: true, State: protocol.LoginChangeStateIn,
	})); err != nil {
		t.Fatalf("HandleEvent in: %v", err)
	}
	account, _ := h.db.AccountByKey(h.key)
	if account.IdentityState != store.IdentityVerified || account.StoppedAt != nil || account.PausedReason != "" {
		t.Fatalf("initial in destroyed verified binding: %+v", account)
	}
	if len(h.runner.names()) != 0 {
		t.Fatal("event handler must not run commands")
	}

	h.hands.set(HandState{Online: true, Session: "session-2", BootID: "boot-2"})
	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("Tick after session change: %+v, %v", result, err)
	}
	want := []string{protocol.PrimProbePlatform, protocol.PrimChatReadList}
	if got := h.runner.businessNames(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("probe order = %v, want %v", got, want)
	}
	account, _ = h.db.AccountByKey(h.key)
	if account.IdentitySession != "session-2" || account.IdentityBootID != "boot-2" {
		t.Fatalf("fresh identity was not persisted: %+v", account)
	}

	if err := h.manager.HandleEvent("hand-1", eventBody(t, h, protocol.EventLoginStateChanged, protocol.LoginStateChangedEventData{
		At: h.clock.Now().UnixMilli(), Stable: true, State: protocol.LoginChangeStateOut,
	})); err != nil {
		t.Fatalf("HandleEvent out: %v", err)
	}
	if err := h.manager.HandleEvent("hand-1", eventBody(t, h, protocol.EventLoginStateChanged, protocol.LoginStateChangedEventData{
		At: h.clock.Now().UnixMilli(), Stable: true, State: protocol.LoginChangeStateIn,
	})); err != nil {
		t.Fatalf("HandleEvent in after out: %v", err)
	}
	account, _ = h.db.AccountByKey(h.key)
	if account.IdentityState != store.IdentityInvalid || account.PausedReason != PauseLoginRequired {
		t.Fatalf("out -> in must remain manual-only invalid: %+v", account)
	}
}

func TestEventFromNonBoundHandCannotInvalidateAccount(t *testing.T) {
	h := newHarness(t)
	err := h.manager.HandleEvent("hand-stale", eventBody(t, h, protocol.EventLoginStateChanged, protocol.LoginStateChangedEventData{
		At: h.clock.Now().UnixMilli(), Stable: true, State: protocol.LoginChangeStateOut,
	}))
	if !errors.Is(err, ErrEventHandMismatch) {
		t.Fatalf("stale hand event should be rejected: %v", err)
	}
	account, _ := h.db.AccountByKey(h.key)
	if account.IdentityState != store.IdentityVerified || account.StoppedAt != nil || account.PausedReason != "" {
		t.Fatalf("stale hand invalidated account: %+v", account)
	}
}

// 2026-08-09 身份判新换根:同文两行在身份世界不再是歧义——同 key 是同一条
// 消息(去重),异 key 是两条消息(第二条如实收编)。本测试从"宁可少投影"
// 翻转为"身份消解歧义",原同文歧义审计随位置机器退役。
func TestTrackedExpirySamePreviewDistinctIdentityAdoptsNew(t *testing.T) {
	h := newHarness(t)
	key := seedTracked(t, h, "same-preview", "peer-same", []store.MessageDraft{draftText("收到")})
	h.clock.Add(31 * time.Minute)
	repeated := threadText(1, "收到")
	repeated.SourceKey = syncledger.HashText("fixture-source|收到|第二条")
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{summary("same-preview", "peer-same", "收到", 0)},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{threadText(0, "收到"), repeated},
				Complete: true, AnchorMatched: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("expiry reconciliation failed: %+v err=%v", result, err)
	}
	if h.runner.count(protocol.PrimChatReadThread) != 1 || result.ProjectionCount() != 1 {
		t.Fatalf("异 key 同文必须如实收编为新消息: calls=%v projection=%d", h.runner.names(), result.ProjectionCount())
	}
	messages, err := h.db.MessagesForConversation(key)
	if err != nil || len(messages) != 2 {
		t.Fatalf("ledger = %+v err=%v", messages, err)
	}
	rounds, err := h.db.RecentPatrolRounds(h.key, 1)
	if err != nil || len(rounds) != 1 || rounds[0].NewMessageCount != 1 {
		t.Fatalf("第二条同文是真实新消息,必须计数: %+v err=%v", rounds, err)
	}
}

func TestEnableTodayRequiresConfiguredStartHour(t *testing.T) {
	h := newHarness(t)
	h.clock.Add(-3 * time.Hour) // 06:00
	if err := h.manager.EnableToday(h.key); !errors.Is(err, ErrDailyWindowNotOpen) {
		t.Fatalf("07:00 前不得开启巡检: %v", err)
	}
}

func TestDevelopmentWindowOverrideAllowsExplicitEnableAtRealClock(t *testing.T) {
	h := newHarness(t)
	h.clock.Add(16 * time.Hour) // 次日 01:00
	h.manager.config.DailyWindow = workflow.DailyWindowPolicy{AllowOutOfWindow: true}
	beforeEnable := h.clock.Now()
	if err := h.manager.EnableToday(h.key); err != nil {
		t.Fatalf("开发窗口覆盖后显式开启失败: %v", err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil ||
		account.EnabledDate != h.clock.Now().Format("2006-01-02") ||
		account.EnabledAt == nil || account.EnabledAt.Before(beforeEnable) ||
		account.EnabledAt.After(h.clock.Now()) {
		t.Fatalf("覆盖必须保留真实日期和时间: account=%+v err=%v", account, err)
	}
}

func TestRoundCrossingMidnightStopsBeforeUsingStaleObservation(t *testing.T) {
	h := newHarness(t)
	h.clock.Add(14*time.Hour + 59*time.Minute) // 23:59，身份已过期，首步会 probe。
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name != protocol.PrimProbePlatform {
			t.Fatalf("跨日 probe 后不得继续执行 %s", request.Name)
		}
		h.clock.Add(2 * time.Minute) // 原语返回时已过本地 24:00。
		return defaultHandler(request)
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || !errors.Is(result.Rounds[0].Err, ErrDailyWindowExpired) {
		t.Fatalf("跨日轮次应响亮失败: result=%+v err=%v", result, err)
	}
	if got := h.runner.names(); len(got) != 1 || got[0] != protocol.PrimProbePlatform {
		t.Fatalf("跨日后仍下发了命令: %v", got)
	}
	account, _ := h.db.AccountByKey(h.key)
	if account.PausedReason != PauseDailyExpired || account.StoppedAt == nil {
		t.Fatalf("跨日后必须失效当日开启状态: %+v", account)
	}
	rounds, _ := h.db.RecentPatrolRounds(h.key, 1)
	if len(rounds) != 1 || rounds[0].Status != "failed" || rounds[0].ListComplete != nil {
		t.Fatalf("跨日观测不得冒充完整列表: %+v", rounds)
	}
}

func TestLongRoundDoesNotBlockManualInteractionEvent(t *testing.T) {
	h := newHarness(t)
	seedTracked(t, h, "quiet-during-list", "peer-quiet", []store.MessageDraft{draftText("old")})
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadList {
			once.Do(func() { close(started) })
			<-release
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{summary("quiet-during-list", "peer-quiet", "new", 1)},
				Complete: true,
			}, nil
		}
		return defaultHandler(request)
	}
	tickDone := make(chan error, 1)
	go func() {
		_, err := h.manager.Tick(context.Background())
		tickDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("巡检未进入长命令")
	}
	beforeEvent := h.clock.Now()
	eventDone := make(chan error, 1)
	go func() {
		eventDone <- h.manager.HandleEvent("hand-1", eventBody(t, h, protocol.EventManualInteraction,
			protocol.ManualInteractionEventData{
				At: h.clock.Now().UnixMilli(), Kind: protocol.ManualInteractionKindNavigation, PageKind: protocol.PageKindIm,
			}))
	}()
	select {
	case err := <-eventDone:
		if err != nil {
			t.Fatalf("长轮次期间传感事件失败: %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Tick 持有全局锁跨网络命令，阻塞了用户事件")
	}
	afterEvent := h.clock.Now()
	account, _ := h.db.AccountByKey(h.key)
	if account.ManualQuietUntil != nil {
		t.Fatalf("静默窗已废除，真人事件不得再开窗: %+v", account.ManualQuietUntil)
	}
	close(release)
	select {
	case err := <-tickDone:
		if err != nil {
			t.Fatalf("释放长命令后 Tick 失败: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("长命令释放后 Tick 未结束")
	}
	if h.runner.count(protocol.PrimChatReadThread) == 0 {
		t.Fatal("废除静默窗后，真人事件不得阻止本轮继续对账")
	}
	account, _ = h.db.AccountByKey(h.key)
	if !account.DirtyHint {
		t.Fatal("长轮次中到达的用户事件被成功 finish 清掉")
	}
	// 事件拉前 = 事件处理时刻 + 合并窗;被 finish 覆盖则是巡检间隔(5 分钟),
	// 远在该区间之外。单调假时钟下事件处理时刻只能界定在事件前后读数之间。
	if account.NextPatrolAt == nil ||
		account.NextPatrolAt.Before(beforeEvent.Add(h.config.CoalesceWindow)) ||
		account.NextPatrolAt.After(afterEvent.Add(h.config.CoalesceWindow)) {
		t.Fatalf("事件拉前时刻被 finish 覆盖: got=%v want=[%v,%v]", account.NextPatrolAt,
			beforeEvent.Add(h.config.CoalesceWindow), afterEvent.Add(h.config.CoalesceWindow))
	}
}

func TestRecommendNavigationEventInvalidatesActiveSourcingFeed(t *testing.T) {
	h := newHarness(t)
	started := seedActiveSourcingBatchForFeedInvalidation(t, h, "batch-feed-navigation")
	now := h.clock.Now()

	err := h.manager.HandleEvent("hand-1", eventBody(t, h, protocol.EventManualInteraction,
		protocol.ManualInteractionEventData{
			At: now.UnixMilli(), Kind: protocol.ManualInteractionKindNavigation,
			PageKind: protocol.PageKindRecommend,
		}))
	if err != nil {
		t.Fatal(err)
	}
	handled := h.clock.Now()
	batch, err := h.db.SourcingBatchByID(started.BatchID)
	if err != nil || batch == nil || batch.Status != store.SourcingBatchStopped ||
		batch.Reason != store.SourcingFeedChangedReason || batch.EndedAt == nil ||
		batch.EndedAt.Before(now) || batch.EndedAt.After(handled) {
		t.Fatalf("推荐页 navigation 未终止旧 active 批次: batch=%+v err=%v", batch, err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil || account.SourcingFeedInvalidatedAt == nil ||
		account.SourcingFeedInvalidatedAt.Before(now) ||
		account.SourcingFeedInvalidatedAt.After(handled) || account.StoppedAt == nil ||
		account.PausedReason != store.SourcingFeedChangedReason || account.ManualQuietUntil != nil {
		t.Fatalf("推荐页 navigation 未写 marker、暂停账号（静默窗已废除不得再开）: account=%+v err=%v", account, err)
	}
}

func TestEventCannotSlipBetweenGenerationGateAndCommandStart(t *testing.T) {
	h := newHarness(t)
	startEntered := make(chan struct{})
	releaseStart := make(chan struct{})
	waitEntered := make(chan struct{})
	releaseWait := make(chan struct{})
	var once sync.Once
	h.runner.startHook = func(request RunRequest) {
		if request.Name != protocol.PrimChatReadList {
			return
		}
		once.Do(func() { close(startEntered) })
		<-releaseStart
	}
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadList {
			close(waitEntered)
			<-releaseWait
			return protocol.ChatReadListData{Sessions: []protocol.ConversationSummary{}, Complete: true}, nil
		}
		return defaultHandler(request)
	}

	tickDone := make(chan struct{})
	go func() {
		_, _ = h.manager.Tick(context.Background())
		close(tickDone)
	}()
	select {
	case <-startEntered:
	case <-time.After(time.Second):
		t.Fatal("巡检未进入 Start 临界区")
	}
	eventDone := make(chan error, 1)
	go func() {
		eventDone <- h.manager.HandleEvent("hand-1", eventBody(t, h, protocol.EventManualInteraction,
			protocol.ManualInteractionEventData{
				At: h.clock.Now().UnixMilli(), Kind: protocol.ManualInteractionKindNavigation, PageKind: protocol.PageKindIm,
			}))
	}()
	select {
	case err := <-eventDone:
		t.Fatalf("事件钻进 generation gate 与 Start 之间: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseStart)
	select {
	case <-waitEntered:
	case <-time.After(time.Second):
		t.Fatal("Start 返回后未进入无锁 Wait")
	}
	select {
	case err := <-eventDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("网络 Wait 期间事件仍被 actor 锁阻塞")
	}
	close(releaseWait)
	select {
	case <-tickDone:
	case <-time.After(time.Second):
		t.Fatal("释放 Wait 后巡检未结束")
	}
}

func TestHandGenerationChangeDuringReadListStopsBeforeUsingResult(t *testing.T) {
	h := newHarness(t)
	seedTracked(t, h, "generation-session", "peer-generation", []store.MessageDraft{draftText("old")})
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadList {
			h.hands.set(HandState{Online: true, Session: "session-2", BootID: "boot-1"})
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{summary("generation-session", "peer-generation", "new", 1)},
				Complete: true,
			}, nil
		}
		if request.Name == protocol.PrimChatReadThread {
			t.Fatal("hand session 已变化，本轮不得直接继续 readThread")
		}
		return defaultHandler(request)
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || !errors.Is(result.Rounds[0].Err, ErrActorGenerationChanged) {
		t.Fatalf("session 代际变化应中止本轮: result=%+v err=%v", result, err)
	}
	if h.runner.count(protocol.PrimChatReadThread) != 0 {
		t.Fatal("session 变化后仍派发了 readThread")
	}
	account, _ := h.db.AccountByKey(h.key)
	if !account.DirtyHint {
		t.Fatal("代际变化后必须保留 dirty 给下轮 fresh probe")
	}
}

func TestAccountRebindDuringReadListStopsBeforeUsingResult(t *testing.T) {
	h := newHarness(t)
	seedTracked(t, h, "generation-binding", "peer-binding", []store.MessageDraft{draftText("old")})
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadList {
			if _, _, err := h.db.BindAccountObservation(
				h.key, "hand-2", "principal-1", "session-2", "boot-2", h.clock.Now(), false,
			); err != nil {
				t.Fatalf("同一主体迁移到另一手: %v", err)
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{summary("generation-binding", "peer-binding", "new", 1)},
				Complete: true,
			}, nil
		}
		if request.Name == protocol.PrimChatReadThread {
			t.Fatal("账号改绑后本轮不得继续 readThread")
		}
		return defaultHandler(request)
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || !errors.Is(result.Rounds[0].Err, ErrActorGenerationChanged) {
		t.Fatalf("账号改绑应中止旧 actor 轮次: result=%+v err=%v", result, err)
	}
	account, _ := h.db.AccountByKey(h.key)
	if account.BoundHandID != "hand-2" || !account.DirtyHint {
		t.Fatalf("改绑结果或 dirty 丢失: %+v", account)
	}
}

// 2026-07-27 甲方裁决废除静默窗：手报 USER_ACTIVE 只让位本轮并催下轮
// 重试，不再开窗、不冻结账号。
func TestUserActiveYieldsRoundWithoutQuietWindow(t *testing.T) {
	h := newHarness(t)
	conversationKey := seedTracked(
		t,
		h,
		"conversation-manual-active",
		"peer-manual-active",
		[]store.MessageDraft{draftText("old")},
	)
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadList {
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary(conversationKey.ConversationRef, "peer-manual-active", "new", 1),
				},
				Complete: true,
			}, nil
		}
		if request.Name == protocol.PrimChatReadThread {
			return nil, wrapRunError(protocol.ErrCodeUserActive, "", errors.New("manual activity"))
		}
		return defaultHandler(request)
	}

	if _, err := h.manager.Tick(context.Background()); err != nil {
		t.Fatalf("USER_ACTIVE 不得升级为 Tick 失败: %v", err)
	}
	account, _ := h.db.AccountByKey(h.key)
	if account.ManualQuietUntil != nil {
		t.Fatalf("静默窗已废除，USER_ACTIVE 不得再开窗: %+v", account.ManualQuietUntil)
	}
	if account.PausedReason != "" || account.StoppedAt != nil {
		t.Fatalf("USER_ACTIVE 不得暂停账号: %+v", account)
	}
	if !account.DirtyHint {
		t.Fatal("USER_ACTIVE 让位后必须催下一轮重试")
	}
}

func TestProbeMismatchPausesBeforeAnyAccountDataRead(t *testing.T) {
	h := newHarness(t)
	h.hands.set(HandState{Online: true, Session: "session-other", BootID: "boot-other"})
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name != protocol.PrimProbePlatform {
			t.Fatalf("mismatch must stop before %s", request.Name)
		}
		fingerprint := "another-principal"
		return protocol.ProbePlatformData{
			ContentScriptOk: true, LoginState: protocol.LoginStateIn, PageKind: protocol.PageKindIm,
			PrincipalFingerprint: &fingerprint, Surface: &protocol.PlatformSurface{ImListVisible: true},
		}, nil
	}
	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err == nil {
		t.Fatalf("Tick mismatch = %+v, %v", result, err)
	}
	if got := h.runner.names(); len(got) != 1 || got[0] != protocol.PrimProbePlatform {
		t.Fatalf("cross-account data command leaked: %v", got)
	}
	account, _ := h.db.AccountByKey(h.key)
	if account.IdentityState != store.IdentityInvalid || account.PausedReason != PauseAccountMismatch || account.StoppedAt == nil {
		t.Fatalf("mismatch was not manual-only paused: %+v", account)
	}
}

// 2026-07-27 甲方裁决：单个候选人 readThread 的 manualOnly 失败只隔离该会话，
// 轮与账号照常运行；不再自动重读由会话级隔离标记保证，人工解除后才有下一次
// 对账机会。
func TestManualOnlyThreadFailureQuarantinesConversationInsteadOfPausingAccount(t *testing.T) {
	h := newHarness(t)
	conversationKey := seedTracked(t, h, "conversation-manual-only", "peer-manual-only", []store.MessageDraft{
		draftText("old"),
	})
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary(conversationKey.ConversationRef, "peer-manual-only", "new", 1),
				},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			return nil, &RunError{
				Code: protocol.ErrCodeInternalHand, Retryable: protocol.RetryableManualOnly,
				SideEffect: protocol.SideEffectPossible, Cause: errors.New("未知手侧异常"),
			}
		default:
			return defaultHandler(request)
		}
	}

	first, err := h.manager.Tick(context.Background())
	if err != nil || len(first.Rounds) != 1 || first.Rounds[0].Err != nil ||
		first.Rounds[0].Status != "ok" {
		t.Fatalf("manualOnly 只隔离当事人，轮必须正常收尾: result=%+v err=%v", first, err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("AccountByKey: account=%+v err=%v", account, err)
	}
	if account.PausedReason != "" || account.StoppedAt != nil {
		t.Fatalf("manualOnly 不得再暂停整个账号: %+v", account)
	}
	if got := h.runner.count(protocol.PrimChatReadThread); got != 1 {
		t.Fatalf("首轮 readThread 次数=%d, want 1", got)
	}
	conversation, err := h.db.ConversationByKey(conversationKey)
	if err != nil || conversation == nil || conversation.PatrolQuarantinedAt == nil ||
		conversation.PatrolQuarantineReason != "patrolQuarantine:hand:INTERNAL_HAND" {
		t.Fatalf("manualOnly 必须隔离该会话: conversation=%+v err=%v", conversation, err)
	}

	// 跨过多个巡检周期：轮照常运行，但被隔离会话不得再自动重读。
	h.clock.Add(3 * h.config.PatrolInterval)
	second, err := h.manager.Tick(context.Background())
	if err != nil || len(second.Rounds) != 1 || second.Rounds[0].Err != nil ||
		h.runner.count(protocol.PrimChatReadThread) != 1 {
		t.Fatalf("隔离后仍自动重复 intrusive: result=%+v err=%v calls=%v", second, err, h.runner.names())
	}

	// 人工解除隔离后才允许下一次对账尝试；再次失败会重新隔离。
	if cleared, err := h.db.ClearConversationPatrolQuarantine(conversationKey, h.clock.Now()); err != nil || !cleared {
		t.Fatalf("人工解除隔离: cleared=%v err=%v", cleared, err)
	}
	h.clock.Add(h.config.PatrolInterval + time.Minute)
	third, err := h.manager.Tick(context.Background())
	if err != nil || len(third.Rounds) != 1 || third.Rounds[0].Err != nil ||
		h.runner.count(protocol.PrimChatReadThread) != 2 {
		t.Fatalf("人工解除后未获得一次正常对账机会: result=%+v err=%v calls=%v", third, err, h.runner.names())
	}
	conversation, err = h.db.ConversationByKey(conversationKey)
	if err != nil || conversation == nil || conversation.PatrolQuarantinedAt == nil {
		t.Fatalf("再次失败必须重新隔离: conversation=%+v err=%v", conversation, err)
	}
}

func TestPageDrivenStaleThreadTargetContinuesVisibleWindow(t *testing.T) {
	h := newHarness(t)
	stale := seedTracked(
		t,
		h,
		"conversation-stale-page-window",
		"peer-stale-page-window",
		[]store.MessageDraft{draftText("old")},
	)
	later := seedTracked(
		t,
		h,
		"conversation-later-page-window",
		"peer-later-page-window",
		[]store.MessageDraft{draftText("later-old")},
	)
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Move != protocol.ListWindowMoveReset {
				t.Fatalf("完整单窗只应 reset 一次: %+v", args)
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary(
						stale.ConversationRef,
						"peer-stale-page-window",
						"new",
						1,
					),
					summary(
						later.ConversationRef,
						"peer-later-page-window",
						"later-new",
						1,
					),
				},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
			if args.ConversationRef == stale.ConversationRef {
				return nil, &RunError{
					Code:       protocol.ErrCodeTargetNotFound,
					Retryable:  protocol.RetryableNo,
					SideEffect: protocol.SideEffectNone,
					Cause:      errors.New("页面列表目标已离开当前虚拟窗口"),
				}
			}
			if args.ConversationRef != later.ConversationRef {
				t.Fatalf("unexpected conversation %q", args.ConversationRef)
			}
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{
					threadText(0, "later-old"),
					threadText(1, "later-new"),
				},
				Peer: ptr(protocol.PeerSummary{
					DisplayName:     "候选人",
					PlatformUserRef: "peer-later-page-window",
				}),
				Complete: true, ReachedTop: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("陈旧目标后应继续当前窗口: result=%+v err=%v", result, err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("AccountByKey: account=%+v err=%v", account, err)
	}
	if account.StoppedAt != nil || account.PausedReason != "" {
		t.Fatalf("陈旧页面目标不得暂停账号: %+v", account)
	}
	wantCalls := []string{
		protocol.PrimChatReadList,
		protocol.PrimChatReadThread,
		protocol.PrimChatReadThread,
	}
	if got := h.runner.businessNames(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("陈旧目标后没有继续当前窗口后续会话: got=%v want=%v", got, wantCalls)
	}
	rounds, err := h.db.RecentPatrolRounds(h.key, 1)
	if err != nil || len(rounds) != 1 || rounds[0].Stage != "finished" {
		t.Fatalf("同轮完成后状态错误: rounds=%+v err=%v", rounds, err)
	}
	staleMessages, err := h.db.MessagesForConversation(stale)
	if err != nil || len(staleMessages) != 1 {
		t.Fatalf("陈旧目标不应被同轮重试: messages=%+v err=%v", staleMessages, err)
	}
	messages, err := h.db.MessagesForConversation(later)
	if err != nil || len(messages) != 2 ||
		messages[1].Text == nil || *messages[1].Text != "later-new" {
		t.Fatalf("后续会话消息账本未正常收敛: messages=%+v err=%v", messages, err)
	}
}

func TestReadListWindowDriftSchedulesResetWithoutInventingManualQuiet(t *testing.T) {
	h := newHarness(t)
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadList {
			return nil, &RunError{
				Code:       protocol.ErrCodeUserActive,
				Retryable:  protocol.RetryableAfterRecovery,
				SideEffect: protocol.SideEffectNone,
				Cause:      errors.New("页面列表窗口正在换代"),
			}
		}
		return defaultHandler(request)
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("readList 页面漂移应安全收束: result=%+v err=%v", result, err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil || !account.DirtyHint ||
		account.ManualQuietUntil != nil || account.StoppedAt != nil {
		t.Fatalf("readList 页面漂移不得伪造人工静默或暂停: account=%+v err=%v", account, err)
	}
	rounds, err := h.db.RecentPatrolRounds(h.key, 1)
	if err != nil || len(rounds) != 1 || rounds[0].Stage != "listWindowPending" ||
		rounds[0].Status != "ok" {
		t.Fatalf("readList 页面漂移状态错误: rounds=%+v err=%v", rounds, err)
	}
}

func TestSourcingGenerationHandoffDropsLateManualOnlyWithoutPausingNewBatch(t *testing.T) {
	h := newHarness(t)
	revisionHash := seedStartableSourcingRevision(t, h, "local-failure")
	conversationKey := seedTracked(
		t,
		h,
		"conversation-generation-local",
		"peer-generation-local",
		[]store.MessageDraft{draftText("old")},
	)
	waitEntered := make(chan struct{})
	releaseWait := make(chan struct{})
	var once sync.Once
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary(conversationKey.ConversationRef, "peer-generation-local", "new", 1),
				},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			once.Do(func() { close(waitEntered) })
			<-releaseWait
			return nil, &RunError{
				Code: protocol.ErrCodeInternalHand, Retryable: protocol.RetryableManualOnly,
				SideEffect: protocol.SideEffectNone, Cause: errors.New("旧读取局部失败"),
			}
		default:
			return defaultHandler(request)
		}
	}

	tickDone := make(chan TickResult, 1)
	go func() {
		result, _ := h.manager.Tick(context.Background())
		tickDone <- result
	}()
	select {
	case <-waitEntered:
	case <-time.After(time.Second):
		t.Fatal("旧巡检未进入 readThread Wait")
	}

	beforeStart := h.clock.Now()
	startDone := make(chan error, 1)
	go func() {
		startDone <- h.manager.StartSourcing(h.key, revisionHash, 30, 0)
	}()
	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("StartSourcing: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("新采集启动被旧 readThread Wait 阻塞")
	}
	close(releaseWait)

	var result TickResult
	select {
	case result = <-tickDone:
	case <-time.After(time.Second):
		t.Fatal("旧巡检迟到结果未收束")
	}
	if len(result.Rounds) != 1 ||
		!errors.Is(result.Rounds[0].Err, ErrRoundSupersededBySourcingBatch) {
		t.Fatalf("旧巡检未按 generation 换代终止: %+v", result)
	}
	batch, err := h.db.ActiveSourcingBatch(h.key)
	if err != nil || batch == nil || batch.Status != store.SourcingBatchPreparing {
		t.Fatalf("新采集批次被旧失败破坏: batch=%+v err=%v", batch, err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("AccountByKey: account=%+v err=%v", account, err)
	}
	if account.StoppedAt != nil || account.PausedReason != "" ||
		account.LastPatrolAt != nil || account.NextPatrolAt == nil ||
		account.NextPatrolAt.Before(beforeStart) ||
		account.NextPatrolAt.After(h.clock.Now()) {
		t.Fatalf("旧巡检覆盖了新批次 Account 调度: %+v", account)
	}
	wantCalls := []string{protocol.PrimChatReadList, protocol.PrimChatReadThread}
	if got := h.runner.businessNames(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("换代后仍派发旧命令: got=%v want=%v", got, wantCalls)
	}
}

func TestSourcingGenerationHandoffPreservesGlobalAccountFailure(t *testing.T) {
	tests := []struct {
		name        string
		runErr      *RunError
		pauseReason string
	}{
		{
			name: "account mismatch",
			runErr: &RunError{
				Code: protocol.ErrCodeAccountMismatch, Retryable: protocol.RetryableManualOnly,
				SideEffect: protocol.SideEffectNone, Cause: errors.New("当前账号不一致"),
			},
			pauseReason: PauseAccountMismatch,
		},
		{
			name: "login required",
			runErr: &RunError{
				Code: protocol.ErrCodeCtxNotReady, Reason: protocol.NotReadyReasonLoginRequired,
				Retryable: protocol.RetryableAfterRecovery, SideEffect: protocol.SideEffectNone,
				Cause: errors.New("当前账号已登出"),
			},
			pauseReason: PauseLoginRequired,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			revisionHash := seedStartableSourcingRevision(t, h, strings.ReplaceAll(test.name, " ", "-"))
			conversationKey := seedTracked(
				t,
				h,
				"conversation-generation-global",
				"peer-generation-global",
				[]store.MessageDraft{draftText("old")},
			)
			waitEntered := make(chan struct{})
			releaseWait := make(chan struct{})
			var once sync.Once
			h.runner.handler = func(request RunRequest) (any, error) {
				switch request.Name {
				case protocol.PrimChatReadList:
					return protocol.ChatReadListData{
						Sessions: []protocol.ConversationSummary{
							summary(conversationKey.ConversationRef, "peer-generation-global", "new", 1),
						},
						Complete: true,
					}, nil
				case protocol.PrimChatReadThread:
					once.Do(func() { close(waitEntered) })
					<-releaseWait
					return nil, test.runErr
				default:
					return defaultHandler(request)
				}
			}
			tickDone := make(chan TickResult, 1)
			go func() {
				result, _ := h.manager.Tick(context.Background())
				tickDone <- result
			}()
			select {
			case <-waitEntered:
			case <-time.After(time.Second):
				t.Fatal("旧巡检未进入 readThread Wait")
			}
			if err := h.manager.StartSourcing(h.key, revisionHash, 30, 0); err != nil {
				t.Fatal(err)
			}
			close(releaseWait)
			var result TickResult
			select {
			case result = <-tickDone:
			case <-time.After(time.Second):
				t.Fatal("全局错误未完成收束")
			}
			if len(result.Rounds) != 1 {
				t.Fatalf("Tick rounds=%+v", result)
			}
			gotErr := runError(result.Rounds[0].Err)
			if gotErr == nil || gotErr.Code != test.runErr.Code ||
				gotErr.Reason != test.runErr.Reason {
				t.Fatalf("全局错误被 generation 换代屏蔽: got=%v want=%v", result.Rounds[0].Err, test.runErr)
			}
			account, err := h.db.AccountByKey(h.key)
			if err != nil || account == nil || account.StoppedAt == nil ||
				account.PausedReason != test.pauseReason ||
				account.IdentityState != store.IdentityInvalid {
				t.Fatalf("全局错误未沿用既有停机语义: account=%+v err=%v", account, err)
			}
			batch, err := h.db.ActiveSourcingBatch(h.key)
			if err != nil || batch == nil || batch.Status != store.SourcingBatchPreparing {
				t.Fatalf("全局停机不应抹掉新批次事实: batch=%+v err=%v", batch, err)
			}
		})
	}
}

func TestSourcingGenerationHandoffStopsBeforeNextCommandAfterLateSuccess(t *testing.T) {
	h := newHarness(t)
	revisionHash := seedStartableSourcingRevision(t, h, "late-success")
	conversationKey := seedTracked(
		t,
		h,
		"conversation-generation-success",
		"peer-generation-success",
		[]store.MessageDraft{draftText("old")},
	)
	waitEntered := make(chan struct{})
	releaseWait := make(chan struct{})
	var once sync.Once
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Move != protocol.ListWindowMoveReset {
				t.Fatalf("generation 换代后仍读取下一窗口: move=%q", args.Move)
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary(conversationKey.ConversationRef, "peer-generation-success", "new", 1),
				},
				Complete: false,
			}, nil
		case protocol.PrimChatReadThread:
			once.Do(func() { close(waitEntered) })
			<-releaseWait
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{
					threadText(0, "old"),
					threadText(1, "new"),
				},
				Complete: true, AnchorMatched: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}
	tickDone := make(chan TickResult, 1)
	go func() {
		result, _ := h.manager.Tick(context.Background())
		tickDone <- result
	}()
	select {
	case <-waitEntered:
	case <-time.After(time.Second):
		t.Fatal("旧巡检未进入 readThread Wait")
	}
	if err := h.manager.StartSourcing(h.key, revisionHash, 30, 0); err != nil {
		t.Fatal(err)
	}
	close(releaseWait)
	result := <-tickDone
	if len(result.Rounds) != 1 ||
		!errors.Is(result.Rounds[0].Err, ErrRoundSupersededBySourcingBatch) {
		t.Fatalf("迟到成功未在下一命令前停止: %+v", result)
	}
	wantCalls := []string{protocol.PrimChatReadList, protocol.PrimChatReadThread}
	if got := h.runner.businessNames(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("迟到成功后仍派发旧任务命令: got=%v want=%v", got, wantCalls)
	}
}

func TestPageAbsentRecoveryFailureKeepsOpaqueBindingUnobservable(t *testing.T) {
	h := newHarness(t)
	h.hands.set(HandState{Online: true, Session: "session-new", BootID: "boot-new"})
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimProbePlatform:
			return protocol.ProbePlatformData{
				ContentScriptOk: false, LoginState: protocol.LoginStateUnknown,
				PageKind: protocol.PageKindNone, PrincipalFingerprint: nil, Surface: nil,
			}, nil
		case protocol.PrimNavEnsureSurface:
			return protocol.NavEnsureSurfaceData{Ready: false, LoginState: protocol.LoginStateUnknown}, nil
		default:
			t.Fatalf("unobservable identity must not run %s", request.Name)
			return nil, nil
		}
	}
	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err == nil || !result.Rounds[0].EnsureUsed {
		t.Fatalf("Tick page absent = %+v, %v", result, err)
	}
	account, _ := h.db.AccountByKey(h.key)
	if account.IdentityState != store.IdentityUnobservable || account.PrincipalFingerprint == nil ||
		*account.PrincipalFingerprint != "principal-1" {
		t.Fatalf("page absence erased/invalidated binding: %+v", account)
	}
	if account.StoppedAt != nil || account.PausedReason != "" {
		t.Fatalf("unknown login was treated as explicit logout: %+v", account)
	}
}

// 2026-07-27 甲方裁决废除静默窗：manualInteraction 只催巡检，不再压制派发。
// 2026-08-11 起该事件 kind 只剩 navigation；此处与另两处并发用例刻意用 im 页
// 构造，避开 recommend 页会触发的推荐流作废，专注测调度语义。
func TestManualQuietAndEventCoalescingRespectMinimumGap(t *testing.T) {
	h := newHarness(t)
	if err := h.manager.HandleEvent("hand-1", eventBody(t, h, protocol.EventManualInteraction, protocol.ManualInteractionEventData{
		At: h.clock.Now().UnixMilli(), Kind: protocol.ManualInteractionKindNavigation, PageKind: protocol.PageKindIm,
	})); err != nil {
		t.Fatal(err)
	}
	after, err := h.manager.Tick(context.Background())
	if err != nil || len(after.Rounds) != 1 || after.Rounds[0].Err != nil {
		t.Fatalf("废除静默窗后事件不得压制派发: %+v %v", after, err)
	}

	// Two unread increases inside the 25s merge window do not slide the
	// schedule, and the previous round imposes the 60s lower bound.
	h.clock.Add(5 * time.Second)
	prev := 0
	if err := h.manager.HandleEvent("hand-1", eventBody(t, h, protocol.EventUnreadBadge, protocol.UnreadBadgeEventData{
		Prev: &prev, Scope: protocol.UnreadScopeTotal, Stable: true, Value: 1,
	})); err != nil {
		t.Fatal(err)
	}
	account, _ := h.db.AccountByKey(h.key)
	firstTarget := *account.NextPatrolAt
	h.clock.Add(5 * time.Second)
	prev = 1
	if err := h.manager.HandleEvent("hand-1", eventBody(t, h, protocol.EventUnreadBadge, protocol.UnreadBadgeEventData{
		Prev: &prev, Scope: protocol.UnreadScopeTotal, Stable: true, Value: 2,
	})); err != nil {
		t.Fatal(err)
	}
	account, _ = h.db.AccountByKey(h.key)
	if !account.NextPatrolAt.Equal(firstTarget) {
		t.Fatalf("merge window slid target: first=%v second=%v", firstTarget, *account.NextPatrolAt)
	}
	if account.LastPatrolAt == nil || account.NextPatrolAt.Before(account.LastPatrolAt.Add(time.Minute)) {
		t.Fatalf("event bypassed 60s minimum: %+v", account)
	}
}

func TestSameDayRestartRestoresActorAndOfflineTickQueuesNothing(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)}
	key := store.AccountKey{Platform: "zhilian", AccountRef: "restart-account"}
	if err := db.CreateAccount(&store.Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatal(err)
	}
	if err := db.BindAccountPrincipal(key, "hand-r", "principal-1", "session-1", "boot-1", clock.Now()); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{handler: defaultHandler}
	hands := &fakeHands{state: HandState{Online: false, Session: "session-1", BootID: "boot-1"}}
	config := Config{Clock: clock, Location: time.UTC, IdentityFreshFor: time.Hour, NewRoundID: func() string { return "round-restart" }}
	manager, err := NewManager(db, runner, hands, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.EnableToday(key); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manager, err = NewManager(db, runner, hands, config)
	if err != nil {
		t.Fatal(err)
	}
	offline, err := manager.Tick(context.Background())
	if err != nil || len(offline.Rounds) != 0 || runner.count(protocol.PrimChatReadList) != 0 {
		t.Fatalf("offline tick queued work: %+v %v calls=%v", offline, err, runner.names())
	}
	account, _ := db.AccountByKey(key)
	if account.LastPatrolAt != nil {
		t.Fatalf("offline tick created logical work: %+v", account)
	}
	hands.set(HandState{Online: true, Session: "session-1", BootID: "boot-1"})
	online, err := manager.Tick(context.Background())
	if err != nil || len(online.Rounds) != 1 || online.Rounds[0].Err != nil || runner.count(protocol.PrimChatReadList) != 1 {
		t.Fatalf("same-day actor did not recover: %+v %v calls=%v", online, err, runner.names())
	}
}

func TestPeriodicRoundFindsChangeWhenAllEventsAreLost(t *testing.T) {
	h := newHarness(t)
	conversationKey := seedTracked(t, h, "conversation-periodic", "peer-periodic", []store.MessageDraft{draftText("old")})
	newAvailable := false
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			preview := "old"
			if newAvailable {
				preview = "new"
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{summary(conversationKey.ConversationRef, "peer-periodic", preview, 0)}, Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{threadText(0, "old"), threadText(1, "new")},
				Peer:     ptr(protocol.PeerSummary{DisplayName: "候选人", PlatformUserRef: "peer-periodic"}),
				Complete: true, AnchorMatched: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}
	first, err := h.manager.Tick(context.Background())
	if err != nil || first.ProjectionCount() != 0 || h.runner.count(protocol.PrimChatReadThread) != 0 {
		t.Fatalf("clean first round = %+v %v calls=%v", first, err, h.runner.names())
	}
	newAvailable = true
	h.clock.Add(h.config.PatrolInterval)
	second, err := h.manager.Tick(context.Background())
	if err != nil || second.ProjectionCount() != 1 {
		t.Fatalf("periodic truth did not discover lost event: %+v %v", second, err)
	}
	messages, _ := h.db.MessagesForConversation(conversationKey)
	if len(messages) != 2 || messages[1].Text == nil || *messages[1].Text != "new" {
		t.Fatalf("ledger after periodic discovery = %+v", messages)
	}
}

func TestEnsureOncePerRoundAndThreeRoundsPauseAcrossManagerRestart(t *testing.T) {
	h := newHarness(t)
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return nil, &RunError{
				Code: protocol.ErrCodeCtxNotReady, Reason: protocol.NotReadyReasonPageAbsent,
				Cause: errors.New("page closed"),
			}
		case protocol.PrimNavEnsureSurface:
			return protocol.NavEnsureSurfaceData{Ready: true, LoginState: protocol.LoginStateIn}, nil
		default:
			return defaultHandler(request)
		}
	}

	for round := 1; round <= 3; round++ {
		result, err := h.manager.Tick(context.Background())
		if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err == nil || !result.Rounds[0].EnsureUsed {
			t.Fatalf("round %d = %+v, %v", round, result, err)
		}
		if round < 3 {
			h.clock.Add(h.config.PatrolInterval)
			// Recreate the manager to prove the safety count comes from SQLite,
			// not process memory.
			manager, managerErr := NewManager(h.db, h.runner, h.hands, h.config)
			if managerErr != nil {
				t.Fatal(managerErr)
			}
			h.manager = manager
		}
	}
	if got := h.runner.count(protocol.PrimNavEnsureSurface); got != 3 {
		t.Fatalf("ensure count = %d, want one per round", got)
	}
	if got := h.runner.count(protocol.PrimChatReadList); got != 6 {
		t.Fatalf("readList count = %d, want original+single retry per round", got)
	}
	account, _ := h.db.AccountByKey(h.key)
	if account.PausedReason != PauseSurfaceDrivenAway || account.StoppedAt == nil {
		t.Fatalf("third driven-away round did not pause: %+v", account)
	}
}

func TestRepeatedAndLateSnapshotsNeverProjectTwice(t *testing.T) {
	h := newHarness(t)
	conversationKey := seedTracked(t, h, "conversation-repeat", "peer-repeat", []store.MessageDraft{
		draftText("old-1"), draftText("old-2"),
	})
	threadRound := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{summary(conversationKey.ConversationRef, "peer-repeat", "new", 1)}, Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			threadRound++
			messages := []protocol.ThreadMessage{threadText(0, "old-1"), threadText(1, "old-2"), threadText(2, "new")}
			if threadRound == 3 {
				messages = messages[:2] // delayed snapshot wholly before current tail
			}
			return protocol.ChatReadThreadData{
				Messages: messages, Peer: ptr(protocol.PeerSummary{DisplayName: "候选人", PlatformUserRef: "peer-repeat"}),
				Complete: true, ReachedTop: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	wantProjection := []int{1, 0, 0}
	for i, want := range wantProjection {
		result, err := h.manager.Tick(context.Background())
		if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
			t.Fatalf("round %d = %+v, %v", i+1, result, err)
		}
		if got := result.ProjectionCount(); got != want {
			t.Fatalf("round %d projection = %d, want %d", i+1, got, want)
		}
		h.clock.Add(h.config.PatrolInterval)
	}
	messages, _ := h.db.MessagesForConversation(conversationKey)
	if len(messages) != 3 {
		t.Fatalf("duplicate/late snapshots changed ledger: %+v", messages)
	}
}

func TestZeroOverlapDiscardsShallowThenDeepRebaselinesWithoutProjection(t *testing.T) {
	h := newHarness(t)
	conversationKey := seedTracked(t, h, "conversation-deep", "peer-deep", []store.MessageDraft{draftText("ledger-old")})
	threadCalls := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{summary(conversationKey.ConversationRef, "peer-deep", "deep-2", 1)}, Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			threadCalls++
			args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
			if threadCalls == 1 {
				if args.Window.Deep {
					t.Fatal("first thread read unexpectedly deep")
				}
				return protocol.ChatReadThreadData{
					Messages: []protocol.ThreadMessage{threadText(0, "shallow-discard")},
					Peer:     ptr(protocol.PeerSummary{DisplayName: "候选人", PlatformUserRef: "peer-deep"}),
					Complete: true, ReachedTop: true,
				}, nil
			}
			if !args.Window.Deep || args.Cursor != "" {
				t.Fatalf("deep retry must restart without cursor: %+v", args)
			}
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{threadText(0, "deep-1"), threadText(1, "deep-2")},
				Peer:     ptr(protocol.PeerSummary{DisplayName: "候选人", PlatformUserRef: "peer-deep"}),
				Complete: true, ReachedTop: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}
	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("Tick deep = %+v, %v", result, err)
	}
	if result.ProjectionCount() != 0 {
		t.Fatalf("deep zero-overlap emitted historical projection: %+v", result.Rounds[0].Projections)
	}
	messages, _ := h.db.MessagesForConversation(conversationKey)
	if len(messages) != 3 || messages[1].Text == nil || *messages[1].Text != "deep-1" || *messages[2].Text != "deep-2" {
		t.Fatalf("deep baseline = %+v", messages)
	}
	for _, message := range messages {
		if message.Text != nil && *message.Text == "shallow-discard" {
			t.Fatal("shallow zero-overlap aggregate leaked into the ledger")
		}
	}
}

func TestConversationListOverlappingWindowsUseLatestFingerprint(t *testing.T) {
	h := newHarness(t)
	first := seedTracked(t, h, "window-first", "peer-window-first", []store.MessageDraft{
		draftText("first-old"),
	})
	second := seedTracked(t, h, "window-second", "peer-window-second", []store.MessageDraft{
		draftText("second-old"),
	})
	listCalls := 0
	var listMoves []protocol.ListWindowMove
	firstThreadCalls := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			listMoves = append(listMoves, args.Move)
			listCalls++
			switch listCalls {
			case 1:
				return protocol.ChatReadListData{
					Sessions: []protocol.ConversationSummary{
						summary(first.ConversationRef, "peer-window-first", "first-new-1", 1),
						summary(second.ConversationRef, "peer-window-second", "second-old", 0),
					},
					Complete: false,
				}, nil
			case 2:
				return protocol.ChatReadListData{
					Sessions: []protocol.ConversationSummary{
						summary(first.ConversationRef, "peer-window-first", "first-new-1", 1),
						summary(second.ConversationRef, "peer-window-second", "second-new", 1),
					},
					Complete: false,
				}, nil
			default:
				return protocol.ChatReadListData{
					Sessions: []protocol.ConversationSummary{
						summary(first.ConversationRef, "peer-window-first", "first-new-2", 1),
						summary(second.ConversationRef, "peer-window-second", "second-new", 1),
					},
					Complete: true,
				}, nil
			}
		case protocol.PrimChatReadThread:
			args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
			switch args.ConversationRef {
			case first.ConversationRef:
				firstThreadCalls++
				newText := "first-new-1"
				messages := []protocol.ThreadMessage{
					threadText(0, "first-old"),
					threadText(1, newText),
				}
				if firstThreadCalls == 2 {
					newText = "first-new-2"
					messages = append(messages, threadText(2, newText))
				}
				return protocol.ChatReadThreadData{
					Messages: messages,
					Peer: ptr(protocol.PeerSummary{
						DisplayName: "候选人", PlatformUserRef: "peer-window-first",
					}),
					Complete: true, AnchorMatched: true,
				}, nil
			case second.ConversationRef:
				return protocol.ChatReadThreadData{
					Messages: []protocol.ThreadMessage{
						threadText(0, "second-old"),
						threadText(1, "second-new"),
					},
					Peer: ptr(protocol.PeerSummary{
						DisplayName: "候选人", PlatformUserRef: "peer-window-second",
					}),
					Complete: true, AnchorMatched: true,
				}, nil
			default:
				t.Fatalf("unexpected conversation %q", args.ConversationRef)
			}
		default:
			return defaultHandler(request)
		}
		return nil, errors.New("unreachable")
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("Tick = %+v, %v", result, err)
	}
	want := []string{
		protocol.PrimChatReadList,
		protocol.PrimChatReadThread,
		protocol.PrimChatReadList,
		protocol.PrimChatReadThread,
		protocol.PrimChatReadList,
		protocol.PrimChatReadThread,
	}
	if got := h.runner.businessNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("窗口重叠/变化处理顺序错误: got=%v want=%v", got, want)
	}
	if wantMoves := []protocol.ListWindowMove{
		protocol.ListWindowMoveReset,
		protocol.ListWindowMoveNext,
		protocol.ListWindowMoveNext,
	}; !reflect.DeepEqual(listMoves, wantMoves) {
		t.Fatalf("窗口移动错误: got=%v want=%v", listMoves, wantMoves)
	}
	if firstThreadCalls != 2 {
		t.Fatalf("同 fingerprint 应跳过、变化后应重读: firstThreadCalls=%d", firstThreadCalls)
	}
	for _, key := range []store.ConversationKey{first, second} {
		messages, messagesErr := h.db.MessagesForConversation(key)
		wantCount := 2
		if key == first {
			wantCount = 3
		}
		if messagesErr != nil || len(messages) != wantCount {
			t.Fatalf("%s 未被公平收敛: messages=%+v err=%v",
				key.ConversationRef, messages, messagesErr)
		}
	}
}

func TestConversationListWindowBudgetProcessesWholeVisibleWindow(t *testing.T) {
	h := newHarness(t)
	h.manager.config.MaxPages = 1
	first := seedTracked(t, h, "budget-first", "peer-budget-first", []store.MessageDraft{
		draftText("first-old"),
	})
	second := seedTracked(t, h, "budget-second", "peer-budget-second", []store.MessageDraft{
		draftText("second-old"),
	})
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary(first.ConversationRef, "peer-budget-first", "first-new", 1),
					summary(second.ConversationRef, "peer-budget-second", "second-new", 1),
				},
				Complete: false,
			}, nil
		case protocol.PrimChatReadThread:
			args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
			oldText := "first-old"
			newText := "first-new"
			peerRef := "peer-budget-first"
			if args.ConversationRef == second.ConversationRef {
				oldText = "second-old"
				newText = "second-new"
				peerRef = "peer-budget-second"
			}
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{
					threadText(0, oldText),
					threadText(1, newText),
				},
				Peer: ptr(protocol.PeerSummary{
					DisplayName: "候选人", PlatformUserRef: peerRef,
				}),
				Complete: true, AnchorMatched: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("预算耗尽应作为部分完成收束: result=%+v err=%v", result, err)
	}
	if got := h.runner.businessNames(); !reflect.DeepEqual(got, []string{
		protocol.PrimChatReadList,
		protocol.PrimChatReadThread,
		protocol.PrimChatReadThread,
	}) {
		t.Fatalf("窗口预算不应截断当前可见窗口: %v", got)
	}
	rounds, err := h.db.RecentPatrolRounds(h.key, 1)
	if err != nil || len(rounds) != 1 || rounds[0].Status != "ok" ||
		rounds[0].Stage != "listWindowPending" {
		t.Fatalf("预算耗尽未记为部分完成: rounds=%+v err=%v", rounds, err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil || !account.DirtyHint {
		t.Fatalf("预算耗尽未安排后续 reset: account=%+v err=%v", account, err)
	}
	secondMessages, err := h.db.MessagesForConversation(second)
	if err != nil || len(secondMessages) != 2 {
		t.Fatalf("当前窗口第二会话也必须处理: messages=%+v err=%v", secondMessages, err)
	}
}

func TestWorkflowConversationGateStopsWithoutFreshRestart(t *testing.T) {
	h := newHarness(t)
	conversation := seedTracked(
		t,
		h,
		"workflow-gate-stop",
		"peer-workflow-gate-stop",
		[]store.MessageDraft{draftText("old")},
	)
	h.manager.SetWorkflowConversationGate(func() (bool, error) {
		return false, nil
	})
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary(conversation.ConversationRef, "peer-workflow-gate-stop", "new", 1),
				},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			t.Fatal("工作流 gate 已关闭时不得读取会话")
		default:
			return defaultHandler(request)
		}
		return nil, errors.New("unreachable")
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("工作流 gate 停止应成功收束: result=%+v err=%v", result, err)
	}
	if got := h.runner.businessNames(); !reflect.DeepEqual(got, []string{protocol.PrimChatReadList}) {
		t.Fatalf("工作流 gate 被误重开为 fresh 扫描: %v", got)
	}
	rounds, err := h.db.RecentPatrolRounds(h.key, 1)
	if err != nil || len(rounds) != 1 || rounds[0].Status != "ok" ||
		rounds[0].Stage != "finished" {
		t.Fatalf("工作流 gate 终局错误: rounds=%+v err=%v", rounds, err)
	}
}

func TestDatabaseConversationAbsentFromObservedWindowCannotDriveThreadRead(t *testing.T) {
	h := newHarness(t)
	visible := seedTracked(t, h, "window-visible", "peer-window-visible", []store.MessageDraft{
		draftText("visible-old"),
	})
	hidden := seedTracked(t, h, "window-hidden", "peer-window-hidden", []store.MessageDraft{
		draftText("hidden-old"),
	})
	h.clock.Add(31 * time.Minute)
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary(visible.ConversationRef, "peer-window-visible", "visible-old", 0),
				},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
			if args.ConversationRef == hidden.ConversationRef {
				t.Fatal("数据库中但页面未见的会话驱动了线程读取")
			}
			if args.ConversationRef != visible.ConversationRef {
				t.Fatalf("unexpected conversation %q", args.ConversationRef)
			}
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{threadText(0, "visible-old")},
				Complete: true, AnchorMatched: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("Tick = %+v, %v", result, err)
	}
	if got := h.runner.count(protocol.PrimChatReadThread); got != 1 {
		t.Fatalf("只应读取页面观察到的一个会话，实际 %d", got)
	}
}

// 纯翻页轮里,工作流闸必须能在读下一页之前截住本轮。闸原本只挂在"领取下
// 一个候选人"处,于是整页无人可领的轮一次都问不到它:用户点的结束要等这
// 一轮把 MaxPages 页翻完(生产上 256 页、每页 2.5~5 秒)才可能生效,期间账
// 号还在一条条发 readList。
func TestConversationGateStopsListPagingWithoutClaimableRow(t *testing.T) {
	h := newHarness(t)
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadList {
			// 页里没有任何可领的行,平台也还没到底:旧实现会一路翻到预算耗尽。
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{}, Complete: false,
			}, nil
		}
		return defaultHandler(request)
	}
	gateCalls := 0
	h.manager.SetWorkflowConversationGate(func() (bool, error) {
		gateCalls++
		// 首页不问闸,第二页放行,随后用户点了结束。
		return gateCalls == 1, nil
	})

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("闸拒绝应安全收束本轮: result=%+v err=%v", result, err)
	}
	// 首页(免闸)+ 闸放行的第二页 = 2;旧实现会一路翻到 MaxPages。
	if got := h.runner.count(protocol.PrimChatReadList); got != 2 {
		t.Fatalf("闸拒绝后仍在翻页: chat.readList 次数 = %d, want 2", got)
	}
	// 提前收束不是"页面窗口被截断",不得据此标脏账号或把下一轮提前——那
	// 个方向与"别再扫了"正好相反。
	rounds, err := h.db.RecentPatrolRounds(h.key, 1)
	if err != nil || len(rounds) != 1 || rounds[0].Status != "ok" ||
		rounds[0].Stage != "finished" {
		t.Fatalf("闸拒绝后的轮状态错误: rounds=%+v err=%v", rounds, err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil || account.DirtyHint {
		t.Fatalf("闸拒绝不得标脏账号: account=%+v err=%v", account, err)
	}
}
