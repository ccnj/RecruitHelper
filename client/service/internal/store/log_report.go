package store

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// LogReportSetting 是日志上报的本机状态(AGENTS.md「全局约定·日志上报」,
// 2026-08-06 甲方裁决)。单行表,ID 恒为 1。
//
// **这里没有开关。** 2026-08-06 甲方当日修订:上报常开、不设开关,理由是
// "只是上报日志,不干过分的事"。这张表只记结果与计数,供诊断台显示 ——
// 它们是状态,不是开关。旧的 enabled 列在已装机器上还留着,不再读写。
type LogReportSetting struct {
	ID uint `gorm:"primaryKey"`
	// 上次上报的时刻与结果,供诊断台显示。上报是后台静默进行的,
	// "到底传没传出去"只能靠这里回答。
	LastAt    *time.Time
	LastOK    bool
	LastError string
	// 累计发出与丢弃的条数。丢弃量是裁决要求"如实告知、不得静默丢弃"的那一半,
	// 除了随载荷上报,本机也要能看见。
	SentCount    int64
	DroppedCount int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

const logReportSettingID = 1

// LogReportSetting 读当前状态。表里没有行时返回零值,不隐式建行。
func (s *Store) LogReportSetting() (LogReportSetting, error) {
	var setting LogReportSetting
	err := s.db.First(&setting, logReportSettingID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return LogReportSetting{ID: logReportSettingID}, nil
		}
		return LogReportSetting{}, err
	}
	return setting, nil
}

// RecordLogReportRun 记一次上报结果与计数。成功时清掉上次的失败原因,
// 否则诊断台会一直挂着一条早已解决的旧错误。
func (s *Store) RecordLogReportRun(at time.Time, ok bool, reason string, sent, dropped int64) error {
	setting, err := s.LogReportSetting()
	if err != nil {
		return err
	}
	setting.ID = logReportSettingID
	setting.LastAt = &at
	setting.LastOK = ok
	if ok {
		setting.LastError = ""
	} else {
		setting.LastError = reason
	}
	setting.SentCount += sent
	setting.DroppedCount += dropped
	return s.db.Save(&setting).Error
}
