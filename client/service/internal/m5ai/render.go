package m5ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
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
	"沉默追问": {"姓名": "input", "年龄": "input", "性别": "input", "简历": "input"},
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

// InterviewWindow 是周表里的一段可面试窗口，起止都是 'HH:MM' 整点，右开区间。
// 09:00-18:00 表示 09、10 … 17 九个整点，不含 18:00。
type InterviewWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// InterviewSchedule 是按星期循环的可面试时段周表，key 取 InterviewWeekdays 里的
// 中文星期名。周表本身不含日期——具体候选时段由 GenerateSlots 在冻结时刻展开。
type InterviewSchedule map[string][]InterviewWindow

// InterviewWeekdays 是周表的合法 key，顺序即展示顺序。
var InterviewWeekdays = [...]string{"周一", "周二", "周三", "周四", "周五", "周六", "周日"}

// interviewWeekdayByGoWeekday 把 time.Weekday 映射到周表 key。time.Sunday 是 0，
// 而周表以周一起头，所以不能直接拿 int 当下标。
var interviewWeekdayByGoWeekday = map[time.Weekday]string{
	time.Monday: "周一", time.Tuesday: "周二", time.Wednesday: "周三",
	time.Thursday: "周四", time.Friday: "周五",
	time.Saturday: "周六", time.Sunday: "周日",
}

// DefaultInterviewSchedule 是没有任何本机配置时的内置周表：七天全部
// [09:00,18:00)（2026-08-01 甲方裁决，此前为周一至周五、周末空）。
//
// 招聘沟通里周末本就是可约面的，剔除周末等于默认少给两天可选。改动只影响
// 未配置的客户端：它们升级后会开始把周末排进推荐时段。已配置的客户端读自己
// 的周表，不受影响；已冻结的 turn 与已约出去的面试同样不受影响。
func DefaultInterviewSchedule() InterviewSchedule {
	schedule := make(InterviewSchedule, len(InterviewWeekdays))
	for _, day := range InterviewWeekdays {
		schedule[day] = []InterviewWindow{{Start: "09:00", End: "18:00"}}
	}
	return schedule
}

// ValidateInterviewSchedule 校验周表可用于展开。空表被拒——甲方裁决要求至少保留
// 一个时段，且该校验必须由脑侧把关，不能只靠 UI。
func ValidateInterviewSchedule(schedule InterviewSchedule) error {
	hours := 0
	for day, windows := range schedule {
		if _, ok := interviewWeekdayIndex(day); !ok {
			return fmt.Errorf("非法星期: %q", day)
		}
		for _, window := range windows {
			start, err := parseInterviewClock(window.Start)
			if err != nil {
				return err
			}
			end, err := parseInterviewClock(window.End)
			if err != nil {
				return err
			}
			if start >= end {
				return fmt.Errorf("%s 起止非法: %s >= %s", day, window.Start, window.End)
			}
			hours += end - start
		}
	}
	if hours == 0 {
		return errors.New("可面试时段不得为空")
	}
	return nil
}

func interviewWeekdayIndex(day string) (int, bool) {
	for index, known := range InterviewWeekdays {
		if known == day {
			return index, true
		}
	}
	return 0, false
}

// parseInterviewClock 解析 'HH:MM' 并返回小时数。面试时段一律整点对齐——
// 下游 MatchFrozenRecommendedMeetingTime 与槽位解析都假定 Minute()==0。
func parseInterviewClock(value string) (int, error) {
	if len(value) != 5 || value[2] != ':' {
		return 0, fmt.Errorf("时间格式必须是 HH:MM: %q", value)
	}
	hour, err := strconv.Atoi(value[0:2])
	if err != nil {
		return 0, fmt.Errorf("时间格式必须是 HH:MM: %q", value)
	}
	minute, err := strconv.Atoi(value[3:5])
	if err != nil {
		return 0, fmt.Errorf("时间格式必须是 HH:MM: %q", value)
	}
	if hour < 0 || hour > 24 || minute != 0 {
		return 0, fmt.Errorf("面试时间必须是整点小时: %q", value)
	}
	return hour, nil
}

// GenerateDefaultSlots 按内置默认周表展开，等价于 GenerateSlots(now,
// DefaultInterviewSchedule())。保留它是为了让既有测试继续钉住"未配置时行为不变"。
func GenerateDefaultSlots(frozenNow time.Time) []string {
	return GenerateSlots(frozenNow, DefaultInterviewSchedule())
}

// GenerateSlots 把周表展开成冻结时刻起 14 个日历日内的候选时段。当天早于冻结时刻
// 的整点被丢弃；周表非法或全空时返回空列表，下游据此不承诺任何面试时间。
func GenerateSlots(frozenNow time.Time, schedule InterviewSchedule) []string {
	now := frozenNow.In(shanghai)
	startDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, shanghai)
	var slots []string
	for offset := 0; offset <= 13; offset++ {
		day := startDay.AddDate(0, 0, offset)
		windows := schedule[interviewWeekdayByGoWeekday[day.Weekday()]]
		hours := make(map[int]struct{}, len(windows)*4)
		for _, window := range windows {
			start, err := parseInterviewClock(window.Start)
			if err != nil {
				continue
			}
			end, err := parseInterviewClock(window.End)
			if err != nil || start >= end {
				continue
			}
			for hour := start; hour < end && hour < 24; hour++ {
				hours[hour] = struct{}{}
			}
		}
		// 同一天的窗口允许重叠，展开后按整点去重并升序，保证时段列表本身有序去重。
		for hour := 0; hour < 24; hour++ {
			if _, selected := hours[hour]; !selected {
				continue
			}
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

type frozenRecommendedTimeText struct {
	Inline string   `json:"inline"`
	Block  string   `json:"block"`
	Slots  []string `json:"slots,omitempty"`
}

// FreezeRecommendedTimeText freezes both approved schedule placements because
// the imported prompt may already contain its own 【可约面时间】 block. The
// canonical JSON is an internal representation of the exact text fragments;
// DialogueTurn persists it once and later rendering never consults wall clock.
func FreezeRecommendedTimeText(frozenNow time.Time, slots []string) (string, error) {
	inline, err := slotsInlinePayload(frozenNow, slots)
	if err != nil {
		return "", err
	}
	block, err := slotsBlock(frozenNow, slots)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(frozenRecommendedTimeText{
		Inline: inline,
		Block:  block,
		Slots:  append([]string(nil), slots...),
	})
	return string(raw), err
}

func decodeFrozenRecommendedTimeText(raw string) (frozenRecommendedTimeText, error) {
	var frozen frozenRecommendedTimeText
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&frozen); err != nil || frozen.Inline == "" || frozen.Block == "" {
		return frozenRecommendedTimeText{}, errors.New("invalidFrozenRecommendedTimeText")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return frozenRecommendedTimeText{}, errors.New("invalidFrozenRecommendedTimeText")
	}
	for _, rawSlot := range frozen.Slots {
		slot, err := time.ParseInLocation("2006-01-02 15:04:05", rawSlot, shanghai)
		if err != nil || slot.Format("2006-01-02 15:04:05") != rawSlot ||
			slot.Minute() != 0 || slot.Second() != 0 {
			return frozenRecommendedTimeText{}, errors.New("invalidFrozenRecommendedTimeText")
		}
	}
	return frozen, nil
}

// FrozenRecommendedSlots returns the canonical slots carried by a newly
// frozen turn. Legacy payloads that only contain rendered text remain valid
// for prompt replay but deliberately report no action-authorizing slot list.
func FrozenRecommendedSlots(raw string) ([]string, bool) {
	frozen, err := decodeFrozenRecommendedTimeText(raw)
	if err != nil || len(frozen.Slots) == 0 {
		return nil, false
	}
	return append([]string(nil), frozen.Slots...), true
}

var meetingTimePattern = regexp.MustCompile(
	`^(?:[1-9]|1[0-2])月(?:[1-9]|[12][0-9]|3[01])日(?:[01][0-9]|2[0-3]):[0-5][0-9]$`,
)

// MatchFrozenRecommendedMeetingTime applies the deliberately narrow reply
// contract: trim the model value, require M月D日HH:mm, and accept it only when
// exactly one canonical Shanghai slot renders to the same value.
func MatchFrozenRecommendedMeetingTime(slots []string, raw string) (int64, bool) {
	meetingTime := strings.TrimSpace(raw)
	if len(slots) == 0 || !meetingTimePattern.MatchString(meetingTime) {
		return 0, false
	}
	matchedAt := int64(0)
	matches := 0
	for _, rawSlot := range slots {
		slot, err := time.ParseInLocation("2006-01-02 15:04:05", rawSlot, shanghai)
		if err != nil || slot.Format("2006-01-02 15:04:05") != rawSlot ||
			slot.Minute() != 0 || slot.Second() != 0 {
			return 0, false
		}
		rendered := fmt.Sprintf(
			"%d月%d日%s",
			slot.Month(),
			slot.Day(),
			slot.Format("15:04"),
		)
		if rendered == meetingTime {
			matches++
			matchedAt = slot.UnixMilli()
		}
	}
	if matches != 1 {
		return 0, false
	}
	return matchedAt, true
}

func renderReplyTemplateFrozen(prompt, resumeJSON, history string, frozen frozenRecommendedTimeText) (string, error) {
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
				replacement = frozen.Inline
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
		rendered = rendered[:insertAt] + "\n" + frozen.Inline + rendered[insertAt:]
	}
	if !hasOwnBlock {
		rendered = strings.TrimRight(rendered, " \t\r\n") + "\n\n" + frozen.Block
	}
	return rendered, nil
}

func RenderReplyPrompt(prompt, resumeJSON, history string, frozenNow time.Time, slots []string, customerFacts string) (string, error) {
	frozen, err := FreezeRecommendedTimeText(frozenNow, slots)
	if err != nil {
		return "", err
	}
	return RenderReplyPromptFrozen(prompt, resumeJSON, history, frozen, customerFacts)
}

// RenderReplyPromptFrozen assembles a reply from a DialogueTurn's persisted
// schedule text. It intentionally accepts no time.Time, so restart or delay
// cannot silently move the recommendation window.
func RenderReplyPromptFrozen(prompt, resumeJSON, history, recommendedTimeText, customerFacts string) (string, error) {
	if err := requireInputTokens("多轮沟通", prompt, "简历", "推荐时段", "对话历史"); err != nil {
		return "", err
	}
	if resumeJSON == "" {
		return "", errors.New("missingTemplateValue: 简历")
	}
	frozen, err := decodeFrozenRecommendedTimeText(recommendedTimeText)
	if err != nil {
		return "", err
	}
	rendered, err := renderReplyTemplateFrozen(prompt, resumeJSON, history, frozen)
	if err != nil {
		return "", err
	}
	rendered = strings.TrimRight(rendered, " \t\r\n") + "\n\n" + customerFactsHeading + "\n" + customerFacts
	return rendered, nil
}

const serviceReplyPolicy = `【当前服务阶段规则】
候选人已经接受面试，本轮目标是服务，不再推销职位或重新邀约。能直接回答的问题简短回答；涉及改期、取消、薪资细节或任何拿不准的信息，只引导候选人改到微信继续沟通。不得声称“已加上”“已通过”，也不得承诺“帮您反馈”“我去问下”或任何系统不会执行的后续动作。`

// AppendServiceReplyPolicy adds only deterministic business constraints
// already frozen in the v4 specification. It does not add candidate facts or
// grant the model any action authority.
func AppendServiceReplyPolicy(rendered string) (string, error) {
	return appendV4ReplyPolicy(rendered, serviceReplyPolicy)
}

func appendV4ReplyPolicy(rendered, policy string) (string, error) {
	rendered = strings.TrimSpace(rendered)
	if rendered == "" {
		return "", errors.New("missingRenderedReplyPrompt")
	}
	return rendered + "\n\n" + policy, nil
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
