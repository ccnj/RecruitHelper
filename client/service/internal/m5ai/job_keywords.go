package m5ai

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// JobKeywordsInputFormatVersion 是本提示词形态的修订号。改动提示词文本或输入
// 结构时必须同时改它——否则 ai-traces 里两种形态的调用无从区分。
//
// v2 = 2026-08-01 增补"避开保险"那条规则。
const JobKeywordsInputFormatVersion = "jobKeywords/2"

// 甲方 2026-07-31 裁决的两个硬数字:总数 3-5,其中词库外的自定义至多 2 个。
// 兜底组的硬上限是 3,留一格余量,免得平台那边刚好卡满。
const (
	JobKeywordsMin       = 3
	JobKeywordsMax       = 5
	JobKeywordsMaxCustom = 2
)

// JobKeywordSectionInput 是给模型看的一个分组:标题、组内上限与组内词条。
//
// 分组必须原样传给模型,不能拍平成一张词表:组内上限是 3,模型看不见分组就会
// 把词堆在一组里,超出的部分会被平台拒掉(手侧只能如实记 dropped)。
type JobKeywordSectionInput struct {
	Title string
	// Limit 为 0 表示这个组件变体没给出上限,不是"上限为 0"。
	Limit int
	Words []string
}

// JobKeywordsSuggestion 是模型的建议。它只是建议:每个词都要由确定性代码回到
// 词库里逐字核对,并复核总数、重复、自定义数量与组内配额。
type JobKeywordsSuggestion struct {
	Keywords []string
	Reason   string
}

// JobKeywordsPlan 是确定性复核之后的落点:哪些词点选、哪些词走兜底组自定义。
// 顺序沿用模型给的顺序,手侧按这个顺序逐项填。
type JobKeywordsPlan struct {
	Keywords []string
	Matched  []string
	Custom   []string
}

// jobKeywordsInstruction 是甲方 2026-07-31 审定、2026-08-01 增补避开保险的原文。
//
// 规则 2 的斜杠条款来自真机:词库里存在 `财务/审计/税务`、`证券/期货` 这类
// 合并词条,拆开选会让职位顺带打上另外两个标签,超出运营本意(docs §1.4 与
// §2.4 第 4 条)。
//
// 规则 3 与类别提示词那条同源:这些岗位实际做保险,但招的是**转行**的人。关键词
// 决定平台把职位匹配给谁,打上保险标签就等于去保险业内捞人,与招聘意图相反。
// 它和类别那条一样**只是偏好、不是闸**——确定性复核不按词面拦保险,因为词库里
// 没有"全是保险"的死局(总能从别的分组选或自己写),而做成硬闸会在某个职位的
// 词库确实偏保险时把整条链卡住。规则 7 末句就是为这种情形留的出口。
const jobKeywordsInstruction = `你在为一个招聘职位挑选招聘平台上的「职位关键词」。关键词决定平台把这个职位匹配给哪些求职者，也决定求职者搜索时能不能找到它。

我会给你四样东西：职位名称、职位描述原文、已经选定的职位类别，以及平台在这个类别下给出的关键词词库（按分组列出，每组标明该组最多能选几个）。

规则，按优先级：

1. 一共只能选 3 到 5 个关键词，不能多也不能少。
2. 优先从词库里选。词库里的词必须原样返回，一个字都不能改——包括「财务/审计/税务」这种用斜杠合并的词条：要选就整条选，绝不能拆成「税务」单独返回。
3. 不要选带「保险」字样的词，也不要选以保险从业为核心的词（例如「保险」「保险销售」「保险经纪」「保险代理」）。这些岗位面向的是从其他行业转行而来的人；打上保险标签会把职位匹配给已在保险业内的人，与招聘意图相反，也会显著缩小候选人池。宁可从别的分组里选，或者自己写一个。
4. 词库里确实没有合适的，才可以自己写，但最多 2 个。自己写的必须是招聘者用来筛人的短词（技能、行业、背景、证书），不能是句子或短语，也同样不能带「保险」字样。
5. 同一个分组里选的词，不能超过该组标明的上限。
6. 必须与职位名称和职位描述所述的实际工作内容相符。不要选描述里找不到依据的词，宁可少选一个也不要硬凑。
7. 必须做出选择。不允许弃选，不允许返回空数组，不允许返回「无法判断」。即便这个职位的词库大半跟保险有关，也要按上面的优先级挑出 3 到 5 个来。

只输出 JSON，不要任何额外文字、不要代码块围栏：
{"关键词":["<第一个>","<第二个>","<第三个>"],"理由":"<一到两句：为什么是这几个>"}`

// RenderJobKeywordsPrompt 拼出完整请求正文。按数据边界,这里只放职位上下文:
// 职位名、描述原文、选定类别与平台词库;不带任何候选人身份字段。
func RenderJobKeywordsPrompt(
	jobName string,
	description string,
	jobClass string,
	sections []JobKeywordSectionInput,
) (string, error) {
	jobName = strings.TrimSpace(jobName)
	description = strings.TrimSpace(description)
	jobClass = strings.TrimSpace(jobClass)
	if jobName == "" || description == "" {
		return "", errors.New("职位名称与职位描述都不能为空")
	}
	if jobClass == "" {
		return "", errors.New("职位类别不能为空")
	}
	if len(sections) == 0 {
		return "", errors.New("关键词词库为空")
	}
	var builder strings.Builder
	builder.WriteString(jobKeywordsInstruction)
	builder.WriteString("\n\n---\n\n职位名称：")
	builder.WriteString(jobName)
	builder.WriteString("\n\n职位类别（已选定）：")
	builder.WriteString(jobClass)
	builder.WriteString("\n\n职位描述：\n")
	builder.WriteString(description)
	builder.WriteString("\n\n平台关键词词库：\n")
	for _, section := range sections {
		title := strings.TrimSpace(section.Title)
		if title == "" {
			return "", errors.New("分组标题不能为空")
		}
		builder.WriteString("- ")
		builder.WriteString(title)
		if section.Limit > 0 {
			builder.WriteString("（本组最多选 ")
			builder.WriteString(strconv.Itoa(section.Limit))
			builder.WriteString(" 个）")
		}
		builder.WriteString("：")
		if len(section.Words) == 0 {
			// 兜底组没有预设词条。明说它是"只能自己写"的那一组,免得模型
			// 以为这里漏了数据而去自造词条名。
			builder.WriteString("（本组没有现成词条，只能自己写）")
		} else {
			builder.WriteString(strings.Join(section.Words, "、"))
		}
		builder.WriteString("\n")
	}
	return builder.String(), nil
}

// ParseJobKeywordsSuggestion 解析模型返回并做与词库无关的复核。错误串是稳定的
// 错误分类标识,会进无正文诊断,所以保持英文短词、不带模型原文。
func ParseJobKeywordsSuggestion(raw string) (JobKeywordsSuggestion, error) {
	object, err := decodeUniqueObject(raw)
	if err != nil {
		return JobKeywordsSuggestion{}, err
	}

	keywordsRaw, err := uniqueField(object, "关键词", "keywords")
	if err != nil {
		return JobKeywordsSuggestion{}, errors.New(err.Error() + "Keywords")
	}
	var keywords []string
	if err := json.Unmarshal(keywordsRaw, &keywords); err != nil {
		return JobKeywordsSuggestion{}, errors.New("invalidKeywords")
	}
	trimmed := make([]string, 0, len(keywords))
	seen := make(map[string]struct{}, len(keywords))
	for _, keyword := range keywords {
		word := strings.TrimSpace(keyword)
		if word == "" {
			return JobKeywordsSuggestion{}, errors.New("emptyKeyword")
		}
		if _, duplicated := seen[word]; duplicated {
			return JobKeywordsSuggestion{}, errors.New("duplicateKeyword")
		}
		seen[word] = struct{}{}
		trimmed = append(trimmed, word)
	}
	if len(trimmed) < JobKeywordsMin || len(trimmed) > JobKeywordsMax {
		return JobKeywordsSuggestion{}, errors.New("countOutOfRange")
	}

	// 理由只给人读,缺了不算失败——它不参与任何判定。
	reason := ""
	if reasonRaw, reasonErr := uniqueField(object, "理由", "reason"); reasonErr == nil {
		var value string
		if json.Unmarshal(reasonRaw, &value) == nil {
			reason = strings.TrimSpace(value)
		}
	}
	return JobKeywordsSuggestion{Keywords: trimmed, Reason: reason}, nil
}

// PlanJobKeywords 把模型选定的词逐字放回词库核对,分出"点选"与"自定义"两个
// 落点,并复核自定义数量与组内配额。
//
// **逐字相等,绝不模糊匹配**:手侧要按全等去点中词条,差一个字就点不中;而
// "差不多的那个"点下去就是给职位打上另一个标签。词库里没有的一律归自定义,
// 由手走兜底组的「+ 自定义」——这条与手侧的填充规则是同一套口径。
func PlanJobKeywords(
	suggestion JobKeywordsSuggestion,
	sections []JobKeywordSectionInput,
) (JobKeywordsPlan, error) {
	// 词 → 它属于哪个分组。同一个词万一出现在多组里,算在第一组名下:两组都
	// 记会把配额算重,而选中它实际只占一个位置。
	owner := make(map[string]int, 32)
	for index, section := range sections {
		for _, word := range section.Words {
			trimmed := strings.TrimSpace(word)
			if trimmed == "" {
				continue
			}
			if _, exists := owner[trimmed]; !exists {
				owner[trimmed] = index
			}
		}
	}

	plan := JobKeywordsPlan{Keywords: suggestion.Keywords}
	perSection := make(map[int]int, len(sections))
	for _, keyword := range suggestion.Keywords {
		index, matched := owner[keyword]
		if !matched {
			plan.Custom = append(plan.Custom, keyword)
			continue
		}
		plan.Matched = append(plan.Matched, keyword)
		perSection[index]++
	}
	if len(plan.Custom) > JobKeywordsMaxCustom {
		return JobKeywordsPlan{}, errors.New("tooManyCustom")
	}
	for index, count := range perSection {
		// Limit 为 0 表示这个组件变体没给出上限,不是"上限为 0"——没有上限
		// 就没什么可复核的,交给平台自己拒。
		if limit := sections[index].Limit; limit > 0 && count > limit {
			return JobKeywordsPlan{}, errors.New("sectionOverflow")
		}
	}
	return plan, nil
}
