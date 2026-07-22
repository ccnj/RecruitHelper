package m5ai

import (
	"strings"
	"testing"
)

func TestV4ShortCircuitUsesOnlyApprovedRules(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		label  IntentLabel
		source string
		ruleID string
	}{
		{name: "online resume", text: "已发送了在线简历", label: IntentInterested, source: "resumeMarker", ruleID: "M5I-RSM-01"},
		{name: "attachment resume", text: "附件简历 请查看", label: IntentInterested, source: "resumeMarker", ruleID: "M5I-RSM-02"},
		{name: "template consider", text: "很抱歉，我暂时不考虑这个机会", label: IntentRejected, source: "rejectionRegex", ruleID: "M5I-RT-N-CONSIDER"},
		{name: "template mismatch", text: "很抱歉，我觉得这个职位不匹配", label: IntentRejected, source: "rejectionRegex", ruleID: "M5I-RT-THINK-MISMATCH"},
		{name: "short consider", text: "暂时不考虑，谢谢", label: IntentRejected, source: "shortRejection", ruleID: "M5I-SR-01"},
		{name: "short uninterested", text: "不感兴趣", label: IntentRejected, source: "shortRejection", ruleID: "M5I-SR-02"},
		{name: "short unsuitable", text: "这个不合适", label: IntentRejected, source: "shortRejection", ruleID: "M5I-SR-04"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyIntentShortCircuitV4([]string{tc.text})
			if !got.Matched || got.Label != tc.label || got.Source != tc.source || got.RuleID != tc.ruleID {
				t.Fatalf("规则命中错误: %+v", got)
			}
		})
	}

	disabled := []string{
		"简历请查收", // RSM-03
		"不需要",   // SR-03
		"勿扰",    // SR-05
	}
	for _, text := range disabled {
		if got := ClassifyIntentShortCircuitV4([]string{text}); got.Matched {
			t.Fatalf("未获批准规则不应启用: text=%q result=%+v", text, got)
		}
	}
}

func TestV4ShortCircuitKeepsFamilyPriorityAcrossWholeTurn(t *testing.T) {
	got := ClassifyIntentShortCircuitV4([]string{
		"不考虑",
		"附件 简历",
	})
	if !got.Matched || got.Label != IntentInterested || got.RuleID != "M5I-RSM-02" {
		t.Fatalf("简历族必须优先于更早消息里的拒绝族: %+v", got)
	}

	got = ClassifyIntentShortCircuitV4([]string{
		"不合适",
		"很抱歉，我觉得这个职位不匹配",
	})
	if !got.Matched || got.RuleID != "M5I-RT-THINK-MISMATCH" {
		t.Fatalf("正则族必须优先于短拒绝族: %+v", got)
	}

	got = ClassifyIntentShortCircuitV4([]string{"附", "件简历"})
	if got.Matched {
		t.Fatalf("不同消息不得拼接后命中: %+v", got)
	}
}

func TestV4ShortCircuitNormalizesWhitespaceAndHonorsShortBoundaries(t *testing.T) {
	got := ClassifyIntentShortCircuitV4([]string{"很 抱歉， 我 暂时 不 考虑"})
	if !got.Matched || got.RuleID != "M5I-RT-N-CONSIDER" {
		t.Fatalf("Unicode 空白删除口径漂移: %+v", got)
	}

	short25 := "不考虑" + strings.Repeat("字", 22)
	if got = ClassifyIntentShortCircuitV4([]string{short25}); !got.Matched || got.RuleID != "M5I-SR-01" {
		t.Fatalf("25 code points 应可命中: %+v", got)
	}
	if got = ClassifyIntentShortCircuitV4([]string{short25 + "字"}); got.Matched {
		t.Fatalf("26 code points 不得走短拒绝: %+v", got)
	}
	for _, text := range []string{"不考虑?", "不考虑？"} {
		if got = ClassifyIntentShortCircuitV4([]string{text}); got.Matched {
			t.Fatalf("带问号不得走短拒绝: text=%q result=%+v", text, got)
		}
	}
}

func TestV4ShortCircuitEmptyTurnKeepsDeterministicNeutral(t *testing.T) {
	got := ClassifyIntentShortCircuitV4(nil)
	if !got.Matched || got.Label != IntentNeutral || got.Source != "emptyTurn" || got.RuleID != "" {
		t.Fatalf("空轮语义错误: %+v", got)
	}
}
