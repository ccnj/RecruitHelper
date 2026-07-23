package patrol

import (
	"errors"
	"log/slog"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
)

func m5ProviderFailureDiagnostics(err error) (string, string) {
	var providerErr *m5ai.ProviderError
	if !errors.As(err, &providerErr) {
		return m5ai.FailureStageTransport, "transportUnavailable"
	}
	stage := safeFailureStage(providerErr.FailureStage)
	detail := safeProviderDetailCode(providerErr.DetailCode)
	if stage != "" && detail != "" {
		return stage, detail
	}
	switch providerErr.Class {
	case "budgetBlocked":
		return m5ai.FailureStageRequestBuild, "outputTokenBudgetExceeded"
	case "inputTokenBudgetExceeded":
		return m5ai.FailureStageResponseDecode, "inputTokenBudgetExceeded"
	case "authentication", "rateLimited", "providerRejected", "providerUnavailable":
		return m5ai.FailureStageProviderHTTP, providerErr.Class
	case "responseInvalid":
		return m5ai.FailureStageResponseDecode, "responseMalformed"
	case "timeout":
		return m5ai.FailureStageTransport, "transportTimeout"
	case "transport":
		return m5ai.FailureStageTransport, "transportUnavailable"
	case "requestInvalid":
		return m5ai.FailureStageRequestBuild, "requestInvalid"
	case "requestPayloadTooLarge":
		return m5ai.FailureStageRequestBuild, "requestPayloadTooLarge"
	default:
		return m5ai.FailureStageTransport, "unknown"
	}
}

func markBusinessParseFailure(completion *store.AIInvocationCompletion, err error) {
	completion.Status = store.AIInvocationInvalidOutput
	completion.ErrorClass = "invalidOutput"
	completion.FailureStage = m5ai.FailureStageBusinessParse
	completion.ErrorDetailCode = safeBusinessParseCode(err)
}

func markReasoningUsageUnsafe(completion *store.AIInvocationCompletion) {
	completion.FailureStage = m5ai.FailureStageResponseDecode
	completion.ErrorDetailCode = "reasoningUsageUnsafe"
}

func markReasoningUsageInvalidOutput(completion *store.AIInvocationCompletion) {
	completion.Status = store.AIInvocationInvalidOutput
	completion.ErrorClass = "reasoningUsageUnsafe"
	markReasoningUsageUnsafe(completion)
}

func markReducerRejected(completion *store.AIInvocationCompletion) {
	if completion.Status != store.AIInvocationOK ||
		(completion.FailureStage != "" && completion.FailureStage != m5ai.FailureStagePersistence) {
		return
	}
	completion.FailureStage = m5ai.FailureStageReducer
	completion.ErrorDetailCode = "reducerRejected"
}

func safeFailureStage(stage string) string {
	switch stage {
	case m5ai.FailureStageRequestBuild, m5ai.FailureStageTransport,
		m5ai.FailureStageProviderHTTP, m5ai.FailureStageResponseDecode,
		m5ai.FailureStageBusinessParse, m5ai.FailureStageReducer,
		m5ai.FailureStagePersistence:
		return stage
	default:
		return ""
	}
}

func safeProviderDetailCode(code string) string {
	switch code {
	case "unknownPurpose", "requestMissingFields", "traceMetadataMissing",
		"outputTokenBudgetExceeded", "requestMarshalFailed", "requestPayloadTooLarge",
		"requestInvalid", "transportTimeout", "transportCanceled", "transportUnavailable",
		"responseReadFailed", "responseTooLarge", "authentication", "rateLimited",
		"providerRejected", "providerUnavailable", "responseMalformed",
		"responseChoiceInvalid", "responseFinishReasonInvalid", "responseContentMissing",
		"usageMissing", "usageInvalid", "inputTokenBudgetExceeded":
		return code
	default:
		return ""
	}
}

func safeBusinessParseCode(err error) string {
	if err == nil {
		return "unknown"
	}
	switch err.Error() {
	case "invalidJSON", "duplicateOutputKey", "unknownOutputKey",
		"duplicateIntentSignal", "missingIntentSignal", "invalidIntentSignal",
		"missingPhraseSequence", "invalidPhraseSequenceType", "emptyPhraseSequence",
		"emptyReplyText",
		"sendTextLimit", "missingScore", "duplicateScore", "invalidScore",
		"missingGreetingText", "invalidGreetingText":
		return err.Error()
	default:
		return "unknown"
	}
}

func safeTraceErrorCode(code string) string {
	switch code {
	case "traceStoreUnavailable", "traceBeginFailed", "traceFinishFailed":
		return code
	default:
		return "traceUnavailable"
	}
}

func logAIInvocationOutcome(
	advice AdviceExecutor,
	purpose m5ai.CompletionPurpose,
	completion store.AIInvocationCompletion,
	traceErrorCode string,
) {
	attrs := aiInvocationLogAttrs(advice, purpose, completion)
	if completion.Status != store.AIInvocationOK ||
		(completion.FailureStage != "" && completion.FailureStage != m5ai.FailureStagePersistence) {
		slog.Warn("AI 调用失败", attrs...)
	}
	if completion.TraceStatus != "" && completion.TraceStatus != m5ai.TraceStatusComplete {
		traceAttrs := append(
			attrs,
			slog.String("traceErrorCode", safeTraceErrorCode(traceErrorCode)),
		)
		slog.Warn("AI 原文追踪不完整", traceAttrs...)
	}
}

func logAIInvocationPersistenceFailure(
	advice AdviceExecutor,
	purpose m5ai.CompletionPurpose,
	completion store.AIInvocationCompletion,
) {
	failed := completion
	failed.FailureStage = m5ai.FailureStagePersistence
	failed.ErrorDetailCode = "brainPersistenceFailed"
	slog.Error("AI 调用终局持久化失败", aiInvocationLogAttrs(advice, purpose, failed)...)
}

func aiInvocationLogAttrs(
	advice AdviceExecutor,
	purpose m5ai.CompletionPurpose,
	completion store.AIInvocationCompletion,
) []any {
	httpStatus := 0
	hasHTTPStatus := completion.ProviderHTTPStatus != nil
	if hasHTTPStatus {
		httpStatus = *completion.ProviderHTTPStatus
	}
	reasoningTokens := 0
	hasReasoningTokens := completion.ReasoningTokens != nil
	if hasReasoningTokens {
		reasoningTokens = *completion.ReasoningTokens
	}
	return []any{
		slog.String("invocationId", completion.InvocationID),
		slog.String("purpose", string(purpose)),
		slog.String("provider", advice.ProviderName()),
		slog.String("model", advice.ModelName()),
		slog.String("status", string(completion.Status)),
		slog.String("failureStage", completion.FailureStage),
		slog.String("errorDetailCode", completion.ErrorDetailCode),
		slog.Bool("hasProviderHTTPStatus", hasHTTPStatus),
		slog.Int("providerHTTPStatus", httpStatus),
		slog.Int("requestBytes", completion.RequestBytes),
		slog.Int("responseBytes", completion.ResponseBytes),
		slog.Int("inputTokens", completion.InputTokens),
		slog.Int("cachedInputTokens", completion.CachedInputTokens),
		slog.Int("outputTokens", completion.OutputTokens),
		slog.Bool("hasReasoningTokens", hasReasoningTokens),
		slog.Int("reasoningTokens", reasoningTokens),
		slog.Int64("latencyMs", completion.LatencyMs),
		slog.String("traceStatus", string(completion.TraceStatus)),
	}
}
