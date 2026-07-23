package m5ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

func decodeUniqueObject(raw string) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	token, err := dec.Token()
	if err != nil {
		return nil, errors.New("invalidJSON")
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil, errors.New("invalidJSON")
	}
	values := make(map[string]json.RawMessage)
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return nil, errors.New("invalidJSON")
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("invalidJSON")
		}
		if _, exists := values[key]; exists {
			return nil, errors.New("duplicateOutputKey")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, errors.New("invalidJSON")
		}
		values[key] = bytes.Clone(value)
	}
	if _, err := dec.Token(); err != nil {
		return nil, errors.New("invalidJSON")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalidJSON")
	}
	return values, nil
}

func ParseIntentSuggestion(raw string) (IntentSuggestion, error) {
	object, err := decodeUniqueObject(raw)
	if err != nil {
		return IntentSuggestion{}, err
	}
	allowed := map[string]bool{"信号": true, "signal": true, "理由": true, "reason": true}
	for key := range object {
		if !allowed[key] {
			return IntentSuggestion{}, errors.New("unknownOutputKey")
		}
	}
	if _, chinese := object["信号"]; chinese {
		if _, english := object["signal"]; english {
			return IntentSuggestion{}, errors.New("duplicateIntentSignal")
		}
	}
	signalRaw, exists := object["信号"]
	if !exists {
		signalRaw, exists = object["signal"]
	}
	if !exists {
		return IntentSuggestion{}, errors.New("missingIntentSignal")
	}
	var signal string
	if json.Unmarshal(signalRaw, &signal) != nil {
		return IntentSuggestion{}, errors.New("invalidIntentSignal")
	}
	labels := map[string]IntentLabel{
		"有意向": IntentInterested,
		"拒绝":  IntentRejected,
		"中性":  IntentNeutral,
	}
	label, ok := labels[signal]
	if !ok {
		return IntentSuggestion{}, errors.New("invalidIntentSignal")
	}
	return IntentSuggestion{Label: label}, nil
}

func ParseReplySuggestion(raw string) (ReplySuggestion, error) {
	object, err := decodeUniqueObject(raw)
	if err != nil {
		return ReplySuggestion{}, err
	}
	phrasesRaw, exists := object["话术_序列"]
	if !exists {
		return ReplySuggestion{}, errors.New("missingPhraseSequence")
	}
	for key := range object {
		if key != "话术_序列" && key != "动作" && key != "会议时间" {
			return ReplySuggestion{}, errors.New("unknownOutputKey")
		}
	}
	if meetingTimeRaw, exists := object["会议时间"]; exists {
		var meetingTime *string
		if json.Unmarshal(meetingTimeRaw, &meetingTime) != nil || meetingTime == nil {
			return ReplySuggestion{}, errors.New("invalidMeetingTimeType")
		}
	}
	var phrases []string
	if err := json.Unmarshal(phrasesRaw, &phrases); err != nil || phrases == nil {
		return ReplySuggestion{}, errors.New("invalidPhraseSequenceType")
	}
	trimmed := make([]string, 0, len(phrases))
	for _, phrase := range phrases {
		if value := strings.TrimSpace(phrase); value != "" {
			trimmed = append(trimmed, value)
		}
	}
	if len(trimmed) == 0 {
		return ReplySuggestion{}, errors.New("emptyPhraseSequence")
	}
	text := strings.Join(trimmed, "\n")
	if err := ValidateSendText(text); err != nil {
		return ReplySuggestion{}, err
	}
	return ReplySuggestion{Text: text}, nil
}

type ShortCircuitResult struct {
	Matched bool
	Label   IntentLabel
	Source  string
	RuleID  string
}

// ClassifyIntentShortCircuit intentionally has no production rules in M5-A:
// batch 0B observed zero qualifying samples. Empty turns retain their explicit
// deterministic neutral meaning; every non-empty ordinary text turn proceeds
// to the provider.
func ClassifyIntentShortCircuit(orderedInboundTexts []string) ShortCircuitResult {
	if len(orderedInboundTexts) == 0 {
		return ShortCircuitResult{Matched: true, Label: IntentNeutral, Source: "emptyTurn"}
	}
	return ShortCircuitResult{}
}

var v4RejectionTemplate = regexp.MustCompile(
	`(很抱歉|抱歉)[，,]?我(暂时)?(不|觉得这个).{0,20}(考虑|不匹配|不感兴趣|不合适)`,
)

var v4ResumeMarkers = []struct {
	ID      string
	Literal string
}{
	{ID: "M5I-RSM-01", Literal: "发送了在线简历"},
	{ID: "M5I-RSM-02", Literal: "附件简历"},
}

var v4ShortRejections = []struct {
	ID      string
	Literal string
}{
	{ID: "M5I-SR-01", Literal: "不考虑"},
	{ID: "M5I-SR-02", Literal: "不感兴趣"},
	{ID: "M5I-SR-04", Literal: "不合适"},
}

// ClassifyIntentShortCircuitV4 is the currently approved deterministic rule
// set. The M5-A function above remains frozen for its historical fixture; new
// production communication uses this versioned classifier. Messages are never
// concatenated, while family priority applies across the complete turn.
func ClassifyIntentShortCircuitV4(orderedInboundTexts []string) ShortCircuitResult {
	if len(orderedInboundTexts) == 0 {
		return ShortCircuitResult{Matched: true, Label: IntentNeutral, Source: "emptyTurn"}
	}
	texts := make([]string, len(orderedInboundTexts))
	for index, text := range orderedInboundTexts {
		texts[index] = normalizeIntentRuleText(text)
	}

	for _, text := range texts {
		for _, rule := range v4ResumeMarkers {
			if strings.Contains(text, rule.Literal) {
				return ShortCircuitResult{
					Matched: true, Label: IntentInterested, Source: "resumeMarker", RuleID: rule.ID,
				}
			}
		}
	}
	for _, text := range texts {
		match := v4RejectionTemplate.FindStringSubmatch(text)
		if len(match) != 5 {
			continue
		}
		return ShortCircuitResult{
			Matched: true, Label: IntentRejected, Source: "rejectionRegex",
			RuleID: rejectionTemplateRuleID(match[3], match[4]),
		}
	}
	for _, text := range texts {
		if utf8.RuneCountInString(text) > 25 || strings.ContainsAny(text, "?？") {
			continue
		}
		for _, rule := range v4ShortRejections {
			if strings.Contains(text, rule.Literal) {
				return ShortCircuitResult{
					Matched: true, Label: IntentRejected, Source: "shortRejection", RuleID: rule.ID,
				}
			}
		}
	}
	return ShortCircuitResult{}
}

func normalizeIntentRuleText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, norm.NFC.String(value))
}

func rejectionTemplateRuleID(mode, outcome string) string {
	prefix := "N"
	if mode == "觉得这个" {
		prefix = "THINK"
	}
	suffix := map[string]string{
		"考虑": "CONSIDER", "不匹配": "MISMATCH",
		"不感兴趣": "NOT_INTERESTED", "不合适": "UNSUITABLE",
	}[outcome]
	return "M5I-RT-" + prefix + "-" + suffix
}
