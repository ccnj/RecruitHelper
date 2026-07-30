package jobconfig

import "testing"

func TestMatchPlatformJobClassOnlyReturnsPlatformOriginalText(t *testing.T) {
	candidates := []string{"理财顾问", "财务咨询顾问", "保险顾问", "保险项目策划"}

	// 归一化只放宽匹配:全半角括号与空白折叠后相等即算命中。
	for _, configured := range []string{"理财顾问", " 理财顾问 ", "理财顾问　"} {
		matched, ok := MatchPlatformJobClass(configured, candidates)
		if !ok || matched != "理财顾问" {
			t.Fatalf("%q 应命中并返回平台原文: %q ok=%v", configured, matched, ok)
		}
	}

	// 后台配置值不在平台候选里是常态(真机两例都不在),必须如实不命中,
	// 交给大模型或人,绝不就近取一个。
	for _, configured := range []string{"", "  ", "销售总监", "财务经理", "理财"} {
		if matched, ok := MatchPlatformJobClass(configured, candidates); ok {
			t.Fatalf("%q 不应命中,却返回了 %q", configured, matched)
		}
	}

	// 归一化后撞成多个时同样不命中:两个都像的情况下替甲方猜一个是最坏的做法。
	if _, ok := MatchPlatformJobClass("理财顾问", []string{"理财顾问", "理财 顾问"}); ok {
		t.Fatal("归一化后多命中必须判不命中")
	}
}

func TestContainsPlatformJobClassIsByteExact(t *testing.T) {
	candidates := []string{"理财顾问", "财务咨询顾问"}
	if !ContainsPlatformJobClass("理财顾问", candidates) {
		t.Fatal("逐字相同应在场")
	}
	// 这是发布前最后一道确定性闸:差一个字、多一个空格都必须判不在场,
	// 否则手会去选择器里找一个不存在的行,或者更糟——选错行。
	for _, chosen := range []string{"理财 顾问", "理财顾问 ", "理财顾问（推荐）", "顾问"} {
		if ContainsPlatformJobClass(chosen, candidates) {
			t.Fatalf("%q 不应判为在场", chosen)
		}
	}
}
