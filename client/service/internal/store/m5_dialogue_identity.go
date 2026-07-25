package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

type DialogueTurnInputKind string

const (
	DialogueTurnInputText             DialogueTurnInputKind = "text"
	DialogueTurnInputResumeAttachment DialogueTurnInputKind = "resumeAttachment"
)

// DialogueTurnInputKindOf is the canonical production eligibility evaluator
// for M5's currently supported frozen inbound shapes. Ordinary text may span a
// contiguous turn; the strong-interest business event is deliberately limited
// to exactly one external resume card. Every other card or mixed shape remains
// outside automatic processing.
func DialogueTurnInputKindOf(inbound []Message) (DialogueTurnInputKind, bool) {
	if len(inbound) == 0 {
		return "", false
	}
	allText := true
	previous := int64(0)
	for i := range inbound {
		message := inbound[i]
		if message.Direction != "in" || message.Seq <= previous {
			return "", false
		}
		previous = message.Seq
		if message.Kind != "text" || message.Text == nil || strings.TrimSpace(*message.Text) == "" {
			allText = false
		}
	}
	if allText {
		return DialogueTurnInputText, true
	}
	message := inbound[0]
	if len(inbound) == 1 && message.Kind == "card" && message.CardType == "resumeAttachment" &&
		message.CardState == "unknown" && message.Origin == "external" {
		return DialogueTurnInputResumeAttachment, true
	}
	return "", false
}

// DialogueTurnCandidateMessages removes neutral system notices from one
// physical post-outbound boundary. System rows stay in the ledger and the
// boundary tail, but they are not candidate input and therefore do not enter
// the immutable turn digest or AI prompt.
func DialogueTurnCandidateMessages(boundary []Message) ([]Message, bool) {
	if len(boundary) == 0 {
		return nil, false
	}
	candidate := make([]Message, 0, len(boundary))
	var previous int64
	for index := range boundary {
		message := boundary[index]
		if message.Seq <= previous {
			return nil, false
		}
		previous = message.Seq
		switch {
		case message.Direction == "system":
			continue
		case message.Direction == "in" && message.Kind == "system":
			continue
		case message.Direction == "in":
			candidate = append(candidate, message)
		default:
			return nil, false
		}
	}
	return candidate, len(candidate) > 0
}

// IsM5RealCandidateMessage controls only the greeted -> communicating fact.
// A resume attachment is a real candidate action, but does not authorize an AI
// call unless the complete turn also passes DialogueTurnInputKindOf.
func IsM5RealCandidateMessage(message Message) bool {
	if message.Direction != "in" {
		return false
	}
	switch message.Kind {
	case "text", "image", "voice", "file":
		return true
	case "card":
		_, ok := DialogueTurnInputKindOf([]Message{message})
		return ok
	default:
		return false
	}
}

// DialogueTurnIdentity is the single canonical evaluator for an M5 turn's
// immutable message boundary. Both the patrol producer and every store-side
// authorization recheck use this exact function.
func DialogueTurnIdentity(profileID string, lastOutbound Message, inbound []Message) (string, string, error) {
	if strings.TrimSpace(profileID) == "" || lastOutbound.Seq <= 0 || lastOutbound.Direction != "out" ||
		strings.TrimSpace(lastOutbound.ContentHash) == "" || len(inbound) == 0 {
		return "", "", ErrDialogueTurnInvalid
	}
	type digestMessage struct {
		Seq         int64  `json:"seq"`
		Kind        string `json:"kind"`
		ContentHash string `json:"contentHash"`
	}
	canonical := struct {
		ProfileID        string          `json:"profileId"`
		LastOutboundSeq  int64           `json:"lastOutboundSeq"`
		LastOutboundHash string          `json:"lastOutboundHash"`
		HistoryThrough   int64           `json:"historyThroughSeq"`
		Messages         []digestMessage `json:"messages"`
	}{
		ProfileID: profileID, LastOutboundSeq: lastOutbound.Seq,
		LastOutboundHash: lastOutbound.ContentHash, HistoryThrough: lastOutbound.Seq,
		Messages: make([]digestMessage, 0, len(inbound)),
	}
	previous := lastOutbound.Seq
	for i := range inbound {
		message := inbound[i]
		if message.Direction != "in" || message.Seq <= previous || strings.TrimSpace(message.Kind) == "" ||
			strings.TrimSpace(message.ContentHash) == "" {
			return "", "", ErrDialogueTurnInvalid
		}
		previous = message.Seq
		canonical.Messages = append(canonical.Messages, digestMessage{
			Seq: message.Seq, Kind: message.Kind, ContentHash: message.ContentHash,
		})
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256(raw)
	hexDigest := hex.EncodeToString(digest[:])
	return hexDigest, "turn-" + hexDigest, nil
}

// DialogueTurnIdentityFromInboundRoot is the canonical evaluator for the
// exceptional first turn of a candidate who initiated the conversation.
// There is deliberately no fabricated outbound message at sequence zero: the
// versioned root reference binds the turn to the first stable inbound platform
// fact, while the ordinary message digests bind the complete candidate turn.
func DialogueTurnIdentityFromInboundRoot(
	profileID string,
	rootRef string,
	inbound []Message,
) (string, string, error) {
	if strings.TrimSpace(profileID) == "" ||
		!IsInboundConversationV4Root(rootRef) ||
		len(inbound) == 0 {
		return "", "", ErrDialogueTurnInvalid
	}
	type digestMessage struct {
		Seq         int64  `json:"seq"`
		Kind        string `json:"kind"`
		ContentHash string `json:"contentHash"`
	}
	canonical := struct {
		Version        string          `json:"version"`
		ProfileID      string          `json:"profileId"`
		RootRef        string          `json:"rootRef"`
		HistoryThrough int64           `json:"historyThroughSeq"`
		Messages       []digestMessage `json:"messages"`
	}{
		Version: "inbound-root-turn-v1", ProfileID: profileID,
		RootRef: rootRef, HistoryThrough: 0,
		Messages: make([]digestMessage, 0, len(inbound)),
	}
	var previous int64
	for i := range inbound {
		message := inbound[i]
		if message.Direction != "in" || message.Seq <= previous ||
			strings.TrimSpace(message.Kind) == "" ||
			strings.TrimSpace(message.ContentHash) == "" {
			return "", "", ErrDialogueTurnInvalid
		}
		previous = message.Seq
		canonical.Messages = append(canonical.Messages, digestMessage{
			Seq: message.Seq, Kind: message.Kind, ContentHash: message.ContentHash,
		})
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256(raw)
	hexDigest := hex.EncodeToString(digest[:])
	return hexDigest, "turn-" + hexDigest, nil
}

// M5AutomaticIntentID binds one persisted communication action to exactly one
// chat.sendMessage intent across repeated patrols and brain restarts.
func M5AutomaticIntentID(actionID string) (string, error) {
	actionID = strings.TrimSpace(actionID)
	if actionID == "" {
		return "", ErrCommunicationActionInvalid
	}
	digest := sha256.Sum256([]byte(actionID))
	return "intent-" + hex.EncodeToString(digest[:]), nil
}
