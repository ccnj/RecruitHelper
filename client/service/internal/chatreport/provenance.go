package chatreport

// provenanceOf 推导一条出站消息的业务出身。三层线索按可信度取第一个命中：
// 沟通事件动作的 V4Kind 最细（coldPrompt/coldPromptFixed 催问、replyText、
// rejectionRetention 挽留等），AI 轮动作的 Kind 次之，最后按意图原语名归类
// （招呼与邀面卡不经沟通动作表）。
//
// 出站且无意图记 manual：账本里我方发送的行必带 OutboundIntentID（SX 终局同
// 事务追加），无意图的出站行是真人手发或被我方接管前的历史出站。这个归类对
// "被接管前对方招聘者发的历史行"会误记 manual——统计上把它当人工发送可接受，
// 它们本来也不是本系统发的。入站行没有出身，返回空串。
func provenanceOf(direction string, hasIntent bool, eventKind, actionKind, primitive string) string {
	if direction != "out" {
		return ""
	}
	if !hasIntent {
		return "manual"
	}
	if eventKind != "" {
		return eventKind
	}
	if actionKind != "" {
		return actionKind
	}
	// 原语名兜底。字符串与契约 contract.v1.json 对齐；这里只做上报标注，
	// 不参与任何业务裁决，对不上落 unknown 即可，不需要 codegen 依赖。
	switch primitive {
	case "chat.sendGreeting":
		return "greeting"
	case "chat.sendInviteCard":
		return "interviewInvite"
	case "chat.sendWechatInvite":
		return "inviteWechat"
	case "chat.acceptWechat":
		return "acceptWechat"
	}
	return "unknown"
}
