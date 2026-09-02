//go:build darwin

package handinput

import (
	"testing"
)

// 期望集合在 keymap_expect_test.go,与 windows 那张表共用一份。

func TestDarwinKeymapCoversWholeAlphabet(t *testing.T) {
	want := emittableKeyCodes()

	for code := range want {
		if _, err := darwinKeyCode(code); err != nil {
			t.Errorf("键码表漏了 %s:%v", code, err)
		}
	}
	if len(darwinKeyCodes) != len(want) {
		for code := range darwinKeyCodes {
			if !want[code] {
				t.Errorf("键码表多了 %s —— 没有生产者的键位不该进表(平台枚举面事实门)", code)
			}
		}
		t.Errorf("表 %d 项,应有 %d 项", len(darwinKeyCodes), len(want))
	}
}

func TestDarwinKeymapRefusesUnknown(t *testing.T) {
	// 没有兜底是刻意的。上游在字元→键位那张表上吃过这个亏:一稿写了默认值,
	// 于是没收录的字元被静默打成「,」,排版 ok=true、退出码 0,真机上却打错字。
	for _, code := range []string{"KeyÄ", "F1", "", "Digit10", "ShiftRight"} {
		if _, err := darwinKeyCode(code); err == nil {
			t.Errorf("%q 不在表里却没报错 —— 兜底会把打错字变成静默失败", code)
		}
	}
}

func TestDarwinKeymapValuesAreDistinct(t *testing.T) {
	// kVK 不是字母序(A=0、S=1、D=2),抄的时候极易把两个键写成同一个号,
	// 而那种错在真机上表现为"某个字母总是打成另一个",很难往表上想。
	seen := map[uint16]string{}
	for code, vk := range darwinKeyCodes {
		if prev, dup := seen[vk]; dup {
			t.Errorf("%s 与 %s 都映射到 %#x", code, prev, vk)
		}
		seen[vk] = code
	}
}

// 排版器能发出、我们刻意不收的键,必须被拒——而且要一直被拒,直到单独立案放行。
func TestDarwinKeymapRefusesOnPurpose(t *testing.T) {
	for _, code := range refusedOnPurpose {
		if _, err := darwinKeyCode(code); err == nil {
			t.Errorf("%s 被刻意排除在键码表外(裸 Enter 会发送),不该查得到", code)
		}
	}
}
