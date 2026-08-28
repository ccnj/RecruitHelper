package handinput

import (
	"math"
	"testing"
)

// fakeInjector 记下每一次调用,并按一份**真实几何**回答光标位置——于是落点可以
// 像真机一样算出来:我们按当前(可能错的)标定发出去,浏览器按真实几何看到别处。
type fakeInjector struct {
	truth  Calib
	moves  [][2]float64
	downs  int
	ups    int
	failAt int // >0 时第几次移动开始报错
	// seed 非零时,SeedCalib 直接返回它 —— 用来构造"粗估算错了"的场面。
	seed *Calib
}

func (f *fakeInjector) MouseMove(x, y float64) error {
	f.moves = append(f.moves, [2]float64{x, y})
	if f.failAt > 0 && len(f.moves) >= f.failAt {
		return errFake
	}
	return nil
}
func (f *fakeInjector) MouseDown(int) error { f.downs++; return nil }
func (f *fakeInjector) MouseUp(int) error   { f.ups++; return nil }
func (f *fakeInjector) CursorPos() (int, int, error) {
	if len(f.moves) == 0 {
		return 0, 0, nil
	}
	last := f.moves[len(f.moves)-1]
	return int(last[0]), int(last[1]), nil
}
func (f *fakeInjector) SeedCalib(h WindowHint) Calib {
	if f.seed != nil {
		return *f.seed
	}
	// 缺省按 macOS 口径:注入 API 收 point,所以不乘 dpr。
	return Calib{ScaleX: 1, ScaleY: 1, OffsetX: h.ScreenX, OffsetY: h.ScreenY}
}
func (f *fakeInjector) Platform() string { return "fake" }
func (f *fakeInjector) Close()           {}

var errFake = &fakeErr{}

type fakeErr struct{}

func (*fakeErr) Error() string { return "假注入器故意失败" }

// 走一次「移动 -> 读落点 -> 喂标定」,返回本轮的标定状态。
func moveAndLand(t *testing.T, s *Service, f *fakeInjector, cssX, cssY float64) PBStatus {
	t.Helper()
	if _, err := s.Play([]PlanPoint{{X: cssX - 5, Y: cssY - 5, T: 0}, {X: cssX, Y: cssY, T: 1}}); err != nil {
		t.Fatal(err)
	}
	last := f.moves[len(f.moves)-1]
	cx, cy := f.truth.ToClient(last[0], last[1])
	st, _ := s.Landing(math.Floor(cx+0.5), math.Floor(cy+0.5))
	return st
}

func newReadyService(t *testing.T) (*Service, *fakeInjector) {
	t.Helper()
	f := &fakeInjector{truth: Calib{ScaleX: 1, ScaleY: 1, OffsetX: 0, OffsetY: 151}}
	s := NewService(f)
	s.mode = WaitSleep
	s.Seed(WindowHint{ScreenX: 0, ScreenY: 151, DPR: 1})
	moveAndLand(t, s, f, 40, 30)
	if st := moveAndLand(t, s, f, 900, 700); st != PBReady {
		t.Fatalf("两次落点后应就绪,得到 %v", st)
	}
	return s, f
}

// 播放期间与播放之后都不许点击:光标正在移动,上一次的落点确认已经过期。
func TestClickRefusedBeforeAnyLanding(t *testing.T) {
	f := &fakeInjector{truth: Calib{ScaleX: 1, ScaleY: 1}}
	s := NewService(f)
	s.mode = WaitSleep
	if err := s.Click(96); err == nil {
		t.Fatal("没标定、没落点就放行了点击")
	}
	if _, err := s.Play([]PlanPoint{{X: 10, Y: 10, T: 0}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Click(96); err == nil {
		t.Fatal("只播了计划、没确认落点就放行了点击")
	}
	if f.downs != 0 || f.ups != 0 {
		t.Fatalf("被拒绝的点击不该真的按下去:down=%d up=%d", f.downs, f.ups)
	}
}

// 每一次点击都必须有属于它自己的落点确认,不能靠上一次的。
func TestClickDisarmsAfterEachClick(t *testing.T) {
	s, f := newReadyService(t)
	if err := s.Click(96); err != nil {
		t.Fatalf("就绪且落点刚被接受,应当放行:%v", err)
	}
	if f.downs != 1 || f.ups != 1 {
		t.Fatalf("一次点击应当恰好一按一抬:down=%d up=%d", f.downs, f.ups)
	}
	if err := s.Click(96); err == nil {
		t.Fatal("同一次落点确认放行了第二次点击")
	}
}

// 落点对不上时是存疑档:**这一次不许点击**,但历史留着。方向永远是宁可不点。
func TestClickRefusedWhenLandingDrifts(t *testing.T) {
	s, f := newReadyService(t)
	f.truth = Calib{ScaleX: 1, ScaleY: 1, OffsetX: 0, OffsetY: 451} // 窗口被拖走了
	if st := moveAndLand(t, s, f, 500, 400); st != PBSuspect {
		t.Fatalf("落点对不上应报存疑,得到 %v", st)
	}
	if err := s.Click(96); err == nil {
		t.Fatal("落点存疑却放行了点击")
	}
}

// 播放必须熄灭点击:光标已经离开上一次确认过的位置。
func TestPlayDisarmsClick(t *testing.T) {
	s, f := newReadyService(t)
	if _, err := s.Play([]PlanPoint{{X: 300, Y: 300, T: 0}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Click(96); err == nil {
		t.Fatal("播放之后没有重新确认落点就放行了点击")
	}
	_ = f
}

// 缩放 <100% 时两个不同 CSS 位置会撞进同一系统像素,那一帧鼠标根本不动、
// 浏览器不派发事件。必须如实上报,不许悄悄凑一个。
func TestPlayReportsUnreachableFrames(t *testing.T) {
	half := Calib{ScaleX: 0.5, ScaleY: 0.5}
	f := &fakeInjector{truth: half, seed: &half}
	s := NewService(f)
	s.mode = WaitSleep
	s.Seed(WindowHint{ScreenX: 0, ScreenY: 0, DPR: 0.5})
	var pts []PlanPoint
	for i := 0; i < 40; i++ {
		pts = append(pts, PlanPoint{X: float64(i), Y: 0, T: float64(i)})
	}
	res, err := s.Play(pts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Unreachable == 0 {
		t.Fatal("scale=0.5 下必然有帧撞格,一帧都没报说明失效方向反了")
	}
}

// 计划的时刻必须被尊重:实际发出的跨度不能短于计划跨度。
// (只断言下界——上界受机器负载影响,那是真机才该量的事。)
func TestPlayHonoursPlannedTimeline(t *testing.T) {
	f := &fakeInjector{truth: Calib{ScaleX: 1, ScaleY: 1}}
	s := NewService(f)
	s.mode = WaitSleep
	res, err := s.Play([]PlanPoint{{X: 0, Y: 0, T: 0}, {X: 10, Y: 0, T: 30}, {X: 20, Y: 0, T: 60}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Injected) != 3 {
		t.Fatalf("应发出 3 帧,实际 %d", len(res.Injected))
	}
	if span := res.Injected[2].At - res.Injected[0].At; span < 55000 {
		t.Fatalf("计划跨度 60ms,实际只用了 %.1fms —— 时刻没被尊重", float64(span)/1000)
	}
}

// 注入中途失败必须把已发出的部分带回去:插件要知道光标停在哪儿了。
func TestPlayReturnsPartialProgressOnInjectionFailure(t *testing.T) {
	f := &fakeInjector{truth: Calib{ScaleX: 1, ScaleY: 1}, failAt: 3}
	s := NewService(f)
	s.mode = WaitSleep
	res, err := s.Play([]PlanPoint{{X: 0, Y: 0, T: 0}, {X: 1, Y: 0, T: 1}, {X: 2, Y: 0, T: 2}, {X: 3, Y: 0, T: 3}})
	if err == nil {
		t.Fatal("注入失败应当报错")
	}
	if len(res.Injected) != 2 {
		t.Fatalf("失败前已发出 2 帧,应当带回来,实际 %d", len(res.Injected))
	}
}
