package dispatch

// 平台拒绝原话落档案(2026-08-07 甲方裁决:错误原因要传到客户端)。
// end_reason=greetingFailed 太笼统——敏感词、平台上限、技术失败在客户端上
// 长得一模一样,用户没法据此改招呼语。手侧 GREETING_REJECTED 的 message
// 带平台原话,这里验证它一路落到档案字段。

import (
	"strings"
	"testing"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

func TestGreetingRejectedCarriesPlatformReasonToProfile(t *testing.T) {
	d, st, m := newDisp(t)
	fixture := seedGreetingTarget(t, st, m, "reject-reason")
	receipt, err := d.SendGreeting(sendGreetingRequest(fixture, "intent-reject-reason", ""))
	if err != nil {
		t.Fatal(err)
	}
	rejected := failedGreetingResult(receipt.MsgID, protocol.ErrCodeGreetingRejected, protocol.SideEffectNone)
	rejected.Error.Message = "平台明确拒绝本次招呼:内容中涉及敏感词，请修改"
	if _, _, err := d.applyResultMessage(fixture.HandID, "result-reject-reason", rejected); err != nil {
		t.Fatal(err)
	}
	profile, err := st.CandidateProfileByID(fixture.ProfileID)
	if err != nil || profile == nil {
		t.Fatalf("读档案失败: %v", err)
	}
	if profile.MainStatus != store.CandidateProfileEnded ||
		profile.EndReason == nil || *profile.EndReason != store.CandidateProfileEndGreetingFailed {
		t.Fatalf("平台拒绝仍应以 greetingFailed 终结档案: %+v", profile)
	}
	if !strings.Contains(profile.GreetingRejectReason, "内容中涉及敏感词") {
		t.Fatalf("平台原话必须落档案供客户端显示: %q", profile.GreetingRejectReason)
	}
}

func TestGreetingRejectedWithoutMessageStillEndsProfile(t *testing.T) {
	d, st, m := newDisp(t)
	fixture := seedGreetingTarget(t, st, m, "reject-no-reason")
	receipt, err := d.SendGreeting(sendGreetingRequest(fixture, "intent-reject-no-reason", ""))
	if err != nil {
		t.Fatal(err)
	}
	rejected := failedGreetingResult(receipt.MsgID, protocol.ErrCodeGreetingRejected, protocol.SideEffectNone)
	rejected.Error.Message = "   "
	if _, _, err := d.applyResultMessage(fixture.HandID, "result-reject-no-reason", rejected); err != nil {
		t.Fatal(err)
	}
	profile, _ := st.CandidateProfileByID(fixture.ProfileID)
	if profile == nil || profile.MainStatus != store.CandidateProfileEnded {
		t.Fatalf("拿不到原话也必须照常终结档案: %+v", profile)
	}
	if profile.GreetingRejectReason != "" {
		t.Fatalf("空原话不得落成空白字符串以外的内容: %q", profile.GreetingRejectReason)
	}
}

func TestGreetingRejectReasonTruncatedForProjection(t *testing.T) {
	d, st, m := newDisp(t)
	fixture := seedGreetingTarget(t, st, m, "reject-long")
	receipt, err := d.SendGreeting(sendGreetingRequest(fixture, "intent-reject-long", ""))
	if err != nil {
		t.Fatal(err)
	}
	rejected := failedGreetingResult(receipt.MsgID, protocol.ErrCodeGreetingRejected, protocol.SideEffectNone)
	rejected.Error.Message = strings.Repeat("敏", 400)
	if _, _, err := d.applyResultMessage(fixture.HandID, "result-reject-long", rejected); err != nil {
		t.Fatal(err)
	}
	profile, _ := st.CandidateProfileByID(fixture.ProfileID)
	if profile == nil {
		t.Fatal("读档案失败")
	}
	if runes := []rune(profile.GreetingRejectReason); len(runes) != 200 {
		t.Fatalf("异常长文案必须按字符截到 200: %d", len(runes))
	}
}
