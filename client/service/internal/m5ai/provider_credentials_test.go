package m5ai

import (
	"os"
	"path/filepath"
	"testing"
)

// 形状照 docs/里程碑5-批次0-job-config事实记录.md 与 2026-07-30 实测:每个 prompt
// block 各带 apiKey/baseUrl/model,同一客户下三者同值,model 是逗号分隔的模型链。
const singleJobResponse = `{
  "job": {"id": 7, "name": "职位", "environment": "production"},
  "scoring": {"prompt": "p", "apiKey": "sk-fixture", "baseUrl": "https://api.deepseek.com", "model": "m-pro,m-flash"},
  "greeting": {"prompt": "p", "apiKey": "sk-fixture", "baseUrl": "https://api.deepseek.com", "model": "m-pro,m-flash"},
  "communication": {"prompt": "p", "apiKey": "sk-fixture", "baseUrl": "https://api.deepseek.com", "model": "m-pro,m-flash"},
  "intent": {"prompt": "p", "apiKey": "sk-fixture", "baseUrl": "https://api.deepseek.com", "model": "m-pro,m-flash"},
  "silenceFollowup": {"prompt": "p", "apiKey": "sk-fixture", "baseUrl": "https://api.deepseek.com", "model": "m-pro,m-flash"}
}`

func TestExtractProviderCredentialsTakesFirstModelInChain(t *testing.T) {
	credentials, err := ExtractProviderCredentials([]byte(singleJobResponse))
	if err != nil {
		t.Fatalf("提取失败: %v", err)
	}
	if credentials.Model != "m-pro" {
		t.Fatalf("model 未取链首项: %q", credentials.Model)
	}
	if credentials.BaseURL != "https://api.deepseek.com" || credentials.APIKey != "sk-fixture" {
		t.Fatalf("凭据提取错: %+v", credentials)
	}
}

func TestExtractProviderCredentialsFallsBackAcrossBlocks(t *testing.T) {
	// communication 没配值时按固定优先级往后取,不要求各 block 一致。
	raw := `{
      "communication": {"prompt": "p", "apiKey": null, "baseUrl": "", "model": null},
      "scoring": {"prompt": "p", "apiKey": "sk-from-scoring", "baseUrl": "https://api.moonshot.cn", "model": "kimi-k2"}
    }`
	credentials, err := ExtractProviderCredentials([]byte(raw))
	if err != nil {
		t.Fatalf("提取失败: %v", err)
	}
	if credentials.APIKey != "sk-from-scoring" || credentials.Model != "kimi-k2" ||
		credentials.BaseURL != "https://api.moonshot.cn" {
		t.Fatalf("跨 block 退路失效: %+v", credentials)
	}
}

func TestExtractProviderCredentialsAcceptsPluralResponse(t *testing.T) {
	raw := `{"currentJobId": 7, "jobs": [` + singleJobResponse + `]}`
	credentials, err := ExtractProviderCredentials([]byte(raw))
	if err != nil {
		t.Fatalf("多职位提取失败: %v", err)
	}
	if credentials.APIKey != "sk-fixture" || credentials.Model != "m-pro" {
		t.Fatalf("多职位凭据提取错: %+v", credentials)
	}
}

func TestExtractProviderCredentialsEmptyIsNotAnError(t *testing.T) {
	// "后台没配"是正常状态,由调用方走兜底,不该当成解析错误。
	credentials, err := ExtractProviderCredentials([]byte(`{"job": {"id": 1}}`))
	if err != nil {
		t.Fatalf("空凭据被当成错误: %v", err)
	}
	if !credentials.empty() {
		t.Fatalf("空响应却提取到凭据: %+v", credentials)
	}
}

func TestExtractProviderCredentialsRejectsBrokenJSON(t *testing.T) {
	if _, err := ExtractProviderCredentials([]byte(`{`)); err == nil {
		t.Fatal("坏 JSON 未被拒")
	}
}

func TestExtractProviderCredentialsIgnoresChainWithOnlySeparators(t *testing.T) {
	raw := `{"communication": {"apiKey": "sk", "baseUrl": "https://api.x.com", "model": " , , "}}`
	credentials, err := ExtractProviderCredentials([]byte(raw))
	if err != nil {
		t.Fatalf("提取失败: %v", err)
	}
	if credentials.Model != "" {
		t.Fatalf("纯分隔符的模型链应视为未配置: %q", credentials.Model)
	}
}

func TestDeriveProviderLabel(t *testing.T) {
	cases := map[string]string{
		"https://api.deepseek.com":          "deepseek",
		"https://api.moonshot.cn":           "moonshot",
		"https://dashscope.aliyuncs.com":    "dashscope",
		"https://llm.internal.corp:8443/v1": "llm",
		"":                                  "",
		"not a url":                         "",
	}
	for baseURL, want := range cases {
		if got := DeriveProviderLabel(baseURL); got != want {
			t.Fatalf("DeriveProviderLabel(%q)=%q, 期望 %q", baseURL, got, want)
		}
	}
}

func TestApplyBackendCredentialsCreatesConfigWithFixedBudget(t *testing.T) {
	dir := t.TempDir()
	store, err := NewProviderConfigStore(dir)
	if err != nil {
		t.Fatalf("建 store 失败: %v", err)
	}
	credentials := BackendProviderCredentials{
		BaseURL: "https://api.deepseek.com", APIKey: "sk-fixture", Model: "m-pro",
	}
	applied, err := store.ApplyBackendCredentials(credentials)
	if err != nil || !applied {
		t.Fatalf("首次应用失败: applied=%v err=%v", applied, err)
	}
	config, err := store.Load()
	if err != nil || config == nil {
		t.Fatalf("回读失败: %v", err)
	}
	if config.Provider != "deepseek" || config.Model != "m-pro" || config.APIKey != "sk-fixture" {
		t.Fatalf("配置落盘错: %+v", config.View())
	}
	// token 预算永不来自后台,也不再落盘:View 里读到的恒是代码常量。
	view := config.View()
	if view.MaxInputTokens != ReplyInputTokenLimit ||
		view.MaxIntentOutputTokens != IntentOutputTokenLimit ||
		view.MaxReplyOutputTokens != ReplyOutputTokenLimit {
		t.Fatalf("token 预算不再由代码常量固定: %+v", view)
	}
	// 0600:key 落盘必须是私有文件。
	info, err := os.Stat(filepath.Join(dir, ProviderConfigFilename))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("配置文件权限错: %v err=%v", info.Mode().Perm(), err)
	}
}

func TestApplyBackendCredentialsIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewProviderConfigStore(dir)
	credentials := BackendProviderCredentials{
		BaseURL: "https://api.deepseek.com", APIKey: "sk-fixture", Model: "m-pro",
	}
	if _, err := store.ApplyBackendCredentials(credentials); err != nil {
		t.Fatalf("首次应用失败: %v", err)
	}
	applied, err := store.ApplyBackendCredentials(credentials)
	if err != nil {
		t.Fatalf("重复应用出错: %v", err)
	}
	if applied {
		t.Fatal("同值重复应用不该再写盘")
	}
}

func TestApplyBackendCredentialsKeepsLocalValueWhenBackendFieldEmpty(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewProviderConfigStore(dir)
	seed := DefaultProviderConfig()
	seed.BaseURL = "https://api.deepseek.com"
	seed.APIKey = "sk-local"
	seed.Provider = "deepseek"
	if err := store.Save(seed); err != nil {
		t.Fatalf("种子配置写入失败: %v", err)
	}
	// 后台只下发 key:base_url 与 model 保留本机原值,后台清空一项不该打掉可用配置。
	applied, err := store.ApplyBackendCredentials(BackendProviderCredentials{APIKey: "sk-from-backend"})
	if err != nil || !applied {
		t.Fatalf("部分覆盖失败: applied=%v err=%v", applied, err)
	}
	config, _ := store.Load()
	if config.APIKey != "sk-from-backend" {
		t.Fatalf("后台 key 未覆盖: %+v", config.View())
	}
	if config.BaseURL != seed.BaseURL || config.Model != seed.Model {
		t.Fatalf("后台空字段抹掉了本机值: %+v", config.View())
	}
}

func TestApplyBackendCredentialsRejectsIllegalBaseURLWithoutDamagingLocal(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewProviderConfigStore(dir)
	seed := DefaultProviderConfig()
	seed.BaseURL = "https://api.deepseek.com"
	seed.APIKey = "sk-local"
	seed.Provider = "deepseek"
	if err := store.Save(seed); err != nil {
		t.Fatalf("种子配置写入失败: %v", err)
	}
	// provider 侧仍强制 https。后台给了不合法端点时必须失败,且不得写坏本机配置。
	if _, err := store.ApplyBackendCredentials(
		BackendProviderCredentials{BaseURL: "http://api.deepseek.com"},
	); err == nil {
		t.Fatal("非 https 端点未被拒")
	}
	config, err := store.Load()
	if err != nil || config == nil || config.BaseURL != seed.BaseURL || config.APIKey != "sk-local" {
		t.Fatalf("本机配置被破坏: %+v err=%v", config, err)
	}
}

func TestRefreshBackendProviderConfigNeverPanicsOrBlocks(t *testing.T) {
	// 凭据刷新是职位配置同步旁边的一条独立失败面,任何输入都不该让调用方崩掉。
	dir := t.TempDir()
	store, _ := NewProviderConfigStore(dir)
	RefreshBackendProviderConfig(nil, []byte(singleJobResponse), nil)
	RefreshBackendProviderConfig(store, []byte(`{`), nil)
	RefreshBackendProviderConfig(store, []byte(`{"job":{"id":1}}`), nil)
	if config, err := store.Load(); err != nil || config != nil {
		t.Fatalf("坏输入却写了配置: %+v err=%v", config, err)
	}
	RefreshBackendProviderConfig(store, []byte(singleJobResponse), nil)
	config, err := store.Load()
	if err != nil || config == nil || config.APIKey != "sk-fixture" || config.Model != "m-pro" {
		t.Fatalf("正常响应未刷新配置: %+v err=%v", config, err)
	}
}

// onApplied 只在配置实际落盘后触发(2026-08-12 甲方裁决"落盘即生效"):
// 坏输入、无凭据、值未变化都不得触发换代。
func TestRefreshBackendProviderConfigInvokesOnAppliedOnlyWhenStored(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewProviderConfigStore(dir)
	applied := 0
	onApplied := func() { applied++ }
	RefreshBackendProviderConfig(nil, []byte(singleJobResponse), onApplied)
	RefreshBackendProviderConfig(store, []byte(`{`), onApplied)
	RefreshBackendProviderConfig(store, []byte(`{"job":{"id":1}}`), onApplied)
	if applied != 0 {
		t.Fatalf("未落盘却触发了换代: applied=%d", applied)
	}
	RefreshBackendProviderConfig(store, []byte(singleJobResponse), onApplied)
	if applied != 1 {
		t.Fatalf("落盘成功未触发换代: applied=%d", applied)
	}
	// 同一响应重放:值未变化不落盘,也就不触发换代。
	RefreshBackendProviderConfig(store, []byte(singleJobResponse), onApplied)
	if applied != 1 {
		t.Fatalf("值未变化却重复触发换代: applied=%d", applied)
	}
}
