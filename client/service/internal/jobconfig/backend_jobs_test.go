package jobconfig

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 旧后台真实响应形状：每个 prompt 块都携带 provider 凭据，发布参数只在
// documents 里。这份夹具刻意保留 apiKey，用来证明投影不会把它带出来。
const backendJobsFixture = `{
  "currentJobId": 42,
  "jobs": [
    {
      "job": {"id": 42, "name": "大客户经理", "environment": "production"},
      "scoring": {"prompt": "p", "apiKey": "sk-must-not-leak", "model": "deepseek-chat", "baseUrl": "https://provider.fixture"},
      "documents": {"打分": "p", "发布参数": "{\"职位名称\":\"大客户经理\"}"},
      "missingDocs": []
    },
    {
      "job": {"id": 7, "name": "理财顾问", "environment": "test"},
      "scoring": {"prompt": "p", "apiKey": "sk-must-not-leak", "model": "deepseek-chat", "baseUrl": "https://provider.fixture"},
      "documents": {"打分": "p", "发布参数": "   "},
      "missingDocs": ["客户事实库"]
    },
    {
      "job": {"id": 9, "name": "家庭资产配置顾问", "environment": "test"},
      "documents": {"打分": "p"},
      "missingDocs": []
    }
  ]
}`

func TestParseBackendJobsProjectsPublishParamsTriStateAndCurrentJob(t *testing.T) {
	jobs, err := ParseBackendJobs([]byte(backendJobsFixture))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("职位条数错误: %d", len(jobs))
	}

	// 填了内容的发布参数才可发布。
	if jobs[0].PublishParams != PublishParamsPresent || !jobs[0].IsCurrent {
		t.Fatalf("当前职位投影错误: %+v", jobs[0])
	}
	if jobs[0].JobID != "42" || jobs[0].JobName != "大客户经理" || jobs[0].Environment != "production" {
		t.Fatalf("职位标识投影错误: %+v", jobs[0])
	}
	if jobs[0].DocumentCount != 2 {
		t.Fatalf("文档计数错误: %+v", jobs[0])
	}

	// 空白内容是存量职位的常态，绝不能当成可发布。
	if jobs[1].PublishParams != PublishParamsEmpty || jobs[1].IsCurrent {
		t.Fatalf("空发布参数投影错误: %+v", jobs[1])
	}
	if len(jobs[1].MissingDocs) != 1 || jobs[1].MissingDocs[0] != "客户事实库" {
		t.Fatalf("缺失文档未透传: %+v", jobs[1])
	}

	// 完全没有这份文档的职位同样不可发布。
	if jobs[2].PublishParams != PublishParamsAbsent || jobs[2].IsCurrent {
		t.Fatalf("缺发布参数投影错误: %+v", jobs[2])
	}
}

func TestParseBackendJobsNeverCarriesProviderCredentialOrDocumentBody(t *testing.T) {
	jobs, err := ParseBackendJobs([]byte(backendJobsFixture))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	projected, err := json.Marshal(jobs)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sk-must-not-leak", "provider.fixture", "deepseek-chat", "职位名称"} {
		if strings.Contains(string(projected), secret) {
			t.Fatalf("投影泄漏了 %q: %s", secret, projected)
		}
	}
}

func TestParseBackendJobsAcceptsDisabledPluralEndpointEmptyList(t *testing.T) {
	// 止血开关 MULTI_JOB_CONFIGS_DISABLED 打开时后台返回 200 + 空 jobs，
	// 这是合法响应而不是故障，表格显示 0 行即可。
	jobs, err := ParseBackendJobs([]byte(`{"currentJobId": null, "jobs": []}`))
	if err != nil || len(jobs) != 0 {
		t.Fatalf("空列表应被接受: jobs=%+v err=%v", jobs, err)
	}
}

func TestParseBackendJobsRejectsUnknownShape(t *testing.T) {
	for name, raw := range map[string]string{
		"非 JSON":     `not-json`,
		"jobs 缺 job": `{"jobs": [{"documents": {}}]}`,
		"job id 非法":  `{"jobs": [{"job": {"id": 0, "name": "x"}}]}`,
	} {
		if _, err := ParseBackendJobs([]byte(raw)); !errors.Is(err, ErrBackendJobsInvalid) {
			t.Fatalf("%s 未被拒绝: err=%v", name, err)
		}
	}
}

func TestFetchAllUsesPluralEndpointWithDocumentsAndDoesNotRetry(t *testing.T) {
	var calls int
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != allJobsPath {
			t.Fatalf("意外请求: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 3 ||
			body["machineId"] != testMachineID || body["licenseToken"] != "token-private" ||
			body["includeDocuments"] != true {
			t.Fatalf("请求体错误: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"currentJobId":1,"jobs":[]}`))
	}))
	defer backend.Close()
	store, _ := NewConfigStore(t.TempDir())
	if err := store.Save(Config{BaseURL: backend.URL, MachineID: testMachineID, LicenseToken: "token-private"}); err != nil {
		t.Fatal(err)
	}
	source := NewSource(store, backend.Client(), fixedMachineID)
	raw, err := source.FetchAll(context.Background())
	if err != nil || calls != 1 || string(raw) != `{"currentJobId":1,"jobs":[]}` {
		t.Fatalf("职位列表读取失败: raw=%s calls=%d err=%v", raw, calls, err)
	}
}

func TestFetchAllRejectsMachineMismatchBeforeNetwork(t *testing.T) {
	var calls int
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer backend.Close()
	store, _ := NewConfigStore(t.TempDir())
	_ = store.Save(Config{BaseURL: backend.URL, MachineID: strings.Repeat("b", 64), LicenseToken: "token-private"})
	_, err := NewSource(store, backend.Client(), fixedMachineID).FetchAll(context.Background())
	if !errors.Is(err, ErrMachineMismatch) || calls != 0 {
		t.Fatalf("机器不匹配未在网络前拦截: calls=%d err=%v", calls, err)
	}
}
