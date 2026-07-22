package m5ai

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

const GreetingInputFormatVersion = "greeting-input-v1"

// GreetingInputV1 is the complete legacy-shaped resume projection consumed by
// the imported greeting prompt. Both fields are derived exclusively from the
// immutable resume snapshot; callers cannot inject a list display name.
type GreetingInputV1 struct {
	ResumeSummaryJSON string
	CareerState       string
}

// RenderGreetingInputV1 preserves all observed resume sections without the
// scoring path's rune truncation. The canonical snapshot has no independent
// full-text fact, so 简历全文 remains explicitly empty.
func RenderGreetingInputV1(snapshotJSON string) (GreetingInputV1, error) {
	if !utf8.ValidString(snapshotJSON) {
		return GreetingInputV1{}, errors.New("invalidResumeUTF8")
	}
	var source storedResume
	if err := json.Unmarshal([]byte(snapshotJSON), &source); err != nil || source.Basic == nil ||
		source.Expectations == nil || source.SelfEvaluation == nil || source.Education == nil ||
		source.WorkExperiences == nil {
		return GreetingInputV1{}, errors.New("missingRequiredSection")
	}

	basic := map[string]string{
		"姓名": "", "性别": "", "年龄": "", "工作年限": "", "最高学历": "", "求职状态": "", "现居": "",
	}
	for _, item := range *source.Basic {
		label := strings.TrimSpace(item.Label)
		if label == "" {
			return GreetingInputV1{}, errors.New("invalidResumeLabel")
		}
		switch label {
		case "工作经验":
			label = "工作年限"
		case "现居地":
			label = "现居"
		}
		mergeScoringValue(basic, label, item.Value)
	}

	expectations := map[string]string{"期望职位": "", "期望薪资": "", "最近投递": ""}
	for _, item := range *source.Expectations {
		label := strings.TrimSpace(item.Label)
		if label == "" {
			return GreetingInputV1{}, errors.New("invalidResumeLabel")
		}
		mergeScoringValue(expectations, label, item.Value)
	}

	view := struct {
		Basic           map[string]string `json:"基本"`
		Expectations    map[string]string `json:"期望"`
		SelfEvaluation  string            `json:"自评"`
		WorkExperiences string            `json:"工作经历"`
		Education       string            `json:"教育经历"`
		FullText        string            `json:"简历全文"`
	}{
		Basic: basic, Expectations: expectations,
		SelfEvaluation:  *source.SelfEvaluation,
		WorkExperiences: *source.WorkExperiences,
		Education:       *source.Education,
		FullText:        "",
	}
	raw, err := json.Marshal(view)
	if err != nil {
		return GreetingInputV1{}, err
	}
	return GreetingInputV1{ResumeSummaryJSON: string(raw), CareerState: basic["求职状态"]}, nil
}
