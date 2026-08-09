package store

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"recruithelper/contract/gen/go/protocol"

	"gorm.io/gorm"
)

var (
	ErrAppProjectionInvalid  = errors.New("产品投影查询参数无效")
	ErrAppCandidateNotFound  = errors.New("候选人档案不存在")
	ErrAppProjectionConflict = errors.New("产品投影业务事实冲突")
)

type AppCandidateView string

const (
	AppCandidateViewCommunicating AppCandidateView = "communicating"
	// AppCandidateViewInterviewed 是已约面且面试时间界未过;
	// AppCandidateViewInterviewElapsed 是已约面且时间界已过。两者同源于
	// main_status = interviewed,只按时间分流,不对应任何新业务状态。
	AppCandidateViewInterviewed      AppCandidateView = "interviewed"
	AppCandidateViewInterviewElapsed AppCandidateView = "interviewElapsed"
	AppCandidateViewWechat           AppCandidateView = "wechat"
)

type AppMetric struct {
	Value             *int64 `json:"value"`
	Exact             bool   `json:"exact"`
	UnavailableReason string `json:"unavailableReason,omitempty"`
}

type AppJobProjection struct {
	Available    bool       `json:"available"`
	BackendJobID string     `json:"backendJobId,omitempty"`
	Name         string     `json:"name,omitempty"`
	Environment  string     `json:"environment,omitempty"`
	SyncStatus   string     `json:"syncStatus"`
	LastSyncedAt *time.Time `json:"lastSyncedAt,omitempty"`
}

type AppFunnelProjection struct {
	Available bool   `json:"available"`
	BatchID   string `json:"batchId,omitempty"`
	Stage     string `json:"stage,omitempty"`
	// TargetCount 是本轮的采集额度,分轮时会从 150 逐轮抬到 CaptureLimit。
	// CaptureLimit 为 0 表示本批不分轮。SelectionTarget 是真正要凑够的选中
	// 人数,来自首轮筛选;还没筛过时为 0——那时目标是多少尚未算出。
	TargetCount     int   `json:"targetCount"`
	CaptureLimit    int   `json:"captureLimit"`
	SelectionTarget int   `json:"selectionTarget"`
	CapturedCount   int64 `json:"capturedCount"`
	ScoredCount     int64 `json:"scoredCount"`
	SelectedCount   int64 `json:"selectedCount"`
	GreetingReady   int64 `json:"greetingReady"`
	PendingConfirm  int64 `json:"pendingConfirm"`
	SentCount       int64 `json:"sentCount"`
	// 生成失败与发送失败是两个阶段的两件事,必须分开报。合成一个字段时
	// 前端在"生成招呼语"和"发送招呼"两格都读它,1 次生成失败 + 2 次发送
	// 失败会在两格各显示"失败 3",看起来像 6 处失败。
	GenerationFailedCount int64      `json:"generationFailedCount"`
	SendFailedCount       int64      `json:"sendFailedCount"`
	SuspectCount          int64      `json:"suspectCount"`
	LastFailureReason     string     `json:"lastFailureReason,omitempty"`
	StartedAt             *time.Time `json:"startedAt,omitempty"`
	FinishedAt            *time.Time `json:"finishedAt,omitempty"`
}

type AppOverviewStatistics struct {
	TodayRated        AppMetric `json:"todayRated"`
	TodayConfirmation AppMetric `json:"todayConfirmation"`
	TodayGreeted      AppMetric `json:"todayGreeted"`
	TodayInvited      AppMetric `json:"todayInvited"`
	// TodayWechat 是今天新换到微信的人数。权威时点是 ContactAsset 的收编时刻,
	// 与 contact_asset_store 里"换微信成功的权威时点即 ContactAsset 创建"同源;
	// 重复收编走 existing 分支不重建行,同一人不会跨天重复计入。
	//
	// 切当天用 observed_at_ms 而不是 created_at:两列写的是同一次收编,但
	// created_at 在现网库里是 UTC 落盘(greeted_at 等列却是本地偏移落盘),拿
	// 本地 time.Time 去比一列 UTC 字符串,本地 00:00-08:00 收编的人会被算到
	// 前一天。毫秒整数比较没有格式与时区的歧义,也与产品端"换微信时间"列
	// 用的 WechatObservedAtMs 同源。
	TodayWechat AppMetric `json:"todayWechat"`

	TotalGreeted     AppMetric `json:"totalGreeted"`
	TotalInterviewed AppMetric `json:"totalInterviewed"`
	TotalWechat      AppMetric `json:"totalWechat"`

	TodayNewReplies      AppMetric `json:"todayNewReplies"`
	TodayNewAppointments AppMetric `json:"todayNewAppointments"`
	// TodayElapsedInterviews 是今天已经过去的面试场次(按人计)。它只说明约定
	// 时间已过,不代表面试确实进行过——系统没有面试完成写入口,原先这里直接
	// 报不可用,产品端那一行于是永远是"—",用户分不清"今天没有"和"读不出来"。
	TodayElapsedInterviews AppMetric `json:"todayElapsedInterviews"`
}

type AppInterviewSummary struct {
	ProfileID   string `json:"profileId"`
	DisplayName string `json:"displayName"`
	JobName     string `json:"jobName,omitempty"`
	StartsAtMs  int64  `json:"startsAtMs"`
	EndsAtMs    *int64 `json:"endsAtMs,omitempty"`
	Method      string `json:"method,omitempty"`
	State       string `json:"state,omitempty"`
}

type AppOverviewRequest struct {
	Now            time.Time
	CurrentBatchID string
	Platform       string
	AccountRef     string
}

type AppOverviewProjection struct {
	Job             AppJobProjection      `json:"job"`
	Funnel          AppFunnelProjection   `json:"funnel"`
	Statistics      AppOverviewStatistics `json:"statistics"`
	TodayInterviews []AppInterviewSummary `json:"todayInterviews"`
	BusinessSince   *time.Time            `json:"businessSince,omitempty"`
	RefreshedAt     time.Time             `json:"refreshedAt"`
}

type AppConfirmationCandidate struct {
	ProfileID    string `json:"profileId"`
	DisplayName  string `json:"displayName"`
	JobName      string `json:"jobName,omitempty"`
	Score        *int   `json:"score"`
	GreetingText string `json:"greetingText,omitempty"`
	Status       string `json:"status"`
	Selectable   bool   `json:"selectable"`
	Failure      string `json:"failure,omitempty"`
}

type AppConfirmationProjection struct {
	Available         bool                       `json:"available"`
	Ready             bool                       `json:"ready"`
	Reason            string                     `json:"reason,omitempty"`
	BatchID           string                     `json:"batchId,omitempty"`
	JobName           string                     `json:"jobName,omitempty"`
	CreatedAt         *time.Time                 `json:"createdAt,omitempty"`
	ScoredCount       int64                      `json:"scoredCount"`
	SelectedCount     int64                      `json:"selectedCount"`
	GeneratedCount    int64                      `json:"generatedCount"`
	GenerationFailed  int64                      `json:"generationFailed"`
	GenerationPending int64                      `json:"generationPending"`
	SelectableCount   int64                      `json:"selectableCount"`
	Candidates        []AppConfirmationCandidate `json:"candidates"`
}

type AppCandidateListQuery struct {
	Platform   string
	AccountRef string
	View       AppCandidateView
	Search     string
	Limit      int
	Offset     int
	// Now 是已约面/已面试分流的时间基准,由调用方(产品 API)给出脑侧本机
	// 时间;零值回落 time.Now()。前端不按渲染进程时钟自行判断。
	Now time.Time
}

type AppCandidateDetailQuery struct {
	Platform   string
	AccountRef string
	ProfileID  string
}

type AppCandidateListItem struct {
	ProfileID   string `json:"profileId"`
	DisplayName string `json:"displayName"`
	JobName     string `json:"jobName,omitempty"`
	Status      string `json:"status"`
	EndReason   string `json:"endReason,omitempty"`
	// GreetingRejectReason 是平台拒绝招呼时的原话,让客户端不再只看到笼统的
	// 「招呼失败」(2026-08-07 甲方裁决)。平台文案,不是候选人明文。
	GreetingRejectReason string  `json:"greetingRejectReason,omitempty"`
	LastMessagePreview   string  `json:"lastMessagePreview,omitempty"`
	LastActivityAtMs     *int64  `json:"lastActivityAtMs,omitempty"`
	UnreadCount          int     `json:"unreadCount"`
	ManualRequired       bool    `json:"manualRequired"`
	ManualReason         string  `json:"manualReason,omitempty"`
	Wechat               *string `json:"wechat,omitempty"`
	// WechatObservedAtMs 是该微信资产的收编观测时刻。资产行一直有它(上面那个
	// 子查询就是按它排序取最新号的),此前没有投影出去,产品端只能常年显示
	// "时间未知"。
	WechatObservedAtMs  *int64  `json:"wechatObservedAtMs,omitempty"`
	InterviewStartsAtMs *int64  `json:"interviewStartsAtMs,omitempty"`
	InterviewEndsAtMs   *int64  `json:"interviewEndsAtMs,omitempty"`
	InterviewMethod     *string `json:"interviewMethod,omitempty"`
	InterviewCardState  string  `json:"interviewCardState,omitempty"`
	// AppointedAtMs 是候选人接受面试邀约的时刻(档案的 interviewed_at),与
	// InterviewStartsAtMs(约定的面试何时开始)是两回事。首页「已约面」数的是
	// 当天有几个人在这个时刻上答应,已约面页数的是当前还没到面试时间的存量,
	// 两个数字对不上时用户只能靠猜——把它投影出去,页面上就能直接看出谁是
	// 今天答应的。
	AppointedAtMs *int64 `json:"appointedAtMs,omitempty"`
}

type AppCandidateListProjection struct {
	View   AppCandidateView       `json:"view"`
	Total  int64                  `json:"total"`
	Items  []AppCandidateListItem `json:"items"`
	Limit  int                    `json:"limit"`
	Offset int                    `json:"offset"`
}

type AppResumeField struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type AppResumeSummary struct {
	Available       bool             `json:"available"`
	Basic           []AppResumeField `json:"basic"`
	Expectations    []AppResumeField `json:"expectations"`
	SelfEvaluation  string           `json:"selfEvaluation,omitempty"`
	Education       string           `json:"education,omitempty"`
	WorkExperiences string           `json:"workExperiences,omitempty"`
	Truncated       bool             `json:"truncated"`
}

type AppMessageSummary struct {
	Seq                 int64   `json:"seq"`
	Direction           string  `json:"direction"`
	Kind                string  `json:"kind"`
	Text                *string `json:"text,omitempty"`
	CardType            string  `json:"cardType,omitempty"`
	CardState           string  `json:"cardState,omitempty"`
	TsApproxMs          *int64  `json:"tsApproxMs,omitempty"`
	InterviewStartsAtMs *int64  `json:"interviewStartsAtMs,omitempty"`
	InterviewEndsAtMs   *int64  `json:"interviewEndsAtMs,omitempty"`
	InterviewMethod     *string `json:"interviewMethod,omitempty"`
}

type AppAIJudgementSummary struct {
	Available    bool       `json:"available"`
	Status       string     `json:"status,omitempty"`
	IntentLabel  string     `json:"intentLabel,omitempty"`
	IntentSource string     `json:"intentSource,omitempty"`
	Failure      string     `json:"failure,omitempty"`
	ClassifiedAt *time.Time `json:"classifiedAt,omitempty"`
}

type AppActionSummary struct {
	Kind      string     `json:"kind"`
	Status    string     `json:"status"`
	Failure   string     `json:"failure,omitempty"`
	CreatedAt *time.Time `json:"createdAt,omitempty"`
}

type AppCandidateDetailProjection struct {
	Candidate AppCandidateListItem  `json:"candidate"`
	Resume    AppResumeSummary      `json:"resume"`
	Messages  []AppMessageSummary   `json:"messages"`
	LatestAI  AppAIJudgementSummary `json:"latestAi"`
	Actions   []AppActionSummary    `json:"actions"`
}

func exactMetric(value int64) AppMetric {
	return AppMetric{Value: &value, Exact: true}
}

func unavailableMetric(reason string) AppMetric {
	return AppMetric{Exact: false, UnavailableReason: reason}
}

// AppCurrentJob 是无账号维度的当前职位投影。全新安装在绑定任何平台账号之前,
// 首页也必须能看见"职位已同步"——职位显示是开始按钮的前置,而点开始才是建立
// 账号的动作(账号跟随登录,2026-07-30 裁决);overview 曾对零账号整体短路,
// 职位被硬编码成 missing,同步成功也不可见,构成装机死锁(2026-08-01 真机复现)。
// 查询与 AppOverview 的零批次分支同源:职位头本就不带账号维度。
func (s *Store) AppCurrentJob() (AppJobProjection, error) {
	var out AppJobProjection
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var txErr error
		out, txErr = appCurrentJobTx(tx, "", "", "")
		return txErr
	})
	if err != nil {
		return AppJobProjection{}, err
	}
	return out, nil
}

func (s *Store) AppOverview(req AppOverviewRequest) (*AppOverviewProjection, error) {
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	req.CurrentBatchID = strings.TrimSpace(req.CurrentBatchID)
	req.Platform = strings.TrimSpace(req.Platform)
	req.AccountRef = strings.TrimSpace(req.AccountRef)
	if len(req.CurrentBatchID) > 128 ||
		!validAppAccountScope(req.Platform, req.AccountRef) {
		return nil, ErrAppProjectionInvalid
	}
	start, end := localBusinessDay(req.Now)
	out := AppOverviewProjection{RefreshedAt: req.Now}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		out.Job, err = appCurrentJobTx(
			tx,
			req.CurrentBatchID,
			req.Platform,
			req.AccountRef,
		)
		if err != nil {
			return err
		}
		out.Funnel, err = appFunnelTx(
			tx,
			req.CurrentBatchID,
			req.Platform,
			req.AccountRef,
		)
		if err != nil {
			return err
		}
		out.Statistics, err = appOverviewStatisticsTx(
			tx,
			req.Now,
			start,
			end,
			req.Platform,
			req.AccountRef,
		)
		if err != nil {
			return err
		}
		out.TodayInterviews, err = appTodayInterviewsTx(
			tx,
			start,
			end,
			req.Platform,
			req.AccountRef,
		)
		if err != nil {
			return err
		}
		var firstProfile CandidateProfile
		err = tx.Where("platform = ? AND account_ref = ?", req.Platform, req.AccountRef).
			Order("created_at ASC, profile_id ASC").First(&firstProfile).Error
		switch {
		case err == nil:
			since := firstProfile.CreatedAt
			out.BusinessSince = &since
		case errors.Is(err, gorm.ErrRecordNotFound):
		default:
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out.TodayInterviews == nil {
		out.TodayInterviews = []AppInterviewSummary{}
	}
	return &out, nil
}

func localBusinessDay(now time.Time) (time.Time, time.Time) {
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return start, start.AddDate(0, 0, 1)
}

func appCurrentJobTx(
	tx *gorm.DB,
	batchID, platform, accountRef string,
) (AppJobProjection, error) {
	type row struct {
		DisplayName  string
		Environment  string
		LastSyncedAt time.Time
		SourceJobRef string
	}
	if batchID != "" {
		var batch SourcingBatch
		if err := tx.First(&batch, "batch_id = ?", batchID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return AppJobProjection{SyncStatus: "missing"}, nil
			}
			return AppJobProjection{}, err
		}
		if batch.Platform != platform || batch.AccountRef != accountRef ||
			batch.BackendJobID == nil || strings.TrimSpace(*batch.BackendJobID) == "" {
			return AppJobProjection{}, ErrAppProjectionConflict
		}
		backendJobID := strings.TrimSpace(*batch.BackendJobID)
		var frozen JobAIContextRevision
		if err := tx.First(
			&frozen,
			"revision_hash = ?",
			batch.ContextRevisionHash,
		).Error; err != nil {
			return AppJobProjection{}, ErrAppProjectionConflict
		}
		if frozen.SourceKind != legacyJobConfigSourceKind ||
			frozen.SourceJobRef != backendJobID {
			return AppJobProjection{}, ErrAppProjectionConflict
		}
		var currentRows []row
		err := tx.Table("job_ai_context_heads AS head").
			Select(
				"revision.display_name, revision.environment, "+
					"head.last_synced_at, head.source_job_ref",
			).
			Joins(
				"JOIN job_ai_context_revisions AS revision "+
					"ON revision.revision_hash = head.revision_hash",
			).
			Where(
				"head.source_kind = ? AND head.source_job_ref = ?",
				legacyJobConfigSourceKind,
				backendJobID,
			).
			Limit(1).
			Scan(&currentRows).Error
		switch {
		case err != nil:
			return AppJobProjection{}, err
		case len(currentRows) == 1:
			current := currentRows[0]
			if current.SourceJobRef != backendJobID {
				return AppJobProjection{}, ErrAppProjectionConflict
			}
			return AppJobProjection{
				Available: true, BackendJobID: backendJobID,
				Name: current.DisplayName, Environment: current.Environment,
				SyncStatus: "synced", LastSyncedAt: &current.LastSyncedAt,
			}, nil
		case len(currentRows) == 0:
			return AppJobProjection{
				Available: true, BackendJobID: backendJobID,
				Name: frozen.DisplayName, Environment: frozen.Environment,
				SyncStatus: "stale",
			}, nil
		default:
			return AppJobProjection{}, ErrAppProjectionConflict
		}
	}
	var rows []row
	if err := tx.Table("job_ai_context_heads AS head").
		Select("revision.display_name, revision.environment, head.last_synced_at, head.source_job_ref").
		Joins("JOIN job_ai_context_revisions AS revision ON revision.revision_hash = head.revision_hash").
		Where(
			"head.source_kind = ? AND head.activation_current = ?",
			legacyJobConfigSourceKind,
			true,
		).
		Order("head.last_synced_at DESC, head.source_job_ref ASC").
		Limit(2).Scan(&rows).Error; err != nil {
		return AppJobProjection{}, err
	}
	if len(rows) == 0 {
		return AppJobProjection{SyncStatus: "missing"}, nil
	}
	if len(rows) > 1 {
		return AppJobProjection{SyncStatus: "ambiguous"}, nil
	}
	return AppJobProjection{
		Available: true, BackendJobID: rows[0].SourceJobRef,
		Name: rows[0].DisplayName, Environment: rows[0].Environment,
		SyncStatus: "synced", LastSyncedAt: &rows[0].LastSyncedAt,
	}, nil
}

func appFunnelTx(
	tx *gorm.DB,
	batchID, platform, accountRef string,
) (AppFunnelProjection, error) {
	if batchID == "" {
		return AppFunnelProjection{}, nil
	}
	var batch SourcingBatch
	if err := tx.First(&batch, "batch_id = ?", batchID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return AppFunnelProjection{}, nil
		}
		return AppFunnelProjection{}, err
	}
	if batch.Platform != platform || batch.AccountRef != accountRef {
		return AppFunnelProjection{}, ErrAppProjectionConflict
	}
	out := AppFunnelProjection{
		Available: true, BatchID: batch.BatchID, TargetCount: batch.TargetCount,
		CaptureLimit:      batch.CaptureLimit,
		LastFailureReason: batch.Reason, StartedAt: &batch.StartedAt, FinishedAt: batch.EndedAt,
	}
	var selectionSummary SourcingBatchSelection
	switch err := tx.First(&selectionSummary, "batch_id = ?", batch.BatchID).Error; {
	case err == nil:
		out.SelectionTarget = selectionSummary.TargetCount
	case errors.Is(err, gorm.ErrRecordNotFound):
	default:
		return AppFunnelProjection{}, err
	}
	if err := tx.Model(&SourcingCandidateRun{}).
		Where(
			"batch_id = ? AND platform = ? AND account_ref = ?",
			batch.BatchID,
			platform,
			accountRef,
		).
		Count(&out.CapturedCount).Error; err != nil {
		return AppFunnelProjection{}, err
	}
	if err := tx.Table("sourcing_score_invocations AS score").
		Joins("JOIN sourcing_candidate_runs AS run ON run.run_id = score.run_id").
		Where(
			"run.batch_id = ? AND run.platform = ? AND run.account_ref = ? "+
				"AND score.finished_at IS NOT NULL",
			batch.BatchID,
			platform,
			accountRef,
		).
		Count(&out.ScoredCount).Error; err != nil {
		return AppFunnelProjection{}, err
	}
	if err := tx.Table("sourcing_selection_decisions AS decision").
		Joins("JOIN sourcing_candidate_runs AS run ON run.run_id = decision.run_id").
		Where(
			"run.batch_id = ? AND run.platform = ? AND run.account_ref = ? "+
				"AND decision.outcome = ?",
			batch.BatchID,
			platform,
			accountRef,
			SourcingSelectionSelected,
		).
		Count(&out.SelectedCount).Error; err != nil {
		return AppFunnelProjection{}, err
	}
	if err := tx.Table("sourcing_greeting_invocations AS greeting").
		Joins("JOIN sourcing_candidate_runs AS run ON run.run_id = greeting.run_id").
		Where(
			"greeting.batch_id = ? AND run.platform = ? AND run.account_ref = ? "+
				"AND greeting.status = ? AND greeting.finished_at IS NOT NULL",
			batch.BatchID,
			platform,
			accountRef,
			AIInvocationOK,
		).
		Count(&out.GreetingReady).Error; err != nil {
		return AppFunnelProjection{}, err
	}
	var generationFailed int64
	if err := tx.Table("sourcing_greeting_invocations AS greeting").
		Joins("JOIN sourcing_candidate_runs AS run ON run.run_id = greeting.run_id").
		Where(
			"greeting.batch_id = ? AND run.platform = ? AND run.account_ref = ? "+
				"AND greeting.status <> ? AND greeting.finished_at IS NOT NULL",
			batch.BatchID,
			platform,
			accountRef,
			AIInvocationOK,
		).
		Count(&generationFailed).Error; err != nil {
		return AppFunnelProjection{}, err
	}
	out.GenerationFailedCount = generationFailed
	var intents []EffectIntent
	if err := tx.Table("effect_intents AS effect").
		Joins("JOIN sourcing_greeting_invocations AS greeting ON greeting.effect_intent_id = effect.intent_id").
		Joins("JOIN sourcing_candidate_runs AS run ON run.run_id = greeting.run_id").
		Where(
			"greeting.batch_id = ? AND run.platform = ? AND run.account_ref = ?",
			batch.BatchID,
			platform,
			accountRef,
		).Find(&intents).Error; err != nil {
		return AppFunnelProjection{}, err
	}
	for i := range intents {
		switch intents[i].Status {
		case EffectIntentOk, EffectIntentResolvedOk:
			out.SentCount++
		case EffectIntentFailed, EffectIntentResolvedFailed:
			out.SendFailedCount++
		case EffectIntentSuspect:
			out.SuspectCount++
		}
	}
	confirmationCandidates, err := appConfirmationCandidatesTx(tx, batch)
	if err != nil {
		return AppFunnelProjection{}, err
	}
	var settledWithoutSend int64
	for i := range confirmationCandidates {
		switch confirmationCandidates[i].Status {
		case "ready":
			out.PendingConfirm++
		case "abandoned", "unavailable":
			settledWithoutSend++
		}
	}
	switch {
	case batch.Status == SourcingBatchBlocked:
		out.Stage = "failed"
	case batch.Status == SourcingBatchPreparing || batch.Status == SourcingBatchCollecting:
		out.Stage = "collecting"
	case out.ScoredCount < int64(batch.TargetCount):
		out.Stage = "scoring"
	case out.SelectedCount == 0:
		var selectionCount int64
		if err := tx.Model(&SourcingBatchSelection{}).Where("batch_id = ?", batch.BatchID).
			Count(&selectionCount).Error; err != nil {
			return AppFunnelProjection{}, err
		}
		if selectionCount == 0 {
			out.Stage = "selecting"
		} else {
			out.Stage = "completed"
		}
	case out.GreetingReady+generationFailed < out.SelectedCount:
		out.Stage = "generatingGreetings"
	case out.PendingConfirm > 0:
		out.Stage = "awaitingConfirmation"
	case out.SentCount+out.GenerationFailedCount+out.SendFailedCount+
		out.SuspectCount+settledWithoutSend >= out.SelectedCount:
		out.Stage = "completed"
	default:
		out.Stage = "sendingGreetings"
	}
	return out, nil
}

func appOverviewStatisticsTx(
	tx *gorm.DB,
	now, start, end time.Time,
	platform, accountRef string,
) (AppOverviewStatistics, error) {
	var out AppOverviewStatistics
	count := func(query *gorm.DB, target *int64) error { return query.Count(target).Error }
	var value int64
	if err := tx.Table("(?) AS rated", tx.Table("sourcing_score_invocations AS score").
		Select("run.platform, run.platform_user_ref").
		Joins("JOIN sourcing_candidate_runs AS run ON run.run_id = score.run_id").
		Where("score.status = ? AND score.finished_at >= ? AND score.finished_at < ?",
			AIInvocationOK, start, end).
		Where("run.platform = ? AND run.account_ref = ?", platform, accountRef).
		Group("run.platform, run.platform_user_ref")).
		Count(&value).Error; err != nil {
		return out, err
	}
	out.TodayRated = exactMetric(value)

	value = 0
	if err := count(tx.Table("sourcing_selection_decisions AS decision").
		Joins("JOIN sourcing_candidate_runs AS run ON run.run_id = decision.run_id").
		Where("decision.outcome = ? AND decision.decided_at >= ? AND decision.decided_at < ?",
			SourcingSelectionSelected, start, end).
		Where("run.platform = ? AND run.account_ref = ?", platform, accountRef).
		Distinct("decision.profile_id"), &value); err != nil {
		return out, err
	}
	out.TodayConfirmation = exactMetric(value)

	value = 0
	if err := count(tx.Model(&CandidateProfile{}).
		Where("platform = ? AND account_ref = ?", platform, accountRef).
		Where("greeted_at >= ? AND greeted_at < ?", start, end).
		Distinct("profile_id"), &value); err != nil {
		return out, err
	}
	out.TodayGreeted = exactMetric(value)

	todayInvite, err := appTimedMessageProfileCountTx(
		tx, platform, accountRef, "out", "card", "interviewInvite", start, end,
	)
	if err != nil {
		return out, err
	}
	out.TodayInvited = exactMetric(todayInvite)

	value = 0
	if err := count(tx.Model(&ContactAsset{}).
		Where("platform = ? AND account_ref = ?", platform, accountRef).
		Where("kind = ?", contactAssetKindWechat).
		Where("observed_at_ms >= ? AND observed_at_ms < ?", start.UnixMilli(), end.UnixMilli()).
		Distinct("profile_id"), &value); err != nil {
		return out, err
	}
	out.TodayWechat = exactMetric(value)

	// 今日新约面数的是"今天有几个人接受了面试邀约",取 interviewed_at。它此前
	// 直接复制 todayInvited(今天发出邀面卡的人数),两个不同标签共用一个数字。
	// 发卡与被接受是两件事,消息时间戳只能算出前者。
	value = 0
	if err := count(tx.Model(&CandidateProfile{}).
		Where("platform = ? AND account_ref = ?", platform, accountRef).
		Where("interviewed_at >= ? AND interviewed_at < ?", start, end).
		Distinct("profile_id"), &value); err != nil {
		return out, err
	}
	out.TodayNewAppointments = exactMetric(value)

	value = 0
	if err := count(tx.Model(&CandidateProfile{}).
		Where("platform = ? AND account_ref = ?", platform, accountRef).
		Where("greeted_at IS NOT NULL").Distinct("profile_id"), &value); err != nil {
		return out, err
	}
	out.TotalGreeted = exactMetric(value)
	value = 0
	if err := count(tx.Model(&CandidateProfile{}).
		Where("platform = ? AND account_ref = ?", platform, accountRef).
		Where("main_status = ?", CandidateProfileInterviewed).
		Distinct("profile_id"), &value); err != nil {
		return out, err
	}
	out.TotalInterviewed = exactMetric(value)

	value = 0
	if err := count(tx.Model(&ContactAsset{}).
		Where("platform = ? AND account_ref = ?", platform, accountRef).
		Where("kind = ?", contactAssetKindWechat).Distinct("profile_id"), &value); err != nil {
		return out, err
	}
	out.TotalWechat = exactMetric(value)

	todayReply, err := appTimedMessageProfileCountTx(
		tx,
		platform,
		accountRef,
		"in",
		"",
		"",
		start,
		end,
	)
	if err != nil {
		return out, err
	}
	out.TodayNewReplies = exactMetric(todayReply)

	// 今天已过去的面试:最新一张邀面卡的时间界落在今天且已经过去。与
	// AppCandidateViewInterviewElapsed 同判据(结束时间优先、缺则开始时间),
	// 因此两处口径天然一致。
	value = 0
	if err := tx.Table("candidate_profiles AS profile").
		Where("profile.platform = ? AND profile.account_ref = ?", platform, accountRef).
		Where("profile.main_status = ?", CandidateProfileInterviewed).
		Where(appLatestInterviewDeadlineMs+" >= ?", start.UnixMilli()).
		Where(appLatestInterviewDeadlineMs+" < ?", now.UnixMilli()).
		Distinct("profile.profile_id").Count(&value).Error; err != nil {
		return out, err
	}
	out.TodayElapsedInterviews = exactMetric(value)
	return out, nil
}

// appTimedMessageProfileCountTx 数当天窗口内有过该类消息的候选人数。
//
// 读不出平台时间的消息直接不计入(2026-07-30 甲方裁决)。此前的做法是:只要
// 该账号历史上存在任何一条无平台时间的同类消息,整个指标就标为不可用。判定
// 范围是全库而不是当天,于是三个月前的一条无时间戳老消息——跟"今天有几个人
// 回复"毫无关系——会把今天的数字打成"—";业务事实又禁止物理删除,那条消息
// 永远在库里,指标从此永久哑掉,再也不会出现数字。平台不给消息时间是常态,
// 这条路径很容易被走到。
//
// 代价是少报且不作声:今天真有三人回复、其中一条没有平台时间,就只报两人。
// 方向是宁可少报,与仓库既有保守取向一致。
func appTimedMessageProfileCountTx(
	tx *gorm.DB,
	platform, accountRef string,
	direction, kind, cardType string,
	start, end time.Time,
) (int64, error) {
	base := tx.Table("messages AS message").
		Joins("JOIN candidate_profiles AS profile ON profile.platform = message.platform "+
			"AND profile.account_ref = message.account_ref AND profile.conversation_ref = message.conversation_ref").
		Where("profile.platform = ? AND profile.account_ref = ?", platform, accountRef).
		Where("message.direction = ? AND message.retracted_at IS NULL", direction)
	if kind != "" {
		base = base.Where("message.kind = ?", kind)
	}
	if cardType != "" {
		base = base.Where("message.card_type = ?", cardType)
	}
	var value int64
	if err := base.
		Where("message.ts_approx_ms >= ? AND message.ts_approx_ms < ?",
			start.UnixMilli(), end.UnixMilli()).
		Distinct("profile.profile_id").Count(&value).Error; err != nil {
		return 0, err
	}
	return value, nil
}

func appTodayInterviewsTx(
	tx *gorm.DB,
	start, end time.Time,
	platform, accountRef string,
) ([]AppInterviewSummary, error) {
	type row struct {
		ProfileID     string
		DisplayName   *string
		PositionTitle *string
		StartsAtMs    *int64
		EndsAtMs      *int64
		Method        *string
		CardState     string
	}
	// 每人只看最新一张邀面卡，判定"今天有没有面试"必须先取最新卡、再看它
	// 落不落在今天，不能在今天的卡里挑最新：候选人要求改期后会重发一张新
	// 卡，先按日期筛就会把已经作废的旧时段留在今日日程里，改期到明天时更
	// 是明明今天没有面试却仍在列。候选人列表(appCandidateSelect)一直按
	// seq DESC 取最新卡，这里同口径，两处不再各说各话。
	var rows []row
	if err := tx.Table("messages AS message").
		Select("profile.profile_id, candidate.display_name, profile.position_title, "+
			"message.interview_starts_at_ms AS starts_at_ms, message.interview_ends_at_ms AS ends_at_ms, "+
			"message.interview_method AS method, message.card_state").
		Joins("JOIN candidate_profiles AS profile ON profile.platform = message.platform "+
			"AND profile.account_ref = message.account_ref AND profile.conversation_ref = message.conversation_ref").
		Joins("JOIN candidates AS candidate ON candidate.platform = profile.platform "+
			"AND candidate.platform_user_ref = profile.platform_user_ref").
		Where("profile.platform = ? AND profile.account_ref = ?", platform, accountRef).
		Where("message.direction = ? AND message.kind = ? AND message.card_type = ? "+
			"AND message.retracted_at IS NULL", "out", "card", "interviewInvite").
		Where("message.seq = (SELECT MAX(latest.seq) FROM messages AS latest "+
			"WHERE latest.platform = message.platform AND latest.account_ref = message.account_ref "+
			"AND latest.conversation_ref = message.conversation_ref AND latest.direction = 'out' "+
			"AND latest.kind = 'card' AND latest.card_type = 'interviewInvite' "+
			"AND latest.retracted_at IS NULL)").
		Where("message.interview_starts_at_ms >= ? AND message.interview_starts_at_ms < ?",
			start.UnixMilli(), end.UnixMilli()).
		Order("message.interview_starts_at_ms ASC, profile.profile_id ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]AppInterviewSummary, 0, len(rows))
	for _, row := range rows {
		if row.StartsAtMs == nil {
			continue
		}
		out = append(out, AppInterviewSummary{
			ProfileID: row.ProfileID, DisplayName: valueOrEmpty(row.DisplayName),
			JobName: valueOrEmpty(row.PositionTitle), StartsAtMs: *row.StartsAtMs,
			EndsAtMs: row.EndsAtMs, Method: valueOrEmpty(row.Method), State: row.CardState,
		})
	}
	return out, nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func validAppCandidateView(view AppCandidateView) bool {
	switch view {
	case AppCandidateViewCommunicating, AppCandidateViewInterviewed,
		AppCandidateViewInterviewElapsed, AppCandidateViewWechat:
		return true
	default:
		return false
	}
}

func validAppAccountScope(platform, accountRef string) bool {
	return platform != "" && accountRef != "" &&
		len(platform) <= 64 && len(accountRef) <= 128
}

func (s *Store) AppConfirmation(batchID string) (*AppConfirmationProjection, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" || len(batchID) > 128 {
		return nil, ErrAppProjectionInvalid
	}
	var out AppConfirmationProjection
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var batch SourcingBatch
		if err := tx.First(&batch, "batch_id = ?", batchID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		out.Available, out.BatchID, out.CreatedAt = true, batch.BatchID, &batch.StartedAt
		if batch.PositionTitle != nil {
			out.JobName = *batch.PositionTitle
		}
		if err := tx.Table("sourcing_score_invocations AS score").
			Joins("JOIN sourcing_candidate_runs AS run ON run.run_id = score.run_id").
			Where("run.batch_id = ? AND score.finished_at IS NOT NULL", batch.BatchID).
			Count(&out.ScoredCount).Error; err != nil {
			return err
		}
		var selection SourcingBatchSelection
		if err := tx.First(&selection, "batch_id = ?", batch.BatchID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				out.Reason = "selectionPending"
				return nil
			}
			return err
		}
		out.SelectedCount = int64(selection.SelectedCount)
		candidates, err := appConfirmationCandidatesTx(tx, batch)
		if err != nil {
			return err
		}
		out.Candidates = candidates
		for i := range candidates {
			switch candidates[i].Status {
			case "ready":
				out.GeneratedCount++
				out.SelectableCount++
			case "generationFailed":
				out.GenerationFailed++
			case "generationPending", "generating":
				out.GenerationPending++
			default:
				if candidates[i].GreetingText != "" {
					out.GeneratedCount++
				}
			}
		}
		out.Ready = out.GenerationPending == 0
		if !out.Ready {
			out.Reason = "greetingGenerationPending"
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out.Candidates == nil {
		out.Candidates = []AppConfirmationCandidate{}
	}
	return &out, nil
}

func appConfirmationCandidatesTx(
	tx *gorm.DB,
	batch SourcingBatch,
) ([]AppConfirmationCandidate, error) {
	type row struct {
		ProfileID          string
		DisplayName        *string
		PositionTitle      *string
		Score              *int
		GreetingText       *string
		GreetingStatus     *AIInvocationStatus
		GreetingFinishedAt *time.Time
		GreetingFailure    *string
		EffectIntentID     *string
		EffectStatus       *EffectIntentStatus
		ProfileStatus      CandidateProfileStatus
		EndReason          *CandidateProfileEndReason
	}
	var rows []row
	if err := tx.Table("sourcing_selection_decisions AS decision").
		Select("profile.profile_id, candidate.display_name, profile.position_title, score.score, "+
			"greeting.greeting_text, greeting.status AS greeting_status, "+
			"greeting.finished_at AS greeting_finished_at, greeting.error_detail_code AS greeting_failure, "+
			"greeting.effect_intent_id, effect.status AS effect_status, "+
			"profile.main_status AS profile_status, profile.end_reason").
		Joins("JOIN sourcing_candidate_runs AS run ON run.run_id = decision.run_id").
		Joins("JOIN candidate_profiles AS profile ON profile.profile_id = decision.profile_id").
		Joins("JOIN candidates AS candidate ON candidate.platform = profile.platform "+
			"AND candidate.platform_user_ref = profile.platform_user_ref").
		Joins("LEFT JOIN sourcing_score_invocations AS score ON score.run_id = run.run_id").
		Joins("LEFT JOIN sourcing_greeting_invocations AS greeting ON greeting.run_id = run.run_id "+
			"AND greeting.profile_id = profile.profile_id").
		Joins("LEFT JOIN effect_intents AS effect ON effect.intent_id = greeting.effect_intent_id").
		Where("run.batch_id = ? AND decision.outcome = ?", batch.BatchID, SourcingSelectionSelected).
		Order("run.captured_at ASC, run.run_id ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	feedChanged := false
	var account Account
	if err := tx.First(&account, "platform = ? AND account_ref = ?",
		batch.Platform, batch.AccountRef).Error; err == nil {
		feedChanged = account.SourcingFeedInvalidatedAt != nil &&
			!account.SourcingFeedInvalidatedAt.Before(batch.StartedAt)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	out := make([]AppConfirmationCandidate, 0, len(rows))
	for _, row := range rows {
		item := AppConfirmationCandidate{
			ProfileID: row.ProfileID, DisplayName: valueOrEmpty(row.DisplayName),
			JobName: valueOrEmpty(row.PositionTitle), Score: row.Score,
			GreetingText: valueOrEmpty(row.GreetingText),
		}
		switch {
		case row.GreetingStatus == nil:
			item.Status = "generationPending"
		case row.GreetingFinishedAt == nil:
			item.Status = "generating"
		case *row.GreetingStatus != AIInvocationOK:
			item.Status = "generationFailed"
			item.Failure = valueOrEmpty(row.GreetingFailure)
		case row.EffectIntentID == nil && feedChanged:
			item.Status = "abandoned"
			item.Failure = SourcingFeedChangedReason
		case row.EffectIntentID == nil && row.ProfileStatus == CandidateProfileSelected && row.EndReason == nil:
			item.Status, item.Selectable = "ready", true
		case row.EffectIntentID == nil:
			item.Status = "unavailable"
		case row.EffectStatus == nil:
			return nil, ErrAppProjectionConflict
		default:
			switch *row.EffectStatus {
			case EffectIntentDispatching, EffectIntentReconciling, EffectIntentVerifying:
				item.Status = "sending"
			case EffectIntentOk, EffectIntentResolvedOk:
				item.Status = "sent"
			case EffectIntentFailed, EffectIntentResolvedFailed:
				item.Status = "failed"
			case EffectIntentSuspect:
				item.Status = "suspect"
			default:
				return nil, ErrAppProjectionConflict
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *Store) AppCandidates(query AppCandidateListQuery) (*AppCandidateListProjection, error) {
	query.Platform = strings.TrimSpace(query.Platform)
	query.AccountRef = strings.TrimSpace(query.AccountRef)
	query.Search = strings.TrimSpace(query.Search)
	if !validAppCandidateView(query.View) || utf8.RuneCountInString(query.Search) > 100 ||
		query.Offset < 0 || query.Limit < 0 || query.Limit > 200 ||
		!validAppAccountScope(query.Platform, query.AccountRef) {
		return nil, ErrAppProjectionInvalid
	}
	if query.Limit == 0 {
		query.Limit = 50
	}
	if query.Now.IsZero() {
		query.Now = time.Now()
	}
	var out AppCandidateListProjection
	err := s.db.Transaction(func(tx *gorm.DB) error {
		base, err := appCandidateBaseQuery(
			tx,
			query.Platform,
			query.AccountRef,
			query.View,
			query.Search,
			query.Now,
		)
		if err != nil {
			return err
		}
		if err := base.Session(&gorm.Session{}).Distinct("profile.profile_id").
			Count(&out.Total).Error; err != nil {
			return err
		}
		var rows []appCandidateRow
		if err := base.Session(&gorm.Session{}).
			Select(appCandidateSelect).
			Order("conversation.last_activity_ms IS NULL ASC, conversation.last_activity_ms DESC, profile.updated_at DESC, profile.profile_id ASC").
			Limit(query.Limit).Offset(query.Offset).Scan(&rows).Error; err != nil {
			return err
		}
		out.Items = make([]AppCandidateListItem, len(rows))
		for i := range rows {
			out.Items[i] = rows[i].projection()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out.View, out.Limit, out.Offset = query.View, query.Limit, query.Offset
	if out.Items == nil {
		out.Items = []AppCandidateListItem{}
	}
	return &out, nil
}

type appCandidateRow struct {
	ProfileID            string
	DisplayName          *string
	PositionTitle        *string
	MainStatus           CandidateProfileStatus
	EndReason            *CandidateProfileEndReason
	GreetingRejectReason *string
	LastMessagePreview   *string
	LastActivityMs       *int64
	UnreadCount          *int
	AutomationStatus     *ProfileCommunicationAutomationStatus
	ManualReason         *string
	Wechat               *string
	WechatObservedAtMs   *int64
	InterviewStartsAtMs  *int64
	InterviewEndsAtMs    *int64
	InterviewMethod      *string
	InterviewCardState   *string
	InterviewedAt        *time.Time
}

const appCandidateSelect = `
profile.profile_id,
candidate.display_name,
profile.position_title,
profile.main_status,
profile.end_reason,
profile.greeting_reject_reason,
profile.interviewed_at,
conversation.last_message_preview,
conversation.last_activity_ms,
conversation.unread_count,
aggregate.automation_status,
aggregate.manual_reason,
(SELECT asset.value FROM contact_assets AS asset
 WHERE asset.profile_id = profile.profile_id
 AND asset.platform = profile.platform AND asset.account_ref = profile.account_ref
 AND asset.kind = 'wechat'
 ORDER BY asset.observed_at_ms DESC, asset.asset_id DESC LIMIT 1) AS wechat,
(SELECT asset.observed_at_ms FROM contact_assets AS asset
 WHERE asset.profile_id = profile.profile_id
 AND asset.platform = profile.platform AND asset.account_ref = profile.account_ref
 AND asset.kind = 'wechat'
 ORDER BY asset.observed_at_ms DESC, asset.asset_id DESC LIMIT 1) AS wechat_observed_at_ms,
(SELECT message.interview_starts_at_ms FROM messages AS message
 WHERE message.platform = profile.platform AND message.account_ref = profile.account_ref
 AND message.conversation_ref = profile.conversation_ref AND message.direction = 'out'
 AND message.kind = 'card' AND message.card_type = 'interviewInvite'
 AND message.retracted_at IS NULL ORDER BY message.seq DESC LIMIT 1) AS interview_starts_at_ms,
(SELECT message.interview_ends_at_ms FROM messages AS message
 WHERE message.platform = profile.platform AND message.account_ref = profile.account_ref
 AND message.conversation_ref = profile.conversation_ref AND message.direction = 'out'
 AND message.kind = 'card' AND message.card_type = 'interviewInvite'
 AND message.retracted_at IS NULL ORDER BY message.seq DESC LIMIT 1) AS interview_ends_at_ms,
(SELECT message.interview_method FROM messages AS message
 WHERE message.platform = profile.platform AND message.account_ref = profile.account_ref
 AND message.conversation_ref = profile.conversation_ref AND message.direction = 'out'
 AND message.kind = 'card' AND message.card_type = 'interviewInvite'
 AND message.retracted_at IS NULL ORDER BY message.seq DESC LIMIT 1) AS interview_method,
(SELECT message.card_state FROM messages AS message
 WHERE message.platform = profile.platform AND message.account_ref = profile.account_ref
 AND message.conversation_ref = profile.conversation_ref AND message.direction = 'out'
 AND message.kind = 'card' AND message.card_type = 'interviewInvite'
 AND message.retracted_at IS NULL ORDER BY message.seq DESC LIMIT 1) AS interview_card_state`

func (row appCandidateRow) projection() AppCandidateListItem {
	unread := 0
	if row.UnreadCount != nil {
		unread = *row.UnreadCount
	}
	manual := row.AutomationStatus != nil &&
		*row.AutomationStatus == ProfileCommunicationAutomationManualRequired
	return AppCandidateListItem{
		ProfileID: row.ProfileID, DisplayName: valueOrEmpty(row.DisplayName),
		JobName: valueOrEmpty(row.PositionTitle), Status: string(row.MainStatus),
		EndReason:            valueOrEmptyCandidateEndReason(row.EndReason),
		GreetingRejectReason: valueOrEmpty(row.GreetingRejectReason),
		LastMessagePreview:   valueOrEmpty(row.LastMessagePreview),
		LastActivityAtMs:     row.LastActivityMs, UnreadCount: unread,
		ManualRequired: manual, ManualReason: valueOrEmpty(row.ManualReason),
		Wechat: row.Wechat, WechatObservedAtMs: row.WechatObservedAtMs,
		InterviewStartsAtMs: row.InterviewStartsAtMs,
		InterviewEndsAtMs:   row.InterviewEndsAtMs, InterviewMethod: row.InterviewMethod,
		InterviewCardState: valueOrEmpty(row.InterviewCardState),
		AppointedAtMs:      epochMillisOrNil(row.InterviewedAt),
	}
}

// epochMillisOrNil 把时间列换算成毫秒。零值当没有:这一列对没约成的档案本来
// 就是 NULL,而历史迁移过的行可能落成零值,两者在页面上是同一件事——不显示。
func epochMillisOrNil(value *time.Time) *int64 {
	if value == nil || value.IsZero() {
		return nil
	}
	millis := value.UnixMilli()
	return &millis
}

func valueOrEmptyCandidateEndReason(value *CandidateProfileEndReason) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

// appLatestInterviewDeadlineMs 取该档案最新一张未撤回邀面卡的时间界:结束时间
// 优先、缺失则退到开始时间(2026-07-30 甲方裁决)。用结束时间是为了让进行中的
// 面试不被提前归为已过去。没有卡、卡的三列全空或档案没有 conversationRef 时
// 整个表达式为 NULL,按同一裁决归入"已面试"。
//
// 取最新卡与 appCandidateSelect、appTodayInterviewsTx 同口径:候选人改期会重发
// 新卡,只有最新那张才代表当前约定。
const appLatestInterviewDeadlineMs = `(SELECT COALESCE(deadline.interview_ends_at_ms, deadline.interview_starts_at_ms)
 FROM messages AS deadline
 WHERE deadline.platform = profile.platform AND deadline.account_ref = profile.account_ref
 AND deadline.conversation_ref = profile.conversation_ref AND deadline.direction = 'out'
 AND deadline.kind = 'card' AND deadline.card_type = 'interviewInvite'
 AND deadline.retracted_at IS NULL ORDER BY deadline.seq DESC LIMIT 1)`

func appCandidateBaseQuery(
	tx *gorm.DB,
	platform, accountRef string,
	view AppCandidateView,
	search string,
	now time.Time,
) (*gorm.DB, error) {
	base := tx.Table("candidate_profiles AS profile").
		Joins("JOIN candidates AS candidate ON candidate.platform = profile.platform "+
			"AND candidate.platform_user_ref = profile.platform_user_ref").
		Joins("LEFT JOIN conversations AS conversation ON conversation.platform = profile.platform "+
			"AND conversation.account_ref = profile.account_ref "+
			"AND conversation.conversation_ref = profile.conversation_ref").
		Joins("LEFT JOIN communication_v4_aggregates AS aggregate ON aggregate.profile_id = profile.profile_id").
		Where("profile.platform = ? AND profile.account_ref = ?", platform, accountRef)
	switch view {
	case AppCandidateViewCommunicating:
		// 已邀面(invited)并入沟通中:规格 §51 把它与已招呼、沟通中同列为推进
		// 态、同在沉默轨上,跟催时钟仍在跑。它没有独立页面,但不能因此从产品
		// 端消失——发了邀面卡、候选人迟迟不确认的人正是要盯的一批。
		base = base.Where("profile.main_status IN ?",
			[]CandidateProfileStatus{
				CandidateProfileGreeted, CandidateProfileCommunicating,
				CandidateProfileInvited, CandidateProfileEnded,
			})
	case AppCandidateViewInterviewed:
		// 已约面且面试时间界尚未过去。时间界读不出来的不留在这里,与
		// interviewElapsed 互补,两个视图不重不漏。
		base = base.Where("profile.main_status = ?", CandidateProfileInterviewed).
			Where(appLatestInterviewDeadlineMs+" >= ?", now.UnixMilli())
	case AppCandidateViewInterviewElapsed:
		// "已面试"是读侧派生的展示分类,不是业务事实:它只说明约定的面试时间
		// 已经过去,不代表面试确实发生过(候选人可能没到场)。系统没有面试
		// 完成写入口,也不打算按这个分类推进任何业务状态。
		base = base.Where("profile.main_status = ?", CandidateProfileInterviewed).
			Where("("+appLatestInterviewDeadlineMs+" IS NULL OR "+
				appLatestInterviewDeadlineMs+" < ?)", now.UnixMilli())
	case AppCandidateViewWechat:
		base = base.Where("EXISTS (SELECT 1 FROM contact_assets AS app_asset "+
			"WHERE app_asset.profile_id = profile.profile_id AND app_asset.kind = ?)", contactAssetKindWechat)
	default:
		return nil, ErrAppProjectionInvalid
	}
	if search != "" {
		pattern := "%" + escapeLike(search) + "%"
		base = base.Where("(COALESCE(candidate.display_name, '') LIKE ? ESCAPE '\\' "+
			"OR COALESCE(profile.position_title, '') LIKE ? ESCAPE '\\')", pattern, pattern)
	}
	return base, nil
}

func escapeLike(input string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(input)
}

func (s *Store) AppCandidateDetail(
	query AppCandidateDetailQuery,
) (*AppCandidateDetailProjection, error) {
	query.Platform = strings.TrimSpace(query.Platform)
	query.AccountRef = strings.TrimSpace(query.AccountRef)
	query.ProfileID = strings.TrimSpace(query.ProfileID)
	if query.ProfileID == "" || len(query.ProfileID) > 128 ||
		!validAppAccountScope(query.Platform, query.AccountRef) {
		return nil, ErrAppProjectionInvalid
	}
	var out AppCandidateDetailProjection
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var rows []appCandidateRow
		if err := tx.Table("candidate_profiles AS profile").
			Select(appCandidateSelect).
			Joins("JOIN candidates AS candidate ON candidate.platform = profile.platform "+
				"AND candidate.platform_user_ref = profile.platform_user_ref").
			Joins("LEFT JOIN conversations AS conversation ON conversation.platform = profile.platform "+
				"AND conversation.account_ref = profile.account_ref "+
				"AND conversation.conversation_ref = profile.conversation_ref").
			Joins("LEFT JOIN communication_v4_aggregates AS aggregate ON aggregate.profile_id = profile.profile_id").
			Where(
				"profile.profile_id = ? AND profile.platform = ? AND profile.account_ref = ?",
				query.ProfileID,
				query.Platform,
				query.AccountRef,
			).Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return ErrAppCandidateNotFound
		}
		if len(rows) != 1 {
			return ErrAppProjectionConflict
		}
		out.Candidate = rows[0].projection()
		var profile CandidateProfile
		if err := tx.First(
			&profile,
			"profile_id = ? AND platform = ? AND account_ref = ?",
			query.ProfileID,
			query.Platform,
			query.AccountRef,
		).Error; err != nil {
			return err
		}
		var err error
		out.Resume, err = appResumeSummaryTx(tx, profile)
		if err != nil {
			return err
		}
		out.Messages, err = appMessagesTx(tx, profile)
		if err != nil {
			return err
		}
		out.LatestAI, err = appLatestAIJudgementTx(tx, profile.ProfileID)
		if err != nil {
			return err
		}
		out.Actions, err = appActionSummariesTx(tx, profile.ProfileID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if out.Messages == nil {
		out.Messages = []AppMessageSummary{}
	}
	if out.Actions == nil {
		out.Actions = []AppActionSummary{}
	}
	return &out, nil
}

func appResumeSummaryTx(tx *gorm.DB, profile CandidateProfile) (AppResumeSummary, error) {
	var snapshot CandidateResumeSnapshot
	var err error
	if profile.ActiveResumeSnapshotID != nil {
		err = tx.First(&snapshot,
			"snapshot_id = ? AND profile_id = ?", *profile.ActiveResumeSnapshotID, profile.ProfileID).Error
	} else {
		err = tx.Where("profile_id = ?", profile.ProfileID).
			Order("captured_at DESC, snapshot_id DESC").First(&snapshot).Error
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return AppResumeSummary{Basic: []AppResumeField{}, Expectations: []AppResumeField{}}, nil
	}
	if err != nil {
		return AppResumeSummary{}, err
	}
	var raw struct {
		Basic           []protocol.CandidateResumeLabelValue `json:"basic"`
		Expectations    []protocol.CandidateResumeLabelValue `json:"expectations"`
		SelfEvaluation  string                               `json:"selfEvaluation"`
		Education       string                               `json:"education"`
		WorkExperiences string                               `json:"workExperiences"`
	}
	if err := json.Unmarshal([]byte(snapshot.ResumeJSON), &raw); err != nil {
		return AppResumeSummary{}, ErrAppProjectionConflict
	}
	summary := AppResumeSummary{Available: true}
	summary.Basic, summary.Truncated = appResumeFields(raw.Basic, 24)
	var truncated bool
	summary.Expectations, truncated = appResumeFields(raw.Expectations, 24)
	summary.Truncated = summary.Truncated || truncated
	summary.SelfEvaluation, truncated = truncateRunesForApp(raw.SelfEvaluation, 800)
	summary.Truncated = summary.Truncated || truncated
	summary.Education, truncated = truncateRunesForApp(raw.Education, 800)
	summary.Truncated = summary.Truncated || truncated
	summary.WorkExperiences, truncated = truncateRunesForApp(raw.WorkExperiences, 800)
	summary.Truncated = summary.Truncated || truncated
	return summary, nil
}

func appResumeFields(
	input []protocol.CandidateResumeLabelValue,
	max int,
) ([]AppResumeField, bool) {
	truncated := len(input) > max
	if len(input) > max {
		input = input[:max]
	}
	out := make([]AppResumeField, 0, len(input))
	for _, field := range input {
		label, labelTruncated := truncateRunesForApp(field.Label, 80)
		value, valueTruncated := truncateRunesForApp(field.Value, 240)
		truncated = truncated || labelTruncated || valueTruncated
		out = append(out, AppResumeField{Label: label, Value: value})
	}
	return out, truncated
}

func truncateRunesForApp(input string, limit int) (string, bool) {
	if utf8.RuneCountInString(input) <= limit {
		return input, false
	}
	runes := []rune(input)
	return string(runes[:limit]), true
}

func appMessagesTx(tx *gorm.DB, profile CandidateProfile) ([]AppMessageSummary, error) {
	if profile.ConversationRef == nil || strings.TrimSpace(*profile.ConversationRef) == "" {
		return []AppMessageSummary{}, nil
	}
	var messages []Message
	if err := tx.Where("platform = ? AND account_ref = ? AND conversation_ref = ? AND retracted_at IS NULL",
		profile.Platform, profile.AccountRef, *profile.ConversationRef).
		Order("seq DESC").Limit(50).Find(&messages).Error; err != nil {
		return nil, err
	}
	out := make([]AppMessageSummary, len(messages))
	for i := range messages {
		message := messages[len(messages)-1-i]
		out[i] = AppMessageSummary{
			Seq: message.Seq, Direction: message.Direction, Kind: message.Kind,
			Text: message.Text, CardType: message.CardType, CardState: message.CardState,
			TsApproxMs: message.TsApproxMs, InterviewStartsAtMs: message.InterviewStartsAtMs,
			InterviewEndsAtMs: message.InterviewEndsAtMs, InterviewMethod: message.InterviewMethod,
		}
	}
	return out, nil
}

func appLatestAIJudgementTx(tx *gorm.DB, profileID string) (AppAIJudgementSummary, error) {
	var turn DialogueTurn
	err := tx.Where("profile_id = ?", profileID).
		Order("created_at DESC, turn_id DESC").First(&turn).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return AppAIJudgementSummary{}, nil
	}
	if err != nil {
		return AppAIJudgementSummary{}, err
	}
	return AppAIJudgementSummary{
		Available: true, Status: string(turn.Status), IntentLabel: string(turn.IntentLabel),
		IntentSource: string(turn.IntentSource), Failure: turn.FailureReason,
		ClassifiedAt: turn.ClassifiedAt,
	}, nil
}

func appActionSummariesTx(tx *gorm.DB, profileID string) ([]AppActionSummary, error) {
	type actionRow struct {
		Kind      string
		Status    string
		Failure   string
		CreatedAt time.Time
	}
	var rows []actionRow
	if err := tx.Raw(`
SELECT action.kind AS kind, action.status AS status, action.failure_reason AS failure,
       action.created_at AS created_at
FROM communication_actions AS action
JOIN dialogue_turns AS turn ON turn.turn_id = action.turn_id
WHERE turn.profile_id = ?
UNION ALL
SELECT event.v4_kind AS kind, event.status AS status, event.failure_reason AS failure,
       event.created_at AS created_at
FROM communication_v4_event_actions AS event
WHERE event.profile_id = ?
ORDER BY created_at DESC
LIMIT 30`, profileID, profileID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].CreatedAt.After(rows[j].CreatedAt) })
	out := make([]AppActionSummary, len(rows))
	for i := range rows {
		createdAt := rows[i].CreatedAt
		out[i] = AppActionSummary{
			Kind: rows[i].Kind, Status: rows[i].Status,
			Failure: rows[i].Failure, CreatedAt: &createdAt,
		}
	}
	return out, nil
}
