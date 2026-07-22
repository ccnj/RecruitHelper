package m5ai

import (
	"encoding/json"
	"errors"
	"strings"
)

const scoringResumePlaceholder = "{resume_json}"

// RenderScoringPrompt binds one immutable resume JSON value to one scoring
// prompt. It never truncates either input: exceeding the provider's approved
// input boundary is an explicit failure before any network request.
func RenderScoringPrompt(prompt, resumeJSON string) (string, error) {
	if strings.Count(prompt, scoringResumePlaceholder) != 1 {
		return "", errors.New("invalidScoringPrompt")
	}
	rendered := strings.Replace(prompt, scoringResumePlaceholder, resumeJSON, 1)
	if len([]byte(rendered)) > ReplyInputTokenLimit {
		return "", errors.New("scoringInputBudgetExceeded")
	}
	return rendered, nil
}

// ParseScoringSuggestion interprets only the score needed by deterministic
// code. Job-specific analysis/tag fields remain allowed but are discarded.
func ParseScoringSuggestion(raw string) (ScoringSuggestion, error) {
	object, err := decodeUniqueObject(raw)
	if err != nil {
		return ScoringSuggestion{}, err
	}

	aliases := [...]string{"score", "分数", "评分"}
	var scoreRaw json.RawMessage
	found := 0
	for _, alias := range aliases {
		if value, exists := object[alias]; exists {
			scoreRaw = value
			found++
		}
	}
	if found == 0 {
		return ScoringSuggestion{}, errors.New("missingScore")
	}
	if found != 1 {
		return ScoringSuggestion{}, errors.New("duplicateScore")
	}

	var score int
	if err := json.Unmarshal(scoreRaw, &score); err != nil || score < 1 || score > 10 {
		return ScoringSuggestion{}, errors.New("invalidScore")
	}
	return ScoringSuggestion{Score: score}, nil
}
