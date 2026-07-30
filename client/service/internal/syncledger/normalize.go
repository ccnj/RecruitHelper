// Package syncledger implements the brain-side, platform-neutral conversation
// normalization and reconciliation algorithm. Platform adapters only translate
// generated protocol messages into SnapshotMessage; they do not own cursors or
// business state.
package syncledger

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/textcanon"
)

const MaxListPreviewRunes = 200

var (
	ErrInvalidDirection     = errors.New("非法消息方向")
	ErrInvalidMessageKind   = errors.New("非法消息类型")
	ErrInvalidOrigin        = errors.New("非法消息 origin")
	ErrContentHashRequired  = errors.New("无法从快照计算 contentHash")
	ErrContentHashMismatch  = errors.New("快照 contentHash 与脑侧规范化结果不一致")
	ErrCardIdentityRequired = errors.New("卡片缺少身份哈希或身份材料")
	ErrInvalidCardState     = errors.New("非法卡片状态")
	ErrInvalidSourceKey     = errors.New("非法消息 sourceKey")
	ErrInvalidInterview     = errors.New("非法邀面卡参数")
)

// SnapshotMessage is the neutral seam consumed by the ledger algorithm.
// ContentHash is the hand-provided identity hash. When the brain has enough raw
// material it recomputes and verifies it; blob-backed text and cards without
// CardIdentity intentionally retain the opaque hash supplied by the adapter.
// CardIdentity contains stable key parameters only and must never include state.
type SnapshotMessage struct {
	Direction           string
	Kind                string
	Text                *string
	BlobRef             string
	ContentHash         string
	CardType            string
	CardIdentity        string
	CardState           string
	InterviewStartsAtMs *int64
	InterviewEndsAtMs   *int64
	InterviewMethod     *string
	TsApproxMs          *int64
	Origin              string
	SourceKey           string
}

// NormalizedMessage is deterministic input for matching and store writes.
type NormalizedMessage struct {
	Direction           string
	Kind                string
	Text                *string
	BlobRef             string
	ContentHash         string
	CardType            string
	CardState           string
	InterviewStartsAtMs *int64
	InterviewEndsAtMs   *int64
	InterviewMethod     *string
	TsApproxMs          *int64
	Origin              string
	SourceKey           string
}

// NormalizeText is the single text rule shared by hashes and list previews:
// Unicode NFC, trim, then collapse every run of Unicode whitespace to one ASCII
// space.
func NormalizeText(raw string) string {
	return textcanon.Normalize(raw)
}

// HashText returns sha256 of normalized text.
func HashText(raw string) string {
	return textcanon.Hash(raw)
}

// CardIdentityHash hashes card type plus stable key parameters. CardState is
// deliberately absent, so pending -> accepted remains the same message.
func CardIdentityHash(cardType, identity string) string {
	return hashCanonical("card\x1f" + cardType + "\x1f" + NormalizeText(identity))
}

// WechatExchangeContentHash is the frozen identity projection for a reliably
// normalized WeChat exchange card. Card state and platform message identity
// are intentionally absent.
func WechatExchangeContentHash() string {
	return hashCanonical("card\x1fwechatExchange")
}

// InterviewInviteContentHash is the frozen identity projection for a reliably
// normalized interview invitation card.
func InterviewInviteContentHash(startsAtMs, endsAtMs int64, method string) string {
	return hashCanonical("card\x1finterviewInvite\x1f" +
		strconv.FormatInt(startsAtMs, 10) + "\x1f" +
		strconv.FormatInt(endsAtMs, 10) + "\x1f" + method)
}

// NormalizeMessage verifies/derives identity and produces a store-safe message.
func NormalizeMessage(in SnapshotMessage) (NormalizedMessage, error) {
	if !validDirection(in.Direction) {
		return NormalizedMessage{}, fmt.Errorf("%w: %q", ErrInvalidDirection, in.Direction)
	}
	if !validKind(in.Kind) {
		return NormalizedMessage{}, fmt.Errorf("%w: %q", ErrInvalidMessageKind, in.Kind)
	}
	origin := in.Origin
	if origin == "" {
		origin = "external"
	}
	if origin != "external" && origin != "self" {
		return NormalizedMessage{}, fmt.Errorf("%w: %q", ErrInvalidOrigin, origin)
	}
	if in.SourceKey != "" && !validSourceKey(in.SourceKey) {
		return NormalizedMessage{}, ErrInvalidSourceKey
	}

	var normalizedText *string
	if in.Text != nil {
		text := NormalizeText(*in.Text)
		normalizedText = &text
	}

	computed, computable, err := canonicalIdentity(in, normalizedText)
	if err != nil {
		return NormalizedMessage{}, err
	}
	hash := in.ContentHash
	if computable {
		expected := hashCanonical(computed)
		if hash != "" && hash != expected {
			return NormalizedMessage{}, fmt.Errorf("%w: kind=%s", ErrContentHashMismatch, in.Kind)
		}
		hash = expected
	}
	if hash == "" {
		if in.Kind == "card" {
			return NormalizedMessage{}, ErrCardIdentityRequired
		}
		return NormalizedMessage{}, ErrContentHashRequired
	}

	cardState := in.CardState
	if in.Kind == "card" && cardState == "" {
		cardState = "unknown"
	}
	if in.Kind == "card" && !validCardState(cardState) {
		return NormalizedMessage{}, fmt.Errorf("%w: %q", ErrInvalidCardState, cardState)
	}
	return NormalizedMessage{
		Direction: in.Direction, Kind: in.Kind, Text: normalizedText, BlobRef: in.BlobRef,
		ContentHash: hash, CardType: in.CardType, CardState: cardState,
		InterviewStartsAtMs: cloneInt64(in.InterviewStartsAtMs),
		InterviewEndsAtMs:   cloneInt64(in.InterviewEndsAtMs),
		InterviewMethod:     cloneString(in.InterviewMethod),
		TsApproxMs:          in.TsApproxMs, Origin: origin, SourceKey: in.SourceKey,
	}, nil
}

func canonicalIdentity(in SnapshotMessage, normalizedText *string) (string, bool, error) {
	if err := validateInterviewProjection(in); err != nil {
		return "", false, err
	}
	switch in.Kind {
	case "image":
		return "[image]", true, nil
	case "voice":
		return "[voice]", true, nil
	case "file":
		return "[file]", true, nil
	case "card":
		if in.CardType == "" {
			return "", false, ErrCardIdentityRequired
		}
		switch in.CardType {
		case "wechatExchange":
			if in.CardIdentity == "" {
				return "card\x1fwechatExchange", true, nil
			}
		case "interviewInvite":
			if in.InterviewStartsAtMs != nil {
				// endsAt 缺席(线下到场面试,平台恒返回 0)时投影为空串,分隔符
				// 位数不变;wechatVideo 卡的结果与本条改写前逐字节相同。
				ends := ""
				if in.InterviewEndsAtMs != nil {
					ends = strconv.FormatInt(*in.InterviewEndsAtMs, 10)
				}
				return "card\x1finterviewInvite\x1f" +
					strconv.FormatInt(*in.InterviewStartsAtMs, 10) + "\x1f" +
					ends + "\x1f" + *in.InterviewMethod, true, nil
			}
		}
		if in.CardIdentity == "" {
			return "", false, nil
		}
		return "card\x1f" + in.CardType + "\x1f" + NormalizeText(in.CardIdentity), true, nil
	case "text", "system":
		if normalizedText == nil {
			// Blob contents are fetched/verified by the data-plane adapter. Until
			// then the opaque protocol hash is the only available identity.
			if in.BlobRef != "" {
				return "", false, nil
			}
			return "", false, ErrContentHashRequired
		}
		return *normalizedText, true, nil
	default:
		return "", false, fmt.Errorf("%w: %q", ErrInvalidMessageKind, in.Kind)
	}
}

func validateInterviewProjection(in SnapshotMessage) error {
	hasStarts := in.InterviewStartsAtMs != nil
	hasEnds := in.InterviewEndsAtMs != nil
	hasMethod := in.InterviewMethod != nil
	if !hasStarts && !hasEnds && !hasMethod {
		return nil
	}
	// 2026-07-31 甲方裁决：endsAt 对线下到场面试 optional——平台恒返回 0，按
	// 协议规格 §4.5 必须省略而不得由 startsAt 合成；有值时仍要求晚于 startsAt。
	if in.Kind != "card" || in.CardType != "interviewInvite" ||
		!hasStarts || !hasMethod ||
		*in.InterviewStartsAtMs <= 0 ||
		(hasEnds && *in.InterviewEndsAtMs <= *in.InterviewStartsAtMs) ||
		(*in.InterviewMethod != "wechatVideo" && *in.InterviewMethod != "onsite") {
		return ErrInvalidInterview
	}
	return nil
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func hashCanonical(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func validDirection(direction string) bool {
	switch direction {
	case "in", "out", "system":
		return true
	default:
		return false
	}
}

func validKind(kind string) bool {
	switch kind {
	case "text", "image", "voice", "file", "card", "system":
		return true
	default:
		return false
	}
}

func validCardState(state string) bool {
	switch state {
	case "pending", "accepted", "rejected", "expired", "unknown":
		return true
	default:
		return false
	}
}

func validSourceKey(sourceKey string) bool {
	if len(sourceKey) != sha256.Size*2 {
		return false
	}
	for i := 0; i < len(sourceKey); i++ {
		ch := sourceKey[i]
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

// CanonicalListPreview applies the same normalization/placeholders as message
// identity, then truncates by Unicode code points rather than UTF-8 bytes.
func CanonicalListPreview(kind, raw string) string {
	var canonical string
	switch kind {
	case "image":
		canonical = "[image]"
	case "voice":
		canonical = "[voice]"
	case "file":
		canonical = "[file]"
	default:
		canonical = NormalizeText(raw)
	}
	return truncateRunes(canonical, MaxListPreviewRunes)
}

type ListPreview struct {
	Direction string
	Kind      string
	Text      string
}

// ListPreviewMatches compares a list summary with a ledger tail using one rule
// on both sides. It never compares a full contentHash directly with textPreview.
func ListPreviewMatches(summary ListPreview, tail store.Message) bool {
	if summary.Direction != tail.Direction || summary.Kind != tail.Kind {
		return false
	}
	tailText := ""
	if tail.Text != nil {
		tailText = *tail.Text
	}
	return CanonicalListPreview(summary.Kind, summary.Text) == CanonicalListPreview(tail.Kind, tailText)
}

func truncateRunes(value string, limit int) string {
	if limit < 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

func (m NormalizedMessage) draft() store.MessageDraft {
	var sourceKey *string
	if m.SourceKey != "" {
		value := m.SourceKey
		sourceKey = &value
	}
	return store.MessageDraft{
		Direction: m.Direction, Kind: m.Kind, ContentHash: m.ContentHash, Text: m.Text,
		BlobRef: m.BlobRef, CardType: m.CardType, CardState: m.CardState,
		InterviewStartsAtMs: cloneInt64(m.InterviewStartsAtMs),
		InterviewEndsAtMs:   cloneInt64(m.InterviewEndsAtMs),
		InterviewMethod:     cloneString(m.InterviewMethod),
		TsApproxMs:          m.TsApproxMs, Origin: m.Origin, SourceKey: sourceKey,
	}
}
