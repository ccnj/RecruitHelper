package store

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
)

func contextRevisionFixture(contextID, revisionHash string, at time.Time) m5ai.ContextRevision {
	replyPrompt := "合成回复:{简历}/{推荐时段}/{对话历史}/{话术_序列}"
	intentPrompt := "合成意向:{回复}/{招呼语}"
	facts := "fixture://customer-facts"
	documents := []m5ai.JobConfigDocument{
		{DocType: "多轮沟通", Content: replyPrompt},
		{DocType: "客户事实库", Content: facts},
		{DocType: "意向判断", Content: intentPrompt},
		{DocType: "未使用加法文档", Content: "fixture://opaque-document"},
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].DocType < documents[j].DocType })
	return m5ai.ContextRevision{
		ContextID: contextID, RevisionHash: revisionHash,
		SourceKind: "localImport", SourceJobRef: "17",
		DisplayName: "合成职位", Environment: "online",
		SourcePackage: m5ai.JobConfigDocumentPackage{Documents: documents},
		Communication: m5ai.CommunicationView{
			ReplyPrompt: replyPrompt, IntentPrompt: intentPrompt,
			CustomerFacts: facts, MappingVersion: m5ai.MappingVersion,
		},
		CreatedAt: at,
	}
}

func TestJobAIContextRevisionIsImmutableAndJSONRoundTrips(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	revision := contextRevisionFixture("context-one", "revision-one", at)

	stored, created, err := s.SaveJobAIContextRevision(revision)
	if err != nil || !created || stored.RevisionHash != revision.RevisionHash {
		t.Fatalf("首次保存 revision 失败: stored=%+v created=%v err=%v", stored, created, err)
	}

	var sourcePackageRaw, communicationRaw string
	row := s.db.Raw(
		"SELECT source_package, communication FROM job_ai_context_revisions WHERE revision_hash = ?",
		revision.RevisionHash,
	).Row()
	if err := row.Scan(&sourcePackageRaw, &communicationRaw); err != nil {
		t.Fatal(err)
	}
	for label, raw := range map[string]string{"sourcePackage": sourcePackageRaw, "communication": communicationRaw} {
		var decoded map[string]any
		if json.Unmarshal([]byte(raw), &decoded) != nil || len(decoded) == 0 {
			t.Fatalf("%s 未以 JSON object 持久化: %q", label, raw)
		}
	}
	if !strings.Contains(sourcePackageRaw, "fixture://opaque-document") ||
		!strings.Contains(communicationRaw, "fixture://customer-facts") {
		t.Fatalf("完整来源包或沟通视图未逐字节保存: package=%s view=%s", sourcePackageRaw, communicationRaw)
	}

	repeat := revision
	repeat.CreatedAt = at.Add(time.Hour)
	reused, created, err := s.SaveJobAIContextRevision(repeat)
	if err != nil || created || !reused.CreatedAt.Equal(at) {
		t.Fatalf("相同材料应复用首次不可变 revision: reused=%+v created=%v err=%v", reused, created, err)
	}

	conflict := revision
	conflict.DisplayName = "被篡改职位"
	if _, _, err := s.SaveJobAIContextRevision(conflict); !errors.Is(err, ErrJobAIContextRevisionConflict) {
		t.Fatalf("同 hash 不同材料未冲突: %v", err)
	}

	second := contextRevisionFixture("context-one", "revision-two", at.Add(2*time.Hour))
	second.Communication.ReplyPrompt += "-v2"
	for index := range second.SourcePackage.Documents {
		if second.SourcePackage.Documents[index].DocType == "多轮沟通" {
			second.SourcePackage.Documents[index].Content = second.Communication.ReplyPrompt
		}
	}
	if _, created, err := s.SaveJobAIContextRevision(second); err != nil || !created {
		t.Fatalf("同 context 的新 revision 应追加: created=%v err=%v", created, err)
	}

	summaries, err := s.JobAIContextRevisionSummaries()
	if err != nil || len(summaries) != 2 || summaries[0].RevisionHash != "revision-one" ||
		summaries[1].RevisionHash != "revision-two" || summaries[0].DocumentCount != 4 {
		t.Fatalf("revision 元数据列表错误: summaries=%+v err=%v", summaries, err)
	}
	summaryJSON, _ := json.Marshal(summaries)
	for _, forbidden := range []string{"合成回复", "fixture://customer-facts", "fixture://opaque-document"} {
		if strings.Contains(string(summaryJSON), forbidden) {
			t.Fatalf("普通列表泄漏配置正文 %q: %s", forbidden, summaryJSON)
		}
	}
}

func TestJobAIContextRevisionValidationRejectsNonCanonicalInput(t *testing.T) {
	s := openTest(t)
	revision := contextRevisionFixture("context-invalid", "revision-invalid", time.Now())

	t.Run("mapping version", func(t *testing.T) {
		invalid := revision
		invalid.Communication.MappingVersion = "future-unapproved"
		if _, _, err := s.SaveJobAIContextRevision(invalid); !errors.Is(err, ErrJobAIContextRevisionInvalid) {
			t.Fatalf("未批准 mappingVersion 未拒绝: %v", err)
		}
	})

	t.Run("derived view mismatch", func(t *testing.T) {
		invalid := revision
		invalid.Communication.IntentPrompt = "被篡改"
		if _, _, err := s.SaveJobAIContextRevision(invalid); !errors.Is(err, ErrJobAIContextRevisionInvalid) {
			t.Fatalf("来源包与执行视图不一致未拒绝: %v", err)
		}
	})

	t.Run("document order", func(t *testing.T) {
		invalid := revision
		invalid.SourcePackage.Documents = append([]m5ai.JobConfigDocument(nil), revision.SourcePackage.Documents...)
		invalid.SourcePackage.Documents[0], invalid.SourcePackage.Documents[1] =
			invalid.SourcePackage.Documents[1], invalid.SourcePackage.Documents[0]
		if _, _, err := s.SaveJobAIContextRevision(invalid); !errors.Is(err, ErrJobAIContextRevisionInvalid) {
			t.Fatalf("未排序来源包未拒绝: %v", err)
		}
	})
}

func TestJobAIContextRevisionBatchIsAtomicOnLaterConflict(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	existing := contextRevisionFixture("context-existing", "revision-existing", at)
	if _, _, err := s.SaveJobAIContextRevision(existing); err != nil {
		t.Fatal(err)
	}

	first := contextRevisionFixture("context-batch-new", "revision-batch-new", at.Add(time.Hour))
	conflictingSecond := existing
	conflictingSecond.DisplayName = "同 hash 的冲突材料"
	if _, err := s.SaveJobAIContextRevisions([]m5ai.ContextRevision{first, conflictingSecond}); !errors.Is(err, ErrJobAIContextRevisionConflict) {
		t.Fatalf("批次第二项冲突未返回固定错误: %v", err)
	}
	if persisted, err := s.JobAIContextRevisionByHash(first.RevisionHash); err != nil || persisted != nil {
		t.Fatalf("批次冲突后第一项未回滚: persisted=%+v err=%v", persisted, err)
	}
	if persisted, err := s.JobAIContextRevisionByHash(existing.RevisionHash); err != nil || persisted == nil ||
		persisted.DisplayName != existing.DisplayName {
		t.Fatalf("批次冲突篡改既有 revision: persisted=%+v err=%v", persisted, err)
	}
}

func TestProfileAIContextBindingRebindsWithoutDeletingHistory(t *testing.T) {
	s := openTest(t)
	fixture := seedResumeStoreFixture(t, s, "profile-context-binding")
	firstAt := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	firstRevision := contextRevisionFixture("context-binding", "revision-binding-one", firstAt)
	secondRevision := contextRevisionFixture("context-binding", "revision-binding-two", firstAt.Add(time.Hour))
	secondRevision.Communication.ReplyPrompt += "-v2"
	for index := range secondRevision.SourcePackage.Documents {
		if secondRevision.SourcePackage.Documents[index].DocType == "多轮沟通" {
			secondRevision.SourcePackage.Documents[index].Content = secondRevision.Communication.ReplyPrompt
		}
	}
	for _, revision := range []m5ai.ContextRevision{firstRevision, secondRevision} {
		if _, _, err := s.SaveJobAIContextRevision(revision); err != nil {
			t.Fatal(err)
		}
	}

	first, err := s.BindActiveM5TrialProfileAIContext(BindProfileAIContextRequest{
		BindingID: "binding-one", ProfileID: fixture.ProfileID,
		ContextID: firstRevision.ContextID, RevisionHash: firstRevision.RevisionHash,
		Reason: "userSelected", BoundBy: "user", BoundAt: firstAt,
	})
	if err != nil || first.Status != ProfileAIContextBindingActive {
		t.Fatalf("首次显式绑定失败: binding=%+v err=%v", first, err)
	}

	repeated, err := s.BindActiveM5TrialProfileAIContext(BindProfileAIContextRequest{
		BindingID: "binding-redundant", ProfileID: fixture.ProfileID,
		ContextID: firstRevision.ContextID, RevisionHash: firstRevision.RevisionHash,
		Reason: "userSelected", BoundBy: "user", BoundAt: firstAt.Add(time.Minute),
	})
	if err != nil || repeated.BindingID != first.BindingID {
		t.Fatalf("相同 active revision 应复用既有事实: binding=%+v err=%v", repeated, err)
	}

	secondAt := firstAt.Add(2 * time.Hour)
	second, err := s.BindActiveM5TrialProfileAIContext(BindProfileAIContextRequest{
		BindingID: "binding-two", ProfileID: fixture.ProfileID,
		ContextID: secondRevision.ContextID, RevisionHash: secondRevision.RevisionHash,
		Reason: "userRebound", BoundBy: "user", BoundAt: secondAt,
	})
	if err != nil || second.Status != ProfileAIContextBindingActive {
		t.Fatalf("改绑失败: binding=%+v err=%v", second, err)
	}

	bindings, err := s.ProfileAIContextBindings(fixture.ProfileID)
	if err != nil || len(bindings) != 2 || bindings[0].Status != ProfileAIContextBindingSuperseded ||
		bindings[0].SupersededAt == nil || !bindings[0].SupersededAt.Equal(secondAt) ||
		bindings[0].Reason != profileAIContextReboundReason || bindings[1].Status != ProfileAIContextBindingActive {
		t.Fatalf("改绑未保留两行显式事实: bindings=%+v err=%v", bindings, err)
	}

	active, err := s.ActiveProfileAIContext(fixture.ProfileID)
	if err != nil || active == nil || active.Binding.BindingID != second.BindingID ||
		active.Revision.RevisionHash != secondRevision.RevisionHash ||
		active.Revision.Communication.ReplyPrompt != secondRevision.Communication.ReplyPrompt {
		t.Fatalf("active binding+revision 查询错误: active=%+v err=%v", active, err)
	}
	visible, _ := json.Marshal(active)
	for _, forbidden := range []string{fixture.UserRef, fixture.AccountRef, fixture.ConversationRef} {
		if strings.Contains(string(visible), forbidden) {
			t.Fatalf("active context 查询泄漏候选人原始引用 %q: %s", forbidden, visible)
		}
	}

	// 绕过业务入口直插第二条 active，也必须由数据库部分唯一索引拒绝。
	if err := s.db.Create(&ProfileAIContextBinding{
		BindingID: "binding-illegal-active", ProfileID: fixture.ProfileID,
		ContextID: secondRevision.ContextID, RevisionHash: secondRevision.RevisionHash,
		Status: ProfileAIContextBindingActive, Reason: "illegal", BoundBy: "test", BoundAt: secondAt.Add(time.Minute),
	}).Error; err == nil {
		t.Fatal("数据库唯一闸允许同 profile 出现第二个 active binding")
	}
	var indexSQL string
	if err := s.db.Raw(
		"SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?",
		"ux_profile_ai_context_active",
	).Row().Scan(&indexSQL); err != nil {
		t.Fatal(err)
	}
	normalizedIndexSQL := strings.ToLower(strings.Join(strings.Fields(indexSQL), " "))
	if !strings.Contains(normalizedIndexSQL, "unique index") ||
		!strings.Contains(normalizedIndexSQL, "where status = 'active'") {
		t.Fatalf("active binding 部分唯一索引未按预期创建: %s", indexSQL)
	}
}

func TestProfileAIContextBindingRequiresCurrentActiveTrialAndExactRevision(t *testing.T) {
	s := openTest(t)
	revision := contextRevisionFixture("context-gates", "revision-gates", time.Now())
	if _, _, err := s.SaveJobAIContextRevision(revision); err != nil {
		t.Fatal(err)
	}
	request := BindProfileAIContextRequest{
		BindingID: "binding-gates", ProfileID: "profile-no-trial",
		ContextID: revision.ContextID, RevisionHash: revision.RevisionHash,
		Reason: "userSelected", BoundBy: "user", BoundAt: time.Now(),
	}
	if _, err := s.BindActiveM5TrialProfileAIContext(request); !errors.Is(err, ErrM5TrialNotActive) {
		t.Fatalf("无 active trial 仍可绑定: %v", err)
	}

	fixture := seedResumeStoreFixture(t, s, "profile-context-gates")
	request.ProfileID = "another-profile"
	if _, err := s.BindActiveM5TrialProfileAIContext(request); !errors.Is(err, ErrM5TrialProfileMismatch) {
		t.Fatalf("非当前试运行 profile 仍可绑定: %v", err)
	}

	request.ProfileID = fixture.ProfileID
	request.RevisionHash = "revision-missing"
	if _, err := s.BindActiveM5TrialProfileAIContext(request); !errors.Is(err, ErrJobAIContextRevisionNotFound) {
		t.Fatalf("不存在 revision 仍可绑定: %v", err)
	}

	request.RevisionHash = revision.RevisionHash
	request.ContextID = "wrong-context"
	if _, err := s.BindActiveM5TrialProfileAIContext(request); !errors.Is(err, ErrJobAIContextRevisionConflict) {
		t.Fatalf("context/revision 不一致仍可绑定: %v", err)
	}
}

func TestSourcingProfileAIContextBindsExactGreetingRevisionWithoutTrial(t *testing.T) {
	fixture := seedSourcingGreetingEffectFixture(t, "context-binding", 1)
	s := fixture.Store
	now := time.Now().UTC().Truncate(time.Millisecond)
	invocation := fixture.Invocations[0]
	req := sourcingGreetingEffectRequest(t, fixture, invocation, now)
	_, err := s.CreateGreetingEffectIntentAndCmd(req)
	if err != nil {
		t.Fatal(err)
	}
	greetedAt := now.Add(time.Minute)
	if err := s.db.Model(&CmdRecord{}).Where("intent_id = ?", req.Intent.IntentID).
		Updates(map[string]any{"status": CmdOk, "terminal_at": greetedAt}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&EffectIntent{}).Where("intent_id = ?", req.Intent.IntentID).
		Updates(map[string]any{"status": EffectIntentOk, "resolved_at": greetedAt}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&CandidateProfile{}).Where("profile_id = ?", req.Intent.TargetRef).
		Updates(map[string]any{
			"main_status": CandidateProfileGreeted, "successful_greeting_intent_id": req.Intent.IntentID,
			"greeted_at": greetedAt,
		}).Error; err != nil {
		t.Fatal(err)
	}
	if _, wasCreated, err := s.EnsureCommunicationV4RootForGreetedProfile(
		req.Intent.TargetRef, greetedAt,
	); err != nil || !wasCreated {
		t.Fatalf("构造 sourcing V4 根失败: created=%v err=%v", wasCreated, err)
	}

	profileIDs, err := s.SourcingProfileIDsNeedingAIContextForAccount(fixture.AccountKey)
	if err != nil || len(profileIDs) != 1 || profileIDs[0] != req.Intent.TargetRef {
		t.Fatalf("未找到待绑定 sourcing 档案: ids=%v err=%v", profileIDs, err)
	}
	binding, wasCreated, err := s.BindSourcingProfileAIContext(req.Intent.TargetRef, greetedAt.Add(time.Minute))
	if err != nil || !wasCreated || binding.RevisionHash != invocation.ContextRevisionHash ||
		binding.Reason != sourcingProfileAIContextBindingReason ||
		binding.BoundBy != sourcingProfileAIContextBoundBy {
		t.Fatalf("sourcing 上下文绑定错误: binding=%+v created=%v err=%v", binding, wasCreated, err)
	}
	replayed, wasCreated, err := s.BindSourcingProfileAIContext(req.Intent.TargetRef, greetedAt.Add(2*time.Minute))
	if err != nil || wasCreated || replayed.BindingID != binding.BindingID ||
		!replayed.BoundAt.Equal(binding.BoundAt) {
		t.Fatalf("sourcing 上下文重放不幂等: binding=%+v created=%v err=%v", replayed, wasCreated, err)
	}
	profileIDs, err = s.SourcingProfileIDsNeedingAIContextForAccount(fixture.AccountKey)
	if err != nil || len(profileIDs) != 0 {
		t.Fatalf("已绑定档案仍被重复扫描: ids=%v err=%v", profileIDs, err)
	}
}
