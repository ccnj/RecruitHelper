package store

import (
	"errors"
	"strings"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/textcanon"

	"gorm.io/gorm"
)

const communicationV4DialogueTurnSemanticKind = "inboundTurn"

const communicationV4ManualUnfreezeSemanticKind = "manualUnfreeze"
const communicationV4DialogueAdviceKeySeparator = "|advice|"

type FreezeCommunicationV4TurnResult struct {
	Turn        DialogueTurn
	Aggregate   CommunicationV4Aggregate
	Application CommunicationV4ProjectionApplication
	Created     bool
}

// CommunicationV4OwnsTurn distinguishes the V4 aggregate continuation from a
// historical M5-A trial turn. The production patrol must never advance an
// unfinished legacy turn through the V4 owner.
func (s *Store) CommunicationV4OwnsTurn(turnID string) (bool, error) {
	if strings.TrimSpace(turnID) == "" {
		return false, ErrDialogueTurnInvalid
	}
	var owned bool
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var turn DialogueTurn
		if err := tx.First(&turn, "turn_id = ?", turnID).Error; err != nil {
			return err
		}
		_, found, err := communicationV4TurnApplicationTx(tx, turn)
		if err != nil {
			return err
		}
		owned = found
		return nil
	})
	return owned, err
}

// CommunicationV4NextAdvice returns the authoritative continuation encoded by
// the latest immutable V4 application receipt. Patrol uses it to distinguish
// ordinary intent/reply, service reply and card-rejection reply without
// inferring a branch from nullable legacy DialogueTurn fields.
func (s *Store) CommunicationV4NextAdvice(
	turnID string,
) (communication.V4AdvicePurpose, bool, error) {
	if strings.TrimSpace(turnID) == "" {
		return "", false, ErrDialogueTurnInvalid
	}
	var next communication.V4AdvicePurpose
	owned := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var turn DialogueTurn
		if err := tx.First(&turn, "turn_id = ?", turnID).Error; err != nil {
			return err
		}
		head, found, err := communicationV4TurnHeadApplicationTx(tx, turn)
		if err != nil || !found {
			return err
		}
		next = head.Outcome.NextAdvice
		owned = true
		return nil
	})
	return next, owned, err
}

// FreezeCommunicationV4Turn is the V4 production admission transaction for
// one contiguous inbound turn. It revalidates the complete target and message
// boundary, runs the deterministic reducer with no invented advice, then
// atomically persists the aggregate transition, immutable application receipt
// and dialogue turn. M5TrialSelection is deliberately outside this path.
func (s *Store) FreezeCommunicationV4Turn(
	req FreezeDialogueTurnRequest,
) (*FreezeCommunicationV4TurnResult, error) {
	if err := validateFreezeDialogueTurnRequest(req); err != nil {
		return nil, err
	}
	if req.FrozenAt.IsZero() {
		req.FrozenAt = time.Now()
	}
	req.FrozenAt = req.FrozenAt.UTC()
	out := &FreezeCommunicationV4TurnResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		aggregate, err := communicationV4AggregateTx(tx, req.ProfileID)
		if err != nil {
			return err
		}
		existing, found, err := dialogueTurnByIdentityTx(
			tx, req.TurnID, req.ProfileID, req.InputDigest,
		)
		if err != nil {
			return err
		}
		application, applied, err := communicationV4ApplicationTx(
			tx, req.ProfileID, CommunicationV4InputDialogueTurn, req.TurnID,
		)
		if err != nil {
			return err
		}
		if found || applied {
			if !found || !applied ||
				!sameFrozenDialogueTurn(existing, dialogueTurnFromFreezeRequest(req)) ||
				application.InputDigest != req.InputDigest ||
				application.SemanticKind != communicationV4DialogueTurnSemanticKind ||
				application.MessageSeq != req.InboundThroughSeq {
				return ErrCommunicationV4Conflict
			}
			if _, _, err := materializeCommunicationV4EventActionsTx(
				tx,
				application,
				application.AppliedAt,
			); err != nil {
				return err
			}
			out.Turn = existing
			out.Aggregate = aggregate
			out.Application = application
			return nil
		}
		if aggregate.AutomationStatus != ProfileCommunicationAutomationActive ||
			aggregate.ProjectedThroughSeq != req.ExpectedProjectedThroughSeq {
			return ErrDialogueTurnBinding
		}
		target, ready, err := communicationTargetTx(tx, req.ProfileID)
		if err != nil {
			return err
		}
		if !ready || target.Conversation.ConversationRef != req.ConversationRef {
			return ErrDialogueTurnBinding
		}
		material, materialReady, err := communicationAIMaterialTx(tx, target)
		if err != nil {
			return err
		}
		if !materialReady ||
			material.ContextRevision.RevisionHash != req.ContextRevisionHash ||
			material.ResumeSnapshot.SnapshotID != req.ResumeSnapshotID {
			return ErrDialogueTurnBinding
		}
		// 开轮准入闸拆腿(2026-08-02 甲方裁决,规格 v4 §一"旧轮失效"):候选人
		// 插话是常态,从未派发过发送意图的旧轮不能终身挡路。
		// 可作废腿:collected/classified/adviceReady 以及停靠 manualRequired 的
		// 旧轮,且其全部动作行 EffectIntentID/EffectStartedAt/SentAt 全空(从未
		// 派发)——在本冻结事务内先作废旧轮,再照常创建新轮;新输入到达并进入
		// 开轮流程这一刻是唯一触发点,不存在扫库作废。
		// 承重墙腿(一字不动):dispatching 旧轮,或任何动作行绑过发送意图的
		// 旧轮,照旧拒绝开轮,等 WAL/suspect 收敛;判据是动作行事实,不看
		// FailureReason 字符串。
		var unfinished []DialogueTurn
		if err := tx.Where("profile_id = ? AND status IN ?", req.ProfileID, []DialogueTurnStatus{
			DialogueTurnCollected,
			DialogueTurnClassified,
			DialogueTurnAdviceReady,
			DialogueTurnDispatching,
			DialogueTurnManualRequired,
		}).Order("created_at, turn_id").Find(&unfinished).Error; err != nil {
			return err
		}
		for index := range unfinished {
			stale := unfinished[index]
			if stale.Status == DialogueTurnDispatching {
				return ErrDialogueTurnState
			}
			if err := supersedeDialogueTurnForBoundaryTx(tx, &stale, req.FrozenAt); err != nil {
				if errors.Is(err, errDialogueTurnEffectBound) {
					return ErrDialogueTurnState
				}
				return err
			}
		}

		lastOutbound, inbound, facts, firstReal, err := reconstructCommunicationV4TurnBoundaryTx(
			tx, target.Profile, req.ConversationRef, req.InboundFromSeq, req.InboundThroughSeq,
		)
		if err != nil {
			return err
		}
		if lastOutbound.Seq != req.OutboundAnchorSeq {
			return ErrDialogueTurnBinding
		}
		// 游标之后、本轮首条候选人输入之前不得存在未领候选人消息；
		// 中间只允许尚未投影或已投影的中性 system 行。
		var unclaimed int64
		if err := tx.Model(&Message{}).
			Where(
				"platform = ? AND account_ref = ? AND conversation_ref = ? AND retracted_at IS NULL AND direction = ? AND kind <> ? AND seq > ? AND seq < ?",
				target.Profile.Platform,
				target.Profile.AccountRef,
				req.ConversationRef,
				"in",
				"system",
				req.ExpectedProjectedThroughSeq,
				req.InboundFromSeq,
			).
			Count(&unclaimed).Error; err != nil {
			return err
		}
		if unclaimed != 0 {
			return ErrDialogueTurnBinding
		}
		digest, turnID, err := communicationV4TurnIdentity(
			aggregate,
			req.ProfileID,
			lastOutbound,
			inbound,
		)
		if err != nil || digest != req.InputDigest || turnID != req.TurnID {
			return ErrDialogueTurnBinding
		}
		fixedPhrases, err := communication.BuildV4FixedPhraseView(
			material.ContextRevision.SourcePackage,
		)
		if err != nil {
			return ErrDialogueTurnBinding
		}
		decision, err := communication.ReduceV4InboundTurn(communication.V4InboundTurnInput{
			State: aggregate.State, TurnID: req.TurnID, Messages: facts,
			Intent:       communication.IntentAdvice{State: communication.AdviceAbsent},
			Reply:        communication.ReplyAdvice{State: communication.AdviceAbsent},
			FixedPhrases: fixedPhrases,
		})
		if err != nil {
			return err
		}
		decision, plans, policyManualReason := communicationV4AdvicePolicy(decision)
		if len(plans) > 0 {
			rendered, ready, err := materializeCommunicationV4FixedTextPlansTx(
				tx,
				req.ProfileID,
				plans,
			)
			if err != nil {
				return err
			}
			if !ready {
				policyManualReason = string(communication.V4ManualFixedPhraseUnavailable)
				plans = nil
				decision.ManualReason = communication.V4ManualFixedPhraseUnavailable
				decision.Dialogue.Status = communication.V4DialogueManualRequired
				decision.Dialogue.NextAdvice = communication.V4AdviceNone
				decision.Dialogue.ManualReason = communication.V4ManualFixedPhraseUnavailable
				decision.Dialogue.Actions = nil
			} else {
				plans = rendered
				decision.Dialogue.Actions = append(
					[]communication.V4PlannedAction(nil),
					rendered...,
				)
			}
		}
		turn, err := dialogueTurnFromV4Decision(req, decision)
		if err != nil {
			return err
		}
		turn.ReplyPhrases = communicationV4ReplyPhrases(plans)
		manualReason := string(decision.ManualReason)
		if policyManualReason != "" {
			manualReason = policyManualReason
			turn.Status = DialogueTurnManualRequired
			turn.FailureReason = policyManualReason
		}
		next := aggregate
		next.State = decision.State
		next.Revision++
		next.ProjectedThroughSeq = req.InboundThroughSeq
		next.UpdatedAt = req.FrozenAt
		if manualReason != "" {
			manualAt := req.FrozenAt
			next.AutomationStatus = ProfileCommunicationAutomationManualRequired
			next.ManualReason = manualReason
			next.ManualRequiredAt = &manualAt
		}
		application = CommunicationV4ProjectionApplication{
			ProfileID:   req.ProfileID,
			InputKind:   CommunicationV4InputDialogueTurn,
			InputKey:    req.TurnID,
			InputDigest: req.InputDigest, SemanticKind: communicationV4DialogueTurnSemanticKind,
			MessageSeq: req.InboundThroughSeq, FromRevision: aggregate.Revision, ToRevision: next.Revision,
			Outcome: CommunicationV4ApplicationOutcome{
				Dialogue:             decision.Requirement,
				DialogueAfterActions: decision.DialogueAfterActions,
				Actions:              append([]communication.V4EventAction(nil), decision.EventActions...),
				ManualReason:         communication.V4ManualReason(manualReason),
				DialogueStatus:       decision.Dialogue.Status,
				NextAdvice:           decision.Dialogue.NextAdvice,
				IntentLabel:          decision.Dialogue.IntentLabel,
				IntentSource:         decision.Dialogue.IntentSource,
				PlannedActions:       redactedCommunicationV4Plans(decision.Dialogue.Actions),
			},
			AppliedAt: req.FrozenAt,
		}
		if err := persistCommunicationV4TransitionTx(tx, aggregate, next, application); err != nil {
			return err
		}
		if firstReal != nil && target.Profile.FirstRealMessageSeq == nil {
			communicatingAt := firstReal.CreatedAt
			if communicatingAt.IsZero() {
				communicatingAt = req.FrozenAt
			}
			updated := tx.Model(&CandidateProfile{}).
				Where(
					"profile_id = ? AND first_real_message_seq = ?",
					req.ProfileID,
					decision.State.LastRealMessageSeq,
				).
				Updates(map[string]any{
					"first_real_message_seq": firstReal.Seq,
					"communicating_at":       communicatingAt,
				})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrCommunicationV4Conflict
			}
		}
		if err := tx.Create(&turn).Error; err != nil {
			return err
		}
		if len(plans) != 0 {
			plan := plans[0]
			action := CommunicationAction{
				ActionID: plan.ActionKey, TurnID: turn.TurnID, Kind: CommunicationActionReplyText,
				Text: plan.Text, ContentHash: textcanon.Hash(plan.Text),
				Status:    CommunicationActionPlanned,
				PlannedAt: req.FrozenAt, CreatedAt: req.FrozenAt, UpdatedAt: req.FrozenAt,
			}
			if err := tx.Create(&action).Error; err != nil {
				return err
			}
		}
		if _, _, err := materializeCommunicationV4EventActionsTx(
			tx,
			application,
			application.AppliedAt,
		); err != nil {
			return err
		}
		out.Turn = turn
		out.Aggregate = next
		out.Application = application
		out.Created = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func validateFreezeDialogueTurnRequest(req FreezeDialogueTurnRequest) error {
	if strings.TrimSpace(req.TurnID) == "" || strings.TrimSpace(req.ProfileID) == "" ||
		strings.TrimSpace(req.ConversationRef) == "" || !validCommunicationV4Digest(req.InputDigest) ||
		strings.TrimSpace(req.ContextRevisionHash) == "" || strings.TrimSpace(req.ResumeSnapshotID) == "" ||
		strings.TrimSpace(req.RecommendedTimeText) == "" || strings.TrimSpace(req.RenderFormatVersion) == "" ||
		req.HistoryThroughSeq < 0 || req.InboundFromSeq != req.HistoryThroughSeq+1 ||
		req.InboundThroughSeq < req.InboundFromSeq ||
		req.OutboundAnchorSeq < 0 || req.OutboundAnchorSeq >= req.InboundFromSeq ||
		req.ExpectedProjectedThroughSeq < req.OutboundAnchorSeq ||
		// 游标窗口(2026-08-27 停机点第二步):锚 ≤ 游标 ≤ 边界尾。边界按
		// v4 §一纯定义现算、不看游标,因此游标可以合法地落在边界内部——
		// 作废重开会把旧轮已消费的输入与新输入并成一轮(并一响应跨作废
		// 成立),resolvedFailed 裁决代次重开则游标恰在边界尾。冻结把游标
		// 写到边界尾,永不回退。
		req.ExpectedProjectedThroughSeq > req.InboundThroughSeq {
		return ErrDialogueTurnInvalid
	}
	return nil
}

func dialogueTurnFromFreezeRequest(req FreezeDialogueTurnRequest) DialogueTurn {
	return DialogueTurn{
		TurnID: req.TurnID, ProfileID: req.ProfileID, ConversationRef: req.ConversationRef,
		InputDigest: req.InputDigest, HistoryThroughSeq: req.HistoryThroughSeq,
		InboundFromSeq: req.InboundFromSeq, InboundThroughSeq: req.InboundThroughSeq,
		ContextRevisionHash: req.ContextRevisionHash, ResumeSnapshotID: req.ResumeSnapshotID,
		RecommendedTimeText: req.RecommendedTimeText, RenderFormatVersion: req.RenderFormatVersion,
		Status: DialogueTurnCollected, CreatedAt: req.FrozenAt, UpdatedAt: req.FrozenAt,
	}
}

func dialogueTurnFromV4Decision(
	req FreezeDialogueTurnRequest,
	decision communication.V4InboundTurnDecision,
) (DialogueTurn, error) {
	turn := dialogueTurnFromFreezeRequest(req)
	turn.IntentLabel = decision.Dialogue.IntentLabel
	turn.IntentSource = dialogueIntentSourceFromV4(decision.Dialogue.IntentSource)
	switch decision.Dialogue.Status {
	case communication.V4DialogueWaitingAdvice:
		switch decision.Dialogue.NextAdvice {
		case communication.V4AdviceIntent:
			turn.Status = DialogueTurnCollected
		case communication.V4AdviceReply, communication.V4AdviceServiceReply,
			communication.V4AdviceInterviewRejectionReply:
			turn.Status = DialogueTurnClassified
			classifiedAt := req.FrozenAt
			turn.ClassifiedAt = &classifiedAt
		default:
			return DialogueTurn{}, ErrDialogueTurnState
		}
	case communication.V4DialogueWaitingPrerequisite:
		turn.Status = DialogueTurnCollected
	case communication.V4DialogueActionsPlanned:
		turn.Status = DialogueTurnAdviceReady
		classifiedAt := req.FrozenAt
		turn.ClassifiedAt = &classifiedAt
	case communication.V4DialogueNoAction:
		turn.Status = DialogueTurnCompleted
	case communication.V4DialogueManualRequired:
		turn.Status = DialogueTurnManualRequired
		turn.FailureReason = string(decision.ManualReason)
	default:
		return DialogueTurn{}, ErrDialogueTurnState
	}
	return turn, nil
}

func dialogueIntentSourceFromV4(source communication.IntentSource) DialogueIntentSource {
	switch source {
	case communication.IntentSourceCodeShortCircuit:
		return DialogueIntentCodeShortCircuit
	case communication.IntentSourceLLM:
		return DialogueIntentLLM
	case communication.IntentSourceLLMFailureFallback:
		return DialogueIntentLLMFailure
	case communication.IntentSourceBusinessEvent:
		return DialogueIntentBusinessEvent
	default:
		return ""
	}
}

func communicationV4TurnApplicationTx(
	tx *gorm.DB,
	turn DialogueTurn,
) (CommunicationV4ProjectionApplication, bool, error) {
	application, found, err := communicationV4ApplicationTx(
		tx,
		turn.ProfileID,
		CommunicationV4InputDialogueTurn,
		turn.TurnID,
	)
	if err != nil || !found {
		return application, found, err
	}
	if application.InputDigest != turn.InputDigest ||
		application.SemanticKind != communicationV4DialogueTurnSemanticKind ||
		application.MessageSeq != turn.InboundThroughSeq {
		return CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Conflict
	}
	return application, true, nil
}

func communicationV4DialogueAdviceKey(turnID string, purpose m5ai.CompletionPurpose) string {
	return turnID + communicationV4DialogueAdviceKeySeparator + string(purpose)
}

func communicationV4OutcomeAuthorizesAdvice(
	outcome CommunicationV4ApplicationOutcome,
	purpose m5ai.CompletionPurpose,
) bool {
	if outcome.DialogueStatus != communication.V4DialogueWaitingAdvice {
		return false
	}
	switch purpose {
	case m5ai.PurposeIntent:
		return outcome.NextAdvice == communication.V4AdviceIntent
	case m5ai.PurposeReply:
		switch outcome.NextAdvice {
		case communication.V4AdviceReply, communication.V4AdviceServiceReply,
			communication.V4AdviceInterviewRejectionReply:
			return true
		}
	}
	return false
}

func communicationV4TurnHeadApplicationTx(
	tx *gorm.DB,
	turn DialogueTurn,
) (CommunicationV4ProjectionApplication, bool, error) {
	head, found, err := communicationV4TurnApplicationTx(tx, turn)
	if err != nil || !found {
		return head, found, err
	}
	if head.Outcome.DialogueAfterActions {
		if head.Outcome.DialogueStatus != communication.V4DialogueWaitingPrerequisite ||
			head.Outcome.NextAdvice != communication.V4AdviceNone {
			return CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Corrupt
		}
		actions, err := communicationV4EventActionsBySourceTx(
			tx,
			turn.ProfileID,
			CommunicationV4InputDialogueTurn,
			turn.TurnID,
		)
		if err != nil {
			return CommunicationV4ProjectionApplication{}, false, err
		}
		switch head.Outcome.Dialogue {
		case communication.V4DialogueWechatContinuation:
			var accept *CommunicationV4EventAction
			for index := range actions {
				if actions[index].V4Kind != communication.V4ActionAcceptWechat {
					continue
				}
				if accept != nil {
					return CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Corrupt
				}
				copy := actions[index]
				accept = &copy
			}
			if accept == nil {
				return CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Corrupt
			}
			if accept.Status == CommunicationV4EventActionSent {
				continuation, exists, err := communicationV4ApplicationTx(
					tx,
					turn.ProfileID,
					CommunicationV4InputConfirmedAction,
					accept.SemanticActionKey,
				)
				if err != nil {
					return CommunicationV4ProjectionApplication{}, false, err
				}
				if !exists ||
					continuation.SemanticKind != string(communication.V4ActionAcceptWechat) ||
					continuation.MessageSeq != 0 ||
					continuation.FromRevision != head.ToRevision ||
					continuation.Outcome.Dialogue != communication.V4DialogueWechatContinuation ||
					continuation.Outcome.DialogueStatus != communication.V4DialogueWaitingAdvice ||
					continuation.Outcome.NextAdvice != communication.V4AdviceReply ||
					continuation.Outcome.IntentLabel != m5ai.IntentInterested ||
					continuation.Outcome.IntentSource != communication.IntentSourceBusinessEvent {
					return CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Corrupt
				}
				head = continuation
			}
		case communication.V4DialogueServiceReply:
			// 已约面固定段(2026-07-31 规格 §五(三)):全部可见动作收束后,
			// 演进投影挂在收尾动作上;未收束是合法等待,head 停在初始投影。
			closing := -1
			settled := true
			for index := range actions {
				switch actions[index].V4Kind {
				case communication.V4ActionNotifyWechat, communication.V4ActionNotifyInterviewAccepted:
					continue
				}
				closing = index
				if actions[index].Status != CommunicationV4EventActionSent {
					settled = false
				}
			}
			if closing < 0 {
				return CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Corrupt
			}
			if settled {
				continuation, exists, err := communicationV4ApplicationTx(
					tx,
					turn.ProfileID,
					CommunicationV4InputConfirmedAction,
					actions[closing].SemanticActionKey,
				)
				if err != nil {
					return CommunicationV4ProjectionApplication{}, false, err
				}
				if !exists ||
					continuation.SemanticKind != string(actions[closing].V4Kind) ||
					continuation.Outcome.Dialogue != communication.V4DialogueServiceReply ||
					continuation.Outcome.DialogueStatus != communication.V4DialogueWaitingAdvice ||
					continuation.Outcome.NextAdvice != communication.V4AdviceServiceReply ||
					continuation.Outcome.IntentLabel != "" ||
					continuation.Outcome.IntentSource != "" {
					return CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Corrupt
				}
				head = continuation
			}
		default:
			return CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Corrupt
		}
	}
	if unfreeze, exists, err := communicationV4ApplicationTx(
		tx,
		turn.ProfileID,
		CommunicationV4InputManualUnfreeze,
		turn.TurnID,
	); err != nil {
		return CommunicationV4ProjectionApplication{}, false, err
	} else if exists {
		// 离线解冻链环只允许把 manualRequired 的轮回执演进为"等待回复建
		// 议";任何其他形状都是坏账本,响亮失败而不是静默续跑。
		if head.Outcome.ManualReason != communication.V4ManualUnsupportedSemantic ||
			unfreeze.InputDigest != head.InputDigest ||
			unfreeze.SemanticKind != communicationV4ManualUnfreezeSemanticKind ||
			unfreeze.MessageSeq != turn.InboundThroughSeq ||
			unfreeze.FromRevision != head.ToRevision ||
			unfreeze.Outcome.Dialogue != communication.V4DialogueReplyKnownInterested ||
			unfreeze.Outcome.DialogueStatus != communication.V4DialogueWaitingAdvice ||
			unfreeze.Outcome.NextAdvice != communication.V4AdviceReply ||
			unfreeze.Outcome.IntentLabel != m5ai.IntentInterested ||
			unfreeze.Outcome.IntentSource != communication.IntentSourceBusinessEvent {
			return CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Corrupt
		}
		head = unfreeze
	}
	for _, purpose := range []m5ai.CompletionPurpose{m5ai.PurposeIntent, m5ai.PurposeReply} {
		key := communicationV4DialogueAdviceKey(turn.TurnID, purpose)
		continuation, exists, err := communicationV4ApplicationTx(
			tx,
			turn.ProfileID,
			CommunicationV4InputDialogueAdvice,
			key,
		)
		if err != nil {
			return CommunicationV4ProjectionApplication{}, false, err
		}
		if !exists {
			continue
		}
		if !communicationV4OutcomeAuthorizesAdvice(head.Outcome, purpose) ||
			continuation.SemanticKind != string(purpose) ||
			continuation.MessageSeq != turn.InboundThroughSeq ||
			continuation.FromRevision != head.ToRevision ||
			continuation.Outcome.Dialogue != head.Outcome.Dialogue {
			return CommunicationV4ProjectionApplication{}, false, ErrCommunicationV4Corrupt
		}
		head = continuation
	}
	return head, true, nil
}

func markCommunicationV4AutomationManualTx(
	tx *gorm.DB,
	profileID string,
	reason string,
	at time.Time,
) error {
	aggregate, err := communicationV4AggregateTx(tx, profileID)
	if err != nil {
		return err
	}
	switch aggregate.AutomationStatus {
	case ProfileCommunicationAutomationManualRequired:
		if aggregate.ManualReason != reason || aggregate.ManualRequiredAt == nil {
			return ErrCommunicationV4Conflict
		}
		return nil
	case ProfileCommunicationAutomationActive:
	default:
		return ErrCommunicationV4Conflict
	}
	updated := tx.Model(&CommunicationV4Aggregate{}).
		Where(
			"profile_id = ? AND automation_status = ?",
			profileID,
			ProfileCommunicationAutomationActive,
		).
		Updates(map[string]any{
			"automation_status":  ProfileCommunicationAutomationManualRequired,
			"manual_reason":      reason,
			"manual_required_at": at.UTC(),
			"updated_at":         at.UTC(),
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrCommunicationV4Conflict
	}
	return nil
}

// reconstructCommunicationV4TurnBoundaryTx rebuilds one v4 turn boundary
// from the immutable ledger under the decoupled semantics (0727当日计划3):
// the outbound identity anchor is the newest non-retracted outbound before
// InboundFromSeq (zero only for a candidate-initiated root), the candidate
// interval is [InboundFromSeq, InboundThroughSeq], and trailing platform
// system rows after the interval are tolerated. Callers must verify the
// derived identity digest against the frozen turn; a wrongly reconstructed
// anchor can never reproduce the frozen digest, so the scan is self-verifying.
func reconstructCommunicationV4TurnBoundaryTx(
	tx *gorm.DB,
	profile CandidateProfile,
	conversationRef string,
	inboundFromSeq int64,
	inboundThroughSeq int64,
) (Message, []Message, []communication.LedgerMessageFact, *Message, error) {
	if inboundFromSeq <= 0 || inboundThroughSeq < inboundFromSeq {
		return Message{}, nil, nil, nil, ErrDialogueTurnBinding
	}
	var anchorSeq int64
	if err := tx.Model(&Message{}).
		Where(
			"platform = ? AND account_ref = ? AND conversation_ref = ? AND retracted_at IS NULL AND direction = ? AND seq < ?",
			profile.Platform,
			profile.AccountRef,
			conversationRef,
			"out",
			inboundFromSeq,
		).
		Select("COALESCE(MAX(seq), 0)").
		Scan(&anchorSeq).Error; err != nil {
		return Message{}, nil, nil, nil, err
	}
	var lateOutbound int64
	if err := tx.Model(&Message{}).
		Where(
			"platform = ? AND account_ref = ? AND conversation_ref = ? AND retracted_at IS NULL AND direction = ? AND seq >= ? "+
				// 交换结果卡(259/出站)是本轮接受动作的平台产物,不是我方新
				// 发言:形态 A 的当轮定向重对账会在承接 advice 之前把它收进
				// 账本,规格 §五(四)"接受链完成→承接"必然发生在它之后,不
				// 豁免则该分支永不可达。真人出站与系统回复仍照旧作废本轮。
				"AND NOT (kind = ? AND card_type = ? AND card_state = ?)",
			profile.Platform,
			profile.AccountRef,
			conversationRef,
			"out",
			inboundFromSeq,
			"card",
			"wechatExchange",
			"accepted",
		).
		Count(&lateOutbound).Error; err != nil {
		return Message{}, nil, nil, nil, err
	}
	if lateOutbound != 0 {
		return Message{}, nil, nil, nil, ErrDialogueTurnBinding
	}
	var candidateTail int64
	if err := tx.Model(&Message{}).
		Where(
			"platform = ? AND account_ref = ? AND conversation_ref = ? AND retracted_at IS NULL AND direction = ? AND kind <> ?",
			profile.Platform,
			profile.AccountRef,
			conversationRef,
			"in",
			"system",
		).
		Select("COALESCE(MAX(seq), 0)").
		Scan(&candidateTail).Error; err != nil {
		return Message{}, nil, nil, nil, err
	}
	if candidateTail != inboundThroughSeq {
		return Message{}, nil, nil, nil, ErrDialogueTurnBinding
	}
	var lastOutbound Message
	if anchorSeq > 0 {
		if err := tx.First(
			&lastOutbound,
			"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ? AND retracted_at IS NULL",
			profile.Platform,
			profile.AccountRef,
			conversationRef,
			anchorSeq,
		).Error; err != nil || lastOutbound.Direction != "out" {
			return Message{}, nil, nil, nil, ErrDialogueTurnBinding
		}
	}
	var boundary []Message
	if err := tx.Where(
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq >= ? AND seq <= ? "+
			"AND retracted_at IS NULL",
		profile.Platform,
		profile.AccountRef,
		conversationRef,
		inboundFromSeq,
		inboundThroughSeq,
	).Order("seq").Find(&boundary).Error; err != nil {
		return Message{}, nil, nil, nil, err
	}
	inbound, validBoundary := DialogueTurnCandidateMessages(boundary)
	if !validBoundary || len(boundary) == 0 ||
		inbound[0].Seq != inboundFromSeq ||
		inbound[len(inbound)-1].Seq != inboundThroughSeq {
		return Message{}, nil, nil, nil, ErrDialogueTurnBinding
	}
	facts := make([]communication.LedgerMessageFact, 0, len(boundary))
	var firstReal *Message
	for index := range boundary {
		message := boundary[index]
		facts = append(facts, communication.LedgerMessageFact{
			Seq: message.Seq, Direction: message.Direction, Kind: message.Kind,
			Text: message.Text, CardType: message.CardType, CardState: message.CardState,
			Origin: message.Origin, TsApproxMs: message.TsApproxMs,
			InterviewMethod: message.InterviewMethod,
		})
		if firstReal == nil && message.Direction == "in" && message.Kind != "system" &&
			IsM5RealCandidateMessage(message) {
			copy := message
			firstReal = &copy
		}
	}
	if anchorSeq == 0 {
		if firstReal == nil || firstReal.SourceKey == nil ||
			strings.TrimSpace(*firstReal.SourceKey) == "" {
			return Message{}, nil, nil, nil, ErrDialogueTurnBinding
		}
		aggregate, err := communicationV4AggregateTx(tx, profile.ProfileID)
		if err != nil {
			return Message{}, nil, nil, nil, err
		}
		expectedRoot, err := InboundConversationV4RootRef(
			profile.Platform,
			profile.AccountRef,
			conversationRef,
			*firstReal.SourceKey,
		)
		if err != nil || expectedRoot != aggregate.RootGreetingIntentID {
			return Message{}, nil, nil, nil, ErrDialogueTurnBinding
		}
	}
	return lastOutbound, inbound, facts, firstReal, nil
}

func communicationV4TurnIdentity(
	aggregate CommunicationV4Aggregate,
	profileID string,
	lastOutbound Message,
	inbound []Message,
) (string, string, error) {
	// 身份分支只看出站锚是否存在，不看投影游标：游标可以合法地停在
	// system/in 行上（0727当日计划3），来聊根在前置 system 已投影后
	// 游标同样大于零。
	if lastOutbound.Seq == 0 {
		if !IsInboundConversationV4Root(aggregate.RootGreetingIntentID) {
			return "", "", ErrDialogueTurnBinding
		}
		return DialogueTurnIdentityFromInboundRoot(
			profileID,
			aggregate.RootGreetingIntentID,
			inbound,
			aggregate.VerdictGeneration,
		)
	}
	return DialogueTurnIdentity(profileID, lastOutbound, inbound, aggregate.VerdictGeneration)
}
