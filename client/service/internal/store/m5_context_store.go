package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"recruithelper/client/service/internal/m5ai"

	"gorm.io/gorm"
)

var (
	ErrJobAIContextRevisionInvalid     = errors.New("职位 AI 上下文 revision 无效")
	ErrJobAIContextRevisionConflict    = errors.New("同一职位 AI 上下文 revision 的材料冲突")
	ErrJobAIContextRevisionNotFound    = errors.New("职位 AI 上下文 revision 不存在")
	ErrJobAIContextHeadInvalid         = errors.New("职位 AI 上下文 current head 无效")
	ErrProfileAIContextBindingInvalid  = errors.New("职位 AI 上下文绑定无效")
	ErrProfileAIContextBindingConflict = errors.New("职位 AI 上下文绑定冲突")
	ErrM5TrialProfileMismatch          = errors.New("目标档案不是当前 M5 试运行档案")
)

const profileAIContextReboundReason = "contextRebound"
const legacyJobConfigSourceKind = "legacyJobConfig"

const (
	sourcingProfileAIContextBindingDomain = "profile-ai-context-sourcing-v1|"
	sourcingProfileAIContextBindingReason = "sourcingGreeting"
	sourcingProfileAIContextBoundBy       = "system:sourcing"
)

// JobAIContextRevisionSummary 是管理列表可用的元数据投影。它不返回 prompt、
// 客户事实或完整来源包，更不联结 Candidate/Profile 的平台身份字段。
type JobAIContextRevisionSummary struct {
	ContextID      string
	RevisionHash   string
	SourceKind     string
	SourceJobRef   string
	DisplayName    string
	Environment    string
	MappingVersion string
	DocumentCount  int
	CreatedAt      time.Time
}

type BindProfileAIContextRequest struct {
	BindingID    string
	ProfileID    string
	ContextID    string
	RevisionHash string
	Reason       string
	BoundBy      string
	BoundAt      time.Time
}

type ActiveProfileAIContext struct {
	Binding  ProfileAIContextBinding
	Revision JobAIContextRevision
}

type saveJobAIContextRevisionResult struct {
	Revision JobAIContextRevision
	Created  bool
}

// SaveJobAIContextRevision 只接收 m5ai importer 已形成的仓库自有类型。
// Store 不复制 importer 的 canonical hash 算法；它验证当前可执行映射，
// 并保证同一 revisionHash 的材料只能首次写入、以后只能字节等值复用。
func (s *Store) SaveJobAIContextRevision(input m5ai.ContextRevision) (*JobAIContextRevision, bool, error) {
	results, err := s.saveJobAIContextRevisions([]m5ai.ContextRevision{input})
	if err != nil {
		return nil, false, err
	}
	return &results[0].Revision, results[0].Created, nil
}

// SaveJobAIContextRevisions 原子收编一次单数或复数 job-config 导入。
// 任一项无效或与已有不可变 revision 冲突时，整批不留下部分结果。
func (s *Store) SaveJobAIContextRevisions(inputs []m5ai.ContextRevision) ([]JobAIContextRevision, error) {
	results, err := s.saveJobAIContextRevisions(inputs)
	if err != nil {
		return nil, err
	}
	revisions := make([]JobAIContextRevision, len(results))
	for index := range results {
		revisions[index] = results[index].Revision
	}
	return revisions, nil
}

// SaveCurrentLegacyJobAIContext 原子保存旧后台 /client/job-config 的不可变
// revision，并推进该 Job.ID 的最近成功同步 head。该接口只接受能唯一确定一个
// 当前职位的 legacyJobConfig 输入；localImport 和复数响应都不能推进 head。
func (s *Store) SaveCurrentLegacyJobAIContext(
	inputs []m5ai.ContextRevision,
	syncedAt time.Time,
) ([]JobAIContextRevision, error) {
	if len(inputs) != 1 || syncedAt.IsZero() {
		return nil, ErrJobAIContextHeadInvalid
	}
	input := inputs[0]
	if input.SourceKind != legacyJobConfigSourceKind || strings.TrimSpace(input.SourceJobRef) == "" {
		return nil, ErrJobAIContextHeadInvalid
	}
	wanted, err := jobAIContextRevisionsFromInputs(inputs)
	if err != nil {
		return nil, err
	}
	results := make([]saveJobAIContextRevisionResult, len(wanted))
	err = s.db.Transaction(func(tx *gorm.DB) error {
		for index := range wanted {
			persisted, created, saveErr := saveJobAIContextRevisionTx(tx, wanted[index])
			if saveErr != nil {
				return saveErr
			}
			results[index] = saveJobAIContextRevisionResult{Revision: persisted, Created: created}
		}
		revision := results[0].Revision
		if err := tx.Model(&JobAIContextHead{}).
			Where(
				"source_kind = ? AND activation_current = ?",
				legacyJobConfigSourceKind,
				true,
			).
			Updates(map[string]any{
				"activation_current": false,
				"updated_at":         syncedAt,
			}).Error; err != nil {
			return err
		}
		var head JobAIContextHead
		headErr := tx.First(
			&head,
			"source_kind = ? AND source_job_ref = ?",
			legacyJobConfigSourceKind,
			revision.SourceJobRef,
		).Error
		switch {
		case errors.Is(headErr, gorm.ErrRecordNotFound):
			head = JobAIContextHead{
				SourceKind: legacyJobConfigSourceKind, SourceJobRef: revision.SourceJobRef,
				ContextID: revision.ContextID, RevisionHash: revision.RevisionHash,
				ActivationCurrent: true, LastSyncedAt: syncedAt,
			}
			return tx.Create(&head).Error
		case headErr != nil:
			return headErr
		default:
			return tx.Model(&head).Updates(map[string]any{
				"context_id": revision.ContextID, "revision_hash": revision.RevisionHash,
				"activation_current": true,
				"last_synced_at":     syncedAt,
				"updated_at":         syncedAt,
			}).Error
		}
	})
	if err != nil {
		return nil, err
	}
	revisions := make([]JobAIContextRevision, len(results))
	for index := range results {
		revisions[index] = results[index].Revision
	}
	return revisions, nil
}

// InvalidateCurrentLegacyJobAIContext closes the automatic position-binding
// eligibility of the previous activation before a newly bound customer is
// allowed to reuse any synchronized job. Immutable revisions and historical
// per-job heads remain intact; a later successful /client/job-config sync
// promotes exactly one head again through SaveCurrentLegacyJobAIContext.
func (s *Store) InvalidateCurrentLegacyJobAIContext(at time.Time) error {
	if s == nil || s.db == nil || at.IsZero() {
		return ErrJobAIContextHeadInvalid
	}
	return s.db.Model(&JobAIContextHead{}).
		Where(
			"source_kind = ? AND activation_current = ?",
			legacyJobConfigSourceKind,
			true,
		).
		Updates(map[string]any{
			"activation_current": false,
			"updated_at":         at,
		}).Error
}

// backfillLegacyJobConfigActivationCurrent is a one-time upgrade bridge for
// databases created before ActivationCurrent existed. The previous product
// projection defined the most recently synchronized head as current, so that
// single deterministic winner keeps its qualification. A tied maximum is
// deliberately left with no qualified head until the next successful sync.
func backfillLegacyJobConfigActivationCurrent(tx *gorm.DB) error {
	if tx == nil {
		return ErrJobAIContextHeadInvalid
	}
	var heads []JobAIContextHead
	if err := tx.Where("source_kind = ?", legacyJobConfigSourceKind).
		Order("last_synced_at DESC, source_job_ref ASC").
		Limit(2).
		Find(&heads).Error; err != nil {
		return err
	}
	if len(heads) == 0 ||
		(len(heads) > 1 && heads[0].LastSyncedAt.Equal(heads[1].LastSyncedAt)) {
		return nil
	}
	return tx.Model(&JobAIContextHead{}).
		Where(
			"source_kind = ? AND source_job_ref = ?",
			legacyJobConfigSourceKind,
			heads[0].SourceJobRef,
		).
		UpdateColumn("activation_current", true).Error
}

// CurrentLegacyJobAIContextByBackendJobID 返回旧后台职位最近一次成功同步的
// revision。没有成功同步过时返回 nil；head 损坏或串到其他职位时 fail-closed。
func (s *Store) CurrentLegacyJobAIContextByBackendJobID(
	backendJobID string,
) (*JobAIContextRevision, error) {
	return currentLegacyJobAIContextByBackendJobIDTx(s.db, backendJobID)
}

func currentLegacyJobAIContextByBackendJobIDTx(
	tx *gorm.DB,
	backendJobID string,
) (*JobAIContextRevision, error) {
	backendJobID = strings.TrimSpace(backendJobID)
	if tx == nil || backendJobID == "" {
		return nil, ErrJobAIContextHeadInvalid
	}
	var head JobAIContextHead
	err := tx.First(
		&head,
		"source_kind = ? AND source_job_ref = ?",
		legacyJobConfigSourceKind,
		backendJobID,
	).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var revision JobAIContextRevision
	if err := tx.First(&revision, "revision_hash = ?", head.RevisionHash).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrJobAIContextRevisionNotFound
		}
		return nil, err
	}
	if revision.SourceKind != legacyJobConfigSourceKind ||
		revision.SourceJobRef != backendJobID ||
		revision.ContextID != head.ContextID {
		return nil, ErrJobAIContextHeadInvalid
	}
	return &revision, nil
}

func (s *Store) saveJobAIContextRevisions(inputs []m5ai.ContextRevision) ([]saveJobAIContextRevisionResult, error) {
	wanted, err := jobAIContextRevisionsFromInputs(inputs)
	if err != nil {
		return nil, err
	}
	results := make([]saveJobAIContextRevisionResult, len(wanted))
	err = s.db.Transaction(func(tx *gorm.DB) error {
		for index := range wanted {
			persisted, created, err := saveJobAIContextRevisionTx(tx, wanted[index])
			if err != nil {
				return err
			}
			results[index] = saveJobAIContextRevisionResult{Revision: persisted, Created: created}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

func jobAIContextRevisionsFromInputs(inputs []m5ai.ContextRevision) ([]JobAIContextRevision, error) {
	if len(inputs) == 0 {
		return nil, ErrJobAIContextRevisionInvalid
	}
	wanted := make([]JobAIContextRevision, len(inputs))
	for index := range inputs {
		input := inputs[index]
		if err := validateJobAIContextRevision(input); err != nil {
			return nil, err
		}
		wanted[index] = JobAIContextRevision{
			RevisionHash:  input.RevisionHash,
			ContextID:     input.ContextID,
			SourceKind:    input.SourceKind,
			SourceJobRef:  input.SourceJobRef,
			DisplayName:   input.DisplayName,
			Environment:   input.Environment,
			SourcePackage: input.SourcePackage,
			Communication: input.Communication,
			CreatedAt:     input.CreatedAt,
		}
	}
	return wanted, nil
}

func saveJobAIContextRevisionTx(tx *gorm.DB, wanted JobAIContextRevision) (JobAIContextRevision, bool, error) {
	var existing JobAIContextRevision
	err := tx.First(&existing, "revision_hash = ?", wanted.RevisionHash).Error
	if err == nil {
		if !sameJobAIContextRevisionMaterial(existing, wanted) {
			return JobAIContextRevision{}, false, ErrJobAIContextRevisionConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return JobAIContextRevision{}, false, err
	}
	if err := tx.Create(&wanted).Error; err != nil {
		return JobAIContextRevision{}, false, err
	}
	return wanted, true, nil
}

func validateJobAIContextRevision(input m5ai.ContextRevision) error {
	if strings.TrimSpace(input.ContextID) == "" || strings.TrimSpace(input.RevisionHash) == "" ||
		strings.TrimSpace(input.SourceKind) == "" || strings.TrimSpace(input.DisplayName) == "" ||
		input.CreatedAt.IsZero() || input.Communication.MappingVersion != m5ai.MappingVersion ||
		strings.TrimSpace(input.Communication.ReplyPrompt) == "" ||
		strings.TrimSpace(input.Communication.IntentPrompt) == "" || len(input.SourcePackage.Documents) == 0 {
		return ErrJobAIContextRevisionInvalid
	}
	documents := input.SourcePackage.Documents
	byType := make(map[string]string, len(documents))
	for index, document := range documents {
		if strings.TrimSpace(document.DocType) == "" || (index > 0 && documents[index-1].DocType >= document.DocType) {
			return ErrJobAIContextRevisionInvalid
		}
		byType[document.DocType] = document.Content
	}
	if byType["多轮沟通"] != input.Communication.ReplyPrompt ||
		byType["意向判断"] != input.Communication.IntentPrompt ||
		byType["客户事实库"] != input.Communication.CustomerFacts {
		return ErrJobAIContextRevisionInvalid
	}
	return nil
}

func sameJobAIContextRevisionMaterial(left, right JobAIContextRevision) bool {
	if left.RevisionHash != right.RevisionHash || left.ContextID != right.ContextID ||
		left.SourceKind != right.SourceKind || left.SourceJobRef != right.SourceJobRef ||
		left.DisplayName != right.DisplayName || left.Environment != right.Environment {
		return false
	}
	leftPackage, leftPackageErr := json.Marshal(left.SourcePackage)
	rightPackage, rightPackageErr := json.Marshal(right.SourcePackage)
	leftView, leftViewErr := json.Marshal(left.Communication)
	rightView, rightViewErr := json.Marshal(right.Communication)
	return leftPackageErr == nil && rightPackageErr == nil && leftViewErr == nil && rightViewErr == nil &&
		bytes.Equal(leftPackage, rightPackage) && bytes.Equal(leftView, rightView)
}

func (s *Store) JobAIContextRevisionByHash(revisionHash string) (*JobAIContextRevision, error) {
	if strings.TrimSpace(revisionHash) == "" {
		return nil, ErrJobAIContextRevisionInvalid
	}
	var revision JobAIContextRevision
	err := s.db.First(&revision, "revision_hash = ?", revisionHash).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &revision, nil
}

func (s *Store) JobAIContextRevisionSummaries() ([]JobAIContextRevisionSummary, error) {
	var revisions []JobAIContextRevision
	if err := s.db.Order("context_id, created_at, revision_hash").Find(&revisions).Error; err != nil {
		return nil, err
	}
	out := make([]JobAIContextRevisionSummary, len(revisions))
	for index := range revisions {
		revision := revisions[index]
		out[index] = JobAIContextRevisionSummary{
			ContextID:      revision.ContextID,
			RevisionHash:   revision.RevisionHash,
			SourceKind:     revision.SourceKind,
			SourceJobRef:   revision.SourceJobRef,
			DisplayName:    revision.DisplayName,
			Environment:    revision.Environment,
			MappingVersion: revision.Communication.MappingVersion,
			DocumentCount:  len(revision.SourcePackage.Documents),
			CreatedAt:      revision.CreatedAt,
		}
	}
	return out, nil
}

// BindActiveM5TrialProfileAIContext 是本阶段唯一绑定写入口。它要求目标
// profile 正是当前 active M5 试运行选择，禁止按职位标题或旧 job id 猜测。
func (s *Store) BindActiveM5TrialProfileAIContext(req BindProfileAIContextRequest) (*ProfileAIContextBinding, error) {
	if strings.TrimSpace(req.BindingID) == "" || strings.TrimSpace(req.ProfileID) == "" ||
		strings.TrimSpace(req.ContextID) == "" || strings.TrimSpace(req.RevisionHash) == "" ||
		strings.TrimSpace(req.Reason) == "" || strings.TrimSpace(req.BoundBy) == "" || req.BoundAt.IsZero() {
		return nil, ErrProfileAIContextBindingInvalid
	}
	var out ProfileAIContextBinding
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var selection M5TrialSelection
		if err := tx.First(&selection, "status = ? AND active_slot = ?", M5TrialSelectionActive, m5TrialActiveSlot).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrM5TrialNotActive
			}
			return err
		}
		if selection.ProfileID != req.ProfileID {
			return ErrM5TrialProfileMismatch
		}
		var profile CandidateProfile
		if err := tx.First(&profile, "profile_id = ?", req.ProfileID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCandidateProfileNotFound
			}
			return err
		}
		var revision JobAIContextRevision
		if err := tx.First(&revision, "revision_hash = ?", req.RevisionHash).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrJobAIContextRevisionNotFound
			}
			return err
		}
		if revision.ContextID != req.ContextID {
			return ErrJobAIContextRevisionConflict
		}

		var sameID ProfileAIContextBinding
		sameIDErr := tx.First(&sameID, "binding_id = ?", req.BindingID).Error
		if sameIDErr == nil {
			if sameID.Status != ProfileAIContextBindingActive || sameID.ProfileID != req.ProfileID ||
				sameID.ContextID != req.ContextID || sameID.RevisionHash != req.RevisionHash {
				return ErrProfileAIContextBindingConflict
			}
			out = sameID
			return nil
		}
		if !errors.Is(sameIDErr, gorm.ErrRecordNotFound) {
			return sameIDErr
		}

		var current ProfileAIContextBinding
		currentErr := tx.First(&current, "profile_id = ? AND status = ?", req.ProfileID, ProfileAIContextBindingActive).Error
		if currentErr == nil {
			if current.ContextID == req.ContextID && current.RevisionHash == req.RevisionHash {
				out = current
				return nil
			}
			if req.BoundAt.Before(current.BoundAt) {
				return ErrProfileAIContextBindingConflict
			}
			updated := tx.Model(&ProfileAIContextBinding{}).
				Where("binding_id = ? AND status = ?", current.BindingID, ProfileAIContextBindingActive).
				Updates(map[string]any{
					"status":        ProfileAIContextBindingSuperseded,
					"reason":        profileAIContextReboundReason,
					"superseded_at": req.BoundAt,
				})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrProfileAIContextBindingConflict
			}
		} else if !errors.Is(currentErr, gorm.ErrRecordNotFound) {
			return currentErr
		}

		out = ProfileAIContextBinding{
			BindingID:    req.BindingID,
			ProfileID:    req.ProfileID,
			ContextID:    req.ContextID,
			RevisionHash: req.RevisionHash,
			Status:       ProfileAIContextBindingActive,
			Reason:       req.Reason,
			BoundBy:      req.BoundBy,
			BoundAt:      req.BoundAt,
		}
		if err := tx.Create(&out).Error; err != nil {
			return ErrProfileAIContextBindingConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// BindSourcingProfileAIContext binds the exact context revision that produced
// this profile's successful M6 greeting. It cannot guess by job title, current
// account config or a newer revision, and it never supersedes a conflicting
// human/legacy binding.
func (s *Store) BindSourcingProfileAIContext(
	profileID string,
	boundAt time.Time,
) (*ProfileAIContextBinding, bool, error) {
	if strings.TrimSpace(profileID) == "" {
		return nil, false, ErrProfileAIContextBindingInvalid
	}
	if boundAt.IsZero() {
		boundAt = time.Now()
	}
	boundAt = boundAt.UTC()
	var out ProfileAIContextBinding
	created := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		aggregate, err := communicationV4AggregateTx(tx, profileID)
		if err != nil {
			return err
		}
		var invocation SourcingGreetingInvocation
		if err := tx.First(&invocation, "profile_id = ?", profileID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrProfileAIContextBindingInvalid
			}
			return err
		}
		source := SourcingGreetingEffectSource{
			BatchID: invocation.BatchID, InvocationID: invocation.InvocationID,
		}
		material, err := loadSourcingGreetingEffectMaterialTx(tx, source)
		if err != nil {
			return err
		}
		if err := validateSourcingGreetingGenerationMaterial(material, source); err != nil ||
			invocation.EffectIntentID == nil ||
			*invocation.EffectIntentID != aggregate.RootGreetingIntentID ||
			material.Profile.SuccessfulGreetingIntentID == nil ||
			*material.Profile.SuccessfulGreetingIntentID != aggregate.RootGreetingIntentID {
			return ErrProfileAIContextBindingConflict
		}
		var revision JobAIContextRevision
		if err := tx.First(&revision, "revision_hash = ?", invocation.ContextRevisionHash).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrJobAIContextRevisionNotFound
			}
			return err
		}
		bindingID := sourcingProfileAIContextBindingID(profileID, revision.RevisionHash)

		var current ProfileAIContextBinding
		currentErr := tx.First(
			&current, "profile_id = ? AND status = ?", profileID, ProfileAIContextBindingActive,
		).Error
		if currentErr == nil {
			if current.ContextID != revision.ContextID || current.RevisionHash != revision.RevisionHash {
				return ErrProfileAIContextBindingConflict
			}
			out = current
			return nil
		}
		if !errors.Is(currentErr, gorm.ErrRecordNotFound) {
			return currentErr
		}

		var sameID ProfileAIContextBinding
		sameIDErr := tx.First(&sameID, "binding_id = ?", bindingID).Error
		if sameIDErr == nil {
			if sameID.ProfileID != profileID || sameID.ContextID != revision.ContextID ||
				sameID.RevisionHash != revision.RevisionHash ||
				sameID.Status != ProfileAIContextBindingActive {
				return ErrProfileAIContextBindingConflict
			}
			out = sameID
			return nil
		}
		if !errors.Is(sameIDErr, gorm.ErrRecordNotFound) {
			return sameIDErr
		}

		out = ProfileAIContextBinding{
			BindingID: bindingID, ProfileID: profileID,
			ContextID: revision.ContextID, RevisionHash: revision.RevisionHash,
			Status: ProfileAIContextBindingActive,
			Reason: sourcingProfileAIContextBindingReason, BoundBy: sourcingProfileAIContextBoundBy,
			BoundAt: boundAt,
		}
		if err := tx.Create(&out).Error; err != nil {
			return ErrProfileAIContextBindingConflict
		}
		created = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return &out, created, nil
}

func (s *Store) SourcingProfileIDsNeedingAIContextForAccount(key AccountKey) ([]string, error) {
	if strings.TrimSpace(key.Platform) == "" || strings.TrimSpace(key.AccountRef) == "" {
		return nil, ErrProfileAIContextBindingInvalid
	}
	var profileIDs []string
	err := s.db.Table("candidate_profiles AS p").
		Select("p.profile_id").
		Joins("JOIN communication_v4_aggregates AS v4 ON v4.profile_id = p.profile_id").
		Joins("JOIN sourcing_greeting_invocations AS gi ON gi.profile_id = p.profile_id AND gi.effect_intent_id = p.successful_greeting_intent_id").
		Joins("LEFT JOIN profile_ai_context_bindings AS b ON b.profile_id = p.profile_id AND b.status = ?", ProfileAIContextBindingActive).
		Where(
			"p.platform = ? AND p.account_ref = ? AND p.main_status IN ? AND p.end_reason IS NULL "+
				"AND gi.status = ? AND gi.finished_at IS NOT NULL AND (b.binding_id IS NULL OR b.revision_hash <> gi.context_revision_hash)",
			key.Platform, key.AccountRef,
			[]CandidateProfileStatus{CandidateProfileGreeted, CandidateProfileCommunicating, CandidateProfileInvited, CandidateProfileInterviewed},
			AIInvocationOK,
		).
		Order("p.profile_id").
		Scan(&profileIDs).Error
	if err != nil {
		return nil, err
	}
	return profileIDs, nil
}

func sourcingProfileAIContextBindingID(profileID, revisionHash string) string {
	digest := sha256.Sum256([]byte(
		sourcingProfileAIContextBindingDomain + profileID + "|" + revisionHash,
	))
	return "binding-sr1-" + hex.EncodeToString(digest[:])
}

func (s *Store) ActiveProfileAIContext(profileID string) (*ActiveProfileAIContext, error) {
	if strings.TrimSpace(profileID) == "" {
		return nil, ErrProfileAIContextBindingInvalid
	}
	var out *ActiveProfileAIContext
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var binding ProfileAIContextBinding
		err := tx.First(&binding, "profile_id = ? AND status = ?", profileID, ProfileAIContextBindingActive).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var revision JobAIContextRevision
		if err := tx.First(&revision, "revision_hash = ?", binding.RevisionHash).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrJobAIContextRevisionNotFound
			}
			return err
		}
		if revision.ContextID != binding.ContextID {
			return ErrProfileAIContextBindingConflict
		}
		out = &ActiveProfileAIContext{Binding: binding, Revision: revision}
		return nil
	})
	return out, err
}

func (s *Store) ProfileAIContextBindings(profileID string) ([]ProfileAIContextBinding, error) {
	if strings.TrimSpace(profileID) == "" {
		return nil, ErrProfileAIContextBindingInvalid
	}
	var bindings []ProfileAIContextBinding
	err := s.db.Where("profile_id = ?", profileID).Order("bound_at, binding_id").Find(&bindings).Error
	return bindings, err
}
