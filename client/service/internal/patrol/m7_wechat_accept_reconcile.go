// 接受微信取得正证后对该会话的定向重对账(立案 4.3,2026-07-29 甲方批准)。
//
// 接受是唯一"我方先于账本知情"的动作:我方自己点的同意,正证一到手就已知交换
// 成功,而消息账本要等下一轮对账才看到那条 259 结果。既有对账每轮固定一次、
// 位置在业务处理之前,接受发生在其后,本轮无从补入。若当日巡检已停或下一轮
// 尚远,这段窗口内产品 UI 的微信线状态与已发出的运营通知自相矛盾。
//
// 我方发起的另一形态不存在此问题:候选人何时同意我方无从知晓,对账就是我方
// 得知的那一刻,账本与认知同时更新。故本文件只对接受动作触发。
//
// 边界:只在本轮接受取得正证后触发、只对该一个会话、完整复用既有
// reconcileConversation(readThread → syncledger.Reconcile → ApplyPlan)。不新增
// 状态、不新增表、不改对账算法、不引入新的写入语义。已考虑并否决的替代方案是
// 用正证返回的 exchangeSourceKey 直接往账本追加该 259 行:若候选人在我方点击
// 前后另发了消息,凭空插行会打乱 seq 顺序、导致下一轮对账判冲突;账本一致性
// 优先于省下的那次读取。
//
// 失败方向只回退为"下轮再说":重对账失败不阻断收号、通知、巡检收束或任何业务
// 状态推进,也不重试。
package patrol

import (
	"context"
	"errors"
	"log/slog"

	"recruithelper/client/service/internal/store"
)

func (a *roundActor) reconcileAfterWechatAccepted(
	ctx context.Context,
	profileID string,
) error {
	if _, pending := a.wechatAcceptedProfiles[profileID]; !pending {
		return nil
	}
	// 无论本次重对账成败都只做一次：它是"让本轮状态跟上"的加速,不是账本的
	// 必经汇点,失败自然由下一轮常规对账兜底。
	delete(a.wechatAcceptedProfiles, profileID)

	target, ready, err := a.manager.store.CommunicationTargetForProfile(profileID)
	if err != nil {
		if errors.Is(err, store.ErrCommunicationV4Missing) {
			return nil
		}
		return err
	}
	if !ready || target == nil {
		return nil
	}
	key := store.ConversationKey{
		Platform:        target.Profile.Platform,
		AccountRef:      target.Profile.AccountRef,
		ConversationRef: target.Conversation.ConversationRef,
	}
	ledger, err := a.manager.store.MessagesForConversation(key)
	if err != nil {
		return err
	}
	projection, err := a.reconcileConversation(ctx, dirtyConversation{
		conversation: target.Conversation,
		ledger:       ledger,
	})
	if len(projection.Messages) != 0 || len(projection.CardTransitions) != 0 {
		a.projection = append(a.projection, projection)
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		slog.Warn(
			"接受微信后定向重对账失败,微信线状态留待下一轮常规对账",
			"profileId", profileID,
			"err", err,
		)
		return nil
	}
	if a.classificationCorrected {
		return nil
	}

	// 新进账本的交换结果卡还要经既有投影汇点才会推进微信线。这里只投影,不再
	// 排空动作:新规划的候选人可见动作留到下一轮,收号与运营通知则由本轮随后的
	// collectExchangedWechatContact 完成。
	refreshed, ready, err := a.manager.store.CommunicationTargetForProfile(profileID)
	if err != nil {
		if errors.Is(err, store.ErrCommunicationV4Missing) {
			return nil
		}
		return err
	}
	if !ready || refreshed == nil {
		return nil
	}
	return a.processCommunicationV4Target(ctx, *refreshed)
}
