package store

import (
	"encoding/json"
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
	FreezeRequest    FreezeDialogueTurnRequest
	Turn             DialogueTurn
	Action           CommunicationV4EventAction
	RequestSourceKey string
	Now              time.Time
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

func seedCommunicationV4WechatAcceptEffect(
	t *testing.T,
	s *Store,
	suffix string,
) communicationV4EventEffectFixture {
	t.Helper()
	profileID := "profile-v4-event-accept-" + suffix
	fixture := seedReadyCommunicationTarget(t, s, profileID)
	requestSourceKey := strings.Repeat("5", 63) + "a"
	inbound := appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 2, Direction: "in", Kind: "card", CardType: "wechatExchange",
		CardState: "pending", ContentHash: "wechat-request-" + suffix,
		SourceKey: &requestSourceKey,
	})
	req := communicationV4TurnRequest(t, s, fixture, inbound)
	frozen, err := s.FreezeCommunicationV4Turn(req)
	if err != nil ||
		frozen.Application.Outcome.DialogueStatus != communication.V4DialogueWaitingPrerequisite {
		t.Fatalf("主动换微信前置轮冻结失败: result=%+v err=%v", frozen, err)
	}
	actions, err := s.CommunicationV4EventActionsBySource(
		profileID,
		CommunicationV4InputDialogueTurn,
		frozen.Turn.TurnID,
	)
	if err != nil {
		t.Fatal(err)
	}
	var accept *CommunicationV4EventAction
	for index := range actions {
		if actions[index].V4Kind == communication.V4ActionAcceptWechat {
			copy := actions[index]
			accept = &copy
			break
		}
	}
	if accept == nil ||
		accept.Status != CommunicationV4EventActionPlanned ||
		accept.EffectKind != CommunicationV4EventEffectAcceptWechat {
		t.Fatalf("接受微信动作未就绪: %+v", actions)
	}
	return communicationV4EventEffectFixture{
		resumeStoreFixture: fixture,
		FreezeRequest:      req,
		Turn:               frozen.Turn,
		Action:             *accept,
		RequestSourceKey:   requestSourceKey,
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
	aggregateRow, err := s.CommunicationV4AggregateByProfile(profileID)
	if err != nil {
		t.Fatal(err)
	}
	freezeReq := FreezeDialogueTurnRequest{
		TurnID: turnID, ProfileID: profileID, ConversationRef: fixture.ConversationRef,
		InputDigest: digest, HistoryThroughSeq: accepted[0].Seq - 1,
		InboundFromSeq: accepted[0].Seq, InboundThroughSeq: accepted[0].Seq,
		ExpectedProjectedThroughSeq: aggregateRow.ProjectedThroughSeq,
		OutboundAnchorSeq:           inviteMessage.Seq,
		ContextRevisionHash:         material.ContextRevision.RevisionHash,
		ResumeSnapshotID:            material.ResumeSnapshot.SnapshotID,
		RecommendedTimeText:         "合成推荐时段",
		RenderFormatVersion:         m5ai.DialogueRenderFormatVersion,
		FrozenAt:                    at.Add(3 * time.Second),
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
	case primitiveChatAcceptWechat:
		requestSourceKey := fixture.RequestSourceKey
		if requestSourceKey == "" {
			requestSourceKey, err =
				s.CommunicationV4AcceptWechatRequestSource(action.ActionID)
			if err != nil {
				t.Fatal(err)
			}
		}
		args, err = protocol.Encode(protocol.ChatAcceptWechatArgs{
			ConversationRef:  fixture.ConversationRef,
			RequestSourceKey: requestSourceKey,
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

func settleCommunicationV4WechatAcceptEffect(
	t *testing.T,
	s *Store,
	fixture communicationV4EventEffectFixture,
	created *CreateEffectIntentResult,
	resultMsgID string,
	exchangeSourceKey string,
	peerWechat string,
) {
	t.Helper()
	resultAt := fixture.Now.Add(time.Minute)
	if _, err := s.ApplyResultMessage(
		created.Command.MsgID,
		resultMsgID,
		"result",
		fixture.HandID,
		func(command *CmdRecord) (ResultCommandMutation, error) {
			command.Status = CmdOk
			command.TerminalAt = &resultAt
			return ResultCommandMutation{
				Save: true,
				Effect: &EffectResultMutation{
					IntentStatus: EffectIntentOk,
					ContentHash:  fixture.Action.ContentHash,
					WechatContact: &WechatContactResultMutation{
						ConversationRef:   fixture.ConversationRef,
						RequestSourceKey:  fixture.RequestSourceKey,
						ExchangeSourceKey: exchangeSourceKey,
						PeerWechat:        peerWechat,
						ObservedAtMs:      resultAt.UnixMilli(),
					},
				},
			}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
}

func assertCommunicationV4WechatAcceptSettled(
	t *testing.T,
	s *Store,
	fixture communicationV4EventEffectFixture,
	created *CreateEffectIntentResult,
	exchangeSourceKey string,
	peerWechat string,
	messageCount int,
) {
	t.Helper()
	intent, err := s.EffectIntentByID(created.Intent.IntentID)
	if err != nil || intent == nil ||
		intent.Status != EffectIntentOk ||
		intent.ResultMessageSeq != nil {
		t.Fatalf("接受微信意图未以零消息正证终局: intent=%+v err=%v", intent, err)
	}
	var action CommunicationV4EventAction
	if err := s.db.First(
		&action,
		"action_id = ?",
		fixture.Action.ActionID,
	).Error; err != nil {
		t.Fatal(err)
	}
	if action.Status != CommunicationV4EventActionSent ||
		action.EffectIntentID == nil ||
		*action.EffectIntentID != created.Intent.IntentID ||
		action.SentAt == nil {
		t.Fatalf("接受微信动作未收束 sent: %+v", action)
	}
	assets, err := s.ContactAssetsByProfile(fixture.ProfileID)
	if err != nil || len(assets) != 1 {
		t.Fatalf("联系方式账本不唯一: assets=%+v err=%v", assets, err)
	}
	asset := assets[0]
	if asset.RequestSourceKey != fixture.RequestSourceKey ||
		asset.SourceKey != exchangeSourceKey ||
		asset.Value != peerWechat ||
		asset.EffectIntentID == nil ||
		*asset.EffectIntentID != created.Intent.IntentID {
		t.Fatalf("联系方式业务事实错误: %+v", asset)
	}
	messages, err := s.MessagesForConversation(ConversationKey{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		ConversationRef: fixture.ConversationRef,
	})
	if err != nil || len(messages) != messageCount {
		t.Fatalf("接受微信不得伪造 outbound Message: count=%d messages=%+v err=%v",
			messageCount, messages, err)
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil ||
		aggregate.State.WechatState != communication.V4WechatExchanged ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive ||
		aggregate.ManualReason != "" {
		t.Fatalf("接受正证未推进微信状态并保留自动承接资格: aggregate=%+v err=%v",
			aggregate, err)
	}
	turn, err := s.DialogueTurnByID(fixture.Turn.TurnID)
	if err != nil || turn == nil ||
		turn.Status != DialogueTurnClassified ||
		turn.IntentLabel != m5ai.IntentInterested ||
		turn.IntentSource != DialogueIntentBusinessEvent ||
		turn.ClassifiedAt == nil {
		t.Fatalf("接受正证未把原轮推进到一次回复建议: turn=%+v err=%v", turn, err)
	}
	var initial CommunicationV4ProjectionApplication
	if err := s.db.First(
		&initial,
		"profile_id = ? AND input_kind = ? AND input_key = ?",
		fixture.ProfileID,
		CommunicationV4InputDialogueTurn,
		fixture.Turn.TurnID,
	).Error; err != nil ||
		initial.Outcome.Dialogue != communication.V4DialogueWechatContinuation ||
		!initial.Outcome.DialogueAfterActions ||
		initial.Outcome.DialogueStatus != communication.V4DialogueWaitingPrerequisite {
		t.Fatalf("主动换微信轮没有冻结动作后承接语义: initial=%+v err=%v", initial, err)
	}
	var confirmed CommunicationV4ProjectionApplication
	if err := s.db.First(
		&confirmed,
		"profile_id = ? AND input_kind = ? AND input_key = ?",
		fixture.ProfileID,
		CommunicationV4InputConfirmedAction,
		fixture.Action.SemanticActionKey,
	).Error; err != nil ||
		confirmed.Outcome.Dialogue != communication.V4DialogueWechatContinuation ||
		confirmed.Outcome.DialogueStatus != communication.V4DialogueWaitingAdvice ||
		confirmed.Outcome.NextAdvice != communication.V4AdviceReply ||
		confirmed.Outcome.IntentLabel != m5ai.IntentInterested ||
		confirmed.Outcome.IntentSource != communication.IntentSourceBusinessEvent {
		t.Fatalf("接受正证没有形成不可变回复授权: confirmed=%+v err=%v", confirmed, err)
	}
}

func TestCommunicationV4WechatAcceptDirectResultIsAtomicAndPrivate(t *testing.T) {
	s := openTest(t)
	fixture := seedCommunicationV4WechatAcceptEffect(t, s, "direct")
	req := communicationV4EventEffectRequest(
		t,
		s,
		fixture,
		fixture.Action,
		"accept-direct",
	)
	fingerprint, err := AcceptWechatFingerprint(fixture.RequestSourceKey)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.Action.ContentHash != fingerprint ||
		req.Intent.SendFingerprint != fingerprint ||
		req.Intent.SendFingerprint == fixture.RequestSourceKey ||
		strings.Contains(req.Intent.PayloadHash, fixture.RequestSourceKey) ||
		strings.Contains(req.Intent.GuardsHash, fixture.RequestSourceKey) {
		t.Fatalf("私有请求身份泄漏进动作/WAL 摘要: action=%+v intent=%+v",
			fixture.Action, req.Intent)
	}
	created, err := s.CreateEffectIntentAndCmd(req)
	if err != nil || !created.Created {
		t.Fatalf("接受微信 WAL 构造失败: result=%+v err=%v", created, err)
	}
	var args protocol.ChatAcceptWechatArgs
	if err := json.Unmarshal([]byte(created.Command.Args), &args); err != nil ||
		args.RequestSourceKey != fixture.RequestSourceKey {
		t.Fatalf("命令未保留原始请求身份: args=%+v err=%v", args, err)
	}
	messagesBefore, err := s.MessagesForConversation(ConversationKey{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		ConversationRef: fixture.ConversationRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	exchangeSourceKey := strings.Repeat("6", 64)
	settleCommunicationV4WechatAcceptEffect(
		t,
		s,
		fixture,
		created,
		"result-v4-accept-direct",
		exchangeSourceKey,
		"synthetic-wechat-direct",
	)
	assertCommunicationV4WechatAcceptSettled(
		t,
		s,
		fixture,
		created,
		exchangeSourceKey,
		"synthetic-wechat-direct",
		len(messagesBefore),
	)
}

func TestCommunicationV4WechatAcceptAuthorizesOneReplyAndFailureKeepsContact(t *testing.T) {
	s := openTest(t)
	fixture := seedCommunicationV4WechatAcceptEffect(t, s, "continuation")
	req := communicationV4EventEffectRequest(
		t,
		s,
		fixture,
		fixture.Action,
		"accept-continuation",
	)
	created, err := s.CreateEffectIntentAndCmd(req)
	if err != nil || !created.Created {
		t.Fatalf("接受微信 WAL 构造失败: result=%+v err=%v", created, err)
	}
	settleCommunicationV4WechatAcceptEffect(
		t,
		s,
		fixture,
		created,
		"result-v4-accept-continuation",
		strings.Repeat("8", 64),
		"synthetic-wechat-continuation",
	)

	reservation := ReserveAIInvocationRequest{
		InvocationID: "invocation-v4-wechat-continuation",
		TurnID:       fixture.Turn.TurnID,
		Purpose:      m5ai.PurposeReply,
		Attempt:      1,
		Provider:     "deepseek",
		Model:        "deepseek-v4-pro",
		InputHash:    "input-v4-wechat-continuation",
		CreatedAt:    fixture.Now.Add(2 * time.Minute),
	}
	reserved, err := s.ReserveAIInvocation(reservation)
	if err != nil || reserved == nil || !reserved.Created {
		t.Fatalf("接受正证后唯一回复调用未获授权: result=%+v err=%v", reserved, err)
	}
	replayed, err := s.ReserveAIInvocation(reservation)
	if err != nil || replayed == nil || replayed.Created ||
		replayed.Invocation.InvocationID != reservation.InvocationID {
		t.Fatalf("回复调用重放发生增生: result=%+v err=%v", replayed, err)
	}
	var invocationCount int64
	if err := s.db.Model(&AIInvocation{}).
		Where("turn_id = ? AND purpose = ?", fixture.Turn.TurnID, m5ai.PurposeReply).
		Count(&invocationCount).Error; err != nil || invocationCount != 1 {
		t.Fatalf("同轮回复调用不唯一: count=%d err=%v", invocationCount, err)
	}

	finishedAt := reservation.CreatedAt.Add(time.Second)
	action, err := s.CompleteReplyInvocation(CompleteReplyInvocationRequest{
		Completion: AIInvocationCompletion{
			InvocationID: reservation.InvocationID,
			Status:       AIInvocationInvalidOutput,
			OutputHash:   "invalid-v4-wechat-continuation",
			LatencyMs:    20,
			ErrorClass:   "invalidJSON",
			FinishedAt:   finishedAt,
		},
	})
	if err != nil || action != nil {
		t.Fatalf("承接 AI 失败应只转人工且不创建正文: action=%+v err=%v", action, err)
	}
	aggregate, aggregateErr := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	assets, assetsErr := s.ContactAssetsByProfile(fixture.ProfileID)
	if aggregateErr != nil ||
		aggregate.State.WechatState != communication.V4WechatExchanged ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationManualRequired ||
		assetsErr != nil ||
		len(assets) != 1 ||
		assets[0].EffectIntentID == nil ||
		*assets[0].EffectIntentID != created.Intent.IntentID {
		t.Fatalf("承接 AI 失败回滚了接受/联系方式事实: aggregate=%+v assets=%+v errs=(%v,%v)",
			aggregate, assets, aggregateErr, assetsErr)
	}
}

func TestCommunicationV4WechatAcceptVerificationAndRestartAreIdempotent(t *testing.T) {
	dataDir := t.TempDir()
	s, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedCommunicationV4WechatAcceptEffect(t, s, "verified")
	req := communicationV4EventEffectRequest(
		t,
		s,
		fixture,
		fixture.Action,
		"accept-verified",
	)
	created, err := s.CreateEffectIntentAndCmd(req)
	if err != nil || !created.Created {
		t.Fatalf("接受微信 WAL 构造失败: result=%+v err=%v", created, err)
	}
	verifyingAt := fixture.Now.Add(time.Minute)
	if _, err := s.ApplyResultMessage(
		created.Command.MsgID,
		"result-v4-accept-possible",
		"result",
		fixture.HandID,
		func(command *CmdRecord) (ResultCommandMutation, error) {
			command.Status = CmdVerifying
			command.TerminalAt = nil
			command.VerificationReason = "result.sideEffect=possible"
			command.VerificationNextAt = &verifyingAt
			return ResultCommandMutation{
				Save:            true,
				KeepCommandOpen: true,
				Effect: &EffectResultMutation{
					IntentStatus: EffectIntentVerifying,
					ContentHash:  fixture.Action.ContentHash,
					Reason:       "result.sideEffect=possible",
				},
			}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	turnBeforeVerification, err := s.LatestDialogueTurnForProfile(fixture.ProfileID)
	if err != nil ||
		turnBeforeVerification == nil ||
		turnBeforeVerification.Status != DialogueTurnCollected {
		t.Fatalf("接受结果未确认时不应推进承接轮: turn=%+v err=%v", turnBeforeVerification, err)
	}
	invocationsBeforeVerification, err := s.AIInvocationsForTurn(fixture.Turn.TurnID)
	if err != nil || len(invocationsBeforeVerification) != 0 {
		t.Fatalf("接受结果未确认时不得构造承接 AI: invocations=%+v err=%v",
			invocationsBeforeVerification, err)
	}
	exchangeSourceKey := strings.Repeat("7", 64)
	resolveReq := VerifiedWechatAcceptSuccess{
		Ref: created.Command.MsgID,
		ConversationKey: ConversationKey{
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			ConversationRef: fixture.ConversationRef,
		},
		RequestSourceKey:  fixture.RequestSourceKey,
		ExchangeSourceKey: exchangeSourceKey,
		PeerWechat:        "synthetic-wechat-verified",
		ObservedAtMs:      verifyingAt.UnixMilli(),
		ResultBody:        `{"status":"ok"}`,
		ResolutionReason:  "verifiedAcceptWechat",
		At:                verifyingAt.Add(time.Second),
	}
	if _, err := s.ResolveWechatAcceptVerified(resolveReq); err != nil {
		t.Fatal(err)
	}
	messages, err := s.MessagesForConversation(resolveReq.ConversationKey)
	if err != nil {
		t.Fatal(err)
	}
	assertCommunicationV4WechatAcceptSettled(
		t,
		s,
		fixture,
		created,
		exchangeSourceKey,
		"synthetic-wechat-verified",
		len(messages),
	)
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil {
		t.Fatal(err)
	}
	revision := aggregate.Revision
	if _, err := s.ResolveWechatAcceptVerified(resolveReq); err != nil {
		t.Fatalf("重复验证未幂等: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if _, err := restarted.ResolveWechatAcceptVerified(resolveReq); err != nil {
		t.Fatalf("重启后验证重放未幂等: %v", err)
	}
	replayed, err := restarted.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || replayed.Revision != revision {
		t.Fatalf("重复/重启验证增生 V4 投影: aggregate=%+v err=%v", replayed, err)
	}
	assets, err := restarted.ContactAssetsByProfile(fixture.ProfileID)
	if err != nil || len(assets) != 1 {
		t.Fatalf("重复/重启验证增生联系方式: assets=%+v err=%v", assets, err)
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
