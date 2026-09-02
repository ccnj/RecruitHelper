//go:build darwin

package handinput

import "fmt"

// W3C `KeyboardEvent.code` → macOS 的 CGKeyCode(虚拟键码)。
//
// # 这张表覆盖的是排版器**能发出**的全集,不是采样
//
// 枚举面取自上游 `compose/pinyin.mjs` 的两处权威来源,不是跑几条文案看看出现了什么:
//
//   - 拼音字母 KeyA..KeyZ —— 26 个,一个不少(采样只会见到二十几个)
//   - 数字 Digit0..Digit9 —— 既作字面数字,也作上屏键(约 14% 的段落按数字选第 N 个候选)
//   - `PUNCT_KEY`(全角,走组字)与 `ASCII_KEY`(半角,透传)两张表合起来的十一个
//     OEM 键:Comma Period Semicolon Slash Quote Backquote Backslash Minus
//     Equal BracketLeft BracketRight
//   - Space(上屏/半角空格)与 ShiftLeft(修饰)
//
// **Enter 刻意不收**。排版器自 2026-09-01 起能为换行段发出 Shift+Enter,但裸 Enter
// 在聊天框里是**发送**:Shift 时序稍偏一点(shiftGuard 那类问题)就把半截话发给
// 候选人——这是整条线最怕的失败模式,而 debug.osType 的承诺是「停在草稿、不发送」。
// 换行段因此在 Validate 阶段就被拒(键码表里没有 Enter),一个键都不发。
// 要放行必须单独立案,连同「Enter 泄漏」的护栏一起。
//
// 按「平台枚举面事实门」,这里**不收**任何"以后可能用得上"的键:没有生产者的键位
// 进了表,只会在真出问题时让人以为它验过。表外的 code 一律显式报错。
//
// # 键码值的出处
//
// Carbon `HIToolbox/Events.h` 的 `kVK_ANSI_*` 常量。它们是**物理键位**,与当前
// 键盘布局无关——这正是我们要的:计划里的 `code` 也是物理键位(W3C code 的定义
// 就是"这个键在 ANSI 布局上是哪个"),两边同一口径,中间不需要翻译布局。
//
// **注意它们不是 ASCII 也不是字母序**:A=0、S=1、D=2 是 Apple 早年键盘的扫描顺序。
// 抄的时候别"顺手补齐"看起来缺的号,那些号属于别的键。
var darwinKeyCodes = map[string]uint16{
	// 字母。顺序按 kVK 值排,便于与 Events.h 逐行对照。
	"KeyA": 0x00, "KeyS": 0x01, "KeyD": 0x02, "KeyF": 0x03, "KeyH": 0x04,
	"KeyG": 0x05, "KeyZ": 0x06, "KeyX": 0x07, "KeyC": 0x08, "KeyV": 0x09,
	"KeyB": 0x0B, "KeyQ": 0x0C, "KeyW": 0x0D, "KeyE": 0x0E, "KeyR": 0x0F,
	"KeyY": 0x10, "KeyT": 0x11, "KeyO": 0x1F, "KeyU": 0x20, "KeyI": 0x22,
	"KeyP": 0x23, "KeyL": 0x25, "KeyJ": 0x26, "KeyK": 0x28, "KeyN": 0x2D,
	"KeyM": 0x2E,

	// 数字。**0x16 是 6、0x17 是 5**,不是顺序排的。
	"Digit1": 0x12, "Digit2": 0x13, "Digit3": 0x14, "Digit4": 0x15,
	"Digit6": 0x16, "Digit5": 0x17, "Digit9": 0x19, "Digit7": 0x1A,
	"Digit8": 0x1C, "Digit0": 0x1D,

	// 标点。全集来自 pinyin.mjs 的 PUNCT_KEY(全角,走组字)与 ASCII_KEY(半角,透传,
	// 上游 2026-09-01 放行)。后者多出三个键位:Equal、BracketLeft、BracketRight。
	"Minus":        0x1B,
	"Equal":        0x18,
	"BracketRight": 0x1E,
	"BracketLeft":  0x21,
	"Quote":        0x27,
	"Semicolon":    0x29,
	"Backslash":    0x2A,
	"Comma":        0x2B,
	"Slash":        0x2C,
	"Period":       0x2F,
	"Backquote":    0x32,

	"Space":     0x31,
	"ShiftLeft": 0x38,
}

// darwinKeyCode 查表。**没有兜底**——表外的 code 显式报错,不猜、不静默降级。
func darwinKeyCode(code string) (uint16, error) {
	vk, ok := darwinKeyCodes[code]
	if !ok {
		return 0, fmt.Errorf("键码表里没有 %q —— 排版器发出了本表未覆盖的键,不猜", code)
	}
	return vk, nil
}
