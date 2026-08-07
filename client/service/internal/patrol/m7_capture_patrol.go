// 运营通知取证截图派发(2026-07-28 成功取证批次)。
// 触发面收敛为一个查询:该 profile 仍 pending 且未派发过取证的运营通知
// (换微信成功/约面成功两类事实在各自提交事务里入队,见 store 层钩子)。
// 截图是尽力而为的降级型感知:标记先行、每通知至多派发一轮,任何失败只留下
// "缺图"并由 notify runner 的 15 分钟兜底闸门按纯文本发送;不重试既有
// effectful,不阻塞巡检收束,不推进任何业务状态。
package patrol

import (
	"context"
	"log/slog"
	"strings"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

func (a *roundActor) captureNotificationEvidence(
	ctx context.Context,
	profileID string,
) error {
	pending, err := a.manager.store.NotificationsNeedingCapture(profileID)
	if err != nil || len(pending) == 0 {
		return err
	}
	profile, err := a.manager.store.CandidateProfileByID(profileID)
	if err != nil {
		return err
	}
	if profile == nil || profile.ConversationRef == nil {
		return nil // 会话尚未绑定:本轮无法取证,留待后续巡检或纯文本兜底
	}
	conversationRef := *profile.ConversationRef

	// 与 collectWechatContactBeforeTransition 同款:普通巡检可能已对账过多个
	// 会话,重开精确目标线程;显式当前会话入口已在所需路由上,不得再导航。
	if !a.requireCurrentThread {
		if _, err := a.readThread(ctx, conversationRef, nil, false); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return nil // 打开失败:不标记、不截图,下轮巡检再试;发送闸门自然兜底
		}
	}

	// 标记先行(每通知至多一轮):进程中断也不会在下轮重复平台交互;
	// 标记后任何截图失败均为最终缺图,不再重拍。
	ids := make([]uint64, 0, len(pending))
	for _, row := range pending {
		ids = append(ids, row.ID)
	}
	if err := a.manager.store.MarkNotificationsAssetsRequested(ids, a.manager.now()); err != nil {
		return err
	}

	chatData, chatErr := invokePrimitiveDirect[protocol.CaptureScreenshotData](
		ctx,
		a,
		protocol.PrimChatCaptureThreadScreenshot,
		protocol.ChatCaptureThreadScreenshotArgs{ConversationRef: conversationRef},
	)
	if chatErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		slog.Warn("聊天取证截图失败,缺图降级", "profileId", profileID, "err", chatErr)
	} else if err := a.manager.store.SaveCandidateScreenshot(
		profileID,
		store.CandidateScreenshotKindChat,
		chatData.ImageBlobRef,
		chatData.ByteSize,
		chatData.Truncated,
		chatData.CapturedAt,
		a.manager.now(),
	); err != nil {
		return err
	}

	resumeData, resumeErr := invokePrimitiveDirect[protocol.CaptureScreenshotData](
		ctx,
		a,
		protocol.PrimCandidateCaptureResumeScreenshot,
		protocol.CandidateCaptureResumeScreenshotArgs{
			ConversationRef: conversationRef,
			PlatformUserRef: profile.PlatformUserRef,
		},
	)
	if resumeErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		slog.Warn("简历取证截图失败,缺图降级", "profileId", profileID, "err", resumeErr)
	} else if err := a.manager.store.SaveCandidateScreenshot(
		profileID,
		store.CandidateScreenshotKindResume,
		resumeData.ImageBlobRef,
		resumeData.ByteSize,
		resumeData.Truncated,
		resumeData.CapturedAt,
		a.manager.now(),
	); err != nil {
		return err
	}
	return a.collectPeerPhoneObservation(ctx, profile, conversationRef)
}

// collectPeerPhoneObservation 取证顺访读侧栏电话(2026-08-06 甲方裁决):与
// 截图同一趟到访,失败只产生「缺号」。收编判定(手机格式 + 面板姓名与会话
// 对方首字核对)在脑侧;日志不携带号码与姓名(普通日志边界)。
func (a *roundActor) collectPeerPhoneObservation(
	ctx context.Context,
	profile *store.CandidateProfile,
	conversationRef string,
) error {
	phoneData, phoneErr := invokePrimitiveDirect[protocol.ChatReadPeerPhoneData](
		ctx,
		a,
		protocol.PrimChatReadPeerPhone,
		protocol.ChatReadPeerPhoneArgs{ConversationRef: conversationRef},
	)
	if phoneErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		slog.Warn("电话侧栏读取失败,缺号降级", "profileId", profile.ProfileID, "err", phoneErr)
		return nil
	}
	if strings.TrimSpace(phoneData.Phone) == "" {
		if !phoneData.Masked {
			return nil // 无号或虚拟号形态:本轮无观察,通知照常少一行
		}
		revealed, err := a.maybeRevealPeerPhone(ctx, profile, conversationRef)
		if err != nil || revealed == nil {
			return err
		}
		phoneData = *revealed
		if strings.TrimSpace(phoneData.Phone) == "" {
			return nil
		}
	}
	conversation, err := a.manager.store.ConversationByKey(store.ConversationKey{
		Platform:        profile.Platform,
		AccountRef:      profile.AccountRef,
		ConversationRef: conversationRef,
	})
	if err != nil {
		return err
	}
	peerDisplayName := ""
	if conversation != nil {
		peerDisplayName = conversation.PeerDisplayName
	}
	if !store.AcceptCandidatePhoneObservation(phoneData.Phone, phoneData.PanelName, peerDisplayName) {
		slog.Info("电话观察未通过收编判定,按缺号处理", "profileId", profile.ProfileID)
		return nil
	}
	return a.manager.store.SaveCandidatePhoneObservation(
		profile.ProfileID,
		strings.TrimSpace(phoneData.Phone),
		phoneData.ObservedAt,
		a.manager.now(),
	)
}

// maybeRevealPeerPhone 对遮挡形态按 2026-08-07 裁决的收窄条件点一次「查看电话」:
// 该候选人有约面成功通知、尚无权威微信资产、且从未尝试过。标记先行——落行成功
// 才派发,不管命令结局如何终身不再点,每候选人至多消耗一次平台查看权益。
// 返回 nil 表示本轮无揭示结果(条件不满足或揭示失败),一律「缺号」收场。
func (a *roundActor) maybeRevealPeerPhone(
	ctx context.Context,
	profile *store.CandidateProfile,
	conversationRef string,
) (*protocol.ChatReadPeerPhoneData, error) {
	meeting, err := a.manager.store.InterviewNotificationForProfile(profile.ProfileID)
	if err != nil {
		return nil, err
	}
	if meeting == nil {
		return nil, nil // 还没约面:不值得消耗查看权益
	}
	hasWechat, err := a.manager.store.HasWechatContactAsset(profile.ProfileID)
	if err != nil {
		return nil, err
	}
	if hasWechat {
		return nil, nil // 微信已到手:不消耗
	}
	first, err := a.manager.store.TryMarkPhoneRevealAttempt(profile.ProfileID, a.manager.now())
	if err != nil {
		return nil, err
	}
	if !first {
		return nil, nil // 唯一一次机会已用过,终身不再点
	}
	slog.Info("遮挡手机号满足揭示条件,派发查看电话", "profileId", profile.ProfileID)
	revealed, revealErr := invokePrimitiveDirect[protocol.ChatReadPeerPhoneData](
		ctx,
		a,
		protocol.PrimChatRevealPeerPhone,
		protocol.ChatReadPeerPhoneArgs{ConversationRef: conversationRef},
	)
	if revealErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		slog.Warn("查看电话揭示失败,缺号降级", "profileId", profile.ProfileID, "err", revealErr)
		return nil, nil
	}
	return &revealed, nil
}
