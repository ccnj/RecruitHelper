package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// ProbeInterviewEditorRequest 是邀面编辑器彩排(2026-07-29 甲方裁决)的派发
// 请求。它是 intrusive 命令:不铸 effect intent、不建消息基线、不取
// expectedTail;账号身份三项校验(绑定手在线、指纹、session/bootId 当前性)
// 与直发同款,但不要求会话已入库或已收编,携带账号上下文进账号串行域,与该
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

	// 彩排只要账号身份,不要会话账本(2026-08-04)。目标页面由手侧按
	// conversationRef——即平台 URL 的 sessionId——匹配人工已打开的标签页,
	// 命不中即 pageAbsent 失败,库里有没有这条会话对定位毫无帮助。此处因此
	// 不再借道发送轨的 PrepareSend:它附带的"会话已入库 / 已收编 / 有账本尾"
	// 三道门槛是发送准备的前提,与构造性不含发送路径的彩排无关,却让诊断台
	// 只能对已收编会话彩排。发送轨的同名调用与其门槛不受本改动影响。
	bound, err := d.currentBoundHand(req.Platform, req.AccountRef)
	if err != nil {
		return nil, err
	}

	return d.Run(ctx, bound.request(protocol.PrimDebugProbeInterviewEditor, argsRaw))
}

// boundHand 是一次诊断台直调所需的账号身份代际快照。
type boundHand struct {
	platform    string
	accountRef  string
	handID      string
	session     string
	bootID      string
	fingerprint string
}

func (b boundHand) request(name string, argsRaw json.RawMessage) DispatchRequest {
	return DispatchRequest{
		HandID:          b.handID,
		ExpectedSession: b.session,
		ExpectedBootID:  b.bootID,
		Name:            name,
		Args:            argsRaw,
		Context: &protocol.CmdContext{
			Platform:                     b.platform,
			AccountRef:                   b.accountRef,
			ExpectedPrincipalFingerprint: b.fingerprint,
		},
	}
}

// currentBoundHand 做账号身份三项校验(绑定手在线、指纹已定、session/bootId
// 仍是当前代际)并返回该代际快照。诊断台直调类命令共用:它们不铸 effect
// intent、不建消息基线、不取 expectedTail,要的只是"此刻这只手确实是这个
// 账号的手"。发送轨的 PrepareSend 另有会话账本门槛,不走这里。
func (d *Dispatcher) currentBoundHand(platform, accountRef string) (boundHand, error) {
	account, err := d.st.AccountByKey(store.AccountKey{Platform: platform, AccountRef: accountRef})
	if err != nil {
		return boundHand{}, err
	}
	if account == nil {
		return boundHand{}, store.ErrAccountNotFound
	}
	if account.BoundHandID == "" || account.PrincipalFingerprint == nil {
		return boundHand{}, store.ErrAccountIdentityNotCurrent
	}
	session, bootID, online := d.sender.HandSession(account.BoundHandID)
	if !online {
		return boundHand{}, ErrHandOffline
	}
	if account.IdentityState != store.IdentityVerified ||
		account.IdentitySession != session ||
		account.IdentityBootID != bootID {
		return boundHand{}, store.ErrAccountIdentityNotCurrent
	}
	return boundHand{
		platform:    platform,
		accountRef:  accountRef,
		handID:      account.BoundHandID,
		session:     session,
		bootID:      bootID,
		fingerprint: *account.PrincipalFingerprint,
	}, nil
}
