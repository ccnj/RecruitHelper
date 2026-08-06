// Package logcontext 给日志上报的事件补上下文:候选人姓名、职位名,以及该会话
// 最近若干条聊天正文(AGENTS.md「全局约定·日志上报」,2026-08-06 甲方裁决)。
//
// 它单独成包,是为了让 logreport 不必认识 store —— 依赖方向是
// logcontext → {logreport, store},上报器本身仍然只管队列与 HTTP。
//
// **两条硬边界,改这个文件前先读清楚:**
//
//  1. **只查这几样,别的一律不去取。** 姓名、职位名、最近若干条聊天正文,到此为止。
//     简历正文与简历快照、权威 ContactAsset 微信号、API key、ai-traces.db 的
//     provider 原文 —— 装配环节根本不去查它们。这是"不去取",不是"要滤掉":
//     日志行和聊天正文里已经有的手机号、微信号原样上传,不扫描、不做内容级脱敏。
//
//  2. **补出来的明文只进上报载荷,不回写日志行。** brain.log 不因为本包而多出
//     一个候选人姓名 —— 否则等于顺手把「普通日志」那条边界也改了,而那条边界
//     管着长期躺在客户机磁盘上、并随每日整包一起传的东西。
package logcontext

import (
	"encoding/json"
	"strings"

	"recruithelper/client/service/internal/logreport"
	"recruithelper/client/service/internal/store"
)

// RecentMessageLimit 是每条事件携带的聊天正文条数上限。
//
// 20 条是这么定的:排障要看的正是上下文 —— AI 为什么这么回、为什么判成 suspect、
// 候选人到底说了什么导致状态跳变 —— 一两条不够用;整会话又等于把 messages 表
// 往外倒,而且大多数行与本次故障无关。
const RecentMessageLimit = 20

// 事件 context 里已知的引用键。它们由打日志的调用点提供(slog attrs),
// 本包只认这几个,认不出就安静跳过 —— 补不上上下文不是故障,不该影响上报。
const (
	keyProfileID       = "profileId"
	keyMsgID           = "msgId"
	keyConversationRef = "conversationRef"
	keyPlatform        = "platform"
	keyAccountRef      = "accountRef"
)

// 补齐后写进 context 的键。
const (
	fieldCandidateName = "candidateName"
	fieldPositionTitle = "positionTitle"
	fieldMessages      = "recentMessages"
	fieldMessageCount  = "recentMessageCount"
)

// Source 是补齐需要的全部读取能力。**这份清单本身就是那道边界**:
// 它没有读简历、读 ContactAsset、读 ai-traces 的方法,所以"装配环节不去查隐私
// 字段"不是靠自律,是靠这个接口里根本没有那些入口。往这里加方法前先读包注释。
//
// *store.Store 天然满足它;抽成接口是为了测试能注入假数据源,不必建整套真实现场。
type Source interface {
	CandidateProfileByID(profileID string) (*store.CandidateProfile, error)
	CandidateProfileByConversation(key store.ConversationKey) (*store.CandidateProfile, error)
	CandidateByKey(key store.CandidateKey) (*store.Candidate, error)
	ConversationByKey(key store.ConversationKey) (*store.Conversation, error)
	RecentMessagesForConversation(key store.ConversationKey, limit int) ([]store.Message, error)
	CmdByMsgID(msgID string) (*store.CmdRecord, error)
}

// Enricher 用本地业务库补上下文。
type Enricher struct {
	store Source
	limit int
}

func New(source Source) *Enricher {
	return &Enricher{store: source, limit: RecentMessageLimit}
}

// Enrich 就地补齐一批事件。任何一条补不上都只是少点信息,不影响其余,也不影响上报。
func (e *Enricher) Enrich(items []logreport.Item) {
	if e == nil || e.store == nil {
		return
	}
	for index := range items {
		e.enrichOne(&items[index])
	}
}

func (e *Enricher) enrichOne(item *logreport.Item) {
	if item.Context == nil {
		return
	}
	profile := e.resolveProfile(item.Context)
	if profile == nil {
		return
	}
	if name := e.candidateName(profile); name != "" {
		item.Context[fieldCandidateName] = name
	}
	if profile.PositionTitle != nil && *profile.PositionTitle != "" {
		item.Context[fieldPositionTitle] = *profile.PositionTitle
	}
	if profile.ConversationRef == nil || *profile.ConversationRef == "" {
		return
	}
	key := store.ConversationKey{
		Platform:        profile.Platform,
		AccountRef:      profile.AccountRef,
		ConversationRef: *profile.ConversationRef,
	}
	if messages := e.recentMessages(key); len(messages) > 0 {
		item.Context[fieldMessages] = messages
		item.Context[fieldMessageCount] = len(messages)
	}
}

// resolveProfile 顺着事件里的引用找到候选人档案。三条入口,按可靠性排序:
// profileId 直接查;conversationRef 反查;msgId 走命令记录再从 args 里取会话。
func (e *Enricher) resolveProfile(context map[string]any) *store.CandidateProfile {
	if profileID := stringValue(context[keyProfileID]); profileID != "" {
		if profile, err := e.store.CandidateProfileByID(profileID); err == nil && profile != nil {
			return profile
		}
	}
	platform := stringValue(context[keyPlatform])
	accountRef := stringValue(context[keyAccountRef])
	if conversationRef := stringValue(context[keyConversationRef]); conversationRef != "" && platform != "" {
		if profile := e.profileByConversation(platform, accountRef, conversationRef); profile != nil {
			return profile
		}
	}
	// 命令类事件(suspect 就是)只带 msgId。命令记录上有 platform/accountRef,
	// 会话引用则藏在 args 原文里 —— 各原语的参数形状不同,只按键名取,取不到就算。
	if msgID := stringValue(context[keyMsgID]); msgID != "" {
		if cmd, err := e.store.CmdByMsgID(msgID); err == nil && cmd != nil {
			if ref := conversationRefFromArgs(cmd.Args); ref != "" {
				if profile := e.profileByConversation(cmd.Platform, cmd.AccountRef, ref); profile != nil {
					return profile
				}
			}
		}
	}
	return nil
}

func (e *Enricher) profileByConversation(platform, accountRef, conversationRef string) *store.CandidateProfile {
	profile, err := e.store.CandidateProfileByConversation(store.ConversationKey{
		Platform:        platform,
		AccountRef:      accountRef,
		ConversationRef: conversationRef,
	})
	if err != nil || profile == nil {
		return nil
	}
	return profile
}

// candidateName 取候选人姓名。先取候选人档案上的,没有再退到会话上的对端名。
func (e *Enricher) candidateName(profile *store.CandidateProfile) string {
	candidate, err := e.store.CandidateByKey(store.CandidateKey{
		Platform:        profile.Platform,
		PlatformUserRef: profile.PlatformUserRef,
	})
	if err == nil && candidate != nil && candidate.DisplayName != nil {
		if name := strings.TrimSpace(*candidate.DisplayName); name != "" {
			return name
		}
	}
	if profile.ConversationRef == nil {
		return ""
	}
	conversation, err := e.store.ConversationByKey(store.ConversationKey{
		Platform:        profile.Platform,
		AccountRef:      profile.AccountRef,
		ConversationRef: *profile.ConversationRef,
	})
	if err != nil || conversation == nil {
		return ""
	}
	return strings.TrimSpace(conversation.PeerDisplayName)
}

// wireMessage 是随事件带走的一条聊天记录。字段刻意只有三个:谁说的、说了什么、
// 什么时候。卡片、blob 引用、seq 这些排障用不上,不往外带。
type wireMessage struct {
	Direction string `json:"direction"`
	Text      string `json:"text"`
	AtMs      int64  `json:"atMs,omitempty"`
}

func (e *Enricher) recentMessages(key store.ConversationKey) []wireMessage {
	rows, err := e.store.RecentMessagesForConversation(key, e.limit)
	if err != nil || len(rows) == 0 {
		return nil
	}
	messages := make([]wireMessage, 0, len(rows))
	for _, row := range rows {
		if row.Text == nil || strings.TrimSpace(*row.Text) == "" {
			// 没有正文的行(纯卡片、图片)跳过:带一条空文本进去只是噪音。
			continue
		}
		message := wireMessage{Direction: row.Direction, Text: *row.Text}
		if row.TsApproxMs != nil {
			message.AtMs = *row.TsApproxMs
		}
		messages = append(messages, message)
	}
	if len(messages) == 0 {
		return nil
	}
	return messages
}

// conversationRefFromArgs 从原语 args 原文里取会话引用。
// 只按键名取顶层字段,不递归、不猜:取不到就是取不到,补不上上下文不是故障。
func conversationRefFromArgs(args string) string {
	if strings.TrimSpace(args) == "" {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		return ""
	}
	return stringValue(parsed[keyConversationRef])
}

func stringValue(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}
