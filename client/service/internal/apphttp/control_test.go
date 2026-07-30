package apphttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"recruithelper/client/service/internal/productapp"
	"recruithelper/client/service/internal/workflow"
)

type fakeWorkflowControl struct {
	mode         string
	backendJobID string
	pauseCalls   int
	resumeCalls  int
	endCalls     int
	syncJobCalls int
	batchID      string
	profileIDs   []string
	err          error
}

func (f *fakeWorkflowControl) SyncJobs(context.Context) error {
	f.syncJobCalls++
	return f.err
}

func (f *fakeWorkflowControl) Start(
	_ context.Context,
	mode, backendJobID string,
) error {
	f.mode = mode
	f.backendJobID = backendJobID
	return f.err
}

func (f *fakeWorkflowControl) Pause(context.Context) error {
	f.pauseCalls++
	return f.err
}

func (f *fakeWorkflowControl) Resume(context.Context) error {
	f.resumeCalls++
	return f.err
}

func (f *fakeWorkflowControl) End(context.Context) error {
	f.endCalls++
	return f.err
}

func (f *fakeWorkflowControl) ConfirmAll(
	_ context.Context,
	batchID string,
	profileIDs []string,
) error {
	f.batchID = batchID
	f.profileIDs = append([]string(nil), profileIDs...)
	return f.err
}

func productPOST(
	t *testing.T,
	handler http.Handler,
	target, body string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewBufferString(body))
	request.RemoteAddr = "127.0.0.1:43000"
	request.Header.Set("Authorization", "Bearer "+testBearer)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestWorkflowControlsForwardOnlyValidatedUserIntent(t *testing.T) {
	control := &fakeWorkflowControl{}
	handler := newTestAPI(t, &fakeProjections{}, WithWorkflowControl(control))

	response := productPOST(
		t,
		handler,
		"/app/workflow/start",
		`{"mode":"full","backendJobId":"42"}`,
	)
	if response.Code != http.StatusAccepted || control.mode != "full" ||
		control.backendJobID != "42" {
		t.Fatalf(
			"start status=%d mode=%q backendJobID=%q body=%s",
			response.Code,
			control.mode,
			control.backendJobID,
			response.Body.String(),
		)
	}
	response = productPOST(t, handler, "/app/workflow/pause", `{}`)
	if response.Code != http.StatusAccepted || control.pauseCalls != 1 {
		t.Fatalf("pause status=%d calls=%d", response.Code, control.pauseCalls)
	}
	response = productPOST(t, handler, "/app/workflow/resume", `{}`)
	if response.Code != http.StatusAccepted || control.resumeCalls != 1 {
		t.Fatalf("resume status=%d calls=%d", response.Code, control.resumeCalls)
	}
	response = productPOST(t, handler, "/app/workflow/end", `{}`)
	if response.Code != http.StatusAccepted || control.endCalls != 1 {
		t.Fatalf("end status=%d calls=%d", response.Code, control.endCalls)
	}
	response = productPOST(t, handler, "/app/confirmation/send",
		`{"batchId":"batch-one","profileIds":["profile-a","profile-b"]}`)
	if response.Code != http.StatusAccepted || control.batchID != "batch-one" ||
		!reflect.DeepEqual(control.profileIDs, []string{"profile-a", "profile-b"}) {
		t.Fatalf("confirm status=%d batch=%q profiles=%v", response.Code, control.batchID, control.profileIDs)
	}
}

func TestSyncJobsEndpointForwardsAndKeepsFailureDetailInside(t *testing.T) {
	control := &fakeWorkflowControl{}
	handler := newTestAPI(t, &fakeProjections{}, WithWorkflowControl(control))
	response := productPOST(t, handler, "/app/jobs/sync", `{}`)
	if response.Code != http.StatusAccepted || control.syncJobCalls != 1 {
		t.Fatalf("sync status=%d calls=%d", response.Code, control.syncJobCalls)
	}

	rejecting := &fakeWorkflowControl{
		err: errors.Join(
			productapp.ErrJobConfigUnavailable,
			errors.New("internal detail must not leave brain"),
		),
	}
	failing := newTestAPI(t, &fakeProjections{}, WithWorkflowControl(rejecting))
	response = productPOST(t, failing, "/app/jobs/sync", `{}`)
	if response.Code != http.StatusConflict ||
		strings.Contains(response.Body.String(), "internal detail") {
		t.Fatalf("失败详情泄漏到产品面: status=%d body=%s", response.Code, response.Body.String())
	}

	malformed := &fakeWorkflowControl{}
	handler = newTestAPI(t, &fakeProjections{}, WithWorkflowControl(malformed))
	if response := productPOST(t, handler, "/app/jobs/sync", `{"unexpected":1}`); response.Code != http.StatusBadRequest {
		t.Fatalf("非空请求体应被拒: status=%d", response.Code)
	}
	if malformed.syncJobCalls != 0 {
		t.Fatal("无效请求不得抵达控制层")
	}

	unavailable := newTestAPI(t, &fakeProjections{})
	if response := productPOST(t, unavailable, "/app/jobs/sync", `{}`); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("控制未就绪应为 503: status=%d", response.Code)
	}
}

func TestWorkflowControlsRejectMalformedOrUnavailableRequestsWithoutCallingControl(t *testing.T) {
	control := &fakeWorkflowControl{}
	handler := newTestAPI(t, &fakeProjections{}, WithWorkflowControl(control))

	for _, test := range []struct {
		target string
		body   string
	}{
		{target: "/app/workflow/start", body: `{"mode":"other"}`},
		{target: "/app/workflow/start", body: `{"mode":"full"}`},
		{target: "/app/workflow/start", body: `{"mode":"replyOnly","backendJobId":"42"}`},
		{target: "/app/workflow/start", body: `{"mode":"full","targetCount":30}`},
		{target: "/app/confirmation/send", body: `{"batchId":"batch-one","profileIds":[]}`},
		{target: "/app/confirmation/send", body: `{"batchId":"batch-one","profileIds":["same","same"]}`},
	} {
		response := productPOST(t, handler, test.target, test.body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", test.target, response.Code, response.Body.String())
		}
	}
	if control.mode != "" || control.batchID != "" {
		t.Fatalf("invalid requests reached control: %+v", control)
	}

	unavailable := newTestAPI(t, &fakeProjections{})
	response := productPOST(t, unavailable, "/app/workflow/pause", `{}`)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestStartFailureMapsKnownSentinelsToFixedTextOnly(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{
			name: "jobSelectionChanged",
			err:  fmt.Errorf("start: %w", productapp.ErrJobSelectionChanged),
			want: "当前职位已变化，请刷新后重试",
		},
		{
			name: "dailyWindowClosed",
			err:  workflow.ErrDailyWindowClosed,
			want: "当前不在业务运行窗口内",
		},
		{
			name: "accountUnavailable",
			err:  productapp.ErrAccountUnavailable,
			want: "没有可运行的平台账号",
		},
		{
			name: "handUnavailable",
			err:  fmt.Errorf("start: %w", productapp.ErrHandUnavailable),
			want: "Chrome 插件未连接，请确认 Chrome 已打开并加载插件后重试",
		},
		{
			name: "handAmbiguous",
			err:  productapp.ErrHandAmbiguous,
			want: "检测到多个在线插件，请只保留一个装有插件的 Chrome",
		},
		{
			name: "loginRequired",
			err:  productapp.ErrLoginRequired,
			want: "请先在 Chrome 中登录智联招聘端，再点击开始",
		},
		{
			name: "jobConfigUnavailableKeepsChainDetailInside",
			err:  errors.Join(productapp.ErrJobConfigUnavailable, errors.New("internal detail must not leave brain")),
			want: "当前职位配置不可用",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			control := &fakeWorkflowControl{err: test.err}
			handler := newTestAPI(t, &fakeProjections{}, WithWorkflowControl(control))
			response := productPOST(
				t,
				handler,
				"/app/workflow/start",
				`{"mode":"full","backendJobId":"42"}`,
			)
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("body=%s err=%v", response.Body.String(), err)
			}
			if response.Code != http.StatusConflict || body.Error != test.want ||
				bytes.Contains(response.Body.Bytes(), []byte("internal detail")) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestWorkflowControlErrorIsHiddenAndDoesNotBecomeSuccess(t *testing.T) {
	control := &fakeWorkflowControl{err: errors.New("internal detail must not leave brain")}
	handler := newTestAPI(t, &fakeProjections{}, WithWorkflowControl(control))
	response := productPOST(t, handler, "/app/workflow/start", `{"mode":"replyOnly"}`)
	if response.Code != http.StatusConflict ||
		bytes.Contains(response.Body.Bytes(), []byte("internal detail")) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
