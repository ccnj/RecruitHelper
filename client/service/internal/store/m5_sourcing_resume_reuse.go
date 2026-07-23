package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	resumeSnapshotSourceSourcing = "sourcingRun"
	sourcingResumeSnapshotDomain = "m5-sourcing-resume-snapshot-v1|"

	sourcingResumeReuseAuditCategory = "m5_sourcing_resume_reuse_anomaly"
)

type SourcingResumeReuseStatus string

const (
	SourcingResumeReuseUnavailable  SourcingResumeReuseStatus = "unavailable"
	SourcingResumeReuseAdopted      SourcingResumeReuseStatus = "adopted"
	SourcingResumeReuseFreshCapture SourcingResumeReuseStatus = "freshCapture"
)

type SourcingResumeReuseResult struct {
	Status   SourcingResumeReuseStatus
	Snapshot *CandidateResumeSnapshot
}

// ReuseSourcingResumeForActiveM5Trial 把“产生本次成功招呼”的精确 M6 run
// 投影成 M5 的 profile-bound 快照。它不按候选人身份猜最新记录；只有
// SourcingGreetingInvocation 的 profile/run/effect 因果链能授权复用。
//
// run 正文 hash 或 schema 异常只说明这份本地复用材料不可信：事务内留下
// 脱敏审计后返回 freshCapture，让既有 IM 补采重新读取。身份、职位或成功
// 招呼意图不一致则是目标绑定冲突，必须返回 ErrResumeCaptureBinding。
func (s *Store) ReuseSourcingResumeForActiveM5Trial(
	profileID string,
	at time.Time,
) (*SourcingResumeReuseResult, error) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return nil, ErrResumeCaptureBinding
	}
	if at.IsZero() {
		at = time.Now()
	}
	out := &SourcingResumeReuseResult{Status: SourcingResumeReuseUnavailable}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		target, err := eligibleResumeTargetTx(tx, profileID, true)
		if err != nil {
			return err
		}
		profile := target.Profile
		switch profile.ResumeCaptureState {
		case ResumeCaptureCaptured:
			if profile.ActiveResumeSnapshotID == nil {
				return ErrResumeCaptureBinding
			}
			var snapshot CandidateResumeSnapshot
			if err := tx.First(&snapshot, "snapshot_id = ? AND profile_id = ?",
				*profile.ActiveResumeSnapshotID, profile.ProfileID).Error; err != nil {
				return ErrResumeCaptureBinding
			}
			out.Status = SourcingResumeReuseAdopted
			out.Snapshot = &snapshot
			return nil
		case ResumeCaptureUnattempted:
		case ResumeCaptureInFlight, ResumeCaptureManualRequired:
			return ErrResumeCaptureNotAllowed
		default:
			return ErrCandidateProfileState
		}

		var invocation SourcingGreetingInvocation
		err = tx.First(&invocation, "profile_id = ?", profile.ProfileID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if invocation.ProfileID != profile.ProfileID || invocation.EffectIntentID == nil ||
			profile.SuccessfulGreetingIntentID == nil ||
			*invocation.EffectIntentID != *profile.SuccessfulGreetingIntentID ||
			invocation.Status != AIInvocationOK || invocation.FinishedAt == nil {
			return ErrResumeCaptureBinding
		}

		var run SourcingCandidateRun
		if err := tx.First(&run, "run_id = ?", invocation.RunID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrResumeCaptureBinding
			}
			return err
		}
		if run.BatchID == nil || *run.BatchID != invocation.BatchID ||
			run.Platform != profile.Platform || run.AccountRef != profile.AccountRef ||
			run.PlatformUserRef != profile.PlatformUserRef ||
			run.PositionRef != profile.PositionRef ||
			run.ContextRevisionHash != invocation.ContextRevisionHash {
			return ErrResumeCaptureBinding
		}

		if !validSourcingResumeForReuse(run, invocation) {
			if err := appendSourcingResumeReuseAuditTx(
				tx, invocation.InvocationID, "reason=contentOrSchemaMismatch fallback=imCapture", at,
			); err != nil {
				return err
			}
			out.Status = SourcingResumeReuseFreshCapture
			return nil
		}

		snapshot := CandidateResumeSnapshot{
			SnapshotID:              sourcingResumeSnapshotID(run.RunID),
			ProfileID:               profile.ProfileID,
			SourceKind:              resumeSnapshotSourceSourcing,
			SourceConversationRef:   *profile.ConversationRef,
			SourceLogicalDispatchID: run.SourceLogicalDispatchID,
			ObservedAt:              run.ObservedAt,
			CapturedAt:              run.CapturedAt,
			SchemaVersion:           resumeSnapshotSchemaV1,
			ContentHash:             run.ContentHash,
			ResumeJSON:              run.ResumeJSON,
			CreatedAt:               at,
		}
		var existing CandidateResumeSnapshot
		err = tx.First(&existing,
			"source_logical_dispatch_id = ?", run.SourceLogicalDispatchID).Error
		if err == nil {
			if existing.SnapshotID != snapshot.SnapshotID ||
				existing.ProfileID != snapshot.ProfileID ||
				existing.SourceKind != snapshot.SourceKind ||
				existing.SourceConversationRef != snapshot.SourceConversationRef {
				return ErrResumeCaptureBinding
			}
			if existing.ContentHash != snapshot.ContentHash ||
				existing.ResumeJSON != snapshot.ResumeJSON ||
				existing.SchemaVersion != snapshot.SchemaVersion {
				if err := appendSourcingResumeReuseAuditTx(
					tx, invocation.InvocationID, "reason=projectionContentMismatch fallback=imCapture", at,
				); err != nil {
					return err
				}
				out.Status = SourcingResumeReuseFreshCapture
				return nil
			}
			snapshot = existing
		} else {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err := tx.Create(&snapshot).Error; err != nil {
				return err
			}
		}

		updated := tx.Model(&CandidateProfile{}).
			Where("profile_id = ? AND resume_capture_state = ?", profile.ProfileID, ResumeCaptureUnattempted).
			Updates(map[string]any{
				"resume_capture_state":               ResumeCaptureCaptured,
				"resume_capture_logical_dispatch_id": run.SourceLogicalDispatchID,
				"resume_capture_attempted_at":        at,
				"active_resume_snapshot_id":          snapshot.SnapshotID,
				"resume_capture_failure_reason":      "",
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrCandidateProfileState
		}
		out.Status = SourcingResumeReuseAdopted
		out.Snapshot = &snapshot
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func validSourcingResumeForReuse(
	run SourcingCandidateRun,
	invocation SourcingGreetingInvocation,
) bool {
	if run.SchemaVersion != resumeSnapshotSchemaV1 ||
		strings.TrimSpace(run.SourceLogicalDispatchID) == "" ||
		run.ContentHash != invocation.RunContentHash {
		return false
	}
	digest := sha256.Sum256([]byte(run.ResumeJSON))
	if hex.EncodeToString(digest[:]) != run.ContentHash {
		return false
	}
	var canonical canonicalResumeV1
	if json.Unmarshal([]byte(run.ResumeJSON), &canonical) != nil {
		return false
	}
	encoded, err := json.Marshal(canonical)
	return err == nil && string(encoded) == run.ResumeJSON
}

func sourcingResumeSnapshotID(runID string) string {
	digest := sha256.Sum256([]byte(sourcingResumeSnapshotDomain + runID))
	return "resume-sr1-" + hex.EncodeToString(digest[:])
}

func appendSourcingResumeReuseAuditTx(
	tx *gorm.DB,
	invocationID string,
	detail string,
	at time.Time,
) error {
	var existing int64
	if err := tx.Model(&AuditEntry{}).
		Where("category = ? AND ref_msg_id = ?",
			sourcingResumeReuseAuditCategory, invocationID).
		Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	return tx.Create(&AuditEntry{
		At: at, Category: sourcingResumeReuseAuditCategory,
		RefMsgID: invocationID, Detail: detail,
	}).Error
}
