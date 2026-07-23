package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

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
	endedAt := now
	if err := s.db.Create(&SourcingBatch{
		BatchID: batchID, Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		ContextRevisionHash: "revision-" + fixture.ProfileID,
		TargetCount:         1, PositionRef: &positionRef, PositionBoundAt: &now,
		Status: SourcingBatchCompleted, StartedAt: now.Add(-time.Minute), EndedAt: &endedAt,
	}).Error; err != nil {
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
