package store

import (
	"errors"
	"testing"
)

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
	if setting.DetailEnabled || setting.Position != "" || setting.EffectivePosition() != StatusBarPositionTop {
		t.Fatalf("新库应是客户版零值、位置归一到顶部: %+v", setting)
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

func TestStatusBarPositionRoundTripAndRejectsUnknown(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.SetStatusBarPosition("middle"); !errors.Is(err, ErrStatusBarPositionInvalid) {
		t.Fatalf("未知档位应被拒绝,得到 %v", err)
	}
	if err := s.SetStatusBarPosition(StatusBarPositionBottomRight); err != nil {
		t.Fatalf("SetStatusBarPosition: %v", err)
	}
	// 位置与开关各写各的列:改开关不得把位置刷回去。
	if err := s.SetStatusBarDetail(true); err != nil {
		t.Fatalf("SetStatusBarDetail: %v", err)
	}
	setting, err := s.StatusBarSetting()
	if err != nil {
		t.Fatalf("StatusBarSetting: %v", err)
	}
	if setting.Position != StatusBarPositionBottomRight || !setting.DetailEnabled {
		t.Fatalf("两列应各自保留: %+v", setting)
	}
	for _, position := range StatusBarPositions {
		if err := s.SetStatusBarPosition(position); err != nil {
			t.Fatalf("枚举内的 %q 应可写: %v", position, err)
		}
	}
}

// 显示开关:没人拨过时 BOSS 开、其余(含快照缺席的空平台)关;拨过之后不再看平台。
func TestStatusBarVisibleFollowsPlatformUntilSetManually(t *testing.T) {
	var auto StatusBarSetting
	if !auto.EffectiveVisible("boss") || auto.EffectiveVisible("zhilian") || auto.EffectiveVisible("") {
		t.Fatalf("默认应 BOSS 开、其余关")
	}
	if auto.VisibleSource() != "platformDefault" {
		t.Fatalf("没拨过应标 platformDefault,得到 %q", auto.VisibleSource())
	}
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.SetStatusBarVisible(true); err != nil {
		t.Fatalf("SetStatusBarVisible: %v", err)
	}
	setting, err := s.StatusBarSetting()
	if err != nil {
		t.Fatalf("StatusBarSetting: %v", err)
	}
	if !setting.EffectiveVisible("zhilian") || setting.VisibleSource() != "manual" {
		t.Fatalf("明示开后智联也应显示: %+v", setting)
	}
	if err := s.SetStatusBarVisible(false); err != nil {
		t.Fatalf("SetStatusBarVisible(false): %v", err)
	}
	setting, _ = s.StatusBarSetting()
	if setting.EffectiveVisible("boss") {
		t.Fatalf("明示关后 BOSS 也应隐藏: %+v", setting)
	}
}
