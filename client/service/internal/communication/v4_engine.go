package communication

import (
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
	hasInterviewAccepted bool
	wechatRequestedCount int
}

func (s v4TurnShape) hasExpressive() bool {
	// 表达性成员=候选人真实发言:文字、简历卡、换微信请求卡。交换成功卡与
	// 邀面接受卡是服务事实,不推真实消息轮(与各自单卡既有语义一致)。
	return s.hasText || s.hasResume || s.hasWechatRequested
}

func (s v4TurnShape) hasSpecial() bool {
	return s.hasResume || s.hasWechatRequested || s.hasWechatExchanged || s.hasInterviewAccepted
}

// ReduceV4InboundTurn closes the pure vertical slice:
// neutral ledger -> normalized events -> v4 state -> optional AI/action plan.
// Per spec §5 mixed-input turns (2026-07-28) the whole turn opens at most one
// real-message round (anchor slides to the turn tail), while every special
// card applies its deterministic actions member by member in seq order.
// Mixes involving wechat/interview cards stay conservative until batches B/C
// activate them.
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

	if !v4TurnShapeActivated(shape) {
		state, err := applyV4AggregateOrdinaryTurn(input.State, input.TurnID, frozen.Messages[len(frozen.Messages)-1].Seq)
		if err != nil {
			return V4InboundTurnDecision{}, err
		}
		decision := manualV4InboundTurn(state, V4ManualUnsupportedSemantic)
		decision.Requirement = V4DialogueNone
		return decision, nil
	}

	turnTailSeq := frozen.Messages[len(frozen.Messages)-1].Seq
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
	if requirement != V4DialogueNone {
		// 规格 §五(三):轮内存在对话承接(文字或简历触发回复、换微信承接、
		// 服务应答)时,该轮固定回执由这一次 AI 调用替代——候选人可见回复
		// 单轨,不与固定语叠发。移除回执动作的同时置位对应义务标志:义务
		// 已改由承接承载,承接失败转人工也不回落固定话术、不补发。
		eventActions, state = suppressV4ReceiptActions(eventActions, state)
	} else {
		// 无对话轮沿用各单卡既有语义,但同类回执一轮至多一条(多张同类卡
		// 不叠加候选人可见动作)。
		eventActions = dedupeV4ReceiptActions(eventActions)
	}

	if hasUnknown {
		return V4InboundTurnDecision{
			State:        state,
			Requirement:  requirement,
			Dialogue:     manualV4Dialogue(state, V4ManualUnknownPlatformEvent, "", ""),
			ManualReason: V4ManualUnknownPlatformEvent,
		}, nil
	}
	if requirement == V4DialogueNone {
		if receipt, handled := v4ReceiptDialogue(state, eventActions, input.FixedPhrases); handled {
			return V4InboundTurnDecision{
				State: state, Requirement: requirement,
				DialogueAfterActions: dialogueAfterActions,
				EventActions:         append([]V4EventAction(nil), eventActions...),
				Dialogue:             receipt, ManualReason: receipt.ManualReason,
			}, nil
		}
	}

	dialogue, err := ReduceV4Dialogue(V4DialogueInput{
		State: state, Requirement: requirement, Turn: frozen,
		Intent: input.Intent, Reply: input.Reply, FixedPhrases: input.FixedPhrases,
		CardMessageSeq: turnTailSeq, PrerequisitesConfirmed: input.PrerequisitesConfirmed,
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

// v4TurnShapeActivated gates which member mixes are live. Batch A activated
// resume cards mixing with text; batch B activated both wechat card states.
// Interview-accepted cards (batch C) stay conservative until activation.
func v4TurnShapeActivated(shape v4TurnShape) bool {
	if shape.hasInterviewAccepted {
		// 批 C 未激活:邀面接受卡只放行单卡无文字轮(既有语义)。
		specials := 0
		if shape.hasResume {
			specials++
		}
		if shape.hasWechatRequested {
			specials++
		}
		if shape.hasWechatExchanged {
			specials++
		}
		if shape.hasInterviewAccepted {
			specials++
		}
		return !shape.hasText && specials == 1
	}
	return true
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
// flag here is the only consistent reading of the obligation.
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
		}
		kept = append(kept, actions[index])
	}
	return kept, next
}

// dedupeV4ReceiptActions keeps at most one receipt action per receipt kind in
// a no-text turn: multiple same-kind cards in one turn must not stack multiple
// candidate-visible fixed phrases (one candidate-visible action per turn).
func dedupeV4ReceiptActions(actions []V4EventAction) []V4EventAction {
	kept := make([]V4EventAction, 0, len(actions))
	seen := make(map[V4ActionKind]bool, 2)
	for index := range actions {
		kind := actions[index].Kind
		if kind == V4ActionWechatReceipt || kind == V4ActionInterviewAcceptedReceipt {
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
// fixed receipt carried by event actions. It validates every receipt-class
// action's fixed phrase; any unavailable phrase turns the whole turn manual.
func v4ReceiptDialogue(
	state V4State,
	actions []V4EventAction,
	phrases V4FixedPhraseView,
) (V4DialogueDecision, bool) {
	handled := false
	for index := range actions {
		var phraseKind V4FixedPhraseKind
		switch actions[index].Kind {
		case V4ActionWechatReceipt:
			phraseKind = V4PhraseWechatReceipt
		case V4ActionInterviewAcceptedReceipt:
			phraseKind = V4PhraseInterviewAccepted
		default:
			continue
		}
		handled = true
		phrase := phrases.Phrase(phraseKind)
		if phrase.State != V4PhraseAvailable || m5ai.ValidateSendText(phrase.Text) != nil {
			return manualV4Dialogue(state, V4ManualFixedPhraseUnavailable, "", ""), true
		}
	}
	if !handled {
		return V4DialogueDecision{}, false
	}
	return V4DialogueDecision{
		State: state, Status: V4DialogueNoAction, NextAdvice: V4AdviceNone,
	}, true
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
