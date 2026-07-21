package store

import (
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/contract/gen/go/protocol"
)

type m5AutomaticEffectFixture struct {
	dialogueStoreFixture
	Turn   DialogueTurn
	Action CommunicationAction
	Now    time.Time
}

func seedPlannedM5AutomaticAction(t *testing.T, s *Store, suffix string) m5AutomaticEffectFixture {
	t.Helper()
	fixture, turn := seedFrozenDialogueTurn(t, s, "profile-auto-effect-"+suffix)
	now := time.Now().UTC().Truncate(time.Millisecond)
	classified, err := s.ApplyCodeClassification(CodeClassificationRequest{
		TurnID: turn.TurnID, Label: m5ai.IntentInterested, ClassifiedAt: now,
	})
	if err != nil || classified.Status != DialogueTurnClassified {
		t.Fatalf("代码分类未推进到 classified: turn=%+v err=%v", classified, err)
	}
	replyID := "invocation-auto-effect-" + suffix
	if result, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
		InvocationID: replyID, TurnID: turn.TurnID, Purpose: m5ai.PurposeReply,
		Attempt: 1, Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input-auto-effect-" + suffix,
	}); err != nil || !result.Created {
		t.Fatalf("reply invocation 预留失败: result=%+v err=%v", result, err)
	}
	action, err := s.CompleteReplyInvocation(CompleteReplyInvocationRequest{
		Completion:  successfulInvocationCompletion(replyID, now.Add(time.Second)),
		ActionID:    "action-auto-effect-" + suffix,
		Text:        "合成自动回复-" + suffix,
		ContentHash: "content-auto-effect-" + suffix,
		PlannedAt:   now.Add(time.Second),
	})
	if err != nil || action == nil || action.Status != CommunicationActionPlanned || action.EffectIntentID != nil {
		t.Fatalf("planned action 构造失败: action=%+v err=%v", action, err)
	}
	return m5AutomaticEffectFixture{
		dialogueStoreFixture: fixture,
		Turn:                 turn,
		Action:               *action,
		Now:                  now.Add(2 * time.Second),
	}
}

func automaticEffectIntentRequest(t *testing.T, fixture m5AutomaticEffectFixture, suffix string) CreateEffectIntentRequest {
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
	msgID := "msg-auto-effect-" + suffix
	idemKey := "idem-auto-effect-" + suffix
	deadline := fixture.Now.Add(time.Hour).UnixMilli()
	return CreateEffectIntentRequest{
		Intent: EffectIntent{
			IntentID: intentID, IdemKey: idemKey,
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			Primitive: primitiveChatSendMessage, TargetRef: fixture.ConversationRef,
			PayloadHash: "payload-auto-effect-" + suffix,
			GuardsHash:  "guards-auto-effect-" + suffix,
			Status:      EffectIntentDispatching, DeadlineMs: deadline,
			SendFingerprint: fixture.Action.ContentHash,
		},
		Command: CmdRecord{
			MsgID: msgID, Name: primitiveChatSendMessage, Class: "effectful", IdemKey: idemKey,
			Domain:   fixture.Platform + ":" + fixture.AccountRef,
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			ExpectedPrincipalFingerprint: fixture.Principal,
			IntentID:                     intentID, HandID: fixture.HandID, Session: fixture.Session,
			BootIDAtDispatch: fixture.BootID, Args: string(args),
			Status: CmdQueued, DeadlineMs: deadline, ExecBudgetMs: 60_000,
		},
		ExpectedTailSeq: 2, AutomaticActionID: fixture.Action.ActionID, Now: fixture.Now,
	}
}

func createM5AutomaticEffect(t *testing.T, s *Store, suffix string) (m5AutomaticEffectFixture, CreateEffectIntentRequest, *CreateEffectIntentResult) {
	t.Helper()
	fixture := seedPlannedM5AutomaticAction(t, s, suffix)
	req := automaticEffectIntentRequest(t, fixture, suffix)
	created, err := s.CreateEffectIntentAndCmd(req)
	if err != nil || !created.Created {
		t.Fatalf("自动回复 intent/WAL 构造失败: result=%+v err=%v", created, err)
	}
	return fixture, req, created
}

func assertM5AutomaticSent(t *testing.T, s *Store, fixture m5AutomaticEffectFixture, intentID string) {
	t.Helper()
	action, err := s.CommunicationActionByTurn(fixture.Turn.TurnID)
	if err != nil || action == nil || action.Status != CommunicationActionSent || action.SentAt == nil ||
		action.EffectIntentID == nil || *action.EffectIntentID != intentID || action.FailureReason != "" {
		t.Fatalf("自动动作未收束 sent: action=%+v err=%v", action, err)
	}
	turn, err := s.DialogueTurnByID(fixture.Turn.TurnID)
	if err != nil || turn == nil || turn.Status != DialogueTurnCompleted || turn.FailureReason != "" {
		t.Fatalf("沟通轮未收束 completed: turn=%+v err=%v", turn, err)
	}
	status, err := s.M5TrialStatus()
	if err != nil || status == nil || status.Selection.Status != M5TrialSelectionCompleted ||
		status.Selection.ActiveSlot != nil || status.Selection.EndedAt == nil {
		t.Fatalf("试运行未收束 completed 并释放 active slot: status=%+v err=%v", status, err)
	}
}

func TestM5AutomaticActionAndEffectIntentAreConstructedAtomically(t *testing.T) {
	s := openTest(t)
	fixture, req, created := createM5AutomaticEffect(t, s, "construct")

	action, err := s.CommunicationActionByTurn(fixture.Turn.TurnID)
	if err != nil || action == nil || action.Status != CommunicationActionEffectPending ||
		action.EffectIntentID == nil || *action.EffectIntentID != req.Intent.IntentID || action.EffectStartedAt == nil {
		t.Fatalf("planned action 未与 WAL 同事务推进 effectPending: action=%+v err=%v", action, err)
	}
	turn, err := s.DialogueTurnByID(fixture.Turn.TurnID)
	if err != nil || turn == nil || turn.Status != DialogueTurnDispatching {
		t.Fatalf("turn 未与 WAL 同事务推进 dispatching: turn=%+v err=%v", turn, err)
	}
	intent, err := s.EffectIntentByID(req.Intent.IntentID)
	if err != nil || intent == nil || intent.Status != EffectIntentDispatching || intent.RootMsgID != created.Command.MsgID {
		t.Fatalf("effect intent 未正确落账: intent=%+v err=%v", intent, err)
	}
	cmd, err := s.CmdByMsgID(created.Command.MsgID)
	if err != nil || cmd == nil || cmd.Status != CmdQueued || cmd.LogicalDispatchID != created.Command.MsgID {
		t.Fatalf("queued WAL 未正确落账: cmd=%+v err=%v", cmd, err)
	}
	if err := s.ValidateM5AutomaticIntentLink(fixture.Action.ActionID, req.Intent.IntentID); err != nil {
		t.Fatalf("持久 action→intent 绑定无效: %v", err)
	}
	replayed, err := s.CreateEffectIntentAndCmd(req)
	if err != nil || replayed.Created || replayed.Intent.IntentID != req.Intent.IntentID ||
		replayed.Command.MsgID != created.Command.MsgID {
		t.Fatalf("同 action 重放必须复用同一 intent/WAL: result=%+v err=%v", replayed, err)
	}
	var intents, commands, heads int64
	_ = s.db.Model(&EffectIntent{}).Where("intent_id = ?", req.Intent.IntentID).Count(&intents).Error
	_ = s.db.Model(&CmdRecord{}).Where("intent_id = ?", req.Intent.IntentID).Count(&commands).Error
	_ = s.db.Model(&ConversationEffectHead{}).Where(
		"platform = ? AND account_ref = ? AND conversation_ref = ?",
		fixture.Platform, fixture.AccountRef, fixture.ConversationRef,
	).Count(&heads).Error
	if intents != 1 || commands != 1 || heads != 1 {
		t.Fatalf("重放后不得增生账本行: intents=%d commands=%d heads=%d", intents, commands, heads)
	}
}

func TestM5AutomaticOKResultAtomicallyAppendsOneSelfMessageAndCompletesTrial(t *testing.T) {
	s := openTest(t)
	fixture, req, created := createM5AutomaticEffect(t, s, "ok")
	resultAt := fixture.Now.Add(time.Minute)
	resultID := "result-auto-effect-ok"
	result, err := s.ApplyResultMessage(created.Command.MsgID, resultID, "result", fixture.HandID,
		func(cmd *CmdRecord) (ResultCommandMutation, error) {
			cmd.Status = CmdOk
			cmd.TerminalAt = &resultAt
			return ResultCommandMutation{Save: true, Effect: &EffectResultMutation{
				IntentStatus: EffectIntentOk, Append: true,
				Text: fixture.Action.Text, ContentHash: fixture.Action.ContentHash,
				ObservedAtMs: resultAt.UnixMilli(),
			}}, nil
		})
	if err != nil || !result.CommandFound || result.AlreadyProcessed {
		t.Fatalf("ok result 入账失败: result=%+v err=%v", result, err)
	}
	assertM5AutomaticSent(t, s, fixture, req.Intent.IntentID)

	replayed, err := s.ApplyResultMessage(created.Command.MsgID, resultID, "result", fixture.HandID,
		func(*CmdRecord) (ResultCommandMutation, error) {
			t.Fatal("重复 result 不得再次执行 mutation")
			return ResultCommandMutation{}, nil
		})
	if err != nil || !replayed.AlreadyProcessed {
		t.Fatalf("重复 result 未被持久去重: result=%+v err=%v", replayed, err)
	}
	var messages int64
	if err := s.db.Model(&Message{}).Where(
		"outbound_intent_id = ? AND direction = ? AND origin = ? AND retracted_at IS NULL",
		req.Intent.IntentID, "out", "self",
	).Count(&messages).Error; err != nil || messages != 1 {
		t.Fatalf("ok result 必须只追加一条活动 self 消息: count=%d err=%v", messages, err)
	}
	intent, _ := s.EffectIntentByID(req.Intent.IntentID)
	if intent == nil || intent.Status != EffectIntentOk || intent.ResultMessageSeq == nil {
		t.Fatalf("权威 intent 未与消息/action 同事务收束: %+v", intent)
	}
}

func TestM5AutomaticFailedNoneResultAtomicallyRequiresManualHandling(t *testing.T) {
	s := openTest(t)
	fixture, req, created := createM5AutomaticEffect(t, s, "failed-none")
	resultAt := fixture.Now.Add(time.Minute)
	result, err := s.ApplyResultMessage(
		created.Command.MsgID, "result-auto-effect-failed-none", "result", fixture.HandID,
		func(cmd *CmdRecord) (ResultCommandMutation, error) {
			cmd.Status = CmdFailed
			cmd.SideEffect = "none"
			cmd.ErrorCode = "TEST_FAILED_NONE"
			cmd.TerminalAt = &resultAt
			return ResultCommandMutation{Save: true, Effect: &EffectResultMutation{
				IntentStatus: EffectIntentFailed, Reason: "failedNone",
			}}, nil
		},
	)
	if err != nil || !result.CommandFound || result.AlreadyProcessed {
		t.Fatalf("failed+none result 入账失败: result=%+v err=%v", result, err)
	}
	action, err := s.CommunicationActionByTurn(fixture.Turn.TurnID)
	if err != nil || action == nil || action.Status != CommunicationActionManualRequired ||
		action.FailureReason != "effectFailed" || action.SentAt != nil {
		t.Fatalf("failed+none 未收束 action 人工处理: action=%+v err=%v", action, err)
	}
	turn, err := s.DialogueTurnByID(fixture.Turn.TurnID)
	if err != nil || turn == nil || turn.Status != DialogueTurnManualRequired || turn.FailureReason != "effectFailed" {
		t.Fatalf("failed+none 未收束 turn 人工处理: turn=%+v err=%v", turn, err)
	}
	assertTrialManualRequired(t, s, "effectFailed")
	var messages int64
	if err := s.db.Model(&Message{}).Where("outbound_intent_id = ?", req.Intent.IntentID).
		Count(&messages).Error; err != nil || messages != 0 {
		t.Fatalf("failed+none 不得伪造 self 消息: count=%d err=%v", messages, err)
	}
	intent, _ := s.EffectIntentByID(req.Intent.IntentID)
	if intent == nil || intent.Status != EffectIntentFailed || intent.ResultMessageSeq != nil {
		t.Fatalf("failed+none 权威 intent 状态错误: %+v", intent)
	}
}

func TestM5AutomaticConfirmedSideEffectSettlesAsSent(t *testing.T) {
	s := openTest(t)
	fixture, req, created := createM5AutomaticEffect(t, s, "confirmed")
	resultAt := fixture.Now.Add(time.Minute)
	result, err := s.ApplyResultMessage(
		created.Command.MsgID, "result-auto-effect-confirmed", "result", fixture.HandID,
		func(cmd *CmdRecord) (ResultCommandMutation, error) {
			cmd.Status = CmdFailed
			cmd.SideEffect = "confirmed"
			cmd.ErrorCode = "TEST_FAILED_CONFIRMED"
			cmd.SuspectReason = "result 失败但已确认发生副作用"
			cmd.TerminalAt = &resultAt
			return ResultCommandMutation{Save: true, Effect: &EffectResultMutation{
				IntentStatus: EffectIntentOk, Append: true,
				Text: fixture.Action.Text, ContentHash: fixture.Action.ContentHash,
				ObservedAtMs: resultAt.UnixMilli(), Reason: cmd.SuspectReason,
			}}, nil
		},
	)
	if err != nil || !result.CommandFound || result.AlreadyProcessed {
		t.Fatalf("confirmed result 入账失败: result=%+v err=%v", result, err)
	}
	assertM5AutomaticSent(t, s, fixture, req.Intent.IntentID)
	cmd, _ := s.CmdByMsgID(created.Command.MsgID)
	if cmd == nil || cmd.Status != CmdFailed || cmd.SideEffect != "confirmed" {
		t.Fatalf("confirmed 物理失败证词不得被业务成功覆盖: %+v", cmd)
	}
	intent, _ := s.EffectIntentByID(req.Intent.IntentID)
	if intent == nil || intent.Status != EffectIntentOk || intent.ResultMessageSeq == nil {
		t.Fatalf("confirmed 必须把权威 intent 收束成功: %+v", intent)
	}
}

func TestM5AutomaticConstructionBoundaryChangeRollsBackEverything(t *testing.T) {
	s := openTest(t)
	fixture := seedPlannedM5AutomaticAction(t, s, "boundary-rollback")
	lateText := "在构造 effect 前到达的合成新消息"
	if err := s.db.Create(&Message{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef, ConversationRef: fixture.ConversationRef,
		Seq: 3, Direction: "in", Kind: "text", ContentHash: "late-boundary-hash",
		Text: &lateText, Origin: "external", CreatedAt: fixture.Now, UpdatedAt: fixture.Now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	req := automaticEffectIntentRequest(t, fixture, "boundary-rollback")
	if _, err := s.CreateEffectIntentAndCmd(req); !errors.Is(err, ErrDialogueTurnBinding) {
		t.Fatalf("消息边界变化必须阻断 effect 构造: %v", err)
	}

	action, err := s.CommunicationActionByTurn(fixture.Turn.TurnID)
	if err != nil || action == nil || action.Status != CommunicationActionPlanned ||
		action.EffectIntentID != nil || action.EffectStartedAt != nil {
		t.Fatalf("失败事务不得改动 planned action: action=%+v err=%v", action, err)
	}
	turn, err := s.DialogueTurnByID(fixture.Turn.TurnID)
	if err != nil || turn == nil || turn.Status != DialogueTurnAdviceReady {
		t.Fatalf("失败事务不得推进 turn: turn=%+v err=%v", turn, err)
	}
	intent, err := s.EffectIntentByID(req.Intent.IntentID)
	if err != nil || intent != nil {
		t.Fatalf("失败事务不得留下 effect intent: intent=%+v err=%v", intent, err)
	}
	cmd, err := s.CmdByMsgID(req.Command.MsgID)
	if err != nil || cmd != nil {
		t.Fatalf("失败事务不得留下 queued WAL: cmd=%+v err=%v", cmd, err)
	}
	var heads int64
	if err := s.db.Model(&ConversationEffectHead{}).Where(
		"platform = ? AND account_ref = ? AND conversation_ref = ?",
		fixture.Platform, fixture.AccountRef, fixture.ConversationRef,
	).Count(&heads).Error; err != nil || heads != 0 {
		t.Fatalf("失败事务不得推进会话 effect head: count=%d err=%v", heads, err)
	}
	status, err := s.M5TrialStatus()
	if err != nil || status == nil || status.Selection.Status != M5TrialSelectionActive ||
		status.Selection.ActiveSlot == nil || status.Selection.EndedAt != nil {
		t.Fatalf("构造回滚不得擅自终结试运行授权: status=%+v err=%v", status, err)
	}
}
