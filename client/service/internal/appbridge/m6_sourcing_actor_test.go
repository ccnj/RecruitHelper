package appbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/patrol"
	"recruithelper/client/service/internal/store"
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

type sourcingActorSender struct {
	dispatcher *dispatch.Dispatcher
	order      []string
	moves      []protocol.SourcingWindowMove
	targets    []string
	windows    [][]string
	window     int
	position   string
	candidates map[string]protocol.CandidateReadSourcingResumeData

	targetPositionOverride string
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
		data = protocol.CandidateReadSourcingWindowData{
			PositionRef: s.position, PlatformUserRefs: append([]string(nil), s.windows[s.window]...),
			Moved: moved, ObservedAt: time.Now().UnixMilli(),
		}
	case protocol.PrimCandidateReadSourcingTargetResume:
		var args protocol.CandidateReadSourcingTargetResumeArgs
		if err := json.Unmarshal(body.Args, &args); err != nil {
			return err
		}
		s.targets = append(s.targets, args.PlatformUserRef)
		candidate, ok := s.candidates[args.PlatformUserRef]
		if !ok {
			return fmt.Errorf("fixture 缺少定点候选人")
		}
		if s.targetPositionOverride != "" {
			candidate.PositionRef = s.targetPositionOverride
		}
		data = candidate
	case protocol.PrimChatReadList, protocol.PrimChatSendGreeting:
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

func (*sourcingActorSender) HandSession(string) (string, string, bool) {
	return "session-sourcing-actor", "boot-sourcing-actor", true
}
func (*sourcingActorSender) HandNegotiation(string) ([]string, []string, bool) {
	return []string{
			protocol.PrimProbePlatform + "@1",
			protocol.PrimCandidateReadSourcingWindow + "@1",
			protocol.PrimCandidateReadSourcingTargetResume + "@1",
		}, []string{
			string(protocol.FeatureLease1), string(protocol.FeatureProgress1), string(protocol.FeatureCancel1),
		}, true
}
func (*sourcingActorSender) HandContractMatch(string) (bool, bool) { return true, true }
func (*sourcingActorSender) HandWitness(string) (dispatch.HandWitness, bool) {
	return dispatch.HandWitness{}, false
}
func (*sourcingActorSender) CloseHand(string, string, string) bool { return true }
func (*sourcingActorSender) HandOfflineMs(string) int64            { return 0 }

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
		SourceKind: "localImport", SourceJobRef: "61", DisplayName: "合成职位",
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
	if _, _, err := st.SaveJobAIContextRevision(revision); err != nil {
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
		windows: windows, window: 0, position: position, candidates: byRef,
	}
	dispatcher := dispatch.New(st, sender)
	sender.dispatcher = dispatcher
	clock := &sourcingActorClock{now: now}
	advice := &sourcingActorAdvice{}
	round := 0
	manager, err := patrol.NewManager(st, PatrolRunner{Dispatcher: dispatcher}, sourcingActorHands{}, patrol.Config{
		Clock: clock, Location: time.UTC, IdentityFreshFor: time.Hour,
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
