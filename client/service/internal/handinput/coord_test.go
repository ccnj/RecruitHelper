package handinput

import (
	"math"
	"testing"
)

// 「往表里补一档」是遇到吸不住的缩放时最顺手的修法,而它是最坏的一个:
// 补到间距小于吸附容差两倍之后,带噪声的拟合会被吸到**相邻的错档**,
// 而错档是个精确数字,后面所有自检都会以为它对。本用例就是那道闸。
func TestScaleGapOKBlocksDenserTable(t *testing.T) {
	ok, worst, pair := ScaleGapOK(0.005)
	if !ok {
		t.Fatalf("PlausibleScales 最近的一对 %v 间距只有 %.4f%%,不到吸附容差 0.5%% 的两倍;"+
			"补密会让带噪声的拟合被吸到相邻的错档,而错档是个精确数字、事后查不出来", pair, worst*100)
	}
}

// jsRound 复刻的是浏览器会算出什么,不是 Go 想算什么。两者只在负的半整数处不同,
// 而那一格恰好是视口左上角:上游实测 offset=151.5、scale=1 时,css=0 会被 Go 的
// math.Round 误判成「表达不出来」。
func TestSnapUsesJavaScriptRoundingAtViewportOrigin(t *testing.T) {
	c := Calib{ScaleX: 1, ScaleY: 1, OffsetX: 151.5, OffsetY: 151.5}
	if _, _, ok := c.Snap(0, 0); !ok {
		t.Fatal("视口左上角被误判为不可达:snap1 用了 Go 的半数远离零,而要预测的是 JS 的半数朝 +∞")
	}
}

// 缩放 >= 100% 时物理格比 CSS 格细,任何整数 CSS 位置都必须能往返。
func TestSnapRoundTripsAtEveryPlausibleScale(t *testing.T) {
	for _, s := range PlausibleScales {
		if s < 1 {
			continue // <100% 另有用例,那时本来就会撞格
		}
		c := Calib{ScaleX: s, ScaleY: s, OffsetX: 151.5, OffsetY: 88.25}
		for css := 0; css < 1400; css++ {
			sx, _, ok := c.Snap(css, 0)
			if !ok {
				t.Fatalf("scale=%v css=%d 报不可达", s, css)
			}
			if back := math.Floor((float64(sx)-c.OffsetX)/c.ScaleX + 0.5); int(back) != css {
				t.Fatalf("scale=%v css=%d 往返回到 %v", s, css, back)
			}
		}
	}
}

// 缩放 <100% 时物理格更粗,两个不同 CSS 位置会撞进同一系统像素。
// 那一帧鼠标根本不动、浏览器不派发事件、样本凭空消失——**必须如实上报,不许悄悄凑一个**。
func TestSnapReportsUnreachableBelowOneHundredPercent(t *testing.T) {
	c := Calib{ScaleX: 0.5, ScaleY: 0.5, OffsetX: 0, OffsetY: 0}
	unreachable := 0
	for css := 0; css < 400; css++ {
		if _, _, ok := c.Snap(css, 0); !ok {
			unreachable++
		}
	}
	if unreachable == 0 {
		t.Fatal("scale=0.5 下必然有 CSS 位置撞格,一个都没报不可达说明失效方向反了")
	}
}

// 上游 §30 事故的回归:真实映射是 scale 恰好 1、offset 恰好 151,而最小二乘在带
// ±0.5px 取整噪声的观测上拟合出 1.000038 / 150.48——残差只有 0.433px、两条自检
// 全过,但那 0.000038 让映射在 cy≈520 处跨过取整边界。噪声不该有资格发明一个分数。
func TestCalibrateSnapsNoiseInducedFractionBackToExactScale(t *testing.T) {
	var ss []Sample
	for cy := 0.0; cy < 1200; cy += 37 {
		// 真值 scale=1 offset=151,观测带 ±0.5 的取整噪声
		noise := 0.5
		if int(cy)%2 == 0 {
			noise = -0.5
		}
		ss = append(ss, Sample{ClientX: cy, ScreenX: cy + 151 + noise, ClientY: cy, ScreenY: cy + 151 + noise})
	}
	c, err := Calibrate(ss)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Snapped {
		t.Fatalf("没能吸到合法档:ScaleY=%v", c.ScaleY)
	}
	if c.ScaleY != 1 {
		t.Fatalf("ScaleY 应被吸到精确的 1,实际 %v", c.ScaleY)
	}
	if math.Abs(c.OffsetY-151) > 0.5 {
		t.Fatalf("吸附后 OffsetY 应回到 151 附近,实际 %v", c.OffsetY)
	}
}

// **这条性质是「起点不会瞬移」的全部依据。**
//
// Snap 与 ToClient 用同一份标定、互为逆运算,所以先把光标的真实系统坐标反算成
// CSS、再 Snap 回去,必然还落在原地——**不管标定有多离谱**。
//
// 反过来说:起点若取自记忆里的 CSS 值,就没有这个抵消。标定一被修正,那个记忆值
// 立刻错位,错位量正好等于修正量;冷启动时那是一两百像素的一次干净瞬移,
// 也就是评分台里最差的那个形状。编排层因此必须每次现读光标位置。
func TestSnapOfToClientReturnsToOriginRegardlessOfCalibrationError(t *testing.T) {
	for _, c := range []Calib{
		{ScaleX: 1, ScaleY: 1, OffsetX: 0, OffsetY: 0},
		{ScaleX: 1.5, ScaleY: 1.5, OffsetX: 656.92, OffsetY: 121.5},
		{ScaleX: 2, ScaleY: 2, OffsetX: -300, OffsetY: 999.25},    // 离谱的 offset
		{ScaleX: 1.13, ScaleY: 0.91, OffsetX: 7.77, OffsetY: 3.3}, // 吸不住的分数 scale
	} {
		for _, sys := range [][2]int{{0, 0}, {137, 42}, {1919, 1079}, {640, 480}} {
			cx, cy := c.ToClient(float64(sys[0]), float64(sys[1]))
			bx, by, ok := c.Snap(int(math.Floor(cx+0.5)), int(math.Floor(cy+0.5)))
			if !ok {
				continue // 撞格是另一回事,由 unreachable 如实上报
			}
			if d := math.Hypot(float64(bx-sys[0]), float64(by-sys[1])); d > 1.5 {
				t.Fatalf("calib=%+v 系统坐标 %v 反算再映射回来偏了 %.2f 像素", c, sys, d)
			}
		}
	}
}
