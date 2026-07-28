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
	screenshotGateWindow = 15 * time.Minute   // 三资产闸门:齐即发,否则最多等 15 分钟兜底
	staleAfter           = 7 * 24 * time.Hour // 超龄未发出标 expired,留表可查
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
		verdict, supplement := r.decide(row, snapshot, now)
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
// 约面前换到→永远独立发)。
func (r *Runner) decide(
	row store.NotificationOutbox,
	snapshot *store.NotificationRenderSnapshot,
	now time.Time,
) (decision, bool) {
	supplement := false
	if row.NotifyType == store.NotificationTypeWechatAdded {
		verdict, isSupplement := r.decideWechatAdded(row)
		if verdict != decisionSend {
			return verdict, false
		}
		supplement = isSupplement
	}
	return r.gateReady(row, snapshot, now), supplement
}

// decideWechatAdded 第二个返回值表示"这条是约面通知的补号"——只在约面通知
// 确实已经发到运营手上、却因当时还没收到号而写了"联系方式:未获取"时为真。
// 此时运营视角里它不是新事件,而是刚才那条面试确认的补丁,标题据此改写。
// 约面通知终败/过期时运营根本没收到过面试确认,仍按独立的微信互加渲染。
func (r *Runner) decideWechatAdded(row store.NotificationOutbox) (decision, bool) {
	meeting, err := r.store.InterviewNotificationForProfile(row.ProfileID)
	if err != nil {
		slog.Warn("微信互加去重查询失败,按独立发送处理", "notifyId", row.ID, "err", err)
		return decisionSend, false
	}
	if meeting == nil {
		return decisionSend, false // 无约面通知:独立事件
	}
	if row.ID < meeting.ID {
		return decisionSend, false // 微信在约面之前就换到:独立事件,始终发
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

func (r *Runner) gateReady(
	row store.NotificationOutbox,
	snapshot *store.NotificationRenderSnapshot,
	now time.Time,
) decision {
	missing := missingAssets(snapshot)
	if len(missing) == 0 {
		return decisionSend // 三样齐立即发
	}
	if now.Sub(row.CreatedAt) >= screenshotGateWindow {
		slog.Warn(
			"运营通知到点兜底发送(缺资产也发)",
			"notifyId", row.ID,
			"type", row.NotifyType,
			"profileId", row.ProfileID,
			"missing", missing,
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
