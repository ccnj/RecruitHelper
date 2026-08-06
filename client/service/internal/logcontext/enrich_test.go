package logcontext

import (
	"encoding/json"
	"fmt"
	"testing"

	"recruithelper/client/service/internal/logreport"
	"recruithelper/client/service/internal/store"
)

// fakeSource 是假数据源。它顺带记下每个方法被调过几次 —— 有一条测试要靠这个
// 证明"没查到档案时不会白跑几趟库"。
type fakeSource struct {
	profilesByID   map[string]*store.CandidateProfile
	profilesByConv map[store.ConversationKey]*store.CandidateProfile
	candidates     map[store.CandidateKey]*store.Candidate
	conversations  map[store.ConversationKey]*store.Conversation
	messages       map[store.ConversationKey][]store.Message
	cmds           map[string]*store.CmdRecord

	messageLimitSeen int
}

func (f *fakeSource) CandidateProfileByID(profileID string) (*store.CandidateProfile, error) {
	return f.profilesByID[profileID], nil
}

func (f *fakeSource) CandidateProfileByConversation(key store.ConversationKey) (*store.CandidateProfile, error) {
	return f.profilesByConv[key], nil
}

func (f *fakeSource) CandidateByKey(key store.CandidateKey) (*store.Candidate, error) {
	return f.candidates[key], nil
}

func (f *fakeSource) ConversationByKey(key store.ConversationKey) (*store.Conversation, error) {
	return f.conversations[key], nil
}

func (f *fakeSource) RecentMessagesForConversation(key store.ConversationKey, limit int) ([]store.Message, error) {
	f.messageLimitSeen = limit
	rows := f.messages[key]
	if len(rows) > limit {
		rows = rows[len(rows)-limit:]
	}
	return rows, nil
}

func (f *fakeSource) CmdByMsgID(msgID string) (*store.CmdRecord, error) {
	return f.cmds[msgID], nil
}

func seeded(messageCount int) (*fakeSource, string, store.ConversationKey) {
	key := store.ConversationKey{Platform: "zhilian", AccountRef: "acct-1", ConversationRef: "conv-1"}
	profile := &store.CandidateProfile{
		ProfileID: "p-1", Platform: key.Platform, AccountRef: key.AccountRef,
		PlatformUserRef: "user-1",
		PositionTitle:   strPtr("平安健康保障顾问"),
		ConversationRef: strPtr(key.ConversationRef),
	}
	source := &fakeSource{
		profilesByID:   map[string]*store.CandidateProfile{"p-1": profile},
		profilesByConv: map[store.ConversationKey]*store.CandidateProfile{key: profile},
		candidates: map[store.CandidateKey]*store.Candidate{
			{Platform: key.Platform, PlatformUserRef: "user-1"}: {DisplayName: strPtr("张三")},
		},
		conversations: map[store.ConversationKey]*store.Conversation{
			key: {PeerDisplayName: "张三(会话上的名字)"},
		},
		messages: map[store.ConversationKey][]store.Message{},
		cmds:     map[string]*store.CmdRecord{},
	}
	rows := make([]store.Message, 0, messageCount)
	for index := 1; index <= messageCount; index++ {
		direction := "in"
		if index%2 == 0 {
			direction = "out"
		}
		text := fmt.Sprintf("第 %d 条消息", index)
		at := int64(1_754_400_000_000 + index*60_000)
		rows = append(rows, store.Message{
			Seq: int64(index), Direction: direction, Kind: "text",
			Text: &text, TsApproxMs: &at,
		})
	}
	source.messages[key] = rows
	return source, "p-1", key
}

func TestEnrichFillsNameTitleAndMessagesByProfileID(t *testing.T) {
	source, profileID, _ := seeded(3)

	items := []logreport.Item{{
		EventType: logreport.EventSuspectCreated,
		Message:   "命令转 suspect",
		Context:   map[string]any{"profileId": profileID},
	}}
	New(source).Enrich(items)

	context := items[0].Context
	if context[fieldCandidateName] != "张三" {
		t.Fatalf("候选人姓名没补上: %+v", context)
	}
	if context[fieldPositionTitle] != "平安健康保障顾问" {
		t.Fatalf("职位名没补上: %+v", context)
	}
	messages, ok := context[fieldMessages].([]wireMessage)
	if !ok || len(messages) != 3 {
		t.Fatalf("聊天正文没补上: %+v", context[fieldMessages])
	}
	if messages[0].Text == "" || messages[0].Direction == "" || messages[0].AtMs == 0 {
		t.Fatalf("正文缺方向、内容或时刻: %+v", messages[0])
	}
	if context[fieldMessageCount] != 3 {
		t.Fatalf("条数不对: %+v", context[fieldMessageCount])
	}
}

func TestEnrichCapsMessageCount(t *testing.T) {
	// 整会话往外倒是明确要挡住的:排障要的是出事前后那一段，不是全部历史。
	source, profileID, _ := seeded(RecentMessageLimit + 15)

	items := []logreport.Item{{Context: map[string]any{"profileId": profileID}}}
	New(source).Enrich(items)

	if source.messageLimitSeen != RecentMessageLimit {
		t.Fatalf("查库时就该带上限，实际传了 %d", source.messageLimitSeen)
	}
	messages := items[0].Context[fieldMessages].([]wireMessage)
	if len(messages) > RecentMessageLimit {
		t.Fatalf("正文条数超过上限 %d，实得 %d", RecentMessageLimit, len(messages))
	}
}

func TestEnrichResolvesThroughCommandRecord(t *testing.T) {
	// suspect 事件只带 msgId。命令记录上有 platform/accountRef，会话引用藏在
	// args 原文里 —— 这条路径决定了本功能最初的立案场景读不读得懂。
	source, _, key := seeded(2)
	args, _ := json.Marshal(map[string]any{"conversationRef": key.ConversationRef})
	source.cmds["cmd-1"] = &store.CmdRecord{
		MsgID: "cmd-1", Name: "chat.sendMessage",
		Platform: key.Platform, AccountRef: key.AccountRef, Args: string(args),
	}

	items := []logreport.Item{{Context: map[string]any{"msgId": "cmd-1"}}}
	New(source).Enrich(items)

	if items[0].Context[fieldCandidateName] != "张三" {
		t.Fatalf("经命令记录反查失败: %+v", items[0].Context)
	}
	if items[0].Context[fieldPositionTitle] != "平安健康保障顾问" {
		t.Fatalf("职位名没补上: %+v", items[0].Context)
	}
}

func TestEnrichFallsBackToConversationPeerName(t *testing.T) {
	// 候选人档案上没有姓名时退到会话上的对端名，总比什么都不显示强。
	source, profileID, _ := seeded(1)
	source.candidates = map[store.CandidateKey]*store.Candidate{}

	items := []logreport.Item{{Context: map[string]any{"profileId": profileID}}}
	New(source).Enrich(items)

	if items[0].Context[fieldCandidateName] != "张三(会话上的名字)" {
		t.Fatalf("没退到会话对端名: %+v", items[0].Context)
	}
}

func TestEnrichSkipsMessagesWithoutText(t *testing.T) {
	// 纯卡片、图片行没有正文，带一条空文本进去只是噪音。
	source, profileID, key := seeded(0)
	empty := ""
	text := "有正文的一条"
	source.messages[key] = []store.Message{
		{Seq: 1, Direction: "in", Kind: "card", Text: nil},
		{Seq: 2, Direction: "in", Kind: "text", Text: &empty},
		{Seq: 3, Direction: "out", Kind: "text", Text: &text},
	}

	items := []logreport.Item{{Context: map[string]any{"profileId": profileID}}}
	New(source).Enrich(items)

	messages := items[0].Context[fieldMessages].([]wireMessage)
	if len(messages) != 1 || messages[0].Text != text {
		t.Fatalf("只该带上有正文的那条: %+v", messages)
	}
}

func TestEnrichNeverTouchesForbiddenFields(t *testing.T) {
	// 这条守的是裁决的硬边界:装配环节**不去查**简历、结构化微信号、provider 原文。
	// 是"不去取"，不是"取了再滤掉"。Source 接口里根本没有那些方法，这条测试
	// 再确认一遍补出来的字段清单没有意外膨胀。
	source, profileID, _ := seeded(3)

	items := []logreport.Item{{Context: map[string]any{"profileId": profileID}}}
	New(source).Enrich(items)

	allowed := map[string]bool{
		"profileId": true, fieldCandidateName: true, fieldPositionTitle: true,
		fieldMessages: true, fieldMessageCount: true,
	}
	for key := range items[0].Context {
		if !allowed[key] {
			t.Fatalf("补出了预期之外的字段 %s: %+v", key, items[0].Context)
		}
	}
}

func TestEnrichIsSilentWhenNothingResolves(t *testing.T) {
	// 补不上上下文不是故障，不该影响上报本身。
	source, _, _ := seeded(1)
	items := []logreport.Item{
		{Context: map[string]any{"profileId": "不存在"}},
		{Context: map[string]any{"msgId": "不存在"}},
		{Context: nil},
	}
	New(source).Enrich(items)

	if _, exists := items[0].Context[fieldCandidateName]; exists {
		t.Fatal("查不到的档案不该补出姓名")
	}
	if items[2].Context != nil {
		t.Fatal("没有 context 的事件不该被凭空造出字段")
	}
}

func strPtr(value string) *string { return &value }
