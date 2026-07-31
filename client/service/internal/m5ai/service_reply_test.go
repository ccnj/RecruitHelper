package m5ai

import (
	"strings"
	"testing"
)

func TestRenderServiceReplyPromptBindsOnlyApprovedInputs(t *testing.T) {
	prompt, err := RenderServiceReplyPrompt(
		[]string{"好的，那咱们面试时间就定在8月3日上午10点。", " "},
		true,
		[]string{"如果您方便，今天下午四点可以吗", ""},
	)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"我(消息):好的，那咱们面试时间就定在8月3日上午10点。",
		"我(卡片):已发起微信交换邀请",
		"候选人(消息):如果您方便，今天下午四点可以吗",
		`{"回复"`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q\n%s", want, prompt)
		}
	}
	for _, banned := range []string{"简历", "可约面时间", "发起线上会议", "动作"} {
		if strings.Contains(prompt, banned) {
			t.Fatalf("prompt must not contain %q\n%s", banned, prompt)
		}
	}
}

func TestRenderServiceReplyPromptWithoutFixedSegment(t *testing.T) {
	prompt, err := RenderServiceReplyPrompt(nil, false, []string{"嗯嗯"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(prompt, "系统刚刚已代表你发出") {
		t.Fatalf("empty fixed segment must not render the sent header\n%s", prompt)
	}
}

func TestRenderServiceReplyPromptRequiresCandidateText(t *testing.T) {
	if _, err := RenderServiceReplyPrompt([]string{"回执"}, true, []string{" ", ""}); err == nil {
		t.Fatal("expected error for empty candidate texts")
	}
}

func TestParseServiceReplySuggestionGuidance(t *testing.T) {
	suggestion, err := ParseServiceReplySuggestion(`{"回复": "关于您提到的时间安排，咱们微信上细聊吧～"}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if suggestion.Reply != "关于您提到的时间安排，咱们微信上细聊吧～" {
		t.Fatalf("unexpected reply: %q", suggestion.Reply)
	}
}

func TestParseServiceReplySuggestionExplicitSilence(t *testing.T) {
	for _, raw := range []string{`{"回复": ""}`, `{"回复": "  "}`} {
		suggestion, err := ParseServiceReplySuggestion(raw)
		if err != nil {
			t.Fatalf("silence must be a valid terminal, got %v for %s", err, raw)
		}
		if suggestion.Reply != "" {
			t.Fatalf("expected empty reply for %s, got %q", raw, suggestion.Reply)
		}
	}
}

func TestParseServiceReplySuggestionRejectsForeignShape(t *testing.T) {
	for _, raw := range []string{
		`{"回复": "好的", "动作": "发起线上会议"}`,
		`{"话术_序列": ["好的"]}`,
		`{"回复": 3}`,
		`{}`,
		`not json`,
	} {
		if _, err := ParseServiceReplySuggestion(raw); err == nil {
			t.Fatalf("expected rejection for %s", raw)
		}
	}
}

func TestParseServiceReplySuggestionValidatesSendText(t *testing.T) {
	if _, err := ParseServiceReplySuggestion(`{"回复": "` + strings.Repeat("长", SendTextMaxUTF8Bytes) + `"}`); err == nil {
		t.Fatal("expected send-text limit rejection")
	}
}
