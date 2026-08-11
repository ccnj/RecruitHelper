package communication

import (
	"fmt"
	"strings"

	"recruithelper/client/service/internal/m5ai"
)

// V4InboundTurnInput is the narrow replay boundary for one contiguous inbound
// candidate turn. The ledger facts remain platform-neutral; provider advice is
// optional and may only be consumed if the deterministic branch asks for it.
type V4InboundTurnInput struct {
	State                  V4State
	TurnID                 string
	Messages               []LedgerMessageFact
	RecommendedSlots       []string
	Intent                 IntentAdvice
	Reply                  ReplyAdvice
	FixedPhrases           V4FixedPhraseView
	PrerequisitesConfirmed bool
}

type V4InboundTurnDecision struct {
	State                V4State
	Requirement          V4DialogueRequirement
	DialogueAfterActions bool

	EventActions []V4EventAction
	Dialogue     V4DialogueDecision
	ManualReason V4ManualReason
}

// v4TurnShape summarizes one frozen turn's member composition. The reducer
// derives the round-level dialogue requirement from the final state plus this
// shape, never from any single member alone (spec §5 mixed-input turns).
type v4TurnShape struct {
	hasText              bool
	hasResume            bool
	hasWechatRequested   bool
	hasWechatExchanged   bool
	hasWechatRejected    bool
	hasInterviewAccepted bool
	wechatRequestedCount int
}

func (s v4TurnShape) hasExpressive() bool {
	// 表达性成员=候选人真实发言:文字、简历卡、换微信请求卡。交换成功卡与
	// 邀面接受卡是服务事实,不推真实消息轮(与各自单卡既有语义一致)。
	return s.hasText || s.hasResume || s.hasWechatRequested
}

func (s v4TurnShape) hasSpecial() bool {
	return s.hasResume || s.hasWechatRequested || s.hasWechatExchanged ||
		s.hasWechatRejected || s.hasInterviewAccepted
}

// ReduceV4InboundTurn closes the pure vertical slice:
// neutral ledger -> normalized events -> v4 state -> optional AI/action plan.
// Per spec §5 mixed-input turns (2026-07-28) the whole turn opens at most one
// real-message round (anchor slides to the turn tail), while every special
// card applies its deterministic actions member by member in seq order. All
// three activation batches (A resume, B wechat, C interview-accepted) are
// live; unknown cards and media still freeze the whole turn.
func ReduceV4InboundTurn(input V4InboundTurnInput) (V4InboundTurnDecision, error) {
	if err := validateV4State(input.State); err != nil || strings.TrimSpace(input.TurnID) == "" || len(input.Messages) == 0 ||
		!validAdviceState(input.Intent.State) || !validAdviceState(input.Reply.State) {
		return V4InboundTurnDecision{}, ErrInvalidV4StateTransition
	}

	frozen := FrozenTurnFacts{
		TurnID:           input.TurnID,
		RecommendedSlots: append([]string(nil), input.RecommendedSlots...),
	}
	var shape v4TurnShape
	var memberEvents []BusinessEvent
	hasUnknown := false
	previousSeq := int64(0)
	for _, fact := range input.Messages {
		if fact.Seq <= previousSeq || (fact.Direction != "in" && fact.Direction != "system") {
			return V4InboundTurnDecision{}, ErrInvalidV4StateTransition
		}
		previousSeq = fact.Seq
		event, err := NormalizeLedgerMessage(fact)
		if err != nil {
			return V4InboundTurnDecision{}, err
		}
		switch event.Kind {
		case EventCandidateExpressionReceived:
			shape.hasText = true
			frozen.Messages = append(frozen.Messages, frozenInboundFromEvent(event))
		case EventResumeSubmitted:
			shape.hasResume = true
			memberEvents = append(memberEvents, event)
			frozen.Messages = append(frozen.Messages, FrozenInboundMessage{Seq: event.MessageSeq, Kind: FrozenMessageCard})
		case EventWechatRequested:
			shape.hasWechatRequested = true
			shape.wechatRequestedCount++
			memberEvents = append(memberEvents, event)
			frozen.Messages = append(frozen.Messages, FrozenInboundMessage{Seq: event.MessageSeq, Kind: FrozenMessageCard})
		case EventWechatExchanged:
			shape.hasWechatExchanged = true
			memberEvents = append(memberEvents, event)
			frozen.Messages = append(frozen.Messages, FrozenInboundMessage{Seq: event.MessageSeq, Kind: FrozenMessageCard})
		case EventWechatRejected:
			// 点拒绝换微信卡:按钮不算说话,不推真实消息轮,也不进冻结轮
			// (对话归约把卡成员判 unsupportedSemantic,该保护规则不动;
			// 拒收卡对对话完全隐形,同 system 行)。状态效果(微信线降已拒)
			// 走成员动作模式按 seq 序应用,零动作零回执(规格事件表,
			// 2026-08-11 甲方裁决)。
			shape.hasWechatRejected = true
			memberEvents = append(memberEvents, event)
		case EventInterviewAccepted:
			shape.hasInterviewAccepted = true
			memberEvents = append(memberEvents, event)
			frozen.Messages = append(frozen.Messages, FrozenInboundMessage{Seq: event.MessageSeq, Kind: FrozenMessageCard})
		case EventSystemNotice:
			// System rows remain in the ledger but do not enter a candidate turn.
		case EventUnknownPlatform:
			hasUnknown = true
		default:
			return V4InboundTurnDecision{}, ErrInvalidV4StateTransition
		}
	}

	if !shape.hasText && !shape.hasSpecial() {
		if hasUnknown {
			decision := manualV4InboundTurn(input.State, V4ManualUnknownPlatformEvent)
			decision.Requirement = V4DialogueNone
			return decision, nil
		}
		dialogue, err := ReduceV4Dialogue(V4DialogueInput{
			State: input.State, Requirement: V4DialogueNone,
			Intent: input.Intent, Reply: input.Reply,
		})
		if err != nil {
			return V4InboundTurnDecision{}, err
		}
		return V4InboundTurnDecision{
			State: dialogue.State, Requirement: V4DialogueNone, Dialogue: dialogue,
		}, nil
	}

	// 拒收卡不进冻结轮:纯拒收轮的 frozen 可为空,此时不存在表达成员,
	// turnTailSeq 也不会被锚使用(hasExpressive 的成员必然进 frozen)。
	turnTailSeq := int64(0)
	if len(frozen.Messages) > 0 {
		turnTailSeq = frozen.Messages[len(frozen.Messages)-1].Seq
	}
	state := cloneV4State(input.State)

	// 轮级表达推进:整轮至多开一个真实消息轮,沉默锚点滑到整轮尾以覆盖
	// 重放边界。stale(整轮已被更新的轮覆盖)短路为无动作;replay(同轮
	// 重放)不再推轮,但必须继续产出与首次完全一致的动作与对话要求。
	if shape.hasExpressive() {
		anchor := BusinessEvent{
			Key: "turn:" + input.TurnID, Kind: EventCandidateExpressionReceived,
			Source: EventSourceMessage, MessageSeq: turnTailSeq,
		}
		anchorDecision, disposition, err := applyV4RealExpressionDecision(
			V4EventDecision{State: state, Dialogue: V4DialogueNone},
			anchor,
		)
		if err != nil {
			return V4InboundTurnDecision{}, err
		}
		if disposition == v4ExpressionStale {
			dialogue, err := ReduceV4Dialogue(V4DialogueInput{
				State: anchorDecision.State, Requirement: V4DialogueNone,
				Intent: input.Intent, Reply: input.Reply,
			})
			if err != nil {
				return V4InboundTurnDecision{}, err
			}
			return V4InboundTurnDecision{
				State: dialogue.State, Requirement: V4DialogueNone, Dialogue: dialogue,
			}, nil
		}
		state = anchorDecision.State
	}

	// 成员动作模式:特殊卡按 seq 序逐个应用确定性部分(动作、微信线、主线
	// 迁移),不再各自推轮。任一成员要求转人工即整轮转人工,状态落在已应用
	// 完的成员为止。
	var eventActions []V4EventAction
	for _, member := range memberEvents {
		memberDecision, err := applyV4TurnMemberEvent(state, member)
		if err != nil {
			return V4InboundTurnDecision{}, err
		}
		if memberDecision.ManualReason != "" {
			decision := manualV4InboundTurn(memberDecision.State, memberDecision.ManualReason)
			decision.Requirement = V4DialogueNone
			return decision, nil
		}
		state = memberDecision.State
		eventActions = append(eventActions, memberDecision.Actions...)
	}

	requirement, dialogueAfterActions, requirementManual := v4TurnRequirement(state, shape)
	if requirementManual != "" {
		decision := manualV4InboundTurn(state, requirementManual)
		decision.Requirement = V4DialogueNone
		return decision, nil
	}
	if hasUnknown {
		// 含未知平台事件的轮整体转人工:在任何回执抑制/义务置位之前短路,
		// 否则回执义务会被标记为已由承接承载,而承接根本不会发生。
		return V4InboundTurnDecision{
			State:        state,
			Requirement:  requirement,
			Dialogue:     manualV4Dialogue(state, V4ManualUnknownPlatformEvent, "", ""),
			ManualReason: V4ManualUnknownPlatformEvent,
		}, nil
	}
	switch requirement {
	case V4DialogueNone:
		// 无对话轮沿用各单卡既有语义,但同类回执一轮至多一条(多张同类卡
		// 不叠加候选人可见动作)。
		eventActions = dedupeV4ReceiptActions(eventActions)
	case V4DialogueServiceReply:
		// 规格 §五(三) 2026-07-31 修订:已约面(含本轮迁入)的轮,固定确认
		// 语与换微信邀请照发、不被补句替代;回执展开与固定语校验沿用纯卡
		// 轮语义,可见事件动作全部收束后才允许创建 serviceReply 补句。
		eventActions = dedupeV4ReceiptActions(eventActions)
		if bubbles, receipt, handled := v4ReceiptDialogue(state, eventActions, input.FixedPhrases); handled {
			if receipt.Status == V4DialogueManualRequired {
				return V4InboundTurnDecision{
					State: state, Requirement: V4DialogueNone,
					EventActions: append([]V4EventAction(nil), eventActions...),
					Dialogue:     receipt, ManualReason: receipt.ManualReason,
				}, nil
			}
			eventActions = bubbles
		}
		if v4HasCandidateVisibleEventAction(eventActions) {
			dialogueAfterActions = true
		}
	default:
		// 规格 §五(三):主线未入已约面的对话轮,该轮固定回执由这一次 AI
		// 承接调用替代——候选人可见回复单轨,不与固定语叠发。移除回执动作
		// 的同时置位对应义务标志:义务已改由承接承载,承接失败转人工也不
		// 回落固定话术、不补发。
		eventActions, state = suppressV4ReceiptActions(eventActions, state)
	}
	if requirement == V4DialogueNone {
		if bubbles, receipt, handled := v4ReceiptDialogue(state, eventActions, input.FixedPhrases); handled {
			return V4InboundTurnDecision{
				State: state, Requirement: requirement,
				DialogueAfterActions: dialogueAfterActions,
				EventActions:         append([]V4EventAction(nil), bubbles...),
				Dialogue:             receipt, ManualReason: receipt.ManualReason,
			}, nil
		}
	}

	dialogue, err := ReduceV4Dialogue(V4DialogueInput{
		State: state, Requirement: requirement, Turn: frozen,
		Intent: input.Intent, Reply: input.Reply, FixedPhrases: input.FixedPhrases,
		CardMessageSeq: turnTailSeq, PrerequisitesConfirmed: input.PrerequisitesConfirmed,
		PendingEventActions: dialogueAfterActions && requirement == V4DialogueServiceReply,
	})
	if err != nil {
		return V4InboundTurnDecision{}, err
	}
	return V4InboundTurnDecision{
		State: dialogue.State, Requirement: requirement,
		DialogueAfterActions: dialogueAfterActions,
		EventActions:         append([]V4EventAction(nil), eventActions...),
		Dialogue:             dialogue, ManualReason: dialogue.ManualReason,
	}, nil
}

// applyV4TurnMemberEvent applies the non-expressive part of one special-card
// member. Expression advancement (round counter, silence anchor, greeted ->
// communicating) is owned by the turn-level anchor; everything else keeps the
// exact single-card semantics.
func applyV4TurnMemberEvent(state V4State, event BusinessEvent) (V4EventDecision, error) {
	decision := V4EventDecision{State: cloneV4State(state), Dialogue: V4DialogueNone}
	switch event.Kind {
	case EventResumeSubmitted:
		// 简历卡对状态机的贡献与普通真实文字相同,已由轮级表达推进承载。
		return decision, nil
	case EventWechatRequested:
		// 与单卡既有语义一致:只有进行态(含已约面服务态)才登记确定性的
		// 接受与收号通知;已终结或未进入沟通的档案不因请求卡产生动作。
		switch decision.State.MainStatus {
		case V4StatusCommunicating, V4StatusInvited, V4StatusInterviewed:
			decision.Actions = append(decision.Actions,
				eventAction(event, V4ActionAcceptWechat),
				eventAction(event, V4ActionNotifyWechat),
			)
		}
		return decision, nil
	case EventWechatExchanged:
		advanceV4Wechat(&decision.State, V4WechatExchanged)
		decision.Actions = append(decision.Actions, eventAction(event, V4ActionNotifyWechat))
		if !decision.State.WechatReceiptSent {
			decision.Actions = append(decision.Actions, eventAction(event, V4ActionWechatReceipt))
		}
		return decision, nil
	case EventWechatRejected:
		// 点拒绝换微信卡(规格事件表,2026-08-11 甲方裁决):仅推进态且线为
		// 已邀请时降已拒,服务态按 §七 无操作;零动作、零回执、不推轮。
		if isV4ProgressStatus(decision.State.MainStatus) && decision.State.WechatState == V4WechatInvited {
			advanceV4Wechat(&decision.State, V4WechatRejected)
		}
		return decision, nil
	case EventInterviewAccepted:
		return applyV4InterviewAccepted(state, decision, event)
	default:
		return V4EventDecision{}, ErrInvalidV4StateTransition
	}
}

// v4TurnRequirement derives the single round-level dialogue requirement from
// the post-application state and the turn shape (spec §5 clause 4: purpose
// selection is deterministic, one dialogue per turn at most). A non-empty
// manual reason freezes the whole turn instead of guessing a requirement.
func v4TurnRequirement(state V4State, shape v4TurnShape) (V4DialogueRequirement, bool, V4ManualReason) {
	if shape.wechatRequestedCount > 1 {
		// 接受链的接续锚要求本轮恰好一个 acceptWechat 动作;同轮多张待接受
		// 请求卡没有确定的接受目标,保守整轮转人工,不猜测接受哪一张。
		return V4DialogueNone, false, V4ManualUnsupportedSemantic
	}
	switch state.MainStatus {
	case V4StatusEliminated, V4StatusEnded, V4StatusGreeted:
		// Ended/Greeted 在含表达成员的轮里已被锚迁入 communicating;仍处于
		// 这些状态说明本轮没有表达成员(纯服务卡轮),沿用各卡既有语义。
		return V4DialogueNone, false, ""
	case V4StatusInterviewed:
		if shape.hasWechatRequested && (shape.hasText || shape.hasResume) {
			// 已约面服务态的换微信请求现有裁决是无跟随确定性接受;伴随
			// 文字时服务应答须等接受链完成,该接续形状尚未立案,保守转
			// 人工,不提前生成可能与接受结果矛盾的回复。
			return V4DialogueNone, false, V4ManualUnsupportedSemantic
		}
		if shape.hasText || shape.hasResume {
			return V4DialogueServiceReply, false, ""
		}
		// 服务态只保留确定性的接受与收号通知,不安排 AI 对话跟随(2026-07
		// 裁决):候选人可见回执由 wechatExchanged/interviewAccepted 的固定
		// 语给出。
		return V4DialogueNone, false, ""
	case V4StatusCommunicating, V4StatusInvited:
		if shape.hasWechatRequested {
			return V4DialogueWechatContinuation, true, ""
		}
		if shape.hasText || shape.hasResume {
			// 规格 §五(二):轮内含任一特殊卡即跳过意向 AI——简历卡是最强
			// 意向信号,交换成功/邀面接受是服务事实,均无需分类。
			if shape.hasResume || shape.hasWechatExchanged || shape.hasInterviewAccepted {
				return V4DialogueReplyKnownInterested, false, ""
			}
			return V4DialogueClassifyAndReply, false, ""
		}
		return V4DialogueNone, false, ""
	default:
		return V4DialogueNone, false, ""
	}
}

// suppressV4ReceiptActions removes receipt-class actions from a turn whose
// candidate-visible reply is carried by one AI continuation call, and marks
// the corresponding obligation flags as fulfilled. Per spec §5 clause 3 the
// continuation replaces the fixed receipt; on continuation failure the turn
// goes manual and the fixed phrase is never fallen back to, so flipping the
// flag here is the only consistent reading of the obligation. The
// interview-accepted follow-up wechat invite is removed together with its
// receipt: its dispatch anchor is the receipt text (body before card), and a
// cross-track dependency on the continuation reply is a mechanism this batch
// deliberately does not add — the follow-up invite stays available through
// the AI action suggestion and cold-followup tracks.
func suppressV4ReceiptActions(actions []V4EventAction, state V4State) ([]V4EventAction, V4State) {
	kept := make([]V4EventAction, 0, len(actions))
	next := cloneV4State(state)
	for index := range actions {
		switch actions[index].Kind {
		case V4ActionWechatReceipt:
			next.WechatReceiptSent = true
			continue
		case V4ActionInterviewAcceptedReceipt:
			next.InterviewAcceptedReceiptSent = true
			continue
		case V4ActionInviteWechat:
			continue
		}
		kept = append(kept, actions[index])
	}
	return kept, next
}

// dedupeV4ReceiptActions keeps at most one candidate-visible action per kind
// in a no-dialogue turn: multiple same-kind cards in one turn must not stack
// multiple fixed phrases or follow-up invite cards.
func dedupeV4ReceiptActions(actions []V4EventAction) []V4EventAction {
	kept := make([]V4EventAction, 0, len(actions))
	seen := make(map[V4ActionKind]bool, 3)
	for index := range actions {
		kind := actions[index].Kind
		if kind == V4ActionWechatReceipt || kind == V4ActionInterviewAcceptedReceipt ||
			kind == V4ActionInviteWechat {
			if seen[kind] {
				continue
			}
			seen[kind] = true
		}
		kept = append(kept, actions[index])
	}
	return kept
}

// v4ReceiptDialogue closes turns whose only candidate-visible obligation is a
// fixed receipt carried by event actions. Each receipt is expanded into one
// action per configured Messages item — the same bubble boundaries the
// rejection and cold-followup tracks already honour — so the joined Text never
// becomes a send payload. Expanding here rather than in the store keeps the
// bubble count frozen inside the projection outcome, which is what later
// replays rebuild their skeletons from.
// The phrase must exist to be sent at all; per-item rendering happens in the
// store, which owns the salutation and interview facts.
func v4ReceiptDialogue(
	state V4State,
	actions []V4EventAction,
	phrases V4FixedPhraseView,
) ([]V4EventAction, V4DialogueDecision, bool) {
	handled := false
	expanded := make([]V4EventAction, 0, len(actions))
	for _, action := range actions {
		var phraseKind V4FixedPhraseKind
		switch action.Kind {
		case V4ActionWechatReceipt:
			phraseKind = V4PhraseWechatReceipt
		case V4ActionInterviewAcceptedReceipt:
			phraseKind = V4InterviewAcceptedPhraseKind(state.InterviewMethod)
		default:
			expanded = append(expanded, action)
			continue
		}
		handled = true
		phrase := phrases.Phrase(phraseKind)
		if phrase.State != V4PhraseAvailable ||
			len(phrase.Messages) == 0 ||
			len(phrase.Messages) > m5ai.ReplyPhraseMaxItems {
			return actions, manualV4Dialogue(state, V4ManualFixedPhraseUnavailable, "", ""), true
		}
		for ordinal, message := range phrase.Messages {
			if strings.TrimSpace(message) != message ||
				m5ai.ValidateSendText(message) != nil {
				return actions, manualV4Dialogue(state, V4ManualFixedPhraseUnavailable, "", ""), true
			}
			expanded = append(expanded, V4EventAction{
				ActionKey:      v4ReceiptBubbleActionKey(action.ActionKey, ordinal+1),
				Kind:           action.Kind,
				CardMessageSeq: action.CardMessageSeq,
			})
		}
	}
	if !handled {
		return actions, V4DialogueDecision{}, false
	}
	return expanded, V4DialogueDecision{
		State: state, Status: V4DialogueNoAction, NextAdvice: V4AdviceNone,
	}, true
}

// v4HasCandidateVisibleEventAction reports whether the turn still carries an
// event action the candidate can see (receipt bubbles, wechat invite, accept):
// only those must settle before the serviceReply suffix. Operator webhooks are
// invisible and never gate the dialogue.
func v4HasCandidateVisibleEventAction(actions []V4EventAction) bool {
	for index := range actions {
		switch actions[index].Kind {
		case V4ActionNotifyWechat, V4ActionNotifyInterviewAccepted:
		default:
			return true
		}
	}
	return false
}

// v4ReceiptBubbleActionKey suffixes the reducer's semantic key with the bubble
// ordinal. A single-bubble phrase still gets its "|1", so one phrase item
// always maps to exactly one stable key regardless of how many there are.
func v4ReceiptBubbleActionKey(actionKey string, ordinal int) string {
	return fmt.Sprintf("%s|%d", actionKey, ordinal)
}

func applyV4AggregateOrdinaryTurn(state V4State, turnID string, lastMessageSeq int64) (V4State, error) {
	decision, err := ApplyV4BusinessEvent(state, BusinessEvent{
		Key: "turn:" + turnID, Kind: EventCandidateExpressionReceived,
		Source: EventSourceMessage, MessageSeq: lastMessageSeq,
	})
	if err != nil {
		return V4State{}, err
	}
	return decision.State, nil
}

func frozenInboundFromEvent(event BusinessEvent) FrozenInboundMessage {
	kind := FrozenMessageKind("")
	switch event.ExpressionKind {
	case ExpressionText:
		kind = FrozenMessageText
	case ExpressionImage:
		kind = FrozenMessageImage
	case ExpressionVoice:
		kind = FrozenMessageVoice
	case ExpressionFile:
		kind = FrozenMessageFile
	default:
		kind = FrozenMessageSystem
	}
	return FrozenInboundMessage{Seq: event.MessageSeq, Kind: kind, Text: event.Text}
}

func manualV4InboundTurn(state V4State, reason V4ManualReason) V4InboundTurnDecision {
	dialogue := manualV4Dialogue(state, reason, "", "")
	return V4InboundTurnDecision{
		State: dialogue.State, Dialogue: dialogue, ManualReason: reason,
	}
}
