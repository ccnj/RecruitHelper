package adminhttp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// 彩排端点是同步等终局的，测试里请求跑在 goroutine，主线程扮手喂
// ack/result；sender 必须自带锁。
type probeAPISender struct {
	mu   sync.Mutex
	sent []protocol.Envelope
}

func (s *probeAPISender) SendEnvelope(_ string, envelope protocol.Envelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, envelope)
	return nil
}

func (s *probeAPISender) take() []protocol.Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]protocol.Envelope, len(s.sent))
	copy(out, s.sent)
	return out
}

func (*probeAPISender) HandSession(string) (string, string, bool) {
	return "session-api", "boot-api", true
}

func (*probeAPISender) HandNegotiation(string) ([]string, []string, bool) {
	return []string{
			protocol.PrimDebugProbeInterviewEditor + "@1",
		}, []string{
			string(protocol.FeatureWitness1), string(protocol.FeatureLease1),
			string(protocol.FeatureProgress1), string(protocol.FeatureCancel1),
		}, true
}

func (*probeAPISender) HandContractMatch(string) (bool, bool) { return true, true }
func (*probeAPISender) HandWitness(string) (dispatch.HandWitness, bool) {
	return dispatch.HandWitness{StoreID: "witness-api"}, true
}
func (*probeAPISender) CloseHand(string, string, string) bool { return true }
func (*probeAPISender) HandOfflineMs(string) int64            { return 0 }

func TestProbeInterviewEditorAPIDispatchesIntrusiveWithoutIntentAndReturnsHandData(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := seedSendAPI(t, st)
	sender := &probeAPISender{}
	dispatcher := dispatch.New(st, sender)
	api := New(st, newFakeAdminHub(), dispatcher, nil, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)

	startsAt := time.Now().AddDate(0, 1, 0).Truncate(time.Hour).UnixMilli()
	raw, _ := json.Marshal(map[string]any{
		"platform": key.Platform, "accountRef": key.AccountRef,
		"conversationRef": key.ConversationRef, "startsAt": startsAt,
	})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/admin/cards/interview/probe", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		done <- response
	}()

	var cmdMsgID string
	var cmdBody protocol.CmdBody
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		envelopes := sender.take()
		if len(envelopes) > 0 {
			cmdMsgID = envelopes[0].MsgID
			_ = json.Unmarshal(envelopes[0].Body, &cmdBody)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if cmdMsgID == "" {
		t.Fatal("等待派发命令超时")
	}
	if cmdBody.Name != protocol.PrimDebugProbeInterviewEditor {
		t.Fatalf("派发原语错误: %s", cmdBody.Name)
	}
	if cmdBody.Context == nil || cmdBody.Context.ExpectedPrincipalFingerprint != "fingerprint-api" {
		t.Fatalf("命令必须携带账号指纹上下文: %+v", cmdBody.Context)
	}
	var sentArgs protocol.DebugProbeInterviewEditorArgs
	if err := json.Unmarshal(cmdBody.Args, &sentArgs); err != nil ||
		sentArgs.ConversationRef != key.ConversationRef ||
		sentArgs.Interview.StartsAt != startsAt {
		t.Fatalf("彩排 args 错误: %+v err=%v", sentArgs, err)
	}

	record, err := st.CmdByMsgID(cmdMsgID)
	if err != nil || record == nil {
		t.Fatalf("命令必须落账本: %v", err)
	}
	if record.Class != string(protocol.ClassIntrusive) || record.IntentID != "" {
		t.Fatalf("彩排必须是 intrusive 且不铸 effect intent: class=%s intentId=%q",
			record.Class, record.IntentID)
	}

	dispatcher.OnAck("hand-api", protocol.AckBody{Ref: cmdMsgID, Status: protocol.AckStatusAccepted})
	data, _ := protocol.Encode(protocol.DebugProbeInterviewEditorData{
		ConversationRef: key.ConversationRef,
		DateValue:       "2026-08-05", TimeValue: "14:00",
		DurationValue: "30分钟", MethodValue: "微信视频",
		Canceled: true,
	})
	dispatcher.OnResult("hand-api", "result-probe-ok", protocol.ResultBody{
		Ref: cmdMsgID, Status: protocol.ResultStatusOk, Data: data,
	})

	response := <-done
	if response.Code != http.StatusOK {
		t.Fatalf("终局后应 200: code=%d body=%s", response.Code, response.Body.String())
	}
	var view struct {
		MsgID  string `json:"msgId"`
		Status string `json:"status"`
		Data   struct {
			DateValue string `json:"dateValue"`
			TimeValue string `json:"timeValue"`
			Canceled  bool   `json:"canceled"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatalf("响应解析失败: %v body=%s", err, response.Body.String())
	}
	if view.MsgID != cmdMsgID || view.Status != string(store.CmdOk) ||
		view.Data.DateValue != "2026-08-05" || view.Data.TimeValue != "14:00" ||
		!view.Data.Canceled {
		t.Fatalf("响应必须透传手侧回读值: %s", response.Body.String())
	}
}

func TestProbeInterviewEditorAPIRejectsInvalidParams(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedSendAPI(t, st)
	sender := &probeAPISender{}
	dispatcher := dispatch.New(st, sender)
	api := New(st, newFakeAdminHub(), dispatcher, nil, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)

	post := func(payload map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/admin/cards/interview/probe", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		return response
	}
	if code := post(map[string]any{
		"platform": "zhilian", "accountRef": "account-api",
		"conversationRef": "conversation-api", "startsAt": 0,
	}).Code; code != http.StatusBadRequest {
		t.Fatalf("startsAt=0 应 400: %d", code)
	}
	if code := post(map[string]any{
		"platform": "", "accountRef": "account-api",
		"conversationRef": "conversation-api", "startsAt": 1,
	}).Code; code != http.StatusBadRequest {
		t.Fatalf("缺 platform 应 400: %d", code)
	}
	if len(sender.take()) != 0 {
		t.Fatal("非法参数不得派发任何命令")
	}
}

// 2026-07-31 甲方裁决:彩排支持 onsite。现场面试在平台上没有时长与方式控件,
// 故 endsAt 必须缺席而不得由 startsAt 合成;缺省仍是既有 wechatVideo 形态。
func TestProbeInterviewEditorAPIOnsiteOmitsEndsAt(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := seedSendAPI(t, st)
	sender := &probeAPISender{}
	dispatcher := dispatch.New(st, sender)
	api := New(st, newFakeAdminHub(), dispatcher, nil, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)

	startsAt := time.Now().AddDate(0, 1, 0).Truncate(time.Hour).UnixMilli()
	raw, _ := json.Marshal(map[string]any{
		"platform": key.Platform, "accountRef": key.AccountRef,
		"conversationRef": key.ConversationRef, "startsAt": startsAt,
		"method": "onsite",
	})
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/admin/cards/interview/probe", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(httptest.NewRecorder(), req)
	}()

	var cmdBody protocol.CmdBody
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if envelopes := sender.take(); len(envelopes) > 0 {
			_ = json.Unmarshal(envelopes[0].Body, &cmdBody)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var sentArgs protocol.DebugProbeInterviewEditorArgs
	if err := json.Unmarshal(cmdBody.Args, &sentArgs); err != nil {
		t.Fatalf("彩排 args 解析失败: %v", err)
	}
	if sentArgs.Interview.Method != protocol.InterviewMethodOnsite {
		t.Fatalf("method 必须透传 onsite: %q", sentArgs.Interview.Method)
	}
	if sentArgs.Interview.EndsAt != 0 {
		t.Fatalf("现场面试不得派生 endsAt: %d", sentArgs.Interview.EndsAt)
	}
	if sentArgs.Interview.StartsAt != startsAt {
		t.Fatalf("startsAt 必须原样透传: %d", sentArgs.Interview.StartsAt)
	}
}

// 缺省形态零回归:不传 method 时仍是 wechatVideo + startsAt+30 分钟。
func TestProbeInterviewEditorAPIDefaultsToWechatVideoWithDuration(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := seedSendAPI(t, st)
	sender := &probeAPISender{}
	dispatcher := dispatch.New(st, sender)
	api := New(st, newFakeAdminHub(), dispatcher, nil, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)

	startsAt := time.Now().AddDate(0, 1, 0).Truncate(time.Hour).UnixMilli()
	raw, _ := json.Marshal(map[string]any{
		"platform": key.Platform, "accountRef": key.AccountRef,
		"conversationRef": key.ConversationRef, "startsAt": startsAt,
	})
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/admin/cards/interview/probe", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(httptest.NewRecorder(), req)
	}()

	var cmdBody protocol.CmdBody
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if envelopes := sender.take(); len(envelopes) > 0 {
			_ = json.Unmarshal(envelopes[0].Body, &cmdBody)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var sentArgs protocol.DebugProbeInterviewEditorArgs
	if err := json.Unmarshal(cmdBody.Args, &sentArgs); err != nil {
		t.Fatalf("彩排 args 解析失败: %v", err)
	}
	if sentArgs.Interview.Method != protocol.InterviewMethodWechatVideo ||
		sentArgs.Interview.EndsAt != startsAt+communication.V4InterviewDurationMs {
		t.Fatalf("缺省必须是 wechatVideo + 30 分钟: %+v", sentArgs.Interview)
	}
}

func TestProbeInterviewEditorAPIRejectsUnknownMethod(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := seedSendAPI(t, st)
	sender := &probeAPISender{}
	dispatcher := dispatch.New(st, sender)
	api := New(st, newFakeAdminHub(), dispatcher, nil, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)

	for _, method := range []string{"tencentVideo", "ONSITE", "线上面试"} {
		raw, _ := json.Marshal(map[string]any{
			"platform": key.Platform, "accountRef": key.AccountRef,
			"conversationRef": key.ConversationRef,
			"startsAt":        time.Now().AddDate(0, 1, 0).Truncate(time.Hour).UnixMilli(),
			"method":          method,
		})
		req := httptest.NewRequest(http.MethodPost, "/admin/cards/interview/probe", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("method=%q 应 400: %d", method, response.Code)
		}
	}
	if len(sender.take()) != 0 {
		t.Fatal("非法 method 不得派发任何命令")
	}
}
