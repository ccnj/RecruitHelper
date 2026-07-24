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

const v4FixedPhraseDocType = "固定话术"

type V4FixedPhraseKind string

const (
	V4PhraseRejectionRetention V4FixedPhraseKind = "rejectionRetention"
	V4PhraseRejectionClosing   V4FixedPhraseKind = "rejectionClosing"
	V4PhraseColdWechat         V4FixedPhraseKind = "coldWechat"
	V4PhraseWechatReceipt      V4FixedPhraseKind = "wechatReceipt"
	V4PhraseInterviewAccepted  V4FixedPhraseKind = "interviewAccepted"
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
	Text        string
}

type V4FixedPhraseView struct {
	Phrases map[V4FixedPhraseKind]V4FixedPhrase
}

type V4FixedPhraseRenderInput struct {
	Salutation string
}

var v4FixedPhraseScenes = []struct {
	kind  V4FixedPhraseKind
	scene string
}{
	{kind: V4PhraseRejectionRetention, scene: "rejectWechat"},
	{kind: V4PhraseColdWechat, scene: "silence48Wechat"},
	{kind: V4PhraseWechatReceipt, scene: "wechatAccepted"},
	{kind: V4PhraseInterviewAccepted, scene: "meetingAccepted"},
}

// BuildV4FixedPhraseView parses only the four approved legacy scene mappings.
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
		phrase.State, phrase.Text = parseV4FixedPhraseScene(raw)
		view.Phrases[mapping.kind] = phrase
	}
	return view, nil
}

func (view V4FixedPhraseView) Phrase(kind V4FixedPhraseKind) V4FixedPhrase {
	phrase, ok := view.Phrases[kind]
	if !ok {
		return V4FixedPhrase{Kind: kind, State: V4PhraseMissing}
	}
	return phrase
}

// RenderV4FixedPhrase is the sole renderer for candidate-visible fixed
// phrases. The supported placeholder surface stays deliberately closed:
// configuration mistakes must stop before an action or content hash exists.
func RenderV4FixedPhrase(
	template string,
	input V4FixedPhraseRenderInput,
) (string, error) {
	template = strings.TrimSpace(template)
	if template == "" {
		return "", ErrInvalidV4FixedPhraseRender
	}

	usesSalutation := false
	remaining := template
	for {
		open := strings.Index(remaining, "{")
		close := strings.Index(remaining, "}")
		switch {
		case open < 0 && close < 0:
			remaining = ""
		case open < 0 || close < 0 || close < open:
			return "", ErrInvalidV4FixedPhraseRender
		default:
			if remaining[open:close+1] != "{称呼}" {
				return "", ErrInvalidV4FixedPhraseRender
			}
			usesSalutation = true
			remaining = remaining[close+1:]
		}
		if remaining == "" {
			break
		}
	}

	salutation := strings.TrimSpace(input.Salutation)
	if usesSalutation &&
		(salutation == "" || strings.ContainsAny(salutation, "{}")) {
		return "", ErrInvalidV4FixedPhraseRender
	}
	rendered := strings.TrimSpace(strings.ReplaceAll(template, "{称呼}", salutation))
	if strings.ContainsAny(rendered, "{}") ||
		m5ai.ValidateSendText(rendered) != nil {
		return "", ErrInvalidV4FixedPhraseRender
	}
	return rendered, nil
}

func newMissingV4FixedPhraseView() V4FixedPhraseView {
	view := V4FixedPhraseView{Phrases: make(map[V4FixedPhraseKind]V4FixedPhrase, len(v4FixedPhraseScenes))}
	for _, mapping := range v4FixedPhraseScenes {
		view.Phrases[mapping.kind] = V4FixedPhrase{
			Kind: mapping.kind, SourceScene: mapping.scene, State: V4PhraseMissing,
		}
	}
	return view
}

func parseV4FixedPhraseScene(raw json.RawMessage) (V4FixedPhraseState, string) {
	var scene map[string]json.RawMessage
	if json.Unmarshal(raw, &scene) != nil || scene == nil {
		return V4PhraseInvalid, ""
	}

	if enabledRaw, exists := scene["enabled"]; exists {
		var enabled bool
		if isJSONNull(enabledRaw) || json.Unmarshal(enabledRaw, &enabled) != nil {
			return V4PhraseInvalid, ""
		}
		if !enabled {
			return V4PhraseDisabled, ""
		}
	}

	var compatibilityMessage *string
	if messageRaw, exists := scene["message"]; exists {
		var message string
		if json.Unmarshal(messageRaw, &message) != nil {
			return V4PhraseInvalid, ""
		}
		compatibilityMessage = &message
	}

	var messages []string
	if messagesRaw, exists := scene["messages"]; exists {
		if isJSONNull(messagesRaw) || json.Unmarshal(messagesRaw, &messages) != nil {
			return V4PhraseInvalid, ""
		}
		if compatibilityMessage != nil && (len(messages) == 0 || *compatibilityMessage != messages[0]) {
			return V4PhraseInvalid, ""
		}
	} else if compatibilityMessage != nil {
		messages = []string{*compatibilityMessage}
	}

	if actionsRaw, exists := scene["actions"]; exists {
		var actions []string
		if isJSONNull(actionsRaw) || json.Unmarshal(actionsRaw, &actions) != nil {
			return V4PhraseInvalid, ""
		}
	}

	nonEmpty := make([]string, 0, len(messages))
	for _, message := range messages {
		if trimmed := strings.TrimSpace(message); trimmed != "" {
			nonEmpty = append(nonEmpty, trimmed)
		}
	}
	if len(nonEmpty) == 0 {
		return V4PhraseEmpty, ""
	}
	text := strings.Join(nonEmpty, "\n")
	if err := m5ai.ValidateSendText(text); err != nil {
		return V4PhraseInvalid, ""
	}
	return V4PhraseAvailable, text
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}
