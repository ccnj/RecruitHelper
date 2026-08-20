package workflow

import (
	"errors"
	"testing"
	"time"
)

func TestEvaluateDailyWindowUsesSuppliedLocalTimeAndExactBoundaries(t *testing.T) {
	shanghai := mustLocation(t, "Asia/Shanghai")
	cases := []struct {
		name string
		now  time.Time
		open bool
	}{
		{
			name: "one second before opening",
			now:  time.Date(2026, 7, 25, 6, 59, 59, 0, shanghai),
		},
		{
			name: "exactly 07:00",
			now:  time.Date(2026, 7, 25, 7, 0, 0, 0, shanghai),
			open: true,
		},
		{
			name: "last instant before midnight",
			now:  time.Date(2026, 7, 25, 23, 59, 59, int(time.Second-time.Nanosecond), shanghai),
			open: true,
		},
		{
			name: "exactly next midnight",
			now:  time.Date(2026, 7, 26, 0, 0, 0, 0, shanghai),
		},
		{
			name: "UTC instant is converted to client location",
			now:  time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC),
			open: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			open, err := EvaluateDailyWindow(tc.now, shanghai)
			if err != nil || open != tc.open {
				t.Fatalf("EvaluateDailyWindow() = (%v, %v), want (%v, nil)", open, err, tc.open)
			}
		})
	}
	if _, err := EvaluateDailyWindow(time.Time{}, shanghai); !errors.Is(err, ErrInvalidTime) {
		t.Fatalf("zero time error = %v, want ErrInvalidTime", err)
	}
	if _, err := EvaluateDailyWindow(time.Now(), nil); !errors.Is(err, ErrInvalidTime) {
		t.Fatalf("nil location error = %v, want ErrInvalidTime", err)
	}
}

func TestDailyWindowPolicyDevelopmentOverrideIsExplicitAndKeepsTimeValidation(t *testing.T) {
	location := mustLocation(t, "Asia/Shanghai")
	closed := time.Date(2026, 7, 25, 1, 30, 0, 0, location)
	policy := DailyWindowPolicy{AllowOutOfWindow: true}
	if open, err := policy.Evaluate(closed, location); err != nil || !open {
		t.Fatalf("development override = (%v, %v), want (true, nil)", open, err)
	}
	started, err := Start(nil, ModeReplyOnly, closed, location, policy)
	if err != nil || !started.Created || started.State.Status != StatusRunning {
		t.Fatalf("override Start() = %+v, %v", started, err)
	}
	member, err := MayStartNextWorkflowMember(started.State, closed, location, policy)
	if err != nil || !member.Allowed || member.Changed {
		t.Fatalf("override member gate = %+v, %v", member, err)
	}
	if _, err := policy.Evaluate(time.Time{}, location); !errors.Is(err, ErrInvalidTime) {
		t.Fatalf("override zero time error = %v, want ErrInvalidTime", err)
	}
	if _, err := policy.Evaluate(closed, nil); !errors.Is(err, ErrInvalidTime) {
		t.Fatalf("override nil location error = %v, want ErrInvalidTime", err)
	}
	for value, want := range map[string]bool{
		"": false, "0": false, "true": false, "01": false, "1": true,
	} {
		if got := ParseDevelopmentAllowOutOfWindow(value); got != want {
			t.Fatalf("ParseDevelopmentAllowOutOfWindow(%q)=%v want %v", value, got, want)
		}
	}
}

func TestStartCreatesOnceAndDoesNotChangeAnActiveWorkflow(t *testing.T) {
	location := mustLocation(t, "Asia/Shanghai")
	open := time.Date(2026, 7, 25, 8, 0, 0, 0, location)

	for _, mode := range []Mode{ModeFull, ModeReplyOnly} {
		created, err := Start(nil, mode, open, location, DailyWindowPolicy{})
		if err != nil || !created.Created ||
			created.State != (State{Mode: mode, Status: StatusRunning}) {
			t.Fatalf("Start(nil, %s) = %+v, %v", mode, created, err)
		}
		replayed, err := Start(
			&created.State, mode, open.Add(time.Minute), location, DailyWindowPolicy{},
		)
		if err != nil || replayed.Created || replayed.State != created.State {
			t.Fatalf("replayed Start(%s) = %+v, %v", mode, replayed, err)
		}
	}

	existing := State{
		Mode: ModeFull, Status: StatusPaused, ResumeStatus: StatusRunning,
	}
	replayed, err := Start(&existing, ModeFull, open, location, DailyWindowPolicy{})
	if err != nil || replayed.Created || replayed.State != existing {
		t.Fatalf("paused replay = %+v, %v", replayed, err)
	}
	if decision, err := Start(
		&existing, ModeReplyOnly, open, location, DailyWindowPolicy{},
	); !errors.Is(err, ErrModeConflict) ||
		decision.State != existing {
		t.Fatalf("mode conflict = %+v, %v", decision, err)
	}
	terminalState := State{Mode: ModeFull, Status: StatusCompleted}
	if decision, err := Start(
		&terminalState, ModeFull, open, location, DailyWindowPolicy{},
	); !errors.Is(err, ErrTerminalWorkflow) ||
		decision.State != terminalState {
		t.Fatalf("terminal start = %+v, %v", decision, err)
	}
}

func TestStartRejectsClosedWindowWithoutCreatingReservation(t *testing.T) {
	location := mustLocation(t, "Asia/Shanghai")
	closed := time.Date(2026, 7, 25, 6, 59, 59, 0, location)
	decision, err := Start(nil, ModeFull, closed, location, DailyWindowPolicy{})
	if !errors.Is(err, ErrDailyWindowClosed) || decision.Created || decision.State != (State{}) {
		t.Fatalf("closed Start = %+v, %v", decision, err)
	}
}

func TestPauseIsIdempotentAndPreservesResumeTarget(t *testing.T) {
	cases := []struct {
		name    string
		current State
		want    State
		changed bool
	}{
		{
			name:    "running",
			current: State{Mode: ModeFull, Status: StatusRunning},
			want:    State{Mode: ModeFull, Status: StatusPaused, ResumeStatus: StatusRunning},
			changed: true,
		},
		{
			name:    "awaiting confirmation",
			current: State{Mode: ModeFull, Status: StatusAwaitingConfirmation},
			want: State{
				Mode: ModeFull, Status: StatusPaused,
				ResumeStatus: StatusAwaitingConfirmation,
			},
			changed: true,
		},
		{
			name:    "already paused",
			current: State{Mode: ModeReplyOnly, Status: StatusPaused, ResumeStatus: StatusRunning},
			want:    State{Mode: ModeReplyOnly, Status: StatusPaused, ResumeStatus: StatusRunning},
		},
		{
			name: "already waiting for the next daily window",
			current: State{
				Mode: ModeFull, Status: StatusWaitingDailyWindow,
				ResumeStatus: StatusAwaitingConfirmation,
			},
			want: State{
				Mode: ModeFull, Status: StatusWaitingDailyWindow,
				ResumeStatus: StatusAwaitingConfirmation,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := Pause(tc.current)
			if err != nil || decision.State != tc.want || decision.Changed != tc.changed {
				t.Fatalf("Pause() = %+v, %v, want state=%+v changed=%v", decision, err, tc.want, tc.changed)
			}
		})
	}
}

func TestResumeRequiresOpenWindowAndRestoresExactStatus(t *testing.T) {
	location := mustLocation(t, "Asia/Shanghai")
	open := time.Date(2026, 7, 25, 7, 0, 0, 0, location)
	closed := open.Add(-time.Nanosecond)

	cases := []struct {
		name    string
		current State
		want    State
	}{
		{
			name:    "paused running",
			current: State{Mode: ModeFull, Status: StatusPaused, ResumeStatus: StatusRunning},
			want:    State{Mode: ModeFull, Status: StatusRunning},
		},
		{
			name: "paused confirmation",
			current: State{
				Mode: ModeFull, Status: StatusPaused,
				ResumeStatus: StatusAwaitingConfirmation,
			},
			want: State{Mode: ModeFull, Status: StatusAwaitingConfirmation},
		},
		{
			name: "daily-window confirmation",
			current: State{
				Mode: ModeFull, Status: StatusWaitingDailyWindow,
				ResumeStatus: StatusAwaitingConfirmation,
			},
			want: State{Mode: ModeFull, Status: StatusAwaitingConfirmation},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := Resume(tc.current, open, location, DailyWindowPolicy{})
			if err != nil || !decision.Changed || decision.State != tc.want {
				t.Fatalf("Resume() = %+v, %v, want %+v", decision, err, tc.want)
			}
		})
	}

	paused := State{Mode: ModeFull, Status: StatusPaused, ResumeStatus: StatusRunning}
	decision, err := Resume(paused, closed, location, DailyWindowPolicy{})
	if !errors.Is(err, ErrDailyWindowClosed) || decision.Changed || decision.State != paused {
		t.Fatalf("closed Resume() = %+v, %v", decision, err)
	}
}

func TestResumeReplayCannotAuthorizeWorkAfterMidnight(t *testing.T) {
	location := mustLocation(t, "Asia/Shanghai")
	closed := time.Date(2026, 7, 26, 0, 0, 0, 0, location)
	current := State{Mode: ModeReplyOnly, Status: StatusRunning}

	decision, err := Resume(current, closed, location, DailyWindowPolicy{})
	if !errors.Is(err, ErrDailyWindowClosed) || decision.Changed || decision.State != current {
		t.Fatalf("midnight Resume replay = %+v, %v", decision, err)
	}

	waiting := State{
		Mode: ModeReplyOnly, Status: StatusWaitingDailyWindow,
		ResumeStatus: StatusRunning,
	}
	open := time.Date(2026, 7, 26, 8, 0, 0, 0, location)
	repeated, err := Resume(waiting, open, location, DailyWindowPolicy{})
	if err != nil || !repeated.Changed ||
		repeated.State != (State{Mode: ModeReplyOnly, Status: StatusRunning}) {
		t.Fatalf("explicit next-day Resume = %+v, %v", repeated, err)
	}
}

func TestMayStartNextWorkflowMemberIsTheSharedMemberBoundaryGate(t *testing.T) {
	location := mustLocation(t, "Asia/Shanghai")
	open := time.Date(2026, 7, 25, 12, 0, 0, 0, location)
	closed := time.Date(2026, 7, 26, 0, 0, 0, 0, location)

	running := State{Mode: ModeFull, Status: StatusRunning}
	decision, err := MayStartNextWorkflowMember(
		running, open, location, DailyWindowPolicy{},
	)
	if err != nil || !decision.Allowed || decision.Changed || decision.State != running {
		t.Fatalf("open running gate = %+v, %v", decision, err)
	}

	decision, err = MayStartNextWorkflowMember(
		running, closed, location, DailyWindowPolicy{},
	)
	wantWaiting := State{
		Mode: ModeFull, Status: StatusWaitingDailyWindow,
		ResumeStatus: StatusRunning,
	}
	if err != nil || decision.Allowed || !decision.Changed || decision.State != wantWaiting {
		t.Fatalf("closed running gate = %+v, %v, want %+v", decision, err, wantWaiting)
	}

	// Reaching 07:00 is not authority to resume.
	decision, err = MayStartNextWorkflowMember(
		wantWaiting, open, location, DailyWindowPolicy{},
	)
	if err != nil || decision.Allowed || decision.Changed || decision.State != wantWaiting {
		t.Fatalf("waiting gate auto-resumed = %+v, %v", decision, err)
	}
}

func TestMayStartNextWorkflowMemberPreservesConfirmationAcrossMidnight(t *testing.T) {
	location := mustLocation(t, "Asia/Shanghai")
	closed := time.Date(2026, 7, 26, 0, 0, 0, 0, location)
	awaiting := State{Mode: ModeFull, Status: StatusAwaitingConfirmation}

	decision, err := MayStartNextWorkflowMember(
		awaiting, closed, location, DailyWindowPolicy{},
	)
	want := State{
		Mode: ModeFull, Status: StatusWaitingDailyWindow,
		ResumeStatus: StatusAwaitingConfirmation,
	}
	if err != nil || decision.Allowed || !decision.Changed || decision.State != want {
		t.Fatalf("confirmation midnight gate = %+v, %v, want %+v", decision, err, want)
	}
}

func TestBlockedAndTerminalStatesNeverStartAnotherMember(t *testing.T) {
	location := mustLocation(t, "Asia/Shanghai")
	open := time.Date(2026, 7, 25, 12, 0, 0, 0, location)
	states := []State{
		{Mode: ModeFull, Status: StatusPaused, ResumeStatus: StatusRunning},
		{Mode: ModeFull, Status: StatusWaitingDailyWindow, ResumeStatus: StatusRunning},
		{Mode: ModeFull, Status: StatusAwaitingConfirmation},
		{Mode: ModeFull, Status: StatusCompleted},
		{Mode: ModeFull, Status: StatusFailed},
	}
	for _, state := range states {
		decision, err := MayStartNextWorkflowMember(
			state, open, location, DailyWindowPolicy{},
		)
		if err != nil || decision.Allowed || decision.Changed || decision.State != state {
			t.Fatalf("gate(%s) = %+v, %v", state.Status, decision, err)
		}
	}
}

func TestInvalidStateCannotBeUsedAsImplicitResumeAuthority(t *testing.T) {
	location := mustLocation(t, "Asia/Shanghai")
	open := time.Date(2026, 7, 25, 12, 0, 0, 0, location)
	invalid := State{Mode: ModeFull, Status: StatusPaused}

	if _, err := Pause(invalid); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("Pause invalid state error = %v", err)
	}
	if _, err := Resume(
		invalid, open, location, DailyWindowPolicy{},
	); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("Resume invalid state error = %v", err)
	}
	if _, err := MayStartNextWorkflowMember(
		invalid, open, location, DailyWindowPolicy{},
	); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("MayStart invalid state error = %v", err)
	}
}

func TestTerminalControlRequestsAreRejected(t *testing.T) {
	location := mustLocation(t, "Asia/Shanghai")
	open := time.Date(2026, 7, 25, 12, 0, 0, 0, location)
	for _, status := range []Status{StatusCompleted, StatusFailed} {
		state := State{Mode: ModeFull, Status: status}
		if _, err := Pause(state); !errors.Is(err, ErrTerminalWorkflow) {
			t.Fatalf("Pause(%s) error = %v", status, err)
		}
		if _, err := Resume(
			state, open, location, DailyWindowPolicy{},
		); !errors.Is(err, ErrTerminalWorkflow) {
			t.Fatalf("Resume(%s) error = %v", status, err)
		}
	}
}

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return location
}
