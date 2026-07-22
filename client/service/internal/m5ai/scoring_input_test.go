package m5ai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderScoringInputV1MapsOnlyObservedFacts(t *testing.T) {
	snapshot := `{
		"basic":[
			{"label":"姓名","value":"不应出站"},
			{"label":"年龄","value":"30岁"},
			{"label":"工作经验","value":"8年"},
			{"label":"现居地","value":"上海"},
			{"label":"其他信息1","value":"补充事实"}
		],
		"expectations":[{"label":"求职期望","value":"上海 顾问 20k"}],
		"selfEvaluation":"自评","education":"教育","workExperiences":"工作"
	}`
	first, err := RenderScoringInputV1(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderScoringInputV1(snapshot)
	if err != nil || first != second {
		t.Fatalf("评分输入序列化不确定: first=%q second=%q err=%v", first, second, err)
	}
	if strings.Contains(first, "不应出站") || strings.Contains(first, `"姓名"`) {
		t.Fatalf("评分输入泄漏候选人姓名: %s", first)
	}
	var decoded struct {
		Basic        map[string]string `json:"基本"`
		Expectations map[string]string `json:"期望"`
		FullText     string            `json:"简历全文"`
	}
	if err := json.Unmarshal([]byte(first), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Basic["年龄"] != "30岁" || decoded.Basic["工作年限"] != "8年" ||
		decoded.Basic["现居"] != "上海" || decoded.Basic["其他信息1"] != "补充事实" ||
		decoded.Basic["性别"] != "" || decoded.Expectations["求职期望"] != "上海 顾问 20k" ||
		decoded.Expectations["期望职位"] != "" || decoded.Expectations["最近投递"] != "" || decoded.FullText != "" {
		t.Fatalf("评分输入映射错误: %+v %+v", decoded.Basic, decoded.Expectations)
	}
}

func TestRenderScoringInputV1UsesLegacyRuneLimits(t *testing.T) {
	snapshot := map[string]any{
		"basic": []map[string]string{}, "expectations": []map[string]string{},
		"selfEvaluation":  strings.Repeat("自", scoringSelfEvaluationLimit+1),
		"workExperiences": strings.Repeat("工", scoringWorkLimit+1),
		"education":       strings.Repeat("教", scoringEducationLimit+1),
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderScoringInputV1(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		SelfEvaluation  string `json:"自评"`
		WorkExperiences string `json:"工作经历"`
		Education       string `json:"教育经历"`
	}
	if err := json.Unmarshal([]byte(rendered), &decoded); err != nil {
		t.Fatal(err)
	}
	if len([]rune(decoded.SelfEvaluation)) != scoringSelfEvaluationLimit ||
		len([]rune(decoded.WorkExperiences)) != scoringWorkLimit ||
		len([]rune(decoded.Education)) != scoringEducationLimit {
		t.Fatalf("评分输入截断错误: self=%d work=%d education=%d",
			len([]rune(decoded.SelfEvaluation)), len([]rune(decoded.WorkExperiences)), len([]rune(decoded.Education)))
	}
}

func TestRenderScoringInputV1RejectsMissingOrAmbiguousSections(t *testing.T) {
	for _, raw := range []string{
		`{"basic":[],"expectations":[],"selfEvaluation":"","education":""}`,
		`{"basic":[{"label":" ","value":"x"}],"expectations":[],"selfEvaluation":"","education":"","workExperiences":""}`,
		`{"basic":[],"expectations":[{"label":"","value":"x"}],"selfEvaluation":"","education":"","workExperiences":""}`,
	} {
		if rendered, err := RenderScoringInputV1(raw); err == nil || rendered != "" {
			t.Fatalf("无效评分输入未拒绝: rendered=%q err=%v", rendered, err)
		}
	}
}
