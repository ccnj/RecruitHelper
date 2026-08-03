package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/textcanon"
	"recruithelper/contract/gen/go/protocol"
)

// 本文件覆盖 Q7(2026-08-03 甲方裁决):三道派发闸对无主 system 家具行/已
// 撤回行的读侧容忍。真机事故形状:平台往会话里插"可以直接给Ta打电话"等
// 系统提示,无业务认领,游标追不平账本尾,催1 构造失败、候选人冻结。

// seedColdPlanWithSuffixRow 冻结冷催计划后往账本尾追加一行消息,返回首个
// 计划动作的 WAL 构造请求。追加发生在计划冻结之后,复现"计划已在、家具行
// 后到"的真机事故顺序。
func seedColdPlanWithSuffixRow(
	t *testing.T,
	s *Store,
	suffix string,
	row Message,
) (resumeStoreFixture, CreateEffectIntentRequest) {
	t.Helper()
	fixture, _, result := freezeCommunicationV4ColdWechatPlan(t, s, suffix)
	appendCommunicationV4Inbound(t, s, fixture, row)
	first := result.Actions[0]
	effectFixture := communicationV4EventEffectFixture{
		resumeStoreFixture: fixture,
		Action:             first,
		Now:                result.Plan.CreatedAt.Add(time.Second),
	}
	req := communicationV4EventEffectRequest(t, s, effectFixture, first, suffix)
	return fixture, req
}

func TestDispatchChainHeadToleratesSystemFurnitureRow(t *testing.T) {
	s := openTest(t)
	furniture := "如果想要让人才更快回复，可以直接给Ta打电话"
	fixture, req := seedColdPlanWithSuffixRow(t, s, "q7-head-furniture", Message{
		Seq: 2, Direction: "system", Kind: "system",
		ContentHash: textcanon.Hash(furniture), Text: &furniture,
	})
	// 红线:传给手的 expectedTail 仍是真实账本尾(含家具行),不是游标。
	if req.ExpectedTailSeq != 2 {
		t.Fatalf("expectedTail 必须是真实账本尾 2: got=%d", req.ExpectedTailSeq)
	}
	created, err := s.CreateEffectIntentAndCmd(req)
	if err != nil || !created.Created {
		t.Fatalf("链首闸未越过 system 家具行,催意图构造失败: result=%+v err=%v",
			created, err)
	}
	var action CommunicationV4EventAction
	if err := s.db.First(
		&action,
		"action_id = ?",
		req.AutomaticActionID,
	).Error; err != nil {
		t.Fatal(err)
	}
	if action.Status != CommunicationV4EventActionEffectPending ||
		action.EffectIntentID == nil ||
		*action.EffectIntentID != req.Intent.IntentID {
		t.Fatalf("催动作未与 WAL 原子绑定: action=%+v", action)
	}
	// 游标数值零改写:放行不等于把家具行记为已投影。
	aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
	if err != nil || aggregate.ProjectedThroughSeq != 1 {
		t.Fatalf("游标不得被改写: aggregate=%+v err=%v", aggregate, err)
	}
}

func TestDispatchChainHeadToleratesUnknownSystemPlaceholder(t *testing.T) {
	s := openTest(t)
	placeholder := "[系统消息:99]"
	_, req := seedColdPlanWithSuffixRow(t, s, "q7-head-placeholder", Message{
		Seq: 2, Direction: "system", Kind: "system",
		ContentHash: textcanon.Hash(placeholder), Text: &placeholder,
	})
	// 判据是形状(direction/kind)不是文本:未知占位同样放行。
	created, err := s.CreateEffectIntentAndCmd(req)
	if err != nil || !created.Created {
		t.Fatalf("未知占位 system 行未放行: result=%+v err=%v", created, err)
	}
}

func TestDispatchChainHeadStillRejectsCandidateInboundRow(t *testing.T) {
	s := openTest(t)
	inbound := "我考虑一下"
	_, req := seedColdPlanWithSuffixRow(t, s, "q7-head-inbound", Message{
		Seq: 2, Direction: "in", Kind: "text",
		ContentHash: textcanon.Hash(inbound), Text: &inbound,
	})
	if _, err := s.CreateEffectIntentAndCmd(req); !errors.Is(err, ErrDialogueTurnBinding) {
		t.Fatalf("候选人 in 行必须照旧拒绝: err=%v", err)
	}
}

func TestDispatchChainHeadStillRejectsHumanOutboundRow(t *testing.T) {
	s := openTest(t)
	human := "真人在平台上手打的消息"
	_, req := seedColdPlanWithSuffixRow(t, s, "q7-head-human-out", Message{
		Seq: 2, Direction: "out", Kind: "text",
		ContentHash: textcanon.Hash(human), Text: &human,
	})
	if _, err := s.CreateEffectIntentAndCmd(req); !errors.Is(err, ErrDialogueTurnBinding) {
		t.Fatalf("真人 out 行必须照旧拒绝: err=%v", err)
	}
}

func TestDispatchChainHeadTreatsRetractedRowTransparent(t *testing.T) {
	s := openTest(t)
	fixture, _, result := freezeCommunicationV4ColdWechatPlan(t, s, "q7-head-retracted")
	retractedText := "误判后已撤回的行"
	furniture := "如果想要让人才更快回复，可以直接给Ta打电话"
	retractedAt := time.Now().UTC().Truncate(time.Millisecond)
	appendCommunicationV4Inbound(t, s, fixture,
		Message{
			Seq: 2, Direction: "in", Kind: "text",
			ContentHash: textcanon.Hash(retractedText), Text: &retractedText,
			RetractedAt: &retractedAt, RetractionReason: "测试构造",
		},
		Message{
			Seq: 3, Direction: "system", Kind: "system",
			ContentHash: textcanon.Hash(furniture), Text: &furniture,
		},
	)
	first := result.Actions[0]
	effectFixture := communicationV4EventEffectFixture{
		resumeStoreFixture: fixture,
		Action:             first,
		Now:                result.Plan.CreatedAt.Add(time.Second),
	}
	req := communicationV4EventEffectRequest(t, s, effectFixture, first, "q7-head-retracted")
	created, err := s.CreateEffectIntentAndCmd(req)
	if err != nil || !created.Created {
		t.Fatalf("已撤回行必须视同透明: result=%+v err=%v", created, err)
	}
}

// seedColdChainParentSettled 冻结单气泡冷催计划,绑定并正证首个正文动作,
// 使换微信邀请子项物化。返回子项 bind 所需的现场。
func seedColdChainParentSettled(
	t *testing.T,
	s *Store,
	suffix string,
) (resumeStoreFixture, CommunicationV4EventAction, time.Time) {
	t.Helper()
	fixture, _, result := freezeCommunicationV4ColdWechatPlan(t, s, suffix)
	first := result.Actions[0]
	effectFixture := communicationV4EventEffectFixture{
		resumeStoreFixture: fixture,
		Action:             first,
		Now:                result.Plan.CreatedAt.Add(time.Second),
	}
	parentReq := communicationV4EventEffectRequest(t, s, effectFixture, first, suffix+"-text")
	parentCreated, err := s.CreateEffectIntentAndCmd(parentReq)
	if err != nil || !parentCreated.Created {
		t.Fatalf("父正文 WAL 构造失败: result=%+v err=%v", parentCreated, err)
	}
	settleCommunicationV4EventTextEffect(t, s, effectFixture, first, parentCreated, suffix+"-text")
	childID, err := CommunicationV4EventActionID(
		fixture.ProfileID,
		result.Plan.PlannedActions[1].ActionKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CommunicationV4EventActionByID(childID)
	if err != nil || child == nil || child.Status != CommunicationV4EventActionPlanned {
		t.Fatalf("父正证后子项未物化: child=%+v err=%v", child, err)
	}
	return fixture, *child, effectFixture.Now.Add(2 * time.Minute)
}

func TestDispatchMidChainToleratesSystemFurnitureAfterParentEvidence(t *testing.T) {
	s := openTest(t)
	fixture, child, now := seedColdChainParentSettled(t, s, "q7-mid-furniture")
	// 父正证消息落在 seq 2,家具行插到 seq 3。
	furniture := "对方查看了您的在线简历"
	appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 3, Direction: "system", Kind: "system",
		ContentHash: textcanon.Hash(furniture), Text: &furniture,
	})
	effectFixture := communicationV4EventEffectFixture{
		resumeStoreFixture: fixture,
		Action:             child,
		Now:                now,
	}
	req := communicationV4EventEffectRequest(t, s, effectFixture, child, "q7-mid-furniture-card")
	if req.ExpectedTailSeq != 3 {
		t.Fatalf("expectedTail 必须是真实账本尾 3: got=%d", req.ExpectedTailSeq)
	}
	created, err := s.CreateEffectIntentAndCmd(req)
	if err != nil || !created.Created {
		t.Fatalf("链中闸未越过父正证后的 system 家具行: result=%+v err=%v",
			created, err)
	}
}

func TestDispatchMidChainStillRejectsInboundAfterParentEvidence(t *testing.T) {
	s := openTest(t)
	fixture, child, now := seedColdChainParentSettled(t, s, "q7-mid-inbound")
	inbound := "候选人在父正证后插话"
	appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 3, Direction: "in", Kind: "text",
		ContentHash: textcanon.Hash(inbound), Text: &inbound,
	})
	effectFixture := communicationV4EventEffectFixture{
		resumeStoreFixture: fixture,
		Action:             child,
		Now:                now,
	}
	req := communicationV4EventEffectRequest(t, s, effectFixture, child, "q7-mid-inbound-card")
	if _, err := s.CreateEffectIntentAndCmd(req); !errors.Is(err, ErrDialogueTurnBinding) {
		t.Fatalf("父正证后的候选人插话必须照旧拒绝: err=%v", err)
	}
}

// TestDispatchLegacyTextCardToleratesSystemFurniture 覆盖第 5 点排查出的同族
// 比较:legacy 文→卡组合(validateM5DependentActionCurrentTx)的父对齐闸。
// 流程复刻拒绝正则族组合:正文正证物化卡片后,平台插入 system 家具行,卡片
// WAL 仍须放行;既有测试已覆盖插入 in 行时照旧拒绝。
func TestDispatchLegacyTextCardToleratesSystemFurniture(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-q7-legacy-combo")
	setCommunicationV4FixedPhrasePackage(t, s, "revision-profile-q7-legacy-combo")
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
	if err != nil || len(actions) != 1 || actions[0].Kind != CommunicationActionReplyText {
		t.Fatalf("拒绝组合正文动作未就绪: actions=%+v err=%v", actions, err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	textFixture := communicationV4AutomaticEffectFixture{
		resumeStoreFixture: fixture,
		Turn:               frozen.Turn,
		Action:             actions[0],
		Now:                now,
	}
	textReq := communicationV4AutomaticEffectRequest(t, s, textFixture, "q7-legacy-combo-text")
	if _, err := s.CreateEffectIntentAndCmd(textReq); err != nil {
		t.Fatal(err)
	}
	textResultAt := now.Add(time.Minute)
	if _, err := s.ApplyResultMessage(
		textReq.Command.MsgID,
		"result-q7-legacy-combo-text",
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
		actions[1].Kind != CommunicationActionInviteWechat ||
		actions[1].Status != CommunicationActionPlanned {
		t.Fatalf("正文正证未物化卡片动作: actions=%+v err=%v", actions, err)
	}
	card := actions[1]
	// 正文正证消息落在 seq 3,家具行插到 seq 4。
	furniture := "如果想要让人才更快回复，可以直接给Ta打电话"
	appendCommunicationV4Inbound(t, s, fixture, Message{
		Seq: 4, Direction: "system", Kind: "system",
		ContentHash: textcanon.Hash(furniture), Text: &furniture,
	})
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
			IdemKey:         "idem-q7-legacy-combo-card",
			Platform:        fixture.Platform,
			AccountRef:      fixture.AccountRef,
			Primitive:       primitiveChatSendWechatInvite,
			TargetRef:       fixture.ConversationRef,
			PayloadHash:     "payload-q7-legacy-combo-card",
			GuardsHash:      "guards-q7-legacy-combo-card",
			Status:          EffectIntentDispatching,
			DeadlineMs:      cardDeadline,
			SendFingerprint: card.ContentHash,
		},
		Command: CmdRecord{
			MsgID:                        "msg-q7-legacy-combo-card",
			Name:                         primitiveChatSendWechatInvite,
			Class:                        "effectful",
			IdemKey:                      "idem-q7-legacy-combo-card",
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
		// 红线:expectedTail 仍是含家具行的真实账本尾。
		ExpectedTailSeq:   4,
		PreviousIntentID:  textReq.Intent.IntentID,
		AutomaticActionID: card.ActionID,
		Now:               cardAt,
	}
	cardCreated, err := s.CreateEffectIntentAndCmd(cardReq)
	if err != nil || !cardCreated.Created {
		t.Fatalf("文卡组合父对齐闸未越过 system 家具行: result=%+v err=%v",
			cardCreated, err)
	}
	if !strings.HasPrefix(cardIntentID, "auto:") {
		// 仅为固定 intent 形状回归提示,不影响裁决语义。
		t.Logf("卡片 intent 形状: %s", cardIntentID)
	}
}
