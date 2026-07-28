package patrol

import (
	"context"
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

func currentConversationThreadMessages(
	t *testing.T,
	h *harness,
	conversationRef string,
) []protocol.ThreadMessage {
	t.Helper()
	rows, err := h.db.MessagesForConversation(store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: conversationRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]protocol.ThreadMessage, len(rows))
	for index := range rows {
		row := rows[index]
		out[index] = protocol.ThreadMessage{
			Idx: index, Direction: protocol.MessageDirection(row.Direction),
			Kind: protocol.MessageKind(row.Kind), Text: row.Text, BlobRef: nil,
			ContentHash: row.ContentHash,
		}
		if row.SourceKey != nil {
			out[index].SourceKey = *row.SourceKey
		}
		if row.TsApproxMs != nil {
			value := *row.TsApproxMs
			out[index].TsApprox = &value
		}
	}
	return out
}

func projectCurrentConversationBoundary(
	t *testing.T,
	h *harness,
	fixture communicationV4PatrolFixture,
) {
	t.Helper()
	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: fixture.conversationRef,
	}
	message, err := h.db.MessageBySeq(key, fixture.inboundSeq)
	if err != nil || message == nil {
		t.Fatalf("读取待投影消息: message=%+v err=%v", message, err)
	}
	event, err := communication.NormalizeLedgerMessage(communication.LedgerMessageFact{
		Seq: message.Seq, Direction: message.Direction, Kind: message.Kind,
		Text: message.Text, CardType: message.CardType, CardState: message.CardState,
		Origin: message.Origin, TsApproxMs: message.TsApproxMs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.ApplyCommunicationV4BusinessEvent(
		store.ApplyCommunicationV4BusinessEventRequest{
			ProfileID: fixture.profileID, Event: event, AppliedAt: h.clock.Now(),
		},
	); err != nil {
		t.Fatal(err)
	}
}

func TestProcessCurrentConversationOnceBypassesOnlyQuietAndDoesNotAdvanceOtherProfile(
	t *testing.T,
) {
	h := newHarness(t)
	current := seedCommunicationV4PatrolTarget(t, h, "current-once", "当前候选人的消息")
	other := seedCommunicationV4PatrolTarget(t, h, "other-once", "其他候选人的消息")
	projectCurrentConversationBoundary(t, h, current)

	quietUntil := h.clock.Now().Add(time.Minute)
	if err := h.db.MutateAccount(h.key, func(account *store.Account) error {
		account.ManualQuietUntil = &quietUntil
		account.DirtyHint = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	beforeAccount, err := h.db.AccountByKey(h.key)
	if err != nil || beforeAccount == nil {
		t.Fatalf("读取运行前账号: account=%+v err=%v", beforeAccount, err)
	}
	beforeOther, err := h.db.CommunicationV4AggregateByProfile(other.profileID)
	if err != nil {
		t.Fatal(err)
	}
	thread := currentConversationThreadMessages(t, h, current.conversationRef)
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatIdentifyCurrentConversation:
			return protocol.ChatIdentifyCurrentConversationData{
				ConversationRef: current.conversationRef,
				ObservedAt:      h.clock.Now().UnixMilli(),
			}, nil
		case protocol.PrimChatReadThread:
			args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
			if !args.RequireCurrent {
				t.Fatal("显式单会话读取必须携带 requireCurrent=true")
			}
			if args.ConversationRef != current.conversationRef {
				t.Fatalf("读取了错误会话 %q", args.ConversationRef)
			}
			return protocol.ChatReadThreadData{
				Messages: thread, Peer: &protocol.PeerSummary{
					DisplayName: "脱敏候选人", PlatformUserRef: "person-v4-patrol-current-once",
				},
				Complete: true, ReachedTop: true,
			}, nil
		default:
			return nil, errors.New("单会话无新增消息时不应派发其他原语")
		}
	}

	outcome, err := h.manager.ProcessCurrentConversationOnce(context.Background(), h.key)
	if err != nil || outcome.Status != "ok" ||
		outcome.Trigger != TriggerCurrentConversation {
		t.Fatalf("单会话处理失败: outcome=%+v err=%v", outcome, err)
	}
	if got := h.runner.names(); len(got) != 2 ||
		got[0] != protocol.PrimChatIdentifyCurrentConversation ||
		got[1] != protocol.PrimChatReadThread {
		t.Fatalf("派发原语越界: %v", got)
	}
	afterOther, err := h.db.CommunicationV4AggregateByProfile(other.profileID)
	if err != nil {
		t.Fatal(err)
	}
	if afterOther.Revision != beforeOther.Revision ||
		afterOther.ProjectedThroughSeq != beforeOther.ProjectedThroughSeq {
		t.Fatalf("其他档案被推进: before=%+v after=%+v", beforeOther, afterOther)
	}
	afterAccount, err := h.db.AccountByKey(h.key)
	if err != nil || afterAccount == nil {
		t.Fatalf("读取运行后账号: account=%+v err=%v", afterAccount, err)
	}
	if afterAccount.ManualQuietUntil == nil ||
		!afterAccount.ManualQuietUntil.Equal(quietUntil) ||
		!afterAccount.DirtyHint {
		t.Fatalf("显式入口不得清除 quiet 或消费 dirty: %+v", afterAccount)
	}
	if (beforeAccount.NextPatrolAt == nil) != (afterAccount.NextPatrolAt == nil) ||
		(beforeAccount.NextPatrolAt != nil &&
			!beforeAccount.NextPatrolAt.Equal(*afterAccount.NextPatrolAt)) {
		t.Fatalf("显式入口改写了自动巡检时刻: before=%v after=%v",
			beforeAccount.NextPatrolAt, afterAccount.NextPatrolAt)
	}
}

func TestProcessCurrentConversationOnceCarriesQuietBypassIntoAutomaticWAL(
	t *testing.T,
) {
	h := newHarness(t)
	current := seedCommunicationV4PatrolTarget(
		t,
		h,
		"current-quiet-wal",
		"暂时不考虑，谢谢",
	)
	quietUntil := h.clock.Now().Add(time.Minute)
	if err := h.db.MutateAccount(h.key, func(account *store.Account) error {
		account.ManualQuietUntil = &quietUntil
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatIdentifyCurrentConversation:
			return protocol.ChatIdentifyCurrentConversationData{
				ConversationRef: current.conversationRef,
				ObservedAt:      h.clock.Now().UnixMilli(),
			}, nil
		case protocol.PrimChatReadThread:
			args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
			if !args.RequireCurrent ||
				args.ConversationRef != current.conversationRef {
				t.Fatalf("显式入口读取越界: %+v", args)
			}
			return protocol.ChatReadThreadData{
				Messages: currentConversationThreadMessages(
					t,
					h,
					current.conversationRef,
				),
				Peer: &protocol.PeerSummary{
					DisplayName:     "脱敏候选人",
					PlatformUserRef: "person-v4-patrol-current-quiet-wal",
				},
				Complete:   true,
				ReachedTop: true,
			}, nil
		default:
			return nil, errors.New("显式拒绝短路不应调用其他只读原语")
		}
	}
	hand := &m5PositiveHand{now: h.clock.Now}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5AutomaticReplyRunner{base: h.runner, dispatcher: dispatcher}
	manager, err := NewManager(h.db, runner, h.hands, h.config)
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := manager.ProcessCurrentConversationOnce(
		context.Background(),
		h.key,
	)
	if err != nil || outcome.Status != "ok" {
		t.Fatalf("显式拒绝短路未穿过事务静默窗: outcome=%+v err=%v", outcome, err)
	}
	if hand.commandCount() != 2 {
		t.Fatalf("同一显式处理轮必须发送挽留正文与 dependent 卡片: commands=%d", hand.commandCount())
	}
	turn, err := h.db.LatestDialogueTurnForProfile(current.profileID)
	if err != nil || turn == nil {
		t.Fatalf("读取拒绝轮失败: turn=%+v err=%v", turn, err)
	}
	action, err := h.db.CommunicationActionByTurn(turn.TurnID)
	if err != nil || action == nil ||
		action.Status != store.CommunicationActionSent ||
		action.EffectIntentID == nil ||
		action.FailureReason != "" {
		t.Fatalf("挽留正文未通过 WAL 正证收敛: action=%+v err=%v", action, err)
	}

	actions, err := h.db.CommunicationActionsByTurn(turn.TurnID)
	if err != nil || len(actions) != 2 ||
		actions[0].Status != store.CommunicationActionSent ||
		actions[1].Kind != store.CommunicationActionInviteWechat ||
		actions[1].Status != store.CommunicationActionSent ||
		actions[0].EffectIntentID == nil ||
		actions[1].EffectIntentID == nil ||
		*actions[0].EffectIntentID == *actions[1].EffectIntentID {
		t.Fatalf("拒绝正文→换微信卡未形成两条独立正证 WAL: actions=%+v err=%v",
			actions, err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil ||
		account.ManualQuietUntil == nil ||
		!account.ManualQuietUntil.Equal(quietUntil) {
		t.Fatalf("显式入口不得清除静默事实: account=%+v err=%v", account, err)
	}
}

func TestProcessCurrentConversationOnceFailsBeforeThreadForUntrackedOrActiveSourcing(
	t *testing.T,
) {
	t.Run("untracked", func(t *testing.T) {
		h := newHarness(t)
		h.runner.handler = func(request RunRequest) (any, error) {
			if request.Name != protocol.PrimChatIdentifyCurrentConversation {
				t.Fatalf("未跟踪会话不应继续派发 %s", request.Name)
			}
			return protocol.ChatIdentifyCurrentConversationData{
				ConversationRef: "conversation-untracked",
				ObservedAt:      h.clock.Now().UnixMilli(),
			}, nil
		}
		_, err := h.manager.ProcessCurrentConversationOnce(context.Background(), h.key)
		if !errors.Is(err, ErrCurrentConversationUntracked) {
			t.Fatalf("未跟踪会话错误=%v", err)
		}
		if got := h.runner.names(); len(got) != 1 ||
			got[0] != protocol.PrimChatIdentifyCurrentConversation {
			t.Fatalf("未跟踪分支派发越界: %v", got)
		}
	})

	t.Run("active sourcing", func(t *testing.T) {
		h := newHarness(t)
		seedActiveSourcingBatchForFeedInvalidation(t, h, "batch-current-once")
		_, err := h.manager.ProcessCurrentConversationOnce(context.Background(), h.key)
		if !errors.Is(err, ErrCurrentConversationSourcingActive) {
			t.Fatalf("活动采集分支错误=%v", err)
		}
		if got := h.runner.names(); len(got) != 0 {
			t.Fatalf("活动采集期间不应派发任何 IM 原语: %v", got)
		}
	})
}

func TestProcessCurrentConversationOnceNeverRecoversOrNavigatesSurface(t *testing.T) {
	t.Run("stale identity probe fails in place", func(t *testing.T) {
		h := newHarness(t)
		stale := h.clock.Now().Add(-2 * h.config.IdentityFreshFor)
		if err := h.db.MutateAccount(h.key, func(account *store.Account) error {
			account.IdentityVerifiedAt = &stale
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		h.runner.handler = func(request RunRequest) (any, error) {
			if request.Name != protocol.PrimProbePlatform {
				t.Fatalf("当前会话身份探针失败后不得自动恢复页面: %s", request.Name)
			}
			return nil, &RunError{
				Code: protocol.ErrCodeCtxNotReady, Reason: protocol.NotReadyReasonPageAbsent,
				Retryable: protocol.RetryableAfterRecovery, SideEffect: protocol.SideEffectNone,
				Cause: errors.New("page closed"),
			}
		}
		if _, err := h.manager.ProcessCurrentConversationOnce(context.Background(), h.key); err == nil {
			t.Fatal("页面缺失时当前会话入口必须原地失败")
		}
		if got := h.runner.names(); len(got) != 1 || got[0] != protocol.PrimProbePlatform {
			t.Fatalf("当前会话入口错误调用了页面恢复链: %v", got)
		}
	})

	t.Run("thread read fails in place", func(t *testing.T) {
		h := newHarness(t)
		current := seedCommunicationV4PatrolTarget(t, h, "current-no-recovery", "当前消息")
		h.runner.handler = func(request RunRequest) (any, error) {
			switch request.Name {
			case protocol.PrimChatIdentifyCurrentConversation:
				return protocol.ChatIdentifyCurrentConversationData{
					ConversationRef: current.conversationRef,
					ObservedAt:      h.clock.Now().UnixMilli(),
				}, nil
			case protocol.PrimChatReadThread:
				return nil, &RunError{
					Code: protocol.ErrCodeCtxNotReady, Reason: protocol.NotReadyReasonContentScriptDead,
					Retryable: protocol.RetryableAfterRecovery, SideEffect: protocol.SideEffectNone,
					Cause: errors.New("content script unavailable"),
				}
			default:
				t.Fatalf("当前会话读取失败后不得自动恢复页面: %s", request.Name)
				return nil, nil
			}
		}
		if _, err := h.manager.ProcessCurrentConversationOnce(context.Background(), h.key); err == nil {
			t.Fatal("当前会话读取失败必须原地停止")
		}
		if got := h.runner.names(); len(got) != 2 ||
			got[0] != protocol.PrimChatIdentifyCurrentConversation ||
			got[1] != protocol.PrimChatReadThread {
			t.Fatalf("当前会话入口错误调用了页面恢复链: %v", got)
		}
	})
}
