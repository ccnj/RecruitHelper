package store

import (
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
)

// Q1/Q2(2026-08-02):无已发前缀的纯 planned 轮在派发遭遇时整轮作废,
// 聚合保持 active,重放幂等。
func TestStalePlannedSupersedeDialoguePurePlannedTurn(t *testing.T) {
	s := openTest(t)
	fixture := seedPlannedCommunicationV4AutomaticAction(t, s, "stale-pure")
	result, err := s.SupersedeStaleDialoguePlannedAction(
		fixture.Turn.TurnID,
		fixture.Action.ActionID,
		fixture.Now,
	)
	if err != nil || result == nil || !result.Changed ||
		result.TurnStatus != DialogueTurnSuperseded {
		t.Fatalf("纯 planned 轮未整轮作废: result=%+v err=%v", result, err)
	}
	action, err := s.CommunicationActionByID(fixture.Action.ActionID)
	if err != nil || action == nil ||
		action.Status != CommunicationActionSuperseded ||
		action.FailureReason != CommunicationStalePlannedSuperseded ||
		action.EffectIntentID != nil || action.SentAt != nil {
		t.Fatalf("陈旧动作未按显式原因作废: action=%+v err=%v", action, err)
	}
	turn, err := s.DialogueTurnByID(fixture.Turn.TurnID)
	if err != nil || turn == nil || turn.Status != DialogueTurnSuperseded ||
		turn.FailureReason != CommunicationStalePlannedSuperseded {
		t.Fatalf("轮未收到 superseded 终局: turn=%+v err=%v", turn, err)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive {
		t.Fatalf("作废不得冻结聚合: aggregate=%+v err=%v", aggregate, err)
	}
	var audits int64
	if err := s.db.Model(&AuditEntry{}).
		Where("category = ?", auditCategoryStalePlannedSuperseded).
		Count(&audits).Error; err != nil || audits != 1 {
		t.Fatalf("作废必须落审计: count=%d err=%v", audits, err)
	}
	replayed, err := s.SupersedeStaleDialoguePlannedAction(
		fixture.Turn.TurnID,
		fixture.Action.ActionID,
		fixture.Now.Add(time.Minute),
	)
	if err != nil || replayed == nil || replayed.Changed ||
		replayed.TurnStatus != DialogueTurnSuperseded {
		t.Fatalf("重放不幂等: result=%+v err=%v", replayed, err)
	}
}

// 红线回归:绑过发送意图的对话动作行遇陈旧作废入口必须原样不动,
// dispatching 轮不 supersede。
func TestStalePlannedSupersedeDialogueRefusesEffectBoundAction(t *testing.T) {
	s := openTest(t)
	fixture, req, _ := createCommunicationV4AutomaticEffect(t, s, "stale-bound")
	_, err := s.SupersedeStaleDialoguePlannedAction(
		fixture.Turn.TurnID,
		fixture.Action.ActionID,
		fixture.Now.Add(time.Minute),
	)
	if !errors.Is(err, ErrCommunicationActionConflict) {
		t.Fatalf("绑过 intent 的动作必须拒绝作废: err=%v", err)
	}
	action, actionErr := s.CommunicationActionByID(fixture.Action.ActionID)
	turn, turnErr := s.DialogueTurnByID(fixture.Turn.TurnID)
	if actionErr != nil || action == nil ||
		action.Status != CommunicationActionEffectPending ||
		action.EffectIntentID == nil ||
		*action.EffectIntentID != req.Intent.IntentID ||
		turnErr != nil || turn == nil ||
		turn.Status != DialogueTurnDispatching {
		t.Fatalf("effect-bound 行被触碰: action=%+v turn=%+v errs=%v/%v",
			action, turn, actionErr, turnErr)
	}
}

// 红线回归:绑过发送意图的事件动作行遇陈旧作废入口必须原样不动。
func TestStalePlannedSupersedeEventActionRefusesEffectBound(t *testing.T) {
	s := openTest(t)
	fixture := seedCommunicationV4WechatReceiptEffect(t, s, "stale-bound")
	req := communicationV4EventEffectRequest(t, s, fixture, fixture.Action, "stale-bound")
	created, err := s.CreateEffectIntentAndCmd(req)
	if err != nil || !created.Created {
		t.Fatalf("事件动作 WAL 构造失败: result=%+v err=%v", created, err)
	}
	err = s.SupersedeStaleCommunicationV4EventAction(
		fixture.Action.ActionID,
		fixture.Now.Add(time.Minute),
	)
	if !errors.Is(err, ErrCommunicationV4EventActionConflict) {
		t.Fatalf("绑过 intent 的事件动作必须拒绝作废: err=%v", err)
	}
	action, actionErr := s.CommunicationV4EventActionByID(fixture.Action.ActionID)
	if actionErr != nil || action == nil ||
		action.Status != CommunicationV4EventActionEffectPending ||
		action.EffectIntentID == nil ||
		*action.EffectIntentID != req.Intent.IntentID {
		t.Fatalf("effect-bound 事件行被触碰: action=%+v err=%v", action, actionErr)
	}
}

// 已预物化的 planned 后项随父项同批作废(同族 reason),聚合保持 active。
func TestStalePlannedSupersedeEventActionClosesPlannedDependents(t *testing.T) {
	s := openTest(t)
	profileID := "profile-v4-stale-dependents"
	fixture := seedReadyCommunicationTarget(t, s, profileID)
	setCommunicationV4FixedPhrasePackageContent(
		t,
		s,
		"revision-"+profileID,
		`{
			"wechatAccepted":{
				"message":"回执气泡一",
				"messages":["回执气泡一","回执气泡二"],
				"actions":[],
				"enabled":true
			}
		}`,
	)
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "card", CardType: "wechatExchange",
		CardState: "accepted", ContentHash: "wechat-accepted-stale-dependents",
	})
	req := communicationV4TurnRequest(t, s, fixture, inbound)
	frozen, err := s.FreezeCommunicationV4Turn(req)
	if err != nil {
		t.Fatal(err)
	}
	actions, err := s.CommunicationV4EventActionsBySource(
		profileID,
		CommunicationV4InputDialogueTurn,
		frozen.Turn.TurnID,
	)
	if err != nil {
		t.Fatal(err)
	}
	var first, second *CommunicationV4EventAction
	for index := range actions {
		if actions[index].V4Kind != communication.V4ActionWechatReceipt {
			continue
		}
		if actions[index].DependsOnActionID == nil {
			copied := actions[index]
			first = &copied
		} else {
			copied := actions[index]
			second = &copied
		}
	}
	if first == nil || second == nil ||
		first.Status != CommunicationV4EventActionPlanned ||
		second.Status != CommunicationV4EventActionPlanned ||
		second.DependsOnActionID == nil ||
		*second.DependsOnActionID != first.ActionID {
		t.Fatalf("双气泡回执链未物化: actions=%+v", actions)
	}
	if err := s.SupersedeStaleCommunicationV4EventAction(
		first.ActionID,
		req.FrozenAt.Add(24*time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	firstAfter, firstErr := s.CommunicationV4EventActionByID(first.ActionID)
	secondAfter, secondErr := s.CommunicationV4EventActionByID(second.ActionID)
	if firstErr != nil || firstAfter == nil ||
		firstAfter.Status != CommunicationV4EventActionManualRequired ||
		firstAfter.FailureReason != CommunicationStalePlannedSuperseded ||
		secondErr != nil || secondAfter == nil ||
		secondAfter.Status != CommunicationV4EventActionManualRequired ||
		secondAfter.FailureReason != CommunicationStalePlannedSuperseded {
		t.Fatalf("父项与预物化后项未同批作废: first=%+v second=%+v errs=%v/%v",
			firstAfter, secondAfter, firstErr, secondErr)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(profileID)
	if err != nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive {
		t.Fatalf("事件作废不得冻结聚合: aggregate=%+v err=%v", aggregate, err)
	}
	// 重放幂等:已作废的行(含被闭包收编的后项)再次遭遇时静默返回。
	if err := s.SupersedeStaleCommunicationV4EventAction(
		second.ActionID,
		req.FrozenAt.Add(25*time.Hour),
	); err != nil {
		t.Fatalf("闭包后项重放不幂等: err=%v", err)
	}
}

// 时刻表残留作废后,pending 判定不再挡住下一次重评铸新计划(测试要求 5,
// 镜像 TestCommunicationV4SchedulePlanRefusesNewPlanWhilePriorPending 的现场,
// 但期望冻结成功)。
func TestStalePlannedSupersedeScheduleUnblocksNextPlan(t *testing.T) {
	s := openTest(t)
	fixture, req, result := freezeCommunicationV4ColdWechatPlan(t, s, "stale-unblock")
	first := result.Actions[0]
	if err := s.SupersedeStaleCommunicationV4EventAction(
		first.ActionID,
		req.FrozenAt.Add(24*time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	voided, err := s.CommunicationV4EventActionByID(first.ActionID)
	if err != nil || voided == nil ||
		voided.Status != CommunicationV4EventActionManualRequired ||
		voided.FailureReason != CommunicationStalePlannedSuperseded ||
		voided.EffectIntentID != nil {
		t.Fatalf("时刻表链首未按显式原因作废: action=%+v err=%v", voided, err)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || aggregate == nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive {
		t.Fatalf("时刻表作废不得冻结聚合: aggregate=%+v err=%v", aggregate, err)
	}
	state := aggregate.State
	state.ColdWechatTextSent = true
	persistCommunicationV4ScheduleState(t, s, aggregate, state)
	next := req
	next.EvaluatedAt = req.EvaluatedAt.Add(time.Hour)
	next.FrozenAt = next.EvaluatedAt.Add(time.Second)
	replan, err := s.FreezeCommunicationV4SchedulePlan(next)
	if err != nil || replan == nil || !replan.Created ||
		replan.Plan == nil || len(replan.Actions) != 1 ||
		replan.Actions[0].V4Kind != communication.V4ActionColdWechatInvite ||
		replan.Actions[0].Status != CommunicationV4EventActionPlanned {
		t.Fatalf("作废后重评未能铸新计划: result=%+v err=%v", replan, err)
	}
}
