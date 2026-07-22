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

func TestSourcingStartStatusAndStopExposeOnlyBatchMetadata(t *testing.T) {
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

	missingTarget := httptest.NewRequest(http.MethodPost, "/admin/sourcing/start", strings.NewReader(
		`{"platform":"zhilian","accountRef":"account-sourcing-admin","contextRevisionHash":"revision-sourcing-admin"}`,
	))
	missingTarget.Header.Set("Content-Type", "application/json")
	missingTargetResponse := httptest.NewRecorder()
	mux.ServeHTTP(missingTargetResponse, missingTarget)
	if missingTargetResponse.Code != http.StatusBadRequest {
		t.Fatalf("缺少显式 targetCount 未拒绝: code=%d body=%s", missingTargetResponse.Code, missingTargetResponse.Body.String())
	}

	start := httptest.NewRequest(http.MethodPost, "/admin/sourcing/start", strings.NewReader(
		`{"platform":"zhilian","accountRef":"account-sourcing-admin","contextRevisionHash":"revision-sourcing-admin","targetCount":150}`,
	))
	start.Header.Set("Content-Type", "application/json")
	startResponse := httptest.NewRecorder()
	mux.ServeHTTP(startResponse, start)
	startBody := startResponse.Body.String()
	if startResponse.Code != http.StatusOK || !strings.Contains(startBody, `"status":"preparing"`) ||
		!strings.Contains(startBody, `"targetCount":150`) || !strings.Contains(startBody, `"capturedCount":0`) ||
		!strings.Contains(startBody, `"remainingCount":150`) || !strings.Contains(startBody, `"batchId":"sb-`) {
		t.Fatalf("启动正式采集失败: code=%d body=%s", startResponse.Code, startBody)
	}

	status := httptest.NewRequest(http.MethodGet,
		"/admin/sourcing/status?platform=zhilian&accountRef=account-sourcing-admin", nil)
	statusResponse := httptest.NewRecorder()
	mux.ServeHTTP(statusResponse, status)
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), revision.RevisionHash) {
		t.Fatalf("读取正式采集状态失败: code=%d body=%s", statusResponse.Code, statusResponse.Body.String())
	}
	for _, forbidden := range []string{
		"account-sourcing-admin", "principal-sourcing-admin", "admin-score-secret", "admin-greeting-secret",
		"admin-reply-secret", "admin-intent-secret", "admin-facts-secret", "admin-position-secret",
		`"enabled"`, `"latest"`, `"sourceLogicalDispatchId"`,
	} {
		if strings.Contains(startBody, forbidden) || strings.Contains(statusResponse.Body.String(), forbidden) {
			t.Fatalf("采集管理响应泄漏旧状态、配置正文或身份 %q", forbidden)
		}
	}
	account, err := st.AccountByKey(key)
	if err != nil || account == nil || account.EnabledAt == nil || account.SourcingEnabled ||
		account.SourcingContextRevisionHash != "" || account.SourcingStartedAt != nil {
		t.Fatalf("start 未开启 actor 或写入 legacy sourcing 双真相: account=%+v err=%v", account, err)
	}

	stop := httptest.NewRequest(http.MethodPost, "/admin/sourcing/stop", strings.NewReader(
		`{"platform":"zhilian","accountRef":"account-sourcing-admin"}`,
	))
	stop.Header.Set("Content-Type", "application/json")
	stopResponse := httptest.NewRecorder()
	mux.ServeHTTP(stopResponse, stop)
	stopBody := stopResponse.Body.String()
	if stopResponse.Code != http.StatusOK || !strings.Contains(stopBody, `"status":"stopped"`) ||
		!strings.Contains(stopBody, `"reason":"userStopped"`) || !strings.Contains(stopBody, `"endedAt"`) {
		t.Fatalf("停止正式采集失败: code=%d body=%s", stopResponse.Code, stopBody)
	}
	account, err = st.AccountByKey(key)
	if err != nil || account == nil || account.StoppedAt == nil || account.PausedReason != patrol.PauseUserStopped {
		t.Fatalf("stop 未暂停账号 actor: account=%+v err=%v", account, err)
	}
}
