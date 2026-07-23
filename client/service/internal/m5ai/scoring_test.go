package m5ai

import (
	"strings"
	"testing"
)

func TestRenderScoringPromptReplacesExactlyOneResumeWithoutTruncation(t *testing.T) {
	resume := `{"basic":[]}`
	rendered, err := RenderScoringPrompt("before:"+scoringResumePlaceholder+":after", resume)
	if err != nil || rendered != "before:"+resume+":after" {
		t.Fatalf("评分输入渲染错误: rendered=%q err=%v", rendered, err)
	}

	for _, prompt := range []string{
		"missing",
		scoringResumePlaceholder + scoringResumePlaceholder,
	} {
		if rendered, err := RenderScoringPrompt(prompt, resume); err == nil || rendered != "" {
			t.Fatalf("占位符数量非法时必须拒绝: rendered=%q err=%v", rendered, err)
		}
	}
}

func TestRenderScoringPromptPreservesInputLargerThanTokenLimitInBytes(t *testing.T) {
	input := strings.Repeat("界", ReplyInputTokenLimit)
	rendered, err := RenderScoringPrompt(scoringResumePlaceholder, input)
	if err != nil || rendered != input || len([]byte(rendered)) <= ReplyInputTokenLimit {
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
