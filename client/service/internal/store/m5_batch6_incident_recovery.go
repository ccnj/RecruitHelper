package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"recruithelper/contract/gen/go/protocol"
)

var ErrM5Batch6IncidentRecoveryUnsafe = errors.New("M5 批次 6 事故恢复前置事实不完整")

const (
	m5Batch6RecoveryAuditCategory = "m5_batch6_incident_recovery"
	m5Batch6FreshProofMaxAge      = 10 * time.Minute

	m5Batch6RetractionReasonPrecedingHistory1 = "m5_batch6_erroneous_rebaseline_preceding_history_1"
	m5Batch6RetractionReasonPrecedingHistory2 = "m5_batch6_erroneous_rebaseline_preceding_history_2"
	m5Batch6RetractionReasonDuplicateGreeting = "m5_batch6_erroneous_rebaseline_duplicate_greeting"
)

type M5Batch6IncidentRecoveryResult struct {
	Applied            bool
	AlreadyApplied     bool
	FreshTailUnique    bool
	ReachedTop         bool
	SourceKeyMatched   bool
	ContentHashMatched bool
	ShapeMatched       bool
	ActiveBefore       int
	ActiveAfter        int
}

type m5Batch6FreshProof struct {
	key        ConversationKey
	data       protocol.ChatReadThreadData
	terminalAt time.Time
}

// RecoverM5Batch6Incident 是批次 6 首次真机验收事故的一次性本地维护事务。
// 它只接受脑已经持久化的 fresh chat.readThread msgId；平台身份、会话引用、
// sourceKey、正文和 hash 均从同一事务内的既有事实推导，不进入调用参数、审计
// detail 或命令行输出。该方法没有 HTTP 接线，也不被巡检或恢复扫描调用。
func (s *Store) RecoverM5Batch6Incident(freshReadMsgID string) (*M5Batch6IncidentRecoveryResult, error) {
	if strings.TrimSpace(freshReadMsgID) == "" {
		return nil, ErrM5Batch6IncidentRecoveryUnsafe
	}
	now := time.Now()
	out := &M5Batch6IncidentRecoveryResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		proof, err := loadM5Batch6FreshProofTx(tx, freshReadMsgID)
		if err != nil {
			return err
		}

		var account Account
		if err := tx.First(&account, "platform = ? AND account_ref = ?", proof.key.Platform, proof.key.AccountRef).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrM5Batch6IncidentRecoveryUnsafe
			}
			return err
		}
		if account.StoppedAt == nil || account.PausedReason != "userPaused" {
			return ErrM5Batch6IncidentRecoveryUnsafe
		}

		var conversation Conversation
		if err := tx.Where(conversationWhere(proof.key), conversationArgs(proof.key)...).First(&conversation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrM5Batch6IncidentRecoveryUnsafe
			}
			return err
		}
		if conversation.TrackingState != TrackingAdopted || conversation.AdoptedBoundarySeq != 0 ||
			conversation.LastMessageSeq != 6 || conversation.LastMessageDirection != "in" ||
			conversation.LastMessageKind != "text" {
			return ErrM5Batch6IncidentRecoveryUnsafe
		}
		var tracked TrackedIntent
		if err := tx.Where(conversationWhere(proof.key), conversationArgs(proof.key)...).First(&tracked).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrM5Batch6IncidentRecoveryUnsafe
			}
			return err
		}
		if tracked.Status != TrackingAdopted {
			return ErrM5Batch6IncidentRecoveryUnsafe
		}

		var facts []Message
		if err := tx.Where(conversationWhere(proof.key), conversationArgs(proof.key)...).Order("seq").Find(&facts).Error; err != nil {
			return err
		}
		if !validM5Batch6PhysicalShape(facts) || conversation.LastMessagePreview != *facts[5].Text {
			return ErrM5Batch6IncidentRecoveryUnsafe
		}
		if err := requireM5Batch6RebaselineAuditTx(tx, proof, facts); err != nil {
			return err
		}
		if err := requireNoM5Batch6DialogueFactsTx(tx, proof.key); err != nil {
			return err
		}

		freshTailUnique, sourceKeyMatched, contentHashMatched, shapeMatched :=
			matchM5Batch6FreshTail(proof.data, facts[5])
		out.FreshTailUnique = freshTailUnique
		out.ReachedTop = proof.data.ReachedTop
		out.SourceKeyMatched = sourceKeyMatched
		out.ContentHashMatched = contentHashMatched
		out.ShapeMatched = shapeMatched
		if !proof.data.ReachedTop || !freshTailUnique || !sourceKeyMatched || !contentHashMatched || !shapeMatched {
			return ErrM5Batch6IncidentRecoveryUnsafe
		}

		initial, applied, activeCount := classifyM5Batch6RecoveryState(facts)
		out.ActiveBefore = activeCount
		var recoveryAudits []AuditEntry
		if err := tx.Where(
			"category = ? AND platform = ? AND account_ref = ? AND conversation_ref = ?",
			m5Batch6RecoveryAuditCategory, proof.key.Platform, proof.key.AccountRef, proof.key.ConversationRef,
		).Order("id").Find(&recoveryAudits).Error; err != nil {
			return err
		}
		if applied {
			if len(recoveryAudits) != 1 || recoveryAudits[0].Detail != m5Batch6RecoveryAuditDetail() ||
				recoveryAudits[0].RefMsgID != freshReadMsgID || recoveryAudits[0].RoundID != facts[5].FirstSeenRoundID {
				return ErrM5Batch6IncidentRecoveryUnsafe
			}
			if err := requireM5Batch6ActiveTailTx(tx, proof.key, conversation); err != nil {
				return err
			}
			out.AlreadyApplied = true
			out.ActiveAfter = 2
			return nil
		}
		if !initial || len(recoveryAudits) != 0 {
			return ErrM5Batch6IncidentRecoveryUnsafe
		}
		if proof.terminalAt.After(now) || now.Sub(proof.terminalAt) > m5Batch6FreshProofMaxAge {
			return ErrM5Batch6IncidentRecoveryUnsafe
		}

		reasons := []string{
			messageRetractionReasonClassificationCorrected,
			m5Batch6RetractionReasonPrecedingHistory1,
			m5Batch6RetractionReasonPrecedingHistory2,
			m5Batch6RetractionReasonDuplicateGreeting,
		}
		for index, reason := range reasons {
			message := &facts[index+1]
			if err := retractMessageTx(tx, message, proof.key, now, reason); err != nil {
				return err
			}
		}
		if err := requireM5Batch6ActiveTailTx(tx, proof.key, conversation); err != nil {
			return err
		}
		if err := tx.Create(&AuditEntry{
			At: now, Category: m5Batch6RecoveryAuditCategory,
			Platform: proof.key.Platform, AccountRef: proof.key.AccountRef,
			ConversationRef: proof.key.ConversationRef, RefMsgID: freshReadMsgID,
			RoundID: facts[5].FirstSeenRoundID, Detail: m5Batch6RecoveryAuditDetail(),
		}).Error; err != nil {
			return err
		}
		out.Applied = true
		out.ActiveAfter = 2
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func loadM5Batch6FreshProofTx(tx *gorm.DB, freshReadMsgID string) (m5Batch6FreshProof, error) {
	var selected CmdRecord
	if err := tx.First(&selected, "msg_id = ?", freshReadMsgID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return m5Batch6FreshProof{}, ErrM5Batch6IncidentRecoveryUnsafe
		}
		return m5Batch6FreshProof{}, err
	}
	var records []CmdRecord
	if err := tx.Where("logical_dispatch_id = ?", selected.LogicalDispatchID).
		Order("lineage_depth, created_at, msg_id").Find(&records).Error; err != nil {
		return m5Batch6FreshProof{}, err
	}
	leaf, err := validateLineage(records)
	if err != nil || leaf.MsgID != freshReadMsgID || leaf.Status != CmdOk || leaf.TerminalAt == nil ||
		leaf.Name != protocol.PrimChatReadThread || leaf.Class != string(protocol.ClassIntrusive) ||
		leaf.Platform != "zhilian" || leaf.AccountRef == "" || leaf.ResultBody == "" {
		return m5Batch6FreshProof{}, ErrM5Batch6IncidentRecoveryUnsafe
	}
	for i := range records {
		record := records[i]
		if record.Name != leaf.Name || record.Class != leaf.Class || record.Platform != leaf.Platform ||
			record.AccountRef != leaf.AccountRef || record.Args != leaf.Args {
			return m5Batch6FreshProof{}, ErrM5Batch6IncidentRecoveryUnsafe
		}
	}

	meta, ok := protocol.Primitives[protocol.PrimChatReadThread]
	if !ok || meta.Ver == 0 || protocol.ValidatePrimitiveArgs(leaf.Name, meta.Ver, json.RawMessage(leaf.Args)) != nil ||
		protocol.ValidatePrimitiveResult(leaf.Name, meta.Ver, json.RawMessage(leaf.ResultBody)) != nil {
		return m5Batch6FreshProof{}, ErrM5Batch6IncidentRecoveryUnsafe
	}
	var args protocol.ChatReadThreadArgs
	var result protocol.ResultBody
	if json.Unmarshal([]byte(leaf.Args), &args) != nil || json.Unmarshal([]byte(leaf.ResultBody), &result) != nil ||
		args.ConversationRef == "" || args.Cursor != "" ||
		result.Ref != leaf.MsgID || result.Status != protocol.ResultStatusOk || result.DataBlobRef != nil {
		return m5Batch6FreshProof{}, ErrM5Batch6IncidentRecoveryUnsafe
	}
	var data protocol.ChatReadThreadData
	if json.Unmarshal(result.Data, &data) != nil || len(data.Messages) == 0 || !data.ReachedTop {
		return m5Batch6FreshProof{}, ErrM5Batch6IncidentRecoveryUnsafe
	}
	return m5Batch6FreshProof{
		key: ConversationKey{
			Platform: leaf.Platform, AccountRef: leaf.AccountRef, ConversationRef: args.ConversationRef,
		},
		data: data, terminalAt: *leaf.TerminalAt,
	}, nil
}

func validM5Batch6PhysicalShape(facts []Message) bool {
	if len(facts) != 6 {
		return false
	}
	for index := range facts {
		if facts[index].Seq != int64(index+1) || facts[index].Platform == "" ||
			facts[index].AccountRef == "" || facts[index].ConversationRef == "" {
			return false
		}
		if index > 0 && (facts[index].Platform != facts[0].Platform ||
			facts[index].AccountRef != facts[0].AccountRef || facts[index].ConversationRef != facts[0].ConversationRef) {
			return false
		}
	}
	seq1, seq2, seq3, seq4, seq5, seq6 := facts[0], facts[1], facts[2], facts[3], facts[4], facts[5]
	if !plainTextMessageShape(seq1, "out", "self") || seq1.SourceKey != nil ||
		seq1.OutboundIntentID == nil || *seq1.OutboundIntentID == "" {
		return false
	}
	if !plainTextMessageShape(seq5, "out", "external") || seq5.OutboundIntentID != nil ||
		seq5.SourceKey == nil || !validMessageSourceKey(*seq5.SourceKey) ||
		seq1.ContentHash != seq5.ContentHash || !sameRequiredText(seq1.Text, seq5.Text) {
		return false
	}
	if seq2.Direction != "system" || seq2.Kind != "system" || seq2.Origin != "external" ||
		seq2.SourceKey != nil || seq2.OutboundIntentID != nil || !messageHasNoCardOrBlob(seq2) {
		return false
	}
	for _, message := range []Message{seq3, seq4} {
		if message.Direction != "system" || message.Kind != "system" || message.Origin != "external" ||
			message.SourceKey == nil || !validMessageSourceKey(*message.SourceKey) ||
			message.OutboundIntentID != nil || !messageHasNoCardOrBlob(message) {
			return false
		}
	}
	if !plainTextMessageShape(seq6, "in", "external") || seq6.SourceKey == nil ||
		!validMessageSourceKey(*seq6.SourceKey) || seq6.OutboundIntentID != nil ||
		seq2.ContentHash == "" || seq2.ContentHash != seq6.ContentHash ||
		!sameRequiredText(seq2.Text, seq6.Text) || !sameRequiredTimestamp(seq2.TsApproxMs, seq6.TsApproxMs) {
		return false
	}
	rebaselineRound := seq3.FirstSeenRoundID
	return rebaselineRound != "" && seq4.FirstSeenRoundID == rebaselineRound &&
		seq5.FirstSeenRoundID == rebaselineRound && seq6.FirstSeenRoundID == rebaselineRound &&
		seq2.FirstSeenRoundID != rebaselineRound
}

func plainTextMessageShape(message Message, direction, origin string) bool {
	return message.Direction == direction && message.Kind == "text" && message.Origin == origin &&
		message.ContentHash != "" && message.Text != nil && strings.TrimSpace(*message.Text) != "" &&
		messageHasNoCardOrBlob(message)
}

func messageHasNoCardOrBlob(message Message) bool {
	return message.BlobRef == "" && message.CardType == "" && message.CardState == ""
}

func sameRequiredText(left, right *string) bool {
	return left != nil && right != nil && strings.TrimSpace(*left) != "" && *left == *right
}

func sameRequiredTimestamp(left, right *int64) bool {
	return left != nil && right != nil && *left == *right
}

func requireM5Batch6RebaselineAuditTx(tx *gorm.DB, proof m5Batch6FreshProof, facts []Message) error {
	roundID := facts[5].FirstSeenRoundID
	var audits []AuditEntry
	if err := tx.Where(
		"category = ? AND platform = ? AND account_ref = ? AND conversation_ref = ? AND round_id = ?",
		"conversation_zero_overlap_rebaseline", proof.key.Platform, proof.key.AccountRef,
		proof.key.ConversationRef, roundID,
	).Find(&audits).Error; err != nil {
		return err
	}
	if len(audits) != 1 || !proof.terminalAt.After(audits[0].At) {
		return ErrM5Batch6IncidentRecoveryUnsafe
	}
	const expected = "oldTail=2 historicalFrom=3 historicalThrough=6 imported=4"
	if audits[0].Detail != expected && !strings.HasPrefix(audits[0].Detail, expected+" ") {
		return ErrM5Batch6IncidentRecoveryUnsafe
	}
	return nil
}

func requireNoM5Batch6DialogueFactsTx(tx *gorm.DB, key ConversationKey) error {
	var profiles []CandidateProfile
	if err := tx.Where("platform = ? AND account_ref = ? AND conversation_ref = ?",
		key.Platform, key.AccountRef, key.ConversationRef).Find(&profiles).Error; err != nil {
		return err
	}
	if len(profiles) != 1 {
		return ErrM5Batch6IncidentRecoveryUnsafe
	}
	profileID := profiles[0].ProfileID
	var turns, actions, invocations int64
	if err := tx.Model(&DialogueTurn{}).
		Where("profile_id = ? AND conversation_ref = ?", profileID, key.ConversationRef).Count(&turns).Error; err != nil {
		return err
	}
	if err := tx.Table("communication_actions AS actions").
		Joins("JOIN dialogue_turns AS turns ON turns.turn_id = actions.turn_id").
		Where("turns.profile_id = ? AND turns.conversation_ref = ?", profileID, key.ConversationRef).
		Count(&actions).Error; err != nil {
		return err
	}
	if err := tx.Table("ai_invocations AS invocations").
		Joins("JOIN dialogue_turns AS turns ON turns.turn_id = invocations.turn_id").
		Where("turns.profile_id = ? AND turns.conversation_ref = ?", profileID, key.ConversationRef).
		Count(&invocations).Error; err != nil {
		return err
	}
	if turns != 0 || actions != 0 || invocations != 0 {
		return ErrM5Batch6IncidentRecoveryUnsafe
	}
	return nil
}

func matchM5Batch6FreshTail(
	data protocol.ChatReadThreadData,
	kept Message,
) (unique, sourceKeyMatched, contentHashMatched, shapeMatched bool) {
	if len(data.Messages) == 0 || kept.SourceKey == nil {
		return false, false, false, false
	}
	tail := data.Messages[len(data.Messages)-1]
	sourceKeyMatched = tail.SourceKey == *kept.SourceKey
	contentHashMatched = tail.ContentHash == kept.ContentHash
	shapeMatched = string(tail.Direction) == kept.Direction && string(tail.Kind) == kept.Kind &&
		sameRequiredText(tail.Text, kept.Text) && sameRequiredTimestamp(tail.TsApprox, kept.TsApproxMs) &&
		tail.BlobRef == nil && tail.CardType == nil && tail.CardState == nil && kept.Origin == "external" &&
		messageHasNoCardOrBlob(kept)
	matchingSourceKeys := 0
	strictMatches := 0
	for index := range data.Messages {
		message := data.Messages[index]
		if message.SourceKey == *kept.SourceKey {
			matchingSourceKeys++
		}
		if message.SourceKey == *kept.SourceKey && message.ContentHash == kept.ContentHash &&
			string(message.Direction) == kept.Direction && string(message.Kind) == kept.Kind &&
			sameRequiredText(message.Text, kept.Text) && sameRequiredTimestamp(message.TsApprox, kept.TsApproxMs) &&
			message.BlobRef == nil && message.CardType == nil && message.CardState == nil {
			strictMatches++
		}
	}
	unique = sourceKeyMatched && contentHashMatched && shapeMatched && matchingSourceKeys == 1 && strictMatches == 1
	return unique, sourceKeyMatched, contentHashMatched, shapeMatched
}

func classifyM5Batch6RecoveryState(facts []Message) (initial, applied bool, activeCount int) {
	if len(facts) != 6 || facts[0].RetractedAt != nil || facts[5].RetractedAt != nil ||
		facts[0].RetractionReason != "" || facts[5].RetractionReason != "" {
		return false, false, 0
	}
	reasons := []string{
		messageRetractionReasonClassificationCorrected,
		m5Batch6RetractionReasonPrecedingHistory1,
		m5Batch6RetractionReasonPrecedingHistory2,
		m5Batch6RetractionReasonDuplicateGreeting,
	}
	initial, applied = true, true
	activeCount = 2
	for index, reason := range reasons {
		message := facts[index+1]
		if message.RetractedAt == nil {
			activeCount++
			applied = false
			if message.RetractionReason != "" {
				initial = false
			}
			continue
		}
		initial = false
		if message.RetractionReason != reason {
			applied = false
		}
	}
	return initial, applied, activeCount
}

func requireM5Batch6ActiveTailTx(tx *gorm.DB, key ConversationKey, before Conversation) error {
	var active []Message
	if err := tx.Where(conversationWhere(key), conversationArgs(key)...).
		Where(activeMessageCondition).Order("seq").Find(&active).Error; err != nil {
		return err
	}
	if len(active) != 2 || active[0].Seq != 1 || active[1].Seq != 6 ||
		active[1].Direction != "in" || active[1].Kind != "text" || active[1].Text == nil {
		return ErrM5Batch6IncidentRecoveryUnsafe
	}
	var after Conversation
	if err := tx.Where(conversationWhere(key), conversationArgs(key)...).First(&after).Error; err != nil {
		return err
	}
	if after.LastMessageSeq != 6 || after.LastMessageDirection != "in" || after.LastMessageKind != "text" ||
		after.LastMessagePreview != *active[1].Text || after.AdoptedBoundarySeq != before.AdoptedBoundarySeq {
		return ErrM5Batch6IncidentRecoveryUnsafe
	}
	return nil
}

func m5Batch6RecoveryAuditDetail() string {
	return fmt.Sprintf(
		"activeBefore=6 activeAfter=2 keptSeqs=1,6 freshTailUnique=true reachedTop=true "+
			"sourceKeyMatched=true contentHashMatched=true shapeMatched=true "+
			"seq2Reason=%s seq3Reason=%s seq4Reason=%s seq5Reason=%s",
		messageRetractionReasonClassificationCorrected,
		m5Batch6RetractionReasonPrecedingHistory1,
		m5Batch6RetractionReasonPrecedingHistory2,
		m5Batch6RetractionReasonDuplicateGreeting,
	)
}
