package store

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// StatusBarSetting 是屏幕顶层状态栏的本机开关(2026-09-08 甲方裁决,出口见
// docs/boss/状态栏出口-2026-09-08.md)。单行表,ID 恒为 1。
//
// DetailEnabled 为真时状态栏画开发版:最近命令账本(带候选人姓名)、批次与插件
// 摘要;为假时只画客户版那一句话状态。默认关闭,且必须是零值语义:开发版把
// 候选人姓名常驻在屏幕最上层,安装、升级、迁移、重装都不得把它带成开——
// 只有人在诊断台显式点击这一条路径能打开。甲方裁定持久化,所以它落库而不是
// 留在主进程内存里。
type StatusBarSetting struct {
	ID            uint `gorm:"primaryKey"`
	DetailEnabled bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

const statusBarSettingID = 1

// StatusBarSetting 读当前设置。表里没有行时返回零值(即客户版),不隐式建行。
func (s *Store) StatusBarSetting() (StatusBarSetting, error) {
	var setting StatusBarSetting
	err := s.db.First(&setting, statusBarSettingID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return StatusBarSetting{ID: statusBarSettingID}, nil
		}
		return StatusBarSetting{}, err
	}
	return setting, nil
}

// SetStatusBarDetail 落开关。只应由诊断台的人工点击调用。
//
// 只写这一列而不整行 Save:与其余单行设置表同一纪律,免得将来加列后被陈旧读回
// 盖掉别的字段。
func (s *Store) SetStatusBarDetail(enabled bool) error {
	res := s.db.Model(&StatusBarSetting{}).
		Where("id = ?", statusBarSettingID).
		Update("detail_enabled", enabled)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		return nil
	}
	row := StatusBarSetting{ID: statusBarSettingID, DetailEnabled: enabled}
	if err := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	// 并发下另一方可能刚建了行且值不同;再更新一次确保本次意图落地。
	return s.db.Model(&StatusBarSetting{}).
		Where("id = ?", statusBarSettingID).
		Update("detail_enabled", enabled).Error
}
