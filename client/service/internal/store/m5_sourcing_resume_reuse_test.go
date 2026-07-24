package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/contract/gen/go/protocol"
)

func seedM5SourcingResume(
	t *testing.T,
	s *Store,
	fixture resumeStoreFixture,
) (SourcingCandidateRun, SourcingGreetingInvocation) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	resumeRaw, err := json.Marshal(canonicalResumeV1{
		Basic:           []protocol.CandidateResumeLabelValue{{Label: "合成标签", Value: "合成值"}},
		Expectations:    []protocol.CandidateResumeLabelValue{{Label: "合成期望", Value: "合成内容"}},
		SelfEvaluation:  "合成自评",
		Education:       "合成教育",
		WorkExperiences: "合成经历",
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(resumeRaw)
	contentHash := hex.EncodeToString(digest[:])
	batchID := "batch-" + fixture.ProfileID
	positionRef := "position-" + fixture.ProfileID
	backendJobID := "job-" + fixture.ProfileID
	endedAt := now
	if err := s.db.Create(&SourcingBatch{
		BatchID: batchID, Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		ContextRevisionHash: "revision-" + fixture.ProfileID, BackendJobID: &backendJobID,
		TargetCount: 1, PositionRef: &positionRef, PositionBoundAt: &now,
		Status: SourcingBatchCompleted, StartedAt: now.Add(-time.Minute), EndedAt: &endedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&CandidateProfile{}).
		Where("profile_id = ?", fixture.ProfileID).
		UpdateColumn("backend_job_id", backendJobID).Error; err != nil {
		t.Fatal(err)
	}
	run := SourcingCandidateRun{
		RunID: "run-" + fixture.ProfileID, BatchID: &batchID,
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		ContextRevisionHash: "revision-" + fixture.ProfileID,
		PlatformUserRef:     fixture.UserRef, PositionRef: positionRef,
		ContactState: "unestablished", SourceLogicalDispatchID: "source-" + fixture.ProfileID,
		ObservedAt: now.UnixMilli(), CapturedAt: now,
		SchemaVersion: resumeSnapshotSchemaV1, ContentHash: contentHash, ResumeJSON: string(resumeRaw),
	}
	if err := s.db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	finishedAt := now
	effectStartedAt := now
	invocation := SourcingGreetingInvocation{
		InvocationID: "invocation-" + fixture.ProfileID,
		BatchID:      batchID, RunID: run.RunID, ProfileID: fixture.ProfileID,
		ContextRevisionHash: run.ContextRevisionHash, RunContentHash: run.ContentHash,
		Provider: "deepseek", Model: "deepseek-v4-pro", InputHash: "input",
		Status: AIInvocationOK, GreetingText: "你好", ContentHash: sourcingGreetingContentHash("你好"),
		StartedAt: now, FinishedAt: &finishedAt,
		EffectIntentID: &fixture.GreetingIntent, EffectStartedAt: &effectStartedAt,
	}
	if err := s.db.Create(&invocation).Error; err != nil {
		t.Fatal(err)
	}
	return run, invocation
}

func seedCommunicationSourcingResume(
	t *testing.T,
	s *Store,
	profileID string,
) (resumeStoreFixture, SourcingCandidateRun) {
	t.Helper()
	fixture := seedResumeStoreFixture(t, s, profileID)
	run, _ := seedM5SourcingResume(t, s, fixture)
	now := time.Now().UTC().Truncate(time.Millisecond)
	greetingText := "你好"
	if err := s.db.Create(&Message{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		ConversationRef: fixture.ConversationRef, Seq: 1,
		Direction: "out", Kind: "text", ContentHash: sourcingGreetingContentHash(greetingText),
		Text: &greetingText, OutboundIntentID: &fixture.GreetingIntent,
		Origin: "effectResult",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&Conversation{}).
		Where("platform = ? AND account_ref = ? AND conversation_ref = ?",
			fixture.Platform, fixture.AccountRef, fixture.ConversationRef).
		Update("last_message_seq", 1).Error; err != nil {
		t.Fatal(err)
	}
	messageSeq := int64(1)
	if err := s.db.Model(&EffectIntent{}).
		Where("intent_id = ?", fixture.GreetingIntent).
		Update("result_message_seq", messageSeq).Error; err != nil {
		t.Fatal(err)
	}
	if _, created, err := s.EnsureCommunicationV4RootForGreetedProfile(
		fixture.ProfileID, now,
	); err != nil || !created {
		t.Fatalf("构造 V4 根失败: created=%v err=%v", created, err)
	}
	revision := contextRevisionFixture(
		"context-"+fixture.ProfileID,
		run.ContextRevisionHash,
		now,
	)
	revision.SourceKind = legacyJobConfigSourceKind
	revision.SourceJobRef = "job-" + fixture.ProfileID
	if _, err := s.SaveCurrentLegacyJobAIContext(
		[]m5ai.ContextRevision{revision},
		now,
	); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&ProfileAIContextBinding{
		BindingID: "binding-" + fixture.ProfileID,
		ProfileID: fixture.ProfileID,
		ContextID: revision.ContextID, RevisionHash: revision.RevisionHash,
		Status: ProfileAIContextBindingActive, Reason: sourcingProfileAIContextBindingReason,
		BoundBy: sourcingProfileAIContextBoundBy, BoundAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	endedAt := now
	if err := s.db.Model(&M5TrialSelection{}).
		Where("profile_id = ? AND status = ?", fixture.ProfileID, M5TrialSelectionActive).
		Updates(map[string]any{
			"status":      M5TrialSelectionCompleted,
			"active_slot": nil,
			"ended_at":    endedAt,
		}).Error; err != nil {
		t.Fatal(err)
	}
	return fixture, run
}

func TestReuseSourcingResumeForActiveM5TrialAdoptsExactGreetingRun(t *testing.T) {
	s := openTest(t)
	fixture := seedResumeStoreFixture(t, s, "profile-reuse-sourcing")
	run, _ := seedM5SourcingResume(t, s, fixture)

	result, err := s.ReuseSourcingResumeForActiveM5Trial(fixture.ProfileID, time.Now())
	if err != nil || result.Status != SourcingResumeReuseAdopted || result.Snapshot == nil {
		t.Fatalf("M6 简历未被复用: result=%+v err=%v", result, err)
	}
	if result.Snapshot.SourceKind != resumeSnapshotSourceSourcing ||
		result.Snapshot.SourceLogicalDispatchID != run.SourceLogicalDispatchID ||
		result.Snapshot.ContentHash != run.ContentHash ||
		result.Snapshot.ResumeJSON != run.ResumeJSON {
		t.Fatalf("投影没有逐字保留 M6 事实: snapshot=%+v run=%+v", result.Snapshot, run)
	}
	profile, err := s.CandidateProfileByID(fixture.ProfileID)
	if err != nil || profile == nil || profile.ResumeCaptureState != ResumeCaptureCaptured ||
		profile.ActiveResumeSnapshotID == nil ||
		*profile.ActiveResumeSnapshotID != result.Snapshot.SnapshotID {
		t.Fatalf("profile 未原子绑定复用快照: profile=%+v err=%v", profile, err)
	}
	var commandCount int64
	if err := s.db.Model(&CmdRecord{}).
		Where("name = ?", protocol.PrimCandidateReadResume).
		Count(&commandCount).Error; err != nil || commandCount != 0 {
		t.Fatalf("复用不得产生 IM 补采命令: count=%d err=%v", commandCount, err)
	}

	replayed, err := s.ReuseSourcingResumeForActiveM5Trial(fixture.ProfileID, time.Now().Add(time.Minute))
	if err != nil || replayed.Status != SourcingResumeReuseAdopted ||
		replayed.Snapshot == nil || replayed.Snapshot.SnapshotID != result.Snapshot.SnapshotID {
		t.Fatalf("复用重放不幂等: replay=%+v err=%v", replayed, err)
	}
	var snapshotCount int64
	if err := s.db.Model(&CandidateResumeSnapshot{}).
		Where("profile_id = ?", fixture.ProfileID).
		Count(&snapshotCount).Error; err != nil || snapshotCount != 1 {
		t.Fatalf("重放增生快照: count=%d err=%v", snapshotCount, err)
	}
}

func TestReuseSourcingResumeForCommunicationProfileDoesNotNeedTrialSlot(t *testing.T) {
	s := openTest(t)
	fixture, run := seedCommunicationSourcingResume(t, s, "profile-reuse-communication")
	key := AccountKey{Platform: fixture.Platform, AccountRef: fixture.AccountRef}

	profileIDs, err := s.SourcingProfileIDsNeedingResumeForAccount(key)
	if err != nil || len(profileIDs) != 1 || profileIDs[0] != fixture.ProfileID {
		t.Fatalf("未枚举到精确 M6 简历复用目标: ids=%v err=%v", profileIDs, err)
	}
	if result, err := s.ReuseSourcingResumeForActiveM5Trial(
		fixture.ProfileID, time.Now(),
	); result != nil || !errors.Is(err, ErrM5TrialNotActive) {
		t.Fatalf("旧试运行入口不应被隐式放宽: result=%+v err=%v", result, err)
	}

	result, err := s.ReuseSourcingResumeForCommunicationProfile(fixture.ProfileID, time.Now())
	if err != nil || result.Status != SourcingResumeReuseAdopted || result.Snapshot == nil ||
		result.Snapshot.SourceLogicalDispatchID != run.SourceLogicalDispatchID {
		t.Fatalf("V4 档案未复用 M6 简历: result=%+v err=%v", result, err)
	}
	profileIDs, err = s.SourcingProfileIDsNeedingResumeForAccount(key)
	if err != nil || len(profileIDs) != 0 {
		t.Fatalf("已复用档案仍被重复枚举: ids=%v err=%v", profileIDs, err)
	}
}

func TestReuseSourcingResumeForCommunicationProfileRejectsBackendJobMismatch(t *testing.T) {
	s := openTest(t)
	fixture, _ := seedCommunicationSourcingResume(t, s, "profile-reuse-context-mismatch")
	if err := s.db.Model(&CandidateProfile{}).
		Where("profile_id = ?", fixture.ProfileID).
		Update("backend_job_id", "other-job").Error; err != nil {
		t.Fatal(err)
	}

	result, err := s.ReuseSourcingResumeForCommunicationProfile(fixture.ProfileID, time.Now())
	if result != nil || !errors.Is(err, ErrResumeCaptureBinding) {
		t.Fatalf("后台职位与招呼 revision 错绑必须阻断: result=%+v err=%v", result, err)
	}
	var snapshots int64
	if err := s.db.Model(&CandidateResumeSnapshot{}).
		Where("profile_id = ?", fixture.ProfileID).
		Count(&snapshots).Error; err != nil || snapshots != 0 {
		t.Fatalf("错绑不得留下简历投影: count=%d err=%v", snapshots, err)
	}
}

func TestReuseSourcingResumeFallsBackForMissingOrHashAnomaly(t *testing.T) {
	t.Run("no exact M6 greeting invocation", func(t *testing.T) {
		s := openTest(t)
		fixture := seedResumeStoreFixture(t, s, "profile-reuse-missing")
		result, err := s.ReuseSourcingResumeForActiveM5Trial(fixture.ProfileID, time.Now())
		if err != nil || result.Status != SourcingResumeReuseUnavailable || result.Snapshot != nil {
			t.Fatalf("无 M6 来源应保留 IM 补采: result=%+v err=%v", result, err)
		}
	})

	t.Run("hash anomaly is audited and falls back", func(t *testing.T) {
		s := openTest(t)
		fixture := seedResumeStoreFixture(t, s, "profile-reuse-hash")
		_, invocation := seedM5SourcingResume(t, s, fixture)
		if err := s.db.Model(&SourcingGreetingInvocation{}).
			Where("invocation_id = ?", invocation.InvocationID).
			Update("run_content_hash", "mismatch").Error; err != nil {
			t.Fatal(err)
		}
		for attempt := 0; attempt < 2; attempt++ {
			result, err := s.ReuseSourcingResumeForActiveM5Trial(
				fixture.ProfileID, time.Now().Add(time.Duration(attempt)*time.Minute),
			)
			if err != nil || result.Status != SourcingResumeReuseFreshCapture || result.Snapshot != nil {
				t.Fatalf("hash 异常未回退 IM: attempt=%d result=%+v err=%v", attempt, result, err)
			}
		}
		profile, _ := s.CandidateProfileByID(fixture.ProfileID)
		if profile.ResumeCaptureState != ResumeCaptureUnattempted || profile.ActiveResumeSnapshotID != nil {
			t.Fatalf("hash 异常不得伪造 captured: %+v", profile)
		}
		var audits []AuditEntry
		if err := s.db.Where("category = ? AND ref_msg_id = ?",
			sourcingResumeReuseAuditCategory, invocation.InvocationID).Find(&audits).Error; err != nil {
			t.Fatal(err)
		}
		if len(audits) != 1 ||
			audits[0].Detail != "reason=contentOrSchemaMismatch fallback=imCapture" ||
			audits[0].Platform != "" || audits[0].AccountRef != "" ||
			audits[0].ConversationRef != "" {
			t.Fatalf("异常审计不唯一或越过脱敏边界: %+v", audits)
		}
	})
}

func TestReuseSourcingResumeBindingMismatchNeverFallsBackToPage(t *testing.T) {
	s := openTest(t)
	fixture := seedResumeStoreFixture(t, s, "profile-reuse-binding")
	run, _ := seedM5SourcingResume(t, s, fixture)
	if err := s.db.Model(&SourcingCandidateRun{}).Where("run_id = ?", run.RunID).
		Update("position_ref", "wrong-position").Error; err != nil {
		t.Fatal(err)
	}
	result, err := s.ReuseSourcingResumeForActiveM5Trial(fixture.ProfileID, time.Now())
	if result != nil || !errors.Is(err, ErrResumeCaptureBinding) {
		t.Fatalf("错绑必须响亮失败: result=%+v err=%v", result, err)
	}
	var commandCount int64
	if err := s.db.Model(&CmdRecord{}).
		Where("name = ?", protocol.PrimCandidateReadResume).
		Count(&commandCount).Error; err != nil || commandCount != 0 {
		t.Fatalf("错绑不得打开 IM 页面: count=%d err=%v", commandCount, err)
	}
}
