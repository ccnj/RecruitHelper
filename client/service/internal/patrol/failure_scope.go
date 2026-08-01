package patrol

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

// 巡检单人错误隔离（2026-07-27 甲方裁决）：单个候选人处理中的错误不再停掉
// 整个账号轮。分类只看错误产地与手的协议声明，不做字符串猜测：
//   - 账号级/轮级控制信号维持全停（登录/身份/窗口/换代/真人让位/进程退出）；
//   - 手侧命令失败按 retryable 声明分流：yes/afterRecovery 为瞬时（本轮跳过，
//     下轮自然重试），no/manualOnly 为确定性（隔离该会话）；
//   - 脑侧错误（store/投影/状态机）与未知错误一律确定性——它们是账本状态的
//     纯函数，等它自己好等于无限静默重试。误判方向选"可见但要人动一下手"，
//     不选"安静但永久"。
const patrolQuarantineAuditCategory = "patrol_conversation_quarantine"
const patrolTransientSkipAuditCategory = "patrol_transient_skips"

type conversationFailureScope int

const (
	failureScopeRoundFatal conversationFailureScope = iota
	failureScopeSkipRound
	failureScopeQuarantine
)

func classifyConversationFailure(err error) conversationFailureScope {
	if isAccountWideRunFailure(err) {
		return failureScopeRoundFatal
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrDailyWindowExpired) || errors.Is(err, ErrDailyWindowNotOpen) ||
		errors.Is(err, ErrActorPaused) || errors.Is(err, ErrActorGenerationChanged) ||
		errors.Is(err, ErrRoundSupersededBySourcingBatch) ||
		errors.Is(err, store.ErrAccountNotFound) {
		return failureScopeRoundFatal
	}
	if typed := runError(err); typed != nil {
		if typed.Code == protocol.ErrCodeUserActive {
			// 真人正在操作页面不是单人的问题；继续跑下一个人会和真人抢页面。
			return failureScopeRoundFatal
		}
		// 目标暂离当前窗口 / 只读后置未确认：既有巡检语义就是本轮跳过，
		// 与手声明的 retryable 无关（retryable 描述的是本条命令，不是这个人）。
		if typed.Code == protocol.ErrCodeTargetNotFound ||
			typed.Code == protocol.ErrCodePostconditionUnconfirmed {
			return failureScopeSkipRound
		}
		switch typed.Retryable {
		case protocol.RetryableYes, protocol.RetryableAfterRecovery:
			return failureScopeSkipRound
		}
		return failureScopeQuarantine
	}
	// 脑侧的已知瞬时例外：它们不是稳定账本的纯函数——版本冲突来自与
	// 事件摄入的良性写竞争（账本已变，下轮重读即收敛）；空收编快照与已
	// 收编空快照都依赖活页面观察（真机 2026-07-28：IM 页刚导航后的同步
	// 窗口内平台历史接口可能空成功，下轮重读通常恢复出历史）。跳过即
	// 保持脏、本轮不消化不派发，无上界（2026-07-28 甲方裁决不加界）。
	//
	// AI 预留冲突同属此列（2026-08-01 事故后并入）：它是"同一身份下已有一条
	// 事实不同的预留"这种状态冲突，不是账本状态的纯函数，也没有任何外部副
	// 作用发生——AI 尚未被调用。把它判成确定性错误会因为一次正常的职位配置
	// 换代冻结整批候选人（当日 79 人）。预留身份判定已同步收窄，这里是兜底：
	// 即使将来又有别的字段跟着业务变，最坏也只是推迟一轮并留下审计。
	if errors.Is(err, store.ErrConversationVersionConflict) ||
		errors.Is(err, syncledger.ErrAdoptionSnapshotEmpty) ||
		errors.Is(err, syncledger.ErrTrackedSnapshotEmpty) ||
		errors.Is(err, store.ErrAIInvocationConflict) {
		return failureScopeSkipRound
	}
	return failureScopeQuarantine
}

// conversationFailureClass 产出有界、无 PII 的错误类别标签，用于隔离原因、
// 审计 detail 与日志。手侧错误取协议 code/reason；脑侧错误取已知哨兵名。
func conversationFailureClass(err error) string {
	if typed := runError(err); typed != nil {
		class := "hand:" + string(typed.Code)
		if typed.Reason != "" {
			class += "/" + string(typed.Reason)
		}
		return class
	}
	switch {
	case errors.Is(err, syncledger.ErrSourceKeySemanticConflict),
		errors.Is(err, store.ErrMessageSourceKeyConflict):
		return "sourceIdentityConflict"
	case errors.Is(err, syncledger.ErrUnsafeMessageClassificationCorrection),
		errors.Is(err, store.ErrMessageClassificationCorrectionUnsafe):
		return "classificationCorrectionUnsafe"
	case errors.Is(err, store.ErrCommunicationV4Conflict):
		return "communicationV4Conflict"
	case errors.Is(err, store.ErrCommunicationV4Corrupt):
		return "communicationV4Corrupt"
	case errors.Is(err, store.ErrDialogueTurnState):
		return "dialogueTurnState"
	case errors.Is(err, store.ErrConversationVersionConflict):
		return "conversationVersionConflict"
	case errors.Is(err, syncledger.ErrAdoptionSnapshotEmpty):
		return "adoptionSnapshotEmpty"
	case errors.Is(err, syncledger.ErrTrackedSnapshotEmpty):
		return "trackedSnapshotEmpty"
	case errors.Is(err, store.ErrAIInvocationConflict):
		return "aiInvocationConflict"
	}
	return "unclassified"
}

type conversationSkipNote struct {
	ConversationRef string
	Class           string
}

// settleConversationFailure 把单个候选人处理失败按裁决分流。
// 返回 (true, nil) 表示已就地收束（本轮跳过或已隔离），轮继续下一个人；
// 返回 (false, err) 表示轮级失败，调用方按既有失败路径返回。
func (a *roundActor) settleConversationFailure(
	ctx context.Context,
	key store.ConversationKey,
	profileID string,
	cause error,
) (bool, error) {
	if classifyConversationFailure(cause) == failureScopeRoundFatal {
		a.handleCommandFailure(cause)
		return false, cause
	}
	// 隔离/跳过前复核派发环境：手离线、账号暂停、窗口关闭、批次换代等
	// 环境级故障不得记到当事人头上。
	if envErr := a.ensureDispatchAllowed(ctx); envErr != nil {
		return false, envErr
	}
	class := conversationFailureClass(cause)
	if classifyConversationFailure(cause) == failureScopeSkipRound {
		a.transientSkips = append(a.transientSkips, conversationSkipNote{
			ConversationRef: key.ConversationRef, Class: class,
		})
		slog.Warn("巡检瞬时错误，本轮跳过该候选人",
			"conversationRef", key.ConversationRef, "class", class, "err", cause)
		return true, nil
	}
	return a.quarantineConversation(key, profileID, class, cause)
}

func (a *roundActor) quarantineConversation(
	key store.ConversationKey,
	profileID string,
	class string,
	cause error,
) (bool, error) {
	reason := "patrolQuarantine:" + class
	newlyMarked, err := a.manager.store.QuarantineConversationPatrol(
		key, reason, a.manager.now(),
	)
	if err != nil {
		// 隔离动作本身失败（会话行缺失、库不可写）：无法安全隔离，轮级失败。
		return false, errors.Join(cause, err)
	}
	if profileID == "" {
		profile, lookupErr := a.manager.store.CandidateProfileByConversation(key)
		if lookupErr != nil {
			return false, errors.Join(cause, lookupErr)
		}
		if profile != nil {
			profileID = profile.ProfileID
		}
	}
	frozeProfile := false
	if profileID != "" {
		markErr := a.manager.store.MarkCommunicationV4AutomationManualRequired(
			profileID, reason, a.manager.now(),
		)
		switch {
		case markErr == nil:
			frozeProfile = true
		case errors.Is(markErr, store.ErrCommunicationV4Missing),
			errors.Is(markErr, store.ErrCommunicationV4Conflict),
			errors.Is(markErr, store.ErrCommunicationV4Corrupt):
			// 没有可冻结的聚合、聚合已处于人工/终态，或聚合投影正处于
			// 漂移/损坏（档案已推进而聚合尚未投影）：会话级标记已经完成
			// 隔离，坏聚合本身正是需要人工处理的对象，不得反过来把整轮
			// 打死。
		default:
			return false, errors.Join(cause, markErr)
		}
	}
	if newlyMarked {
		if auditErr := a.manager.store.AppendAudit(&store.AuditEntry{
			At: a.manager.now(), Category: patrolQuarantineAuditCategory,
			Platform: key.Platform, AccountRef: key.AccountRef,
			ConversationRef: key.ConversationRef, RoundID: a.roundID,
			Detail: fmt.Sprintf(
				"status=quarantined class=%s profileFrozen=%t", class, frozeProfile,
			),
		}); auditErr != nil {
			return false, errors.Join(cause, auditErr)
		}
	}
	slog.Warn("巡检确定性错误，已隔离该候选人会话，轮继续",
		"conversationRef", key.ConversationRef, "profileID", profileID,
		"class", class, "newlyMarked", newlyMarked, "err", cause)
	return true, nil
}

// appendTransientSkipSummary 在轮收尾留一条有界汇总审计，保证瞬时跳过不是
// 完全隐形的。写入失败只响亮记日志，不改变轮的收尾结果。
func (a *roundActor) appendTransientSkipSummary() {
	if len(a.transientSkips) == 0 {
		return
	}
	const maxListed = 8
	parts := make([]string, 0, maxListed+1)
	for index, note := range a.transientSkips {
		if index == maxListed {
			parts = append(parts, "…")
			break
		}
		parts = append(parts, note.ConversationRef+":"+note.Class)
	}
	if err := a.manager.store.AppendAudit(&store.AuditEntry{
		At: a.manager.now(), Category: patrolTransientSkipAuditCategory,
		Platform: a.account.Platform, AccountRef: a.account.AccountRef,
		RoundID: a.roundID,
		Detail: fmt.Sprintf(
			"status=skipped count=%d refs=%s",
			len(a.transientSkips), strings.Join(parts, ","),
		),
	}); err != nil {
		slog.Error("瞬时跳过汇总审计写入失败",
			"roundID", a.roundID, "count", len(a.transientSkips), "err", err)
	}
}
