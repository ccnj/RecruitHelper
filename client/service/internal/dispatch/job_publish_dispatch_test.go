package dispatch

import (
	"strings"
	"testing"

	"recruithelper/contract/gen/go/protocol"
)

func TestBuildPublishJobIntentIDIsStablePerJobAndPayload(t *testing.T) {
	// 同职位同参数必须得到同一个意图：这是"同一份发布参数只发一次"的全部依据，
	// HTTP 重试靠它收编原意图而不是另铸一个。
	first := BuildPublishJobIntentID("15", "payload-hash-a")
	if second := BuildPublishJobIntentID("15", "payload-hash-a"); first != second {
		t.Fatalf("同职位同参数应得到同一意图: %s vs %s", first, second)
	}
	// 改了发布参数是新意图（甲方裁决的口径：允许改完再发），平台侧"同名不重发"
	// 仍兜着。
	if changed := BuildPublishJobIntentID("15", "payload-hash-b"); changed == first {
		t.Fatal("发布参数变化后应得到新的意图")
	}
	// 不同职位绝不能撞同一个意图，否则第二个职位会被当成重试而静默跳过。
	if other := BuildPublishJobIntentID("16", "payload-hash-a"); other == first {
		t.Fatal("不同职位不得共用意图")
	}
	if !strings.HasPrefix(first, "jp-") || len(first) != len("jp-")+24 {
		t.Fatalf("意图标识形态不符: %s", first)
	}
}

func TestBuildEffectIdemKeyDistinguishesPublishTargets(t *testing.T) {
	const platform, account = "zhilian", "a-1"
	keyOf := func(jobID, payloadHash string) string {
		return BuildEffectIdemKey(platform, account, protocol.PrimJobPublishDraft, jobID,
			BuildPublishJobIntentID(jobID, payloadHash))
	}
	same := keyOf("15", "hash-a")
	if again := keyOf("15", "hash-a"); same != again {
		t.Fatalf("同职位同参数的 idemKey 必须稳定: %s vs %s", same, again)
	}
	for name, key := range map[string]string{
		"换职位":  keyOf("16", "hash-a"),
		"换发布参数": keyOf("15", "hash-b"),
	} {
		if key == same {
			t.Fatalf("%s 后 idemKey 不应相同", name)
		}
	}
}

func TestPublishJobRejectsIncompleteRequest(t *testing.T) {
	d := &Dispatcher{}
	cases := map[string]PublishJobRequest{
		"缺平台":  {AccountRef: "a-1", JobID: "15", Args: protocol.JobPrepareDraftArgs{JobName: "x"}},
		"缺账号":  {Platform: "zhilian", JobID: "15", Args: protocol.JobPrepareDraftArgs{JobName: "x"}},
		"缺职位":  {Platform: "zhilian", AccountRef: "a-1", Args: protocol.JobPrepareDraftArgs{JobName: "x"}},
		"缺职位名": {Platform: "zhilian", AccountRef: "a-1", JobID: "15"},
	}
	for name, req := range cases {
		receipt, err := d.PublishJob(req)
		if err == nil || receipt != nil {
			t.Fatalf("%s 应在任何派发之前被拒: receipt=%v err=%v", name, receipt, err)
		}
	}
}
