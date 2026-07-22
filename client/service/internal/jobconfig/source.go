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

	"recruithelper/client/service/internal/machineid"
)

const (
	ConfigFilename   = "legacy-job-config.json"
	bindPath         = "/api/v1/client/bind"
	currentJobPath   = "/api/v1/client/job-config"
	maxResponseBytes = 4 << 20
	requestTimeout   = 8 * time.Second
)

var (
	ErrConfigInvalid    = errors.New("旧后台职位配置源无效")
	ErrConfigMissing    = errors.New("旧后台职位配置源尚未配置")
	ErrMachineIdentity  = errors.New("当前机器身份不可用")
	ErrMachineMismatch  = errors.New("旧后台授权与当前机器不匹配")
	ErrBindRejected     = errors.New("旧后台拒绝激活")
	ErrUpstreamFailed   = errors.New("旧后台职位配置读取失败")
	ErrUpstreamRejected = errors.New("旧后台拒绝职位配置读取")
)

type Customer struct {
	ID                 int    `json:"id,omitempty"`
	Name               string `json:"name,omitempty"`
	Status             string `json:"status,omitempty"`
	SubscriptionEndsAt string `json:"subscription_ends_at,omitempty"`
}

type Config struct {
	BaseURL      string   `json:"base_url"`
	MachineID    string   `json:"machine_id"`
	LicenseToken string   `json:"license_token"`
	Customer     Customer `json:"customer,omitempty"`
}

type ConfigView struct {
	Configured             bool   `json:"configured"`
	BaseURLConfigured      bool   `json:"baseUrlConfigured"`
	MachineIDConfigured    bool   `json:"machineIdConfigured"`
	LicenseTokenConfigured bool   `json:"licenseTokenConfigured"`
	MachineIdentityReady   bool   `json:"machineIdentityReady"`
	MachineMatch           bool   `json:"machineMatch"`
	CustomerName           string `json:"customerName,omitempty"`
	CustomerStatus         string `json:"customerStatus,omitempty"`
}

func (c Config) View() ConfigView {
	view := ConfigView{
		BaseURLConfigured:      strings.TrimSpace(c.BaseURL) != "",
		MachineIDConfigured:    strings.TrimSpace(c.MachineID) != "",
		LicenseTokenConfigured: strings.TrimSpace(c.LicenseToken) != "",
		CustomerName:           strings.TrimSpace(c.Customer.Name),
		CustomerStatus:         strings.TrimSpace(c.Customer.Status),
	}
	view.Configured = view.BaseURLConfigured && view.MachineIDConfigured && view.LicenseTokenConfigured
	return view
}

func normalizeConfig(config Config) (Config, error) {
	baseURL, err := normalizeBaseURL(config.BaseURL)
	if err != nil {
		return Config{}, err
	}
	config.BaseURL = baseURL
	config.MachineID = strings.TrimSpace(config.MachineID)
	config.LicenseToken = strings.TrimSpace(config.LicenseToken)
	config.Customer.Name = strings.TrimSpace(config.Customer.Name)
	config.Customer.Status = strings.TrimSpace(config.Customer.Status)
	config.Customer.SubscriptionEndsAt = strings.TrimSpace(config.Customer.SubscriptionEndsAt)
	if !validMachineID(config.MachineID) || config.LicenseToken == "" {
		return Config{}, ErrConfigInvalid
	}
	return config, nil
}

func normalizeBaseURL(raw string) (string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(raw), "/")
	if baseURL == "" {
		return "", ErrConfigInvalid
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrConfigInvalid
	}
	return baseURL, nil
}

func validMachineID(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
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
	config    *ConfigStore
	client    *http.Client
	machineID func(context.Context) (string, error)
}

func NewSource(
	config *ConfigStore,
	client *http.Client,
	machineIDProvider ...func(context.Context) (string, error),
) *Source {
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	provider := machineid.Current
	if len(machineIDProvider) > 0 && machineIDProvider[0] != nil {
		provider = machineIDProvider[0]
	}
	return &Source{config: config, client: client, machineID: provider}
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

func (s *Source) Status(ctx context.Context) (ConfigView, error) {
	config, err := s.LoadConfig()
	if err != nil {
		return ConfigView{}, err
	}
	view := Config{}.View()
	if config != nil {
		view = config.View()
	}
	machineID, machineErr := s.currentMachineID(ctx)
	if machineErr != nil {
		return view, nil
	}
	view.MachineIdentityReady = true
	view.MachineMatch = config != nil && config.MachineID == machineID
	return view, nil
}

type BindResult struct {
	Status   string   `json:"status"`
	Customer Customer `json:"customer"`
}

type BindRejectedError struct {
	Status string
}

func (e *BindRejectedError) Error() string {
	return fmt.Sprintf("%s: status=%s", ErrBindRejected, safeStatus(e.Status))
}

func (e *BindRejectedError) Unwrap() error { return ErrBindRejected }

// Bind uses an attended, one-shot activation code to obtain the old backend's
// compatibility credential. It never retries and never persists the code.
func (s *Source) Bind(ctx context.Context, rawBaseURL, inviteCode string) (BindResult, error) {
	if strings.TrimSpace(rawBaseURL) == "" {
		if existing, loadErr := s.LoadConfig(); loadErr != nil {
			return BindResult{}, loadErr
		} else if existing != nil {
			rawBaseURL = existing.BaseURL
		}
	}
	baseURL, err := normalizeBaseURL(rawBaseURL)
	if err != nil || strings.TrimSpace(inviteCode) == "" {
		return BindResult{}, ErrConfigInvalid
	}
	machineID, err := s.currentMachineID(ctx)
	if err != nil {
		return BindResult{}, err
	}
	payload, err := json.Marshal(map[string]string{
		"inviteCode": strings.TrimSpace(inviteCode),
		"machineId":  machineID,
	})
	if err != nil {
		return BindResult{}, ErrConfigInvalid
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+bindPath, bytes.NewReader(payload))
	if err != nil {
		return BindResult{}, ErrConfigInvalid
	}
	request.Header.Set("Content-Type", "application/json")
	raw, err := s.do(request, ErrUpstreamRejected)
	if err != nil {
		return BindResult{}, err
	}
	var response struct {
		Authorized   bool   `json:"authorized"`
		Status       string `json:"status"`
		LicenseToken string `json:"licenseToken"`
		Customer     *struct {
			CustomerID         int    `json:"customerId"`
			CustomerName       string `json:"customerName"`
			Status             string `json:"status"`
			SubscriptionEndsAt string `json:"subscriptionEndsAt"`
		} `json:"customer"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return BindResult{}, fmt.Errorf("%w: response", ErrUpstreamFailed)
	}
	status := safeStatus(response.Status)
	if !response.Authorized {
		return BindResult{}, &BindRejectedError{Status: status}
	}
	if strings.TrimSpace(response.LicenseToken) == "" || response.Customer == nil {
		return BindResult{}, fmt.Errorf("%w: response", ErrUpstreamFailed)
	}
	customer := Customer{
		ID: response.Customer.CustomerID, Name: strings.TrimSpace(response.Customer.CustomerName),
		Status:             strings.TrimSpace(response.Customer.Status),
		SubscriptionEndsAt: strings.TrimSpace(response.Customer.SubscriptionEndsAt),
	}
	if err := s.SaveConfig(Config{
		BaseURL: baseURL, MachineID: machineID, LicenseToken: response.LicenseToken, Customer: customer,
	}); err != nil {
		return BindResult{}, err
	}
	return BindResult{Status: status, Customer: customer}, nil
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
	machineID, err := s.currentMachineID(ctx)
	if err != nil {
		return nil, err
	}
	if config.MachineID != machineID {
		return nil, ErrMachineMismatch
	}
	payload, err := json.Marshal(map[string]string{
		"machineId": machineID, "licenseToken": config.LicenseToken,
	})
	if err != nil {
		return nil, ErrConfigInvalid
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, config.BaseURL+currentJobPath, bytes.NewReader(payload))
	if err != nil {
		return nil, ErrConfigInvalid
	}
	request.Header.Set("Content-Type", "application/json")
	return s.do(request, ErrUpstreamRejected)
}

func (s *Source) currentMachineID(ctx context.Context) (string, error) {
	if s == nil || s.machineID == nil {
		return "", ErrMachineIdentity
	}
	machineID, err := s.machineID(ctx)
	if err != nil || !validMachineID(machineID) {
		return "", ErrMachineIdentity
	}
	return machineID, nil
}

func (s *Source) do(request *http.Request, rejected error) ([]byte, error) {
	response, err := s.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: transport", ErrUpstreamFailed)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status=%d", rejected, response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes {
		return nil, fmt.Errorf("%w: response", ErrUpstreamFailed)
	}
	return raw, nil
}

func safeStatus(raw string) string {
	status := strings.TrimSpace(raw)
	if status == "" || len(status) > 64 {
		return "unknown"
	}
	for _, char := range status {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' && char != '-' {
			return "unknown"
		}
	}
	return status
}
