// 运营通知发件箱与取证截图事实(AGENTS.md「运营通知 webhook」2026-07-28 裁决)。
// 入队只发生在两个业务事实的提交事务内:换微信成功(ContactAsset 创建)与约面
// 成功(V4 主线真迁入 interviewed);幂等由 EventKey 唯一索引保证。发送、闸门与
// 去重语义在 notify runner(照抄旧项目 notify.py),本文件只提供账本操作。
package store

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	NotificationTypeInterviewAccepted = "wecomInterviewAccepted"
	NotificationTypeWechatAdded       = "wecomWechatAdded"

	NotificationStatusPending = "pending"
	NotificationStatusSent    = "sent"
	NotificationStatusFailed  = "failed"
	NotificationStatusSkipped = "skipped"
	NotificationStatusExpired = "expired"

	CandidateScreenshotKindChat   = "chat"
	CandidateScreenshotKindResume = "resume"
)

// enqueueNotificationTx 在业务事务内幂等入队;已存在同 EventKey 时静默忽略。
func enqueueNotificationTx(
	tx *gorm.DB,
	notifyType string,
	eventKey string,
	profileID string,
	at time.Time,
) error {
	if tx == nil || strings.TrimSpace(eventKey) == "" || strings.TrimSpace(profileID) == "" {
		return errors.New("通知入队参数不完整")
	}
	row := NotificationOutbox{
		NotifyType:  notifyType,
		EventKey:    eventKey,
		ProfileID:   profileID,
		PayloadJSON: "{}",
		Status:      NotificationStatusPending,
		CreatedAt:   at,
		UpdatedAt:   at,
	}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "event_key"}},
		DoNothing: true,
	}).Create(&row).Error
}

// TakePendingNotifications 取一批待发送通知(不含已耗尽重试者),按入队顺序。
func (s *Store) TakePendingNotifications(limit int, maxAttempts int) ([]NotificationOutbox, error) {
	var rows []NotificationOutbox
	err := s.db.
		Where("status = ? AND attempts < ?", NotificationStatusPending, maxAttempts).
		Order("id ASC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// MarkNotificationSent 落终态 sent;sentWithWechat 只对约面通知有意义。
func (s *Store) MarkNotificationSent(id uint64, sentWithWechat bool, at time.Time) error {
	return s.db.Model(&NotificationOutbox{}).
		Where("id = ? AND status = ?", id, NotificationStatusPending).
		Updates(map[string]any{
			"status":           NotificationStatusSent,
			"sent_with_wechat": sentWithWechat,
			"sent_at":          at,
			"updated_at":       at,
		}).Error
}

// MarkNotificationFailed 记一次失败;达到 maxAttempts 落终态 failed,否则保持 pending 等下轮。
func (s *Store) MarkNotificationFailed(id uint64, lastError string, maxAttempts int, at time.Time) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var row NotificationOutbox
		if err := tx.First(&row, "id = ?", id).Error; err != nil {
			return err
		}
		if row.Status != NotificationStatusPending {
			return nil
		}
		updates := map[string]any{
			"attempts":   row.Attempts + 1,
			"last_error": truncateErrorText(lastError),
			"updated_at": at,
		}
		if row.Attempts+1 >= maxAttempts {
			updates["status"] = NotificationStatusFailed
		}
		return tx.Model(&NotificationOutbox{}).Where("id = ?", id).Updates(updates).Error
	})
}

// MarkNotificationSkipped 记终态 skipped(如微信号已随约面通知带出的去重丢弃)。
func (s *Store) MarkNotificationSkipped(id uint64, reason string, at time.Time) error {
	return s.db.Model(&NotificationOutbox{}).
		Where("id = ? AND status = ?", id, NotificationStatusPending).
		Updates(map[string]any{
			"status":     NotificationStatusSkipped,
			"last_error": truncateErrorText(reason),
			"updated_at": at,
		}).Error
}

// ExpireStaleNotifications 把超龄未发出的 pending 标记 expired,返回条数。
func (s *Store) ExpireStaleNotifications(olderThan time.Duration, at time.Time) (int64, error) {
	cutoff := at.Add(-olderThan)
	result := s.db.Model(&NotificationOutbox{}).
		Where("status = ? AND created_at < ?", NotificationStatusPending, cutoff).
		Updates(map[string]any{"status": NotificationStatusExpired, "updated_at": at})
	return result.RowsAffected, result.Error
}

// InterviewNotificationForProfile 返回该候选人的约面通知行(微信互加去重判定用);无则 nil。
func (s *Store) InterviewNotificationForProfile(profileID string) (*NotificationOutbox, error) {
	var row NotificationOutbox
	err := s.db.First(
		&row,
		"notify_type = ? AND profile_id = ?",
		NotificationTypeInterviewAccepted,
		profileID,
	).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// NotificationsNeedingCapture 返回该候选人仍 pending 且未派发过取证截图的通知。
func (s *Store) NotificationsNeedingCapture(profileID string) ([]NotificationOutbox, error) {
	var rows []NotificationOutbox
	err := s.db.
		Where(
			"profile_id = ? AND status = ? AND assets_requested_at IS NULL",
			profileID,
			NotificationStatusPending,
		).
		Order("id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// MarkNotificationsAssetsRequested 记录取证已派发(每通知至多一轮,失败不重拍)。
func (s *Store) MarkNotificationsAssetsRequested(ids []uint64, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	return s.db.Model(&NotificationOutbox{}).
		Where("id IN ? AND assets_requested_at IS NULL", ids).
		Updates(map[string]any{"assets_requested_at": at, "updated_at": at}).Error
}

// SaveCandidateScreenshot 追加一行取证截图事实(不覆盖旧行,消费方取最新)。
func (s *Store) SaveCandidateScreenshot(
	profileID string,
	kind string,
	blobRef string,
	byteSize int64,
	truncated bool,
	capturedAtMs int64,
	at time.Time,
) error {
	if strings.TrimSpace(profileID) == "" || strings.TrimSpace(blobRef) == "" ||
		(kind != CandidateScreenshotKindChat && kind != CandidateScreenshotKindResume) {
		return errors.New("截图事实参数不完整")
	}
	return s.db.Create(&CandidateScreenshot{
		ProfileID:    profileID,
		Kind:         kind,
		BlobRef:      blobRef,
		ByteSize:     byteSize,
		Truncated:    truncated,
		CapturedAtMs: capturedAtMs,
		CreatedAt:    at,
	}).Error
}

// LatestCandidateScreenshots 返回该候选人各 kind 的最新截图行。
func (s *Store) LatestCandidateScreenshots(profileID string) (map[string]CandidateScreenshot, error) {
	var rows []CandidateScreenshot
	if err := s.db.
		Where("profile_id = ?", profileID).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	latest := map[string]CandidateScreenshot{}
	for _, row := range rows {
		latest[row.Kind] = row
	}
	return latest, nil
}

func truncateErrorText(text string) string {
	const limit = 500
	if len(text) <= limit {
		return text
	}
	return text[:limit]
}

// NotificationRenderSnapshot 是通知正文按发送时刻现算所需的最新业务事实。
// WechatID 来自权威 ContactAsset;运营通知 webhook 正文是其获准位置之一
// (AGENTS.md 2026-07-28 裁决),本快照不得回流进日志、审计或管理 API。
type NotificationRenderSnapshot struct {
	ProfileID           string
	DisplayName         string
	PositionTitle       string
	MainStatus          CandidateProfileStatus
	WechatState         string
	WechatID            string
	InterviewStartsAtMs *int64
	ChatShot            *CandidateScreenshot
	ResumeShot          *CandidateScreenshot
}

// NotificationRenderSnapshotForProfile 汇集渲染一条运营通知所需的最新事实。
func (s *Store) NotificationRenderSnapshotForProfile(profileID string) (*NotificationRenderSnapshot, error) {
	profile, err := s.CandidateProfileByID(profileID)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, nil
	}
	snapshot := &NotificationRenderSnapshot{
		ProfileID:  profileID,
		MainStatus: profile.MainStatus,
	}
	if profile.PositionTitle != nil {
		snapshot.PositionTitle = strings.TrimSpace(*profile.PositionTitle)
	}
	var person Candidate
	err = s.db.First(
		&person,
		"platform = ? AND platform_user_ref = ?",
		profile.Platform,
		profile.PlatformUserRef,
	).Error
	if err == nil && person.DisplayName != nil {
		snapshot.DisplayName = strings.TrimSpace(*person.DisplayName)
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	aggregate, err := s.CommunicationV4AggregateByProfile(profileID)
	if err == nil {
		snapshot.WechatState = string(aggregate.State.WechatState)
	} else if !errors.Is(err, ErrCommunicationV4Missing) {
		return nil, err
	}
	assets, err := s.ContactAssetsByProfile(profileID)
	if err != nil {
		return nil, err
	}
	for index := len(assets) - 1; index >= 0; index-- {
		if assets[index].Kind == contactAssetKindWechat {
			snapshot.WechatID = assets[index].Value
			break
		}
	}
	shots, err := s.LatestCandidateScreenshots(profileID)
	if err != nil {
		return nil, err
	}
	if shot, ok := shots[CandidateScreenshotKindChat]; ok {
		copied := shot
		snapshot.ChatShot = &copied
	}
	if shot, ok := shots[CandidateScreenshotKindResume]; ok {
		copied := shot
		snapshot.ResumeShot = &copied
	}
	if profile.ConversationRef != nil {
		var card Message
		err := s.db.
			Where(
				"platform = ? AND account_ref = ? AND conversation_ref = ? AND interview_starts_at_ms IS NOT NULL",
				profile.Platform,
				profile.AccountRef,
				*profile.ConversationRef,
			).
			Order("seq DESC").
			First(&card).Error
		if err == nil {
			snapshot.InterviewStartsAtMs = card.InterviewStartsAtMs
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	return snapshot, nil
}
