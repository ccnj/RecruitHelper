package store

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/textcanon"
	"recruithelper/contract/gen/go/protocol"

	"gorm.io/gorm"
)

type communicationV4AutomaticEffectFixture struct {
	resumeStoreFixture
	Turn   DialogueTurn
	Action CommunicationAction
	Now    time.Time
}

func seedPlannedCommunicationV4AutomaticAction(
	t *testing.T,
	s *Store,
	suffix string,
) communicationV4AutomaticEffectFixture {
	t.Helper()
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-auto-effect-"+suffix)
	if err := s.db.Where("profile_id = ?", fixture.ProfileID).
		Delete(&M5TrialSelection{}).Error; err != nil {
		t.Fatal(err)
	}
	inboundText := "合成候选人普通回复-" + suffix
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text",
		ContentHash: textcanon.Hash(inboundText), Text: &inboundText,
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	intentID := "invocation-v4-auto-intent-" + suffix
	if reserved, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: intentID, TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeIntent, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro",
		InputHash: "input-v4-auto-intent-" + suffix, CreatedAt: now,
	}); err != nil || !reserved.Created {
		t.Fatalf("V4 intent 未预留: result=%+v err=%v", reserved, err)
	}
	if _, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
		Completion: successfulInvocationCompletion(intentID, now.Add(time.Second)),
		Label:      m5ai.IntentInterested,
		Source:     DialogueIntentLLM,
	}); err != nil {
		t.Fatal(err)
	}
	replyID := "invocation-v4-auto-reply-" + suffix
	if reserved, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: replyID, TurnID: frozen.Turn.TurnID,
		Purpose: m5ai.PurposeReply, Attempt: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro",
		InputHash: "input-v4-auto-reply-" + suffix, CreatedAt: now.Add(2 * time.Second),
	}); err != nil || !reserved.Created {
		t.Fatalf("V4 reply 未预留: result=%+v err=%v", reserved, err)
	}
	replyText := "合成自动回复-" + suffix
	action, err := s.CompleteReplyInvocation(CompleteReplyInvocationRequest{
		Completion: successfulInvocationCompletion(replyID, now.Add(3*time.Second)),
		ActionID:   "caller-action-id-is-ignored-" + suffix,
		Text:       replyText, ContentHash: textcanon.Hash(replyText),
		PlannedAt: now.Add(3 * time.Second),
	})
	if err != nil || action == nil || action.Status != CommunicationActionPlanned ||
		action.ActionID != frozen.Turn.TurnID+"|replyText" {
		t.Fatalf("V4 planned action 构造失败: action=%+v err=%v", action, err)
	}
	return communicationV4AutomaticEffectFixture{
		resumeStoreFixture: fixture,
		Turn:               frozen.Turn,
		Action:             *action,
		Now:                now.Add(4 * time.Second),
	}
}

func communicationV4AutomaticEffectRequest(
	t *testing.T,
	s *Store,
	fixture communicationV4AutomaticEffectFixture,
	suffix string,
) CreateEffectIntentRequest {
	t.Helper()
	intentID, err := M5AutomaticIntentID(fixture.Action.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	args, err := protocol.Encode(protocol.ChatSendMessageArgs{
		ConversationRef: fixture.ConversationRef,
		Text:            fixture.Action.Text,
	})
	if err != nil {
		t.Fatal(err)
	}
	previousIntentID := ""
	latest, err := s.LatestEffectIntent(
		fixture.Platform,
		fixture.AccountRef,
		fixture.ConversationRef,
	)
	if err != nil {
		t.Fatal(err)
	}
	if latest != nil {
		previousIntentID = latest.IntentID
	}
	deadline := fixture.Now.Add(time.Hour).UnixMilli()
	return CreateEffectIntentRequest{
		Intent: EffectIntent{
			IntentID: intentID, IdemKey: "idem-v4-auto-effect-" + suffix,
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			Primitive: primitiveChatSendMessage, TargetRef: fixture.ConversationRef,
			PayloadHash: "payload-v4-auto-effect-" + suffix,
			GuardsHash:  "guards-v4-auto-effect-" + suffix,
			Status:      EffectIntentDispatching, DeadlineMs: deadline,
			SendFingerprint: fixture.Action.ContentHash,
		},
		Command: CmdRecord{
			MsgID: "msg-v4-auto-effect-" + suffix,
			Name:  primitiveChatSendMessage, Class: "effectful",
			IdemKey:  "idem-v4-auto-effect-" + suffix,
			Domain:   fixture.Platform + ":" + fixture.AccountRef,
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			ExpectedPrincipalFingerprint: fixture.Principal,
			IntentID:                     intentID, HandID: fixture.HandID, Session: fixture.Session,
			BootIDAtDispatch: fixture.BootID, Args: string(args),
			Status: CmdQueued, DeadlineMs: deadline, ExecBudgetMs: 60_000,
		},
		ExpectedTailSeq:   fixture.Turn.InboundThroughSeq,
		PreviousIntentID:  previousIntentID,
		AutomaticActionID: fixture.Action.ActionID,
		Now:               fixture.Now,
	}
}

func createCommunicationV4AutomaticEffect(
	t *testing.T,
	s *Store,
	suffix string,
) (
	communicationV4AutomaticEffectFixture,
	CreateEffectIntentRequest,
	*CreateEffectIntentResult,
) {
	t.Helper()
	fixture := seedPlannedCommunicationV4AutomaticAction(t, s, suffix)
	req := communicationV4AutomaticEffectRequest(t, s, fixture, suffix)
	created, err := s.CreateEffectIntentAndCmd(req)
	if err != nil || !created.Created {
		t.Fatalf("V4 intent/WAL 构造失败: result=%+v err=%v", created, err)
	}
	return fixture, req, created
}

func confirmCommunicationV4TextEffect(
	t *testing.T,
	s *Store,
	fixture communicationV4AutomaticEffectFixture,
	suffix string,
) CreateEffectIntentRequest {
	t.Helper()
	req := communicationV4AutomaticEffectRequest(t, s, fixture, suffix)
	created, err := s.CreateEffectIntentAndCmd(req)
	if err != nil || !created.Created {
		t.Fatalf("V4 正文 WAL 构造失败: result=%+v err=%v", created, err)
	}
	resultAt := fixture.Now.Add(time.Minute)
	if _, err := s.ApplyResultMessage(
		created.Command.MsgID,
		"result-"+suffix,
		"result",
		fixture.HandID,
		func(cmd *CmdRecord) (ResultCommandMutation, error) {
			cmd.Status = CmdOk
			cmd.TerminalAt = &resultAt
			return ResultCommandMutation{
				Save: true,
				Effect: &EffectResultMutation{
					IntentStatus: EffectIntentOk,
					Append:       true,
					Text:         fixture.Action.Text,
					ContentHash:  fixture.Action.ContentHash,
					ObservedAtMs: resultAt.UnixMilli(),
				},
			}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	return req
}

func assertCommunicationV4HasNoTrial(
	t *testing.T,
	s *Store,
	profileID string,
) {
	t.Helper()
	var trials int64
	if err := s.db.Model(&M5TrialSelection{}).
		Where("profile_id = ?", profileID).
		Count(&trials).Error; err != nil || trials != 0 {
		t.Fatalf("V4 生产路径不得依赖 trial: count=%d err=%v", trials, err)
	}
}

func TestCommunicationV4AutomaticActionConstructsWALWithoutTrial(t *testing.T) {
	s := openTest(t)
	fixture, req, created := createCommunicationV4AutomaticEffect(t, s, "construct")
	assertCommunicationV4HasNoTrial(t, s, fixture.ProfileID)

	action, err := s.CommunicationActionByTurn(fixture.Turn.TurnID)
	if err != nil || action == nil || action.Status != CommunicationActionEffectPending ||
		action.EffectIntentID == nil || *action.EffectIntentID != req.Intent.IntentID {
		t.Fatalf("V4 action 未与 WAL 原子绑定: action=%+v err=%v", action, err)
	}
	turn, err := s.DialogueTurnByID(fixture.Turn.TurnID)
	if err != nil || turn == nil || turn.Status != DialogueTurnDispatching {
		t.Fatalf("V4 turn 未进入 dispatching: turn=%+v err=%v", turn, err)
	}
	replayed, err := s.CreateEffectIntentAndCmd(req)
	if err != nil || replayed.Created ||
		replayed.Intent.IntentID != created.Intent.IntentID ||
		replayed.Command.MsgID != created.Command.MsgID {
		t.Fatalf("V4 WAL 重放发生增生: result=%+v err=%v", replayed, err)
	}
}

func TestCommunicationV4RejectionTextThenWechatCardOwnIndependentEffects(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-rejection-combo")
	setCommunicationV4FixedPhrasePackage(t, s, "revision-profile-v4-rejection-combo")
	inboundText := "暂时不考虑，谢谢"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text",
		ContentHash: textcanon.Hash(inboundText), Text: &inboundText,
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil || frozen.Turn.Status != DialogueTurnAdviceReady {
		t.Fatalf("拒绝组合未冻结到正文待发: result=%+v err=%v", frozen, err)
	}
	actions, err := s.CommunicationActionsByTurn(frozen.Turn.TurnID)
	if err != nil || len(actions) != 1 ||
		actions[0].Kind != CommunicationActionReplyText {
		t.Fatalf("正文正证前出现了卡片 action: actions=%+v err=%v", actions, err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	textFixture := communicationV4AutomaticEffectFixture{
		resumeStoreFixture: fixture,
		Turn:               frozen.Turn,
		Action:             actions[0],
		Now:                now,
	}
	textReq := communicationV4AutomaticEffectRequest(
		t,
		s,
		textFixture,
		"rejection-combo-text",
	)
	textCreated, err := s.CreateEffectIntentAndCmd(textReq)
	if err != nil || !textCreated.Created {
		t.Fatalf("拒绝正文 WAL 构造失败: result=%+v err=%v", textCreated, err)
	}
	textResultAt := now.Add(time.Minute)
	if _, err := s.ApplyResultMessage(
		textCreated.Command.MsgID,
		"result-v4-rejection-combo-text",
		"result",
		fixture.HandID,
		func(cmd *CmdRecord) (ResultCommandMutation, error) {
			cmd.Status = CmdOk
			cmd.TerminalAt = &textResultAt
			return ResultCommandMutation{
				Save: true,
				Effect: &EffectResultMutation{
					IntentStatus: EffectIntentOk,
					Append:       true,
					Text:         actions[0].Text,
					ContentHash:  actions[0].ContentHash,
					ObservedAtMs: textResultAt.UnixMilli(),
				},
			}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	actions, err = s.CommunicationActionsByTurn(frozen.Turn.TurnID)
	if err != nil || len(actions) != 2 ||
		actions[0].Status != CommunicationActionSent ||
		actions[1].Kind != CommunicationActionInviteWechat ||
		actions[1].Status != CommunicationActionPlanned ||
		actions[1].DependsOnActionID == nil ||
		*actions[1].DependsOnActionID != actions[0].ActionID {
		t.Fatalf("正文正证没有原子实体化唯一卡片 action: actions=%+v err=%v", actions, err)
	}
	turn, err := s.DialogueTurnByID(frozen.Turn.TurnID)
	if err != nil || turn == nil || turn.Status != DialogueTurnAdviceReady {
		t.Fatalf("组合正文后 turn 未等待卡片: turn=%+v err=%v", turn, err)
	}

	card := actions[1]
	childIntent := EffectIntent{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		TargetRef: fixture.ConversationRef,
	}
	if err := validateM5ActionDependencyTx(
		s.db,
		*turn,
		card,
		"",
		&childIntent,
	); !errors.Is(err, ErrEffectIntentCASConflict) {
		t.Fatalf("dependent 卡片必须钉死正文 parent intent: err=%v", err)
	}
	rollback := errors.New("rollback inserted message fixture")
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		inserted := "候选人在正文后立即回复"
		if err := tx.Create(&Message{
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			ConversationRef: fixture.ConversationRef, Seq: 4,
			Direction: "in", Kind: "text", ContentHash: textcanon.Hash(inserted),
			Text: &inserted, Origin: "external", CreatedAt: textResultAt.Add(time.Second),
		}).Error; err != nil {
			return err
		}
		if _, currentErr := validateM5DependentActionCurrentTx(tx, *turn, card); !errors.Is(currentErr, ErrDialogueTurnBinding) {
			t.Fatalf("正文后出现新消息必须停止 dependent 卡片: err=%v", currentErr)
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("插入消息负例没有回滚: err=%v", err)
	}
	cardIntentID, err := M5AutomaticIntentID(card.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	cardArgs, err := protocol.Encode(protocol.ChatSendWechatInviteArgs{
		ConversationRef: fixture.ConversationRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	cardAt := textResultAt.Add(time.Minute)
	cardDeadline := cardAt.Add(time.Hour).UnixMilli()
	cardReq := CreateEffectIntentRequest{
		Intent: EffectIntent{
			IntentID:        cardIntentID,
			IdemKey:         "idem-v4-rejection-combo-card",
			Platform:        fixture.Platform,
			AccountRef:      fixture.AccountRef,
			Primitive:       primitiveChatSendWechatInvite,
			TargetRef:       fixture.ConversationRef,
			PayloadHash:     "payload-v4-rejection-combo-card",
			GuardsHash:      "guards-v4-rejection-combo-card",
			Status:          EffectIntentDispatching,
			DeadlineMs:      cardDeadline,
			SendFingerprint: card.ContentHash,
		},
		Command: CmdRecord{
			MsgID:                        "msg-v4-rejection-combo-card",
			Name:                         primitiveChatSendWechatInvite,
			Class:                        "effectful",
			IdemKey:                      "idem-v4-rejection-combo-card",
			Domain:                       fixture.Platform + ":" + fixture.AccountRef,
			Platform:                     fixture.Platform,
			AccountRef:                   fixture.AccountRef,
			ExpectedPrincipalFingerprint: fixture.Principal,
			IntentID:                     cardIntentID,
			HandID:                       fixture.HandID,
			Session:                      fixture.Session,
			BootIDAtDispatch:             fixture.BootID,
			Args:                         string(cardArgs),
			Status:                       CmdQueued,
			DeadlineMs:                   cardDeadline,
			ExecBudgetMs:                 60_000,
		},
		ExpectedTailSeq:   3,
		PreviousIntentID:  textReq.Intent.IntentID,
		AutomaticActionID: card.ActionID,
		Now:               cardAt,
	}
	cardCreated, err := s.CreateEffectIntentAndCmd(cardReq)
	if err != nil || !cardCreated.Created {
		t.Fatalf("换微信卡独立 WAL 构造失败: result=%+v err=%v", cardCreated, err)
	}
	cardResultAt := cardAt.Add(time.Minute)
	if _, err := s.ApplyResultMessage(
		cardCreated.Command.MsgID,
		"result-v4-rejection-combo-card",
		"result",
		fixture.HandID,
		func(cmd *CmdRecord) (ResultCommandMutation, error) {
			cmd.Status = CmdOk
			cmd.TerminalAt = &cardResultAt
			return ResultCommandMutation{
				Save: true,
				Effect: &EffectResultMutation{
					IntentStatus: EffectIntentOk,
					ContentHash:  card.ContentHash,
					Card: &CardResultMutation{
						ConversationRef: fixture.ConversationRef,
						CardType:        "wechatExchange",
						CardState:       "pending",
						ContentHash:     card.ContentHash,
						SourceKey:       strings.Repeat("a", 64),
					},
				},
			}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	card, err = func() (CommunicationAction, error) {
		found, lookupErr := s.CommunicationActionByID(actions[1].ActionID)
		if found == nil {
			return CommunicationAction{}, lookupErr
		}
		return *found, lookupErr
	}()
	turn, turnErr := s.DialogueTurnByID(frozen.Turn.TurnID)
	aggregate, aggregateErr := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil ||
		card.Status != CommunicationActionSent ||
		turnErr != nil ||
		turn == nil ||
		turn.Status != DialogueTurnCompleted ||
		aggregateErr != nil ||
		aggregate.State.WechatState != communication.V4WechatInvited ||
		!aggregate.State.RetentionSent ||
		aggregate.ProjectedThroughSeq != 4 {
		t.Fatalf(
			"组合卡片正证未完成 turn/v4: card=%+v turn=%+v aggregate=%+v errs=%v/%v/%v",
			card,
			turn,
			aggregate,
			err,
			turnErr,
			aggregateErr,
		)
	}
	if textReq.Intent.IntentID == cardReq.Intent.IntentID ||
		textReq.Command.MsgID == cardReq.Command.MsgID ||
		textReq.Intent.IdemKey == cardReq.Intent.IdemKey {
		t.Fatal("组合两动作必须拥有独立 intent/cmd/idemKey")
	}
}

func TestCommunicationV4CombinationTextFailureNeverMaterializesCard(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-v4-combo-text-failed")
	setCommunicationV4FixedPhrasePackage(t, s, "revision-profile-v4-combo-text-failed")
	inboundText := "暂时不考虑，谢谢"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text",
		ContentHash: textcanon.Hash(inboundText), Text: &inboundText,
	})
	frozen, err := s.FreezeCommunicationV4Turn(
		communicationV4TurnRequest(t, s, fixture, inbound),
	)
	if err != nil {
		t.Fatal(err)
	}
	actions, err := s.CommunicationActionsByTurn(frozen.Turn.TurnID)
	if err != nil || len(actions) != 1 {
		t.Fatalf("拒绝组合正文规划错误: actions=%+v err=%v", actions, err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	textFixture := communicationV4AutomaticEffectFixture{
		resumeStoreFixture: fixture,
		Turn:               frozen.Turn,
		Action:             actions[0],
		Now:                now,
	}
	req := communicationV4AutomaticEffectRequest(
		t,
		s,
		textFixture,
		"combo-text-failed",
	)
	created, err := s.CreateEffectIntentAndCmd(req)
	if err != nil || !created.Created {
		t.Fatalf("正文 WAL 构造失败: result=%+v err=%v", created, err)
	}
	failedAt := now.Add(time.Minute)
	if _, err := s.ApplyResultMessage(
		created.Command.MsgID,
		"result-v4-combo-text-failed",
		"result",
		fixture.HandID,
		func(cmd *CmdRecord) (ResultCommandMutation, error) {
			cmd.Status = CmdFailed
			cmd.SideEffect = "none"
			cmd.TerminalAt = &failedAt
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
	// 2026-08-02 §8.4 通则:正文干净失败自动重铸 |try2,原动作标 retried;
	// 卡片仍然只能等实际发出的那一代正文取得正证后物化。
	actions, err = s.CommunicationActionsByTurn(frozen.Turn.TurnID)
	if err != nil || len(actions) != 2 ||
		actions[0].Status != CommunicationActionRetried ||
		actions[1].ActionID != actions[0].ActionID+"|try2" ||
		actions[1].Status != CommunicationActionPlanned ||
		actions[1].Kind != CommunicationActionReplyText {
		t.Fatalf("正文干净失败应重铸而非物化卡片: actions=%+v err=%v", actions, err)
	}
	var cardIntents int64
	if err := s.db.Model(&EffectIntent{}).
		Where("primitive IN ?", []string{
			primitiveChatSendWechatInvite,
			primitiveChatSendInviteCard,
		}).
		Count(&cardIntents).Error; err != nil || cardIntents != 0 {
		t.Fatalf("正文失败不得创建卡片 WAL: count=%d err=%v", cardIntents, err)
	}
}

// 2026-08-04 审查发现的回归：onsite 计划物化时解 endsAt 空指针当场 panic。
// 这条路径跑在 WS result 处理的事务里，全仓 Go 生产代码没有 recover，崩的是
// 整个脑进程，且 result 未入账、重启后重放会再走一遍。
func TestCommunicationV4OnsiteInterviewCardMaterializesWithoutEndsAt(t *testing.T) {
	s := openTest(t)
	fixture := seedPlannedCommunicationV4AutomaticAction(t, s, "interview-onsite")
	startsAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Minute).UnixMilli()
	method := "onsite"
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
			InterviewMethod:     &method,
		},
	)
	if err := s.db.Save(&advice).Error; err != nil {
		t.Fatal(err)
	}
	confirmCommunicationV4TextEffect(t, s, fixture, "interview-onsite-text")
	actions, err := s.CommunicationActionsByTurn(fixture.Turn.TurnID)
	if err != nil || len(actions) != 2 {
		t.Fatalf("正文正证未实体化现场邀面 action: actions=%+v err=%v", actions, err)
	}
	card := actions[1]
	if card.Kind != CommunicationActionInterviewInvite ||
		card.InterviewStartsAtMs == nil || *card.InterviewStartsAtMs != startsAt ||
		card.InterviewEndsAtMs != nil ||
		card.InterviewMethod == nil || *card.InterviewMethod != "onsite" {
		t.Fatalf("现场邀面 action 形态错误(endsAt 必须缺席): %+v", card)
	}
	if want := communicationInterviewInviteContentHash(startsAt, 0, "onsite"); card.ContentHash != want {
		t.Fatalf("缺席 endsAt 必须按空串投影 contentHash: got=%s want=%s", card.ContentHash, want)
	}
}

func TestCommunicationV4InterviewCardActionBindsAndCompletesAfterTextEvidence(t *testing.T) {
	s := openTest(t)
	fixture := seedPlannedCommunicationV4AutomaticAction(t, s, "interview-combo")
	startsAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Minute).UnixMilli()
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
	// This test-only fixture mutation stands in for the separately scoped AI
	// meeting-time matcher. Production still cannot create this second plan
	// until that deterministic matcher lands.
	if err := s.db.Save(&advice).Error; err != nil {
		t.Fatal(err)
	}
	textReq := confirmCommunicationV4TextEffect(
		t,
		s,
		fixture,
		"interview-combo-text",
	)
	actions, err := s.CommunicationActionsByTurn(fixture.Turn.TurnID)
	if err != nil || len(actions) != 2 ||
		actions[1].Kind != CommunicationActionInterviewInvite ||
		actions[1].DependsOnActionID == nil ||
		actions[1].InterviewStartsAtMs == nil ||
		*actions[1].InterviewStartsAtMs != startsAt ||
		actions[1].InterviewEndsAtMs == nil ||
		*actions[1].InterviewEndsAtMs != endsAt ||
		actions[1].InterviewMethod == nil ||
		*actions[1].InterviewMethod != method {
		t.Fatalf("正文正证未实体化邀面 action: actions=%+v err=%v", actions, err)
	}
	card := actions[1]
	intentID, err := M5AutomaticIntentID(card.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	interview := protocol.InterviewDetails{
		StartsAt: startsAt,
		EndsAt:   endsAt,
		Method:   protocol.InterviewMethodWechatVideo,
	}
	args, err := protocol.Encode(protocol.ChatSendInviteCardArgs{
		ConversationRef: fixture.ConversationRef,
		Interview:       interview,
	})
	if err != nil {
		t.Fatal(err)
	}
	cardAt := fixture.Now.Add(2 * time.Minute)
	deadline := cardAt.Add(time.Hour).UnixMilli()
	req := CreateEffectIntentRequest{
		Intent: EffectIntent{
			IntentID:        intentID,
			IdemKey:         "idem-v4-interview-combo",
			Platform:        fixture.Platform,
			AccountRef:      fixture.AccountRef,
			Primitive:       primitiveChatSendInviteCard,
			TargetRef:       fixture.ConversationRef,
			PayloadHash:     "payload-v4-interview-combo",
			GuardsHash:      "guards-v4-interview-combo",
			Status:          EffectIntentDispatching,
			DeadlineMs:      deadline,
			SendFingerprint: card.ContentHash,
		},
		Command: CmdRecord{
			MsgID:                        "msg-v4-interview-combo",
			Name:                         primitiveChatSendInviteCard,
			Class:                        "effectful",
			IdemKey:                      "idem-v4-interview-combo",
			Domain:                       fixture.Platform + ":" + fixture.AccountRef,
			Platform:                     fixture.Platform,
			AccountRef:                   fixture.AccountRef,
			ExpectedPrincipalFingerprint: fixture.Principal,
			IntentID:                     intentID,
			HandID:                       fixture.HandID,
			Session:                      fixture.Session,
			BootIDAtDispatch:             fixture.BootID,
			Args:                         string(args),
			Status:                       CmdQueued,
			DeadlineMs:                   deadline,
			ExecBudgetMs:                 120_000,
		},
		ExpectedTailSeq:   fixture.Turn.InboundThroughSeq + 1,
		PreviousIntentID:  textReq.Intent.IntentID,
		AutomaticActionID: card.ActionID,
		Now:               cardAt,
	}
	created, err := s.CreateEffectIntentAndCmd(req)
	if err != nil || !created.Created {
		t.Fatalf("邀面 action 未独立绑定 WAL: result=%+v err=%v", created, err)
	}
	resultAt := cardAt.Add(time.Minute)
	if _, err := s.ApplyResultMessage(
		created.Command.MsgID,
		"result-v4-interview-combo",
		"result",
		fixture.HandID,
		func(cmd *CmdRecord) (ResultCommandMutation, error) {
			cmd.Status = CmdOk
			cmd.TerminalAt = &resultAt
			return ResultCommandMutation{
				Save: true,
				Effect: &EffectResultMutation{
					IntentStatus: EffectIntentOk,
					ContentHash:  card.ContentHash,
					Card: &CardResultMutation{
						ConversationRef:     fixture.ConversationRef,
						CardType:            "interviewInvite",
						CardState:           "unknown",
						ContentHash:         card.ContentHash,
						SourceKey:           strings.Repeat("b", 64),
						InterviewStartsAtMs: &startsAt,
						InterviewEndsAtMs:   &endsAt,
						InterviewMethod:     &method,
					},
				},
			}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	settled, err := s.CommunicationActionByID(card.ActionID)
	turn, turnErr := s.DialogueTurnByID(fixture.Turn.TurnID)
	aggregate, aggregateErr := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil ||
		settled == nil ||
		settled.Status != CommunicationActionSent ||
		turnErr != nil ||
		turn == nil ||
		turn.Status != DialogueTurnCompleted ||
		aggregateErr != nil ||
		aggregate.State.MainStatus != communication.V4StatusInvited ||
		len(aggregate.State.InterviewGroups) != 1 ||
		aggregate.State.InterviewGroups[0].MessageSeq != fixture.Turn.InboundThroughSeq+2 {
		t.Fatalf(
			"邀面正证未完成 action/turn/v4: action=%+v turn=%+v aggregate=%+v errs=%v/%v/%v",
			settled,
			turn,
			aggregate,
			err,
			turnErr,
			aggregateErr,
		)
	}
}

func TestCommunicationV4InterviewCardPlanRequiresThirtyMinutes(t *testing.T) {
	startsAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Minute).UnixMilli()
	method := "wechatVideo"
	for _, duration := range []time.Duration{15 * time.Minute, 45 * time.Minute} {
		endsAt := startsAt + duration.Milliseconds()
		if supportedCommunicationV4CardPlan(communication.V4PlannedAction{
			ActionKey:           "turn|interviewInvite",
			Kind:                communication.V4ActionInterviewInvite,
			InterviewStartsAtMs: &startsAt,
			InterviewEndsAtMs:   &endsAt,
			InterviewMethod:     &method,
		}) {
			t.Fatalf("非 30 分钟邀面计划不得获批: duration=%s", duration)
		}
	}
}

// 2026-08-04 真机首验的回归:线下邀面计划曾在这道闸上被判不支持,连带整轮
// 建议按 multiVisibleActionPolicyConflict 作废重采,候选人那边表现为"临近发
// 面试卡片就没动作了"。三处闸(本闸、消息落库、动作与计划配对)现在共用
// communication.ValidV4InterviewShape,形态与 method 必须自洽。
func TestCommunicationV4InterviewCardPlanAcceptsOnsiteWithoutEndsAt(t *testing.T) {
	startsAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Minute).UnixMilli()
	endsAt := startsAt + communication.V4InterviewDurationMs
	onsite := "onsite"
	online := "wechatVideo"
	plan := func(method *string, ends *int64) communication.V4PlannedAction {
		return communication.V4PlannedAction{
			ActionKey:           "turn|interviewInvite",
			Kind:                communication.V4ActionInterviewInvite,
			InterviewStartsAtMs: &startsAt,
			InterviewEndsAtMs:   ends,
			InterviewMethod:     method,
		}
	}
	if !supportedCommunicationV4CardPlan(plan(&onsite, nil)) {
		t.Fatal("缺席 endsAt 的现场面试计划必须获批")
	}
	if supportedCommunicationV4CardPlan(plan(&onsite, &endsAt)) {
		t.Fatal("现场面试带 endsAt 不得获批:平台不提供结束时间,不得合成")
	}
	if supportedCommunicationV4CardPlan(plan(&online, nil)) {
		t.Fatal("线上会议缺 endsAt 不得获批")
	}
	if !supportedCommunicationV4CardPlan(plan(&online, &endsAt)) {
		t.Fatal("标准时长的线上会议计划必须获批")
	}
}

func TestCommunicationV4AutomaticPositiveEvidenceAdvancesCursorAndAllowsNextTurn(t *testing.T) {
	s := openTest(t)
	fixture, req, created := createCommunicationV4AutomaticEffect(t, s, "positive")
	before, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || before.State.LastBodyAt == nil {
		t.Fatalf("缺少招呼正文时钟基线: aggregate=%+v err=%v", before, err)
	}
	resultAt := fixture.Now.Add(time.Minute)
	result, err := s.ApplyResultMessage(
		created.Command.MsgID,
		"result-v4-auto-effect-positive",
		"result",
		fixture.HandID,
		func(cmd *CmdRecord) (ResultCommandMutation, error) {
			cmd.Status = CmdOk
			cmd.TerminalAt = &resultAt
			return ResultCommandMutation{Save: true, Effect: &EffectResultMutation{
				IntentStatus: EffectIntentOk, Append: true,
				Text: fixture.Action.Text, ContentHash: fixture.Action.ContentHash,
				ObservedAtMs: resultAt.UnixMilli(),
			}}, nil
		},
	)
	if err != nil || !result.CommandFound || result.AlreadyProcessed {
		t.Fatalf("V4 正证入账失败: result=%+v err=%v", result, err)
	}
	assertCommunicationV4HasNoTrial(t, s, fixture.ProfileID)
	action, err := s.CommunicationActionByTurn(fixture.Turn.TurnID)
	if err != nil || action == nil || action.Status != CommunicationActionSent ||
		action.SentAt == nil || action.EffectIntentID == nil ||
		*action.EffectIntentID != req.Intent.IntentID {
		t.Fatalf("V4 action 未收敛 sent: action=%+v err=%v", action, err)
	}
	confirmedAt := *action.SentAt
	turn, err := s.DialogueTurnByID(fixture.Turn.TurnID)
	if err != nil || turn == nil || turn.Status != DialogueTurnCompleted {
		t.Fatalf("V4 turn 未收敛 completed: turn=%+v err=%v", turn, err)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || aggregate.Revision != 4 || aggregate.ProjectedThroughSeq != 3 ||
		aggregate.State.LastOutboundMessageSeq != 3 ||
		aggregate.State.LastOutboundAt == nil ||
		!aggregate.State.LastOutboundAt.Equal(confirmedAt) ||
		aggregate.State.ClockUncertain ||
		aggregate.State.LastBodyAt == nil ||
		!aggregate.State.LastBodyAt.Equal(confirmedAt) ||
		aggregate.State.BodyClockUncertain ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive {
		t.Fatalf("V4 正证未以脑侧确认时刻推进状态/游标: aggregate=%+v err=%v", aggregate, err)
	}
	var confirmedMessage Message
	if err := s.db.First(
		&confirmedMessage,
		"outbound_intent_id = ?",
		req.Intent.IntentID,
	).Error; err != nil || confirmedMessage.TsApproxMs != nil {
		t.Fatalf("保守 V4 时钟不得伪造平台消息时间: message=%+v err=%v",
			confirmedMessage, err)
	}
	var messages, confirmations int64
	if err := s.db.Model(&Message{}).
		Where("outbound_intent_id = ? AND retracted_at IS NULL", req.Intent.IntentID).
		Count(&messages).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&CommunicationV4ProjectionApplication{}).
		Where("profile_id = ? AND input_kind = ? AND input_key = ?",
			fixture.ProfileID, CommunicationV4InputConfirmedAction, fixture.Action.ActionID).
		Count(&confirmations).Error; err != nil {
		t.Fatal(err)
	}
	if messages != 1 || confirmations != 1 {
		t.Fatalf("V4 正证发生事实增生: messages=%d confirmations=%d", messages, confirmations)
	}
	laterReplayAt := confirmedAt.Add(time.Hour)
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		return applyM5AutomaticEffectStatusByIDTx(
			tx,
			req.Intent.IntentID,
			laterReplayAt,
		)
	}); err != nil {
		t.Fatalf("V4 正证状态重放失败: %v", err)
	}
	replayedAction, err := s.CommunicationActionByTurn(fixture.Turn.TurnID)
	replayedAggregate, aggregateErr := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || replayedAction == nil || replayedAction.SentAt == nil ||
		!replayedAction.SentAt.Equal(confirmedAt) ||
		aggregateErr != nil || replayedAggregate.Revision != aggregate.Revision ||
		replayedAggregate.State.LastOutboundAt == nil ||
		!replayedAggregate.State.LastOutboundAt.Equal(confirmedAt) ||
		replayedAggregate.State.LastBodyAt == nil ||
		!replayedAggregate.State.LastBodyAt.Equal(confirmedAt) {
		t.Fatalf(
			"较晚重放不得漂移首次确认锚: action=%+v aggregate=%+v err=%v aggregateErr=%v",
			replayedAction,
			replayedAggregate,
			err,
			aggregateErr,
		)
	}

	nextText := "合成第二轮候选人回复"
	nextInbound := appendCommunicationV4Inbound(t, s, fixture.resumeStoreFixture, Message{
		Seq: 4, Direction: "in", Kind: "text",
		ContentHash: textcanon.Hash(nextText), Text: &nextText,
	})
	var lastOutbound Message
	if err := s.db.First(
		&lastOutbound,
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ?",
		fixture.Platform, fixture.AccountRef, fixture.ConversationRef, 3,
	).Error; err != nil {
		t.Fatal(err)
	}
	digest, turnID, err := DialogueTurnIdentity(
		fixture.ProfileID,
		lastOutbound,
		nextInbound,
	)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := s.CommunicationTargetsForAccount(AccountKey{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
	})
	if err != nil || len(targets) != 1 {
		t.Fatalf("正证后目标不可继续: targets=%+v err=%v", targets, err)
	}
	material, materialReady, err := s.CommunicationAIMaterialForProfile(fixture.ProfileID)
	if err != nil || !materialReady {
		t.Fatalf("正证后 AI 材料不可继续: material=%+v ready=%v err=%v",
			material, materialReady, err)
	}
	next, err := s.FreezeCommunicationV4Turn(FreezeDialogueTurnRequest{
		TurnID: turnID, ProfileID: fixture.ProfileID,
		ConversationRef: fixture.ConversationRef, InputDigest: digest,
		HistoryThroughSeq: 3, InboundFromSeq: 4, InboundThroughSeq: 4,
		ExpectedProjectedThroughSeq: 3, OutboundAnchorSeq: 3,
		ContextRevisionHash: material.ContextRevision.RevisionHash,
		ResumeSnapshotID:    material.ResumeSnapshot.SnapshotID,
		RecommendedTimeText: "合成推荐时段",
		RenderFormatVersion: m5ai.DialogueRenderFormatVersion,
		FrozenAt:            resultAt.Add(time.Minute),
	})
	if err != nil || !next.Created || next.Turn.Status != DialogueTurnCollected ||
		next.Aggregate.ProjectedThroughSeq != 4 {
		t.Fatalf("V4 正证后第二轮无法冻结: result=%+v err=%v", next, err)
	}
}

// TestCommunicationV4AutomaticCleanFailureAutoRetriesReplyText 钉住 2026-08-02
// §8.4 通则在对话轨回复气泡上的推广:干净失败(sideEffect=none)自动铸 |try2、
// 原动作 retried 留档、无任何冻结;try2 走完整安全轨成功正证后照常收编。
func TestCommunicationV4AutomaticCleanFailureAutoRetriesReplyText(t *testing.T) {
	s := openTest(t)
	fixture, req, created := createCommunicationV4AutomaticEffect(t, s, "failed")
	resultAt := fixture.Now.Add(time.Minute)
	if _, err := s.ApplyResultMessage(
		created.Command.MsgID,
		"result-v4-auto-effect-failed",
		"result",
		fixture.HandID,
		func(cmd *CmdRecord) (ResultCommandMutation, error) {
			cmd.Status = CmdFailed
			cmd.SideEffect = "none"
			cmd.TerminalAt = &resultAt
			return ResultCommandMutation{Save: true, Effect: &EffectResultMutation{
				IntentStatus: EffectIntentFailed, Reason: "failedNone",
			}}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	assertCommunicationV4HasNoTrial(t, s, fixture.ProfileID)
	original, err := s.CommunicationActionByID(fixture.Action.ActionID)
	if err != nil || original == nil ||
		original.Status != CommunicationActionRetried ||
		original.FailureReason != "effectFailed" || original.SentAt != nil {
		t.Fatalf("原动作未标 retried 留档: action=%+v err=%v", original, err)
	}
	retryID := fixture.Action.ActionID + "|try2"
	retry, err := s.CommunicationActionByID(retryID)
	if err != nil || retry == nil ||
		retry.Status != CommunicationActionPlanned ||
		retry.Kind != CommunicationActionReplyText ||
		retry.Text != fixture.Action.Text ||
		retry.ContentHash != fixture.Action.ContentHash {
		t.Fatalf("重试动作未按原参数铸造: %+v err=%v", retry, err)
	}
	turn, _ := s.DialogueTurnByID(fixture.Turn.TurnID)
	aggregate, aggregateErr := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if turn == nil || turn.Status != DialogueTurnAdviceReady ||
		turn.FailureReason != "" ||
		aggregateErr != nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive {
		t.Fatalf("干净失败不得冻结轮或档案: turn=%+v aggregate=%+v err=%v",
			turn, aggregate, aggregateErr)
	}
	var messages int64
	if err := s.db.Model(&Message{}).
		Where("outbound_intent_id = ?", req.Intent.IntentID).
		Count(&messages).Error; err != nil || messages != 0 {
		t.Fatalf("V4 failed 不得伪造消息: count=%d err=%v", messages, err)
	}
	// try2 走完整 WAL 派发并取得正证:全新 intentId/idemKey,旧 idemKey 不复用。
	retryFixture := fixture
	retryFixture.Action = *retry
	retryFixture.Now = fixture.Now.Add(2 * time.Minute)
	retryReq := communicationV4AutomaticEffectRequest(t, s, retryFixture, "failed-try2")
	if retryReq.Intent.IntentID == req.Intent.IntentID ||
		retryReq.Intent.IdemKey == req.Intent.IdemKey {
		t.Fatal("重试尝试必须持有全新 intentId/idemKey")
	}
	retryCreated, err := s.CreateEffectIntentAndCmd(retryReq)
	if err != nil || !retryCreated.Created {
		t.Fatalf("try2 WAL 构造失败: result=%+v err=%v", retryCreated, err)
	}
	sentAt := retryFixture.Now.Add(time.Minute)
	if _, err := s.ApplyResultMessage(
		retryCreated.Command.MsgID,
		"result-v4-auto-effect-failed-try2",
		"result",
		fixture.HandID,
		func(cmd *CmdRecord) (ResultCommandMutation, error) {
			cmd.Status = CmdOk
			cmd.TerminalAt = &sentAt
			return ResultCommandMutation{Save: true, Effect: &EffectResultMutation{
				IntentStatus: EffectIntentOk, Append: true,
				Text:         retry.Text,
				ContentHash:  retry.ContentHash,
				ObservedAtMs: sentAt.UnixMilli(),
			}}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	settled, err := s.CommunicationActionByID(retryID)
	finalTurn, turnErr := s.DialogueTurnByID(fixture.Turn.TurnID)
	if err != nil || settled == nil || settled.Status != CommunicationActionSent ||
		turnErr != nil || finalTurn == nil || finalTurn.Status != DialogueTurnCompleted {
		t.Fatalf("try2 成功链未完成: action=%+v turn=%+v errs=%v/%v",
			settled, finalTurn, err, turnErr)
	}
}

func TestCommunicationV4AutomaticSuspectVerdictsPreserveFirstManualReason(t *testing.T) {
	// 裁决即恢复(2026-08-02):resolvedFailed 落账即自动恢复候选人推进——
	// 旧 intent/动作终局原样留档,轮残留作废(resolvedFailedSuperseded),
	// 聚合自动回 active,无需第二次人工确认;禁止重放原冻结文案,重新规划
	// 交给下一个自然触发点。
	t.Run("resolved failed", func(t *testing.T) {
		s := openTest(t)
		fixture, req, created := createCommunicationV4AutomaticEffect(t, s, "resolved-failed")
		verifyAt := fixture.Now.Add(time.Minute)
		if err := s.MoveEffectToVerification(
			created.Command.MsgID,
			"resultLost",
			verifyAt,
		); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkEffectSuspect(
			created.Command.MsgID,
			"verificationExhausted",
			verifyAt.Add(time.Minute),
		); err != nil {
			t.Fatal(err)
		}
		suspectAggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
		if err != nil ||
			suspectAggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
			suspectAggregate.ManualReason != "effectSuspect" {
			t.Fatalf("suspect 未按原语义冻结: %+v err=%v", suspectAggregate, err)
		}
		if err := s.ResolveSuspectVerdict(ResolveSuspectVerdictRequest{
			Ref: created.Command.MsgID, Verdict: CmdResolvedFailed,
			ConversationKey: ConversationKey{
				Platform: fixture.Platform, AccountRef: fixture.AccountRef,
				ConversationRef: fixture.ConversationRef,
			},
			At: verifyAt.Add(2 * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
		action, _ := s.CommunicationActionByTurn(fixture.Turn.TurnID)
		turn, _ := s.DialogueTurnByID(fixture.Turn.TurnID)
		aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
		if action == nil || action.Status != CommunicationActionManualRequired ||
			action.FailureReason != "effectResolvedFailed" || err != nil {
			t.Fatalf("resolvedFailed 终局未原样落账: action=%+v err=%v", action, err)
		}
		if turn == nil || turn.Status != DialogueTurnSuperseded ||
			turn.FailureReason != dialogueTurnResolvedFailedSuperseded {
			t.Fatalf("裁决即恢复未作废轮残留: turn=%+v", turn)
		}
		if aggregate.AutomationStatus != ProfileCommunicationAutomationActive ||
			aggregate.ManualReason != "" {
			t.Fatalf("裁决即恢复未解冻聚合: %+v", aggregate)
		}
		intent, err := s.EffectIntentByID(req.Intent.IntentID)
		if err != nil || intent == nil || intent.Status != EffectIntentResolvedFailed {
			t.Fatalf("旧 intent 终局必须原样保留: %+v err=%v", intent, err)
		}
		var messages int64
		if err := s.db.Model(&Message{}).
			Where("outbound_intent_id = ?", req.Intent.IntentID).
			Count(&messages).Error; err != nil || messages != 0 {
			t.Fatalf("resolvedFailed 不得追加消息: count=%d err=%v", messages, err)
		}
		// 恢复只作废与解冻,不自动重发:不得出现任何新 planned 动作或第二个
		// 发送 intent(禁止重放原冻结文案)。
		var planned int64
		if err := s.db.Model(&CommunicationAction{}).
			Where("turn_id = ? AND status = ?", fixture.Turn.TurnID, CommunicationActionPlanned).
			Count(&planned).Error; err != nil || planned != 0 {
			t.Fatalf("裁决即恢复不得重放文案铸新动作: count=%d err=%v", planned, err)
		}
		var intents int64
		if err := s.db.Model(&EffectIntent{}).
			Where("target_ref = ?", fixture.ConversationRef).
			Count(&intents).Error; err != nil || intents != 1 {
			t.Fatalf("裁决即恢复不得自动重发: count=%d err=%v", intents, err)
		}
		var audits int64
		if err := s.db.Model(&AuditEntry{}).
			Where("category IN ?", []string{
				auditCategoryResolvedFailedRecovered,
				auditCategoryAutomationUnfrozen,
			}).
			Count(&audits).Error; err != nil || audits != 2 {
			t.Fatalf("裁决即恢复必须落审计: count=%d err=%v", audits, err)
		}
	})

	// 其他人工原因的聚合不被误解冻:聚合人工原因不属 effectSuspect 族时,
	// resolvedFailed 照常写终局并作废本链残留,但聚合保持原人工接管状态。
	t.Run("resolved failed keeps foreign manual reason", func(t *testing.T) {
		s := openTest(t)
		fixture, _, created := createCommunicationV4AutomaticEffect(t, s, "resolved-failed-foreign")
		verifyAt := fixture.Now.Add(time.Minute)
		if err := s.MoveEffectToVerification(
			created.Command.MsgID,
			"resultLost",
			verifyAt,
		); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkEffectSuspect(
			created.Command.MsgID,
			"verificationExhausted",
			verifyAt.Add(time.Minute),
		); err != nil {
			t.Fatal(err)
		}
		// 模拟另一条业务链先以其他原因接管了该候选人。
		if err := s.db.Model(&CommunicationV4Aggregate{}).
			Where("profile_id = ?", fixture.ProfileID).
			Update("manual_reason", "fixedPhraseUnavailable").Error; err != nil {
			t.Fatal(err)
		}
		if err := s.ResolveSuspectVerdict(ResolveSuspectVerdictRequest{
			Ref: created.Command.MsgID, Verdict: CmdResolvedFailed,
			ConversationKey: ConversationKey{
				Platform: fixture.Platform, AccountRef: fixture.AccountRef,
				ConversationRef: fixture.ConversationRef,
			},
			At: verifyAt.Add(2 * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
		aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
		if err != nil ||
			aggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
			aggregate.ManualReason != "fixedPhraseUnavailable" {
			t.Fatalf("其他人工原因被误解冻: %+v err=%v", aggregate, err)
		}
	})

	t.Run("late positive", func(t *testing.T) {
		s := openTest(t)
		fixture, req, created := createCommunicationV4AutomaticEffect(t, s, "late-positive")
		verifyAt := fixture.Now.Add(time.Minute)
		if err := s.MoveEffectToVerification(
			created.Command.MsgID,
			"resultLost",
			verifyAt,
		); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkEffectSuspect(
			created.Command.MsgID,
			"verificationExhausted",
			verifyAt.Add(time.Minute),
		); err != nil {
			t.Fatal(err)
		}
		beforeConfirmation, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
		if err != nil {
			t.Fatal(err)
		}
		resolvedAt := verifyAt.Add(2 * time.Minute)
		if err := s.ResolveSuspectVerdict(ResolveSuspectVerdictRequest{
			Ref: created.Command.MsgID, Verdict: CmdResolvedOk,
			ConversationKey: ConversationKey{
				Platform: fixture.Platform, AccountRef: fixture.AccountRef,
				ConversationRef: fixture.ConversationRef,
			},
			Text: fixture.Action.Text, ContentHash: fixture.Action.ContentHash,
			At: resolvedAt,
		}); err != nil {
			t.Fatal(err)
		}
		action, _ := s.CommunicationActionByTurn(fixture.Turn.TurnID)
		turn, _ := s.DialogueTurnByID(fixture.Turn.TurnID)
		aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
		if action == nil || action.Status != CommunicationActionSent ||
			turn == nil || turn.Status != DialogueTurnCompleted ||
			err != nil || aggregate.ProjectedThroughSeq != 3 ||
			aggregate.State.LastOutboundMessageSeq != 3 ||
			aggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
			aggregate.ManualReason != "effectSuspect" {
			t.Fatalf("迟到正证未推进事实或错误解除人工闸: action=%+v turn=%+v aggregate=%+v err=%v",
				action, turn, aggregate, err)
		}
		var messages, confirmations int64
		if err := s.db.Model(&Message{}).
			Where("outbound_intent_id = ? AND retracted_at IS NULL", req.Intent.IntentID).
			Count(&messages).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.db.Model(&CommunicationV4ProjectionApplication{}).
			Where("profile_id = ? AND input_kind = ?",
				fixture.ProfileID, CommunicationV4InputConfirmedAction).
			Count(&confirmations).Error; err != nil {
			t.Fatal(err)
		}
		if messages != 1 || confirmations != 1 {
			t.Fatalf("迟到正证发生增生: messages=%d confirmations=%d",
				messages, confirmations)
		}

		lateAt := resolvedAt.Add(time.Minute)
		result, err := s.ApplyResultMessage(
			created.Command.MsgID,
			"late-safe-v4-auto-effect",
			"result",
			fixture.HandID,
			func(cmd *CmdRecord) (ResultCommandMutation, error) {
				cmd.Status = CmdFailed
				cmd.SideEffect = "none"
				cmd.TerminalAt = &lateAt
				return ResultCommandMutation{Save: true, Effect: &EffectResultMutation{
					IntentStatus: EffectIntentFailed, Retract: true,
					Reason: "lateFailedNone",
				}}, nil
			},
		)
		if err != nil || !result.CommandFound || result.AlreadyProcessed {
			t.Fatalf("迟到安全终局未被收编: result=%+v err=%v", result, err)
		}
		action, _ = s.CommunicationActionByTurn(fixture.Turn.TurnID)
		turn, _ = s.DialogueTurnByID(fixture.Turn.TurnID)
		aggregate, err = s.CommunicationV4AggregateByProfile(fixture.ProfileID)
		if action == nil ||
			action.Status != CommunicationActionManualRequired ||
			action.FailureReason != "effectFailed" ||
			action.SentAt != nil ||
			turn == nil ||
			turn.Status != DialogueTurnManualRequired ||
			turn.FailureReason != "effectFailed" ||
			err != nil ||
			aggregate.Revision != 5 ||
			aggregate.ProjectedThroughSeq != beforeConfirmation.ProjectedThroughSeq ||
			!reflect.DeepEqual(aggregate.State, beforeConfirmation.State) ||
			aggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
			aggregate.ManualReason != "effectSuspect" {
			t.Fatalf("迟到安全终局未补偿 V4 正证: action=%+v turn=%+v aggregate=%+v err=%v",
				action, turn, aggregate, err)
		}
		var activeMessages, retractions int64
		if err := s.db.Model(&Message{}).
			Where("outbound_intent_id = ? AND retracted_at IS NULL", req.Intent.IntentID).
			Count(&activeMessages).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.db.Model(&CommunicationV4ProjectionApplication{}).
			Where("profile_id = ? AND input_kind = ? AND input_key = ?",
				fixture.ProfileID, CommunicationV4InputRetractedAction, fixture.Action.ActionID).
			Count(&retractions).Error; err != nil {
			t.Fatal(err)
		}
		if activeMessages != 0 || retractions != 1 {
			t.Fatalf("迟到安全终局仍留下活动消息或补偿增生: active=%d retractions=%d",
				activeMessages, retractions)
		}
	})
}

// 跨越判据只拦候选人真实输入：我方无主出站行（平台在我方动作后自动留下的
// 卡片跃迁、真人手打消息）没有任何确认动作会认领，卡住游标会让该档案之后
// 的每一条出站都撞墙（2026-08-01 客户机验证读死循环事故）。
func TestCommunicationV4ConfirmedActionSkipPolicy(t *testing.T) {
	cases := []struct {
		name      string
		direction string
		kind      string
		cardType  string
		blocked   bool
	}{
		{name: "我方卡片跃迁行放行", direction: "out", kind: "card", cardType: "wechatExchange"},
		{name: "真人手打出站行放行", direction: "out", kind: "text"},
		{name: "平台系统行放行", direction: "system", kind: "system"},
		{name: "候选人系统提示放行", direction: "in", kind: "system"},
		{name: "候选人真实文字必须拦下", direction: "in", kind: "text", blocked: true},
		{name: "候选人卡片必须拦下", direction: "in", kind: "card", cardType: "wechatExchange", blocked: true},
	}
	for index := range cases {
		testCase := cases[index]
		t.Run(testCase.name, func(t *testing.T) {
			s := openTest(t)
			at := time.Now().UTC().Truncate(time.Millisecond)
			profileID := "v4-skip-policy"
			conversationRef := "conversation-v4-skip-policy"
			fixture, root := seedSuccessfulV4Greeting(t, s, profileID, conversationRef, at)
			text := "跨越判据样本"
			message := Message{
				Platform: fixture.Platform, AccountRef: fixture.AccountRef,
				ConversationRef: conversationRef, Seq: 2,
				Direction: testCase.direction, Kind: testCase.kind,
				CardType:    testCase.cardType,
				ContentHash: "skip-policy-hash", Text: &text, Origin: "external",
				CreatedAt: at, UpdatedAt: at,
			}
			if err := s.db.Create(&message).Error; err != nil {
				t.Fatal(err)
			}
			err := s.db.Transaction(func(tx *gorm.DB) error {
				_, _, _, applyErr := applyCommunicationV4ConfirmedActionTx(
					tx,
					root.ProfileID,
					communication.V4ConfirmedAction{
						ActionKey: "skip-policy-action", Kind: communication.V4ActionReplyText,
						MessageSeq: 3,
					},
					at.Add(time.Minute),
				)
				return applyErr
			})
			blocked := errors.Is(err, ErrCommunicationV4Conflict)
			if blocked != testCase.blocked {
				t.Fatalf("跨越判据与预期不符: blocked=%v want=%v err=%v",
					blocked, testCase.blocked, err)
			}
		})
	}
}

// 账本缺行不再拦（2026-08-01 甲方裁决）：它是极小概率事件，为它把跨越判据
// 收严的代价是整条出站链撞墙，不相称。跨越只对候选人真实输入负责。
func TestCommunicationV4ConfirmedActionAllowsProjectionGap(t *testing.T) {
	s := openTest(t)
	at := time.Now().UTC().Truncate(time.Millisecond)
	_, root := seedSuccessfulV4Greeting(
		t,
		s,
		"v4-confirmed-gap",
		"conversation-v4-confirmed-gap",
		at,
	)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		_, _, _, err := applyCommunicationV4ConfirmedActionTx(
			tx,
			root.ProfileID,
			communication.V4ConfirmedAction{
				ActionKey: "gap-action", Kind: communication.V4ActionReplyText,
				MessageSeq: 3,
			},
			at.Add(time.Minute),
		)
		return err
	})
	if errors.Is(err, ErrCommunicationV4Conflict) {
		t.Fatalf("账本缺行不应被跨越判据拦下: %v", err)
	}
}

// seedCommunicationV4ResolvedFailedVerdict 走完整真实链构造"裁决即恢复"后
// 的终局形状:V4 自动回复 WAL → 验证穷尽 suspect → 人工裁决 resolvedFailed,
// 并自证恢复已生效(轮 superseded/resolvedFailedSuperseded、聚合 active)。
func seedCommunicationV4ResolvedFailedVerdict(
	t *testing.T,
	s *Store,
	suffix string,
) (
	communicationV4AutomaticEffectFixture,
	CreateEffectIntentRequest,
	*CreateEffectIntentResult,
	time.Time,
) {
	t.Helper()
	fixture, req, created := createCommunicationV4AutomaticEffect(t, s, suffix)
	verifyAt := fixture.Now.Add(time.Minute)
	if err := s.MoveEffectToVerification(
		created.Command.MsgID,
		"resultLost",
		verifyAt,
	); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkEffectSuspect(
		created.Command.MsgID,
		"verificationExhausted",
		verifyAt.Add(time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	resolvedAt := verifyAt.Add(2 * time.Minute)
	if err := s.ResolveSuspectVerdict(ResolveSuspectVerdictRequest{
		Ref: created.Command.MsgID, Verdict: CmdResolvedFailed,
		ConversationKey: ConversationKey{
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			ConversationRef: fixture.ConversationRef,
		},
		At: resolvedAt,
	}); err != nil {
		t.Fatal(err)
	}
	turn, _ := s.DialogueTurnByID(fixture.Turn.TurnID)
	action, _ := s.CommunicationActionByTurn(fixture.Turn.TurnID)
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if turn == nil || turn.Status != DialogueTurnSuperseded ||
		turn.FailureReason != dialogueTurnResolvedFailedSuperseded ||
		action == nil || action.Status != CommunicationActionManualRequired ||
		action.FailureReason != "effectResolvedFailed" ||
		err != nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive {
		t.Fatalf("裁决即恢复前置不成立: turn=%+v action=%+v aggregate=%+v err=%v",
			turn, action, aggregate, err)
	}
	return fixture, req, created, resolvedAt
}

func countLateVerdictAudits(t *testing.T, s *Store, detailFragment string) int64 {
	t.Helper()
	var audits int64
	if err := s.db.Model(&AuditEntry{}).
		Where(
			"category = ? AND detail LIKE ?",
			auditCategoryLateResultAfterVerdict,
			"%"+detailFragment+"%",
		).
		Count(&audits).Error; err != nil {
		t.Fatal(err)
	}
	return audits
}

// TestCommunicationV4VerdictThenLateSafeTerminalSettlesWithoutRewrite 钉住
// 2026-08-03 修复:resolvedFailed 裁决(裁决即恢复,轮已 superseded)之后,
// 手重连补投的迟到 failed+none 必须能整事务入账(ProcessedMsg 落、ack 路径
// 无错),且与裁决同向——动作/轮/聚合原样,只落审计;同一 result 再放一次
// 照样成功短路。修复前该重放撞轮状态白名单整事务回滚,手每次重连重放失败
// 直到 outbox TTL。
func TestCommunicationV4VerdictThenLateSafeTerminalSettlesWithoutRewrite(t *testing.T) {
	s := openTest(t)
	fixture, req, created, resolvedAt := seedCommunicationV4ResolvedFailedVerdict(
		t, s, "late-failed-after-verdict",
	)
	lateAt := resolvedAt.Add(time.Minute)
	lateMsgID := "late-failed-after-verdict-result"
	result, err := s.ApplyResultMessage(
		created.Command.MsgID,
		lateMsgID,
		"result",
		fixture.HandID,
		func(cmd *CmdRecord) (ResultCommandMutation, error) {
			// 镜像 dispatch 层 wasHumanResolved 纠正:failed+none 覆写人裁。
			cmd.Status = CmdFailed
			cmd.SideEffect = "none"
			cmd.TerminalAt = &lateAt
			return ResultCommandMutation{Save: true, Effect: &EffectResultMutation{
				IntentStatus: EffectIntentFailed, Retract: true,
				Reason: "lateFailedNone",
			}}, nil
		},
	)
	if err != nil || result == nil || !result.CommandFound || result.AlreadyProcessed {
		t.Fatalf("裁决后迟到安全终局必须可入账: result=%+v err=%v", result, err)
	}
	var processed int64
	if err := s.db.Model(&ProcessedMsg{}).
		Where("msg_id = ?", lateMsgID).
		Count(&processed).Error; err != nil || processed != 1 {
		t.Fatalf("ProcessedMsg 必须落库(ack 前提): count=%d err=%v", processed, err)
	}
	intent, err := s.EffectIntentByID(req.Intent.IntentID)
	if err != nil || intent == nil || intent.Status != EffectIntentFailed {
		t.Fatalf("dispatch 层 intent 覆写必须落地: %+v err=%v", intent, err)
	}
	action, _ := s.CommunicationActionByTurn(fixture.Turn.TurnID)
	turn, _ := s.DialogueTurnByID(fixture.Turn.TurnID)
	aggregate, aggregateErr := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if action == nil || action.Status != CommunicationActionManualRequired ||
		action.FailureReason != "effectResolvedFailed" || action.SentAt != nil ||
		turn == nil || turn.Status != DialogueTurnSuperseded ||
		turn.FailureReason != dialogueTurnResolvedFailedSuperseded ||
		aggregateErr != nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive ||
		aggregate.ManualReason != "" {
		t.Fatalf("同向迟到终局不得改写裁决账本或再冻聚合: action=%+v turn=%+v aggregate=%+v err=%v",
			action, turn, aggregate, aggregateErr)
	}
	var messages int64
	if err := s.db.Model(&Message{}).
		Where("outbound_intent_id = ?", req.Intent.IntentID).
		Count(&messages).Error; err != nil || messages != 0 {
		t.Fatalf("迟到安全终局不得伪造消息: count=%d err=%v", messages, err)
	}
	if audits := countLateVerdictAudits(t, s, "direction=consistent"); audits != 1 {
		t.Fatalf("迟到重放必须落审计: count=%d", audits)
	}
	// 同一 result 重放:ProcessedMsg 挡在 mutate 之前,第二次照样成功。
	replay, err := s.ApplyResultMessage(
		created.Command.MsgID,
		lateMsgID,
		"result",
		fixture.HandID,
		func(cmd *CmdRecord) (ResultCommandMutation, error) {
			t.Fatal("同 msgId 重放不得进入 mutate")
			return ResultCommandMutation{}, nil
		},
	)
	if err != nil || replay == nil || !replay.AlreadyProcessed {
		t.Fatalf("迟到 result 重放必须幂等成功: result=%+v err=%v", replay, err)
	}
	if audits := countLateVerdictAudits(t, s, "direction=consistent"); audits != 1 {
		t.Fatalf("重放不得增生审计: count=%d", audits)
	}
}

// TestCommunicationV4VerdictThenLatePositiveCorrectsToSent 钉住 2026-08-03
// 修复的反向腿:裁决判"未发"而迟到 durable ok 证明实则已发(历史最贵教训
// 方向)。intent 保留 dispatch 层覆写的 ok,动作 CAS 转 sent,轮保持
// superseded 终局不回写;干净尾部时出站行经既有 confirmed-action 应用收编
// 进 V4 投影,游标间存在未投影候选人输入时按形状拒绝回落"仅动作入账+
// 响亮审计",整事务照样成功、ack 照发。
func TestCommunicationV4VerdictThenLatePositiveCorrectsToSent(t *testing.T) {
	t.Run("clean tail incorporates", func(t *testing.T) {
		s := openTest(t)
		fixture, req, created, resolvedAt := seedCommunicationV4ResolvedFailedVerdict(
			t, s, "late-ok-after-verdict",
		)
		lateAt := resolvedAt.Add(time.Minute)
		result, err := s.ApplyResultMessage(
			created.Command.MsgID,
			"late-ok-after-verdict-result",
			"result",
			fixture.HandID,
			func(cmd *CmdRecord) (ResultCommandMutation, error) {
				cmd.Status = CmdOk
				cmd.TerminalAt = &lateAt
				return ResultCommandMutation{Save: true, Effect: &EffectResultMutation{
					IntentStatus: EffectIntentOk, Append: true,
					Text:         fixture.Action.Text,
					ContentHash:  fixture.Action.ContentHash,
					ObservedAtMs: lateAt.UnixMilli(),
				}}, nil
			},
		)
		if err != nil || result == nil || !result.CommandFound || result.AlreadyProcessed {
			t.Fatalf("裁决后迟到正证必须可入账: result=%+v err=%v", result, err)
		}
		intent, err := s.EffectIntentByID(req.Intent.IntentID)
		if err != nil || intent == nil || intent.Status != EffectIntentOk ||
			intent.ResultMessageSeq == nil {
			t.Fatalf("迟到正证 intent 纠正未落地: %+v err=%v", intent, err)
		}
		action, _ := s.CommunicationActionByTurn(fixture.Turn.TurnID)
		turn, _ := s.DialogueTurnByID(fixture.Turn.TurnID)
		aggregate, aggregateErr := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
		if action == nil || action.Status != CommunicationActionSent ||
			action.FailureReason != "" || action.SentAt == nil ||
			turn == nil || turn.Status != DialogueTurnSuperseded ||
			turn.FailureReason != dialogueTurnResolvedFailedSuperseded ||
			aggregateErr != nil ||
			aggregate.AutomationStatus != ProfileCommunicationAutomationActive {
			t.Fatalf("迟到正证纠正形状不符: action=%+v turn=%+v aggregate=%+v err=%v",
				action, turn, aggregate, aggregateErr)
		}
		if aggregate.ProjectedThroughSeq != *intent.ResultMessageSeq ||
			aggregate.State.LastOutboundMessageSeq != *intent.ResultMessageSeq {
			t.Fatalf("干净尾部必须收编出站行: aggregate=%+v seq=%d",
				aggregate, *intent.ResultMessageSeq)
		}
		var confirmations int64
		if err := s.db.Model(&CommunicationV4ProjectionApplication{}).
			Where("profile_id = ? AND input_kind = ?",
				fixture.ProfileID, CommunicationV4InputConfirmedAction).
			Count(&confirmations).Error; err != nil || confirmations != 1 {
			t.Fatalf("确认应用必须恰一条: count=%d err=%v", confirmations, err)
		}
		if audits := countLateVerdictAudits(t, s, "incorporation=confirmed"); audits != 1 {
			t.Fatalf("迟到正证必须落审计: count=%d", audits)
		}
		replay, err := s.ApplyResultMessage(
			created.Command.MsgID,
			"late-ok-after-verdict-result",
			"result",
			fixture.HandID,
			func(cmd *CmdRecord) (ResultCommandMutation, error) {
				t.Fatal("同 msgId 重放不得进入 mutate")
				return ResultCommandMutation{}, nil
			},
		)
		if err != nil || replay == nil || !replay.AlreadyProcessed {
			t.Fatalf("迟到正证重放必须幂等成功: result=%+v err=%v", replay, err)
		}
	})

	t.Run("unprojected candidate input falls back to action-only", func(t *testing.T) {
		s := openTest(t)
		fixture, req, created, resolvedAt := seedCommunicationV4ResolvedFailedVerdict(
			t, s, "late-ok-gap",
		)
		// 候选人在裁决后、手重连前又发了一条(夜间常态):它尚未投影,
		// 确认应用的 unclaimed 闸必须拒绝跨越,纠正回落"仅动作入账"。
		interleaved := "裁决后候选人插话"
		appendCommunicationV4Inbound(t, s, fixture.resumeStoreFixture, Message{
			Seq: 3, Direction: "in", Kind: "text",
			ContentHash: textcanon.Hash(interleaved), Text: &interleaved,
		})
		aggregateBefore, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
		if err != nil {
			t.Fatal(err)
		}
		lateAt := resolvedAt.Add(time.Minute)
		result, err := s.ApplyResultMessage(
			created.Command.MsgID,
			"late-ok-gap-result",
			"result",
			fixture.HandID,
			func(cmd *CmdRecord) (ResultCommandMutation, error) {
				cmd.Status = CmdOk
				cmd.TerminalAt = &lateAt
				return ResultCommandMutation{Save: true, Effect: &EffectResultMutation{
					IntentStatus: EffectIntentOk, Append: true,
					Text:         fixture.Action.Text,
					ContentHash:  fixture.Action.ContentHash,
					ObservedAtMs: lateAt.UnixMilli(),
				}}, nil
			},
		)
		if err != nil || result == nil || !result.CommandFound || result.AlreadyProcessed {
			t.Fatalf("形状拒绝不得打回整事务: result=%+v err=%v", result, err)
		}
		intent, err := s.EffectIntentByID(req.Intent.IntentID)
		action, _ := s.CommunicationActionByTurn(fixture.Turn.TurnID)
		aggregate, aggregateErr := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
		if err != nil || intent == nil || intent.Status != EffectIntentOk ||
			action == nil || action.Status != CommunicationActionSent ||
			aggregateErr != nil ||
			aggregate.Revision != aggregateBefore.Revision ||
			aggregate.ProjectedThroughSeq != aggregateBefore.ProjectedThroughSeq {
			t.Fatalf("回落腿必须仅动作入账、不动投影: intent=%+v action=%+v aggregate=%+v errs=%v/%v",
				intent, action, aggregate, err, aggregateErr)
		}
		var confirmations int64
		if err := s.db.Model(&CommunicationV4ProjectionApplication{}).
			Where("profile_id = ? AND input_kind = ?",
				fixture.ProfileID, CommunicationV4InputConfirmedAction).
			Count(&confirmations).Error; err != nil || confirmations != 0 {
			t.Fatalf("形状拒绝时不得落确认应用: count=%d err=%v", confirmations, err)
		}
		if audits := countLateVerdictAudits(t, s, "incorporation=actionOnly"); audits != 1 {
			t.Fatalf("回落腿必须响亮审计: count=%d", audits)
		}
	})
}
