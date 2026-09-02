package store

import (
	"strings"
	"testing"
	"time"
)

// 批次拦停/终局原因收窄前的判定现场随行落库(「错误收敛必须留痕」,2026-09-02):
// 单行、截断、只留痕不做判据。
func TestSourcingBatchReasonDetailPersistsOnBlockAndStop(t *testing.T) {
	s := openTest(t)
	at := time.Date(2026, 9, 2, 10, 0, 0, 0, time.FixedZone("CST", 8*3600))
	key, revisionHash := seedSourcingBatchDependencies(t, s, "detail")
	started, err := s.StartSourcingBatch(StartSourcingBatchRequest{
		Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revisionHash, TargetCount: 1, StartedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	long := "CTX_NOT_READY/pageBroken: 智联推荐页在期限内未就绪\n第二行" + strings.Repeat("x", 500)
	blocked, err := s.BlockSourcingBatch(BlockSourcingBatchRequest{
		BatchID: started.Batch.BatchID, Reason: SourcingBatchGateReasonRecommendPageNotReady,
		Detail: long, BlockedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(blocked.ReasonDetail, "CTX_NOT_READY/pageBroken: 智联推荐页在期限内未就绪 | 第二行") ||
		strings.Contains(blocked.ReasonDetail, "\n") || len([]rune(blocked.ReasonDetail)) != 400 {
		t.Fatalf("blocked 留痕应单行截断到 400 字符: %q", blocked.ReasonDetail)
	}

	key2, revision2 := seedSourcingBatchDependencies(t, s, "detail-stop")
	second, err := s.StartSourcingBatch(StartSourcingBatchRequest{
		Platform: key2.Platform, AccountRef: key2.AccountRef,
		ContextRevisionHash: revision2, TargetCount: 1, StartedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := s.StopSourcingBatch(StopSourcingBatchRequest{
		BatchID: second.Batch.BatchID, Reason: SourcingBatchGateReasonJobNotOnline,
		Detail: "职位「甲」当前平台状态为「未上线」,未在线,不开始采集", StoppedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stopped.ReasonDetail != "职位「甲」当前平台状态为「未上线」,未在线,不开始采集" {
		t.Fatalf("stopped 留痕不符: %q", stopped.ReasonDetail)
	}
}
