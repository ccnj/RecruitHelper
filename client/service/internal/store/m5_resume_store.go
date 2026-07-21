package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"recruithelper/contract/gen/go/protocol"

	"gorm.io/gorm"
)

const (
	m5TrialActiveSlot      = "m5-single-profile"
	resumeSnapshotSourceIM = "imConversation"
	resumeSnapshotSchemaV1 = 1
)

var (
	ErrM5TrialAlreadyActive    = errors.New("已有另一个 M5 试运行档案")
	ErrM5TrialNotActive        = errors.New("M5 试运行未激活")
	ErrResumeCaptureNotAllowed = errors.New("当前档案不允许补采简历")
	ErrResumeCaptureConflict   = errors.New("同一简历补采逻辑返回冲突内容")
	ErrResumeCaptureBinding    = errors.New("简历补采目标绑定已变化")
)

type ResumeCaptureTarget struct {
	Selection    M5TrialSelection
	Profile      CandidateProfile
	Account      Account
	Conversation Conversation
}

func (s *Store) SelectM5TrialProfile(profileID, selectionID, selectedBy string, at time.Time) (*M5TrialSelection, error) {
	if profileID == "" || selectionID == "" {
		return nil, errors.New("M5 试运行选择缺少 profileId/selectionId")
	}
	if selectedBy == "" {
		selectedBy = "user"
	}
	if at.IsZero() {
		at = time.Now()
	}
	var out M5TrialSelection
	err := s.db.Transaction(func(tx *gorm.DB) error {
		target, err := eligibleResumeTargetTx(tx, profileID, false)
		if err != nil {
			return err
		}
		var active M5TrialSelection
		err = tx.First(&active, "status = ? AND active_slot = ?", M5TrialSelectionActive, m5TrialActiveSlot).Error
		if err == nil {
			if active.ProfileID != profileID {
				return ErrM5TrialAlreadyActive
			}
			out = active
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		// 自动补采失败是本里程碑的终局事实，当前没有 reset/cancel 入口。
		// 只有从未尝试过的档案可以新占 active slot；既有同档案 active
		// 选择仍由上方幂等返回，供捕获成功后的 2B 继续复用。
		if target.Profile.ResumeCaptureState != ResumeCaptureUnattempted {
			return ErrResumeCaptureNotAllowed
		}
		slot := m5TrialActiveSlot
		out = M5TrialSelection{
			SelectionID: selectionID, ProfileID: profileID, Status: M5TrialSelectionActive,
			ActiveSlot: &slot, SelectedBy: selectedBy, SelectedAt: at,
		}
		return tx.Create(&out).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) CandidateProfileByConversation(key ConversationKey) (*CandidateProfile, error) {
	var profile CandidateProfile
	err := s.db.First(&profile,
		"platform = ? AND account_ref = ? AND conversation_ref = ?",
		key.Platform, key.AccountRef, key.ConversationRef).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &profile, nil
}

func (s *Store) ActiveM5TrialForAccount(key AccountKey) (*ResumeCaptureTarget, error) {
	var out *ResumeCaptureTarget
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var selection M5TrialSelection
		err := tx.First(&selection, "status = ? AND active_slot = ?", M5TrialSelectionActive, m5TrialActiveSlot).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := eligibleResumeTargetTx(tx, selection.ProfileID, true)
		if err != nil {
			return err
		}
		if target.Account.Platform != key.Platform || target.Account.AccountRef != key.AccountRef {
			return nil
		}
		target.Selection = selection
		out = target
		return nil
	})
	return out, err
}

func eligibleResumeTargetTx(tx *gorm.DB, profileID string, requireActive bool) (*ResumeCaptureTarget, error) {
	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", profileID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrCandidateProfileNotFound
		}
		return nil, err
	}
	statusAllowed := profile.MainStatus == CandidateProfileGreeted
	if requireActive {
		// 首次选择仍只接受 greeted；既有 active 试运行观察到真实候选人
		// 消息后会进入 communicating，后续补采与结果收编必须继续认得它。
		statusAllowed = statusAllowed || profile.MainStatus == CandidateProfileCommunicating
	}
	if !statusAllowed || profile.EndReason != nil ||
		profile.ConversationRef == nil || *profile.ConversationRef == "" ||
		profile.SuccessfulGreetingIntentID == nil || *profile.SuccessfulGreetingIntentID == "" {
		return nil, ErrResumeCaptureNotAllowed
	}
	var account Account
	if err := tx.First(&account, "platform = ? AND account_ref = ?", profile.Platform, profile.AccountRef).Error; err != nil {
		return nil, err
	}
	key := ConversationKey{Platform: profile.Platform, AccountRef: profile.AccountRef, ConversationRef: *profile.ConversationRef}
	var conversation Conversation
	if err := tx.Where(conversationWhere(key), conversationArgs(key)...).First(&conversation).Error; err != nil {
		return nil, err
	}
	var tracked TrackedIntent
	if err := tx.Where(conversationWhere(key), conversationArgs(key)...).First(&tracked).Error; err != nil {
		return nil, err
	}
	if conversation.TrackingState != TrackingAdopted || tracked.Status != TrackingAdopted ||
		conversation.PlatformUserRef != profile.PlatformUserRef {
		return nil, ErrResumeCaptureBinding
	}
	var greeting EffectIntent
	if err := tx.First(&greeting, "intent_id = ?", *profile.SuccessfulGreetingIntentID).Error; err != nil {
		return nil, err
	}
	if greeting.Primitive != protocol.PrimChatSendGreeting || greeting.TargetRef != profile.ProfileID ||
		greeting.Platform != profile.Platform || greeting.AccountRef != profile.AccountRef ||
		(greeting.Status != EffectIntentOk && greeting.Status != EffectIntentResolvedOk) ||
		greeting.ResultConversationRef == nil || *greeting.ResultConversationRef != *profile.ConversationRef {
		return nil, ErrResumeCaptureBinding
	}
	out := &ResumeCaptureTarget{Profile: profile, Account: account, Conversation: conversation}
	if requireActive {
		var selection M5TrialSelection
		if err := tx.First(&selection,
			"profile_id = ? AND status = ? AND active_slot = ?",
			profileID, M5TrialSelectionActive, m5TrialActiveSlot).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrM5TrialNotActive
			}
			return nil, err
		}
		out.Selection = selection
	}
	return out, nil
}

type CreateResumeCaptureCmdRequest struct {
	ProfileID string
	Command   CmdRecord
	Now       time.Time
}

type CreateResumeCaptureCmdResult struct {
	Command CmdRecord
	Created bool
}

func (s *Store) CreateResumeCaptureCmd(req CreateResumeCaptureCmdRequest) (*CreateResumeCaptureCmdResult, error) {
	if req.ProfileID == "" || req.Command.MsgID == "" || req.Command.Name != protocol.PrimCandidateReadResume ||
		req.Command.Class != string(protocol.ClassIntrusive) {
		return nil, errors.New("简历补采命令形态无效")
	}
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	out := &CreateResumeCaptureCmdResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		target, err := eligibleResumeTargetTx(tx, req.ProfileID, true)
		if err != nil {
			return err
		}
		profile := target.Profile
		switch profile.ResumeCaptureState {
		case ResumeCaptureInFlight:
			if profile.ResumeCaptureLogicalDispatchID == nil || *profile.ResumeCaptureLogicalDispatchID == "" {
				return ErrCandidateProfileState
			}
			if err := tx.First(&out.Command, "msg_id = ?", *profile.ResumeCaptureLogicalDispatchID).Error; err != nil {
				return err
			}
			if err := validateResumeCaptureRoot(out.Command, target); err != nil {
				return err
			}
			return nil
		case ResumeCaptureUnattempted:
			// 继续创建。
		case ResumeCaptureCaptured, ResumeCaptureManualRequired:
			return ErrResumeCaptureNotAllowed
		default:
			return ErrCandidateProfileState
		}
		if err := validateResumeCaptureRoot(req.Command, target); err != nil {
			return err
		}
		prepareRootCmd(&req.Command)
		if err := createCmdIfDomainAvailableTx(tx, &req.Command); err != nil {
			return err
		}
		logicalID := req.Command.LogicalDispatchID
		updated := tx.Model(&CandidateProfile{}).
			Where("profile_id = ? AND resume_capture_state = ?", req.ProfileID, ResumeCaptureUnattempted).
			Updates(map[string]any{
				"resume_capture_state":               ResumeCaptureInFlight,
				"resume_capture_logical_dispatch_id": logicalID,
				"resume_capture_attempted_at":        req.Now,
				"resume_capture_failure_reason":      "",
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrCandidateProfileState
		}
		out.Command = req.Command
		out.Created = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func validateResumeCaptureRoot(command CmdRecord, target *ResumeCaptureTarget) error {
	if target == nil || target.Profile.ConversationRef == nil || command.Name != protocol.PrimCandidateReadResume ||
		command.Class != string(protocol.ClassIntrusive) || command.Platform != target.Profile.Platform ||
		command.AccountRef != target.Profile.AccountRef || command.Domain != target.Profile.Platform+":"+target.Profile.AccountRef {
		return ErrResumeCaptureBinding
	}
	var args protocol.CandidateReadResumeArgs
	if err := json.Unmarshal([]byte(command.Args), &args); err != nil ||
		args.ConversationRef != *target.Profile.ConversationRef || args.PlatformUserRef != target.Profile.PlatformUserRef {
		return ErrResumeCaptureBinding
	}
	var context protocol.CmdContext
	if err := json.Unmarshal([]byte(command.ContextJSON), &context); err != nil ||
		context.Platform != target.Profile.Platform || context.AccountRef != target.Profile.AccountRef ||
		context.ExpectedPrincipalFingerprint == "" || target.Account.PrincipalFingerprint == nil ||
		context.ExpectedPrincipalFingerprint != *target.Account.PrincipalFingerprint ||
		command.ExpectedPrincipalFingerprint != context.ExpectedPrincipalFingerprint {
		return ErrResumeCaptureBinding
	}
	return nil
}

type CompleteResumeCaptureRequest struct {
	ProfileID         string
	LogicalDispatchID string
	SnapshotID        string
	Data              protocol.CandidateReadResumeData
}

type canonicalResumeV1 struct {
	Basic           []protocol.CandidateResumeLabelValue `json:"basic"`
	Expectations    []protocol.CandidateResumeLabelValue `json:"expectations"`
	SelfEvaluation  string                               `json:"selfEvaluation"`
	Education       string                               `json:"education"`
	WorkExperiences string                               `json:"workExperiences"`
}

func (s *Store) CompleteResumeCapture(req CompleteResumeCaptureRequest) (*CandidateResumeSnapshot, error) {
	if req.ProfileID == "" || req.LogicalDispatchID == "" || req.SnapshotID == "" {
		return nil, errors.New("简历快照收编缺少 profile/logical/snapshot 标识")
	}
	resumeRaw, err := json.Marshal(canonicalResumeV1{
		Basic: req.Data.Basic, Expectations: req.Data.Expectations,
		SelfEvaluation: req.Data.SelfEvaluation, Education: req.Data.Education,
		WorkExperiences: req.Data.WorkExperiences,
	})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(resumeRaw)
	contentHash := hex.EncodeToString(digest[:])
	var out CandidateResumeSnapshot
	var transitionErr error
	err = s.db.Transaction(func(tx *gorm.DB) error {
		target, bindErr := eligibleResumeTargetTx(tx, req.ProfileID, true)
		if bindErr != nil {
			transitionErr = bindErr
			return markResumeCaptureManualTx(tx, req.ProfileID, req.LogicalDispatchID, "bindingChanged", time.Now())
		}
		profile := target.Profile
		if profile.ResumeCaptureLogicalDispatchID == nil || *profile.ResumeCaptureLogicalDispatchID != req.LogicalDispatchID {
			return ErrCandidateProfileState
		}
		var records []CmdRecord
		if err := tx.Where("logical_dispatch_id = ?", req.LogicalDispatchID).
			Order("lineage_depth, created_at, msg_id").Find(&records).Error; err != nil {
			return err
		}
		leaf, err := validateLineage(records)
		if err != nil {
			return err
		}
		if leaf.Status != CmdOk || leaf.Name != protocol.PrimCandidateReadResume || leaf.TerminalAt == nil {
			return ErrResumeCaptureNotAllowed
		}
		if err := validateResumeCaptureRoot(records[0], target); err != nil {
			transitionErr = ErrResumeCaptureBinding
			return markResumeCaptureManualTx(tx, req.ProfileID, req.LogicalDispatchID, "commandBindingChanged", time.Now())
		}
		resultRaw := json.RawMessage(leaf.ResultBody)
		meta := protocol.Primitives[protocol.PrimCandidateReadResume]
		var result protocol.ResultBody
		var persistedData protocol.CandidateReadResumeData
		if len(resultRaw) == 0 || protocol.ValidatePrimitiveResult(protocol.PrimCandidateReadResume, meta.Ver, resultRaw) != nil ||
			json.Unmarshal(resultRaw, &result) != nil || result.Ref != leaf.MsgID || result.Status != protocol.ResultStatusOk ||
			json.Unmarshal(result.Data, &persistedData) != nil {
			transitionErr = ErrResumeCaptureBinding
			return markResumeCaptureManualTx(tx, req.ProfileID, req.LogicalDispatchID, "invalidPersistedResult", time.Now())
		}
		persistedRaw, _ := json.Marshal(persistedData)
		requestedRaw, _ := json.Marshal(req.Data)
		if string(persistedRaw) != string(requestedRaw) {
			transitionErr = ErrResumeCaptureConflict
			return markResumeCaptureManualTx(tx, req.ProfileID, req.LogicalDispatchID, "resultReplayConflict", time.Now())
		}
		if req.Data.ConversationRef != *profile.ConversationRef || req.Data.PlatformUserRef != profile.PlatformUserRef {
			transitionErr = ErrResumeCaptureBinding
			return markResumeCaptureManualTx(tx, req.ProfileID, req.LogicalDispatchID, "resultBindingChanged", time.Now())
		}
		var existing CandidateResumeSnapshot
		existingErr := tx.First(&existing, "source_logical_dispatch_id = ?", req.LogicalDispatchID).Error
		if existingErr == nil {
			if existing.ProfileID != req.ProfileID || existing.ContentHash != contentHash || existing.ResumeJSON != string(resumeRaw) {
				transitionErr = ErrResumeCaptureConflict
				return markResumeCaptureManualTx(tx, req.ProfileID, req.LogicalDispatchID, "contentConflict", time.Now())
			}
			out = existing
			return nil
		}
		if !errors.Is(existingErr, gorm.ErrRecordNotFound) {
			return existingErr
		}
		if profile.ResumeCaptureState != ResumeCaptureInFlight {
			return ErrCandidateProfileState
		}
		out = CandidateResumeSnapshot{
			SnapshotID: req.SnapshotID, ProfileID: req.ProfileID,
			SourceKind: resumeSnapshotSourceIM, SourceConversationRef: req.Data.ConversationRef,
			SourceLogicalDispatchID: req.LogicalDispatchID, ObservedAt: req.Data.ObservedAt,
			CapturedAt: *leaf.TerminalAt, SchemaVersion: resumeSnapshotSchemaV1,
			ContentHash: contentHash, ResumeJSON: string(resumeRaw), CreatedAt: time.Now(),
		}
		if err := tx.Create(&out).Error; err != nil {
			return err
		}
		updated := tx.Model(&CandidateProfile{}).
			Where("profile_id = ? AND resume_capture_state = ? AND resume_capture_logical_dispatch_id = ?",
				req.ProfileID, ResumeCaptureInFlight, req.LogicalDispatchID).
			Updates(map[string]any{
				"resume_capture_state":          ResumeCaptureCaptured,
				"active_resume_snapshot_id":     out.SnapshotID,
				"resume_capture_failure_reason": "",
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrCandidateProfileState
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if transitionErr != nil {
		return nil, transitionErr
	}
	return &out, nil
}

type FailResumeCaptureRequest struct {
	ProfileID         string
	LogicalDispatchID string
	Reason            string
	At                time.Time
}

func (s *Store) FailResumeCapture(req FailResumeCaptureRequest) error {
	if req.ProfileID == "" || req.LogicalDispatchID == "" || req.Reason == "" || len(req.Reason) > 64 {
		return errors.New("简历补采失败收敛参数无效")
	}
	if req.At.IsZero() {
		req.At = time.Now()
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		return markResumeCaptureManualTx(tx, req.ProfileID, req.LogicalDispatchID, req.Reason, req.At)
	})
}

func markResumeCaptureManualTx(tx *gorm.DB, profileID, logicalID, reason string, at time.Time) error {
	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", profileID).Error; err != nil {
		return err
	}
	if profile.ResumeCaptureLogicalDispatchID == nil || *profile.ResumeCaptureLogicalDispatchID != logicalID {
		return ErrCandidateProfileState
	}
	if profile.ResumeCaptureState == ResumeCaptureManualRequired {
		return nil
	}
	if profile.ResumeCaptureState != ResumeCaptureInFlight && profile.ResumeCaptureState != ResumeCaptureCaptured {
		return ErrCandidateProfileState
	}
	if err := tx.Model(&CandidateProfile{}).Where("profile_id = ?", profileID).Updates(map[string]any{
		"resume_capture_state":          ResumeCaptureManualRequired,
		"active_resume_snapshot_id":     nil,
		"resume_capture_failure_reason": reason,
	}).Error; err != nil {
		return err
	}
	return tx.Model(&M5TrialSelection{}).
		Where("profile_id = ? AND status = ? AND active_slot = ?", profileID, M5TrialSelectionActive, m5TrialActiveSlot).
		Updates(map[string]any{
			"status":      M5TrialSelectionManualRequired,
			"active_slot": nil,
			"reason":      reason,
			"ended_at":    at,
		}).Error
}

type ResumeCaptureStatus struct {
	Selection M5TrialSelection
	Profile   CandidateProfile
	Snapshot  *CandidateResumeSnapshot
	ByteSize  int
}

func (s *Store) M5TrialStatus() (*ResumeCaptureStatus, error) {
	var selection M5TrialSelection
	err := s.db.Order("selected_at desc, selection_id desc").First(&selection).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var profile CandidateProfile
	if err := s.db.First(&profile, "profile_id = ?", selection.ProfileID).Error; err != nil {
		return nil, err
	}
	out := &ResumeCaptureStatus{Selection: selection, Profile: profile}
	if profile.ActiveResumeSnapshotID != nil {
		var snapshot CandidateResumeSnapshot
		if err := s.db.First(&snapshot, "snapshot_id = ? AND profile_id = ?", *profile.ActiveResumeSnapshotID, profile.ProfileID).Error; err != nil {
			return nil, err
		}
		out.Snapshot = &snapshot
		out.ByteSize = len([]byte(snapshot.ResumeJSON))
	}
	return out, nil
}
