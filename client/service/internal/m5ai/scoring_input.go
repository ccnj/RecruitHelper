package m5ai

import (
	"encoding/json"
	"errors"
	"strings"
)

const (
	scoringSelfEvaluationLimit = 1500
	scoringWorkLimit           = 1500
	scoringEducationLimit      = 500
)

// RenderScoringInputV1 projects the repository-owned resume snapshot into the
// stable Chinese shape consumed by the legacy job scoring prompt. It preserves
// only observed facts, never infers missing attributes, and deliberately omits
// the candidate name from the structured provider input.
func RenderScoringInputV1(snapshotJSON string) (string, error) {
	var source storedResume
	if err := json.Unmarshal([]byte(snapshotJSON), &source); err != nil || source.Basic == nil ||
		source.Expectations == nil || source.SelfEvaluation == nil || source.Education == nil ||
		source.WorkExperiences == nil {
		return "", errors.New("missingRequiredSection")
	}

	basic := map[string]string{
		"性别": "", "年龄": "", "工作年限": "", "最高学历": "", "求职状态": "", "现居": "",
	}
	for _, item := range *source.Basic {
		label := strings.TrimSpace(item.Label)
		if label == "" {
			return "", errors.New("invalidResumeLabel")
		}
		if label == "姓名" {
			continue
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
			return "", errors.New("invalidResumeLabel")
		}
		mergeScoringValue(expectations, label, item.Value)
	}

	// Struct field order is the frozen top-level byte order. encoding/json sorts
	// map keys, so equivalent snapshots always produce the same input bytes.
	view := struct {
		Basic           map[string]string `json:"基本"`
		Expectations    map[string]string `json:"期望"`
		SelfEvaluation  string            `json:"自评"`
		WorkExperiences string            `json:"工作经历"`
		Education       string            `json:"教育经历"`
		FullText        string            `json:"简历全文"`
	}{
		Basic: basic, Expectations: expectations,
		SelfEvaluation:  truncateScoringRunes(*source.SelfEvaluation, scoringSelfEvaluationLimit),
		WorkExperiences: truncateScoringRunes(*source.WorkExperiences, scoringWorkLimit),
		Education:       truncateScoringRunes(*source.Education, scoringEducationLimit),
		FullText:        "",
	}
	raw, err := json.Marshal(view)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func mergeScoringValue(target map[string]string, label, value string) {
	value = strings.TrimSpace(value)
	if current := target[label]; current != "" && value != "" && current != value {
		target[label] = current + "\n" + value
		return
	}
	if value != "" || target[label] == "" {
		target[label] = value
	}
}

func truncateScoringRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
