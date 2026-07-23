package m5ai

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"recruithelper/contract/gen/go/protocol"
)

type formatGoldenCase struct {
	ID       string          `json:"id"`
	Input    json.RawMessage `json:"input"`
	Expected json.RawMessage `json:"expected"`
}

type formatGoldenSection struct {
	Cases                    []formatGoldenCase `json:"cases"`
	ProductionEnabledRuleIDs []string           `json:"productionEnabledRuleIds"`
	CandidateRules           struct {
		ResumeMarkers []struct {
			Literal             string   `json:"literal"`
			RequiresAllLiterals []string `json:"requiresAllLiterals"`
			ProductionEnabled   bool     `json:"productionEnabled"`
		} `json:"resumeMarkers"`
		RejectionRegex struct {
			ProductionEnabled bool `json:"productionEnabled"`
		} `json:"rejectionRegex"`
		ShortRejections []struct {
			Literal           string `json:"literal"`
			ProductionEnabled bool   `json:"productionEnabled"`
		} `json:"shortRejections"`
	} `json:"candidateRules"`
}

type formatGoldenFixture struct {
	Constants struct {
		HistoryLimit                    int    `json:"historyLimit"`
		HistoryInboundCodePointLimit    int    `json:"historyInboundCodePointLimit"`
		HistoryOutboundCodePointLimit   int    `json:"historyOutboundCodePointLimit"`
		HistoryTruncationSuffix         string `json:"historyTruncationSuffix"`
		SendTextMaxUTF8Bytes            int    `json:"sendTextMaxUtf8Bytes"`
		ResumeDataMaxUTF8BytesInclusive int    `json:"resumeDataMaxUtf8BytesInclusive"`
	} `json:"constants"`
	Sections map[string]formatGoldenSection `json:"sections"`
}

func loadFormatGoldenFixture(t *testing.T) formatGoldenFixture {
	t.Helper()
	var fixture formatGoldenFixture
	readFixture(t, "m5-batch0b-format-goldens.v1.json", &fixture)
	if fixture.Constants.HistoryLimit != HistoryLimit ||
		fixture.Constants.SendTextMaxUTF8Bytes != SendTextMaxUTF8Bytes ||
		fixture.Constants.HistoryTruncationSuffix != historyTruncateSuffix {
		t.Fatalf("格式常量与实现漂移: %+v", fixture.Constants)
	}
	return fixture
}

func decodeFormatFixture[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func formatCaseByID(t *testing.T, section formatGoldenSection, id string) formatGoldenCase {
	t.Helper()
	for _, testCase := range section.Cases {
		if testCase.ID == id {
			return testCase
		}
	}
	t.Fatalf("fixture 缺少 case: %s", id)
	return formatGoldenCase{}
}

func requireFixtureError(t *testing.T, err error, errorClass string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), errorClass) {
		t.Fatalf("错误分类漂移: got=%v want~=%s", err, errorClass)
	}
}

func sameFixtureStrings(t *testing.T, got, want []string) {
	t.Helper()
	gotRaw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantRaw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotRaw) != string(wantRaw) {
		t.Fatalf("字符串序列漂移: got=%s want=%s", gotRaw, wantRaw)
	}
}

func TestFormatFixtureDrivesPromptTokenWhitelist(t *testing.T) {
	fixture := loadFormatGoldenFixture(t)
	section := fixture.Sections["promptTokens"]
	if len(section.Cases) != 5 {
		t.Fatalf("prompt token fixture case 数漂移: %d", len(section.Cases))
	}
	for _, testCase := range section.Cases {
		t.Run(testCase.ID, func(t *testing.T) {
			input := decodeFormatFixture[struct {
				DocType string `json:"docType"`
				Text    string `json:"text"`
			}](t, testCase.Input)
			expected := decodeFormatFixture[struct {
				Status            string   `json:"status"`
				ActiveInputTokens []string `json:"activeInputTokens"`
				ErrorClass        string   `json:"errorClass"`
			}](t, testCase.Expected)
			got, err := ValidatePromptTokens(input.DocType, input.Text)
			if expected.Status == "rejected" {
				requireFixtureError(t, err, expected.ErrorClass)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			// Token 顺序不是契约，fixture 与 production 均按集合比较。
			sortFixtureStrings(got)
			sortFixtureStrings(expected.ActiveInputTokens)
			sameFixtureStrings(t, got, expected.ActiveInputTokens)
		})
	}
}

func sortFixtureStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

type fixtureResumeInput struct {
	ConversationRef          string                               `json:"conversationRef"`
	PlatformUserRef          string                               `json:"platformUserRef"`
	ObservedAt               int64                                `json:"observedAt"`
	Basic                    []protocol.CandidateResumeLabelValue `json:"basic"`
	Expectations             []protocol.CandidateResumeLabelValue `json:"expectations"`
	SelfEvaluation           string                               `json:"selfEvaluation"`
	Education                string                               `json:"education"`
	WorkExperiences          string                               `json:"workExperiences"`
	FullyLoadedExplicitEmpty bool                                 `json:"fullyLoadedExplicitEmpty"`
}

type canonicalFixtureResume struct {
	ConversationRef string                               `json:"conversationRef"`
	PlatformUserRef string                               `json:"platformUserRef"`
	ObservedAt      int64                                `json:"observedAt"`
	Basic           []protocol.CandidateResumeLabelValue `json:"basic"`
	Expectations    []protocol.CandidateResumeLabelValue `json:"expectations"`
	SelfEvaluation  string                               `json:"selfEvaluation"`
	Education       string                               `json:"education"`
	WorkExperiences string                               `json:"workExperiences"`
}

func canonicalFixtureResumeFrom(input fixtureResumeInput) canonicalFixtureResume {
	return canonicalFixtureResume{
		ConversationRef: input.ConversationRef, PlatformUserRef: input.PlatformUserRef,
		ObservedAt: input.ObservedAt, Basic: input.Basic, Expectations: input.Expectations,
		SelfEvaluation: input.SelfEvaluation, Education: input.Education,
		WorkExperiences: input.WorkExperiences,
	}
}

func resumeRendererInput(t *testing.T, input fixtureResumeInput) string {
	t.Helper()
	raw, err := json.Marshal(struct {
		Basic           []protocol.CandidateResumeLabelValue `json:"basic"`
		Expectations    []protocol.CandidateResumeLabelValue `json:"expectations"`
		SelfEvaluation  string                               `json:"selfEvaluation"`
		Education       string                               `json:"education"`
		WorkExperiences string                               `json:"workExperiences"`
	}{input.Basic, input.Expectations, input.SelfEvaluation, input.Education, input.WorkExperiences})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestFormatFixtureDrivesResumeCanonicalRendererAndPayloadLimit(t *testing.T) {
	fixture := loadFormatGoldenFixture(t)
	section := fixture.Sections["resumeDTO"]
	if fixture.Constants.ResumeDataMaxUTF8BytesInclusive != 65_536 || len(section.Cases) != 5 {
		t.Fatalf("resume fixture 常量或 case 数漂移: constants=%+v cases=%d", fixture.Constants, len(section.Cases))
	}
	baseCase := formatCaseByID(t, section, "all_five_sections_populated")
	baseInput := decodeFormatFixture[fixtureResumeInput](t, baseCase.Input)

	for _, testCase := range section.Cases {
		t.Run(testCase.ID, func(t *testing.T) {
			switch testCase.ID {
			case "all_five_sections_populated", "explicit_empty_sections_valid":
				input := decodeFormatFixture[fixtureResumeInput](t, testCase.Input)
				expected := decodeFormatFixture[struct {
					CanonicalJSON string `json:"canonicalJSON"`
				}](t, testCase.Expected)
				raw, err := json.Marshal(canonicalFixtureResumeFrom(input))
				if err != nil || string(raw) != expected.CanonicalJSON {
					t.Fatalf("简历 canonical 漂移: got=%s want=%s err=%v", raw, expected.CanonicalJSON, err)
				}
				if err := protocol.ValidatePrimitiveData(protocol.PrimCandidateReadResume, 1, raw); err != nil {
					t.Fatalf("简历协议 validator 拒绝 fixture: %v", err)
				}
				rendered, err := RenderResumeJSON(resumeRendererInput(t, input))
				if err != nil {
					t.Fatalf("五分区 renderer 拒绝 fixture: %v", err)
				}
				var sections map[string]json.RawMessage
				if json.Unmarshal([]byte(rendered), &sections) != nil || len(sections) != 5 {
					t.Fatalf("五分区 renderer 输出漂移: %s", rendered)
				}
			case "missing_section_invalid":
				input := decodeFormatFixture[struct {
					MissingKey string `json:"missingKey"`
				}](t, testCase.Input)
				expected := decodeFormatFixture[struct {
					ErrorClass string `json:"errorClass"`
				}](t, testCase.Expected)
				var protocolValue map[string]any
				baseRaw, _ := json.Marshal(canonicalFixtureResumeFrom(baseInput))
				if err := json.Unmarshal(baseRaw, &protocolValue); err != nil {
					t.Fatal(err)
				}
				delete(protocolValue, input.MissingKey)
				missingRaw, _ := json.Marshal(protocolValue)
				if err := protocol.ValidatePrimitiveData(protocol.PrimCandidateReadResume, 1, missingRaw); err == nil {
					t.Fatal("协议 validator 必须拒绝缺失五分区")
				}
				var rendererValue map[string]any
				if err := json.Unmarshal([]byte(resumeRendererInput(t, baseInput)), &rendererValue); err != nil {
					t.Fatal(err)
				}
				delete(rendererValue, input.MissingKey)
				rendererRaw, _ := json.Marshal(rendererValue)
				_, err := RenderResumeJSON(string(rendererRaw))
				requireFixtureError(t, err, expected.ErrorClass)
			case "unreadable_not_empty":
				// “读不到”与“明确空”的判定发生在手端 MAIN 页面 evaluator，
				// 不属于 Go renderer/validator 输入。对应生产证据是 plugin/test/unit.mjs
				// 的“candidate.readResume MAIN 对旧弹窗、换绑与缺区整体失败且不点击”。
				t.Skip("手端页面事实；由 plugin candidate.readResume MAIN handler 测试覆盖")
			case "aggregate_payload_boundary":
				input := decodeFormatFixture[struct {
					CanonicalUTF8ByteCases []int `json:"canonicalUtf8ByteCases"`
				}](t, testCase.Input)
				for index, size := range input.CanonicalUTF8ByteCases {
					value := canonicalFixtureResumeFrom(baseInput)
					value.Basic = []protocol.CandidateResumeLabelValue{}
					value.Expectations = []protocol.CandidateResumeLabelValue{}
					value.Education = ""
					value.WorkExperiences = ""
					value.SelfEvaluation = ""
					baseRaw, _ := json.Marshal(value)
					value.SelfEvaluation = strings.Repeat("a", size-len(baseRaw))
					raw, _ := json.Marshal(value)
					if len(raw) != size {
						t.Fatalf("payload fixture 构造长度=%d want=%d", len(raw), size)
					}
					err := protocol.ValidatePrimitiveData(protocol.PrimCandidateReadResume, 1, raw)
					if index == 0 && err != nil {
						t.Fatalf("64KB 边界应通过: %v", err)
					}
					if index == 1 && err == nil {
						t.Fatal("64KB+1 边界应被 production validator 拒绝")
					}
				}
			default:
				t.Fatalf("未消费的 resume fixture case: %s", testCase.ID)
			}
		})
	}
}

func TestFormatFixtureDrivesEveryHistoryGolden(t *testing.T) {
	fixture := loadFormatGoldenFixture(t)
	section := fixture.Sections["history"]
	for _, testCase := range section.Cases {
		t.Run(testCase.ID, func(t *testing.T) {
			switch testCase.ID {
			case "roles_order_blank_retracted", "empty_history":
				input := decodeFormatFixture[struct {
					Messages []AdviceMessage `json:"messages"`
				}](t, testCase.Input)
				expected := decodeFormatFixture[struct {
					Rendered string `json:"rendered"`
				}](t, testCase.Expected)
				got, err := RenderHistory(input.Messages)
				if err != nil || got != expected.Rendered {
					t.Fatalf("history fixture 漂移: got=%q want=%q err=%v", got, expected.Rendered, err)
				}
			case "last_20_of_21":
				input := decodeFormatFixture[struct {
					MessageSeqRangeInclusive []int64 `json:"messageSeqRangeInclusive"`
				}](t, testCase.Input)
				expected := decodeFormatFixture[struct {
					SelectedSeqRangeInclusive []int64 `json:"selectedSeqRangeInclusive"`
					SelectedCount             int     `json:"selectedCount"`
				}](t, testCase.Expected)
				messages := sequentialFixtureMessages(input.MessageSeqRangeInclusive[0], input.MessageSeqRangeInclusive[1])
				selected, err := LatestHistory(messages)
				if err != nil || len(selected) != expected.SelectedCount || selected[0].Seq != expected.SelectedSeqRangeInclusive[0] || selected[len(selected)-1].Seq != expected.SelectedSeqRangeInclusive[1] {
					t.Fatalf("history 20/21 窗口漂移: selected=%+v err=%v", selected, err)
				}
				if rendered, err := RenderHistory(messages); err != nil || len(strings.Split(rendered, "\n")) != expected.SelectedCount {
					t.Fatalf("history renderer 未消费 20/21 fixture: lines=%d err=%v", len(strings.Split(rendered, "\n")), err)
				}
			case "inbound_1000_and_1001", "outbound_300_and_301":
				input := decodeFormatFixture[struct {
					Cases []struct {
						Repeat     string `json:"repeat"`
						CodePoints int    `json:"codePoints"`
					} `json:"cases"`
				}](t, testCase.Input)
				expected := decodeFormatFixture[struct {
					FirstTruncated         bool   `json:"firstTruncated"`
					SecondPrefixCodePoints int    `json:"secondPrefixCodePoints"`
					SecondSuffix           string `json:"secondSuffix"`
				}](t, testCase.Expected)
				direction, label := "inbound", "候选人(消息):"
				if testCase.ID == "outbound_300_and_301" {
					direction, label = "outbound", "我(消息):"
				}
				for index, boundary := range input.Cases {
					got, err := RenderHistory([]AdviceMessage{{Seq: 1, Direction: direction, Kind: "text", Text: strings.Repeat(boundary.Repeat, boundary.CodePoints)}})
					if err != nil {
						t.Fatal(err)
					}
					body := strings.TrimPrefix(got, label)
					if index == 0 && strings.HasSuffix(body, expected.SecondSuffix) != expected.FirstTruncated {
						t.Fatalf("边界首项截断口径漂移: %q", body)
					}
					if index == 1 {
						prefix := strings.TrimSuffix(body, expected.SecondSuffix)
						if !strings.HasSuffix(body, expected.SecondSuffix) || utf8.RuneCountInString(prefix) != expected.SecondPrefixCodePoints {
							t.Fatalf("边界次项截断口径漂移: runes=%d body=%q", utf8.RuneCountInString(prefix), body)
						}
					}
				}
			case "restart_same_bytes":
				input := decodeFormatFixture[struct {
					FixtureRef string `json:"fixtureRef"`
					Runs       int    `json:"runs"`
				}](t, testCase.Input)
				referenced := formatCaseByID(t, section, input.FixtureRef)
				referenceInput := decodeFormatFixture[struct {
					Messages []AdviceMessage `json:"messages"`
				}](t, referenced.Input)
				var first string
				for run := 0; run < input.Runs; run++ {
					got, err := RenderHistory(referenceInput.Messages)
					if err != nil {
						t.Fatal(err)
					}
					if run == 0 {
						first = got
					} else if got != first {
						t.Fatal("restart 后 history bytes 漂移")
					}
				}
			default:
				t.Fatalf("未消费的 history fixture case: %s", testCase.ID)
			}
		})
	}
}

func sequentialFixtureMessages(first, last int64) []AdviceMessage {
	messages := make([]AdviceMessage, 0, last-first+1)
	for seq := first; seq <= last; seq++ {
		messages = append(messages, AdviceMessage{Seq: seq, Direction: "inbound", Kind: "text", Text: "消息" + strconv.FormatInt(seq, 10)})
	}
	return messages
}

type scheduleFixtureInput struct {
	FrozenNow           string   `json:"frozenNow"`
	SourcePrompt        string   `json:"sourcePrompt"`
	SelectedSlots       []string `json:"selectedSlots"`
	FixtureRef          string   `json:"fixtureRef"`
	RuntimeAfterRestart string   `json:"runtimeNowAfterRestart"`
}

func renderScheduleFixture(t *testing.T, input scheduleFixtureInput) string {
	t.Helper()
	now, err := time.Parse(time.RFC3339, input.FrozenNow)
	if err != nil {
		t.Fatal(err)
	}
	frozenRaw, err := FreezeRecommendedTimeText(now, input.SelectedSlots)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := decodeFrozenRecommendedTimeText(frozenRaw)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := renderReplyTemplateFrozen(input.SourcePrompt, "", "", frozen)
	if err != nil {
		t.Fatal(err)
	}
	return rendered
}

func TestFormatFixtureDrivesEveryScheduleGolden(t *testing.T) {
	fixture := loadFormatGoldenFixture(t)
	section := fixture.Sections["schedule"]
	for _, testCase := range section.Cases {
		t.Run(testCase.ID, func(t *testing.T) {
			switch testCase.ID {
			case "default_generation_friday_and_fourteen_day_boundary":
				input := decodeFormatFixture[scheduleFixtureInput](t, testCase.Input)
				expected := decodeFormatFixture[struct {
					SlotCount           int    `json:"slotCount"`
					FirstSlot           string `json:"firstSlot"`
					LastSlot            string `json:"lastSlot"`
					WeekendSlotCount    int    `json:"weekendSlotCount"`
					DayOffset14Included bool   `json:"dayOffset14Included"`
				}](t, testCase.Expected)
				now, err := time.Parse(time.RFC3339, input.FrozenNow)
				if err != nil {
					t.Fatal(err)
				}
				slots := GenerateDefaultSlots(now)
				if len(slots) != expected.SlotCount || slots[0] != expected.FirstSlot || slots[len(slots)-1] != expected.LastSlot {
					t.Fatalf("默认时段漂移: count=%d first=%s last=%s", len(slots), slots[0], slots[len(slots)-1])
				}
				weekendCount := 0
				offset14Day := now.In(shanghai).AddDate(0, 0, 14).Format("2006-01-02")
				offset14Included := false
				for _, slot := range slots {
					parsed, err := time.ParseInLocation("2006-01-02 15:04:05", slot, shanghai)
					if err != nil {
						t.Fatal(err)
					}
					if parsed.Weekday() == time.Saturday || parsed.Weekday() == time.Sunday {
						weekendCount++
					}
					if strings.HasPrefix(slot, offset14Day+" ") {
						offset14Included = true
					}
				}
				if weekendCount != expected.WeekendSlotCount || offset14Included != expected.DayOffset14Included {
					t.Fatalf("默认时段边界漂移: weekend=%d offset14=%v", weekendCount, offset14Included)
				}
			case "weekday_exact_hour_boundary":
				input := decodeFormatFixture[scheduleFixtureInput](t, testCase.Input)
				expected := decodeFormatFixture[struct {
					FirstSlot          string `json:"firstSlot"`
					NineOClockIncluded bool   `json:"nineOClockIncluded"`
					TenOClockIncluded  bool   `json:"tenOClockIncluded"`
				}](t, testCase.Expected)
				now, err := time.Parse(time.RFC3339, input.FrozenNow)
				if err != nil {
					t.Fatal(err)
				}
				slots := GenerateDefaultSlots(now)
				nine := containsFixtureString(slots, "2026-07-13 09:00:00")
				ten := containsFixtureString(slots, "2026-07-13 10:00:00")
				if len(slots) == 0 || slots[0] != expected.FirstSlot || nine != expected.NineOClockIncluded || ten != expected.TenOClockIncluded {
					t.Fatalf("整点边界漂移: first=%q nine=%v ten=%v", slots[0], nine, ten)
				}
			case "no_time_block_multiple_tokens", "existing_block_tokens_inside_and_outside", "empty_slots_block":
				input := decodeFormatFixture[scheduleFixtureInput](t, testCase.Input)
				expected := decodeFormatFixture[struct {
					RenderedPrompt string `json:"renderedPrompt"`
				}](t, testCase.Expected)
				if got := renderScheduleFixture(t, input); got != expected.RenderedPrompt {
					t.Fatalf("时段 renderer 漂移:\n got=%q\nwant=%q", got, expected.RenderedPrompt)
				}
			case "frozen_clock_restart":
				input := decodeFormatFixture[scheduleFixtureInput](t, testCase.Input)
				referenced := formatCaseByID(t, section, input.FixtureRef)
				referenceInput := decodeFormatFixture[scheduleFixtureInput](t, referenced.Input)
				first := renderScheduleFixture(t, referenceInput)
				if _, err := time.Parse(time.RFC3339, input.RuntimeAfterRestart); err != nil {
					t.Fatal(err)
				}
				second := renderScheduleFixture(t, referenceInput)
				if first != second {
					t.Fatal("restart 后冻结时段 bytes 漂移")
				}
			default:
				t.Fatalf("未消费的 schedule fixture case: %s", testCase.ID)
			}
		})
	}
}

func containsFixtureString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

type providerAssemblyFixtureInput struct {
	Purpose        string `json:"purpose"`
	FrozenNow      string `json:"frozenNow"`
	SourcePrompt   string `json:"sourcePrompt"`
	TemplateValues struct {
		Resume  string `json:"简历"`
		History string `json:"对话历史"`
	} `json:"templateValues"`
	SelectedSlots     []string        `json:"selectedSlots"`
	CustomerFacts     string          `json:"customerFacts"`
	SentGreeting      string          `json:"sentGreeting"`
	CurrentTurn       []AdviceMessage `json:"currentTurn"`
	HistoryBeforeTurn []AdviceMessage `json:"historyBeforeTurn"`
	MissingValue      string          `json:"missingValue"`
	FixtureRef        string          `json:"fixtureRef"`
	Runs              int             `json:"runs"`
}

func TestFormatFixtureDrivesEveryProviderAssemblyGolden(t *testing.T) {
	fixture := loadFormatGoldenFixture(t)
	section := fixture.Sections["providerAssembly"]
	replyReference := decodeFormatFixture[providerAssemblyFixtureInput](t,
		formatCaseByID(t, section, "reply_all_values_and_customer_facts_once").Input)
	intentReference := decodeFormatFixture[providerAssemblyFixtureInput](t,
		formatCaseByID(t, section, "intent_prompt_and_envelope").Input)

	for _, testCase := range section.Cases {
		t.Run(testCase.ID, func(t *testing.T) {
			switch testCase.ID {
			case "reply_all_values_and_customer_facts_once":
				input := decodeFormatFixture[providerAssemblyFixtureInput](t, testCase.Input)
				expected := decodeFormatFixture[struct {
					ProviderMessages []struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"providerMessages"`
				}](t, testCase.Expected)
				now, err := time.Parse(time.RFC3339, input.FrozenNow)
				if err != nil {
					t.Fatal(err)
				}
				content, err := RenderReplyPrompt(input.SourcePrompt, input.TemplateValues.Resume,
					input.TemplateValues.History, now, input.SelectedSlots, input.CustomerFacts)
				if err != nil || len(expected.ProviderMessages) != 1 || expected.ProviderMessages[0].Role != "user" || content != expected.ProviderMessages[0].Content {
					t.Fatalf("reply provider assembly 漂移: content=%q err=%v", content, err)
				}
			case "intent_prompt_and_envelope":
				input := decodeFormatFixture[providerAssemblyFixtureInput](t, testCase.Input)
				expected := decodeFormatFixture[struct {
					RenderedPrompt   string `json:"renderedPrompt"`
					EnvelopeJSON     string `json:"envelopeJSON"`
					ProviderMessages []struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"providerMessages"`
				}](t, testCase.Expected)
				content, envelope, err := RenderIntentPrompt(input.SourcePrompt, input.SentGreeting,
					input.HistoryBeforeTurn, input.CurrentTurn)
				prefix := strings.TrimSuffix(content, "\n\n"+intentEnvelopeHeading+"\n"+envelope)
				if err != nil || prefix != expected.RenderedPrompt || envelope != expected.EnvelopeJSON ||
					len(expected.ProviderMessages) != 1 || content != expected.ProviderMessages[0].Content {
					t.Fatalf("intent provider assembly 漂移: content=%q envelope=%q err=%v", content, envelope, err)
				}
			case "assembly_missing_required_value_fails":
				input := decodeFormatFixture[providerAssemblyFixtureInput](t, testCase.Input)
				expected := decodeFormatFixture[struct {
					ErrorClass string `json:"errorClass"`
				}](t, testCase.Expected)
				if input.MissingValue != "简历" {
					t.Fatalf("当前 production 必填值入口未覆盖 fixture 字段: %s", input.MissingValue)
				}
				now, err := time.Parse(time.RFC3339, replyReference.FrozenNow)
				if err != nil {
					t.Fatal(err)
				}
				_, err = RenderReplyPrompt(replyReference.SourcePrompt, "", replyReference.TemplateValues.History,
					now, replyReference.SelectedSlots, replyReference.CustomerFacts)
				requireFixtureError(t, err, expected.ErrorClass)
			case "assembly_unknown_token_fails":
				input := decodeFormatFixture[providerAssemblyFixtureInput](t, testCase.Input)
				expected := decodeFormatFixture[struct {
					ErrorClass string `json:"errorClass"`
				}](t, testCase.Expected)
				now, err := time.Parse(time.RFC3339, replyReference.FrozenNow)
				if err != nil {
					t.Fatal(err)
				}
				_, err = RenderReplyPrompt(input.SourcePrompt, replyReference.TemplateValues.Resume,
					replyReference.TemplateValues.History, now, replyReference.SelectedSlots, replyReference.CustomerFacts)
				requireFixtureError(t, err, expected.ErrorClass)
			case "canonical_assembly_bytes":
				input := decodeFormatFixture[providerAssemblyFixtureInput](t, testCase.Input)
				if input.FixtureRef != "intent_prompt_and_envelope" || input.Runs < 2 {
					t.Fatalf("未知 canonical fixtureRef: %s", input.FixtureRef)
				}
				var first, firstEnvelope string
				for run := 0; run < input.Runs; run++ {
					content, envelope, err := RenderIntentPrompt(intentReference.SourcePrompt, intentReference.SentGreeting,
						intentReference.HistoryBeforeTurn, intentReference.CurrentTurn)
					if err != nil {
						t.Fatal(err)
					}
					if run == 0 {
						first, firstEnvelope = content, envelope
					} else if content != first || envelope != firstEnvelope {
						t.Fatal("provider assembly bytes 非确定性")
					}
				}
			default:
				t.Fatalf("未消费的 provider assembly fixture case: %s", testCase.ID)
			}
		})
	}
}

func replyParserRaw(t *testing.T, input json.RawMessage) string {
	t.Helper()
	decoded := decodeFormatFixture[struct {
		RawJSON        string            `json:"rawJSON"`
		PhraseSequence []json.RawMessage `json:"phraseSequence"`
	}](t, input)
	if decoded.RawJSON != "" {
		return decoded.RawJSON
	}
	phrases := make([]string, 0, len(decoded.PhraseSequence))
	for _, raw := range decoded.PhraseSequence {
		var literal string
		if json.Unmarshal(raw, &literal) == nil {
			phrases = append(phrases, literal)
			continue
		}
		descriptor := decodeFormatFixture[struct {
			Repeat    string `json:"repeat"`
			UTF8Bytes int    `json:"utf8Bytes"`
		}](t, raw)
		if descriptor.Repeat == "" || descriptor.UTF8Bytes%len([]byte(descriptor.Repeat)) != 0 {
			t.Fatalf("无法按 UTF-8 bytes 展开 phrase descriptor: %+v", descriptor)
		}
		phrases = append(phrases, strings.Repeat(descriptor.Repeat,
			descriptor.UTF8Bytes/len([]byte(descriptor.Repeat))))
	}
	raw, err := json.Marshal(struct {
		PhraseSequence []string `json:"话术_序列"`
	}{PhraseSequence: phrases})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestFormatFixtureDrivesEveryReplyParserGolden(t *testing.T) {
	fixture := loadFormatGoldenFixture(t)
	section := fixture.Sections["replyParser"]
	for _, testCase := range section.Cases {
		t.Run(testCase.ID, func(t *testing.T) {
			// These two M5-A goldens intentionally asserted that action fields
			// were ignored. M5-B supersedes only that behavior with a closed
			// action vocabulary; the remaining batch-0B format cases stay live.
			if testCase.ID == "one_phrase_valid_action_ignored" {
				_, err := ParseReplySuggestion(replyParserRaw(t, testCase.Input))
				requireFixtureError(t, err, "invalidReplyAction")
				return
			}
			if testCase.ID == "nonempty_meeting_time_ignored" {
				_, err := ParseReplySuggestion(replyParserRaw(t, testCase.Input))
				requireFixtureError(t, err, "unexpectedMeetingTime")
				return
			}
			expected := decodeFormatFixture[struct {
				Status          string `json:"status"`
				Text            string `json:"text"`
				ErrorClass      string `json:"errorClass"`
				MergedUTF8Bytes int    `json:"mergedUtf8Bytes"`
			}](t, testCase.Expected)
			raw := replyParserRaw(t, testCase.Input)
			got, err := ParseReplySuggestion(raw)
			if expected.Status == "rejected" {
				requireFixtureError(t, err, expected.ErrorClass)
				return
			}
			if err != nil || got.Text != expected.Text && expected.Text != "" {
				t.Fatalf("reply parser 漂移: got=%q want=%q err=%v", got.Text, expected.Text, err)
			}
			if expected.MergedUTF8Bytes != 0 && len([]byte(got.Text)) != expected.MergedUTF8Bytes {
				t.Fatalf("reply parser 合并字节数=%d want=%d", len([]byte(got.Text)), expected.MergedUTF8Bytes)
			}
		})
	}
}

func TestFormatFixtureDrivesEveryIntentParserGolden(t *testing.T) {
	fixture := loadFormatGoldenFixture(t)
	section := fixture.Sections["intentParser"]
	for _, testCase := range section.Cases {
		t.Run(testCase.ID, func(t *testing.T) {
			input := decodeFormatFixture[struct {
				RawJSON string `json:"rawJSON"`
			}](t, testCase.Input)
			expected := decodeFormatFixture[struct {
				Status     string `json:"status"`
				Label      string `json:"label"`
				ErrorClass string `json:"errorClass"`
			}](t, testCase.Expected)
			got, err := ParseIntentSuggestion(input.RawJSON)
			if expected.Status == "rejected" {
				requireFixtureError(t, err, expected.ErrorClass)
				return
			}
			if err != nil || string(got.Label) != expected.Label {
				t.Fatalf("intent parser 漂移: got=%+v want=%s err=%v", got, expected.Label, err)
			}
		})
	}
}

func intentFixtureMessages(seqs []int64) []AdviceMessage {
	messages := make([]AdviceMessage, 0, len(seqs))
	for _, seq := range seqs {
		messages = append(messages, AdviceMessage{
			Seq: seq, Direction: "inbound", Kind: "text", Text: "消息" + strconv.FormatInt(seq, 10),
		})
	}
	return messages
}

func decodeIntentEnvelopeFixture(t *testing.T, raw string) IntentEnvelope {
	t.Helper()
	var envelope IntentEnvelope
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func messageSeqs(messages []AdviceMessage) []int64 {
	out := make([]int64, 0, len(messages))
	for _, message := range messages {
		out = append(out, message.Seq)
	}
	return out
}

func TestFormatFixtureDrivesEveryIntentEnvelopeGolden(t *testing.T) {
	fixture := loadFormatGoldenFixture(t)
	section := fixture.Sections["intentEnvelope"]
	for _, testCase := range section.Cases {
		t.Run(testCase.ID, func(t *testing.T) {
			switch testCase.ID {
			case "multi_message_current_turn_and_last_reply_token":
				input := decodeFormatFixture[struct {
					CurrentTurn []AdviceMessage `json:"currentTurn"`
				}](t, testCase.Input)
				expected := decodeFormatFixture[struct {
					EnvelopeCurrentTurnSeq []int64 `json:"envelopeCurrentTurnSeq"`
					ReplyTokenValue        string  `json:"replyTokenValue"`
				}](t, testCase.Expected)
				content, raw, err := RenderIntentPrompt("招呼={招呼语}\n回复={回复}", "你好", nil, input.CurrentTurn)
				if err != nil {
					t.Fatal(err)
				}
				envelope := decodeIntentEnvelopeFixture(t, raw)
				gotSeq := messageSeqs(envelope.CurrentTurn)
				wantSeq := expected.EnvelopeCurrentTurnSeq
				if string(mustFixtureJSON(t, gotSeq)) != string(mustFixtureJSON(t, wantSeq)) ||
					!strings.Contains(content, "回复="+expected.ReplyTokenValue) {
					t.Fatalf("多消息 intent envelope 漂移: content=%q seq=%v", content, gotSeq)
				}
			case "prior_history_20_of_21":
				input := decodeFormatFixture[struct {
					PriorSeqRangeInclusive []int64 `json:"priorSeqRangeInclusive"`
					CurrentTurnSeq         []int64 `json:"currentTurnSeq"`
				}](t, testCase.Input)
				expected := decodeFormatFixture[struct {
					HistorySeqRangeInclusive     []int64 `json:"historySeqRangeInclusive"`
					HistoryCount                 int     `json:"historyCount"`
					CurrentTurnDuplicatedHistory bool    `json:"currentTurnDuplicatedInHistory"`
				}](t, testCase.Expected)
				history := sequentialFixtureMessages(input.PriorSeqRangeInclusive[0], input.PriorSeqRangeInclusive[1])
				current := intentFixtureMessages(input.CurrentTurnSeq)
				raw, err := BuildIntentEnvelope(history, current)
				if err != nil {
					t.Fatal(err)
				}
				envelope := decodeIntentEnvelopeFixture(t, raw)
				duplicated := false
				for _, prior := range envelope.HistoryBeforeTurn {
					for _, turn := range envelope.CurrentTurn {
						duplicated = duplicated || prior.Seq == turn.Seq
					}
				}
				if len(envelope.HistoryBeforeTurn) != expected.HistoryCount ||
					envelope.HistoryBeforeTurn[0].Seq != expected.HistorySeqRangeInclusive[0] ||
					envelope.HistoryBeforeTurn[len(envelope.HistoryBeforeTurn)-1].Seq != expected.HistorySeqRangeInclusive[1] ||
					duplicated != expected.CurrentTurnDuplicatedHistory {
					t.Fatalf("intent 20/21 envelope 漂移: envelope=%s", raw)
				}
			case "retracted_blank_excluded":
				input := decodeFormatFixture[struct {
					Messages []struct {
						Seq       int64  `json:"seq"`
						Text      string `json:"text"`
						Retracted bool   `json:"retracted"`
					} `json:"messages"`
				}](t, testCase.Input)
				expected := decodeFormatFixture[struct {
					IncludedSeq []int64 `json:"includedSeq"`
				}](t, testCase.Expected)
				messages := make([]AdviceMessage, 0, len(input.Messages))
				for _, message := range input.Messages {
					messages = append(messages, AdviceMessage{Seq: message.Seq, Direction: "inbound", Kind: "text", Text: message.Text, Retracted: message.Retracted})
				}
				raw, err := BuildIntentEnvelope(messages, nil)
				if err != nil {
					t.Fatal(err)
				}
				envelope := decodeIntentEnvelopeFixture(t, raw)
				if string(mustFixtureJSON(t, messageSeqs(envelope.HistoryBeforeTurn))) != string(mustFixtureJSON(t, expected.IncludedSeq)) {
					t.Fatalf("retracted/blank envelope 过滤漂移: %s", raw)
				}
			case "greeting_uses_sent_message_only":
				input := decodeFormatFixture[struct {
					SentGreeting struct {
						Text string `json:"text"`
					} `json:"sentGreeting"`
					DraftGreeting  string `json:"draftGreeting"`
					FailedGreeting string `json:"failedGreeting"`
				}](t, testCase.Input)
				expected := decodeFormatFixture[struct {
					GreetingTokenValue string `json:"greetingTokenValue"`
				}](t, testCase.Expected)
				content, _, err := RenderIntentPrompt("招呼={招呼语}\n回复={回复}", input.SentGreeting.Text, nil,
					[]AdviceMessage{{Seq: 2, Direction: "inbound", Kind: "text", Text: "收到"}})
				if err != nil || !strings.Contains(content, "招呼="+expected.GreetingTokenValue) ||
					strings.Contains(content, input.DraftGreeting) || strings.Contains(content, input.FailedGreeting) {
					t.Fatalf("招呼事实选择漂移: content=%q err=%v", content, err)
				}
			case "draft_or_failed_greeting_missing_input":
				expected := decodeFormatFixture[struct {
					ErrorClass string `json:"errorClass"`
				}](t, testCase.Expected)
				_, _, err := RenderIntentPrompt("招呼={招呼语}\n回复={回复}", "", nil,
					[]AdviceMessage{{Seq: 2, Direction: "inbound", Kind: "text", Text: "收到"}})
				requireFixtureError(t, err, expected.ErrorClass)
			case "duplicate_seq_invalid":
				input := decodeFormatFixture[struct {
					HistorySeq     []int64 `json:"historySeq"`
					CurrentTurnSeq []int64 `json:"currentTurnSeq"`
				}](t, testCase.Input)
				expected := decodeFormatFixture[struct {
					ErrorClass string `json:"errorClass"`
				}](t, testCase.Expected)
				_, err := BuildIntentEnvelope(intentFixtureMessages(input.HistorySeq), intentFixtureMessages(input.CurrentTurnSeq))
				requireFixtureError(t, err, expected.ErrorClass)
			default:
				t.Fatalf("未消费的 intent envelope fixture case: %s", testCase.ID)
			}
		})
	}
}

func TestFormatFixtureProvesEmptyEnabledRulesCannotShortCircuit(t *testing.T) {
	fixture := loadFormatGoldenFixture(t)
	section := fixture.Sections["intentShortCircuit"]
	if len(section.ProductionEnabledRuleIDs) != 0 {
		t.Fatalf("M5-A enabledRuleIds 必须为空: %v", section.ProductionEnabledRuleIDs)
	}
	for _, rule := range section.CandidateRules.ResumeMarkers {
		if rule.ProductionEnabled {
			t.Fatal("fixture resume marker 不得在 M5-A 启用")
		}
		text := rule.Literal
		if text == "" {
			text = strings.Join(rule.RequiresAllLiterals, "")
		}
		if result := ClassifyIntentShortCircuit([]string{text}); result.Matched {
			t.Fatalf("未启用 resume marker 发生短路: %+v", result)
		}
	}
	if section.CandidateRules.RejectionRegex.ProductionEnabled {
		t.Fatal("fixture rejection regex 不得在 M5-A 启用")
	}
	if result := ClassifyIntentShortCircuit([]string{"很抱歉，我暂时不考虑"}); result.Matched {
		t.Fatalf("未启用 rejection regex 发生短路: %+v", result)
	}
	for _, rule := range section.CandidateRules.ShortRejections {
		if rule.ProductionEnabled {
			t.Fatal("fixture short rejection 不得在 M5-A 启用")
		}
		if result := ClassifyIntentShortCircuit([]string{rule.Literal}); result.Matched {
			t.Fatalf("未启用 short rejection 发生短路: %+v", result)
		}
	}

	for _, testCase := range section.Cases {
		t.Run(testCase.ID, func(t *testing.T) {
			switch testCase.ID {
			case "unseen_resume_marker_does_not_classify", "unseen_short_rejection_does_not_classify", "question_mark_boundaries_with_empty_rule_set":
				input := decodeFormatFixture[struct {
					OrderedMessages []string `json:"orderedMessages"`
				}](t, testCase.Input)
				result := ClassifyIntentShortCircuit(input.OrderedMessages)
				if result.Matched || result.RuleID != "" || result.Source != "" || result.Label != "" {
					t.Fatalf("空启用集错误命中: %+v", result)
				}
			case "twenty_five_and_twenty_six_boundaries_with_empty_rule_set":
				input := decodeFormatFixture[struct {
					MessageCodePointLengths []int `json:"messageCodePointLengths"`
				}](t, testCase.Input)
				for _, length := range input.MessageCodePointLengths {
					result := ClassifyIntentShortCircuit([]string{strings.Repeat("不", length)})
					if result.Matched || result.RuleID != "" {
						t.Fatalf("空启用集在 %d code points 错误命中: %+v", length, result)
					}
				}
			case "empty_turn_is_neutral_not_short_rejection":
				expected := decodeFormatFixture[struct {
					Label  string `json:"label"`
					Source string `json:"source"`
				}](t, testCase.Expected)
				result := ClassifyIntentShortCircuit(nil)
				if result.RuleID != "" || string(result.Label) != expected.Label || result.Source != expected.Source {
					t.Fatalf("空轮语义漂移: %+v", result)
				}
			default:
				t.Fatalf("未消费的 short circuit fixture case: %s", testCase.ID)
			}
		})
	}
}
