package m5ai

import (
	"encoding/json"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"
)

func TestRenderGreetingPromptReplacesOnlyOriginalTokensOnce(t *testing.T) {
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
	want := "状态=状态含{resume_summary_json};简历=" + input.ResumeSummaryJSON
	if rendered != want {
		t.Fatalf("招呼模板发生递归替换: got=%q want=%q", rendered, want)
	}
}

func TestRenderGreetingPromptRejectsMissingRepeatedOrUnknownTokens(t *testing.T) {
	input := GreetingInputV1{CareerState: "状态", ResumeSummaryJSON: `{}`}
	for _, prompt := range []string{
		"{career_state}",
		"{career_state}{resume_summary_json}{resume_summary_json}",
		"{career_state}{resume_summary_json}{score}",
	} {
		if rendered, err := RenderGreetingPrompt(prompt, input); err == nil || rendered != "" {
			t.Fatalf("非法招呼模板未拒绝: prompt=%q rendered=%q err=%v", prompt, rendered, err)
		}
	}
	if rendered, err := RenderGreetingPrompt(
		"{career_state}{resume_summary_json}", GreetingInputV1{ResumeSummaryJSON: "not-json"},
	); err == nil || rendered != "" {
		t.Fatalf("非法简历 JSON 未拒绝: rendered=%q err=%v", rendered, err)
	}
}

func TestRenderGreetingPromptUsesUTF8ByteBudgetWithoutTruncation(t *testing.T) {
	input := GreetingInputV1{CareerState: "x", ResumeSummaryJSON: `{}`}
	within := strings.Repeat("a", GreetingInputTokenLimit-3) + "{career_state}{resume_summary_json}"
	rendered, err := RenderGreetingPrompt(within, input)
	if err != nil || len([]byte(rendered)) != GreetingInputTokenLimit {
		t.Fatalf("边界内招呼输入被拒绝: bytes=%d err=%v", len([]byte(rendered)), err)
	}
	over := "a" + within
	if rendered, err := RenderGreetingPrompt(over, input); err == nil || rendered != "" {
		t.Fatalf("越界招呼输入未拒绝: bytes=%d err=%v", len([]byte(rendered)), err)
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
