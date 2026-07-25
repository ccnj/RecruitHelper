package store

import (
	"encoding/json"
	"errors"
	"strings"
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

func seedInboundResumeStoreFixture(
	t *testing.T,
	s *Store,
	at time.Time,
) resumeStoreFixture {
	t.Helper()
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
	saveInboundLegacyJob(t, s, "job-inbound-resume", "客户 经理", at.Add(-time.Minute))
	adopted, err := s.AdoptInboundConversationProfile(req)
	if err != nil || adopted == nil || adopted.Profile == nil ||
		adopted.Outcome != InboundProfileAdopted {
		t.Fatalf("准备主动来聊档案失败: adopted=%+v err=%v", adopted, err)
	}

	fixture := resumeStoreFixture{
		greetingLedgerFixture: greetingLedgerFixture{
			ProfileID: adopted.Profile.ProfileID,
			Platform:  req.Platform, AccountRef: req.AccountRef,
			HandID: "hand-inbound-resume", Session: "session-inbound-resume",
			BootID: "boot-inbound-resume", Principal: "principal-inbound-resume",
		},
		ConversationRef: req.ConversationRef,
		UserRef:         req.PlatformUserRef,
	}
	if err := s.BindAccountPrincipal(
		AccountKey{Platform: req.Platform, AccountRef: req.AccountRef},
		fixture.HandID,
		fixture.Principal,
		fixture.Session,
		fixture.BootID,
		at,
	); err != nil {
		t.Fatal(err)
	}
	sourceKey := strings.Repeat("a", 64)
	inboundText := "合成主动来聊消息"
	if _, err := s.ApplyConversationChanges(ApplyConversationChangesRequest{
		Key: ConversationKey{
			Platform:        req.Platform,
			AccountRef:      req.AccountRef,
			ConversationRef: req.ConversationRef,
		},
		ExpectedTailSeq: 0,
		PlatformUserRef: req.PlatformUserRef,
		NewMessages: []MessageDraft{{
			Direction: "in", Kind: "text",
			ContentHash: strings.Repeat("b", 64),
			Text:        &inboundText, Origin: "external", SourceKey: &sourceKey,
		}},
		Adopt:    true,
		SyncedAt: at.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	return fixture
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

func TestInboundSelectedResumeCaptureReusesWALAcrossRestartWithoutTrialSelection(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	fixture := seedInboundResumeStoreFixture(t, s, at)

	// A retracted historical outbound fact is not part of the active ledger
	// and therefore must not invalidate a currently inbound-only conversation.
	retractedAt := at.Add(2 * time.Second)
	if err := s.db.Create(&Message{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		ConversationRef: fixture.ConversationRef, Seq: 2,
		Direction: "out", Kind: "text", ContentHash: strings.Repeat("c", 64),
		Origin: "self", RetractedAt: &retractedAt, RetractionReason: "testFixture",
	}).Error; err != nil {
		t.Fatal(err)
	}
	target, err := s.InboundResumeCaptureTarget(fixture.ProfileID)
	if err != nil || target == nil ||
		target.Profile.ProfileID != fixture.ProfileID ||
		target.Conversation.ConversationRef != fixture.ConversationRef {
		t.Fatalf("巡检未识别待补采主动来聊目标: target=%+v err=%v", target, err)
	}

	root := "resume-root-inbound"
	first, err := s.CreateResumeCaptureCmd(CreateResumeCaptureCmdRequest{
		ProfileID: fixture.ProfileID,
		Command:   resumeCaptureCommand(t, fixture, root),
		Now:       at.Add(3 * time.Second),
	})
	if err != nil || first == nil || !first.Created ||
		first.Command.LogicalDispatchID != root {
		t.Fatalf("主动来聊首次补采 WAL 失败: result=%+v err=%v", first, err)
	}
	var trialN int64
	if err := s.db.Model(&M5TrialSelection{}).Count(&trialN).Error; err != nil {
		t.Fatal(err)
	}
	if trialN != 0 {
		t.Fatalf("主动来聊不得伪造 M5 试运行选择: count=%d", trialN)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	replayedCreate, err := s.CreateResumeCaptureCmd(CreateResumeCaptureCmdRequest{
		ProfileID: fixture.ProfileID,
		Command:   resumeCaptureCommand(t, fixture, "resume-root-inbound-duplicate"),
		Now:       at.Add(4 * time.Second),
	})
	if err != nil || replayedCreate == nil || replayedCreate.Created ||
		replayedCreate.Command.MsgID != root ||
		replayedCreate.Command.LogicalDispatchID != root {
		t.Fatalf("重启后必须附着原补采 logical root: result=%+v err=%v", replayedCreate, err)
	}
	var commandN int64
	if err := s.db.Model(&CmdRecord{}).
		Where("name = ?", protocol.PrimCandidateReadResume).
		Count(&commandN).Error; err != nil || commandN != 1 {
		t.Fatalf("重接后补采命令增生: count=%d err=%v", commandN, err)
	}

	data := resumeData(fixture)
	settleResumeCaptureOK(t, s, root, data)
	snapshot, err := s.CompleteResumeCapture(CompleteResumeCaptureRequest{
		ProfileID: fixture.ProfileID, LogicalDispatchID: root,
		SnapshotID: "snapshot-inbound", Data: data,
	})
	if err != nil || snapshot == nil ||
		snapshot.SnapshotID != "snapshot-inbound" ||
		snapshot.SourceKind != resumeSnapshotSourceIM {
		t.Fatalf("主动来聊简历快照收编失败: snapshot=%+v err=%v", snapshot, err)
	}
	replayedSnapshot, err := s.CompleteResumeCapture(CompleteResumeCaptureRequest{
		ProfileID: fixture.ProfileID, LogicalDispatchID: root,
		SnapshotID: "snapshot-inbound-replay", Data: data,
	})
	if err != nil || replayedSnapshot == nil ||
		replayedSnapshot.SnapshotID != snapshot.SnapshotID ||
		replayedSnapshot.ContentHash != snapshot.ContentHash {
		t.Fatalf("主动来聊结果重放不得增生快照: snapshot=%+v err=%v", replayedSnapshot, err)
	}
	profile, err := s.CandidateProfileByID(fixture.ProfileID)
	if err != nil || profile == nil ||
		profile.MainStatus != CandidateProfileSelected ||
		profile.SuccessfulGreetingIntentID != nil ||
		profile.ResumeCaptureState != ResumeCaptureCaptured ||
		profile.ActiveResumeSnapshotID == nil ||
		*profile.ActiveResumeSnapshotID != snapshot.SnapshotID {
		t.Fatalf("主动来聊档案补采投影错误: profile=%+v err=%v", profile, err)
	}
	if err := s.db.Model(&M5TrialSelection{}).Count(&trialN).Error; err != nil || trialN != 0 {
		t.Fatalf("补采完成后仍不得产生试运行选择: count=%d err=%v", trialN, err)
	}
	target, err = s.InboundResumeCaptureTarget(fixture.ProfileID)
	if err != nil || target != nil {
		t.Fatalf("已补采档案不得继续枚举为待补采目标: target=%+v err=%v", target, err)
	}
}

func TestInboundSelectedResumeCaptureRejectsIncompleteFactChains(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *Store, resumeStoreFixture)
	}{
		{
			name: "requestedBy is not inbound adoption",
			mutate: func(t *testing.T, s *Store, fixture resumeStoreFixture) {
				t.Helper()
				if err := s.db.Model(&TrackedIntent{}).
					Where("platform = ? AND account_ref = ? AND conversation_ref = ?",
						fixture.Platform, fixture.AccountRef, fixture.ConversationRef).
					Update("requested_by", "user").Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "tracking is not adopted",
			mutate: func(t *testing.T, s *Store, fixture resumeStoreFixture) {
				t.Helper()
				if err := s.db.Model(&Conversation{}).
					Where("platform = ? AND account_ref = ? AND conversation_ref = ?",
						fixture.Platform, fixture.AccountRef, fixture.ConversationRef).
					Update("tracking_state", TrackingPending).Error; err != nil {
					t.Fatal(err)
				}
				if err := s.db.Model(&TrackedIntent{}).
					Where("platform = ? AND account_ref = ? AND conversation_ref = ?",
						fixture.Platform, fixture.AccountRef, fixture.ConversationRef).
					Update("status", TrackingPending).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "conversation identity drifted",
			mutate: func(t *testing.T, s *Store, fixture resumeStoreFixture) {
				t.Helper()
				if err := s.db.Model(&Conversation{}).
					Where("platform = ? AND account_ref = ? AND conversation_ref = ?",
						fixture.Platform, fixture.AccountRef, fixture.ConversationRef).
					Update("platform_user_ref", "different-user").Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "backend job id is absent",
			mutate: func(t *testing.T, s *Store, fixture resumeStoreFixture) {
				t.Helper()
				if err := s.db.Model(&CandidateProfile{}).
					Where("profile_id = ?", fixture.ProfileID).
					Update("backend_job_id", nil).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "profile is no longer selected",
			mutate: func(t *testing.T, s *Store, fixture resumeStoreFixture) {
				t.Helper()
				if err := s.db.Model(&CandidateProfile{}).
					Where("profile_id = ?", fixture.ProfileID).
					Update("main_status", CandidateProfileCommunicating).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "greeting intent unexpectedly exists",
			mutate: func(t *testing.T, s *Store, fixture resumeStoreFixture) {
				t.Helper()
				if err := s.db.Model(&CandidateProfile{}).
					Where("profile_id = ?", fixture.ProfileID).
					Update("successful_greeting_intent_id", "unexpected-greeting").Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "stable inbound source key is absent",
			mutate: func(t *testing.T, s *Store, fixture resumeStoreFixture) {
				t.Helper()
				if err := s.db.Model(&Message{}).
					Where("platform = ? AND account_ref = ? AND conversation_ref = ? AND direction = ?",
						fixture.Platform, fixture.AccountRef, fixture.ConversationRef, "in").
					Update("source_key", nil).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "only system-shaped inbound exists",
			mutate: func(t *testing.T, s *Store, fixture resumeStoreFixture) {
				t.Helper()
				if err := s.db.Model(&Message{}).
					Where("platform = ? AND account_ref = ? AND conversation_ref = ? AND direction = ?",
						fixture.Platform, fixture.AccountRef, fixture.ConversationRef, "in").
					Update("kind", "system").Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "active outbound already exists",
			mutate: func(t *testing.T, s *Store, fixture resumeStoreFixture) {
				t.Helper()
				if err := s.db.Create(&Message{
					Platform: fixture.Platform, AccountRef: fixture.AccountRef,
					ConversationRef: fixture.ConversationRef, Seq: 2,
					Direction: "out", Kind: "text",
					ContentHash: strings.Repeat("d", 64), Origin: "self",
				}).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := openTest(t)
			fixture := seedInboundResumeStoreFixture(
				t,
				s,
				time.Date(2026, 7, 26, 11, 0, 0, 0, time.UTC),
			)
			test.mutate(t, s, fixture)
			target, targetErr := s.InboundResumeCaptureTarget(fixture.ProfileID)
			if target != nil ||
				(targetErr == nil && test.name != "profile is no longer selected" &&
					test.name != "greeting intent unexpectedly exists") {
				t.Fatalf("不完整事实链不得被枚举为目标: target=%+v err=%v", target, targetErr)
			}
			result, err := s.CreateResumeCaptureCmd(CreateResumeCaptureCmdRequest{
				ProfileID: fixture.ProfileID,
				Command: resumeCaptureCommand(
					t,
					fixture,
					"resume-root-rejected-"+strings.ReplaceAll(test.name, " ", "-"),
				),
			})
			if result != nil ||
				(!errors.Is(err, ErrResumeCaptureNotAllowed) &&
					!errors.Is(err, ErrResumeCaptureBinding)) {
				t.Fatalf("不完整主动来聊事实链必须拒绝: result=%+v err=%v", result, err)
			}
			var commandN, trialN int64
			if err := s.db.Model(&CmdRecord{}).
				Where("name = ?", protocol.PrimCandidateReadResume).
				Count(&commandN).Error; err != nil {
				t.Fatal(err)
			}
			if err := s.db.Model(&M5TrialSelection{}).Count(&trialN).Error; err != nil {
				t.Fatal(err)
			}
			profile, profileErr := s.CandidateProfileByID(fixture.ProfileID)
			if commandN != 0 || trialN != 0 || profileErr != nil || profile == nil ||
				profile.ResumeCaptureState != ResumeCaptureUnattempted {
				t.Fatalf(
					"拒绝路径不得留下命令、试运行或状态推进: cmd=%d trial=%d profile=%+v err=%v",
					commandN,
					trialN,
					profile,
					profileErr,
				)
			}
		})
	}
}

func TestInboundSelectedResumeCaptureCompletionRechecksActiveLedger(t *testing.T) {
	s := openTest(t)
	fixture := seedInboundResumeStoreFixture(
		t,
		s,
		time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
	)
	root := "resume-root-inbound-drift"
	if _, err := s.CreateResumeCaptureCmd(CreateResumeCaptureCmdRequest{
		ProfileID: fixture.ProfileID,
		Command:   resumeCaptureCommand(t, fixture, root),
	}); err != nil {
		t.Fatal(err)
	}
	data := resumeData(fixture)
	settleResumeCaptureOK(t, s, root, data)
	if err := s.db.Create(&Message{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		ConversationRef: fixture.ConversationRef, Seq: 2,
		Direction: "out", Kind: "text",
		ContentHash: strings.Repeat("e", 64), Origin: "self",
	}).Error; err != nil {
		t.Fatal(err)
	}

	snapshot, err := s.CompleteResumeCapture(CompleteResumeCaptureRequest{
		ProfileID: fixture.ProfileID, LogicalDispatchID: root,
		SnapshotID: "snapshot-inbound-drift", Data: data,
	})
	if snapshot != nil || !errors.Is(err, ErrResumeCaptureNotAllowed) {
		t.Fatalf("完成前出现活动 outbound 必须停止收编: snapshot=%+v err=%v", snapshot, err)
	}
	profile, queryErr := s.CandidateProfileByID(fixture.ProfileID)
	if queryErr != nil || profile == nil ||
		profile.ResumeCaptureState != ResumeCaptureManualRequired ||
		profile.ActiveResumeSnapshotID != nil ||
		profile.ResumeCaptureFailureReason != "bindingChanged" {
		t.Fatalf("绑定变化未收敛为档案级人工: profile=%+v err=%v", profile, queryErr)
	}
	var snapshotN, trialN int64
	if err := s.db.Model(&CandidateResumeSnapshot{}).Count(&snapshotN).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&M5TrialSelection{}).Count(&trialN).Error; err != nil {
		t.Fatal(err)
	}
	if snapshotN != 0 || trialN != 0 {
		t.Fatalf("失败收编不得留下快照或试运行选择: snapshots=%d trials=%d", snapshotN, trialN)
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

func TestM5TrialSelectionCannotReactivateManualRequiredProfile(t *testing.T) {
	s := openTest(t)
	fixture := seedResumeStoreFixture(t, s, "profile-trial-manual")
	root := "resume-root-trial-manual"
	if _, err := s.CreateResumeCaptureCmd(CreateResumeCaptureCmdRequest{
		ProfileID: fixture.ProfileID, Command: resumeCaptureCommand(t, fixture, root),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.FailResumeCapture(FailResumeCaptureRequest{
		ProfileID: fixture.ProfileID, LogicalDispatchID: root, Reason: "fixtureFailure",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectM5TrialProfile(
		fixture.ProfileID, "selection-manual-repeat", "user", time.Now(),
	); !errors.Is(err, ErrResumeCaptureNotAllowed) {
		t.Fatalf("manualRequired 档案不得重新占用试运行槽: %v", err)
	}
	var active int64
	if err := s.db.Model(&M5TrialSelection{}).
		Where("status = ? AND active_slot = ?", M5TrialSelectionActive, m5TrialActiveSlot).
		Count(&active).Error; err != nil || active != 0 {
		t.Fatalf("失败档案重选后 active slot 必须保持释放: count=%d err=%v", active, err)
	}
}
