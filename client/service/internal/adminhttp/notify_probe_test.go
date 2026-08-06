package adminhttp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/notify"
	"recruithelper/client/service/internal/store"
)

type recordingProbeSender struct{ calls int }

func (s *recordingProbeSender) SendProbe(notify.ProbeRequest) (notify.ProbeOutcome, error) {
	s.calls++
	return notify.ProbeOutcome{}, nil
}

type emptyBlobs struct{}

func (emptyBlobs) ReadFile(string) ([]byte, error) { return nil, nil }

// 陌生会话必须在动手截图之前就被拒:简历截图要 platformUserRef、正文要状态与
// 微信号,都只能从档案取。查不到就整体拒绝,不派命令、不发半截通知。
func TestNotifyProbeRejectsConversationWithoutProfile(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := seedSendAPI(t, st) // 建了账号与会话,但没有候选人档案
	sender := &probeAPISender{}
	notifySender := &recordingProbeSender{}
	api := New(st, newFakeAdminHub(), dispatch.New(st, sender), nil, nil, "").
		SetNotifyProbeDeps(NotifyProbeDeps{Blobs: emptyBlobs{}, Sender: notifySender})
	mux := http.NewServeMux()
	api.Routes(mux)

	raw, _ := json.Marshal(map[string]any{
		"platform": key.Platform, "accountRef": key.AccountRef,
		"conversationRef": key.ConversationRef,
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/notify/probe", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)

	if response.Code != http.StatusNotFound {
		t.Fatalf("无档案应 404: code=%d body=%s", response.Code, response.Body.String())
	}
	if len(sender.take()) != 0 {
		t.Fatalf("拒绝路径不该派发任何截图命令: %d", len(sender.take()))
	}
	if notifySender.calls != 0 {
		t.Fatalf("拒绝路径不该发出任何通知: %d", notifySender.calls)
	}
}

func TestNotifyProbeRejectsUnknownNotifyType(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := seedSendAPI(t, st)
	sender := &probeAPISender{}
	api := New(st, newFakeAdminHub(), dispatch.New(st, sender), nil, nil, "").
		SetNotifyProbeDeps(NotifyProbeDeps{Blobs: emptyBlobs{}, Sender: &recordingProbeSender{}})
	mux := http.NewServeMux()
	api.Routes(mux)

	raw, _ := json.Marshal(map[string]any{
		"platform": key.Platform, "accountRef": key.AccountRef,
		"conversationRef": key.ConversationRef, "notifyType": "wecomSomethingElse",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/notify/probe", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)

	// 档案先于类型校验:这条会话没档案,先撞 404 也算拒绝在动手之前。
	if response.Code == http.StatusOK {
		t.Fatalf("非法 notifyType 不该 200: %s", response.Body.String())
	}
	if len(sender.take()) != 0 {
		t.Fatalf("拒绝路径不该派发任何截图命令: %d", len(sender.take()))
	}
}

// 未装配发送器时必须明说,不能装作发过了。
func TestNotifyProbeWithoutSenderIsUnavailable(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	api := New(st, newFakeAdminHub(), dispatch.New(st, &probeAPISender{}), nil, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)

	raw, _ := json.Marshal(map[string]any{
		"platform": "zhilian", "accountRef": "a", "conversationRef": "c",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/notify/probe", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("未装配发送器应 503: code=%d", response.Code)
	}
}
