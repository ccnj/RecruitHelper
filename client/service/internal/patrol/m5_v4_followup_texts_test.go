package patrol

import (
	"strings"
	"testing"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
)

// 钉住甲方核准的三档跟催文案：恰好 1/2/3 三档、可渲染称呼、通过发送文本校验、
// 不含"改期/换时间"等尚未实现能力的承诺（2026-07-27 裁决）。
func TestInterviewFollowupTextsApprovedShape(t *testing.T) {
	if len(communicationV4InterviewFollowupTexts) != 3 {
		t.Fatalf("必须恰好三档: %d", len(communicationV4InterviewFollowupTexts))
	}
	for stage := uint8(1); stage <= 3; stage++ {
		template, exists := communicationV4InterviewFollowupTexts[stage]
		if !exists {
			t.Fatalf("缺少第 %d 档文案", stage)
		}
		rendered, err := communication.RenderV4FixedPhrase(
			template,
			communication.V4FixedPhraseRenderInput{Salutation: "洪总"},
		)
		if err != nil {
			t.Fatalf("第 %d 档渲染失败: %v", stage, err)
		}
		if !strings.Contains(rendered, "洪总") || strings.Contains(rendered, "{称呼}") {
			t.Fatalf("第 %d 档称呼渲染异常: %s", stage, rendered)
		}
		if err := m5ai.ValidateSendText(rendered); err != nil {
			t.Fatalf("第 %d 档发送文本校验失败: %v", stage, err)
		}
		for _, forbidden := range []string{"改期", "换时间", "调整时间", "重新安排", "随时可以调"} {
			if strings.Contains(rendered, forbidden) {
				t.Fatalf("第 %d 档不得承诺未实现能力 %q: %s", stage, forbidden, rendered)
			}
		}
	}
}
