//go:build !windows

package secposture

import "context"

// 非 Windows(开发机)上采集整块不存在:Run 立即返回,Cached 恒 nil,
// 载荷 omitempty 让 security 块整体消失。这不是降级,是设计 —— 客户环境
// 全是 Windows,mac 上没有 Defender/MSRT 这些概念,报 unknown 只会制造噪音。
const collectSupported = false

func collectOnce(context.Context) *Posture { return nil }
