package appbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/patrol"
)

func TestScoringCallsSharedWorkflowGateBeforeEachNewCandidate(t *testing.T) {
	h := newSourcingActorHarness(t, [][]string{{"candidate-a", "candidate-b"}})
	if err := h.manager.StartSourcing(h.key, h.revision.RevisionHash, 2, 0); err != nil {
		t.Fatal(err)
	}
	batch, err := h.store.ActiveSourcingBatch(h.key)
	if err != nil || batch == nil {
		t.Fatalf("missing sourcing batch: %+v, %v", batch, err)
	}
	if _, err := h.manager.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}

	advice := &sourcingBatchScoringAdvice{}
	scorer, err := patrol.NewManager(
		h.store, PatrolRunner{Dispatcher: h.sender.dispatcher}, sourcingActorHands{},
		patrol.Config{Clock: h.clock, Location: time.UTC}, advice,
	)
	if err != nil {
		t.Fatal(err)
	}
	blocked := errors.New("fixture workflow paused")
	gateCalls := 0
	scorer.SetWorkflowMemberGate(func() error {
		gateCalls++
		if gateCalls == 2 {
			return blocked
		}
		return nil
	})

	progress, err := scorer.ScoreCompletedSourcingBatch(context.Background(), batch.BatchID)
	if !errors.Is(err, blocked) || progress == nil || progress.OKCount != 1 ||
		progress.PendingCount != 1 || advice.requestCount() != 1 || gateCalls != 2 {
		t.Fatalf(
			"member gate did not stop exactly before candidate 2: progress=%+v calls=%d advice=%d err=%v",
			progress, gateCalls, advice.requestCount(), err,
		)
	}
}
