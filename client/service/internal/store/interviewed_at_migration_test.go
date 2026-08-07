package store

import (
	"strings"
	"testing"
	"time"
)

// 补记只在旧库首次引入 interviewed_at 列时发生。列已存在之后,已约面档案的空
// 值只能是写入点漏写,属于损坏——若每次启动都按 IS NULL 补记,就会把那种 bug
// 静默盖成一个看起来合理的时刻。
func TestBackfillInterviewedAtRunsOnlyOnColumnIntroduction(t *testing.T) {
	s := openTest(t)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.Local)
	platform, accountRef := "zhilian", "backfill-account"
	createM4Account(t, s, platform, accountRef)

	seed := func(profileID string, status CandidateProfileStatus) {
		t.Helper()
		userRef := "U-" + profileID
		displayName := "候选人" + profileID
		if err := s.db.Create(&Candidate{
			Platform: platform, PlatformUserRef: userRef, DisplayName: &displayName,
			FirstSeenAt: now.Add(-72 * time.Hour), LastSeenAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.db.Create(&CandidateProfile{
			ProfileID: profileID, Platform: platform, AccountRef: accountRef,
			PlatformUserRef: userRef, PositionRef: "position-" + profileID,
			MainStatus: status, ResumeCaptureState: ResumeCaptureUnattempted,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	seed("P-old-interviewed", CandidateProfileInterviewed)
	seed("P-old-communicating", CandidateProfileCommunicating)

	// 旧库首次引入列:只补已约面档案。
	affected, err := backfillInterviewedAt(s.db, false)
	if err != nil {
		t.Fatal(err)
	}
	if affected != 1 {
		t.Fatalf("补记应只覆盖已约面档案，实得 %d 行", affected)
	}
	var interviewed CandidateProfile
	if err := s.db.First(&interviewed, "profile_id = ?", "P-old-interviewed").Error; err != nil {
		t.Fatal(err)
	}
	if interviewed.InterviewedAt == nil ||
		!interviewed.InterviewedAt.Equal(interviewedAtBackfillMoment) {
		t.Fatalf("已约面档案未按裁决时刻补记: %+v", interviewed.InterviewedAt)
	}
	var communicating CandidateProfile
	if err := s.db.First(&communicating, "profile_id = ?", "P-old-communicating").Error; err != nil {
		t.Fatal(err)
	}
	if communicating.InterviewedAt != nil {
		t.Fatalf("非已约面档案不得被补记: %+v", communicating.InterviewedAt)
	}

	// 补记时刻必须已经过去,否则会虚增补记当天的"今日新约面"。
	if !interviewedAtBackfillMoment.Before(now) {
		t.Fatalf("补记时刻必须早于运行时刻: %v", interviewedAtBackfillMoment)
	}

	// 列已存在:一个新出现的空值不得被补记掩盖。
	seed("P-new-interviewed", CandidateProfileInterviewed)
	affected, err = backfillInterviewedAt(s.db, true)
	if err != nil {
		t.Fatal(err)
	}
	if affected != 0 {
		t.Fatalf("列已存在时不得再补记，实得 %d 行", affected)
	}
	var fresh CandidateProfile
	if err := s.db.First(&fresh, "profile_id = ?", "P-new-interviewed").Error; err != nil {
		t.Fatal(err)
	}
	if fresh.InterviewedAt != nil {
		t.Fatalf("写入点漏写不得被补记掩盖: %+v", fresh.InterviewedAt)
	}
}

// 写入点必须真的写:补记只在旧库引入列时跑一次,此后全靠状态推进落这个事实。
// 走真实事件路径(文字 → 邀面卡 → 卡片接受)推进到已约面,断言时刻已落库。
func TestInterviewedAtWrittenWhenProfileEntersInterviewed(t *testing.T) {
	s := openTest(t)
	fixture := seedCommunicationV4InterviewedWechatAcceptEffect(t, s, "interviewed-at")

	var profile CandidateProfile
	if err := s.db.First(&profile, "profile_id = ?", fixture.ProfileID).Error; err != nil {
		t.Fatal(err)
	}
	if profile.MainStatus != CandidateProfileInterviewed {
		t.Fatalf("前置未推进到已约面: %s", profile.MainStatus)
	}
	if profile.InterviewedAt == nil || profile.InterviewedAt.IsZero() {
		t.Fatal("进入已约面时必须落下 interviewed_at,否则今日新约面永远数不到人")
	}
	first := *profile.InterviewedAt

	// 只写一次:规格 §45 允许归档后点旧卡再次生效,重复推进不得覆盖首次时刻,
	// 否则同一人会反复计入"今日新约面"。
	if err := s.db.Model(&CandidateProfile{}).
		Where("profile_id = ?", fixture.ProfileID).
		Update("main_status", CandidateProfileEnded).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&CandidateProfile{}).
		Where("profile_id = ? AND interviewed_at IS NULL", fixture.ProfileID).
		Update("interviewed_at", first.Add(48*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.First(&profile, "profile_id = ?", fixture.ProfileID).Error; err != nil {
		t.Fatal(err)
	}
	if !profile.InterviewedAt.Equal(first) {
		t.Fatalf("首次已约面时刻被覆盖: %v -> %v", first, *profile.InterviewedAt)
	}
}

// 今日新约面数的是"今天有几个人接受了面试邀约",取 interviewed_at;它此前直接
// 复制"今天发出邀面卡的人数",两个不同标签共用一个数字。
func TestTodayNewAppointmentsCountsInterviewedAtNotInviteCards(t *testing.T) {
	s := openTest(t)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.Local)
	platform, accountRef := "zhilian", "appointment-account"
	createM4Account(t, s, platform, accountRef)

	seed := func(profileID string, interviewedAt *time.Time) {
		t.Helper()
		userRef := "U-" + profileID
		displayName := "候选人" + profileID
		if err := s.db.Create(&Candidate{
			Platform: platform, PlatformUserRef: userRef, DisplayName: &displayName,
			FirstSeenAt: now.Add(-72 * time.Hour), LastSeenAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.db.Create(&CandidateProfile{
			ProfileID: profileID, Platform: platform, AccountRef: accountRef,
			PlatformUserRef: userRef, PositionRef: "position-" + profileID,
			MainStatus: CandidateProfileInterviewed, InterviewedAt: interviewedAt,
			ResumeCaptureState: ResumeCaptureUnattempted,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	seed("P-today", appPtrTime(now.Add(-2*time.Hour)))
	seed("P-yesterday", appPtrTime(now.Add(-26*time.Hour)))
	seed("P-backfilled", appPtrTime(interviewedAtBackfillMoment))

	got, err := s.AppOverview(AppOverviewRequest{
		Now: now, Platform: platform, AccountRef: accountRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	appointments := got.Statistics.TodayNewAppointments
	if !appointments.Exact || appointments.Value == nil || *appointments.Value != 1 {
		t.Fatalf("今日新约面应为精确 1(只有今天接受的那位)，实得 %+v", appointments)
	}
	// 今日已邀面数的是今天发出的邀面卡，本库一张卡都没有，必须是 0——它和
	// 今日新约面不再是同一个数字。
	invited := got.Statistics.TodayInvited
	if !invited.Exact || invited.Value == nil || *invited.Value != 0 {
		t.Fatalf("今日已邀面与今日新约面不得共用一个数字: %+v", invited)
	}
}

// 已约面页要在候选人行上直接显示"这个人什么时候答应的"，所以 interviewed_at
// 必须投影进列表项。它是 time.Time 列而列表里其余时间都是毫秒，换算若没接上
// 只会静默变成"—"，页面看不出是没约成还是没读出来。
func TestAppCandidatesProjectsAppointedAt(t *testing.T) {
	s := openTest(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.Local)
	platform, accountRef := "zhilian", "appointed-at-account"
	createM4Account(t, s, platform, accountRef)

	appointedAt := now.Add(-26 * time.Hour)
	userRef, profileID, conversationRef := "U-appointed", "P-appointed", "C-appointed"
	displayName := "候选人 A"
	if err := s.db.Create(&Candidate{
		Platform: platform, PlatformUserRef: userRef, DisplayName: &displayName,
		FirstSeenAt: now.Add(-72 * time.Hour), LastSeenAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&CandidateProfile{
		ProfileID: profileID, Platform: platform, AccountRef: accountRef,
		PlatformUserRef: userRef, PositionRef: "position-appointed",
		MainStatus: CandidateProfileInterviewed, InterviewedAt: appPtrTime(appointedAt),
		ConversationRef: &conversationRef, ResumeCaptureState: ResumeCaptureUnattempted,
	}).Error; err != nil {
		t.Fatal(err)
	}
	// 面试时间还没到，这一位才会落在已约面视图里。
	startsMs := now.Add(20 * time.Hour).UnixMilli()
	if err := s.db.Create(&Message{
		Platform: platform, AccountRef: accountRef, ConversationRef: conversationRef,
		Seq: 1, Direction: "out", Kind: "card", CardType: "interviewInvite",
		CardState: "accepted", ContentHash: strings.Repeat("a", 64),
		InterviewStartsAtMs: &startsMs, Origin: "self",
	}).Error; err != nil {
		t.Fatal(err)
	}

	list, err := s.AppCandidates(AppCandidateListQuery{
		Platform: platform, AccountRef: accountRef,
		View: AppCandidateViewInterviewed, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("已约面视图应有 1 位候选人，实得 %d", len(list.Items))
	}
	got := list.Items[0].AppointedAtMs
	if got == nil {
		t.Fatal("约面时刻没有投影出去，页面上会永远显示为空")
	}
	if *got != appointedAt.UnixMilli() {
		t.Fatalf("约面时刻不是接受邀约那一刻: 期望 %d，实得 %d", appointedAt.UnixMilli(), *got)
	}
	// 面试时刻是另一件事，两者不得互相顶替。
	if list.Items[0].InterviewStartsAtMs == nil || *list.Items[0].InterviewStartsAtMs != startsMs {
		t.Fatalf("面试开始时刻被约面时刻污染: %+v", list.Items[0].InterviewStartsAtMs)
	}
}
