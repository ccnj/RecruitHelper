package m5ai

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderConfigIsPrivateFileAndViewNeverReturnsKey(t *testing.T) {
	store, err := NewProviderConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := configuredProvider("https://provider.invalid/v1")
	if err := store.Save(config); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || loaded == nil || loaded.APIKey != config.APIKey {
		t.Fatalf("配置未无损读取: loaded=%+v err=%v", loaded, err)
	}
	viewRaw := mustJSON(t, loaded.View())
	if strings.Contains(viewRaw, config.APIKey) || strings.Contains(viewRaw, config.BaseURL) || strings.Contains(viewRaw, "api_key") ||
		!strings.Contains(viewRaw, `"keyConfigured":true`) || !strings.Contains(viewRaw, `"baseUrlConfigured":true`) {
		t.Fatalf("masked view 泄漏 key 或缺少状态: %s", viewRaw)
	}
	info, err := os.Stat(filepath.Join(filepath.Dir(store.path), ProviderConfigFilename))
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("配置文件权限不是私有: mode=%v err=%v", info.Mode(), err)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
