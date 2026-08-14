package m5ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	greetingCareerStateToken   = "career_state"
	greetingResumeSummaryToken = "resume_summary_json"
)

// RenderGreetingPrompt binds the two and only two active inputs declared by
// the imported greeting document. Replacement scans the original template in
// one pass, so token-looking candidate text cannot become a second template.
func RenderGreetingPrompt(prompt string, input GreetingInputV1) (string, error) {
	if !utf8.ValidString(input.CareerState) || !utf8.ValidString(input.ResumeSummaryJSON) ||
		!json.Valid([]byte(input.ResumeSummaryJSON)) {
		return "", errors.New("invalidGreetingInput")
	}
	counts := map[string]int{
		greetingCareerStateToken:   0,
		greetingResumeSummaryToken: 0,
	}
	values := map[string]string{
		greetingCareerStateToken:   input.CareerState,
		greetingResumeSummaryToken: input.ResumeSummaryJSON,
	}

	var builder strings.Builder
	cursor := 0
	for _, match := range activeTokenPattern.FindAllStringSubmatchIndex(prompt, -1) {
		name := prompt[match[2]:match[3]]
		value, ok := values[name]
		if !ok {
			return "", fmt.Errorf("unknownTemplateToken: %s", name)
		}
		counts[name]++
		builder.WriteString(prompt[cursor:match[0]])
		builder.WriteString(value)
		cursor = match[1]
	}
	builder.WriteString(prompt[cursor:])
	if counts[greetingCareerStateToken] != 1 || counts[greetingResumeSummaryToken] != 1 {
		return "", errors.New("invalidGreetingPrompt")
	}
	// 现实边界紧凑版(2026-08-14 甲方裁决,详见 render.go 完整版注释):招呼语
	// 同样是候选人可见正文,不许承诺到场或编造地址。
	return builder.String() + "\n\n" + realityBoundaryCompactPolicy, nil
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
