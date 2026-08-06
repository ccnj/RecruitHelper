package store

import (
	"errors"
	"strings"
	"time"

	"recruithelper/client/service/internal/workflow"
	"recruithelper/internal/ids"

	"gorm.io/gorm"
)

var (
	ErrProductWorkflowInvalid  = errors.New("产品工作流参数无效")
	ErrProductWorkflowNotFound = errors.New("产品工作流不存在")
	ErrProductWorkflowConflict = errors.New("产品工作流状态冲突")
)

const (
	ProductWorkflowStageSourcing              = "sourcing"
	ProductWorkflowStageScoring               = "scoring"
	ProductWorkflowStageSelection             = "selection"
	ProductWorkflowStageGreetingGeneration    = "greetingGeneration"
	ProductWorkflowStageAwaitingConfirmation  = "awaitingConfirmation"
	ProductWorkflowStageGreetingSending       = "greetingSending"
	ProductWorkflowStageCommunication         = "communication"
	ProductWorkflowStageCompleted             = "completed"
	ProductWorkflowStageFailed                = "failed"
	productWorkflowActiveSlot                 = "active"
	maxProductWorkflowFailureReasonCharacters = 512
	maxProductWorkflowEndReasonCharacters     = 128
	maxProductWorkflowRevisionHashCharacters  = 128
	maxProductWorkflowStageCharacters         = 64
)

type ProductWorkflowPendingAction string

const (
	ProductWorkflowPendingActionSourcing ProductWorkflowPendingAction = "sourcing"
	ProductWorkflowPendingActionEnd      ProductWorkflowPendingAction = "end"
)

type CreateProductWorkflowRunRequest struct {
	RunID      string
	Platform   string
	AccountRef string
	State      workflow.State
	Stage      string
	StartedAt  time.Time
}

type TransitionProductWorkflowRunRequest struct {
	RunID     string
	From      workflow.State
	To        workflow.State
	At        time.Time
	Stage     string
	Failure   string
	EndReason string
}

type RequestProductWorkflowPendingActionRequest struct {
	RunID               string
	Action              ProductWorkflowPendingAction
	ContextRevisionHash string
	RequestedAt         time.Time
}

type ClearProductWorkflowPendingActionRequest struct {
	RunID          string
	ExpectedAction ProductWorkflowPendingAction
}

type AdvanceProductWorkflowStageRequest struct {
	RunID          string
	ExpectedStage  string
	ExpectedStatus workflow.Status
	NextStage      string
	At             time.Time
}

func (s *Store) ActiveProductWorkflowRun() (*ProductWorkflowRun, error) {
	var run ProductWorkflowRun
	err := s.db.First(&run, "active_slot = ?", productWorkflowActiveSlot).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

// LatestProductWorkflowRun returns the newest run by start time, terminal
// history included. The status report uses it to answer "why did this machine
// stop"—an active run alone cannot: once a run ends it releases ActiveSlot,
// and the end reason lives only on that terminal row.
func (s *Store) LatestProductWorkflowRun() (*ProductWorkflowRun, error) {
	var run ProductWorkflowRun
	err := s.db.Order("started_at DESC").First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Store) ProductWorkflowRunByID(runID string) (*ProductWorkflowRun, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, ErrProductWorkflowInvalid
	}
	var run ProductWorkflowRun
	err := s.db.First(&run, "run_id = ?", runID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

// ProductWorkflowRunBySourcingBatchID returns the newest workflow which
// adopted a batch, including terminal history. A failed controller start may
// be followed by a later user-authorized run over that same still-open batch;
// newest-first ordering therefore selects the current confirmation authority
// without making historical business facts disappear.
func (s *Store) ProductWorkflowRunBySourcingBatchID(
	batchID string,
) (*ProductWorkflowRun, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return nil, ErrProductWorkflowInvalid
	}
	var run ProductWorkflowRun
	err := s.db.
		Where("sourcing_batch_id = ?", batchID).
		Order("started_at DESC").
		Order("run_id DESC").
		First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Store) CreateProductWorkflowRun(
	req CreateProductWorkflowRunRequest,
) (*ProductWorkflowRun, error) {
	req.RunID = strings.TrimSpace(req.RunID)
	req.Platform = strings.TrimSpace(req.Platform)
	req.AccountRef = strings.TrimSpace(req.AccountRef)
	req.Stage = strings.TrimSpace(req.Stage)
	if req.RunID == "" {
		req.RunID = ids.NewProductWorkflowRunID()
	}
	if req.Platform == "" || req.AccountRef == "" || req.Stage == "" ||
		req.StartedAt.IsZero() || !validProductWorkflowState(req.State) {
		return nil, ErrProductWorkflowInvalid
	}

	var out ProductWorkflowRun
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var existing ProductWorkflowRun
		err := tx.First(&existing, "active_slot = ?", productWorkflowActiveSlot).Error
		if err == nil {
			return ErrProductWorkflowConflict
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		active := productWorkflowActiveSlot
		out = ProductWorkflowRun{
			RunID: req.RunID, ActiveSlot: &active,
			Platform: req.Platform, AccountRef: req.AccountRef,
			Mode: req.State.Mode, Status: req.State.Status, ResumeStatus: req.State.ResumeStatus,
			Stage: req.Stage, StartedAt: req.StartedAt,
		}
		return tx.Create(&out).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// TransitionProductWorkflowRun is a compare-and-swap over the pure workflow
// state. Replaying the exact target state is idempotent; any other divergence
// is surfaced instead of silently overwriting a concurrent pause/resume.
func (s *Store) TransitionProductWorkflowRun(
	req TransitionProductWorkflowRunRequest,
) (*ProductWorkflowRun, error) {
	req.RunID = strings.TrimSpace(req.RunID)
	req.Stage = strings.TrimSpace(req.Stage)
	req.Failure = strings.TrimSpace(req.Failure)
	req.EndReason = strings.TrimSpace(req.EndReason)
	if req.RunID == "" || req.At.IsZero() ||
		!validProductWorkflowState(req.From) || !validProductWorkflowState(req.To) ||
		len([]rune(req.Failure)) > maxProductWorkflowFailureReasonCharacters ||
		len([]rune(req.EndReason)) > maxProductWorkflowEndReasonCharacters ||
		(req.EndReason != "" && !productWorkflowTerminalStatus(req.To.Status)) {
		return nil, ErrProductWorkflowInvalid
	}

	var out ProductWorkflowRun
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var current ProductWorkflowRun
		if err := tx.First(&current, "run_id = ?", req.RunID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrProductWorkflowNotFound
			}
			return err
		}
		currentState := productWorkflowState(current)
		if currentState == req.To {
			if productWorkflowTerminalStatus(req.To.Status) &&
				current.EndReason != req.EndReason {
				return ErrProductWorkflowConflict
			}
			out = current
			return nil
		}
		if currentState != req.From || current.ActiveSlot == nil {
			return ErrProductWorkflowConflict
		}

		updates := map[string]any{
			"mode": req.To.Mode, "status": req.To.Status,
			"resume_status": req.To.ResumeStatus,
		}
		if req.Stage != "" {
			updates["stage"] = req.Stage
		}
		if req.Failure != "" {
			updates["failure_reason"] = req.Failure
		}
		switch req.To.Status {
		case workflow.StatusPaused, workflow.StatusWaitingDailyWindow:
			updates["paused_at"] = req.At
		case workflow.StatusRunning, workflow.StatusAwaitingConfirmation:
			if req.From.Status == workflow.StatusPaused ||
				req.From.Status == workflow.StatusWaitingDailyWindow {
				updates["resumed_at"] = req.At
			}
		case workflow.StatusCompleted, workflow.StatusFailed:
			updates["active_slot"] = nil
			updates["ended_at"] = req.At
			updates["end_reason"] = req.EndReason
		}
		updated := tx.Model(&ProductWorkflowRun{}).
			Where("run_id = ? AND status = ? AND resume_status = ? AND active_slot = ?",
				req.RunID, req.From.Status, req.From.ResumeStatus, productWorkflowActiveSlot).
			Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrProductWorkflowConflict
		}
		if err := tx.First(&out, "run_id = ?", req.RunID).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// RequestProductWorkflowPendingAction durably records the user's next control
// intent while communication still owns the browser. Replaying the same
// action and sourcing revision preserves the first request time; a competing
// action or revision is surfaced instead of being silently overwritten.
func (s *Store) RequestProductWorkflowPendingAction(
	req RequestProductWorkflowPendingActionRequest,
) (*ProductWorkflowRun, error) {
	req.RunID = strings.TrimSpace(req.RunID)
	req.ContextRevisionHash = strings.TrimSpace(req.ContextRevisionHash)
	if req.RunID == "" || req.RequestedAt.IsZero() ||
		!validProductWorkflowPendingAction(req.Action) ||
		len([]rune(req.ContextRevisionHash)) > maxProductWorkflowRevisionHashCharacters ||
		(req.Action == ProductWorkflowPendingActionSourcing && req.ContextRevisionHash == "") ||
		(req.Action == ProductWorkflowPendingActionEnd && req.ContextRevisionHash != "") {
		return nil, ErrProductWorkflowInvalid
	}

	var out ProductWorkflowRun
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var current ProductWorkflowRun
		if err := tx.First(&current, "run_id = ?", req.RunID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrProductWorkflowNotFound
			}
			return err
		}
		if !productWorkflowAcceptsPendingAction(current, req.Action) {
			return ErrProductWorkflowConflict
		}
		if current.PendingAction != "" {
			if current.PendingAction == req.Action &&
				current.PendingContextRevisionHash == req.ContextRevisionHash &&
				current.PendingRequestedAt != nil {
				out = current
				return nil
			}
			return ErrProductWorkflowConflict
		}

		updated := tx.Model(&ProductWorkflowRun{}).
			Where(
				"run_id = ? AND active_slot = ? AND stage = ? AND status = ? AND (pending_action = ? OR pending_action IS NULL)",
				req.RunID,
				productWorkflowActiveSlot,
				// 用读到的当前阶段做 CAS 条件,而不是写死沟通阶段:这里要挡的是
				// "读到之后又被人改了",不是限定动作只能发生在某个阶段——那件事
				// 由上面的 productWorkflowAcceptsPendingAction 判。写死会让漏斗
				// 阶段的结束请求永远更新 0 行,表现为点了没反应。
				current.Stage,
				current.Status,
				ProductWorkflowPendingAction(""),
			).
			Updates(map[string]any{
				"pending_action":                req.Action,
				"pending_context_revision_hash": req.ContextRevisionHash,
				"pending_requested_at":          req.RequestedAt,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrProductWorkflowConflict
		}
		return tx.First(&out, "run_id = ?", req.RunID).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ClearProductWorkflowPendingAction withdraws exactly the expected pending
// action. Clearing an already-empty slot is idempotent while the same active
// communication run remains eligible; it never erases a competing request.
func (s *Store) ClearProductWorkflowPendingAction(
	req ClearProductWorkflowPendingActionRequest,
) (*ProductWorkflowRun, error) {
	req.RunID = strings.TrimSpace(req.RunID)
	if req.RunID == "" || !validProductWorkflowPendingAction(req.ExpectedAction) {
		return nil, ErrProductWorkflowInvalid
	}

	var out ProductWorkflowRun
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var current ProductWorkflowRun
		if err := tx.First(&current, "run_id = ?", req.RunID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrProductWorkflowNotFound
			}
			return err
		}
		if !productWorkflowAcceptsPendingAction(current, req.ExpectedAction) {
			return ErrProductWorkflowConflict
		}
		if current.PendingAction == "" {
			out = current
			return nil
		}
		if current.PendingAction != req.ExpectedAction {
			return ErrProductWorkflowConflict
		}

		updated := tx.Model(&ProductWorkflowRun{}).
			Where(
				"run_id = ? AND active_slot = ? AND stage = ? AND status = ? AND pending_action = ?",
				req.RunID,
				productWorkflowActiveSlot,
				// 同 Request:CAS 条件用读到的当前阶段,写死会让漏斗阶段撤回请求
				// 永远更新 0 行。
				current.Stage,
				current.Status,
				req.ExpectedAction,
			).
			Updates(map[string]any{
				"pending_action":                ProductWorkflowPendingAction(""),
				"pending_context_revision_hash": "",
				"pending_requested_at":          nil,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrProductWorkflowConflict
		}
		return tx.First(&out, "run_id = ?", req.RunID).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) AttachProductWorkflowSourcingBatch(
	runID, batchID string,
) (*ProductWorkflowRun, error) {
	runID = strings.TrimSpace(runID)
	batchID = strings.TrimSpace(batchID)
	if runID == "" || batchID == "" {
		return nil, ErrProductWorkflowInvalid
	}

	var out ProductWorkflowRun
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var run ProductWorkflowRun
		if err := tx.First(&run, "run_id = ?", runID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrProductWorkflowNotFound
			}
			return err
		}
		if run.Mode != workflow.ModeFull || run.ActiveSlot == nil {
			return ErrProductWorkflowConflict
		}
		if run.SourcingBatchID != nil {
			if *run.SourcingBatchID != batchID {
				return ErrProductWorkflowConflict
			}
			out = run
			return nil
		}
		var batch SourcingBatch
		if err := tx.First(&batch, "batch_id = ?", batchID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourcingBatchNotFound
			}
			return err
		}
		if batch.Platform != run.Platform || batch.AccountRef != run.AccountRef {
			return ErrProductWorkflowConflict
		}
		updated := tx.Model(&ProductWorkflowRun{}).
			Where("run_id = ? AND sourcing_batch_id IS NULL AND active_slot = ?",
				runID, productWorkflowActiveSlot).
			Update("sourcing_batch_id", batchID)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrProductWorkflowConflict
		}
		return tx.First(&out, "run_id = ?", runID).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// AdvanceProductWorkflowStage is the coarse orchestrator CAS. Candidate-level
// progress remains in the existing batch/invocation facts; this only advances
// the UI-visible phase while the pure workflow status stays unchanged.
// Repeating expected→target after target was already written is idempotent.
func (s *Store) AdvanceProductWorkflowStage(
	req AdvanceProductWorkflowStageRequest,
) (*ProductWorkflowRun, error) {
	req.RunID = strings.TrimSpace(req.RunID)
	req.ExpectedStage = strings.TrimSpace(req.ExpectedStage)
	req.NextStage = strings.TrimSpace(req.NextStage)
	if req.RunID == "" || req.ExpectedStage == "" || req.NextStage == "" ||
		req.ExpectedStage == req.NextStage || req.At.IsZero() ||
		len([]rune(req.ExpectedStage)) > maxProductWorkflowStageCharacters ||
		len([]rune(req.NextStage)) > maxProductWorkflowStageCharacters ||
		!productWorkflowStageAdvanceStatus(req.ExpectedStatus) {
		return nil, ErrProductWorkflowInvalid
	}

	var out ProductWorkflowRun
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var current ProductWorkflowRun
		if err := tx.First(&current, "run_id = ?", req.RunID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrProductWorkflowNotFound
			}
			return err
		}
		if current.Status == req.ExpectedStatus && current.Stage == req.NextStage {
			out = current
			return nil
		}
		if current.ActiveSlot == nil || current.Status != req.ExpectedStatus ||
			current.Stage != req.ExpectedStage {
			return ErrProductWorkflowConflict
		}
		updated := tx.Model(&ProductWorkflowRun{}).
			Where(
				"run_id = ? AND active_slot = ? AND status = ? AND stage = ?",
				req.RunID, productWorkflowActiveSlot, req.ExpectedStatus, req.ExpectedStage,
			).
			Updates(map[string]any{"stage": req.NextStage, "updated_at": req.At})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrProductWorkflowConflict
		}
		return tx.First(&out, "run_id = ?", req.RunID).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func productWorkflowState(run ProductWorkflowRun) workflow.State {
	return workflow.State{
		Mode: run.Mode, Status: run.Status, ResumeStatus: run.ResumeStatus,
	}
}

func validProductWorkflowState(state workflow.State) bool {
	switch state.Mode {
	case workflow.ModeFull, workflow.ModeReplyOnly:
	default:
		return false
	}
	switch state.Status {
	case workflow.StatusRunning, workflow.StatusAwaitingConfirmation,
		workflow.StatusCompleted, workflow.StatusFailed:
		return state.ResumeStatus == ""
	case workflow.StatusPaused, workflow.StatusWaitingDailyWindow:
		return state.ResumeStatus == workflow.StatusRunning ||
			state.ResumeStatus == workflow.StatusAwaitingConfirmation
	default:
		return false
	}
}

func productWorkflowStageAdvanceStatus(status workflow.Status) bool {
	return status == workflow.StatusRunning ||
		status == workflow.StatusAwaitingConfirmation
}

func productWorkflowTerminalStatus(status workflow.Status) bool {
	return status == workflow.StatusCompleted || status == workflow.StatusFailed
}

func validProductWorkflowPendingAction(action ProductWorkflowPendingAction) bool {
	return action == ProductWorkflowPendingActionSourcing ||
		action == ProductWorkflowPendingActionEnd
}

func productWorkflowAcceptsPendingAction(
	run ProductWorkflowRun,
	action ProductWorkflowPendingAction,
) bool {
	if run.ActiveSlot == nil {
		return false
	}
	// 阶段限制按动作分开(2026-07-31 甲方裁决)。再采一批的语义是"这批聊完了
	// 再来一批",离开沟通阶段没有意义;结束则在任何未终局阶段都成立,否则
	// 漏斗跑着的一两个小时里,用户点结束会被这里直接拒回,界面上就是点了
	// 没反应。
	if action == ProductWorkflowPendingActionSourcing &&
		run.Stage != ProductWorkflowStageCommunication {
		return false
	}
	switch run.Status {
	case workflow.StatusRunning, workflow.StatusPaused, workflow.StatusWaitingDailyWindow:
		return true
	case workflow.StatusAwaitingConfirmation:
		// 等待人工确认时只允许结束:招呼语已生成但一条都还没发,此时放弃这批
		// 是用户的正当选择。再采一批在这里仍然无意义。
		return action == ProductWorkflowPendingActionEnd
	default:
		return false
	}
}
