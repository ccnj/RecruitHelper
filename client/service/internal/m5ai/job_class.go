package m5ai

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// JobClassInputFormatVersion 是本提示词形态的修订号。改动提示词文本或输入结构
// 时必须同时改它——否则 ai-traces 里两种形态的调用无从区分。
//
// v2 = 2026-08-01 起的全批统一分配。v1 是逐个职位各问一次的形态，已废。
const JobClassInputFormatVersion = "jobClass/2"

// JobClassCandidateInput 是给模型看的一个候选:类别名与平台的官方释义。
// 释义是判断贴合度的主要依据,不截断成半句。
type JobClassCandidateInput struct {
	Name       string
	Definition string
}

// JobClassJobInput 是待分配的一个职位。候选清单由平台按这个职位的名称与描述
// 现给，**每个职位的候选各不相同**，所以必须随职位一起带，不能合并成一张全局表。
type JobClassJobInput struct {
	JobID       string
	JobName     string
	Description string
	Candidates  []JobClassCandidateInput
}

// JobClassAssignment 是模型给一个职位的分配。它只是建议:类别名必须由确定性
// 代码回到**那个职位自己的**候选清单里逐字核对，核不上一律拒绝。
type JobClassAssignment struct {
	JobID      string
	Class      string
	Confidence float64
	Reason     string
}

// 分配问题的稳定分类标识。会进无正文诊断，保持英文短词、不带模型原文。
const (
	JobClassProblemMissing         = "missing"
	JobClassProblemNotInCandidates = "notInCandidates"
	JobClassProblemDuplicate       = "duplicateJob"
	JobClassProblemUnknownJob      = "unknownJob"
)

// jobClassInstruction 是甲方 2026-07-30 审定、2026-08-01 增补差异化的提示词原文。
//
// 规则 3 的由来:这些岗位实际是做保险的,但招聘策略正是招**转行**的人(职位名里
// 就写着「HR/培训/市场背景优先」)。类别落进保险桶,平台会把职位推给已在保险业内
// 的人,与招聘意图相反且池子更窄。它由规则 4 兜住诚实性——必须仍与职位实际内容
// 相符,不是"随便找个不像保险的"。
//
// 规则 2 的由来:发多个职位的核心诉求是扩充候选人池。类别决定平台把职位推给哪
// 一类求职者,十几个职位全落进同一个类别就等于推给同一批人,多发的那些白发。
const jobClassInstruction = `你在为一批招聘职位挑选招聘平台上的「职位类别」。类别决定平台把每个职位推送给哪一类求职者，选错会让职位触达错误的人群。

我会给你若干个职位，每个职位带：职位编号、职位名称、职位描述原文，以及平台**专门针对这个职位**给出的候选类别清单（每项含类别名与平台的官方释义）。不同职位的候选清单不一样。

规则，按优先级：

1. 每个职位只能从**它自己的**候选清单里选。必须原样返回其中一个类别名，一个字都不能改，不得自造、不得合并、不得改写，也不得把别的职位的候选拿过来用。
2. **尽量让这批职位落到互不相同的类别上。** 我们同时发多个职位，图的就是让更多不同背景的求职者看到；若它们都落进同一个类别，平台会把它们推给同一批人，多发的那几个等于白发。所以在贴合度接近时，优先选一个还没被占用的类别。
3. 不要选保险相关的类别（以保险从业为核心定义的，例如「保险顾问」「保险项目策划」）。这些岗位面向的是从其他行业转行而来的人；落进保险类会把职位推送给已在保险业内的人，与招聘意图相反，也会显著缩小候选人池。
4. 在满足以上的前提下，为每个职位选与它的名称和描述所述实际工作内容最贴合的一项。贴合度以平台给出的官方释义为准，不要凭类别名字面猜。
5. 必须为**每一个**职位都给出选择。不允许弃选，不允许漏掉任何一个职位，不允许返回「无法判断」。
6. 规则 2 与规则 4 冲突时以规则 4 为准：某个职位的候选全被占用、或者不撞车就只能选一个明显不贴合的，那就撞车，并在该职位的理由里写明原因。
7. 若某个职位的候选清单里所有选项都是保险相关，仍必须返回其中一项，但该项置信度必须低于 0.3，且在理由里明确写出「候选全部为保险相关」。

只输出 JSON，不要任何额外文字、不要代码块围栏。理由务必简短，一句话以内：
{"分配":[{"职位":"<职位编号，原样照抄>","类别":"<原样照抄的候选类别名>","置信度":<0 到 1 的小数>,"理由":"<一句>"}]}`

// RenderJobClassPrompt 拼出完整请求正文。按数据边界,这里只放职位上下文:
// 职位编号、职位名、描述原文、候选清单及其平台释义;不带任何候选人身份字段。
//
// occupied 是"已经被占用、请尽量避开"的类别名。整批分配时它为空;运营单独重跑
// 某一个职位时,把其余职位已定的类别传进来,模型就会主动避开——因此系统里只有
// 这一套类别提示词,没有批量版与单职位版两份。
func RenderJobClassPrompt(jobs []JobClassJobInput, occupied []string) (string, error) {
	if len(jobs) == 0 {
		return "", errors.New("待分配职位清单为空")
	}
	var builder strings.Builder
	builder.WriteString(jobClassInstruction)
	if len(occupied) > 0 {
		builder.WriteString("\n\n---\n\n本批其余职位已经占用了这些类别，请尽量避开：")
		builder.WriteString(strings.Join(occupied, "、"))
	}
	seen := make(map[string]struct{}, len(jobs))
	for _, job := range jobs {
		jobID := strings.TrimSpace(job.JobID)
		jobName := strings.TrimSpace(job.JobName)
		description := strings.TrimSpace(job.Description)
		if jobID == "" {
			return "", errors.New("职位编号不能为空")
		}
		if _, duplicated := seen[jobID]; duplicated {
			// 同一个编号出现两次,模型的回答就无从对应回职位。
			return "", errors.New("职位编号重复")
		}
		seen[jobID] = struct{}{}
		if jobName == "" || description == "" {
			return "", errors.New("职位名称与职位描述都不能为空")
		}
		if len(job.Candidates) == 0 {
			return "", errors.New("候选类别清单为空")
		}
		builder.WriteString("\n\n---\n\n职位编号：")
		builder.WriteString(jobID)
		builder.WriteString("\n职位名称：")
		builder.WriteString(jobName)
		builder.WriteString("\n\n职位描述：\n")
		builder.WriteString(description)
		builder.WriteString("\n\n该职位的候选类别清单：\n")
		for index, candidate := range job.Candidates {
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
	}
	return builder.String(), nil
}

// ParseJobClassAssignments 解析模型返回的分配表。只做形状层面的解析,与候选清单
// 有关的核对交给 ClassifyJobClassAssignments——两者分开是因为前者失败意味着整次
// 返回都不可用,后者失败可以只废掉个别职位。
func ParseJobClassAssignments(raw string) ([]JobClassAssignment, error) {
	object, err := decodeUniqueObject(raw)
	if err != nil {
		return nil, err
	}
	listRaw, err := uniqueField(object, "分配", "assignments")
	if err != nil {
		return nil, errors.New(err.Error() + "Assignments")
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(listRaw, &entries); err != nil {
		return nil, errors.New("invalidAssignments")
	}
	if len(entries) == 0 {
		return nil, errors.New("emptyAssignments")
	}

	out := make([]JobClassAssignment, 0, len(entries))
	for _, entry := range entries {
		jobRaw, fieldErr := uniqueField(entry, "职位", "职位编号", "jobId")
		if fieldErr != nil {
			return nil, errors.New(fieldErr.Error() + "JobId")
		}
		jobID, ok := decodeLooseString(jobRaw)
		if !ok || jobID == "" {
			return nil, errors.New("invalidJobId")
		}

		classRaw, fieldErr := uniqueField(entry, "类别", "类别名", "jobClass", "class")
		if fieldErr != nil {
			return nil, errors.New(fieldErr.Error() + "Class")
		}
		var class string
		if err := json.Unmarshal(classRaw, &class); err != nil {
			return nil, errors.New("invalidClass")
		}
		class = strings.TrimSpace(class)
		if class == "" {
			return nil, errors.New("invalidClass")
		}

		confidenceRaw, fieldErr := uniqueField(entry, "置信度", "confidence")
		if fieldErr != nil {
			return nil, errors.New(fieldErr.Error() + "Confidence")
		}
		var confidence float64
		if err := json.Unmarshal(confidenceRaw, &confidence); err != nil ||
			confidence < 0 || confidence > 1 {
			return nil, errors.New("invalidConfidence")
		}

		// 理由只给人读,缺了不算失败——它不参与任何判定。
		reason := ""
		if reasonRaw, reasonErr := uniqueField(entry, "理由", "reason"); reasonErr == nil {
			var value string
			if json.Unmarshal(reasonRaw, &value) == nil {
				reason = strings.TrimSpace(value)
			}
		}
		out = append(out, JobClassAssignment{
			JobID: jobID, Class: class, Confidence: confidence, Reason: reason,
		})
	}
	return out, nil
}

// ClassifyJobClassAssignments 把分配表按"能不能用"分成两堆。
//
// 唯一的硬闸:每个职位分到的类别必须**逐字**出现在**那个职位自己的**候选清单里。
// 绝不模糊匹配、绝不就近取一个——类别选错会把职位推给错误的人群,而页面看上去
// 一切正常。差异化不在这里核:它是目标不是闸,候选高度重叠时物理上就不可满足,
// 做成闸会直接死锁。
//
// 返回 accepted 与 problems 而不是一个 error,是因为按裁决"3 次之后保留合法的、
// 跳过不合法的",调用方需要看得见到底哪些职位成了、哪些没成。
func ClassifyJobClassAssignments(
	assignments []JobClassAssignment,
	jobs []JobClassJobInput,
) (accepted map[string]JobClassAssignment, problems map[string]string) {
	candidates := make(map[string][]string, len(jobs))
	for _, job := range jobs {
		names := make([]string, 0, len(job.Candidates))
		for _, candidate := range job.Candidates {
			names = append(names, candidate.Name)
		}
		candidates[job.JobID] = names
	}

	accepted = make(map[string]JobClassAssignment, len(jobs))
	problems = make(map[string]string, len(jobs))
	for _, assignment := range assignments {
		names, known := candidates[assignment.JobID]
		if !known {
			// 模型编了一个不在本批里的职位编号，不对应任何待发职位，只记诊断。
			problems[assignment.JobID] = JobClassProblemUnknownJob
			continue
		}
		if problems[assignment.JobID] == JobClassProblemDuplicate {
			continue
		}
		if _, duplicated := accepted[assignment.JobID]; duplicated {
			// 同一个职位给了两次:两个都不能用——挑哪个都是替甲方猜。
			delete(accepted, assignment.JobID)
			problems[assignment.JobID] = JobClassProblemDuplicate
			continue
		}
		if !containsExact(assignment.Class, names) {
			problems[assignment.JobID] = JobClassProblemNotInCandidates
			continue
		}
		accepted[assignment.JobID] = assignment
	}
	for _, job := range jobs {
		if _, done := accepted[job.JobID]; done {
			continue
		}
		if _, flagged := problems[job.JobID]; flagged {
			continue
		}
		problems[job.JobID] = JobClassProblemMissing
	}
	return accepted, problems
}

// JobClassCollisions 找出被多个职位共用的类别。差异化不是闸,撞车照常放行,
// 但必须让运营在二次确认清单上看见。返回值内的职位编号排序,便于稳定呈现。
func JobClassCollisions(assigned map[string]string) map[string][]string {
	byClass := make(map[string][]string, len(assigned))
	for jobID, class := range assigned {
		byClass[class] = append(byClass[class], jobID)
	}
	out := make(map[string][]string, len(byClass))
	for class, jobIDs := range byClass {
		if len(jobIDs) < 2 {
			continue
		}
		sort.Strings(jobIDs)
		out[class] = jobIDs
	}
	return out
}

func containsExact(chosen string, names []string) bool {
	for _, name := range names {
		if name == chosen {
			return true
		}
	}
	return false
}

// decodeLooseString 接受字符串或数字形态的职位编号。编号在提示词里是原样照抄的
// 文本，但模型常把纯数字编号写成 JSON 数字;这属于形态差异、不是内容错误，认下来
// 比多跑两次重试划算。类别名不适用本宽松——那是要逐字点中控件的。
func decodeLooseString(raw json.RawMessage) (string, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text), true
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return strings.TrimSpace(number.String()), true
	}
	return "", false
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
