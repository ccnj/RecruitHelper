package store

// 本文件是三道派发闸对"平台系统家具行"读侧容忍的单点谓词(Q7,2026-08-03
// 甲方裁决)。真机事故:智联往会话里插"可以直接给Ta打电话"等系统提示(我方
// 账本 direction=system, kind=system),没有任何业务认领它们,投影游标永远
// 追不平账本尾,链首平价闸与链中父对齐闸把候选人整个冻死(真机 8 人冻结、
// 13 人待爆)。
//
// 判据是双条件,不是白名单。一行消息对派发闸"透明"(可越过)当且仅当:
//   (1) direction=='system' 且 kind=='system';
//   (2) 不属于"已知正常业务在用的 system 形状"黑名单——该黑名单今天为空
//       (2026-08-03 全库核对:业务路径全部把 system 行排除在输入之外,只有
//       一次性恢复 CLI 和排除性判断引用它)。未来某个业务真要启用某种
//       system 形状时,在 communicationDispatchSystemShapeClaimed 里按形状
//       登记,不得在各闸各自加条件。
//
// retracted 行视同透明:它已被更强证据推翻,不属于活动账本。
// in 行、out 行永不透明。
//
// 已接受的代价(裁决原文):带业务语义却落入 system 保守分支的行——148 拒绝
// 快捷回复的边缘形态、"[系统消息:99]"可能的微信被拒/拒收、352 敏感包装真人
// 消息——会被越过。正道是上游分类修复(已另案);催的打扰上界由 v4 预算定理
// 兜底。
//
// 红线:本文件只放宽脑侧读闸。WAL/idemKey/证词/发后正证/ExpectedTailSeq/
// guards 零改动(派发时传给手的 expectedTail 仍是真实账本尾,手照常对页面
// 核验);游标数值零改写。

import (
	"log/slog"

	"gorm.io/gorm"
)

// communicationDispatchSystemShapeClaimed 是判据第 (2) 条的黑名单钩子:
// 已知正常业务在用的 system 形状。今天为空;未来业务启用某 system 形状时在
// 此按形状(不是按具体文本)登记,登记后该形状不再透明。届时若判定需要正文
// 等更多列,同步扩 communicationDispatchTransparentSuffixTx 的 Select。
func communicationDispatchSystemShapeClaimed(Message) bool {
	return false
}

// communicationDispatchTransparentRow 判定单行消息对派发闸是否透明。
func communicationDispatchTransparentRow(message Message) bool {
	if message.RetractedAt != nil {
		return true
	}
	return message.Direction == "system" && message.Kind == "system" &&
		!communicationDispatchSystemShapeClaimed(message)
}

// communicationDispatchTransparentSuffixTx 判定 (afterSeq, throughSeq] 是否
// 为透明后缀:区间内不存在"非透明且未撤回"的行。一次索引查询(messages
// 复合主键前缀 platform+account_ref+conversation_ref+seq)。afterSeq >=
// throughSeq 不构成后缀问题,一律判不透明,由调用闸按原判据处置。透明时按
// 裁决在此单点记 Info 日志(conversationRef 与越过的行数),gate 标注是哪道
// 闸放的行。
// communicationV4ScheduleTailFreshTx 是时刻表侧(plan 冻结/AI 预留/36h 归档)
// 的账本尾新鲜度判定(C5,2026-08-27 甲方裁决):游标追平活动尾且会话尾计数
// 一致时直接新鲜;不齐时,(游标, 会话尾] 全为无主 system/已撤回行也放行——
// 消除"派发闸有 Q7 透明后缀容忍、时刻表无"的不对称,无主家具行不再卡住
// 催问与归档。判的问题不变:世界在评估快照之后没有长出业务上有意义的新行。
func communicationV4ScheduleTailFreshTx(
	tx *gorm.DB,
	gate string,
	platform string,
	accountRef string,
	conversationRef string,
	cursor int64,
	activeTail int64,
	lastMessageSeq int64,
) (bool, error) {
	if activeTail == cursor && lastMessageSeq == activeTail {
		return true, nil
	}
	return communicationDispatchTransparentSuffixTx(
		tx, gate, platform, accountRef, conversationRef, cursor, lastMessageSeq,
	)
}

func communicationDispatchTransparentSuffixTx(
	tx *gorm.DB,
	gate string,
	platform string,
	accountRef string,
	conversationRef string,
	afterSeq int64,
	throughSeq int64,
) (bool, error) {
	if afterSeq >= throughSeq {
		return false, nil
	}
	var rows []Message
	if err := tx.
		Select("seq", "direction", "kind", "retracted_at").
		Where(
			"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq > ? AND seq <= ?",
			platform,
			accountRef,
			conversationRef,
			afterSeq,
			throughSeq,
		).
		Find(&rows).Error; err != nil {
		return false, err
	}
	for index := range rows {
		if !communicationDispatchTransparentRow(rows[index]) {
			return false, nil
		}
	}
	slog.Info("派发闸越过无主 system/已撤回后缀",
		"gate", gate,
		"conversationRef", conversationRef,
		"afterSeq", afterSeq,
		"throughSeq", throughSeq,
		"skippedRows", len(rows),
	)
	return true, nil
}
