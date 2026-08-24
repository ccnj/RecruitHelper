package m5ai

import "testing"

func TestExtractSubSmartProviderCredentialsReadsTopLevelBlock(t *testing.T) {
	raw := `{
      "currentJobId": 7,
      "jobs": [` + singleJobResponse + `],
      "smartAi": {"apiKey": "sk-smart", "baseUrl": "https://api.deepseek.com", "model": "deepseek-v4-pro"},
      "subSmartAi": {"apiKey": "sk-sub", "baseUrl": "https://api.deepseek.com", "model": "deepseek-v4-flash,deepseek-v4-lite"}
    }`
	credentials, err := ExtractSubSmartProviderCredentials([]byte(raw))
	if err != nil {
		t.Fatalf("提取失败: %v", err)
	}
	if credentials.APIKey != "sk-sub" || credentials.BaseURL != "https://api.deepseek.com" {
		t.Fatalf("次聪明ai凭据提取错: %+v", credentials)
	}
	if credentials.Model != "deepseek-v4-flash" {
		t.Fatalf("次聪明ai model 未取链首项: %q", credentials.Model)
	}
}

func TestExtractSubSmartProviderCredentialsIgnoresOtherBlocks(t *testing.T) {
	// 客户级 block 与 smartAi 都配满,但没有 subSmartAi:必须判"后台没配",
	// 绝不能把另外两组凭据认作次聪明——三组各归各是本设计的目的。
	raw := `{
      "communication": {"prompt": "p", "apiKey": "sk-user", "baseUrl": "https://api.doubao.com", "model": "doubao-pro"},
      "smartAi": {"apiKey": "sk-smart", "baseUrl": "https://api.deepseek.com", "model": "deepseek-v4-pro"}
    }`
	credentials, err := ExtractSubSmartProviderCredentials([]byte(raw))
	if err != nil {
		t.Fatalf("块缺失被当成错误: %v", err)
	}
	if !credentials.empty() {
		t.Fatalf("没有 subSmartAi 块却提取到凭据: %+v", credentials)
	}
}

func TestRefreshSubSmartProviderConfigWritesOwnFileOnly(t *testing.T) {
	dir := t.TempDir()
	smartStore, err := NewSmartProviderConfigStore(dir)
	if err != nil {
		t.Fatalf("建聪明ai store 失败: %v", err)
	}
	subStore, err := NewSubSmartProviderConfigStore(dir)
	if err != nil {
		t.Fatalf("建次聪明ai store 失败: %v", err)
	}
	raw := `{
      "smartAi": {"apiKey": "sk-smart", "baseUrl": "https://api.deepseek.com", "model": "deepseek-v4-pro"},
      "subSmartAi": {"apiKey": "sk-sub", "baseUrl": "https://api.deepseek.com", "model": "deepseek-v4-flash"}
    }`
	subApplied := false
	RefreshSmartProviderConfig(smartStore, []byte(raw), nil)
	RefreshSubSmartProviderConfig(subStore, []byte(raw), func() { subApplied = true })
	if !subApplied {
		t.Fatal("次聪明ai落盘未触发换代回调")
	}
	smartConfig, err := smartStore.Load()
	if err != nil || smartConfig == nil || smartConfig.Model != "deepseek-v4-pro" ||
		smartConfig.APIKey != "sk-smart" {
		t.Fatalf("聪明ai配置被次聪明污染: %+v err=%v", smartConfig, err)
	}
	subConfig, err := subStore.Load()
	if err != nil || subConfig == nil || subConfig.Model != "deepseek-v4-flash" ||
		subConfig.APIKey != "sk-sub" {
		t.Fatalf("次聪明ai配置落盘错: %+v err=%v", subConfig, err)
	}
}

func TestRefreshSubSmartProviderConfigAbsentBlockKeepsExistingFile(t *testing.T) {
	dir := t.TempDir()
	subStore, err := NewSubSmartProviderConfigStore(dir)
	if err != nil {
		t.Fatalf("建次聪明ai store 失败: %v", err)
	}
	saved := ProviderConfig{
		Provider: "deepseek", Model: "deepseek-v4-flash",
		BaseURL: "https://api.deepseek.com", APIKey: "sk-old",
	}
	if err := subStore.Save(saved); err != nil {
		t.Fatalf("预置次聪明ai配置失败: %v", err)
	}
	called := false
	RefreshSubSmartProviderConfig(subStore, []byte(`{"jobs": []}`), func() { called = true })
	if called {
		t.Fatal("块缺失不该触发换代")
	}
	config, err := subStore.Load()
	if err != nil || config == nil || config.APIKey != "sk-old" {
		t.Fatalf("既有次聪明ai配置被清掉: config=%+v err=%v", config, err)
	}
}
