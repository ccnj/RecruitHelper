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

type blockingVerifier struct{ release <-chan struct{} }

func (v blockingVerifier) Verify(ctx context.Context, _ VerificationRequest) (VerificationObservation, error) {
	select {
	case <-ctx.Done():
		return VerificationObservation{}, ctx.Err()
	case <-v.release:
		return VerificationObservation{Reason: "test miss"}, nil
	}
}

func seedSendTarget(t *testing.T, st *store.Store, m *mockSender, accountRef, conversationRef string) store.ConversationKey {
	t.Helper()
	const handID = "hand-send"
	const bootID = "boot-send"
	m.up(handID, bootID)
	m.negotiate(handID,
		[]string{protocol.PrimChatSendMessage + "@1", protocol.PrimChatReadThread + "@1"},
		append(append([]string(nil), allM2Features...), string(protocol.FeatureWitness1)),
	)
	m.mu.Lock()
	m.witness[handID] = HandWitness{StoreID: "witness-store-1"}
	m.mu.Unlock()
	key := store.ConversationKey{Platform: "zhilian", AccountRef: accountRef, ConversationRef: conversationRef}
	if err := st.CreateAccount(&store.Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccountPrincipal(store.AccountKey{Platform: key.Platform, AccountRef: key.AccountRef},
		handID, "opaque-fp-"+accountRef, "s-test", bootID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveConversationList(store.SaveConversationListRequest{
		Platform: key.Platform, AccountRef: key.AccountRef, Complete: true,
		Entries: []store.ListIndexEntry{{ConversationRef: key.ConversationRef, PlatformUserRef: "peer-" + conversationRef}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TrackConversation(key, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	history := "历史消息"
	if _, err := st.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: 0, PlatformUserRef: "peer-" + conversationRef, Adopt: true,
		NewMessages: []store.MessageDraft{{
			Direction: "in", Kind: "text", ContentHash: syncledger.HashText(history), Text: &history, Origin: "external",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	return key
}

func sendRequest(intentID string, key store.ConversationKey, text string) SendMessageRequest {
	return SendMessageRequest{
		IntentID: intentID, Platform: key.Platform, AccountRef: key.AccountRef,
		ConversationRef: key.ConversationRef, Text: text,
	}
}

func validSendResult(ref, conversationRef, text string) protocol.ResultBody {
	data, _ := protocol.Encode(protocol.ChatSendMessageData{
		ConversationRef: conversationRef, ContentHash: syncledger.HashText(text), ObservedAt: time.Now().UnixMilli(),
	})
	return protocol.ResultBody{
		Ref: ref, Status: protocol.ResultStatusOk, Data: data,
		Evidence: []protocol.Evidence{{Type: string(protocol.SendMessageEvidenceTypeOutboundMessageObserved)}},
	}
}

func TestSendMessageHTTPRetryReusesIntentAfterSuccessAndPreservesExactText(t *testing.T) {
	d, st, m := newDisp(t)
	key := seedSendTarget(t, st, m, "acct-send-1", "conv-send-1")
	text := "  你好，候选人  "
	request := sendRequest("intent-retry-success", key, text)
	first, err := d.SendMessage(request)
	if err != nil || !first.Created || first.MsgID == "" {
		t.Fatalf("首次发送意图失败: receipt=%+v err=%v", first, err)
	}
	var cmdBody protocol.CmdBody
	m.mu.Lock()
	_ = json.Unmarshal(m.sent[0].Body, &cmdBody)
	m.mu.Unlock()
	var sentArgs protocol.ChatSendMessageArgs
	_ = json.Unmarshal(cmdBody.Args, &sentArgs)
	if sentArgs.Text != text {
		t.Fatalf("真实正文不得 trim/fold: got=%q want=%q", sentArgs.Text, text)
	}
	d.OnAck("hand-send", protocol.AckBody{Ref: first.MsgID, Status: protocol.AckStatusAccepted})
	d.OnResult("hand-send", "result-send-ok", validSendResult(first.MsgID, key.ConversationRef, text))

	before := m.sentCount()
	second, err := d.SendMessage(request)
	if err != nil || second.Created || second.MsgID != first.MsgID || second.LogicalDispatchID != first.LogicalDispatchID {
		t.Fatalf("HTTP 重试未复用原 intent: first=%+v second=%+v err=%v", first, second, err)
	}
	if m.sentCount() != before {
		t.Fatal("成功后 HTTP 重试不得因账本尾已变而再发")
	}
	messages, _ := st.MessagesForConversation(key)
	if len(messages) != 2 || messages[1].Text == nil || *messages[1].Text != text || messages[1].Origin != "self" {
		t.Fatalf("成功结果未原子追加唯一 self 消息: %+v", messages)
	}
}

func TestMalformedOKAndRetryHintsNeverCreateRealEffectReplacement(t *testing.T) {
	t.Run("malformed ok becomes possible", func(t *testing.T) {
		d, st, m := newDisp(t)
		key := seedSendTarget(t, st, m, "acct-send-bad", "conv-send-bad")
		receipt, err := d.SendMessage(sendRequest("intent-bad-ok", key, "你好"))
		if err != nil {
			t.Fatal(err)
		}
		// data 看似正确但缺 effectful 必需 evidence。nil verifier 会立即
		// fail-closed 到 suspect，不得伪造 sideEffect=none。
		bad := validSendResult(receipt.MsgID, key.ConversationRef, "你好")
		bad.Evidence = nil
		d.OnResult("hand-send", "result-bad-ok", bad)
		cmd, _ := st.CmdByMsgID(receipt.MsgID)
		lineage, _ := st.CmdLineage(receipt.LogicalDispatchID)
		intent, _ := st.EffectIntentByID(receipt.IntentID)
		if cmd.Status != store.CmdSuspect || cmd.SideEffect != string(protocol.SideEffectPossible) ||
			cmd.ErrorCode != string(protocol.ErrCodeInternalHand) || len(lineage) != 1 || intent.Status != store.EffectIntentSuspect {
			t.Fatalf("畸形 ok 必须 possible→验证/suspect 且零 replacement: cmd=%+v intent=%+v lineage=%+v", cmd, intent, lineage)
		}
	})

	for _, tc := range []struct {
		name      string
		code      protocol.ErrorCode
		retryable protocol.Retryable
	}{
		{name: "retryable yes none terminates", code: protocol.ErrCodeExecTimeoutHand, retryable: protocol.RetryableYes},
		{name: "retryable afterRecovery none terminates", code: protocol.ErrCodeCtxNotReady, retryable: protocol.RetryableAfterRecovery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, st, m := newDisp(t)
			key := seedSendTarget(t, st, m, "acct-send-none-"+string(tc.retryable), "conv-send-none-"+string(tc.retryable))
			receipt, err := d.SendMessage(sendRequest("intent-none-"+string(tc.retryable), key, "你好"))
			if err != nil {
				t.Fatal(err)
			}
			d.OnResult("hand-send", "result-none-"+string(tc.retryable), protocol.ResultBody{
				Ref: receipt.MsgID, Status: protocol.ResultStatusFailed,
				Error: &protocol.ErrorBody{
					Code: tc.code, Message: "definitely not sent",
					Retryable: tc.retryable, SideEffect: protocol.SideEffectNone,
				},
			})
			lineage, _ := st.CmdLineage(receipt.LogicalDispatchID)
			intent, _ := st.EffectIntentByID(receipt.IntentID)
			if len(lineage) != 1 || lineage[0].Status != store.CmdFailed || intent.Status != store.EffectIntentFailed {
				t.Fatalf("真实 SX failed/none 必须终局化原意图,不得因 retryable=%s 铸 replacement: lineage=%+v intent=%+v",
					tc.retryable, lineage, intent)
			}
		})
	}
}

func TestSendMessageIntentConflictDoesNotRecomputeGuards(t *testing.T) {
	d, st, m := newDisp(t)
	key := seedSendTarget(t, st, m, "acct-send-conflict", "conv-send-conflict")
	first, err := d.SendMessage(sendRequest("intent-conflict", key, "你好"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.SendMessage(sendRequest("intent-conflict", key, "你好！"))
	if !errors.Is(err, store.ErrEffectIntentConflict) {
		t.Fatalf("同 intentId 偷换正文必须冲突: %v", err)
	}
	if got := m.sentCount(); got != 1 {
		t.Fatalf("冲突请求不得再发: %d", got)
	}
	if first.Created != true {
		t.Fatal("首次应创建")
	}
}

func TestSendMessageAuthoritativeGatesRemainAfterPreflightPruning(t *testing.T) {
	t.Run("contract mismatch is rejected before WAL", func(t *testing.T) {
		d, st, m := newDisp(t)
		key := seedSendTarget(t, st, m, "acct-send-contract", "conv-send-contract")
		m.setContractMatch("hand-send", false)
		intentID := "intent-send-contract"
		receipt, err := d.SendMessage(sendRequest(intentID, key, "你好"))
		if receipt != nil || !errors.Is(err, ErrContractMismatch) || m.sentCount() != 0 {
			t.Fatalf("契约不一致必须在 WAL 前拒绝 effectful: receipt=%+v err=%v sent=%d", receipt, err, m.sentCount())
		}
		if intent, lookupErr := st.EffectIntentByID(intentID); lookupErr != nil || intent != nil {
			t.Fatalf("WAL 前拒绝不得留下 intent: intent=%+v err=%v", intent, lookupErr)
		}
		if rows, lookupErr := st.RecentCmds(10); lookupErr != nil || len(rows) != 0 {
			t.Fatalf("WAL 前拒绝不得留下 cmd: rows=%+v err=%v", rows, lookupErr)
		}
		if !hasAudit(t, st, "effect_contract_mismatch_blocked", "") {
			t.Fatal("WAL 前契约闸触发必须审计")
		}
	})

	t.Run("removed manual quiet no longer gates direct send", func(t *testing.T) {
		d, st, m := newDisp(t)
		key := seedSendTarget(t, st, m, "acct-send-quiet", "conv-send-quiet")
		until := time.Now().Add(time.Minute)
		if err := st.MutateAccount(store.AccountKey{Platform: key.Platform, AccountRef: key.AccountRef}, func(account *store.Account) error {
			account.ManualQuietUntil = &until
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		intentID := "intent-send-quiet"
		receipt, err := d.SendMessage(sendRequest(intentID, key, "你好"))
		if err != nil || receipt == nil || m.sentCount() != 1 {
			t.Fatalf("静默窗已废除，遗留窗口值不得阻塞发送: receipt=%+v err=%v sent=%d", receipt, err, m.sentCount())
		}
	})

	t.Run("capability is enforced inside dispatch gate", func(t *testing.T) {
		d, st, m := newDisp(t)
		key := seedSendTarget(t, st, m, "acct-send-cap", "conv-send-cap")
		m.negotiate("hand-send", nil,
			append(append([]string(nil), allM2Features...), string(protocol.FeatureWitness1)))
		intentID := "intent-send-cap"
		receipt, err := d.SendMessage(sendRequest(intentID, key, "你好"))
		if receipt != nil || !errors.Is(err, ErrCapability) || m.sentCount() != 0 {
			t.Fatalf("内层 capability 闸未拒绝: receipt=%+v err=%v sent=%d", receipt, err, m.sentCount())
		}
		if intent, lookupErr := st.EffectIntentByID(intentID); lookupErr != nil || intent != nil {
			t.Fatalf("capability 拒绝不得留下 intent: intent=%+v err=%v", intent, lookupErr)
		}
	})

	t.Run("witness feature is enforced inside dispatch gate", func(t *testing.T) {
		d, st, m := newDisp(t)
		key := seedSendTarget(t, st, m, "acct-send-feature", "conv-send-feature")
		m.negotiate("hand-send",
			[]string{protocol.PrimChatSendMessage + "@1", protocol.PrimChatReadThread + "@1"},
			append([]string(nil), allM2Features...))
		intentID := "intent-send-feature"
		receipt, err := d.SendMessage(sendRequest(intentID, key, "你好"))
		if receipt != nil || !errors.Is(err, ErrFeature) || m.sentCount() != 0 {
			t.Fatalf("内层 witness feature 闸未拒绝: receipt=%+v err=%v sent=%d", receipt, err, m.sentCount())
		}
		if intent, lookupErr := st.EffectIntentByID(intentID); lookupErr != nil || intent != nil {
			t.Fatalf("feature 拒绝不得留下 intent: intent=%+v err=%v", intent, lookupErr)
		}
	})

	t.Run("witness store is enforced inside dispatch gate", func(t *testing.T) {
		d, st, m := newDisp(t)
		key := seedSendTarget(t, st, m, "acct-send-witness", "conv-send-witness")
		m.mu.Lock()
		delete(m.witness, "hand-send")
		m.mu.Unlock()
		intentID := "intent-send-witness"
		receipt, err := d.SendMessage(sendRequest(intentID, key, "你好"))
		if receipt != nil || !errors.Is(err, ErrWitnessUnavailable) || m.sentCount() != 0 {
			t.Fatalf("内层 witness store 闸未拒绝: receipt=%+v err=%v sent=%d", receipt, err, m.sentCount())
		}
		if intent, lookupErr := st.EffectIntentByID(intentID); lookupErr != nil || intent != nil {
			t.Fatalf("witness 拒绝不得留下 intent: intent=%+v err=%v", intent, lookupErr)
		}
	})
}

func TestRealEffectDefinitivePreSendAbortAndProtocolRejectTerminateIntent(t *testing.T) {
	t.Run("generation fence proves no socket write", func(t *testing.T) {
		d, st, m := newDisp(t)
		key := seedSendTarget(t, st, m, "acct-send-stale", "conv-send-stale")
		d.sender = &staleAtSendSender{mockSender: m}
		receipt, err := d.SendMessage(sendRequest("intent-send-stale", key, "你好"))
		if !errors.Is(err, ErrStaleSession) || receipt == nil {
			t.Fatalf("代际写栅栏失败应返回可查 intent: receipt=%+v err=%v", receipt, err)
		}
		cmd, _ := st.CmdByMsgID(receipt.MsgID)
		intent, _ := st.EffectIntentByID(receipt.IntentID)
		if cmd.Status != store.CmdFailed || cmd.SideEffect != string(protocol.SideEffectNone) ||
			intent.Status != store.EffectIntentFailed {
			t.Fatalf("可证明未写 socket 必须原子终结 Cmd+Intent: cmd=%+v intent=%+v", cmd, intent)
		}
	})

	t.Run("contract socket fence proves no write", func(t *testing.T) {
		d, st, m := newDisp(t)
		key := seedSendTarget(t, st, m, "acct-send-contract-final", "conv-send-contract-final")
		d.sender = &contractMismatchAtSendSender{mockSender: m}
		receipt, err := d.SendMessage(sendRequest("intent-send-contract-final", key, "你好"))
		if !errors.Is(err, ErrContractMismatch) || receipt == nil {
			t.Fatalf("socket 契约写栅栏失败应返回可查 intent: receipt=%+v err=%v", receipt, err)
		}
		cmd, _ := st.CmdByMsgID(receipt.MsgID)
		intent, _ := st.EffectIntentByID(receipt.IntentID)
		if cmd.Status != store.CmdFailed || cmd.ErrorCode != contractMismatchBeforeSendCode ||
			cmd.SideEffect != string(protocol.SideEffectNone) || intent.Status != store.EffectIntentFailed {
			t.Fatalf("可证明未写 socket 的契约阻断必须原子终结 Cmd+Intent: cmd=%+v intent=%+v", cmd, intent)
		}
		if m.sentCount() != 0 {
			t.Fatalf("socket 契约闸不得产生 effectful 写: %d", m.sentCount())
		}
	})

	t.Run("protocol rejected", func(t *testing.T) {
		d, st, m := newDisp(t)
		key := seedSendTarget(t, st, m, "acct-send-reject", "conv-send-reject")
		receipt, err := d.SendMessage(sendRequest("intent-send-reject", key, "你好"))
		if err != nil {
			t.Fatal(err)
		}
		d.OnAck("hand-send", protocol.AckBody{
			Ref: receipt.MsgID, Status: protocol.AckStatusRejected,
			Error: &protocol.ErrorBody{
				Code: protocol.ErrCodeProtoBadArgs, Message: "rejected",
				Retryable: protocol.RetryableNo, SideEffect: protocol.SideEffectNone,
			},
		})
		cmd, _ := st.CmdByMsgID(receipt.MsgID)
		intent, _ := st.EffectIntentByID(receipt.IntentID)
		if cmd.Status != store.CmdRejected || intent.Status != store.EffectIntentFailed {
			t.Fatalf("ack rejected 不得留下悬空 dispatching intent: cmd=%+v intent=%+v", cmd, intent)
		}
	})
}

func TestRealEffectTransientAckRejectNeverEntersAutomaticQueuedRetry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		slug    string
		code    protocol.ErrorCode
		message string
	}{
		{name: "queue full", slug: "queue-full", code: protocol.ErrCodeQueueFull, message: "queue full"},
		{name: "stale session", slug: "stale-session", code: protocol.ErrCodeStaleSession, message: "stale session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, st, m := newDisp(t)
			key := seedSendTarget(t, st, m, "acct-reject-"+tc.slug, "conv-reject-"+tc.slug)
			receipt, err := d.SendMessage(sendRequest("intent-reject-"+tc.slug, key, "你好"))
			if err != nil {
				t.Fatal(err)
			}
			d.OnAck("hand-send", protocol.AckBody{
				Ref: receipt.MsgID, Status: protocol.AckStatusRejected,
				Error: &protocol.ErrorBody{
					Code: tc.code, Message: tc.message,
					Retryable: protocol.RetryableYes, SideEffect: protocol.SideEffectNone,
				},
			})
			cmd, _ := st.CmdByMsgID(receipt.MsgID)
			intent, _ := st.EffectIntentByID(receipt.IntentID)
			before := cmdCountFor(m, receipt.MsgID)
			d.sweepFaults(time.Now().Add(time.Hour))
			if cmd.Status != store.CmdRejected || intent.Status != store.EffectIntentFailed ||
				before != 1 || cmdCountFor(m, receipt.MsgID) != 1 {
				t.Fatalf("真实 SX 瞬态 receipt 拒绝必须终局化意图,不得绕过 unknown 证词自动重发: cmd=%+v intent=%+v count=%d",
					cmd, intent, cmdCountFor(m, receipt.MsgID))
			}
		})
	}
}

func TestConversationLatestIntentCASSerializesTabsAndRecoversLostResponse(t *testing.T) {
	d, st, m := newDisp(t)
	key := seedSendTarget(t, st, m, "acct-cas-tabs", "conv-cas-tabs")
	requests := []SendMessageRequest{
		sendRequest("intent-tab-a", key, "A"),
		sendRequest("intent-tab-b", key, "B"),
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
		go func(index int) {
			defer wg.Done()
			<-start
			outcomes[index].receipt, outcomes[index].err = d.SendMessage(requests[index])
		}(i)
	}
	close(start)
	wg.Wait()

	winner, loser := -1, -1
	for i := range outcomes {
		switch {
		case outcomes[i].err == nil:
			winner = i
		case errors.Is(outcomes[i].err, store.ErrEffectIntentCASConflict):
			loser = i
		default:
			t.Fatalf("并发 CAS 返回未知结果: %+v", outcomes)
		}
	}
	if winner < 0 || loser < 0 || outcomes[winner].receipt == nil || outcomes[loser].receipt == nil ||
		outcomes[loser].receipt.IntentID != outcomes[winner].receipt.IntentID || m.sentCount() != 1 {
		t.Fatalf("同一 predecessor 只能创建一个意图并向失败标签回报 current: %+v sent=%d", outcomes, m.sentCount())
	}
	missing, _ := st.EffectIntentByID(requests[loser].IntentID)
	if missing != nil {
		t.Fatalf("CAS 失败标签不得留下半条 intent: %+v", missing)
	}

	// 模拟首次 POST 响应丢失：同 intentId 重试必须优先复用；按会话
	// 恢复 latest 也必须得到同一持久意图，不能再进 socket。
	before := m.sentCount()
	retried, err := d.SendMessage(requests[winner])
	if err != nil || retried.IntentID != outcomes[winner].receipt.IntentID || retried.Created || m.sentCount() != before {
		t.Fatalf("丢响应后的同 intent 重试未幂等复用: receipt=%+v err=%v sent=%d", retried, err, m.sentCount())
	}
	latest, err := d.LatestSendMessageStatus(key.Platform, key.AccountRef, key.ConversationRef)
	if err != nil || latest.IntentID != outcomes[winner].receipt.IntentID {
		t.Fatalf("会话 latest 未恢复丢失响应: latest=%+v err=%v", latest, err)
	}
	m.mu.Lock()
	m.online["hand-send"] = false
	m.mu.Unlock()
	stale := sendRequest("intent-offline-stale-tab", key, "离线标签")
	current, err := d.SendMessage(stale)
	if !errors.Is(err, store.ErrEffectIntentCASConflict) || current == nil ||
		current.IntentID != outcomes[winner].receipt.IntentID {
		t.Fatalf("CAS 冲突应先于手在线条件回 current: current=%+v err=%v", current, err)
	}
}

func TestLatestEffectIntentSurvivesBrainRestart(t *testing.T) {
	dataDir := t.TempDir()
	st, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	m := newMock()
	d := New(st, m)
	key := seedSendTarget(t, st, m, "acct-cas-restart", "conv-cas-restart")
	receipt, err := d.SendMessage(sendRequest("intent-cas-restart", key, "你好"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	latest, err := reopened.LatestEffectIntent(key.Platform, key.AccountRef, key.ConversationRef)
	if err != nil || latest == nil || latest.IntentID != receipt.IntentID {
		t.Fatalf("重启后 latest 意图丢失: latest=%+v err=%v", latest, err)
	}
}
