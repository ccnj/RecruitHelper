package patrol

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

// 钉住错误分类表（2026-07-27 立案，2026-08-02 反转默认方向，2026-08-14 补全
// 证词缺席语义）：账号级信号全停；手侧命令按协议 retryable 声明分流，隔离
// 必须有正面证词——显式 no/manualOnly 才隔离（可能发生在 effect 派发后，是
// 手的协议级"需要人"证词）；证词缺席是未知，与脑侧、未知错误同走默认方向：
// 本轮跳过、下轮重读；脑侧取消（CANCELED_BY_BRAIN）是环境事件，特判跳过。
func TestClassifyConversationFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want conversationFailureScope
	}{
		{"账号身份不符", &RunError{Code: protocol.ErrCodeAccountMismatch}, failureScopeRoundFatal},
		{"登录丢失", &RunError{
			Code: protocol.ErrCodeCtxNotReady, Reason: protocol.NotReadyReasonLoginRequired,
		}, failureScopeRoundFatal},
		{"身份不可确证", &RunError{
			Code: protocol.ErrCodeCtxNotReady, Reason: protocol.NotReadyReasonIdentityUnverified,
		}, failureScopeRoundFatal},
		{"真人活动让位", &RunError{Code: protocol.ErrCodeUserActive}, failureScopeRoundFatal},
		{"日窗口到期", ErrDailyWindowExpired, failureScopeRoundFatal},
		{"账号已暂停", ErrActorPaused, failureScopeRoundFatal},
		{"代际变化", ErrActorGenerationChanged, failureScopeRoundFatal},
		{"采集批次换代", ErrRoundSupersededBySourcingBatch, failureScopeRoundFatal},
		{"context 取消", context.Canceled, failureScopeRoundFatal},
		{"目标暂离窗口", &RunError{
			Code: protocol.ErrCodeTargetNotFound, Retryable: protocol.RetryableNo,
		}, failureScopeSkipRound},
		{"只读后置未确认", &RunError{
			Code: protocol.ErrCodePostconditionUnconfirmed, SideEffect: protocol.SideEffectPossible,
		}, failureScopeSkipRound},
		{"手声明可重试", &RunError{
			Code: protocol.ErrCodeElementUnresolved, Retryable: protocol.RetryableYes,
		}, failureScopeSkipRound},
		{"手声明恢复后可重试", &RunError{
			Code: protocol.ErrCodeCtxNotReady, Reason: protocol.NotReadyReasonPageAbsent,
			Retryable: protocol.RetryableAfterRecovery,
		}, failureScopeSkipRound},
		{"手声明重试无用", &RunError{
			Code: protocol.ErrCodeElementUnresolved, Retryable: protocol.RetryableNo,
		}, failureScopeQuarantine},
		{"手声明只准人工", &RunError{
			Code: protocol.ErrCodeInternalHand, Retryable: protocol.RetryableManualOnly,
		}, failureScopeQuarantine},
		// 2026-08-14 甲方裁决:隔离必须有正面证词。证词缺席(retryable 空)是
		// 未知,走未知的默认——本轮跳过。此前空串落进隔离默认分支,2026-08-13
		// 一次 4 分钟页面卡顿(openConversation expired)把候选人永久冻结。
		{"证词缺席的手侧错误", &RunError{
			Code: protocol.ErrCodeElementUnresolved,
		}, failureScopeSkipRound},
		{"证词缺席的超时", &RunError{
			Code: protocol.ErrCodeExecTimeoutHand,
		}, failureScopeSkipRound},
		// 合成层按契约把 effectful 超时解析成 manualOnly,隔离经证词达成,
		// 不依赖默认分支——这条钉住危险方向没有随反转放松。
		{"effectful 超时带契约证词", &RunError{
			Code: protocol.ErrCodeExecTimeoutHand, Retryable: protocol.RetryableManualOnly,
		}, failureScopeQuarantine},
		// 脑取消自己的命令是环境事件;契约 retryable=no 是命令级语义,不构成
		// 人级隔离证词。
		{"脑侧取消特判跳过", &RunError{
			Code: protocol.ErrCodeCanceledByBrain, Retryable: protocol.RetryableNo,
		}, failureScopeSkipRound},
		{"已收编空快照矛盾", syncledger.ErrTrackedSnapshotEmpty, failureScopeSkipRound},
		// 2026-08-02 反转：脑侧与未知错误默认跳过。它们全部发生在世界未被
		// 改动、或重派已被 WAL/idemKey/动作状态机结构性挡住的位置；隔离换来
		// 的是"安静但永久"的冻结（08-01 事故一次配置换代冻 79 人是其代表作）。
		{"源身份冲突", syncledger.ErrSourceKeySemanticConflict, failureScopeSkipRound},
		{"store 等值键冲突", store.ErrMessageSourceKeyConflict, failureScopeSkipRound},
		{"不安全修正", syncledger.ErrUnsafeMessageClassificationCorrection, failureScopeSkipRound},
		{"V4 投影冲突", store.ErrCommunicationV4Conflict, failureScopeSkipRound},
		{"V4 投影损坏", store.ErrCommunicationV4Corrupt, failureScopeSkipRound},
		{"AI 预留冲突", store.ErrAIInvocationConflict, failureScopeSkipRound},
		{"未知脑侧错误", errors.New("some brain bug"), failureScopeSkipRound},
	}
	for _, testCase := range cases {
		if got := classifyConversationFailure(testCase.err); got != testCase.want {
			t.Fatalf("%s: classify=%v want=%v", testCase.name, got, testCase.want)
		}
	}
	if class := conversationFailureClass(&RunError{
		Code: protocol.ErrCodeInternalHand, Retryable: protocol.RetryableManualOnly,
	}); class != "hand:INTERNAL_HAND" {
		t.Fatalf("手侧类别标签错误: %s", class)
	}
	if class := conversationFailureClass(syncledger.ErrTrackedSnapshotEmpty); class != "trackedSnapshotEmpty" {
		t.Fatalf("已收编空快照类别标签错误: %s", class)
	}
	// 具名分类：事故当日 79 条隔离审计全部只写着 unclassified，看不出是什么。
	if class := conversationFailureClass(store.ErrAIInvocationConflict); class != "aiInvocationConflict" {
		t.Fatalf("AI 预留冲突类别标签错误: %s", class)
	}
	if class := conversationFailureClass(store.ErrMessageSourceKeyConflict); class != "sourceIdentityConflict" {
		t.Fatalf("脑侧类别标签错误: %s", class)
	}
	if class := conversationFailureClass(errors.New("x")); class != "unclassified" {
		t.Fatalf("未知类别标签错误: %s", class)
	}
}

// 三个候选人中间一个确定性失败：他被隔离，前后两人照常投影，轮正常收尾。
func TestPatrolRoundContinuesPastQuarantinedConversation(t *testing.T) {
	h := newHarness(t)
	first := seedTracked(t, h, "conversation-iso-a", "peer-iso-a", []store.MessageDraft{draftText("old-a")})
	poisoned := seedTracked(t, h, "conversation-iso-b", "peer-iso-b", []store.MessageDraft{draftText("old-b")})
	third := seedTracked(t, h, "conversation-iso-c", "peer-iso-c", []store.MessageDraft{draftText("old-c")})

	threadFor := func(oldText, newText, peerRef string) protocol.ChatReadThreadData {
		return protocol.ChatReadThreadData{
			Messages: []protocol.ThreadMessage{threadText(0, oldText), threadText(1, newText)},
			Peer:     ptr(protocol.PeerSummary{DisplayName: "候选人", PlatformUserRef: peerRef}),
			Complete: true, AnchorMatched: true,
		}
	}
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary(first.ConversationRef, "peer-iso-a", "new-a", 1),
					summary(poisoned.ConversationRef, "peer-iso-b", "new-b", 1),
					summary(third.ConversationRef, "peer-iso-c", "new-c", 1),
				},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
			switch args.ConversationRef {
			case first.ConversationRef:
				return threadFor("old-a", "new-a", "peer-iso-a"), nil
			case poisoned.ConversationRef:
				return nil, &RunError{
					Code: protocol.ErrCodeInternalHand, Retryable: protocol.RetryableManualOnly,
					SideEffect: protocol.SideEffectPossible, Cause: errors.New("单人确定性异常"),
				}
			case third.ConversationRef:
				return threadFor("old-c", "new-c", "peer-iso-c"), nil
			}
			t.Fatalf("未知 readThread 目标: %s", args.ConversationRef)
			return nil, nil
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil ||
		result.Rounds[0].Status != "ok" {
		t.Fatalf("中间人失败不得停轮: result=%+v err=%v", result, err)
	}
	if len(result.Rounds[0].Projections) != 2 {
		t.Fatalf("前后两人必须照常投影: %+v", result.Rounds[0].Projections)
	}
	for _, key := range []store.ConversationKey{first, third} {
		messages, err := h.db.MessagesForConversation(key)
		if err != nil || len(messages) != 2 {
			t.Fatalf("健康候选人 %s 未推进: messages=%+v err=%v", key.ConversationRef, messages, err)
		}
	}
	conversation, err := h.db.ConversationByKey(poisoned)
	if err != nil || conversation == nil || conversation.PatrolQuarantinedAt == nil {
		t.Fatalf("失败者必须被隔离: conversation=%+v err=%v", conversation, err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account.StoppedAt != nil || account.PausedReason != "" {
		t.Fatalf("账号不得被暂停: account=%+v err=%v", account, err)
	}
}

// 聚合投影正处于漂移（档案已推进而聚合尚未投影，加载器报"损坏"）的候选人
// 确定性失败：会话级隔离照常生效，冻结聚合按 best-effort 容忍失败
// （profileFrozen=false），坏聚合本身留给人工——绝不因此把整轮打死。
func TestQuarantineToleratesCorruptAggregateProjection(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5AdviceFixture(t, h)
	conversationKey := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: fixture.conversationRef,
	}
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary(fixture.conversationRef, "person-m5-advice", "毒化预览", 1),
				},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			return nil, &RunError{
				Code: protocol.ErrCodeInternalHand, Retryable: protocol.RetryableManualOnly,
				SideEffect: protocol.SideEffectPossible, Cause: errors.New("单人确定性异常"),
			}
		default:
			return defaultHandler(request)
		}
	}

	// 前置自证：该 fixture 的聚合确实处于投影漂移（加载器报损坏）。
	if _, aggErr := h.db.CommunicationV4AggregateByProfile(fixture.profileID); aggErr == nil {
		// 聚合尚未创建也可接受（Missing 同属容忍分支），但若可正常加载则
		// 本测试失去意义。
		t.Fatal("fixture 聚合意外可正常加载，测试前提失效")
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("坏聚合候选人失败不得停轮: result=%+v err=%v", result, err)
	}
	conversation, err := h.db.ConversationByKey(conversationKey)
	if err != nil || conversation == nil || conversation.PatrolQuarantinedAt == nil {
		t.Fatalf("会话必须被隔离: conversation=%+v err=%v", conversation, err)
	}
	audits, err := h.db.AuditEntries(20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, audit := range audits {
		if audit.Category == patrolQuarantineAuditCategory &&
			audit.ConversationRef == fixture.conversationRef &&
			strings.Contains(audit.Detail, "profileFrozen=false") {
			found = true
		}
	}
	if !found {
		t.Fatal("隔离审计必须如实记录聚合未能冻结")
	}
}

// 有一致聚合的候选人（洪建辉案的真实形状）确定性失败：会话打隔离标记的
// 同时，V4 聚合必须挂 manualRequired，审计记 profileFrozen=true。
func TestQuarantineFreezesProfileAggregateWhenPresent(t *testing.T) {
	h := newHarness(t)
	profileID := seedGreetedProfileWithV4Root(t, h, "conversation-freeze", "peer-freeze")
	conversationKey := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: "conversation-freeze",
	}
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary("conversation-freeze", "peer-freeze", "毒化预览", 1),
				},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			return nil, &RunError{
				Code: protocol.ErrCodeInternalHand, Retryable: protocol.RetryableManualOnly,
				SideEffect: protocol.SideEffectPossible, Cause: errors.New("单人确定性异常"),
			}
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("有档案候选人失败不得停轮: result=%+v err=%v", result, err)
	}
	conversation, err := h.db.ConversationByKey(conversationKey)
	if err != nil || conversation == nil || conversation.PatrolQuarantinedAt == nil {
		t.Fatalf("会话必须被隔离: conversation=%+v err=%v", conversation, err)
	}
	aggregate, err := h.db.CommunicationV4AggregateByProfile(profileID)
	if err != nil || aggregate.AutomationStatus != store.ProfileCommunicationAutomationManualRequired ||
		aggregate.ManualReason != "patrolQuarantine:hand:INTERNAL_HAND" {
		t.Fatalf("V4 聚合必须挂 manualRequired: aggregate=%+v err=%v", aggregate, err)
	}
	audits, err := h.db.AuditEntries(20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, audit := range audits {
		if audit.Category == patrolQuarantineAuditCategory &&
			audit.ConversationRef == "conversation-freeze" &&
			strings.Contains(audit.Detail, "profileFrozen=true") {
			found = true
		}
	}
	if !found {
		t.Fatal("隔离审计必须记录聚合已冻结")
	}
}

// seedGreetedProfileWithV4Root 建一个"已招呼、无入站、聚合与档案投影一致"
// 的候选人：走真实招呼链（intent→verification→服务端正证），聚合由轮内
// ensureCommunicationV4Roots 现建即可保持一致。
func seedGreetedProfileWithV4Root(
	t *testing.T,
	h *harness,
	conversationRef string,
	platformUserRef string,
) string {
	t.Helper()
	now := h.clock.Now()
	profileID := "profile-" + conversationRef
	displayName, positionTitle := "合成候选人", "合成职位"
	if _, err := h.db.SelectCandidateProfile(store.SelectCandidateProfileRequest{
		ProfileID: profileID,
		Scope: store.CandidateProfileScope{
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			PlatformUserRef: platformUserRef, PositionRef: "position-" + conversationRef,
		},
		DisplayName: &displayName, PositionTitle: &positionTitle, ObservedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	greetingIntent := "intent-" + conversationRef
	greetingText := "合成招呼"
	greetingHash := syncledger.HashText(greetingText)
	deadline := now.Add(time.Hour).UnixMilli()
	greeting, err := h.db.CreateGreetingEffectIntentAndCmd(store.CreateGreetingEffectIntentRequest{
		Intent: store.EffectIntent{
			IntentID: greetingIntent, IdemKey: "idem-" + conversationRef,
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			Primitive: protocol.PrimChatSendGreeting, TargetRef: profileID,
			PayloadHash: "payload-" + conversationRef, GuardsHash: "guards-" + conversationRef,
			SendFingerprint: greetingHash, Status: store.EffectIntentDispatching, DeadlineMs: deadline,
		},
		Command: store.CmdRecord{
			MsgID: "msg-" + conversationRef, Name: protocol.PrimChatSendGreeting,
			Class: string(protocol.ClassEffectful), IdemKey: "idem-" + conversationRef,
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
	if err := h.db.MoveEffectToVerification(greeting.Command.MsgID, "fixturePositiveRead", now); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.ResolveGreetingVerified(store.VerifiedGreetingSuccess{
		Ref: greeting.Command.MsgID, ProfileID: profileID, PlatformUserRef: platformUserRef,
		PositionRef: "position-" + conversationRef, ConversationRef: conversationRef,
		Text: greetingText, ContentHash: greetingHash, ObservedAtMs: now.UnixMilli(),
		ResolutionReason: "fixturePositiveRead", At: now,
	}); err != nil {
		t.Fatal(err)
	}
	return profileID
}

// 瞬时错误（目标暂离窗口）：本轮跳过并留汇总审计，下一轮自然重试；不隔离。
func TestPatrolRoundTransientSkipLeavesTraceAndRetriesNextRound(t *testing.T) {
	h := newHarness(t)
	flaky := seedTracked(t, h, "conversation-transient", "peer-transient", []store.MessageDraft{draftText("old")})
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary(flaky.ConversationRef, "peer-transient", "new", 1),
				},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			return nil, &RunError{
				Code: protocol.ErrCodeTargetNotFound, Retryable: protocol.RetryableNo,
				SideEffect: protocol.SideEffectNone,
			}
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("瞬时错误不得停轮: result=%+v err=%v", result, err)
	}
	conversation, err := h.db.ConversationByKey(flaky)
	if err != nil || conversation == nil || conversation.PatrolQuarantinedAt != nil {
		t.Fatalf("瞬时错误不得隔离: conversation=%+v err=%v", conversation, err)
	}
	audits, err := h.db.AuditEntries(20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, audit := range audits {
		if audit.Category != patrolTransientSkipAuditCategory {
			continue
		}
		found = true
		if !strings.Contains(audit.Detail, "count=1") ||
			!strings.Contains(audit.Detail, flaky.ConversationRef+":hand:TARGET_NOT_FOUND") {
			t.Fatalf("瞬时跳过汇总内容错误: %+v", audit)
		}
	}
	if !found {
		t.Fatal("瞬时跳过必须留下轮收尾汇总审计")
	}

	// 下一轮自然重试（listHint 未核实，行仍脏）。
	readThreadCount := h.runner.count(protocol.PrimChatReadThread)
	h.clock.Add(h.config.PatrolInterval + time.Minute)
	next, err := h.manager.Tick(context.Background())
	if err != nil || len(next.Rounds) != 1 ||
		h.runner.count(protocol.PrimChatReadThread) != readThreadCount+1 {
		t.Fatalf("瞬时错误下一轮必须自然重试: next=%+v err=%v calls=%v", next, err, h.runner.names())
	}
}
