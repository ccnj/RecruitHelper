package store

import (
	"testing"
	"time"
)

// 默认关闭是这张表最重要的性质:新库、读取,都不得把开关带成开,也不得隐式建行。
func TestAutoStartSettingDefaultsOffWithoutCreatingRow(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	setting, err := s.AutoStartSetting()
	if err != nil {
		t.Fatalf("AutoStartSetting: %v", err)
	}
	if setting.Enabled || setting.LastOutcome != "" || setting.LastAttemptAt != nil {
		t.Fatalf("新库应是关闭零值: %+v", setting)
	}
	var count int64
	if err := s.db.Model(&AutoStartSetting{}).Count(&count).Error; err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 0 {
		t.Fatalf("读取不得隐式建行: count=%d", count)
	}
}

func TestAutoStartSettingEnableAndRecordRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.SetAutoStartEnabled(true); err != nil {
		t.Fatalf("SetAutoStartEnabled: %v", err)
	}
	at := time.Date(2026, 8, 20, 7, 12, 30, 0, time.Local)
	if err := s.RecordAutoStartAttempt(at, AutoStartOutcomeStartFailed, "Chrome 插件未连接"); err != nil {
		t.Fatalf("RecordAutoStartAttempt: %v", err)
	}
	setting, err := s.AutoStartSetting()
	if err != nil {
		t.Fatalf("AutoStartSetting: %v", err)
	}
	if !setting.Enabled || setting.LastOutcome != AutoStartOutcomeStartFailed ||
		setting.LastDetail != "Chrome 插件未连接" ||
		setting.LastAttemptAt == nil || !setting.LastAttemptAt.Equal(at) {
		t.Fatalf("往返漂移: %+v", setting)
	}
	// 记录结果不得动开关;关掉开关也不得抹掉上次结果。
	if err := s.SetAutoStartEnabled(false); err != nil {
		t.Fatalf("SetAutoStartEnabled(false): %v", err)
	}
	setting, err = s.AutoStartSetting()
	if err != nil {
		t.Fatalf("AutoStartSetting: %v", err)
	}
	if setting.Enabled || setting.LastOutcome != AutoStartOutcomeStartFailed {
		t.Fatalf("关开关后上次结果应保留: %+v", setting)
	}
}
