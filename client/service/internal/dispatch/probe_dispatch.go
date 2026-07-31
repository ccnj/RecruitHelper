package dispatch

import (
	"context"
	"errors"
	"strings"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// ProbeInterviewEditorRequest 是邀面编辑器彩排(2026-07-29 甲方裁决)的派发
// 请求。它是 intrusive 命令:不铸 effect intent、不建消息基线、不取
// expectedTail;账号身份反查与直发同款,携带账号上下文进账号串行域,与该
// 账号的巡检/发送命令互斥,彩排弹窗不会与其他命令并发操作同一页面。
type ProbeInterviewEditorRequest struct {
	Platform        string
	AccountRef      string
	ConversationRef string
	Interview       protocol.InterviewDetails
}

// ProbeInterviewEditor 派发 debug.probeInterviewEditor 并等待逻辑终局。
// 手侧与 chat.sendInviteCard 字面共用同一编辑器准备实现,构造性不含发送
// 路径;本方法同样不触碰 WAL/idemKey/正证任何发送轨状态。
func (d *Dispatcher) ProbeInterviewEditor(
	ctx context.Context,
	req ProbeInterviewEditorRequest,
) (*store.LogicalDispatchState, error) {
	req.Platform = strings.TrimSpace(req.Platform)
	req.AccountRef = strings.TrimSpace(req.AccountRef)
	req.ConversationRef = strings.TrimSpace(req.ConversationRef)
	if req.Platform == "" || req.AccountRef == "" || req.ConversationRef == "" {
		return nil, errors.New("缺少有效的账号/会话标识")
	}
	// 形态按 method 分支(2026-07-31 甲方裁决):线上仍是 startsAt+30 分钟的
	// 微信视频;现场面试在平台上没有时长控件,endsAt 必须缺席而不得合成。
	if req.Interview.StartsAt <= 0 {
		return nil, errors.New("邀面参数必须给出正的 startsAt")
	}
	switch req.Interview.Method {
	case protocol.InterviewMethodWechatVideo:
		if req.Interview.EndsAt != req.Interview.StartsAt+communication.V4InterviewDurationMs {
			return nil, errors.New("微信视频彩排的 endsAt 必须是 startsAt+30 分钟")
		}
	case protocol.InterviewMethodOnsite:
		if req.Interview.EndsAt != 0 {
			return nil, errors.New("现场面试彩排不得携带 endsAt")
		}
	default:
		return nil, errors.New("彩排面试方式只开放 wechatVideo 与 onsite")
	}
	argsRaw, err := protocol.Encode(protocol.DebugProbeInterviewEditorArgs{
		ConversationRef: req.ConversationRef,
		Interview:       req.Interview,
	})
	if err != nil {
		return nil, err
	}
	meta := protocol.Primitives[protocol.PrimDebugProbeInterviewEditor]
	if err := protocol.ValidatePrimitiveArgs(
		protocol.PrimDebugProbeInterviewEditor, meta.Ver, argsRaw,
	); err != nil {
		return nil, err
	}

	preparation, err := d.st.PrepareSend(store.ConversationKey{
		Platform: req.Platform, AccountRef: req.AccountRef, ConversationRef: req.ConversationRef,
	}, 1)
	if err != nil {
		return nil, err
	}
	if preparation.Account.BoundHandID == "" || preparation.Account.PrincipalFingerprint == nil {
		return nil, store.ErrAccountIdentityNotCurrent
	}
	session, bootID, online := d.sender.HandSession(preparation.Account.BoundHandID)
	if !online {
		return nil, ErrHandOffline
	}
	if preparation.Account.IdentityState != store.IdentityVerified ||
		preparation.Account.IdentitySession != session ||
		preparation.Account.IdentityBootID != bootID {
		return nil, store.ErrAccountIdentityNotCurrent
	}

	return d.Run(ctx, DispatchRequest{
		HandID:          preparation.Account.BoundHandID,
		ExpectedSession: session,
		ExpectedBootID:  bootID,
		Name:            protocol.PrimDebugProbeInterviewEditor,
		Args:            argsRaw,
		Context: &protocol.CmdContext{
			Platform:                     req.Platform,
			AccountRef:                   req.AccountRef,
			ExpectedPrincipalFingerprint: *preparation.Account.PrincipalFingerprint,
		},
	})
}
