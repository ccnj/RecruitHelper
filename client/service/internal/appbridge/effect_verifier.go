package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/patrol"
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
	case protocol.PrimChatSendGreeting:
		return v.verifyGreeting(ctx, req)
	default:
		return dispatch.VerificationObservation{}, errors.New("验证请求不是已支持的真实副作用意图")
	}
}

func (v EffectVerifier) verifySendMessage(ctx context.Context, req dispatch.VerificationRequest) (dispatch.VerificationObservation, error) {
	if req.Command.Name != protocol.PrimChatSendMessage || req.Args.ConversationRef == "" ||
		len(req.Guards.ExpectedTail) == 0 {
		return dispatch.VerificationObservation{}, errors.New("验证请求不是完整 chat.sendMessage 意图")
	}

	var aggregate []protocol.ThreadMessage
	cursor := ""
	restarts := 0
	seen := map[string]struct{}{}
	for page := 0; page < verificationMaxPages; page++ {
		args := protocol.ChatReadThreadArgs{
			ConversationRef: req.Args.ConversationRef, Cursor: cursor,
			Window: protocol.ThreadWindow{
				AnchorTail: req.Guards.ExpectedTail, Deep: true,
				MaxMessages: protocol.DefaultPaginationReadThreadMaxItems,
			},
		}
		argsRaw, err := protocol.Encode(args)
		if err != nil {
			return dispatch.VerificationObservation{}, err
		}
		state, err := v.Dispatcher.RunVerificationRead(ctx, req.Command.MsgID, dispatch.DispatchRequest{
			HandID: req.Command.HandID, Name: protocol.PrimChatReadThread, Args: argsRaw,
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
			if cursor != "" && restarts == 0 && isCursorInvalid(err) {
				restarts++
				aggregate = nil
				cursor = ""
				seen = map[string]struct{}{}
				page = -1
				continue
			}
			return dispatch.VerificationObservation{}, err
		}
		var data protocol.ChatReadThreadData
		if err := json.Unmarshal(dataRaw, &data); err != nil {
			return dispatch.VerificationObservation{}, fmt.Errorf("解析验证 readThread: %w", err)
		}
		if cursor == "" {
			aggregate = append([]protocol.ThreadMessage(nil), data.Messages...)
		} else {
			// 首页最新，cursor 页更旧，聚合后始终保持旧→新。
			aggregate = append(append([]protocol.ThreadMessage(nil), data.Messages...), aggregate...)
		}
		anchorStarts := matchingAnchorStarts(aggregate, req.Guards.ExpectedTail)
		if data.Complete || data.ReachedTop || len(anchorStarts) != 0 {
			return classifyVerifiedSend(aggregate, anchorStarts, len(req.Guards.ExpectedTail), req.Intent.SendFingerprint)
		}
		if data.NextCursor == nil || *data.NextCursor == "" {
			return dispatch.VerificationObservation{}, errors.New("验证分页未完成但缺少 nextCursor")
		}
		next := *data.NextCursor
		if _, duplicate := seen[next]; duplicate || next == cursor {
			return dispatch.VerificationObservation{}, errors.New("验证分页 cursor 循环")
		}
		seen[next] = struct{}{}
		cursor = next
	}
	return dispatch.VerificationObservation{}, errors.New("验证分页超过上限")
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
		return dispatch.VerificationObservation{Reason: "本轮未取得唯一招呼正证"}, nil
	}
	return dispatch.VerificationObservation{
		Confirmed: true, ContentHash: data.ContentHash, ConversationRef: data.ConversationRef,
		ObservedAt: data.ObservedAt, Reason: "候选人、职位、新会话与服务端招呼唯一匹配",
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
