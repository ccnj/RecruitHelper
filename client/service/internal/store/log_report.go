package store

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// LogReportSetting 是日志上报的本机开关(AGENTS.md「全局约定·日志上报」,
// 2026-08-06 甲方裁决)。单行表,ID 恒为 1。
//
// 与 FieldReportSetting 是两个独立开关,不共用:那个管"整包每天自动传",这个管
// "出事就推一条"。两件事的风险与频次都不是一个量级,合成一个开关会让人在想开
// 其中一个时被迫连另一个一起开。
//
// **默认关闭是这张表最重要的性质**:裁决写死了"只有人在同机诊断台显式点击这一
// 条路径能开启",安装、升级、迁移、重装、配置下发与任何默认值填充都不得使其变为
// 开启。所以 Enabled 是零值 false 的普通 bool —— 新建行、加列、重装,拿到的都是关。
type LogReportSetting struct {
	ID      uint `gorm:"primaryKey"`
	Enabled bool
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

// LogReportSetting 读当前设置。表里没有行时返回零值(即开关关闭),不隐式建行。
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

// SetLogReportEnabled 落开关。只应由诊断台的人工点击调用。
func (s *Store) SetLogReportEnabled(enabled bool) error {
	setting, err := s.LogReportSetting()
	if err != nil {
		return err
	}
	setting.ID = logReportSettingID
	setting.Enabled = enabled
	return s.db.Save(&setting).Error
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
