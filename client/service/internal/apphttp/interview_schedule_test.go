package apphttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"recruithelper/client/service/internal/m5ai"
)

func postSchedule(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/app/interview-schedule", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("Authorization", "Bearer "+testBearer)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}

// 读端补齐七天 key：空天返回空数组而不是缺 key，前端不必区分 undefined 与 []。
func TestInterviewScheduleGetReturnsAllSevenDays(t *testing.T) {
	handler := newTestAPI(t, &fakeProjections{})
	res := request(t, handler, http.MethodGet, "/app/interview-schedule", "127.0.0.1:5000", testBearer)
	if res.Code != http.StatusOK {
		t.Fatalf("状态码=%d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		Schedule map[string][]m5ai.InterviewWindow `json:"schedule"`
		Weekdays []string                          `json:"weekdays"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Schedule) != 7 {
		t.Fatalf("应补齐七天: %+v", body.Schedule)
	}
	if len(body.Weekdays) != 7 || body.Weekdays[0] != "周一" || body.Weekdays[6] != "周日" {
		t.Fatalf("星期顺序应由脑侧给出: %v", body.Weekdays)
	}
	if windows, ok := body.Schedule["周六"]; !ok || windows == nil || len(windows) != 0 {
		t.Fatalf("空天应为空数组而非缺 key: %+v", body.Schedule["周六"])
	}
	if len(body.Schedule["周一"]) != 1 || body.Schedule["周一"][0].Start != "09:00" {
		t.Fatalf("未配置时应返回内置默认: %+v", body.Schedule["周一"])
	}
}

// 配置损坏时诚实报错，绝不退回内置默认——否则配置页会显示一张库里并不存在的表。
func TestInterviewScheduleGetSurfacesCorruptConfig(t *testing.T) {
	handler := newTestAPI(t, &fakeProjections{scheduleErr: errors.New("可面试时段配置无法解析")})
	res := request(t, handler, http.MethodGet, "/app/interview-schedule", "127.0.0.1:5000", testBearer)
	if res.Code != http.StatusConflict {
		t.Fatalf("损坏配置应报错: 状态码=%d body=%s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), "09:00") {
		t.Fatalf("报错响应不得夹带默认周表: %s", res.Body.String())
	}
}

func TestInterviewSchedulePostSavesAndRejects(t *testing.T) {
	fake := &fakeProjections{}
	handler := newTestAPI(t, fake)
	res := postSchedule(t, handler, `{"schedule":{"周二":[{"start":"10:00","end":"12:00"}]}}`)
	if res.Code != http.StatusOK {
		t.Fatalf("合法周表应保存: 状态码=%d body=%s", res.Code, res.Body.String())
	}
	if len(fake.savedSchedule["周二"]) != 1 || fake.savedSchedule["周二"][0].End != "12:00" {
		t.Fatalf("落库周表漂移: %+v", fake.savedSchedule)
	}
	for _, bad := range []string{
		`{}`,
		`{"schedule":null}`,
		`not json`,
	} {
		if res := postSchedule(t, handler, bad); res.Code != http.StatusBadRequest {
			t.Fatalf("非法请求体应 400: body=%q 状态码=%d", bad, res.Code)
		}
	}
}

// 空表的拒绝原因必须原样带回，UI 要能把"你清空了"和"存坏了"区分开。
func TestInterviewSchedulePostReportsRejectionReason(t *testing.T) {
	fake := &fakeProjections{saveScheduleErr: errors.New("可面试时段不得为空")}
	handler := newTestAPI(t, fake)
	res := postSchedule(t, handler, `{"schedule":{"周一":[]}}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("空表应 400: 状态码=%d", res.Code)
	}
	if !strings.Contains(res.Body.String(), "不得为空") {
		t.Fatalf("拒绝原因应如实带回: %s", res.Body.String())
	}
}

func TestInterviewScheduleRequiresLoopbackAndBearer(t *testing.T) {
	handler := newTestAPI(t, &fakeProjections{})
	if res := request(t, handler, http.MethodGet,
		"/app/interview-schedule", "203.0.113.9:5000", testBearer); res.Code != http.StatusForbidden {
		t.Fatalf("非 loopback 应 403: %d", res.Code)
	}
	if res := request(t, handler, http.MethodGet,
		"/app/interview-schedule", "127.0.0.1:5000", ""); res.Code != http.StatusUnauthorized {
		t.Fatalf("缺 bearer 应 401: %d", res.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/app/interview-schedule",
		strings.NewReader(`{"schedule":{"周一":[{"start":"09:00","end":"10:00"}]}}`))
	req.RemoteAddr = "127.0.0.1:5000"
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("写端缺 bearer 应 401: %d", res.Code)
	}
}
