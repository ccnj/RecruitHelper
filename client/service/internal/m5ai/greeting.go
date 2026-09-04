package m5ai

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	greetingCareerStateToken   = "career_state"
	greetingResumeSummaryToken = "resume_summary_json"
)

// RenderGreetingPrompt 按统一渲染规则绑定招呼语文档的两个输入(求职状态、简历摘要):
// 正文占位符换指针、数据落尾部子块,占位符出现次数不再校验,陌生占位符原样保留。
func RenderGreetingPrompt(prompt string, input GreetingInputV1) (string, error) {
	if !utf8.ValidString(input.CareerState) || !utf8.ValidString(input.ResumeSummaryJSON) ||
		!json.Valid([]byte(input.ResumeSummaryJSON)) {
		return "", errors.New("invalidGreetingInput")
	}
	rendered, err := renderPromptWithInputs("招呼语", prompt, map[string]string{
		greetingCareerStateToken:   input.CareerState,
		greetingResumeSummaryToken: input.ResumeSummaryJSON,
	})
	if err != nil {
		return "", err
	}
	// 现实边界紧凑版(2026-08-14 甲方裁决,详见 render.go 完整版注释):招呼语
	// 同样是候选人可见正文,不许承诺到场或编造地址。
	return rendered + "\n\n" + realityBoundaryCompactPolicy, nil
}

// ParseGreetingSuggestion consumes only the prompt's canonical Chinese body
// field. All analysis fields are deliberately ignored and never become domain
// actions or persisted model reasoning.
func ParseGreetingSuggestion(raw string) (GreetingSuggestion, error) {
	if !utf8.ValidString(raw) {
		return GreetingSuggestion{}, errors.New("invalidJSON")
	}
	object, err := decodeUniqueObject(raw)
	if err != nil {
		return GreetingSuggestion{}, err
	}
	bodyRaw, exists := object["招呼语"]
	if !exists {
		return GreetingSuggestion{}, errors.New("missingGreetingText")
	}
	var body string
	if err := json.Unmarshal(bodyRaw, &body); err != nil {
		return GreetingSuggestion{}, errors.New("invalidGreetingText")
	}
	body = norm.NFC.String(strings.TrimSpace(body))
	if err := ValidateSendText(body); err != nil {
		return GreetingSuggestion{}, err
	}
	return GreetingSuggestion{Text: body}, nil
}
