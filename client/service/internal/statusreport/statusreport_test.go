package statusreport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
)

// allowedPayloadKeys 是载荷允许出现的全部 JSON 键。
//
// 这不是一份说明,是一道闸:加字段必须先改这里,改的时候人会被迫看一眼
// 「这个字段会不会把候选人身份或业务正文带出本机」。上报是本产品少数几条
// 出本机的通道之一,而这一条按裁决常开、无开关。
var allowedPayloadKeys = map[string]bool{
	// 身份与时刻
	"machineId": true, "licenseToken": true, "appVersion": true,
	"schemaVersion": true, "reportedAt": true, "localDate": true,
	// 分块
	"account": true, "job": true, "workflow": true, "batch": true,
	"today": true, "total": true, "health": true,
	// account
	"bound": true, "platform": true,
	// job
	"backendJobId": true, "name": true, "syncStatus": true, "lastSyncedAt": true,
	// workflow
	"status": true, "stage": true, "mode": true, "endReason": true,
	"failureReason": true, "startedAt": true, "pausedAt": true, "endedAt": true,
	// batch
	"batchId": true, "reason": true, "targetCount": true, "capturedCount": true,
	"scoredCount": true, "selectedCount": true, "sentCount": true,
	"generationFailedCount": true, "sendFailedCount": true, "suspectCount": true,
	// today
	"captured": true, "rated": true, "confirmed": true, "greeted": true,
	"replies": true, "wechat": true, "interviewInvites": true,
	"appointments": true, "elapsedInterviews": true, "byJob": true,
	// total
	"interviewed": true,
	// health
	"handOnline": true, "handContractMatch": true, "extensionVersion": true,
	"lastHeartbeatAgoMs": true, "witnessJournalOpen": true,
	"witnessOutboxPending": true, "pendingManualVerdicts": true,
	"manualRequiredProfiles": true, "llmProviderConfigured": true,
	"brainUptimeSec": true,
}

func TestPayloadKeysStayInsideWhitelist(t *testing.T) {
	payload, err := Collect(fullDeps())
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	for _, key := range jsonKeysOf(t, payload) {
		if !allowedPayloadKeys[key] {
			t.Fatalf("载荷出现未登记的键 %q —— 先确认它不会带出候选人信息,再加进 allowedPayloadKeys", key)
		}
	}
}

// 这条守的是本包的核心设计:载荷由独立结构体逐字段拼装,不整体序列化产品投影。
// store.AppOverviewProjection.TodayInterviews 里带着候选人 DisplayName,一旦有人
// "简化"成直接把投影发出去,这条会红。
func TestCandidateDisplayNameNeverReachesPayload(t *testing.T) {
	deps := fullDeps()
	source := deps.Store.(*fakeStore)
	source.overview.TodayInterviews = []store.AppInterviewSummary{
		{ProfileID: "profile-1", DisplayName: "张三", JobName: "顾问", StartsAtMs: 1},
	}

	payload, err := Collect(deps)
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	for _, forbidden := range []string{"张三", "profile-1", "displayName", "profileId"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("载荷里出现了 %q: %s", forbidden, encoded)
		}
	}
}

func TestCollectMapsTodayAndHealthFields(t *testing.T) {
	payload, err := Collect(fullDeps())
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}

	if payload.LocalDate != "2026-08-06" {
		t.Fatalf("本地日期错: %s", payload.LocalDate)
	}
	if !payload.Account.Bound || payload.Account.Platform != "zhilian" {
		t.Fatalf("账号块错: %+v", payload.Account)
	}
	if payload.Today.Captured != 71 || payload.Today.Greeted != 32 || payload.Today.Wechat != 3 {
		t.Fatalf("今日计数错: %+v", payload.Today)
	}
	if len(payload.Today.ByJob) != 1 || payload.Today.ByJob[0].Captured != 71 {
		t.Fatalf("职位维计数错: %+v", payload.Today.ByJob)
	}
	if payload.Total.Greeted != 1200 {
		t.Fatalf("累计计数错: %+v", payload.Total)
	}
	if payload.Health.PendingManualVerdicts != 2 {
		t.Fatalf("待裁决条数错: %d", payload.Health.PendingManualVerdicts)
	}
	if payload.Health.ManualRequiredProfiles != 5 {
		t.Fatalf("转人工人数错: %d", payload.Health.ManualRequiredProfiles)
	}
	if payload.Health.BrainUptimeSec != 3600 {
		t.Fatalf("uptime 错: %d", payload.Health.BrainUptimeSec)
	}
	// 失败原因原样带出,不做枚举映射(2026-08-06 裁决)。
	if payload.Workflow.FailureReason != "startInterruptedBeforeBatch" {
		t.Fatalf("失败原因被改写: %q", payload.Workflow.FailureReason)
	}
}

// 零账号(全新安装、还没登录)必须照常上报:那正是最需要被看见的状态。
func TestCollectStillReportsWhenNoAccountBound(t *testing.T) {
	deps := fullDeps()
	deps.Runtime = func() (Runtime, error) { return Runtime{}, nil }

	payload, err := Collect(deps)
	if err != nil {
		t.Fatalf("零账号时应照常上报: %v", err)
	}
	if payload.Account.Bound {
		t.Fatal("零账号却报了已绑定")
	}
	if payload.Job.Name != "平安健康保障顾问" {
		t.Fatalf("零账号时职位仍应有值: %+v", payload.Job)
	}
	if payload.Today.Captured != 0 || payload.Today.ByJob == nil {
		t.Fatalf("零账号时今日计数应为零且 byJob 非 nil: %+v", payload.Today)
	}
}

// 读不出来就整体失败,不降级成"报零":零在运营眼里等于"今天没干活",
// 那个方向的误判会让人不去查。
func TestCollectFailsInsteadOfReportingZeroOnReadError(t *testing.T) {
	deps := fullDeps()
	deps.Store.(*fakeStore).countsErr = errors.New("库读失败")

	if _, err := Collect(deps); err == nil {
		t.Fatal("读失败时不应返回一份全零快照")
	}
}

func TestUploadPostsJSONAndIgnoresExtraReceiptFields(t *testing.T) {
	var gotPath, gotType string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		// 回执里塞一堆额外字段:本机必须一个都不理会。这条通道只上行,
		// 回执一旦成为配置来源就成了下行控制面。
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"id":9,"command":"stop","intervalSeconds":1}`))
	}))
	defer server.Close()

	payload, err := Collect(fullDeps())
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	err = Upload(context.Background(), payload, Target{
		BaseURL: server.URL, MachineID: "M1", LicenseToken: "T1", AppVersion: "1.9.1",
	})
	if err != nil {
		t.Fatalf("上传失败: %v", err)
	}

	if gotPath != "/api/v1/client/status" {
		t.Fatalf("端点错: %s", gotPath)
	}
	if gotType != "application/json" {
		t.Fatalf("Content-Type 错: %s", gotType)
	}
	if body["machineId"] != "M1" || body["licenseToken"] != "T1" {
		t.Fatalf("身份未随请求送出: %v", body["machineId"])
	}
}

func TestUploadRefusesWithoutAuthorization(t *testing.T) {
	err := Upload(context.Background(), &Payload{}, Target{BaseURL: "http://127.0.0.1:1"})
	if err == nil {
		t.Fatal("缺 machineId/licenseToken 时不应发出请求")
	}
}

func TestUploadErrorKeepsServerBodyShort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, strings.Repeat("x", 4096), http.StatusBadRequest)
	}))
	defer server.Close()

	err := Upload(context.Background(), &Payload{}, Target{
		BaseURL: server.URL, MachineID: "M1", LicenseToken: "T1",
	})
	if err == nil {
		t.Fatal("400 应当报错")
	}
	// 错误信息会进普通日志,而请求里带着 licenseToken;服务端回显整段的话就漏了。
	if len(err.Error()) > 700 {
		t.Fatalf("错误信息未截断,长度 %d", len(err.Error()))
	}
}

func TestNoiseGateLogsOnceThenDigestsHourly(t *testing.T) {
	base := time.Date(2026, 8, 6, 9, 0, 0, 0, time.Local)
	gate := &noiseGate{digestEvery: time.Hour}

	if action := gate.decide(false, base); action.kind != noiseError {
		t.Fatalf("首次失败应响亮报: %v", action.kind)
	}
	// 一小时内的后续失败保持沉默 —— 288 条/天会把业务日志挤出 32MB 轮转窗口。
	for minute := 5; minute < 60; minute += 5 {
		if action := gate.decide(false, base.Add(time.Duration(minute)*time.Minute)); action.kind != noiseSilent {
			t.Fatalf("第 %d 分钟不该写日志: %v", minute, action.kind)
		}
	}
	action := gate.decide(false, base.Add(time.Hour))
	if action.kind != noiseDigest || action.failures != 13 {
		t.Fatalf("满一小时应汇总一条: %+v", action)
	}

	recovered := gate.decide(true, base.Add(90*time.Minute))
	if recovered.kind != noiseRecovered || recovered.failures != 13 {
		t.Fatalf("恢复应报一条并带累计次数: %+v", recovered)
	}
	if next := gate.decide(true, base.Add(2*time.Hour)); next.kind != noiseSilent {
		t.Fatalf("持续成功应当沉默: %v", next.kind)
	}
}

func TestRunnerSkipsSilentlyUntilAuthorized(t *testing.T) {
	deps := RunnerDeps{
		Deps:   fullDeps(),
		Target: func() (Target, bool) { return Target{}, false },
		Upload: func(context.Context, *Payload, Target) error {
			t.Fatal("授权未就绪时不应上传")
			return nil
		},
	}
	runOnce(context.Background(), deps, &noiseGate{})
}

func TestRunnerUploadsCollectedPayload(t *testing.T) {
	var uploaded *Payload
	deps := RunnerDeps{
		Deps:   fullDeps(),
		Target: func() (Target, bool) { return Target{BaseURL: "http://x", MachineID: "M1", LicenseToken: "T1"}, true },
		Upload: func(_ context.Context, payload *Payload, _ Target) error {
			uploaded = payload
			return nil
		},
	}

	runOnce(context.Background(), deps, &noiseGate{})

	if uploaded == nil || uploaded.Today.Greeted != 32 {
		t.Fatalf("未上传采集到的快照: %+v", uploaded)
	}
}

func fullDeps() Deps {
	now := time.Date(2026, 8, 6, 14, 35, 2, 0, time.Local)
	started := now.Add(-time.Hour)
	greetedAt := now.Add(-2 * time.Hour)
	return Deps{
		Store: &fakeStore{
			job: store.AppJobProjection{
				Available: true, BackendJobID: "16",
				Name: "平安健康保障顾问", SyncStatus: "ok",
			},
			run: &store.ProductWorkflowRun{
				Status: "running", Stage: "sendingGreetings", Mode: "full",
				FailureReason: "startInterruptedBeforeBatch",
				StartedAt:     greetedAt,
			},
			suspects: []store.CmdRecord{{MsgID: "m1"}, {MsgID: "m2"}},
			counts: store.StatusReportCounts{
				TodayCaptured:          71,
				ManualRequiredProfiles: 5,
				TodayCapturedByJob: []store.StatusReportJobCount{
					{BackendJobID: "16", Name: "平安健康保障顾问", Captured: 71},
				},
			},
			overview: &store.AppOverviewProjection{
				Funnel: store.AppFunnelProjection{
					Available: true, BatchID: "batch-1", Stage: "sendingGreetings",
					TargetCount: 96, CapturedCount: 71, SentCount: 32,
					SuspectCount: 1,
				},
				Statistics: store.AppOverviewStatistics{
					TodayGreeted: metric(32), TodayWechat: metric(3),
					TodayNewReplies: metric(11), TodayNewAppointments: metric(2),
					TotalGreeted: metric(1200),
				},
			},
		},
		Runtime: func() (Runtime, error) {
			return Runtime{Platform: "zhilian", AccountRef: "acc-1", CurrentBatchID: "batch-1"}, nil
		},
		Hand: func() HandHealth {
			return HandHealth{Online: true, ContractMatch: true, ExtensionVersion: "1.9.1", JournalOpen: 3}
		},
		ProviderConfigured: func() bool { return true },
		AppVersion:         "1.9.1",
		StartedAt:          started,
		Now:                func() time.Time { return now },
	}
}

func metric(value int64) store.AppMetric {
	return store.AppMetric{Value: &value, Exact: true}
}

type fakeStore struct {
	job       store.AppJobProjection
	run       *store.ProductWorkflowRun
	suspects  []store.CmdRecord
	counts    store.StatusReportCounts
	countsErr error
	overview  *store.AppOverviewProjection
}

func (f *fakeStore) AppCurrentJob() (store.AppJobProjection, error) { return f.job, nil }

func (f *fakeStore) AppOverview(store.AppOverviewRequest) (*store.AppOverviewProjection, error) {
	return f.overview, nil
}

func (f *fakeStore) StatusReportCounts(
	string, string, time.Time, time.Time,
) (store.StatusReportCounts, error) {
	if f.countsErr != nil {
		return store.StatusReportCounts{}, f.countsErr
	}
	return f.counts, nil
}

func (f *fakeStore) SuspectCmds() ([]store.CmdRecord, error) { return f.suspects, nil }

func (f *fakeStore) LatestProductWorkflowRun() (*store.ProductWorkflowRun, error) {
	return f.run, nil
}

// jsonKeysOf 递归收集序列化结果里出现的全部键。
func jsonKeysOf(t *testing.T, value any) []string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	seen := map[string]bool{}
	var walk func(node any)
	walk = func(node any) {
		switch typed := node.(type) {
		case map[string]any:
			for key, child := range typed {
				seen[key] = true
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(decoded)
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
