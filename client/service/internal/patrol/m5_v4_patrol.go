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

const (
	communicationV4ManualInterleavedOutbound = "interleavedOutboundBoundary"
	communicationV4ManualMissingOutbound     = "outboundBoundaryMissing"
)

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
		return a.processCommunicationV4Schedule(ctx, target)
	}

	// 投影游标只表示顺序进度，可停在 in/out/system 任意行；边界一律取
	// 游标之后的全部账本行，轮身份锚另由聚合 state/招呼链接解析
	// （0727当日计划3）。
	cursor := target.Aggregate.ProjectedThroughSeq
	var boundary []store.Message
	for index := range messages {
		if messages[index].Seq > cursor {
			boundary = append(boundary, messages[index])
		}
	}
	if len(boundary) == 0 {
		return nil
	}
	hasCandidateInput := false
	hasOutbound := false
	for index := range boundary {
		message := boundary[index]
		switch {
		case message.Direction == "system":
		case message.Direction == "in" && message.Kind == "system":
		case message.Direction == "in":
			hasCandidateInput = true
		case message.Direction == "out":
			hasOutbound = true
		default:
			return a.manager.store.MarkCommunicationV4AutomationManualRequired(
				target.Profile.ProfileID,
				communicationV4ManualInterleavedOutbound,
				a.manager.now(),
			)
		}
	}
	if hasCandidateInput && hasOutbound {
		return a.manager.store.MarkCommunicationV4AutomationManualRequired(
			target.Profile.ProfileID,
			communicationV4ManualInterleavedOutbound,
			a.manager.now(),
		)
	}
	if !hasCandidateInput {
		if parkedTurn != nil {
			// 停靠轮只被候选人新输入唤醒;中性尾巴不触发投影推进或时刻表。
			return nil
		}
		target, err = a.projectCommunicationV4NonCandidateTail(target, boundary)
		if err != nil || target.Aggregate.AutomationStatus != store.ProfileCommunicationAutomationActive {
			return err
		}
		return a.processCommunicationV4Schedule(ctx, target)
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
	inbound, validBoundary := store.DialogueTurnCandidateMessages(boundary)
	if !validBoundary {
		return a.manager.store.MarkCommunicationV4AutomationManualRequired(
			target.Profile.ProfileID,
			communicationV4ManualInterleavedOutbound,
			a.manager.now(),
		)
	}

	anchorSeq, err := a.manager.store.CommunicationV4OutboundAnchorSeq(
		target.Profile.ProfileID,
	)
	if err != nil {
		if errors.Is(err, store.ErrCommunicationV4AnchorUnresolvable) {
			return a.manager.store.MarkCommunicationV4AutomationManualRequired(
				target.Profile.ProfileID,
				communicationV4ManualMissingOutbound,
				a.manager.now(),
			)
		}
		return err
	}
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
			return a.manager.store.MarkCommunicationV4AutomationManualRequired(
				target.Profile.ProfileID,
				communicationV4ManualMissingOutbound,
				a.manager.now(),
			)
		}
		digest, turnID, err = store.DialogueTurnIdentity(
			target.Profile.ProfileID,
			*anchorMessage,
			inbound,
			target.Aggregate.VerdictGeneration,
		)
	}
	if err != nil {
		slog.Warn("V4 冻结转人工:回合身份计算失败",
			"profileId", target.Profile.ProfileID,
			"reason", communicationV4ManualInterleavedOutbound, "err", err)
		return a.manager.store.MarkCommunicationV4AutomationManualRequired(
			target.Profile.ProfileID,
			communicationV4ManualInterleavedOutbound,
			a.manager.now(),
		)
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
			return a.manager.store.MarkCommunicationV4AutomationManualRequired(
				target.Profile.ProfileID,
				"turnBoundaryChanged",
				a.manager.now(),
			)
		}
		if parkedTurn != nil && errors.Is(err, store.ErrDialogueTurnState) {
			// 承重墙腿:停靠轮带发送案底(动作绑过 EffectIntentID/已派发),
			// 开轮闸照旧拒绝。不算错误,等 WAL/suspect 收敛后由人工处置。
			slog.Info("开轮暂缓:停靠旧轮带发送案底,等 WAL/suspect 收敛",
				"profileId", target.Profile.ProfileID, "turnId", parkedTurn.TurnID)
			return nil
		}
		return err
	}
	if err := a.setStage("advising"); err != nil {
		return err
	}
	return a.advanceM5Turn(ctx, frozen.Turn)
}

func (a *roundActor) projectCommunicationV4NonCandidateTail(
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
