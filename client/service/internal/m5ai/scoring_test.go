package m5ai

import (
	"strings"
	"testing"
)

func TestRenderScoringPromptPointsToTrailingResumeBlock(t *testing.T) {
	resume := `{"basic":[]}`
	rendered, err := RenderScoringPrompt("before:{resume_json}:after", resume)
	want := "before:简历(见下方输入参数-简历):after\n\n【输入参数】\n\n【输入参数-简历】\n" + resume
	if err != nil || rendered != want {
		t.Fatalf("评分输入渲染错误: rendered=%q want=%q err=%v", rendered, want, err)
	}
	// 占位符缺失或重复都不再拒绝(2026-09-03 统一渲染):必填输入恒追加,正文每处
	// 引用都只是指针,简历正文永远只送一份。
	missing, err := RenderScoringPrompt("missing", resume)
	if err != nil || missing != "missing\n\n【输入参数】\n\n【输入参数-简历】\n"+resume {
		t.Fatalf("缺占位符时必须仍追加简历子块: rendered=%q err=%v", missing, err)
	}
	repeated, err := RenderScoringPrompt("{resume_json}{resume_json}", resume)
	if err != nil || strings.Count(repeated, resume) != 1 ||
		strings.Count(repeated, "简历(见下方输入参数-简历)") != 2 {
		t.Fatalf("重复占位符必须全换指针、数据只一份: rendered=%q err=%v", repeated, err)
	}
	unknown, err := RenderScoringPrompt("{resume_json} {未知字段}", resume)
	if err != nil || !strings.HasPrefix(unknown, "简历(见下方输入参数-简历) {未知字段}\n\n") {
		t.Fatalf("陌生占位符必须原样保留、不拒绝: rendered=%q err=%v", unknown, err)
	}
}

func TestRenderScoringPromptPreservesInputLargerThanTokenLimitInBytes(t *testing.T) {
	input := strings.Repeat("界", ReplyInputTokenLimit)
	rendered, err := RenderScoringPrompt("{resume_json}", input)
	if err != nil || !strings.HasSuffix(rendered, "【输入参数-简历】\n"+input) || len([]byte(rendered)) <= ReplyInputTokenLimit {
		t.Fatalf("评分渲染不应以 UTF-8 字节冒充 token: bytes=%d err=%v", len([]byte(rendered)), err)
	}
}

func TestParseScoringSuggestionAcceptsOneLegacyAliasAndDiscardsOtherFields(t *testing.T) {
	tests := []struct {
		raw  string
		want int
	}{
		{raw: `{"score":1,"reason":"discard"}`, want: 1},
		{raw: `{"分数":10,"标签":{"nested":true}}`, want: 10},
		{raw: `{"评分":7,"match_points":[]}`, want: 7},
	}
	for _, testCase := range tests {
		suggestion, err := ParseScoringSuggestion(testCase.raw)
		if err != nil || suggestion.Score != testCase.want {
			t.Fatalf("合法评分未解析: raw=%s suggestion=%+v err=%v", testCase.raw, suggestion, err)
		}
	}
}

func TestParseScoringSuggestionRejectsAmbiguousOrNonIntegerScore(t *testing.T) {
	invalid := []string{
		`{}`,
		`{"score":5,"分数":5}`,
		`{"score":5,"score":5}`,
		`{"score":0}`,
		`{"score":11}`,
		`{"score":7.0}`,
		`{"score":7e0}`,
		`{"score":"7"}`,
		`{"score":true}`,
		`{"score":null}`,
		`{"score":[]}`,
		`{"score":5,"extra":1,"extra":2}`,
		`[]`,
		`{"score":5}{"score":6}`,
		`not-json`,
	}
	for _, raw := range invalid {
		if suggestion, err := ParseScoringSuggestion(raw); err == nil {
			t.Fatalf("非法评分未拒绝: raw=%s suggestion=%+v", raw, suggestion)
		}
	}
}
