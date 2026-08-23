package jobconfig

import (
	"strings"
	"testing"
)

// 取自真实后台的发布参数形态（正文截短）。薪资 2万/4万 正好卡在 2 倍上限，
// 是平台接受的合法组合。
const validPublishParams = `{
  "工作性质": "社招全职",
  "职位名称": "财富传承顾问",
  "职位描述": "【关于团队】\n这是某团队的财富传承顾问岗。",
  "职位类别": "理财顾问",
  "最低学历": "大专",
  "工作经验": "3-5年",
  "最低月薪": "2万",
  "最高月薪": "4万",
  "薪资月数": "12个月",
  "职位关键词": ["个人客户", "银行", "基金", "投资与资产管理"],
  "工作地址": "默认",
  "招聘人数": 1,
  "对求职者展示": false,
  "简历同步至同事": null,
  "同步至我的邮箱": false
}`

func issueFields(issues []PublishIssue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, issue.Field+":"+issue.Message)
	}
	return strings.Join(parts, " | ")
}

func TestParsePublishSpecAcceptsRealBackendDocument(t *testing.T) {
	spec, issues := ParsePublishSpec(validPublishParams)
	if len(issues) != 0 {
		t.Fatalf("真实发布参数不应有预检问题: %s", issueFields(issues))
	}
	if spec.EmploymentType != "社招全职" || spec.Education != "大专" || spec.Experience != "3-5年" {
		t.Fatalf("枚举字段解析错误: %+v", spec)
	}
	if spec.SalaryMin != "2万" || spec.SalaryMax != "4万" || spec.SalaryMonths != "12个月" {
		t.Fatalf("薪资字段解析错误: %+v", spec)
	}
	if spec.Headcount != 1 || spec.Workplace != "默认" {
		t.Fatalf("其余字段解析错误: %+v", spec)
	}
	// 关键词是死字段：只解析进 DeadKeywords 供提示，绝不进入填充路径。
	if len(spec.DeadKeywords) != 4 {
		t.Fatalf("死字段关键词未解析进提示用字段: %+v", spec.DeadKeywords)
	}
}

func TestParsePublishSpecRejectsSalaryBeyondDoubleCap(t *testing.T) {
	// 后台的 validate_publish_params 只看字段非空，这条规则只能由本地预检拦。
	raw := strings.Replace(validPublishParams, `"最高月薪": "4万"`, `"最高月薪": "5万"`, 1)
	_, issues := ParsePublishSpec(raw)
	if len(issues) != 1 || issues[0].Field != "最高月薪" ||
		!strings.Contains(issues[0].Message, "2 倍") || !strings.Contains(issues[0].Message, "4万") {
		t.Fatalf("超 2 倍上限未被拦下或提示不含上限值: %s", issueFields(issues))
	}
}

func TestParsePublishSpecRejectsMaxNotAboveMin(t *testing.T) {
	raw := strings.Replace(validPublishParams, `"最高月薪": "4万"`, `"最高月薪": "2万"`, 1)
	_, issues := ParsePublishSpec(raw)
	if len(issues) != 1 || issues[0].Field != "最高月薪" ||
		!strings.Contains(issues[0].Message, "必须高于") {
		t.Fatalf("最高不高于最低未被拦下: %s", issueFields(issues))
	}
}

func TestParsePublishSpecAllowsLowestTierWithoutRatioCheck(t *testing.T) {
	// “1千以下”没有确定数值，不参与倍数比较，但档位本身仍须合法。
	raw := strings.Replace(validPublishParams, `"最低月薪": "2万"`, `"最低月薪": "1千以下"`, 1)
	if _, issues := ParsePublishSpec(raw); len(issues) != 0 {
		t.Fatalf("最低档位不应触发倍数校验: %s", issueFields(issues))
	}
}

func TestParsePublishSpecRejectsValuesOutsidePlatformOptions(t *testing.T) {
	cases := map[string]struct{ from, to, field string }{
		"学历写法不在选项内": {`"最低学历": "大专"`, `"最低学历": "大学本科"`, "最低学历"},
		"经验写法不在选项内": {`"工作经验": "3-5年"`, `"工作经验": "3到5年"`, "工作经验"},
		"月数写法不在选项内": {`"薪资月数": "12个月"`, `"薪资月数": "12"`, "薪资月数"},
		"工作性质不在选项内": {`"工作性质": "社招全职"`, `"工作性质": "全职"`, "工作性质"},
		"薪资档位不存在":   {`"最低月薪": "2万"`, `"最低月薪": "2.05万"`, "最低月薪"},
	}
	for name, tc := range cases {
		raw := strings.Replace(validPublishParams, tc.from, tc.to, 1)
		_, issues := ParsePublishSpec(raw)
		found := false
		for _, issue := range issues {
			if issue.Field == tc.field {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s 未被拦下: %s", name, issueFields(issues))
		}
	}
}

// 关键词 2026-07-31 起是死字段，预检一概不再看它。原来"缺少/重复/超过 11 个"
// 会判 blocked，现在这些职位都会变成可发——这是裁决的直接后果，本用例把它钉住，
// 免得日后有人以为是漏检又把校验加回来。
func TestParsePublishSpecIgnoresBackendKeywordsEntirely(t *testing.T) {
	for name, replacement := range map[string]string{
		"超总配额": `"职位关键词": ["a","b","c","d","e","f","g","h","i","j","k","l"]`,
		"重复":   `"职位关键词": ["银行", "银行"]`,
		"有空词":  `"职位关键词": ["银行", "  "]`,
		"整个缺失": `"职位关键词": []`,
	} {
		raw := strings.Replace(validPublishParams,
			`"职位关键词": ["个人客户", "银行", "基金", "投资与资产管理"]`, replacement, 1)
		if _, issues := ParsePublishSpec(raw); len(issues) != 0 {
			t.Fatalf("%s 不应再产生预检问题: %s", name, issueFields(issues))
		}
	}
}

func TestParsePublishSpecRequiresDescriptionAndHeadcount(t *testing.T) {
	raw := strings.Replace(validPublishParams, `"职位描述": "【关于团队】\n这是某团队的财富传承顾问岗。"`, `"职位描述": "  "`, 1)
	raw = strings.Replace(raw, `"招聘人数": 1`, `"招聘人数": 0`, 1)
	_, issues := ParsePublishSpec(raw)
	fields := map[string]bool{}
	for _, issue := range issues {
		fields[issue.Field] = true
	}
	if !fields["职位描述"] || !fields["招聘人数"] {
		t.Fatalf("必填缺失未被拦下: %s", issueFields(issues))
	}
}

func TestParsePublishSpecRejectsEmptyAndMalformed(t *testing.T) {
	for name, raw := range map[string]string{"空文档": "   ", "非 JSON": "{不是 json"} {
		_, issues := ParsePublishSpec(raw)
		if len(issues) != 1 {
			t.Fatalf("%s 应恰好产生一条整体问题: %s", name, issueFields(issues))
		}
	}
}

func TestDeadFieldNoticesAlwaysSurfaceAndNeverBlock(t *testing.T) {
	spec, issues := ParsePublishSpec(validPublishParams)
	if len(issues) != 0 {
		t.Fatalf("夹具不应有问题: %s", issueFields(issues))
	}
	// 与 job.name 一致时也要提示，否则运营不知道这一行根本没被读。
	same := spec.DeadFieldNotices("财富传承顾问")
	if len(same) != 3 {
		t.Fatalf("死字段提示应有三条: %s", issueFields(same))
	}
	if !strings.Contains(same[0].Message, "两者一致") {
		t.Fatalf("一致情形提示错误: %s", issueFields(same))
	}
	// 不一致时必须写明真正会被发布的名字。
	diff := spec.DeadFieldNotices("财富传承顾问(法律/财务/税务背景优先)")
	if !strings.Contains(diff[0].Message, "财富传承顾问(法律/财务/税务背景优先)") {
		t.Fatalf("不一致情形未写明实际发布名: %s", issueFields(diff))
	}
	// 类别与关键词 2026-07-31 起都是死字段：一律由大模型看着平台当次给出的
	// 候选/词库选定。提示必须说清"不参与发布"，否则运营会以为自己填的生效了。
	if diff[1].Field != DeadFieldJobClass || !strings.Contains(diff[1].Message, "不参与发布") {
		t.Fatalf("职位类别提示错误: %s", issueFields(diff))
	}
	if diff[2].Field != DeadFieldKeywords || !strings.Contains(diff[2].Message, "不参与发布") {
		t.Fatalf("职位关键词提示错误: %s", issueFields(diff))
	}
}

func TestDraftArgsTakesJobNameFromCallerAndDropsDeadFields(t *testing.T) {
	spec, issues := ParsePublishSpec(validPublishParams)
	if len(issues) != 0 {
		t.Fatalf("夹具不应有问题: %s", issueFields(issues))
	}
	// 夹具里发布参数的职位名称是"财富传承顾问"，这里刻意传一个不同的后台职位名：
	// 按裁决必须发后者，前者是死字段。
	args := spec.DraftArgs(
		"财富传承顾问(法律/财务/税务背景优先)", "理财顾问",
		[]string{"法律", "税务", "会计"},
	)

	if args["jobName"] != "财富传承顾问(法律/财务/税务背景优先)" {
		t.Fatalf("职位名未取调用方传入的 job.name: %v", args["jobName"])
	}
	// 职位类别必须取调用方传入的平台候选原文,绝不能取发布参数里那个值——
	// 后者未必在平台候选里(真机两例都不在)。
	if args["jobClass"] != "理财顾问" {
		t.Fatalf("职位类别未取调用方传入的平台候选原文: %v", args["jobClass"])
	}
	// 死字段一旦漏进入参，手就会拿它去填表——职位名漂移会让候选人配置错配。
	if _, leaked := args["职位名称"]; leaked {
		t.Fatal("发布参数里的职位名称不得进入试填参数")
	}
	for _, key := range []string{"职位类别", "category", "职位关键词"} {
		if _, leaked := args[key]; leaked {
			t.Fatalf("死字段不得进入试填参数: %s", key)
		}
	}

	want := map[string]any{
		"employmentType": "社招全职",
		"education":      "大专",
		"experience":     "3-5年",
		"salaryMin":      "2万",
		"salaryMax":      "4万",
		"salaryMonths":   "12个月",
		"headcount":      int64(1),
		"showToSeeker":   false,
		"syncToMailbox":  false,
	}
	for key, expected := range want {
		if args[key] != expected {
			t.Fatalf("%s 映射错误: 期望 %v 实得 %v", key, expected, args[key])
		}
	}
	// 关键词必须取调用方传入的那几个（大模型看着平台当次词库选、且经确定性
	// 复核）。发布参数里的 4 个是死字段，一个都不能漏进来。
	keywords, ok := args["keywords"].([]string)
	if !ok || strings.Join(keywords, ",") != "法律,税务,会计" {
		t.Fatalf("关键词未取调用方传入的选定结果: %v", args["keywords"])
	}
	description, ok := args["description"].(string)
	if !ok || !strings.Contains(description, "【关于团队】") {
		t.Fatalf("职位描述未原样传递: %q", description)
	}
	// 手侧契约的 args 只认这些键；多一个都会被 ValidatePrimitiveArgs 拒。
	if len(args) != 13 {
		t.Fatalf("试填参数键数与契约不符: %d 个 %v", len(args), args)
	}
}

func TestDraftArgsTrimsCallerJobName(t *testing.T) {
	spec, _ := ParsePublishSpec(validPublishParams)
	args := spec.DraftArgs("  财富传承顾问  ", "  理财顾问  ", []string{" 法律 ", "", "税务"})
	if got := args["jobName"]; got != "财富传承顾问" {
		t.Fatalf("职位名未去除首尾空白: %q", got)
	}
	// 关键词同样去空白，空词整项丢弃：手侧按全等去点词条，带空白点不中。
	if got := strings.Join(args["keywords"].([]string), ","); got != "法律,税务" {
		t.Fatalf("关键词未去空白或未丢弃空词: %q", got)
	}
}

func TestSalaryTiersMatchLivePlatformEnumeration(t *testing.T) {
	// 2026-07-29 真机读到的下拉共 59 档。这条断言锁住枚举面：档位不是连续值，
	// 放宽成"能被 1000 整除"会放过 3.1万 这种并不存在的档位。
	if len(salaryTiers) != 59 {
		t.Fatalf("薪资档位数与真机不符: %d", len(salaryTiers))
	}
	for _, present := range []string{"1千以下", "1千", "9千", "1万", "1.1万", "2.9万", "3万", "3.5万", "9.5万", "10万", "24万"} {
		if _, ok := parseSalaryTier(present); !ok {
			t.Fatalf("真机已见档位缺失: %s", present)
		}
	}
	// 步长之间的空档必须落空，否则预检会放行一个填不进去的值。
	for _, absent := range []string{"3.1万", "9.6万", "10.5万", "25万", "0.5千", "2.05万", "12个月"} {
		if _, ok := parseSalaryTier(absent); ok {
			t.Fatalf("不存在的档位被接受: %s", absent)
		}
	}
	if value, _ := parseSalaryTier("1千以下"); value != 0 {
		t.Fatal("“1千以下”应为无确定数值的合法档位")
	}
}

func TestMatchesExistingPostingFoldsBracketsAndSpaces(t *testing.T) {
	postings := []string{"大客户经理（养老&财富传承）", "财富传承顾问", "家庭资产配置顾问(房地产背景优先)"}
	// 后台职位名全半角括号混用，严格相等会把"已存在"判成"可发"，即多发。
	for _, name := range []string{
		"大客户经理(养老&财富传承)",
		"大客户经理（养老&财富传承）",
		"财富传承顾问",
		"家庭资产配置顾问（房地产背景优先）",
		" 财富传承顾问 ",
	} {
		if !MatchesExistingPosting(name, postings) {
			t.Fatalf("同名职位未被识别为已存在: %q", name)
		}
	}
	for _, name := range []string{
		"财富传承顾问(法律/财务/税务背景优先)",
		"储备总监(管理经验优先)",
		"",
	} {
		if MatchesExistingPosting(name, postings) {
			t.Fatalf("不同职位被误判为已存在: %q", name)
		}
	}
}

// 代招公司（2026-08-22 甲方裁决）：选填键，只去首尾空白、不校验、不产生 issue；
// 配置了才进入 args，没配置不带键（键数与契约旧形态一致，手侧按最后一家）。
func TestParsePublishSpecReadsOptionalPartnerCompany(t *testing.T) {
	raw := strings.Replace(validPublishParams, `"招聘人数": 1,`, `"招聘人数": 1,
  "代招公司": "  桃子科技有限公司  ",`, 1)
	spec, issues := ParsePublishSpec(raw)
	if len(issues) != 0 {
		t.Fatalf("代招公司不应产生任何预检问题: %s", issueFields(issues))
	}
	if spec.PartnerCompany != "桃子科技有限公司" {
		t.Fatalf("代招公司未去首尾空白解析: %q", spec.PartnerCompany)
	}
	args := spec.DraftArgs("财富传承顾问", "理财顾问", []string{"法律"})
	if args["partnerCompany"] != "桃子科技有限公司" {
		t.Fatalf("代招公司未进入试填参数: %v", args["partnerCompany"])
	}
	if len(args) != 14 {
		t.Fatalf("配置代招公司后试填参数键数应为 14: %d 个 %v", len(args), args)
	}
	if notice := spec.PartnerCompanyNotice(); notice == nil || notice.Field != "代招公司" ||
		!strings.Contains(notice.Message, "桃子科技有限公司") {
		t.Fatalf("配置了代招公司预检应给出提示: %+v", notice)
	}

	// 没配置 / 空白：视同未配置，不带键、不提示、不报错。
	for _, variant := range []string{
		validPublishParams,
		strings.Replace(validPublishParams, `"招聘人数": 1,`, `"招聘人数": 1,
  "代招公司": "   ",`, 1),
	} {
		spec, issues := ParsePublishSpec(variant)
		if len(issues) != 0 {
			t.Fatalf("未配置代招公司不应有预检问题: %s", issueFields(issues))
		}
		if spec.PartnerCompany != "" {
			t.Fatalf("空白代招公司应视同未配置: %q", spec.PartnerCompany)
		}
		args := spec.DraftArgs("财富传承顾问", "理财顾问", []string{"法律"})
		if _, present := args["partnerCompany"]; present {
			t.Fatalf("未配置时不得带 partnerCompany 键: %v", args)
		}
		if len(args) != 13 {
			t.Fatalf("未配置时试填参数键数应为 13: %d", len(args))
		}
		if spec.PartnerCompanyNotice() != nil {
			t.Fatal("未配置代招公司不应有预检提示")
		}
	}
}

// 结果提示：2026-08-23 起手侧只认逐字相等，成功回来的名字恒等于配置；该段没
// 走到（账号没有代招公司一栏）时单独说明；"未配置却选中了"与"选中的与配置不
// 一致"按理到不了，留作手脑版本错位的显眼提示，不得静默。
func TestPartnerCompanyHintMirrorsHandMatching(t *testing.T) {
	ptr := func(value string) *string { return &value }
	cases := []struct {
		name       string
		configured string
		actual     *string
		want       string
	}{
		{"未配置且未涉及", "", nil, ""},
		{"配置了但账号没有这一栏", "桃子科技有限公司", nil, "该账号的发布表单没有代招公司一栏，配置的代招公司「桃子科技有限公司」未使用"},
		{"逐字命中", "桃子科技有限公司", ptr(" 桃子科技有限公司 "), "代招公司已按配置选中「桃子科技有限公司」"},
		{"未配置却选中了（旧手）", "", ptr("最后一家有限公司"), "后台未配置代招公司，但手侧选中了「最后一家有限公司」——当前版本应以未配置失败，请确认插件已随客户端一并更新"},
		{"选中的与配置不一致（旧手）", "阿狸与桃子", ptr("阿狸与桃子（上海）信息咨询有限公司"), "手侧选中的代招公司「阿狸与桃子（上海）信息咨询有限公司」与配置「阿狸与桃子」不一致——当前版本只认逐字相等，请确认插件已随客户端一并更新"},
	}
	for _, tc := range cases {
		spec := PublishSpec{PartnerCompany: tc.configured}
		if got := spec.PartnerCompanyHint(tc.actual); got != tc.want {
			t.Fatalf("%s: 期望 %q 实得 %q", tc.name, tc.want, got)
		}
	}
}
