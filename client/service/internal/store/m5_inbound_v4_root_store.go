package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/textcanon"

	"gorm.io/gorm"
)

const (
	inboundConversationV4RootPrefix = "inbound-conversation-v1:"
	inboundConversationV4RootDomain = "inbound-conversation-v1|"
)

// InboundConversationV4RootRef derives an opaque, versioned root reference
// from the stable page facts. The persisted value cannot reveal any of the
// platform identifiers that were used to bind it.
func InboundConversationV4RootRef(
	platform string,
	accountRef string,
	conversationRef string,
	sourceKey string,
) (string, error) {
	platform = strings.TrimSpace(platform)
	accountRef = strings.TrimSpace(accountRef)
	conversationRef = strings.TrimSpace(conversationRef)
	sourceKey = strings.TrimSpace(sourceKey)
	if platform == "" || accountRef == "" || conversationRef == "" ||
		!validMessageSourceKey(sourceKey) {
		return "", ErrCommunicationV4Invalid
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(inboundConversationV4RootDomain))
	for _, part := range []string{platform, accountRef, conversationRef, sourceKey} {
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(part))
	}
	return inboundConversationV4RootPrefix + hex.EncodeToString(digest.Sum(nil)), nil
}

// IsInboundConversationV4Root distinguishes the candidate-initiated root from
// the historical greeting-intent root without changing the persisted column.
func IsInboundConversationV4Root(rootRef string) bool {
	rootRef = strings.TrimSpace(rootRef)
	if !strings.HasPrefix(rootRef, inboundConversationV4RootPrefix) {
		return false
	}
	digest := strings.TrimPrefix(rootRef, inboundConversationV4RootPrefix)
	return validMessageSourceKey(digest)
}

// EnsureInboundConversationV4Root activates a captured, candidate-initiated
// profile without inventing a greeting EffectIntent or outbound Message. The
// first stable real inbound message binds the root, while projection remains
// at sequence zero so the ordinary reducer can consume the complete ledger in
// order.
func (s *Store) EnsureInboundConversationV4Root(
	profileID string,
	at time.Time,
) (*CommunicationV4Aggregate, bool, error) {
	if s == nil || s.db == nil || strings.TrimSpace(profileID) == "" {
		return nil, false, ErrCommunicationV4Invalid
	}
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()

	var out CommunicationV4Aggregate
	created := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var profile CandidateProfile
		if err := tx.First(&profile, "profile_id = ?", profileID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCommunicationV4Missing
			}
			return err
		}
		firstReal, rootRef, err := inboundConversationV4RootFactsTx(tx, profile)
		if err != nil {
			return err
		}

		var existing CommunicationV4Aggregate
		existingErr := tx.First(&existing, "profile_id = ?", profile.ProfileID).Error
		switch {
		case existingErr == nil:
			validated, err := communicationV4AggregateTx(tx, profile.ProfileID)
			if err != nil {
				return err
			}
			if !IsInboundConversationV4Root(validated.RootGreetingIntentID) ||
				validated.RootGreetingIntentID != rootRef {
				return ErrCommunicationV4Conflict
			}
			out = validated
			return nil
		case !errors.Is(existingErr, gorm.ErrRecordNotFound):
			return existingErr
		}

		if profile.MainStatus != CandidateProfileSelected ||
			profile.EndReason != nil ||
			profile.SuccessfulGreetingIntentID != nil ||
			profile.GreetedAt != nil ||
			profile.CommunicatingAt != nil ||
			profile.FirstRealMessageSeq != nil {
			return ErrCommunicationV4Conflict
		}
		var outboundCount int64
		if err := tx.Model(&Message{}).
			Where(
				"platform = ? AND account_ref = ? AND conversation_ref = ? AND direction = ? AND retracted_at IS NULL",
				profile.Platform,
				profile.AccountRef,
				*profile.ConversationRef,
				"out",
			).
			Count(&outboundCount).Error; err != nil {
			return err
		}
		if outboundCount != 0 {
			return ErrCommunicationV4Conflict
		}

		communicatingAt := inboundConversationV4MessageTime(firstReal, at)
		state := communication.NewV4InboundConversationState()
		if err := communication.ValidateV4State(state); err != nil {
			return ErrCommunicationV4Corrupt
		}
		aggregate := CommunicationV4Aggregate{
			ProfileID:            profile.ProfileID,
			RootGreetingIntentID: rootRef,
			StateSchemaVersion:   communicationV4StateSchemaVersion,
			Revision:             0,
			ProjectedThroughSeq:  0,
			State:                state,
			AutomationStatus:     ProfileCommunicationAutomationActive,
			CreatedAt:            at,
			UpdatedAt:            at,
		}
		profileUpdated := tx.Model(&CandidateProfile{}).
			Where(
				"profile_id = ? AND main_status = ? AND end_reason IS NULL "+
					"AND successful_greeting_intent_id IS NULL AND greeted_at IS NULL "+
					"AND communicating_at IS NULL AND first_real_message_seq IS NULL",
				profile.ProfileID,
				CandidateProfileSelected,
			).
			Updates(map[string]any{
				"main_status":            CandidateProfileCommunicating,
				"first_real_message_seq": firstReal.Seq,
				"communicating_at":       communicatingAt,
				"updated_at":             at,
			})
		if profileUpdated.Error != nil {
			return profileUpdated.Error
		}
		if profileUpdated.RowsAffected != 1 {
			return ErrCommunicationV4Conflict
		}
		if err := tx.Create(&aggregate).Error; err != nil {
			return err
		}
		out = aggregate
		created = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return &out, created, nil
}

func inboundConversationV4RootFactsTx(
	tx *gorm.DB,
	profile CandidateProfile,
) (Message, string, error) {
	if tx == nil ||
		profile.ConversationRef == nil ||
		strings.TrimSpace(*profile.ConversationRef) == "" ||
		profile.Platform == "" ||
		profile.AccountRef == "" ||
		profile.PlatformUserRef == "" ||
		profile.BackendJobID == nil ||
		strings.TrimSpace(*profile.BackendJobID) == "" ||
		profile.PositionRef != strings.TrimSpace(*profile.BackendJobID) ||
		profile.PositionTitle == nil ||
		textcanon.Normalize(*profile.PositionTitle) == "" ||
		profile.ResumeCaptureState != ResumeCaptureCaptured ||
		profile.ResumeCaptureLogicalDispatchID == nil ||
		strings.TrimSpace(*profile.ResumeCaptureLogicalDispatchID) == "" ||
		profile.ActiveResumeSnapshotID == nil ||
		strings.TrimSpace(*profile.ActiveResumeSnapshotID) == "" {
		return Message{}, "", ErrCommunicationV4Conflict
	}
	key := ConversationKey{
		Platform: profile.Platform, AccountRef: profile.AccountRef,
		ConversationRef: *profile.ConversationRef,
	}
	var conversation Conversation
	if err := tx.Where(conversationWhere(key), conversationArgs(key)...).
		First(&conversation).Error; err != nil {
		return Message{}, "", ErrCommunicationV4Conflict
	}
	if conversation.PlatformUserRef != profile.PlatformUserRef ||
		conversation.TrackingState != TrackingAdopted ||
		conversation.AdoptedBoundarySeq <= 0 ||
		conversation.LastMessageSeq < conversation.AdoptedBoundarySeq {
		return Message{}, "", ErrCommunicationV4Conflict
	}
	var tracked TrackedIntent
	if err := tx.Where(conversationWhere(key), conversationArgs(key)...).
		First(&tracked).Error; err != nil {
		return Message{}, "", ErrCommunicationV4Conflict
	}
	if tracked.Status != TrackingAdopted ||
		tracked.AdoptedAt == nil ||
		tracked.RequestedBy != inboundProfileRequestedBy {
		return Message{}, "", ErrCommunicationV4Conflict
	}

	var candidate Candidate
	if err := tx.First(
		&candidate,
		"platform = ? AND platform_user_ref = ?",
		profile.Platform,
		profile.PlatformUserRef,
	).Error; err != nil {
		return Message{}, "", ErrCommunicationV4Conflict
	}
	revision, err := currentLegacyJobAIContextByBackendJobIDTx(
		tx,
		strings.TrimSpace(*profile.BackendJobID),
	)
	if err != nil || revision == nil ||
		revision.SourceJobRef != strings.TrimSpace(*profile.BackendJobID) {
		return Message{}, "", ErrCommunicationV4Conflict
	}
	var snapshot CandidateResumeSnapshot
	if err := tx.First(
		&snapshot,
		"profile_id = ? AND snapshot_id = ?",
		profile.ProfileID,
		*profile.ActiveResumeSnapshotID,
	).Error; err != nil {
		return Message{}, "", ErrCommunicationV4Conflict
	}
	if snapshot.SourceKind != resumeSnapshotSourceIM ||
		snapshot.SourceConversationRef != key.ConversationRef ||
		snapshot.SourceLogicalDispatchID != *profile.ResumeCaptureLogicalDispatchID ||
		snapshot.SchemaVersion != resumeSnapshotSchemaV1 ||
		strings.TrimSpace(snapshot.ContentHash) == "" ||
		strings.TrimSpace(snapshot.ResumeJSON) == "" {
		return Message{}, "", ErrCommunicationV4Conflict
	}

	var messages []Message
	if err := tx.Where(
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND retracted_at IS NULL",
		key.Platform,
		key.AccountRef,
		key.ConversationRef,
	).Order("seq").Find(&messages).Error; err != nil {
		return Message{}, "", err
	}
	for index := range messages {
		message := messages[index]
		if !IsM5RealCandidateMessage(message) {
			continue
		}
		if message.SourceKey == nil || !validMessageSourceKey(*message.SourceKey) {
			return Message{}, "", ErrCommunicationV4Conflict
		}
		rootRef, err := InboundConversationV4RootRef(
			profile.Platform,
			profile.AccountRef,
			key.ConversationRef,
			*message.SourceKey,
		)
		if err != nil {
			return Message{}, "", err
		}
		return message, rootRef, nil
	}
	return Message{}, "", ErrCommunicationV4Conflict
}

func inboundConversationV4MessageTime(message Message, fallback time.Time) time.Time {
	if message.TsApproxMs != nil && *message.TsApproxMs > 0 {
		return time.UnixMilli(*message.TsApproxMs).UTC()
	}
	if !message.CreatedAt.IsZero() {
		return message.CreatedAt.UTC()
	}
	return fallback.UTC()
}

func validateInboundConversationV4RootTx(
	tx *gorm.DB,
	aggregate CommunicationV4Aggregate,
	profile CandidateProfile,
) error {
	if !IsInboundConversationV4Root(aggregate.RootGreetingIntentID) ||
		profile.SuccessfulGreetingIntentID != nil ||
		profile.GreetedAt != nil ||
		profile.FirstRealMessageSeq == nil ||
		*profile.FirstRealMessageSeq <= 0 ||
		profile.CommunicatingAt == nil ||
		profile.CommunicatingAt.IsZero() {
		return ErrCommunicationV4Corrupt
	}
	firstReal, expectedRoot, err := inboundConversationV4RootFactsTx(tx, profile)
	if err != nil ||
		expectedRoot != aggregate.RootGreetingIntentID ||
		firstReal.Seq != *profile.FirstRealMessageSeq ||
		!profile.CommunicatingAt.Equal(inboundConversationV4MessageTime(firstReal, *profile.CommunicatingAt)) {
		return ErrCommunicationV4Corrupt
	}
	return nil
}
