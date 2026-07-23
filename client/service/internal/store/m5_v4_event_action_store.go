package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/textcanon"

	"gorm.io/gorm"
)

const (
	communicationV4EventActionIDDomain = "communication-v4-event-action-v1|"

	CommunicationV4EventActionFailureFixedPhraseUnavailable      = "fixedPhraseUnavailable"
	CommunicationV4EventActionFailureNotificationChannelDeferred = "notificationChannelDeferred"
	CommunicationV4EventActionFailurePrimitiveUnavailable        = "primitiveUnavailable"
	CommunicationV4EventActionFailureDialogueActionOwned         = "dialogueActionOwned"
)

var (
	ErrCommunicationV4EventActionInvalid  = errors.New("V4 事件动作输入无效")
	ErrCommunicationV4EventActionMissing  = errors.New("V4 事件动作来源不存在")
	ErrCommunicationV4EventActionConflict = errors.New("V4 事件动作账本冲突")
)

type MaterializeCommunicationV4EventActionsRequest struct {
	ProfileID      string
	SourceInputKey string
	MaterializedAt time.Time
}

type MaterializeCommunicationV4EventActionsResult struct {
	Actions []CommunicationV4EventAction
	Created bool
}

type communicationV4EventActionSkeleton struct {
	actionID          string
	sourceOrdinal     int
	semanticActionKey string
	v4Kind            communication.V4ActionKind
	cardMessageSeq    int64
	effectKind        CommunicationV4EventEffectKind
	dependsOnActionID *string
	dialogueOwned     bool
	dialogueOwnerID   string
}

// CommunicationV4EventActionID scopes the reducer's semantic action key to one
// profile. The semantic key itself is deliberately not a global primary key.
func CommunicationV4EventActionID(profileID, semanticActionKey string) (string, error) {
	profileID = strings.TrimSpace(profileID)
	semanticActionKey = strings.TrimSpace(semanticActionKey)
	if profileID == "" || semanticActionKey == "" {
		return "", ErrCommunicationV4EventActionInvalid
	}
	sum := sha256.Sum256([]byte(
		communicationV4EventActionIDDomain + profileID + "\x00" + semanticActionKey,
	))
	return hex.EncodeToString(sum[:]), nil
}

// MaterializeCommunicationV4EventActions turns the immutable outcome of one
// projection receipt into local action facts. A replay returns
// the original frozen rows without consulting the current job configuration.
func (s *Store) MaterializeCommunicationV4EventActions(
	req MaterializeCommunicationV4EventActionsRequest,
) (*MaterializeCommunicationV4EventActionsResult, error) {
	req.ProfileID = strings.TrimSpace(req.ProfileID)
	req.SourceInputKey = strings.TrimSpace(req.SourceInputKey)
	if req.ProfileID == "" || req.SourceInputKey == "" {
		return nil, ErrCommunicationV4EventActionInvalid
	}
	if req.MaterializedAt.IsZero() {
		req.MaterializedAt = time.Now()
	}
	req.MaterializedAt = req.MaterializedAt.UTC()

	out := &MaterializeCommunicationV4EventActionsResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		application, found, err := communicationV4ApplicationTx(
			tx,
			req.ProfileID,
			CommunicationV4InputBusinessEvent,
			req.SourceInputKey,
		)
		if err != nil {
			return err
		}
		if !found {
			return ErrCommunicationV4EventActionMissing
		}
		actions, created, err := materializeCommunicationV4EventActionsTx(
			tx,
			application,
			req.MaterializedAt,
		)
		if err != nil {
			return err
		}
		out.Actions = actions
		out.Created = created
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func materializeCommunicationV4EventActionsTx(
	tx *gorm.DB,
	application CommunicationV4ProjectionApplication,
	materializedAt time.Time,
) ([]CommunicationV4EventAction, bool, error) {
	if tx == nil ||
		(application.InputKind != CommunicationV4InputBusinessEvent &&
			application.InputKind != CommunicationV4InputDialogueTurn) ||
		strings.TrimSpace(application.ProfileID) == "" ||
		strings.TrimSpace(application.InputKey) == "" ||
		materializedAt.IsZero() {
		return nil, false, ErrCommunicationV4EventActionInvalid
	}
	materializedAt = materializedAt.UTC()
	skeletons, err := communicationV4EventActionSkeletonsTx(tx, application)
	if err != nil {
		return nil, false, err
	}
	existing, err := communicationV4EventActionsBySourceTx(
		tx,
		application.ProfileID,
		application.InputKind,
		application.InputKey,
	)
	if err != nil {
		return nil, false, err
	}
	if len(existing) > 0 {
		if !communicationV4EventActionReplayMatches(
			existing,
			skeletons,
			application.ProfileID,
			application.InputKind,
			application.InputKey,
		) {
			return nil, false, ErrCommunicationV4EventActionConflict
		}
		return existing, false, nil
	}
	if len(skeletons) == 0 {
		return nil, false, nil
	}
	for _, skeleton := range skeletons {
		var collision CommunicationV4EventAction
		err := tx.First(&collision, "action_id = ?", skeleton.actionID).Error
		if err == nil {
			return nil, false, ErrCommunicationV4EventActionConflict
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, err
		}
	}

	var fixedPhrases communication.V4FixedPhraseView
	var contextRevisionHash string
	var fixedPhrasesReady bool
	if communicationV4EventActionsNeedFixedPhrases(skeletons) {
		fixedPhrases, contextRevisionHash, fixedPhrasesReady, err =
			communicationV4FixedPhrasesForProfileTx(tx, application.ProfileID)
		if err != nil {
			return nil, false, err
		}
	}
	rows := make([]CommunicationV4EventAction, 0, len(skeletons))
	for _, skeleton := range skeletons {
		row := CommunicationV4EventAction{
			ActionID: skeleton.actionID, ProfileID: application.ProfileID,
			SourceInputKind:   application.InputKind,
			SourceInputKey:    application.InputKey,
			SourceOrdinal:     skeleton.sourceOrdinal,
			SemanticActionKey: skeleton.semanticActionKey,
			V4Kind:            skeleton.v4Kind,
			CardMessageSeq:    skeleton.cardMessageSeq,
			EffectKind:        skeleton.effectKind,
			DependsOnActionID: cloneStringPointer(skeleton.dependsOnActionID),
			PlannedAt:         materializedAt,
			CreatedAt:         materializedAt,
			UpdatedAt:         materializedAt,
		}
		if skeleton.dialogueOwned {
			row.Status = CommunicationV4EventActionDeferred
			row.FailureReason = CommunicationV4EventActionFailureDialogueActionOwned
		} else {
			materializeCommunicationV4EventActionDisposition(
				&row,
				fixedPhrases,
				contextRevisionHash,
				fixedPhrasesReady,
			)
		}
		rows = append(rows, row)
	}
	if err := tx.Create(&rows).Error; err != nil {
		return nil, false, err
	}
	return rows, true, nil
}

func (s *Store) CommunicationV4EventActionsByProfile(
	profileID string,
) ([]CommunicationV4EventAction, error) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return nil, ErrCommunicationV4EventActionInvalid
	}
	var actions []CommunicationV4EventAction
	err := s.db.
		Where("profile_id = ?", profileID).
		Order("planned_at, source_input_kind, source_input_key, source_ordinal").
		Find(&actions).Error
	return actions, err
}

func (s *Store) CommunicationV4EventActionsBySource(
	profileID string,
	inputKind CommunicationV4InputKind,
	inputKey string,
) ([]CommunicationV4EventAction, error) {
	profileID = strings.TrimSpace(profileID)
	inputKey = strings.TrimSpace(inputKey)
	if profileID == "" || inputKind == "" || inputKey == "" {
		return nil, ErrCommunicationV4EventActionInvalid
	}
	return communicationV4EventActionsBySourceTx(s.db, profileID, inputKind, inputKey)
}

func communicationV4EventActionsBySourceTx(
	tx *gorm.DB,
	profileID string,
	inputKind CommunicationV4InputKind,
	inputKey string,
) ([]CommunicationV4EventAction, error) {
	var actions []CommunicationV4EventAction
	err := tx.
		Where(
			"profile_id = ? AND source_input_kind = ? AND source_input_key = ?",
			profileID,
			inputKind,
			inputKey,
		).
		Order("source_ordinal").
		Find(&actions).Error
	return actions, err
}

func communicationV4EventActionSkeletonsTx(
	tx *gorm.DB,
	application CommunicationV4ProjectionApplication,
) ([]communicationV4EventActionSkeleton, error) {
	if tx == nil {
		return nil, ErrCommunicationV4EventActionInvalid
	}
	profileID := application.ProfileID
	actions := application.Outcome.Actions
	skeletons := make([]communicationV4EventActionSkeleton, len(actions))
	seenKeys := make(map[string]struct{}, len(actions))
	receiptIDs := make([]string, 0, 1)
	receiptIndexes := make([]int, 0, 1)
	for index, action := range actions {
		action.ActionKey = strings.TrimSpace(action.ActionKey)
		if action.ActionKey == "" || action.CardMessageSeq <= 0 {
			return nil, ErrCommunicationV4EventActionInvalid
		}
		if _, exists := seenKeys[action.ActionKey]; exists {
			return nil, ErrCommunicationV4EventActionConflict
		}
		seenKeys[action.ActionKey] = struct{}{}
		actionID, err := CommunicationV4EventActionID(profileID, action.ActionKey)
		if err != nil {
			return nil, err
		}
		effectKind, err := communicationV4EventEffectKind(action.Kind)
		if err != nil {
			return nil, err
		}
		skeletons[index] = communicationV4EventActionSkeleton{
			actionID: actionID, sourceOrdinal: index,
			semanticActionKey: action.ActionKey, v4Kind: action.Kind,
			cardMessageSeq: action.CardMessageSeq, effectKind: effectKind,
		}
		if action.Kind == communication.V4ActionWechatReceipt ||
			action.Kind == communication.V4ActionInterviewAcceptedReceipt {
			receiptIDs = append(receiptIDs, actionID)
			receiptIndexes = append(receiptIndexes, index)
		}
	}
	if len(receiptIDs) > 1 {
		return nil, ErrCommunicationV4EventActionConflict
	}
	if application.InputKind == CommunicationV4InputDialogueTurn &&
		len(skeletons) > 0 {
		if err := bindLegacyCommunicationV4DialogueReceiptTx(
			tx,
			application,
			skeletons,
			receiptIndexes,
		); err != nil {
			return nil, err
		}
	}
	if len(receiptIDs) == 1 {
		dependency := receiptIDs[0]
		if skeletons[receiptIndexes[0]].dialogueOwned {
			dependency = skeletons[receiptIndexes[0]].dialogueOwnerID
		}
		for index := range skeletons {
			if skeletons[index].v4Kind == communication.V4ActionInviteWechat {
				skeletons[index].dependsOnActionID = &dependency
			}
		}
	}
	return skeletons, nil
}

func bindLegacyCommunicationV4DialogueReceiptTx(
	tx *gorm.DB,
	application CommunicationV4ProjectionApplication,
	skeletons []communicationV4EventActionSkeleton,
	receiptIndexes []int,
) error {
	plans := application.Outcome.PlannedActions
	if len(plans) == 0 {
		return nil
	}
	if len(receiptIndexes) != 1 || len(plans) != 1 {
		return ErrCommunicationV4EventActionConflict
	}
	skeleton := &skeletons[receiptIndexes[0]]
	plan := plans[0]
	if strings.TrimSpace(plan.ActionKey) != skeleton.semanticActionKey ||
		plan.Kind != skeleton.v4Kind ||
		plan.CardMessageSeq != skeleton.cardMessageSeq ||
		plan.Text != "" ||
		plan.InterviewStartsAtMs != nil ||
		plan.InterviewEndsAtMs != nil ||
		plan.InterviewMethod != nil ||
		plan.Round != 0 ||
		plan.Stage != 0 ||
		plan.EndReason != "" {
		return ErrCommunicationV4EventActionConflict
	}
	var action CommunicationAction
	if err := tx.First(
		&action,
		"action_id = ? AND turn_id = ?",
		plan.ActionKey,
		application.InputKey,
	).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrCommunicationV4EventActionConflict
		}
		return err
	}
	if action.Kind != CommunicationActionReplyText ||
		!communicationActionMatchesV4Plan(action, plan) ||
		!validLegacyCommunicationV4DialogueReceiptStatus(action.Status) {
		return ErrCommunicationV4EventActionConflict
	}
	skeleton.dialogueOwned = true
	skeleton.dialogueOwnerID = action.ActionID
	return nil
}

func validLegacyCommunicationV4DialogueReceiptStatus(
	status CommunicationActionStatus,
) bool {
	switch status {
	case CommunicationActionPlanned,
		CommunicationActionEffectPending,
		CommunicationActionSent,
		CommunicationActionManualRequired,
		CommunicationActionSuperseded:
		return true
	default:
		return false
	}
}

func communicationV4EventActionsNeedFixedPhrases(
	skeletons []communicationV4EventActionSkeleton,
) bool {
	for _, skeleton := range skeletons {
		if skeleton.dialogueOwned {
			continue
		}
		if skeleton.v4Kind == communication.V4ActionWechatReceipt ||
			skeleton.v4Kind == communication.V4ActionInterviewAcceptedReceipt {
			return true
		}
	}
	return false
}

func communicationV4EventEffectKind(
	kind communication.V4ActionKind,
) (CommunicationV4EventEffectKind, error) {
	switch kind {
	case communication.V4ActionWechatReceipt, communication.V4ActionInterviewAcceptedReceipt:
		return CommunicationV4EventEffectReplyText, nil
	case communication.V4ActionInviteWechat:
		return CommunicationV4EventEffectInviteWechat, nil
	case communication.V4ActionNotifyWechat, communication.V4ActionNotifyInterviewAccepted:
		return CommunicationV4EventEffectNotification, nil
	case communication.V4ActionAcceptWechat:
		return CommunicationV4EventEffectAcceptWechat, nil
	default:
		return "", ErrCommunicationV4EventActionInvalid
	}
}

func communicationV4FixedPhrasesForProfileTx(
	tx *gorm.DB,
	profileID string,
) (communication.V4FixedPhraseView, string, bool, error) {
	var binding ProfileAIContextBinding
	err := tx.First(
		&binding,
		"profile_id = ? AND status = ?",
		profileID,
		ProfileAIContextBindingActive,
	).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return communication.V4FixedPhraseView{}, "", false, nil
	}
	if err != nil {
		return communication.V4FixedPhraseView{}, "", false, err
	}
	var revision JobAIContextRevision
	err = tx.First(&revision, "revision_hash = ?", binding.RevisionHash).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return communication.V4FixedPhraseView{}, "", false, nil
	}
	if err != nil {
		return communication.V4FixedPhraseView{}, "", false, err
	}
	if revision.ContextID != binding.ContextID {
		return communication.V4FixedPhraseView{}, revision.RevisionHash, false, nil
	}
	view, err := communication.BuildV4FixedPhraseView(revision.SourcePackage)
	if err != nil {
		return communication.V4FixedPhraseView{}, revision.RevisionHash, false, nil
	}
	return view, revision.RevisionHash, true, nil
}

func materializeCommunicationV4EventActionDisposition(
	row *CommunicationV4EventAction,
	fixedPhrases communication.V4FixedPhraseView,
	contextRevisionHash string,
	fixedPhrasesReady bool,
) {
	switch row.V4Kind {
	case communication.V4ActionWechatReceipt, communication.V4ActionInterviewAcceptedReceipt:
		phraseKind := communication.V4PhraseWechatReceipt
		if row.V4Kind == communication.V4ActionInterviewAcceptedReceipt {
			phraseKind = communication.V4PhraseInterviewAccepted
		}
		phrase := fixedPhrases.Phrase(phraseKind)
		if !fixedPhrasesReady || phrase.State != communication.V4PhraseAvailable {
			row.Status = CommunicationV4EventActionManualRequired
			row.FailureReason = CommunicationV4EventActionFailureFixedPhraseUnavailable
			row.ContextRevisionHash = contextRevisionHash
			return
		}
		row.Status = CommunicationV4EventActionPlanned
		row.Text = phrase.Text
		row.ContentHash = textcanon.Hash(phrase.Text)
		row.ContextRevisionHash = contextRevisionHash
	case communication.V4ActionInviteWechat:
		row.Status = CommunicationV4EventActionPlanned
		row.ContentHash = communicationWechatInviteContentHash()
	case communication.V4ActionNotifyWechat, communication.V4ActionNotifyInterviewAccepted:
		row.Status = CommunicationV4EventActionDeferred
		row.FailureReason = CommunicationV4EventActionFailureNotificationChannelDeferred
	case communication.V4ActionAcceptWechat:
		row.Status = CommunicationV4EventActionDeferred
		row.FailureReason = CommunicationV4EventActionFailurePrimitiveUnavailable
	}
}

func communicationV4EventActionReplayMatches(
	rows []CommunicationV4EventAction,
	skeletons []communicationV4EventActionSkeleton,
	profileID string,
	sourceInputKind CommunicationV4InputKind,
	sourceInputKey string,
) bool {
	if len(rows) != len(skeletons) {
		return false
	}
	for index, row := range rows {
		skeleton := skeletons[index]
		if row.ActionID != skeleton.actionID ||
			row.ProfileID != profileID ||
			row.SourceInputKind != sourceInputKind ||
			row.SourceInputKey != sourceInputKey ||
			row.SourceOrdinal != skeleton.sourceOrdinal ||
			row.SemanticActionKey != skeleton.semanticActionKey ||
			row.V4Kind != skeleton.v4Kind ||
			row.CardMessageSeq != skeleton.cardMessageSeq ||
			row.EffectKind != skeleton.effectKind ||
			!sameOptionalString(row.DependsOnActionID, skeleton.dependsOnActionID) ||
			!validCommunicationV4EventActionDisposition(row) ||
			communicationV4EventActionDialogueOwned(row) != skeleton.dialogueOwned {
			return false
		}
	}
	return true
}

func validCommunicationV4EventActionDisposition(row CommunicationV4EventAction) bool {
	switch row.V4Kind {
	case communication.V4ActionWechatReceipt, communication.V4ActionInterviewAcceptedReceipt:
		switch row.Status {
		case CommunicationV4EventActionPlanned:
			return row.Text != "" &&
				row.ContentHash == textcanon.Hash(row.Text) &&
				row.ContextRevisionHash != "" &&
				row.FailureReason == "" &&
				validCommunicationV4EventActionEffectFields(row)
		case CommunicationV4EventActionEffectPending,
			CommunicationV4EventActionSent:
			return row.Text != "" &&
				row.ContentHash == textcanon.Hash(row.Text) &&
				row.ContextRevisionHash != "" &&
				row.FailureReason == "" &&
				validCommunicationV4EventActionEffectFields(row)
		case CommunicationV4EventActionManualRequired:
			if row.FailureReason == CommunicationV4EventActionFailureFixedPhraseUnavailable {
				return row.Text == "" &&
					row.ContentHash == "" &&
					row.EffectIntentID == nil &&
					row.EffectStartedAt == nil &&
					row.SentAt == nil
			}
			return row.Text != "" &&
				row.ContentHash == textcanon.Hash(row.Text) &&
				row.ContextRevisionHash != "" &&
				validCommunicationV4EventActionFailureReason(row.FailureReason) &&
				validCommunicationV4EventActionEffectFields(row)
		case CommunicationV4EventActionDeferred:
			return row.Text == "" &&
				row.ContentHash == "" &&
				row.ContextRevisionHash == "" &&
				row.FailureReason == CommunicationV4EventActionFailureDialogueActionOwned &&
				validCommunicationV4EventActionEffectFields(row)
		default:
			return false
		}
	case communication.V4ActionInviteWechat:
		if row.Status != CommunicationV4EventActionPlanned &&
			row.Status != CommunicationV4EventActionEffectPending &&
			row.Status != CommunicationV4EventActionSent &&
			row.Status != CommunicationV4EventActionManualRequired {
			return false
		}
		return (row.Status != CommunicationV4EventActionManualRequired ||
			validCommunicationV4EventActionFailureReason(row.FailureReason)) &&
			row.Text == "" &&
			row.ContentHash == communicationWechatInviteContentHash() &&
			row.ContextRevisionHash == "" &&
			(row.Status == CommunicationV4EventActionManualRequired ||
				row.FailureReason == "") &&
			validCommunicationV4EventActionEffectFields(row)
	case communication.V4ActionNotifyWechat, communication.V4ActionNotifyInterviewAccepted:
		return row.Status == CommunicationV4EventActionDeferred &&
			row.FailureReason == CommunicationV4EventActionFailureNotificationChannelDeferred &&
			validCommunicationV4EventActionEffectFields(row)
	case communication.V4ActionAcceptWechat:
		return row.Status == CommunicationV4EventActionDeferred &&
			row.FailureReason == CommunicationV4EventActionFailurePrimitiveUnavailable &&
			validCommunicationV4EventActionEffectFields(row)
	default:
		return false
	}
}

func validCommunicationV4EventActionEffectFields(
	row CommunicationV4EventAction,
) bool {
	switch row.Status {
	case CommunicationV4EventActionPlanned,
		CommunicationV4EventActionDeferred:
		return row.EffectIntentID == nil &&
			row.EffectStartedAt == nil &&
			row.SentAt == nil
	case CommunicationV4EventActionEffectPending:
		return row.EffectIntentID != nil &&
			strings.TrimSpace(*row.EffectIntentID) != "" &&
			row.EffectStartedAt != nil &&
			row.SentAt == nil
	case CommunicationV4EventActionSent:
		return row.EffectIntentID != nil &&
			strings.TrimSpace(*row.EffectIntentID) != "" &&
			row.EffectStartedAt != nil &&
			row.SentAt != nil
	case CommunicationV4EventActionManualRequired:
		return row.EffectIntentID != nil &&
			strings.TrimSpace(*row.EffectIntentID) != "" &&
			row.EffectStartedAt != nil &&
			row.SentAt == nil
	default:
		return false
	}
}

func validCommunicationV4EventActionFailureReason(reason string) bool {
	switch reason {
	case "effectFailed", "effectSuspect", "effectResolvedFailed":
		return true
	default:
		return false
	}
}

func communicationV4EventActionDialogueOwned(row CommunicationV4EventAction) bool {
	return row.Status == CommunicationV4EventActionDeferred &&
		row.FailureReason == CommunicationV4EventActionFailureDialogueActionOwned
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
