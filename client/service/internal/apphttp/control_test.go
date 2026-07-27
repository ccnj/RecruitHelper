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
	batchID      string
	profileIDs   []string
	err          error
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
			want: "没有唯一可运行的平台账号",
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
