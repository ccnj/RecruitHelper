//go:build darwin

package handinput

// macOS 的注入实现,**只为开发机上的本地迭代存在,不进任何验收**。
//
// 上游 hiBoss 的同一份实现走 cgo,而本仓库明令禁止引入 cgo 依赖。这里改用 purego:
// 它是**运行期 FFI**——dlopen/dlsym 拿函数地址,再按平台 ABI 走手写跳板,
// 既不需要 C 工具链、也不需要 CGO_ENABLED=1。因此:
//
//   - `CGO_ENABLED=0` 照常构建,宪法那条禁令一个字不用改
//   - import 只出现在本文件(//go:build darwin),Windows 发布产物零影响
//
// 代价两条,如实记着:多一个依赖(purego 自身零外部依赖);它靠 //go:linkname 借
// runtime 内部(runtime.cgocall 等),**升 Go 版本时要看一眼**。爆炸半径有界——
// 真炸也只炸 darwin 这条开发路径,Windows 发布路径根本不编译本文件。
//
// # 为什么 Mac 上绿了不算数
//
// scale 恰好为 1 是所有档位里最容易的一档:上游在 Windows 上那一趟照出两个 bug,
// 两个在 macOS 上都完全不可见(单位相同、数值相同)。凡是碰坐标换算的,
// **Mac 上绿了不算数**——本文件的存在只为让人能边写边跑,验收判据始终是 Windows。

import (
	"fmt"
	"sync"

	"github.com/ebitengine/purego"
)

// CoreGraphics 的常量。名字保留 C 侧的写法,方便对着苹果文档核。
const (
	cgEventSourceStateHIDSystemState = 1 // 事件进入与真实硬件同一条队列
	cgHIDEventTap                    = 0

	cgEventLeftMouseDown  = 1
	cgEventLeftMouseUp    = 2
	cgEventRightMouseDown = 3
	cgEventRightMouseUp   = 4
	cgEventMouseMoved     = 5 // 不是 Dragged —— 我们不在拖拽
	cgEventOtherMouseDown = 25
	cgEventOtherMouseUp   = 26

	cgMouseButtonLeft   = 0
	cgMouseButtonRight  = 1
	cgMouseButtonCenter = 2
)

// cgPoint 与 CGPoint 二进制等价(两个 float64)。arm64 上按 HFA 走浮点寄存器传参,
// purego 直接支持结构体传值与结构体返回。
type cgPoint struct{ X, Y float64 }

var (
	cgOnce sync.Once
	cgErr  error

	cgEventSourceCreate        func(int32) uintptr
	cgEventCreate              func(uintptr) uintptr
	cgEventCreateMouseEvent    func(src uintptr, typ int32, pos cgPoint, button int32) uintptr
	cgEventCreateKeyboardEvent func(src uintptr, vk uint16, down bool) uintptr
	cgEventGetLocation         func(uintptr) cgPoint
	cgEventPost                func(tap int32, event uintptr)
	cfRelease                  func(uintptr)
	axIsProcessTrusted         func() bool
)

func loadCoreGraphics() {
	cg, err := purego.Dlopen(
		"/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics",
		purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		cgErr = fmt.Errorf("打不开 CoreGraphics:%w", err)
		return
	}
	as, err := purego.Dlopen(
		"/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices",
		purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		cgErr = fmt.Errorf("打不开 ApplicationServices:%w", err)
		return
	}
	purego.RegisterLibFunc(&cgEventSourceCreate, cg, "CGEventSourceCreate")
	purego.RegisterLibFunc(&cgEventCreate, cg, "CGEventCreate")
	purego.RegisterLibFunc(&cgEventCreateMouseEvent, cg, "CGEventCreateMouseEvent")
	purego.RegisterLibFunc(&cgEventCreateKeyboardEvent, cg, "CGEventCreateKeyboardEvent")
	purego.RegisterLibFunc(&cgEventGetLocation, cg, "CGEventGetLocation")
	purego.RegisterLibFunc(&cgEventPost, cg, "CGEventPost")
	purego.RegisterLibFunc(&cfRelease, cg, "CFRelease")
	purego.RegisterLibFunc(&axIsProcessTrusted, as, "AXIsProcessTrusted")
}

type darwinInjector struct{ src uintptr }

// NewInjector 造一个本平台的注入器。
//
// **未授权时不返回错误**:AXIsProcessTrusted 只是一次性的环境事实,而真正的失效
// 方式是 CGEventPost 静默丢弃事件——所以授权状态由 Platform() 带出来给人看,
// 让"程序说发完了、屏幕上什么也没发生"这件事至少有个可查的说法。
func NewInjector() (Injector, error) {
	cgOnce.Do(loadCoreGraphics)
	if cgErr != nil {
		return nil, cgErr
	}
	src := cgEventSourceCreate(cgEventSourceStateHIDSystemState)
	if src == 0 {
		return nil, fmt.Errorf("CGEventSourceCreate 返回空")
	}
	return &darwinInjector{src: src}, nil
}

func (d *darwinInjector) MouseMove(x, y float64) error {
	return d.post(cgEventMouseMoved, cgPoint{X: x, Y: y}, cgMouseButtonLeft)
}

func (d *darwinInjector) MouseDown(button int) error { return d.button(button, true) }
func (d *darwinInjector) MouseUp(button int) error   { return d.button(button, false) }

func (d *darwinInjector) button(button int, down bool) error {
	var typ, btn int32
	switch button {
	case MouseMiddle:
		btn = cgMouseButtonCenter
		typ = pick(down, cgEventOtherMouseDown, cgEventOtherMouseUp)
	case MouseRight:
		btn = cgMouseButtonRight
		typ = pick(down, cgEventRightMouseDown, cgEventRightMouseUp)
	case MouseLeft:
		btn = cgMouseButtonLeft
		typ = pick(down, cgEventLeftMouseDown, cgEventLeftMouseUp)
	default:
		return fmt.Errorf("未知鼠标键 %d", button)
	}
	// **按下/抬起必须发在光标现在所在的点上**:CGEventCreateMouseEvent 的坐标参数
	// 会顺带把光标挪过去,传错就等于在点击之前多一次瞬移。
	x, y, err := d.CursorPos()
	if err != nil {
		return err
	}
	return d.post(typ, cgPoint{X: float64(x), Y: float64(y)}, btn)
}

func (d *darwinInjector) KnowsKey(code string) error {
	_, err := darwinKeyCode(code)
	return err
}

func (d *darwinInjector) KeyDown(code string) error { return d.key(code, true) }
func (d *darwinInjector) KeyUp(code string) error   { return d.key(code, false) }

// key 发一次按键。
//
// **发在 HID 层(与鼠标同一个 tap)是这条路走得通的全部理由**:那是最底下一层,
// 等同于硬件产生的事件,系统输入法坐在它上面,一定会收到。发在 kCGSessionEventTap
// 之类更高的层上,输入法就不一定看得见了。
//
// macOS 上没有自研 TIP,走的是**系统输入法**——所以上屏哪个词不由我们说了算,
// 「聊聊」可能出成「了了」。这在开发机上是接受的:整条链路里除 TIP 之外的每一环
// 都能验,而上屏词对不对由调用方回读输入框自行判断,本层不做任何补救。
func (d *darwinInjector) key(code string, down bool) error {
	vk, err := darwinKeyCode(code)
	if err != nil {
		return err
	}
	e := cgEventCreateKeyboardEvent(d.src, vk, down)
	if e == 0 {
		return fmt.Errorf("CGEventCreateKeyboardEvent 返回空(code=%s vk=%#x down=%v)", code, vk, down)
	}
	cgEventPost(cgHIDEventTap, e)
	cfRelease(e)
	return nil
}

func (d *darwinInjector) post(typ int32, p cgPoint, button int32) error {
	e := cgEventCreateMouseEvent(d.src, typ, p, button)
	if e == 0 {
		return fmt.Errorf("CGEventCreateMouseEvent 返回空(type=%d)", typ)
	}
	cgEventPost(cgHIDEventTap, e)
	cfRelease(e)
	return nil
}

func (d *darwinInjector) CursorPos() (int, int, error) {
	e := cgEventCreate(0)
	if e == 0 {
		return 0, 0, fmt.Errorf("CGEventCreate 返回空")
	}
	p := cgEventGetLocation(e)
	cfRelease(e)
	return int(p.X), int(p.Y), nil
}

// SeedCalib:**scale = 1,offset 不乘 dpr**。
//
// 2026-08-28 真机两点实测(副屏,1470x956 @2x,页面缩放 100%):
//
//	ScaleX = 1.000000   OffsetX = 2560.000   ← 精确等于 window.screenX,差 0.0
//	ScaleY = 1.000000   OffsetY =  638.000   ← screenY(517) + 121(浏览器顶部高度)
//
// CGEventPost 收的是**全局显示坐标,单位 point**,而 window.screenX 报的已经就是
// point —— 乘 devicePixelRatio 是多乘一遍。此前那份公式是照 Windows 抄的,
// 在副屏上(screenX=2560)会把视口正中算到 x=6590,超出整个桌面右边界 2560 点。
//
// y 轴刻意仍用裸 screenY,差那 121 点(浏览器标签栏+地址栏+书签栏)交给搭车标定去学:
// 它小到光标还落在页面上,所以观测得到、学得回来。用 outerHeight-innerHeight 去猜
// 反而更差 —— 同一趟实测那个差是 177,多算了 56。
//
// 页面缩放不是 100% 时 scale 也不是 1,但那正是标定要学的东西:粗估只要让光标
// **落在页面上**就够了。
func (d *darwinInjector) SeedCalib(h WindowHint) Calib {
	return Calib{ScaleX: 1, ScaleY: 1, OffsetX: h.ScreenX, OffsetY: h.ScreenY}
}

func (d *darwinInjector) Close() {
	if d.src != 0 {
		cfRelease(d.src)
		d.src = 0
	}
}

func (d *darwinInjector) Authorized() bool { return axIsProcessTrusted() }

func (d *darwinInjector) Platform() string {
	auth := "已授权"
	if !axIsProcessTrusted() {
		// 未授权时 CGEventPost 不报错、事件被系统静默丢弃。
		auth = "未授权(要授权的是**启动本进程的那个应用**,不是编出来的二进制:" +
			"macOS 按父进程给权限,而 go test / go run 每次编到一个新临时路径,单独授权它没用)"
	}
	return "darwin/CGEventPost 开发机专用 " + auth
}

func pick(cond bool, a, b int32) int32 {
	if cond {
		return a
	}
	return b
}
