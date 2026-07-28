package store

import (
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/contract/gen/go/protocol"
)

func TestCommunicationActionRetryIdentity(t *testing.T) {
	base := "turn-abc|interviewInvite"
	if communicationActionPlanKey(base) != base {
		t.Fatalf("非重试 ID 不得被改写: %s", communicationActionPlanKey(base))
	}
	if communicationActionPlanKey(base+"|try2") != base ||
		communicationActionPlanKey(base+"|try13") != base {
		t.Fatal("planKey 未剥离重试后缀")
	}
	if communicationActionNextRetryID(base) != base+"|try2" {
		t.Fatalf("首次重试应为 try2: %s", communicationActionNextRetryID(base))
	}
	if communicationActionNextRetryID(base+"|try2") != base+"|try3" ||
		communicationActionNextRetryID(base+"|try9") != base+"|try10" {
		t.Fatal("重试序号未递增")
	}
	if IsRetryCommunicationActionID(base) || !IsRetryCommunicationActionID(base+"|try2") {
		t.Fatal("重试后缀判定错误")
	}
}

type interviewRetryHarness struct {
	fixture  communicationV4AutomaticEffectFixture
	startsAt int64
	endsAt   int64
	card     CommunicationAction
	textReq  CreateEffectIntentRequest
}

// seedInterviewCardAfterText 复用既有 fixture:注入邀面 plan、完成正文正证,
// 返回实体化的邀面动作。
func seedInterviewCardAfterText(
	t *testing.T,
	s *Store,
	suffix string,
	startsAt int64,
) interviewRetryHarness {
	t.Helper()
	fixture := seedPlannedCommunicationV4AutomaticAction(t, s, suffix)
	endsAt := startsAt + int64((30*time.Minute)/time.Millisecond)
	method := "wechatVideo"
	var advice CommunicationV4ProjectionApplication
	if err := s.db.First(
		&advice,
		"profile_id = ? AND input_kind = ? AND input_key = ?",
		fixture.ProfileID,
		CommunicationV4InputDialogueAdvice,
		communicationV4DialogueAdviceKey(fixture.Turn.TurnID, m5ai.PurposeReply),
	).Error; err != nil {
		t.Fatal(err)
	}
	advice.Outcome.PlannedActions = append(
		advice.Outcome.PlannedActions,
		communication.V4PlannedAction{
			ActionKey:           fixture.Turn.TurnID + "|interviewInvite",
			Kind:                communication.V4ActionInterviewInvite,
			InterviewStartsAtMs: &startsAt,
			InterviewEndsAtMs:   &endsAt,
			InterviewMethod:     &method,
		},
	)
	if err := s.db.Save(&advice).Error; err != nil {
		t.Fatal(err)
	}
	textReq := confirmCommunicationV4TextEffect(t, s, fixture, suffix+"-text")
	actions, err := s.CommunicationActionsByTurn(fixture.Turn.TurnID)
	if err != nil || len(actions) != 2 ||
		actions[1].Kind != CommunicationActionInterviewInvite {
		t.Fatalf("邀面 action 未实体化: actions=%+v err=%v", actions, err)
	}
	return interviewRetryHarness{
		fixture: fixture, startsAt: startsAt, endsAt: endsAt,
		card: actions[1], textReq: textReq,
	}
}

// bindInterviewCard 按巡检语义把邀面动作绑定为一个新 effect intent。
func bindInterviewCard(
	t *testing.T,
	s *Store,
	h interviewRetryHarness,
	actionID string,
	previousIntentID string,
	suffix string,
	at time.Time,
) *CreateEffectIntentResult {
	t.Helper()
	intentID, err := M5AutomaticIntentID(actionID)
	if err != nil {
		t.Fatal(err)
	}
	args, err := protocol.Encode(protocol.ChatSendInviteCardArgs{
		ConversationRef: h.fixture.ConversationRef,
		Interview: protocol.InterviewDetails{
			StartsAt: h.startsAt,
			EndsAt:   h.endsAt,
			Method:   protocol.InterviewMethodWechatVideo,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateEffectIntentAndCmd(CreateEffectIntentRequest{
		Intent: EffectIntent{
			IntentID:        intentID,
			IdemKey:         "idem-" + suffix,
			Platform:        h.fixture.Platform,
			AccountRef:      h.fixture.AccountRef,
			Primitive:       primitiveChatSendInviteCard,
			TargetRef:       h.fixture.ConversationRef,
			PayloadHash:     "payload-" + suffix,
			GuardsHash:      "guards-" + suffix,
			Status:          EffectIntentDispatching,
			DeadlineMs:      at.Add(time.Hour).UnixMilli(),
			SendFingerprint: h.card.ContentHash,
		},
		Command: CmdRecord{
			MsgID:                        "msg-" + suffix,
			Name:                         primitiveChatSendInviteCard,
			Class:                        "effectful",
			IdemKey:                      "idem-" + suffix,
			Domain:                       h.fixture.Platform + ":" + h.fixture.AccountRef,
			Platform:                     h.fixture.Platform,
			AccountRef:                   h.fixture.AccountRef,
			ExpectedPrincipalFingerprint: h.fixture.Principal,
			IntentID:                     intentID,
			HandID:                       h.fixture.HandID,
			Session:                      h.fixture.Session,
			BootIDAtDispatch:             h.fixture.BootID,
			Args:                         string(args),
			Status:                       CmdQueued,
			DeadlineMs:                   at.Add(time.Hour).UnixMilli(),
			ExecBudgetMs:                 120_000,
		},
		ExpectedTailSeq:   h.fixture.Turn.InboundThroughSeq + 1,
		PreviousIntentID:  previousIntentID,
		AutomaticActionID: actionID,
		Now:               at,
	})
	if err != nil || !created.Created {
		t.Fatalf("邀面 intent 绑定失败(%s): result=%+v err=%v", suffix, created, err)
	}
	return created
}

func failInterviewCard(
	t *testing.T,
	s *Store,
	created *CreateEffectIntentResult,
	suffix string,
	at time.Time,
) {
	t.Helper()
	if _, err := s.ApplyResultMessage(
		created.Command.MsgID,
		"result-"+suffix,
		"result",
		created.Command.HandID,
		func(cmd *CmdRecord) (ResultCommandMutation, error) {
			cmd.Status = CmdFailed
			cmd.SideEffect = "none"
			cmd.TerminalAt = &at
			return ResultCommandMutation{Save: true, Effect: &EffectResultMutation{
				IntentStatus: EffectIntentFailed, Reason: "failedNone",
			}}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
}

func TestCommunicationV4InterviewCardCleanFailureAutoRetriesUnbounded(t *testing.T) {
	s := openTest(t)
	startsAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Minute).UnixMilli()
	h := seedInterviewCardAfterText(t, s, "interview-retry", startsAt)

	firstAt := h.fixture.Now.Add(2 * time.Minute)
	first := bindInterviewCard(
		t, s, h, h.card.ActionID, h.textReq.Intent.IntentID, "interview-retry-1", firstAt,
	)
	failInterviewCard(t, s, first, "interview-retry-1", firstAt.Add(time.Minute))

	retryID := h.card.ActionID + "|try2"
	original, err := s.CommunicationActionByID(h.card.ActionID)
	retry, retryErr := s.CommunicationActionByID(retryID)
	turn, turnErr := s.DialogueTurnByID(h.fixture.Turn.TurnID)
	aggregate, aggregateErr := s.CommunicationV4AggregateByProfile(h.fixture.ProfileID)
	if err != nil || original == nil || original.Status != CommunicationActionRetried ||
		original.FailureReason != "effectFailed" {
		t.Fatalf("原动作未标 retried 留档: %+v err=%v", original, err)
	}
	if retryErr != nil || retry == nil || retry.Status != CommunicationActionPlanned ||
		retry.Kind != CommunicationActionInterviewInvite ||
		retry.DependsOnActionID == nil || *retry.DependsOnActionID != *h.card.DependsOnActionID ||
		retry.ContentHash != h.card.ContentHash ||
		retry.InterviewStartsAtMs == nil || *retry.InterviewStartsAtMs != h.startsAt {
		t.Fatalf("重试动作未按原参数铸造: %+v err=%v", retry, retryErr)
	}
	if turnErr != nil || turn == nil || turn.Status != DialogueTurnAdviceReady ||
		turn.FailureReason != "" {
		t.Fatalf("turn 未复位 adviceReady: %+v err=%v", turn, turnErr)
	}
	if aggregateErr != nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive {
		t.Fatalf("干净失败不得冻结档案自动化: %+v err=%v", aggregate, aggregateErr)
	}
	var retryAudits int64
	if err := s.db.Model(&AuditEntry{}).
		Where("category = ?", "interview_invite_auto_retry").
		Count(&retryAudits).Error; err != nil || retryAudits != 1 {
		t.Fatalf("重试必须落审计: count=%d err=%v", retryAudits, err)
	}

	// 第二次尝试:CAS 锚为前次失败 intent(巡检语义),透明锚放行绑定;再次
	// 干净失败必须继续铸 try3——无限重试,不设上限。
	secondAt := firstAt.Add(5 * time.Minute)
	second := bindInterviewCard(
		t, s, h, retryID, first.Intent.IntentID, "interview-retry-2", secondAt,
	)
	failInterviewCard(t, s, second, "interview-retry-2", secondAt.Add(time.Minute))
	try3, err := s.CommunicationActionByID(h.card.ActionID + "|try3")
	if err != nil || try3 == nil || try3.Status != CommunicationActionPlanned {
		t.Fatalf("第二次失败未铸 try3: %+v err=%v", try3, err)
	}

	// 第三次尝试成功:重试动作必须走完整成功链(sent/turn 完成/主线已约面)。
	thirdAt := secondAt.Add(5 * time.Minute)
	third := bindInterviewCard(
		t, s, h, h.card.ActionID+"|try3", second.Intent.IntentID, "interview-retry-3", thirdAt,
	)
	method := "wechatVideo"
	resultAt := thirdAt.Add(time.Minute)
	if _, err := s.ApplyResultMessage(
		third.Command.MsgID,
		"result-interview-retry-3",
		"result",
		h.fixture.HandID,
		func(cmd *CmdRecord) (ResultCommandMutation, error) {
			cmd.Status = CmdOk
			cmd.TerminalAt = &resultAt
			return ResultCommandMutation{
				Save: true,
				Effect: &EffectResultMutation{
					IntentStatus: EffectIntentOk,
					ContentHash:  h.card.ContentHash,
					Card: &CardResultMutation{
						ConversationRef:     h.fixture.ConversationRef,
						CardType:            "interviewInvite",
						CardState:           "unknown",
						ContentHash:         h.card.ContentHash,
						SourceKey:           strings.Repeat("c", 64),
						InterviewStartsAtMs: &h.startsAt,
						InterviewEndsAtMs:   &h.endsAt,
						InterviewMethod:     &method,
					},
				},
			}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	settled, err := s.CommunicationActionByID(h.card.ActionID + "|try3")
	finalTurn, turnErr := s.DialogueTurnByID(h.fixture.Turn.TurnID)
	finalAggregate, aggregateErr := s.CommunicationV4AggregateByProfile(h.fixture.ProfileID)
	if err != nil || settled == nil || settled.Status != CommunicationActionSent ||
		turnErr != nil || finalTurn == nil || finalTurn.Status != DialogueTurnCompleted ||
		aggregateErr != nil ||
		finalAggregate.State.MainStatus != communication.V4StatusInvited {
		t.Fatalf(
			"重试成功链未完成: action=%+v turn=%+v aggregate=%+v errs=%v/%v/%v",
			settled, finalTurn, finalAggregate, err, turnErr, aggregateErr,
		)
	}
}

func TestCommunicationV4InterviewCardRetryStopsWhenStartsAtElapsed(t *testing.T) {
	s := openTest(t)
	// 面试开始时间已在失败入账时刻(真实时钟)之前:目标不可达,照旧转人工终局。
	startsAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Minute).UnixMilli()
	h := seedInterviewCardAfterText(t, s, "interview-elapsed", startsAt)
	bindAt := h.fixture.Now.Add(2 * time.Minute)
	created := bindInterviewCard(
		t, s, h, h.card.ActionID, h.textReq.Intent.IntentID, "interview-elapsed-1", bindAt,
	)
	failInterviewCard(
		t, s, created, "interview-elapsed-1",
		bindAt.Add(time.Minute),
	)
	action, err := s.CommunicationActionByID(h.card.ActionID)
	turn, turnErr := s.DialogueTurnByID(h.fixture.Turn.TurnID)
	aggregate, aggregateErr := s.CommunicationV4AggregateByProfile(h.fixture.ProfileID)
	if err != nil || action == nil || action.Status != CommunicationActionManualRequired {
		t.Fatalf("到期动作应转人工: %+v err=%v", action, err)
	}
	if retry, retryErr := s.CommunicationActionByID(h.card.ActionID + "|try2"); retryErr == nil && retry != nil {
		t.Fatalf("到期动作不得铸重试: %+v", retry)
	}
	if turnErr != nil || turn == nil || turn.Status != DialogueTurnManualRequired ||
		aggregateErr != nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired {
		t.Fatalf("到期失败未按原语义收敛: turn=%+v aggregate=%+v", turn, aggregate)
	}
	var abandoned int64
	if err := s.db.Model(&AuditEntry{}).
		Where("category = ?", "interview_invite_retry_abandoned").
		Count(&abandoned).Error; err != nil || abandoned != 1 {
		t.Fatalf("到期放弃必须落审计: count=%d err=%v", abandoned, err)
	}
}

func TestCommunicationV4InterviewCardSuspectNeverAutoRetries(t *testing.T) {
	s := openTest(t)
	startsAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Minute).UnixMilli()
	h := seedInterviewCardAfterText(t, s, "interview-suspect", startsAt)
	bindAt := h.fixture.Now.Add(2 * time.Minute)
	created := bindInterviewCard(
		t, s, h, h.card.ActionID, h.textReq.Intent.IntentID, "interview-suspect-1", bindAt,
	)
	resultAt := bindAt.Add(time.Minute)
	if _, err := s.ApplyResultMessage(
		created.Command.MsgID,
		"result-interview-suspect-1",
		"result",
		h.fixture.HandID,
		func(cmd *CmdRecord) (ResultCommandMutation, error) {
			cmd.Status = CmdSuspect
			cmd.SideEffect = "possible"
			cmd.SuspectReason = "result.sideEffect=possible"
			return ResultCommandMutation{Save: true, Effect: &EffectResultMutation{
				IntentStatus: EffectIntentSuspect, Reason: "sideEffectPossible",
			}}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	_ = resultAt
	action, err := s.CommunicationActionByID(h.card.ActionID)
	if err != nil || action == nil || action.Status != CommunicationActionManualRequired ||
		action.FailureReason != "effectSuspect" {
		t.Fatalf("suspect 必须转人工不得重试: %+v err=%v", action, err)
	}
	if retry, retryErr := s.CommunicationActionByID(h.card.ActionID + "|try2"); retryErr == nil && retry != nil {
		t.Fatalf("suspect 不得铸重试动作: %+v", retry)
	}
}
