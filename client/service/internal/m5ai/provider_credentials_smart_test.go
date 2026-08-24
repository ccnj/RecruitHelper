package m5ai

import "testing"

// 聪明ai块在响应顶层(AGENTS.md「LLM provider 直连」2026-08-24 增补),单职位与
// 多职位形态后台都放顶层,因此提取不区分形状。
func TestExtractSmartProviderCredentialsReadsTopLevelBlock(t *testing.T) {
	raw := `{
      "currentJobId": 7,
      "jobs": [` + singleJobResponse + `],
      "smartAi": {"apiKey": "sk-smart", "baseUrl": "https://api.deepseek.com", "model": "deepseek-v4-pro,deepseek-v4-flash"}
    }`
	credentials, err := ExtractSmartProviderCredentials([]byte(raw))
	if err != nil {
		t.Fatalf("提取失败: %v", err)
	}
	if credentials.APIKey != "sk-smart" || credentials.BaseURL != "https://api.deepseek.com" {
		t.Fatalf("聪明ai凭据提取错: %+v", credentials)
	}
	if credentials.Model != "deepseek-v4-pro" {
		t.Fatalf("聪明ai model 未取链首项: %q", credentials.Model)
	}
}

func TestExtractSmartProviderCredentialsAbsentIsEmpty(t *testing.T) {
	// 客户级 block 里配满了凭据,但顶层没有 smartAi 块:聪明ai必须判"后台没配",
	// 绝不能把客户级凭据认作聪明ai——两组互不回落正是本设计的目的。
	credentials, err := ExtractSmartProviderCredentials([]byte(singleJobResponse))
	if err != nil {
		t.Fatalf("块缺失被当成错误: %v", err)
	}
	if !credentials.empty() {
		t.Fatalf("没有 smartAi 块却提取到凭据: %+v", credentials)
	}
}

func TestExtractSmartProviderCredentialsRejectsBrokenJSON(t *testing.T) {
	if _, err := ExtractSmartProviderCredentials([]byte(`{`)); err == nil {
		t.Fatal("坏 JSON 未被拒")
	}
}

func TestRefreshSmartProviderConfigWritesOwnFileOnly(t *testing.T) {
	dir := t.TempDir()
	userStore, err := NewProviderConfigStore(dir)
	if err != nil {
		t.Fatalf("建用户 store 失败: %v", err)
	}
	smartStore, err := NewSmartProviderConfigStore(dir)
	if err != nil {
		t.Fatalf("建聪明ai store 失败: %v", err)
	}
	raw := `{
      "communication": {"prompt": "p", "apiKey": "sk-user", "baseUrl": "https://api.doubao.com", "model": "doubao-pro"},
      "smartAi": {"apiKey": "sk-smart", "baseUrl": "https://api.deepseek.com", "model": "deepseek-v4-pro"}
    }`
	smartApplied := false
	RefreshBackendProviderConfig(userStore, []byte(raw), nil)
	RefreshSmartProviderConfig(smartStore, []byte(raw), func() { smartApplied = true })
	if !smartApplied {
		t.Fatal("聪明ai落盘未触发换代回调")
	}
	userConfig, err := userStore.Load()
	if err != nil || userConfig == nil {
		t.Fatalf("用户配置回读失败: %v", err)
	}
	if userConfig.Model != "doubao-pro" || userConfig.APIKey != "sk-user" {
		t.Fatalf("用户配置被聪明ai污染: %+v", userConfig.View())
	}
	smartConfig, err := smartStore.Load()
	if err != nil || smartConfig == nil {
		t.Fatalf("聪明ai配置回读失败: %v", err)
	}
	if smartConfig.Model != "deepseek-v4-pro" || smartConfig.APIKey != "sk-smart" ||
		smartConfig.BaseURL != "https://api.deepseek.com" {
		t.Fatalf("聪明ai配置落盘错: %+v", smartConfig.View())
	}
}

func TestRefreshSmartProviderConfigAbsentBlockKeepsExistingFile(t *testing.T) {
	// 后台没配聪明ai时不动本机既有文件,也不触发换代——沿用旧凭据继续服务,
	// 与用户级刷新"后台该项为空保留本机原值"同向。
	dir := t.TempDir()
	smartStore, err := NewSmartProviderConfigStore(dir)
	if err != nil {
		t.Fatalf("建聪明ai store 失败: %v", err)
	}
	saved := ProviderConfig{
		Provider: "deepseek", Model: "deepseek-v4-pro",
		BaseURL: "https://api.deepseek.com", APIKey: "sk-old",
	}
	if err := smartStore.Save(saved); err != nil {
		t.Fatalf("预置聪明ai配置失败: %v", err)
	}
	called := false
	RefreshSmartProviderConfig(smartStore, []byte(`{"jobs": []}`), func() { called = true })
	if called {
		t.Fatal("块缺失不该触发换代")
	}
	config, err := smartStore.Load()
	if err != nil || config == nil || config.APIKey != "sk-old" {
		t.Fatalf("既有聪明ai配置被清掉: config=%+v err=%v", config, err)
	}
}
