package handinput

import "fmt"

// 滚轮计划。与打字计划同一条分界:**插件排,本包播**(doc.go「数据 / 过程」)。
//
// 一格是一次事件。真人的滚轮一格一个 HID 报告,"甩一下"是好几格挤在几十毫秒里、
// 停一停再甩——那个形状(几格一簇、簇内间隔的分布、簇间停顿)全由插件的排版器算,
// 这里只按 At 把每一格发出去。本包里出现"并几格成一个事件"或"算一个间隔",分界就破了。
type ScrollPlan struct {
	Ticks []ScrollTick `json:"ticks"`
}

// ScrollTick 是一格滚轮:什么时候(相对计划起点的毫秒)、几格、哪个方向。
type ScrollTick struct {
	At float64 `json:"at"`
	// Dy 的符号按 W3C `WheelEvent.deltaY` 口径:**正=内容向下(朝文档末尾),负=向上**。
	// 插件读 DOM 就是这个口径;翻成各平台注入 API 的符号是注入器的事(见 Injector.Wheel)。
	Dy int `json:"dy"`
}

// 计划的封顶。不是"像不像人"的判据(那在插件),只是把明显排错的计划挡在第一格之前:
//
//   - 一次最多 400 格:插件闭环是"一簇几格→读一次页面",一簇不会超过十几格;
//     几百格只可能是排版器失控。
//   - 一格最多 3 个刻度:真人一格一报告,快甩时系统偶有并成两三格的报告;再大就不是滚轮了。
//   - 跨度最长 20 秒:与条件等待的封顶同一个数。
const (
	maxScrollTicks         = 400
	maxScrollNotchesPerTck = 3
	maxScrollSpanMs        = 20_000
)

// Validate 在发出任何一格**之前**把计划否掉的那些理由。失效方向是"一格都没发"。
//
// 方向必须单一:一次计划只滚一个方向,回弹(滚过头再往回)由排版器另起一次计划——
// 那时它先读页面再决定,而不是在一份计划里预排一个"猜的"回弹。
func (p *ScrollPlan) Validate() error {
	if len(p.Ticks) == 0 {
		return fmt.Errorf("空计划")
	}
	if len(p.Ticks) > maxScrollTicks {
		return fmt.Errorf("一次计划 %d 格,超过封顶 %d", len(p.Ticks), maxScrollTicks)
	}
	prev, dir := 0.0, 0
	for i, t := range p.Ticks {
		if t.At < 0 || t.At < prev {
			return fmt.Errorf("第 %d 格时刻倒退:%.0fms -> %.0fms", i+1, prev, t.At)
		}
		if t.At > maxScrollSpanMs {
			return fmt.Errorf("第 %d 格在 %.0fms,超过单次计划跨度封顶 %dms", i+1, t.At, maxScrollSpanMs)
		}
		if t.Dy == 0 {
			return fmt.Errorf("第 %d 格没有方向(dy=0)", i+1)
		}
		if absInt(t.Dy) > maxScrollNotchesPerTck {
			return fmt.Errorf("第 %d 格 %d 个刻度,超过一格封顶 %d", i+1, absInt(t.Dy), maxScrollNotchesPerTck)
		}
		d := signInt(t.Dy)
		if dir != 0 && d != dir {
			return fmt.Errorf("第 %d 格方向反转:一次计划只滚一个方向,回弹由排版器另起一次", i+1)
		}
		dir, prev = d, t.At
	}
	return nil
}

// ScrollResult 是一次滚轮播放的回执。**不含"页面滚到了哪"**——那要回读 DOM,
// 是插件的事;本包只知道自己发了些什么。
type ScrollResult struct {
	Ticks     int     `json:"ticks"`   // 实际发出的事件数
	Notches   int     `json:"notches"` // 实际发出的刻度数(各格 |dy| 之和)
	LagMeanUs float64 `json:"lagMeanUs"`
	LagMaxUs  float64 `json:"lagMaxUs"`
	Status    string  `json:"status"`
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func signInt(v int) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}
