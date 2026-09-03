package handinput

import "fmt"

// W3C `KeyboardEvent.code` → Windows 虚拟键码(VK_*)。
//
// # 为什么这个文件没有 //go:build windows
//
// 它是**纯数据**,一行 Windows API 都不调,所以不需要平台标签。而不加标签是有意的:
// 加了的话这张表的门禁就只在 Windows 上跑,而 Windows 那台机器上我们已经裁定
// 「一次跑到底、不做前置校验」——键码表将第一次执行就在没有体检的现场。
// 不加标签,`go test ./...` 在开发机上每天都替它把关。
//
// 键码表错了的症状是"某个字母总是打成另一个",而那在真机上极难往表上想:
// 拼音差一个字母,上屏的是另一个词,看起来像候选词选错——正好与 TIP 的已知病症
// 撞脸。这是**能在 mac 上验的东西一定要在 mac 上验**的典型。
//
// (`keymap_darwin.go` 带 darwin 标签,因为开发机就是 darwin,它天天在跑。)
//
// # 枚举面
//
// 与 darwin 那张**同一个集合**,取自上游 `compose/pinyin.mjs`:26 个字母、10 个数字、
// `PUNCT_KEY` + `ASCII_KEY` 合起来的十一个 OEM 键、Space、ShiftLeft。按「平台枚举面
// 事实门」不收任何"以后可能用得上"的键:上游的 windowsKey 还有 Tab/Backspace/
// Escape/ShiftRight/CapsLock,**我们一个都不收**——排版器发不出它们。
//
// **Enter 刻意不收,但它是后手不是前门**,理由见 keymap_darwin.go 文件头:前门是插件
// 在排版前把换行删掉,这里不收只是为了万一前门漏了,方向仍是一个键不发。
//
// # 键码值的出处
//
// VK_A..VK_Z 与 VK_0..VK_9 就是对应字符的 ASCII 码(微软文档明写),所以**推出来**
// 而不是抄二十六行——抄才会写错,而推错了整族一起错、一测就现。
// 标点那八个是 OEM 键,值只能查文档:它们的**含义随键盘布局变**(VK_OEM_1 在美式
// 布局上是分号,在别的布局上是别的字符),但我们要的正是"物理上的那个键"——
// W3C code 的定义也是物理键位,两边同一口径。
var windowsKeyCodes = map[string]uint16{
	// 标点。全集来自 pinyin.mjs 的 PUNCT_KEY,顺序与 darwin 表一致便于对照。
	"Minus":        0xBD, // VK_OEM_MINUS
	"Equal":        0xBB, // VK_OEM_PLUS
	"BracketLeft":  0xDB, // VK_OEM_4
	"BracketRight": 0xDD, // VK_OEM_6
	"Quote":        0xDE, // VK_OEM_7
	"Semicolon":    0xBA, // VK_OEM_1
	"Backslash":    0xDC, // VK_OEM_5
	"Comma":        0xBC, // VK_OEM_COMMA
	"Slash":        0xBF, // VK_OEM_2
	"Period":       0xBE, // VK_OEM_PERIOD
	"Backquote":    0xC0, // VK_OEM_3

	"Space":     0x20, // VK_SPACE
	"ShiftLeft": 0xA0, // VK_LSHIFT —— 不是 VK_SHIFT(0x10)。后者是"哪边都行"的
	// 聚合码,SendInput 发它系统认不出是哪个物理键,扫描码也就映不出来。
	// 编辑键:清空输入框走 ctrl+A 加 Backspace(2026-09-03 裁决撤销 composer.empty)。
	// 刻意不收 MetaLeft:VK_LWIN 是扩展键,本表的扫描码路径没处理扩展标记,收了就是
	// 半对的键;Windows 上全选本来也不用它。排版器发了它会在校验期显式报错。
	"Backspace":   0x08, // VK_BACK
	"ControlLeft": 0xA2, // VK_LCONTROL —— 同 ShiftLeft 的理由,不用聚合码 VK_CONTROL
}

func init() {
	for c := byte('A'); c <= 'Z'; c++ {
		windowsKeyCodes["Key"+string(c)] = uint16(c)
	}
	for d := byte('0'); d <= '9'; d++ {
		windowsKeyCodes["Digit"+string(d)] = uint16(d)
	}
}

// windowsKeyCode 查表。**没有兜底**——表外的 code 显式报错,不猜、不静默降级。
func windowsKeyCode(code string) (uint16, error) {
	vk, ok := windowsKeyCodes[code]
	if !ok {
		return 0, fmt.Errorf("键码表里没有 %q —— 排版器发出了本表未覆盖的键,不猜", code)
	}
	return vk, nil
}
