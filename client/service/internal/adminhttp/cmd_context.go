package adminhttp

import (
	"encoding/json"
	"strings"
	"time"

	"recruithelper/client/service/internal/store"
)

// 命令行的上下文投影，suspect 队列与命令账本共用。
//
// 裁决 resolvedOk/resolvedFailed 是替平台事实下结论：判 resolvedFailed 而其实
// 已经发出去，后续补发就是实打实多发一条给真人。所以裁决的人必须先看清这条
// 到底是谁、发的什么、卡在哪一步——只给一个原语英文名等于让人靠猜。账本同理：
// 一串 msgId 加原语名，看不出"刚才对谁做了什么、花了多久"。
//
// 数据一直在 CmdRecord 那一行里（args/guards/resultBody/平台/账号/意图/时刻），
// 此前只是没投影出来。按 AGENTS.md「开发者诊断台明文边界」(2026-07-31)，
// /admin/* 可以返回本地业务库的全部业务明文，因此这里不做逐字段白名单，
// 原文一并给出；摘要字段只为可读性挑重点，不承担过滤职责。

// 原语名 → 中文动作名。命中不了就回退原名，不猜。
var suspectActionNames = map[string]string{
	"chat.sendMessage":      "发消息",
	"chat.sendGreeting":     "发招呼",
	"chat.sendWechatInvite": "发换微信邀请",
	"chat.sendInviteCard":   "发邀面卡",
	"chat.acceptWechat":     "接受换微信",
	"job.publishDraft":      "发布职位",
	"job.takeOffline":       "下线职位",
	"debug.slowEcho":        "调试回声",
}

// 原因串大多本来就是中文，只有少数几处行话需要翻。不做封闭枚举映射：
// SuspectReason 是各处自由拼装的，硬套枚举只会把没覆盖到的显示成空白。
var suspectReasonPhrases = map[string]string{
	"未找到 expectedTail": "回读会话最近窗口没找到这条消息",
}

const verificationExhaustedPrefix = "verification exhausted: "

func humanizeSuspectReason(reason string) string {
	inner := reason
	prefix := ""
	if strings.HasPrefix(reason, verificationExhaustedPrefix) {
		inner = strings.TrimPrefix(reason, verificationExhaustedPrefix)
		prefix = "发后核验做完仍无法确认："
	}
	if phrase, ok := suspectReasonPhrases[inner]; ok {
		inner = phrase
	}
	return prefix + inner
}

func suspectActionName(primitive string) string {
	if name, ok := suspectActionNames[primitive]; ok {
		return name
	}
	return primitive
}

// cmdArgsFacts 是从 cmd.args 里认得出来的几个通用字段。
type cmdArgsFacts struct {
	ConversationRef string // 会话类原语的目标
	Text            string // 发送类原语的正文
	JobName         string // 职位类原语的目标
}

// Summary 给人一句话看清"这条在干什么"：会话类看正文，职位类看职位名。
func (f cmdArgsFacts) Summary() string {
	if f.Text != "" {
		return f.Text
	}
	return f.JobName
}

// argsFacts 通用解析而非逐原语分支：新增原语只要沿用同名字段就自动可读，
// 取不到就留空，由前端的"原始现场"折叠区兜底。
func argsFacts(argsJSON string) cmdArgsFacts {
	var facts cmdArgsFacts
	if strings.TrimSpace(argsJSON) == "" {
		return facts
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return facts
	}
	if v, ok := args["conversationRef"].(string); ok {
		facts.ConversationRef = v
	}
	if v, ok := args["text"].(string); ok {
		facts.Text = v
	}
	if v, ok := args["jobName"].(string); ok {
		facts.JobName = v
	}
	return facts
}

// cmdTarget 回答"这条命令作用在谁/什么上"。会话类优先给候选人名字——账本是
// 用来扫的，一串 conversationRef 认不出人；名字查不到才退回引用本身。
func (a *API) cmdTarget(rec store.CmdRecord, facts cmdArgsFacts) string {
	if facts.ConversationRef != "" {
		if name := a.peerDisplayNameFor(rec, facts.ConversationRef); name != "" {
			return name
		}
		return facts.ConversationRef
	}
	return facts.JobName
}

// unixMilliOrZero：零值时间的 UnixMilli 是个很大的负数，直接送到前端会显示成
// 一个上古日期。统一回 0，前端的 toDate 把 0 当"无"处理。
func unixMilliOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// terminalMillis：命令未终局时 TerminalAt 是 nil，回 0 表示"还没结束"，
// 前端据此显示"进行中"而不是算出一个荒谬的耗时。
func terminalMillis(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return unixMilliOrZero(*t)
}

// peerDisplayNameFor 把 conversationRef 换成候选人名字。查不到（会话尚未投影、
// 或该原语本来就没有会话，如 job.publishDraft）就返回空串，前端降级为不显示。
func (a *API) peerDisplayNameFor(rec store.CmdRecord, conversationRef string) string {
	if conversationRef == "" || rec.Platform == "" || rec.AccountRef == "" {
		return ""
	}
	conv, err := a.st.ConversationByKey(store.ConversationKey{
		Platform: rec.Platform, AccountRef: rec.AccountRef, ConversationRef: conversationRef,
	})
	if err != nil || conv == nil {
		return ""
	}
	return conv.PeerDisplayName
}
