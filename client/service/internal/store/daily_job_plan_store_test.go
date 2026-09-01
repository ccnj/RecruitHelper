package store

import (
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/testfixture"
)

// planRevisionFixture 造一个 legacyJobConfig 来源、带候选人筛选文档的配置
// 版本。targetMin/targetMax 决定当日总量抽取区间。
func planRevisionFixture(jobID, jobName string, targetMin, targetMax int, at time.Time) m5ai.ContextRevision {
	replyPrompt := "合成回复:{简历}/{推荐时段}/{对话历史}/{话术_序列}"
	intentPrompt := "合成意向:{回复}/{招呼语}"
	selection := fmt.Sprintf(
		`{"minScore":5,"targetMin":%d,"targetMax":%d,"maleRatioLimit":50}`,
		targetMin, targetMax,
	)
	documents := []m5ai.JobConfigDocument{
		{DocType: "多轮沟通", Content: replyPrompt},
		{DocType: "客户事实库", Content: "fixture://facts-" + jobID},
		{DocType: "意向判断", Content: intentPrompt},
		{DocType: "候选人筛选", Content: selection},
		{DocType: "打分", Content: "score {resume_json}"},
		{DocType: "招呼语", Content: `{"prompt":"{career_state} {resume_summary_json}"}`},
		{DocType: "职位筛选", Content: testfixture.SourcingFiltersDocument},
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].DocType < documents[j].DocType })
	return m5ai.ContextRevision{
		ContextID: "ctx-" + jobID, RevisionHash: "rev-" + jobID,
		SourceKind: "legacyJobConfig", SourceJobRef: jobID,
		DisplayName: jobName, Environment: "online",
		SourcePackage: m5ai.JobConfigDocumentPackage{Documents: documents},
		Communication: m5ai.CommunicationView{
			ReplyPrompt: replyPrompt, IntentPrompt: intentPrompt,
			CustomerFacts: "fixture://facts-" + jobID, MappingVersion: m5ai.MappingVersion,
		},
		CreatedAt: at,
	}
}

func seedEffectiveJobs(t *testing.T, s *Store, at time.Time, revisions ...m5ai.ContextRevision) {
	t.Helper()
	if _, err := s.SaveEffectiveLegacyJobAIContexts(revisions, at); err != nil {
		t.Fatalf("SaveEffectiveLegacyJobAIContexts: %v", err)
	}
}

func planTestKey() AccountKey {
	return AccountKey{Platform: "zhilian", AccountRef: "a-test"}
}

func TestDailyPlanShareSplitsExactlyWithFrontLoadedRemainder(t *testing.T) {
	cases := []struct {
		total, jobs int
		want        []int
	}{
		{87, 5, []int{18, 18, 17, 17, 17}},
		{90, 5, []int{18, 18, 18, 18, 18}},
		{17, 1, []int{17}},
		{3, 5, []int{1, 1, 1, 0, 0}},
		{0, 5, []int{0, 0, 0, 0, 0}},
	}
	for _, tc := range cases {
		sum := 0
		for rank := 0; rank < tc.jobs; rank++ {
			share := DailyPlanShare(tc.total, tc.jobs, rank)
			if share != tc.want[rank] {
				t.Fatalf("share(%d,%d,%d)=%d want %d", tc.total, tc.jobs, rank, share, tc.want[rank])
			}
			sum += share
		}
		if tc.total > 0 && sum != tc.total {
			t.Fatalf("total=%d jobs=%d 份额之和=%d,必须恰等于总量", tc.total, tc.jobs, sum)
		}
	}
}

func TestPlanCaptureSizingCoefficients(t *testing.T) {
	// 甲方改定系数:首轮 ceil(1.5x)、步进 ceil(0.5x)、上限 3x。
	cases := []struct{ share, first, step, limit int }{
		{18, 27, 9, 54},
		{17, 26, 9, 51},
		{1, 2, 1, 3},
		{0, 0, 0, 0},
	}
	for _, tc := range cases {
		if got := PlanCaptureFirstRound(tc.share); got != tc.first {
			t.Fatalf("first(%d)=%d want %d", tc.share, got, tc.first)
		}
		if got := PlanCaptureStep(tc.share); got != tc.step {
			t.Fatalf("step(%d)=%d want %d", tc.share, got, tc.step)
		}
		if got := PlanCaptureLimit(tc.share); got != tc.limit {
			t.Fatalf("limit(%d)=%d want %d", tc.share, got, tc.limit)
		}
	}
}

func TestStableDailyPlanQuotaIsDeterministicAndInRange(t *testing.T) {
	first := StableDailyPlanQuota("djp-x", "2026-09-01", 80, 90)
	for i := 0; i < 5; i++ {
		if again := StableDailyPlanQuota("djp-x", "2026-09-01", 80, 90); again != first {
			t.Fatalf("同键重抽出现漂移: %d != %d", again, first)
		}
	}
	if first < 80 || first > 90 {
		t.Fatalf("抽取越界: %d", first)
	}
	if StableDailyPlanQuota("djp-x", "2026-09-01", 85, 85) != 85 {
		t.Fatal("min==max 必须原样返回")
	}
}

func TestCreateDailyJobPlanOrdersJobsNumericallyAndDrawsFromFirstEntry(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 9, 1, 7, 30, 0, 0, time.Local)
	// 注意乱序与两位数 ID:数值序应是 9 < 10 < 21,字符串序会把 10 排最前。
	seedEffectiveJobs(t, s, at,
		planRevisionFixture("21", "销售专员", 80, 90, at),
		planRevisionFixture("9", "客服专员", 84, 84, at),
		planRevisionFixture("10", "保险顾问", 80, 90, at),
	)
	result, err := s.CreateDailyJobPlan(planTestKey(), "2026-09-01", at)
	if err != nil {
		t.Fatalf("CreateDailyJobPlan: %v", err)
	}
	if got := len(result.Entries); got != 3 {
		t.Fatalf("条目数=%d", got)
	}
	order := []string{"9", "10", "21"}
	for index, entry := range result.Entries {
		if entry.BackendJobID != order[index] || entry.Seq != index+1 {
			t.Fatalf("计划序错误: %+v", result.Entries)
		}
		if entry.Status != DailyJobPlanEntryPending || entry.Quota != 0 {
			t.Fatalf("草稿条目应 pending 且零份额: %+v", entry)
		}
	}
	// 总量来自计划序第一职位(9 号,固定 84~84),而不是字符串序第一的 10 号。
	if result.Plan.TotalQuota != 84 || result.Plan.QuotaSourceJobID != "9" {
		t.Fatalf("总量来源错误: %+v", result.Plan)
	}
	if result.Plan.Status != DailyJobPlanDraft || result.Plan.JobCount != 0 {
		t.Fatalf("新计划应为 draft: %+v", result.Plan)
	}
}

func TestCreateDailyJobPlanSupersedesPriorActivePlan(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 9, 1, 7, 30, 0, 0, time.Local)
	seedEffectiveJobs(t, s, at, planRevisionFixture("7", "职位甲", 80, 90, at))
	first, err := s.CreateDailyJobPlan(planTestKey(), "2026-09-01", at)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := s.CreateDailyJobPlan(planTestKey(), "2026-09-02", at.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	var reloaded DailyJobPlan
	if err := s.db.First(&reloaded, "plan_id = ?", first.Plan.PlanID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != DailyJobPlanSuperseded {
		t.Fatalf("旧计划未被顶替: %+v", reloaded)
	}
	plan, entries, err := s.ActiveDailyJobPlan(planTestKey())
	if err != nil || plan == nil || plan.PlanID != second.Plan.PlanID || len(entries) != 1 {
		t.Fatalf("活跃计划应是新计划: plan=%+v err=%v", plan, err)
	}
}

func TestCreateDailyJobPlanWithoutJobsFails(t *testing.T) {
	s := openTest(t)
	if _, err := s.CreateDailyJobPlan(planTestKey(), "2026-09-01", time.Now()); !errors.Is(err, ErrDailyJobPlanNoJobs) {
		t.Fatalf("空有效集应报无职位: %v", err)
	}
}

func TestFinalizeDailyJobPlanFreezesSharesAndSkipsOffline(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 9, 1, 7, 30, 0, 0, time.Local)
	seedEffectiveJobs(t, s, at,
		planRevisionFixture("1", "职位一", 87, 87, at),
		planRevisionFixture("2", "职位二", 80, 90, at),
		planRevisionFixture("3", "职位三", 80, 90, at),
		planRevisionFixture("4", "职位四", 80, 90, at),
	)
	created, err := s.CreateDailyJobPlan(planTestKey(), "2026-09-01", at)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	finalized, err := s.FinalizeDailyJobPlan(created.Plan.PlanID, []DailyJobPlanGateObservation{
		{Seq: 1, Online: true, StatusLabel: "在线中"},
		{Seq: 2, Online: false, StatusLabel: "审核中"},
		{Seq: 3, Online: true, StatusLabel: "在线中"},
		{Seq: 4, Online: true, StatusLabel: "在线中"},
	}, at.Add(time.Minute))
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if finalized.Plan.Status != DailyJobPlanActive || finalized.Plan.JobCount != 3 {
		t.Fatalf("定稿后 N 应为 3: %+v", finalized.Plan)
	}
	// 87/3 = 29 整除,三份各 29。
	wantQuota := map[int]int{1: 29, 3: 29, 4: 29}
	for _, entry := range finalized.Entries {
		switch entry.Seq {
		case 2:
			if entry.Status != DailyJobPlanEntrySkipped ||
				entry.SkipReason != "jobNotOnlineAtPlan:审核中" {
				t.Fatalf("离线条目未跳过: %+v", entry)
			}
		default:
			if entry.Status != DailyJobPlanEntryPending || entry.Quota != wantQuota[entry.Seq] {
				t.Fatalf("份额错误: %+v", entry)
			}
		}
	}
	// 幂等:再次定稿返回同一事实,不重算。
	again, err := s.FinalizeDailyJobPlan(created.Plan.PlanID, nil, at.Add(2*time.Minute))
	if err != nil || again.Plan.JobCount != 3 {
		t.Fatalf("定稿重放失败: %+v err=%v", again, err)
	}
}

func TestFinalizeDailyJobPlanSkipsZeroQuotaEntries(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 9, 1, 7, 30, 0, 0, time.Local)
	seedEffectiveJobs(t, s, at,
		planRevisionFixture("1", "职位一", 2, 2, at),
		planRevisionFixture("2", "职位二", 80, 90, at),
		planRevisionFixture("3", "职位三", 80, 90, at),
	)
	created, err := s.CreateDailyJobPlan(planTestKey(), "2026-09-01", at)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	finalized, err := s.FinalizeDailyJobPlan(created.Plan.PlanID, []DailyJobPlanGateObservation{
		{Seq: 1, Online: true, StatusLabel: "在线中"},
		{Seq: 2, Online: true, StatusLabel: "在线中"},
		{Seq: 3, Online: true, StatusLabel: "在线中"},
	}, at.Add(time.Minute))
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	// 总量 2、职位 3:前两条各 1,第三条 0 → 跳过。
	if finalized.Entries[2].Status != DailyJobPlanEntrySkipped ||
		finalized.Entries[2].SkipReason != DailyJobPlanSkipZeroQuota {
		t.Fatalf("零份额条目未跳过: %+v", finalized.Entries[2])
	}
	if next := NextPendingDailyJobPlanEntry(finalized.Entries); next == nil || next.Seq != 1 || next.Quota != 1 {
		t.Fatalf("下一条目错误: %+v", next)
	}
}

func TestFinalizeDailyJobPlanRequiresFullObservationCoverage(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 9, 1, 7, 30, 0, 0, time.Local)
	seedEffectiveJobs(t, s, at,
		planRevisionFixture("1", "职位一", 80, 90, at),
		planRevisionFixture("2", "职位二", 80, 90, at),
	)
	created, err := s.CreateDailyJobPlan(planTestKey(), "2026-09-01", at)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.FinalizeDailyJobPlan(created.Plan.PlanID, []DailyJobPlanGateObservation{
		{Seq: 1, Online: true, StatusLabel: "在线中"},
	}, at.Add(time.Minute)); !errors.Is(err, ErrDailyJobPlanStateConflict) {
		t.Fatalf("覆盖不全应冲突: %v", err)
	}
}

func TestDailyJobPlanShareForEntryDraftIsProvisionalAndNeverExceedsFinal(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 9, 1, 7, 30, 0, 0, time.Local)
	seedEffectiveJobs(t, s, at,
		planRevisionFixture("1", "职位一", 85, 85, at),
		planRevisionFixture("2", "职位二", 80, 90, at),
		planRevisionFixture("3", "职位三", 80, 90, at),
		planRevisionFixture("4", "职位四", 80, 90, at),
		planRevisionFixture("5", "职位五", 80, 90, at),
	)
	created, err := s.CreateDailyJobPlan(planTestKey(), "2026-09-01", at)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	draftShare := DailyJobPlanShareForEntry(&created.Plan, created.Entries, created.Entries[0].EntryID)
	if draftShare != 17 {
		t.Fatalf("草稿临时份额应为 85/5=17: %d", draftShare)
	}
	// 定稿时两个职位掉线:N=3,份额上浮,草稿值不得超过定稿值。
	finalized, err := s.FinalizeDailyJobPlan(created.Plan.PlanID, []DailyJobPlanGateObservation{
		{Seq: 1, Online: true, StatusLabel: "在线中"},
		{Seq: 2, Online: false, StatusLabel: "平台未见"},
		{Seq: 3, Online: true, StatusLabel: "在线中"},
		{Seq: 4, Online: false, StatusLabel: "平台未见"},
		{Seq: 5, Online: true, StatusLabel: "在线中"},
	}, at.Add(time.Minute))
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	finalShare := DailyJobPlanShareForEntry(&finalized.Plan, finalized.Entries, created.Entries[0].EntryID)
	if finalShare != 29 {
		t.Fatalf("定稿份额应为 85/3 前置=29: %d", finalShare)
	}
	if draftShare > finalShare {
		t.Fatalf("临时份额 %d 不得超过定稿份额 %d(方向必须少采)", draftShare, finalShare)
	}
}

func TestDailyJobPlanEntryTransitions(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 9, 1, 7, 30, 0, 0, time.Local)
	seedEffectiveJobs(t, s, at,
		planRevisionFixture("1", "职位一", 80, 90, at),
		planRevisionFixture("2", "职位二", 80, 90, at),
	)
	created, err := s.CreateDailyJobPlan(planTestKey(), "2026-09-01", at)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	planID := created.Plan.PlanID
	if err := s.SkipDailyJobPlanEntry(planID, 1, "jobNotOnline", at); err != nil {
		t.Fatalf("skip: %v", err)
	}
	if err := s.SkipDailyJobPlanEntry(planID, 1, "jobNotOnline", at); err != nil {
		t.Fatalf("skip 应幂等: %v", err)
	}
	if err := s.MarkDailyJobPlanEntryDone(planID, 1, "sb-x", at); !errors.Is(err, ErrDailyJobPlanStateConflict) {
		t.Fatalf("skipped→done 应冲突: %v", err)
	}
	if err := s.MarkDailyJobPlanEntryDone(planID, 2, "sb-y", at); err != nil {
		t.Fatalf("done: %v", err)
	}
	if err := s.MarkDailyJobPlanEntryDone(planID, 2, "sb-y", at); err != nil {
		t.Fatalf("done 应幂等: %v", err)
	}
	_, entries, err := s.ActiveDailyJobPlan(planTestKey())
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Status != DailyJobPlanEntrySkipped || entries[1].Status != DailyJobPlanEntryDone ||
		entries[1].BatchID != "sb-y" {
		t.Fatalf("条目状态错误: %+v", entries)
	}
	if NextPendingDailyJobPlanEntry(entries) != nil {
		t.Fatal("不应再有 pending 条目")
	}
}

func TestDailyJobPlanEndStates(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 9, 1, 7, 30, 0, 0, time.Local)
	seedEffectiveJobs(t, s, at, planRevisionFixture("1", "职位一", 80, 90, at))
	created, err := s.CreateDailyJobPlan(planTestKey(), "2026-09-01", at)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.AbortDailyJobPlan(created.Plan.PlanID, "chainInterrupted", at); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if err := s.AbortDailyJobPlan(created.Plan.PlanID, "chainInterrupted", at); err != nil {
		t.Fatalf("同终态应幂等: %v", err)
	}
	if err := s.CompleteDailyJobPlan(created.Plan.PlanID, at); !errors.Is(err, ErrDailyJobPlanStateConflict) {
		t.Fatalf("终态互改应冲突: %v", err)
	}
	plan, entries, err := s.ActiveDailyJobPlan(planTestKey())
	if err != nil || plan != nil || entries != nil {
		t.Fatalf("终局后不应再有活跃计划: %+v %v", plan, err)
	}
}
