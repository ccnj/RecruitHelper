package syncledger

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"recruithelper/client/service/internal/store"
)

func TestNormalizeTextNFCWhitespaceAndHash(t *testing.T) {
	decomposed := "  Cafe\u0301\t\n  你好\u3000世界  "
	composed := "Café 你好 世界"
	if got := NormalizeText(decomposed); got != composed {
		t.Fatalf("规范化错误: %q", got)
	}
	if HashText(decomposed) != HashText(composed) {
		t.Fatal("NFC/空白等价文本必须产生相同哈希")
	}
}

func TestNormalizeMessagePlaceholdersAndHashMismatch(t *testing.T) {
	for _, kind := range []string{"image", "voice", "file"} {
		left := "页面可见文案一"
		right := "页面可见文案二"
		a, err := NormalizeMessage(SnapshotMessage{Direction: "in", Kind: kind, Text: &left})
		if err != nil {
			t.Fatalf("%s normalize: %v", kind, err)
		}
		b, err := NormalizeMessage(SnapshotMessage{Direction: "in", Kind: kind, Text: &right})
		if err != nil {
			t.Fatalf("%s normalize: %v", kind, err)
		}
		if a.ContentHash != b.ContentHash {
			t.Fatalf("%s 必须使用稳定占位符哈希", kind)
		}
	}

	text := "同一条消息"
	_, err := NormalizeMessage(SnapshotMessage{
		Direction: "in", Kind: "text", Text: &text, ContentHash: "wrong",
	})
	if !errors.Is(err, ErrContentHashMismatch) {
		t.Fatalf("手脑规范化不一致必须响亮失败,得到 %v", err)
	}
}

func TestNormalizeMessageValidatesAndPreservesSourceKey(t *testing.T) {
	text := "拒绝模板"
	valid := strings.Repeat("a", 64)
	normalized, err := NormalizeMessage(SnapshotMessage{
		Direction: "in", Kind: "text", Text: &text, SourceKey: valid,
	})
	if err != nil {
		t.Fatalf("有效 sourceKey 不应被拒绝: %v", err)
	}
	if normalized.SourceKey != valid {
		t.Fatalf("sourceKey 未穿透规范化层: %q", normalized.SourceKey)
	}
	draft := normalized.draft()
	if draft.SourceKey == nil || *draft.SourceKey != valid {
		t.Fatalf("sourceKey 未穿透到落库草案: %+v", draft.SourceKey)
	}

	invalid := []string{
		strings.Repeat("a", 63),
		strings.Repeat("A", 64),
		strings.Repeat("g", 64),
	}
	for _, sourceKey := range invalid {
		_, err := NormalizeMessage(SnapshotMessage{
			Direction: "in", Kind: "text", Text: &text, SourceKey: sourceKey,
		})
		if !errors.Is(err, ErrInvalidSourceKey) {
			t.Fatalf("非法 sourceKey 必须响亮失败: len=%d err=%v", len(sourceKey), err)
		}
	}
}

func TestCardIdentityExcludesState(t *testing.T) {
	base := SnapshotMessage{
		Direction: "out", Kind: "card", CardType: "interviewInvite",
		CardIdentity: " 2026-07-20\t15:00 ", Origin: "external",
	}
	pending := base
	pending.CardState = "pending"
	accepted := base
	accepted.CardState = "accepted"
	a, err := NormalizeMessage(pending)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NormalizeMessage(accepted)
	if err != nil {
		t.Fatal(err)
	}
	if a.ContentHash != b.ContentHash {
		t.Fatal("卡片状态不得进入 identity hash")
	}
	if a.CardState == b.CardState {
		t.Fatal("卡片 identity 相同时状态仍必须独立保留")
	}
	if a.ContentHash != CardIdentityHash(base.CardType, base.CardIdentity) {
		t.Fatal("卡片哈希入口必须共用同一规则")
	}
	invalid := base
	invalid.CardState = "changed"
	if _, err := NormalizeMessage(invalid); !errors.Is(err, ErrInvalidCardState) {
		t.Fatalf("未知卡片状态不得落账,得到 %v", err)
	}
}

func TestListPreviewUsesSameNormalizationAndRuneTruncation(t *testing.T) {
	prefix := strings.Repeat("候", 199)
	ledgerRaw := prefix + "é" + "账本尾部不应参与比较"
	summaryRaw := "  " + prefix + "e\u0301" + "列表尾部可以不同"
	ledgerText := ledgerRaw
	tail := store.Message{Direction: "in", Kind: "text", Text: &ledgerText}
	if !ListPreviewMatches(ListPreview{Direction: "in", Kind: "text", Text: summaryRaw}, tail) {
		t.Fatal("列表摘要与账本尾必须先同规则规范化/截断再比较")
	}
	preview := CanonicalListPreview("text", ledgerRaw)
	if got := utf8.RuneCountInString(preview); got != MaxListPreviewRunes {
		t.Fatalf("应按 rune 截断到 %d,得到 %d", MaxListPreviewRunes, got)
	}
	if ListPreviewMatches(ListPreview{Direction: "out", Kind: "text", Text: summaryRaw}, tail) {
		t.Fatal("方向不同必须判脏")
	}
	if !ListPreviewMatches(ListPreview{Direction: "in", Kind: "image", Text: "[图片]"},
		store.Message{Direction: "in", Kind: "image"}) {
		t.Fatal("媒体列表摘要必须统一为协议占位符")
	}
}
