package m5ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode/utf8"
)

const (
	slotPointerText       = "可约面时间(见【可约面时间】)"
	slotHeading           = "【可约面时间】"
	slotFormatGuard       = "话术中最多写出1-2个具体时段，严禁罗列时段列表；写具体时间用「7月14日14:00」这种「X月X日+24小时制」格式。"
	customerFactsHeading  = "【客户事实库】"
	intentEnvelopeHeading = "【对话数据信封/v1】"
	historyTruncateSuffix = "…(超长消息已截断)"
)

var activeTokenPattern = regexp.MustCompile(`\{([\p{L}_][\p{L}\p{N}_]*)\}`)

var allowedTokens = map[string]map[string]string{
	"多轮沟通": {
		"简历": "input", "推荐时段": "input", "对话历史": "input", "话术_序列": "output",
	},
	"意向判断": {"回复": "input", "招呼语": "input"},
}

func ValidatePromptTokens(docType, prompt string) ([]string, error) {
	allowed, ok := allowedTokens[docType]
	if !ok {
		return nil, fmt.Errorf("未知模板类型: %s", docType)
	}
	seen := make(map[string]struct{})
	for _, match := range activeTokenPattern.FindAllStringSubmatch(prompt, -1) {
		name := match[1]
		class, exists := allowed[name]
		if !exists {
			return nil, fmt.Errorf("unknownTemplateToken: %s", name)
		}
		if class == "input" {
			seen[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func requireInputTokens(docType, prompt string, required ...string) error {
	found, err := ValidatePromptTokens(docType, prompt)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(found))
	for _, name := range found {
		seen[name] = struct{}{}
	}
	for _, name := range required {
		if _, ok := seen[name]; !ok {
			return fmt.Errorf("missingTemplateToken: %s", name)
		}
	}
	return nil
}

type resumeLabelValue struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type storedResume struct {
	Basic           *[]resumeLabelValue `json:"basic"`
	Expectations    *[]resumeLabelValue `json:"expectations"`
	SelfEvaluation  *string             `json:"selfEvaluation"`
	Education       *string             `json:"education"`
	WorkExperiences *string             `json:"workExperiences"`
}

func RenderResumeJSON(snapshotJSON string) (string, error) {
	var source storedResume
	if err := json.Unmarshal([]byte(snapshotJSON), &source); err != nil || source.Basic == nil ||
		source.Expectations == nil || source.SelfEvaluation == nil || source.Education == nil ||
		source.WorkExperiences == nil {
		return "", errors.New("missingRequiredSection")
	}
	for _, values := range [][]resumeLabelValue{*source.Basic, *source.Expectations} {
		for _, item := range values {
			if strings.TrimSpace(item.Label) == "" {
				return "", errors.New("invalidResumeLabel")
			}
		}
	}
	// Struct order is the frozen canonical byte order.
	view := struct {
		Basic           []resumeLabelValue `json:"基本"`
		Expectations    []resumeLabelValue `json:"期望"`
		SelfEvaluation  string             `json:"自评"`
		Education       string             `json:"教育经历"`
		WorkExperiences string             `json:"工作经历"`
	}{
		Basic: *source.Basic, Expectations: *source.Expectations,
		SelfEvaluation: *source.SelfEvaluation, Education: *source.Education,
		WorkExperiences: *source.WorkExperiences,
	}
	raw, err := json.Marshal(view)
	return string(raw), err
}

func activeMessages(messages []AdviceMessage) ([]AdviceMessage, error) {
	active := make([]AdviceMessage, 0, len(messages))
	seen := make(map[int64]struct{}, len(messages))
	for _, message := range messages {
		if message.Retracted || strings.TrimSpace(message.Text) == "" {
			continue
		}
		if message.Seq <= 0 || (message.Direction != "inbound" && message.Direction != "outbound") {
			return nil, errors.New("invalidHistoryMessage")
		}
		if _, exists := seen[message.Seq]; exists {
			return nil, errors.New("duplicateMessageSeq")
		}
		seen[message.Seq] = struct{}{}
		message.Text = strings.TrimSpace(message.Text)
		active = append(active, message)
	}
	sort.Slice(active, func(i, j int) bool { return active[i].Seq < active[j].Seq })
	return active, nil
}

func limitRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + historyTruncateSuffix
}

func LatestHistory(messages []AdviceMessage) ([]AdviceMessage, error) {
	active, err := activeMessages(messages)
	if err != nil {
		return nil, err
	}
	if len(active) > HistoryLimit {
		active = active[len(active)-HistoryLimit:]
	}
	return active, nil
}

func RenderHistory(messages []AdviceMessage) (string, error) {
	active, err := LatestHistory(messages)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(active))
	for _, message := range active {
		label := "候选人(消息)"
		limit := 1000
		if message.Direction == "outbound" {
			label = "我(消息)"
			limit = 300
			if message.Kind == "greeting" {
				label = "我(招呼语)"
			}
		}
		lines = append(lines, label+":"+limitRunes(message.Text, limit))
	}
	return strings.Join(lines, "\n"), nil
}

type IntentEnvelope struct {
	HistoryBeforeTurn []AdviceMessage `json:"historyBeforeTurn"`
	CurrentTurn       []AdviceMessage `json:"currentTurn"`
}

func BuildIntentEnvelope(history, current []AdviceMessage) (string, error) {
	prior, err := LatestHistory(history)
	if err != nil {
		return "", err
	}
	turn, err := activeMessages(current)
	if err != nil {
		return "", err
	}
	seen := make(map[int64]struct{}, len(prior))
	for _, message := range prior {
		seen[message.Seq] = struct{}{}
	}
	for _, message := range turn {
		if _, exists := seen[message.Seq]; exists {
			return "", errors.New("overlappingTurnBoundary")
		}
	}
	canonicalize := func(messages []AdviceMessage) []AdviceMessage {
		out := make([]AdviceMessage, len(messages))
		for i := range messages {
			out[i] = AdviceMessage{
				Seq: messages[i].Seq, Direction: messages[i].Direction,
				Kind: messages[i].Kind, Text: messages[i].Text,
			}
		}
		return out
	}
	raw, err := json.Marshal(IntentEnvelope{
		HistoryBeforeTurn: canonicalize(prior), CurrentTurn: canonicalize(turn),
	})
	return string(raw), err
}

var shanghai = func() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		panic("Asia/Shanghai 时区不可用: " + err.Error())
	}
	return location
}()

func GenerateDefaultSlots(frozenNow time.Time) []string {
	now := frozenNow.In(shanghai)
	startDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, shanghai)
	var slots []string
	for offset := 0; offset <= 13; offset++ {
		day := startDay.AddDate(0, 0, offset)
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			continue
		}
		for hour := 9; hour < 18; hour++ {
			slot := time.Date(day.Year(), day.Month(), day.Day(), hour, 0, 0, 0, shanghai)
			if slot.Before(now) {
				continue
			}
			slots = append(slots, slot.Format("2006-01-02 15:04:05"))
		}
	}
	return slots
}

var weekdays = [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

func slotsOverview(slots []string) (string, error) {
	type daySlots struct {
		day   time.Time
		hours []int
	}
	byDate := make(map[string]*daySlots)
	var order []string
	for _, raw := range slots {
		dt, err := time.ParseInLocation("2006-01-02 15:04:05", raw, shanghai)
		if err != nil || dt.Minute() != 0 || dt.Second() != 0 {
			return "", errors.New("invalidInterviewSlot")
		}
		key := dt.Format("2006-01-02")
		entry := byDate[key]
		if entry == nil {
			entry = &daySlots{day: dt}
			byDate[key] = entry
			order = append(order, key)
		}
		entry.hours = append(entry.hours, dt.Hour())
	}
	sort.Strings(order)
	lines := make([]string, 0, len(order))
	for _, key := range order {
		entry := byDate[key]
		sort.Ints(entry.hours)
		unique := entry.hours[:0]
		for _, hour := range entry.hours {
			if len(unique) == 0 || unique[len(unique)-1] != hour {
				unique = append(unique, hour)
			}
		}
		parts := make([]string, 0)
		for startIndex := 0; startIndex < len(unique); {
			endIndex := startIndex
			for endIndex+1 < len(unique) && unique[endIndex+1] == unique[endIndex]+1 {
				endIndex++
			}
			if endIndex == startIndex {
				parts = append(parts, fmt.Sprintf("%02d:00", unique[startIndex]))
			} else {
				parts = append(parts, fmt.Sprintf("%02d:00-%02d:00", unique[startIndex], unique[endIndex]))
			}
			startIndex = endIndex + 1
		}
		lines = append(lines, fmt.Sprintf("%d月%d日(%s) %s 的整点",
			entry.day.Month(), entry.day.Day(), weekdays[entry.day.Weekday()], strings.Join(parts, "、")))
	}
	return strings.Join(lines, "\n"), nil
}

func slotDateLine(frozenNow time.Time) string {
	now := frozenNow.In(shanghai)
	return fmt.Sprintf("现在是%d年%d月%d日(%s)%s。",
		now.Year(), now.Month(), now.Day(), weekdays[now.Weekday()], now.Format("15:04"))
}

func slotsInlinePayload(frozenNow time.Time, slots []string) (string, error) {
	dateLine := slotDateLine(frozenNow)
	if len(slots) == 0 {
		return dateLine + "当前未配置可面试时间，不要主动承诺具体面试时间。", nil
	}
	overview, err := slotsOverview(slots)
	if err != nil {
		return "", err
	}
	return dateLine + slotFormatGuard + "\n" + overview, nil
}

func slotsBlock(frozenNow time.Time, slots []string) (string, error) {
	dateLine := slotDateLine(frozenNow)
	if len(slots) == 0 {
		return slotHeading + "\n" + dateLine + "当前未配置可面试时间，不要主动承诺具体面试时间。", nil
	}
	overview, err := slotsOverview(slots)
	if err != nil {
		return "", err
	}
	return slotHeading + "\n" + dateLine +
		"约面话术只能使用下列时间，不要编造其它面试时间；正文未规定怎么选时，优先最早的时段。\n" +
		slotFormatGuard + "\n" + overview, nil
}

func renderReplyTemplate(prompt, resumeJSON, history string, frozenNow time.Time, slots []string) (string, error) {
	hasOwnBlock := strings.Contains(prompt, slotHeading)
	matches := activeTokenPattern.FindAllStringSubmatchIndex(prompt, -1)
	anchor := -1
	if hasOwnBlock {
		anchor = strings.Index(prompt, slotHeading)
	}
	dataStart := -1
	if anchor >= 0 {
		for _, match := range matches {
			if prompt[match[2]:match[3]] == "推荐时段" && match[0] > anchor {
				dataStart = match[0]
				break
			}
		}
	}
	inline, err := slotsInlinePayload(frozenNow, slots)
	if err != nil {
		return "", err
	}
	var builder strings.Builder
	cursor := 0
	preAnchorDelta := 0
	for _, match := range matches {
		start, end := match[0], match[1]
		name := prompt[match[2]:match[3]]
		replacement := prompt[start:end]
		switch name {
		case "简历":
			replacement = resumeJSON
		case "对话历史":
			replacement = history
		case "推荐时段":
			replacement = slotPointerText
			if start == dataStart {
				replacement = inline
			}
		case "话术_序列":
			// This is the frozen output-example key, not an input token.
		default:
			return "", fmt.Errorf("unknownTemplateToken: %s", name)
		}
		builder.WriteString(prompt[cursor:start])
		builder.WriteString(replacement)
		cursor = end
		if start < anchor {
			preAnchorDelta += len(replacement) - (end - start)
		}
	}
	builder.WriteString(prompt[cursor:])
	rendered := builder.String()
	if anchor >= 0 && dataStart < 0 {
		insertAt := anchor + preAnchorDelta + len(slotHeading)
		rendered = rendered[:insertAt] + "\n" + inline + rendered[insertAt:]
	}
	if !hasOwnBlock {
		block, err := slotsBlock(frozenNow, slots)
		if err != nil {
			return "", err
		}
		rendered = strings.TrimRight(rendered, " \t\r\n") + "\n\n" + block
	}
	return rendered, nil
}

func RenderReplyPrompt(prompt, resumeJSON, history string, frozenNow time.Time, slots []string, customerFacts string) (string, error) {
	if err := requireInputTokens("多轮沟通", prompt, "简历", "推荐时段", "对话历史"); err != nil {
		return "", err
	}
	if resumeJSON == "" {
		return "", errors.New("missingTemplateValue: 简历")
	}
	rendered, err := renderReplyTemplate(prompt, resumeJSON, history, frozenNow, slots)
	if err != nil {
		return "", err
	}
	rendered = strings.TrimRight(rendered, " \t\r\n") + "\n\n" + customerFactsHeading + "\n" + customerFacts
	return rendered, nil
}

func RenderIntentPrompt(prompt, sentGreeting string, history, current []AdviceMessage) (string, string, error) {
	if err := requireInputTokens("意向判断", prompt, "回复", "招呼语"); err != nil {
		return "", "", err
	}
	turn, err := activeMessages(current)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(sentGreeting) == "" {
		return "", "", errors.New("missingSentGreeting")
	}
	if len(turn) == 0 {
		return "", "", errors.New("missingCurrentTurn")
	}
	for _, message := range turn {
		if message.Direction != "inbound" || message.Kind != "text" {
			return "", "", errors.New("invalidCurrentTurnKind")
		}
	}
	lastReply := turn[len(turn)-1].Text
	values := map[string]string{"招呼语": sentGreeting, "回复": lastReply}
	var builder strings.Builder
	cursor := 0
	for _, match := range activeTokenPattern.FindAllStringSubmatchIndex(prompt, -1) {
		name := prompt[match[2]:match[3]]
		value, ok := values[name]
		if !ok {
			return "", "", fmt.Errorf("unknownTemplateToken: %s", name)
		}
		builder.WriteString(prompt[cursor:match[0]])
		builder.WriteString(value)
		cursor = match[1]
	}
	builder.WriteString(prompt[cursor:])
	rendered := builder.String()
	envelope, err := BuildIntentEnvelope(history, turn)
	if err != nil {
		return "", "", err
	}
	content := rendered + "\n\n" + intentEnvelopeHeading + "\n" + envelope
	return content, envelope, nil
}

func ValidateSendText(text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("emptyReplyText")
	}
	if !utf8.ValidString(text) || len([]byte(text)) > SendTextMaxUTF8Bytes {
		return errors.New("sendTextLimit")
	}
	return nil
}
