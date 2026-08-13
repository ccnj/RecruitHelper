package patrol

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

func lastLookAuditEntries(t *testing.T, h *harness) []store.AuditEntry {
	t.Helper()
	entries, err := h.db.AuditEntries(200)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]store.AuditEntry, 0)
	for _, entry := range entries {
		if entry.Category == lastLookAuditCategory {
			out = append(out, entry)
		}
	}
	return out
}

// 核心场景:巡检处理完"开窗候选人"后继续扫列表,期间他秒回;列表里另一个
// 候选人判脏、即将被打开。切换前必须先就地读回并回掉开窗候选人的插话,再照
// 原计划处理脏行候选人。
func TestLastLookProcessesOpenConversationBeforeSwitchingToDirtyRow(t *testing.T) {
	h := newHarness(t)
	open := seedCommunicationV4PatrolTarget(t, h, "last-look-open", "开窗候选人的首条消息")
	dirty := seedCommunicationV4PatrolTarget(t, h, "last-look-dirty", "脏行候选人的首条消息")

	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			switch request.Purpose {
			case m5ai.PurposeIntent:
				return safeFakeResponse(`{"信号":"有意向","理由":"fixture"}`), nil
			case m5ai.PurposeReply:
				return safeFakeResponse(`{"话术_序列":["合成临走回复"],"动作":"无"}`), nil
			default:
				return m5ai.CompletionResponse{}, fmt.Errorf("未知建议用途 %q", request.Purpose)
			}
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

	// 第一轮:两行摘要与账本一致(不脏),各自收束首轮回复,页面最后停在
	// 开窗候选人的会话上。此轮不应触发任何临走检查。
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary(open.conversationRef, "person-v4-patrol-last-look-open", "开窗候选人的首条消息", 0),
					summary(dirty.conversationRef, "person-v4-patrol-last-look-dirty", "脏行候选人的首条消息", 0),
				},
				Complete: true,
			}, nil
		case protocol.PrimChatIdentifyCurrentConversation:
			t.Fatal("无脏行的轮不得触发临走检查")
			return nil, nil
		case protocol.PrimChatReadThread:
			t.Fatal("摘要与账本一致时不应读取线程")
			return nil, nil
		default:
			return defaultHandler(request)
		}
	}
	result, err := manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("首轮收束失败: result=%+v err=%v", result, err)
	}
	if hand.commandCount() != 2 {
		t.Fatalf("首轮应各回一条: commands=%d", hand.commandCount())
	}

	// 第二轮:开窗候选人插话(不在列表快照里),脏行候选人有新消息判脏。
	openInterjection := "开窗候选人的插话"
	dirtyInbound := "脏行候选人的新消息"
	var order []string
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary(dirty.conversationRef, "person-v4-patrol-last-look-dirty", dirtyInbound, 0),
				},
				Complete: true,
			}, nil
		case protocol.PrimChatIdentifyCurrentConversation:
			order = append(order, "identify")
			return protocol.ChatIdentifyCurrentConversationData{
				ConversationRef: open.conversationRef,
				ObservedAt:      h.clock.Now().UnixMilli(),
			}, nil
		case protocol.PrimChatReadThread:
			args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
			switch args.ConversationRef {
			case open.conversationRef:
				if !args.RequireCurrent {
					t.Fatal("临走读取当前窗口必须 requireCurrent=true(不得导航)")
				}
				order = append(order, "readOpen")
				thread := currentConversationThreadMessages(t, h, open.conversationRef)
				thread = append(thread, threadText(len(thread), openInterjection))
				return protocol.ChatReadThreadData{
					Messages: thread,
					Peer: &protocol.PeerSummary{
						DisplayName:     "脱敏候选人",
						PlatformUserRef: "person-v4-patrol-last-look-open",
					},
					Complete: true, ReachedTop: true,
				}, nil
			case dirty.conversationRef:
				if args.RequireCurrent {
					t.Fatal("目标会话读取必须走正常导航路径(requireCurrent=false)")
				}
				order = append(order, "readDirty")
				thread := currentConversationThreadMessages(t, h, dirty.conversationRef)
				thread = append(thread, threadText(len(thread), dirtyInbound))
				return protocol.ChatReadThreadData{
					Messages: thread,
					Peer: &protocol.PeerSummary{
						DisplayName:     "脱敏候选人",
						PlatformUserRef: "person-v4-patrol-last-look-dirty",
					},
					Complete: true, ReachedTop: true,
				}, nil
			default:
				t.Fatalf("读取了未知会话 %q", args.ConversationRef)
				return nil, nil
			}
		default:
			return defaultHandler(request)
		}
	}
	h.clock.Add(6 * time.Minute)
	result, err = manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("临走轮失败: result=%+v err=%v", result, err)
	}

	want := []string{"identify", "readOpen", "readDirty"}
	if len(order) != len(want) {
		t.Fatalf("命令序列越界: %v", order)
	}
	for index := range want {
		if order[index] != want[index] {
			t.Fatalf("临走处理必须先于目标切换: got=%v want=%v", order, want)
		}
	}
	if hand.commandCount() != 4 {
		t.Fatalf("临走轮应各回一条(插话+脏行): commands=%d", hand.commandCount())
	}
	messages, err := h.db.MessagesForConversation(store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: open.conversationRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	collected := false
	for _, message := range messages {
		if message.Text != nil && *message.Text == openInterjection {
			collected = true
		}
	}
	if !collected {
		t.Fatalf("插话未被临走检查收进账本: messages=%d", len(messages))
	}
	turn, err := h.db.LatestDialogueTurnForProfile(open.profileID)
	if err != nil || turn == nil || turn.Status != store.DialogueTurnCompleted {
		t.Fatalf("插话回复轮未完成: turn=%+v err=%v", turn, err)
	}
	dirtyTurn, err := h.db.LatestDialogueTurnForProfile(dirty.profileID)
	if err != nil || dirtyTurn == nil || dirtyTurn.Status != store.DialogueTurnCompleted {
		t.Fatalf("脏行候选人被临走检查挡住: turn=%+v err=%v", dirtyTurn, err)
	}
	audits := lastLookAuditEntries(t, h)
	if len(audits) != 1 || audits[0].ConversationRef != open.conversationRef ||
		!strings.Contains(audits[0].Detail, "status=processed") ||
		strings.Contains(audits[0].Detail, "newMessages=0") {
		t.Fatalf("临走审计缺失或未记命中: audits=%+v", audits)
	}
}

// 各种"检查没做成"的场景必须零动作放行,目标会话照常处理;当前会话绝不能
// 被 requireCurrent 读取(那意味着对不可处理对象动了手)。
func TestLastLookSkipsAndNeverBlocksTargetProcessing(t *testing.T) {
	type variant struct {
		name string
		// prepare 返回 identify 的应答(或错误);nil data + err 表示识别失败。
		prepare func(t *testing.T, h *harness, dirtyRef string) (protocol.ChatIdentifyCurrentConversationData, error)
	}
	variants := []variant{
		{
			name: "当前会话即目标会话",
			prepare: func(_ *testing.T, h *harness, dirtyRef string) (protocol.ChatIdentifyCurrentConversationData, error) {
				return protocol.ChatIdentifyCurrentConversationData{
					ConversationRef: dirtyRef, ObservedAt: h.clock.Now().UnixMilli(),
				}, nil
			},
		},
		{
			name: "识别失败(页面级)",
			prepare: func(_ *testing.T, _ *harness, _ string) (protocol.ChatIdentifyCurrentConversationData, error) {
				return protocol.ChatIdentifyCurrentConversationData{}, &RunError{
					Code: protocol.ErrCodeCtxNotReady, Reason: protocol.NotReadyReasonContentScriptDead,
					Retryable: protocol.RetryableAfterRecovery, SideEffect: protocol.SideEffectNone,
					Cause: errors.New("content script unavailable"),
				}
			},
		},
		{
			name: "当前会话未跟踪",
			prepare: func(_ *testing.T, h *harness, _ string) (protocol.ChatIdentifyCurrentConversationData, error) {
				return protocol.ChatIdentifyCurrentConversationData{
					ConversationRef: "conversation-untracked",
					ObservedAt:      h.clock.Now().UnixMilli(),
				}, nil
			},
		},
		{
			name: "当前会话已隔离",
			prepare: func(t *testing.T, h *harness, _ string) (protocol.ChatIdentifyCurrentConversationData, error) {
				quarantined := seedCommunicationV4PatrolTarget(
					t, h, "last-look-quarantined", "被隔离候选人的消息",
				)
				if _, err := h.db.QuarantineConversationPatrol(store.ConversationKey{
					Platform: h.key.Platform, AccountRef: h.key.AccountRef,
					ConversationRef: quarantined.conversationRef,
				}, "patrolQuarantine:test", h.clock.Now()); err != nil {
					t.Fatal(err)
				}
				return protocol.ChatIdentifyCurrentConversationData{
					ConversationRef: quarantined.conversationRef,
					ObservedAt:      h.clock.Now().UnixMilli(),
				}, nil
			},
		},
	}
	for _, tc := range variants {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			dirty := seedCommunicationV4PatrolTarget(t, h, "last-look-target", "目标候选人的首条消息")
			identifyData, identifyErr := tc.prepare(t, h, dirty.conversationRef)

			dirtyInbound := "目标候选人的新消息"
			identifies, dirtyReads := 0, 0
			h.runner.handler = func(request RunRequest) (any, error) {
				switch request.Name {
				case protocol.PrimChatReadList:
					return protocol.ChatReadListData{
						Sessions: []protocol.ConversationSummary{
							summary(dirty.conversationRef, "person-v4-patrol-last-look-target", dirtyInbound, 0),
						},
						Complete: true,
					}, nil
				case protocol.PrimChatIdentifyCurrentConversation:
					identifies++
					if identifyErr != nil {
						return nil, identifyErr
					}
					return identifyData, nil
				case protocol.PrimChatReadThread:
					args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
					if args.RequireCurrent || args.ConversationRef != dirty.conversationRef {
						t.Fatalf("跳过场景只允许目标会话的导航读取: %+v", args)
					}
					dirtyReads++
					thread := currentConversationThreadMessages(t, h, dirty.conversationRef)
					thread = append(thread, threadText(len(thread), dirtyInbound))
					return protocol.ChatReadThreadData{
						Messages: thread,
						Peer: &protocol.PeerSummary{
							DisplayName:     "脱敏候选人",
							PlatformUserRef: "person-v4-patrol-last-look-target",
						},
						Complete: true, ReachedTop: true,
					}, nil
				default:
					return defaultHandler(request)
				}
			}
			result, err := h.manager.Tick(context.Background())
			if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
				t.Fatalf("跳过场景不得影响轮结果: result=%+v err=%v", result, err)
			}
			if identifies != 1 || dirtyReads != 1 {
				t.Fatalf("跳过场景派发越界: identifies=%d dirtyReads=%d", identifies, dirtyReads)
			}
			// 无 AI provider 的世界里目标会话仍应完成对账并冻结待处理轮:
			// 临走检查的跳过不改变目标会话的既有推进语义。
			turn, err := h.db.LatestDialogueTurnForProfile(dirty.profileID)
			if err != nil || turn == nil {
				t.Fatalf("目标会话未照常推进: turn=%+v err=%v", turn, err)
			}
			if audits := lastLookAuditEntries(t, h); len(audits) != 0 {
				t.Fatalf("跳过场景不得写临走审计: %+v", audits)
			}
		})
	}
}

// 未读插队路径:固定打开前触发临走检查;打开后共享处理块内的第二次检查因
// "当前会话即目标会话"零动作跳过,不产生额外读取。
func TestLastLookRunsBeforeUnreadOpenAndDedupesAfterOpen(t *testing.T) {
	h := newHarness(t)
	dirty := seedCommunicationV4PatrolTarget(t, h, "last-look-unread", "未读候选人的首条消息")
	setUnreadHintForTest(h, ptr(1))

	dirtyInbound := "未读候选人的新消息"
	var order []string
	identifies := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Filter == protocol.ListFilterUnread {
				order = append(order, "readListUnread")
				return protocol.ChatReadListData{
					Sessions: []protocol.ConversationSummary{
						summary(dirty.conversationRef, "person-v4-patrol-last-look-unread", dirtyInbound, 1),
					},
					Complete: true,
				}, nil
			}
			order = append(order, "readListAll")
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{}, Complete: true,
			}, nil
		case protocol.PrimChatIdentifyCurrentConversation:
			identifies++
			order = append(order, "identify")
			if identifies == 1 {
				// 打开前:页面还停在一个未跟踪的旧窗口上。
				return protocol.ChatIdentifyCurrentConversationData{
					ConversationRef: "conversation-stale-window",
					ObservedAt:      h.clock.Now().UnixMilli(),
				}, nil
			}
			// 打开后:当前会话即目标会话,检查必须零动作跳过。
			return protocol.ChatIdentifyCurrentConversationData{
				ConversationRef: dirty.conversationRef,
				ObservedAt:      h.clock.Now().UnixMilli(),
			}, nil
		case protocol.PrimChatOpenConversation:
			order = append(order, "open")
			args := decodeArgs[protocol.ChatOpenConversationArgs](t, request)
			return protocol.ChatOpenConversationData{ConversationRef: args.ConversationRef}, nil
		case protocol.PrimChatReadThread:
			args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
			if args.RequireCurrent || args.ConversationRef != dirty.conversationRef {
				t.Fatalf("未读路径只允许目标会话的导航读取: %+v", args)
			}
			order = append(order, "readThread")
			thread := currentConversationThreadMessages(t, h, dirty.conversationRef)
			thread = append(thread, threadText(len(thread), dirtyInbound))
			return protocol.ChatReadThreadData{
				Messages: thread,
				Peer: &protocol.PeerSummary{
					DisplayName:     "脱敏候选人",
					PlatformUserRef: "person-v4-patrol-last-look-unread",
				},
				Complete: true, ReachedTop: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}
	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("未读临走轮失败: result=%+v err=%v", result, err)
	}
	want := []string{"readListUnread", "identify", "open", "identify", "readThread", "readListAll"}
	if len(order) != len(want) {
		t.Fatalf("未读路径命令序列越界: got=%v want=%v", order, want)
	}
	for index := range want {
		if order[index] != want[index] {
			t.Fatalf("未读路径顺序错误: got=%v want=%v", order, want)
		}
	}
}
