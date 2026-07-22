package patrol

import (
	"context"
	"errors"
	"testing"
	"time"

	"recruithelper/contract/gen/go/protocol"
)

func TestRandomSourcingPaceDelayStaysWithinHumanizedBounds(t *testing.T) {
	for range 1_000 {
		delay := randomSourcingPaceDelay()
		if delay < sourcingPaceMin || delay > sourcingPaceMax {
			t.Fatalf("采集节奏越界: delay=%s want=[%s,%s]", delay, sourcingPaceMin, sourcingPaceMax)
		}
	}
}

func TestDefaultSourcingPaceWaitHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if err := defaultSourcingPaceWait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消未中断随机等待: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("取消后仍阻塞: %s", elapsed)
	}
}

func TestDefaultInteractionPaceWaitHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if err := defaultInteractionPaceWait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消未中断平台交互等待: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("取消后仍阻塞: %s", elapsed)
	}
}

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
