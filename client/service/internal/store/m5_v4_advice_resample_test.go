package store

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/textcanon"
)

// 本文件覆盖 2026-08-02 裁决在结算层的落地:规格 v4 §五的"输出契约非法/业务
// 前置不满足→本轮不创建任何动作、跳过该候选人、下轮巡检重试"由
// completeCommunicationV4ReplyTx 的重采信号实现,§一的同轮 5 次梯子耗尽后
// 才按现状停靠。

func freezeV4MeetingAdviceTurn(
	t *testing.T,
	s *Store,
	fixture resumeStoreFixture,
) *FreezeCommunicationV4TurnResult {
	t.Helper()
	text := "合成普通回复"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text",
		ContentHash: "v4-advice-resample-2", Text: &text,
	})
	request := communicationV4TurnRequest(t, s, fixture, inbound)
	var err error
	request.RecommendedTimeText, err = m5ai.FreezeRecommendedTimeText(
		time.Date(2026, 7, 10, 14, 23, 0, 0, time.FixedZone("CST", 8*60*60)),
		[]string{"2026-07-14 09:00:00", "2026-07-14 14:00:00"},
	)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := s.FreezeCommunicationV4Turn(request)
	if err != nil {
		t.Fatal(err)
	}
	return frozen
}

func classifyV4TurnInterested(t *testing.T, s *Store, turnID, idPrefix string) {
	t.Helper()
	intentID := "invocation-" + idPrefix + "-intent"
	if reserved, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: intentID, TurnID: turnID,
		Purpose: m5ai.PurposeIntent, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro",
		InputHash: "input-" + idPrefix + "-intent",
	}); err != nil || !reserved.Created {
		t.Fatalf("意向调用未预留: result=%+v err=%v", reserved, err)
	}
	if _, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
		Completion: successfulInvocationCompletion(
			intentID,
			time.Now().UTC().Truncate(time.Millisecond),
		),
		Label: m5ai.IntentInterested, Source: DialogueIntentLLM,
	}); err != nil {
		t.Fatal(err)
	}
}

func reserveV4ReplyAttempt(
	t *testing.T,
	s *Store,
	turnID string,
	idPrefix string,
	attempt int,
) string {
	t.Helper()
	invocationID := fmt.Sprintf("invocation-%s-reply-%d", idPrefix, attempt)
	reserved, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: invocationID, TurnID: turnID,
		Purpose: m5ai.PurposeReply, Attempt: attempt,
		Provider: "deepseek", Model: "deepseek-v4-pro",
		InputHash: "input-" + idPrefix + "-reply",
	})
	if err != nil || !reserved.Created {
		t.Fatalf("回复调用 attempt=%d 未预留: result=%+v err=%v", attempt, reserved, err)
	}
	return invocationID
}

func assertV4TurnLeftForResample(
	t *testing.T,
	s *Store,
	fixture resumeStoreFixture,
	turnID string,
	wantApplications int64,
	wantRevision uint64,
) {
	t.Helper()
	turn, err := s.DialogueTurnByID(turnID)
	if err != nil || turn == nil ||
		turn.Status != DialogueTurnClassified || turn.FailureReason != "" {
		t.Fatalf("重采样本不得留下轮终局: turn=%+v err=%v", turn, err)
	}
	actions, err := s.CommunicationActionsByTurn(turnID)
	if err != nil || len(actions) != 0 {
		t.Fatalf("重采样本不得留下动作: actions=%+v err=%v", actions, err)
	}
	var applications int64
	if err := s.db.Model(&CommunicationV4ProjectionApplication{}).
		Where("profile_id = ?", fixture.ProfileID).
		Count(&applications).Error; err != nil || applications != wantApplications {
		t.Fatalf("重采样本不得留下建议回执: count=%d want=%d err=%v",
			applications, wantApplications, err)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive ||
		aggregate.ManualReason != "" || aggregate.Revision != wantRevision {
		t.Fatalf("重采不得触碰聚合: aggregate=%+v err=%v", aggregate, err)
	}
}

func TestCommunicationV4InvalidMeetingAdviceSchedulesResampleThenNextAttemptPlansAction(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-advice-resample")
	frozen := freezeV4MeetingAdviceTurn(t, s, fixture)
	classifyV4TurnInterested(t, s, frozen.Turn.TurnID, "v4-resample")

	replyText := "那我们约在这个时间视频面试。"
	firstID := reserveV4ReplyAttempt(t, s, frozen.Turn.TurnID, "v4-resample", 1)
	completion := successfulInvocationCompletion(
		firstID,
		time.Now().UTC().Truncate(time.Millisecond),
	)
	invalid := CompleteReplyInvocationRequest{
		Completion: completion, ActionID: "caller-action-id-is-not-authoritative",
		Text: replyText, Action: m5ai.ReplyActionStartOnlineMeeting,
		// 7月15日不在冻结时段列表(7月14日两档)里:真语义越界,不是 b809c20
		// 已修的同时刻写法差异。
		MeetingTime: "7月15日14:00",
		ContentHash: textcanon.Hash(replyText), PlannedAt: completion.FinishedAt,
	}
	action, err := s.CompleteReplyInvocation(invalid)
	var resample *AIAdviceResampleScheduledError
	if action != nil || !errors.As(err, &resample) ||
		resample.TurnID != frozen.Turn.TurnID ||
		resample.Reason != string(communication.V4ManualReplyInvalid) ||
		resample.Attempt != 1 {
		t.Fatalf("越界邀面建议未安排重采: action=%+v err=%v", action, err)
	}
	assertV4TurnLeftForResample(t, s, fixture, frozen.Turn.TurnID, 2, 2)
	var invocation AIInvocation
	if err := s.db.First(&invocation, "invocation_id = ?", firstID).Error; err != nil ||
		invocation.FinishedAt == nil || invocation.Status != AIInvocationOK {
		t.Fatalf("重采样本的 invocation 必须落终局: invocation=%+v err=%v", invocation, err)
	}

	// 同 completion 重放仍是重采信号,不产生任何增生。
	action, err = s.CompleteReplyInvocation(invalid)
	if action != nil || !errors.As(err, &resample) || resample.Attempt != 1 {
		t.Fatalf("重采信号重放不幂等: action=%+v err=%v", action, err)
	}
	assertV4TurnLeftForResample(t, s, fixture, frozen.Turn.TurnID, 2, 2)

	// 下一次采样输出合法建议:动作照常物化,head 链校验通过。
	secondID := reserveV4ReplyAttempt(t, s, frozen.Turn.TurnID, "v4-resample", 2)
	validCompletion := successfulInvocationCompletion(
		secondID,
		time.Now().UTC().Truncate(time.Millisecond).Add(time.Second),
	)
	valid := invalid
	valid.Completion = validCompletion
	valid.MeetingTime = "7月14日14:00"
	valid.PlannedAt = validCompletion.FinishedAt
	action, err = s.CompleteReplyInvocation(valid)
	if err != nil || action == nil ||
		action.ActionID != frozen.Turn.TurnID+"|replyText" ||
		action.Status != CommunicationActionPlanned {
		t.Fatalf("重采后的合法建议未物化正文动作: action=%+v err=%v", action, err)
	}
	turn, err := s.DialogueTurnByID(frozen.Turn.TurnID)
	if err != nil || turn == nil || turn.Status != DialogueTurnAdviceReady {
		t.Fatalf("重采后轮未进入 adviceReady: turn=%+v err=%v", turn, err)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || aggregate.Revision != 3 ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive {
		t.Fatalf("重采后聚合未按单次建议推进: aggregate=%+v err=%v", aggregate, err)
	}
	current, err := s.RecheckDialogueTurnCurrent(frozen.Turn.TurnID, time.Now())
	if err != nil || !current {
		t.Fatalf("重采后完成的轮 head 链校验未通过: current=%v err=%v", current, err)
	}
}

func TestCommunicationV4InvalidAdviceExhaustsLadderThenDocksWithoutOwnerFreeze(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-advice-exhaust")
	frozen := freezeV4MeetingAdviceTurn(t, s, fixture)
	classifyV4TurnInterested(t, s, frozen.Turn.TurnID, "v4-exhaust")

	replyText := "那我们约在这个时间视频面试。"
	for attempt := 1; attempt <= MaxAIInvocationAttempts; attempt++ {
		invocationID := reserveV4ReplyAttempt(t, s, frozen.Turn.TurnID, "v4-exhaust", attempt)
		completion := successfulInvocationCompletion(
			invocationID,
			time.Now().UTC().Truncate(time.Millisecond).Add(time.Duration(attempt)*time.Second),
		)
		action, err := s.CompleteReplyInvocation(CompleteReplyInvocationRequest{
			Completion: completion, ActionID: "caller-action-id-is-not-authoritative",
			Text: replyText, Action: m5ai.ReplyActionStartOnlineMeeting,
			MeetingTime: "7月15日14:00",
			ContentHash: textcanon.Hash(replyText), PlannedAt: completion.FinishedAt,
		})
		if attempt < MaxAIInvocationAttempts {
			var resample *AIAdviceResampleScheduledError
			if action != nil || !errors.As(err, &resample) || resample.Attempt != attempt {
				t.Fatalf("attempt=%d 未安排重采: action=%+v err=%v", attempt, action, err)
			}
			assertV4TurnLeftForResample(t, s, fixture, frozen.Turn.TurnID, 2, 2)
			continue
		}
		// 第 5 次仍非法:梯子耗尽,按现状停靠 manualRequired(replyInvalid)。
		if err != nil || action != nil {
			t.Fatalf("梯子耗尽必须零动作停靠: action=%+v err=%v", action, err)
		}
	}
	turn, err := s.DialogueTurnByID(frozen.Turn.TurnID)
	if err != nil || turn == nil ||
		turn.Status != DialogueTurnManualRequired ||
		turn.FailureReason != string(communication.V4ManualReplyInvalid) {
		t.Fatalf("梯子耗尽未停靠 replyInvalid: turn=%+v err=%v", turn, err)
	}
	// replyInvalid 属聚合冻结豁免族:轮停靠但候选人不冻结,时刻表照跑。
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive ||
		aggregate.ManualReason != "" || aggregate.Revision != 3 {
		t.Fatalf("停靠不得冻结聚合: aggregate=%+v err=%v", aggregate, err)
	}
	actions, err := s.CommunicationActionsByTurn(frozen.Turn.TurnID)
	if err != nil || len(actions) != 0 {
		t.Fatalf("停靠轮不得留下动作: actions=%+v err=%v", actions, err)
	}
}

func TestCommunicationV4WechatInviteAdviceAfterExchangeSchedulesResample(t *testing.T) {
	s := openTest(t)
	profileID := "profile-v4-advice-wechat-resample"
	fixture := seedReadyCommunicationTarget(t, s, profileID)
	setCommunicationV4FixedPhrasePackage(t, s, "revision-"+profileID)
	text := "加好了,后面微信聊"
	inbound := appendCommunicationV4Inbound(t, s, fixture,
		Message{
			Seq: 2, Direction: "in", Kind: "card", CardType: "wechatExchange",
			CardState: "accepted", ContentHash: "v4-wechat-resample-card",
		},
		Message{
			Seq: 3, Direction: "in", Kind: "text", Text: &text,
			ContentHash: "v4-wechat-resample-text",
		},
	)
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil || frozen.Turn.Status != DialogueTurnClassified ||
		frozen.Aggregate.State.WechatState != communication.V4WechatExchanged {
		t.Fatalf("交换成功混合轮未冻结: frozen=%+v err=%v", frozen, err)
	}

	// 业务前置不满足族:微信线已推进到已交换,建议"发起换微信邀请"是合法枚举
	// 但前置不满足(规格 §五),同样是样本作废、下轮重采,不是第 1 次停靠。
	invocationID := reserveV4ReplyAttempt(t, s, frozen.Turn.TurnID, "v4-wechat-resample", 1)
	completion := successfulInvocationCompletion(
		invocationID,
		time.Now().UTC().Truncate(time.Millisecond),
	)
	replyText := "好的,那微信上聊。"
	action, err := s.CompleteReplyInvocation(CompleteReplyInvocationRequest{
		Completion: completion, ActionID: "caller-action-id-is-not-authoritative",
		Phrases: []string{replyText}, Text: replyText,
		Action:      m5ai.ReplyActionInviteWechat,
		ContentHash: textcanon.Hash(replyText), PlannedAt: completion.FinishedAt,
	})
	var resample *AIAdviceResampleScheduledError
	if action != nil || !errors.As(err, &resample) ||
		resample.Reason != string(communication.V4ManualReplyInvalid) ||
		resample.Attempt != 1 {
		t.Fatalf("前置不满足的换微信建议未安排重采: action=%+v err=%v", action, err)
	}
	turn, err := s.DialogueTurnByID(frozen.Turn.TurnID)
	if err != nil || turn == nil ||
		turn.Status != DialogueTurnClassified || turn.FailureReason != "" {
		t.Fatalf("重采样本不得留下轮终局: turn=%+v err=%v", turn, err)
	}
	actions, err := s.CommunicationActionsByTurn(frozen.Turn.TurnID)
	if err != nil || len(actions) != 0 {
		t.Fatalf("重采样本不得留下动作: actions=%+v err=%v", actions, err)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive ||
		aggregate.ManualReason != "" {
		t.Fatalf("重采不得触碰聚合: aggregate=%+v err=%v", aggregate, err)
	}
}

func TestCommunicationV4RejectedIntentWithoutPhrasesStillDocksFixedPhraseUnavailable(t *testing.T) {
	s := openTest(t)
	// 保留族负例:话术配置缺失是确定性配置问题,规格明示保留停靠,不得被
	// 重采机制放宽——意向结算侧因此不接重采信号,第 1 次即停靠。
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-advice-phrase-dock")
	text := "合成拒绝消息"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text",
		ContentHash: "v4-phrase-dock-2", Text: &text,
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil {
		t.Fatal(err)
	}
	intentID := "invocation-v4-phrase-dock-intent"
	if reserved, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: intentID, TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeIntent, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro",
		InputHash: "input-v4-phrase-dock-intent",
	}); err != nil || !reserved.Created {
		t.Fatalf("意向调用未预留: result=%+v err=%v", reserved, err)
	}
	turn, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
		Completion: successfulInvocationCompletion(
			intentID,
			time.Now().UTC().Truncate(time.Millisecond),
		),
		Label: m5ai.IntentRejected, Source: DialogueIntentLLM,
		ManualReason: "intentRejected",
	})
	if err != nil || turn == nil ||
		turn.Status != DialogueTurnManualRequired ||
		turn.FailureReason != string(communication.V4ManualFixedPhraseUnavailable) {
		t.Fatalf("话术缺失未按现状第 1 次停靠: turn=%+v err=%v", turn, err)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
		aggregate.ManualReason != string(communication.V4ManualFixedPhraseUnavailable) {
		t.Fatalf("话术缺失未按现状冻结聚合: aggregate=%+v err=%v", aggregate, err)
	}
	actions, err := s.CommunicationActionsByTurn(frozen.Turn.TurnID)
	if err != nil || len(actions) != 0 {
		t.Fatalf("话术缺失停靠不得留下动作: actions=%+v err=%v", actions, err)
	}
}
