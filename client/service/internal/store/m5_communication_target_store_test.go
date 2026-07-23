package store

import (
	"errors"
	"testing"
	"time"
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
		target.Aggregate.RootGreetingIntentID != first.GreetingIntent ||
		target.ContextBinding.RevisionHash != target.ContextRevision.RevisionHash ||
		target.ResumeSnapshot.ProfileID != first.ProfileID {
		t.Fatalf("完整沟通目标事实不一致: target=%+v", target)
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

func TestCommunicationTargetRejectsDanglingContextOrResume(t *testing.T) {
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
		if targets != nil || !errors.Is(err, ErrCommunicationTargetConflict) {
			t.Fatalf("悬空职位上下文必须阻断: targets=%+v err=%v", targets, err)
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
		if targets != nil || !errors.Is(err, ErrCommunicationTargetConflict) {
			t.Fatalf("错会话简历必须阻断: targets=%+v err=%v", targets, err)
		}
	})
}

func TestCommunicationTargetsSkipNormalPreparationGapsAndIgnoreTrials(t *testing.T) {
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
	if err != nil || len(targets) != 1 || targets[0].Profile.ProfileID != ready.ProfileID {
		t.Fatalf("未就绪档案或历史 trial 干扰生产目标: targets=%+v err=%v", targets, err)
	}
}
