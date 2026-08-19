// 日报账本与只读投影(AGENTS.md「运营通知 webhook·每日日报」,2026-08-18 甲方
// 裁决)。日报行与候选人级通知共用 NotificationOutbox 发件箱:event_key 按本地
// 日期幂等、终态只标记不删除;渲染在发送时刻按最新账本现算,行内不存正文。
// 计数与工作状态上报共用 app_projection.go 的同一批窗口查询(条款要求同源),
// 名单复用邀面卡"每人最新一张未撤回"的既有口径。
package store

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm/clause"
)

type dailyReportPayload struct {
	LocalDate string `json:"localDate"`
}

// DailyReportEventKey 是日报行的幂等键;localDate 为客户端本地日期(2006-01-02)。
func DailyReportEventKey(localDate string) string { return "dailyReport:" + localDate }

// EnqueueDailyReport 幂等入队当日日报;同日重复调用由 event_key 唯一索引吸收,
// 返回值表示本次是否真的新插入了一行。
func (s *Store) EnqueueDailyReport(localDate string, at time.Time) (bool, error) {
	localDate = strings.TrimSpace(localDate)
	if localDate == "" {
		return false, errors.New("日报入队缺少本地日期")
	}
	body, err := json.Marshal(dailyReportPayload{LocalDate: localDate})
	if err != nil {
		return false, err
	}
	row := NotificationOutbox{
		NotifyType:  NotificationTypeDailyReport,
		EventKey:    DailyReportEventKey(localDate),
		ProfileID:   "",
		PayloadJSON: string(body),
		Status:      NotificationStatusPending,
		CreatedAt:   at,
		UpdatedAt:   at,
	}
	result := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "event_key"}},
		DoNothing: true,
	}).Create(&row)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// DailyReportLocalDate 解出日报行覆盖的发送日;载荷异常返回空串,由调用方按
// 陈旧处理(条款:隔日不补发)。
func DailyReportLocalDate(payloadJSON string) string {
	var payload dailyReportPayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.LocalDate)
}

// DailyReportCounts 是日报「昨日成果」的封闭字段面:只有换微信与约面两项
// (2026-08-18 甲方裁决,过程量与负向指标不进日报)。
type DailyReportCounts struct {
	Wechat       int64
	Appointments int64
}

// DailyReportCounts 按窗口数两项人数,与工作状态上报走同一批查询实现。
func (s *Store) DailyReportCounts(
	platform, accountRef string,
	start, end time.Time,
) (DailyReportCounts, error) {
	out := DailyReportCounts{}
	wechat, err := appWechatProfileCountTx(s.db, platform, accountRef, start, end)
	if err != nil {
		return out, err
	}
	out.Wechat = wechat
	appointments, err := appNewAppointmentsCountTx(s.db, platform, accountRef, start, end)
	if err != nil {
		return out, err
	}
	out.Appointments = appointments
	return out, nil
}

// DailyReportInterview 是待面试名单一行的封闭字段面(条款:仅姓名、职位、
// 面试时间、方式)。
type DailyReportInterview struct {
	DisplayName   string
	PositionTitle string
	StartsAtMs    int64
	Method        string
}

// DailyReportInterviews 列未来待面试:主线已约面、且最新未撤回邀面卡的开始时间
// 不早于 fromMs(当日 00:00),无上界,按时间升序。已约面后被平台拉黑者数据上
// 分辨不出、会照常在列(立案文档已知边界,记录级)。
func (s *Store) DailyReportInterviews(
	platform, accountRef string,
	fromMs int64,
) ([]DailyReportInterview, error) {
	rows, err := appInterviewCardRowsTx(s.db, platform, accountRef, fromMs, nil, true)
	if err != nil {
		return nil, err
	}
	out := make([]DailyReportInterview, 0, len(rows))
	for _, row := range rows {
		out = append(out, DailyReportInterview{
			DisplayName:   row.DisplayName,
			PositionTitle: row.JobName,
			StartsAtMs:    row.StartsAtMs,
			Method:        row.Method,
		})
	}
	return out, nil
}
