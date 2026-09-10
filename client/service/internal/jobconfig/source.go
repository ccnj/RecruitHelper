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

	"log/slog"
	"recruithelper/client/service/internal/machineid"
)

const (
	ConfigFilename   = "legacy-job-config.json"
	bindPath         = "/api/v1/client/bind"
	currentJobPath   = "/api/v1/client/job-config"
	allJobsPath      = "/api/v1/client/job-configs"
	maxResponseBytes = 4 << 20
	requestTimeout   = 8 * time.Second

	// DefaultBaseURL 是内置的旧后台地址,与更新源(selfupdate.DefaultFeedURL)同一台
	// 机器、同一先例:写死在代码里,不经旧后台、插件或 AI 下发。激活不再要求人填
	// 地址;显式传入(仅 /admin/job-config/activate 的 API 层)仍以传入值为准,
	// 开发与冒烟据此指向假后台。
	DefaultBaseURL = "http://8.153.161.25"
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
	// Platform 是客户记录的平台归属(2026-09-02 甲方裁决,模型 1「一条客户记录一个平台」),
	// 随 bind 与 job-config(s) 顶层客户快照下发;缺席为空串,由消费方按智联处理。
	Platform string `json:"platform,omitempty"`
}

type Config struct {
	BaseURL      string   `json:"base_url"`
	MachineID    string   `json:"machine_id"`
	LicenseToken string   `json:"license_token"`
	Customer     Customer `json:"customer,omitempty"`
	// RevokedAt / RevokedReason:旧后台已判定本机授权失效——激活码被停用、授权不存在、
	// 机器不匹配(2026-09-10 甲方裁决「停用激活码即硬停机」)。非空即视为需重新激活:
	// 运行快照不再报 authorized,开始/恢复入口拒绝,工作状态上报停发。只有一次成功的
	// bind 会整体覆盖本文件、把它们清掉。订阅过期与客户停用不在此列:那两样在后台侧
	// 改回来就能继续,不该逼客户重新激活。token 保留不删:verify 端已不认它,留着便于
	// 诊断,也免得把"被停用"混同成"从没激活过"。
	RevokedAt     string `json:"revoked_at,omitempty"`
	RevokedReason string `json:"revoked_reason,omitempty"`
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
	CustomerPlatform       string `json:"customerPlatform,omitempty"`
	// Revoked:后台已停用本机授权,需要新激活码。Configured 仍为 true(文件在、token 在),
	// 消费方必须用 Revoked 另行判断,不能只看 Configured。
	Revoked       bool   `json:"revoked"`
	RevokedAt     string `json:"revokedAt,omitempty"`
	RevokedReason string `json:"revokedReason,omitempty"`
}

func (c Config) View() ConfigView {
	view := ConfigView{
		BaseURLConfigured:      strings.TrimSpace(c.BaseURL) != "",
		MachineIDConfigured:    strings.TrimSpace(c.MachineID) != "",
		LicenseTokenConfigured: strings.TrimSpace(c.LicenseToken) != "",
		CustomerName:           strings.TrimSpace(c.Customer.Name),
		CustomerStatus:         strings.TrimSpace(c.Customer.Status),
		CustomerPlatform:       strings.TrimSpace(c.Customer.Platform),
		Revoked:                strings.TrimSpace(c.RevokedAt) != "",
		RevokedAt:              strings.TrimSpace(c.RevokedAt),
		RevokedReason:          strings.TrimSpace(c.RevokedReason),
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
	config.Customer.Platform = strings.TrimSpace(config.Customer.Platform)
	config.RevokedAt = strings.TrimSpace(config.RevokedAt)
	config.RevokedReason = strings.TrimSpace(config.RevokedReason)
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
		return nil, fmt.Errorf("旧后台职位配置源读取失败: %v", err)
	}
	var config Config
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrConfigInvalid, err)
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
		return fmt.Errorf("旧后台职位配置源目录不可写: %v", err)
	}
	raw, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrConfigInvalid, err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(s.path, raw, 0o600); err != nil {
		return fmt.Errorf("旧后台职位配置源写入失败: %v", err)
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		return fmt.Errorf("旧后台职位配置源权限设置失败: %v", err)
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

// CustomerPlatform 实现 productapp.CustomerPlatformSource:读本地客户快照的平台归属。
// 快照缺席、读失败或字段为空一律返回空串,由消费方按 productapp.DefaultPlatform 处理——
// 失效方向是"当旧世界",不会把存量智联客户导向别的平台。
func (s *Source) CustomerPlatform() string {
	config, err := s.LoadConfig()
	if err != nil || config == nil {
		return ""
	}
	return strings.TrimSpace(config.Customer.Platform)
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
		} else {
			rawBaseURL = DefaultBaseURL
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
		return BindResult{}, fmt.Errorf("%w: %v", ErrConfigInvalid, err)
	}
	request.Header.Set("Content-Type", "application/json")
	raw, err := s.do(request, ErrUpstreamRejected, false)
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
			Platform           string `json:"platform"`
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
		Platform:           strings.TrimSpace(response.Customer.Platform),
	}
	if s == nil || s.config == nil {
		return BindResult{}, ErrConfigMissing
	}
	if err := s.config.Save(Config{
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
	return s.postConfigPlane(ctx, currentJobPath, nil)
}

// FetchAll reads the approved plural endpoint. includeDocuments is always true:
// the caller needs per-job document presence, and the endpoint blanks documents
// without that flag. Same single-request, no-retry discipline and the same
// accepted verification side effect as FetchCurrent.
func (s *Source) FetchAll(ctx context.Context) ([]byte, error) {
	return s.postConfigPlane(ctx, allJobsPath, map[string]any{"includeDocuments": true})
}

// postConfigPlane is the only outbound shape for the approved configuration
// plane: one request, no retry, credentials never widened by the caller.
func (s *Source) postConfigPlane(ctx context.Context, path string, extra map[string]any) ([]byte, error) {
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
	body := make(map[string]any, len(extra)+2)
	for key, value := range extra {
		body[key] = value
	}
	body["machineId"] = machineID
	body["licenseToken"] = config.LicenseToken
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, ErrConfigInvalid
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, config.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrConfigInvalid, err)
	}
	request.Header.Set("Content-Type", "application/json")
	raw, err := s.do(request, ErrUpstreamRejected, true)
	if err != nil {
		return nil, err
	}
	s.refreshCustomerSnapshot(raw)
	return raw, nil
}

// refreshCustomerSnapshot 把职位配置响应顶层的 customer 快照(与 bind 响应同形)
// 回写进本地配置,让后台改客户名后无需重新激活即可跟上。只动 customer 段;
// 保存前重读本地配置,避免覆盖并发 bind 刚写入的凭据。响应缺块、名字为空、
// 解析或回写失败都安静放过 —— 失效方向是"名字不更新",不影响配置拉取主流程。
func (s *Source) refreshCustomerSnapshot(raw []byte) {
	var response struct {
		Customer *struct {
			CustomerID         int    `json:"customerId"`
			CustomerName       string `json:"customerName"`
			Status             string `json:"status"`
			SubscriptionEndsAt string `json:"subscriptionEndsAt"`
			Platform           string `json:"platform"`
		} `json:"customer"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Customer == nil {
		return
	}
	customer := Customer{
		ID:                 response.Customer.CustomerID,
		Name:               strings.TrimSpace(response.Customer.CustomerName),
		Status:             strings.TrimSpace(response.Customer.Status),
		SubscriptionEndsAt: strings.TrimSpace(response.Customer.SubscriptionEndsAt),
		Platform:           strings.TrimSpace(response.Customer.Platform),
	}
	if customer.Name == "" {
		return
	}
	current, err := s.config.Load()
	if err != nil || current == nil || current.Customer == customer {
		return
	}
	updated := *current
	updated.Customer = customer
	_ = s.config.Save(updated)
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

// do 发一次请求、不重试。authenticated 标记请求带了 licenseToken:只有这类请求的 401
// 才可能意味着"本机授权已被停用",要看 detail.code 决定是否自标需重新激活;bind 不带
// token,它的 401 只是普通拒绝。
func (s *Source) do(request *http.Request, rejected error, authenticated bool) ([]byte, error) {
	response, err := s.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: transport", ErrUpstreamFailed)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized && authenticated {
		// 旧后台自 2026-09-10 起在 401 的 detail.code 里说明是哪一种拒绝。只取这一个
		// 封闭枚举值进错误链,不带正文——正文可能回显请求,而请求里有 token,错误信息
		// 是要进普通日志的。老后台 detail 是一段文案,取不到码就按普通拒绝处理。
		code := unauthorizedCode(response.Body)
		if revocationCode(code) {
			s.markRevoked(code)
		}
		return nil, fmt.Errorf("%w: status=%d code=%s", rejected, response.StatusCode, code)
	}
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

// unauthorizedCode 从 401 响应正文里取旧后台的拒绝码(`{"detail":{"code":...}}`)。
// 老后台的 detail 是一段文案,取不到码就返回空串——空串不属于任何停用码,只当普通拒绝。
func unauthorizedCode(body io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(body, 4096))
	if err != nil {
		return ""
	}
	var payload struct {
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(raw, &payload) != nil || len(payload.Detail) == 0 {
		return ""
	}
	var detail struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(payload.Detail, &detail) != nil {
		return ""
	}
	return safeCode(detail.Code)
}

// safeCode 只放行 snake_case 枚举形状,其余(含空)归为空串。它会进日志与错误链。
func safeCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 40 {
		return ""
	}
	for _, char := range code {
		if (char < 'a' || char > 'z') && char != '_' {
			return ""
		}
	}
	return code
}

// revocationCode 判断一个拒绝码是不是"没有新激活码就回不来"的那一类。订阅过期
// (subscription_expired)与客户停用(customer_inactive)不算:那两样在后台侧改回来
// 客户端就能继续,不该逼客户重新激活。
func revocationCode(code string) bool {
	switch code {
	case "binding_inactive", "license_not_found", "machine_mismatch":
		return true
	}
	return false
}

// NoteUnauthorized 供其他带 licenseToken 出站的模块(工作状态上报)在收到 401 时转告:
// 同一套判断、同一处落盘,不各自维护一份"该不该停"的名单。
func (s *Source) NoteUnauthorized(code string) {
	if code = safeCode(code); revocationCode(code) {
		s.markRevoked(code)
	}
}

// markRevoked 把"授权已失效"写进本地配置(2026-09-10 甲方裁决「停用激活码即硬停机」)。
// 只写一次;保存失败只记日志——下一次 401 会再来。
func (s *Source) markRevoked(code string) {
	config, err := s.LoadConfig()
	if err != nil || config == nil || strings.TrimSpace(config.RevokedAt) != "" {
		return
	}
	config.RevokedAt = time.Now().Format(time.RFC3339)
	config.RevokedReason = code
	if saveErr := s.config.Save(*config); saveErr != nil {
		slog.Error("授权失效标记写入失败", "errorCode", "authorizationRevoked", "code", code, "err", saveErr.Error())
		return
	}
	slog.Error("旧后台判定本机授权已失效,已标记需重新激活", "errorCode", "authorizationRevoked", "code", code)
}
