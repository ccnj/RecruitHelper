package m5ai

import (
	"strings"
	"testing"
)

func twoJobBatch() []JobClassJobInput {
	return []JobClassJobInput{
		{
			JobID:       "16",
			JobName:     "家庭财务规划师(HR/培训/市场背景优先)",
			Description: "岗位职责：为家庭客户提供财务规划与资产配置建议。",
			Candidates: []JobClassCandidateInput{
				{Name: "理财顾问", Definition: "负责评估客户财务状况、配置金融产品的专业人员"},
				{Name: "保险顾问", Definition: "从事保险业务代理与保险产品销售的专业人员"},
			},
		},
		{
			JobID:       "17",
			JobName:     "财富传承顾问(法律/财务/税务背景优先)",
			Description: "岗位职责：为高净值家庭提供财富传承方案。",
			Candidates: []JobClassCandidateInput{
				{Name: "理财顾问", Definition: "负责评估客户财务状况、配置金融产品的专业人员"},
				{Name: "财务咨询顾问", Definition: "帮助企业分析财务现状、提出优化方案的专业人员"},
			},
		},
	}
}

func TestRenderJobClassPromptCarriesEveryJobWithItsOwnCandidates(t *testing.T) {
	content, err := RenderJobClassPrompt(twoJobBatch(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		// 每个职位的编号、名称与它自己的候选都要在，候选清单不能合并成一张全局表。
		"职位编号：16",
		"家庭财务规划师(HR/培训/市场背景优先)",
		"1. 理财顾问 —— 负责评估客户财务状况、配置金融产品的专业人员",
		"2. 保险顾问 —— 从事保险业务代理与保险产品销售的专业人员",
		"职位编号：17",
		"2. 财务咨询顾问 —— 帮助企业分析财务现状、提出优化方案的专业人员",
		// 平台官方释义是判断贴合度的唯一依据，漏掉它等于让模型凭类别名字面猜。
		"只能从**它自己的**候选清单里选",
		"尽量让这批职位落到互不相同的类别上",
		"不要选保险相关的类别",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("提示词缺少 %q", want)
		}
	}
	// 整批分配时没有"已占用"，那一段不该出现。
	if strings.Contains(content, "已经占用了这些类别") {
		t.Fatal("整批分配不应带已占用清单")
	}

	// 单职位重跑：把其余职位已定的类别传进来，模型才会主动避开。
	single, err := RenderJobClassPrompt(twoJobBatch()[:1], []string{"财务咨询顾问", "理财顾问"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(single, "已经占用了这些类别，请尽量避开：财务咨询顾问、理财顾问") {
		t.Fatal("单职位重跑未把已占用类别写进提示词")
	}

	for name, jobs := range map[string][]JobClassJobInput{
		"空批次": nil,
		"缺候选": {{JobID: "16", JobName: "岗位", Description: "描述"}},
		"缺职位名": {{
			JobID: "16", Description: "描述",
			Candidates: []JobClassCandidateInput{{Name: "理财顾问"}},
		}},
		"缺编号": {{
			JobName: "岗位", Description: "描述",
			Candidates: []JobClassCandidateInput{{Name: "理财顾问"}},
		}},
		// 编号重复时模型的回答无从对应回职位，宁可不发这次请求。
		"编号重复": {
			{JobID: "16", JobName: "甲", Description: "描述",
				Candidates: []JobClassCandidateInput{{Name: "理财顾问"}}},
			{JobID: "16", JobName: "乙", Description: "描述",
				Candidates: []JobClassCandidateInput{{Name: "理财顾问"}}},
		},
	} {
		if _, err := RenderJobClassPrompt(jobs, nil); err == nil {
			t.Fatalf("%s 必须拒绝构造请求", name)
		}
	}
}

func TestParseJobClassAssignmentsRejectsUnusableAnswers(t *testing.T) {
	good, err := ParseJobClassAssignments(
		`{"分配":[{"职位":"16","类别":"理财顾问","置信度":0.82,"理由":"最贴合"},` +
			`{"职位":17,"类别":"财务咨询顾问","置信度":0.6}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(good) != 2 || good[0].JobID != "16" || good[0].Class != "理财顾问" ||
		good[0].Confidence != 0.82 || good[0].Reason != "最贴合" {
		t.Fatalf("第一条解析错误: %+v", good[0])
	}
	// 职位编号写成 JSON 数字是形态差异不是内容错误，认下来比多跑两次重试划算。
	if good[1].JobID != "17" || good[1].Class != "财务咨询顾问" {
		t.Fatalf("数字形态的职位编号未认下来: %+v", good[1])
	}
	// 理由缺席不算失败：它只给人读，不参与任何判定。
	if good[1].Reason != "" {
		t.Fatalf("缺理由应当接受: %+v", good[1])
	}

	// 英文键也接受：模型偶尔会照 JSON 惯例回英文。
	if alias, err := ParseJobClassAssignments(
		`{"assignments":[{"jobId":"16","class":"保险顾问","confidence":0.2}]}`,
	); err != nil || len(alias) != 1 || alias[0].Class != "保险顾问" {
		t.Fatalf("英文键返回解析错误: %+v err=%v", alias, err)
	}

	for name, raw := range map[string]string{
		"不是 JSON":     "理财顾问",
		"缺分配":         `{"理由":"忘了"}`,
		"分配不是数组":      `{"分配":"理财顾问"}`,
		"分配为空":        `{"分配":[]}`,
		"缺职位编号":       `{"分配":[{"类别":"理财顾问","置信度":0.8}]}`,
		"缺类别":         `{"分配":[{"职位":"16","置信度":0.8}]}`,
		"类别为空":        `{"分配":[{"职位":"16","类别":"  ","置信度":0.8}]}`,
		"缺置信度":        `{"分配":[{"职位":"16","类别":"理财顾问"}]}`,
		"置信度越界":       `{"分配":[{"职位":"16","类别":"理财顾问","置信度":1.4}]}`,
		"置信度不是数":      `{"分配":[{"职位":"16","类别":"理财顾问","置信度":"高"}]}`,
		"中英文键同时出现":    `{"分配":[{"职位":"16","类别":"理财顾问","class":"保险顾问","置信度":0.8}]}`,
		"置信度中英文键同时出现": `{"分配":[{"职位":"16","类别":"理财顾问","置信度":0.8,"confidence":0.2}]}`,
	} {
		if _, err := ParseJobClassAssignments(raw); err == nil {
			t.Fatalf("%s 必须拒绝: %s", name, raw)
		}
	}
}

func TestClassifyJobClassAssignmentsKeepsGoodOnesAndNamesTheProblems(t *testing.T) {
	jobs := twoJobBatch()

	accepted, problems := ClassifyJobClassAssignments([]JobClassAssignment{
		{JobID: "16", Class: "理财顾问", Confidence: 0.8},
		{JobID: "17", Class: "财务咨询顾问", Confidence: 0.7},
	}, jobs)
	if len(problems) != 0 || len(accepted) != 2 || accepted["16"].Class != "理财顾问" {
		t.Fatalf("全合法的分配不该有问题: accepted=%v problems=%v", accepted, problems)
	}

	// 一个坏职位不该废掉整批：合法的那个必须留下来。
	partial, problems := ClassifyJobClassAssignments([]JobClassAssignment{
		{JobID: "16", Class: "理财顾问", Confidence: 0.8},
		{JobID: "17", Class: "储备经理人", Confidence: 0.9},
	}, jobs)
	if len(partial) != 1 || partial["16"].Class != "理财顾问" {
		t.Fatalf("合法的那个应当保留: %v", partial)
	}
	if problems["17"] != JobClassProblemNotInCandidates {
		t.Fatalf("不在候选里的应判 notInCandidates: %v", problems)
	}

	// 逐字相等，绝不模糊匹配：差一个字手就点不中那一行。
	if _, problems := ClassifyJobClassAssignments([]JobClassAssignment{
		{JobID: "16", Class: "理财 顾问", Confidence: 0.8},
	}, jobs[:1]); problems["16"] != JobClassProblemNotInCandidates {
		t.Fatalf("带空格的类别名必须判不命中: %v", problems)
	}

	// 拿别的职位的候选来用也不行——候选是按职位现给的。
	if _, problems := ClassifyJobClassAssignments([]JobClassAssignment{
		{JobID: "16", Class: "财务咨询顾问", Confidence: 0.8},
	}, jobs); problems["16"] != JobClassProblemNotInCandidates {
		t.Fatalf("串用别的职位候选必须判不命中: %v", problems)
	}

	// 漏掉的职位要点名，否则调用方不知道该重试还是该跳过。
	if _, problems := ClassifyJobClassAssignments([]JobClassAssignment{
		{JobID: "16", Class: "理财顾问", Confidence: 0.8},
	}, jobs); problems["17"] != JobClassProblemMissing {
		t.Fatalf("漏掉的职位应判 missing: %v", problems)
	}

	// 同一个职位给两次：两个都不能用，挑哪个都是替甲方猜。
	dup, problems := ClassifyJobClassAssignments([]JobClassAssignment{
		{JobID: "16", Class: "理财顾问", Confidence: 0.8},
		{JobID: "16", Class: "保险顾问", Confidence: 0.9},
	}, jobs[:1])
	if len(dup) != 0 || problems["16"] != JobClassProblemDuplicate {
		t.Fatalf("重复职位应整条作废: accepted=%v problems=%v", dup, problems)
	}

	// 模型编出来的职位编号只记诊断，不影响真实职位。
	if _, problems := ClassifyJobClassAssignments([]JobClassAssignment{
		{JobID: "16", Class: "理财顾问", Confidence: 0.8},
		{JobID: "999", Class: "理财顾问", Confidence: 0.8},
	}, jobs[:1]); problems["999"] != JobClassProblemUnknownJob {
		t.Fatalf("不在本批的职位编号应判 unknownJob: %v", problems)
	}
}

func TestJobClassCollisionsOnlyReportsSharedClasses(t *testing.T) {
	// 差异化是目标不是闸：撞车照常放行，但必须让运营在确认清单上看见。
	collisions := JobClassCollisions(map[string]string{
		"16": "理财顾问", "17": "理财顾问", "18": "财务咨询顾问", "19": "理财顾问",
	})
	if len(collisions) != 1 {
		t.Fatalf("只该报一个撞车类别: %v", collisions)
	}
	if strings.Join(collisions["理财顾问"], ",") != "16,17,19" {
		t.Fatalf("撞车职位应按编号稳定排序: %v", collisions["理财顾问"])
	}
	if len(JobClassCollisions(map[string]string{"16": "理财顾问", "17": "财务咨询顾问"})) != 0 {
		t.Fatal("全不相同时不该报撞车")
	}
}

func TestPurposeInputTokenLimitOverridesOnlyTheTwoNamedPurposes(t *testing.T) {
	const configured = ReplyInputTokenLimit

	// 全批分配要带全部职位的完整描述，按用途放宽（甲方 2026-08-01 裁决）。
	if got := purposeInputTokenLimit(PurposeJobClass, configured); got != JobClassInputTokenLimit {
		t.Fatalf("jobClass 应放宽到 %d，实得 %d", JobClassInputTokenLimit, got)
	}
	// 意图判断只看会话尾部，照旧收窄。
	if got := purposeInputTokenLimit(PurposeIntent, configured); got != IntentInputTokenLimit {
		t.Fatalf("intent 应收窄到 %d，实得 %d", IntentInputTokenLimit, got)
	}
	// 别的用途一个都不受影响——放宽只能是点名的，不能顺手全放开。
	for _, purpose := range []CompletionPurpose{
		PurposeReply, PurposeServiceReply, PurposeSilenceFollowup,
		PurposeScoring, PurposeGreeting, PurposeJobKeywords,
	} {
		if got := purposeInputTokenLimit(purpose, configured); got != configured {
			t.Fatalf("%s 不该被覆盖，实得 %d", purpose, got)
		}
	}
	// 覆盖只在一个方向上生效：配置本来就更宽时不把它收回去。
	if got := purposeInputTokenLimit(PurposeJobClass, JobClassInputTokenLimit*2); got != JobClassInputTokenLimit*2 {
		t.Fatalf("配置更宽时不该被覆盖收窄，实得 %d", got)
	}
	if got := purposeInputTokenLimit(PurposeIntent, IntentInputTokenLimit/2); got != IntentInputTokenLimit/2 {
		t.Fatalf("配置更窄时不该被覆盖放宽，实得 %d", got)
	}
}
