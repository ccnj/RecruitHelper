// Package workflow contains the smallest pure decision core shared by the
// product workflow controls and each per-candidate processing loop.
package workflow

import (
	"errors"
	"fmt"
	"time"
)

const DailyStartHour = 8

const DevelopmentAllowOutOfWindowEnv = "RECRUITHELPER_DEV_ALLOW_OUT_OF_WINDOW"

var (
	ErrInvalidMode       = errors.New("工作流模式无效")
	ErrInvalidStatus     = errors.New("工作流状态无效")
	ErrInvalidTime       = errors.New("工作流裁决时刻无效")
	ErrDailyWindowClosed = errors.New("本地业务运行窗口未开放")
	ErrModeConflict      = errors.New("已有未终局工作流使用了不同模式")
	ErrTerminalWorkflow  = errors.New("终局工作流不能继续控制")
)

type Mode string

const (
	ModeFull      Mode = "full"
	ModeReplyOnly Mode = "replyOnly"
)

type Status string

const (
	StatusRunning              Status = "running"
	StatusPaused               Status = "paused"
	StatusWaitingDailyWindow   Status = "waitingDailyWindow"
	StatusAwaitingConfirmation Status = "awaitingConfirmation"
	StatusCompleted            Status = "completed"
	StatusFailed               Status = "failed"
)

// State is deliberately limited to the fields needed for pure control
// decisions. Batch identity and business progress remain in their existing
// domain facts.
//
// ResumeStatus is populated only while paused or waiting for the next daily
// window. It preserves an awaiting-confirmation gate across pause and midnight.
type State struct {
	Mode         Mode   `json:"mode"`
	Status       Status `json:"status"`
	ResumeStatus Status `json:"resumeStatus,omitempty"`
}

type StartDecision struct {
	State   State
	Created bool
}

type TransitionDecision struct {
	State   State
	Changed bool
}

type MemberStartDecision struct {
	State   State
	Allowed bool
	Changed bool
}

// DailyWindowPolicy is the one civil-time policy shared by product controls,
// candidate-member gates, patrol enablement and the product UI projection.
// AllowOutOfWindow is a startup-only development override; it never changes
// the supplied clock or grants any non-time business authority.
type DailyWindowPolicy struct {
	AllowOutOfWindow bool
}

// ParseDevelopmentAllowOutOfWindow accepts only the explicitly documented
// value. Missing, empty and typoed values fail closed.
func ParseDevelopmentAllowOutOfWindow(value string) bool {
	return value == "1"
}

// EvaluateDailyWindow is the sole civil-time evaluator for workflow start,
// resume and per-member dispatch. The open interval is [08:00, 24:00) in the
// supplied client-local location.
func EvaluateDailyWindow(now time.Time, location *time.Location) (bool, error) {
	return (DailyWindowPolicy{}).Evaluate(now, location)
}

func (p DailyWindowPolicy) Evaluate(now time.Time, location *time.Location) (bool, error) {
	if now.IsZero() || location == nil {
		return false, ErrInvalidTime
	}
	if p.AllowOutOfWindow {
		return true, nil
	}
	return now.In(location).Hour() >= DailyStartHour, nil
}

// Start creates a running state when there is no active workflow. Repeating
// the same request against an existing non-terminal workflow is a no-op;
// changing its mode is rejected. Callers must pass nil after they have
// deliberately selected a new run following a terminal one.
func Start(
	current *State,
	requested Mode,
	now time.Time,
	location *time.Location,
	dailyWindow DailyWindowPolicy,
) (StartDecision, error) {
	if !validMode(requested) {
		return StartDecision{}, ErrInvalidMode
	}
	if current != nil {
		if err := validateState(*current); err != nil {
			return StartDecision{}, err
		}
		if terminal(current.Status) {
			return StartDecision{State: *current}, ErrTerminalWorkflow
		}
		if current.Mode != requested {
			return StartDecision{State: *current}, ErrModeConflict
		}
		return StartDecision{State: *current}, nil
	}
	open, err := dailyWindow.Evaluate(now, location)
	if err != nil {
		return StartDecision{}, err
	}
	if !open {
		return StartDecision{}, ErrDailyWindowClosed
	}
	return StartDecision{
		State:   State{Mode: requested, Status: StatusRunning},
		Created: true,
	}, nil
}

// Pause stops future work at the next member boundary. A repeated pause and a
// pause while already waiting for the daily window are idempotent.
func Pause(current State) (TransitionDecision, error) {
	if err := validateState(current); err != nil {
		return TransitionDecision{}, err
	}
	switch current.Status {
	case StatusRunning, StatusAwaitingConfirmation:
		next := current
		next.ResumeStatus = current.Status
		next.Status = StatusPaused
		return TransitionDecision{State: next, Changed: true}, nil
	case StatusPaused, StatusWaitingDailyWindow:
		return TransitionDecision{State: current}, nil
	case StatusCompleted, StatusFailed:
		return TransitionDecision{State: current}, ErrTerminalWorkflow
	default:
		return TransitionDecision{}, ErrInvalidStatus
	}
}

// Resume is always an explicit user action. It never turns a request rejected
// by the supplied policy into a reservation, and a waitingDailyWindow state
// stays blocked until this function is called under an open policy.
func Resume(
	current State,
	now time.Time,
	location *time.Location,
	dailyWindow DailyWindowPolicy,
) (TransitionDecision, error) {
	if err := validateState(current); err != nil {
		return TransitionDecision{}, err
	}
	open, err := dailyWindow.Evaluate(now, location)
	if err != nil {
		return TransitionDecision{}, err
	}

	switch current.Status {
	case StatusPaused, StatusWaitingDailyWindow:
		if !open {
			return TransitionDecision{State: current}, ErrDailyWindowClosed
		}
		next := current
		next.Status = current.ResumeStatus
		next.ResumeStatus = ""
		return TransitionDecision{State: next, Changed: true}, nil
	case StatusRunning, StatusAwaitingConfirmation:
		if open {
			return TransitionDecision{State: current}, nil
		}
		// The per-member gate owns the persisted midnight transition. Keeping
		// errors side-effect free prevents HTTP callers from having to commit a
		// returned state after a rejected control request.
		return TransitionDecision{State: current}, ErrDailyWindowClosed
	case StatusCompleted, StatusFailed:
		return TransitionDecision{State: current}, ErrTerminalWorkflow
	default:
		return TransitionDecision{}, ErrInvalidStatus
	}
}

// MayStartNextWorkflowMember is the literal shared gate that scoring,
// greeting generation, greeting sending and communication loops must call
// before starting their next candidate. It does not interrupt work that has
// already begun. Under the formal policy, when an active state crosses
// midnight the returned state is waitingDailyWindow and must be persisted
// before the loop stops.
func MayStartNextWorkflowMember(
	current State,
	now time.Time,
	location *time.Location,
	dailyWindow DailyWindowPolicy,
) (MemberStartDecision, error) {
	if err := validateState(current); err != nil {
		return MemberStartDecision{}, err
	}
	open, err := dailyWindow.Evaluate(now, location)
	if err != nil {
		return MemberStartDecision{}, err
	}

	switch current.Status {
	case StatusRunning:
		if open {
			return MemberStartDecision{State: current, Allowed: true}, nil
		}
		next := enterWaitingDailyWindow(current)
		return MemberStartDecision{State: next, Changed: true}, nil
	case StatusAwaitingConfirmation:
		if open {
			return MemberStartDecision{State: current}, nil
		}
		next := enterWaitingDailyWindow(current)
		return MemberStartDecision{State: next, Changed: true}, nil
	case StatusPaused, StatusWaitingDailyWindow, StatusCompleted, StatusFailed:
		return MemberStartDecision{State: current}, nil
	default:
		return MemberStartDecision{}, ErrInvalidStatus
	}
}

func enterWaitingDailyWindow(current State) State {
	next := current
	next.ResumeStatus = current.Status
	next.Status = StatusWaitingDailyWindow
	return next
}

func validateState(state State) error {
	if !validMode(state.Mode) {
		return ErrInvalidMode
	}
	switch state.Status {
	case StatusRunning, StatusAwaitingConfirmation, StatusCompleted, StatusFailed:
		if state.ResumeStatus != "" {
			return fmt.Errorf("%w: %s 不得携带 resumeStatus", ErrInvalidStatus, state.Status)
		}
	case StatusPaused, StatusWaitingDailyWindow:
		if !resumableStatus(state.ResumeStatus) {
			return fmt.Errorf("%w: %s 缺少可恢复目标", ErrInvalidStatus, state.Status)
		}
	default:
		return fmt.Errorf("%w: %q", ErrInvalidStatus, state.Status)
	}
	return nil
}

func validMode(mode Mode) bool {
	return mode == ModeFull || mode == ModeReplyOnly
}

func resumableStatus(status Status) bool {
	return status == StatusRunning || status == StatusAwaitingConfirmation
}

func terminal(status Status) bool {
	return status == StatusCompleted || status == StatusFailed
}
