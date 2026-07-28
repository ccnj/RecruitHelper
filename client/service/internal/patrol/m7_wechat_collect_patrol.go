// 微信号收编触发器(2026-07-28 真机事实驱动,2026-07-29 扩到两种发起形态)。
//
// 真机事实:平台不翻转我方那张换微信邀请卡的状态,候选人同意表现为新增一条
// 交换结果消息(内含双方微信号结构化字段)。既有的收号入口
// collectWechatContactBeforeTransition 只由"卡片 pending→accepted 跃迁事实"
// 驱动,而该跃迁在真机上从不发生,因此这一形态的号从未被收编过——状态机知道
// 交换成功了,账本里却没有号,运营通知也就无从入队。
//
// 本触发器改由已持久的业务状态驱动:微信线已是"已换号"、档案却没有对应资产,
// 且账本里存在带稳定键的换微信请求卡时,重开该会话并调用既有 readonly 专用
// 读收号。它不新增原语、不新增契约表面,只补上一个缺失的调用点。
//
// 两种发起形态共用本触发器,只是锚不同:我方发起时锚在我方 out 请求卡,候选人
// 主动发起时锚在其 in 请求卡;结果卡(accepted)在查询里就被排除,不会被误当成
// 请求锚。形态判定不在这里做——原语自己按 originType 配对核对方向,配不上就
// 阴性。候选人主动发起的一路通常已由接受动作当场取到号,本触发器是它的兜底:
// 259 晚到时接受动作按无号成功收束,号由后续巡检在这里补收。两条链最终汇合
// 在同一个幂等资产收编事务。
//
// 失败方向全部收敛为"本轮没收到号":readonly 读无副作用、可无限重试,下一轮
// 巡检自然再来;绝不因收号失败阻塞巡检收束或推进任何业务状态。
package patrol

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

func (a *roundActor) collectExchangedWechatContact(
	ctx context.Context,
	profileID string,
) error {
	aggregate, err := a.manager.store.CommunicationV4AggregateByProfile(profileID)
	if err != nil {
		if errors.Is(err, store.ErrCommunicationV4Missing) {
			return nil
		}
		return err
	}
	if aggregate.State.WechatState != communication.V4WechatExchanged {
		return nil
	}
	profile, err := a.manager.store.CandidateProfileByID(profileID)
	if err != nil {
		return err
	}
	if profile == nil || profile.ConversationRef == nil {
		return nil
	}
	conversationRef := *profile.ConversationRef
	requestSourceKey, found, err := a.manager.store.LatestWechatExchangeRequestSourceKey(
		store.ConversationKey{
			Platform:        profile.Platform,
			AccountRef:      profile.AccountRef,
			ConversationRef: conversationRef,
		},
	)
	if err != nil {
		return err
	}
	if !found {
		// 账本里没有任何换微信请求卡,无锚可用:本轮无从收号。
		return nil
	}
	hasAsset, err := a.manager.store.HasWechatContactAssetForRequest(profileID, requestSourceKey)
	if err != nil || hasAsset {
		return err
	}

	// 普通巡检可能已经对账过若干会话;按既有做法重开这一条精确会话。
	// 显式当前会话入口本来就在所需路由上,不得再导航走。
	if !a.requireCurrentThread {
		if _, err := a.readThread(ctx, conversationRef, nil, false); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			slog.Warn(
				"微信收号未能打开目标会话,本轮跳过",
				"profileId", profileID,
				"err", err,
			)
			return nil
		}
	}
	data, err := invokePrimitiveDirect[protocol.ChatReadWechatExchangeOutcomeData](
		ctx,
		a,
		protocol.PrimChatReadWechatExchangeOutcome,
		protocol.ChatReadWechatExchangeOutcomeArgs{
			ConversationRef:  conversationRef,
			RequestSourceKey: requestSourceKey,
		},
	)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		// 响亮记录:收号是这条链上唯一的号来源,静默无操作会让"通知没发"
		// 无从归因。错误码足以区分页面未就绪、路由漂移与结构不符。
		slog.Warn(
			"微信收号读取失败,本轮未收到号",
			"profileId", profileID,
			"err", err,
		)
		return nil
	}
	if !data.Confirmed ||
		strings.TrimSpace(data.ExchangeSourceKey) == "" ||
		strings.TrimSpace(data.PeerWechat) == "" {
		// 正证不足:可能结果消息尚未到达、匹配不唯一或字段缺失。原语当前只
		// 回单一 confirmed 布尔,更细的分类需要扩契约字段,列为后置项。
		slog.Warn(
			"微信收号本轮未取得唯一正证",
			"profileId", profileID,
			"confirmed", data.Confirmed,
		)
		return nil
	}
	_, created, err := a.manager.store.RecordObservedWechatContact(
		store.WechatContactAssetRequest{
			ProfileID:         profileID,
			Platform:          profile.Platform,
			AccountRef:        profile.AccountRef,
			ConversationRef:   conversationRef,
			RequestSourceKey:  requestSourceKey,
			ExchangeSourceKey: data.ExchangeSourceKey,
			PeerWechat:        data.PeerWechat,
			ObservedAtMs:      data.ObservedAt,
			RecordedAt:        a.manager.now(),
		},
	)
	if err != nil {
		return err
	}
	if created {
		// 只记事实成立,不记号本身(数据边界)。
		slog.Info("微信号已收编", "profileId", profileID)
	}
	return nil
}
