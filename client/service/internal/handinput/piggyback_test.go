package handinput

import (
	"math"
	"testing"
)

// truth 模拟一台真实机器:我方按当前(可能是错的)标定把一个 CSS 目标映射成系统坐标
// 发出去,浏览器按**真实**几何算出 clientX/Y 并取整上报。这就是一次落点样本。
func landing(cur Calib, truth Calib, targetCSSX, targetCSSY int) Sample {
	sx, sy, _ := cur.Snap(targetCSSX, targetCSSY)
	cx, cy := truth.ToClient(float64(sx), float64(sy))
	return Sample{
		ScreenX: float64(sx), ScreenY: float64(sy),
		ClientX: math.Floor(cx + 0.5), ClientY: math.Floor(cy + 0.5),
	}
}

// 冷启动:粗估天然是错的(页面报的是窗口位置,而视口原点在标题栏+标签栏+地址栏
// +书签栏下面一百多像素),两次正常移动的落点就该把它追回来。
//
// 追上之前状态必须是**冷启动或存疑,不是就绪**——那两档都不许点击,方向是宁可不点。
func TestPiggybackConvergesFromRoughSeedInTwoLandings(t *testing.T) {
	truth := Calib{ScaleX: 1.5, ScaleY: 1.5, OffsetX: 656.92, OffsetY: 121.5}
	var pb Piggyback
	// 页面自报视口原点(CSS),y 轴天然偏一百多像素——这是真机上必然发生的那个偏差
	pb.Seed(WindowHint{ScreenX: 437.9, ScreenY: 0, DPR: 1.5}, 0, 0)

	if _, ready := pb.Calib(); ready {
		t.Fatal("只播了粗估就报就绪——那会让第一次移动直接去点击")
	}

	// 两次业务移动,落点张得够开(两轴都要 >= MinSpanPx)
	for _, p := range [][2]int{{80, 60}, {700, 520}} {
		cur, _ := pb.Calib()
		st, err := pb.Observe(landing(cur, truth, p[0], p[1]))
		if err != nil && st != PBCold {
			t.Fatalf("正常落点不该报错:%v (%v)", err, st)
		}
	}

	got, ready := pb.Calib()
	if !ready {
		t.Fatalf("两次落点后仍未就绪,样本数 %d,残差 %.3f", pb.N(), pb.Residual())
	}
	if got.ScaleY != truth.ScaleY || math.Abs(got.OffsetY-truth.OffsetY) > 1 {
		t.Fatalf("没追上真值:得到 %+v,真值 %+v", got, truth)
	}
}

// 「一个样本对不上」和「几何真的变了」不是一回事(浏览器可能丢了最后一个事件)。
// 所以分两级,而且**方向永远是宁可不点**:
//
//	一次对不上   存疑,这一次不许点击,历史留着
//	连续两次     几何真变了,历史全丢,回冷启动
//
// 历史必须全丢——旧样本描述的是旧几何,混进新拟合只会得到一个哪边都不对的中间值。
func TestPiggybackTwoTierDriftDisposition(t *testing.T) {
	truth := Calib{ScaleX: 1, ScaleY: 1, OffsetX: 0, OffsetY: 151}
	var pb Piggyback
	pb.Seed(WindowHint{ScreenX: 0, ScreenY: 151, DPR: 1}, 0, 0)
	for _, p := range [][2]int{{40, 30}, {900, 700}} {
		cur, _ := pb.Calib()
		if _, err := pb.Observe(landing(cur, truth, p[0], p[1])); err != nil {
			t.Fatal(err)
		}
	}
	if _, ready := pb.Calib(); !ready {
		t.Fatal("前置不成立:两次落点后应已就绪")
	}
	before := pb.N()

	// 窗口被拖走了:同样的系统坐标,浏览器按新几何算出完全不同的 clientY
	moved := Calib{ScaleX: 1, ScaleY: 1, OffsetX: 0, OffsetY: 451}
	cur, _ := pb.Calib()
	st, err := pb.Observe(landing(cur, moved, 500, 400))
	if st != PBSuspect || err == nil {
		t.Fatalf("第一次对不上应报存疑,得到 %v / %v", st, err)
	}
	if pb.N() != before {
		t.Fatalf("存疑档不许丢历史:样本从 %d 变成 %d", before, pb.N())
	}

	cur, _ = pb.Calib()
	st, err = pb.Observe(landing(cur, moved, 520, 420))
	if st != PBReset || err == nil {
		t.Fatalf("连续两次对不上应重置,得到 %v / %v", st, err)
	}
	if pb.N() != 0 {
		t.Fatalf("几何变了必须把历史全丢,还剩 %d 条", pb.N())
	}
	if _, ready := pb.Calib(); ready {
		t.Fatal("重置后必须回冷启动,不许仍报就绪")
	}
}

// MinSpanPx 必须量 **client** 侧的跨度,不能量 screen 侧。
//
// 噪声在 client 那一侧(clientX 是整数),推导也在那一侧。量 screen 的话这道门会
// 自动放松 scale 倍:scale=2 时 screen 跨 200 只对应 client 跨 100,scale 的误差
// 上界翻到 1%,越过 snapScale 的 0.5% 容差——留下一个带噪声的分数 scale,
// 也就是上游 §30 那种「残差很小、自检全过、映射整体偏一格」。
//
// **scale 恰好为 1 时两侧数值相同,所以这个错在开发机(macOS)上永远看不出来。**
func TestPiggybackSpanIsMeasuredOnClientSideNotScreenSide(t *testing.T) {
	var pb Piggyback
	// scale=2:client 跨度 120(不足 200),而 screen 跨度 240(若量错侧就会放行)
	pb.samples = []Sample{
		{ClientX: 10, ClientY: 10, ScreenX: 20, ScreenY: 20},
		{ClientX: 130, ClientY: 130, ScreenX: 260, ScreenY: 260},
	}
	if pb.hasSpan() {
		t.Fatalf("client 跨度只有 120 < %v 却放行了——这道门量错了侧,而它在 scale=1 的开发机上不可见", MinSpanPx)
	}
}
