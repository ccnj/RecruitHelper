package store

import (
	"strings"
	"testing"

	"recruithelper/client/service/internal/m5ai"
)

// 新库没有配置行时读到内置默认周表。这是升级零漂移的根据：老客户端升上来
// 不会凭空多出一行，读到的就是可配置化之前那张硬编码窗口。
func TestInterviewScheduleFallsBackToBuiltinDefaultWhenUnset(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	schedule, err := s.InterviewSchedule()
	if err != nil {
		t.Fatalf("InterviewSchedule: %v", err)
	}
	want := m5ai.DefaultInterviewSchedule()
	if len(schedule) != len(want) {
		t.Fatalf("未配置时应回落内置默认: got=%+v", schedule)
	}
	for day, windows := range want {
		got := schedule[day]
		if len(got) != len(windows) || got[0] != windows[0] {
			t.Fatalf("%s 回落漂移: got=%+v want=%+v", day, got, windows)
		}
	}
	// 回落不得隐式建行——否则"从未配置"与"配置成了默认值"就分不开了。
	var count int64
	if err := s.db.Model(&InterviewScheduleSetting{}).Count(&count).Error; err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 0 {
		t.Fatalf("读取不得隐式建行: count=%d", count)
	}
}

func TestSetInterviewScheduleRoundTrips(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := m5ai.InterviewSchedule{
		"周二": {{Start: "10:00", End: "12:00"}},
		"周六": {{Start: "14:00", End: "16:00"}},
	}
	if err := s.SetInterviewSchedule(want); err != nil {
		t.Fatalf("SetInterviewSchedule: %v", err)
	}
	got, err := s.InterviewSchedule()
	if err != nil {
		t.Fatalf("InterviewSchedule: %v", err)
	}
	if len(got) != 2 || got["周二"][0].Start != "10:00" || got["周六"][0].End != "16:00" {
		t.Fatalf("周表往返漂移: %+v", got)
	}
	// 再存一次必须是整表替换，不是叠加——单行 Save 天然满足，这里钉住它。
	if err := s.SetInterviewSchedule(m5ai.InterviewSchedule{
		"周三": {{Start: "09:00", End: "10:00"}},
	}); err != nil {
		t.Fatalf("二次保存: %v", err)
	}
	got, err = s.InterviewSchedule()
	if err != nil {
		t.Fatalf("InterviewSchedule: %v", err)
	}
	if len(got) != 1 || len(got["周三"]) != 1 || len(got["周二"]) != 0 {
		t.Fatalf("整表替换失败，出现叠加: %+v", got)
	}
	var count int64
	if err := s.db.Model(&InterviewScheduleSetting{}).Count(&count).Error; err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 1 {
		t.Fatalf("配置表必须恒为单行: count=%d", count)
	}
}

// 空表由脑侧挡掉，不依赖 UI 先行把关（甲方裁决：至少保留一个时段）。
func TestSetInterviewScheduleRejectsEmptyTable(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for name, schedule := range map[string]m5ai.InterviewSchedule{
		"空表":    {},
		"nil 表": nil,
		"全空窗口":  {"周一": {}},
	} {
		if err := s.SetInterviewSchedule(schedule); err == nil {
			t.Fatalf("%s 必须被拒", name)
		}
	}
	// 被拒之后不得留下任何行。
	var count int64
	if err := s.db.Model(&InterviewScheduleSetting{}).Count(&count).Error; err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 0 {
		t.Fatalf("拒绝写入后不得留行: count=%d", count)
	}
}

// 这是本刀最重要的阻断项：配置行损坏时必须报错，绝不静默回落到内置默认。
// 静默回落会让脑按一张用户已经改掉的表去承诺面试时间，属于错约。
func TestInterviewScheduleNeverSilentlyFallsBackOnCorruptRow(t *testing.T) {
	for _, corrupt := range []string{
		`not json at all`,
		`{"周一":[{"start":"09:30","end":"10:00"}]}`, // 非整点
		`{"周八":[{"start":"09:00","end":"10:00"}]}`, // 非法星期
		`{}`, // 空表：写入侧挡得住，读到了说明库被直接改过
	} {
		s, err := Open(t.TempDir())
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := s.db.Save(&InterviewScheduleSetting{
			ID: interviewScheduleSettingID, ScheduleJSON: corrupt,
		}).Error; err != nil {
			t.Fatalf("注入损坏行: %v", err)
		}
		schedule, err := s.InterviewSchedule()
		if err == nil {
			t.Fatalf("损坏配置必须报错而非回落: json=%q got=%+v", corrupt, schedule)
		}
		if schedule != nil {
			t.Fatalf("报错时不得同时返回周表: %+v", schedule)
		}
		if !strings.Contains(err.Error(), "可面试时段配置") {
			t.Fatalf("错误信息应指明是时段配置: %v", err)
		}
	}
}
