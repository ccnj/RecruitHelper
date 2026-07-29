package store

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"

	"gorm.io/gorm"
)

const SourcingSelectionAlgorithmVersion = "selection-target-v1"

var (
	ErrSourcingSelectionNotReady = errors.New("正式采集批次尚未形成完整评分")
	ErrSourcingSelectionConflict = errors.New("正式采集批次筛选材料冲突")
)

type sourcingSelectionGender uint8

const (
	sourcingGenderUnknown sourcingSelectionGender = iota
	sourcingGenderFemale
	sourcingGenderMale
)

type sourcingSelectionRow struct {
	Run        SourcingCandidateRun
	Invocation SourcingScoreInvocation
	Gender     sourcingSelectionGender
}

// SourcingBatchSelectionByBatchID 返回不含候选人身份、逐人分数或 profileId
// 的完整批次摘要。尚未筛选时返回 (nil, nil)。
func (s *Store) SourcingBatchSelectionByBatchID(batchID string) (*SourcingBatchSelection, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return nil, ErrSourcingBatchInvalid
	}
	var selection SourcingBatchSelection
	err := s.db.First(&selection, "batch_id = ?", batchID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &selection, nil
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

// SelectCompletedSourcingBatch 只消费一个已经结束并完成统一评分的正式批次。
// 全员决策、selected 档案和脱敏摘要在同一个 SQLite 事务中提交；已有摘要
// 只能作为一份结构完整的既有结果重放，不会重新计算目标人数或名单。
func (s *Store) SelectCompletedSourcingBatch(batchID string, decidedAt time.Time) (*SourcingBatchSelection, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return nil, ErrSourcingBatchInvalid
	}
	if decidedAt.IsZero() {
		decidedAt = time.Now()
	}
	var out SourcingBatchSelection
	err := s.db.Transaction(func(tx *gorm.DB) error {
		batch, err := validateCompletedSourcingBatchForScoringTx(tx, batchID)
		if err != nil {
			return err
		}

		var existing SourcingBatchSelection
		err = tx.First(&existing, "batch_id = ?", batch.BatchID).Error
		if err == nil {
			if err := validatePersistedSourcingBatchSelectionTx(tx, batch, existing); err != nil {
				return err
			}
			out = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var preexistingDecisions int64
		if err := tx.Table("sourcing_selection_decisions AS decision").
			Joins("JOIN sourcing_candidate_runs AS run ON run.run_id = decision.run_id").
			Where("run.batch_id = ?", batch.BatchID).
			Count(&preexistingDecisions).Error; err != nil {
			return err
		}
		if preexistingDecisions != 0 {
			return ErrSourcingSelectionConflict
		}

		rows, err := loadCompleteSourcingSelectionRowsTx(tx, batch)
		if err != nil {
			return err
		}
		revision, err := currentLegacyRevisionForSourcingBatchTx(tx, batch)
		if err != nil {
			return err
		}
		if revision == nil {
			return ErrJobAIContextRevisionNotFound
		}
		backendJobID, err := sourcingBatchBackendJobID(batch)
		if err != nil {
			return ErrSourcingSelectionConflict
		}
		view, err := m5ai.DeriveSourcingView(revision.SourcePackage)
		if err != nil {
			return err
		}
		selectionView := view.CandidateSelection
		targetCount := stableSourcingSelectionTarget(
			batch.BatchID, revision.RevisionHash, selectionView.TargetMin, selectionView.TargetMax,
		)
		maleLimit := targetCount * selectionView.MaleRatioLimit / 100
		summary := SourcingBatchSelection{
			BatchID: batch.BatchID, ContextRevisionHash: revision.RevisionHash,
			AlgorithmVersion: SourcingSelectionAlgorithmVersion,
			MinScore:         selectionView.MinScore, TargetMin: selectionView.TargetMin,
			TargetMax: selectionView.TargetMax, TargetCount: targetCount,
			MaleRatioLimit: selectionView.MaleRatioLimit, MaleLimit: maleLimit,
			PoolCount: len(rows), CompletedAt: decidedAt, CreatedAt: decidedAt,
		}

		for i := range rows {
			row := rows[i]
			if row.Gender == sourcingGenderUnknown {
				summary.UnknownGenderCount++
			}
			decision := SourcingSelectionDecision{
				RunID: row.Run.RunID, ContextRevisionHash: revision.RevisionHash,
				Score: row.Invocation.Score, MinScore: selectionView.MinScore,
				DecidedAt: decidedAt, CreatedAt: decidedAt,
			}
			switch {
			case row.Invocation.Status != AIInvocationOK || row.Invocation.Score == nil:
				decision.Outcome = SourcingSelectionScoringFailed
			// 只排除 unknown:页面上没有唯一可点的"打招呼"按钮,常见成因是本
			// 账号已经打过、按钮已变"继续沟通",协议规格 §614 明确 unknown 永
			// 不授权 sendGreeting。established 是同事聊过,真机确认其卡片按钮
			// 仍为"打招呼",平台仍允许招呼,故放行(2026-07-29 甲方裁决,见
			// docs/同事聊过候选人放行裁决-2026-07-29.md)。
			case row.Run.ContactState == string(protocol.CandidateContactStateUnknown):
				decision.Outcome = SourcingSelectionContactStateRejected
			case *row.Invocation.Score < selectionView.MinScore:
				decision.Outcome = SourcingSelectionScoreBelowThreshold
			default:
				occupied, err := sourcingCandidateAlreadyProfiledTx(tx, row.Run)
				if err != nil {
					return err
				}
				if occupied {
					decision.Outcome = SourcingSelectionExistingProfile
					break
				}
				summary.EligibleCount++
				if summary.SelectedCount >= targetCount {
					decision.Outcome = SourcingSelectionQuotaFull
					break
				}
				if row.Gender == sourcingGenderMale && summary.MaleSelectedCount >= maleLimit {
					decision.Outcome = SourcingSelectionMaleRatioLimited
					break
				}
				profile, err := createSourcingSelectedProfileTx(
					tx,
					row.Run,
					backendJobID,
					ids.NewProfileID(),
					decidedAt,
				)
				if err != nil {
					return err
				}
				decision.Outcome = SourcingSelectionSelected
				decision.ProfileID = &profile.ProfileID
				summary.SelectedCount++
				if row.Gender == sourcingGenderMale {
					summary.MaleSelectedCount++
				}
			}
			if err := tx.Create(&decision).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(&summary).Error; err != nil {
			return err
		}
		out = summary
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func stableSourcingSelectionTarget(batchID, revisionHash string, targetMin, targetMax int) int {
	if targetMin == targetMax {
		return targetMin
	}
	digest := sha256.Sum256([]byte(
		SourcingSelectionAlgorithmVersion + "|" + batchID + "|" + revisionHash,
	))
	span := uint64(targetMax - targetMin + 1)
	return targetMin + int(binary.BigEndian.Uint64(digest[:8])%span)
}

func loadCompleteSourcingSelectionRowsTx(
	tx *gorm.DB,
	batch SourcingBatch,
) ([]sourcingSelectionRow, error) {
	var runs []SourcingCandidateRun
	if err := tx.Where("batch_id = ?", batch.BatchID).
		Order("captured_at ASC, run_id ASC").Find(&runs).Error; err != nil {
		return nil, err
	}
	if len(runs) != batch.TargetCount {
		return nil, ErrSourcingBatchConflict
	}
	var invocations []SourcingScoreInvocation
	if err := tx.Table("sourcing_score_invocations AS invocation").
		Select("invocation.*").
		Joins("JOIN sourcing_candidate_runs AS run ON run.run_id = invocation.run_id").
		Where("run.batch_id = ?", batch.BatchID).
		Find(&invocations).Error; err != nil {
		return nil, err
	}
	if len(invocations) != len(runs) {
		return nil, ErrSourcingSelectionNotReady
	}
	byRun := make(map[string]SourcingScoreInvocation, len(invocations))
	provider, model := "", ""
	scoringRevisionHash := ""
	for _, invocation := range invocations {
		if _, exists := byRun[invocation.RunID]; exists {
			return nil, ErrSourcingSelectionConflict
		}
		if strings.TrimSpace(invocation.Provider) == "" || strings.TrimSpace(invocation.Model) == "" {
			return nil, ErrSourcingSelectionConflict
		}
		if provider == "" {
			provider, model = invocation.Provider, invocation.Model
		} else if provider != invocation.Provider || model != invocation.Model {
			return nil, ErrSourcingSelectionConflict
		}
		if _, err := requireLegacyRevisionForSourcingBatchTx(
			tx, batch, invocation.ContextRevisionHash,
		); err != nil {
			return nil, ErrSourcingSelectionConflict
		}
		if scoringRevisionHash == "" {
			scoringRevisionHash = invocation.ContextRevisionHash
		} else if scoringRevisionHash != invocation.ContextRevisionHash {
			return nil, ErrSourcingSelectionConflict
		}
		if invocation.FinishedAt == nil {
			if invocation.Status != AIInvocationTransportFailed || invocation.Score != nil {
				return nil, ErrSourcingSelectionConflict
			}
			return nil, ErrSourcingSelectionNotReady
		}
		if invocation.Status == AIInvocationOK {
			if invocation.Score == nil || *invocation.Score < 1 || *invocation.Score > 10 {
				return nil, ErrSourcingSelectionConflict
			}
		} else if invocation.Score != nil {
			return nil, ErrSourcingSelectionConflict
		}
		byRun[invocation.RunID] = invocation
	}

	rows := make([]sourcingSelectionRow, 0, len(runs))
	for _, run := range runs {
		invocation, ok := byRun[run.RunID]
		if !ok {
			return nil, ErrSourcingSelectionNotReady
		}
		if run.BatchID == nil || *run.BatchID != batch.BatchID ||
			run.Platform != batch.Platform || run.AccountRef != batch.AccountRef ||
			run.ContextRevisionHash != batch.ContextRevisionHash ||
			invocation.RunContentHash != run.ContentHash {
			return nil, ErrSourcingSelectionConflict
		}
		gender, err := sourcingSelectionGenderOf(run)
		if err != nil {
			return nil, err
		}
		rows = append(rows, sourcingSelectionRow{Run: run, Invocation: invocation, Gender: gender})
	}
	sort.Slice(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		leftOK := left.Invocation.Status == AIInvocationOK && left.Invocation.Score != nil
		rightOK := right.Invocation.Status == AIInvocationOK && right.Invocation.Score != nil
		if leftOK != rightOK {
			return leftOK
		}
		if leftOK && *left.Invocation.Score != *right.Invocation.Score {
			return *left.Invocation.Score > *right.Invocation.Score
		}
		if !left.Run.CapturedAt.Equal(right.Run.CapturedAt) {
			return left.Run.CapturedAt.Before(right.Run.CapturedAt)
		}
		return left.Run.RunID < right.Run.RunID
	})
	return rows, nil
}

func sourcingSelectionGenderOf(run SourcingCandidateRun) (sourcingSelectionGender, error) {
	var resume struct {
		Basic []protocol.CandidateResumeLabelValue `json:"basic"`
	}
	if err := json.Unmarshal([]byte(run.ResumeJSON), &resume); err != nil {
		return sourcingGenderUnknown, ErrSourcingSelectionConflict
	}
	explicit := false
	gender := sourcingGenderUnknown
	for _, item := range resume.Basic {
		if strings.TrimSpace(item.Label) != "性别" {
			continue
		}
		explicit = true
		var observed sourcingSelectionGender
		switch strings.TrimSpace(item.Value) {
		case "男":
			observed = sourcingGenderMale
		case "女":
			observed = sourcingGenderFemale
		default:
			continue
		}
		if gender != sourcingGenderUnknown && gender != observed {
			return sourcingGenderUnknown, nil
		}
		gender = observed
	}
	if explicit {
		return gender, nil
	}
	if run.DisplayName == nil {
		return sourcingGenderUnknown, nil
	}
	displayName := strings.TrimSpace(*run.DisplayName)
	switch {
	case strings.HasSuffix(displayName, "先生"):
		return sourcingGenderMale, nil
	case strings.HasSuffix(displayName, "女士"):
		return sourcingGenderFemale, nil
	default:
		return sourcingGenderUnknown, nil
	}
}

func sourcingCandidateAlreadyProfiledTx(tx *gorm.DB, run SourcingCandidateRun) (bool, error) {
	scope := CandidateProfileScope{
		Platform: run.Platform, AccountRef: run.AccountRef,
		PlatformUserRef: run.PlatformUserRef, PositionRef: run.PositionRef,
	}
	existing, err := candidateProfileByScopeTx(tx, scope)
	if err != nil {
		return false, err
	}
	if existing != nil {
		return true, nil
	}
	var occupied int64
	if err := tx.Model(&CandidateProfile{}).
		Where("platform = ? AND platform_user_ref = ? AND main_status <> ?",
			run.Platform, run.PlatformUserRef, CandidateProfileEliminated).
		Count(&occupied).Error; err != nil {
		return false, err
	}
	return occupied != 0, nil
}

func validatePersistedSourcingBatchSelectionTx(
	tx *gorm.DB,
	batch SourcingBatch,
	selection SourcingBatchSelection,
) error {
	if selection.BatchID != batch.BatchID ||
		selection.AlgorithmVersion != SourcingSelectionAlgorithmVersion ||
		selection.PoolCount != batch.TargetCount || selection.TargetMin < 0 ||
		selection.TargetMax < selection.TargetMin || selection.TargetCount < selection.TargetMin ||
		selection.TargetCount > selection.TargetMax || selection.MinScore < 1 || selection.MinScore > 10 ||
		selection.MaleRatioLimit < 0 || selection.MaleRatioLimit > 100 ||
		selection.MaleLimit != selection.TargetCount*selection.MaleRatioLimit/100 ||
		selection.EligibleCount < selection.SelectedCount || selection.SelectedCount < 0 ||
		selection.MaleSelectedCount < 0 || selection.MaleSelectedCount > selection.SelectedCount ||
		selection.UnknownGenderCount < 0 || selection.UnknownGenderCount > selection.PoolCount ||
		selection.CompletedAt.IsZero() {
		return ErrSourcingSelectionConflict
	}
	if _, err := requireLegacyRevisionForSourcingBatchTx(
		tx, batch, selection.ContextRevisionHash,
	); err != nil {
		return ErrSourcingSelectionConflict
	}
	type persistedDecision struct {
		RunID               string
		ContextRevisionHash string
		Outcome             SourcingSelectionOutcome
		ProfileID           *string
	}
	var decisions []persistedDecision
	if err := tx.Table("sourcing_selection_decisions AS decision").
		Select("decision.run_id, decision.context_revision_hash, decision.outcome, decision.profile_id").
		Joins("JOIN sourcing_candidate_runs AS run ON run.run_id = decision.run_id").
		Where("run.batch_id = ?", batch.BatchID).
		Find(&decisions).Error; err != nil {
		return err
	}
	if len(decisions) != batch.TargetCount {
		return ErrSourcingSelectionConflict
	}
	selected := 0
	for _, decision := range decisions {
		if decision.ContextRevisionHash != selection.ContextRevisionHash {
			return ErrSourcingSelectionConflict
		}
		switch decision.Outcome {
		case SourcingSelectionSelected:
			if decision.ProfileID == nil || strings.TrimSpace(*decision.ProfileID) == "" {
				return ErrSourcingSelectionConflict
			}
			var profile CandidateProfile
			if err := tx.First(&profile, "profile_id = ?", *decision.ProfileID).Error; err != nil {
				return ErrSourcingSelectionConflict
			}
			selected++
		case SourcingSelectionScoreBelowThreshold, SourcingSelectionContactStateRejected,
			SourcingSelectionScoringFailed, SourcingSelectionExistingProfile,
			SourcingSelectionQuotaFull, SourcingSelectionMaleRatioLimited:
			if decision.ProfileID != nil {
				return ErrSourcingSelectionConflict
			}
		default:
			return ErrSourcingSelectionConflict
		}
	}
	if selected != selection.SelectedCount {
		return ErrSourcingSelectionConflict
	}
	return nil
}

func createSourcingSelectedProfileTx(
	tx *gorm.DB,
	run SourcingCandidateRun,
	backendJobID string,
	profileID string,
	at time.Time,
) (*CandidateProfile, error) {
	backendJobID = strings.TrimSpace(backendJobID)
	if backendJobID == "" {
		return nil, ErrSourcingSelectionConflict
	}
	var account Account
	if err := tx.First(&account, "platform = ? AND account_ref = ?", run.Platform, run.AccountRef).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAccountNotFound
		}
		return nil, err
	}
	if occupied, err := sourcingCandidateAlreadyProfiledTx(tx, run); err != nil {
		return nil, err
	} else if occupied {
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
		ProfileID: profileID, Scope: CandidateProfileScope{
			Platform: run.Platform, AccountRef: run.AccountRef,
			PlatformUserRef: run.PlatformUserRef, PositionRef: run.PositionRef,
		},
		DisplayName: run.DisplayName, PositionTitle: run.PositionTitle, ObservedAt: observedAt,
	})
	if err != nil {
		return nil, err
	}
	profile := &CandidateProfile{
		ProfileID: profileID, Platform: run.Platform, AccountRef: run.AccountRef,
		PlatformUserRef: run.PlatformUserRef, PositionRef: run.PositionRef,
		PositionTitle: run.PositionTitle, BackendJobID: &backendJobID,
		MainStatus:         CandidateProfileSelected,
		ResumeCaptureState: ResumeCaptureUnattempted, CreatedAt: at, UpdatedAt: at,
	}
	if err := tx.Create(profile).Error; err != nil {
		return nil, err
	}
	return profile, nil
}
