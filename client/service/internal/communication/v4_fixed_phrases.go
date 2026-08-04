package communication

import (
	"encoding/json"
	"errors"
	"strings"

	"recruithelper/client/service/internal/m5ai"
)

var (
	ErrInvalidV4FixedPhrasePackage = errors.New("v4 固定话术包无效")
	ErrInvalidV4FixedPhraseRender  = errors.New("v4 固定话术渲染失败")
)

const (
	v4FixedPhraseDocType         = "固定话术"
	v4LocalRejectionClosingScene = "local:rejectionClosing:v1"
	v4LocalRejectionClosingText  = "好的，理解，感谢您的回复，祝您接下来一切顺利。"
)

// v4BraceStripper removes braces that never formed a placeholder. A stray "{"
// or "}" is a typo in the configuration, and a typo must not cost a send.
var v4BraceStripper = strings.NewReplacer("{", "", "}", "")

type V4FixedPhraseKind string

const (
	V4PhraseRejectionRetention      V4FixedPhraseKind = "rejectionRetention"
	V4PhraseRejectionClosing        V4FixedPhraseKind = "rejectionClosing"
	V4PhraseColdWechat              V4FixedPhraseKind = "coldWechat"
	V4PhraseWechatReceipt           V4FixedPhraseKind = "wechatReceipt"
	V4PhraseInterviewAccepted       V4FixedPhraseKind = "interviewAccepted"
	V4PhraseOnsiteInterviewAccepted V4FixedPhraseKind = "onsiteInterviewAccepted"
)

type V4FixedPhraseState string

const (
	V4PhraseAvailable V4FixedPhraseState = "available"
	V4PhraseMissing   V4FixedPhraseState = "missing"
	V4PhraseDisabled  V4FixedPhraseState = "disabled"
	V4PhraseEmpty     V4FixedPhraseState = "empty"
	V4PhraseInvalid   V4FixedPhraseState = "invalid"
)

type V4FixedPhrase struct {
	Kind        V4FixedPhraseKind
	SourceScene string
	State       V4FixedPhraseState
	Messages    []string
	// Text is the canonical newline-joined compatibility summary. Candidate-
	// visible planning must use Messages so the configured bubble boundaries
	// are not lost.
	Text string
}

type V4FixedPhraseView struct {
	Phrases map[V4FixedPhraseKind]V4FixedPhrase
}

type V4FixedPhraseRenderInput struct {
	Salutation string
	// InterviewTime 是已按本地时区格式化好的面试开始时间(如"7月31日 10:00")。
	// 空值不是失败:占位符会被整体删除,话术照发。
	InterviewTime string
}

var v4FixedPhraseScenes = []struct {
	kind  V4FixedPhraseKind
	scene string
}{
	{kind: V4PhraseRejectionRetention, scene: "rejectWechat"},
	{kind: V4PhraseColdWechat, scene: "silence48Wechat"},
	{kind: V4PhraseWechatReceipt, scene: "wechatAccepted"},
	{kind: V4PhraseInterviewAccepted, scene: "meetingAccepted"},
	{kind: V4PhraseOnsiteInterviewAccepted, scene: "offlineMeetingAccepted"},
}

// BuildV4FixedPhraseView parses only the four approved legacy scene mappings
// and adds the locally approved rejection-closing default.
// The old actions array is shape-checked and then discarded: all action and
// state authority remains in the deterministic v4 reducer. A broken, disabled
// or missing scene degrades only that branch; the immutable source package is
// never rewritten.
func BuildV4FixedPhraseView(source m5ai.JobConfigDocumentPackage) (V4FixedPhraseView, error) {
	view := newMissingV4FixedPhraseView()
	var content string
	found := false
	for _, document := range source.Documents {
		if document.DocType != v4FixedPhraseDocType {
			continue
		}
		if found {
			return V4FixedPhraseView{}, ErrInvalidV4FixedPhrasePackage
		}
		found = true
		content = document.Content
	}
	if !found {
		return view, nil
	}

	var root map[string]json.RawMessage
	if strings.TrimSpace(content) == "" || json.Unmarshal([]byte(content), &root) != nil || root == nil {
		return V4FixedPhraseView{}, ErrInvalidV4FixedPhrasePackage
	}
	for _, mapping := range v4FixedPhraseScenes {
		phrase := view.Phrases[mapping.kind]
		raw, exists := root[mapping.scene]
		if !exists {
			continue
		}
		phrase.State, phrase.Messages, phrase.Text = parseV4FixedPhraseScene(raw)
		view.Phrases[mapping.kind] = phrase
	}
	return view, nil
}

// V4InterviewAcceptedPhraseKind 按本次面试类型选接受回执话术。类型缺失时
// 按线上处理:线下卡在该能力上线前不可能存在,这是事实推论而不是猜测,比
// 转人工更少把存量候选人无谓堆给人(《沟通逻辑规格-v4》事件表,2026-08-04
// 甲方裁决)。只有明确读到 onsite 才走线下话术。
func V4InterviewAcceptedPhraseKind(interviewMethod string) V4FixedPhraseKind {
	if strings.TrimSpace(interviewMethod) == "onsite" {
		return V4PhraseOnsiteInterviewAccepted
	}
	return V4PhraseInterviewAccepted
}

func (view V4FixedPhraseView) Phrase(kind V4FixedPhraseKind) V4FixedPhrase {
	phrase, ok := view.Phrases[kind]
	if !ok {
		return V4FixedPhrase{Kind: kind, State: V4PhraseMissing}
	}
	return phrase
}

// RenderV4FixedPhrase is the sole renderer for candidate-visible fixed
// phrases. Per the 2026-07-30 甲方裁决 it degrades instead of stopping: a
// placeholder with no value — and any placeholder this renderer does not know
// — is deleted together with its braces, and the phrase is still sent. Only
// three outcomes still stop, and none of them is a configuration opinion:
// nothing survives the substitution, a brace survives it, or the value handed
// in carries braces of its own (that one would let配置 inject placeholders).
func RenderV4FixedPhrase(
	template string,
	input V4FixedPhraseRenderInput,
) (string, error) {
	template = strings.TrimSpace(template)
	if template == "" {
		return "", ErrInvalidV4FixedPhraseRender
	}

	values := map[string]string{
		"{称呼}":   strings.TrimSpace(input.Salutation),
		"{面试时间}": strings.TrimSpace(input.InterviewTime),
	}
	for _, value := range values {
		if strings.ContainsAny(value, "{}") {
			return "", ErrInvalidV4FixedPhraseRender
		}
	}

	var builder strings.Builder
	remaining := template
	for {
		open := strings.Index(remaining, "{")
		if open < 0 {
			builder.WriteString(v4BraceStripper.Replace(remaining))
			break
		}
		builder.WriteString(v4BraceStripper.Replace(remaining[:open]))
		rest := remaining[open:]
		end := strings.Index(rest, "}")
		if end < 0 {
			// 只有左括号没有右括号:同样按"删掉花括号"处理,不因手滑的配置停机。
			builder.WriteString(v4BraceStripper.Replace(rest))
			break
		}
		// 已知占位符取值(可能为空),未知占位符取空:两种情况都是整体删除。
		builder.WriteString(values[rest[:end+1]])
		remaining = rest[end+1:]
	}

	rendered := tidyV4RenderedPhrase(builder.String())
	if rendered == "" ||
		strings.ContainsAny(rendered, "{}") ||
		m5ai.ValidateSendText(rendered) != nil {
		return "", ErrInvalidV4FixedPhraseRender
	}
	return rendered, nil
}

// tidyV4RenderedPhrase repairs the seams a deleted placeholder leaves behind.
// "那我们 {面试时间} 线上见" must not go out as "那我们  线上见", and a phrase
// that opened with "{称呼}," must not open with a bare comma.
func tidyV4RenderedPhrase(rendered string) string {
	lines := strings.Split(rendered, "\n")
	for index, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		line = dropSpacesBetweenCJK(line)
		lines[index] = strings.TrimLeft(line, "，,、；;：: ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func dropSpacesBetweenCJK(line string) string {
	runes := []rune(line)
	var out []rune
	for index := 0; index < len(runes); index++ {
		if runes[index] == ' ' &&
			len(out) > 0 && isCJK(out[len(out)-1]) &&
			index+1 < len(runes) && isCJK(runes[index+1]) {
			continue
		}
		out = append(out, runes[index])
	}
	return string(out)
}

// isCJK covers the ranges that make a space between two characters wrong in
// Chinese copy: CJK punctuation, kana, Han, and the fullwidth forms.
func isCJK(r rune) bool {
	switch {
	case r >= 0x3000 && r <= 0x30FF,
		r >= 0x3400 && r <= 0x4DBF,
		r >= 0x4E00 && r <= 0x9FFF,
		r >= 0xF900 && r <= 0xFAFF,
		r >= 0xFF00 && r <= 0xFFEF:
		return true
	}
	return false
}

func newMissingV4FixedPhraseView() V4FixedPhraseView {
	view := V4FixedPhraseView{Phrases: make(map[V4FixedPhraseKind]V4FixedPhrase, len(v4FixedPhraseScenes)+1)}
	for _, mapping := range v4FixedPhraseScenes {
		view.Phrases[mapping.kind] = V4FixedPhrase{
			Kind: mapping.kind, SourceScene: mapping.scene, State: V4PhraseMissing,
		}
	}
	view.Phrases[V4PhraseRejectionClosing] = V4FixedPhrase{
		Kind:        V4PhraseRejectionClosing,
		SourceScene: v4LocalRejectionClosingScene,
		State:       V4PhraseAvailable,
		Messages:    []string{v4LocalRejectionClosingText},
		Text:        v4LocalRejectionClosingText,
	}
	return view
}

func parseV4FixedPhraseScene(raw json.RawMessage) (V4FixedPhraseState, []string, string) {
	var scene map[string]json.RawMessage
	if json.Unmarshal(raw, &scene) != nil || scene == nil {
		return V4PhraseInvalid, nil, ""
	}

	if enabledRaw, exists := scene["enabled"]; exists {
		var enabled bool
		if isJSONNull(enabledRaw) || json.Unmarshal(enabledRaw, &enabled) != nil {
			return V4PhraseInvalid, nil, ""
		}
		if !enabled {
			return V4PhraseDisabled, nil, ""
		}
	}

	var compatibilityMessage *string
	if messageRaw, exists := scene["message"]; exists {
		var message string
		if json.Unmarshal(messageRaw, &message) != nil {
			return V4PhraseInvalid, nil, ""
		}
		compatibilityMessage = &message
	}

	var messages []string
	if messagesRaw, exists := scene["messages"]; exists {
		if isJSONNull(messagesRaw) || json.Unmarshal(messagesRaw, &messages) != nil {
			return V4PhraseInvalid, nil, ""
		}
		if compatibilityMessage != nil && (len(messages) == 0 || *compatibilityMessage != messages[0]) {
			return V4PhraseInvalid, nil, ""
		}
	} else if compatibilityMessage != nil {
		messages = []string{*compatibilityMessage}
	}

	if actionsRaw, exists := scene["actions"]; exists {
		var actions []string
		if isJSONNull(actionsRaw) || json.Unmarshal(actionsRaw, &actions) != nil {
			return V4PhraseInvalid, nil, ""
		}
	}

	nonEmpty := make([]string, 0, len(messages))
	for _, message := range messages {
		if trimmed := strings.TrimSpace(message); trimmed != "" {
			nonEmpty = append(nonEmpty, trimmed)
		}
	}
	if len(nonEmpty) == 0 {
		return V4PhraseEmpty, nil, ""
	}
	for _, message := range nonEmpty {
		if err := m5ai.ValidateSendText(message); err != nil {
			return V4PhraseInvalid, nil, ""
		}
	}
	text := strings.Join(nonEmpty, "\n")
	if err := m5ai.ValidateSendText(text); err != nil {
		return V4PhraseInvalid, nil, ""
	}
	return V4PhraseAvailable, append([]string(nil), nonEmpty...), text
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}
