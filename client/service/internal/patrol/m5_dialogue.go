package patrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

// m5RetriableAdviceFailures 是允许换一次采样重来的失败原因白名单(2026-08-01
// 甲方裁决:任何失败都重试,上限 5 次调用)。四项的共同点是"模型这一次没吐出
// 可用建议",与业务结论无关:provider 调用失败(含超时)、响应解析失败、输出
// 契约或业务前置不合法、reasoning 用量可疑、reducer 拒绝本次建议。
//
// 白名单之外一律不重试,因为它们不是"这次没吐好":
//   - intentRejected/unsupportedMedia/unsupportedSemantic/wechatContinuationManual
//     是模型判对了、业务规则要求转人工。重试等于反复摇色子直到摇出另一个
//     业务结论,会扭曲判断。
//   - fixedPhraseUnavailable 是话术配置缺失,inputBudgetBlocked 是输入超预算,
//     inputBoundaryChanged 是世界已经变了——都确定性复现,重试纯粹浪费。
//
// 重试只是"重新取建议",不放宽任何业务前置:每次 attempt 都用同一份冻结输入
// 重新走完整的 planV4ReplyActions 裁决,试到上限仍不合法就照旧转人工。
var m5RetriableAdviceFailures = map[string]struct{}{
	string(communication.ManualReplyFailed):  {},
	string(communication.ManualReplyInvalid): {},
	"reasoningUsageUnsafe":                   {},
	"reducerRejected":                        {},
}

// errM5AdviceRoundSkipped 是包内哨兵:2026-08-02 甲方裁决把"这次没吐好"的
// 失败从本轮内连打改成跨巡检轮重试,一次 attempt 失败后本轮必须放下该候选人。
// 用哨兵而不是裸 nil,是因为 advanceM5Turn 的推进循环会在 nil 返回后重读
// turn 状态再进一步——那会在同一轮里游走到下一个 attempt 再次调用 provider,
// 恰好绕过"每巡检轮每用途至多一次调用"的节流。哨兵只在 advanceM5Turn 出口
// 换成 nil,不出本包。
var errM5AdviceRoundSkipped = errors.New("m5 advice skipped for this patrol round")

// settleM5TurnBoundaryChanged 收敛"AI 边界重验/结果落账时发现输入边界已变"
// (2026-08-02 甲方裁决,规格 v4 §一"旧轮失效"):store 层多半已在同事务内把
// 旧轮作废,这里做一次幂等兜底,然后以跳过哨兵结束本轮——新消息属于下一轮,
// 下轮巡检按最新账本边界重开新轮重新裁决,候选人不冻结。带 effect 案底的轮
// (多气泡已发前缀后候选人插话)由 store 回落保守 manualRequired,不作废。
func (a *roundActor) settleM5TurnBoundaryChanged(turnID string) error {
	if err := a.manager.store.SupersedeDialogueTurnForBoundary(turnID, a.manager.now()); err != nil {
		return err
	}
	a.logM5TurnBoundarySettled(turnID)
	return errM5AdviceRoundSkipped
}

// logM5TurnBoundarySettled 按收敛后的真实状态记日志:同一个边界失配事件有
// 两种合法归宿,不能只按乐观分支写文案。
func (a *roundActor) logM5TurnBoundarySettled(turnID string) {
	turn, err := a.manager.store.DialogueTurnByID(turnID)
	if err == nil && turn != nil && turn.Status == store.DialogueTurnManualRequired {
		slog.Info("对话轮转人工:输入边界已变且旧轮带发送案底,交人工处置",
			"turnId", turnID)
		return
	}
	slog.Info("对话轮作废:输入边界已变,旧轮 superseded,下轮巡检按最新账本边界重开新轮",
		"turnId", turnID)
}

func m5AdviceShouldRetry(
	completion store.AIInvocationCompletion,
	decision communication.Decision,
	manualReason string,
	attempt int,
) bool {
	if attempt >= store.MaxAIInvocationAttempts {
		return false
	}
	if completion.Status == store.AIInvocationBudgetBlocked {
		// reply 路径上超预算会以 replyFailed 的面目混进白名单,这里统一挡住:
		// 同一份冻结输入重发多少次都是同样的字节数。
		return false
	}
	reason := manualReason
	if reason == "" {
		if decision.TurnStatus != communication.TurnManualRequired {
			return false
		}
		reason = string(decision.ManualReason)
	}
	_, ok := m5RetriableAdviceFailures[reason]
	return ok
}

const m5ResumeAttachmentHistoryText = "候选人已投递简历"

type m5TurnMaterial struct {
	profile      store.CandidateProfile
	revision     store.JobAIContextRevision
	snapshot     store.CandidateResumeSnapshot
	inputKind    store.DialogueTurnInputKind
	currentFacts []communication.LedgerMessageFact
	history      []m5ai.AdviceMessage
	current      []m5ai.AdviceMessage
	throughTurn  []m5ai.AdviceMessage
	sentGreeting string
}

// advanceM5Turn 是对话轮推进的唯一入口。它把 advanceM5TurnSteps 冒出的
// errM5AdviceRoundSkipped 换成 nil:跳过是本轮的正常结论,不是错误,不得
// 流进上层的账号级失败分流。
func (a *roundActor) advanceM5Turn(ctx context.Context, initial store.DialogueTurn) error {
	err := a.advanceM5TurnSteps(ctx, initial)
	if errors.Is(err, errM5AdviceRoundSkipped) {
		return nil
	}
	return err
}

func (a *roundActor) advanceM5TurnSteps(ctx context.Context, initial store.DialogueTurn) error {
	turn := initial
	for step := 0; step < 3; step++ {
		switch turn.Status {
		case store.DialogueTurnManualRequired, store.DialogueTurnSuperseded,
			store.DialogueTurnDispatching, store.DialogueTurnCompleted:
			return nil
		}
		if err := a.mayAdvanceM5Turn(ctx); err != nil {
			return err
		}
		nextV4Advice, v4Owned, err := a.manager.store.CommunicationV4NextAdvice(turn.TurnID)
		if err != nil {
			return err
		}
		if turn.Status == store.DialogueTurnAdviceReady {
			return a.dispatchM5Action(ctx, turn, false)
		}
		if turn.Status == store.DialogueTurnCollected &&
			v4Owned && nextV4Advice == communication.V4AdviceNone {
			// An event-driven branch is durably waiting for a prerequisite
			// action. Neither AI material nor provider configuration is part
			// of that deterministic continuation.
			slog.Info("对话轮跳过:事件分支在等前置动作正证,本轮不调 AI",
				"turnId", turn.TurnID)
			return nil
		}
		material, err := a.loadM5TurnMaterial(turn)
		if err != nil {
			// 材料装配失败多为读取瞬断或档案暂时失配(2026-08-02 裁决):不写
			// 任何状态,跳过本轮;真正的边界失效由既有 Recheck/预留校验收敛。
			slog.Warn("对话轮跳过:渲染材料装配失败,等下轮巡检重试",
				"profileId", turn.ProfileID, "turnId", turn.TurnID,
				"reason", "renderInputUnavailable", "err", err)
			return nil
		}
		facts := communication.FrozenTurnFacts{TurnID: turn.TurnID}
		for _, message := range material.current {
			facts.Messages = append(facts.Messages, communication.FrozenInboundMessage{
				Seq: message.Seq, Kind: communication.FrozenMessageKind(message.Kind), Text: message.Text,
			})
		}

		switch turn.Status {
		case store.DialogueTurnCollected:
			if material.inputKind == store.DialogueTurnInputResumeAttachment {
				decision, reduceErr := reduceM5ResumeTurn(turn, material, communication.ReplyAdvice{State: communication.AdviceAbsent})
				if reduceErr != nil || !m5ResumeReplyAdviceAuthorized(decision) {
					return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "reducerRejected", a.manager.now())
				}
				if _, err := a.manager.store.ApplyResumeBusinessClassification(turn.TurnID, a.manager.now()); err != nil {
					return err
				}
				break
			}
			decision, reduceErr := communication.Reduce(communication.ReduceInput{
				Turn: facts, Intent: communication.IntentAdvice{State: communication.AdviceAbsent},
				Reply: communication.ReplyAdvice{State: communication.AdviceAbsent},
			})
			if reduceErr != nil {
				return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "reducerRejected", a.manager.now())
			}
			if decision.NextAdvice != m5ai.PurposeIntent {
				// Q5 死代码删除(2026-08-02):此分支生产不可达。v4 冻结事务
				// (reduceV4ClassifiedDialogue)已用同一 Reduce 在同一文本集上
				// 消化过短路分类——凡短路命中,冻结时即落 Classified/终局,
				// 不会以 Collected 停留;Collected 轮存在本身就证明短路未命中,
				// 而 Recheck 的 digest 重验加单 actor 串行化保证推进时文本集
				// 与冻结时逐字相同,纯函数重算只会再次要求意向建议。原先的
				// ApplyCodeClassification 落账臂已随 trial 轨删除,这里只留
				// 保守停靠兜底。
				return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "reducerStateConflict", a.manager.now())
			}
			if a.manager.advice == nil {
				return nil
			}
			if err := a.runM5IntentAdvice(ctx, turn, material, facts); err != nil {
				return err
			}
		case store.DialogueTurnClassified:
			if v4Owned && nextV4Advice == communication.V4AdviceServiceReply {
				if a.manager.advice == nil {
					return nil
				}
				if err := a.runM5ReplyAdvice(
					ctx,
					turn,
					material,
					facts,
					communication.IntentAdvice{State: communication.AdviceAbsent},
					nextV4Advice,
				); err != nil {
					return err
				}
				break
			}
			if v4Owned && nextV4Advice == communication.V4AdviceReply &&
				material.inputKind == store.DialogueTurnInputWechatCard {
				// 批B换微信混合/承接轮:对话要求已由 v4 回执链裁决(请求卡轮
				// 只有接受链完成的接续回执才推进到 reply),settle 侧还会以
				// 聚合态全量重放校验 requirement;此处只核对轮标签形状,不用
				// 招呼态重放(请求卡轮会被误判为仍在等待前置)。
				if turn.IntentLabel != m5ai.IntentInterested || turn.IntentSource != store.DialogueIntentBusinessEvent {
					return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "reducerStateConflict", a.manager.now())
				}
				if a.manager.advice == nil {
					return nil
				}
				if err := a.runM5ReplyAdvice(
					ctx,
					turn,
					material,
					facts,
					communication.IntentAdvice{State: communication.AdviceAbsent},
					communication.V4AdviceReply,
				); err != nil {
					return err
				}
				break
			}
			if material.inputKind == store.DialogueTurnInputResumeAttachment {
				if turn.IntentLabel != m5ai.IntentInterested || turn.IntentSource != store.DialogueIntentBusinessEvent {
					return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "reducerStateConflict", a.manager.now())
				}
				decision, reduceErr := reduceM5ResumeTurn(turn, material, communication.ReplyAdvice{State: communication.AdviceAbsent})
				if reduceErr != nil || !m5ResumeReplyAdviceAuthorized(decision) {
					return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "reducerStateConflict", a.manager.now())
				}
				if a.manager.advice == nil {
					return nil
				}
				if err := a.runM5ReplyAdvice(
					ctx,
					turn,
					material,
					facts,
					communication.IntentAdvice{State: communication.AdviceAbsent},
					communication.V4AdviceReply,
				); err != nil {
					return err
				}
				break
			}
			intent := intentAdviceFromTurn(turn)
			decision, reduceErr := communication.Reduce(communication.ReduceInput{
				Turn: facts, Intent: intent,
				Reply: communication.ReplyAdvice{State: communication.AdviceAbsent},
			})
			if reduceErr != nil || decision.NextAdvice != m5ai.PurposeReply {
				return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "reducerStateConflict", a.manager.now())
			}
			if a.manager.advice == nil {
				return nil
			}
			if err := a.runM5ReplyAdvice(
				ctx,
				turn,
				material,
				facts,
				intent,
				communication.V4AdviceReply,
			); err != nil {
				return err
			}
		default:
			return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "turnStateUnknown", a.manager.now())
		}
		reloaded, err := a.manager.store.DialogueTurnByID(turn.TurnID)
		if err != nil || reloaded == nil {
			return err
		}
		turn = *reloaded
	}
	return nil
}

// mayAdvanceM5Turn calls the literal workflow member gate also used by the
// scoring, greeting-generation and greeting-send loops. It releases the actor
// lock before entering that gate to preserve the control lock order
// (workflow -> actor), then rechecks the actor generation after reacquiring it.
// Consequently a pause which linearizes while one provider call is in flight
// lets that call persist its result, but cannot authorize the next advice
// stage or a new pre-WAL action.
func (a *roundActor) mayAdvanceM5Turn(ctx context.Context) error {
	a.manager.gateMu.RLock()
	installed := a.manager.workflowMemberGate != nil
	a.manager.gateMu.RUnlock()
	if !installed {
		return nil
	}
	var gateErr error
	func() {
		a.manager.mu.Unlock()
		defer a.manager.mu.Lock()
		gateErr = a.manager.mayStartNextWorkflowMember()
	}()
	if gateErr != nil {
		return gateErr
	}
	return a.ensureDispatchAllowed(ctx)
}

func reduceM5ResumeTurn(
	turn store.DialogueTurn,
	material m5TurnMaterial,
	reply communication.ReplyAdvice,
) (communication.V4InboundTurnDecision, error) {
	if material.inputKind != store.DialogueTurnInputResumeAttachment || len(material.currentFacts) == 0 {
		return communication.V4InboundTurnDecision{}, communication.ErrInvalidV4StateTransition
	}
	return communication.ReduceV4InboundTurn(communication.V4InboundTurnInput{
		State:  communication.NewV4GreetedState(material.profile.GreetedAt),
		TurnID: turn.TurnID, Messages: material.currentFacts,
		Intent: communication.IntentAdvice{State: communication.AdviceAbsent}, Reply: reply,
	})
}

func m5ResumeReplyAdviceAuthorized(decision communication.V4InboundTurnDecision) bool {
	return decision.ManualReason == "" && len(decision.EventActions) == 0 &&
		decision.Dialogue.Status == communication.V4DialogueWaitingAdvice &&
		decision.Dialogue.NextAdvice == communication.V4AdviceReply &&
		decision.Dialogue.IntentLabel == m5ai.IntentInterested &&
		decision.Dialogue.IntentSource == communication.IntentSourceBusinessEvent &&
		len(decision.Dialogue.Actions) == 0
}

// dispatchM5Action 派发 turn 当前唯一的 planned 动作。withinChain 表示本次
// 调用是同一 turn 内紧接前项正证的链内推进：按《24点边界裁决-2026-07-28》
// 链内只做 ensureChainDispatchAllowed 复核（不查日窗口/日界），链首与次日
// 恢复轨调用传 false，仍走 workflow 成员闸与完整日界复核。
func (a *roundActor) dispatchM5Action(
	ctx context.Context,
	turn store.DialogueTurn,
	withinChain bool,
) error {
	action, err := a.manager.store.PlannedCommunicationActionByTurn(turn.TurnID)
	if err != nil {
		return err
	}
	if action == nil ||
		action.Status != store.CommunicationActionPlanned ||
		action.EffectIntentID != nil {
		return a.manager.store.MarkDialogueTurnManualRequired(
			turn.TurnID, "automaticActionUnavailable", a.manager.now(),
		)
	}
	// Q1/Q2 裁决(2026-08-02):链首/恢复遭遇的陈旧 planned 残留一律作废并
	// 收束轮终局(已发前缀照常保留),不再续发;次日按最新账本边界重新规划。
	// 链内推进(withinChain)是《24点边界裁决》批准跨过 24:00 收束的同进程
	// 自然延续,不适用陈旧判定。绑过发送意图的行由上方守卫与 store 侧 WHERE
	// 双重排除,永不作废。
	if !withinChain &&
		action.EffectStartedAt == nil &&
		action.SentAt == nil &&
		a.manager.plannedActionStale(action.CreatedAt) {
		result, err := a.manager.store.SupersedeStaleDialoguePlannedAction(
			turn.TurnID,
			action.ActionID,
			a.manager.now(),
		)
		if err != nil {
			return err
		}
		slog.Info("陈旧未派发残留作废:对话轮 planned 动作跨日/跨启动未发,轮已收束",
			"turnId", turn.TurnID,
			"actionId", action.ActionID,
			"createdAt", action.CreatedAt,
			"turnStatus", string(result.TurnStatus))
		return nil
	}
	switch action.Kind {
	case store.CommunicationActionReplyText:
		if _, ok := a.manager.runner.(AutomaticReplyRunner); !ok {
			// Pure reducer/store tests may intentionally stop at the
			// persisted action seam.
			return nil
		}
	case store.CommunicationActionInviteWechat, store.CommunicationActionInterviewInvite:
		if _, ok := a.manager.runner.(AutomaticCardRunner); !ok {
			return nil
		}
	default:
		return a.manager.store.MarkM5AutomaticActionManualRequired(
			action.ActionID,
			"automaticActionUnsupported",
			a.manager.now(),
		)
	}
	profile, err := a.manager.store.CandidateProfileByID(turn.ProfileID)
	if err != nil || profile == nil || profile.ConversationRef == nil ||
		*profile.ConversationRef != turn.ConversationRef {
		return a.manager.store.MarkM5AutomaticActionManualRequired(
			action.ActionID, "automaticBindingUnavailable", a.manager.now(),
		)
	}
	intentID, err := store.M5AutomaticIntentID(action.ActionID)
	if err != nil {
		return a.manager.store.MarkM5AutomaticActionManualRequired(
			action.ActionID, "automaticIntentInvalid", a.manager.now(),
		)
	}
	previousIntentID := ""
	if action.DependsOnActionID != nil {
		parent, parentErr := a.manager.store.CommunicationActionByID(
			*action.DependsOnActionID,
		)
		if parentErr != nil {
			return parentErr
		}
		if parent == nil ||
			parent.EffectIntentID == nil ||
			parent.Status != store.CommunicationActionSent {
			return a.manager.store.MarkM5AutomaticActionManualRequired(
				action.ActionID,
				"automaticDependencyUnavailable",
				a.manager.now(),
			)
		}
		previousIntentID = *parent.EffectIntentID
		if store.IsRetryCommunicationActionID(action.ActionID) {
			// 邀面卡自动重试(协议规格 §8.4 例外):前次失败尝试是会话最新
			// intent,WAL CAS 锚取最新;依赖校验仍以父正证 intent 为语义锚,
			// 并按透明锚四条件核验该最新 intent 确为前次零副作用失败尝试。
			latest, latestErr := a.manager.store.LatestEffectIntent(
				profile.Platform,
				profile.AccountRef,
				turn.ConversationRef,
			)
			if latestErr != nil {
				return latestErr
			}
			if latest != nil {
				previousIntentID = latest.IntentID
			}
		}
	} else {
		latest, latestErr := a.manager.store.LatestEffectIntent(
			profile.Platform,
			profile.AccountRef,
			turn.ConversationRef,
		)
		if latestErr != nil {
			return latestErr
		}
		if latest != nil {
			previousIntentID = latest.IntentID
		}
	}
	// The visible send interaction shares the same brain-owned pacing and
	// post-wait authorization recheck as the sourcing workflow. The hand still
	// receives one command and owns no business timer. Within a chain the
	// post-wait recheck drops only the daily-boundary clauses.
	paceWait := a.waitSourcingDelay
	if withinChain {
		paceWait = a.waitChainDelay
	}
	if err := paceWait(ctx, a.manager.config.InteractionPaceWait); err != nil {
		if preservesM5PlannedAction(err) {
			return err
		}
		if closeErr := a.manager.store.MarkM5AutomaticActionManualRequired(
			action.ActionID, "automaticDispatchNotAllowed", a.manager.now(),
		); closeErr != nil {
			return errors.Join(err, closeErr)
		}
		return err
	}
	var handle interface {
		Wait(context.Context) error
	}
	switch action.Kind {
	case store.CommunicationActionReplyText:
		handle, err = a.manager.runner.(AutomaticReplyRunner).StartAutomaticReply(
			ctx,
			AutomaticReplyRequest{
				ActionID: action.ActionID, IntentID: intentID, PreviousIntentID: previousIntentID,
				ExpectedSession: a.hand.Session, ExpectedBootID: a.hand.BootID,
				Platform: profile.Platform, AccountRef: profile.AccountRef,
				ConversationRef: turn.ConversationRef, Text: action.Text,
			},
		)
	case store.CommunicationActionInviteWechat:
		handle, err = a.manager.runner.(AutomaticCardRunner).StartAutomaticCard(
			ctx,
			AutomaticCardRequest{
				ActionID: action.ActionID, IntentID: intentID, PreviousIntentID: previousIntentID,
				ExpectedSession: a.hand.Session, ExpectedBootID: a.hand.BootID,
				Platform: profile.Platform, AccountRef: profile.AccountRef,
				ConversationRef: turn.ConversationRef, Kind: action.Kind,
			},
		)
	case store.CommunicationActionInterviewInvite:
		if !communication.ValidV4PlannedInterview(
			action.InterviewStartsAtMs, action.InterviewEndsAtMs, action.InterviewMethod,
		) {
			return a.manager.store.MarkM5AutomaticActionManualRequired(
				action.ActionID,
				"automaticActionInvalid",
				a.manager.now(),
			)
		}
		// 现场面试没有 endsAt：契约里它是 omitempty，留 0 即缺席，不得合成。
		interview := &protocol.InterviewDetails{
			StartsAt: *action.InterviewStartsAtMs,
			Method:   protocol.InterviewMethod(*action.InterviewMethod),
		}
		if action.InterviewEndsAtMs != nil {
			interview.EndsAt = *action.InterviewEndsAtMs
		}
		handle, err = a.manager.runner.(AutomaticCardRunner).StartAutomaticCard(
			ctx,
			AutomaticCardRequest{
				ActionID: action.ActionID, IntentID: intentID, PreviousIntentID: previousIntentID,
				ExpectedSession: a.hand.Session, ExpectedBootID: a.hand.BootID,
				Platform: profile.Platform, AccountRef: profile.AccountRef,
				ConversationRef: turn.ConversationRef, Kind: action.Kind, Interview: interview,
			},
		)
	}
	if err != nil || handle == nil {
		if closeErr := a.manager.store.MarkM5AutomaticActionManualRequired(
			action.ActionID, "automaticDispatchNotConstructed", a.manager.now(),
		); closeErr != nil {
			return errors.Join(err, closeErr)
		}
		return nil
	}
	func() {
		a.manager.mu.Unlock()
		defer a.manager.mu.Lock()
		err = handle.Wait(ctx)
	}()
	if err != nil {
		// A constructed effect is exclusively owned by the persistent recovery
		// rail. Never turn a wait interruption into a second business intent.
		return err
	}
	settled, err := a.manager.store.CommunicationActionByID(action.ActionID)
	if err != nil {
		return err
	}
	if settled == nil || (settled.Status != store.CommunicationActionSent &&
		settled.Status != store.CommunicationActionManualRequired &&
		// 干净失败已在结算事务内标 retried 并铸出 |try{n} 新动作(§8.4);
		// 本轮不再推进,新尝试留给下一轮巡检,自然满足每轮至多一次。
		settled.Status != store.CommunicationActionRetried) {
		return store.ErrCommunicationActionConflict
	}
	if settled.Status == store.CommunicationActionSent {
		next, nextErr := a.manager.store.PlannedCommunicationActionByTurn(turn.TurnID)
		if nextErr != nil {
			return nextErr
		}
		if next != nil {
			// The positive parent result has atomically materialized the only
			// dependent child. Keep the current conversation surface and reuse
			// the exact same dispatch path instead of yielding to another
			// page-driven patrol, which may switch the IM route first.
			//
			// This is still a new candidate-visible action with its own
			// action, WAL/idemKey, witness and positive-evidence boundary.
			// Per《24点边界裁决-2026-07-28》the chain interior does not
			// re-enter the workflow member gate: its daily-window clause is
			// exactly what a started chain is allowed to cross, and a user
			// pause closes the account projection which the chain recheck
			// still honors. Cancel and hand handover cut the chain too.
			if err := a.ensureChainDispatchAllowed(ctx); err != nil {
				return err
			}
			return a.dispatchM5Action(ctx, turn, true)
		}
	}
	return nil
}

// waitChainDelay keeps the brain-owned interaction pacing for a chain-interior
// action but rechecks with ensureChainDispatchAllowed afterwards, so a chain
// finishing across midnight is not cut by the daily-boundary clauses while a
// pause or handover arriving during the wait still cuts it.
func (a *roundActor) waitChainDelay(ctx context.Context, wait func(context.Context) error) error {
	var waitErr error
	func() {
		a.manager.mu.Unlock()
		defer a.manager.mu.Lock()
		waitErr = wait(ctx)
	}()
	if waitErr != nil {
		return waitErr
	}
	return a.ensureChainDispatchAllowed(ctx)
}

// A pause, daily boundary or process shutdown before WAL construction removes
// only the current dispatch authorization. The durable planned action remains
// the resume point; converting it to manualRequired would make an ordinary
// pause irreversibly consume otherwise valid work.
func preservesM5PlannedAction(err error) bool {
	return errors.Is(err, ErrActorPaused) ||
		errors.Is(err, ErrDailyWindowExpired) ||
		errors.Is(err, ErrRoundSupersededBySourcingBatch) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

func intentAdviceFromTurn(turn store.DialogueTurn) communication.IntentAdvice {
	switch turn.IntentSource {
	case store.DialogueIntentLLM:
		return communication.IntentAdvice{
			State: communication.AdviceOK, Suggestion: m5ai.IntentSuggestion{Label: turn.IntentLabel},
		}
	case store.DialogueIntentLLMFailure:
		return communication.IntentAdvice{State: communication.AdviceFailed}
	case store.DialogueIntentCodeShortCircuit:
		return communication.IntentAdvice{State: communication.AdviceAbsent}
	case store.DialogueIntentBusinessEvent:
		return communication.IntentAdvice{State: communication.AdviceAbsent}
	default:
		return communication.IntentAdvice{State: communication.AdviceState("invalid")}
	}
}

func (a *roundActor) loadM5TurnMaterial(turn store.DialogueTurn) (m5TurnMaterial, error) {
	profile, err := a.manager.store.CandidateProfileByID(turn.ProfileID)
	if err != nil || profile == nil || profile.ConversationRef == nil || *profile.ConversationRef != turn.ConversationRef ||
		profile.ActiveResumeSnapshotID == nil || *profile.ActiveResumeSnapshotID != turn.ResumeSnapshotID {
		return m5TurnMaterial{}, store.ErrDialogueTurnBinding
	}
	revision, err := a.manager.store.JobAIContextRevisionByHash(turn.ContextRevisionHash)
	if err != nil || revision == nil ||
		profile.BackendJobID == nil ||
		strings.TrimSpace(*profile.BackendJobID) == "" ||
		revision.SourceJobRef != strings.TrimSpace(*profile.BackendJobID) {
		return m5TurnMaterial{}, store.ErrDialogueTurnBinding
	}
	snapshot, err := a.manager.store.CandidateResumeSnapshotByID(turn.ProfileID, turn.ResumeSnapshotID)
	if err != nil || snapshot == nil {
		return m5TurnMaterial{}, store.ErrDialogueTurnBinding
	}
	key := store.ConversationKey{Platform: profile.Platform, AccountRef: profile.AccountRef, ConversationRef: turn.ConversationRef}
	messages, err := a.manager.store.MessagesForConversation(key)
	if err != nil || len(messages) == 0 ||
		messages[len(messages)-1].Seq < turn.InboundThroughSeq {
		return m5TurnMaterial{}, store.ErrDialogueTurnBinding
	}
	// 轮区间之后允许平台中性 system 行滞留（0727当日计划3）；出现新的
	// 候选人消息或我方出站仍视为边界失效。唯一豁免:交换结果卡(259/出站)
	// 是本轮接受动作的平台产物,不是我方新发言——形态 A 当轮定向重对账会
	// 在承接 advice 前把它收进账本,不豁免则"接受链完成→承接"永不可达
	// (与 reconstructCommunicationV4TurnBoundaryTx 的 lateOutbound 豁免同规则)。
	for index := range messages {
		message := messages[index]
		if message.Seq <= turn.InboundThroughSeq {
			continue
		}
		if message.Direction == "system" ||
			(message.Direction == "in" && message.Kind == "system") {
			continue
		}
		if message.Direction == "out" && message.Kind == "card" &&
			message.CardType == "wechatExchange" && message.CardState == "accepted" {
			continue
		}
		return m5TurnMaterial{}, store.ErrDialogueTurnBinding
	}
	material := m5TurnMaterial{profile: *profile, revision: *revision, snapshot: *snapshot}
	var currentBoundary []store.Message
	for _, message := range messages {
		if message.Seq > turn.InboundThroughSeq {
			// 轮后唯一被容忍的非 system 行是接受动作产生的交换结果卡(见上
			// 方边界豁免);它是承接的前情事实,必须进入对话历史渲染,否则
			// 模型不知道微信已交换、会再建议换微信动作而撞前置裁决转人工。
			if !(message.Direction == "out" && message.Kind == "card" &&
				message.CardType == "wechatExchange" && message.CardState == "accepted") {
				continue
			}
		}
		if message.Seq >= turn.InboundFromSeq && message.Seq <= turn.InboundThroughSeq {
			currentBoundary = append(currentBoundary, message)
		}
		if message.Direction == "system" || (message.Direction == "in" && message.Kind == "system") {
			continue
		}
		text := ""
		if message.Direction == "in" && message.Kind == "card" && message.CardType == "resumeAttachment" {
			text = m5ResumeAttachmentHistoryText
		} else if message.Text != nil {
			text = strings.TrimSpace(*message.Text)
		}
		if text == "" {
			continue
		}
		direction := "inbound"
		if message.Direction == "out" {
			direction = "outbound"
		}
		kind := message.Kind
		if direction == "outbound" && message.OutboundIntentID != nil &&
			profile.SuccessfulGreetingIntentID != nil && *message.OutboundIntentID == *profile.SuccessfulGreetingIntentID {
			kind = "greeting"
			material.sentGreeting = *message.Text
		}
		advice := m5ai.AdviceMessage{Seq: message.Seq, Direction: direction, Kind: kind, Text: text}
		material.throughTurn = append(material.throughTurn, advice)
		if message.Seq <= turn.HistoryThroughSeq {
			material.history = append(material.history, advice)
		}
		if message.Seq >= turn.InboundFromSeq && message.Seq <= turn.InboundThroughSeq {
			material.current = append(material.current, advice)
		}
	}
	// 不以 HistoryThroughSeq==0 判来聊根：解耦后来聊根首轮在前置 system
	// 已投影时渲染边界大于零。凡找不到已发招呼，一律经聚合根裁决。
	if material.sentGreeting == "" {
		aggregate, aggregateErr := a.manager.store.CommunicationV4AggregateByProfile(
			turn.ProfileID,
		)
		if aggregateErr != nil || aggregate == nil ||
			!store.IsInboundConversationV4Root(aggregate.RootGreetingIntentID) {
			return m5TurnMaterial{}, store.ErrDialogueTurnBinding
		}
		material.sentGreeting = m5ai.InboundConversationNoGreetingText
	}
	currentMessages, validBoundary := store.DialogueTurnCandidateMessages(currentBoundary)
	if !validBoundary {
		return m5TurnMaterial{}, store.ErrDialogueTurnBinding
	}
	for index := range currentMessages {
		message := currentMessages[index]
		material.currentFacts = append(material.currentFacts, communication.LedgerMessageFact{
			Seq: message.Seq, Direction: message.Direction, Kind: message.Kind, Text: message.Text,
			CardType: message.CardType, CardState: message.CardState, Origin: message.Origin,
			InterviewMethod: message.InterviewMethod,
			TsApproxMs:      message.TsApproxMs,
		})
	}
	inputKind, ok := store.DialogueTurnInputKindOf(currentMessages)
	if !ok {
		return m5TurnMaterial{}, store.ErrDialogueTurnBinding
	}
	material.inputKind = inputKind
	if material.sentGreeting == "" || len(material.current) == 0 || len(material.currentFacts) == 0 {
		return m5TurnMaterial{}, store.ErrDialogueTurnBinding
	}
	return material, nil
}

func (a *roundActor) runM5IntentAdvice(
	ctx context.Context,
	turn store.DialogueTurn,
	material m5TurnMaterial,
	facts communication.FrozenTurnFacts,
) error {
	content, _, err := m5ai.RenderIntentPrompt(
		material.revision.Communication.IntentPrompt, material.sentGreeting,
		material.history, material.current,
	)
	if err != nil {
		// AI 输入渲染失败是"世界干净"的纯计算失败(2026-08-02 甲方裁决):
		// 发生在预留任何调用之前,连 turn 停靠都不需要——不写终局、不冻结,
		// 跳过本轮等模板或快照修复后自然重试。本函数下同。
		slog.Warn("对话轮跳过:意向提示词渲染失败,等下轮巡检重试",
			"turnId", turn.TurnID, "reason", "intentRenderFailed")
		return errM5AdviceRoundSkipped
	}
	return a.executeM5Advice(ctx, turn, material, facts, m5ai.PurposeIntent, content, communication.IntentAdvice{})
}

func (a *roundActor) runM5ReplyAdvice(
	ctx context.Context,
	turn store.DialogueTurn,
	material m5TurnMaterial,
	facts communication.FrozenTurnFacts,
	intent communication.IntentAdvice,
	v4Purpose communication.V4AdvicePurpose,
) error {
	if v4Purpose == communication.V4AdviceServiceReply {
		return a.runM5ServiceReplyAdvice(ctx, turn, material, facts)
	}
	// 回复输入的渲染失败同 runM5IntentAdvice:纯计算失败,发生在预留任何
	// 调用之前,不写终局、不冻结,跳过本轮等下轮重试(2026-08-02 甲方裁决)。
	resumeJSON, err := m5ai.RenderResumeJSON(material.snapshot.ResumeJSON)
	if err != nil {
		slog.Warn("对话轮跳过:简历渲染失败,等下轮巡检重试",
			"turnId", turn.TurnID, "reason", "resumeRenderFailed")
		return errM5AdviceRoundSkipped
	}
	history, err := m5ai.RenderHistory(material.throughTurn)
	if err != nil {
		slog.Warn("对话轮跳过:对话历史渲染失败,等下轮巡检重试",
			"turnId", turn.TurnID, "reason", "historyRenderFailed")
		return errM5AdviceRoundSkipped
	}
	content, err := m5ai.RenderReplyPromptFrozen(
		material.revision.Communication.ReplyPrompt, resumeJSON, history,
		turn.RecommendedTimeText, material.revision.Communication.CustomerFacts,
	)
	if err != nil {
		slog.Warn("对话轮跳过:回复提示词渲染失败,等下轮巡检重试",
			"turnId", turn.TurnID, "reason", "replyRenderFailed")
		return errM5AdviceRoundSkipped
	}
	if v4Purpose != communication.V4AdviceReply {
		// 编排断言:除函数开头分流的 ServiceReply 外只应收到 Reply。原因串
		// 与上一分支同为 replyRenderFailed,处置也保持同款,免得同因不同罚。
		slog.Warn("对话轮跳过:回复建议用途非法,等下轮巡检重试",
			"turnId", turn.TurnID, "reason", "replyRenderFailed",
			"v4Purpose", string(v4Purpose))
		return errM5AdviceRoundSkipped
	}
	content = a.appendM5ReplyActionMenu(turn, facts, content)
	return a.executeM5Advice(ctx, turn, material, facts, m5ai.PurposeReply, content, intent)
}

// runM5ServiceReplyAdvice runs the post-interview suffix (spec v4 §7,
// 2026-07-31): a fixed in-code prompt over the candidate's texts of this turn
// plus the fixed segment already sent — no playbook, resume or slots. Render
// failures skip this patrol round without touching the turn (2026-08-02).
func (a *roundActor) runM5ServiceReplyAdvice(
	ctx context.Context,
	turn store.DialogueTurn,
	material m5TurnMaterial,
	facts communication.FrozenTurnFacts,
) error {
	actions, err := a.manager.store.CommunicationV4EventActionsBySource(
		turn.ProfileID,
		store.CommunicationV4InputDialogueTurn,
		turn.TurnID,
	)
	if err != nil {
		return err
	}
	fixedBubbles := make([]string, 0, len(actions))
	wechatInviteSent := false
	for index := range actions {
		if actions[index].Status != store.CommunicationV4EventActionSent {
			continue
		}
		switch actions[index].V4Kind {
		case communication.V4ActionInviteWechat:
			wechatInviteSent = true
		case communication.V4ActionWechatReceipt, communication.V4ActionInterviewAcceptedReceipt:
			fixedBubbles = append(fixedBubbles, actions[index].Text)
		}
	}
	candidateTexts := make([]string, 0, len(material.current))
	for _, message := range material.current {
		if message.Direction == "inbound" && !message.Retracted {
			candidateTexts = append(candidateTexts, message.Text)
		}
	}
	content, err := m5ai.RenderServiceReplyPrompt(
		fixedBubbles,
		wechatInviteSent,
		candidateTexts,
	)
	if err != nil {
		// 输入构造失败是编排级异常,不属于 §七 的"调用失败/输出不合法";
		// 沿用普通轨渲染失败纪律:跳过本轮、不写终局、不冻结(2026-08-02
		// 甲方裁决)。
		slog.Warn("服务补句跳过:提示词渲染失败,等下轮巡检重试",
			"turnId", turn.TurnID, "reason", "serviceReplyRenderFailed")
		return errM5AdviceRoundSkipped
	}
	return a.executeM5ServiceAdvice(ctx, turn, material, facts, content)
}

// executeM5ServiceAdvice mirrors executeM5Advice for the service suffix: the
// ledger identity stays PurposeReply (one advice per turn, same invocation and
// advice keys), while the provider request and trace carry PurposeServiceReply.
// Call failure, parse failure and unsafe reasoning all abandon the suffix on a
// normal close (ServiceNoAction); only an interrupted prior invocation falls
// back to the ordinary recovery path, which stays conservative.
// executeM5ServiceAdvice 与 executeM5Advice 同样把补句获取跑到终局。差别在
// 失败的归宿:补句失败按规格 v4 §7 放弃补句、本轮正常终局,不转人工。
func (a *roundActor) executeM5ServiceAdvice(
	ctx context.Context,
	turn store.DialogueTurn,
	material m5TurnMaterial,
	facts communication.FrozenTurnFacts,
	content string,
) error {
	inputHash := sha256Hex(content)
	for attempt := 1; attempt >= 1 && attempt <= store.MaxAIInvocationAttempts; {
		next, err := a.executeM5ServiceAdviceAttempt(ctx, turn, material, facts, content, inputHash, attempt)
		if err != nil || next <= 0 {
			return err
		}
		attempt = next
	}
	return nil
}

func (a *roundActor) executeM5ServiceAdviceAttempt(
	ctx context.Context,
	turn store.DialogueTurn,
	material m5TurnMaterial,
	facts communication.FrozenTurnFacts,
	content string,
	inputHash string,
	attempt int,
) (int, error) {
	invocationID := stableM5ID("invocation", turn.TurnID, string(m5ai.PurposeReply), strconv.Itoa(attempt))
	reserved, err := a.manager.store.ReserveAIInvocation(store.ReserveAIInvocationRequest{
		InvocationID: invocationID, TurnID: turn.TurnID, Purpose: m5ai.PurposeReply,
		Attempt:  attempt,
		Provider: a.manager.advice.ProviderName(), Model: a.manager.advice.ModelName(),
		InputHash: inputHash, CreatedAt: a.manager.now(),
	})
	if err != nil {
		if errors.Is(err, store.ErrDialogueTurnBinding) {
			return 0, a.settleM5TurnBoundaryChanged(turn.TurnID)
		}
		return 0, err
	}
	if !reserved.Created {
		// 已完成但 turn 没推进,只可能是重试链在中途崩溃(FailAIInvocationForRetry
		// 刻意只落 invocation),接着往下一个 attempt 数即可。预留未完成的遗留
		// 归脑启动时的 RecoverInterruptedAIInvocations 收敛,这里维持原样。
		if reserved.Invocation.FinishedAt != nil && attempt < store.MaxAIInvocationAttempts {
			return attempt + 1, nil
		}
		return 0, a.finishInterruptedM5Advice(turn, material, facts, m5ai.PurposeReply,
			communication.IntentAdvice{State: communication.AdviceAbsent}, reserved.Invocation)
	}

	request := m5ai.CompletionRequest{
		InvocationID:        reserved.Invocation.InvocationID,
		Purpose:             m5ai.PurposeServiceReply,
		ContextRevisionHash: turn.ContextRevisionHash,
		PromptRevision:      turn.RenderFormatVersion,
		UserContent:         content,
		MaxOutputTokens:     m5ai.ServiceReplyOutputTokenLimit,
	}
	started := time.Now()
	var response m5ai.CompletionResponse
	var callErr error
	func() {
		a.manager.mu.Unlock()
		defer a.manager.mu.Lock()
		response, callErr = a.manager.advice.CompleteJSON(ctx, request)
	}()
	completion := m5CompletionFromProvider(reserved.Invocation.InvocationID, response, callErr, time.Since(started), a.manager.now())

	reply := ""
	sendable := false
	// failed 与 !sendable 不是一回事:解析成功但回复为空是规格 v4 §7 的"显式
	// 静默"(候选人只说了"嗯嗯""到时见"这类纯确认),那是本次调用的合法结论,
	// 不得当失败重试——重试只会把该闭嘴的一轮试出一句话来。
	failed := true
	if callErr == nil {
		suggestion, parseErr := m5ai.ParseServiceReplySuggestion(response.JSONText)
		if parseErr == nil {
			reply = suggestion.Reply
			sendable = reply != ""
			failed = false
		} else {
			markBusinessParseFailure(&completion, parseErr)
		}
	}
	if callErr == nil && !reasoningUsageSafe(completion) {
		markReasoningUsageUnsafe(&completion)
		sendable = false
		failed = true
	}
	logAIInvocationOutcome(
		a.manager.advice, m5ai.PurposeServiceReply, completion, response.Diagnostics.TraceErrorCode,
	)
	if failed && completion.Status != store.AIInvocationBudgetBlocked &&
		attempt < store.MaxAIInvocationAttempts {
		if failErr := a.manager.store.FailAIInvocationForRetry(completion, m5ai.PurposeReply); failErr != nil {
			return 0, failErr
		}
		// 2026-08-02 裁决:失败不在本轮内连打。留下本次失败事实后跳过该
		// 候选人,下一巡检轮经 attempt 游走走到下一个 attempt 再调用。
		slog.Warn("服务补句调用失败,本轮跳过等下轮重试",
			"turnId", turn.TurnID, "purpose", string(m5ai.PurposeServiceReply),
			"attempt", attempt, "status", string(completion.Status),
			"errorClass", completion.ErrorClass)
		return 0, errM5AdviceRoundSkipped
	}
	request2 := store.CompleteReplyInvocationRequest{
		Completion: completion, PlannedAt: a.manager.now(),
	}
	if sendable {
		request2.ActionID = stableM5ID("action", turn.TurnID, string(communication.CommunicationActionReplyText))
		request2.Phrases = []string{reply}
		request2.Text = reply
		request2.ContentHash = syncledger.HashText(reply)
	} else {
		request2.ServiceNoAction = true
	}
	_, err = a.manager.store.CompleteReplyInvocation(request2)
	if errors.Is(err, store.ErrDialogueTurnBinding) {
		return 0, a.settleM5TurnBoundaryChanged(turn.TurnID)
	}
	var resample *store.AIAdviceResampleScheduledError
	if errors.As(err, &resample) {
		// 服务补句的形状(单文本、无动作)按 reduceV4ServiceReply 语义不会
		// 产出可重采停靠,此分支理论不可达;拦住是防结算面漂移时把信号误当
		// 落账失败上抛。
		slog.Warn("服务补句结算判非法,本样本作废等下轮重采",
			"turnId", turn.TurnID, "reason", resample.Reason, "attempt", resample.Attempt)
		return 0, errM5AdviceRoundSkipped
	}
	if err != nil {
		logAIInvocationPersistenceFailure(a.manager.advice, m5ai.PurposeServiceReply, completion)
	}
	return 0, err
}

// executeM5Advice 推进一个 turn 的建议获取。循环体只对已完成的历史 attempt
// 做无调用的账面游走;真实 provider 调用每巡检轮每用途至多一次——失败留下
// invocation 事实后以 errM5AdviceRoundSkipped 让出本轮,下一巡检轮从游走处
// 接着走下一个 attempt(2026-08-02 裁决,总额仍是 store.MaxAIInvocationAttempts
// 次调用)。turn 的终局只由最后一次 attempt 写入。
func (a *roundActor) executeM5Advice(
	ctx context.Context,
	turn store.DialogueTurn,
	material m5TurnMaterial,
	facts communication.FrozenTurnFacts,
	purpose m5ai.CompletionPurpose,
	content string,
	intent communication.IntentAdvice,
) error {
	inputHash := sha256Hex(content)
	for attempt := 1; attempt >= 1 && attempt <= store.MaxAIInvocationAttempts; {
		next, err := a.executeM5AdviceAttempt(
			ctx, turn, material, facts, purpose, content, intent, inputHash, attempt,
		)
		if err != nil || next <= 0 {
			return err
		}
		attempt = next
	}
	return nil
}

// executeM5AdviceAttempt 至多跑一次 provider 调用。返回 next>0 表示该 attempt
// 已有完成事实、应继续游走到下一号(不发生调用);next=0 且 err 为 nil 表示
// 本 turn 已终局;err 为 errM5AdviceRoundSkipped 表示本次调用失败已入账、本轮
// 到此为止。
func (a *roundActor) executeM5AdviceAttempt(
	ctx context.Context,
	turn store.DialogueTurn,
	material m5TurnMaterial,
	facts communication.FrozenTurnFacts,
	purpose m5ai.CompletionPurpose,
	content string,
	intent communication.IntentAdvice,
	inputHash string,
	attempt int,
) (int, error) {
	// legacy 预算恢复是那次事故的一次性授权,自带"attempt=2 失败即永久停止"
	// 的边界(见 ReserveAuthorizedM5ReplyBudgetRecovery),不并入通用重试。
	legacyRecovered := false
	invocationID := stableM5ID("invocation", turn.TurnID, string(purpose), strconv.Itoa(attempt))
	reserved, err := a.manager.store.ReserveAIInvocation(store.ReserveAIInvocationRequest{
		InvocationID: invocationID, TurnID: turn.TurnID, Purpose: purpose, Attempt: attempt,
		Provider: a.manager.advice.ProviderName(), Model: a.manager.advice.ModelName(),
		InputHash: inputHash, CreatedAt: a.manager.now(),
	})
	if err != nil {
		if errors.Is(err, store.ErrDialogueTurnBinding) {
			return 0, a.settleM5TurnBoundaryChanged(turn.TurnID)
		}
		return 0, err
	}
	if !reserved.Created {
		if reserved.Invocation.FinishedAt != nil {
			if purpose != m5ai.PurposeReply ||
				!isLegacyM5ReplyBudgetFalsePositive(reserved.Invocation) {
				// 这一次 attempt 已经完成过,而 turn 还停在等建议的状态——说明
				// 上次进程在它完成后、写入 turn 终局前崩溃,那次没能产出可用
				// 建议。按重试纪律换下一次 attempt,不再判冲突。
				if attempt < store.MaxAIInvocationAttempts {
					return attempt + 1, nil
				}
				// 五次调用账已满而 turn 终局缺失,只是崩溃留下的账面歧义,
				// 不是候选人的业务终局(2026-08-02 裁决):不押给人工,跳过
				// 本轮。该轮不会再产生新调用,由输入边界变化或人工处置收敛。
				slog.Warn("对话轮调用账已满而终局缺失,本轮跳过",
					"turnId", turn.TurnID, "purpose", string(purpose),
					"attempt", attempt)
				return 0, errM5AdviceRoundSkipped
			}
			recovery, recoveryErr := a.manager.store.ReserveAuthorizedM5ReplyBudgetRecovery(
				store.ReserveM5ReplyBudgetRecoveryRequest{
					InvocationID: stableM5ID(
						"invocation", turn.TurnID, string(m5ai.PurposeReply), "2",
					),
					TurnID: turn.TurnID, Provider: a.manager.advice.ProviderName(),
					Model: a.manager.advice.ModelName(), InputHash: inputHash,
					CreatedAt: a.manager.now(),
				},
			)
			if recoveryErr != nil {
				switch {
				case errors.Is(recoveryErr, store.ErrDialogueTurnBinding):
					return 0, a.settleM5TurnBoundaryChanged(turn.TurnID)
				case errors.Is(recoveryErr, store.ErrM5ReplyBudgetRecoveryUnsafe):
					return 0, a.manager.store.MarkDialogueTurnManualRequired(
						turn.TurnID, "replyBudgetRecoveryUnsafe", a.manager.now(),
					)
				default:
					return 0, recoveryErr
				}
			}
			reserved = recovery
			if reserved.Invocation.FinishedAt != nil {
				return 0, a.manager.store.MarkDialogueTurnManualRequired(
					turn.TurnID, "replyBudgetRecoveryAlreadyFinished", a.manager.now(),
				)
			}
			legacyRecovered = true
			attempt = reserved.Invocation.Attempt
		}
		if !reserved.Created {
			// 预留了但没完成的遗留由脑启动时的 RecoverInterruptedAIInvocations
			// 统一收敛(main.go),那条路刻意保守终局、不重试;这里维持原样。
			return 0, a.finishInterruptedM5Advice(turn, material, facts, purpose, intent, reserved.Invocation)
		}
	}

	request := m5ai.CompletionRequest{
		InvocationID:        reserved.Invocation.InvocationID,
		Purpose:             purpose,
		ContextRevisionHash: turn.ContextRevisionHash,
		PromptRevision:      turn.RenderFormatVersion,
		UserContent:         content,
	}
	if purpose == m5ai.PurposeIntent {
		request.MaxOutputTokens = m5ai.IntentOutputTokenLimit
	} else {
		request.MaxOutputTokens = m5ai.ReplyOutputTokenLimit
	}
	started := time.Now()
	var response m5ai.CompletionResponse
	var callErr error
	func() {
		a.manager.mu.Unlock()
		defer a.manager.mu.Lock()
		response, callErr = a.manager.advice.CompleteJSON(ctx, request)
	}()
	completion := m5CompletionFromProvider(reserved.Invocation.InvocationID, response, callErr, time.Since(started), a.manager.now())

	if purpose == m5ai.PurposeIntent {
		advice := communication.IntentAdvice{State: communication.AdviceFailed}
		manualReason := ""
		if callErr == nil {
			suggestion, parseErr := m5ai.ParseIntentSuggestion(response.JSONText)
			if parseErr == nil {
				advice = communication.IntentAdvice{State: communication.AdviceOK, Suggestion: suggestion}
			} else {
				markBusinessParseFailure(&completion, parseErr)
			}
		}
		if callErr == nil && !reasoningUsageSafe(completion) {
			markReasoningUsageUnsafe(&completion)
			manualReason = "reasoningUsageUnsafe"
		} else if completion.Status == store.AIInvocationBudgetBlocked {
			manualReason = "inputBudgetBlocked"
		}
		decision, reduceErr := communication.Reduce(communication.ReduceInput{
			Turn: facts, Intent: advice, Reply: communication.ReplyAdvice{State: communication.AdviceAbsent},
		})
		if reduceErr != nil {
			markReducerRejected(&completion)
			manualReason = "reducerRejected"
		}
		logAIInvocationOutcome(
			a.manager.advice, purpose, completion, response.Diagnostics.TraceErrorCode,
		)
		if !legacyRecovered && m5AdviceShouldRetry(completion, decision, manualReason, attempt) {
			if failErr := a.manager.store.FailAIInvocationForRetry(completion, purpose); failErr != nil {
				return 0, failErr
			}
			// 2026-08-02 裁决:失败不在本轮内连打。留下本次失败事实后跳过该
			// 候选人,下一巡检轮经 attempt 游走走到下一个 attempt 再调用。
			slog.Warn("对话轮 AI 调用未产出可用建议,本轮跳过等下轮重试",
				"turnId", turn.TurnID, "purpose", string(purpose),
				"attempt", attempt, "status", string(completion.Status),
				"errorClass", completion.ErrorClass)
			return 0, errM5AdviceRoundSkipped
		}
		err := a.completeM5Intent(turn.TurnID, completion, decision, manualReason)
		if err != nil && !errors.Is(err, errM5AdviceRoundSkipped) {
			// 跳过哨兵是本轮的正常结论(边界收敛/结算重采),不是落账失败。
			logAIInvocationPersistenceFailure(a.manager.advice, purpose, completion)
		}
		return 0, err
	}

	reply := communication.ReplyAdvice{State: communication.AdviceFailed}
	if callErr == nil {
		suggestion, parseErr := m5ai.ParseReplySuggestion(response.JSONText)
		if parseErr == nil {
			reply = communication.ReplyAdvice{State: communication.AdviceOK, Suggestion: suggestion}
		} else {
			markBusinessParseFailure(&completion, parseErr)
		}
	}
	decision, reduceErr := a.reduceM5ReplyDecision(turn, material, facts, intent, reply)
	if reduceErr != nil {
		markReducerRejected(&completion)
		decision = communication.Decision{TurnID: turn.TurnID, TurnStatus: communication.TurnManualRequired, ManualReason: "reducerRejected"}
	} else if callErr == nil && !reasoningUsageSafe(completion) {
		markReasoningUsageUnsafe(&completion)
		decision = communication.Decision{
			TurnID: turn.TurnID, TurnStatus: communication.TurnManualRequired,
			ManualReason: "reasoningUsageUnsafe",
		}
	}
	logAIInvocationOutcome(
		a.manager.advice, purpose, completion, response.Diagnostics.TraceErrorCode,
	)
	if !legacyRecovered && m5AdviceShouldRetry(completion, decision, "", attempt) {
		if failErr := a.manager.store.FailAIInvocationForRetry(completion, purpose); failErr != nil {
			return 0, failErr
		}
		// 同 intent 分支:每巡检轮每用途至多一次真实 provider 调用。
		slog.Warn("对话轮 AI 调用未产出可用建议,本轮跳过等下轮重试",
			"turnId", turn.TurnID, "purpose", string(purpose),
			"attempt", attempt, "status", string(completion.Status),
			"errorClass", completion.ErrorClass)
		return 0, errM5AdviceRoundSkipped
	}
	err = a.completeM5Reply(
		turn.TurnID,
		completion,
		decision,
		reply.Suggestion,
	)
	if err != nil && !errors.Is(err, errM5AdviceRoundSkipped) {
		// 同 intent 分支:跳过哨兵不是落账失败,不记持久化失败日志。
		logAIInvocationPersistenceFailure(a.manager.advice, purpose, completion)
	}
	return 0, err
}

func isLegacyM5ReplyBudgetFalsePositive(invocation store.AIInvocation) bool {
	return invocation.Purpose == m5ai.PurposeReply &&
		invocation.Attempt == 1 &&
		invocation.Status == store.AIInvocationBudgetBlocked &&
		invocation.ErrorClass == "budgetBlocked" &&
		invocation.FinishedAt != nil &&
		invocation.OutputHash == "" &&
		invocation.InputTokens == 0 &&
		invocation.CachedInputTokens == 0 &&
		invocation.OutputTokens == 0 &&
		invocation.ReasoningTokens == nil &&
		invocation.UsageShape == "" &&
		invocation.LatencyMs == 0 &&
		invocation.EstimatedCostMicros == 0
}

func (a *roundActor) finishInterruptedM5Advice(
	turn store.DialogueTurn,
	material m5TurnMaterial,
	facts communication.FrozenTurnFacts,
	purpose m5ai.CompletionPurpose,
	intent communication.IntentAdvice,
	invocation store.AIInvocation,
) error {
	completion := store.AIInvocationCompletion{
		InvocationID: invocation.InvocationID, Status: store.AIInvocationTransportFailed,
		ErrorClass: "processInterrupted", FinishedAt: a.manager.now(),
	}
	if purpose == m5ai.PurposeIntent {
		decision, err := communication.Reduce(communication.ReduceInput{
			Turn: facts, Intent: communication.IntentAdvice{State: communication.AdviceFailed},
			Reply: communication.ReplyAdvice{State: communication.AdviceAbsent},
		})
		if err != nil {
			return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "reducerRejected", a.manager.now())
		}
		return a.completeM5Intent(turn.TurnID, completion, decision, "")
	}
	decision, err := a.reduceM5ReplyDecision(
		turn, material, facts, intent, communication.ReplyAdvice{State: communication.AdviceFailed},
	)
	if err != nil {
		return a.manager.store.MarkDialogueTurnManualRequired(turn.TurnID, "reducerRejected", a.manager.now())
	}
	return a.completeM5Reply(
		turn.TurnID,
		completion,
		decision,
		m5ai.ReplySuggestion{},
	)
}

func (a *roundActor) reduceM5ReplyDecision(
	turn store.DialogueTurn,
	material m5TurnMaterial,
	facts communication.FrozenTurnFacts,
	intent communication.IntentAdvice,
	reply communication.ReplyAdvice,
) (communication.Decision, error) {
	v4Owned, err := a.manager.store.CommunicationV4OwnsTurn(turn.TurnID)
	if err != nil {
		return communication.Decision{}, err
	}
	if v4Owned {
		decision := communication.Decision{TurnID: turn.TurnID}
		switch reply.State {
		case communication.AdviceFailed:
			decision.TurnStatus = communication.TurnManualRequired
			decision.ManualReason = communication.ManualReplyFailed
			return decision, nil
		case communication.AdviceOK:
			if err := m5ai.ValidateSendText(reply.Suggestion.Text); err != nil {
				decision.TurnStatus = communication.TurnManualRequired
				decision.ManualReason = communication.ManualReplyInvalid
				return decision, nil
			}
			decision.TurnStatus = communication.TurnAdviceReady
			decision.Action = &communication.CommunicationActionPlan{
				TurnID: turn.TurnID, Kind: communication.CommunicationActionReplyText,
				Text: reply.Suggestion.Text, Status: communication.CommunicationActionPlanned,
			}
			return decision, nil
		default:
			return communication.Decision{}, communication.ErrInvalidV4StateTransition
		}
	}
	if material.inputKind != store.DialogueTurnInputResumeAttachment {
		return communication.Reduce(communication.ReduceInput{Turn: facts, Intent: intent, Reply: reply})
	}
	if turn.IntentLabel != m5ai.IntentInterested || turn.IntentSource != store.DialogueIntentBusinessEvent ||
		intent.State != communication.AdviceAbsent {
		return communication.Decision{}, communication.ErrInvalidV4StateTransition
	}
	v4, err := reduceM5ResumeTurn(turn, material, reply)
	if err != nil || v4.Dialogue.IntentLabel != m5ai.IntentInterested ||
		v4.Dialogue.IntentSource != communication.IntentSourceBusinessEvent || len(v4.EventActions) != 0 {
		return communication.Decision{}, communication.ErrInvalidV4StateTransition
	}
	decision := communication.Decision{
		TurnID: turn.TurnID, IntentLabel: m5ai.IntentInterested,
		IntentSource: communication.IntentSourceBusinessEvent,
	}
	switch v4.Dialogue.Status {
	case communication.V4DialogueActionsPlanned:
		if v4.Dialogue.NextAdvice != communication.V4AdviceNone || len(v4.Dialogue.Actions) != 1 {
			return communication.Decision{}, communication.ErrInvalidV4StateTransition
		}
		action := v4.Dialogue.Actions[0]
		// 简历轮动作锚定引擎合成事件的段尾 seq;单卡轮里它就是该卡的 seq。
		if action.Kind != communication.V4ActionReplyText || action.CardMessageSeq != turn.InboundThroughSeq ||
			strings.TrimSpace(action.Text) == "" {
			return communication.Decision{}, communication.ErrInvalidV4StateTransition
		}
		decision.TurnStatus = communication.TurnAdviceReady
		decision.Action = &communication.CommunicationActionPlan{
			TurnID: turn.TurnID, Kind: communication.CommunicationActionReplyText,
			Text: action.Text, Status: communication.CommunicationActionPlanned,
		}
	case communication.V4DialogueManualRequired:
		if v4.Dialogue.NextAdvice != communication.V4AdviceNone || len(v4.Dialogue.Actions) != 0 ||
			(v4.Dialogue.ManualReason != communication.V4ManualReplyFailed &&
				v4.Dialogue.ManualReason != communication.V4ManualReplyInvalid) {
			return communication.Decision{}, communication.ErrInvalidV4StateTransition
		}
		decision.TurnStatus = communication.TurnManualRequired
		decision.ManualReason = communication.ManualReason(v4.Dialogue.ManualReason)
	default:
		return communication.Decision{}, communication.ErrInvalidV4StateTransition
	}
	return decision, nil
}

func (a *roundActor) completeM5Intent(
	turnID string,
	completion store.AIInvocationCompletion,
	decision communication.Decision,
	manualReason string,
) error {
	if manualReason == "" && decision.TurnStatus == communication.TurnManualRequired {
		manualReason = string(decision.ManualReason)
	}
	source := store.DialogueIntentSource(decision.IntentSource)
	label := decision.IntentLabel
	if manualReason != "" && manualReason != "intentRejected" {
		label = ""
		source = ""
	}
	_, err := a.manager.store.CompleteIntentInvocation(store.CompleteIntentInvocationRequest{
		Completion: completion, Label: label, Source: source, ManualReason: manualReason,
	})
	if errors.Is(err, store.ErrDialogueTurnBinding) {
		return a.settleM5TurnBoundaryChanged(turnID)
	}
	return err
}

func (a *roundActor) completeM5Reply(
	turnID string,
	completion store.AIInvocationCompletion,
	decision communication.Decision,
	suggestion m5ai.ReplySuggestion,
) error {
	request := store.CompleteReplyInvocationRequest{Completion: completion, PlannedAt: a.manager.now()}
	if decision.Action != nil {
		request.ActionID = stableM5ID("action", turnID, string(decision.Action.Kind))
		request.Phrases = append([]string(nil), suggestion.Phrases...)
		request.Text = decision.Action.Text
		request.Action = suggestion.Action
		request.MeetingTime = suggestion.MeetingTime
		request.ContentHash = syncledger.HashText(decision.Action.Text)
	} else {
		request.ManualReason = string(decision.ManualReason)
		if request.ManualReason == "" {
			request.ManualReason = "replyFailed"
		}
	}
	_, err := a.manager.store.CompleteReplyInvocation(request)
	if errors.Is(err, store.ErrDialogueTurnBinding) {
		return a.settleM5TurnBoundaryChanged(turnID)
	}
	var resample *store.AIAdviceResampleScheduledError
	if errors.As(err, &resample) {
		// 结算层重放判本次建议非法(规格 §五):巡检层的 reduce 只能看到解析
		// 与文本长度,planV4ReplyActions/建议应用策略的裁决在这里才浮出。样本
		// 已按失败待重采形态落账,本轮放下该候选人,下轮经 attempt 游走重采。
		slog.Warn("对话轮回复建议结算判非法,本样本作废等下轮重采",
			"turnId", turnID, "reason", resample.Reason, "attempt", resample.Attempt)
		return errM5AdviceRoundSkipped
	}
	return err
}

func m5CompletionFromProvider(
	invocationID string,
	response m5ai.CompletionResponse,
	callErr error,
	latency time.Duration,
	finishedAt time.Time,
) store.AIInvocationCompletion {
	completion := store.AIInvocationCompletion{
		InvocationID: invocationID, Status: store.AIInvocationOK,
		OutputHash: sha256Hex(response.JSONText), InputTokens: response.Usage.InputTokens,
		CachedInputTokens: response.Usage.CachedInputTokens, OutputTokens: response.Usage.OutputTokens,
		ReasoningTokens: response.Usage.ReasoningTokens, ReasoningContentEmpty: response.ReasoningContentEmpty,
		LatencyMs:           latency.Milliseconds(),
		ProviderHTTPStatus:  response.Diagnostics.ProviderHTTPStatus,
		RequestBytes:        response.Diagnostics.RequestBytes,
		ResponseBytes:       response.Diagnostics.ResponseBytes,
		TraceStatus:         response.Diagnostics.TraceStatus,
		EstimatedCostMicros: m5ai.EstimatedCostMicros(response.Usage), FinishedAt: finishedAt,
	}
	if response.Usage.ReasoningTokens == nil {
		completion.UsageShape = store.AIInvocationReasoningFieldAbsent
	} else {
		completion.UsageShape = store.AIInvocationUsageComplete
	}
	if callErr != nil {
		completion.Status, completion.ErrorClass = m5ProviderFailure(callErr)
		completion.FailureStage, completion.ErrorDetailCode = m5ProviderFailureDiagnostics(callErr)
		var providerErr *m5ai.ProviderError
		if errors.As(callErr, &providerErr) && providerErr.Class == "inputTokenBudgetExceeded" {
			return completion
		}
		completion.OutputHash = ""
		completion.UsageShape = ""
		completion.ReasoningTokens = nil
		completion.InputTokens = 0
		completion.CachedInputTokens = 0
		completion.OutputTokens = 0
		completion.EstimatedCostMicros = 0
		completion.ReasoningContentEmpty = false
	} else if completion.TraceStatus != "" && completion.TraceStatus != m5ai.TraceStatusComplete {
		completion.FailureStage = m5ai.FailureStagePersistence
		completion.ErrorDetailCode = safeTraceErrorCode(response.Diagnostics.TraceErrorCode)
	}
	return completion
}

func m5ProviderFailure(err error) (store.AIInvocationStatus, string) {
	var providerErr *m5ai.ProviderError
	if !errors.As(err, &providerErr) {
		return store.AIInvocationTransportFailed, "transport"
	}
	switch providerErr.Class {
	case "budgetBlocked", "inputTokenBudgetExceeded":
		return store.AIInvocationBudgetBlocked, providerErr.Class
	case "authentication", "rateLimited", "providerRejected":
		return store.AIInvocationProviderRejected, providerErr.Class
	case "responseInvalid":
		return store.AIInvocationInvalidOutput, "responseInvalid"
	case "timeout", "transport", "providerUnavailable", "requestInvalid", "requestPayloadTooLarge":
		return store.AIInvocationTransportFailed, providerErr.Class
	default:
		return store.AIInvocationTransportFailed, "providerFailure"
	}
}

func reasoningUsageSafe(completion store.AIInvocationCompletion) bool {
	if !completion.ReasoningContentEmpty {
		return false
	}
	if completion.UsageShape == store.AIInvocationUsageComplete {
		return completion.ReasoningTokens != nil && *completion.ReasoningTokens == 0
	}
	return completion.UsageShape == store.AIInvocationReasoningFieldAbsent && completion.ReasoningTokens == nil
}

func sha256Hex(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func stableM5ID(kind string, parts ...string) string {
	return kind + "-" + sha256Hex(strings.Join(parts, "\x00"))
}

// appendM5ReplyActionMenu 追加【本轮可选动作】块(规格 v4 §五)。菜单与
// settle 侧 planV4ReplyActions 同源;这里读到的是渲染时刻的状态,裁决时刻
// 可能已经变了,一律以裁决为准——那个方向只会更严(模型少建议),不放宽。
//
// 聚合读不到、缺失或损坏时省略该块、照常调用 AI:省略只是回到"模型自己猜、
// 脑事后照拒"的既有行为,不放宽任何裁决;而为一个提示增强停掉整轮,损失的
// 正是这个块本来要挽回的那一次回复。
func (a *roundActor) appendM5ReplyActionMenu(
	turn store.DialogueTurn,
	facts communication.FrozenTurnFacts,
	content string,
) string {
	aggregate, err := a.manager.store.CommunicationV4AggregateByProfile(turn.ProfileID)
	if err != nil || aggregate == nil {
		slog.Info("对话轮省略可选动作块:聚合不可用,本轮照常调用 AI",
			"turnId", turn.TurnID, "err", err)
		return content
	}
	facts.RecommendedSlots, _ = m5ai.FrozenRecommendedSlots(turn.RecommendedTimeText)
	withMenu, err := m5ai.AppendReplyActionMenu(
		content,
		communication.V4ReplyActionMenu(aggregate.State, facts, true),
	)
	if err != nil {
		slog.Info("对话轮省略可选动作块:块渲染失败,本轮照常调用 AI",
			"turnId", turn.TurnID, "err", err)
		return content
	}
	return withMenu
}
