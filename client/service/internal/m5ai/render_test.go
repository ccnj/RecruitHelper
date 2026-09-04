package m5ai

import (
	"fmt"
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
	// 241 = 冻结当天周五 14:23 剩余 7 个半小时格(14:30…17:30) + 其后 13 天各 18 个
	//（含周末，2026-08-01 裁决；半小时步长，2026-09-04 裁决）。
	if len(defaults) != 241 || defaults[0] != "2026-07-10 14:30:00" || defaults[len(defaults)-1] != "2026-07-23 17:30:00" {
		t.Fatalf("默认时段漂移: count=%d first=%s last=%s", len(defaults), defaults[0], defaults[len(defaults)-1])
	}
	rendered, err := RenderReplyPrompt(
		"简历={简历}\n历史={对话历史}\n时段={推荐时段}",
		`{"basic":[]}`, "候选人(消息):你好", "", now,
		[]string{"2026-07-13 09:00:00", "2026-07-13 09:30:00", "2026-07-13 10:00:00"},
	)
	// 统一渲染(2026-09-03):三个 token 全渲染成指针,数据以【输入参数】起头、按
	// promptInputSpecs 的顺序追加到正文末尾(推荐时段 → 简历 → 对话历史);模板没
	// 引用 {事实库},事实库子块不出现。
	want := "简历=简历(见下方输入参数-简历)\n历史=对话历史(见下方输入参数-对话历史)\n时段=推荐时段(见下方输入参数-推荐时段)\n\n" +
		"【输入参数】\n\n" +
		"【输入参数-推荐时段】\n现在是2026年7月10日(周五)14:23。约面话术只能使用下列时间，不要编造其它面试时间。\n" +
		"话术中最多写出1-2个具体时段，严禁罗列时段列表；写具体时间用「7月14日14:00」这种「X月X日+24小时制」格式。\n" +
		"7月13日(周一) 09:00-10:00 的整点与半点\n\n" +
		"【输入参数-简历】\n{\"basic\":[]}\n\n" +
		"【输入参数-对话历史】\n" + historyGuard + "\n候选人(消息):你好"
	if err != nil || rendered != want {
		t.Fatalf("reply 组装 golden 漂移:\n got=%q\nwant=%q err=%v", rendered, want, err)
	}
}

func TestPersistedRecommendedTimeTextNeverMovesWithWallClock(t *testing.T) {
	now := frozenShanghai(t, "2026-07-10T14:23:00+08:00")
	frozen, err := FreezeRecommendedTimeText(now, []string{"2026-07-13 09:00:00"})
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderReplyPromptFrozen(
		"简历={简历}\n历史={对话历史}\n时段={推荐时段}",
		`{"基本":[]}`, "候选人(消息):你好", frozen, "",
	)
	if err != nil || !strings.Contains(rendered, "现在是2026年7月10日(周五)14:23。") ||
		strings.Contains(rendered, "2026年7月11日") {
		t.Fatalf("冻结推荐时段发生漂移: rendered=%q err=%v", rendered, err)
	}
	if _, err := RenderReplyPromptFrozen(
		"简历={简历}\n历史={对话历史}\n时段={推荐时段}",
		`{"基本":[]}`, "", frozen+`{"候选人正文":"不得回显"}`, "",
	); err == nil || strings.Contains(err.Error(), "候选人正文") {
		t.Fatalf("损坏的冻结文本必须固定分类拒绝且不回显内容: %v", err)
	}
}

func TestFrozenRecommendedTimeCarriesCanonicalSlotsWithoutBreakingLegacyRender(t *testing.T) {
	now := frozenShanghai(t, "2026-07-10T14:23:00+08:00")
	frozen, err := FreezeRecommendedTimeText(
		now,
		[]string{"2026-07-13 09:00:00", "2026-07-13 10:00:00"},
	)
	if err != nil {
		t.Fatal(err)
	}
	slots, ok := FrozenRecommendedSlots(frozen)
	if !ok || len(slots) != 2 ||
		slots[0] != "2026-07-13 09:00:00" ||
		slots[1] != "2026-07-13 10:00:00" {
		t.Fatalf("新冻结载荷没有保留 canonical slots: slots=%+v ok=%v", slots, ok)
	}
	slots[0] = "被调用方篡改"
	reloaded, ok := FrozenRecommendedSlots(frozen)
	if !ok || reloaded[0] != "2026-07-13 09:00:00" {
		t.Fatalf("冻结 slots 被外部切片改写: %+v", reloaded)
	}

	legacy := `{"inline":"旧内联时段","block":"旧时段块"}`
	if _, ok := FrozenRecommendedSlots(legacy); ok {
		t.Fatal("仅带旧渲染文本的 turn 不得取得动作授权 slots")
	}
	rendered, err := RenderReplyPromptFrozen(
		"简历={简历}\n历史={对话历史}\n时段={推荐时段}",
		`{"基本":[]}`,
		"候选人(消息):你好",
		legacy, "",
	)
	if err != nil || !strings.Contains(rendered, "旧时段块") {
		t.Fatalf("旧 turn 必须仍可按原冻结文本渲染: rendered=%q err=%v", rendered, err)
	}
}

func TestMatchFrozenRecommendedMeetingTimeIsStrictAndUnique(t *testing.T) {
	slots := []string{
		"2026-07-14 09:00:00",
		"2026-07-14 14:00:00",
		"2026-07-14 14:30:00",
	}
	want := frozenShanghai(t, "2026-07-14T14:00:00+08:00").UnixMilli()
	if got, ok := MatchFrozenRecommendedMeetingTime(slots, " \n7月14日14:00\t"); !ok || got != want {
		t.Fatalf("合法时间未唯一命中: got=%d want=%d ok=%v", got, want, ok)
	}
	// 真机 2026-08-04：模型自然写出的两种变体——小时不补前导零、日期与时间之间
	// 一个半角空格——语义与规范写法完全相同。此前 "7月14日 14:00" 被本测试断言
	// 为非法格式，那条判断随本批推翻：拦下它没有任何安全收益，代价却是整轮回复
	// 作废、真实候选人收不到约面。放宽的只是写法，时刻仍须逐字段精确命中。
	earlier := frozenShanghai(t, "2026-07-14T09:00:00+08:00").UnixMilli()
	half := frozenShanghai(t, "2026-07-14T14:30:00+08:00").UnixMilli()
	for _, accepted := range []struct {
		value string
		want  int64
	}{
		{"7月14日9:00", earlier},  // 小时无前导零
		{"7月14日09:00", earlier}, // 规范写法仍然成立
		{"7月14日 14:00", want},   // 分隔空格
		{"7月14日 9:00", earlier}, // 两种变体叠加
		{"7月14日14:30", half},    // 半点(2026-09-04 裁决):与 14:00 是两个不同的时段
		{"7月14日 14:30", half},   // 半点 + 分隔空格
	} {
		if got, ok := MatchFrozenRecommendedMeetingTime(slots, accepted.value); !ok || got != accepted.want {
			t.Fatalf("等价写法未命中: value=%q got=%d want=%d ok=%v",
				accepted.value, got, accepted.want, ok)
		}
	}
	for _, invalid := range []string{
		"07月14日14:00",  // 月份补零：无实际证据，不放宽
		"7月14日　14:00",  // 全角空格：同上
		"7月14日  14:00", // 两个空格：只放行单个半角空格
		"7月14日2:00",    // 写法合法但 02:00 不在冻结时段——语义一步没松
		"7月14日14:15",   // 不在半小时格上,也不在冻结时段
		"2026年7月14日14:00",
		"明天下午两点",
	} {
		if got, ok := MatchFrozenRecommendedMeetingTime(slots, invalid); ok || got != 0 {
			t.Fatalf("非法格式被接受: value=%q got=%d ok=%v", invalid, got, ok)
		}
	}
	if got, ok := MatchFrozenRecommendedMeetingTime(
		[]string{"2026-07-14 14:00:00", "2026-07-14 14:00:00"},
		"7月14日14:00",
	); ok || got != 0 {
		t.Fatalf("多候选命中不得被任取一项: got=%d ok=%v", got, ok)
	}
	if got, ok := MatchFrozenRecommendedMeetingTime(
		slots,
		"7月15日14:00",
	); ok || got != 0 {
		t.Fatalf("零命中不得获得动作授权: got=%d ok=%v", got, ok)
	}
	// 冻结列表本身不在格上(只可能是库被直接改过):整轮不授权,不挑其中"合法"的那条。
	if got, ok := MatchFrozenRecommendedMeetingTime(
		[]string{"2026-07-14 14:00:00", "2026-07-14 14:15:00"},
		"7月14日14:00",
	); ok || got != 0 {
		t.Fatalf("含格外时段的冻结列表不得授权: got=%d ok=%v", got, ok)
	}
}

// 半小时步长(2026-09-04 甲方裁决)对冻结载荷的三条边界:半点 slot 能冻结能读回;
// 2026-09-04 之前冻结的整点载荷原样可读(存量轮不迁移);格外时刻的载荷整体判非法。
func TestFrozenRecommendedTimeAcceptsHalfHourAndRejectsOffGrid(t *testing.T) {
	now := frozenShanghai(t, "2026-07-10T14:23:00+08:00")
	frozen, err := FreezeRecommendedTimeText(now, []string{"2026-07-13 09:30:00", "2026-07-13 10:00:00"})
	if err != nil {
		t.Fatal(err)
	}
	slots, ok := FrozenRecommendedSlots(frozen)
	if !ok || len(slots) != 2 || slots[0] != "2026-07-13 09:30:00" {
		t.Fatalf("半点 slot 未能冻结读回: slots=%v ok=%v", slots, ok)
	}
	legacyWholeHour := `{"inline":"旧内联","block":"旧块","slots":["2026-07-13 09:00:00","2026-07-13 10:00:00"]}`
	if slots, ok := FrozenRecommendedSlots(legacyWholeHour); !ok || len(slots) != 2 {
		t.Fatalf("整点存量载荷必须原样可读: slots=%v ok=%v", slots, ok)
	}
	offGrid := `{"inline":"x","block":"y","slots":["2026-07-13 09:15:00"]}`
	if _, ok := FrozenRecommendedSlots(offGrid); ok {
		t.Fatal("格外时刻的载荷不得取得动作授权 slots")
	}
	if _, err := FreezeRecommendedTimeText(now, []string{"2026-07-13 09:15:00"}); err == nil {
		t.Fatal("格外时刻不得被冻结进轮行")
	}
}

// 概览按"相邻一个步长合段、孤立单写"压成区间,区间两端都是时段起点。
func TestSlotsOverviewMergesAdjacentHalfHours(t *testing.T) {
	overview, err := slotsOverview([]string{
		"2026-07-13 09:00:00", "2026-07-13 09:30:00", "2026-07-13 10:00:00",
		"2026-07-13 14:00:00",
		"2026-07-14 09:30:00", "2026-07-14 10:00:00",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "7月13日(周一) 09:00-10:00、14:00 的整点与半点\n7月14日(周二) 09:30-10:00 的整点与半点"
	if overview != want {
		t.Fatalf("概览漂移:\n got=%q\nwant=%q", overview, want)
	}
	// 只差一个整点、中间缺半点的两个时段不得被合成区间——区间意味着中间的半点也可选。
	overview, err = slotsOverview([]string{"2026-07-13 09:00:00", "2026-07-13 10:00:00"})
	if err != nil || overview != "7月13日(周一) 09:00、10:00 的整点与半点" {
		t.Fatalf("非相邻时段被误合成区间: %q err=%v", overview, err)
	}
}

func TestAppendRealityBoundaryPlacesBlockLastAndRejectsEmptyPrompt(t *testing.T) {
	rendered, err := AppendRealityBoundary("已渲染的回复提示词\n\n【本轮可选动作】\n· 无")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(rendered, realityBoundaryPolicy) {
		t.Fatalf("现实边界块必须在提示词最末: rendered=%q", rendered)
	}
	for _, anchor := range []string{
		"人不在任何现场",
		"“下来接你”“我在前台”“我马上到”“在公司等你”这类话一个字不许出现",
		"只许逐字照抄【输入参数-事实库】写了的",
		"没发生过的见面不许说成发生过",
		"他定下具体时间才填「发起线下面试」",
		"错认在自己身上，不许暗示他记错",
	} {
		if !strings.Contains(rendered, anchor) {
			t.Fatalf("现实边界块缺失条款 %q", anchor)
		}
	}
	if _, err := AppendRealityBoundary(" \n\t "); err == nil {
		t.Fatal("空基础 prompt 不得被现实边界文本伪装成合法请求")
	}
}

func TestServiceReplyPromptForbidsPresenceClaims(t *testing.T) {
	rendered, err := RenderServiceReplyPrompt(nil, false, []string{"我到楼下了"})
	if err != nil {
		t.Fatal(err)
	}
	for _, anchor := range []string{
		"人不在任何现场",
		`不得出现"下来接你""我在前台""我马上到"这类话`,
		"不得给出或编造任何地址、楼层",
	} {
		if !strings.Contains(rendered, anchor) {
			t.Fatalf("服务阶段提示词缺失到场禁令 %q: rendered=%q", anchor, rendered)
		}
	}
}

func TestServiceReplyPolicyAppendsDeterministicBoundaries(t *testing.T) {
	service, err := AppendServiceReplyPolicy("原职位话术")
	if err != nil ||
		!strings.Contains(service, "候选人已经接受面试") ||
		!strings.Contains(service, "不得承诺“帮您反馈”“我去问下”") {
		t.Fatalf("服务态约束未完整附加: rendered=%q err=%v", service, err)
	}
	if _, err := AppendServiceReplyPolicy(" \n\t "); err == nil {
		t.Fatal("空基础 prompt 不得被策略文本伪装成合法请求")
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
	wantContent := "请判断。招呼=招呼语(见下方输入参数-招呼语)；回复=回复(见下方输入参数-回复)\n\n" +
		"【输入参数】\n\n【输入参数-招呼语】\n你好\n\n【输入参数-回复】\n明天下午方便\n\n" +
		"【对话数据信封/v1】\n" + wantEnvelope
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
		"候选人(消息):原文 {简历}", "", now, nil)
	if err != nil || !strings.Contains(rendered, "候选人原文 {推荐时段} {对话历史}") ||
		!strings.Contains(rendered, "候选人(消息):原文 {简历}") {
		t.Fatalf("注入值被二次解释: rendered=%q err=%v", rendered, err)
	}
	content, _, err := RenderIntentPrompt("招呼={招呼语}\n回复={回复}", "你好 {回复}", nil,
		[]AdviceMessage{{Seq: 1, Direction: "inbound", Kind: "text", Text: "正文 {招呼语}"}})
	if err != nil || !strings.Contains(content, "【输入参数-招呼语】\n你好 {回复}\n") || !strings.Contains(content, "【输入参数-回复】\n正文 {招呼语}\n") {
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

func TestReplyActionMenuBlockOnlySubtracts(t *testing.T) {
	t.Run("wechat still open lists all three", func(t *testing.T) {
		block := replyActionMenuBlock(ReplyActionMenu{
			AllowStartMeeting: true, AllowInviteWechat: true,
			WechatLine: ReplyMenuWechatNotInvited,
		})
		for _, want := range []string{"· 无", "· 发起线上会议", "· 发起换微信邀请"} {
			if !strings.Contains(block, want) {
				t.Fatalf("微信线未推进时应列出 %q: %s", want, block)
			}
		}
		if strings.Contains(block, "不得填") {
			t.Fatalf("尚可邀请时不应出现微信禁止句: %s", block)
		}
	})

	t.Run("exchanged forbids invite and the phrasing that implies it", func(t *testing.T) {
		block := replyActionMenuBlock(ReplyActionMenu{
			AllowStartMeeting: true, WechatLine: ReplyMenuWechatExchanged,
		})
		if strings.Contains(block, "· 发起换微信邀请") {
			t.Fatalf("已换号不得把换微信列为可选项: %s", block)
		}
		for _, want := range []string{"已经交换成功", "不得填「发起换微信邀请」", "加个微信"} {
			if !strings.Contains(block, want) {
				t.Fatalf("已换号应含 %q: %s", want, block)
			}
		}
	})

	t.Run("invited says awaiting, not exchanged", func(t *testing.T) {
		block := replyActionMenuBlock(ReplyActionMenu{
			AllowStartMeeting: true, WechatLine: ReplyMenuWechatInvited,
		})
		// 已邀请说成"已经交换成功"就是拿假事实喂模型。
		if strings.Contains(block, "已经交换成功") {
			t.Fatalf("已邀请未换号不得声称交换成功: %s", block)
		}
		if !strings.Contains(block, "正等对方通过") ||
			!strings.Contains(block, "不得填「发起换微信邀请」") {
			t.Fatalf("已邀请应说明正在等待且禁止重复邀请: %s", block)
		}
	})

	t.Run("nothing allowed leaves only 无", func(t *testing.T) {
		block := replyActionMenuBlock(ReplyActionMenu{WechatLine: ReplyMenuWechatNotInvited})
		if strings.Contains(block, "· 发起线上会议") || strings.Contains(block, "· 发起换微信邀请") {
			t.Fatalf("无可用动作时只能留「无」: %s", block)
		}
		if !strings.Contains(block, "· 无") {
			t.Fatalf("「无」必须始终可填: %s", block)
		}
		// 没有可约时段时不谈时间格式,免得凭空引出"可以约时间"的暗示。
		if strings.Contains(block, "8月3日10:00") {
			t.Fatalf("不能约面时不应出现时间格式指引: %s", block)
		}
	})

	t.Run("block never encourages an action", func(t *testing.T) {
		block := replyActionMenuBlock(ReplyActionMenu{
			AllowStartMeeting: true, AllowInviteWechat: true,
			WechatLine: ReplyMenuWechatNotInvited,
		})
		// 30 次实验里唯一那次作废,是块把"发起线上会议"说成必选项诱导出来的。
		for _, banned := range []string{"请填", "应当填", "优先填", "尽量填"} {
			if strings.Contains(block, banned) {
				t.Fatalf("块只做减法,不得鼓励选择动作(命中 %q): %s", banned, block)
			}
		}
		if !strings.Contains(block, "默认就填这个") {
			t.Fatalf("「无」必须有明确的默认地位: %s", block)
		}
	})
}

func TestAppendReplyActionMenuKeepsRenderedPromptAndAppendsOnce(t *testing.T) {
	rendered, err := AppendReplyActionMenu("已渲染的回复提示词", ReplyActionMenu{
		AllowStartMeeting: true, WechatLine: ReplyMenuWechatExchanged,
	})
	if err != nil {
		t.Fatalf("追加失败: %v", err)
	}
	if !strings.HasPrefix(rendered, "已渲染的回复提示词") {
		t.Fatalf("原提示词必须原样保留: %s", rendered)
	}
	if strings.Count(rendered, replyActionMenuHeading) != 1 {
		t.Fatalf("块只能出现一次: %s", rendered)
	}
	if _, err := AppendReplyActionMenu("   ", ReplyActionMenu{}); err == nil {
		t.Fatal("空提示词必须响亮失败")
	}
}

// 已发出过邀面卡(2026-08-21 甲方裁决):块不再列两项邀面动作,改为一句事实与
// 禁止;措辞不得说"正等对方确认"——那张卡也可能已被拒,拿假事实喂模型是禁区。
func TestReplyActionMenuBlockAfterInterviewCardSent(t *testing.T) {
	block := replyActionMenuBlock(ReplyActionMenu{
		InterviewCardSent: true, AllowInviteWechat: true, WechatLine: ReplyMenuWechatNotInvited,
	})
	if strings.Contains(block, "· 发起线上会议") || strings.Contains(block, "· 发起线下面试") {
		t.Fatalf("已发卡不得把邀面列为可选项: %s", block)
	}
	for _, want := range []string{"邀面卡已经发出过", "不得填「发起线上会议」「发起线下面试」", "不要另约时间"} {
		if !strings.Contains(block, want) {
			t.Fatalf("已发卡应含 %q: %s", want, block)
		}
	}
	if strings.Contains(block, "正等对方确认") || strings.Contains(block, "8月3日10:00") {
		t.Fatalf("已发卡不得声称待答或给出时间格式指引: %s", block)
	}
	if !strings.Contains(block, "· 发起换微信邀请") {
		t.Fatalf("邀面闸不得波及换微信选项: %s", block)
	}
	for _, banned := range []string{"请填", "应当填", "优先填", "尽量填"} {
		if strings.Contains(block, banned) {
			t.Fatalf("块只做减法(命中 %q): %s", banned, block)
		}
	}
}

// 2026-09-03 统一渲染的三条边界:模板引用 {事实库} 才追加事实库子块、事实库原文为
// 空时子块写固定缺席文案、陌生占位符原样保留且不拒绝。
func TestReplyRendererFactsBlockAndUnknownTokens(t *testing.T) {
	now := frozenShanghai(t, "2026-07-10T14:23:00+08:00")
	slots := []string{"2026-07-13 09:00:00"}
	withFacts, err := RenderReplyPrompt("简历={简历} 历史={对话历史} 时段={推荐时段} 事实={事实库}",
		`{"basic":[]}`, "候选人(消息):你好", "  fixture://facts  ", now, slots)
	if err != nil || !strings.HasSuffix(withFacts, "【输入参数-事实库】\nfixture://facts") ||
		!strings.Contains(withFacts, "事实=事实库(见下方输入参数-事实库)") {
		t.Fatalf("事实库子块未按引用追加: rendered=%q err=%v", withFacts, err)
	}
	emptyFacts, err := RenderReplyPrompt("{简历}{对话历史}{推荐时段}{事实库}",
		`{"basic":[]}`, "候选人(消息):你好", "", now, slots)
	if err != nil || !strings.HasSuffix(emptyFacts, "【输入参数-事实库】\n"+customerFactsMissingText) {
		t.Fatalf("事实库为空时须写缺席文案并照常渲染: rendered=%q err=%v", emptyFacts, err)
	}
	unreferenced, err := RenderReplyPrompt("{简历}{对话历史}{推荐时段}",
		`{"basic":[]}`, "候选人(消息):你好", "fixture://facts", now, slots)
	if err != nil || strings.Contains(unreferenced, "事实库") {
		t.Fatalf("模板未引用事实库时不得追加: rendered=%q err=%v", unreferenced, err)
	}
	unknown, err := RenderReplyPrompt("看 {简厉} 与 {对话历史}", `{"basic":[]}`, "候选人(消息):你好", "", now, slots)
	if err != nil || !strings.HasPrefix(unknown, "看 {简厉} 与 对话历史(见下方输入参数-对话历史)\n\n【输入参数】\n\n【输入参数-推荐时段】") ||
		strings.Count(unknown, "【输入参数-简历】\n{\"basic\":[]}") != 1 {
		t.Fatalf("陌生占位符须原样保留、必填子块仍恒追加: rendered=%q err=%v", unknown, err)
	}
	legacy := `{"inline":"旧内联时段","block":"【可约面时间】\n旧时段块"}`
	rendered, err := RenderReplyPromptFrozen("{推荐时段}{简历}{对话历史}", `{"basic":[]}`, "候选人(消息):你好", legacy, "")
	if err != nil || !strings.Contains(rendered, "【输入参数-推荐时段】\n旧时段块\n") || strings.Contains(rendered, "【可约面时间】") {
		t.Fatalf("存量冻结载荷的旧块标题须剥离后套新外壳: rendered=%q err=%v", rendered, err)
	}
}

// 「优先提」时段(2026-09-04 甲方裁决):按候选人稳定哈希从冻结全表挑最近可约日的
// 一上午一下午;它只影响措辞,挑出的一定是全表成员。
func TestPreferredProposalSlotsPicksOneMorningOneAfternoonOnNextDay(t *testing.T) {
	// 2026-07-13(周一)09:00 冻结:当天还剩很多时段,但优先提要跳过今天取 07-14。
	now := frozenShanghai(t, "2026-07-13T09:00:00+08:00")
	slots := GenerateSlots(now, DefaultInterviewSchedule())
	picked := PreferredProposalSlots("profile-a", now, slots)
	if len(picked) != 2 {
		t.Fatalf("默认周表应挑出一上午一下午: %v", picked)
	}
	for _, slot := range picked {
		if slot[:10] != "2026-07-14" {
			t.Fatalf("必须取最近一个晚于冻结日的可约日: %v", picked)
		}
		if !containsFixtureString(slots, slot) {
			t.Fatalf("挑出的时段必须是全表成员: %s", slot)
		}
	}
	if picked[0][11:13] >= "12" || picked[1][11:13] < "13" {
		t.Fatalf("第一项须在上午、第二项须在下午: %v", picked)
	}
	// 稳定:同一候选人多次调用结果一致;不同候选人散在不同格上。
	if again := PreferredProposalSlots("profile-a", now, slots); again[0] != picked[0] || again[1] != picked[1] {
		t.Fatalf("同一 seed 必须稳定: %v vs %v", picked, again)
	}
	mornings, afternoons := map[string]struct{}{}, map[string]struct{}{}
	for i := 0; i < 200; i++ {
		p := PreferredProposalSlots(fmt.Sprintf("profile-%d", i), now, slots)
		mornings[p[0][11:16]] = struct{}{}
		afternoons[p[1][11:16]] = struct{}{}
	}
	// 默认表上午 6 格(09:00-11:30)、下午 10 格(13:00-17:30),200 个候选人应全部铺开。
	if len(mornings) != 6 || len(afternoons) != 10 {
		t.Fatalf("哈希轮转没有铺开: 上午 %d 格 下午 %d 格", len(mornings), len(afternoons))
	}
}

func TestPreferredProposalSlotsEdges(t *testing.T) {
	now := frozenShanghai(t, "2026-07-13T09:00:00+08:00")
	// 只有上午格的一天:只挑一个,不合成下午。
	onlyMorning := []string{"2026-07-14 09:00:00", "2026-07-14 09:30:00", "2026-07-14 12:00:00"}
	if p := PreferredProposalSlots("x", now, onlyMorning); len(p) != 1 || p[0][11:13] != "09" {
		t.Fatalf("只有上午格时应只挑一个上午时段(12:xx 不算下午): %v", p)
	}
	// 全表只剩今天:没有晚于冻结日的可约日,不挑。
	if p := PreferredProposalSlots("x", now, []string{"2026-07-13 15:00:00"}); p != nil {
		t.Fatalf("只有当天时段时不得挑: %v", p)
	}
	if p := PreferredProposalSlots("x", now, nil); p != nil {
		t.Fatalf("空表不得挑: %v", p)
	}
	// 乱序输入也取最早的那一天。
	unordered := []string{"2026-07-20 10:00:00", "2026-07-15 14:00:00", "2026-07-15 10:30:00"}
	if p := PreferredProposalSlots("x", now, unordered); len(p) != 2 || p[0] != "2026-07-15 10:30:00" || p[1] != "2026-07-15 14:00:00" {
		t.Fatalf("应取最近可约日 07-15 的一上午一下午: %v", p)
	}
}

func TestReplyActionMenuBlockRendersPreferredSlotsOnlyWhenMeetingAllowed(t *testing.T) {
	sameDay := []string{"2026-07-14 10:30:00", "2026-07-14 15:00:00"}
	block := replyActionMenuBlock(ReplyActionMenu{
		AllowStartMeeting: true, WechatLine: ReplyMenuWechatNotInvited, PreferredSlots: sameDay,
	})
	wantLine := "本轮抛时段优先提这两个：7月14日10:30或15:00（候选人另提别的时间，按【输入参数-推荐时段】判断）。"
	if !strings.Contains(block, wantLine) {
		t.Fatalf("允许邀面时应带优先提句: %s", block)
	}
	if strings.Index(block, wantLine) > strings.Index(block, "话术里的时间一律写成") {
		t.Fatalf("优先提句应在时间格式句之前: %s", block)
	}
	// 跨天各带日期;单个时段换措辞。
	block = replyActionMenuBlock(ReplyActionMenu{
		AllowStartMeeting: true, WechatLine: ReplyMenuWechatNotInvited,
		PreferredSlots: []string{"2026-07-14 10:30:00", "2026-07-15 15:00:00"},
	})
	if !strings.Contains(block, "本轮抛时段优先提这两个：7月14日10:30或7月15日15:00（") {
		t.Fatalf("跨天应各带日期: %s", block)
	}
	block = replyActionMenuBlock(ReplyActionMenu{
		AllowStartMeeting: true, WechatLine: ReplyMenuWechatNotInvited,
		PreferredSlots: []string{"2026-07-14 10:30:00"},
	})
	if !strings.Contains(block, "本轮抛时段优先提：7月14日10:30（") {
		t.Fatalf("单个时段应换措辞: %s", block)
	}
	// 已发卡(AllowStartMeeting 必为假)时即便带了时段也不渲染——重放第五轮实证,
	// 否则模型会违反「不许自己定新时间」再抛时段。
	block = replyActionMenuBlock(ReplyActionMenu{
		AllowStartMeeting: false, InterviewCardSent: true,
		WechatLine: ReplyMenuWechatNotInvited, PreferredSlots: sameDay,
	})
	if strings.Contains(block, "优先提") {
		t.Fatalf("已发卡后不得出现优先提句: %s", block)
	}
	// 时段串非法时整句省略,不喂半截。
	block = replyActionMenuBlock(ReplyActionMenu{
		AllowStartMeeting: true, WechatLine: ReplyMenuWechatNotInvited,
		PreferredSlots: []string{"2026-07-14 10:30:00", "坏掉的"},
	})
	if strings.Contains(block, "优先提") {
		t.Fatalf("时段串非法时应整句省略: %s", block)
	}
	// 没挑到时段(空)也不渲染,其余块内容照旧。
	block = replyActionMenuBlock(ReplyActionMenu{AllowStartMeeting: true, WechatLine: ReplyMenuWechatNotInvited})
	if strings.Contains(block, "优先提") || !strings.Contains(block, "话术里的时间一律写成") {
		t.Fatalf("无优先提时段时块应保持原样: %s", block)
	}
}
