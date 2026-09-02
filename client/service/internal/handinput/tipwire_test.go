package handinput

import "testing"

// 线格式逐字节钉死。这些字符串是 Go 与 Rust 之间的**唯一契约**,两边各写各的解析,
// 没有共享类型能保证一致——所以这里的每一个字节,都必须和上游
// `tip/src/drive.rs` 的 Word::parse 文档里的例子逐字对得上。用例照搬上游
// tipwire_test.go,免得两边的钉子各钉各的。
func TestWordFields(t *testing.T) {
	got, err := wordFields([]PlanWord{
		{Text: "你好", Splits: []int{2}},
		{Text: "9", Direct: true, Passthrough: true},
		{Text: "，", Direct: true},
		{Text: "招聘顾问", Splits: []int{2, 4, 6}},
		{Text: "base"},
	})
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	want := []string{"你好|2|", "9||P", "，||", "招聘顾问|2,4,6|", "base||"}
	if len(got) != len(want) {
		t.Fatalf("字段数 %d,想要 %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 个字段 = %q,想要 %q", i, got[i], want[i])
		}
	}
}

// 换行段是透传词,文本就是 "\n"。它上线时转成可读的 `\n` 两个字符——TIP 不打它,
// 这段文本只进 PASS 回报和日志。
func TestWordFieldsEscapesPassthroughText(t *testing.T) {
	got, err := wordFields([]PlanWord{
		{Text: "\n", Direct: true, Passthrough: true},
		{Text: "\\", Direct: true, Passthrough: true},
	})
	if err != nil {
		t.Fatalf("透传词的换行该被转义而不是拒绝: %v", err)
	}
	if got[0] != `\n||P` || got[1] != `\\||P` {
		t.Errorf("= %q,想要 %q", got, []string{`\n||P`, `\\||P`})
	}
}

// 三个分隔符加回车符都必须拦住,而且要在发出去之前拦——**对非透传词**。
// 透传词走上面的转义;非透传词的文本要被 TIP 原样上屏,不许改。
func TestWordFieldsRejectsSeparators(t *testing.T) {
	for _, bad := range []string{"\t", "\n", "\r", "|", "前端/后端\n第二行", "你好\r世界"} {
		if _, err := wordFields([]PlanWord{{Text: "你好"}, {Text: bad}}); err == nil {
			t.Errorf("%q 应当被拒绝", bad)
		}
	}
}

// 没有音节边界、也不透传的词,仍然要有两个分隔符——空字段不是可省略的。
// 省了的话 Rust 那边 split_once 只切出一段,标志位读的就是音节字段。
func TestWordFieldsAlwaysTwoSeparators(t *testing.T) {
	got, err := wordFields([]PlanWord{{Text: "吗"}})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "吗||" {
		t.Errorf("= %q,想要 %q", got[0], "吗||")
	}
}
