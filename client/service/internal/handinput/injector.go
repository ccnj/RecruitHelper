package handinput

import "fmt"

// Injector 是真实系统输入的出口。改写自上游 hiBoss `lab/engine/inject/injector.go`。
//
// **本轮只有鼠标。** 键盘随打字那半一并后置:上游的打字链要靠自研 TIP 决定上屏
// 哪个词(命名管道驱动),没有 TIP 的键盘注入在中文下会打出错字,那是死代码里更坏
// 的一种——能编译、能跑、结果是错的。
type Injector interface {
	// MouseMove 把光标移到**该平台注入 API 自己的那个坐标系**里的一点。
	//
	// 单位刻意不统一:macOS 的 CGEventPost 收 point,Windows 的 SendInput 收
	// 归一化到 0..65535 的虚拟桌面坐标。**上层不需要知道是哪个**——`Calib` 是
	// 端到端观测拟合出来的,会把单位差异一起吸收(见 coord.go)。
	//
	// 绝对坐标,不是相对位移。相对位移会被系统的「指针加速度」改写,
	// 那等于把一个我们控制不了的非线性函数插进轨迹里。
	MouseMove(x, y float64) error
	MouseDown(button int) error
	MouseUp(button int) error

	// CursorPos 读光标当前所在的系统坐标。
	//
	// **编排层每次生成计划前都要现读它**,不许用记忆里的 CSS 值:Snap 与 ToClient
	// 用同一份标定、互为逆运算,所以现读再反算必然落回原地,不管标定多离谱;
	// 而记忆值没有这个抵消,标定一被修正就错位,错位量正好等于修正量(见 coord.go)。
	CursorPos() (int, int, error)

	// SeedCalib 把页面自报的窗口粗估翻成一个初始映射。
	//
	// **必须由各平台自己实现,因为差异的根源就是"本平台的注入 API 收什么单位"** ——
	// 而那正是注入器自己的知识,不该由共享的标定层去猜。
	//
	// 2026-08-28 真机实测:此前只有一份照 Windows 抄的公式(offset = screenX × dpr),
	// 在 macOS 上多乘了一遍——副屏(screenX=2560)下种子把光标算到桌面外 2560 点,
	// 被系统钳死在边角、压根不在页面上,于是观测不到落点、学不到东西,重试全成瞎扫。
	//
	// 粗估只要**落在页面上**就够了:剩下的误差由搭车标定从落点学回来。
	SeedCalib(hint WindowHint) Calib

	Platform() string
	Close()
}

// 鼠标键。跟 W3C 的 MouseEvent.button 对齐,省得中间再翻译一次。
const (
	MouseLeft   = 0
	MouseMiddle = 1
	MouseRight  = 2
)

// ErrInjectorUnsupported 是本平台没有注入实现。
//
// 非 Windows 上必然是这个:上游的 macOS 实现走 CGEventPost,而那需要 cgo,
// 本仓库明令禁止引入 cgo 依赖(AGENTS.md)。后果是**端到端冒烟只能在 Windows 上跑**,
// 开发机上只能跑到"生成计划"为止。
var ErrInjectorUnsupported = fmt.Errorf("本平台没有键鼠注入实现")
