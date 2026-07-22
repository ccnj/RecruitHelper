package m5ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxProviderResponseBytes = 1 << 20

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
	Class string
}

func (e *ProviderError) Error() string { return "provider 调用失败: " + e.Class }

type OpenAICompatibleProvider struct {
	config ProviderConfig
	client *http.Client
}

func NewOpenAICompatibleProvider(config ProviderConfig, client *http.Client) (*OpenAICompatibleProvider, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &OpenAICompatibleProvider{config: config, client: client}, nil
}

func (p *OpenAICompatibleProvider) ProviderName() string { return p.config.Provider }

func (p *OpenAICompatibleProvider) ModelName() string { return p.config.Model }

func (p *OpenAICompatibleProvider) CompleteJSON(ctx context.Context, request CompletionRequest) (CompletionResponse, error) {
	if request.Purpose != PurposeIntent && request.Purpose != PurposeReply &&
		request.Purpose != PurposeScoring && request.Purpose != PurposeGreeting {
		return CompletionResponse{}, errors.New("未知 provider 用途")
	}
	if strings.TrimSpace(request.UserContent) == "" || request.MaxOutputTokens <= 0 {
		return CompletionResponse{}, errors.New("provider 请求缺少正文或输出上限")
	}
	if (request.Purpose == PurposeIntent && request.MaxOutputTokens > p.config.MaxIntentOutputTokens) ||
		((request.Purpose == PurposeReply || request.Purpose == PurposeScoring || request.Purpose == PurposeGreeting) &&
			request.MaxOutputTokens > p.config.MaxReplyOutputTokens) {
		return CompletionResponse{}, &ProviderError{Class: "budgetBlocked"}
	}
	inputLimit := p.config.MaxInputTokens
	if request.Purpose == PurposeIntent && inputLimit > IntentInputTokenLimit {
		inputLimit = IntentInputTokenLimit
	}
	if request.Purpose == PurposeGreeting && inputLimit > GreetingInputTokenLimit {
		inputLimit = GreetingInputTokenLimit
	}
	// UTF-8 byte length is a conservative tokenizer-independent upper bound for
	// this plain-text path because every input token consumes at least one input
	// byte. This may route a
	// large Chinese prompt to a person early, but it can never silently exceed
	// the approved P budget or truncate an input.
	if len([]byte(request.UserContent)) > inputLimit {
		return CompletionResponse{}, &ProviderError{Class: "budgetBlocked"}
	}
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
	payload.Thinking.Type = "disabled"
	body, err := json.Marshal(payload)
	if err != nil {
		return CompletionResponse{}, err
	}
	endpoint := strings.TrimRight(p.config.BaseURL, "/")
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint += "/chat/completions"
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(p.config.RequestTimeoutMs)*time.Millisecond)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(timeoutCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return CompletionResponse{}, &ProviderError{Class: "requestInvalid"}
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+p.config.APIKey)
	response, err := p.client.Do(httpRequest)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
			return CompletionResponse{}, &ProviderError{Class: "timeout"}
		}
		return CompletionResponse{}, &ProviderError{Class: "transport"}
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxProviderResponseBytes+1)
	raw, readErr := io.ReadAll(limited)
	if readErr != nil || len(raw) > maxProviderResponseBytes {
		return CompletionResponse{}, &ProviderError{Class: "responseInvalid"}
	}
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
		return CompletionResponse{}, &ProviderError{Class: class}
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
			PromptTokens         int `json:"prompt_tokens"`
			CompletionTokens     int `json:"completion_tokens"`
			PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`
			CompletionDetails    *struct {
				ReasoningTokens *int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &decoded) != nil || len(decoded.Choices) != 1 ||
		decoded.Choices[0].FinishReason != "stop" || strings.TrimSpace(decoded.Choices[0].Message.Content) == "" ||
		decoded.Usage.PromptTokens < 0 || decoded.Usage.PromptTokens > inputLimit || decoded.Usage.CompletionTokens < 0 ||
		decoded.Usage.PromptCacheHitTokens < 0 || decoded.Usage.PromptCacheHitTokens > decoded.Usage.PromptTokens ||
		decoded.Usage.CompletionTokens > request.MaxOutputTokens {
		return CompletionResponse{}, &ProviderError{Class: "responseInvalid"}
	}
	var reasoning *int
	if decoded.Usage.CompletionDetails != nil {
		reasoning = decoded.Usage.CompletionDetails.ReasoningTokens
		if reasoning != nil && *reasoning < 0 {
			return CompletionResponse{}, &ProviderError{Class: "responseInvalid"}
		}
	}
	return CompletionResponse{
		JSONText: decoded.Choices[0].Message.Content,
		Usage: CompletionUsage{
			InputTokens:       decoded.Usage.PromptTokens,
			CachedInputTokens: decoded.Usage.PromptCacheHitTokens,
			OutputTokens:      decoded.Usage.CompletionTokens,
			ReasoningTokens:   reasoning,
		},
		ReasoningContentEmpty: decoded.Choices[0].Message.ReasoningContent == nil ||
			*decoded.Choices[0].Message.ReasoningContent == "",
	}, nil
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
