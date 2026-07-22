package m5ai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderGreetingInputV1UsesOnlyCompleteCanonicalResume(t *testing.T) {
	longSelf := strings.Repeat("自", scoringSelfEvaluationLimit+1)
	snapshot := `{
		"displayName":"不得作为简历姓名出站",
		"basic":[
			{"label":"姓名","value":"简历观察姓名"},
			{"label":"工作经验","value":"8年"},
			{"label":"现居地","value":"上海"},
			{"label":"求职状态","value":"离职-正在找工作"},
			{"label":"其他事实","value":"保留"}
		],
		"expectations":[{"label":"求职期望","value":"上海 顾问"}],
		"selfEvaluation":` + mustJSONText(t, longSelf) + `,
		"education":"完整教育章节",
		"workExperiences":"完整工作章节"
	}`
	first, err := RenderGreetingInputV1(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderGreetingInputV1(snapshot)
	if err != nil || first != second {
		t.Fatalf("招呼输入序列化不确定: first=%+v second=%+v err=%v", first, second, err)
	}
	if first.CareerState != "离职-正在找工作" || strings.Contains(first.ResumeSummaryJSON, "不得作为简历姓名出站") {
		t.Fatalf("招呼输入身份或求职状态错误: %+v", first)
	}
	var decoded struct {
		Basic           map[string]string `json:"基本"`
		Expectations    map[string]string `json:"期望"`
		SelfEvaluation  string            `json:"自评"`
		WorkExperiences string            `json:"工作经历"`
		Education       string            `json:"教育经历"`
		FullText        string            `json:"简历全文"`
	}
	if err := json.Unmarshal([]byte(first.ResumeSummaryJSON), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Basic["姓名"] != "简历观察姓名" || decoded.Basic["工作年限"] != "8年" ||
		decoded.Basic["现居"] != "上海" || decoded.Basic["其他事实"] != "保留" ||
		decoded.Expectations["求职期望"] != "上海 顾问" || decoded.FullText != "" ||
		decoded.SelfEvaluation != longSelf || decoded.WorkExperiences != "完整工作章节" ||
		decoded.Education != "完整教育章节" {
		t.Fatalf("招呼输入投影错误: %+v", decoded)
	}
}

func TestRenderGreetingInputV1UsesEmptyMissingCareerState(t *testing.T) {
	input, err := RenderGreetingInputV1(`{
		"basic":[],"expectations":[],"selfEvaluation":"","education":"","workExperiences":""
	}`)
	if err != nil || input.CareerState != "" {
		t.Fatalf("缺失求职状态未映射为空串: input=%+v err=%v", input, err)
	}
}

func TestRenderGreetingInputV1RejectsIncompleteOrInvalidSnapshot(t *testing.T) {
	for _, raw := range []string{
		`{"basic":[],"expectations":[],"selfEvaluation":"","education":""}`,
		`{"basic":[{"label":" ","value":"x"}],"expectations":[],"selfEvaluation":"","education":"","workExperiences":""}`,
		"{\"basic\":[],\"expectations\":[],\"selfEvaluation\":\"\",\"education\":\"\",\"workExperiences\":\"\xff\"}",
	} {
		if input, err := RenderGreetingInputV1(raw); err == nil || input != (GreetingInputV1{}) {
			t.Fatalf("无效招呼输入未拒绝: input=%+v err=%v", input, err)
		}
	}
}

func mustJSONText(t *testing.T, value string) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
