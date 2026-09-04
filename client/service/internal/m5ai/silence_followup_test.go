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
	want := "姓名=姓名(见下方输入参数-姓名)\n年龄=年龄(见下方输入参数-年龄)\n性别=性别(见下方输入参数-性别)\n简历=简历(见下方输入参数-简历)\n\n" +
		"【输入参数】\n\n【输入参数-姓名】\n候选人\n\n【输入参数-年龄】\n30岁\n\n【输入参数-性别】\n女\n\n【输入参数-简历】\n" + resume +
		"\n\n" + realityBoundaryCompactPolicy
	if rendered != want {
		t.Fatalf("沉默追问渲染漂移:\n got=%q\nwant=%q", rendered, want)
	}
}

func TestRenderSilenceFollowupPromptUsesUnknownForMissingAgeAndGender(t *testing.T) {
	resume := canonicalSilenceResume(t, []resumeLabelValue{{Label: "求职状态", Value: "在职"}})
	rendered, err := RenderSilenceFollowupPrompt(
		"{姓名}|{年龄}|{性别}|{简历}",
		resume,
	)
	if err != nil || !strings.Contains(rendered, "【输入参数-年龄】\n未知\n") ||
		!strings.Contains(rendered, "【输入参数-性别】\n未知\n") {
		t.Fatalf("缺失事实未保守渲染: rendered=%q err=%v", rendered, err)
	}
}

// 2026-09-03 统一渲染:模板缺占位符、多占位符、带陌生占位符都不拒绝——必填输入恒
// 追加、陌生的原样保留;仍拒绝的只有简历事实自相矛盾。
func TestRenderSilenceFollowupPromptToleratesTemplateDefectsButRejectsResumeAmbiguity(t *testing.T) {
	resume := canonicalSilenceResume(t, []resumeLabelValue{{Label: "年龄", Value: "30岁"}})
	for _, prompt := range []string{
		"{姓名}{年龄}{性别}",
		"{姓名}{年龄}{性别}{简历}{动作}",
		"{姓名}{姓名}{年龄}{性别}{简历}",
	} {
		rendered, err := RenderSilenceFollowupPrompt(prompt, resume)
		if err != nil || strings.Count(rendered, "【输入参数-姓名】\n候选人\n") != 1 ||
			strings.Count(rendered, "【输入参数-简历】\n"+resume) != 1 {
			t.Fatalf("模板缺陷不得拒绝且数据各只一份: prompt=%q rendered=%q err=%v", prompt, rendered, err)
		}
	}
	rendered, err := RenderSilenceFollowupPrompt("{姓名}{年龄}{性别}{简历}{动作}", resume)
	if err != nil || !strings.HasPrefix(rendered, "姓名(见下方输入参数-姓名)年龄(见下方输入参数-年龄)性别(见下方输入参数-性别)简历(见下方输入参数-简历){动作}\n\n") {
		t.Fatalf("陌生占位符必须原样保留: rendered=%q err=%v", rendered, err)
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
