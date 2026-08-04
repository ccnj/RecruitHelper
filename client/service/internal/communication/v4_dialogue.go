package communication

import (
	"fmt"
	"strings"
	"time"

	"recruithelper/client/service/internal/m5ai"
)

type V4DialogueDecisionStatus string

const (
	V4DialogueWaitingAdvice       V4DialogueDecisionStatus = "waitingAdvice"
	V4DialogueWaitingPrerequisite V4DialogueDecisionStatus = "waitingPrerequisite"
	V4DialogueActionsPlanned      V4DialogueDecisionStatus = "actionsPlanned"
	V4DialogueNoAction            V4DialogueDecisionStatus = "noAction"
	V4DialogueManualRequired      V4DialogueDecisionStatus = "manualRequired"
)

type V4AdvicePurpose string

const (
	V4AdviceNone                    V4AdvicePurpose = "none"
	V4AdviceIntent                  V4AdvicePurpose = "intent"
	V4AdviceReply                   V4AdvicePurpose = "reply"
	V4AdviceServiceReply            V4AdvicePurpose = "serviceReply"
	V4AdviceInterviewRejectionReply V4AdvicePurpose = "interviewRejectionReply"
)

const IntentSourceBusinessEvent IntentSource = "businessEvent"

const V4InterviewDurationMs int64 = 30 * 60 * 1000

// V4InterviewTimeGridMs 是智联邀面时间选择器的分钟粒度（5 分钟格，2026-07-28
// 真机事实：分钟列仅 00/05/…/55）。
const V4InterviewTimeGridMs int64 = 5 * 60 * 1000

// roundUpToInterviewTimeGrid 把邀面开始时间向上取整到平台时间格。取整只允许
// 发生在时间出生点：plan/动作/contentHash/命令 args/平台读回全链按同一毫秒值
// 精确配对，手侧擅自取整会让发出的卡片永远无法确认（2026-07-28 甲方裁决）。
func roundUpToInterviewTimeGrid(ms int64) int64 {
	remainder := ms % V4InterviewTimeGridMs
	if remainder == 0 {
		return ms
	}
	return ms + V4InterviewTimeGridMs - remainder
}

const (
	V4ManualUnsupportedMedia       V4ManualReason = "unsupportedMedia"
	V4ManualUnsupportedSemantic    V4ManualReason = "unsupportedSemantic"
	V4ManualReplyFailed            V4ManualReason = "replyFailed"
	V4ManualReplyInvalid           V4ManualReason = "replyInvalid"
	V4ManualFixedPhraseUnavailable V4ManualReason = "fixedPhraseUnavailable"
	V4ManualWechatContinuation     V4ManualReason = "wechatContinuationManual"
)

type V4PlannedAction struct {
	ActionKey           string       `json:"actionKey"`
	Kind                V4ActionKind `json:"kind"`
	Text                string       `json:"text,omitempty"`
	CardMessageSeq      int64        `json:"cardMessageSeq,omitempty"`
	AnchorMessageSeq    int64        `json:"anchorMessageSeq,omitempty"`
	InterviewStartsAtMs *int64       `json:"interviewStartsAtMs,omitempty"`
	InterviewEndsAtMs   *int64       `json:"interviewEndsAtMs,omitempty"`
	InterviewMethod     *string      `json:"interviewMethod,omitempty"`
	Round               uint64       `json:"round,omitempty"`
	Stage               uint8        `json:"stage,omitempty"`
	EndReason           V4EndReason  `json:"endReason,omitempty"`
	DueAt               *time.Time   `json:"dueAt,omitempty"`
}

type V4DialogueInput struct {
	State                  V4State
	Requirement            V4DialogueRequirement
	Turn                   FrozenTurnFacts
	Intent                 IntentAdvice
	Reply                  ReplyAdvice
	FixedPhrases           V4FixedPhraseView
	CardMessageSeq         int64
	PrerequisitesConfirmed bool
	// PendingEventActions marks a turn whose candidate-visible event actions
	// (receipt bubbles, wechat invite) must settle before the serviceReply
	// suffix may be advised (spec v4 §5(3), 2026-07-31). The engine computes
	// it from the surviving event actions; replays rebuild the same value.
	PendingEventActions bool
}

// V4ServiceReplyVerdict distinguishes the three service-suffix terminals that
// look identical to the candidate but must stay distinguishable in the ledger
// (spec v4 §7): a sent guidance sentence carries no verdict (the action row is
// the evidence), silence and skip are explicit verdicts on a no-action close.
type V4ServiceReplyVerdict string

const (
	V4ServiceReplySilent  V4ServiceReplyVerdict = "silent"
	V4ServiceReplySkipped V4ServiceReplyVerdict = "skipped"
)

type V4DialogueDecision struct {
	State          V4State
	Status         V4DialogueDecisionStatus
	IntentLabel    m5ai.IntentLabel
	IntentSource   IntentSource
	NextAdvice     V4AdvicePurpose
	Actions        []V4PlannedAction
	ManualReason   V4ManualReason
	ServiceVerdict V4ServiceReplyVerdict
}

// ReduceV4Dialogue is the AI permission gate. The deterministic requirement
// selects the branch first; only waitingAdvice decisions authorize one model
// invocation. Fixed phrases, terminal/no-op branches and manual fallbacks never
// call AI merely to make the pipeline uniform.
func ReduceV4Dialogue(input V4DialogueInput) (V4DialogueDecision, error) {
	if err := validateV4State(input.State); err != nil || !validAdviceState(input.Intent.State) || !validAdviceState(input.Reply.State) {
		return V4DialogueDecision{}, ErrInvalidV4StateTransition
	}
	state := cloneV4State(input.State)

	switch input.Requirement {
	case V4DialogueNone:
		if input.Intent.State != AdviceAbsent || input.Reply.State != AdviceAbsent {
			return V4DialogueDecision{}, ErrInvalidV4StateTransition
		}
		return V4DialogueDecision{State: state, Status: V4DialogueNoAction, NextAdvice: V4AdviceNone}, nil
	case V4DialogueClassifyAndReply:
		if state.MainStatus != V4StatusCommunicating && state.MainStatus != V4StatusInvited {
			return V4DialogueDecision{}, ErrInvalidV4StateTransition
		}
		return reduceV4ClassifiedDialogue(input, state)
	case V4DialogueReplyKnownInterested, V4DialogueWechatContinuation:
		if state.MainStatus != V4StatusCommunicating && state.MainStatus != V4StatusInvited {
			return V4DialogueDecision{}, ErrInvalidV4StateTransition
		}
		if input.Intent.State != AdviceAbsent {
			return V4DialogueDecision{}, ErrInvalidV4StateTransition
		}
		if input.Requirement == V4DialogueWechatContinuation && !input.PrerequisitesConfirmed {
			if input.Reply.State != AdviceAbsent {
				return V4DialogueDecision{}, ErrInvalidV4StateTransition
			}
			return V4DialogueDecision{
				State: state, Status: V4DialogueWaitingPrerequisite, IntentLabel: m5ai.IntentInterested,
				IntentSource: IntentSourceBusinessEvent, NextAdvice: V4AdviceNone,
			}, nil
		}
		return reduceV4ReplyOnly(input, state, V4ActionReplyText, V4AdviceReply, m5ai.IntentInterested, IntentSourceBusinessEvent)
	case V4DialogueServiceReply:
		if state.MainStatus != V4StatusInterviewed || input.Intent.State != AdviceAbsent {
			return V4DialogueDecision{}, ErrInvalidV4StateTransition
		}
		if input.PendingEventActions && !input.PrerequisitesConfirmed {
			// 固定段(回执气泡/换微信邀请)未收束前不得创建补句建议,
			// 复用换微信承接的"先动作后对话"闸(规格 §五(三) 2026-07-31)。
			return V4DialogueDecision{
				State: state, Status: V4DialogueWaitingPrerequisite, NextAdvice: V4AdviceNone,
			}, nil
		}
		return reduceV4ServiceReply(input, state)
	case V4DialogueInterviewRejectionReceipt:
		if input.Intent.State != AdviceAbsent || input.CardMessageSeq <= 0 ||
			(state.MainStatus != V4StatusCommunicating && state.MainStatus != V4StatusInvited) {
			return V4DialogueDecision{}, ErrInvalidV4StateTransition
		}
		index := exactV4InterviewGroup(state.InterviewGroups, input.CardMessageSeq)
		if index < 0 || !state.InterviewGroups[index].Rejected {
			return V4DialogueDecision{}, ErrInvalidV4StateTransition
		}
		if state.InterviewGroups[index].RejectionReceiptSent {
			return V4DialogueDecision{State: state, Status: V4DialogueNoAction, NextAdvice: V4AdviceNone}, nil
		}
		return reduceV4ReplyOnly(input, state, V4ActionInterviewRejectionReply, V4AdviceInterviewRejectionReply, "", "")
	default:
		return V4DialogueDecision{}, ErrInvalidV4StateTransition
	}
}

func reduceV4ClassifiedDialogue(input V4DialogueInput, state V4State) (V4DialogueDecision, error) {
	base, err := Reduce(ReduceInput{Turn: input.Turn, Intent: input.Intent, Reply: input.Reply})
	if err != nil {
		return V4DialogueDecision{}, err
	}
	switch base.TurnStatus {
	case TurnCollected:
		return V4DialogueDecision{State: state, Status: V4DialogueWaitingAdvice, NextAdvice: V4AdviceIntent}, nil
	case TurnClassified:
		return V4DialogueDecision{
			State: state, Status: V4DialogueWaitingAdvice, IntentLabel: base.IntentLabel,
			IntentSource: base.IntentSource, NextAdvice: V4AdviceReply,
		}, nil
	case TurnAdviceReady:
		actions, valid := planV4ReplyActions(
			state,
			input.Turn,
			input.Reply.Suggestion,
			true,
		)
		if !valid {
			return manualV4Dialogue(
				state,
				V4ManualReplyInvalid,
				base.IntentLabel,
				base.IntentSource,
			), nil
		}
		return V4DialogueDecision{
			State: state, Status: V4DialogueActionsPlanned, IntentLabel: base.IntentLabel,
			IntentSource: base.IntentSource, NextAdvice: V4AdviceNone,
			Actions: actions,
		}, nil
	case TurnManualRequired:
		if base.ManualReason == ManualIntentRejected {
			return reduceV4Rejected(input, state, base.IntentSource)
		}
		return manualV4Dialogue(state, mapV4ManualReason(base.ManualReason), base.IntentLabel, base.IntentSource), nil
	default:
		return V4DialogueDecision{}, ErrInvalidV4StateTransition
	}
}

func reduceV4Rejected(input V4DialogueInput, state V4State, source IntentSource) (V4DialogueDecision, error) {
	state.ColdPromptRemaining = 0
	state.ColdWechatRemaining = 0
	turnSeq := input.Turn.Messages[len(input.Turn.Messages)-1].Seq
	if state.RejectionTurnMessageSeq > turnSeq ||
		(state.RejectionTurnMessageSeq == turnSeq && state.RejectionTurnID != "" && state.RejectionTurnID != input.Turn.TurnID) {
		return V4DialogueDecision{}, ErrInvalidV4StateTransition
	}
	if state.RejectionTurnMessageSeq != turnSeq {
		state.RejectionTurnMessageSeq = turnSeq
		state.RejectionTurnID = input.Turn.TurnID
		state.RejectionStage = chooseV4RejectionStage(state)
	}

	switch state.RejectionStage {
	case V4RejectionStageRetention:
		phrase := input.FixedPhrases.Phrase(V4PhraseRejectionRetention)
		actions, valid := planV4FixedPhraseActions(
			state.RejectionTurnID,
			V4ActionRejectionRetention,
			phrase,
		)
		if !valid {
			return manualV4Dialogue(state, V4ManualFixedPhraseUnavailable, m5ai.IntentRejected, source), nil
		}
		if state.WechatState == V4WechatNotInvited {
			actions = append(actions, V4PlannedAction{
				ActionKey: stableV4TurnActionKey(state.RejectionTurnID, V4ActionInviteWechat, 0),
				Kind:      V4ActionInviteWechat,
			})
		}
		return V4DialogueDecision{
			State: state, Status: V4DialogueActionsPlanned, IntentLabel: m5ai.IntentRejected,
			IntentSource: source, NextAdvice: V4AdviceNone, Actions: actions,
		}, nil
	case V4RejectionStageClosing:
		phrase := input.FixedPhrases.Phrase(V4PhraseRejectionClosing)
		actions, valid := planV4FixedPhraseActions(
			state.RejectionTurnID,
			V4ActionRejectionClosing,
			phrase,
		)
		if !valid {
			return manualV4Dialogue(state, V4ManualFixedPhraseUnavailable, m5ai.IntentRejected, source), nil
		}
		return V4DialogueDecision{
			State: state, Status: V4DialogueActionsPlanned, IntentLabel: m5ai.IntentRejected,
			IntentSource: source, NextAdvice: V4AdviceNone,
			Actions: actions,
		}, nil
	case V4RejectionStageArchive:
		archiveV4State(&state, V4EndRejected)
		return V4DialogueDecision{
			State: state, Status: V4DialogueNoAction, IntentLabel: m5ai.IntentRejected,
			IntentSource: source, NextAdvice: V4AdviceNone,
		}, nil
	default:
		return V4DialogueDecision{}, ErrInvalidV4StateTransition
	}
}

func planV4FixedPhraseActions(
	turnID string,
	kind V4ActionKind,
	phrase V4FixedPhrase,
) ([]V4PlannedAction, bool) {
	if strings.TrimSpace(turnID) == "" ||
		phrase.State != V4PhraseAvailable ||
		len(phrase.Messages) == 0 ||
		len(phrase.Messages) > m5ai.ReplyPhraseMaxItems {
		return nil, false
	}
	messages := make([]string, len(phrase.Messages))
	actions := make([]V4PlannedAction, len(phrase.Messages))
	for index, message := range phrase.Messages {
		if strings.TrimSpace(message) != message ||
			m5ai.ValidateSendText(message) != nil {
			return nil, false
		}
		messages[index] = message
		actions[index] = V4PlannedAction{
			ActionKey: stableV4TurnPhraseActionKey(
				turnID,
				kind,
				0,
				index+1,
			),
			Kind: kind,
			Text: message,
		}
	}
	if strings.Join(messages, "\n") != phrase.Text ||
		m5ai.ValidateSendText(phrase.Text) != nil {
		return nil, false
	}
	return actions, true
}

func chooseV4RejectionStage(state V4State) V4RejectionStage {
	if !state.RetentionSent {
		return V4RejectionStageRetention
	}
	if !state.ClosingSent {
		return V4RejectionStageClosing
	}
	return V4RejectionStageArchive
}

// reduceV4ServiceReply is the post-interview suffix terminal (spec v4 §7,
// 2026-07-31): one guidance sentence, explicit silence, or skip. Failure and
// out-of-contract shapes abandon the suffix on a normal close — never manual —
// because the candidate already holds the fixed segment and a lost guidance
// sentence is cheaper than freezing the whole profile.
func reduceV4ServiceReply(input V4DialogueInput, state V4State) (V4DialogueDecision, error) {
	if strings.TrimSpace(input.Turn.TurnID) == "" {
		return V4DialogueDecision{}, ErrInvalidV4StateTransition
	}
	switch input.Reply.State {
	case AdviceAbsent:
		return V4DialogueDecision{
			State: state, Status: V4DialogueWaitingAdvice, NextAdvice: V4AdviceServiceReply,
		}, nil
	case AdviceFailed:
		return V4DialogueDecision{
			State: state, Status: V4DialogueNoAction, NextAdvice: V4AdviceNone,
			ServiceVerdict: V4ServiceReplySkipped,
		}, nil
	case AdviceOK:
		suggestion := input.Reply.Suggestion
		if suggestion.Action != m5ai.ReplyActionNone || suggestion.MeetingTime != "" ||
			len(suggestion.Phrases) > 1 {
			// 合同外形状(动作、会议时间、多气泡)不是服务解析器能产出的;
			// 保守放弃补句而不是猜测发送或冻结档案。
			return V4DialogueDecision{
				State: state, Status: V4DialogueNoAction, NextAdvice: V4AdviceNone,
				ServiceVerdict: V4ServiceReplySkipped,
			}, nil
		}
		if len(suggestion.Phrases) == 0 {
			return V4DialogueDecision{
				State: state, Status: V4DialogueNoAction, NextAdvice: V4AdviceNone,
				ServiceVerdict: V4ServiceReplySilent,
			}, nil
		}
		text := strings.TrimSpace(suggestion.Phrases[0])
		if m5ai.ValidateSendText(text) != nil {
			return V4DialogueDecision{
				State: state, Status: V4DialogueNoAction, NextAdvice: V4AdviceNone,
				ServiceVerdict: V4ServiceReplySkipped,
			}, nil
		}
		return V4DialogueDecision{
			State: state, Status: V4DialogueActionsPlanned, NextAdvice: V4AdviceNone,
			Actions: []V4PlannedAction{{
				ActionKey: stableV4TurnPhraseActionKey(
					input.Turn.TurnID, V4ActionServiceReply, input.CardMessageSeq, 1,
				),
				Kind: V4ActionServiceReply, Text: text, CardMessageSeq: input.CardMessageSeq,
			}},
		}, nil
	default:
		return V4DialogueDecision{}, ErrInvalidV4StateTransition
	}
}

func reduceV4ReplyOnly(
	input V4DialogueInput,
	state V4State,
	actionKind V4ActionKind,
	purpose V4AdvicePurpose,
	label m5ai.IntentLabel,
	source IntentSource,
) (V4DialogueDecision, error) {
	if strings.TrimSpace(input.Turn.TurnID) == "" {
		return V4DialogueDecision{}, ErrInvalidV4StateTransition
	}
	switch input.Reply.State {
	case AdviceAbsent:
		return V4DialogueDecision{
			State: state, Status: V4DialogueWaitingAdvice, IntentLabel: label,
			IntentSource: source, NextAdvice: purpose,
		}, nil
	case AdviceFailed:
		return manualV4Dialogue(state, V4ManualReplyFailed, label, source), nil
	case AdviceOK:
		actions, valid := planV4ReplyActions(
			state,
			input.Turn,
			input.Reply.Suggestion,
			purpose == V4AdviceReply,
		)
		if !valid {
			return manualV4Dialogue(state, V4ManualReplyInvalid, label, source), nil
		}
		if len(actions) == 0 || actions[0].Kind != V4ActionReplyText {
			return V4DialogueDecision{}, ErrInvalidV4StateTransition
		}
		textOrdinal := 0
		for index := range actions {
			if actions[index].Kind != V4ActionReplyText {
				break
			}
			textOrdinal++
			actions[index].Kind = actionKind
			actions[index].ActionKey = stableV4TurnPhraseActionKey(
				input.Turn.TurnID,
				actionKind,
				input.CardMessageSeq,
				textOrdinal,
			)
			actions[index].CardMessageSeq = input.CardMessageSeq
		}
		return V4DialogueDecision{
			State: state, Status: V4DialogueActionsPlanned, IntentLabel: label,
			IntentSource: source, NextAdvice: V4AdviceNone,
			Actions: actions,
		}, nil
	default:
		return V4DialogueDecision{}, ErrInvalidV4StateTransition
	}
}

// V4ReplyActionMenu 是本轮 `动作` 字段合法取值的唯一实现。planV4ReplyActions
// 的事后裁决与【本轮可选动作】块的渲染都从这里取,不得各写一份(规格 v4 §五
// 「客户端渲染期追加块」同源要求)。
//
// 它是纯函数,不读时钟、不读库:调用方给什么状态就按什么状态算。渲染发生在
// 调用 provider 之前,裁决发生在拿到建议之后,两个时点的状态可能不同——那不
// 是本函数要解决的问题,一律以裁决时为准,方向只会更严。
func V4ReplyActionMenu(
	state V4State,
	turn FrozenTurnFacts,
	allowSuggestedAction bool,
) m5ai.ReplyActionMenu {
	menu := m5ai.ReplyActionMenu{WechatLine: v4ReplyMenuWechatLine(state.WechatState)}
	if !allowSuggestedAction || !v4ReplyActionEligible(state, turn) {
		return menu
	}
	// 时段列表为空时任何 `会议时间` 都不可能命中(见
	// MatchFrozenRecommendedMeetingTime),等价于本轮不允许建议线上会议。
	menu.AllowStartMeeting = len(turn.RecommendedSlots) > 0
	menu.AllowInviteWechat = state.WechatState == V4WechatNotInvited
	return menu
}

func v4ReplyMenuWechatLine(status V4WechatStatus) m5ai.ReplyMenuWechatLine {
	switch status {
	case V4WechatInvited:
		return m5ai.ReplyMenuWechatInvited
	case V4WechatExchanged:
		return m5ai.ReplyMenuWechatExchanged
	default:
		return m5ai.ReplyMenuWechatNotInvited
	}
}

// ValidV4InterviewShape 是邀面参数形态的唯一判据:线上会议必须带晚于开始的
// endsAt,现场面试必须缺席 endsAt——平台对现场面试不提供结束时间,合成或以
// 占位值冒充都会让发后正证配不上。
//
// 它只管"形态与 method 是否自洽",不管时长是否恰好是我方标准值,那是计划侧
// 的额外要求。三处闸(建议应用策略、消息落库校验、动作与计划配对)都必须经
// 由它分支:2026-08-04 真机首验就是因为其中一处仍写死 wechatVideo 与"endsAt
// 必须非空",线下卡到了发送前一步被判 multiVisibleActionPolicyConflict、
// 整轮作废重采。
func ValidV4InterviewShape(startsAtMs, endsAtMs *int64, method *string) bool {
	if startsAtMs == nil || method == nil || *startsAtMs <= 0 {
		return false
	}
	switch *method {
	case "wechatVideo":
		return endsAtMs != nil && *endsAtMs > *startsAtMs
	case "onsite":
		return endsAtMs == nil
	default:
		return false
	}
}

func planV4ReplyActions(
	state V4State,
	turn FrozenTurnFacts,
	suggestion m5ai.ReplySuggestion,
	allowSuggestedAction bool,
) ([]V4PlannedAction, bool) {
	phrases, _, err := m5ai.CanonicalReplyPhrases(suggestion)
	if err != nil {
		return nil, false
	}
	menu := V4ReplyActionMenu(state, turn, allowSuggestedAction)
	plans := make([]V4PlannedAction, 0, len(phrases)+1)
	for index, phrase := range phrases {
		plans = append(plans, V4PlannedAction{
			ActionKey: stableV4TurnPhraseActionKey(
				turn.TurnID,
				V4ActionReplyText,
				0,
				index+1,
			),
			Kind: V4ActionReplyText,
			Text: phrase,
		})
	}
	switch suggestion.Action {
	case m5ai.ReplyActionNone:
		if suggestion.MeetingTime != "" {
			return nil, false
		}
		return plans, true
	case m5ai.ReplyActionInviteWechat:
		if !menu.AllowInviteWechat || suggestion.MeetingTime != "" {
			return nil, false
		}
		return append(plans, V4PlannedAction{
			ActionKey: stableV4TurnActionKey(turn.TurnID, V4ActionInviteWechat, 0),
			Kind:      V4ActionInviteWechat,
		}), true
	case m5ai.ReplyActionStartOnlineMeeting, m5ai.ReplyActionStartOnsiteInterview:
		// 两种邀面动作共用同一道业务前置与同一份冻结推荐时段,只在派生的
		// method 与 endsAt 上分叉(《沟通逻辑规格-v4》§五,2026-08-04 裁决)。
		if !menu.AllowStartMeeting {
			return nil, false
		}
		startsAt, matched := m5ai.MatchFrozenRecommendedMeetingTime(
			turn.RecommendedSlots,
			suggestion.MeetingTime,
		)
		if !matched {
			return nil, false
		}
		// 当前冻结时段恒为整点（槽位解析强制 Minute()==0），本行为未来时段
		// 策略放开时的保险，今天恒为 no-op。
		startsAt = roundUpToInterviewTimeGrid(startsAt)
		planned := V4PlannedAction{
			ActionKey:           stableV4TurnActionKey(turn.TurnID, V4ActionInterviewInvite, 0),
			Kind:                V4ActionInterviewInvite,
			InterviewStartsAtMs: &startsAt,
		}
		if suggestion.Action == m5ai.ReplyActionStartOnsiteInterview {
			// 现场面试在平台上没有时长控件,endsAt 必须缺席而不得由 startsAt
			// 合成——手侧与协议规格 §4.5 都按缺席校验并投影 sourceKey。
			method := "onsite"
			planned.InterviewMethod = &method
		} else {
			endsAt := startsAt + V4InterviewDurationMs
			method := "wechatVideo"
			planned.InterviewEndsAtMs = &endsAt
			planned.InterviewMethod = &method
		}
		return append(plans, planned), true
	default:
		return nil, false
	}
}

func v4ReplyActionEligible(state V4State, turn FrozenTurnFacts) bool {
	if state.MainStatus != V4StatusCommunicating &&
		state.MainStatus != V4StatusInvited {
		return false
	}
	for _, message := range turn.Messages {
		switch message.Kind {
		case FrozenMessageText, FrozenMessageImage, FrozenMessageVoice,
			FrozenMessageFile, FrozenMessageCard:
			return true
		}
	}
	return false
}

func stableV4TurnActionKey(turnID string, kind V4ActionKind, cardMessageSeq int64) string {
	if cardMessageSeq > 0 {
		return fmt.Sprintf("%s|%s|card:%d", turnID, kind, cardMessageSeq)
	}
	return fmt.Sprintf("%s|%s", turnID, kind)
}

func stableV4TurnPhraseActionKey(
	turnID string,
	kind V4ActionKind,
	cardMessageSeq int64,
	ordinal int,
) string {
	if ordinal == 1 {
		return stableV4TurnActionKey(turnID, kind, cardMessageSeq)
	}
	return fmt.Sprintf(
		"%s|bubble:%d",
		stableV4TurnActionKey(turnID, kind, cardMessageSeq),
		ordinal,
	)
}

func manualV4Dialogue(state V4State, reason V4ManualReason, label m5ai.IntentLabel, source IntentSource) V4DialogueDecision {
	return V4DialogueDecision{
		State: cloneV4State(state), Status: V4DialogueManualRequired,
		IntentLabel: label, IntentSource: source, NextAdvice: V4AdviceNone, ManualReason: reason,
	}
}

func mapV4ManualReason(reason ManualReason) V4ManualReason {
	switch reason {
	case ManualUnsupportedMedia:
		return V4ManualUnsupportedMedia
	case ManualUnsupportedSemantic:
		return V4ManualUnsupportedSemantic
	case ManualReplyFailed:
		return V4ManualReplyFailed
	case ManualReplyInvalid:
		return V4ManualReplyInvalid
	default:
		return V4ManualInvalidTransition
	}
}
