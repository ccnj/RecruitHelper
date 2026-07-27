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

// 钉住 2026-07-27 甲方裁决的错误分类表：账号级信号全停；手侧命令按协议
// retryable 声明分瞬时/确定性；脑侧与未知错误一律确定性隔离。
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
		{"源身份冲突", syncledger.ErrSourceKeySemanticConflict, failureScopeQuarantine},
		{"store 等值键冲突", store.ErrMessageSourceKeyConflict, failureScopeQuarantine},
		{"不安全修正", syncledger.ErrUnsafeMessageClassificationCorrection, failureScopeQuarantine},
		{"V4 投影冲突", store.ErrCommunicationV4Conflict, failureScopeQuarantine},
		{"未知脑侧错误", errors.New("some brain bug"), failureScopeQuarantine},
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
