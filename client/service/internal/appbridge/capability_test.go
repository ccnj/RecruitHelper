package appbridge

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/patrol"
	"recruithelper/contract/gen/go/protocol"
)

// 脑闸按平台表拒绝(2026-09-02 甲方裁决)到 patrol 时必须是哨兵而不是 RunError:
// 既让调用方能 errors.Is,又不扰动 isAccountWideRunFailure 那些基于 RunError 的判定。
func TestPatrolRunnerStartTranslatesCapabilityGateToSentinel(t *testing.T) {
	runner, _, _ := newPatrolRunnerHarness(t)
	// completingSender 只声明 probe.platform@1,派 readWechatSetting 必被并集闸拒绝。
	_, err := runner.Start(context.Background(), patrol.RunRequest{
		HandID: "hand-1", ExpectedSession: "session-1", ExpectedBootID: "boot-1",
		Platform: "boss", AccountRef: "acct-1", ExpectedPrincipalFingerprint: "fp",
		Name: protocol.PrimAccountReadWechatSetting, Version: 1, Args: []byte(`{}`),
	})
	if !errors.Is(err, patrol.ErrHandCapabilityMissing) {
		t.Fatalf("应翻成 patrol.ErrHandCapabilityMissing: %v", err)
	}
	var typed *patrol.RunError
	if errors.As(err, &typed) {
		t.Fatalf("脑闸拒绝不得伪装成 RunError: %+v", typed)
	}
	if !errors.Is(err, dispatch.ErrCapability) {
		t.Fatalf("原因链必须保留脑闸原错误供留痕: %v", err)
	}
}

func TestCapabilityMissingRecognisesBothSignals(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"脑闸哨兵", fmt.Errorf("%w: x@1", dispatch.ErrCapability), true},
		{"手侧运行期拒绝", &patrol.RunError{Code: protocol.ErrCodeProtoUnsupportedCmd, Retryable: protocol.RetryableNo}, true},
		{"手侧其他失败", &patrol.RunError{Code: protocol.ErrCodeCtxNotReady, Retryable: protocol.RetryableAfterRecovery}, false},
		{"普通错误", errors.New("个人中心读不到"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := capabilityMissing(tc.err); got != tc.want {
			t.Fatalf("%s: capabilityMissing=%v 期望 %v", tc.name, got, tc.want)
		}
	}
}
