package syncledger

// SentText 是「实发正文即事实」(2026-09-07 甲方裁决,AGENTS 防护成本预算第 9 条同名段)
// 在脑侧的唯一计算点:发送族 result / 验证读带回实发正文时,账本行以实发正文落账,
// 脑不再核对实发正文与计划正文的关系,只留一条含双方 hash 与字数的审计留痕。
//
// 收成一个函数的理由写在这里,免得日后各处自己比:计划正文与实发正文从此是两个
// 事实——意图上的 SendFingerprint 永远是计划正文的指纹(HTTP 幂等重试、多气泡
// 父子链、招呼生成记录绑定都靠它);消息行上的 ContentHash 是实发正文的指纹
// (验证读、自举、上报、AI 下一轮都读它)。任何新代码若默认两者相等就会悄悄错,
// 请经这里取值。
type SentText struct {
	// Text 是应落账本行的正文:手带回实发正文即实发正文,否则就是计划正文。
	Text string
	// Hash 是 Text 的 §4.5 规范哈希。
	Hash string
	// PlannedHash 是计划正文的规范哈希,只作审计比对。
	PlannedHash string
	// Differs 为真表示实发正文与计划正文规范化后不同,调用方应留痕(不记正文)。
	Differs bool
}

// ResolveSentText 按 planned(命令 args.text)与 reported(result data.sentText,可空)
// 得出账本应记的正文与哈希。reported 为空即"手没有带回实发正文",按计划正文处理——
// 程序化写入的平台(智联)恒走这一支。
func ResolveSentText(planned, reported string) SentText {
	plannedHash := HashText(planned)
	if reported == "" {
		return SentText{Text: planned, Hash: plannedHash, PlannedHash: plannedHash}
	}
	hash := HashText(reported)
	return SentText{Text: reported, Hash: hash, PlannedHash: plannedHash, Differs: hash != plannedHash}
}

// SentTextUsable 判 result 带回的实发正文本身是否可用:规范化后非空。
// 空白串在契约 minLength 之内却没有内容,不能当正文落账。
func SentTextUsable(reported string) bool {
	return reported == "" || NormalizeText(reported) != ""
}
