package store

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

var (
	ErrCandidateProfileNotFound   = errors.New("候选人档案不存在")
	ErrCandidateAlreadyProfiled   = errors.New("候选人已有非淘汰档案")
	ErrCandidateProfileState      = errors.New("候选人档案状态损坏")
	ErrCandidateProfileIDConflict = errors.New("profileId 已被其他候选人档案占用")
)

type CandidateKey struct {
	Platform        string
	PlatformUserRef string
}

type CandidateProfileScope struct {
	Platform        string
	AccountRef      string
	PlatformUserRef string
	PositionRef     string
}

func (s CandidateProfileScope) CandidateKey() CandidateKey {
	return CandidateKey{Platform: s.Platform, PlatformUserRef: s.PlatformUserRef}
}

type SelectCandidateProfileRequest struct {
	ProfileID     string
	Scope         CandidateProfileScope
	DisplayName   *string
	PositionTitle *string
	ObservedAt    time.Time
}

type SelectCandidateProfileResult struct {
	Candidate        Candidate
	Profile          CandidateProfile
	CandidateCreated bool
	ProfileCreated   bool
}

// SelectCandidateProfile 是 M4 有人值守“当前候选人→selected”的唯一账本入口。
// Candidate 快照、同 scope 幂等收编与人级建档闸在同一个 SQLite 单写事务中完成；
// 任一步失败都不会留下新人、改名或半条档案。
func (s *Store) SelectCandidateProfile(req SelectCandidateProfileRequest) (*SelectCandidateProfileResult, error) {
	if err := validateSelectCandidateProfileRequest(req); err != nil {
		return nil, err
	}
	if req.ObservedAt.IsZero() {
		req.ObservedAt = time.Now()
	}
	out := &SelectCandidateProfileResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var account Account
		if err := tx.First(&account, "platform = ? AND account_ref = ?", req.Scope.Platform, req.Scope.AccountRef).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAccountNotFound
			}
			return err
		}

		existingProfile, err := candidateProfileByScopeTx(tx, req.Scope)
		if err != nil {
			return err
		}
		if existingProfile != nil && !validCandidateProfileStatus(existingProfile.MainStatus) {
			return ErrCandidateProfileState
		}
		// 精确 scope 不存在时，ended/greeted/selected 等全部非淘汰事实都会
		// 跨 AccountRef 阻止另一职位；冲突事务不得顺手刷新任何展示快照。
		if existingProfile == nil {
			var occupied CandidateProfile
			if err := tx.First(&occupied, "profile_id = ?", req.ProfileID).Error; err == nil {
				return ErrCandidateProfileIDConflict
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			var activeN int64
			if err := tx.Model(&CandidateProfile{}).
				Where("platform = ? AND platform_user_ref = ? AND main_status <> ?",
					req.Scope.Platform, req.Scope.PlatformUserRef, CandidateProfileEliminated).
				Count(&activeN).Error; err != nil {
				return err
			}
			if activeN != 0 {
				return ErrCandidateAlreadyProfiled
			}
		}

		candidate, candidateCreated, err := upsertCandidateSnapshotTx(tx, req)
		if err != nil {
			return err
		}
		out.Candidate = candidate
		out.CandidateCreated = candidateCreated

		if existingProfile != nil {
			if req.PositionTitle != nil {
				updated := tx.Model(&CandidateProfile{}).
					Where("profile_id = ?", existingProfile.ProfileID).
					Update("position_title", req.PositionTitle)
				if updated.Error != nil {
					return updated.Error
				}
			}
			if err := tx.First(existingProfile, "profile_id = ?", existingProfile.ProfileID).Error; err != nil {
				return err
			}
			out.Profile = *existingProfile
			return nil
		}

		profile := CandidateProfile{
			ProfileID: req.ProfileID, Platform: req.Scope.Platform, AccountRef: req.Scope.AccountRef,
			PlatformUserRef: req.Scope.PlatformUserRef, PositionRef: req.Scope.PositionRef,
			PositionTitle: req.PositionTitle, MainStatus: CandidateProfileSelected,
			ResumeCaptureState: ResumeCaptureUnattempted,
		}
		if err := tx.Create(&profile).Error; err != nil {
			return err
		}
		out.Profile = profile
		out.ProfileCreated = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func validateSelectCandidateProfileRequest(req SelectCandidateProfileRequest) error {
	if req.ProfileID == "" || req.Scope.Platform == "" || req.Scope.AccountRef == "" ||
		req.Scope.PlatformUserRef == "" || req.Scope.PositionRef == "" {
		return errors.New("候选人建档缺少 profileId/platform/accountRef/platformUserRef/positionRef")
	}
	if req.DisplayName != nil && *req.DisplayName == "" {
		return errors.New("候选人展示名不可为空串")
	}
	if req.PositionTitle != nil && *req.PositionTitle == "" {
		return errors.New("职位标题不可为空串")
	}
	return nil
}

func upsertCandidateSnapshotTx(tx *gorm.DB, req SelectCandidateProfileRequest) (Candidate, bool, error) {
	key := req.Scope.CandidateKey()
	var candidate Candidate
	err := tx.First(&candidate, "platform = ? AND platform_user_ref = ?", key.Platform, key.PlatformUserRef).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		candidate = Candidate{
			Platform: key.Platform, PlatformUserRef: key.PlatformUserRef, DisplayName: req.DisplayName,
			FirstSeenAt: req.ObservedAt, LastSeenAt: req.ObservedAt,
		}
		if err := tx.Create(&candidate).Error; err != nil {
			return Candidate{}, false, err
		}
		return candidate, true, nil
	}
	if err != nil {
		return Candidate{}, false, err
	}
	updates := map[string]any{}
	if req.ObservedAt.After(candidate.LastSeenAt) {
		updates["last_seen_at"] = req.ObservedAt
	}
	if req.DisplayName != nil {
		updates["display_name"] = req.DisplayName
	}
	if len(updates) != 0 {
		if err := tx.Model(&Candidate{}).
			Where("platform = ? AND platform_user_ref = ?", key.Platform, key.PlatformUserRef).
			Updates(updates).Error; err != nil {
			return Candidate{}, false, err
		}
		if err := tx.First(&candidate, "platform = ? AND platform_user_ref = ?", key.Platform, key.PlatformUserRef).Error; err != nil {
			return Candidate{}, false, err
		}
	}
	return candidate, false, nil
}

func validCandidateProfileStatus(status CandidateProfileStatus) bool {
	switch status {
	case CandidateProfileSelected, CandidateProfileGreeted, CandidateProfileCommunicating,
		CandidateProfileEnded, CandidateProfileEliminated:
		return true
	default:
		return false
	}
}

func candidateProfileByScopeTx(tx *gorm.DB, scope CandidateProfileScope) (*CandidateProfile, error) {
	var profile CandidateProfile
	err := tx.First(&profile,
		"platform = ? AND account_ref = ? AND platform_user_ref = ? AND position_ref = ?",
		scope.Platform, scope.AccountRef, scope.PlatformUserRef, scope.PositionRef).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &profile, nil
}

func (s *Store) CandidateByKey(key CandidateKey) (*Candidate, error) {
	var candidate Candidate
	err := s.db.First(&candidate, "platform = ? AND platform_user_ref = ?", key.Platform, key.PlatformUserRef).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &candidate, nil
}

func (s *Store) CandidateProfileByID(profileID string) (*CandidateProfile, error) {
	var profile CandidateProfile
	err := s.db.First(&profile, "profile_id = ?", profileID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &profile, nil
}

func (s *Store) CandidateProfileByScope(scope CandidateProfileScope) (*CandidateProfile, error) {
	return candidateProfileByScopeTx(s.db, scope)
}
