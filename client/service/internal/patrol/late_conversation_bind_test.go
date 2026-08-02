package patrol

import (
	"context"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

type patrolLateGreetingFixture struct {
	profileID       string
	platformUserRef string
	positionRef     string
	intentID        string
}

func seedPatrolLateGreeting(t *testing.T, h *harness) patrolLateGreetingFixture {
	t.Helper()
	now := h.clock.Now()
	fixture := patrolLateGreetingFixture{
		profileID: "profile-patrol-late-bind", platformUserRef: "person-patrol-late-bind",
		positionRef: "position-patrol-late-bind", intentID: "intent-patrol-late-bind",
	}
	displayName, positionTitle := "合成候选人", "合成职位"
	if _, err := h.db.SelectCandidateProfile(store.SelectCandidateProfileRequest{
		ProfileID: fixture.profileID,
		Scope: store.CandidateProfileScope{
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			PlatformUserRef: fixture.platformUserRef, PositionRef: fixture.positionRef,
		},
		DisplayName: &displayName, PositionTitle: &positionTitle, ObservedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	msgID := "msg-patrol-late-bind"
	greetingText := "合成招呼"
	contentHash := syncledger.HashText(greetingText)
	argsRaw, err := protocol.Encode(protocol.ChatSendGreetingArgs{
		PlatformUserRef: fixture.platformUserRef, PositionRef: fixture.positionRef, Text: greetingText,
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := now.Add(time.Hour).UnixMilli()
	created, err := h.db.CreateGreetingEffectIntentAndCmd(store.CreateGreetingEffectIntentRequest{
		Intent: store.EffectIntent{
			IntentID: fixture.intentID, IdemKey: "idem-patrol-late-bind",
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			Primitive: protocol.PrimChatSendGreeting, TargetRef: fixture.profileID,
			PayloadHash: "payload-patrol-late-bind", GuardsHash: "guards-patrol-late-bind",
			SendFingerprint: contentHash, Status: store.EffectIntentDispatching, DeadlineMs: deadline,
		},
		Command: store.CmdRecord{
			MsgID: msgID, Name: protocol.PrimChatSendGreeting, Class: string(protocol.ClassEffectful),
			IdemKey: "idem-patrol-late-bind", Domain: h.key.Platform + ":" + h.key.AccountRef,
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			ExpectedPrincipalFingerprint: "principal-1", IntentID: fixture.intentID,
			HandID: "hand-1", Session: "session-1", BootIDAtDispatch: "boot-1",
			Status: store.CmdQueued, Args: string(argsRaw), DeadlineMs: deadline, ExecBudgetMs: 60_000,
		},
		Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.db.MoveEffectToVerification(created.Command.MsgID, "lateBindFixture", now); err != nil {
		t.Fatal(err)
	}
	message, err := h.db.ResolveGreetingVerified(store.VerifiedGreetingSuccess{
		Ref: created.Command.MsgID, ProfileID: fixture.profileID,
		PlatformUserRef: fixture.platformUserRef, PositionRef: fixture.positionRef,
		Text: greetingText, ContentHash: contentHash,
		ResolutionReason: "lateBindFixture", At: now,
	})
	if err != nil || message != nil {
		t.Fatalf("构造无会话引用的招呼成功失败: message=%+v err=%v", message, err)
	}
	return fixture
}

func TestPatrolLateBindsGreetedConversationThenImportsHistoryAsBusinessEvent(t *testing.T) {
	h := newHarness(t)
	fixture := seedPatrolLateGreeting(t, h)
	conversationRef := "conversation-patrol-late-bind"
	greetingText := "合成招呼"
	greetingHash := syncledger.HashText(greetingText)
	cardHash := syncledger.HashText("synthetic-resume-attachment-card")
	cardSourceKey := syncledger.HashText("synthetic-resume-attachment-source")
	resumeCardType := protocol.CardTypeResumeAttachment
	unknownCardState := protocol.CardStateUnknown
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{{
					ConversationRef: conversationRef,
					Peer: protocol.PeerSummary{
						DisplayName: "合成候选人", PlatformUserRef: fixture.platformUserRef,
					},
					UnreadCount: 1,
					LastMessage: protocol.LastMessageSummary{
						Direction: protocol.MessageDirectionIn, Kind: protocol.MessageKindCard,
						TextPreview: "已投递在线简历",
					},
				}},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
			if args.ConversationRef != conversationRef || len(args.Window.AnchorTail) != 1 ||
				args.Window.AnchorTail[0].Direction != protocol.MessageDirectionOut ||
				args.Window.AnchorTail[0].ContentHash != greetingHash {
				t.Fatalf("晚到会话深读目标或锚错误: %+v", args)
			}
			outGreeting := greetingText
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{
					{
						Idx: 0, Direction: protocol.MessageDirectionOut, Kind: protocol.MessageKindText,
						Text: &outGreeting, ContentHash: greetingHash,
					},
					{
						Idx: 1, Direction: protocol.MessageDirectionIn, Kind: protocol.MessageKindCard,
						Text: nil, ContentHash: cardHash, CardType: &resumeCardType,
						CardState: &unknownCardState, SourceKey: cardSourceKey,
					},
				},
				Peer: &protocol.PeerSummary{
					DisplayName: "合成候选人", PlatformUserRef: fixture.platformUserRef,
				},
				Complete: true, ReachedTop: true, AnchorMatched: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("晚到回绑巡检失败: result=%+v err=%v", result, err)
	}
	if h.runner.count(protocol.PrimChatReadThread) != 1 || result.ProjectionCount() != 1 {
		t.Fatalf("晚到回绑后未立即深读并投影业务事件: calls=%v projection=%d",
			h.runner.names(), result.ProjectionCount())
	}
	if len(result.Rounds[0].Projections) != 1 || len(result.Rounds[0].Projections[0].Messages) != 1 ||
		result.Rounds[0].Projections[0].Messages[0].Kind != "card" ||
		result.Rounds[0].Projections[0].Messages[0].CardType != string(protocol.CardTypeResumeAttachment) {
		t.Fatalf("普通 reconcile 投影了招呼历史或漏掉 313: %+v", result.Rounds[0].Projections)
	}

	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef, ConversationRef: conversationRef,
	}
	profile, _ := h.db.CandidateProfileByID(fixture.profileID)
	v4Root, v4RootErr := h.db.CommunicationV4AggregateByProfile(fixture.profileID)
	intent, _ := h.db.EffectIntentByID(fixture.intentID)
	conversation, _ := h.db.ConversationByKey(key)
	tracked, _ := h.db.TrackedIntentByConversation(key)
	messages, messagesErr := h.db.MessagesForConversation(key)
	if messagesErr != nil || v4RootErr != nil || v4Root == nil ||
		v4Root.RootGreetingIntentID != fixture.intentID || v4Root.ProjectedThroughSeq != 1 ||
		profile == nil || profile.ConversationRef == nil ||
		*profile.ConversationRef != conversationRef || intent == nil || intent.ResultConversationRef == nil ||
		*intent.ResultConversationRef != conversationRef || intent.ResultMessageSeq == nil ||
		*intent.ResultMessageSeq != 1 || conversation == nil ||
		conversation.TrackingState != store.TrackingAdopted || conversation.AdoptedBoundarySeq != 1 ||
		tracked == nil || tracked.Status != store.TrackingAdopted || len(messages) != 2 ||
		messages[0].Direction != "out" || messages[0].Text == nil || *messages[0].Text != greetingText ||
		messages[0].OutboundIntentID == nil || *messages[0].OutboundIntentID != fixture.intentID ||
		messages[1].Direction != "in" || messages[1].Kind != "card" ||
		messages[1].CardType != string(protocol.CardTypeResumeAttachment) ||
		messages[1].SourceKey == nil || *messages[1].SourceKey != cardSourceKey ||
		messages[1].Origin != "external" {
		t.Fatalf("晚到回绑与历史导入未闭合: root=%+v rootErr=%v profile=%+v intent=%+v conversation=%+v tracked=%+v messages=%+v err=%v",
			v4Root, v4RootErr, profile, intent, conversation, tracked, messages, messagesErr)
	}
	// Q5:trial 死入口的 inspect/identity 辅助已删,改用生产 v4 冻结路径
	// 同款的账本事实与身份原语断言"晚到导入的历史能形成可冻结的 313 强意向轮"。
	lastOutbound := messages[0]
	inboundBoundary := messages[1:]
	if lastOutbound.Direction != "out" || lastOutbound.Seq != 1 ||
		lastOutbound.OutboundIntentID == nil || *lastOutbound.OutboundIntentID != fixture.intentID ||
		len(inboundBoundary) != 1 || inboundBoundary[0].Seq != 2 {
		t.Fatalf("晚到招呼锚未形成可冻结的 313 强意向轮: outbound=%+v inbound=%+v", lastOutbound, inboundBoundary)
	}
	if kind, ok := store.DialogueTurnInputKindOf(inboundBoundary); !ok ||
		kind != store.DialogueTurnInputResumeAttachment {
		t.Fatalf("313 未归入强意向冻结输入: kind=%q ok=%v", kind, ok)
	}
	if digest, turnID, identityErr := store.DialogueTurnIdentity(fixture.profileID, lastOutbound, inboundBoundary); identityErr != nil || digest == "" || turnID != "turn-"+digest {
		t.Fatalf("强意向轮无法冻结唯一 identity: digest=%q turn=%q err=%v", digest, turnID, identityErr)
	}
	rounds, roundsErr := h.db.RecentPatrolRounds(h.key, 1)
	if roundsErr != nil || len(rounds) != 1 || rounds[0].NewMessageCount != 1 {
		t.Fatalf("真实历史未按普通 Adopt=false 业务事件计数: rounds=%+v err=%v", rounds, roundsErr)
	}
}
