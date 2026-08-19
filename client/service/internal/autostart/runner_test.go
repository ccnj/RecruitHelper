package autostart

import (
	"context"
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/productapp"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/workflow"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

type recordedAttempt struct {
	at      time.Time
	outcome string
	detail  string
}

type fakeStore struct {
	enabled    bool
	settingErr error
	active     *store.ProductWorkflowRun
	activeErr  error
	latest     *store.ProductWorkflowRun
	latestErr  error
	job        store.AppJobProjection
	jobErr     error
	attempts   []recordedAttempt
}

func (f *fakeStore) AutoStartSetting() (store.AutoStartSetting, error) {
	if f.settingErr != nil {
		return store.AutoStartSetting{}, f.settingErr
	}
	return store.AutoStartSetting{ID: 1, Enabled: f.enabled}, nil
}

func (f *fakeStore) RecordAutoStartAttempt(at time.Time, outcome, detail string) error {
	f.attempts = append(f.attempts, recordedAttempt{at: at, outcome: outcome, detail: detail})
	return nil
}

func (f *fakeStore) ActiveProductWorkflowRun() (*store.ProductWorkflowRun, error) {
	return f.active, f.activeErr
}

func (f *fakeStore) LatestProductWorkflowRun() (*store.ProductWorkflowRun, error) {
	return f.latest, f.latestErr
}

func (f *fakeStore) AppCurrentJob() (store.AppJobProjection, error) {
	if f.jobErr != nil {
		return store.AppJobProjection{}, f.jobErr
	}
	return f.job, nil
}

type fakeControl struct {
	startCalls  int
	startMode   string
	startJobID  string
	startErr    error
	resumeCalls int
	resumeErr   error
}

func (f *fakeControl) Start(_ context.Context, mode, backendJobID string) error {
	f.startCalls++
	f.startMode = mode
	f.startJobID = backendJobID
	return f.startErr
}

func (f *fakeControl) Resume(_ context.Context) error {
	f.resumeCalls++
	return f.resumeErr
}

func shanghai(t *testing.T) *time.Location {
	t.Helper()
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func newTestRunner(t *testing.T, st *fakeStore, control *fakeControl, clock *fakeClock) *Runner {
	t.Helper()
	return NewRunner(Config{
		Store: st, Control: control, Now: clock.Now,
		Location: shanghai(t), Tick: 30 * time.Second, Seed: 42,
	})
}

// tickThrough 以 30 秒步长把检查点从 from 驱动到 to(含端点后一拍)。
func tickThrough(runner *Runner, clock *fakeClock, from, to time.Time) {
	for clock.now = from; !clock.now.After(to); clock.now = clock.now.Add(30 * time.Second) {
		runner.TickOnce(context.Background())
	}
}

func day(t *testing.T, hour, minute int) time.Time {
	t.Helper()
	return time.Date(2026, 8, 20, hour, minute, 0, 0, shanghai(t))
}

func TestRunningAcrossSlotStartsFullWorkflowOnce(t *testing.T) {
	st := &fakeStore{
		enabled: true,
		job:     store.AppJobProjection{Available: true, BackendJobID: "42"},
	}
	control := &fakeControl{}
	clock := &fakeClock{}
	runner := newTestRunner(t, st, control, clock)

	tickThrough(runner, clock, day(t, 6, 50), day(t, 7, 35))

	if control.startCalls != 1 || control.startMode != "full" || control.startJobID != "42" {
		t.Fatalf("start calls = %d mode=%q job=%q", control.startCalls, control.startMode, control.startJobID)
	}
	if control.resumeCalls != 0 {
		t.Fatalf("resume must not be called: %d", control.resumeCalls)
	}
	if len(st.attempts) != 1 || st.attempts[0].outcome != store.AutoStartOutcomeStarted {
		t.Fatalf("attempts = %+v", st.attempts)
	}
	earliest := day(t, 7, 5)
	latest := day(t, 7, 30).Add(30 * time.Second)
	if st.attempts[0].at.Before(earliest) || st.attempts[0].at.After(latest) {
		t.Fatalf("fire time %v outside [07:05, 07:30]+tick", st.attempts[0].at)
	}
}

func TestSlotStaysWithinRangeAndRedrawsAcrossDays(t *testing.T) {
	st := &fakeStore{
		enabled: true,
		job:     store.AppJobProjection{Available: true, BackendJobID: "42"},
	}
	control := &fakeControl{}
	clock := &fakeClock{}
	runner := newTestRunner(t, st, control, clock)

	location := shanghai(t)
	base := day(t, 7, 0)
	slots := make([]time.Time, 0, 4)
	for i := 0; i < 4; i++ {
		dayStart := base.AddDate(0, 0, i)
		tickThrough(runner, clock, dayStart, dayStart.Add(35*time.Minute))
		slots = append(slots, runner.slot)
		lower := time.Date(dayStart.Year(), dayStart.Month(), dayStart.Day(), 7, 5, 0, 0, location)
		upper := time.Date(dayStart.Year(), dayStart.Month(), dayStart.Day(), 7, 30, 0, 0, location)
		if runner.slot.Before(lower) || runner.slot.After(upper) {
			t.Fatalf("day %d slot %v outside [07:05, 07:30]", i, runner.slot)
		}
	}
	if control.startCalls != 4 {
		t.Fatalf("start calls across 4 days = %d", control.startCalls)
	}
	sameClock := true
	for _, slot := range slots[1:] {
		if slot.Format("15:04:05") != slots[0].Format("15:04:05") {
			sameClock = false
		}
	}
	if sameClock {
		t.Fatalf("slot never redrawn across days: %v", slots)
	}
}

func TestLateBootSkipsWholeDay(t *testing.T) {
	st := &fakeStore{
		enabled: true,
		job:     store.AppJobProjection{Available: true, BackendJobID: "42"},
	}
	control := &fakeControl{}
	clock := &fakeClock{}
	runner := newTestRunner(t, st, control, clock)

	// 脑 07:35 才启动:所有可能的 slot 都已在身后,当日不得发起。
	tickThrough(runner, clock, day(t, 7, 35), day(t, 9, 0))

	if control.startCalls != 0 || control.resumeCalls != 0 || len(st.attempts) != 0 {
		t.Fatalf("late boot must skip: start=%d resume=%d attempts=%+v",
			control.startCalls, control.resumeCalls, st.attempts)
	}
}

func TestDisabledCrossingDoesNothing(t *testing.T) {
	st := &fakeStore{
		enabled: false,
		job:     store.AppJobProjection{Available: true, BackendJobID: "42"},
	}
	control := &fakeControl{}
	clock := &fakeClock{}
	runner := newTestRunner(t, st, control, clock)

	tickThrough(runner, clock, day(t, 6, 50), day(t, 7, 35))

	if control.startCalls != 0 || len(st.attempts) != 0 {
		t.Fatalf("disabled must be silent: start=%d attempts=%+v", control.startCalls, st.attempts)
	}
}

func TestActiveRunIsSkippedNotTouched(t *testing.T) {
	st := &fakeStore{
		enabled: true,
		active:  &store.ProductWorkflowRun{RunID: "wf-1", Status: workflow.StatusRunning},
		job:     store.AppJobProjection{Available: true, BackendJobID: "42"},
	}
	control := &fakeControl{}
	clock := &fakeClock{}
	runner := newTestRunner(t, st, control, clock)

	tickThrough(runner, clock, day(t, 6, 50), day(t, 7, 35))

	if control.startCalls != 0 || control.resumeCalls != 0 {
		t.Fatalf("active run must not be touched: start=%d resume=%d",
			control.startCalls, control.resumeCalls)
	}
	if len(st.attempts) != 1 || st.attempts[0].outcome != store.AutoStartOutcomeSkippedRun {
		t.Fatalf("attempts = %+v", st.attempts)
	}
}

func TestWaitingDailyWindowResumesInsteadOfStart(t *testing.T) {
	st := &fakeStore{
		enabled: true,
		active: &store.ProductWorkflowRun{
			RunID: "wf-1", Status: workflow.StatusWaitingDailyWindow,
			ResumeStatus: workflow.StatusRunning,
		},
		job: store.AppJobProjection{Available: true, BackendJobID: "42"},
	}
	control := &fakeControl{}
	clock := &fakeClock{}
	runner := newTestRunner(t, st, control, clock)

	tickThrough(runner, clock, day(t, 6, 50), day(t, 7, 35))

	if control.resumeCalls != 1 || control.startCalls != 0 {
		t.Fatalf("waitingDailyWindow must resume: start=%d resume=%d",
			control.startCalls, control.resumeCalls)
	}
	if len(st.attempts) != 1 || st.attempts[0].outcome != store.AutoStartOutcomeResumed {
		t.Fatalf("attempts = %+v", st.attempts)
	}
}

func TestRunStartedTodaySkips(t *testing.T) {
	st := &fakeStore{
		enabled: true,
		latest:  &store.ProductWorkflowRun{RunID: "wf-1", StartedAt: day(t, 7, 2)},
		job:     store.AppJobProjection{Available: true, BackendJobID: "42"},
	}
	control := &fakeControl{}
	clock := &fakeClock{}
	runner := newTestRunner(t, st, control, clock)

	tickThrough(runner, clock, day(t, 6, 50), day(t, 7, 35))

	if control.startCalls != 0 {
		t.Fatalf("already-ran day must skip start: %d", control.startCalls)
	}
	if len(st.attempts) != 1 || st.attempts[0].outcome != store.AutoStartOutcomeSkippedToday {
		t.Fatalf("attempts = %+v", st.attempts)
	}
}

func TestYesterdayTerminalRunDoesNotBlockToday(t *testing.T) {
	st := &fakeStore{
		enabled: true,
		latest: &store.ProductWorkflowRun{
			RunID: "wf-1", StartedAt: day(t, 7, 2).AddDate(0, 0, -1),
		},
		job: store.AppJobProjection{Available: true, BackendJobID: "42"},
	}
	control := &fakeControl{}
	clock := &fakeClock{}
	runner := newTestRunner(t, st, control, clock)

	tickThrough(runner, clock, day(t, 6, 50), day(t, 7, 35))

	if control.startCalls != 1 {
		t.Fatalf("yesterday's terminal run must not block today: %d", control.startCalls)
	}
}

func TestStartFailureRecordedOnceWithoutRetry(t *testing.T) {
	st := &fakeStore{
		enabled: true,
		job:     store.AppJobProjection{Available: true, BackendJobID: "42"},
	}
	control := &fakeControl{startErr: productapp.ErrHandUnavailable}
	clock := &fakeClock{}
	runner := newTestRunner(t, st, control, clock)

	// 失败后继续驱动到当天深夜:不得重试。
	tickThrough(runner, clock, day(t, 6, 50), day(t, 23, 50))

	if control.startCalls != 1 {
		t.Fatalf("failed attempt must not retry: %d", control.startCalls)
	}
	if len(st.attempts) != 1 || st.attempts[0].outcome != store.AutoStartOutcomeStartFailed {
		t.Fatalf("attempts = %+v", st.attempts)
	}
	if st.attempts[0].detail != productapp.StartFailureText(productapp.ErrHandUnavailable) {
		t.Fatalf("detail = %q", st.attempts[0].detail)
	}
}

func TestMissingCurrentJobFailsWithoutStart(t *testing.T) {
	st := &fakeStore{
		enabled: true,
		jobErr:  errors.New("投影不可用"),
	}
	control := &fakeControl{}
	clock := &fakeClock{}
	runner := newTestRunner(t, st, control, clock)

	tickThrough(runner, clock, day(t, 6, 50), day(t, 7, 35))

	if control.startCalls != 0 {
		t.Fatalf("missing job must not start: %d", control.startCalls)
	}
	if len(st.attempts) != 1 || st.attempts[0].outcome != store.AutoStartOutcomeStartFailed {
		t.Fatalf("attempts = %+v", st.attempts)
	}
}
