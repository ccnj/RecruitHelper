package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func textPointer(value string) *string { return &value }

func createM4Account(t *testing.T, s *Store, platform, accountRef string) {
	t.Helper()
	if err := s.CreateAccount(&Account{Platform: platform, AccountRef: accountRef}); err != nil {
		t.Fatal(err)
	}
}

func candidateSelection(
	profileID, platform, accountRef, userRef, positionRef, displayName, positionTitle string,
	at time.Time,
) SelectCandidateProfileRequest {
	return SelectCandidateProfileRequest{
		ProfileID: profileID,
		Scope: CandidateProfileScope{
			Platform: platform, AccountRef: accountRef, PlatformUserRef: userRef, PositionRef: positionRef,
		},
		DisplayName: textPointer(displayName), PositionTitle: textPointer(positionTitle), ObservedAt: at,
	}
}

func TestCandidateProfileSchemaFreezesIdentityAndNoDeleteSemantics(t *testing.T) {
	s := openTest(t)

	type tableColumn struct {
		Name string
		PK   int `gorm:"column:pk"`
	}
	for table, expectedPrimary := range map[string]map[string]bool{
		"candidates":         {"platform": false, "platform_user_ref": false},
		"candidate_profiles": {"profile_id": false},
	} {
		var columns []tableColumn
		if err := s.db.Raw("PRAGMA table_info('" + table + "')").Scan(&columns).Error; err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, column := range columns {
			seen[column.Name] = true
			if _, ok := expectedPrimary[column.Name]; ok && column.PK > 0 {
				expectedPrimary[column.Name] = true
			}
		}
		for name, primary := range expectedPrimary {
			if !primary {
				t.Fatalf("%s 主键缺少 %s: %+v", table, name, columns)
			}
		}
		for _, forbidden := range []string{"resume_number", "deleted_at"} {
			if seen[forbidden] {
				t.Fatalf("%s 不得出现 %s", table, forbidden)
			}
		}
	}

	for indexName, wantParts := range map[string][]string{
		"ux_candidate_profile_identity":     {"platform", "account_ref", "platform_user_ref", "position_ref"},
		"ux_candidate_profile_conversation": {"platform", "account_ref", "conversation_ref"},
		"ux_candidate_profile_active":       {"platform", "platform_user_ref"},
	} {
		var sql string
		if err := s.db.Raw("SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?", indexName).
			Scan(&sql).Error; err != nil {
			t.Fatal(err)
		}
		normalized := strings.ToLower(strings.Join(strings.Fields(sql), " "))
		if normalized == "" {
			t.Fatalf("缺少索引 %s", indexName)
		}
		for _, part := range wantParts {
			if !strings.Contains(normalized, "`"+part+"`") && !strings.Contains(normalized, part) {
				t.Fatalf("索引 %s 缺少列 %s: %s", indexName, part, sql)
			}
		}
		if indexName == "ux_candidate_profile_active" &&
			!strings.Contains(normalized, "where main_status <> 'eliminated'") &&
			!strings.Contains(normalized, "where `main_status` <> 'eliminated'") {
			t.Fatalf("人级建档闸必须是非 eliminated 部分唯一索引: %s", sql)
		}
	}

	type foreignKeyRow struct {
		Table    string `gorm:"column:table"`
		From     string `gorm:"column:from"`
		To       string `gorm:"column:to"`
		OnUpdate string `gorm:"column:on_update"`
		OnDelete string `gorm:"column:on_delete"`
	}
	var foreignKeys []foreignKeyRow
	if err := s.db.Raw("PRAGMA foreign_key_list('candidate_profiles')").Scan(&foreignKeys).Error; err != nil {
		t.Fatal(err)
	}
	wantForeignKeys := map[string]string{"platform": "platform", "platform_user_ref": "platform_user_ref"}
	for _, row := range foreignKeys {
		if row.Table != "candidates" {
			continue
		}
		if wantForeignKeys[row.From] == row.To {
			delete(wantForeignKeys, row.From)
		}
		if row.OnDelete != "RESTRICT" || row.OnUpdate != "RESTRICT" {
			t.Fatalf("候选人外键不得 cascade/set null: %+v", row)
		}
	}
	if len(wantForeignKeys) != 0 {
		t.Fatalf("CandidateProfile 缺少复合 Candidate 外键: rows=%+v missing=%+v", foreignKeys, wantForeignKeys)
	}
}

func TestCandidateIdentityIncludesPlatformAndProfileUpsertIsIdempotent(t *testing.T) {
	s := openTest(t)
	createM4Account(t, s, "zhilian", "account-a")
	createM4Account(t, s, "other", "account-a")
	firstAt := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)

	first, err := s.SelectCandidateProfile(candidateSelection(
		"profile-a", "zhilian", "account-a", "same-user", "position-a", "旧名字", "旧职位名", firstAt,
	))
	if err != nil || !first.CandidateCreated || !first.ProfileCreated {
		t.Fatalf("首次建档: result=%+v err=%v", first, err)
	}
	retried, err := s.SelectCandidateProfile(candidateSelection(
		"discarded-retry-profile-id", "zhilian", "account-a", "same-user", "position-a", "新名字", "新职位名", firstAt.Add(time.Minute),
	))
	if err != nil || retried.CandidateCreated || retried.ProfileCreated || retried.Profile.ProfileID != "profile-a" {
		t.Fatalf("同 scope 重试必须收编原档案: result=%+v err=%v", retried, err)
	}
	if retried.Candidate.DisplayName == nil || *retried.Candidate.DisplayName != "新名字" ||
		retried.Candidate.FirstSeenAt != firstAt || !retried.Candidate.LastSeenAt.Equal(firstAt.Add(time.Minute)) ||
		retried.Profile.PositionTitle == nil || *retried.Profile.PositionTitle != "新职位名" ||
		retried.Profile.MainStatus != CandidateProfileSelected {
		t.Fatalf("幂等重读只应刷新展示快照: candidate=%+v profile=%+v", retried.Candidate, retried.Profile)
	}

	crossPlatform, err := s.SelectCandidateProfile(candidateSelection(
		"profile-other", "other", "account-a", "same-user", "position-a", "另平台同值", "职位", firstAt,
	))
	if err != nil || !crossPlatform.CandidateCreated || !crossPlatform.ProfileCreated {
		t.Fatalf("相同 userRef 跨平台不得冲突: result=%+v err=%v", crossPlatform, err)
	}
	var candidates, profiles int64
	if err := s.db.Model(&Candidate{}).Count(&candidates).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&CandidateProfile{}).Count(&profiles).Error; err != nil {
		t.Fatal(err)
	}
	if candidates != 2 || profiles != 2 {
		t.Fatalf("身份/档案发生增生: candidates=%d profiles=%d", candidates, profiles)
	}
}

func TestCandidateProfileUpsertNeverResetsExistingState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status CandidateProfileStatus
	}{
		{name: "greeted", status: CandidateProfileGreeted},
		{name: "communicating", status: CandidateProfileCommunicating},
		{name: "invited", status: CandidateProfileInvited},
		{name: "interviewed", status: CandidateProfileInterviewed},
		{name: "ended", status: CandidateProfileEnded},
		{name: "eliminated", status: CandidateProfileEliminated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTest(t)
			createM4Account(t, s, "zhilian", "account-state")
			at := time.Date(2026, 7, 20, 11, 0, 0, 0, time.UTC)
			created, err := s.SelectCandidateProfile(candidateSelection(
				"profile-state", "zhilian", "account-state", "user-state", "position-state", "候选人", "初始职位", at,
			))
			if err != nil {
				t.Fatal(err)
			}
			endReason := CandidateProfileEndGreetingFailed
			intentID, conversationRef := "successful-intent", "conversation-state"
			greetedAt := at.Add(time.Minute)
			updates := map[string]any{
				"main_status": tc.status, "end_reason": &endReason,
				"successful_greeting_intent_id": &intentID, "conversation_ref": &conversationRef,
				"greeted_at": &greetedAt,
			}
			if err := s.db.Model(&CandidateProfile{}).Where("profile_id = ?", created.Profile.ProfileID).Updates(updates).Error; err != nil {
				t.Fatal(err)
			}
			got, err := s.SelectCandidateProfile(candidateSelection(
				"new-id-must-be-ignored", "zhilian", "account-state", "user-state", "position-state", "改名", "更新职位", at.Add(2*time.Minute),
			))
			if err != nil {
				t.Fatal(err)
			}
			profile := got.Profile
			if got.ProfileCreated || profile.ProfileID != "profile-state" || profile.MainStatus != tc.status ||
				profile.EndReason == nil || *profile.EndReason != endReason ||
				profile.SuccessfulGreetingIntentID == nil || *profile.SuccessfulGreetingIntentID != intentID ||
				profile.ConversationRef == nil || *profile.ConversationRef != conversationRef ||
				profile.GreetedAt == nil || !profile.GreetedAt.Equal(greetedAt) ||
				profile.PositionTitle == nil || *profile.PositionTitle != "更新职位" {
				t.Fatalf("重复建档不得复活/重置终态，只能更新标题: %+v", profile)
			}
		})
	}
}

func TestPersonLevelProfileGateSeesEndedAcrossAccounts(t *testing.T) {
	for _, status := range []CandidateProfileStatus{
		CandidateProfileSelected, CandidateProfileGreeted, CandidateProfileCommunicating, CandidateProfileEnded,
	} {
		t.Run(string(status), func(t *testing.T) {
			s := openTest(t)
			createM4Account(t, s, "zhilian", "account-one")
			createM4Account(t, s, "zhilian", "account-two")
			at := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
			first, err := s.SelectCandidateProfile(candidateSelection(
				"profile-first", "zhilian", "account-one", "person-one", "position-one", "原名", "职位一", at,
			))
			if err != nil {
				t.Fatal(err)
			}
			if err := s.db.Model(&CandidateProfile{}).Where("profile_id = ?", first.Profile.ProfileID).
				Update("main_status", status).Error; err != nil {
				t.Fatal(err)
			}
			_, err = s.SelectCandidateProfile(candidateSelection(
				"profile-second", "zhilian", "account-two", "person-one", "position-two", "不应落库的新名", "职位二", at.Add(time.Hour),
			))
			if !errors.Is(err, ErrCandidateAlreadyProfiled) {
				t.Fatalf("%s 必须跨账号阻止第二职位: %v", status, err)
			}
			candidate, _ := s.CandidateByKey(CandidateKey{Platform: "zhilian", PlatformUserRef: "person-one"})
			if candidate.DisplayName == nil || *candidate.DisplayName != "原名" || !candidate.LastSeenAt.Equal(at) {
				t.Fatalf("建档闸失败不得更新候选人快照: %+v", candidate)
			}
			var profiles int64
			_ = s.db.Model(&CandidateProfile{}).Count(&profiles).Error
			if profiles != 1 {
				t.Fatalf("建档闸后档案增生: %d", profiles)
			}
		})
	}
}

func TestEliminatedAllowsAnotherPositionButDoesNotReviveSameScope(t *testing.T) {
	s := openTest(t)
	createM4Account(t, s, "zhilian", "account-eliminated")
	at := time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)
	first, err := s.SelectCandidateProfile(candidateSelection(
		"profile-eliminated", "zhilian", "account-eliminated", "person-eliminated", "position-old", "候选人", "旧职位", at,
	))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&CandidateProfile{}).Where("profile_id = ?", first.Profile.ProfileID).
		Update("main_status", CandidateProfileEliminated).Error; err != nil {
		t.Fatal(err)
	}
	same, err := s.SelectCandidateProfile(candidateSelection(
		"must-not-replace", "zhilian", "account-eliminated", "person-eliminated", "position-old", "候选人", "旧职位改名", at.Add(time.Minute),
	))
	if err != nil || same.Profile.ProfileID != "profile-eliminated" || same.Profile.MainStatus != CandidateProfileEliminated {
		t.Fatalf("同职位 eliminated 不得复活: result=%+v err=%v", same, err)
	}
	other, err := s.SelectCandidateProfile(candidateSelection(
		"profile-new-position", "zhilian", "account-eliminated", "person-eliminated", "position-new", "候选人", "新职位", at.Add(2*time.Minute),
	))
	if err != nil || !other.ProfileCreated || other.Profile.MainStatus != CandidateProfileSelected {
		t.Fatalf("只有 eliminated 允许新职位建档: result=%+v err=%v", other, err)
	}
}

func TestCandidateProfileCreateFailureRollsBackCandidateAndSnapshots(t *testing.T) {
	s := openTest(t)
	createM4Account(t, s, "zhilian", "account-rollback")
	forced := errors.New("forced candidate profile create failure")
	callbackName := "test:fail_candidate_profile_create"
	if err := s.db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "CandidateProfile" {
			tx.AddError(forced)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer s.db.Callback().Create().Remove(callbackName)

	_, err := s.SelectCandidateProfile(candidateSelection(
		"profile-rollback", "zhilian", "account-rollback", "person-rollback", "position-rollback", "不应留下", "不应留下", time.Now(),
	))
	if !errors.Is(err, forced) {
		t.Fatalf("应返回注入失败: %v", err)
	}
	candidate, candidateErr := s.CandidateByKey(CandidateKey{Platform: "zhilian", PlatformUserRef: "person-rollback"})
	profile, profileErr := s.CandidateProfileByID("profile-rollback")
	if candidateErr != nil || profileErr != nil || candidate != nil || profile != nil {
		t.Fatalf("profile 写失败必须回滚同事务 Candidate: candidate=%+v/%v profile=%+v/%v", candidate, candidateErr, profile, profileErr)
	}
}

func TestCandidateProfileConversationBindingUniqueAndNullable(t *testing.T) {
	s := openTest(t)
	createM4Account(t, s, "zhilian", "account-binding")
	at := time.Now()
	for i, userRef := range []string{"person-a", "person-b"} {
		profileID := []string{"profile-a", "profile-b"}[i]
		if _, err := s.SelectCandidateProfile(candidateSelection(
			profileID, "zhilian", "account-binding", userRef, "position", "候选人", "职位", at,
		)); err != nil {
			t.Fatal(err)
		}
	}
	// 两条未绑定档案必须都能存在；NULL 不参与唯一冲突。
	conversationRef := "conversation-one"
	if err := s.db.Model(&CandidateProfile{}).Where("profile_id = ?", "profile-a").
		Update("conversation_ref", &conversationRef).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&CandidateProfile{}).Where("profile_id = ?", "profile-b").
		Update("conversation_ref", &conversationRef).Error; err == nil {
		t.Fatal("同账号 conversationRef 不得绑定两个档案")
	}
	second, err := s.CandidateProfileByID("profile-b")
	if err != nil || second.ConversationRef != nil {
		t.Fatalf("唯一键失败不得写入第二绑定: profile=%+v err=%v", second, err)
	}
}

func TestM4SchemaDoesNotBackfillExistingConversations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	createM4Account(t, s, "zhilian", "account-existing")
	key := ConversationKey{Platform: "zhilian", AccountRef: "account-existing", ConversationRef: "conversation-existing"}
	if err := s.SaveConversationList(SaveConversationListRequest{
		Platform: key.Platform, AccountRef: key.AccountRef, Complete: true,
		Entries: []ListIndexEntry{{ConversationRef: key.ConversationRef, PlatformUserRef: "person-existing"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	conversation, err := reopened.ConversationByKey(key)
	if err != nil || conversation == nil || conversation.PlatformUserRef != "person-existing" {
		t.Fatalf("M1-M3 会话升级后丢失: conversation=%+v err=%v", conversation, err)
	}
	var candidates, profiles int64
	if err := reopened.db.Model(&Candidate{}).Count(&candidates).Error; err != nil {
		t.Fatal(err)
	}
	if err := reopened.db.Model(&CandidateProfile{}).Count(&profiles).Error; err != nil {
		t.Fatal(err)
	}
	if candidates != 0 || profiles != 0 {
		t.Fatalf("既有 untracked 会话不得自动回填候选人档案: candidates=%d profiles=%d", candidates, profiles)
	}
}
