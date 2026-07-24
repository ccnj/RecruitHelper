package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"gorm.io/gorm"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/textcanon"
	"recruithelper/contract/gen/go/protocol"
)

var ErrM5B177IncidentRecoveryUnsafe = errors.New("M5-B 智联 177 事故恢复前置事实不完整")

const (
	m5B177RecoveryAuditCategory       = "m5b_177_incident_recovery"
	m5B177FreshProofMaxAge            = 10 * time.Minute
	m5B177MessageSeq            int64 = 2
	m5B177CanonicalInputKey           = "message:2"
	m5B177ArchivedInputKey            = "archivedSystemNotice/message:2"
	m5B177ResumeText                  = "您好，这是我的附件简历，请查收"
)

type M5B177IncidentRecoveryResult struct {
	Applied                   bool
	AlreadyApplied            bool
	FreshTailUnique           bool
	ApplicationKeyArchived    bool
	ProjectedThroughSeqBefore int64
	ProjectedThroughSeqAfter  int64
}

type m5B177FreshProof struct {
	key        ConversationKey
	data       protocol.ChatReadThreadData
	terminalAt time.Time
}

// RecoverM5B177Incident applies the one approved in-place correction for the
// live type-177 attachment-resume row. The only caller input is a persisted
// fresh chat.readThread msgId. Platform identities and candidate text never
// enter command output, audit detail, logs, or an HTTP surface.
func (s *Store) RecoverM5B177Incident(
	freshReadMsgID string,
) (*M5B177IncidentRecoveryResult, error) {
	if strings.TrimSpace(freshReadMsgID) == "" {
		return nil, ErrM5B177IncidentRecoveryUnsafe
	}
	now := time.Now().UTC()
	out := &M5B177IncidentRecoveryResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		proof, err := loadM5B177FreshProofTx(tx, freshReadMsgID)
		if err != nil {
			return err
		}

		account, conversation, profile, old, aggregate, canonical, archived, err :=
			loadM5B177IncidentStateTx(tx, proof)
		if err != nil {
			return err
		}
		if account.StoppedAt == nil || account.PausedReason != "userPaused" {
			return ErrM5B177IncidentRecoveryUnsafe
		}
		if err := requireM5B177NoDownstreamFactsTx(tx, proof.key, profile, old[0]); err != nil {
			return err
		}
		if err := requireM5B177FreshTail(proof.data, old[1]); err != nil {
			return err
		}
		out.FreshTailUnique = true

		audits, err := m5B177RecoveryAuditsTx(tx, proof.key)
		if err != nil {
			return err
		}
		initial := m5B177InitialState(
			conversation, profile, old, aggregate, canonical, archived, audits,
		)
		applied := m5B177AppliedState(
			conversation, profile, old, aggregate, canonical, archived, audits, freshReadMsgID,
		)
		switch {
		case applied:
			out.AlreadyApplied = true
			out.ApplicationKeyArchived = true
			out.ProjectedThroughSeqBefore = m5B177MessageSeq
			out.ProjectedThroughSeqAfter = m5B177MessageSeq - 1
			return nil
		case !initial:
			return ErrM5B177IncidentRecoveryUnsafe
		}
		if proof.terminalAt.After(now) || now.Sub(proof.terminalAt) > m5B177FreshProofMaxAge {
			return ErrM5B177IncidentRecoveryUnsafe
		}

		messageUpdated := tx.Model(&Message{}).
			Where(
				"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ? AND "+
					"direction = ? AND kind = ? AND content_hash = ? AND card_type = ? AND card_state = ? AND "+
					activeMessageCondition,
				proof.key.Platform, proof.key.AccountRef, proof.key.ConversationRef, m5B177MessageSeq,
				"system", "system", old[1].ContentHash, "", "",
			).
			UpdateColumns(map[string]any{
				"direction":    "in",
				"kind":         "card",
				"content_hash": proof.data.Messages[len(proof.data.Messages)-1].ContentHash,
				"card_type":    "resumeAttachment",
				"card_state":   "unknown",
			})
		if messageUpdated.Error != nil {
			return messageUpdated.Error
		}
		if messageUpdated.RowsAffected != 1 {
			return ErrM5B177IncidentRecoveryUnsafe
		}

		conversationUpdated := tx.Model(&Conversation{}).
			Where(conversationWhere(proof.key), conversationArgs(proof.key)...).
			Where(
				"last_message_seq = ? AND last_message_direction = ? AND last_message_kind = ? AND last_message_preview = ?",
				m5B177MessageSeq, "system", "system", m5B177ResumeText,
			).
			UpdateColumns(map[string]any{
				"last_message_direction": "in",
				"last_message_kind":      "card",
				"last_message_preview":   m5B177ResumeText,
			})
		if conversationUpdated.Error != nil {
			return conversationUpdated.Error
		}
		if conversationUpdated.RowsAffected != 1 {
			return ErrM5B177IncidentRecoveryUnsafe
		}

		applicationUpdated := tx.Model(&CommunicationV4ProjectionApplication{}).
			Where(
				"profile_id = ? AND input_kind = ? AND input_key = ? AND semantic_kind = ? AND "+
					"message_seq = ? AND from_revision = 0 AND to_revision = 1",
				profile.ProfileID, CommunicationV4InputBusinessEvent, m5B177CanonicalInputKey,
				communication.EventSystemNotice, m5B177MessageSeq,
			).
			UpdateColumn("input_key", m5B177ArchivedInputKey)
		if applicationUpdated.Error != nil {
			return applicationUpdated.Error
		}
		if applicationUpdated.RowsAffected != 1 {
			return ErrM5B177IncidentRecoveryUnsafe
		}

		aggregateUpdated := tx.Model(&CommunicationV4Aggregate{}).
			Where(
				"profile_id = ? AND revision = 1 AND projected_through_seq = ?",
				profile.ProfileID, m5B177MessageSeq,
			).
			UpdateColumn("projected_through_seq", m5B177MessageSeq-1)
		if aggregateUpdated.Error != nil {
			return aggregateUpdated.Error
		}
		if aggregateUpdated.RowsAffected != 1 {
			return ErrM5B177IncidentRecoveryUnsafe
		}

		if err := tx.Create(&AuditEntry{
			At: now, Category: m5B177RecoveryAuditCategory,
			Platform: proof.key.Platform, AccountRef: proof.key.AccountRef,
			ConversationRef: proof.key.ConversationRef, RefMsgID: freshReadMsgID,
			RoundID: old[1].FirstSeenRoundID, Detail: m5B177RecoveryAuditDetail(),
		}).Error; err != nil {
			return err
		}

		_, afterConversation, afterProfile, afterMessages, afterAggregate,
			afterCanonical, afterArchived, err := loadM5B177IncidentStateTx(tx, proof)
		if err != nil {
			return err
		}
		afterAudits, err := m5B177RecoveryAuditsTx(tx, proof.key)
		if err != nil {
			return err
		}
		if !m5B177AppliedState(
			afterConversation, afterProfile, afterMessages, afterAggregate,
			afterCanonical, afterArchived, afterAudits, freshReadMsgID,
		) {
			return ErrM5B177IncidentRecoveryUnsafe
		}

		out.Applied = true
		out.ApplicationKeyArchived = true
		out.ProjectedThroughSeqBefore = m5B177MessageSeq
		out.ProjectedThroughSeqAfter = m5B177MessageSeq - 1
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func loadM5B177FreshProofTx(
	tx *gorm.DB,
	freshReadMsgID string,
) (m5B177FreshProof, error) {
	var selected CmdRecord
	if err := tx.First(&selected, "msg_id = ?", freshReadMsgID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return m5B177FreshProof{}, ErrM5B177IncidentRecoveryUnsafe
		}
		return m5B177FreshProof{}, err
	}
	var records []CmdRecord
	if err := tx.Where("logical_dispatch_id = ?", selected.LogicalDispatchID).
		Order("lineage_depth, created_at, msg_id").Find(&records).Error; err != nil {
		return m5B177FreshProof{}, err
	}
	leaf, err := validateLineage(records)
	if err != nil || leaf.MsgID != freshReadMsgID || leaf.Status != CmdOk || leaf.TerminalAt == nil ||
		leaf.Name != protocol.PrimChatReadThread || leaf.Class != string(protocol.ClassIntrusive) ||
		leaf.Platform != "zhilian" || strings.TrimSpace(leaf.AccountRef) == "" || leaf.ResultBody == "" {
		return m5B177FreshProof{}, ErrM5B177IncidentRecoveryUnsafe
	}
	for index := range records {
		record := records[index]
		if record.Name != leaf.Name || record.Class != leaf.Class || record.Platform != leaf.Platform ||
			record.AccountRef != leaf.AccountRef || record.Args != leaf.Args {
			return m5B177FreshProof{}, ErrM5B177IncidentRecoveryUnsafe
		}
	}

	meta, ok := protocol.Primitives[protocol.PrimChatReadThread]
	if !ok || meta.Ver == 0 ||
		protocol.ValidatePrimitiveArgs(leaf.Name, meta.Ver, json.RawMessage(leaf.Args)) != nil ||
		protocol.ValidatePrimitiveResult(leaf.Name, meta.Ver, json.RawMessage(leaf.ResultBody)) != nil {
		return m5B177FreshProof{}, ErrM5B177IncidentRecoveryUnsafe
	}
	var args protocol.ChatReadThreadArgs
	var result protocol.ResultBody
	if json.Unmarshal([]byte(leaf.Args), &args) != nil ||
		json.Unmarshal([]byte(leaf.ResultBody), &result) != nil ||
		strings.TrimSpace(args.ConversationRef) == "" || args.Cursor != "" || !args.RequireCurrent ||
		result.Ref != leaf.MsgID || result.Status != protocol.ResultStatusOk || result.DataBlobRef != nil {
		return m5B177FreshProof{}, ErrM5B177IncidentRecoveryUnsafe
	}
	var data protocol.ChatReadThreadData
	if json.Unmarshal(result.Data, &data) != nil || !data.Complete || !data.ReachedTop ||
		len(data.Messages) == 0 {
		return m5B177FreshProof{}, ErrM5B177IncidentRecoveryUnsafe
	}
	for index := range data.Messages {
		if data.Messages[index].Idx != index {
			return m5B177FreshProof{}, ErrM5B177IncidentRecoveryUnsafe
		}
	}
	return m5B177FreshProof{
		key: ConversationKey{
			Platform: leaf.Platform, AccountRef: leaf.AccountRef, ConversationRef: args.ConversationRef,
		},
		data: data, terminalAt: leaf.TerminalAt.UTC(),
	}, nil
}

func loadM5B177IncidentStateTx(
	tx *gorm.DB,
	proof m5B177FreshProof,
) (
	Account,
	Conversation,
	CandidateProfile,
	[]Message,
	CommunicationV4Aggregate,
	*CommunicationV4ProjectionApplication,
	*CommunicationV4ProjectionApplication,
	error,
) {
	unsafe := func() (
		Account, Conversation, CandidateProfile, []Message, CommunicationV4Aggregate,
		*CommunicationV4ProjectionApplication, *CommunicationV4ProjectionApplication, error,
	) {
		return Account{}, Conversation{}, CandidateProfile{}, nil, CommunicationV4Aggregate{},
			nil, nil, ErrM5B177IncidentRecoveryUnsafe
	}

	var account Account
	if err := tx.First(
		&account, "platform = ? AND account_ref = ?", proof.key.Platform, proof.key.AccountRef,
	).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return unsafe()
		}
		return Account{}, Conversation{}, CandidateProfile{}, nil, CommunicationV4Aggregate{}, nil, nil, err
	}
	var conversation Conversation
	if err := tx.Where(conversationWhere(proof.key), conversationArgs(proof.key)...).
		First(&conversation).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return unsafe()
		}
		return Account{}, Conversation{}, CandidateProfile{}, nil, CommunicationV4Aggregate{}, nil, nil, err
	}
	var tracked []TrackedIntent
	if err := tx.Where(conversationWhere(proof.key), conversationArgs(proof.key)...).
		Find(&tracked).Error; err != nil {
		return Account{}, Conversation{}, CandidateProfile{}, nil, CommunicationV4Aggregate{}, nil, nil, err
	}
	if len(tracked) != 1 || tracked[0].Status != TrackingAdopted ||
		conversation.TrackingState != TrackingAdopted ||
		conversation.PlatformUserRef == "" {
		return unsafe()
	}
	var profiles []CandidateProfile
	if err := tx.Where(
		"platform = ? AND account_ref = ? AND conversation_ref = ?",
		proof.key.Platform, proof.key.AccountRef, proof.key.ConversationRef,
	).Find(&profiles).Error; err != nil {
		return Account{}, Conversation{}, CandidateProfile{}, nil, CommunicationV4Aggregate{}, nil, nil, err
	}
	if len(profiles) != 1 || profiles[0].PlatformUserRef != conversation.PlatformUserRef {
		return unsafe()
	}
	profile := profiles[0]

	var messages []Message
	if err := tx.Where(conversationWhere(proof.key), conversationArgs(proof.key)...).
		Order("seq").Find(&messages).Error; err != nil {
		return Account{}, Conversation{}, CandidateProfile{}, nil, CommunicationV4Aggregate{}, nil, nil, err
	}
	if len(messages) != 2 || messages[0].Seq != 1 || messages[1].Seq != m5B177MessageSeq {
		return unsafe()
	}
	var aggregate CommunicationV4Aggregate
	if err := tx.First(&aggregate, "profile_id = ?", profile.ProfileID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return unsafe()
		}
		return Account{}, Conversation{}, CandidateProfile{}, nil, CommunicationV4Aggregate{}, nil, nil, err
	}
	var applications []CommunicationV4ProjectionApplication
	if err := tx.Where("profile_id = ?", profile.ProfileID).Order("to_revision").Find(&applications).Error; err != nil {
		return Account{}, Conversation{}, CandidateProfile{}, nil, CommunicationV4Aggregate{}, nil, nil, err
	}
	if len(applications) != 1 {
		return unsafe()
	}
	var canonical, archived *CommunicationV4ProjectionApplication
	switch applications[0].InputKey {
	case m5B177CanonicalInputKey:
		copy := applications[0]
		canonical = &copy
	case m5B177ArchivedInputKey:
		copy := applications[0]
		archived = &copy
	default:
		return unsafe()
	}
	return account, conversation, profile, messages, aggregate, canonical, archived, nil
}

func m5B177InitialState(
	conversation Conversation,
	profile CandidateProfile,
	messages []Message,
	aggregate CommunicationV4Aggregate,
	canonical *CommunicationV4ProjectionApplication,
	archived *CommunicationV4ProjectionApplication,
	audits []AuditEntry,
) bool {
	if len(audits) != 0 || canonical == nil || archived != nil ||
		!m5B177CommonShape(conversation, profile, messages, aggregate, *canonical) {
		return false
	}
	message := messages[1]
	return conversation.LastMessageDirection == "system" &&
		conversation.LastMessageKind == "system" &&
		message.Direction == "system" && message.Kind == "system" &&
		message.ContentHash == textcanon.Hash(m5B177ResumeText) &&
		message.CardType == "" && message.CardState == "" &&
		message.InterviewStartsAtMs == nil && message.InterviewEndsAtMs == nil &&
		message.InterviewMethod == nil &&
		aggregate.ProjectedThroughSeq == m5B177MessageSeq
}

func m5B177AppliedState(
	conversation Conversation,
	profile CandidateProfile,
	messages []Message,
	aggregate CommunicationV4Aggregate,
	canonical *CommunicationV4ProjectionApplication,
	archived *CommunicationV4ProjectionApplication,
	audits []AuditEntry,
	freshReadMsgID string,
) bool {
	if canonical != nil || archived == nil || len(audits) != 1 ||
		!m5B177CommonShape(conversation, profile, messages, aggregate, *archived) {
		return false
	}
	message := messages[1]
	if conversation.LastMessageDirection != "in" || conversation.LastMessageKind != "card" ||
		message.Direction != "in" || message.Kind != "card" ||
		message.CardType != "resumeAttachment" || message.CardState != "unknown" ||
		message.InterviewStartsAtMs != nil || message.InterviewEndsAtMs != nil ||
		message.InterviewMethod != nil || aggregate.ProjectedThroughSeq != m5B177MessageSeq-1 {
		return false
	}
	audit := audits[0]
	return audit.RefMsgID == freshReadMsgID &&
		audit.RoundID == message.FirstSeenRoundID &&
		audit.Detail == m5B177RecoveryAuditDetail()
}

func m5B177CommonShape(
	conversation Conversation,
	profile CandidateProfile,
	messages []Message,
	aggregate CommunicationV4Aggregate,
	application CommunicationV4ProjectionApplication,
) bool {
	if len(messages) != 2 ||
		conversation.AdoptedBoundarySeq != 0 ||
		conversation.LastMessageSeq != m5B177MessageSeq ||
		conversation.LastMessagePreview != m5B177ResumeText ||
		profile.MainStatus != CandidateProfileGreeted || profile.EndReason != nil ||
		profile.SuccessfulGreetingIntentID == nil || profile.GreetedAt == nil ||
		profile.CommunicatingAt != nil || profile.FirstRealMessageSeq != nil ||
		aggregate.ProfileID != profile.ProfileID ||
		aggregate.RootGreetingIntentID != *profile.SuccessfulGreetingIntentID ||
		aggregate.StateSchemaVersion != communicationV4StateSchemaVersion ||
		aggregate.Revision != 1 ||
		aggregate.AutomationStatus != ProfileCommunicationAutomationActive ||
		aggregate.ManualReason != "" || aggregate.ManualRequiredAt != nil ||
		!reflect.DeepEqual(aggregate.State, communication.NewV4GreetedState(profile.GreetedAt)) {
		return false
	}
	greeting, candidate := messages[0], messages[1]
	if greeting.RetractedAt != nil || greeting.RetractionReason != "" ||
		greeting.Direction != "out" || greeting.Kind != "text" || greeting.Origin != "self" ||
		greeting.SourceKey != nil || greeting.OutboundIntentID == nil ||
		greeting.Text == nil || strings.TrimSpace(*greeting.Text) == "" ||
		greeting.BlobRef != "" || greeting.CardType != "" || greeting.CardState != "" ||
		candidate.RetractedAt != nil || candidate.RetractionReason != "" ||
		candidate.Origin != "external" || candidate.OutboundIntentID != nil ||
		candidate.SourceKey == nil || !validMessageSourceKey(*candidate.SourceKey) ||
		candidate.Text == nil || *candidate.Text != m5B177ResumeText ||
		candidate.TsApproxMs == nil || *candidate.TsApproxMs <= 0 ||
		candidate.BlobRef != "" || strings.TrimSpace(candidate.FirstSeenRoundID) == "" {
		return false
	}
	oldEvent, err := communication.NormalizeLedgerMessage(communication.LedgerMessageFact{
		Seq: candidate.Seq, Direction: "system", Kind: "system",
		Text: candidate.Text, Origin: candidate.Origin, TsApproxMs: candidate.TsApproxMs,
	})
	if err != nil {
		return false
	}
	digest, err := communicationV4InputDigest(oldEvent)
	if err != nil {
		return false
	}
	decision, err := communication.ApplyV4BusinessEvent(
		communication.NewV4GreetedState(profile.GreetedAt),
		oldEvent,
	)
	if err != nil {
		return false
	}
	expectedOutcome := CommunicationV4ApplicationOutcome{
		Dialogue: decision.Dialogue, DialogueAfterActions: decision.DialogueAfterActions,
		Actions:      append([]communication.V4EventAction(nil), decision.Actions...),
		ManualReason: decision.ManualReason,
	}
	return application.ProfileID == profile.ProfileID &&
		application.InputKind == CommunicationV4InputBusinessEvent &&
		(application.InputKey == m5B177CanonicalInputKey ||
			application.InputKey == m5B177ArchivedInputKey) &&
		application.InputDigest == digest &&
		application.SemanticKind == string(communication.EventSystemNotice) &&
		application.MessageSeq == m5B177MessageSeq &&
		application.FromRevision == 0 && application.ToRevision == 1 &&
		reflect.DeepEqual(application.Outcome, expectedOutcome)
}

func requireM5B177FreshTail(data protocol.ChatReadThreadData, old Message) error {
	if len(data.Messages) == 0 || old.SourceKey == nil || old.Text == nil || old.TsApproxMs == nil {
		return ErrM5B177IncidentRecoveryUnsafe
	}
	tail := data.Messages[len(data.Messages)-1]
	if tail.Direction != protocol.MessageDirectionIn ||
		tail.Kind != protocol.MessageKindCard ||
		tail.CardType == nil || *tail.CardType != protocol.CardTypeResumeAttachment ||
		tail.CardState == nil || *tail.CardState != protocol.CardStateUnknown ||
		tail.BlobRef != nil || tail.Interview != nil ||
		tail.Text == nil || *tail.Text != m5B177ResumeText ||
		tail.SourceKey != *old.SourceKey ||
		tail.TsApprox == nil || *tail.TsApprox != *old.TsApproxMs ||
		!validMessageSourceKey(tail.ContentHash) {
		return ErrM5B177IncidentRecoveryUnsafe
	}
	matches := 0
	for index := range data.Messages {
		message := data.Messages[index]
		if message.SourceKey == tail.SourceKey {
			matches++
		}
	}
	if matches != 1 {
		return ErrM5B177IncidentRecoveryUnsafe
	}
	return nil
}

func requireM5B177NoDownstreamFactsTx(
	tx *gorm.DB,
	key ConversationKey,
	profile CandidateProfile,
	greeting Message,
) error {
	if greeting.OutboundIntentID == nil ||
		profile.SuccessfulGreetingIntentID == nil ||
		*greeting.OutboundIntentID != *profile.SuccessfulGreetingIntentID {
		return ErrM5B177IncidentRecoveryUnsafe
	}
	var effects []EffectIntent
	if err := tx.Where(
		"intent_id = ? OR (platform = ? AND account_ref = ? AND target_ref IN ?)",
		*profile.SuccessfulGreetingIntentID,
		key.Platform, key.AccountRef, []string{profile.ProfileID, key.ConversationRef},
	).Find(&effects).Error; err != nil {
		return err
	}
	if len(effects) != 1 {
		return ErrM5B177IncidentRecoveryUnsafe
	}
	effect := effects[0]
	if effect.IntentID != *profile.SuccessfulGreetingIntentID ||
		effect.Primitive != primitiveChatSendGreeting ||
		effect.Status != EffectIntentOk ||
		effect.ResultConversationRef == nil ||
		*effect.ResultConversationRef != key.ConversationRef ||
		effect.ResultMessageSeq == nil || *effect.ResultMessageSeq != greeting.Seq ||
		effect.SendFingerprint != greeting.ContentHash {
		return ErrM5B177IncidentRecoveryUnsafe
	}

	var turns, actions, invocations, v4Actions, schedules, contacts, transitions, selections int64
	counts := []struct {
		target *int64
		query  *gorm.DB
	}{
		{&turns, tx.Model(&DialogueTurn{}).Where("profile_id = ?", profile.ProfileID)},
		{&actions, tx.Table("communication_actions AS a").
			Joins("JOIN dialogue_turns AS t ON t.turn_id = a.turn_id").
			Where("t.profile_id = ?", profile.ProfileID)},
		{&invocations, tx.Table("ai_invocations AS i").
			Joins("JOIN dialogue_turns AS t ON t.turn_id = i.turn_id").
			Where("t.profile_id = ?", profile.ProfileID)},
		{&v4Actions, tx.Model(&CommunicationV4EventAction{}).Where("profile_id = ?", profile.ProfileID)},
		{&schedules, tx.Model(&CommunicationV4ScheduleOccurrence{}).Where("profile_id = ?", profile.ProfileID)},
		{&contacts, tx.Model(&ContactAsset{}).Where("profile_id = ?", profile.ProfileID)},
		{&transitions, tx.Model(&CardTransitionFact{}).Where(
			"platform = ? AND account_ref = ? AND conversation_ref = ?",
			key.Platform, key.AccountRef, key.ConversationRef,
		)},
		{&selections, tx.Model(&M5TrialSelection{}).Where("profile_id = ?", profile.ProfileID)},
	}
	for index := range counts {
		if err := counts[index].query.Count(counts[index].target).Error; err != nil {
			return err
		}
		if *counts[index].target != 0 {
			return ErrM5B177IncidentRecoveryUnsafe
		}
	}
	return nil
}

func m5B177RecoveryAuditsTx(
	tx *gorm.DB,
	key ConversationKey,
) ([]AuditEntry, error) {
	var audits []AuditEntry
	err := tx.Where(
		"category = ? AND platform = ? AND account_ref = ? AND conversation_ref = ?",
		m5B177RecoveryAuditCategory, key.Platform, key.AccountRef, key.ConversationRef,
	).Order("id").Find(&audits).Error
	return audits, err
}

func m5B177RecoveryAuditDetail() string {
	return "messageSeq=2 from=system/system to=in/card/resumeAttachment " +
		"archivedApplicationKey=true projectedThroughSeq=2->1"
}
