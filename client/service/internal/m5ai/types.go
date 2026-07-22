// Package m5ai contains the platform-independent M5 AI seam. It owns prompt
// rendering and provider wire semantics, but never decides or performs a
// browser side effect.
package m5ai

import (
	"context"
	"time"
)

const (
	MappingVersion              = "m5-communication-v1"
	HistoryFormatVersion        = 1
	ScheduleFormatVersion       = 1
	ProviderAssemblyVersion     = 1
	DialogueRenderFormatVersion = "m5-history1-schedule1-assembly1"
	SendTextMaxUTF8Bytes        = 2048
	HistoryLimit                = 20
	IntentInputTokenLimit       = 8000
	ReplyInputTokenLimit        = 16000
	IntentOutputTokenLimit      = 64
	ReplyOutputTokenLimit       = 512
	ScoringOutputTokenLimit     = 512
)

type JobConfigDocument struct {
	DocType string `json:"docType"`
	Content string `json:"content"`
}

type JobConfigDocumentPackage struct {
	Documents []JobConfigDocument `json:"documents"`
}

type CommunicationView struct {
	ReplyPrompt    string `json:"replyPrompt"`
	IntentPrompt   string `json:"intentPrompt"`
	CustomerFacts  string `json:"customerFacts"`
	MappingVersion string `json:"mappingVersion"`
}

// ContextRevision is the repository-owned, immutable representation of one
// imported legacy job configuration revision. Provider credentials are
// deliberately absent.
type ContextRevision struct {
	ContextID     string                   `json:"contextId"`
	RevisionHash  string                   `json:"revisionHash"`
	SourceKind    string                   `json:"sourceKind"`
	SourceJobRef  string                   `json:"sourceJobRef"`
	DisplayName   string                   `json:"displayName"`
	Environment   string                   `json:"environment"`
	SourcePackage JobConfigDocumentPackage `json:"sourcePackage"`
	Communication CommunicationView        `json:"communication"`
	CreatedAt     time.Time                `json:"createdAt"`
}

type AdviceMessage struct {
	Seq       int64  `json:"seq"`
	Direction string `json:"direction"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	Retracted bool   `json:"retracted,omitempty"`
}

type IntentLabel string

const (
	IntentInterested IntentLabel = "interested"
	IntentRejected   IntentLabel = "rejected"
	IntentNeutral    IntentLabel = "neutral"
)

type IntentSuggestion struct {
	Label IntentLabel
}

type ReplySuggestion struct {
	Text string
}

type ScoringSuggestion struct {
	Score int
}

type CompletionPurpose string

const (
	PurposeIntent  CompletionPurpose = "intent"
	PurposeReply   CompletionPurpose = "reply"
	PurposeScoring CompletionPurpose = "scoring"
)

type CompletionRequest struct {
	Purpose         CompletionPurpose
	UserContent     string
	MaxOutputTokens int
}

type CompletionUsage struct {
	InputTokens       int
	CachedInputTokens int
	OutputTokens      int
	ReasoningTokens   *int
}

type CompletionResponse struct {
	JSONText              string
	Usage                 CompletionUsage
	ReasoningContentEmpty bool
}

type LLMProvider interface {
	CompleteJSON(ctx context.Context, request CompletionRequest) (CompletionResponse, error)
}
