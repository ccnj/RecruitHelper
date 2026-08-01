package m5ai

import (
	"testing"
)

// 内置默认周表是七天全 [09:00,18:00)（2026-08-01 甲方裁决）。周末在册是
// 这条断言的重点——它此前被剔除，回归成周一至周五会让未配置的客户端悄悄
// 少给两天可选时间。
func TestDefaultInterviewScheduleShape(t *testing.T) {
	schedule := DefaultInterviewSchedule()
	if len(schedule) != 7 {
		t.Fatalf("默认周表天数漂移: %d", len(schedule))
	}
	for _, day := range InterviewWeekdays {
		windows := schedule[day]
		if len(windows) != 1 || windows[0].Start != "09:00" || windows[0].End != "18:00" {
			t.Fatalf("%s 默认窗口漂移: %+v", day, windows)
		}
	}
	if err := ValidateInterviewSchedule(schedule); err != nil {
		t.Fatalf("默认周表必须自校验通过: %v", err)
	}
}

func TestGenerateSlotsHonoursCustomSchedule(t *testing.T) {
	// 2026-07-10 是周五 14:23。周六排一段、周五排一段，验证周末可选、
	// 当天已过去的整点被裁掉、跨天顺序正确。
	now := frozenShanghai(t, "2026-07-10T14:23:00+08:00")
	slots := GenerateSlots(now, InterviewSchedule{
		"周五": {{Start: "09:00", End: "17:00"}},
		"周六": {{Start: "10:00", End: "12:00"}},
	})
	if len(slots) == 0 {
		t.Fatal("自定义周表不应展开为空")
	}
	// 当天 14:23 之后的周五整点是 15:00、16:00；09:00 已过去必须裁掉。
	if slots[0] != "2026-07-10 15:00:00" || slots[1] != "2026-07-10 16:00:00" {
		t.Fatalf("当天裁剪漂移: %v", slots[:2])
	}
	// 紧接着是周六 10:00、11:00——周末在自定义表里必须能排上。
	if slots[2] != "2026-07-11 10:00:00" || slots[3] != "2026-07-11 11:00:00" {
		t.Fatalf("周末时段缺失: %v", slots[2:4])
	}
	for _, slot := range slots {
		if slot < slots[0] {
			t.Fatalf("时段列表未按时间升序: %v", slots)
		}
	}
}

// 同一天的多段窗口允许重叠，展开后必须按整点去重并升序——重复时段会让
// MatchFrozenRecommendedMeetingTime 的"唯一命中"判定失去意义。
func TestGenerateSlotsDeduplicatesOverlappingWindows(t *testing.T) {
	now := frozenShanghai(t, "2026-07-13T00:00:00+08:00")
	slots := GenerateSlots(now, InterviewSchedule{
		"周一": {{Start: "09:00", End: "12:00"}, {Start: "11:00", End: "13:00"}},
	})
	var monday []string
	for _, slot := range slots {
		if slot >= "2026-07-13" && slot < "2026-07-14" {
			monday = append(monday, slot)
		}
	}
	want := []string{
		"2026-07-13 09:00:00", "2026-07-13 10:00:00",
		"2026-07-13 11:00:00", "2026-07-13 12:00:00",
	}
	if len(monday) != len(want) {
		t.Fatalf("重叠窗口去重失败: %v", monday)
	}
	for i := range want {
		if monday[i] != want[i] {
			t.Fatalf("重叠窗口展开漂移: got=%v want=%v", monday, want)
		}
	}
}

func TestGenerateSlotsEmptyScheduleYieldsNoSlots(t *testing.T) {
	now := frozenShanghai(t, "2026-07-10T14:23:00+08:00")
	if slots := GenerateSlots(now, InterviewSchedule{}); len(slots) != 0 {
		t.Fatalf("空周表必须展开为空: %v", slots)
	}
	if slots := GenerateSlots(now, nil); len(slots) != 0 {
		t.Fatalf("nil 周表必须展开为空: %v", slots)
	}
}

func TestValidateInterviewScheduleRejectsIllegalTables(t *testing.T) {
	cases := []struct {
		name     string
		schedule InterviewSchedule
	}{
		{"空表", InterviewSchedule{}},
		{"nil 表", nil},
		{"全天空窗口", InterviewSchedule{"周一": {}}},
		{"非法星期", InterviewSchedule{"周八": {{Start: "09:00", End: "10:00"}}}},
		{"非整点", InterviewSchedule{"周一": {{Start: "09:30", End: "10:00"}}}},
		{"起止倒置", InterviewSchedule{"周一": {{Start: "10:00", End: "09:00"}}}},
		{"起止相等", InterviewSchedule{"周一": {{Start: "10:00", End: "10:00"}}}},
		{"格式非法", InterviewSchedule{"周一": {{Start: "9:00", End: "10:00"}}}},
		{"小时越界", InterviewSchedule{"周一": {{Start: "25:00", End: "26:00"}}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := ValidateInterviewSchedule(testCase.schedule); err == nil {
				t.Fatalf("非法周表必须被拒: %+v", testCase.schedule)
			}
		})
	}
}

func TestValidateInterviewScheduleAcceptsMinimalTable(t *testing.T) {
	// 只剩一个小时格也是合法的——甲方要求的是"至少保留一个"，不是"每天都要有"。
	minimal := InterviewSchedule{"周三": {{Start: "14:00", End: "15:00"}}}
	if err := ValidateInterviewSchedule(minimal); err != nil {
		t.Fatalf("单格周表应当合法: %v", err)
	}
}
