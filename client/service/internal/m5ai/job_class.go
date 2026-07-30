package m5ai

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// JobClassInputFormatVersion 是本提示词形态的修订号。改动提示词文本或输入结构
// 时必须同时改它——否则 ai-traces 里两种形态的调用无从区分。
const JobClassInputFormatVersion = "jobClass/1"

// JobClassCandidateInput 是给模型看的一个候选:类别名与平台的官方释义。
// 释义是判断贴合度的主要依据,不截断成半句。
type JobClassCandidateInput struct {
	Name       string
	Definition string
}

// JobClassSuggestion 是模型的建议。它只是建议:类别名必须由确定性代码回到候选
// 清单里逐字核对,核不上一律拒绝,绝不模糊匹配。
type JobClassSuggestion struct {
	Class      string
	Confidence float64
	Reason     string
}

// jobClassInstruction 是甲方 2026-07-30 审定的提示词原文。
//
// 规则 2 的由来:这些岗位实际是做保险的,但招聘策略正是招**转行**的人(职位名里
// 就写着「HR/培训/市场背景优先」)。类别落进保险桶,平台会把职位推给已在保险业内
// 的人,与招聘意图相反且池子更窄。它由规则 3 兜住诚实性——必须仍与职位实际内容
// 相符,不是"随便找个不像保险的"。
const jobClassInstruction = `你在为一个招聘职位挑选招聘平台上的「职位类别」。类别决定平台把这个职位推送给哪一类求职者，选错会让职位触达错误的人群。

我会给你三样东西：职位名称、职位描述原文、平台针对这个职位给出的候选类别清单（每项含类别名与平台的官方释义）。

规则，按优先级：

1. 只能从候选清单里选。必须原样返回其中一个类别名，一个字都不能改，不得自造、不得合并、不得改写。
2. 不要选保险相关的类别（以保险从业为核心定义的，例如「保险顾问」「保险项目策划」）。这个岗位面向的是从其他行业转行而来的人；落进保险类会把职位推送给已在保险业内的人，与招聘意图相反，也会显著缩小候选人池。
3. 在剩下的候选里，选与职位名称和职位描述所述实际工作内容最贴合的一项。贴合度以平台给出的官方释义为准，不要凭类别名字面猜。
4. 必须做出选择。不允许弃选，不允许返回「无法判断」。若几项都不够理想，就选相对最贴合的一项，并把把握程度如实反映在置信度里。
5. 若候选清单里所有选项都是保险相关，仍必须返回其中一项，但置信度必须低于 0.3，且在理由里明确写出「候选全部为保险相关」。

只输出 JSON，不要任何额外文字、不要代码块围栏：
{"类别":"<原样照抄的候选类别名>","置信度":<0 到 1 的小数>,"理由":"<一到两句：为什么选它，为什么排除其他>"}`

// RenderJobClassPrompt 拼出完整请求正文。按数据边界,这里只放职位上下文:
// 职位名、描述原文、候选清单及其平台释义;不带任何候选人身份字段。
func RenderJobClassPrompt(
	jobName string,
	description string,
	candidates []JobClassCandidateInput,
) (string, error) {
	jobName = strings.TrimSpace(jobName)
	description = strings.TrimSpace(description)
	if jobName == "" || description == "" {
		return "", errors.New("职位名称与职位描述都不能为空")
	}
	if len(candidates) == 0 {
		return "", errors.New("候选类别清单为空")
	}
	var builder strings.Builder
	builder.WriteString(jobClassInstruction)
	builder.WriteString("\n\n---\n\n职位名称：")
	builder.WriteString(jobName)
	builder.WriteString("\n\n职位描述：\n")
	builder.WriteString(description)
	builder.WriteString("\n\n候选类别清单：\n")
	for index, candidate := range candidates {
		name := strings.TrimSpace(candidate.Name)
		if name == "" {
			return "", errors.New("候选类别名不能为空")
		}
		builder.WriteString(strconv.Itoa(index + 1))
		builder.WriteString(". ")
		builder.WriteString(name)
		if definition := strings.TrimSpace(candidate.Definition); definition != "" {
			builder.WriteString(" —— ")
			builder.WriteString(definition)
		}
		builder.WriteString("\n")
	}
	return builder.String(), nil
}

// ParseJobClassSuggestion 解析模型返回。错误串是稳定的错误分类标识,会进无正文
// 诊断,所以保持英文短词、不带模型原文。
func ParseJobClassSuggestion(raw string) (JobClassSuggestion, error) {
	object, err := decodeUniqueObject(raw)
	if err != nil {
		return JobClassSuggestion{}, err
	}

	classRaw, err := uniqueField(object, "类别", "类别名", "jobClass", "class")
	if err != nil {
		return JobClassSuggestion{}, errors.New(err.Error() + "Class")
	}
	var class string
	if err := json.Unmarshal(classRaw, &class); err != nil {
		return JobClassSuggestion{}, errors.New("invalidClass")
	}
	class = strings.TrimSpace(class)
	if class == "" {
		return JobClassSuggestion{}, errors.New("invalidClass")
	}

	confidenceRaw, err := uniqueField(object, "置信度", "confidence")
	if err != nil {
		return JobClassSuggestion{}, errors.New(err.Error() + "Confidence")
	}
	var confidence float64
	if err := json.Unmarshal(confidenceRaw, &confidence); err != nil ||
		confidence < 0 || confidence > 1 {
		return JobClassSuggestion{}, errors.New("invalidConfidence")
	}

	// 理由只给人读,缺了不算失败——它不参与任何判定。
	reason := ""
	if reasonRaw, reasonErr := uniqueField(object, "理由", "reason"); reasonErr == nil {
		var value string
		if json.Unmarshal(reasonRaw, &value) == nil {
			reason = strings.TrimSpace(value)
		}
	}

	return JobClassSuggestion{Class: class, Confidence: confidence, Reason: reason}, nil
}

// uniqueField 取一个字段,别名里出现多次视为歧义——模型同时写了「类别」和
// "class" 且两者不同时,猜哪个都是错的。
func uniqueField(
	object map[string]json.RawMessage,
	aliases ...string,
) (json.RawMessage, error) {
	var found json.RawMessage
	count := 0
	for _, alias := range aliases {
		if value, exists := object[alias]; exists {
			found = value
			count++
		}
	}
	if count == 0 {
		return nil, errors.New("missing")
	}
	if count > 1 {
		return nil, errors.New("duplicate")
	}
	return found, nil
}
