package jobconfig

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

const testMachineID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func fixedMachineID(context.Context) (string, error) { return testMachineID, nil }

func TestConfigStoreKeepsCredentialPrivateAndViewMasked(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := Config{BaseURL: "https://backend.fixture/", MachineID: testMachineID, LicenseToken: "token-private"}
	if err := store.Save(config); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || loaded == nil || loaded.BaseURL != "https://backend.fixture" || loaded.LicenseToken != "token-private" {
		t.Fatalf("配置未往返: loaded=%+v err=%v", loaded, err)
	}
	viewRaw, _ := json.Marshal(loaded.View())
	for _, secret := range []string{loaded.BaseURL, loaded.MachineID, loaded.LicenseToken} {
		if strings.Contains(string(viewRaw), secret) {
			t.Fatalf("masked view 泄漏凭据: %s", viewRaw)
		}
	}
	info, err := os.Stat(store.path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("配置权限错误: info=%v err=%v", info, err)
	}
}

func TestFetchCurrentUsesOnlyApprovedEndpointAndCredentialShape(t *testing.T) {
	var calls int
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != currentJobPath {
			t.Fatalf("意外请求: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 2 ||
			body["machineId"] != testMachineID || body["licenseToken"] != "token-private" {
			t.Fatalf("请求体错误: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"job":{"id":1}}`))
	}))
	defer backend.Close()
	store, _ := NewConfigStore(t.TempDir())
	if err := store.Save(Config{BaseURL: backend.URL, MachineID: testMachineID, LicenseToken: "token-private"}); err != nil {
		t.Fatal(err)
	}
	source := NewSource(store, backend.Client(), fixedMachineID)
	raw, err := source.FetchCurrent(context.Background())
	if err != nil || string(raw) != `{"job":{"id":1}}` || calls != 1 {
		t.Fatalf("当前职位读取失败: raw=%s calls=%d err=%v", raw, calls, err)
	}
}

func TestFetchCurrentDoesNotRetryRejectedRequest(t *testing.T) {
	var calls int
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "secret-shaped upstream body", http.StatusUnauthorized)
	}))
	defer backend.Close()
	store, _ := NewConfigStore(t.TempDir())
	_ = store.Save(Config{BaseURL: backend.URL, MachineID: testMachineID, LicenseToken: "token-private"})
	_, err := NewSource(store, backend.Client(), fixedMachineID).FetchCurrent(context.Background())
	if !errors.Is(err, ErrUpstreamRejected) || calls != 1 || strings.Contains(err.Error(), "secret-shaped") {
		t.Fatalf("拒绝语义错误: calls=%d err=%v", calls, err)
	}
}

func TestBindUsesOneApprovedRequestAndPersistsCredentialWithoutInviteCode(t *testing.T) {
	const inviteCode = "invite-private"
	var calls int
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != bindPath {
			t.Fatalf("意外请求: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 2 ||
			body["inviteCode"] != inviteCode || body["machineId"] != testMachineID {
			t.Fatalf("激活请求体错误: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authorized":true,"status":"bound","licenseToken":"token-new","customer":{"customerId":7,"customerName":"合成客户","status":"active","subscriptionEndsAt":"2027-01-01T00:00:00Z"}}`))
	}))
	defer backend.Close()

	store, _ := NewConfigStore(t.TempDir())
	result, err := NewSource(store, backend.Client(), fixedMachineID).
		Bind(context.Background(), backend.URL+"/", inviteCode)
	if err != nil || calls != 1 || result.Status != "bound" || result.Customer.ID != 7 {
		t.Fatalf("激活失败: result=%+v calls=%d err=%v", result, calls, err)
	}
	loaded, err := store.Load()
	if err != nil || loaded == nil || loaded.BaseURL != backend.URL || loaded.MachineID != testMachineID ||
		loaded.LicenseToken != "token-new" || loaded.Customer.Name != "合成客户" {
		t.Fatalf("激活凭据未正确落盘: loaded=%+v err=%v", loaded, err)
	}
	raw, err := os.ReadFile(store.path)
	if err != nil || strings.Contains(string(raw), inviteCode) {
		t.Fatalf("激活码被持久化: err=%v raw=%s", err, raw)
	}
	viewRaw, _ := json.Marshal(loaded.View())
	for _, secret := range []string{testMachineID, "token-new", inviteCode} {
		if strings.Contains(string(viewRaw), secret) {
			t.Fatalf("激活状态泄漏秘密: %s", viewRaw)
		}
	}
}

// stubBindTransport 在内存里应答激活请求,不触真实网络——DefaultBaseURL 指向
// 生产服务器,回退语义只能这样验证。
type stubBindTransport struct{ gotURL string }

func (s *stubBindTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	s.gotURL = r.URL.String()
	body := `{"authorized":true,"status":"bound","licenseToken":"token-new","customer":{"customerId":7,"customerName":"合成客户","status":"active","subscriptionEndsAt":""}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

func TestBindFallsBackToBuiltinDefaultBaseURLOnFreshMachine(t *testing.T) {
	transport := &stubBindTransport{}
	store, _ := NewConfigStore(t.TempDir())
	source := NewSource(store, &http.Client{Transport: transport}, fixedMachineID)
	result, err := source.Bind(context.Background(), "", "invite-private")
	if err != nil || result.Status != "bound" {
		t.Fatalf("全新机器空地址激活失败: result=%+v err=%v", result, err)
	}
	if transport.gotURL != DefaultBaseURL+bindPath {
		t.Fatalf("空地址未回退到内置默认后台: %s", transport.gotURL)
	}
	loaded, loadErr := store.Load()
	if loadErr != nil || loaded == nil || loaded.BaseURL != DefaultBaseURL {
		t.Fatalf("内置地址未落盘: loaded=%+v err=%v", loaded, loadErr)
	}
}

func TestBindPrefersStoredBaseURLOverBuiltinDefault(t *testing.T) {
	var calls int
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authorized":true,"status":"bound","licenseToken":"token-new","customer":{"customerId":7,"customerName":"合成客户","status":"active","subscriptionEndsAt":""}}`))
	}))
	defer backend.Close()
	store, _ := NewConfigStore(t.TempDir())
	if err := store.Save(Config{BaseURL: backend.URL, MachineID: testMachineID, LicenseToken: "token-old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSource(store, backend.Client(), fixedMachineID).
		Bind(context.Background(), "", "invite-private"); err != nil || calls != 1 {
		t.Fatalf("已存地址未被沿用: calls=%d err=%v", calls, err)
	}
}

func TestBindRejectionDoesNotOverwriteExistingCredentialOrLeakInvite(t *testing.T) {
	const inviteCode = "invite-private"
	var calls int
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"authorized":false,"status":"invite_used","message":"do not trust body"}`))
	}))
	defer backend.Close()
	store, _ := NewConfigStore(t.TempDir())
	if err := store.Save(Config{BaseURL: backend.URL, MachineID: testMachineID, LicenseToken: "token-old"}); err != nil {
		t.Fatal(err)
	}
	_, err := NewSource(store, backend.Client(), fixedMachineID).
		Bind(context.Background(), backend.URL, inviteCode)
	var rejected *BindRejectedError
	if !errors.As(err, &rejected) || rejected.Status != "invite_used" || calls != 1 ||
		strings.Contains(err.Error(), inviteCode) || strings.Contains(err.Error(), "do not trust") {
		t.Fatalf("激活拒绝语义错误: calls=%d err=%v", calls, err)
	}
	loaded, loadErr := store.Load()
	if loadErr != nil || loaded == nil || loaded.LicenseToken != "token-old" {
		t.Fatalf("拒绝覆盖了既有凭据: loaded=%+v err=%v", loaded, loadErr)
	}
}

func TestFetchCurrentRejectsMachineMismatchBeforeNetwork(t *testing.T) {
	var calls int
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer backend.Close()
	store, _ := NewConfigStore(t.TempDir())
	otherMachineID := strings.Repeat("b", 64)
	if err := store.Save(Config{BaseURL: backend.URL, MachineID: otherMachineID, LicenseToken: "token-private"}); err != nil {
		t.Fatal(err)
	}
	_, err := NewSource(store, backend.Client(), fixedMachineID).FetchCurrent(context.Background())
	if !errors.Is(err, ErrMachineMismatch) || calls != 0 {
		t.Fatalf("机器不匹配仍访问后台: calls=%d err=%v", calls, err)
	}
}
