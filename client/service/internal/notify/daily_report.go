package notify

// 日报(AGENTS.md「运营通知 webhook·每日日报」,2026-08-18 甲方裁决)。触发:
// 本地时间 (08:30 + 客户id 分钟) 起、进程在运行时的首个检查点;按本地日期
// event_key 幂等,开机晚随到随发、不设当日截止、隔日不补发。槽位错峰是为转发
// 真机的节奏与周一 09:00 段的服务端周报让路。正文是给客户群看的交付物台账:
// 零也照发、格式恒定,名单为空固定写「暂无」。

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"recruithelper/client/service/internal/store"
)

const (
	dailyReportBaseHour   = 8
	dailyReportBaseMinute = 30
)

// DailyReportLedger 是日报对账本的最窄依赖面(*store.Store 天然满足)。
type DailyReportLedger interface {
	EnqueueDailyReport(localDate string, at time.Time) (bool, error)
	DailyReportCounts(platform, accountRef string, start, end time.Time) (store.DailyReportCounts, error)
	DailyReportInterviews(platform, accountRef string, fromMs int64) ([]store.DailyReportInterview, error)
}

// DailyReportDeps 由装配方注入;不注入时 runner 不产生日报(既有通知行为不变)。
type DailyReportDeps struct {
	Ledger DailyReportLedger
	// CustomerID 返回旧后台自增客户 id(bind 响应下发、存于本机配置),它同时是
	// 槽位偏移;<=0 表示尚未激活,不入队——没有客户名的日报对运营无意义。
	CustomerID func() int
	// Runtime 返回当前平台账号绑定;未绑定返回空串,日报照发(计数为零、名单空),
	// 台账语义:没绑定账号的日子,零就是真实的零。
	Runtime func() (platform, accountRef string)
}

func (r *Runner) SetDailyReportDeps(deps DailyReportDeps) *Runner {
	r.daily = deps
	return r
}

func localDateOf(now time.Time) string { return now.Format("2006-01-02") }

// maybeEnqueueDailyReport 每 tick 检查:到槽位且今天还没入队就幂等入队。进程内
// 用 dailyEnqueuedFor 挡重复插入;重启后至多多做一次被唯一索引吸收的空插入。
func (r *Runner) maybeEnqueueDailyReport(now time.Time) {
	if r.daily.Ledger == nil || r.daily.CustomerID == nil {
		return
	}
	today := localDateOf(now)
	if r.dailyEnqueuedFor == today {
		return
	}
	customerID := r.daily.CustomerID()
	if customerID <= 0 {
		return // 未激活:静默等待,激活后的下一个 tick 自然接上
	}
	slot := time.Date(
		now.Year(), now.Month(), now.Day(),
		dailyReportBaseHour, dailyReportBaseMinute, 0, 0, now.Location(),
	).Add(time.Duration(customerID) * time.Minute)
	if now.Before(slot) {
		return
	}
	inserted, err := r.daily.Ledger.EnqueueDailyReport(today, now)
	if err != nil {
		slog.Warn("日报入队失败", "err", err)
		return
	}
	r.dailyEnqueuedFor = today
	if inserted {
		slog.Info("日报已入队", "localDate", today, "slot", slot.Format("15:04"))
	}
}

// handleDailyReport 处理发件箱里的日报行。行日期不是今天→标 skipped(条款:隔日
// 不补发);计数或名单读失败→按失败重试,不发零蛋假账(诚实高于好看)。
func (r *Runner) handleDailyReport(row store.NotificationOutbox, now time.Time, summary *tickSummary) {
	rowDate := store.DailyReportLocalDate(row.PayloadJSON)
	if rowDate != localDateOf(now) {
		if err := r.store.MarkNotificationSkipped(row.ID, "staleDailyReport", now); err != nil {
			slog.Warn("日报陈旧落账失败", "notifyId", row.ID, "err", err)
			return
		}
		summary.Dropped++
		slog.Info("日报陈旧丢弃(隔日不补发)", "notifyId", row.ID, "rowDate", rowDate)
		return
	}
	platform, accountRef := "", ""
	if r.daily.Runtime != nil {
		platform, accountRef = r.daily.Runtime()
	}
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	counts := store.DailyReportCounts{}
	interviews := []store.DailyReportInterview{}
	if platform != "" && accountRef != "" {
		var err error
		counts, err = r.daily.Ledger.DailyReportCounts(
			platform, accountRef, todayStart.AddDate(0, 0, -1), todayStart,
		)
		if err == nil {
			interviews, err = r.daily.Ledger.DailyReportInterviews(
				platform, accountRef, todayStart.UnixMilli(),
			)
		}
		if err != nil {
			slog.Warn("日报数据读取失败", "notifyId", row.ID, "err", err)
			if markErr := r.store.MarkNotificationFailed(row.ID, err.Error(), maxAttempts, now); markErr != nil {
				slog.Warn("日报失败落账失败", "notifyId", row.ID, "err", markErr)
			}
			summary.Failed++
			return
		}
	}
	customer := ""
	if r.customerName != nil {
		customer = strings.TrimSpace(r.customerName())
	}
	content := renderDailyReport(customer, todayStart.AddDate(0, 0, -1), counts, interviews)
	if err := sendWecomText(r.client, r.webhookURL, content); err != nil {
		slog.Warn("日报发送失败", "notifyId", row.ID, "attempt", row.Attempts+1, "err", err)
		if markErr := r.store.MarkNotificationFailed(row.ID, err.Error(), maxAttempts, now); markErr != nil {
			slog.Warn("日报失败落账失败", "notifyId", row.ID, "err", markErr)
		}
		summary.Failed++
		return
	}
	if err := r.store.MarkNotificationSent(row.ID, false, now); err != nil {
		slog.Warn("日报发送落账失败", "notifyId", row.ID, "err", err)
	}
	summary.Sent++
	slog.Info(
		"日报已发送",
		"notifyId", row.ID,
		"wechat", counts.Wechat,
		"appointments", counts.Appointments,
		"interviews", len(interviews),
	)
}

// renderDailyReport 渲染日报正文。台账原则(2026-08-18 甲方裁决):零也照发、
// 格式与非零完全一致;不含独立日期行、环比、过程量与负向指标。段头括注的是
// 数据所属的昨日日期(2026-08-19 甲方修订)——日报到达时间浮动,括注钉死数字归属。
func renderDailyReport(
	customerName string,
	yesterday time.Time,
	counts store.DailyReportCounts,
	interviews []store.DailyReportInterview,
) string {
	title := "【工作日报】" + customerName
	lines := []string{
		title,
		"",
		fmt.Sprintf("昨日成果(%02d-%02d)", yesterday.Month(), yesterday.Day()),
		fmt.Sprintf("换到微信:%d 人", counts.Wechat),
		fmt.Sprintf("约成面试:%d 人", counts.Appointments),
		"",
		"待面试安排",
	}
	if len(interviews) == 0 {
		lines = append(lines, "暂无")
	} else {
		for _, interview := range interviews {
			lines = append(lines, dailyInterviewLine(interview))
		}
	}
	return truncateBytes(strings.Join(lines, "\n"), wecomTextLimitBytes)
}

// dailyInterviewLine 渲染名单一行:时间  方式  姓名(职位)。方式文案复用约面
// 通知的标签表(条款要求同一场面试两处叫法一致),枚举外省略该段。
func dailyInterviewLine(interview store.DailyReportInterview) string {
	starts := interview.StartsAtMs
	parts := []string{formatInterviewTime(&starts)}
	if method, ok := interviewMethodLabels[interview.Method]; ok {
		parts = append(parts, method)
	}
	name := strings.TrimSpace(interview.DisplayName)
	if name == "" {
		name = "候选人"
	}
	if job := strings.TrimSpace(interview.PositionTitle); job != "" {
		name += "(" + job + ")"
	}
	parts = append(parts, name)
	return strings.Join(parts, "  ")
}
