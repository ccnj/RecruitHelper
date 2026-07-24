package adminhttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/testfixture"
)

func syntheticAdminJobBundle() map[string]any {
	documents := map[string]string{
		"候选人筛选": `{"minScore":5}`,
		"固定规则":  "",
		"固定话术":  `{"fixture":true}`,
		"多轮沟通":  "简历={简历}\n时段={推荐时段}\n历史={对话历史}",
		"客户事实库": "PRIVATE-FACTS-FIXTURE",
		"意向判断":  "招呼={招呼语}\n回复={回复}",
		"打分":    "fixture-score",
		"招呼语":   "fixture-greeting",
		"沉默追问":  "fixture-silence",
		"职位筛选":  testfixture.SourcingFiltersDocument,
	}
	block := func(prompt string) map[string]any {
		return map[string]any{
			"prompt": prompt, "apiKey": "PRIVATE-LEGACY-KEY",
			"model": "PRIVATE-LEGACY-MODEL", "baseUrl": "https://private-legacy.invalid",
		}
	}
	return map[string]any{
		"job":       map[string]any{"id": 77, "name": "合成职位", "environment": "online"},
		"documents": documents,
		"scoring":   block(documents["打分"]), "greeting": block(documents["招呼语"]),
		"communication": block(documents["多轮沟通"]), "intent": block(documents["意向判断"]),
		"silenceFollowup": block(documents["沉默追问"]),
		"facts":           map[string]any{"content": documents["客户事实库"]},
		"fixedPhrases":    map[string]any{"content": documents["固定话术"], "scenes": map[string]any{}},
		"fixedRules":      map[string]any{"content": documents["固定规则"]},
		"filters":         map[string]any{}, "candidateSelection": map[string]any{"minScore": 5},
	}
}

func activeM5ContextAPIFixture(t *testing.T) (*store.Store, http.Handler) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hub := newFakeAdminHub()
	seedGreetingAPI(t, st, hub)
	sender := &greetingAPISender{}
	dispatcher := dispatch.New(st, sender)
	sender.dispatcher = dispatcher
	if _, err := dispatcher.SendGreeting(dispatch.SendGreetingRequest{
		IntentID: greetingTestIntent, ProfileID: greetingTestProfile, Text: greetingTestText,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SelectM5TrialProfile(greetingTestProfile, "trial-context-api", "user", time.Now()); err != nil {
		t.Fatal(err)
	}
	api := New(st, hub, dispatcher, nil, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)
	return st, mux
}

func TestM5ContextImportAndExplicitCurrentTrialBinding(t *testing.T) {
	st, mux := activeM5ContextAPIFixture(t)
	defer st.Close()
	payload, _ := json.Marshal(map[string]any{"bundle": syntheticAdminJobBundle()})
	request := httptest.NewRequest(http.MethodPost, "/admin/m5/contexts/import", strings.NewReader(string(payload)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("整包导入失败: code=%d body=%s", response.Code, response.Body.String())
	}
	for _, forbidden := range []string{
		"PRIVATE-LEGACY-KEY", "PRIVATE-LEGACY-MODEL", "private-legacy.invalid",
		"PRIVATE-FACTS-FIXTURE", greetingTestUserRef, greetingConversation,
	} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("导入响应泄漏 %q: %s", forbidden, response.Body.String())
		}
	}
	var imported struct {
		Contexts []m5ContextView `json:"contexts"`
	}
	if json.Unmarshal(response.Body.Bytes(), &imported) != nil || len(imported.Contexts) != 1 || imported.Contexts[0].DocumentCount != 10 {
		t.Fatalf("导入元数据错误: %+v", imported)
	}

	bindRaw, _ := json.Marshal(map[string]string{
		"contextId": imported.Contexts[0].ContextID, "revisionHash": imported.Contexts[0].RevisionHash,
	})
	bindRequest := httptest.NewRequest(http.MethodPost, "/admin/m5/context-binding", strings.NewReader(string(bindRaw)))
	bindRequest.Header.Set("Content-Type", "application/json")
	bindResponse := httptest.NewRecorder()
	mux.ServeHTTP(bindResponse, bindRequest)
	if bindResponse.Code != http.StatusOK || !strings.Contains(bindResponse.Body.String(), `"status":"active"`) {
		t.Fatalf("显式绑定失败: code=%d body=%s", bindResponse.Code, bindResponse.Body.String())
	}
	for _, forbidden := range []string{greetingTestUserRef, greetingConversation, greetingTestPosition, "PRIVATE-FACTS-FIXTURE"} {
		if strings.Contains(bindResponse.Body.String(), forbidden) {
			t.Fatalf("绑定响应泄漏 %q: %s", forbidden, bindResponse.Body.String())
		}
	}

	get := httptest.NewRequest(http.MethodGet, "/admin/m5/context-binding", nil)
	getResponse := httptest.NewRecorder()
	mux.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK || !strings.Contains(getResponse.Body.String(), imported.Contexts[0].RevisionHash) {
		t.Fatalf("active binding 读取失败: code=%d body=%s", getResponse.Code, getResponse.Body.String())
	}
}

func TestM5ContextImportNeverPersistsLegacyProviderCredentialsToBrainDB(t *testing.T) {
	dataDir := t.TempDir()
	st, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	api := New(st, newFakeAdminHub(), nil, nil, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)
	payload, _ := json.Marshal(map[string]any{"bundle": syntheticAdminJobBundle()})
	request := httptest.NewRequest(http.MethodPost, "/admin/m5/contexts/import", strings.NewReader(string(payload)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("导入失败: %s", response.Body.String())
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"brain.db", "brain.db-wal"} {
		raw, err := os.ReadFile(filepath.Join(dataDir, name))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"PRIVATE-LEGACY-KEY", "PRIVATE-LEGACY-MODEL", "https://private-legacy.invalid"} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatalf("%s 持久化了旧 provider 凭据 %q", name, forbidden)
			}
		}
	}
}
