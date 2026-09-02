package handinput

import (
	"fmt"
	"strconv"
	"strings"
)

// 透传词的文本上线前做可读转义。**只对透传词做**:TIP 从不打透传词的文本(它只推进
// 词序号,字是键盘布局自己打出来的),这段文本唯一的用处是 PASS 回报和日志——
// 所以它只需可读、不需可逆。换行段的文本就是 "\n",不转义会被下面的协议检查拦下
// (上游 2026-09-02 真机上 plan-newline 正是这么死的)。
//
// 非透传词(要被 TIP 原样上屏的)**不走这里**:它们的文本必须逐字节准确,
// 而排版器的输出字母表里本来就没有这几个字符,真出现了就该报错。
var wireEscape = strings.NewReplacer("\\", "\\\\", "\t", "\\t", "\n", "\\n", "\r", "\\r", "|", "\\|")

// TIP 词表的线格式。**放在平台无关的文件里是有意的**:它是协议知识,不是 Windows
// 知识,而 tip_windows.go 只在 Windows 上编译,那里的东西在开发机上一行都测不到。
// 格式写错了要么真机上词全错位、要么音节边界无声消失,两种都只有上机才发现得了。
// 放这里之后 tipwire_test.go 逐字节钉住它,go test 每天跑。
//
// 字段是 `词|音节边界|标志`(与上游 hiBoss `inject/tipwire.go` 逐字节相同):
//
//	你好|2|        走组字,音节边界在第 2 个字母后(组字区显示 ni'hao)
//	9||P           透传:TIP 不吃这个键,让它落到键盘布局上自己打出来
//	招聘顾问|2,4,6|  多个音节边界
//
// 第三段是后加的。老格式(两段)在 TIP 那边解析出来标志为空 = 走组字,
// 与加它之前的行为一致。解析见 hiBoss `tip/src/drive.rs` 的 Word::parse。
func wordFields(words []PlanWord) ([]string, error) {
	fields := make([]string, 0, len(words))
	for _, w := range words {
		text := w.Text
		if w.Passthrough {
			text = wireEscape.Replace(text)
		}
		// 竖线是字段分隔符、制表符是词分隔符、换行是行分隔符——三个都装不下。
		// 回车符也不行:TIP 那边只 trim 行尾的 \r,行中间的会活着进词表。
		// 排版器的输出字母表里本来就没有它们,但协议的兜底得在协议这一层,
		// 不能指望上游——上游第一版这里就漏了 \r,正是「指望上游」的形状。
		if strings.ContainsAny(text, "\t\n\r|") {
			return nil, fmt.Errorf("词 %q 含制表符、换行、回车或竖线 —— 协议是行式文本,装不下", w.Text)
		}
		n := make([]string, len(w.Splits))
		for i, v := range w.Splits {
			n[i] = strconv.Itoa(v)
		}
		flag := ""
		if w.Passthrough {
			flag = "P"
		}
		fields = append(fields, text+"|"+strings.Join(n, ",")+"|"+flag)
	}
	return fields, nil
}
