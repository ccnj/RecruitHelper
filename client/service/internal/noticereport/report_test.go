package noticereport

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"recruithelper/contract/gen/go/protocol"
)

func int64Ptr(v int64) *int64 { return &v }

func sampleData() protocol.AccountReadNoticesData {
	return protocol.AccountReadNoticesData{
		ObservedAt: 1788320000000,
		Notices: []protocol.PlatformNotice{
			{NoticeId: "6841347103", MessageType: int64Ptr(5), Title: "职位审核通过",
				Content: "您发布的职位【财富传承顾问】审核已通过并上线", NoticeTimeMs: int64Ptr(1785635435566), IsRead: true},
			{NoticeId: " 6841346803 ", MessageType: nil, Title: "职位审核未通过",
				Content: "您的职位【养老金融创业合伙人】审核未通过", NoticeTimeMs: nil, IsRead: false},
			{NoticeId: "   ", MessageType: int64Ptr(1), Title: "没有 id 的条目", Content: "x", IsRead: true},
		},
	}
}

func readyTarget() (Target, bool) {
	return Target{BaseURL: "http://backend.test/", MachineID: "M1", LicenseToken: "T1"}, true
}

// 载荷是白名单:逐字段抄自手侧结果,鉴权对由 Upload 填,序列化后不能多出任何键。
func TestReportBuildsWhitelistedPayload(t *testing.T) {
	var captured Payload
	reporter := &Reporter{
		ClientVersion: "3.25.0",
		Target:        readyTarget,
		Upload: func(_ context.Context, target Target, payload Payload) error {
			payload.MachineID = target.MachineID
			payload.LicenseToken = target.LicenseToken
			captured = payload
			return nil
		},
	}
	if err := reporter.Report(context.Background(), "zhilian", sampleData()); err != nil {
		t.Fatal(err)
	}
	if captured.Platform != "zhilian" || captured.ObservedAt != 1788320000000 || captured.ClientVersion != "3.25.0" {
		t.Fatalf("头部字段不对: %+v", captured)
	}
	if len(captured.Notices) != 2 {
		t.Fatalf("空 id 条目应被跳过,其余照抄: %+v", captured.Notices)
	}
	first, second := captured.Notices[0], captured.Notices[1]
	if first.NoticeID != "6841347103" || first.MessageType == nil || *first.MessageType != 5 ||
		first.NoticeTimeMs == nil || *first.NoticeTimeMs != 1785635435566 || !first.IsRead ||
		first.Title != "职位审核通过" || !strings.Contains(first.Content, "财富传承顾问") {
		t.Fatalf("第一条抄错: %+v", first)
	}
	if second.NoticeID != "6841346803" || second.MessageType != nil || second.NoticeTimeMs != nil || second.IsRead {
		t.Fatalf("第二条 null 与去空白处理不对: %+v", second)
	}
	encoded, err := json.Marshal(captured)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &keys); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"machineId", "licenseToken", "clientVersion", "platform", "observedAt", "notices"} {
		if _, ok := keys[key]; !ok {
			t.Fatalf("缺白名单键 %s: %s", key, encoded)
		}
	}
	if len(keys) != 6 {
		t.Fatalf("载荷出现白名单之外的键: %s", encoded)
	}
	var notices []map[string]json.RawMessage
	if err := json.Unmarshal(keys["notices"], &notices); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"noticeId", "messageType", "title", "content", "noticeTimeMs", "isRead"} {
		if _, ok := notices[0][key]; !ok {
			t.Fatalf("通知条目缺白名单键 %s: %s", key, keys["notices"])
		}
	}
	if len(notices[0]) != 6 {
		t.Fatalf("通知条目出现白名单之外的键: %s", keys["notices"])
	}
	if string(notices[1]["messageType"]) != "null" || string(notices[1]["noticeTimeMs"]) != "null" {
		t.Fatalf("平台没给的字段应原样为 null: %s", keys["notices"])
	}
}

// 未激活(授权未就绪)静默跳过;空列表不上传;上传错误原样返回给调用方记日志。
func TestReportSkipsAndPropagates(t *testing.T) {
	calls := 0
	upload := func(context.Context, Target, Payload) error {
		calls++
		return nil
	}
	notReady := &Reporter{Target: func() (Target, bool) { return Target{}, false }, Upload: upload}
	if err := notReady.Report(context.Background(), "zhilian", sampleData()); err != nil || calls != 0 {
		t.Fatalf("未激活应静默跳过: err=%v calls=%d", err, calls)
	}
	empty := &Reporter{Target: readyTarget, Upload: upload}
	if err := empty.Report(context.Background(), "zhilian", protocol.AccountReadNoticesData{ObservedAt: 1}); err != nil || calls != 0 {
		t.Fatalf("空列表不应上传: err=%v calls=%d", err, calls)
	}
	invalid := &Reporter{Target: func() (Target, bool) { return Target{BaseURL: "http://x"}, true }, Upload: upload}
	if err := invalid.Report(context.Background(), "zhilian", sampleData()); err == nil || calls != 0 {
		t.Fatalf("缺鉴权对应报错且不上传: err=%v calls=%d", err, calls)
	}
	boom := errors.New("HTTP 500")
	failing := &Reporter{Target: readyTarget, Upload: func(context.Context, Target, Payload) error { return boom }}
	if err := failing.Report(context.Background(), "zhilian", sampleData()); !errors.Is(err, boom) {
		t.Fatalf("上传错误应原样返回: %v", err)
	}
	var nilReporter *Reporter
	if err := nilReporter.Report(context.Background(), "zhilian", sampleData()); err == nil {
		t.Fatal("未接线应报错")
	}
}
