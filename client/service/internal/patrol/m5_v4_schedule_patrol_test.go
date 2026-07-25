package patrol

import (
	"context"
	"fmt"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
)

func TestCommunicationV4SchedulePatrolSendsColdPromptThenWechatSequence(
	t *testing.T,
) {
	h := newHarness(t)
	h.clock.Add(scheduleTestBusinessNow().Sub(h.clock.Now()))
	h.clock.Add(-25 * time.Hour)
	fixture := seedCommunicationV4PatrolTargetWithBoundary(
		t,
		h,
		"schedule-cold-sequence",
		nil,
	)
	h.clock.Add(25 * time.Hour)

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
	hand := &m5PositiveHand{}
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
	if len(advice.requests) != 1 || hand.commandCount() != 3 {
		aggregate, _ := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
		t.Fatalf(
			"冷催二必须不调用 AI 且按正文→卡片各发一次: advice=%d sends=%d aggregate=%+v",
			len(advice.requests),
			hand.commandCount(),
			aggregate,
		)
	}
	actions, err = h.db.CommunicationV4EventActionsByProfile(fixture.profileID)
	if err != nil || len(actions) != 3 {
		t.Fatalf("冷催动作事实数量错误: actions=%+v err=%v", actions, err)
	}
	var coldText, coldInvite *store.CommunicationV4EventAction
	for index := range actions {
		switch actions[index].V4Kind {
		case communication.V4ActionColdWechatText:
			coldText = &actions[index]
		case communication.V4ActionColdWechatInvite:
			coldInvite = &actions[index]
		}
	}
	if coldText == nil || coldInvite == nil ||
		coldText.Status != store.CommunicationV4EventActionSent ||
		coldInvite.Status != store.CommunicationV4EventActionSent ||
		coldInvite.DependsOnActionID == nil ||
		*coldInvite.DependsOnActionID != coldText.ActionID ||
		coldText.EffectIntentID == nil ||
		coldInvite.EffectIntentID == nil {
		t.Fatalf(
			"冷催二没有按正文正证→卡片依赖收敛: text=%+v invite=%+v",
			coldText,
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
		if dispatchErr != nil || !stopped || hand.commandCount() != 3 {
			t.Fatalf(
				"缺父依赖的冷催卡不得进入派发: stopped=%v sends=%d err=%v",
				stopped,
				hand.commandCount(),
				dispatchErr,
			)
		}
	})
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

	hand := &m5PositiveHand{}
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
	wall := time.Now().UTC()
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
		if !crossAt.IsZero() && paceCalls == 2 {
			h.clock.Add(crossAt.Sub(h.clock.Now()))
		}
		return ctx.Err()
	}
	hand := &m5PositiveHand{}
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

func scheduleTestBusinessNow() time.Time {
	wall := time.Now().UTC()
	return time.Date(
		wall.Year(),
		wall.Month(),
		wall.Day(),
		10,
		0,
		0,
		0,
		time.UTC,
	)
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
