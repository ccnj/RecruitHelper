package handinput

import "testing"

// 这三条与 darwin 那三条是同一套判据,**但它们在开发机上真的跑**——
// 这正是 keymap_windows.go 不带 //go:build windows 的理由。
//
// Windows 那台机器上我们已裁定「一次跑到底、不做前置校验」,键码表错了的症状
// (某个字母总打成另一个 → 上屏另一个词)与 TIP 选错词撞脸,现场分不开。
// 所以凡是能在这边验的,一定在这边验完再上机。

func TestWindowsKeymapCoversWholeAlphabet(t *testing.T) {
	want := emittableKeyCodes()
	for code := range want {
		if _, err := windowsKeyCode(code); err != nil {
			t.Errorf("键码表漏了 %s:%v", code, err)
		}
	}
	if len(windowsKeyCodes) != len(want) {
		for code := range windowsKeyCodes {
			if !want[code] {
				t.Errorf("键码表多了 %s —— 没有生产者的键位不该进表(平台枚举面事实门)", code)
			}
		}
		t.Errorf("表 %d 项,应有 %d 项", len(windowsKeyCodes), len(want))
	}
}

func TestWindowsKeymapRefusesUnknown(t *testing.T) {
	// 上游的 windowsKey 收了这些,我们刻意不收:排版器发不出它们。
	// 表里多一个没有生产者的键,真出问题时会让人以为它验过。
	for _, code := range []string{"Enter", "Tab", "Backspace", "Escape", "Equal",
		"BracketLeft", "BracketRight", "ShiftRight", "CapsLock", "F1", "", "Digit10"} {
		if _, err := windowsKeyCode(code); err == nil {
			t.Errorf("%q 不在表里却没报错 —— 兜底会把打错字变成静默失败", code)
		}
	}
}

func TestWindowsKeymapValuesAreDistinct(t *testing.T) {
	seen := map[uint16]string{}
	for code, vk := range windowsKeyCodes {
		if prev, dup := seen[vk]; dup {
			t.Errorf("%s 与 %s 都映射到 %#x", code, prev, vk)
		}
		seen[vk] = code
	}
}

// 字母与数字的 VK 就是 ASCII 码,这是微软文档明写的。**独立核一遍**:
// keymap_windows.go 里那两个 for 循环与本条各自表述同一事实,写错一处就分叉。
func TestWindowsLetterAndDigitVKAreASCII(t *testing.T) {
	for _, c := range []struct {
		code string
		vk   uint16
	}{
		{"KeyA", 0x41}, {"KeyZ", 0x5A}, {"KeyN", 0x4E},
		{"Digit0", 0x30}, {"Digit9", 0x39}, {"Digit2", 0x32},
	} {
		got, err := windowsKeyCode(c.code)
		if err != nil {
			t.Fatalf("%s 查不到:%v", c.code, err)
		}
		if got != c.vk {
			t.Errorf("%s 应为 %#x,实得 %#x", c.code, c.vk, got)
		}
	}
}

// ShiftLeft 必须是 VK_LSHIFT(0xA0),不是 VK_SHIFT(0x10)。
//
// 后者是"哪边都行"的聚合码。SendInput 发它时系统认不出是哪个物理键,
// MapVirtualKey 也就映不出扫描码——而**输入法只认扫描码**,我们的 TIP 正是输入法。
// 症状:所有需要 Shift 的字元(全角标点)一个都上不了屏,而键序看起来完全正常。
func TestWindowsShiftIsLeftSpecific(t *testing.T) {
	vk, err := windowsKeyCode("ShiftLeft")
	if err != nil {
		t.Fatalf("ShiftLeft 查不到:%v", err)
	}
	if vk == 0x10 {
		t.Fatal("ShiftLeft 映到了 VK_SHIFT(0x10) —— 那是聚合码,映不出扫描码,输入法收不到")
	}
	if vk != 0xA0 {
		t.Errorf("ShiftLeft 应为 VK_LSHIFT(0xA0),实得 %#x", vk)
	}
}
