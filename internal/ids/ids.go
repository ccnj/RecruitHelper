// Package ids:协议标识符生成。msgId/session/token 是不透明串,接收方不得解析其内容;
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

// NewToken:配对令牌,128 位随机;明文只在 welcome 下发一次,脑侧只存哈希。
func NewToken() string { return randHex(16) }
