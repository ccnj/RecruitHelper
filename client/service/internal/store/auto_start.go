package store

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// AutoStartSetting 是「每日自动开始」的本机开关与上次尝试结果(2026-08-19 甲方
// 裁决,AGENTS.md 统一业务运行窗口条款)。单行表,ID 恒为 1。
//
// 默认关闭:裁决要求"仅本机产品 UI 可改、显式开启才构成授权",所以 Enabled 是
// 零值 false 的普通 bool —— 新建行、加列、重装,拿到的都是关。
type AutoStartSetting struct {
	ID      uint `gorm:"primaryKey"`
	Enabled bool
	// 上次自动尝试的时刻与结果。无人在场时,"今天早上到底开没开、为什么没开"
	// 只能靠这里回答;outcome 是封闭码,detail 是给设置页看的中文原因。
	LastAttemptAt *time.Time
	LastOutcome   string
	LastDetail    string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// 上次尝试结果的封闭码。started/resumed 是成功;skipped 族当天什么都没做;
// failed 族是完整尝试被前置闸拒绝 —— 按裁决当日不重试。
const (
	AutoStartOutcomeStarted      = "started"
	AutoStartOutcomeResumed      = "resumed"
	AutoStartOutcomeStartFailed  = "startFailed"
	AutoStartOutcomeResumeFailed = "resumeFailed"
	AutoStartOutcomeSkippedRun   = "skippedActiveRun"
	AutoStartOutcomeSkippedToday = "skippedAlreadyRanToday"
	AutoStartOutcomeError        = "error"
)

const autoStartSettingID = 1

// AutoStartSetting 读当前设置。表里没有行时返回零值(即开关关闭),不隐式建行。
func (s *Store) AutoStartSetting() (AutoStartSetting, error) {
	var setting AutoStartSetting
	err := s.db.First(&setting, autoStartSettingID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return AutoStartSetting{ID: autoStartSettingID}, nil
		}
		return AutoStartSetting{}, err
	}
	return setting, nil
}

// SetAutoStartEnabled 落开关。只应由产品设置页的人工点击调用。
func (s *Store) SetAutoStartEnabled(enabled bool) error {
	setting, err := s.AutoStartSetting()
	if err != nil {
		return err
	}
	setting.ID = autoStartSettingID
	setting.Enabled = enabled
	return s.db.Save(&setting).Error
}

// RecordAutoStartAttempt 记一次当日尝试的结果,覆盖上一次 —— 设置页只需要
// 回答"最近一次怎么样",历史由普通日志承担。
func (s *Store) RecordAutoStartAttempt(at time.Time, outcome, detail string) error {
	setting, err := s.AutoStartSetting()
	if err != nil {
		return err
	}
	setting.ID = autoStartSettingID
	setting.LastAttemptAt = &at
	setting.LastOutcome = outcome
	setting.LastDetail = detail
	return s.db.Save(&setting).Error
}
