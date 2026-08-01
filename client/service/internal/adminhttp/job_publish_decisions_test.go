package adminhttp

import (
	"strings"
	"testing"
)

// 类别与关键词是前两趟由大模型定下、并在二次确认清单上给运营看过的两项决定。
// 发布入口不替它们兜默认值：猜错会把职位推给错误的人群，而页面看上去一切正常。
func TestCheckPublishDecisionsRequiresBothAIDecisions(t *testing.T) {
	good := []string{"法律", "税务", "会计"}
	if message := checkPublishDecisions("理财顾问", good); message != "" {
		t.Fatalf("齐备的决定不该被拒: %s", message)
	}
	if message := checkPublishDecisions("理财顾问", []string{"a", "b", "c", "d", "e"}); message != "" {
		t.Fatalf("五个关键词是上限之内: %s", message)
	}

	for name, testCase := range map[string]struct {
		jobClass string
		keywords []string
		want     string
	}{
		"缺类别":   {"", good, "缺少职位类别"},
		"类别全空白": {"   ", good, "缺少职位类别"},
		"关键词为空": {"理财顾问", nil, "必须是 3-5 个"},
		"少于三个":  {"理财顾问", []string{"法律", "税务"}, "必须是 3-5 个"},
		"多于五个":  {"理财顾问", []string{"a", "b", "c", "d", "e", "f"}, "必须是 3-5 个"},
		"有空词":   {"理财顾问", []string{"法律", "  ", "会计"}, "有空词"},
		// 重复会让手对同一个词条点两次：第二次是取消选中，最后少一个词。
		"重复": {"理财顾问", []string{"法律", "法律", "会计"}, "重复"},
	} {
		message := checkPublishDecisions(testCase.jobClass, testCase.keywords)
		if !strings.Contains(message, testCase.want) {
			t.Fatalf("%s 的拒绝理由应含 %q，实得 %q", name, testCase.want, message)
		}
	}
}
