package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"recruithelper/contract/gen/go/protocol"
)

type resumeStoreFixture struct {
	greetingLedgerFixture
	ConversationRef string
	UserRef         string
	GreetingIntent  string
}

func seedResumeStoreFixture(t *testing.T, s *Store, profileID string) resumeStoreFixture {
	t.Helper()
	base := seedGreetingLedger(t, s, profileID)
	now := time.Now().UTC().Truncate(time.Millisecond)
	fixture := resumeStoreFixture{
		greetingLedgerFixture: base,
		ConversationRef:       "conversation-" + profileID,
		UserRef:               "person-" + profileID,
		GreetingIntent:        "greeting-" + profileID,
	}
	if err := s.db.Create(&Conversation{
		Platform: base.Platform, AccountRef: base.AccountRef, ConversationRef: fixture.ConversationRef,
		PlatformUserRef: fixture.UserRef, TrackingState: TrackingAdopted,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&TrackedIntent{
		Platform: base.Platform, AccountRef: base.AccountRef, ConversationRef: fixture.ConversationRef,
		Status: TrackingAdopted, RequestedBy: "greeting", RequestedAt: now, AdoptedAt: &now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	conversationRef := fixture.ConversationRef
	if err := s.db.Create(&EffectIntent{
		IntentID: fixture.GreetingIntent, IdemKey: "idem-" + fixture.GreetingIntent,
		Platform: base.Platform, AccountRef: base.AccountRef,
		Primitive: protocol.PrimChatSendGreeting, TargetRef: profileID,
		PayloadHash: "payload", GuardsHash: "guards", RootMsgID: "root-" + fixture.GreetingIntent,
		Status: EffectIntentOk, DeadlineMs: now.Add(time.Hour).UnixMilli(),
		ResultConversationRef: &conversationRef,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&CandidateProfile{}).Where("profile_id = ?", profileID).Updates(map[string]any{
		"platform_user_ref":             fixture.UserRef,
		"main_status":                   CandidateProfileGreeted,
		"successful_greeting_intent_id": fixture.GreetingIntent,
		"conversation_ref":              fixture.ConversationRef,
		"greeted_at":                    now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectM5TrialProfile(profileID, "selection-"+profileID, "user", now); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func resumeCaptureCommand(t *testing.T, fixture resumeStoreFixture, msgID string) CmdRecord {
	t.Helper()
	args, err := protocol.Encode(protocol.CandidateReadResumeArgs{
		ConversationRef: fixture.ConversationRef, PlatformUserRef: fixture.UserRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	contextRaw, err := protocol.Encode(protocol.CmdContext{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		ExpectedPrincipalFingerprint: fixture.Principal,
	})
	if err != nil {
		t.Fatal(err)
	}
	return CmdRecord{
		MsgID: msgID, Name: protocol.PrimCandidateReadResume, Class: string(protocol.ClassIntrusive),
		Domain:   fixture.Platform + ":" + fixture.AccountRef,
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		ExpectedPrincipalFingerprint: fixture.Principal, ContextJSON: string(contextRaw), Args: string(args),
		HandID: fixture.HandID, Session: fixture.Session, BootIDAtDispatch: fixture.BootID,
		Status: CmdQueued, DeadlineMs: time.Now().Add(time.Minute).UnixMilli(), ExecBudgetMs: 60_000,
	}
}

func resumeData(fixture resumeStoreFixture) protocol.CandidateReadResumeData {
	return protocol.CandidateReadResumeData{
		ConversationRef: fixture.ConversationRef, PlatformUserRef: fixture.UserRef,
		ObservedAt:     time.Now().UnixMilli(),
		Basic:          []protocol.CandidateResumeLabelValue{{Label: "合成标签", Value: "合成值"}},
		Expectations:   []protocol.CandidateResumeLabelValue{{Label: "合成期望", Value: "合成内容"}},
		SelfEvaluation: "", Education: "合成教育", WorkExperiences: "合成经历",
	}
}

func settleResumeCaptureOK(t *testing.T, s *Store, msgID string, data protocol.CandidateReadResumeData) {
	t.Helper()
	dataRaw, err := protocol.Encode(data)
	if err != nil {
		t.Fatal(err)
	}
	resultRaw, err := protocol.Encode(protocol.ResultBody{
		Ref: msgID, Status: protocol.ResultStatusOk, Data: dataRaw, ExecMs: 10, Replayed: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.db.Model(&CmdRecord{}).Where("msg_id = ?", msgID).Updates(map[string]any{
		"status": CmdOk, "result_body": string(resultRaw), "terminal_at": now,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestResumeCaptureCommandAndProfileFactAreAtomicAndIdempotent(t *testing.T) {
	s := openTest(t)
	fixture := seedResumeStoreFixture(t, s, "profile-resume-atomic")
	first, err := s.CreateResumeCaptureCmd(CreateResumeCaptureCmdRequest{
		ProfileID: fixture.ProfileID, Command: resumeCaptureCommand(t, fixture, "resume-root-atomic"),
	})
	if err != nil || !first.Created || first.Command.LogicalDispatchID != "resume-root-atomic" {
		t.Fatalf("首次补采 WAL 失败: result=%+v err=%v", first, err)
	}
	profile, _ := s.CandidateProfileByID(fixture.ProfileID)
	if profile.ResumeCaptureState != ResumeCaptureInFlight || profile.ResumeCaptureLogicalDispatchID == nil ||
		*profile.ResumeCaptureLogicalDispatchID != "resume-root-atomic" || profile.ResumeCaptureAttemptedAt == nil {
		t.Fatalf("profile 未与 logical root 同事务冻结: %+v", profile)
	}
	repeated, err := s.CreateResumeCaptureCmd(CreateResumeCaptureCmdRequest{
		ProfileID: fixture.ProfileID, Command: resumeCaptureCommand(t, fixture, "resume-root-duplicate"),
	})
	if err != nil || repeated.Created || repeated.Command.MsgID != "resume-root-atomic" {
		t.Fatalf("重复计划必须附着原 root: result=%+v err=%v", repeated, err)
	}
	var count int64
	if err := s.db.Model(&CmdRecord{}).Where("name = ?", protocol.PrimCandidateReadResume).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("补采命令增生: count=%d err=%v", count, err)
	}
}

func TestResumeSnapshotReplayIsImmutableAndConflictTurnsManual(t *testing.T) {
	s := openTest(t)
	fixture := seedResumeStoreFixture(t, s, "profile-resume-snapshot")
	root := "resume-root-snapshot"
	if _, err := s.CreateResumeCaptureCmd(CreateResumeCaptureCmdRequest{
		ProfileID: fixture.ProfileID, Command: resumeCaptureCommand(t, fixture, root),
	}); err != nil {
		t.Fatal(err)
	}
	data := resumeData(fixture)
	settleResumeCaptureOK(t, s, root, data)
	first, err := s.CompleteResumeCapture(CompleteResumeCaptureRequest{
		ProfileID: fixture.ProfileID, LogicalDispatchID: root, SnapshotID: "snapshot-one", Data: data,
	})
	if err != nil || first.SnapshotID != "snapshot-one" || first.ResumeJSON == "" {
		t.Fatalf("首次快照失败: snapshot=%+v err=%v", first, err)
	}
	replayed, err := s.CompleteResumeCapture(CompleteResumeCaptureRequest{
		ProfileID: fixture.ProfileID, LogicalDispatchID: root, SnapshotID: "snapshot-two", Data: data,
	})
	if err != nil || replayed.SnapshotID != first.SnapshotID || replayed.ContentHash != first.ContentHash {
		t.Fatalf("同 result 重放未复用快照: replay=%+v err=%v", replayed, err)
	}
	conflict := data
	conflict.Education = "另一份合成教育"
	_, err = s.CompleteResumeCapture(CompleteResumeCaptureRequest{
		ProfileID: fixture.ProfileID, LogicalDispatchID: root, SnapshotID: "snapshot-three", Data: conflict,
	})
	if !errors.Is(err, ErrResumeCaptureConflict) {
		t.Fatalf("同 logical 不同正文必须冲突: %v", err)
	}
	profile, _ := s.CandidateProfileByID(fixture.ProfileID)
	if profile.ResumeCaptureState != ResumeCaptureManualRequired || profile.ActiveResumeSnapshotID != nil {
		t.Fatalf("冲突未转人工或仍绑定旧快照: %+v", profile)
	}
	status, err := s.M5TrialStatus()
	if err != nil || status.Selection.Status != M5TrialSelectionManualRequired {
		t.Fatalf("试运行选择未随冲突收敛: status=%+v err=%v", status, err)
	}
	var snapshots int64
	if err := s.db.Model(&CandidateResumeSnapshot{}).Count(&snapshots).Error; err != nil || snapshots != 1 {
		t.Fatalf("冲突不得增生第二快照: count=%d err=%v", snapshots, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(first.ResumeJSON), &decoded); err != nil || len(decoded) != 5 {
		t.Fatalf("快照必须只含五分区正文: fields=%v err=%v", decoded, err)
	}
}

func TestM5TrialSelectionIsPersistentAndGloballyUnique(t *testing.T) {
	s := openTest(t)
	first := seedResumeStoreFixture(t, s, "profile-trial-first")
	if _, err := s.SelectM5TrialProfile(first.ProfileID, "selection-repeat", "user", time.Now()); err != nil {
		t.Fatalf("同 profile 选择应幂等: %v", err)
	}
	slot := m5TrialActiveSlot
	err := s.db.Create(&M5TrialSelection{
		SelectionID: "selection-conflict", ProfileID: "another-profile",
		Status: M5TrialSelectionActive, ActiveSlot: &slot, SelectedBy: "user", SelectedAt: time.Now(),
	}).Error
	if err == nil {
		t.Fatal("数据库唯一闸必须拒绝第二个 active slot")
	}
	var active int64
	if err := s.db.Model(&M5TrialSelection{}).Where("status = ?", M5TrialSelectionActive).Count(&active).Error; err != nil || active != 1 {
		t.Fatalf("active 试运行选择不唯一: count=%d err=%v", active, err)
	}
}
