package appbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/patrol"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

type sourcingActorClock struct{ now time.Time }

func (c *sourcingActorClock) Now() time.Time { return c.now }

type sourcingActorHands struct{}

func (sourcingActorHands) State(context.Context, string) (patrol.HandState, error) {
	return patrol.HandState{Online: true, Session: "session-sourcing-actor", BootID: "boot-sourcing-actor"}, nil
}

type sourcingActorAdvice struct{ requests []m5ai.CompletionRequest }

func (*sourcingActorAdvice) ProviderName() string { return "fixture-provider" }
func (*sourcingActorAdvice) ModelName() string    { return "fixture-model" }
func (a *sourcingActorAdvice) CompleteJSON(
	_ context.Context,
	request m5ai.CompletionRequest,
) (m5ai.CompletionResponse, error) {
	a.requests = append(a.requests, request)
	return m5ai.CompletionResponse{}, fmt.Errorf("正式纯采集不得调用 provider")
}

type sourcingBatchScoringAdvice struct {
	requests []m5ai.CompletionRequest
}

func (*sourcingBatchScoringAdvice) ProviderName() string { return "fixture-score-provider" }
func (*sourcingBatchScoringAdvice) ModelName() string    { return "fixture-score-model" }
func (a *sourcingBatchScoringAdvice) CompleteJSON(
	_ context.Context,
	request m5ai.CompletionRequest,
) (m5ai.CompletionResponse, error) {
	a.requests = append(a.requests, request)
	zero := 0
	response := m5ai.CompletionResponse{
		ReasoningContentEmpty: true,
		Usage: m5ai.CompletionUsage{
			InputTokens: 12, CachedInputTokens: 2, OutputTokens: 4, ReasoningTokens: &zero,
		},
	}
	switch len(a.requests) {
	case 1:
		response.JSONText = `{"score":8}`
		return response, nil
	case 2:
		return m5ai.CompletionResponse{}, fmt.Errorf("fixture transport failed")
	case 3:
		response.JSONText = `{"score":"bad"}`
		return response, nil
	default:
		return m5ai.CompletionResponse{}, fmt.Errorf("发生未授权的第 %d 次评分调用", len(a.requests))
	}
}

type postResponseInputBudgetAdvice struct {
	requests []m5ai.CompletionRequest
	response m5ai.CompletionResponse
	delay    time.Duration
}

func (*postResponseInputBudgetAdvice) ProviderName() string {
	return "fixture-input-budget-provider"
}
func (*postResponseInputBudgetAdvice) ModelName() string { return "fixture-input-budget-model" }
func (a *postResponseInputBudgetAdvice) CompleteJSON(
	_ context.Context,
	request m5ai.CompletionRequest,
) (m5ai.CompletionResponse, error) {
	a.requests = append(a.requests, request)
	if a.delay > 0 {
		time.Sleep(a.delay)
	}
	return a.response, &m5ai.ProviderError{Class: "inputTokenBudgetExceeded"}
}

type sourcingActorSender struct {
	mu         sync.Mutex
	dispatcher *dispatch.Dispatcher
	order      []string
	moves      []protocol.SourcingWindowMove
	targets    []string
	windows    [][]string
	window     int
	position   string
	candidates map[string]protocol.CandidateReadSourcingResumeData

	targetPositionOverride string
	unreadableTargets      map[string]bool
	online                 bool
	holdGreeting           bool
	greetings              []sourcingGreetingCommand
}

type sourcingGreetingCommand struct {
	handID string
	msgID  string
	args   protocol.ChatSendGreetingArgs
}

func (s *sourcingActorSender) SendEnvelope(handID string, env protocol.Envelope) error {
	if env.Kind != protocol.KindCmd {
		return nil
	}
	var body protocol.CmdBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		return err
	}
	s.order = append(s.order, body.Name)
	var data any
	switch body.Name {
	case protocol.PrimProbePlatform:
		fingerprint := "principal-sourcing-actor"
		data = protocol.ProbePlatformData{
			ContentScriptOk: true, LoginState: protocol.LoginStateIn, PageKind: protocol.PageKindRecommend,
			PrincipalFingerprint: &fingerprint,
		}
	case protocol.PrimCandidateSelectSourcingPosition:
		var args protocol.CandidateSelectSourcingPositionArgs
		if err := json.Unmarshal(body.Args, &args); err != nil {
			return err
		}
		if args.PositionTitle != "合成职位" {
			return fmt.Errorf("fixture 收到错误职位标题 %q", args.PositionTitle)
		}
		data = protocol.CandidateSelectSourcingPositionData{
			PositionRef: s.position, PositionTitle: args.PositionTitle,
			ObservedAt: time.Now().UnixMilli(),
		}
	case protocol.PrimCandidateReadSourcingWindow:
		var args protocol.CandidateReadSourcingWindowArgs
		if err := json.Unmarshal(body.Args, &args); err != nil {
			return err
		}
		s.moves = append(s.moves, args.Move)
		moved := false
		switch args.Move {
		case protocol.SourcingWindowMoveCurrent:
			// current 不发生导航，moved=false 是合法返回。
		case protocol.SourcingWindowMoveReset:
			moved = s.window != 0
			s.window = 0
		case protocol.SourcingWindowMoveNext:
			if s.window+1 < len(s.windows) {
				s.window++
				moved = true
			}
		default:
			return fmt.Errorf("测试未实现窗口动作 %s", args.Move)
		}
		if s.window < 0 || s.window >= len(s.windows) {
			return fmt.Errorf("fixture 没有窗口 %d", s.window)
		}
		positionTitle := "合成职位"
		data = protocol.CandidateReadSourcingWindowData{
			PositionRef: s.position, PlatformUserRefs: append([]string(nil), s.windows[s.window]...),
			PositionTitle: &positionTitle, Moved: moved, ObservedAt: time.Now().UnixMilli(),
		}
	case protocol.PrimCandidateReadSourcingTargetResume:
		var args protocol.CandidateReadSourcingTargetResumeArgs
		if err := json.Unmarshal(body.Args, &args); err != nil {
			return err
		}
		s.targets = append(s.targets, args.PlatformUserRef)
		if s.unreadableTargets[args.PlatformUserRef] {
			s.dispatcher.OnAck(handID, protocol.AckBody{Ref: env.MsgID, Status: protocol.AckStatusAccepted})
			s.dispatcher.OnResult(handID, "result-"+env.MsgID, protocol.ResultBody{
				Ref: env.MsgID, Status: protocol.ResultStatusFailed, ExecMs: 1,
				Error: &protocol.ErrorBody{
					Code: protocol.ErrCodeElementUnresolved, Message: "候选人简历结构不完整",
					Retryable: protocol.RetryableManualOnly, SideEffect: protocol.SideEffectNone,
				},
			})
			return nil
		}
		candidate, ok := s.candidates[args.PlatformUserRef]
		if !ok {
			return fmt.Errorf("fixture 缺少定点候选人")
		}
		if s.targetPositionOverride != "" {
			candidate.PositionRef = s.targetPositionOverride
		}
		data = candidate
	case protocol.PrimChatSendGreeting:
		var args protocol.ChatSendGreetingArgs
		if err := json.Unmarshal(body.Args, &args); err != nil {
			return err
		}
		s.mu.Lock()
		s.greetings = append(s.greetings, sourcingGreetingCommand{
			handID: handID, msgID: env.MsgID, args: args,
		})
		hold := s.holdGreeting
		s.mu.Unlock()
		s.dispatcher.OnAck(handID, protocol.AckBody{Ref: env.MsgID, Status: protocol.AckStatusAccepted})
		if hold {
			return nil
		}
		return s.completeGreeting(sourcingGreetingCommand{handID: handID, msgID: env.MsgID, args: args})
	case protocol.PrimChatReadList:
		return fmt.Errorf("正式纯采集越界调用 %s", body.Name)
	default:
		return fmt.Errorf("unexpected primitive %s", body.Name)
	}
	raw, err := protocol.Encode(data)
	if err != nil {
		return err
	}
	s.dispatcher.OnAck(handID, protocol.AckBody{Ref: env.MsgID, Status: protocol.AckStatusAccepted})
	s.dispatcher.OnResult(handID, "result-"+env.MsgID, protocol.ResultBody{
		Ref: env.MsgID, Status: protocol.ResultStatusOk, Data: raw, ExecMs: 1,
	})
	return nil
}

func (s *sourcingActorSender) HandSession(string) (string, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return "session-sourcing-actor", "boot-sourcing-actor", s.online
}
func (*sourcingActorSender) HandNegotiation(string) ([]string, []string, bool) {
	return []string{
			protocol.PrimProbePlatform + "@1",
			protocol.PrimCandidateSelectSourcingPosition + "@1",
			protocol.PrimCandidateReadSourcingWindow + "@1",
			protocol.PrimCandidateReadSourcingTargetResume + "@1",
			protocol.PrimChatSendGreeting + "@1",
		}, []string{
			string(protocol.FeatureLease1), string(protocol.FeatureProgress1), string(protocol.FeatureCancel1),
			string(protocol.FeatureWitness1),
		}, true
}
func (*sourcingActorSender) HandContractMatch(string) (bool, bool) { return true, true }
func (*sourcingActorSender) HandWitness(string) (dispatch.HandWitness, bool) {
	return dispatch.HandWitness{StoreID: "witness-sourcing-actor"}, true
}
func (*sourcingActorSender) CloseHand(string, string, string) bool { return true }
func (*sourcingActorSender) HandOfflineMs(string) int64            { return 0 }

func (s *sourcingActorSender) completeGreeting(command sourcingGreetingCommand) error {
	data, err := protocol.Encode(protocol.ChatSendGreetingData{
		PlatformUserRef: command.args.PlatformUserRef,
		PositionRef:     command.args.PositionRef,
		ContentHash:     syncledger.HashText(command.args.Text),
		ObservedAt:      time.Now().UnixMilli(),
	})
	if err != nil {
		return err
	}
	s.dispatcher.OnResult(command.handID, "result-"+command.msgID, protocol.ResultBody{
		Ref: command.msgID, Status: protocol.ResultStatusOk, Data: data, ExecMs: 1,
		Evidence: []protocol.Evidence{{Type: string(protocol.SendGreetingEvidenceTypeOutboundGreetingObserved)}},
	})
	return nil
}

func (s *sourcingActorSender) completeHeldGreeting() error {
	s.mu.Lock()
	if len(s.greetings) == 0 {
		s.mu.Unlock()
		return fmt.Errorf("fixture 没有待完成的招呼命令")
	}
	command := s.greetings[len(s.greetings)-1]
	s.holdGreeting = false
	s.mu.Unlock()
	return s.completeGreeting(command)
}

func (s *sourcingActorSender) greetingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.greetings)
}

func (s *sourcingActorSender) setOnline(online bool) {
	s.mu.Lock()
	s.online = online
	s.mu.Unlock()
}

func sourcingActorRevision(at time.Time) m5ai.ContextRevision {
	documents := []m5ai.JobConfigDocument{
		{DocType: "候选人筛选", Content: `{"minScore":5}`},
		{DocType: "多轮沟通", Content: "reply"},
		{DocType: "客户事实库", Content: "facts"},
		{DocType: "意向判断", Content: "intent"},
		{DocType: "打分", Content: "请评分 {resume_json}"},
		{DocType: "招呼语", Content: `{"prompt":"状态={career_state};简历={resume_summary_json}"}`},
		{DocType: "职位筛选", Content: `[]`},
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].DocType < documents[j].DocType })
	return m5ai.ContextRevision{
		ContextID: "context-sourcing-actor", RevisionHash: "revision-sourcing-actor",
		SourceKind: "legacyJobConfig", SourceJobRef: "61", DisplayName: "合成职位",
		SourcePackage: m5ai.JobConfigDocumentPackage{Documents: documents},
		Communication: m5ai.CommunicationView{
			ReplyPrompt: "reply", IntentPrompt: "intent", CustomerFacts: "facts", MappingVersion: m5ai.MappingVersion,
		},
		CreatedAt: at,
	}
}

func sourcingCandidate(ref, position string, observedAt time.Time) protocol.CandidateReadSourcingResumeData {
	return protocol.CandidateReadSourcingResumeData{
		PlatformUserRef: ref, PositionRef: position,
		ContactState: protocol.CandidateContactStateUnestablished, ObservedAt: observedAt.UnixMilli(),
		Basic: []protocol.CandidateResumeLabelValue{}, Expectations: []protocol.CandidateResumeLabelValue{},
		SelfEvaluation: "", Education: "", WorkExperiences: "",
	}
}

type sourcingActorHarness struct {
	t          *testing.T
	store      *store.Store
	manager    *patrol.Manager
	sender     *sourcingActorSender
	advice     *sourcingActorAdvice
	clock      *sourcingActorClock
	key        store.AccountKey
	revision   m5ai.ContextRevision
	position   string
	candidates []protocol.CandidateReadSourcingResumeData
	paceWaits  *int
}

func newSourcingActorHarness(t *testing.T, windows [][]string) *sourcingActorHarness {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	revision := sourcingActorRevision(now.Add(-time.Hour))
	if _, err := st.SaveCurrentLegacyJobAIContext(
		[]m5ai.ContextRevision{revision}, now.Add(-time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	key := store.AccountKey{Platform: "zhilian", AccountRef: "account-sourcing-actor"}
	if err := st.CreateAccount(&store.Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccountPrincipal(
		key, "hand-sourcing-actor", "principal-sourcing-actor",
		"session-sourcing-actor", "boot-sourcing-actor", now.Add(-2*time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	position := "actor-position-ref"
	byRef := make(map[string]protocol.CandidateReadSourcingResumeData)
	var candidates []protocol.CandidateReadSourcingResumeData
	for _, window := range windows {
		for _, ref := range window {
			if _, exists := byRef[ref]; exists {
				continue
			}
			candidate := sourcingCandidate(ref, position, now)
			byRef[ref] = candidate
			candidates = append(candidates, candidate)
		}
	}
	sender := &sourcingActorSender{
		windows: windows, window: 0, position: position, candidates: byRef, online: true,
	}
	dispatcher := dispatch.New(st, sender)
	sender.dispatcher = dispatcher
	clock := &sourcingActorClock{now: now}
	advice := &sourcingActorAdvice{}
	round := 0
	paceWaits := 0
	manager, err := patrol.NewManager(st, PatrolRunner{Dispatcher: dispatcher}, sourcingActorHands{}, patrol.Config{
		Clock: clock, Location: time.UTC, IdentityFreshFor: time.Hour,
		InteractionPaceWait: func(ctx context.Context) error {
			return ctx.Err()
		},
		SourcingPaceWait: func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			paceWaits++
			return nil
		},
		MinimumRoundGap: time.Millisecond, NewRoundID: func() string {
			round++
			return fmt.Sprintf("round-sourcing-actor-%d", round)
		},
	}, advice)
	if err != nil {
		t.Fatal(err)
	}
	return &sourcingActorHarness{
		t: t, store: st, manager: manager, sender: sender, advice: advice,
		clock: clock, key: key, revision: revision, position: position, candidates: candidates,
		paceWaits: &paceWaits,
	}
}

func TestFormalSourcingActorCompletesWholeBatchInOneRound(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-a", "candidate-b"}, {"candidate-b", "candidate-c"}})
	if err := h.manager.StartSourcing(h.key, h.revision.RevisionHash, 3); err != nil {
		t.Fatal(err)
	}
	started, err := h.store.ActiveSourcingBatch(h.key)
	if err != nil || started == nil {
		t.Fatalf("启动后缺少正式批次: batch=%+v err=%v", started, err)
	}
	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("正式批采单轮失败: result=%+v err=%v", result, err)
	}
	if got, want := h.sender.moves, []protocol.SourcingWindowMove{
		protocol.SourcingWindowMoveCurrent, protocol.SourcingWindowMoveNext,
	}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("窗口命令序列错误: got=%v want=%v order=%v", got, want, h.sender.order)
	}
	if got, want := h.sender.targets, []string{"candidate-a", "candidate-b", "candidate-c"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("定点读取顺序或去重错误: got=%v want=%v", got, want)
	}
	if got, want := *h.paceWaits, 2; got != want {
		t.Fatalf("相邻三个候选人应只等待两次: got=%d want=%d", got, want)
	}
	progresses, err := h.store.SourcingBatchProgressByID(started.BatchID)
	if err != nil || progresses.Status != store.SourcingBatchCompleted || progresses.CapturedCount != 3 || progresses.RemainingCount != 0 {
		t.Fatalf("批次没有原子达标: progress=%+v err=%v", progresses, err)
	}
	account, err := h.store.AccountByKey(h.key)
	if err != nil || account == nil || account.StoppedAt == nil || account.PausedReason != patrol.PauseSourcingTargetReached {
		t.Fatalf("达标后账号未自动暂停: account=%+v err=%v", account, err)
	}
	if account.SourcingEnabled || account.SourcingContextRevisionHash != "" || account.SourcingStartedAt != nil {
		t.Fatalf("正式批次不应写 legacy sourcing 双真相: %+v", account)
	}
	if len(h.advice.requests) != 0 {
		t.Fatalf("纯采集调用了 provider: %+v", h.advice.requests)
	}
	for _, name := range h.sender.order {
		if name == protocol.PrimChatReadList || name == protocol.PrimChatSendGreeting || name == protocol.PrimCandidateReadSourcingResume {
			t.Fatalf("正式批采越界进入旧采集/IM/发送: order=%v", h.sender.order)
		}
	}
	before := len(h.sender.order)
	if second, err := h.manager.Tick(context.Background()); err != nil || len(second.Rounds) != 0 || len(h.sender.order) != before {
		t.Fatalf("达标后仍继续读取: result=%+v err=%v order=%v", second, err, h.sender.order)
	}
}

func TestFormalSourcingManualStartDoesNotInheritCommunicationStartHour(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-a"}})
	h.clock.now = time.Date(2026, 7, 23, 1, 0, 0, 0, time.UTC)
	if err := h.manager.StartSourcing(h.key, h.revision.RevisionHash, 1); err != nil {
		t.Fatalf("真人显式采集不应被 08:00 沟通巡检门拦截: %v", err)
	}
	batch, err := h.store.ActiveSourcingBatch(h.key)
	if err != nil || batch == nil || batch.Status != store.SourcingBatchPreparing {
		t.Fatalf("夜间手工采集未建立唯一正式批次: batch=%+v err=%v", batch, err)
	}
}

func TestFormalSourcingActorSkipsUnreadableTargetWithinCurrentRound(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{
		{"candidate-a", "candidate-b"},
		{"candidate-a", "candidate-c"},
	})
	h.sender.unreadableTargets = map[string]bool{"candidate-a": true}
	if err := h.manager.StartSourcing(h.key, h.revision.RevisionHash, 2); err != nil {
		t.Fatal(err)
	}
	started, err := h.store.ActiveSourcingBatch(h.key)
	if err != nil || started == nil {
		t.Fatalf("启动后缺少正式批次: batch=%+v err=%v", started, err)
	}
	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("单个不可读候选人拖死整批: result=%+v err=%v", result, err)
	}
	if got, want := h.sender.targets, []string{"candidate-a", "candidate-b", "candidate-c"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("不可读候选人在后续窗口被重复读取: got=%v want=%v", got, want)
	}
	if got, want := *h.paceWaits, 2; got != want {
		t.Fatalf("跳过不可读目标后仍须维持相邻动作节奏: got=%d want=%d", got, want)
	}
	if got, want := h.sender.moves, []protocol.SourcingWindowMove{
		protocol.SourcingWindowMoveCurrent, protocol.SourcingWindowMoveNext,
	}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("跳过后未继续推进窗口: got=%v want=%v", got, want)
	}
	progress, err := h.store.SourcingBatchProgressByID(started.BatchID)
	if err != nil || progress.Status != store.SourcingBatchCompleted ||
		progress.CapturedCount != 2 || progress.RemainingCount != 0 {
		t.Fatalf("跳过后批次未按成功成员达标: progress=%+v err=%v", progress, err)
	}
	members, err := h.store.SourcingBatchExcludedPlatformUserRefs(started.BatchID)
	if err != nil || fmt.Sprint(members) != fmt.Sprint([]string{"candidate-b", "candidate-c"}) {
		t.Fatalf("不可读候选人被误记为批次成员: members=%v err=%v", members, err)
	}
}

func TestResumedSourcingWaitsBeforeFirstNewTargetAfterWindowMove(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-a"}})
	if err := h.manager.StartSourcing(h.key, h.revision.RevisionHash, 2); err != nil {
		t.Fatal(err)
	}
	started, err := h.store.ActiveSourcingBatch(h.key)
	if err != nil || started == nil {
		t.Fatalf("启动后缺少正式批次: batch=%+v err=%v", started, err)
	}
	if result, err := h.manager.Tick(context.Background()); err != nil || len(result.Rounds) != 1 {
		t.Fatalf("首轮采集失败: result=%+v err=%v", result, err)
	}
	blocked, err := h.store.SourcingBatchByID(started.BatchID)
	if err != nil || blocked == nil || blocked.Status != store.SourcingBatchBlocked {
		t.Fatalf("单窗耗尽后未进入可恢复 blocked: batch=%+v err=%v", blocked, err)
	}

	h.sender.windows = [][]string{{"candidate-a"}, {"candidate-b"}}
	h.sender.window = 0
	h.sender.candidates["candidate-b"] = sourcingCandidate("candidate-b", h.position, h.clock.Now())
	if _, err := h.store.ResumeSourcingBatch(store.ResumeSourcingBatchRequest{BatchID: started.BatchID}); err != nil {
		t.Fatal(err)
	}
	h.clock.now = h.clock.now.Add(time.Millisecond)
	if err := h.manager.EnableToday(h.key); err != nil {
		t.Fatal(err)
	}
	beforeWaits := *h.paceWaits
	if result, err := h.manager.Tick(context.Background()); err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("恢复后采集失败: result=%+v err=%v", result, err)
	}
	if got := *h.paceWaits - beforeWaits; got != 1 {
		t.Fatalf("滚动后首个新候选人必须等待一次候选人节奏: got=%d want=1", got)
	}
	if got, want := h.sender.targets, []string{"candidate-a", "candidate-b"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("恢复后目标顺序错误: got=%v want=%v", got, want)
	}
}

func TestCompletedSourcingBatchScoresEveryMemberWithoutTouchingHand(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-a", "candidate-b", "candidate-c"}})
	if err := h.manager.StartSourcing(h.key, h.revision.RevisionHash, 3); err != nil {
		t.Fatal(err)
	}
	batch, err := h.store.ActiveSourcingBatch(h.key)
	if err != nil || batch == nil {
		t.Fatalf("启动后缺少正式批次: batch=%+v err=%v", batch, err)
	}
	if _, err := h.manager.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}

	beforeHandCalls := len(h.sender.order)
	advice := &sourcingBatchScoringAdvice{}
	scorer, err := patrol.NewManager(
		h.store, PatrolRunner{Dispatcher: h.sender.dispatcher}, sourcingActorHands{},
		patrol.Config{Clock: h.clock, Location: time.UTC}, advice,
	)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := scorer.ScoreCompletedSourcingBatch(context.Background(), batch.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if !progress.Completed || progress.TargetCount != 3 || progress.OKCount != 1 ||
		progress.FailedCount != 2 || progress.InFlightCount != 0 || progress.PendingCount != 0 ||
		progress.Provider != advice.ProviderName() || progress.Model != advice.ModelName() {
		t.Fatalf("统一评分没有完整终局: %+v", progress)
	}
	if len(advice.requests) != 3 {
		t.Fatalf("provider 调用次数错误: %d", len(advice.requests))
	}
	for _, request := range advice.requests {
		if request.Purpose != m5ai.PurposeScoring || request.MaxOutputTokens != m5ai.ScoringOutputTokenLimit {
			t.Fatalf("评分请求越界: %+v", request)
		}
	}
	if len(h.sender.order) != beforeHandCalls {
		t.Fatalf("统一评分触碰了 hand: before=%d after=%d order=%v", beforeHandCalls, len(h.sender.order), h.sender.order)
	}

	replayed, err := scorer.ScoreCompletedSourcingBatch(context.Background(), batch.BatchID)
	if err != nil || !replayed.Completed || len(advice.requests) != 3 {
		t.Fatalf("重复统一评分产生了新调用: progress=%+v requests=%d err=%v", replayed, len(advice.requests), err)
	}
}

func TestCompletedSourcingBatchPostResponseTokenBudgetKeepsUsageWithoutScore(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-a"}})
	if err := h.manager.StartSourcing(h.key, h.revision.RevisionHash, 1); err != nil {
		t.Fatal(err)
	}
	batch, err := h.store.ActiveSourcingBatch(h.key)
	if err != nil || batch == nil {
		t.Fatalf("启动后缺少正式批次: batch=%+v err=%v", batch, err)
	}
	if _, err := h.manager.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, err := h.store.NextSourcingBatchRunWithoutScore(batch.BatchID)
	if err != nil || run == nil {
		t.Fatalf("评分前缺少待评分成员: run=%+v err=%v", run, err)
	}

	zero := 0
	usage := m5ai.CompletionUsage{
		InputTokens:       m5ai.ReplyInputTokenLimit + 1,
		CachedInputTokens: 301,
		OutputTokens:      5,
		ReasoningTokens:   &zero,
	}
	advice := &postResponseInputBudgetAdvice{
		response: m5ai.CompletionResponse{
			JSONText: `{"score":9}`, Usage: usage, ReasoningContentEmpty: true,
		},
		delay: 2 * time.Millisecond,
	}
	scorer, err := patrol.NewManager(
		h.store, PatrolRunner{Dispatcher: h.sender.dispatcher}, sourcingActorHands{},
		patrol.Config{Clock: h.clock, Location: time.UTC}, advice,
	)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := scorer.ScoreCompletedSourcingBatch(context.Background(), batch.BatchID)
	if err != nil || !progress.Completed || progress.OKCount != 0 || progress.FailedCount != 1 ||
		len(advice.requests) != 1 {
		t.Fatalf("超 token 评分未形成单次失败终局: progress=%+v calls=%d err=%v",
			progress, len(advice.requests), err)
	}
	invocation, err := h.store.SourcingScoreByRunID(run.RunID)
	if err != nil || invocation == nil ||
		invocation.Status != store.AIInvocationBudgetBlocked ||
		invocation.ErrorClass != "inputTokenBudgetExceeded" || invocation.Score != nil ||
		invocation.OutputHash == "" ||
		invocation.InputTokens != usage.InputTokens ||
		invocation.CachedInputTokens != usage.CachedInputTokens ||
		invocation.OutputTokens != usage.OutputTokens ||
		invocation.UsageShape != store.AIInvocationUsageComplete ||
		invocation.ReasoningTokens == nil || *invocation.ReasoningTokens != 0 ||
		invocation.LatencyMs < 1 ||
		invocation.EstimatedCostMicros != m5ai.EstimatedCostMicros(usage) {
		t.Fatalf("超 token 评分计量或零分数事实错误: invocation=%+v err=%v", invocation, err)
	}
}

func TestCompletedSourcingBatchWithoutProviderCreatesNoReservation(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-a"}})
	if err := h.manager.StartSourcing(h.key, h.revision.RevisionHash, 1); err != nil {
		t.Fatal(err)
	}
	batch, err := h.store.ActiveSourcingBatch(h.key)
	if err != nil || batch == nil {
		t.Fatalf("启动后缺少正式批次: batch=%+v err=%v", batch, err)
	}
	if _, err := h.manager.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	scorer, err := patrol.NewManager(
		h.store, PatrolRunner{Dispatcher: h.sender.dispatcher}, sourcingActorHands{},
		patrol.Config{Clock: h.clock, Location: time.UTC},
	)
	if err != nil {
		t.Fatal(err)
	}
	if progress, err := scorer.ScoreCompletedSourcingBatch(context.Background(), batch.BatchID); progress == nil ||
		progress.PendingCount != 1 || err != patrol.ErrSourcingScoringProviderUnavailable {
		t.Fatalf("缺少 provider 未响亮拒绝: progress=%+v err=%v", progress, err)
	}
	progress, err := h.store.SourcingBatchScoringProgress(batch.BatchID)
	if err != nil || progress.PendingCount != 1 || progress.InFlightCount != 0 || progress.Completed {
		t.Fatalf("缺少 provider 仍创建了预留: progress=%+v err=%v", progress, err)
	}
}

func TestFormalSourcingActorBlocksOnTargetPositionMismatch(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-a"}})
	h.sender.targetPositionOverride = "other-position"
	if err := h.manager.StartSourcing(h.key, h.revision.RevisionHash, 1); err != nil {
		t.Fatal(err)
	}
	started, err := h.store.ActiveSourcingBatch(h.key)
	if err != nil || started == nil {
		t.Fatalf("启动后缺少正式批次: batch=%+v err=%v", started, err)
	}
	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err == nil {
		t.Fatalf("职位错绑应响亮失败并阻塞: result=%+v err=%v", result, err)
	}
	batch, err := h.store.SourcingBatchByID(started.BatchID)
	if err != nil || batch == nil || batch.Status != store.SourcingBatchBlocked || batch.Reason != "targetReadFailed" {
		t.Fatalf("职位错绑未进入固定 blocked: batch=%+v err=%v", batch, err)
	}
	account, _ := h.store.AccountByKey(h.key)
	if account == nil || account.PausedReason != patrol.PauseSourcingBlocked {
		t.Fatalf("职位错绑未暂停 actor: %+v", account)
	}
	if len(h.sender.targets) != 1 || len(h.sender.moves) != 1 {
		t.Fatalf("职位错绑后仍继续读取: moves=%v targets=%v", h.sender.moves, h.sender.targets)
	}
}
