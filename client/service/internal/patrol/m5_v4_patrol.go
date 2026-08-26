package patrol

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
)

// interleavedOutboundBoundary 与 outboundBoundaryMissing 两个挂人工原因自
// 2026-08-27 停机点第二步起停产(边界现算使真人插话自动接上、锚不再需要
// 认领);存量行照旧按原原因解读,离线解锁 CLI(m5_v4_boundary_recovery)
// 继续认识 outboundBoundaryMissing 字面量。

// processCommunicationV4Targets is the production successor to the single
// M5TrialSelection slot. Targets are durable profile roots, so one account
// round can advance every ready profile independently and a restart simply
// re-enumerates the same set.
func (a *roundActor) processCommunicationV4Targets(ctx context.Context) error {
	if err := a.processCommunicationV4CardTransitions(ctx); err != nil {
		return err
	}
	targets, err := a.manager.store.CommunicationTargetsForAccount(a.key())
	if err != nil {
		return err
	}
	for index := range targets {
		if err := a.ensureDispatchAllowed(ctx); err != nil {
			return err
		}
		target := targets[index]
		// The seven-day fallback outranks every previously planned browser
		// action. Apply it before draining this profile so an old schedule row
		// can never cross its terminal archive boundary into the WAL.
		archived, err := a.processCommunicationV4ScheduleArchive(target, true)
		if err != nil {
			return err
		}
		if archived {
			continue
		}
		if err := a.drainCommunicationV4EventActionsForProfile(
			ctx,
			target.Profile.ProfileID,
		); err != nil {
			return err
		}
		refreshed, ready, err := a.manager.store.CommunicationTargetForProfile(
			target.Profile.ProfileID,
		)
		if err != nil {
			return err
		}
		if !ready || refreshed == nil {
			continue
		}
		if err := a.processCommunicationV4Target(ctx, *refreshed); err != nil {
			return err
		}
		if err := a.drainCommunicationV4EventActionsForProfile(
			ctx,
			target.Profile.ProfileID,
		); err != nil {
			return err
		}
		// 收号排在取证之前:同一轮内补齐"号 + 两张图",发件箱三资产闸门
		// 可以立即放行,不必等 15 分钟兜底。
		if err := a.collectExchangedWechatContact(ctx, target.Profile.ProfileID); err != nil {
			return err
		}
		if err := a.captureNotificationEvidence(ctx, target.Profile.ProfileID); err != nil {
			return err
		}
	}
	return nil
}

// processCommunicationV4Profile advances exactly one page-observed profile
// through the same card, event, dialogue and effect rails used by the explicit
// current-conversation entrypoint. It never enumerates account-wide targets.
func (a *roundActor) processCommunicationV4Profile(
	ctx context.Context,
	profileID string,
) error {
	if err := a.processCommunicationV4CardTransitionsForProfile(ctx, profileID); err != nil {
		return err
	}
	target, ready, err := a.manager.store.CommunicationTargetForProfile(profileID)
	if err != nil {
		if errors.Is(err, store.ErrCommunicationV4Missing) {
			// 静默跳过曾两次让甲方误判故障(2026-07-31):跳过是设计内行为,
			// 但必须在日志留下原因,人不该靠翻库反推。
			slog.Info("沟通层跳过:候选人尚无沟通根,等简历采集/建根后自动接续",
				"profileId", profileID)
			return nil
		}
		return err
	}
	if !ready || target == nil {
		slog.Info("沟通层跳过:沟通目标未就绪(已隔离/已淘汰/未绑会话)",
			"profileId", profileID)
		return nil
	}
	archived, err := a.processCommunicationV4ScheduleArchive(*target, true)
	if err != nil || archived {
		return err
	}
	if err := a.drainCommunicationV4EventActionsForProfile(ctx, profileID); err != nil {
		return err
	}
	target, ready, err = a.manager.store.CommunicationTargetForProfile(profileID)
	if err != nil {
		return err
	}
	if !ready || target == nil {
		return nil
	}
	if err := a.processCommunicationV4Target(ctx, *target); err != nil {
		return err
	}
	if err := a.drainCommunicationV4EventActionsForProfile(ctx, profileID); err != nil {
		return err
	}
	// 位置在动作排空之后、收号之前:接受动作的正证使我方先于账本知情,本轮补齐
	// 账本与微信线状态,让收号、运营通知与产品 UI 当轮一致(立案 4.3)。补齐后
	// 新规划的候选人可见动作本轮不再排空,留给下一轮——否则回执可能抢在联系
	// 方式收编事务之前发出,与 2026-07-26 甲方裁决的前置条件相悖。
	if err := a.reconcileAfterWechatAccepted(ctx, profileID); err != nil {
		return err
	}
	if a.classificationCorrected {
		return nil
	}
	if err := a.collectExchangedWechatContact(ctx, profileID); err != nil {
		return err
	}
	return a.captureNotificationEvidence(ctx, profileID)
}

func (a *roundActor) processCommunicationV4Target(
	ctx context.Context,
	target store.CommunicationTarget,
) error {
	archived, err := a.processCommunicationV4ScheduleArchive(target, true)
	if err != nil || archived {
		return err
	}
	latest, err := a.manager.store.LatestDialogueTurnForProfile(target.Profile.ProfileID)
	if err != nil {
		return err
	}
	// parkedTurn 非空表示最新轮是 v4 停靠轮(manualRequired 但聚合仍 active,
	// 即 2026-08-02 第 3 族的纯计算失败停靠):它不再终身挡路,只有账本长出
	// 新候选人输入并走到下方开轮流程时,才由 FreezeCommunicationV4Turn 在
	// 冻结事务内作废停靠轮、重开新轮(第 4 族);没有新输入时保持停靠原状,
	// 不跑时刻表、不投影中性尾巴——作废是新输入到达时刻的事件驱动行为,
	// 不是扫库。
	var parkedTurn *store.DialogueTurn
	if latest != nil {
		v4Owned, err := a.manager.store.CommunicationV4OwnsTurn(latest.TurnID)
		if err != nil {
			return err
		}
		switch latest.Status {
		case store.DialogueTurnCollected, store.DialogueTurnClassified, store.DialogueTurnAdviceReady:
			if !v4Owned {
				return a.manager.store.MarkCommunicationV4AutomationManualRequired(
					target.Profile.ProfileID,
					"legacyTurnUnfinished",
					a.manager.now(),
				)
			}
			current, err := a.manager.store.RecheckDialogueTurnCurrent(
				latest.TurnID,
				a.manager.now(),
			)
			if err != nil || !current {
				if err == nil {
					// Recheck 已在事务内收敛旧轮(2026-08-02 裁决:pre-effect
					// 作废,effect 案底转人工);下一巡检轮对作废轮按最新账本
					// 边界重开新轮,这里按真实归宿记日志。
					a.logM5TurnBoundarySettled(latest.TurnID)
				}
				return err
			}
			if err := a.setStage("advising"); err != nil {
				return err
			}
			return a.advanceM5Turn(ctx, *latest)
		case store.DialogueTurnDispatching:
			// A constructed effect is owned by the persistent recovery rail.
			return nil
		case store.DialogueTurnManualRequired:
			if !v4Owned {
				reason := latest.FailureReason
				if reason == "" {
					reason = "legacyTurnManual"
				}
				return a.manager.store.MarkCommunicationV4AutomationManualRequired(
					target.Profile.ProfileID,
					reason,
					a.manager.now(),
				)
			}
			// 非停靠的 manualRequired(业务性转人工)会连带把聚合置 manual,
			// 那类档案在 CommunicationTargetForProfile 就已不 ready,走不到
			// 这里;能到这里的只有聚合仍 active 的停靠轮。
			parkedTurn = latest
		case store.DialogueTurnCompleted, store.DialogueTurnSuperseded:
			// A later ledger boundary may open the next turn.
		default:
			return store.ErrDialogueTurnState
		}
	}

	key := store.ConversationKey{
		Platform: target.Profile.Platform, AccountRef: target.Profile.AccountRef,
		ConversationRef: target.Conversation.ConversationRef,
	}
	messages, err := a.manager.store.MessagesForConversation(key)
	if err != nil {
		return err
	}
	if len(messages) == 0 ||
		messages[len(messages)-1].Seq <= target.Aggregate.ProjectedThroughSeq {
		if parkedTurn != nil {
			// 停靠轮且无新账本行:保持停靠,不跑时刻表(与拆腿前行为一致)。
			return nil
		}
		if len(messages) > 0 {
			a.advanceRespondedThroughWatermark(key, messages[len(messages)-1].Seq)
		}
		return a.processCommunicationV4Schedule(ctx, target)
	}

	// 边界现算(2026-08-27 停机点第二步,替代 0727 游标边界):锚 = 账本内
	// 最后一条未撤回出站,不问认领、不分真人/系统来源;本轮输入 = 锚之后、
	// 游标之后的连续候选人消息。真人手发的出站行按构造就是新锚,天然免疫;
	// 无主行不再需要认领,interleavedOutboundBoundary 挂人工机制随之拆除
	// (0727 计划 §2.1 第 6/回归 9 条已废,立案 §五-2)。
	cursor := target.Aggregate.ProjectedThroughSeq
	var lastOut int64
	for index := range messages {
		if messages[index].Direction == "out" && messages[index].Seq > lastOut {
			lastOut = messages[index].Seq
		}
	}
	answeredThrough := lastOut
	if cursor > answeredThrough {
		answeredThrough = cursor
	}
	// 三份行集,各司其职:prefix=已回应新行(逐行投影);fresh=锚后且游标后
	// 的新行(静默时逐行投影);turnBoundary=锚后全部行——轮边界按 v4 §一
	// 纯定义现算,完全不看游标,消费去重交给下方「最新轮身份比对」。
	var prefix, fresh, turnBoundary []store.Message
	pendingInput := false
	for index := range messages {
		message := messages[index]
		if message.Seq > lastOut {
			turnBoundary = append(turnBoundary, message)
			if message.Direction == "in" && message.Kind != "system" {
				pendingInput = true
			}
		}
		if message.Seq <= cursor {
			continue
		}
		if message.Seq <= answeredThrough {
			prefix = append(prefix, message)
			continue
		}
		fresh = append(fresh, message)
	}
	if !pendingInput {
		if parkedTurn != nil {
			// 停靠轮只被候选人新输入唤醒;已回应段与中性尾巴不触发投影
			// 推进或时刻表。
			return nil
		}
		target, err = a.projectCommunicationV4AnsweredSegment(
			target, append(prefix, fresh...),
		)
		if err != nil || target.Aggregate.AutomationStatus != store.ProfileCommunicationAutomationActive {
			return err
		}
		a.advanceRespondedThroughWatermark(key, target.Aggregate.ProjectedThroughSeq)
		return a.processCommunicationV4Schedule(ctx, target)
	}
	// 先把锚之前的已回应段逐行投影:真人手发的邀面/换微信卡照样归一化为
	// 业务事件推进主线,被回答的候选人输入滑动真实消息轮与沉默锚、不再
	// 要求对话回应。投影失败方向是跳过本轮,不冻结候选人。
	if len(prefix) > 0 {
		target, err = a.projectCommunicationV4AnsweredSegment(target, prefix)
		if err != nil || target.Aggregate.AutomationStatus != store.ProfileCommunicationAutomationActive {
			return err
		}
		cursor = target.Aggregate.ProjectedThroughSeq
	}
	inbound, validBoundary := store.DialogueTurnCandidateMessages(turnBoundary)
	if !validBoundary {
		// 边界按构造只含 in/system 行;走到这里是账本形状异常。跳过本轮、
		// 下轮按最新账本重算,不冻结候选人(2026-08-27 停机点第二步,原
		// interleavedOutboundBoundary 挂人工已拆除)。
		slog.Warn("沟通层跳过:边界候选行形状异常,等下轮巡检重算",
			"profileId", target.Profile.ProfileID)
		return nil
	}

	anchorSeq := lastOut
	var digest, turnID string
	if tail := inbound[len(inbound)-1]; tail.SourceKey == nil ||
		strings.TrimSpace(*tail.SourceKey) == "" {
		// 存量无身份行兜底为账本 seq(协议 §7.4;立案 C3):不转人工,只留
		// 观测痕。键值本身按 §4.5 保密边界不进日志。
		slog.Info("输入边界锚兜底为账本 seq(2026-08-09 前收编的存量无身份行)",
			"profileId", target.Profile.ProfileID, "seq", tail.Seq)
	}
	if anchorSeq == 0 {
		digest, turnID, err = store.DialogueTurnIdentityFromInboundRoot(
			target.Profile.ProfileID,
			target.Aggregate.RootGreetingIntentID,
			inbound,
			target.Aggregate.VerdictGeneration,
		)
	} else {
		var anchorMessage *store.Message
		for index := range messages {
			if messages[index].Seq == anchorSeq {
				anchorMessage = &messages[index]
				break
			}
		}
		if anchorMessage == nil || anchorMessage.Direction != "out" {
			// 锚按构造取自本列表内最新出站行;走到这里是并发变化或形状
			// 异常,跳过本轮、下轮重算(不再挂 outboundBoundaryMissing)。
			slog.Warn("沟通层跳过:出站锚行不可复取,等下轮巡检重算",
				"profileId", target.Profile.ProfileID, "anchorSeq", anchorSeq)
			return nil
		}
		digest, turnID, err = store.DialogueTurnIdentity(
			target.Profile.ProfileID,
			*anchorMessage,
			inbound,
			target.Aggregate.VerdictGeneration,
		)
	}
	if err != nil {
		// 身份计算失败是形状/数据问题,不是发送危险:跳过本轮,不冻结
		// 候选人(2026-08-27 停机点第二步,原挂人工路径拆除)。
		slog.Warn("沟通层跳过:回合身份计算失败,等下轮巡检重算",
			"profileId", target.Profile.ProfileID, "err", err)
		return nil
	}
	if latest != nil && latest.TurnID == turnID {
		// 该边界已被最新轮消费(同指纹):completed=已回应、superseded=已
		// 作废且指纹未变——都不重开,静默交时刻表;停靠轮维持「只被新输入
		// 唤醒」。裁决代次加一(resolvedFailed 裁决即恢复)或候选人新输入
		// 都会改变指纹,自然落入下方冻结重开路径。
		if parkedTurn != nil {
			return nil
		}
		switch latest.Status {
		case store.DialogueTurnCompleted, store.DialogueTurnSuperseded:
			target, err = a.projectCommunicationV4AnsweredSegment(target, fresh)
			if err != nil || target.Aggregate.AutomationStatus != store.ProfileCommunicationAutomationActive {
				return err
			}
			a.advanceRespondedThroughWatermark(key, target.Aggregate.ProjectedThroughSeq)
			return a.processCommunicationV4Schedule(ctx, target)
		default:
			return nil
		}
	}
	material, materialReady, err := a.manager.store.CommunicationAIMaterialForProfile(
		target.Profile.ProfileID,
	)
	if err != nil {
		return err
	}
	if !materialReady {
		slog.Info("沟通层跳过:AI 材料未就绪(简历快照或职位上下文缺失)",
			"profileId", target.Profile.ProfileID)
		return nil
	}
	// 可面试时段周表在冻结这一刻实时读一次，之后随 turn 固定。读失败不回落默认，
	// 否则会按用户已经改掉的表承诺时间。
	schedule, err := a.manager.store.InterviewSchedule()
	if err != nil {
		slog.Warn("V4 冻结转人工:可面试时段周表读取失败",
			"profileId", target.Profile.ProfileID,
			"reason", "scheduleRenderFailed", "err", err)
		return a.manager.store.MarkCommunicationV4AutomationManualRequired(
			target.Profile.ProfileID,
			"scheduleRenderFailed",
			a.manager.now(),
		)
	}
	recommended, err := m5ai.FreezeRecommendedTimeText(
		a.manager.now(),
		m5ai.GenerateSlots(a.manager.now(), schedule),
	)
	if err != nil {
		slog.Warn("V4 冻结转人工:推荐时段文本冻结失败",
			"profileId", target.Profile.ProfileID,
			"reason", "scheduleRenderFailed", "err", err)
		return a.manager.store.MarkCommunicationV4AutomationManualRequired(
			target.Profile.ProfileID,
			"scheduleRenderFailed",
			a.manager.now(),
		)
	}
	frozen, err := a.manager.store.FreezeCommunicationV4Turn(store.FreezeDialogueTurnRequest{
		TurnID: turnID, ProfileID: target.Profile.ProfileID,
		ConversationRef: target.Conversation.ConversationRef,
		InputDigest:     digest, HistoryThroughSeq: inbound[0].Seq - 1,
		InboundFromSeq: inbound[0].Seq, InboundThroughSeq: inbound[len(inbound)-1].Seq,
		ExpectedProjectedThroughSeq: cursor,
		OutboundAnchorSeq:           anchorSeq,
		ContextRevisionHash:         material.ContextRevision.RevisionHash,
		ResumeSnapshotID:            material.ResumeSnapshot.SnapshotID,
		RecommendedTimeText:         recommended,
		RenderFormatVersion:         m5ai.DialogueRenderFormatVersion,
		FrozenAt:                    a.manager.now(),
	})
	if err != nil {
		if errors.Is(err, store.ErrDialogueTurnBinding) {
			// 世界在本轮读取与冻结之间又动了(2026-08-27 停机点第二步:
			// 失败方向从挂人工改为作废本批下轮重开——下一巡检轮按最新
			// 账本重算边界,自然覆盖本次变化)。
			slog.Info("开轮跳过:边界在冻结前又发生变化,下轮按最新账本重开",
				"profileId", target.Profile.ProfileID)
			return nil
		}
		if errors.Is(err, store.ErrDialogueTurnState) {
			// 承重墙腿:旧轮带未收束发送案底(在途/suspect,Q6 放宽后纯
			// failed/resolvedFailed 案底已不在此列),开轮闸照旧拒绝。
			// 不算错误,等 WAL/suspect 收敛。
			slog.Info("开轮暂缓:旧轮带未收束发送案底,等 WAL/suspect 收敛",
				"profileId", target.Profile.ProfileID)
			return nil
		}
		if errors.Is(err, store.ErrCommunicationV4Conflict) {
			slog.Info("开轮跳过:冻结遇并发写或回执重放漂移,下轮按最新账本重算",
				"profileId", target.Profile.ProfileID)
			return nil
		}
		return err
	}
	if err := a.setStage("advising"); err != nil {
		return err
	}
	return a.advanceM5Turn(ctx, frozen.Turn)
}

// advanceRespondedThroughWatermark 推进「已回应至」水位(决策 3):巡检对
// 该会话得出"没有需要回应的输入"结论的静默收尾时点。失败只记日志——水位
// 永远只是加速下界,不是闸,落后的代价只是多一次重判。
func (a *roundActor) advanceRespondedThroughWatermark(key store.ConversationKey, seq int64) {
	if err := a.manager.store.AdvanceConversationRespondedThrough(key, seq); err != nil {
		slog.Warn("已回应至水位推进失败(仅加速下界,忽略)",
			"conversationRef", key.ConversationRef, "seq", seq, "err", err)
	}
}

// projectCommunicationV4AnsweredSegment 把一段不需要新对话回应的账本行按
// 序投影为业务事件(2026-08-27 停机点第二步,替代原 NonCandidateTail):
// system 家具行、我方/真人出站(含真人手发卡片的邀面/换微信事件),以及
// 已被后续出站回应过的候选人行。被回应的候选人表达与简历卡以 answered
// 标记应用——状态推进(真实消息轮、沉默锚、简历事件)照旧,不再产生对话
// 回应要求;换微信请求卡与邀面接受卡保留完整语义(无条件接受与已约面
// 推进不因真人插话而豁免)。
func (a *roundActor) projectCommunicationV4AnsweredSegment(
	target store.CommunicationTarget,
	messages []store.Message,
) (store.CommunicationTarget, error) {
	for index := range messages {
		message := messages[index]
		event, err := communication.NormalizeLedgerMessage(communication.LedgerMessageFact{
			Seq: message.Seq, Direction: message.Direction, Kind: message.Kind,
			Text: message.Text, CardType: message.CardType, CardState: message.CardState,
			Origin: message.Origin, TsApproxMs: message.TsApproxMs,
			InterviewMethod: message.InterviewMethod,
		})
		if err != nil {
			return target, err
		}
		switch event.Kind {
		case communication.EventCandidateExpressionReceived, communication.EventResumeSubmitted:
			event.Answered = true
		}
		result, err := a.manager.store.ApplyCommunicationV4BusinessEvent(
			store.ApplyCommunicationV4BusinessEventRequest{
				ProfileID: target.Profile.ProfileID,
				Event:     event,
				AppliedAt: a.manager.now(),
			},
		)
		if err != nil {
			return target, err
		}
		target.Aggregate = result.Aggregate
		if target.Aggregate.AutomationStatus != store.ProfileCommunicationAutomationActive {
			return target, nil
		}
	}
	return target, nil
}

func (a *roundActor) processCommunicationV4ScheduleArchive(
	target store.CommunicationTarget,
	hasPendingDialogue bool,
) (bool, error) {
	evaluatedAt := a.manager.now()
	decision, err := communication.EvaluateV4Schedule(communication.V4ScheduleInput{
		ProfileKey:          target.Profile.ProfileID,
		State:               target.Aggregate.State,
		ProjectedThroughSeq: target.Aggregate.ProjectedThroughSeq,
		Now:                 evaluatedAt,
		HasPendingDialogue:  hasPendingDialogue,
		Reply:               communication.ReplyAdvice{State: communication.AdviceAbsent},
	})
	if err != nil {
		return false, err
	}
	if decision.Status != communication.V4ScheduleActionsPlanned {
		return false, nil
	}
	if len(decision.Actions) != 1 || decision.Actions[0].Kind != communication.V4ActionArchive {
		return false, nil
	}
	result, err := a.manager.store.ApplyCommunicationV4ArchiveAction(
		store.ApplyCommunicationV4ArchiveActionRequest{
			ProfileID:                   target.Profile.ProfileID,
			ConversationRef:             target.Conversation.ConversationRef,
			ExpectedRevision:            target.Aggregate.Revision,
			ExpectedProjectedThroughSeq: target.Aggregate.ProjectedThroughSeq,
			HasPendingDialogue:          hasPendingDialogue,
			Action:                      decision.Actions[0],
			EvaluatedAt:                 evaluatedAt,
			AppliedAt:                   a.manager.now(),
		},
	)
	return err == nil && result != nil, err
}
