// Package jobconfig owns the narrow old-backend configuration-plane adapter.
// It never participates in browser execution and never exposes stored
// credentials through a read API.
package jobconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	ConfigFilename   = "legacy-job-config.json"
	currentJobPath   = "/api/v1/client/job-config"
	maxResponseBytes = 4 << 20
	requestTimeout   = 8 * time.Second
)

var (
	ErrConfigInvalid    = errors.New("旧后台职位配置源无效")
	ErrConfigMissing    = errors.New("旧后台职位配置源尚未配置")
	ErrUpstreamFailed   = errors.New("旧后台职位配置读取失败")
	ErrUpstreamRejected = errors.New("旧后台拒绝职位配置读取")
)

type Config struct {
	BaseURL      string `json:"base_url"`
	MachineID    string `json:"machine_id"`
	LicenseToken string `json:"license_token"`
}

type ConfigView struct {
	Configured             bool `json:"configured"`
	BaseURLConfigured      bool `json:"baseUrlConfigured"`
	MachineIDConfigured    bool `json:"machineIdConfigured"`
	LicenseTokenConfigured bool `json:"licenseTokenConfigured"`
}

func (c Config) View() ConfigView {
	view := ConfigView{
		BaseURLConfigured:      strings.TrimSpace(c.BaseURL) != "",
		MachineIDConfigured:    strings.TrimSpace(c.MachineID) != "",
		LicenseTokenConfigured: strings.TrimSpace(c.LicenseToken) != "",
	}
	view.Configured = view.BaseURLConfigured && view.MachineIDConfigured && view.LicenseTokenConfigured
	return view
}

func normalizeConfig(config Config) (Config, error) {
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	config.MachineID = strings.TrimSpace(config.MachineID)
	config.LicenseToken = strings.TrimSpace(config.LicenseToken)
	if config.BaseURL == "" || config.MachineID == "" || config.LicenseToken == "" {
		return Config{}, ErrConfigInvalid
	}
	parsed, err := url.Parse(config.BaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Config{}, ErrConfigInvalid
	}
	return config, nil
}

type ConfigStore struct {
	path string
}

func NewConfigStore(dataDir string) (*ConfigStore, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, ErrConfigInvalid
	}
	return &ConfigStore{path: filepath.Join(dataDir, ConfigFilename)}, nil
}

func (s *ConfigStore) Load() (*Config, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("旧后台职位配置源读取失败")
	}
	var config Config
	if json.Unmarshal(raw, &config) != nil {
		return nil, ErrConfigInvalid
	}
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	return &normalized, nil
}

// Save mirrors the attended-development secret-file policy used by the LLM
// provider configuration. Loss is recoverable by re-entering the credential.
func (s *ConfigStore) Save(config Config) error {
	normalized, err := normalizeConfig(config)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return errors.New("旧后台职位配置源目录不可写")
	}
	raw, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return ErrConfigInvalid
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(s.path, raw, 0o600); err != nil {
		return errors.New("旧后台职位配置源写入失败")
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		return errors.New("旧后台职位配置源权限设置失败")
	}
	return nil
}

type Source struct {
	config *ConfigStore
	client *http.Client
}

func NewSource(config *ConfigStore, client *http.Client) *Source {
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	return &Source{config: config, client: client}
}

func (s *Source) LoadConfig() (*Config, error) {
	if s == nil || s.config == nil {
		return nil, ErrConfigMissing
	}
	return s.config.Load()
}

func (s *Source) SaveConfig(config Config) error {
	if s == nil || s.config == nil {
		return ErrConfigMissing
	}
	return s.config.Save(config)
}

// FetchCurrent performs exactly one request and no retry. The endpoint's
// existing verification side effect (last_seen_at + client.verified audit) is
// an explicitly accepted property of the old configuration plane.
func (s *Source) FetchCurrent(ctx context.Context) ([]byte, error) {
	config, err := s.LoadConfig()
	if err != nil {
		return nil, err
	}
	if config == nil {
		return nil, ErrConfigMissing
	}
	payload, err := json.Marshal(map[string]string{
		"machineId": config.MachineID, "licenseToken": config.LicenseToken,
	})
	if err != nil {
		return nil, ErrConfigInvalid
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, config.BaseURL+currentJobPath, bytes.NewReader(payload))
	if err != nil {
		return nil, ErrConfigInvalid
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: transport", ErrUpstreamFailed)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status=%d", ErrUpstreamRejected, response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes {
		return nil, fmt.Errorf("%w: response", ErrUpstreamFailed)
	}
	return raw, nil
}
