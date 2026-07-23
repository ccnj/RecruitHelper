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
	projection              []ConversationProjection
}

type threadSnapshot struct {
	messages      []syncledger.SnapshotMessage
	peer          *protocol.PeerSummary
	reachedTop    bool
	anchorMatched bool
}

type dirtyConversation struct {
	conversation store.Conversation
	ledger       []store.Message
}

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
		err = actor.execute(roundCtx)
	}()
	cancel()
	if errors.Is(err, context.DeadlineExceeded) && m.localDate(m.now()) != m.localDate(now) {
		err = ErrDailyWindowExpired
	}
	if errors.Is(err, ErrDailyWindowExpired) {
		_ = m.pauseAccount(key, PauseDailyExpired, m.now())
	}
	outcome.EnsureUsed = actor.ensureUsed
	outcome.Projections = actor.projection
	outcome.Err = err
	if err == nil {
		outcome.Status = "ok"
	}

	if finishErr := actor.finish(err); finishErr != nil {
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
	if batch != nil {
		if err := a.runSourcingBatch(ctx, batch); err != nil {
			a.handleCommandFailure(err)
			return err
		}
		return nil
	}

	if err := a.setStage("readingList"); err != nil {
		return err
	}
	sessions, err := a.readList(ctx)
	if err != nil {
		a.handleCommandFailure(err)
		return err
	}
	entries, err := listEntries(sessions)
	if err != nil {
		return err
	}
	if err := a.manager.store.SaveConversationList(store.SaveConversationListRequest{
		Platform: a.account.Platform, AccountRef: a.account.AccountRef, RoundID: a.roundID,
		ObservedAt: a.manager.now(), Complete: true, Entries: entries,
	}); err != nil {
		return err
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
		return err
	}
	if err := a.ensureCommunicationV4Roots(); err != nil {
		return err
	}
	if err := a.ensureSourcingCommunicationContexts(); err != nil {
		return err
	}
	if err := a.ensureSourcingCommunicationResumes(); err != nil {
		return err
	}
	listComplete := true
	if err := a.manager.store.MutatePatrolRound(a.account.Platform, a.account.AccountRef, a.roundID, func(round *store.PatrolRound) error {
		round.ListComplete = &listComplete
		return nil
	}); err != nil {
		return err
	}

	dirty, err := a.detectDirty(sessions)
	if err != nil {
		return err
	}
	for i := range dirty {
		if err := a.setStage("readingThread"); err != nil {
			return err
		}
		projection, err := a.reconcileConversation(ctx, dirty[i])
		if len(projection.Messages) != 0 || len(projection.CardTransitions) != 0 {
			a.projection = append(a.projection, projection)
		}
		if err != nil {
			a.handleCommandFailure(err)
			return err
		}
		if a.classificationCorrected {
			return nil
		}
	}
	return a.processCommunicationV4Targets(ctx)
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
	switch target.Profile.ResumeCaptureState {
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
		ProfileID: target.Profile.ProfileID,
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
		return a.finishResumeCaptureFailure(target.Profile.ProfileID, logicalID, resumeFailureReason(runErr))
	}
	meta := protocol.Primitives[protocol.PrimCandidateReadResume]
	if err := protocol.ValidatePrimitiveData(protocol.PrimCandidateReadResume, meta.Ver, raw); err != nil {
		return a.finishResumeCaptureFailure(target.Profile.ProfileID, logicalID, "invalidResult")
	}
	var data protocol.CandidateReadResumeData
	if err := json.Unmarshal(raw, &data); err != nil {
		return a.finishResumeCaptureFailure(target.Profile.ProfileID, logicalID, "invalidResult")
	}
	_, err = a.manager.store.CompleteResumeCapture(store.CompleteResumeCaptureRequest{
		ProfileID: target.Profile.ProfileID, LogicalDispatchID: logicalID,
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
	data, err := invokePrimitive[protocol.ProbePlatformData](ctx, a, protocol.PrimProbePlatform, protocol.ProbePlatformArgs{})
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

func (a *roundActor) readList(ctx context.Context) ([]protocol.ConversationSummary, error) {
	var aggregate []protocol.ConversationSummary
	cursor := ""
	restarts := 0
	seen := map[string]struct{}{}
	for page := 0; page < a.manager.config.MaxPages; page++ {
		args := protocol.ChatReadListArgs{
			Cursor: cursor, Filter: protocol.ListFilterAll,
			MaxSessions: protocol.DefaultPaginationReadListMaxItems, StopOlderThanDays: 8,
		}
		data, err := invokePrimitive[protocol.ChatReadListData](ctx, a, protocol.PrimChatReadList, args)
		if err != nil {
			if isRunError(err, protocol.ErrCodeCursorInvalid) && cursor != "" && restarts == 0 {
				restarts++
				aggregate = nil
				cursor = ""
				seen = map[string]struct{}{}
				page = -1
				continue
			}
			return nil, err
		}
		aggregate = append(aggregate, data.Sessions...)
		if data.Complete {
			return aggregate, nil
		}
		next := *data.NextCursor // generated validation requires it here.
		if _, duplicate := seen[next]; duplicate || next == cursor {
			return nil, ErrPaginationLoop
		}
		seen[next] = struct{}{}
		cursor = next
	}
	return nil, ErrPaginationLimit
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

func (a *roundActor) detectDirty(sessions []protocol.ConversationSummary) ([]dirtyConversation, error) {
	observedAt := a.manager.now()
	byRef := make(map[string]protocol.ConversationSummary, len(sessions))
	for _, summary := range sessions {
		byRef[summary.ConversationRef] = summary
	}
	tracked, err := a.manager.store.TrackedConversations(a.key())
	if err != nil {
		return nil, err
	}
	out := make([]dirtyConversation, 0, len(tracked))
	for _, conversation := range tracked {
		key := store.ConversationKey{
			Platform: conversation.Platform, AccountRef: conversation.AccountRef,
			ConversationRef: conversation.ConversationRef,
		}
		ledger, err := a.manager.store.MessagesForConversation(key)
		if err != nil {
			return nil, err
		}
		dirty := conversation.TrackingState == store.TrackingPending
		if conversation.LastSyncedAt == nil ||
			!observedAt.Before(conversation.LastSyncedAt.Add(a.manager.config.TrackedReconcileInterval)) {
			// 事件、未读与列表摘要都是提示；低频到期对账才保证提示全丢、
			// 同文连续消息或旧卡状态变化时仍能恢复账本。
			dirty = true
		}
		if summary, listed := byRef[conversation.ConversationRef]; listed {
			if summary.UnreadCount > 0 || len(ledger) == 0 {
				dirty = true
			} else if !syncledger.ListPreviewMatches(syncledger.ListPreview{
				Direction: string(summary.LastMessage.Direction), Kind: string(summary.LastMessage.Kind),
				Text: summary.LastMessage.TextPreview,
			}, ledger[len(ledger)-1]) {
				dirty = true
			}
		}
		if dirty {
			out = append(out, dirtyConversation{conversation: conversation, ledger: ledger})
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
			Window: protocol.ThreadWindow{
				AnchorTail: anchors, Deep: deep, MaxMessages: protocol.DefaultPaginationReadThreadMaxItems,
			},
		}
		data, err := invokePrimitive[protocol.ChatReadThreadData](ctx, a, protocol.PrimChatReadThread, args)
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
		out[i] = syncledger.SnapshotMessage{
			Direction: string(message.Direction), Kind: string(message.Kind), Text: message.Text,
			BlobRef: blobRef, ContentHash: message.ContentHash, CardType: cardType,
			CardState: cardState, TsApproxMs: message.TsApprox, Origin: "external",
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
	if gateErr := actor.ensureDispatchAllowed(ctx); gateErr != nil {
		return zero, logicalID, gateErr
	}
	if err != nil {
		return zero, logicalID, err
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
	if err := a.manager.store.MutateAccount(a.key(), func(account *store.Account) error {
		account.LastPatrolAt = timePointer(finishedAt)
		regularNext := finishedAt.Add(a.manager.config.PatrolInterval)
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
	if current.ManualQuietUntil != nil && now.Before(*current.ManualQuietUntil) {
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
	return nil
}

func sameFingerprint(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
