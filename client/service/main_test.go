package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackgroundGroupJoinsCanceledLoops(t *testing.T) {
	appCtx, appCancel := context.WithCancel(context.Background())
	var group backgroundGroup
	var stopped atomic.Int32
	for range 4 {
		group.Go(func() {
			<-appCtx.Done()
			stopped.Add(1)
		})
	}

	appCancel()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	if err := group.Wait(waitCtx); err != nil {
		t.Fatalf("后台循环未在取消后收束: %v", err)
	}
	if got := stopped.Load(); got != 4 {
		t.Fatalf("已收束循环数=%d, want 4", got)
	}
}

func TestBackgroundGroupWaitIsBounded(t *testing.T) {
	release := make(chan struct{})
	var group backgroundGroup
	group.Go(func() { <-release })

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := group.Wait(waitCtx)
	waitCancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("未结束循环必须受 deadline 约束，得到 %v", err)
	}

	close(release)
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
	defer cleanupCancel()
	if err := group.Wait(cleanupCtx); err != nil {
		t.Fatalf("释放后后台循环仍未收束: %v", err)
	}
}
