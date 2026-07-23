package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

type verificationReadCall struct {
	msgID string
	args  protocol.ChatReadThreadArgs
}

type verificationSender struct {
	mu         sync.Mutex
	dispatcher *dispatch.Dispatcher
	autoPages  bool
	anchorHash string
	targetHash string
	targetCard *protocol.ThreadMessage
	reads      []verificationReadCall
}

func (s *verificationSender) SendEnvelope(handID string, env protocol.Envelope) error {
	if env.Kind != protocol.KindCmd {
		return nil
	}
	var body protocol.CmdBody
	if err := json.Unmarshal(env.Body, &body); err != nil || body.Name != protocol.PrimChatReadThread {
		return nil
	}
	var args protocol.ChatReadThreadArgs
	if err := json.Unmarshal(body.Args, &args); err != nil {
		return err
	}
	s.mu.Lock()
	s.reads = append(s.reads, verificationReadCall{msgID: env.MsgID, args: args})
	autoPages, anchorHash, targetHash, targetCard, d :=
		s.autoPages, s.anchorHash, s.targetHash, s.targetCard, s.dispatcher
	s.mu.Unlock()
	if !autoPages {
		return nil
	}
	var data protocol.ChatReadThreadData
	if args.Cursor == "" {
		next := "older-page"
		target := verificationThreadMessage(0, protocol.MessageDirectionOut, targetHash)
		if targetCard != nil {
			target = *targetCard
		}
		data = protocol.ChatReadThreadData{
			Messages:   []protocol.ThreadMessage{target},
			NextCursor: &next,
		}
	} else {
		data = protocol.ChatReadThreadData{
			AnchorMatched: true, Complete: true,
			Messages: []protocol.ThreadMessage{verificationThreadMessage(0, protocol.MessageDirectionIn, anchorHash)},
		}
	}
	d.OnAck(handID, protocol.AckBody{Ref: env.MsgID, Status: protocol.AckStatusAccepted})
	d.OnResult(handID, "result-"+env.MsgID, verificationReadResult(env.MsgID, data))
	return nil
}

func (s *verificationSender) HandSession(string) (string, string, bool) {
	return "session-verifier", "boot-verifier", true
}

func (s *verificationSender) HandNegotiation(string) ([]string, []string, bool) {
	return []string{
			protocol.PrimChatSendMessage + "@1",
			protocol.PrimChatSendWechatInvite + "@1",
			protocol.PrimChatSendInviteCard + "@1",
			protocol.PrimChatReadThread + "@1",
		}, []string{
			string(protocol.FeatureLease1),
			string(protocol.FeatureProgress1),
			string(protocol.FeatureCancel1),
			string(protocol.FeatureWitness1),
		}, true
}

func (*verificationSender) HandContractMatch(string) (bool, bool) { return true, true }

func (s *verificationSender) HandWitness(string) (dispatch.HandWitness, bool) {
	return dispatch.HandWitness{StoreID: "witness-verifier"}, true
}

func (s *verificationSender) CloseHand(string, string, string) bool { return true }
func (s *verificationSender) HandOfflineMs(string) int64            { return 0 }

func (s *verificationSender) readCalls() []verificationReadCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]verificationReadCall(nil), s.reads...)
}

func verificationThreadMessage(idx int, direction protocol.MessageDirection, hash string) protocol.ThreadMessage {
	text := hash
	observedAt := time.Now().UnixMilli()
	return protocol.ThreadMessage{
		Direction: direction, Kind: protocol.MessageKindText, ContentHash: hash,
		Idx: idx, Text: &text, TsApprox: &observedAt,
	}
}

func verificationReadResult(ref string, data protocol.ChatReadThreadData) protocol.ResultBody {
	raw, err := protocol.Encode(data)
	if err != nil {
		panic(err)
	}
	return protocol.ResultBody{Ref: ref, Status: protocol.ResultStatusOk, Data: raw}
}

func verificationPossibleResult(ref string) protocol.ResultBody {
	return protocol.ResultBody{
		Ref: ref, Status: protocol.ResultStatusFailed,
		Error: &protocol.ErrorBody{
			Code: protocol.ErrCodeInternalHand, Message: "click outcome unknown",
			Retryable: protocol.RetryableNo, SideEffect: protocol.SideEffectPossible,
		},
	}
}

func newVerificationFixture(t *testing.T, intentID string) (*dispatch.Dispatcher, *store.Store, *verificationSender, dispatch.SendMessageReceipt, string, string) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sender := &verificationSender{}
	d := dispatch.New(st, sender)
	sender.dispatcher = d

	const handID = "hand-verifier"
	key := store.ConversationKey{Platform: "zhilian", AccountRef: "account-verifier", ConversationRef: "conversation-verifier"}
	if err := st.CreateAccount(&store.Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccountPrincipal(store.AccountKey{Platform: key.Platform, AccountRef: key.AccountRef},
		handID, "principal-verifier", "session-verifier", "boot-verifier", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveConversationList(store.SaveConversationListRequest{
		Platform: key.Platform, AccountRef: key.AccountRef, Complete: true,
		Entries: []store.ListIndexEntry{{ConversationRef: key.ConversationRef, PlatformUserRef: "candidate-verifier"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TrackConversation(key, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	history := "历史消息"
	anchorHash := syncledger.HashText(history)
	if _, err := st.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: 0, PlatformUserRef: "candidate-verifier", Adopt: true,
		NewMessages: []store.MessageDraft{{
			Direction: "in", Kind: "text", ContentHash: anchorHash, Text: &history, Origin: "external",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	text := "你好"
	receipt, err := d.SendMessage(dispatch.SendMessageRequest{
		IntentID: intentID, Platform: key.Platform, AccountRef: key.AccountRef,
		ConversationRef: key.ConversationRef, Text: text,
	})
	if err != nil || receipt == nil {
		t.Fatalf("创建发送意图: receipt=%+v err=%v", receipt, err)
	}
	return d, st, sender, *receipt, anchorHash, syncledger.HashText(text)
}

func verificationRequestFor(t *testing.T, st *store.Store, receipt dispatch.SendMessageReceipt) dispatch.VerificationRequest {
	t.Helper()
	cmd, err := st.CmdByMsgID(receipt.MsgID)
	if err != nil || cmd == nil {
		t.Fatalf("读取 parent cmd: cmd=%+v err=%v", cmd, err)
	}
	intent, err := st.EffectIntentByID(receipt.IntentID)
	if err != nil || intent == nil {
		t.Fatalf("读取 intent: intent=%+v err=%v", intent, err)
	}
	var args protocol.ChatSendMessageArgs
	var guards protocol.ChatSendMessageGuards
	if err := json.Unmarshal([]byte(cmd.Args), &args); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(cmd.Guards), &guards); err != nil {
		t.Fatal(err)
	}
	return dispatch.VerificationRequest{Command: *cmd, Intent: *intent, Args: args, Guards: guards}
}

func waitVerificationStatus(t *testing.T, st *store.Store, ref string, want store.CmdStatus) *store.CmdRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cmd, err := st.CmdByMsgID(ref)
		if err == nil && cmd != nil && cmd.Status == want {
			return cmd
		}
		time.Sleep(10 * time.Millisecond)
	}
	cmd, _ := st.CmdByMsgID(ref)
	t.Fatalf("等待状态 %s 超时，当前=%+v", want, cmd)
	return nil
}

func TestEffectVerifierProductionPaginationUsesDistinctChildren(t *testing.T) {
	d, st, sender, receipt, anchorHash, targetHash := newVerificationFixture(t, "intent-verifier-pages")
	sender.mu.Lock()
	sender.autoPages, sender.anchorHash, sender.targetHash = true, anchorHash, targetHash
	sender.mu.Unlock()
	d.SetEffectVerifier(EffectVerifier{Dispatcher: d})
	d.OnResult("hand-verifier", "result-parent-possible", verificationPossibleResult(receipt.MsgID))

	parent := waitVerificationStatus(t, st, receipt.MsgID, store.CmdOk)
	if parent.VerificationN != 0 {
		t.Fatalf("确认命中不应计 miss: %d", parent.VerificationN)
	}
	reads := sender.readCalls()
	if len(reads) != 2 || reads[0].msgID == reads[1].msgID || reads[0].args.Cursor != "" || reads[1].args.Cursor != "older-page" {
		t.Fatalf("每个已消费分页必须是不同 child: %+v", reads)
	}
	intent, _ := st.EffectIntentByID(receipt.IntentID)
	if intent == nil || intent.Status != store.EffectIntentOk {
		t.Fatalf("验证命中未终结权威 intent: %+v", intent)
	}
}

func TestEffectVerifierTimeoutRetainsAndReusesPersistentChild(t *testing.T) {
	d, st, sender, receipt, anchorHash, targetHash := newVerificationFixture(t, "intent-verifier-timeout")
	if err := st.MoveEffectToVerification(receipt.MsgID, "test timeout", time.Now()); err != nil {
		t.Fatal(err)
	}
	verifier := EffectVerifier{Dispatcher: d}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := verifier.Verify(ctx, verificationRequestFor(t, st, receipt))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("预置验证读应超时: %v", err)
	}
	child, err := st.VerificationChildForParent(receipt.MsgID)
	if err != nil || child == nil {
		t.Fatalf("ctx 超时必须保留持久 child: child=%+v err=%v", child, err)
	}
	parent, _ := st.CmdByMsgID(receipt.MsgID)
	if parent.VerificationN != 0 {
		t.Fatalf("child 尚在途不能计一次 miss: %d", parent.VerificationN)
	}

	d.SetEffectVerifier(verifier)
	data := protocol.ChatReadThreadData{
		AnchorMatched: true, Complete: true,
		Messages: []protocol.ThreadMessage{
			verificationThreadMessage(0, protocol.MessageDirectionIn, anchorHash),
			verificationThreadMessage(1, protocol.MessageDirectionOut, targetHash),
		},
	}
	d.OnAck("hand-verifier", protocol.AckBody{Ref: child.MsgID, Status: protocol.AckStatusAccepted})
	d.OnResult("hand-verifier", "result-late-child", verificationReadResult(child.MsgID, data))

	parent = waitVerificationStatus(t, st, receipt.MsgID, store.CmdOk)
	if parent.VerificationN != 0 || len(sender.readCalls()) != 1 {
		t.Fatalf("迟到 child 应复用且不计 miss/不新发: parent=%+v reads=%+v", parent, sender.readCalls())
	}
}
