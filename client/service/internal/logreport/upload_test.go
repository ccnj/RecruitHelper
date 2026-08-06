package logreport

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestUploadPostsContractShape 跑真 HTTP,断言脑发出去的报文形状。
//
// 这条测试的产物会被写进 testdata/,旧后台侧有一条契约锚点测试读同一份文件
// (tests/test_client_log_events.py)。两边共用一份真实载荷,是为了挡住
// "客户端加了字段、后台模型没跟上,于是那个字段被安静忽略"这类静默偏差。
func TestUploadPostsContractShape(t *testing.T) {
	var gotPath, gotContentType string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"accepted":2}`))
	}))
	defer server.Close()

	occurred := time.Date(2026, 8, 6, 10, 30, 0, 0, time.UTC)
	first := occurred.Add(-2 * time.Minute)
	last := occurred
	items := []Item{
		{
			OccurredAt: occurred,
			Source:     SourceBrain,
			Level:      "warn",
			EventType:  EventSuspectCreated,
			Code:       "deadlineExceeded",
			Message:    "命令转 suspect(永不自动重试,待人工裁决)",
			Context: map[string]any{
				"msgId":   "cmd-1",
				"handId":  "hand-1",
				"name":    "chat.sendGreeting",
				"idemKey": "greet:p-1:r1",
			},
			MergedCount: 1,
		},
		{
			OccurredAt:  occurred,
			Source:      SourceHand,
			Level:       "error",
			EventType:   FallbackEventType,
			Message:     "手侧日志: WS 重连失败",
			Fingerprint: "fp-1",
			MergedCount: 7,
			FirstAt:     &first,
			LastAt:      &last,
		},
	}

	target := Target{
		BaseURL:      server.URL,
		MachineID:    "M1",
		LicenseToken: "T1",
		AppVersion:   "1.9.1",
	}
	if err := Upload(context.Background(), target, items); err != nil {
		t.Fatalf("上传失败: %v", err)
	}

	if gotPath != "/api/v1/client/log-events" {
		t.Fatalf("端点错: %s", gotPath)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type 错: %s", gotContentType)
	}

	var decoded map[string]any
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("报文不是合法 JSON: %v", err)
	}
	if decoded["machineId"] != "M1" || decoded["licenseToken"] != "T1" || decoded["appVersion"] != "1.9.1" {
		t.Fatalf("身份字段错: %+v", decoded)
	}
	events, ok := decoded["events"].([]any)
	if !ok || len(events) != 2 {
		t.Fatalf("events 应有 2 条: %+v", decoded["events"])
	}
	firstEvent := events[0].(map[string]any)
	if firstEvent["eventType"] != EventSuspectCreated {
		t.Fatalf("eventType 错: %+v", firstEvent)
	}
	// 时刻走毫秒 epoch,与协议里其余时刻字段一致。
	if firstEvent["occurredAt"] != float64(occurred.UnixMilli()) {
		t.Fatalf("occurredAt 应是毫秒 epoch: %v", firstEvent["occurredAt"])
	}
	context1 := firstEvent["context"].(map[string]any)
	if context1["msgId"] != "cmd-1" {
		t.Fatalf("定位标识丢失: %+v", context1)
	}
	// 空字段不上送,省得后台存一堆空串。
	if _, exists := firstEvent["fingerprint"]; exists {
		t.Fatalf("空 fingerprint 不该出现在报文里: %+v", firstEvent)
	}

	writeContractFixture(t, gotBody)
}

func TestUploadRejectsUnreadyAuth(t *testing.T) {
	// 授权未就绪时连请求都不该发出去。
	err := Upload(context.Background(), Target{BaseURL: "http://x"}, []Item{{Message: "x"}})
	if err == nil {
		t.Fatal("缺 machineId/licenseToken 时应拒绝上传")
	}
}

func TestUploadSurfacesServerRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"detail":"events 超过上限"}`))
	}))
	defer server.Close()

	err := Upload(context.Background(), Target{
		BaseURL: server.URL, MachineID: "M1", LicenseToken: "T1",
	}, []Item{{Message: "x"}})
	if err == nil {
		t.Fatal("服务端拒绝时应返回错误")
	}
}

// writeContractFixture 把真实报文落到 testdata/,供旧后台的契约锚点测试读取。
func writeContractFixture(t *testing.T, body []byte) {
	t.Helper()
	var pretty map[string]any
	if err := json.Unmarshal(body, &pretty); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(pretty, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dir := "testdata"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "log_events_request.json")
	if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
