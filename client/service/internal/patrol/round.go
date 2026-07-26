package patrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	bypassManualQuiet       bool
	requireCurrentThread    bool
	sourcingBatchIDAtStart  string
	superseded              bool
	freshListRequired       bool
	listSnapshotGeneration  uint64
	checkedConversationRefs map[string]struct{}
	unreadRetryDeferred     bool
	unreadAttemptedRefs     map[string]struct{}
	projection              []ConversationProjection
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
	sessions   []protocol.ConversationSummary
	complete   bool
	nextCursor string
}

type conversationListPageOutcome uint8

const (
	conversationListPageContinue conversationListPageOutcome = iota
	conversationListPageFresh
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
		bypassManualQuiet:    trigger == TriggerCurrentConversation,
		requireCurrentThread: trigger == TriggerCurrentConversation,
	}
	// 生产 Clock 下这个 timeout 会在本地 24:00 取消正在等待的 dispatcher。
	// 注入假时钟的测试则由每次原语前后的日边界复核立即截断。
	untilMidnight := m.nextLocalMidnight(now).Sub(now)
	roundCtx, cancel := context.WithTimeout(ctx, untilMidnight)
	// actor 短锁覆盖所有本地判定、Start 与账本提交，但 invokePrimitiveDirect
	// 会在 Wait 期间主动释放它。事件/暂停/改绑因此只能线性化在命令启动前
	// 或启动后，绝不会钻进“门禁已过、socket 尚未送入”的缝隙。
	m.mu.Lock()
	func() {
		defer m.mu.Unlock()
		if err = actor.freezeSourcingBatchGeneration(); err == nil {
			err = actor.execute(roundCtx)
		}
	}()
	cancel()
	if errors.Is(err, context.DeadlineExceeded) && m.localDate(m.now()) != m.localDate(now) {
		err = ErrDailyWindowExpired
	}
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
	cursor := ""
	startAt := protocol.ListStartTop
	seenCursors := make(map[string]struct{})
	seenConversations := make(map[string]struct{})
	a.checkedConversationRefs = make(map[string]struct{})
	_, err = a.refreshHandState(ctx)
	if err != nil {
		return err
	}
	if a.manager.config.MaxPages >= 2 && a.beginUnreadPass() {
		filter = protocol.ListFilterUnread
	}
	pagesRead := 0
	resetListRead := func(nextStart protocol.ListStart) {
		cursor = ""
		startAt = nextStart
		seenCursors = make(map[string]struct{})
		seenConversations = make(map[string]struct{})
	}
	startAllScan := func() {
		filter = protocol.ListFilterAll
		resetListRead(protocol.ListStartTop)
		// Returning from unread creates a new ordinary scan generation. The
		// process-local list fingerprint, not this call-stack set, decides
		// whether a previously seen conversation needs another detail read.
		a.checkedConversationRefs = make(map[string]struct{})
	}
	for pagesRead < a.manager.config.MaxPages {
		// Entering unread reserves one actual all-list read to close the
		// platform filter. The reserve is part of the same MaxPages budget.
		if filter == protocol.ListFilterUnread &&
			pagesRead >= a.manager.config.MaxPages-1 {
			a.unreadRetryDeferred = true
			startAllScan()
			continue
		}
		if err := a.setStage("readingList"); err != nil {
			return err
		}
		page, err := a.readListPage(ctx, cursor, filter, startAt)
		pagesRead++
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
			if isRunError(err, protocol.ErrCodeUserActive) ||
				isRunError(err, protocol.ErrCodeCursorInvalid) {
				// readList 的 USER_ACTIVE/CURSOR_INVALID 只说明本轮页面
				// 快照已经换代。已完成的会话事实照常保留，下一巡检从
				// fresh 当前窗口重读；这里没有人工草稿或发送动作可重试。
				a.freshListRequired = true
				return nil
			}
			a.handleCommandFailure(err)
			return err
		}
		for _, summary := range page.sessions {
			if _, duplicate := seenConversations[summary.ConversationRef]; duplicate {
				return fmt.Errorf("%w: %s", store.ErrDuplicateConversationEntry, summary.ConversationRef)
			}
			seenConversations[summary.ConversationRef] = struct{}{}
		}
		outcome, err := a.processConversationListPage(
			ctx,
			page,
			filter,
			pagesRead < a.manager.config.MaxPages-1,
		)
		if err != nil {
			return err
		}
		switch outcome {
		case conversationListPageFresh:
			// readThread、简历读取或候选人可见动作都可能切换会话、清除
			// 未读或重排列表。旧 page/cursor 此刻不再是导航依据；同一
			// 完整扫描保留 checked 集合，并从当前物理窗口建立无 cursor
			// 的 fresh 页面快照继续。只有筛选切换才重新从顶部建立快照。
			if filter == protocol.ListFilterAll &&
				pagesRead < a.manager.config.MaxPages-1 {
				_, stateErr := a.refreshHandState(ctx)
				if stateErr != nil {
					return stateErr
				}
				if a.beginUnreadPass() {
					filter = protocol.ListFilterUnread
					resetListRead(protocol.ListStartTop)
					continue
				}
			}
			resetListRead(protocol.ListStartCurrent)
			continue
		case conversationListPageSwitchUnread:
			filter = protocol.ListFilterUnread
			resetListRead(protocol.ListStartTop)
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
		if page.nextCursor == "" || page.nextCursor == cursor {
			return ErrPaginationLoop
		}
		if _, duplicate := seenCursors[page.nextCursor]; duplicate {
			return ErrPaginationLoop
		}
		seenCursors[page.nextCursor] = struct{}{}
		cursor = page.nextCursor
	}
	// MaxPages 是本次完整扫描所有 fresh restart 共享的总读取预算。
	// 预算耗尽只表示部分扫描已经安全收束；不把它伪装成业务失败，
	// 下一轮从 fresh 当前列表继续。
	a.freshListRequired = true
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

	dirty, err := a.detectDirty(
		page.sessions,
		filter == protocol.ListFilterUnread,
	)
	if err != nil {
		return conversationListPageContinue, err
	}
	dirtyByRef := make(map[string]dirtyConversation, len(dirty))
	for index := range dirty {
		dirtyByRef[dirty[index].conversation.ConversationRef] = dirty[index]
	}
	if filter == protocol.ListFilterAll && a.classificationCorrected {
		// A classification correction encountered in unread mode still needs
		// this actual all-list read to close the page filter, but it must not
		// authorize another candidate after the correction stop boundary.
		return conversationListPageStop, nil
	}
	if filter == protocol.ListFilterUnread {
		return a.processUnreadConversationListPage(ctx, page, dirtyByRef)
	}
	for _, summary := range page.sessions {
		if _, checked := a.checkedConversationRefs[summary.ConversationRef]; checked {
			continue
		}
		if canEnterUnread {
			_, stateErr := a.refreshHandState(ctx)
			if stateErr != nil {
				return conversationListPageContinue, stateErr
			}
			if a.beginUnreadPass() {
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
		_, readCurrent := dirtyByRef[summary.ConversationRef]
		if dirtyConversation, ok := dirtyByRef[summary.ConversationRef]; ok {
			a.checkedConversationRefs[summary.ConversationRef] = struct{}{}
			if err := a.setStage("readingThread"); err != nil {
				return conversationListPageContinue, err
			}
			projection, err := a.reconcileConversation(ctx, dirtyConversation)
			if len(projection.Messages) != 0 || len(projection.CardTransitions) != 0 {
				a.projection = append(a.projection, projection)
			}
			if err != nil {
				if isRunError(err, protocol.ErrCodeTargetNotFound) {
					// 目标在 click 前离开本轮可定位窗口，没有产生页面
					// 副作用。把它视为快照过期，禁止数据库反向找人；
					// 当前 ref 本次已尝试，fresh 后继续后续会话。
					return conversationListPageFresh, nil
				}
				a.handleCommandFailure(err)
				return conversationListPageContinue, err
			}
			a.manager.markListHintVerified(
				dirtyConversation.listHintKey,
				dirtyConversation.listHintFingerprint,
			)
			if a.classificationCorrected {
				return conversationListPageStop, nil
			}
		}
		key := store.ConversationKey{
			Platform: a.account.Platform, AccountRef: a.account.AccountRef,
			ConversationRef: summary.ConversationRef,
		}
		profile, err := a.manager.store.CandidateProfileByConversation(key)
		if err != nil {
			return conversationListPageContinue, err
		}
		if profile == nil {
			if readCurrent {
				return conversationListPageFresh, nil
			}
			continue
		}
		a.checkedConversationRefs[summary.ConversationRef] = struct{}{}
		snapshotGeneration := a.listSnapshotGeneration
		if err := a.prepareInboundConversationProfile(
			ctx,
			*profile,
		); err != nil {
			return conversationListPageContinue, err
		}
		if err := a.processCommunicationV4Profile(ctx, profile.ProfileID); err != nil {
			return conversationListPageContinue, err
		}
		if readCurrent || a.listSnapshotGeneration != snapshotGeneration {
			return conversationListPageFresh, nil
		}
	}
	return conversationListPageContinue, nil
}

const unreadPatrolAuditCategory = "unread_patrol"

func (a *roundActor) processUnreadConversationListPage(
	ctx context.Context,
	page conversationListPage,
	dirtyByRef map[string]dirtyConversation,
) (conversationListPageOutcome, error) {
	for _, summary := range page.sessions {
		if _, attempted := a.unreadAttemptedRefs[summary.ConversationRef]; attempted {
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
			return conversationListPageContinue, err
		}
		if profile == nil {
			return a.openUnreadConversationWithoutAutomation(ctx, summary.ConversationRef)
		}
		_, ready, targetErr := a.manager.store.CommunicationTargetForProfile(
			profile.ProfileID,
		)
		// A retained profile without a V4 root is not automation-ready. It
		// still has to be opened once to clear unread, but may not be promoted
		// into resume capture, state-machine work or AI from this cleanup pass.
		if targetErr != nil && !errors.Is(targetErr, store.ErrCommunicationV4Missing) {
			return conversationListPageContinue, targetErr
		}
		if !ready {
			return a.openUnreadConversationWithoutAutomation(ctx, summary.ConversationRef)
		}

		dirtyConversation, ok := dirtyByRef[summary.ConversationRef]
		if !ok {
			return conversationListPageContinue, errors.New(
				"已有候选人档案的未读会话缺少可对账会话事实",
			)
		}
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
				return conversationListPageFresh, nil
			}
			a.handleCommandFailure(err)
			return conversationListPageContinue, err
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
			return conversationListPageContinue, err
		}
		if err := a.processCommunicationV4Profile(ctx, profile.ProfileID); err != nil {
			return conversationListPageContinue, err
		}
		return conversationListPageFresh, nil
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
) (conversationListPageOutcome, error) {
	if err := a.setStage("openingUnreadConversation"); err != nil {
		return conversationListPageContinue, err
	}
	a.invalidateListSnapshot()
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
				return conversationListPageContinue, auditErr
			}
			return conversationListPageFresh, nil
		}
		a.handleCommandFailure(err)
		return conversationListPageContinue, err
	}
	if data.ConversationRef != conversationRef {
		err := &RunError{
			Code:       protocol.ErrCodeInternalHand,
			Retryable:  protocol.RetryableManualOnly,
			SideEffect: protocol.SideEffectPossible,
			Cause:      errors.New("未读会话打开结果与目标会话不一致"),
		}
		a.handleCommandFailure(err)
		return conversationListPageContinue, err
	}
	return conversationListPageFresh, nil
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
			(runErr.Code == protocol.ErrCodeCtxNotReady && runErr.Reason == protocol.NotReadyReasonLoginRequired) ||
			runErr.Code == protocol.ErrCodeUserActive {
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

func (a *roundActor) captureTrialResume(ctx context.Context) error {
	target, err := a.manager.store.ActiveM5TrialForAccount(a.key())
	if err != nil || target == nil {
		return err
	}
	return a.captureResumeForProfile(ctx, target.Profile)
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
	// Resume capture is an intrusive current-conversation action. Even if the
	// result later fails, the page may already have opened/closed a resume
	// surface, so a previously observed conversation-list cursor is no longer
	// a valid navigation basis.
	a.invalidateListSnapshot()
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
	cursor string,
	filter protocol.ListFilter,
	startAt protocol.ListStart,
) (conversationListPage, error) {
	args := protocol.ChatReadListArgs{
		Cursor: cursor, Filter: filter,
		MaxSessions: protocol.DefaultPaginationReadListMaxItems,
	}
	if cursor == "" {
		args.StartAt = startAt
	}
	if filter == protocol.ListFilterAll {
		args.StopOlderThanDays = 8
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
	page := conversationListPage{
		sessions: data.Sessions,
		complete: data.Complete,
	}
	if !data.Complete {
		// contract validation guarantees a non-empty cursor for an incomplete
		// page. Keep the defensive branch local so a malformed hand result
		// cannot be interpreted as completion.
		if data.NextCursor == nil || *data.NextCursor == "" {
			return conversationListPage{}, ErrPaginationLoop
		}
		page.nextCursor = *data.NextCursor
	}
	return page, nil
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

func (a *roundActor) detectDirty(
	sessions []protocol.ConversationSummary,
	forceUnread bool,
) ([]dirtyConversation, error) {
	observedAt := a.manager.now()
	tracked, err := a.manager.store.TrackedConversations(a.key())
	if err != nil {
		return nil, err
	}
	trackedByRef := make(map[string]store.Conversation, len(tracked))
	for _, conversation := range tracked {
		trackedByRef[conversation.ConversationRef] = conversation
	}
	out := make([]dirtyConversation, 0, len(sessions))
	for _, summary := range sessions {
		conversation, listed := trackedByRef[summary.ConversationRef]
		if !listed {
			continue
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
		if conversation.LastSyncedAt == nil ||
			!observedAt.Before(conversation.LastSyncedAt.Add(a.manager.config.TrackedReconcileInterval)) {
			// 事件、未读与列表摘要都是提示；低频到期对账才保证提示全丢、
			// 同文连续消息或旧卡状态变化时仍能恢复账本。
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
		if forceUnread || forceReconcile || (hintDirty && !hintAlreadyVerified) {
			out = append(out, dirtyConversation{
				conversation:        conversation,
				ledger:              ledger,
				listHintKey:         hintKey,
				listHintFingerprint: hintFingerprint,
			})
		}
	}
	return out, nil
}

// M5 的简历读取与自动回复都只作用于已绑定的当前 IM 会话，原语本身按契约
// 不得搜索会话列表。试运行目标确实需要补采或处理入站时，本轮只对账该目标：
// 这既完成 M2→M5 的页面所有权交接，也避免无关旧会话的可恢复定位失败阻断
// 一次性试运行。其他 dirty 会话只延后到试运行释放 active slot 后的下一轮。
// 已采集且没有待处理入站时不得为维持路由而反复抢页面。
func (a *roundActor) isolateActiveM5Target(dirty []dirtyConversation) ([]dirtyConversation, error) {
	target, err := a.manager.store.ActiveM5TrialForAccount(a.key())
	if err != nil || target == nil {
		return dirty, err
	}
	key := store.ConversationKey{
		Platform: target.Conversation.Platform, AccountRef: target.Conversation.AccountRef,
		ConversationRef: target.Conversation.ConversationRef,
	}
	ledger, err := a.manager.store.MessagesForConversation(key)
	if err != nil {
		return nil, err
	}
	targetIndex := -1
	for i := range dirty {
		if dirty[i].conversation.ConversationRef == target.Conversation.ConversationRef {
			targetIndex = i
			break
		}
	}
	needsHandoff := m5TargetNeedsRouteHandoff(
		targetIndex >= 0,
		target.Profile.ResumeCaptureState,
		ledger,
	)
	if !needsHandoff {
		return dirty, nil
	}
	return []dirtyConversation{{conversation: target.Conversation, ledger: ledger}}, nil
}

func m5TargetNeedsRouteHandoff(
	alreadyDirty bool,
	captureState store.ResumeCaptureState,
	ledger []store.Message,
) bool {
	if alreadyDirty || captureState == store.ResumeCaptureUnattempted || captureState == store.ResumeCaptureInFlight {
		return true
	}
	return len(inspectM5Pending(ledger).inbound) > 0
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
		return ConversationProjection{Key: key}, a.convergeReconcileFailure(key, err)
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
			return ConversationProjection{Key: key}, a.convergeReconcileFailure(key, err)
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
		return ConversationProjection{Key: key}, a.convergeReconcileFailure(key, err)
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

func (a *roundActor) convergeReconcileFailure(key store.ConversationKey, reconcileErr error) error {
	unsafeCorrection := errors.Is(reconcileErr, syncledger.ErrUnsafeMessageClassificationCorrection) ||
		errors.Is(reconcileErr, store.ErrMessageClassificationCorrectionUnsafe)
	sourceIdentityConflict := errors.Is(reconcileErr, syncledger.ErrSourceKeySemanticConflict) ||
		errors.Is(reconcileErr, store.ErrMessageSourceKeyConflict)
	if !unsafeCorrection && !sourceIdentityConflict {
		return reconcileErr
	}
	// Stable-identity conflicts and incomplete correction evidence are not
	// retryable page-read failures. Stop the account actor before recording the
	// diagnostic so a later audit failure still cannot cause automatic rereads.
	pauseErr := a.manager.pauseAccount(a.key(), PauseHandManualReview, a.manager.now())
	category := "conversation_source_identity_conflict"
	detail := "稳定消息等值键的方向或正文哈希冲突，已暂停账号等待人工处理"
	if unsafeCorrection {
		category = "conversation_classification_correction_unsafe"
		detail = "候选修正缺少完整唯一证据，已暂停账号等待人工处理"
	}
	auditErr := a.manager.store.AppendAudit(&store.AuditEntry{
		At: a.manager.now(), Category: category,
		Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: key.ConversationRef,
		RoundID: a.roundID,
		Detail:  detail,
	})
	return errors.Join(reconcileErr, pauseErr, auditErr)
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
		// chat.readThread is intrusive: it may switch the current conversation
		// and clear an unread receipt. Once attempted, no caller may continue
		// navigating with the conversation-list snapshot observed before it.
		a.invalidateListSnapshot()
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
			endsAt := message.Interview.EndsAt
			method := string(message.Interview.Method)
			interviewStartsAtMs = &startsAt
			interviewEndsAtMs = &endsAt
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
		_ = a.manager.store.MutateAccount(a.key(), func(account *store.Account) error {
			until := a.manager.now().Add(a.manager.config.ManualQuiet)
			if account.ManualQuietUntil == nil || account.ManualQuietUntil.Before(until) {
				account.ManualQuietUntil = timePointer(until)
			}
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
		_ = a.manager.pauseAccount(a.key(), PauseHandManualReview, a.manager.now())
	}
}

func (a *roundActor) finish(runErr error) error {
	finishedAt := a.manager.now()
	status := "ok"
	stage := "finished"
	if runErr != nil {
		status = "failed"
		stage = "failed"
	} else if a.freshListRequired {
		// 页面窗口自然换代是一次已收束的部分巡检，不冒充完整扫描，
		// 也不记成业务失败。下一轮仍从无 cursor 的当前窗口开始。
		stage = "freshListPending"
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
		if a.freshListRequired {
			account.DirtyHint = true
			freshNext := finishedAt.Add(a.manager.config.MinimumRoundGap)
			if account.NextPatrolAt == nil || account.NextPatrolAt.After(freshNext) {
				account.NextPatrolAt = timePointer(freshNext)
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

func (a *roundActor) invalidateListSnapshot() {
	a.listSnapshotGeneration++
}

// beginUnreadPass uses the same baseline comparison at every ordinary
// candidate boundary. A completed pass records its end total, so an unchanged
// residual badge will not re-enter. An incomplete pass defers retry until the
// next ordinary scan instead of spinning inside this actor.
func (a *roundActor) beginUnreadPass() bool {
	if a.unreadRetryDeferred ||
		!a.manager.unreadPassNeeded(a.account, a.hand.UnreadTotal) {
		return false
	}
	a.unreadAttemptedRefs = make(map[string]struct{})
	return true
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
	if !a.bypassManualQuiet &&
		current.ManualQuietUntil != nil && now.Before(*current.ManualQuietUntil) {
		return wrapRunError(protocol.ErrCodeUserActive, "", ErrManualQuietActive)
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
	case typed.Code == protocol.ErrCodeUserActive:
		return true
	default:
		return false
	}
}

func sameFingerprint(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
