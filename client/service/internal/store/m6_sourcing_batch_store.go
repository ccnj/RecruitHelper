package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"recruithelper/client/service/internal/textcanon"
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

const (
	SourcingFeedChangedReason        = "recommendationFeedChanged"
	SourcingTargetReachedPauseReason = "sourcingTargetReached"
	// SourcingNoNewCandidatesReason 是批次侧的收口原因。账号暂停仍复用
	// SourcingTargetReachedPauseReason：那是漏斗识别采集内部 hold 的精确
	// 键，两种收口对下游是同一件事——采集结束、推荐页交还给后续阶段。
	SourcingNoNewCandidatesReason = "noNewCandidates"
)

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
	// CaptureLimit 为 0 表示本批不分轮:采到 TargetCount 即终局。大于 0 时
	// 必须不小于 TargetCount,轮次之间由 ReopenSourcingBatchForCapture 抬档。
	CaptureLimit int
	StartedAt    time.Time
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

type SettleSourcingBatchRequest struct {
	BatchID  string
	SettleAt time.Time
}

type SourcingBatchProgress struct {
	BatchID             string
	ContextRevisionHash string
	BackendJobID        *string
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
	if req.CaptureLimit != 0 && req.CaptureLimit < req.TargetCount {
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
		backendJobID := strings.TrimSpace(revision.SourceJobRef)
		if backendJobID == "" {
			return ErrJobAIContextRevisionInvalid
		}

		var byID SourcingBatch
		err := tx.First(&byID, "batch_id = ?", req.BatchID).Error
		if err == nil {
			if !sameSourcingBatchStartMaterial(byID, req, backendJobID) {
				return ErrSourcingBatchConflict
			}
			result.Batch = byID
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var open SourcingBatch
		err = tx.First(&open, "platform = ? AND account_ref = ? AND ended_at IS NULL", req.Platform, req.AccountRef).Error
		if err == nil {
			if !sameSourcingBatchStartMaterial(open, req, backendJobID) {
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
			ContextRevisionHash: req.ContextRevisionHash, BackendJobID: &backendJobID,
			TargetCount: req.TargetCount, CaptureLimit: req.CaptureLimit,
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

func sameSourcingBatchStartMaterial(
	batch SourcingBatch,
	req StartSourcingBatchRequest,
	backendJobID string,
) bool {
	return batch.Platform == req.Platform && batch.AccountRef == req.AccountRef &&
		batch.ContextRevisionHash == req.ContextRevisionHash &&
		batch.BackendJobID != nil && *batch.BackendJobID == backendJobID &&
		batch.TargetCount == req.TargetCount &&
		batch.CaptureLimit == req.CaptureLimit
}

// BindSourcingBatchPosition 只从首个 candidate.readSourcingWindow 的持久
// 命令正结果派生职位绑定，调用方不能直接提交 positionRef。首次绑定时，
// 页面职位标题必须与批次所锚后台职位的标题精确匹配；已经绑定后的幂等
// 重放仍只按 positionRef 收敛，不允许刷新首次展示快照。
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
		var revision JobAIContextRevision
		if err := tx.First(&revision, "revision_hash = ?", batch.ContextRevisionHash).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrJobAIContextRevisionNotFound
			}
			return err
		}
		backendJobID := strings.TrimSpace(revision.SourceJobRef)
		if batch.BackendJobID == nil || backendJobID == "" ||
			strings.TrimSpace(*batch.BackendJobID) != backendJobID ||
			data.PositionTitle == nil ||
			textcanon.Normalize(*data.PositionTitle) == "" ||
			textcanon.Normalize(*data.PositionTitle) != textcanon.Normalize(revision.DisplayName) {
			return ErrSourcingBinding
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

type ReopenSourcingBatchForCaptureRequest struct {
	BatchID string
	// Step 是本轮采集额度的增量。抬档后的 TargetCount 不得越过 CaptureLimit,
	// 越过就截到上限。
	Step     int
	ReopenAt time.Time
}

// ReopenSourcingBatchForCapture 把一个刚采满、但选中人数还没够选中目标的批次
// 退回 collecting 再采一轮。它与 ResumeSourcingBatch 是两件事：那个是人工把
// blocked 的故障批次救回来，这个是分轮采集的正常推进，只认 completed。
//
// 三种批次拒绝回退，各有各的道理：
//   - 撞底收口的（reason 前缀 noNewCandidates）：SettleSourcingBatch 已经把
//     TargetCount 改写成实到人数，说明推荐流里已经没有新人，再采一轮只空转。
//   - CaptureLimit 为 0 或已经采到上限的：分轮额度用完，该收口了。
//   - 成员数与 TargetCount 对不上的：那道“run 数精确等于 TargetCount”的不变
//     式是评分与筛选的共同前置，对不上说明批次本身已经不自洽，不能再往上抬。
//
// 账号只解除采集内部的 hold，不碰 EnabledDate：跨日之后能不能接着跑，仍由既
// 有的每日门禁和业务窗口裁决，本入口不代替用户做“今天继续”的决定。
func (s *Store) ReopenSourcingBatchForCapture(
	req ReopenSourcingBatchForCaptureRequest,
) (*SourcingBatch, error) {
	req.BatchID = strings.TrimSpace(req.BatchID)
	if req.BatchID == "" || req.Step <= 0 {
		return nil, ErrSourcingBatchInvalid
	}
	if req.ReopenAt.IsZero() {
		req.ReopenAt = time.Now()
	}

	var out SourcingBatch
	err := s.db.Transaction(func(tx *gorm.DB) error {
		batch, err := sourcingBatchByIDTx(tx, req.BatchID)
		if err != nil {
			return err
		}
		// 已经回到采集态就是本入口的幂等重放,原样返回。
		if batch.Status == SourcingBatchCollecting && batch.EndedAt == nil {
			out = batch
			return nil
		}
		if batch.Status != SourcingBatchCompleted || batch.EndedAt == nil {
			return ErrSourcingBatchStateConflict
		}
		if batch.CaptureLimit <= 0 || batch.TargetCount >= batch.CaptureLimit {
			return ErrSourcingBatchStateConflict
		}
		if strings.HasPrefix(batch.Reason, SourcingNoNewCandidatesReason) {
			return ErrSourcingBatchStateConflict
		}
		captured, err := sourcingBatchCapturedCountTx(tx, batch.BatchID)
		if err != nil {
			return err
		}
		if captured != int64(batch.TargetCount) {
			return ErrSourcingBatchConflict
		}
		// ended_at 置空会让本批重新占用“同账号唯一未终局批次”的位置,先确认
		// 没有别的批次已经占着,否则唯一索引会在提交时才炸。
		var openCount int64
		if err := tx.Model(&SourcingBatch{}).
			Where("platform = ? AND account_ref = ? AND ended_at IS NULL AND batch_id <> ?",
				batch.Platform, batch.AccountRef, batch.BatchID).
			Count(&openCount).Error; err != nil {
			return err
		}
		if openCount != 0 {
			return ErrSourcingBatchStateConflict
		}

		nextTarget := batch.TargetCount + req.Step
		if nextTarget > batch.CaptureLimit {
			nextTarget = batch.CaptureLimit
		}
		updated := tx.Model(&SourcingBatch{}).
			Where("batch_id = ? AND status = ? AND ended_at IS NOT NULL",
				batch.BatchID, SourcingBatchCompleted).
			Updates(map[string]any{
				"status":       SourcingBatchCollecting,
				"reason":       "",
				"target_count": nextTarget,
				"ended_at":     nil,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrSourcingBatchStateConflict
		}
		var account Account
		if err := tx.First(&account, "platform = ? AND account_ref = ?",
			batch.Platform, batch.AccountRef).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAccountNotFound
			}
			return err
		}
		// 只解除采集自己挂上的那一种暂停。真人点的暂停、掉登录、跨日过期等
		// 一律原样保留:筛选事务要跑几百毫秒,真人的暂停完全可能落在那期间,
		// 无条件清空会让"UI 显示已暂停、浏览器却继续采下一轮"成为可达状态。
		// 被真人停住时批次照样退回采集态,等他恢复即可,不需要在此重试。
		if account.PausedReason == SourcingTargetReachedPauseReason {
			updatedAccount := tx.Model(&Account{}).
				Where("platform = ? AND account_ref = ? AND paused_reason = ?",
					batch.Platform, batch.AccountRef, SourcingTargetReachedPauseReason).
				Updates(map[string]any{
					"stopped_at":     nil,
					"paused_reason":  "",
					"next_patrol_at": req.ReopenAt,
					"dirty_hint":     true,
				})
			if updatedAccount.Error != nil {
				return updatedAccount.Error
			}
			if updatedAccount.RowsAffected != 1 {
				return ErrSourcingBinding
			}
		}
		out, err = sourcingBatchByIDTx(tx, batch.BatchID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// SettleSourcingBatch 按脑侧扫描预算把一个还没采满的批次收成终态，让漏斗
// 用已经采到的候选人走完后续流程。它按规格《里程碑6 正式采集批次》§二.2 的
// 唯一例外把 targetCount 下调为实到成员数：收口后的批次自述“本批就是这些
// 人”，评分、筛选与招呼语生成的覆盖不变量因此不需要第二个计数。原目标数写
// 进 reason，事实不丢。
//
// 成员为零不是“没有更多候选人”，而是职位、筛选或页面有问题，此时返回状态
// 冲突让调用方改走 blocked 转人工。账号暂停逐字复用达标路径，包括暂停原因：
// 产品漏斗按该原因精确识别“这是采集内部 hold，不是用户暂停”。
func (s *Store) SettleSourcingBatch(req SettleSourcingBatchRequest) (*SourcingBatch, error) {
	req.BatchID = strings.TrimSpace(req.BatchID)
	if req.BatchID == "" {
		return nil, ErrSourcingBatchInvalid
	}
	if req.SettleAt.IsZero() {
		req.SettleAt = time.Now()
	}

	var out SourcingBatch
	err := s.db.Transaction(func(tx *gorm.DB) error {
		batch, err := sourcingBatchByIDTx(tx, req.BatchID)
		if err != nil {
			return err
		}
		if batch.Status == SourcingBatchCompleted && batch.EndedAt != nil {
			out = batch
			return nil
		}
		if batch.Status != SourcingBatchCollecting || batch.EndedAt != nil {
			return ErrSourcingBatchStateConflict
		}
		captured, err := sourcingBatchCapturedCountTx(tx, batch.BatchID)
		if err != nil {
			return err
		}
		// 达标那条路由成员事务自己收口，不该绕到这里来。
		if captured <= 0 || captured >= int64(batch.TargetCount) {
			return ErrSourcingBatchStateConflict
		}
		updatedBatch := tx.Model(&SourcingBatch{}).
			Where("batch_id = ? AND status = ? AND ended_at IS NULL", batch.BatchID, SourcingBatchCollecting).
			Updates(map[string]any{
				"status":       SourcingBatchCompleted,
				"reason":       fmt.Sprintf("%s:target=%d", SourcingNoNewCandidatesReason, batch.TargetCount),
				"target_count": captured,
				"ended_at":     req.SettleAt,
			})
		if updatedBatch.Error != nil {
			return updatedBatch.Error
		}
		if updatedBatch.RowsAffected != 1 {
			return ErrSourcingBatchStateConflict
		}
		updatedAccount := tx.Model(&Account{}).
			Where("platform = ? AND account_ref = ?", batch.Platform, batch.AccountRef).
			Updates(map[string]any{
				"stopped_at":    req.SettleAt,
				"paused_reason": SourcingTargetReachedPauseReason,
				"dirty_hint":    true,
			})
		if updatedAccount.Error != nil {
			return updatedAccount.Error
		}
		if updatedAccount.RowsAffected != 1 {
			return ErrSourcingBinding
		}
		out, err = sourcingBatchByIDTx(tx, batch.BatchID)
		return err
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
		BackendJobID: batch.BackendJobID,
		TargetCount:  batch.TargetCount, CapturedCount: captured, RemainingCount: remaining,
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
