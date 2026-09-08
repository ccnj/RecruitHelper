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
	// Position 是小窗贴在主屏工作区的哪个角(封闭枚举,见 StatusBarPositions)。
	// 空串按顶部居中——旧库加列后拿到的就是空串,与首版行为一致。
	Position  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// 位置档位。名字直接是 Electron 侧几何函数的锚点名,两边不做翻译。
const (
	StatusBarPositionTop         = "top"
	StatusBarPositionBottom      = "bottom"
	StatusBarPositionTopLeft     = "topLeft"
	StatusBarPositionTopRight    = "topRight"
	StatusBarPositionBottomLeft  = "bottomLeft"
	StatusBarPositionBottomRight = "bottomRight"
)

// StatusBarPositions 是允许写入的全集;顺序即诊断台的展示顺序。
var StatusBarPositions = []string{
	StatusBarPositionTop, StatusBarPositionBottom,
	StatusBarPositionTopLeft, StatusBarPositionTopRight,
	StatusBarPositionBottomLeft, StatusBarPositionBottomRight,
}

// ErrStatusBarPositionInvalid:不在枚举里的位置一律拒绝,不猜最近的。
var ErrStatusBarPositionInvalid = errors.New("状态栏位置不在允许的档位里")

func validStatusBarPosition(position string) bool {
	for _, allowed := range StatusBarPositions {
		if allowed == position {
			return true
		}
	}
	return false
}

// EffectivePosition 把空串归一到顶部居中,读方不必各自判空。
func (s StatusBarSetting) EffectivePosition() string {
	if s.Position == "" {
		return StatusBarPositionTop
	}
	return s.Position
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
func (s *Store) SetStatusBarDetail(enabled bool) error {
	return s.updateStatusBarColumns(map[string]any{"detail_enabled": enabled})
}

// SetStatusBarPosition 落位置档位。不在枚举里直接拒绝。
func (s *Store) SetStatusBarPosition(position string) error {
	if !validStatusBarPosition(position) {
		return ErrStatusBarPositionInvalid
	}
	return s.updateStatusBarColumns(map[string]any{"position": position})
}

// updateStatusBarColumns 只写指定列而不整行 Save:与其余单行设置表同一纪律,
// 两个开关各写各的列,谁也不会用陈旧读回盖掉对方刚落的值。
func (s *Store) updateStatusBarColumns(values map[string]any) error {
	res := s.db.Model(&StatusBarSetting{}).
		Where("id = ?", statusBarSettingID).Updates(values)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		return nil
	}
	row := StatusBarSetting{ID: statusBarSettingID}
	if err := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	return s.db.Model(&StatusBarSetting{}).
		Where("id = ?", statusBarSettingID).Updates(values).Error
}
