package store

// 本文件实现"裁决已终局的迟到重放"短路(2026-08-03,停机点战役独立审查轮
// 阻断级缺陷修复)。
//
// 场景:suspect 经人工裁决 resolvedFailed 落账,"裁决即恢复"(45cbca8)同事务
// 把对话轨的轮置 superseded/resolvedFailedSuperseded、事件轨依赖残留闭包
// 作废、聚合解冻。此后手重连按协议 §9 journal at-least-once 补投迟到的
// durable result;dispatch 层按设计好的 wasHumanResolved 纠正路径("durable
// 平台 result 比人工裁决更强")把 intent 从 resolvedFailed 覆写为真实终局
// (ok 或 failed)后进入两条结算轨。修复前:
//   - 对话轨(legacy):轮状态白名单 {dispatching, manualRequired, completed}
//     不含 superseded → ErrDialogueTurnState → ApplyResultMessage 整事务
//     回滚 → ProcessedMsg 不落、ack 不发,手每次重连重放失败直到 outbox
//     TTL(7 天),期间该手对账查询被 outboxPending 闸住;
//   - 事件轨:迟到 failed/canceled/expired 走失败腿竟然成功——裁决终局
//     reason 被改写为 effectFailed,且刚被裁决解冻的聚合被
//     markCommunicationV4AutomationManualTx 静默再冻,裁决效果无声撤销,
//     还没有 suspect 队列条目指引甲方。
//
// 修复形态与既有 retried 重放短路同形,判据机械:动作行
// FailureReason==effectResolvedFailed(事件轨),或所属轮 Status==superseded
// 且 FailureReason==resolvedFailedSuperseded(对话轨;该轮形状只有裁决即
// 恢复一个生产者,boundarySuperseded 与 stalePlannedSuperseded 的作废入口
// 都拒绝 effect-bound 行,不可能带出绑定过 intent 的动作)。两个方向:
//   - 迟到 failed/none/canceled/expired(与裁决同向):账本已是"未发生"
//     终局,不改写动作/轮/聚合任何状态,只落审计后 return nil,让 dispatch
//     层的 intent 覆写、ProcessedMsg 与 ack 正常落地;
//   - 迟到 ok/durable 正证(与裁决反向——判"未发"实则已发,历史最贵教训
//     方向):intent 已由 dispatch 层覆写为 ok(账本真相,保留);动作行
//     CAS 转 sent;轮保持 superseded 终局不回写、不物化后项(恢复形态是
//     "按最新世界状态重新规划",不是重放);尝试把出站行按既有
//     confirmed-action 应用收编进 V4 投影,形状校验合法拒绝时不硬塞,回落
//     "仅动作入账+响亮审计"。
//
// 关键安全语义:动作转 sent 后,该候选人若已按"裁决即恢复"重新规划过新
// 动作,新旧两条是不同 idemKey 的既成事实,不回滚不撤销——防双发靠重新
// 规划时的账本边界自愈(出站行入账后 boundary 在其后),本文件不加拦截。
//
// 幂等:真实重放有三道既有闸——同 msgId 被 ProcessedMsg 挡住;纠正后 cmd
// 已是普通终局(Ok/Failed),dispatch 层对非 Resolved*/Suspect 的终局命令
// 一律 ocLate 丢弃、不再进结算;对话轨另加 sent+superseded 结算短路兜底
// (与既有 sent+completed 短路同形)。

import (
	"errors"
	"strconv"
	"time"

	"recruithelper/client/service/internal/communication"

	"gorm.io/gorm"
)

const auditCategoryLateResultAfterVerdict = "communication_late_result_after_verdict"

// communicationV4LateConfirmShapeRejected 判定确认应用返回的错误是否属于
// "形状校验合法拒绝"。这三类哨兵——聚合游标与该消息之间存在尚未投影的
// 候选人输入(unclaimed 闸)、同 ActionKey 既有应用摘要不符、状态机 reducer
// 按当前状态拒绝该动作——在 applyCommunicationV4ConfirmedActionTx 内全部
// 前置于任何写入(SQLite 单连接事务内聚合行不可能被并发改写,revision CAS
// 在本事务内不可能中途失败),因此按哨兵回落"仅动作入账"不会留下半截
// 投影写入。真实 DB 错误不在此列,照常上抛回滚。
func communicationV4LateConfirmShapeRejected(err error) bool {
	return errors.Is(err, ErrCommunicationV4Conflict) ||
		errors.Is(err, ErrCommunicationV4Invalid) ||
		errors.Is(err, communication.ErrInvalidV4StateTransition)
}

func lateResultAfterVerdictAuditTx(
	tx *gorm.DB,
	conversationRef string,
	detail string,
	at time.Time,
) error {
	return tx.Create(&AuditEntry{
		At: at, Category: auditCategoryLateResultAfterVerdict,
		ConversationRef: conversationRef,
		Detail:          detail,
	}).Error
}

// applyCommunicationV4LegacyLateVerdictPositiveTx 处理对话轨"裁决
// resolvedFailed 后迟到 durable 正证"的纠正落账。调用前提(由调用方判定):
// v4 轮、轮 superseded/resolvedFailedSuperseded、动作
// manualRequired/effectResolvedFailed,且唯一出站 Message 已由本事务内的
// dispatch 纠正路径铸出并通过与通用 Ok 腿相同的一致性检查。
//
// 收编走法的选择理由:confirmed-action 应用对 superseded 轮形状本身没有
// 障碍(它只读聚合与既有应用,不读轮状态),且本轮场景下该消息刚追加在
// 会话尾、游标间无未投影候选人输入时可直接落账,消除无主出站行后患;但
// 迟到场景天然可能撞上三类合法拒绝(候选人夜里又说了话、状态机已随后续
// 事实推进、ActionKey 撞既有应用),拒绝时不硬塞——动作已 sent、消息已在
// 账本,后续建轮边界会包含它(账本边界自愈),投影层缺口以响亮审计留痕、
// 待下轮对账自然收编。轮不回写、后项不物化:裁决即恢复已把未派发残留
// 作废,重新规划归自然触发点。
func applyCommunicationV4LegacyLateVerdictPositiveTx(
	tx *gorm.DB,
	turn DialogueTurn,
	action CommunicationAction,
	v4Plan communication.V4PlannedAction,
	message Message,
	at time.Time,
) error {
	sentAt := &at
	updated := tx.Model(&CommunicationAction{}).
		Where(
			"action_id = ? AND status = ? AND failure_reason = ?",
			action.ActionID,
			CommunicationActionManualRequired,
			"effectResolvedFailed",
		).
		Updates(map[string]any{
			"status": CommunicationActionSent, "failure_reason": "",
			"sent_at": sentAt, "updated_at": at,
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrCommunicationActionConflict
	}
	confirmedAt := sentAt
	if message.TsApproxMs != nil {
		value := time.UnixMilli(*message.TsApproxMs).UTC()
		confirmedAt = &value
	}
	detail := "turn=" + turn.TurnID + " action=" + action.ActionID +
		" messageSeq=" + strconv.FormatInt(message.Seq, 10) +
		" direction=contradictsVerdict"
	_, _, _, err := applyCommunicationV4ConfirmedActionTx(
		tx,
		turn.ProfileID,
		communication.V4ConfirmedAction{
			ActionKey: v4Plan.ActionKey, Kind: v4Plan.Kind,
			MessageSeq: message.Seq, CardMessageSeq: v4Plan.CardMessageSeq,
			SentAt: confirmedAt, Round: v4Plan.Round, Stage: v4Plan.Stage,
		},
		at,
	)
	switch {
	case err == nil:
		detail += " incorporation=confirmed"
	case communicationV4LateConfirmShapeRejected(err):
		// 迟到正证已入账为动作 sent;出站行未能进投影,待下轮对账收编。
		detail += " incorporation=actionOnly"
	default:
		return err
	}
	return lateResultAfterVerdictAuditTx(tx, turn.ConversationRef, detail, at)
}

// applyCommunicationV4EventLateVerdictPositiveTx 是事件动作轨的同款纠正。
// 调用前提(由调用方判定):动作 manualRequired/effectResolvedFailed 且
// intent 已被 dispatch 纠正为 ok。不物化时刻表后项、不建微信承接:裁决即
// 恢复已把依赖残留闭包作废,重新规划归自然触发点;承接是候选人可见动作的
// 前置,少建保守。
func applyCommunicationV4EventLateVerdictPositiveTx(
	tx *gorm.DB,
	action CommunicationV4EventAction,
	intent *EffectIntent,
	sourceInfo communicationV4EventActionSource,
	at time.Time,
) error {
	sentAt := &at
	casToSent := func() error {
		updated := tx.Model(&CommunicationV4EventAction{}).
			Where(
				"action_id = ? AND status = ? AND failure_reason = ?",
				action.ActionID,
				CommunicationV4EventActionManualRequired,
				"effectResolvedFailed",
			).
			Updates(map[string]any{
				"status":         CommunicationV4EventActionSent,
				"failure_reason": "",
				"sent_at":        sentAt,
				"updated_at":     at,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrCommunicationV4EventActionConflict
		}
		return nil
	}
	baseKey := communicationActionPlanKey(action.SemanticActionKey)
	if action.EffectKind == CommunicationV4EventEffectAcceptWechat {
		if action.V4Kind != communication.V4ActionAcceptWechat ||
			intent.ResultMessageSeq != nil {
			return ErrCommunicationActionConflict
		}
		asset, err := contactAssetByEffectIntentTx(tx, intent.IntentID)
		if err != nil {
			return err
		}
		if asset != nil &&
			(asset.ProfileID != action.ProfileID ||
				asset.Platform != intent.Platform ||
				asset.AccountRef != intent.AccountRef ||
				asset.ConversationRef != intent.TargetRef ||
				asset.Kind != contactAssetKindWechat) {
			return ErrCommunicationActionConflict
		}
		if err := casToSent(); err != nil {
			return err
		}
		detail := "eventAction=" + action.ActionID + " direction=contradictsVerdict"
		if asset != nil {
			// 本次结果携带取号,资产已在同事务收编完成,确认可安全落账
			// (取号缺席时不落:延迟取号轨 chat.readWechatExchangeOutcome
			// 之后会携资产重入通用 Ok 腿,彼时动作已 sent、无既有确认应用,
			// 原路完整收编含承接裁决;此处若先落无承接确认,会让该重入撞
			// found-existing 的承接形状冲突)。
			_, _, _, err := applyCommunicationV4ConfirmedActionTx(
				tx,
				action.ProfileID,
				communication.V4ConfirmedAction{
					ActionKey: baseKey, Kind: action.V4Kind,
					MessageSeq: 0, CardMessageSeq: action.CardMessageSeq,
					SentAt: sentAt,
				},
				at,
			)
			switch {
			case err == nil:
				detail += " incorporation=confirmed"
			case communicationV4LateConfirmShapeRejected(err):
				detail += " incorporation=actionOnly"
			default:
				return err
			}
		} else {
			detail += " incorporation=deferredToCollection"
		}
		return lateResultAfterVerdictAuditTx(tx, intent.TargetRef, detail, at)
	}
	// 消息类(replyText 气泡 / 换微信邀请卡):与通用 Ok 腿相同的唯一出站
	// 消息证明。
	if intent.ResultMessageSeq == nil {
		return ErrCommunicationActionConflict
	}
	var message Message
	if err := tx.First(
		&message,
		"outbound_intent_id = ?",
		intent.IntentID,
	).Error; err != nil {
		return err
	}
	if message.RetractedAt != nil ||
		message.Seq != *intent.ResultMessageSeq ||
		!communicationV4EventActionMatchesMessage(action, message) {
		return ErrCommunicationActionConflict
	}
	if err := casToSent(); err != nil {
		return err
	}
	confirmedAt := sentAt
	if message.TsApproxMs != nil {
		value := time.UnixMilli(*message.TsApproxMs).UTC()
		confirmedAt = &value
	}
	detail := "eventAction=" + action.ActionID +
		" messageSeq=" + strconv.FormatInt(message.Seq, 10) +
		" direction=contradictsVerdict"
	_, _, _, err := applyCommunicationV4ConfirmedActionTx(
		tx,
		action.ProfileID,
		communication.V4ConfirmedAction{
			ActionKey: baseKey, Kind: action.V4Kind,
			MessageSeq: message.Seq, CardMessageSeq: action.CardMessageSeq,
			SentAt: confirmedAt, Round: sourceInfo.Round, Stage: sourceInfo.Stage,
		},
		at,
	)
	switch {
	case err == nil:
		detail += " incorporation=confirmed"
	case communicationV4LateConfirmShapeRejected(err):
		detail += " incorporation=actionOnly"
	default:
		return err
	}
	return lateResultAfterVerdictAuditTx(tx, intent.TargetRef, detail, at)
}
