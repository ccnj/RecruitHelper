package report

import (
	"context"
	"errors"
	"testing"
	"time"
)

func at(hour, minute int) time.Time {
	return time.Date(2026, 8, 1, hour, minute, 0, 0, time.Local)
}

// 触发时刻算错会导致同一秒里反复触发，或整天不触发。
func TestNextDailyRun(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"当天尚未到点", at(0, 5), at(0, 10)},
		{"当天已过点", at(9, 0), at(0, 10).AddDate(0, 0, 1)},
		{"正好等于触发时刻", at(0, 10), at(0, 10).AddDate(0, 0, 1)},
		{"深夜", at(23, 59), at(0, 10).AddDate(0, 0, 1)},
	}
	for _, item := range cases {
		got := nextDailyRun(item.now, dailyHour, dailyMinute)
		if !got.Equal(item.want) {
			t.Errorf("%s: 期望 %v，得到 %v", item.name, item.want, got)
		}
	}
}

// 开关默认关闭是硬约束：关着的时候绝不能因为到点了就把候选人明文发出去。
func TestSchedulerSkipsWhenDisabled(t *testing.T) {
	ran := false
	runDailyOnce(context.Background(), SchedulerDeps{
		Enabled: func() (bool, error) { return false, nil },
		Quiet:   func() (bool, string) { return true, "" },
		RunOnce: func(context.Context) error { ran = true; return nil },
		Now:     func() time.Time { return at(0, 10) },
	}, at(0, 10))
	if ran {
		t.Fatal("开关关闭时不得执行上传")
	}
}

// 读不出开关时按关处理：宁可不传，也不能因为读库出错就把明文发出去。
func TestSchedulerTreatsReadErrorAsDisabled(t *testing.T) {
	ran := false
	runDailyOnce(context.Background(), SchedulerDeps{
		Enabled: func() (bool, error) { return true, errors.New("库读失败") },
		Quiet:   func() (bool, string) { return true, "" },
		RunOnce: func(context.Context) error { ran = true; return nil },
		Now:     func() time.Time { return at(0, 10) },
	}, at(0, 10))
	if ran {
		t.Fatal("开关读取失败时必须按关闭处理")
	}
}

// 静默就跑，并记成功。
func TestSchedulerRunsWhenQuiet(t *testing.T) {
	ran := false
	var recordedOK bool
	runDailyOnce(context.Background(), SchedulerDeps{
		Enabled: func() (bool, error) { return true, nil },
		Quiet:   func() (bool, string) { return true, "" },
		RunOnce: func(context.Context) error { ran = true; return nil },
		Record:  func(_ time.Time, ok bool, _ string) { recordedOK = ok },
		Now:     func() time.Time { return at(0, 10) },
	}, at(0, 10))
	if !ran {
		t.Fatal("静默时应当执行上传")
	}
	if !recordedOK {
		t.Error("成功结果应被记录")
	}
}

// 不静默就顺延；期间变静默了就执行——这条保证"业务收尾拖了一会儿"不至于白等一天。
func TestSchedulerDefersUntilQuiet(t *testing.T) {
	now := at(0, 10)
	checks := 0
	ran := false
	runDailyOnce(context.Background(), SchedulerDeps{
		Enabled: func() (bool, error) { return true, nil },
		Quiet: func() (bool, string) {
			checks++
			if checks < 3 {
				return false, "有活跃工作流"
			}
			return true, ""
		},
		RunOnce:    func(context.Context) error { ran = true; return nil },
		Now:        func() time.Time { return now },
		RetryEvery: time.Millisecond,
	}, at(0, 10))
	if !ran {
		t.Fatalf("变静默后应当执行，实际检查了 %d 次", checks)
	}
}

// 一直不静默，过了 02:00 就放弃，且必须留下可见的原因——当日不补传。
func TestSchedulerGivesUpAfterDeadline(t *testing.T) {
	now := at(1, 59)
	ran := false
	var recordedOK bool
	var recordedReason string
	runDailyOnce(context.Background(), SchedulerDeps{
		Enabled: func() (bool, error) { return true, nil },
		Quiet:   func() (bool, string) { return false, "仍有 3 条未收束命令" },
		RunOnce: func(context.Context) error { ran = true; return nil },
		Record: func(_ time.Time, ok bool, reason string) {
			recordedOK, recordedReason = ok, reason
		},
		Now:        func() time.Time { return now },
		RetryEvery: time.Hour,
	}, at(0, 10))
	if ran {
		t.Error("始终不静默时不得执行上传")
	}
	if recordedOK {
		t.Error("顺延超时应记为未成功")
	}
	if recordedReason == "" {
		t.Error("必须留下原因，否则无人值守时没人知道为什么没传")
	}
}

// 上传失败不重试（裁决），但要记下来。
func TestSchedulerRecordsFailureWithoutRetry(t *testing.T) {
	calls := 0
	var recordedOK bool
	runDailyOnce(context.Background(), SchedulerDeps{
		Enabled: func() (bool, error) { return true, nil },
		Quiet:   func() (bool, string) { return true, "" },
		RunOnce: func(context.Context) error { calls++; return errors.New("上传被拒") },
		Record:  func(_ time.Time, ok bool, _ string) { recordedOK = ok },
		Now:     func() time.Time { return at(0, 10) },
	}, at(0, 10))
	if calls != 1 {
		t.Errorf("失败不得重试，期望调用 1 次，实际 %d 次", calls)
	}
	if recordedOK {
		t.Error("失败结果应被记录为未成功")
	}
}

// ctx 取消时循环必须退出，不能把关不掉的 goroutine 留在退出流程里。
func TestSchedulerStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		RunScheduler(ctx, SchedulerDeps{
			Enabled: func() (bool, error) { return true, nil },
			Quiet:   func() (bool, string) { return true, "" },
			RunOnce: func(context.Context) error { return nil },
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后调度循环没有退出")
	}
}
