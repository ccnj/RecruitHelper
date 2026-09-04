package report

import (
	"fmt"
	"testing"
	"time"
)

func day(year int, month time.Month, dayOfMonth int) time.Time {
	return time.Date(year, month, dayOfMonth, 13, 47, 21, 0, time.Local)
}

// 稳定性是这套东西的立身之本:脑半夜重启后必须重算出同一个档期,否则一天会跑两次。
func TestSlotIsStableAcrossRecomputation(t *testing.T) {
	pick := NewSlotPicker(func() string { return "a1b2c3" })
	first := pick(day(2026, 9, 4))
	for i := 0; i < 100; i++ {
		if got := pick(day(2026, 9, 4)); !got.Equal(first) {
			t.Fatalf("同一天重算得到不同档期:%v vs %v", first, got)
		}
	}
	// 同一天的任意时刻问,答案都该一样——调度器会在一天里多次问它。
	atNoon := pick(time.Date(2026, 9, 4, 12, 0, 0, 0, time.Local))
	atNight := pick(time.Date(2026, 9, 4, 23, 59, 59, 0, time.Local))
	if !atNoon.Equal(first) || !atNight.Equal(first) {
		t.Errorf("同一日期不同时刻问出了不同档期:%v / %v / %v", first, atNoon, atNight)
	}
}

// 档期必须落在 [00:05, 01:35) 内,且是当天的。越界会跑到业务窗口里去。
func TestSlotStaysInWindow(t *testing.T) {
	for i := 0; i < 500; i++ {
		pick := NewSlotPicker(func() string { return fmt.Sprintf("machine-%d", i) })
		target := day(2026, 9, 4)
		got := pick(target)
		midnight := time.Date(target.Year(), target.Month(), target.Day(), 0, 0, 0, 0, target.Location())
		earliest := midnight.Add(slotWindowStart)
		latest := midnight.Add(slotWindowStart + slotWindowSpread)
		if got.Before(earliest) || !got.Before(latest) {
			t.Fatalf("机器 %d 的档期 %v 越出窗口 [%v, %v)", i, got, earliest, latest)
		}
	}
}

// 加上最大的任务偏移后仍不得跨进业务窗口,也不得跨天——跨天会让 nextDailyRun 的
// "今天/明天"判断失去意义。
func TestSlotPlusMaxOffsetStaysSameDay(t *testing.T) {
	target := day(2026, 9, 4)
	midnight := time.Date(target.Year(), target.Month(), target.Day(), 0, 0, 0, 0, target.Location())
	for i := 0; i < 500; i++ {
		pick := NewSlotPicker(func() string { return fmt.Sprintf("machine-%d", i) })
		latest := pick(target).Add(OffsetChatReport)
		if latest.Day() != target.Day() {
			t.Fatalf("机器 %d 的最后一个任务 %v 跨天了", i, latest)
		}
		// 顺延窗口用尽也不该摸到 07:00 的业务窗口。
		if deadline := deferDeadline(latest); !deadline.Before(midnight.Add(7 * time.Hour)) {
			t.Fatalf("机器 %d 的顺延截止 %v 摸进了业务窗口", i, deadline)
		}
	}
}

// 不同机器要真的分开。改造前所有机器钉在同一分钟,这条用例就是那个 bug 的反面。
func TestSlotSpreadsAcrossMachines(t *testing.T) {
	const machines = 30
	minuteBuckets := make(map[int]int)
	for i := 0; i < machines; i++ {
		pick := NewSlotPicker(func() string {
			// 真实机器标识是 64 位十六进制,这里模拟其长度与字符集。
			return fmt.Sprintf("%064x", i*2654435761)
		})
		got := pick(day(2026, 9, 4))
		minuteBuckets[got.Hour()*60+got.Minute()]++
	}
	// 30 台机器落进 90 个分钟坑,允许少量撞车,但不该出现"一半挤在同一分钟"。
	worst := 0
	for _, count := range minuteBuckets {
		if count > worst {
			worst = count
		}
	}
	if worst > 3 {
		t.Errorf("单分钟内挤了 %d 台机器,打散没起作用(桶分布 %v)", worst, minuteBuckets)
	}
	if len(minuteBuckets) < machines/2 {
		t.Errorf("30 台机器只落进 %d 个分钟坑,分布过于集中", len(minuteBuckets))
	}
}

// 同一台机器每天换坑,不长期占住同一个时刻。
func TestSlotVariesByDate(t *testing.T) {
	pick := NewSlotPicker(func() string { return "same-machine" })
	seen := make(map[time.Duration]bool)
	for d := 1; d <= 28; d++ {
		got := pick(day(2026, 9, d))
		midnight := time.Date(2026, 9, d, 0, 0, 0, 0, time.Local)
		seen[got.Sub(midnight)] = true
	}
	if len(seen) < 20 {
		t.Errorf("同一机器 28 天只用了 %d 个不同档期,日期没起作用", len(seen))
	}
}

// 未激活(机器标识为空)不得 panic;此时所有机器同档期是已知且可接受的,
// 因为出站任务在授权未就绪时本来就不传。
func TestSlotHandlesMissingMachineID(t *testing.T) {
	for _, pick := range []SlotPicker{
		NewSlotPicker(nil),
		NewSlotPicker(func() string { return "" }),
		NewSlotPicker(func() string { return "   " }),
	} {
		got := pick(day(2026, 9, 4))
		if got.IsZero() {
			t.Fatal("机器标识缺席时档期不得为零值")
		}
	}
}

// 三条任务的相对顺序不能乱:清理 → 诊断包 → 聊天记录。顺序错了,当天上传的包
// 就不是瘦身后的,聊天记录还会跟大包抢上行。
func TestOffsetsKeepTaskOrder(t *testing.T) {
	if !(OffsetCmdRetention < OffsetFieldReport && OffsetFieldReport < OffsetChatReport) {
		t.Fatalf("任务偏移顺序错乱:清理 %v / 诊断包 %v / 聊天记录 %v",
			OffsetCmdRetention, OffsetFieldReport, OffsetChatReport)
	}
}
