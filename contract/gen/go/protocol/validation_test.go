package protocol

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestValidateKindBodyCommandAndMustIgnore(t *testing.T) {
	raw := json.RawMessage(`{
		"name":"chat.readThread","ver":1,
		"context":{"platform":"zhilian","accountRef":"acc-01","expectedPrincipalFingerprint":"opaque-01","futureContextField":true},
		"args":{"conversationRef":"conv-01","window":{"maxMessages":50,"anchorTail":[],"deep":false,"futureWindowField":1},"futureArgField":true},
		"deadline":1999999999999,"execBudgetMs":240000,"leaseMs":30000,
		"futureBodyField":{"added":true}
	}`)
	if err := ValidateKindBody(KindCmd, raw); err != nil {
		t.Fatalf("合法命令及未知加法字段应通过:%v", err)
	}
}

func TestValidateLocalHelloRequiresStableHandID(t *testing.T) {
	valid := json.RawMessage(`{
		"handId":"h-local-random","bootId":"b-one","protoSupported":[1],
		"contractHash":"sha256:public-digest","app":{"extVersion":"0.1.0","browser":"chrome"},
		"caps":[],"features":[],"auth":"retired-and-ignored"
	}`)
	if err := ValidateKindBody(KindHello, valid); err != nil {
		t.Fatalf("本地 handId 必填且历史 auth 作为未知字段无语义地忽略: %v", err)
	}
	missing := json.RawMessage(`{
		"bootId":"b-one","protoSupported":[1],"contractHash":"sha256:public-digest",
		"app":{"extVersion":"0.1.0","browser":"chrome"},"caps":[],"features":[]
	}`)
	assertValidationError(t, ValidateKindBody(KindHello, missing), "$.handId", "required")
	nullID := json.RawMessage(`{
		"handId":null,"bootId":"b-one","protoSupported":[1],"contractHash":"sha256:public-digest",
		"app":{"extVersion":"0.1.0","browser":"chrome"},"caps":[],"features":[]
	}`)
	assertValidationError(t, ValidateKindBody(KindHello, nullID), "$.handId", "nullable")
}

func TestValidateCommandSemanticGates(t *testing.T) {
	preBindProbe := json.RawMessage(`{"name":"probe.platform","ver":1,"args":{},"deadline":1999999999999,"execBudgetMs":5000}`)
	if err := ValidateKindBody(KindCmd, preBindProbe); err != nil {
		t.Fatalf("首次绑定前 probe.platform 应允许省略 context: %v", err)
	}

	tests := []struct {
		name string
		raw  string
		path string
		rule string
	}{
		{
			name: "非绑定探测缺 context",
			raw:  `{"name":"chat.readList","ver":1,"args":{"filter":"all"},"deadline":1999999999999,"execBudgetMs":240000,"leaseMs":60000}`,
			path: "$.context", rule: "required",
		},
		{
			name: "intrusive 缺账号指纹",
			raw:  `{"name":"chat.readList","ver":1,"context":{"platform":"zhilian","accountRef":"acc-01"},"args":{"filter":"all"},"deadline":1999999999999,"execBudgetMs":240000,"leaseMs":60000}`,
			path: "$.context.expectedPrincipalFingerprint", rule: "required",
		},
		{
			name: "长原语缺租约",
			raw:  `{"name":"chat.readList","ver":1,"context":{"platform":"zhilian","accountRef":"acc-01","expectedPrincipalFingerprint":"opaque"},"args":{"filter":"all"},"deadline":1999999999999,"execBudgetMs":240000}`,
			path: "$.leaseMs", rule: "required",
		},
		{
			name: "版本不匹配",
			raw:  `{"name":"probe.platform","ver":2,"context":{"platform":"zhilian","accountRef":"acc-01"},"args":{},"deadline":1999999999999,"execBudgetMs":5000}`,
			path: "$.ver", rule: "version",
		},
		{
			name: "参数越界",
			raw:  `{"name":"chat.readThread","ver":1,"context":{"platform":"zhilian","accountRef":"acc-01","expectedPrincipalFingerprint":"opaque"},"args":{"conversationRef":"conv","window":{"maxMessages":501}},"deadline":1999999999999,"execBudgetMs":240000,"leaseMs":30000}`,
			path: "$.args.window.maxMessages", rule: "maximum",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateKindBody(KindCmd, json.RawMessage(tt.raw))
			assertValidationError(t, err, tt.path, tt.rule)
		})
	}
}

func TestValidateEventDataAndConst(t *testing.T) {
	valid := json.RawMessage(`{"name":"unreadBadge","context":{"platform":"zhilian","accountRef":"acc-01"},"observedAt":100,"data":{"scope":"total","value":3,"prev":2,"stable":true,"future":1}}`)
	if err := ValidateKindBody(KindEvent, valid); err != nil {
		t.Fatalf("合法事件应通过:%v", err)
	}
	invalid := json.RawMessage(`{"name":"unreadBadge","context":{"platform":"zhilian","accountRef":"acc-01"},"observedAt":100,"data":{"scope":"total","value":3,"prev":2,"stable":false}}`)
	assertValidationError(t, ValidateKindBody(KindEvent, invalid), "$.data.stable", "const")
}

func TestValidateResultConditionalFields(t *testing.T) {
	valid := json.RawMessage(`{"ref":"cmd-1","status":"ok","data":{},"replayed":false,"execMs":2,"future":true}`)
	if err := ValidateKindBody(KindResult, valid); err != nil {
		t.Fatalf("合法 result 应通过:%v", err)
	}
	missingData := json.RawMessage(`{"ref":"cmd-1","status":"ok","replayed":false,"execMs":2}`)
	assertValidationError(t, ValidateKindBody(KindResult, missingData), "$", "exactlyOneWhen")
	failedWithoutError := json.RawMessage(`{"ref":"cmd-1","status":"failed","replayed":false,"execMs":2}`)
	assertValidationError(t, ValidateKindBody(KindResult, failedWithoutError), "$.error", "requiredWhen")
}

func TestValidateThreadPayloadLimitsAndNullable(t *testing.T) {
	base := `{"messages":[{"idx":0,"direction":"in","kind":"text","text":%s,"blobRef":null,"contentHash":"hash"}],"reachedTop":false,"anchorMatched":true,"peer":null,"complete":true}`
	valid := json.RawMessage(strings.Replace(base, "%s", `"你好"`, 1))
	if err := ValidatePrimitiveData(PrimChatReadThread, 1, valid); err != nil {
		t.Fatalf("合法线程结果应通过:%v", err)
	}
	tooLong := json.RawMessage(strings.Replace(base, "%s", `"`+strings.Repeat("a", DefaultPayloadInlineMessageTextBytes+1)+`"`, 1))
	assertValidationError(t, ValidatePrimitiveData(PrimChatReadThread, 1, tooLong), "$.messages[0].text", "maxBytes")
}

func TestPaginationCursorConsistency(t *testing.T) {
	listContinue := json.RawMessage(`{"sessions":[],"complete":false,"nextCursor":"opaque-next"}`)
	if err := ValidatePrimitiveData(PrimChatReadList, 1, listContinue); err != nil {
		t.Fatalf("合法列表续页应通过:%v", err)
	}
	listMissingCursor := json.RawMessage(`{"sessions":[],"complete":false}`)
	assertValidationError(t, ValidatePrimitiveData(PrimChatReadList, 1, listMissingCursor), "$.nextCursor", "requiredWhen")
	listFinishedWithCursor := json.RawMessage(`{"sessions":[],"complete":true,"nextCursor":"stale"}`)
	assertValidationError(t, ValidatePrimitiveData(PrimChatReadList, 1, listFinishedWithCursor), "$.nextCursor", "forbiddenWhen")

	threadContinue := json.RawMessage(`{"messages":[],"reachedTop":false,"anchorMatched":false,"peer":null,"complete":false,"nextCursor":"opaque-next"}`)
	if err := ValidatePrimitiveData(PrimChatReadThread, 1, threadContinue); err != nil {
		t.Fatalf("合法线程续页应通过:%v", err)
	}
	threadFalseComplete := json.RawMessage(`{"messages":[],"reachedTop":false,"anchorMatched":false,"peer":null,"complete":true}`)
	assertValidationError(t, ValidatePrimitiveData(PrimChatReadThread, 1, threadFalseComplete), "$", "atLeastOneTrueWhen")
	threadContinuesAtTop := json.RawMessage(`{"messages":[],"reachedTop":true,"anchorMatched":false,"peer":null,"complete":false,"nextCursor":"opaque-next"}`)
	assertValidationError(t, ValidatePrimitiveData(PrimChatReadThread, 1, threadContinuesAtTop), "$.reachedTop", "allFalseWhen")
}

func TestPaginationAlsoEnforcesByteBudget(t *testing.T) {
	sessions := make([]ConversationSummary, DefaultPaginationReadListMaxItems)
	activity := int64(1)
	for i := range sessions {
		sessions[i] = ConversationSummary{
			ConversationRef: strings.Repeat("界", 512),
			Peer: PeerSummary{
				DisplayName:     strings.Repeat("界", 256),
				PlatformUserRef: strings.Repeat("界", 512),
			},
			LastMessage: LastMessageSummary{
				Direction:   MessageDirectionIn,
				Kind:        MessageKindText,
				TextPreview: strings.Repeat("界", 200),
			},
			LastActivityTs: &activity,
		}
	}
	raw, err := Encode(ChatReadListData{Sessions: sessions, Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	assertValidationError(t, ValidatePrimitiveData(PrimChatReadList, 1, raw), "$", "maxJsonBytes")
}

func TestFrameAndCompatibilityPolicies(t *testing.T) {
	if UnknownFieldPolicy != "must-ignore" || ContractHashPolicy != "warn-only" || JSONIntegerPolicy != "safe-int53" {
		t.Fatalf("兼容策略漂移:%s/%s/%s", UnknownFieldPolicy, ContractHashPolicy, JSONIntegerPolicy)
	}
	if err := ValidateFrameSize(make([]byte, DefaultMaxMsgBytes), 0); err != nil {
		t.Fatalf("limit 边界应通过:%v", err)
	}
	assertValidationError(t, ValidateFrameSize(make([]byte, DefaultMaxMsgBytes+1), 0), "$", "maxBytes")
	assertValidationError(t, ValidateKindBody(KindPong, json.RawMessage(`{"now":9007199254740992}`)), "$.now", "safeInteger")
}

func TestErrorPhaseAndCancelBatch(t *testing.T) {
	for code := range AckRejectableErrorCodes {
		if ErrorCodes[code].Phase != ErrorPhaseReceipt {
			t.Errorf("ack(rejected) 错误 %s 不是 receipt", code)
		}
	}
	for _, code := range []ErrorCode{ErrCodeUserActive, ErrCodeAccountMismatch, ErrCodeConversationNotFound, ErrCodeElementUnresolved} {
		if ErrorCodes[code].Phase != ErrorPhaseExecution {
			t.Errorf("accepted 后错误 %s 应走 execution/result", code)
		}
	}
	if Kinds[KindCancel].Batch != BatchS {
		t.Fatalf("cancel 必须在 S 批激活")
	}
	if ExpiredAtReceiptAckStatus != AckStatusAccepted || ExpiredAtReceiptResultStatus != ResultStatusExpired || ExpiredAtReceiptInvokeHandler || !ExpiredAtReceiptBypassQueueCapacity {
		t.Fatalf("接收时过期语义漂移")
	}
	if DefaultPaginationReadListMaxItems != 32 || DefaultPaginationReadThreadMaxItems != 64 {
		t.Fatalf("分页上限漂移:list=%d thread=%d", DefaultPaginationReadListMaxItems, DefaultPaginationReadThreadMaxItems)
	}
}

func assertValidationError(t *testing.T, err error, path, rule string) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望校验失败(path=%s rule=%s)", path, rule)
	}
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("期望 ValidationError,得到 %T:%v", err, err)
	}
	if validation.Path != path || validation.Rule != rule {
		t.Fatalf("校验错误不符:得到 path=%s rule=%s,期望 path=%s rule=%s (%v)", validation.Path, validation.Rule, path, rule, err)
	}
}
