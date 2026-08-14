package appbridge

import (
	"testing"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// 契约表是 retryable 解析的唯一来源。这条测试是绊线：将来契约加新错误码或
// 换新声明格式，解析不动了会先在这里红，而不是静默返回空串让分类器兜底。
func TestParseRetryableDefaultCoversWholeContractTable(t *testing.T) {
	for code, meta := range protocol.ErrorCodes {
		base, _, ok := parseRetryableDefault(meta.RetryableDefault)
		if !ok || base == "" {
			t.Fatalf("契约错误码 %s 的 retryable 声明 %q 无法解析", code, meta.RetryableDefault)
		}
	}
}

// 钉住两个类键控码的解析方向：readonly/intrusive 取基础分支（瞬时，本轮跳过
// 下轮重试），effectful 取 (SX) 分支（副作用可能已发生，保留隔离）。
func TestSynthesizedRetryablePerClass(t *testing.T) {
	cases := []struct {
		code  protocol.ErrorCode
		class string
		want  protocol.Retryable
	}{
		{protocol.ErrCodeExecTimeoutHand, string(protocol.ClassIntrusive), protocol.RetryableYes},
		{protocol.ErrCodeExecTimeoutHand, string(protocol.ClassReadonly), protocol.RetryableYes},
		{protocol.ErrCodeExecTimeoutHand, string(protocol.ClassEffectful), protocol.RetryableManualOnly},
		{protocol.ErrCodeCtxLostDuringExec, string(protocol.ClassIntrusive), protocol.RetryableAfterRecovery},
		{protocol.ErrCodeCtxLostDuringExec, string(protocol.ClassEffectful), protocol.RetryableManualOnly},
		// witness 的限定分支依赖手侧数据，脑合成只取基础分支。
		{protocol.ErrCodeWitnessUnavailable, string(protocol.ClassEffectful), protocol.RetryableAfterRecovery},
		// 未知码解析不出来，返回空串交分类器默认方向。
		{protocol.ErrorCode("NO_SUCH_CODE"), string(protocol.ClassIntrusive), protocol.Retryable("")},
	}
	for _, testCase := range cases {
		got := synthesizedRetryable(testCase.code, testCase.class)
		if got != testCase.want {
			t.Fatalf("%s/%s: got %q want %q", testCase.code, testCase.class, got, testCase.want)
		}
	}
}

// 本次修复的根：脑合成的 RunError 必须带契约解析出的 retryable，不得留空。
// 2026-08-13 真机现场：chat.openConversation（intrusive）expired 后合成的
// RunError retryable 为空，被失败分流当成最重证词，候选人被永久隔离。
func TestRunErrorFromLeafFillsSynthesizedRetryable(t *testing.T) {
	expired := &protocol.ResultBody{Status: protocol.ResultStatusExpired}
	canceled := &protocol.ResultBody{Status: protocol.ResultStatusCanceled}

	cases := []struct {
		name          string
		leaf          store.CmdRecord
		result        *protocol.ResultBody
		wantCode      protocol.ErrorCode
		wantRetryable protocol.Retryable
	}{
		{
			"intrusive 超时是瞬时故障",
			store.CmdRecord{Class: string(protocol.ClassIntrusive), Status: store.CmdExpired},
			expired, protocol.ErrCodeExecTimeoutHand, protocol.RetryableYes,
		},
		{
			"effectful 超时保留人工方向",
			store.CmdRecord{Class: string(protocol.ClassEffectful), Status: store.CmdExpired},
			expired, protocol.ErrCodeExecTimeoutHand, protocol.RetryableManualOnly,
		},
		{
			"脑侧取消按表解析(人级语义由分类器特判)",
			store.CmdRecord{Class: string(protocol.ClassIntrusive), Status: store.CmdCanceled},
			canceled, protocol.ErrCodeCanceledByBrain, protocol.RetryableNo,
		},
		{
			"空 ResultBody 按上下文丢失解析",
			store.CmdRecord{Class: string(protocol.ClassIntrusive), Status: store.CmdExpired},
			nil, protocol.ErrCodeCtxLostDuringExec, protocol.RetryableAfterRecovery,
		},
	}
	for _, testCase := range cases {
		got := runErrorFromLeaf(testCase.leaf, testCase.result)
		if got.Code != testCase.wantCode || got.Retryable != testCase.wantRetryable {
			t.Fatalf("%s: got %s/%q want %s/%q",
				testCase.name, got.Code, got.Retryable, testCase.wantCode, testCase.wantRetryable)
		}
	}
}

// 手的显式证词原样保留，绝不被契约默认覆盖。
func TestRunErrorFromLeafKeepsExplicitHandTestimony(t *testing.T) {
	result := &protocol.ResultBody{
		Status: protocol.ResultStatusFailed,
		Error: &protocol.ErrorBody{
			Code:      protocol.ErrCodeExecTimeoutHand,
			Message:   "手自报超时",
			Retryable: protocol.RetryableManualOnly,
		},
	}
	got := runErrorFromLeaf(store.CmdRecord{Class: string(protocol.ClassIntrusive)}, result)
	if got.Retryable != protocol.RetryableManualOnly {
		t.Fatalf("显式 manualOnly 被覆盖成 %q", got.Retryable)
	}

	// 手回了 error 却漏填 retryable 的畸形结果，同样按契约默认补齐。
	malformed := &protocol.ResultBody{
		Status: protocol.ResultStatusFailed,
		Error: &protocol.ErrorBody{
			Code:    protocol.ErrCodeExecTimeoutHand,
			Message: "漏填 retryable",
		},
	}
	got = runErrorFromLeaf(store.CmdRecord{Class: string(protocol.ClassIntrusive)}, malformed)
	if got.Retryable != protocol.RetryableYes {
		t.Fatalf("畸形结果应按契约补齐为 yes，实际 %q", got.Retryable)
	}
}
