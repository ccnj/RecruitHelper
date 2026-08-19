package store

import (
	"strings"
	"testing"
	"time"
)

func TestEnqueueDailyReportIdempotent(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 8, 18, 8, 33, 0, 0, time.Local)

	inserted, err := s.EnqueueDailyReport("2026-08-18", at)
	if err != nil || !inserted {
		t.Fatalf("首次入队应插入: inserted=%v err=%v", inserted, err)
	}
	inserted, err = s.EnqueueDailyReport("2026-08-18", at.Add(30*time.Second))
	if err != nil || inserted {
		t.Fatalf("同日重复入队应被唯一索引吸收: inserted=%v err=%v", inserted, err)
	}
	inserted, err = s.EnqueueDailyReport("2026-08-19", at.AddDate(0, 0, 1))
	if err != nil || !inserted {
		t.Fatalf("次日入队应插入新行: inserted=%v err=%v", inserted, err)
	}

	var rows []NotificationOutbox
	if err := s.db.Where("notify_type = ?", NotificationTypeDailyReport).
		Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("应恰有两行日报: %+v", rows)
	}
	first := rows[0]
	if first.EventKey != "dailyReport:2026-08-18" || first.Status != NotificationStatusPending ||
		first.ProfileID != "" {
		t.Fatalf("日报行形态不对: %+v", first)
	}
	if got := DailyReportLocalDate(first.PayloadJSON); got != "2026-08-18" {
		t.Fatalf("载荷日期解析应为 2026-08-18: %q (payload=%s)", got, first.PayloadJSON)
	}
	if got := DailyReportLocalDate("{}"); got != "" {
		t.Fatalf("空载荷应解出空串按陈旧处理: %q", got)
	}

	if _, err := s.EnqueueDailyReport("  ", at); err == nil {
		t.Fatal("空日期入队应报错")
	}
}

func TestDailyReportCountsYesterdayWindow(t *testing.T) {
	s := openTest(t)
	platform, accountRef := "zhilian", "daily-counts-account"
	createM4Account(t, s, platform, accountRef)
	todayStart := time.Date(2026, 8, 18, 0, 0, 0, 0, time.Local)
	yesterdayStart := todayStart.AddDate(0, 0, -1)

	asset := func(assetID, profileID string, at time.Time) {
		t.Helper()
		if err := s.db.Create(&ContactAsset{
			AssetID: assetID, ProfileID: profileID, Platform: platform,
			AccountRef: accountRef, ConversationRef: "conv-" + assetID,
			Kind: contactAssetKindWechat, SourceKey: assetID + "-source",
			RequestSourceKey: assetID + "-request", Value: "candidate_wechat",
			// 照抄现网落盘形态:created_at 是 UTC,切窗只认 observed_at_ms。
			ObservedAtMs: at.UnixMilli(), CreatedAt: at.UTC(), UpdatedAt: at.UTC(),
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	profile := func(profileID string, interviewedAt *time.Time) {
		t.Helper()
		displayName := "候选人" + profileID
		if err := s.db.Create(&Candidate{
			Platform: platform, PlatformUserRef: "U-" + profileID, DisplayName: &displayName,
			FirstSeenAt: yesterdayStart, LastSeenAt: todayStart,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.db.Create(&CandidateProfile{
			ProfileID: profileID, Platform: platform, AccountRef: accountRef,
			PlatformUserRef: "U-" + profileID, PositionRef: "position-" + profileID,
			MainStatus: CandidateProfileInterviewed, InterviewedAt: interviewedAt,
			ResumeCaptureState: ResumeCaptureUnattempted,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	// 昨日换微信:P1 两个会话各收编一次只算一人,P2 一次;昨日本地 00:30(UTC 前一天)
	// 必须算进昨日;今天凌晨收编的不算。
	asset("a1", "P-1", yesterdayStart.Add(10*time.Hour))
	asset("a2", "P-1", yesterdayStart.Add(11*time.Hour))
	asset("a3", "P-2", yesterdayStart.Add(30*time.Minute))
	asset("a4", "P-3", todayStart.Add(30*time.Minute))
	// 昨日约面:P-4 昨天接受;P-5 前天接受不算;P-6 无 interviewed_at 不算。
	profile("P-4", appPtrTime(yesterdayStart.Add(15*time.Hour)))
	profile("P-5", appPtrTime(yesterdayStart.Add(-3*time.Hour)))
	profile("P-6", nil)

	counts, err := s.DailyReportCounts(platform, accountRef, yesterdayStart, todayStart)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Wechat != 2 || counts.Appointments != 1 {
		t.Fatalf("昨日计数应为 换微信2/约面1: %+v", counts)
	}
}

func TestDailyReportInterviewsRoster(t *testing.T) {
	s := openTest(t)
	now := time.Date(2026, 8, 18, 9, 0, 0, 0, time.Local)
	platform, accountRef := "zhilian", "daily-roster-account"
	createM4Account(t, s, platform, accountRef)
	todayStart := time.Date(2026, 8, 18, 0, 0, 0, 0, time.Local)

	seed := func(profileID, displayName, jobTitle string, status CandidateProfileStatus) {
		t.Helper()
		userRef, conversationRef := "U-"+profileID, "C-"+profileID
		if err := s.db.Create(&Candidate{
			Platform: platform, PlatformUserRef: userRef, DisplayName: &displayName,
			FirstSeenAt: now.Add(-48 * time.Hour), LastSeenAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.db.Create(&CandidateProfile{
			ProfileID: profileID, Platform: platform, AccountRef: accountRef,
			PlatformUserRef: userRef, PositionRef: "position-" + profileID,
			PositionTitle: &jobTitle, MainStatus: status,
			ConversationRef: &conversationRef, ResumeCaptureState: ResumeCaptureUnattempted,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	card := func(profileID string, seq int64, starts time.Time, method string, hashSeed string) {
		t.Helper()
		startsMs := starts.UnixMilli()
		var methodPtr *string
		if method != "" {
			methodPtr = &method
		}
		if err := s.db.Create(&Message{
			Platform: platform, AccountRef: accountRef, ConversationRef: "C-" + profileID,
			Seq: seq, Direction: "out", Kind: "card", CardType: "interviewInvite",
			CardState: "accepted", ContentHash: strings.Repeat(hashSeed, 64),
			InterviewStartsAtMs: &startsMs, InterviewMethod: methodPtr, Origin: "self",
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	// 今天下午的面试:在列。
	seed("P-today", "张三", "保障顾问", CandidateProfileInterviewed)
	card("P-today", 1, todayStart.Add(14*time.Hour), "wechatVideo", "a")
	// 后天的面试:无上界,也在列。
	seed("P-later", "李四", "销售专员", CandidateProfileInterviewed)
	card("P-later", 1, todayStart.AddDate(0, 0, 2).Add(10*time.Hour), "onsite", "b")
	// 昨天的面试:过期不列。
	seed("P-past", "王五", "销售专员", CandidateProfileInterviewed)
	card("P-past", 1, todayStart.Add(-10*time.Hour), "onsite", "c")
	// 发了卡但候选人未接受(已邀面):不列——名单口径是已约面。
	seed("P-invited", "赵六", "销售专员", CandidateProfileInvited)
	card("P-invited", 1, todayStart.Add(16*time.Hour), "wechatVideo", "d")
	// 改期:旧卡在今天、最新卡已挪到昨天——只认最新卡,整人不列。
	seed("P-moved", "钱七", "销售专员", CandidateProfileInterviewed)
	card("P-moved", 1, todayStart.Add(15*time.Hour), "wechatVideo", "e")
	card("P-moved", 2, todayStart.Add(-5*time.Hour), "wechatVideo", "f")

	rows, err := s.DailyReportInterviews(platform, accountRef, todayStart.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("名单应恰有两人(今天+后天): %+v", rows)
	}
	first, second := rows[0], rows[1]
	if first.DisplayName != "张三" || first.PositionTitle != "保障顾问" ||
		first.Method != "wechatVideo" || first.StartsAtMs != todayStart.Add(14*time.Hour).UnixMilli() {
		t.Fatalf("首行应为张三今天 14:00: %+v", first)
	}
	if second.DisplayName != "李四" || second.Method != "onsite" {
		t.Fatalf("次行应为李四后天线下: %+v", second)
	}
}
