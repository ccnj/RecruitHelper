package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
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
	mu            sync.Mutex
	dispatcher    *dispatch.Dispatcher
	autoPages     bool
	anchorHash    string
	targetHash    string
	targetCard    *protocol.ThreadMessage
	reads         []verificationReadCall
	wechatOutcome *protocol.ChatReadWechatExchangeOutcomeData
	wechatReads   []protocol.ChatReadWechatExchangeOutcomeArgs
}

func (s *verificationSender) SendEnvelope(handID string, env protocol.Envelope) error {
	if env.Kind != protocol.KindCmd {
		return nil
	}
	var body protocol.CmdBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		return err
	}
	switch body.Name {
	case protocol.PrimChatReadThread:
		return s.sendThreadRead(handID, env, body)
	case protocol.PrimChatReadWechatExchangeOutcome:
		return s.sendWechatOutcomeRead(handID, env, body)
	default:
		return nil
	}
}

func (s *verificationSender) sendThreadRead(
	handID string,
	env protocol.Envelope,
	body protocol.CmdBody,
) error {
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

func (s *verificationSender) sendWechatOutcomeRead(
	handID string,
	env protocol.Envelope,
	body protocol.CmdBody,
) error {
	var args protocol.ChatReadWechatExchangeOutcomeArgs
	if err := json.Unmarshal(body.Args, &args); err != nil {
		return err
	}
	s.mu.Lock()
	s.wechatReads = append(s.wechatReads, args)
	outcome, d := s.wechatOutcome, s.dispatcher
	s.mu.Unlock()
	if outcome == nil {
		return nil
	}
	dataRaw, err := protocol.Encode(*outcome)
	if err != nil {
		return err
	}
	d.OnAck(handID, protocol.AckBody{
		Ref: env.MsgID, Status: protocol.AckStatusAccepted,
	})
	d.OnResult(handID, "result-"+env.MsgID, protocol.ResultBody{
		Ref: env.MsgID, Status: protocol.ResultStatusOk, Data: dataRaw,
	})
	return nil
}

func (s *verificationSender) HandSession(string) (string, string, bool) {
	return "session-verifier", "boot-verifier", true
}

func (s *verificationSender) HandNegotiation(string) ([]string, []string, bool) {
	return []string{
			protocol.PrimChatSendGreeting + "@1",
			protocol.PrimChatSendMessage + "@1",
			protocol.PrimChatSendWechatInvite + "@1",
			protocol.PrimChatSendInviteCard + "@1",
			protocol.PrimChatAcceptWechat + "@1",
			protocol.PrimChatReadThread + "@1",
			protocol.PrimChatReadWechatExchangeOutcome + "@1",
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

func (s *verificationSender) wechatOutcomeReadCalls() []protocol.ChatReadWechatExchangeOutcomeArgs {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]protocol.ChatReadWechatExchangeOutcomeArgs(nil), s.wechatReads...)
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

func TestWechatAcceptVerifierReadsOutcomeAndResolvesContactAtomically(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sender := &verificationSender{}
	d := dispatch.New(st, sender)
	sender.dispatcher = d

	const (
		handID          = "hand-wechat-accept-verifier"
		accountRef      = "account-wechat-accept-verifier"
		conversationRef = "conversation-wechat-accept-verifier"
		profileID       = "profile-wechat-accept-verifier"
		platformUserRef = "candidate-wechat-accept-verifier"
		positionRef     = "position-wechat-accept-verifier"
	)
	key := store.ConversationKey{
		Platform:        "zhilian",
		AccountRef:      accountRef,
		ConversationRef: conversationRef,
	}
	if err := st.CreateAccount(&store.Account{
		Platform: key.Platform, AccountRef: key.AccountRef,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccountPrincipal(
		store.AccountKey{Platform: key.Platform, AccountRef: key.AccountRef},
		handID,
		"principal-wechat-accept-verifier",
		"session-verifier",
		"boot-verifier",
		time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	displayName := "合成候选人"
	positionTitle := "合成职位"
	if _, err := st.SelectCandidateProfile(store.SelectCandidateProfileRequest{
		ProfileID: profileID,
		Scope: store.CandidateProfileScope{
			Platform: key.Platform, AccountRef: key.AccountRef,
			PlatformUserRef: platformUserRef, PositionRef: positionRef,
		},
		DisplayName: &displayName, PositionTitle: &positionTitle,
		ObservedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	greetingText := "合成招呼"
	greeting, err := d.SendGreeting(dispatch.SendGreetingRequest{
		IntentID:  "intent-wechat-accept-verifier-greeting",
		ProfileID: profileID,
		Text:      greetingText,
	})
	if err != nil || greeting == nil {
		t.Fatalf("构造招呼根失败: receipt=%+v err=%v", greeting, err)
	}
	d.OnAck(handID, protocol.AckBody{
		Ref: greeting.MsgID, Status: protocol.AckStatusAccepted,
	})
	greetingData, err := protocol.Encode(protocol.ChatSendGreetingData{
		PlatformUserRef: platformUserRef,
		PositionRef:     positionRef,
		ConversationRef: conversationRef,
		ContentHash:     syncledger.HashText(greetingText),
		ObservedAt:      time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	d.OnResult(handID, "result-"+greeting.MsgID, protocol.ResultBody{
		Ref: greeting.MsgID, Status: protocol.ResultStatusOk, Data: greetingData,
		Evidence: []protocol.Evidence{{
			Type: string(protocol.SendGreetingEvidenceTypeOutboundGreetingObserved),
		}},
	})
	if _, _, err := st.EnsureCommunicationV4RootForGreetedProfile(
		profileID,
		time.Now(),
	); err != nil {
		t.Fatal(err)
	}

	requestSourceKey := strings.Repeat("d", 64)
	requestHash := syncledger.HashText("synthetic-wechat-request")
	if _, err := st.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: 1,
		NewMessages: []store.MessageDraft{{
			Direction: "in", Kind: "card", CardType: "wechatExchange",
			CardState: "pending", ContentHash: requestHash,
			Origin: "external", SourceKey: &requestSourceKey,
		}},
		SyncedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	applied, err := st.ApplyCommunicationV4BusinessEvent(
		store.ApplyCommunicationV4BusinessEventRequest{
			ProfileID: profileID,
			Event: communication.BusinessEvent{
				Key: "message:2", Kind: communication.EventWechatRequested,
				Source: communication.EventSourceMessage, MessageSeq: 2,
			},
			AppliedAt: time.Now(),
		},
	)
	if err != nil || !applied.Applied {
		t.Fatalf("微信请求事件入账失败: result=%+v err=%v", applied, err)
	}
	actions, err := st.CommunicationV4EventActionsBySource(
		profileID,
		store.CommunicationV4InputBusinessEvent,
		"message:2",
	)
	if err != nil {
		t.Fatal(err)
	}
	var accept *store.CommunicationV4EventAction
	for index := range actions {
		if actions[index].V4Kind == communication.V4ActionAcceptWechat {
			copy := actions[index]
			accept = &copy
			break
		}
	}
	if accept == nil || accept.Status != store.CommunicationV4EventActionPlanned {
		t.Fatalf("接受微信动作未就绪: %+v", actions)
	}
	intentID, err := store.M5AutomaticIntentID(accept.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	exchangeSourceKey := strings.Repeat("e", 64)
	sender.mu.Lock()
	sender.wechatOutcome = &protocol.ChatReadWechatExchangeOutcomeData{
		Confirmed: true, ExchangeSourceKey: exchangeSourceKey,
		PeerWechat: "synthetic-wechat-verifier",
		ObservedAt: time.Now().UnixMilli(),
	}
	sender.mu.Unlock()
	d.SetEffectVerifier(EffectVerifier{Dispatcher: d})
	receipt, err := d.SendAutomaticCard(dispatch.SendAutomaticCardRequest{
		IntentID:          intentID,
		PreviousIntentID:  "",
		AutomaticActionID: accept.ActionID,
		Platform:          key.Platform,
		AccountRef:        key.AccountRef,
		ConversationRef:   key.ConversationRef,
		Primitive:         protocol.PrimChatAcceptWechat,
		RequestSourceKey:  requestSourceKey,
	})
	if err != nil || receipt == nil {
		t.Fatalf("接受微信 WAL 构造失败: receipt=%+v err=%v", receipt, err)
	}
	d.OnAck(handID, protocol.AckBody{
		Ref: receipt.MsgID, Status: protocol.AckStatusAccepted,
	})
	d.OnResult(
		handID,
		"result-possible-"+receipt.MsgID,
		verificationPossibleResult(receipt.MsgID),
	)
	parent := waitVerificationStatus(t, st, receipt.MsgID, store.CmdOk)
	if err := protocol.ValidatePrimitiveResult(
		protocol.PrimChatAcceptWechat,
		protocol.Primitives[protocol.PrimChatAcceptWechat].Ver,
		[]byte(parent.ResultBody),
	); err != nil {
		t.Fatalf("验证器合成的接受结果不符合契约: %v", err)
	}
	reads := sender.wechatOutcomeReadCalls()
	if len(reads) != 1 ||
		reads[0].ConversationRef != conversationRef ||
		reads[0].RequestSourceKey != requestSourceKey {
		t.Fatalf("验证读未精确携带原请求锚: %+v", reads)
	}
	assets, err := st.ContactAssetsByProfile(profileID)
	if err != nil || len(assets) != 1 ||
		assets[0].RequestSourceKey != requestSourceKey ||
		assets[0].SourceKey != exchangeSourceKey ||
		assets[0].Value != "synthetic-wechat-verifier" ||
		assets[0].EffectIntentID == nil ||
		*assets[0].EffectIntentID != intentID {
		t.Fatalf("验证正证未原子收编联系方式: assets=%+v err=%v", assets, err)
	}
	messages, err := st.MessagesForConversation(key)
	if err != nil || len(messages) != 2 {
		t.Fatalf("验证接受不得伪造 outbound Message: messages=%+v err=%v",
			messages, err)
	}
	aggregate, err := st.CommunicationV4AggregateByProfile(profileID)
	if err != nil ||
		aggregate.State.WechatState != communication.V4WechatExchanged ||
		aggregate.AutomationStatus != store.ProfileCommunicationAutomationManualRequired ||
		aggregate.ManualReason != string(communication.V4ManualWechatContinuation) {
		t.Fatalf("验证接受未推进状态并显式移交人工: aggregate=%+v err=%v",
			aggregate, err)
	}
}
