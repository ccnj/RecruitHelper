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
	// max_*_tokens 仍然接收但一律忽略:token 预算只由代码常量固定(AGENTS.md
	// 「输入/输出 token 预算由客户端代码固定」),手工配置入口只管身份与连接
	// 参数。字段不能直接删——decodeJSON 拒绝未知字段,而既有前端一直在提交
	// 这三个键,删了会让保存整个 400。
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
	_, _, _ = request.MaxInputTokens, request.MaxIntentOutputTokens, request.MaxReplyOutputTokens
	if decodeJSON(r, &request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "模型配置请求无效"})
		return
	}
	// request.Provider 刻意忽略:标签一律由 base_url 推导(AGENTS.md 2026-07-30
	// 裁决),否则手填一次就能让标签与实际端点脱节,而它要进 AI 诊断摘要和采集
	// 批次的模型一致性校验。
	config := m5ai.ProviderConfig{
		Model:   strings.TrimSpace(request.Model),
		BaseURL: strings.TrimSpace(request.BaseURL), APIKey: strings.TrimSpace(request.APIKey),
		RequestTimeoutMs: request.RequestTimeoutMs,
	}
	if existing, err := a.providerConfig.Load(); err == nil && existing != nil {
		if config.APIKey == "" {
			config.APIKey = existing.APIKey
		}
		if config.BaseURL == "" {
			config.BaseURL = existing.BaseURL
		}
		// model 同样"留空则保留":这个兜底表单只管地址与密钥,不该把后台下发的
		// 模型打回本地常量。
		if config.Model == "" {
			config.Model = existing.Model
		}
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "本地模型配置读取失败"})
		return
	}
	if config.Model == "" {
		config.Model = m5ai.DefaultProviderConfig().Model
	}
	config.Provider = m5ai.DeriveProviderLabel(config.BaseURL)
	if err := a.providerConfig.Save(config); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "模型配置不符合 M5-A 预算或缺少必填项"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": config.View()})
}
