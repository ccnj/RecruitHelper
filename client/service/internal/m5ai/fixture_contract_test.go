package m5ai

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"
)

func fixturePath(t *testing.T, name string) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位测试源文件")
	}
	return filepath.Join(filepath.Dir(source), "..", "..", "..", "..", "docs", "fixtures", name)
}

func readFixture(t *testing.T, name string, target any) {
	t.Helper()
	raw, err := os.ReadFile(fixturePath(t, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatal(err)
	}
}

func TestSanitizedProductionShapeDrivesCompleteDocumentImport(t *testing.T) {
	var fixture struct {
		ContainsRawPromptOrDocumentContent bool `json:"containsRawPromptOrDocumentContent"`
		TargetCurrentBundle                struct {
			DocumentCount int `json:"documentCount"`
			Documents     []struct {
				DocType string `json:"docType"`
			} `json:"documents"`
		} `json:"targetCurrentBundle"`
		SyntheticRoundTrip struct {
			Documents []JobConfigDocument `json:"documents"`
		} `json:"syntheticRoundTrip"`
	}
	readFixture(t, "m5-job-config-production-shape.v1.json", &fixture)
	if fixture.ContainsRawPromptOrDocumentContent || fixture.TargetCurrentBundle.DocumentCount != 10 ||
		len(fixture.SyntheticRoundTrip.Documents) != 10 {
		t.Fatalf("生产形状 fixture 边界漂移: %+v", fixture.TargetCurrentBundle)
	}
	observed := make([]string, 0, len(fixture.TargetCurrentBundle.Documents))
	for _, document := range fixture.TargetCurrentBundle.Documents {
		observed = append(observed, document.DocType)
	}
	synthetic := make(map[string]string, len(fixture.SyntheticRoundTrip.Documents))
	for _, document := range fixture.SyntheticRoundTrip.Documents {
		synthetic[document.DocType] = document.Content
	}
	sort.Strings(observed)
	syntheticTypes := make([]string, 0, len(synthetic))
	for docType := range synthetic {
		syntheticTypes = append(syntheticTypes, docType)
	}
	sort.Strings(syntheticTypes)
	if string(mustFixtureJSON(t, observed)) != string(mustFixtureJSON(t, syntheticTypes)) {
		t.Fatalf("production shape 与 synthetic round-trip 类型不一致: %v != %v", observed, syntheticTypes)
	}

	// The shape fixture deliberately contains no production prompt. Build the
	// executable synthetic envelope from its ten approved doc types, then prove
	// the importer preserves those exact synthetic bytes.
	synthetic["多轮沟通"] = "简历={简历}\n时段={推荐时段}\n历史={对话历史}"
	synthetic["意向判断"] = "招呼={招呼语}\n回复={回复}"
	synthetic["沉默追问"] = "姓名={姓名}\n年龄={年龄}\n性别={性别}\n简历={简历}"
	synthetic["客户事实库"] = "fixture://客户事实库"
	bundle := syntheticLegacyBundle(t, 9, "fixture job")
	bundle["documents"] = synthetic
	bundle["scoring"].(map[string]any)["prompt"] = synthetic["打分"]
	bundle["greeting"].(map[string]any)["prompt"] = synthetic["招呼语"]
	bundle["communication"].(map[string]any)["prompt"] = synthetic["多轮沟通"]
	bundle["intent"].(map[string]any)["prompt"] = synthetic["意向判断"]
	bundle["silenceFollowup"].(map[string]any)["prompt"] = synthetic["沉默追问"]
	bundle["facts"].(map[string]any)["content"] = synthetic["客户事实库"]
	bundle["fixedPhrases"].(map[string]any)["content"] = synthetic["固定话术"]
	bundle["fixedRules"].(map[string]any)["content"] = synthetic["固定规则"]
	raw, _ := json.Marshal(bundle)
	revisions, err := ImportLegacyJobConfig(raw, time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC))
	if err != nil || len(revisions) != 1 || len(revisions[0].SourcePackage.Documents) != 10 {
		t.Fatalf("完整 synthetic production shape 导入失败: revisions=%d err=%v", len(revisions), err)
	}
	for _, document := range revisions[0].SourcePackage.Documents {
		if document.Content != synthetic[document.DocType] {
			t.Fatalf("文档字节未守恒: %s", document.DocType)
		}
	}
}

func mustFixtureJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
