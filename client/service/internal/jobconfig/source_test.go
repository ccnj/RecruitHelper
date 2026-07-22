package jobconfig

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestConfigStoreKeepsCredentialPrivateAndViewMasked(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := Config{BaseURL: "https://backend.fixture/", MachineID: "machine-private", LicenseToken: "token-private"}
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
			body["machineId"] != "machine-private" || body["licenseToken"] != "token-private" {
			t.Fatalf("请求体错误: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"job":{"id":1}}`))
	}))
	defer backend.Close()
	store, _ := NewConfigStore(t.TempDir())
	if err := store.Save(Config{BaseURL: backend.URL, MachineID: "machine-private", LicenseToken: "token-private"}); err != nil {
		t.Fatal(err)
	}
	source := NewSource(store, backend.Client())
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
	_ = store.Save(Config{BaseURL: backend.URL, MachineID: "machine-private", LicenseToken: "token-private"})
	_, err := NewSource(store, backend.Client()).FetchCurrent(context.Background())
	if !errors.Is(err, ErrUpstreamRejected) || calls != 1 || strings.Contains(err.Error(), "secret-shaped") {
		t.Fatalf("拒绝语义错误: calls=%d err=%v", calls, err)
	}
}
