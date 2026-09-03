package handinput

import (
	"math"
	"strings"
	"testing"
)

// fakeInjector 记下每一次调用,并按一份**真实几何**回答光标位置——于是落点可以
// 像真机一样算出来:我们按当前(可能错的)标定发出去,浏览器按真实几何看到别处。
type fakeInjector struct {
	unauthorized bool
	truth        Calib
	moves        [][2]float64
	downs        int
	ups          int
	failAt       int // >0 时第几次移动开始报错
	// seed 非零时,SeedCalib 直接返回它 —— 用来构造"粗估算错了"的场面。
	seed *Calib
	// cursorAt 非空时,CursorPos 报它而不是最后注入的那一点 —— 用来构造
	// "落点确认之后有人碰了鼠标"的场面。
	cursorAt *[2]float64
	// cursorErr 为真时 CursorPos 报错 —— 构造"连光标都读不到"的场面。
	cursorErr bool
	// keys 记下按键序列,形如 "KeyN↓" / "KeyN↑",供打字用例逐项核对。
	keys []string
	// unknownKey 非空时,KnowsKey 对它报错 —— 构造"计划里有注入器不认识的键"。
	unknownKey string
	// keyFailAt >0 时,第几次按键动作开始报错 —— 构造"打到一半失败"。
	keyFailAt int
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
func (f *fakeInjector) KnowsKey(code string) error {
	if f.unknownKey != "" && code == f.unknownKey {
		return errFake
	}
	return nil
}
func (f *fakeInjector) KeyDown(code string) error { return f.recordKey(code, "\u2193") }
func (f *fakeInjector) KeyUp(code string) error   { return f.recordKey(code, "\u2191") }

func (f *fakeInjector) recordKey(code, arrow string) error {
	f.keys = append(f.keys, code+arrow)
	if f.keyFailAt > 0 && len(f.keys) >= f.keyFailAt {
		return errFake
	}
	return nil
}
func (f *fakeInjector) CursorPos() (int, int, error) {
	if f.cursorErr {
		return 0, 0, errFake
	}
	if f.cursorAt != nil {
		return int(f.cursorAt[0]), int(f.cursorAt[1]), nil
	}
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
func (f *fakeInjector) Authorized() bool { return !f.unauthorized }
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

// 运行期间有人碰了鼠标 —— 页面报的"最后一个 mousemove"就是那一下,而不是我们
// 注入的落点。把它喂进搭车标定等于用随机位置拟合几何,越学越歪,而外表看起来
// 一切正常(有样本、有残差、有状态)。2026-08-28 真机撞到过:两趟观测解出来的
// scale_y 是 -2.0,而屏幕映射不可能是负的。
func TestLandingRejectedWhenCursorMovedBysomeoneElse(t *testing.T) {
	s, f := newReadyService(t)
	if _, err := s.Play([]PlanPoint{{X: 500, Y: 400, T: 0}}); err != nil {
		t.Fatal(err)
	}
	// 真人把光标挪走了:CursorPos 不再是最后注入的那一点。
	f.moves = append(f.moves, [2]float64{9999, 9999})
	st, err := s.Landing(500, 400)
	if err == nil {
		t.Fatal("光标已被别人挪走,落点必须作废")
	}
	if st != PBCold {
		t.Fatalf("作废的落点应报冷启动,得到 %v", st)
	}
	if err := s.Click(96); err == nil {
		t.Fatal("作废落点之后不得放行点击")
	}
}

// 落点确认与点击派发之间有一次页面往返(编排层拿落点去问平台的命中测试)。
// 那个窗口里真人碰一下鼠标,点击就会落在任意元素上 —— 而 armed 对"现在"
// 一无所知,它只记得"刚才那次落点被接受过"。
func TestClickRefusedWhenCursorMovedAfterLanding(t *testing.T) {
	s, f := newReadyService(t)
	moved := [2]float64{9_000, 9_000}
	f.cursorAt = &moved
	if err := s.Click(96); err == nil {
		t.Fatal("落点确认之后光标被挪走,仍然放行了点击")
	}
	if f.downs != 0 || f.ups != 0 {
		t.Fatalf("被拒的点击不得真的按下去:down=%d up=%d", f.downs, f.ups)
	}
	// 拒绝之后必须熄灭:不能让下一次调用凭同一次陈旧的落点确认蒙混过去。
	f.cursorAt = nil
	if err := s.Click(96); err == nil {
		t.Fatal("被拒之后没有重新确认落点就放行了点击")
	}
}

// 读不到光标同样不点。失效方向永远是宁可不点,不是"读不到就当没动过"。
func TestClickRefusedWhenCursorUnreadable(t *testing.T) {
	s, f := newReadyService(t)
	f.cursorErr = true
	if err := s.Click(96); err == nil {
		t.Fatal("读不到光标位置仍然放行了点击")
	}
	if f.downs != 0 {
		t.Fatalf("被拒的点击不得真的按下去:down=%d", f.downs)
	}
}

// 重新播种是从"换屏死循环"里出来的唯一出口:旧映射把光标送到别的屏,页面收不到
// 任何事件,而修正映射又必须有观测。它是降级,所以既要真的清干净,也要真的回冷启动。
func TestReseedDropsEverythingAndGoesCold(t *testing.T) {
	s, f := newReadyService(t)
	if !s.State().Calibrated {
		t.Fatal("前置:这一步应当已经标定就绪")
	}
	if err := s.Click(96); err != nil {
		t.Fatalf("前置:此刻应当放行点击:%v", err)
	}

	before := s.State()
	s.Reseed(WindowHint{ScreenX: 2560, ScreenY: 517, DPR: 2})
	after := s.State()

	if after.Calibrated {
		t.Fatal("重新播种之后必须回到冷启动")
	}
	if after.Samples != 0 {
		t.Fatalf("旧样本描述的是旧几何,必须全丢,还剩 %d 条", after.Samples)
	}
	if after.ClickArmed {
		t.Fatal("标定都没了,上一次的落点确认必须作废")
	}
	if before.Samples == 0 {
		t.Fatal("用例前置失效:重新播种前本该有样本")
	}

	// lastInj 也清了:下一个落点不许跟一个属于旧几何的注入点配成样本。
	if _, err := s.Landing(100, 100); err == nil {
		t.Fatal("重新播种之后、还没播过计划就收落点,应当拒绝配对")
	}

	// 新种子必须真的按新窗口位置来 —— 副屏 x=2560,macOS 收 point 不乘 dpr。
	if _, err := s.Play([]PlanPoint{{X: 0, Y: 0, T: 0}}); err != nil {
		t.Fatal(err)
	}
	if got := f.moves[len(f.moves)-1]; got[0] != 2560 {
		t.Fatalf("新种子没按新窗口位置播:视口原点应当映到 x=2560,实际 %v", got)
	}
}

// 2026-09-03:客户端拉起的脑没有 macOS 辅助功能授权,CGEventPost 静默丢事件,整轮"没观测到
// mousemove"。授权状态必须随 /state 带给插件,让它移动前就拒并说清原因。
func TestStateCarriesInjectAuthorization(t *testing.T) {
	ok := NewService(&fakeInjector{})
	if !ok.State().InjectAuthorized {
		t.Fatal("已授权的注入器必须报 injectAuthorized=true")
	}
	denied := NewService(&fakeInjector{unauthorized: true})
	if denied.State().InjectAuthorized {
		t.Fatal("未授权的注入器必须报 injectAuthorized=false")
	}
}

// /keys:裸按键不经上屏机制,按时刻顺序播出;修饰键按住期间的字母键照发,松手后再删除。
func TestKeysPlaysSequenceInPlannedOrder(t *testing.T) {
	f := &fakeInjector{}
	s := NewService(f)
	res, err := s.Keys(KeySequence{Keys: []PlanKey{
		{Code: "ControlLeft", Down: 0, Up: 230, Modifier: true},
		{Code: "KeyA", Down: 90, Up: 170},
		{Code: "Backspace", Down: 420, Up: 500},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ControlLeft↓", "KeyA↓", "KeyA↑", "ControlLeft↑", "Backspace↓", "Backspace↑"}
	if strings.Join(f.keys, " ") != strings.Join(want, " ") {
		t.Fatalf("按键顺序不对:%v", f.keys)
	}
	if res.Keys != 6 || res.Status != "ok" {
		t.Fatalf("回包不对:%+v", res)
	}
}

// 键表里没有的键在发出任何一次按键之前就拒——失效方向是一个都没发。
func TestKeysRejectsUnknownCodeBeforeAnyPress(t *testing.T) {
	f := &fakeInjector{unknownKey: "MetaLeft"}
	s := NewService(f)
	if _, err := s.Keys(KeySequence{Keys: []PlanKey{
		{Code: "MetaLeft", Down: 0, Up: 230, Modifier: true},
		{Code: "KeyA", Down: 90, Up: 170},
	}}); err == nil {
		t.Fatal("注入器不认识的键放行了")
	}
	if len(f.keys) != 0 {
		t.Fatalf("校验失败后仍发了按键:%v", f.keys)
	}
}

// 修饰键松手到下一键按下不足 40ms 属排版错误,同样在发键前拒。
func TestKeysRejectsModifierWindowShortfall(t *testing.T) {
	f := &fakeInjector{}
	s := NewService(f)
	if _, err := s.Keys(KeySequence{Keys: []PlanKey{
		{Code: "ControlLeft", Down: 0, Up: 230, Modifier: true},
		{Code: "KeyA", Down: 90, Up: 170},
		{Code: "Backspace", Down: 250, Up: 330},
	}}); err == nil {
		t.Fatal("修饰键窗口不足放行了")
	}
	if len(f.keys) != 0 {
		t.Fatalf("校验失败后仍发了按键:%v", f.keys)
	}
}
