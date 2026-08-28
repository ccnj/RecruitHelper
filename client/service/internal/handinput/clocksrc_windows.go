//go:build windows

package handinput

// Windows 的时间源:QueryPerformanceCounter。改写自上游
// hiBoss `lab/engine/inject/clocksrc_windows.go`,推导原样保留。
//
// **不能用 time.Now()**。Go 在 Windows 上的 runtime·nanotime1 读的是
// KUSER_SHARED_DATA 共享页里的 InterruptTime,而它只在每次时钟中断时递增。
// Go 1.23 起运行时不再调 timeBeginPeriod,默认时钟中断周期就是 **15.625ms**
// ——也就是说 time.Now() / time.Since() 在 Windows 上的分辨率是 15.625ms。
//
// 这会让整个测量体系静默失效,而且失效方式极具误导性:忙等收尾(SpinTail=1.5ms)
// 根本看不见,spin/hybrid 退化成「等到 deadline 之后的下一个 tick」,比 sleep 还差,
// 于是结论会写成「Windows 时钟很烂」。而真相相反:Go 1.23 起 time.Sleep 走
// CreateWaitableTimerExW + CREATE_WAITABLE_TIMER_HIGH_RESOLUTION(挂 IOCP、
// 不经过 tick),精度约 0.5ms。**拿一把 15.6ms 的尺子去量 0.5ms 的东西,
// 量出来的永远是尺子本身。**
//
// QPC 不受这条限制:x64 上频率通常 10MHz(100ns),ARM64 上 24MHz(约 41.7ns)。
// golang.org/x/sys/windows 没有导出它,只能自己绑。

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	procQPC  = kernel32.NewProc("QueryPerformanceCounter")
	procQPF  = kernel32.NewProc("QueryPerformanceFrequency")
	qpcFreq  int64
)

func init() {
	procQPF.Call(uintptr(unsafe.Pointer(&qpcFreq)))
}

func nowNanos() int64 {
	var c int64
	procQPC.Call(uintptr(unsafe.Pointer(&c)))
	// 先乘后除会溢出:10MHz 下 c*1e9 只撑 293 秒。拆成整秒 + 余数。
	return (c/qpcFreq)*1e9 + (c%qpcFreq)*1e9/qpcFreq
}

func clockSourceName() string {
	if qpcFreq == 0 {
		return "QueryPerformanceCounter(频率读取失败!)"
	}
	return fmt.Sprintf("QueryPerformanceCounter  freq=%.3fMHz  标称分辨率 %.1fns",
		float64(qpcFreq)/1e6, 1e9/float64(qpcFreq))
}
