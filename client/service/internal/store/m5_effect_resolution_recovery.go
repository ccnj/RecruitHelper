package store

// 本文件实现 2026-08-02 甲方裁决"人工裁决 resolvedFailed 即恢复"(协议规格
// §8"裁决即恢复"、沟通规格 v4 §一"发过")。suspect 的冻结与人工裁决必要性
// 一字不动:这里只加"裁决之后"的自动恢复,不改"裁决之前"的任何等待。恢复
// 发生在人工裁决落账的同一 SQLite 事务内,由两条结算轨的 resolvedFailed 腿
// 在"动作正从 effectSuspect 停靠态转入终局"这一刻调用——天然只由本次裁决
// 事件触发,不扫历史 suspect/manualRequired 存量,迟到重放不再触发。
//
// 恢复的形态是"按最新世界状态重新规划",不是重放:旧 intent/动作终局在账本
// 原样保留、不复活、不改写;被冻结的轮与其未派发残留显式作废
// (resolvedFailedSuperseded,复用第 4 族 supersede 的形状与 effect-bound
// 守卫思路);候选人聚合仅在人工原因确属 effectSuspect 且无其他未决 effect
// 停靠时解冻。同边界重开轮受投影游标不可回退约束(轮身份=边界内容寻址,
// FreezeCommunicationV4Turn 要求边界在游标之后),故重新规划交给下一个自然
// 触发点(候选人新输入开新轮,或时刻表轨),不铸代次后缀。

import (
	"time"

	"gorm.io/gorm"
)

const (
	// dialogueTurnResolvedFailedSuperseded 是"裁决即恢复"作废旧轮与未派发
	// 残留的显式终局原因,与第 4 族 boundarySuperseded 平行。
	dialogueTurnResolvedFailedSuperseded = "resolvedFailedSuperseded"

	auditCategoryResolvedFailedRecovered       = "communication_v4_resolved_failed_recovered"
	auditCategoryResolvedFailedRecoverySkipped = "communication_v4_resolved_failed_recovery_skipped"
	auditCategoryAutomationUnfrozen            = "communication_v4_automation_unfrozen"
)

// recoverCommunicationV4LegacyAfterResolvedFailedTx 是对话轨(legacy
// CommunicationAction)的裁决即恢复。调用前提:本次结算已把动作与轮写成
// manualRequired/effectResolvedFailed 终局,且动作此前处于 effectSuspect
// 停靠态(verdictRecovery 判定)。
func recoverCommunicationV4LegacyAfterResolvedFailedTx(
	tx *gorm.DB,
	turn DialogueTurn,
	action CommunicationAction,
	at time.Time,
) error {
	// 轮上其他"可见/未决"的 effect 案底判定:绑定过 intent 的兄弟动作里,
	// intent 终局不属于 {failed, resolvedFailed}(构造性零副作用)的任何一个
	// ——已 sent 前缀、在途 WAL、其他 suspect——都意味着该链仍在发送领域,
	// 按第 4 族 effect-bound 守卫保持保守停靠,不作废、不解冻。
	var bound []CommunicationAction
	if err := tx.Where(
		"turn_id = ? AND effect_intent_id IS NOT NULL",
		turn.TurnID,
	).Find(&bound).Error; err != nil {
		return err
	}
	blockedBy := ""
	for index := range bound {
		row := bound[index]
		if row.ActionID == action.ActionID {
			continue
		}
		var rowIntent EffectIntent
		if err := tx.First(
			&rowIntent,
			"intent_id = ?",
			*row.EffectIntentID,
		).Error; err != nil {
			return err
		}
		if rowIntent.Status != EffectIntentFailed &&
			rowIntent.Status != EffectIntentResolvedFailed {
			blockedBy = row.ActionID
			break
		}
	}
	if blockedBy != "" {
		return tx.Create(&AuditEntry{
			At: at, Category: auditCategoryResolvedFailedRecoverySkipped,
			ConversationRef: turn.ConversationRef,
			Detail: "turn=" + turn.TurnID + " action=" + action.ActionID +
				" blockedBy=" + blockedBy,
		}).Error
	}
	// 作废未派发残留:planned 与随停靠一同标注的 manualRequired 里从未绑过
	// intent 的行(含干净失败重铸留下的 planned 重试行)。本次裁决对象自身
	// 带 effect 案底,天然不在该集合内,其 manualRequired/effectResolvedFailed
	// 终局原样留档。
	if err := tx.Model(&CommunicationAction{}).
		Where(
			"turn_id = ? AND status IN ? AND effect_intent_id IS NULL AND effect_started_at IS NULL AND sent_at IS NULL",
			turn.TurnID,
			[]CommunicationActionStatus{
				CommunicationActionPlanned,
				CommunicationActionManualRequired,
			},
		).
		Updates(map[string]any{
			"status":         CommunicationActionSuperseded,
			"failure_reason": dialogueTurnResolvedFailedSuperseded,
			"updated_at":     at,
		}).Error; err != nil {
		return err
	}
	updated := tx.Model(&DialogueTurn{}).
		Where("turn_id = ? AND status = ?", turn.TurnID, DialogueTurnManualRequired).
		Updates(map[string]any{
			"status":         DialogueTurnSuperseded,
			"failure_reason": dialogueTurnResolvedFailedSuperseded,
			"updated_at":     at,
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrDialogueTurnConflict
	}
	// 事件侧未派发依赖残留(对话代持回执的多气泡/卡片)同样作废,防止解冻
	// 后留下永远等不到父正证的 planned 僵尸行。
	if err := closeCommunicationV4PlannedDependentsTx(
		tx,
		turn.ProfileID,
		[]string{action.ActionID, communicationActionPlanKey(action.ActionID)},
		at,
	); err != nil {
		return err
	}
	if err := tx.Create(&AuditEntry{
		At: at, Category: auditCategoryResolvedFailedRecovered,
		ConversationRef: turn.ConversationRef,
		Detail:          "turn=" + turn.TurnID + " action=" + action.ActionID,
	}).Error; err != nil {
		return err
	}
	return unfreezeCommunicationV4AggregateAfterResolvedFailedTx(
		tx, turn.ProfileID, action.ActionID, "", at,
	)
}

// recoverCommunicationV4EventAfterResolvedFailedTx 是事件动作轨的裁决即
// 恢复。事件动作没有轮可作废;残留只有依赖本动作(基础键或本代)的未派发
// 行,闭包作废后按同一条件解冻聚合。
func recoverCommunicationV4EventAfterResolvedFailedTx(
	tx *gorm.DB,
	action CommunicationV4EventAction,
	at time.Time,
) error {
	roots := []string{action.ActionID}
	baseKey := communicationActionPlanKey(action.SemanticActionKey)
	if baseID, err := CommunicationV4EventActionID(
		action.ProfileID,
		baseKey,
	); err == nil && baseID != action.ActionID {
		roots = append(roots, baseID)
	}
	if err := closeCommunicationV4PlannedDependentsTx(
		tx,
		action.ProfileID,
		roots,
		at,
	); err != nil {
		return err
	}
	if err := tx.Create(&AuditEntry{
		At: at, Category: auditCategoryResolvedFailedRecovered,
		Detail: "eventAction=" + action.ActionID,
	}).Error; err != nil {
		return err
	}
	return unfreezeCommunicationV4AggregateAfterResolvedFailedTx(
		tx, action.ProfileID, "", action.ActionID, at,
	)
}

// closeCommunicationV4PlannedDependentsTx 沿依赖边把仍处 planned、从未绑定
// intent 的事件动作行闭包式收编为 manualRequired/automaticDependencyUnavailable
// (既有 pre-WAL 终局词汇)。已绑 intent 的行归 WAL 恢复轨,这里不碰。
func closeCommunicationV4PlannedDependentsTx(
	tx *gorm.DB,
	profileID string,
	roots []string,
	at time.Time,
) error {
	visited := make(map[string]struct{}, len(roots))
	frontier := roots
	for len(frontier) > 0 {
		var dependents []CommunicationV4EventAction
		if err := tx.Where(
			"profile_id = ? AND depends_on_action_id IN ?",
			profileID,
			frontier,
		).Find(&dependents).Error; err != nil {
			return err
		}
		next := make([]string, 0, len(dependents))
		for index := range dependents {
			dependent := dependents[index]
			if _, seen := visited[dependent.ActionID]; seen {
				continue
			}
			visited[dependent.ActionID] = struct{}{}
			next = append(next, dependent.ActionID)
			if dependent.Status != CommunicationV4EventActionPlanned ||
				dependent.EffectIntentID != nil {
				continue
			}
			updated := tx.Model(&CommunicationV4EventAction{}).
				Where(
					"action_id = ? AND status = ? AND effect_intent_id IS NULL",
					dependent.ActionID,
					CommunicationV4EventActionPlanned,
				).
				Updates(map[string]any{
					"status":         CommunicationV4EventActionManualRequired,
					"failure_reason": CommunicationV4EventActionFailureDependencyUnavailable,
					"updated_at":     at,
				})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrCommunicationV4EventActionConflict
			}
		}
		frontier = next
	}
	return nil
}

// unfreezeCommunicationV4AggregateAfterResolvedFailedTx 在裁决即恢复的
// 收尾把候选人聚合从 manualRequired 恢复为 active。三道闸,缺一即保持冻结
// 并静默返回(不报错,恢复的其余账本迁移仍然有效):
//  1. 聚合当前人工原因必须是 effectSuspect——其他人工原因(fixedPhrase、
//     业务转人工等)不属于本次裁决,不得顺手解冻;
//  2. 该档案不得再有其他 effectSuspect/effectFailed 停靠动作(两轨都查,
//     排除本次裁决对象)——另一条链的 suspect 仍在等人工;
//  3. 该档案不得有仍处 effectPending 的在途 WAL 动作。
func unfreezeCommunicationV4AggregateAfterResolvedFailedTx(
	tx *gorm.DB,
	profileID string,
	excludeLegacyActionID string,
	excludeEventActionID string,
	at time.Time,
) error {
	aggregate, err := communicationV4AggregateTx(tx, profileID)
	if err != nil {
		return err
	}
	if aggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
		aggregate.ManualReason != "effectSuspect" {
		return nil
	}
	blockingReasons := []string{"effectSuspect", "effectFailed"}
	var blockingLegacy int64
	if err := tx.Model(&CommunicationAction{}).
		Joins("JOIN dialogue_turns ON dialogue_turns.turn_id = communication_actions.turn_id").
		Where("dialogue_turns.profile_id = ?", profileID).
		Where("communication_actions.action_id <> ?", excludeLegacyActionID).
		Where(
			"((communication_actions.status = ? AND communication_actions.failure_reason IN ?) OR communication_actions.status = ?)",
			CommunicationActionManualRequired,
			blockingReasons,
			CommunicationActionEffectPending,
		).
		Count(&blockingLegacy).Error; err != nil {
		return err
	}
	var blockingEvent int64
	if err := tx.Model(&CommunicationV4EventAction{}).
		Where("profile_id = ?", profileID).
		Where("action_id <> ?", excludeEventActionID).
		Where(
			"((status = ? AND failure_reason IN ?) OR status = ?)",
			CommunicationV4EventActionManualRequired,
			blockingReasons,
			CommunicationV4EventActionEffectPending,
		).
		Count(&blockingEvent).Error; err != nil {
		return err
	}
	if blockingLegacy != 0 || blockingEvent != 0 {
		return nil
	}
	updated := tx.Model(&CommunicationV4Aggregate{}).
		Where(
			"profile_id = ? AND automation_status = ? AND manual_reason = ?",
			profileID,
			ProfileCommunicationAutomationManualRequired,
			"effectSuspect",
		).
		Updates(map[string]any{
			"automation_status":  ProfileCommunicationAutomationActive,
			"manual_reason":      "",
			"manual_required_at": nil,
			"updated_at":         at.UTC(),
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrCommunicationV4Conflict
	}
	return tx.Create(&AuditEntry{
		At: at, Category: auditCategoryAutomationUnfrozen,
		Detail: "profile=" + profileID + " reason=resolvedFailedVerdict",
	}).Error
}
