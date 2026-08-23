package jobclassreport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func ptr(v float64) *float64 { return &v }

func readyTarget() func() (Target, bool) {
	return func() (Target, bool) {
		return Target{BaseURL: "http://backend.test/", MachineID: "M1", LicenseToken: "T1"}, true
	}
}

// 载荷白名单:序列化后只出现条款列出的键;候选人相关的任何键都不该存在。
func TestPayloadIsWhitelistOnly(t *testing.T) {
	var got Payload
	r := &Reporter{
		Target:        readyTarget(),
		ClientVersion: "3.14.0",
		Upload: func(_ context.Context, _ Target, payload Payload) error {
			got = payload
			return nil
		},
	}
	err := r.Report(context.Background(), []Record{{
		JobID: " 94 ", JobName: "储备总监", Platform: "zhilian", Stage: StagePlanned,
		ObservedAt:     1755900000000,
		Candidates:     []Candidate{{Name: " 销售总监 ", Definition: "释义"}, {Name: "", Definition: "无名丢弃"}},
		PrefilledClass: "销售总监", ChosenClass: "保险业务管理", Source: "model",
		Confidence: ptr(1.7), Reason: "依据", Problem: "",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Records) != 1 {
		t.Fatalf("应有 1 条记录: %+v", got)
	}
	rec := got.Records[0]
	if rec.JobID != "94" || rec.Stage != StagePlanned || len(rec.Candidates) != 1 ||
		rec.Candidates[0].Name != "销售总监" || *rec.Confidence != 1 {
		t.Fatalf("记录未按白名单整形: %+v", rec)
	}
	if got.ClientVersion != "3.14.0" {
		t.Fatalf("客户端版本未带: %+v", got)
	}
	raw, _ := json.Marshal(got)
	for _, key := range []string{"jobId", "jobName", "platform", "stage", "observedAt", "candidates", "prefilledClass", "chosenClass", "source", "confidence", "reason"} {
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("载荷缺少键 %s: %s", key, raw)
		}
	}
	for _, forbidden := range []string{"displayName", "profileId", "platformUserRef", "description", "resume", "message"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("载荷出现不该有的键 %s: %s", forbidden, raw)
		}
	}
}

// 不合格记录整条丢弃:非数字 jobId、未知 stage、空平台;全丢则不上传。
func TestSanitizeDropsInvalidRecords(t *testing.T) {
	calls := 0
	r := &Reporter{
		Target: readyTarget(),
		Upload: func(_ context.Context, _ Target, payload Payload) error {
			calls++
			if len(payload.Records) != 1 || payload.Records[0].JobID != "7" {
				t.Fatalf("只应剩 jobId=7 一条: %+v", payload.Records)
			}
			return nil
		},
	}
	err := r.Report(context.Background(), []Record{
		{JobID: "abc", Platform: "zhilian", Stage: StagePlanned},
		{JobID: "5", Platform: "zhilian", Stage: "drafted"},
		{JobID: "6", Platform: "", Stage: StagePlanned},
		{JobID: "7", Platform: "zhilian", Stage: StagePublished, ChosenClass: "X"},
	})
	if err != nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	calls = 0
	if err := r.Report(context.Background(), []Record{{JobID: "0", Platform: "zhilian", Stage: StagePlanned}}); err != nil || calls != 0 {
		t.Fatalf("全部不合格时不应上传: err=%v calls=%d", err, calls)
	}
}

// 文本按服务端上限截断而不是整批被拒;候选超过 50 只留前 50。
func TestSanitizeTruncatesToServerLimits(t *testing.T) {
	long := strings.Repeat("字", 1000)
	cands := make([]Candidate, 0, 60)
	for i := 0; i < 60; i++ {
		cands = append(cands, Candidate{Name: "类别" + strings.Repeat("x", i%3), Definition: long})
	}
	var got Payload
	r := &Reporter{Target: readyTarget(), Upload: func(_ context.Context, _ Target, p Payload) error { got = p; return nil }}
	if err := r.Report(context.Background(), []Record{{
		JobID: "1", JobName: long, Platform: long, Stage: StagePlanned, Candidates: cands,
		ChosenClass: long, Reason: long, Problem: long, Source: long,
	}}); err != nil {
		t.Fatal(err)
	}
	rec := got.Records[0]
	if len([]rune(rec.JobName)) != 256 || len([]rune(rec.Platform)) != 32 || len([]rune(rec.ChosenClass)) != 64 ||
		len([]rune(rec.Reason)) != 500 || len([]rune(rec.Problem)) != 64 || len([]rune(rec.Source)) != 32 {
		t.Fatalf("截断不符: jobName=%d platform=%d class=%d reason=%d problem=%d source=%d",
			len([]rune(rec.JobName)), len([]rune(rec.Platform)), len([]rune(rec.ChosenClass)),
			len([]rune(rec.Reason)), len([]rune(rec.Problem)), len([]rune(rec.Source)))
	}
	if len(rec.Candidates) != 50 || len([]rune(rec.Candidates[0].Definition)) != 400 {
		t.Fatalf("候选上限不符: n=%d defLen=%d", len(rec.Candidates), len([]rune(rec.Candidates[0].Definition)))
	}
}

// 超过 100 条按批切开逐批上传。
func TestReportChunksOver100(t *testing.T) {
	sizes := []int{}
	r := &Reporter{Target: readyTarget(), Upload: func(_ context.Context, _ Target, p Payload) error {
		sizes = append(sizes, len(p.Records))
		return nil
	}}
	records := make([]Record, 0, 230)
	for i := 1; i <= 230; i++ {
		records = append(records, Record{JobID: "1", Platform: "zhilian", Stage: StagePlanned})
	}
	if err := r.Report(context.Background(), records); err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 3 || sizes[0] != 100 || sizes[1] != 100 || sizes[2] != 30 {
		t.Fatalf("分批不符: %v", sizes)
	}
}

// 授权未就绪静默跳过;目标不完整报错不上传。
func TestReportSkipsWhenNotReady(t *testing.T) {
	r := &Reporter{
		Target: func() (Target, bool) { return Target{}, false },
		Upload: func(context.Context, Target, Payload) error { return errors.New("不该调用") },
	}
	if err := r.Report(context.Background(), []Record{{JobID: "1", Platform: "zhilian", Stage: StagePlanned}}); err != nil {
		t.Fatalf("未就绪应静默: %v", err)
	}
	r.Target = func() (Target, bool) { return Target{BaseURL: "http://x"}, true }
	if err := r.Report(context.Background(), []Record{{JobID: "1", Platform: "zhilian", Stage: StagePlanned}}); err == nil {
		t.Fatal("缺鉴权对应报错")
	}
}

// HTTP 上传:路径、Content-Type、鉴权对进正文;非 200 报错且不回显整段。
func TestUploadPostsToBackend(t *testing.T) {
	var gotPath, gotCT string
	var gotBody map[string]any
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotPath = req.URL.Path
		gotCT = req.Header.Get("Content-Type")
		body, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	target := Target{BaseURL: server.URL + "/", MachineID: "M1", LicenseToken: "T1"}
	payload := Payload{ClientVersion: "3.14.0", Records: []Record{{JobID: "94", Platform: "zhilian", Stage: StagePublished, ChosenClass: "X"}}}
	if err := Upload(context.Background(), target, payload); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/client/job-class-report" || gotCT != "application/json" {
		t.Fatalf("path=%s ct=%s", gotPath, gotCT)
	}
	if gotBody["machineId"] != "M1" || gotBody["licenseToken"] != "T1" || gotBody["clientVersion"] != "3.14.0" {
		t.Fatalf("鉴权对/版本未进正文: %v", gotBody)
	}
	status = http.StatusUnauthorized
	err := Upload(context.Background(), target, payload)
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("非 200 应报错: %v", err)
	}
}
