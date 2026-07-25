package store

import (
	"errors"
	"strings"
	"time"

	"recruithelper/client/service/internal/textcanon"
	"recruithelper/internal/ids"

	"gorm.io/gorm"
)

var (
	ErrInboundProfileAdoptionInvalid  = errors.New("主动来聊候选人收编输入无效")
	ErrInboundProfileAdoptionConflict = errors.New("主动来聊候选人收编事实冲突")
)

type InboundProfileAdoptionOutcome string

const (
	InboundProfileAdopted           InboundProfileAdoptionOutcome = "adopted"
	InboundProfileAlreadyAdopted    InboundProfileAdoptionOutcome = "alreadyAdopted"
	InboundProfilePositionNoMatch   InboundProfileAdoptionOutcome = "positionNoMatch"
	InboundProfilePositionAmbiguous InboundProfileAdoptionOutcome = "positionAmbiguous"
)

const inboundProfileRequestedBy = "system:inbound-conversation"

type AdoptInboundConversationProfileRequest struct {
	Platform        string
	AccountRef      string
	ConversationRef string
	PlatformUserRef string
	DisplayName     string
	PositionTitle   string
	ObservedAt      time.Time
}

type AdoptInboundConversationProfileResult struct {
	Outcome InboundProfileAdoptionOutcome
	Profile *CandidateProfile
}

// AdoptInboundConversationProfile atomically turns one untracked IM list row
// into a selected candidate profile and a pending tracking intent. The page
// position title is allowed to route only when it uniquely equals the display
// name of a current legacyJobConfig head after the repository's ordinary text
// normalization. A missing or ambiguous match is a conservative, non-mutating
// result rather than an error.
//
// PositionRef intentionally stores the same backend Job.ID as BackendJobID for
// this inbound-only path: the IM list contract exposes the visible position
// title, not a separately trusted platform position identity. Because the
// production configuration plane currently syncs exactly one current job,
// historical heads never participate: only the most recently synchronized
// head may match, while a timestamp tie is treated as ambiguous.
func (s *Store) AdoptInboundConversationProfile(
	req AdoptInboundConversationProfileRequest,
) (*AdoptInboundConversationProfileResult, error) {
	if s == nil || s.db == nil ||
		strings.TrimSpace(req.Platform) == "" ||
		strings.TrimSpace(req.AccountRef) == "" ||
		strings.TrimSpace(req.ConversationRef) == "" ||
		strings.TrimSpace(req.PlatformUserRef) == "" {
		return nil, ErrInboundProfileAdoptionInvalid
	}
	displayName := textcanon.Normalize(req.DisplayName)
	positionTitle := textcanon.Normalize(req.PositionTitle)
	if displayName == "" || positionTitle == "" {
		return nil, ErrInboundProfileAdoptionInvalid
	}
	if req.ObservedAt.IsZero() {
		req.ObservedAt = time.Now()
	}
	req.ObservedAt = req.ObservedAt.UTC()

	out := &AdoptInboundConversationProfileResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		key := ConversationKey{
			Platform: req.Platform, AccountRef: req.AccountRef,
			ConversationRef: req.ConversationRef,
		}
		if err := requireAccount(tx, AccountKey{
			Platform:   req.Platform,
			AccountRef: req.AccountRef,
		}); err != nil {
			return err
		}
		var conversation Conversation
		if err := tx.Where(conversationWhere(key), conversationArgs(key)...).
			First(&conversation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrInboundProfileAdoptionConflict
			}
			return err
		}
		if conversation.PlatformUserRef != req.PlatformUserRef {
			return ErrInboundProfileAdoptionConflict
		}

		existing, err := inboundProfileByConversationTx(tx, key)
		if err != nil {
			return err
		}
		if existing != nil {
			if err := validateReplayedInboundProfileTx(
				tx,
				conversation,
				*existing,
				req.PlatformUserRef,
				positionTitle,
			); err != nil {
				return err
			}
			profile := *existing
			out.Outcome = InboundProfileAlreadyAdopted
			out.Profile = &profile
			return nil
		}
		if conversation.TrackingState != TrackingUntracked ||
			conversation.AdoptedBoundarySeq != 0 ||
			conversation.LastMessageSeq != 0 {
			return ErrInboundProfileAdoptionConflict
		}
		var trackedN int64
		if err := tx.Model(&TrackedIntent{}).
			Where(conversationWhere(key), conversationArgs(key)...).
			Count(&trackedN).Error; err != nil {
			return err
		}
		if trackedN != 0 {
			return ErrInboundProfileAdoptionConflict
		}

		match, ambiguous, err := currentLegacyJobMatchByTitleTx(tx, positionTitle)
		if err != nil {
			return err
		}
		switch {
		case ambiguous:
			out.Outcome = InboundProfilePositionAmbiguous
			return nil
		case match == nil:
			out.Outcome = InboundProfilePositionNoMatch
			return nil
		default:
			// Continue inside this transaction so the matched current head and
			// the new profile cannot observe different configuration states.
		}
		backendJobID := strings.TrimSpace(match.SourceJobRef)
		if backendJobID == "" {
			return ErrJobAIContextHeadInvalid
		}

		var activeN int64
		if err := tx.Model(&CandidateProfile{}).
			Where(
				"platform = ? AND platform_user_ref = ? AND main_status <> ?",
				req.Platform,
				req.PlatformUserRef,
				CandidateProfileEliminated,
			).
			Count(&activeN).Error; err != nil {
			return err
		}
		if activeN != 0 {
			return ErrCandidateAlreadyProfiled
		}
		scope := CandidateProfileScope{
			Platform: req.Platform, AccountRef: req.AccountRef,
			PlatformUserRef: req.PlatformUserRef, PositionRef: backendJobID,
		}
		occupied, err := candidateProfileByScopeTx(tx, scope)
		if err != nil {
			return err
		}
		if occupied != nil {
			return ErrCandidateAlreadyProfiled
		}

		displayNameCopy := displayName
		positionTitleCopy := positionTitle
		backendJobIDCopy := backendJobID
		conversationRefCopy := req.ConversationRef
		profileID := ids.NewProfileID()
		_, _, err = upsertCandidateSnapshotTx(tx, SelectCandidateProfileRequest{
			ProfileID:     profileID,
			Scope:         scope,
			DisplayName:   &displayNameCopy,
			PositionTitle: &positionTitleCopy,
			ObservedAt:    req.ObservedAt,
		})
		if err != nil {
			return err
		}

		profile := CandidateProfile{
			ProfileID: profileID,
			Platform:  req.Platform, AccountRef: req.AccountRef,
			PlatformUserRef: req.PlatformUserRef,
			PositionRef:     backendJobID, PositionTitle: &positionTitleCopy,
			BackendJobID:       &backendJobIDCopy,
			MainStatus:         CandidateProfileSelected,
			ConversationRef:    &conversationRefCopy,
			ResumeCaptureState: ResumeCaptureUnattempted,
		}
		if err := tx.Create(&profile).Error; err != nil {
			return err
		}
		tracked := TrackedIntent{
			Platform: req.Platform, AccountRef: req.AccountRef,
			ConversationRef: req.ConversationRef,
			Status:          TrackingPending, RequestedBy: inboundProfileRequestedBy,
			RequestedAt: req.ObservedAt,
		}
		if err := tx.Create(&tracked).Error; err != nil {
			return err
		}
		updated := tx.Model(&Conversation{}).
			Where(conversationWhere(key), conversationArgs(key)...).
			Where("tracking_state = ? AND adopted_boundary_seq = 0 AND last_message_seq = 0", TrackingUntracked).
			Update("tracking_state", TrackingPending)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrInboundProfileAdoptionConflict
		}

		out.Outcome = InboundProfileAdopted
		out.Profile = &profile
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func currentLegacyJobMatchByTitleTx(
	tx *gorm.DB,
	positionTitle string,
) (*JobAIContextRevision, bool, error) {
	if tx == nil || textcanon.Normalize(positionTitle) == "" {
		return nil, false, ErrInboundProfileAdoptionInvalid
	}
	var heads []JobAIContextHead
	if err := tx.Where("source_kind = ?", legacyJobConfigSourceKind).
		Order("last_synced_at DESC, source_job_ref ASC").
		Limit(2).
		Find(&heads).Error; err != nil {
		return nil, false, err
	}
	if len(heads) == 0 {
		return nil, false, nil
	}
	if len(heads) > 1 &&
		heads[0].LastSyncedAt.Equal(heads[1].LastSyncedAt) &&
		heads[0].SourceJobRef != heads[1].SourceJobRef {
		return nil, true, nil
	}
	revision, err := currentLegacyJobAIContextByBackendJobIDTx(
		tx,
		heads[0].SourceJobRef,
	)
	if err != nil {
		return nil, false, err
	}
	if revision == nil {
		return nil, false, ErrJobAIContextHeadInvalid
	}
	if textcanon.Normalize(revision.DisplayName) != textcanon.Normalize(positionTitle) {
		return nil, false, nil
	}
	return revision, false, nil
}

func inboundProfileByConversationTx(
	tx *gorm.DB,
	key ConversationKey,
) (*CandidateProfile, error) {
	var profile CandidateProfile
	err := tx.First(
		&profile,
		"platform = ? AND account_ref = ? AND conversation_ref = ?",
		key.Platform,
		key.AccountRef,
		key.ConversationRef,
	).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &profile, nil
}

func validateReplayedInboundProfileTx(
	tx *gorm.DB,
	conversation Conversation,
	profile CandidateProfile,
	platformUserRef string,
	positionTitle string,
) error {
	if profile.Platform != conversation.Platform ||
		profile.AccountRef != conversation.AccountRef ||
		profile.ConversationRef == nil ||
		*profile.ConversationRef != conversation.ConversationRef ||
		profile.PlatformUserRef != platformUserRef ||
		profile.PlatformUserRef != conversation.PlatformUserRef ||
		profile.BackendJobID == nil ||
		strings.TrimSpace(*profile.BackendJobID) == "" ||
		profile.PositionRef != strings.TrimSpace(*profile.BackendJobID) ||
		profile.PositionTitle == nil ||
		textcanon.Normalize(*profile.PositionTitle) != positionTitle ||
		!validCandidateProfileStatus(profile.MainStatus) {
		return ErrInboundProfileAdoptionConflict
	}
	var tracked TrackedIntent
	if err := tx.Where(
		conversationWhere(ConversationKey{
			Platform:        conversation.Platform,
			AccountRef:      conversation.AccountRef,
			ConversationRef: conversation.ConversationRef,
		}),
		conversationArgs(ConversationKey{
			Platform:        conversation.Platform,
			AccountRef:      conversation.AccountRef,
			ConversationRef: conversation.ConversationRef,
		})...,
	).First(&tracked).Error; err != nil {
		return ErrInboundProfileAdoptionConflict
	}
	if (tracked.Status != TrackingPending && tracked.Status != TrackingAdopted) ||
		conversation.TrackingState != tracked.Status {
		return ErrInboundProfileAdoptionConflict
	}
	return nil
}
