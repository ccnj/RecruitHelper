package store

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// FieldReportSetting 是现场数据上报的本机开关与上次自动执行的结果(2026-07-31
// 补充裁决)。单行表,ID 恒为 1。
//
// **默认关闭是这张表最重要的性质**:裁决写死了"只有人在诊断台显式点击这一条路径
// 能把它打开",安装、升级、迁移、重装与任何默认值填充都不得使其变为开启。所以
// AutoUploadEnabled 是零值 false 的普通 bool —— 新建行、加列、重装,拿到的都是关。
type FieldReportSetting struct {
	ID                uint `gorm:"primaryKey"`
	AutoUploadEnabled bool
	// 上次自动执行的时刻与结果。失败原因留着给诊断台显示 —— 无人值守时,
	// "上次传成功没有"只能靠这里回答。
	LastAutoAt    *time.Time
	LastAutoOK    bool
	LastAutoError string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

const fieldReportSettingID = 1

// FieldReportSetting 读当前设置。表里没有行时返回零值(即开关关闭),不隐式建行。
func (s *Store) FieldReportSetting() (FieldReportSetting, error) {
	var setting FieldReportSetting
	err := s.db.First(&setting, fieldReportSettingID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return FieldReportSetting{ID: fieldReportSettingID}, nil
		}
		return FieldReportSetting{}, err
	}
	return setting, nil
}

// SetFieldReportAutoUpload 落开关。只应由诊断台的人工点击调用。
func (s *Store) SetFieldReportAutoUpload(enabled bool) error {
	setting, err := s.FieldReportSetting()
	if err != nil {
		return err
	}
	setting.ID = fieldReportSettingID
	setting.AutoUploadEnabled = enabled
	return s.db.Save(&setting).Error
}

// RecordFieldReportAutoRun 记一次自动执行的结果。成功时清掉上次的失败原因,
// 否则诊断台会一直挂着一条早已解决的旧错误。
func (s *Store) RecordFieldReportAutoRun(at time.Time, ok bool, reason string) error {
	setting, err := s.FieldReportSetting()
	if err != nil {
		return err
	}
	setting.ID = fieldReportSettingID
	setting.LastAutoAt = &at
	setting.LastAutoOK = ok
	if ok {
		setting.LastAutoError = ""
	} else {
		setting.LastAutoError = reason
	}
	return s.db.Save(&setting).Error
}
