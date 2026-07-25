package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/textcanon"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func seedCommunicationV4MultiBubblePlans(
	t *testing.T,
	s *Store,
	suffix string,
	phrases []string,
	withWechatCard bool,
) communicationV4AutomaticEffectFixture {
	t.Helper()
	fixture := seedPlannedCommunicationV4AutomaticAction(t, s, suffix)
	if len(phrases) < 2 {
		t.Fatal("多气泡 fixture 至少需要两项")
	}
	phrases[0] = fixture.Action.Text
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
	plans := make([]communication.V4PlannedAction, 0, len(phrases)+1)
	for index := range phrases {
		actionKey := fixture.Action.ActionID
		if index > 0 {
			actionKey = fixture.Turn.TurnID + "|replyText|bubble:" + string(rune('1'+index))
		}
		plans = append(plans, communication.V4PlannedAction{
			ActionKey: actionKey,
			Kind:      communication.V4ActionReplyText,
		})
	}
	if withWechatCard {
		plans = append(plans, communication.V4PlannedAction{
			ActionKey: fixture.Turn.TurnID + "|inviteWechat",
			Kind:      communication.V4ActionInviteWechat,
		})
	}
	advice.Outcome.PlannedActions = plans
	if err := s.db.Save(&advice).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.First(&fixture.Turn, "turn_id = ?", fixture.Turn.TurnID).Error; err != nil {
		t.Fatal(err)
	}
	fixture.Turn.ReplyPhrases = append([]string(nil), phrases...)
	if err := s.db.Save(&fixture.Turn).Error; err != nil {
		t.Fatal(err)
	}
	return fixture
}

func confirmCommunicationV4Bubble(
	t *testing.T,
	s *Store,
	fixture communicationV4AutomaticEffectFixture,
	action CommunicationAction,
	expectedTail int64,
	suffix string,
) {
	t.Helper()
	fixture.Action = action
	fixture.Now = fixture.Now.Add(time.Duration(expectedTail) * time.Second)
	req := communicationV4AutomaticEffectRequest(t, s, fixture, suffix)
	req.ExpectedTailSeq = expectedTail
	created, err := s.CreateEffectIntentAndCmd(req)
	if err != nil || !created.Created {
		t.Fatalf("气泡 WAL 构造失败: result=%+v err=%v", created, err)
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

func TestCommunicationV4MultiBubbleMaterializesOneAtATimeThenCard(t *testing.T) {
	s := openTest(t)
	phrases := []string{"fixture replaces first", "第二个气泡", "第三个气泡"}
	fixture := seedCommunicationV4MultiBubblePlans(t, s, "multi-bubble-card", phrases, true)

	for index := range phrases {
		actions, err := s.CommunicationActionsByTurn(fixture.Turn.TurnID)
		if err != nil || len(actions) != index+1 {
			t.Fatalf("第 %d 项发送前只能物化到当前气泡: actions=%+v err=%v", index+1, actions, err)
		}
		current := actions[index]
		if current.Kind != CommunicationActionReplyText ||
			current.Status != CommunicationActionPlanned ||
			current.Text != phrases[index] ||
			current.ContentHash != textcanon.Hash(phrases[index]) {
			t.Fatalf("第 %d 个气泡正文错误: %+v", index+1, current)
		}
		if index == 0 {
			if current.DependsOnActionID != nil {
				t.Fatalf("首气泡不得有父动作: %+v", current)
			}
		} else if current.DependsOnActionID == nil ||
			*current.DependsOnActionID != actions[index-1].ActionID {
			t.Fatalf("第 %d 个气泡未钉死上一项正证: %+v", index+1, current)
		}
		confirmCommunicationV4Bubble(
			t,
			s,
			fixture,
			current,
			fixture.Turn.InboundThroughSeq+int64(index),
			"multi-bubble-"+string(rune('1'+index)),
		)
	}

	actions, err := s.CommunicationActionsByTurn(fixture.Turn.TurnID)
	if err != nil || len(actions) != 4 {
		t.Fatalf("最后气泡正证后应物化卡片: actions=%+v err=%v", actions, err)
	}
	card := actions[3]
	if card.Kind != CommunicationActionInviteWechat ||
		card.Status != CommunicationActionPlanned ||
		card.DependsOnActionID == nil ||
		*card.DependsOnActionID != actions[2].ActionID {
		t.Fatalf("卡片没有严格依赖最后一个气泡: %+v", card)
	}
	for index := 0; index < 3; index++ {
		if actions[index].Status != CommunicationActionSent ||
			actions[index].EffectIntentID == nil {
			t.Fatalf("第 %d 个气泡没有独立 WAL/正证: %+v", index+1, actions[index])
		}
	}
}

func TestCommunicationV4MultiBubbleFailureDoesNotMaterializeLaterItems(t *testing.T) {
	s := openTest(t)
	phrases := []string{"fixture replaces first", "第二个气泡", "永远不应物化"}
	fixture := seedCommunicationV4MultiBubblePlans(t, s, "multi-bubble-failed", phrases, false)
	actions, _ := s.CommunicationActionsByTurn(fixture.Turn.TurnID)
	confirmCommunicationV4Bubble(
		t,
		s,
		fixture,
		actions[0],
		fixture.Turn.InboundThroughSeq,
		"multi-bubble-failed-first",
	)
	actions, _ = s.CommunicationActionsByTurn(fixture.Turn.TurnID)
	if len(actions) != 2 {
		t.Fatalf("首项正证后应只物化第二项: %+v", actions)
	}
	second := actions[1]
	fixture.Action = second
	req := communicationV4AutomaticEffectRequest(t, s, fixture, "multi-bubble-failed-second")
	req.ExpectedTailSeq = fixture.Turn.InboundThroughSeq + 1
	created, err := s.CreateEffectIntentAndCmd(req)
	if err != nil || !created.Created {
		t.Fatalf("第二项 WAL 构造失败: result=%+v err=%v", created, err)
	}
	failedAt := fixture.Now.Add(time.Minute)
	if _, err := s.ApplyResultMessage(
		created.Command.MsgID,
		"result-multi-bubble-failed-second",
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
	actions, err = s.CommunicationActionsByTurn(fixture.Turn.TurnID)
	if err != nil || len(actions) != 2 ||
		actions[1].Status != CommunicationActionManualRequired {
		t.Fatalf("中间失败后不得物化第三项: actions=%+v err=%v", actions, err)
	}
	turn, err := s.DialogueTurnByID(fixture.Turn.TurnID)
	if err != nil || turn == nil || turn.Status != DialogueTurnManualRequired {
		t.Fatalf("中间失败必须终止本轮: turn=%+v err=%v", turn, err)
	}
}

type communicationActionBeforeMultiBubble struct {
	ActionID      string                  `gorm:"primaryKey"`
	TurnID        string                  `gorm:"not null;uniqueIndex:ux_communication_action_turn_kind,priority:1"`
	Kind          CommunicationActionKind `gorm:"not null;uniqueIndex:ux_communication_action_turn_kind,priority:2"`
	Text          string                  `gorm:"not null"`
	ContentHash   string                  `gorm:"not null"`
	Status        CommunicationActionStatus
	FailureReason string
	PlannedAt     time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (communicationActionBeforeMultiBubble) TableName() string {
	return "communication_actions"
}

func TestCommunicationActionMultiBubbleMigrationDropsOnlyRetiredUniqueIndex(t *testing.T) {
	dir := t.TempDir()
	legacyDB, err := gorm.Open(
		sqlite.Open("file:"+filepath.Join(dir, "brain.db")),
		&gorm.Config{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacyDB.AutoMigrate(&communicationActionBeforeMultiBubble{}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Millisecond)
	legacy := communicationActionBeforeMultiBubble{
		ActionID:    "legacy-action",
		TurnID:      "legacy-turn",
		Kind:        CommunicationActionReplyText,
		Text:        "旧动作",
		ContentHash: textcanon.Hash("旧动作"),
		Status:      CommunicationActionSent,
		PlannedAt:   at,
		CreatedAt:   at,
		UpdatedAt:   at,
	}
	if err := legacyDB.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := legacyDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("旧库多气泡迁移失败: %v", err)
	}
	defer s.Close()
	if s.db.Migrator().HasIndex(&CommunicationAction{}, "ux_communication_action_turn_kind") {
		t.Fatal("已退役的 turn+kind 唯一索引仍存在")
	}
	var preserved CommunicationAction
	if err := s.db.First(&preserved, "action_id = ?", legacy.ActionID).Error; err != nil ||
		preserved.Text != legacy.Text ||
		preserved.Status != legacy.Status {
		t.Fatalf("迁移没有原样保留旧动作: action=%+v err=%v", preserved, err)
	}
	second := CommunicationAction{
		ActionID:          "new-second-bubble",
		TurnID:            legacy.TurnID,
		Kind:              CommunicationActionReplyText,
		Text:              "新气泡",
		ContentHash:       textcanon.Hash("新气泡"),
		DependsOnActionID: &legacy.ActionID,
		Status:            CommunicationActionPlanned,
		PlannedAt:         at.Add(time.Second),
		CreatedAt:         at.Add(time.Second),
		UpdatedAt:         at.Add(time.Second),
	}
	if err := s.db.Create(&second).Error; err != nil {
		t.Fatalf("同 turn 第二个 replyText 应由 actionID 独立标识: %v", err)
	}
	var count int64
	if err := s.db.Model(&CommunicationAction{}).
		Where("turn_id = ? AND kind = ?", legacy.TurnID, CommunicationActionReplyText).
		Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("迁移后动作数错误: count=%d err=%v", count, err)
	}
	if !s.db.Migrator().HasColumn(&DialogueTurn{}, "ReplyPhrases") {
		t.Fatal("迁移未增加 dialogue_turns.reply_phrases")
	}
}

func TestCommunicationV4PersistedPlanRejectsMissingPhraseBody(t *testing.T) {
	turn := DialogueTurn{ReplyPhrases: []string{"只有一项"}}
	plans := []communication.V4PlannedAction{
		{ActionKey: "one", Kind: communication.V4ActionReplyText},
		{ActionKey: "two", Kind: communication.V4ActionReplyText},
	}
	if text, ready := communicationV4PlanText(turn, plans, 1, ""); ready || strings.TrimSpace(text) != "" {
		t.Fatalf("缺失未来气泡正文时必须拒绝物化: text=%q ready=%v", text, ready)
	}
	if _, ready := communicationV4PlanText(
		DialogueTurn{},
		plans,
		1,
		"不能拿已物化正文冒充第二项",
	); ready {
		t.Fatal("旧单气泡兼容不得扩成多气泡正文来源")
	}
}
