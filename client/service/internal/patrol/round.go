package patrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

type roundActor struct {
	manager                 *Manager
	account                 *store.Account
	hand                    HandState
	roundID                 string
	trigger                 string
	now                     time.Time
	ensureUsed              bool
	classificationCorrected bool
	requireCurrentThread    bool
	sourcingBatchIDAtStart  string
	superseded              bool
	listTraversalIncomplete bool
	checkedListFingerprints map[string]string
	unreadRetryDeferred     bool
	unreadAttemptedRefs     map[string]struct{}
	// 上次插队判定的结论，用于把会话边界上的重复判定压成变化沿日志。
	lastUnreadDecision string
	projection         []ConversationProjection
	transientSkips     []conversationSkipNote
	// 本轮取得正证的接受微信动作所属档案：接受是唯一"我方先于账本知情"的
	// 动作，需要在同一轮定向重对账把 259 结果补进账本（立案 4.3）。
	wechatAcceptedProfiles map[string]struct{}
}

type threadSnapshot struct {
	messages      []syncledger.SnapshotMessage
	peer          *protocol.PeerSummary
	reachedTop    bool
	anchorMatched bool
}

type dirtyConversation struct {
	conversation        store.Conversation
	ledger              []store.Message
	listHintKey         listHintVerificationKey
	listHintFingerprint string
}

type conversationListPage struct {
	sessions []protocol.ConversationSummary
	complete bool
}

type conversationListPageOutcome uint8

const (
	conversationListPageContinue conversationListPageOutcome = iota
	conversationListPageSwitchUnread
	conversationListPageSwitchAll
	conversationListPageStop
)

func (m *Manager) runAccountRound(ctx context.Context, account *store.Account, hand HandState, trigger string, now time.Time) RoundOutcome {
	key := store.AccountKey{Platform: account.Platform, AccountRef: account.AccountRef}
	roundID := m.config.NewRoundID()
	outcome := RoundOutcome{Key: key, RoundID: roundID, Trigger: trigger, Status: "failed"}
	if roundID == "" {
		outcome.Err = errors.New("NewRoundID 返回空值")
		return outcome
	}
	// 在线性化的开跑点消费本轮之前的 dirty，并把下一次巡检先放到正常
	// 周期。此后到达的事件会重新置 dirty/拉前 next；finish 只在没有新
	// dirty 时覆盖它。这样事件恰落在长轮次中也不会被成功收尾抹掉。
	m.mu.Lock()
	err := m.store.BeginPatrolRound(&store.PatrolRound{
		Platform: account.Platform, AccountRef: account.AccountRef, RoundID: roundID,
		Trigger: trigger, Status: "running", Stage: "starting", StartedAt: now,
	}, now.Add(m.config.PatrolInterval))
	m.mu.Unlock()
	if err != nil {
		outcome.Err = err
		return outcome
	}

	actor := &roundActor{
		manager: m, account: account, hand: hand, roundID: roundID, trigger: trigger, now: now,
		requireCurrentThread: trigger == TriggerCurrentConversation,
	}
	// 按《24点边界裁决-2026-07-28》不再以 24:00 wall-clock 超时取消等待中
	// 的 dispatcher：已发首条可见动作的链要跨点收束到终局，日界收束由链首
	// 与下一候选人边界的 ensureDispatchAllowed 日界复核承担。
	// actor 短锁覆盖所有本地判定、Start 与账本提交，但 invokePrimitiveDirect
	// 会在 Wait 期间主动释放它。事件/暂停/改绑因此只能线性化在命令启动前
	// 或启动后，绝不会钻进“门禁已过、socket 尚未送入”的缝隙。
	m.mu.Lock()
	func() {
		defer m.mu.Unlock()
		if err = actor.freezeSourcingBatchGeneration(); err == nil {
			err = actor.execute(ctx)
		}
	}()
	if errors.Is(err, ErrDailyWindowExpired) {
		_ = m.pauseAccount(key, PauseDailyExpired, m.now())
	}
	m.mu.Lock()
	err, generationErr := actor.resolveFinishGeneration(err)
	finishErr := actor.finish(err)
	m.mu.Unlock()
	if generationErr != nil {
		finishErr = errors.Join(finishErr, generationErr)
	}
	outcome.EnsureUsed = actor.ensureUsed
	outcome.Projections = actor.projection
	outcome.Err = err
	if err == nil {
		outcome.Status = "ok"
	}
	if finishErr != nil {
		if outcome.Err == nil {
			outcome.Err = finishErr
			outcome.Status = "failed"
		} else {
			outcome.Err = errors.Join(outcome.Err, finishErr)
		}
	}
	if actor.ensureUsed {
		outcome.Trigger += surfaceRecoverySuffix
	}
	return outcome
}

func (a *roundActor) execute(ctx context.Context) error {
	if err := a.ensureWithinDailyWindow(); err != nil {
		return err
	}
	if a.account.PrincipalFingerprint == nil || *a.account.PrincipalFingerprint == "" || a.account.BoundHandID == "" {
		_ = a.manager.pauseAccount(a.key(), PauseIdentityInvalid, a.now)
		return ErrAccountNotBound
	}
	if a.account.IdentityState == store.IdentityInvalid || a.account.IdentityState == store.IdentityUnbound {
		_ = a.manager.pauseAccount(a.key(), PauseIdentityInvalid, a.now)
		return ErrIdentityInvalid
	}

	if a.needsProbe() {
		if err := a.setStage("probing"); err != nil {
			return err
		}
		if err := a.probeAndVerify(ctx); err != nil {
			a.handleCommandFailure(err)
			return err
		}
	}
	// 正式采集只由唯一非终态 SourcingBatch 授权，Account 上的旧
	// SourcingEnabled 不再是第二份业务真相。一个采集 round 持续消费
	// 推荐窗口直到达标、明确阻塞或命令失败，且绝不进入评分/招呼/IM。
	batch, err := a.manager.store.ActiveSourcingBatch(a.key())
	if err != nil {
		return err
	}
	if a.trigger == TriggerCurrentConversation {
		if batch != nil {
			return ErrCurrentConversationSourcingActive
		}
		return a.executeCurrentConversationOnce(ctx)
	}
	if batch != nil {
		if err := a.runSourcingBatch(ctx, batch); err != nil {
			a.handleCommandFailure(err)
			return err
		}
		return nil
	}

	filter := protocol.ListFilterAll
	move := protocol.ListWindowMoveReset
	a.checkedListFingerprints = make(map[string]string)
	_, err = a.refreshHandState(ctx)
	if err != nil {
		return err
	}
	if a.manager.config.MaxPages >= 2 && a.beginUnreadPass(unreadDecisionAtRoundStart) {
		filter = protocol.ListFilterUnread
	}
	windowsRead := 0
	startAllScan := func() {
		filter = protocol.ListFilterAll
		move = protocol.ListWindowMoveReset
		// Returning from unread restarts the physical all-list traversal, but
		// keeps this actor's ref -> fingerprint decisions. Overlapping rows are
		// skipped while a conversation whose visible hint changed is eligible
		// for another authoritative read.
	}
	for windowsRead < a.manager.config.MaxPages {
		// Entering unread reserves one actual all-list read to close the
		// platform filter. The reserve is part of the same MaxPages budget.
		if filter == protocol.ListFilterUnread &&
			windowsRead >= a.manager.config.MaxPages-1 {
			a.unreadRetryDeferred = true
			startAllScan()
			continue
		}
		if err := a.setStage("readingList"); err != nil {
			return err
		}
		page, err := a.readListPage(ctx, filter, move)
		windowsRead++
		if err != nil {
			if filter == protocol.ListFilterUnread &&
				isRunError(err, protocol.ErrCodeElementUnresolved) {
				if auditErr := a.appendUnreadPatrolAudit(
					"status=inconsistent reason=unreadListElementUnresolved",
				); auditErr != nil {
					return auditErr
				}
				a.unreadRetryDeferred = true
				startAllScan()
				continue
			}
			if isRunError(err, protocol.ErrCodeUserActive) {
				// 用户在窗口移动期间改变了页面。已经完成的会话事实照常
				// 保留，本轮按部分遍历收束；下一巡检重新 reset，不复用
				// 任何页面位置或跨命令快照。
				a.listTraversalIncomplete = true
				return nil
			}
			a.handleCommandFailure(err)
			return err
		}
		outcome, err := a.processConversationListPage(
			ctx,
			page,
			filter,
			windowsRead < a.manager.config.MaxPages-1,
		)
		if err != nil {
			return err
		}
		switch outcome {
		case conversationListPageSwitchUnread:
			filter = protocol.ListFilterUnread
			move = protocol.ListWindowMoveReset
			continue
		case conversationListPageSwitchAll:
			startAllScan()
			continue
		case conversationListPageStop:
			// 工作流候选人边界与分类修正是明确停止，不得被误解释成
			// fresh 续扫后重新领取候选人。
			return nil
		case conversationListPageContinue:
			// 当前快照仍可继续分页。
		default:
			return errors.New("未知会话列表处理结果")
		}
		if a.classificationCorrected {
			return nil
		}
		if page.complete {
			if filter == protocol.ListFilterUnread {
				// The unread page processor normally returns SwitchAll here.
				// Keep a defensive close so no future branch can leave the
				// platform filter active at round completion.
				a.unreadRetryDeferred = true
				startAllScan()
				continue
			}
			return nil
		}
		move = protocol.ListWindowMoveNext
	}
	// MaxPages 仍是单轮窗口总量的高位止损。预算耗尽只表示部分遍历
	// 已经安全收束；下一轮重新 reset，不持久化或猜测页面位置。
	a.listTraversalIncomplete = true
	return nil
}

func (a *roundActor) processConversationListPage(
	ctx context.Context,
	page conversationListPage,
	filter protocol.ListFilter,
	canEnterUnread bool,
) (conversationListPageOutcome, error) {
	entries, err := listEntries(page.sessions)
	if err != nil {
		return conversationListPageContinue, err
	}
	if err := a.manager.store.SaveConversationList(store.SaveConversationListRequest{
		Platform: a.account.Platform, AccountRef: a.account.AccountRef, RoundID: a.roundID,
		ObservedAt: a.manager.now(), Complete: page.complete, Entries: entries,
	}); err != nil {
		return conversationListPageContinue, err
	}
	lateObservations := make([]store.LateGreetingConversationObservation, 0, len(entries))
	for _, entry := range entries {
		if entry.PlatformUserRef == "" {
			continue
		}
		lateObservations = append(lateObservations, store.LateGreetingConversationObservation{
			ConversationRef: entry.ConversationRef,
			PlatformUserRef: entry.PlatformUserRef,
		})
	}
	if _, err := a.manager.store.LateBindGreetedConversations(store.LateBindGreetedConversationsRequest{
		Platform: a.account.Platform, AccountRef: a.account.AccountRef, RoundID: a.roundID,
		ObservedAt: a.manager.now(), Conversations: lateObservations,
	}); err != nil {
		return conversationListPageContinue, err
	}
	if _, err := a.adoptInboundConversationProfiles(page.sessions); err != nil {
		return conversationListPageContinue, err
	}
	if err := a.ensureCommunicationV4Roots(); err != nil {
		return conversationListPageContinue, err
	}
	if err := a.ensureSourcingCommunicationContexts(); err != nil {
		return conversationListPageContinue, err
	}
	if err := a.ensureSourcingCommunicationResumes(); err != nil {
		return conversationListPageContinue, err
	}
	tracked, err := a.trackedConversationsByRef()
	if err != nil {
		return conversationListPageContinue, err
	}

	if filter == protocol.ListFilterAll && a.classificationCorrected {
		// A classification correction encountered in unread mode still needs
		// this actual all-list read to close the page filter, but it must not
		// authorize another candidate after the correction stop boundary.
		return conversationListPageStop, nil
	}
	if filter == protocol.ListFilterUnread {
		return a.processUnreadConversationListPage(ctx, page, tracked)
	}
	for _, summary := range page.sessions {
		fingerprint := a.listFingerprint(summary)
		if checked, exists := a.checkedListFingerprints[summary.ConversationRef]; exists && checked == fingerprint {
			continue
		}
		// 已被巡检隔离的会话在人工解除前不再自动对账或推进（2026-07-27
		// 甲方裁决），也不消耗工作流候选人闸。
		if conversation, exists := tracked[summary.ConversationRef]; exists &&
			conversation.PatrolQuarantinedAt != nil {
			a.checkedListFingerprints[summary.ConversationRef] = fingerprint
			continue
		}
		if canEnterUnread {
			_, stateErr := a.refreshHandState(ctx)
			if stateErr != nil {
				return conversationListPageContinue, stateErr
			}
			if a.beginUnreadPass(unreadDecisionAtBoundary) {
				return conversationListPageSwitchUnread, nil
			}
		}
		allowed, gateErr := a.mayStartNextConversation(ctx)
		if gateErr != nil {
			return conversationListPageContinue, gateErr
		}
		if !allowed {
			// 当前候选人若有动作链，已经在上一轮循环中完整收束。这里
			// 只是不再领取下一位；外层随后释放 tickMu，让工作流编排器
			// 在线性化边界切换页面或结束任务。
			return conversationListPageStop, nil
		}
		// Mark only after the unread and workflow gates. If unread preempts
		// this candidate, returning to all/reset must still be allowed to
		// process it. Once claimed, however, a local failure cannot spin on
		// the same unchanged row in an overlapping window.
		a.checkedListFingerprints[summary.ConversationRef] = fingerprint

		key := store.ConversationKey{
			Platform: a.account.Platform, AccountRef: a.account.AccountRef,
			ConversationRef: summary.ConversationRef,
		}
		dirty, err := a.detectDirtySummary(
			summary,
			false,
			tracked,
		)
		if err != nil {
			handled, fatalErr := a.settleConversationFailure(ctx, key, "", err)
			if !handled {
				return conversationListPageContinue, fatalErr
			}
			continue
		}
		readCurrent := dirty != nil
		if readCurrent {
			dirtyConversation := *dirty
			if err := a.setStage("readingThread"); err != nil {
				return conversationListPageContinue, err
			}
			projection, err := a.reconcileConversation(ctx, dirtyConversation)
			if len(projection.Messages) != 0 || len(projection.CardTransitions) != 0 {
				a.projection = append(a.projection, projection)
			}
			if err != nil {
				// 目标暂离窗口等瞬时错误只本轮跳过；确定性错误隔离该会话；
				// 账号级信号仍全停（2026-07-27 甲方裁决）。当前 fingerprint
				// 已尝试，重叠窗口不会围绕它打转。
				handled, fatalErr := a.settleConversationFailure(ctx, key, "", err)
				if !handled {
					return conversationListPageContinue, fatalErr
				}
				continue
			}
			a.manager.markListHintVerified(
				dirtyConversation.listHintKey,
				dirtyConversation.listHintFingerprint,
			)
			if a.classificationCorrected {
				return conversationListPageStop, nil
			}
		}
		profile, err := a.manager.store.CandidateProfileByConversation(key)
		if err != nil {
			handled, fatalErr := a.settleConversationFailure(ctx, key, "", err)
			if !handled {
				return conversationListPageContinue, fatalErr
			}
			continue
		}
		if profile == nil {
			continue
		}
		if err := a.prepareInboundConversationProfile(
			ctx,
			*profile,
		); err != nil {
			handled, fatalErr := a.settleConversationFailure(ctx, key, profile.ProfileID, err)
			if !handled {
				return conversationListPageContinue, fatalErr
			}
			continue
		}
		if err := a.processCommunicationV4Profile(ctx, profile.ProfileID); err != nil {
			handled, fatalErr := a.settleConversationFailure(ctx, key, profile.ProfileID, err)
			if !handled {
				return conversationListPageContinue, fatalErr
			}
			continue
		}
		if a.classificationCorrected {
			return conversationListPageStop, nil
		}
	}
	return conversationListPageContinue, nil
}

const unreadPatrolAuditCategory = "unread_patrol"

func (a *roundActor) processUnreadConversationListPage(
	ctx context.Context,
	page conversationListPage,
	tracked map[string]store.Conversation,
) (conversationListPageOutcome, error) {
	for _, summary := range page.sessions {
		if _, attempted := a.unreadAttemptedRefs[summary.ConversationRef]; attempted {
			continue
		}
		// 已被巡检隔离的会话在人工解除前不再自动打开或对账（2026-07-27
		// 甲方裁决）；其未读气泡由 pass 收尾基线吸收。
		if conversation, exists := tracked[summary.ConversationRef]; exists &&
			conversation.PatrolQuarantinedAt != nil {
			a.unreadAttemptedRefs[summary.ConversationRef] = struct{}{}
			continue
		}
		allowed, gateErr := a.mayStartNextConversation(ctx)
		if gateErr != nil {
			return conversationListPageContinue, gateErr
		}
		if !allowed {
			return conversationListPageStop, nil
		}

		// The attempt marker precedes the intrusive action. A locally failed
		// open/read therefore cannot spin on the same residual unread row in
		// this complete scan.
		a.unreadAttemptedRefs[summary.ConversationRef] = struct{}{}
		key := store.ConversationKey{
			Platform: a.account.Platform, AccountRef: a.account.AccountRef,
			ConversationRef: summary.ConversationRef,
		}
		profile, err := a.manager.store.CandidateProfileByConversation(key)
		if err != nil {
			handled, fatalErr := a.settleConversationFailure(ctx, key, "", err)
			if !handled {
				return conversationListPageContinue, fatalErr
			}
			continue
		}
		if profile == nil {
			if err := a.openUnreadConversationWithoutAutomation(
				ctx,
				summary.ConversationRef,
			); err != nil {
				handled, fatalErr := a.settleConversationFailure(ctx, key, "", err)
				if !handled {
					return conversationListPageContinue, fatalErr
				}
			}
			continue
		}
		_, ready, targetErr := a.manager.store.CommunicationTargetForProfile(
			profile.ProfileID,
		)
		// A retained profile without a V4 root is not automation-ready. It
		// still has to be opened once to clear unread, but may not be promoted
		// into resume capture, state-machine work or AI from this cleanup pass.
		if targetErr != nil && !errors.Is(targetErr, store.ErrCommunicationV4Missing) {
			handled, fatalErr := a.settleConversationFailure(ctx, key, profile.ProfileID, targetErr)
			if !handled {
				return conversationListPageContinue, fatalErr
			}
			continue
		}
		if !ready {
			if err := a.openUnreadConversationWithoutAutomation(
				ctx,
				summary.ConversationRef,
			); err != nil {
				handled, fatalErr := a.settleConversationFailure(ctx, key, profile.ProfileID, err)
				if !handled {
					return conversationListPageContinue, fatalErr
				}
			}
			continue
		}

		dirty, err := a.detectDirtySummary(
			summary,
			true,
			tracked,
		)
		if err != nil {
			handled, fatalErr := a.settleConversationFailure(ctx, key, profile.ProfileID, err)
			if !handled {
				return conversationListPageContinue, fatalErr
			}
			continue
		}
		if dirty == nil {
			handled, fatalErr := a.settleConversationFailure(
				ctx, key, profile.ProfileID,
				errors.New("已有候选人档案的未读会话缺少可对账会话事实"),
			)
			if !handled {
				return conversationListPageContinue, fatalErr
			}
			continue
		}
		dirtyConversation := *dirty
		if err := a.setStage("readingThread"); err != nil {
			return conversationListPageContinue, err
		}
		projection, err := a.reconcileConversation(ctx, dirtyConversation)
		if len(projection.Messages) != 0 || len(projection.CardTransitions) != 0 {
			a.projection = append(a.projection, projection)
		}
		if err != nil {
			if a.isLocallyAttemptedUnreadError(err) {
				if auditErr := a.appendUnreadPatrolAudit(
					"status=attempted outcome=" + unreadLocalOutcome(err),
				); auditErr != nil {
					return conversationListPageContinue, auditErr
				}
				continue
			}
			handled, fatalErr := a.settleConversationFailure(ctx, key, profile.ProfileID, err)
			if !handled {
				return conversationListPageContinue, fatalErr
			}
			continue
		}
		a.manager.markListHintVerified(
			dirtyConversation.listHintKey,
			dirtyConversation.listHintFingerprint,
		)
		if a.classificationCorrected {
			a.unreadRetryDeferred = true
			return conversationListPageSwitchAll, nil
		}
		if err := a.prepareInboundConversationProfile(ctx, *profile); err != nil {
			handled, fatalErr := a.settleConversationFailure(ctx, key, profile.ProfileID, err)
			if !handled {
				return conversationListPageContinue, fatalErr
			}
			continue
		}
		if err := a.processCommunicationV4Profile(ctx, profile.ProfileID); err != nil {
			handled, fatalErr := a.settleConversationFailure(ctx, key, profile.ProfileID, err)
			if !handled {
				return conversationListPageContinue, fatalErr
			}
			continue
		}
		if a.classificationCorrected {
			a.unreadRetryDeferred = true
			return conversationListPageSwitchAll, nil
		}
	}
	if !page.complete {
		return conversationListPageContinue, nil
	}
	hand, err := a.refreshHandState(ctx)
	if err != nil {
		return conversationListPageContinue, err
	}
	if hand.UnreadTotal == nil {
		a.unreadRetryDeferred = true
		if err := a.appendUnreadPatrolAudit(
			"status=incomplete reason=unreadPassEndTotalUnknown",
		); err != nil {
			return conversationListPageContinue, err
		}
		return conversationListPageSwitchAll, nil
	}
	if !a.manager.recordUnreadPassEnd(a.account, hand.UnreadTotal) {
		a.unreadRetryDeferred = true
		if err := a.appendUnreadPatrolAudit(
			"status=incomplete reason=unreadPassEndTotalInvalid",
		); err != nil {
			return conversationListPageContinue, err
		}
		return conversationListPageSwitchAll, nil
	}
	if err := a.appendUnreadPatrolAudit(
		fmt.Sprintf("status=completed endUnreadTotal=%d", *hand.UnreadTotal),
	); err != nil {
		return conversationListPageContinue, err
	}
	return conversationListPageSwitchAll, nil
}

func (a *roundActor) openUnreadConversationWithoutAutomation(
	ctx context.Context,
	conversationRef string,
) error {
	if err := a.setStage("openingUnreadConversation"); err != nil {
		return err
	}
	// 打开会话是"切到下一个人"，与批采换人同档(4～8 秒)，比页面内的
	// 翻窗滚动慢一档。
	if err := a.waitSourcingDelay(ctx, a.manager.config.SourcingPaceWait); err != nil {
		return err
	}
	data, err := invokePrimitive[protocol.ChatOpenConversationData](
		ctx,
		a,
		protocol.PrimChatOpenConversation,
		protocol.ChatOpenConversationArgs{ConversationRef: conversationRef},
	)
	if err != nil {
		if a.isLocallyAttemptedUnreadError(err) {
			if auditErr := a.appendUnreadPatrolAudit(
				"status=attempted outcome=" + unreadLocalOutcome(err),
			); auditErr != nil {
				return auditErr
			}
			return nil
		}
		// 失败分流（含账号级信号的 handleCommandFailure）由调用方的
		// settleConversationFailure 统一执行。
		return err
	}
	if data.ConversationRef != conversationRef {
		return &RunError{
			Code:       protocol.ErrCodeInternalHand,
			Retryable:  protocol.RetryableManualOnly,
			SideEffect: protocol.SideEffectPossible,
			Cause:      errors.New("未读会话打开结果与目标会话不一致"),
		}
	}
	return nil
}

func (a *roundActor) isLocallyAttemptedUnreadError(err error) bool {
	typed := runError(err)
	if typed == nil {
		return false
	}
	return (typed.Code == protocol.ErrCodeTargetNotFound &&
		typed.Retryable == protocol.RetryableNo &&
		typed.SideEffect == protocol.SideEffectNone) ||
		(typed.Code == protocol.ErrCodePostconditionUnconfirmed &&
			typed.SideEffect == protocol.SideEffectPossible)
}

func unreadLocalOutcome(err error) string {
	typed := runError(err)
	if typed == nil {
		return "unknown"
	}
	switch typed.Code {
	case protocol.ErrCodeTargetNotFound:
		return "targetNotFound"
	case protocol.ErrCodePostconditionUnconfirmed:
		return "postconditionUnconfirmed"
	default:
		return "unknown"
	}
}

func (a *roundActor) appendUnreadPatrolAudit(detail string) error {
	return a.manager.store.AppendAudit(&store.AuditEntry{
		At: a.manager.now(), Category: unreadPatrolAuditCategory,
		Platform: a.account.Platform, AccountRef: a.account.AccountRef,
		RoundID: a.roundID, Detail: detail,
	})
}

// mayStartNextConversation calls the coarse product-workflow gate without
// holding the patrol actor lock. Product controls acquire their workflow lock
// before pausing the patrol actor, so entering the gate under Manager.mu would
// invert that order. After reacquiring Manager.mu, the same dispatch recheck
// used around network waits proves that the account, hand generation, daily
// window and sourcing context still belong to this round.
func (a *roundActor) mayStartNextConversation(
	ctx context.Context,
) (bool, error) {
	a.manager.gateMu.RLock()
	installed := a.manager.workflowConversationGate != nil
	a.manager.gateMu.RUnlock()
	if !installed {
		return true, nil
	}
	var (
		allowed bool
		gateErr error
	)
	func() {
		a.manager.mu.Unlock()
		defer a.manager.mu.Lock()
		allowed, gateErr = a.manager.mayStartNextConversation()
	}()
	if gateErr != nil {
		return false, gateErr
	}
	if err := a.ensureDispatchAllowed(ctx); err != nil {
		return false, err
	}
	return allowed, nil
}

const inboundProfileAdoptionAuditCategory = "inbound_profile_adoption"

func (a *roundActor) adoptInboundConversationProfiles(
	sessions []protocol.ConversationSummary,
) (map[string]struct{}, error) {
	adopted := make(map[string]struct{})
	for _, summary := range sessions {
		key := store.ConversationKey{
			Platform: a.account.Platform, AccountRef: a.account.AccountRef,
			ConversationRef: summary.ConversationRef,
		}
		existing, err := a.manager.store.CandidateProfileByConversation(key)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			continue
		}

		reason := ""
		switch {
		case inboundHandoverBlocked(
			summary.LastActivityTs,
			a.manager.config.InboundHandoverCutoff,
		):
			// 交接前存量优先于身份事实判定：旧会话即便同时缺 positionTitle，
			// "不属于本产品"也比"页面事实不全"更准确地解释为何没接管。
			reason = inboundHandoverSkipReason
		case strings.TrimSpace(summary.Peer.PlatformUserRef) == "":
			reason = "missingPlatformUserRef"
		case strings.TrimSpace(summary.Peer.DisplayName) == "":
			reason = "missingDisplayName"
		case summary.PositionTitle == nil || strings.TrimSpace(*summary.PositionTitle) == "":
			reason = "missingPositionTitle"
		}
		if reason != "" {
			if err := a.appendInboundProfileAdoptionAudit(
				summary.ConversationRef,
				"status=skipped reason="+reason,
			); err != nil {
				return nil, err
			}
			continue
		}

		result, err := a.manager.store.AdoptInboundConversationProfile(
			store.AdoptInboundConversationProfileRequest{
				Platform: a.account.Platform, AccountRef: a.account.AccountRef,
				ConversationRef: summary.ConversationRef,
				PlatformUserRef: summary.Peer.PlatformUserRef,
				DisplayName:     summary.Peer.DisplayName,
				PositionTitle:   *summary.PositionTitle,
				ObservedAt:      a.manager.now(),
			},
		)
		if err != nil {
			conflictReason := ""
			switch {
			case errors.Is(err, store.ErrInboundProfileAdoptionConflict):
				conflictReason = "identityFactConflict"
			case errors.Is(err, store.ErrCandidateAlreadyProfiled):
				conflictReason = "humanProfileConflict"
			case errors.Is(err, store.ErrInboundProfileAdoptionInvalid):
				conflictReason = "invalidPageIdentity"
			}
			if conflictReason == "" {
				return nil, err
			}
			if err := a.appendInboundProfileAdoptionAudit(
				summary.ConversationRef,
				"status=manualRequired reason="+conflictReason,
			); err != nil {
				return nil, err
			}
			continue
		}
		if result == nil {
			return nil, errors.New("主动来聊候选人收编返回空结果")
		}
		switch result.Outcome {
		case store.InboundProfileAdopted:
			if result.Profile == nil || strings.TrimSpace(result.Profile.ProfileID) == "" {
				return nil, errors.New("主动来聊候选人收编成功但缺少 profileId")
			}
			adopted[result.Profile.ProfileID] = struct{}{}
			if err := a.appendInboundProfileAdoptionAudit(
				summary.ConversationRef,
				"status=adopted",
			); err != nil {
				return nil, err
			}
		case store.InboundProfileAlreadyAdopted:
			// A concurrent/idempotent adoption is not newly owned by this
			// page pass. The ordinary profile path decides its next action.
		case store.InboundProfilePositionNoMatch:
			if err := a.appendInboundProfileAdoptionAudit(
				summary.ConversationRef,
				"status=skipped reason=positionNoMatch",
			); err != nil {
				return nil, err
			}
		case store.InboundProfilePositionAmbiguous:
			if err := a.appendInboundProfileAdoptionAudit(
				summary.ConversationRef,
				"status=skipped reason=positionAmbiguous",
			); err != nil {
				return nil, err
			}
		case store.InboundProfileNoEligibleJobs:
			if err := a.appendInboundProfileAdoptionAudit(
				summary.ConversationRef,
				"status=skipped reason=noEligibleJobs",
			); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("未知主动来聊候选人收编结果: %q", result.Outcome)
		}
	}
	return adopted, nil
}

func (a *roundActor) appendInboundProfileAdoptionAudit(
	conversationRef string,
	detail string,
) error {
	return a.manager.store.AppendAudit(&store.AuditEntry{
		At: a.manager.now(), Category: inboundProfileAdoptionAuditCategory,
		Platform: a.account.Platform, AccountRef: a.account.AccountRef,
		ConversationRef: conversationRef, RoundID: a.roundID,
		Detail: detail,
	})
}

func (a *roundActor) prepareInboundConversationProfile(
	ctx context.Context,
	profile store.CandidateProfile,
) error {
	if profile.MainStatus != store.CandidateProfileSelected ||
		profile.SuccessfulGreetingIntentID != nil ||
		profile.ConversationRef == nil {
		return nil
	}
	switch profile.ResumeCaptureState {
	case store.ResumeCaptureUnattempted, store.ResumeCaptureInFlight:
		target, err := a.manager.store.InboundResumeCaptureTarget(profile.ProfileID)
		if err != nil {
			if errors.Is(err, store.ErrResumeCaptureBinding) ||
				errors.Is(err, store.ErrResumeCaptureNotAllowed) ||
				errors.Is(err, store.ErrCandidateProfileState) {
				return a.appendInboundProfileAdoptionAudit(
					*profile.ConversationRef,
					"status=manualRequired reason=resumeTargetConflict",
				)
			}
			return err
		}
		if target == nil {
			return nil
		}
		if err := a.captureResumeForProfile(ctx, target.Profile); err != nil {
			if errors.Is(err, store.ErrResumeCaptureBinding) ||
				errors.Is(err, store.ErrResumeCaptureNotAllowed) ||
				errors.Is(err, store.ErrResumeCaptureConflict) ||
				errors.Is(err, store.ErrCandidateProfileState) {
				return a.appendInboundProfileAdoptionAudit(
					*profile.ConversationRef,
					"status=manualRequired reason=resumeCaptureConflict",
				)
			}
			return err
		}
		refreshed, err := a.manager.store.CandidateProfileByConversation(
			store.ConversationKey{
				Platform: profile.Platform, AccountRef: profile.AccountRef,
				ConversationRef: *profile.ConversationRef,
			},
		)
		if err != nil {
			return err
		}
		if refreshed == nil {
			return store.ErrCandidateProfileState
		}
		profile = *refreshed
	case store.ResumeCaptureCaptured:
		// Continue below.
	case store.ResumeCaptureManualRequired:
		return nil
	default:
		return store.ErrCandidateProfileState
	}
	if profile.ResumeCaptureState != store.ResumeCaptureCaptured {
		return nil
	}
	_, _, err := a.manager.store.EnsureInboundConversationV4Root(
		profile.ProfileID,
		a.manager.now(),
	)
	if err == nil {
		return a.appendInboundProfileAdoptionAudit(
			*profile.ConversationRef,
			"status=rooted",
		)
	}
	if errors.Is(err, store.ErrCommunicationV4Invalid) ||
		errors.Is(err, store.ErrCommunicationV4Conflict) ||
		errors.Is(err, store.ErrCommunicationV4Corrupt) ||
		errors.Is(err, store.ErrCommunicationV4Missing) {
		return a.appendInboundProfileAdoptionAudit(
			*profile.ConversationRef,
			"status=manualRequired reason=inboundRootConflict",
		)
	}
	return err
}

const communicationV4RootActivationAuditCategory = "communication_v4_root_activation"
const communicationV4ContextBindingAuditCategory = "communication_v4_context_binding"
const communicationV4ResumeReuseAuditCategory = "communication_v4_resume_reuse"

func (a *roundActor) ensureCommunicationV4Roots() error {
	profileIDs, err := a.manager.store.UnrootedGreetedProfileIDsForAccount(a.key())
	if err != nil {
		return err
	}
	for _, profileID := range profileIDs {
		if _, _, err := a.manager.store.EnsureCommunicationV4RootForGreetedProfile(
			profileID, a.manager.now(),
		); err != nil {
			if errors.Is(err, store.ErrCommunicationV4Conflict) ||
				errors.Is(err, store.ErrCommunicationV4Missing) {
				a.manager.store.Audit(
					communicationV4RootActivationAuditCategory, "", profileID,
					"status=manualRequired reason=legacyRootBindingConflict",
				)
				continue
			}
			return err
		}
	}
	return nil
}

func (a *roundActor) ensureSourcingCommunicationContexts() error {
	profileIDs, err := a.manager.store.SourcingProfileIDsNeedingAIContextForAccount(a.key())
	if err != nil {
		return err
	}
	for _, profileID := range profileIDs {
		if _, _, err := a.manager.store.BindSourcingProfileAIContext(
			profileID, a.manager.now(),
		); err != nil {
			if errors.Is(err, store.ErrProfileAIContextBindingInvalid) ||
				errors.Is(err, store.ErrProfileAIContextBindingConflict) ||
				errors.Is(err, store.ErrJobAIContextRevisionNotFound) ||
				errors.Is(err, store.ErrCommunicationV4Conflict) ||
				errors.Is(err, store.ErrCommunicationV4Corrupt) {
				a.manager.store.Audit(
					communicationV4ContextBindingAuditCategory, "", profileID,
					"status=manualRequired reason=sourcingContextBindingConflict",
				)
				continue
			}
			return err
		}
	}
	return nil
}

func (a *roundActor) ensureSourcingCommunicationResumes() error {
	profileIDs, err := a.manager.store.SourcingProfileIDsNeedingResumeForAccount(a.key())
	if err != nil {
		return err
	}
	for _, profileID := range profileIDs {
		if _, err := a.manager.store.ReuseSourcingResumeForCommunicationProfile(
			profileID, a.manager.now(),
		); err != nil {
			if errors.Is(err, store.ErrResumeCaptureBinding) ||
				errors.Is(err, store.ErrResumeCaptureNotAllowed) ||
				errors.Is(err, store.ErrCandidateProfileState) ||
				errors.Is(err, store.ErrCommunicationV4Conflict) ||
				errors.Is(err, store.ErrCommunicationV4Corrupt) {
				a.manager.store.Audit(
					communicationV4ResumeReuseAuditCategory, "", profileID,
					"status=manualRequired reason=sourcingResumeReuseConflict",
				)
				continue
			}
			return err
		}
	}
	return nil
}

func (a *roundActor) captureSourcingResume(ctx context.Context) (*store.SourcingCandidateRun, error) {
	if !a.account.SourcingEnabled {
		return nil, nil
	}
	revisionHash := a.account.SourcingContextRevisionHash
	if revisionHash == "" {
		return nil, store.ErrSourcingBinding
	}
	if err := a.setStage("readingSourcingResume"); err != nil {
		return nil, err
	}
	if err := a.ensureDispatchAllowed(ctx); err != nil {
		return nil, err
	}
	runner, ok := a.manager.runner.(SourcingResumeRunner)
	if !ok {
		return nil, errors.New("巡检 runner 未实现推荐页简历采集接缝")
	}
	excluded, err := a.manager.store.SourcingExcludedPlatformUserRefs(a.key(), revisionHash, 32)
	if err != nil {
		return nil, err
	}
	attemptedAt := a.manager.now()
	if err := a.manager.store.MarkSourcingAttempt(a.key(), revisionHash, attemptedAt, ""); err != nil {
		return nil, err
	}
	expected := ""
	if a.account.PrincipalFingerprint != nil {
		expected = *a.account.PrincipalFingerprint
	}
	handle, err := runner.StartSourcingResume(ctx, SourcingResumeRequest{
		HandID: a.account.BoundHandID, ExpectedSession: a.hand.Session, ExpectedBootID: a.hand.BootID,
		Platform: a.account.Platform, AccountRef: a.account.AccountRef,
		ExpectedPrincipalFingerprint: expected, ExcludePlatformUserRefs: excluded,
	})
	if err != nil {
		return nil, err
	}
	logicalID := handle.LogicalDispatchID()
	var raw json.RawMessage
	func() {
		a.manager.mu.Unlock()
		defer a.manager.mu.Lock()
		raw, err = handle.Wait(ctx)
	}()
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		var runErr *RunError
		if !errors.As(err, &runErr) {
			return nil, err
		}
		if markErr := a.manager.store.MarkSourcingAttempt(a.key(), revisionHash, attemptedAt, sourcingFailureCode(runErr)); markErr != nil {
			return nil, markErr
		}
		if runErr.Code == protocol.ErrCodeAccountMismatch ||
			(runErr.Code == protocol.ErrCodeCtxNotReady && runErr.Reason == protocol.NotReadyReasonLoginRequired) {
			return nil, err
		}
		// 推荐页无候选人、页面局部变化等只结束本次采集，不阻断同轮 IM 对账。
		return nil, nil
	}
	if err := a.ensureDispatchAllowed(ctx); err != nil {
		return nil, err
	}
	meta := protocol.Primitives[protocol.PrimCandidateReadSourcingResume]
	if err := protocol.ValidatePrimitiveData(protocol.PrimCandidateReadSourcingResume, meta.Ver, raw); err != nil {
		return nil, err
	}
	var data protocol.CandidateReadSourcingResumeData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	run, err := a.manager.store.CompleteSourcingCandidateRun(store.CompleteSourcingCandidateRunRequest{
		RunID: ids.NewSourcingRunID(), Platform: a.account.Platform, AccountRef: a.account.AccountRef,
		ContextRevisionHash: revisionHash, LogicalDispatchID: logicalID, Data: data,
	})
	if errors.Is(err, store.ErrSourcingBinding) || errors.Is(err, store.ErrSourcingNotEnabled) {
		return nil, nil
	}
	return run, err
}

func sourcingFailureCode(err *RunError) string {
	if err == nil || err.Code == "" {
		return "commandFailed"
	}
	if err.Code == protocol.ErrCodeCtxNotReady && err.Reason != "" {
		return string(err.Code) + "/" + string(err.Reason)
	}
	return string(err.Code)
}

func (a *roundActor) captureResumeForProfile(
	ctx context.Context,
	profile store.CandidateProfile,
) error {
	switch profile.ResumeCaptureState {
	case store.ResumeCaptureCaptured:
		return nil
	case store.ResumeCaptureUnattempted, store.ResumeCaptureInFlight:
		// 继续。
	case store.ResumeCaptureManualRequired:
		return nil
	default:
		return store.ErrCandidateProfileState
	}
	if err := a.setStage("readingResume"); err != nil {
		return err
	}
	if err := a.ensureDispatchAllowed(ctx); err != nil {
		return err
	}
	runner, ok := a.manager.runner.(ResumeCaptureRunner)
	if !ok {
		return errors.New("巡检 runner 未实现简历补采接缝")
	}
	expected := ""
	if a.account.PrincipalFingerprint != nil {
		expected = *a.account.PrincipalFingerprint
	}
	handle, err := runner.StartResumeCapture(ctx, ResumeCaptureRequest{
		ProfileID: profile.ProfileID,
		HandID:    a.account.BoundHandID, ExpectedSession: a.hand.Session, ExpectedBootID: a.hand.BootID,
		Platform: a.account.Platform, AccountRef: a.account.AccountRef,
		ExpectedPrincipalFingerprint: expected,
	})
	if err != nil {
		return err
	}
	logicalID := handle.LogicalDispatchID()
	var raw json.RawMessage
	func() {
		a.manager.mu.Unlock()
		defer a.manager.mu.Lock()
		raw, err = handle.Wait(ctx)
	}()
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		var runErr *RunError
		if !errors.As(err, &runErr) {
			return err
		}
		return a.finishResumeCaptureFailure(profile.ProfileID, logicalID, resumeFailureReason(runErr))
	}
	meta := protocol.Primitives[protocol.PrimCandidateReadResume]
	if err := protocol.ValidatePrimitiveData(protocol.PrimCandidateReadResume, meta.Ver, raw); err != nil {
		return a.finishResumeCaptureFailure(profile.ProfileID, logicalID, "invalidResult")
	}
	var data protocol.CandidateReadResumeData
	if err := json.Unmarshal(raw, &data); err != nil {
		return a.finishResumeCaptureFailure(profile.ProfileID, logicalID, "invalidResult")
	}
	_, err = a.manager.store.CompleteResumeCapture(store.CompleteResumeCaptureRequest{
		ProfileID: profile.ProfileID, LogicalDispatchID: logicalID,
		SnapshotID: ids.NewResumeSnapshotID(), Data: data,
	})
	if errors.Is(err, store.ErrResumeCaptureBinding) || errors.Is(err, store.ErrResumeCaptureConflict) {
		return nil
	}
	return err
}

func (a *roundActor) finishResumeCaptureFailure(profileID, logicalID, reason string) error {
	if err := a.manager.store.FailResumeCapture(store.FailResumeCaptureRequest{
		ProfileID: profileID, LogicalDispatchID: logicalID, Reason: reason, At: a.manager.now(),
	}); err != nil {
		return err
	}
	return nil
}

func resumeFailureReason(err *RunError) string {
	if err == nil {
		return "commandFailed"
	}
	switch err.Code {
	case protocol.ErrCodePayloadLimit:
		return "payloadLimit"
	case protocol.ErrCodeAccountMismatch:
		return "accountMismatch"
	case protocol.ErrCodeTargetNotFound, protocol.ErrCodeConversationNotFound:
		return "targetMissing"
	case protocol.ErrCodeElementUnresolved:
		return "pageStructure"
	case protocol.ErrCodeInternalHand:
		return "invalidResult"
	case protocol.ErrCodeCtxLostDuringExec:
		return "bindingLost"
	default:
		return "commandFailed"
	}
}

func (a *roundActor) needsProbe() bool {
	if a.account.IdentityState != store.IdentityVerified || a.account.IdentityVerifiedAt == nil {
		return true
	}
	if a.account.IdentitySession != a.hand.Session || a.account.IdentityBootID != a.hand.BootID {
		return true
	}
	return !a.now.Before(a.account.IdentityVerifiedAt.Add(a.manager.config.IdentityFreshFor))
}

func (a *roundActor) probeAndVerify(ctx context.Context) error {
	var (
		data protocol.ProbePlatformData
		err  error
	)
	if a.trigger == TriggerCurrentConversation {
		data, err = invokePrimitiveDirect[protocol.ProbePlatformData](
			ctx,
			a,
			protocol.PrimProbePlatform,
			protocol.ProbePlatformArgs{},
		)
	} else {
		data, err = invokePrimitive[protocol.ProbePlatformData](
			ctx,
			a,
			protocol.PrimProbePlatform,
			protocol.ProbePlatformArgs{},
		)
	}
	if err != nil {
		return err
	}
	// Successful probe data can still say the page/content script is absent.
	// That maps to the same one-shot recovery budget as CTX_NOT_READY.
	if (!data.ContentScriptOk || data.PageKind == protocol.PageKindNone) && data.PrincipalFingerprint == nil {
		reason := protocol.NotReadyReasonContentScriptDead
		if data.PageKind == protocol.PageKindNone {
			reason = protocol.NotReadyReasonPageAbsent
		}
		if err := a.markIdentityUnobservable(reason); err != nil {
			return err
		}
		if a.trigger == TriggerCurrentConversation {
			return wrapRunError(protocol.ErrCodeCtxNotReady, reason, ErrEnsureNotReady)
		}
		if err := a.ensureSurface(ctx, reason); err != nil {
			return err
		}
		data, err = invokePrimitiveDirect[protocol.ProbePlatformData](ctx, a, protocol.PrimProbePlatform, protocol.ProbePlatformArgs{})
		if err != nil {
			return err
		}
	}
	return a.verifyProbeData(data)
}

func (a *roundActor) verifyProbeData(data protocol.ProbePlatformData) error {
	if data.LoginState == protocol.LoginStateOut {
		return wrapRunError(protocol.ErrCodeCtxNotReady, protocol.NotReadyReasonLoginRequired, ErrLoginRequired)
	}
	if data.LoginState != protocol.LoginStateIn {
		if err := a.markIdentityUnobservable(protocol.NotReadyReasonUnknown); err != nil {
			return err
		}
		return ErrIdentityUnobservable
	}
	if data.PrincipalFingerprint == nil || *data.PrincipalFingerprint == "" {
		if err := a.markIdentityUnobservable(protocol.NotReadyReasonIdentityUnverified); err != nil {
			return err
		}
		return ErrIdentityUnobservable
	}
	if *data.PrincipalFingerprint != *a.account.PrincipalFingerprint {
		return wrapRunError(protocol.ErrCodeAccountMismatch, "", errors.New("probe principal fingerprint mismatch"))
	}
	verifiedAt := a.manager.now()
	if err := a.manager.store.MutateAccount(a.key(), func(account *store.Account) error {
		account.IdentityState = store.IdentityVerified
		account.IdentityVerifiedAt = timePointer(verifiedAt)
		account.IdentitySession = a.hand.Session
		account.IdentityBootID = a.hand.BootID
		account.IdentityReason = ""
		return nil
	}); err != nil {
		return err
	}
	a.account.IdentityState = store.IdentityVerified
	a.account.IdentityVerifiedAt = timePointer(verifiedAt)
	a.account.IdentitySession = a.hand.Session
	a.account.IdentityBootID = a.hand.BootID
	a.account.IdentityReason = ""
	return nil
}

func (a *roundActor) markIdentityUnobservable(reason protocol.NotReadyReason) error {
	if err := a.manager.store.SetAccountIdentityState(a.key(), store.IdentityUnobservable, string(reason)); err != nil {
		return err
	}
	a.account.IdentityState = store.IdentityUnobservable
	a.account.IdentitySession = ""
	a.account.IdentityBootID = ""
	a.account.IdentityReason = string(reason)
	return nil
}

func (a *roundActor) readListPage(
	ctx context.Context,
	filter protocol.ListFilter,
	move protocol.ListWindowMove,
) (conversationListPage, error) {
	args := protocol.ChatReadListArgs{
		Filter: filter,
		Move:   move,
	}
	if filter == protocol.ListFilterAll {
		args.StopOlderThanDays = listStopOlderThanDays(
			a.manager.now(),
			a.manager.config.InboundHandoverCutoff,
			a.manager.config.Location,
		)
	}
	// 列表翻窗（滚动/切筛选）同为可见交互，套用统一节奏。
	if err := a.waitSourcingDelay(ctx, a.manager.config.InteractionPaceWait); err != nil {
		return conversationListPage{}, err
	}
	data, err := invokePrimitive[protocol.ChatReadListData](
		ctx,
		a,
		protocol.PrimChatReadList,
		args,
	)
	if err != nil {
		return conversationListPage{}, err
	}
	return conversationListPage{
		sessions: data.Sessions,
		complete: data.Complete,
	}, nil
}

func (a *roundActor) listFingerprint(
	summary protocol.ConversationSummary,
) string {
	principalFingerprint := ""
	if a.account.PrincipalFingerprint != nil {
		principalFingerprint = *a.account.PrincipalFingerprint
	}
	return listHintFingerprint(
		makeListHintVerificationKey(
			a.account.Platform,
			a.account.AccountRef,
			principalFingerprint,
			summary.ConversationRef,
		),
		summary,
	)
}

func listEntries(sessions []protocol.ConversationSummary) ([]store.ListIndexEntry, error) {
	entries := make([]store.ListIndexEntry, 0, len(sessions))
	seen := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		if _, exists := seen[session.ConversationRef]; exists {
			return nil, fmt.Errorf("%w: %s", store.ErrDuplicateConversationEntry, session.ConversationRef)
		}
		seen[session.ConversationRef] = struct{}{}
		entries = append(entries, store.ListIndexEntry{
			ConversationRef: session.ConversationRef, PlatformUserRef: session.Peer.PlatformUserRef,
			PeerDisplayName: session.Peer.DisplayName, UnreadCount: session.UnreadCount,
			LastMessageDirection: string(session.LastMessage.Direction), LastMessageKind: string(session.LastMessage.Kind),
			LastMessagePreview: session.LastMessage.TextPreview, LastActivityMs: session.LastActivityTs,
		})
	}
	return entries, nil
}

func (a *roundActor) trackedConversationsByRef() (
	map[string]store.Conversation,
	error,
) {
	tracked, err := a.manager.store.TrackedConversations(a.key())
	if err != nil {
		return nil, err
	}
	trackedByRef := make(map[string]store.Conversation, len(tracked))
	for _, conversation := range tracked {
		trackedByRef[conversation.ConversationRef] = conversation
	}
	return trackedByRef, nil
}

func (a *roundActor) detectDirtySummary(
	summary protocol.ConversationSummary,
	forceUnread bool,
	trackedByRef map[string]store.Conversation,
) (*dirtyConversation, error) {
	conversation, listed := trackedByRef[summary.ConversationRef]
	if !listed {
		return nil, nil
	}
	key := store.ConversationKey{
		Platform: conversation.Platform, AccountRef: conversation.AccountRef,
		ConversationRef: conversation.ConversationRef,
	}
	ledger, err := a.manager.store.MessagesForConversation(key)
	if err != nil {
		return nil, err
	}
	principalFingerprint := ""
	if a.account.PrincipalFingerprint != nil {
		principalFingerprint = *a.account.PrincipalFingerprint
	}
	hintKey := makeListHintVerificationKey(
		conversation.Platform,
		conversation.AccountRef,
		principalFingerprint,
		conversation.ConversationRef,
	)
	hintFingerprint := listHintFingerprint(hintKey, summary)
	hintAlreadyVerified, hintChangedFromVerified :=
		a.manager.observeListHintFingerprint(hintKey, hintFingerprint)

	forceReconcile := conversation.TrackingState == store.TrackingPending
	observedAt := a.manager.now()
	if conversation.LastSyncedAt == nil ||
		!observedAt.Before(conversation.LastSyncedAt.Add(a.manager.config.TrackedReconcileInterval)) {
		// 事件、未读与列表摘要都是提示；低频到期对账兜住提示整体失真的场景。
		// 同文连续消息与卡片静默变化都会改写指纹的 lastActivityMs，由下面的
		// hintDirty 负责，不依赖本闸；真正只能靠它捞回的是平台不返回任何时间
		// 戳的会话。间隔取值理由见 Config.withDefaults。
		forceReconcile = true
	}
	if len(ledger) == 0 {
		forceReconcile = true
	}
	hintDirty := summary.UnreadCount > 0 || hintChangedFromVerified
	if len(ledger) > 0 && !syncledger.ListPreviewMatches(syncledger.ListPreview{
		Direction: string(summary.LastMessage.Direction), Kind: string(summary.LastMessage.Kind),
		Text: summary.LastMessage.TextPreview,
	}, ledger[len(ledger)-1]) {
		hintDirty = true
	}
	if !forceUnread && !forceReconcile && (!hintDirty || hintAlreadyVerified) {
		// 上面四条都不成立时的最后一条读取理由(2026-08-04 真机立案,推导与
		// 收窄见 store/pending_visible_dispatch.go):发送系原语只认已经打开
		// 的会话页,而页面导航只发生在判脏之后的 reconcileConversation 里。
		// 候选人安静下来之后才要发的回复因此必然撞 pageAbsent,真机两例各自
		// 连续 20 代重试全败、回复迟了 3~4 小时。
		//
		// 注意本判据必须落在 forceReconcile 一侧,不能并入 hintDirty:这类会话
		// 的列表指纹恰恰是"已验证且没变过",hintAlreadyVerified 会把它重新
		// 挡回 return nil。
		pendingDispatch, pendingErr := a.manager.store.ConversationHasPlannedVisibleDispatch(key)
		if pendingErr != nil {
			return nil, pendingErr
		}
		if !pendingDispatch {
			return nil, nil
		}
	}
	return &dirtyConversation{
		conversation:        conversation,
		ledger:              ledger,
		listHintKey:         hintKey,
		listHintFingerprint: hintFingerprint,
	}, nil
}

func (a *roundActor) reconcileConversation(ctx context.Context, dirty dirtyConversation) (ConversationProjection, error) {
	key := store.ConversationKey{
		Platform: dirty.conversation.Platform, AccountRef: dirty.conversation.AccountRef,
		ConversationRef: dirty.conversation.ConversationRef,
	}
	anchors := syncledger.AnchorTail(dirty.ledger)
	protocolAnchors := make([]protocol.MessageAnchor, len(anchors))
	for i := range anchors {
		protocolAnchors[i] = protocol.MessageAnchor{
			Direction: protocol.MessageDirection(anchors[i].Direction), ContentHash: anchors[i].ContentHash,
		}
	}

	snapshot, err := a.readThread(ctx, key.ConversationRef, protocolAnchors, false)
	if err != nil {
		return ConversationProjection{Key: key}, err
	}
	peerRef := dirty.conversation.PlatformUserRef
	if snapshot.peer != nil && snapshot.peer.PlatformUserRef != "" {
		peerRef = snapshot.peer.PlatformUserRef
	}
	input := syncledger.ReconcileInput{
		Key: key, RoundID: a.roundID, PlatformUserRef: peerRef, Ledger: dirty.ledger,
		Snapshot: snapshot.messages, Adopt: dirty.conversation.TrackingState == store.TrackingPending,
		Deep: false, ReachedTop: snapshot.reachedTop, AnchorMatched: snapshot.anchorMatched, SyncedAt: a.manager.now(),
	}
	plan, err := syncledger.Reconcile(input)
	if err != nil {
		return ConversationProjection{Key: key}, err
	}
	if plan.NeedsDeep() {
		snapshot, err = a.readThread(ctx, key.ConversationRef, protocolAnchors, true)
		if err != nil {
			return ConversationProjection{Key: key}, err
		}
		if snapshot.peer != nil && snapshot.peer.PlatformUserRef != "" {
			peerRef = snapshot.peer.PlatformUserRef
		}
		input.PlatformUserRef = peerRef
		input.Snapshot = snapshot.messages
		input.Deep = true
		input.ReachedTop = snapshot.reachedTop
		input.AnchorMatched = snapshot.anchorMatched
		plan, err = syncledger.Reconcile(input)
		if err != nil {
			return ConversationProjection{Key: key}, err
		}
	}

	projection := ConversationProjection{
		Key: key, Messages: append([]store.MessageDraft(nil), plan.EventProjection...),
		CardTransitions: append([]syncledger.CardTransition(nil), plan.CardTransitions...),
	}
	if plan.Decision == syncledger.DecisionClassificationCorrection && plan.Correction != nil {
		plan.Correction.PauseReason = PauseUserRequested
	}
	if _, err := syncledger.ApplyPlan(a.manager.store, plan); err != nil {
		return ConversationProjection{Key: key}, err
	}
	if plan.Decision != syncledger.DecisionAuditedRebaseline {
		for i := range plan.Audits {
			audit := plan.Audits[i]
			if err := a.manager.store.AppendAudit(&audit); err != nil {
				// ApplyPlan has already committed. Preserve its projection in the
				// Tick result even when a diagnostic audit write fails, otherwise a
				// later replay would correctly produce no event and silently lose it.
				return projection, err
			}
		}
	}
	if plan.Decision == syncledger.DecisionClassificationCorrection {
		a.classificationCorrected = true
	}
	return projection, nil
}

func (a *roundActor) readThread(ctx context.Context, conversationRef string, anchors []protocol.MessageAnchor, deep bool) (threadSnapshot, error) {
	aggregate := threadSnapshot{}
	cursor := ""
	restarts := 0
	seen := map[string]struct{}{}
	for page := 0; page < a.manager.config.MaxPages; page++ {
		args := protocol.ChatReadThreadArgs{
			ConversationRef: conversationRef, Cursor: cursor,
			RequireCurrent: a.requireCurrentThread,
			Window: protocol.ThreadWindow{
				AnchorTail: anchors, Deep: deep, MaxMessages: protocol.DefaultPaginationReadThreadMaxItems,
			},
		}
		var (
			data protocol.ChatReadThreadData
			err  error
		)
		// 深读首页会把页面切到目标会话，按候选人切换节奏(4～8 秒)停顿；
		// 同会话内的分页滚动不再叠加大停顿。
		if cursor == "" {
			if paceErr := a.waitSourcingDelay(ctx, a.manager.config.SourcingPaceWait); paceErr != nil {
				return threadSnapshot{}, paceErr
			}
		}
		if a.requireCurrentThread {
			data, err = invokePrimitiveDirect[protocol.ChatReadThreadData](
				ctx,
				a,
				protocol.PrimChatReadThread,
				args,
			)
		} else {
			data, err = invokePrimitive[protocol.ChatReadThreadData](
				ctx,
				a,
				protocol.PrimChatReadThread,
				args,
			)
		}
		if err != nil {
			if isRunError(err, protocol.ErrCodeCursorInvalid) && cursor != "" && restarts == 0 {
				restarts++
				aggregate = threadSnapshot{}
				cursor = ""
				seen = map[string]struct{}{}
				page = -1
				continue
			}
			return threadSnapshot{}, err
		}
		if data.Peer != nil {
			if aggregate.peer != nil && aggregate.peer.PlatformUserRef != "" && data.Peer.PlatformUserRef != "" &&
				aggregate.peer.PlatformUserRef != data.Peer.PlatformUserRef {
				return threadSnapshot{}, ErrPeerChangedInPages
			}
			peerCopy := *data.Peer
			aggregate.peer = &peerCopy
		}
		pageMessages := snapshotMessages(data.Messages)
		if cursor == "" {
			aggregate.messages = pageMessages
		} else {
			// The first page is newest; each cursor page is older and therefore
			// must be prepended before reconciliation.
			aggregate.messages = append(pageMessages, aggregate.messages...)
		}
		// anchor 可能恰跨两个协议结果页：任何单页都无法把 AnchorMatched
		// 置真，但脑已持有完整聚合，可以自行证明停止条件。anchorTail 本来
		// 就由脑产生，不能把跨页正确性押在手的单次调用记忆上。
		derivedAnchorMatch := snapshotContainsAnchor(aggregate.messages, anchors)
		if data.Complete || derivedAnchorMatch {
			aggregate.reachedTop = data.ReachedTop
			aggregate.anchorMatched = data.AnchorMatched || derivedAnchorMatch
			return aggregate, nil
		}
		next := *data.NextCursor
		if _, duplicate := seen[next]; duplicate || next == cursor {
			return threadSnapshot{}, ErrPaginationLoop
		}
		seen[next] = struct{}{}
		cursor = next
	}
	return threadSnapshot{}, ErrPaginationLimit
}

func snapshotContainsAnchor(messages []syncledger.SnapshotMessage, anchors []protocol.MessageAnchor) bool {
	if len(anchors) == 0 || len(messages) < len(anchors) {
		return false
	}
	for start := 0; start+len(anchors) <= len(messages); start++ {
		matched := true
		for offset := range anchors {
			message := messages[start+offset]
			anchor := anchors[offset]
			if message.Direction != string(anchor.Direction) || message.ContentHash != anchor.ContentHash {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func snapshotMessages(messages []protocol.ThreadMessage) []syncledger.SnapshotMessage {
	out := make([]syncledger.SnapshotMessage, len(messages))
	for i := range messages {
		message := messages[i]
		blobRef := ""
		if message.BlobRef != nil {
			blobRef = *message.BlobRef
		}
		cardType := ""
		if message.CardType != nil {
			cardType = string(*message.CardType)
		}
		cardState := ""
		if message.CardState != nil {
			cardState = string(*message.CardState)
		}
		var interviewStartsAtMs, interviewEndsAtMs *int64
		var interviewMethod *string
		if message.Interview != nil {
			startsAt := message.Interview.StartsAt
			method := string(message.Interview.Method)
			interviewStartsAtMs = &startsAt
			// 现场面试没有结束时间：契约里 endsAt 是 omitempty，手侧对 onsite
			// 显式省略，解出来就是 0。直接取地址会变成"有 endsAt 且等于 0"，
			// 归一化随即判 endsAt<=startsAt 非法 → 会话被隔离、档案被冻结。
			// 这条不依赖我方发不发线下卡：招聘方自己在平台手发一张就会踩上。
			interviewEndsAtMs = syncledger.OptionalEndsAt(message.Interview.EndsAt)
			interviewMethod = &method
		}
		out[i] = syncledger.SnapshotMessage{
			Direction: string(message.Direction), Kind: string(message.Kind), Text: message.Text,
			BlobRef: blobRef, ContentHash: message.ContentHash, CardType: cardType,
			CardState: cardState, InterviewStartsAtMs: interviewStartsAtMs,
			InterviewEndsAtMs: interviewEndsAtMs, InterviewMethod: interviewMethod,
			TsApproxMs: message.TsApprox, Origin: "external",
			SourceKey: message.SourceKey,
		}
	}
	return out
}

func invokePrimitive[T any](ctx context.Context, actor *roundActor, name string, args any) (T, error) {
	result, err := invokePrimitiveDirect[T](ctx, actor, name, args)
	if err == nil {
		return result, nil
	}
	typed := runError(err)
	if typed == nil || typed.Code != protocol.ErrCodeCtxNotReady ||
		(typed.Reason != protocol.NotReadyReasonPageAbsent && typed.Reason != protocol.NotReadyReasonContentScriptDead) {
		return result, err
	}
	if stateErr := actor.markIdentityUnobservable(typed.Reason); stateErr != nil {
		return result, stateErr
	}
	if ensureErr := actor.ensureSurface(ctx, typed.Reason); ensureErr != nil {
		return result, ensureErr
	}
	if name != protocol.PrimProbePlatform {
		probe, probeErr := invokePrimitiveDirect[protocol.ProbePlatformData](ctx, actor, protocol.PrimProbePlatform, protocol.ProbePlatformArgs{})
		if probeErr != nil {
			return result, probeErr
		}
		if probeErr := actor.verifyProbeData(probe); probeErr != nil {
			return result, probeErr
		}
	}
	return invokePrimitiveDirect[T](ctx, actor, name, args)
}

func invokePrimitiveDirect[T any](ctx context.Context, actor *roundActor, name string, args any) (T, error) {
	result, _, err := invokePrimitiveDirectWithLogicalID[T](ctx, actor, name, args)
	return result, err
}

// invokePrimitiveDirectWithLogicalID 与普通 actor 原语走字面同一条 generic
// Runner/Dispatcher 路径，只额外把持久逻辑派发引用交给需要重验命令谱系的
// 领域事务。它不创建、恢复或缓存任何 in-flight 状态。
func invokePrimitiveDirectWithLogicalID[T any](
	ctx context.Context,
	actor *roundActor,
	name string,
	args any,
) (T, string, error) {
	var zero T
	if err := actor.ensureDispatchAllowed(ctx); err != nil {
		return zero, "", err
	}
	meta, ok := protocol.Primitives[name]
	if !ok || meta.Ver == 0 {
		return zero, "", fmt.Errorf("未激活原语 %q", name)
	}
	rawArgs, err := protocol.Encode(args)
	if err != nil {
		return zero, "", err
	}
	if err := protocol.ValidatePrimitiveArgs(name, meta.Ver, rawArgs); err != nil {
		return zero, "", err
	}
	expected := ""
	if actor.account.PrincipalFingerprint != nil {
		expected = *actor.account.PrincipalFingerprint
	}
	handle, err := actor.manager.runner.Start(ctx, RunRequest{
		HandID: actor.account.BoundHandID, ExpectedSession: actor.hand.Session, ExpectedBootID: actor.hand.BootID,
		Platform: actor.account.Platform, AccountRef: actor.account.AccountRef,
		ExpectedPrincipalFingerprint: expected, Name: name, Version: meta.Ver, Args: rawArgs,
	})
	if err != nil {
		return zero, "", err
	}
	if handle == nil || handle.LogicalDispatchID() == "" {
		return zero, "", errors.New("原语未返回持久逻辑派发引用")
	}
	logicalID := handle.LogicalDispatchID()
	var rawData json.RawMessage
	// 调度 Start 已在 actor 锁内完成；长时间只等持久逻辑命令，不阻塞
	// QoS0 事件、用户暂停或账号绑定。无论正常返回还是测试 Goexit，defer
	// 都会先恢复锁，保证调用栈继续处于同一锁不变量。
	func() {
		actor.manager.mu.Unlock()
		defer actor.manager.mu.Lock()
		rawData, err = handle.Wait(ctx)
	}()
	// 命令在途期间可能发生人工暂停、切号/改绑或 hand session/boot 更替。
	// 返回数据入账前复用同一门禁；任何代际变化都丢弃本轮观察，留给下一轮
	// fresh probe，不能把旧手/旧主体的数据写进当前账号根。
	if waitErr := actor.resolvePostWait(ctx, err); waitErr != nil {
		return zero, logicalID, waitErr
	}
	if err := protocol.ValidatePrimitiveData(name, meta.Ver, rawData); err != nil {
		return zero, logicalID, err
	}
	if err := json.Unmarshal(rawData, &zero); err != nil {
		return zero, logicalID, err
	}
	return zero, logicalID, nil
}

func (a *roundActor) ensureSurface(ctx context.Context, reason protocol.NotReadyReason) error {
	if reason != protocol.NotReadyReasonPageAbsent && reason != protocol.NotReadyReasonContentScriptDead {
		return wrapRunError(protocol.ErrCodeCtxNotReady, reason, ErrEnsureNotReady)
	}
	if a.ensureUsed {
		return wrapRunError(protocol.ErrCodeCtxNotReady, reason, ErrEnsureNotReady)
	}
	a.ensureUsed = true
	if err := a.setStage("ensuringSurface"); err != nil {
		return err
	}
	data, err := invokePrimitiveDirect[protocol.NavEnsureSurfaceData](ctx, a, protocol.PrimNavEnsureSurface,
		protocol.NavEnsureSurfaceArgs{Surface: protocol.SurfaceNameIm})
	if err != nil {
		return err
	}
	if data.LoginState == protocol.LoginStateOut {
		return wrapRunError(protocol.ErrCodeCtxNotReady, protocol.NotReadyReasonLoginRequired, ErrLoginRequired)
	}
	if !data.Ready {
		return wrapRunError(protocol.ErrCodeCtxNotReady, protocol.NotReadyReasonPageBroken, ErrEnsureNotReady)
	}
	if data.LoginState != protocol.LoginStateIn {
		return wrapRunError(protocol.ErrCodeCtxNotReady, protocol.NotReadyReasonUnknown, ErrEnsureNotReady)
	}
	return nil
}

func (a *roundActor) handleCommandFailure(err error) {
	typed := runError(err)
	switch {
	case typed != nil && typed.Code == protocol.ErrCodeAccountMismatch:
		_ = a.manager.store.SetAccountIdentityState(a.key(), store.IdentityInvalid, string(typed.Code))
		_ = a.manager.pauseAccount(a.key(), PauseAccountMismatch, a.now)
	case typed != nil && typed.Code == protocol.ErrCodeCtxNotReady && typed.Reason == protocol.NotReadyReasonLoginRequired:
		_ = a.manager.store.SetAccountIdentityState(a.key(), store.IdentityInvalid, string(typed.Reason))
		_ = a.manager.pauseAccount(a.key(), PauseLoginRequired, a.now)
	case typed != nil && typed.Code == protocol.ErrCodeUserActive:
		// 2026-07-27 甲方裁决废除静默窗：真人活动只催下一轮巡检，
		// 让位本轮自动重试，不冻结账号。
		_ = a.manager.store.MutateAccount(a.key(), func(account *store.Account) error {
			account.DirtyHint = true
			return nil
		})
	case typed != nil && typed.Code == protocol.ErrCodeCtxNotReady && typed.Reason == protocol.NotReadyReasonIdentityUnverified:
		_ = a.manager.store.SetAccountIdentityState(a.key(), store.IdentityUnobservable, string(typed.Reason))
	case typed != nil && typed.Retryable == protocol.RetryableManualOnly:
		// manualOnly 是手对脑侧通用重试矩阵的保守收窄，不是“本物理命令
		// 不重派、下一巡检再偷偷生产一条”的同义词。停止整个账号 actor，
		// 由真人重新开启后才允许下一次正常对账，避免 INTERNAL_HAND 等实现
		// 异常在每个 dirty/周期轮次中无界重放页面驱动。
		// 2026-07-27 甲方裁决后本分支只覆盖账号/页面级命令（probe、readList
		// 等）：单个候选人处理中的 manualOnly 由 settleConversationFailure
		// 隔离该会话，不再进入这里暂停整个账号。
		_ = a.manager.pauseAccount(a.key(), PauseHandManualReview, a.manager.now())
	}
}

func (a *roundActor) finish(runErr error) error {
	a.appendTransientSkipSummary()
	finishedAt := a.manager.now()
	status := "ok"
	stage := "finished"
	if runErr != nil {
		status = "failed"
		stage = "failed"
	} else if a.listTraversalIncomplete {
		// 页面窗口遍历被用户活动或高位预算截断。已完成的会话事实照常
		// 保留；下一轮从 reset 重新建立页面现场。
		stage = "listWindowPending"
	}
	if a.superseded {
		status = "failed"
		stage = "superseded"
	}
	trigger := a.trigger
	if a.ensureUsed {
		trigger += surfaceRecoverySuffix
	}
	if err := a.manager.store.MutatePatrolRound(a.account.Platform, a.account.AccountRef, a.roundID, func(round *store.PatrolRound) error {
		round.Status = status
		round.Stage = stage
		round.Trigger = trigger
		round.ErrorCode = errorCode(runErr)
		round.FinishedAt = timePointer(finishedAt)
		return nil
	}); err != nil {
		return err
	}
	// StartSourcing 已在与本函数相同的 Manager.mu 下建立新批次并写好
	// Enabled/Dirty/Next。换代后的旧轮只终局化自己的 PatrolRound，不能再
	// 用旧 LastPatrolAt、NextPatrolAt 或 surfaceRecovery 计数覆盖新任务。
	if a.superseded {
		return nil
	}
	if err := a.manager.store.MutateAccount(a.key(), func(account *store.Account) error {
		account.LastPatrolAt = timePointer(finishedAt)
		regularNext := finishedAt.Add(a.manager.config.PatrolInterval)
		if a.listTraversalIncomplete {
			account.DirtyHint = true
			windowNext := finishedAt.Add(a.manager.config.MinimumRoundGap)
			if account.NextPatrolAt == nil || account.NextPatrolAt.After(windowNext) {
				account.NextPatrolAt = timePointer(windowNext)
			}
			return nil
		}
		if runErr != nil {
			account.DirtyHint = true
		}
		if !account.DirtyHint {
			account.NextPatrolAt = timePointer(regularNext)
		} else if account.NextPatrolAt == nil || account.NextPatrolAt.After(regularNext) {
			// 保留轮次中事件已经拉前的更早时刻；失败但无事件时至少按
			// 正常周期重试，不造紧循环。
			account.NextPatrolAt = timePointer(regularNext)
		}
		return nil
	}); err != nil {
		return err
	}
	if a.ensureUsed {
		rounds, err := a.manager.store.RecentPatrolRounds(a.key(), 3)
		if err != nil {
			return err
		}
		consecutive := 0
		for _, round := range rounds {
			if a.manager.localDate(round.StartedAt) != a.account.EnabledDate ||
				!strings.HasSuffix(round.Trigger, surfaceRecoverySuffix) {
				break
			}
			consecutive++
		}
		if consecutive >= 3 {
			if err := a.manager.pauseAccount(a.key(), PauseSurfaceDrivenAway, a.now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *roundActor) setStage(stage string) error {
	return a.manager.store.MutatePatrolRound(a.account.Platform, a.account.AccountRef, a.roundID, func(round *store.PatrolRound) error {
		round.Stage = stage
		return nil
	})
}

func (a *roundActor) key() store.AccountKey {
	return store.AccountKey{Platform: a.account.Platform, AccountRef: a.account.AccountRef}
}

// beginUnreadPass uses the same baseline comparison at every ordinary
// candidate boundary. A completed pass records its end total, so an unchanged
// residual badge will not re-enter. An incomplete pass defers retry until the
// next ordinary scan instead of spinning inside this actor.
func (a *roundActor) beginUnreadPass(at string) bool {
	if a.unreadRetryDeferred {
		a.noteUnreadDecision(at, "本轮已延后", nil, false)
		return false
	}
	needed, reason, baseline := a.manager.unreadPassDecision(a.account, a.hand.UnreadTotal)
	a.noteUnreadDecision(at, reason, baseline, needed)
	if !needed {
		return false
	}
	a.unreadAttemptedRefs = make(map[string]struct{})
	return true
}

// noteUnreadDecision 记录插队判定的输入与结论。四种不插队成因（读不到 / 与基线
// 同值 / 零未读 / 本轮已延后）在外部表现一模一样，但要修的地方完全不同，不落
// 日志就只能靠事后轮询碰运气。轮首必记；其后一轮几十个会话边界，只记结论变化
// 沿，否则真正的转折会被淹没。
func (a *roundActor) noteUnreadDecision(at, reason string, baseline *int, needed bool) {
	if at != unreadDecisionAtRoundStart && a.lastUnreadDecision == reason {
		return
	}
	a.lastUnreadDecision = reason
	slog.Info("未读插队判定",
		"platform", a.account.Platform,
		"accountRef", a.account.AccountRef,
		"roundId", a.roundID,
		"at", at,
		"current", optionalIntText(a.hand.UnreadTotal),
		"baseline", optionalIntText(baseline),
		"decision", reason,
		"enter", needed)
}

const (
	unreadDecisionAtRoundStart = "轮首"
	unreadDecisionAtBoundary   = "会话边界"
)

// optionalIntText 让"缺席"与"零"在日志里一眼可分：前者是传感通道断了，后者是
// 有效的清空信号，两者导致的后续动作完全不同。
func optionalIntText(value *int) string {
	if value == nil {
		return "读不到"
	}
	return strconv.Itoa(*value)
}

func (a *roundActor) refreshHandState(ctx context.Context) (HandState, error) {
	if err := ctx.Err(); err != nil {
		return HandState{}, err
	}
	state, err := a.manager.hands.State(ctx, a.account.BoundHandID)
	if err != nil {
		return HandState{}, err
	}
	if !state.Online ||
		state.Session != a.hand.Session ||
		state.BootID != a.hand.BootID {
		return HandState{}, ErrActorGenerationChanged
	}
	a.hand.UnreadTotal = nil
	if state.UnreadTotal != nil {
		value := *state.UnreadTotal
		a.hand.UnreadTotal = &value
		state.UnreadTotal = &value
	}
	return state, nil
}

func (a *roundActor) ensureWithinDailyWindow() error {
	if a.manager.localDate(a.manager.now()) != a.manager.localDate(a.now) ||
		a.account.EnabledDate != a.manager.localDate(a.now) {
		return ErrDailyWindowExpired
	}
	return nil
}

// ensureDispatchAllowed 在每一条新命令落账前复读 actor 持久状态。
// 这使长 readList 执行期间到达的用户事件/停止指令能阻止后续
// readThread；已在途命令仍自然完成，不另造取消旁路。
func (a *roundActor) ensureDispatchAllowed(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.ensureWithinDailyWindow(); err != nil {
		return err
	}
	now := a.manager.now()
	current, err := a.manager.store.AccountByKey(a.key())
	if err != nil {
		return err
	}
	if current == nil {
		return store.ErrAccountNotFound
	}
	if !a.manager.enabledToday(*current, now) {
		return ErrActorPaused
	}
	if current.BoundHandID != a.account.BoundHandID ||
		!sameFingerprint(current.PrincipalFingerprint, a.account.PrincipalFingerprint) {
		return ErrActorGenerationChanged
	}
	hand, err := a.manager.hands.State(ctx, a.account.BoundHandID)
	if err != nil {
		return err
	}
	if !hand.Online || hand.Session != a.hand.Session || hand.BootID != a.hand.BootID {
		return ErrActorGenerationChanged
	}
	return a.ensureSourcingBatchGenerationCurrent()
}

// ensureChainDispatchAllowed 是链内推进（同一 turn 内紧接前项正证的下一
// 动作）专用的派发复核。按《24点边界裁决-2026-07-28》，已发出首条可见
// 动作的链跨过 24:00 继续收束，因此链内不复核日窗口与本地日界；其余闸
// 原样保留——上下文取消、账号显式暂停/停止（用户点暂停必须仍能在下一
// 动作前截住链）、手换代与采集批次换代。次日恢复轨对残留 planned 的派
// 发不是链内推进，仍走 ensureDispatchAllowed 的完整复核。
func (a *roundActor) ensureChainDispatchAllowed(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := a.manager.store.AccountByKey(a.key())
	if err != nil {
		return err
	}
	if current == nil {
		return store.ErrAccountNotFound
	}
	if current.EnabledAt == nil || current.StoppedAt != nil ||
		current.PausedReason != "" {
		return ErrActorPaused
	}
	if current.BoundHandID != a.account.BoundHandID ||
		!sameFingerprint(current.PrincipalFingerprint, a.account.PrincipalFingerprint) {
		return ErrActorGenerationChanged
	}
	hand, err := a.manager.hands.State(ctx, a.account.BoundHandID)
	if err != nil {
		return err
	}
	if !hand.Online || hand.Session != a.hand.Session || hand.BootID != a.hand.BootID {
		return ErrActorGenerationChanged
	}
	return a.ensureSourcingBatchGenerationCurrent()
}

// freezeSourcingBatchGeneration 在线性化的 actor 开始点只冻结活动批次 ID。
// preparing→collecting 等同批状态变化不构成任务换代；空串明确表示当时没有
// 活动采集批次。
func (a *roundActor) freezeSourcingBatchGeneration() error {
	batchID, err := a.activeSourcingBatchID()
	if err != nil {
		return err
	}
	a.sourcingBatchIDAtStart = batchID
	return nil
}

func (a *roundActor) activeSourcingBatchID() (string, error) {
	batch, err := a.manager.store.ActiveSourcingBatch(a.key())
	if err != nil || batch == nil {
		return "", err
	}
	return batch.BatchID, nil
}

func (a *roundActor) detectSourcingBatchSuperseded() error {
	currentID, err := a.activeSourcingBatchID()
	if err != nil {
		return err
	}
	if currentID == a.sourcingBatchIDAtStart {
		return nil
	}
	// 达标事务会把本轮自己持有的批次原子终局为 completed，使 active
	// 查询自然从原 ID 变为空。这是本轮的正常出口，不是另一个任务接管。
	// 若此时已有新 active ID，或原批次是 stopped，则仍按换代处理。
	if a.sourcingBatchIDAtStart != "" && currentID == "" {
		original, loadErr := a.manager.store.SourcingBatchByID(a.sourcingBatchIDAtStart)
		if loadErr != nil {
			return loadErr
		}
		if original != nil &&
			original.Status == store.SourcingBatchCompleted &&
			original.EndedAt != nil {
			return nil
		}
	}
	a.superseded = true
	return ErrRoundSupersededBySourcingBatch
}

func (a *roundActor) ensureSourcingBatchGenerationCurrent() error {
	if err := a.detectSourcingBatchSuperseded(); err != nil {
		return err
	}
	if a.trigger == TriggerCurrentConversation && a.sourcingBatchIDAtStart != "" {
		return ErrCurrentConversationSourcingActive
	}
	return nil
}

// resolvePostWait 只在逻辑命令已由 Wait 收编后裁决旧任务是否还能消费结果。
// 账号错位、登出等当前世界事实仍按既有账号级语义处理；其余旧任务局部失败
// 在采集批次换代后只终止旧轮，不能触发 handManualReview 暂停新批次。
func (a *roundActor) resolvePostWait(ctx context.Context, waitErr error) error {
	gateErr := a.ensureDispatchAllowed(ctx)
	if errors.Is(gateErr, ErrRoundSupersededBySourcingBatch) &&
		isAccountWideRunFailure(waitErr) {
		return waitErr
	}
	if gateErr != nil {
		return gateErr
	}
	return waitErr
}

// resolveFinishGeneration 覆盖“最后一条命令已返回、旧 execute 刚结束、新批次
// 才线性化”的窄窗口。调用方与 StartSourcing 共持 Manager.mu，因而要么旧轮
// 先正常收尾、再由新批次覆盖调度，要么旧轮观察到换代并完全不碰 Account。
func (a *roundActor) resolveFinishGeneration(runErr error) (error, error) {
	generationErr := a.detectSourcingBatchSuperseded()
	if generationErr == nil {
		return runErr, nil
	}
	if !errors.Is(generationErr, ErrRoundSupersededBySourcingBatch) {
		return runErr, generationErr
	}
	if isAccountWideRunFailure(runErr) {
		return runErr, nil
	}
	return ErrRoundSupersededBySourcingBatch, nil
}

func isAccountWideRunFailure(err error) bool {
	typed := runError(err)
	if typed == nil {
		return false
	}
	switch {
	case typed.Code == protocol.ErrCodeAccountMismatch:
		return true
	case typed.Code == protocol.ErrCodeCtxNotReady &&
		(typed.Reason == protocol.NotReadyReasonLoginRequired ||
			typed.Reason == protocol.NotReadyReasonIdentityUnverified):
		return true
	default:
		// USER_ACTIVE 不再是账号级失败：真人活动让位本轮、下轮重试
		//（2026-07-27 甲方裁决废除静默窗）。
		return false
	}
}

func sameFingerprint(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
