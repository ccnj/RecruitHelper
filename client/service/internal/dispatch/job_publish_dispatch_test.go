package dispatch

import (
	"strings"
	"testing"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

func TestBuildPublishJobIntentIDIsStablePerJobAndPayload(t *testing.T) {
	// 同职位同参数必须得到同一个意图：这是"同一份发布参数只发一次"的全部依据，
	// HTTP 重试靠它收编原意图而不是另铸一个。
	first := BuildPublishJobIntentID("15", "payload-hash-a", 1)
	if second := BuildPublishJobIntentID("15", "payload-hash-a", 1); first != second {
		t.Fatalf("同职位同参数应得到同一意图: %s vs %s", first, second)
	}
	// 改了发布参数是新意图（甲方裁决的口径：允许改完再发），平台侧"同名不重发"
	// 仍兜着。
	if changed := BuildPublishJobIntentID("15", "payload-hash-b", 1); changed == first {
		t.Fatal("发布参数变化后应得到新的意图")
	}
	// 不同职位绝不能撞同一个意图，否则第二个职位会被当成重试而静默跳过。
	if other := BuildPublishJobIntentID("16", "payload-hash-a", 1); other == first {
		t.Fatal("不同职位不得共用意图")
	}
	if !strings.HasPrefix(first, "jp-") || len(first) != len("jp-")+24 {
		t.Fatalf("意图标识形态不符: %s", first)
	}
	// 序号 1 不带后缀:本裁决之前落下的意图必须仍然命中同一身份，否则一次
	// 升级就会把历史发布当成"没发过"。
	if strings.Contains(first, "-1") && strings.HasSuffix(first, "-1") {
		t.Fatalf("序号 1 不得带后缀: %s", first)
	}
	// 干净失败后重来一次必须是**新**意图:旧意图是永久终局，不能原地复活。
	retry := BuildPublishJobIntentID("15", "payload-hash-a", 2)
	if retry == first || !strings.HasSuffix(retry, "-2") {
		t.Fatalf("第二次尝试应是带序号的新意图: %s（首次 %s）", retry, first)
	}
	if third := BuildPublishJobIntentID("15", "payload-hash-a", 3); third == retry {
		t.Fatal("不同尝试序号不得共用意图")
	}
}

func TestBuildEffectIdemKeyDistinguishesPublishTargets(t *testing.T) {
	const platform, account = "zhilian", "a-1"
	keyOf := func(jobID, payloadHash string) string {
		return BuildEffectIdemKey(platform, account, protocol.PrimJobPublishDraft, jobID,
			BuildPublishJobIntentID(jobID, payloadHash, 1))
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

// 账本闸放宽到"已终局即可重来"之后，唯一不能松的是未收束态：suspect 是
// 结果未知等人裁，在途是还没回来，两者被新尝试绕过就等于 idemKey 冻结失效。
func TestPublishAttemptSettledNeverBypassesUnsettledIntents(t *testing.T) {
	settled := []store.EffectIntentStatus{
		store.EffectIntentOk, store.EffectIntentResolvedOk,
		store.EffectIntentFailed, store.EffectIntentResolvedFailed,
	}
	for _, status := range settled {
		if !publishAttemptSettled(store.EffectIntent{Status: status}) {
			t.Fatalf("%s 已终局，运营显式再发应允许递进尝试序号", status)
		}
	}
	unsettled := []store.EffectIntentStatus{
		store.EffectIntentSuspect, store.EffectIntentDispatching,
		store.EffectIntentReconciling, store.EffectIntentVerifying,
	}
	for _, status := range unsettled {
		if publishAttemptSettled(store.EffectIntent{Status: status}) {
			t.Fatalf("%s 尚未收束，绝不允许被新尝试绕过", status)
		}
	}
	// 未知/空状态必须落到保守分支，不能因为不在枚举里就被当成可重来。
	if publishAttemptSettled(store.EffectIntent{Status: ""}) ||
		publishAttemptSettled(store.EffectIntent{Status: "somethingNew"}) {
		t.Fatal("未知意图状态必须按未收束处理")
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
