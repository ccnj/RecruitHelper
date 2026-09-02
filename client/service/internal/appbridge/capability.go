package appbridge

import (
	"errors"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/patrol"
	"recruithelper/contract/gen/go/protocol"
)

// capabilityMissing 识别"该平台没这条原语"的两种信号(2026-09-02 甲方裁决,hello 按
// 平台声明能力):
//
//  1. 脑闸 dispatch.ErrCapability —— 手声明了该平台表且表里没有,记账前就拒绝,
//     无 cmd_record、无手侧往返;
//  2. 手侧 result(failed, PROTO_UNSUPPORTED_CMD, retryable=no) —— 旧手未声明
//     platforms、或脑闸漏判时适配器 requireCapability 的运行期兜底,经 runErrorFromLeaf
//     到达时是 *patrol.RunError。
//
// 两种都要认:只认一种,升级过渡期会出现同一台机上一天跳过、下一天当失败。
// productapp 与 patrol 都不 import dispatch(appbridge 的职责就是不让两边互相依赖),
// 所以翻译只能落在本包。
func capabilityMissing(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, dispatch.ErrCapability) {
		return true
	}
	var typed *patrol.RunError
	if errors.As(err, &typed) && typed.Code == protocol.ErrCodeProtoUnsupportedCmd {
		return true
	}
	return false
}
