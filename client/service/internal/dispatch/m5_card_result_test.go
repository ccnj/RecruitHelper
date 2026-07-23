package dispatch

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

func seedCardEffectIntent(
	t *testing.T,
	st *store.Store,
	key store.ConversationKey,
	primitive string,
	args any,
	fingerprint string,
	expectedTail int64,
) (*store.EffectIntent, *store.CmdRecord) {
	t.Helper()
	argsRaw, err := protocol.Encode(args)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	intentID := "intent-" + strings.ReplaceAll(primitive, ".", "-")
	msgID := "msg-" + strings.ReplaceAll(primitive, ".", "-")
	idemKey := "idem-" + intentID
	intent := store.EffectIntent{
		IntentID: intentID, IdemKey: idemKey,
		Platform: key.Platform, AccountRef: key.AccountRef,
		Primitive: primitive, TargetRef: key.ConversationRef,
		PayloadHash: fingerprint, GuardsHash: "guards-hash",
		SendFingerprint: fingerprint, DeadlineMs: now.Add(time.Hour).UnixMilli(),
	}
	command := store.CmdRecord{
		MsgID: msgID, Name: primitive, Class: string(protocol.ClassEffectful),
		IdemKey: idemKey, Domain: key.Platform + ":" + key.AccountRef,
		Platform: key.Platform, AccountRef: key.AccountRef,
		ExpectedPrincipalFingerprint: "opaque-fp-" + key.AccountRef,
		ContextJSON: `{"platform":"zhilian","accountRef":"` + key.AccountRef +
			`","expectedPrincipalFingerprint":"opaque-fp-` + key.AccountRef + `"}`,
		Args: string(argsRaw), Guards: `{"expectedTail":[{"direction":"in","contentHash":"anchor"}]}`,
		IntentID: intentID, HandID: "hand-send", Session: "s-test", BootIDAtDispatch: "boot-send",
		Status: store.CmdQueued, DeadlineMs: now.Add(time.Hour).UnixMilli(),
		ExecBudgetMs: 60_000, WitnessStoreIDAtDispatch: "witness-store-1",
	}
	created, err := st.CreateEffectIntentAndCmd(store.CreateEffectIntentRequest{
		Intent: intent, Command: command, ExpectedTailSeq: expectedTail, Now: now,
	})
	if err != nil {
		t.Fatalf("创建卡片意图: %v", err)
	}
	if !created.Created {
		t.Fatal("卡片意图应首次创建")
	}
	return &created.Intent, &created.Command
}

func validWechatCardResult(ref, conversationRef, sourceKey string) protocol.ResultBody {
	data, _ := protocol.Encode(protocol.ChatSendWechatInviteData{
		ConversationRef: conversationRef,
		ContentHash:     syncledger.WechatExchangeContentHash(),
		SourceKey:       sourceKey,
		ObservedAt:      time.Now().UnixMilli(),
	})
	return protocol.ResultBody{
		Ref: ref, Status: protocol.ResultStatusOk, Data: data,
		Evidence: []protocol.Evidence{{
			Type: string(protocol.SendWechatInviteEvidenceTypeOutboundWechatInviteObserved),
		}},
	}
}

func validInviteCardResult(
	ref, conversationRef, sourceKey string,
	interview protocol.InterviewDetails,
) protocol.ResultBody {
	data, _ := protocol.Encode(protocol.ChatSendInviteCardData{
		ConversationRef: conversationRef,
		ContentHash: syncledger.InterviewInviteContentHash(
			interview.StartsAt, interview.EndsAt, string(interview.Method),
		),
		SourceKey: sourceKey, Interview: interview, ObservedAt: time.Now().UnixMilli(),
	})
	return protocol.ResultBody{
		Ref: ref, Status: protocol.ResultStatusOk, Data: data,
		Evidence: []protocol.Evidence{{
			Type: string(protocol.SendInviteCardEvidenceTypeOutboundInterviewInviteObserved),
		}},
	}
}

func TestCardResultsAtomicallyCreateBusinessFacts(t *testing.T) {
	t.Run("wechat invite", func(t *testing.T) {
		d, st, hand := newDisp(t)
		key := seedSendTarget(t, st, hand, "acct-card-wechat", "conv-card-wechat")
		hash := syncledger.WechatExchangeContentHash()
		_, command := seedCardEffectIntent(
			t, st, key, protocol.PrimChatSendWechatInvite,
			protocol.ChatSendWechatInviteArgs{ConversationRef: key.ConversationRef},
			hash, 1,
		)
		sourceKey := strings.Repeat("a", 64)
		outcome, _, err := d.applyResultMessage(
			"hand-send", "result-card-wechat",
			validWechatCardResult(command.MsgID, key.ConversationRef, sourceKey),
		)
		if err != nil || outcome != ocDone {
			t.Fatalf("换微信卡 result 入账失败: outcome=%v err=%v", outcome, err)
		}
		assertCardBusinessFact(t, st, key, command.IntentID, "wechatExchange", "pending",
			hash, sourceKey, nil)
	})

	t.Run("interview invite", func(t *testing.T) {
		d, st, hand := newDisp(t)
		key := seedSendTarget(t, st, hand, "acct-card-interview", "conv-card-interview")
		interview := protocol.InterviewDetails{
			StartsAt: 1_722_000_000_000, EndsAt: 1_722_001_800_000,
			Method: protocol.InterviewMethodWechatVideo,
		}
		hash := syncledger.InterviewInviteContentHash(
			interview.StartsAt, interview.EndsAt, string(interview.Method),
		)
		_, command := seedCardEffectIntent(
			t, st, key, protocol.PrimChatSendInviteCard,
			protocol.ChatSendInviteCardArgs{ConversationRef: key.ConversationRef, Interview: interview},
			hash, 1,
		)
		sourceKey := strings.Repeat("b", 64)
		outcome, _, err := d.applyResultMessage(
			"hand-send", "result-card-interview",
			validInviteCardResult(command.MsgID, key.ConversationRef, sourceKey, interview),
		)
		if err != nil || outcome != ocDone {
			t.Fatalf("邀面卡 result 入账失败: outcome=%v err=%v", outcome, err)
		}
		assertCardBusinessFact(t, st, key, command.IntentID, "interviewInvite", "unknown",
			hash, sourceKey, &interview)
	})
}

func assertCardBusinessFact(
	t *testing.T,
	st *store.Store,
	key store.ConversationKey,
	intentID, cardType, cardState, contentHash, sourceKey string,
	interview *protocol.InterviewDetails,
) {
	t.Helper()
	messages, err := st.MessagesForConversation(key)
	if err != nil || len(messages) != 2 {
		t.Fatalf("卡片结果应追加唯一消息: messages=%+v err=%v", messages, err)
	}
	card := messages[1]
	if card.Direction != "out" || card.Kind != "card" ||
		card.CardType != cardType || card.CardState != cardState ||
		card.ContentHash != contentHash || card.Origin != "self" ||
		card.SourceKey == nil || *card.SourceKey != sourceKey ||
		card.OutboundIntentID == nil || *card.OutboundIntentID != intentID {
		t.Fatalf("卡片业务事实错误: %+v", card)
	}
	if interview == nil {
		if card.InterviewStartsAtMs != nil || card.InterviewEndsAtMs != nil || card.InterviewMethod != nil {
			t.Fatalf("换微信卡不得携带邀面参数: %+v", card)
		}
	} else if card.InterviewStartsAtMs == nil || *card.InterviewStartsAtMs != interview.StartsAt ||
		card.InterviewEndsAtMs == nil || *card.InterviewEndsAtMs != interview.EndsAt ||
		card.InterviewMethod == nil || *card.InterviewMethod != string(interview.Method) {
		t.Fatalf("邀面参数未完整持久化: %+v", card)
	}
	intent, err := st.EffectIntentByID(intentID)
	if err != nil || intent == nil || intent.Status != store.EffectIntentOk ||
		intent.ResultMessageSeq == nil || *intent.ResultMessageSeq != card.Seq {
		t.Fatalf("卡片意图未与消息同事务终局: intent=%+v err=%v", intent, err)
	}
}

func TestCardResultStrictValidationFailsClosedWithoutAppending(t *testing.T) {
	tests := []struct {
		name      string
		primitive string
		args      any
		hash      string
		mutate    func(*protocol.ResultBody)
	}{
		{
			name: "wechat wrong conversation", primitive: protocol.PrimChatSendWechatInvite,
			args: protocol.ChatSendWechatInviteArgs{ConversationRef: "conv-strict"},
			hash: syncledger.WechatExchangeContentHash(),
			mutate: func(result *protocol.ResultBody) {
				var data protocol.ChatSendWechatInviteData
				_ = json.Unmarshal(result.Data, &data)
				data.ConversationRef = "other"
				result.Data, _ = protocol.Encode(data)
			},
		},
		{
			name: "wechat uppercase source key", primitive: protocol.PrimChatSendWechatInvite,
			args: protocol.ChatSendWechatInviteArgs{ConversationRef: "conv-strict"},
			hash: syncledger.WechatExchangeContentHash(),
			mutate: func(result *protocol.ResultBody) {
				var data protocol.ChatSendWechatInviteData
				_ = json.Unmarshal(result.Data, &data)
				data.SourceKey = strings.Repeat("A", 64)
				result.Data, _ = protocol.Encode(data)
			},
		},
		{
			name: "wechat wrong content hash", primitive: protocol.PrimChatSendWechatInvite,
			args: protocol.ChatSendWechatInviteArgs{ConversationRef: "conv-strict"},
			hash: syncledger.WechatExchangeContentHash(),
			mutate: func(result *protocol.ResultBody) {
				var data protocol.ChatSendWechatInviteData
				_ = json.Unmarshal(result.Data, &data)
				data.ContentHash = strings.Repeat("c", 64)
				result.Data, _ = protocol.Encode(data)
			},
		},
		{
			name: "evidence duplicate", primitive: protocol.PrimChatSendWechatInvite,
			args: protocol.ChatSendWechatInviteArgs{ConversationRef: "conv-strict"},
			hash: syncledger.WechatExchangeContentHash(),
			mutate: func(result *protocol.ResultBody) {
				result.Evidence = append(result.Evidence, result.Evidence[0])
			},
		},
		{
			name: "evidence payload forbidden", primitive: protocol.PrimChatSendWechatInvite,
			args: protocol.ChatSendWechatInviteArgs{ConversationRef: "conv-strict"},
			hash: syncledger.WechatExchangeContentHash(),
			mutate: func(result *protocol.ResultBody) {
				result.Evidence[0].Text = "clicked"
			},
		},
		{
			name: "invite params mismatch", primitive: protocol.PrimChatSendInviteCard,
			args: protocol.ChatSendInviteCardArgs{
				ConversationRef: "conv-strict",
				Interview: protocol.InterviewDetails{
					StartsAt: 1000, EndsAt: 2000, Method: protocol.InterviewMethodWechatVideo,
				},
			},
			hash: syncledger.InterviewInviteContentHash(1000, 2000, "wechatVideo"),
			mutate: func(result *protocol.ResultBody) {
				var data protocol.ChatSendInviteCardData
				_ = json.Unmarshal(result.Data, &data)
				data.Interview.EndsAt = 3000
				result.Data, _ = protocol.Encode(data)
			},
		},
		{
			name: "invite non increasing time", primitive: protocol.PrimChatSendInviteCard,
			args: protocol.ChatSendInviteCardArgs{
				ConversationRef: "conv-strict",
				Interview: protocol.InterviewDetails{
					StartsAt: 2000, EndsAt: 1000, Method: protocol.InterviewMethodWechatVideo,
				},
			},
			hash: syncledger.InterviewInviteContentHash(2000, 1000, "wechatVideo"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d, st, hand := newDisp(t)
			key := seedSendTarget(t, st, hand, "acct-"+strings.ReplaceAll(test.name, " ", "-"), "conv-strict")
			_, command := seedCardEffectIntent(t, st, key, test.primitive, test.args, test.hash, 1)
			sourceKey := strings.Repeat("d", 64)
			var result protocol.ResultBody
			switch args := test.args.(type) {
			case protocol.ChatSendWechatInviteArgs:
				result = validWechatCardResult(command.MsgID, args.ConversationRef, sourceKey)
			case protocol.ChatSendInviteCardArgs:
				result = validInviteCardResult(command.MsgID, args.ConversationRef, sourceKey, args.Interview)
			default:
				t.Fatalf("未知测试 args %T", test.args)
			}
			if test.mutate != nil {
				test.mutate(&result)
			}
			outcome, _, err := d.applyResultMessage(
				"hand-send", "result-"+strings.ReplaceAll(test.name, " ", "-"), result,
			)
			if err != nil || outcome != ocEffSuspect {
				t.Fatalf("畸形卡片成功必须降为 possible: outcome=%v err=%v", outcome, err)
			}
			messages, _ := st.MessagesForConversation(key)
			if len(messages) != 1 {
				t.Fatalf("畸形卡片结果不得追加消息: %+v", messages)
			}
			intent, _ := st.EffectIntentByID(command.IntentID)
			cmd, _ := st.CmdByMsgID(command.MsgID)
			if intent.Status != store.EffectIntentVerifying || cmd.Status != store.CmdVerifying ||
				cmd.ErrorCode != string(protocol.ErrCodeInternalHand) ||
				cmd.SideEffect != string(protocol.SideEffectPossible) {
				t.Fatalf("畸形卡片结果未 fail-closed: intent=%+v cmd=%+v", intent, cmd)
			}
		})
	}
}

func TestCardResultAdoptsExistingSourceKeyWithoutDuplicate(t *testing.T) {
	d, st, hand := newDisp(t)
	key := seedSendTarget(t, st, hand, "acct-card-adopt", "conv-card-adopt")
	sourceKey := strings.Repeat("e", 64)
	hash := syncledger.WechatExchangeContentHash()
	if _, err := st.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: 1,
		NewMessages: []store.MessageDraft{{
			Direction: "out", Kind: "card", ContentHash: hash,
			CardType: "wechatExchange", CardState: "pending",
			Origin: "external", SourceKey: &sourceKey,
		}},
		SyncedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	_, command := seedCardEffectIntent(
		t, st, key, protocol.PrimChatSendWechatInvite,
		protocol.ChatSendWechatInviteArgs{ConversationRef: key.ConversationRef},
		hash, 2,
	)
	outcome, _, err := d.applyResultMessage(
		"hand-send", "result-card-adopt",
		validWechatCardResult(command.MsgID, key.ConversationRef, sourceKey),
	)
	if err != nil || outcome != ocDone {
		t.Fatalf("已有同源卡片应被收编: outcome=%v err=%v", outcome, err)
	}
	messages, _ := st.MessagesForConversation(key)
	if len(messages) != 2 || messages[1].OutboundIntentID == nil ||
		*messages[1].OutboundIntentID != command.IntentID || messages[1].Origin != "self" {
		t.Fatalf("同 sourceKey 不得重复追加且必须绑定意图: %+v", messages)
	}
}
