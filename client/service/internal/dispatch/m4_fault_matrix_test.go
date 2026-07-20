package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

type greetingDispatchFixture struct {
	ProfileID       string
	Platform        string
	AccountRef      string
	PlatformUserRef string
	PositionRef     string
	HandID          string
	ConversationRef string
	GreetingText    string
}

func seedGreetingTarget(
	t *testing.T,
	st *store.Store,
	m *mockSender,
	slug string,
) greetingDispatchFixture {
	t.Helper()
	fixture := greetingDispatchFixture{
		ProfileID:       "profile-" + slug,
		Platform:        "zhilian",
		AccountRef:      "account-" + slug,
		PlatformUserRef: "person-" + slug,
		PositionRef:     "position-" + slug,
		HandID:          "hand-greeting",
		ConversationRef: "conversation-" + slug,
		GreetingText:    "测试招呼",
	}
	const bootID = "boot-greeting"
	m.up(fixture.HandID, bootID)
	m.negotiate(fixture.HandID, []string{
		protocol.PrimChatSendGreeting + "@1",
		protocol.PrimChatReadGreetingOutcome + "@1",
	}, append(append([]string(nil), allM2Features...), string(protocol.FeatureWitness1)))
	m.mu.Lock()
	m.witness[fixture.HandID] = HandWitness{StoreID: "witness-greeting"}
	m.mu.Unlock()
	if err := st.CreateAccount(&store.Account{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccountPrincipal(
		store.AccountKey{Platform: fixture.Platform, AccountRef: fixture.AccountRef},
		fixture.HandID, "principal-"+slug, "s-test", bootID, time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	displayName := "候选人"
	positionTitle := "职位"
	if _, err := st.SelectCandidateProfile(store.SelectCandidateProfileRequest{
		ProfileID: fixture.ProfileID,
		Scope: store.CandidateProfileScope{
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			PlatformUserRef: fixture.PlatformUserRef, PositionRef: fixture.PositionRef,
		},
		DisplayName: &displayName, PositionTitle: &positionTitle, ObservedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func sendGreetingRequest(fixture greetingDispatchFixture, intentID, previousIntentID string) SendGreetingRequest {
	return SendGreetingRequest{
		IntentID: intentID, PreviousIntentID: previousIntentID,
		ProfileID: fixture.ProfileID, Text: fixture.GreetingText,
	}
}

func validGreetingResult(ref string, fixture greetingDispatchFixture) protocol.ResultBody {
	data, _ := protocol.Encode(protocol.ChatSendGreetingData{
		PlatformUserRef: fixture.PlatformUserRef,
		PositionRef:     fixture.PositionRef,
		ConversationRef: fixture.ConversationRef,
		ContentHash:     syncledger.HashText(fixture.GreetingText),
		ObservedAt:      time.Now().UnixMilli(),
	})
	return protocol.ResultBody{
		Ref: ref, Status: protocol.ResultStatusOk, Data: data,
		Evidence: []protocol.Evidence{{Type: string(protocol.SendGreetingEvidenceTypeOutboundGreetingObserved)}},
	}
}

func failedGreetingResult(ref string, code protocol.ErrorCode, sideEffect protocol.SideEffect) protocol.ResultBody {
	return protocol.ResultBody{
		Ref: ref, Status: protocol.ResultStatusFailed,
		Error: &protocol.ErrorBody{
			Code: code, Message: "test failure",
			Retryable: protocol.RetryableNo, SideEffect: sideEffect,
		},
	}
}

func waitGreetingCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("等待招呼故障轨状态超时")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func assertNoGreetingConversation(t *testing.T, st *store.Store, fixture greetingDispatchFixture) {
	t.Helper()
	conversations, err := st.ConversationsForAccount(store.AccountKey{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
	})
	if err != nil || len(conversations) != 0 {
		t.Fatalf("阴性招呼不得制造会话: conversations=%+v err=%v", conversations, err)
	}
}

func assertGreetingSuccess(
	t *testing.T,
	st *store.Store,
	fixture greetingDispatchFixture,
	intentID string,
) {
	t.Helper()
	profile, err := st.CandidateProfileByID(fixture.ProfileID)
	if err != nil || profile == nil || profile.MainStatus != store.CandidateProfileGreeted ||
		profile.SuccessfulGreetingIntentID == nil || *profile.SuccessfulGreetingIntentID != intentID ||
		profile.ConversationRef == nil || *profile.ConversationRef != fixture.ConversationRef ||
		profile.GreetedAt == nil {
		t.Fatalf("招呼成功未完整推进档案: profile=%+v err=%v", profile, err)
	}
	key := store.ConversationKey{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		ConversationRef: fixture.ConversationRef,
	}
	conversation, err := st.ConversationByKey(key)
	if err != nil || conversation == nil || conversation.TrackingState != store.TrackingAdopted ||
		conversation.PlatformUserRef != fixture.PlatformUserRef || conversation.LastMessageSeq != 1 {
		t.Fatalf("招呼成功未建立 adopted 会话: conversation=%+v err=%v", conversation, err)
	}
	tracked, err := st.TrackedIntentByConversation(key)
	if err != nil || tracked == nil || tracked.Status != store.TrackingAdopted ||
		tracked.RequestedBy != "system:greeting" {
		t.Fatalf("招呼成功未建立 adopted 跟踪事实: tracked=%+v err=%v", tracked, err)
	}
	messages, err := st.MessagesForConversation(key)
	if err != nil || len(messages) != 1 || messages[0].Seq != 1 || messages[0].Direction != "out" ||
		messages[0].Origin != "self" || messages[0].OutboundIntentID == nil ||
		*messages[0].OutboundIntentID != intentID {
		t.Fatalf("招呼成功未建立唯一首条 self 消息: messages=%+v err=%v", messages, err)
	}
	intent, err := st.EffectIntentByID(intentID)
	if err != nil || intent == nil || intent.Status != store.EffectIntentOk ||
		intent.ResultConversationRef == nil || *intent.ResultConversationRef != fixture.ConversationRef ||
		intent.ResultMessageSeq == nil || *intent.ResultMessageSeq != 1 {
		t.Fatalf("招呼成功 intent 结果引用不完整: intent=%+v err=%v", intent, err)
	}
}

type fixedGreetingVerifier struct {
	observation VerificationObservation
	err         error
}

func (v fixedGreetingVerifier) Verify(context.Context, VerificationRequest) (VerificationObservation, error) {
	return v.observation, v.err
}

func TestGreetingRejectedIsTheOnlyFailureThatEndsProfile(t *testing.T) {
	tests := []struct {
		name       string
		code       protocol.ErrorCode
		wantStatus store.CandidateProfileStatus
		wantEnded  bool
	}{
		{name: "platform business rejection", code: protocol.ErrCodeGreetingRejected, wantStatus: store.CandidateProfileEnded, wantEnded: true},
		{name: "technical guard failure", code: protocol.ErrCodeGuardFailed, wantStatus: store.CandidateProfileSelected},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, st, m := newDisp(t)
			fixture := seedGreetingTarget(t, st, m, tc.name)
			intentID := "intent-" + tc.name
			receipt, err := d.SendGreeting(sendGreetingRequest(fixture, intentID, ""))
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := d.applyResultMessage(fixture.HandID, "result-"+tc.name,
				failedGreetingResult(receipt.MsgID, tc.code, protocol.SideEffectNone)); err != nil {
				t.Fatal(err)
			}
			profile, err := st.CandidateProfileByID(fixture.ProfileID)
			if err != nil || profile == nil || profile.MainStatus != tc.wantStatus {
				t.Fatalf("失败分流后的档案状态错误: profile=%+v err=%v", profile, err)
			}
			if tc.wantEnded {
				if profile.EndReason == nil || *profile.EndReason != store.CandidateProfileEndGreetingFailed {
					t.Fatalf("明确业务拒绝必须以 greetingFailed 结束: %+v", profile)
				}
			} else if profile.EndReason != nil {
				t.Fatalf("技术失败不得写业务终态: %+v", profile)
			}
			intent, _ := st.EffectIntentByID(intentID)
			cmd, _ := st.CmdByMsgID(receipt.MsgID)
			if intent == nil || intent.Status != store.EffectIntentFailed || cmd == nil || cmd.Status != store.CmdFailed {
				t.Fatalf("失败必须终局化物理命令与原意图: intent=%+v cmd=%+v", intent, cmd)
			}
			assertNoGreetingConversation(t, st, fixture)
			next, nextErr := d.SendGreeting(sendGreetingRequest(fixture, "next-"+intentID, intentID))
			if tc.wantEnded {
				if next != nil || !errors.Is(nextErr, store.ErrCandidateProfileNotSelected) {
					t.Fatalf("明确业务拒绝结束后不得再铸招呼: receipt=%+v err=%v", next, nextErr)
				}
			} else if nextErr != nil || next == nil || !next.Created {
				t.Fatalf("技术失败后应允许真人沿 head 新铸意图: receipt=%+v err=%v", next, nextErr)
			}
		})
	}
}

func TestGreetingPossibleAndConfirmedBothEnterVerification(t *testing.T) {
	tests := []struct {
		name       string
		code       protocol.ErrorCode
		sideEffect protocol.SideEffect
	}{
		{name: "possible", code: protocol.ErrCodePostconditionUnconfirmed, sideEffect: protocol.SideEffectPossible},
		{name: "confirmed without unique evidence", code: protocol.ErrCodeInternalHand, sideEffect: protocol.SideEffectConfirmed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, st, m := newDisp(t)
			fixture := seedGreetingTarget(t, st, m, tc.name)
			release := make(chan struct{})
			d.SetEffectVerifier(blockingVerifier{release: release})
			receipt, err := d.SendGreeting(sendGreetingRequest(fixture, "intent-"+tc.name, ""))
			if err != nil {
				t.Fatal(err)
			}
			d.OnResult(fixture.HandID, "result-"+tc.name,
				failedGreetingResult(receipt.MsgID, tc.code, tc.sideEffect))
			cmd, _ := st.CmdByMsgID(receipt.MsgID)
			intent, _ := st.EffectIntentByID(receipt.IntentID)
			if cmd == nil || cmd.Status != store.CmdVerifying || intent == nil ||
				intent.Status != store.EffectIntentVerifying {
				t.Fatalf("歧义失败必须进入验证轨: cmd=%+v intent=%+v", cmd, intent)
			}
			profile, _ := st.CandidateProfileByID(fixture.ProfileID)
			if profile == nil || profile.MainStatus != store.CandidateProfileSelected {
				t.Fatalf("验证前不得推进档案: %+v", profile)
			}
			close(release)
			waitGreetingCondition(t, func() bool {
				current, _ := st.CmdByMsgID(receipt.MsgID)
				return current != nil && current.VerificationN == 1
			})
			assertNoGreetingConversation(t, st, fixture)
		})
	}
}

func TestGreetingVerificationMatrix(t *testing.T) {
	t.Run("ambiguous and false exhaust to suspect", func(t *testing.T) {
		d, st, m := newDisp(t)
		fixture := seedGreetingTarget(t, st, m, "verify-ambiguous")
		receipt, err := d.SendGreeting(sendGreetingRequest(fixture, "intent-verify-ambiguous", ""))
		if err != nil {
			t.Fatal(err)
		}
		if err := st.MoveEffectToVerification(receipt.MsgID, "test ambiguity", time.Now()); err != nil {
			t.Fatal(err)
		}
		d.SetEffectVerifier(fixedGreetingVerifier{observation: VerificationObservation{
			Confirmed: false, Reason: "发现多条候选会话，不能形成唯一正证",
		}})
		for range protocol.DefaultVerificationMaxRounds {
			d.verifyEffect(context.Background(), receipt.MsgID)
		}
		cmd, _ := st.CmdByMsgID(receipt.MsgID)
		intent, _ := st.EffectIntentByID(receipt.IntentID)
		if cmd == nil || cmd.Status != store.CmdSuspect ||
			cmd.VerificationN != protocol.DefaultVerificationMaxRounds ||
			intent == nil || intent.Status != store.EffectIntentSuspect {
			t.Fatalf("三轮阴性/多义必须停在 suspect: cmd=%+v intent=%+v", cmd, intent)
		}
		profile, _ := st.CandidateProfileByID(fixture.ProfileID)
		if profile == nil || profile.MainStatus != store.CandidateProfileSelected {
			t.Fatalf("阴性验证不得推进档案: %+v", profile)
		}
		assertNoGreetingConversation(t, st, fixture)
	})

	t.Run("confirmed without conversation is still a miss", func(t *testing.T) {
		d, st, m := newDisp(t)
		fixture := seedGreetingTarget(t, st, m, "verify-missing-conversation")
		receipt, err := d.SendGreeting(sendGreetingRequest(fixture, "intent-verify-missing-conversation", ""))
		if err != nil {
			t.Fatal(err)
		}
		if err := st.MoveEffectToVerification(receipt.MsgID, "test", time.Now()); err != nil {
			t.Fatal(err)
		}
		d.SetEffectVerifier(fixedGreetingVerifier{observation: VerificationObservation{
			Confirmed: true, ContentHash: syncledger.HashText(fixture.GreetingText), ObservedAt: time.Now().UnixMilli(),
		}})
		d.verifyEffect(context.Background(), receipt.MsgID)
		cmd, _ := st.CmdByMsgID(receipt.MsgID)
		if cmd == nil || cmd.Status != store.CmdVerifying || cmd.VerificationN != 1 {
			t.Fatalf("缺新会话的命中不得伪装唯一正证: %+v", cmd)
		}
		assertNoGreetingConversation(t, st, fixture)
	})

	t.Run("unique positive evidence commits full business facts", func(t *testing.T) {
		d, st, m := newDisp(t)
		fixture := seedGreetingTarget(t, st, m, "verify-positive")
		receipt, err := d.SendGreeting(sendGreetingRequest(fixture, "intent-verify-positive", ""))
		if err != nil {
			t.Fatal(err)
		}
		if err := st.MoveEffectToVerification(receipt.MsgID, "test", time.Now()); err != nil {
			t.Fatal(err)
		}
		d.SetEffectVerifier(fixedGreetingVerifier{observation: VerificationObservation{
			Confirmed: true, ContentHash: syncledger.HashText(fixture.GreetingText),
			ConversationRef: fixture.ConversationRef, ObservedAt: time.Now().UnixMilli(),
		}})
		d.verifyEffect(context.Background(), receipt.MsgID)
		cmd, _ := st.CmdByMsgID(receipt.MsgID)
		if cmd == nil || cmd.Status != store.CmdOk || cmd.SideEffect != "confirmed" {
			t.Fatalf("唯一正证未终结物理命令: %+v", cmd)
		}
		assertGreetingSuccess(t, st, fixture, receipt.IntentID)
	})
}

func TestGreetingRepeatedAndLateResultsRemainSingleFact(t *testing.T) {
	d, st, m := newDisp(t)
	fixture := seedGreetingTarget(t, st, m, "duplicate-result")
	request := sendGreetingRequest(fixture, "intent-duplicate-result", "")
	receipt, err := d.SendGreeting(request)
	if err != nil {
		t.Fatal(err)
	}
	result := validGreetingResult(receipt.MsgID, fixture)
	if outcome, _, err := d.applyResultMessage(fixture.HandID, "result-same", result); err != nil || outcome != ocDone {
		t.Fatalf("首次 result: outcome=%v err=%v", outcome, err)
	}
	if outcome, _, err := d.applyResultMessage(fixture.HandID, "result-same", result); err != nil || outcome != ocAlreadyProcessed {
		t.Fatalf("同上行 msgId 重复未去重: outcome=%v err=%v", outcome, err)
	}
	if outcome, _, err := d.applyResultMessage(fixture.HandID, "result-late", result); err != nil || outcome != ocLate {
		t.Fatalf("不同上行 msgId 的迟到 result 未收编: outcome=%v err=%v", outcome, err)
	}
	before := m.sentCount()
	retried, err := d.SendGreeting(request)
	if err != nil || retried.MsgID != receipt.MsgID || retried.Created || m.sentCount() != before {
		t.Fatalf("成功后重复 POST 必须复用同一意图且零再派发: receipt=%+v err=%v", retried, err)
	}
	assertGreetingSuccess(t, st, fixture, receipt.IntentID)
}

func TestGreetingVerificationAndResultRaceRemainSingleFact(t *testing.T) {
	for round := range 8 {
		t.Run(fmt.Sprintf("round-%d", round), func(t *testing.T) {
			d, st, m := newDisp(t)
			fixture := seedGreetingTarget(t, st, m, fmt.Sprintf("race-%d", round))
			receipt, err := d.SendGreeting(sendGreetingRequest(fixture, fmt.Sprintf("intent-race-%d", round), ""))
			if err != nil {
				t.Fatal(err)
			}
			if err := st.MoveEffectToVerification(receipt.MsgID, "race", time.Now()); err != nil {
				t.Fatal(err)
			}
			contentHash := syncledger.HashText(fixture.GreetingText)
			start := make(chan struct{})
			errs := make([]error, 2)
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				_, _, errs[0] = d.applyResultMessage(
					fixture.HandID, fmt.Sprintf("result-race-%d", round), validGreetingResult(receipt.MsgID, fixture),
				)
			}()
			go func() {
				defer wg.Done()
				<-start
				_, errs[1] = st.ResolveGreetingVerified(store.VerifiedGreetingSuccess{
					Ref: receipt.MsgID, ProfileID: fixture.ProfileID,
					PlatformUserRef: fixture.PlatformUserRef, PositionRef: fixture.PositionRef,
					ConversationRef: fixture.ConversationRef, Text: fixture.GreetingText,
					ContentHash: contentHash, ObservedAtMs: time.Now().UnixMilli(),
					ResultBody: "{}", ResolutionReason: "race verification", At: time.Now(),
				})
			}()
			close(start)
			wg.Wait()
			if errs[0] != nil || errs[1] != nil {
				t.Fatalf("result/验证竞态必须双路幂等收编: errs=%v", errs)
			}
			assertGreetingSuccess(t, st, fixture, receipt.IntentID)
		})
	}
}

func TestGreetingConcurrentCASAndExactRetryDoNotGrowLedger(t *testing.T) {
	t.Run("concurrent contenders have one head winner", func(t *testing.T) {
		d, st, m := newDisp(t)
		fixture := seedGreetingTarget(t, st, m, "dispatch-cas")
		requests := []SendGreetingRequest{
			sendGreetingRequest(fixture, "intent-dispatch-cas-a", ""),
			sendGreetingRequest(fixture, "intent-dispatch-cas-b", ""),
		}
		type outcome struct {
			receipt *SendMessageReceipt
			err     error
		}
		outcomes := make([]outcome, len(requests))
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range requests {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				outcomes[i].receipt, outcomes[i].err = d.SendGreeting(requests[i])
			}(i)
		}
		close(start)
		wg.Wait()
		winners, conflicts := 0, 0
		for _, outcome := range outcomes {
			if outcome.err == nil {
				winners++
				continue
			}
			var conflict *store.CandidateGreetingCASConflictError
			if errors.As(outcome.err, &conflict) {
				conflicts++
				continue
			}
			t.Fatalf("并发 CAS 意外错误: %v", outcome.err)
		}
		if winners != 1 || conflicts != 1 {
			t.Fatalf("并发招呼必须一胜一冲突: outcomes=%+v", outcomes)
		}
		latest, err := st.LatestGreetingEffectIntent(fixture.ProfileID)
		if err != nil || latest == nil {
			t.Fatalf("唯一 head 未指向赢家: latest=%+v err=%v", latest, err)
		}
		rows, err := st.RecentCmds(20)
		if err != nil {
			t.Fatal(err)
		}
		intentCommands := 0
		for _, row := range rows {
			if row.IntentID != "" {
				intentCommands++
			}
		}
		if intentCommands != 1 {
			t.Fatalf("CAS 败方不得留下命令: rows=%+v", rows)
		}
	})

	t.Run("exact retry reuses one command", func(t *testing.T) {
		d, st, m := newDisp(t)
		fixture := seedGreetingTarget(t, st, m, "dispatch-retry")
		request := sendGreetingRequest(fixture, "intent-dispatch-retry", "")
		first, err := d.SendGreeting(request)
		if err != nil {
			t.Fatal(err)
		}
		before := m.sentCount()
		second, err := d.SendGreeting(request)
		if err != nil || second.MsgID != first.MsgID || second.Created || m.sentCount() != before {
			t.Fatalf("精确重试未复用命令: first=%+v second=%+v err=%v", first, second, err)
		}
		rows, _ := st.RecentCmds(20)
		intentCommands := 0
		for _, row := range rows {
			if row.IntentID == first.IntentID {
				intentCommands++
			}
		}
		if intentCommands != 1 {
			t.Fatalf("精确重试发生命令增生: rows=%+v", rows)
		}
	})
}
