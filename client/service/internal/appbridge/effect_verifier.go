package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/patrol"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

const verificationMaxPages = 32

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
	if req.Command.Name != protocol.PrimChatSendMessage || req.Args.ConversationRef == "" ||
		len(req.Guards.ExpectedTail) == 0 {
		return dispatch.VerificationObservation{}, errors.New("验证请求不是完整 chat.sendMessage 意图")
	}
	aggregate, anchorStarts, err := v.readThreadWindow(ctx, req, req.Args.ConversationRef)
	if err != nil {
		return dispatch.VerificationObservation{}, err
	}
	return classifyVerifiedSend(
		aggregate, anchorStarts, len(req.Guards.ExpectedTail), req.Intent.SendFingerprint,
	)
}

func (v EffectVerifier) readThreadWindow(
	ctx context.Context,
	req dispatch.VerificationRequest,
	conversationRef string,
) ([]protocol.ThreadMessage, []int, error) {
	var aggregate []protocol.ThreadMessage
	cursor := ""
	restarts := 0
	seen := map[string]struct{}{}
	for page := 0; page < verificationMaxPages; page++ {
		args := protocol.ChatReadThreadArgs{
			ConversationRef: conversationRef, Cursor: cursor,
			Window: protocol.ThreadWindow{
				AnchorTail: req.Guards.ExpectedTail, Deep: true,
				MaxMessages: protocol.DefaultPaginationReadThreadMaxItems,
			},
		}
		argsRaw, err := protocol.Encode(args)
		if err != nil {
			return nil, nil, err
		}
		state, err := v.Dispatcher.RunVerificationRead(ctx, req.Command.MsgID, dispatch.DispatchRequest{
			HandID: req.Command.HandID, Name: protocol.PrimChatReadThread, Args: argsRaw,
			Context: &protocol.CmdContext{
				Platform: req.Command.Platform, AccountRef: req.Command.AccountRef,
				ExpectedPrincipalFingerprint: req.Command.ExpectedPrincipalFingerprint,
			},
		})
		if err != nil {
			return nil, nil, err
		}
		dataRaw, err := resultData(state.Leaf)
		if err != nil {
			if cursor != "" && restarts == 0 && isCursorInvalid(err) {
				restarts++
				aggregate = nil
				cursor = ""
				seen = map[string]struct{}{}
				page = -1
				continue
			}
			return nil, nil, err
		}
		var data protocol.ChatReadThreadData
		if err := json.Unmarshal(dataRaw, &data); err != nil {
			return nil, nil, fmt.Errorf("解析验证 readThread: %w", err)
		}
		if cursor == "" {
			aggregate = append([]protocol.ThreadMessage(nil), data.Messages...)
		} else {
			// 首页最新，cursor 页更旧，聚合后始终保持旧→新。
			aggregate = append(append([]protocol.ThreadMessage(nil), data.Messages...), aggregate...)
		}
		anchorStarts := matchingAnchorStarts(aggregate, req.Guards.ExpectedTail)
		if data.Complete || data.ReachedTop || len(anchorStarts) != 0 {
			return aggregate, anchorStarts, nil
		}
		if data.NextCursor == nil || *data.NextCursor == "" {
			return nil, nil, errors.New("验证分页未完成但缺少 nextCursor")
		}
		next := *data.NextCursor
		if _, duplicate := seen[next]; duplicate || next == cursor {
			return nil, nil, errors.New("验证分页 cursor 循环")
		}
		seen[next] = struct{}{}
		cursor = next
	}
	return nil, nil, errors.New("验证分页超过上限")
}

func (v EffectVerifier) verifyCard(
	ctx context.Context,
	req dispatch.VerificationRequest,
) (dispatch.VerificationObservation, error) {
	if len(req.Guards.ExpectedTail) == 0 {
		return dispatch.VerificationObservation{}, errors.New("卡片验证请求缺少 expectedTail")
	}
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
	aggregate, anchorStarts, err := v.readThreadWindow(ctx, req, conversationRef)
	if err != nil {
		return dispatch.VerificationObservation{}, err
	}
	return classifyVerifiedCard(
		aggregate, anchorStarts, len(req.Guards.ExpectedTail),
		req.Command.Name, targetHash, interview,
	)
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

func matchingAnchorStarts(messages []protocol.ThreadMessage, anchors []protocol.MessageAnchor) []int {
	if len(anchors) == 0 || len(messages) < len(anchors) {
		return nil
	}
	var starts []int
	for start := 0; start+len(anchors) <= len(messages); start++ {
		matched := true
		for offset := range anchors {
			if messages[start+offset].Direction != anchors[offset].Direction ||
				messages[start+offset].ContentHash != anchors[offset].ContentHash {
				matched = false
				break
			}
		}
		if matched {
			starts = append(starts, start)
		}
	}
	return starts
}

func classifyVerifiedSend(
	messages []protocol.ThreadMessage,
	anchorStarts []int,
	tailLength int,
	targetHash string,
) (dispatch.VerificationObservation, error) {
	if len(anchorStarts) != 1 {
		reason := "未找到 expectedTail"
		if len(anchorStarts) > 1 {
			reason = "expectedTail 在当前窗口出现多次，无法唯一定位"
		}
		return dispatch.VerificationObservation{Reason: reason}, nil
	}
	start := anchorStarts[0] + tailLength
	if tailLength <= 0 || start > len(messages) {
		return dispatch.VerificationObservation{}, errors.New("验证分类的 expectedTail 长度非法")
	}
	var matched []protocol.ThreadMessage
	for i := start; i < len(messages); i++ {
		message := messages[i]
		if message.Direction == protocol.MessageDirectionOut && message.Kind == protocol.MessageKindText &&
			message.ContentHash == targetHash {
			matched = append(matched, message)
		}
	}
	if len(matched) != 1 {
		reason := "expectedTail 之后未找到目标 out/text 指纹"
		if len(matched) > 1 {
			reason = "expectedTail 之后出现多条相同 out/text 指纹，结果歧义"
		}
		return dispatch.VerificationObservation{Reason: reason}, nil
	}
	return dispatch.VerificationObservation{
		Confirmed: true, ContentHash: matched[0].ContentHash, ObservedAt: observedAt(matched[0]),
		Reason: "expectedTail 之后唯一命中目标 out/text 指纹",
	}, nil
}

func classifyVerifiedCard(
	messages []protocol.ThreadMessage,
	anchorStarts []int,
	tailLength int,
	primitive string,
	targetHash string,
	expectedInterview *protocol.InterviewDetails,
) (dispatch.VerificationObservation, error) {
	if len(anchorStarts) != 1 {
		reason := "未找到 expectedTail"
		if len(anchorStarts) > 1 {
			reason = "expectedTail 在当前窗口出现多次，无法唯一定位"
		}
		return dispatch.VerificationObservation{Reason: reason}, nil
	}
	start := anchorStarts[0] + tailLength
	if tailLength <= 0 || start > len(messages) {
		return dispatch.VerificationObservation{}, errors.New("卡片验证分类的 expectedTail 长度非法")
	}
	var matched []protocol.ThreadMessage
	for i := start; i < len(messages); i++ {
		message := messages[i]
		if message.Direction != protocol.MessageDirectionOut ||
			message.Kind != protocol.MessageKindCard ||
			message.ContentHash != targetHash ||
			!validOpaqueSourceKey(message.SourceKey) ||
			message.CardType == nil || message.CardState == nil {
			continue
		}
		switch primitive {
		case protocol.PrimChatSendWechatInvite:
			if *message.CardType == protocol.CardTypeWechatExchange &&
				*message.CardState == protocol.CardStatePending &&
				message.Interview == nil {
				matched = append(matched, message)
			}
		case protocol.PrimChatSendInviteCard:
			if expectedInterview != nil &&
				*message.CardType == protocol.CardTypeInterviewInvite &&
				*message.CardState == protocol.CardStateUnknown &&
				message.Interview != nil &&
				*message.Interview == *expectedInterview {
				matched = append(matched, message)
			}
		default:
			return dispatch.VerificationObservation{}, errors.New("卡片验证原语非法")
		}
	}
	if len(matched) != 1 {
		reason := "expectedTail 之后未找到严格匹配的 out/card 正证"
		if len(matched) > 1 {
			reason = "expectedTail 之后出现多条严格匹配的 out/card，结果歧义"
		}
		return dispatch.VerificationObservation{Reason: reason}, nil
	}
	confirmed := matched[0]
	var interview *protocol.InterviewDetails
	if confirmed.Interview != nil {
		value := *confirmed.Interview
		interview = &value
	}
	return dispatch.VerificationObservation{
		Confirmed: true, ContentHash: confirmed.ContentHash, SourceKey: confirmed.SourceKey,
		Interview: interview, ObservedAt: observedAt(confirmed),
		Reason: "expectedTail 之后唯一命中严格卡片正证",
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

func isCursorInvalid(err error) bool {
	var runErr *patrol.RunError
	return errors.As(err, &runErr) && runErr.Code == protocol.ErrCodeCursorInvalid
}

func observedAt(message protocol.ThreadMessage) int64 {
	if message.TsApprox != nil && *message.TsApprox > 0 {
		return *message.TsApprox
	}
	return time.Now().UnixMilli()
}
