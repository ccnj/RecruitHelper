package store

import (
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
)

func saveInboundLegacyJob(
	t *testing.T,
	s *Store,
	jobID string,
	displayName string,
	at time.Time,
) {
	t.Helper()
	revision := contextRevisionFixture(
		"inbound-context-"+jobID,
		"inbound-revision-"+jobID,
		at,
	)
	revision.SourceKind = legacyJobConfigSourceKind
	revision.SourceJobRef = jobID
	revision.DisplayName = displayName
	if _, err := s.SaveCurrentLegacyJobAIContext(
		[]m5ai.ContextRevision{revision},
		at,
	); err != nil {
		t.Fatalf("保存职位 %s: %v", jobID, err)
	}
}

func seedInboundConversation(
	t *testing.T,
	s *Store,
	platform string,
	accountRef string,
	conversationRef string,
	platformUserRef string,
	displayName string,
	at time.Time,
) {
	t.Helper()
	if err := s.CreateAccount(&Account{
		Platform:   platform,
		AccountRef: accountRef,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveConversationList(SaveConversationListRequest{
		Platform:   platform,
		AccountRef: accountRef,
		ObservedAt: at,
		Complete:   true,
		Entries: []ListIndexEntry{{
			ConversationRef: conversationRef,
			PlatformUserRef: platformUserRef,
			PeerDisplayName: displayName,
		}},
	}); err != nil {
		t.Fatal(err)
	}
}

func inboundAdoptionRequest(at time.Time) AdoptInboundConversationProfileRequest {
	return AdoptInboundConversationProfileRequest{
		Platform:        "zhilian",
		AccountRef:      "account-inbound",
		ConversationRef: "conversation-inbound",
		PlatformUserRef: "user-inbound",
		DisplayName:     " 候选人  甲 ",
		PositionTitle:   " 客户\t经理 ",
		ObservedAt:      at,
	}
}

func TestAdoptInboundConversationProfileUsesUniqueCurrentLegacyJobAtomically(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 7, 26, 8, 30, 0, 0, time.UTC)
	req := inboundAdoptionRequest(at)
	seedInboundConversation(
		t,
		s,
		req.Platform,
		req.AccountRef,
		req.ConversationRef,
		req.PlatformUserRef,
		"候选人甲",
		at,
	)
	saveInboundLegacyJob(t, s, "job-42", "客户 经理", at.Add(-time.Minute))

	result, err := s.AdoptInboundConversationProfile(req)
	if err != nil || result == nil ||
		result.Outcome != InboundProfileAdopted ||
		result.Profile == nil {
		t.Fatalf("唯一职位应原子收编: result=%+v err=%v", result, err)
	}
	profile := result.Profile
	if profile.ProfileID == "" ||
		profile.Platform != req.Platform ||
		profile.AccountRef != req.AccountRef ||
		profile.PlatformUserRef != req.PlatformUserRef ||
		profile.PositionRef != "job-42" ||
		profile.PositionTitle == nil ||
		*profile.PositionTitle != "客户 经理" ||
		profile.BackendJobID == nil ||
		*profile.BackendJobID != "job-42" ||
		profile.ConversationRef == nil ||
		*profile.ConversationRef != req.ConversationRef ||
		profile.MainStatus != CandidateProfileSelected ||
		profile.ResumeCaptureState != ResumeCaptureUnattempted {
		t.Fatalf("主动来聊档案绑定错误: %+v", profile)
	}
	candidate, err := s.CandidateByKey(CandidateKey{
		Platform:        req.Platform,
		PlatformUserRef: req.PlatformUserRef,
	})
	if err != nil || candidate == nil ||
		candidate.DisplayName == nil ||
		*candidate.DisplayName != "候选人 甲" {
		t.Fatalf("候选人快照未同事务写入: candidate=%+v err=%v", candidate, err)
	}
	key := ConversationKey{
		Platform:        req.Platform,
		AccountRef:      req.AccountRef,
		ConversationRef: req.ConversationRef,
	}
	conversation, err := s.ConversationByKey(key)
	if err != nil || conversation == nil ||
		conversation.TrackingState != TrackingPending ||
		conversation.LastMessageSeq != 0 ||
		conversation.AdoptedBoundarySeq != 0 {
		t.Fatalf("会话未进入 pending 收编: conversation=%+v err=%v", conversation, err)
	}
	tracked, err := s.TrackedIntentByConversation(key)
	if err != nil || tracked == nil ||
		tracked.Status != TrackingPending ||
		tracked.RequestedBy != inboundProfileRequestedBy {
		t.Fatalf("tracked intent 未同事务写入: tracked=%+v err=%v", tracked, err)
	}

	replayed, err := s.AdoptInboundConversationProfile(req)
	if err != nil || replayed == nil ||
		replayed.Outcome != InboundProfileAlreadyAdopted ||
		replayed.Profile == nil ||
		replayed.Profile.ProfileID != profile.ProfileID {
		t.Fatalf("相同事实重放不幂等: result=%+v err=%v", replayed, err)
	}
	var profileN, trackedN int64
	if err := s.db.Model(&CandidateProfile{}).Count(&profileN).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&TrackedIntent{}).Count(&trackedN).Error; err != nil {
		t.Fatal(err)
	}
	if profileN != 1 || trackedN != 1 {
		t.Fatalf("重放产生重复事实: profiles=%d tracked=%d", profileN, trackedN)
	}
}

func TestAdoptInboundConversationProfileConservativelySkipsMissingOrAmbiguousJob(t *testing.T) {
	at := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	for _, fixture := range []struct {
		name string
		jobs []struct {
			id    string
			title string
		}
		want InboundProfileAdoptionOutcome
	}{
		{
			name: "no match",
			jobs: []struct {
				id    string
				title string
			}{{id: "job-other", title: "其他职位"}},
			want: InboundProfilePositionNoMatch,
		},
		{
			name: "ambiguous",
			jobs: []struct {
				id    string
				title string
			}{
				{id: "job-first", title: "客户经理"},
				{id: "job-second", title: " 客户经理 "},
			},
			want: InboundProfilePositionAmbiguous,
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			s := openTest(t)
			req := inboundAdoptionRequest(at)
			req.PositionTitle = "客户经理"
			seedInboundConversation(
				t,
				s,
				req.Platform,
				req.AccountRef,
				req.ConversationRef,
				req.PlatformUserRef,
				"候选人甲",
				at,
			)
			for _, job := range fixture.jobs {
				saveInboundLegacyJob(t, s, job.id, job.title, at.Add(-time.Minute))
			}

			result, err := s.AdoptInboundConversationProfile(req)
			if err != nil || result == nil ||
				result.Outcome != fixture.want ||
				result.Profile != nil {
				t.Fatalf("保守结果错误: result=%+v err=%v", result, err)
			}
			assertInboundAdoptionLeftNoFacts(t, s, req)
		})
	}
}

func TestAdoptInboundConversationProfileOnlyMatchesMostRecentlySyncedJob(t *testing.T) {
	at := time.Date(2026, 7, 26, 9, 15, 0, 0, time.UTC)

	t.Run("old customer matching title cannot win", func(t *testing.T) {
		s := openTest(t)
		req := inboundAdoptionRequest(at)
		req.PositionTitle = "客户经理"
		seedInboundConversation(
			t,
			s,
			req.Platform,
			req.AccountRef,
			req.ConversationRef,
			req.PlatformUserRef,
			"候选人甲",
			at,
		)
		saveInboundLegacyJob(t, s, "old-customer-job", "客户经理", at.Add(-time.Hour))
		saveInboundLegacyJob(t, s, "current-customer-job", "销售经理", at.Add(-time.Minute))

		result, err := s.AdoptInboundConversationProfile(req)
		if err != nil || result == nil ||
			result.Outcome != InboundProfilePositionNoMatch ||
			result.Profile != nil {
			t.Fatalf("旧客户职位不得参与当前匹配: result=%+v err=%v", result, err)
		}
		assertInboundAdoptionLeftNoFacts(t, s, req)
	})

	t.Run("same title binds current customer job", func(t *testing.T) {
		s := openTest(t)
		req := inboundAdoptionRequest(at)
		req.PositionTitle = "客户经理"
		seedInboundConversation(
			t,
			s,
			req.Platform,
			req.AccountRef,
			req.ConversationRef,
			req.PlatformUserRef,
			"候选人甲",
			at,
		)
		saveInboundLegacyJob(t, s, "old-customer-job", "客户经理", at.Add(-time.Hour))
		saveInboundLegacyJob(t, s, "current-customer-job", "客户经理", at.Add(-time.Minute))

		result, err := s.AdoptInboundConversationProfile(req)
		if err != nil || result == nil ||
			result.Outcome != InboundProfileAdopted ||
			result.Profile == nil ||
			result.Profile.BackendJobID == nil ||
			*result.Profile.BackendJobID != "current-customer-job" {
			t.Fatalf("同名职位必须绑定最近同步的当前职位: result=%+v err=%v", result, err)
		}
	})
}

func TestAdoptInboundConversationProfileHonorsHumanLevelProfileGate(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 7, 26, 9, 30, 0, 0, time.UTC)
	req := inboundAdoptionRequest(at)
	seedInboundConversation(
		t,
		s,
		req.Platform,
		req.AccountRef,
		req.ConversationRef,
		req.PlatformUserRef,
		"候选人甲",
		at,
	)
	saveInboundLegacyJob(t, s, "job-42", "客户 经理", at.Add(-time.Minute))
	if _, err := s.SelectCandidateProfile(SelectCandidateProfileRequest{
		ProfileID: "profile-existing",
		Scope: CandidateProfileScope{
			Platform:        req.Platform,
			AccountRef:      req.AccountRef,
			PlatformUserRef: req.PlatformUserRef,
			PositionRef:     "another-job",
		},
		DisplayName:   textPointer("候选人甲"),
		PositionTitle: textPointer("另一个职位"),
		ObservedAt:    at.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	result, err := s.AdoptInboundConversationProfile(req)
	if result != nil || !errors.Is(err, ErrCandidateAlreadyProfiled) {
		t.Fatalf("既有人级档案必须拦截: result=%+v err=%v", result, err)
	}
	key := ConversationKey{
		Platform:        req.Platform,
		AccountRef:      req.AccountRef,
		ConversationRef: req.ConversationRef,
	}
	conversation, queryErr := s.ConversationByKey(key)
	if queryErr != nil || conversation == nil ||
		conversation.TrackingState != TrackingUntracked {
		t.Fatalf("冲突后会话不应被部分收编: conversation=%+v err=%v", conversation, queryErr)
	}
	tracked, queryErr := s.TrackedIntentByConversation(key)
	if queryErr != nil || tracked != nil {
		t.Fatalf("冲突后不得留下 tracked intent: tracked=%+v err=%v", tracked, queryErr)
	}
}

func TestAdoptInboundConversationProfileRejectsConversationIdentityDriftWithoutPartialFacts(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	req := inboundAdoptionRequest(at)
	seedInboundConversation(
		t,
		s,
		req.Platform,
		req.AccountRef,
		req.ConversationRef,
		"different-user",
		"候选人甲",
		at,
	)
	saveInboundLegacyJob(t, s, "job-42", "客户 经理", at.Add(-time.Minute))

	result, err := s.AdoptInboundConversationProfile(req)
	if result != nil || !errors.Is(err, ErrInboundProfileAdoptionConflict) {
		t.Fatalf("会话换绑必须失败: result=%+v err=%v", result, err)
	}
	assertInboundAdoptionLeftNoFacts(t, s, req)
}

func assertInboundAdoptionLeftNoFacts(
	t *testing.T,
	s *Store,
	req AdoptInboundConversationProfileRequest,
) {
	t.Helper()
	var candidateN, profileN, trackedN int64
	if err := s.db.Model(&Candidate{}).
		Where("platform = ? AND platform_user_ref = ?", req.Platform, req.PlatformUserRef).
		Count(&candidateN).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&CandidateProfile{}).
		Where("platform = ? AND account_ref = ? AND conversation_ref = ?",
			req.Platform, req.AccountRef, req.ConversationRef).
		Count(&profileN).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&TrackedIntent{}).
		Where("platform = ? AND account_ref = ? AND conversation_ref = ?",
			req.Platform, req.AccountRef, req.ConversationRef).
		Count(&trackedN).Error; err != nil {
		t.Fatal(err)
	}
	if candidateN != 0 || profileN != 0 || trackedN != 0 {
		t.Fatalf(
			"保守路径留下部分事实: candidates=%d profiles=%d tracked=%d",
			candidateN,
			profileN,
			trackedN,
		)
	}
	conversation, err := s.ConversationByKey(ConversationKey{
		Platform:        req.Platform,
		AccountRef:      req.AccountRef,
		ConversationRef: req.ConversationRef,
	})
	if err != nil || conversation == nil ||
		conversation.TrackingState != TrackingUntracked {
		t.Fatalf("保守路径改变会话状态: conversation=%+v err=%v", conversation, err)
	}
}
