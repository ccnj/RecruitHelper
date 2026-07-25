package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/textcanon"

	"gorm.io/gorm"
)

const communicationV4SchedulePlanIDDomain = "communication-v4-schedule-plan-v1|"

type FreezeCommunicationV4SchedulePlanRequest struct {
	ProfileID                   string
	ConversationRef             string
	ExpectedRevision            uint64
	ExpectedProjectedThroughSeq int64
	ContextRevisionHash         string
	HasPendingDialogue          bool
	Reply                       communication.ReplyAdvice
	InterviewFollowupTexts      map[uint8]string
	EvaluatedAt                 time.Time
	FrozenAt                    time.Time
}

type FreezeCommunicationV4SchedulePlanResult struct {
	Decision communication.V4ScheduleDecision
	Plan     *CommunicationV4SchedulePlan
	Actions  []CommunicationV4EventAction
	Created  bool
}

// FreezeCommunicationV4SchedulePlan is the sole persistence seam for
// non-archive schedule work. It repeats the deterministic evaluation inside
// the transaction, freezes the exact basis and ordered action list, and makes
// only the first action eligible for the existing effect rail.
func (s *Store) FreezeCommunicationV4SchedulePlan(
	req FreezeCommunicationV4SchedulePlanRequest,
) (*FreezeCommunicationV4SchedulePlanResult, error) {
	req.ProfileID = strings.TrimSpace(req.ProfileID)
	req.ConversationRef = strings.TrimSpace(req.ConversationRef)
	req.ContextRevisionHash = strings.TrimSpace(req.ContextRevisionHash)
	if req.ProfileID == "" ||
		req.ConversationRef == "" ||
		req.ContextRevisionHash == "" ||
		req.ExpectedProjectedThroughSeq < 0 ||
		req.EvaluatedAt.IsZero() ||
		req.FrozenAt.IsZero() ||
		req.FrozenAt.Before(req.EvaluatedAt) {
		return nil, ErrCommunicationV4Invalid
	}
	req.EvaluatedAt = req.EvaluatedAt.UTC()
	req.FrozenAt = req.FrozenAt.UTC()

	out := &FreezeCommunicationV4SchedulePlanResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		aggregate, err := communicationV4AggregateTx(tx, req.ProfileID)
		if err != nil {
			return err
		}
		if aggregate.Revision != req.ExpectedRevision ||
			aggregate.ProjectedThroughSeq != req.ExpectedProjectedThroughSeq {
			return ErrCommunicationV4Conflict
		}
		profile, conversation, activeTail, err := communicationV4ArchiveBoundaryTx(
			tx,
			req.ProfileID,
			req.ConversationRef,
		)
		if err != nil {
			return err
		}
		if profile.Platform != conversation.Platform ||
			profile.AccountRef != conversation.AccountRef ||
			conversation.LastMessageSeq != activeTail ||
			activeTail != aggregate.ProjectedThroughSeq {
			return ErrCommunicationV4Conflict
		}

		fixedPhrases, contextRevisionHash, salutation, ready, err :=
			communicationV4FixedPhrasesForProfileTx(tx, req.ProfileID)
		if err != nil {
			return err
		}
		if !ready || contextRevisionHash != req.ContextRevisionHash {
			return ErrCommunicationV4Conflict
		}
		fixedPhrases = renderCommunicationV4ScheduleFixedPhrases(
			fixedPhrases,
			salutation,
		)
		followups := renderCommunicationV4ScheduleFollowups(
			req.InterviewFollowupTexts,
			salutation,
		)
		decision, err := communication.EvaluateV4Schedule(
			communication.V4ScheduleInput{
				ProfileKey:             req.ProfileID,
				State:                  aggregate.State,
				ProjectedThroughSeq:    aggregate.ProjectedThroughSeq,
				Now:                    req.EvaluatedAt,
				HasPendingDialogue:     req.HasPendingDialogue,
				Reply:                  req.Reply,
				FixedPhrases:           fixedPhrases,
				InterviewFollowupTexts: followups,
			},
		)
		if err != nil {
			return err
		}
		out.Decision = decision
		if decision.Status != communication.V4ScheduleActionsPlanned {
			return nil
		}
		if len(decision.Actions) == 0 ||
			decision.Actions[0].Kind == communication.V4ActionArchive {
			return ErrCommunicationV4Conflict
		}
		dueAt, err := validateCommunicationV4ScheduleActions(
			decision.Actions,
			req.EvaluatedAt,
		)
		if err != nil {
			return err
		}
		actionsDigest, err := communicationV4InputDigest(decision.Actions)
		if err != nil {
			return err
		}
		planKey := decision.Actions[0].ActionKey
		planID := communicationV4SchedulePlanID(req.ProfileID, planKey)
		existing, found, err := communicationV4SchedulePlanTx(tx, planID)
		if err != nil {
			return err
		}
		if found {
			if !communicationV4SchedulePlanMatches(
				existing,
				req,
				activeTail,
				dueAt,
				actionsDigest,
				decision.Actions,
			) {
				return ErrCommunicationV4Conflict
			}
			actions, err := communicationV4EventActionsBySourceTx(
				tx,
				req.ProfileID,
				CommunicationV4InputSchedulePlan,
				planID,
			)
			if err != nil {
				return err
			}
			out.Plan = &existing
			out.Actions = actions
			return nil
		}

		plan := CommunicationV4SchedulePlan{
			PlanID: planID, PlanKey: planKey,
			ProfileID: req.ProfileID, ConversationRef: req.ConversationRef,
			BasisRevision:            req.ExpectedRevision,
			BasisProjectedThroughSeq: req.ExpectedProjectedThroughSeq,
			BasisMessageTailSeq:      activeTail,
			ContextRevisionHash:      req.ContextRevisionHash,
			EvaluatedAt:              req.EvaluatedAt,
			DueAt:                    dueAt,
			PlannedActions:           cloneCommunicationV4PlannedActions(decision.Actions),
			ActionsDigest:            actionsDigest,
			CreatedAt:                req.FrozenAt,
			UpdatedAt:                req.FrozenAt,
		}
		if err := tx.Create(&plan).Error; err != nil {
			return err
		}
		first, _, err := materializeCommunicationV4ScheduleActionTx(
			tx,
			plan,
			0,
			req.FrozenAt,
		)
		if err != nil {
			return err
		}
		out.Plan = &plan
		out.Actions = []CommunicationV4EventAction{first}
		out.Created = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) CommunicationV4SchedulePlanByID(
	planID string,
) (*CommunicationV4SchedulePlan, error) {
	planID = strings.TrimSpace(planID)
	if planID == "" {
		return nil, ErrCommunicationV4Invalid
	}
	plan, found, err := communicationV4SchedulePlanTx(s.db, planID)
	if err != nil || !found {
		return nil, err
	}
	return &plan, nil
}

func communicationV4SchedulePlanID(profileID, planKey string) string {
	sum := sha256.Sum256([]byte(
		communicationV4SchedulePlanIDDomain + profileID + "\x00" + planKey,
	))
	return hex.EncodeToString(sum[:])
}

func communicationV4SchedulePlanTx(
	tx *gorm.DB,
	planID string,
) (CommunicationV4SchedulePlan, bool, error) {
	var plan CommunicationV4SchedulePlan
	err := tx.First(&plan, "plan_id = ?", planID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CommunicationV4SchedulePlan{}, false, nil
	}
	if err != nil {
		return CommunicationV4SchedulePlan{}, false, err
	}
	if strings.TrimSpace(plan.PlanID) == "" ||
		strings.TrimSpace(plan.PlanKey) == "" ||
		strings.TrimSpace(plan.ProfileID) == "" ||
		strings.TrimSpace(plan.ConversationRef) == "" ||
		strings.TrimSpace(plan.ContextRevisionHash) == "" ||
		plan.BasisProjectedThroughSeq < 0 ||
		plan.BasisMessageTailSeq != plan.BasisProjectedThroughSeq ||
		plan.EvaluatedAt.IsZero() ||
		plan.DueAt.IsZero() ||
		plan.EvaluatedAt.Before(plan.DueAt) ||
		plan.CreatedAt.IsZero() ||
		plan.UpdatedAt.IsZero() ||
		len(plan.PlannedActions) == 0 ||
		strings.TrimSpace(plan.ActionsDigest) == "" ||
		communicationV4SchedulePlanID(plan.ProfileID, plan.PlanKey) != plan.PlanID {
		return CommunicationV4SchedulePlan{}, false, ErrCommunicationV4Corrupt
	}
	digest, err := communicationV4InputDigest(plan.PlannedActions)
	if err != nil || digest != plan.ActionsDigest {
		return CommunicationV4SchedulePlan{}, false, ErrCommunicationV4Corrupt
	}
	if _, err := validateCommunicationV4ScheduleActions(
		plan.PlannedActions,
		plan.EvaluatedAt,
	); err != nil {
		return CommunicationV4SchedulePlan{}, false, ErrCommunicationV4Corrupt
	}
	return plan, true, nil
}

func validateCommunicationV4ScheduleActions(
	actions []communication.V4PlannedAction,
	evaluatedAt time.Time,
) (time.Time, error) {
	if len(actions) == 0 || len(actions) > 2 || evaluatedAt.IsZero() {
		return time.Time{}, ErrCommunicationV4Invalid
	}
	seen := make(map[string]struct{}, len(actions))
	var dueAt time.Time
	for index, action := range actions {
		action.ActionKey = strings.TrimSpace(action.ActionKey)
		if action.ActionKey == "" ||
			action.DueAt == nil ||
			action.DueAt.IsZero() ||
			evaluatedAt.Before(action.DueAt.UTC()) {
			return time.Time{}, ErrCommunicationV4Invalid
		}
		if _, exists := seen[action.ActionKey]; exists {
			return time.Time{}, ErrCommunicationV4Conflict
		}
		seen[action.ActionKey] = struct{}{}
		if index == 0 {
			dueAt = action.DueAt.UTC()
		} else if !dueAt.Equal(action.DueAt.UTC()) {
			return time.Time{}, ErrCommunicationV4Invalid
		}
		switch action.Kind {
		case communication.V4ActionColdPrompt:
			if index != 0 || strings.TrimSpace(action.Text) == "" ||
				action.Round == 0 || action.Stage < 1 || action.Stage > 2 ||
				m5ai.ValidateSendText(action.Text) != nil {
				return time.Time{}, ErrCommunicationV4Invalid
			}
		case communication.V4ActionColdWechatText:
			if index != 0 || strings.TrimSpace(action.Text) == "" ||
				m5ai.ValidateSendText(action.Text) != nil {
				return time.Time{}, ErrCommunicationV4Invalid
			}
		case communication.V4ActionColdWechatInvite:
			if (len(actions) == 2 && index != 1) ||
				(len(actions) == 1 && index != 0) ||
				action.Text != "" {
				return time.Time{}, ErrCommunicationV4Invalid
			}
		case communication.V4ActionInterviewFollowup:
			if index != 0 || strings.TrimSpace(action.Text) == "" ||
				action.CardMessageSeq <= 0 ||
				action.Stage < 1 || action.Stage > 3 ||
				m5ai.ValidateSendText(action.Text) != nil {
				return time.Time{}, ErrCommunicationV4Invalid
			}
		default:
			return time.Time{}, ErrCommunicationV4Invalid
		}
	}
	if len(actions) == 2 &&
		(actions[0].Kind != communication.V4ActionColdWechatText ||
			actions[1].Kind != communication.V4ActionColdWechatInvite) {
		return time.Time{}, ErrCommunicationV4Invalid
	}
	return dueAt, nil
}

func materializeCommunicationV4ScheduleActionTx(
	tx *gorm.DB,
	plan CommunicationV4SchedulePlan,
	ordinal int,
	at time.Time,
) (CommunicationV4EventAction, bool, error) {
	if tx == nil || ordinal < 0 || ordinal >= len(plan.PlannedActions) || at.IsZero() {
		return CommunicationV4EventAction{}, false, ErrCommunicationV4EventActionInvalid
	}
	planned := plan.PlannedActions[ordinal]
	actionID, err := CommunicationV4EventActionID(plan.ProfileID, planned.ActionKey)
	if err != nil {
		return CommunicationV4EventAction{}, false, err
	}
	var existing CommunicationV4EventAction
	err = tx.First(&existing, "action_id = ?", actionID).Error
	if err == nil {
		if !communicationV4ScheduleEventActionMatches(
			existing,
			plan,
			planned,
			ordinal,
		) {
			return CommunicationV4EventAction{}, false, ErrCommunicationV4EventActionConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return CommunicationV4EventAction{}, false, err
	}
	if ordinal > 0 {
		if err := validateCommunicationV4SchedulePreviousActionTx(
			tx,
			plan,
			ordinal,
		); err != nil {
			return CommunicationV4EventAction{}, false, err
		}
	}
	effectKind, err := communicationV4EventEffectKind(planned.Kind)
	if err != nil {
		return CommunicationV4EventAction{}, false, err
	}
	row := CommunicationV4EventAction{
		ActionID: actionID, ProfileID: plan.ProfileID,
		SourceInputKind:     CommunicationV4InputSchedulePlan,
		SourceInputKey:      plan.PlanID,
		SourceOrdinal:       ordinal,
		SemanticActionKey:   planned.ActionKey,
		V4Kind:              planned.Kind,
		CardMessageSeq:      planned.CardMessageSeq,
		EffectKind:          effectKind,
		Text:                planned.Text,
		ContextRevisionHash: plan.ContextRevisionHash,
		Status:              CommunicationV4EventActionPlanned,
		PlannedAt:           at.UTC(),
		CreatedAt:           at.UTC(),
		UpdatedAt:           at.UTC(),
	}
	switch effectKind {
	case CommunicationV4EventEffectReplyText:
		row.ContentHash = textcanon.Hash(row.Text)
	case CommunicationV4EventEffectInviteWechat:
		row.ContentHash = communicationWechatInviteContentHash()
	default:
		return CommunicationV4EventAction{}, false, ErrCommunicationV4EventActionInvalid
	}
	if ordinal > 0 {
		parentID, err := CommunicationV4EventActionID(
			plan.ProfileID,
			plan.PlannedActions[ordinal-1].ActionKey,
		)
		if err != nil {
			return CommunicationV4EventAction{}, false, err
		}
		row.DependsOnActionID = &parentID
	}
	if !validCommunicationV4EventActionDisposition(row) {
		return CommunicationV4EventAction{}, false, ErrCommunicationV4EventActionInvalid
	}
	if err := tx.Create(&row).Error; err != nil {
		return CommunicationV4EventAction{}, false, err
	}
	return row, true, nil
}

func validateCommunicationV4SchedulePreviousActionTx(
	tx *gorm.DB,
	plan CommunicationV4SchedulePlan,
	ordinal int,
) error {
	if ordinal <= 0 || ordinal >= len(plan.PlannedActions) {
		return ErrCommunicationV4EventActionConflict
	}
	previous := plan.PlannedActions[ordinal-1]
	previousID, err := CommunicationV4EventActionID(
		plan.ProfileID,
		previous.ActionKey,
	)
	if err != nil {
		return err
	}
	var row CommunicationV4EventAction
	if err := tx.First(&row, "action_id = ?", previousID).Error; err != nil {
		return err
	}
	if !communicationV4ScheduleEventActionMatches(
		row,
		plan,
		previous,
		ordinal-1,
	) ||
		row.Status != CommunicationV4EventActionSent ||
		row.EffectIntentID == nil ||
		row.SentAt == nil ||
		row.FailureReason != "" {
		return ErrCommunicationV4EventActionConflict
	}
	confirmed, found, err := communicationV4ApplicationTx(
		tx,
		plan.ProfileID,
		CommunicationV4InputConfirmedAction,
		previous.ActionKey,
	)
	if err != nil {
		return err
	}
	if !found ||
		confirmed.SemanticKind != string(previous.Kind) ||
		confirmed.MessageSeq <= 0 {
		return ErrCommunicationV4EventActionConflict
	}
	return nil
}

func communicationV4ScheduleEventActionMatches(
	row CommunicationV4EventAction,
	plan CommunicationV4SchedulePlan,
	planned communication.V4PlannedAction,
	ordinal int,
) bool {
	expectedID, err := CommunicationV4EventActionID(plan.ProfileID, planned.ActionKey)
	if err != nil {
		return false
	}
	if row.ActionID != expectedID ||
		row.ProfileID != plan.ProfileID ||
		row.SourceInputKind != CommunicationV4InputSchedulePlan ||
		row.SourceInputKey != plan.PlanID ||
		row.SourceOrdinal != ordinal ||
		row.SemanticActionKey != planned.ActionKey ||
		row.V4Kind != planned.Kind ||
		row.CardMessageSeq != planned.CardMessageSeq ||
		row.Text != planned.Text ||
		row.ContextRevisionHash != plan.ContextRevisionHash ||
		!validCommunicationV4EventActionDisposition(row) {
		return false
	}
	if ordinal == 0 {
		return row.DependsOnActionID == nil
	}
	parentID, err := CommunicationV4EventActionID(
		plan.ProfileID,
		plan.PlannedActions[ordinal-1].ActionKey,
	)
	return err == nil &&
		row.DependsOnActionID != nil &&
		*row.DependsOnActionID == parentID
}

func communicationV4SchedulePlanMatches(
	plan CommunicationV4SchedulePlan,
	req FreezeCommunicationV4SchedulePlanRequest,
	activeTail int64,
	dueAt time.Time,
	digest string,
	actions []communication.V4PlannedAction,
) bool {
	return plan.ProfileID == req.ProfileID &&
		plan.ConversationRef == req.ConversationRef &&
		plan.BasisRevision == req.ExpectedRevision &&
		plan.BasisProjectedThroughSeq == req.ExpectedProjectedThroughSeq &&
		plan.BasisMessageTailSeq == activeTail &&
		plan.ContextRevisionHash == req.ContextRevisionHash &&
		plan.EvaluatedAt.Equal(req.EvaluatedAt) &&
		plan.DueAt.Equal(dueAt) &&
		plan.ActionsDigest == digest &&
		communicationV4PlannedActionsEqual(plan.PlannedActions, actions)
}

func communicationV4PlannedActionsEqual(
	left []communication.V4PlannedAction,
	right []communication.V4PlannedAction,
) bool {
	leftDigest, leftErr := communicationV4InputDigest(left)
	rightDigest, rightErr := communicationV4InputDigest(right)
	return leftErr == nil && rightErr == nil && leftDigest == rightDigest
}

func cloneCommunicationV4PlannedActions(
	actions []communication.V4PlannedAction,
) []communication.V4PlannedAction {
	cloned := make([]communication.V4PlannedAction, len(actions))
	copy(cloned, actions)
	for index := range cloned {
		if cloned[index].DueAt != nil {
			value := cloned[index].DueAt.UTC()
			cloned[index].DueAt = &value
		}
	}
	return cloned
}

func renderCommunicationV4ScheduleFixedPhrases(
	view communication.V4FixedPhraseView,
	salutation string,
) communication.V4FixedPhraseView {
	phrase := view.Phrase(communication.V4PhraseColdWechat)
	if phrase.State != communication.V4PhraseAvailable {
		return view
	}
	rendered, err := communication.RenderV4FixedPhrase(
		phrase.Text,
		communication.V4FixedPhraseRenderInput{Salutation: salutation},
	)
	if err != nil {
		phrase.State = communication.V4PhraseInvalid
		phrase.Text = ""
	} else {
		phrase.Text = rendered
	}
	view.Phrases[communication.V4PhraseColdWechat] = phrase
	return view
}

func renderCommunicationV4ScheduleFollowups(
	input map[uint8]string,
	salutation string,
) map[uint8]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[uint8]string, len(input))
	for stage, text := range input {
		rendered, err := communication.RenderV4FixedPhrase(
			text,
			communication.V4FixedPhraseRenderInput{Salutation: salutation},
		)
		if err == nil {
			out[stage] = rendered
		}
	}
	return out
}
