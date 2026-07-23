package textcanon

import "testing"

func TestNormalizeAndHashAreUnicodeWhitespaceStable(t *testing.T) {
	decomposed := "  Cafe\u0301\t\n  你好\u3000世界  "
	composed := "Café 你好 世界"
	if got := Normalize(decomposed); got != composed {
		t.Fatalf("规范化错误: %q", got)
	}
	if Hash(decomposed) != Hash(composed) {
		t.Fatal("规范等价文本必须产生相同摘要")
	}
}
