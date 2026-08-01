package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"recruithelper/client/service/internal/m5ai"
)

// InterviewScheduleSetting 是本机可面试时段周表。单行表，ID 恒为 1，整张周表
// 存成一份 JSON。
//
// 之所以是单行 JSON 而不是老项目那种"一行一个时段"的多行表：多行表的保存语义是
// DELETE 全表 + 重新 INSERT，与 AGENTS.md「业务事实行禁止物理 DELETE」冲突；
// 单行 Save 天然是原子整表替换，也没有半张表的中间态。
//
// **行不存在与行存在且为空是两件事**：前者表示从未配置过，读取时回落到内置默认
// 周表；后者在写入侧已被 ValidateInterviewSchedule 拒绝，不该出现。老项目正是分不清
// 这两者，导致清空后界面显示默认、实际一个时段都没有。
type InterviewScheduleSetting struct {
	ID uint `gorm:"primaryKey"`
	// ScheduleJSON 是 m5ai.InterviewSchedule 的 JSON。存文本而不是结构化列，
	// 是因为周表整体读写、从不按天查询，且诊断台直接看得懂。
	ScheduleJSON string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

const interviewScheduleSettingID = 1

// InterviewSchedule 读当前周表。表里没有行时返回内置默认周表，不隐式建行。
//
// 解析失败一律返回错误，**绝不静默回落到默认**：那会让脑按一张用户已经改掉的表
// 去承诺面试时间，属于错约。调用方应据此转人工。
func (s *Store) InterviewSchedule() (m5ai.InterviewSchedule, error) {
	var setting InterviewScheduleSetting
	err := s.db.First(&setting, interviewScheduleSettingID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return m5ai.DefaultInterviewSchedule(), nil
		}
		return nil, err
	}
	var schedule m5ai.InterviewSchedule
	if err := json.Unmarshal([]byte(setting.ScheduleJSON), &schedule); err != nil {
		return nil, fmt.Errorf("可面试时段配置无法解析: %w", err)
	}
	if err := m5ai.ValidateInterviewSchedule(schedule); err != nil {
		return nil, fmt.Errorf("可面试时段配置非法: %w", err)
	}
	return schedule, nil
}

// SetInterviewSchedule 整表替换。写入前必须通过校验——空表、非整点、起止倒置都
// 在这里挡掉，不依赖调用方或 UI 先行把关。
func (s *Store) SetInterviewSchedule(schedule m5ai.InterviewSchedule) error {
	if err := m5ai.ValidateInterviewSchedule(schedule); err != nil {
		return err
	}
	raw, err := json.Marshal(schedule)
	if err != nil {
		return err
	}
	return s.db.Save(&InterviewScheduleSetting{
		ID:           interviewScheduleSettingID,
		ScheduleJSON: string(raw),
	}).Error
}
