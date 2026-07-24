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

func TestValidateDebugReloadPrimitive(t *testing.T) {
	command := json.RawMessage(`{
		"name":"debug.reload","ver":1,"args":{},
		"deadline":1999999999999,"execBudgetMs":5000
	}`)
	if err := ValidateKindBody(KindCmd, command); err != nil {
		t.Fatalf("debug.reload 空参数命令应通过正式命令校验: %v", err)
	}
	result := json.RawMessage(`{
		"ref":"reload-1","status":"ok","data":{},
		"replayed":false,"execMs":1
	}`)
	if err := ValidatePrimitiveResult(PrimDebugReload, 1, result); err != nil {
		t.Fatalf("debug.reload 空数据结果应通过原语结果校验: %v", err)
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
	withSourceKey := json.RawMessage(`{"messages":[{"idx":0,"direction":"in","kind":"text","text":"拒绝模板","blobRef":null,"contentHash":"hash","sourceKey":"` + strings.Repeat("a", 64) + `"}],"reachedTop":false,"anchorMatched":true,"peer":null,"complete":true}`)
	if err := ValidatePrimitiveData(PrimChatReadThread, 1, withSourceKey); err != nil {
		t.Fatalf("合法 sourceKey 应通过:%v", err)
	}
	shortSourceKey := json.RawMessage(`{"messages":[{"idx":0,"direction":"in","kind":"text","text":"拒绝模板","blobRef":null,"contentHash":"hash","sourceKey":"` + strings.Repeat("a", 63) + `"}],"reachedTop":false,"anchorMatched":true,"peer":null,"complete":true}`)
	assertValidationError(t, ValidatePrimitiveData(PrimChatReadThread, 1, shortSourceKey), "$.messages[0].sourceKey", "minLength")
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

func TestM3WitnessAdvertisementAndSendGuards(t *testing.T) {
	hello := json.RawMessage(`{
		"handId":"h-1","bootId":"b-1","protoSupported":[1],"contractHash":"sha256:x",
		"app":{"extVersion":"0.1.0","browser":"chrome"},"caps":["chat.sendMessage@1"],
		"features":["witness/1"],"witnessStoreId":"w-1","outboxPending":0,"journalOpen":0
	}`)
	if err := ValidateKindBody(KindHello, hello); err != nil {
		t.Fatalf("完整 witness/1 hello 应通过:%v", err)
	}
	missingStats := json.RawMessage(`{
		"handId":"h-1","bootId":"b-1","protoSupported":[1],"contractHash":"sha256:x",
		"app":{"extVersion":"0.1.0","browser":"chrome"},"caps":[],"features":["witness/1"]
	}`)
	assertValidationError(t, ValidateKindBody(KindHello, missingStats), "$", "witnessAdvertisement")

	valid := json.RawMessage(`{
		"name":"chat.sendMessage","ver":1,
		"context":{"platform":"zhilian","accountRef":"acc-1","expectedPrincipalFingerprint":"opaque"},
		"args":{"conversationRef":"conv-1","text":"你好"},
		"idemKey":"ik1:zhilian:acc-1:chat.sendMessage:conv-1:int-1",
		"deadline":1999999999999,"execBudgetMs":60000,"leaseMs":30000,
		"guards":{"expectedTail":[{"direction":"in","contentHash":"abc"}]}
	}`)
	if err := ValidateKindBody(KindCmd, valid); err != nil {
		t.Fatalf("合法 chat.sendMessage 命令应通过:%v", err)
	}
	missingGuards := json.RawMessage(`{
		"name":"chat.sendMessage","ver":1,
		"context":{"platform":"zhilian","accountRef":"acc-1","expectedPrincipalFingerprint":"opaque"},
		"args":{"conversationRef":"conv-1","text":"你好"},
		"idemKey":"ik1:zhilian:acc-1:chat.sendMessage:conv-1:int-1",
		"deadline":1999999999999,"execBudgetMs":60000,"leaseMs":30000
	}`)
	assertValidationError(t, ValidateKindBody(KindCmd, missingGuards), "$.guards", "required")
}

func TestM3JournalOutboxAndReportInvariants(t *testing.T) {
	committed := json.RawMessage(`{
		"ref":"cmd-1","idemKey":"ik-1","state":"committed","startedAt":10,"committedAt":20,
		"result":{"ref":"cmd-1","status":"ok","data":{"conversationRef":"conv-1","contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","observedAt":20},"evidence":[{"type":"outboundMessageObserved"}],"replayed":false,"execMs":10},
		"expiresAt":100
	}`)
	if err := ValidateSchema("JournalEntry", committed); err != nil {
		t.Fatalf("合法 committed journal 应通过:%v", err)
	}
	committedNullResult := json.RawMessage(`{
		"ref":"cmd-1","idemKey":"ik-1","state":"committed","startedAt":10,"committedAt":20,
		"result":null,"expiresAt":100
	}`)
	assertValidationError(t, ValidateSchema("JournalEntry", committedNullResult), "$.result", "nullable")
	attemptingWithResult := json.RawMessage(`{
		"ref":"cmd-1","idemKey":"ik-1","state":"attempting","startedAt":10,
		"result":{"ref":"cmd-1","status":"expired","replayed":false,"execMs":0},"expiresAt":100
	}`)
	assertValidationError(t, ValidateSchema("JournalEntry", attemptingWithResult), "$.result", "forbiddenWhen")

	outbox := json.RawMessage(`{
		"message":{"proto":1,"kind":"result","msgId":"result-1","session":"session-created",
		"ts":20,"attempt":1,"body":{"ref":"cmd-1","status":"expired","replayed":false,"execMs":0}},
		"createdAt":20,"expiresAt":100
	}`)
	if err := ValidateSchema("OutboxEntry", outbox); err != nil {
		t.Fatalf("合法 outbox 应通过:%v", err)
	}
	outboxNullSession := json.RawMessage(`{
		"message":{"proto":1,"kind":"result","msgId":"result-1","session":null,
		"ts":20,"attempt":1,"body":{"ref":"cmd-1","status":"expired","replayed":false,"execMs":0}},
		"createdAt":20,"expiresAt":100
	}`)
	assertValidationError(t, ValidateSchema("OutboxEntry", outboxNullSession), "$.message.session", "nullable")

	done := json.RawMessage(`{
		"ref":"cmd-1","witnessStoreId":"w-1","state":"done",
		"result":{"ref":"cmd-1","status":"ok","data":{},"evidence":[{"type":"outboundMessageObserved"}],"replayed":true,"execMs":10},
		"journal":{"ref":"cmd-1","idemKey":"ik-1","state":"committed","startedAt":10,"committedAt":20}
	}`)
	if err := ValidateKindBody(KindReport, done); err != nil {
		t.Fatalf("合法 done report 应通过:%v", err)
	}
	doneNull := json.RawMessage(`{"ref":"cmd-1","witnessStoreId":"w-1","state":"done","result":null,"journal":null}`)
	assertValidationError(t, ValidateKindBody(KindReport, doneNull), "$.result", "requiredWhen")
	wrongAttempt := json.RawMessage(`{
		"ref":"cmd-1","witnessStoreId":"w-1","state":"attempting","result":null,
		"journal":{"ref":"cmd-1","idemKey":"ik-1","state":"committed","startedAt":10,"committedAt":20}
	}`)
	assertValidationError(t, ValidateKindBody(KindReport, wrongAttempt), "$.journal.state", "state")
}

func TestM3EffectfulResultEvidenceAndWitnessError(t *testing.T) {
	valid := json.RawMessage(`{
		"ref":"cmd-1","status":"ok",
		"data":{"conversationRef":"conv-1","contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","observedAt":20},
		"evidence":[{"type":"outboundMessageObserved"}],"replayed":false,"execMs":10
	}`)
	if err := ValidatePrimitiveResult(PrimChatSendMessage, 1, valid); err != nil {
		t.Fatalf("合法 effectful result 应通过:%v", err)
	}
	missingEvidence := json.RawMessage(`{
		"ref":"cmd-1","status":"ok",
		"data":{"conversationRef":"conv-1","contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","observedAt":20},
		"replayed":false,"execMs":10
	}`)
	assertValidationError(t, ValidatePrimitiveResult(PrimChatSendMessage, 1, missingEvidence), "$.evidence", "minItems")
	wrongEvidence := json.RawMessage(`{
		"ref":"cmd-1","status":"ok",
		"data":{"conversationRef":"conv-1","contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","observedAt":20},
		"evidence":[{"type":"clicked"}],"replayed":false,"execMs":10
	}`)
	assertValidationError(t, ValidatePrimitiveResult(PrimChatSendMessage, 1, wrongEvidence), "$.evidence[0].type", "enum")
	failedWithoutSideEffect := json.RawMessage(`{
		"ref":"cmd-1","status":"failed","error":{"code":"GUARD_FAILED","retryable":"manualOnly"},
		"replayed":false,"execMs":1
	}`)
	assertValidationError(t, ValidatePrimitiveResult(PrimChatSendMessage, 1, failedWithoutSideEffect), "$.error.sideEffect", "required")
	witnessFailure := json.RawMessage(`{
		"ref":"cmd-1","status":"failed",
		"error":{"code":"WITNESS_UNAVAILABLE","retryable":"afterRecovery","sideEffect":"none","data":{"reason":"writeFailed"}},
		"replayed":false,"execMs":1
	}`)
	if err := ValidatePrimitiveResult(PrimChatSendMessage, 1, witnessFailure); err != nil {
		t.Fatalf("动作前 witness 写失败应可诚实表达:%v", err)
	}
	witnessWithoutData := json.RawMessage(`{
		"ref":"cmd-1","status":"failed",
		"error":{"code":"WITNESS_UNAVAILABLE","retryable":"afterRecovery","sideEffect":"none"},
		"replayed":false,"execMs":1
	}`)
	assertValidationError(t, ValidatePrimitiveResult(PrimChatSendMessage, 1, witnessWithoutData), "$.error.data", "required")

	meta := Primitives[PrimChatSendMessage]
	if meta.GuardsSchema == "" || meta.EvidenceSchema == "" || meta.VerificationPrimitive != PrimChatReadThread || meta.VerificationMaxRounds != 3 {
		t.Fatalf("真实 SX 契约元数据不完整:%+v", meta)
	}
}

func TestM4CandidateAndGreetingSchemas(t *testing.T) {
	candidateCommand := json.RawMessage(`{"name":"candidate.readCurrent","ver":1,"context":{"platform":"zhilian","accountRef":"acc-1","expectedPrincipalFingerprint":"opaque"},"args":{},"deadline":1999999999999,"execBudgetMs":5000}`)
	if err := ValidateKindBody(KindCmd, candidateCommand); err != nil {
		t.Fatalf("合法 candidate.readCurrent 命令应通过:%v", err)
	}
	for _, state := range []string{"unestablished", "established", "unknown"} {
		data := json.RawMessage(`{"platformUserRef":"user-1","displayName":null,"positionRef":"job-1","positionTitle":null,"contactState":"` + state + `"}`)
		if err := ValidatePrimitiveData(PrimCandidateReadCurrent, 1, data); err != nil {
			t.Fatalf("合法 current candidate 状态 %s 应通过:%v", state, err)
		}
	}
	assertValidationError(t, ValidatePrimitiveData(PrimCandidateReadCurrent, 1, json.RawMessage(`{"displayName":null,"positionRef":"job-1","positionTitle":null,"contactState":"unknown"}`)), "$.platformUserRef", "required")
	assertValidationError(t, ValidatePrimitiveData(PrimCandidateReadCurrent, 1, json.RawMessage(`{"platformUserRef":"user-1","positionRef":"job-1","positionTitle":null,"contactState":"unknown"}`)), "$.displayName", "required")
	assertValidationError(t, ValidatePrimitiveData(PrimCandidateReadCurrent, 1, json.RawMessage(`{"platformUserRef":"user-1","displayName":null,"positionRef":"job-1","positionTitle":null,"contactState":"bad"}`)), "$.contactState", "enum")

	validArgs := json.RawMessage(`{"platformUserRef":"user-1","positionRef":"job-1","text":"你好"}`)
	if err := ValidatePrimitiveArgs(PrimChatSendGreeting, 1, validArgs); err != nil {
		t.Fatalf("合法 greeting args 应通过:%v", err)
	}
	assertValidationError(t, ValidatePrimitiveArgs(PrimChatSendGreeting, 1, json.RawMessage(`{"platformUserRef":"user-1","positionRef":"job-1","text":""}`)), "$.text", "minLength")
	tooManyBytes := `{"platformUserRef":"user-1","positionRef":"job-1","text":"` + strings.Repeat("界", 683) + `"}`
	assertValidationError(t, ValidatePrimitiveArgs(PrimChatSendGreeting, 1, json.RawMessage(tooManyBytes)), "$.text", "maxBytes")

	if err := ValidatePrimitiveGuards(PrimChatSendGreeting, 1, json.RawMessage(`{"expectUnestablished":true}`)); err != nil {
		t.Fatalf("合法 greeting guards 应通过:%v", err)
	}
	assertValidationError(t, ValidatePrimitiveGuards(PrimChatSendGreeting, 1, json.RawMessage(`{"expectUnestablished":false}`)), "$.expectUnestablished", "const")
	missingGreetingGuards := json.RawMessage(`{"name":"chat.sendGreeting","ver":1,"context":{"platform":"zhilian","accountRef":"acc-1","expectedPrincipalFingerprint":"opaque"},"args":{"platformUserRef":"user-1","positionRef":"job-1","text":"你好"},"idemKey":"ik1:zhilian:acc-1:chat.sendGreeting:profile-1:int-1","deadline":1999999999999,"execBudgetMs":60000,"leaseMs":30000}`)
	assertValidationError(t, ValidateKindBody(KindCmd, missingGreetingGuards), "$.guards", "required")

	validResult := json.RawMessage(`{
		"ref":"greet-1","status":"ok",
		"data":{"platformUserRef":"user-1","positionRef":"job-1","conversationRef":"conv-1","contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","observedAt":20},
		"evidence":[{"type":"outboundGreetingObserved"}],"replayed":false,"execMs":10
	}`)
	if err := ValidatePrimitiveResult(PrimChatSendGreeting, 1, validResult); err != nil {
		t.Fatalf("合法 greeting result 应通过:%v", err)
	}
	visibleRelationshipResult := json.RawMessage(`{"ref":"greet-1","status":"ok","data":{"platformUserRef":"user-1","positionRef":"job-1","contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","observedAt":20},"evidence":[{"type":"outboundGreetingObserved"}],"replayed":false,"execMs":10}`)
	if err := ValidatePrimitiveResult(PrimChatSendGreeting, 1, visibleRelationshipResult); err != nil {
		t.Fatalf("不带 conversationRef 的可见关系正证应通过:%v", err)
	}
	missingEvidence := json.RawMessage(`{"ref":"greet-1","status":"ok","data":{"platformUserRef":"user-1","positionRef":"job-1","conversationRef":"conv-1","contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","observedAt":20},"replayed":false,"execMs":10}`)
	assertValidationError(t, ValidatePrimitiveResult(PrimChatSendGreeting, 1, missingEvidence), "$.evidence", "minItems")
	wrongEvidence := json.RawMessage(`{"ref":"greet-1","status":"ok","data":{"platformUserRef":"user-1","positionRef":"job-1","conversationRef":"conv-1","contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","observedAt":20},"evidence":[{"type":"outboundMessageObserved"}],"replayed":false,"execMs":10}`)
	assertValidationError(t, ValidatePrimitiveResult(PrimChatSendGreeting, 1, wrongEvidence), "$.evidence[0].type", "enum")
	shortHash := json.RawMessage(`{"ref":"greet-1","status":"ok","data":{"platformUserRef":"user-1","positionRef":"job-1","conversationRef":"conv-1","contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","observedAt":20},"evidence":[{"type":"outboundGreetingObserved"}],"replayed":false,"execMs":10}`)
	assertValidationError(t, ValidatePrimitiveResult(PrimChatSendGreeting, 1, shortHash), "$.data.contentHash", "minLength")
	longHash := json.RawMessage(`{"ref":"greet-1","status":"ok","data":{"platformUserRef":"user-1","positionRef":"job-1","conversationRef":"conv-1","contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","observedAt":20},"evidence":[{"type":"outboundGreetingObserved"}],"replayed":false,"execMs":10}`)
	assertValidationError(t, ValidatePrimitiveResult(PrimChatSendGreeting, 1, longHash), "$.data.contentHash", "maxLength")

	outcomeCommand := json.RawMessage(`{"name":"chat.readGreetingOutcome","ver":1,"context":{"platform":"zhilian","accountRef":"acc-1","expectedPrincipalFingerprint":"opaque"},"args":{"platformUserRef":"user-1","positionRef":"job-1","contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"deadline":1999999999999,"execBudgetMs":240000,"leaseMs":30000}`)
	if err := ValidateKindBody(KindCmd, outcomeCommand); err != nil {
		t.Fatalf("合法 chat.readGreetingOutcome 命令应通过:%v", err)
	}
	confirmed := json.RawMessage(`{"confirmed":true,"conversationRef":"conv-1","contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","observedAt":20}`)
	if err := ValidatePrimitiveData(PrimChatReadGreetingOutcome, 1, confirmed); err != nil {
		t.Fatalf("合法 confirmed greeting outcome 应通过:%v", err)
	}
	confirmedWithoutConversation := json.RawMessage(`{"confirmed":true,"contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","observedAt":20}`)
	if err := ValidatePrimitiveData(PrimChatReadGreetingOutcome, 1, confirmedWithoutConversation); err != nil {
		t.Fatalf("不带 conversationRef 的可见关系验证正证应通过:%v", err)
	}
	assertValidationError(t, ValidatePrimitiveData(PrimChatReadGreetingOutcome, 1, json.RawMessage(`{"confirmed":true,"conversationRef":"conv-1","observedAt":20}`)), "$.contentHash", "requiredWhen")
	unconfirmed := json.RawMessage(`{"confirmed":false,"observedAt":20}`)
	if err := ValidatePrimitiveData(PrimChatReadGreetingOutcome, 1, unconfirmed); err != nil {
		t.Fatalf("合法 unconfirmed greeting outcome 应通过:%v", err)
	}
	assertValidationError(t, ValidatePrimitiveData(PrimChatReadGreetingOutcome, 1, json.RawMessage(`{"confirmed":false,"conversationRef":"conv-1","observedAt":20}`)), "$.conversationRef", "forbiddenWhen")
	assertValidationError(t, ValidatePrimitiveData(PrimChatReadGreetingOutcome, 1, json.RawMessage(`{"confirmed":false,"contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","observedAt":20}`)), "$.contentHash", "forbiddenWhen")

	rejected := json.RawMessage(`{"ref":"greet-1","status":"failed","error":{"code":"GREETING_REJECTED","retryable":"no","sideEffect":"none"},"replayed":false,"execMs":1}`)
	if err := ValidatePrimitiveResult(PrimChatSendGreeting, 1, rejected); err != nil {
		t.Fatalf("GREETING_REJECTED/none 应通过:%v", err)
	}
	rejectedWithoutSideEffect := json.RawMessage(`{"ref":"greet-1","status":"failed","error":{"code":"GREETING_REJECTED","retryable":"no"},"replayed":false,"execMs":1}`)
	assertValidationError(t, ValidatePrimitiveResult(PrimChatSendGreeting, 1, rejectedWithoutSideEffect), "$.error.sideEffect", "required")
	rejectedPossible := json.RawMessage(`{"ref":"greet-1","status":"failed","error":{"code":"GREETING_REJECTED","retryable":"no","sideEffect":"possible"},"replayed":false,"execMs":1}`)
	assertValidationError(t, ValidatePrimitiveResult(PrimChatSendGreeting, 1, rejectedPossible), "$.error.sideEffect", "errorCodeSideEffect")
	rejectedConfirmed := json.RawMessage(`{"ref":"greet-1","status":"failed","error":{"code":"GREETING_REJECTED","retryable":"no","sideEffect":"confirmed"},"replayed":false,"execMs":1}`)
	assertValidationError(t, ValidatePrimitiveResult(PrimChatSendGreeting, 1, rejectedConfirmed), "$.error.sideEffect", "errorCodeSideEffect")

	meta := Primitives[PrimChatSendGreeting]
	if meta.Ver != 1 || meta.GuardsSchema == "" || meta.EvidenceSchema == "" || meta.VerificationPrimitive != PrimChatReadGreetingOutcome || meta.VerificationVer != 1 || meta.VerificationMaxRounds != DefaultVerificationMaxRounds {
		t.Fatalf("greeting verifier metadata 漂移:%+v", meta)
	}
}

func TestM5BCardPrimitiveSchemas(t *testing.T) {
	wechatCommand := json.RawMessage(`{
		"name":"chat.sendWechatInvite","ver":1,
		"context":{"platform":"zhilian","accountRef":"acc-1","expectedPrincipalFingerprint":"opaque"},
		"args":{"conversationRef":"conv-1"},
		"idemKey":"ik1:zhilian:acc-1:chat.sendWechatInvite:conv-1:int-1",
		"deadline":1999999999999,"execBudgetMs":60000,"leaseMs":30000,
		"guards":{"expectedTail":[{"direction":"in","contentHash":"tail-hash"}]}
	}`)
	if err := ValidateKindBody(KindCmd, wechatCommand); err != nil {
		t.Fatalf("合法 chat.sendWechatInvite 命令应通过:%v", err)
	}
	wechatMissingGuards := json.RawMessage(`{
		"name":"chat.sendWechatInvite","ver":1,
		"context":{"platform":"zhilian","accountRef":"acc-1","expectedPrincipalFingerprint":"opaque"},
		"args":{"conversationRef":"conv-1"},
		"idemKey":"ik1:zhilian:acc-1:chat.sendWechatInvite:conv-1:int-1",
		"deadline":1999999999999,"execBudgetMs":60000,"leaseMs":30000
	}`)
	assertValidationError(t, ValidateKindBody(KindCmd, wechatMissingGuards), "$.guards", "required")

	inviteCommand := json.RawMessage(`{
		"name":"chat.sendInviteCard","ver":1,
		"context":{"platform":"zhilian","accountRef":"acc-1","expectedPrincipalFingerprint":"opaque"},
		"args":{"conversationRef":"conv-1","interview":{"startsAt":1000,"endsAt":2000,"method":"wechatVideo"}},
		"idemKey":"ik1:zhilian:acc-1:chat.sendInviteCard:conv-1:int-1",
		"deadline":1999999999999,"execBudgetMs":120000,"leaseMs":30000,
		"guards":{"expectedTail":[{"direction":"in","contentHash":"tail-hash"}]}
	}`)
	if err := ValidateKindBody(KindCmd, inviteCommand); err != nil {
		t.Fatalf("合法 chat.sendInviteCard 命令应通过:%v", err)
	}
	invalidMethod := json.RawMessage(`{
		"name":"chat.sendInviteCard","ver":1,
		"context":{"platform":"zhilian","accountRef":"acc-1","expectedPrincipalFingerprint":"opaque"},
		"args":{"conversationRef":"conv-1","interview":{"startsAt":1000,"endsAt":2000,"method":"phone"}},
		"idemKey":"ik1:zhilian:acc-1:chat.sendInviteCard:conv-1:int-1",
		"deadline":1999999999999,"execBudgetMs":120000,"leaseMs":30000,
		"guards":{"expectedTail":[{"direction":"in","contentHash":"tail-hash"}]}
	}`)
	assertValidationError(t, ValidateKindBody(KindCmd, invalidMethod), "$.args.interview.method", "enum")
	invalidStart := json.RawMessage(`{
		"name":"chat.sendInviteCard","ver":1,
		"context":{"platform":"zhilian","accountRef":"acc-1","expectedPrincipalFingerprint":"opaque"},
		"args":{"conversationRef":"conv-1","interview":{"startsAt":0,"endsAt":2000,"method":"wechatVideo"}},
		"idemKey":"ik1:zhilian:acc-1:chat.sendInviteCard:conv-1:int-1",
		"deadline":1999999999999,"execBudgetMs":120000,"leaseMs":30000,
		"guards":{"expectedTail":[{"direction":"in","contentHash":"tail-hash"}]}
	}`)
	assertValidationError(t, ValidateKindBody(KindCmd, invalidStart), "$.args.interview.startsAt", "minimum")

	wechatResult := json.RawMessage(`{
		"ref":"wechat-1","status":"ok",
		"data":{"conversationRef":"conv-1","contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","sourceKey":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","observedAt":20},
		"evidence":[{"type":"outboundWechatInviteObserved"}],"replayed":false,"execMs":10
	}`)
	if err := ValidatePrimitiveResult(PrimChatSendWechatInvite, 1, wechatResult); err != nil {
		t.Fatalf("合法换微信邀请 result 应通过:%v", err)
	}
	wechatMissingSourceKey := json.RawMessage(`{
		"ref":"wechat-1","status":"ok",
		"data":{"conversationRef":"conv-1","contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","observedAt":20},
		"evidence":[{"type":"outboundWechatInviteObserved"}],"replayed":false,"execMs":10
	}`)
	assertValidationError(t, ValidatePrimitiveResult(PrimChatSendWechatInvite, 1, wechatMissingSourceKey), "$.data.sourceKey", "required")

	inviteResult := json.RawMessage(`{
		"ref":"invite-1","status":"ok",
		"data":{"conversationRef":"conv-1","contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","sourceKey":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","interview":{"startsAt":1000,"endsAt":2000,"method":"wechatVideo"},"observedAt":20},
		"evidence":[{"type":"outboundInterviewInviteObserved"}],"replayed":false,"execMs":10
	}`)
	if err := ValidatePrimitiveResult(PrimChatSendInviteCard, 1, inviteResult); err != nil {
		t.Fatalf("合法邀面卡 result 应通过:%v", err)
	}
	inviteWrongEvidence := json.RawMessage(`{
		"ref":"invite-1","status":"ok",
		"data":{"conversationRef":"conv-1","contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","sourceKey":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","interview":{"startsAt":1000,"endsAt":2000,"method":"wechatVideo"},"observedAt":20},
		"evidence":[{"type":"outboundWechatInviteObserved"}],"replayed":false,"execMs":10
	}`)
	assertValidationError(t, ValidatePrimitiveResult(PrimChatSendInviteCard, 1, inviteWrongEvidence), "$.evidence[0].type", "enum")

	threadWithInterview := json.RawMessage(`{
		"messages":[{"idx":0,"direction":"out","kind":"card","text":null,"blobRef":null,
			"contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"sourceKey":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			"cardType":"interviewInvite","cardState":"unknown",
			"interview":{"startsAt":1000,"endsAt":2000,"method":"wechatVideo"}}],
		"reachedTop":true,"anchorMatched":false,"peer":null,"complete":true
	}`)
	if err := ValidatePrimitiveData(PrimChatReadThread, 1, threadWithInterview); err != nil {
		t.Fatalf("合法邀面卡线程消息应通过:%v", err)
	}
	threadWithInvalidMethod := json.RawMessage(`{
		"messages":[{"idx":0,"direction":"out","kind":"card","text":null,"blobRef":null,
			"contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"cardType":"interviewInvite","cardState":"unknown",
			"interview":{"startsAt":1000,"endsAt":2000,"method":"phone"}}],
		"reachedTop":true,"anchorMatched":false,"peer":null,"complete":true
	}`)
	assertValidationError(t, ValidatePrimitiveData(PrimChatReadThread, 1, threadWithInvalidMethod), "$.messages[0].interview.method", "enum")

	acceptCommand := json.RawMessage(`{
		"name":"chat.acceptWechat","ver":1,
		"context":{"platform":"zhilian","accountRef":"acc-1","expectedPrincipalFingerprint":"opaque"},
		"args":{"conversationRef":"conv-1","requestSourceKey":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		"idemKey":"ik1:zhilian:acc-1:chat.acceptWechat:conv-1:req-1",
		"deadline":1999999999999,"execBudgetMs":60000,"leaseMs":30000,
		"guards":{"expectedTail":[{"direction":"in","contentHash":"tail-hash"}]}
	}`)
	if err := ValidateKindBody(KindCmd, acceptCommand); err != nil {
		t.Fatalf("合法 chat.acceptWechat 命令应通过:%v", err)
	}
	acceptResult := json.RawMessage(`{
		"ref":"accept-1","status":"ok",
		"data":{"conversationRef":"conv-1",
			"requestSourceKey":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"exchangeSourceKey":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			"peerWechat":"peer-wechat","observedAt":20},
		"evidence":[{"type":"candidateWechatRequestAcceptedObserved"}],"replayed":false,"execMs":10
	}`)
	if err := ValidatePrimitiveResult(PrimChatAcceptWechat, 1, acceptResult); err != nil {
		t.Fatalf("合法接受微信 result 应通过:%v", err)
	}
	acceptWrongEvidence := json.RawMessage(`{
		"ref":"accept-1","status":"ok",
		"data":{"conversationRef":"conv-1",
			"requestSourceKey":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"exchangeSourceKey":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			"peerWechat":"peer-wechat","observedAt":20},
		"evidence":[{"type":"outboundWechatInviteObserved"}],"replayed":false,"execMs":10
	}`)
	assertValidationError(t, ValidatePrimitiveResult(PrimChatAcceptWechat, 1, acceptWrongEvidence), "$.evidence[0].type", "enum")

	readOutcomeCommand := json.RawMessage(`{
		"name":"chat.readWechatExchangeOutcome","ver":1,
		"context":{"platform":"zhilian","accountRef":"acc-1","expectedPrincipalFingerprint":"opaque"},
		"args":{"conversationRef":"conv-1","requestSourceKey":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		"deadline":1999999999999,"execBudgetMs":30000
	}`)
	if err := ValidateKindBody(KindCmd, readOutcomeCommand); err != nil {
		t.Fatalf("合法微信 outcome 读命令应通过:%v", err)
	}
	confirmedOutcome := json.RawMessage(`{
		"confirmed":true,
		"exchangeSourceKey":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"peerWechat":"peer-wechat","observedAt":20
	}`)
	if err := ValidatePrimitiveData(PrimChatReadWechatExchangeOutcome, 1, confirmedOutcome); err != nil {
		t.Fatalf("合法微信 outcome data 应通过:%v", err)
	}
	unconfirmedOutcome := json.RawMessage(`{"confirmed":false,"observedAt":20}`)
	if err := ValidatePrimitiveData(PrimChatReadWechatExchangeOutcome, 1, unconfirmedOutcome); err != nil {
		t.Fatalf("合法未确认 outcome data 应通过:%v", err)
	}
	unconfirmedWithContact := json.RawMessage(`{
		"confirmed":false,
		"exchangeSourceKey":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"peerWechat":"peer-wechat","observedAt":20
	}`)
	assertValidationError(t, ValidatePrimitiveData(PrimChatReadWechatExchangeOutcome, 1, unconfirmedWithContact), "$.exchangeSourceKey", "forbiddenWhen")

	for _, primitive := range []string{PrimChatSendWechatInvite, PrimChatSendInviteCard} {
		meta := Primitives[primitive]
		if meta.Ver != 1 || meta.Class != ClassEffectful || meta.Batch != BatchX ||
			meta.GuardsSchema != "ChatSendMessageGuards" || meta.EvidenceSchema == "" ||
			meta.VerificationPrimitive != PrimChatReadThread || meta.VerificationVer != 1 ||
			meta.VerificationMaxRounds != DefaultVerificationMaxRounds {
			t.Fatalf("%s 卡片发送元数据漂移:%+v", primitive, meta)
		}
	}
	acceptMeta := Primitives[PrimChatAcceptWechat]
	if acceptMeta.Ver != 1 || acceptMeta.Class != ClassEffectful || acceptMeta.Batch != BatchX ||
		acceptMeta.GuardsSchema != "ChatSendMessageGuards" ||
		acceptMeta.EvidenceSchema != "ChatAcceptWechatEvidence" ||
		acceptMeta.VerificationPrimitive != PrimChatReadWechatExchangeOutcome ||
		acceptMeta.VerificationVer != 1 ||
		acceptMeta.VerificationMaxRounds != DefaultVerificationMaxRounds {
		t.Fatalf("chat.acceptWechat 元数据漂移:%+v", acceptMeta)
	}
	outcomeMeta := Primitives[PrimChatReadWechatExchangeOutcome]
	if outcomeMeta.Ver != 1 || outcomeMeta.Class != ClassReadonly || outcomeMeta.Batch != BatchX ||
		outcomeMeta.GuardsSchema != "" || outcomeMeta.EvidenceSchema != "" ||
		outcomeMeta.VerificationPrimitive != "" {
		t.Fatalf("chat.readWechatExchangeOutcome 元数据漂移:%+v", outcomeMeta)
	}
}

func TestM5CandidateReadResumeSchemas(t *testing.T) {
	command := json.RawMessage(`{"name":"candidate.readResume","ver":1,"context":{"platform":"zhilian","accountRef":"acc-1","expectedPrincipalFingerprint":"opaque"},"args":{"conversationRef":"conv-1","platformUserRef":"user-1"},"deadline":1999999999999,"execBudgetMs":60000,"leaseMs":30000}`)
	if err := ValidateKindBody(KindCmd, command); err != nil {
		t.Fatalf("合法 candidate.readResume 命令应通过:%v", err)
	}
	missingLease := json.RawMessage(`{"name":"candidate.readResume","ver":1,"context":{"platform":"zhilian","accountRef":"acc-1","expectedPrincipalFingerprint":"opaque"},"args":{"conversationRef":"conv-1","platformUserRef":"user-1"},"deadline":1999999999999,"execBudgetMs":60000}`)
	assertValidationError(t, ValidateKindBody(KindCmd, missingLease), "$.leaseMs", "required")
	withIdemKey := json.RawMessage(`{"name":"candidate.readResume","ver":1,"context":{"platform":"zhilian","accountRef":"acc-1","expectedPrincipalFingerprint":"opaque"},"args":{"conversationRef":"conv-1","platformUserRef":"user-1"},"idemKey":"forbidden","deadline":1999999999999,"execBudgetMs":60000,"leaseMs":30000}`)
	assertValidationError(t, ValidateKindBody(KindCmd, withIdemKey), "$.idemKey", "forbidden")
	withGuards := json.RawMessage(`{"name":"candidate.readResume","ver":1,"context":{"platform":"zhilian","accountRef":"acc-1","expectedPrincipalFingerprint":"opaque"},"args":{"conversationRef":"conv-1","platformUserRef":"user-1"},"deadline":1999999999999,"execBudgetMs":60000,"leaseMs":30000,"guards":{}}`)
	assertValidationError(t, ValidateKindBody(KindCmd, withGuards), "$.guards", "forbidden")

	meta := Primitives[PrimCandidateReadResume]
	wantPreconditions := []string{"context.platform", "context.accountRef", "context.expectedPrincipalFingerprint", "surface.im", "login.in", "conversation.tracked", "manualQuiet"}
	if meta.Ver != 1 || meta.Class != ClassIntrusive || meta.Batch != BatchX || meta.PlatformSideEffect != "none" ||
		meta.ExecBudgetMs != 60000 || meta.DeadlineMs != 120000 || meta.LeaseMs != 30000 ||
		meta.GuardsSchema != "" || meta.EvidenceSchema != "" || meta.VerificationPrimitive != "" ||
		strings.Join(meta.Preconditions, "\x00") != strings.Join(wantPreconditions, "\x00") {
		t.Fatalf("candidate.readResume metadata 漂移:%+v", meta)
	}

	valid := map[string]any{
		"conversationRef": "conv-1", "platformUserRef": "user-1", "observedAt": int64(20),
		"basic": []any{map[string]any{"label": "姓名", "value": ""}},
		"expectations": []any{}, "selfEvaluation": "", "education": "本科", "workExperiences": "",
	}
	validRaw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePrimitiveData(PrimCandidateReadResume, 1, validRaw); err != nil {
		t.Fatalf("合法五分区简历应通过:%v", err)
	}
	for _, field := range []string{"conversationRef", "platformUserRef", "observedAt", "basic", "expectations", "selfEvaluation", "education", "workExperiences"} {
		missing := make(map[string]any, len(valid)-1)
		for key, value := range valid {
			if key != field {
				missing[key] = value
			}
		}
		raw, marshalErr := json.Marshal(missing)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		assertValidationError(t, ValidatePrimitiveData(PrimCandidateReadResume, 1, raw), "$."+field, "required")
	}
	assertValidationError(t, ValidatePrimitiveData(PrimCandidateReadResume, 1, json.RawMessage(`{"conversationRef":"conv-1","platformUserRef":"user-1","observedAt":-1,"basic":[],"expectations":[],"selfEvaluation":"","education":"","workExperiences":""}`)), "$.observedAt", "minimum")
	assertValidationError(t, ValidatePrimitiveData(PrimCandidateReadResume, 1, json.RawMessage(`{"conversationRef":"conv-1","platformUserRef":"user-1","observedAt":"2026-07-21T00:00:00Z","basic":[],"expectations":[],"selfEvaluation":"","education":"","workExperiences":""}`)), "$.observedAt", "type")
	assertValidationError(t, ValidatePrimitiveData(PrimCandidateReadResume, 1, json.RawMessage(`{"conversationRef":"conv-1","platformUserRef":"user-1","observedAt":20,"basic":[{"label":"","value":""}],"expectations":[],"selfEvaluation":"","education":"","workExperiences":""}`)), "$.basic[0].label", "minLength")
	assertValidationError(t, ValidatePrimitiveData(PrimCandidateReadResume, 1, json.RawMessage(`{"conversationRef":"conv-1","platformUserRef":"user-1","observedAt":20,"basic":null,"expectations":[],"selfEvaluation":"","education":"","workExperiences":""}`)), "$.basic", "nullable")

	boundary := map[string]any{
		"conversationRef": "conv-1", "platformUserRef": "user-1", "observedAt": int64(20),
		"basic": []any{}, "expectations": []any{}, "selfEvaluation": "", "education": "", "workExperiences": "",
	}
	baseRaw, err := json.Marshal(boundary)
	if err != nil {
		t.Fatal(err)
	}
	boundary["selfEvaluation"] = strings.Repeat("a", 65536-len(baseRaw))
	limitRaw, err := json.Marshal(boundary)
	if err != nil {
		t.Fatal(err)
	}
	if len(limitRaw) != 65536 {
		t.Fatalf("边界 fixture 长度=%d, want 65536", len(limitRaw))
	}
	if err := ValidatePrimitiveData(PrimCandidateReadResume, 1, limitRaw); err != nil {
		t.Fatalf("65536-byte data 应通过:%v", err)
	}
	boundary["selfEvaluation"] = boundary["selfEvaluation"].(string) + "a"
	overLimitRaw, err := json.Marshal(boundary)
	if err != nil {
		t.Fatal(err)
	}
	assertValidationError(t, ValidatePrimitiveData(PrimCandidateReadResume, 1, overLimitRaw), "$", "maxJsonBytes")
}

func TestM6CandidateApplySourcingFiltersSchemas(t *testing.T) {
	meta := Primitives[PrimCandidateApplySourcingFilters]
	wantPreconditions := []string{
		"context.platform",
		"context.accountRef",
		"context.expectedPrincipalFingerprint",
		"login.in",
		"manualQuiet",
	}
	if meta.Ver != 1 || meta.Class != ClassIntrusive || meta.Batch != BatchS || meta.PlatformSideEffect != "none" ||
		meta.ExecBudgetMs != 120000 || meta.DeadlineMs != 180000 || meta.LeaseMs != 30000 ||
		meta.ArgsSchema != "CandidateApplySourcingFiltersArgs" || meta.DataSchema != "CandidateApplySourcingFiltersData" ||
		meta.GuardsSchema != "" || meta.EvidenceSchema != "" || meta.VerificationPrimitive != "" ||
		meta.VerificationVer != 0 || meta.VerificationMaxRounds != 0 || meta.ContextOptionalBeforeBinding ||
		strings.Join(meta.Preconditions, "\x00") != strings.Join(wantPreconditions, "\x00") {
		t.Fatalf("candidate.applySourcingFilters metadata 漂移:%+v", meta)
	}

	newFilters := func(age map[string]any) map[string]any {
		return map[string]any{
			"age":                      age,
			"activeWindow":             "days3",
			"careerStatuses":           []any{"employedLooking", "leftLooking"},
			"educations":               []any{"associate", "bachelor", "master"},
			"gender":                   "any",
			"excludeViewed":            true,
			"excludeCoworkerContacted": false,
		}
	}
	commandFor := func(filters map[string]any) json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"name": PrimCandidateApplySourcingFilters,
			"ver":  1,
			"context": map[string]any{
				"platform":                     "zhilian",
				"accountRef":                   "acc-1",
				"expectedPrincipalFingerprint": "opaque",
			},
			"args": map[string]any{
				"positionRef":   "position-1",
				"positionTitle": "后端工程师",
				"filters":       filters,
			},
			"deadline":     int64(1999999999999),
			"execBudgetMs": int64(120000),
			"leaseMs":      int64(30000),
		})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	for name, filters := range map[string]map[string]any{
		"不限年龄": newFilters(map[string]any{"mode": "any"}),
		"年龄闭区间": newFilters(map[string]any{
			"mode": "range", "minAge": 25, "maxAge": 45,
		}),
		"只有年龄下限": newFilters(map[string]any{
			"mode": "range", "minAge": 25,
		}),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateKindBody(KindCmd, commandFor(filters)); err != nil {
				t.Fatalf("合法筛选命令应通过:%v", err)
			}
		})
	}

	anyWithMin := newFilters(map[string]any{"mode": "any", "minAge": 25})
	assertValidationError(t, ValidateKindBody(KindCmd, commandFor(anyWithMin)), "$.args.filters.age.minAge", "forbiddenWhen")

	rangeWithoutMin := newFilters(map[string]any{"mode": "range", "maxAge": 45})
	assertValidationError(t, ValidateKindBody(KindCmd, commandFor(rangeWithoutMin)), "$.args.filters.age.minAge", "requiredWhen")

	reversedRange := newFilters(map[string]any{"mode": "range", "minAge": 45, "maxAge": 25})
	assertValidationError(t, ValidateKindBody(KindCmd, commandFor(reversedRange)), "$.args.filters.age.maxAge", "lessThanOrEqualWhen")

	duplicateCareers := newFilters(map[string]any{"mode": "any"})
	duplicateCareers["careerStatuses"] = []any{"employedLooking", "employedLooking"}
	assertValidationError(t, ValidateKindBody(KindCmd, commandFor(duplicateCareers)), "$.args.filters.careerStatuses[1]", "uniqueItems")

	duplicateEducations := newFilters(map[string]any{"mode": "any"})
	duplicateEducations["educations"] = []any{"bachelor", "bachelor"}
	assertValidationError(t, ValidateKindBody(KindCmd, commandFor(duplicateEducations)), "$.args.filters.educations[1]", "uniqueItems")

	unknownActiveWindow := newFilters(map[string]any{"mode": "any"})
	unknownActiveWindow["activeWindow"] = "days14"
	assertValidationError(t, ValidateKindBody(KindCmd, commandFor(unknownActiveWindow)), "$.args.filters.activeWindow", "enum")

	unknownGender := newFilters(map[string]any{"mode": "any"})
	unknownGender["gender"] = "unknown"
	assertValidationError(t, ValidateKindBody(KindCmd, commandFor(unknownGender)), "$.args.filters.gender", "enum")

	validData, err := json.Marshal(map[string]any{
		"positionRef":   "position-1",
		"positionTitle": "后端工程师",
		"filters":       newFilters(map[string]any{"mode": "range", "minAge": 25}),
		"observedAt":    int64(20),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePrimitiveData(PrimCandidateApplySourcingFilters, 1, validData); err != nil {
		t.Fatalf("合法筛选回读结果应通过:%v", err)
	}

	negativeObservedAt, err := json.Marshal(map[string]any{
		"positionRef":   "position-1",
		"positionTitle": "后端工程师",
		"filters":       newFilters(map[string]any{"mode": "any"}),
		"observedAt":    int64(-1),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertValidationError(t, ValidatePrimitiveData(PrimCandidateApplySourcingFilters, 1, negativeObservedAt), "$.observedAt", "minimum")
}

func TestSendSurfaceDiagnosticStagesMatchCurrentProducer(t *testing.T) {
	retained := []string{
		"page_absent",
		"route_missing",
		"composer_cardinality",
		"detail_cardinality",
		"button_cardinality",
		"dom_containment",
		"button_form_unsafe",
		"draft_present",
		"thread_unavailable",
		"diagnostic_unavailable",
		"ready",
	}
	if len(SendSurfaceDiagnosticStageValues) != len(retained) {
		t.Fatalf("诊断阶段枚举数量漂移:得到 %d,期望 %d", len(SendSurfaceDiagnosticStageValues), len(retained))
	}
	for i, stage := range retained {
		if string(SendSurfaceDiagnosticStageValues[i]) != stage {
			t.Fatalf("诊断阶段枚举[%d]漂移:得到 %q,期望 %q", i, SendSurfaceDiagnosticStageValues[i], stage)
		}
	}
	validate := func(stage string) error {
		data, err := Encode(DebugInspectSendSurfaceData{
			Ready: stage == "ready",
			Stage: SendSurfaceDiagnosticStage(stage),
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := Encode(ResultBody{
			Ref: "debug-surface-1", Status: ResultStatusOk, Data: data, Replayed: false, ExecMs: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		return ValidatePrimitiveResult(PrimDebugInspectSendSurface, 1, result)
	}
	for _, stage := range retained {
		if err := validate(stage); err != nil {
			t.Errorf("当前生产阶段 %q 应通过:%v", stage, err)
		}
	}
	assertValidationError(t, validate("component_tree_unavailable"), "$.data.stage", "enum")
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
