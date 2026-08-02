package patrol

import (
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
)

func TestM5ProviderFailureUsesFixedClasses(t *testing.T) {
	status, class := m5ProviderFailure(&m5ai.ProviderError{Class: "rateLimited"})
	if status != store.AIInvocationProviderRejected || class != "rateLimited" {
		t.Fatalf("rate limit 分类错误: %s/%s", status, class)
	}
	secret := errors.New("候选人手机号13800138000")
	status, class = m5ProviderFailure(secret)
	if status != store.AIInvocationTransportFailed || class != "transport" || class == secret.Error() {
		t.Fatalf("未知 provider 错误泄漏正文: %s/%s", status, class)
	}

	status, class = m5ProviderFailure(&m5ai.ProviderError{Class: "inputTokenBudgetExceeded"})
	if status != store.AIInvocationBudgetBlocked || class != "inputTokenBudgetExceeded" {
		t.Fatalf("响应后输入 token 超限分类错误: %s/%s", status, class)
	}

	payloadErr := &m5ai.ProviderError{Class: "requestPayloadTooLarge"}
	completion := m5CompletionFromProvider(
		"invocation-payload-cap", m5ai.CompletionResponse{}, payloadErr,
		5*time.Millisecond, time.Now(),
	)
	if completion.Status != store.AIInvocationTransportFailed ||
		completion.ErrorClass != "requestPayloadTooLarge" ||
		completion.FailureStage != m5ai.FailureStageRequestBuild ||
		completion.ErrorDetailCode != "requestPayloadTooLarge" ||
		completion.InputTokens != 0 || completion.CachedInputTokens != 0 ||
		completion.OutputTokens != 0 || completion.EstimatedCostMicros != 0 ||
		completion.OutputHash != "" || completion.UsageShape != "" ||
		completion.LatencyMs != 5 {
		t.Fatalf("请求运输上限终局错误: %+v", completion)
	}
}

func TestM5CompletionKeepsCompactTraceDiagnosticsWithoutRawContent(t *testing.T) {
	status := 429
	response := m5ai.CompletionResponse{
		Diagnostics: m5ai.CompletionDiagnostics{
			ProviderHTTPStatus: &status,
			RequestBytes:       1024,
			ResponseBytes:      64,
			TraceStatus:        m5ai.TraceStatusComplete,
		},
	}
	completion := m5CompletionFromProvider(
		"invocation-diagnostics",
		response,
		&m5ai.ProviderError{
			Class: "rateLimited", FailureStage: m5ai.FailureStageProviderHTTP,
			DetailCode: "rateLimited",
		},
		7*time.Millisecond,
		time.Now(),
	)
	if completion.Status != store.AIInvocationProviderRejected ||
		completion.FailureStage != m5ai.FailureStageProviderHTTP ||
		completion.ErrorDetailCode != "rateLimited" ||
		completion.ProviderHTTPStatus == nil || *completion.ProviderHTTPStatus != 429 ||
		completion.RequestBytes != 1024 || completion.ResponseBytes != 64 ||
		completion.TraceStatus != m5ai.TraceStatusComplete {
		t.Fatalf("provider 紧凑诊断丢失: %+v", completion)
	}

	traceUnavailable := m5CompletionFromProvider(
		"invocation-trace-unavailable",
		m5ai.CompletionResponse{
			JSONText:              "{}",
			Usage:                 m5ai.CompletionUsage{},
			ReasoningContentEmpty: true,
			Diagnostics: m5ai.CompletionDiagnostics{
				TraceStatus: m5ai.TraceStatusUnavailable, TraceErrorCode: "traceBeginFailed",
			},
		},
		nil,
		time.Millisecond,
		time.Now(),
	)
	if traceUnavailable.Status != store.AIInvocationOK ||
		traceUnavailable.FailureStage != m5ai.FailureStagePersistence ||
		traceUnavailable.ErrorDetailCode != "traceBeginFailed" ||
		traceUnavailable.TraceStatus != m5ai.TraceStatusUnavailable {
		t.Fatalf("成功调用的 trace 缺口未进入紧凑诊断: %+v", traceUnavailable)
	}
}
