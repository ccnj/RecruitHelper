package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

type DialogueTurnInputKind string

const (
	DialogueTurnInputText              DialogueTurnInputKind = "text"
	DialogueTurnInputResumeAttachment  DialogueTurnInputKind = "resumeAttachment"
	DialogueTurnInputWechatCard        DialogueTurnInputKind = "wechatCard"
	DialogueTurnInputInterviewAccepted DialogueTurnInputKind = "interviewAccepted"
)

// DialogueTurnInputKindOf is the canonical production eligibility evaluator
// for M5's currently supported frozen inbound shapes. Ordinary text may span a
// contiguous turn. Per spec §5 mixed-input turns (2026-07-28) all three
// activation batches are live: external resume cards (batch A),
// wechat-exchange cards pending/accepted (batch B) and interview-accepted
// cards (batch C) mix freely with non-empty text and each other. The label
// priority wechatCard > interviewAccepted > resumeAttachment routes downstream
// to the strictest applicable branch (accept-chain first, then service reply).
// Every other card kind, media message or empty text keeps the whole turn
// outside automatic processing.
func DialogueTurnInputKindOf(inbound []Message) (DialogueTurnInputKind, bool) {
	if len(inbound) == 0 {
		return "", false
	}
	resumeCards := 0
	wechatCards := 0
	interviewAccepted := 0
	previous := int64(0)
	for i := range inbound {
		message := inbound[i]
		if message.Direction != "in" || message.Seq <= previous {
			return "", false
		}
		previous = message.Seq
		switch {
		case message.Kind == "text" && message.Text != nil && strings.TrimSpace(*message.Text) != "":
		case message.Kind == "card" && message.CardType == "resumeAttachment" &&
			message.CardState == "unknown" && message.Origin == "external":
			resumeCards++
		case message.Kind == "card" && message.CardType == "wechatExchange" &&
			(message.CardState == "pending" || message.CardState == "accepted") &&
			message.Origin == "external":
			wechatCards++
		case message.Kind == "card" && message.CardType == "interviewInvite" &&
			message.CardState == "accepted" && message.Origin == "external":
			interviewAccepted++
		default:
			return "", false
		}
	}
	if wechatCards > 0 {
		return DialogueTurnInputWechatCard, true
	}
	if interviewAccepted > 0 {
		return DialogueTurnInputInterviewAccepted, true
	}
	if resumeCards == 0 {
		return DialogueTurnInputText, true
	}
	return DialogueTurnInputResumeAttachment, true
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
// A resume attachment and a candidate-initiated wechat request are real
// candidate actions; an accepted exchange-result card is a service fact that
// must not open the communicating state by itself. Passing this check does not
// authorize an AI call unless the complete turn also passes
// DialogueTurnInputKindOf.
func IsM5RealCandidateMessage(message Message) bool {
	if message.Direction != "in" {
		return false
	}
	switch message.Kind {
	case "text", "image", "voice", "file":
		return true
	case "card":
		if message.Origin != "external" {
			return false
		}
		switch {
		case message.CardType == "resumeAttachment" && message.CardState == "unknown":
			return true
		case message.CardType == "wechatExchange" && message.CardState == "pending":
			return true
		default:
			return false
		}
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

// communicationActionRetrySuffix 是邀面卡干净失败自动重试(2026-07-29 甲方
// 裁决,协议规格 §8.4 例外)的尝试序号后缀。每次重试铸造带新后缀的动作 ID,
// 使派生 intentId/idemKey 全新,单次尝试的幂等闸不变。
var communicationActionRetrySuffix = regexp.MustCompile(`\|try([0-9]+)$`)

// communicationActionPlanKey 剥离自动重试后缀,返回与 v4 plan.ActionKey 对齐
// 的基础动作键;非重试动作原样返回。
func communicationActionPlanKey(actionID string) string {
	return communicationActionRetrySuffix.ReplaceAllString(actionID, "")
}

// IsRetryCommunicationActionID 报告动作 ID 是否携带自动重试后缀。巡检对
// 重试动作把 WAL CAS 锚改取会话最新 intent(前次失败尝试),依赖校验的透明
// 锚判定据此收窄。
func IsRetryCommunicationActionID(actionID string) bool {
	return communicationActionRetrySuffix.MatchString(actionID)
}

// communicationActionNextRetryID 返回下一次自动重试的动作 ID:基础键追加
// |try{n},首次重试为 try2。
func communicationActionNextRetryID(actionID string) string {
	match := communicationActionRetrySuffix.FindStringSubmatch(actionID)
	if match == nil {
		return actionID + "|try2"
	}
	attempt, err := strconv.Atoi(match[1])
	if err != nil || attempt < 2 {
		return communicationActionPlanKey(actionID) + "|try2"
	}
	return communicationActionPlanKey(actionID) + "|try" + strconv.Itoa(attempt+1)
}
