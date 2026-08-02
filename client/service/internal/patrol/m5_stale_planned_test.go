package patrol

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// stalePlannedDialogueHarness 搭起"首气泡已 sent、次气泡 planned 残留"的
// 现场:AI 话术_序列给两个气泡,第二个气泡的节奏等待被注入 ErrDailyWindowExpired
// 模拟 24:00 掐断,pre-WAL 中断按既有语义保留 planned 残留。
type stalePlannedDialogueHarness struct {
	h         *harness
	fixture   communicationV4PatrolFixture
	advice    *recordingAdviceExecutor
	hand      *m5PositiveHand
	manager   *Manager
	turnID    string
	firstID   string
	secondID  string
	paceCalls int
	cutAt     int
}

func newStalePlannedDialogueHarness(
	t *testing.T,
	suffix string,
	cutSecondBubble bool,
) *stalePlannedDialogueHarness {
	t.Helper()
	h := newHarness(t)
	out := &stalePlannedDialogueHarness{h: h}
	out.fixture = seedCommunicationV4PatrolTarget(t, h, suffix, "这个岗位还在招吗")
	out.advice = &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			switch request.Purpose {
			case m5ai.PurposeIntent:
				return safeFakeResponse(`{"信号":"有意向","理由":"fixture"}`), nil
			case m5ai.PurposeReply:
				return safeFakeResponse(
					`{"话术_序列":["陈旧用例气泡一","陈旧用例气泡二"],"动作":"无"}`,
				), nil
			case m5ai.PurposeSilenceFollowup:
				return safeFakeResponse(`{"话术":"合成重规划冷催","抓的点":"合成经历"}`), nil
			default:
				return m5ai.CompletionResponse{}, fmt.Errorf("未知建议用途 %q", request.Purpose)
			}
		},
	}
	out.hand = &m5PositiveHand{now: h.clock.Now}
	dispatcher := dispatch.New(h.db, out.hand)
	out.hand.setDispatcher(dispatcher)
	runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
	config := h.config
	if cutSecondBubble {
		out.cutAt = 2
	}
	config.InteractionPaceWait = func(ctx context.Context) error {
		out.paceCalls++
		if out.cutAt > 0 && out.paceCalls == out.cutAt {
			return ErrDailyWindowExpired
		}
		return ctx.Err()
	}
	manager, err := NewManager(h.db, runner, h.hands, config, out.advice)
	if err != nil {
		t.Fatal(err)
	}
	out.manager = manager
	if err := manager.EnableToday(h.key); err != nil {
		t.Fatal(err)
	}
	return out
}

func (s *stalePlannedDialogueHarness) runRound(
	t *testing.T,
	manager *Manager,
	roundID string,
) error {
	t.Helper()
	account, err := s.h.db.AccountByKey(s.h.key)
	if err != nil || account == nil {
		t.Fatalf("读取账号失败: account=%+v err=%v", account, err)
	}
	beginCommunicationV4PatrolRound(t, s.h, roundID)
	actor := &roundActor{
		manager: manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: roundID, now: s.h.clock.Now(),
	}
	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	return err
}

// interruptAfterFirstBubble 跑首轮并断言中断后留下"sent 前缀 + planned 残留"。
func (s *stalePlannedDialogueHarness) interruptAfterFirstBubble(t *testing.T) {
	t.Helper()
	err := s.runRound(t, s.manager, "round-stale-planned-cut")
	if !errors.Is(err, ErrDailyWindowExpired) {
		t.Fatalf("链内第二气泡未被日界掐断: err=%v", err)
	}
	if s.hand.commandCount() != 1 {
		t.Fatalf("中断前必须恰好发出首气泡: sends=%d", s.hand.commandCount())
	}
	turn, err := s.h.db.LatestDialogueTurnForProfile(s.fixture.profileID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnAdviceReady {
		t.Fatalf("中断后轮未停在 adviceReady: turn=%+v err=%v", turn, err)
	}
	s.turnID = turn.TurnID
	first, err := s.h.db.CommunicationActionByTurn(s.turnID)
	if err != nil || first == nil || first.Status != store.CommunicationActionSent ||
		first.EffectIntentID == nil {
		t.Fatalf("首气泡未 sent: action=%+v err=%v", first, err)
	}
	s.firstID = first.ActionID
	second, err := s.h.db.PlannedCommunicationActionByTurn(s.turnID)
	if err != nil || second == nil || second.EffectIntentID != nil ||
		second.EffectStartedAt != nil || second.SentAt != nil {
		t.Fatalf("次气泡残留不是从未派发的 planned: action=%+v err=%v", second, err)
	}
	s.secondID = second.ActionID
	s.cutAt = 0
}

// assertResidualVoided 断言派发遭遇后的 Q1/Q2 收束:残留作废、轮 completed、
// sent 前缀原样、聚合 active、无新发送与新建议。
func (s *stalePlannedDialogueHarness) assertResidualVoided(t *testing.T) {
	t.Helper()
	second, err := s.h.db.CommunicationActionByID(s.secondID)
	if err != nil || second == nil ||
		second.Status != store.CommunicationActionSuperseded ||
		second.FailureReason != store.CommunicationStalePlannedSuperseded ||
		second.EffectIntentID != nil || second.SentAt != nil {
		t.Fatalf("陈旧残留未按显式原因作废: action=%+v err=%v", second, err)
	}
	turn, err := s.h.db.DialogueTurnByID(s.turnID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnCompleted ||
		turn.FailureReason != store.CommunicationStalePlannedSuperseded {
		t.Fatalf("sent 前缀轮未收束为 completed: turn=%+v err=%v", turn, err)
	}
	first, err := s.h.db.CommunicationActionByID(s.firstID)
	if err != nil || first == nil || first.Status != store.CommunicationActionSent ||
		first.EffectIntentID == nil || first.SentAt == nil {
		t.Fatalf("已正证前项被触碰: action=%+v err=%v", first, err)
	}
	aggregate, err := s.h.db.CommunicationV4AggregateByProfile(s.fixture.profileID)
	if err != nil ||
		aggregate.AutomationStatus != store.ProfileCommunicationAutomationActive {
		t.Fatalf("作废不得冻结聚合: aggregate=%+v err=%v", aggregate, err)
	}
	if s.hand.commandCount() != 1 {
		t.Fatalf("作废不得补发残留: sends=%d", s.hand.commandCount())
	}
	if len(s.advice.requests) != 2 {
		t.Fatalf("作废不得触发新建议调用: calls=%d", len(s.advice.requests))
	}
}

// 测试要求 1(跨日):昨日创建的 planned 余项在次日派发遭遇时作废,轮收束
// completed,已 sent 前缀原样;时刻表照常接管并按最新状态重新规划(催该催的)。
func TestStalePlannedDialogueResidualVoidedAcrossDayThenScheduleReplans(t *testing.T) {
	s := newStalePlannedDialogueHarness(t, "stale-cross-day", true)
	s.interruptAfterFirstBubble(t)

	s.h.clock.Add(25 * time.Hour)
	if err := s.manager.EnableToday(s.h.key); err != nil {
		t.Fatal(err)
	}
	if err := s.runRound(t, s.manager, "round-stale-planned-void"); err != nil {
		t.Fatal(err)
	}
	s.assertResidualVoided(t)

	// 时刻表可继续:再跑一轮,沉默追问按最新世界状态重新规划并发出冷催,
	// 证明作废没有把候选人卡死。
	s.h.runner.handler = scheduleThreadEchoHandler(t, s.h, s.fixture.conversationRef)
	if err := s.runRound(t, s.manager, "round-stale-planned-replan"); err != nil {
		t.Fatal(err)
	}
	if s.hand.commandCount() != 2 {
		t.Fatalf("时刻表未按最新状态重新规划冷催: sends=%d", s.hand.commandCount())
	}
	actions, err := s.h.db.CommunicationV4EventActionsByProfile(s.fixture.profileID)
	if err != nil {
		t.Fatal(err)
	}
	coldSent := false
	for index := range actions {
		if actions[index].V4Kind == communication.V4ActionColdPrompt &&
			actions[index].Status == store.CommunicationV4EventActionSent {
			coldSent = true
		}
	}
	if !coldSent {
		t.Fatalf("冷催未走 EventAction/WAL 正证轨: actions=%+v", actions)
	}
	turn, err := s.h.db.DialogueTurnByID(s.turnID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnCompleted {
		t.Fatalf("重新规划不得复活已收束的轮: turn=%+v err=%v", turn, err)
	}
}

// 测试要求 2(跨启动):同日内脑重启(新 Manager)后,启动前创建的 planned
// 残留同样在派发遭遇时作废。
func TestStalePlannedDialogueResidualVoidedAcrossRestart(t *testing.T) {
	s := newStalePlannedDialogueHarness(t, "stale-restart", true)
	s.interruptAfterFirstBubble(t)

	restartedDispatcher := dispatch.New(s.h.db, s.hand)
	s.hand.setDispatcher(restartedDispatcher)
	restartedRunner := &m5AutomaticReplyRunner{
		base:       s.h.runner,
		dispatcher: restartedDispatcher,
	}
	config := s.h.config
	config.InteractionPaceWait = func(ctx context.Context) error {
		return ctx.Err()
	}
	restarted, err := NewManager(s.h.db, restartedRunner, s.h.hands, config, s.advice)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.runRound(t, restarted, "round-stale-planned-restart"); err != nil {
		t.Fatal(err)
	}
	s.assertResidualVoided(t)
}

// 测试要求 3(同日同进程零影响):不注入中断时,两个气泡当场创建当场派发,
// 一个不落,陈旧判定不产生任何作废。
func TestStalePlannedSameDaySameProcessChainUnaffected(t *testing.T) {
	s := newStalePlannedDialogueHarness(t, "stale-sameday", false)
	if err := s.runRound(t, s.manager, "round-stale-planned-sameday"); err != nil {
		t.Fatal(err)
	}
	if s.hand.commandCount() != 2 {
		t.Fatalf("同日同进程链未完整派发: sends=%d", s.hand.commandCount())
	}
	turn, err := s.h.db.LatestDialogueTurnForProfile(s.fixture.profileID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnCompleted ||
		turn.FailureReason != "" {
		t.Fatalf("完整链未正常完成: turn=%+v err=%v", turn, err)
	}
	entries, err := s.h.db.AuditEntries(200)
	if err != nil {
		t.Fatal(err)
	}
	for index := range entries {
		if entries[index].Category == "communication_stale_planned_superseded" {
			t.Fatalf("同日链不得触发陈旧作废审计: entry=%+v", entries[index])
		}
	}
}

// 测试要求 1/3 的事件动作轨与时刻表侧:昨日冻结的时刻表链首 planned 残留在
// 次日派发遭遇(drain)时作废,不再触碰页面;当日同进程遭遇不受影响的部分由
// 首轮"保持 planned"断言覆盖。
func TestStalePlannedScheduleEventActionVoidedAtDrain(t *testing.T) {
	h := newHarness(t)
	h.clock.Add(scheduleTestBusinessNow().Sub(h.clock.Now()))
	h.clock.Add(-25 * time.Hour)
	fixture := seedCommunicationV4PatrolTargetWithBoundary(t, h, "stale-drain", nil)
	h.clock.Add(25 * time.Hour)
	// 链首定向对账持续失败:既有语义是"本轮跳过,动作保持 planned 留待下一轮",
	// 恰好制造跨日残留。
	h.runner.handler = func(request RunRequest) (any, error) {
		return nil, fmt.Errorf("合成:平台页面不可用 %s", request.Name)
	}
	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			if request.Purpose != m5ai.PurposeSilenceFollowup {
				return m5ai.CompletionResponse{}, fmt.Errorf("未知建议用途 %q", request.Purpose)
			}
			return safeFakeResponse(`{"话术":"合成冷催一","抓的点":"合成经历"}`), nil
		},
	}
	hand := &m5PositiveHand{now: h.clock.Now}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
	manager, err := NewManager(h.db, runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.EnableToday(h.key); err != nil {
		t.Fatal(err)
	}
	runCommunicationV4ScheduleRound(t, h, manager, "round-stale-drain-freeze")
	actions, err := h.db.CommunicationV4EventActionsByProfile(fixture.profileID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != store.CommunicationV4EventActionPlanned ||
		actions[0].EffectIntentID != nil {
		t.Fatalf("对账失败轮未保持 planned 残留: actions=%+v err=%v", actions, err)
	}
	if hand.commandCount() != 0 {
		t.Fatalf("对账失败不得发送: sends=%d", hand.commandCount())
	}
	staleID := actions[0].ActionID
	readsBefore := h.runner.count(protocol.PrimChatReadThread)

	h.clock.Add(24 * time.Hour)
	if err := manager.EnableToday(h.key); err != nil {
		t.Fatal(err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("读取账号失败: account=%+v err=%v", account, err)
	}
	actor := &roundActor{
		manager: manager, account: account,
		hand: HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		now:  h.clock.Now(),
	}
	manager.mu.Lock()
	err = actor.drainCommunicationV4EventActionsForProfile(
		context.Background(),
		fixture.profileID,
	)
	manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	voided, err := h.db.CommunicationV4EventActionByID(staleID)
	if err != nil || voided == nil ||
		voided.Status != store.CommunicationV4EventActionManualRequired ||
		voided.FailureReason != store.CommunicationStalePlannedSuperseded ||
		voided.EffectIntentID != nil {
		t.Fatalf("时刻表残留未在派发遭遇时作废: action=%+v err=%v", voided, err)
	}
	aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil ||
		aggregate.AutomationStatus != store.ProfileCommunicationAutomationActive {
		t.Fatalf("作废不得冻结聚合: aggregate=%+v err=%v", aggregate, err)
	}
	if hand.commandCount() != 0 ||
		h.runner.count(protocol.PrimChatReadThread) != readsBefore {
		t.Fatalf("作废不得发送或触碰页面: sends=%d reads=%d/%d",
			hand.commandCount(),
			h.runner.count(protocol.PrimChatReadThread),
			readsBefore)
	}
}

// 测试要求 4(effect-bound 红线回归)的巡检侧:首气泡 sent(绑过 intent)后,
// 对它调用作废入口必须拒绝且原样不动;从未绑 intent 的残留才可作废。
func TestStalePlannedVoidRefusesEffectBoundRows(t *testing.T) {
	s := newStalePlannedDialogueHarness(t, "stale-bound-guard", true)
	s.interruptAfterFirstBubble(t)
	if _, err := s.h.db.SupersedeStaleDialoguePlannedAction(
		s.turnID,
		s.firstID,
		s.h.clock.Now(),
	); !errors.Is(err, store.ErrCommunicationActionConflict) {
		t.Fatalf("绑过 intent 的 sent 前项必须拒绝作废: err=%v", err)
	}
	first, err := s.h.db.CommunicationActionByID(s.firstID)
	if err != nil || first == nil || first.Status != store.CommunicationActionSent ||
		first.EffectIntentID == nil {
		t.Fatalf("effect-bound 行被触碰: action=%+v err=%v", first, err)
	}
	turn, err := s.h.db.DialogueTurnByID(s.turnID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnAdviceReady {
		t.Fatalf("拒绝作废不得改写轮状态: turn=%+v err=%v", turn, err)
	}
}
