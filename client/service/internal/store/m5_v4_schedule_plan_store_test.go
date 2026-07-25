package store

import (
	"encoding/json"
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
	fixture := seedReadyCommunicationTarget(
		t,
		s,
		"profile-v4-schedule-plan-"+suffix,
	)
	material, ready, err := s.CommunicationAIMaterialForProfile(fixture.ProfileID)
	if err != nil || !ready {
		t.Fatalf("读取时刻表配置失败: ready=%v err=%v", ready, err)
	}
	if coldWechat {
		var revision JobAIContextRevision
		if err := s.db.First(
			&revision,
			"revision_hash = ?",
			material.ContextRevision.RevisionHash,
		).Error; err != nil {
			t.Fatal(err)
		}
		revision.SourcePackage.Documents = append(
			revision.SourcePackage.Documents,
			m5ai.JobConfigDocument{
				DocType: "固定话术",
				Content: `{
					"silence48Wechat":{
						"message":"{称呼}您好，还方便继续沟通吗",
						"messages":["{称呼}您好，还方便继续沟通吗"],
						"actions":[],
						"enabled":true
					}
				}`,
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
	fixture, aggregate, revision :=
		seedCommunicationV4SchedulePlanFixture(t, s, suffix, true)
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
		result.Plan == nil || len(result.Plan.PlannedActions) != 2 ||
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
