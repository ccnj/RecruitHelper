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

func (c sourcingActorClock) Now() time.Time { return c.now }

type sourcingActorHands struct{}

func (sourcingActorHands) State(context.Context, string) (patrol.HandState, error) {
	return patrol.HandState{Online: true, Session: "session-sourcing-actor", BootID: "boot-sourcing-actor"}, nil
}

type sourcingActorAdvice struct {
	requests []m5ai.CompletionRequest
}

func (*sourcingActorAdvice) ProviderName() string { return "fixture-provider" }
func (*sourcingActorAdvice) ModelName() string    { return "fixture-model" }

func (a *sourcingActorAdvice) CompleteJSON(
	_ context.Context,
	request m5ai.CompletionRequest,
) (m5ai.CompletionResponse, error) {
	a.requests = append(a.requests, request)
	zero := 0
	return m5ai.CompletionResponse{
		JSONText: `{"评分":7,"说明":"discard"}`,
		Usage: m5ai.CompletionUsage{
			InputTokens: 4, OutputTokens: 2, ReasoningTokens: &zero,
		},
		ReasoningContentEmpty: true,
	}, nil
}

type sourcingActorSender struct {
	dispatcher *dispatch.Dispatcher
	order      []string
	data       protocol.CandidateReadSourcingResumeData
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
			ContentScriptOk: true, LoginState: protocol.LoginStateIn, PageKind: protocol.PageKindIm,
			PrincipalFingerprint: &fingerprint, Surface: &protocol.PlatformSurface{ImListVisible: true},
		}
	case protocol.PrimCandidateReadSourcingResume:
		data = s.data
	case protocol.PrimChatReadList:
		data = protocol.ChatReadListData{Sessions: []protocol.ConversationSummary{}, Complete: true}
	default:
		return fmt.Errorf("unexpected primitive %s", body.Name)
	}
	dataRaw, err := protocol.Encode(data)
	if err != nil {
		return err
	}
	s.dispatcher.OnAck(handID, protocol.AckBody{Ref: env.MsgID, Status: protocol.AckStatusAccepted})
	s.dispatcher.OnResult(handID, "result-"+env.MsgID, protocol.ResultBody{
		Ref: env.MsgID, Status: protocol.ResultStatusOk, Data: dataRaw, ExecMs: 1,
	})
	return nil
}

func (*sourcingActorSender) HandSession(string) (string, string, bool) {
	return "session-sourcing-actor", "boot-sourcing-actor", true
}

func (*sourcingActorSender) HandNegotiation(string) ([]string, []string, bool) {
	return []string{
			protocol.PrimProbePlatform + "@1",
			protocol.PrimCandidateReadSourcingResume + "@1",
			protocol.PrimChatReadList + "@1",
		}, []string{
			string(protocol.FeatureLease1), string(protocol.FeatureProgress1), string(protocol.FeatureCancel1),
		}, true
}

func (*sourcingActorSender) HandContractMatch(string) (bool, bool) { return true, true }
func (*sourcingActorSender) CloseHand(string, string, string) bool { return true }
func (*sourcingActorSender) HandOfflineMs(string) int64            { return 0 }

func sourcingActorRevision(at time.Time) m5ai.ContextRevision {
	replyPrompt, intentPrompt, facts := "reply", "intent", "facts"
	documents := []m5ai.JobConfigDocument{
		{DocType: "候选人筛选", Content: `{"minScore":5}`},
		{DocType: "多轮沟通", Content: replyPrompt},
		{DocType: "客户事实库", Content: facts},
		{DocType: "意向判断", Content: intentPrompt},
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
			ReplyPrompt: replyPrompt, IntentPrompt: intentPrompt,
			CustomerFacts: facts, MappingVersion: m5ai.MappingVersion,
		},
		CreatedAt: at,
	}
}

func TestSourcingActorCapturesOneThenContinuesNormalRound(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
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
		"session-sourcing-actor", "boot-sourcing-actor", now.Add(-time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	displayName, positionTitle := "actor-sensitive-name", "actor-sensitive-position"
	sender := &sourcingActorSender{data: protocol.CandidateReadSourcingResumeData{
		PlatformUserRef: "actor-sensitive-user-ref", DisplayName: &displayName,
		PositionRef: "actor-position-ref", PositionTitle: &positionTitle,
		ContactState: protocol.CandidateContactStateUnestablished, ObservedAt: now.UnixMilli(),
		Basic: []protocol.CandidateResumeLabelValue{}, Expectations: []protocol.CandidateResumeLabelValue{},
		SelfEvaluation: "", Education: "", WorkExperiences: "",
	}}
	d := dispatch.New(st, sender)
	sender.dispatcher = d
	advice := &sourcingActorAdvice{}
	manager, err := patrol.NewManager(st, PatrolRunner{Dispatcher: d}, sourcingActorHands{}, patrol.Config{
		Clock: sourcingActorClock{now: now}, Location: time.UTC,
		IdentityFreshFor: time.Minute, PatrolInterval: 5 * time.Minute,
		MinimumRoundGap: time.Millisecond, NewRoundID: func() string { return "round-sourcing-actor" },
	}, advice)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.StartSourcing(key, revision.RevisionHash); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("采集轮次失败: result=%+v err=%v", result, err)
	}
	wantOrder := []string{
		protocol.PrimProbePlatform,
		protocol.PrimCandidateReadSourcingResume,
		protocol.PrimChatReadList,
	}
	if fmt.Sprint(sender.order) != fmt.Sprint(wantOrder) {
		t.Fatalf("采集必须位于 probe 后、readList 前且每轮一次: got=%v want=%v", sender.order, wantOrder)
	}
	status, err := st.AccountSourcingStatus(key)
	if err != nil || status == nil || status.CaptureCount != 1 || status.Latest == nil {
		t.Fatalf("actor 未落唯一采集事实: status=%+v err=%v", status, err)
	}
	if len(advice.requests) != 1 || advice.requests[0].Purpose != m5ai.PurposeScoring ||
		advice.requests[0].MaxOutputTokens != m5ai.ScoringOutputTokenLimit {
		t.Fatalf("每条新采集事实必须恰好一次评分调用: requests=%+v", advice.requests)
	}
	score, err := st.SourcingScoreByRunID(status.Latest.RunID)
	if err != nil || score == nil || score.Status != store.AIInvocationOK || score.Score == nil || *score.Score != 7 {
		t.Fatalf("评分未与采集 run 唯一绑定: score=%+v err=%v", score, err)
	}
}
