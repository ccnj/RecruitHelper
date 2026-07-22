// Package ids:协议标识符生成。msgId/session 等是不透明串,接收方不得解析其内容;
// 前缀仅为人读日志辨认,无语义。两端(脑与假手)共用,故置于顶层 internal。
package ids

import (
	"crypto/rand"
	"encoding/hex"
)

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand 不可用: " + err.Error()) // 熵源失效属不可恢复
	}
	return hex.EncodeToString(b)
}

// NewMsgID:消息逻辑身份,全局唯一,重传不变。
func NewMsgID() string { return "m-" + randHex(12) }

// NewSessionID:脑在 welcome 中分配。
func NewSessionID() string { return "s-" + randHex(12) }

// NewBootID:手每次 SW 出生新生成(手记忆连续性指示器);假手启动时生成。
func NewBootID() string { return "b-" + randHex(8) }

// NewAccountRef:脑为本地平台账号签发的稳定不透明引用。它不编码平台 userId，
// 同一引用必须与 platform 维度共同使用。
func NewAccountRef() string { return "a-" + randHex(8) }

// NewProfileID:脑为候选人×职位档案签发的稳定不透明引用。
// 它不编码平台、账号、候选人或职位信息。
func NewProfileID() string { return "p-" + randHex(12) }

func NewResumeSnapshotID() string { return "rs-" + randHex(12) }

func NewSourcingRunID() string { return "sr-" + randHex(12) }

func NewTrialSelectionID() string { return "ts-" + randHex(12) }

func NewAIContextBindingID() string { return "cb-" + randHex(12) }
