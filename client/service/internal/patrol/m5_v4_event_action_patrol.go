package patrol

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/store"
)

type communicationV4EventDependencyState uint8

const (
	communicationV4EventDependencyReady communicationV4EventDependencyState = iota
	communicationV4EventDependencyWaiting
	communicationV4EventDependencyUnavailable
)

// drainCommunicationV4EventActions is the sole patrol bridge from immutable
// V4 event-action facts into the existing M5 effect runners. The runners still
// construct the WAL atomically through the Store binding added in the prior
// batch; this loop only chooses deterministic order and supplies the frozen
// arguments.
func (a *roundActor) drainCommunicationV4EventActions(ctx context.Context) error {
	return a.drainCommunicationV4EventActionsForProfile(ctx, "")
}

func (a *roundActor) drainCommunicationV4EventActionsForProfile(
	ctx context.Context,
	profileID string,
) error {
	unresolved, err :=
		a.manager.store.CommunicationV4EventActionsNeedingProfileManualForAccount(a.key())
	if err != nil {
		return err
	}
	isolatedProfiles := make(map[string]struct{})
	for index := range unresolved {
		action := unresolved[index]
		if profileID != "" && action.ProfileID != profileID {
			continue
		}
		if _, isolated := isolatedProfiles[action.ProfileID]; isolated {
			continue
		}
		if err := a.manager.store.MarkCommunicationV4AutomationManualRequired(
			action.ProfileID,
			action.FailureReason,
			a.manager.now(),
		); err != nil {
			return err
		}
		isolatedProfiles[action.ProfileID] = struct{}{}
	}

	stoppedProfiles := make(map[string]struct{})
	seenActions := make(map[string]struct{})
	seenBaseKeys := make(map[string]struct{})
	for {
		actions, err :=
			a.manager.store.PlannedCommunicationV4EventActionsForAccount(a.key())
		if err != nil {
			return err
		}
		foundNew := false
		for index := range actions {
			action := actions[index]
			if profileID != "" && action.ProfileID != profileID {
				continue
			}
			if _, seen := seenActions[action.ActionID]; seen {
				continue
			}
			seenActions[action.ActionID] = struct{}{}
			foundNew = true
			if _, stopped := stoppedProfiles[action.ProfileID]; stopped {
				continue
			}
			// Q1/Q2 裁决(2026-08-02):跨日/跨启动残留的未派发 planned 行在
			// 派发遭遇时刻一律作废,不再续发;次日按最新世界状态重新规划。
			// 判据机械——绑过发送意图(EffectIntentID/EffectStartedAt/SentAt
			// 任一非空)的行永不作废。本枚举只列聚合 active 的候选人,被冻结
			// 候选人(聚合 manual)的 planned 行不进入枚举、天然不碰;时刻表
			// 计划物化行同规则,其 plan 失效仍由枚举查询的既有 occurrence
			// 判定收敛。作废发生在链首定向对账之前,不为死行支付页面成本。
			if action.EffectIntentID == nil &&
				action.EffectStartedAt == nil &&
				action.SentAt == nil &&
				a.manager.plannedActionStale(action.CreatedAt) {
				if err := a.manager.store.SupersedeStaleCommunicationV4EventAction(
					action.ActionID,
					a.manager.now(),
				); err != nil {
					return err
				}
				slog.Info("陈旧未派发残留作废:事件动作跨日/跨启动仍 planned,不再续发",
					"profileId", action.ProfileID,
					"actionId", action.ActionID,
					"createdAt", action.CreatedAt)
				continue
			}
			// 干净失败自动重铸(§8.4)必须自然受巡检节奏节流:同一基础动作
			// 每轮至多推进一次。快失败会在本轮内铸出 |try{n} 新行并被外层
			// 重查看见,这里按基础语义键折叠,把新尝试留给下一轮。
			baseKey := action.ProfileID + "\x00" +
				store.CommunicationActionBasePlanKey(action.SemanticActionKey)
			if _, dispatched := seenBaseKeys[baseKey]; dispatched {
				continue
			}
			seenBaseKeys[baseKey] = struct{}{}
			stopProfile, err := a.dispatchCommunicationV4EventAction(ctx, action)
			if err != nil {
				return err
			}
			if stopProfile {
				stoppedProfiles[action.ProfileID] = struct{}{}
			}
		}
		if !foundNew {
			return nil
		}
	}
}

// dispatchCommunicationV4EventAction returns stopProfile when this profile
// must not advance further in this round: either it was deliberately
// transferred to manual handling, or a schedule chain-head could not confirm
// its conversation page this round and every remaining action stays planned
// for a later patrol. A dependency that is still owned by the WAL recovery
// rail merely waits for a later patrol.
func (a *roundActor) dispatchCommunicationV4EventAction(
	ctx context.Context,
	action store.CommunicationV4EventAction,
) (bool, error) {
	var replyRunner AutomaticReplyRunner
	var cardRunner AutomaticCardRunner
	switch {
	case action.EffectKind == store.CommunicationV4EventEffectReplyText &&
		(action.V4Kind == communication.V4ActionWechatReceipt ||
			action.V4Kind == communication.V4ActionInterviewAcceptedReceipt ||
			action.V4Kind == communication.V4ActionColdPrompt ||
			action.V4Kind == communication.V4ActionColdWechatText ||
			action.V4Kind == communication.V4ActionInterviewFollowup) &&
		// 多气泡话术自第二个气泡起依赖前一个气泡的正证,这里不再按 v4Kind 限定谁
		// 允许带依赖:能否发由下面的 communicationV4EventDependency 统一裁决(前项
		// 须 sent 且已落 effectIntentID)。在此另立白名单只会让新增的多气泡形态被
		// 误判为 automaticActionInvalid。
		strings.TrimSpace(action.Text) != "" &&
		strings.TrimSpace(action.ContentHash) != "":
		var ok bool
		replyRunner, ok = a.manager.runner.(AutomaticReplyRunner)
		if !ok {
			return a.markCommunicationV4EventActionManual(
				action,
				store.CommunicationV4EventActionFailureRunnerUnavailable,
			)
		}
	case action.EffectKind == store.CommunicationV4EventEffectInviteWechat &&
		(action.V4Kind == communication.V4ActionInviteWechat ||
			action.V4Kind == communication.V4ActionColdWechatInvite) &&
		action.DependsOnActionID != nil &&
		strings.TrimSpace(action.ContentHash) != "":
		var ok bool
		cardRunner, ok = a.manager.runner.(AutomaticCardRunner)
		if !ok {
			return a.markCommunicationV4EventActionManual(
				action,
				store.CommunicationV4EventActionFailureRunnerUnavailable,
			)
		}
	case action.EffectKind == store.CommunicationV4EventEffectAcceptWechat &&
		action.V4Kind == communication.V4ActionAcceptWechat &&
		action.DependsOnActionID == nil &&
		strings.TrimSpace(action.ContentHash) != "":
		var ok bool
		cardRunner, ok = a.manager.runner.(AutomaticCardRunner)
		if !ok {
			return a.markCommunicationV4EventActionManual(
				action,
				store.CommunicationV4EventActionFailureRunnerUnavailable,
			)
		}
	default:
		return a.markCommunicationV4EventActionManual(
			action,
			store.CommunicationV4EventActionFailureActionInvalid,
		)
	}

	profile, err := a.manager.store.CandidateProfileByID(action.ProfileID)
	if err != nil {
		return false, err
	}
	if profile == nil ||
		profile.Platform != a.account.Platform ||
		profile.AccountRef != a.account.AccountRef ||
		profile.ConversationRef == nil ||
		strings.TrimSpace(*profile.ConversationRef) == "" {
		return a.markCommunicationV4EventActionManual(
			action,
			store.CommunicationV4EventActionFailureBindingUnavailable,
		)
	}

	previousIntentID := ""
	if action.DependsOnActionID == nil {
		latest, latestErr := a.manager.store.LatestEffectIntent(
			profile.Platform,
			profile.AccountRef,
			*profile.ConversationRef,
		)
		if latestErr != nil {
			return false, latestErr
		}
		if latest != nil {
			previousIntentID = latest.IntentID
		}
	} else {
		dependencyState, dependencyIntentID, dependencyErr :=
			a.communicationV4EventDependency(*action.DependsOnActionID)
		if dependencyErr != nil {
			return false, dependencyErr
		}
		switch dependencyState {
		case communicationV4EventDependencyWaiting:
			return false, nil
		case communicationV4EventDependencyUnavailable:
			return a.markCommunicationV4EventActionManual(
				action,
				store.CommunicationV4EventActionFailureDependencyUnavailable,
			)
		case communicationV4EventDependencyReady:
			previousIntentID = dependencyIntentID
		default:
			return false, store.ErrCommunicationV4EventActionConflict
		}
		if store.IsRetryCommunicationActionID(action.SemanticActionKey) {
			// 干净失败自动重试(§8.4):前次失败尝试是会话最新 intent,WAL
			// CAS 锚取最新;依赖校验仍以父正证 intent 为语义锚,并由 store
			// 侧按透明锚判据核验该最新 intent 确为前次零副作用失败尝试。
			latest, latestErr := a.manager.store.LatestEffectIntent(
				profile.Platform,
				profile.AccountRef,
				*profile.ConversationRef,
			)
			if latestErr != nil {
				return false, latestErr
			}
			if latest != nil {
				previousIntentID = latest.IntentID
			}
		}
	}

	intentID, err := store.M5AutomaticIntentID(action.ActionID)
	if err != nil {
		return a.markCommunicationV4EventActionManual(
			action,
			store.CommunicationV4EventActionFailureActionInvalid,
		)
	}
	if action.SourceInputKind == store.CommunicationV4InputSchedulePlan &&
		action.DependsOnActionID == nil {
		// 时刻表链首的目标会话通常未被本轮列表标脏、从未被打开;先用库内
		// 复核挡掉已终局/已归档档案(它们按设计仍在目标枚举里,残留 planned
		// 行不能换来每轮一次的页面白读),再定向对账完成页面导航与投影。
		stopped, err := a.recheckCommunicationV4ScheduleFallbackBeforeWAL(action)
		if err != nil || stopped {
			return stopped, err
		}
		stopped, err = a.reconcileCommunicationV4ScheduleConversationBeforeDispatch(
			ctx,
			action,
		)
		if err != nil || stopped {
			return stopped, err
		}
	}
	if err := a.waitSourcingDelay(ctx, a.manager.config.InteractionPaceWait); err != nil {
		// A process stop, account pause or hand-generation change during the
		// pacing wait is a recoverable pre-WAL interruption. Leave the durable
		// action planned for the next authorized patrol.
		return false, err
	}
	if action.SourceInputKind == store.CommunicationV4InputSchedulePlan {
		stopped, err := a.recheckCommunicationV4ScheduleFallbackBeforeWAL(action)
		if err != nil || stopped {
			return stopped, err
		}
	}

	var handle interface {
		Wait(context.Context) error
	}
	switch action.EffectKind {
	case store.CommunicationV4EventEffectReplyText:
		handle, err = replyRunner.StartAutomaticReply(
			ctx,
			AutomaticReplyRequest{
				ActionID: action.ActionID, IntentID: intentID,
				PreviousIntentID: previousIntentID,
				ExpectedSession:  a.hand.Session, ExpectedBootID: a.hand.BootID,
				Platform: profile.Platform, AccountRef: profile.AccountRef,
				ConversationRef: *profile.ConversationRef, Text: action.Text,
			},
		)
	case store.CommunicationV4EventEffectInviteWechat:
		handle, err = cardRunner.StartAutomaticCard(
			ctx,
			AutomaticCardRequest{
				ActionID: action.ActionID, IntentID: intentID,
				PreviousIntentID: previousIntentID,
				ExpectedSession:  a.hand.Session, ExpectedBootID: a.hand.BootID,
				Platform: profile.Platform, AccountRef: profile.AccountRef,
				ConversationRef:   *profile.ConversationRef,
				Kind:              store.CommunicationActionInviteWechat,
			},
		)
	case store.CommunicationV4EventEffectAcceptWechat:
		requestSourceKey, sourceErr :=
			a.manager.store.CommunicationV4AcceptWechatRequestSource(action.ActionID)
		if sourceErr != nil {
			err = sourceErr
			break
		}
		handle, err = cardRunner.StartAutomaticCard(
			ctx,
			AutomaticCardRequest{
				ActionID: action.ActionID, IntentID: intentID,
				PreviousIntentID: previousIntentID,
				ExpectedSession:  a.hand.Session, ExpectedBootID: a.hand.BootID,
				Platform: profile.Platform, AccountRef: profile.AccountRef,
				ConversationRef:   *profile.ConversationRef,
				Kind:              store.CommunicationActionAcceptWechat,
				RequestSourceKey:  requestSourceKey,
			},
		)
	}
	if err != nil || handle == nil {
		stopProfile, closeErr := a.markCommunicationV4EventActionManual(
			action,
			store.CommunicationV4EventActionFailureDispatchNotConstructed,
		)
		if closeErr != nil {
			return stopProfile, errors.Join(err, closeErr)
		}
		return stopProfile, nil
	}
	func() {
		a.manager.mu.Unlock()
		defer a.manager.mu.Lock()
		err = handle.Wait(ctx)
	}()
	if err != nil {
		// Once Start returned a handle, the persistent effect rail exclusively
		// owns recovery and terminal convergence.
		return false, err
	}
	settled, err := a.manager.store.CommunicationV4EventActionByID(action.ActionID)
	if err != nil {
		return false, err
	}
	if settled == nil {
		return false, store.ErrCommunicationV4EventActionConflict
	}
	switch settled.Status {
	case store.CommunicationV4EventActionSent:
		if action.EffectKind == store.CommunicationV4EventEffectAcceptWechat {
			// 接受动作的正证一到手，我方就已知交换成功，而消息账本要等下一轮
			// 对账才知道。登记档案，由本轮稍后的定向重对账补齐（立案 4.3）。
			if a.wechatAcceptedProfiles == nil {
				a.wechatAcceptedProfiles = make(map[string]struct{})
			}
			a.wechatAcceptedProfiles[action.ProfileID] = struct{}{}
		}
		return false, nil
	case store.CommunicationV4EventActionManualRequired:
		return true, nil
	case store.CommunicationV4EventActionRetried:
		// 干净失败已在结算事务内铸出 |try{n} 重试行(§8.4)。档案不停,新
		// 尝试受同轮基础键节流,自然留给下一轮巡检。
		return false, nil
	default:
		return false, store.ErrCommunicationV4EventActionConflict
	}
}

func (a *roundActor) recheckCommunicationV4ScheduleFallbackBeforeWAL(
	action store.CommunicationV4EventAction,
) (bool, error) {
	target, ready, err := a.manager.store.CommunicationTargetForProfile(
		action.ProfileID,
	)
	if err != nil {
		return false, err
	}
	if !ready || target == nil {
		return a.markCommunicationV4EventActionManual(
			action,
			store.CommunicationV4EventActionFailureBindingUnavailable,
		)
	}
	if target.Aggregate.State.MainStatus == communication.V4StatusEnded {
		return true, nil
	}
	archived, err := a.processCommunicationV4ScheduleArchive(*target, false)
	if err != nil || archived {
		return archived, err
	}
	return false, nil
}

// reconcileCommunicationV4ScheduleConversationBeforeDispatch 在时刻表链首动作
// 进入 WAL 前对目标会话做一次定向对账。send 系原语只认"已经打开的会话页"
// (按标签页 URL 定位、自身绝不导航),而时刻表到期的会话恰恰是列表未标脏、
// 本轮从未 readThread 过的静默会话;缺了这一步,链首派发必然
// CTX_NOT_READY/pageAbsent(2026-07-30 真机 49/49 全败)。readThread 内的路由
// 切换完成导航,读到的快照照常投影入账,随后的 WAL 前复核据此看到最新事实
// (候选人已回话即归档跟催)。链内后续气泡与卡片搭链首打开的页面,不逐项重读。
// 失败方向只允许"本轮跳过、动作保持 planned 留待下一轮",不得转 manualRequired,
// 更不得放行发送;残留 planned 行的寿命由既有七天归档兜底。
func (a *roundActor) reconcileCommunicationV4ScheduleConversationBeforeDispatch(
	ctx context.Context,
	action store.CommunicationV4EventAction,
) (bool, error) {
	target, ready, err := a.manager.store.CommunicationTargetForProfile(action.ProfileID)
	if err != nil {
		if errors.Is(err, store.ErrCommunicationV4Missing) {
			return false, nil
		}
		return false, err
	}
	if !ready || target == nil {
		// 绑定缺失交由随后的 WAL 前复核按既有语义定性,这里不重复裁决。
		return false, nil
	}
	key := store.ConversationKey{
		Platform:        target.Profile.Platform,
		AccountRef:      target.Profile.AccountRef,
		ConversationRef: target.Conversation.ConversationRef,
	}
	ledger, err := a.manager.store.MessagesForConversation(key)
	if err != nil {
		return false, err
	}
	projection, err := a.reconcileConversation(ctx, dirtyConversation{
		conversation: target.Conversation,
		ledger:       ledger,
	})
	if len(projection.Messages) != 0 || len(projection.CardTransitions) != 0 {
		a.projection = append(a.projection, projection)
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		slog.Warn(
			"时刻表链首派发前定向对账失败,动作保持 planned 留待下一轮",
			"profileId", action.ProfileID,
			"actionId", action.ActionID,
			"err", err,
		)
		return true, nil
	}
	if a.classificationCorrected {
		return true, nil
	}
	return false, nil
}

func (a *roundActor) communicationV4EventDependency(
	actionID string,
) (communicationV4EventDependencyState, string, error) {
	eventAction, err := a.manager.store.CommunicationV4EventActionByID(actionID)
	if err != nil {
		return communicationV4EventDependencyUnavailable, "", err
	}
	legacyAction, err := a.manager.store.CommunicationActionByID(actionID)
	if err != nil {
		return communicationV4EventDependencyUnavailable, "", err
	}
	if (eventAction == nil) == (legacyAction == nil) {
		if eventAction == nil {
			return communicationV4EventDependencyUnavailable, "", nil
		}
		return communicationV4EventDependencyUnavailable, "",
			store.ErrCommunicationV4EventActionConflict
	}
	if eventAction != nil {
		if eventAction.Status == store.CommunicationV4EventActionRetried {
			// 父项经历过干净失败自动重试(§8.4):正证事实在重试链最新一代
			// 尝试行上,取到后按同一套状态判据裁决。
			walked, walkErr := a.manager.store.CommunicationV4EventActionLatestAttempt(
				eventAction.ActionID,
			)
			if walkErr != nil {
				return communicationV4EventDependencyUnavailable, "", walkErr
			}
			if walked == nil ||
				walked.Status == store.CommunicationV4EventActionRetried {
				return communicationV4EventDependencyUnavailable, "", nil
			}
			eventAction = walked
		}
		switch eventAction.Status {
		case store.CommunicationV4EventActionSent:
			if eventAction.EffectIntentID == nil ||
				strings.TrimSpace(*eventAction.EffectIntentID) == "" {
				return communicationV4EventDependencyUnavailable, "", nil
			}
			return communicationV4EventDependencyReady, *eventAction.EffectIntentID, nil
		case store.CommunicationV4EventActionPlanned,
			store.CommunicationV4EventActionEffectPending:
			return communicationV4EventDependencyWaiting, "", nil
		case store.CommunicationV4EventActionManualRequired:
			if eventAction.EffectIntentID != nil {
				return communicationV4EventDependencyWaiting, "", nil
			}
			return communicationV4EventDependencyUnavailable, "", nil
		case store.CommunicationV4EventActionDeferred:
			return communicationV4EventDependencyUnavailable, "", nil
		default:
			return communicationV4EventDependencyUnavailable, "",
				store.ErrCommunicationV4EventActionConflict
		}
	}

	switch legacyAction.Status {
	case store.CommunicationActionSent:
		if legacyAction.EffectIntentID == nil ||
			strings.TrimSpace(*legacyAction.EffectIntentID) == "" {
			return communicationV4EventDependencyUnavailable, "", nil
		}
		return communicationV4EventDependencyReady, *legacyAction.EffectIntentID, nil
	case store.CommunicationActionPlanned,
		store.CommunicationActionEffectPending:
		return communicationV4EventDependencyWaiting, "", nil
	case store.CommunicationActionManualRequired:
		if legacyAction.EffectIntentID != nil {
			return communicationV4EventDependencyWaiting, "", nil
		}
		return communicationV4EventDependencyUnavailable, "", nil
	case store.CommunicationActionSuperseded:
		return communicationV4EventDependencyUnavailable, "", nil
	default:
		return communicationV4EventDependencyUnavailable, "",
			store.ErrCommunicationV4EventActionConflict
	}
}

func (a *roundActor) markCommunicationV4EventActionManual(
	action store.CommunicationV4EventAction,
	reason string,
) (bool, error) {
	err := a.manager.store.MarkCommunicationV4EventActionManualRequired(
		action.ActionID,
		reason,
		a.manager.now(),
	)
	return true, err
}
