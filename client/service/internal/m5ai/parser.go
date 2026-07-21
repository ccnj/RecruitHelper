package m5ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

func decodeUniqueObject(raw string) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	token, err := dec.Token()
	if err != nil {
		return nil, errors.New("invalidJSON")
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil, errors.New("invalidJSON")
	}
	values := make(map[string]json.RawMessage)
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return nil, errors.New("invalidJSON")
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("invalidJSON")
		}
		if _, exists := values[key]; exists {
			return nil, errors.New("duplicateOutputKey")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, errors.New("invalidJSON")
		}
		values[key] = bytes.Clone(value)
	}
	if _, err := dec.Token(); err != nil {
		return nil, errors.New("invalidJSON")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalidJSON")
	}
	return values, nil
}

func ParseIntentSuggestion(raw string) (IntentSuggestion, error) {
	object, err := decodeUniqueObject(raw)
	if err != nil {
		return IntentSuggestion{}, err
	}
	allowed := map[string]bool{"信号": true, "signal": true, "理由": true, "reason": true}
	for key := range object {
		if !allowed[key] {
			return IntentSuggestion{}, errors.New("unknownOutputKey")
		}
	}
	if _, chinese := object["信号"]; chinese {
		if _, english := object["signal"]; english {
			return IntentSuggestion{}, errors.New("duplicateIntentSignal")
		}
	}
	signalRaw, exists := object["信号"]
	if !exists {
		signalRaw, exists = object["signal"]
	}
	if !exists {
		return IntentSuggestion{}, errors.New("missingIntentSignal")
	}
	var signal string
	if json.Unmarshal(signalRaw, &signal) != nil {
		return IntentSuggestion{}, errors.New("invalidIntentSignal")
	}
	labels := map[string]IntentLabel{
		"有意向": IntentInterested,
		"拒绝":  IntentRejected,
		"中性":  IntentNeutral,
	}
	label, ok := labels[signal]
	if !ok {
		return IntentSuggestion{}, errors.New("invalidIntentSignal")
	}
	return IntentSuggestion{Label: label}, nil
}

func ParseReplySuggestion(raw string) (ReplySuggestion, error) {
	object, err := decodeUniqueObject(raw)
	if err != nil {
		return ReplySuggestion{}, err
	}
	phrasesRaw, exists := object["话术_序列"]
	if !exists {
		return ReplySuggestion{}, errors.New("missingPhraseSequence")
	}
	for key := range object {
		if key != "话术_序列" && key != "动作" {
			return ReplySuggestion{}, errors.New("unknownOutputKey")
		}
	}
	var phrases []string
	if err := json.Unmarshal(phrasesRaw, &phrases); err != nil || phrases == nil {
		return ReplySuggestion{}, errors.New("invalidPhraseSequenceType")
	}
	trimmed := make([]string, 0, len(phrases))
	for _, phrase := range phrases {
		if value := strings.TrimSpace(phrase); value != "" {
			trimmed = append(trimmed, value)
		}
	}
	text := strings.Join(trimmed, "\n")
	if err := ValidateSendText(text); err != nil {
		return ReplySuggestion{}, err
	}
	return ReplySuggestion{Text: text}, nil
}

type ShortCircuitResult struct {
	Matched bool
	Label   IntentLabel
	Source  string
	RuleID  string
}

// ClassifyIntentShortCircuit intentionally has no production rules in M5-A:
// batch 0B observed zero qualifying samples. Empty turns retain their explicit
// deterministic neutral meaning; every non-empty ordinary text turn proceeds
// to the provider.
func ClassifyIntentShortCircuit(orderedInboundTexts []string) ShortCircuitResult {
	if len(orderedInboundTexts) == 0 {
		return ShortCircuitResult{Matched: true, Label: IntentNeutral, Source: "emptyTurn"}
	}
	return ShortCircuitResult{}
}
