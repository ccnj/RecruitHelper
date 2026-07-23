// Package textcanon owns the platform-neutral canonical form used by message
// fingerprints. It is dependency-free from store/syncledger so planning and
// reconciliation cannot drift onto two subtly different text hashes.
package textcanon

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Normalize applies Unicode NFC, trims the edges and collapses every run of
// Unicode whitespace to one ASCII space.
func Normalize(raw string) string {
	return strings.Join(strings.Fields(norm.NFC.String(raw)), " ")
}

// Hash returns the lowercase SHA-256 hex digest of normalized text.
func Hash(raw string) string {
	sum := sha256.Sum256([]byte(Normalize(raw)))
	return hex.EncodeToString(sum[:])
}
