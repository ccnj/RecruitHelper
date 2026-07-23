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
	actions, err = s.CommunicationActionsByTurn(frozen.Turn.TurnID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != CommunicationActionManualRequired {
		t.Fatalf("正文失败不得实体化卡片: actions=%+v err=%v", actions, err)
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

func TestCommunicationV4AutomaticFailureRequiresManualWithoutTrial(t *testing.T) {
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
	action, err := s.CommunicationActionByTurn(fixture.Turn.TurnID)
	if err != nil || action == nil ||
		action.Status != CommunicationActionManualRequired ||
		action.FailureReason != "effectFailed" || action.SentAt != nil {
		t.Fatalf("V4 failed action 未转人工: action=%+v err=%v", action, err)
	}
	turn, _ := s.DialogueTurnByID(fixture.Turn.TurnID)
	aggregate, aggregateErr := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if turn == nil || turn.Status != DialogueTurnManualRequired ||
		turn.FailureReason != "effectFailed" ||
		aggregateErr != nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
		aggregate.ManualReason != "effectFailed" ||
		aggregate.ProjectedThroughSeq != 2 {
		t.Fatalf("V4 failed 未原子收敛: turn=%+v aggregate=%+v err=%v",
			turn, aggregate, aggregateErr)
	}
	var messages int64
	if err := s.db.Model(&Message{}).
		Where("outbound_intent_id = ?", req.Intent.IntentID).
		Count(&messages).Error; err != nil || messages != 0 {
		t.Fatalf("V4 failed 不得伪造消息: count=%d err=%v", messages, err)
	}
}

func TestCommunicationV4AutomaticSuspectVerdictsPreserveFirstManualReason(t *testing.T) {
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
		aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
		if action == nil || action.Status != CommunicationActionManualRequired ||
			action.FailureReason != "effectResolvedFailed" ||
			err != nil ||
			aggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
			aggregate.ManualReason != "effectSuspect" {
			t.Fatalf("resolvedFailed 覆盖了首次人工原因或未终局: action=%+v aggregate=%+v err=%v",
				action, aggregate, err)
		}
		var messages int64
		if err := s.db.Model(&Message{}).
			Where("outbound_intent_id = ?", req.Intent.IntentID).
			Count(&messages).Error; err != nil || messages != 0 {
			t.Fatalf("resolvedFailed 不得追加消息: count=%d err=%v", messages, err)
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

func TestCommunicationV4ConfirmedActionRejectsProjectionGap(t *testing.T) {
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
	if err == nil {
		t.Fatal("跨过未投影 seq 的正证必须拒绝")
	}
	aggregate, aggregateErr := s.CommunicationV4AggregateByProfile(root.ProfileID)
	if aggregateErr != nil || aggregate.Revision != 0 ||
		aggregate.ProjectedThroughSeq != 1 {
		t.Fatalf("序号缺口失败事务污染聚合: aggregate=%+v err=%v",
			aggregate, aggregateErr)
	}
}
