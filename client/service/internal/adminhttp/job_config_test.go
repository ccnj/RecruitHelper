package adminhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"recruithelper/client/service/internal/jobconfig"
	"recruithelper/client/service/internal/store"
)

const adminTestMachineID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

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

func TestJobConfigCurrentSyncIsIdempotent(t *testing.T) {
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
			body["machineId"] != adminTestMachineID || body["licenseToken"] != "token-private" {
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
	if err := configStore.Save(jobconfig.Config{
		BaseURL: backend.URL, MachineID: adminTestMachineID, LicenseToken: "token-private",
	}); err != nil {
		t.Fatal(err)
	}
	source := jobconfig.NewSource(configStore, backend.Client(), func(context.Context) (string, error) {
		return adminTestMachineID, nil
	})
	api := New(st, newFakeAdminHub(), nil, nil, nil, "").SetJobConfigSource(source)
	mux := http.NewServeMux()
	api.Routes(mux)

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

func TestLegacyActivationBindsThenSynchronizesCurrentJob(t *testing.T) {
	const inviteCode = "invite-private"
	const token = "token-new-private"
	var calls []string
	backendPayload := syntheticCurrentJobConfig(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatalf("旧后台请求无法解析")
		}
		switch r.URL.Path {
		case "/api/v1/client/bind":
			if len(body) != 2 || body["inviteCode"] != inviteCode || body["machineId"] != adminTestMachineID {
				t.Fatalf("bind 请求体错误: %+v", body)
			}
			_, _ = w.Write([]byte(`{"authorized":true,"status":"bound","licenseToken":"` + token + `","customer":{"customerId":9,"customerName":"合成客户","status":"active"}}`))
		case "/api/v1/client/job-config":
			if len(body) != 2 || body["machineId"] != adminTestMachineID || body["licenseToken"] != token {
				t.Fatalf("job-config 请求体错误: %+v", body)
			}
			_, _ = w.Write(backendPayload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()

	dataDir := t.TempDir()
	st, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	configStore, _ := jobconfig.NewConfigStore(dataDir)
	source := jobconfig.NewSource(configStore, backend.Client(), func(context.Context) (string, error) {
		return adminTestMachineID, nil
	})
	api := New(st, newFakeAdminHub(), nil, nil, nil, "").SetJobConfigSource(source)
	mux := http.NewServeMux()
	api.Routes(mux)

	body := `{"base_url":` + mustJSONString(t, backend.URL) + `,"invite_code":` + mustJSONString(t, inviteCode) + `}`
	request := httptest.NewRequest(http.MethodPost, "/admin/job-config/activate", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"activated":true`) ||
		!strings.Contains(response.Body.String(), `"synced":true`) ||
		!strings.Contains(response.Body.String(), `"documentCount":10`) {
		t.Fatalf("激活并同步失败: code=%d body=%s", response.Code, response.Body.String())
	}
	for _, secret := range []string{inviteCode, token, adminTestMachineID, backend.URL} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("激活响应泄漏秘密 %q", secret)
		}
	}
	if strings.Join(calls, ",") != "/api/v1/client/bind,/api/v1/client/job-config" {
		t.Fatalf("请求顺序错误: %v", calls)
	}
	loaded, err := configStore.Load()
	if err != nil || loaded == nil || loaded.LicenseToken != token || loaded.Customer.Name != "合成客户" {
		t.Fatalf("正式授权未落盘: loaded=%+v err=%v", loaded, err)
	}
	summaries, err := st.JobAIContextRevisionSummaries()
	if err != nil || len(summaries) != 1 || summaries[0].SourceKind != "legacyJobConfig" {
		t.Fatalf("正式职位配置未导入: summaries=%+v err=%v", summaries, err)
	}

	statusRequest := httptest.NewRequest(http.MethodGet, "/admin/job-config/source", nil)
	statusResponse := httptest.NewRecorder()
	mux.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK ||
		!strings.Contains(statusResponse.Body.String(), `"machineIdentityReady":true`) ||
		!strings.Contains(statusResponse.Body.String(), `"machineMatch":true`) {
		t.Fatalf("激活状态错误: code=%d body=%s", statusResponse.Code, statusResponse.Body.String())
	}
}

func TestLegacyActivationKeepsCredentialWhenAutomaticSyncFails(t *testing.T) {
	const token = "token-new-private"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/client/bind" {
			_, _ = w.Write([]byte(`{"authorized":true,"status":"bound","licenseToken":"` + token + `","customer":{"customerId":9,"customerName":"合成客户","status":"active"}}`))
			return
		}
		http.Error(w, "upstream fixture", http.StatusServiceUnavailable)
	}))
	defer backend.Close()

	dataDir := t.TempDir()
	st, _ := store.Open(dataDir)
	defer st.Close()
	configStore, _ := jobconfig.NewConfigStore(dataDir)
	source := jobconfig.NewSource(configStore, backend.Client(), func(context.Context) (string, error) {
		return adminTestMachineID, nil
	})
	api := New(st, newFakeAdminHub(), nil, nil, nil, "").SetJobConfigSource(source)
	mux := http.NewServeMux()
	api.Routes(mux)

	body := `{"base_url":` + mustJSONString(t, backend.URL) + `,"invite_code":"invite-private"}`
	request := httptest.NewRequest(http.MethodPost, "/admin/job-config/activate", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"activated":true`) ||
		!strings.Contains(response.Body.String(), `"synced":false`) ||
		!strings.Contains(response.Body.String(), `"syncError"`) {
		t.Fatalf("同步失败未保留激活成功语义: code=%d body=%s", response.Code, response.Body.String())
	}
	loaded, err := configStore.Load()
	if err != nil || loaded == nil || loaded.LicenseToken != token {
		t.Fatalf("同步失败丢失正式授权: loaded=%+v err=%v", loaded, err)
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
