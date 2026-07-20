package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

type recordingGreetingVerifier struct {
	mu          sync.Mutex
	calls       int
	requests    []VerificationRequest
	observation VerificationObservation
	err         error
	started     chan struct{}
	release     <-chan struct{}
	before      func(VerificationRequest) error
}

func (v *recordingGreetingVerifier) Verify(
	ctx context.Context,
	req VerificationRequest,
) (VerificationObservation, error) {
	v.mu.Lock()
	v.calls++
	v.requests = append(v.requests, req)
	v.mu.Unlock()
	if v.before != nil {
		if err := v.before(req); err != nil {
			return VerificationObservation{}, err
		}
	}
	if v.started != nil {
		select {
		case v.started <- struct{}{}:
		default:
		}
	}
	if v.release != nil {
		select {
		case <-v.release:
		case <-ctx.Done():
			return VerificationObservation{}, ctx.Err()
		}
	}
	return v.observation, v.err
}

func (v *recordingGreetingVerifier) snapshot() (int, []VerificationRequest) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.calls, append([]VerificationRequest(nil), v.requests...)
}

func awaitVerifierCall(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("等待真人触发的招呼正证读取超时")
	}
}

func sentPrimitiveCount(t *testing.T, m *mockSender, name string) int {
	t.Helper()
	m.mu.Lock()
	sent := append([]protocol.Envelope(nil), m.sent...)
	m.mu.Unlock()
	count := 0
	for _, envelope := range sent {
		if envelope.Kind != protocol.KindCmd {
			continue
		}
		var body protocol.CmdBody
		if err := json.Unmarshal(envelope.Body, &body); err != nil {
			t.Fatalf("解析测试命令信封: %v", err)
		}
		if body.Name == name {
			count++
		}
	}
	return count
}

func TestGreetingResolvedOKVerdictRequiresOneFreshPositiveRead(t *testing.T) {
	d, st, m := newDisp(t)
	fixture := seedGreetingTarget(t, st, m, "manual-positive")
	receipt, err := d.SendGreeting(sendGreetingRequest(fixture, "intent-manual-positive", ""))
	if err != nil {
		t.Fatal(err)
	}
	makeEffectSuspectReviewable(t, d, st, receipt.MsgID, time.Now().Add(-time.Second))
	before, _ := st.CmdByMsgID(receipt.MsgID)
	started := make(chan struct{}, 1)
	verifier := &recordingGreetingVerifier{
		started: started,
		observation: VerificationObservation{
			Confirmed: true, ContentHash: syncledger.HashText(fixture.GreetingText),
			ConversationRef: fixture.ConversationRef, ObservedAt: time.Now().UnixMilli(),
		},
	}
	d.SetEffectVerifier(verifier)

	if err := d.Verdict(receipt.MsgID, store.CmdResolvedOk); err != nil {
		t.Fatalf("真人 resolvedOk 应触发一次正证读取: %v", err)
	}
	awaitVerifierCall(t, started)
	waitGreetingCondition(t, func() bool {
		cmd, _ := st.CmdByMsgID(receipt.MsgID)
		return cmd != nil && cmd.Status == store.CmdOk
	})

	calls, requests := verifier.snapshot()
	if calls != 1 || len(requests) != 1 {
		t.Fatalf("真人操作必须恰好触发一次验证读: calls=%d requests=%d", calls, len(requests))
	}
	request := requests[0]
	if request.Command.Name != protocol.PrimChatSendGreeting || request.GreetingArgs == nil ||
		request.GreetingArgs.PlatformUserRef != fixture.PlatformUserRef ||
		request.GreetingArgs.PositionRef != fixture.PositionRef ||
		request.Intent.TargetRef != fixture.ProfileID {
		t.Fatalf("招呼正证读取材料错误，ProfileID 不得冒充 conversationRef: %+v", request)
	}
	after, _ := st.CmdByMsgID(receipt.MsgID)
	if after.VerificationN != before.VerificationN {
		t.Fatalf("真人正证读取不得重置或增加三轮自动预算: before=%d after=%d",
			before.VerificationN, after.VerificationN)
	}
	assertGreetingSuccess(t, st, fixture, receipt.IntentID)
}

func TestGreetingResolvedOKNegativeOrReadFailureStaysFrozenAfterOneRead(t *testing.T) {
	tests := []struct {
		name        string
		observation VerificationObservation
		err         error
	}{
		{name: "negative", observation: VerificationObservation{Reason: "未取得唯一招呼正证"}},
		{name: "read failure", err: errors.New("读取失败")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, st, m := newDisp(t)
			fixture := seedGreetingTarget(t, st, m, "manual-miss-"+tc.name)
			receipt, err := d.SendGreeting(sendGreetingRequest(fixture, "intent-manual-miss-"+tc.name, ""))
			if err != nil {
				t.Fatal(err)
			}
			makeEffectSuspectReviewable(t, d, st, receipt.MsgID, time.Now().Add(-time.Second))
			before, _ := st.CmdByMsgID(receipt.MsgID)
			started := make(chan struct{}, 1)
			verifier := &recordingGreetingVerifier{
				started: started, observation: tc.observation, err: tc.err,
			}
			d.SetEffectVerifier(verifier)

			if err := d.Verdict(receipt.MsgID, store.CmdResolvedOk); err != nil {
				t.Fatal(err)
			}
			awaitVerifierCall(t, started)
			waitGreetingCondition(t, func() bool {
				cmd, _ := st.CmdByMsgID(receipt.MsgID)
				return cmd != nil && cmd.Status == store.CmdSuspect &&
					cmd.VerificationReason != manualGreetingVerdictVerificationReason
			})

			cmd, _ := st.CmdByMsgID(receipt.MsgID)
			intent, _ := st.EffectIntentByID(receipt.IntentID)
			if cmd.VerificationN != before.VerificationN || !cmd.ReviewReady ||
				cmd.VerificationNextAt != nil || intent == nil || intent.Status != store.EffectIntentSuspect {
				t.Fatalf("阴性/失败必须原地回到 review-ready suspect 且零自动再试: cmd=%+v intent=%+v", cmd, intent)
			}
			profile, _ := st.CandidateProfileByID(fixture.ProfileID)
			if profile == nil || profile.MainStatus != store.CandidateProfileSelected {
				t.Fatalf("阴性读取不得推进档案: %+v", profile)
			}
			assertNoGreetingConversation(t, st, fixture)
			if sentPrimitiveCount(t, m, protocol.PrimChatSendGreeting) != 1 {
				t.Fatal("真人正证读取不得授权第二次招呼动作")
			}
			if next, nextErr := d.SendGreeting(sendGreetingRequest(
				fixture, "next-manual-miss-"+tc.name, receipt.IntentID,
			)); next != nil || !errors.Is(nextErr, store.ErrCandidateGreetingFrozen) {
				t.Fatalf("阴性后原 intent 必须继续冻结: receipt=%+v err=%v", next, nextErr)
			}
			calls, _ := verifier.snapshot()
			if calls != 1 {
				t.Fatalf("阴性/失败不得自动循环验证: calls=%d", calls)
			}
		})
	}
}

func TestGreetingManualReadTimeoutWithPendingChildDoesNotDefer(t *testing.T) {
	d, st, m := newDisp(t)
	fixture := seedGreetingTarget(t, st, m, "manual-pending-child")
	receipt, err := d.SendGreeting(sendGreetingRequest(fixture, "intent-manual-pending-child", ""))
	if err != nil {
		t.Fatal(err)
	}
	makeEffectSuspectReviewable(t, d, st, receipt.MsgID, time.Now().Add(-time.Second))
	before, _ := st.CmdByMsgID(receipt.MsgID)
	started := make(chan struct{}, 1)
	verifier := &recordingGreetingVerifier{
		started: started,
		before: func(req VerificationRequest) error {
			return st.CreateVerificationCmd(req.Command.MsgID, &store.CmdRecord{
				MsgID: "manual-pending-child", Name: protocol.PrimChatReadGreetingOutcome,
				Class: "intrusive", Domain: req.Command.Domain,
				Platform: req.Command.Platform, AccountRef: req.Command.AccountRef,
				ExpectedPrincipalFingerprint: req.Command.ExpectedPrincipalFingerprint,
				ContextJSON:                  req.Command.ContextJSON, Args: `{}`, Guards: `{}`,
				HandID: req.Command.HandID, Session: req.Command.Session,
				BootIDAtDispatch: req.Command.BootIDAtDispatch, Status: store.CmdSent,
				DeadlineMs: time.Now().Add(time.Minute).UnixMilli(), ExecBudgetMs: 1000,
				VerificationForMsgID: req.Command.MsgID,
			})
		},
		err: context.DeadlineExceeded,
	}
	d.SetEffectVerifier(verifier)

	if err := d.Verdict(receipt.MsgID, store.CmdResolvedOk); err != nil {
		t.Fatal(err)
	}
	awaitVerifierCall(t, started)
	waitGreetingCondition(t, func() bool {
		cmd, _ := st.CmdByMsgID(receipt.MsgID)
		return cmd != nil && cmd.Status == store.CmdSuspect
	})
	cmd, _ := st.CmdByMsgID(receipt.MsgID)
	if cmd.VerificationNextAt != nil || cmd.VerificationN != before.VerificationN || !cmd.ReviewReady {
		t.Fatalf("pending child 超时也不得 defer/增加自动轮次: %+v", cmd)
	}
	calls, _ := verifier.snapshot()
	if calls != 1 {
		t.Fatalf("pending child 超时后不得自动再读: calls=%d", calls)
	}
}

func TestGreetingResolvedFailedVerdictDoesNotTouchConversationLedger(t *testing.T) {
	d, st, m := newDisp(t)
	fixture := seedGreetingTarget(t, st, m, "manual-failed")
	receipt, err := d.SendGreeting(sendGreetingRequest(fixture, "intent-manual-failed", ""))
	if err != nil {
		t.Fatal(err)
	}
	makeEffectSuspectReviewable(t, d, st, receipt.MsgID, time.Now().Add(-time.Second))

	if err := d.Verdict(receipt.MsgID, store.CmdResolvedFailed); err != nil {
		t.Fatal(err)
	}
	cmd, _ := st.CmdByMsgID(receipt.MsgID)
	intent, _ := st.EffectIntentByID(receipt.IntentID)
	profile, _ := st.CandidateProfileByID(fixture.ProfileID)
	if cmd == nil || cmd.Status != store.CmdResolvedFailed || intent == nil ||
		intent.Status != store.EffectIntentResolvedFailed || intent.ResultConversationRef != nil ||
		intent.ResultMessageSeq != nil || profile == nil || profile.MainStatus != store.CandidateProfileSelected {
		t.Fatalf("resolvedFailed 只能终结招呼 Cmd/Intent 并保留 selected: cmd=%+v intent=%+v profile=%+v",
			cmd, intent, profile)
	}
	assertNoGreetingConversation(t, st, fixture)
}

func TestGreetingVerdictAndLateResultRaceKeepsAuthoritativeResult(t *testing.T) {
	t.Run("late ok wins resolvedFailed", func(t *testing.T) {
		d, st, m := newDisp(t)
		fixture := seedGreetingTarget(t, st, m, "late-ok-after-failed")
		receipt, err := d.SendGreeting(sendGreetingRequest(fixture, "intent-late-ok-after-failed", ""))
		if err != nil {
			t.Fatal(err)
		}
		makeEffectSuspectReviewable(t, d, st, receipt.MsgID, time.Now().Add(-time.Second))
		if err := d.Verdict(receipt.MsgID, store.CmdResolvedFailed); err != nil {
			t.Fatal(err)
		}
		if _, _, err := d.applyResultMessage(
			fixture.HandID, "late-ok-after-failed", validGreetingResult(receipt.MsgID, fixture),
		); err != nil {
			t.Fatal(err)
		}
		assertGreetingSuccess(t, st, fixture, receipt.IntentID)
	})

	t.Run("late ok wins in-flight manual read", func(t *testing.T) {
		d, st, m := newDisp(t)
		fixture := seedGreetingTarget(t, st, m, "late-ok-during-read")
		receipt, err := d.SendGreeting(sendGreetingRequest(fixture, "intent-late-ok-during-read", ""))
		if err != nil {
			t.Fatal(err)
		}
		makeEffectSuspectReviewable(t, d, st, receipt.MsgID, time.Now().Add(-time.Second))
		started := make(chan struct{}, 1)
		release := make(chan struct{})
		verifier := &recordingGreetingVerifier{
			started: started, release: release,
			observation: VerificationObservation{Reason: "迟到 result 已先收束"},
		}
		d.SetEffectVerifier(verifier)
		if err := d.Verdict(receipt.MsgID, store.CmdResolvedOk); err != nil {
			t.Fatal(err)
		}
		awaitVerifierCall(t, started)
		if _, _, err := d.applyResultMessage(
			fixture.HandID, "late-ok-during-read", validGreetingResult(receipt.MsgID, fixture),
		); err != nil {
			t.Fatal(err)
		}
		close(release)
		waitGreetingCondition(t, func() bool {
			cmd, _ := st.CmdByMsgID(receipt.MsgID)
			return cmd != nil && cmd.Status == store.CmdOk
		})
		assertGreetingSuccess(t, st, fixture, receipt.IntentID)
	})

	t.Run("late possible cannot turn manual read into auto retry", func(t *testing.T) {
		d, st, m := newDisp(t)
		fixture := seedGreetingTarget(t, st, m, "late-possible-during-read")
		receipt, err := d.SendGreeting(sendGreetingRequest(fixture, "intent-late-possible-during-read", ""))
		if err != nil {
			t.Fatal(err)
		}
		makeEffectSuspectReviewable(t, d, st, receipt.MsgID, time.Now().Add(-time.Second))
		before, _ := st.CmdByMsgID(receipt.MsgID)
		started := make(chan struct{}, 1)
		release := make(chan struct{})
		verifier := &recordingGreetingVerifier{
			started: started, release: release,
			observation: VerificationObservation{Reason: "本轮仍无唯一正证"},
		}
		d.SetEffectVerifier(verifier)
		if err := d.Verdict(receipt.MsgID, store.CmdResolvedOk); err != nil {
			t.Fatal(err)
		}
		awaitVerifierCall(t, started)
		outcome, _, err := d.applyResultMessage(
			fixture.HandID, "late-possible-during-read",
			failedGreetingResult(receipt.MsgID, protocol.ErrCodePostconditionUnconfirmed, protocol.SideEffectPossible),
		)
		if err != nil || outcome != ocHumanVerdictKept {
			t.Fatalf("迟到 possible 应保留真人单次读取而非另开自动验证: outcome=%v err=%v", outcome, err)
		}
		inFlight, _ := st.CmdByMsgID(receipt.MsgID)
		if !isGreetingManualVerdictVerification(*inFlight) {
			t.Fatalf("迟到 possible 不得覆盖真人读取标记: %+v", inFlight)
		}
		close(release)
		waitGreetingCondition(t, func() bool {
			cmd, _ := st.CmdByMsgID(receipt.MsgID)
			return cmd != nil && cmd.Status == store.CmdSuspect
		})
		after, _ := st.CmdByMsgID(receipt.MsgID)
		calls, _ := verifier.snapshot()
		if calls != 1 || after.VerificationN != before.VerificationN || after.VerificationNextAt != nil {
			t.Fatalf("迟到 possible 后仍须恰好一次且零自动 defer: calls=%d cmd=%+v", calls, after)
		}
		assertNoGreetingConversation(t, st, fixture)
	})

	t.Run("result first blocks failed verdict", func(t *testing.T) {
		d, st, m := newDisp(t)
		fixture := seedGreetingTarget(t, st, m, "result-first")
		receipt, err := d.SendGreeting(sendGreetingRequest(fixture, "intent-result-first-greeting", ""))
		if err != nil {
			t.Fatal(err)
		}
		makeEffectSuspectReviewable(t, d, st, receipt.MsgID, time.Now().Add(-time.Second))
		if _, _, err := d.applyResultMessage(
			fixture.HandID, "result-first-greeting", validGreetingResult(receipt.MsgID, fixture),
		); err != nil {
			t.Fatal(err)
		}
		if err := d.Verdict(receipt.MsgID, store.CmdResolvedFailed); !errors.Is(err, ErrNotSuspect) {
			t.Fatalf("权威 result 先落账后人工失败裁决必须输: %v", err)
		}
		assertGreetingSuccess(t, st, fixture, receipt.IntentID)
	})
}

func TestMessageResolvedOKVerdictKeepsM3ConversationSemantics(t *testing.T) {
	d, st, m := newDisp(t)
	key := seedSendTarget(t, st, m, "acct-m3-verdict-regression", "conv-m3-verdict-regression")
	receipt, err := d.SendMessage(sendRequest("intent-m3-verdict-regression", key, "测试正文"))
	if err != nil {
		t.Fatal(err)
	}
	makeEffectSuspectReviewable(t, d, st, receipt.MsgID, time.Now().Add(-time.Second))
	if err := d.Verdict(receipt.MsgID, store.CmdResolvedOk); err != nil {
		t.Fatal(err)
	}
	messages, err := st.MessagesForConversation(key)
	if err != nil || len(messages) != 2 || messages[1].OutboundIntentID == nil ||
		*messages[1].OutboundIntentID != receipt.IntentID {
		t.Fatalf("M3 resolvedOk 必须继续按 conversationRef 追加唯一 self 消息: messages=%+v err=%v",
			messages, err)
	}
}
