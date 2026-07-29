package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

// 2026-07-29「成功=发后页面可见」裁决(docs/发后核验成功判据裁决-2026-07-29.md):
// 消息与卡片的发后核验只浅读一页最近窗口,不再做 expectedTail 锚对齐、深分页
// 与历史到顶判定。成功判据 = 方向/内容与本次动作一致 + 服务器时间不早于派发
// 时刻减时钟容差;同文多条取满足条件的最新一条。失败标志由手侧行映射承担:
// out 行非 success 在手内直接判读取失败,不会出现在窗口里。零匹配只落未确认,
// 由 miss 计数走向 suspect 转人工,禁止自动重发。
const (
	verificationRecentWindow     = 16
	verificationClockToleranceMs = 5_000
)

// EffectVerifier 通过每个真实 SX metadata 指定的正式读取原语消解歧义。
// 它只比较 generated 结构化字段，不解析 evidence/DOM。
type EffectVerifier struct {
	Dispatcher *dispatch.Dispatcher
}

func (v EffectVerifier) Verify(ctx context.Context, req dispatch.VerificationRequest) (dispatch.VerificationObservation, error) {
	if v.Dispatcher == nil {
		return dispatch.VerificationObservation{}, errors.New("dispatcher 不能为空")
	}
	switch req.Command.Name {
	case protocol.PrimChatSendMessage:
		return v.verifySendMessage(ctx, req)
	case protocol.PrimChatSendWechatInvite, protocol.PrimChatSendInviteCard:
		return v.verifyCard(ctx, req)
	case protocol.PrimChatAcceptWechat:
		return v.verifyAcceptWechat(ctx, req)
	case protocol.PrimChatSendGreeting:
		return v.verifyGreeting(ctx, req)
	default:
		return dispatch.VerificationObservation{}, errors.New("验证请求不是已支持的真实副作用意图")
	}
}

func (v EffectVerifier) verifyAcceptWechat(
	ctx context.Context,
	req dispatch.VerificationRequest,
) (dispatch.VerificationObservation, error) {
	if req.Command.Name != protocol.PrimChatAcceptWechat ||
		req.AcceptWechatArgs == nil ||
		req.AcceptWechatArgs.ConversationRef == "" ||
		req.AcceptWechatArgs.RequestSourceKey == "" {
		return dispatch.VerificationObservation{},
			errors.New("验证请求不是完整 chat.acceptWechat 意图")
	}
	argsRaw, err := protocol.Encode(
		protocol.ChatReadWechatExchangeOutcomeArgs{
			ConversationRef:  req.AcceptWechatArgs.ConversationRef,
			RequestSourceKey: req.AcceptWechatArgs.RequestSourceKey,
		},
	)
	if err != nil {
		return dispatch.VerificationObservation{}, err
	}
	state, err := v.Dispatcher.RunVerificationRead(
		ctx,
		req.Command.MsgID,
		dispatch.DispatchRequest{
			HandID: req.Command.HandID,
			Name:   protocol.PrimChatReadWechatExchangeOutcome,
			Args:   argsRaw,
			Context: &protocol.CmdContext{
				Platform:                     req.Command.Platform,
				AccountRef:                   req.Command.AccountRef,
				ExpectedPrincipalFingerprint: req.Command.ExpectedPrincipalFingerprint,
			},
		},
	)
	if err != nil {
		return dispatch.VerificationObservation{}, err
	}
	dataRaw, err := resultData(state.Leaf)
	if err != nil {
		return dispatch.VerificationObservation{}, err
	}
	var data protocol.ChatReadWechatExchangeOutcomeData
	if err := json.Unmarshal(dataRaw, &data); err != nil {
		return dispatch.VerificationObservation{},
			fmt.Errorf("解析验证 readWechatExchangeOutcome: %w", err)
	}
	if !data.Confirmed {
		return dispatch.VerificationObservation{
			Reason: "本轮未取得同一请求的唯一微信交换正证",
		}, nil
	}
	fingerprint, err := store.AcceptWechatFingerprint(
		req.AcceptWechatArgs.RequestSourceKey,
	)
	if err != nil {
		return dispatch.VerificationObservation{}, err
	}
	return dispatch.VerificationObservation{
		Confirmed:   true,
		ContentHash: fingerprint,
		SourceKey:   data.ExchangeSourceKey,
		PeerWechat:  data.PeerWechat,
		ObservedAt:  data.ObservedAt,
		Reason:      "同一微信请求后唯一命中交换完成正证",
	}, nil
}

func (v EffectVerifier) verifySendMessage(ctx context.Context, req dispatch.VerificationRequest) (dispatch.VerificationObservation, error) {
	if req.Command.Name != protocol.PrimChatSendMessage || req.Args.ConversationRef == "" {
		return dispatch.VerificationObservation{}, errors.New("验证请求不是完整 chat.sendMessage 意图")
	}
	dispatchedAtMs, err := dispatchReferenceMs(req)
	if err != nil {
		return dispatch.VerificationObservation{}, err
	}
	window, err := v.readRecentWindow(ctx, req, req.Args.ConversationRef)
	if err != nil {
		return dispatch.VerificationObservation{}, err
	}
	return classifyVerifiedSend(window, req.Intent.SendFingerprint, dispatchedAtMs)
}

// readRecentWindow 浅读一页最近消息。不携带 anchorTail、不开 deep、不消费
// cursor:页面可见判据只关心"刚发生"的窗口,分页完整性与历史到顶都不再是
// 成功前提;窗口读取失败按原语失败沿 miss→suspect 收敛。
func (v EffectVerifier) readRecentWindow(
	ctx context.Context,
	req dispatch.VerificationRequest,
	conversationRef string,
) ([]protocol.ThreadMessage, error) {
	args := protocol.ChatReadThreadArgs{
		ConversationRef: conversationRef,
		Window:          protocol.ThreadWindow{MaxMessages: verificationRecentWindow},
	}
	argsRaw, err := protocol.Encode(args)
	if err != nil {
		return nil, err
	}
	state, err := v.Dispatcher.RunVerificationRead(ctx, req.Command.MsgID, dispatch.DispatchRequest{
		HandID: req.Command.HandID, Name: protocol.PrimChatReadThread, Args: argsRaw,
		Context: &protocol.CmdContext{
			Platform: req.Command.Platform, AccountRef: req.Command.AccountRef,
			ExpectedPrincipalFingerprint: req.Command.ExpectedPrincipalFingerprint,
		},
	})
	if err != nil {
		return nil, err
	}
	dataRaw, err := resultData(state.Leaf)
	if err != nil {
		return nil, err
	}
	var data protocol.ChatReadThreadData
	if err := json.Unmarshal(dataRaw, &data); err != nil {
		return nil, fmt.Errorf("解析验证 readThread: %w", err)
	}
	return data.Messages, nil
}

func (v EffectVerifier) verifyCard(
	ctx context.Context,
	req dispatch.VerificationRequest,
) (dispatch.VerificationObservation, error) {
	var (
		conversationRef string
		targetHash      string
		interview       *protocol.InterviewDetails
	)
	switch req.Command.Name {
	case protocol.PrimChatSendWechatInvite:
		if req.WechatInviteArgs == nil || req.WechatInviteArgs.ConversationRef == "" {
			return dispatch.VerificationObservation{}, errors.New("验证请求不是完整 chat.sendWechatInvite 意图")
		}
		conversationRef = req.WechatInviteArgs.ConversationRef
		targetHash = syncledger.WechatExchangeContentHash()
	case protocol.PrimChatSendInviteCard:
		if req.InviteCardArgs == nil || req.InviteCardArgs.ConversationRef == "" ||
			req.InviteCardArgs.Interview.StartsAt <= 0 ||
			req.InviteCardArgs.Interview.EndsAt <= req.InviteCardArgs.Interview.StartsAt ||
			req.InviteCardArgs.Interview.Method != protocol.InterviewMethodWechatVideo {
			return dispatch.VerificationObservation{}, errors.New("验证请求不是完整 chat.sendInviteCard 意图")
		}
		conversationRef = req.InviteCardArgs.ConversationRef
		value := req.InviteCardArgs.Interview
		interview = &value
		targetHash = syncledger.InterviewInviteContentHash(
			value.StartsAt, value.EndsAt, string(value.Method),
		)
	default:
		return dispatch.VerificationObservation{}, errors.New("验证请求不是卡片意图")
	}
	dispatchedAtMs, err := dispatchReferenceMs(req)
	if err != nil {
		return dispatch.VerificationObservation{}, err
	}
	window, err := v.readRecentWindow(ctx, req, conversationRef)
	if err != nil {
		return dispatch.VerificationObservation{}, err
	}
	return classifyVerifiedCard(window, req.Command.Name, targetHash, interview, dispatchedAtMs)
}

func (v EffectVerifier) verifyGreeting(ctx context.Context, req dispatch.VerificationRequest) (dispatch.VerificationObservation, error) {
	if req.GreetingArgs == nil || req.Command.Name != protocol.PrimChatSendGreeting ||
		req.GreetingArgs.PlatformUserRef == "" || req.GreetingArgs.PositionRef == "" ||
		req.Intent.SendFingerprint == "" {
		return dispatch.VerificationObservation{}, errors.New("验证请求不是完整 chat.sendGreeting 意图")
	}
	argsRaw, err := protocol.Encode(protocol.ChatReadGreetingOutcomeArgs{
		PlatformUserRef: req.GreetingArgs.PlatformUserRef,
		PositionRef:     req.GreetingArgs.PositionRef,
		ContentHash:     req.Intent.SendFingerprint,
	})
	if err != nil {
		return dispatch.VerificationObservation{}, err
	}
	state, err := v.Dispatcher.RunVerificationRead(ctx, req.Command.MsgID, dispatch.DispatchRequest{
		HandID: req.Command.HandID, Name: protocol.PrimChatReadGreetingOutcome, Args: argsRaw,
		Context: &protocol.CmdContext{
			Platform: req.Command.Platform, AccountRef: req.Command.AccountRef,
			ExpectedPrincipalFingerprint: req.Command.ExpectedPrincipalFingerprint,
		},
	})
	if err != nil {
		return dispatch.VerificationObservation{}, err
	}
	dataRaw, err := resultData(state.Leaf)
	if err != nil {
		return dispatch.VerificationObservation{}, err
	}
	var data protocol.ChatReadGreetingOutcomeData
	if err := json.Unmarshal(dataRaw, &data); err != nil {
		return dispatch.VerificationObservation{}, fmt.Errorf("解析验证 readGreetingOutcome: %w", err)
	}
	if !data.Confirmed {
		return dispatch.VerificationObservation{Reason: "本轮未取得同一目标的关系已建立正证"}, nil
	}
	return dispatch.VerificationObservation{
		Confirmed: true, ContentHash: data.ContentHash, ConversationRef: data.ConversationRef,
		ObservedAt: data.ObservedAt, Reason: "同一候选人和职位的可见关系状态已建立",
	}, nil
}

// dispatchReferenceMs 取"本次派发"的时间参照:优先命令最近一次进入 sent 的
// 时刻。安全重投同 msgId 的前提是前次已证明零副作用,取最近一次仍是正确下界。
// 结果先于发送闭环落库的路径可能尚未记录 SentAt,退回意图创建时刻——它只会
// 更早,窗口只放宽不收窄,仍排除派发前已存在的历史同文消息。
func dispatchReferenceMs(req dispatch.VerificationRequest) (int64, error) {
	if req.Command.SentAt != nil && !req.Command.SentAt.IsZero() {
		return req.Command.SentAt.UnixMilli(), nil
	}
	if !req.Intent.CreatedAt.IsZero() {
		return req.Intent.CreatedAt.UnixMilli(), nil
	}
	return 0, errors.New("验证缺少派发时刻参照")
}

// withinDispatchWindow 判定消息服务器时间落在"本次派发"窗口:不早于派发时刻
// 减 5 秒时钟容差(2026-07-29 甲方裁决),往晚不设界。时间缺失按不合格处理:
// 失效方向是少认(→复读→suspect 转人工),不是误认旧消息。
func withinDispatchWindow(message protocol.ThreadMessage, dispatchedAtMs int64) bool {
	return message.TsApprox != nil && *message.TsApprox > 0 &&
		*message.TsApprox >= dispatchedAtMs-verificationClockToleranceMs
}

func classifyVerifiedSend(
	messages []protocol.ThreadMessage,
	targetHash string,
	dispatchedAtMs int64,
) (dispatch.VerificationObservation, error) {
	if targetHash == "" || dispatchedAtMs <= 0 {
		return dispatch.VerificationObservation{}, errors.New("验证分类缺少目标指纹或派发时刻")
	}
	var matched *protocol.ThreadMessage
	for i := range messages {
		message := messages[i]
		if message.Direction == protocol.MessageDirectionOut &&
			message.Kind == protocol.MessageKindText &&
			message.ContentHash == targetHash &&
			withinDispatchWindow(message, dispatchedAtMs) {
			matched = &messages[i]
		}
	}
	if matched == nil {
		return dispatch.VerificationObservation{
			Reason: "最近窗口未见时间容差内的目标 out/text 指纹",
		}, nil
	}
	return dispatch.VerificationObservation{
		Confirmed: true, ContentHash: matched.ContentHash, ObservedAt: observedAt(*matched),
		Reason: "最近窗口命中目标 out/text 指纹(同文取最新)",
	}, nil
}

func classifyVerifiedCard(
	messages []protocol.ThreadMessage,
	primitive string,
	targetHash string,
	expectedInterview *protocol.InterviewDetails,
	dispatchedAtMs int64,
) (dispatch.VerificationObservation, error) {
	if primitive != protocol.PrimChatSendWechatInvite && primitive != protocol.PrimChatSendInviteCard {
		return dispatch.VerificationObservation{}, errors.New("卡片验证原语非法")
	}
	if targetHash == "" || dispatchedAtMs <= 0 {
		return dispatch.VerificationObservation{}, errors.New("卡片验证分类缺少目标指纹或派发时刻")
	}
	var matched *protocol.ThreadMessage
	for i := range messages {
		message := messages[i]
		if message.Direction != protocol.MessageDirectionOut ||
			message.Kind != protocol.MessageKindCard ||
			message.ContentHash != targetHash ||
			!validOpaqueSourceKey(message.SourceKey) ||
			message.CardType == nil || message.CardState == nil ||
			!withinDispatchWindow(message, dispatchedAtMs) {
			continue
		}
		switch primitive {
		case protocol.PrimChatSendWechatInvite:
			if *message.CardType == protocol.CardTypeWechatExchange &&
				*message.CardState == protocol.CardStatePending &&
				message.Interview == nil {
				matched = &messages[i]
			}
		case protocol.PrimChatSendInviteCard:
			if expectedInterview != nil &&
				*message.CardType == protocol.CardTypeInterviewInvite &&
				*message.CardState == protocol.CardStateUnknown &&
				message.Interview != nil &&
				*message.Interview == *expectedInterview {
				matched = &messages[i]
			}
		}
	}
	if matched == nil {
		return dispatch.VerificationObservation{
			Reason: "最近窗口未见时间容差内的严格卡片正证",
		}, nil
	}
	var interview *protocol.InterviewDetails
	if matched.Interview != nil {
		value := *matched.Interview
		interview = &value
	}
	return dispatch.VerificationObservation{
		Confirmed: true, ContentHash: matched.ContentHash, SourceKey: matched.SourceKey,
		Interview: interview, ObservedAt: observedAt(*matched),
		Reason: "最近窗口命中严格卡片正证(同类取最新)",
	}, nil
}

func validOpaqueSourceKey(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		char := value[i]
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func observedAt(message protocol.ThreadMessage) int64 {
	if message.TsApprox != nil && *message.TsApprox > 0 {
		return *message.TsApprox
	}
	return time.Now().UnixMilli()
}
