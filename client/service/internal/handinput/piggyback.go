package handinput

// 搭车标定：**生产的标定方式**。不做任何专门的标定动作。
//
// 九点标定(上游 `mousecalib.go`)在生产里明令不能跑 —— 九次机械瞬移正是评分台里
// `teleport` 引擎的形状，所有候选里得分最差的一个，在 BOSS 页面上跑一遍等于
// 开工前先自报家门。所以生产只能**搭车**：我们本来就在为业务移动鼠标。
//
// # 搭哪一趟车：落点，不是整条轨迹
//
// `mousecalib.go` 的头部原先写的是「一条业务轨迹几十个点，整条拿去拟合」。
// **那一句做不到**，这里订正：
//
//   合帧    浏览器一帧只派发一个 mousemove，好几个注入帧并成一个观测事件 ——
//           所以观测和注入不是一一对应，没法按下标配对。
//   时钟    观测带的是页面的 `performance.now()`，跟我们的 `nowNanos()` 差一个
//           未知偏移。按时刻配对就得先对齐时钟，而那正是整条对账链里唯一
//           被刻意避开的依赖（见 mouseplay.go：观测点集合判据不需要时钟对齐）。
//
// **落点这一个配对两样都不需要。** 移动结束后光标静止，静止就不再派发事件，
// 所以最后一个观测事件必然停在最后一个注入位置上。一次移动 = 一个可靠样本。
//
// 代价是**每次移动只产一个样本**，比原先设想的「几十个」慢得多。补偿是它零假设：
// 不猜时钟、不猜合帧、不需要任何对齐。慢而确定，好过快而可疑。
//
// （驻留点也是无歧义的位置，但驻留期间光标不动、浏览器压根不派发事件，
// 所以那里没有样本可捡。这一条查过了，不是没想到。）
//
// # 失效检测是白送的
//
// 窗口被拖走、系统缩放改了、页面 zoom 变了 —— 不用监听任何窗口事件：
// **新落点对不上当前拟合，就是几何变了。**
//
// 但「一个样本对不上」和「几何真的变了」不是一回事（浏览器可能丢了最后一个事件，
// 也可能是人碰了鼠标）。所以分两级，方向永远是宁可不点：
//
//   一次对不上   → 存疑，**这一次不许点击**，但历史留着
//   连续两次     → 几何真的变了，历史全丢，回冷启动
//
// # 为什么标定漂了必须硬失败
//
// 不是「可能点错元素」这种业务事故 —— 是**必然上报**：标定漂了会让 `clientX`
// 变成负数或越界，直接点亮 BOSS 的 700005 / 700007 检测码(上游 report §30.2)。

import (
	"fmt"
	"math"
	"sync"
)

// ToleranceProdPx 是**生产运行**能接受的标定误差上限(CSS 像素)。搬自上游
// hiBoss `lab/engine/inject/mousecalib.go`,推导原样保留:
//
// 误差的分布是**双峰**的:
//
//	拟合噪声 / 半像素翻转          0~2 px
//	───── 中间几乎不会自然出现 ─────
//	窗口被拖动 / zoom 改变 / 换屏   几十 ~ 几百 px
//
// 中间那一档需要一个「刚好错一点点」的原因,而实际上没有这种原因。
// 所以门限只要落在 2 和 8 之间就既不误报也不漏报。4 同时满足三条:
// 是噪声上界的两倍、是最紧目标半径的一半、是结构性失效的零头。
const ToleranceProdPx = 4.0

// WindowHint 是插件自报的窗口粗估。**它是原始事实,不是映射** ——
// 怎么把它翻成 Calib 由各平台的注入器决定(见 Injector.SeedCalib)。
//
// 上游是标定页 POST /calib/hint 上报的;我方由插件在定位时顺带读一次。
// 三个值都可能不准:`window.screenX` 上游实测过它有时直接是错的(窗口一次没动,
// 页面报的值在 430 与 0 之间跳),所以它**只能用来猜往哪个方向先动,不许参与计算**。
type WindowHint struct {
	// ScreenX/ScreenY 是页面自报的视口原点,**CSS 像素**。
	ScreenX, ScreenY float64
	// DPR 是页面自报的设备像素比。
	DPR float64
}

// PBStatus 是搭车标定当前能不能用。
type PBStatus int

const (
	// PBCold 样本还不够或几何跨度不够 —— 只能用页面粗估移动，**不许点击**。
	PBCold PBStatus = iota
	// PBReady 拟合可用。
	PBReady
	// PBSuspect 最新的落点对不上当前拟合 —— **这一次不许点击**，历史先留着。
	PBSuspect
	// PBReset 连续对不上，几何真的变了 —— 历史已丢，回冷启动。
	PBReset
)

func (s PBStatus) String() string {
	return [...]string{"冷启动", "就绪", "存疑", "已重置"}[s]
}

// MinSpanPx 是样本在单轴上必须张开的最小跨度，**单位是 CSS px（client 侧）**。
//
// **这个数是推出来的，不是拍的。** `clientX` 是整数，所以每个观测带 ±0.5px 的
// 取整噪声。两个相距 R 的样本，最坏情况下两端噪声反向，解出的 scale 相对误差
// 上界是 1.0/R。而 `snapScale` 要把 scale 吸到最近的合法档，容差 0.5% ——
// 要让原始拟合落进那个容差里：
//
//	1.0 / R < 0.005   →   R > 200
//
// 这是两点的最坏界，样本更多时实际更松，所以 200 是保守的。
//
// **必须量 client 跨度，不能量 screen 跨度。** 噪声在 client 那一侧（clientX 是整数），
// 推导也在那一侧。量 screen 的话这道门会自动放松 scale 倍：
//
//	scale   screen 门限 200 → 实际 client 跨度   scale 误差上界   吸附容差 0.5%
//	1.0                            200               0.500%        ✓
//	1.5                            133               0.750%        ❌ 越过
//	2.0                            100               1.000%        ❌ 越过
//
// 越过之后 `snapScale` 吸不住，留下一个带噪声的分数 scale —— 那正是 §30 记的那种
// 「残差只有 0.433px、两条自检全过，而映射在 cy≈520 处整体偏一格」。
// **scale 恰好为 1 时两侧数值相同，所以这个错在 macOS 上永远看不出来**，
// 而 Windows 的每一个缩放档都会踩到。2026-08-27 写这一层时就量错了侧，当天查出。
const MinSpanPx = 200.0

// PBDriftPx 是「新落点算不算对得上」的门限。
//
// 用 ToleranceProdPx（4px）跟 `landingMustMatch` 同一个数 —— 它们判的是同一件事：
// 浏览器上报的位置和我们算的位置差多少。两处若各用各的，早晚一处改了另一处没改。
const PBDriftPx = ToleranceProdPx

// PBMaxSamples 是样本环的容量。
//
// 不是精度考虑（样本越多拟合越稳），是**新鲜度**：几何是会变的，而变化的检测靠
// 「新样本对不上」。留太久的样本会在重新拟合时把已经过时的几何拽回来。
const PBMaxSamples = 64

// Piggyback 累积业务移动的落点，持续拟合。零值可用。
//
// **并发安全**：注入侧写、判据侧读，两边不在一个 goroutine 上。
type Piggyback struct {
	mu      sync.Mutex
	samples []Sample
	c       Calib
	ready   bool
	miss    int // 连续对不上的次数
}

// SeedCalib 播一个初始映射，**只够走「不点击」的第一趟车**。
//
// 映射由注入器按本平台的坐标单位算出（见 Injector.SeedCalib）：Windows 的 SendInput
// 收虚拟桌面物理像素，macOS 的 CGEventPost 收 point，同一份 screenX 要做的翻译不一样。
//
// 播完之后状态仍是冷启动：能移动，不能点击。
//
// **样本一并清空** —— 播种的语义就是"回到冷启动"。首次播种时样本本来是空的，
// 这一行只在**重新**播种时起作用：那时旧样本描述的是旧几何（窗口换了屏），
// 留着只会拟合出一个哪边都不对的中间值，与 Observe 里 PBReset 丢历史同理。
func (p *Piggyback) SeedCalib(c Calib) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.c = c
	p.ready = false
	p.samples = nil
	p.miss = 0
}

// Calib 返回当前映射，以及它是否已经由观测标定过（false = 还是粗估）。
func (p *Piggyback) Calib() (Calib, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.c, p.ready
}

// Observe 收一个落点样本并重新拟合。
//
// `s` 是这次移动的落点：`ScreenX/Y` 是我们最后发出去的系统坐标，
// `ClientX/Y` 是浏览器上报的最后一个位置。
func (p *Piggyback) Observe(s Sample) (PBStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 已经标定过的话，先拿新样本查当前拟合 —— 这一步就是失效检测。
	if p.ready {
		cx, cy := p.c.ToClient(s.ScreenX, s.ScreenY)
		d := math.Hypot(cx-s.ClientX, cy-s.ClientY)
		if d > PBDriftPx {
			p.miss++
			if p.miss >= 2 {
				// 几何真的变了。**历史必须全丢** —— 旧样本描述的是旧几何，
				// 混进新拟合只会得到一个哪边都不对的中间值。
				p.samples, p.ready, p.miss = nil, false, 0
				return PBReset, fmt.Errorf("落点连续两次对不上（这次偏 %.1f px）—— 几何变了，标定作废", d)
			}
			return PBSuspect, fmt.Errorf("落点偏 %.1f px 超过 %.0f —— 这一次不许点击", d, PBDriftPx)
		}
		p.miss = 0
	}

	p.samples = append(p.samples, s)
	if len(p.samples) > PBMaxSamples {
		p.samples = p.samples[len(p.samples)-PBMaxSamples:]
	}
	if !p.hasSpan() {
		p.ready = false
		return PBCold, nil
	}
	c, err := Calibrate(p.samples)
	if err != nil {
		p.ready = false
		return PBCold, err
	}
	p.c, p.ready = c, true
	return PBReady, nil
}

// hasSpan 判样本在两个轴上都张得够开。**量 client 侧**，见 MinSpanPx 的推导。
//
// **不能只靠 Calibrate 的可解性判据。** 那一条只拒绝「严格共线」；几乎共线的
// 一把样本照样解得出来，解出来的 scale 却由噪声主导。
func (p *Piggyback) hasSpan() bool {
	if len(p.samples) < 2 {
		return false
	}
	minX, maxX := math.Inf(1), math.Inf(-1)
	minY, maxY := math.Inf(1), math.Inf(-1)
	for _, s := range p.samples {
		minX, maxX = math.Min(minX, s.ClientX), math.Max(maxX, s.ClientX)
		minY, maxY = math.Min(minY, s.ClientY), math.Max(maxY, s.ClientY)
	}
	return maxX-minX >= MinSpanPx && maxY-minY >= MinSpanPx
}

// Residual 是当前样本集在当前拟合下的残差（CSS px）。样本不足时返回 NaN。
func (p *Piggyback) Residual() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.samples) < 2 {
		return math.NaN()
	}
	return p.c.Residual(p.samples)
}

// N 是当前留着的样本数。
func (p *Piggyback) N() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.samples)
}
