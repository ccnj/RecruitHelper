package m5ai

import (
	"encoding/json"
	"errors"
	"testing"
)

func sourcingPackage(overrides map[string]string) JobConfigDocumentPackage {
	documents := map[string]string{
		"打分":    "请评分 {resume_json}",
		"招呼语":   `{"prompt":"状态={career_state};简历={resume_summary_json}"}`,
		"职位筛选":  `[{"field":"age"}]`,
		"候选人筛选": `{"minScore":5,"targetMin":80,"targetMax":90,"maleRatioLimit":50}`,
	}
	for key, value := range overrides {
		documents[key] = value
	}
	out := JobConfigDocumentPackage{}
	for _, key := range []string{"候选人筛选", "打分", "招呼语", "职位筛选"} {
		out.Documents = append(out.Documents, JobConfigDocument{DocType: key, Content: documents[key]})
	}
	return out
}

func TestDeriveSourcingViewUsesImmutableDocuments(t *testing.T) {
	view, err := DeriveSourcingView(sourcingPackage(map[string]string{
		"候选人筛选": `{"minScore":7,"targetMin":20,"targetMax":10,"maleRatioLimit":30}`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if view.MappingVersion != SourcingMappingVersion || view.ScoringPrompt != "请评分 {resume_json}" ||
		view.GreetingPrompt != "状态={career_state};简历={resume_summary_json}" ||
		view.UsePlatformDefaultGreeting ||
		view.CandidateSelection != (CandidateSelectionView{MinScore: 7, TargetMin: 10, TargetMax: 20, MaleRatioLimit: 30}) {
		t.Fatalf("采集视图派生错误: %+v", view)
	}
	var filters []map[string]any
	if json.Unmarshal(view.JobFilters, &filters) != nil || len(filters) != 1 || filters[0]["field"] != "age" {
		t.Fatalf("职位筛选未原样派生: %s", view.JobFilters)
	}
}

func TestDeriveSourcingViewParsesPlainAndWrappedGreetingDocuments(t *testing.T) {
	tests := []struct {
		name                string
		document            string
		wantPrompt          string
		wantPlatformDefault bool
	}{
		{
			name: "plain", document: "状态={career_state};简历={resume_summary_json}",
			wantPrompt: "状态={career_state};简历={resume_summary_json}",
		},
		{
			name: "wrapped", document: `{"prompt":"状态={career_state};简历={resume_summary_json}","usePlatformDefault":true}`,
			wantPrompt: "状态={career_state};简历={resume_summary_json}", wantPlatformDefault: true,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			view, err := DeriveSourcingView(sourcingPackage(map[string]string{"招呼语": testCase.document}))
			if err != nil || view.GreetingPrompt != testCase.wantPrompt ||
				view.UsePlatformDefaultGreeting != testCase.wantPlatformDefault {
				t.Fatalf("招呼文档解析错误: view=%+v err=%v", view, err)
			}
		})
	}
}

func TestDeriveSourcingViewMatchesLegacyDefaultsAndClamps(t *testing.T) {
	view, err := DeriveSourcingView(sourcingPackage(map[string]string{
		"候选人筛选": `{"minScore":99,"targetMin":-1,"targetMax":999,"maleRatioLimit":"25"}`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if view.CandidateSelection != (CandidateSelectionView{MinScore: 10, TargetMin: 0, TargetMax: 150, MaleRatioLimit: 25}) {
		t.Fatalf("候选人筛选 clamp 错误: %+v", view.CandidateSelection)
	}
	defaults, err := DeriveSourcingView(sourcingPackage(map[string]string{"候选人筛选": "bad-json"}))
	if err != nil || defaults.CandidateSelection != (CandidateSelectionView{MinScore: 5, TargetMin: 80, TargetMax: 90, MaleRatioLimit: 50}) {
		t.Fatalf("候选人筛选默认值错误: view=%+v err=%v", defaults.CandidateSelection, err)
	}
}

func TestDeriveSourcingViewRejectsUnboundPromptsAndInvalidFilters(t *testing.T) {
	for name, overrides := range map[string]map[string]string{
		"scoring placeholder missing":   {"打分": "请评分"},
		"scoring placeholder repeated":  {"打分": "{resume_json}{resume_json}"},
		"greeting placeholder missing":  {"招呼语": `{"prompt":"{resume_summary_json}"}`},
		"greeting wrapper flag invalid": {"招呼语": `{"prompt":"{career_state}{resume_summary_json}","usePlatformDefault":"false"}`},
		"filters invalid":               {"职位筛选": `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := DeriveSourcingView(sourcingPackage(overrides))
			if !errors.Is(err, ErrInvalidSourcingView) {
				t.Fatalf("非法采集配置未拒绝: %v", err)
			}
		})
	}
}
