package m5ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func configuredProvider(baseURL string) ProviderConfig {
	config := DefaultProviderConfig()
	config.BaseURL = baseURL
	config.APIKey = "sk-fixture"
	return config
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestOpenAICompatibleProviderUsesOneNonThinkingJSONRequestAndNoRetry(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer sk-fixture" {
			t.Fatalf("请求路由或认证错误: path=%s auth=%s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatal("请求 JSON 无效")
		}
		messages, _ := body["messages"].([]any)
		thinking, _ := body["thinking"].(map[string]any)
		format, _ := body["response_format"].(map[string]any)
		if len(messages) != 1 || thinking["type"] != "disabled" || format["type"] != "json_object" {
			t.Fatalf("provider 请求形状错误: %#v", body)
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"信号\":\"中性\"}"}}],"usage":{"prompt_tokens":12,"completion_tokens":3,"prompt_cache_hit_tokens":4,"completion_tokens_details":{"reasoning_tokens":0}}}`)),
		}, nil
	})}
	provider, err := NewOpenAICompatibleProvider(configuredProvider("https://provider.invalid"), client)
	if err != nil {
		t.Fatal(err)
	}
	if provider.ProviderName() != "deepseek" || provider.ModelName() != "deepseek-v4-pro" {
		t.Fatalf("provider 身份错误: provider=%s model=%s", provider.ProviderName(), provider.ModelName())
	}
	response, err := provider.CompleteJSON(context.Background(), CompletionRequest{
		Purpose: PurposeIntent, UserContent: "fixture", MaxOutputTokens: IntentOutputTokenLimit,
	})
	if err != nil || calls != 1 || response.Usage.ReasoningTokens == nil || *response.Usage.ReasoningTokens != 0 {
		t.Fatalf("provider 调用不符合单次/usage 约束: calls=%d response=%+v err=%v", calls, response, err)
	}
}

func TestValidateBaseURLRequiresHTTPSWithoutAuthorityDecorations(t *testing.T) {
	for _, accepted := range []string{
		"https://provider.invalid",
		"https://provider.invalid/v1",
	} {
		if err := validateBaseURL(accepted); err != nil {
			t.Fatalf("合法 HTTPS base URL 被拒绝: value=%s err=%v", accepted, err)
		}
	}
	for _, rejected := range []string{
		"http://provider.invalid",
		"https://user:secret@provider.invalid/v1",
		"https://provider.invalid/v1?tenant=private",
		"https://provider.invalid/v1?",
		"https://provider.invalid/v1#fragment",
	} {
		if err := validateBaseURL(rejected); err == nil {
			t.Fatalf("不安全 base URL 未拒绝: %s", rejected)
		}
	}
}

func TestOpenAICompatibleProviderRejectsInvalidChoiceAndUsageShapes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		max  int
	}{
		{
			name: "missing finish reason",
			raw:  `{"choices":[{"message":{"content":"{}"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			max:  1,
		},
		{
			name: "non stop finish reason",
			raw:  `{"choices":[{"finish_reason":"length","message":{"content":"{}"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			max:  1,
		},
		{
			name: "multiple choices",
			raw:  `{"choices":[{"finish_reason":"stop","message":{"content":"{}"}},{"finish_reason":"stop","message":{"content":"{}"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			max:  1,
		},
		{
			name: "negative input",
			raw:  `{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}],"usage":{"prompt_tokens":-1,"completion_tokens":1}}`,
			max:  1,
		},
		{
			name: "input exceeds approved budget",
			raw:  `{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}],"usage":{"prompt_tokens":8001,"completion_tokens":1}}`,
			max:  1,
		},
		{
			name: "negative cached input",
			raw:  `{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"prompt_cache_hit_tokens":-1}}`,
			max:  1,
		},
		{
			name: "cached input exceeds input",
			raw:  `{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"prompt_cache_hit_tokens":2}}`,
			max:  1,
		},
		{
			name: "negative output",
			raw:  `{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}],"usage":{"prompt_tokens":1,"completion_tokens":-1}}`,
			max:  1,
		},
		{
			name: "output exceeds request",
			raw:  `{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`,
			max:  1,
		},
		{
			name: "negative reasoning",
			raw:  `{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"completion_tokens_details":{"reasoning_tokens":-1}}}`,
			max:  1,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(testCase.raw)),
				}, nil
			})}
			provider, _ := NewOpenAICompatibleProvider(configuredProvider("https://provider.invalid"), client)
			_, err := provider.CompleteJSON(context.Background(), CompletionRequest{
				Purpose: PurposeIntent, UserContent: "fixture", MaxOutputTokens: testCase.max,
			})
			var providerErr *ProviderError
			if calls != 1 || !errors.As(err, &providerErr) || providerErr.Class != "responseInvalid" ||
				strings.Contains(err.Error(), testCase.raw) {
				t.Fatalf("非法响应未固定收敛: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestOpenAICompatibleProviderAcceptsMissingReasoningUsageField(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}],"usage":{"prompt_tokens":4,"completion_tokens":1,"prompt_cache_hit_tokens":2}}`,
			)),
		}, nil
	})}
	provider, _ := NewOpenAICompatibleProvider(configuredProvider("https://provider.invalid"), client)
	response, err := provider.CompleteJSON(context.Background(), CompletionRequest{
		Purpose: PurposeIntent, UserContent: "fixture", MaxOutputTokens: 1,
	})
	if err != nil || response.Usage.ReasoningTokens != nil {
		t.Fatalf("reasoning 字段缺失应保留缺失形态: response=%+v err=%v", response, err)
	}
}

func TestEstimatedCostMicrosUsesFrozenDeepSeekV4ProPrices(t *testing.T) {
	tests := []struct {
		name  string
		usage CompletionUsage
		want  int64
	}{
		{name: "cache miss", usage: CompletionUsage{InputTokens: 1_000_000}, want: 435_000},
		{name: "cache hit", usage: CompletionUsage{InputTokens: 1_000_000, CachedInputTokens: 1_000_000}, want: 3_625},
		{name: "output", usage: CompletionUsage{OutputTokens: 1_000_000}, want: 870_000},
		{name: "approved baseline", usage: CompletionUsage{InputTokens: 14_000, OutputTokens: 400}, want: 6_438},
		{name: "mixed exact integer arithmetic", usage: CompletionUsage{InputTokens: 12, CachedInputTokens: 4, OutputTokens: 3}, want: 6},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := EstimatedCostMicros(testCase.usage); got != testCase.want {
				t.Fatalf("费用估算错误: got=%d want=%d usage=%+v", got, testCase.want, testCase.usage)
			}
		})
	}
}

func TestOpenAICompatibleProviderSanitizesRejectionAndDoesNotRetry(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Body:       io.NopCloser(strings.NewReader("secret provider body")), Header: make(http.Header),
		}, nil
	})}
	provider, _ := NewOpenAICompatibleProvider(configuredProvider("https://provider.invalid"), client)
	_, err := provider.CompleteJSON(context.Background(), CompletionRequest{
		Purpose: PurposeReply, UserContent: "fixture", MaxOutputTokens: ReplyOutputTokenLimit,
	})
	var providerErr *ProviderError
	if calls != 1 || !errors.As(err, &providerErr) || providerErr.Class != "rateLimited" || strings.Contains(err.Error(), "secret provider body") {
		t.Fatalf("拒绝未安全收敛: calls=%d err=%v", calls, err)
	}
}

func TestOpenAICompatibleProviderFailureClassesAreBoundedAndNeverRetried(t *testing.T) {
	cases := []struct {
		status int
		class  string
	}{
		{http.StatusUnauthorized, "authentication"},
		{http.StatusTooManyRequests, "rateLimited"},
		{http.StatusServiceUnavailable, "providerUnavailable"},
		{http.StatusTeapot, "providerRejected"},
	}
	for _, testCase := range cases {
		t.Run(testCase.class, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{
					StatusCode: testCase.status, Header: make(http.Header),
					Body: io.NopCloser(strings.NewReader("private body")),
				}, nil
			})}
			provider, _ := NewOpenAICompatibleProvider(configuredProvider("https://provider.invalid"), client)
			_, err := provider.CompleteJSON(context.Background(), CompletionRequest{
				Purpose: PurposeIntent, UserContent: "fixture", MaxOutputTokens: IntentOutputTokenLimit,
			})
			var providerErr *ProviderError
			if calls != 1 || !errors.As(err, &providerErr) || providerErr.Class != testCase.class || strings.Contains(err.Error(), "private body") {
				t.Fatalf("失败分类或重试错误: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestOpenAICompatibleProviderBoundsTransportOutputAndInputFailures(t *testing.T) {
	t.Run("transport timeout", func(t *testing.T) {
		calls := 0
		client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			calls++
			return nil, context.DeadlineExceeded
		})}
		provider, _ := NewOpenAICompatibleProvider(configuredProvider("https://provider.invalid"), client)
		_, err := provider.CompleteJSON(context.Background(), CompletionRequest{
			Purpose: PurposeIntent, UserContent: "fixture", MaxOutputTokens: IntentOutputTokenLimit,
		})
		var providerErr *ProviderError
		if calls != 1 || !errors.As(err, &providerErr) || providerErr.Class != "timeout" {
			t.Fatalf("timeout 未收敛: calls=%d err=%v", calls, err)
		}
	})

	t.Run("invalid response", func(t *testing.T) {
		client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[]}`))}, nil
		})}
		provider, _ := NewOpenAICompatibleProvider(configuredProvider("https://provider.invalid"), client)
		_, err := provider.CompleteJSON(context.Background(), CompletionRequest{
			Purpose: PurposeReply, UserContent: "fixture", MaxOutputTokens: ReplyOutputTokenLimit,
		})
		var providerErr *ProviderError
		if !errors.As(err, &providerErr) || providerErr.Class != "responseInvalid" {
			t.Fatalf("非法输出未收敛: %v", err)
		}
	})

	t.Run("input budget before transport", func(t *testing.T) {
		calls := 0
		client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("不应调用")
		})}
		provider, _ := NewOpenAICompatibleProvider(configuredProvider("https://provider.invalid"), client)
		_, err := provider.CompleteJSON(context.Background(), CompletionRequest{
			Purpose: PurposeIntent, UserContent: strings.Repeat("a", IntentInputTokenLimit+1), MaxOutputTokens: IntentOutputTokenLimit,
		})
		var providerErr *ProviderError
		if calls != 0 || !errors.As(err, &providerErr) || providerErr.Class != "budgetBlocked" {
			t.Fatalf("输入预算未在网络前阻断: calls=%d err=%v", calls, err)
		}
	})
}

type scriptedProvider struct {
	responses []CompletionResponse
	errors    []error
	requests  []CompletionRequest
}

func (p *scriptedProvider) CompleteJSON(_ context.Context, request CompletionRequest) (CompletionResponse, error) {
	p.requests = append(p.requests, request)
	index := len(p.requests) - 1
	if index < len(p.errors) && p.errors[index] != nil {
		return CompletionResponse{}, p.errors[index]
	}
	return p.responses[index], nil
}

func TestAIAdvisorKeepsIntentAndReplyAsTwoCallsAndFallsBackOnce(t *testing.T) {
	provider := &scriptedProvider{
		responses: []CompletionResponse{{}, {JSONText: `{"话术_序列":[" 第一段 ","", "第二段"],"动作":"忽略"}`}},
		errors:    []error{errors.New("intent failed"), nil},
	}
	advisor, _ := NewAIAdvisor(provider)
	intent, _, intentErr := advisor.SuggestIntent(context.Background(), "intent")
	if intentErr != nil {
		intent = IntentSuggestion{Label: IntentNeutral}
	}
	if intent.Label != IntentNeutral {
		t.Fatalf("intent 失败必须持久化为 neutral fallback: %s", intent.Label)
	}
	reply, _, err := advisor.SuggestReply(context.Background(), "reply")
	if err != nil || reply.Text != "第一段\n第二段" || len(provider.requests) != 2 ||
		provider.requests[0].Purpose != PurposeIntent || provider.requests[1].Purpose != PurposeReply {
		t.Fatalf("两调用/fallback 语义错误: reply=%+v requests=%+v err=%v", reply, provider.requests, err)
	}
}

func TestStrictParsersAndEmptyProductionShortCircuit(t *testing.T) {
	intent, err := ParseIntentSuggestion(`{"信号":"有意向","理由":"不持久化"}`)
	if err != nil || intent.Label != IntentInterested {
		t.Fatalf("中文意向映射失败: %+v err=%v", intent, err)
	}
	if _, err := ParseIntentSuggestion(`{"信号":"中性","置信度":0.8}`); err == nil {
		t.Fatal("意向未知字段必须拒绝")
	}
	if _, err := ParseReplySuggestion(`{"reply":"你好"}`); err == nil {
		t.Fatal("旧 reply 回退不得接受")
	}
	for _, raw := range []string{
		`{"信号":"中性","候选人手机号13800138000":true}`,
		`{"话术_序列":["你好"],"候选人手机号13800138000":true}`,
	} {
		var parseErr error
		if strings.Contains(raw, "话术_序列") {
			_, parseErr = ParseReplySuggestion(raw)
		} else {
			_, parseErr = ParseIntentSuggestion(raw)
		}
		if parseErr == nil || strings.Contains(parseErr.Error(), "13800138000") {
			t.Fatalf("未知 provider 字段必须固定分类且不回显字段名: %v", parseErr)
		}
	}
	if got := ClassifyIntentShortCircuit([]string{"不考虑"}); got.Matched {
		t.Fatalf("批次0B未启用的旧规则不得分类: %+v", got)
	}
}
