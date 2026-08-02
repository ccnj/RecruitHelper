package store

// 本文件实现 Q1/Q2 裁决(2026-08-02,《24点边界裁决-2026-07-28》第 4 条修订,
// AGENTS.md 运行窗口同句同步):残留的未派发 planned 动作一律作废,替代原
// "次日恢复轨自动续发"语义,次日按最新世界状态重新规划;已取得正证的前项
// 照常保留、不回滚。崩溃/重启后已生成文案但从未派发的动作同样作废,下轮
// 重取建议。
//
// 触发点=派发遭遇时刻,零扫库:只有巡检派发枚举(对话轨 dispatchM5Action、
// 事件动作轨 drainCommunicationV4EventActions)在遭遇一条陈旧 planned 行时
// 才调用本文件的入口;不存在启动清扫或定时清扫。判据机械——绑过发送意图
// (EffectIntentID/EffectStartedAt/SentAt 任一非空)的行不算"未派发",永不
// 作废(承重墙,与第 4 族 errDialogueTurnEffectBound 同一红线)。
//
// 关于"存量":陈旧 planned 行本身就是存量,但派发枚举只列聚合 active 的
// 候选人,被冻结候选人(聚合 manualRequired)的 planned 行不进入枚举、天然
// 不碰;因此这里作废的存量恰是活跃派发队列里的 Q1/Q2 裁决对象,不构成对
// 其他存量硬边界的放宽。
//
// 形状复用:对话轨作废沿用第 4 族 supersede(CommunicationAction superseded
// + 显式原因、轮终局),事件动作轨沿用第 5 族 pre-WAL 终局词汇
// (manualRequired + 显式原因)与 closeCommunicationV4PlannedDependentsTx 的
// 依赖闭包;均不产生新的投影 application 行,不改写任何已正证事实。

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	// CommunicationStalePlannedSuperseded 是 Q1/Q2 作废的显式终局原因,横跨
	// 对话轨(superseded)与事件动作轨(manualRequired)两种既有终局形状。
	CommunicationStalePlannedSuperseded = "stalePlannedSuperseded"

	auditCategoryStalePlannedSuperseded = "communication_stale_planned_superseded"
)

// StaleDialoguePlannedSupersedeResult 报告轮收束的终局动词,供巡检记日志。
type StaleDialoguePlannedSupersedeResult struct {
	TurnStatus DialogueTurnStatus
	Changed    bool
}

// SupersedeStaleDialoguePlannedAction 在派发遭遇时刻作废对话轮的陈旧
// planned 动作并给轮接上终局:
//   - 轮内存在已正证前缀(已 sent 动作)时,轮收束为 completed——链部分完成,
//     已发的算发过,锚点照滑(对齐结算 Ok 腿"无后续项即 completed"的形状);
//   - 无已发前缀时整轮 superseded(对齐第 4 族边界作废的形状;轮内仅存
//     retried 等构造性零副作用留档行时同样成立,对齐第 5 族先例)。
//
// dispatching 轮是承重墙:遭遇只发生在 adviceReady 轮,其余状态一律拒绝。
// 已绑发送意图的动作行零触碰;事件侧未派发依赖残留同批闭包作废。
func (s *Store) SupersedeStaleDialoguePlannedAction(
	turnID string,
	actionID string,
	at time.Time,
) (*StaleDialoguePlannedSupersedeResult, error) {
	if strings.TrimSpace(turnID) == "" || strings.TrimSpace(actionID) == "" {
		return nil, ErrDialogueTurnInvalid
	}
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()
	out := &StaleDialoguePlannedSupersedeResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var turn DialogueTurn
		if err := tx.First(&turn, "turn_id = ?", turnID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrDialogueTurnNotFound
			}
			return err
		}
		var action CommunicationAction
		if err := tx.First(
			&action,
			"action_id = ? AND turn_id = ?",
			actionID,
			turnID,
		).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCommunicationActionInvalid
			}
			return err
		}
		if action.Status == CommunicationActionSuperseded &&
			action.FailureReason == CommunicationStalePlannedSuperseded {
			// 结果重放:本次作废早已入账。
			out.TurnStatus = turn.Status
			return nil
		}
		// 红线:绑过发送意图(EffectIntentID/EffectStartedAt/SentAt 任一非空)
		// 的行零触碰。
		if action.Status != CommunicationActionPlanned ||
			action.EffectIntentID != nil ||
			action.EffectStartedAt != nil ||
			action.SentAt != nil {
			return ErrCommunicationActionConflict
		}
		if turn.Status != DialogueTurnAdviceReady {
			return ErrDialogueTurnState
		}
		var siblings []CommunicationAction
		if err := tx.Where("turn_id = ?", turnID).Find(&siblings).Error; err != nil {
			return err
		}
		hasSent := false
		for index := range siblings {
			row := siblings[index]
			if row.Status == CommunicationActionEffectPending {
				// 在途 WAL 与 planned 并存不是合法遭遇形状,保守拒绝,归
				// WAL 恢复轨收敛。
				return ErrCommunicationActionConflict
			}
			if row.SentAt != nil || row.Status == CommunicationActionSent {
				hasSent = true
			}
		}
		// 作废本轮全部从未派发的 planned 残留(与第 4 族同一 WHERE 形状)。
		superseded := tx.Model(&CommunicationAction{}).
			Where(
				"turn_id = ? AND status = ? AND effect_intent_id IS NULL AND effect_started_at IS NULL AND sent_at IS NULL",
				turnID,
				CommunicationActionPlanned,
			).
			Updates(map[string]any{
				"status":         CommunicationActionSuperseded,
				"failure_reason": CommunicationStalePlannedSuperseded,
				"updated_at":     at,
			})
		if superseded.Error != nil {
			return superseded.Error
		}
		if superseded.RowsAffected < 1 {
			return ErrCommunicationActionConflict
		}
		// 事件侧未派发依赖残留(对话代持回执的多气泡/卡片)同批闭包作废,
		// 防止留下永远等不到父正证的 planned 僵尸行(对齐第 5 族)。链中未
		// 物化的后项因前项非 sent 自然不再物化,无需显式处理。
		if err := closeCommunicationV4PlannedDependentsTx(
			tx,
			turn.ProfileID,
			[]string{action.ActionID, communicationActionPlanKey(action.ActionID)},
			CommunicationStalePlannedSuperseded,
			at,
		); err != nil {
			return err
		}
		terminal := DialogueTurnSuperseded
		if hasSent {
			terminal = DialogueTurnCompleted
		}
		updated := tx.Model(&DialogueTurn{}).
			Where("turn_id = ? AND status = ?", turnID, DialogueTurnAdviceReady).
			Updates(map[string]any{
				"status":         terminal,
				"failure_reason": CommunicationStalePlannedSuperseded,
				"updated_at":     at,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrDialogueTurnConflict
		}
		out.TurnStatus = terminal
		out.Changed = true
		// 不触碰聚合 AutomationStatus:作废正是为了让候选人保持 active,
		// 次日由最新世界状态的自然触发点(新输入或时刻表)重新规划。
		return tx.Create(&AuditEntry{
			At: at, Category: auditCategoryStalePlannedSuperseded,
			ConversationRef: turn.ConversationRef,
			Detail: "turn=" + turnID + " action=" + actionID +
				" terminal=" + string(terminal),
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SupersedeStaleCommunicationV4EventAction 在派发遭遇时刻作废事件动作轨
// (含时刻表计划物化行)的陈旧 planned 行,并沿依赖边闭包作废已预物化的
// planned 后项(同族 reason)。事件动作没有轮,无需收束;时刻表计划的
// occurrence/plan 失效仍由 PlannedCommunicationV4EventActionsForAccount 的
// 既有 occurrence 判定收敛,这里不另造第二套。
//
// 与 MarkCommunicationV4EventActionManualRequired 的关键差别:绝不触碰聚合
// AutomationStatus——作废正是为了让候选人保持 active、次日按最新世界状态
// 重新规划,冻结反而违背 Q1/Q2 裁决的方向。
func (s *Store) SupersedeStaleCommunicationV4EventAction(
	actionID string,
	at time.Time,
) error {
	actionID = strings.TrimSpace(actionID)
	if actionID == "" {
		return ErrCommunicationV4EventActionInvalid
	}
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()
	return s.db.Transaction(func(tx *gorm.DB) error {
		var action CommunicationV4EventAction
		if err := tx.First(&action, "action_id = ?", actionID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCommunicationV4EventActionMissing
			}
			return err
		}
		if action.Status == CommunicationV4EventActionManualRequired &&
			action.FailureReason == CommunicationStalePlannedSuperseded {
			// 结果重放:同批被父项闭包收编的后项在同一轮再次遭遇时到此。
			return nil
		}
		// 红线:绑过发送意图的行零触碰。
		if action.Status != CommunicationV4EventActionPlanned ||
			action.EffectIntentID != nil ||
			action.EffectStartedAt != nil ||
			action.SentAt != nil {
			return ErrCommunicationV4EventActionConflict
		}
		updated := tx.Model(&CommunicationV4EventAction{}).
			Where(
				"action_id = ? AND status = ? AND effect_intent_id IS NULL AND effect_started_at IS NULL AND sent_at IS NULL",
				actionID,
				CommunicationV4EventActionPlanned,
			).
			Updates(map[string]any{
				"status":         CommunicationV4EventActionManualRequired,
				"failure_reason": CommunicationStalePlannedSuperseded,
				"updated_at":     at,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrCommunicationV4EventActionConflict
		}
		roots := []string{action.ActionID}
		baseKey := communicationActionPlanKey(action.SemanticActionKey)
		if baseID, err := CommunicationV4EventActionID(
			action.ProfileID,
			baseKey,
		); err == nil && baseID != action.ActionID {
			// 陈旧行本身是 |try{n} 重试行时,依赖者按第 5 族约定挂在基础行
			// 身份上,闭包根同时携带两代身份。
			roots = append(roots, baseID)
		}
		if err := closeCommunicationV4PlannedDependentsTx(
			tx,
			action.ProfileID,
			roots,
			CommunicationStalePlannedSuperseded,
			at,
		); err != nil {
			return err
		}
		return tx.Create(&AuditEntry{
			At: at, Category: auditCategoryStalePlannedSuperseded,
			Detail: "eventAction=" + action.ActionID,
		}).Error
	})
}
