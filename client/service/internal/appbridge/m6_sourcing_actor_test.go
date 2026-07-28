package appbridge

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

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/patrol"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/client/service/internal/testfixture"
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

// sourcingBatchScoringAdvice 按成员标记与该成员的第几次尝试出剧本。评分
// 编排器并发驱动成员且失败会重试，fake 必须并发安全并按成员分流，不能再
// 按全局调用序号假设顺序。
type sourcingBatchScoringAdvice struct {
	mu       sync.Mutex
	requests []m5ai.CompletionRequest
	markers  []string
	attempts map[string]int
	respond  func(marker string, attempt int) (m5ai.CompletionResponse, error)
}

func scoringFixtureResponse(jsonText string) m5ai.CompletionResponse {
	zero := 0
	return m5ai.CompletionResponse{
		JSONText:              jsonText,
		ReasoningContentEmpty: true,
		Usage: m5ai.CompletionUsage{
			InputTokens: 12, CachedInputTokens: 2, OutputTokens: 4, ReasoningTokens: &zero,
		},
	}
}

func (*sourcingBatchScoringAdvice) ProviderName() string { return "fixture-score-provider" }
func (*sourcingBatchScoringAdvice) ModelName() string    { return "fixture-score-model" }
func (a *sourcingBatchScoringAdvice) CompleteJSON(
	_ context.Context,
	request m5ai.CompletionRequest,
) (m5ai.CompletionResponse, error) {
	a.mu.Lock()
	a.requests = append(a.requests, request)
	marker := ""
	for _, candidate := range a.markers {
		if strings.Contains(request.UserContent, candidate) {
			marker = candidate
			break
		}
	}
	if a.attempts == nil {
		a.attempts = make(map[string]int)
	}
	a.attempts[marker]++
	attempt := a.attempts[marker]
	respond := a.respond
	a.mu.Unlock()
	if respond == nil {
		return scoringFixtureResponse(`{"score":8}`), nil
	}
	return respond(marker, attempt)
}

func (a *sourcingBatchScoringAdvice) requestCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.requests)
}

func (a *sourcingBatchScoringAdvice) requestsSnapshot() []m5ai.CompletionRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]m5ai.CompletionRequest, len(a.requests))
	copy(out, a.requests)
	return out
}

func noSourcingAIRetryWait(context.Context, bool, int) error { return nil }

type postResponseInputBudgetAdvice struct {
	mu       sync.Mutex
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
	a.mu.Lock()
	a.requests = append(a.requests, request)
	a.mu.Unlock()
	if a.delay > 0 {
		time.Sleep(a.delay)
	}
	return a.response, &m5ai.ProviderError{Class: "inputTokenBudgetExceeded"}
}

type sourcingActorSender struct {
	mu         sync.Mutex
	dispatcher *dispatch.Dispatcher
	order      []string
	filterArgs []protocol.CandidateApplySourcingFiltersArgs
	moves      []protocol.SourcingWindowMove
	targets    []string
	windows    [][]string
	window     int
	position   string
	candidates map[string]protocol.CandidateReadSourcingResumeData

	targetPositionOverride string
	unreadableTargets      map[string]bool
	filterFailures         int
	online                 bool
	holdGreeting           bool
	greetings              []sourcingGreetingCommand
	afterFirstGreeting     [][]string
	probeFingerprint       string
	windowPageAbsent       bool
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
		if s.probeFingerprint != "" {
			fingerprint = s.probeFingerprint
		}
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
	case protocol.PrimCandidateApplySourcingFilters:
		var args protocol.CandidateApplySourcingFiltersArgs
		if err := json.Unmarshal(body.Args, &args); err != nil {
			return err
		}
		s.filterArgs = append(s.filterArgs, args)
		if s.filterFailures > 0 {
			s.filterFailures--
			s.dispatcher.OnAck(handID, protocol.AckBody{Ref: env.MsgID, Status: protocol.AckStatusAccepted})
			s.dispatcher.OnResult(handID, "result-"+env.MsgID, protocol.ResultBody{
				Ref: env.MsgID, Status: protocol.ResultStatusFailed, ExecMs: 1,
				Error: &protocol.ErrorBody{
					Code: protocol.ErrCodeElementUnresolved, Message: "筛选面暂不可读",
					Retryable: protocol.RetryableManualOnly, SideEffect: protocol.SideEffectNone,
				},
			})
			return nil
		}
		if args.PositionRef != s.position || args.PositionTitle != "合成职位" {
			return fmt.Errorf("fixture 收到错误筛选职位: %+v", args)
		}
		data = protocol.CandidateApplySourcingFiltersData{
			PositionRef: args.PositionRef, PositionTitle: args.PositionTitle,
			Filters: args.Filters, ObservedAt: time.Now().UnixMilli(),
		}
	case protocol.PrimCandidateReadSourcingWindow:
		if s.windowPageAbsent {
			s.dispatcher.OnAck(handID, protocol.AckBody{
				Ref: env.MsgID, Status: protocol.AckStatusAccepted,
			})
			s.dispatcher.OnResult(handID, "result-"+env.MsgID, protocol.ResultBody{
				Ref: env.MsgID, Status: protocol.ResultStatusFailed, ExecMs: 1,
				Error: &protocol.ErrorBody{
					Code:    protocol.ErrCodeCtxNotReady,
					Data:    json.RawMessage(`{"reason":"pageAbsent"}`),
					Message: "当前智联页面不是推荐页", Retryable: protocol.RetryableAfterRecovery,
					SideEffect: protocol.SideEffectNone,
				},
			})
			return nil
		}
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
			protocol.PrimCandidateApplySourcingFilters + "@1",
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
	s.mu.Lock()
	if len(s.greetings) == 1 && s.afterFirstGreeting != nil {
		s.windows = s.afterFirstGreeting
		if s.window >= len(s.windows) {
			s.window = len(s.windows) - 1
		}
	}
	s.mu.Unlock()
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
		{DocType: "职位筛选", Content: testfixture.SourcingFiltersDocument},
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
	var setupOrder []string
	for _, name := range h.sender.order {
		switch name {
		case protocol.PrimCandidateSelectSourcingPosition,
			protocol.PrimCandidateApplySourcingFilters,
			protocol.PrimCandidateReadSourcingWindow:
			setupOrder = append(setupOrder, name)
		}
		if len(setupOrder) == 3 {
			break
		}
	}
	if want := []string{
		protocol.PrimCandidateSelectSourcingPosition,
		protocol.PrimCandidateApplySourcingFilters,
		protocol.PrimCandidateReadSourcingWindow,
	}; !reflect.DeepEqual(setupOrder, want) {
		t.Fatalf("采集准备顺序错误: got=%v want=%v order=%v", setupOrder, want, h.sender.order)
	}
	view, err := m5ai.DeriveSourcingView(h.revision.SourcePackage)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.sender.filterArgs) != 1 ||
		h.sender.filterArgs[0].PositionRef != h.position ||
		h.sender.filterArgs[0].PositionTitle != h.revision.DisplayName ||
		!reflect.DeepEqual(h.sender.filterArgs[0].Filters, view.JobFilters) {
		t.Fatalf("筛选命令未绑定同一职位 revision: args=%+v view=%+v",
			h.sender.filterArgs, view.JobFilters)
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
	completedBatch, err := h.store.SourcingBatchByID(started.BatchID)
	if err != nil || completedBatch == nil || completedBatch.PositionRef == nil ||
		*completedBatch.PositionRef != h.position || completedBatch.PositionTitle == nil ||
		*completedBatch.PositionTitle != h.revision.DisplayName {
		t.Fatalf("筛选确认后的首窗未形成职位绑定: batch=%+v err=%v", completedBatch, err)
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

func TestFormalSourcingFilterFailureStaysUnboundAndResumeRepeatsPreparation(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-a"}})
	h.sender.filterFailures = 1
	if err := h.manager.StartSourcing(h.key, h.revision.RevisionHash, 1); err != nil {
		t.Fatal(err)
	}
	started, err := h.store.ActiveSourcingBatch(h.key)
	if err != nil || started == nil {
		t.Fatalf("启动后缺少正式批次: batch=%+v err=%v", started, err)
	}
	first, err := h.manager.Tick(context.Background())
	if err != nil || len(first.Rounds) != 1 || first.Rounds[0].Err == nil {
		t.Fatalf("筛选失败未响亮终止本轮: result=%+v err=%v", first, err)
	}
	blocked, err := h.store.SourcingBatchByID(started.BatchID)
	if err != nil || blocked == nil || blocked.Status != store.SourcingBatchBlocked ||
		blocked.Reason != "filtersApplyFailed" || blocked.PositionRef != nil ||
		blocked.PositionTitle != nil || len(h.sender.moves) != 0 || len(h.sender.targets) != 0 {
		t.Fatalf("筛选失败后批次越过绑定边界: batch=%+v moves=%v targets=%v err=%v",
			blocked, h.sender.moves, h.sender.targets, err)
	}
	account, err := h.store.AccountByKey(h.key)
	if err != nil || account == nil || account.PausedReason != patrol.PauseHandManualReview {
		t.Fatalf("筛选失败未沿既有 manualOnly 路径暂停账号: account=%+v err=%v", account, err)
	}

	if _, err := h.store.ResumeSourcingBatch(store.ResumeSourcingBatchRequest{BatchID: started.BatchID}); err != nil {
		t.Fatal(err)
	}
	h.clock.now = h.clock.now.Add(time.Millisecond)
	if err := h.manager.EnableToday(h.key); err != nil {
		t.Fatal(err)
	}
	second, err := h.manager.Tick(context.Background())
	if err != nil || len(second.Rounds) != 1 || second.Rounds[0].Err != nil {
		t.Fatalf("重新武装后未重做完整准备链: result=%+v err=%v", second, err)
	}
	completed, err := h.store.SourcingBatchByID(started.BatchID)
	if err != nil || completed == nil || completed.Status != store.SourcingBatchCompleted ||
		completed.PositionRef == nil || *completed.PositionRef != h.position {
		t.Fatalf("恢复后批次未成功绑定并完成: batch=%+v err=%v", completed, err)
	}
	if got, want := h.sender.order, []string{
		protocol.PrimProbePlatform,
		protocol.PrimCandidateSelectSourcingPosition,
		protocol.PrimCandidateApplySourcingFilters,
		protocol.PrimCandidateSelectSourcingPosition,
		protocol.PrimCandidateApplySourcingFilters,
		protocol.PrimCandidateReadSourcingWindow,
		protocol.PrimCandidateReadSourcingTargetResume,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("恢复没有从职位选择重新执行: got=%v want=%v", got, want)
	}
	if len(h.sender.filterArgs) != 2 ||
		!reflect.DeepEqual(h.sender.filterArgs[0], h.sender.filterArgs[1]) {
		t.Fatalf("恢复前后筛选目标漂移: %+v", h.sender.filterArgs)
	}
}

func TestFormalSourcingManualStartRespectsUnifiedBusinessWindow(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-a"}})
	h.clock.now = time.Date(2026, 7, 23, 1, 0, 0, 0, time.UTC)
	if err := h.manager.StartSourcing(h.key, h.revision.RevisionHash, 1); !errors.Is(err, patrol.ErrDailyWindowNotOpen) {
		t.Fatalf("08:00 前正式采集必须被统一业务窗口拒绝: %v", err)
	}
	batch, err := h.store.ActiveSourcingBatch(h.key)
	if err != nil || batch != nil {
		t.Fatalf("夜间拒绝不得留下正式批次: batch=%+v err=%v", batch, err)
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
	for ref, marker := range map[string]string{
		"candidate-a": "marker-alpha", "candidate-b": "marker-bravo", "candidate-c": "marker-charlie",
	} {
		candidate := h.sender.candidates[ref]
		candidate.SelfEvaluation = marker
		h.sender.candidates[ref] = candidate
	}
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
	work, err := h.store.PendingSourcingScoreWork(batch.BatchID)
	if err != nil || len(work) != 3 {
		t.Fatalf("评分前待驱动成员错误: work=%d err=%v", len(work), err)
	}
	// alpha 一次成功；bravo 每次传输失败，预算耗尽（1+3 次）后终局失败；
	// charlie 每次输出不可解析，同样耗尽预算后终局失败。
	advice := &sourcingBatchScoringAdvice{
		markers: []string{"marker-alpha", "marker-bravo", "marker-charlie"},
		respond: func(marker string, attempt int) (m5ai.CompletionResponse, error) {
			switch marker {
			case "marker-alpha":
				return scoringFixtureResponse(`{"score":8}`), nil
			case "marker-bravo":
				return m5ai.CompletionResponse{}, fmt.Errorf("fixture transport failed")
			default:
				return scoringFixtureResponse(`{"score":"bad"}`), nil
			}
		},
	}
	scorer, err := patrol.NewManager(
		h.store, PatrolRunner{Dispatcher: h.sender.dispatcher}, sourcingActorHands{},
		patrol.Config{Clock: h.clock, Location: time.UTC, SourcingAIRetryWait: noSourcingAIRetryWait},
		advice,
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
	requests := advice.requestsSnapshot()
	if len(requests) != 9 {
		t.Fatalf("provider 调用次数错误(1 成功 + 2×4 预算耗尽): %d", len(requests))
	}
	attemptSuffixes := 0
	for _, request := range requests {
		if request.Purpose != m5ai.PurposeScoring || request.MaxOutputTokens != m5ai.ScoringOutputTokenLimit {
			t.Fatalf("评分请求越界: %+v", request)
		}
		if strings.Contains(request.InvocationID, "#a") {
			attemptSuffixes++
		}
	}
	if attemptSuffixes != 6 {
		t.Fatalf("重试尝试缺少独立追踪身份: suffixed=%d requests=%d", attemptSuffixes, len(requests))
	}
	if len(h.sender.order) != beforeHandCalls {
		t.Fatalf("统一评分触碰了 hand: before=%d after=%d order=%v", beforeHandCalls, len(h.sender.order), h.sender.order)
	}
	for marker, wantAttempts := range map[string]int{
		"marker-bravo": 4, "marker-charlie": 4,
	} {
		if got := advice.attempts[marker]; got != wantAttempts {
			t.Fatalf("%s 预算内尝试次数错误: got=%d want=%d", marker, got, wantAttempts)
		}
	}
	failedRuns := 0
	for _, item := range work {
		invocation, err := h.store.SourcingScoreByRunID(item.Run.RunID)
		if err != nil || invocation == nil {
			t.Fatalf("成员缺少评分终局: run=%s err=%v", item.Run.RunID, err)
		}
		if invocation.Status == store.AIInvocationOK {
			if invocation.AttemptCount != 1 || invocation.BudgetedAttemptCount != 1 {
				t.Fatalf("成功成员尝试计数错误: %+v", invocation)
			}
			continue
		}
		failedRuns++
		if invocation.AttemptCount != 4 || invocation.BudgetedAttemptCount != 4 ||
			invocation.Score != nil || invocation.FinishedAt == nil {
			t.Fatalf("失败成员未按预算耗尽终局: %+v", invocation)
		}
	}
	if failedRuns != 2 {
		t.Fatalf("失败成员数量错误: %d", failedRuns)
	}

	replayed, err := scorer.ScoreCompletedSourcingBatch(context.Background(), batch.BatchID)
	if err != nil || !replayed.Completed || advice.requestCount() != 9 {
		t.Fatalf("重复统一评分产生了新调用: progress=%+v requests=%d err=%v", replayed, advice.requestCount(), err)
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
