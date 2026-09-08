package store

import "testing"

// 默认关闭是这张表最重要的性质:新库、读取,都不得把开关带成开,也不得隐式建行。
func TestStatusBarSettingDefaultsOffWithoutCreatingRow(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	setting, err := s.StatusBarSetting()
	if err != nil {
		t.Fatalf("StatusBarSetting: %v", err)
	}
	if setting.DetailEnabled {
		t.Fatalf("新库应是客户版零值: %+v", setting)
	}
	var count int64
	if err := s.db.Model(&StatusBarSetting{}).Count(&count).Error; err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 0 {
		t.Fatalf("读取不得隐式建行: count=%d", count)
	}
}

func TestStatusBarSettingToggleRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, want := range []bool{true, false, true} {
		if err := s.SetStatusBarDetail(want); err != nil {
			t.Fatalf("SetStatusBarDetail(%v): %v", want, err)
		}
		setting, err := s.StatusBarSetting()
		if err != nil {
			t.Fatalf("StatusBarSetting: %v", err)
		}
		if setting.DetailEnabled != want {
			t.Fatalf("开关应为 %v,读到 %+v", want, setting)
		}
	}
	var count int64
	if err := s.db.Model(&StatusBarSetting{}).Count(&count).Error; err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 1 {
		t.Fatalf("单行表只能有一行: count=%d", count)
	}
}
