package m5ai

import (
	"strings"
	"testing"
	"time"
)

func frozenShanghai(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestHistoryRendererMatchesFrozenGolden(t *testing.T) {
	messages := []AdviceMessage{
		{Seq: 3, Direction: "outbound", Kind: "greeting", Text: " 你好 "},
		{Seq: 5, Direction: "inbound", Kind: "text", Text: "已撤回", Retracted: true},
		{Seq: 1, Direction: "inbound", Kind: "text", Text: "收到"},
		{Seq: 4, Direction: "inbound", Kind: "text", Text: "   "},
		{Seq: 2, Direction: "outbound", Kind: "text", Text: "请问方便聊聊吗"},
	}
	rendered, err := RenderHistory(messages)
	want := "候选人(消息):收到\n我(消息):请问方便聊聊吗\n我(招呼语):你好"
	if err != nil || rendered != want {
		t.Fatalf("历史 golden 漂移:\n got=%q\nwant=%q err=%v", rendered, want, err)
	}
	long := strings.Repeat("入", 1001)
	rendered, err = RenderHistory([]AdviceMessage{{Seq: 1, Direction: "inbound", Kind: "text", Text: long}})
	if err != nil || !strings.HasSuffix(rendered, historyTruncateSuffix) || len([]rune(strings.TrimSuffix(strings.TrimPrefix(rendered, "候选人(消息):"), historyTruncateSuffix))) != 1000 {
		t.Fatalf("Unicode 截断口径错误: err=%v", err)
	}
}

func TestDefaultScheduleAndReplyAssemblyMatchFrozenGolden(t *testing.T) {
	now := frozenShanghai(t, "2026-07-10T14:23:00+08:00")
	defaults := GenerateDefaultSlots(now)
	if len(defaults) != 84 || defaults[0] != "2026-07-10 15:00:00" || defaults[len(defaults)-1] != "2026-07-23 17:00:00" {
		t.Fatalf("默认时段漂移: count=%d first=%s last=%s", len(defaults), defaults[0], defaults[len(defaults)-1])
	}
	rendered, err := RenderReplyPrompt(
		"简历={简历}\n历史={对话历史}\n时段={推荐时段}",
		`{"basic":[]}`, "候选人(消息):你好", now,
		[]string{"2026-07-13 09:00:00", "2026-07-13 10:00:00"}, "仅供测试的客户事实",
	)
	want := "简历={\"basic\":[]}\n历史=候选人(消息):你好\n时段=可约面时间(见【可约面时间】)\n\n" +
		"【可约面时间】\n现在是2026年7月10日(周五)14:23。约面话术只能使用下列时间，不要编造其它面试时间；正文未规定怎么选时，优先最早的时段。\n" +
		"话术中最多写出1-2个具体时段，严禁罗列时段列表；写具体时间用「7月14日14:00」这种「X月X日+24小时制」格式。\n" +
		"7月13日(周一) 09:00-10:00 的整点\n\n【客户事实库】\n仅供测试的客户事实"
	if err != nil || rendered != want {
		t.Fatalf("reply 组装 golden 漂移:\n got=%q\nwant=%q err=%v", rendered, want, err)
	}
}

func TestIntentEnvelopeAndPromptAreCanonicalAndDisjoint(t *testing.T) {
	history := []AdviceMessage{{Seq: 1, Direction: "outbound", Kind: "greeting", Text: "你好"}}
	turn := []AdviceMessage{
		{Seq: 2, Direction: "inbound", Kind: "text", Text: "可以聊聊"},
		{Seq: 3, Direction: "inbound", Kind: "text", Text: "明天下午方便"},
	}
	content, envelope, err := RenderIntentPrompt("请判断。招呼={招呼语}；回复={回复}", "你好", history, turn)
	wantEnvelope := `{"historyBeforeTurn":[{"seq":1,"direction":"outbound","kind":"greeting","text":"你好"}],"currentTurn":[{"seq":2,"direction":"inbound","kind":"text","text":"可以聊聊"},{"seq":3,"direction":"inbound","kind":"text","text":"明天下午方便"}]}`
	wantContent := "请判断。招呼=你好；回复=明天下午方便\n\n【对话数据信封/v1】\n" + wantEnvelope
	if err != nil || envelope != wantEnvelope || content != wantContent {
		t.Fatalf("intent 组装漂移: content=%q envelope=%q err=%v", content, envelope, err)
	}
	if _, _, err := RenderIntentPrompt("招呼={招呼语}；回复={回复}", "你好", history, append(turn, AdviceMessage{Seq: 1, Direction: "inbound", Kind: "text", Text: "重复"})); err == nil {
		t.Fatal("轮前/本轮 seq 重叠必须拒绝")
	}
}

func TestTemplateValuesAreNeverReinterpretedAsTemplateSyntax(t *testing.T) {
	now := frozenShanghai(t, "2026-07-10T14:23:00+08:00")
	resume := `{"自评":"候选人原文 {推荐时段} {对话历史}"}`
	rendered, err := RenderReplyPrompt("简历={简历}\n历史={对话历史}\n时段={推荐时段}", resume,
		"候选人(消息):原文 {简历}", now, nil, "facts")
	if err != nil || !strings.Contains(rendered, "候选人原文 {推荐时段} {对话历史}") ||
		!strings.Contains(rendered, "候选人(消息):原文 {简历}") {
		t.Fatalf("注入值被二次解释: rendered=%q err=%v", rendered, err)
	}
	content, _, err := RenderIntentPrompt("招呼={招呼语}\n回复={回复}", "你好 {回复}", nil,
		[]AdviceMessage{{Seq: 1, Direction: "inbound", Kind: "text", Text: "正文 {招呼语}"}})
	if err != nil || !strings.Contains(content, "招呼=你好 {回复}") || !strings.Contains(content, "回复=正文 {招呼语}") {
		t.Fatalf("意向注入值被二次解释: content=%q err=%v", content, err)
	}
}

func TestResumeRendererUsesOwnedFiveSectionShape(t *testing.T) {
	got, err := RenderResumeJSON(`{"basic":[{"label":"学历","value":"本科"}],"expectations":[],"selfEvaluation":"","education":"示例大学","workExperiences":"示例公司"}`)
	want := `{"基本":[{"label":"学历","value":"本科"}],"期望":[],"自评":"","教育经历":"示例大学","工作经历":"示例公司"}`
	if err != nil || got != want {
		t.Fatalf("简历 renderer 漂移: got=%q want=%q err=%v", got, want, err)
	}
}
