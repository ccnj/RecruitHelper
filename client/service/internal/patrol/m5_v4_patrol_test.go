package patrol

import (
	"context"
	"encoding/json"
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
	"recruithelper/client/service/internal/workflow"
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
	return seedCommunicationV4PatrolTargetWithBoundaryAndFixedPhrases(
		t,
		h,
		suffix,
		boundary,
		`{
			"rejectWechat":{"enabled":true,"messages":["合成挽留"]},
			"silence48Wechat":{"enabled":true,"messages":["合成冷催"]},
			"wechatAccepted":{"enabled":true,"messages":["好的，晚点加你"]},
			"meetingAccepted":{"enabled":true,"messages":["好的，面试安排已确认"]}
		}`,
	)
}

func seedCommunicationV4PatrolTargetWithBoundaryAndFixedPhrases(
	t *testing.T,
	h *harness,
	suffix string,
	boundary []store.MessageDraft,
	fixedPhrases string,
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
		{DocType: "沉默追问", Content: "姓名={姓名}\n年龄={年龄}\n性别={性别}\n简历={简历}\n只返回话术 JSON"},
		{DocType: "客户事实库", Content: ""},
		{DocType: "固定话术", Content: fixedPhrases},
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].DocType < documents[j].DocType })
	revision := m5ai.ContextRevision{
		ContextID:    "context-v4-patrol-" + suffix,
		RevisionHash: "revision-v4-patrol-" + suffix,
		SourceKind:   "legacyJobConfig", SourceJobRef: "job-v4-patrol-" + suffix,
		DisplayName:   "合成职位上下文-" + suffix,
		SourcePackage: m5ai.JobConfigDocumentPackage{Documents: documents},
		Communication: m5ai.CommunicationView{
			ReplyPrompt: replyPrompt, IntentPrompt: intentPrompt,
			MappingVersion: m5ai.MappingVersion,
		},
		CreatedAt: now,
	}
	if _, err := h.db.SaveCurrentLegacyJobAIContext(
		[]m5ai.ContextRevision{revision},
		now,
	); err != nil {
		t.Fatal(err)
	}
	if err := setCandidateBackendJobIDForTest(
		h,
		profileID,
		revision.SourceJobRef,
	); err != nil {
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
	tailSeq := greetingMessage.Seq
	if len(changes.Inserted) != 0 {
		tailSeq = changes.Inserted[len(changes.Inserted)-1].Seq
	}
	return communicationV4PatrolFixture{
		profileID: profileID, conversationRef: conversationRef,
		inboundSeq: tailSeq,
	}
}

func makeCommunicationV4AIMaterialUnavailable(
	t *testing.T,
	h *harness,
	profileID string,
) {
	t.Helper()
	profile, err := h.db.CandidateProfileByID(profileID)
	if err != nil || profile == nil || profile.ResumeCaptureLogicalDispatchID == nil {
		t.Fatalf("读取简历材料绑定失败: profile=%+v err=%v", profile, err)
	}
	if err := h.db.FailResumeCapture(store.FailResumeCaptureRequest{
		ProfileID: profileID, LogicalDispatchID: *profile.ResumeCaptureLogicalDispatchID,
		Reason: "fixtureAIMaterialUnavailable", At: h.clock.Now(),
	}); err != nil {
		t.Fatalf("制造 AI 材料准备缺口失败: %v", err)
	}
	if material, ready, err := h.db.CommunicationAIMaterialForProfile(profileID); err != nil || ready {
		t.Fatalf("AI 材料准备缺口不成立: material=%+v ready=%v err=%v", material, ready, err)
	}
}

func TestPageDrivenRoundAdvancesOnlyObservedV4Profile(t *testing.T) {
	h := newHarness(t)
	firstInbound := "页面当前窗入站"
	secondInbound := "数据库旧目标入站"
	first := seedCommunicationV4PatrolTarget(t, h, "page-observed", firstInbound)
	second := seedCommunicationV4PatrolTarget(t, h, "page-absent", secondInbound)

	listCalls := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Move != protocol.ListWindowMoveReset {
				t.Fatalf("完整单窗不应因候选人动作重新读取: %+v", args)
			}
			listCalls++
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary(first.conversationRef, "person-v4-patrol-page-observed", firstInbound, 0),
				},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			t.Fatal("摘要与账本一致时不应读取线程")
		default:
			return defaultHandler(request)
		}
		return nil, errors.New("unreachable")
	}
	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			switch request.Purpose {
			case m5ai.PurposeIntent:
				return safeFakeResponse(`{"信号":"有意向","理由":"fixture"}`), nil
			case m5ai.PurposeReply:
				return safeFakeResponse(`{"话术_序列":["页面驱动回复"],"动作":"无"}`), nil
			default:
				return m5ai.CompletionResponse{}, fmt.Errorf("未知建议用途 %q", request.Purpose)
			}
		},
	}
	hand := &m5PositiveHand{now: h.clock.Now}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
	manager, err := NewManager(h.db, runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}

	result, tickErr := manager.Tick(context.Background())
	if tickErr != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("页面驱动 Tick 失败: result=%+v err=%v", result, tickErr)
	}
	if len(advice.requests) != 2 || hand.commandCount() != 1 {
		t.Fatalf("只应推进页面当前窗档案: advice=%d sends=%d",
			len(advice.requests), hand.commandCount())
	}
	if listCalls != 1 {
		t.Fatalf("候选人可见动作不应打断当前可见窗口: listCalls=%d", listCalls)
	}
	firstTurn, err := h.db.LatestDialogueTurnForProfile(first.profileID)
	if err != nil || firstTurn == nil || firstTurn.Status != store.DialogueTurnCompleted {
		t.Fatalf("页面已见档案未完成: turn=%+v err=%v", firstTurn, err)
	}
	secondTurn, err := h.db.LatestDialogueTurnForProfile(second.profileID)
	if err != nil || secondTurn != nil {
		t.Fatalf("页面未见档案被数据库枚举推进: turn=%+v err=%v", secondTurn, err)
	}
	secondAggregate, err := h.db.CommunicationV4AggregateByProfile(second.profileID)
	if err != nil || secondAggregate.ProjectedThroughSeq >= second.inboundSeq {
		t.Fatalf("页面未见档案游标被推进: aggregate=%+v err=%v", secondAggregate, err)
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
				return safeFakeResponse(`{"话术_序列":["合成多档案回复"],"动作":"无"}`), nil
			default:
				return m5ai.CompletionResponse{}, fmt.Errorf("未知建议用途 %q", request.Purpose)
			}
		},
	}
	hand := &m5PositiveHand{now: h.clock.Now}
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

func TestCommunicationV4PatrolArchivesSevenDayFallbackAndSupersedesPendingDialogue(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PatrolTarget(
		t,
		h,
		"seven-day-fallback",
		"这条未处理消息也不能推迟七天兜底",
	)
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("账号读取失败: account=%+v err=%v", account, err)
	}
	pendingRoundID := "round-v4-seven-day-pending"
	beginCommunicationV4PatrolRound(t, h, pendingRoundID)
	pendingActor := &roundActor{
		manager: h.manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: pendingRoundID, now: h.clock.Now(),
	}
	if err := pendingActor.processCommunicationV4Targets(context.Background()); err != nil {
		t.Fatal(err)
	}
	pending, err := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	if err != nil || pending == nil || pending.Status != store.DialogueTurnCollected {
		t.Fatalf("provider 缺失时没有冻结待处理轮: turn=%+v err=%v", pending, err)
	}
	before, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil {
		t.Fatal(err)
	}
	makeCommunicationV4AIMaterialUnavailable(t, h, fixture.profileID)
	h.clock.Add(8 * 24 * time.Hour)
	if err := h.manager.EnableToday(h.key); err != nil {
		t.Fatal(err)
	}
	if err := h.db.BindAccountPrincipal(
		h.key,
		"hand-1",
		"principal-1",
		"session-1",
		"boot-1",
		h.clock.Now(),
	); err != nil {
		t.Fatal(err)
	}
	roundID := "round-v4-seven-day-fallback"
	beginCommunicationV4PatrolRound(t, h, roundID)
	account, err = h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("账号读取失败: account=%+v err=%v", account, err)
	}
	actor := &roundActor{
		manager: h.manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: roundID, now: h.clock.Now(),
	}

	if err := actor.processCommunicationV4Targets(context.Background()); err != nil {
		t.Fatal(err)
	}
	aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	profile, profileErr := h.db.CandidateProfileByID(fixture.profileID)
	turn, turnErr := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	if err != nil || aggregate.State.MainStatus != communication.V4StatusEnded ||
		aggregate.State.EndReason != communication.V4EndFallback ||
		aggregate.Revision != before.Revision+1 ||
		profileErr != nil || profile == nil || profile.MainStatus != store.CandidateProfileEnded ||
		profile.EndReason == nil || *profile.EndReason != store.CandidateProfileEndFallbackArchive ||
		turnErr != nil || turn == nil || turn.Status != store.DialogueTurnSuperseded ||
		turn.FailureReason != "scheduleArchivedBeforeEffect" ||
		aggregate.AutomationStatus != store.ProfileCommunicationAutomationActive {
		t.Fatalf("七天兜底没有原子归档并作废旧轮: aggregate=%+v err=%v profile=%+v profileErr=%v turn=%+v turnErr=%v",
			aggregate, err, profile, profileErr, turn, turnErr)
	}
	revision := aggregate.Revision

	for attempt := 0; attempt < 2; attempt++ {
		if err := actor.processCommunicationV4Targets(context.Background()); err != nil {
			t.Fatalf("重复巡检失败: attempt=%d err=%v", attempt+1, err)
		}
	}
	replayed, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || replayed.Revision != revision {
		t.Fatalf("七天兜底重复巡检发生增生: aggregate=%+v err=%v", replayed, err)
	}
	for _, name := range h.runner.names() {
		if name == protocol.PrimChatSendMessage {
			t.Fatalf("七天兜底不得产生候选人可见发送: calls=%+v", h.runner.names())
		}
	}
}

func TestCommunicationV4PatrolWakesEndedProfileWithoutRestoringColdBudget(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PatrolTarget(
		t,
		h,
		"ended-wakeup",
		"归档前的正常一轮",
	)
	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			switch request.Purpose {
			case m5ai.PurposeIntent:
				return safeFakeResponse(`{"信号":"有意向","理由":"fixture"}`), nil
			case m5ai.PurposeReply:
				return safeFakeResponse(`{"话术_序列":["合成唤醒回复"],"动作":"无"}`), nil
			default:
				return m5ai.CompletionResponse{}, fmt.Errorf("未知建议用途 %q", request.Purpose)
			}
		},
	}
	hand := &m5PositiveHand{now: h.clock.Now}
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
	firstRoundID := "round-v4-ended-wakeup-first"
	beginCommunicationV4PatrolRound(t, h, firstRoundID)
	firstActor := &roundActor{
		manager: manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: firstRoundID, now: h.clock.Now(),
	}
	manager.mu.Lock()
	err = firstActor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil || len(advice.requests) != 2 || hand.commandCount() != 1 {
		t.Fatalf("归档前对话没有收敛: err=%v advice=%d sends=%d",
			err, len(advice.requests), hand.commandCount())
	}

	beforeArchive, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || beforeArchive.State.LastBodyAt == nil {
		t.Fatalf("归档前聚合缺少正文锚: aggregate=%+v err=%v", beforeArchive, err)
	}
	archiveAt := beforeArchive.State.LastBodyAt.Add(8 * 24 * time.Hour)
	archiveLocal := archiveAt.In(manager.config.Location)
	if archiveLocal.Hour() < workflow.DailyStartHour {
		archiveAt = time.Date(
			archiveLocal.Year(),
			archiveLocal.Month(),
			archiveLocal.Day(),
			workflow.DailyStartHour,
			archiveLocal.Minute(),
			archiveLocal.Second(),
			archiveLocal.Nanosecond(),
			manager.config.Location,
		)
	}
	h.clock.Add(archiveAt.Sub(h.clock.Now()))
	archiveDecision, err := communication.EvaluateV4Schedule(communication.V4ScheduleInput{
		ProfileKey: fixture.profileID, State: beforeArchive.State,
		ProjectedThroughSeq: beforeArchive.ProjectedThroughSeq, Now: h.clock.Now(),
		Reply: communication.ReplyAdvice{State: communication.AdviceAbsent},
	})
	if err != nil || archiveDecision.Status != communication.V4ScheduleActionsPlanned ||
		len(archiveDecision.Actions) != 1 ||
		archiveDecision.Actions[0].Kind != communication.V4ActionArchive {
		t.Fatalf("没有得到七天归档动作: decision=%+v err=%v", archiveDecision, err)
	}
	archiveResult, err := h.db.ApplyCommunicationV4ArchiveAction(
		store.ApplyCommunicationV4ArchiveActionRequest{
			ProfileID:                   fixture.profileID,
			ConversationRef:             fixture.conversationRef,
			ExpectedRevision:            beforeArchive.Revision,
			ExpectedProjectedThroughSeq: beforeArchive.ProjectedThroughSeq,
			Action:                      archiveDecision.Actions[0],
			EvaluatedAt:                 h.clock.Now(),
			AppliedAt:                   h.clock.Now(),
		},
	)
	if err != nil || archiveResult == nil || !archiveResult.Applied ||
		archiveResult.Aggregate.State.MainStatus != communication.V4StatusEnded ||
		archiveResult.Aggregate.State.ColdPromptRemaining != 0 ||
		archiveResult.Aggregate.State.ColdWechatRemaining != 0 {
		t.Fatalf("归档前置没有收敛: result=%+v err=%v",
			archiveResult, err)
	}
	archived := archiveResult.Aggregate
	archivedRevision := archived.Revision

	if err := manager.EnableToday(h.key); err != nil {
		t.Fatal(err)
	}
	if err := h.db.BindAccountPrincipal(
		h.key,
		"hand-1",
		"principal-1",
		"session-1",
		"boot-1",
		h.clock.Now(),
	); err != nil {
		t.Fatal(err)
	}
	account, err = h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("归档后账号读取失败: account=%+v err=%v", account, err)
	}
	idleRoundID := "round-v4-ended-wakeup-idle"
	beginCommunicationV4PatrolRound(t, h, idleRoundID)
	idleActor := &roundActor{
		manager: manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: idleRoundID, now: h.clock.Now(),
	}
	manager.mu.Lock()
	err = idleActor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	idle, idleErr := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || idleErr != nil || idle.Revision != archivedRevision ||
		len(advice.requests) != 2 || hand.commandCount() != 1 {
		t.Fatalf("已结束档案无新消息时不得发生动作: err=%v aggregate=%+v aggregateErr=%v advice=%d sends=%d",
			err, idle, idleErr, len(advice.requests), hand.commandCount())
	}

	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: fixture.conversationRef,
	}
	messages, err := h.db.MessagesForConversation(key)
	if err != nil || len(messages) == 0 {
		t.Fatalf("读取唤醒前账本失败: messages=%+v err=%v", messages, err)
	}
	wakeupText := "这个职位现在还在吗"
	changes, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: messages[len(messages)-1].Seq,
		NewMessages: []store.MessageDraft{{
			Direction: "in", Kind: "text", ContentHash: syncledger.HashText(wakeupText),
			Text: &wakeupText, Origin: "external",
		}},
		SyncedAt: h.clock.Now().Add(time.Minute),
	})
	if err != nil || len(changes.Inserted) != 1 {
		t.Fatalf("追加唤醒消息失败: changes=%+v err=%v", changes, err)
	}
	wakeupSeq := changes.Inserted[0].Seq

	wakeupRoundID := "round-v4-ended-wakeup-reply"
	beginCommunicationV4PatrolRound(t, h, wakeupRoundID)
	wakeupActor := &roundActor{
		manager: manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: wakeupRoundID, now: h.clock.Now(),
	}
	manager.mu.Lock()
	err = wakeupActor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	awake, aggregateErr := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	profile, profileErr := h.db.CandidateProfileByID(fixture.profileID)
	turn, turnErr := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	if err != nil || aggregateErr != nil ||
		awake.State.MainStatus != communication.V4StatusCommunicating ||
		awake.State.EndReason != "" ||
		awake.State.ColdPromptRemaining != 0 || awake.State.ColdWechatRemaining != 0 ||
		awake.State.LastRealMessageSeq != wakeupSeq ||
		awake.ProjectedThroughSeq != wakeupSeq+1 ||
		profileErr != nil || profile == nil ||
		profile.MainStatus != store.CandidateProfileCommunicating || profile.EndReason != nil ||
		turnErr != nil || turn == nil || turn.Status != store.DialogueTurnCompleted ||
		len(advice.requests) != 4 || hand.commandCount() != 2 {
		t.Fatalf("已结束档案没有经既有轨道唤醒并唯一回复: err=%v aggregate=%+v aggregateErr=%v profile=%+v profileErr=%v turn=%+v turnErr=%v advice=%d sends=%d",
			err, awake, aggregateErr, profile, profileErr, turn, turnErr,
			len(advice.requests), hand.commandCount())
	}
	revision := awake.Revision

	// 唤醒回复后再推进 8 天,让七天回退归档真正到期;此前该分支靠
	// 真实挂钟污染出的"时钟倒退"保守路径误通过,注入时钟一致后必须
	// 显式经历沉默时长。
	h.clock.Add(8 * 24 * time.Hour)
	restarted, err := NewManager(h.db, runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.EnableToday(h.key); err != nil {
		t.Fatal(err)
	}
	if err := h.db.BindAccountPrincipal(
		h.key,
		"hand-1",
		"principal-1",
		"session-1",
		"boot-1",
		h.clock.Now(),
	); err != nil {
		t.Fatal(err)
	}
	account, err = h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("二次归档轮账号读取失败: account=%+v err=%v", account, err)
	}
	restartRoundID := "round-v4-ended-wakeup-second-archive"
	beginCommunicationV4PatrolRound(t, h, restartRoundID)
	restartedActor := &roundActor{
		manager: restarted, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: restartRoundID, now: h.clock.Now(),
	}
	for attempt := 0; attempt < 2; attempt++ {
		restarted.mu.Lock()
		err = restartedActor.processCommunicationV4Targets(context.Background())
		restarted.mu.Unlock()
		if err != nil {
			t.Fatalf("重启后重复巡检失败: attempt=%d err=%v", attempt+1, err)
		}
	}
	replayed, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || replayed.Revision != revision+1 ||
		replayed.State.MainStatus != communication.V4StatusEnded ||
		replayed.State.EndReason != communication.V4EndFallback ||
		len(advice.requests) != 4 || hand.commandCount() != 2 {
		t.Fatalf("唤醒后到期的第二次归档没有唯一收敛: aggregate=%+v advice=%d sends=%d err=%v",
			replayed, len(advice.requests), hand.commandCount(), err)
	}
}

func TestCommunicationV4PatrolConsumesWechatAcceptedMessageWithoutAIOrReplayGrowth(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PatrolTargetWithBoundary(t, h, "wechat-accepted", []store.MessageDraft{{
		Direction: "in", Kind: "card", CardType: "wechatExchange", CardState: "accepted",
		ContentHash: syncledger.HashText("wechat-exchanged-fixture"), Origin: "external",
	}})
	hand := &m5PositiveHand{now: h.clock.Now}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
	manager, err := NewManager(h.db, runner, h.hands, h.config)
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
	turn, turnErr := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	var action *store.CommunicationAction
	if turn != nil {
		action, _ = h.db.CommunicationActionByTurn(turn.TurnID)
	}
	if hand.commandCount() != 1 {
		t.Fatalf("换微信成功在 provider 未配置时也应只发一条固定回执: sends=%d turn=%+v action=%+v turnErr=%v",
			hand.commandCount(), turn, action, turnErr)
	}
	turn, err = h.db.LatestDialogueTurnForProfile(fixture.profileID)
	aggregate, aggregateErr := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnCompleted ||
		aggregateErr != nil || aggregate.State.WechatState != communication.V4WechatExchanged ||
		!aggregate.State.WechatReceiptSent ||
		aggregate.State.RealMessageRound != 1 || aggregate.State.LastRealMessageSeq != 0 ||
		aggregate.ProjectedThroughSeq != fixture.inboundSeq+1 {
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
	if err != nil || replayed.Revision != revision || hand.commandCount() != 1 {
		t.Fatalf("重复巡检发生状态或发送增生: aggregate=%+v sends=%d err=%v",
			replayed, hand.commandCount(), err)
	}
}

func TestCommunicationV4PatrolFreezesDialogueUntilProviderBecomesAvailable(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PatrolTarget(
		t,
		h,
		"provider-unavailable",
		"这个岗位现在还在招吗",
	)
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("账号读取失败: account=%+v err=%v", account, err)
	}
	waitingManager, err := NewManager(h.db, h.runner, h.hands, h.config)
	if err != nil {
		t.Fatal(err)
	}
	waitingRoundID := "round-v4-provider-unavailable"
	beginCommunicationV4PatrolRound(t, h, waitingRoundID)
	waitingActor := &roundActor{
		manager: waitingManager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: waitingRoundID, now: h.clock.Now(),
	}
	waitingManager.mu.Lock()
	err = waitingActor.processCommunicationV4Targets(context.Background())
	waitingManager.mu.Unlock()
	turn, turnErr := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	aggregate, aggregateErr := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || turnErr != nil || turn == nil || turn.Status != store.DialogueTurnCollected ||
		aggregateErr != nil || aggregate.State.MainStatus != communication.V4StatusCommunicating ||
		aggregate.ProjectedThroughSeq != fixture.inboundSeq ||
		len(h.runner.names()) != 0 {
		t.Fatalf("provider 缺失时没有只冻结业务轮: err=%v turn=%+v turnErr=%v aggregate=%+v aggregateErr=%v calls=%+v",
			err, turn, turnErr, aggregate, aggregateErr, h.runner.names())
	}

	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			switch request.Purpose {
			case m5ai.PurposeIntent:
				return safeFakeResponse(`{"信号":"有意向","理由":"fixture"}`), nil
			case m5ai.PurposeReply:
				return safeFakeResponse(`{"话术_序列":["合成恢复回复"],"动作":"无"}`), nil
			default:
				return m5ai.CompletionResponse{}, fmt.Errorf("未知建议用途 %q", request.Purpose)
			}
		},
	}
	hand := &m5PositiveHand{now: h.clock.Now}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
	resumedManager, err := NewManager(h.db, runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	resumedRoundID := "round-v4-provider-resumed"
	beginCommunicationV4PatrolRound(t, h, resumedRoundID)
	resumedActor := &roundActor{
		manager: resumedManager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: resumedRoundID, now: h.clock.Now(),
	}
	for attempt := 0; attempt < 2; attempt++ {
		resumedManager.mu.Lock()
		err = resumedActor.processCommunicationV4Targets(context.Background())
		resumedManager.mu.Unlock()
		if err != nil {
			t.Fatalf("provider 恢复后的巡检失败: attempt=%d err=%v", attempt+1, err)
		}
	}
	turn, turnErr = h.db.LatestDialogueTurnForProfile(fixture.profileID)
	aggregate, aggregateErr = h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if turnErr != nil || turn == nil || turn.Status != store.DialogueTurnCompleted ||
		aggregateErr != nil || aggregate.ProjectedThroughSeq != fixture.inboundSeq+1 ||
		len(advice.requests) != 2 || hand.commandCount() != 1 {
		t.Fatalf("provider 恢复后没有续跑原轮或发生增生: turn=%+v turnErr=%v aggregate=%+v aggregateErr=%v advice=%d sends=%d",
			turn, turnErr, aggregate, aggregateErr, len(advice.requests), hand.commandCount())
	}
}

func TestCommunicationV4PatrolSendsRejectionRetentionAfterWechatExchanged(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PatrolTargetWithBoundary(t, h, "rejection-retention", []store.MessageDraft{{
		Direction: "in", Kind: "card", CardType: "wechatExchange", CardState: "accepted",
		ContentHash: syncledger.HashText("wechat-exchanged-before-rejection"), Origin: "external",
	}})
	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			return m5ai.CompletionResponse{}, fmt.Errorf("拒绝短路与换微信回执都不得调用 AI: %s", request.Purpose)
		},
	}
	hand := &m5PositiveHand{now: h.clock.Now}
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
	roundID := "round-v4-rejection-retention"
	beginCommunicationV4PatrolRound(t, h, roundID)
	actor := &roundActor{
		manager: manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: roundID, now: h.clock.Now(),
	}

	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil || len(advice.requests) != 0 || hand.commandCount() != 1 {
		t.Fatalf("换微信成功前置没有收敛: err=%v advice=%+v sends=%d",
			err, advice.requests, hand.commandCount())
	}

	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: fixture.conversationRef,
	}
	messages, err := h.db.MessagesForConversation(key)
	if err != nil || len(messages) == 0 {
		t.Fatalf("读取换微信后账本失败: messages=%+v err=%v", messages, err)
	}
	rejection := "不感兴趣"
	changes, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: messages[len(messages)-1].Seq,
		NewMessages: []store.MessageDraft{{
			Direction: "in", Kind: "text", ContentHash: syncledger.HashText(rejection),
			Text: &rejection, Origin: "external",
		}},
		SyncedAt: h.clock.Now().Add(time.Minute),
	})
	if err != nil || len(changes.Inserted) != 1 {
		t.Fatalf("追加拒绝消息失败: changes=%+v err=%v", changes, err)
	}
	rejectionSeq := changes.Inserted[0].Seq

	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	turn, turnErr := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	aggregate, aggregateErr := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || turnErr != nil || turn == nil || turn.Status != store.DialogueTurnCompleted ||
		aggregateErr != nil || !aggregate.State.RetentionSent ||
		aggregate.State.RejectionStage != communication.V4RejectionStageRetention ||
		aggregate.State.WechatState != communication.V4WechatExchanged ||
		aggregate.ProjectedThroughSeq != rejectionSeq+1 ||
		len(advice.requests) != 0 || hand.commandCount() != 2 {
		t.Fatalf("拒绝挽留没有经既有安全轨道收敛: err=%v turn=%+v turnErr=%v aggregate=%+v aggregateErr=%v advice=%+v sends=%d",
			err, turn, turnErr, aggregate, aggregateErr, advice.requests, hand.commandCount())
	}
	messages, err = h.db.MessagesForConversation(key)
	if err != nil || len(messages) == 0 || messages[len(messages)-1].Direction != "out" ||
		messages[len(messages)-1].Text == nil || *messages[len(messages)-1].Text != "合成挽留" {
		t.Fatalf("挽留正证没有形成唯一出站消息: messages=%+v err=%v", messages, err)
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
	if err != nil || replayed.Revision != revision || len(advice.requests) != 0 ||
		hand.commandCount() != 2 {
		t.Fatalf("拒绝挽留重复巡检发生增生: aggregate=%+v advice=%+v sends=%d err=%v",
			replayed, advice.requests, hand.commandCount(), err)
	}
}

func TestCommunicationV4PatrolSendsRejectionTextThenWechatCardThroughDispatcher(t *testing.T) {
	h := newHarness(t)
	rejection := "不感兴趣"
	fixture := seedCommunicationV4PatrolTarget(
		t,
		h,
		"rejection-text-wechat-card",
		rejection,
	)
	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			return m5ai.CompletionResponse{}, fmt.Errorf(
				"拒绝短路组合不得调用 AI: %s",
				request.Purpose,
			)
		},
	}
	hand := &m5PositiveHand{now: h.clock.Now}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
	paceCalls := 0
	h.config.InteractionPaceWait = func(ctx context.Context) error {
		paceCalls++
		return ctx.Err()
	}
	manager, err := NewManager(h.db, runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("读取拒绝组合账号失败: account=%+v err=%v", account, err)
	}
	roundID := "round-v4-rejection-text-wechat-card"
	beginCommunicationV4PatrolRound(t, h, roundID)
	actor := &roundActor{
		manager: manager,
		account: account,
		hand: HandState{
			Online: true, Session: "session-1", BootID: "boot-1",
		},
		roundID: roundID,
		now:     h.clock.Now(),
	}

	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil || hand.commandCount() != 2 || paceCalls != 2 ||
		len(advice.requests) != 0 {
		t.Fatalf(
			"拒绝正文与微信卡必须留在同一处理轮并各自等待: err=%v commands=%d pace=%d advice=%+v",
			err,
			hand.commandCount(),
			paceCalls,
			advice.requests,
		)
	}
	turn, err := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	if err != nil || turn == nil {
		t.Fatalf("读取拒绝组合 turn 失败: turn=%+v err=%v", turn, err)
	}
	aggregate, aggregateErr := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnCompleted ||
		aggregateErr != nil ||
		!aggregate.State.RetentionSent ||
		aggregate.State.WechatState != communication.V4WechatInvited ||
		aggregate.ProjectedThroughSeq != fixture.inboundSeq+2 {
		t.Fatalf(
			"拒绝组合未完成: turn=%+v aggregate=%+v err=%v aggregateErr=%v",
			turn,
			aggregate,
			err,
			aggregateErr,
		)
	}
	actions, err := h.db.CommunicationActionsByTurn(turn.TurnID)
	if err != nil || len(actions) != 2 ||
		actions[0].Kind != store.CommunicationActionReplyText ||
		actions[0].Status != store.CommunicationActionSent ||
		actions[1].Kind != store.CommunicationActionInviteWechat ||
		actions[1].Status != store.CommunicationActionSent ||
		actions[1].DependsOnActionID == nil ||
		*actions[1].DependsOnActionID != actions[0].ActionID ||
		actions[0].EffectIntentID == nil ||
		actions[1].EffectIntentID == nil ||
		*actions[0].EffectIntentID == *actions[1].EffectIntentID {
		t.Fatalf("拒绝组合动作未各自完成: actions=%+v err=%v", actions, err)
	}
	cardIntent, err := h.db.EffectIntentByID(*actions[1].EffectIntentID)
	if err != nil || cardIntent == nil ||
		cardIntent.Primitive != protocol.PrimChatSendWechatInvite ||
		cardIntent.Status != store.EffectIntentOk {
		t.Fatalf("dispatcher 未建立并完成换微信卡 WAL: intent=%+v err=%v", cardIntent, err)
	}
	hand.mu.Lock()
	commands := append([]protocol.CmdBody(nil), hand.commands...)
	hand.mu.Unlock()
	if commands[0].Name != protocol.PrimChatSendMessage ||
		commands[1].Name != protocol.PrimChatSendWechatInvite {
		t.Fatalf("拒绝组合命令顺序错误: %+v", commands)
	}
	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil || hand.commandCount() != 2 || paceCalls != 2 {
		t.Fatalf(
			"拒绝组合重复推进发生增生: err=%v commands=%d pace=%d",
			err,
			hand.commandCount(),
			paceCalls,
		)
	}
}

func TestCommunicationV4DependentCardPauseDuringChainCutsBeforeChildWAL(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PatrolTarget(
		t,
		h,
		"dependent-card-workflow-gate",
		"不感兴趣",
	)
	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			return m5ai.CompletionResponse{}, fmt.Errorf(
				"拒绝短路组合不得调用 AI: %s",
				request.Purpose,
			)
		},
	}
	hand := &m5PositiveHand{now: h.clock.Now}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
	// 子动作的节奏等待期间到达用户暂停：链内复核必须在发出子命令前截住
	// 链（《24点边界裁决-2026-07-28》链内只豁免日界，不豁免暂停）。
	var manager *Manager
	paceCalls := 0
	h.config.InteractionPaceWait = func(ctx context.Context) error {
		paceCalls++
		if paceCalls == 2 {
			if pauseErr := manager.PauseNow(h.key); pauseErr != nil {
				return pauseErr
			}
		}
		return ctx.Err()
	}
	manager, err := NewManager(h.db, runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	manager.SetWorkflowMemberGate(func() error {
		if hand.commandCount() >= 1 {
			t.Errorf("链内推进不得重进 workflow member gate: commands=%d", hand.commandCount())
		}
		return nil
	})
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("读取 dependent 卡片账号失败: account=%+v err=%v", account, err)
	}
	roundID := "round-v4-dependent-card-workflow-gate"
	beginCommunicationV4PatrolRound(t, h, roundID)
	actor := &roundActor{
		manager: manager,
		account: account,
		hand: HandState{
			Online: true, Session: "session-1", BootID: "boot-1",
		},
		roundID: roundID,
		now:     h.clock.Now(),
	}

	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if !errors.Is(err, ErrActorPaused) || hand.commandCount() != 1 ||
		paceCalls != 2 || len(advice.requests) != 0 {
		t.Fatalf(
			"父动作后暂停必须阻止子动作构造 WAL: err=%v commands=%d pace=%d advice=%+v",
			err,
			hand.commandCount(),
			paceCalls,
			advice.requests,
		)
	}
	turn, err := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnAdviceReady {
		t.Fatalf("暂停后 turn 必须保留在可恢复状态: turn=%+v err=%v", turn, err)
	}
	actions, err := h.db.CommunicationActionsByTurn(turn.TurnID)
	if err != nil || len(actions) != 2 ||
		actions[0].Kind != store.CommunicationActionReplyText ||
		actions[0].Status != store.CommunicationActionSent ||
		actions[0].EffectIntentID == nil ||
		actions[1].Kind != store.CommunicationActionInviteWechat ||
		actions[1].Status != store.CommunicationActionPlanned ||
		actions[1].EffectIntentID != nil ||
		actions[1].FailureReason != "" ||
		actions[1].DependsOnActionID == nil ||
		*actions[1].DependsOnActionID != actions[0].ActionID {
		t.Fatalf("暂停误伤了 dependent 卡片恢复点: actions=%+v err=%v", actions, err)
	}
}

func TestCommunicationV4DependentCardStartedChainFinishesAcrossMidnight(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PatrolTarget(
		t,
		h,
		"dependent-card-cross-midnight",
		"不感兴趣",
	)
	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			return m5ai.CompletionResponse{}, fmt.Errorf(
				"拒绝短路组合不得调用 AI: %s",
				request.Purpose,
			)
		},
	}
	hand := &m5PositiveHand{now: h.clock.Now}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
	// 子卡片的节奏等待期间本地时间跨过 24:00。按《24点边界裁决-2026-07-28》
	// 已发出首条可见动作的链必须继续收束到终局，而不是把卡片留在 planned
	// 隔夜（正是 2026-07-27 真实案例的形态）。
	paceCalls := 0
	h.config.InteractionPaceWait = func(ctx context.Context) error {
		paceCalls++
		if paceCalls == 2 {
			h.clock.Add(15 * time.Hour)
		}
		return ctx.Err()
	}
	manager, err := NewManager(h.db, runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	manager.SetWorkflowMemberGate(func() error {
		if hand.commandCount() >= 1 {
			t.Errorf("链内推进不得重进 workflow member gate: commands=%d", hand.commandCount())
		}
		return nil
	})
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("读取跨日链账号失败: account=%+v err=%v", account, err)
	}
	roundID := "round-v4-dependent-card-cross-midnight"
	beginCommunicationV4PatrolRound(t, h, roundID)
	actor := &roundActor{
		manager: manager,
		account: account,
		hand: HandState{
			Online: true, Session: "session-1", BootID: "boot-1",
		},
		roundID: roundID,
		now:     h.clock.Now(),
	}

	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil || hand.commandCount() != 2 || paceCalls != 2 ||
		len(advice.requests) != 0 {
		t.Fatalf(
			"已开始的链未跨点收束: err=%v commands=%d pace=%d advice=%+v",
			err,
			hand.commandCount(),
			paceCalls,
			advice.requests,
		)
	}
	turn, err := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	if err != nil || turn == nil {
		t.Fatalf("读取跨日链 turn 失败: turn=%+v err=%v", turn, err)
	}
	actions, err := h.db.CommunicationActionsByTurn(turn.TurnID)
	if err != nil || len(actions) != 2 ||
		actions[0].Kind != store.CommunicationActionReplyText ||
		actions[0].Status != store.CommunicationActionSent ||
		actions[1].Kind != store.CommunicationActionInviteWechat ||
		actions[1].Status != store.CommunicationActionSent ||
		actions[1].EffectIntentID == nil {
		t.Fatalf("跨点后组合动作未各自终局: actions=%+v err=%v", actions, err)
	}
	cardIntent, err := h.db.EffectIntentByID(*actions[1].EffectIntentID)
	if err != nil || cardIntent == nil ||
		cardIntent.Primitive != protocol.PrimChatSendWechatInvite ||
		cardIntent.Status != store.EffectIntentOk {
		t.Fatalf("跨点卡片未取得正证: intent=%+v err=%v", cardIntent, err)
	}
	hand.mu.Lock()
	commands := append([]protocol.CmdBody(nil), hand.commands...)
	hand.mu.Unlock()
	if commands[0].Name != protocol.PrimChatSendMessage ||
		commands[1].Name != protocol.PrimChatSendWechatInvite {
		t.Fatalf("跨点链命令顺序错误: %+v", commands)
	}
}

func TestCommunicationV4PatrolSendsAIReplyThenInterviewCardThroughDispatcher(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PatrolTarget(
		t,
		h,
		"ai-reply-interview-card",
		"明天下午方便面试",
	)
	slots := m5ai.GenerateDefaultSlots(h.clock.Now())
	if len(slots) == 0 {
		t.Fatal("测试时钟没有生成可约面时段")
	}
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := time.ParseInLocation(
		"2006-01-02 15:04:05",
		slots[0],
		shanghai,
	)
	if err != nil {
		t.Fatal(err)
	}
	meetingTime := fmt.Sprintf(
		"%d月%d日%s",
		selected.Month(),
		selected.Day(),
		selected.Format("15:04"),
	)
	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			switch request.Purpose {
			case m5ai.PurposeIntent:
				return safeFakeResponse(`{"信号":"有意向","理由":"fixture"}`), nil
			case m5ai.PurposeReply:
				return safeFakeResponse(fmt.Sprintf(
					`{"话术_序列":["那我们约在这个时间视频面试。"],"动作":"发起线上会议","会议时间":%q}`,
					meetingTime,
				)), nil
			default:
				return m5ai.CompletionResponse{}, fmt.Errorf(
					"未知建议用途 %q",
					request.Purpose,
				)
			}
		},
	}
	hand := &m5PositiveHand{now: h.clock.Now}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
	paceCalls := 0
	h.config.InteractionPaceWait = func(ctx context.Context) error {
		paceCalls++
		return ctx.Err()
	}
	manager, err := NewManager(h.db, runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("读取邀面组合账号失败: account=%+v err=%v", account, err)
	}
	roundID := "round-v4-ai-reply-interview-card"
	beginCommunicationV4PatrolRound(t, h, roundID)
	actor := &roundActor{
		manager: manager,
		account: account,
		hand: HandState{
			Online: true, Session: "session-1", BootID: "boot-1",
		},
		roundID: roundID,
		now:     h.clock.Now(),
	}

	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil || hand.commandCount() != 2 || paceCalls != 2 ||
		len(advice.requests) != 2 {
		t.Fatalf(
			"AI 正文与邀面卡必须留在同一处理轮并各自等待: err=%v commands=%d pace=%d advice=%+v",
			err,
			hand.commandCount(),
			paceCalls,
			advice.requests,
		)
	}
	turn, err := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	if err != nil || turn == nil {
		t.Fatalf("读取邀面组合 turn 失败: turn=%+v err=%v", turn, err)
	}
	hand.mu.Lock()
	commands := append([]protocol.CmdBody(nil), hand.commands...)
	hand.mu.Unlock()
	if commands[0].Name != protocol.PrimChatSendMessage ||
		commands[1].Name != protocol.PrimChatSendInviteCard {
		t.Fatalf("邀面组合命令顺序错误: %+v", commands)
	}
	var cardArgs protocol.ChatSendInviteCardArgs
	if err := json.Unmarshal(commands[1].Args, &cardArgs); err != nil {
		t.Fatal(err)
	}
	if cardArgs.Interview.StartsAt != selected.UnixMilli() ||
		cardArgs.Interview.EndsAt !=
			selected.UnixMilli()+communication.V4InterviewDurationMs ||
		cardArgs.Interview.Method != protocol.InterviewMethodWechatVideo {
		t.Fatalf("dispatcher WAL 参数偏离冻结时段: %+v", cardArgs)
	}
	turn, err = h.db.DialogueTurnByID(turn.TurnID)
	aggregate, aggregateErr := h.db.CommunicationV4AggregateByProfile(
		fixture.profileID,
	)
	if err != nil || turn == nil ||
		turn.Status != store.DialogueTurnCompleted ||
		aggregateErr != nil ||
		aggregate.State.MainStatus != communication.V4StatusInvited {
		t.Fatalf(
			"邀面组合未在卡片正证后完成: turn=%+v aggregate=%+v err=%v aggregateErr=%v",
			turn,
			aggregate,
			err,
			aggregateErr,
		)
	}
	actions, err := h.db.CommunicationActionsByTurn(turn.TurnID)
	if err != nil || len(actions) != 2 ||
		actions[0].Kind != store.CommunicationActionReplyText ||
		actions[0].Status != store.CommunicationActionSent ||
		actions[1].Kind != store.CommunicationActionInterviewInvite ||
		actions[1].Status != store.CommunicationActionSent ||
		actions[1].DependsOnActionID == nil ||
		*actions[1].DependsOnActionID != actions[0].ActionID ||
		actions[0].EffectIntentID == nil ||
		actions[1].EffectIntentID == nil ||
		*actions[0].EffectIntentID == *actions[1].EffectIntentID ||
		actions[1].InterviewStartsAtMs == nil ||
		*actions[1].InterviewStartsAtMs != selected.UnixMilli() ||
		actions[1].InterviewEndsAtMs == nil ||
		*actions[1].InterviewEndsAtMs !=
			selected.UnixMilli()+communication.V4InterviewDurationMs ||
		actions[1].InterviewMethod == nil ||
		*actions[1].InterviewMethod != "wechatVideo" {
		t.Fatalf("邀面组合没有形成两条独立 WAL: actions=%+v err=%v", actions, err)
	}
}

func TestCommunicationV4PatrolArchivesAfterThirtySixSilentHoursWithoutAvailableColdAction(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PatrolTargetWithBoundary(t, h, "thirty-six-hour-archive", []store.MessageDraft{{
		Direction: "in", Kind: "card", CardType: "wechatExchange", CardState: "accepted",
		ContentHash: syncledger.HashText("wechat-exchanged-before-silence"), Origin: "external",
	}})
	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			return m5ai.CompletionResponse{}, fmt.Errorf("固定事件与拒绝短路不得调用 AI: %s", request.Purpose)
		},
	}
	hand := &m5PositiveHand{now: h.clock.Now}
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
	roundID := "round-v4-thirty-six-hour-prepare"
	beginCommunicationV4PatrolRound(t, h, roundID)
	actor := &roundActor{
		manager: manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: roundID, now: h.clock.Now(),
	}
	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil || hand.commandCount() != 1 {
		t.Fatalf("换微信成功前置没有收敛: err=%v sends=%d", err, hand.commandCount())
	}

	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: fixture.conversationRef,
	}
	messages, err := h.db.MessagesForConversation(key)
	if err != nil || len(messages) == 0 {
		t.Fatalf("读取换微信后账本失败: messages=%+v err=%v", messages, err)
	}
	rejection := "不感兴趣"
	changes, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: messages[len(messages)-1].Seq,
		NewMessages: []store.MessageDraft{{
			Direction: "in", Kind: "text", ContentHash: syncledger.HashText(rejection),
			Text: &rejection, Origin: "external",
		}},
		SyncedAt: h.clock.Now().Add(time.Minute),
	})
	if err != nil || len(changes.Inserted) != 1 {
		t.Fatalf("追加拒绝消息失败: changes=%+v err=%v", changes, err)
	}
	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	prepared, aggregateErr := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || aggregateErr != nil || !prepared.State.RetentionSent ||
		prepared.State.ColdPromptRemaining != 0 || prepared.State.ColdWechatRemaining != 0 ||
		prepared.State.WechatState != communication.V4WechatExchanged ||
		prepared.State.LastOutboundAt == nil ||
		hand.commandCount() != 2 {
		t.Fatalf("36 小时归档前置状态不成立: err=%v aggregate=%+v aggregateErr=%v sends=%d",
			err, prepared, aggregateErr, hand.commandCount())
	}
	makeCommunicationV4AIMaterialUnavailable(t, h, fixture.profileID)

	// 保持在与最后出站相同的日内时刻，避免跨日推进恰好落到 07:00
	// 开跑窗口之前，让沉默归档断言受测试实际执行时刻影响。
	archiveAt := prepared.State.LastOutboundAt.Add(48 * time.Hour)
	h.clock.Add(archiveAt.Sub(h.clock.Now()))
	if err := manager.EnableToday(h.key); err != nil {
		t.Fatal(err)
	}
	if err := h.db.BindAccountPrincipal(
		h.key,
		"hand-1",
		"principal-1",
		"session-1",
		"boot-1",
		h.clock.Now(),
	); err != nil {
		t.Fatal(err)
	}
	account, err = h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("归档轮账号读取失败: account=%+v err=%v", account, err)
	}
	archiveRoundID := "round-v4-thirty-six-hour-archive"
	beginCommunicationV4PatrolRound(t, h, archiveRoundID)
	archiveActor := &roundActor{
		manager: manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: archiveRoundID, now: h.clock.Now(),
	}
	manager.mu.Lock()
	err = archiveActor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	archived, aggregateErr := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	profile, profileErr := h.db.CandidateProfileByID(fixture.profileID)
	if err != nil || aggregateErr != nil ||
		archived.State.MainStatus != communication.V4StatusEnded ||
		archived.State.EndReason != communication.V4EndSilentWechatExchanged ||
		archived.Revision != prepared.Revision+1 ||
		profileErr != nil || profile == nil || profile.MainStatus != store.CandidateProfileEnded ||
		profile.EndReason == nil || *profile.EndReason != store.CandidateProfileEndSilentWechatExchanged ||
		hand.commandCount() != 2 {
		t.Fatalf("36 小时沉默归档没有收敛: err=%v aggregate=%+v aggregateErr=%v profile=%+v profileErr=%v sends=%d",
			err, archived, aggregateErr, profile, profileErr, hand.commandCount())
	}
	revision := archived.Revision

	for attempt := 0; attempt < 2; attempt++ {
		manager.mu.Lock()
		err = archiveActor.processCommunicationV4Targets(context.Background())
		manager.mu.Unlock()
		if err != nil {
			t.Fatalf("重复巡检失败: attempt=%d err=%v", attempt+1, err)
		}
	}
	replayed, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || replayed.Revision != revision || hand.commandCount() != 2 {
		t.Fatalf("36 小时归档重复巡检发生增生: aggregate=%+v sends=%d err=%v",
			replayed, hand.commandCount(), err)
	}
}

func TestCommunicationV4PatrolProjectsHumanOutboundTailAndSlidesClocks(t *testing.T) {
	h := newHarness(t)
	text := "真人已经在页面里回复"
	manualAtMs := h.clock.Now().Add(time.Hour).UnixMilli()
	fixture := seedCommunicationV4PatrolTargetWithBoundary(t, h, "human-outbound", []store.MessageDraft{{
		Direction: "out", Kind: "text", ContentHash: syncledger.HashText(text),
		Text: &text, Origin: "external", TsApproxMs: &manualAtMs,
	}})
	before, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil {
		t.Fatal(err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("账号读取失败: account=%+v err=%v", account, err)
	}
	roundID := "round-v4-human-outbound"
	beginCommunicationV4PatrolRound(t, h, roundID)
	actor := &roundActor{
		manager: h.manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: roundID, now: h.clock.Now(),
	}

	if err := actor.processCommunicationV4Targets(context.Background()); err != nil {
		t.Fatal(err)
	}
	aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	turn, turnErr := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	manualAt := time.UnixMilli(manualAtMs).UTC()
	if err != nil || aggregate.Revision != before.Revision+1 ||
		aggregate.ProjectedThroughSeq != fixture.inboundSeq ||
		aggregate.State.LastOutboundMessageSeq != fixture.inboundSeq ||
		aggregate.State.LastOutboundAt == nil || !aggregate.State.LastOutboundAt.Equal(manualAt) ||
		aggregate.State.LastBodyAt == nil || !aggregate.State.LastBodyAt.Equal(manualAt) ||
		aggregate.State.ClockUncertain || aggregate.State.BodyClockUncertain ||
		aggregate.AutomationStatus != store.ProfileCommunicationAutomationActive ||
		turnErr != nil || turn != nil {
		t.Fatalf("真人出站没有只作为时钟事实收敛: aggregate=%+v err=%v turn=%+v turnErr=%v",
			aggregate, err, turn, turnErr)
	}
	revision := aggregate.Revision

	for attempt := 0; attempt < 2; attempt++ {
		if err := actor.processCommunicationV4Targets(context.Background()); err != nil {
			t.Fatalf("重复巡检失败: attempt=%d err=%v", attempt+1, err)
		}
	}
	replayed, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || replayed.Revision != revision {
		t.Fatalf("真人出站重复巡检发生增生: aggregate=%+v err=%v", replayed, err)
	}
	if len(h.runner.names()) != 0 {
		t.Fatalf("真人出站投影不得触发浏览器命令: calls=%+v", h.runner.names())
	}
}

func TestCommunicationV4PatrolAutoAdoptsInterleavedCandidateAndHumanOutbound(t *testing.T) {
	// 2026-08-27 停机点第二步(立案 §五-2;0727 计划 §2.1 第 6/回归 9 条已
	// 废):候选人消息与真人回复交错不再挂人工——真人出站行按构造就是新
	// 边界锚,被回应的候选人输入滑动真实消息轮与沉默锚后整段收编;没有
	// 待回应输入时不触发任何浏览器命令。
	h := newHarness(t)
	inbound, outbound := "候选人先发来消息", "真人随后已经回复"
	repliedAtMs := h.clock.Now().Add(-time.Minute).UnixMilli()
	fixture := seedCommunicationV4PatrolTargetWithBoundary(t, h, "human-interleaved", []store.MessageDraft{
		{
			Direction: "in", Kind: "text", ContentHash: syncledger.HashText(inbound),
			Text: &inbound, Origin: "external",
		},
		{
			Direction: "out", Kind: "text", ContentHash: syncledger.HashText(outbound),
			Text: &outbound, Origin: "external", TsApproxMs: &repliedAtMs,
		},
	})
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("账号读取失败: account=%+v err=%v", account, err)
	}
	roundID := "round-v4-human-interleaved"
	beginCommunicationV4PatrolRound(t, h, roundID)
	actor := &roundActor{
		manager: h.manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: roundID, now: h.clock.Now(),
	}

	if err := actor.processCommunicationV4Targets(context.Background()); err != nil {
		t.Fatal(err)
	}
	aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil ||
		aggregate.AutomationStatus != store.ProfileCommunicationAutomationActive ||
		aggregate.ManualReason != "" {
		t.Fatalf("真人插话不得再挂人工: aggregate=%+v err=%v", aggregate, err)
	}
	if aggregate.State.LastOutboundMessageSeq == 0 ||
		aggregate.State.RealMessageRound == 0 ||
		aggregate.ProjectedThroughSeq < aggregate.State.LastOutboundMessageSeq {
		t.Fatalf("已回应段未完成状态推进: state=%+v cursor=%d",
			aggregate.State, aggregate.ProjectedThroughSeq)
	}
	if len(h.runner.names()) != 0 {
		t.Fatalf("无待回应输入不得触发浏览器命令: calls=%+v", h.runner.names())
	}
}

func TestCommunicationV4PatrolAdoptsBootBacklogWithHumanCardAndCandidateAcceptance(t *testing.T) {
	// 开机攒消息幕(2026-08-27 甲方点名的必过场景,明杰案形状):停机期间
	// 候选人提问、真人手发文本与线下面试卡、候选人接受面试,四行攒在同一
	// 段落里,开机首轮必须全自动收编——真人卡归一化为邀面事件、接受卡把
	// 主线推进到已约面,全程不挂人工。旧行为(2026-08-12 真机):两边都在
	// 游标后 → interleavedOutboundBoundary 挂人工,约面成功通知丢失。
	h := newHarness(t)
	question := "面试地点在哪里"
	humanText := "在公司总部,发你定位"
	humanRepliedMs := h.clock.Now().Add(-3 * time.Minute).UnixMilli()
	cardSentMs := h.clock.Now().Add(-2 * time.Minute).UnixMilli()
	acceptedMs := h.clock.Now().Add(-time.Minute).UnixMilli()
	onsite := "onsite"
	startsAt := h.clock.Now().Add(48 * time.Hour).UnixMilli()
	fixture := seedCommunicationV4PatrolTargetWithBoundaryAndFixedPhrases(t, h, "boot-backlog", []store.MessageDraft{
		{
			Direction: "in", Kind: "text", ContentHash: syncledger.HashText(question),
			Text: &question, Origin: "external",
		},
		{
			Direction: "out", Kind: "text", ContentHash: syncledger.HashText(humanText),
			Text: &humanText, Origin: "external", TsApproxMs: &humanRepliedMs,
		},
		{
			Direction: "out", Kind: "card", CardType: "interviewInvite", CardState: "pending",
			ContentHash: syncledger.HashText("human-interview-card"), Origin: "external",
			InterviewStartsAtMs: &startsAt, InterviewMethod: &onsite, TsApproxMs: &cardSentMs,
		},
		{
			Direction: "in", Kind: "card", CardType: "interviewInvite", CardState: "accepted",
			ContentHash: syncledger.HashText("candidate-accepted-card"), Origin: "external",
			TsApproxMs: &acceptedMs,
		},
	}, `{
		"rejectWechat":{"enabled":true,"messages":["合成挽留"]},
		"silence48Wechat":{"enabled":true,"messages":["合成冷催"]},
		"wechatAccepted":{"enabled":true,"messages":["好的，晚点加你"]},
		"meetingAccepted":{"enabled":true,"messages":["好的，面试安排已确认"]},
		"offlineMeetingAccepted":{"enabled":true,"messages":["好的，到时现场见"]}
	}`)
	hand := &m5PositiveHand{now: h.clock.Now}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
	manager, err := NewManager(h.db, runner, h.hands, h.config)
	if err != nil {
		t.Fatal(err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("账号读取失败: account=%+v err=%v", account, err)
	}
	roundID := "round-v4-boot-backlog"
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
	aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil ||
		aggregate.AutomationStatus != store.ProfileCommunicationAutomationActive ||
		aggregate.ManualReason != "" {
		t.Fatalf("开机攒消息不得挂人工: aggregate=%+v err=%v", aggregate, err)
	}
	if aggregate.State.MainStatus != communication.V4StatusInterviewed {
		t.Fatalf("真人手发邀面卡+候选人接受必须推进到已约面: state=%+v", aggregate.State)
	}
	profile, err := h.db.CandidateProfileByID(fixture.profileID)
	if err != nil || profile == nil ||
		profile.MainStatus != store.CandidateProfileInterviewed ||
		profile.InterviewedAt == nil {
		t.Fatalf("档案投影未跟进已约面: profile=%+v err=%v", profile, err)
	}
	// 派发面钉死(审查补测):已约面确认语正文 + 换微信邀请卡各恰一条,
	// 不多发、不少发、不换种类。
	hand.mu.Lock()
	commands := append([]protocol.CmdBody(nil), hand.commands...)
	hand.mu.Unlock()
	sends, invites := 0, 0
	for index := range commands {
		switch commands[index].Name {
		case protocol.PrimChatSendMessage:
			sends++
		case protocol.PrimChatSendWechatInvite:
			invites++
		default:
			t.Fatalf("开机幕出现预期外的候选人可见命令: %+v", commands[index].Name)
		}
	}
	if sends != 1 || invites != 1 {
		t.Fatalf("开机幕派发面不符: sendMessage=%d wechatInvite=%d", sends, invites)
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
				return safeFakeResponse(`{"话术_序列":["合成系统边界回复"],"动作":"无"}`), nil
			default:
				return m5ai.CompletionResponse{}, fmt.Errorf("未知建议用途 %q", request.Purpose)
			}
		},
	}
	hand := &m5PositiveHand{now: h.clock.Now}
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
				return safeFakeResponse(`{"话术_序列":["可以的，我们继续聊聊岗位细节"],"动作":"无"}`), nil
			case 3:
				// 2026-07-31 规格 §七:服务补句用程序内短提示词与独立
				// provider 用途;不含攻略、简历与可约时段。
				if request.Purpose != m5ai.PurposeServiceReply {
					return m5ai.CompletionResponse{}, fmt.Errorf("服务态用途错误: %q", request.Purpose)
				}
				if !strings.Contains(request.UserContent, "候选人已经接受了面试邀请") ||
					!strings.Contains(request.UserContent, `{"回复"`) ||
					!strings.Contains(request.UserContent, "面试地址在哪里") {
					return m5ai.CompletionResponse{}, errors.New("服务态短提示词要素缺失")
				}
				if strings.Contains(request.UserContent, "系统刚刚已代表你发出") {
					// 异轮服务应答没有本轮固定段;该段只出现在接受卡与
					// 正文同轮的场景。
					return m5ai.CompletionResponse{}, errors.New("异轮服务应答不应携带固定段")
				}
				if strings.Contains(request.UserContent, "可约面时间") ||
					strings.Contains(request.UserContent, "简历=") {
					return m5ai.CompletionResponse{}, errors.New("服务态短提示词不得携带攻略输入")
				}
				return safeFakeResponse(`{"回复":"关于您问的地址，咱们微信上细聊吧～"}`), nil
			default:
				return m5ai.CompletionResponse{}, fmt.Errorf("发生未授权的第 %d 次建议调用", call)
			}
		},
	}
	hand := &m5PositiveHand{now: h.clock.Now}
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

	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: fixture.conversationRef,
	}
	messages, err := h.db.MessagesForConversation(key)
	if err != nil || len(messages) == 0 {
		t.Fatalf("服务态前置账本不可用: messages=%+v err=%v", messages, err)
	}
	acceptedAt := h.clock.Now().Add(time.Minute)
	cardText := "合成邀面卡"
	cardHash := syncledger.HashText("service-interview-card")
	cardChanges, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: messages[len(messages)-1].Seq,
		NewMessages: []store.MessageDraft{{
			Direction: "out", Kind: "card", ContentHash: cardHash,
			Text: &cardText, CardType: "interviewInvite", CardState: "pending",
			Origin: "external",
		}},
		SyncedAt: acceptedAt.Add(-time.Second),
	})
	if err != nil || len(cardChanges.Inserted) != 1 {
		t.Fatalf("服务态前置邀面卡入账失败: changes=%+v err=%v", cardChanges, err)
	}
	cardSeq := cardChanges.Inserted[0].Seq
	if _, err := h.db.ApplyCommunicationV4BusinessEvent(
		store.ApplyCommunicationV4BusinessEventRequest{
			ProfileID: fixture.profileID,
			Event: communication.BusinessEvent{
				Key:  fmt.Sprintf("message:%d", cardSeq),
				Kind: communication.EventInterviewInvited, Source: communication.EventSourceMessage,
				MessageSeq: cardSeq,
			},
			AppliedAt: acceptedAt.Add(-time.Second),
		},
	); err != nil {
		t.Fatalf("服务态前置邀面投影失败: %v", err)
	}
	accepted, err := h.db.ApplyCommunicationV4BusinessEvent(store.ApplyCommunicationV4BusinessEventRequest{
		ProfileID: fixture.profileID,
		Event: communication.BusinessEvent{
			Key: fmt.Sprintf("card:%d:pending:accepted", cardSeq), Kind: communication.EventInterviewAccepted,
			Source: communication.EventSourceCardTransition, MessageSeq: cardSeq,
			OccurredAt: &acceptedAt,
		},
		AppliedAt: acceptedAt,
	})
	if err != nil || accepted.Aggregate.State.MainStatus != communication.V4StatusInterviewed {
		t.Fatalf("服务态前置事实失败: result=%+v err=%v", accepted, err)
	}
	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil || len(advice.requests) != 2 || hand.commandCount() != 3 {
		t.Fatalf("服务态前置接受事实必须先收敛固定回执与换微信卡: err=%v advice=%+v sends=%d",
			err, advice.requests, hand.commandCount())
	}
	messages, err = h.db.MessagesForConversation(key)
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
	if len(advice.requests) != 3 || advice.requests[2].Purpose != m5ai.PurposeServiceReply ||
		hand.commandCount() != 4 {
		t.Fatalf("服务态必须新增一次 serviceReply、零 intent、一次发送: advice=%+v sends=%d",
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
				return safeFakeResponse(`{"话术_序列":["暂停前已生成的回复"],"动作":"无"}`), nil
			default:
				return m5ai.CompletionResponse{}, fmt.Errorf("未知建议用途 %q", request.Purpose)
			}
		},
	}
	hand := &m5PositiveHand{now: h.clock.Now}
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
	if err != nil || firstAction == nil ||
		firstAction.Status != store.CommunicationActionPlanned ||
		firstAction.EffectIntentID != nil || firstAction.FailureReason != "" {
		t.Fatalf("首档案的计划动作没有保留为可恢复状态: action=%+v err=%v", firstAction, err)
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
