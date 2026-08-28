package handinput

// 高精度等待。改写自上游 hiBoss `lab/engine/inject/clock.go`,推导原样保留。
//
// 轨迹引擎的全部价值建立在能精确控制间隔上:排出 [137, 203, 89] ms 却发成
// 15.6ms 的整数倍,分布会被量化成梳状,CV 与 IQR 都不是标定时算的那个值。
//
// 三种策略并存是为了**实测**而不是猜:
//
//	sleep   裸 time.Sleep —— Go runtime 在各平台的默认表现
//	spin    纯忙等 —— 精度上限,代价是烧满一个核
//	hybrid  粗睡到目标前 SpinTail,再忙等收尾 —— 生产用这个
//
// **时刻一律走 nowNanos(),不要用 time.Now()** —— Windows 上后者的分辨率是
// 15.625ms,会让上面三种策略的差异完全测不出来。见 clocksrc_windows.go。

import "time"

type WaitMode int

const (
	WaitSleep WaitMode = iota
	WaitSpin
	WaitHybrid
)

// SpinTail 是 hybrid 模式下改用忙等的剩余时长。
var SpinTail = 1500 * time.Microsecond

// WaitUntil 等到 deadline(nowNanos() 口径的纳秒时刻)。
func WaitUntil(deadline int64, mode WaitMode) {
	switch mode {
	case WaitSpin:
		for nowNanos() < deadline {
		}
	case WaitSleep:
		if d := deadline - nowNanos(); d > 0 {
			time.Sleep(time.Duration(d))
		}
	default: // hybrid
		if d := deadline - nowNanos() - int64(SpinTail); d > 0 {
			time.Sleep(time.Duration(d))
		}
		for nowNanos() < deadline {
		}
	}
}

// Now 是本包对外的单调时刻(纳秒),与 WaitUntil 同一口径。
func Now() int64 { return nowNanos() }

// ClockSource 报告本机用的是哪个时间源,给诊断读。
func ClockSource() string { return clockSourceName() }

// ClockResolution 实测本机时间源能分辨的最小非零间隔。
//
// 这是整套测量的前置自检:尺子比被测对象还粗的时候,所有结论都是尺子的形状。
// 按**墙钟时间**封顶,不能按轮数——每观察到一次跳变要等一整个 tick,时钟越粗
// 每轮越久;按轮数写的话,正好在「时钟粗到 15.6ms」这个最该被检测出来的情形下
// 要跑几十分钟。
func ClockResolution() time.Duration {
	const budget = 20 * time.Millisecond
	deadline := time.Now().Add(budget) // 只用来卡预算,不参与测量
	min := int64(1) << 62
	prev := nowNanos()
	for time.Now().Before(deadline) {
		b := nowNanos()
		if b != prev {
			if d := b - prev; d > 0 && d < min {
				min = d
			}
			prev = b
		}
	}
	if min == int64(1)<<62 {
		return -1 // 预算内一次都没跳,时钟比 budget 还粗
	}
	return time.Duration(min)
}
