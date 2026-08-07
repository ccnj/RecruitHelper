package dispatch

// 招呼命令转 suspect 后的现场截图取证(2026-08-07 甲方裁决立案,契约增量
// debug.capturePage@1)。定位:suspect 的固定文案分不清撞每日上限、平台拒绝
// 还是弹窗卡死;一闪而过的平台浮层原话由手侧发送轮询顺带捕获(随错误
// message),这里补的是"赖着不走"的形态——卡死的弹窗、异形的页面,截一帧
// 留在同机 blobs,供远程协助时人工查看。
//
// 三条边界:
//  1. 只取证,不裁决。结果只落 SuspectSceneShot 事实行与审计,不参与任何
//     业务判定;任何失败即弃,不重试、不转人工。
//  2. 不阻塞批次。异步 goroutine + 独立超时;suspect 冻结的只有该成员自身
//     (2026-08-05 甲方裁决),截图不给批次添任何等待理由。
//  3. 不出站。截图字节是本地业务证据资产,诊断包白名单硬排除 blobs/。

import (
	"context"
	"encoding/json"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// suspectSceneCaptureTimeout 覆盖 debug.capturePage 的 30s deadline 加派发余量;
// 到点即弃,不追问结果。
const suspectSceneCaptureTimeout = 45 * time.Second

// suspectSceneCaptureWanted 列出转 suspect 后要留现场截图的原语。当前只有
// 招呼:它的失败形态有真机证据(2026-08-06 弹窗卡死连累后续 11 人被
// USER_ACTIVE 跳过);其他原语等真机需要再扩,不预置。
func suspectSceneCaptureWanted(name string) bool {
	return name == protocol.PrimChatSendGreeting
}

func (d *Dispatcher) captureSuspectScene(cmd store.CmdRecord) {
	ctx, cancel := context.WithTimeout(context.Background(), suspectSceneCaptureTimeout)
	defer cancel()
	args, err := protocol.Encode(protocol.DebugCapturePageArgs{})
	if err != nil {
		return
	}
	state, err := d.Run(ctx, DispatchRequest{
		HandID: cmd.HandID, Name: protocol.PrimDebugCapturePage, Args: args,
	})
	if err != nil {
		d.st.Audit("suspect_scene_capture_failed", cmd.HandID, cmd.MsgID,
			"现场截图未得: "+err.Error())
		return
	}
	data, reason := suspectSceneData(state.Leaf)
	if data == nil {
		d.st.Audit("suspect_scene_capture_failed", cmd.HandID, cmd.MsgID,
			"现场截图未得: "+reason)
		return
	}
	if err := d.st.SaveSuspectSceneShot(cmd.MsgID, cmd.IntentID, cmd.Name,
		data.ImageBlobRef, data.ByteSize, data.CapturedAt, time.Now()); err != nil {
		d.st.Audit("suspect_scene_capture_failed", cmd.HandID, cmd.MsgID,
			"现场截图落账失败: "+err.Error())
		return
	}
	// blobRef 是内容寻址引用,不是图像内容,可进审计。
	d.st.Audit("suspect_scene_captured", cmd.HandID, cmd.MsgID, "blobRef="+data.ImageBlobRef)
}

// suspectSceneData 从截图命令的终局 leaf 取出截图引用;拿不到时返回一句
// 不含图像内容的原因,供审计。
func suspectSceneData(leaf store.CmdRecord) (*protocol.CaptureScreenshotData, string) {
	if leaf.Status != store.CmdOk || leaf.ResultBody == "" {
		reason := string(leaf.Status)
		if leaf.ErrorCode != "" {
			reason += "/" + leaf.ErrorCode
		}
		return nil, reason
	}
	var result protocol.ResultBody
	if err := json.Unmarshal([]byte(leaf.ResultBody), &result); err != nil {
		return nil, "result 解析失败"
	}
	var data protocol.CaptureScreenshotData
	if len(result.Data) == 0 || json.Unmarshal(result.Data, &data) != nil || data.ImageBlobRef == "" {
		return nil, "result 缺少截图引用"
	}
	return &data, ""
}
