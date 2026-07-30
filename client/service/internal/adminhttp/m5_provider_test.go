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

// AGENTS.md 2026-07-30 甲方裁决放开了厂商与模型名校验(base_url/model 都改由旧
// 后台下发,日后要换用非 deepseek 模型),所以"未批准模型必须被拒"这条旧断言已经
// 作废。留下的义务是格式合法性:模型链首项的提取发生在导入侧,落到配置里的必须
// 是单个模型 ID,不能再带分隔符或空白。
func TestM5ProviderConfigAcceptsAnyWellFormedModel(t *testing.T) {
	dataDir := t.TempDir()
	st, _ := store.Open(dataDir)
	defer st.Close()
	configStore, _ := m5ai.NewProviderConfigStore(dataDir)
	api := New(st, newFakeAdminHub(), nil, nil, nil, "", configStore)
	mux := http.NewServeMux()
	api.Routes(mux)
	post := func(model string) int {
		config := m5ai.DefaultProviderConfig()
		config.Model = model
		config.BaseURL = "https://provider.fixture/v1"
		config.APIKey = "sk-fixture"
		raw, _ := json.Marshal(config)
		request := httptest.NewRequest(http.MethodPost, "/admin/m5/provider-config", strings.NewReader(string(raw)))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response.Code
	}
	for _, accepted := range []string{"deepseek-v4-flash", "kimi-k2-turbo", "qwen3-max"} {
		if code := post(accepted); code != http.StatusOK {
			t.Fatalf("合法模型 %q 被拒: code=%d", accepted, code)
		}
	}
	// 模型链原样落盘会让 ProviderName/ModelName 与批次一致性校验失去意义,必须挡住。
	for _, rejected := range []string{"deepseek-v4-pro,deepseek-v4-flash", "deepseek v4", " ", ""} {
		if code := post(rejected); code != http.StatusBadRequest {
			t.Fatalf("非法模型 %q 未被拒: code=%d", rejected, code)
		}
	}
}
