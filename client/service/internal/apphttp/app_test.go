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
	overviewReq    store.AppOverviewRequest
	confirmationID string
	candidateQuery store.AppCandidateListQuery
	candidateID    string
	detailErr      error
}

func (f *fakeProjections) AppOverview(req store.AppOverviewRequest) (*store.AppOverviewProjection, error) {
	f.overviewReq = req
	return &store.AppOverviewProjection{Job: store.AppJobProjection{SyncStatus: "missing"}}, nil
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

func (f *fakeProjections) AppCandidateDetail(profileID string) (*store.AppCandidateDetailProjection, error) {
	f.candidateID = profileID
	if f.detailErr != nil {
		return nil, f.detailErr
	}
	return &store.AppCandidateDetailProjection{
		Candidate: store.AppCandidateListItem{ProfileID: profileID},
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
				CurrentBatchID: "batch-private", WorkflowMode: "full",
			}, nil
		}),
	)
	res := request(t, handler, http.MethodGet, "/app/overview", "127.0.0.1:43000", testBearer)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if fake.overviewReq.CurrentBatchID != "batch-private" || !fake.overviewReq.Now.Equal(now) {
		t.Fatalf("overview request=%+v", fake.overviewReq)
	}
	if strings.Contains(res.Body.String(), "batch-private") {
		t.Fatalf("runtime 内部批次引用不应单独暴露: %s", res.Body.String())
	}
	var body struct {
		Runtime RuntimeSnapshot `json:"runtime"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil ||
		body.Runtime.CustomerName != "合成客户" || !body.Runtime.Authorized {
		t.Fatalf("unexpected body=%s err=%v", res.Body.String(), err)
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
	handler := newTestAPI(t, fake)
	res := request(t, handler, http.MethodGet,
		"/app/candidates?view=communicating&search=%E5%80%99%E9%80%89&limit=20&offset=1",
		"127.0.0.1:43000", testBearer)
	if res.Code != http.StatusOK || fake.candidateQuery.View != store.AppCandidateViewCommunicating ||
		fake.candidateQuery.Search != "候选" || fake.candidateQuery.Limit != 20 ||
		fake.candidateQuery.Offset != 1 {
		t.Fatalf("status=%d query=%+v body=%s", res.Code, fake.candidateQuery, res.Body.String())
	}

	res = request(t, handler, http.MethodGet,
		"/app/candidates/P-sensitive", "127.0.0.1:43000", testBearer)
	if res.Code != http.StatusInternalServerError ||
		strings.Contains(res.Body.String(), "candidate plaintext") {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}
