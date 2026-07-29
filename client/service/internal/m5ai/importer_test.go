package m5ai

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/testfixture"
)

func syntheticLegacyBundle(t *testing.T, jobID int, name string) map[string]any {
	t.Helper()
	documents := map[string]string{
		"候选人筛选": `{"minScore":5}`,
		"固定规则":  "",
		"固定话术":  `{"fixture":true}`,
		"多轮沟通":  "简历={简历}\n时段={推荐时段}\n历史={对话历史}\n输出={话术_序列}",
		"客户事实库": "fixture://facts",
		"意向判断":  "招呼={招呼语}\n回复={回复}",
		"打分":    "fixture://score",
		"招呼语":   "fixture://greeting",
		"沉默追问":  "姓名={姓名}\n年龄={年龄}\n性别={性别}\n简历={简历}",
		"职位筛选":  testfixture.SourcingFiltersDocument,
	}
	block := func(prompt string) map[string]any {
		return map[string]any{"prompt": prompt, "apiKey": "sk-must-not-persist", "model": "legacy-model", "baseUrl": "https://legacy.invalid"}
	}
	return map[string]any{
		"job":       map[string]any{"id": jobID, "name": name, "environment": "online"},
		"documents": documents,
		"scoring":   block(documents["打分"]), "greeting": block(documents["招呼语"]),
		"communication": block(documents["多轮沟通"]), "intent": block(documents["意向判断"]),
		"silenceFollowup": block(documents["沉默追问"]),
		"facts":           map[string]any{"content": documents["客户事实库"]},
		"fixedPhrases":    map[string]any{"content": documents["固定话术"], "scenes": map[string]any{}},
		"fixedRules":      map[string]any{"content": documents["固定规则"]},
		"filters":         map[string]any{}, "candidateSelection": map[string]any{"minScore": 5},
	}
}

func TestImportLegacyJobConfigPreservesEveryDocumentAndDropsCredentials(t *testing.T) {
	bundle := syntheticLegacyBundle(t, 17, "合成职位")
	raw, _ := json.Marshal(bundle)
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	revisions, err := ImportLegacyJobConfig(raw, now)
	if err != nil || len(revisions) != 1 {
		t.Fatalf("导入失败: revisions=%d err=%v", len(revisions), err)
	}
	revision := revisions[0]
	if revision.ContextID == "" || revision.RevisionHash == "" || revision.SourceJobRef != "17" ||
		revision.Communication.MappingVersion != MappingVersion {
		t.Fatalf("revision 元数据不完整: %+v", revision)
	}
	source := bundle["documents"].(map[string]string)
	silencePrompt, promptErr := SilenceFollowupPrompt(revision)
	if promptErr != nil || silencePrompt != source["沉默追问"] {
		t.Fatalf("沉默追问原文未从 source package 唯一提取: prompt=%q err=%v", silencePrompt, promptErr)
	}
	if len(revision.SourcePackage.Documents) != len(source) {
		t.Fatalf("文档数不守恒: got=%d want=%d", len(revision.SourcePackage.Documents), len(source))
	}
	for _, document := range revision.SourcePackage.Documents {
		if source[document.DocType] != document.Content {
			t.Fatalf("文档未逐字节保留: %s", document.DocType)
		}
	}
	persisted, _ := json.Marshal(revision)
	for _, forbidden := range []string{"sk-must-not-persist", "legacy-model", "https://legacy.invalid", "apiKey", "baseUrl"} {
		if strings.Contains(string(persisted), forbidden) {
			t.Fatalf("revision 泄漏旧 provider 字段 %q", forbidden)
		}
	}

	repeat, err := ImportLegacyJobConfig(raw, now.Add(time.Hour))
	if err != nil || repeat[0].ContextID != revision.ContextID || repeat[0].RevisionHash != revision.RevisionHash {
		t.Fatalf("相同来源包未得到稳定 revision: %+v err=%v", repeat, err)
	}
}

func TestImportLegacyPluralImportsAllWithoutChoosingByTitle(t *testing.T) {
	payload := map[string]any{
		"currentJobId": 2,
		"jobs":         []any{syntheticLegacyBundle(t, 1, "同名也不匹配档案"), syntheticLegacyBundle(t, 2, "另一个职位")},
	}
	raw, _ := json.Marshal(payload)
	revisions, err := ImportLegacyJobConfig(raw, time.Now())
	if err != nil || len(revisions) != 2 {
		t.Fatalf("多职位导入失败: revisions=%d err=%v", len(revisions), err)
	}
	if revisions[0].ContextID == revisions[1].ContextID {
		t.Fatal("两个 job id 不得折叠成同一 context")
	}
}

func TestTolerantPluralImportKeepsGoodJobsWhenOneJobIsUnderConfigured(t *testing.T) {
	broken := syntheticLegacyBundle(t, 2, "配置不全的历史职位")
	// The realistic failure in a customer's backend: a job whose documents were
	// never completed. All-or-nothing would disqualify jobs 1 and 3 with it.
	delete(broken["documents"].(map[string]string), "意向判断")
	broken["intent"] = map[string]any{"prompt": ""}
	payload := map[string]any{
		"currentJobId": 1,
		"jobs": []any{
			syntheticLegacyBundle(t, 1, "健康职位甲"),
			broken,
			syntheticLegacyBundle(t, 3, "健康职位乙"),
		},
	}
	raw, _ := json.Marshal(payload)
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)

	if _, err := ImportLegacyJobConfigFromBackend(raw, now); err == nil {
		t.Fatal("既有 all-or-nothing 入口必须仍然整批失败，本测试的前提才成立")
	}

	revisions, skipped, err := ImportLegacyJobConfigsTolerant(raw, now)
	if err != nil {
		t.Fatalf("容错导入不应整批失败: %v", err)
	}
	if len(revisions) != 2 {
		t.Fatalf("健康职位未全部保留: got=%d want=2", len(revisions))
	}
	for _, revision := range revisions {
		if revision.SourceJobRef == "2" {
			t.Fatal("配置不全的职位不得进入有效职位集")
		}
		if revision.SourceKind != "legacyJobConfig" {
			t.Fatalf("来源身份错误: %q", revision.SourceKind)
		}
	}
	if len(skipped) != 1 || skipped[0].SourceJobRef != "2" || skipped[0].Index != 1 {
		t.Fatalf("跳过职位未被如实记录: %+v", skipped)
	}
	if skipped[0].Reason == "" {
		t.Fatal("跳过原因不得为空，否则运营无法判断要去后台补什么")
	}
	// The skip reason travels into logs and the admin diagnostic surface, so it
	// must stay structural: no document content, no provider credential.
	for _, forbidden := range []string{
		"sk-must-not-persist", "legacy-model", "https://legacy.invalid",
		"fixture://facts", "fixture://score", "fixture://greeting",
	} {
		if strings.Contains(skipped[0].Reason, forbidden) {
			t.Fatalf("跳过原因泄漏配置正文或凭据: %q", skipped[0].Reason)
		}
	}
}

func TestTolerantPluralImportFailsWholePayloadDamageAndSkipsDuplicateJobID(t *testing.T) {
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)

	t.Run("whole payload damage still fails", func(t *testing.T) {
		for name, raw := range map[string][]byte{
			"invalid json": []byte(`{`),
			"not plural":   []byte(`{"job":{"id":1,"name":"职位"},"documents":{}}`),
			"empty jobs":   []byte(`{"currentJobId":null,"jobs":[]}`),
		} {
			if _, _, err := ImportLegacyJobConfigsTolerant(raw, now); err == nil {
				t.Fatalf("%s 必须整体失败，不得静默退化成零个有效职位", name)
			}
		}
	})

	t.Run("duplicate job id keeps first arrival", func(t *testing.T) {
		payload := map[string]any{
			"currentJobId": 1,
			"jobs": []any{
				syntheticLegacyBundle(t, 1, "先到"),
				syntheticLegacyBundle(t, 1, "后到"),
			},
		}
		raw, _ := json.Marshal(payload)
		revisions, skipped, err := ImportLegacyJobConfigsTolerant(raw, now)
		if err != nil {
			t.Fatalf("重复 job id 不应整批失败: %v", err)
		}
		if len(revisions) != 1 || revisions[0].DisplayName != "先到" {
			t.Fatalf("重复 job id 未保留首次到达: %+v", revisions)
		}
		if len(skipped) != 1 || skipped[0].Index != 1 {
			t.Fatalf("重复项未被记录: %+v", skipped)
		}
	})
}

func TestBackendImportRecordsDistinctStableSourceKind(t *testing.T) {
	bundle := syntheticLegacyBundle(t, 17, "合成职位")
	raw, _ := json.Marshal(bundle)
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	local, err := ImportLegacyJobConfig(raw, now)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := ImportLegacyJobConfigFromBackend(raw, now)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := ImportLegacyJobConfigFromBackend(raw, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if remote[0].SourceKind != "legacyJobConfig" || remote[0].ContextID == local[0].ContextID ||
		remote[0].ContextID != repeat[0].ContextID || remote[0].RevisionHash != repeat[0].RevisionHash {
		t.Fatalf("旧后台来源身份不稳定: local=%+v remote=%+v repeat=%+v", local[0], remote[0], repeat[0])
	}
}

func TestImportLegacyJobConfigFailsClosedOnConflictsAndIncompleteDocuments(t *testing.T) {
	t.Run("duplicate doc type", func(t *testing.T) {
		raw := []byte(`{"job":{"id":1,"name":"职位","environment":"online"},"documents":{"多轮沟通":"一","多轮沟通":"二"}}`)
		if _, err := ImportLegacyJobConfig(raw, time.Now()); err == nil || !strings.Contains(err.Error(), "重复 doc_type") {
			t.Fatalf("重复 doc_type 未拒绝: %v", err)
		}
	})
	t.Run("structured conflict", func(t *testing.T) {
		bundle := syntheticLegacyBundle(t, 1, "职位")
		bundle["communication"].(map[string]any)["prompt"] = "被篡改"
		raw, _ := json.Marshal(bundle)
		if _, err := ImportLegacyJobConfig(raw, time.Now()); err == nil || !strings.Contains(err.Error(), "冲突") {
			t.Fatalf("结构化区冲突未拒绝: %v", err)
		}
	})
	t.Run("include documents false", func(t *testing.T) {
		bundle := syntheticLegacyBundle(t, 1, "职位")
		bundle["documents"] = map[string]string{}
		raw, _ := json.Marshal(bundle)
		if _, err := ImportLegacyJobConfig(raw, time.Now()); err == nil {
			t.Fatal("空 documents 不得创建 revision")
		}
	})
	t.Run("unknown live token", func(t *testing.T) {
		bundle := syntheticLegacyBundle(t, 1, "职位")
		docs := bundle["documents"].(map[string]string)
		docs["多轮沟通"] += "\n{未知字段}"
		bundle["communication"].(map[string]any)["prompt"] = docs["多轮沟通"]
		raw, _ := json.Marshal(bundle)
		if _, err := ImportLegacyJobConfig(raw, time.Now()); err == nil || !strings.Contains(err.Error(), "unknownTemplateToken") {
			t.Fatalf("未知活 token 未拒绝: %v", err)
		}
	})
}
