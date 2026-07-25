package m5ai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func canonicalSilenceResume(t *testing.T, basic []resumeLabelValue) string {
	t.Helper()
	raw, err := json.Marshal(struct {
		Basic           []resumeLabelValue `json:"基本"`
		Expectations    []resumeLabelValue `json:"期望"`
		SelfEvaluation  string             `json:"自评"`
		Education       string             `json:"教育经历"`
		WorkExperiences string             `json:"工作经历"`
	}{
		Basic: basic, Expectations: []resumeLabelValue{},
		SelfEvaluation: "自评", Education: "教育", WorkExperiences: "经历",
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestRenderSilenceFollowupPromptUsesNeutralNameAndCanonicalResumeFacts(t *testing.T) {
	resume := canonicalSilenceResume(t, []resumeLabelValue{
		{Label: "姓名", Value: "不应注入姓名占位"},
		{Label: "年龄", Value: "30岁"},
		{Label: "性别", Value: "女"},
	})
	rendered, err := RenderSilenceFollowupPrompt(
		"姓名={姓名}\n年龄={年龄}\n性别={性别}\n简历={简历}",
		resume,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rendered, "姓名=候选人\n年龄=30岁\n性别=女\n简历=") ||
		!strings.HasSuffix(rendered, resume) {
		t.Fatalf("沉默追问渲染错误: %s", rendered)
	}
}

func TestRenderSilenceFollowupPromptUsesUnknownForMissingAgeAndGender(t *testing.T) {
	resume := canonicalSilenceResume(t, []resumeLabelValue{{Label: "求职状态", Value: "在职"}})
	rendered, err := RenderSilenceFollowupPrompt(
		"{姓名}|{年龄}|{性别}|{简历}",
		resume,
	)
	if err != nil || !strings.HasPrefix(rendered, "候选人|未知|未知|") {
		t.Fatalf("缺失事实未保守渲染: rendered=%q err=%v", rendered, err)
	}
}

func TestRenderSilenceFollowupPromptRejectsTemplateAndResumeAmbiguity(t *testing.T) {
	resume := canonicalSilenceResume(t, []resumeLabelValue{{Label: "年龄", Value: "30岁"}})
	for _, prompt := range []string{
		"{姓名}{年龄}{性别}",
		"{姓名}{年龄}{性别}{简历}{动作}",
		"{姓名}{姓名}{年龄}{性别}{简历}",
	} {
		if rendered, err := RenderSilenceFollowupPrompt(prompt, resume); err == nil || rendered != "" {
			t.Fatalf("非法模板未拒绝: prompt=%q rendered=%q err=%v", prompt, rendered, err)
		}
	}
	ambiguous := canonicalSilenceResume(t, []resumeLabelValue{
		{Label: "年龄", Value: "30岁"},
		{Label: "年龄", Value: "31岁"},
	})
	if rendered, err := RenderSilenceFollowupPrompt(
		"{姓名}{年龄}{性别}{简历}", ambiguous,
	); err == nil || rendered != "" {
		t.Fatalf("冲突年龄事实未拒绝: rendered=%q err=%v", rendered, err)
	}
}

func TestSilenceFollowupPromptReadsUniqueSourceDocument(t *testing.T) {
	revision := ContextRevision{SourcePackage: JobConfigDocumentPackage{Documents: []JobConfigDocument{
		{DocType: "沉默追问", Content: "{姓名}{年龄}{性别}{简历}"},
	}}}
	prompt, err := SilenceFollowupPrompt(revision)
	if err != nil || prompt != "{姓名}{年龄}{性别}{简历}" {
		t.Fatalf("唯一原文提取失败: prompt=%q err=%v", prompt, err)
	}
	revision.SourcePackage.Documents = append(revision.SourcePackage.Documents,
		JobConfigDocument{DocType: "沉默追问", Content: "{姓名}{年龄}{性别}{简历}"},
	)
	if prompt, err := SilenceFollowupPrompt(revision); err == nil || prompt != "" {
		t.Fatalf("重复原文未拒绝: prompt=%q err=%v", prompt, err)
	}
}

func TestParseSilenceFollowupSuggestionAcceptsOnlyTextAndOptionalReview(t *testing.T) {
	got, err := ParseSilenceFollowupSuggestion(`{"话术":" 还在考虑这个机会吗？ ","抓的点":"经历匹配"}`)
	if err != nil || got.Text != "还在考虑这个机会吗？" {
		t.Fatalf("沉默追问解析失败: got=%+v err=%v", got, err)
	}
	for _, raw := range []string{
		`{"抓的点":"缺话术"}`,
		`{"话术":["不能是数组"]}`,
		`{"话术":"你好","抓的点":["不能是数组"]}`,
		`{"话术":"你好","动作":"发起换微信邀请"}`,
		`{"话术":"a","话术":"b"}`,
		`{"话术":"   "}`,
	} {
		if suggestion, err := ParseSilenceFollowupSuggestion(raw); err == nil ||
			suggestion != (SilenceFollowupSuggestion{}) {
			t.Fatalf("非法输出未拒绝: raw=%s suggestion=%+v err=%v", raw, suggestion, err)
		}
	}
}

func TestAIAdvisorUsesDedicatedSilenceFollowupPurposeAndReplyBudget(t *testing.T) {
	provider := &scriptedProvider{responses: []CompletionResponse{{
		JSONText: `{"话术":"还在考虑吗？"}`,
	}}}
	advisor, err := NewAIAdvisor(provider)
	if err != nil {
		t.Fatal(err)
	}
	suggestion, _, err := advisor.SuggestSilenceFollowup(context.Background(), "fixture")
	if err != nil || suggestion.Text != "还在考虑吗？" || len(provider.requests) != 1 ||
		provider.requests[0].Purpose != PurposeSilenceFollowup ||
		provider.requests[0].MaxOutputTokens != SilenceFollowupOutputTokenLimit {
		t.Fatalf("沉默追问 advisor 语义错误: suggestion=%+v requests=%+v err=%v",
			suggestion, provider.requests, err)
	}
}
