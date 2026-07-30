package syncledger

import (
	"crypto/sha256"
	"encoding/hex"
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

func TestKnownCardHashesAndInterviewProjection(t *testing.T) {
	expectedWechat := sha256.Sum256([]byte("card\x1fwechatExchange"))
	if got, want := WechatExchangeContentHash(), hex.EncodeToString(expectedWechat[:]); got != want {
		t.Fatalf("换微信卡 hash=%s, want %s", got, want)
	}
	startsAt, endsAt, method := int64(1_722_000_000_000), int64(1_722_001_800_000), "wechatVideo"
	expectedInvite := sha256.Sum256([]byte(
		"card\x1finterviewInvite\x1f1722000000000\x1f1722001800000\x1fwechatVideo",
	))
	if got, want := InterviewInviteContentHash(startsAt, endsAt, method), hex.EncodeToString(expectedInvite[:]); got != want {
		t.Fatalf("邀面卡 hash=%s, want %s", got, want)
	}

	normalized, err := NormalizeMessage(SnapshotMessage{
		Direction: "out", Kind: "card", CardType: "interviewInvite", CardState: "unknown",
		InterviewStartsAtMs: &startsAt, InterviewEndsAtMs: &endsAt, InterviewMethod: &method,
		Origin: "external",
	})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.ContentHash != InterviewInviteContentHash(startsAt, endsAt, method) ||
		normalized.InterviewStartsAtMs == nil || *normalized.InterviewStartsAtMs != startsAt ||
		normalized.InterviewEndsAtMs == nil || *normalized.InterviewEndsAtMs != endsAt ||
		normalized.InterviewMethod == nil || *normalized.InterviewMethod != method {
		t.Fatalf("邀面卡参数未穿透归一化: %+v", normalized)
	}
	draft := normalized.draft()
	if draft.InterviewStartsAtMs == nil || *draft.InterviewStartsAtMs != startsAt ||
		draft.InterviewEndsAtMs == nil || *draft.InterviewEndsAtMs != endsAt ||
		draft.InterviewMethod == nil || *draft.InterviewMethod != method {
		t.Fatalf("邀面卡参数未穿透落库草案: %+v", draft)
	}

	wechat, err := NormalizeMessage(SnapshotMessage{
		Direction: "out", Kind: "card", CardType: "wechatExchange", CardState: "pending",
	})
	if err != nil || wechat.ContentHash != WechatExchangeContentHash() {
		t.Fatalf("换微信卡应使用冻结 hash: message=%+v err=%v", wechat, err)
	}
}

func TestNormalizeMessageRejectsInvalidInterviewProjection(t *testing.T) {
	validStart, validEnd, validMethod := int64(1000), int64(2000), "wechatVideo"
	wrongMethod := "offline"
	for _, test := range []struct {
		name    string
		message SnapshotMessage
	}{
		{
			name: "partial",
			message: SnapshotMessage{
				Direction: "out", Kind: "card", CardType: "interviewInvite",
				InterviewStartsAtMs: &validStart,
			},
		},
		{
			name: "non increasing",
			message: SnapshotMessage{
				Direction: "out", Kind: "card", CardType: "interviewInvite",
				InterviewStartsAtMs: &validEnd, InterviewEndsAtMs: &validStart, InterviewMethod: &validMethod,
			},
		},
		{
			name: "wrong method",
			message: SnapshotMessage{
				Direction: "out", Kind: "card", CardType: "interviewInvite",
				InterviewStartsAtMs: &validStart, InterviewEndsAtMs: &validEnd, InterviewMethod: &wrongMethod,
			},
		},
		{
			name: "wrong card type",
			message: SnapshotMessage{
				Direction: "out", Kind: "card", CardType: "wechatExchange",
				InterviewStartsAtMs: &validStart, InterviewEndsAtMs: &validEnd, InterviewMethod: &validMethod,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NormalizeMessage(test.message); !errors.Is(err, ErrInvalidInterview) {
				t.Fatalf("非法邀面参数必须响亮失败: %v", err)
			}
		})
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

// 2026-07-31 真机：线下(到场)邀面卡 interviewType="ATTENDANCE"，平台不下发
// interviewPlatform 且 endTime 恒为 "0"。协议规格 §4.5 据此把 endsAt 改为
// optional，投影为空串而不得由 startsAt 合成。
func TestInterviewProjectionAcceptsOnsiteWithoutEndsAt(t *testing.T) {
	startsAt, method := int64(1_785_448_800_000), "onsite"
	expected := sha256.Sum256([]byte(
		"card\x1finterviewInvite\x1f1785448800000\x1f\x1fonsite",
	))
	normalized, err := NormalizeMessage(SnapshotMessage{
		Direction: "out", Kind: "card", CardType: "interviewInvite", CardState: "unknown",
		InterviewStartsAtMs: &startsAt, InterviewMethod: &method, Origin: "external",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := normalized.ContentHash, hex.EncodeToString(expected[:]); got != want {
		t.Fatalf("线下邀面卡 hash=%s, want %s", got, want)
	}
	if normalized.InterviewEndsAtMs != nil {
		t.Fatalf("缺席的 endsAt 不得被合成: %v", *normalized.InterviewEndsAtMs)
	}
	if draft := normalized.draft(); draft.InterviewEndsAtMs != nil ||
		draft.InterviewStartsAtMs == nil || *draft.InterviewStartsAtMs != startsAt ||
		draft.InterviewMethod == nil || *draft.InterviewMethod != method {
		t.Fatalf("线下邀面参数未正确穿透落库草案: %+v", draft)
	}
}

// endsAt 转 optional 不得改变既有 wechatVideo 卡的任何一个字节——它同时是
// 发送侧正证判据，漂移会让存量邀面动作全部对不上。
func TestInterviewProjectionKeepsWechatVideoByteIdentical(t *testing.T) {
	startsAt, endsAt, method := int64(1_722_000_000_000), int64(1_722_001_800_000), "wechatVideo"
	normalized, err := NormalizeMessage(SnapshotMessage{
		Direction: "out", Kind: "card", CardType: "interviewInvite", CardState: "unknown",
		InterviewStartsAtMs: &startsAt, InterviewEndsAtMs: &endsAt, InterviewMethod: &method,
		Origin: "external",
	})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.ContentHash != InterviewInviteContentHash(startsAt, endsAt, method) {
		t.Fatal("wechatVideo 卡的归一化 hash 必须与发送侧冻结投影逐字节一致")
	}
}

func TestInterviewProjectionRejectsUnknownMethodAndBadOnsiteEnds(t *testing.T) {
	startsAt, sameAsStart := int64(1_785_448_800_000), int64(1_785_448_800_000)
	// TENCENT 旧样本已见但本仓库真机未见，按平台枚举面事实门不得放行。
	tencent, onsite := "tencentVideo", "onsite"
	for _, test := range []struct {
		name    string
		message SnapshotMessage
	}{
		{
			name: "unknown method",
			message: SnapshotMessage{
				Direction: "out", Kind: "card", CardType: "interviewInvite",
				InterviewStartsAtMs: &startsAt, InterviewMethod: &tencent,
			},
		},
		{
			name: "onsite ends not after starts",
			message: SnapshotMessage{
				Direction: "out", Kind: "card", CardType: "interviewInvite",
				InterviewStartsAtMs: &startsAt, InterviewEndsAtMs: &sameAsStart, InterviewMethod: &onsite,
			},
		},
		{
			name: "ends without starts",
			message: SnapshotMessage{
				Direction: "out", Kind: "card", CardType: "interviewInvite",
				InterviewEndsAtMs: &sameAsStart, InterviewMethod: &onsite,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NormalizeMessage(test.message); !errors.Is(err, ErrInvalidInterview) {
				t.Fatalf("非法邀面参数必须响亮失败: %v", err)
			}
		})
	}
}
