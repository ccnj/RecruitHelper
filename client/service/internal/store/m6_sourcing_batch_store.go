package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"

	"gorm.io/gorm"
)

var (
	ErrSourcingBatchInvalid       = errors.New("采集批次参数无效")
	ErrSourcingBatchNotFound      = errors.New("采集批次不存在")
	ErrSourcingBatchConflict      = errors.New("采集批次材料冲突")
	ErrSourcingBatchStateConflict = errors.New("采集批次状态不允许当前操作")
)

const SourcingFeedChangedReason = "recommendationFeedChanged"

type InvalidateSourcingFeedRequest struct {
	Platform   string
	AccountRef string
	Trigger    string
	At         time.Time
}

type InvalidateSourcingFeedResult struct {
	MarkerAdvanced bool
	BatchStopped   bool
}

type StartSourcingBatchRequest struct {
	BatchID             string
	Platform            string
	AccountRef          string
	ContextRevisionHash string
	TargetCount         int
	StartedAt           time.Time
}

type StartSourcingBatchResult struct {
	Batch   SourcingBatch
	Created bool
}

type BindSourcingBatchPositionRequest struct {
	BatchID           string
	LogicalDispatchID string
}

type BlockSourcingBatchRequest struct {
	BatchID   string
	Reason    string
	BlockedAt time.Time
}

type ResumeSourcingBatchRequest struct {
	BatchID string
}

type StopSourcingBatchRequest struct {
	BatchID   string
	Reason    string
	StoppedAt time.Time
}

type SourcingBatchProgress struct {
	BatchID             string
	ContextRevisionHash string
	TargetCount         int
	CapturedCount       int64
	RemainingCount      int
	Status              SourcingBatchStatus
	Reason              string
	StartedAt           time.Time
	LastAttemptAt       *time.Time
	PositionBoundAt     *time.Time
	EndedAt             *time.Time
}

// InvalidateSourcingFeed 原子记录推荐流换代边界，并终止仍属于旧页面的
// 非终态采集批次。已经完成的批次不改写；其尚未进入 WAL 的招呼由读取投影
// 根据该边界派生为 abandoned，已经进入 WAL 的招呼仍继续收敛。
func (s *Store) InvalidateSourcingFeed(
	req InvalidateSourcingFeedRequest,
) (*InvalidateSourcingFeedResult, error) {
	req.Platform = strings.TrimSpace(req.Platform)
	req.AccountRef = strings.TrimSpace(req.AccountRef)
	req.Trigger = strings.TrimSpace(req.Trigger)
	if req.Platform == "" || req.AccountRef == "" || req.Trigger == "" || len(req.Trigger) > 64 {
		return nil, ErrSourcingBatchInvalid
	}
	if req.At.IsZero() {
		req.At = time.Now()
	}

	result := InvalidateSourcingFeedResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var account Account
		if err := tx.First(&account,
			"platform = ? AND account_ref = ?", req.Platform, req.AccountRef,
		).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAccountNotFound
			}
			return err
		}

		effectiveAt := req.At
		if account.SourcingFeedInvalidatedAt == nil || account.SourcingFeedInvalidatedAt.Before(req.At) {
			invalidatedAt := req.At
			account.SourcingFeedInvalidatedAt = &invalidatedAt
			result.MarkerAdvanced = true
		} else {
			effectiveAt = *account.SourcingFeedInvalidatedAt
		}

		var batch SourcingBatch
		batchErr := tx.First(&batch,
			"platform = ? AND account_ref = ? AND ended_at IS NULL", req.Platform, req.AccountRef,
		).Error
		if batchErr != nil && !errors.Is(batchErr, gorm.ErrRecordNotFound) {
			return batchErr
		}
		if batchErr == nil && !batch.StartedAt.After(effectiveAt) {
			updated := tx.Model(&SourcingBatch{}).
				Where("batch_id = ? AND ended_at IS NULL", batch.BatchID).
				Updates(map[string]any{
					"status": SourcingBatchStopped, "reason": SourcingFeedChangedReason,
					"ended_at": effectiveAt,
				})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrSourcingBatchStateConflict
			}
			stoppedAt := effectiveAt
			account.StoppedAt = &stoppedAt
			account.PausedReason = SourcingFeedChangedReason
			account.DirtyHint = true
			result.BatchStopped = true
		}

		if result.MarkerAdvanced || result.BatchStopped {
			if err := tx.Save(&account).Error; err != nil {
				return err
			}
		}
		if !result.MarkerAdvanced {
			return nil
		}
		return tx.Create(&AuditEntry{
			At: req.At, Category: "sourcing_feed_invalidated", HandID: account.BoundHandID,
			Platform: req.Platform, AccountRef: req.AccountRef,
			Detail: fmt.Sprintf("trigger=%s;batchStopped=%t", req.Trigger, result.BatchStopped),
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// AccountsBoundToHand 返回当前由同一手服务的账号根，供插件换代时逐账号
// 失效推荐流。返回完整 Account 只在脑内使用，不进入管理投影或日志。
func (s *Store) AccountsBoundToHand(handID string) ([]Account, error) {
	handID = strings.TrimSpace(handID)
	if handID == "" {
		return nil, ErrSourcingBatchInvalid
	}
	var accounts []Account
	err := s.db.Where("bound_hand_id = ?", handID).
		Order("platform, account_ref").Find(&accounts).Error
	return accounts, err
}

// InvalidateSourcingFeedsForHand 用同一脑时刻失效该手当前绑定账号的推荐流。
// 单账号事务已经完整闭合；多账号中途失败时调用方停止重载，重试会幂等补齐。
func (s *Store) InvalidateSourcingFeedsForHand(handID, trigger string, at time.Time) error {
	trigger = strings.TrimSpace(trigger)
	if trigger == "" || len(trigger) > 64 {
		return ErrSourcingBatchInvalid
	}
	if at.IsZero() {
		at = time.Now()
	}
	accounts, err := s.AccountsBoundToHand(handID)
	if err != nil {
		return err
	}
	for i := range accounts {
		if _, err := s.InvalidateSourcingFeed(InvalidateSourcingFeedRequest{
			Platform: accounts[i].Platform, AccountRef: accounts[i].AccountRef,
			Trigger: trigger, At: at,
		}); err != nil {
			return err
		}
	}
	return nil
}

// StartSourcingBatch 创建一次显式目标数的正式采集批次。同一账号已经存在
// 材料完全相同的非终态批次时复用它；材料不同则拒绝覆盖。BatchID 为空时
// 由脑生成随机引用，调用方无需从平台身份派生它。
func (s *Store) StartSourcingBatch(req StartSourcingBatchRequest) (*StartSourcingBatchResult, error) {
	req.BatchID = strings.TrimSpace(req.BatchID)
	req.Platform = strings.TrimSpace(req.Platform)
	req.AccountRef = strings.TrimSpace(req.AccountRef)
	req.ContextRevisionHash = strings.TrimSpace(req.ContextRevisionHash)
	if req.Platform == "" || req.AccountRef == "" || req.ContextRevisionHash == "" || req.TargetCount <= 0 {
		return nil, ErrSourcingBatchInvalid
	}
	if req.BatchID == "" {
		req.BatchID = ids.NewSourcingBatchID()
	}
	if req.StartedAt.IsZero() {
		req.StartedAt = time.Now()
	}

	result := StartSourcingBatchResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var byID SourcingBatch
		err := tx.First(&byID, "batch_id = ?", req.BatchID).Error
		if err == nil {
			if !sameSourcingBatchStartMaterial(byID, req) {
				return ErrSourcingBatchConflict
			}
			result.Batch = byID
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var account Account
		if err := tx.First(&account, "platform = ? AND account_ref = ?", req.Platform, req.AccountRef).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAccountNotFound
			}
			return err
		}
		var revision JobAIContextRevision
		if err := tx.First(&revision, "revision_hash = ?", req.ContextRevisionHash).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrJobAIContextRevisionNotFound
			}
			return err
		}

		var open SourcingBatch
		err = tx.First(&open, "platform = ? AND account_ref = ? AND ended_at IS NULL", req.Platform, req.AccountRef).Error
		if err == nil {
			if !sameSourcingBatchStartMaterial(open, req) {
				return ErrSourcingBatchConflict
			}
			result.Batch = open
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		batch := SourcingBatch{
			BatchID: req.BatchID, Platform: req.Platform, AccountRef: req.AccountRef,
			ContextRevisionHash: req.ContextRevisionHash, TargetCount: req.TargetCount,
			Status: SourcingBatchPreparing, StartedAt: req.StartedAt,
		}
		if err := tx.Create(&batch).Error; err != nil {
			return err
		}
		result.Batch = batch
		result.Created = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func sameSourcingBatchStartMaterial(batch SourcingBatch, req StartSourcingBatchRequest) bool {
	return batch.Platform == req.Platform && batch.AccountRef == req.AccountRef &&
		batch.ContextRevisionHash == req.ContextRevisionHash && batch.TargetCount == req.TargetCount
}

// BindSourcingBatchPosition 只从首个 candidate.readSourcingWindow 的持久
// 命令正结果派生职位绑定，调用方不能直接提交 positionRef。PositionTitle
// 只是首次观测的展示快照，不参与幂等或身份判断。
func (s *Store) BindSourcingBatchPosition(req BindSourcingBatchPositionRequest) (*SourcingBatch, error) {
	req.BatchID = strings.TrimSpace(req.BatchID)
	req.LogicalDispatchID = strings.TrimSpace(req.LogicalDispatchID)
	if req.BatchID == "" || req.LogicalDispatchID == "" {
		return nil, ErrSourcingBatchInvalid
	}

	var out SourcingBatch
	err := s.db.Transaction(func(tx *gorm.DB) error {
		batch, err := sourcingBatchByIDTx(tx, req.BatchID)
		if err != nil {
			return err
		}
		var account Account
		if err := tx.First(&account,
			"platform = ? AND account_ref = ?", batch.Platform, batch.AccountRef,
		).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAccountNotFound
			}
			return err
		}
		if account.PrincipalFingerprint == nil {
			return ErrSourcingBinding
		}
		var records []CmdRecord
		if err := tx.Where("logical_dispatch_id = ?", req.LogicalDispatchID).
			Order("lineage_depth, created_at, msg_id").Find(&records).Error; err != nil {
			return err
		}
		leaf, err := validateSourcingWindowLineage(records, batch, account)
		if err != nil {
			return err
		}
		if records[0].CreatedAt.Before(batch.StartedAt) || leaf.TerminalAt == nil || leaf.TerminalAt.Before(batch.StartedAt) {
			return ErrSourcingBinding
		}
		meta := protocol.Primitives[protocol.PrimCandidateReadSourcingWindow]
		resultRaw := json.RawMessage(leaf.ResultBody)
		var result protocol.ResultBody
		var data protocol.CandidateReadSourcingWindowData
		if len(resultRaw) == 0 ||
			protocol.ValidatePrimitiveResult(protocol.PrimCandidateReadSourcingWindow, meta.Ver, resultRaw) != nil ||
			json.Unmarshal(resultRaw, &result) != nil || result.Ref != leaf.MsgID ||
			result.Status != protocol.ResultStatusOk || json.Unmarshal(result.Data, &data) != nil || data.PositionRef == "" {
			return ErrSourcingBinding
		}
		if batch.PositionRef != nil {
			if *batch.PositionRef != data.PositionRef {
				return ErrSourcingBatchConflict
			}
			out = batch
			return nil
		}
		if batch.Status != SourcingBatchPreparing || batch.EndedAt != nil {
			return ErrSourcingBatchStateConflict
		}
		updates := map[string]any{
			"position_ref":      data.PositionRef,
			"position_title":    data.PositionTitle,
			"position_bound_at": leaf.TerminalAt,
			"last_attempt_at":   leaf.TerminalAt,
			"status":            SourcingBatchCollecting,
			"reason":            "",
		}
		updated := tx.Model(&SourcingBatch{}).
			Where("batch_id = ? AND status = ? AND position_ref IS NULL AND ended_at IS NULL", req.BatchID, SourcingBatchPreparing).
			Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrSourcingBatchStateConflict
		}
		batch, err = sourcingBatchByIDTx(tx, req.BatchID)
		if err != nil {
			return err
		}
		out = batch
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func validateSourcingWindowLineage(
	records []CmdRecord,
	batch SourcingBatch,
	account Account,
) (*CmdRecord, error) {
	leaf, err := validateLineage(records)
	if err != nil {
		return nil, err
	}
	if leaf.Status != CmdOk || leaf.Name != protocol.PrimCandidateReadSourcingWindow || leaf.TerminalAt == nil {
		return nil, ErrSourcingBinding
	}
	for _, command := range records {
		if command.Name != protocol.PrimCandidateReadSourcingWindow ||
			command.Class != string(protocol.ClassIntrusive) || command.Platform != batch.Platform ||
			command.AccountRef != batch.AccountRef || command.Domain != batch.Platform+":"+batch.AccountRef {
			return nil, ErrSourcingBinding
		}
		var args protocol.CandidateReadSourcingWindowArgs
		if json.Unmarshal([]byte(command.Args), &args) != nil {
			return nil, ErrSourcingBinding
		}
		if !sourcingBatchCommandMatchesAccount(command, batch, account) {
			return nil, ErrSourcingBinding
		}
	}
	return leaf, nil
}

func sourcingBatchCommandMatchesAccount(command CmdRecord, batch SourcingBatch, account Account) bool {
	var context protocol.CmdContext
	return json.Unmarshal([]byte(command.ContextJSON), &context) == nil &&
		context.Platform == batch.Platform && context.AccountRef == batch.AccountRef &&
		context.ExpectedPrincipalFingerprint != "" && account.PrincipalFingerprint != nil &&
		context.ExpectedPrincipalFingerprint == *account.PrincipalFingerprint &&
		command.ExpectedPrincipalFingerprint == context.ExpectedPrincipalFingerprint
}

// MarkSourcingBatchAttempt 只记录批次级尝试时刻，不创建候选人成员事实。
func (s *Store) MarkSourcingBatchAttempt(batchID string, at time.Time) error {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return ErrSourcingBatchInvalid
	}
	if at.IsZero() {
		at = time.Now()
	}
	result := s.db.Model(&SourcingBatch{}).
		Where("batch_id = ? AND ended_at IS NULL AND status IN ?", batchID,
			[]SourcingBatchStatus{SourcingBatchPreparing, SourcingBatchCollecting}).
		Update("last_attempt_at", at)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	batch, err := s.SourcingBatchByID(batchID)
	if err != nil {
		return err
	}
	if batch == nil {
		return ErrSourcingBatchNotFound
	}
	return ErrSourcingBatchStateConflict
}

// BlockSourcingBatch 停止自动派发但保留同一批次。BlockedAt 记入最近尝试
// 时刻；重复阻塞允许刷新原因，因为它是当前可恢复状态而非终态事实。
func (s *Store) BlockSourcingBatch(req BlockSourcingBatchRequest) (*SourcingBatch, error) {
	req.BatchID = strings.TrimSpace(req.BatchID)
	req.Reason = strings.TrimSpace(req.Reason)
	if req.BatchID == "" || req.Reason == "" || len(req.Reason) > 256 {
		return nil, ErrSourcingBatchInvalid
	}
	if req.BlockedAt.IsZero() {
		req.BlockedAt = time.Now()
	}

	var out SourcingBatch
	err := s.db.Transaction(func(tx *gorm.DB) error {
		batch, err := sourcingBatchByIDTx(tx, req.BatchID)
		if err != nil {
			return err
		}
		if batch.EndedAt != nil || sourcingBatchTerminal(batch.Status) {
			return ErrSourcingBatchStateConflict
		}
		if batch.Status != SourcingBatchPreparing && batch.Status != SourcingBatchCollecting && batch.Status != SourcingBatchBlocked {
			return ErrSourcingBatchStateConflict
		}
		if err := tx.Model(&SourcingBatch{}).Where("batch_id = ? AND ended_at IS NULL", req.BatchID).
			Updates(map[string]any{
				"status": SourcingBatchBlocked, "reason": req.Reason, "last_attempt_at": req.BlockedAt,
			}).Error; err != nil {
			return err
		}
		batch, err = sourcingBatchByIDTx(tx, req.BatchID)
		if err != nil {
			return err
		}
		out = batch
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ResumeSourcingBatch 是 blocked 的唯一恢复入口；是否已有职位绑定决定恢复到
// preparing 还是 collecting。普通账号开关不得隐式调用它。
func (s *Store) ResumeSourcingBatch(req ResumeSourcingBatchRequest) (*SourcingBatch, error) {
	req.BatchID = strings.TrimSpace(req.BatchID)
	if req.BatchID == "" {
		return nil, ErrSourcingBatchInvalid
	}

	var out SourcingBatch
	err := s.db.Transaction(func(tx *gorm.DB) error {
		batch, err := sourcingBatchByIDTx(tx, req.BatchID)
		if err != nil {
			return err
		}
		if batch.EndedAt != nil || sourcingBatchTerminal(batch.Status) {
			return ErrSourcingBatchStateConflict
		}
		if batch.Status == SourcingBatchPreparing || batch.Status == SourcingBatchCollecting {
			out = batch
			return nil
		}
		if batch.Status != SourcingBatchBlocked {
			return ErrSourcingBatchStateConflict
		}
		next := SourcingBatchPreparing
		if batch.PositionRef != nil {
			next = SourcingBatchCollecting
		}
		updated := tx.Model(&SourcingBatch{}).
			Where("batch_id = ? AND status = ? AND ended_at IS NULL", req.BatchID, SourcingBatchBlocked).
			Updates(map[string]any{"status": next, "reason": ""})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrSourcingBatchStateConflict
		}
		batch, err = sourcingBatchByIDTx(tx, req.BatchID)
		if err != nil {
			return err
		}
		out = batch
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// StopSourcingBatch 把用户放弃写成不可变终态，不删除批次或成员。
func (s *Store) StopSourcingBatch(req StopSourcingBatchRequest) (*SourcingBatch, error) {
	req.BatchID = strings.TrimSpace(req.BatchID)
	req.Reason = strings.TrimSpace(req.Reason)
	if req.BatchID == "" || req.Reason == "" || len(req.Reason) > 256 {
		return nil, ErrSourcingBatchInvalid
	}
	if req.StoppedAt.IsZero() {
		req.StoppedAt = time.Now()
	}

	var out SourcingBatch
	err := s.db.Transaction(func(tx *gorm.DB) error {
		batch, err := sourcingBatchByIDTx(tx, req.BatchID)
		if err != nil {
			return err
		}
		if batch.Status == SourcingBatchStopped && batch.EndedAt != nil {
			if batch.Reason != req.Reason {
				return ErrSourcingBatchConflict
			}
			out = batch
			return nil
		}
		if batch.EndedAt != nil || sourcingBatchTerminal(batch.Status) {
			return ErrSourcingBatchStateConflict
		}
		updated := tx.Model(&SourcingBatch{}).
			Where("batch_id = ? AND ended_at IS NULL", req.BatchID).
			Updates(map[string]any{
				"status": SourcingBatchStopped, "reason": req.Reason, "ended_at": req.StoppedAt,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrSourcingBatchStateConflict
		}
		batch, err = sourcingBatchByIDTx(tx, req.BatchID)
		if err != nil {
			return err
		}
		out = batch
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func sourcingBatchTerminal(status SourcingBatchStatus) bool {
	return status == SourcingBatchCompleted || status == SourcingBatchStopped
}

func sourcingBatchByIDTx(tx *gorm.DB, batchID string) (SourcingBatch, error) {
	var batch SourcingBatch
	if err := tx.First(&batch, "batch_id = ?", batchID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return SourcingBatch{}, ErrSourcingBatchNotFound
		}
		return SourcingBatch{}, err
	}
	return batch, nil
}

func (s *Store) SourcingBatchByID(batchID string) (*SourcingBatch, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return nil, ErrSourcingBatchInvalid
	}
	var batch SourcingBatch
	err := s.db.First(&batch, "batch_id = ?", batchID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &batch, nil
}

func (s *Store) ActiveSourcingBatch(key AccountKey) (*SourcingBatch, error) {
	key.Platform = strings.TrimSpace(key.Platform)
	key.AccountRef = strings.TrimSpace(key.AccountRef)
	if key.Platform == "" || key.AccountRef == "" {
		return nil, ErrSourcingBatchInvalid
	}
	var batch SourcingBatch
	err := s.db.First(&batch, "platform = ? AND account_ref = ? AND ended_at IS NULL", key.Platform, key.AccountRef).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &batch, nil
}

func (s *Store) SourcingBatchProgressByID(batchID string) (*SourcingBatchProgress, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return nil, ErrSourcingBatchInvalid
	}
	var progress SourcingBatchProgress
	err := s.db.Transaction(func(tx *gorm.DB) error {
		batch, err := sourcingBatchByIDTx(tx, batchID)
		if err != nil {
			return err
		}
		progress, err = sourcingBatchProgressTx(tx, batch)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &progress, nil
}

// LatestSourcingBatchProgress 返回账号最新一次正式批次；非终态结束后仍返回
// 最近终态，便于管理状态观察 completed/stopped。投影不含账号引用、平台
// 候选人身份、职位引用、职位标题或简历正文。
func (s *Store) LatestSourcingBatchProgress(key AccountKey) (*SourcingBatchProgress, error) {
	key.Platform = strings.TrimSpace(key.Platform)
	key.AccountRef = strings.TrimSpace(key.AccountRef)
	if key.Platform == "" || key.AccountRef == "" {
		return nil, ErrSourcingBatchInvalid
	}
	var progress *SourcingBatchProgress
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var batch SourcingBatch
		err := tx.Where("platform = ? AND account_ref = ?", key.Platform, key.AccountRef).
			Order("started_at DESC, created_at DESC, batch_id DESC").First(&batch).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		value, err := sourcingBatchProgressTx(tx, batch)
		if err != nil {
			return err
		}
		progress = &value
		return nil
	})
	return progress, err
}

func sourcingBatchProgressTx(tx *gorm.DB, batch SourcingBatch) (SourcingBatchProgress, error) {
	var captured int64
	if err := tx.Model(&SourcingCandidateRun{}).Where("batch_id = ?", batch.BatchID).Count(&captured).Error; err != nil {
		return SourcingBatchProgress{}, err
	}
	remaining := batch.TargetCount - int(captured)
	if remaining < 0 {
		remaining = 0
	}
	return SourcingBatchProgress{
		BatchID: batch.BatchID, ContextRevisionHash: batch.ContextRevisionHash,
		TargetCount: batch.TargetCount, CapturedCount: captured, RemainingCount: remaining,
		Status: batch.Status, Reason: batch.Reason, StartedAt: batch.StartedAt,
		LastAttemptAt: batch.LastAttemptAt, PositionBoundAt: batch.PositionBoundAt, EndedAt: batch.EndedAt,
	}, nil
}

// SourcingBatchExcludedPlatformUserRefs 返回完整批次成员集合，不再继承旧原语
// 的 32 项数组上限。返回值只供脑内等值比较，不得进入管理 API 或日志。
func (s *Store) SourcingBatchExcludedPlatformUserRefs(batchID string) ([]string, error) {
	batch, err := s.SourcingBatchByID(batchID)
	if err != nil {
		return nil, err
	}
	if batch == nil {
		return nil, ErrSourcingBatchNotFound
	}
	var refs []string
	err = s.db.Model(&SourcingCandidateRun{}).
		Where("batch_id = ?", batch.BatchID).
		Order("captured_at, run_id").
		Pluck("platform_user_ref", &refs).Error
	return refs, err
}

func (s *Store) SourcingBatchHasMember(batchID, platformUserRef string) (bool, error) {
	batchID = strings.TrimSpace(batchID)
	platformUserRef = strings.TrimSpace(platformUserRef)
	if batchID == "" || platformUserRef == "" {
		return false, ErrSourcingBatchInvalid
	}
	batch, err := s.SourcingBatchByID(batchID)
	if err != nil {
		return false, err
	}
	if batch == nil {
		return false, ErrSourcingBatchNotFound
	}
	var count int64
	err = s.db.Model(&SourcingCandidateRun{}).
		Where("batch_id = ? AND platform_user_ref = ?", batch.BatchID, platformUserRef).
		Count(&count).Error
	return count != 0, err
}

// SourcingRunByID 是脑内成员事实查询，不得把返回的候选人身份或正文直接
// 投影进管理 API、日志或报告。
func (s *Store) SourcingRunByID(runID string) (*SourcingCandidateRun, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, ErrSourcingBatchInvalid
	}
	var run SourcingCandidateRun
	err := s.db.First(&run, "run_id = ?", runID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}
