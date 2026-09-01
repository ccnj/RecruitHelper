// 当日职位计划(AGENTS.md「当日职位计划与招呼配额分摊」,2026-09-01 甲方裁决)。
// 本文件只管计划事实本身:建计划、定稿、条目状态与配额数学。计划如何驱动
// 运行(接续、跳过、收口)在 productworkflow 编排层;份额如何进入筛选在
// m6_sourcing_selection_store 的 override 分支。
package store

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/internal/ids"
)

const (
	DailyJobPlanDraft      = "draft"
	DailyJobPlanActive     = "active"
	DailyJobPlanCompleted  = "completed"
	DailyJobPlanAborted    = "aborted"
	DailyJobPlanSuperseded = "superseded"

	DailyJobPlanEntryPending = "pending"
	DailyJobPlanEntrySkipped = "skipped"
	DailyJobPlanEntryDone    = "done"

	// DailyJobPlanSkipZeroQuota:总量小于职位数时计划序靠后条目份额为 0,
	// 不开批(方向少发,不加机制)。
	DailyJobPlanSkipZeroQuota = "zeroQuota"
	// DailyJobPlanSkipNotOnlineAtPlan 前缀 + 平台分区原样文案(含「平台未见」)。
	DailyJobPlanSkipNotOnlineAtPlan = "jobNotOnlineAtPlan"
)

var (
	ErrDailyJobPlanInvalid       = errors.New("当日职位计划输入无效")
	ErrDailyJobPlanNoJobs        = errors.New("当日没有配置合格的职位,无法建立职位计划")
	ErrDailyJobPlanZeroQuota     = errors.New("当日招呼总量为 0,不建立职位计划")
	ErrDailyJobPlanNotFound      = errors.New("当日职位计划不存在")
	ErrDailyJobPlanStateConflict = errors.New("当日职位计划状态冲突")
)

// StableDailyPlanQuota 抽取当日招呼总量 T。与批次配额 stableSourcingSelectionTarget
// 同款手法:哈希稳定,脑重启重算必得同值,计划不漂移(设计文档「总量抽取」)。
func StableDailyPlanQuota(planID, localDate string, targetMin, targetMax int) int {
	if targetMin > targetMax {
		targetMin, targetMax = targetMax, targetMin
	}
	if targetMin == targetMax {
		return targetMin
	}
	digest := sha256.Sum256([]byte("dailyJobPlan|" + planID + "|" + localDate))
	span := uint64(targetMax - targetMin + 1)
	return targetMin + int(binary.BigEndian.Uint64(digest[:8])%span)
}

// DailyPlanShare 是整除打底+余数按计划序前置逐个加一的份额分摊。rank 是该
// 条目在参与分摊职位里的序位(0 起)。总和恰等于 total,任何环节不向上取整。
func DailyPlanShare(total, jobs, rank int) int {
	if jobs <= 0 || rank < 0 || rank >= jobs || total <= 0 {
		return 0
	}
	share := total / jobs
	if rank < total%jobs {
		share++
	}
	return share
}

// 采集规模与份额联动(2026-09-01 甲方改定系数):首轮 ceil(1.5×份额)、续采
// 步进 ceil(0.5×份额)、整批上限 3×份额。采集不产生候选人可见动作,向上取整
// 不违反少发方向。
func PlanCaptureFirstRound(share int) int {
	if share <= 0 {
		return 0
	}
	return (3*share + 1) / 2
}

func PlanCaptureStep(share int) int {
	if share <= 0 {
		return 0
	}
	return (share + 1) / 2
}

func PlanCaptureLimit(share int) int {
	if share <= 0 {
		return 0
	}
	return 3 * share
}

// DailyJobPlanShareForEntry 给出条目此刻的份额。定稿后用冻结配额;draft 期间
// 给临时份额(以当前未跳过条目数 N₀ 计,N₀≥N 故临时份额≤定稿份额,首批少采由
// 既有续采轮自愈,方向永不多采)。
func DailyJobPlanShareForEntry(
	plan *DailyJobPlan,
	entries []DailyJobPlanEntry,
	entryID string,
) int {
	if plan == nil {
		return 0
	}
	var target *DailyJobPlanEntry
	for index := range entries {
		if entries[index].EntryID == entryID {
			target = &entries[index]
			break
		}
	}
	if target == nil || target.Status == DailyJobPlanEntrySkipped {
		return 0
	}
	if plan.Status == DailyJobPlanActive {
		return target.Quota
	}
	if plan.Status != DailyJobPlanDraft {
		return 0
	}
	rank, count := -1, 0
	for index := range entries {
		if entries[index].Status == DailyJobPlanEntrySkipped {
			continue
		}
		if entries[index].EntryID == entryID {
			rank = count
		}
		count++
	}
	return DailyPlanShare(plan.TotalQuota, count, rank)
}

// NextPendingDailyJobPlanEntry 返回计划序第一个 pending 条目;定稿后 pending
// 条目份额恒>0(零份额在定稿时已标跳过)。
func NextPendingDailyJobPlanEntry(entries []DailyJobPlanEntry) *DailyJobPlanEntry {
	for index := range entries {
		if entries[index].Status == DailyJobPlanEntryPending {
			return &entries[index]
		}
	}
	return nil
}

// DailyJobPlanEntryByRevision 按配置版本定位条目。同一计划内职位互异,revision
// 与条目一一对应;批次→条目的一切归属判定都走这条路,不依赖 BatchID 诊断锚。
func DailyJobPlanEntryByRevision(
	entries []DailyJobPlanEntry,
	revisionHash string,
) *DailyJobPlanEntry {
	revisionHash = strings.TrimSpace(revisionHash)
	if revisionHash == "" {
		return nil
	}
	for index := range entries {
		if entries[index].RevisionHash == revisionHash {
			return &entries[index]
		}
	}
	return nil
}

type DailyJobPlanWithEntries struct {
	Plan    DailyJobPlan
	Entries []DailyJobPlanEntry
}

// CreateDailyJobPlan 建立当日计划:有效职位集(后台合格)按后台职位 ID 数值
// 升序成序,总量按计划序第一职位配置稳定抽取并落库。既有 draft/active 计划
// 一律标 superseded——每次显式开始都是一份全新授权。
func (s *Store) CreateDailyJobPlan(
	key AccountKey,
	localDate string,
	now time.Time,
) (*DailyJobPlanWithEntries, error) {
	key.Platform = strings.TrimSpace(key.Platform)
	key.AccountRef = strings.TrimSpace(key.AccountRef)
	localDate = strings.TrimSpace(localDate)
	if key.Platform == "" || key.AccountRef == "" || localDate == "" || now.IsZero() {
		return nil, ErrDailyJobPlanInvalid
	}
	var out DailyJobPlanWithEntries
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var heads []JobAIContextHead
		if err := tx.Where(
			"source_kind = ? AND inbound_eligible = ?",
			legacyJobConfigSourceKind, true,
		).Find(&heads).Error; err != nil {
			return err
		}
		if len(heads) == 0 {
			return ErrDailyJobPlanNoJobs
		}
		type planJob struct {
			backendJobID string
			jobName      string
			revisionHash string
			revision     *JobAIContextRevision
		}
		jobs := make([]planJob, 0, len(heads))
		for index := range heads {
			head := heads[index]
			var revision JobAIContextRevision
			if err := tx.First(
				&revision, "revision_hash = ?", head.RevisionHash,
			).Error; err != nil {
				return fmt.Errorf("有效职位 %s 的配置版本缺失: %w", head.SourceJobRef, err)
			}
			jobs = append(jobs, planJob{
				backendJobID: head.SourceJobRef,
				jobName:      revision.DisplayName,
				revisionHash: revision.RevisionHash,
				revision:     &revision,
			})
		}
		sort.SliceStable(jobs, func(i, j int) bool {
			left, leftErr := strconv.Atoi(jobs[i].backendJobID)
			right, rightErr := strconv.Atoi(jobs[j].backendJobID)
			if leftErr == nil && rightErr == nil {
				return left < right
			}
			if leftErr == nil {
				return true
			}
			if rightErr == nil {
				return false
			}
			return jobs[i].backendJobID < jobs[j].backendJobID
		})

		view, err := m5ai.DeriveSourcingView(jobs[0].revision.SourcePackage)
		if err != nil {
			return err
		}
		selection := view.CandidateSelection
		planID := ids.NewDailyJobPlanID()
		total := StableDailyPlanQuota(planID, localDate, selection.TargetMin, selection.TargetMax)
		if total <= 0 {
			return ErrDailyJobPlanZeroQuota
		}

		if err := tx.Model(&DailyJobPlan{}).
			Where("platform = ? AND account_ref = ? AND status IN ?",
				key.Platform, key.AccountRef,
				[]string{DailyJobPlanDraft, DailyJobPlanActive}).
			Updates(map[string]any{
				"status":     DailyJobPlanSuperseded,
				"end_reason": "superseded",
				"ended_at":   now,
				"updated_at": now,
			}).Error; err != nil {
			return err
		}

		plan := DailyJobPlan{
			PlanID: planID, Platform: key.Platform, AccountRef: key.AccountRef,
			LocalDate: localDate, Status: DailyJobPlanDraft,
			TotalQuota: total, QuotaSourceJobID: jobs[0].backendJobID,
			TargetMin: selection.TargetMin, TargetMax: selection.TargetMax,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&plan).Error; err != nil {
			return err
		}
		entries := make([]DailyJobPlanEntry, 0, len(jobs))
		for index := range jobs {
			entry := DailyJobPlanEntry{
				EntryID: fmt.Sprintf("%s-e%02d", planID, index+1),
				PlanID:  planID, Seq: index + 1,
				BackendJobID: jobs[index].backendJobID,
				JobName:      jobs[index].jobName,
				RevisionHash: jobs[index].revisionHash,
				Status:       DailyJobPlanEntryPending,
				CreatedAt:    now, UpdatedAt: now,
			}
			if err := tx.Create(&entry).Error; err != nil {
				return err
			}
			entries = append(entries, entry)
		}
		out = DailyJobPlanWithEntries{Plan: plan, Entries: entries}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ActiveDailyJobPlan 返回该账号当前 draft/active 计划及全部条目(计划序)。
func (s *Store) ActiveDailyJobPlan(
	key AccountKey,
) (*DailyJobPlan, []DailyJobPlanEntry, error) {
	key.Platform = strings.TrimSpace(key.Platform)
	key.AccountRef = strings.TrimSpace(key.AccountRef)
	if key.Platform == "" || key.AccountRef == "" {
		return nil, nil, ErrDailyJobPlanInvalid
	}
	var plan DailyJobPlan
	err := s.db.Where(
		"platform = ? AND account_ref = ? AND status IN ?",
		key.Platform, key.AccountRef,
		[]string{DailyJobPlanDraft, DailyJobPlanActive},
	).Order("created_at DESC").First(&plan).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	entries, err := s.dailyJobPlanEntries(plan.PlanID)
	if err != nil {
		return nil, nil, err
	}
	return &plan, entries, nil
}

// DailyJobPlanByID 读取任意状态的计划及条目(诊断/投影/测试)。
func (s *Store) DailyJobPlanByID(planID string) (*DailyJobPlanWithEntries, error) {
	planID = strings.TrimSpace(planID)
	if planID == "" {
		return nil, ErrDailyJobPlanInvalid
	}
	var plan DailyJobPlan
	err := s.db.First(&plan, "plan_id = ?", planID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entries, err := s.dailyJobPlanEntries(planID)
	if err != nil {
		return nil, err
	}
	return &DailyJobPlanWithEntries{Plan: plan, Entries: entries}, nil
}

// ActiveDailyJobPlans 列出全部 draft/active 计划(不限账号),供无活跃运行时
// 的收口扫描使用。现网单账号,实际至多一份。
func (s *Store) ActiveDailyJobPlans() ([]DailyJobPlanWithEntries, error) {
	var plans []DailyJobPlan
	if err := s.db.Where(
		"status IN ?", []string{DailyJobPlanDraft, DailyJobPlanActive},
	).Order("created_at ASC").Find(&plans).Error; err != nil {
		return nil, err
	}
	out := make([]DailyJobPlanWithEntries, 0, len(plans))
	for index := range plans {
		entries, err := s.dailyJobPlanEntries(plans[index].PlanID)
		if err != nil {
			return nil, err
		}
		out = append(out, DailyJobPlanWithEntries{Plan: plans[index], Entries: entries})
	}
	return out, nil
}

func (s *Store) dailyJobPlanEntries(planID string) ([]DailyJobPlanEntry, error) {
	var entries []DailyJobPlanEntry
	if err := s.db.Where("plan_id = ?", planID).
		Order("seq ASC").Find(&entries).Error; err != nil {
		return nil, err
	}
	return entries, nil
}

// DailyJobPlanGateObservation 是批 actor 从状态闸读取里为一个 pending 条目
// 带回的在线裁决。判定(名字归一化、分区匹配)在 patrol 层完成,store 只落账。
type DailyJobPlanGateObservation struct {
	Seq         int
	Online      bool
	StatusLabel string
}

// FinalizeDailyJobPlan 在首批状态闸读取时定稿:离线条目标跳过,N 冻结,份额
// 按计划序落库,零份额条目标跳过。active 幂等返回;observations 必须覆盖全部
// pending 条目,少一个都是冲突——定稿是一锤子事务,不做部分定稿。
func (s *Store) FinalizeDailyJobPlan(
	planID string,
	observations []DailyJobPlanGateObservation,
	at time.Time,
) (*DailyJobPlanWithEntries, error) {
	planID = strings.TrimSpace(planID)
	if planID == "" || at.IsZero() {
		return nil, ErrDailyJobPlanInvalid
	}
	var out DailyJobPlanWithEntries
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var plan DailyJobPlan
		if err := tx.First(&plan, "plan_id = ?", planID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrDailyJobPlanNotFound
			}
			return err
		}
		var entries []DailyJobPlanEntry
		if err := tx.Where("plan_id = ?", planID).
			Order("seq ASC").Find(&entries).Error; err != nil {
			return err
		}
		if plan.Status == DailyJobPlanActive {
			out = DailyJobPlanWithEntries{Plan: plan, Entries: entries}
			return nil
		}
		if plan.Status != DailyJobPlanDraft {
			return ErrDailyJobPlanStateConflict
		}
		observed := make(map[int]DailyJobPlanGateObservation, len(observations))
		for index := range observations {
			observed[observations[index].Seq] = observations[index]
		}
		type pendingEntry struct {
			index int
		}
		online := make([]pendingEntry, 0, len(entries))
		for index := range entries {
			if entries[index].Status != DailyJobPlanEntryPending {
				continue
			}
			observation, found := observed[entries[index].Seq]
			if !found {
				return ErrDailyJobPlanStateConflict
			}
			if observation.Online {
				online = append(online, pendingEntry{index: index})
				continue
			}
			entries[index].Status = DailyJobPlanEntrySkipped
			entries[index].SkipReason = DailyJobPlanSkipNotOnlineAtPlan + ":" + observation.StatusLabel
			entries[index].UpdatedAt = at
		}
		jobCount := len(online)
		for rank, pending := range online {
			quota := DailyPlanShare(plan.TotalQuota, jobCount, rank)
			entries[pending.index].Quota = quota
			entries[pending.index].UpdatedAt = at
			if quota == 0 {
				entries[pending.index].Status = DailyJobPlanEntrySkipped
				entries[pending.index].SkipReason = DailyJobPlanSkipZeroQuota
			}
		}
		for index := range entries {
			if err := tx.Model(&DailyJobPlanEntry{}).
				Where("entry_id = ?", entries[index].EntryID).
				Updates(map[string]any{
					"quota":       entries[index].Quota,
					"status":      entries[index].Status,
					"skip_reason": entries[index].SkipReason,
					"updated_at":  entries[index].UpdatedAt,
				}).Error; err != nil {
				return err
			}
		}
		finalizedAt := at
		plan.Status = DailyJobPlanActive
		plan.JobCount = jobCount
		plan.FinalizedAt = &finalizedAt
		plan.UpdatedAt = at
		if err := tx.Model(&DailyJobPlan{}).
			Where("plan_id = ? AND status = ?", planID, DailyJobPlanDraft).
			Updates(map[string]any{
				"status":       DailyJobPlanActive,
				"job_count":    jobCount,
				"finalized_at": finalizedAt,
				"updated_at":   at,
			}).Error; err != nil {
			return err
		}
		out = DailyJobPlanWithEntries{Plan: plan, Entries: entries}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// SkipDailyJobPlanEntry 把一个 pending 条目标为跳过(份额不挪,2026-09-01 甲方
// 裁决第 4 条)。已跳过幂等;done 条目不可回改。
func (s *Store) SkipDailyJobPlanEntry(
	planID string,
	seq int,
	reason string,
	at time.Time,
) error {
	planID = strings.TrimSpace(planID)
	reason = strings.TrimSpace(reason)
	if planID == "" || seq <= 0 || reason == "" || at.IsZero() {
		return ErrDailyJobPlanInvalid
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var entry DailyJobPlanEntry
		if err := tx.First(
			&entry, "plan_id = ? AND seq = ?", planID, seq,
		).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrDailyJobPlanNotFound
			}
			return err
		}
		if entry.Status == DailyJobPlanEntrySkipped {
			return nil
		}
		if entry.Status != DailyJobPlanEntryPending {
			return ErrDailyJobPlanStateConflict
		}
		return tx.Model(&DailyJobPlanEntry{}).
			Where("entry_id = ? AND status = ?", entry.EntryID, DailyJobPlanEntryPending).
			Updates(map[string]any{
				"status":      DailyJobPlanEntrySkipped,
				"skip_reason": reason,
				"updated_at":  at,
			}).Error
	})
}

// MarkDailyJobPlanEntryDone 在条目批次发送全部终局、运行进入沟通并接续/收口
// 时落账。幂等;pending 之外的状态不可转 done。
func (s *Store) MarkDailyJobPlanEntryDone(
	planID string,
	seq int,
	batchID string,
	at time.Time,
) error {
	planID = strings.TrimSpace(planID)
	batchID = strings.TrimSpace(batchID)
	if planID == "" || seq <= 0 || at.IsZero() {
		return ErrDailyJobPlanInvalid
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var entry DailyJobPlanEntry
		if err := tx.First(
			&entry, "plan_id = ? AND seq = ?", planID, seq,
		).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrDailyJobPlanNotFound
			}
			return err
		}
		if entry.Status == DailyJobPlanEntryDone {
			return nil
		}
		if entry.Status != DailyJobPlanEntryPending {
			return ErrDailyJobPlanStateConflict
		}
		updates := map[string]any{
			"status":     DailyJobPlanEntryDone,
			"done_at":    at,
			"updated_at": at,
		}
		if batchID != "" && entry.BatchID == "" {
			updates["batch_id"] = batchID
		}
		return tx.Model(&DailyJobPlanEntry{}).
			Where("entry_id = ? AND status = ?", entry.EntryID, DailyJobPlanEntryPending).
			Updates(updates).Error
	})
}

// StampDailyJobPlanEntryBatch 落诊断锚(仅空时写入),失败只影响可读性不影响
// 正确性,调用方按 best-effort 处理。
func (s *Store) StampDailyJobPlanEntryBatch(planID string, seq int, batchID string) error {
	planID = strings.TrimSpace(planID)
	batchID = strings.TrimSpace(batchID)
	if planID == "" || seq <= 0 || batchID == "" {
		return ErrDailyJobPlanInvalid
	}
	return s.db.Model(&DailyJobPlanEntry{}).
		Where("plan_id = ? AND seq = ? AND batch_id = ''", planID, seq).
		Update("batch_id", batchID).Error
}

// CompleteDailyJobPlan / AbortDailyJobPlan 收口计划。幂等到同终态;不同终态
// 冲突报错——终态互相覆盖等于抹审计。
func (s *Store) CompleteDailyJobPlan(planID string, at time.Time) error {
	return s.endDailyJobPlan(planID, DailyJobPlanCompleted, "", at)
}

func (s *Store) AbortDailyJobPlan(planID, reason string, at time.Time) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ErrDailyJobPlanInvalid
	}
	return s.endDailyJobPlan(planID, DailyJobPlanAborted, reason, at)
}

func (s *Store) endDailyJobPlan(planID, status, reason string, at time.Time) error {
	planID = strings.TrimSpace(planID)
	if planID == "" || at.IsZero() {
		return ErrDailyJobPlanInvalid
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var plan DailyJobPlan
		if err := tx.First(&plan, "plan_id = ?", planID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrDailyJobPlanNotFound
			}
			return err
		}
		if plan.Status == status && plan.EndReason == reason {
			return nil
		}
		if plan.Status != DailyJobPlanDraft && plan.Status != DailyJobPlanActive {
			return ErrDailyJobPlanStateConflict
		}
		return tx.Model(&DailyJobPlan{}).
			Where("plan_id = ? AND status IN ?", planID,
				[]string{DailyJobPlanDraft, DailyJobPlanActive}).
			Updates(map[string]any{
				"status":     status,
				"end_reason": reason,
				"ended_at":   at,
				"updated_at": at,
			}).Error
	})
}

// —— 产品 UI 投影(留痕条款:每个职位跑没跑、发了几个、为何跳过必须可见) ——

type AppDailyPlanEntryView struct {
	Seq           int    `json:"seq"`
	JobName       string `json:"jobName"`
	Quota         int    `json:"quota"`
	Status        string `json:"status"`
	SkipReason    string `json:"skipReason,omitempty"`
	SelectedCount int    `json:"selectedCount"`
	SentCount     int    `json:"sentCount"`
	SuspectCount  int    `json:"suspectCount"`
}

type AppDailyPlanView struct {
	Available  bool                    `json:"available"`
	LocalDate  string                  `json:"localDate,omitempty"`
	Status     string                  `json:"status,omitempty"`
	EndReason  string                  `json:"endReason,omitempty"`
	TotalQuota int                     `json:"totalQuota,omitempty"`
	JobCount   int                     `json:"jobCount,omitempty"`
	Entries    []AppDailyPlanEntryView `json:"entries,omitempty"`
}

// AppDailyPlan 投影最近一份当日职位计划(含已终局的,便于事后回看当天名单)。
// 逐条目的发送计数按批次锚回查;锚缺失或统计报错只降级为零值,不阻断投影。
func (s *Store) AppDailyPlan() (*AppDailyPlanView, error) {
	var plan DailyJobPlan
	err := s.db.Order("created_at DESC").First(&plan).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &AppDailyPlanView{}, nil
	}
	if err != nil {
		return nil, err
	}
	entries, err := s.dailyJobPlanEntries(plan.PlanID)
	if err != nil {
		return nil, err
	}
	view := &AppDailyPlanView{
		Available: true, LocalDate: plan.LocalDate, Status: plan.Status,
		EndReason: plan.EndReason, TotalQuota: plan.TotalQuota, JobCount: plan.JobCount,
		Entries: make([]AppDailyPlanEntryView, 0, len(entries)),
	}
	for index := range entries {
		entry := entries[index]
		item := AppDailyPlanEntryView{
			Seq: entry.Seq, JobName: entry.JobName, Quota: entry.Quota,
			Status: entry.Status, SkipReason: entry.SkipReason,
		}
		if entry.BatchID != "" {
			if selection, selErr := s.SourcingBatchSelectionByBatchID(entry.BatchID); selErr == nil && selection != nil {
				item.SelectedCount = selection.SelectedCount
			}
			if sent, suspect, countErr := s.dailyPlanEntrySendCounts(entry.BatchID); countErr == nil {
				item.SentCount = sent
				item.SuspectCount = suspect
			}
		}
		view.Entries = append(view.Entries, item)
	}
	return view, nil
}

// dailyPlanEntrySendCounts 是计划面板的轻量发送计数:招呼 invocation 与其
// effect intent 的状态聚合,语义对齐 SourcingBatchGreetingSendProgress 的
// sent(ok/resolvedOk)与 suspect 两桶,但不加载整批成员材料(那条重路径含
// 全量 ResumeJSON 反序列化,UI 每 15 秒轮询扛不起)。
func (s *Store) dailyPlanEntrySendCounts(batchID string) (int, int, error) {
	type statusCount struct {
		Status string
		N      int
	}
	var rows []statusCount
	if err := s.db.Raw(`
		SELECT ei.status AS status, COUNT(*) AS n
		FROM sourcing_greeting_invocations gi
		JOIN effect_intents ei ON ei.intent_id = gi.effect_intent_id
		WHERE gi.batch_id = ? AND gi.effect_intent_id IS NOT NULL
		GROUP BY ei.status`, batchID).Scan(&rows).Error; err != nil {
		return 0, 0, err
	}
	sent, suspect := 0, 0
	for _, row := range rows {
		switch EffectIntentStatus(row.Status) {
		case EffectIntentOk, EffectIntentResolvedOk:
			sent += row.N
		case EffectIntentSuspect:
			suspect += row.N
		}
	}
	return sent, suspect, nil
}
