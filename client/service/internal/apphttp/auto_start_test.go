package apphttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
)

func postAutoStart(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/app/auto-start", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("Authorization", "Bearer "+testBearer)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}

func TestAutoStartGetReturnsSettingWithLastAttempt(t *testing.T) {
	at := time.Date(2026, 8, 20, 7, 12, 0, 0, time.Local)
	handler := newTestAPI(t, &fakeProjections{autoStart: store.AutoStartSetting{
		ID: 1, Enabled: true, LastAttemptAt: &at,
		LastOutcome: store.AutoStartOutcomeStartFailed,
		LastDetail:  "Chrome 插件未连接",
	}})
	res := request(t, handler, http.MethodGet, "/app/auto-start", "127.0.0.1:5000", testBearer)
	if res.Code != http.StatusOK {
		t.Fatalf("状态码=%d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		Enabled       bool   `json:"enabled"`
		LastAttemptAt string `json:"lastAttemptAt"`
		LastOutcome   string `json:"lastOutcome"`
		LastDetail    string `json:"lastDetail"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Enabled || body.LastOutcome != "startFailed" ||
		body.LastDetail != "Chrome 插件未连接" || body.LastAttemptAt == "" {
		t.Fatalf("响应漂移: %+v", body)
	}
}

func TestAutoStartPostRequiresExplicitEnabled(t *testing.T) {
	fake := &fakeProjections{}
	handler := newTestAPI(t, fake)
	// 缺 enabled 字段必须拒绝:开关授权只能是显式的。
	if res := postAutoStart(t, handler, `{}`); res.Code != http.StatusBadRequest {
		t.Fatalf("缺字段应 400: %d %s", res.Code, res.Body.String())
	}
	if fake.savedAutoStart != nil {
		t.Fatalf("拒绝请求不得落库: %+v", *fake.savedAutoStart)
	}
	if res := postAutoStart(t, handler, `{"enabled":true}`); res.Code != http.StatusOK {
		t.Fatalf("有效请求应 200: %d %s", res.Code, res.Body.String())
	}
	if fake.savedAutoStart == nil || !*fake.savedAutoStart {
		t.Fatalf("开关未落库: %+v", fake.savedAutoStart)
	}
	if res := postAutoStart(t, handler, `{"enabled":false}`); res.Code != http.StatusOK {
		t.Fatalf("显式关闭应 200: %d %s", res.Code, res.Body.String())
	}
	if fake.savedAutoStart == nil || *fake.savedAutoStart {
		t.Fatalf("显式关闭未落库: %+v", fake.savedAutoStart)
	}
}
