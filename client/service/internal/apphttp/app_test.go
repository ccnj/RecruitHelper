package apphttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
)

const testBearer = "0123456789abcdef0123456789abcdef"

type fakeProjections struct {
	overviewReq          store.AppOverviewRequest
	overviewCalls        int
	currentJob           store.AppJobProjection
	currentJobErr        error
	currentJobCalls      int
	confirmationID       string
	candidateQuery       store.AppCandidateListQuery
	candidateQueryDetail store.AppCandidateDetailQuery
	detailErr            error
}

func (f *fakeProjections) AppOverview(req store.AppOverviewRequest) (*store.AppOverviewProjection, error) {
	f.overviewReq = req
	f.overviewCalls++
	return &store.AppOverviewProjection{Job: store.AppJobProjection{SyncStatus: "missing"}}, nil
}

func (f *fakeProjections) AppCurrentJob() (store.AppJobProjection, error) {
	f.currentJobCalls++
	if f.currentJobErr != nil {
		return store.AppJobProjection{}, f.currentJobErr
	}
	if f.currentJob.SyncStatus == "" {
		return store.AppJobProjection{SyncStatus: "missing"}, nil
	}
	return f.currentJob, nil
}

func (f *fakeProjections) AppConfirmation(batchID string) (*store.AppConfirmationProjection, error) {
	f.confirmationID = batchID
	return &store.AppConfirmationProjection{
		Available: true, BatchID: batchID,
		Candidates: []store.AppConfirmationCandidate{},
	}, nil
}

func (f *fakeProjections) AppCandidates(query store.AppCandidateListQuery) (*store.AppCandidateListProjection, error) {
	f.candidateQuery = query
	return &store.AppCandidateListProjection{
		View: query.View, Items: []store.AppCandidateListItem{},
	}, nil
}

func (f *fakeProjections) AppCandidateDetail(query store.AppCandidateDetailQuery) (*store.AppCandidateDetailProjection, error) {
	f.candidateQueryDetail = query
	if f.detailErr != nil {
		return nil, f.detailErr
	}
	return &store.AppCandidateDetailProjection{
		Candidate: store.AppCandidateListItem{ProfileID: query.ProfileID},
		Messages:  []store.AppMessageSummary{},
		Actions:   []store.AppActionSummary{},
	}, nil
}

func newTestAPI(t *testing.T, fake *fakeProjections, options ...Option) http.Handler {
	t.Helper()
	api, err := New(fake, testBearer, options...)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	api.Routes(mux)
	return mux
}

func request(t *testing.T, handler http.Handler, method, target, remote, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = remote
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}

func TestProductAPIGuardRequiresLoopbackAndBearer(t *testing.T) {
	handler := newTestAPI(t, &fakeProjections{})
	for _, test := range []struct {
		name   string
		remote string
		token  string
		want   int
	}{
		{name: "loopback authenticated", remote: "127.0.0.1:43000", token: testBearer, want: http.StatusOK},
		{name: "ipv6 loopback", remote: "[::1]:43000", token: testBearer, want: http.StatusOK},
		{name: "non loopback", remote: "192.0.2.10:43000", token: testBearer, want: http.StatusForbidden},
		{name: "missing token", remote: "127.0.0.1:43000", want: http.StatusUnauthorized},
		{name: "wrong token", remote: "127.0.0.1:43000", token: "wrong-wrong-wrong", want: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			res := request(t, handler, http.MethodGet, "/app/overview", test.remote, test.token)
			if res.Code != test.want {
				t.Fatalf("status=%d body=%s, want %d", res.Code, res.Body.String(), test.want)
			}
			if got := res.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control=%q", got)
			}
		})
	}
}

func TestTrustedLoopbackPreflightDoesNotRequireBearer(t *testing.T) {
	handler := newTestAPI(t, &fakeProjections{})
	req := httptest.NewRequest(http.MethodOptions, "/app/overview", nil)
	req.RemoteAddr = "127.0.0.1:43000"
	req.Header.Set("Origin", "null")
	req.Header.Set("Access-Control-Request-Headers", "authorization")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestOverviewUsesRuntimeBatchWithoutExposingItInRuntimeJSON(t *testing.T) {
	fake := &fakeProjections{}
	now := time.Date(2026, 7, 25, 15, 0, 0, 0, time.Local)
	handler := newTestAPI(t, fake,
		WithClock(func() time.Time { return now }),
		WithRuntimeSnapshotProvider(func(context.Context) (RuntimeSnapshot, error) {
			return RuntimeSnapshot{
				Available: true, CustomerName: "合成客户", Authorized: true,
				ProviderConfigured: true, Provider: " deepseek ", Model: " deepseek-v4-pro ",
				PluginOnline: true, PluginHealth: " ready ", PluginVersion: " 1.2.3 ",
				ContractMatch: true, BusinessWindowOpen: true,
				Platform: " zhilian ", AccountRef: " account-product ",
				CurrentBatchID: "batch-private", WorkflowMode: "full",
				CanAddBatch: true,
			}, nil
		}),
	)
	res := request(t, handler, http.MethodGet, "/app/overview", "127.0.0.1:43000", testBearer)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if fake.overviewReq.CurrentBatchID != "batch-private" ||
		fake.overviewReq.Platform != "zhilian" ||
		fake.overviewReq.AccountRef != "account-product" ||
		!fake.overviewReq.Now.Equal(now) {
		t.Fatalf("overview request=%+v", fake.overviewReq)
	}
	if strings.Contains(res.Body.String(), "batch-private") {
		t.Fatalf("runtime 内部批次引用不应单独暴露: %s", res.Body.String())
	}
	var body struct {
		Runtime RuntimeSnapshot `json:"runtime"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil ||
		body.Runtime.CustomerName != "合成客户" || !body.Runtime.Authorized ||
		!body.Runtime.ProviderConfigured || body.Runtime.Provider != "deepseek" ||
		body.Runtime.Model != "deepseek-v4-pro" || !body.Runtime.PluginOnline ||
		body.Runtime.PluginHealth != "ready" || body.Runtime.PluginVersion != "1.2.3" ||
		!body.Runtime.ContractMatch || !body.Runtime.BusinessWindowOpen ||
		!body.Runtime.CanAddBatch {
		t.Fatalf("unexpected body=%s err=%v", res.Body.String(), err)
	}
	for _, forbidden := range []string{
		"handId", "bootId", "contractHash", "caps", "api_key", "apiKey",
		"zhilian", "account-product",
	} {
		if strings.Contains(res.Body.String(), forbidden) {
			t.Fatalf("产品运行快照泄露内部字段 %q: %s", forbidden, res.Body.String())
		}
	}
}

func TestOverviewRuntimeFailureReturnsHonestUnavailableSnapshot(t *testing.T) {
	handler := newTestAPI(t, &fakeProjections{},
		WithRuntimeSnapshotProvider(func(context.Context) (RuntimeSnapshot, error) {
			return RuntimeSnapshot{
				Available: true, ProviderConfigured: true, Provider: "should-not-leak",
			}, errors.New("runtime unavailable")
		}),
	)
	res := request(t, handler, http.MethodGet, "/app/overview", "127.0.0.1:43000", testBearer)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		Runtime RuntimeSnapshot `json:"runtime"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Runtime.Available || body.Runtime.ProviderConfigured ||
		body.Runtime.PluginOnline || strings.Contains(res.Body.String(), "should-not-leak") {
		t.Fatalf("运行态读取失败必须诚实降级为空快照: %s", res.Body.String())
	}
}

func TestConfirmationWithoutWorkflowBatchReturnsHonestEmptyState(t *testing.T) {
	fake := &fakeProjections{}
	handler := newTestAPI(t, fake)
	res := request(t, handler, http.MethodGet, "/app/confirmation", "127.0.0.1:43000", testBearer)
	if res.Code != http.StatusOK || fake.confirmationID != "" ||
		!strings.Contains(res.Body.String(), "workflowBatchUnavailable") {
		t.Fatalf("status=%d body=%s confirmationID=%q", res.Code, res.Body.String(), fake.confirmationID)
	}
}

func TestConfirmationIgnoresCallerChosenHistoricalBatch(t *testing.T) {
	fake := &fakeProjections{}
	handler := newTestAPI(t, fake,
		WithRuntimeSnapshotProvider(func(context.Context) (RuntimeSnapshot, error) {
			return RuntimeSnapshot{CurrentBatchID: "batch-current"}, nil
		}),
	)
	res := request(t, handler, http.MethodGet,
		"/app/confirmation?batchId=batch-historical",
		"127.0.0.1:43000", testBearer)
	if res.Code != http.StatusOK || fake.confirmationID != "batch-current" {
		t.Fatalf("status=%d body=%s confirmationID=%q", res.Code, res.Body.String(), fake.confirmationID)
	}
}

func TestCandidateRoutesValidateAndHideStoreErrors(t *testing.T) {
	fake := &fakeProjections{detailErr: errors.New("database error with candidate plaintext")}
	handler := newTestAPI(t, fake,
		WithRuntimeSnapshotProvider(func(context.Context) (RuntimeSnapshot, error) {
			return RuntimeSnapshot{Platform: "zhilian", AccountRef: "account-product"}, nil
		}),
	)
	res := request(t, handler, http.MethodGet,
		"/app/candidates?view=communicating&search=%E5%80%99%E9%80%89&limit=20&offset=1",
		"127.0.0.1:43000", testBearer)
	if res.Code != http.StatusOK || fake.candidateQuery.View != store.AppCandidateViewCommunicating ||
		fake.candidateQuery.Search != "候选" || fake.candidateQuery.Limit != 20 ||
		fake.candidateQuery.Offset != 1 || fake.candidateQuery.Platform != "zhilian" ||
		fake.candidateQuery.AccountRef != "account-product" {
		t.Fatalf("status=%d query=%+v body=%s", res.Code, fake.candidateQuery, res.Body.String())
	}

	res = request(t, handler, http.MethodGet,
		"/app/candidates/P-sensitive", "127.0.0.1:43000", testBearer)
	if res.Code != http.StatusInternalServerError ||
		fake.candidateQueryDetail.ProfileID != "P-sensitive" ||
		fake.candidateQueryDetail.Platform != "zhilian" ||
		fake.candidateQueryDetail.AccountRef != "account-product" ||
		strings.Contains(res.Body.String(), "candidate plaintext") {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestProjectionRoutesFailClosedWithoutUniqueAccountScope(t *testing.T) {
	fake := &fakeProjections{}
	handler := newTestAPI(t, fake)
	for _, target := range []string{
		"/app/overview",
		"/app/candidates?view=communicating",
	} {
		res := request(t, handler, http.MethodGet, target, "127.0.0.1:43000", testBearer)
		if res.Code != http.StatusOK {
			t.Fatalf("target=%s status=%d body=%s", target, res.Code, res.Body.String())
		}
	}
	res := request(
		t,
		handler,
		http.MethodGet,
		"/app/candidates/P-hidden",
		"127.0.0.1:43000",
		testBearer,
	)
	if res.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if fake.overviewReq.Platform != "" || fake.candidateQuery.Platform != "" ||
		fake.candidateQueryDetail.ProfileID != "" {
		t.Fatalf("空账号作用域不得进入 Store: %+v", fake)
	}
}

// 零账号(全新安装未绑定)时职位投影必须照常返回,否则职位同步成功也不可见,
// 开始按钮永远不亮,而点开始才是建立账号的动作——装机死锁(2026-08-01 真机复现)。
func TestOverviewWithoutAccountStillProjectsBoundJob(t *testing.T) {
	fake := &fakeProjections{currentJob: store.AppJobProjection{
		Available: true, BackendJobID: "42", Name: "产品经理", SyncStatus: "synced",
	}}
	handler := newTestAPI(t, fake,
		WithRuntimeSnapshotProvider(func(context.Context) (RuntimeSnapshot, error) {
			return RuntimeSnapshot{Available: true, Authorized: true}, nil
		}),
	)
	res := request(t, handler, http.MethodGet, "/app/overview", "127.0.0.1:43000", testBearer)
	if res.Code != http.StatusOK || fake.currentJobCalls != 1 || fake.overviewCalls != 0 {
		t.Fatalf("status=%d currentJobCalls=%d overviewCalls=%d body=%s",
			res.Code, fake.currentJobCalls, fake.overviewCalls, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"backendJobId":"42"`) ||
		!strings.Contains(res.Body.String(), `"syncStatus":"synced"`) {
		t.Fatalf("零账号 overview 未携带职位投影: %s", res.Body.String())
	}
}

func TestOverviewWithoutAccountFailsClosedOnProjectionError(t *testing.T) {
	fake := &fakeProjections{currentJobErr: errors.New("storage failure with internals")}
	handler := newTestAPI(t, fake,
		WithRuntimeSnapshotProvider(func(context.Context) (RuntimeSnapshot, error) {
			return RuntimeSnapshot{Available: true, Authorized: true}, nil
		}),
	)
	res := request(t, handler, http.MethodGet, "/app/overview", "127.0.0.1:43000", testBearer)
	if res.Code != http.StatusInternalServerError ||
		strings.Contains(res.Body.String(), "internals") {
		t.Fatalf("投影失败未安全收口: status=%d body=%s", res.Code, res.Body.String())
	}
}
