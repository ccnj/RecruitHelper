package patrol

import (
	"context"
	"errors"
	"strings"
	"testing"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// 巡检轮起手台面步骤(2026-09-08):手最近一次 ping 说本账号的沟通页不在,就先
// 派 nav.ensureSurface 再读列表,而不是让 readList 先撞一次 CTX_NOT_READY 再救场。
// 立案起因:BOSS 真机招呼转回复时 readList 报 pageAbsent、临走看一眼报"没有打开
// 的会话",两条预期内的失败在状态栏画成红行;智联开发账本同款——采集收口与
// 开工闸读之后的首轮 57/1223 轮走了救场。

func pageAbsentHint(h *harness) HandState {
	return HandState{
		Online: true, Session: "session-1", BootID: "boot-1",
		Contexts: []protocol.PingContext{{
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			Ready: false, Reason: protocol.NotReadyReasonPageAbsent,
		}},
	}
}

func indexOf(names []string, name string) int {
	for i, got := range names {
		if got == name {
			return i
		}
	}
	return -1
}

func TestRoundPreparesSurfaceBeforeFirstReadWhenHandReportsPageAbsent(t *testing.T) {
	h := newHarness(t)
	h.hands.set(pageAbsentHint(h))

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("Tick = %+v, %v", result, err)
	}
	names := h.runner.names()
	ensureAt := indexOf(names, protocol.PrimNavEnsureSurface)
	readListAt := indexOf(names, protocol.PrimChatReadList)
	if ensureAt < 0 || readListAt < 0 || ensureAt > readListAt {
		t.Fatalf("起手必须先保证台面再读列表: %v", names)
	}
	if h.runner.count(protocol.PrimNavEnsureSurface) != 1 || h.runner.count(protocol.PrimChatReadList) != 1 {
		t.Fatalf("台面保证一次、列表读一次即够,不该出现失败重读: %v", names)
	}
	round := result.Rounds[0]
	if !round.SurfacePrepared || round.EnsureUsed {
		t.Fatalf("起手台面不占救场预算: %+v", round)
	}
	if !strings.HasSuffix(round.Trigger, surfacePreparedSuffix) || strings.Contains(round.Trigger, surfaceRecoverySuffix) {
		t.Fatalf("trigger 应只带 surfacePrepared 标记: %q", round.Trigger)
	}
	rounds, err := h.db.RecentPatrolRounds(h.key, 1)
	if err != nil || len(rounds) != 1 || rounds[0].Trigger != round.Trigger || rounds[0].Status != "ok" {
		t.Fatalf("账本行应与 outcome 同一份 trigger: %+v err=%v", rounds, err)
	}
}

func TestRoundDoesNotPrepareSurfaceWithoutAbsentHint(t *testing.T) {
	cases := map[string]HandState{
		"提示缺席": {Online: true, Session: "session-1", BootID: "boot-1"},
		"页面就绪": {
			Online: true, Session: "session-1", BootID: "boot-1",
			Contexts: []protocol.PingContext{{Platform: "zhilian", AccountRef: "account-1", Ready: true}},
		},
		"别的账号不在": {
			Online: true, Session: "session-1", BootID: "boot-1",
			Contexts: []protocol.PingContext{{
				Platform: "zhilian", AccountRef: "account-other",
				Ready: false, Reason: protocol.NotReadyReasonPageAbsent,
			}},
		},
		"掉登录不是台面问题": {
			Online: true, Session: "session-1", BootID: "boot-1",
			Contexts: []protocol.PingContext{{
				Platform: "zhilian", AccountRef: "account-1",
				Ready: false, Reason: protocol.NotReadyReasonLoginRequired,
			}},
		},
	}
	for name, state := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.hands.set(state)
			result, err := h.manager.Tick(context.Background())
			if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
				t.Fatalf("Tick = %+v, %v", result, err)
			}
			if h.runner.count(protocol.PrimNavEnsureSurface) != 0 {
				t.Fatalf("没有明确的缺席提示不得动页面: %v", h.runner.names())
			}
			round := result.Rounds[0]
			if round.SurfacePrepared || strings.Contains(round.Trigger, surfacePreparedSuffix) {
				t.Fatalf("不该标记起手台面: %+v", round)
			}
		})
	}
}

func TestSurfacePrepareFailureFailsRoundWithoutTouchingIdentityOrRecovery(t *testing.T) {
	h := newHarness(t)
	h.hands.set(pageAbsentHint(h))
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimNavEnsureSurface:
			return protocol.NavEnsureSurfaceData{Ready: false, LoginState: protocol.LoginStateIn}, nil
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err == nil {
		t.Fatalf("Tick = %+v, %v", result, err)
	}
	round := result.Rounds[0]
	typed := runError(round.Err)
	if typed == nil || typed.Code != protocol.ErrCodeCtxNotReady || typed.Reason != protocol.NotReadyReasonPageBroken {
		t.Fatalf("台面没就绪应按 CTX_NOT_READY/pageBroken 收场: %v", round.Err)
	}
	if h.runner.count(protocol.PrimChatReadList) != 0 {
		t.Fatalf("台面没就绪不得读列表: %v", h.runner.names())
	}
	if round.SurfacePrepared || round.EnsureUsed {
		t.Fatalf("失败的起手不算已保证,也不占救场预算: %+v", round)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil || account.IdentityState != store.IdentityVerified || account.PausedReason != "" {
		t.Fatalf("起手台面失败不改身份、不暂停账号: %+v err=%v", account, err)
	}
}

func TestSurfacePrepareKeepsOneShotRecoveryForMidRoundLoss(t *testing.T) {
	h := newHarness(t)
	h.hands.set(pageAbsentHint(h))
	readListCalls := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			readListCalls++
			if readListCalls == 1 {
				return nil, &RunError{
					Code: protocol.ErrCodeCtxNotReady, Reason: protocol.NotReadyReasonPageAbsent,
					Cause: errors.New("page closed right after prepare"),
				}
			}
			return defaultHandler(request)
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("Tick = %+v, %v", result, err)
	}
	round := result.Rounds[0]
	if !round.SurfacePrepared || !round.EnsureUsed {
		t.Fatalf("起手保证与轮中救场应各记各的: %+v", round)
	}
	if h.runner.count(protocol.PrimNavEnsureSurface) != 2 || h.runner.count(protocol.PrimChatReadList) != 2 {
		t.Fatalf("起手一次、救场一次、列表读原次+重读一次: %v", h.runner.names())
	}
	if !strings.HasSuffix(round.Trigger, surfacePreparedSuffix+surfaceRecoverySuffix) {
		t.Fatalf("两个标记应按发生顺序都留在 trigger 上: %q", round.Trigger)
	}
}

func TestFreshSurfaceSkipsOnlyFirstLastLook(t *testing.T) {
	run := func(t *testing.T, state HandState) (identifies, threads int) {
		h := newHarness(t)
		h.hands.set(state)
		seedTracked(t, h, "fresh-a", "peer-fresh-a", []store.MessageDraft{draftText("old-a")})
		seedTracked(t, h, "fresh-b", "peer-fresh-b", []store.MessageDraft{draftText("old-b")})
		h.runner.handler = func(request RunRequest) (any, error) {
			switch request.Name {
			case protocol.PrimChatReadList:
				return protocol.ChatReadListData{
					Sessions: []protocol.ConversationSummary{
						summary("fresh-a", "peer-fresh-a", "new-a", 1),
						summary("fresh-b", "peer-fresh-b", "new-b", 1),
					},
					Complete: true,
				}, nil
			case protocol.PrimChatReadThread:
				args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
				suffix := strings.TrimPrefix(args.ConversationRef, "fresh-")
				return protocol.ChatReadThreadData{
					Messages: []protocol.ThreadMessage{
						threadText(0, "old-"+suffix), threadText(1, "new-"+suffix),
					},
					Complete: true, AnchorMatched: true,
				}, nil
			default:
				return defaultHandler(request)
			}
		}
		result, err := h.manager.Tick(context.Background())
		if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
			t.Fatalf("Tick = %+v, %v", result, err)
		}
		return h.runner.count(protocol.PrimChatIdentifyCurrentConversation),
			h.runner.count(protocol.PrimChatReadThread)
	}

	t.Run("起手保证台面后第一次切换不看", func(t *testing.T) {
		h := newHarness(t)
		identifies, threads := run(t, pageAbsentHint(h))
		if threads != 2 {
			t.Fatalf("两个脏行都应被处理: threads=%d", threads)
		}
		if identifies != 1 {
			t.Fatalf("刚导航出来的页面没有会话可看,只有第二次切换才看: identifies=%d", identifies)
		}
	})
	t.Run("页面本就在时每次切换都看", func(t *testing.T) {
		identifies, threads := run(t, HandState{Online: true, Session: "session-1", BootID: "boot-1"})
		if threads != 2 || identifies != 2 {
			t.Fatalf("既有行为不变: identifies=%d threads=%d", identifies, threads)
		}
	})
}

func TestSurfaceAbsentHintTable(t *testing.T) {
	cases := []struct {
		name   string
		state  HandState
		absent bool
		reason protocol.NotReadyReason
	}{
		{name: "nil", state: HandState{}, absent: false},
		{name: "pageAbsent", state: HandState{Contexts: []protocol.PingContext{
			{Platform: "boss", AccountRef: "a", Ready: false, Reason: protocol.NotReadyReasonPageAbsent},
		}}, absent: true, reason: protocol.NotReadyReasonPageAbsent},
		{name: "contentScriptDead", state: HandState{Contexts: []protocol.PingContext{
			{Platform: "boss", AccountRef: "a", Ready: false, Reason: protocol.NotReadyReasonContentScriptDead},
		}}, absent: true, reason: protocol.NotReadyReasonContentScriptDead},
		{name: "ready", state: HandState{Contexts: []protocol.PingContext{
			{Platform: "boss", AccountRef: "a", Ready: true},
		}}, absent: false},
		{name: "identityUnverified", state: HandState{Contexts: []protocol.PingContext{
			{Platform: "boss", AccountRef: "a", Ready: false, Reason: protocol.NotReadyReasonIdentityUnverified},
		}}, absent: false},
		{name: "samePlatformOtherAccount", state: HandState{Contexts: []protocol.PingContext{
			{Platform: "boss", AccountRef: "b", Ready: false, Reason: protocol.NotReadyReasonPageAbsent},
		}}, absent: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, absent := surfaceAbsentHint(tc.state, "boss", "a")
			if absent != tc.absent || reason != tc.reason {
				t.Fatalf("surfaceAbsentHint = %q,%v want %q,%v", reason, absent, tc.reason, tc.absent)
			}
		})
	}
}
