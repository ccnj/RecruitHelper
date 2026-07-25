package m5ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const silenceFollowupDocType = "沉默追问"

// SilenceFollowupPrompt returns the one source document preserved by the
// immutable context revision. It deliberately does not duplicate that source
// text into CommunicationView, so older rows with the same revision material
// remain byte-compatible.
func SilenceFollowupPrompt(revision ContextRevision) (string, error) {
	var prompt string
	count := 0
	for _, document := range revision.SourcePackage.Documents {
		if document.DocType != silenceFollowupDocType {
			continue
		}
		count++
		prompt = document.Content
	}
	if count != 1 || strings.TrimSpace(prompt) == "" {
		return "", errors.New("missingSilenceFollowupPrompt")
	}
	if err := requireInputTokens(silenceFollowupDocType, prompt, "姓名", "年龄", "性别", "简历"); err != nil {
		return "", err
	}
	return prompt, nil
}

// RenderSilenceFollowupPrompt binds the four and only four approved inputs.
// Replacement scans the original template once, so token-looking resume text
// cannot become a second template pass.
func RenderSilenceFollowupPrompt(prompt, canonicalResumeJSON string) (string, error) {
	if !utf8.ValidString(canonicalResumeJSON) || !json.Valid([]byte(canonicalResumeJSON)) {
		return "", errors.New("invalidSilenceFollowupResume")
	}
	var resume struct {
		Basic *[]resumeLabelValue `json:"基本"`
	}
	if err := json.Unmarshal([]byte(canonicalResumeJSON), &resume); err != nil || resume.Basic == nil {
		return "", errors.New("invalidSilenceFollowupResume")
	}
	age, err := silenceResumeBasicFact(*resume.Basic, "年龄")
	if err != nil {
		return "", err
	}
	gender, err := silenceResumeBasicFact(*resume.Basic, "性别")
	if err != nil {
		return "", err
	}

	values := map[string]string{
		"姓名": "候选人",
		"年龄": age,
		"性别": gender,
		"简历": canonicalResumeJSON,
	}
	counts := map[string]int{"姓名": 0, "年龄": 0, "性别": 0, "简历": 0}
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
	for _, name := range []string{"姓名", "年龄", "性别", "简历"} {
		if counts[name] != 1 {
			return "", errors.New("invalidSilenceFollowupPrompt")
		}
	}
	rendered := builder.String()
	if strings.TrimSpace(rendered) == "" {
		return "", errors.New("missingRenderedSilenceFollowupPrompt")
	}
	return rendered, nil
}

func silenceResumeBasicFact(basic []resumeLabelValue, wanted string) (string, error) {
	value := ""
	for _, item := range basic {
		if strings.TrimSpace(item.Label) != wanted {
			continue
		}
		observed := strings.TrimSpace(item.Value)
		if observed == "" {
			continue
		}
		if value != "" && value != observed {
			return "", errors.New("ambiguousSilenceFollowupResumeFact")
		}
		value = observed
	}
	if value == "" {
		return "未知", nil
	}
	return value, nil
}

// ParseSilenceFollowupSuggestion accepts no action-bearing output. 抓的点 is
// type-checked when present and then discarded as non-authoritative review
// material.
func ParseSilenceFollowupSuggestion(raw string) (SilenceFollowupSuggestion, error) {
	if !utf8.ValidString(raw) {
		return SilenceFollowupSuggestion{}, errors.New("invalidJSON")
	}
	object, err := decodeUniqueObject(raw)
	if err != nil {
		return SilenceFollowupSuggestion{}, err
	}
	for key := range object {
		if key != "话术" && key != "抓的点" {
			return SilenceFollowupSuggestion{}, errors.New("unknownOutputKey")
		}
	}
	textRaw, exists := object["话术"]
	if !exists {
		return SilenceFollowupSuggestion{}, errors.New("missingSilenceFollowupText")
	}
	var text string
	if err := json.Unmarshal(textRaw, &text); err != nil {
		return SilenceFollowupSuggestion{}, errors.New("invalidSilenceFollowupText")
	}
	if reviewRaw, exists := object["抓的点"]; exists {
		var review string
		if err := json.Unmarshal(reviewRaw, &review); err != nil {
			return SilenceFollowupSuggestion{}, errors.New("invalidSilenceFollowupReview")
		}
	}
	text = norm.NFC.String(strings.TrimSpace(text))
	if err := ValidateSendText(text); err != nil {
		return SilenceFollowupSuggestion{}, err
	}
	return SilenceFollowupSuggestion{Text: text}, nil
}
