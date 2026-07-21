package store

import (
	"bytes"
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
	ErrProfileAIContextBindingInvalid  = errors.New("职位 AI 上下文绑定无效")
	ErrProfileAIContextBindingConflict = errors.New("职位 AI 上下文绑定冲突")
	ErrM5TrialProfileMismatch          = errors.New("目标档案不是当前 M5 试运行档案")
)

const profileAIContextReboundReason = "contextRebound"

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

func (s *Store) saveJobAIContextRevisions(inputs []m5ai.ContextRevision) ([]saveJobAIContextRevisionResult, error) {
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
	results := make([]saveJobAIContextRevisionResult, len(wanted))
	err := s.db.Transaction(func(tx *gorm.DB) error {
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
