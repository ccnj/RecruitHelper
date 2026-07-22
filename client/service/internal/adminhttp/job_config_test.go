package adminhttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"recruithelper/client/service/internal/jobconfig"
	"recruithelper/client/service/internal/store"
)

func syntheticCurrentJobConfig(t *testing.T) []byte {
	t.Helper()
	documents := map[string]string{
		"候选人筛选": `{"fixture":true}`,
		"固定规则":  "",
		"固定话术":  `{"fixture":true}`,
		"多轮沟通":  "简历={简历}\n时段={推荐时段}\n历史={对话历史}\n输出={话术_序列}",
		"客户事实库": "fixture://facts",
		"意向判断":  "招呼={招呼语}\n回复={回复}",
		"打分":    "fixture://score",
		"招呼语":   "fixture://greeting",
		"沉默追问":  "fixture://silence",
		"职位筛选":  `[]`,
	}
	block := func(prompt string) map[string]any {
		return map[string]any{
			"prompt": prompt, "apiKey": "old-provider-secret", "model": "old-provider-model",
			"baseUrl": "https://old-provider.fixture",
		}
	}
	payload := map[string]any{
		"job":       map[string]any{"id": 19, "name": "合成职位", "environment": "online"},
		"documents": documents,
		"scoring":   block(documents["打分"]), "greeting": block(documents["招呼语"]),
		"communication": block(documents["多轮沟通"]), "intent": block(documents["意向判断"]),
		"silenceFollowup": block(documents["沉默追问"]),
		"facts":           map[string]any{"content": documents["客户事实库"]},
		"fixedPhrases":    map[string]any{"content": documents["固定话术"], "scenes": map[string]any{}},
		"fixedRules":      map[string]any{"content": documents["固定规则"]},
		"filters":         map[string]any{}, "candidateSelection": map[string]any{"minScore": 5},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestJobConfigSourceSaveAndCurrentSyncSmoke(t *testing.T) {
	var calls int
	backendPayload := syntheticCurrentJobConfig(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/client/job-config" {
			t.Errorf("意外旧后台请求: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 2 ||
			body["machineId"] != "machine-private" || body["licenseToken"] != "token-private" {
			t.Errorf("旧后台请求体错误: %+v", body)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(backendPayload)
	}))
	defer backend.Close()

	dataDir := t.TempDir()
	st, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	configStore, err := jobconfig.NewConfigStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	source := jobconfig.NewSource(configStore, backend.Client())
	api := New(st, newFakeAdminHub(), nil, nil, nil, "").SetJobConfigSource(source)
	mux := http.NewServeMux()
	api.Routes(mux)

	configBody := `{"base_url":` + mustJSONString(t, backend.URL) + `,"machine_id":"machine-private","license_token":"token-private"}`
	configRequest := httptest.NewRequest(http.MethodPost, "/admin/job-config/source", strings.NewReader(configBody))
	configRequest.Header.Set("Content-Type", "application/json")
	configResponse := httptest.NewRecorder()
	mux.ServeHTTP(configResponse, configRequest)
	if configResponse.Code != http.StatusOK || !strings.Contains(configResponse.Body.String(), `"configured":true`) {
		t.Fatalf("保存旧后台配置失败: code=%d body=%s", configResponse.Code, configResponse.Body.String())
	}
	for _, forbidden := range []string{backend.URL, "machine-private", "token-private"} {
		if strings.Contains(configResponse.Body.String(), forbidden) {
			t.Fatalf("配置响应泄漏 %q", forbidden)
		}
	}

	for attempt := 0; attempt < 2; attempt++ {
		syncRequest := httptest.NewRequest(http.MethodPost, "/admin/job-config/sync-current", strings.NewReader(`{}`))
		syncRequest.Header.Set("Content-Type", "application/json")
		syncResponse := httptest.NewRecorder()
		mux.ServeHTTP(syncResponse, syncRequest)
		if syncResponse.Code != http.StatusOK || !strings.Contains(syncResponse.Body.String(), `"documentCount":10`) {
			t.Fatalf("同步当前职位失败: attempt=%d code=%d body=%s", attempt, syncResponse.Code, syncResponse.Body.String())
		}
		for _, forbidden := range []string{"old-provider-secret", "old-provider-model", "https://old-provider.fixture"} {
			if strings.Contains(syncResponse.Body.String(), forbidden) {
				t.Fatalf("同步响应泄漏旧 provider 配置 %q", forbidden)
			}
		}
	}
	summaries, err := st.JobAIContextRevisionSummaries()
	if err != nil || len(summaries) != 1 || summaries[0].SourceKind != "legacyJobConfig" || calls != 2 {
		t.Fatalf("同步未幂等收敛: summaries=%+v calls=%d err=%v", summaries, calls, err)
	}
}

func mustJSONString(t *testing.T, value string) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
