package m5ai

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"

	"recruithelper/contract/gen/go/protocol"
)

const SourcingMappingVersion = "m6-sourcing-v1"

var ErrInvalidSourcingView = errors.New("职位采集与评分配置无效")

// CandidateSelectionView mirrors the old backend's derived selection knobs.
// It is rebuilt from the immutable 候选人筛选 document, so the new client does
// not depend on the old backend's convenience projection at runtime.
type CandidateSelectionView struct {
	MinScore       int `json:"minScore"`
	TargetMin      int `json:"targetMin"`
	TargetMax      int `json:"targetMax"`
	MaleRatioLimit int `json:"maleRatioLimit"`
}

// SourcingView is an in-memory executable projection. Prompts and derived filters
// stay inside the local brain and must never be returned by management APIs.
type SourcingView struct {
	ScoringPrompt              string
	GreetingPrompt             string
	UsePlatformDefaultGreeting bool
	JobFilters                 protocol.CandidateSourcingFilters
	CandidateSelection         CandidateSelectionView
	MappingVersion             string
}

// DeriveSourcingView reads only the immutable repository-owned document
// package. It intentionally ignores provider fields from the legacy response.
func DeriveSourcingView(source JobConfigDocumentPackage) (SourcingView, error) {
	documents := make(map[string]string, len(source.Documents))
	for _, document := range source.Documents {
		if strings.TrimSpace(document.DocType) == "" {
			return SourcingView{}, ErrInvalidSourcingView
		}
		if _, exists := documents[document.DocType]; exists {
			return SourcingView{}, ErrInvalidSourcingView
		}
		documents[document.DocType] = document.Content
	}

	scoring := documents["打分"]
	if strings.TrimSpace(scoring) == "" || strings.Count(scoring, "{resume_json}") != 1 {
		return SourcingView{}, ErrInvalidSourcingView
	}
	greeting, usePlatformDefaultGreeting, err := deriveGreetingConfig(documents["招呼语"])
	if err != nil {
		return SourcingView{}, ErrInvalidSourcingView
	}
	if strings.TrimSpace(greeting) == "" || strings.Count(greeting, "{resume_summary_json}") != 1 ||
		strings.Count(greeting, "{career_state}") != 1 {
		return SourcingView{}, ErrInvalidSourcingView
	}

	filters, err := deriveSourcingFilters(documents["职位筛选"])
	if err != nil {
		return SourcingView{}, ErrInvalidSourcingView
	}

	return SourcingView{
		ScoringPrompt: scoring, GreetingPrompt: greeting,
		UsePlatformDefaultGreeting: usePlatformDefaultGreeting,
		JobFilters:                 filters,
		CandidateSelection:         deriveCandidateSelection(documents["候选人筛选"]),
		MappingVersion:             SourcingMappingVersion,
	}, nil
}

type legacySourcingFilterOption struct {
	Label    string `json:"label"`
	Action   string `json:"action"`
	Selected bool   `json:"selected"`
}

type legacySourcingFilterGroup struct {
	FieldKey     string                       `json:"fieldKey"`
	Title        string                       `json:"title"`
	Multiple     bool                         `json:"multiple"`
	ControlType  string                       `json:"controlType"`
	CustomMinAge *int                         `json:"customMinAge"`
	CustomMaxAge *int                         `json:"customMaxAge"`
	Options      []legacySourcingFilterOption `json:"options"`
}

type legacySourcingOptionSpec struct {
	label  string
	action string
}

type legacySourcingGroupSpec struct {
	title       string
	multiple    bool
	controlType string
	options     []legacySourcingOptionSpec
}

var legacySourcingGroupSpecs = map[string]legacySourcingGroupSpec{
	"age": {
		title: "年龄要求", controlType: "checkbox-group",
		options: []legacySourcingOptionSpec{
			{label: "不限", action: "age:不限"},
			{label: "20-25", action: "age:20-25"},
			{label: "25-30", action: "age:25-30"},
			{label: "30-35", action: "age:30-35"},
			{label: "35-40", action: "age:35-40"},
			{label: "40以上", action: "age:40以上"},
			{label: "自定义", action: "age:自定义"},
		},
	},
	"activeTime": {
		title: "活跃日期", controlType: "radio-group",
		options: []legacySourcingOptionSpec{
			{label: "不限", action: "activeTime:不限"},
			{label: "今日活跃", action: "activeTime:今日活跃"},
			{label: "3天内活跃", action: "activeTime:3天内活跃"},
			{label: "7天内活跃", action: "activeTime:7天内活跃"},
			{label: "30天内活跃", action: "activeTime:30天内活跃"},
		},
	},
	"careerStatuses": {
		title: "求职状态", multiple: true, controlType: "checkbox-group",
		options: []legacySourcingOptionSpec{
			{label: "不限", action: "careerStatuses:不限"},
			{label: "在职-正在找工作", action: "careerStatuses:在职-正在找工作"},
			{label: "离职-正在找工作", action: "careerStatuses:离职-正在找工作"},
			{label: "在职-看看机会", action: "careerStatuses:在职-看看机会"},
			{label: "在职-暂不找工作", action: "careerStatuses:在职-暂不找工作"},
		},
	},
	"educations": {
		title: "学历要求", multiple: true, controlType: "checkbox-group",
		options: []legacySourcingOptionSpec{
			{label: "不限", action: "educations:不限"},
			{label: "初中及以下", action: "educations:初中及以下"},
			{label: "高中", action: "educations:高中"},
			{label: "中专/中技", action: "educations:中专/中技"},
			{label: "大专", action: "educations:大专"},
			{label: "本科", action: "educations:本科"},
			{label: "硕士", action: "educations:硕士"},
			{label: "MBA/EMBA", action: "educations:MBA/EMBA"},
			{label: "博士", action: "educations:博士"},
		},
	},
	"gender": {
		title: "性别要求", controlType: "radio-group",
		options: []legacySourcingOptionSpec{
			{label: "不限", action: "gender:不限"},
			{label: "男", action: "gender:男"},
			{label: "女", action: "gender:女"},
		},
	},
	"filterTypes": {
		title: "人才范围", multiple: true, controlType: "checkbox-group",
		options: []legacySourcingOptionSpec{
			{label: "不限", action: "filterTypes:不限"},
			{label: "过滤我已看过", action: "filterTypes:过滤我已看过"},
			{label: "过滤同事已聊", action: "filterTypes:过滤同事已聊"},
		},
	},
}

func deriveSourcingFilters(raw string) (protocol.CandidateSourcingFilters, error) {
	var rawGroups []json.RawMessage
	if json.Unmarshal([]byte(raw), &rawGroups) != nil || len(rawGroups) != len(legacySourcingGroupSpecs) {
		return protocol.CandidateSourcingFilters{}, ErrInvalidSourcingView
	}

	groups := make(map[string]legacySourcingFilterGroup, len(rawGroups))
	for _, rawGroup := range rawGroups {
		group, err := decodeLegacySourcingGroup(rawGroup)
		if err != nil {
			return protocol.CandidateSourcingFilters{}, ErrInvalidSourcingView
		}
		if _, exists := groups[group.FieldKey]; exists {
			return protocol.CandidateSourcingFilters{}, ErrInvalidSourcingView
		}
		groups[group.FieldKey] = group
	}
	for fieldKey := range legacySourcingGroupSpecs {
		if _, exists := groups[fieldKey]; !exists {
			return protocol.CandidateSourcingFilters{}, ErrInvalidSourcingView
		}
	}

	age, err := mapLegacySourcingAge(groups["age"])
	if err != nil {
		return protocol.CandidateSourcingFilters{}, ErrInvalidSourcingView
	}
	activeWindow, err := mapLegacySourcingActiveWindow(groups["activeTime"])
	if err != nil {
		return protocol.CandidateSourcingFilters{}, ErrInvalidSourcingView
	}
	careerStatuses, err := mapLegacySourcingCareerStatuses(groups["careerStatuses"])
	if err != nil {
		return protocol.CandidateSourcingFilters{}, ErrInvalidSourcingView
	}
	educations, err := mapLegacySourcingEducations(groups["educations"])
	if err != nil {
		return protocol.CandidateSourcingFilters{}, ErrInvalidSourcingView
	}
	gender, err := mapLegacySourcingGender(groups["gender"])
	if err != nil {
		return protocol.CandidateSourcingFilters{}, ErrInvalidSourcingView
	}
	excludeViewed, excludeCoworkerContacted, err := mapLegacySourcingFilterTypes(groups["filterTypes"])
	if err != nil {
		return protocol.CandidateSourcingFilters{}, ErrInvalidSourcingView
	}

	return protocol.CandidateSourcingFilters{
		Age: age, ActiveWindow: activeWindow, CareerStatuses: careerStatuses,
		Educations: educations, Gender: gender, ExcludeViewed: excludeViewed,
		ExcludeCoworkerContacted: excludeCoworkerContacted,
	}, nil
}

func decodeLegacySourcingGroup(raw json.RawMessage) (legacySourcingFilterGroup, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return legacySourcingFilterGroup{}, ErrInvalidSourcingView
	}
	var fieldKey string
	if json.Unmarshal(fields["fieldKey"], &fieldKey) != nil {
		return legacySourcingFilterGroup{}, ErrInvalidSourcingView
	}
	spec, exists := legacySourcingGroupSpecs[fieldKey]
	if !exists {
		return legacySourcingFilterGroup{}, ErrInvalidSourcingView
	}
	requiredFields := map[string]struct{}{
		"fieldKey": {}, "title": {}, "multiple": {}, "controlType": {}, "options": {},
	}
	if fieldKey == "age" {
		requiredFields["customMinAge"] = struct{}{}
		requiredFields["customMaxAge"] = struct{}{}
	}
	if len(fields) != len(requiredFields) {
		return legacySourcingFilterGroup{}, ErrInvalidSourcingView
	}
	for name := range fields {
		if _, allowed := requiredFields[name]; !allowed {
			return legacySourcingFilterGroup{}, ErrInvalidSourcingView
		}
	}
	for name := range requiredFields {
		if _, present := fields[name]; !present {
			return legacySourcingFilterGroup{}, ErrInvalidSourcingView
		}
	}

	var group legacySourcingFilterGroup
	if json.Unmarshal(raw, &group) != nil ||
		group.Title != spec.title ||
		group.Multiple != spec.multiple ||
		group.ControlType != spec.controlType {
		return legacySourcingFilterGroup{}, ErrInvalidSourcingView
	}
	if group.CustomMinAge != nil && (*group.CustomMinAge < 16 || *group.CustomMinAge > 65) {
		return legacySourcingFilterGroup{}, ErrInvalidSourcingView
	}
	if group.CustomMaxAge != nil && (*group.CustomMaxAge < 16 || *group.CustomMaxAge > 65) {
		return legacySourcingFilterGroup{}, ErrInvalidSourcingView
	}
	if group.CustomMinAge != nil && group.CustomMaxAge != nil && *group.CustomMinAge > *group.CustomMaxAge {
		return legacySourcingFilterGroup{}, ErrInvalidSourcingView
	}
	if len(group.Options) != len(spec.options) {
		return legacySourcingFilterGroup{}, ErrInvalidSourcingView
	}

	optionsByAction := make(map[string]legacySourcingFilterOption, len(group.Options))
	var rawOptions []json.RawMessage
	if json.Unmarshal(fields["options"], &rawOptions) != nil || len(rawOptions) != len(spec.options) {
		return legacySourcingFilterGroup{}, ErrInvalidSourcingView
	}
	for index, option := range group.Options {
		var optionFields map[string]json.RawMessage
		if json.Unmarshal(rawOptions[index], &optionFields) != nil || len(optionFields) != 3 {
			return legacySourcingFilterGroup{}, ErrInvalidSourcingView
		}
		for _, name := range []string{"label", "action", "selected"} {
			if _, present := optionFields[name]; !present {
				return legacySourcingFilterGroup{}, ErrInvalidSourcingView
			}
		}
		if _, exists := optionsByAction[option.Action]; exists {
			return legacySourcingFilterGroup{}, ErrInvalidSourcingView
		}
		optionsByAction[option.Action] = option
	}

	selectedCount := 0
	unlimitedSelected := false
	for _, optionSpec := range spec.options {
		option, exists := optionsByAction[optionSpec.action]
		if !exists || option.Label != optionSpec.label {
			return legacySourcingFilterGroup{}, ErrInvalidSourcingView
		}
		if option.Selected {
			selectedCount++
			unlimitedSelected = unlimitedSelected || option.Label == "不限"
		}
	}
	if (!spec.multiple && selectedCount != 1) ||
		(spec.multiple && (selectedCount == 0 || (unlimitedSelected && selectedCount != 1))) {
		return legacySourcingFilterGroup{}, ErrInvalidSourcingView
	}
	return group, nil
}

func selectedLegacySourcingActions(group legacySourcingFilterGroup) map[string]bool {
	selected := make(map[string]bool)
	for _, option := range group.Options {
		if option.Selected {
			selected[option.Action] = true
		}
	}
	return selected
}

func mapLegacySourcingAge(group legacySourcingFilterGroup) (protocol.CandidateSourcingAgeFilter, error) {
	selected := selectedLegacySourcingActions(group)
	switch {
	case selected["age:不限"]:
		return protocol.CandidateSourcingAgeFilter{Mode: protocol.SourcingAgeModeAny}, nil
	case selected["age:20-25"]:
		return protocol.CandidateSourcingAgeFilter{Mode: protocol.SourcingAgeModeRange, MinAge: 20, MaxAge: 25}, nil
	case selected["age:25-30"]:
		return protocol.CandidateSourcingAgeFilter{Mode: protocol.SourcingAgeModeRange, MinAge: 25, MaxAge: 30}, nil
	case selected["age:30-35"]:
		return protocol.CandidateSourcingAgeFilter{Mode: protocol.SourcingAgeModeRange, MinAge: 30, MaxAge: 35}, nil
	case selected["age:35-40"]:
		return protocol.CandidateSourcingAgeFilter{Mode: protocol.SourcingAgeModeRange, MinAge: 35, MaxAge: 40}, nil
	case selected["age:40以上"]:
		return protocol.CandidateSourcingAgeFilter{Mode: protocol.SourcingAgeModeRange, MinAge: 40}, nil
	case selected["age:自定义"] && group.CustomMinAge != nil:
		age := protocol.CandidateSourcingAgeFilter{Mode: protocol.SourcingAgeModeRange, MinAge: *group.CustomMinAge}
		if group.CustomMaxAge != nil {
			age.MaxAge = *group.CustomMaxAge
		}
		return age, nil
	default:
		return protocol.CandidateSourcingAgeFilter{}, ErrInvalidSourcingView
	}
}

func mapLegacySourcingActiveWindow(group legacySourcingFilterGroup) (protocol.SourcingActiveWindow, error) {
	selected := selectedLegacySourcingActions(group)
	for _, candidate := range []struct {
		action string
		value  protocol.SourcingActiveWindow
	}{
		{action: "activeTime:不限", value: protocol.SourcingActiveWindowAny},
		{action: "activeTime:今日活跃", value: protocol.SourcingActiveWindowToday},
		{action: "activeTime:3天内活跃", value: protocol.SourcingActiveWindowDays3},
		{action: "activeTime:7天内活跃", value: protocol.SourcingActiveWindowDays7},
		{action: "activeTime:30天内活跃", value: protocol.SourcingActiveWindowDays30},
	} {
		if selected[candidate.action] {
			return candidate.value, nil
		}
	}
	return "", ErrInvalidSourcingView
}

func mapLegacySourcingCareerStatuses(group legacySourcingFilterGroup) ([]protocol.SourcingCareerStatus, error) {
	selected := selectedLegacySourcingActions(group)
	if selected["careerStatuses:不限"] {
		return []protocol.SourcingCareerStatus{}, nil
	}
	values := make([]protocol.SourcingCareerStatus, 0, len(selected))
	for _, candidate := range []struct {
		action string
		value  protocol.SourcingCareerStatus
	}{
		{action: "careerStatuses:在职-正在找工作", value: protocol.SourcingCareerStatusEmployedLooking},
		{action: "careerStatuses:离职-正在找工作", value: protocol.SourcingCareerStatusLeftLooking},
		{action: "careerStatuses:在职-看看机会", value: protocol.SourcingCareerStatusEmployedOpen},
		{action: "careerStatuses:在职-暂不找工作", value: protocol.SourcingCareerStatusEmployedNotLooking},
	} {
		if selected[candidate.action] {
			values = append(values, candidate.value)
		}
	}
	return values, nil
}

func mapLegacySourcingEducations(group legacySourcingFilterGroup) ([]protocol.SourcingEducation, error) {
	selected := selectedLegacySourcingActions(group)
	if selected["educations:不限"] {
		return []protocol.SourcingEducation{}, nil
	}
	values := make([]protocol.SourcingEducation, 0, len(selected))
	for _, candidate := range []struct {
		action string
		value  protocol.SourcingEducation
	}{
		{action: "educations:初中及以下", value: protocol.SourcingEducationJuniorHighOrBelow},
		{action: "educations:高中", value: protocol.SourcingEducationHighSchool},
		{action: "educations:中专/中技", value: protocol.SourcingEducationSecondaryVocational},
		{action: "educations:大专", value: protocol.SourcingEducationAssociate},
		{action: "educations:本科", value: protocol.SourcingEducationBachelor},
		{action: "educations:硕士", value: protocol.SourcingEducationMaster},
		{action: "educations:MBA/EMBA", value: protocol.SourcingEducationMbaEmba},
		{action: "educations:博士", value: protocol.SourcingEducationDoctorate},
	} {
		if selected[candidate.action] {
			values = append(values, candidate.value)
		}
	}
	return values, nil
}

func mapLegacySourcingGender(group legacySourcingFilterGroup) (protocol.SourcingGender, error) {
	selected := selectedLegacySourcingActions(group)
	for _, candidate := range []struct {
		action string
		value  protocol.SourcingGender
	}{
		{action: "gender:不限", value: protocol.SourcingGenderAny},
		{action: "gender:男", value: protocol.SourcingGenderMale},
		{action: "gender:女", value: protocol.SourcingGenderFemale},
	} {
		if selected[candidate.action] {
			return candidate.value, nil
		}
	}
	return "", ErrInvalidSourcingView
}

func mapLegacySourcingFilterTypes(group legacySourcingFilterGroup) (bool, bool, error) {
	selected := selectedLegacySourcingActions(group)
	if selected["filterTypes:不限"] {
		return false, false, nil
	}
	return selected["filterTypes:过滤我已看过"], selected["filterTypes:过滤同事已聊"], nil
}

func deriveCandidateSelection(raw string) CandidateSelectionView {
	selection := CandidateSelectionView{MinScore: 5, TargetMin: 80, TargetMax: 90, MaleRatioLimit: 50}
	var parsed map[string]any
	if json.Unmarshal([]byte(raw), &parsed) != nil {
		return selection
	}
	selection.MinScore = clampLegacyInt(parsed["minScore"], 1, 10, selection.MinScore)
	selection.TargetMin = clampLegacyInt(parsed["targetMin"], 0, 150, selection.TargetMin)
	selection.TargetMax = clampLegacyInt(parsed["targetMax"], 0, 150, selection.TargetMax)
	if selection.TargetMin > selection.TargetMax {
		selection.TargetMin, selection.TargetMax = selection.TargetMax, selection.TargetMin
	}
	selection.MaleRatioLimit = clampLegacyInt(parsed["maleRatioLimit"], 0, 100, selection.MaleRatioLimit)
	return selection
}

func clampLegacyInt(value any, minimum, maximum, fallback int) int {
	var number int
	switch typed := value.(type) {
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return fallback
		}
		number = int(typed)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return fallback
		}
		number = parsed
	default:
		return fallback
	}
	if number < minimum {
		return minimum
	}
	if number > maximum {
		return maximum
	}
	return number
}
