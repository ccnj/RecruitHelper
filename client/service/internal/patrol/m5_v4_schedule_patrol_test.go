package patrol

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

func TestCommunicationV4SchedulePatrolSendsColdPromptThenWechatSequence(
	t *testing.T,
) {
	h := newHarness(t)
	h.clock.Add(scheduleTestBusinessNow().Sub(h.clock.Now()))
	h.clock.Add(-25 * time.Hour)
	// 催2配置为两个气泡:正文链必须逐项正证后再物化下一项,最后才发卡。
	fixture := seedCommunicationV4PatrolTargetWithBoundaryAndFixedPhrases(
		t,
		h,
		"schedule-cold-sequence",
		nil,
		`{
			"rejectWechat":{"enabled":true,"messages":["合成挽留"]},
			"silence48Wechat":{"enabled":true,"messages":["合成冷催上","合成冷催下"]},
			"wechatAccepted":{"enabled":true,"messages":["好的，晚点加你"]},
			"meetingAccepted":{"enabled":true,"messages":["好的，面试安排已确认"]}
		}`,
	)
	h.clock.Add(25 * time.Hour)
	// 时刻表链首派发前会定向对账目标会话;把夹具账本回声成页面快照放行发送。
	h.runner.handler = scheduleThreadEchoHandler(t, h, fixture.conversationRef)

	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			if request.Purpose != m5ai.PurposeSilenceFollowup ||
				request.PromptRevision != m5ai.SilenceFollowupRenderVersion {
				return m5ai.CompletionResponse{}, fmt.Errorf(
					"沉默追问用途错误: purpose=%q revision=%q",
					request.Purpose,
					request.PromptRevision,
				)
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
	runCommunicationV4ScheduleRound(
		t,
		h,
		manager,
		"round-v4-schedule-cold-one",
	)
	if len(advice.requests) != 1 || hand.commandCount() != 1 {
		t.Fatalf(
			"冷催一必须一次 provider、一次正文发送: advice=%d sends=%d",
			len(advice.requests),
			hand.commandCount(),
		)
	}
	actions, err := h.db.CommunicationV4EventActionsByProfile(fixture.profileID)
	if err != nil || len(actions) != 1 ||
		actions[0].V4Kind != communication.V4ActionColdPrompt ||
		actions[0].Status != store.CommunicationV4EventActionSent ||
		actions[0].EffectIntentID == nil {
		t.Fatalf("冷催一没有走 EventAction/WAL 正证轨: actions=%+v err=%v", actions, err)
	}

	// 同一真实消息轮的 AI 冷催已用过；下一次 24h 档位必须走固定正文，
	// 其唯一正证在同一 drain 中才会物化并发送换微信卡。
	afterColdOne, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || afterColdOne == nil || afterColdOne.State.LastOutboundAt == nil {
		t.Fatalf("冷催一后缺少出站锚: aggregate=%+v err=%v", afterColdOne, err)
	}
	h.clock.Add(
		nextScheduleTestBusinessTime(
			afterColdOne.State.LastOutboundAt.Add(24 * time.Hour),
		).Sub(h.clock.Now()),
	)
	if err := manager.EnableToday(h.key); err != nil {
		t.Fatal(err)
	}
	runCommunicationV4ScheduleRound(
		t,
		h,
		manager,
		"round-v4-schedule-cold-two",
	)
	if len(advice.requests) != 1 || hand.commandCount() != 4 {
		aggregate, _ := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
		t.Fatalf(
			"冷催二必须不调用 AI 且按气泡×2→卡片各发一次: advice=%d sends=%d aggregate=%+v",
			len(advice.requests),
			hand.commandCount(),
			aggregate,
		)
	}
	actions, err = h.db.CommunicationV4EventActionsByProfile(fixture.profileID)
	if err != nil || len(actions) != 4 {
		t.Fatalf("冷催动作事实数量错误: actions=%+v err=%v", actions, err)
	}
	var coldTextOne, coldTextTwo, coldInvite *store.CommunicationV4EventAction
	for index := range actions {
		switch actions[index].V4Kind {
		case communication.V4ActionColdWechatText:
			if actions[index].SourceOrdinal == 0 {
				coldTextOne = &actions[index]
			} else {
				coldTextTwo = &actions[index]
			}
		case communication.V4ActionColdWechatInvite:
			coldInvite = &actions[index]
		}
	}
	if coldTextOne == nil || coldTextTwo == nil || coldInvite == nil ||
		coldTextOne.Status != store.CommunicationV4EventActionSent ||
		coldTextTwo.Status != store.CommunicationV4EventActionSent ||
		coldInvite.Status != store.CommunicationV4EventActionSent ||
		coldTextOne.Text != "合成冷催上" ||
		coldTextTwo.Text != "合成冷催下" ||
		strings.Contains(coldTextOne.SemanticActionKey, "|bubble:") ||
		!strings.HasSuffix(coldTextTwo.SemanticActionKey, "|bubble:2") ||
		coldTextOne.DependsOnActionID != nil ||
		coldTextTwo.DependsOnActionID == nil ||
		*coldTextTwo.DependsOnActionID != coldTextOne.ActionID ||
		coldInvite.DependsOnActionID == nil ||
		*coldInvite.DependsOnActionID != coldTextTwo.ActionID ||
		coldTextOne.EffectIntentID == nil ||
		coldTextTwo.EffectIntentID == nil ||
		coldInvite.EffectIntentID == nil {
		t.Fatalf(
			"冷催二没有按气泡正证链→卡片依赖收敛: one=%+v two=%+v invite=%+v",
			coldTextOne,
			coldTextTwo,
			coldInvite,
		)
	}

	t.Run("冷催换微信卡缺少正文依赖时派发入口拒绝", func(t *testing.T) {
		account, err := h.db.AccountByKey(h.key)
		if err != nil || account == nil {
			t.Fatalf("读取账号失败: account=%+v err=%v", account, err)
		}
		malformed := *coldInvite
		malformed.DependsOnActionID = nil
		actor := &roundActor{
			manager: manager,
			account: account,
			hand: HandState{
				Online:  true,
				Session: "session-1",
				BootID:  "boot-1",
			},
			now: h.clock.Now(),
		}
		manager.mu.Lock()
		stopped, dispatchErr := actor.dispatchCommunicationV4EventAction(
			context.Background(),
			malformed,
		)
		manager.mu.Unlock()
		if dispatchErr != nil || !stopped || hand.commandCount() != 4 {
			t.Fatalf(
				"缺父依赖的冷催卡不得进入派发: stopped=%v sends=%d err=%v",
				stopped,
				hand.commandCount(),
				dispatchErr,
			)
		}
	})
}

func TestCommunicationV4SchedulePatrolSendsFixedColdPromptWithoutAI(
	t *testing.T,
) {
	h := newHarness(t)
	h.clock.Add(scheduleTestBusinessNow().Sub(h.clock.Now()))
	h.clock.Add(-25 * time.Hour)
	// 催1 配置了固定话术:到期直接以固定文本冻结计划,一次 provider 都不许调。
	fixture := seedCommunicationV4PatrolTargetWithBoundaryAndFixedPhrases(
		t,
		h,
		"schedule-cold-fixed",
		nil,
		`{
			"silence24Followup":{"enabled":true,"messages":["前面的消息看到了吗？方便回个话。"]},
			"silence48Wechat":{"enabled":true,"messages":["合成冷催二"]}
		}`,
	)
	h.clock.Add(25 * time.Hour)
	h.runner.handler = scheduleThreadEchoHandler(t, h, fixture.conversationRef)

	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			return m5ai.CompletionResponse{}, fmt.Errorf(
				"固定催1 不得发起 provider 调用: purpose=%q",
				request.Purpose,
			)
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
	runCommunicationV4ScheduleRound(
		t,
		h,
		manager,
		"round-v4-schedule-cold-fixed",
	)
	if len(advice.requests) != 0 || hand.commandCount() != 1 {
		t.Fatalf(
			"固定催1 必须零 provider、一次正文发送: advice=%d sends=%d",
			len(advice.requests),
			hand.commandCount(),
		)
	}
	actions, err := h.db.CommunicationV4EventActionsByProfile(fixture.profileID)
	if err != nil || len(actions) != 1 ||
		actions[0].V4Kind != communication.V4ActionColdPromptFixed ||
		actions[0].Status != store.CommunicationV4EventActionSent ||
		actions[0].Text != "前面的消息看到了吗？方便回个话。" ||
		actions[0].EffectIntentID == nil {
		t.Fatalf("固定催1 没有走 EventAction/WAL 正证轨: actions=%+v err=%v", actions, err)
	}
	afterFixed, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || afterFixed == nil ||
		!afterFixed.State.ColdPromptFixedTextSent ||
		afterFixed.State.ColdPromptSentCount != 1 ||
		afterFixed.State.ColdPromptRemaining != 1 ||
		afterFixed.State.LastOutboundAt == nil {
		t.Fatalf("固定催1 正证没有置公理4 标记并扣预算: aggregate=%+v err=%v", afterFixed, err)
	}

	// 下一次 24h 档位:催1 已发固定文本,阶梯落到固定催2+换微信卡,仍零 AI。
	h.clock.Add(
		nextScheduleTestBusinessTime(
			afterFixed.State.LastOutboundAt.Add(24 * time.Hour),
		).Sub(h.clock.Now()),
	)
	if err := manager.EnableToday(h.key); err != nil {
		t.Fatal(err)
	}
	runCommunicationV4ScheduleRound(
		t,
		h,
		manager,
		"round-v4-schedule-cold-fixed-two",
	)
	if len(advice.requests) != 0 || hand.commandCount() != 3 {
		aggregate, _ := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
		t.Fatalf(
			"固定催1 后的催2 必须零 AI 且按正文→卡片各发一次: advice=%d sends=%d aggregate=%+v",
			len(advice.requests),
			hand.commandCount(),
			aggregate,
		)
	}
	actions, err = h.db.CommunicationV4EventActionsByProfile(fixture.profileID)
	if err != nil || len(actions) != 3 {
		t.Fatalf("固定催1+催2 动作事实数量错误: actions=%+v err=%v", actions, err)
	}
	kinds := map[communication.V4ActionKind]int{}
	for _, action := range actions {
		if action.Status != store.CommunicationV4EventActionSent {
			t.Fatalf("存在未正证收编的冷催动作: %+v", action)
		}
		kinds[action.V4Kind]++
	}
	if kinds[communication.V4ActionColdPromptFixed] != 1 ||
		kinds[communication.V4ActionColdWechatText] != 1 ||
		kinds[communication.V4ActionColdWechatInvite] != 1 {
		t.Fatalf("冷催阶梯形态错误: kinds=%+v", kinds)
	}
}

func TestCommunicationV4SchedulePatrolArchivesBeforeOldPlannedAction(
	t *testing.T,
) {
	h := newHarness(t)
	h.clock.Add(scheduleTestBusinessNow().Sub(h.clock.Now()))
	h.clock.Add(-25 * time.Hour)
	fixture := seedCommunicationV4PatrolTargetWithBoundary(
		t,
		h,
		"schedule-fallback-before-drain",
		nil,
	)
	h.clock.Add(25 * time.Hour)
	target, ready, err := h.db.CommunicationTargetForProfile(fixture.profileID)
	if err != nil || !ready || target == nil {
		t.Fatalf("时刻表目标不可用: target=%+v ready=%v err=%v", target, ready, err)
	}
	material, ready, err := h.db.CommunicationAIMaterialForProfile(fixture.profileID)
	if err != nil || !ready {
		t.Fatalf("时刻表 AI 材料不可用: ready=%v err=%v", ready, err)
	}
	frozen, err := h.db.FreezeCommunicationV4SchedulePlan(
		store.FreezeCommunicationV4SchedulePlanRequest{
			ProfileID:                   fixture.profileID,
			ConversationRef:             fixture.conversationRef,
			ExpectedRevision:            target.Aggregate.Revision,
			ExpectedProjectedThroughSeq: target.Aggregate.ProjectedThroughSeq,
			ContextRevisionHash:         material.ContextRevision.RevisionHash,
			Reply: communication.ReplyAdvice{
				State:      communication.AdviceOK,
				Suggestion: m5ai.ReplySuggestion{Text: "不得越过七天归档发送"},
			},
			EvaluatedAt: h.clock.Now(),
			FrozenAt:    h.clock.Now(),
		},
	)
	if err != nil || frozen == nil || frozen.Plan == nil ||
		len(frozen.Actions) != 1 {
		t.Fatalf("未能冻结七天前的待发送动作: result=%+v err=%v", frozen, err)
	}

	lastBodyAt := target.Aggregate.State.LastBodyAt
	if lastBodyAt == nil {
		t.Fatal("测试缺少正文锚")
	}
	fallbackAt := lastBodyAt.Add(8 * 24 * time.Hour)
	h.clock.Add(fallbackAt.Sub(h.clock.Now()))

	hand := &m5PositiveHand{now: h.clock.Now}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
	manager, err := NewManager(h.db, runner, h.hands, h.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.EnableToday(h.key); err != nil {
		t.Fatal(err)
	}
	runCommunicationV4ScheduleRound(
		t,
		h,
		manager,
		"round-v4-schedule-fallback-before-drain",
	)
	aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil ||
		aggregate.State.MainStatus != communication.V4StatusEnded ||
		aggregate.State.EndReason != communication.V4EndFallback ||
		hand.commandCount() != 0 {
		t.Fatalf(
			"七天归档没有先于旧动作收敛: aggregate=%+v sends=%d err=%v",
			aggregate,
			hand.commandCount(),
			err,
		)
	}
	actions, err := h.db.CommunicationV4EventActionsByProfile(fixture.profileID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != store.CommunicationV4EventActionPlanned ||
		actions[0].EffectIntentID != nil {
		t.Fatalf("归档后旧动作不得进入 WAL: actions=%+v err=%v", actions, err)
	}

	// 归档不是只保护当前一轮。旧 planned 行会作为不可变业务事实保留，
	// 但后续任意巡检都必须持续把它排除在 WAL 构造候选之外。
	runCommunicationV4ScheduleRound(
		t,
		h,
		manager,
		"round-v4-schedule-fallback-repeat",
	)
	actions, err = h.db.CommunicationV4EventActionsByProfile(fixture.profileID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != store.CommunicationV4EventActionPlanned ||
		actions[0].EffectIntentID != nil ||
		hand.commandCount() != 0 {
		t.Fatalf(
			"七天归档后的后续巡检不得复活旧动作: actions=%+v sends=%d err=%v",
			actions,
			hand.commandCount(),
			err,
		)
	}
}

func TestCommunicationV4SchedulePatrolRechecksFallbackBetweenColdTextAndInvite(
	t *testing.T,
) {
	h := newHarness(t)
	wall := scheduleTestWallAnchor
	secondRoundAt := nextScheduleTestBusinessTime(wall.Add(25 * time.Hour))
	bodyAt := secondRoundAt.Add(-167 * time.Hour)
	h.clock.Add(bodyAt.Sub(h.clock.Now()))
	fixture := seedCommunicationV4PatrolTargetWithBoundary(
		t,
		h,
		"schedule-fallback-between-cold-actions",
		nil,
	)
	h.clock.Add(scheduleTestWindowAtOrAfter(wall).Sub(h.clock.Now()))
	// 时刻表链首派发前会定向对账目标会话;把夹具账本回声成页面快照放行发送。
	h.runner.handler = scheduleThreadEchoHandler(t, h, fixture.conversationRef)

	advice := &recordingAdviceExecutor{
		complete: func(_ int, _ m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			return safeFakeResponse(`{"话术":"合成冷催一","抓的点":"合成经历"}`), nil
		},
	}
	paceCalls := 0
	var crossAt time.Time
	config := h.config
	config.InteractionPaceWait = func(ctx context.Context) error {
		paceCalls++
		// 第二轮走本档的节奏序列:冷催正文发送(1)→换微信卡发送(2)。链首
		// 对账读会切会话,走的是换人档(SourcingPaceWait),不计入本探针。
		// 在卡片自己的节奏等待里跨过七天边界,由 WAL 前复核把卡拦下。
		if !crossAt.IsZero() && paceCalls == 2 {
			h.clock.Add(crossAt.Sub(h.clock.Now()))
		}
		return ctx.Err()
	}
	hand := &m5PositiveHand{now: h.clock.Now}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
	manager, err := NewManager(h.db, runner, h.hands, config, advice)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.EnableToday(h.key); err != nil {
		t.Fatal(err)
	}
	runCommunicationV4ScheduleRound(
		t,
		h,
		manager,
		"round-v4-schedule-cross-fallback-cold-one",
	)
	afterColdOne, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || afterColdOne == nil ||
		afterColdOne.State.LastOutboundAt == nil ||
		afterColdOne.State.LastBodyAt == nil {
		t.Fatalf("冷催一后时钟不可用: aggregate=%+v err=%v", afterColdOne, err)
	}
	if secondRoundAt.Before(afterColdOne.State.LastOutboundAt.Add(24 * time.Hour)) {
		t.Fatalf(
			"测试第二轮未跨过冷催 24h 门槛: second=%v outbound=%v",
			secondRoundAt,
			afterColdOne.State.LastOutboundAt,
		)
	}
	h.clock.Add(secondRoundAt.Sub(h.clock.Now()))
	if err := manager.EnableToday(h.key); err != nil {
		t.Fatal(err)
	}
	paceCalls = 0
	crossAt = afterColdOne.State.LastBodyAt.Add(7*24*time.Hour + time.Second)
	runCommunicationV4ScheduleRound(
		t,
		h,
		manager,
		"round-v4-schedule-cross-fallback-cold-two",
	)

	aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || aggregate == nil ||
		aggregate.State.MainStatus != communication.V4StatusEnded ||
		aggregate.State.EndReason != communication.V4EndFallback ||
		hand.commandCount() != 2 {
		t.Fatalf(
			"父正文后跨七天边界必须归档且子卡零发送: aggregate=%+v sends=%d err=%v",
			aggregate,
			hand.commandCount(),
			err,
		)
	}
	actions, err := h.db.CommunicationV4EventActionsByProfile(fixture.profileID)
	if err != nil || len(actions) != 3 {
		t.Fatalf("跨边界后的动作事实错误: actions=%+v err=%v", actions, err)
	}
	var child *store.CommunicationV4EventAction
	for index := range actions {
		if actions[index].V4Kind == communication.V4ActionColdWechatInvite {
			child = &actions[index]
			break
		}
	}
	if child == nil ||
		child.Status != store.CommunicationV4EventActionPlanned ||
		child.EffectIntentID != nil {
		t.Fatalf("跨七天边界的子卡不得进入 WAL: child=%+v", child)
	}
}

func runCommunicationV4ScheduleRound(
	t *testing.T,
	h *harness,
	manager *Manager,
	roundID string,
) {
	t.Helper()
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("读取账号失败: account=%+v err=%v", account, err)
	}
	beginCommunicationV4PatrolRound(t, h, roundID)
	actor := &roundActor{
		manager: manager,
		account: account,
		hand: HandState{
			Online:  true,
			Session: "session-1",
			BootID:  "boot-1",
		},
		roundID: roundID,
		now:     h.clock.Now(),
	}
	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
}

// scheduleTestWallAnchor 是 schedule 测试的固定挂钟锚点。必须晚于
// newHarness 的假时钟纪元(2026-07-17 09:00 UTC)至少 167 小时,保证
// 各测试从锚点回拨 25h/167h 定位 fixture 时不会早于账号建档时刻;
// 小时钉在 10:00 UTC 使统一业务窗口判定与真实执行时刻无关。
var scheduleTestWallAnchor = time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)

func scheduleTestBusinessNow() time.Time {
	return scheduleTestWallAnchor
}

func nextScheduleTestBusinessTime(notBefore time.Time) time.Time {
	notBefore = notBefore.UTC()
	next := time.Date(
		notBefore.Year(),
		notBefore.Month(),
		notBefore.Day(),
		10,
		0,
		0,
		0,
		time.UTC,
	)
	if next.Before(notBefore) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

func scheduleTestWindowAtOrAfter(notBefore time.Time) time.Time {
	notBefore = notBefore.UTC()
	if notBefore.Hour() >= 8 {
		return notBefore
	}
	return time.Date(
		notBefore.Year(),
		notBefore.Month(),
		notBefore.Day(),
		8,
		0,
		0,
		0,
		time.UTC,
	)
}

// scheduleThreadEchoHandler 把夹具会话的当前账本回声成 readThread 快照,其余
// 原语走 defaultHandler。时刻表链首派发前的定向对账要求一次真实读取;账本
// 回声带全 cardType/cardState/sourceKey 才能与锚完整重叠、判为无新增。
func scheduleThreadEchoHandler(
	t *testing.T,
	h *harness,
	conversationRef string,
) func(RunRequest) (any, error) {
	t.Helper()
	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: conversationRef,
	}
	return func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadThread {
			return protocol.ChatReadThreadData{
				Messages: echoLedgerAsThread(t, h, key),
				Complete: true, ReachedTop: true,
			}, nil
		}
		return defaultHandler(request)
	}
}

// scheduleOrderRecordingRunner 在共享序列里记录发送时点,与 handler 记录的
// 读取时点合成同一条时间线,用于断言"先对账导航、后发送"的顺序。
type scheduleOrderRecordingRunner struct {
	*m5AutomaticReplyRunner
	mu    *sync.Mutex
	order *[]string
}

func (r *scheduleOrderRecordingRunner) StartAutomaticReply(
	ctx context.Context,
	req AutomaticReplyRequest,
) (AutomaticReplyHandle, error) {
	r.mu.Lock()
	*r.order = append(*r.order, "send:"+req.ConversationRef)
	r.mu.Unlock()
	return r.m5AutomaticReplyRunner.StartAutomaticReply(ctx, req)
}

// 出口回归(2026-07-30):send 系原语只认已打开的会话页、自身不导航,时刻表
// 链首必须先经定向对账把目标会话页打开再发送;顺序颠倒就是当日真机
// 49/49 CTX_NOT_READY/pageAbsent 事故的形态。
func TestCommunicationV4SchedulePatrolNavigatesBeforeChainHeadSend(
	t *testing.T,
) {
	h := newHarness(t)
	h.clock.Add(scheduleTestBusinessNow().Sub(h.clock.Now()))
	h.clock.Add(-25 * time.Hour)
	fixture := seedCommunicationV4PatrolTargetWithBoundary(
		t,
		h,
		"schedule-navigate-first",
		nil,
	)
	h.clock.Add(25 * time.Hour)

	var mu sync.Mutex
	var order []string
	echo := scheduleThreadEchoHandler(t, h, fixture.conversationRef)
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadThread {
			args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
			mu.Lock()
			order = append(order, "read:"+args.ConversationRef)
			mu.Unlock()
		}
		return echo(request)
	}
	advice := &recordingAdviceExecutor{
		complete: func(_ int, _ m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			return safeFakeResponse(`{"话术":"合成冷催一","抓的点":"合成经历"}`), nil
		},
	}
	hand := &m5PositiveHand{now: h.clock.Now}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &scheduleOrderRecordingRunner{
		m5AutomaticReplyRunner: &m5AutomaticReplyRunner{
			base: h.runner, dispatcher: dispatcher,
		},
		mu: &mu, order: &order,
	}
	manager, err := NewManager(h.db, runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.EnableToday(h.key); err != nil {
		t.Fatal(err)
	}
	runCommunicationV4ScheduleRound(
		t,
		h,
		manager,
		"round-v4-schedule-navigate-first",
	)

	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	want := []string{
		"read:" + fixture.conversationRef,
		"send:" + fixture.conversationRef,
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("链首必须恰好一次对账读取在前、一次发送在后: got=%v", got)
	}
	actions, err := h.db.CommunicationV4EventActionsByProfile(fixture.profileID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != store.CommunicationV4EventActionSent ||
		actions[0].EffectIntentID == nil {
		t.Fatalf("导航后链首没有走 WAL 正证轨: actions=%+v err=%v", actions, err)
	}
}

// 出口回归(2026-07-30):链首对账失败只允许"本轮跳过、动作保持 planned",不得
// 铸意图、不得转 manualRequired。真机已证实刚导航出的同步窗口内平台会对有
// 消息的会话返回空快照(2026-07-28 事实),该瞬时失败必须能在后续轮自愈。
func TestCommunicationV4SchedulePatrolKeepsChainHeadPlannedWhenNavigationUnconfirmed(
	t *testing.T,
) {
	h := newHarness(t)
	h.clock.Add(scheduleTestBusinessNow().Sub(h.clock.Now()))
	h.clock.Add(-25 * time.Hour)
	fixture := seedCommunicationV4PatrolTargetWithBoundary(
		t,
		h,
		"schedule-navigate-degrade",
		nil,
	)
	h.clock.Add(25 * time.Hour)
	// defaultHandler 的 readThread 返回空快照,正是真机"导航同步窗口空读"的
	// 形态;只额外计数,不放行。
	reads := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadThread {
			reads++
		}
		return defaultHandler(request)
	}
	advice := &recordingAdviceExecutor{
		complete: func(_ int, _ m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
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
	assertPlannedUntouched := func(stage string, wantReads int) {
		t.Helper()
		if hand.commandCount() != 0 {
			t.Fatalf("%s: 对账未确认不得发送: sends=%d", stage, hand.commandCount())
		}
		if reads != wantReads {
			t.Fatalf("%s: 每次派发尝试恰好一次读取、不得原地重试: reads=%d want=%d",
				stage, reads, wantReads)
		}
		actions, err := h.db.CommunicationV4EventActionsByProfile(fixture.profileID)
		if err != nil || len(actions) != 1 ||
			actions[0].Status != store.CommunicationV4EventActionPlanned ||
			actions[0].EffectIntentID != nil {
			t.Fatalf("%s: 链首必须保持 planned 且未铸意图: actions=%+v err=%v",
				stage, actions, err)
		}
		aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
		if err != nil ||
			aggregate.AutomationStatus != store.ProfileCommunicationAutomationActive {
			t.Fatalf("%s: 对账失败不得把档案转人工: aggregate=%+v err=%v",
				stage, aggregate, err)
		}
	}

	// 第一轮:链首动作在两次排空之间才冻结出来,只有后一次排空尝试对账,
	// 失败即降级。
	runCommunicationV4ScheduleRound(t, h, manager, "round-v4-schedule-degrade-one")
	assertPlannedUntouched("首轮", 1)
	// 第二轮:两次排空各尝试一次,仍失败,不累积状态、不转人工。
	runCommunicationV4ScheduleRound(t, h, manager, "round-v4-schedule-degrade-two")
	assertPlannedUntouched("次轮", 3)

	// 读取恢复后,同一动作在下一轮自愈发出,advice 不再重复调用。
	h.runner.handler = scheduleThreadEchoHandler(t, h, fixture.conversationRef)
	runCommunicationV4ScheduleRound(t, h, manager, "round-v4-schedule-degrade-heal")
	if hand.commandCount() != 1 || len(advice.requests) != 1 {
		t.Fatalf("对账恢复后链首必须自愈发出且不重复调用 AI: sends=%d advice=%d",
			hand.commandCount(), len(advice.requests))
	}
	actions, err := h.db.CommunicationV4EventActionsByProfile(fixture.profileID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != store.CommunicationV4EventActionSent ||
		actions[0].EffectIntentID == nil {
		t.Fatalf("自愈后链首没有走 WAL 正证轨: actions=%+v err=%v", actions, err)
	}
}
