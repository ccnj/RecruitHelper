package store

import (
	"errors"
	"strings"

	"gorm.io/gorm"
)

// backendJobIDBackfillReport 只统计迁移结果，不携带 profile、candidate 或
// 平台身份。无法唯一映射的历史档案保持 NULL，后续配置路由必须 fail-closed。
type backendJobIDBackfillReport struct {
	BatchesFilled      int64
	BatchesUnresolved  int64
	ProfilesFilled     int64
	ProfilesUnresolved int64
	ProfilesAmbiguous  int64
}

func backfillBackendJobIDs(db *gorm.DB) (backendJobIDBackfillReport, error) {
	report := backendJobIDBackfillReport{}
	err := db.Transaction(func(tx *gorm.DB) error {
		var batches []SourcingBatch
		if err := tx.Where("backend_job_id IS NULL OR TRIM(backend_job_id) = ''").
			Order("batch_id").Find(&batches).Error; err != nil {
			return err
		}
		for i := range batches {
			var revision JobAIContextRevision
			if err := tx.First(&revision, "revision_hash = ?", batches[i].ContextRevisionHash).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					report.BatchesUnresolved++
					continue
				}
				return err
			}
			backendJobID := strings.TrimSpace(revision.SourceJobRef)
			if backendJobID == "" {
				report.BatchesUnresolved++
				continue
			}
			updated := tx.Model(&SourcingBatch{}).
				Where("batch_id = ? AND (backend_job_id IS NULL OR TRIM(backend_job_id) = '')", batches[i].BatchID).
				UpdateColumn("backend_job_id", backendJobID)
			if updated.Error != nil {
				return updated.Error
			}
			report.BatchesFilled += updated.RowsAffected
		}

		var profiles []CandidateProfile
		if err := tx.Where("backend_job_id IS NULL OR TRIM(backend_job_id) = ''").
			Order("profile_id").Find(&profiles).Error; err != nil {
			return err
		}
		for i := range profiles {
			var candidates []struct {
				BackendJobID string
			}
			if err := tx.Raw(`
				SELECT DISTINCT TRIM(revision.source_job_ref) AS backend_job_id
				FROM job_ai_context_revisions AS revision
				JOIN (
					SELECT binding.revision_hash AS revision_hash
					FROM profile_ai_context_bindings AS binding
					WHERE binding.profile_id = ?
					UNION
					SELECT greeting.context_revision_hash AS revision_hash
					FROM sourcing_greeting_invocations AS greeting
					WHERE greeting.profile_id = ?
					UNION
					SELECT decision.context_revision_hash AS revision_hash
					FROM sourcing_selection_decisions AS decision
					WHERE decision.profile_id = ?
					UNION
					SELECT turn.context_revision_hash AS revision_hash
					FROM dialogue_turns AS turn
					WHERE turn.profile_id = ?
				) AS source
				ON source.revision_hash = revision.revision_hash
				WHERE TRIM(revision.source_job_ref) <> ''
				ORDER BY backend_job_id
			`, profiles[i].ProfileID, profiles[i].ProfileID, profiles[i].ProfileID, profiles[i].ProfileID).
				Scan(&candidates).Error; err != nil {
				return err
			}
			switch len(candidates) {
			case 0:
				report.ProfilesUnresolved++
			case 1:
				updated := tx.Model(&CandidateProfile{}).
					Where("profile_id = ? AND (backend_job_id IS NULL OR TRIM(backend_job_id) = '')", profiles[i].ProfileID).
					UpdateColumn("backend_job_id", candidates[0].BackendJobID)
				if updated.Error != nil {
					return updated.Error
				}
				report.ProfilesFilled += updated.RowsAffected
			default:
				report.ProfilesAmbiguous++
			}
		}
		return nil
	})
	return report, err
}
