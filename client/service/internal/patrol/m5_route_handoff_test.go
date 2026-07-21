package patrol

import (
	"testing"

	"recruithelper/client/service/internal/store"
)

func TestM5TargetNeedsRouteHandoff(t *testing.T) {
	outbound := store.Message{Seq: 1, Direction: "out", Kind: "text"}
	inboundText := "候选人回复"
	inbound := store.Message{Seq: 2, Direction: "in", Kind: "text", Text: &inboundText}
	replied := store.Message{Seq: 3, Direction: "out", Kind: "text"}

	tests := []struct {
		name         string
		alreadyDirty bool
		captureState store.ResumeCaptureState
		ledger       []store.Message
		want         bool
	}{
		{name: "dirty target is ordered last", alreadyDirty: true, captureState: store.ResumeCaptureCaptured, want: true},
		{name: "unattempted capture needs target route", captureState: store.ResumeCaptureUnattempted, want: true},
		{name: "inflight capture needs target route", captureState: store.ResumeCaptureInFlight, want: true},
		{
			name: "captured pending inbound needs reply route", captureState: store.ResumeCaptureCaptured,
			ledger: []store.Message{outbound, inbound}, want: true,
		},
		{
			name: "captured idle trial does not steal page", captureState: store.ResumeCaptureCaptured,
			ledger: []store.Message{outbound}, want: false,
		},
		{
			name: "already replied inbound is not pending", captureState: store.ResumeCaptureCaptured,
			ledger: []store.Message{outbound, inbound, replied}, want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := m5TargetNeedsRouteHandoff(test.alreadyDirty, test.captureState, test.ledger); got != test.want {
				t.Fatalf("m5TargetNeedsRouteHandoff() = %v, want %v", got, test.want)
			}
		})
	}
}
