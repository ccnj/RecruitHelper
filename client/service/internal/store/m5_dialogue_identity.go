package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// DialogueTurnIdentity is the single canonical evaluator for an M5 turn's
// immutable message boundary. Both the patrol producer and every store-side
// authorization recheck use this exact function.
func DialogueTurnIdentity(profileID string, lastOutbound Message, inbound []Message) (string, string, error) {
	if strings.TrimSpace(profileID) == "" || lastOutbound.Seq <= 0 || lastOutbound.Direction != "out" ||
		strings.TrimSpace(lastOutbound.ContentHash) == "" || len(inbound) == 0 {
		return "", "", ErrDialogueTurnInvalid
	}
	type digestMessage struct {
		Seq         int64  `json:"seq"`
		Kind        string `json:"kind"`
		ContentHash string `json:"contentHash"`
	}
	canonical := struct {
		ProfileID        string          `json:"profileId"`
		LastOutboundSeq  int64           `json:"lastOutboundSeq"`
		LastOutboundHash string          `json:"lastOutboundHash"`
		HistoryThrough   int64           `json:"historyThroughSeq"`
		Messages         []digestMessage `json:"messages"`
	}{
		ProfileID: profileID, LastOutboundSeq: lastOutbound.Seq,
		LastOutboundHash: lastOutbound.ContentHash, HistoryThrough: lastOutbound.Seq,
		Messages: make([]digestMessage, 0, len(inbound)),
	}
	previous := lastOutbound.Seq
	for i := range inbound {
		message := inbound[i]
		if message.Direction != "in" || message.Seq <= previous || strings.TrimSpace(message.Kind) == "" ||
			strings.TrimSpace(message.ContentHash) == "" {
			return "", "", ErrDialogueTurnInvalid
		}
		previous = message.Seq
		canonical.Messages = append(canonical.Messages, digestMessage{
			Seq: message.Seq, Kind: message.Kind, ContentHash: message.ContentHash,
		})
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256(raw)
	hexDigest := hex.EncodeToString(digest[:])
	return hexDigest, "turn-" + hexDigest, nil
}
