package m5ai

import (
	"encoding/json"
	"errors"
)

const scoringResumeToken = "resume_json"

// RenderScoringPrompt 按统一渲染规则把一份不可变简历 JSON 绑进打分文档:正文
// {resume_json} 换指针、数据落尾部【输入参数-简历】子块,从不截断。provider 上报
// 的 token 用量才是输入预算的权威边界。
func RenderScoringPrompt(prompt, resumeJSON string) (string, error) {
	return renderPromptWithInputs("打分", prompt, map[string]string{scoringResumeToken: resumeJSON})
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
