package handinput

import "fmt"

// 排版器能发出的键位全集。**两张键码表(darwin / windows)共用这一份期望**——
// 各写一份的话,上游长出新键位时要改两处,而漏改的那一处会一直绿。
//
// 标点全集取自上游 `compose/pinyin.mjs` 的 `PUNCT_KEY`。插件侧有一条配套用例钉住
// 那张表的 code 集合恰好是这八个——**上游加了第九个,那边先红**,提醒回来补这里。
// 两处合起来才是完整的门禁:只有这一处的话,上游长出新键位时我们这边一片绿,
// 直到真机上打出错字。
var punctKeysFromUpstream = []string{
	"Comma", "Period", "Semicolon", "Slash",
	"Quote", "Backquote", "Backslash", "Minus",
}

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
	return want
}
