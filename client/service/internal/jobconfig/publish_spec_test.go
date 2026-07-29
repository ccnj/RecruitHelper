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
	if len(spec.Keywords) != 4 || spec.Headcount != 1 || spec.Workplace != "默认" {
		t.Fatalf("其余字段解析错误: %+v", spec)
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

func TestParsePublishSpecRejectsKeywordsOverQuota(t *testing.T) {
	raw := strings.Replace(validPublishParams,
		`"职位关键词": ["个人客户", "银行", "基金", "投资与资产管理"]`,
		`"职位关键词": ["a","b","c","d","e","f","g","h","i","j","k","l"]`, 1)
	_, issues := ParsePublishSpec(raw)
	if len(issues) != 1 || issues[0].Field != "职位关键词" ||
		!strings.Contains(issues[0].Message, "11") {
		t.Fatalf("关键词超总配额未被拦下: %s", issueFields(issues))
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
	if len(same) != 2 {
		t.Fatalf("死字段提示应有两条: %s", issueFields(same))
	}
	if !strings.Contains(same[0].Message, "两者一致") {
		t.Fatalf("一致情形提示错误: %s", issueFields(same))
	}
	// 不一致时必须写明真正会被发布的名字。
	diff := spec.DeadFieldNotices("财富传承顾问(法律/财务/税务背景优先)")
	if !strings.Contains(diff[0].Message, "财富传承顾问(法律/财务/税务背景优先)") {
		t.Fatalf("不一致情形未写明实际发布名: %s", issueFields(diff))
	}
	if diff[1].Field != DeadFieldJobClass || !strings.Contains(diff[1].Message, "自动判定") {
		t.Fatalf("职位类别提示错误: %s", issueFields(diff))
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
