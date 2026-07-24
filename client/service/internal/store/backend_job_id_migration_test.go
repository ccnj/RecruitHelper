package store

import (
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
)

func TestBackfillBackendJobIDsUsesOnlyUniqueExistingFacts(t *testing.T) {
	s := openTest(t)
	base := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
	first := contextRevisionFixture("context-job-17", "revision-job-17-first", base)
	second := contextRevisionFixture("context-job-17", "revision-job-17-second", base.Add(time.Minute))
	other := contextRevisionFixture("context-job-18", "revision-job-18", base.Add(2*time.Minute))
	other.SourceJobRef = "18"
	if _, err := s.SaveJobAIContextRevisions([]m5ai.ContextRevision{first, second, other}); err != nil {
		t.Fatal(err)
	}

	key := AccountKey{Platform: "zhilian", AccountRef: "account-backend-job-migration"}
	createM4Account(t, s, key.Platform, key.AccountRef)
	profileIDs := []string{"profile-unique", "profile-same-job", "profile-ambiguous", "profile-none", "profile-existing"}
	for index, profileID := range profileIDs {
		if _, err := s.SelectCandidateProfile(SelectCandidateProfileRequest{
			ProfileID: profileID,
			Scope: CandidateProfileScope{
				Platform: key.Platform, AccountRef: key.AccountRef,
				PlatformUserRef: "user-backend-job-" + profileID,
				PositionRef:     "position-backend-job-" + profileID,
			},
			ObservedAt: base.Add(time.Duration(index) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	existingBackendJobID := "99"
	if err := s.db.Model(&CandidateProfile{}).Where("profile_id = ?", "profile-existing").
		UpdateColumn("backend_job_id", existingBackendJobID).Error; err != nil {
		t.Fatal(err)
	}
	bindings := []ProfileAIContextBinding{
		{
			BindingID: "binding-unique", ProfileID: "profile-unique",
			ContextID: first.ContextID, RevisionHash: first.RevisionHash,
			Status: ProfileAIContextBindingActive, BoundBy: "fixture", BoundAt: base,
		},
		{
			BindingID: "binding-same-first", ProfileID: "profile-same-job",
			ContextID: first.ContextID, RevisionHash: first.RevisionHash,
			Status: ProfileAIContextBindingSuperseded, BoundBy: "fixture", BoundAt: base,
		},
		{
			BindingID: "binding-same-second", ProfileID: "profile-same-job",
			ContextID: second.ContextID, RevisionHash: second.RevisionHash,
			Status: ProfileAIContextBindingActive, BoundBy: "fixture", BoundAt: base.Add(time.Minute),
		},
		{
			BindingID: "binding-ambiguous-first", ProfileID: "profile-ambiguous",
			ContextID: first.ContextID, RevisionHash: first.RevisionHash,
			Status: ProfileAIContextBindingSuperseded, BoundBy: "fixture", BoundAt: base,
		},
		{
			BindingID: "binding-ambiguous-other", ProfileID: "profile-ambiguous",
			ContextID: other.ContextID, RevisionHash: other.RevisionHash,
			Status: ProfileAIContextBindingActive, BoundBy: "fixture", BoundAt: base.Add(time.Minute),
		},
	}
	if err := s.db.Create(&bindings).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&SourcingBatch{
		BatchID: "batch-backend-job-migration", Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: first.RevisionHash, TargetCount: 1,
		Status: SourcingBatchPreparing, StartedAt: base,
	}).Error; err != nil {
		t.Fatal(err)
	}

	report, err := backfillBackendJobIDs(s.db)
	if err != nil {
		t.Fatal(err)
	}
	if report.BatchesFilled != 1 || report.BatchesUnresolved != 0 ||
		report.ProfilesFilled != 2 || report.ProfilesUnresolved != 1 || report.ProfilesAmbiguous != 1 {
		t.Fatalf("回填统计错误: %+v", report)
	}
	assertProfileBackendJobID(t, s, "profile-unique", "17")
	assertProfileBackendJobID(t, s, "profile-same-job", "17")
	assertProfileBackendJobID(t, s, "profile-existing", "99")
	assertProfileBackendJobID(t, s, "profile-ambiguous", "")
	assertProfileBackendJobID(t, s, "profile-none", "")

	replayed, err := backfillBackendJobIDs(s.db)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.BatchesFilled != 0 || replayed.ProfilesFilled != 0 ||
		replayed.ProfilesUnresolved != 1 || replayed.ProfilesAmbiguous != 1 {
		t.Fatalf("重复迁移不幂等: %+v", replayed)
	}
}

func assertProfileBackendJobID(t *testing.T, s *Store, profileID, want string) {
	t.Helper()
	var profile CandidateProfile
	if err := s.db.First(&profile, "profile_id = ?", profileID).Error; err != nil {
		t.Fatal(err)
	}
	if want == "" {
		if profile.BackendJobID != nil {
			t.Fatalf("%s 不应猜测后台职位 ID: %+v", profileID, profile.BackendJobID)
		}
		return
	}
	if profile.BackendJobID == nil || *profile.BackendJobID != want {
		t.Fatalf("%s 后台职位 ID 错误: got=%+v want=%s", profileID, profile.BackendJobID, want)
	}
}
