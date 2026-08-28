package handinput

import (
	"fmt"
	"math"
	"runtime"
	"sync"
)

// PlanPoint 是插件算好的一帧:去哪儿(视口 CSS 坐标)、什么时候(相对本计划起点的毫秒)。
//
// **坐标是插件给的最终值,本包不做任何平移。** 上游原型由注入器把轨迹平移到视口
// 中央,那是因为原型里只有 Go 知道视口多大;我方反过来——视口与目标元素都是插件的
// 知识,而且「整条轨迹会不会走出视口」这道检查必须在交坐标之前做,也只有插件做得了。
type PlanPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	T float64 `json:"t"`
}

// Injected 记一帧实际发生了什么,只回给插件做诊断,不参与任何判定。
type Injected struct {
	SX int   `json:"sx"`
	SY int   `json:"sy"`
	At int64 `json:"at"` // 实际发出时刻(相对本计划起点的微秒)
}

// PlayResult 是一次播放的结果。
type PlayResult struct {
	Injected []Injected `json:"injected"`
	// Unreachable 是撞格的帧数:缩放 <100% 时两个不同 CSS 位置会落进同一系统像素,
	// 那一帧鼠标根本不动、浏览器不派发事件。**如实上报,不悄悄凑一个。**
	Unreachable int     `json:"unreachable"`
	LagMeanUs   float64 `json:"lagMeanUs"`
	LagMaxUs    float64 `json:"lagMaxUs"`
	Status      string  `json:"status"`
	ClickArmed  bool    `json:"clickArmed"`
}

// State 是插件在生成计划之前要问的那几件事。
type State struct {
	Platform string `json:"platform"`
	// CursorCSSX/Y 是光标此刻所在,已按当前标定反算成视口 CSS 坐标。
	// **还没有任何标定(连粗估都没播)时是 nil,不是 0**:那时我们是真不知道,
	// 而 0 会被编排层当成"光标在视口左上角"照着算一条轨迹出来。
	//
	// **编排层必须每次现读它当起点。** Snap 与 ToClient 用同一份标定、互为逆运算,
	// 所以现读再反算必然落回原地,不管标定多离谱;而用记忆里的 CSS 值没有这个抵消,
	// 标定一被修正就错位,错位量正好等于修正量——冷启动时那是一两百像素的一次
	// 干净瞬移,也就是评分台里最差的那个形状。
	CursorCSSX *float64 `json:"cursorCssX"`
	CursorCSSY *float64 `json:"cursorCssY"`
	Calibrated bool     `json:"calibrated"`
	ClickArmed bool     `json:"clickArmed"`
	Samples    int      `json:"samples"`
	// ResidualPx 是当前拟合的残差(CSS px)。**样本不足两个时是 nil,不是 0**——
	// 0 的意思是"拟合完美",跟"还没得拟合"差着十万八千里,而这个面是给人读的。
	ResidualPx  *float64 `json:"residualPx"`
	ClockSource string   `json:"clockSource"`
}

// Service 是手服务本体。
//
// **它只把数据变成过程**:收一份算好的计划,按时刻发出去;收一个落点,喂给标定。
// 它不生成轨迹、不决定去哪儿、不重试、不等待业务条件。一旦这里出现「生成」或
// 「决定走哪条路径」,数据/过程的分界就破了(见 doc.go)。
type Service struct {
	mu     sync.Mutex
	inj    Injector
	pb     Piggyback
	seeded bool
	// armed 是「这一次允许点击」。它由一次**被接受的落点**点亮,并在每次点击后
	// 熄灭——所以每一次点击都必须有一次属于它自己的落点确认,不能靠上一次的。
	armed bool
	mode  WaitMode
	// lastInj 是最后一帧发出去的系统坐标,给落点配对用。
	//
	// 只配落点、不配整条轨迹:浏览器一帧只派发一个 mousemove,好几个注入帧会并成
	// 一个观测事件,按下标配不上;而观测带的是页面时钟,跟我们的差一个未知偏移,
	// 按时刻配就得先对齐时钟。**落点两样都不需要**——移动结束光标静止,静止就不再
	// 派发事件,所以最后一个观测事件必然停在最后一个注入位置上。一次移动 = 一个可靠样本。
	lastInj *[2]float64
}

// finiteOrNil 把 NaN/Inf 挡在序列化之外。
//
// **这是真机第一跑照出来的**:样本不足两个时 Piggyback.Residual() 返回 NaN,
// 而 encoding/json 编不了 NaN——于是 /handinput/state 回了 200 却带着空 body。
// 头已经发出去了,错误无处可去。所以 NaN 必须在进结构体之前就挡掉,
// 不能指望序列化那一步报错。
func finiteOrNil(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

func NewService(inj Injector) *Service {
	return &Service{inj: inj, mode: WaitHybrid}
}

func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := State{Platform: s.inj.Platform(), ClockSource: clockSourceName(),
		ClickArmed: s.armed, Samples: s.pb.N(), ResidualPx: finiteOrNil(s.pb.Residual())}
	c, ready := s.pb.Calib()
	st.Calibrated = ready
	// 零值 Calib 的 ScaleX/Y 是 0,ToClient 会除以零得到 Inf——真机第一跑就是
	// 这么把 /handinput/state 变成"200 加空 body"的。所以既查 seeded 也查有限性。
	if s.seeded {
		if x, y, err := s.inj.CursorPos(); err == nil {
			cx, cy := c.ToClient(float64(x), float64(y))
			st.CursorCSSX, st.CursorCSSY = finiteOrNil(cx), finiteOrNil(cy)
		}
	}
	return st
}

// Seed 用插件自报的窗口粗估播一个初始映射。只在还没标定过时有效。
func (s *Service) Seed(h WindowHint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seeded {
		return
	}
	s.pb.Seed(h, 0, 0)
	s.seeded = true
}

// Play 按时刻把计划发出去。
//
// 播放本身**不设放行闸**:移动没有副作用,标定再差也只是移偏,而移偏正是标定
// 学习所需要的样本。闸在 Click 上。
func (s *Service) Play(points []PlanPoint) (PlayResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(points) == 0 {
		return PlayResult{}, fmt.Errorf("空计划")
	}
	// 把播放 goroutine 钉在一个 OS 线程上。
	//
	// **它在 macOS 上实测没用**:开发机上 5 趟里仍有 2 趟出现 10~22ms 的单帧离群,
	// 加不加一个样。GC 也不是元凶——GOGC=off 之后离群照旧。那是 OS 级的调度抖动。
	//
	// 保留它的理由是**方法上的**,不是"多一层保险":上游在 Windows 上量到的
	// 「发出时刻相对计划最大偏差 0.79~2.49ms」是在 LockOSThread 之下测的
	// (它们的 CLI 在 main 里无条件调)。我方若不加,将来在 Windows 上量出差异时,
	// 分不清是架构带来的还是这一行缺席带来的。等 Windows 上有了自己的数,
	// 再决定它去留——那时才有资格判。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// 播放期间一律不许点击:光标正在移动,上一次的落点确认已经过期。
	s.armed = false

	c, _ := s.pb.Calib()
	res := PlayResult{Injected: make([]Injected, 0, len(points))}
	base := nowNanos()
	var lagSum, lagMax float64
	for _, p := range points {
		sx, sy, ok := c.Snap(int(math.Round(p.X)), int(math.Round(p.Y)))
		if !ok {
			res.Unreachable++
		}
		deadline := base + int64(p.T*1e6)
		WaitUntil(deadline, s.mode)
		at := nowNanos()
		if err := s.inj.MouseMove(float64(sx), float64(sy)); err != nil {
			return res, fmt.Errorf("第 %d 帧注入失败:%w", len(res.Injected), err)
		}
		lag := float64(at-deadline) / 1e3
		lagSum += lag
		lagMax = math.Max(lagMax, math.Abs(lag))
		res.Injected = append(res.Injected, Injected{SX: sx, SY: sy, At: (at - base) / 1e3})
		s.lastInj = &[2]float64{float64(sx), float64(sy)}
	}
	res.LagMeanUs = lagSum / float64(len(points))
	res.LagMaxUs = lagMax
	res.Status = "ok"
	return res, nil
}

// Landing 收一个落点样本:插件读回的最后一个观测位置。
//
// 这是**搭车标定**——这次移动本来就要做,落点是白送的样本。生产里绝不跑九点标定:
// 九次机械瞬移正是评分台里得分最差的那个形状,开工前跑一遍等于自报家门。
func (s *Service) Landing(clientX, clientY float64) (PBStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	last := s.lastInj
	if last == nil {
		s.armed = false
		return PBCold, fmt.Errorf("这一轮还没播过任何计划,落点无从配对")
	}
	st, err := s.pb.Observe(Sample{
		ScreenX: last[0], ScreenY: last[1], ClientX: clientX, ClientY: clientY,
	})
	// 只有「被接受的落点 + 标定就绪」才点亮点击。存疑与重置都不点亮——
	// 方向永远是宁可不点。
	s.armed = st == PBReady
	return st, err
}

// Click 按一次左键。
//
// 两道闸都在这儿,缺一不点:标定就绪,且本次移动的落点刚刚被接受过。
// 点完立刻熄灭——下一次点击必须有属于它自己的落点确认。
func (s *Service) Click(pressMs float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.armed {
		return fmt.Errorf("未放行:标定未就绪,或本次移动的落点没有被接受")
	}
	if pressMs <= 0 || pressMs > 2000 {
		return fmt.Errorf("按压时长 %.1fms 不在合理范围", pressMs)
	}
	s.armed = false
	if err := s.inj.MouseDown(MouseLeft); err != nil {
		return err
	}
	WaitUntil(nowNanos()+int64(pressMs*1e6), s.mode)
	return s.inj.MouseUp(MouseLeft)
}
