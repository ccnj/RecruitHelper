package handinput

// 坐标换算与标定。**平台无关** —— 这一层不知道 CGEventPost 还是 SendInput。
//
// # 起点是网页坐标，终点是系统坐标
//
// 引擎吐的是**网页坐标**（CSS 像素，`clientX`/`clientY`）。理由见上游
// hiBoss `report/runtime-evidence.md` §30 与 HANDOFF：BOSS 记的就是 `clientX`
// （`sec-entry.deob.js:2935` 的 `getMousePathHistory(a.clientX, a.clientY, …)`），
// 我们整套验收体系——评分台、真人基线、1606 次逐字节对账——全部建在这个坐标系上。
// 引擎若吐系统坐标，离线评分就得先模拟一遍换算，而换算依赖机器，
// **「离线跑绿 ≈ 真机跑绿」会直接断掉。**
//
// # 标定：别算，观测
//
// 从视口原点推到屏幕原点要一路加：窗口位置 + 边框 + 标题栏 + 标签栏 + 地址栏 +
// 书签栏 + 页面 zoom + 系统缩放 + Retina 倍率。每一项都要查一个 API，
// 每一项都可能因浏览器版本/系统主题/用户设置而变，**任何一项错了都不知道错在哪**。
//
// 观测的做法：注入一个已知的系统坐标，读插件在页面里观测到的 `clientX`。
// 两个不同位置就解出 scale 和 offset，**中间有多少层、每层多厚一概不需要知道**。
//
// 副作用是单位也不需要知道：macOS 的 CGEventPost 收 point，Windows 的 SendInput
// 收归一化到 0..65535 的虚拟桌面坐标 —— 拟合出来的 scale 会把单位差异一起吸收掉。
// 所以 `Injector.MouseMove` 收的就是**该平台注入 API 自己的那个数**。
//
// # 失效条件
//
//   窗口被拖动/缩放、系统缩放改变、页面 zoom 改变、全屏切换、拖到另一块屏  → 要重标定
//   页面滚动                                                          → 不用
//
// 滚动不失效，因为 `clientX` 是**视口坐标** —— 鼠标相对窗口的位置，与滚到哪儿无关。
// 这一点恰好是 BOSS 选 clientX 帮了我们。

import (
	"fmt"
	"math"
)

// Calib 是「网页坐标 → 系统坐标」的仿射映射，两轴各自独立。
//
//	sx = cssX*ScaleX + OffsetX
type Calib struct {
	ScaleX, ScaleY   float64
	OffsetX, OffsetY float64
	// Snapped 是**两个轴的 scale 都被吸到了合法档**。
	//
	// 吸不住不等于标定坏了 —— 真实缩放不在表里时（Windows 225%、或者系统缩放
	// 乘上浏览器 zoom 的那些乘积）本来就吸不住，那时留下的是原始拟合，
	// 它可能足够好也可能不够。**够不够由哨兵探针说了算，不由这个字段说了算。**
	//
	// 但它必须**被说出来**：先前吸不住是静默的，而「静默地用一个带噪声的分数 scale」
	// 正是 §30 那次事故的形态（残差 0.433px、两条自检全过、映射整体偏一格）。
	Snapped bool
}

// Sample 是一次标定观测：我们发出去的系统坐标，与插件读回的网页坐标。
type Sample struct {
	ScreenX, ScreenY float64 // 我们发给注入 API 的数
	ClientX, ClientY float64 // 插件在 isolated world 读到的 clientX/clientY
}

// ToScreen 把网页坐标翻成系统坐标（浮点，未取整）。
func (c Calib) ToScreen(cssX, cssY float64) (float64, float64) {
	return cssX*c.ScaleX + c.OffsetX, cssY*c.ScaleY + c.OffsetY
}

// ToClient 是反向：给定系统坐标，浏览器会算出哪个网页坐标。
func (c Calib) ToClient(sx, sy float64) (float64, float64) {
	return (sx - c.OffsetX) / c.ScaleX, (sy - c.OffsetY) / c.ScaleY
}

// Snap 把一个**整数网页坐标**翻成一个整数系统坐标，并保证往返一致：
// 浏览器拿这个整数系统坐标算回去、四舍五入，必须还是原来那个整数网页坐标。
//
// # 为什么需要往返，而不是乘一下取整
//
// 两个整数格子对不齐。BOSS 看到的格子是 CSS 像素（`Math.round(clientX)`，
// 见 sec-entry.deob.js:3009），我们能控制的格子是系统像素。
// 而**逐帧位移 `d`/`e` 正是判定特征的直接输入** —— 取整误差会改写位移序列，
// 也就改写 speedCV / accelerationCV。
//
// 缩放 ≥ 100% 时物理格比 CSS 格细，取整最多偏半个物理格，跨不过 Math.round 的
// 边界，所以怎么取整都能回来 —— 这一步是**兜底**，顺带给标定当自检。
//
// 缩放 < 100% 时（用户按了 Ctrl+-）物理格反而更粗，两个不同的 CSS 位置会撞进
// 同一个系统像素，那一帧鼠标根本不动、浏览器不派发事件、样本凭空消失。
// 那时 ok=false —— **必须如实上报，不能悄悄凑一个**。
func (c Calib) Snap(cssX, cssY int) (int, int, bool) {
	sx, ok1 := snap1(float64(cssX), c.ScaleX, c.OffsetX)
	sy, ok2 := snap1(float64(cssY), c.ScaleY, c.OffsetY)
	return sx, sy, ok1 && ok2
}

// # ⚠ 一个被证伪的想法：用「余量」自检（2026-08-25）
//
// 曾经想加一个 Fragility：算每个整数 CSS 位置翻过去再翻回来之后，离取整边界
// 还剩多远。想法是「余量小 = 经不起误差」，而且**不需要知道真值**。
//
// 拿几个真实 scale 实测（拟合噪声来自 clientY 的 ±0.5px 取整），直接被推翻：
//
//	真值        拟合         最小余量   真实错几个
//	1.000000    1.000000     0.4444        0
//	1.500000    1.500000     0.1667        0
//	1.237189    1.237043     0.0956        0     ← 余量最低，一个不错
//	1.350000    1.349741     0.1299        3
//	1.125000    1.125265     0.0556       33
//	1.375000    1.374373     0.1370       96
//	2.200000    2.196615     0.2724      694     ← 余量最高，错了 58%
//
// **完全不相关，甚至反相关。**
//
// 错在哪：余量是用**我们自己的** scale/offset 算的。而真正的病是
// **scale 本身偏了**，偏了之后误差随位置累积。用错的尺子量自己，量得再细也没用。
//
// 这是同一个盲区第三次出现（前两次：Snap 的往返、Residual 对着拟合样本）。
// 一般形式是：**任何只用自己参数做的自检，都检不出参数本身错了。**
//
// 唯一管用的办法只有一个：**去问浏览器**。上游的做法是 mousecalib.go 的 refineCalib
// (哨兵探针)；我方暂不搬,理由与代价见 doc.go。
//
// 顺带记一条能算的：真实错误量 ≈ scale 的相对误差 × 视口尺寸。
// 1.237189 的拟合误差 0.012% × 1205 = 0.14 CSS px（不到半像素，不跨边界）；
// 2.2 的误差 0.150% × 1205 = 1.80 px（超过一像素，到处跨）。
// 但**这个式子需要知道真值**，所以只能事后解释，不能事前自检。

// jsRound 复刻 ECMAScript 的 `Math.round`：**半数朝 +∞**（`floor(x+0.5)`）。
//
// Go 的 `math.Round` 是**半数远离零**，两者只在**负的半整数**处不同：
// `-0.5` → Go 给 −1、JS 给 −0。而我们要预测的是浏览器算出来的 `clientX`，
// 那是 JS 的口径，不是 Go 的。
//
// **这不是理论边角。** 实测 offset = 151.5（chrome 高度，§30）、scale = 1 时：
//
//	snap1(css=0)：唯一候选 v=151，(151−151.5) = −0.5
//	  Go  round(−0.5) = −1  ≠ 0  →  报「这个 CSS 坐标表达不出来」
//	  JS  floor(0)    =  0  = 0  →  浏览器其实算得出来
//
// 也就是说视口左上角那一格会被我们误判成不可达。2026-08-27 在 §35 四里
// 记过这条边，当时判断是「拟合 offset 带 .5 且**光标在视口外**才够得到」——
// **判断错了**，css=0 就在视口里。写用例时撞出来的。
func jsRound(x float64) float64 { return math.Floor(x + 0.5) }

func snap1(css, scale, offset float64) (int, bool) {
	base := int(math.Round(css*scale + offset))
	// 先试直接取整，不行再往两边各挪一格。缩放 ≥1 时第一次就中。
	// **这一行必须用 jsRound** —— 它预测的是浏览器会算出什么，不是我们想算什么。
	for _, d := range [...]int{0, 1, -1, 2, -2} {
		v := base + d
		if jsRound((float64(v)-offset)/scale) == css {
			return v, true
		}
	}
	return base, false
}

// Calibrate 用最小二乘从观测里解出映射。**至少两个 x 不同、两个 y 不同的样本。**
//
// 用最小二乘而不是「取两点解方程」，是因为观测本身带取整噪声（clientX 是整数）。
// 两点解方程会把噪声全吃进 scale；样本多几个就能平掉。
func Calibrate(ss []Sample) (Calib, error) {
	if len(ss) < 2 {
		return Calib{}, fmt.Errorf("标定至少要 2 个观测，只有 %d 个", len(ss))
	}
	sx, okx := fit1(ss, func(s Sample) (float64, float64) { return s.ClientX, s.ScreenX })
	sy, oky := fit1(ss, func(s Sample) (float64, float64) { return s.ClientY, s.ScreenY })
	if !okx || !oky {
		return Calib{}, fmt.Errorf("标定不可解：需要至少两个 %s 不同的观测点"+
			"（把鼠标只在一条直线上移动是不够的）", map[bool]string{true: "y", false: "x"}[okx])
	}
	c := Calib{ScaleX: sx.a, OffsetX: sx.b, ScaleY: sy.a, OffsetY: sy.b}
	return snapScale(c, ss), nil
}

// ScaleGapOK 报告 PlausibleScales 里任意两档的相对间距，是否都大于吸附容差的两倍。
//
// **这是给「往表里补档」这个念头准备的闸。** 遇到吸不住的缩放时，最顺手的修法是
// 把它补进表 —— 而那是错的：系统缩放 × 浏览器 zoom 的乘积集合里，最近的两对
// 相对间距只有 0.444%（2.800 vs 2.8125）和 0.568%，**小于吸附容差 0.5% 的两倍**。
// 补全之后一个带噪声的拟合会被吸到**相邻的错档**，而错档是个精确数字，
// 后面所有自检都会以为它对 —— 比不吸附更坏。
//
// 表里现在最近的一对是 0.75 vs 0.8，间距 6.7%，余量充足。
func ScaleGapOK(tol float64) (bool, float64, [2]float64) {
	worst, pair := math.Inf(1), [2]float64{}
	for i := 1; i < len(PlausibleScales); i++ {
		a, b := PlausibleScales[i-1], PlausibleScales[i]
		if g := (b - a) / a; g < worst {
			worst, pair = g, [2]float64{a, b}
		}
	}
	return worst > 2*tol, worst, pair
}

// PlausibleScales 是「网页坐标 → 系统坐标」在真实机器上可能的精确取值。
//
// 它们全都是**显示与缩放设置的产物**，不是连续量：Retina 是整数倍，
// Windows 的缩放档是 100/125/150/175/200%，浏览器 zoom 是固定档位。
var PlausibleScales = []float64{
	0.5, 0.667, 0.75, 0.8, 0.9, 1, 1.1, 1.25, 1.5, 1.75, 2, 2.5, 3,
}

// snapScale 把拟合出来的 scale 吸到最近的**物理上可能的精确值**，再用它重解 offset。
//
// # 为什么必须做
//
// 2026-08-25 真机实测：真实映射是 `clientY = screenY − 151` —— scale 恰好 1、
// offset 恰好是整数。而最小二乘在带 ±0.5px 取整噪声的观测上拟合出
// **ScaleY = 1.000038、OffsetY = 150.48**。
//
// 残差只有 0.433px，两条自检全过 —— 但那 0.000038 让映射值在 cy≈520 处
// **跨过取整边界**：121 个观测里 cy<500 的 93~100% 偏了 −1，cy>650 的 0% 偏。
//
// **噪声不该有资格发明一个分数。** scale 是物理量，取值是离散的。
// 先把它吸到最近的合法档，再重解 offset —— 那时 offset 也会落回整数附近。
//
// 容差 0.5%：足以吃掉观测噪声（1205px 量程上 ±0.5px ≈ 0.04%），
// 又远小于相邻档之间的距离（最近的一对是 1.0 与 1.1）。
func snapScale(c Calib, ss []Sample) Calib {
	fix := func(scale float64, pick func(Sample) (float64, float64)) (float64, float64) {
		best, bestD := scale, math.Inf(1)
		for _, p := range PlausibleScales {
			if d := math.Abs(scale-p) / p; d < bestD {
				best, bestD = p, d
			}
		}
		if bestD > 0.005 {
			return scale, math.NaN() // 离任何合法档都远 —— 保持原样，别硬掰
		}
		// 用吸附后的 scale 重解 offset：offset = mean(screen − client*scale)
		var sum, n float64
		for _, s := range ss {
			cl, sc := pick(s)
			sum += sc - cl*best
			n++
		}
		return best, sum / n
	}
	okX, okY := false, false
	if sx, ox := fix(c.ScaleX, func(s Sample) (float64, float64) { return s.ClientX, s.ScreenX }); !math.IsNaN(ox) {
		c.ScaleX, c.OffsetX, okX = sx, ox, true
	}
	if sy, oy := fix(c.ScaleY, func(s Sample) (float64, float64) { return s.ClientY, s.ScreenY }); !math.IsNaN(oy) {
		c.ScaleY, c.OffsetY, okY = sy, oy, true
	}
	c.Snapped = okX && okY
	return c
}

type line struct{ a, b float64 } // y = a*x + b

func fit1(ss []Sample, pick func(Sample) (float64, float64)) (line, bool) {
	var n, sx, sy, sxx, sxy float64
	for _, s := range ss {
		x, y := pick(s)
		n++
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	den := n*sxx - sx*sx
	if math.Abs(den) < 1e-9 {
		return line{}, false // 所有 x 相同 —— 解不出斜率
	}
	a := (n*sxy - sx*sy) / den
	if math.Abs(a) < 1e-9 || math.IsNaN(a) || math.IsInf(a, 0) {
		return line{}, false
	}
	return line{a: a, b: (sy - a*sx) / n}, true
}

// Residual 报告这份标定在给定观测上的最大误差（CSS 像素）。
//
// **这是「检查二」** —— 跟现实对账，抓的是「标定过期」和「标定算错」。
// 它跟 `Snap` 的往返（检查一，纯算术）是两回事：往返用同一组 scale/offset
// 自己跟自己对，标定错了它照样通过。
//
// 误差超过 1~2px 就不能动鼠标：不只是会点错元素（业务事故），
// 更硬的是 sec-370 在同一段里查坐标合理性 ——
// `clientX<=0` 点亮 700005、为负点亮 700007，那是会上报的检测码。
// 见上游 hiBoss report/runtime-evidence.md §30.2。
func (c Calib) Residual(ss []Sample) float64 {
	worst := 0.0
	for _, s := range ss {
		cx, cy := c.ToClient(s.ScreenX, s.ScreenY)
		worst = math.Max(worst, math.Max(math.Abs(cx-s.ClientX), math.Abs(cy-s.ClientY)))
	}
	return worst
}
