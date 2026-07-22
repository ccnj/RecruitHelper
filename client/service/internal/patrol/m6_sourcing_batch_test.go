package patrol

import (
	"errors"
	"testing"

	"recruithelper/contract/gen/go/protocol"
)

func TestSkipsUnreadableSourcingTargetRequiresExactMachineTuple(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "明确不可读且零副作用",
			err: &RunError{
				Code:      protocol.ErrCodeElementUnresolved,
				Retryable: protocol.RetryableManualOnly, SideEffect: protocol.SideEffectNone,
			},
			want: true,
		},
		{
			name: "同码但可恢复",
			err: &RunError{
				Code:      protocol.ErrCodeElementUnresolved,
				Retryable: protocol.RetryableAfterRecovery, SideEffect: protocol.SideEffectNone,
			},
		},
		{
			name: "同码但副作用可能",
			err: &RunError{
				Code:      protocol.ErrCodeElementUnresolved,
				Retryable: protocol.RetryableManualOnly, SideEffect: protocol.SideEffectPossible,
			},
		},
		{
			name: "其他错误码",
			err: &RunError{
				Code:      protocol.ErrCodeTargetNotFound,
				Retryable: protocol.RetryableManualOnly, SideEffect: protocol.SideEffectNone,
			},
		},
		{name: "普通错误", err: errors.New("fixture")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := skipsUnreadableSourcingTarget(tt.err); got != tt.want {
				t.Fatalf("跳过判定错误: got=%v want=%v err=%v", got, tt.want, tt.err)
			}
		})
	}
}
