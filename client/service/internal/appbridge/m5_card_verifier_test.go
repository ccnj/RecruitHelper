package appbridge

import (
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

func TestClassifyVerifiedCardRequiresUniqueStrictMatchAfterAnchor(t *testing.T) {
	anchorHash := syncledger.HashText("anchor")
	sourceKey := strings.Repeat("a", 64)
	wechatType, pending := protocol.CardTypeWechatExchange, protocol.CardStatePending
	wechatHash := syncledger.WechatExchangeContentHash()
	wechat := protocol.ThreadMessage{
		Direction: protocol.MessageDirectionOut, Kind: protocol.MessageKindCard,
		ContentHash: wechatHash, SourceKey: sourceKey,
		CardType: &wechatType, CardState: &pending,
	}
	anchor := verificationThreadMessage(0, protocol.MessageDirectionIn, anchorHash)

	observation, err := classifyVerifiedCard(
		[]protocol.ThreadMessage{wechat, anchor, wechat}, []int{1}, 1,
		protocol.PrimChatSendWechatInvite, wechatHash, nil,
	)
	if err != nil || !observation.Confirmed || observation.SourceKey != sourceKey {
		t.Fatalf("锚前同卡不得制造歧义，锚后唯一卡应确认: observation=%+v err=%v", observation, err)
	}

	accepted := protocol.CardStateAccepted
	invalidState := wechat
	invalidState.CardState = &accepted
	uppercaseSource := wechat
	uppercaseSource.SourceKey = strings.Repeat("A", 64)
	wrongHash := wechat
	wrongHash.ContentHash = strings.Repeat("b", 64)
	cases := []struct {
		name         string
		messages     []protocol.ThreadMessage
		anchorStarts []int
	}{
		{name: "missing anchor", messages: []protocol.ThreadMessage{wechat}},
		{name: "duplicate anchor", messages: []protocol.ThreadMessage{anchor, wechat}, anchorStarts: []int{0, 0}},
		{name: "wrong state", messages: []protocol.ThreadMessage{anchor, invalidState}, anchorStarts: []int{0}},
		{name: "unstable source", messages: []protocol.ThreadMessage{anchor, uppercaseSource}, anchorStarts: []int{0}},
		{name: "wrong hash", messages: []protocol.ThreadMessage{anchor, wrongHash}, anchorStarts: []int{0}},
		{name: "multiple matches", messages: []protocol.ThreadMessage{anchor, wechat, wechat}, anchorStarts: []int{0}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, classifyErr := classifyVerifiedCard(
				test.messages, test.anchorStarts, 1,
				protocol.PrimChatSendWechatInvite, wechatHash, nil,
			)
			if classifyErr != nil || got.Confirmed {
				t.Fatalf("阴性/歧义只能保持未确认: observation=%+v err=%v", got, classifyErr)
			}
		})
	}
}

func TestClassifyVerifiedInterviewCardRequiresExactFrozenParameters(t *testing.T) {
	anchorHash := syncledger.HashText("anchor")
	sourceKey := strings.Repeat("b", 64)
	cardType, state := protocol.CardTypeInterviewInvite, protocol.CardStateUnknown
	expected := protocol.InterviewDetails{
		StartsAt: 1_722_000_000_000, EndsAt: 1_722_001_800_000,
		Method: protocol.InterviewMethodWechatVideo,
	}
	targetHash := syncledger.InterviewInviteContentHash(
		expected.StartsAt, expected.EndsAt, string(expected.Method),
	)
	anchor := verificationThreadMessage(0, protocol.MessageDirectionIn, anchorHash)
	card := protocol.ThreadMessage{
		Direction: protocol.MessageDirectionOut, Kind: protocol.MessageKindCard,
		ContentHash: targetHash, SourceKey: sourceKey, CardType: &cardType, CardState: &state,
		Interview: &expected,
	}
	observation, err := classifyVerifiedCard(
		[]protocol.ThreadMessage{anchor, card}, []int{0}, 1,
		protocol.PrimChatSendInviteCard, targetHash, &expected,
	)
	if err != nil || !observation.Confirmed || observation.Interview == nil ||
		*observation.Interview != expected {
		t.Fatalf("精确邀面参数应确认: observation=%+v err=%v", observation, err)
	}

	different := expected
	different.EndsAt += 300_000
	card.Interview = &different
	observation, err = classifyVerifiedCard(
		[]protocol.ThreadMessage{anchor, card}, []int{0}, 1,
		protocol.PrimChatSendInviteCard, targetHash, &expected,
	)
	if err != nil || observation.Confirmed {
		t.Fatalf("邀面参数不一致只能未确认: observation=%+v err=%v", observation, err)
	}
}

func TestCardVerificationReadAtomicallyAdoptsObservedCard(t *testing.T) {
	tests := []struct {
		name      string
		primitive string
		args      any
		message   func(string) protocol.ThreadMessage
		hash      string
		cardType  string
		cardState string
	}{
		{
			name: "wechat invite", primitive: protocol.PrimChatSendWechatInvite,
			args: protocol.ChatSendWechatInviteArgs{ConversationRef: "conversation-card-verifier"},
			hash: syncledger.WechatExchangeContentHash(), cardType: "wechatExchange", cardState: "pending",
			message: func(sourceKey string) protocol.ThreadMessage {
				cardType, state := protocol.CardTypeWechatExchange, protocol.CardStatePending
				return protocol.ThreadMessage{
					Direction: protocol.MessageDirectionOut, Kind: protocol.MessageKindCard,
					ContentHash: syncledger.WechatExchangeContentHash(), SourceKey: sourceKey,
					CardType: &cardType, CardState: &state,
				}
			},
		},
		{
			name: "interview invite", primitive: protocol.PrimChatSendInviteCard,
			args: protocol.ChatSendInviteCardArgs{
				ConversationRef: "conversation-card-verifier",
				Interview: protocol.InterviewDetails{
					StartsAt: 1_722_000_000_000, EndsAt: 1_722_001_800_000,
					Method: protocol.InterviewMethodWechatVideo,
				},
			},
			hash: syncledger.InterviewInviteContentHash(
				1_722_000_000_000, 1_722_001_800_000, "wechatVideo",
			),
			cardType: "interviewInvite", cardState: "unknown",
			message: func(sourceKey string) protocol.ThreadMessage {
				cardType, state := protocol.CardTypeInterviewInvite, protocol.CardStateUnknown
				interview := protocol.InterviewDetails{
					StartsAt: 1_722_000_000_000, EndsAt: 1_722_001_800_000,
					Method: protocol.InterviewMethodWechatVideo,
				}
				return protocol.ThreadMessage{
					Direction: protocol.MessageDirectionOut, Kind: protocol.MessageKindCard,
					ContentHash: syncledger.InterviewInviteContentHash(
						interview.StartsAt, interview.EndsAt, string(interview.Method),
					),
					SourceKey: sourceKey, CardType: &cardType, CardState: &state,
					Interview: &interview,
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d, st, sender, command, intentID, key, anchorHash :=
				newCardVerificationFixture(t, test.primitive, test.args, test.hash)
			sourceKey := strings.Repeat("c", 64)
			target := test.message(sourceKey)
			sender.mu.Lock()
			sender.autoPages, sender.anchorHash, sender.targetCard = true, anchorHash, &target
			sender.mu.Unlock()
			d.SetEffectVerifier(EffectVerifier{Dispatcher: d})
			d.OnResult("hand-verifier", "result-"+command.MsgID, verificationPossibleResult(command.MsgID))

			parent := waitVerificationStatus(t, st, command.MsgID, store.CmdOk)
			if parent.VerificationN != 0 {
				t.Fatalf("唯一正证不应计 miss: %+v", parent)
			}
			messages, err := st.MessagesForConversation(key)
			if err != nil || len(messages) != 2 {
				t.Fatalf("卡片验证应原子追加且不重复: messages=%+v err=%v", messages, err)
			}
			card := messages[1]
			if card.Kind != "card" || card.Direction != "out" ||
				card.CardType != test.cardType || card.CardState != test.cardState ||
				card.ContentHash != test.hash || card.SourceKey == nil || *card.SourceKey != sourceKey ||
				card.OutboundIntentID == nil || *card.OutboundIntentID != intentID {
				t.Fatalf("验证收编的卡片事实不完整: %+v", card)
			}
			intent, _ := st.EffectIntentByID(intentID)
			if intent == nil || intent.Status != store.EffectIntentOk ||
				intent.ResultMessageSeq == nil || *intent.ResultMessageSeq != card.Seq {
				t.Fatalf("卡片意图未与消息同事务终局: %+v", intent)
			}
			if err := protocol.ValidatePrimitiveResult(
				test.primitive, protocol.Primitives[test.primitive].Ver, []byte(parent.ResultBody),
			); err != nil {
				t.Fatalf("验证合成的卡片 result 不符合契约: %v", err)
			}
			reads := sender.readCalls()
			if len(reads) != 2 {
				t.Fatalf("应复用 readThread 分页读取锚与新卡: %+v", reads)
			}
			for _, read := range reads {
				if read.args.ConversationRef != key.ConversationRef ||
					!read.args.Window.Deep ||
					len(read.args.Window.AnchorTail) != 1 ||
					read.args.Window.AnchorTail[0].ContentHash != anchorHash {
					t.Fatalf("卡片验证读必须原样复用 conversationRef/expectedTail: %+v", read)
				}
			}
		})
	}
}

func newCardVerificationFixture(
	t *testing.T,
	primitive string,
	args any,
	fingerprint string,
) (
	*dispatch.Dispatcher,
	*store.Store,
	*verificationSender,
	store.CmdRecord,
	string,
	store.ConversationKey,
	string,
) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sender := &verificationSender{}
	d := dispatch.New(st, sender)
	sender.dispatcher = d

	const handID = "hand-verifier"
	key := store.ConversationKey{
		Platform: "zhilian", AccountRef: "account-card-verifier",
		ConversationRef: "conversation-card-verifier",
	}
	if err := st.CreateAccount(&store.Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccountPrincipal(
		store.AccountKey{Platform: key.Platform, AccountRef: key.AccountRef},
		handID, "principal-verifier", "session-verifier", "boot-verifier", time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveConversationList(store.SaveConversationListRequest{
		Platform: key.Platform, AccountRef: key.AccountRef, Complete: true,
		Entries: []store.ListIndexEntry{{
			ConversationRef: key.ConversationRef, PlatformUserRef: "candidate-card-verifier",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TrackConversation(key, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	history := "历史消息"
	anchorHash := syncledger.HashText(history)
	if _, err := st.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: 0, PlatformUserRef: "candidate-card-verifier", Adopt: true,
		NewMessages: []store.MessageDraft{{
			Direction: "in", Kind: "text", ContentHash: anchorHash, Text: &history, Origin: "external",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	argsRaw, err := protocol.Encode(args)
	if err != nil {
		t.Fatal(err)
	}
	guardsRaw, err := protocol.Encode(protocol.ChatSendMessageGuards{
		ExpectedTail: []protocol.MessageAnchor{{
			Direction: protocol.MessageDirectionIn, ContentHash: anchorHash,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	intentID := "intent-" + strings.ReplaceAll(primitive, ".", "-")
	msgID := "msg-" + strings.ReplaceAll(primitive, ".", "-")
	now := time.Now()
	created, err := st.CreateEffectIntentAndCmd(store.CreateEffectIntentRequest{
		Intent: store.EffectIntent{
			IntentID: intentID, IdemKey: "idem-" + intentID,
			Platform: key.Platform, AccountRef: key.AccountRef,
			Primitive: primitive, TargetRef: key.ConversationRef,
			PayloadHash: fingerprint, GuardsHash: "guards-hash",
			SendFingerprint: fingerprint, DeadlineMs: now.Add(time.Hour).UnixMilli(),
		},
		Command: store.CmdRecord{
			MsgID: msgID, Name: primitive, Class: string(protocol.ClassEffectful),
			IdemKey: "idem-" + intentID, Domain: key.Platform + ":" + key.AccountRef,
			Platform: key.Platform, AccountRef: key.AccountRef,
			ExpectedPrincipalFingerprint: "principal-verifier",
			ContextJSON: `{"platform":"zhilian","accountRef":"account-card-verifier",` +
				`"expectedPrincipalFingerprint":"principal-verifier"}`,
			Args: string(argsRaw), Guards: string(guardsRaw), IntentID: intentID,
			HandID: handID, Session: "session-verifier", BootIDAtDispatch: "boot-verifier",
			Status: store.CmdQueued, DeadlineMs: now.Add(time.Hour).UnixMilli(),
			ExecBudgetMs: 120_000, WitnessStoreIDAtDispatch: "witness-verifier",
		},
		ExpectedTailSeq: 1, Now: now,
	})
	if err != nil || !created.Created {
		t.Fatalf("创建卡片验证意图: created=%+v err=%v", created, err)
	}
	return d, st, sender, created.Command, intentID, key, anchorHash
}
