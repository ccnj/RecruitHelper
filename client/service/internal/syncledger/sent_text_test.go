package syncledger

import "testing"

func TestResolveSentTextFallsBackToPlannedAndFlagsDifference(t *testing.T) {
	planned := "  你好，候选人  "
	same := ResolveSentText(planned, "")
	if same.Text != planned || same.Hash != HashText(planned) || same.Differs || same.PlannedHash != same.Hash {
		t.Fatalf("无实发正文应按计划正文: %+v", same)
	}
	equivalent := ResolveSentText(planned, "你好，候选人")
	if equivalent.Differs || equivalent.Hash != HashText(planned) {
		t.Fatalf("规范等价的实发正文不算不同: %+v", equivalent)
	}
	typo := ResolveSentText(planned, "你好，侯选人")
	if !typo.Differs || typo.Text != "你好，侯选人" || typo.Hash != HashText("你好，侯选人") || typo.PlannedHash != HashText(planned) {
		t.Fatalf("实发正文不同应如实标记并以实发为准: %+v", typo)
	}
}

func TestSentTextUsableRejectsWhitespaceOnly(t *testing.T) {
	if !SentTextUsable("") || !SentTextUsable("一句话") || SentTextUsable("  \u3000\t ") {
		t.Fatal("空即不带、正文可用、纯空白不可用")
	}
}
