package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/contract/gen/go/protocol"

	"gorm.io/gorm"
)

type communicationV4EventEffectFixture struct {
	resumeStoreFixture
	FreezeRequest FreezeDialogueTurnRequest
	Turn          DialogueTurn
	Action        CommunicationV4EventAction
	Now           time.Time
}

func seedCommunicationV4WechatReceiptEffect(
	t *testing.T,
	s *Store,
	suffix string,
) communicationV4EventEffectFixture {
	t.Helper()
	profileID := "profile-v4-event-effect-" + suffix
	fixture := seedReadyCommunicationTarget(t, s, profileID)
	setCommunicationV4FixedPhrasePackage(t, s, "revision-"+profileID)
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "card", CardType: "wechatExchange",
		CardState: "accepted", ContentHash: "wechat-accepted-" + suffix,
	})
	req := communicationV4TurnRequest(t, s, fixture, inbound)
	frozen, err := s.FreezeCommunicationV4Turn(req)
	if err != nil || frozen.Turn.Status != DialogueTurnCompleted {
		t.Fatalf("换微信回执轮冻结失败: result=%+v err=%v", frozen, err)
	}
	actions, err := s.CommunicationV4EventActionsBySource(
		profileID,
		CommunicationV4InputDialogueTurn,
		frozen.Turn.TurnID,
	)
	if err != nil {
		t.Fatal(err)
	}
	var receipt *CommunicationV4EventAction
	for index := range actions {
		if actions[index].V4Kind == communication.V4ActionWechatReceipt {
			copy := actions[index]
			receipt = &copy
			break
		}
	}
	if receipt == nil || receipt.Status != CommunicationV4EventActionPlanned {
		t.Fatalf("换微信回执动作未就绪: %+v", actions)
	}
	return communicationV4EventEffectFixture{
		resumeStoreFixture: fixture,
		FreezeRequest:      req,
		Turn:               frozen.Turn,
		Action:             *receipt,
		Now:                req.FrozenAt.Add(time.Second),
	}
}

func seedCommunicationV4InterviewEventActions(
	t *testing.T,
	s *Store,
	suffix string,
) (
	communicationV4EventEffectFixture,
	CommunicationV4EventAction,
	CommunicationV4EventAction,
) {
	t.Helper()
	profileID := "profile-v4-event-combo-" + suffix
	fixture := seedReadyCommunicationTarget(t, s, profileID)
	setCommunicationV4FixedPhrasePackage(t, s, "revision-"+profileID)
	at := time.Now().UTC().Truncate(time.Millisecond)
	candidateText := "合成前置候选人消息"
	appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "text",
		Text: &candidateText, ContentHash: "event-combo-candidate-" + suffix,
		CreatedAt: at,
	})
	if _, err := s.ApplyCommunicationV4BusinessEvent(
		ApplyCommunicationV4BusinessEventRequest{
			ProfileID: profileID,
			Event: communication.BusinessEvent{
				Key: "message:2", Kind: communication.EventCandidateExpressionReceived,
				Source: communication.EventSourceMessage, MessageSeq: 2,
				ExpressionKind: communication.ExpressionText, Text: candidateText,
			},
			AppliedAt: at,
		},
	); err != nil {
		t.Fatal(err)
	}
	inviteMessage := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 3, Direction: "out", Kind: "card", CardType: "interviewInvite",
		CardState: "pending", ContentHash: "event-combo-interview-" + suffix,
		Origin: "self", CreatedAt: at.Add(time.Second),
	})[0]
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		_, _, _, err := applyCommunicationV4ConfirmedActionTx(
			tx,
			profileID,
			communication.V4ConfirmedAction{
				ActionKey:  "fixture-interview-invite-" + suffix,
				Kind:       communication.V4ActionInterviewInvite,
				MessageSeq: inviteMessage.Seq,
				SentAt:     &inviteMessage.CreatedAt,
			},
			inviteMessage.CreatedAt,
		)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	accepted := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 4, Direction: "in", Kind: "card", CardType: "interviewInvite",
		CardState: "accepted", ContentHash: "event-combo-accepted-" + suffix,
		CreatedAt: at.Add(2 * time.Second),
	})
	digest, turnID, err := DialogueTurnIdentity(profileID, inviteMessage, accepted)
	if err != nil {
		t.Fatal(err)
	}
	material, ready, err := s.CommunicationAIMaterialForProfile(profileID)
	if err != nil || !ready {
		t.Fatalf("邀面接受轮材料未就绪: ready=%v err=%v", ready, err)
	}
	freezeReq := FreezeDialogueTurnRequest{
		TurnID: turnID, ProfileID: profileID, ConversationRef: fixture.ConversationRef,
		InputDigest: digest, HistoryThroughSeq: inviteMessage.Seq,
		InboundFromSeq: accepted[0].Seq, InboundThroughSeq: accepted[0].Seq,
		ContextRevisionHash: material.ContextRevision.RevisionHash,
		ResumeSnapshotID:    material.ResumeSnapshot.SnapshotID,
		RecommendedTimeText: "合成推荐时段",
		RenderFormatVersion: m5ai.DialogueRenderFormatVersion,
		FrozenAt:            at.Add(3 * time.Second),
	}
	frozen, err := s.FreezeCommunicationV4Turn(freezeReq)
	if err != nil {
		t.Fatal(err)
	}
	actions, err := s.CommunicationV4EventActionsBySource(
		profileID,
		CommunicationV4InputDialogueTurn,
		turnID,
	)
	if err != nil {
		t.Fatal(err)
	}
	var receipt, invite *CommunicationV4EventAction
	for index := range actions {
		switch actions[index].V4Kind {
		case communication.V4ActionInterviewAcceptedReceipt:
			copy := actions[index]
			receipt = &copy
		case communication.V4ActionInviteWechat:
			copy := actions[index]
			invite = &copy
		}
	}
	if receipt == nil || invite == nil {
		t.Fatalf("邀面接受动作不完整: %+v", actions)
	}
	eventFixture := communicationV4EventEffectFixture{
		resumeStoreFixture: fixture,
		FreezeRequest:      freezeReq,
		Turn:               frozen.Turn,
		Action:             *receipt,
		Now:                freezeReq.FrozenAt.Add(time.Second),
	}
	return eventFixture, *receipt, *invite
}

func communicationV4EventEffectRequest(
	t *testing.T,
	s *Store,
	fixture communicationV4EventEffectFixture,
	action CommunicationV4EventAction,
	suffix string,
) CreateEffectIntentRequest {
	t.Helper()
	intentID, err := M5AutomaticIntentID(action.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	primitive := communicationV4EventActionPrimitive(action)
	var args []byte
	switch primitive {
	case primitiveChatSendMessage:
		args, err = protocol.Encode(protocol.ChatSendMessageArgs{
			ConversationRef: fixture.ConversationRef,
			Text:            action.Text,
		})
	case primitiveChatSendWechatInvite:
		args, err = protocol.Encode(protocol.ChatSendWechatInviteArgs{
			ConversationRef: fixture.ConversationRef,
		})
	default:
		t.Fatalf("测试动作原语不可执行: %+v", action)
	}
	if err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestEffectIntent(
		fixture.Platform,
		fixture.AccountRef,
		fixture.ConversationRef,
	)
	if err != nil {
		t.Fatal(err)
	}
	previousIntentID := ""
	if latest != nil {
		previousIntentID = latest.IntentID
	}
	var conversation Conversation
	if err := s.db.First(
		&conversation,
		"platform = ? AND account_ref = ? AND conversation_ref = ?",
		fixture.Platform,
		fixture.AccountRef,
		fixture.ConversationRef,
	).Error; err != nil {
		t.Fatal(err)
	}
	deadline := fixture.Now.Add(time.Hour).UnixMilli()
	idemKey := "idem-v4-event-effect-" + suffix
	return CreateEffectIntentRequest{
		Intent: EffectIntent{
			IntentID: intentID, IdemKey: idemKey,
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			Primitive: primitive, TargetRef: fixture.ConversationRef,
			PayloadHash: "payload-v4-event-effect-" + suffix,
			GuardsHash:  "guards-v4-event-effect-" + suffix,
			Status:      EffectIntentDispatching, DeadlineMs: deadline,
			SendFingerprint: action.ContentHash,
		},
		Command: CmdRecord{
			MsgID: "msg-v4-event-effect-" + suffix,
			Name:  primitive, Class: "effectful", IdemKey: idemKey,
			Domain:   fixture.Platform + ":" + fixture.AccountRef,
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			ExpectedPrincipalFingerprint: fixture.Principal,
			IntentID:                     intentID,
			HandID:                       fixture.HandID,
			Session:                      fixture.Session,
			BootIDAtDispatch:             fixture.BootID,
			Args:                         string(args),
			Status:                       CmdQueued,
			DeadlineMs:                   deadline,
			ExecBudgetMs:                 60_000,
		},
		ExpectedTailSeq:   conversation.LastMessageSeq,
		PreviousIntentID:  previousIntentID,
		AutomaticActionID: action.ActionID,
		Now:               fixture.Now,
	}
}

func settleCommunicationV4EventTextEffect(
	t *testing.T,
	s *Store,
	fixture communicationV4EventEffectFixture,
	action CommunicationV4EventAction,
	created *CreateEffectIntentResult,
	suffix string,
) {
	t.Helper()
	resultAt := fixture.Now.Add(time.Minute)
	if _, err := s.ApplyResultMessage(
		created.Command.MsgID,
		"result-v4-event-effect-"+suffix,
		"result",
		fixture.HandID,
		func(command *CmdRecord) (ResultCommandMutation, error) {
			command.Status = CmdOk
			command.TerminalAt = &resultAt
			return ResultCommandMutation{
				Save: true,
				Effect: &EffectResultMutation{
					IntentStatus: EffectIntentOk,
					Append:       true,
					Text:         action.Text,
					ContentHash:  action.ContentHash,
					ObservedAtMs: resultAt.UnixMilli(),
				},
			}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
}

func TestCommunicationV4EventReceiptConstructsWALAndReplays(t *testing.T) {
	s := openTest(t)
	fixture := seedCommunicationV4WechatReceiptEffect(t, s, "construct")
	req := communicationV4EventEffectRequest(
		t,
		s,
		fixture,
		fixture.Action,
		"construct",
	)
	created, err := s.CreateEffectIntentAndCmd(req)
	if err != nil || !created.Created {
		t.Fatalf("事件回执 WAL 构造失败: result=%+v err=%v", created, err)
	}
	var action CommunicationV4EventAction
	if err := s.db.First(&action, "action_id = ?", fixture.Action.ActionID).Error; err != nil {
		t.Fatal(err)
	}
	if action.Status != CommunicationV4EventActionEffectPending ||
		action.EffectIntentID == nil ||
		*action.EffectIntentID != req.Intent.IntentID ||
		action.EffectStartedAt == nil {
		t.Fatalf("事件回执未与 WAL 原子绑定: %+v", action)
	}
	if err := s.ValidateM5AutomaticIntentLink(
		action.ActionID,
		req.Intent.IntentID,
	); err != nil {
		t.Fatalf("事件回执 action→intent 绑定无效: %v", err)
	}
	replayed, err := s.CreateEffectIntentAndCmd(req)
	if err != nil || replayed.Created ||
		replayed.Intent.IntentID != created.Intent.IntentID ||
		replayed.Command.MsgID != created.Command.MsgID {
		t.Fatalf("事件回执 WAL 重放发生增生: result=%+v err=%v", replayed, err)
	}
}

func TestCommunicationV4EventReceiptPositiveEvidenceProjectsSemanticKey(t *testing.T) {
	s := openTest(t)
	fixture := seedCommunicationV4WechatReceiptEffect(t, s, "positive")
	req := communicationV4EventEffectRequest(
		t,
		s,
		fixture,
		fixture.Action,
		"positive",
	)
	created, err := s.CreateEffectIntentAndCmd(req)
	if err != nil {
		t.Fatal(err)
	}
	settleCommunicationV4EventTextEffect(
		t,
		s,
		fixture,
		fixture.Action,
		created,
		"positive",
	)
	var action CommunicationV4EventAction
	if err := s.db.First(&action, "action_id = ?", fixture.Action.ActionID).Error; err != nil {
		t.Fatal(err)
	}
	if action.Status != CommunicationV4EventActionSent ||
		action.SentAt == nil ||
		action.FailureReason != "" {
		t.Fatalf("事件回执正证未收束 sent: %+v", action)
	}
	var confirmed CommunicationV4ProjectionApplication
	if err := s.db.First(
		&confirmed,
		"profile_id = ? AND input_kind = ? AND input_key = ?",
		fixture.ProfileID,
		CommunicationV4InputConfirmedAction,
		fixture.Action.SemanticActionKey,
	).Error; err != nil {
		t.Fatal(err)
	}
	if confirmed.InputKey == fixture.Action.ActionID ||
		confirmed.SemanticKind != string(communication.V4ActionWechatReceipt) ||
		confirmed.MessageSeq != 3 {
		t.Fatalf("正证投影没有使用语义 action key: %+v", confirmed)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil ||
		!aggregate.State.WechatReceiptSent ||
		aggregate.ProjectedThroughSeq != 3 {
		t.Fatalf("事件回执未推进 V4 状态/游标: aggregate=%+v err=%v",
			aggregate, err)
	}
}

func TestCommunicationV4EventActionManualStillValidatesIntentReplay(t *testing.T) {
	s := openTest(t)
	fixture := seedCommunicationV4WechatReceiptEffect(t, s, "manual-replay")
	req := communicationV4EventEffectRequest(
		t,
		s,
		fixture,
		fixture.Action,
		"manual-replay",
	)
	created, err := s.CreateEffectIntentAndCmd(req)
	if err != nil {
		t.Fatal(err)
	}
	failedAt := fixture.Now.Add(time.Minute)
	if _, err := s.ApplyResultMessage(
		created.Command.MsgID,
		"result-v4-event-effect-manual-replay",
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
	var action CommunicationV4EventAction
	if err := s.db.First(&action, "action_id = ?", fixture.Action.ActionID).Error; err != nil {
		t.Fatal(err)
	}
	aggregate, aggregateErr := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if action.Status != CommunicationV4EventActionManualRequired ||
		action.FailureReason != "effectFailed" ||
		action.EffectIntentID == nil ||
		aggregateErr != nil ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired {
		t.Fatalf("失败事件动作未收敛转人工: action=%+v aggregate=%+v err=%v",
			action, aggregate, aggregateErr)
	}
	if err := s.ValidateM5AutomaticIntentLink(
		action.ActionID,
		req.Intent.IntentID,
	); err != nil {
		t.Fatalf("已绑定 manual 动作不能校验原 intent: %v", err)
	}
	replayed, err := s.CreateEffectIntentAndCmd(req)
	if err != nil || replayed.Created ||
		replayed.Intent.IntentID != req.Intent.IntentID {
		t.Fatalf("manual 后原 WAL 重放未收编: result=%+v err=%v", replayed, err)
	}
	replayedTurn, err := s.FreezeCommunicationV4Turn(fixture.FreezeRequest)
	if err != nil || replayedTurn.Created {
		t.Fatalf("manual disposition 未通过来源重放校验: result=%+v err=%v",
			replayedTurn, err)
	}
}

func TestCommunicationV4EventActionMaterialMismatchRollsBackWAL(t *testing.T) {
	s := openTest(t)
	fixture := seedCommunicationV4WechatReceiptEffect(t, s, "mismatch")
	req := communicationV4EventEffectRequest(
		t,
		s,
		fixture,
		fixture.Action,
		"mismatch",
	)
	badArgs, err := protocol.Encode(protocol.ChatSendMessageArgs{
		ConversationRef: fixture.ConversationRef,
		Text:            "被替换的正文",
	})
	if err != nil {
		t.Fatal(err)
	}
	req.Command.Args = string(badArgs)
	if _, err := s.CreateEffectIntentAndCmd(req); !errors.Is(
		err,
		ErrCommunicationActionConflict,
	) {
		t.Fatalf("动作正文与命令不一致未阻断: %v", err)
	}
	var action CommunicationV4EventAction
	if err := s.db.First(&action, "action_id = ?", fixture.Action.ActionID).Error; err != nil {
		t.Fatal(err)
	}
	var intents, commands, heads int64
	_ = s.db.Model(&EffectIntent{}).
		Where("intent_id = ?", req.Intent.IntentID).
		Count(&intents).Error
	_ = s.db.Model(&CmdRecord{}).
		Where("intent_id = ?", req.Intent.IntentID).
		Count(&commands).Error
	_ = s.db.Model(&ConversationEffectHead{}).
		Where(
			"platform = ? AND account_ref = ? AND conversation_ref = ?",
			fixture.Platform,
			fixture.AccountRef,
			fixture.ConversationRef,
		).
		Count(&heads).Error
	if action.Status != CommunicationV4EventActionPlanned ||
		action.EffectIntentID != nil ||
		intents != 0 ||
		commands != 0 ||
		heads != 0 {
		t.Fatalf("材料冲突留下半态: action=%+v intents=%d commands=%d heads=%d",
			action, intents, commands, heads)
	}
}

func TestCommunicationV4EventInviteUsesEventReceiptParent(t *testing.T) {
	s := openTest(t)
	fixture, receipt, invite := seedCommunicationV4InterviewEventActions(
		t,
		s,
		"event-parent",
	)
	childBeforeParent := communicationV4EventEffectRequest(
		t,
		s,
		fixture,
		invite,
		"event-parent-child-too-early",
	)
	if _, err := s.CreateEffectIntentAndCmd(childBeforeParent); err == nil {
		t.Fatal("回执正证前不得构造换微信卡 WAL")
	}
	parentReq := communicationV4EventEffectRequest(
		t,
		s,
		fixture,
		receipt,
		"event-parent-receipt",
	)
	parentCreated, err := s.CreateEffectIntentAndCmd(parentReq)
	if err != nil {
		t.Fatal(err)
	}
	settleCommunicationV4EventTextEffect(
		t,
		s,
		fixture,
		receipt,
		parentCreated,
		"event-parent-receipt",
	)
	childReq := communicationV4EventEffectRequest(
		t,
		s,
		fixture,
		invite,
		"event-parent-invite",
	)
	if childReq.PreviousIntentID != parentReq.Intent.IntentID {
		t.Fatalf("child 未钉住 parent intent: child=%+v parent=%+v",
			childReq, parentReq.Intent)
	}
	childCreated, err := s.CreateEffectIntentAndCmd(childReq)
	if err != nil || !childCreated.Created {
		t.Fatalf("event parent 正证后 child WAL 构造失败: result=%+v err=%v",
			childCreated, err)
	}
	resultAt := fixture.Now.Add(2 * time.Minute)
	if _, err := s.ApplyResultMessage(
		childCreated.Command.MsgID,
		"result-v4-event-effect-event-parent-invite",
		"result",
		fixture.HandID,
		func(command *CmdRecord) (ResultCommandMutation, error) {
			command.Status = CmdOk
			command.TerminalAt = &resultAt
			return ResultCommandMutation{
				Save: true,
				Effect: &EffectResultMutation{
					IntentStatus: EffectIntentOk,
					ContentHash:  invite.ContentHash,
					Card: &CardResultMutation{
						ConversationRef: fixture.ConversationRef,
						CardType:        "wechatExchange",
						CardState:       "pending",
						ContentHash:     invite.ContentHash,
						SourceKey:       strings.Repeat("d", 64),
					},
				},
			}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	var settled CommunicationV4EventAction
	if err := s.db.First(&settled, "action_id = ?", invite.ActionID).Error; err != nil {
		t.Fatal(err)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if settled.Status != CommunicationV4EventActionSent ||
		err != nil ||
		aggregate.State.WechatState != communication.V4WechatInvited ||
		aggregate.ProjectedThroughSeq != 6 {
		t.Fatalf("event parent→invite 未闭合: action=%+v aggregate=%+v err=%v",
			settled, aggregate, err)
	}
}
