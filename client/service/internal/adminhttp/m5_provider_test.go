package adminhttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
)

func TestM5ProviderConfigAPIAlwaysMasksSecretAndBaseURL(t *testing.T) {
	dataDir := t.TempDir()
	st, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	configStore, _ := m5ai.NewProviderConfigStore(dataDir)
	api := New(st, newFakeAdminHub(), nil, nil, nil, "", configStore)
	mux := http.NewServeMux()
	api.Routes(mux)

	config := m5ai.DefaultProviderConfig()
	payload := map[string]any{
		"provider": config.Provider, "model": config.Model,
		"base_url": "https://provider.fixture/v1", "api_key": "sk-private-fixture",
		"request_timeout_ms":       config.RequestTimeoutMs,
		"max_input_tokens":         config.MaxInputTokens,
		"max_intent_output_tokens": config.MaxIntentOutputTokens,
		"max_reply_output_tokens":  config.MaxReplyOutputTokens,
	}
	raw, _ := json.Marshal(payload)
	request := httptest.NewRequest(http.MethodPost, "/admin/m5/provider-config", strings.NewReader(string(raw)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("保存配置失败: code=%d body=%s", response.Code, response.Body.String())
	}
	for _, forbidden := range []string{"sk-private-fixture", "https://provider.fixture/v1", "api_key", "base_url"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("POST 响应泄漏 %q: %s", forbidden, response.Body.String())
		}
	}

	get := httptest.NewRequest(http.MethodGet, "/admin/m5/provider-config", nil)
	getResponse := httptest.NewRecorder()
	mux.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK || !strings.Contains(getResponse.Body.String(), `"keyConfigured":true`) ||
		!strings.Contains(getResponse.Body.String(), `"baseUrlConfigured":true`) {
		t.Fatalf("GET masked 状态错误: code=%d body=%s", getResponse.Code, getResponse.Body.String())
	}
	for _, forbidden := range []string{"sk-private-fixture", "https://provider.fixture/v1"} {
		if strings.Contains(getResponse.Body.String(), forbidden) {
			t.Fatalf("GET 响应泄漏 %q", forbidden)
		}
	}

	loaded, err := configStore.Load()
	if err != nil || loaded == nil || loaded.APIKey != "sk-private-fixture" {
		t.Fatalf("私有配置未保存: loaded=%+v err=%v", loaded, err)
	}
}

func TestM5ProviderConfigRejectsUnapprovedModel(t *testing.T) {
	dataDir := t.TempDir()
	st, _ := store.Open(dataDir)
	defer st.Close()
	configStore, _ := m5ai.NewProviderConfigStore(dataDir)
	api := New(st, newFakeAdminHub(), nil, nil, nil, "", configStore)
	mux := http.NewServeMux()
	api.Routes(mux)
	config := m5ai.DefaultProviderConfig()
	config.Model = "deepseek-v4-flash"
	config.BaseURL = "https://provider.fixture/v1"
	config.APIKey = "sk-fixture"
	raw, _ := json.Marshal(config)
	request := httptest.NewRequest(http.MethodPost, "/admin/m5/provider-config", strings.NewReader(string(raw)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), "deepseek-v4-flash") {
		t.Fatalf("未批准模型未被安全拒绝: code=%d body=%s", response.Code, response.Body.String())
	}
}
