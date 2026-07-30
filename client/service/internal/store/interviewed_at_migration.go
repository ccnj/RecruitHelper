package store

import (
	"time"

	"gorm.io/gorm"
)

// interviewedAtBackfillMoment 是历史"已约面"档案的补记时刻(2026-07-30 甲方
// 裁决:历史数据统一填 2026-07-29 18:00 本地时间)。它是补记,不是观测事实:
// 这批档案进入已约面时系统还没有记录该时刻的列。取一个已经过去的固定时刻,
// 使它们既不落入任何将来的"今日新约面",也不会因为回填而虚增当天数字。
var interviewedAtBackfillMoment = time.Date(2026, 7, 29, 18, 0, 0, 0, time.Local)

// backfillInterviewedAt 只在旧库首次引入 interviewed_at 列时跑一次。
//
// 之所以由调用方传入"列此前是否存在"而不是就地判断 IS NULL:列已经存在之后,
// 某个已约面档案的 interviewed_at 为空只能是写入点漏写,属于损坏。若每次启动
// 都按 IS NULL 补记,就会把那种 bug 静默盖成"2026-07-29 18:00",与 store.go
// 里 head 表迁移同一条纪律——重启不得重算并掩盖。
func backfillInterviewedAt(db *gorm.DB, columnExisted bool) (int64, error) {
	if columnExisted {
		return 0, nil
	}
	updated := db.Model(&CandidateProfile{}).
		Where("main_status = ? AND interviewed_at IS NULL", CandidateProfileInterviewed).
		Update("interviewed_at", interviewedAtBackfillMoment)
	if updated.Error != nil {
		return 0, updated.Error
	}
	return updated.RowsAffected, nil
}
