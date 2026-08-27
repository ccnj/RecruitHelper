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

// dialogueBoundaryAnchorToken 返回一行消息的确定性身份记号(协议 §7.4,
// 2026-08-27 停机点第二步):优先服务端消息身份 sourceKey(§4.5,身份判新
// 战役的能力门槛保证新收编行必有);2026-08-09 之前收编的存量无身份行以
// 账本 seq 确定性兜底——seq 是脑内稳定物理序,确定性同样成立,不转人工。
// 兜底命中的观测日志由巡检层负责(键值本身按 §4.5 保密边界不进日志)。
func dialogueBoundaryAnchorToken(message Message) string {
	if message.SourceKey != nil && strings.TrimSpace(*message.SourceKey) != "" {
		return "sk:" + *message.SourceKey
	}
	return "seq:" + strconv.FormatInt(message.Seq, 10)
}

// dialogueBoundaryFingerprint 是对话轨动作身份的唯一配方(协议 §7.4
// bnd-v1,2026-08-27 甲方裁决):对(档案、输入边界锚、裁决代次)取 sha256。
// 输入边界锚 = 本轮输入尾条候选人消息的身份记号;来聊根另携带版本化根引用
// 作锚成员。刻意不含出站锚与逐条输入的内容哈希:同尾条 = 同边界 = 同键
// (多条新输入并一响应),文案可变、位置不变、键不变(重发 = 重新规划,
// 2026-08-02 决策 2)。裁决代次平时恒 0,resolvedFailed 裁决事务内加一,
// 使同边界的重新规划键确定性区别于旧尝试。版本域纪律:配方任何变更必须
// 换域(bnd-v2),存量 turn-/bnd-v1- 键不迁移、按派发时契约解释。
func dialogueBoundaryFingerprint(
	profileID string,
	rootRef string,
	tail Message,
	verdictGeneration int64,
) (string, string, error) {
	canonical := struct {
		Version           string `json:"version"`
		ProfileID         string `json:"profileId"`
		RootRef           string `json:"rootRef,omitempty"`
		BoundaryAnchor    string `json:"boundaryAnchor"`
		VerdictGeneration int64  `json:"verdictGeneration"`
	}{
		Version: "bnd-v1", ProfileID: profileID, RootRef: rootRef,
		BoundaryAnchor:    dialogueBoundaryAnchorToken(tail),
		VerdictGeneration: verdictGeneration,
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256(raw)
	hexDigest := hex.EncodeToString(digest[:])
	return hexDigest, "bnd-v1-" + hexDigest, nil
}

// DialogueTurnIdentity is the single canonical evaluator for an M5 turn's
// identity. Both the patrol producer and every store-side authorization
// recheck use this exact function. 2026-08-27 停机点第二步起身份根为输入
// 边界指纹(协议 §7.4 bnd-v1);成员校验保持既有不变式(锚为未撤回出站、
// 输入为升序候选人行),但成员内容不再进入指纹。
func DialogueTurnIdentity(
	profileID string,
	lastOutbound Message,
	inbound []Message,
	verdictGeneration int64,
) (string, string, error) {
	if strings.TrimSpace(profileID) == "" || lastOutbound.Seq <= 0 || lastOutbound.Direction != "out" ||
		strings.TrimSpace(lastOutbound.ContentHash) == "" || len(inbound) == 0 ||
		verdictGeneration < 0 {
		return "", "", ErrDialogueTurnInvalid
	}
	previous := lastOutbound.Seq
	for i := range inbound {
		message := inbound[i]
		if message.Direction != "in" || message.Seq <= previous || strings.TrimSpace(message.Kind) == "" ||
			strings.TrimSpace(message.ContentHash) == "" {
			return "", "", ErrDialogueTurnInvalid
		}
		previous = message.Seq
	}
	return dialogueBoundaryFingerprint(
		profileID, "", inbound[len(inbound)-1], verdictGeneration,
	)
}

// DialogueTurnIdentityFromInboundRoot is the canonical evaluator for the
// exceptional first turn of a candidate who initiated the conversation.
// There is deliberately no fabricated outbound message at sequence zero: the
// versioned root reference joins the bnd-v1 fingerprint as the anchor member
// (协议 §7.4,2026-08-27 停机点第二步).
func DialogueTurnIdentityFromInboundRoot(
	profileID string,
	rootRef string,
	inbound []Message,
	verdictGeneration int64,
) (string, string, error) {
	if strings.TrimSpace(profileID) == "" ||
		!IsInboundConversationV4Root(rootRef) ||
		len(inbound) == 0 || verdictGeneration < 0 {
		return "", "", ErrDialogueTurnInvalid
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
	}
	return dialogueBoundaryFingerprint(
		profileID, rootRef, inbound[len(inbound)-1], verdictGeneration,
	)
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

// CommunicationActionBasePlanKey 剥离 |try{n} 自动重试后缀,返回基础语义键;
// 非重试标识原样返回。巡检用它把同一基础动作的多代尝试折叠成同一个每轮
// 节流单元(§8.4 重铸每巡检轮至多推进一次)。
func CommunicationActionBasePlanKey(id string) string {
	return communicationActionPlanKey(id)
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
