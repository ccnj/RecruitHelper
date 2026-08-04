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

// boundTimeFieldKey 给出该动作在模型输出里绑定的时间字段名。两种邀面动作
// 各绑定一个,其余动作没有绑定字段——此时两个时间字段都与本次动作无关。
func boundTimeFieldKey(action ReplyAction) string {
	switch action {
	case ReplyActionStartOnlineMeeting:
		return "会议时间"
	case ReplyActionStartOnsiteInterview:
		return "面试时间"
	default:
		return ""
	}
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
		if key != "话术_序列" && key != "动作" && key != "会议时间" && key != "面试时间" {
			return ReplySuggestion{}, errors.New("unknownOutputKey")
		}
	}
	action := ReplyActionNone
	if actionRaw, exists := object["动作"]; exists {
		var decoded any
		if json.Unmarshal(actionRaw, &decoded) != nil {
			return ReplySuggestion{}, errors.New("invalidReplyActionType")
		}
		value, ok := decoded.(string)
		if !ok {
			return ReplySuggestion{}, errors.New("invalidReplyActionType")
		}
		switch value {
		case "", "无":
			action = ReplyActionNone
		case "发起线上会议":
			action = ReplyActionStartOnlineMeeting
		case "发起线下面试":
			action = ReplyActionStartOnsiteInterview
		case "发起换微信邀请":
			action = ReplyActionInviteWechat
		default:
			return ReplySuggestion{}, errors.New("invalidReplyAction")
		}
	}
	// 每种邀面动作只绑定一个时间字段;另一个时间字段无论缺席、空值还是携带
	// 任意内容都整个忽略,连类型都不校验(2026-08-04 甲方裁决:模型多填一个与
	// 本次动作无关的已知字段不构成语义跑偏,不值得让整轮回复作废)。白名单外
	// 的顶层字段仍整体拒绝。
	meetingTime := ""
	if boundKey := boundTimeFieldKey(action); boundKey != "" {
		timeRaw, exists := object[boundKey]
		if !exists {
			return ReplySuggestion{}, errors.New("missingMeetingTime")
		}
		var decoded any
		if json.Unmarshal(timeRaw, &decoded) != nil {
			return ReplySuggestion{}, errors.New("invalidMeetingTimeType")
		}
		value, ok := decoded.(string)
		if !ok {
			return ReplySuggestion{}, errors.New("invalidMeetingTimeType")
		}
		if strings.TrimSpace(value) == "" {
			return ReplySuggestion{}, errors.New("missingMeetingTime")
		}
		meetingTime = value
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
	if len(trimmed) > ReplyPhraseMaxItems {
		return ReplySuggestion{}, errors.New("phraseSequenceLimit")
	}
	for _, phrase := range trimmed {
		if err := ValidateSendText(phrase); err != nil {
			return ReplySuggestion{}, err
		}
	}
	text := strings.Join(trimmed, "\n")
	if err := ValidateSendText(text); err != nil {
		return ReplySuggestion{}, err
	}
	return ReplySuggestion{
		Phrases: append([]string(nil), trimmed...),
		Text:    text, Action: action, MeetingTime: meetingTime,
	}, nil
}

// CanonicalReplyPhrases returns the provider-defined bubble boundaries and
// their newline-joined compatibility summary. A legacy in-memory suggestion
// with only Text remains one bubble even when that text contains a newline;
// planners must never infer new boundaries by splitting Text.
func CanonicalReplyPhrases(suggestion ReplySuggestion) ([]string, string, error) {
	if len(suggestion.Phrases) == 0 {
		if err := ValidateSendText(suggestion.Text); err != nil {
			return nil, "", err
		}
		return []string{suggestion.Text}, suggestion.Text, nil
	}
	if len(suggestion.Phrases) > ReplyPhraseMaxItems {
		return nil, "", errors.New("phraseSequenceLimit")
	}
	phrases := make([]string, len(suggestion.Phrases))
	for index, phrase := range suggestion.Phrases {
		if phrase == "" || phrase != strings.TrimSpace(phrase) {
			return nil, "", errors.New("invalidPhraseSequence")
		}
		if err := ValidateSendText(phrase); err != nil {
			return nil, "", err
		}
		phrases[index] = phrase
	}
	text := strings.Join(phrases, "\n")
	if err := ValidateSendText(text); err != nil {
		return nil, "", err
	}
	if suggestion.Text != "" && suggestion.Text != text {
		return nil, "", errors.New("replySummaryMismatch")
	}
	return phrases, text, nil
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
