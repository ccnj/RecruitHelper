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
	resumePointerText     = "简历(见【简历】)"
	resumeHeading         = "【简历】"
	historyPointerText    = "完整对话(见【完整对话】)"
	historyHeading        = "【完整对话】"
	// historyGuard 压的是模型复读自己上一轮话术的倾向:候选人只回"好的""我尽量快"
	// 这类没有新信息的短句时,历史末尾就摆着我方刚发的几句,模型顺手照抄一遍——
	// 2026-08-05 真机上侯先生就连收了两条一字不差的消息。
	//
	// 措辞经 100 次 × 3 组实测选定:"不许和历史相似度过高"这类说法基本无效(9→6,
	// 在 ±4 的噪声里),因为模型没有相似度的尺子;认准 RenderHistory 渲染出来的
	// "我(消息)" 前缀、要求逐句不得重出,才把复读压到 0/100。
	//
	// 注意耦合:这句话依赖 RenderHistory 的出站标签字面量。改那边的标签写法,这道
	// 软约束会静默失效且没有任何报错。它也只是软约束——0/100 不等于永不发生,发送
	// 前的确定性去重闸另案。
	historyGuard = `以下是已经发生的对话，只供你了解上下文。你这一轮要写的是新的话——` +
		`凡是“我(消息)”开头的句子都已经发过了，一句都不许再发一遍。`
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

// 模型只会写 X月X日HH:mm 这一族。真机 2026-08-04 观察到两种自然变体：小时不补
// 前导零（8月5日9:00），以及日期与时间之间带一个半角空格（8月5日 9:00）。两者与
// 规范写法语义完全相同，此前却被正则挡在门外——而提示词自己就把月、日渲染成一位
// 数（%d月%d日），只有小时用 Format("15:04") 出两位，模型照着类推写出一位小时是
// 必然的。格式不自洽是我们这边的问题，不该由候选人承担代价。
// 放宽的只有写法：时刻仍须精确命中本轮冻结时段，且必须唯一。全角空格、多个空格、
// 月份补零、带年份、自然语言一律仍然拒绝——只认已有真实证据的两种变体。
var meetingTimePattern = regexp.MustCompile(
	`^([1-9]|1[0-2])月([1-9]|[12][0-9]|3[01])日[ ]?([01]?[0-9]|2[0-3]):([0-5][0-9])$`,
)

// MatchFrozenRecommendedMeetingTime applies the deliberately narrow reply
// contract: trim the model value, require M月D日HH:mm (optionally without an
// hour-leading zero and with one separating space), and accept it only when
// exactly one canonical Shanghai slot denotes the same instant.
//
// 返回的时间戳恒取自命中的 slot，模型给的字符串只用于"挑中哪一个"，一个字符都
// 不参与算值；因此写法放宽不会影响下发给手的 startsAt。
func MatchFrozenRecommendedMeetingTime(slots []string, raw string) (int64, bool) {
	groups := meetingTimePattern.FindStringSubmatch(strings.TrimSpace(raw))
	if len(slots) == 0 || groups == nil {
		return 0, false
	}
	// 四段都已由正则约束为合法区间内的纯数字，转换不会失败。
	month, _ := strconv.Atoi(groups[1])
	day, _ := strconv.Atoi(groups[2])
	hour, _ := strconv.Atoi(groups[3])
	minute, _ := strconv.Atoi(groups[4])
	matchedAt := int64(0)
	matches := 0
	for _, rawSlot := range slots {
		slot, err := time.ParseInLocation("2006-01-02 15:04:05", rawSlot, shanghai)
		if err != nil || slot.Format("2006-01-02 15:04:05") != rawSlot ||
			slot.Minute() != 0 || slot.Second() != 0 {
			return 0, false
		}
		// 比时刻而不是比拼法：写法差异不得冒充语义越界，语义仍须逐字段精确命中。
		if int(slot.Month()) == month && slot.Day() == day &&
			slot.Hour() == hour && slot.Minute() == minute {
			matches++
			matchedAt = slot.UnixMilli()
		}
	}
	if matches != 1 {
		return 0, false
	}
	return matchedAt, true
}

// replyDataBlock 描述一块"大段输入数据"在提示词里的两种安放方式。模板里同一
// 个 token 常出现多次,其中大多数是名词性指代("从 {推荐时段} 里取"),只有一处
// 是真正的数据入口;无条件把每一处都替换成完整数据,等于把简历和整段对话在一份
// 提示词里塞进去两遍。
//
// 2026-08-05 甲方裁决:三个数据 token 共用同一套安放规则,不再各行其是。此前只有
// 推荐时段做了指代→指针的转换,简历与对话历史走无条件替换,真机上每次调用因此
// 多送约 1900 字符。
type replyDataBlock struct {
	token   string
	heading string
	pointer string
	inline  string // 模板自带块标题时,就地渲染进标题后的第一个占位符
	block   string // 模板不带块标题时,作为独立块追加到正文末尾(自带标题)
}

// replyDataBlocks 的切片顺序即末尾追加顺序。
func replyDataBlocks(resumeJSON, history string, frozen frozenRecommendedTimeText) []replyDataBlock {
	return []replyDataBlock{
		{token: "推荐时段", heading: slotHeading, pointer: slotPointerText,
			inline: frozen.Inline, block: frozen.Block},
		{token: "简历", heading: resumeHeading, pointer: resumePointerText,
			inline: resumeJSON, block: resumeHeading + "\n" + resumeJSON},
		// 与推荐时段同构:inline 与 block 都是"说明 + 数据",说明恒在数据之前。
		{token: "对话历史", heading: historyHeading, pointer: historyPointerText,
			inline: historyGuard + "\n" + history,
			block:  historyHeading + "\n" + historyGuard + "\n" + history},
	}
}

func renderReplyTemplateFrozen(prompt, resumeJSON, history string, frozen frozenRecommendedTimeText) (string, error) {
	blocks := replyDataBlocks(resumeJSON, history, frozen)
	matches := activeTokenPattern.FindAllStringSubmatchIndex(prompt, -1)

	// present 表示模板里引用过该 token:块的唯一意义是给指针提供落脚点,模板没
	// 引用就什么都不安排,不凭空塞一段数据进去。
	// anchor < 0 表示模板不带该块标题;dataStart < 0 表示标题在、但标题之后没有
	// 对应占位符。delta 累计"该标题之前"的替换带来的长度变化,用于把标题在渲染后
	// 文本里的位置算准——不能事后用 strings.Index 找标题,那会撞上指针文字里的
	// "(见【可约面时间】)"。
	type blockPlacement struct {
		present   bool
		anchor    int
		dataStart int
		delta     int
	}
	placements := make(map[string]*blockPlacement, len(blocks))
	byToken := make(map[string]replyDataBlock, len(blocks))
	for _, block := range blocks {
		byToken[block.token] = block
		placement := &blockPlacement{anchor: -1, dataStart: -1}
		if index := strings.Index(prompt, block.heading); index >= 0 {
			placement.anchor = index
		}
		for _, match := range matches {
			if prompt[match[2]:match[3]] != block.token {
				continue
			}
			placement.present = true
			if placement.dataStart < 0 && match[0] > placement.anchor && placement.anchor >= 0 {
				placement.dataStart = match[0]
			}
		}
		placements[block.token] = placement
	}

	var builder strings.Builder
	cursor := 0
	for _, match := range matches {
		start, end := match[0], match[1]
		name := prompt[match[2]:match[3]]
		replacement := prompt[start:end]
		if block, found := byToken[name]; found {
			replacement = block.pointer
			if placements[name].dataStart == start {
				replacement = block.inline
			}
		} else if name != "话术_序列" {
			// 话术_序列 is the frozen output-example key, not an input token.
			return "", fmt.Errorf("unknownTemplateToken: %s", name)
		}
		builder.WriteString(prompt[cursor:start])
		builder.WriteString(replacement)
		cursor = end
		for _, placement := range placements {
			if start < placement.anchor {
				placement.delta += len(replacement) - (end - start)
			}
		}
	}
	builder.WriteString(prompt[cursor:])
	rendered := builder.String()

	// 标题在、占位符不在:把数据插到标题正下方。多块时从后往前插,先插入的文本
	// 才不会推移还没插的那些块的落点。
	type blockInsert struct {
		at   int
		text string
	}
	inserts := make([]blockInsert, 0, len(blocks))
	for _, block := range blocks {
		placement := placements[block.token]
		if placement.present && placement.anchor >= 0 && placement.dataStart < 0 {
			inserts = append(inserts, blockInsert{
				at:   placement.anchor + placement.delta + len(block.heading),
				text: "\n" + block.inline,
			})
		}
	}
	sort.Slice(inserts, func(i, j int) bool { return inserts[i].at > inserts[j].at })
	for _, insert := range inserts {
		rendered = rendered[:insert.at] + insert.text + rendered[insert.at:]
	}

	for _, block := range blocks {
		if placement := placements[block.token]; placement.present && placement.anchor < 0 {
			rendered = strings.TrimRight(rendered, " \t\r\n") + "\n\n" + block.block
		}
	}
	return rendered, nil
}

func RenderReplyPrompt(prompt, resumeJSON, history string, frozenNow time.Time, slots []string) (string, error) {
	frozen, err := FreezeRecommendedTimeText(frozenNow, slots)
	if err != nil {
		return "", err
	}
	return RenderReplyPromptFrozen(prompt, resumeJSON, history, frozen)
}

// RenderReplyPromptFrozen assembles a reply from a DialogueTurn's persisted
// schedule text. It intentionally accepts no time.Time, so restart or delay
// cannot silently move the recommendation window.
//
// 客户事实库不再进提示词(2026-08-05 甲方裁决):职位配置的 replyPrompt 自己就带
// 一整段事实库,追加的 customerFacts 是同一批事实的另一个版本——真机那份 6399 字,
// 其中 23 条数字事实模板里一条不缺。字段本身仍从旧后台导入、仍存进
// job_ai_context_revisions 并参与导入一致性校验,只是不再渲染进提示词。
func RenderReplyPromptFrozen(prompt, resumeJSON, history, recommendedTimeText string) (string, error) {
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
	return renderReplyTemplateFrozen(prompt, resumeJSON, history, frozen)
}

const replyActionMenuHeading = "【本轮可选动作】"

// AppendReplyActionMenu 追加【本轮可选动作】块(规格 v4 §五「客户端渲染期
// 追加块」)。菜单由 communication.V4ReplyActionMenu 算出,与事后裁决同源。
//
// 块只做减法:只列本轮合法的枚举并给出禁止事项,不鼓励、不暗示模型去选任何
// 具体动作。这条不是洁癖——30 次真实样本实验里唯一那次整轮作废,正是被
// "发起线上会议(必须同时填会议时间)"这句写法诱导去选了邀面卡,而邀面卡是
// 候选人可见、不可逆的。措辞往"什么时候不该填"偏,不往"填这个"偏。
func AppendReplyActionMenu(rendered string, menu ReplyActionMenu) (string, error) {
	return appendV4ReplyPolicy(rendered, replyActionMenuBlock(menu))
}

func replyActionMenuBlock(menu ReplyActionMenu) string {
	lines := []string{
		replyActionMenuHeading,
		"「动作」字段本轮只允许填：",
		"· 无 —— 默认就填这个。抛出时段让他挑、他还没答应时，也填「无」。",
	}
	if menu.AllowStartMeeting {
		// 点名三种"不该填",但压到一行:实测块越长,后面的禁止句越压不住
		// (换微信误填随块长度 0→1→2 单调上升)。只说正面条件时又有 2/30 在
		// 对方尚未挑定时就发卡,而邀面卡是候选人可见、不可逆的。
		// 两种邀面动作合并成一行而不是各占一行:上面那条实测规律(块越长,后面的
		// 禁止句越压不住)对新增动作同样成立,能省一行就省一行。
		lines = append(lines,
			"· 发起线上会议 / 发起线下面试 —— 只有他自己说定了具体时间才填；你在问、"+
				"他没答、时间是你提的，都填「无」。他明确要到场面试才填线下，拿不准填线上。")
	}
	if menu.AllowInviteWechat {
		lines = append(lines,
			"· 发起换微信邀请 —— 一生一次，用掉就没有了；约面还推得动时不要动用它，也不要开场就要。")
	}
	// 同样是"本轮不能再邀请"，已经发出邀请与已经换到号是两件不同的事实。
	switch menu.WechatLine {
	case ReplyMenuWechatInvited:
		lines = append(lines,
			"本轮微信邀请已经发出、正等对方通过。不得填「发起换微信邀请」，话术里也不要再说「我把微信发你」这类话。")
	case ReplyMenuWechatRejected:
		lines = append(lines,
			"对方已经拒绝了换微信邀请。不得填「发起换微信邀请」，话术里也绝对不要再提加微信、发号、通过这类话；他若自己主动提出换微信，按他的原话正常回应即可。")
	case ReplyMenuWechatExchanged:
		lines = append(lines,
			"本轮微信已经交换成功。不得填「发起换微信邀请」，话术里也不要出现「加个微信」「通过一下」这类说法。")
	}
	if menu.AllowStartMeeting {
		lines = append(lines,
			"话术里的时间一律写成「8月3日10:00」这种具体日期，不要用「明天」「后天」；【可约面时间】以外的时间一律不得出现。")
	}
	return strings.Join(lines, "\n")
}

// 现实边界块(2026-08-14 甲方裁决)。立案事故:回复模型在候选人三次坚持上门
// 后承诺"我下来接你",凭预训练世界知识编出事实库里没有的门牌"大连路688号宝
// 地广场A座",候选人真到场后又捏造"我到前台了""刚才咱们面聊得挺投缘"——把
// 真人骗到现场空等半小时。块文本用该事故全部关键轮的 ai-traces 真实请求逐轮
// 回放定稿(三版迭代,终版 12/12 样本零违规);回放同时抓出并堵掉了"发你定位"
// "你是不是记错时间了"与道歉轮追发换微信卡三个方差缺口。遵循
// AppendReplyActionMenu 的实测规律(块越长,后面的禁止句越压不住)已压到最短,
// 再加条款前先重跑回放。它是概率压制不是闸;出站词面硬闸另案立项。
const realityBoundaryPolicy = `【现实边界】
你在线上跟他打字，人不在任何现场：看不见他、接不了他，现场也没有人等他。本块盖过其余一切要求（包括“像本人在聊天”）；对话历史里你自己说过的话若与本块冲突，不许接着圆，按本块收场。
· 不许承诺或描述你的到场与动作：“下来接你”“我在前台”“我马上到”“在公司等你”这类话一个字不许出现。
· 地址、楼座、楼层、门牌只许逐字照抄【事实库】写了的，没写的不许自己补，也不许说“发你定位”。
· 没发生过的见面不许说成发生过。
· 他要当面聊：回“当面聊也可以，需要提前约个时间”，时间从【可约面时间】给二选一；他定下具体时间才填「发起线下面试」，不许答应“现在/今天就见”。
· 他说已经出发或已经到了，而今天并没有约好的见面：只道歉——“实在抱歉，让你白跑了，是我这边没安排好，今天见不了”——错认在自己身上，不许暗示他记错；这一轮不再约时间、不再讲卖点、不发任何邀请，动作填「无」。`

// realityBoundaryCompactPolicy 是招呼语与沉默追问出口的紧凑版:这两个提示词
// 没有【事实库】【可约面时间】与动作枚举,完整版的引用在那里落空反而添乱;
// 也没有会话上下文,"他说到了"类场景不可达,只保留三条通用禁令。
const realityBoundaryCompactPolicy = `【现实边界】
你在线上跟他打字，人不在任何现场。不许承诺或描述你的到场与动作（“下来接你”“我在前台”“我马上到”这类）；提示词里没写的地址、楼座、楼层、门牌不许自己编；没发生过的见面不许说成发生过。`

// AppendRealityBoundary 把现实边界块追加到已渲染的回复提示词末尾(动作菜单
// 之后)。位置与回放验证一致:块在最尾,权重最高。
func AppendRealityBoundary(rendered string) (string, error) {
	return appendV4ReplyPolicy(rendered, realityBoundaryPolicy)
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
