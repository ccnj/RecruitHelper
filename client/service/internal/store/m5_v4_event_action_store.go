package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/textcanon"

	"gorm.io/gorm"
)

const (
	communicationV4EventActionIDDomain = "communication-v4-event-action-v1|"

	CommunicationV4EventActionFailureFixedPhraseUnavailable = "fixedPhraseUnavailable"
	// notificationChannelDeferred 只存在于 2026-07-28 运营通知 webhook 裁决
	// 之前物化的存量历史行（当时尚无任何通知渠道）。这些行是不可变事件层
	// 记录，永不补发；新行一律使用 notificationOutboxOwned。
	CommunicationV4EventActionFailureNotificationChannelDeferred = "notificationChannelDeferred"
	// notificationOutboxOwned：运营通知的发送义务由 NotificationOutbox
	// （企微 webhook 发件箱，2026-07-28 裁决）在收编/迁入事务内按 event_key
	// 幂等承接。事件动作行自此只是事件层的不可变记录，不代表待发欠账，
	// 任何渠道都不得再按本行补发。
	CommunicationV4EventActionFailureNotificationOutboxOwned = "notificationOutboxOwned"
	CommunicationV4EventActionFailurePrimitiveUnavailable    = "primitiveUnavailable"
	CommunicationV4EventActionFailureDialogueActionOwned     = "dialogueActionOwned"
	CommunicationV4EventActionFailureRunnerUnavailable       = "automaticRunnerUnavailable"
	CommunicationV4EventActionFailureBindingUnavailable      = "automaticBindingUnavailable"
	CommunicationV4EventActionFailureDependencyUnavailable   = "automaticDependencyUnavailable"
	CommunicationV4EventActionFailureDispatchNotConstructed  = "automaticDispatchNotConstructed"
	CommunicationV4EventActionFailureActionInvalid           = "automaticActionInvalid"
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
	// receiptOrdinal 是回执在其固定话术气泡序列中的 1-based 位置,决定这一行
	// 取 Messages 的哪一项;非回执行为 0。
	receiptOrdinal int
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
	var salutation string
	var fixedPhrasesReady bool
	if communicationV4EventActionsNeedFixedPhrases(skeletons) {
		fixedPhrases, contextRevisionHash, salutation, fixedPhrasesReady, err =
			communicationV4FixedPhrasesForProfileTx(tx, application.ProfileID)
		if err != nil {
			return nil, false, err
		}
	}
	interviewTime, interviewMethod, err := communicationV4InterviewTimeTextTx(
		tx, application.ProfileID, skeletons,
	)
	if err != nil {
		return nil, false, err
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
		if row.V4Kind == communication.V4ActionAcceptWechat {
			fingerprint, err := communicationV4AcceptWechatFingerprintTx(
				tx,
				row.ProfileID,
				row.CardMessageSeq,
			)
			if err != nil {
				return nil, false, err
			}
			row.ContentHash = fingerprint
		}
		if skeleton.dialogueOwned {
			row.Status = CommunicationV4EventActionDeferred
			row.FailureReason = CommunicationV4EventActionFailureDialogueActionOwned
		} else {
			materializeCommunicationV4EventActionDisposition(
				&row,
				fixedPhrases,
				contextRevisionHash,
				salutation,
				interviewTime,
				interviewMethod,
				skeleton.receiptOrdinal,
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

// PlannedCommunicationV4EventActionsForAccount returns only effect work that
// is still eligible for automatic dispatch. Ordering is stable across rounds
// and keeps actions from one immutable source in reducer order.
func (s *Store) PlannedCommunicationV4EventActionsForAccount(
	key AccountKey,
) ([]CommunicationV4EventAction, error) {
	key.Platform = strings.TrimSpace(key.Platform)
	key.AccountRef = strings.TrimSpace(key.AccountRef)
	if key.Platform == "" || key.AccountRef == "" {
		return nil, ErrCommunicationV4EventActionInvalid
	}
	var actions []CommunicationV4EventAction
	err := s.db.
		Table("communication_v4_event_actions AS action").
		Select("action.*").
		Joins("JOIN candidate_profiles AS profile ON profile.profile_id = action.profile_id").
		Joins("JOIN communication_v4_aggregates AS aggregate ON aggregate.profile_id = action.profile_id").
		Where(
			"profile.platform = ? AND profile.account_ref = ? AND action.status = ? AND aggregate.automation_status = ?",
			key.Platform,
			key.AccountRef,
			CommunicationV4EventActionPlanned,
			ProfileCommunicationAutomationActive,
		).
		Where(
			// 自动重试行(§8.4)的 source_input_key 带 |try{n} 后缀,剥后缀
			// 归位到原计划,保证"计划已被更新 occurrence 取代"的失效判定
			// 同样覆盖重试行,不给过期计划的重试留旁路。
			`action.source_input_kind <> ? OR NOT EXISTS (
				SELECT 1
				FROM communication_v4_schedule_occurrences AS occurrence
				JOIN communication_v4_schedule_plans AS schedule_plan
					ON schedule_plan.plan_id = CASE
						WHEN instr(action.source_input_key, '|try') > 0
						THEN substr(action.source_input_key, 1, instr(action.source_input_key, '|try') - 1)
						ELSE action.source_input_key
					END
				WHERE occurrence.profile_id = action.profile_id
					AND occurrence.status = ?
					AND occurrence.basis_revision >= schedule_plan.basis_revision
			)`,
			CommunicationV4InputSchedulePlan,
			CommunicationV4ScheduleOccurrenceApplied,
		).
		Order("action.planned_at, action.profile_id, action.source_input_kind, action.source_input_key, action.source_ordinal, action.action_id").
		Scan(&actions).Error
	return actions, err
}

// CommunicationV4EventActionsNeedingProfileManualForAccount exposes the one
// materialization-level manual outcome that intentionally does not mutate the
// aggregate. Patrol consumes it by isolating that profile; deferred and
// effect-linked terminal rows are not part of this seam.
func (s *Store) CommunicationV4EventActionsNeedingProfileManualForAccount(
	key AccountKey,
) ([]CommunicationV4EventAction, error) {
	key.Platform = strings.TrimSpace(key.Platform)
	key.AccountRef = strings.TrimSpace(key.AccountRef)
	if key.Platform == "" || key.AccountRef == "" {
		return nil, ErrCommunicationV4EventActionInvalid
	}
	var actions []CommunicationV4EventAction
	err := s.db.
		Table("communication_v4_event_actions AS action").
		Select("action.*").
		Joins("JOIN candidate_profiles AS profile ON profile.profile_id = action.profile_id").
		Joins("JOIN communication_v4_aggregates AS aggregate ON aggregate.profile_id = action.profile_id").
		Where(
			"profile.platform = ? AND profile.account_ref = ? AND action.status = ? AND action.failure_reason = ? AND aggregate.automation_status = ?",
			key.Platform,
			key.AccountRef,
			CommunicationV4EventActionManualRequired,
			CommunicationV4EventActionFailureFixedPhraseUnavailable,
			ProfileCommunicationAutomationActive,
		).
		Order("action.profile_id, action.planned_at, action.source_input_kind, action.source_input_key, action.source_ordinal, action.action_id").
		Scan(&actions).Error
	return actions, err
}

func (s *Store) CommunicationV4EventActionByID(
	actionID string,
) (*CommunicationV4EventAction, error) {
	actionID = strings.TrimSpace(actionID)
	if actionID == "" {
		return nil, ErrCommunicationV4EventActionInvalid
	}
	var action CommunicationV4EventAction
	err := s.db.First(&action, "action_id = ?", actionID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &action, nil
}

// MarkCommunicationV4EventActionManualRequired closes only work for which no
// WAL exists. Effect-linked rows remain exclusively owned by the persistent
// recovery rail.
func (s *Store) MarkCommunicationV4EventActionManualRequired(
	actionID string,
	reason string,
	at time.Time,
) error {
	actionID = strings.TrimSpace(actionID)
	reason = strings.TrimSpace(reason)
	if actionID == "" || !communicationV4EventActionPreWALFailureReason(reason) {
		return ErrCommunicationV4EventActionInvalid
	}
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()
	return s.db.Transaction(func(tx *gorm.DB) error {
		var action CommunicationV4EventAction
		if err := tx.First(&action, "action_id = ?", actionID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCommunicationV4EventActionMissing
			}
			return err
		}
		if action.Status == CommunicationV4EventActionEffectPending ||
			action.Status == CommunicationV4EventActionSent ||
			action.Status == CommunicationV4EventActionManualRequired ||
			action.Status == CommunicationV4EventActionDeferred ||
			action.EffectIntentID != nil {
			return nil
		}
		if action.Status != CommunicationV4EventActionPlanned ||
			action.EffectStartedAt != nil ||
			action.SentAt != nil ||
			action.FailureReason != "" {
			return ErrCommunicationV4EventActionConflict
		}
		updated := tx.Model(&CommunicationV4EventAction{}).
			Where(
				"action_id = ? AND status = ? AND effect_intent_id IS NULL",
				action.ActionID,
				CommunicationV4EventActionPlanned,
			).
			Updates(map[string]any{
				"status":         CommunicationV4EventActionManualRequired,
				"failure_reason": reason,
				"updated_at":     at,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrCommunicationV4EventActionConflict
		}
		aggregate, err := communicationV4AggregateTx(tx, action.ProfileID)
		if err != nil {
			return err
		}
		if aggregate.AutomationStatus == ProfileCommunicationAutomationManualRequired {
			return nil
		}
		return markCommunicationV4AutomationManualTx(
			tx,
			action.ProfileID,
			reason,
			at,
		)
	})
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
			skeletons[index].receiptOrdinal = len(receiptIDs)
		}
	}
	// 同一轮仍然只允许一种回执:多个回执骨架只能是同一条固定话术展开出来的
	// 多个气泡。两种不同回执同轮出现是另一个场景,沿用原有的保守拒绝。
	for _, index := range receiptIndexes {
		if skeletons[index].v4Kind != skeletons[receiptIndexes[0]].v4Kind {
			return nil, ErrCommunicationV4EventActionConflict
		}
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
	// 一条固定话术可以配成多个气泡,reducer 已按 Messages 展开成多个同类回执。
	// 它们必须严格逐条推进:第 n 个气泡挂在第 n-1 个之后,换微信卡片挂在最后
	// 一个气泡之后,任一前项未取得正证时后项都不构造(AGENTS.md 冒烟剖面一)。
	for position, index := range receiptIndexes {
		if position == 0 {
			continue
		}
		previous := receiptIndexes[position-1]
		dependency := receiptIDs[position-1]
		if skeletons[previous].dialogueOwned {
			dependency = skeletons[previous].dialogueOwnerID
		}
		skeletons[index].dependsOnActionID = &dependency
	}
	if len(receiptIDs) > 0 {
		last := len(receiptIDs) - 1
		dependency := receiptIDs[last]
		if skeletons[receiptIndexes[last]].dialogueOwned {
			dependency = skeletons[receiptIndexes[last]].dialogueOwnerID
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
		!communicationActionMatchesV4Plan(action, plan, action.Text, nil) ||
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
		CommunicationActionSuperseded,
		// retried 是干净失败自动重试(§8.4)后代持方原尝试的留档终态;冻结轮
		// 重放期间对话侧可能正处重试窗口,回执代持关系本身不变。
		CommunicationActionRetried:
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
	case communication.V4ActionWechatReceipt,
		communication.V4ActionInterviewAcceptedReceipt,
		communication.V4ActionColdPrompt,
		communication.V4ActionColdWechatText,
		communication.V4ActionInterviewFollowup:
		return CommunicationV4EventEffectReplyText, nil
	case communication.V4ActionInviteWechat,
		communication.V4ActionColdWechatInvite:
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
) (communication.V4FixedPhraseView, string, string, bool, error) {
	var profile CandidateProfile
	err := tx.First(&profile, "profile_id = ?", profileID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return communication.V4FixedPhraseView{}, "", "", false, nil
	}
	if err != nil {
		return communication.V4FixedPhraseView{}, "", "", false, err
	}
	revision, ready, err := currentCommunicationJobAIContextTx(tx, profile)
	if err != nil {
		return communication.V4FixedPhraseView{}, "", "", false, err
	}
	if !ready {
		return communication.V4FixedPhraseView{}, "", "", false, nil
	}
	view, err := communication.BuildV4FixedPhraseView(revision.SourcePackage)
	if err != nil {
		return communication.V4FixedPhraseView{}, revision.RevisionHash, "", false, nil
	}
	salutation, err := communicationV4ProfileSalutationTx(tx, profile)
	if err != nil {
		return communication.V4FixedPhraseView{}, "", "", false, err
	}
	return view, revision.RevisionHash, salutation, true, nil
}

// communicationV4InterviewTimeTextTx resolves the {面试时间} value for an
// interview-accepted receipt. The accepted card the reducer fired on carries
// no schedule of its own — the time lives on the invite card we sent earlier —
// so this walks back to the newest invite at or before the accepted card.
// An absent or unreadable time is not a failure: the caller renders an empty
// value, the placeholder drops out, and the phrase still goes.
// communicationV4InterviewTimeTextTx 返回本次面试的候选人可见时间文本与
// 平台无关的面试类型。两者取自同一张邀面卡:平台一个候选人终身只有一张,
// 所以不存在"时间取这张、类型取那张"的错配。类型为空表示该卡没有类型
// 投影(线下能力上线前发出的卡与历史未映射数据),由调用方按线上处理。
func communicationV4InterviewTimeTextTx(
	tx *gorm.DB,
	profileID string,
	skeletons []communicationV4EventActionSkeleton,
) (string, string, error) {
	cardMessageSeq := int64(0)
	for _, skeleton := range skeletons {
		if skeleton.dialogueOwned ||
			skeleton.v4Kind != communication.V4ActionInterviewAcceptedReceipt {
			continue
		}
		if skeleton.cardMessageSeq > cardMessageSeq {
			cardMessageSeq = skeleton.cardMessageSeq
		}
	}
	if cardMessageSeq <= 0 {
		return "", "", nil
	}
	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", profileID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", nil
		}
		return "", "", err
	}
	if profile.ConversationRef == nil || strings.TrimSpace(*profile.ConversationRef) == "" {
		return "", "", nil
	}
	var invite Message
	err := tx.Where(
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq <= ?",
		profile.Platform, profile.AccountRef, *profile.ConversationRef, cardMessageSeq,
	).Where(
		"kind = ? AND card_type = ? AND interview_starts_at_ms IS NOT NULL",
		"card", "interviewInvite",
	).Order("seq DESC").First(&invite).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	method := ""
	if invite.InterviewMethod != nil {
		method = strings.TrimSpace(*invite.InterviewMethod)
	}
	return formatV4InterviewTime(*invite.InterviewStartsAtMs), method, nil
}

// formatV4InterviewTime renders the candidate-visible form chosen by 甲方 on
// 2026-07-30: "7月31日 10:00" in the client's local zone. No year — every
// interview in scope is days away — and no relative wording, which would go
// stale between planning and sending.
func formatV4InterviewTime(startsAtMs int64) string {
	if startsAtMs <= 0 {
		return ""
	}
	at := time.UnixMilli(startsAtMs).Local()
	return fmt.Sprintf("%d月%d日 %02d:%02d", int(at.Month()), at.Day(), at.Hour(), at.Minute())
}

func communicationV4ProfileSalutationTx(
	tx *gorm.DB,
	profile CandidateProfile,
) (string, error) {
	var candidate Candidate
	err := tx.First(
		&candidate,
		"platform = ? AND platform_user_ref = ?",
		profile.Platform,
		profile.PlatformUserRef,
	).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if candidate.DisplayName == nil {
		return "", nil
	}
	return strings.TrimSpace(*candidate.DisplayName), nil
}

func materializeCommunicationV4EventActionDisposition(
	row *CommunicationV4EventAction,
	fixedPhrases communication.V4FixedPhraseView,
	contextRevisionHash string,
	salutation string,
	interviewTime string,
	interviewMethod string,
	receiptOrdinal int,
	fixedPhrasesReady bool,
) {
	switch row.V4Kind {
	case communication.V4ActionWechatReceipt, communication.V4ActionInterviewAcceptedReceipt:
		phraseKind := communication.V4PhraseWechatReceipt
		if row.V4Kind == communication.V4ActionInterviewAcceptedReceipt {
			phraseKind = communication.V4InterviewAcceptedPhraseKind(interviewMethod)
		}
		phrase := fixedPhrases.Phrase(phraseKind)
		// 气泡序号来自 reducer 冻结的展开结果;若当前配置的气泡数比冻结时少,
		// 这一项就没有内容可发,只能停,不能改发别的气泡。
		if !fixedPhrasesReady || phrase.State != communication.V4PhraseAvailable ||
			receiptOrdinal < 1 || receiptOrdinal > len(phrase.Messages) {
			row.Status = CommunicationV4EventActionManualRequired
			row.FailureReason = CommunicationV4EventActionFailureFixedPhraseUnavailable
			row.ContextRevisionHash = contextRevisionHash
			return
		}
		rendered, err := communication.RenderV4FixedPhrase(
			phrase.Messages[receiptOrdinal-1],
			communication.V4FixedPhraseRenderInput{
				Salutation:    salutation,
				InterviewTime: interviewTime,
			},
		)
		if err != nil {
			row.Status = CommunicationV4EventActionManualRequired
			row.FailureReason = CommunicationV4EventActionFailureFixedPhraseUnavailable
			row.ContextRevisionHash = contextRevisionHash
			return
		}
		row.Status = CommunicationV4EventActionPlanned
		row.Text = rendered
		row.ContentHash = textcanon.Hash(rendered)
		row.ContextRevisionHash = contextRevisionHash
	case communication.V4ActionInviteWechat:
		row.Status = CommunicationV4EventActionPlanned
		row.ContentHash = communicationWechatInviteContentHash()
	case communication.V4ActionNotifyWechat, communication.V4ActionNotifyInterviewAccepted:
		// 发送义务在 NotificationOutbox（收编/迁入事务内 event_key 幂等入队，
		// 见 enqueueNotificationTx 调用点），本行只保留事件层不可变记录。
		row.Status = CommunicationV4EventActionDeferred
		row.FailureReason = CommunicationV4EventActionFailureNotificationOutboxOwned
	case communication.V4ActionAcceptWechat:
		row.Status = CommunicationV4EventActionPlanned
	}
}

func communicationV4AcceptWechatFingerprintTx(
	tx *gorm.DB,
	profileID string,
	cardMessageSeq int64,
) (string, error) {
	if tx == nil || strings.TrimSpace(profileID) == "" || cardMessageSeq <= 0 {
		return "", ErrCommunicationV4EventActionConflict
	}
	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", profileID).Error; err != nil {
		return "", err
	}
	if profile.ConversationRef == nil ||
		strings.TrimSpace(*profile.ConversationRef) == "" {
		return "", ErrCommunicationV4EventActionConflict
	}
	var message Message
	if err := tx.First(
		&message,
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ?",
		profile.Platform,
		profile.AccountRef,
		*profile.ConversationRef,
		cardMessageSeq,
	).Error; err != nil {
		return "", err
	}
	if message.RetractedAt != nil ||
		message.Direction != "in" ||
		message.Kind != "card" ||
		message.CardType != "wechatExchange" ||
		message.CardState != "pending" ||
		message.SourceKey == nil {
		return "", ErrCommunicationV4EventActionConflict
	}
	return AcceptWechatFingerprint(*message.SourceKey)
}

// CommunicationV4AcceptWechatRequestSource returns the private request anchor
// for one persisted accept action. Callers may use it only to construct the
// typed command args; the WAL transaction repeats this exact lookup.
func (s *Store) CommunicationV4AcceptWechatRequestSource(
	actionID string,
) (string, error) {
	actionID = strings.TrimSpace(actionID)
	if actionID == "" {
		return "", ErrCommunicationV4EventActionInvalid
	}
	var sourceKey string
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var action CommunicationV4EventAction
		if err := tx.First(&action, "action_id = ?", actionID).Error; err != nil {
			return err
		}
		var profile CandidateProfile
		if err := tx.First(&profile, "profile_id = ?", action.ProfileID).Error; err != nil {
			return err
		}
		if profile.ConversationRef == nil {
			return ErrCommunicationV4EventActionConflict
		}
		var err error
		sourceKey, err = communicationV4AcceptWechatRequestSourceTx(
			tx,
			action,
			*profile.ConversationRef,
		)
		return err
	})
	return sourceKey, err
}

func communicationV4AcceptWechatRequestSourceTx(
	tx *gorm.DB,
	action CommunicationV4EventAction,
	conversationRef string,
) (string, error) {
	if tx == nil ||
		action.V4Kind != communication.V4ActionAcceptWechat ||
		action.EffectKind != CommunicationV4EventEffectAcceptWechat ||
		action.CardMessageSeq <= 0 ||
		strings.TrimSpace(conversationRef) == "" {
		return "", ErrCommunicationV4EventActionConflict
	}
	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", action.ProfileID).Error; err != nil {
		return "", err
	}
	if profile.ConversationRef == nil ||
		*profile.ConversationRef != conversationRef {
		return "", ErrCommunicationV4EventActionConflict
	}
	var message Message
	if err := tx.First(
		&message,
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ?",
		profile.Platform,
		profile.AccountRef,
		conversationRef,
		action.CardMessageSeq,
	).Error; err != nil {
		return "", err
	}
	if message.RetractedAt != nil ||
		message.Direction != "in" ||
		message.Kind != "card" ||
		message.CardType != "wechatExchange" ||
		message.CardState != "pending" ||
		message.SourceKey == nil {
		return "", ErrCommunicationV4EventActionConflict
	}
	fingerprint, err := AcceptWechatFingerprint(*message.SourceKey)
	if err != nil || action.ContentHash != fingerprint {
		return "", ErrCommunicationV4EventActionConflict
	}
	return *message.SourceKey, nil
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
	case communication.V4ActionWechatReceipt,
		communication.V4ActionInterviewAcceptedReceipt,
		communication.V4ActionColdPrompt,
		communication.V4ActionColdWechatText,
		communication.V4ActionInterviewFollowup:
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
			if communicationV4EventActionPreWALFailureReason(row.FailureReason) {
				return row.Text != "" &&
					row.ContentHash == textcanon.Hash(row.Text) &&
					row.ContextRevisionHash != "" &&
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
		case CommunicationV4EventActionRetried:
			// 干净失败自动重试(§8.4)的原行留档终态:内容字段原封,effect
			// 三件套与 manualRequired 同形(绑过 intent、无 sent)。
			return row.Text != "" &&
				row.ContentHash == textcanon.Hash(row.Text) &&
				row.ContextRevisionHash != "" &&
				row.FailureReason == "effectFailed" &&
				validCommunicationV4EventActionEffectFields(row)
		default:
			return false
		}
	case communication.V4ActionInviteWechat, communication.V4ActionColdWechatInvite:
		expectedContextHash := ""
		if row.V4Kind == communication.V4ActionColdWechatInvite &&
			row.SourceInputKind == CommunicationV4InputSchedulePlan {
			expectedContextHash = row.ContextRevisionHash
			if expectedContextHash == "" {
				return false
			}
		}
		if row.Status == CommunicationV4EventActionRetried {
			return row.FailureReason == "effectFailed" &&
				row.Text == "" &&
				row.ContentHash == communicationWechatInviteContentHash() &&
				row.ContextRevisionHash == expectedContextHash &&
				validCommunicationV4EventActionEffectFields(row)
		}
		if row.Status != CommunicationV4EventActionPlanned &&
			row.Status != CommunicationV4EventActionEffectPending &&
			row.Status != CommunicationV4EventActionSent &&
			row.Status != CommunicationV4EventActionManualRequired {
			return false
		}
		if row.Status == CommunicationV4EventActionManualRequired &&
			communicationV4EventActionPreWALFailureReason(row.FailureReason) {
			return row.Text == "" &&
				row.ContentHash == communicationWechatInviteContentHash() &&
				row.ContextRevisionHash == expectedContextHash &&
				row.EffectIntentID == nil &&
				row.EffectStartedAt == nil &&
				row.SentAt == nil
		}
		return (row.Status != CommunicationV4EventActionManualRequired ||
			validCommunicationV4EventActionFailureReason(row.FailureReason)) &&
			row.Text == "" &&
			row.ContentHash == communicationWechatInviteContentHash() &&
			row.ContextRevisionHash == expectedContextHash &&
			(row.Status == CommunicationV4EventActionManualRequired ||
				row.FailureReason == "") &&
			validCommunicationV4EventActionEffectFields(row)
	case communication.V4ActionNotifyWechat, communication.V4ActionNotifyInterviewAccepted:
		// notificationChannelDeferred 是 webhook 裁决前的存量历史形态，重放
		// 时继续可读；新建行一律 notificationOutboxOwned。
		return row.Status == CommunicationV4EventActionDeferred &&
			(row.FailureReason == CommunicationV4EventActionFailureNotificationOutboxOwned ||
				row.FailureReason == CommunicationV4EventActionFailureNotificationChannelDeferred) &&
			validCommunicationV4EventActionEffectFields(row)
	case communication.V4ActionAcceptWechat:
		// Before chat.acceptWechat@1 existed, materialization persisted this
		// explicit deferred fact. It remains readable for immutable replay but
		// PlannedCommunicationV4EventActionsForAccount never revives it.
		if row.Status == CommunicationV4EventActionDeferred {
			return row.FailureReason ==
				CommunicationV4EventActionFailurePrimitiveUnavailable &&
				row.Text == "" &&
				row.ContentHash == "" &&
				row.ContextRevisionHash == "" &&
				validCommunicationV4EventActionEffectFields(row)
		}
		if row.Status == CommunicationV4EventActionRetried {
			return row.FailureReason == "effectFailed" &&
				row.Text == "" &&
				validMessageSourceKey(row.ContentHash) &&
				row.ContextRevisionHash == "" &&
				validCommunicationV4EventActionEffectFields(row)
		}
		if row.Status != CommunicationV4EventActionPlanned &&
			row.Status != CommunicationV4EventActionEffectPending &&
			row.Status != CommunicationV4EventActionSent &&
			row.Status != CommunicationV4EventActionManualRequired {
			return false
		}
		if row.Status == CommunicationV4EventActionManualRequired &&
			communicationV4EventActionPreWALFailureReason(row.FailureReason) {
			return row.Text == "" &&
				validMessageSourceKey(row.ContentHash) &&
				row.ContextRevisionHash == "" &&
				row.EffectIntentID == nil &&
				row.EffectStartedAt == nil &&
				row.SentAt == nil
		}
		return (row.Status != CommunicationV4EventActionManualRequired ||
			validCommunicationV4EventActionFailureReason(row.FailureReason)) &&
			row.Text == "" &&
			validMessageSourceKey(row.ContentHash) &&
			row.ContextRevisionHash == "" &&
			(row.Status == CommunicationV4EventActionManualRequired ||
				row.FailureReason == "") &&
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
	case CommunicationV4EventActionRetried:
		// 原失败尝试留档:绑定过 intent、从未 sent,与 manualRequired 同形。
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

func communicationV4EventActionPreWALFailureReason(reason string) bool {
	switch reason {
	case CommunicationV4EventActionFailureRunnerUnavailable,
		CommunicationV4EventActionFailureBindingUnavailable,
		CommunicationV4EventActionFailureDependencyUnavailable,
		CommunicationV4EventActionFailureDispatchNotConstructed,
		CommunicationV4EventActionFailureActionInvalid,
		// stalePlannedSuperseded 是 Q1/Q2 裁决(2026-08-02)"未派发 planned
		// 残留一律作废"的显式终局原因;它只落在从未绑定发送意图的行上,
		// 形状与其余 pre-WAL 终局完全一致。
		CommunicationStalePlannedSuperseded:
		return true
	default:
		return false
	}
}

func communicationV4EventActionDialogueOwned(row CommunicationV4EventAction) bool {
	return row.Status == CommunicationV4EventActionDeferred &&
		row.FailureReason == CommunicationV4EventActionFailureDialogueActionOwned
}

// communicationV4EventActionRetrySuffixConsistent 要求重试事件动作行的
// SemanticActionKey 与 SourceInputKey 携带完全相同的 |try{n} 后缀(基础行则
// 两者都不带)。不一致的行是坏账本,一律拒绝参与任何授权判定。
func communicationV4EventActionRetrySuffixConsistent(
	row CommunicationV4EventAction,
) bool {
	semanticBase := communicationActionPlanKey(row.SemanticActionKey)
	sourceBase := communicationActionPlanKey(row.SourceInputKey)
	return row.SemanticActionKey[len(semanticBase):] ==
		row.SourceInputKey[len(sourceBase):]
}

// latestCommunicationV4EventActionAttemptTx 沿 |try{n} 链取同一基础事件动作
// 的最新一代尝试行。输入可以是任意一代;不存在更晚尝试时返回输入行本身。
// 链上每一跳都要求 ProfileID 与语义键严格匹配,防止哈希碰撞或脏行冒充。
func latestCommunicationV4EventActionAttemptTx(
	tx *gorm.DB,
	row CommunicationV4EventAction,
) (CommunicationV4EventAction, error) {
	current := row
	for {
		nextKey := communicationActionNextRetryID(current.SemanticActionKey)
		nextID, err := CommunicationV4EventActionID(current.ProfileID, nextKey)
		if err != nil {
			return CommunicationV4EventAction{}, err
		}
		var next CommunicationV4EventAction
		err = tx.First(&next, "action_id = ?", nextID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return current, nil
		}
		if err != nil {
			return CommunicationV4EventAction{}, err
		}
		if next.ProfileID != current.ProfileID ||
			next.SemanticActionKey != nextKey {
			return CommunicationV4EventAction{}, ErrCommunicationV4EventActionConflict
		}
		current = next
	}
}

// CommunicationV4EventActionLatestAttempt 是巡检依赖解析用的只读入口:给定
// 任意一代事件动作 ID,返回其重试链上的最新一代尝试行。
func (s *Store) CommunicationV4EventActionLatestAttempt(
	actionID string,
) (*CommunicationV4EventAction, error) {
	actionID = strings.TrimSpace(actionID)
	if actionID == "" {
		return nil, ErrCommunicationV4EventActionInvalid
	}
	var latest *CommunicationV4EventAction
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var row CommunicationV4EventAction
		if err := tx.First(&row, "action_id = ?", actionID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCommunicationV4EventActionMissing
			}
			return err
		}
		walked, err := latestCommunicationV4EventActionAttemptTx(tx, row)
		if err != nil {
			return err
		}
		latest = &walked
		return nil
	})
	if err != nil {
		return nil, err
	}
	return latest, nil
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
