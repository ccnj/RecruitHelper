package adminhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"recruithelper/client/service/internal/session"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

type fakeAdminHub struct {
	mu      sync.Mutex
	session string
	boot    string
	online  bool
	frames  *session.FrameBus
	reg     *session.Registry
}

func newFakeAdminHub() *fakeAdminHub {
	return &fakeAdminHub{frames: session.NewFrameBus(), reg: session.NewRegistry(protocol.DefaultHbGraceMs)}
}

func (h *fakeAdminHub) Frames() *session.FrameBus   { return h.frames }
func (h *fakeAdminHub) Registry() *session.Registry { return h.reg }
func (h *fakeAdminHub) ActiveHandIDs() []string     { return nil }
func (h *fakeAdminHub) HandSession(string) (string, string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.session, h.boot, h.online
}

func (h *fakeAdminHub) WithCurrentHandSession(_ string, sessionID, bootID string, fn func() error) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.online || h.session != sessionID || h.boot != bootID {
		return false, nil
	}
	return true, fn()
}

func (h *fakeAdminHub) set(sessionID, bootID string, online bool) {
	h.mu.Lock()
	h.session, h.boot, h.online = sessionID, bootID, online
	h.mu.Unlock()
}

type probeFunc func(context.Context, string) (protocol.ProbePlatformData, error)

func (f probeFunc) Probe(ctx context.Context, handID string) (protocol.ProbePlatformData, error) {
	return f(ctx, handID)
}

func guardedHealth(t *testing.T, token string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	api := New(st, newFakeAdminHub(), nil, nil, nil, token)
	mux := http.NewServeMux()
	api.Routes(mux)
	req := httptest.NewRequest(http.MethodGet, "/admin/health", nil)
	if mutate != nil {
		mutate(req)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestGuardRequiresExactBearerAndRejectsUntrustedOrigin(t *testing.T) {
	const token = "random-per-start-token"
	if got := guardedHealth(t, token, func(r *http.Request) { r.Header.Set("Origin", "null") }); got.Code != http.StatusUnauthorized {
		t.Fatalf("渲染器请求缺 token 应 401，得到 %d", got.Code)
	}
	if got := guardedHealth(t, token, func(r *http.Request) {
		r.Header.Set("Origin", "null")
		r.Header.Set("Authorization", token)
	}); got.Code != http.StatusUnauthorized {
		t.Fatalf("禁止无 Bearer 前缀的裸 token，得到 %d", got.Code)
	}
	if got := guardedHealth(t, token, func(r *http.Request) {
		r.Header.Set("Origin", "https://evil.example")
		r.Header.Set("Authorization", "Bearer "+token)
	}); got.Code != http.StatusForbidden {
		t.Fatalf("即使 token 正确也不得接受任意网页 Origin，得到 %d", got.Code)
	}
	if got := guardedHealth(t, token, func(r *http.Request) {
		r.Header.Set("Origin", "null")
		r.Header.Set("Authorization", "Bearer "+token)
	}); got.Code != http.StatusOK || got.Header().Get("Access-Control-Allow-Origin") != "null" {
		t.Fatalf("生产 Electron 渲染器应通过: code=%d cors=%q", got.Code, got.Header().Get("Access-Control-Allow-Origin"))
	}
	// Electron 主进程无 Origin 健康探针是唯一公开例外。
	if got := guardedHealth(t, token, nil); got.Code != http.StatusOK {
		t.Fatalf("无 Origin 健康探针应通过，得到 %d", got.Code)
	}
}

func TestGuardDevOriginAndExactJSONMediaType(t *testing.T) {
	api := New(nil, newFakeAdminHub(), nil, nil, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)
	for _, contentType := range []string{"", "text/plain", "application/jsonp"} {
		req := httptest.NewRequest(http.MethodPost, "/admin/accounts/bind", bytes.NewBufferString(`{}`))
		req.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("Content-Type %q 应 415，得到 %d", contentType, w.Code)
		}
	}
	if got := guardedHealth(t, "", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }); got.Code != http.StatusForbidden {
		t.Fatalf("无 token 开发态不得接受任意 Origin，得到 %d", got.Code)
	}
	if got := guardedHealth(t, "", func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:5273") }); got.Code != http.StatusOK {
		t.Fatalf("固定 Vite Origin 应通过，得到 %d", got.Code)
	}
}

func TestHealthAndRoutesExposeNoPairingControlPlane(t *testing.T) {
	api := New(nil, newFakeAdminHub(), nil, nil, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)

	healthReq := httptest.NewRequest(http.MethodGet, "/admin/health", nil)
	health := httptest.NewRecorder()
	mux.ServeHTTP(health, healthReq)
	if health.Code != http.StatusOK {
		t.Fatalf("health code=%d body=%s", health.Code, health.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(health.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, exists := body["pairingOpen"]; exists {
		t.Fatal("health 不得泄漏已删除的配对状态")
	}

	for _, endpoint := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/admin/pairing/open"},
		{http.MethodGet, "/admin/pairing/pending"},
		{http.MethodPost, "/admin/pairing/confirm"},
	} {
		req := httptest.NewRequest(endpoint.method, endpoint.path, bytes.NewBufferString(`{}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code >= 200 && w.Code < 300 {
			t.Fatalf("已删除的路由 %s %s 不得成功，code=%d", endpoint.method, endpoint.path, w.Code)
		}
	}
}

func TestBindAccountRejectsSessionBootTOCTOU(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hub := newFakeAdminHub()
	hub.set("session-before", "boot-before", true)
	fingerprint := "opaque-principal"
	prober := probeFunc(func(context.Context, string) (protocol.ProbePlatformData, error) {
		// 模拟 probe 执行期间手被新 session/boot 顶替。旧观测不得绑到新连接。
		hub.set("session-after", "boot-after", true)
		return protocol.ProbePlatformData{
			ContentScriptOk: true, LoginState: protocol.LoginStateIn, PageKind: protocol.PageKindIm,
			PrincipalFingerprint: &fingerprint,
		}, nil
	})
	api := New(st, hub, nil, nil, prober, "")
	mux := http.NewServeMux()
	api.Routes(mux)
	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/bind",
		bytes.NewBufferString(`{"platform":"zhilian","handId":"hand-1"}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("session/boot 变更必须拒绝绑定，code=%d body=%s", w.Code, w.Body.String())
	}
	accounts, err := st.Accounts()
	if err != nil || len(accounts) != 0 {
		t.Fatalf("被 TOCTOU 拒绝后不得留下账号行: %+v err=%v", accounts, err)
	}
}

func TestBindAccountSucceedsAfterPreBindProbe(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hub := newFakeAdminHub()
	hub.set("session-bind", "boot-bind", true)
	fingerprint := "opaque-principal-for-binding"
	var probedHand string
	prober := probeFunc(func(_ context.Context, handID string) (protocol.ProbePlatformData, error) {
		probedHand = handID
		return protocol.ProbePlatformData{
			ContentScriptOk:      true,
			LoginState:           protocol.LoginStateIn,
			PageKind:             protocol.PageKindIm,
			PrincipalFingerprint: &fingerprint,
			Surface:              &protocol.PlatformSurface{ImListVisible: true},
		}, nil
	})
	api := New(st, hub, nil, nil, prober, "")
	mux := http.NewServeMux()
	api.Routes(mux)
	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/bind",
		bytes.NewBufferString(`{"platform":" platform-under-test ","handId":"hand-bind"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("首次绑定应在 probe 后成功，code=%d body=%s", w.Code, w.Body.String())
	}
	if probedHand != "hand-bind" {
		t.Fatalf("绑定探测发给了错误手: %q", probedHand)
	}
	accounts, err := st.Accounts()
	if err != nil || len(accounts) != 1 {
		t.Fatalf("绑定后账号行错误: %+v err=%v", accounts, err)
	}
	got := accounts[0]
	if got.Platform != "platform-under-test" || got.AccountRef == "" || got.BoundHandID != "hand-bind" ||
		got.PrincipalFingerprint == nil || *got.PrincipalFingerprint != fingerprint {
		t.Fatalf("绑定观测未完整持久化: %+v", got)
	}
}

func TestAccountKeyValidationTreatsPlatformAsOpaqueContext(t *testing.T) {
	got, err := validateAccountKey(" platform-under-test ", " account-1 ")
	if err != nil {
		t.Fatalf("通用平台上下文不应依赖平台枚举: %v", err)
	}
	if got.Platform != "platform-under-test" || got.AccountRef != "account-1" {
		t.Fatalf("平台/账号上下文未规范化透传: %+v", got)
	}

	for _, input := range []struct {
		platform   string
		accountRef string
	}{
		{"", "account-1"},
		{"   ", "account-1"},
		{strings.Repeat("界", 65), "account-1"},
		{"platform-under-test", ""},
		{"platform-under-test", strings.Repeat("账", 257)},
	} {
		if _, err := validateAccountKey(input.platform, input.accountRef); err == nil {
			t.Fatalf("越过协议通用边界的账号键应被拒绝: platform=%q accountRunes=%d",
				input.platform, len([]rune(input.accountRef)))
		}
	}
}
