package patrol

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

// TestSimulationCandidateInterjectsDuringMultiBubbleChain 是行为模拟:钉住
// "多气泡链中途候选人插话"在身份判新(2026-08-09 战役 S2 换根)下的终态走向。
// 换根前它钉的是丢插话的旧现状(dcc9d5c);换根使行为反转,断言随生产同批翻转。
// 四幕:
// 幕一,AI 回复拆三条气泡,链内无重读,整链发完;
// 幕二,页面实序 [气泡1, 插话, 气泡2, 气泡3] 进入到期对账,插话按服务端
// 身份判新捞回进账本、开新一轮;"算了不考虑了"命中拒绝正则族短路(零 AI
// 调用),就地转挽留——挽留话术与换微信卡发出,不再有 context_discarded;
// 幕三,24 小时后排程巡检,对已说"算了"的候选人零动作——旧世界在这里
// 会发出冷催,正是本战役根治的误催;
// 幕四,候选人(挽留后)再发新消息,正常收编、正常开新轮回复;插话永久
// 存在于账本与 AI 可见的对话历史中。
func TestSimulationCandidateInterjectsDuringMultiBubbleChain(t *testing.T) {
	h := newHarness(t)
	// 时间钉到 schedule 锚点前 25 小时,给幕三留出冷催 24h 门槛。
	h.clock.Add(scheduleTestBusinessNow().Sub(h.clock.Now()))
	h.clock.Add(-25 * time.Hour)

	inbound := "你好,想了解一下这个岗位"
	fixture := seedCommunicationV4PatrolTarget(t, h, "interleave-sim", inbound)
	peerRef := "person-v4-patrol-interleave-sim"

	bubbles := []string{
		"您好,这个岗位主要负责客户维护",
		"工作时间灵活,底薪加提成",
		"方便的话我们约个时间详细聊聊",
	}
	interjection := "待遇太低了,算了不考虑了"

	// 幕四把回复话术换成单气泡,便于按发送序列区分各幕产物。
	replyBubbles := bubbles
	advice := &recordingAdviceExecutor{
		complete: func(_ int, request m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			switch request.Purpose {
			case m5ai.PurposeIntent:
				return safeFakeResponse(`{"信号":"有意向","理由":"fixture"}`), nil
			case m5ai.PurposeReply:
				raw, err := json.Marshal(replyBubbles)
				if err != nil {
					return m5ai.CompletionResponse{}, err
				}
				return safeFakeResponse(fmt.Sprintf(`{"话术_序列":%s,"动作":"无"}`, raw)), nil
			case m5ai.PurposeSilenceFollowup:
				return safeFakeResponse(`{"话术":"合成冷催一","抓的点":"合成经历"}`), nil
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
	// —— 幕一:候选人来一句,AI 回三条气泡,整链发完 ——
	if err := manager.EnableToday(h.key); err != nil {
		t.Fatal(err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("账号读取失败: account=%+v err=%v", account, err)
	}
	beginCommunicationV4PatrolRound(t, h, "round-sim-chain")
	actor := &roundActor{
		manager: manager, account: account,
		hand:    HandState{Online: true, Session: "session-1", BootID: "boot-1"},
		roundID: "round-sim-chain", now: h.clock.Now(),
	}
	manager.mu.Lock()
	err = actor.processCommunicationV4Targets(context.Background())
	manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	sentTexts := recordedSendTexts(t, hand)
	if len(sentTexts) != len(bubbles) {
		t.Fatalf("三条气泡应全部发出(链内没有任何重读页面的动作): sent=%v", sentTexts)
	}
	for i := range bubbles {
		if sentTexts[i] != bubbles[i] {
			t.Fatalf("气泡顺序错乱: got=%v want=%v", sentTexts, bubbles)
		}
	}
	chainTurn, err := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	if err != nil || chainTurn == nil || chainTurn.Status != store.DialogueTurnCompleted {
		t.Fatalf("幕一 turn 未完成: turn=%+v err=%v", chainTurn, err)
	}

	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: fixture.conversationRef,
	}
	ledgerAfterChain, err := h.db.MessagesForConversation(key)
	if err != nil {
		t.Fatal(err)
	}
	// 招呼(out) + 入站(in) + 三条气泡(out)。
	if len(ledgerAfterChain) != 5 {
		t.Fatalf("幕一后账本行数不符: %d", len(ledgerAfterChain))
	}

	// 页面实序:插话夹在气泡 1 与气泡 2 之间。每次调用现读账本再插入,
	// 保证各幕拿到的都是"账本内容 + 一条从未收编的插话";extraTail 模拟
	// 候选人后来追加的链尾消息,已收编过的不重复附加。
	interleavedPage := func(extraTail ...string) []protocol.ThreadMessage {
		rows := echoLedgerAsThread(t, h, key)
		inLedger := map[string]bool{}
		for _, row := range rows {
			if row.Text != nil {
				inLedger[*row.Text] = true
			}
		}
		out := make([]protocol.ThreadMessage, 0, len(rows)+1+len(extraTail))
		interjectionInserted := false
		for _, row := range rows {
			out = append(out, row)
			if !interjectionInserted && row.Text != nil && *row.Text == bubbles[0] {
				interjectionInserted = true
				text := interjection
				out = append(out, protocol.ThreadMessage{
					Direction: protocol.MessageDirectionIn, Kind: protocol.MessageKindText,
					Text: &text, ContentHash: syncledger.HashText(interjection),
					SourceKey: fixtureSourceKey(interjection),
				})
			}
		}
		for _, tail := range extraTail {
			if inLedger[tail] {
				continue
			}
			text := tail
			out = append(out, protocol.ThreadMessage{
				Direction: protocol.MessageDirectionIn, Kind: protocol.MessageKindText,
				Text: &text, ContentHash: syncledger.HashText(text),
				SourceKey: fixtureSourceKey(text),
			})
		}
		for i := range out {
			out[i].Idx = i
		}
		return out
	}
	threadReads := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{{
					ConversationRef: fixture.conversationRef,
					Peer: protocol.PeerSummary{
						DisplayName: "合成候选人-interleave-sim", PlatformUserRef: peerRef,
					},
					LastMessage: protocol.LastMessageSummary{
						Direction: protocol.MessageDirectionOut, Kind: protocol.MessageKindText,
						TextPreview: bubbles[len(bubbles)-1],
					},
				}},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			threadReads++
			return protocol.ChatReadThreadData{
				Messages: interleavedPage(),
				Peer: ptr(protocol.PeerSummary{
					DisplayName: "合成候选人-interleave-sim", PlatformUserRef: peerRef,
				}),
				Complete: true, ReachedTop: true, AnchorMatched: false,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	// —— 幕二:到期对账读到插话,对齐把它裁进丢弃前缀 ——
	h.clock.Add(h.config.TrackedReconcileInterval + time.Minute)
	if err := manager.EnableToday(h.key); err != nil {
		t.Fatal(err)
	}
	result, tickErr := manager.Tick(context.Background())
	if tickErr != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("幕二 Tick 失败: result=%+v err=%v", result, tickErr)
	}
	if threadReads == 0 {
		t.Fatal("到期对账没有发生,幕二没有模拟到位")
	}
	ledgerAfterReconcile, err := h.db.MessagesForConversation(key)
	if err != nil {
		t.Fatal(err)
	}
	// 身份判新捞回:插话按身份收编进账本,拒绝正则族短路命中(零新增 AI
	// 调用),就地转挽留——挽留话术 + 换微信卡两行出站。5 + 插话 + 挽留 + 卡 = 8。
	if len(ledgerAfterReconcile) != len(ledgerAfterChain)+3 {
		t.Fatalf("幕二后账本应为 捞回插话+挽留+卡 三行新增: before=%d after=%d",
			len(ledgerAfterChain), len(ledgerAfterReconcile))
	}
	interjectionAdopted := false
	for _, row := range ledgerAfterReconcile {
		if row.Text != nil && *row.Text == interjection {
			interjectionAdopted = true
			if row.Direction != "in" || row.SourceKey == nil {
				t.Fatalf("捞回的插话必须是带身份的入站行: row=%+v", row)
			}
		}
	}
	if !interjectionAdopted {
		t.Fatal("插话必须被身份判新捞回进账本")
	}
	if discards := countContextDiscardAudits(t, h, fixture.conversationRef); discards != 0 {
		t.Fatalf("身份判新不再裁弃上下文,不应有 context_discarded 审计: %d", discards)
	}
	afterTurn, err := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	if err != nil || afterTurn == nil || afterTurn.TurnID == chainTurn.TurnID {
		t.Fatalf("捞回的插话必须开启新一轮处理: turn=%+v err=%v", afterTurn, err)
	}
	if len(advice.requests) != 2 {
		t.Fatalf("拒绝语走确定性正则短路,不应新增 AI 调用: advice=%d", len(advice.requests))
	}
	retentionSends := recordedSendTexts(t, hand)
	if len(retentionSends) != len(bubbles)+1 || retentionSends[len(retentionSends)-1] != "合成挽留" {
		t.Fatalf("拒绝分支应发出挽留话术: sends=%v", retentionSends)
	}
	aggregate, err := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	if err != nil || aggregate == nil || aggregate.State.LastOutboundAt == nil {
		t.Fatalf("幕二后聚合缺失: aggregate=%+v err=%v", aggregate, err)
	}

	// —— 幕三:24 小时后排程巡检,对已说"算了"的候选人不再冷催 ——
	h.clock.Add(nextScheduleTestBusinessTime(
		aggregate.State.LastOutboundAt.Add(24 * time.Hour),
	).Sub(h.clock.Now()))
	if err := manager.EnableToday(h.key); err != nil {
		t.Fatal(err)
	}
	runCommunicationV4ScheduleRound(t, h, manager, "round-sim-cold")

	actions, err := h.db.CommunicationV4EventActionsByProfile(fixture.profileID)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range actions {
		if action.V4Kind == communication.V4ActionColdPrompt {
			t.Fatalf("对已说\"算了\"的候选人不得再冷催: %+v", action)
		}
	}
	if got := recordedSendTexts(t, hand); len(got) != len(bubbles)+1 {
		t.Fatalf("幕三不应有任何新发送: sends=%v", got)
	}
	// —— 幕四:候选人在链尾之后又发新消息,应正常收编、正常开新轮 ——
	followupInbound := "那你再具体说说岗位内容吧"
	followupReply := "好的,我再详细介绍一下"
	replyBubbles = []string{followupReply}
	ledgerBeforeFollowup, err := h.db.MessagesForConversation(key)
	if err != nil {
		t.Fatal(err)
	}
	adviceBeforeFollowup := len(advice.requests)
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{{
					ConversationRef: fixture.conversationRef,
					Peer: protocol.PeerSummary{
						DisplayName: "合成候选人-interleave-sim", PlatformUserRef: peerRef,
					},
					UnreadCount: 1,
					LastMessage: protocol.LastMessageSummary{
						Direction: protocol.MessageDirectionIn, Kind: protocol.MessageKindText,
						TextPreview: followupInbound,
					},
				}},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			return protocol.ChatReadThreadData{
				Messages: interleavedPage(followupInbound),
				Peer: ptr(protocol.PeerSummary{
					DisplayName: "合成候选人-interleave-sim", PlatformUserRef: peerRef,
				}),
				Complete: true, ReachedTop: true, AnchorMatched: false,
			}, nil
		default:
			return defaultHandler(request)
		}
	}
	h.clock.Add(5 * time.Minute)
	result, tickErr = manager.Tick(context.Background())
	if tickErr != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("幕四 Tick 失败: result=%+v err=%v", result, tickErr)
	}
	followupTurn, err := h.db.LatestDialogueTurnForProfile(fixture.profileID)
	if err != nil || followupTurn == nil ||
		followupTurn.TurnID == chainTurn.TurnID ||
		followupTurn.Status != store.DialogueTurnCompleted {
		t.Fatalf("链尾新消息应正常开新轮并完成: turn=%+v err=%v", followupTurn, err)
	}
	if len(advice.requests) != adviceBeforeFollowup+2 {
		t.Fatalf("新轮应恰好一次意向、一次回复: before=%d now=%d",
			adviceBeforeFollowup, len(advice.requests))
	}
	finalSends := recordedSendTexts(t, hand)
	if finalSends[len(finalSends)-1] != followupReply {
		t.Fatalf("新轮回复未发出: sends=%v", finalSends)
	}
	finalLedger, err := h.db.MessagesForConversation(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(finalLedger) != len(ledgerBeforeFollowup)+2 {
		t.Fatalf("幕四应恰好收编新消息与我方回复两行: before=%d after=%d",
			len(ledgerBeforeFollowup), len(finalLedger))
	}
	if finalLedger[len(finalLedger)-2].Text == nil ||
		*finalLedger[len(finalLedger)-2].Text != followupInbound ||
		finalLedger[len(finalLedger)-1].Text == nil ||
		*finalLedger[len(finalLedger)-1].Text != followupReply {
		t.Fatalf("幕四账本尾部不符: %+v", finalLedger[len(finalLedger)-2:])
	}
	interjectionPersisted := false
	for _, row := range finalLedger {
		if row.Text != nil && *row.Text == interjection {
			interjectionPersisted = true
		}
	}
	if !interjectionPersisted {
		t.Fatal("捞回的插话必须永久存在于账本")
	}
	ledgerDump := make([]string, 0, len(finalLedger))
	for _, row := range finalLedger {
		text := "<无正文>"
		if row.Text != nil {
			text = *row.Text
		}
		ledgerDump = append(ledgerDump, fmt.Sprintf("%s:%s", row.Direction, text))
	}
	t.Logf("最终账本(插话在场): %v", ledgerDump)
	t.Logf("模拟结论: 三气泡全发; 插话被身份判新捞回并触发拒绝短路转挽留(context_discarded %d 条); 24h 后零冷催; 挽留后新消息正常开新轮",
		countContextDiscardAudits(t, h, fixture.conversationRef))
}

func recordedSendTexts(t *testing.T, hand *m5PositiveHand) []string {
	t.Helper()
	hand.mu.Lock()
	commands := append([]protocol.CmdBody(nil), hand.commands...)
	hand.mu.Unlock()
	out := make([]string, 0, len(commands))
	for _, command := range commands {
		if command.Name != protocol.PrimChatSendMessage {
			continue
		}
		var args protocol.ChatSendMessageArgs
		if err := json.Unmarshal(command.Args, &args); err != nil {
			t.Fatal(err)
		}
		out = append(out, args.Text)
	}
	return out
}

func countContextDiscardAudits(t *testing.T, h *harness, conversationRef string) int {
	t.Helper()
	db, err := sql.Open(
		"sqlite",
		"file:"+filepath.Join(h.dataDir, "brain.db")+"?_pragma=busy_timeout(5000)",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM audit_entries WHERE category = ? AND conversation_ref = ?",
		"conversation_alignment_context_discarded", conversationRef,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
