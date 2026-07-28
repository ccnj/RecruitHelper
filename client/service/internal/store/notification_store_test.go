package store

import (
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"recruithelper/client/service/internal/communication"
)

func outboxRows(t *testing.T, s *Store, notifyType string) []NotificationOutbox {
	t.Helper()
	var rows []NotificationOutbox
	if err := s.db.Where("notify_type = ?", notifyType).Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

// 约面成功入队:主线真迁入 interviewed 的那次提交同事务入队;事件重放不增生。
func TestInterviewAcceptedEnqueuesNotificationExactlyOnce(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	_, root := seedSuccessfulV4Greeting(t, s, "notify-interview", "conversation-notify-interview", at)

	expression := communication.BusinessEvent{
		Key: "message:2", Kind: communication.EventCandidateExpressionReceived,
		Source: communication.EventSourceMessage, MessageSeq: 2,
		OccurredAt: &at, ExpressionKind: communication.ExpressionText, Text: "可以聊聊",
	}
	if _, err := s.ApplyCommunicationV4BusinessEvent(ApplyCommunicationV4BusinessEventRequest{
		ProfileID: root.ProfileID, Event: expression, AppliedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	if rows := outboxRows(t, s, NotificationTypeInterviewAccepted); len(rows) != 0 {
		t.Fatalf("普通表达不得入队约面通知: %+v", rows)
	}

	accepted, err := communication.NormalizeCardTransition(communication.LedgerCardTransitionFact{
		MessageSeq: 2, CardType: "interviewInvite", FromState: "pending", ToState: "accepted",
		OccurredAt: &at,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.ApplyCommunicationV4BusinessEvent(ApplyCommunicationV4BusinessEventRequest{
		ProfileID: root.ProfileID, Event: accepted, AppliedAt: at.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Aggregate.State.MainStatus != communication.V4StatusInterviewed {
		t.Fatalf("主线未迁入 interviewed: %+v", first.Aggregate.State)
	}
	rows := outboxRows(t, s, NotificationTypeInterviewAccepted)
	if len(rows) != 1 || rows[0].ProfileID != root.ProfileID ||
		rows[0].EventKey != "interviewAccepted:"+root.ProfileID ||
		rows[0].Status != NotificationStatusPending {
		t.Fatalf("约面通知入队不符: %+v", rows)
	}
	// 同一事务的两轨衔接:发件箱持有唯一发送义务,事件动作轨的通知行只是
	// 不可变记录,必须标记为发件箱承接而不是待发欠账(2026-07-28 收束)。
	eventActions, err := s.CommunicationV4EventActionsBySource(
		root.ProfileID,
		CommunicationV4InputBusinessEvent,
		accepted.Key,
	)
	if err != nil {
		t.Fatal(err)
	}
	notifyRows := 0
	for _, action := range eventActions {
		if action.V4Kind != communication.V4ActionNotifyInterviewAccepted {
			continue
		}
		notifyRows++
		if action.Status != CommunicationV4EventActionDeferred ||
			action.FailureReason != CommunicationV4EventActionFailureNotificationOutboxOwned {
			t.Fatalf("约面通知事件动作行未标记为发件箱承接: %+v", action)
		}
	}
	if notifyRows != 1 {
		t.Fatalf("约面通知事件动作行数不符: actions=%+v", eventActions)
	}

	if _, err := s.ApplyCommunicationV4BusinessEvent(ApplyCommunicationV4BusinessEventRequest{
		ProfileID: root.ProfileID, Event: accepted, AppliedAt: at.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if rows := outboxRows(t, s, NotificationTypeInterviewAccepted); len(rows) != 1 {
		t.Fatalf("事件重放导致通知增生: %+v", rows)
	}
}

// 真实链路回归:候选人接受在真机表现为 in 方向 accepted 卡消息,单独成轮经
// FreezeCommunicationV4Turn 迁入 interviewed,不产生卡片跃迁事实(手侧我方
// 邀面卡状态恒 unknown)。入队钩子必须在该路径同样生效——挂在全部 V4 聚合
// 转换的唯一持久化汇点 persistCommunicationV4TransitionTx。
func TestInterviewAcceptedEnqueuesOnInboundTurnFreeze(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "notify-freeze")
	// 账本先落 seq2 文本与 seq3 accepted 卡,再经消息事件把主线推进到
	// communicating(投影游标到 2):混合轮(文字+特殊卡同批)按设计转人工
	// 不迁移,真实迁移形态是 accepted 卡单独成轮。
	expression := "我想继续了解岗位"
	messages := appendCommunicationV4Inbound(t, s, fixture,
		Message{
			Seq: 2, Direction: "in", Kind: "text", ContentHash: "notify-freeze-2",
			Text: &expression, CreatedAt: time.Now().Add(-time.Second),
		},
		Message{
			Seq: 3, Direction: "in", Kind: "card", CardType: "interviewInvite",
			CardState: "accepted", ContentHash: "notify-freeze-3", CreatedAt: time.Now(),
		},
	)
	if _, err := s.ApplyCommunicationV4BusinessEvent(ApplyCommunicationV4BusinessEventRequest{
		ProfileID: fixture.ProfileID,
		Event: communication.BusinessEvent{
			Key: "message:2", Kind: communication.EventCandidateExpressionReceived,
			Source: communication.EventSourceMessage, MessageSeq: 2,
			ExpressionKind: communication.ExpressionText, Text: expression,
		},
		AppliedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	material, ready, err := s.CommunicationAIMaterialForProfile(fixture.ProfileID)
	if err != nil || !ready {
		t.Fatalf("AI 材料未就绪: %v", err)
	}
	setCommunicationV4FixedPhrasePackage(t, s, material.ContextRevision.RevisionHash)
	req := communicationV4TurnRequest(t, s, fixture, messages[1:])

	result, err := s.FreezeCommunicationV4Turn(req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Aggregate.State.MainStatus != communication.V4StatusInterviewed {
		t.Fatalf("inbound 轮未迁入 interviewed: %+v", result.Aggregate.State)
	}
	rows := outboxRows(t, s, NotificationTypeInterviewAccepted)
	if len(rows) != 1 || rows[0].ProfileID != fixture.ProfileID ||
		rows[0].EventKey != "interviewAccepted:"+fixture.ProfileID {
		t.Fatalf("inbound 轮冻结未入队约面通知: %+v", rows)
	}
	if _, err := s.FreezeCommunicationV4Turn(req); err != nil {
		t.Fatal(err)
	}
	if rows := outboxRows(t, s, NotificationTypeInterviewAccepted); len(rows) != 1 {
		t.Fatalf("turn 重放导致通知增生: %+v", rows)
	}
}

// 换微信成功入队:ContactAsset 创建的同事务入队;同一收编重放不增生。
func TestWechatAdoptionEnqueuesNotificationExactlyOnce(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 7, 28, 11, 0, 0, 0, time.UTC)
	fixture, _ := seedSuccessfulV4Greeting(t, s, "notify-wechat", "conversation-notify-wechat", at)

	request := WechatContactAssetRequest{
		ProfileID:         fixture.ProfileID,
		Platform:          fixture.Platform,
		AccountRef:        fixture.AccountRef,
		ConversationRef:   "conversation-notify-wechat",
		RequestSourceKey:  strings.Repeat("a", 64),
		ExchangeSourceKey: strings.Repeat("b", 64),
		PeerWechat:        "wx-demo-001",
		ObservedAtMs:      at.UnixMilli(),
		RecordedAt:        at,
	}
	asset, created, err := s.RecordObservedWechatContact(request)
	if err != nil || asset == nil || !created {
		t.Fatalf("首次收编失败: asset=%v created=%v err=%v", asset, created, err)
	}
	rows := outboxRows(t, s, NotificationTypeWechatAdded)
	if len(rows) != 1 || rows[0].EventKey != "wechatAdded:"+fixture.ProfileID {
		t.Fatalf("微信互加通知入队不符: %+v", rows)
	}

	_, created, err = s.RecordObservedWechatContact(request)
	if err != nil || created {
		t.Fatalf("同源收编重放应幂等: created=%v err=%v", created, err)
	}
	if rows := outboxRows(t, s, NotificationTypeWechatAdded); len(rows) != 1 {
		t.Fatalf("收编重放导致通知增生: %+v", rows)
	}
}

func enqueueForTest(t *testing.T, s *Store, notifyType, eventKey, profileID string, at time.Time) uint64 {
	t.Helper()
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		return enqueueNotificationTx(tx, notifyType, eventKey, profileID, at)
	}); err != nil {
		t.Fatal(err)
	}
	var row NotificationOutbox
	if err := s.db.First(&row, "event_key = ?", eventKey).Error; err != nil {
		t.Fatal(err)
	}
	return row.ID
}

// 通知姓名优先取 IM 会话展示名:身份根上的名字可能是推荐列表的脱敏形态
// (真机 2026-07-28:会话侧"胡卫华"、身份根"胡先生"),运营要靠真名对上人。
func TestNotificationSnapshotPrefersConversationDisplayName(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 7, 28, 21, 0, 0, 0, time.UTC)
	fixture, _ := seedSuccessfulV4Greeting(t, s, "notify-name", "conversation-notify-name", at)

	masked := "胡先生"
	if err := s.db.Model(&Candidate{}).
		Where("platform = ? AND platform_user_ref = ?", fixture.Platform, "person-notify-name").
		UpdateColumn("display_name", masked).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&Conversation{}).
		Where(
			"platform = ? AND account_ref = ? AND conversation_ref = ?",
			fixture.Platform, fixture.AccountRef, "conversation-notify-name",
		).
		UpdateColumn("peer_display_name", "胡卫华").Error; err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.NotificationRenderSnapshotForProfile(fixture.ProfileID)
	if err != nil || snapshot == nil || snapshot.DisplayName != "胡卫华" {
		t.Fatalf("未优先取会话真名: snapshot=%+v err=%v", snapshot, err)
	}

	// 会话侧为空时回落身份根,不至于渲染成"候选人"。
	if err := s.db.Model(&Conversation{}).
		Where(
			"platform = ? AND account_ref = ? AND conversation_ref = ?",
			fixture.Platform, fixture.AccountRef, "conversation-notify-name",
		).
		UpdateColumn("peer_display_name", "").Error; err != nil {
		t.Fatal(err)
	}
	snapshot, err = s.NotificationRenderSnapshotForProfile(fixture.ProfileID)
	if err != nil || snapshot == nil || snapshot.DisplayName != masked {
		t.Fatalf("会话名为空未回落身份根: snapshot=%+v err=%v", snapshot, err)
	}
}

// 发件箱生命周期:取件、失败重试上限、发送、跳过与过期都只标记不删除。
func TestNotificationOutboxLifecycle(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	id := enqueueForTest(t, s, NotificationTypeInterviewAccepted, "interviewAccepted:p1", "p1", at)

	pendingRows, err := s.TakePendingNotifications(10, 5)
	if err != nil || len(pendingRows) != 1 || pendingRows[0].ID != id {
		t.Fatalf("取件不符: rows=%+v err=%v", pendingRows, err)
	}
	for attempt := 0; attempt < 5; attempt++ {
		if err := s.MarkNotificationFailed(id, "webhook 超时", 5, at.Add(time.Duration(attempt)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if rows, _ := s.TakePendingNotifications(10, 5); len(rows) != 0 {
		t.Fatalf("重试耗尽后仍被取件: %+v", rows)
	}
	var failed NotificationOutbox
	if err := s.db.First(&failed, "id = ?", id).Error; err != nil ||
		failed.Status != NotificationStatusFailed || failed.Attempts != 5 || failed.LastError == "" {
		t.Fatalf("终败状态不符: %+v err=%v", failed, err)
	}

	sentID := enqueueForTest(t, s, NotificationTypeInterviewAccepted, "interviewAccepted:p2", "p2", at)
	if err := s.MarkNotificationSent(sentID, true, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	meeting, err := s.InterviewNotificationForProfile("p2")
	if err != nil || meeting == nil || meeting.Status != NotificationStatusSent ||
		!meeting.SentWithWechat || meeting.SentAt == nil {
		t.Fatalf("已发送行不符: %+v err=%v", meeting, err)
	}

	skipID := enqueueForTest(t, s, NotificationTypeWechatAdded, "wechatAdded:p2", "p2", at)
	if err := s.MarkNotificationSkipped(skipID, "superseded", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	staleID := enqueueForTest(t, s, NotificationTypeWechatAdded, "wechatAdded:p3", "p3", at)
	expired, err := s.ExpireStaleNotifications(7*24*time.Hour, at.Add(8*24*time.Hour))
	if err != nil || expired != 1 {
		t.Fatalf("过期条数不符: %d err=%v", expired, err)
	}
	var staleRow NotificationOutbox
	if err := s.db.First(&staleRow, "id = ?", staleID).Error; err != nil ||
		staleRow.Status != NotificationStatusExpired {
		t.Fatalf("过期状态不符: %+v err=%v", staleRow, err)
	}
	var total int64
	if err := s.db.Model(&NotificationOutbox{}).Count(&total).Error; err != nil || total != 4 {
		t.Fatalf("发件箱行数不符(不得物理删除): %d err=%v", total, err)
	}
}

// 取证派发标记与截图事实:每通知至多标记一次;截图行追加、读取取最新。
func TestNotificationCaptureMarkingAndScreenshots(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC)
	id := enqueueForTest(t, s, NotificationTypeWechatAdded, "wechatAdded:cap", "cap", at)

	needing, err := s.NotificationsNeedingCapture("cap")
	if err != nil || len(needing) != 1 {
		t.Fatalf("待取证查询不符: %+v err=%v", needing, err)
	}
	if err := s.MarkNotificationsAssetsRequested([]uint64{id}, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if needing, _ := s.NotificationsNeedingCapture("cap"); len(needing) != 0 {
		t.Fatalf("标记后仍报待取证: %+v", needing)
	}

	ref1 := "sha256:" + strings.Repeat("1", 64)
	ref2 := "sha256:" + strings.Repeat("2", 64)
	if err := s.SaveCandidateScreenshot("cap", CandidateScreenshotKindChat, ref1, 100, false, at.UnixMilli(), at); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCandidateScreenshot("cap", CandidateScreenshotKindChat, ref2, 200, true, at.UnixMilli()+1, at); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCandidateScreenshot("cap", CandidateScreenshotKindResume, ref1, 300, false, at.UnixMilli(), at); err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestCandidateScreenshots("cap")
	if err != nil || latest[CandidateScreenshotKindChat].BlobRef != ref2 ||
		latest[CandidateScreenshotKindChat].ByteSize != 200 ||
		latest[CandidateScreenshotKindResume].BlobRef != ref1 {
		t.Fatalf("最新截图读取不符: %+v err=%v", latest, err)
	}
	var count int64
	if err := s.db.Model(&CandidateScreenshot{}).Count(&count).Error; err != nil || count != 3 {
		t.Fatalf("截图行应追加保留: %d err=%v", count, err)
	}
}
