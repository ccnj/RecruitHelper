package patrol

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

func setUnreadHintForTest(h *harness, unread *int) {
	state := HandState{
		Online: true, Session: "session-1", BootID: "boot-1",
	}
	if unread != nil {
		value := *unread
		state.UnreadTotal = &value
	}
	h.hands.set(state)
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
	if len(got) != 2 ||
		got[0].Filter != protocol.ListFilterUnread ||
		got[0].StopOlderThanDays != 0 ||
		got[1].Filter != protocol.ListFilterAll ||
		got[1].StopOlderThanDays != 8 {
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
				got[0].StopOlderThanDays != 8 {
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
	if unreadReads != 2 ||
		h.runner.count(protocol.PrimChatOpenConversation) != 1 ||
		h.runner.count(protocol.PrimChatReadThread) != 0 {
		t.Fatalf("残留气泡重复打开或走错路径: calls=%v unreadReads=%d", h.runner.names(), unreadReads)
	}
	got := unreadListFilters(t, h)
	if len(got) != 3 || got[2].Filter != protocol.ListFilterAll {
		t.Fatalf("残留气泡后没有实际切回全部列表: %+v", got)
	}
	audits, err := h.db.AuditEntries(100)
	if err != nil || countAudit(audits, unreadPatrolAuditCategory) == 0 {
		t.Fatalf("残留气泡未记录无 PII 不一致审计: audits=%+v err=%v", audits, err)
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
			if openCalls != 2 {
				t.Fatalf("局部失败阻断了后续未读: calls=%v", h.runner.names())
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
		t.Fatalf("未读空态异常没有切回全部: %+v", got)
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

func TestUnreadPriorityKnownProfileForcesReadThreadDespiteVerifiedHint(t *testing.T) {
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
		case protocol.PrimChatReadThread:
			return nil, &RunError{
				Code: protocol.ErrCodeTargetNotFound, Retryable: protocol.RetryableNo,
				SideEffect: protocol.SideEffectNone,
			}
		default:
			return defaultHandler(request)
		}
	}

	requireUnreadRoundOK(t, h)
	if h.runner.count(protocol.PrimChatReadThread) != 1 ||
		h.runner.count(protocol.PrimChatOpenConversation) != 0 {
		t.Fatalf("已核对缓存错误压制未读 readThread: %v", h.runner.names())
	}
}

func TestUnreadPriorityKnownManualRequiredProfileUsesOpenOnly(t *testing.T) {
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
		t.Fatalf("manualRequired 已知档案走错未读路径: %v", h.runner.names())
	}
}

func TestUnreadPriorityAdoptedProfileWithoutV4RootUsesOpenOnly(t *testing.T) {
	h := newHarness(t)
	seedCommunicationV4PatrolTarget(t, h, "unread-missing-root-context", "已有入站")
	setUnreadHintForTest(h, ptr(1))
	current := summary(
		"unread-missing-root",
		"peer-unread-missing-root",
		"新入站",
		1,
	)
	current.PositionTitle = ptr("合成职位上下文-unread-missing-root-context")
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
	profile, err := h.db.CandidateProfileByConversation(store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: current.ConversationRef,
	})
	if err != nil || profile == nil {
		t.Fatalf("既有收编规则没有建立 missing-root 档案: profile=%+v err=%v", profile, err)
	}
	if h.runner.count(protocol.PrimChatOpenConversation) != 1 ||
		h.runner.count(protocol.PrimChatReadThread) != 0 {
		t.Fatalf("missing-root 档案没有按仅消未读处理: %v", h.runner.names())
	}
}

func TestUnreadPriorityDoesNotSwallowElementUnresolvedFromOpen(t *testing.T) {
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
	if err != nil || len(result.Rounds) != 1 ||
		!isRunError(result.Rounds[0].Err, protocol.ErrCodeElementUnresolved) {
		t.Fatalf("open 的 ELEMENT_UNRESOLVED 被误吞: result=%+v err=%v", result, err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil || account.PausedReason != PauseHandManualReview {
		t.Fatalf("open 的 manualOnly 没有保留正常停机语义: account=%+v err=%v", account, err)
	}
}

func TestUnreadPriorityGenerationChangeStops(t *testing.T) {
	h := newHarness(t)
	setUnreadHintForTest(h, ptr(0))
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadList {
			h.hands.set(HandState{
				Online: true, Session: "replacement-session", BootID: "replacement-boot",
				UnreadTotal: ptr(1),
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

func TestUnreadPendingSurvivesNilOrZeroSnapshotAfterFailedPass(t *testing.T) {
	tests := []struct {
		name string
		next *int
	}{
		{name: "nil", next: nil},
		{name: "zero", next: ptr(0)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			setUnreadHintForTest(h, ptr(2))
			h.runner.handler = func(request RunRequest) (any, error) {
				if request.Name == protocol.PrimChatReadList {
					args := decodeArgs[protocol.ChatReadListArgs](t, request)
					if args.Filter == protocol.ListFilterUnread {
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
				return defaultHandler(request)
			}

			requireUnreadRoundOK(t, h)
			setUnreadHintForTest(h, test.next)
			h.clock.Add(h.config.MinimumRoundGap)
			before := len(unreadListFilters(t, h))
			requireUnreadRoundOK(t, h)
			after := unreadListFilters(t, h)
			if len(after) <= before ||
				after[before].Filter != protocol.ListFilterUnread {
				t.Fatalf("失败后的 pending 被 %s 快照覆盖: %+v", test.name, after[before:])
			}
		})
	}
}

func TestUnreadPendingClearsOnlyAfterExplicitEmptyAndStableZero(t *testing.T) {
	h := newHarness(t)
	setUnreadHintForTest(h, ptr(1))
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadList {
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Filter == protocol.ListFilterUnread {
				setUnreadHintForTest(h, ptr(0))
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{}, Complete: true,
			}, nil
		}
		return defaultHandler(request)
	}

	requireUnreadRoundOK(t, h)
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("读取账号失败: account=%+v err=%v", account, err)
	}
	h.manager.mu.Lock()
	_, pending := h.manager.unreadPendingGeneration(account)
	h.manager.mu.Unlock()
	if pending {
		t.Fatal("明确空未读列表且稳定总气泡为零后仍保留 pending")
	}

	setUnreadHintForTest(h, nil)
	h.clock.Add(h.config.PatrolInterval)
	before := len(unreadListFilters(t, h))
	requireUnreadRoundOK(t, h)
	after := unreadListFilters(t, h)
	if len(after) != before+1 ||
		after[before].Filter != protocol.ListFilterAll {
		t.Fatalf("已清 pending 后下一轮仍错误插队: %+v", after[before:])
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

	setUnreadHintForTest(h, ptr(0))
	h.clock.Add(h.config.MinimumRoundGap)
	requireUnreadRoundOK(t, h)
	after := unreadListFilters(t, h)
	if len(after) <= prevFilters ||
		after[prevFilters].Filter != protocol.ListFilterUnread {
		t.Fatalf("首帧正数没有建立 pending 插队: %+v", after[prevFilters:])
	}
}

func TestUnreadPendingGenerationPreventsOlderPassFromClearingNewEvent(t *testing.T) {
	h := newHarness(t)
	setUnreadHintForTest(h, ptr(1))
	raisedDuringFirstPass := false
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadList {
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Filter == protocol.ListFilterUnread {
				if !raisedDuringFirstPass {
					raisedDuringFirstPass = true
					prev := 1
					if err := h.manager.HandleEvent(
						"hand-1",
						eventBody(t, h, protocol.EventUnreadBadge, protocol.UnreadBadgeEventData{
							Prev: &prev, Scope: protocol.UnreadScopeTotal,
							Stable: true, Value: 2,
						}),
					); err != nil {
						return nil, err
					}
				}
				// Model the later stable zero arriving before the older list read
				// finishes. The generation, rather than timing, must protect the
				// newly observed work from that older pass's clear.
				setUnreadHintForTest(h, ptr(0))
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{}, Complete: true,
			}, nil
		}
		return defaultHandler(request)
	}

	requireUnreadRoundOK(t, h)
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("读取账号失败: account=%+v err=%v", account, err)
	}
	h.manager.mu.Lock()
	generation, pending := h.manager.unreadPendingGeneration(account)
	h.manager.mu.Unlock()
	if !pending || generation != 2 {
		t.Fatalf("旧 pass 误清了新事件: pending=%v generation=%d", pending, generation)
	}

	h.clock.Add(h.config.MinimumRoundGap)
	before := len(unreadListFilters(t, h))
	requireUnreadRoundOK(t, h)
	after := unreadListFilters(t, h)
	if len(after) <= before ||
		after[before].Filter != protocol.ListFilterUnread {
		t.Fatalf("新一代 pending 没有在下一轮重试: %+v", after[before:])
	}
	h.manager.mu.Lock()
	_, pending = h.manager.unreadPendingGeneration(account)
	h.manager.mu.Unlock()
	if pending {
		t.Fatal("新一代 pending 在自己的明确空态后没有清除")
	}
}

func TestUnreadPendingIsScopedByPrincipal(t *testing.T) {
	h := newHarness(t)
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("读取账号失败: account=%+v err=%v", account, err)
	}
	h.manager.mu.Lock()
	h.manager.observeUnreadSnapshot(account, ptr(1))
	h.manager.mu.Unlock()

	if err := h.db.BindAccountPrincipal(
		h.key, "hand-1", "principal-2", "session-1", "boot-1", h.clock.Now(),
	); err != nil {
		t.Fatalf("改绑新主体失败: %v", err)
	}
	rebound, err := h.db.AccountByKey(h.key)
	if err != nil || rebound == nil {
		t.Fatalf("读取改绑账号失败: account=%+v err=%v", rebound, err)
	}
	h.manager.mu.Lock()
	_, pending := h.manager.unreadPendingGeneration(rebound)
	h.manager.mu.Unlock()
	if pending {
		t.Fatal("新主体继承了旧主体的未读 pending")
	}
}
