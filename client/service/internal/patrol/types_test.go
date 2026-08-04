package patrol

import "testing"

// 正常轮间隔只决定"没有事件催的时候多久回头看一眼"，它必须始终宽于
// MinimumRoundGap 那道硬下限。两者都是可调常量，一旦有人把间隔调到比
// 下限还密，空闲账号每轮都立刻到期，巡检退化成紧循环——而 due() 里两道
// 闸是独立判断，编译期不会报错，真机上表现为无谓的页面读取压力。
func TestDefaultPatrolIntervalStaysAboveMinimumRoundGap(t *testing.T) {
	config := Config{}.withDefaults()
	if config.PatrolInterval <= config.MinimumRoundGap {
		t.Fatalf(
			"正常轮间隔 %v 未宽于最小轮间隔 %v，空闲账号会退化成紧循环",
			config.PatrolInterval, config.MinimumRoundGap,
		)
	}
}
