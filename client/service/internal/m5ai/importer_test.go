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
