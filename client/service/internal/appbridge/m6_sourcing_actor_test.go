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
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

type sourcingActorClock struct{ now time.Time }

func (c *sourcingActorClock) Now() time.Time { return c.now }
func (c *sourcingActorClock) Add(delta time.Duration) {
	c.now = c.now.Add(delta)
}

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
	dispatcher         *dispatch.Dispatcher
	order              []string
	candidates         []protocol.CandidateReadSourcingResumeData
	sourcingExclusions [][]string
	greetings          []string
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
	var evidence []protocol.Evidence
	switch body.Name {
	case protocol.PrimProbePlatform:
		fingerprint := "principal-sourcing-actor"
		data = protocol.ProbePlatformData{
			ContentScriptOk: true, LoginState: protocol.LoginStateIn, PageKind: protocol.PageKindIm,
			PrincipalFingerprint: &fingerprint, Surface: &protocol.PlatformSurface{ImListVisible: true},
		}
	case protocol.PrimCandidateReadSourcingResume:
		var args protocol.CandidateReadSourcingResumeArgs
		if err := json.Unmarshal(body.Args, &args); err != nil {
			return err
		}
		s.sourcingExclusions = append(s.sourcingExclusions, append([]string(nil), args.ExcludePlatformUserRefs...))
		excluded := make(map[string]struct{}, len(args.ExcludePlatformUserRefs))
		for _, ref := range args.ExcludePlatformUserRefs {
			excluded[ref] = struct{}{}
		}
		for _, candidate := range s.candidates {
			if _, found := excluded[candidate.PlatformUserRef]; !found {
				data = candidate
				break
			}
		}
		if data == nil {
			return fmt.Errorf("fixture 没有未排除候选人")
		}
	case protocol.PrimChatSendGreeting:
		var args protocol.ChatSendGreetingArgs
		if err := json.Unmarshal(body.Args, &args); err != nil {
			return err
		}
		s.greetings = append(s.greetings, args.Text)
		data = protocol.ChatSendGreetingData{
			PlatformUserRef: args.PlatformUserRef, PositionRef: args.PositionRef,
			ConversationRef: "conversation-sourcing-actor",
			ContentHash:     syncledger.HashText(args.Text), ObservedAt: time.Now().UnixMilli(),
		}
		evidence = []protocol.Evidence{{Type: string(protocol.SendGreetingEvidenceTypeOutboundGreetingObserved)}}
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
		Ref: env.MsgID, Status: protocol.ResultStatusOk, Data: dataRaw, ExecMs: 1, Evidence: evidence,
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
			protocol.PrimChatSendGreeting + "@1",
			protocol.PrimChatReadList + "@1",
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

func TestSourcingActorCapturesOnlyAcrossConsecutiveRounds(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	clock := &sourcingActorClock{now: now}
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
	positionTitle := "actor-sensitive-position"
	candidates := make([]protocol.CandidateReadSourcingResumeData, 3)
	for i := range candidates {
		displayName := fmt.Sprintf("actor-sensitive-name-%d", i+1)
		candidates[i] = protocol.CandidateReadSourcingResumeData{
			PlatformUserRef: fmt.Sprintf("actor-sensitive-user-ref-%d", i+1), DisplayName: &displayName,
			PositionRef: "actor-position-ref", PositionTitle: &positionTitle,
			ContactState: protocol.CandidateContactStateUnestablished, ObservedAt: now.Add(time.Duration(i) * time.Minute).UnixMilli(),
			Basic: []protocol.CandidateResumeLabelValue{}, Expectations: []protocol.CandidateResumeLabelValue{},
			SelfEvaluation: "", Education: "", WorkExperiences: "",
		}
	}
	sender := &sourcingActorSender{candidates: candidates}
	d := dispatch.New(st, sender)
	sender.dispatcher = d
	advice := &sourcingActorAdvice{}
	roundNumber := 0
	manager, err := patrol.NewManager(st, PatrolRunner{Dispatcher: d}, sourcingActorHands{}, patrol.Config{
		Clock: clock, Location: time.UTC,
		IdentityFreshFor: time.Hour, PatrolInterval: 5 * time.Minute,
		MinimumRoundGap: time.Millisecond, NewRoundID: func() string {
			roundNumber++
			return fmt.Sprintf("round-sourcing-actor-%d", roundNumber)
		},
	}, advice)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.StartSourcing(key, revision.RevisionHash); err != nil {
		t.Fatal(err)
	}
	runIDs := make([]string, 0, len(candidates))
	for index := range candidates {
		if index > 0 {
			clock.Add(5*time.Minute + time.Second)
		}
		result, tickErr := manager.Tick(context.Background())
		if tickErr != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
			t.Fatalf("第 %d 个采集轮次失败: result=%+v err=%v", index+1, result, tickErr)
		}
		status, statusErr := st.AccountSourcingStatus(key)
		if statusErr != nil || status == nil || status.CaptureCount != int64(index+1) || status.Latest == nil {
			t.Fatalf("第 %d 轮未新增唯一采集事实: status=%+v err=%v", index+1, status, statusErr)
		}
		runIDs = append(runIDs, status.Latest.RunID)
	}

	if len(advice.requests) != 0 {
		t.Fatalf("capture-only 阶段不得调用 provider: requests=%+v", advice.requests)
	}
	if len(sender.greetings) != 0 {
		t.Fatalf("capture-only 阶段不得调用 chat.sendGreeting: greetings=%v", sender.greetings)
	}
	readCount, greetingCount := 0, 0
	for _, name := range sender.order {
		switch name {
		case protocol.PrimCandidateReadSourcingResume:
			readCount++
		case protocol.PrimChatSendGreeting:
			greetingCount++
		}
	}
	if readCount != len(candidates) || greetingCount != 0 {
		t.Fatalf("连续采集轮命令面错误: order=%v", sender.order)
	}
	if len(sender.sourcingExclusions) != len(candidates) {
		t.Fatalf("每轮必须携带已采身份排除集: exclusions=%v", sender.sourcingExclusions)
	}
	for index, exclusions := range sender.sourcingExclusions {
		seen := make(map[string]bool, len(exclusions))
		for _, ref := range exclusions {
			seen[ref] = true
		}
		for previous := 0; previous < index; previous++ {
			if !seen[candidates[previous].PlatformUserRef] {
				t.Fatalf("第 %d 轮缺少已采身份 %q: exclusions=%v", index+1, candidates[previous].PlatformUserRef, exclusions)
			}
		}
	}
	for index, runID := range runIDs {
		score, scoreErr := st.SourcingScoreByRunID(runID)
		if scoreErr != nil || score != nil {
			t.Fatalf("第 %d 条采集事实不得形成 score: score=%+v err=%v", index+1, score, scoreErr)
		}
		selection, selectionErr := st.SourcingSelectionByRunID(runID)
		if selectionErr != nil || selection != nil {
			t.Fatalf("第 %d 条采集事实不得形成 selection: selection=%+v err=%v", index+1, selection, selectionErr)
		}
		profile, profileErr := st.CandidateProfileByScope(store.CandidateProfileScope{
			Platform: key.Platform, AccountRef: key.AccountRef,
			PlatformUserRef: candidates[index].PlatformUserRef, PositionRef: candidates[index].PositionRef,
		})
		if profileErr != nil || profile != nil {
			t.Fatalf("第 %d 条采集事实不得形成 profile/effect intent: profile=%+v err=%v", index+1, profile, profileErr)
		}
	}
}
