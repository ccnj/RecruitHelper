package store

import (
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
)

// 本文件是混合输入轮批 D(2026-07-28 甲方裁决)的一次性定向解冻:批 A 激活
// 前被 unsupportedSemantic 冻结、且轮输入按新形态判定合法(文字+简历卡)的
// v4 档案,以追加 manualUnfreeze 接续回执的方式把不可变轮回执演进为"等待
// 回复建议",turn 回 collected、聚合回 active。不改写任何既有回执。它有意
// 不接入服务二进制、HTTP API、巡检或重启恢复路径,只由独立 CLI 在停脑后
// 调用;形态不合法或账本形状意外的档案原样保留并在结果中报告原因。
const v4UnsupportedSemanticUnfreezeAuditCategory = "v4UnsupportedSemanticUnfreeze"

const v4UnsupportedSemanticReason = string(communication.V4ManualUnsupportedSemantic)

type V4UnsupportedSemanticUnfreezeResult struct {
	ProfileID  string
	TurnID     string
	Unfrozen   bool
	SkipReason string
}

func (s *Store) UnfreezeV4UnsupportedSemanticProfiles() ([]V4UnsupportedSemanticUnfreezeResult, error) {
	var out []V4UnsupportedSemanticUnfreezeResult
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var aggregates []CommunicationV4Aggregate
		if err := tx.
			Where("automation_status = ? AND manual_reason = ?",
				ProfileCommunicationAutomationManualRequired, v4UnsupportedSemanticReason).
			Order("profile_id").
			Find(&aggregates).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		for index := range aggregates {
			aggregate := aggregates[index]
			result, err := unfreezeV4UnsupportedSemanticProfileTx(tx, aggregate, now)
			if err != nil {
				return err
			}
			out = append(out, result)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func unfreezeV4UnsupportedSemanticProfileTx(
	tx *gorm.DB,
	aggregate CommunicationV4Aggregate,
	now time.Time,
) (V4UnsupportedSemanticUnfreezeResult, error) {
	skip := func(turnID, reason string) V4UnsupportedSemanticUnfreezeResult {
		return V4UnsupportedSemanticUnfreezeResult{
			ProfileID: aggregate.ProfileID, TurnID: turnID, SkipReason: reason,
		}
	}
	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", aggregate.ProfileID).Error; err != nil {
		return V4UnsupportedSemanticUnfreezeResult{}, err
	}
	var turns []DialogueTurn
	if err := tx.
		Where("profile_id = ? AND status = ? AND failure_reason = ?",
			aggregate.ProfileID, DialogueTurnManualRequired, v4UnsupportedSemanticReason).
		Order("created_at").
		Find(&turns).Error; err != nil {
		return V4UnsupportedSemanticUnfreezeResult{}, err
	}
	if len(turns) != 1 {
		return skip("", fmt.Sprintf("turnCount=%d", len(turns))), nil
	}
	turn := turns[0]

	var boundary []Message
	if err := tx.Where(
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq > ? AND seq <= ? AND retracted_at IS NULL",
		profile.Platform, profile.AccountRef, turn.ConversationRef,
		turn.HistoryThroughSeq, turn.InboundThroughSeq,
	).Order("seq").Find(&boundary).Error; err != nil {
		return V4UnsupportedSemanticUnfreezeResult{}, err
	}
	inbound, validBoundary := DialogueTurnCandidateMessages(boundary)
	if !validBoundary || inbound[0].Seq != turn.InboundFromSeq ||
		inbound[len(inbound)-1].Seq != turn.InboundThroughSeq {
		return skip(turn.TurnID, "boundaryMismatch"), nil
	}
	if kind, ok := DialogueTurnInputKindOf(inbound); !ok || kind != DialogueTurnInputResumeAttachment {
		return skip(turn.TurnID, "inputShapeStillUnsupported"), nil
	}

	head, found, err := communicationV4ApplicationTx(
		tx, aggregate.ProfileID, CommunicationV4InputDialogueTurn, turn.TurnID,
	)
	if err != nil {
		return V4UnsupportedSemanticUnfreezeResult{}, err
	}
	if !found || head.SemanticKind != communicationV4DialogueTurnSemanticKind ||
		head.Outcome.ManualReason != communication.V4ManualUnsupportedSemantic ||
		head.ToRevision != aggregate.Revision ||
		head.InputDigest != turn.InputDigest {
		return skip(turn.TurnID, "turnReceiptShapeUnexpected"), nil
	}

	facts := make([]communication.LedgerMessageFact, 0, len(inbound))
	for i := range inbound {
		message := inbound[i]
		facts = append(facts, communication.LedgerMessageFact{
			Seq: message.Seq, Direction: message.Direction, Kind: message.Kind,
			Text: message.Text, CardType: message.CardType, CardState: message.CardState,
			Origin: message.Origin, TsApproxMs: message.TsApproxMs,
			InterviewMethod: message.InterviewMethod,
		})
	}
	replayed, err := communication.ReduceV4InboundTurn(communication.V4InboundTurnInput{
		State: aggregate.State, TurnID: turn.TurnID, Messages: facts,
		Intent: communication.IntentAdvice{State: communication.AdviceAbsent},
		Reply:  communication.ReplyAdvice{State: communication.AdviceAbsent},
	})
	if err != nil {
		return V4UnsupportedSemanticUnfreezeResult{}, err
	}
	if replayed.ManualReason != "" ||
		replayed.Requirement != communication.V4DialogueReplyKnownInterested ||
		replayed.Dialogue.Status != communication.V4DialogueWaitingAdvice ||
		replayed.Dialogue.NextAdvice != communication.V4AdviceReply ||
		replayed.Dialogue.IntentLabel != m5ai.IntentInterested ||
		replayed.Dialogue.IntentSource != communication.IntentSourceBusinessEvent ||
		len(replayed.EventActions) != 0 || len(replayed.Dialogue.Actions) != 0 {
		return skip(turn.TurnID, "replayNotKnownInterested"), nil
	}
	beforeState, err := json.Marshal(aggregate.State)
	if err != nil {
		return V4UnsupportedSemanticUnfreezeResult{}, err
	}
	afterState, err := json.Marshal(replayed.State)
	if err != nil {
		return V4UnsupportedSemanticUnfreezeResult{}, err
	}
	if string(beforeState) != string(afterState) {
		return skip(turn.TurnID, "replayMutatedState"), nil
	}

	next := aggregate
	next.Revision++
	unfreeze := CommunicationV4ProjectionApplication{
		ProfileID: aggregate.ProfileID, InputKind: CommunicationV4InputManualUnfreeze,
		InputKey: turn.TurnID, InputDigest: turn.InputDigest,
		SemanticKind: communicationV4ManualUnfreezeSemanticKind,
		MessageSeq:   turn.InboundThroughSeq,
		FromRevision: aggregate.Revision, ToRevision: next.Revision,
		Outcome: CommunicationV4ApplicationOutcome{
			Dialogue:       communication.V4DialogueReplyKnownInterested,
			DialogueStatus: communication.V4DialogueWaitingAdvice,
			NextAdvice:     communication.V4AdviceReply,
			IntentLabel:    m5ai.IntentInterested,
			IntentSource:   communication.IntentSourceBusinessEvent,
		},
		AppliedAt: now,
	}
	if err := tx.Create(&unfreeze).Error; err != nil {
		return V4UnsupportedSemanticUnfreezeResult{}, err
	}
	turnUpdate := tx.Model(&DialogueTurn{}).
		Where("turn_id = ? AND status = ? AND failure_reason = ?",
			turn.TurnID, DialogueTurnManualRequired, v4UnsupportedSemanticReason).
		Updates(map[string]any{
			"status": DialogueTurnCollected, "failure_reason": "", "updated_at": now,
		})
	if turnUpdate.Error != nil {
		return V4UnsupportedSemanticUnfreezeResult{}, turnUpdate.Error
	}
	if turnUpdate.RowsAffected != 1 {
		return V4UnsupportedSemanticUnfreezeResult{}, ErrCommunicationV4Conflict
	}
	aggregateUpdate := tx.Model(&CommunicationV4Aggregate{}).
		Where("profile_id = ? AND automation_status = ? AND manual_reason = ? AND revision = ?",
			aggregate.ProfileID, ProfileCommunicationAutomationManualRequired,
			v4UnsupportedSemanticReason, aggregate.Revision).
		Updates(map[string]any{
			"automation_status": ProfileCommunicationAutomationActive,
			"manual_reason":     "", "manual_required_at": nil,
			"revision": next.Revision, "updated_at": now,
		})
	if aggregateUpdate.Error != nil {
		return V4UnsupportedSemanticUnfreezeResult{}, aggregateUpdate.Error
	}
	if aggregateUpdate.RowsAffected != 1 {
		return V4UnsupportedSemanticUnfreezeResult{}, ErrCommunicationV4Conflict
	}
	conversationRef := ""
	if profile.ConversationRef != nil {
		conversationRef = *profile.ConversationRef
	}
	if err := tx.Create(&AuditEntry{
		At: now, Category: v4UnsupportedSemanticUnfreezeAuditCategory,
		Platform: profile.Platform, AccountRef: profile.AccountRef,
		ConversationRef: conversationRef,
		Detail: fmt.Sprintf("turn=%s fromRevision=%d toRevision=%d",
			turn.TurnID, aggregate.Revision, next.Revision),
	}).Error; err != nil {
		return V4UnsupportedSemanticUnfreezeResult{}, err
	}
	return V4UnsupportedSemanticUnfreezeResult{
		ProfileID: aggregate.ProfileID, TurnID: turn.TurnID, Unfrozen: true,
	}, nil
}
