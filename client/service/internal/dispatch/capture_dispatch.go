package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// ProbeCaptureRequest 是诊断台运营通知彩排的取证截图派发请求
// (AGENTS.md「运营通知 webhook」2026-08-06 增补的第三类触发)。
//
// 与巡检里的同名取证(m7_capture_patrol.go)派发的是字面同一个原语,区别只在
// 触发方与落账:巡检那条为发件箱里某条待发通知取证,拍到就落 CandidateScreenshot
// 事实行;彩排这条谁也不为,拍到的字节直接回给调用方发出去,一行都不写库——
// 落了库线上通知就会挑到这张彩排图。
type ProbeCaptureRequest struct {
	Platform        string
	AccountRef      string
	ConversationRef string
	// PlatformUserRef 只有简历截图需要;聊天截图留空。
	PlatformUserRef string
	Resume          bool
}

// ProbeCaptureScreenshot 派发一次取证截图并等待终局,成功时返回图像引用。
// 它是 intrusive 命令:不铸 effect intent、不落 WAL、不碰证词与幂等,只借
// 账号串行域与该账号的巡检/发送命令互斥,不会与它们抢同一个页面现场。
// 返回的 state 即便在失败路径上也尽量非 nil,供调用方回显 errorCode。
func (d *Dispatcher) ProbeCaptureScreenshot(
	ctx context.Context,
	req ProbeCaptureRequest,
) (protocol.CaptureScreenshotData, *store.LogicalDispatchState, error) {
	var zero protocol.CaptureScreenshotData
	req.Platform = strings.TrimSpace(req.Platform)
	req.AccountRef = strings.TrimSpace(req.AccountRef)
	req.ConversationRef = strings.TrimSpace(req.ConversationRef)
	req.PlatformUserRef = strings.TrimSpace(req.PlatformUserRef)
	if req.Platform == "" || req.AccountRef == "" || req.ConversationRef == "" {
		return zero, nil, errors.New("缺少有效的账号/会话标识")
	}

	name := protocol.PrimChatCaptureThreadScreenshot
	var args any = protocol.ChatCaptureThreadScreenshotArgs{ConversationRef: req.ConversationRef}
	if req.Resume {
		if req.PlatformUserRef == "" {
			return zero, nil, errors.New("简历截图缺少候选人平台标识")
		}
		name = protocol.PrimCandidateCaptureResumeScreenshot
		args = protocol.CandidateCaptureResumeScreenshotArgs{
			ConversationRef: req.ConversationRef,
			PlatformUserRef: req.PlatformUserRef,
		}
	}
	meta := protocol.Primitives[name]
	argsRaw, err := protocol.Encode(args)
	if err != nil {
		return zero, nil, err
	}
	if err := protocol.ValidatePrimitiveArgs(name, meta.Ver, argsRaw); err != nil {
		return zero, nil, err
	}

	// 与邀面彩排同款:只要账号身份,不要会话账本。目标页面由手侧按
	// conversationRef(平台 URL 的 sessionId)匹配人工已打开的标签页。
	bound, err := d.currentBoundHand(req.Platform, req.AccountRef)
	if err != nil {
		return zero, nil, err
	}
	state, err := d.Run(ctx, bound.request(name, argsRaw))
	if err != nil {
		return zero, state, err
	}
	data, err := captureData(name, meta.Ver, state)
	return data, state, err
}

// captureData 从终局叶命令里抽出截图数据并按契约校验。
func captureData(
	name string,
	ver int,
	logical *store.LogicalDispatchState,
) (protocol.CaptureScreenshotData, error) {
	var zero protocol.CaptureScreenshotData
	if logical == nil || !logical.Settled {
		return zero, errors.New("截图命令未终局")
	}
	leaf := logical.Leaf
	if leaf.Name != name || leaf.Status != store.CmdOk || leaf.ResultBody == "" {
		return zero, errors.New("截图未取得成功终局")
	}
	resultRaw := json.RawMessage(leaf.ResultBody)
	if err := protocol.ValidatePrimitiveResult(name, ver, resultRaw); err != nil {
		return zero, errors.New("截图结果不符合契约")
	}
	var result protocol.ResultBody
	if err := json.Unmarshal(resultRaw, &result); err != nil ||
		result.Ref != leaf.MsgID || result.Status != protocol.ResultStatusOk {
		return zero, errors.New("截图结果关联无效")
	}
	var data protocol.CaptureScreenshotData
	if err := json.Unmarshal(result.Data, &data); err != nil {
		return zero, errors.New("截图数据无法解析")
	}
	return data, nil
}
