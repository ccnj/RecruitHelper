package m5ai

import (
	"encoding/json"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"
)

func TestRenderGreetingPromptPointsToTrailingBlocksWithoutReinterpreting(t *testing.T) {
	input := GreetingInputV1{
		CareerState:       "状态含{resume_summary_json}",
		ResumeSummaryJSON: `{"事实":"值含{career_state}"}`,
	}
	rendered, err := RenderGreetingPrompt(
		"状态={career_state};简历={resume_summary_json}", input,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "状态=求职状态(见下方输入参数-求职状态);简历=简历摘要(见下方输入参数-简历摘要)\n\n" +
		"【输入参数】\n\n【输入参数-求职状态】\n" + input.CareerState +
		"\n\n【输入参数-简历摘要】\n" + input.ResumeSummaryJSON +
		"\n\n" + realityBoundaryCompactPolicy
	if rendered != want {
		t.Fatalf("招呼模板渲染漂移或值被二次解释: got=%q want=%q", rendered, want)
	}
}

// 2026-09-03 统一渲染:占位符缺失、重复、陌生都不再拒绝——必填输入恒追加,
// 陌生占位符原样保留。仍拒绝的只有数据侧非法(简历摘要不是 JSON)。
func TestRenderGreetingPromptToleratesTemplateDefectsButRejectsBadInput(t *testing.T) {
	input := GreetingInputV1{CareerState: "状态", ResumeSummaryJSON: `{}`}
	for _, prompt := range []string{
		"{career_state}",
		"{career_state}{resume_summary_json}{resume_summary_json}",
		"{career_state}{resume_summary_json}{score}",
	} {
		rendered, err := RenderGreetingPrompt(prompt, input)
		if err != nil || strings.Count(rendered, "【输入参数-求职状态】\n状态\n") != 1 ||
			strings.Count(rendered, "【输入参数-简历摘要】\n{}\n") != 1 {
			t.Fatalf("模板缺陷不得拒绝且数据各只一份: prompt=%q rendered=%q err=%v", prompt, rendered, err)
		}
	}
	rendered, err := RenderGreetingPrompt("{career_state}{resume_summary_json}{score}", input)
	if err != nil || !strings.HasPrefix(rendered, "求职状态(见下方输入参数-求职状态)简历摘要(见下方输入参数-简历摘要){score}\n\n") {
		t.Fatalf("陌生占位符必须原样保留: rendered=%q err=%v", rendered, err)
	}
	empty, err := RenderGreetingPrompt("{career_state}{resume_summary_json}", GreetingInputV1{ResumeSummaryJSON: `{}`})
	if err != nil || !strings.Contains(empty, "【输入参数-求职状态】\n\n【输入参数-简历摘要】\n{}") {
		t.Fatalf("求职状态为空串时子块只剩标题行: rendered=%q err=%v", empty, err)
	}
	if rendered, err := RenderGreetingPrompt(
		"{career_state}{resume_summary_json}", GreetingInputV1{ResumeSummaryJSON: "not-json"},
	); err == nil || rendered != "" {
		t.Fatalf("非法简历 JSON 未拒绝: rendered=%q err=%v", rendered, err)
	}
}

func TestRenderGreetingPromptPreservesInputLargerThanTokenLimitInBytes(t *testing.T) {
	careerState := strings.Repeat("界", GreetingInputTokenLimit)
	input := GreetingInputV1{CareerState: careerState, ResumeSummaryJSON: `{}`}
	rendered, err := RenderGreetingPrompt("{career_state}{resume_summary_json}", input)
	if err != nil || !strings.Contains(rendered, "【输入参数-求职状态】\n"+careerState+"\n\n") ||
		len([]byte(rendered)) <= GreetingInputTokenLimit {
		t.Fatalf("招呼渲染不应以 UTF-8 字节冒充 token: bytes=%d err=%v", len([]byte(rendered)), err)
	}
}

func TestParseGreetingSuggestionConsumesOnlyChineseBodyAndNormalizes(t *testing.T) {
	decomposed := "  cafe\u0301  "
	raw, err := json.Marshal(map[string]any{
		"招呼语":      decomposed,
		"需求点":      "丢弃",
		"需求点置信度":   0.8,
		"用到的简历事实":  []string{"不持久化"},
		"字数":       4,
		"greeting": "不得作为 fallback",
	})
	if err != nil {
		t.Fatal(err)
	}
	suggestion, err := ParseGreetingSuggestion(string(raw))
	if err != nil || suggestion.Text != norm.NFC.String(strings.TrimSpace(decomposed)) {
		t.Fatalf("招呼正文解析/规范化错误: suggestion=%+v err=%v", suggestion, err)
	}
}

func TestParseGreetingSuggestionRejectsFallbackDuplicateInvalidAndOversize(t *testing.T) {
	overlong, _ := json.Marshal(map[string]string{"招呼语": strings.Repeat("a", SendTextMaxUTF8Bytes+1)})
	for _, raw := range []string{
		`{"greeting":"你好"}`,
		`{"招呼语":"甲","招呼语":"乙"}`,
		`{"招呼语":1}`,
		`{"招呼语":"  "}`,
		string(overlong),
		"{\"招呼语\":\"\xff\"}",
	} {
		if suggestion, err := ParseGreetingSuggestion(raw); err == nil || suggestion != (GreetingSuggestion{}) {
			t.Fatalf("非法招呼输出未拒绝: suggestion=%+v err=%v", suggestion, err)
		}
	}
}
