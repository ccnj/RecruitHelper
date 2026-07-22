package appbridge

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

type greetingVerificationSender struct {
	mu         sync.Mutex
	dispatcher *dispatch.Dispatcher
	reads      []protocol.ChatReadGreetingOutcomeArgs
}

func (s *greetingVerificationSender) SendEnvelope(handID string, env protocol.Envelope) error {
	if env.Kind != protocol.KindCmd {
		return nil
	}
	var body protocol.CmdBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		return err
	}
	if body.Name != protocol.PrimChatReadGreetingOutcome {
		return nil
	}
	var args protocol.ChatReadGreetingOutcomeArgs
	if err := json.Unmarshal(body.Args, &args); err != nil {
		return err
	}
	s.mu.Lock()
	s.reads = append(s.reads, args)
	s.mu.Unlock()
	data, err := protocol.Encode(protocol.ChatReadGreetingOutcomeData{
		Confirmed: true, ContentHash: args.ContentHash, ObservedAt: time.Now().UnixMilli(),
	})
	if err != nil {
		return err
	}
	s.dispatcher.OnAck(handID, protocol.AckBody{Ref: env.MsgID, Status: protocol.AckStatusAccepted})
	s.dispatcher.OnResult(handID, "result-"+env.MsgID, protocol.ResultBody{
		Ref: env.MsgID, Status: protocol.ResultStatusOk, Data: data, ExecMs: 1,
	})
	return nil
}

func (*greetingVerificationSender) HandSession(string) (string, string, bool) {
	return "session-greeting-verifier", "boot-greeting-verifier", true
}

func (*greetingVerificationSender) HandNegotiation(string) ([]string, []string, bool) {
	return []string{
			protocol.PrimChatSendGreeting + "@1",
			protocol.PrimChatReadGreetingOutcome + "@1",
		}, []string{
			string(protocol.FeatureLease1), string(protocol.FeatureProgress1),
			string(protocol.FeatureCancel1), string(protocol.FeatureWitness1),
		}, true
}

func (*greetingVerificationSender) HandContractMatch(string) (bool, bool) { return true, true }

func (*greetingVerificationSender) HandWitness(string) (dispatch.HandWitness, bool) {
	return dispatch.HandWitness{StoreID: "witness-greeting-verifier"}, true
}

func (*greetingVerificationSender) CloseHand(string, string, string) bool { return true }
func (*greetingVerificationSender) HandOfflineMs(string) int64            { return 0 }

func TestGreetingVerificationConfirmedUsesSameAtomicSuccessTransaction(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sender := &greetingVerificationSender{}
	d := dispatch.New(st, sender)
	sender.dispatcher = d
	d.SetEffectVerifier(EffectVerifier{Dispatcher: d})

	const (
		handID      = "hand-greeting-verifier"
		platform    = "zhilian"
		accountRef  = "account-greeting-verifier"
		profileID   = "profile-greeting-verifier"
		userRef     = "user-greeting-verifier"
		positionRef = "position-greeting-verifier"
		text        = "自然问候"
	)
	if err := st.CreateAccount(&store.Account{Platform: platform, AccountRef: accountRef}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccountPrincipal(
		store.AccountKey{Platform: platform, AccountRef: accountRef},
		handID, "principal-greeting-verifier", "session-greeting-verifier", "boot-greeting-verifier", time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	displayName := "候选人"
	positionTitle := "职位"
	if _, err := st.SelectCandidateProfile(store.SelectCandidateProfileRequest{
		ProfileID: profileID,
		Scope: store.CandidateProfileScope{
			Platform: platform, AccountRef: accountRef,
			PlatformUserRef: userRef, PositionRef: positionRef,
		},
		DisplayName: &displayName, PositionTitle: &positionTitle, ObservedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	receipt, err := d.SendGreeting(dispatch.SendGreetingRequest{
		IntentID: "intent-greeting-verifier", ProfileID: profileID, Text: text,
	})
	if err != nil || receipt == nil {
		t.Fatalf("创建招呼意图: receipt=%+v err=%v", receipt, err)
	}
	d.OnResult(handID, "result-parent-possible", protocol.ResultBody{
		Ref: receipt.MsgID, Status: protocol.ResultStatusFailed, ExecMs: 1,
		Error: &protocol.ErrorBody{
			Code: protocol.ErrCodeInternalHand, Retryable: protocol.RetryableNo,
			SideEffect: protocol.SideEffectPossible,
		},
	})
	parent := waitVerificationStatus(t, st, receipt.MsgID, store.CmdOk)
	if parent.VerificationN != 0 {
		t.Fatalf("唯一正证不应计 miss: %+v", parent)
	}
	sender.mu.Lock()
	reads := append([]protocol.ChatReadGreetingOutcomeArgs(nil), sender.reads...)
	sender.mu.Unlock()
	if len(reads) != 1 || reads[0].PlatformUserRef != userRef || reads[0].PositionRef != positionRef ||
		reads[0].ContentHash != syncledger.HashText(text) {
		t.Fatalf("配套验证读未从原命令重建稳定三元组: %+v", reads)
	}
	profile, _ := st.CandidateProfileByID(profileID)
	if profile == nil || profile.MainStatus != store.CandidateProfileGreeted ||
		profile.ConversationRef != nil || profile.SuccessfulGreetingIntentID == nil ||
		*profile.SuccessfulGreetingIntentID != receipt.IntentID {
		t.Fatalf("验证正证未推进同一 Profile: %+v", profile)
	}
	conversations, _ := st.ConversationsForAccount(store.AccountKey{Platform: platform, AccountRef: accountRef})
	intent, _ := st.EffectIntentByID(receipt.IntentID)
	if len(conversations) != 0 || intent == nil || intent.Status != store.EffectIntentOk ||
		intent.ResultConversationRef != nil || intent.ResultMessageSeq != nil {
		t.Fatalf("可见关系验证不得伪造会话事实: conversations=%+v intent=%+v", conversations, intent)
	}
}
