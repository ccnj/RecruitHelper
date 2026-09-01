//go:build windows

package handinput

// SendInput 鼠标注入。改写自上游 hiBoss `lab/engine/inject/input_windows.go`,
// 只保留鼠标部分,两条实战注意事项原样保留:
//
//  1. **DPI 感知必须在任何坐标操作之前声明**(写在 init 里)。不声明的话 Windows 会
//     对本进程「撒谎」:GetCursorPos 返回虚拟化坐标、SendInput 被偷偷缩放,症状是
//     「坐标算得对但点下去偏」,且偏移量随缩放率变化——一旦踩上会先怀疑坐标换算,
//     最后才想到 DPI。
//
//  2. **INPUT 的 x64 布局**:union 按最大成员 MOUSEINPUT(32B) 计,加上前 4 字节
//     type + 4 字节 padding,sizeof(INPUT) = 40。结构体不对齐的话 SendInput 的
//     cbSize 校验不过、**静默返回 0**。
//
// 纯 Go(syscall.NewLazyDLL),不引入 cgo。

import (
	"fmt"
	"math"
	"syscall"
	"unsafe"
)

var (
	user32                            = syscall.NewLazyDLL("user32.dll")
	procSendInput                     = user32.NewProc("SendInput")
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	procGetCursorPos                  = user32.NewProc("GetCursorPos")
)

const (
	dpiPerMonitorAwareV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
	inputMouse           = 0

	// ABSOLUTE|VIRTUALDESK 一起用才是「整个虚拟桌面的绝对坐标」;
	// 只给 ABSOLUTE 的话坐标是相对**主屏**的,多屏下第二块屏永远够不着。
	mouseeventfMove        = 0x0001
	mouseeventfAbsolute    = 0x8000
	mouseeventfVirtualDesk = 0x4000
	mouseeventfLeftDown    = 0x0002
	mouseeventfLeftUp      = 0x0004
	mouseeventfRightDown   = 0x0008
	mouseeventfRightUp     = 0x0010
	mouseeventfMiddleDown  = 0x0020
	mouseeventfMiddleUp    = 0x0040

	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79
)

// MOUSEINPUT 自身就是 union 里最大的那个(32B),所以 winMouseInput 不用补齐。
type mouseInput struct {
	dx          int32
	dy          int32
	mouseData   uint32
	dwFlags     uint32
	time        uint32
	_           uint32 // x64 下 dwExtraInfo 要 8 字节对齐
	dwExtraInfo uintptr
}

type winMouseInput struct {
	typ uint32
	_   uint32 // x64 padding
	mi  mouseInput
}

type winPoint struct{ X, Y int32 }

func init() { procSetProcessDpiAwarenessContext.Call(dpiPerMonitorAwareV2) }

type windowsInjector struct{}

// NewInjector 造一个本平台的注入器。
func NewInjector() (Injector, error) { return &windowsInjector{}, nil }

func (w *windowsInjector) MouseMove(x, y float64) error {
	vx, vy := metric(smXVirtualScreen), metric(smYVirtualScreen)
	cx, cy := metric(smCXVirtualScreen), metric(smCYVirtualScreen)
	if cx <= 1 || cy <= 1 {
		return fmt.Errorf("虚拟桌面尺寸异常 %dx%d —— 多半是 DPI 感知没声明成功", cx, cy)
	}
	// 微软文档的口径:归一化坐标覆盖 [0, 65535],映射到 [虚拟桌面起点, 起点+尺寸-1]。
	nx := int32(math.Round((x - float64(vx)) * 65535 / float64(cx-1)))
	ny := int32(math.Round((y - float64(vy)) * 65535 / float64(cy-1)))
	return sendMouse(mouseeventfMove|mouseeventfAbsolute|mouseeventfVirtualDesk, nx, ny)
}

func (w *windowsInjector) MouseDown(button int) error { return w.button(button, true) }
func (w *windowsInjector) MouseUp(button int) error   { return w.button(button, false) }

// 键盘注入在 Windows 上**尚未实现**(键盘线第二段)。
//
// 这里显式报错而不是发一个"差不多"的 SendInput:Windows 那半要配自研 TIP,
// 由命名管道告诉它上屏哪个词;没有 TIP 的键盘注入在中文下会打出错字——那是死代码里
// 更坏的一种,能编译、能跑、结果是错的。
func (w *windowsInjector) KnowsKey(code string) error { return errKeyboardNotOnWindows(code) }
func (w *windowsInjector) KeyDown(code string) error  { return errKeyboardNotOnWindows(code) }
func (w *windowsInjector) KeyUp(code string) error    { return errKeyboardNotOnWindows(code) }

func errKeyboardNotOnWindows(code string) error {
	return fmt.Errorf("键盘注入尚未在 Windows 上实现(键盘线第二段:自研 TIP + 命名管道);code=%s", code)
}

func (w *windowsInjector) button(button int, down bool) error {
	var f uint32
	switch button {
	case MouseLeft:
		f = map[bool]uint32{true: mouseeventfLeftDown, false: mouseeventfLeftUp}[down]
	case MouseMiddle:
		f = map[bool]uint32{true: mouseeventfMiddleDown, false: mouseeventfMiddleUp}[down]
	case MouseRight:
		f = map[bool]uint32{true: mouseeventfRightDown, false: mouseeventfRightUp}[down]
	default:
		return fmt.Errorf("未知鼠标键 %d", button)
	}
	// 按下/抬起**不带坐标** —— 不给 MOUSEEVENTF_MOVE,系统就发在光标当前位置。
	// 顺手带坐标会多产生一次移动事件,网页那边就多一个轨迹点。
	return sendMouse(f, 0, 0)
}

func (w *windowsInjector) CursorPos() (int, int, error) {
	var p winPoint
	r, _, err := procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	if r == 0 {
		return 0, 0, fmt.Errorf("GetCursorPos 失败:%v", err)
	}
	return int(p.X), int(p.Y), nil
}

// SeedCalib:**offset 要乘 dpr**,因为 SendInput 收的是虚拟桌面物理像素。
//
// 上游 hiBoss 的 Windows 真机实测:150% 缩放下页面报 screenX=430(CSS),真值 offsetX
// 是 656.92(物理);不乘的话种子偏 227 物理像素,乘了只剩 12(窗口边框)。
//
// **这条只对 Windows 成立。** macOS 的 CGEventPost 收 point,同一份公式在那边会多乘
// 一遍 —— 2026-08-28 就是这么在副屏上把光标算到桌面外去的。差异的根源是各平台注入
// API 收什么单位,所以这个翻译归各自的注入器,不归共享的标定层。
func (w *windowsInjector) SeedCalib(h WindowHint) Calib {
	s := h.DPR
	if s <= 0 {
		s = 1
	}
	return Calib{ScaleX: s, ScaleY: s, OffsetX: h.ScreenX * s, OffsetY: h.ScreenY * s}
}

func (w *windowsInjector) Close()           {}
func (w *windowsInjector) Platform() string { return "windows/SendInput" }

func sendMouse(flags uint32, dx, dy int32) error {
	in := winMouseInput{typ: inputMouse, mi: mouseInput{dx: dx, dy: dy, dwFlags: flags}}
	n, _, err := procSendInput.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
	if n != 1 {
		// UIPI:目标窗口完整性级别比我们高时,SendInput **静默失败**,返回 0 而不报错。
		// 上游标注这一条「只有注释里的断言,零实测」——真机首验时要专门确认。
		return fmt.Errorf("SendInput 只发出 %d 个事件(%v) —— 若目标是以管理员身份运行的"+
			"浏览器,本进程也必须提权(UIPI)", n, err)
	}
	return nil
}

func metric(i int) int {
	v, _, _ := procGetSystemMetrics.Call(uintptr(i))
	return int(int32(v))
}
