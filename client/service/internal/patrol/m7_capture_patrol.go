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
	return nil
}
