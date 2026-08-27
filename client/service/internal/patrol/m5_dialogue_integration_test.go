package patrol

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

type recordingAdviceExecutor struct {
	requests []m5ai.CompletionRequest
	complete func(int, m5ai.CompletionRequest) (m5ai.CompletionResponse, error)
}

func (a *recordingAdviceExecutor) ProviderName() string { return "fake-provider" }
func (a *recordingAdviceExecutor) ModelName() string    { return "fake-model" }

func (a *recordingAdviceExecutor) CompleteJSON(
	_ context.Context,
	request m5ai.CompletionRequest,
) (m5ai.CompletionResponse, error) {
	a.requests = append(a.requests, request)
	if a.complete != nil {
		return a.complete(len(a.requests), request)
	}
	zero := 0
	usage := m5ai.CompletionUsage{
		InputTokens: 12, CachedInputTokens: 2, OutputTokens: 4, ReasoningTokens: &zero,
	}
	switch len(a.requests) {
	case 1:
		if request.Purpose != m5ai.PurposeIntent {
			return m5ai.CompletionResponse{}, fmt.Errorf("首次调用用途错误: %s", request.Purpose)
		}
		return m5ai.CompletionResponse{
			JSONText: `{"信号":"有意向","理由":"fixture"}`, Usage: usage, ReasoningContentEmpty: true,
		}, nil
	case 2:
		if request.Purpose != m5ai.PurposeReply {
			return m5ai.CompletionResponse{}, fmt.Errorf("第二次调用用途错误: %s", request.Purpose)
		}
		return m5ai.CompletionResponse{
			JSONText: `{"话术_序列":["合成回复"],"动作":"无"}`, Usage: usage, ReasoningContentEmpty: true,
		}, nil
	default:
		return m5ai.CompletionResponse{}, fmt.Errorf("发生未授权的第 %d 次调用", len(a.requests))
	}
}

func safeFakeResponse(raw string) m5ai.CompletionResponse {
	zero := 0
	return m5ai.CompletionResponse{
		JSONText: raw, ReasoningContentEmpty: true,
		Usage: m5ai.CompletionUsage{
			InputTokens: 12, CachedInputTokens: 2, OutputTokens: 4, ReasoningTokens: &zero,
		},
	}
}

type m5AdviceFixture struct {
	turn            store.DialogueTurn
	profileID       string
	conversationRef string
	greetingIntent  string
}

func setCandidateBackendJobIDForTest(
	h *harness,
	profileID string,
	backendJobID string,
) error {
	db, err := sql.Open(
		"sqlite",
		"file:"+filepath.Join(h.dataDir, "brain.db")+"?_pragma=busy_timeout(5000)",
	)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := db.Exec(
		"UPDATE candidate_profiles SET backend_job_id = ? WHERE profile_id = ?",
		backendJobID,
		profileID,
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("测试档案后台职位更新行数错误: %d", rows)
	}
	return nil
}

// setResumeSnapshotJSONForTest 直接改写快照正文,用于构造/修复"简历渲染
// 失败"的现场;身份与绑定列不动,不会触发边界重验。
func setResumeSnapshotJSONForTest(
	h *harness,
	snapshotID string,
	resumeJSON string,
) error {
	db, err := sql.Open(
		"sqlite",
		"file:"+filepath.Join(h.dataDir, "brain.db")+"?_pragma=busy_timeout(5000)",
	)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := db.Exec(
		"UPDATE candidate_resume_snapshots SET resume_json = ? WHERE snapshot_id = ?",
		resumeJSON,
		snapshotID,
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("测试快照正文更新行数错误: %d", rows)
	}
	return nil
}

func seedM5AdviceFixture(t *testing.T, h *harness) m5AdviceFixture {
	inboundText := "我想了解一下这个职位"
	return seedM5AdviceFixtureWithInbound(t, h, store.MessageDraft{
		Direction: "in", Kind: "text", ContentHash: syncledger.HashText(inboundText),
		Text: &inboundText, Origin: "external",
	})
}

func seedM5ResumeAdviceFixture(t *testing.T, h *harness) m5AdviceFixture {
	return seedM5AdviceFixtureWithInbound(t, h, store.MessageDraft{
		Direction: "in", Kind: "card", ContentHash: syncledger.HashText("card\x1fresumeAttachment"),
		CardType: "resumeAttachment", CardState: "unknown", Origin: "external",
	})
}

func seedM5AdviceFixtureWithInbound(
	t *testing.T,
	h *harness,
	inbound store.MessageDraft,
	moreInbound ...store.MessageDraft,
) m5AdviceFixture {
	t.Helper()
	fixture, freeze := seedM5AdviceFixtureUnfrozen(t, h, inbound, moreInbound...)
	frozen, err := h.db.FreezeDialogueTurn(freeze)
	if err != nil || !frozen.Created {
		t.Fatalf("冻结合成沟通轮失败: frozen=%+v err=%v", frozen, err)
	}
	fixture.turn = frozen.Turn
	return fixture
}

// seedM5AdviceFixtureUnfrozen 建到"入站已收、轮未冻结"为止,返回可自行冻结
// 的请求。用于验证冻结之前的巡检分支(如配置缺失跳过)。
func seedM5AdviceFixtureUnfrozen(
	t *testing.T,
	h *harness,
	inbound store.MessageDraft,
	moreInbound ...store.MessageDraft,
) (m5AdviceFixture, store.FreezeDialogueTurnRequest) {
	t.Helper()
	now := h.clock.Now()
	profileID := "profile-m5-advice"
	platformUserRef := "person-m5-advice"
	positionRef := "position-m5-advice"
	conversationRef := "conversation-m5-advice"
	displayName, positionTitle := "合成候选人", "合成职位"
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

	greetingIntent := "intent-m5-advice-greeting"
	greetingMsgID := "msg-m5-advice-greeting"
	greetingText := "合成招呼"
	greetingHash := syncledger.HashText(greetingText)
	deadline := now.Add(time.Hour).UnixMilli()
	greeting, err := h.db.CreateGreetingEffectIntentAndCmd(store.CreateGreetingEffectIntentRequest{
		Intent: store.EffectIntent{
			IntentID: greetingIntent, IdemKey: "idem-m5-advice-greeting",
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			Primitive: protocol.PrimChatSendGreeting, TargetRef: profileID,
			PayloadHash: "payload-m5-advice", GuardsHash: "guards-m5-advice",
			SendFingerprint: greetingHash, Status: store.EffectIntentDispatching, DeadlineMs: deadline,
		},
		Command: store.CmdRecord{
			MsgID: greetingMsgID, Name: protocol.PrimChatSendGreeting, Class: string(protocol.ClassEffectful),
			IdemKey: "idem-m5-advice-greeting", Domain: h.key.Platform + ":" + h.key.AccountRef,
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
	if err := h.db.MoveEffectToVerification(greeting.Command.MsgID, "fixturePositiveRead", now); err != nil {
		t.Fatal(err)
	}
	greetingMessage, err := h.db.ResolveGreetingVerified(store.VerifiedGreetingSuccess{
		Ref: greeting.Command.MsgID, ProfileID: profileID, PlatformUserRef: platformUserRef,
		PositionRef: positionRef, ConversationRef: conversationRef, Text: greetingText,
		ContentHash: greetingHash, ObservedAtMs: now.UnixMilli(),
		ResolutionReason: "fixturePositiveRead", At: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.SelectM5TrialProfile(profileID, "trial-m5-advice", "user", now); err != nil {
		t.Fatal(err)
	}

	replyPrompt := "这是完全合成的接口验收。请只返回 JSON 对象，不要返回 Markdown 或解释。" +
		"输出格式必须是 {\"话术_序列\":[\"一条简短自然的招聘沟通回复\"],\"动作\":\"无\"}。\n" +
		"简历={简历}\n时段={推荐时段}\n历史={对话历史}"
	intentPrompt := "这是完全合成的接口验收。请只返回 JSON 对象，不要返回 Markdown 或解释。" +
		"候选人表示想了解职位时，输出 {\"信号\":\"有意向\",\"理由\":\"合成验收\"}。\n" +
		"回复={回复}\n招呼={招呼语}"
	documents := []m5ai.JobConfigDocument{
		{DocType: "多轮沟通", Content: replyPrompt},
		{DocType: "意向判断", Content: intentPrompt},
		{DocType: "客户事实库", Content: ""},
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].DocType < documents[j].DocType })
	revision := m5ai.ContextRevision{
		ContextID: "context-m5-advice", RevisionHash: "revision-m5-advice",
		SourceKind: "legacyJobConfig", SourceJobRef: "job-m5-advice",
		DisplayName:   "合成职位上下文",
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
	if _, err := h.db.BindActiveM5TrialProfileAIContext(store.BindProfileAIContextRequest{
		BindingID: "binding-m5-advice", ProfileID: profileID,
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
			MsgID: "msg-m5-advice-resume", Name: protocol.PrimCandidateReadResume,
			Class: string(protocol.ClassIntrusive), Domain: h.key.Platform + ":" + h.key.AccountRef,
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			ExpectedPrincipalFingerprint: "principal-1", ContextJSON: string(contextRaw), Args: string(argsRaw),
			HandID: "hand-1", Session: "session-1", BootIDAtDispatch: "boot-1",
			Status: store.CmdQueued, DeadlineMs: deadline, ExecBudgetMs: 60_000,
		},
		Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	resumeData := protocol.CandidateReadResumeData{
		ConversationRef: conversationRef, PlatformUserRef: platformUserRef, ObservedAt: now.UnixMilli(),
		Basic:          []protocol.CandidateResumeLabelValue{{Label: "学历", Value: "本科"}},
		Expectations:   []protocol.CandidateResumeLabelValue{{Label: "职位", Value: "合成职位"}},
		SelfEvaluation: "合成自评", Education: "合成教育", WorkExperiences: "合成经历",
	}
	resumeRaw, err := protocol.Encode(resumeData)
	if err != nil {
		t.Fatal(err)
	}
	resultRaw, err := protocol.Encode(protocol.ResultBody{
		Ref: capture.Command.MsgID, Status: protocol.ResultStatusOk, Data: resumeRaw, ExecMs: 10,
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
	snapshot, err := h.db.CompleteResumeCapture(store.CompleteResumeCaptureRequest{
		ProfileID: profileID, LogicalDispatchID: capture.Command.LogicalDispatchID,
		SnapshotID: "snapshot-m5-advice", Data: resumeData,
	})
	if err != nil {
		t.Fatal(err)
	}

	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef, ConversationRef: conversationRef,
	}
	inbounds := append([]store.MessageDraft{inbound}, moreInbound...)
	changes, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: greetingMessage.Seq, PlatformUserRef: platformUserRef,
		NewMessages: inbounds,
		SyncedAt:    now,
	})
	if err != nil || len(changes.Inserted) != len(inbounds) {
		t.Fatalf("追加合成入站失败: changes=%+v err=%v", changes, err)
	}
	digest, turnID, err := store.DialogueTurnIdentity(profileID, *greetingMessage, changes.Inserted, 0)
	if err != nil {
		t.Fatal(err)
	}
	recommended, err := m5ai.FreezeRecommendedTimeText(now, m5ai.GenerateDefaultSlots(now))
	if err != nil {
		t.Fatal(err)
	}
	freeze := store.FreezeDialogueTurnRequest{
		TurnID: turnID, ProfileID: profileID, ConversationRef: conversationRef,
		InputDigest: digest, HistoryThroughSeq: greetingMessage.Seq,
		InboundFromSeq: changes.Inserted[0].Seq, InboundThroughSeq: changes.Inserted[len(changes.Inserted)-1].Seq,
		ContextRevisionHash: revision.RevisionHash, ResumeSnapshotID: snapshot.SnapshotID,
		RecommendedTimeText: recommended, RenderFormatVersion: m5ai.DialogueRenderFormatVersion,
		FrozenAt: now,
	}
	return m5AdviceFixture{
		profileID:       profileID,
		conversationRef: conversationRef, greetingIntent: greetingIntent,
	}, freeze
}

func TestAdvanceM5TurnCallsIntentThenReplyOnceAndStopsAtPlannedAction(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5AdviceFixture(t, h)
	advice := &recordingAdviceExecutor{}
	h.manager.SetAdvice(advice)
	beforeCommands, err := h.db.RecentCmds(100)
	if err != nil {
		t.Fatal(err)
	}

	actor := &roundActor{manager: h.manager, now: h.clock.Now()}
	h.manager.mu.Lock()
	err = actor.advanceM5Turn(context.Background(), fixture.turn)
	h.manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(advice.requests) != 2 || advice.requests[0].Purpose != m5ai.PurposeIntent ||
		advice.requests[1].Purpose != m5ai.PurposeReply {
		t.Fatalf("建议调用顺序/次数错误: %+v", advice.requests)
	}
	invocations, err := h.db.AIInvocationsForTurn(fixture.turn.TurnID)
	if err != nil || len(invocations) != 2 || invocations[0].Purpose != m5ai.PurposeIntent ||
		invocations[1].Purpose != m5ai.PurposeReply {
		t.Fatalf("AIInvocation 事实错误: invocations=%+v err=%v", invocations, err)
	}
	action, err := h.db.CommunicationActionByTurn(fixture.turn.TurnID)
	if err != nil || action == nil || action.Status != store.CommunicationActionPlanned ||
		action.Kind != store.CommunicationActionReplyText || action.Text != "合成回复" || action.EffectIntentID != nil {
		t.Fatalf("唯一 planned action 错误: action=%+v err=%v", action, err)
	}
	turn, err := h.db.DialogueTurnByID(fixture.turn.TurnID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnAdviceReady {
		t.Fatalf("沟通轮未停在 adviceReady: turn=%+v err=%v", turn, err)
	}
	afterCommands, err := h.db.RecentCmds(100)
	if err != nil || len(afterCommands) != len(beforeCommands) {
		t.Fatalf("批次3不得新增命令: before=%d after=%d err=%v", len(beforeCommands), len(afterCommands), err)
	}
	conversationEffect, err := h.db.LatestEffectIntent(h.key.Platform, h.key.AccountRef, fixture.conversationRef)
	if err != nil || conversationEffect != nil {
		t.Fatalf("批次3不得为会话构造 effect intent: intent=%+v err=%v", conversationEffect, err)
	}
	greeting, err := h.db.LatestGreetingEffectIntent(fixture.profileID)
	if err != nil || greeting == nil || greeting.IntentID != fixture.greetingIntent {
		t.Fatalf("既有 greeting intent 被改写: greeting=%+v err=%v", greeting, err)
	}

	restarted, err := NewManager(h.db, h.runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	restartedActor := &roundActor{manager: restarted, now: h.clock.Now()}
	restarted.mu.Lock()
	err = restartedActor.advanceM5Turn(context.Background(), *turn)
	restarted.mu.Unlock()
	if err != nil || len(advice.requests) != 2 {
		t.Fatalf("重启重放不得再次调用 provider: calls=%d err=%v", len(advice.requests), err)
	}
}

func TestAdvanceM5ResumeCardSkipsIntentAndPlansExactlyOneReply(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5ResumeAdviceFixture(t, h)
	advice := &recordingAdviceExecutor{complete: func(call int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
		if call != 1 || request.Purpose != m5ai.PurposeReply {
			return m5ai.CompletionResponse{}, fmt.Errorf("简历强意向分支发生未授权调用: call=%d purpose=%s", call, request.Purpose)
		}
		if !strings.Contains(request.UserContent, m5ResumeAttachmentHistoryText) {
			return m5ai.CompletionResponse{}, fmt.Errorf("reply 历史未使用平台无关简历事件文本")
		}
		return safeFakeResponse(`{"话术_序列":["已收到您的简历，我们继续沟通一下岗位细节。"],"动作":"无"}`), nil
	}}
	h.manager.SetAdvice(advice)
	actor := &roundActor{manager: h.manager, now: h.clock.Now()}
	h.manager.mu.Lock()
	err := actor.advanceM5Turn(context.Background(), fixture.turn)
	h.manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(advice.requests) != 1 || advice.requests[0].Purpose != m5ai.PurposeReply {
		t.Fatalf("简历强意向必须零 intent、一次 reply: requests=%+v", advice.requests)
	}
	invocations, err := h.db.AIInvocationsForTurn(fixture.turn.TurnID)
	if err != nil || len(invocations) != 1 || invocations[0].Purpose != m5ai.PurposeReply {
		t.Fatalf("简历强意向 invocation 事实错误: invocations=%+v err=%v", invocations, err)
	}
	turn, err := h.db.DialogueTurnByID(fixture.turn.TurnID)
	action, actionErr := h.db.CommunicationActionByTurn(fixture.turn.TurnID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnAdviceReady ||
		turn.IntentLabel != m5ai.IntentInterested || turn.IntentSource != store.DialogueIntentBusinessEvent ||
		actionErr != nil || action == nil || action.Status != store.CommunicationActionPlanned ||
		action.Kind != store.CommunicationActionReplyText || action.EffectIntentID != nil {
		t.Fatalf("简历强意向唯一动作事实错误: turn=%+v action=%+v err=%v actionErr=%v", turn, action, err, actionErr)
	}

	restarted, err := NewManager(h.db, h.runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	restartedActor := &roundActor{manager: restarted, now: h.clock.Now()}
	restarted.mu.Lock()
	err = restartedActor.advanceM5Turn(context.Background(), *turn)
	restarted.mu.Unlock()
	if err != nil || len(advice.requests) != 1 {
		t.Fatalf("简历强意向重启重放不得再次调用 provider: calls=%d err=%v", len(advice.requests), err)
	}
}

// 混合输入轮(规格 §五,2026-07-28 批 A)端到端:文字+简历卡+文字的一轮走
// "冻结→businessEvent 分类→一次 reply→planned 动作",零意向调用,文字与
// 简历占位文本全部进回复上下文。
func TestAdvanceM5MixedResumeTurnSkipsIntentAndPlansExactlyOneReply(t *testing.T) {
	h := newHarness(t)
	firstText, secondText := "请问做几休几", "工作时间是"
	fixture := seedM5AdviceFixtureWithInbound(t, h,
		store.MessageDraft{Direction: "in", Kind: "text", ContentHash: syncledger.HashText(firstText), Text: &firstText, Origin: "external"},
		store.MessageDraft{Direction: "in", Kind: "card", ContentHash: syncledger.HashText("card\x1fresumeAttachment"), CardType: "resumeAttachment", CardState: "unknown", Origin: "external"},
		store.MessageDraft{Direction: "in", Kind: "text", ContentHash: syncledger.HashText(secondText), Text: &secondText, Origin: "external"},
	)
	advice := &recordingAdviceExecutor{complete: func(call int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
		if call != 1 || request.Purpose != m5ai.PurposeReply {
			return m5ai.CompletionResponse{}, fmt.Errorf("混合简历轮发生未授权调用: call=%d purpose=%s", call, request.Purpose)
		}
		if !strings.Contains(request.UserContent, m5ResumeAttachmentHistoryText) ||
			!strings.Contains(request.UserContent, firstText) ||
			!strings.Contains(request.UserContent, secondText) {
			return m5ai.CompletionResponse{}, fmt.Errorf("reply 上下文缺失文字或简历事件文本")
		}
		return safeFakeResponse(`{"话术_序列":["简历收到了，工作时间我们详细说下。"],"动作":"无"}`), nil
	}}
	h.manager.SetAdvice(advice)
	actor := &roundActor{manager: h.manager, now: h.clock.Now()}
	h.manager.mu.Lock()
	err := actor.advanceM5Turn(context.Background(), fixture.turn)
	h.manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(advice.requests) != 1 || advice.requests[0].Purpose != m5ai.PurposeReply {
		t.Fatalf("混合简历轮必须零 intent、一次 reply: requests=%+v", advice.requests)
	}
	invocations, err := h.db.AIInvocationsForTurn(fixture.turn.TurnID)
	if err != nil || len(invocations) != 1 || invocations[0].Purpose != m5ai.PurposeReply {
		t.Fatalf("混合简历轮 invocation 事实错误: invocations=%+v err=%v", invocations, err)
	}
	turn, err := h.db.DialogueTurnByID(fixture.turn.TurnID)
	action, actionErr := h.db.CommunicationActionByTurn(fixture.turn.TurnID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnAdviceReady ||
		turn.IntentLabel != m5ai.IntentInterested || turn.IntentSource != store.DialogueIntentBusinessEvent ||
		actionErr != nil || action == nil || action.Status != store.CommunicationActionPlanned ||
		action.Kind != store.CommunicationActionReplyText || action.EffectIntentID != nil {
		t.Fatalf("混合简历轮唯一动作事实错误: turn=%+v action=%+v err=%v actionErr=%v", turn, action, err, actionErr)
	}

	restarted, err := NewManager(h.db, h.runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	restartedActor := &roundActor{manager: restarted, now: h.clock.Now()}
	restarted.mu.Lock()
	err = restartedActor.advanceM5Turn(context.Background(), *turn)
	restarted.mu.Unlock()
	if err != nil || len(advice.requests) != 1 {
		t.Fatalf("混合简历轮重启重放不得再次调用 provider: calls=%d err=%v", len(advice.requests), err)
	}
}

func TestResumeReplyReservationInterruptedBecomesManualWithoutSecondCall(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5ResumeAdviceFixture(t, h)
	classified, err := h.db.ApplyResumeBusinessClassification(fixture.turn.TurnID, h.clock.Now())
	if err != nil || classified == nil || classified.Status != store.DialogueTurnClassified ||
		classified.IntentSource != store.DialogueIntentBusinessEvent {
		t.Fatalf("简历业务事件分类失败: turn=%+v err=%v", classified, err)
	}
	reserved, err := h.db.ReserveAIInvocation(store.ReserveAIInvocationRequest{
		InvocationID: stableM5ID("invocation", fixture.turn.TurnID, string(m5ai.PurposeReply), "1"),
		TurnID:       fixture.turn.TurnID, Purpose: m5ai.PurposeReply, Attempt: 1,
		Provider: "fake-provider", Model: "fake-model", InputHash: "synthetic-reply-input",
		CreatedAt: h.clock.Now(),
	})
	if err != nil || reserved == nil || !reserved.Created {
		t.Fatalf("reply 预留失败: reserved=%+v err=%v", reserved, err)
	}
	recovered, err := h.db.RecoverInterruptedAIInvocations(h.clock.Now().Add(time.Minute))
	if err != nil || recovered != 1 {
		t.Fatalf("reply 崩溃恢复失败: recovered=%d err=%v", recovered, err)
	}
	turn, err := h.db.DialogueTurnByID(fixture.turn.TurnID)
	action, actionErr := h.db.CommunicationActionByTurn(fixture.turn.TurnID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnManualRequired ||
		turn.FailureReason != "replyProcessInterrupted" || actionErr != nil || action != nil {
		t.Fatalf("reply 预留崩溃不得重调或造动作: turn=%+v action=%+v err=%v actionErr=%v", turn, action, err, actionErr)
	}
}

// 2026-08-02 裁决回归(第 1 条补齐):轮已冻结后 AI 输入渲染失败
// (resumeRenderFailed)不写 turn 终局、不冻结候选人,失败发生在预留调用之前
// 故本轮除 intent 外零 provider 调用;快照修复后下一巡检轮自然续跑到建议与
// planned 动作。
func TestAdvanceM5TurnResumeRenderFailureSkipsRoundWithoutFreeze(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5AdviceFixture(t, h)
	advice := &recordingAdviceExecutor{}
	h.manager.SetAdvice(advice)
	actor := &roundActor{manager: h.manager, now: h.clock.Now()}

	snapshot, err := h.db.CandidateResumeSnapshotByID(
		fixture.profileID, fixture.turn.ResumeSnapshotID,
	)
	if err != nil || snapshot == nil {
		t.Fatalf("读取简历快照: snapshot=%+v err=%v", snapshot, err)
	}
	if err := setResumeSnapshotJSONForTest(h, fixture.turn.ResumeSnapshotID, "{}"); err != nil {
		t.Fatal(err)
	}

	h.manager.mu.Lock()
	err = actor.advanceM5Turn(context.Background(), fixture.turn)
	h.manager.mu.Unlock()
	// intent 一次成功后进入 reply 渲染;简历渲染失败即跳过,不再有调用。
	if err != nil || len(advice.requests) != 1 ||
		advice.requests[0].Purpose != m5ai.PurposeIntent {
		t.Fatalf("渲染失败轮应只有一次 intent 调用: calls=%d err=%v", len(advice.requests), err)
	}
	turn, err := h.db.DialogueTurnByID(fixture.turn.TurnID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnClassified ||
		turn.FailureReason != "" {
		t.Fatalf("渲染失败不得写 turn 终局: turn=%+v err=%v", turn, err)
	}
	assertM5TrialStillActive(t, h)

	if err := setResumeSnapshotJSONForTest(
		h, fixture.turn.ResumeSnapshotID, snapshot.ResumeJSON,
	); err != nil {
		t.Fatal(err)
	}
	h.manager.mu.Lock()
	err = actor.advanceM5Turn(context.Background(), *turn)
	h.manager.mu.Unlock()
	if err != nil || len(advice.requests) != 2 {
		t.Fatalf("快照修复后下一巡检轮应自然续跑: calls=%d err=%v", len(advice.requests), err)
	}
	turn, err = h.db.DialogueTurnByID(fixture.turn.TurnID)
	action, actionErr := h.db.CommunicationActionByTurn(fixture.turn.TurnID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnAdviceReady ||
		actionErr != nil || action == nil || action.Status != store.CommunicationActionPlanned {
		t.Fatalf("续跑轮未形成 planned 动作: turn=%+v action=%+v err=%v actionErr=%v",
			turn, action, err, actionErr)
	}
}

func TestAdvanceM5TurnIntentFailureFallsBackOnceThenReplies(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5AdviceFixture(t, h)
	advice := &recordingAdviceExecutor{complete: func(call int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
		switch call {
		case 1:
			if request.Purpose != m5ai.PurposeIntent {
				return m5ai.CompletionResponse{}, fmt.Errorf("首次调用用途错误: %s", request.Purpose)
			}
			return m5ai.CompletionResponse{}, context.DeadlineExceeded
		case 2:
			if request.Purpose != m5ai.PurposeReply {
				return m5ai.CompletionResponse{}, fmt.Errorf("第二次调用用途错误: %s", request.Purpose)
			}
			return safeFakeResponse(`{"话术_序列":["合成兜底回复"],"动作":"无"}`), nil
		default:
			return m5ai.CompletionResponse{}, fmt.Errorf("发生未授权的第 %d 次调用", call)
		}
	}}
	h.manager.SetAdvice(advice)
	actor := &roundActor{manager: h.manager, now: h.clock.Now()}
	h.manager.mu.Lock()
	err := actor.advanceM5Turn(context.Background(), fixture.turn)
	h.manager.mu.Unlock()
	if err != nil || len(advice.requests) != 2 {
		t.Fatalf("intent 失败后必须只走一次独立 reply: calls=%d err=%v", len(advice.requests), err)
	}
	turn, _ := h.db.DialogueTurnByID(fixture.turn.TurnID)
	action, actionErr := h.db.CommunicationActionByTurn(fixture.turn.TurnID)
	if turn == nil || turn.Status != store.DialogueTurnAdviceReady || turn.IntentLabel != m5ai.IntentNeutral ||
		turn.IntentSource != store.DialogueIntentLLMFailure || actionErr != nil || action == nil ||
		action.Text != "合成兜底回复" {
		t.Fatalf("intent fallback 编排事实错误: turn=%+v action=%+v actionErr=%v", turn, action, actionErr)
	}
}

func TestAdvanceM5TurnLLMRejectedStopsBeforeReplyAndKeepsClassification(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5AdviceFixture(t, h)
	advice := &recordingAdviceExecutor{complete: func(call int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
		if call != 1 || request.Purpose != m5ai.PurposeIntent {
			return m5ai.CompletionResponse{}, fmt.Errorf("拒绝轮发生额外调用: call=%d purpose=%s", call, request.Purpose)
		}
		return safeFakeResponse(`{"信号":"拒绝","理由":"fixture"}`), nil
	}}
	h.manager.SetAdvice(advice)
	actor := &roundActor{manager: h.manager, now: h.clock.Now()}
	h.manager.mu.Lock()
	err := actor.advanceM5Turn(context.Background(), fixture.turn)
	h.manager.mu.Unlock()
	if err != nil || len(advice.requests) != 1 {
		t.Fatalf("LLM rejected 必须仅一次 intent 调用: calls=%d err=%v", len(advice.requests), err)
	}
	turn, _ := h.db.DialogueTurnByID(fixture.turn.TurnID)
	action, actionErr := h.db.CommunicationActionByTurn(fixture.turn.TurnID)
	if turn == nil || turn.Status != store.DialogueTurnManualRequired || turn.IntentLabel != m5ai.IntentRejected ||
		turn.IntentSource != store.DialogueIntentLLM || turn.FailureReason != "intentRejected" ||
		actionErr != nil || action != nil {
		t.Fatalf("LLM rejected 分类/人工事实错误: turn=%+v action=%+v actionErr=%v", turn, action, actionErr)
	}
}

func TestAdvanceM5TurnMissingReasoningUsageWithEmptyContentCanPlan(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5AdviceFixture(t, h)
	advice := &recordingAdviceExecutor{complete: func(call int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
		switch call {
		case 1:
			if request.Purpose != m5ai.PurposeIntent {
				return m5ai.CompletionResponse{}, fmt.Errorf("首次调用用途错误: %s", request.Purpose)
			}
			return m5ai.CompletionResponse{
				JSONText:              `{"信号":"有意向","理由":"fixture"}`,
				Usage:                 m5ai.CompletionUsage{InputTokens: 3, OutputTokens: 2},
				ReasoningContentEmpty: true,
			}, nil
		case 2:
			if request.Purpose != m5ai.PurposeReply {
				return m5ai.CompletionResponse{}, fmt.Errorf("第二次调用用途错误: %s", request.Purpose)
			}
			return m5ai.CompletionResponse{
				JSONText:              `{"话术_序列":["缺失 usage 字段的合成回复"],"动作":"无"}`,
				Usage:                 m5ai.CompletionUsage{InputTokens: 4, OutputTokens: 3},
				ReasoningContentEmpty: true,
			}, nil
		default:
			return m5ai.CompletionResponse{}, fmt.Errorf("发生未授权的第 %d 次调用", call)
		}
	}}
	h.manager.SetAdvice(advice)
	actor := &roundActor{manager: h.manager, now: h.clock.Now()}
	h.manager.mu.Lock()
	err := actor.advanceM5Turn(context.Background(), fixture.turn)
	h.manager.mu.Unlock()
	if err != nil || len(advice.requests) != 2 {
		t.Fatalf("reasoning usage 缺失且 content 空应完成两次建议: calls=%d err=%v", len(advice.requests), err)
	}
	invocations, err := h.db.AIInvocationsForTurn(fixture.turn.TurnID)
	if err != nil || len(invocations) != 2 {
		t.Fatalf("缺失 usage 的 invocation 事实不完整: invocations=%+v err=%v", invocations, err)
	}
	for _, invocation := range invocations {
		if invocation.Status != store.AIInvocationOK || invocation.UsageShape != store.AIInvocationReasoningFieldAbsent ||
			invocation.ReasoningTokens != nil || invocation.InputTokens <= 0 || invocation.OutputTokens <= 0 {
			t.Fatalf("缺失 usage 事实被伪造或丢失计量: invocation=%+v", invocation)
		}
	}
	turn, err := h.db.DialogueTurnByID(fixture.turn.TurnID)
	action, actionErr := h.db.CommunicationActionByTurn(fixture.turn.TurnID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnAdviceReady ||
		actionErr != nil || action == nil || action.Status != store.CommunicationActionPlanned {
		t.Fatalf("缺失 usage 的安全响应未形成 planned action: turn=%+v action=%+v err=%v actionErr=%v", turn, action, err, actionErr)
	}
}
