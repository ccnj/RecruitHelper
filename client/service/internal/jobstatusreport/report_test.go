package jobstatusreport

import (
	"context"
	"errors"
	"testing"

	"recruithelper/contract/gen/go/protocol"
)

type fakeSource struct {
	raw []byte
	err error
}

func (f fakeSource) FetchAll(context.Context) ([]byte, error) { return f.raw, f.err }

// 后台职位清单的最小合法形态,与 jobconfig.ParseBackendJobs 的解码口径对齐。
const backendJobsRaw = `{"jobs":[
  {"job":{"id":17,"name":"销售顾问","environment":"prod"},"documents":{}},
  {"job":{"id":18,"name":"保障顾问","environment":"prod"},"documents":{}},
  {"job":{"id":19,"name":"不存在的职位","environment":"prod"},"documents":{}}
]}`

func sections() protocol.JobReadPublishedListData {
	return protocol.JobReadPublishedListData{
		Sections: []protocol.JobPostingSection{
			{Label: "在线中", Names: []string{"销售顾问"}},
			{Label: "未上线", Names: []string{"保障顾问"}},
		},
		ObservedAt: 1755000000000,
	}
}

func TestReportBuildsWhitelistPayload(t *testing.T) {
	var got Payload
	reporter := &Reporter{
		Source: fakeSource{raw: []byte(backendJobsRaw)},
		Target: func() (Target, bool) {
			return Target{BaseURL: "http://backend", LicenseToken: "token-1"}, true
		},
		Upload: func(_ context.Context, target Target, payload Payload) error {
			if target.LicenseToken != "token-1" {
				t.Fatalf("target 不符: %+v", target)
			}
			got = payload
			return nil
		},
	}
	if err := reporter.Report(context.Background(), sections()); err != nil {
		t.Fatal(err)
	}
	if got.ObservedAt != 1755000000000 || len(got.Jobs) != 3 {
		t.Fatalf("载荷不符: %+v", got)
	}
	want := map[string]string{"17": "在线中", "18": "未上线", "19": "平台未见"}
	for _, job := range got.Jobs {
		if want[job.JobID] != job.Status {
			t.Fatalf("职位 %s 状态不符: got=%q want=%q", job.JobID, job.Status, want[job.JobID])
		}
	}
}

func TestReportSkipsWhenNotActivated(t *testing.T) {
	reporter := &Reporter{
		Source: fakeSource{raw: []byte(backendJobsRaw)},
		Target: func() (Target, bool) { return Target{}, false },
		Upload: func(context.Context, Target, Payload) error {
			t.Fatal("未激活不该上传")
			return nil
		},
	}
	if err := reporter.Report(context.Background(), sections()); err != nil {
		t.Fatalf("未激活应静默跳过: %v", err)
	}
}

func TestReportSurfacesSourceFailure(t *testing.T) {
	reporter := &Reporter{
		Source: fakeSource{err: errors.New("backend down")},
		Target: func() (Target, bool) {
			return Target{BaseURL: "http://backend", LicenseToken: "token-1"}, true
		},
		Upload: func(context.Context, Target, Payload) error { return nil },
	}
	if err := reporter.Report(context.Background(), sections()); err == nil {
		t.Fatal("职位清单读取失败应返回错误交调用方记日志")
	}
}
