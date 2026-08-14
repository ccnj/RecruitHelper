// Package appbridge wires the protocol dispatcher to the business-level patrol
// interfaces without making either package depend on the other.
package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/patrol"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// PatrolRunner is the sole command path used by account actors. Start performs
// only generation-fenced dispatch; the returned handle waits for the logical
// replacement chain without holding the patrol manager's short actor lock.
type PatrolRunner struct {
	Dispatcher *dispatch.Dispatcher
}

type patrolRunHandle struct {
	dispatcher *dispatch.Dispatcher
	logicalID  string
}

func (r PatrolRunner) Start(ctx context.Context, req patrol.RunRequest) (patrol.RunHandle, error) {
	if r.Dispatcher == nil {
		return nil, errors.New("dispatcher 不能为空")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	meta, ok := protocol.Primitives[req.Name]
	if !ok || meta.Ver == 0 || meta.Ver != req.Version {
		return nil, fmt.Errorf("原语版本不可用: %s@%d", req.Name, req.Version)
	}
	cmdContext := &protocol.CmdContext{
		Platform: req.Platform, AccountRef: req.AccountRef,
		ExpectedPrincipalFingerprint: req.ExpectedPrincipalFingerprint,
	}
	logicalID, err := r.Dispatcher.DispatchStructured(dispatch.DispatchRequest{
		HandID: req.HandID, ExpectedSession: req.ExpectedSession, ExpectedBootID: req.ExpectedBootID,
		Name: req.Name, Args: req.Args, Context: cmdContext,
	})
	if err != nil {
		return nil, err
	}
	return &patrolRunHandle{dispatcher: r.Dispatcher, logicalID: logicalID}, nil
}

func (h *patrolRunHandle) Wait(ctx context.Context) (json.RawMessage, error) {
	logical, err := h.dispatcher.WaitLogical(ctx, h.logicalID)
	if err != nil {
		return nil, err
	}
	return resultData(logical.Leaf)
}

func (h *patrolRunHandle) LogicalDispatchID() string { return h.logicalID }

func (r PatrolRunner) StartSourcingResume(
	ctx context.Context,
	req patrol.SourcingResumeRequest,
) (patrol.SourcingResumeHandle, error) {
	if r.Dispatcher == nil {
		return nil, errors.New("dispatcher 不能为空")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	args, err := protocol.Encode(protocol.CandidateReadSourcingResumeArgs{
		ExcludePlatformUserRefs: req.ExcludePlatformUserRefs,
	})
	if err != nil {
		return nil, err
	}
	logicalID, err := r.Dispatcher.DispatchStructured(dispatch.DispatchRequest{
		HandID: req.HandID, ExpectedSession: req.ExpectedSession, ExpectedBootID: req.ExpectedBootID,
		Name: protocol.PrimCandidateReadSourcingResume, Args: args,
		Context: &protocol.CmdContext{
			Platform: req.Platform, AccountRef: req.AccountRef,
			ExpectedPrincipalFingerprint: req.ExpectedPrincipalFingerprint,
		},
	})
	if err != nil {
		return nil, err
	}
	return &patrolRunHandle{dispatcher: r.Dispatcher, logicalID: logicalID}, nil
}

func (r PatrolRunner) StartResumeCapture(
	ctx context.Context,
	req patrol.ResumeCaptureRequest,
) (patrol.ResumeCaptureHandle, error) {
	if r.Dispatcher == nil {
		return nil, errors.New("dispatcher 不能为空")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	receipt, err := r.Dispatcher.DispatchResumeCapture(dispatch.ResumeCaptureDispatchRequest{
		ProfileID: req.ProfileID, HandID: req.HandID,
		ExpectedSession: req.ExpectedSession, ExpectedBootID: req.ExpectedBootID,
		Platform: req.Platform, AccountRef: req.AccountRef,
		ExpectedPrincipalFingerprint: req.ExpectedPrincipalFingerprint,
	})
	if err != nil {
		return nil, err
	}
	return &resumeCaptureRunHandle{dispatcher: r.Dispatcher, logicalID: receipt.LogicalDispatchID}, nil
}

type resumeCaptureRunHandle struct {
	dispatcher *dispatch.Dispatcher
	logicalID  string
}

func (h *resumeCaptureRunHandle) LogicalDispatchID() string { return h.logicalID }

func (h *resumeCaptureRunHandle) Wait(ctx context.Context) (json.RawMessage, error) {
	logical, err := h.dispatcher.WaitLogical(ctx, h.logicalID)
	if err != nil {
		return nil, err
	}
	return resumeCaptureResultData(logical.Leaf)
}

type automaticReplyRunHandle struct {
	dispatcher *dispatch.Dispatcher
	logicalID  string
}

type automaticGreetingRunHandle struct {
	dispatcher *dispatch.Dispatcher
	logicalID  string
}

func (r PatrolRunner) StartAutomaticGreeting(
	ctx context.Context,
	req patrol.AutomaticGreetingRequest,
) (patrol.AutomaticGreetingHandle, error) {
	if r.Dispatcher == nil {
		return nil, errors.New("dispatcher 不能为空")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	expectedIntentID, err := store.SourcingGreetingEffectIntentID(req.InvocationID)
	if err != nil {
		return nil, err
	}
	receipt, dispatchErr := r.Dispatcher.SendSourcingGreeting(dispatch.SendSourcingGreetingRequest{
		BatchID: req.BatchID, InvocationID: req.InvocationID,
	})
	if receipt == nil || receipt.IntentID != expectedIntentID || receipt.LogicalDispatchID == "" {
		if dispatchErr == nil {
			dispatchErr = store.ErrSourcingGreetingEffectConflict
		}
		return nil, dispatchErr
	}
	// WAL 一旦存在便由既有恢复轨独占结果；即使 socket 同步返回错误，也只
	// 等待同一个 logical dispatch，绝不另铸招呼意图。
	return &automaticGreetingRunHandle{dispatcher: r.Dispatcher, logicalID: receipt.LogicalDispatchID}, nil
}

func (h *automaticGreetingRunHandle) Wait(ctx context.Context) error {
	if h == nil || h.dispatcher == nil || h.logicalID == "" {
		return errors.New("自动招呼等待句柄无效")
	}
	_, err := h.dispatcher.WaitLogical(ctx, h.logicalID)
	return err
}

func (r PatrolRunner) StartAutomaticReply(
	ctx context.Context,
	req patrol.AutomaticReplyRequest,
) (patrol.AutomaticReplyHandle, error) {
	if r.Dispatcher == nil {
		return nil, errors.New("dispatcher 不能为空")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	receipt, dispatchErr := r.Dispatcher.SendMessage(dispatch.SendMessageRequest{
		IntentID: req.IntentID, PreviousIntentID: req.PreviousIntentID,
		AutomaticActionID: req.ActionID,
		ExpectedSession:   req.ExpectedSession, ExpectedBootID: req.ExpectedBootID,
		Platform: req.Platform, AccountRef: req.AccountRef,
		ConversationRef: req.ConversationRef, Text: req.Text,
	})
	if receipt == nil || receipt.IntentID != req.IntentID || receipt.LogicalDispatchID == "" {
		if dispatchErr == nil {
			dispatchErr = store.ErrEffectIntentConflict
		}
		return nil, dispatchErr
	}
	// Once the WAL transaction exists, its persistent recovery rail owns the
	// outcome. A socket error may already have moved it to verification; the
	// actor waits on the same logical command instead of forging another intent.
	return &automaticReplyRunHandle{dispatcher: r.Dispatcher, logicalID: receipt.LogicalDispatchID}, nil
}

func (h *automaticReplyRunHandle) Wait(ctx context.Context) error {
	if h == nil || h.dispatcher == nil || h.logicalID == "" {
		return errors.New("自动回复等待句柄无效")
	}
	_, err := h.dispatcher.WaitLogical(ctx, h.logicalID)
	return err
}

func (r PatrolRunner) StartAutomaticCard(
	ctx context.Context,
	req patrol.AutomaticCardRequest,
) (patrol.AutomaticCardHandle, error) {
	if r.Dispatcher == nil {
		return nil, errors.New("dispatcher 不能为空")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	primitive := ""
	switch req.Kind {
	case store.CommunicationActionInviteWechat:
		primitive = protocol.PrimChatSendWechatInvite
	case store.CommunicationActionInterviewInvite:
		primitive = protocol.PrimChatSendInviteCard
	case store.CommunicationActionAcceptWechat:
		primitive = protocol.PrimChatAcceptWechat
	default:
		return nil, store.ErrCommunicationActionInvalid
	}
	receipt, dispatchErr := r.Dispatcher.SendAutomaticCard(dispatch.SendAutomaticCardRequest{
		IntentID:          req.IntentID,
		PreviousIntentID:  req.PreviousIntentID,
		AutomaticActionID: req.ActionID,
		ExpectedSession:   req.ExpectedSession,
		ExpectedBootID:    req.ExpectedBootID,
		Platform:          req.Platform,
		AccountRef:        req.AccountRef,
		ConversationRef:   req.ConversationRef,
		Primitive:         primitive,
		Interview:         req.Interview,
		RequestSourceKey:  req.RequestSourceKey,
	})
	if receipt == nil ||
		receipt.IntentID != req.IntentID ||
		receipt.LogicalDispatchID == "" {
		if dispatchErr == nil {
			dispatchErr = store.ErrEffectIntentConflict
		}
		return nil, dispatchErr
	}
	return &automaticReplyRunHandle{
		dispatcher: r.Dispatcher,
		logicalID:  receipt.LogicalDispatchID,
	}, nil
}

// ── RunError 合成的唯一出口 ────────────────────────────────────────────
//
// 手侧显式 error 里的 retryable/sideEffect 原样保留——那是手的协议级证词。
// 手根本没回 result 的终局(expired/canceled/空 ResultBody)由脑合成 code,
// 此时 retryable 必须从契约表按命令 class 解析出来,不得留空:留空的
// retryable 曾被巡检失败分流当成最重的证词,一次 4 分钟的页面卡顿把候选人
// 永久隔离(2026-08-13 真机现场,2026-08-14 甲方裁决修正)。
// 新增合成路径一律经 runErrorFromLeaf,不得再手写 &patrol.RunError{Code: ...}。

// parseRetryableDefault 解析契约 errorCodes 表的 retryable 声明。
// 已知形态(contract.v1.json):单值 "yes";类键控 "yes|manualOnly(SX)",竖线
// 后带 (SX) 限定的分支只适用 effectful;其他限定(如 witness 的
// capacity/corrupt)依赖手侧数据,脑合成时无从判断,只取基础分支。
func parseRetryableDefault(raw string) (base, sx protocol.Retryable, ok bool) {
	valid := func(r protocol.Retryable) bool {
		switch r {
		case protocol.RetryableYes, protocol.RetryableNo,
			protocol.RetryableAfterRecovery, protocol.RetryableManualOnly:
			return true
		}
		return false
	}
	parts := strings.SplitN(raw, "|", 2)
	base = protocol.Retryable(parts[0])
	if !valid(base) {
		return "", "", false
	}
	if len(parts) == 1 {
		return base, "", true
	}
	if alt, found := strings.CutSuffix(parts[1], "(SX)"); found {
		sx = protocol.Retryable(alt)
		if !valid(sx) {
			return "", "", false
		}
		return base, sx, true
	}
	return base, "", true
}

// synthesizedRetryable 按契约表把错误码解析成该命令 class 下的 retryable。
// 表中缺该码或格式不识别时返回空串,由失败分流的默认方向兜底(2026-08-14
// 裁决:证词缺席=未知=本轮跳过)。
func synthesizedRetryable(code protocol.ErrorCode, class string) protocol.Retryable {
	meta, ok := protocol.ErrorCodes[code]
	if !ok {
		return ""
	}
	base, sx, ok := parseRetryableDefault(meta.RetryableDefault)
	if !ok {
		return ""
	}
	if class == string(protocol.ClassEffectful) && sx != "" {
		return sx
	}
	return base
}

// runErrorFromLeaf 把一条没有成功 data 的持久命令终局翻成 RunError。
// result 传 nil 表示 ResultBody 为空(命令从未产生 result)。
func runErrorFromLeaf(leaf store.CmdRecord, result *protocol.ResultBody) *patrol.RunError {
	code := protocol.ErrCodeCtxLostDuringExec
	if result == nil {
		if leaf.ErrorCode != "" {
			code = protocol.ErrorCode(leaf.ErrorCode)
		}
		return &patrol.RunError{
			Code:      code,
			Retryable: synthesizedRetryable(code, leaf.Class),
			Cause:     fmt.Errorf("逻辑命令终局 %s，未产生 result data", leaf.Status),
		}
	}
	var reason protocol.NotReadyReason
	var retryable protocol.Retryable
	var sideEffect protocol.SideEffect
	message := string(result.Status)
	if result.Error != nil {
		if result.Error.Code != "" {
			code = result.Error.Code
		}
		if result.Error.Message != "" {
			message = result.Error.Message
		}
		retryable = result.Error.Retryable
		sideEffect = result.Error.SideEffect
		if len(result.Error.Data) > 0 {
			var detail struct {
				Reason protocol.NotReadyReason `json:"reason"`
			}
			if json.Unmarshal(result.Error.Data, &detail) == nil {
				reason = detail.Reason
			}
		}
	} else if result.Status == protocol.ResultStatusCanceled {
		code = protocol.ErrCodeCanceledByBrain
	} else if result.Status == protocol.ResultStatusExpired {
		code = protocol.ErrCodeExecTimeoutHand
	}
	if retryable == "" {
		// 覆盖两种缺席:脑合成的 code(手没回 result),以及手回了 error
		// 却漏填 retryable 的畸形结果。两者都按契约默认补齐。
		retryable = synthesizedRetryable(code, leaf.Class)
	}
	return &patrol.RunError{
		Code: code, Reason: reason, Retryable: retryable, SideEffect: sideEffect,
		Cause: errors.New(message),
	}
}

func resumeCaptureResultData(leaf store.CmdRecord) (json.RawMessage, error) {
	if leaf.ResultBody == "" {
		return nil, runErrorFromLeaf(leaf, nil)
	}
	var result protocol.ResultBody
	if err := json.Unmarshal([]byte(leaf.ResultBody), &result); err != nil {
		return nil, errors.New("简历补采持久结果无法解析")
	}
	if result.Status == protocol.ResultStatusOk {
		if len(result.Data) == 0 || string(result.Data) == "null" {
			return nil, errors.New("简历补采成功结果缺少 data")
		}
		return append(json.RawMessage(nil), result.Data...), nil
	}
	return nil, runErrorFromLeaf(leaf, &result)
}

// Run 保留给 appbridge 的窄集成测试和非 actor 调用；生产巡检只走 Start/Wait。
func (r PatrolRunner) Run(ctx context.Context, req patrol.RunRequest) (json.RawMessage, error) {
	handle, err := r.Start(ctx, req)
	if err != nil {
		return nil, err
	}
	return handle.Wait(ctx)
}

// Probe runs the same formal dispatcher path before an account exists. It is
// used only by the human binding flow; patrol rounds use PatrolRunner.Start.
func (r PatrolRunner) Probe(ctx context.Context, handID string) (protocol.ProbePlatformData, error) {
	if r.Dispatcher == nil {
		return protocol.ProbePlatformData{}, errors.New("dispatcher 不能为空")
	}
	args, _ := json.Marshal(protocol.ProbePlatformArgs{})
	logical, err := r.Dispatcher.Run(ctx, dispatch.DispatchRequest{
		HandID: handID, Name: string(protocol.PrimProbePlatform), Args: args,
	})
	if err != nil {
		return protocol.ProbePlatformData{}, err
	}
	data, err := resultData(logical.Leaf)
	if err != nil {
		return protocol.ProbePlatformData{}, err
	}
	var probe protocol.ProbePlatformData
	if err := json.Unmarshal(data, &probe); err != nil {
		return protocol.ProbePlatformData{}, fmt.Errorf("解析 probe.platform 结果: %w", err)
	}
	return probe, nil
}

func resultData(leaf store.CmdRecord) (json.RawMessage, error) {
	if leaf.ResultBody == "" {
		return nil, runErrorFromLeaf(leaf, nil)
	}
	var result protocol.ResultBody
	if err := json.Unmarshal([]byte(leaf.ResultBody), &result); err != nil {
		return nil, fmt.Errorf("解析持久化 result: %w", err)
	}
	if result.Status == protocol.ResultStatusOk {
		if len(result.Data) == 0 || string(result.Data) == "null" {
			return nil, errors.New("成功 result 缺少 data")
		}
		return append(json.RawMessage(nil), result.Data...), nil
	}
	return nil, runErrorFromLeaf(leaf, &result)
}

// HandAvailability deliberately exposes only the active hand generation.
// Actor policy cannot reach tabs, selectors or other hand internals; unread
// awareness now flows through the chat.readUnreadTotal command path
// (2026-08-10), not this sensor projection.
type handAvailabilityHub interface {
	HandSession(string) (string, string, bool)
}

type HandAvailability struct {
	Hub handAvailabilityHub
}

func (h HandAvailability) State(_ context.Context, handID string) (patrol.HandState, error) {
	if h.Hub == nil {
		return patrol.HandState{}, errors.New("hub 不能为空")
	}
	sessionID, bootID, online := h.Hub.HandSession(handID)
	return patrol.HandState{Online: online, Session: sessionID, BootID: bootID}, nil
}
