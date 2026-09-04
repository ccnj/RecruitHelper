package m5ai

import (
	"encoding/json"
	"errors"
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
	return prompt, nil
}

// RenderSilenceFollowupPrompt 按统一渲染规则绑定沉默追问的四个输入:正文占位符换
// 指针、数据落尾部子块;姓名固定为中性值「候选人」,年龄与性别只取简历事实。
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

	rendered, err := renderPromptWithInputs(silenceFollowupDocType, prompt, map[string]string{
		"姓名": "候选人",
		"年龄": age,
		"性别": gender,
		"简历": canonicalResumeJSON,
	})
	if err != nil {
		return "", err
	}
	// 现实边界紧凑版(2026-08-14 甲方裁决,详见 render.go 完整版注释):追问
	// 话术同样是候选人可见正文,不许承诺到场或编造地址。
	return rendered + "\n\n" + realityBoundaryCompactPolicy, nil
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
