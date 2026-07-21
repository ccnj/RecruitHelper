package adminhttp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

const (
	greetingTestProfile   = "profile-greeting-api"
	greetingTestUserRef   = "raw-user-greeting-api"
	greetingTestPosition  = "raw-position-greeting-api"
	greetingConversation  = "raw-conversation-greeting-api"
	greetingTestText      = "自然问候"
	greetingTestIntent    = "intent-greeting-api"
	greetingTestHand      = "hand-greeting-api"
	greetingTestSession   = "session-greeting-api"
	greetingTestBoot      = "boot-greeting-api"
	greetingTestPrincipal = "principal-greeting-api"
)

type greetingAPISender struct {
	dispatcher *dispatch.Dispatcher
	sent       []protocol.Envelope
	result     func(string, protocol.ChatSendGreetingArgs) protocol.ResultBody
}

func (s *greetingAPISender) SendEnvelope(handID string, envelope protocol.Envelope) error {
	if envelope.Kind != protocol.KindCmd {
		return nil
	}
	var body protocol.CmdBody
	if err := json.Unmarshal(envelope.Body, &body); err != nil {
		return err
	}
	if body.Name != protocol.PrimChatSendGreeting {
		return nil
	}
	s.sent = append(s.sent, envelope)
	var args protocol.ChatSendGreetingArgs
	if err := json.Unmarshal(body.Args, &args); err != nil {
		return err
	}
	s.dispatcher.OnAck(handID, protocol.AckBody{Ref: envelope.MsgID, Status: protocol.AckStatusAccepted})
	result := protocol.ResultBody{}
	if s.result != nil {
		result = s.result(envelope.MsgID, args)
	} else {
		data, err := protocol.Encode(protocol.ChatSendGreetingData{
			PlatformUserRef: args.PlatformUserRef, PositionRef: args.PositionRef,
			ConversationRef: greetingConversation, ContentHash: syncledger.HashText(args.Text),
			ObservedAt: time.Now().UnixMilli(),
		})
		if err != nil {
			return err
		}
		result = protocol.ResultBody{
			Ref: envelope.MsgID, Status: protocol.ResultStatusOk, Data: data, ExecMs: 1,
			Evidence: []protocol.Evidence{{Type: string(protocol.SendGreetingEvidenceTypeOutboundGreetingObserved)}},
		}
	}
	s.dispatcher.OnResult(handID, "result-"+envelope.MsgID, result)
	return nil
}

func (*greetingAPISender) HandSession(string) (string, string, bool) {
	return greetingTestSession, greetingTestBoot, true
}

func (*greetingAPISender) HandNegotiation(string) ([]string, []string, bool) {
	return []string{protocol.PrimChatSendGreeting + "@1", protocol.PrimCandidateReadResume + "@1"}, []string{
		string(protocol.FeatureWitness1), string(protocol.FeatureLease1),
		string(protocol.FeatureProgress1), string(protocol.FeatureCancel1),
	}, true
}

func (*greetingAPISender) HandContractMatch(string) (bool, bool) { return true, true }

func (*greetingAPISender) HandWitness(string) (dispatch.HandWitness, bool) {
	return dispatch.HandWitness{StoreID: "witness-greeting-api"}, true
}

func (*greetingAPISender) CloseHand(string, string, string) bool { return true }
func (*greetingAPISender) HandOfflineMs(string) int64            { return 0 }

func seedGreetingAPI(t *testing.T, st *store.Store, hub *fakeAdminHub) {
	t.Helper()
	if err := st.CreateAccount(&store.Account{Platform: "zhilian", AccountRef: "account-greeting-api"}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccountPrincipal(
		store.AccountKey{Platform: "zhilian", AccountRef: "account-greeting-api"},
		greetingTestHand, greetingTestPrincipal, greetingTestSession, greetingTestBoot, time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	displayName := "候选人"
	positionTitle := "职位"
	if _, err := st.SelectCandidateProfile(store.SelectCandidateProfileRequest{
		ProfileID: greetingTestProfile,
		Scope: store.CandidateProfileScope{
			Platform: "zhilian", AccountRef: "account-greeting-api",
			PlatformUserRef: greetingTestUserRef, PositionRef: greetingTestPosition,
		},
		DisplayName: &displayName, PositionTitle: &positionTitle, ObservedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	hub.set(greetingTestSession, greetingTestBoot, true)
}

func TestSendGreetingAPIUsesProfileLedgerAndAtomicallyAdoptsConversation(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hub := newFakeAdminHub()
	seedGreetingAPI(t, st, hub)
	sender := &greetingAPISender{}
	dispatcher := dispatch.New(st, sender)
	sender.dispatcher = dispatcher
	api := New(st, hub, dispatcher, nil, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)

	body := map[string]string{
		"intentId": greetingTestIntent, "previousIntentId": "",
		"profileId": greetingTestProfile, "text": greetingTestText,
	}
	post := func(value map[string]string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(value)
		req := httptest.NewRequest(http.MethodPost, "/admin/candidates/greeting/send", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	first := post(body)
	if first.Code != http.StatusAccepted {
		t.Fatalf("首次招呼应返回已创建: code=%d body=%s", first.Code, first.Body.String())
	}
	if strings.Contains(first.Body.String(), greetingTestUserRef) ||
		strings.Contains(first.Body.String(), greetingTestPosition) ||
		strings.Contains(first.Body.String(), greetingTestText) ||
		strings.Contains(first.Body.String(), greetingConversation) {
		t.Fatalf("招呼回执不得泄漏平台引用、正文或结果会话: %s", first.Body.String())
	}
	var firstView sendMessageView
	if err := json.Unmarshal(first.Body.Bytes(), &firstView); err != nil {
		t.Fatal(err)
	}
	if firstView.IntentID != greetingTestIntent || firstView.Status != store.EffectIntentOk ||
		firstView.CommandStatus != store.CmdOk || len(sender.sent) != 1 {
		t.Fatalf("招呼未走正式 effect intent: view=%+v sent=%d", firstView, len(sender.sent))
	}

	profile, err := st.CandidateProfileByID(greetingTestProfile)
	if err != nil || profile == nil || profile.MainStatus != store.CandidateProfileGreeted ||
		profile.SuccessfulGreetingIntentID == nil || *profile.SuccessfulGreetingIntentID != greetingTestIntent ||
		profile.ConversationRef == nil || *profile.ConversationRef != greetingConversation {
		t.Fatalf("Profile 未在成功事务推进: profile=%+v err=%v", profile, err)
	}
	key := store.ConversationKey{
		Platform: "zhilian", AccountRef: "account-greeting-api", ConversationRef: greetingConversation,
	}
	conversation, err := st.ConversationByKey(key)
	if err != nil || conversation == nil || conversation.PlatformUserRef != greetingTestUserRef ||
		conversation.TrackingState != store.TrackingAdopted || conversation.AdoptedBoundarySeq != 0 ||
		conversation.LastMessageSeq != 1 {
		t.Fatalf("新会话未原子 adopted: conversation=%+v err=%v", conversation, err)
	}
	tracked, err := st.TrackedIntentByConversation(key)
	if err != nil || tracked == nil || tracked.Status != store.TrackingAdopted ||
		tracked.RequestedBy != "system:greeting" {
		t.Fatalf("TrackedIntent 未原子 adopted: tracked=%+v err=%v", tracked, err)
	}
	messages, err := st.MessagesForConversation(key)
	if err != nil || len(messages) != 1 || messages[0].Seq != 1 || messages[0].Direction != "out" ||
		messages[0].Kind != "text" || messages[0].Origin != "self" ||
		messages[0].OutboundIntentID == nil || *messages[0].OutboundIntentID != greetingTestIntent ||
		messages[0].ContentHash != syncledger.HashText(greetingTestText) {
		t.Fatalf("唯一招呼消息事实错误: messages=%+v err=%v", messages, err)
	}
	intent, err := st.EffectIntentByID(greetingTestIntent)
	if err != nil || intent == nil || intent.ResultConversationRef == nil ||
		*intent.ResultConversationRef != greetingConversation || intent.ResultMessageSeq == nil ||
		*intent.ResultMessageSeq != 1 || intent.IdemKey !=
		"ik1:zhilian:account-greeting-api:chat.sendGreeting:"+greetingTestProfile+":"+greetingTestIntent {
		t.Fatalf("招呼意图结果引用或稳定 idemKey 错误: intent=%+v err=%v", intent, err)
	}

	retried := post(body)
	if retried.Code != http.StatusOK || len(sender.sent) != 1 {
		t.Fatalf("响应丢失重试必须收编同一 current: code=%d body=%s sent=%d",
			retried.Code, retried.Body.String(), len(sender.sent))
	}
	statusReq := httptest.NewRequest(http.MethodGet,
		"/admin/candidates/greeting/send?profileId="+greetingTestProfile, nil)
	status := httptest.NewRecorder()
	mux.ServeHTTP(status, statusReq)
	if status.Code != http.StatusOK || strings.Contains(status.Body.String(), greetingTestText) {
		t.Fatalf("按 profile 查询 current 失败或泄漏正文: code=%d body=%s", status.Code, status.Body.String())
	}
	conflictBody := map[string]string{
		"intentId": "intent-other-tab", "previousIntentId": "",
		"profileId": greetingTestProfile, "text": "其他正文",
	}
	conflict := post(conflictBody)
	if conflict.Code != http.StatusConflict || len(sender.sent) != 1 ||
		!strings.Contains(conflict.Body.String(), greetingTestIntent) ||
		strings.Contains(conflict.Body.String(), "其他正文") {
		t.Fatalf("多标签旧 predecessor 必须只回 current: code=%d body=%s sent=%d",
			conflict.Code, conflict.Body.String(), len(sender.sent))
	}
	messages, _ = st.MessagesForConversation(key)
	if len(messages) != 1 {
		t.Fatalf("重复 POST/CAS 冲突造成消息增生: %+v", messages)
	}
}

func TestSendGreetingFailureOnlyBusinessRejectionEndsProfile(t *testing.T) {
	tests := []struct {
		name       string
		code       protocol.ErrorCode
		retryable  protocol.Retryable
		wantStatus store.CandidateProfileStatus
		wantReason bool
	}{
		{
			name: "platform business rejection", code: protocol.ErrCodeGreetingRejected,
			retryable: protocol.RetryableNo, wantStatus: store.CandidateProfileEnded, wantReason: true,
		},
		{
			name: "technical context failure", code: protocol.ErrCodeCtxNotReady,
			retryable: protocol.RetryableAfterRecovery, wantStatus: store.CandidateProfileSelected,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			hub := newFakeAdminHub()
			seedGreetingAPI(t, st, hub)
			sender := &greetingAPISender{result: func(ref string, _ protocol.ChatSendGreetingArgs) protocol.ResultBody {
				return protocol.ResultBody{
					Ref: ref, Status: protocol.ResultStatusFailed, ExecMs: 1,
					Error: &protocol.ErrorBody{
						Code: test.code, Retryable: test.retryable, SideEffect: protocol.SideEffectNone,
					},
				}
			}}
			dispatcher := dispatch.New(st, sender)
			sender.dispatcher = dispatcher
			api := New(st, hub, dispatcher, nil, nil, "")
			mux := http.NewServeMux()
			api.Routes(mux)
			raw, _ := json.Marshal(map[string]string{
				"intentId": greetingTestIntent + "-failure", "previousIntentId": "",
				"profileId": greetingTestProfile, "text": greetingTestText,
			})
			req := httptest.NewRequest(http.MethodPost, "/admin/candidates/greeting/send", bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, req)
			if response.Code != http.StatusAccepted || len(sender.sent) != 1 {
				t.Fatalf("失败终局仍应返回已创建回执: code=%d body=%s sent=%d",
					response.Code, response.Body.String(), len(sender.sent))
			}
			profile, err := st.CandidateProfileByID(greetingTestProfile)
			if err != nil || profile == nil || profile.MainStatus != test.wantStatus {
				t.Fatalf("档案分流错误: profile=%+v err=%v", profile, err)
			}
			if test.wantReason {
				if profile.EndReason == nil || *profile.EndReason != store.CandidateProfileEndGreetingFailed {
					t.Fatalf("业务拒绝未写 greetingFailed: %+v", profile)
				}
			} else if profile.EndReason != nil {
				t.Fatalf("技术失败不得归档档案: %+v", profile)
			}
			rows, _ := st.ConversationsForAccount(store.AccountKey{Platform: "zhilian", AccountRef: "account-greeting-api"})
			if len(rows) != 0 || profile.ConversationRef != nil || profile.SuccessfulGreetingIntentID != nil {
				t.Fatalf("失败终局不得建立关系事实: conversations=%d profile=%+v", len(rows), profile)
			}
		})
	}
}
