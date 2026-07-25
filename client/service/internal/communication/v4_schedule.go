package communication

import (
	"fmt"
	"strings"
	"time"

	"recruithelper/client/service/internal/m5ai"
)

const (
	v4FallbackDelay      = 7 * 24 * time.Hour
	v4ColdDelay          = 24 * time.Hour
	v4ArchiveDelay       = 36 * time.Hour
	v4FollowupOneDelay   = 10 * time.Minute
	v4FollowupTwoDelay   = time.Hour
	v4FollowupThreeDelay = 4 * time.Hour
)

const V4AdviceSilenceFollowup V4AdvicePurpose = "silenceFollowup"

const (
	V4ManualScheduleClockUnknown      V4ManualReason = "scheduleClockUnknown"
	V4ManualFollowupPhraseUnavailable V4ManualReason = "followupPhraseUnavailable"
)

type V4ScheduleDecisionStatus string

const (
	V4ScheduleNoAction       V4ScheduleDecisionStatus = "noAction"
	V4ScheduleWaitingAdvice  V4ScheduleDecisionStatus = "waitingAdvice"
	V4ScheduleActionsPlanned V4ScheduleDecisionStatus = "actionsPlanned"
	V4ScheduleManualRequired V4ScheduleDecisionStatus = "manualRequired"
)

type V4ScheduleInput struct {
	ProfileKey             string
	State                  V4State
	ProjectedThroughSeq    int64
	Now                    time.Time
	HasPendingDialogue     bool
	Reply                  ReplyAdvice
	FixedPhrases           V4FixedPhraseView
	InterviewFollowupTexts map[uint8]string
}

type V4ScheduleDecision struct {
	State        V4State
	Status       V4ScheduleDecisionStatus
	NextAdvice   V4AdvicePurpose
	AdviceKey    string
	Actions      []V4PlannedAction
	ManualReason V4ManualReason
}

// EvaluateV4Schedule applies the v4 priority order: seven-day fallback first,
// then unresolved dialogue suppression, card followups, cold messages and
// finally 36-hour archive. It returns at most the one current tier; planning
// never spends a budget or slides an anchor.
func EvaluateV4Schedule(input V4ScheduleInput) (V4ScheduleDecision, error) {
	if err := validateV4State(input.State); err != nil || strings.TrimSpace(input.ProfileKey) == "" ||
		input.ProjectedThroughSeq < input.State.LastOutboundMessageSeq ||
		input.Now.IsZero() || !validAdviceState(input.Reply.State) {
		return V4ScheduleDecision{}, ErrInvalidV4StateTransition
	}
	state := cloneV4State(input.State)
	if !isV4ProgressStatus(state.MainStatus) {
		if input.Reply.State != AdviceAbsent {
			return V4ScheduleDecision{}, ErrInvalidV4StateTransition
		}
		return noV4ScheduleAction(state), nil
	}

	if state.BodyClockUncertain || state.LastBodyAt == nil || input.Now.Before(*state.LastBodyAt) {
		return manualV4Schedule(state, V4ManualScheduleClockUnknown), nil
	}
	if input.Now.Sub(*state.LastBodyAt) >= v4FallbackDelay {
		if input.Reply.State != AdviceAbsent {
			return V4ScheduleDecision{}, ErrInvalidV4StateTransition
		}
		return plannedV4Archive(
			input.ProfileKey,
			state,
			input.ProjectedThroughSeq,
			state.LastBodyAt.Add(v4FallbackDelay),
			V4EndFallback,
		), nil
	}
	if input.HasPendingDialogue {
		if input.Reply.State != AdviceAbsent {
			return V4ScheduleDecision{}, ErrInvalidV4StateTransition
		}
		return noV4ScheduleAction(state), nil
	}
	if state.ClockUncertain || state.LastOutboundAt == nil || input.Now.Before(*state.LastOutboundAt) {
		return manualV4Schedule(state, V4ManualScheduleClockUnknown), nil
	}
	elapsed := input.Now.Sub(*state.LastOutboundAt)

	if state.MainStatus == V4StatusInvited {
		if group, exists := nextV4InterviewFollowup(state.InterviewGroups); exists {
			delay, ok := v4FollowupDelay(group.NextStage)
			if !ok {
				return V4ScheduleDecision{}, ErrInvalidV4StateTransition
			}
			if elapsed >= delay {
				if input.Reply.State != AdviceAbsent {
					return V4ScheduleDecision{}, ErrInvalidV4StateTransition
				}
				text := strings.TrimSpace(input.InterviewFollowupTexts[group.NextStage])
				if text == "" || m5ai.ValidateSendText(text) != nil {
					return manualV4Schedule(state, V4ManualFollowupPhraseUnavailable), nil
				}
				dueAt := state.LastOutboundAt.Add(delay)
				return V4ScheduleDecision{
					State: state, Status: V4ScheduleActionsPlanned, NextAdvice: V4AdviceNone,
					Actions: []V4PlannedAction{{
						ActionKey: stableV4ScheduleKey(input.ProfileKey, V4ActionInterviewFollowup, group.MessageSeq, 0, group.NextStage),
						Kind:      V4ActionInterviewFollowup, Text: text, CardMessageSeq: group.MessageSeq, Stage: group.NextStage,
						DueAt: &dueAt,
					}},
				}, nil
			}
		}
	}

	if elapsed >= v4ColdDelay {
		if v4ColdPromptAvailable(state) {
			return evaluateV4ColdPrompt(input, state)
		}
		if v4ColdWechatAvailable(state) {
			if input.Reply.State != AdviceAbsent {
				return V4ScheduleDecision{}, ErrInvalidV4StateTransition
			}
			return evaluateV4ColdWechat(input, state), nil
		}
	}
	if input.Reply.State != AdviceAbsent {
		return V4ScheduleDecision{}, ErrInvalidV4StateTransition
	}
	if elapsed >= v4ArchiveDelay && !v4ColdPromptAvailable(state) && !v4ColdWechatAvailable(state) {
		return plannedV4Archive(
			input.ProfileKey,
			state,
			input.ProjectedThroughSeq,
			state.LastOutboundAt.Add(v4ArchiveDelay),
			v4SilenceEndReason(state),
		), nil
	}
	return noV4ScheduleAction(state), nil
}

func evaluateV4ColdPrompt(input V4ScheduleInput, state V4State) (V4ScheduleDecision, error) {
	switch input.Reply.State {
	case AdviceAbsent:
		return V4ScheduleDecision{
			State: state, Status: V4ScheduleWaitingAdvice, NextAdvice: V4AdviceSilenceFollowup,
			AdviceKey: stableV4ScheduleAdviceKey(
				input.ProfileKey,
				V4AdviceSilenceFollowup,
				state.RealMessageRound,
				state.ColdPromptSentCount+1,
			),
		}, nil
	case AdviceFailed:
		return manualV4Schedule(state, V4ManualReplyFailed), nil
	case AdviceOK:
		if err := m5ai.ValidateSendText(input.Reply.Suggestion.Text); err != nil {
			return manualV4Schedule(state, V4ManualReplyInvalid), nil
		}
		stage := state.ColdPromptSentCount + 1
		dueAt := state.LastOutboundAt.Add(v4ColdDelay)
		return V4ScheduleDecision{
			State: state, Status: V4ScheduleActionsPlanned, NextAdvice: V4AdviceNone,
			Actions: []V4PlannedAction{{
				ActionKey: stableV4ScheduleKey(input.ProfileKey, V4ActionColdPrompt, 0, state.RealMessageRound, stage),
				Kind:      V4ActionColdPrompt, Text: input.Reply.Suggestion.Text,
				Round: state.RealMessageRound, Stage: stage, DueAt: &dueAt,
			}},
		}, nil
	default:
		return V4ScheduleDecision{}, ErrInvalidV4StateTransition
	}
}

func evaluateV4ColdWechat(input V4ScheduleInput, state V4State) V4ScheduleDecision {
	phrase := input.FixedPhrases.Phrase(V4PhraseColdWechat)
	if !state.ColdWechatTextSent && (phrase.State != V4PhraseAvailable || m5ai.ValidateSendText(phrase.Text) != nil) {
		return manualV4Schedule(state, V4ManualFixedPhraseUnavailable)
	}
	dueAt := state.LastOutboundAt.Add(v4ColdDelay)
	actions := make([]V4PlannedAction, 0, 2)
	if !state.ColdWechatTextSent {
		actions = append(actions, V4PlannedAction{
			ActionKey: stableV4ScheduleKey(input.ProfileKey, V4ActionColdWechatText, 0, 0, 0),
			Kind:      V4ActionColdWechatText, Text: phrase.Text, DueAt: &dueAt,
		})
	}
	actions = append(actions, V4PlannedAction{
		ActionKey: stableV4ScheduleKey(input.ProfileKey, V4ActionColdWechatInvite, 0, 0, 0),
		Kind:      V4ActionColdWechatInvite, DueAt: &dueAt,
	})
	return V4ScheduleDecision{
		State: state, Status: V4ScheduleActionsPlanned, NextAdvice: V4AdviceNone, Actions: actions,
	}
}

func nextV4InterviewFollowup(groups []V4InterviewFollowupGroup) (V4InterviewFollowupGroup, bool) {
	var selected V4InterviewFollowupGroup
	found := false
	for _, group := range groups {
		if !group.Active {
			continue
		}
		if !found || group.NextStage < selected.NextStage ||
			(group.NextStage == selected.NextStage && group.MessageSeq < selected.MessageSeq) {
			selected = group
			found = true
		}
	}
	return selected, found
}

func v4FollowupDelay(stage uint8) (time.Duration, bool) {
	switch stage {
	case 1:
		return v4FollowupOneDelay, true
	case 2:
		return v4FollowupTwoDelay, true
	case 3:
		return v4FollowupThreeDelay, true
	default:
		return 0, false
	}
}

func v4ColdPromptAvailable(state V4State) bool {
	return state.ColdPromptRemaining > 0 && state.LastColdPromptRound != state.RealMessageRound
}

func v4ColdWechatAvailable(state V4State) bool {
	return state.ColdWechatRemaining > 0 && state.WechatState == V4WechatNotInvited
}

func v4SilenceEndReason(state V4State) V4EndReason {
	if state.MainStatus == V4StatusInvited {
		return V4EndSilentInterview
	}
	switch state.WechatState {
	case V4WechatInvited:
		return V4EndSilentWechatInvited
	case V4WechatExchanged:
		return V4EndSilentWechatExchanged
	default:
		return V4EndSilent
	}
}

func plannedV4Archive(
	profileKey string,
	state V4State,
	anchorMessageSeq int64,
	dueAt time.Time,
	reason V4EndReason,
) V4ScheduleDecision {
	key := fmt.Sprintf(
		"%s|schedule|%s|round:%d|anchor:%d|reason:%s",
		profileKey,
		V4ActionArchive,
		state.RealMessageRound,
		anchorMessageSeq,
		reason,
	)
	dueAt = dueAt.UTC()
	return V4ScheduleDecision{
		State: state, Status: V4ScheduleActionsPlanned, NextAdvice: V4AdviceNone,
		Actions: []V4PlannedAction{{
			ActionKey: key, Kind: V4ActionArchive,
			AnchorMessageSeq: anchorMessageSeq,
			Round:            state.RealMessageRound,
			EndReason:        reason,
			DueAt:            &dueAt,
		}},
	}
}

func noV4ScheduleAction(state V4State) V4ScheduleDecision {
	return V4ScheduleDecision{State: state, Status: V4ScheduleNoAction, NextAdvice: V4AdviceNone}
}

func manualV4Schedule(state V4State, reason V4ManualReason) V4ScheduleDecision {
	return V4ScheduleDecision{
		State: state, Status: V4ScheduleManualRequired, NextAdvice: V4AdviceNone, ManualReason: reason,
	}
}

func stableV4ScheduleKey(profileKey string, kind V4ActionKind, cardMessageSeq int64, round uint64, stage uint8) string {
	key := fmt.Sprintf("%s|schedule|%s", profileKey, kind)
	if cardMessageSeq > 0 {
		key += fmt.Sprintf("|card:%d", cardMessageSeq)
	}
	if round > 0 {
		key += fmt.Sprintf("|round:%d", round)
	}
	if stage > 0 {
		key += fmt.Sprintf("|stage:%d", stage)
	}
	return key
}

func stableV4ScheduleAdviceKey(
	profileKey string,
	purpose V4AdvicePurpose,
	round uint64,
	stage uint8,
) string {
	return fmt.Sprintf(
		"%s|schedule-advice|%s|round:%d|stage:%d",
		profileKey,
		purpose,
		round,
		stage,
	)
}

// ApplyV4ArchiveAction is the internal, non-effectful completion for a planned
// archive. It is separate from ApplyV4ConfirmedAction because no browser
// postcondition exists or is required.
func ApplyV4ArchiveAction(input V4State, action V4PlannedAction) (V4State, error) {
	if err := validateV4State(input); err != nil || action.Kind != V4ActionArchive ||
		strings.TrimSpace(action.ActionKey) == "" || action.AnchorMessageSeq < 0 ||
		action.Round != input.RealMessageRound || action.DueAt == nil || action.DueAt.IsZero() ||
		!validV4EndReason(action.EndReason) {
		return V4State{}, ErrInvalidV4StateTransition
	}
	state := cloneV4State(input)
	if state.MainStatus == V4StatusEnded {
		if state.EndReason != action.EndReason {
			return V4State{}, ErrInvalidV4StateTransition
		}
		return state, nil
	}
	if !isV4ProgressStatus(state.MainStatus) {
		return V4State{}, ErrInvalidV4StateTransition
	}
	archiveV4State(&state, action.EndReason)
	if err := validateV4State(state); err != nil {
		return V4State{}, err
	}
	return state, nil
}
