package store

import (
	"strings"
	"testing"
	"time"
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
