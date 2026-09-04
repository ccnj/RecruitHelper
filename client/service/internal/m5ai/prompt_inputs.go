package m5ai

import (
	"fmt"
	"strings"
)

// 后台下发提示词的统一渲染(规格 v4「后台下发提示词的统一渲染」,2026-09-03 甲方裁决)。
//
// 五份后台文档共用一套规则:正文里每一处已知输入占位符 {X} 换成指针
// 「显示名(见下方输入参数-显示名)」,数据一律不进正文;全部数据以「【输入参数】」
// 起头、按 promptInputSpecs 的固定顺序以「【输入参数-显示名】」子块追加到末尾。
// 白名单外的占位符原样保留、不拒绝、不换指针、不追加子块——模板作者写错一个
// 占位符名,不该让职位或候选人停下来;导入时另有一行 Warn 留痕(UnknownPromptTokens)。
// 数据是数据不是模板:值里的花括号原样保留,不做二次解释。
const (
	inputSectionHeading = "【输入参数】"
	inputBlockPrefix    = "【输入参数-"
	inputPointerInfix   = "(见下方输入参数-"
)

func inputBlockHeading(displayName string) string {
	return inputBlockPrefix + displayName + "】"
}

func inputPointerText(displayName string) string {
	return displayName + inputPointerInfix + displayName + ")"
}

type promptInputSpec struct {
	token   string
	display string
	// required 为真的输入恒追加子块,模板缺引用也不拒;为假的只在模板引用时追加。
	required bool
}

// promptInputSpecs 是五份后台下发文档的输入白名单,切片顺序即尾部子块顺序。
var promptInputSpecs = map[string][]promptInputSpec{
	"多轮沟通": {
		{token: "推荐时段", display: "推荐时段", required: true},
		{token: "简历", display: "简历", required: true},
		{token: "对话历史", display: "对话历史", required: true},
		{token: "事实库", display: "事实库", required: false},
	},
	"意向判断": {
		{token: "招呼语", display: "招呼语", required: true},
		{token: "回复", display: "回复", required: true},
	},
	"沉默追问": {
		{token: "姓名", display: "姓名", required: true},
		{token: "年龄", display: "年龄", required: true},
		{token: "性别", display: "性别", required: true},
		{token: "简历", display: "简历", required: true},
	},
	"招呼语": {
		{token: "career_state", display: "求职状态", required: true},
		{token: "resume_summary_json", display: "简历摘要", required: true},
	},
	"打分": {
		{token: "resume_json", display: "简历", required: true},
	},
}

// promptOutputExampleTokens 是文档里的输出示例键。它们不是输入,渲染时与陌生占位符
// 同样原样保留;导入扫描不把它们算作陌生,免得每次同步都为一个已知的示例键告警。
var promptOutputExampleTokens = map[string]map[string]struct{}{
	"多轮沟通": {"话术_序列": {}},
}

func promptTokensInOrder(prompt string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, match := range activeTokenPattern.FindAllStringSubmatch(prompt, -1) {
		name := match[1]
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// ScanPromptTokens 返回文档引用到的已知输入占位符(按白名单顺序)与白名单外的占位符
// (按出现顺序)。只有文档类型未登记才报错;陌生占位符不是错误。
func ScanPromptTokens(docType, prompt string) (known, unknown []string, err error) {
	specs, ok := promptInputSpecs[docType]
	if !ok {
		return nil, nil, fmt.Errorf("未知模板类型: %s", docType)
	}
	referenced := make(map[string]struct{})
	for _, name := range promptTokensInOrder(prompt) {
		referenced[name] = struct{}{}
	}
	known = make([]string, 0, len(specs))
	for _, spec := range specs {
		if _, hit := referenced[spec.token]; hit {
			known = append(known, spec.token)
		}
	}
	unknown = UnknownPromptTokens(docType, prompt)
	if unknown == nil {
		unknown = []string{}
	}
	return known, unknown, nil
}

// UnknownPromptTokens 列出白名单外的占位符;未登记的文档类型返回 nil——没有白名单
// 就谈不上陌生。
func UnknownPromptTokens(docType, prompt string) []string {
	specs, ok := promptInputSpecs[docType]
	if !ok {
		return nil
	}
	byToken := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		byToken[spec.token] = struct{}{}
	}
	var unknown []string
	for _, name := range promptTokensInOrder(prompt) {
		if _, known := byToken[name]; known {
			continue
		}
		if _, example := promptOutputExampleTokens[docType][name]; example {
			continue
		}
		unknown = append(unknown, name)
	}
	return unknown
}

// renderPromptInputs 是统一渲染器本体。values 以占位符名为键、以子块正文为值;
// 值为空时子块只剩标题行。陌生占位符与输出示例键原样写回。
func renderPromptInputs(specs []promptInputSpec, prompt string, values map[string]string) string {
	byToken := make(map[string]promptInputSpec, len(specs))
	for _, spec := range specs {
		byToken[spec.token] = spec
	}
	referenced := make(map[string]struct{}, len(specs))
	var body strings.Builder
	cursor := 0
	for _, match := range activeTokenPattern.FindAllStringSubmatchIndex(prompt, -1) {
		name := prompt[match[2]:match[3]]
		spec, known := byToken[name]
		if !known {
			continue
		}
		referenced[name] = struct{}{}
		body.WriteString(prompt[cursor:match[0]])
		body.WriteString(inputPointerText(spec.display))
		cursor = match[1]
	}
	body.WriteString(prompt[cursor:])

	blocks := make([]string, 0, len(specs))
	for _, spec := range specs {
		if _, hit := referenced[spec.token]; !hit && !spec.required {
			continue
		}
		block := inputBlockHeading(spec.display)
		if value := values[spec.token]; value != "" {
			block += "\n" + value
		}
		blocks = append(blocks, block)
	}
	if len(blocks) == 0 {
		return body.String()
	}
	return strings.TrimRight(body.String(), " \t\r\n") + "\n\n" +
		inputSectionHeading + "\n\n" + strings.Join(blocks, "\n\n")
}

func renderPromptWithInputs(docType, prompt string, values map[string]string) (string, error) {
	specs, ok := promptInputSpecs[docType]
	if !ok {
		return "", fmt.Errorf("未知模板类型: %s", docType)
	}
	return renderPromptInputs(specs, prompt, values), nil
}
