package dispatch

import (
	"encoding/json"
	"math"
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

// 预算按平台系数(2026-09-02 甲方裁决,批 D 2.3):契约里的 execBudgetMs/deadlineMs 按智联的
// 页面内交互标定;BOSS 走 OS 注入,一次点击含移动 3～5 秒(第四段验收报告实测),多点击原语
// 翻倍起步,真机标定后再调。不改契约、不加字段:命令帧本就带 execBudgetMs,手侧照帧执行。
//
// 三条规则:
//  1. 预算封顶 480000——CmdBody.execBudgetMs 的契约上限(contract.v1.json,Go 侧无具名常量,
//     此处镜像并由用例钉住;不要用 DefaultExecBudgetDefaultMsCapMs=240000,它自 08-26 起
//     只作手侧 execMs 诊断钳制,当上限会把 readList/readThread 的智联预算砍回一半);
//  2. 期限同乘、不封顶——契约里 13 条原语的 deadline 不足 2×预算,手侧定时器取
//     min(deadline, budget),只放预算不放期限会让手先报 expired;
//  3. lease 不同比——它是活性心跳不是时长预算,脑侧本就钳到期限(08-26 先例)。
//
// 失效方向不变:系数只挪动"本轮未就绪"的时点,不产生第二次点击;取不到系数一律 1.0。
var platformBudgetFactor = map[string]float64{
	"boss": 2.0,
}

// execBudgetEnvelopeMaxMs 镜像 contract.v1.json CmdBody.execBudgetMs.maximum。
const execBudgetEnvelopeMaxMs int64 = 480000

func platformFactor(platform string) float64 {
	if factor, ok := platformBudgetFactor[platform]; ok && factor > 0 {
		return factor
	}
	return 1.0
}

func scaleBudgetMs(base int64, platform string) int64 {
	scaled := int64(math.Round(float64(base) * platformFactor(platform)))
	if scaled > execBudgetEnvelopeMaxMs {
		return execBudgetEnvelopeMaxMs
	}
	return scaled
}

func scaleDeadlineMs(base int64, platform string) int64 {
	return int64(math.Round(float64(base) * platformFactor(platform)))
}
