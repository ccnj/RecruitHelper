package dispatch

import (
	"encoding/json"
	"strings"

	"recruithelper/contract/gen/go/protocol"
)

// 命令的目标平台只有两个来源,优先级与手侧 platform/registry.ts 一致:
//
//  1. context.platform —— 一切业务命令;
//  2. args.platform    —— 刻意不带 context 的两条(probe.platform 绑定前探测、
//     debug.capturePage suspect 取证),脑在 args 里说出探哪个平台(571ff4b)。
//
// 都没有返回空串,调用方按"无平台"处理(能力闸按并集判、预算系数 1.0)。
// 只认非空字符串;args 解析失败视同没有——这里不是校验点,只是取键。

func commandPlatform(ctx *protocol.CmdContext, args json.RawMessage) string {
	if ctx != nil && strings.TrimSpace(ctx.Platform) != "" {
		return ctx.Platform
	}
	return argsPlatform(args)
}

// recordPlatform 是 commandPlatform 的账本版:CmdRecord.Platform 只从 context 落账,
// 无 context 命令的记录里为空,重投时要回落到 Args——首派与重投必须取到同一个平台,
// 否则同一条命令两次派发的系数与能力闸口径会不一致。
func recordPlatform(platform, args string) string {
	if strings.TrimSpace(platform) != "" {
		return platform
	}
	return argsPlatform(json.RawMessage(args))
}

func argsPlatform(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var hint struct {
		Platform string `json:"platform"`
	}
	if err := json.Unmarshal(args, &hint); err != nil {
		return ""
	}
	return strings.TrimSpace(hint.Platform)
}
