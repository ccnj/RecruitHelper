package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"recruithelper/contract/gen/go/protocol"

	"golang.org/x/text/unicode/norm"
	"gorm.io/gorm"
)

const (
	maxSourcingGreetingTextBytes         = 2048
	sourcingGreetingEffectIntentDomainV1 = "sourcing-greeting-effect-intent-v1|"
)

var (
	ErrSourcingGreetingEffectInvalid  = errors.New("正式批次招呼发送来源无效")
	ErrSourcingGreetingEffectConflict = errors.New("正式批次招呼发送来源冲突")
	ErrSourcingGreetingFeedChanged    = errors.New("正式批次所属推荐流已换代")
)

// SourcingGreetingEffectSource 是自动列表招呼进入 WAL 时唯一允许携带的
// 来源引用。正文、目标、账号和 intentId 都必须由 Store 重新派生。
type SourcingGreetingEffectSource struct {
	BatchID      string
	InvocationID string
}

// SourcingGreetingSendPreparation 是 socket 派发前的只读快照。它不授权
// 副作用；CreateGreetingEffectIntentAndCmd 会在同一写事务内重做全部校验。
type SourcingGreetingSendPreparation struct {
	Source       SourcingGreetingEffectSource
	IntentID     string
	EffectLinked bool
	Account      Account
	Profile      CandidateProfile
	GreetingText string
	ContentHash  string
}

// SourcingGreetingSendTarget 是批次编排可见的内部定位材料。正文不会离开
// Dispatcher 的专用入口，也不会进入管理投影。
type SourcingGreetingSendTarget struct {
	BatchID             string
	ContextRevisionHash string
	InvocationID        string
	ProfileID           string
	Platform            string
	AccountRef          string
	PlatformUserRef     string
	PositionRef         string
	EffectIntentID      *string
}

// SourcingGreetingSendScanPlan 是批量招呼页面续扫的一次性脑内投影。
// Targets 只包含生成成功且尚未终局的成员：未绑定成员可参与当前页面匹配，
// 已绑定成员只供既有 WAL 优先收编。BatchTailAnchor 与 CapturedCount 来自
// 全批采集成员而非 selected 子集。该投影不得进入管理 API、普通日志或持久化。
type SourcingGreetingSendScanPlan struct {
	BatchID         string
	Platform        string
	AccountRef      string
	PositionRef     string
	CapturedCount   int
	BatchTailAnchor string
	Targets         []SourcingGreetingSendTarget
}

// SourcingBatchGreetingSendProgress 是列表发送阶段的脱敏聚合。ReadyCount
// 是生成成功总数；其余发送桶只对可发送成员互斥计数。
type SourcingBatchGreetingSendProgress struct {
	BatchID             string
	ContextRevisionHash string
	SelectedCount       int
	ReadyCount          int64
	PendingCount        int64
	InFlightCount       int64
	SentCount           int64
	FailedCount         int64
	SuspectCount        int64
	AbandonedCount      int64
	Completed           bool
}

type sourcingGreetingEffectMaterial struct {
	Batch      SourcingBatch
	Selection  SourcingBatchSelection
	Run        SourcingCandidateRun
	Decision   SourcingSelectionDecision
	Invocation SourcingGreetingInvocation
	Profile    CandidateProfile
	Account    Account
}

// SourcingGreetingMaterial 是批量招呼语生成唯一允许读取的候选材料。它只
// 从完整筛选批次的 selected decision 投影，调用方无需也不得自行拼接档案。
type SourcingGreetingMaterial struct {
	BatchID             string
	ContextRevisionHash string
	RunID               string
	RunContentHash      string
	ProfileID           string
	ResumeJSON          string
	CapturedAt          time.Time
}

type ReserveSourcingGreetingRequest struct {
	InvocationID        string
	BatchID             string
	RunID               string
	ProfileID           string
	ContextRevisionHash string
	RunContentHash      string
	Provider            string
	Model               string
	InputHash           string
	StartedAt           time.Time
}

type ReserveSourcingGreetingResult struct {
	Invocation SourcingGreetingInvocation
	Created    bool
}

type CompleteSourcingGreetingRequest struct {
	Completion   AIInvocationCompletion
	GreetingText string
	ContentHash  string
}

// SourcingBatchGreetingProgress 是招呼语生成的脱敏批次聚合。正文、成员、
// run/profile/invocation 引用都不会进入这个投影。
type SourcingBatchGreetingProgress struct {
	BatchID             string
	ContextRevisionHash string
	SelectedCount       int
	OKCount             int64
	FailedCount         int64
	InFlightCount       int64
	PendingCount        int64
	Provider            string
	Model               string
	InputTokens         int64
	CachedInputTokens   int64
	OutputTokens        int64
	EstimatedCostMicros int64
	Completed           bool
}

func (s *Store) SourcingGreetingByProfileID(profileID string) (*SourcingGreetingInvocation, error) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return nil, ErrAIInvocationInvalid
	}
	var invocation SourcingGreetingInvocation
	err := s.db.First(&invocation, "profile_id = ?", profileID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &invocation, nil
}

// SourcingGreetingRevision 返回本批招呼语生成阶段的配置。已有任意预留时
// 沿用该阶段已记录 revision；完全未开始时才读取职位 current legacy head。
func (s *Store) SourcingGreetingRevision(batchID string) (*JobAIContextRevision, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return nil, ErrSourcingBatchInvalid
	}
	var out *JobAIContextRevision
	err := s.db.Transaction(func(tx *gorm.DB) error {
		batch, err := validateCompletedSourcingBatchForScoringTx(tx, batchID)
		if err != nil {
			return err
		}
		revision, err := sourcingStageRevisionTx(
			tx, batch, "sourcing_greeting_invocations", true,
		)
		if err != nil {
			return err
		}
		out = revision
		return nil
	})
	return out, err
}

// SourcingGreetingSendScanPlan 返回当前批次续扫所需的最小内部材料。窗口、
// 游标、扫描轮次和 notLocated 均不持久化；页面命中也不授权发送，最终授权
// 仍由 PrepareSourcingGreetingSend 与 WAL 创建事务重新校验。
func (s *Store) SourcingGreetingSendScanPlan(
	batchID string,
) (*SourcingGreetingSendScanPlan, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return nil, ErrSourcingGreetingEffectInvalid
	}
	var out SourcingGreetingSendScanPlan
	err := s.db.Transaction(func(tx *gorm.DB) error {
		materials, err := loadSourcingGreetingSendBatchTx(tx, batchID)
		if err != nil {
			return err
		}
		var batch SourcingBatch
		if err := tx.First(&batch, "batch_id = ?", batchID).Error; err != nil {
			return err
		}
		if batch.PositionRef == nil || strings.TrimSpace(*batch.PositionRef) == "" {
			return ErrSourcingBinding
		}
		var runs []SourcingCandidateRun
		if err := tx.Where("batch_id = ?", batch.BatchID).
			Order("captured_at ASC, run_id ASC").Find(&runs).Error; err != nil {
			return err
		}
		if len(runs) != batch.TargetCount || len(runs) == 0 {
			return ErrSourcingBatchConflict
		}
		for i := range runs {
			run := runs[i]
			if run.BatchID == nil || *run.BatchID != batch.BatchID ||
				run.Platform != batch.Platform || run.AccountRef != batch.AccountRef ||
				run.ContextRevisionHash != batch.ContextRevisionHash ||
				run.PositionRef != *batch.PositionRef ||
				strings.TrimSpace(run.PlatformUserRef) == "" || run.CapturedAt.IsZero() {
				return ErrSourcingGreetingEffectConflict
			}
		}

		out = SourcingGreetingSendScanPlan{
			BatchID: batch.BatchID, Platform: batch.Platform, AccountRef: batch.AccountRef,
			PositionRef: *batch.PositionRef, CapturedCount: len(runs),
			BatchTailAnchor: runs[len(runs)-1].PlatformUserRef,
			Targets:         make([]SourcingGreetingSendTarget, 0, len(materials)),
		}
		for i := range materials {
			target, err := unresolvedSourcingGreetingSendTargetTx(tx, materials[i])
			if err != nil {
				return err
			}
			if target != nil {
				out.Targets = append(out.Targets, *target)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// SourcingGreetingEffectIntentID 把一个不可变生成 invocation 映射到唯一
// 自动招呼意图。版本域属于持久身份配方，未来变更必须换域而不能迁移旧 key。
func SourcingGreetingEffectIntentID(invocationID string) (string, error) {
	invocationID = strings.TrimSpace(invocationID)
	if invocationID == "" {
		return "", ErrSourcingGreetingEffectInvalid
	}
	digest := sha256.Sum256([]byte(sourcingGreetingEffectIntentDomainV1 + invocationID))
	return "intent-sg1-" + hex.EncodeToString(digest[:]), nil
}

// PrepareSourcingGreetingSend 只给 Dispatcher 提供本次派发所需材料。已
// 绑定来源会验证既有 intent/cmd 链并允许 profile 已推进为 greeted；未
// 绑定来源则要求 profile 仍是干净 selected。
func (s *Store) PrepareSourcingGreetingSend(
	batchID string,
	invocationID string,
) (*SourcingGreetingSendPreparation, error) {
	source := SourcingGreetingEffectSource{
		BatchID: strings.TrimSpace(batchID), InvocationID: strings.TrimSpace(invocationID),
	}
	if source.BatchID == "" || source.InvocationID == "" {
		return nil, ErrSourcingGreetingEffectInvalid
	}
	var out SourcingGreetingSendPreparation
	err := s.db.Transaction(func(tx *gorm.DB) error {
		material, err := loadSourcingGreetingEffectMaterialTx(tx, source)
		if err != nil {
			return err
		}
		intentID, err := SourcingGreetingEffectIntentID(source.InvocationID)
		if err != nil {
			return err
		}
		linked := material.Invocation.EffectIntentID != nil
		if linked {
			var intent EffectIntent
			if err := tx.First(&intent, "intent_id = ?", intentID).Error; err != nil {
				return ErrSourcingGreetingEffectConflict
			}
			var command CmdRecord
			if err := tx.First(&command, "msg_id = ?", intent.RootMsgID).Error; err != nil {
				return ErrSourcingGreetingEffectConflict
			}
			if err := validateSourcingGreetingEffectReplayMaterial(
				material, source, intent, command,
			); err != nil {
				return err
			}
		} else if sourcingGreetingFeedChanged(material) {
			return ErrSourcingGreetingFeedChanged
		} else if !sourcingGreetingProfileAllowsNewEffect(material.Profile) {
			return ErrSourcingGreetingEffectConflict
		}
		out = SourcingGreetingSendPreparation{
			Source: source, IntentID: intentID, EffectLinked: linked,
			Account: material.Account, Profile: material.Profile,
			GreetingText: material.Invocation.GreetingText,
			ContentHash:  material.Invocation.ContentHash,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// NextSourcingGreetingSendTarget 返回固定 selection 顺序中的首个待发送或
// 在途成员。既有终局由聚合状态收编，不会被重新定位或另铸 intent。
func (s *Store) NextSourcingGreetingSendTarget(batchID string) (*SourcingGreetingSendTarget, error) {
	plan, err := s.SourcingGreetingSendScanPlan(batchID)
	if err != nil || len(plan.Targets) == 0 {
		return nil, err
	}
	target := plan.Targets[0]
	return &target, nil
}

// unresolvedSourcingGreetingSendTargetTx 与旧 Next 及新续扫投影共用同一份
// ready/linked/terminal 分类。已绑定来源必须先完整复核既有 intent/cmd；
// 未绑定来源若推荐流已换代则不再返回。
func unresolvedSourcingGreetingSendTargetTx(
	tx *gorm.DB,
	material sourcingGreetingEffectMaterial,
) (*SourcingGreetingSendTarget, error) {
	invocation := material.Invocation
	if invocation.InvocationID == "" || invocation.FinishedAt == nil ||
		invocation.Status != AIInvocationOK {
		return nil, nil
	}
	if invocation.EffectIntentID == nil {
		if sourcingGreetingFeedChanged(material) {
			return nil, nil
		}
	} else {
		var intent EffectIntent
		if err := tx.First(&intent, "intent_id = ?", *invocation.EffectIntentID).Error; err != nil {
			return nil, ErrSourcingGreetingEffectConflict
		}
		var command CmdRecord
		if err := tx.First(&command, "msg_id = ?", intent.RootMsgID).Error; err != nil {
			return nil, ErrSourcingGreetingEffectConflict
		}
		if err := validateSourcingGreetingEffectReplayMaterial(
			material,
			SourcingGreetingEffectSource{
				BatchID: material.Batch.BatchID, InvocationID: invocation.InvocationID,
			},
			intent,
			command,
		); err != nil {
			return nil, err
		}
		switch intent.Status {
		case EffectIntentDispatching, EffectIntentReconciling, EffectIntentVerifying:
		case EffectIntentOk, EffectIntentResolvedOk, EffectIntentFailed,
			EffectIntentResolvedFailed, EffectIntentSuspect:
			return nil, nil
		default:
			return nil, ErrSourcingGreetingEffectConflict
		}
	}
	return &SourcingGreetingSendTarget{
		BatchID:             material.Batch.BatchID,
		ContextRevisionHash: invocation.ContextRevisionHash,
		InvocationID:        invocation.InvocationID,
		ProfileID:           material.Profile.ProfileID,
		Platform:            material.Profile.Platform,
		AccountRef:          material.Profile.AccountRef,
		PlatformUserRef:     material.Profile.PlatformUserRef,
		PositionRef:         material.Profile.PositionRef,
		EffectIntentID:      invocation.EffectIntentID,
	}, nil
}

func (s *Store) SourcingBatchGreetingSendProgress(
	batchID string,
) (*SourcingBatchGreetingSendProgress, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return nil, ErrSourcingGreetingEffectInvalid
	}
	var out SourcingBatchGreetingSendProgress
	err := s.db.Transaction(func(tx *gorm.DB) error {
		materials, err := loadSourcingGreetingSendBatchTx(tx, batchID)
		if err != nil {
			return err
		}
		var batch SourcingBatch
		if err := tx.First(&batch, "batch_id = ?", batchID).Error; err != nil {
			return err
		}
		out.BatchID = batchID
		out.SelectedCount = len(materials)
		unresolvedGeneration := int64(0)
		for i := range materials {
			invocation := materials[i].Invocation
			if invocation.InvocationID == "" || invocation.FinishedAt == nil {
				unresolvedGeneration++
				continue
			}
			if out.ContextRevisionHash == "" {
				out.ContextRevisionHash = invocation.ContextRevisionHash
			} else if out.ContextRevisionHash != invocation.ContextRevisionHash {
				return ErrSourcingGreetingEffectConflict
			}
			if invocation.Status != AIInvocationOK {
				out.FailedCount++
				continue
			}
			out.ReadyCount++
			if invocation.EffectIntentID == nil {
				if sourcingGreetingFeedChanged(materials[i]) {
					out.AbandonedCount++
				} else {
					out.PendingCount++
				}
				continue
			}
			var intent EffectIntent
			if err := tx.First(&intent, "intent_id = ?", *invocation.EffectIntentID).Error; err != nil {
				return ErrSourcingGreetingEffectConflict
			}
			var command CmdRecord
			if err := tx.First(&command, "msg_id = ?", intent.RootMsgID).Error; err != nil {
				return ErrSourcingGreetingEffectConflict
			}
			if err := validateSourcingGreetingEffectReplayMaterial(materials[i],
				SourcingGreetingEffectSource{BatchID: materials[i].Batch.BatchID, InvocationID: invocation.InvocationID},
				intent, command,
			); err != nil {
				return err
			}
			switch intent.Status {
			case EffectIntentDispatching, EffectIntentReconciling, EffectIntentVerifying:
				out.InFlightCount++
			case EffectIntentOk, EffectIntentResolvedOk:
				out.SentCount++
			case EffectIntentFailed, EffectIntentResolvedFailed:
				out.FailedCount++
			case EffectIntentSuspect:
				out.SuspectCount++
			default:
				return ErrSourcingGreetingEffectConflict
			}
		}
		out.Completed = unresolvedGeneration == 0 && out.PendingCount == 0 && out.InFlightCount == 0 &&
			out.SentCount+out.FailedCount+out.SuspectCount+out.AbandonedCount == int64(out.SelectedCount)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// NextSelectedSourcingGreetingMaterial 返回批次中尚无任何调用预留的最早
// selected 成员。批次筛选、逐人 decision、profile 与 run 任一绑定不完整
// 都响亮失败，不会退化为“看起来 selected”的宽松查询。
func (s *Store) NextSelectedSourcingGreetingMaterial(
	batchID string,
	contextRevisionHash string,
) (*SourcingGreetingMaterial, error) {
	batchID = strings.TrimSpace(batchID)
	contextRevisionHash = strings.TrimSpace(contextRevisionHash)
	if batchID == "" || contextRevisionHash == "" {
		return nil, ErrSourcingBatchInvalid
	}
	var next *SourcingGreetingMaterial
	err := s.db.Transaction(func(tx *gorm.DB) error {
		_, materials, invocations, err := loadSourcingGreetingScopeTx(
			tx, batchID, true, contextRevisionHash,
		)
		if err != nil {
			return err
		}
		reservedByRun := make(map[string]struct{}, len(invocations))
		for _, invocation := range invocations {
			reservedByRun[invocation.RunID] = struct{}{}
		}
		for i := range materials {
			if _, exists := reservedByRun[materials[i].RunID]; exists {
				continue
			}
			material := materials[i]
			next = &material
			break
		}
		return nil
	})
	return next, err
}

// SourcingGreetingWorkItem 是招呼语生成编排器的一个待驱动成员。Invocation
// 为 nil 表示尚无预留；非 nil 时必为未终局（inFlight）行，按 2026-07-28
// 并行重试裁决允许续驱动。
type SourcingGreetingWorkItem struct {
	Material   SourcingGreetingMaterial
	Invocation *SourcingGreetingInvocation
}

// PendingSourcingGreetingWork 按采集顺序返回批次内全部仍需驱动的 selected
// 成员：尚无预留的与 inFlight 的。已终局成员不出现。
func (s *Store) PendingSourcingGreetingWork(
	batchID string,
	contextRevisionHash string,
) ([]SourcingGreetingWorkItem, error) {
	batchID = strings.TrimSpace(batchID)
	contextRevisionHash = strings.TrimSpace(contextRevisionHash)
	if batchID == "" || contextRevisionHash == "" {
		return nil, ErrSourcingBatchInvalid
	}
	var items []SourcingGreetingWorkItem
	err := s.db.Transaction(func(tx *gorm.DB) error {
		_, materials, invocations, err := loadSourcingGreetingScopeTx(
			tx, batchID, true, contextRevisionHash,
		)
		if err != nil {
			return err
		}
		byRun := make(map[string]SourcingGreetingInvocation, len(invocations))
		for i := range invocations {
			byRun[invocations[i].RunID] = invocations[i]
		}
		for i := range materials {
			invocation, exists := byRun[materials[i].RunID]
			if !exists {
				items = append(items, SourcingGreetingWorkItem{Material: materials[i]})
				continue
			}
			if invocation.FinishedAt != nil {
				continue
			}
			inFlight := invocation
			items = append(items, SourcingGreetingWorkItem{
				Material: materials[i], Invocation: &inFlight,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// RecordSourcingGreetingAttempt 在一次 provider HTTP 尝试发出前登记该尝试。
// 只允许作用于未终局预留；budgeted 表示本次尝试计入非 429 预算。
func (s *Store) RecordSourcingGreetingAttempt(invocationID string, budgeted bool) (*SourcingGreetingInvocation, error) {
	invocationID = strings.TrimSpace(invocationID)
	if invocationID == "" {
		return nil, ErrAIInvocationInvalid
	}
	var out SourcingGreetingInvocation
	err := s.db.Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{"attempt_count": gorm.Expr("attempt_count + 1")}
		if budgeted {
			updates["budgeted_attempt_count"] = gorm.Expr("budgeted_attempt_count + 1")
		}
		updated := tx.Model(&SourcingGreetingInvocation{}).
			Where("invocation_id = ? AND finished_at IS NULL AND status = ?",
				invocationID, AIInvocationTransportFailed).
			Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrAIInvocationConflict
		}
		return tx.First(&out, "invocation_id = ?", invocationID).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ReserveSourcingGreeting 是 provider 调用的唯一持久授权点。RunID 与
// ProfileID 任一已存在预留都只能重放原事实，不授权第二次网络调用。
func (s *Store) ReserveSourcingGreeting(req ReserveSourcingGreetingRequest) (*ReserveSourcingGreetingResult, error) {
	if strings.TrimSpace(req.InvocationID) == "" || strings.TrimSpace(req.BatchID) == "" ||
		strings.TrimSpace(req.RunID) == "" || strings.TrimSpace(req.ProfileID) == "" ||
		strings.TrimSpace(req.ContextRevisionHash) == "" || strings.TrimSpace(req.RunContentHash) == "" ||
		strings.TrimSpace(req.Provider) == "" || strings.TrimSpace(req.Model) == "" ||
		strings.TrimSpace(req.InputHash) == "" {
		return nil, ErrAIInvocationInvalid
	}
	if req.StartedAt.IsZero() {
		req.StartedAt = time.Now()
	}
	out := &ReserveSourcingGreetingResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		_, materials, _, err := loadSourcingGreetingScopeTx(
			tx, req.BatchID, true, req.ContextRevisionHash,
		)
		if err != nil {
			return err
		}
		var material *SourcingGreetingMaterial
		for i := range materials {
			if materials[i].RunID == req.RunID && materials[i].ProfileID == req.ProfileID {
				candidate := materials[i]
				material = &candidate
				break
			}
		}
		if material == nil || material.ContextRevisionHash != req.ContextRevisionHash ||
			material.RunContentHash != req.RunContentHash {
			return ErrSourcingBinding
		}
		// 同批不再冻结单一 provider/model:引擎运行期可换代(2026-08-12 甲方
		// 裁决),混模型批次照常预留推进。
		wanted := SourcingGreetingInvocation{
			InvocationID: req.InvocationID, BatchID: req.BatchID, RunID: req.RunID,
			ProfileID: req.ProfileID, ContextRevisionHash: req.ContextRevisionHash,
			RunContentHash: req.RunContentHash, Provider: req.Provider, Model: req.Model,
			InputHash: req.InputHash, Status: AIInvocationTransportFailed, StartedAt: req.StartedAt,
		}
		var existing SourcingGreetingInvocation
		err = tx.First(&existing, "invocation_id = ?", req.InvocationID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = tx.First(&existing, "run_id = ? OR profile_id = ?", req.RunID, req.ProfileID).Error
		}
		if err == nil {
			if !sameSourcingGreetingReservation(existing, wanted) {
				return ErrAIInvocationConflict
			}
			out.Invocation = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Create(&wanted).Error; err != nil {
			return err
		}
		out.Invocation = wanted
		out.Created = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CompleteSourcingGreeting 只完成既有 inFlight 预留。成功正文必须已经
// 完成 trim+NFC 规范化并满足发送边界；失败终局禁止携带正文或正文 hash。
func (s *Store) CompleteSourcingGreeting(req CompleteSourcingGreetingRequest) (*SourcingGreetingInvocation, error) {
	if err := validateInvocationCompletion(req.Completion); err != nil {
		return nil, err
	}
	if req.Completion.Status == AIInvocationOK {
		if !validPersistedGreetingText(req.GreetingText) || strings.TrimSpace(req.ContentHash) == "" ||
			req.ContentHash != sourcingGreetingContentHash(req.GreetingText) ||
			!reasoningCompletionSafe(req.Completion) {
			return nil, ErrAIInvocationInvalid
		}
	} else if req.GreetingText != "" || req.ContentHash != "" {
		return nil, ErrAIInvocationInvalid
	}
	var out SourcingGreetingInvocation
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&out, "invocation_id = ?", req.Completion.InvocationID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAIInvocationNotFound
			}
			return err
		}
		if out.FinishedAt != nil {
			if sameSourcingGreetingCompletion(out, req) {
				return nil
			}
			return ErrAIInvocationConflict
		}
		if out.Status != AIInvocationTransportFailed || out.GreetingText != "" || out.ContentHash != "" {
			return ErrAIInvocationConflict
		}
		updates := map[string]any{
			"status": req.Completion.Status, "greeting_text": req.GreetingText,
			"content_hash": req.ContentHash, "output_hash": req.Completion.OutputHash,
			"input_tokens": req.Completion.InputTokens, "cached_input_tokens": req.Completion.CachedInputTokens,
			"output_tokens": req.Completion.OutputTokens, "reasoning_tokens": req.Completion.ReasoningTokens,
			"usage_shape": req.Completion.UsageShape, "latency_ms": req.Completion.LatencyMs,
			"error_class": req.Completion.ErrorClass, "failure_stage": req.Completion.FailureStage,
			"error_detail_code":    req.Completion.ErrorDetailCode,
			"provider_http_status": req.Completion.ProviderHTTPStatus,
			"request_bytes":        req.Completion.RequestBytes, "response_bytes": req.Completion.ResponseBytes,
			"trace_status":          req.Completion.TraceStatus,
			"estimated_cost_micros": req.Completion.EstimatedCostMicros,
			"finished_at":           req.Completion.FinishedAt,
		}
		updated := tx.Model(&SourcingGreetingInvocation{}).
			Where("invocation_id = ? AND finished_at IS NULL AND status = ?",
				req.Completion.InvocationID, AIInvocationTransportFailed).
			Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrAIInvocationConflict
		}
		return tx.First(&out, "invocation_id = ?", req.Completion.InvocationID).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) SourcingBatchGreetingProgress(batchID string) (*SourcingBatchGreetingProgress, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return nil, ErrSourcingBatchInvalid
	}
	var progress SourcingBatchGreetingProgress
	err := s.db.Transaction(func(tx *gorm.DB) error {
		selection, materials, invocations, err := loadSourcingGreetingScopeTx(
			tx, batchID, false, "",
		)
		if err != nil {
			return err
		}
		progress = SourcingBatchGreetingProgress{
			BatchID: selection.BatchID, SelectedCount: selection.SelectedCount,
		}
		byRun := make(map[string]SourcingGreetingInvocation, len(invocations))
		for _, invocation := range invocations {
			byRun[invocation.RunID] = invocation
		}
		for _, material := range materials {
			invocation, exists := byRun[material.RunID]
			if !exists {
				progress.PendingCount++
				continue
			}
			// 混模型批次合法(2026-08-12 甲方裁决):Provider/Model 取首行、只作
			// 展示参考,不再要求全批一致;revision 一致性照旧硬校验。
			if progress.Provider == "" {
				progress.Provider, progress.Model = invocation.Provider, invocation.Model
			}
			if progress.ContextRevisionHash == "" {
				progress.ContextRevisionHash = invocation.ContextRevisionHash
			} else if progress.ContextRevisionHash != invocation.ContextRevisionHash {
				return ErrAIInvocationConflict
			}
			progress.InputTokens += int64(invocation.InputTokens)
			progress.CachedInputTokens += int64(invocation.CachedInputTokens)
			progress.OutputTokens += int64(invocation.OutputTokens)
			progress.EstimatedCostMicros += invocation.EstimatedCostMicros
			if invocation.FinishedAt == nil {
				if invocation.Status != AIInvocationTransportFailed || invocation.GreetingText != "" || invocation.ContentHash != "" {
					return ErrAIInvocationConflict
				}
				progress.InFlightCount++
				continue
			}
			if invocation.Status == AIInvocationOK {
				if !validPersistedGreetingText(invocation.GreetingText) || strings.TrimSpace(invocation.ContentHash) == "" {
					return ErrAIInvocationConflict
				}
				progress.OKCount++
				continue
			}
			if invocation.GreetingText != "" || invocation.ContentHash != "" {
				return ErrAIInvocationConflict
			}
			progress.FailedCount++
		}
		total := progress.OKCount + progress.FailedCount + progress.InFlightCount + progress.PendingCount
		if total != int64(progress.SelectedCount) {
			return ErrSourcingSelectionConflict
		}
		progress.Completed = progress.OKCount+progress.FailedCount == int64(progress.SelectedCount)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &progress, nil
}

// loadSourcingGreetingScopeTx 先重建并验证冻结的 selected 范围，再读取
// 与该范围相交的 invocation。只有 next/reserve 需要档案仍可生成；历史
// progress 允许档案在生成完成后继续推进业务状态。
func loadSourcingGreetingScopeTx(
	tx *gorm.DB,
	batchID string,
	requireCurrentSelected bool,
	wantedRevisionHash string,
) (SourcingBatchSelection, []SourcingGreetingMaterial, []SourcingGreetingInvocation, error) {
	batch, err := validateCompletedSourcingBatchForScoringTx(tx, strings.TrimSpace(batchID))
	if err != nil {
		return SourcingBatchSelection{}, nil, nil, err
	}
	if batch.PositionRef == nil || strings.TrimSpace(*batch.PositionRef) == "" {
		return SourcingBatchSelection{}, nil, nil, ErrSourcingBinding
	}
	var selection SourcingBatchSelection
	if err := tx.First(&selection, "batch_id = ?", batch.BatchID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return SourcingBatchSelection{}, nil, nil, ErrSourcingSelectionNotReady
		}
		return SourcingBatchSelection{}, nil, nil, err
	}
	if err := validatePersistedSourcingBatchSelectionTx(tx, batch, selection); err != nil {
		return SourcingBatchSelection{}, nil, nil, err
	}
	var runs []SourcingCandidateRun
	if err := tx.Where("batch_id = ?", batch.BatchID).
		Order("captured_at ASC, run_id ASC").Find(&runs).Error; err != nil {
		return SourcingBatchSelection{}, nil, nil, err
	}
	if len(runs) != batch.TargetCount {
		return SourcingBatchSelection{}, nil, nil, ErrSourcingBatchConflict
	}
	materials := make([]SourcingGreetingMaterial, 0, selection.SelectedCount)
	seenProfiles := make(map[string]struct{}, selection.SelectedCount)
	for i := range runs {
		run := runs[i]
		if run.BatchID == nil || *run.BatchID != batch.BatchID || run.Platform != batch.Platform ||
			run.AccountRef != batch.AccountRef || run.ContextRevisionHash != batch.ContextRevisionHash ||
			run.PositionRef != *batch.PositionRef || strings.TrimSpace(run.ContentHash) == "" {
			return SourcingBatchSelection{}, nil, nil, ErrSourcingBinding
		}
		var decision SourcingSelectionDecision
		if err := tx.First(&decision, "run_id = ?", run.RunID).Error; err != nil {
			return SourcingBatchSelection{}, nil, nil, ErrSourcingSelectionConflict
		}
		if decision.ContextRevisionHash != selection.ContextRevisionHash {
			return SourcingBatchSelection{}, nil, nil, ErrSourcingSelectionConflict
		}
		if decision.Outcome != SourcingSelectionSelected {
			if decision.ProfileID != nil {
				return SourcingBatchSelection{}, nil, nil, ErrSourcingSelectionConflict
			}
			continue
		}
		if decision.ProfileID == nil || strings.TrimSpace(*decision.ProfileID) == "" {
			return SourcingBatchSelection{}, nil, nil, ErrSourcingSelectionConflict
		}
		if _, duplicate := seenProfiles[*decision.ProfileID]; duplicate {
			return SourcingBatchSelection{}, nil, nil, ErrSourcingSelectionConflict
		}
		var profile CandidateProfile
		if err := tx.First(&profile, "profile_id = ?", *decision.ProfileID).Error; err != nil {
			return SourcingBatchSelection{}, nil, nil, ErrSourcingSelectionConflict
		}
		if profile.Platform != run.Platform || profile.AccountRef != run.AccountRef ||
			profile.PlatformUserRef != run.PlatformUserRef || profile.PositionRef != run.PositionRef ||
			profile.BackendJobID == nil || batch.BackendJobID == nil ||
			strings.TrimSpace(*profile.BackendJobID) != strings.TrimSpace(*batch.BackendJobID) {
			return SourcingBatchSelection{}, nil, nil, ErrSourcingBinding
		}
		if requireCurrentSelected &&
			(profile.MainStatus != CandidateProfileSelected || profile.EndReason != nil) {
			return SourcingBatchSelection{}, nil, nil, ErrSourcingBinding
		}
		seenProfiles[profile.ProfileID] = struct{}{}
		materials = append(materials, SourcingGreetingMaterial{
			BatchID: batch.BatchID, RunID: run.RunID,
			RunContentHash: run.ContentHash, ProfileID: profile.ProfileID,
			ResumeJSON: run.ResumeJSON, CapturedAt: run.CapturedAt,
		})
	}
	if len(materials) != selection.SelectedCount {
		return SourcingBatchSelection{}, nil, nil, ErrSourcingSelectionConflict
	}
	if len(materials) == 0 {
		return selection, materials, nil, nil
	}
	runIDs := make([]string, 0, len(materials))
	profileIDs := make([]string, 0, len(materials))
	materialByRun := make(map[string]SourcingGreetingMaterial, len(materials))
	for _, material := range materials {
		runIDs = append(runIDs, material.RunID)
		profileIDs = append(profileIDs, material.ProfileID)
		materialByRun[material.RunID] = material
	}
	var invocations []SourcingGreetingInvocation
	if err := tx.Where("batch_id = ? OR run_id IN ? OR profile_id IN ?", batch.BatchID, runIDs, profileIDs).
		Order("started_at, invocation_id").Find(&invocations).Error; err != nil {
		return SourcingBatchSelection{}, nil, nil, err
	}
	seenRuns := make(map[string]struct{}, len(invocations))
	seenInvocationProfiles := make(map[string]struct{}, len(invocations))
	stageRevisionHash := ""
	for _, invocation := range invocations {
		material, exists := materialByRun[invocation.RunID]
		if !exists || invocation.BatchID != batch.BatchID || invocation.ProfileID != material.ProfileID ||
			invocation.RunContentHash != material.RunContentHash || strings.TrimSpace(invocation.Provider) == "" ||
			strings.TrimSpace(invocation.Model) == "" || strings.TrimSpace(invocation.InputHash) == "" {
			return SourcingBatchSelection{}, nil, nil, ErrAIInvocationConflict
		}
		if _, duplicate := seenRuns[invocation.RunID]; duplicate {
			return SourcingBatchSelection{}, nil, nil, ErrAIInvocationConflict
		}
		if _, duplicate := seenInvocationProfiles[invocation.ProfileID]; duplicate {
			return SourcingBatchSelection{}, nil, nil, ErrAIInvocationConflict
		}
		seenRuns[invocation.RunID] = struct{}{}
		seenInvocationProfiles[invocation.ProfileID] = struct{}{}
		// 批内 provider/model 不再要求一致(2026-08-12 甲方裁决):引擎运行期
		// 可换代,混模型批次照常加载推进。
		if _, err := requireLegacyRevisionForSourcingBatchTx(
			tx, batch, invocation.ContextRevisionHash,
		); err != nil {
			return SourcingBatchSelection{}, nil, nil, ErrAIInvocationConflict
		}
		if stageRevisionHash == "" {
			stageRevisionHash = invocation.ContextRevisionHash
		} else if stageRevisionHash != invocation.ContextRevisionHash {
			return SourcingBatchSelection{}, nil, nil, ErrAIInvocationConflict
		}
	}
	wantedRevisionHash = strings.TrimSpace(wantedRevisionHash)
	if wantedRevisionHash != "" {
		if _, err := requireLegacyRevisionForSourcingBatchTx(
			tx, batch, wantedRevisionHash,
		); err != nil {
			return SourcingBatchSelection{}, nil, nil, ErrSourcingBinding
		}
		if stageRevisionHash != "" {
			if stageRevisionHash != wantedRevisionHash {
				return SourcingBatchSelection{}, nil, nil, ErrAIInvocationConflict
			}
		} else {
			current, err := currentLegacyRevisionForSourcingBatchTx(tx, batch)
			if err != nil {
				return SourcingBatchSelection{}, nil, nil, err
			}
			if current == nil || current.RevisionHash != wantedRevisionHash {
				return SourcingBatchSelection{}, nil, nil, ErrSourcingBinding
			}
			stageRevisionHash = wantedRevisionHash
		}
	}
	for index := range materials {
		materials[index].ContextRevisionHash = stageRevisionHash
	}
	return selection, materials, invocations, nil
}

func loadSourcingGreetingEffectMaterialTx(
	tx *gorm.DB,
	source SourcingGreetingEffectSource,
) (sourcingGreetingEffectMaterial, error) {
	source.BatchID = strings.TrimSpace(source.BatchID)
	source.InvocationID = strings.TrimSpace(source.InvocationID)
	if tx == nil || source.BatchID == "" || source.InvocationID == "" {
		return sourcingGreetingEffectMaterial{}, ErrSourcingGreetingEffectInvalid
	}
	batch, err := validateCompletedSourcingBatchForScoringTx(tx, source.BatchID)
	if err != nil {
		return sourcingGreetingEffectMaterial{}, err
	}
	if batch.PositionRef == nil || strings.TrimSpace(*batch.PositionRef) == "" {
		return sourcingGreetingEffectMaterial{}, ErrSourcingBinding
	}
	var selection SourcingBatchSelection
	if err := tx.First(&selection, "batch_id = ?", batch.BatchID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return sourcingGreetingEffectMaterial{}, ErrSourcingSelectionNotReady
		}
		return sourcingGreetingEffectMaterial{}, err
	}
	if err := validatePersistedSourcingBatchSelectionTx(tx, batch, selection); err != nil {
		return sourcingGreetingEffectMaterial{}, err
	}
	var invocation SourcingGreetingInvocation
	if err := tx.First(&invocation, "invocation_id = ?", source.InvocationID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return sourcingGreetingEffectMaterial{}, ErrAIInvocationNotFound
		}
		return sourcingGreetingEffectMaterial{}, err
	}
	var run SourcingCandidateRun
	if err := tx.First(&run, "run_id = ?", invocation.RunID).Error; err != nil {
		return sourcingGreetingEffectMaterial{}, ErrSourcingGreetingEffectConflict
	}
	var decision SourcingSelectionDecision
	if err := tx.First(&decision, "run_id = ?", run.RunID).Error; err != nil {
		return sourcingGreetingEffectMaterial{}, ErrSourcingGreetingEffectConflict
	}
	if decision.ProfileID == nil || strings.TrimSpace(*decision.ProfileID) == "" {
		return sourcingGreetingEffectMaterial{}, ErrSourcingGreetingEffectConflict
	}
	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", *decision.ProfileID).Error; err != nil {
		return sourcingGreetingEffectMaterial{}, ErrSourcingGreetingEffectConflict
	}
	var account Account
	if err := tx.First(&account,
		"platform = ? AND account_ref = ?", batch.Platform, batch.AccountRef,
	).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return sourcingGreetingEffectMaterial{}, ErrAccountNotFound
		}
		return sourcingGreetingEffectMaterial{}, err
	}
	material := sourcingGreetingEffectMaterial{
		Batch: batch, Selection: selection, Run: run, Decision: decision,
		Invocation: invocation, Profile: profile, Account: account,
	}
	if _, err := requireLegacyRevisionForSourcingBatchTx(
		tx, batch, invocation.ContextRevisionHash,
	); err != nil {
		return sourcingGreetingEffectMaterial{}, ErrSourcingGreetingEffectConflict
	}
	if err := validateSourcingGreetingGenerationMaterial(material, source); err != nil {
		return sourcingGreetingEffectMaterial{}, err
	}
	return material, nil
}

// loadSourcingGreetingSendBatchTx 与生成阶段的完整 scope 不同：它验证
// selection 的不可变绑定，但不要求已经成功发送的 profile 仍停留 selected。
func loadSourcingGreetingSendBatchTx(
	tx *gorm.DB,
	batchID string,
) ([]sourcingGreetingEffectMaterial, error) {
	batch, err := validateCompletedSourcingBatchForScoringTx(tx, batchID)
	if err != nil {
		return nil, err
	}
	if batch.PositionRef == nil || strings.TrimSpace(*batch.PositionRef) == "" {
		return nil, ErrSourcingBinding
	}
	var selection SourcingBatchSelection
	if err := tx.First(&selection, "batch_id = ?", batch.BatchID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSourcingSelectionNotReady
		}
		return nil, err
	}
	if err := validatePersistedSourcingBatchSelectionTx(tx, batch, selection); err != nil {
		return nil, err
	}
	var account Account
	if err := tx.First(&account,
		"platform = ? AND account_ref = ?", batch.Platform, batch.AccountRef,
	).Error; err != nil {
		return nil, err
	}
	var runs []SourcingCandidateRun
	if err := tx.Where("batch_id = ?", batch.BatchID).
		Order("captured_at ASC, run_id ASC").Find(&runs).Error; err != nil {
		return nil, err
	}
	var invocations []SourcingGreetingInvocation
	if err := tx.Where("batch_id = ?", batch.BatchID).Find(&invocations).Error; err != nil {
		return nil, err
	}
	invocationByRun := make(map[string]SourcingGreetingInvocation, len(invocations))
	stageRevisionHash := ""
	for _, invocation := range invocations {
		if _, duplicate := invocationByRun[invocation.RunID]; duplicate {
			return nil, ErrSourcingGreetingEffectConflict
		}
		if _, err := requireLegacyRevisionForSourcingBatchTx(
			tx, batch, invocation.ContextRevisionHash,
		); err != nil {
			return nil, ErrSourcingGreetingEffectConflict
		}
		if stageRevisionHash == "" {
			stageRevisionHash = invocation.ContextRevisionHash
		} else if stageRevisionHash != invocation.ContextRevisionHash {
			return nil, ErrSourcingGreetingEffectConflict
		}
		invocationByRun[invocation.RunID] = invocation
	}
	materials := make([]sourcingGreetingEffectMaterial, 0, selection.SelectedCount)
	consumedInvocations := 0
	for i := range runs {
		run := runs[i]
		var decision SourcingSelectionDecision
		if err := tx.First(&decision, "run_id = ?", run.RunID).Error; err != nil {
			return nil, ErrSourcingSelectionConflict
		}
		if decision.Outcome != SourcingSelectionSelected {
			if _, exists := invocationByRun[run.RunID]; exists {
				return nil, ErrSourcingGreetingEffectConflict
			}
			continue
		}
		if decision.ProfileID == nil || strings.TrimSpace(*decision.ProfileID) == "" {
			return nil, ErrSourcingSelectionConflict
		}
		var profile CandidateProfile
		if err := tx.First(&profile, "profile_id = ?", *decision.ProfileID).Error; err != nil {
			return nil, ErrSourcingSelectionConflict
		}
		material := sourcingGreetingEffectMaterial{
			Batch: batch, Selection: selection, Run: run, Decision: decision,
			Profile: profile, Account: account,
		}
		if invocation, exists := invocationByRun[run.RunID]; exists {
			material.Invocation = invocation
			consumedInvocations++
			if err := validateSourcingGreetingInvocationBinding(material, SourcingGreetingEffectSource{
				BatchID: batch.BatchID, InvocationID: invocation.InvocationID,
			}); err != nil {
				return nil, err
			}
		} else if err := validateSourcingGreetingSelectedBinding(material); err != nil {
			return nil, err
		}
		materials = append(materials, material)
	}
	if len(materials) != selection.SelectedCount || consumedInvocations != len(invocations) {
		return nil, ErrSourcingGreetingEffectConflict
	}
	return materials, nil
}

func validateSourcingGreetingSelectedBinding(material sourcingGreetingEffectMaterial) error {
	batch, run, decision, profile := material.Batch, material.Run, material.Decision, material.Profile
	if run.BatchID == nil || *run.BatchID != batch.BatchID || run.Platform != batch.Platform ||
		run.AccountRef != batch.AccountRef || run.ContextRevisionHash != batch.ContextRevisionHash ||
		batch.PositionRef == nil || run.PositionRef != *batch.PositionRef || strings.TrimSpace(run.ContentHash) == "" ||
		decision.RunID != run.RunID || decision.ContextRevisionHash != material.Selection.ContextRevisionHash ||
		decision.Outcome != SourcingSelectionSelected || decision.ProfileID == nil ||
		*decision.ProfileID != profile.ProfileID || profile.Platform != run.Platform ||
		profile.AccountRef != run.AccountRef || profile.PlatformUserRef != run.PlatformUserRef ||
		profile.PositionRef != run.PositionRef || profile.BackendJobID == nil ||
		batch.BackendJobID == nil ||
		strings.TrimSpace(*profile.BackendJobID) != strings.TrimSpace(*batch.BackendJobID) {
		return ErrSourcingGreetingEffectConflict
	}
	return nil
}

func validateSourcingGreetingGenerationMaterial(
	material sourcingGreetingEffectMaterial,
	source SourcingGreetingEffectSource,
) error {
	if err := validateSourcingGreetingInvocationBinding(material, source); err != nil {
		return err
	}
	invocation := material.Invocation
	if invocation.Status != AIInvocationOK || invocation.FinishedAt == nil ||
		!validPersistedGreetingText(invocation.GreetingText) ||
		invocation.ContentHash != sourcingGreetingContentHash(invocation.GreetingText) ||
		(invocation.ReasoningTokens != nil && *invocation.ReasoningTokens != 0) {
		return ErrSourcingGreetingEffectConflict
	}
	return nil
}

func validateSourcingGreetingInvocationBinding(
	material sourcingGreetingEffectMaterial,
	source SourcingGreetingEffectSource,
) error {
	if err := validateSourcingGreetingSelectedBinding(material); err != nil {
		return err
	}
	invocation := material.Invocation
	if source.BatchID != material.Batch.BatchID || source.InvocationID != invocation.InvocationID ||
		invocation.BatchID != material.Batch.BatchID || invocation.RunID != material.Run.RunID ||
		invocation.ProfileID != material.Profile.ProfileID ||
		invocation.RunContentHash != material.Run.ContentHash || strings.TrimSpace(invocation.Provider) == "" ||
		strings.TrimSpace(invocation.Model) == "" || strings.TrimSpace(invocation.InputHash) == "" ||
		(invocation.EffectIntentID == nil) != (invocation.EffectStartedAt == nil) {
		return ErrSourcingGreetingEffectConflict
	}
	if invocation.FinishedAt == nil {
		if invocation.Status != AIInvocationTransportFailed || invocation.GreetingText != "" ||
			invocation.ContentHash != "" || invocation.EffectIntentID != nil {
			return ErrSourcingGreetingEffectConflict
		}
		return nil
	}
	if invocation.Status == AIInvocationOK {
		if !validPersistedGreetingText(invocation.GreetingText) ||
			invocation.ContentHash != sourcingGreetingContentHash(invocation.GreetingText) ||
			(invocation.ReasoningTokens != nil && *invocation.ReasoningTokens != 0) {
			return ErrSourcingGreetingEffectConflict
		}
		return nil
	}
	if invocation.GreetingText != "" || invocation.ContentHash != "" || invocation.EffectIntentID != nil {
		return ErrSourcingGreetingEffectConflict
	}
	return nil
}

func sourcingGreetingProfileAllowsNewEffect(profile CandidateProfile) bool {
	return profile.MainStatus == CandidateProfileSelected && profile.EndReason == nil &&
		profile.SuccessfulGreetingIntentID == nil && profile.ConversationRef == nil &&
		profile.GreetedAt == nil
}

func sourcingGreetingFeedChanged(material sourcingGreetingEffectMaterial) bool {
	invalidatedAt := material.Account.SourcingFeedInvalidatedAt
	return invalidatedAt != nil && !invalidatedAt.Before(material.Batch.StartedAt)
}

func validateSourcingGreetingEffectCreationTx(
	tx *gorm.DB,
	source SourcingGreetingEffectSource,
	intent EffectIntent,
	command CmdRecord,
) (sourcingGreetingEffectMaterial, error) {
	material, err := loadSourcingGreetingEffectMaterialTx(tx, source)
	if err != nil {
		return sourcingGreetingEffectMaterial{}, err
	}
	if sourcingGreetingFeedChanged(material) {
		return sourcingGreetingEffectMaterial{}, ErrSourcingGreetingFeedChanged
	}
	if material.Invocation.EffectIntentID != nil {
		return sourcingGreetingEffectMaterial{}, ErrSourcingGreetingEffectConflict
	}
	if !sourcingGreetingProfileAllowsNewEffect(material.Profile) {
		return sourcingGreetingEffectMaterial{}, ErrSourcingGreetingEffectConflict
	}
	if err := validateSourcingGreetingIntentCommand(material, intent, command); err != nil {
		return sourcingGreetingEffectMaterial{}, err
	}
	return material, nil
}

func bindSourcingGreetingEffectTx(
	tx *gorm.DB,
	source SourcingGreetingEffectSource,
	intentID string,
	at time.Time,
) error {
	expectedIntentID, err := SourcingGreetingEffectIntentID(source.InvocationID)
	if err != nil || intentID != expectedIntentID || at.IsZero() {
		return ErrSourcingGreetingEffectInvalid
	}
	updated := tx.Model(&SourcingGreetingInvocation{}).
		Where("invocation_id = ? AND batch_id = ? AND effect_intent_id IS NULL AND effect_started_at IS NULL",
			source.InvocationID, source.BatchID).
		Updates(map[string]any{"effect_intent_id": intentID, "effect_started_at": at})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrSourcingGreetingEffectConflict
	}
	return nil
}

func validateSourcingGreetingEffectReplayTx(
	tx *gorm.DB,
	source SourcingGreetingEffectSource,
	intent EffectIntent,
	command CmdRecord,
) error {
	material, err := loadSourcingGreetingEffectMaterialTx(tx, source)
	if err != nil {
		return err
	}
	return validateSourcingGreetingEffectReplayMaterial(material, source, intent, command)
}

func validateSourcingGreetingEffectReplayMaterial(
	material sourcingGreetingEffectMaterial,
	source SourcingGreetingEffectSource,
	intent EffectIntent,
	command CmdRecord,
) error {
	expectedIntentID, err := SourcingGreetingEffectIntentID(source.InvocationID)
	if err != nil || material.Invocation.EffectIntentID == nil ||
		*material.Invocation.EffectIntentID != expectedIntentID || material.Invocation.EffectStartedAt == nil ||
		intent.IntentID != expectedIntentID {
		return ErrSourcingGreetingEffectConflict
	}
	return validateSourcingGreetingIntentCommand(material, intent, command)
}

func validateSourcingGreetingIntentCommand(
	material sourcingGreetingEffectMaterial,
	intent EffectIntent,
	command CmdRecord,
) error {
	expectedIntentID, err := SourcingGreetingEffectIntentID(material.Invocation.InvocationID)
	if err != nil {
		return err
	}
	meta, exists := protocol.Primitives[protocol.PrimChatSendGreeting]
	if !exists || meta.Ver == 0 {
		return ErrSourcingGreetingEffectInvalid
	}
	argsRaw := json.RawMessage(command.Args)
	var args protocol.ChatSendGreetingArgs
	if protocol.ValidatePrimitiveArgs(protocol.PrimChatSendGreeting, meta.Ver, argsRaw) != nil ||
		json.Unmarshal(argsRaw, &args) != nil || args.PlatformUserRef != material.Profile.PlatformUserRef ||
		args.PositionRef != material.Profile.PositionRef || args.Text != material.Invocation.GreetingText {
		return ErrSourcingGreetingEffectConflict
	}
	guardsRaw := json.RawMessage(command.Guards)
	var guards protocol.ChatSendGreetingGuards
	if protocol.ValidatePrimitiveGuards(protocol.PrimChatSendGreeting, meta.Ver, guardsRaw) != nil ||
		json.Unmarshal(guardsRaw, &guards) != nil || !guards.ExpectUnestablished {
		return ErrSourcingGreetingEffectConflict
	}
	var context protocol.CmdContext
	if json.Unmarshal([]byte(command.ContextJSON), &context) != nil ||
		context.Platform != material.Profile.Platform || context.AccountRef != material.Profile.AccountRef ||
		command.ExpectedPrincipalFingerprint == "" ||
		context.ExpectedPrincipalFingerprint != command.ExpectedPrincipalFingerprint {
		return ErrSourcingGreetingEffectConflict
	}
	expectedIdemKey := fmt.Sprintf("ik1:%s:%s:%s:%s:%s",
		material.Profile.Platform, material.Profile.AccountRef, primitiveChatSendGreeting,
		material.Profile.ProfileID, expectedIntentID,
	)
	if intent.IntentID != expectedIntentID || intent.IdemKey != expectedIdemKey ||
		intent.Platform != material.Profile.Platform || intent.AccountRef != material.Profile.AccountRef ||
		intent.Primitive != primitiveChatSendGreeting || intent.TargetRef != material.Profile.ProfileID ||
		intent.PayloadHash != sourcingGreetingContentHash(command.Args) ||
		intent.GuardsHash != sourcingGreetingContentHash(command.Guards) ||
		intent.SendFingerprint != sourcingGreetingSendFingerprint(material.Invocation.GreetingText) ||
		command.IntentID != intent.IntentID || command.IdemKey != intent.IdemKey ||
		command.Platform != intent.Platform || command.AccountRef != intent.AccountRef ||
		command.Name != primitiveChatSendGreeting || command.Class != "effectful" ||
		command.Domain != intent.Platform+":"+intent.AccountRef || command.MsgID != intent.RootMsgID {
		return ErrSourcingGreetingEffectConflict
	}
	return nil
}

// provider/model 刻意不参与预留同一性:引擎运行期可换代,旧引擎预留的行由
// 新引擎按原身份接手,行上保留预留时刻的 provider/model 事实。
func sameSourcingGreetingReservation(existing, wanted SourcingGreetingInvocation) bool {
	return existing.BatchID == wanted.BatchID && existing.RunID == wanted.RunID &&
		existing.ProfileID == wanted.ProfileID && existing.ContextRevisionHash == wanted.ContextRevisionHash &&
		existing.RunContentHash == wanted.RunContentHash && existing.InputHash == wanted.InputHash
}

func sameSourcingGreetingCompletion(existing SourcingGreetingInvocation, req CompleteSourcingGreetingRequest) bool {
	completion := req.Completion
	return existing.Status == completion.Status && existing.GreetingText == req.GreetingText &&
		existing.ContentHash == req.ContentHash && existing.OutputHash == completion.OutputHash &&
		existing.InputTokens == completion.InputTokens && existing.CachedInputTokens == completion.CachedInputTokens &&
		existing.OutputTokens == completion.OutputTokens &&
		sameOptionalInt(existing.ReasoningTokens, completion.ReasoningTokens) &&
		existing.UsageShape == completion.UsageShape && existing.LatencyMs == completion.LatencyMs &&
		existing.ErrorClass == completion.ErrorClass && existing.FailureStage == completion.FailureStage &&
		existing.ErrorDetailCode == completion.ErrorDetailCode &&
		sameOptionalInt(existing.ProviderHTTPStatus, completion.ProviderHTTPStatus) &&
		existing.RequestBytes == completion.RequestBytes && existing.ResponseBytes == completion.ResponseBytes &&
		existing.TraceStatus == completion.TraceStatus &&
		existing.EstimatedCostMicros == completion.EstimatedCostMicros && existing.FinishedAt != nil &&
		existing.FinishedAt.Equal(completion.FinishedAt)
}

func validPersistedGreetingText(text string) bool {
	return text != "" && text == strings.TrimSpace(text) && utf8.ValidString(text) &&
		len([]byte(text)) <= maxSourcingGreetingTextBytes && norm.NFC.IsNormalString(text)
}

func sourcingGreetingContentHash(text string) string {
	digest := sha256.Sum256([]byte(text))
	return hex.EncodeToString(digest[:])
}

func sourcingGreetingSendFingerprint(text string) string {
	normalized := strings.Join(strings.Fields(norm.NFC.String(text)), " ")
	return sourcingGreetingContentHash(normalized)
}
