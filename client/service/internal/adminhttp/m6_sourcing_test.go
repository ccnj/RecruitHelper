package adminhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/patrol"
	"recruithelper/client/service/internal/store"
)

type sourcingAdminClock struct{ now time.Time }

func (c sourcingAdminClock) Now() time.Time { return c.now }

type sourcingAdminRunner struct{}

func (sourcingAdminRunner) Start(context.Context, patrol.RunRequest) (patrol.RunHandle, error) {
	return nil, nil
}

type sourcingAdminHands struct{}

func (sourcingAdminHands) State(context.Context, string) (patrol.HandState, error) {
	return patrol.HandState{Online: true}, nil
}

func sourcingAdminRevision(at time.Time) m5ai.ContextRevision {
	replyPrompt, intentPrompt, facts := "admin-reply-secret", "admin-intent-secret", "admin-facts-secret"
	documents := []m5ai.JobConfigDocument{
		{DocType: "候选人筛选", Content: `{"minScore":5}`},
		{DocType: "多轮沟通", Content: replyPrompt},
		{DocType: "客户事实库", Content: facts},
		{DocType: "意向判断", Content: intentPrompt},
		{DocType: "打分", Content: "admin-score-secret {resume_json}"},
		{DocType: "招呼语", Content: `{"prompt":"admin-greeting-secret {career_state} {resume_summary_json}"}`},
		{DocType: "职位筛选", Content: `[]`},
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].DocType < documents[j].DocType })
	return m5ai.ContextRevision{
		ContextID: "context-sourcing-admin", RevisionHash: "revision-sourcing-admin",
		SourceKind: "localImport", SourceJobRef: "71", DisplayName: "admin-position-secret",
		SourcePackage: m5ai.JobConfigDocumentPackage{Documents: documents},
		Communication: m5ai.CommunicationView{
			ReplyPrompt: replyPrompt, IntentPrompt: intentPrompt,
			CustomerFacts: facts, MappingVersion: m5ai.MappingVersion,
		},
		CreatedAt: at,
	}
}

func TestSourcingStartAndStatusExposeOnlyOperationalMetadata(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	revision := sourcingAdminRevision(now.Add(-time.Hour))
	if _, _, err := st.SaveJobAIContextRevision(revision); err != nil {
		t.Fatal(err)
	}
	key := store.AccountKey{Platform: "zhilian", AccountRef: "account-sourcing-admin"}
	if err := st.CreateAccount(&store.Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccountPrincipal(
		key, "hand-sourcing-admin", "principal-sourcing-admin",
		"session-sourcing-admin", "boot-sourcing-admin", now,
	); err != nil {
		t.Fatal(err)
	}
	manager, err := patrol.NewManager(st, sourcingAdminRunner{}, sourcingAdminHands{}, patrol.Config{
		Clock: sourcingAdminClock{now: now}, Location: time.UTC,
	})
	if err != nil {
		t.Fatal(err)
	}
	api := New(st, newFakeAdminHub(), nil, manager, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)

	start := httptest.NewRequest(http.MethodPost, "/admin/sourcing/start", strings.NewReader(
		`{"platform":"zhilian","accountRef":"account-sourcing-admin","contextRevisionHash":"revision-sourcing-admin"}`,
	))
	start.Header.Set("Content-Type", "application/json")
	startResponse := httptest.NewRecorder()
	mux.ServeHTTP(startResponse, start)
	if startResponse.Code != http.StatusOK || !strings.Contains(startResponse.Body.String(), `"enabled":true`) ||
		!strings.Contains(startResponse.Body.String(), `"captureCount":0`) {
		t.Fatalf("启动采集失败: code=%d body=%s", startResponse.Code, startResponse.Body.String())
	}

	status := httptest.NewRequest(http.MethodGet,
		"/admin/sourcing/status?platform=zhilian&accountRef=account-sourcing-admin", nil)
	statusResponse := httptest.NewRecorder()
	mux.ServeHTTP(statusResponse, status)
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), revision.RevisionHash) {
		t.Fatalf("读取采集状态失败: code=%d body=%s", statusResponse.Code, statusResponse.Body.String())
	}
	for _, forbidden := range []string{
		"admin-score-secret", "admin-greeting-secret", "admin-reply-secret",
		"admin-intent-secret", "admin-facts-secret", "admin-position-secret",
	} {
		if strings.Contains(startResponse.Body.String(), forbidden) || strings.Contains(statusResponse.Body.String(), forbidden) {
			t.Fatalf("采集管理响应泄漏配置正文或职位明文 %q", forbidden)
		}
	}
	account, err := st.AccountByKey(key)
	if err != nil || account == nil || !account.SourcingEnabled ||
		account.SourcingContextRevisionHash != revision.RevisionHash || account.EnabledAt == nil {
		t.Fatalf("start 未持久化采集配置并开启 actor: account=%+v err=%v", account, err)
	}
}
