package adminhttp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

type sendAPISender struct {
	sent []protocol.Envelope
}

func (s *sendAPISender) SendEnvelope(_ string, envelope protocol.Envelope) error {
	s.sent = append(s.sent, envelope)
	return nil
}
func (*sendAPISender) HandSession(string) (string, string, bool) {
	return "session-api", "boot-api", true
}
func (*sendAPISender) HandNegotiation(string) ([]string, []string, bool) {
	return []string{
		protocol.PrimChatSendMessage + "@1",
		protocol.PrimChatSendInviteCard + "@1",
	}, []string{
		string(protocol.FeatureWitness1), string(protocol.FeatureLease1),
		string(protocol.FeatureProgress1), string(protocol.FeatureCancel1),
	}, true
}

func (*sendAPISender) HandContractMatch(string) (bool, bool) { return true, true }
func (*sendAPISender) HandWitness(string) (dispatch.HandWitness, bool) {
	return dispatch.HandWitness{StoreID: "witness-api"}, true
}
func (*sendAPISender) CloseHand(string, string, string) bool { return true }
func (*sendAPISender) HandOfflineMs(string) int64            { return 0 }

func seedSendAPI(t *testing.T, st *store.Store) store.ConversationKey {
	t.Helper()
	key := store.ConversationKey{Platform: "zhilian", AccountRef: "account-api", ConversationRef: "conversation-api"}
	if err := st.CreateAccount(&store.Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccountPrincipal(store.AccountKey{Platform: key.Platform, AccountRef: key.AccountRef},
		"hand-api", "fingerprint-api", "session-api", "boot-api", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveConversationList(store.SaveConversationListRequest{
		Platform: key.Platform, AccountRef: key.AccountRef, Complete: true,
		Entries: []store.ListIndexEntry{{ConversationRef: key.ConversationRef, PlatformUserRef: "peer-api"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TrackConversation(key, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	text := "history"
	if _, err := st.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, PlatformUserRef: "peer-api", Adopt: true,
		NewMessages: []store.MessageDraft{{
			Direction: "in", Kind: "text", ContentHash: syncledger.HashText(text), Text: &text, Origin: "external",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestSendMessageAPIIsAsyncIdempotentAndNeverReturnsText(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := seedSendAPI(t, st)
	sender := &sendAPISender{}
	dispatcher := dispatch.New(st, sender)
	api := New(st, newFakeAdminHub(), dispatcher, nil, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)
	body := map[string]any{
		"intentId": "intent-api", "platform": key.Platform, "accountRef": key.AccountRef,
		"conversationRef": key.ConversationRef, "text": "你好", "previousIntentId": "",
	}
	raw, _ := json.Marshal(body)

	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/admin/messages/send", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		return response
	}
	first := post()
	if first.Code != http.StatusAccepted {
		t.Fatalf("首次应 202: code=%d body=%s", first.Code, first.Body.String())
	}
	var firstBody map[string]any
	_ = json.Unmarshal(first.Body.Bytes(), &firstBody)
	if firstBody["created"] != true || firstBody["intentId"] != "intent-api" || firstBody["msgId"] == "" {
		t.Fatalf("首次回执错误: %+v", firstBody)
	}
	if _, leaked := firstBody["text"]; leaked {
		t.Fatalf("发送 API 不得回传正文: %+v", firstBody)
	}

	second := post()
	if second.Code != http.StatusOK {
		t.Fatalf("重试应 200: code=%d body=%s", second.Code, second.Body.String())
	}
	var secondBody map[string]any
	_ = json.Unmarshal(second.Body.Bytes(), &secondBody)
	if secondBody["created"] != false || secondBody["msgId"] != firstBody["msgId"] || len(sender.sent) != 1 {
		t.Fatalf("HTTP 重试必须复用同一命令: first=%+v second=%+v sent=%d", firstBody, secondBody, len(sender.sent))
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/admin/messages/send?intentId=intent-api", nil)
	statusResponse := httptest.NewRecorder()
	mux.ServeHTTP(statusResponse, statusReq)
	if statusResponse.Code != http.StatusOK || bytes.Contains(statusResponse.Body.Bytes(), []byte(`"text"`)) {
		t.Fatalf("状态查询不得泄漏正文: code=%d body=%s", statusResponse.Code, statusResponse.Body.String())
	}

	latestReq := httptest.NewRequest(http.MethodGet,
		"/admin/messages/send?platform=zhilian&accountRef=account-api&conversationRef=conversation-api", nil)
	latestResponse := httptest.NewRecorder()
	mux.ServeHTTP(latestResponse, latestReq)
	if latestResponse.Code != http.StatusOK || bytes.Contains(latestResponse.Body.Bytes(), []byte(`"text"`)) {
		t.Fatalf("按会话恢复 latest 不得泄漏正文: code=%d body=%s", latestResponse.Code, latestResponse.Body.String())
	}
	var latestBody map[string]any
	_ = json.Unmarshal(latestResponse.Body.Bytes(), &latestBody)
	if latestBody["intentId"] != "intent-api" {
		t.Fatalf("按会话未恢复持久 latest: %+v", latestBody)
	}

	conflictRaw, _ := json.Marshal(map[string]any{
		"intentId": "intent-api-other-tab", "previousIntentId": "",
		"platform": key.Platform, "accountRef": key.AccountRef,
		"conversationRef": key.ConversationRef, "text": "另一标签消息",
	})
	conflictReq := httptest.NewRequest(http.MethodPost, "/admin/messages/send", bytes.NewReader(conflictRaw))
	conflictReq.Header.Set("Content-Type", "application/json")
	conflictResponse := httptest.NewRecorder()
	mux.ServeHTTP(conflictResponse, conflictReq)
	var conflictBody struct {
		Current sendMessageView `json:"current"`
	}
	_ = json.Unmarshal(conflictResponse.Body.Bytes(), &conflictBody)
	if conflictResponse.Code != http.StatusConflict || conflictBody.Current.IntentID != "intent-api" ||
		bytes.Contains(conflictResponse.Body.Bytes(), []byte(`"text"`)) || len(sender.sent) != 1 {
		t.Fatalf("旧 predecessor 必须 409 回 current 且不创建/不泄正文: code=%d body=%s sent=%d",
			conflictResponse.Code, conflictResponse.Body.String(), len(sender.sent))
	}
}

func TestGenericAdminCmdCannotBypassProductEffectGate(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sender := &sendAPISender{}
	dispatcher := dispatch.New(st, sender)
	api := New(st, newFakeAdminHub(), dispatcher, nil, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)
	req := httptest.NewRequest(http.MethodPost, "/admin/cmd",
		bytes.NewBufferString(`{"handId":"hand-api","name":"chat.sendMessage","args":{"conversationRef":"c","text":"你好"}}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	if response.Code != http.StatusConflict || len(sender.sent) != 0 {
		t.Fatalf("通用 /admin/cmd 不得绕过真实 SX 意图闸: code=%d body=%s", response.Code, response.Body.String())
	}
}
