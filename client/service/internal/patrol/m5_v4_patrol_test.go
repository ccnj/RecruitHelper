package patrol

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

type communicationV4PatrolFixture struct {
	profileID       string
	conversationRef string
	inboundSeq      int64
}

func beginCommunicationV4PatrolRound(t *testing.T, h *harness, roundID string) {
	t.Helper()
	if err := h.db.BeginPatrolRound(&store.PatrolRound{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		RoundID: roundID, Trigger: TriggerDirty, Status: "running",
		Stage: "readingThread", StartedAt: h.clock.Now(),
	}, h.clock.Now().Add(h.config.PatrolInterval)); err != nil {
		t.Fatal(err)
	}
}

func seedCommunicationV4PatrolTarget(
	t *testing.T,
	h *harness,
	suffix string,
	inboundText string,
) communicationV4PatrolFixture {
	t.Helper()
	return seedCommunicationV4PatrolTargetWithBoundary(t, h, suffix, []store.MessageDraft{{
		Direction: "in", Kind: "text", ContentHash: syncledger.HashText(inboundText),
		Text: &inboundText, Origin: "external",
	}})
}

func seedCommunicationV4PatrolTargetWithBoundary(
	t *testing.T,
	h *harness,
	suffix string,
	boundary []store.MessageDraft,
) communicationV4PatrolFixture {
	t.Helper()
	now := h.clock.Now()
	profileID := "profile-v4-patrol-" + suffix
	platformUserRef := "person-v4-patrol-" + suffix
	positionRef := "position-v4-patrol-" + suffix
	conversationRef := "conversation-v4-patrol-" + suffix
	displayName, positionTitle := "合成候选人-"+suffix, "合成职位-"+suffix
	if _, err := h.db.SelectCandidateProfile(store.SelectCandidateProfileRequest{
		ProfileID: profileID,
		Scope: store.CandidateProfileScope{
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			PlatformUserRef: platformUserRef, PositionRef: positionRef,
		},
		DisplayName: &displayName, PositionTitle: &positionTitle, ObservedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	greetingIntent := "intent-v4-patrol-greeting-" + suffix
	greetingText := "合成招呼-" + suffix
	greetingHash := syncledger.HashText(greetingText)
	deadline := now.Add(time.Hour).UnixMilli()
	greeting, err := h.db.CreateGreetingEffectIntentAndCmd(store.CreateGreetingEffectIntentRequest{
		Intent: store.EffectIntent{
			IntentID: greetingIntent, IdemKey: "idem-v4-patrol-greeting-" + suffix,
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			Primitive: protocol.PrimChatSendGreeting, TargetRef: profileID,
			PayloadHash: "payload-v4-patrol-" + suffix, GuardsHash: "guards-v4-patrol-" + suffix,
			SendFingerprint: greetingHash, Status: store.EffectIntentDispatching, DeadlineMs: deadline,
		},
		Command: store.CmdRecord{
			MsgID: "msg-v4-patrol-greeting-" + suffix,
			Name:  protocol.PrimChatSendGreeting, Class: string(protocol.ClassEffectful),
			IdemKey:  "idem-v4-patrol-greeting-" + suffix,
			Domain:   h.key.Platform + ":" + h.key.AccountRef,
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			ExpectedPrincipalFingerprint: "principal-1", IntentID: greetingIntent,
			HandID: "hand-1", Session: "session-1", BootIDAtDispatch: "boot-1",
			Status: store.CmdQueued, DeadlineMs: deadline, ExecBudgetMs: 60_000,
		},
		Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.db.MoveEffectToVerification(
		greeting.Command.MsgID,
		"fixturePositiveRead",
		now,
	); err != nil {
		t.Fatal(err)
	}
	greetingMessage, err := h.db.ResolveGreetingVerified(store.VerifiedGreetingSuccess{
		Ref: greeting.Command.MsgID, ProfileID: profileID,
		PlatformUserRef: platformUserRef, PositionRef: positionRef,
		ConversationRef: conversationRef, Text: greetingText,
		ContentHash: greetingHash, ObservedAtMs: now.UnixMilli(),
		ResolutionReason: "fixturePositiveRead", At: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	replyPrompt := "只返回 JSON 对象。" +
		"输出格式必须是 {\"话术_序列\":[\"一条简短自然的招聘沟通回复\"],\"动作\":\"无\"}。\n" +
		"简历={简历}\n时段={推荐时段}\n历史={对话历史}"
	intentPrompt := "只返回 JSON 对象。" +
		"输出 {\"信号\":\"有意向\",\"理由\":\"合成验收\"}。\n" +
		"回复={回复}\n招呼={招呼语}"
	documents := []m5ai.JobConfigDocument{
		{DocType: "多轮沟通", Content: replyPrompt},
		{DocType: "意向判断", Content: intentPrompt},
		{DocType: "客户事实库", Content: ""},
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].DocType < documents[j].DocType })
	revision := m5ai.ContextRevision{
		ContextID:    "context-v4-patrol-" + suffix,
		RevisionHash: "revision-v4-patrol-" + suffix,
		SourceKind:   "localImport", DisplayName: "合成职位上下文-" + suffix,
		SourcePackage: m5ai.JobConfigDocumentPackage{Documents: documents},
		Communication: m5ai.CommunicationView{
			ReplyPrompt: replyPrompt, IntentPrompt: intentPrompt,
			MappingVersion: m5ai.MappingVersion,
		},
		CreatedAt: now,
	}
	if _, _, err := h.db.SaveJobAIContextRevision(revision); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.SelectM5TrialProfile(
		profileID,
		"trial-v4-patrol-"+suffix,
		"user",
		now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.BindActiveM5TrialProfileAIContext(store.BindProfileAIContextRequest{
		BindingID: "binding-v4-patrol-" + suffix, ProfileID: profileID,
		ContextID: revision.ContextID, RevisionHash: revision.RevisionHash,
		Reason: "fixture", BoundBy: "user", BoundAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	argsRaw, err := protocol.Encode(protocol.CandidateReadResumeArgs{
		ConversationRef: conversationRef, PlatformUserRef: platformUserRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	contextRaw, err := protocol.Encode(protocol.CmdContext{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ExpectedPrincipalFingerprint: "principal-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	capture, err := h.db.CreateResumeCaptureCmd(store.CreateResumeCaptureCmdRequest{
		ProfileID: profileID,
		Command: store.CmdRecord{
			MsgID: "msg-v4-patrol-resume-" + suffix,
			Name:  protocol.PrimCandidateReadResume, Class: string(protocol.ClassIntrusive),
			Domain:   h.key.Platform + ":" + h.key.AccountRef,
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			ExpectedPrincipalFingerprint: "principal-1",
			ContextJSON:                  string(contextRaw), Args: string(argsRaw),
			HandID: "hand-1", Session: "session-1", BootIDAtDispatch: "boot-1",
			Status: store.CmdQueued, DeadlineMs: deadline, ExecBudgetMs: 60_000,
		},
		Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	resumeData := protocol.CandidateReadResumeData{
		ConversationRef: conversationRef, PlatformUserRef: platformUserRef,
		ObservedAt: now.UnixMilli(),
		Basic:      []protocol.CandidateResumeLabelValue{{Label: "学历", Value: "本科"}},
		Expectations: []protocol.CandidateResumeLabelValue{
			{Label: "职位", Value: "合成职位"},
		},
		SelfEvaluation: "合成自评", Education: "合成教育", WorkExperiences: "合成经历",
	}
	resumeRaw, err := protocol.Encode(resumeData)
	if err != nil {
		t.Fatal(err)
	}
	resultRaw, err := protocol.Encode(protocol.ResultBody{
		Ref: capture.Command.MsgID, Status: protocol.ResultStatusOk,
		Data: resumeRaw, ExecMs: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.db.MutateCmd(capture.Command.MsgID, func(command *store.CmdRecord) error {
		command.Status = store.CmdOk
		command.ResultBody = string(resultRaw)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.CompleteResumeCapture(store.CompleteResumeCaptureRequest{
		ProfileID: profileID, LogicalDispatchID: capture.Command.LogicalDispatchID,
		SnapshotID: "snapshot-v4-patrol-" + suffix, Data: resumeData,
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.db.MarkActiveM5TrialManualRequired(
		profileID,
		"fixturePreparedForV4",
		now,
	); err != nil {
		t.Fatal(err)
	}
	if root, _, err := h.db.EnsureCommunicationV4RootForGreetedProfile(
		profileID,
		now,
	); err != nil || root == nil || root.ProfileID != profileID {
		t.Fatalf("V4 根不可用: root=%+v err=%v", root, err)
	}
	changes, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: store.ConversationKey{
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			ConversationRef: conversationRef,
		},
		ExpectedTailSeq: greetingMessage.Seq,
		PlatformUserRef: platformUserRef,
		NewMessages:     boundary,
		SyncedAt:        now,
	})
	if err != nil || len(changes.Inserted) != len(boundary) {
		t.Fatalf("追加 V4 入站失败: changes=%+v err=%v", changes, err)
	}
	return communicationV4PatrolFixture{
		profileID: profileID, conversationRef: conversationRef,
		inboundSeq: changes.Inserted[len(changes.Inserted)-1].Seq,
	}
}

func TestCommunicationV4PatrolAdvancesMultipleProfilesAndNextRoundWithoutGrowth(
	t *testing.T,
) {
	h := newHarness(t)
	first := seedCommunicationV4PatrolTarget(t, h, "a", "我想了解一下岗位")
	second := seedCommunicationV4PatrolTarget(t, h, "b", "这个岗位还在招吗")
	if active, err := h.db.ActiveM5TrialForAccount(h.key); err != nil || active != nil {
		t.Fatalf("V4 多档案测试不得留下 active trial: active=%+v err=%v", active, err)
	}

	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			switch request.Purpose {
			case m5ai.PurposeIntent:
				return safeFakeResponse(`{"信号":"有意向","理由":"fixture"}`), nil
			case m5ai.PurposeReply:
				return safeFakeResponse(`{"话术_序列":["合成多档案回复"],"动作":"忽略"}`), nil
			default:
				return m5ai.CompletionResponse{}, fmt.Errorf("未知建议用途 %q", request.Purpose)
			}
		},
	}
	hand := &m5PositiveHand{}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
	manager, err := NewManager(h.db, runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("账号读取失败: account=%+v err=%v", account, err)
	}
	roundID := "round-v4-multi-profile"
	beginCommunicationV4PatrolRound(t, h, roundID)
	actor := &roundActor{
		manager: manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: roundID, now: h.clock.Now(),
	}

	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(advice.requests) != 4 || hand.commandCount() != 2 {
		t.Fatalf("双档案首轮必须各两次建议、各一次发送: advice=%d sends=%d",
			len(advice.requests), hand.commandCount())
	}
	for _, fixture := range []communicationV4PatrolFixture{first, second} {
		turn, err := h.db.LatestDialogueTurnForProfile(fixture.profileID)
		aggregate, aggregateErr := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
		if err != nil || turn == nil || turn.Status != store.DialogueTurnCompleted ||
			aggregateErr != nil || aggregate.ProjectedThroughSeq != fixture.inboundSeq+1 {
			t.Fatalf("档案未完成首轮: fixture=%+v turn=%+v aggregate=%+v err=%v aggregateErr=%v",
				fixture, turn, aggregate, err, aggregateErr)
		}
	}

	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: first.conversationRef,
	}
	messages, err := h.db.MessagesForConversation(key)
	if err != nil {
		t.Fatal(err)
	}
	secondInboundText := "我再问一个问题"
	changes, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: messages[len(messages)-1].Seq,
		PlatformUserRef: "",
		NewMessages: []store.MessageDraft{{
			Direction: "in", Kind: "text",
			ContentHash: syncledger.HashText(secondInboundText),
			Text:        &secondInboundText, Origin: "external",
		}},
		SyncedAt: h.clock.Now().Add(time.Minute),
	})
	if err != nil || len(changes.Inserted) != 1 {
		t.Fatalf("追加第二轮失败: changes=%+v err=%v", changes, err)
	}
	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(advice.requests) != 6 || hand.commandCount() != 3 {
		t.Fatalf("第二轮只应推进一个档案: advice=%d sends=%d",
			len(advice.requests), hand.commandCount())
	}

	restartedDispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(restartedDispatcher)
	restartedRunner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: restartedDispatcher}
	restarted, err := NewManager(h.db, restartedRunner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	restartedActor := &roundActor{
		manager: restarted, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: roundID, now: h.clock.Now(),
	}
	for attempt := 0; attempt < 2; attempt++ {
		restarted.mu.Lock()
		err = restartedActor.processCommunicationV4Targets(context.Background())
		restarted.mu.Unlock()
		if err != nil {
			t.Fatalf("重启后重复巡检失败: attempt=%d err=%v", attempt+1, err)
		}
	}
	if len(advice.requests) != 6 || hand.commandCount() != 3 {
		t.Fatalf("重启/重复巡检发生增生: advice=%d sends=%d",
			len(advice.requests), hand.commandCount())
	}
}

func TestCommunicationV4PatrolConsumesWechatAcceptedMessageWithoutAIOrReplayGrowth(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PatrolTargetWithBoundary(t, h, "wechat-accepted", []store.MessageDraft{{
		Direction: "in", Kind: "card", CardType: "wechatExchange", CardState: "accepted",
		ContentHash: syncledger.HashText("wechat-exchanged-fixture"), Origin: "external",
	}})
	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			return m5ai.CompletionResponse{}, fmt.Errorf("换微信成功事实不得调用 AI: %s", request.Purpose)
		},
	}
	manager, err := NewManager(h.db, h.runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("账号读取失败: account=%+v err=%v", account, err)
	}
	roundID := "round-v4-wechat-accepted"
	beginCommunicationV4PatrolRound(t, h, roundID)
	actor := &roundActor{
		manager: manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: roundID, now: h.clock.Now(),
	}

	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(advice.requests) != 0 {
		t.Fatalf("换微信成功事实触发了 AI: %+v", advice.requests)
	}
	turn, err := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	aggregate, aggregateErr := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnCompleted ||
		aggregateErr != nil || aggregate.State.WechatState != communication.V4WechatExchanged ||
		aggregate.ProjectedThroughSeq != fixture.inboundSeq {
		t.Fatalf("换微信成功消息未收敛: turn=%+v aggregate=%+v err=%v aggregateErr=%v",
			turn, aggregate, err, aggregateErr)
	}
	revision := aggregate.Revision

	for attempt := 0; attempt < 2; attempt++ {
		manager.mu.Lock()
		err = actor.processCommunicationV4Targets(context.Background())
		manager.mu.Unlock()
		if err != nil {
			t.Fatalf("重复巡检失败: attempt=%d err=%v", attempt+1, err)
		}
	}
	replayed, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || replayed.Revision != revision || len(advice.requests) != 0 {
		t.Fatalf("重复巡检发生状态或 AI 增生: aggregate=%+v advice=%+v err=%v",
			replayed, advice.requests, err)
	}
}

func TestCommunicationV4PatrolIgnoresSystemRowsAroundCandidateInput(t *testing.T) {
	h := newHarness(t)
	before, after := "合成系统前置", "合成系统尾部"
	text := "我想进一步了解岗位"
	textTarget := seedCommunicationV4PatrolTargetWithBoundary(t, h, "system-text", []store.MessageDraft{
		{Direction: "system", Kind: "system", ContentHash: syncledger.HashText(before), Text: &before, Origin: "external"},
		{Direction: "in", Kind: "text", ContentHash: syncledger.HashText(text), Text: &text, Origin: "external"},
		{Direction: "system", Kind: "system", ContentHash: syncledger.HashText(after), Text: &after, Origin: "external"},
	})
	resumeTarget := seedCommunicationV4PatrolTargetWithBoundary(t, h, "system-resume", []store.MessageDraft{
		{Direction: "system", Kind: "system", ContentHash: syncledger.HashText(before + "-resume"), Text: &before, Origin: "external"},
		{
			Direction: "in", Kind: "card", CardType: "resumeAttachment", CardState: "unknown",
			ContentHash: syncledger.HashText("resume-attachment"), Origin: "external",
		},
		{Direction: "system", Kind: "system", ContentHash: syncledger.HashText(after + "-resume"), Text: &after, Origin: "external"},
	})

	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			switch request.Purpose {
			case m5ai.PurposeIntent:
				return safeFakeResponse(`{"信号":"有意向","理由":"fixture"}`), nil
			case m5ai.PurposeReply:
				return safeFakeResponse(`{"话术_序列":["合成系统边界回复"],"动作":"忽略"}`), nil
			default:
				return m5ai.CompletionResponse{}, fmt.Errorf("未知建议用途 %q", request.Purpose)
			}
		},
	}
	hand := &m5PositiveHand{}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
	manager, err := NewManager(h.db, runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("账号读取失败: account=%+v err=%v", account, err)
	}
	roundID := "round-v4-system-boundary"
	beginCommunicationV4PatrolRound(t, h, roundID)
	actor := &roundActor{
		manager: manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: roundID, now: h.clock.Now(),
	}
	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	intentCalls := 0
	replyCalls := 0
	for index := range advice.requests {
		switch advice.requests[index].Purpose {
		case m5ai.PurposeIntent:
			intentCalls++
		case m5ai.PurposeReply:
			replyCalls++
		}
	}
	if intentCalls != 1 || replyCalls != 2 || hand.commandCount() != 2 {
		t.Fatalf("系统行不得改变文本/投简历语义: intent=%d reply=%d sends=%d",
			intentCalls, replyCalls, hand.commandCount())
	}
	for _, fixture := range []communicationV4PatrolFixture{textTarget, resumeTarget} {
		turn, turnErr := h.db.LatestDialogueTurnForProfile(fixture.profileID)
		aggregate, aggregateErr := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
		if turnErr != nil || turn == nil || turn.Status != store.DialogueTurnCompleted ||
			aggregateErr != nil || aggregate.ProjectedThroughSeq != fixture.inboundSeq+1 {
			t.Fatalf("系统边界档案未完成: fixture=%+v turn=%+v aggregate=%+v turnErr=%v aggregateErr=%v",
				fixture, turn, aggregate, turnErr, aggregateErr)
		}
	}
}

func TestCommunicationV4PatrolGlobalStopStopsLaterProfiles(t *testing.T) {
	for _, tc := range []struct {
		name          string
		cancelContext bool
	}{
		{name: "account-pause"},
		{name: "context-cancel", cancelContext: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testCommunicationV4PatrolGlobalStop(t, tc.cancelContext)
		})
	}
}

func TestCommunicationV4PatrolServiceReplySkipsIntentAndUsesServicePolicy(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PatrolTarget(t, h, "service", "我想先了解一下岗位")

	advice := &recordingAdviceExecutor{
		complete: func(call int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			switch call {
			case 1:
				if request.Purpose != m5ai.PurposeIntent {
					return m5ai.CompletionResponse{}, fmt.Errorf("首轮意向用途错误: %q", request.Purpose)
				}
				return safeFakeResponse(`{"信号":"有意向","理由":"fixture"}`), nil
			case 2:
				if request.Purpose != m5ai.PurposeReply {
					return m5ai.CompletionResponse{}, fmt.Errorf("首轮回复用途错误: %q", request.Purpose)
				}
				return safeFakeResponse(`{"话术_序列":["可以的，我们继续聊聊岗位细节"],"动作":"忽略"}`), nil
			case 3:
				if request.Purpose != m5ai.PurposeReply {
					return m5ai.CompletionResponse{}, fmt.Errorf("服务态不得调用 %q", request.Purpose)
				}
				if !strings.Contains(request.UserContent, "候选人已经接受面试") ||
					!strings.Contains(request.UserContent, "不得承诺“帮您反馈”“我去问下”") {
					return m5ai.CompletionResponse{}, errors.New("服务态规则未进入 provider 请求")
				}
				return safeFakeResponse(`{"话术_序列":["面试地址以邀约信息为准，有其他细节我们微信上聊哈"],"动作":"忽略"}`), nil
			default:
				return m5ai.CompletionResponse{}, fmt.Errorf("发生未授权的第 %d 次建议调用", call)
			}
		},
	}
	hand := &m5PositiveHand{}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
	manager, err := NewManager(h.db, runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("账号读取失败: account=%+v err=%v", account, err)
	}
	roundID := "round-v4-service"
	beginCommunicationV4PatrolRound(t, h, roundID)
	actor := &roundActor{
		manager: manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: roundID, now: h.clock.Now(),
	}
	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(advice.requests) != 2 || hand.commandCount() != 1 {
		t.Fatalf("进入服务态前的普通轮次未完成: advice=%+v sends=%d",
			advice.requests, hand.commandCount())
	}

	acceptedAt := h.clock.Now().Add(time.Minute)
	accepted, err := h.db.ApplyCommunicationV4BusinessEvent(store.ApplyCommunicationV4BusinessEventRequest{
		ProfileID: fixture.profileID,
		Event: communication.BusinessEvent{
			Key: "card:99:pending:accepted", Kind: communication.EventInterviewAccepted,
			Source: communication.EventSourceCardTransition, MessageSeq: 99,
			OccurredAt: &acceptedAt,
		},
		AppliedAt: acceptedAt,
	})
	if err != nil || accepted.Aggregate.State.MainStatus != communication.V4StatusInterviewed {
		t.Fatalf("服务态前置事实失败: result=%+v err=%v", accepted, err)
	}
	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: fixture.conversationRef,
	}
	messages, err := h.db.MessagesForConversation(key)
	if err != nil || len(messages) == 0 {
		t.Fatalf("服务态前置账本不可用: messages=%+v err=%v", messages, err)
	}
	serviceText := "面试地址在哪里"
	changes, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: messages[len(messages)-1].Seq,
		NewMessages: []store.MessageDraft{{
			Direction: "in", Kind: "text", ContentHash: syncledger.HashText(serviceText),
			Text: &serviceText, Origin: "external",
		}},
		SyncedAt: acceptedAt.Add(time.Minute),
	})
	if err != nil || len(changes.Inserted) != 1 {
		t.Fatalf("追加服务态入站失败: changes=%+v err=%v", changes, err)
	}
	serviceInboundSeq := changes.Inserted[0].Seq

	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(advice.requests) != 3 || advice.requests[2].Purpose != m5ai.PurposeReply ||
		hand.commandCount() != 2 {
		t.Fatalf("服务态必须新增一次 reply、零 intent、一次发送: advice=%+v sends=%d",
			advice.requests, hand.commandCount())
	}
	turn, err := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnCompleted {
		t.Fatalf("服务态轮次未完成: turn=%+v err=%v", turn, err)
	}
	aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || aggregate.State.MainStatus != communication.V4StatusInterviewed ||
		aggregate.ProjectedThroughSeq != serviceInboundSeq+1 {
		t.Fatalf("服务态发送错误推进主状态或游标: aggregate=%+v err=%v", aggregate, err)
	}
}

func testCommunicationV4PatrolGlobalStop(t *testing.T, cancelContext bool) {
	h := newHarness(t)
	first := seedCommunicationV4PatrolTarget(t, h, "pause-a", "我想了解岗位")
	second := seedCommunicationV4PatrolTarget(t, h, "pause-b", "岗位还在招聘吗")
	runContext := context.Background()
	cancel := func() {}
	if cancelContext {
		runContext, cancel = context.WithCancel(runContext)
	}

	var manager *Manager
	advice := &recordingAdviceExecutor{
		complete: func(call int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			switch request.Purpose {
			case m5ai.PurposeIntent:
				return safeFakeResponse(`{"信号":"有意向","理由":"fixture"}`), nil
			case m5ai.PurposeReply:
				if call == 2 {
					if cancelContext {
						cancel()
					} else {
						if err := manager.PauseNow(h.key); err != nil {
							return m5ai.CompletionResponse{}, err
						}
					}
				}
				return safeFakeResponse(`{"话术_序列":["暂停前已生成的回复"],"动作":"忽略"}`), nil
			default:
				return m5ai.CompletionResponse{}, fmt.Errorf("未知建议用途 %q", request.Purpose)
			}
		},
	}
	hand := &m5PositiveHand{}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
	var err error
	manager, err = NewManager(h.db, runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("账号读取失败: account=%+v err=%v", account, err)
	}
	roundID := "round-v4-global-pause"
	beginCommunicationV4PatrolRound(t, h, roundID)
	actor := &roundActor{
		manager: manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: roundID, now: h.clock.Now(),
	}
	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(runContext)
	manager.mu.Unlock()
	expected := ErrActorPaused
	if cancelContext {
		expected = context.Canceled
	}
	if !errors.Is(err, expected) {
		t.Fatalf("全局停止必须终止本轮: want=%v got=%v", expected, err)
	}
	if hand.commandCount() != 0 || len(advice.requests) != 2 {
		t.Fatalf("暂停后不得派发或处理第二档案: advice=%d sends=%d",
			len(advice.requests), hand.commandCount())
	}
	firstTurn, err := h.db.LatestDialogueTurnForProfile(first.profileID)
	if err != nil || firstTurn == nil {
		t.Fatalf("首档案轮次缺失: turn=%+v err=%v", firstTurn, err)
	}
	firstAction, err := h.db.CommunicationActionByTurn(firstTurn.TurnID)
	if err != nil || firstAction == nil || firstAction.Status != store.CommunicationActionManualRequired {
		t.Fatalf("首档案未安全终结计划动作: action=%+v err=%v", firstAction, err)
	}
	secondTurn, err := h.db.LatestDialogueTurnForProfile(second.profileID)
	if err != nil || secondTurn != nil {
		t.Fatalf("后续档案不得生成轮次: turn=%+v err=%v", secondTurn, err)
	}
	secondAggregate, err := h.db.CommunicationV4AggregateByProfile(second.profileID)
	if err != nil || secondAggregate.AutomationStatus != store.ProfileCommunicationAutomationActive {
		t.Fatalf("后续档案必须保持可运行: aggregate=%+v err=%v", secondAggregate, err)
	}
}
