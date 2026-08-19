package store

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
// failed 族是完整尝试被前置闸拒绝 —— 按裁决当日不重试;missedSlot 是
// 到点时脑没在运行(晚开机/睡眠错过),当日跳过的落库痕迹。
const (
	AutoStartOutcomeStarted      = "started"
	AutoStartOutcomeResumed      = "resumed"
	AutoStartOutcomeStartFailed  = "startFailed"
	AutoStartOutcomeResumeFailed = "resumeFailed"
	AutoStartOutcomeSkippedRun   = "skippedActiveRun"
	AutoStartOutcomeSkippedToday = "skippedAlreadyRanToday"
	AutoStartOutcomeMissedSlot   = "missedSlot"
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
	return s.updateAutoStartColumns(map[string]any{"enabled": enabled})
}

// RecordAutoStartAttempt 记一次当日尝试的结果,覆盖上一次 —— 设置页只需要
// 回答"最近一次怎么样",历史由普通日志承担。
func (s *Store) RecordAutoStartAttempt(at time.Time, outcome, detail string) error {
	return s.updateAutoStartColumns(map[string]any{
		"last_attempt_at": at,
		"last_outcome":    outcome,
		"last_detail":     detail,
	})
}

// updateAutoStartColumns 只写指定列,不做整行读-改-写:开关(HTTP goroutine)
// 与尝试记录(触发器 goroutine)并发落库时,整行 Save 会把对方刚写的列用
// 陈旧读回盖掉 —— 最贵的形态是"落账把用户刚关掉的开关写回开"。
func (s *Store) updateAutoStartColumns(values map[string]any) error {
	res := s.db.Model(&AutoStartSetting{}).
		Where("id = ?", autoStartSettingID).Updates(values)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		return nil
	}
	row := AutoStartSetting{ID: autoStartSettingID}
	if err := s.db.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&row).Error; err != nil {
		return err
	}
	return s.db.Model(&AutoStartSetting{}).
		Where("id = ?", autoStartSettingID).Updates(values).Error
}
