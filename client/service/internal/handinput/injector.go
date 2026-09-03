package handinput

import (
	"fmt"
	"time"
)

// Injector 是真实系统输入的出口。改写自上游 hiBoss `lab/engine/inject/injector.go`。
//
// 鼠标与键盘两半。键盘那半在 Windows 上要配自研 TIP(命名管道告诉它上屏哪个词),
// 在 macOS 开发机上退回系统输入法——上屏词不可控,但整条链路能跑通,而链路里
// 除 TIP 之外的每一环(排版、时序、焦点、与鼠标线的衔接)都是平台无关的。
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

	// KeyDown / KeyUp 按 W3C `KeyboardEvent.code`(计划里就是这个)注入一次按键。
	//
	// **键码映射由各平台自己做**,理由与 SeedCalib 同款:差异的根源是"本平台的
	// 注入 API 收什么键码"——macOS 收 CGKeyCode、Windows 收虚拟键码,而那正是
	// 注入器自己的知识,不该由共享层去猜。
	//
	// **认识就发,不认识就报错,没有兜底。** 上游在字元→键位那张表上吃过这个亏:
	// 一稿写了 `?? { code: 'Comma' }`,于是任何没收录的字元被静默打成「,」,
	// 排版返回 ok=true、退出码 0,真机上却打错字——错误被完全掩盖,连查都没处查。
	//
	// 修饰键(ShiftLeft)在计划里是**普通键**,时序由排版器排好并受其约束校验,
	// 注入层不自行推算按住/松开——只有排版器有全局视野知道前后键在哪。
	KeyDown(code string) error
	KeyUp(code string) error

	// KnowsKey 只回答"这个 code 我认不认识",不发任何东西。
	//
	// 有它才能**在发出第一次按键之前**把整份计划否掉。没有的话只能边发边试,
	// 而打到一半失败会留下按住的修饰键——那是最坏的收场:失败之后用户的键盘
	// 还带着 Shift。方向必须是"一个都没发",不是"发了一半"。
	KnowsKey(code string) error

	// Wheel 在光标**当前位置**注入一次滚轮滚动,notches 是刻度数。
	//
	// 符号按 W3C `WheelEvent.deltaY` 口径:**正=内容向下(朝文档末尾),负=向上**——
	// 插件读 DOM 就是这个口径,不让它再翻译一次。各平台注入 API 的符号与之相反
	// (Windows 的 mouseData 正=滚轮向前=向上;macOS 的 wheel1 按常见用法同样正=向上),
	// 翻译归各自的注入器,理由与 SeedCalib、键码映射同款:差异的根源是"本平台的注入
	// API 收什么",那是注入器自己的知识,不该由共享层去猜。
	//
	// 一次调用一个事件。真人的滚轮一格一个 HID 报告,几格一簇、簇内什么间隔由排版器
	// (插件)排、本包照播;这里不把几格并成一个事件,并了就不是真人的形状。
	Wheel(notches int) error

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
	// Authorized 回答"操作系统会不会真的把我们发的事件投出去"。macOS 看辅助功能
	// 授权(AXIsProcessTrusted),没授权时 CGEventPost 静默丢弃、屏幕上什么都不发生;
	// Windows 的 SendInput 不需要授权。它和 Platform() 一样是环境事实,由 /state 带给
	// 插件,让插件在移动之前就能拒绝并说清要授权哪个应用——2026-09-03 Mac 首跑就是
	// 客户端拉起的脑没授权,整轮"没观测到 mousemove",查了一下午。
	Authorized() bool
	Close()
}

// wordDriver 是**可选**能力:本平台能不能决定"这一串按键上屏成哪些词"。
//
// Windows 实现它(自研 TSF 输入法 + 命名管道);macOS 不实现,那不是缺口而是
// 如实的答案——开发机走系统输入法,上屏词由它挑,我方不可控。段一真机里
// 「加个」出成「价格」就是这么来的,是预期行为。
//
// **写成可选接口而不是 Injector 的方法,是为了让"没有"说得出口。** 给 darwin 加
// 一个空实现的话,读代码的人分不清"这平台不需要"和"这平台还没做"——而这两件事
// 在真机现场的处置完全相反(前者照打,后者不许打)。
type wordDriver interface {
	// DriveWords 让本平台的上屏机制接管这一轮的词。
	//
	// 必须在**焦点已经落在目标窗口之后**调用:实现会按前台窗口挑对象,
	// 而按键也只落在前台窗口,两者得指同一个。
	DriveWords(words []PlanWord, wait time.Duration) (WordSession, error)
}

// WordSession 是一轮打字期间的上屏会话。
type WordSession interface {
	// Settle 等这一轮的回报送完,返回一句对账结论(上屏了几个词)。
	// 超时就照实说,**不假装**——不可信的结论比没有结论更糟。
	Settle(timeout time.Duration) string
	// Close 收尾,让上屏机制回到透传。
	//
	// **调用方必须 defer 它。** 留在受驱动状态的输入法会把我方的词表用在
	// 真人自己敲的字上:招聘人员随手打一句,屏幕上出来的是我们的消息内容。
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
