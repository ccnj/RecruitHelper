package store

import (
	"time"

	"gorm.io/gorm"

	"recruithelper/client/service/internal/m5ai"
)

// 工作状态上报要的两个数,现有投影都算不出来(AGENTS.md「全局约定·工作状态上报」,
// 2026-08-06 甲方裁决):
//
//   - 今日采集人数。AppFunnelProjection.CapturedCount 是"当前这一批采了多少",
//     一天里可能开过好几批,运营要看的是整天。
//   - 已转人工的候选人数。它是"这台机器攒了多少要人处理的活",与 suspect 队列
//     (那是命令级的不确定)不是一回事。
//
// 两个数都不带候选人身份出来 —— 上报载荷只有计数。

// StatusReportJobCount 是今天某个职位采了多少人。职位是客户自己的,不是候选人信息。
type StatusReportJobCount struct {
	BackendJobID string `json:"backendJobId,omitempty"`
	PositionRef  string `json:"-"`
	Name         string `json:"name,omitempty"`
	Captured     int64  `json:"captured"`
}

type StatusReportCounts struct {
	TodayCaptured          int64
	TodayCapturedByJob     []StatusReportJobCount
	ManualRequiredProfiles int64
	// 拒绝按「最近一轮意向」口径(2026-08-14 甲方裁决,AGENTS.md 工作状态上报
	// 条目):候选人最新的已分类轮 intent_label=rejected 即计入。这是状态口径:
	// 挽留阶梯在途的、拒一次就沉默的都算;拒后改口的自动移出,所以 Current
	// 会回落,不是只增不减的流水数。归档口径不能用 —— 拒绝要爬完挽留/收尾
	// 阶梯才归档 rejected,拒一次就沉默的人最终归档在沉默族,会把拒绝数数漏。
	CurrentRejected int64
	TodayRejected   int64 // 末轮是拒绝,且该轮在 [start, end) 内判定
	// 拉黑按归档原因数:拉黑经平台拒收通知判定,不产生拒绝轮,上面的口径抓不到。
	TotalBlacklisted int64
	TodayBlacklisted int64
}

// StatusReportCounts 统计 [start, end) 内该账号采集到的人数与当前转人工人数。
//
// 采集按 platform_user_ref 去重:同一个人被两个批次各采一次,对运营来说仍然是
// 一个人。这与产品首页各项"按人计"的口径一致。
func (s *Store) StatusReportCounts(
	platform, accountRef string, start, end time.Time,
) (StatusReportCounts, error) {
	var out StatusReportCounts
	if platform == "" || accountRef == "" || !end.After(start) {
		// 零账号(全新安装、还没登录)是正常状态,不是错误:照常上报全零,
		// 那恰恰是最需要被看见的状态。
		return out, nil
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&SourcingCandidateRun{}).
			Where("platform = ? AND account_ref = ?", platform, accountRef).
			Where("captured_at >= ? AND captured_at < ?", start, end).
			Distinct("platform_user_ref").
			Count(&out.TodayCaptured).Error; err != nil {
			return err
		}

		rows := make([]StatusReportJobCount, 0, 4)
		if err := tx.Table("sourcing_candidate_runs AS run").
			Select(
				"COALESCE(batch.backend_job_id, '') AS backend_job_id, "+
					"COALESCE(batch.position_title, '') AS name, "+
					"COUNT(DISTINCT run.platform_user_ref) AS captured",
			).
			Joins("LEFT JOIN sourcing_batches AS batch ON batch.batch_id = run.batch_id").
			Where("run.platform = ? AND run.account_ref = ?", platform, accountRef).
			Where("run.captured_at >= ? AND run.captured_at < ?", start, end).
			Group("batch.backend_job_id, batch.position_title").
			Order("captured DESC").
			Scan(&rows).Error; err != nil {
			return err
		}
		out.TodayCapturedByJob = rows

		// 转人工是当前状态而非当日事件,所以不切时间窗:关心的是"此刻积压多少"。
		if err := tx.Table("communication_v4_aggregates AS agg").
			Joins("JOIN candidate_profiles AS profile ON profile.profile_id = agg.profile_id").
			Where("agg.automation_status = ?", ProfileCommunicationAutomationManualRequired).
			Where("profile.platform = ? AND profile.account_ref = ?", platform, accountRef).
			Count(&out.ManualRequiredProfiles).Error; err != nil {
			return err
		}

		// 每人取最新已分类轮:先按 inbound_through_seq(输入边界推进),再按
		// created_at 破平(同边界重铸的轮)。最新一轮还没分类完时取上一轮,
		// 统计接受这点分钟级滞后。classified_at 与 start/end 都经 GORM 同一套
		// time.Time 序列化,不踩本库"时间列两种落盘格式"的字符串比较坑。
		var rejected struct {
			CurrentRejected int64
			TodayRejected   int64
		}
		if err := tx.Raw(`
			SELECT
			  COUNT(*) AS current_rejected,
			  COALESCE(SUM(CASE WHEN latest.classified_at >= ? AND latest.classified_at < ? THEN 1 ELSE 0 END), 0) AS today_rejected
			FROM candidate_profiles AS profile
			JOIN dialogue_turns AS latest ON latest.turn_id = (
			  SELECT turn.turn_id FROM dialogue_turns AS turn
			  WHERE turn.profile_id = profile.profile_id AND turn.intent_label <> ''
			  ORDER BY turn.inbound_through_seq DESC, turn.created_at DESC
			  LIMIT 1
			)
			WHERE profile.platform = ? AND profile.account_ref = ?
			  AND latest.intent_label = ?`,
			start, end, platform, accountRef, string(m5ai.IntentRejected),
		).Scan(&rejected).Error; err != nil {
			return err
		}
		out.CurrentRejected = rejected.CurrentRejected
		out.TodayRejected = rejected.TodayRejected

		if err := tx.Model(&CandidateProfile{}).
			Where("platform = ? AND account_ref = ?", platform, accountRef).
			Where("end_reason = ?", CandidateProfileEndBlacklisted).
			Count(&out.TotalBlacklisted).Error; err != nil {
			return err
		}
		// 今日拉黑用 updated_at 锚:档案没有归档时刻列,而拉黑归档是终态、行
		// 不再被业务写,updated_at 即归档时刻。弱点是将来若有迁移类批量回写会
		// 污染当日数一天;它只是显示数字,不进任何业务裁决,接受这个精度。
		return tx.Model(&CandidateProfile{}).
			Where("platform = ? AND account_ref = ?", platform, accountRef).
			Where("end_reason = ?", CandidateProfileEndBlacklisted).
			Where("updated_at >= ? AND updated_at < ?", start, end).
			Count(&out.TodayBlacklisted).Error
	})
	if err != nil {
		return StatusReportCounts{}, err
	}
	if out.TodayCapturedByJob == nil {
		out.TodayCapturedByJob = []StatusReportJobCount{}
	}
	return out, nil
}
