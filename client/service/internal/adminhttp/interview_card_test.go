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
	"recruithelper/contract/gen/go/protocol"
)

// 冒烟生产者与 /admin/messages/send 同轨：真 WAL、真派发、HTTP 幂等重试。
func TestSendInterviewCardAPIUsesDirectRailIdempotently(t *testing.T) {
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

	startsAt := time.Now().Add(24 * time.Hour).Truncate(time.Minute).UnixMilli()
	post := func(payload map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/admin/cards/interview", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		return response
	}
	body := map[string]any{
		"intentId": "intent-interview-api", "previousIntentId": "",
		"platform": key.Platform, "accountRef": key.AccountRef,
		"conversationRef": key.ConversationRef, "startsAt": startsAt,
	}

	first := post(body)
	if first.Code != http.StatusAccepted {
		t.Fatalf("首次应 202: code=%d body=%s", first.Code, first.Body.String())
	}
	var firstBody map[string]any
	_ = json.Unmarshal(first.Body.Bytes(), &firstBody)
	if firstBody["created"] != true || firstBody["intentId"] != "intent-interview-api" ||
		firstBody["msgId"] == "" {
		t.Fatalf("首次回执错误: %+v", firstBody)
	}
	intent, err := st.EffectIntentByID("intent-interview-api")
	if err != nil || intent == nil {
		t.Fatalf("直发轨必须落 WAL intent: %v", err)
	}
	if intent.Primitive != protocol.PrimChatSendInviteCard {
		t.Fatalf("intent 原语错误: %s", intent.Primitive)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("首次必须派发一条命令: sent=%d", len(sender.sent))
	}

	second := post(body)
	if second.Code != http.StatusOK {
		t.Fatalf("HTTP 重试应 200: code=%d body=%s", second.Code, second.Body.String())
	}
	var secondBody map[string]any
	_ = json.Unmarshal(second.Body.Bytes(), &secondBody)
	if secondBody["created"] != false || secondBody["msgId"] != firstBody["msgId"] ||
		len(sender.sent) != 1 {
		t.Fatalf("HTTP 重试必须复用同一命令: first=%+v second=%+v sent=%d",
			firstBody, secondBody, len(sender.sent))
	}

	bad := post(map[string]any{
		"intentId": "intent-interview-bad", "previousIntentId": "intent-interview-api",
		"platform": key.Platform, "accountRef": key.AccountRef,
		"conversationRef": key.ConversationRef, "startsAt": 0,
	})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("非法 startsAt 应 400: code=%d body=%s", bad.Code, bad.Body.String())
	}

	stale := post(map[string]any{
		"intentId": "intent-interview-cas", "previousIntentId": "",
		"platform": key.Platform, "accountRef": key.AccountRef,
		"conversationRef": key.ConversationRef, "startsAt": startsAt,
	})
	if stale.Code != http.StatusConflict {
		t.Fatalf("过期 CAS 应 409: code=%d body=%s", stale.Code, stale.Body.String())
	}
}
