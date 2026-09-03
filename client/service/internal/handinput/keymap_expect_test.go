package handinput

import "fmt"

// 排版器能发出的键位全集。**两张键码表(darwin / windows)共用这一份期望**——
// 各写一份的话,上游长出新键位时要改两处,而漏改的那一处会一直绿。
//
// 标点全集取自上游 `compose/pinyin.mjs` 的 `PUNCT_KEY` 与 `ASCII_KEY`。插件侧有一条
// 配套用例把两张表能发出的 code 全集钉死——**上游再长出新键位,那边先红**,提醒回来补这里。
// 两处合起来才是完整的门禁:只有这一处的话,上游长出新键位时我们这边一片绿,
// 直到真机上打出错字。
var punctKeysFromUpstream = []string{
	"Comma", "Period", "Semicolon", "Slash",
	"Quote", "Backquote", "Backslash", "Minus",
	// 上游 2026-09-01 放行半角标点(ASCII_KEY,透传)多出的三个。
	"Equal", "BracketLeft", "BracketRight",
}

// 排版器**能发出、但我们刻意不收**的键。Validate 阶段拒绝、一个键都不发。
//
// Enter:换行段是 Shift+Enter,而裸 Enter 在聊天框里是**发送**,Shift 松早了就把
// 半截话发给候选人。**这是后手不是前门**:插件 boss.osType 在排版前把换行删掉
// (甲方 2026-09-02 裁决),正常路径碰不到这里;留着是为了万一前门漏了。
var refusedOnPurpose = []string{"Enter"}

// 清空输入框的裸按键序列(插件 osinput.composeClearKeys,2026-09-03 裁决撤销 composer.empty)
// 用到的编辑键:全选的修饰键与 Backspace。两张表都收 ControlLeft;darwin 的全选是 cmd+A,
// MetaLeft 只进 darwin 表(见 keymap_darwin_test.go),windows 表刻意不收它(VK_LWIN 是扩展键)。
var editingKeysFromClearSequence = []string{"Backspace", "ControlLeft"}

// emittableKeyCodes 把全集**推出来**,不是从任何一张表里抄——抄一遍就成了
// 自己跟自己比,表里少一个字母、期望里也少一个,测试照样绿。
func emittableKeyCodes() map[string]bool {
	want := map[string]bool{"Space": true, "ShiftLeft": true}
	for c := 'A'; c <= 'Z'; c++ {
		want[fmt.Sprintf("Key%c", c)] = true
	}
	for d := 0; d <= 9; d++ {
		want[fmt.Sprintf("Digit%d", d)] = true
	}
	for _, p := range punctKeysFromUpstream {
		want[p] = true
	}
	for _, e := range editingKeysFromClearSequence {
		want[e] = true
	}
	return want
}
