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
	ReplyPhraseMaxItems         = 5
	HistoryLimit                = 20
	IntentInputTokenLimit       = 8000
	ReplyInputTokenLimit        = 16000
	GreetingInputTokenLimit     = ReplyInputTokenLimit
	IntentOutputTokenLimit      = 64
	ReplyOutputTokenLimit       = 512
	ScoringOutputTokenLimit     = 512
	GreetingOutputTokenLimit    = ReplyOutputTokenLimit
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

type ReplyAction string

const (
	ReplyActionNone               ReplyAction = ""
	ReplyActionStartOnlineMeeting ReplyAction = "startOnlineMeeting"
	ReplyActionInviteWechat       ReplyAction = "inviteWechat"
)

type ReplySuggestion struct {
	// Phrases preserves the provider's 话术_序列 item boundaries. Text remains
	// the canonical newline-joined compatibility summary; dispatch planners
	// must use Phrases instead of splitting Text.
	Phrases     []string
	Text        string
	Action      ReplyAction
	MeetingTime string
}

type ScoringSuggestion struct {
	Score int
}

type GreetingSuggestion struct {
	Text string
}

type CompletionPurpose string

const (
	PurposeIntent   CompletionPurpose = "intent"
	PurposeReply    CompletionPurpose = "reply"
	PurposeScoring  CompletionPurpose = "scoring"
	PurposeGreeting CompletionPurpose = "greeting"
)

type CompletionRequest struct {
	InvocationID        string
	Purpose             CompletionPurpose
	ContextRevisionHash string
	PromptRevision      string
	UserContent         string
	MaxOutputTokens     int
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
	Diagnostics           CompletionDiagnostics
}

type TraceStatus string

const (
	TraceStatusComplete            TraceStatus = "complete"
	TraceStatusUnavailable         TraceStatus = "unavailable"
	TraceStatusResponseUnavailable TraceStatus = "responseUnavailable"
)

const (
	FailureStageRequestBuild   = "requestBuild"
	FailureStageTransport      = "transport"
	FailureStageProviderHTTP   = "providerHTTP"
	FailureStageResponseDecode = "responseDecode"
	FailureStageBusinessParse  = "businessParse"
	FailureStageReducer        = "reducer"
	FailureStagePersistence    = "persistence"
)

// CompletionDiagnostics is the PII-free bridge from the provider adapter to
// brain.db and stdout. Raw request/response bodies remain exclusively in the
// standalone ai-traces.db.
type CompletionDiagnostics struct {
	ProviderHTTPStatus *int
	RequestBytes       int
	ResponseBytes      int
	TraceStatus        TraceStatus
	TraceErrorCode     string
}

type LLMProvider interface {
	CompleteJSON(ctx context.Context, request CompletionRequest) (CompletionResponse, error)
}
