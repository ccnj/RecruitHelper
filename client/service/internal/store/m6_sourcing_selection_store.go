package store

import (
	"errors"
	"strings"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/contract/gen/go/protocol"

	"gorm.io/gorm"
)

var (
	ErrSourcingSelectionNotReady = errors.New("采集候选人尚未形成可裁决评分")
	ErrSourcingSelectionConflict = errors.New("采集选人决策材料冲突")
)

type DecideSourcingCandidateRequest struct {
	RunID     string
	ProfileID string
	DecidedAt time.Time
}

type DecideSourcingCandidateResult struct {
	Decision SourcingSelectionDecision
	Profile  *CandidateProfile
	Created  bool
}

// NextSourcingRunWithoutSelection 返回最早一条已经终局评分、但尚无选人决策的
// 采集事实。评分失败也会返回，以便写下显式终局后继续处理下一位候选人。
func (s *Store) NextSourcingRunWithoutSelection(key AccountKey, revisionHash string) (*SourcingCandidateRun, error) {
	if key.Platform == "" || key.AccountRef == "" || strings.TrimSpace(revisionHash) == "" {
		return nil, ErrSourcingSelectionNotReady
	}
	var run SourcingCandidateRun
	err := s.db.Table("sourcing_candidate_runs AS run").
		Select("run.*").
		Joins("JOIN sourcing_score_invocations AS score ON score.run_id = run.run_id AND score.finished_at IS NOT NULL").
		Joins("LEFT JOIN sourcing_selection_decisions AS decision ON decision.run_id = run.run_id").
		Where("run.platform = ? AND run.account_ref = ? AND run.context_revision_hash = ? AND decision.run_id IS NULL",
			key.Platform, key.AccountRef, revisionHash).
		Order("run.captured_at ASC, run.run_id ASC").
		First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Store) SourcingSelectionByRunID(runID string) (*SourcingSelectionDecision, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, ErrSourcingSelectionNotReady
	}
	var decision SourcingSelectionDecision
	err := s.db.First(&decision, "run_id = ?", runID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &decision, nil
}

// DecideSourcingCandidate 在一个 SQLite 单写事务内重读 run、评分和不可变职位
// revision，再落一次性决策。只有高于阈值、尚未建联且无人级档案占用的候选人
// 会同时创建 selected 档案；任何其他终局都只留下无明文的决策事实。
func (s *Store) DecideSourcingCandidate(req DecideSourcingCandidateRequest) (*DecideSourcingCandidateResult, error) {
	req.RunID = strings.TrimSpace(req.RunID)
	req.ProfileID = strings.TrimSpace(req.ProfileID)
	if req.RunID == "" || req.ProfileID == "" {
		return nil, ErrSourcingSelectionNotReady
	}
	if req.DecidedAt.IsZero() {
		req.DecidedAt = time.Now()
	}
	out := &DecideSourcingCandidateResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var existingDecision SourcingSelectionDecision
		if err := tx.First(&existingDecision, "run_id = ?", req.RunID).Error; err == nil {
			out.Decision = existingDecision
			if existingDecision.ProfileID != nil {
				var profile CandidateProfile
				if err := tx.First(&profile, "profile_id = ?", *existingDecision.ProfileID).Error; err != nil {
					return ErrSourcingSelectionConflict
				}
				out.Profile = &profile
			}
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var run SourcingCandidateRun
		if err := tx.First(&run, "run_id = ?", req.RunID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourcingSelectionNotReady
			}
			return err
		}
		var score SourcingScoreInvocation
		if err := tx.First(&score, "run_id = ?", run.RunID).Error; err != nil || score.FinishedAt == nil {
			return ErrSourcingSelectionNotReady
		}
		if score.ContextRevisionHash != run.ContextRevisionHash || score.RunContentHash != run.ContentHash {
			return ErrSourcingSelectionConflict
		}
		var revision JobAIContextRevision
		if err := tx.First(&revision, "revision_hash = ?", run.ContextRevisionHash).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrJobAIContextRevisionNotFound
			}
			return err
		}
		view, err := m5ai.DeriveSourcingView(revision.SourcePackage)
		if err != nil {
			return err
		}

		decision := SourcingSelectionDecision{
			RunID: run.RunID, ContextRevisionHash: run.ContextRevisionHash,
			Score: score.Score, MinScore: view.CandidateSelection.MinScore,
			DecidedAt: req.DecidedAt, CreatedAt: req.DecidedAt,
		}
		switch {
		case score.Status != AIInvocationOK || score.Score == nil:
			decision.Outcome = SourcingSelectionScoringFailed
		case run.ContactState != string(protocol.CandidateContactStateUnestablished):
			decision.Outcome = SourcingSelectionContactStateRejected
		case *score.Score < view.CandidateSelection.MinScore:
			decision.Outcome = SourcingSelectionScoreBelowThreshold
		default:
			profile, createErr := createSourcingSelectedProfileTx(tx, run, req.ProfileID, req.DecidedAt)
			if errors.Is(createErr, ErrCandidateAlreadyProfiled) {
				decision.Outcome = SourcingSelectionExistingProfile
			} else if createErr != nil {
				return createErr
			} else {
				decision.Outcome = SourcingSelectionSelected
				decision.ProfileID = &profile.ProfileID
				out.Profile = profile
			}
		}
		if err := tx.Create(&decision).Error; err != nil {
			return ErrSourcingSelectionConflict
		}
		out.Decision = decision
		out.Created = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func createSourcingSelectedProfileTx(
	tx *gorm.DB,
	run SourcingCandidateRun,
	profileID string,
	at time.Time,
) (*CandidateProfile, error) {
	var account Account
	if err := tx.First(&account, "platform = ? AND account_ref = ?", run.Platform, run.AccountRef).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAccountNotFound
		}
		return nil, err
	}
	scope := CandidateProfileScope{
		Platform: run.Platform, AccountRef: run.AccountRef,
		PlatformUserRef: run.PlatformUserRef, PositionRef: run.PositionRef,
	}
	if existing, err := candidateProfileByScopeTx(tx, scope); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, ErrCandidateAlreadyProfiled
	}
	var occupied int64
	if err := tx.Model(&CandidateProfile{}).
		Where("platform = ? AND platform_user_ref = ? AND main_status <> ?",
			run.Platform, run.PlatformUserRef, CandidateProfileEliminated).
		Count(&occupied).Error; err != nil {
		return nil, err
	}
	if occupied != 0 {
		return nil, ErrCandidateAlreadyProfiled
	}
	var profileIDCount int64
	if err := tx.Model(&CandidateProfile{}).Where("profile_id = ?", profileID).Count(&profileIDCount).Error; err != nil {
		return nil, err
	}
	if profileIDCount != 0 {
		return nil, ErrCandidateProfileIDConflict
	}
	observedAt := time.UnixMilli(run.ObservedAt)
	if run.ObservedAt <= 0 {
		observedAt = run.CapturedAt
	}
	_, _, err := upsertCandidateSnapshotTx(tx, SelectCandidateProfileRequest{
		ProfileID: profileID, Scope: scope, DisplayName: run.DisplayName,
		PositionTitle: run.PositionTitle, ObservedAt: observedAt,
	})
	if err != nil {
		return nil, err
	}
	profile := &CandidateProfile{
		ProfileID: profileID, Platform: run.Platform, AccountRef: run.AccountRef,
		PlatformUserRef: run.PlatformUserRef, PositionRef: run.PositionRef,
		PositionTitle: run.PositionTitle, MainStatus: CandidateProfileSelected,
		ResumeCaptureState: ResumeCaptureUnattempted, CreatedAt: at, UpdatedAt: at,
	}
	if err := tx.Create(profile).Error; err != nil {
		return nil, err
	}
	return profile, nil
}
