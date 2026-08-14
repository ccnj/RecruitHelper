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

// 巡检单人错误分流（2026-07-27 甲方裁决立案，2026-08-02 停机点体检战役修订
// 默认方向）：单个候选人处理中的错误不停整个账号轮。分类只看错误产地与手的
// 协议声明，不做字符串猜测：
//   - 账号级/轮级控制信号维持全停（登录/身份/窗口/换代/真人让位/进程退出）；
//   - 手侧命令失败按 retryable 声明分流：隔离必须有正面证词——显式
//     no/manualOnly 才隔离（这是手的协议级"需要人"证词，且该失败可能发生在
//     effect 派发之后，不随默认反转）；yes/afterRecovery 为瞬时（本轮跳过，
//     下轮自然重试）；**证词缺席（retryable 为空）是未知，走未知的默认：本轮
//     跳过**（2026-08-14 甲方裁决补全:此前证词缺席落进隔离默认分支,一次瞬时
//     超时把候选人永久冻结;脑合成的错误现已由 appbridge runErrorFromLeaf 按
//     契约表补齐 retryable,effectful 超时仍解析为 manualOnly、隔离不放松）；
//   - 脑主动取消自己的命令（CANCELED_BY_BRAIN:租约到期/换代/停机）是环境
//     事件,不是这个人的状态坏了;契约给它的 retryable=no 说的是"别重发这条
//     命令",不是"这个人需要人工",特判本轮跳过；
//   - 脑侧错误与未知错误默认本轮跳过、下轮重读（2026-08-02 反转，废止 07-27
//     "脑侧一律确定性隔离"）：它们全部发生在世界未被改动、或重派已被
//     WAL/idemKey/动作状态机结构性挡住的位置，隔离换来的是"安静但永久"的
//     冻结，与"默认动词是继续"的大方向相反。跳过保持脏、留轮收尾汇总审计；
//     长期不收敛由人工介入率数据裁决，不由本分类器预防。
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
		// 脑取消自己的命令是环境事件（租约到期/换代/停机），不是这个人的
		// 状态坏了。契约 retryable=no 描述命令级重试，不构成人级隔离证词。
		if typed.Code == protocol.ErrCodeCanceledByBrain {
			return failureScopeSkipRound
		}
		// 隔离必须有正面证词（2026-08-14 甲方裁决）：显式 no/manualOnly 才
		// 隔离；证词缺席是未知，未知走 2026-08-02 的默认方向——本轮跳过。
		// 真正防多发的是账本/idemKey/手证词（全是成员级闸），本分类器只是
		// 毯子；effectful 超时经契约解析仍带 manualOnly，不依赖默认分支。
		switch typed.Retryable {
		case protocol.RetryableNo, protocol.RetryableManualOnly:
			return failureScopeQuarantine
		}
		return failureScopeSkipRound
	}
	// 脑侧与未知错误：默认本轮跳过（2026-08-02 甲方裁决反转；07-28 起逐案
	// 特赦的瞬时白名单——版本冲突、空收编快照、AI 预留冲突等——随默认反转
	// 整体并入，不再单列）。
	return failureScopeSkipRound
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
