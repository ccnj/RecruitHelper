package m5ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const ProviderConfigFilename = "llm-provider.json"

type ProviderConfig struct {
	Provider              string `json:"provider"`
	Model                 string `json:"model"`
	BaseURL               string `json:"base_url"`
	APIKey                string `json:"api_key"`
	RequestTimeoutMs      int64  `json:"request_timeout_ms"`
	MaxInputTokens        int    `json:"max_input_tokens"`
	MaxIntentOutputTokens int    `json:"max_intent_output_tokens"`
	MaxReplyOutputTokens  int    `json:"max_reply_output_tokens"`
}

func DefaultProviderConfig() ProviderConfig {
	return ProviderConfig{
		Provider: "deepseek", Model: "deepseek-v4-pro",
		RequestTimeoutMs: 30000, MaxInputTokens: ReplyInputTokenLimit,
		MaxIntentOutputTokens: IntentOutputTokenLimit, MaxReplyOutputTokens: ReplyOutputTokenLimit,
	}
}

func (c ProviderConfig) Validate() error {
	if c.Provider != "deepseek" || c.Model != "deepseek-v4-pro" ||
		strings.TrimSpace(c.APIKey) == "" || validateBaseURL(c.BaseURL) != nil {
		return errors.New("LLM provider 配置不完整")
	}
	if c.RequestTimeoutMs < 1000 || c.RequestTimeoutMs > 120000 ||
		c.MaxInputTokens != ReplyInputTokenLimit ||
		c.MaxIntentOutputTokens != IntentOutputTokenLimit ||
		c.MaxReplyOutputTokens != ReplyOutputTokenLimit {
		return errors.New("LLM provider 配置越过 P 档预算")
	}
	return nil
}

type ProviderConfigView struct {
	Provider              string `json:"provider"`
	Model                 string `json:"model"`
	BaseURLConfigured     bool   `json:"baseUrlConfigured"`
	KeyConfigured         bool   `json:"keyConfigured"`
	RequestTimeoutMs      int64  `json:"request_timeout_ms"`
	MaxInputTokens        int    `json:"max_input_tokens"`
	MaxIntentOutputTokens int    `json:"max_intent_output_tokens"`
	MaxReplyOutputTokens  int    `json:"max_reply_output_tokens"`
}

func (c ProviderConfig) View() ProviderConfigView {
	return ProviderConfigView{
		Provider: c.Provider, Model: c.Model, BaseURLConfigured: strings.TrimSpace(c.BaseURL) != "",
		KeyConfigured: strings.TrimSpace(c.APIKey) != "", RequestTimeoutMs: c.RequestTimeoutMs,
		MaxInputTokens: c.MaxInputTokens, MaxIntentOutputTokens: c.MaxIntentOutputTokens,
		MaxReplyOutputTokens: c.MaxReplyOutputTokens,
	}
}

type ProviderConfigStore struct {
	path string
}

func NewProviderConfigStore(dataDir string) (*ProviderConfigStore, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("provider 配置缺少 data 目录")
	}
	return &ProviderConfigStore{path: filepath.Join(dataDir, ProviderConfigFilename)}, nil
}

func (s *ProviderConfigStore) Load() (*ProviderConfig, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 provider 配置失败")
	}
	var config ProviderConfig
	if json.Unmarshal(raw, &config) != nil || config.Validate() != nil {
		return nil, errors.New("provider 配置文件无效")
	}
	return &config, nil
}

// Save intentionally uses a small direct private-file write. Configuration
// loss is recoverable by a person in the attended development profile, so M5
// does not introduce a second journal/recovery protocol for this one file.
func (s *ProviderConfigStore) Save(config ProviderConfig) error {
	if err := config.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return errors.New("provider 配置目录不可写")
	}
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return errors.New("provider 配置编码失败")
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(s.path, raw, 0o600); err != nil {
		return errors.New("provider 配置写入失败")
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		return errors.New("provider 配置权限设置失败")
	}
	return nil
}
