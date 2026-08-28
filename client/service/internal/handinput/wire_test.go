package handinput

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

// wirePlan 是插件真会发过来的那份 JSON。
//
// 基准由 `plugin/test/emit-plan.mjs` 经与生产同一条 esbuild 路径吐出,所以
// **「JS 产出的形状 == Go 消费的形状」这件事有用例锁着**,不靠人去对字段名。
// 跨语言接缝上字段名写错、取整口径不一致这类问题,不会有编译期报错。
type wirePlan struct {
	From    PlanPoint   `json:"from"`
	To      PlanPoint   `json:"to"`
	Seed    int         `json:"seed"`
	Points  []PlanPoint `json:"points"`
	PressMs float64     `json:"pressMs"`
}

func loadWirePlan(t *testing.T) wirePlan {
	t.Helper()
	b, err := os.ReadFile("testdata/wire-plan.json")
	if err != nil {
		t.Fatal(err)
	}
	var p wirePlan
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("插件产出的计划 Go 这边解不开——跨语言接缝对不上了:%v", err)
	}
	return p
}

// 跨语言接缝:JS 吐出来的 JSON,Go 必须原样吃进去,而且语义要对得上。
func TestWirePlanDecodesWithMatchingSemantics(t *testing.T) {
	p := loadWirePlan(t)
	if len(p.Points) < 10 {
		t.Fatalf("计划只有 %d 帧,基准像是坏的", len(p.Points))
	}
	last := p.Points[len(p.Points)-1]
	if math.Abs(last.X-p.To.X) > 0.01 || math.Abs(last.Y-p.To.Y) > 0.01 {
		t.Fatalf("末帧 (%.3f,%.3f) 没落在目标 (%.0f,%.0f) 上——"+
			"引擎的坐标空间与我们的理解不一致", last.X, last.Y, p.To.X, p.To.Y)
	}
	for i := 1; i < len(p.Points); i++ {
		if p.Points[i].T < p.Points[i-1].T {
			t.Fatalf("第 %d 帧时刻倒退:%.2f -> %.2f", i, p.Points[i-1].T, p.Points[i].T)
		}
	}
	// pressMs 来自 464 条实测池,不是常数。范围核对只是防「字段没传过来变成 0」。
	if p.PressMs < 20 || p.PressMs > 900 {
		t.Fatalf("pressMs=%v 不在实测池的量程内,多半是字段没接上", p.PressMs)
	}
}

// **这条会真的接管本机鼠标一秒左右**,所以默认跳过:
//
//	RECRUITHELPER_DEV_MOUSE_TEST=1 go test ./client/service/internal/handinput/ -run WireEndToEnd -v
//
// 它是这套架构的第一条真实端到端:插件那半生成的计划 -> Go 手服务按时刻播 ->
// 真实系统输入。上游从来没跑过这个形态(那边是 Go 自己生成自己播)。
//
// 只移动、**绝不点击**,跑完把光标放回原处。
func TestWireEndToEndPlaysRealTrajectory(t *testing.T) {
	if os.Getenv("RECRUITHELPER_DEV_MOUSE_TEST") != "1" {
		t.Skip("需要 RECRUITHELPER_DEV_MOUSE_TEST=1 —— 本用例会真的接管鼠标")
	}
	inj, err := NewInjector()
	if err != nil {
		t.Skipf("本平台没有注入实现:%v", err)
	}
	defer inj.Close()

	x0, y0, err := inj.CursorPos()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inj.MouseMove(float64(x0), float64(y0)) }()

	plan := loadWirePlan(t)
	s := NewService(inj)
	// 恒等标定:CSS 与系统坐标一比一。真机上这个映射由搭车标定学出来,
	// 这里写死是为了让判据只盯播放本身,不掺标定的误差。
	s.Seed(WindowHint{ScreenX: 0, ScreenY: 0, DPR: 1})

	res, err := s.Play(plan.Points)
	if err != nil {
		t.Fatalf("播放失败:%v", err)
	}
	t.Logf("平台 %s", inj.Platform())
	t.Logf("时间源 %s，分辨率 %v", ClockSource(), ClockResolution())
	t.Logf("%d 帧，计划跨度 %.0fms，撞格 %d 帧",
		len(res.Injected), plan.Points[len(plan.Points)-1].T, res.Unreachable)
	t.Logf("相对计划的滞后：均值 %.0fus，最大 %.0fus", res.LagMeanUs, res.LagMaxUs)

	got := res.Injected[len(res.Injected)-1]
	want := plan.Points[len(plan.Points)-1]
	if math.Abs(float64(got.SX)-want.X) > 1 || math.Abs(float64(got.SY)-want.Y) > 1 {
		t.Fatalf("末帧发到了 (%d,%d)，计划是 (%.0f,%.0f)", got.SX, got.SY, want.X, want.Y)
	}
	// 判据是**回读**,不是返回值:未授权时 CGEventPost 不报错、事件被静默丢弃。
	cx, cy, err := inj.CursorPos()
	if err != nil {
		t.Fatal(err)
	}
	if d := math.Hypot(float64(cx)-want.X, float64(cy)-want.Y); d > 2 {
		t.Fatalf("光标停在 (%d,%d)，离计划末点偏 %.1f 像素——"+
			"若 Platform() 报未授权，事件是被系统静默丢弃了", cx, cy, d)
	}
}
