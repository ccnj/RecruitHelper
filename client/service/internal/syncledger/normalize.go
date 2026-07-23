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
)

// SnapshotMessage is the neutral seam consumed by the ledger algorithm.
// ContentHash is the hand-provided identity hash. When the brain has enough raw
// material it recomputes and verifies it; blob-backed text and cards without
// CardIdentity intentionally retain the opaque hash supplied by the adapter.
// CardIdentity contains stable key parameters only and must never include state.
type SnapshotMessage struct {
	Direction    string
	Kind         string
	Text         *string
	BlobRef      string
	ContentHash  string
	CardType     string
	CardIdentity string
	CardState    string
	TsApproxMs   *int64
	Origin       string
	SourceKey    string
}

// NormalizedMessage is deterministic input for matching and store writes.
type NormalizedMessage struct {
	Direction   string
	Kind        string
	Text        *string
	BlobRef     string
	ContentHash string
	CardType    string
	CardState   string
	TsApproxMs  *int64
	Origin      string
	SourceKey   string
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
		TsApproxMs: in.TsApproxMs, Origin: origin, SourceKey: in.SourceKey,
	}, nil
}

func canonicalIdentity(in SnapshotMessage, normalizedText *string) (string, bool, error) {
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
		TsApproxMs: m.TsApproxMs, Origin: m.Origin, SourceKey: sourceKey,
	}
}
