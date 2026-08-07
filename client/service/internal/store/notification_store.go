// 运营通知发件箱与取证截图事实(AGENTS.md「运营通知 webhook」2026-07-28 裁决)。
// 入队只发生在两个业务事实的提交事务内:换微信成功(ContactAsset 创建)与约面
// 成功(V4 主线真迁入 interviewed);幂等由 EventKey 唯一索引保证。发送、闸门与
// 去重语义在 notify runner(照抄旧项目 notify.py),本文件只提供账本操作。
package store

import (
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
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

// WechatExchangeInitiator* 是微信互加通知行事件元数据里的发起方枚举:入队时按
// 请求卡方向判定一次,供 runner 裁决 2 小时并发窗口(2026-08-06 甲方裁决)。
const (
	WechatExchangeInitiatorPeer    = "peer"    // 候选人主动发起换微信
	WechatExchangeInitiatorSelf    = "self"    // 我方发起邀请被接受
	WechatExchangeInitiatorUnknown = "unknown" // 判不出,行为等同我方发起(立即发)
)

type wechatAddedPayload struct {
	ExchangeInitiator string `json:"exchangeInitiator"`
}

func encodeWechatAddedPayload(initiator string) string {
	body, err := json.Marshal(wechatAddedPayload{ExchangeInitiator: initiator})
	if err != nil {
		return "{}"
	}
	return string(body)
}

// WechatAddedExchangeInitiator 解析微信互加通知行的发起方;历史行("{}")、解析
// 失败或值越界一律 unknown——unknown 的行为方向是立即发送,不会押住通知。
func WechatAddedExchangeInitiator(payloadJSON string) string {
	var payload wechatAddedPayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return WechatExchangeInitiatorUnknown
	}
	switch payload.ExchangeInitiator {
	case WechatExchangeInitiatorPeer, WechatExchangeInitiatorSelf:
		return payload.ExchangeInitiator
	default:
		return WechatExchangeInitiatorUnknown
	}
}

// enqueueNotificationTx 在业务事务内幂等入队;已存在同 EventKey 时静默忽略。
// payloadJSON 是该行的事件元数据(空串按 "{}" 落库),不得携带候选人明文。
func enqueueNotificationTx(
	tx *gorm.DB,
	notifyType string,
	eventKey string,
	profileID string,
	payloadJSON string,
	at time.Time,
) error {
	if tx == nil || strings.TrimSpace(eventKey) == "" || strings.TrimSpace(profileID) == "" {
		return errors.New("通知入队参数不完整")
	}
	if strings.TrimSpace(payloadJSON) == "" {
		payloadJSON = "{}"
	}
	row := NotificationOutbox{
		NotifyType:  notifyType,
		EventKey:    eventKey,
		ProfileID:   profileID,
		PayloadJSON: payloadJSON,
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

// SaveSuspectSceneShot 追加一行 suspect 现场截图事实(2026-08-07 甲方裁决)。
// 不覆盖旧行、不物理删除;失败由调用方以审计记录,不重试。
func (s *Store) SaveSuspectSceneShot(
	msgID string,
	intentID string,
	primitive string,
	blobRef string,
	byteSize int64,
	capturedAtMs int64,
	at time.Time,
) error {
	if strings.TrimSpace(msgID) == "" || strings.TrimSpace(blobRef) == "" ||
		strings.TrimSpace(primitive) == "" {
		return errors.New("suspect 现场截图事实参数不完整")
	}
	return s.db.Create(&SuspectSceneShot{
		MsgID:        msgID,
		IntentID:     intentID,
		Primitive:    primitive,
		BlobRef:      blobRef,
		ByteSize:     byteSize,
		CapturedAtMs: capturedAtMs,
		CreatedAt:    at,
	}).Error
}

// SuspectSceneShotsByMsgID 取某条 suspect 命令的现场截图事实(诊断台用)。
func (s *Store) SuspectSceneShotsByMsgID(msgID string) ([]SuspectSceneShot, error) {
	var rows []SuspectSceneShot
	err := s.db.Where("msg_id = ?", msgID).Order("id ASC").Find(&rows).Error
	return rows, err
}

var candidateMobileRe = regexp.MustCompile(`^1[3-9]\d{9}$`)

// AcceptCandidatePhoneObservation 判定取证顺访读到的电话能否收编为观察事实:
// 号码须是 11 位手机格式;面板姓名与会话对方展示名按**首字符**核对——同一
// 候选人可能一处真名一处「X先生」(2026-08-06 甲方裁决只核第一个字)。任一
// 不满足或锚点缺失都不收编,方向是通知少一行手机号,绝不冒认。
func AcceptCandidatePhoneObservation(phone, panelName, peerDisplayName string) bool {
	if !candidateMobileRe.MatchString(strings.TrimSpace(phone)) {
		return false
	}
	panel := []rune(strings.TrimSpace(panelName))
	peer := []rune(strings.TrimSpace(peerDisplayName))
	if len(panel) == 0 || len(peer) == 0 {
		return false
	}
	return panel[0] == peer[0]
}

// SaveCandidatePhoneObservation 追加一行电话观察事实(不覆盖旧行,消费方取最新)。
func (s *Store) SaveCandidatePhoneObservation(
	profileID string,
	phone string,
	observedAtMs int64,
	at time.Time,
) error {
	if strings.TrimSpace(profileID) == "" || strings.TrimSpace(phone) == "" {
		return errors.New("电话观察事实参数不完整")
	}
	return s.db.Create(&CandidatePhoneObservation{
		ProfileID:    profileID,
		Phone:        phone,
		ObservedAtMs: observedAtMs,
		CreatedAt:    at,
	}).Error
}

// TryMarkPhoneRevealAttempt 标记先行:首次落行返回 true(允许派发揭示),
// 已存在返回 false(终身不再派发)。行不删除、不更新。
func (s *Store) TryMarkPhoneRevealAttempt(profileID string, at time.Time) (bool, error) {
	if strings.TrimSpace(profileID) == "" {
		return false, errors.New("揭示标记参数不完整")
	}
	result := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "profile_id"}},
		DoNothing: true,
	}).Create(&PhoneRevealAttempt{ProfileID: profileID, CreatedAt: at})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// LatestCandidatePhone 返回该候选人最新一行电话观察事实的号码;无则空串。
func (s *Store) LatestCandidatePhone(profileID string) (string, error) {
	var row CandidatePhoneObservation
	err := s.db.
		Where("profile_id = ?", profileID).
		Order("id DESC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return row.Phone, nil
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
	ProfileID     string
	DisplayName   string
	PositionTitle string
	MainStatus    CandidateProfileStatus
	WechatState   string
	WechatID      string
	// PhoneNumber 来自取证顺访的电话观察事实(2026-08-06 裁决),缺失渲染侧
	// 整行省略;与 WechatID 同受"不回流进日志/审计/管理 API"边界约束。
	PhoneNumber         string
	InterviewStartsAtMs *int64
	// InterviewMethod 取自同一张邀面卡(契约封闭枚举 wechatVideo/onsite,
	// 2026-08-07 甲方裁决进通知),空串=未知,渲染侧整行省略。
	InterviewMethod string
	ChatShot        *CandidateScreenshot
	ResumeShot      *CandidateScreenshot
	// 画像摘要:AGENTS.md 2026-07-28 补充裁决允许的封闭四项,逐项可空。
	// 简历未采到、快照结构异常或标签缺失时留空,渲染侧逐项省略,绝不阻断通知。
	Age           string
	Education     string
	City          string
	DesiredSalary string
}

// notificationProfileLabels 是画像摘要四项在简历快照里的标签名。
// 只认这四个精确标签,不做同义猜测——猜错会把别的简历内容带出本机。
var notificationProfileLabels = struct {
	age, education, city, salary string
}{age: "年龄", education: "最高学历", city: "现居地", salary: "期望薪资"}

const notificationProfileValueMaxRunes = 24

// fillNotificationProfileSummary 从简历快照抽取画像四项。
// 全程尽力而为:没有快照、JSON 解析失败、分区不是数组、标签缺失、值超长,
// 任何一种都只让对应项留空,不返回错误、不阻断通知发送。
func fillNotificationProfileSummary(snapshot *NotificationRenderSnapshot, resumeJSON string) {
	resumeJSON = strings.TrimSpace(resumeJSON)
	if resumeJSON == "" {
		return
	}
	var parsed struct {
		Basic []struct {
			Label string `json:"label"`
			Value string `json:"value"`
		} `json:"basic"`
		Expectations []struct {
			Label string `json:"label"`
			Value string `json:"value"`
		} `json:"expectations"`
	}
	if err := json.Unmarshal([]byte(resumeJSON), &parsed); err != nil {
		return
	}
	take := func(value string) string {
		value = strings.TrimSpace(value)
		if value == "" {
			return ""
		}
		// 简历值理论上短,但异常快照可能塞进整段文字;截断保证正文不被撑爆。
		runes := []rune(value)
		if len(runes) > notificationProfileValueMaxRunes {
			return string(runes[:notificationProfileValueMaxRunes]) + "…"
		}
		return value
	}
	for _, item := range parsed.Basic {
		switch strings.TrimSpace(item.Label) {
		case notificationProfileLabels.age:
			snapshot.Age = take(item.Value)
		case notificationProfileLabels.education:
			snapshot.Education = take(item.Value)
		case notificationProfileLabels.city:
			snapshot.City = take(item.Value)
		}
	}
	for _, item := range parsed.Expectations {
		if strings.TrimSpace(item.Label) == notificationProfileLabels.salary {
			snapshot.DesiredSalary = take(item.Value)
		}
	}
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
	// 姓名优先取 IM 会话里的对方展示名:候选人身份根上的名字可能来自推荐/采集
	// 列表的脱敏形态(如"胡先生"),而运营要靠这个名字在微信里对上人。真机上
	// 两者绝大多数一致,少数不一致时会话侧才是真名。会话缺失时回落身份根。
	if profile.ConversationRef != nil {
		var conversation Conversation
		convErr := s.db.First(
			&conversation,
			"platform = ? AND account_ref = ? AND conversation_ref = ?",
			profile.Platform,
			profile.AccountRef,
			*profile.ConversationRef,
		).Error
		if convErr == nil {
			snapshot.DisplayName = strings.TrimSpace(conversation.PeerDisplayName)
		} else if !errors.Is(convErr, gorm.ErrRecordNotFound) {
			return nil, convErr
		}
	}
	if snapshot.DisplayName == "" {
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
	phone, err := s.LatestCandidatePhone(profileID)
	if err != nil {
		return nil, err
	}
	snapshot.PhoneNumber = phone
	// 画像摘要:没有活动快照就整行不出,读取失败也只当没有,绝不阻断通知。
	if profile.ActiveResumeSnapshotID != nil {
		var resume CandidateResumeSnapshot
		resumeErr := s.db.First(
			&resume,
			"snapshot_id = ? AND profile_id = ?",
			*profile.ActiveResumeSnapshotID,
			profileID,
		).Error
		if resumeErr == nil {
			fillNotificationProfileSummary(snapshot, resume.ResumeJSON)
		} else if !errors.Is(resumeErr, gorm.ErrRecordNotFound) {
			slog.Warn("运营通知画像摘要读取失败,按无画像发送", "profileId", profileID, "err", resumeErr)
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
			if card.InterviewMethod != nil {
				snapshot.InterviewMethod = strings.TrimSpace(*card.InterviewMethod)
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	return snapshot, nil
}
