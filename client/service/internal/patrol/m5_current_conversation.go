package patrol

import (
	"context"
	"errors"
	"strings"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// ProcessCurrentConversationOnce 是真人显式触发的正式生产入口。它与普通
// Tick 共用 tickMu、roundActor、原语 Runner、状态机与 effect rail，但不读
// 会话列表，也不消费账号级 dirty/nextPatrolAt 调度提示。
func (m *Manager) ProcessCurrentConversationOnce(
	ctx context.Context,
	key store.AccountKey,
) (RoundOutcome, error) {
	m.tickMu.Lock()
	defer m.tickMu.Unlock()

	now := m.now()
	account, err := m.store.AccountByKey(key)
	if err != nil {
		return RoundOutcome{}, err
	}
	if account == nil {
		return RoundOutcome{}, store.ErrAccountNotFound
	}
	if !m.enabledToday(*account, now) {
		return RoundOutcome{}, ErrActorPaused
	}
	if account.PrincipalFingerprint == nil ||
		strings.TrimSpace(*account.PrincipalFingerprint) == "" ||
		strings.TrimSpace(account.BoundHandID) == "" {
		return RoundOutcome{}, ErrAccountNotBound
	}
	if account.IdentityState == store.IdentityInvalid ||
		account.IdentityState == store.IdentityUnbound {
		return RoundOutcome{}, ErrIdentityInvalid
	}
	if batch, batchErr := m.store.ActiveSourcingBatch(key); batchErr != nil {
		return RoundOutcome{}, batchErr
	} else if batch != nil {
		return RoundOutcome{}, ErrCurrentConversationSourcingActive
	}
	hand, err := m.hands.State(ctx, account.BoundHandID)
	if err != nil {
		return RoundOutcome{}, err
	}
	if !hand.Online {
		return RoundOutcome{}, ErrActorGenerationChanged
	}

	outcome := m.runCurrentConversationRound(ctx, account, hand, now)
	if outcome.Err != nil {
		return outcome, outcome.Err
	}
	return outcome, nil
}

func (m *Manager) runCurrentConversationRound(
	ctx context.Context,
	account *store.Account,
	hand HandState,
	now time.Time,
) RoundOutcome {
	key := store.AccountKey{Platform: account.Platform, AccountRef: account.AccountRef}
	roundID := m.config.NewRoundID()
	outcome := RoundOutcome{
		Key: key, RoundID: roundID, Trigger: TriggerCurrentConversation, Status: "failed",
	}
	if roundID == "" {
		outcome.Err = errors.New("NewRoundID 返回空值")
		return outcome
	}
	if err := m.store.CreatePatrolRound(&store.PatrolRound{
		Platform: account.Platform, AccountRef: account.AccountRef, RoundID: roundID,
		Trigger: TriggerCurrentConversation, Status: "running", Stage: "starting", StartedAt: now,
	}); err != nil {
		outcome.Err = err
		return outcome
	}

	actor := &roundActor{
		manager: m, account: account, hand: hand, roundID: roundID,
		trigger: TriggerCurrentConversation, now: now,
		bypassManualQuiet: true, requireCurrentThread: true,
	}
	untilMidnight := m.nextLocalMidnight(now).Sub(now)
	roundCtx, cancel := context.WithTimeout(ctx, untilMidnight)
	m.mu.Lock()
	func() {
		defer m.mu.Unlock()
		outcome.Err = actor.execute(roundCtx)
	}()
	cancel()
	if errors.Is(outcome.Err, context.DeadlineExceeded) &&
		m.localDate(m.now()) != m.localDate(now) {
		outcome.Err = ErrDailyWindowExpired
	}
	if errors.Is(outcome.Err, ErrDailyWindowExpired) {
		_ = m.pauseAccount(key, PauseDailyExpired, m.now())
	}
	outcome.EnsureUsed = actor.ensureUsed
	outcome.Projections = actor.projection
	if outcome.Err == nil {
		outcome.Status = "ok"
	}
	if finishErr := actor.finishCurrentConversation(outcome.Err); finishErr != nil {
		if outcome.Err == nil {
			outcome.Err = finishErr
			outcome.Status = "failed"
		} else {
			outcome.Err = errors.Join(outcome.Err, finishErr)
		}
	}
	return outcome
}

func (a *roundActor) executeCurrentConversationOnce(ctx context.Context) error {
	if err := a.setStage("identifyingCurrentConversation"); err != nil {
		return err
	}
	current, err := invokePrimitiveDirect[protocol.ChatIdentifyCurrentConversationData](
		ctx,
		a,
		protocol.PrimChatIdentifyCurrentConversation,
		protocol.ChatIdentifyCurrentConversationArgs{},
	)
	if err != nil {
		a.handleCommandFailure(err)
		return err
	}
	key := store.ConversationKey{
		Platform: a.account.Platform, AccountRef: a.account.AccountRef,
		ConversationRef: current.ConversationRef,
	}
	profile, err := a.manager.store.CandidateProfileByConversation(key)
	if err != nil {
		return err
	}
	if profile == nil || profile.ConversationRef == nil ||
		*profile.ConversationRef != current.ConversationRef {
		return ErrCurrentConversationUntracked
	}
	if profile.BackendJobID == nil || strings.TrimSpace(*profile.BackendJobID) == "" {
		return ErrCurrentConversationJobUnbound
	}
	head, err := a.manager.store.CurrentLegacyJobAIContextByBackendJobID(
		*profile.BackendJobID,
	)
	if err != nil {
		return err
	}
	if head == nil {
		return ErrCurrentConversationContextMissing
	}
	target, ready, err := a.manager.store.CommunicationTargetForProfile(profile.ProfileID)
	if err != nil {
		if errors.Is(err, store.ErrCommunicationV4Missing) {
			return ErrCurrentConversationV4NotReady
		}
		return err
	}
	if !ready || target == nil ||
		target.Conversation.ConversationRef != current.ConversationRef {
		return ErrCurrentConversationV4NotReady
	}
	ledger, err := a.manager.store.MessagesForConversation(key)
	if err != nil {
		return err
	}

	if err := a.setStage("readingThread"); err != nil {
		return err
	}
	projection, err := a.reconcileConversation(ctx, dirtyConversation{
		conversation: target.Conversation,
		ledger:       ledger,
	})
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

	return a.processCommunicationV4Profile(ctx, profile.ProfileID)
}

func (a *roundActor) finishCurrentConversation(runErr error) error {
	finishedAt := a.manager.now()
	status := "ok"
	stage := "finished"
	if runErr != nil {
		status = "failed"
		stage = "failed"
	}
	return a.manager.store.MutatePatrolRound(
		a.account.Platform,
		a.account.AccountRef,
		a.roundID,
		func(round *store.PatrolRound) error {
			round.Status = status
			round.Stage = stage
			round.ErrorCode = errorCode(runErr)
			round.FinishedAt = timePointer(finishedAt)
			return nil
		},
	)
}
