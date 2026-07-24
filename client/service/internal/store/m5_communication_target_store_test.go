package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/communication"
)

func seedReadyCommunicationTarget(
	t *testing.T,
	s *Store,
	profileID string,
) resumeStoreFixture {
	t.Helper()
	fixture, _ := seedCommunicationSourcingResume(t, s, profileID)
	result, err := s.ReuseSourcingResumeForCommunicationProfile(profileID, time.Now())
	if err != nil || result.Status != SourcingResumeReuseAdopted {
		t.Fatalf("构造完整沟通目标失败: result=%+v err=%v", result, err)
	}
	return fixture
}

func moveReadyCommunicationTargetAccount(
	t *testing.T,
	s *Store,
	fixture resumeStoreFixture,
	accountRef string,
) resumeStoreFixture {
	t.Helper()
	oldAccountRef := fixture.AccountRef
	if oldAccountRef == accountRef {
		return fixture
	}
	updates := []struct {
		model any
		where string
		args  []any
	}{
		{&EffectIntent{}, "intent_id = ?", []any{fixture.GreetingIntent}},
		{&Message{}, "platform = ? AND account_ref = ? AND conversation_ref = ?",
			[]any{fixture.Platform, oldAccountRef, fixture.ConversationRef}},
		{&Conversation{}, "platform = ? AND account_ref = ? AND conversation_ref = ?",
			[]any{fixture.Platform, oldAccountRef, fixture.ConversationRef}},
		{&TrackedIntent{}, "platform = ? AND account_ref = ? AND conversation_ref = ?",
			[]any{fixture.Platform, oldAccountRef, fixture.ConversationRef}},
		{&CandidateProfile{}, "profile_id = ?", []any{fixture.ProfileID}},
	}
	for _, update := range updates {
		result := s.db.Model(update.model).Where(update.where, update.args...).
			UpdateColumn("account_ref", accountRef)
		if result.Error != nil || result.RowsAffected != 1 {
			t.Fatalf("迁移同账号测试事实失败: model=%T rows=%d err=%v",
				update.model, result.RowsAffected, result.Error)
		}
	}
	fixture.AccountRef = accountRef
	return fixture
}

func TestCommunicationTargetsRequireCompleteFactsAndUseStableOrder(t *testing.T) {
	s := openTest(t)
	first := seedReadyCommunicationTarget(t, s, "profile-target-a")
	second := seedReadyCommunicationTarget(t, s, "profile-target-z")
	second = moveReadyCommunicationTargetAccount(t, s, second, first.AccountRef)
	key := AccountKey{Platform: first.Platform, AccountRef: first.AccountRef}

	targets, err := s.CommunicationTargetsForAccount(key)
	if err != nil || len(targets) != 2 ||
		targets[0].Profile.ProfileID != first.ProfileID ||
		targets[1].Profile.ProfileID != second.ProfileID {
		t.Fatalf("沟通目标排序或范围错误: targets=%+v err=%v", targets, err)
	}
	target := targets[0]
	if target.Profile.ProfileID != first.ProfileID ||
		target.Account.Platform != key.Platform ||
		target.Conversation.ConversationRef != first.ConversationRef ||
		target.Aggregate.RootGreetingIntentID != first.GreetingIntent {
		t.Fatalf("基础沟通目标事实不一致: target=%+v", target)
	}
	material, ready, err := s.CommunicationAIMaterialForProfile(first.ProfileID)
	if err != nil || !ready ||
		material.ContextBinding.RevisionHash != material.ContextRevision.RevisionHash ||
		material.ResumeSnapshot.ProfileID != first.ProfileID {
		t.Fatalf("按需 AI 材料不一致: material=%+v ready=%v err=%v",
			material, ready, err)
	}

	if err := s.db.Model(&CommunicationV4Aggregate{}).
		Where("profile_id = ?", second.ProfileID).
		Updates(map[string]any{
			"automation_status":  ProfileCommunicationAutomationManualRequired,
			"manual_reason":      "fixtureManual",
			"manual_required_at": time.Now(),
		}).Error; err != nil {
		t.Fatal(err)
	}
	targets, err = s.CommunicationTargetsForAccount(key)
	if err != nil || len(targets) != 1 || targets[0].Profile.ProfileID != first.ProfileID {
		t.Fatalf("转人工档案不应进入自动目标: targets=%+v err=%v", targets, err)
	}
}

func TestCommunicationAIMaterialRejectsClaimedDanglingContextOrResume(t *testing.T) {
	t.Run("context revision mismatch", func(t *testing.T) {
		s := openTest(t)
		fixture := seedReadyCommunicationTarget(t, s, "profile-target-context-conflict")
		if err := s.db.Model(&ProfileAIContextBinding{}).
			Where("profile_id = ? AND status = ?", fixture.ProfileID, ProfileAIContextBindingActive).
			Update("context_id", "wrong-context").Error; err != nil {
			t.Fatal(err)
		}
		targets, err := s.CommunicationTargetsForAccount(AccountKey{
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		})
		if err != nil || len(targets) != 1 || targets[0].Profile.ProfileID != fixture.ProfileID {
			t.Fatalf("AI 上下文损坏不得隐藏基础目标: targets=%+v err=%v", targets, err)
		}
		material, ready, err := s.CommunicationAIMaterialForProfile(fixture.ProfileID)
		if ready || !errors.Is(err, ErrCommunicationTargetConflict) {
			t.Fatalf("悬空职位上下文必须在按需加载时报错: material=%+v ready=%v err=%v",
				material, ready, err)
		}
	})

	t.Run("resume conversation mismatch", func(t *testing.T) {
		s := openTest(t)
		fixture := seedReadyCommunicationTarget(t, s, "profile-target-resume-conflict")
		if err := s.db.Model(&CandidateResumeSnapshot{}).
			Where("profile_id = ?", fixture.ProfileID).
			Update("source_conversation_ref", "wrong-conversation").Error; err != nil {
			t.Fatal(err)
		}
		targets, err := s.CommunicationTargetsForAccount(AccountKey{
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		})
		if err != nil || len(targets) != 1 || targets[0].Profile.ProfileID != fixture.ProfileID {
			t.Fatalf("简历损坏不得隐藏基础目标: targets=%+v err=%v", targets, err)
		}
		material, ready, err := s.CommunicationAIMaterialForProfile(fixture.ProfileID)
		if ready || !errors.Is(err, ErrCommunicationTargetConflict) {
			t.Fatalf("错会话简历必须在按需加载时报错: material=%+v ready=%v err=%v",
				material, ready, err)
		}
	})
}

func TestCommunicationTargetsIncludeNormalAIPreparationGapsAndIgnoreTrials(t *testing.T) {
	s := openTest(t)
	ready := seedReadyCommunicationTarget(t, s, "profile-target-ready")
	waiting := seedReadyCommunicationTarget(t, s, "profile-target-waiting")
	waiting = moveReadyCommunicationTargetAccount(t, s, waiting, ready.AccountRef)
	if err := s.db.Model(&CandidateProfile{}).
		Where("profile_id = ?", waiting.ProfileID).
		Update("resume_capture_state", ResumeCaptureUnattempted).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&M5TrialSelection{}).
		Where("profile_id = ?", ready.ProfileID).
		Updates(map[string]any{
			"status":      M5TrialSelectionManualRequired,
			"active_slot": nil,
			"reason":      "historicalFixture",
			"ended_at":    time.Now(),
		}).Error; err != nil {
		t.Fatal(err)
	}

	targets, err := s.CommunicationTargetsForAccount(AccountKey{
		Platform: ready.Platform, AccountRef: ready.AccountRef,
	})
	if err != nil || len(targets) != 2 ||
		targets[0].Profile.ProfileID != ready.ProfileID ||
		targets[1].Profile.ProfileID != waiting.ProfileID {
		t.Fatalf("AI 未就绪档案仍须进入基础目标且历史 trial 不得干扰: targets=%+v err=%v",
			targets, err)
	}
	material, materialReady, err := s.CommunicationAIMaterialForProfile(waiting.ProfileID)
	if err != nil || materialReady {
		t.Fatalf("简历正常准备缺口应返回未就绪: material=%+v ready=%v err=%v",
			material, materialReady, err)
	}
}

func TestCommunicationAIMaterialMissingActiveContextIsNormalPreparationGap(t *testing.T) {
	s := openTest(t)
	fixture := seedReadyCommunicationTarget(t, s, "profile-target-context-waiting")
	if err := s.db.Model(&ProfileAIContextBinding{}).
		Where("profile_id = ? AND status = ?", fixture.ProfileID, ProfileAIContextBindingActive).
		Updates(map[string]any{
			"status": ProfileAIContextBindingSuperseded,
			"reason": "fixtureWaiting",
		}).Error; err != nil {
		t.Fatal(err)
	}
	targets, err := s.CommunicationTargetsForAccount(AccountKey{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
	})
	if err != nil || len(targets) != 1 || targets[0].Profile.ProfileID != fixture.ProfileID {
		t.Fatalf("职位上下文准备缺口不得隐藏基础目标: targets=%+v err=%v", targets, err)
	}
	material, ready, err := s.CommunicationAIMaterialForProfile(fixture.ProfileID)
	if err != nil || ready {
		t.Fatalf("无活动职位上下文应返回未就绪: material=%+v ready=%v err=%v",
			material, ready, err)
	}
}

func TestCommunicationTargetsIncludeEndedForWakeupButExcludeEliminated(t *testing.T) {
	t.Run("ended remains an event target", func(t *testing.T) {
		s := openTest(t)
		fixture := seedReadyCommunicationTarget(t, s, "profile-target-ended")
		aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
		if err != nil {
			t.Fatal(err)
		}
		archiveAt := aggregate.State.LastBodyAt.Add(8 * 24 * time.Hour)
		result, err := s.ApplyCommunicationV4ArchiveAction(
			communicationV4ArchiveRequestForTest(t, s, *aggregate, archiveAt, false),
		)
		if err != nil || result == nil || !result.Applied ||
			result.Aggregate.State.MainStatus != communication.V4StatusEnded {
			t.Fatalf("构造已结束档案失败: aggregate=%+v applied=%v err=%v",
				result, result != nil && result.Applied, err)
		}

		targets, err := s.CommunicationTargetsForAccount(AccountKey{
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		})
		if err != nil || len(targets) != 1 ||
			targets[0].Profile.ProfileID != fixture.ProfileID ||
			targets[0].Aggregate.State.MainStatus != communication.V4StatusEnded {
			t.Fatalf("已结束档案没有保留为事件层目标: targets=%+v err=%v", targets, err)
		}
	})

	t.Run("eliminated stays terminal", func(t *testing.T) {
		s := openTest(t)
		fixture := seedReadyCommunicationTarget(t, s, "profile-target-eliminated")
		aggregate, err := s.CommunicationV4AggregateByProfile(fixture.ProfileID)
		if err != nil {
			t.Fatal(err)
		}
		aggregate.State.MainStatus = communication.V4StatusEliminated
		aggregate.State.EndReason = ""
		stateJSON, err := json.Marshal(aggregate.State)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.db.Model(&CommunicationV4Aggregate{}).
			Where("profile_id = ?", fixture.ProfileID).
			Update("state", string(stateJSON)).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.db.Model(&CandidateProfile{}).
			Where("profile_id = ?", fixture.ProfileID).
			Updates(map[string]any{
				"main_status": CandidateProfileEliminated,
				"end_reason":  nil,
			}).Error; err != nil {
			t.Fatal(err)
		}

		targets, err := s.CommunicationTargetsForAccount(AccountKey{
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		})
		if err != nil || len(targets) != 0 {
			t.Fatalf("已淘汰档案不得进入自动事件层: targets=%+v err=%v", targets, err)
		}
	})
}
