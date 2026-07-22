package m5ai

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
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

// SourcingView is an in-memory executable projection. Prompts and raw filters
// stay inside the local brain and must never be returned by management APIs.
type SourcingView struct {
	ScoringPrompt      string
	GreetingPrompt     string
	JobFilters         json.RawMessage
	CandidateSelection CandidateSelectionView
	MappingVersion     string
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
	greeting := derivedGreetingPrompt(documents["招呼语"])
	if strings.TrimSpace(greeting) == "" || strings.Count(greeting, "{resume_summary_json}") != 1 ||
		strings.Count(greeting, "{career_state}") != 1 {
		return SourcingView{}, ErrInvalidSourcingView
	}

	filters := json.RawMessage(strings.TrimSpace(documents["职位筛选"]))
	if len(filters) == 0 {
		filters = json.RawMessage("[]")
	}
	var filterItems []map[string]any
	if json.Unmarshal(filters, &filterItems) != nil {
		return SourcingView{}, ErrInvalidSourcingView
	}

	return SourcingView{
		ScoringPrompt: scoring, GreetingPrompt: greeting,
		JobFilters:         append(json.RawMessage(nil), filters...),
		CandidateSelection: deriveCandidateSelection(documents["候选人筛选"]),
		MappingVersion:     SourcingMappingVersion,
	}, nil
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
