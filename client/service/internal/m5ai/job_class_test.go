package m5ai

import (
	"strings"
	"testing"
)

func TestRenderJobClassPromptCarriesCandidatesWithDefinitions(t *testing.T) {
	content, err := RenderJobClassPrompt(
		"家庭财务规划师(HR/培训/市场背景优先)",
		"岗位职责：为家庭客户提供财务规划与资产配置建议。",
		[]JobClassCandidateInput{
			{Name: "理财顾问", Definition: "负责评估客户财务状况、配置金融产品的专业人员"},
			{Name: "保险顾问", Definition: "从事保险业务代理与保险产品销售的专业人员"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	// 平台官方释义是模型判断贴合度的唯一依据,漏掉它等于让模型凭类别名字面猜。
	for _, want := range []string{
		"家庭财务规划师(HR/培训/市场背景优先)",
		"1. 理财顾问 —— 负责评估客户财务状况、配置金融产品的专业人员",
		"2. 保险顾问 —— 从事保险业务代理与保险产品销售的专业人员",
		"不要选保险相关的类别",
		"必须原样返回其中一个类别名",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("提示词缺少 %q", want)
		}
	}

	// 候选为空时不能拼出一个"从空清单里选"的请求——那注定得到幻觉。
	if _, err := RenderJobClassPrompt("岗位", "描述", nil); err == nil {
		t.Fatal("候选为空必须拒绝构造请求")
	}
	if _, err := RenderJobClassPrompt("", "描述", []JobClassCandidateInput{{Name: "理财顾问"}}); err == nil {
		t.Fatal("缺职位名必须拒绝构造请求")
	}
}

func TestParseJobClassSuggestionRejectsUnusableAnswers(t *testing.T) {
	good, err := ParseJobClassSuggestion(`{"类别":"理财顾问","置信度":0.82,"理由":"与家庭财务规划最贴合"}`)
	if err != nil || good.Class != "理财顾问" || good.Confidence != 0.82 ||
		good.Reason != "与家庭财务规划最贴合" {
		t.Fatalf("合法返回解析错误: %+v err=%v", good, err)
	}
	// 英文键也接受:模型偶尔会照 JSON 惯例回英文。
	if alias, err := ParseJobClassSuggestion(`{"class":"保险顾问","confidence":0.2}`); err != nil ||
		alias.Class != "保险顾问" || alias.Confidence != 0.2 {
		t.Fatalf("英文键返回解析错误: %+v err=%v", alias, err)
	}

	for name, raw := range map[string]string{
		"不是 JSON":     "理财顾问",
		"缺类别":         `{"置信度":0.8}`,
		"类别为空":        `{"类别":"  ","置信度":0.8}`,
		"缺置信度":        `{"类别":"理财顾问"}`,
		"置信度越界":       `{"类别":"理财顾问","置信度":1.4}`,
		"置信度不是数":      `{"类别":"理财顾问","置信度":"高"}`,
		"中英文键同时出现":    `{"类别":"理财顾问","class":"保险顾问","置信度":0.8}`,
		"置信度中英文键同时出现": `{"类别":"理财顾问","置信度":0.8,"confidence":0.2}`,
	} {
		if _, err := ParseJobClassSuggestion(raw); err == nil {
			t.Fatalf("%s 必须拒绝: %s", name, raw)
		}
	}

	// 理由缺席不算失败:它只给人读,不参与任何判定。
	if noReason, err := ParseJobClassSuggestion(`{"类别":"理财顾问","置信度":0.5}`); err != nil ||
		noReason.Reason != "" {
		t.Fatalf("缺理由应当接受: %+v err=%v", noReason, err)
	}
}
