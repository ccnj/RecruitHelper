package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/jobconfig"
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
	case protocol.PrimJobPublishDraft:
		return v.verifyJobPublish(ctx, req)
	default:
		return dispatch.VerificationObservation{}, errors.New("验证请求不是已支持的真实副作用意图")
	}
}

// verifyJobPublish 用职位管理列表回答"这个职位到底发出去没有"。
//
// 与消息类验证读的差别是本质的:它不读会话尾部、不比对发送指纹,只看平台职位名
// 全集里有没有这个名字。因此 ContentHash 留空——意图侧的 SendFingerprint 对发布
// 同样是空,两者相等即视为无指纹要求。归一化口径与幂等判定共用一套。
func (v EffectVerifier) verifyJobPublish(
	ctx context.Context,
	req dispatch.VerificationRequest,
) (dispatch.VerificationObservation, error) {
	if req.Command.Name != protocol.PrimJobPublishDraft || req.PublishDraftArgs == nil ||
		strings.TrimSpace(req.PublishDraftArgs.JobName) == "" {
		return dispatch.VerificationObservation{},
			errors.New("验证请求不是完整 job.publishDraft 意图")
	}
	argsRaw, err := protocol.Encode(protocol.JobReadPublishedListArgs{})
	if err != nil {
		return dispatch.VerificationObservation{}, err
	}
	state, err := v.Dispatcher.RunVerificationRead(
		ctx,
		req.Command.MsgID,
		dispatch.DispatchRequest{
			HandID:          req.Command.HandID,
			ExpectedSession: req.Command.Session,
			ExpectedBootID:  req.Command.BootIDAtDispatch,
			Name:            protocol.PrimJobReadPublishedList,
			Args:            argsRaw,
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
	if state == nil || !state.Settled || state.Leaf.Status != store.CmdOk || state.Leaf.ResultBody == "" {
		return dispatch.VerificationObservation{Reason: "职位清单验证读未取得成功终局"}, nil
	}
	var result protocol.ResultBody
	if err := json.Unmarshal([]byte(state.Leaf.ResultBody), &result); err != nil ||
		result.Status != protocol.ResultStatusOk {
		return dispatch.VerificationObservation{Reason: "职位清单验证读结果无效"}, nil
	}
	var data protocol.JobReadPublishedListData
	if err := json.Unmarshal(result.Data, &data); err != nil {
		return dispatch.VerificationObservation{Reason: "职位清单验证读数据无法解析"}, nil
	}
	if !jobconfig.MatchesExistingPosting(req.PublishDraftArgs.JobName, data.PostingNames) {
		return dispatch.VerificationObservation{
			Reason:     "平台职位列表中仍未出现该职位",
			ObservedAt: data.ObservedAt,
		}, nil
	}
	return dispatch.VerificationObservation{
		Confirmed:  true,
		ObservedAt: data.ObservedAt,
	}, nil
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
	if rejected := deliveryRejectedObservation(window, dispatchedAtMs); rejected != nil {
		return *rejected, nil
	}
	return classifyVerifiedSend(window.Messages, req.Intent.SendFingerprint, dispatchedAtMs)
}

// readRecentWindow 浅读一页最近消息。不携带 anchorTail、不开 deep、不消费
// cursor:页面可见判据只关心"刚发生"的窗口,分页完整性与历史到顶都不再是
// 成功前提;窗口读取失败按原语失败沿 miss→suspect 收敛。
func (v EffectVerifier) readRecentWindow(
	ctx context.Context,
	req dispatch.VerificationRequest,
	conversationRef string,
) (protocol.ChatReadThreadData, error) {
	args := protocol.ChatReadThreadArgs{
		ConversationRef: conversationRef,
		Window:          protocol.ThreadWindow{MaxMessages: verificationRecentWindow},
	}
	argsRaw, err := protocol.Encode(args)
	if err != nil {
		return protocol.ChatReadThreadData{}, err
	}
	state, err := v.Dispatcher.RunVerificationRead(ctx, req.Command.MsgID, dispatch.DispatchRequest{
		HandID: req.Command.HandID, Name: protocol.PrimChatReadThread, Args: argsRaw,
		Context: &protocol.CmdContext{
			Platform: req.Command.Platform, AccountRef: req.Command.AccountRef,
			ExpectedPrincipalFingerprint: req.Command.ExpectedPrincipalFingerprint,
		},
	})
	if err != nil {
		return protocol.ChatReadThreadData{}, err
	}
	dataRaw, err := resultData(state.Leaf)
	if err != nil {
		return protocol.ChatReadThreadData{}, err
	}
	var data protocol.ChatReadThreadData
	if err := json.Unmarshal(dataRaw, &data); err != nil {
		return protocol.ChatReadThreadData{}, fmt.Errorf("解析验证 readThread: %w", err)
	}
	return data, nil
}

// deliveryRejectedObservation 实现「拒收通知判失败」(AGENTS 防护成本预算第 9 条,
// 2026-08-11 甲方裁决):验证窗口带回不早于本次派发(减既有 5 秒时钟容差)的平台
// 拒收通知服务端时间戳时,本次发送判确定性失败,且优先于成功匹配——同窗并存
// 成功匹配与新鲜拒收通知的秒级竞态按拒收收场,甲方知情接受账面误差(方向少做,
// 归档使其不产生任何后续动作)。化石通知(时间早于窗口)返回 nil,走原有
// 匹配/miss 路径;字段缺失(含手侧 DOM 降级取数)同样返回 nil。
func deliveryRejectedObservation(
	data protocol.ChatReadThreadData,
	dispatchedAtMs int64,
) *dispatch.VerificationObservation {
	if data.RejectNoticeTs == nil || *data.RejectNoticeTs <= 0 ||
		*data.RejectNoticeTs < dispatchedAtMs-verificationClockToleranceMs {
		return nil
	}
	ts := *data.RejectNoticeTs
	return &dispatch.VerificationObservation{
		DeliveryRejectedTs: &ts,
		ObservedAt:         ts,
		Reason:             "验证窗口出现不早于派发的平台拒收通知(候选人已拉黑),判确定性失败",
	}
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
			!communication.ValidV4InterviewDetailsShape(
				req.InviteCardArgs.Interview.StartsAt,
				req.InviteCardArgs.Interview.EndsAt,
				string(req.InviteCardArgs.Interview.Method),
			) {
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
	if rejected := deliveryRejectedObservation(window, dispatchedAtMs); rejected != nil {
		return *rejected, nil
	}
	return classifyVerifiedCard(window.Messages, req.Command.Name, targetHash, interview, dispatchedAtMs)
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
		Confirmed: true, ContentHash: matched.ContentHash, SourceKey: matched.SourceKey,
		ObservedAt: observedAt(*matched),
		PlatformTsMs: platformTs(*matched),
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
		PlatformTsMs: platformTs(*matched),
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

// platformTs 与 observedAt 的区别是失败方向:observedAt 兜底到本机时钟,
// 本函数在消息不带平台时间时返回 nil——它的消费方(账本 ts_approx_ms)
// 只收平台证据,不收本机时钟。
func platformTs(message protocol.ThreadMessage) *int64 {
	if message.TsApprox != nil && *message.TsApprox > 0 {
		value := *message.TsApprox
		return &value
	}
	return nil
}
