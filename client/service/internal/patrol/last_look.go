package patrol

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// 临走看一眼(2026-08-13 甲方立案)。候选人最容易秒回的时刻,恰是我们刚处理完
// 他之后的几十秒;而本轮列表快照读在他回话之前,按原节奏这条回复要等下一轮
// 巡检才被发现。巡检切换到另一个脏会话之前(该切换会顶掉页面上还开着的窗口),
// 先认一眼当前开着的会话:开着的是另一个已跟踪、已就绪的候选人,就用既有的
// 对账→v4 处理链就地处理——有新消息按正常流程回,没有就是零动作——然后照
// 原计划切换。
//
// 整个检查是锦上添花:识别失败、认不出、未就绪、被隔离,一律记日志放行,不得
// 挡住巡检主流程,失效方向永远是"当它不存在";就地处理链内的失败按
// settleConversationFailure 与正常轮内处理同规则分流。不新增任何 effectful 面,
// 账本/idemKey/证词/验证读全部走既有路径。
const lastLookAuditCategory = "patrol_last_look"

// lastLookBeforeSwitch 返回非 nil 仅代表轮级失败;一切"检查没做成"都以 nil
// 放行,调用方照原计划切换会话。调用方在返回后须自查 classificationCorrected,
// 与正常单会话处理的停止边界同规则。
func (a *roundActor) lastLookBeforeSwitch(ctx context.Context, targetRef string) error {
	if err := a.setStage("lastLook"); err != nil {
		return err
	}
	current, err := invokePrimitiveDirect[protocol.ChatIdentifyCurrentConversationData](
		ctx,
		a,
		protocol.PrimChatIdentifyCurrentConversation,
		protocol.ChatIdentifyCurrentConversationArgs{},
	)
	if err != nil {
		// 环境级失败(身份/换代/暂停/日窗/真人操作)照常升级为轮级;页面级
		// 失败不在这里恢复页面——放行后目标会话的正常读取链自带恢复。
		if classifyConversationFailure(err) == failureScopeRoundFatal {
			a.handleCommandFailure(err)
			return err
		}
		slog.Info("临走看一眼:识别当前会话失败,照原计划切换",
			"targetRef", targetRef, "err", err)
		return nil
	}
	currentRef := strings.TrimSpace(current.ConversationRef)
	if currentRef == "" || currentRef == targetRef {
		return nil
	}
	key := store.ConversationKey{
		Platform: a.account.Platform, AccountRef: a.account.AccountRef,
		ConversationRef: currentRef,
	}
	row, err := a.manager.store.ConversationByKey(key)
	if err != nil || row == nil {
		slog.Info("临走看一眼:当前会话不在本地账本,照原计划切换",
			"currentRef", currentRef, "targetRef", targetRef, "err", err)
		return nil
	}
	// 已被巡检隔离的会话在人工解除前不再自动对账或推进(2026-07-27 甲方裁决),
	// 临走检查不得成为绕过隔离的第二入口。
	if row.PatrolQuarantinedAt != nil {
		slog.Info("临走看一眼:当前会话处于巡检隔离,照原计划切换",
			"currentRef", currentRef, "targetRef", targetRef)
		return nil
	}
	profile, err := a.manager.store.CandidateProfileByConversation(key)
	if err != nil || profile == nil || profile.ConversationRef == nil ||
		*profile.ConversationRef != currentRef {
		slog.Info("临走看一眼:当前会话未绑定候选人档案,照原计划切换",
			"currentRef", currentRef, "targetRef", targetRef, "err", err)
		return nil
	}
	if profile.BackendJobID == nil || strings.TrimSpace(*profile.BackendJobID) == "" {
		slog.Info("临走看一眼:当前候选人未绑定职位,照原计划切换",
			"currentRef", currentRef, "targetRef", targetRef)
		return nil
	}
	head, err := a.manager.store.CurrentLegacyJobAIContextByBackendJobID(
		*profile.BackendJobID,
	)
	if err != nil || head == nil {
		slog.Info("临走看一眼:当前候选人职位上下文缺失,照原计划切换",
			"currentRef", currentRef, "targetRef", targetRef, "err", err)
		return nil
	}
	target, ready, err := a.manager.store.CommunicationTargetForProfile(profile.ProfileID)
	if err != nil || !ready || target == nil ||
		target.Conversation.ConversationRef != currentRef {
		slog.Info("临走看一眼:当前候选人沟通目标未就绪,照原计划切换",
			"currentRef", currentRef, "targetRef", targetRef, "err", err)
		return nil
	}
	ledger, err := a.manager.store.MessagesForConversation(key)
	if err != nil {
		slog.Info("临走看一眼:当前会话账本读取失败,照原计划切换",
			"currentRef", currentRef, "targetRef", targetRef, "err", err)
		return nil
	}

	// 与显式"处理当前会话"入口同一姿态:窗口已经开着,全程 requireCurrent
	// 就地读写,不得导航离开;处理完成后恢复原值,目标会话照旧走导航读取。
	prior := a.requireCurrentThread
	a.requireCurrentThread = true
	defer func() { a.requireCurrentThread = prior }()

	projection, err := a.reconcileConversation(ctx, dirtyConversation{
		conversation: target.Conversation,
		ledger:       ledger,
	})
	newMessages := len(projection.Messages)
	cardTransitions := len(projection.CardTransitions)
	if newMessages != 0 || cardTransitions != 0 {
		a.projection = append(a.projection, projection)
	}
	if err != nil {
		slog.Warn("临走看一眼:就地对账失败,按单人分流后照原计划切换",
			"currentRef", currentRef, "targetRef", targetRef, "err", err)
		handled, fatalErr := a.settleConversationFailure(ctx, key, profile.ProfileID, err)
		if !handled {
			return fatalErr
		}
		return nil
	}
	if a.classificationCorrected {
		return a.appendLastLookAudit(
			currentRef, targetRef, "classificationCorrected", newMessages, cardTransitions,
		)
	}
	if err := a.processCommunicationV4Profile(ctx, profile.ProfileID); err != nil {
		slog.Warn("临走看一眼:就地档案推进失败,按单人分流后照原计划切换",
			"currentRef", currentRef, "targetRef", targetRef, "err", err)
		handled, fatalErr := a.settleConversationFailure(ctx, key, profile.ProfileID, err)
		if !handled {
			return fatalErr
		}
		return nil
	}
	return a.appendLastLookAudit(currentRef, targetRef, "processed", newMessages, cardTransitions)
}

// appendLastLookAudit 是临走检查存废裁决的数据来源:status=processed 行的总数
// 是尝试数,newMessages>0 的行是命中数(真机命中率,防护成本预算第 8 条)。
func (a *roundActor) appendLastLookAudit(
	currentRef string,
	targetRef string,
	status string,
	newMessages int,
	cardTransitions int,
) error {
	return a.manager.store.AppendAudit(&store.AuditEntry{
		At: a.manager.now(), Category: lastLookAuditCategory,
		Platform: a.account.Platform, AccountRef: a.account.AccountRef,
		ConversationRef: currentRef, RoundID: a.roundID,
		Detail: fmt.Sprintf(
			"status=%s target=%s newMessages=%d cardTransitions=%d",
			status, targetRef, newMessages, cardTransitions,
		),
	})
}
