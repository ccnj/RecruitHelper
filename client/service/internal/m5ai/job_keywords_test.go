package m5ai

import (
	"strings"
	"testing"
)

func sampleKeywordSections() []JobKeywordSectionInput {
	return []JobKeywordSectionInput{
		{Title: "行业经验 (0/3)", Limit: 3, Words: []string{"保险", "银行", "财务/审计/税务"}},
		{Title: "证书 (0/2)", Limit: 2, Words: []string{"CPA", "ACCA"}},
		{Title: "您还有哪些招聘要求？ (0/3)", Limit: 3},
	}
}

func TestRenderJobKeywordsPromptCarriesSectionsAndQuotas(t *testing.T) {
	content, err := RenderJobKeywordsPrompt(
		"家庭财务规划师(HR/培训/市场背景优先)",
		"岗位职责：为家庭客户提供财务规划与资产配置建议。",
		"理财顾问",
		sampleKeywordSections(),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"家庭财务规划师(HR/培训/市场背景优先)",
		"职位类别（已选定）：理财顾问",
		// 组内上限必须进提示词:模型看不见它就会把词堆在一组里被平台拒掉。
		"- 行业经验 (0/3)（本组最多选 3 个）：保险、银行、财务/审计/税务",
		"- 证书 (0/2)（本组最多选 2 个）：CPA、ACCA",
		// 兜底组没有预设词条,得明说,免得模型以为是数据漏了而去自造词条名。
		"（本组没有现成词条，只能自己写）",
		"一共只能选 3 到 5 个关键词",
		"绝不能拆成「税务」单独返回",
		// 判据面向候选人而不是面向岗位：关键词在平台上匹配的是求职者的简历，
		// 选成岗位动作的摘要就匹配不到人。
		"匹配的是**求职者的简历**",
		"什么样的人适合这个岗位",
		"最可能出现在这些人简历上的词",
		// 避开保险与类别那条同源：招的是转行的人，打上保险标签等于去保险业内
		// 捞人。它只是偏好不是闸，所以只能在提示词里钉住。
		"不要选带「保险」字样的词",
		"也同样不能带「保险」字样",
		"即便这个职位的词库大半跟保险有关",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("提示词缺少 %q", want)
		}
	}

	// 词库为空时不能拼出一个"从空词库里选"的请求——那注定得到幻觉。
	if _, err := RenderJobKeywordsPrompt("岗位", "描述", "理财顾问", nil); err == nil {
		t.Fatal("词库为空必须拒绝构造请求")
	}
	if _, err := RenderJobKeywordsPrompt("岗位", "描述", "", sampleKeywordSections()); err == nil {
		t.Fatal("缺职位类别必须拒绝构造请求")
	}
	if _, err := RenderJobKeywordsPrompt("", "描述", "理财顾问", sampleKeywordSections()); err == nil {
		t.Fatal("缺职位名必须拒绝构造请求")
	}
}

func TestParseJobKeywordsSuggestionRejectsUnusableAnswers(t *testing.T) {
	good, err := ParseJobKeywordsSuggestion(
		`{"关键词":["银行","CPA","财务/审计/税务"],"理由":"与描述里的财税背景相符"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(good.Keywords) != 3 || good.Keywords[0] != "银行" ||
		good.Reason != "与描述里的财税背景相符" {
		t.Fatalf("正常返回解析错误: %+v", good)
	}

	// 错误串是稳定的错误分类标识,会进无正文诊断,所以逐条钉住。
	for _, testCase := range []struct{ name, raw, want string }{
		{"少于三个", `{"关键词":["银行","CPA"]}`, "countOutOfRange"},
		{"多于五个", `{"关键词":["a","b","c","d","e","f"]}`, "countOutOfRange"},
		{"空数组", `{"关键词":[]}`, "countOutOfRange"},
		{"重复", `{"关键词":["银行","银行","CPA"]}`, "duplicateKeyword"},
		{"空词", `{"关键词":["银行","  ","CPA"]}`, "emptyKeyword"},
		{"不是数组", `{"关键词":"银行"}`, "invalidKeywords"},
		{"缺字段", `{"理由":"忘了给词"}`, "missingKeywords"},
		{"别名撞车", `{"关键词":["a","b","c"],"keywords":["d","e","f"]}`, "duplicateKeywords"},
	} {
		if _, err := ParseJobKeywordsSuggestion(testCase.raw); err == nil {
			t.Fatalf("%s 必须被拒绝", testCase.name)
		} else if err.Error() != testCase.want {
			t.Fatalf("%s 的错误分类应为 %q，实得 %q", testCase.name, testCase.want, err)
		}
	}
}

func TestPlanJobKeywordsSplitsMatchedAndCustom(t *testing.T) {
	sections := sampleKeywordSections()

	plan, err := PlanJobKeywords(
		JobKeywordsSuggestion{Keywords: []string{"银行", "CPA", "家族信托"}}, sections)
	if err != nil {
		t.Fatal(err)
	}
	// 词库里有的走点选,没有的走兜底组自定义。顺序沿用模型给的顺序。
	if strings.Join(plan.Matched, ",") != "银行,CPA" {
		t.Fatalf("命中词库的应为 银行,CPA，实得 %v", plan.Matched)
	}
	if strings.Join(plan.Custom, ",") != "家族信托" {
		t.Fatalf("自定义的应为 家族信托，实得 %v", plan.Custom)
	}

	// 逐字相等,绝不模糊匹配:差一个字手就点不中,而"差不多的那个"点下去
	// 就是给职位打上另一个标签。
	near, err := PlanJobKeywords(
		JobKeywordsSuggestion{Keywords: []string{"税务", "银行", "CPA"}}, sections)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(near.Custom, ",") != "税务" {
		t.Fatalf("「税务」不与「财务/审计/税务」全等，必须落自定义，实得 %v", near.Custom)
	}

	if _, err := PlanJobKeywords(
		JobKeywordsSuggestion{Keywords: []string{"甲", "乙", "丙"}}, sections,
	); err == nil || err.Error() != "tooManyCustom" {
		t.Fatalf("自定义超过 2 个必须被拒，实得 %v", err)
	}

	if _, err := PlanJobKeywords(
		JobKeywordsSuggestion{Keywords: []string{"保险", "银行", "财务/审计/税务", "CPA"}},
		[]JobKeywordSectionInput{
			{Title: "行业经验", Limit: 2, Words: []string{"保险", "银行", "财务/审计/税务"}},
			{Title: "证书", Limit: 2, Words: []string{"CPA"}},
		},
	); err == nil || err.Error() != "sectionOverflow" {
		t.Fatalf("单组超配额必须被拒，实得 %v", err)
	}

	// Limit 为 0 是"这个组件变体没给出上限"，不是"上限为 0"。没有上限就没什么
	// 可复核的，交给平台自己拒——否则不带配额那个变体会整批发不出去。
	if _, err := PlanJobKeywords(
		JobKeywordsSuggestion{Keywords: []string{"甲", "乙", "丙"}},
		[]JobKeywordSectionInput{{Title: "财务管理方向", Words: []string{"甲", "乙", "丙"}}},
	); err != nil {
		t.Fatalf("无上限的分组不应被复核出溢出: %v", err)
	}

	// 同一个词出现在多组里只算一次:两组都记会把配额算重,而选中它实际只占
	// 一个位置。
	if _, err := PlanJobKeywords(
		JobKeywordsSuggestion{Keywords: []string{"保险", "银行", "信托"}},
		[]JobKeywordSectionInput{
			{Title: "行业经验", Limit: 2, Words: []string{"保险", "银行", "信托"}},
			{Title: "工作方向", Limit: 2, Words: []string{"保险"}},
		},
	); err == nil || err.Error() != "sectionOverflow" {
		t.Fatalf("三个词全在第一组、上限 2，必须判溢出，实得 %v", err)
	}
}

// 全部命中词库时 Custom 一次都不会被 append。它必须是空切片而不是 nil：
// nil 序列化成 null，诊断台按数组读 .length 就抛异常、整棵树卸掉（2026-08-02
// 客户机白屏两次的根因）。
func TestPlanJobKeywordsNeverLeavesNilSlices(t *testing.T) {
	sections := []JobKeywordSectionInput{
		{Title: "行业经验", Limit: 3, Words: []string{"银行", "基金", "证券"}},
	}

	allMatched, err := PlanJobKeywords(
		JobKeywordsSuggestion{Keywords: []string{"银行", "基金", "证券"}}, sections)
	if err != nil {
		t.Fatal(err)
	}
	if allMatched.Custom == nil {
		t.Fatal("全部命中词库时 Custom 是 nil，会序列化成 null")
	}

	// 两个词都不在词库里（自定义上限是 2，再多会先被 tooManyCustom 拦掉，
	// 就验不到 Matched 了）。
	allCustom, err := PlanJobKeywords(
		JobKeywordsSuggestion{Keywords: []string{"家族信托", "跨境税务"}},
		[]JobKeywordSectionInput{{Title: "兜底", Limit: 3}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if allCustom.Matched == nil {
		t.Fatal("一个都没命中时 Matched 是 nil，会序列化成 null")
	}
}
