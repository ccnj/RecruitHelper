package patrol

import (
	"context"
	"errors"
	"testing"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// 列表巡检轮起手先保证沟通台面(2026-09-08 甲方裁决):每轮无条件派一条
// nav.ensureSurface,再读第一条命令。此前脑靠 readList 撞 CTX_NOT_READY 来感知
// 招呼转回复、开工闸读之后页面不在沟通页,预期内的失败在账本里与真故障同形。
// 采集轮与「处理当前会话」入口不经这里:前者已由 m6 用例钉住 ensure 计数为 0,
// 后者由 m5_current_conversation_test 的"不得派发其他原语"钉住。

func indexOf(names []string, name string) int {
	for i, got := range names {
		if got == name {
			return i
		}
	}
	return -1
}

func TestListRoundStartsWithEnsureSurfaceBeforeFirstRead(t *testing.T) {
	h := newHarness(t)

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("Tick = %+v, %v", result, err)
	}
	names := h.runner.names()
	ensureAt := indexOf(names, protocol.PrimNavEnsureSurface)
	firstRead := indexOf(names, protocol.PrimChatReadUnreadTotal)
	if firstRead < 0 {
		firstRead = indexOf(names, protocol.PrimChatReadList)
	}
	if ensureAt < 0 || firstRead < 0 || ensureAt > firstRead {
		t.Fatalf("起手必须先保证台面再读: %v", names)
	}
	if h.runner.count(protocol.PrimNavEnsureSurface) != 1 || h.runner.count(protocol.PrimChatReadList) != 1 {
		t.Fatalf("台面保证一次、列表读一次即够,不该出现失败重读: %v", names)
	}
	if result.Rounds[0].EnsureUsed {
		t.Fatalf("起手台面不占救场预算: %+v", result.Rounds[0])
	}
}

func TestEnsureSurfaceFailureAtRoundStartFailsRoundWithoutTouchingIdentityOrRecovery(t *testing.T) {
	h := newHarness(t)
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
	if h.runner.count(protocol.PrimChatReadList) != 0 || h.runner.count(protocol.PrimNavEnsureSurface) != 1 {
		t.Fatalf("台面没就绪不得读列表,也不该再救场: %v", h.runner.names())
	}
	if round.EnsureUsed {
		t.Fatalf("失败的起手不占救场预算: %+v", round)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil || account.IdentityState != store.IdentityVerified || account.PausedReason != "" {
		t.Fatalf("起手台面失败不改身份、不暂停账号: %+v err=%v", account, err)
	}
}

func TestEnsureSurfaceAtRoundStartKeepsOneShotRecoveryForMidRoundLoss(t *testing.T) {
	h := newHarness(t)
	readListCalls := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			readListCalls++
			if readListCalls == 1 {
				return nil, &RunError{
					Code: protocol.ErrCodeCtxNotReady, Reason: protocol.NotReadyReasonPageAbsent,
					Cause: errors.New("page closed right after the round started"),
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
	if !round.EnsureUsed {
		t.Fatalf("轮中丢页仍应走一次救场: %+v", round)
	}
	if h.runner.count(protocol.PrimNavEnsureSurface) != 2 || h.runner.count(protocol.PrimChatReadList) != 2 {
		t.Fatalf("起手一次、救场一次、列表读原次+重读一次: %v", h.runner.names())
	}
}
