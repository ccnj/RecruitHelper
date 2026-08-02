package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"

	"gorm.io/gorm"
)

func seedCommunicationV4SchedulePlanFixture(
	t *testing.T,
	s *Store,
	suffix string,
	coldWechat bool,
) (resumeStoreFixture, CommunicationV4Aggregate, JobAIContextRevision) {
	t.Helper()
	var messages []string
	if coldWechat {
		messages = []string{"{称呼}您好，还方便继续沟通吗"}
	}
	return seedCommunicationV4SchedulePlanFixtureMessages(t, s, suffix, messages)
}

func seedCommunicationV4SchedulePlanFixtureMessages(
	t *testing.T,
	s *Store,
	suffix string,
	coldMessages []string,
) (resumeStoreFixture, CommunicationV4Aggregate, JobAIContextRevision) {
	t.Helper()
	fixture := seedReadyCommunicationTarget(
		t,
		s,
		"profile-v4-schedule-plan-"+suffix,
	)
	material, ready, err := s.CommunicationAIMaterialForProfile(fixture.ProfileID)
	if err != nil || !ready {
		t.Fatalf("读取时刻表配置失败: ready=%v err=%v", ready, err)
	}
	if len(coldMessages) > 0 {
		var revision JobAIContextRevision
		if err := s.db.First(
			&revision,
			"revision_hash = ?",
			material.ContextRevision.RevisionHash,
		).Error; err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(map[string]any{
			"silence48Wechat": map[string]any{
				"message":  coldMessages[0],
				"messages": coldMessages,
				"actions":  []string{},
				"enabled":  true,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		revision.SourcePackage.Documents = append(
			revision.SourcePackage.Documents,
			m5ai.JobConfigDocument{
				DocType: "固定话术",
				Content: string(payload),
			},
		)
		raw, err := json.Marshal(revision.SourcePackage)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.db.Model(&JobAIContextRevision{}).
			Where("revision_hash = ?", revision.RevisionHash).
			UpdateColumn("source_package", string(raw)).Error; err != nil {
			t.Fatal(err)
		}
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || aggregate == nil || aggregate.State.LastOutboundAt == nil {
		t.Fatalf("读取时刻表聚合失败: aggregate=%+v err=%v", aggregate, err)
	}
	return fixture, *aggregate, material.ContextRevision
}

func persistCommunicationV4ScheduleState(
	t *testing.T,
	s *Store,
	aggregate *CommunicationV4Aggregate,
	state communication.V4State,
) {
	t.Helper()
	status, endReason, err := candidateProfileProjection(state)
	if err != nil {
		t.Fatal(err)
	}
	aggregate.State = state
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(aggregate).Error; err != nil {
			return err
		}
		return tx.Model(&CandidateProfile{}).
			Where("profile_id = ?", aggregate.ProfileID).
			Updates(map[string]any{
				"main_status": status,
				"end_reason":  endReason,
			}).Error
	}); err != nil {
		t.Fatal(err)
	}
}

func freezeCommunicationV4ColdWechatPlan(
	t *testing.T,
	s *Store,
	suffix string,
) (
	resumeStoreFixture,
	FreezeCommunicationV4SchedulePlanRequest,
	*FreezeCommunicationV4SchedulePlanResult,
) {
	t.Helper()
	return freezeCommunicationV4ColdWechatPlanMessages(
		t,
		s,
		suffix,
		[]string{"{称呼}您好，还方便继续沟通吗"},
	)
}

func freezeCommunicationV4ColdWechatPlanMessages(
	t *testing.T,
	s *Store,
	suffix string,
	messages []string,
) (
	resumeStoreFixture,
	FreezeCommunicationV4SchedulePlanRequest,
	*FreezeCommunicationV4SchedulePlanResult,
) {
	t.Helper()
	fixture, aggregate, revision :=
		seedCommunicationV4SchedulePlanFixtureMessages(t, s, suffix, messages)
	state := aggregate.State
	state.MainStatus = communication.V4StatusCommunicating
	state.ColdPromptRemaining = 0
	state.ColdWechatRemaining = 1
	state.ColdWechatTextSent = false
	state.WechatState = communication.V4WechatNotInvited
	persistCommunicationV4ScheduleState(t, s, &aggregate, state)
	evaluatedAt := state.LastOutboundAt.Add(25 * time.Hour)
	req := FreezeCommunicationV4SchedulePlanRequest{
		ProfileID:                   fixture.ProfileID,
		ConversationRef:             fixture.ConversationRef,
		ExpectedRevision:            aggregate.Revision,
		ExpectedProjectedThroughSeq: aggregate.ProjectedThroughSeq,
		ContextRevisionHash:         revision.RevisionHash,
		Reply:                       communication.ReplyAdvice{State: communication.AdviceAbsent},
		EvaluatedAt:                 evaluatedAt,
		FrozenAt:                    evaluatedAt.Add(time.Second),
	}
	result, err := s.FreezeCommunicationV4SchedulePlan(req)
	if err != nil || result == nil || !result.Created ||
		result.Plan == nil ||
		len(result.Plan.PlannedActions) != len(messages)+1 ||
		len(result.Actions) != 1 {
		t.Fatalf("冷催二计划未原子冻结首项: result=%+v err=%v", result, err)
	}
	return fixture, req, result
}

func TestCommunicationV4SchedulePlanFreezesRenderedColdWechatAndReplays(t *testing.T) {
	s := openTest(t)
	_, req, result := freezeCommunicationV4ColdWechatPlan(t, s, "cold-replay")
	first := result.Actions[0]
	if first.V4Kind != communication.V4ActionColdWechatText ||
		first.SourceInputKind != CommunicationV4InputSchedulePlan ||
		first.SourceInputKey != result.Plan.PlanID ||
		first.SourceOrdinal != 0 ||
		first.DependsOnActionID != nil ||
		strings.Contains(first.Text, "{称呼}") ||
		first.ContextRevisionHash != req.ContextRevisionHash ||
		result.Plan.BasisRevision != req.ExpectedRevision ||
		result.Plan.BasisProjectedThroughSeq != req.ExpectedProjectedThroughSeq ||
		result.Plan.BasisMessageTailSeq != req.ExpectedProjectedThroughSeq ||
		result.Plan.DueAt.After(req.EvaluatedAt) {
		t.Fatalf("冷催二计划未冻结渲染正文与精确 basis: plan=%+v first=%+v",
			result.Plan, first)
	}
	replayed, err := s.FreezeCommunicationV4SchedulePlan(req)
	if err != nil || replayed == nil || replayed.Created ||
		replayed.Plan == nil || replayed.Plan.PlanID != result.Plan.PlanID ||
		len(replayed.Actions) != 1 ||
		replayed.Actions[0].ActionID != first.ActionID {
		t.Fatalf("冷催二计划重放增生: result=%+v err=%v", replayed, err)
	}
}

func TestCommunicationV4SchedulePlanPositiveTextMaterializesInviteOnlyAfterEvidence(t *testing.T) {
	s := openTest(t)
	fixture, _, result := freezeCommunicationV4ColdWechatPlan(
		t,
		s,
		"cold-positive",
	)
	first := result.Actions[0]
	effectFixture := communicationV4EventEffectFixture{
		resumeStoreFixture: fixture,
		Action:             first,
		Now:                result.Plan.CreatedAt.Add(time.Second),
	}
	childKey := result.Plan.PlannedActions[1].ActionKey
	childID, err := CommunicationV4EventActionID(fixture.ProfileID, childKey)
	if err != nil {
		t.Fatal(err)
	}
	if action, err := s.CommunicationV4EventActionByID(childID); err != nil || action != nil {
		t.Fatalf("父项正证前不应物化邀请: action=%+v err=%v", action, err)
	}
	parentReq := communicationV4EventEffectRequest(
		t,
		s,
		effectFixture,
		first,
		"schedule-cold-positive-text",
	)
	parentCreated, err := s.CreateEffectIntentAndCmd(parentReq)
	if err != nil || !parentCreated.Created {
		t.Fatalf("冷催正文 WAL 构造失败: result=%+v err=%v", parentCreated, err)
	}
	settleCommunicationV4EventTextEffect(
		t,
		s,
		effectFixture,
		first,
		parentCreated,
		"schedule-cold-positive-text",
	)
	child, err := s.CommunicationV4EventActionByID(childID)
	if err != nil || child == nil ||
		child.Status != CommunicationV4EventActionPlanned ||
		child.V4Kind != communication.V4ActionColdWechatInvite ||
		child.DependsOnActionID == nil ||
		*child.DependsOnActionID != first.ActionID {
		t.Fatalf("冷催正文正证后未物化邀请: child=%+v err=%v", child, err)
	}
	effectFixture.Action = *child
	effectFixture.Now = effectFixture.Now.Add(2 * time.Minute)
	childReq := communicationV4EventEffectRequest(
		t,
		s,
		effectFixture,
		*child,
		"schedule-cold-positive-invite",
	)
	if childReq.PreviousIntentID != parentReq.Intent.IntentID {
		t.Fatalf("邀请没有钉住正文 intent: child=%q parent=%q",
			childReq.PreviousIntentID, parentReq.Intent.IntentID)
	}
	childCreated, err := s.CreateEffectIntentAndCmd(childReq)
	if err != nil || !childCreated.Created {
		t.Fatalf("冷催正文正证后邀请 WAL 未放行: result=%+v err=%v",
			childCreated, err)
	}
}

func TestCommunicationV4SchedulePlanFailedTextNeverMaterializesInvite(t *testing.T) {
	s := openTest(t)
	fixture, _, result := freezeCommunicationV4ColdWechatPlan(
		t,
		s,
		"cold-failed",
	)
	first := result.Actions[0]
	effectFixture := communicationV4EventEffectFixture{
		resumeStoreFixture: fixture,
		Action:             first,
		Now:                result.Plan.CreatedAt.Add(time.Second),
	}
	req := communicationV4EventEffectRequest(
		t,
		s,
		effectFixture,
		first,
		"schedule-cold-failed-text",
	)
	created, err := s.CreateEffectIntentAndCmd(req)
	if err != nil {
		t.Fatal(err)
	}
	failedAt := effectFixture.Now.Add(time.Minute)
	if _, err := s.ApplyResultMessage(
		created.Command.MsgID,
		"result-schedule-cold-failed-text",
		"result",
		fixture.HandID,
		func(command *CmdRecord) (ResultCommandMutation, error) {
			command.Status = CmdFailed
			command.SideEffect = "none"
			command.TerminalAt = &failedAt
			return ResultCommandMutation{
				Save: true,
				Effect: &EffectResultMutation{
					IntentStatus: EffectIntentFailed,
					Reason:       "failedNone",
				},
			}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	childID, err := CommunicationV4EventActionID(
		fixture.ProfileID,
		result.Plan.PlannedActions[1].ActionKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CommunicationV4EventActionByID(childID)
	if err != nil || child != nil {
		t.Fatalf("正文失败后不得物化邀请: child=%+v err=%v", child, err)
	}
}

func TestCommunicationV4SchedulePlanMultiBubbleChainMaterializesInOrder(t *testing.T) {
	s := openTest(t)
	messages := []string{
		"{称呼}您好，考虑得怎么样了",
		"我们下周还有面试名额",
		"方便的话加个微信细聊",
	}
	fixture, _, result := freezeCommunicationV4ColdWechatPlanMessages(
		t,
		s,
		"cold-multi",
		messages,
	)
	plans := result.Plan.PlannedActions
	if len(plans) != 4 ||
		plans[0].Kind != communication.V4ActionColdWechatText ||
		plans[1].Kind != communication.V4ActionColdWechatText ||
		plans[2].Kind != communication.V4ActionColdWechatText ||
		plans[3].Kind != communication.V4ActionColdWechatInvite ||
		plans[1].ActionKey != plans[0].ActionKey+"|bubble:2" ||
		plans[2].ActionKey != plans[0].ActionKey+"|bubble:3" ||
		strings.Contains(plans[0].ActionKey, "|bubble:") ||
		strings.Contains(plans[0].Text, "{称呼}") ||
		plans[1].Text != messages[1] ||
		plans[2].Text != messages[2] {
		t.Fatalf("多气泡计划形态错误: %+v", plans)
	}
	ids := make([]string, len(plans))
	for index := range plans {
		id, err := CommunicationV4EventActionID(
			fixture.ProfileID,
			plans[index].ActionKey,
		)
		if err != nil {
			t.Fatal(err)
		}
		ids[index] = id
	}
	for _, id := range ids[1:] {
		if action, err := s.CommunicationV4EventActionByID(id); err != nil || action != nil {
			t.Fatalf("前项正证前不应物化后项: action=%+v err=%v", action, err)
		}
	}
	now := result.Plan.CreatedAt.Add(time.Second)
	current := result.Actions[0]
	parentIntentID := ""
	for index := range plans {
		effectFixture := communicationV4EventEffectFixture{
			resumeStoreFixture: fixture,
			Action:             current,
			Now:                now,
		}
		suffix := fmt.Sprintf("cold-multi-%d", index)
		req := communicationV4EventEffectRequest(t, s, effectFixture, current, suffix)
		if index > 0 && req.PreviousIntentID != parentIntentID {
			t.Fatalf(
				"第 %d 项没有钉住父 intent: got=%q want=%q",
				index+1,
				req.PreviousIntentID,
				parentIntentID,
			)
		}
		created, err := s.CreateEffectIntentAndCmd(req)
		if err != nil || !created.Created {
			t.Fatalf("第 %d 项 WAL 构造失败: result=%+v err=%v", index+1, created, err)
		}
		if index == len(plans)-1 {
			break
		}
		settleCommunicationV4EventTextEffect(t, s, effectFixture, current, created, suffix)
		parentIntentID = req.Intent.IntentID
		next, err := s.CommunicationV4EventActionByID(ids[index+1])
		if err != nil || next == nil ||
			next.Status != CommunicationV4EventActionPlanned ||
			next.V4Kind != plans[index+1].Kind ||
			next.SourceOrdinal != index+1 ||
			next.DependsOnActionID == nil ||
			*next.DependsOnActionID != current.ActionID {
			t.Fatalf("第 %d 项正证后未按序物化后项: next=%+v err=%v", index+1, next, err)
		}
		for _, id := range ids[index+2:] {
			if action, err := s.CommunicationV4EventActionByID(id); err != nil || action != nil {
				t.Fatalf("越级物化: action=%+v err=%v", action, err)
			}
		}
		current = *next
		now = now.Add(2 * time.Minute)
	}
}

func TestCommunicationV4SchedulePlanMidChainFailureStopsPosterior(t *testing.T) {
	s := openTest(t)
	messages := []string{"第一句", "第二句", "第三句"}
	fixture, _, result := freezeCommunicationV4ColdWechatPlanMessages(
		t,
		s,
		"cold-midfail",
		messages,
	)
	plans := result.Plan.PlannedActions
	first := result.Actions[0]
	effectFixture := communicationV4EventEffectFixture{
		resumeStoreFixture: fixture,
		Action:             first,
		Now:                result.Plan.CreatedAt.Add(time.Second),
	}
	firstReq := communicationV4EventEffectRequest(
		t,
		s,
		effectFixture,
		first,
		"cold-midfail-1",
	)
	firstCreated, err := s.CreateEffectIntentAndCmd(firstReq)
	if err != nil || !firstCreated.Created {
		t.Fatalf("首气泡 WAL 构造失败: result=%+v err=%v", firstCreated, err)
	}
	settleCommunicationV4EventTextEffect(
		t,
		s,
		effectFixture,
		first,
		firstCreated,
		"cold-midfail-1",
	)
	secondID, err := CommunicationV4EventActionID(fixture.ProfileID, plans[1].ActionKey)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CommunicationV4EventActionByID(secondID)
	if err != nil || second == nil {
		t.Fatalf("首气泡正证后第二气泡未物化: second=%+v err=%v", second, err)
	}
	effectFixture.Action = *second
	effectFixture.Now = effectFixture.Now.Add(2 * time.Minute)
	secondReq := communicationV4EventEffectRequest(
		t,
		s,
		effectFixture,
		*second,
		"cold-midfail-2",
	)
	secondCreated, err := s.CreateEffectIntentAndCmd(secondReq)
	if err != nil || !secondCreated.Created {
		t.Fatalf("第二气泡 WAL 构造失败: result=%+v err=%v", secondCreated, err)
	}
	failedAt := effectFixture.Now.Add(time.Minute)
	if _, err := s.ApplyResultMessage(
		secondCreated.Command.MsgID,
		"result-cold-midfail-2",
		"result",
		fixture.HandID,
		func(command *CmdRecord) (ResultCommandMutation, error) {
			command.Status = CmdFailed
			command.SideEffect = "none"
			command.TerminalAt = &failedAt
			return ResultCommandMutation{
				Save: true,
				Effect: &EffectResultMutation{
					IntentStatus: EffectIntentFailed,
					Reason:       "failedNone",
				},
			}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{plans[2].ActionKey, plans[3].ActionKey} {
		id, err := CommunicationV4EventActionID(fixture.ProfileID, key)
		if err != nil {
			t.Fatal(err)
		}
		if action, err := s.CommunicationV4EventActionByID(id); err != nil || action != nil {
			t.Fatalf("链中失败后不得物化后项: action=%+v err=%v", action, err)
		}
	}
	settled, err := s.CommunicationV4EventActionByID(second.ActionID)
	if err != nil || settled == nil ||
		settled.Status == CommunicationV4EventActionSent {
		t.Fatalf("失败气泡不得记为已发送: settled=%+v err=%v", settled, err)
	}
	// 2026-08-02 §8.4 通则:链中气泡干净失败自动重铸(时刻表链的后项按正证
	// 惰性物化,失败时不存在依赖者,收窄准入放行)。原行 retried 留档,档案
	// 不冻结。
	if settled.Status != CommunicationV4EventActionRetried ||
		settled.FailureReason != "effectFailed" {
		t.Fatalf("链中干净失败未标 retried 留档: %+v", settled)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || aggregate.AutomationStatus != ProfileCommunicationAutomationActive {
		t.Fatalf("链中干净失败不得冻结档案: %+v err=%v", aggregate, err)
	}
	retryID, err := CommunicationV4EventActionID(
		fixture.ProfileID,
		plans[1].ActionKey+"|try2",
	)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.CommunicationV4EventActionByID(retryID)
	if err != nil || retry == nil ||
		retry.Status != CommunicationV4EventActionPlanned ||
		retry.Text != second.Text ||
		retry.SourceOrdinal != second.SourceOrdinal ||
		retry.DependsOnActionID == nil ||
		*retry.DependsOnActionID != first.ActionID {
		t.Fatalf("链中重试行未按原参数铸造: %+v err=%v", retry, err)
	}
	// try2 依赖首气泡正证,但 CAS 锚是前次失败尝试(透明锚);正证后按序物化
	// 第三气泡,其父仍是 plan 记的基础键行(retried),依赖解析沿重试链走到
	// 实际发出的一代。
	effectFixture.Action = *retry
	effectFixture.Now = effectFixture.Now.Add(2 * time.Minute)
	retryReq := communicationV4EventEffectRequest(
		t,
		s,
		effectFixture,
		*retry,
		"cold-midfail-2-try2",
	)
	if retryReq.PreviousIntentID != secondReq.Intent.IntentID {
		t.Fatalf("重试 CAS 锚必须是前次失败 intent: got=%q want=%q",
			retryReq.PreviousIntentID, secondReq.Intent.IntentID)
	}
	retryCreated, err := s.CreateEffectIntentAndCmd(retryReq)
	if err != nil || !retryCreated.Created {
		t.Fatalf("链中 try2 WAL 构造失败: result=%+v err=%v", retryCreated, err)
	}
	settleCommunicationV4EventTextEffect(
		t,
		s,
		effectFixture,
		*retry,
		retryCreated,
		"cold-midfail-2-try2",
	)
	thirdID, err := CommunicationV4EventActionID(fixture.ProfileID, plans[2].ActionKey)
	if err != nil {
		t.Fatal(err)
	}
	third, err := s.CommunicationV4EventActionByID(thirdID)
	if err != nil || third == nil ||
		third.Status != CommunicationV4EventActionPlanned ||
		third.DependsOnActionID == nil ||
		*third.DependsOnActionID != second.ActionID {
		t.Fatalf("try2 正证后未按序物化第三气泡: third=%+v err=%v", third, err)
	}
	effectFixture.Action = *third
	effectFixture.Now = effectFixture.Now.Add(2 * time.Minute)
	thirdReq := communicationV4EventEffectRequest(
		t,
		s,
		effectFixture,
		*third,
		"cold-midfail-3",
	)
	if thirdReq.PreviousIntentID != retryReq.Intent.IntentID {
		t.Fatalf("第三气泡必须钉住实际发出的重试代 intent: got=%q want=%q",
			thirdReq.PreviousIntentID, retryReq.Intent.IntentID)
	}
	thirdCreated, err := s.CreateEffectIntentAndCmd(thirdReq)
	if err != nil || !thirdCreated.Created {
		t.Fatalf("依赖 retried 父项的第三气泡 WAL 未放行: result=%+v err=%v",
			thirdCreated, err)
	}
}

func TestCommunicationV4SchedulePlanRefusesNewPlanWhilePriorPending(t *testing.T) {
	s := openTest(t)
	fixture, req, _ := freezeCommunicationV4ColdWechatPlan(t, s, "cold-gate")
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || aggregate == nil {
		t.Fatalf("读取聚合失败: aggregate=%+v err=%v", aggregate, err)
	}
	state := aggregate.State
	state.ColdWechatTextSent = true
	persistCommunicationV4ScheduleState(t, s, aggregate, state)
	blocked := req
	blocked.EvaluatedAt = req.EvaluatedAt.Add(time.Hour)
	blocked.FrozenAt = blocked.EvaluatedAt.Add(time.Second)
	result, err := s.FreezeCommunicationV4SchedulePlan(blocked)
	if !errors.Is(err, ErrCommunicationV4Conflict) {
		t.Fatalf(
			"前计划动作未终局时新计划必须被拒绝: result=%+v err=%v",
			result,
			err,
		)
	}
}

func TestCommunicationV4SchedulePlanInviteOnlyResumeFreezes(t *testing.T) {
	s := openTest(t)
	fixture, aggregate, revision :=
		seedCommunicationV4SchedulePlanFixture(t, s, "cold-resume", true)
	state := aggregate.State
	state.MainStatus = communication.V4StatusCommunicating
	state.ColdPromptRemaining = 0
	state.ColdWechatRemaining = 1
	state.ColdWechatTextSent = true
	state.WechatState = communication.V4WechatNotInvited
	persistCommunicationV4ScheduleState(t, s, &aggregate, state)
	evaluatedAt := state.LastOutboundAt.Add(25 * time.Hour)
	result, err := s.FreezeCommunicationV4SchedulePlan(
		FreezeCommunicationV4SchedulePlanRequest{
			ProfileID:                   fixture.ProfileID,
			ConversationRef:             fixture.ConversationRef,
			ExpectedRevision:            aggregate.Revision,
			ExpectedProjectedThroughSeq: aggregate.ProjectedThroughSeq,
			ContextRevisionHash:         revision.RevisionHash,
			Reply:                       communication.ReplyAdvice{State: communication.AdviceAbsent},
			EvaluatedAt:                 evaluatedAt,
			FrozenAt:                    evaluatedAt.Add(time.Second),
		},
	)
	if err != nil || result == nil || !result.Created ||
		result.Plan == nil || len(result.Plan.PlannedActions) != 1 ||
		len(result.Actions) != 1 ||
		result.Actions[0].V4Kind != communication.V4ActionColdWechatInvite ||
		result.Actions[0].SourceOrdinal != 0 ||
		result.Actions[0].DependsOnActionID != nil {
		t.Fatalf("正文已发后的邀请续接计划未冻结: result=%+v err=%v", result, err)
	}
}

func TestValidateCommunicationV4ScheduleActionShapes(t *testing.T) {
	evaluatedAt := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	dueAt := evaluatedAt.Add(-time.Minute)
	text := func(key, body string) communication.V4PlannedAction {
		return communication.V4PlannedAction{
			ActionKey: key,
			Kind:      communication.V4ActionColdWechatText,
			Text:      body,
			DueAt:     &dueAt,
		}
	}
	invite := func(key string) communication.V4PlannedAction {
		return communication.V4PlannedAction{
			ActionKey: key,
			Kind:      communication.V4ActionColdWechatInvite,
			DueAt:     &dueAt,
		}
	}
	prompt := communication.V4PlannedAction{
		ActionKey: "k|prompt",
		Kind:      communication.V4ActionColdPrompt,
		Text:      "催一正文",
		Round:     1,
		Stage:     1,
		DueAt:     &dueAt,
	}
	valid := []communication.V4PlannedAction{
		text("k1", "第一句"), text("k2", "第二句"), text("k3", "第三句"), invite("k4"),
	}
	if _, err := validateCommunicationV4ScheduleActions(valid, evaluatedAt); err != nil {
		t.Fatalf("正文×3+邀请必须合法: err=%v", err)
	}
	sixTexts := []communication.V4PlannedAction{
		text("k1", "一"), text("k2", "二"), text("k3", "三"),
		text("k4", "四"), text("k5", "五"), text("k6", "六"), invite("k7"),
	}
	invalid := [][]communication.V4PlannedAction{
		{invite("k1"), text("k2", "颠倒")},
		{text("k1", "缺邀请")},
		{text("k1", "一"), invite("k2"), text("k3", "邀请后还有正文")},
		{prompt, invite("k2")},
		sixTexts,
	}
	for index, actions := range invalid {
		if _, err := validateCommunicationV4ScheduleActions(actions, evaluatedAt); err == nil {
			t.Fatalf("非法形态 %d 被放行: %+v", index, actions)
		}
	}
}

func TestCommunicationV4SchedulePlanFreezesOneInterviewFollowup(t *testing.T) {
	s := openTest(t)
	fixture, aggregate, revision :=
		seedCommunicationV4SchedulePlanFixture(t, s, "interview", false)
	state := aggregate.State
	state.MainStatus = communication.V4StatusInvited
	state.ColdPromptRemaining = 0
	state.ColdWechatRemaining = 0
	state.InterviewGroups = []communication.V4InterviewFollowupGroup{{
		MessageSeq: 9,
		NextStage:  1,
		Active:     true,
	}}
	persistCommunicationV4ScheduleState(t, s, &aggregate, state)
	evaluatedAt := state.LastOutboundAt.Add(11 * time.Minute)
	result, err := s.FreezeCommunicationV4SchedulePlan(
		FreezeCommunicationV4SchedulePlanRequest{
			ProfileID:                   fixture.ProfileID,
			ConversationRef:             fixture.ConversationRef,
			ExpectedRevision:            aggregate.Revision,
			ExpectedProjectedThroughSeq: aggregate.ProjectedThroughSeq,
			ContextRevisionHash:         revision.RevisionHash,
			Reply:                       communication.ReplyAdvice{State: communication.AdviceAbsent},
			InterviewFollowupTexts:      map[uint8]string{1: "{称呼}您好，请确认面试安排"},
			EvaluatedAt:                 evaluatedAt,
			FrozenAt:                    evaluatedAt.Add(time.Second),
		},
	)
	if err != nil || result == nil || !result.Created ||
		result.Plan == nil || len(result.Plan.PlannedActions) != 1 ||
		len(result.Actions) != 1 ||
		result.Actions[0].V4Kind != communication.V4ActionInterviewFollowup ||
		result.Actions[0].CardMessageSeq != 9 ||
		result.Actions[0].SourceOrdinal != 0 ||
		strings.Contains(result.Actions[0].Text, "{称呼}") {
		t.Fatalf("邀面跟催未冻结为单条已渲染动作: result=%+v err=%v", result, err)
	}
}

func TestCommunicationV4ScheduleAIInvocationPersistsOnceAndReplays(
	t *testing.T,
) {
	s := openTest(t)
	fixture, aggregate, revision :=
		seedCommunicationV4SchedulePlanFixture(t, s, "silence-ai", false)
	state := aggregate.State
	state.MainStatus = communication.V4StatusCommunicating
	state.ColdPromptRemaining = 2
	state.LastColdPromptRound = 0
	state.RealMessageRound = 1
	persistCommunicationV4ScheduleState(t, s, &aggregate, state)
	material, ready, err := s.CommunicationAIMaterialForProfile(fixture.ProfileID)
	if err != nil || !ready {
		t.Fatalf("沉默建议材料不可用: ready=%v err=%v", ready, err)
	}
	evaluatedAt := state.LastOutboundAt.Add(25 * time.Hour)
	req := ReserveCommunicationV4ScheduleAIInvocationRequest{
		ProfileID:                   fixture.ProfileID,
		ConversationRef:             fixture.ConversationRef,
		ExpectedRevision:            aggregate.Revision,
		ExpectedProjectedThroughSeq: aggregate.ProjectedThroughSeq,
		ContextRevisionHash:         revision.RevisionHash,
		ResumeSnapshotID:            material.ResumeSnapshot.SnapshotID,
		EvaluatedAt:                 evaluatedAt,
		Provider:                    "fixture-provider",
		Model:                       "fixture-model",
		InputHash:                   strings.Repeat("a", 64),
		CreatedAt:                   evaluatedAt,
	}
	first, err := s.ReserveCommunicationV4ScheduleAIInvocation(req)
	if err != nil || first == nil || !first.Created ||
		first.Invocation.FinishedAt != nil ||
		first.Invocation.Purpose != m5ai.PurposeSilenceFollowup ||
		first.Invocation.AdviceKey == "" {
		t.Fatalf("沉默建议未持久预留: result=%+v err=%v", first, err)
	}
	replayed, err := s.ReserveCommunicationV4ScheduleAIInvocation(req)
	if err != nil || replayed == nil || replayed.Created ||
		replayed.Invocation.InvocationID != first.Invocation.InvocationID {
		t.Fatalf("沉默建议预留发生增生: result=%+v err=%v", replayed, err)
	}

	zero := 0
	completion := AIInvocationCompletion{
		InvocationID:          first.Invocation.InvocationID,
		Status:                AIInvocationOK,
		OutputHash:            strings.Repeat("b", 64),
		InputTokens:           20,
		CachedInputTokens:     2,
		OutputTokens:          5,
		ReasoningTokens:       &zero,
		UsageShape:            AIInvocationUsageComplete,
		ReasoningContentEmpty: true,
		LatencyMs:             20,
		TraceStatus:           m5ai.TraceStatusComplete,
		FinishedAt:            evaluatedAt.Add(time.Second),
	}
	finished, err := s.CompleteCommunicationV4ScheduleAIInvocation(
		CompleteCommunicationV4ScheduleAIInvocationRequest{
			Completion:     completion,
			SuggestionText: "合成沉默追问",
		},
	)
	if err != nil || finished == nil ||
		finished.Status != AIInvocationOK ||
		finished.SuggestionText != "合成沉默追问" ||
		finished.FinishedAt == nil {
		t.Fatalf("沉默建议终局未保存: invocation=%+v err=%v", finished, err)
	}
	finishedReplay, err := s.CompleteCommunicationV4ScheduleAIInvocation(
		CompleteCommunicationV4ScheduleAIInvocationRequest{
			Completion:     completion,
			SuggestionText: "合成沉默追问",
		},
	)
	if err != nil || finishedReplay == nil ||
		finishedReplay.InvocationID != finished.InvocationID {
		t.Fatalf("沉默建议终局不能幂等重放: invocation=%+v err=%v",
			finishedReplay, err)
	}
}

// 预留身份只认 AdviceKey/ProfileID/Purpose。职位配置换代、模型切换、聚合推进、
// 简历重采都会改动上下文列，但它们是记录不是身份——2026-08-01 事故正是因为把
// 这些会变的上下文当成了身份，配置一换代就把 79 个尚未发出追问的候选人判成
// 冲突、隔离会话并冻结档案。
func TestScheduleAIReservationIdentityIgnoresMutableContext(t *testing.T) {
	profileID := "p-schedule-ai-identity"
	adviceKey := profileID + "|schedule-advice|silenceFollowup|round:1|stage:0"
	base := CommunicationV4ScheduleAIInvocation{
		InvocationID: communicationV4ScheduleAIInvocationID(
			profileID, adviceKey,
		),
		AdviceKey:                adviceKey,
		ProfileID:                profileID,
		ConversationRef:          "conv-schedule-ai-identity",
		BasisRevision:            3,
		BasisProjectedThroughSeq: 5,
		ContextRevisionHash:      strings.Repeat("a", 64),
		ResumeSnapshotID:         "snap-old",
		EvaluatedAt:              time.Unix(1_700_000_000, 0).UTC(),
		Purpose:                  m5ai.PurposeSilenceFollowup,
		Attempt:                  1,
		Provider:                 "old-provider",
		Model:                    "old-model",
		InputHash:                strings.Repeat("b", 64),
		// 未终局形态：身份判定与完成状态无关，取最简合法记录即可。
		Status:    AIInvocationTransportFailed,
		CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
	}
	if !sameCommunicationV4ScheduleAIReservation(base, base) {
		t.Fatal("同一条预留未被判为同一件事")
	}

	for _, tc := range []struct {
		name   string
		mutate func(*CommunicationV4ScheduleAIInvocation)
	}{
		{"职位配置换代", func(v *CommunicationV4ScheduleAIInvocation) {
			v.ContextRevisionHash = strings.Repeat("c", 64)
		}},
		{"prompt 变化导致输入哈希变化", func(v *CommunicationV4ScheduleAIInvocation) {
			v.InputHash = strings.Repeat("d", 64)
		}},
		{"切换 provider 与 model", func(v *CommunicationV4ScheduleAIInvocation) {
			v.Provider = "new-provider"
			v.Model = "new-model"
		}},
		{"聚合版本推进", func(v *CommunicationV4ScheduleAIInvocation) {
			v.BasisRevision = 9
		}},
		{"投影游标推进", func(v *CommunicationV4ScheduleAIInvocation) {
			v.BasisProjectedThroughSeq = 42
		}},
		{"简历重采", func(v *CommunicationV4ScheduleAIInvocation) {
			v.ResumeSnapshotID = "snap-new"
		}},
		{"上下文整体换新", func(v *CommunicationV4ScheduleAIInvocation) {
			v.ContextRevisionHash = strings.Repeat("e", 64)
			v.InputHash = strings.Repeat("f", 64)
			v.Provider = "another-provider"
			v.Model = "another-model"
			v.BasisRevision = 11
			v.BasisProjectedThroughSeq = 77
			v.ResumeSnapshotID = "snap-latest"
		}},
	} {
		t.Run(tc.name+"仍是同一件事", func(t *testing.T) {
			wanted := base
			tc.mutate(&wanted)
			if !sameCommunicationV4ScheduleAIReservation(base, wanted) {
				t.Fatalf("上下文变化被误判为不同预留: %+v", wanted)
			}
		})
	}

	for _, tc := range []struct {
		name   string
		mutate func(*CommunicationV4ScheduleAIInvocation)
	}{
		{"AdviceKey 不同", func(v *CommunicationV4ScheduleAIInvocation) {
			v.AdviceKey = v.AdviceKey + "|round:2"
		}},
		{"ProfileID 串档", func(v *CommunicationV4ScheduleAIInvocation) {
			v.ProfileID = "p-other-candidate"
		}},
		{"用途不同", func(v *CommunicationV4ScheduleAIInvocation) {
			v.Purpose = m5ai.PurposeScoring
		}},
	} {
		t.Run(tc.name+"必须拒绝", func(t *testing.T) {
			wanted := base
			tc.mutate(&wanted)
			if sameCommunicationV4ScheduleAIReservation(base, wanted) {
				t.Fatalf("身份错乱未被拒绝: %+v", wanted)
			}
		})
	}

	t.Run("既有记录本身不合法必须拒绝", func(t *testing.T) {
		broken := base
		broken.InvocationID = "mismatched-id"
		if sameCommunicationV4ScheduleAIReservation(broken, base) {
			t.Fatal("非法既有记录被当成可复用预留")
		}
	})
}
