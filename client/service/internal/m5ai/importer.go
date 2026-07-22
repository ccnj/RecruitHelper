package m5ai

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	sourceKindLocalImport     = "localImport"
	sourceKindLegacyJobConfig = "legacyJobConfig"
)

var ErrInvalidJobConfig = errors.New("职位 AI 配置整包无效")

type documentMap map[string]string

// UnmarshalJSON rejects duplicate doc_type keys instead of silently keeping
// the last value. That ambiguity cannot produce an immutable source package.
func (m *documentMap) UnmarshalJSON(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return errors.New("documents 必须是对象")
	}
	out := make(documentMap)
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok || strings.TrimSpace(key) == "" {
			return errors.New("documents 含空 doc_type")
		}
		if _, exists := out[key]; exists {
			return fmt.Errorf("documents 含重复 doc_type: %s", key)
		}
		var content string
		if err := dec.Decode(&content); err != nil {
			return fmt.Errorf("document %s 不是字符串: %w", key, err)
		}
		out[key] = content
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	*m = out
	return nil
}

type legacyJob struct {
	ID          json.Number `json:"id"`
	Name        string      `json:"name"`
	Environment string      `json:"environment"`
}

type legacyPromptBlock struct {
	Prompt  string  `json:"prompt"`
	APIKey  *string `json:"apiKey"`
	Model   *string `json:"model"`
	BaseURL *string `json:"baseUrl"`
}

type legacyContentBlock struct {
	Content string `json:"content"`
}

type legacyJobBundle struct {
	Job             *legacyJob         `json:"job"`
	Documents       documentMap        `json:"documents"`
	Scoring         legacyPromptBlock  `json:"scoring"`
	Greeting        legacyPromptBlock  `json:"greeting"`
	Communication   legacyPromptBlock  `json:"communication"`
	Intent          legacyPromptBlock  `json:"intent"`
	SilenceFollowup legacyPromptBlock  `json:"silenceFollowup"`
	Facts           legacyContentBlock `json:"facts"`
	FixedPhrases    legacyContentBlock `json:"fixedPhrases"`
	FixedRules      legacyContentBlock `json:"fixedRules"`
}

type legacyPlural struct {
	CurrentJobID json.Number       `json:"currentJobId"`
	Jobs         []legacyJobBundle `json:"jobs"`
}

// ImportLegacyJobConfig accepts either the production single response or the
// production plural response with includeDocuments=true. It imports every job
// in a plural response; choosing which one applies to a profile remains a
// separate explicit binding fact.
func ImportLegacyJobConfig(raw []byte, now time.Time) ([]ContextRevision, error) {
	return importLegacyJobConfig(raw, now, sourceKindLocalImport)
}

// ImportLegacyJobConfigFromBackend uses the same compatibility boundary as a
// local import while preserving that the immutable material came directly
// from the approved old-backend configuration plane.
func ImportLegacyJobConfigFromBackend(raw []byte, now time.Time) ([]ContextRevision, error) {
	return importLegacyJobConfig(raw, now, sourceKindLegacyJobConfig)
}

func importLegacyJobConfig(raw []byte, now time.Time, sourceKind string) ([]ContextRevision, error) {
	if now.IsZero() {
		return nil, fmt.Errorf("%w: 缺少导入时刻", ErrInvalidJobConfig)
	}
	var shape map[string]json.RawMessage
	if err := decodeUseNumber(raw, &shape); err != nil {
		return nil, fmt.Errorf("%w: JSON 无效", ErrInvalidJobConfig)
	}
	if _, plural := shape["jobs"]; plural {
		var payload legacyPlural
		if err := decodeUseNumber(raw, &payload); err != nil {
			return nil, fmt.Errorf("%w: 多职位整包无效: %v", ErrInvalidJobConfig, err)
		}
		if len(payload.Jobs) == 0 {
			return nil, fmt.Errorf("%w: 多职位整包为空", ErrInvalidJobConfig)
		}
		out := make([]ContextRevision, 0, len(payload.Jobs))
		seen := make(map[string]struct{}, len(payload.Jobs))
		for i := range payload.Jobs {
			revision, err := importBundle(payload.Jobs[i], now, sourceKind)
			if err != nil {
				return nil, fmt.Errorf("%w: jobs[%d]: %v", ErrInvalidJobConfig, i, err)
			}
			if _, exists := seen[revision.ContextID]; exists {
				return nil, fmt.Errorf("%w: 多职位整包含重复 job id", ErrInvalidJobConfig)
			}
			seen[revision.ContextID] = struct{}{}
			out = append(out, revision)
		}
		return out, nil
	}
	var payload legacyJobBundle
	if err := decodeUseNumber(raw, &payload); err != nil {
		return nil, fmt.Errorf("%w: 单职位整包无效: %v", ErrInvalidJobConfig, err)
	}
	revision, err := importBundle(payload, now, sourceKind)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidJobConfig, err)
	}
	return []ContextRevision{revision}, nil
}

func decodeUseNumber(raw []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(target); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("JSON 含尾随内容")
	}
	return nil
}

func importBundle(bundle legacyJobBundle, now time.Time, sourceKind string) (ContextRevision, error) {
	if bundle.Job == nil || bundle.Job.ID.String() == "" || strings.TrimSpace(bundle.Job.Name) == "" {
		return ContextRevision{}, errors.New("缺少 job id/name")
	}
	if jobID, err := strconv.ParseInt(bundle.Job.ID.String(), 10, 64); err != nil || jobID <= 0 {
		return ContextRevision{}, errors.New("job id 不是正整数")
	}
	if len(bundle.Documents) == 0 {
		return ContextRevision{}, errors.New("documents 为空；includeDocuments=false 不可导入")
	}
	if err := validateDirectViews(bundle); err != nil {
		return ContextRevision{}, err
	}
	replyPrompt, replyOK := bundle.Documents["多轮沟通"]
	intentPrompt, intentOK := bundle.Documents["意向判断"]
	facts, factsOK := bundle.Documents["客户事实库"]
	if !replyOK || !intentOK || !factsOK || strings.TrimSpace(replyPrompt) == "" || strings.TrimSpace(intentPrompt) == "" {
		return ContextRevision{}, errors.New("缺少多轮沟通、意向判断或客户事实库原文")
	}
	if _, err := ValidatePromptTokens("多轮沟通", replyPrompt); err != nil {
		return ContextRevision{}, err
	}
	if _, err := ValidatePromptTokens("意向判断", intentPrompt); err != nil {
		return ContextRevision{}, err
	}
	if err := requireInputTokens("多轮沟通", replyPrompt, "简历", "推荐时段", "对话历史"); err != nil {
		return ContextRevision{}, err
	}
	if err := requireInputTokens("意向判断", intentPrompt, "回复", "招呼语"); err != nil {
		return ContextRevision{}, err
	}

	documents := make([]JobConfigDocument, 0, len(bundle.Documents))
	for docType, content := range bundle.Documents {
		documents = append(documents, JobConfigDocument{DocType: docType, Content: content})
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].DocType < documents[j].DocType })
	sourceJobRef := bundle.Job.ID.String()
	contextSeed := sha256.Sum256([]byte(sourceKind + "\x00" + sourceJobRef))
	contextID := "ctx-" + hex.EncodeToString(contextSeed[:12])
	canonical := struct {
		ContextID      string                   `json:"contextId"`
		SourceKind     string                   `json:"sourceKind"`
		SourceJobRef   string                   `json:"sourceJobRef"`
		DisplayName    string                   `json:"displayName"`
		Environment    string                   `json:"environment"`
		MappingVersion string                   `json:"mappingVersion"`
		SourcePackage  JobConfigDocumentPackage `json:"sourcePackage"`
	}{
		ContextID: contextID, SourceKind: sourceKind, SourceJobRef: sourceJobRef,
		DisplayName: bundle.Job.Name, Environment: bundle.Job.Environment, MappingVersion: MappingVersion,
		SourcePackage: JobConfigDocumentPackage{Documents: documents},
	}
	canonicalRaw, err := json.Marshal(canonical)
	if err != nil {
		return ContextRevision{}, err
	}
	revisionDigest := sha256.Sum256(canonicalRaw)
	return ContextRevision{
		ContextID: contextID, RevisionHash: hex.EncodeToString(revisionDigest[:]),
		SourceKind: sourceKind, SourceJobRef: sourceJobRef,
		DisplayName: bundle.Job.Name, Environment: bundle.Job.Environment,
		SourcePackage: canonical.SourcePackage,
		Communication: CommunicationView{
			ReplyPrompt: replyPrompt, IntentPrompt: intentPrompt, CustomerFacts: facts,
			MappingVersion: MappingVersion,
		},
		CreatedAt: now,
	}, nil
}

func validateDirectViews(bundle legacyJobBundle) error {
	direct := []struct {
		docType string
		value   string
	}{
		{"打分", bundle.Scoring.Prompt},
		{"多轮沟通", bundle.Communication.Prompt},
		{"意向判断", bundle.Intent.Prompt},
		{"沉默追问", bundle.SilenceFollowup.Prompt},
		{"客户事实库", bundle.Facts.Content},
		{"固定话术", bundle.FixedPhrases.Content},
		{"固定规则", bundle.FixedRules.Content},
	}
	for _, field := range direct {
		raw, exists := bundle.Documents[field.docType]
		if exists && raw != field.value {
			return fmt.Errorf("documents 与结构化区冲突: %s", field.docType)
		}
		if !exists && field.value != "" {
			return fmt.Errorf("结构化区无法无损补回缺失文档: %s", field.docType)
		}
	}
	// Greeting is the sole prompt block whose backend may derive prompt from a
	// JSON document. Validate the reversible transformation without retaining
	// any provider credential from the block.
	if raw, exists := bundle.Documents["招呼语"]; exists {
		if derivedGreetingPrompt(raw) != bundle.Greeting.Prompt {
			return errors.New("documents 与结构化区冲突: 招呼语")
		}
	} else if bundle.Greeting.Prompt != "" {
		return errors.New("结构化区无法无损补回缺失文档: 招呼语")
	}
	return nil
}

func derivedGreetingPrompt(raw string) string {
	prompt, _, err := deriveGreetingConfig(raw)
	if err != nil {
		return ""
	}
	return prompt
}

func deriveGreetingConfig(raw string) (string, bool, error) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return raw, false, nil
	}
	prompt, ok := parsed["prompt"].(string)
	if !ok {
		return "", false, errors.New("招呼语 JSON wrapper 缺少 prompt")
	}
	usePlatformDefault := false
	if value, exists := parsed["usePlatformDefault"]; exists {
		var valid bool
		usePlatformDefault, valid = value.(bool)
		if !valid {
			return "", false, errors.New("招呼语 JSON wrapper 的 usePlatformDefault 无效")
		}
	}
	return prompt, usePlatformDefault, nil
}
