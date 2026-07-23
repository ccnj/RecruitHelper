package store

import (
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
)

func TestValidateAIInvocationCompletionAcceptsHistoricalEmptyDiagnostics(t *testing.T) {
	completion := AIInvocationCompletion{
		InvocationID: "invocation-diagnostics-historical",
		Status:       AIInvocationTransportFailed,
		ErrorClass:   "timeout",
		FinishedAt:   time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC),
	}
	if err := validateInvocationCompletion(completion); err != nil {
		t.Fatalf("历史调用的空诊断字段应继续合法: %v", err)
	}
}

func TestValidateAIInvocationCompletionRejectsUnsafeDiagnostics(t *testing.T) {
	statusOK := 200
	valid := AIInvocationCompletion{
		InvocationID:       "invocation-diagnostics-validation",
		Status:             AIInvocationProviderRejected,
		OutputHash:         "provider-response-hash",
		ErrorClass:         "providerRejected",
		FailureStage:       AIInvocationFailureStageProviderHTTP,
		ErrorDetailCode:    "providerRejected",
		ProviderHTTPStatus: &statusOK,
		RequestBytes:       1024,
		ResponseBytes:      128,
		TraceStatus:        m5ai.TraceStatusComplete,
		FinishedAt:         time.Date(2026, 7, 23, 10, 1, 0, 0, time.UTC),
	}
	if err := validateInvocationCompletion(valid); err != nil {
		t.Fatalf("合法紧凑诊断被拒绝: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*AIInvocationCompletion)
	}{
		{name: "负请求字节", mutate: func(value *AIInvocationCompletion) { value.RequestBytes = -1 }},
		{name: "负响应字节", mutate: func(value *AIInvocationCompletion) { value.ResponseBytes = -1 }},
		{name: "非法HTTP状态", mutate: func(value *AIInvocationCompletion) {
			status := 700
			value.ProviderHTTPStatus = &status
		}},
		{name: "未知失败阶段", mutate: func(value *AIInvocationCompletion) { value.FailureStage = "providerPrivateLayer" }},
		{name: "错误码混入原文", mutate: func(value *AIInvocationCompletion) {
			value.ErrorDetailCode = "provider said candidate name"
		}},
		{name: "未知追踪状态", mutate: func(value *AIInvocationCompletion) {
			value.TraceStatus = m5ai.TraceStatus("partiallyMaybe")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if err := validateInvocationCompletion(candidate); !errors.Is(err, ErrAIInvocationInvalid) {
				t.Fatalf("非法诊断应被拒绝: completion=%+v err=%v", candidate, err)
			}
		})
	}
}

func TestAIInvocationCompactDiagnosticsPersistAndParticipateInIdempotency(t *testing.T) {
	finishedAt := time.Date(2026, 7, 23, 11, 0, 0, 0, time.UTC)
	httpStatus := 429
	completion := AIInvocationCompletion{
		Status:              AIInvocationProviderRejected,
		OutputHash:          "provider-response-hash",
		ErrorClass:          "providerRejected",
		FailureStage:        AIInvocationFailureStageProviderHTTP,
		ErrorDetailCode:     "providerRejected",
		ProviderHTTPStatus:  &httpStatus,
		RequestBytes:        4096,
		ResponseBytes:       256,
		TraceStatus:         m5ai.TraceStatusComplete,
		LatencyMs:           80,
		EstimatedCostMicros: 12,
		FinishedAt:          finishedAt,
	}

	t.Run("M5意向", func(t *testing.T) {
		s := openTest(t)
		_, turn := seedFrozenDialogueTurn(t, s, "profile-diagnostics-m5")
		completion.InvocationID = "invocation-diagnostics-m5"
		if _, err := s.ReserveAIInvocation(ReserveAIInvocationRequest{
			InvocationID: completion.InvocationID,
			TurnID:       turn.TurnID,
			Purpose:      m5ai.PurposeIntent,
			Attempt:      1,
			Provider:     "deepseek",
			Model:        "deepseek-v4-pro",
			InputHash:    "input-diagnostics-m5",
			CreatedAt:    finishedAt.Add(-time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
			Completion: completion,
			Label:      m5ai.IntentNeutral,
			Source:     DialogueIntentLLMFailure,
		}); err != nil {
			t.Fatal(err)
		}
		var stored AIInvocation
		if err := s.db.First(&stored, "invocation_id = ?", completion.InvocationID).Error; err != nil {
			t.Fatal(err)
		}
		assertAIInvocationDiagnostics(t, stored.FailureStage, stored.ErrorDetailCode,
			stored.ProviderHTTPStatus, stored.RequestBytes, stored.ResponseBytes, stored.TraceStatus)

		if _, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
			Completion: completion,
			Label:      m5ai.IntentNeutral,
			Source:     DialogueIntentLLMFailure,
		}); err != nil {
			t.Fatalf("相同诊断完成应幂等: %v", err)
		}
		conflicting := completion
		conflicting.ResponseBytes++
		if _, err := s.CompleteIntentInvocation(CompleteIntentInvocationRequest{
			Completion: conflicting,
			Label:      m5ai.IntentNeutral,
			Source:     DialogueIntentLLMFailure,
		}); !errors.Is(err, ErrAIInvocationConflict) {
			t.Fatalf("诊断摘要不同不得覆盖首次完成: %v", err)
		}
	})

	t.Run("M6评分", func(t *testing.T) {
		s := openTest(t)
		completion.InvocationID = "invocation-diagnostics-score"
		if err := s.db.Create(&SourcingScoreInvocation{
			InvocationID:        completion.InvocationID,
			RunID:               "run-diagnostics-score",
			ContextRevisionHash: "revision-diagnostics",
			RunContentHash:      "run-content-diagnostics",
			Provider:            "deepseek",
			Model:               "deepseek-v4-pro",
			InputHash:           "input-diagnostics-score",
			Status:              AIInvocationTransportFailed,
			StartedAt:           finishedAt.Add(-time.Minute),
		}).Error; err != nil {
			t.Fatal(err)
		}
		stored, err := s.CompleteSourcingScore(CompleteSourcingScoreRequest{Completion: completion})
		if err != nil {
			t.Fatal(err)
		}
		assertAIInvocationDiagnostics(t, stored.FailureStage, stored.ErrorDetailCode,
			stored.ProviderHTTPStatus, stored.RequestBytes, stored.ResponseBytes, stored.TraceStatus)
		if _, err := s.CompleteSourcingScore(CompleteSourcingScoreRequest{Completion: completion}); err != nil {
			t.Fatalf("相同诊断完成应幂等: %v", err)
		}
		conflicting := completion
		conflicting.TraceStatus = m5ai.TraceStatusUnavailable
		if _, err := s.CompleteSourcingScore(CompleteSourcingScoreRequest{
			Completion: conflicting,
		}); !errors.Is(err, ErrAIInvocationConflict) {
			t.Fatalf("诊断摘要不同不得覆盖首次完成: %v", err)
		}
	})

	t.Run("M6招呼", func(t *testing.T) {
		s := openTest(t)
		completion.InvocationID = "invocation-diagnostics-greeting"
		if err := s.db.Create(&SourcingGreetingInvocation{
			InvocationID:        completion.InvocationID,
			BatchID:             "batch-diagnostics-greeting",
			RunID:               "run-diagnostics-greeting",
			ProfileID:           "profile-diagnostics-greeting",
			ContextRevisionHash: "revision-diagnostics",
			RunContentHash:      "run-content-diagnostics",
			Provider:            "deepseek",
			Model:               "deepseek-v4-pro",
			InputHash:           "input-diagnostics-greeting",
			Status:              AIInvocationTransportFailed,
			StartedAt:           finishedAt.Add(-time.Minute),
		}).Error; err != nil {
			t.Fatal(err)
		}
		stored, err := s.CompleteSourcingGreeting(CompleteSourcingGreetingRequest{Completion: completion})
		if err != nil {
			t.Fatal(err)
		}
		assertAIInvocationDiagnostics(t, stored.FailureStage, stored.ErrorDetailCode,
			stored.ProviderHTTPStatus, stored.RequestBytes, stored.ResponseBytes, stored.TraceStatus)
		if _, err := s.CompleteSourcingGreeting(CompleteSourcingGreetingRequest{Completion: completion}); err != nil {
			t.Fatalf("相同诊断完成应幂等: %v", err)
		}
		conflicting := completion
		conflicting.ErrorDetailCode = "providerRejectedOther"
		if _, err := s.CompleteSourcingGreeting(CompleteSourcingGreetingRequest{
			Completion: conflicting,
		}); !errors.Is(err, ErrAIInvocationConflict) {
			t.Fatalf("诊断摘要不同不得覆盖首次完成: %v", err)
		}
	})
}

func assertAIInvocationDiagnostics(
	t *testing.T,
	failureStage string,
	errorDetailCode string,
	providerHTTPStatus *int,
	requestBytes int,
	responseBytes int,
	traceStatus m5ai.TraceStatus,
) {
	t.Helper()
	if failureStage != AIInvocationFailureStageProviderHTTP ||
		errorDetailCode != "providerRejected" ||
		providerHTTPStatus == nil || *providerHTTPStatus != 429 ||
		requestBytes != 4096 || responseBytes != 256 ||
		traceStatus != m5ai.TraceStatusComplete {
		t.Fatalf("紧凑诊断未完整持久化: stage=%q detail=%q http=%v request=%d response=%d trace=%q",
			failureStage, errorDetailCode, providerHTTPStatus, requestBytes, responseBytes, traceStatus)
	}
}
