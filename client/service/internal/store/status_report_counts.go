package store

import (
	"time"

	"gorm.io/gorm"
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
		return tx.Table("communication_v4_aggregates AS agg").
			Joins("JOIN candidate_profiles AS profile ON profile.profile_id = agg.profile_id").
			Where("agg.automation_status = ?", ProfileCommunicationAutomationManualRequired).
			Where("profile.platform = ? AND profile.account_ref = ?", platform, accountRef).
			Count(&out.ManualRequiredProfiles).Error
	})
	if err != nil {
		return StatusReportCounts{}, err
	}
	if out.TodayCapturedByJob == nil {
		out.TodayCapturedByJob = []StatusReportJobCount{}
	}
	return out, nil
}
