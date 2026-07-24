package m5ai

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"recruithelper/client/service/internal/testfixture"
	"recruithelper/contract/gen/go/protocol"
)

func sourcingPackage(overrides map[string]string) JobConfigDocumentPackage {
	documents := map[string]string{
		"打分":    "请评分 {resume_json}",
		"招呼语":   `{"prompt":"状态={career_state};简历={resume_summary_json}"}`,
		"职位筛选":  testfixture.SourcingFiltersDocument,
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
	wantFilters := protocol.CandidateSourcingFilters{
		Age:            protocol.CandidateSourcingAgeFilter{Mode: protocol.SourcingAgeModeRange, MinAge: 25, MaxAge: 45},
		ActiveWindow:   protocol.SourcingActiveWindowDays3,
		CareerStatuses: []protocol.SourcingCareerStatus{},
		Educations: []protocol.SourcingEducation{
			protocol.SourcingEducationAssociate,
			protocol.SourcingEducationBachelor,
			protocol.SourcingEducationMaster,
			protocol.SourcingEducationMbaEmba,
			protocol.SourcingEducationDoctorate,
		},
		Gender:                   protocol.SourcingGenderAny,
		ExcludeViewed:            true,
		ExcludeCoworkerContacted: false,
	}
	if !reflect.DeepEqual(view.JobFilters, wantFilters) {
		t.Fatalf("职位筛选强类型派生错误: got=%+v want=%+v", view.JobFilters, wantFilters)
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

func mutateSourcingFiltersDocument(t *testing.T, mutate func([]map[string]any)) string {
	t.Helper()
	var groups []map[string]any
	if err := json.Unmarshal([]byte(testfixture.SourcingFiltersDocument), &groups); err != nil {
		t.Fatal(err)
	}
	mutate(groups)
	raw, err := json.Marshal(groups)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func findSourcingFilterGroup(t *testing.T, groups []map[string]any, fieldKey string) map[string]any {
	t.Helper()
	for _, group := range groups {
		if group["fieldKey"] == fieldKey {
			return group
		}
	}
	t.Fatalf("fixture 缺少筛选组 %s", fieldKey)
	return nil
}

func sourcingFilterOptions(t *testing.T, group map[string]any) []map[string]any {
	t.Helper()
	rawOptions, ok := group["options"].([]any)
	if !ok {
		t.Fatalf("fixture options 类型错误: %T", group["options"])
	}
	options := make([]map[string]any, len(rawOptions))
	for index, rawOption := range rawOptions {
		option, ok := rawOption.(map[string]any)
		if !ok {
			t.Fatalf("fixture option 类型错误: %T", rawOption)
		}
		options[index] = option
	}
	return options
}

func selectSourcingFilterActions(t *testing.T, group map[string]any, actions ...string) {
	t.Helper()
	selected := make(map[string]bool, len(actions))
	for _, action := range actions {
		selected[action] = true
	}
	for _, option := range sourcingFilterOptions(t, group) {
		option["selected"] = selected[option["action"].(string)]
	}
}

func TestDeriveSourcingViewMapsUnlimitedAndFixedEnumOrder(t *testing.T) {
	document := mutateSourcingFiltersDocument(t, func(groups []map[string]any) {
		age := findSourcingFilterGroup(t, groups, "age")
		selectSourcingFilterActions(t, age, "age:不限")
		active := findSourcingFilterGroup(t, groups, "activeTime")
		selectSourcingFilterActions(t, active, "activeTime:30天内活跃")
		careers := findSourcingFilterGroup(t, groups, "careerStatuses")
		selectSourcingFilterActions(t, careers,
			"careerStatuses:在职-暂不找工作",
			"careerStatuses:在职-正在找工作",
		)
		educations := findSourcingFilterGroup(t, groups, "educations")
		selectSourcingFilterActions(t, educations,
			"educations:博士",
			"educations:高中",
			"educations:大专",
		)
		gender := findSourcingFilterGroup(t, groups, "gender")
		selectSourcingFilterActions(t, gender, "gender:女")
		filterTypes := findSourcingFilterGroup(t, groups, "filterTypes")
		selectSourcingFilterActions(t, filterTypes, "filterTypes:不限")
	})
	view, err := DeriveSourcingView(sourcingPackage(map[string]string{"职位筛选": document}))
	if err != nil {
		t.Fatal(err)
	}
	want := protocol.CandidateSourcingFilters{
		Age:          protocol.CandidateSourcingAgeFilter{Mode: protocol.SourcingAgeModeAny},
		ActiveWindow: protocol.SourcingActiveWindowDays30,
		CareerStatuses: []protocol.SourcingCareerStatus{
			protocol.SourcingCareerStatusEmployedLooking,
			protocol.SourcingCareerStatusEmployedNotLooking,
		},
		Educations: []protocol.SourcingEducation{
			protocol.SourcingEducationHighSchool,
			protocol.SourcingEducationAssociate,
			protocol.SourcingEducationDoctorate,
		},
		Gender:                   protocol.SourcingGenderFemale,
		ExcludeViewed:            false,
		ExcludeCoworkerContacted: false,
	}
	if !reflect.DeepEqual(view.JobFilters, want) {
		t.Fatalf("不限或固定枚举顺序映射错误: got=%+v want=%+v", view.JobFilters, want)
	}
}

func TestDeriveSourcingViewRejectsInvalidSourcingFilterShape(t *testing.T) {
	tests := map[string]func([]map[string]any){
		"missing group": func(groups []map[string]any) {
			groups[0] = groups[1]
		},
		"duplicate group": func(groups []map[string]any) {
			groups[1] = groups[0]
		},
		"unknown group": func(groups []map[string]any) {
			groups[0]["fieldKey"] = "salary"
		},
		"unknown group property": func(groups []map[string]any) {
			groups[0]["selector"] = ".forbidden"
		},
		"missing metadata": func(groups []map[string]any) {
			delete(groups[0], "controlType")
		},
		"wrong title": func(groups []map[string]any) {
			groups[0]["title"] = "年龄"
		},
		"wrong multiple": func(groups []map[string]any) {
			groups[2]["multiple"] = false
		},
		"wrong control type": func(groups []map[string]any) {
			groups[4]["controlType"] = "checkbox-group"
		},
		"missing age bound property": func(groups []map[string]any) {
			delete(groups[0], "customMaxAge")
		},
		"age bound on other group": func(groups []map[string]any) {
			groups[1]["customMinAge"] = nil
		},
		"unknown option": func(groups []map[string]any) {
			options := sourcingFilterOptions(t, groups[1])
			options[0]["action"] = "activeTime:days14"
		},
		"wrong option label": func(groups []map[string]any) {
			options := sourcingFilterOptions(t, groups[3])
			options[0]["label"] = "任意"
		},
		"duplicate option": func(groups []map[string]any) {
			options := sourcingFilterOptions(t, groups[3])
			options[1]["action"] = options[0]["action"]
		},
		"unknown option property": func(groups []map[string]any) {
			options := sourcingFilterOptions(t, groups[5])
			options[0]["value"] = "any"
		},
		"single select none": func(groups []map[string]any) {
			selectSourcingFilterActions(t, groups[4])
		},
		"single select multiple": func(groups []map[string]any) {
			selectSourcingFilterActions(t, groups[1], "activeTime:不限", "activeTime:今日活跃")
		},
		"multiple select none": func(groups []map[string]any) {
			selectSourcingFilterActions(t, groups[2])
		},
		"unlimited mixed with concrete": func(groups []map[string]any) {
			selectSourcingFilterActions(t, groups[5], "filterTypes:不限", "filterTypes:过滤我已看过")
		},
		"custom age missing minimum": func(groups []map[string]any) {
			groups[0]["customMinAge"] = nil
		},
		"custom age over bounds": func(groups []map[string]any) {
			groups[0]["customMaxAge"] = float64(66)
		},
		"custom age inverted": func(groups []map[string]any) {
			groups[0]["customMinAge"] = float64(46)
			groups[0]["customMaxAge"] = float64(45)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			document := mutateSourcingFiltersDocument(t, mutate)
			_, err := DeriveSourcingView(sourcingPackage(map[string]string{"职位筛选": document}))
			if !errors.Is(err, ErrInvalidSourcingView) {
				t.Fatalf("非法职位筛选未拒绝: %v", err)
			}
		})
	}
}
