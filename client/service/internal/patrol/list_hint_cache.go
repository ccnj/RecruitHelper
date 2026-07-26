package patrol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

const listHintFingerprintVersion = "list-hint-v1"

// listHintVerificationKey keeps a successful list-hint observation scoped to
// one recruiting-account identity generation. It is deliberately in-memory
// only: losing it can cause one extra thread read, never lost business facts.
type listHintVerificationKey struct {
	platform             string
	accountRef           string
	principalFingerprint string
	conversationRef      string
}

type listHintFingerprintInput struct {
	Version              string `json:"version"`
	Platform             string `json:"platform"`
	AccountRef           string `json:"accountRef"`
	PrincipalFingerprint string `json:"principalFingerprint"`
	ConversationRef      string `json:"conversationRef"`
	Direction            string `json:"direction"`
	Kind                 string `json:"kind"`
	CanonicalPreviewHash string `json:"canonicalPreviewHash"`
	UnreadCount          int    `json:"unreadCount"`
	LastActivityMs       *int64 `json:"lastActivityMs"`
}

func makeListHintVerificationKey(
	platform string,
	accountRef string,
	principalFingerprint string,
	conversationRef string,
) listHintVerificationKey {
	return listHintVerificationKey{
		platform:             platform,
		accountRef:           accountRef,
		principalFingerprint: principalFingerprint,
		conversationRef:      conversationRef,
	}
}

// listHintFingerprint hashes the entire low-fidelity observation. The
// canonical preview is itself reduced to a digest before it enters the outer
// recipe, so the Manager cache never retains candidate message text.
func listHintFingerprint(
	key listHintVerificationKey,
	summary protocol.ConversationSummary,
) string {
	canonicalPreview := syncledger.CanonicalListPreview(
		string(summary.LastMessage.Kind),
		summary.LastMessage.TextPreview,
	)
	previewDigest := sha256.Sum256([]byte(canonicalPreview))
	input := listHintFingerprintInput{
		Version:              listHintFingerprintVersion,
		Platform:             key.platform,
		AccountRef:           key.accountRef,
		PrincipalFingerprint: key.principalFingerprint,
		ConversationRef:      key.conversationRef,
		Direction:            string(summary.LastMessage.Direction),
		Kind:                 string(summary.LastMessage.Kind),
		CanonicalPreviewHash: hex.EncodeToString(previewDigest[:]),
		UnreadCount:          summary.UnreadCount,
		LastActivityMs:       summary.LastActivityTs,
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		// Every field above is a scalar or nullable scalar. If that invariant is
		// ever broken, an empty digest fails open to another authoritative read
		// instead of suppressing one.
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// observeListHintFingerprint observes the current fingerprint and invalidates a
// previously verified different value immediately. This ordering is
// essential for A -> B(failed) -> A: the failed B read must not leave A
// eligible for suppression when it reappears.
//
// Caller must hold Manager.mu.
func (m *Manager) observeListHintFingerprint(
	key listHintVerificationKey,
	fingerprint string,
) (alreadyVerified bool, changedFromVerified bool) {
	verified, ok := m.verifiedListHints[key]
	if !ok {
		return false, false
	}
	if verified != fingerprint {
		delete(m.verifiedListHints, key)
		return false, true
	}
	return true, false
}

// markListHintVerified is called only after the authoritative thread snapshot
// has reconciled and ApplyPlan has committed successfully.
//
// Caller must hold Manager.mu.
func (m *Manager) markListHintVerified(
	key listHintVerificationKey,
	fingerprint string,
) {
	if fingerprint == "" {
		return
	}
	m.verifiedListHints[key] = fingerprint
}
