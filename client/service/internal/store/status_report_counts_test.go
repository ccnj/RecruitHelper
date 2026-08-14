package store

import (
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
)

func TestStatusReportCountsCountsTodayCapturesAcrossBatchesByPerson(t *testing.T) {
	s := openTest(t)
	now := time.Date(2026, 8, 6, 14, 0, 0, 0, time.Local)
	start := time.Date(2026, 8, 6, 0, 0, 0, 0, time.Local)
	end := start.AddDate(0, 0, 1)

	batchOne := "batch-status-1"
	batchTwo := "batch-status-2"
	// 同账号只允许一个未结束批次(ux_sourcing_batch_open),所以早的那批已收尾。
	// 这正是"今天开过好几批"的真实形状,也是当前批次口径答不了整天的原因。
	endedAt := now.Add(-90 * time.Minute)
	for _, batch := range []struct {
		id, jobID, title string
		status           SourcingBatchStatus
		ended            *time.Time
	}{
		{batchOne, "16", "平安健康保障顾问", SourcingBatchCompleted, &endedAt},
		{batchTwo, "17", "线下门店主管", SourcingBatchCollecting, nil},
	} {
		jobID, title := batch.jobID, batch.title
		if err := s.db.Create(&SourcingBatch{
			BatchID: batch.id, Platform: "zhilian", AccountRef: "status-account",
			ContextRevisionHash: "revision-status", BackendJobID: &jobID,
			PositionTitle: &title, TargetCount: 10,
			Status: batch.status, StartedAt: now.Add(-2 * time.Hour),
			EndedAt: batch.ended,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	// 同一个人被两个批次各采一次:对运营来说仍然是一个人。
	captures := []struct {
		runID, batchID, userRef string
		capturedAt              time.Time
	}{
		{"run-status-1", batchOne, "person-a", now.Add(-3 * time.Hour)},
		{"run-status-2", batchOne, "person-b", now.Add(-2 * time.Hour)},
		{"run-status-3", batchTwo, "person-a", now.Add(-time.Hour)},
		// 昨天采的不算今天。
		{"run-status-4", batchTwo, "person-c", start.Add(-2 * time.Hour)},
	}
	for _, capture := range captures {
		batchID := capture.batchID
		if err := s.db.Create(&SourcingCandidateRun{
			RunID: capture.runID, BatchID: &batchID,
			Platform: "zhilian", AccountRef: "status-account",
			ContextRevisionHash: "revision-status", PlatformUserRef: capture.userRef,
			PositionRef: "position-status", ContactState: "unestablished",
			SourceLogicalDispatchID: capture.runID + "-logical",
			ObservedAt:              capture.capturedAt.UnixMilli(),
			CapturedAt:              capture.capturedAt, SchemaVersion: 1,
			ContentHash: strings.Repeat("7", 64),
			ResumeJSON:  `{"basic":[],"expectations":[]}`,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	counts, err := s.StatusReportCounts("zhilian", "status-account", start, end)
	if err != nil {
		t.Fatal(err)
	}

	// person-a 被采两次,只算一个人;person-c 是昨天的,不计入。
	if counts.TodayCaptured != 2 {
		t.Fatalf("今日采集人数应按人去重为 2，实际 %d", counts.TodayCaptured)
	}
	if len(counts.TodayCapturedByJob) != 2 {
		t.Fatalf("应按职位分两组，实际 %+v", counts.TodayCapturedByJob)
	}
	byJob := map[string]int64{}
	for _, row := range counts.TodayCapturedByJob {
		byJob[row.Name] = row.Captured
	}
	if byJob["平安健康保障顾问"] != 2 || byJob["线下门店主管"] != 1 {
		t.Fatalf("职位维计数错: %+v", counts.TodayCapturedByJob)
	}
}

func TestStatusReportCountsCountsManualRequiredProfilesForAccount(t *testing.T) {
	s := openTest(t)
	start := time.Date(2026, 8, 6, 0, 0, 0, 0, time.Local)
	end := start.AddDate(0, 0, 1)

	rows := []struct {
		profileID  string
		accountRef string
		automation ProfileCommunicationAutomationStatus
	}{
		{"profile-manual-1", "status-account", ProfileCommunicationAutomationManualRequired},
		{"profile-manual-2", "status-account", ProfileCommunicationAutomationManualRequired},
		{"profile-active-1", "status-account", ProfileCommunicationAutomationActive},
		// 别的账号的转人工不算这台机器当前账号的账。
		{"profile-other-1", "other-account", ProfileCommunicationAutomationManualRequired},
	}
	for index, row := range rows {
		// 档案挂在人的平台身份根上,先建根再建档。
		if err := s.db.Create(&Candidate{
			Platform: "zhilian", PlatformUserRef: row.profileID + "-user",
			FirstSeenAt: start, LastSeenAt: start,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.db.Create(&CandidateProfile{
			ProfileID: row.profileID, Platform: "zhilian", AccountRef: row.accountRef,
			PlatformUserRef: row.profileID + "-user",
			MainStatus:      CandidateProfileCommunicating,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.db.Create(&CommunicationV4Aggregate{
			ProfileID:            row.profileID,
			RootGreetingIntentID: row.profileID + "-intent",
			StateSchemaVersion:   1,
			Revision:             uint64(index + 1),
			AutomationStatus:     row.automation,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	counts, err := s.StatusReportCounts("zhilian", "status-account", start, end)
	if err != nil {
		t.Fatal(err)
	}
	if counts.ManualRequiredProfiles != 2 {
		t.Fatalf("转人工人数应为 2，实际 %d", counts.ManualRequiredProfiles)
	}
}

// 拒绝按「最近一轮意向」口径(2026-08-14 甲方裁决):每人取最新已分类轮,
// rejected 才计入。挽留在途的算,拒后改口的移出;今日=末轮拒绝且当日判定。
func TestStatusReportCountsRejectedByLatestClassifiedTurn(t *testing.T) {
	s := openTest(t)
	start := time.Date(2026, 8, 6, 0, 0, 0, 0, time.Local)
	end := start.AddDate(0, 0, 1)
	today := start.Add(10 * time.Hour)
	yesterday := start.Add(-10 * time.Hour)

	profiles := []struct {
		id         string
		accountRef string
		turns      []struct {
			seq          int64
			label        string
			classifiedAt *time.Time
		}
	}{
		// 今天拒绝,末轮即拒绝:计入 Current 与 Today。
		{"profile-rej-today", "status-account", []struct {
			seq          int64
			label        string
			classifiedAt *time.Time
		}{{1, "rejected", &today}}},
		// 昨天拒绝后今天改口有意向:移出拒绝数。
		{"profile-rej-flipped", "status-account", []struct {
			seq          int64
			label        string
			classifiedAt *time.Time
		}{{1, "rejected", &yesterday}, {2, "interested", &today}}},
		// 昨天拒绝,之后没新轮:计入 Current,不计 Today。
		{"profile-rej-old", "status-account", []struct {
			seq          int64
			label        string
			classifiedAt *time.Time
		}{{1, "interested", &yesterday}, {2, "rejected", &yesterday}}},
		// 末轮还没分类完(collected 无标签):按上一轮(拒绝)算,接受分钟级滞后。
		{"profile-rej-pending", "status-account", []struct {
			seq          int64
			label        string
			classifiedAt *time.Time
		}{{1, "rejected", &today}, {2, "", nil}}},
		// 别的账号的拒绝不算这台机器当前账号的账。
		{"profile-rej-other", "other-account", []struct {
			seq          int64
			label        string
			classifiedAt *time.Time
		}{{1, "rejected", &today}}},
	}
	for _, profile := range profiles {
		if err := s.db.Create(&Candidate{
			Platform: "zhilian", PlatformUserRef: profile.id + "-user",
			FirstSeenAt: start, LastSeenAt: start,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.db.Create(&CandidateProfile{
			ProfileID: profile.id, Platform: "zhilian", AccountRef: profile.accountRef,
			PlatformUserRef: profile.id + "-user",
			MainStatus:      CandidateProfileCommunicating,
		}).Error; err != nil {
			t.Fatal(err)
		}
		for _, turn := range profile.turns {
			status := DialogueTurnClassified
			if turn.label == "" {
				status = DialogueTurnCollected
			}
			if err := s.db.Create(&DialogueTurn{
				TurnID:    profile.id + "-turn-" + string(rune('0'+turn.seq)),
				ProfileID: profile.id, ConversationRef: profile.id + "-conv",
				InputDigest:       strings.Repeat("d", 60) + string(rune('0'+turn.seq)),
				HistoryThroughSeq: turn.seq, InboundFromSeq: turn.seq, InboundThroughSeq: turn.seq,
				ContextRevisionHash: "revision-status", ResumeSnapshotID: "snapshot-status",
				RecommendedTimeText: "-", RenderFormatVersion: "v1",
				Status: status, IntentLabel: m5ai.IntentLabel(turn.label),
				ClassifiedAt: turn.classifiedAt,
			}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}

	counts, err := s.StatusReportCounts("zhilian", "status-account", start, end)
	if err != nil {
		t.Fatal(err)
	}
	// Current: rej-today + rej-old + rej-pending = 3(flipped 已改口、other 是别的账号)。
	if counts.CurrentRejected != 3 {
		t.Fatalf("当前拒绝人数应为 3，实际 %d", counts.CurrentRejected)
	}
	// Today: rej-today + rej-pending(末轮拒绝且今天判定)= 2。
	if counts.TodayRejected != 2 {
		t.Fatalf("今日拒绝人数应为 2，实际 %d", counts.TodayRejected)
	}
}

// 拉黑按归档原因数;今日数用 updated_at 锚(拉黑归档是终态,行不再被业务写)。
func TestStatusReportCountsBlacklistedByEndReason(t *testing.T) {
	s := openTest(t)
	start := time.Date(2026, 8, 6, 0, 0, 0, 0, time.Local)
	yesterday := start.Add(-10 * time.Hour)

	blacklisted := CandidateProfileEndBlacklisted
	rejected := CandidateProfileEndRejected
	rows := []struct {
		id        string
		endReason *CandidateProfileEndReason
		updatedAt *time.Time
	}{
		{"profile-blk-today", &blacklisted, nil},         // 今天拉黑(updated_at=现在)
		{"profile-blk-old", &blacklisted, &yesterday},    // 昨天拉黑
		{"profile-end-rejected", &rejected, nil},         // 归档拒绝不算拉黑
	}
	for _, row := range rows {
		if err := s.db.Create(&Candidate{
			Platform: "zhilian", PlatformUserRef: row.id + "-user",
			FirstSeenAt: start, LastSeenAt: start,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.db.Create(&CandidateProfile{
			ProfileID: row.id, Platform: "zhilian", AccountRef: "status-account",
			PlatformUserRef: row.id + "-user",
			MainStatus:      CandidateProfileEnded, EndReason: row.endReason,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if row.updatedAt != nil {
			// UpdateColumn 跳过 GORM 的自动时间戳,才能把 updated_at 钉在昨天。
			if err := s.db.Model(&CandidateProfile{}).
				Where("profile_id = ?", row.id).
				UpdateColumn("updated_at", *row.updatedAt).Error; err != nil {
				t.Fatal(err)
			}
		}
	}

	// 让"今天"的窗口覆盖 Create 落下的当前时刻:end 取明天以后,避免测试跑在
	// start 所在日之外时误红 —— 这里验证的是端到端 SQL 形态,不是墙钟。
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	counts, err := s.StatusReportCounts("zhilian", "status-account", dayStart, dayStart.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if counts.TotalBlacklisted != 2 {
		t.Fatalf("累计拉黑应为 2，实际 %d", counts.TotalBlacklisted)
	}
	if counts.TodayBlacklisted != 1 {
		t.Fatalf("今日拉黑应为 1，实际 %d", counts.TodayBlacklisted)
	}
}

// 零账号是全新安装的正常状态,不是错误:上报照常发,计数全零。
func TestStatusReportCountsReturnsZeroForUnboundAccount(t *testing.T) {
	s := openTest(t)
	start := time.Date(2026, 8, 6, 0, 0, 0, 0, time.Local)

	counts, err := s.StatusReportCounts("", "", start, start.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if counts.TodayCaptured != 0 || len(counts.TodayCapturedByJob) != 0 {
		t.Fatalf("零账号应返回全零: %+v", counts)
	}
}
