package notify

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"recruithelper/client/service/internal/store"
)

// Ledger 是 runner 对发件箱账本的最窄依赖面(*store.Store 天然满足)。
type Ledger interface {
	ExpireStaleNotifications(olderThan time.Duration, at time.Time) (int64, error)
	TakePendingNotifications(limit int, maxAttempts int) ([]store.NotificationOutbox, error)
	NotificationRenderSnapshotForProfile(profileID string) (*store.NotificationRenderSnapshot, error)
	InterviewNotificationForProfile(profileID string) (*store.NotificationOutbox, error)
	NotificationCaptureGateForProfile(profileID string) (store.NotificationCaptureGate, error)
	MarkNotificationSent(id uint64, sentWithWechat bool, at time.Time) error
	MarkNotificationFailed(id uint64, lastError string, maxAttempts int, at time.Time) error
	MarkNotificationSkipped(id uint64, reason string, at time.Time) error
}

// BlobReader 只读取内容寻址截图字节(*blobstore.Store 天然满足)。
type BlobReader interface {
	ReadFile(ref string) ([]byte, error)
}

const (
	maxAttempts          = 5
	takeBatchLimit       = 10
	tickInterval         = 30 * time.Second
	screenshotGateWindow = 15 * time.Minute // 三资产闸门:齐即发,否则最多等 15 分钟兜底
	// 候选人主动换微信后的并发窗口:窗口内约到面则并入面试确认,到点仍无约面
	// 才单独发出(2026-08-06 甲方裁决)。
	wechatMergeWindow = 2 * time.Hour
	staleAfter        = 7 * 24 * time.Hour // 超龄未发出标 expired,留表可查
)

// Runner 是发件箱后台轮询器。webhook 发送不属于候选人可见动作,不受统一
// 业务运行窗口约束(AGENTS.md 条款),失败只降级不阻塞任何业务主线。
type Runner struct {
	store        Ledger
	blobs        BlobReader
	customerName func() string
	webhookURL   string
	client       *http.Client
	now          func() time.Time

	heldNotified map[uint64]bool // 已记过「进入等待」INFO 的通知,状态变化才再打

	daily            DailyReportDeps // 日报依赖;零值时不产生日报(见 daily_report.go)
	dailyEnqueuedFor string          // 进程内已入队的本地日期,挡每 tick 重复插入
}

func NewRunner(
	st Ledger,
	blobs BlobReader,
	customerName func() string,
) *Runner {
	return &Runner{
		store:        st,
		blobs:        blobs,
		customerName: customerName,
		webhookURL:   WecomWebhookURL,
		client:       &http.Client{Timeout: requestTimeout},
		now:          time.Now,
		heldNotified: map[uint64]bool{},
	}
}

// Run 阻塞轮询直到 ctx 结束;tick 内任何异常只记日志,守护循环不退出。
func (r *Runner) Run(ctx context.Context) {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.Tick()
		}
	}
}

type tickSummary struct {
	Sent    int
	Failed  int
	Held    int
	Dropped int
}

func (r *Runner) Tick() tickSummary {
	summary := tickSummary{}
	now := r.now()
	r.maybeEnqueueDailyReport(now)
	if expired, err := r.store.ExpireStaleNotifications(staleAfter, now); err != nil {
		slog.Warn("运营通知过期清点失败", "err", err)
	} else if expired > 0 {
		slog.Warn("运营通知过期丢弃", "count", expired)
	}
	rows, err := r.store.TakePendingNotifications(takeBatchLimit, maxAttempts)
	if err != nil {
		slog.Warn("运营通知取件失败", "err", err)
		return summary
	}
	for _, row := range rows {
		// 日报行没有候选人,走独立分叉;不得进入下方按 ProfileID 取快照的路径,
		// 否则会被记成 profile_missing 失败。
		if row.NotifyType == store.NotificationTypeDailyReport {
			r.handleDailyReport(row, now, &summary)
			continue
		}
		snapshot, err := r.store.NotificationRenderSnapshotForProfile(row.ProfileID)
		if err != nil || snapshot == nil {
			slog.Warn("运营通知快照缺失", "notifyId", row.ID, "type", row.NotifyType, "err", err)
			if err == nil {
				// 档案缺失属数据异常:按失败重试留痕,不静默丢弃。
				_ = r.store.MarkNotificationFailed(row.ID, "profile_missing", maxAttempts, now)
				summary.Failed++
			}
			continue
		}
		// 取证事实在取件时点一次性取,hold 日志与闸门用同一份:查询失败按
		// "取证无事在身"处理,退回只看资产有无的旧行为,不阻塞发送。
		gate, capErr := r.store.NotificationCaptureGateForProfile(row.ProfileID)
		if capErr != nil {
			slog.Warn("取证事实查询失败,按无待取证处理", "notifyId", row.ID, "err", capErr)
			gate = store.NotificationCaptureGate{}
		}
		verdict, supplement := r.decide(row, snapshot, gate, now)
		switch verdict {
		case decisionHold:
			summary.Held++
			if !r.heldNotified[row.ID] {
				r.heldNotified[row.ID] = true
				slog.Info(
					"运营通知进入等待发送",
					"notifyId", row.ID,
					"type", row.NotifyType,
					"profileId", row.ProfileID,
					"missing", missingAssets(snapshot),
					"pendingCapture", gate.Pending,
					"stale", staleAssets(snapshot, gate.LastDispatchAt),
				)
			}
			continue
		case decisionDrop:
			// 微信号已随约面通知带给运营:该独立通知不再发送,也不算失败。
			if err := r.store.MarkNotificationSkipped(row.ID, "superseded", now); err != nil {
				slog.Warn("运营通知去重落账失败", "notifyId", row.ID, "err", err)
				continue
			}
			delete(r.heldNotified, row.ID)
			summary.Dropped++
			slog.Info("运营通知去重丢弃", "notifyId", row.ID, "type", row.NotifyType, "profileId", row.ProfileID)
			continue
		}
		content := r.render(row, snapshot, supplement)
		if err := sendWecomText(r.client, r.webhookURL, content); err != nil {
			slog.Warn(
				"运营通知发送失败",
				"notifyId", row.ID,
				"type", row.NotifyType,
				"attempt", row.Attempts+1,
				"err", err,
			)
			if markErr := r.store.MarkNotificationFailed(row.ID, err.Error(), maxAttempts, now); markErr != nil {
				slog.Warn("运营通知失败落账失败", "notifyId", row.ID, "err", markErr)
			}
			delete(r.heldNotified, row.ID)
			summary.Failed++
			continue
		}
		sentWithWechat := row.NotifyType == store.NotificationTypeInterviewAccepted &&
			strings.TrimSpace(snapshot.WechatID) != ""
		if err := r.store.MarkNotificationSent(row.ID, sentWithWechat, now); err != nil {
			slog.Warn("运营通知发送落账失败", "notifyId", row.ID, "err", err)
		}
		delete(r.heldNotified, row.ID)
		summary.Sent++
		slog.Info(
			"运营通知已发送",
			"notifyId", row.ID,
			"type", row.NotifyType,
			"profileId", row.ProfileID,
			"sentWithWechat", sentWithWechat,
		)
		r.sendScreenshots(row.ID, snapshot)
	}
	if summary.Sent > 0 || summary.Failed > 0 || summary.Dropped > 0 {
		slog.Info(
			"运营通知 tick 完成",
			"sent", summary.Sent,
			"failed", summary.Failed,
			"dropped", summary.Dropped,
			"held", summary.Held,
		)
	}
	return summary
}

type decision int

const (
	decisionSend decision = iota
	decisionHold
	decisionDrop
)

// decide 照抄旧项目:两类通知都走三资产 15 分钟闸门;微信互加通知先过
// 与约面通知的去重判定(约面后换到且号已随约面带出→drop;约面仍 pending→hold;
// 约面前换到→独立发,唯候选人主动的行在 2 小时并发窗口内并入约面通知)。
func (r *Runner) decide(
	row store.NotificationOutbox,
	snapshot *store.NotificationRenderSnapshot,
	gate store.NotificationCaptureGate,
	now time.Time,
) (decision, bool) {
	supplement := false
	if row.NotifyType == store.NotificationTypeWechatAdded {
		verdict, isSupplement := r.decideWechatAdded(row, now)
		if verdict != decisionSend {
			return verdict, false
		}
		supplement = isSupplement
	}
	return r.gateReady(row, snapshot, gate, now), supplement
}

// decideWechatAdded 第二个返回值表示"这条是约面通知的补号"——只在约面通知
// 确实已经发到运营手上、却因当时还没收到号而写了"联系方式:未获取"时为真。
// 此时运营视角里它不是新事件,而是刚才那条面试确认的补丁,标题据此改写。
// 约面通知终败/过期时运营根本没收到过面试确认,仍按独立的微信互加渲染。
//
// 2026-08-06 甲方裁决新增 2 小时并发窗口:候选人主动换到微信、且尚无约面通知
// 时,本通知从入队起最多按住 2 小时;窗口内约面成功则不再单发,号随面试确认
// 一并带出(本行走下方状态机落 drop);到点仍无约面才单独发出。我方发起邀请
// 与发起方判不出的行维持立即发送的旧行为。
func (r *Runner) decideWechatAdded(row store.NotificationOutbox, now time.Time) (decision, bool) {
	meeting, err := r.store.InterviewNotificationForProfile(row.ProfileID)
	if err != nil {
		slog.Warn("微信互加去重查询失败,按独立发送处理", "notifyId", row.ID, "err", err)
		return decisionSend, false
	}
	peerInitiated := store.WechatAddedExchangeInitiator(row.PayloadJSON) == store.WechatExchangeInitiatorPeer
	if meeting == nil {
		if peerInitiated && now.Sub(row.CreatedAt) < wechatMergeWindow {
			return decisionHold, false // 并发窗口内:等约面一起走
		}
		return decisionSend, false // 无约面通知:独立事件
	}
	if row.ID < meeting.ID {
		// 微信在约面之前就换到。候选人主动且约面落在并发窗口内→并入约面通知
		// (走下方状态机,由约面通知的结局决定 drop/补号/独立发);否则维持
		// 独立发送的旧行为。
		if !peerInitiated || !meeting.CreatedAt.Before(row.CreatedAt.Add(wechatMergeWindow)) {
			return decisionSend, false
		}
	}
	switch meeting.Status {
	case store.NotificationStatusPending:
		return decisionHold, false // 约面通知尚未落定,号可能随它带出,等它先判
	case store.NotificationStatusSent:
		if meeting.SentWithWechat {
			return decisionDrop, false
		}
		return decisionSend, true // 约面 15 分钟兜底先发且未带号:补号
	default:
		return decisionSend, false // 约面终败/过期:运营没见过面试确认,按独立事件发
	}
}

func missingAssets(snapshot *store.NotificationRenderSnapshot) []string {
	missing := []string{}
	if strings.TrimSpace(snapshot.WechatID) == "" {
		missing = append(missing, "wechat")
	}
	if snapshot.ChatShot == nil {
		missing = append(missing, "chat")
	}
	if snapshot.ResumeShot == nil {
		missing = append(missing, "resume")
	}
	return missing
}

// staleAssets 报告哪几张图早于最近一次取证派发——那一轮的新图还没落库,此刻
// 发出去带的是上一次事件拍的旧图。图缺失不算 stale(那归 missingAssets 管)。
func staleAssets(
	snapshot *store.NotificationRenderSnapshot,
	dispatchedAt *time.Time,
) []string {
	if dispatchedAt == nil {
		return nil
	}
	stale := []string{}
	if snapshot.ChatShot != nil && snapshot.ChatShot.CreatedAt.Before(*dispatchedAt) {
		stale = append(stale, "chat")
	}
	if snapshot.ResumeShot != nil && snapshot.ResumeShot.CreatedAt.Before(*dispatchedAt) {
		stale = append(stale, "resume")
	}
	return stale
}

// gateReady 三样齐、且这一轮的新图确实已经落库,才放行。两道取证闸各挡一段
// (见 store.NotificationCaptureGate):少挡任何一段,通知都会带着上一次事件拍
// 的旧图发出——2026-08-10 客户机补号行 19 发的是前一天晚上那张。取证那轮若
// 始终没跑成(会话未绑定、重开失败、截图命令本身失败),15 分钟兜底照常放行,
// 代价是图旧、通知晚,不影响正文完整。
func (r *Runner) gateReady(
	row store.NotificationOutbox,
	snapshot *store.NotificationRenderSnapshot,
	gate store.NotificationCaptureGate,
	now time.Time,
) decision {
	missing := missingAssets(snapshot)
	stale := staleAssets(snapshot, gate.LastDispatchAt)
	if len(missing) == 0 && !gate.Pending && len(stale) == 0 {
		return decisionSend // 三样齐、新图已就位:立即发
	}
	if now.Sub(row.CreatedAt) >= screenshotGateWindow {
		slog.Warn(
			"运营通知到点兜底发送(缺资产也发)",
			"notifyId", row.ID,
			"type", row.NotifyType,
			"profileId", row.ProfileID,
			"missing", missing,
			"pendingCapture", gate.Pending,
			"stale", stale,
		)
		return decisionSend
	}
	return decisionHold
}

func (r *Runner) render(
	row store.NotificationOutbox,
	snapshot *store.NotificationRenderSnapshot,
	supplement bool,
) string {
	customer := ""
	if r.customerName != nil {
		customer = strings.TrimSpace(r.customerName())
	}
	if row.NotifyType == store.NotificationTypeWechatAdded {
		return renderWechatAdded(snapshot, customer, supplement)
	}
	return renderInterviewAccepted(snapshot, customer)
}

// sendScreenshots 文本主通知成功后按「先聊天后简历」追发;任何失败静默降级,
// 不影响已 sent 的主通知。日志只出现字节数与哈希引用。
func (r *Runner) sendScreenshots(notifyID uint64, snapshot *store.NotificationRenderSnapshot) {
	shots := []struct {
		label string
		shot  *store.CandidateScreenshot
	}{
		{"chat", snapshot.ChatShot},
		{"resume", snapshot.ResumeShot},
	}
	for _, entry := range shots {
		if entry.shot == nil {
			continue
		}
		data, err := r.blobs.ReadFile(entry.shot.BlobRef)
		if err != nil {
			slog.Warn("追发截图读取失败", "notifyId", notifyID, "kind", entry.label, "err", err)
			continue
		}
		if err := sendWecomImage(r.client, r.webhookURL, data); err != nil {
			if _, skipped := err.(*skippedImageError); skipped {
				slog.Info("追发截图跳过", "notifyId", notifyID, "kind", entry.label, "reason", err.Error())
			} else {
				slog.Warn("追发截图发送失败", "notifyId", notifyID, "kind", entry.label, "err", err)
			}
			continue
		}
		slog.Info("追发截图成功", "notifyId", notifyID, "kind", entry.label, "byteSize", len(data))
	}
}
