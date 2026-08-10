package patrol

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

// setUnreadHintForTest 声明模拟页面的角标读数:chat.readUnreadTotal 现场读
// 会读到它。nil 模拟角标节点缺席(读不到)。
func setUnreadHintForTest(h *harness, unread *int) {
	h.runner.setUnreadBadge(unread)
}

func unreadListFilters(t *testing.T, h *harness) []protocol.ChatReadListArgs {
	t.Helper()
	h.runner.mu.Lock()
	defer h.runner.mu.Unlock()
	var out []protocol.ChatReadListArgs
	for _, call := range h.runner.calls {
		if call.Name != protocol.PrimChatReadList {
			continue
		}
		var args protocol.ChatReadListArgs
		if err := json.Unmarshal(call.Args, &args); err != nil {
			t.Fatal(err)
		}
		out = append(out, args)
	}
	return out
}

func requireUnreadRoundOK(t *testing.T, h *harness) TickResult {
	t.Helper()
	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("未读巡检失败: result=%+v err=%v", result, err)
	}
	return result
}

func TestUnreadPriorityStartsFromPositiveHintAndClosesWithAll(t *testing.T) {
	h := newHarness(t)
	setUnreadHintForTest(h, ptr(1))
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Filter == protocol.ListFilterUnread {
				setUnreadHintForTest(h, ptr(0))
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{}, Complete: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	requireUnreadRoundOK(t, h)
	got := unreadListFilters(t, h)
	// harness 时钟距交接日远超上界，all 面因此取 listStopOlderThanDaysMax；
	// 断言的是"all 带年龄截止、unread 不带"，不是某个具体天数。
	if len(got) != 2 ||
		got[0].Filter != protocol.ListFilterUnread ||
		got[0].StopOlderThanDays != 0 ||
		got[1].Filter != protocol.ListFilterAll ||
		got[1].StopOlderThanDays != listStopOlderThanDaysMax {
		t.Fatalf("未读→全部筛选或年龄参数错误: %+v", got)
	}
}

func TestUnreadPriorityDoesNotStartFromZeroOrUnknownHint(t *testing.T) {
	tests := []struct {
		name   string
		unread *int
	}{
		{name: "zero", unread: ptr(0)},
		{name: "unknown", unread: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			setUnreadHintForTest(h, test.unread)
			requireUnreadRoundOK(t, h)
			got := unreadListFilters(t, h)
			if len(got) != 1 ||
				got[0].Filter != protocol.ListFilterAll ||
				got[0].StopOlderThanDays != listStopOlderThanDaysMax {
				t.Fatalf("零/未知提示不应进入未读: %+v", got)
			}
		})
	}
}

func TestUnreadPriorityInterruptsAtOrdinaryCandidateBoundary(t *testing.T) {
	h := newHarness(t)
	setUnreadHintForTest(h, ptr(0))
	allReads := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			switch args.Filter {
			case protocol.ListFilterAll:
				allReads++
				if allReads == 1 {
					setUnreadHintForTest(h, ptr(1))
					return protocol.ChatReadListData{
						Sessions: []protocol.ConversationSummary{
							summary("ordinary-interrupt", "peer-ordinary-interrupt", "普通列表", 0),
						},
						Complete: true,
					}, nil
				}
			case protocol.ListFilterUnread:
				setUnreadHintForTest(h, ptr(0))
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{}, Complete: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	requireUnreadRoundOK(t, h)
	got := unreadListFilters(t, h)
	want := []protocol.ListFilter{
		protocol.ListFilterAll,
		protocol.ListFilterUnread,
		protocol.ListFilterAll,
	}
	var filters []protocol.ListFilter
	for _, args := range got {
		filters = append(filters, args.Filter)
	}
	if !reflect.DeepEqual(filters, want) {
		t.Fatalf("普通候选人边界未立即插入一次未读轮: got=%v want=%v", filters, want)
	}
	if h.runner.count(protocol.PrimChatOpenConversation) != 0 ||
		h.runner.count(protocol.PrimChatReadThread) != 0 {
		t.Fatalf("插队前不应处理普通候选人: %v", h.runner.names())
	}
}

func TestUnreadPriorityInterruptsAfterOrdinaryThreadAction(t *testing.T) {
	h := newHarness(t)
	setUnreadHintForTest(h, ptr(0))
	key := seedTracked(
		t,
		h,
		"ordinary-action-interrupt",
		"peer-ordinary-action-interrupt",
		[]store.MessageDraft{draftText("旧消息")},
	)
	allReads := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			switch args.Filter {
			case protocol.ListFilterAll:
				allReads++
				if allReads == 1 {
					return protocol.ChatReadListData{
						Sessions: []protocol.ConversationSummary{
							summary(key.ConversationRef, "peer-ordinary-action-interrupt", "新消息", 0),
							summary("ordinary-after-action", "peer-after-action", "普通", 0),
						},
						Complete: true,
					}, nil
				}
			case protocol.ListFilterUnread:
				setUnreadHintForTest(h, ptr(0))
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{}, Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			setUnreadHintForTest(h, ptr(1))
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{
					threadText(0, "旧消息"),
					threadText(1, "新消息"),
				},
				Peer: &protocol.PeerSummary{
					DisplayName:     "合成候选人",
					PlatformUserRef: "peer-ordinary-action-interrupt",
				},
				Complete: true, ReachedTop: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	requireUnreadRoundOK(t, h)
	got := unreadListFilters(t, h)
	want := []protocol.ListFilter{
		protocol.ListFilterAll,
		protocol.ListFilterUnread,
		protocol.ListFilterAll,
	}
	var filters []protocol.ListFilter
	for _, args := range got {
		filters = append(filters, args.Filter)
	}
	if !reflect.DeepEqual(filters, want) {
		t.Fatalf("普通详情动作收束后未插入未读轮: got=%v want=%v", filters, want)
	}
}

func TestUnreadPriorityUnknownConversationOpensAtMostOnceThenReturnsAll(t *testing.T) {
	h := newHarness(t)
	setUnreadHintForTest(h, ptr(1))
	unreadReads := 0
	target := summary("unread-residual", "peer-unread-residual", "旧未读", 1)
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Filter == protocol.ListFilterUnread {
				unreadReads++
				return protocol.ChatReadListData{
					Sessions: []protocol.ConversationSummary{target},
					Complete: true,
				}, nil
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{}, Complete: true,
			}, nil
		case protocol.PrimChatOpenConversation:
			args := decodeArgs[protocol.ChatOpenConversationArgs](t, request)
			return protocol.ChatOpenConversationData{
				ConversationRef: args.ConversationRef,
				ObservedAt:      h.clock.Now().UnixMilli(),
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	requireUnreadRoundOK(t, h)
	if unreadReads != 1 ||
		h.runner.count(protocol.PrimChatOpenConversation) != 1 ||
		h.runner.count(protocol.PrimChatReadThread) != 0 {
		t.Fatalf("残留气泡重复打开或走错路径: calls=%v unreadReads=%d", h.runner.names(), unreadReads)
	}
	got := unreadListFilters(t, h)
	if len(got) != 2 || got[1].Filter != protocol.ListFilterAll {
		t.Fatalf("残留气泡后没有实际切回全部列表: %+v", got)
	}
	audits, err := h.db.AuditEntries(100)
	if err != nil || countAudit(audits, unreadPatrolAuditCategory) == 0 {
		t.Fatalf("残留气泡未记录无 PII 子轮完成审计: audits=%+v err=%v", audits, err)
	}
}

func TestUnreadPriorityOverlappingWindowsOpenEachConversationOnce(t *testing.T) {
	h := newHarness(t)
	setUnreadHintForTest(h, ptr(3))
	first := summary("unread-window-a", "peer-unread-window-a", "未读一", 1)
	second := summary("unread-window-b", "peer-unread-window-b", "未读二", 1)
	third := summary("unread-window-c", "peer-unread-window-c", "未读三", 1)
	unreadReads := 0
	var opened []string
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Filter != protocol.ListFilterUnread {
				return protocol.ChatReadListData{
					Sessions: []protocol.ConversationSummary{},
					Complete: true,
				}, nil
			}
			unreadReads++
			if unreadReads == 1 {
				if args.Move != protocol.ListWindowMoveReset {
					t.Fatalf("未读子轮必须 reset 起步: %+v", args)
				}
				return protocol.ChatReadListData{
					Sessions: []protocol.ConversationSummary{first, second},
					Complete: false,
				}, nil
			}
			if args.Move != protocol.ListWindowMoveNext {
				t.Fatalf("未读重叠窗口必须 next 续扫: %+v", args)
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{second, third},
				Complete: true,
			}, nil
		case protocol.PrimChatOpenConversation:
			args := decodeArgs[protocol.ChatOpenConversationArgs](t, request)
			opened = append(opened, args.ConversationRef)
			return protocol.ChatOpenConversationData{
				ConversationRef: args.ConversationRef,
				ObservedAt:      h.clock.Now().UnixMilli(),
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	requireUnreadRoundOK(t, h)
	if want := []string{
		first.ConversationRef,
		second.ConversationRef,
		third.ConversationRef,
	}; !reflect.DeepEqual(opened, want) {
		t.Fatalf("重叠未读窗口重复或漏开会话: got=%v want=%v", opened, want)
	}
	got := unreadListFilters(t, h)
	if len(got) != 3 ||
		got[0].Move != protocol.ListWindowMoveReset ||
		got[1].Move != protocol.ListWindowMoveNext ||
		got[2].Filter != protocol.ListFilterAll ||
		got[2].Move != protocol.ListWindowMoveReset {
		t.Fatalf("未读窗口移动或切回 all 错误: %+v", got)
	}
}

func TestUnreadPriorityLocalOpenFailuresContinueToLaterRows(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "targetNotFound",
			err: &RunError{
				Code: protocol.ErrCodeTargetNotFound, Retryable: protocol.RetryableNo,
				SideEffect: protocol.SideEffectNone,
			},
		},
		{
			name: "postconditionUnconfirmed",
			err: &RunError{
				Code:      protocol.ErrCodePostconditionUnconfirmed,
				Retryable: protocol.RetryableManualOnly, SideEffect: protocol.SideEffectPossible,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			setUnreadHintForTest(h, ptr(2))
			first := summary("unread-local-a", "peer-unread-local-a", "旧未读一", 1)
			second := summary("unread-local-b", "peer-unread-local-b", "旧未读二", 1)
			unreadReads := 0
			openCalls := 0
			h.runner.handler = func(request RunRequest) (any, error) {
				switch request.Name {
				case protocol.PrimChatReadList:
					args := decodeArgs[protocol.ChatReadListArgs](t, request)
					if args.Filter == protocol.ListFilterUnread {
						unreadReads++
						if unreadReads <= 2 {
							return protocol.ChatReadListData{
								Sessions: []protocol.ConversationSummary{first, second},
								Complete: true,
							}, nil
						}
						setUnreadHintForTest(h, ptr(0))
					}
					return protocol.ChatReadListData{
						Sessions: []protocol.ConversationSummary{}, Complete: true,
					}, nil
				case protocol.PrimChatOpenConversation:
					openCalls++
					if openCalls == 1 {
						return nil, test.err
					}
					args := decodeArgs[protocol.ChatOpenConversationArgs](t, request)
					return protocol.ChatOpenConversationData{
						ConversationRef: args.ConversationRef,
						ObservedAt:      h.clock.Now().UnixMilli(),
					}, nil
				default:
					return defaultHandler(request)
				}
			}

			requireUnreadRoundOK(t, h)
			if unreadReads != 1 || openCalls != 2 {
				t.Fatalf("局部失败未在同一窗口继续后续未读: reads=%d calls=%v",
					unreadReads, h.runner.names())
			}
		})
	}
}

func TestUnreadPriorityAccountFailureStillStopsActor(t *testing.T) {
	h := newHarness(t)
	setUnreadHintForTest(h, ptr(1))
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary("unread-account-failure", "peer-unread-account-failure", "未读", 1),
				},
				Complete: true,
			}, nil
		case protocol.PrimChatOpenConversation:
			return nil, &RunError{
				Code: protocol.ErrCodeAccountMismatch, Retryable: protocol.RetryableManualOnly,
				SideEffect: protocol.SideEffectNone,
			}
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 ||
		!isRunError(result.Rounds[0].Err, protocol.ErrCodeAccountMismatch) {
		t.Fatalf("账号错误没有响亮停止: result=%+v err=%v", result, err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil || account.PausedReason != PauseAccountMismatch {
		t.Fatalf("账号错误没有暂停 actor: account=%+v err=%v", account, err)
	}
	if got := unreadListFilters(t, h); len(got) != 1 {
		t.Fatalf("账号错误后仍继续读列表: %+v", got)
	}
}

func TestUnreadPriorityTreatsOnlyUnreadListElementUnresolvedAsInconsistency(t *testing.T) {
	h := newHarness(t)
	setUnreadHintForTest(h, ptr(1))
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Filter == protocol.ListFilterUnread {
				return nil, &RunError{
					Code:      protocol.ErrCodeElementUnresolved,
					Retryable: protocol.RetryableManualOnly, SideEffect: protocol.SideEffectNone,
				}
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{}, Complete: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	requireUnreadRoundOK(t, h)
	got := unreadListFilters(t, h)
	if len(got) != 2 ||
		got[0].Filter != protocol.ListFilterUnread ||
		got[1].Filter != protocol.ListFilterAll {
		t.Fatalf("未读列表读取异常没有切回全部: %+v", got)
	}
}

func TestUnreadPrioritySharesMaxPagesAndReservesAllClose(t *testing.T) {
	h := newHarness(t)
	h.manager.config.MaxPages = 2
	setUnreadHintForTest(h, ptr(1))
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Filter == protocol.ListFilterUnread {
				return protocol.ChatReadListData{
					Sessions: []protocol.ConversationSummary{
						summary("unread-budget", "peer-unread-budget", "未读", 1),
					},
					Complete: true,
				}, nil
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{}, Complete: true,
			}, nil
		case protocol.PrimChatOpenConversation:
			args := decodeArgs[protocol.ChatOpenConversationArgs](t, request)
			return protocol.ChatOpenConversationData{
				ConversationRef: args.ConversationRef,
				ObservedAt:      h.clock.Now().UnixMilli(),
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	requireUnreadRoundOK(t, h)
	got := unreadListFilters(t, h)
	if len(got) != 2 ||
		got[0].Filter != protocol.ListFilterUnread ||
		got[1].Filter != protocol.ListFilterAll {
		t.Fatalf("MaxPages 未共享或没有预留关闭读取: %+v", got)
	}
}

// 判脏与全量轮共用后（2026-08-10 甲方裁决），指纹已核对且未变的未读行不再被
// 强制重读：固定打开已清掉角标，账本在上次核对时就是齐的，重读带不来新事实。
func TestUnreadPriorityVerifiedUnchangedHintOpensWithoutReread(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PatrolTarget(t, h, "unread-known", "已有入站")
	profile, err := h.db.CandidateProfileByID(fixture.profileID)
	if err != nil || profile == nil {
		t.Fatalf("读取 active 档案失败: profile=%+v err=%v", profile, err)
	}
	ledger, err := h.db.MessagesForConversation(store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: fixture.conversationRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	lastText := ""
	if ledger[len(ledger)-1].Text != nil {
		lastText = *ledger[len(ledger)-1].Text
	}
	current := summary(
		fixture.conversationRef,
		profile.PlatformUserRef,
		lastText,
		1,
	)
	seedVerifiedListHint(t, h, current)
	setUnreadHintForTest(h, ptr(1))
	unreadReads := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Filter == protocol.ListFilterUnread {
				unreadReads++
				if unreadReads == 1 {
					return protocol.ChatReadListData{
						Sessions: []protocol.ConversationSummary{current},
						Complete: true,
					}, nil
				}
				setUnreadHintForTest(h, ptr(0))
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{}, Complete: true,
			}, nil
		case protocol.PrimChatOpenConversation:
			args := decodeArgs[protocol.ChatOpenConversationArgs](t, request)
			return protocol.ChatOpenConversationData{
				ConversationRef: args.ConversationRef,
				ObservedAt:      h.clock.Now().UnixMilli(),
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	requireUnreadRoundOK(t, h)
	if h.runner.count(protocol.PrimChatOpenConversation) != 1 ||
		h.runner.count(protocol.PrimChatReadThread) != 0 {
		t.Fatalf("已核对未变的未读行应只打开不重读: %v", h.runner.names())
	}
}

// 未读轮与全量轮共用处理块后（2026-08-10 甲方裁决），manualRequired 档案的
// 会话照常打开并对账——新消息进账本供人工裁决——但自动化保持冻结，零发送。
func TestUnreadPriorityManualRequiredProfileReconcilesWithoutAutomation(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PatrolTarget(t, h, "unread-known-manual", "已有入站")
	if err := h.db.MarkCommunicationV4AutomationManualRequired(
		fixture.profileID,
		"unreadKnownManualFixture",
		h.clock.Now(),
	); err != nil {
		t.Fatalf("冻结已知档案自动化失败: %v", err)
	}
	profile, err := h.db.CandidateProfileByID(fixture.profileID)
	if err != nil || profile == nil {
		t.Fatalf("读取 manualRequired 档案失败: profile=%+v err=%v", profile, err)
	}
	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: fixture.conversationRef,
	}
	ledger, err := h.db.MessagesForConversation(key)
	if err != nil {
		t.Fatal(err)
	}
	newInbound := "冻结期间的新消息"
	thread := make([]protocol.ThreadMessage, 0, len(ledger)+1)
	for index := range ledger {
		thread = append(thread, protocolThreadMessageFromLedger(ledger[index], index))
	}
	thread = append(thread, threadText(len(ledger), newInbound))
	current := summary(
		fixture.conversationRef,
		profile.PlatformUserRef,
		newInbound,
		1,
	)
	setUnreadHintForTest(h, ptr(1))
	unreadReads := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Filter == protocol.ListFilterUnread {
				unreadReads++
				if unreadReads == 1 {
					return protocol.ChatReadListData{
						Sessions: []protocol.ConversationSummary{current},
						Complete: true,
					}, nil
				}
				setUnreadHintForTest(h, ptr(0))
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{}, Complete: true,
			}, nil
		case protocol.PrimChatOpenConversation:
			args := decodeArgs[protocol.ChatOpenConversationArgs](t, request)
			return protocol.ChatOpenConversationData{
				ConversationRef: args.ConversationRef,
				ObservedAt:      h.clock.Now().UnixMilli(),
			}, nil
		case protocol.PrimChatReadThread:
			return protocol.ChatReadThreadData{
				Messages: thread,
				Peer: &protocol.PeerSummary{
					DisplayName:     "合成候选人",
					PlatformUserRef: profile.PlatformUserRef,
				},
				Complete: true, ReachedTop: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	requireUnreadRoundOK(t, h)
	if h.runner.count(protocol.PrimChatOpenConversation) != 1 ||
		h.runner.count(protocol.PrimChatReadThread) != 1 {
		t.Fatalf("manualRequired 档案应打开并对账: %v", h.runner.names())
	}
	after, err := h.db.MessagesForConversation(key)
	if err != nil || len(after) != len(ledger)+1 ||
		after[len(after)-1].Text == nil || *after[len(after)-1].Text != newInbound ||
		after[len(after)-1].Direction != "in" {
		t.Fatalf("冻结档案的新消息没有进账本: before=%d after=%+v err=%v",
			len(ledger), after, err)
	}
	aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || aggregate == nil ||
		aggregate.AutomationStatus != store.ProfileCommunicationAutomationManualRequired {
		t.Fatalf("对账不得解冻自动化: aggregate=%+v err=%v", aggregate, err)
	}
	if h.runner.count(protocol.PrimChatSendMessage) != 0 {
		t.Fatalf("冻结档案不得产生发送: %v", h.runner.names())
	}
}

// 未读轮与全量轮共用处理块后（2026-08-10 甲方裁决），主动来聊的新候选人不再
// 只清角标等下一次全量：同一未读子轮内完成收编→补简历→建根→AI→首条回复。
func TestUnreadPassPromotesAdoptedInboundProfileToFirstReply(t *testing.T) {
	h := newHarness(t)
	savePatrolInboundLegacyJob(t, h, "job-inbound-unread", "客户 经理")

	conversationRef := "conversation-inbound-unread"
	platformUserRef := "peer-inbound-unread"
	positionTitle := " 客户\t经理 "
	sourceKey := strings.Repeat("b", 64)
	setUnreadHintForTest(h, ptr(1))
	unreadReads := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Filter == protocol.ListFilterUnread {
				unreadReads++
				if unreadReads == 1 {
					return protocol.ChatReadListData{
						Sessions: []protocol.ConversationSummary{{
							ConversationRef: conversationRef,
							Peer: protocol.PeerSummary{
								PlatformUserRef: platformUserRef,
								DisplayName:     "合成候选人",
							},
							PositionTitle:  &positionTitle,
							LastActivityTs: inboundListActivityMs(),
							UnreadCount:    1,
							LastMessage: protocol.LastMessageSummary{
								Direction:   protocol.MessageDirectionIn,
								Kind:        protocol.MessageKindText,
								TextPreview: "想了解一下",
							},
						}},
						Complete: true,
					}, nil
				}
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{}, Complete: true,
			}, nil
		case protocol.PrimChatOpenConversation:
			args := decodeArgs[protocol.ChatOpenConversationArgs](t, request)
			setUnreadHintForTest(h, ptr(0))
			return protocol.ChatOpenConversationData{
				ConversationRef: args.ConversationRef,
				ObservedAt:      h.clock.Now().UnixMilli(),
			}, nil
		case protocol.PrimChatReadThread:
			args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
			if args.ConversationRef != conversationRef {
				t.Fatalf("深读了错误会话: %+v", args)
			}
			text := "想了解一下"
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{{
					Idx: 0, Direction: protocol.MessageDirectionIn,
					Kind: protocol.MessageKindText, Text: &text,
					ContentHash: syncledger.HashText(text), SourceKey: sourceKey,
				}},
				Peer: &protocol.PeerSummary{
					PlatformUserRef: platformUserRef,
					DisplayName:     "合成候选人",
				},
				Complete: true, ReachedTop: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	advice := &recordingAdviceExecutor{}
	hand := &m5PositiveHand{now: h.clock.Now}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5InboundAutomaticRunner{m5AutomaticReplyRunner: &m5AutomaticReplyRunner{
		base: h.runner, dispatcher: dispatcher,
	}}
	manager, err := NewManager(h.db, runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("未读子轮主动来聊闭环失败: result=%+v err=%v", result, err)
	}
	if h.runner.count(protocol.PrimChatOpenConversation) != 1 ||
		h.runner.count(protocol.PrimChatReadThread) != 1 {
		t.Fatalf("未读子轮应固定打开并权威对账一次: calls=%v", h.runner.names())
	}
	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: conversationRef,
	}
	profile, err := h.db.CandidateProfileByConversation(key)
	if err != nil || profile == nil ||
		profile.MainStatus != store.CandidateProfileCommunicating ||
		profile.ResumeCaptureState != store.ResumeCaptureCaptured ||
		profile.ActiveResumeSnapshotID == nil {
		t.Fatalf("未读子轮没有完成来聊档案晋升: profile=%+v err=%v", profile, err)
	}
	messages, err := h.db.MessagesForConversation(key)
	if err != nil || len(messages) != 2 ||
		messages[0].Direction != "in" ||
		messages[1].Direction != "out" || messages[1].Origin != "self" {
		t.Fatalf("未读子轮没有完成首条回复: messages=%+v err=%v", messages, err)
	}
	root, err := h.db.CommunicationV4AggregateByProfile(profile.ProfileID)
	if err != nil || root == nil ||
		!store.IsInboundConversationV4Root(root.RootGreetingIntentID) ||
		root.ProjectedThroughSeq != 2 {
		t.Fatalf("未读子轮没有建根并投影首轮: root=%+v err=%v", root, err)
	}
	turn, err := h.db.LatestDialogueTurnForProfile(profile.ProfileID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnCompleted {
		t.Fatalf("未读子轮首轮未完成: turn=%+v err=%v", turn, err)
	}
	action, err := h.db.CommunicationActionByTurn(turn.TurnID)
	if err != nil || action == nil ||
		action.Status != store.CommunicationActionSent ||
		action.EffectIntentID == nil {
		t.Fatalf("未读子轮回复未以 WAL 正证完成: action=%+v err=%v", action, err)
	}
	if len(advice.requests) != 2 ||
		advice.requests[0].Purpose != m5ai.PurposeIntent ||
		advice.requests[1].Purpose != m5ai.PurposeReply {
		t.Fatalf("未读子轮调用链不完整: advice=%+v", advice.requests)
	}
	filters := unreadListFilters(t, h)
	if len(filters) < 2 ||
		filters[0].Filter != protocol.ListFilterUnread ||
		filters[len(filters)-1].Filter != protocol.ListFilterAll {
		t.Fatalf("未读子轮没有以全量读收口: %+v", filters)
	}
	assertInboundAdoptionAudit(t, h, conversationRef, "status=adopted")
	assertInboundAdoptionAudit(t, h, conversationRef, "status=rooted")
}

// 2026-07-27 甲方裁决：未读清理 open 的 manualOnly 失败只隔离该会话，
// 不再暂停整个账号；轮正常收尾且下一轮不再自动重开。
func TestUnreadPriorityElementUnresolvedFromOpenQuarantinesConversation(t *testing.T) {
	h := newHarness(t)
	setUnreadHintForTest(h, ptr(1))
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary("unread-open-unresolved", "peer-unread-open-unresolved", "未读", 1),
				},
				Complete: true,
			}, nil
		case protocol.PrimChatOpenConversation:
			return nil, &RunError{
				Code:      protocol.ErrCodeElementUnresolved,
				Retryable: protocol.RetryableManualOnly, SideEffect: protocol.SideEffectNone,
			}
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil ||
		result.Rounds[0].Status != "ok" {
		t.Fatalf("open 的 manualOnly 只隔离当事人，轮必须正常收尾: result=%+v err=%v", result, err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil || account.PausedReason != "" || account.StoppedAt != nil {
		t.Fatalf("open 的 manualOnly 不得再暂停账号: account=%+v err=%v", account, err)
	}
	conversation, err := h.db.ConversationByKey(store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: "unread-open-unresolved",
	})
	if err != nil || conversation == nil || conversation.PatrolQuarantinedAt == nil ||
		conversation.PatrolQuarantineReason != "patrolQuarantine:hand:ELEMENT_UNRESOLVED" {
		t.Fatalf("open 失败必须隔离该会话: conversation=%+v err=%v", conversation, err)
	}
	openCount := h.runner.count(protocol.PrimChatOpenConversation)
	h.clock.Add(h.config.PatrolInterval + time.Minute)
	next, err := h.manager.Tick(context.Background())
	if err != nil || len(next.Rounds) != 1 ||
		h.runner.count(protocol.PrimChatOpenConversation) != openCount {
		t.Fatalf("被隔离会话不得自动重开: next=%+v err=%v calls=%v", next, err, h.runner.names())
	}
}

func TestUnreadPriorityGenerationChangeStops(t *testing.T) {
	h := newHarness(t)
	setUnreadHintForTest(h, ptr(0))
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadList {
			h.hands.set(HandState{
				Online: true, Session: "replacement-session", BootID: "replacement-boot",
			})
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary("ordinary-generation-change", "peer-generation-change", "普通", 0),
				},
				Complete: true,
			}, nil
		}
		return defaultHandler(request)
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 ||
		!errors.Is(result.Rounds[0].Err, ErrActorGenerationChanged) {
		t.Fatalf("手代际变化没有停止当前轮: result=%+v err=%v", result, err)
	}
}

// 角标一直停在 3、打开也不清行的病态残留:同一轮内该行只开一次(指纹认领),
// 紧随其后的白跑把轮内重入封死(白跑记号),不会每个边界都钻一趟。下一轮记号
// 归零,固定打开再来一次——按 2026-08-10 设计,残留行的代价上限是每轮一开。
func TestUnreadRetainedRowFruitlessPassSealsReentry(t *testing.T) {
	h := newHarness(t)
	setUnreadHintForTest(h, ptr(3))
	retained := summary("unread-retained", "peer-unread-retained", "旧未读", 1)
	ordinary := []protocol.ConversationSummary{
		summary("ordinary-after-unread-a", "peer-after-unread-a", "普通一", 0),
		summary("ordinary-after-unread-b", "peer-after-unread-b", "普通二", 0),
		summary("ordinary-after-unread-c", "peer-after-unread-c", "普通三", 0),
	}
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Filter == protocol.ListFilterUnread {
				return protocol.ChatReadListData{
					Sessions: []protocol.ConversationSummary{retained},
					Complete: true,
				}, nil
			}
			return protocol.ChatReadListData{
				Sessions: ordinary, Complete: true,
			}, nil
		case protocol.PrimChatOpenConversation:
			args := decodeArgs[protocol.ChatOpenConversationArgs](t, request)
			return protocol.ChatOpenConversationData{
				ConversationRef: args.ConversationRef,
				ObservedAt:      h.clock.Now().UnixMilli(),
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	requireUnreadRoundOK(t, h)
	if h.runner.count(protocol.PrimChatOpenConversation) != 1 {
		t.Fatalf("保留行在同一轮被重复打开: %v", h.runner.names())
	}
	first := unreadListFilters(t, h)
	// 轮首有认领的一趟 + 首个边界的白跑一趟 = 恰好两次未读读取;白跑记号
	// 生效后,其余边界(普通行还剩两个)不再产生第三趟。
	if countListFilter(first, protocol.ListFilterUnread) != 2 ||
		first[len(first)-1].Filter != protocol.ListFilterAll {
		t.Fatalf("白跑一趟后没有封死轮内重入: %+v", first)
	}

	h.clock.Add(h.config.PatrolInterval)
	before := len(first)
	requireUnreadRoundOK(t, h)
	after := unreadListFilters(t, h)
	if countListFilter(after[before:], protocol.ListFilterUnread) != 2 {
		t.Fatalf("下一轮应重新一开一封: %+v", after[before:])
	}
	if h.runner.count(protocol.PrimChatOpenConversation) != 2 {
		t.Fatalf("残留行的固定打开应每轮至多一次: %v", h.runner.names())
	}
}

// 连环回复同轮再插队(2026-08-10 甲方裁决的核心诉求):同一候选人第二次回复
// 使行指纹变化,"已试过"不再挡道,下一个边界照常再进再处理。
func TestUnreadRepeatReplySameRoundIsPreemptedAgain(t *testing.T) {
	h := newHarness(t)
	seedTracked(t, h, "unread-repeat", "peer-unread-repeat", []store.MessageDraft{
		draftText("老话"),
	})
	setUnreadHintForTest(h, ptr(1))
	unreadReads := 0
	ordinary := summary("ordinary-idle", "peer-ordinary-idle", "闲聊", 0)
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Filter == protocol.ListFilterUnread {
				unreadReads++
				preview := "回复一"
				if unreadReads >= 2 {
					preview = "回复二"
				}
				return protocol.ChatReadListData{
					Sessions: []protocol.ConversationSummary{
						summary("unread-repeat", "peer-unread-repeat", preview, 1),
					},
					Complete: true,
				}, nil
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{ordinary}, Complete: true,
			}, nil
		case protocol.PrimChatOpenConversation:
			args := decodeArgs[protocol.ChatOpenConversationArgs](t, request)
			return protocol.ChatOpenConversationData{
				ConversationRef: args.ConversationRef, ObservedAt: h.clock.Now().UnixMilli(),
			}, nil
		case protocol.PrimChatReadThread:
			messages := []protocol.ThreadMessage{threadText(0, "老话"), threadText(1, "回复一")}
			if unreadReads >= 2 {
				messages = append(messages, threadText(2, "回复二"))
			}
			return protocol.ChatReadThreadData{
				Messages: messages, Complete: true, ReachedTop: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	requireUnreadRoundOK(t, h)
	if got := h.runner.count(protocol.PrimChatOpenConversation); got != 2 {
		t.Fatalf("第二次回复没有在同轮再次插队处理: opens=%d calls=%v", got, h.runner.names())
	}
	messages, err := h.db.MessagesForConversation(store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef, ConversationRef: "unread-repeat",
	})
	if err != nil || len(messages) != 3 {
		t.Fatalf("两次回复没有都进账本: n=%d err=%v", len(messages), err)
	}
}

// 读数命令失败(含旧插件不认识该原语的场景)一律"不进",全量轮照常兜底,
// 轮不失败——插队失灵的方向只许是慢,不许是停。
func TestUnreadBadgeReadFailureFallsBackToAllPass(t *testing.T) {
	h := newHarness(t)
	h.runner.mu.Lock()
	h.runner.unreadBadgeServed = false
	h.runner.mu.Unlock()
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadUnreadTotal {
			return nil, &RunError{
				Code: protocol.ErrCodeInternalHand, Retryable: protocol.RetryableYes,
				SideEffect: protocol.SideEffectNone, Cause: errors.New("旧插件不认识该原语"),
			}
		}
		return defaultHandler(request)
	}
	requireUnreadRoundOK(t, h)
	got := unreadListFilters(t, h)
	if len(got) != 1 || got[0].Filter != protocol.ListFilterAll {
		t.Fatalf("读数失败应退化为纯全量轮: %+v", got)
	}
}

// 角标读是本轮第一条命令:它带回的掉登录信号必须与 readList 失败同款当轮
// 暂停账号并置身份失效,不能只失败轮、等 probe 到期才暂停。
func TestUnreadBadgeReadLoginRequiredPausesAccount(t *testing.T) {
	h := newHarness(t)
	h.runner.mu.Lock()
	h.runner.unreadBadgeServed = false
	h.runner.mu.Unlock()
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadUnreadTotal {
			return nil, &RunError{
				Code: protocol.ErrCodeCtxNotReady, Reason: protocol.NotReadyReasonLoginRequired,
				Retryable: protocol.RetryableManualOnly, SideEffect: protocol.SideEffectNone,
				Cause: errors.New("智联账号已退出登录"),
			}
		}
		return defaultHandler(request)
	}
	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err == nil {
		t.Fatalf("掉登录应失败本轮: result=%+v err=%v", result, err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil || account.StoppedAt == nil ||
		account.PausedReason != PauseLoginRequired ||
		account.IdentityState != store.IdentityInvalid {
		t.Fatalf("掉登录未当轮暂停并置身份失效: account=%+v err=%v", account, err)
	}
}

func TestUnreadChangedTotalCanReenterWithinSameActor(t *testing.T) {
	h := newHarness(t)
	setUnreadHintForTest(h, ptr(1))
	allReads := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name != protocol.PrimChatReadList {
			return defaultHandler(request)
		}
		args := decodeArgs[protocol.ChatReadListArgs](t, request)
		if args.Filter == protocol.ListFilterUnread {
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{}, Complete: true,
			}, nil
		}
		allReads++
		if allReads == 1 {
			setUnreadHintForTest(h, ptr(2))
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary("ordinary-before-second-unread", "peer-before-second-unread", "普通", 0),
				},
				Complete: true,
			}, nil
		}
		return protocol.ChatReadListData{
			Sessions: []protocol.ConversationSummary{}, Complete: true,
		}, nil
	}

	requireUnreadRoundOK(t, h)
	got := unreadListFilters(t, h)
	want := []protocol.ListFilter{
		protocol.ListFilterUnread,
		protocol.ListFilterAll,
		protocol.ListFilterUnread,
		protocol.ListFilterAll,
	}
	filters := make([]protocol.ListFilter, 0, len(got))
	for _, args := range got {
		filters = append(filters, args.Filter)
	}
	if !reflect.DeepEqual(filters, want) {
		t.Fatalf("气泡数 1→2 后没有在下一会话边界再次插队: got=%v want=%v", filters, want)
	}
}

func TestUnreadReturnToAllRebuildsCheckedAndUsesFingerprint(t *testing.T) {
	h := newHarness(t)
	key := seedTracked(
		t,
		h,
		"unread-return-all",
		"peer-unread-return-all",
		[]store.MessageDraft{draftText("旧消息")},
	)
	setUnreadHintForTest(h, ptr(0))
	allReads := 0
	threadReads := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Filter == protocol.ListFilterUnread {
				return protocol.ChatReadListData{
					Sessions: []protocol.ConversationSummary{}, Complete: true,
				}, nil
			}
			allReads++
			switch allReads {
			case 1:
				return protocol.ChatReadListData{
					Sessions: []protocol.ConversationSummary{
						summary(key.ConversationRef, "peer-unread-return-all", "新消息一", 0),
						summary("ordinary-unread-boundary", "peer-unread-boundary", "普通", 0),
					},
					Complete: true,
				}, nil
			case 2:
				return protocol.ChatReadListData{
					Sessions: []protocol.ConversationSummary{
						summary(key.ConversationRef, "peer-unread-return-all", "新消息二", 0),
					},
					Complete: true,
				}, nil
			default:
				return protocol.ChatReadListData{
					Sessions: []protocol.ConversationSummary{}, Complete: true,
				}, nil
			}
		case protocol.PrimChatReadThread:
			threadReads++
			messages := []protocol.ThreadMessage{
				threadText(0, "旧消息"),
				threadText(1, "新消息一"),
			}
			if threadReads == 1 {
				// The first ordinary conversation completes before unread is
				// allowed to preempt.
				setUnreadHintForTest(h, ptr(1))
			} else {
				messages = append(messages, threadText(2, "新消息二"))
			}
			return protocol.ChatReadThreadData{
				Messages: messages,
				Peer: &protocol.PeerSummary{
					DisplayName:     "合成候选人",
					PlatformUserRef: "peer-unread-return-all",
				},
				Complete: true, ReachedTop: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	requireUnreadRoundOK(t, h)
	if threadReads != 2 {
		t.Fatalf("返回 all 后旧 checked 压掉了已变化会话: readThread=%d calls=%v",
			threadReads, h.runner.names())
	}
	got := unreadListFilters(t, h)
	want := []protocol.ListFilter{
		protocol.ListFilterAll,
		protocol.ListFilterUnread,
		protocol.ListFilterAll,
	}
	filters := make([]protocol.ListFilter, 0, len(got))
	for _, args := range got {
		filters = append(filters, args.Filter)
	}
	if !reflect.DeepEqual(filters, want) {
		t.Fatalf("普通→未读→新普通扫描顺序错误: got=%v want=%v", filters, want)
	}
}

func TestUnreadIncompletePassDefersWithinActorAndRetriesNextOrdinaryRound(t *testing.T) {
	h := newHarness(t)
	setUnreadHintForTest(h, ptr(2))
	failFirstUnread := true
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name != protocol.PrimChatReadList {
			return defaultHandler(request)
		}
		args := decodeArgs[protocol.ChatReadListArgs](t, request)
		if args.Filter == protocol.ListFilterUnread && failFirstUnread {
			failFirstUnread = false
			return nil, &RunError{
				Code:       protocol.ErrCodeElementUnresolved,
				Retryable:  protocol.RetryableManualOnly,
				SideEffect: protocol.SideEffectNone,
			}
		}
		return protocol.ChatReadListData{
			Sessions: []protocol.ConversationSummary{}, Complete: true,
		}, nil
	}

	requireUnreadRoundOK(t, h)
	first := unreadListFilters(t, h)
	if got := countListFilter(first, protocol.ListFilterUnread); got != 1 {
		t.Fatalf("失败现场在同一 actor 内重复进入未读: filters=%+v", first)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil || account.LastPatrolAt == nil ||
		account.NextPatrolAt == nil {
		t.Fatalf("读取失败后调度状态: account=%+v err=%v", account, err)
	}
	if !account.NextPatrolAt.Equal(account.LastPatrolAt.Add(h.config.PatrolInterval)) {
		t.Fatalf("未完成子轮被旧 pending 拉成紧循环: last=%v next=%v",
			account.LastPatrolAt, account.NextPatrolAt)
	}

	h.clock.Add(h.config.PatrolInterval)
	before := len(first)
	requireUnreadRoundOK(t, h)
	after := unreadListFilters(t, h)
	if len(after) <= before ||
		after[before].Filter != protocol.ListFilterUnread {
		t.Fatalf("下一普通巡检没有重试未完成子轮: %+v", after[before:])
	}
}

func TestUnreadUnknownEndTotalDoesNotRecordBaseline(t *testing.T) {
	h := newHarness(t)
	setUnreadHintForTest(h, ptr(2))
	firstUnread := true
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name != protocol.PrimChatReadList {
			return defaultHandler(request)
		}
		args := decodeArgs[protocol.ChatReadListArgs](t, request)
		if args.Filter == protocol.ListFilterUnread && firstUnread {
			firstUnread = false
			setUnreadHintForTest(h, nil)
		}
		return protocol.ChatReadListData{
			Sessions: []protocol.ConversationSummary{}, Complete: true,
		}, nil
	}

	requireUnreadRoundOK(t, h)
	first := unreadListFilters(t, h)
	setUnreadHintForTest(h, ptr(2))
	h.clock.Add(h.config.PatrolInterval)
	before := len(first)
	requireUnreadRoundOK(t, h)
	after := unreadListFilters(t, h)
	if len(after) <= before ||
		after[before].Filter != protocol.ListFilterUnread {
		t.Fatalf("缺失结束读数错误伪造了同值基线: %+v", after[before:])
	}
}

func TestUnreadFirstPositiveEventPullsForwardAndStartsUnreadPass(t *testing.T) {
	h := newHarness(t)
	setUnreadHintForTest(h, nil)
	requireUnreadRoundOK(t, h)

	prevFilters := len(unreadListFilters(t, h))
	if err := h.manager.HandleEvent(
		"hand-1",
		eventBody(t, h, protocol.EventUnreadBadge, protocol.UnreadBadgeEventData{
			Prev: nil, Scope: protocol.UnreadScopeTotal, Stable: true, Value: 1,
		}),
	); err != nil {
		t.Fatalf("首帧正数事件失败: %v", err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil || account.LastPatrolAt == nil ||
		account.NextPatrolAt == nil {
		t.Fatalf("读取事件后调度状态失败: account=%+v err=%v", account, err)
	}
	wantNext := account.LastPatrolAt.Add(h.config.MinimumRoundGap)
	if !account.NextPatrolAt.Equal(wantNext) {
		t.Fatalf("首帧正数没有拉前到最小轮间隔: got=%v want=%v", account.NextPatrolAt, wantNext)
	}

	setUnreadHintForTest(h, ptr(1))
	h.clock.Add(h.config.MinimumRoundGap)
	requireUnreadRoundOK(t, h)
	after := unreadListFilters(t, h)
	if len(after) <= prevFilters ||
		after[prevFilters].Filter != protocol.ListFilterUnread {
		t.Fatalf("首帧正数没有触发首次未读插队: %+v", after[prevFilters:])
	}

	account, err = h.db.AccountByKey(h.key)
	if err != nil || account == nil || account.NextPatrolAt == nil {
		t.Fatalf("读取首次插队后调度状态失败: account=%+v err=%v", account, err)
	}
	regularNext := *account.NextPatrolAt
	prev := 1
	if err := h.manager.HandleEvent(
		"hand-1",
		eventBody(t, h, protocol.EventUnreadBadge, protocol.UnreadBadgeEventData{
			Prev: &prev, Scope: protocol.UnreadScopeTotal, Stable: true, Value: 0,
		}),
	); err != nil {
		t.Fatalf("零值事件失败: %v", err)
	}
	account, err = h.db.AccountByKey(h.key)
	if err != nil || account == nil || account.NextPatrolAt == nil {
		t.Fatalf("读取零值事件后状态失败: account=%+v err=%v", account, err)
	}
	if !account.NextPatrolAt.Equal(regularNext) {
		t.Fatalf("零值事件不该拉前: got=%v want=%v", account.NextPatrolAt, regularNext)
	}

	// 任何正数事件都拉前(2026-08-10):插队判定改由现场读命令承担,事件不再
	// 与基线比对,同值正数照样是"有动静"的提示。
	prev = 0
	if err := h.manager.HandleEvent(
		"hand-1",
		eventBody(t, h, protocol.EventUnreadBadge, protocol.UnreadBadgeEventData{
			Prev: &prev, Scope: protocol.UnreadScopeTotal, Stable: true, Value: 1,
		}),
	); err != nil {
		t.Fatalf("正数事件失败: %v", err)
	}
	account, err = h.db.AccountByKey(h.key)
	if err != nil || account == nil || account.LastPatrolAt == nil ||
		account.NextPatrolAt == nil {
		t.Fatalf("读取重新拉前状态失败: account=%+v err=%v", account, err)
	}
	wantNext = account.LastPatrolAt.Add(h.config.MinimumRoundGap)
	if !account.NextPatrolAt.Equal(wantNext) {
		t.Fatalf("正数事件没有重新拉前: got=%v want=%v",
			account.NextPatrolAt, wantNext)
	}
}

// 隔离检查必须排在固定打开之前（2026-07-27 甲方裁决：被隔离会话人工解除前
// 不得自动打开）；同批其余未读行照常打开处理。
func TestUnreadQuarantinedRowIsNotOpenedButOthersAre(t *testing.T) {
	h := newHarness(t)
	key := seedTracked(
		t,
		h,
		"unread-quarantined",
		"peer-unread-quarantined",
		[]store.MessageDraft{draftText("旧消息")},
	)
	if _, err := h.db.QuarantineConversationPatrol(
		key, "patrolQuarantine:test", h.clock.Now(),
	); err != nil {
		t.Fatalf("预置隔离失败: %v", err)
	}
	setUnreadHintForTest(h, ptr(2))
	quarantined := summary(key.ConversationRef, "peer-unread-quarantined", "新未读", 1)
	fresh := summary("unread-fresh", "peer-unread-fresh", "另一条未读", 1)
	unreadReads := 0
	var opened []string
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Filter == protocol.ListFilterUnread {
				unreadReads++
				if unreadReads == 1 {
					return protocol.ChatReadListData{
						Sessions: []protocol.ConversationSummary{quarantined, fresh},
						Complete: true,
					}, nil
				}
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{}, Complete: true,
			}, nil
		case protocol.PrimChatOpenConversation:
			args := decodeArgs[protocol.ChatOpenConversationArgs](t, request)
			opened = append(opened, args.ConversationRef)
			return protocol.ChatOpenConversationData{
				ConversationRef: args.ConversationRef,
				ObservedAt:      h.clock.Now().UnixMilli(),
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	requireUnreadRoundOK(t, h)
	if !reflect.DeepEqual(opened, []string{"unread-fresh"}) {
		t.Fatalf("隔离会话被打开或同批他人被拦: opened=%v", opened)
	}
}

// 工作流候选人闸拒绝时未读轮明确停止，不得先打开会话再停（打开也是平台可见
// 动作，停止边界后的任何领取都是多做方向）。
func TestUnreadWorkflowGateStopsBeforeOpen(t *testing.T) {
	h := newHarness(t)
	h.manager.SetWorkflowConversationGate(func() (bool, error) {
		return false, nil
	})
	setUnreadHintForTest(h, ptr(1))
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Filter == protocol.ListFilterUnread {
				return protocol.ChatReadListData{
					Sessions: []protocol.ConversationSummary{
						summary("unread-gated", "peer-unread-gated", "未读", 1),
					},
					Complete: true,
				}, nil
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{}, Complete: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	requireUnreadRoundOK(t, h)
	if h.runner.count(protocol.PrimChatOpenConversation) != 0 ||
		h.runner.count(protocol.PrimChatReadThread) != 0 {
		t.Fatalf("停止边界后仍打开或处理了未读会话: %v", h.runner.names())
	}
}

// 分类修正在未读轮就地停止（2026-08-10）：修正已停账号，"切回全量关筛选"的
// 收口读会被派发门禁拒绝，只会产出假失败轮。页面残留的未读筛选无跨轮毒性：
// 每次列表读都显式携带筛选与 reset 参数。
func TestUnreadClassificationCorrectionStopsInPlace(t *testing.T) {
	h := newHarness(t)
	fixture := seedM5AdviceFixture(t, h)
	legacyText := "我暂时不考虑，祝你早日找到合适的人"
	timestamp := h.clock.Now().UnixMilli()
	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: fixture.conversationRef,
	}
	before, err := h.db.MessagesForConversation(key)
	if err != nil || len(before) != 2 {
		t.Fatalf("M5 fixture 活动账本错误: messages=%+v err=%v", before, err)
	}
	if _, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: before[len(before)-1].Seq,
		NewMessages: []store.MessageDraft{{
			Direction: "system", Kind: "system", ContentHash: syncledger.HashText(legacyText),
			Text: &legacyText, TsApproxMs: &timestamp, Origin: "external",
		}},
		SyncedAt: h.clock.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	ledger, err := h.db.MessagesForConversation(key)
	if err != nil || len(ledger) != 3 {
		t.Fatalf("无法预置旧 system 尾: messages=%+v err=%v", ledger, err)
	}
	thread := make([]protocol.ThreadMessage, len(ledger))
	for index := range ledger {
		thread[index] = protocolThreadMessageFromLedger(ledger[index], index)
	}
	correctedKey := strings.Repeat("5", 64)
	thread[len(thread)-1].Direction = protocol.MessageDirectionIn
	thread[len(thread)-1].Kind = protocol.MessageKindText
	thread[len(thread)-1].SourceKey = correctedKey
	conversation, err := h.db.ConversationByKey(key)
	if err != nil {
		t.Fatal(err)
	}
	setUnreadHintForTest(h, ptr(1))
	h.manager.advice = &recordingAdviceExecutor{}
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Filter == protocol.ListFilterUnread {
				return protocol.ChatReadListData{
					Sessions: []protocol.ConversationSummary{
						summary(key.ConversationRef, conversation.PlatformUserRef, legacyText, 1),
					},
					Complete: true,
				}, nil
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{}, Complete: true,
			}, nil
		case protocol.PrimChatOpenConversation:
			args := decodeArgs[protocol.ChatOpenConversationArgs](t, request)
			return protocol.ChatOpenConversationData{
				ConversationRef: args.ConversationRef,
				ObservedAt:      h.clock.Now().UnixMilli(),
			}, nil
		case protocol.PrimChatReadThread:
			return protocol.ChatReadThreadData{
				Messages: thread,
				Peer: ptr(protocol.PeerSummary{
					DisplayName: "候选人", PlatformUserRef: conversation.PlatformUserRef,
				}),
				Complete: true, ReachedTop: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil ||
		result.Rounds[0].Status != "ok" {
		t.Fatalf("未读轮分类修正必须成功收尾: result=%+v err=%v", result, err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account.StoppedAt == nil || account.PausedReason != PauseUserRequested {
		t.Fatalf("成功修正后必须 userPaused 等待人工继续: account=%+v err=%v", account, err)
	}
	if names := h.runner.names(); len(names) != 4 ||
		names[0] != protocol.PrimChatReadUnreadTotal ||
		names[1] != protocol.PrimChatReadList ||
		names[2] != protocol.PrimChatOpenConversation ||
		names[3] != protocol.PrimChatReadThread {
		t.Fatalf("修正后不得继续派发其他原语(尤其收口全量读): %v", names)
	}
	if got := unreadListFilters(t, h); countListFilter(got, protocol.ListFilterAll) != 0 {
		t.Fatalf("修正停止后不得再发全量收口读: %+v", got)
	}
}

func countListFilter(got []protocol.ChatReadListArgs, filter protocol.ListFilter) int {
	count := 0
	for _, args := range got {
		if args.Filter == filter {
			count++
		}
	}
	return count
}
