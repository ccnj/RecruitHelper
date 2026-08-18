package m5ai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"recruithelper/client/service/internal/aitrace"
)

// maxProviderRequestBytes / maxProviderResponseBytes 是**我们自己的**保守自限,
// 不是任何 provider 公布的限制,查它们的文档也找不到这两个数——256 KB 是从协议层
// WS 帧上限顺手取的一个"看着够大"的值。别把它们当成平台契约:需要时可以调,只要
// 有实测依据。
//
// provider 真正的限制是按 token 算的上下文窗口,那由 purposeInputTokenLimit 与
// 拿到响应后的 promptTokens 复核两道守着;字节数只是发请求前的粗糙外围保险。
// 它咬人的方向也是安全的:超了就在发出去之前干净失败(requestPayloadTooLarge),
// 不花钱、不产生副作用。
//
// 另外别把它绑成别的东西的推导基数——2026-08-02 客户机那次事故,起因正是拿输出
// 预算去推"一次带几个职位",两个不相干的约束共用一个数。
const (
	maxProviderRequestBytes  = 256 << 10
	maxProviderResponseBytes = 1 << 20
	traceWriteTimeout        = 5 * time.Second

	// DeepSeek V4 Pro USD prices frozen on 2026-07-21, expressed as
	// micro-dollars per one million tokens so cost accounting never uses a
	// floating-point conversion.
	deepSeekV4ProCacheHitMicrosPerMillion  int64 = 3_625
	deepSeekV4ProCacheMissMicrosPerMillion int64 = 435_000
	deepSeekV4ProOutputMicrosPerMillion    int64 = 870_000
	costTokenDenominator                         = 1_000_000
)

// EstimatedCostMicros returns the nearest whole micro-dollar for one validated
// DeepSeek V4 Pro usage record. InputTokens includes CachedInputTokens.
func EstimatedCostMicros(usage CompletionUsage) int64 {
	cacheMissTokens := int64(usage.InputTokens - usage.CachedInputTokens)
	numerator := cacheMissTokens*deepSeekV4ProCacheMissMicrosPerMillion +
		int64(usage.CachedInputTokens)*deepSeekV4ProCacheHitMicrosPerMillion +
		int64(usage.OutputTokens)*deepSeekV4ProOutputMicrosPerMillion
	return (numerator + costTokenDenominator/2) / costTokenDenominator
}

type ProviderError struct {
	Class        string
	FailureStage string
	DetailCode   string
}

func (e *ProviderError) Error() string { return "provider 调用失败: " + e.Class }

func newProviderError(class, failureStage, detailCode string) *ProviderError {
	return &ProviderError{Class: class, FailureStage: failureStage, DetailCode: detailCode}
}

type OpenAICompatibleProvider struct {
	config        ProviderConfig
	client        *http.Client
	trace         TraceRecorder
	traceExpected bool
	configHash    string
}

// TraceRecorder is deliberately narrower than the trace store. The provider
// can append one request/response pair, but cannot enumerate or export the raw
// local corpus.
type TraceRecorder interface {
	Begin(context.Context, aitrace.BeginRecord) error
	Finish(context.Context, aitrace.FinishRecord) error
}

// NewOpenAICompatibleProvider keeps the optional recorder variadic so focused
// adapter tests and helpers can omit tracing. Production always passes exactly
// one recorder argument; a nil value means tracing was expected but its
// standalone database could not be opened.
func NewOpenAICompatibleProvider(
	config ProviderConfig,
	client *http.Client,
	traceRecorder ...TraceRecorder,
) (*OpenAICompatibleProvider, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if len(traceRecorder) > 1 {
		return nil, errors.New("LLM provider trace recorder 数量无效")
	}
	if client == nil {
		client = http.DefaultClient
	}
	configHash, err := providerConfigHash(config)
	if err != nil {
		return nil, err
	}
	provider := &OpenAICompatibleProvider{
		config: config, client: client, configHash: configHash,
		traceExpected: len(traceRecorder) == 1,
	}
	if len(traceRecorder) == 1 {
		provider.trace = traceRecorder[0]
	}
	return provider, nil
}

func (p *OpenAICompatibleProvider) ProviderName() string { return p.config.Provider }

func (p *OpenAICompatibleProvider) ModelName() string { return p.config.Model }

// purposeInputTokenLimit 给出这一次调用的输入上限。
//
// 默认取 ReplyInputTokenLimit——它按"一次回复"设定(32000),对绝大多数用途就是
// 正确的天花板。两个用途另有覆盖,方向相反:
//
//	intent    收窄到 8000  —— 意图判断只看会话尾部,给多了是浪费
//	jobClass  放宽到 64000 —— 全批分配一次要带全部职位的完整描述(甲方
//	                          2026-08-01 裁决,依据见 JobClassInputTokenLimit)
//
// 覆盖只对本用途生效,别的用途一律不受影响;这道闸本身仍在(provider.go 拿响应
// 里的 promptTokens 事后比对),只是对这一个用途换了个数。
func purposeInputTokenLimit(purpose CompletionPurpose, configured int) int {
	switch purpose {
	case PurposeIntent:
		if configured > IntentInputTokenLimit {
			return IntentInputTokenLimit
		}
	case PurposeJobClass:
		if configured < JobClassInputTokenLimit {
			return JobClassInputTokenLimit
		}
	}
	return configured
}

func (p *OpenAICompatibleProvider) CompleteJSON(ctx context.Context, request CompletionRequest) (CompletionResponse, error) {
	preflight := CompletionDiagnostics{}
	if p.traceExpected {
		preflight.TraceStatus = TraceStatusUnavailable
	}
	if request.Purpose != PurposeIntent && request.Purpose != PurposeReply &&
		request.Purpose != PurposeServiceReply &&
		request.Purpose != PurposeSilenceFollowup &&
		request.Purpose != PurposeScoring && request.Purpose != PurposeGreeting &&
		request.Purpose != PurposeJobClass && request.Purpose != PurposeJobKeywords {
		return CompletionResponse{Diagnostics: preflight},
			newProviderError("requestInvalid", FailureStageRequestBuild, "unknownPurpose")
	}
	if strings.TrimSpace(request.UserContent) == "" || request.MaxOutputTokens <= 0 {
		return CompletionResponse{Diagnostics: preflight},
			newProviderError("requestInvalid", FailureStageRequestBuild, "requestMissingFields")
	}
	if p.traceExpected && (strings.TrimSpace(request.InvocationID) == "" ||
		strings.TrimSpace(request.ContextRevisionHash) == "") {
		return CompletionResponse{Diagnostics: preflight},
			newProviderError("requestInvalid", FailureStageRequestBuild, "traceMetadataMissing")
	}
	if (request.Purpose == PurposeIntent && request.MaxOutputTokens > IntentOutputTokenLimit) ||
		((request.Purpose == PurposeReply || request.Purpose == PurposeServiceReply ||
			request.Purpose == PurposeSilenceFollowup ||
			request.Purpose == PurposeScoring || request.Purpose == PurposeGreeting ||
			request.Purpose == PurposeJobClass || request.Purpose == PurposeJobKeywords) &&
			request.MaxOutputTokens > ReplyOutputTokenLimit) {
		return CompletionResponse{Diagnostics: preflight},
			newProviderError("budgetBlocked", FailureStageRequestBuild, "outputTokenBudgetExceeded")
	}
	inputLimit := purposeInputTokenLimit(request.Purpose, ReplyInputTokenLimit)
	payload := struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		ResponseFormat struct {
			Type string `json:"type"`
		} `json:"response_format"`
		MaxTokens int `json:"max_tokens"`
		Thinking  struct {
			Type string `json:"type"`
		} `json:"thinking"`
	}{Model: p.config.Model, MaxTokens: request.MaxOutputTokens}
	payload.Messages = append(payload.Messages, struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{Role: "user", Content: request.UserContent})
	payload.ResponseFormat.Type = "json_object"
	// 2026-08-16 甲方裁决:全部用途开启思考模式。真机 21 张邀面卡里 2 张把
	// 候选人说的日期算错一天(候选人都点了接受),33 个真实 case × 1320 次
	// 对照显示日期错只在关思考档出现(4/330),开思考档 660 次零错。
	payload.Thinking.Type = "enabled"
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("AI provider 请求编码失败", "err", err)
		return CompletionResponse{Diagnostics: preflight},
			newProviderError("requestInvalid", FailureStageRequestBuild, "requestMarshalFailed")
	}
	startedAt := time.Now()
	diagnostics := p.beginTrace(request, body, startedAt)
	result := CompletionResponse{Diagnostics: diagnostics}
	if len(body) > maxProviderRequestBytes {
		p.finishTrace(
			&result.Diagnostics,
			aitrace.FinishRecord{
				InvocationID:  request.InvocationID,
				TransportCode: aitrace.TransportRequestInvalid,
				FinishedAt:    time.Now(),
			},
		)
		return result,
			newProviderError("requestPayloadTooLarge", FailureStageRequestBuild, "requestPayloadTooLarge")
	}
	endpoint := strings.TrimRight(p.config.BaseURL, "/")
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint += "/chat/completions"
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, ProviderRequestTimeoutMs*time.Millisecond)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(timeoutCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		slog.Warn("AI provider 请求构造失败", "err", err)
		p.finishTrace(
			&result.Diagnostics,
			aitrace.FinishRecord{
				InvocationID:  request.InvocationID,
				TransportCode: aitrace.TransportRequestInvalid,
				FinishedAt:    time.Now(),
			},
		)
		return result, newProviderError("requestInvalid", FailureStageRequestBuild, "requestInvalid")
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+p.config.APIKey)
	response, err := p.client.Do(httpRequest)
	if err != nil {
		transportCode := aitrace.TransportNetwork
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
			transportCode = aitrace.TransportTimeout
		} else if errors.Is(err, context.Canceled) || errors.Is(timeoutCtx.Err(), context.Canceled) {
			transportCode = aitrace.TransportCanceled
		}
		// err 原文必须留痕:传输失败没有响应可存,追踪库只有枚举码,这里是
		// DNS/拒连/TLS/代理这类底层原因在全系统唯一的出口。
		slog.Warn("AI provider 传输失败", "transportCode", string(transportCode), "err", err)
		p.finishTrace(
			&result.Diagnostics,
			aitrace.FinishRecord{
				InvocationID:  request.InvocationID,
				TransportCode: transportCode,
				FinishedAt:    time.Now(),
			},
		)
		if transportCode == aitrace.TransportTimeout {
			return result, newProviderError("timeout", FailureStageTransport, "transportTimeout")
		}
		if transportCode == aitrace.TransportCanceled {
			return result, newProviderError("transport", FailureStageTransport, "transportCanceled")
		}
		return result, newProviderError("transport", FailureStageTransport, "transportUnavailable")
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxProviderResponseBytes+1)
	raw, readErr := io.ReadAll(limited)
	result.Diagnostics.ProviderHTTPStatus = intPointer(response.StatusCode)
	result.Diagnostics.ResponseBytes = len(raw)
	if readErr != nil || len(raw) > maxProviderResponseBytes {
		p.finishTrace(
			&result.Diagnostics,
			aitrace.FinishRecord{
				InvocationID:  request.InvocationID,
				HTTPStatus:    &response.StatusCode,
				RawResponse:   raw,
				TransportCode: aitrace.TransportResponseRead,
				FinishedAt:    time.Now(),
			},
		)
		detailCode := "responseReadFailed"
		if readErr == nil {
			detailCode = "responseTooLarge"
		}
		slog.Warn("AI provider 响应读取失败", "detail", detailCode,
			"httpStatus", response.StatusCode, "bytes", len(raw), "err", readErr)
		return result, newProviderError("responseInvalid", FailureStageResponseDecode, detailCode)
	}
	p.finishTrace(
		&result.Diagnostics,
		aitrace.FinishRecord{
			InvocationID: request.InvocationID,
			HTTPStatus:   &response.StatusCode,
			RawResponse:  raw,
			FinishedAt:   time.Now(),
		},
	)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		class := "providerRejected"
		switch response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			class = "authentication"
		case http.StatusTooManyRequests:
			class = "rateLimited"
		default:
			if response.StatusCode >= 500 {
				class = "providerUnavailable"
			}
		}
		return result, newProviderError(class, FailureStageProviderHTTP, class)
	}
	var decoded struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content          string  `json:"content"`
				ReasoningContent *string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens         *int `json:"prompt_tokens"`
			CompletionTokens     *int `json:"completion_tokens"`
			PromptCacheHitTokens int  `json:"prompt_cache_hit_tokens"`
			CompletionDetails    *struct {
				ReasoningTokens *int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &decoded) != nil {
		return result, newProviderError(
			"responseInvalid", FailureStageResponseDecode, "responseMalformed",
		)
	}
	if len(decoded.Choices) != 1 {
		return result, newProviderError(
			"responseInvalid", FailureStageResponseDecode, "responseChoiceInvalid",
		)
	}
	if decoded.Choices[0].FinishReason != "stop" {
		return result, newProviderError(
			"responseInvalid", FailureStageResponseDecode, "responseFinishReasonInvalid",
		)
	}
	if strings.TrimSpace(decoded.Choices[0].Message.Content) == "" {
		return result, newProviderError(
			"responseInvalid", FailureStageResponseDecode, "responseContentMissing",
		)
	}
	if decoded.Usage.PromptTokens == nil || decoded.Usage.CompletionTokens == nil {
		return result, newProviderError(
			"responseInvalid", FailureStageResponseDecode, "usageMissing",
		)
	}
	promptTokens := *decoded.Usage.PromptTokens
	completionTokens := *decoded.Usage.CompletionTokens
	if promptTokens < 0 || completionTokens < 0 ||
		decoded.Usage.PromptCacheHitTokens < 0 || decoded.Usage.PromptCacheHitTokens > promptTokens ||
		completionTokens > request.MaxOutputTokens {
		return result, newProviderError(
			"responseInvalid", FailureStageResponseDecode, "usageInvalid",
		)
	}
	var reasoning *int
	if decoded.Usage.CompletionDetails != nil {
		reasoning = decoded.Usage.CompletionDetails.ReasoningTokens
		if reasoning != nil && *reasoning < 0 {
			return result, newProviderError(
				"responseInvalid", FailureStageResponseDecode, "usageInvalid",
			)
		}
	}
	result.JSONText = decoded.Choices[0].Message.Content
	result.Usage = CompletionUsage{
		InputTokens:       promptTokens,
		CachedInputTokens: decoded.Usage.PromptCacheHitTokens,
		OutputTokens:      completionTokens,
		ReasoningTokens:   reasoning,
	}
	result.ReasoningContentEmpty = decoded.Choices[0].Message.ReasoningContent == nil ||
		*decoded.Choices[0].Message.ReasoningContent == ""
	if promptTokens > inputLimit {
		return result, newProviderError(
			"inputTokenBudgetExceeded", FailureStageResponseDecode, "inputTokenBudgetExceeded",
		)
	}
	return result, nil
}

func (p *OpenAICompatibleProvider) beginTrace(
	request CompletionRequest,
	body []byte,
	startedAt time.Time,
) CompletionDiagnostics {
	if !p.traceExpected {
		return CompletionDiagnostics{}
	}
	diagnostics := CompletionDiagnostics{
		RequestBytes: len(body), TraceStatus: TraceStatusUnavailable,
	}
	if p.trace == nil {
		diagnostics.TraceErrorCode = "traceStoreUnavailable"
		return diagnostics
	}
	traceCtx, cancel := context.WithTimeout(context.Background(), traceWriteTimeout)
	defer cancel()
	err := p.trace.Begin(traceCtx, aitrace.BeginRecord{
		InvocationID:        request.InvocationID,
		Purpose:             string(request.Purpose),
		Provider:            p.config.Provider,
		Model:               p.config.Model,
		ConfigHash:          p.configHash,
		ContextRevisionHash: request.ContextRevisionHash,
		PromptRevision:      request.PromptRevision,
		RequestJSON:         body,
		StartedAt:           startedAt,
	})
	if err != nil {
		diagnostics.TraceErrorCode = "traceBeginFailed"
		return diagnostics
	}
	diagnostics.TraceStatus = TraceStatusResponseUnavailable
	return diagnostics
}

func (p *OpenAICompatibleProvider) finishTrace(
	diagnostics *CompletionDiagnostics,
	record aitrace.FinishRecord,
) {
	if !p.traceExpected || p.trace == nil ||
		diagnostics.TraceStatus != TraceStatusResponseUnavailable {
		return
	}
	traceCtx, cancel := context.WithTimeout(context.Background(), traceWriteTimeout)
	defer cancel()
	if err := p.trace.Finish(traceCtx, record); err != nil {
		diagnostics.TraceErrorCode = "traceFinishFailed"
		return
	}
	diagnostics.TraceStatus = TraceStatusComplete
	diagnostics.TraceErrorCode = ""
}

func providerConfigHash(config ProviderConfig) (string, error) {
	// APIKey is intentionally absent. The digest tracks the effective request
	// configuration without making a credential or header serializable.
	value := struct {
		Provider              string `json:"provider"`
		Model                 string `json:"model"`
		BaseURL               string `json:"base_url"`
		RequestTimeoutMs      int64  `json:"request_timeout_ms"`
		MaxInputTokens        int    `json:"max_input_tokens"`
		MaxIntentOutputTokens int    `json:"max_intent_output_tokens"`
		MaxReplyOutputTokens  int    `json:"max_reply_output_tokens"`
	}{
		Provider: config.Provider, Model: config.Model, BaseURL: config.BaseURL,
		RequestTimeoutMs: ProviderRequestTimeoutMs, MaxInputTokens: ReplyInputTokenLimit,
		MaxIntentOutputTokens: IntentOutputTokenLimit,
		MaxReplyOutputTokens:  ReplyOutputTokenLimit,
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("LLM provider 配置摘要失败: %v", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func intPointer(value int) *int {
	copy := value
	return &copy
}

func validateBaseURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return fmt.Errorf("base_url 无效")
	}
	return nil
}

type AIAdvisor struct {
	provider LLMProvider
}

func NewAIAdvisor(provider LLMProvider) (*AIAdvisor, error) {
	if provider == nil {
		return nil, errors.New("LLM provider 不能为空")
	}
	return &AIAdvisor{provider: provider}, nil
}

func (a *AIAdvisor) SuggestIntent(ctx context.Context, userContent string) (IntentSuggestion, CompletionUsage, error) {
	response, err := a.provider.CompleteJSON(ctx, CompletionRequest{
		Purpose: PurposeIntent, UserContent: userContent, MaxOutputTokens: IntentOutputTokenLimit,
	})
	if err != nil {
		return IntentSuggestion{}, CompletionUsage{}, err
	}
	suggestion, err := ParseIntentSuggestion(response.JSONText)
	return suggestion, response.Usage, err
}

func (a *AIAdvisor) SuggestReply(ctx context.Context, userContent string) (ReplySuggestion, CompletionUsage, error) {
	response, err := a.provider.CompleteJSON(ctx, CompletionRequest{
		Purpose: PurposeReply, UserContent: userContent, MaxOutputTokens: ReplyOutputTokenLimit,
	})
	if err != nil {
		return ReplySuggestion{}, CompletionUsage{}, err
	}
	suggestion, err := ParseReplySuggestion(response.JSONText)
	return suggestion, response.Usage, err
}

func (a *AIAdvisor) SuggestSilenceFollowup(
	ctx context.Context,
	userContent string,
) (SilenceFollowupSuggestion, CompletionUsage, error) {
	response, err := a.provider.CompleteJSON(ctx, CompletionRequest{
		Purpose: PurposeSilenceFollowup, UserContent: userContent,
		MaxOutputTokens: SilenceFollowupOutputTokenLimit,
	})
	if err != nil {
		return SilenceFollowupSuggestion{}, CompletionUsage{}, err
	}
	suggestion, err := ParseSilenceFollowupSuggestion(response.JSONText)
	return suggestion, response.Usage, err
}
