package patrol

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
)

type diagnosticAdviceStub struct{}

func (diagnosticAdviceStub) ProviderName() string { return "fixture-provider" }
func (diagnosticAdviceStub) ModelName() string    { return "fixture-model" }
func (diagnosticAdviceStub) CompleteJSON(
	context.Context,
	m5ai.CompletionRequest,
) (m5ai.CompletionResponse, error) {
	return m5ai.CompletionResponse{}, errors.New("unused")
}

func TestAIDiagnosticsKeepKnownParserCodeAndBoundUnknownText(t *testing.T) {
	for _, code := range []string{"missingPhraseSequence", "invalidMeetingTimeType"} {
		known := store.AIInvocationCompletion{Status: store.AIInvocationOK}
		markBusinessParseFailure(&known, errors.New(code))
		if known.Status != store.AIInvocationInvalidOutput ||
			known.FailureStage != m5ai.FailureStageBusinessParse ||
			known.ErrorDetailCode != code {
			t.Fatalf("已知解析码未保留: %+v", known)
		}
	}

	unknown := store.AIInvocationCompletion{Status: store.AIInvocationOK}
	markBusinessParseFailure(&unknown, errors.New("候选人手机号13800138000"))
	if unknown.ErrorDetailCode != "unknown" ||
		strings.Contains(unknown.ErrorDetailCode, "13800138000") {
		t.Fatalf("未知解析错误泄露原文: %+v", unknown)
	}
}

func TestAIStdoutDiagnosticsContainOnlyStructuredSummary(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	defer slog.SetDefault(previous)

	secret := "候选人手机号13800138000"
	completion := m5CompletionFromProvider(
		"invocation-stdout-safe",
		m5ai.CompletionResponse{
			Diagnostics: m5ai.CompletionDiagnostics{
				RequestBytes: 321, TraceStatus: m5ai.TraceStatusUnavailable,
			},
		},
		errors.New(secret),
		9*time.Millisecond,
		time.Now(),
	)
	logAIInvocationOutcome(
		diagnosticAdviceStub{}, m5ai.PurposeReply, completion, secret,
	)

	text := output.String()
	for _, want := range []string{
		"invocation-stdout-safe", "fixture-provider", "fixture-model",
		"failureStage=transport", "errorDetailCode=transportUnavailable",
		"requestBytes=321", "traceStatus=unavailable", "traceErrorCode=traceUnavailable",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("stdout 缺少结构化字段 %q: %s", want, text)
		}
	}
	if strings.Contains(text, secret) || strings.Contains(text, "13800138000") {
		t.Fatalf("stdout 泄露未经分类的错误原文: %s", text)
	}
}
