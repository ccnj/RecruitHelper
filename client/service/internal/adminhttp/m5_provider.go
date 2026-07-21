package adminhttp

import (
	"net/http"
	"strings"

	"recruithelper/client/service/internal/m5ai"
)

func (a *API) m5ProviderConfig(w http.ResponseWriter, _ *http.Request) {
	if a.providerConfig == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "本地模型配置尚未就绪"})
		return
	}
	config, err := a.providerConfig.Load()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "本地模型配置读取失败"})
		return
	}
	if config == nil {
		defaults := m5ai.DefaultProviderConfig()
		writeJSON(w, http.StatusOK, map[string]any{"config": defaults.View()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": config.View()})
}

func (a *API) saveM5ProviderConfig(w http.ResponseWriter, r *http.Request) {
	if a.providerConfig == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "本地模型配置尚未就绪"})
		return
	}
	var request struct {
		Provider              string `json:"provider"`
		Model                 string `json:"model"`
		BaseURL               string `json:"base_url"`
		APIKey                string `json:"api_key"`
		RequestTimeoutMs      int64  `json:"request_timeout_ms"`
		MaxInputTokens        int    `json:"max_input_tokens"`
		MaxIntentOutputTokens int    `json:"max_intent_output_tokens"`
		MaxReplyOutputTokens  int    `json:"max_reply_output_tokens"`
	}
	if decodeJSON(r, &request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "模型配置请求无效"})
		return
	}
	config := m5ai.ProviderConfig{
		Provider: strings.TrimSpace(request.Provider), Model: strings.TrimSpace(request.Model),
		BaseURL: strings.TrimSpace(request.BaseURL), APIKey: strings.TrimSpace(request.APIKey),
		RequestTimeoutMs: request.RequestTimeoutMs, MaxInputTokens: request.MaxInputTokens,
		MaxIntentOutputTokens: request.MaxIntentOutputTokens,
		MaxReplyOutputTokens:  request.MaxReplyOutputTokens,
	}
	if existing, err := a.providerConfig.Load(); err == nil && existing != nil {
		if config.APIKey == "" {
			config.APIKey = existing.APIKey
		}
		if config.BaseURL == "" {
			config.BaseURL = existing.BaseURL
		}
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "本地模型配置读取失败"})
		return
	}
	if err := a.providerConfig.Save(config); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "模型配置不符合 M5-A 预算或缺少必填项"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": config.View()})
}
