package store

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrEffectIntentNotFound       = errors.New("发送意图不存在")
	ErrEffectIntentConflict       = errors.New("同一 intentId 的发送材料不一致")
	ErrEffectIntentFrozen         = errors.New("发送意图已终局或冻结")
	ErrAccountIdentityNotCurrent  = errors.New("账号身份未在当前手会话验证")
	ErrManualQuietActive          = errors.New("真人操作静默窗仍有效")
	ErrRecoveryStateConflict      = errors.New("副作用恢复状态冲突")
	ErrRecoveryReportSource       = errors.New("恢复 report 与命令或手不匹配")
	ErrVerificationAlreadyRunning = errors.New("该副作用已有验证读在途")
	ErrEffectIntentCASConflict    = errors.New("会话最新发送意图已变化")
	ErrEffectIntentHeadCorrupt    = errors.New("会话副作用 head 缺失或损坏")
)

const (
	primitiveChatSendMessage      = "chat.sendMessage"
	primitiveChatSendGreeting     = "chat.sendGreeting"
	primitiveChatSendWechatInvite = "chat.sendWechatInvite"
	primitiveChatSendInviteCard   = "chat.sendInviteCard"

	messageRetractionReasonAuthoritativeSafeTerminal = "authoritative_safe_terminal"
	messageRetractionReasonManualResolvedFailed      = "manual_resolved_failed"
)

// SQLite INTEGER 是有符号 64 位。Generation 在 Go 中用 uint64
// 表达非负值，但不允许跨过 SQLite 可持久化的上界。
const maxSQLiteEffectHeadGeneration = uint64(1<<63 - 1)

type EffectIntentCASConflictError struct {
	PreviousIntentID string
	Current          *EffectIntent
}

func (e *EffectIntentCASConflictError) Error() string {
	current := "<none>"
	if e != nil && e.Current != nil {
		current = e.Current.IntentID
	}
	previous := ""
	if e != nil {
		previous = e.PreviousIntentID
	}
	return fmt.Sprintf("%s: previous=%q current=%q", ErrEffectIntentCASConflict, previous, current)
}

func (e *EffectIntentCASConflictError) Unwrap() error { return ErrEffectIntentCASConflict }

// SendPreparation 是创建真实发送意图前的只读快照。它只用于组装
// generated guards；CreateEffectIntentAndCmd 会在同一写事务里重查尾序号和
// 账号身份，因此不把该快照当成授权。
type SendPreparation struct {
	Account      Account
	Conversation Conversation
	Tail         []Message
}

func (s *Store) PrepareSend(key ConversationKey, tailLimit int) (*SendPreparation, error) {
	if tailLimit < 1 || tailLimit > 5 {
		return nil, errors.New("发送 guard 尾窗必须在 1..5")
	}
	var out SendPreparation
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&out.Account, "platform = ? AND account_ref = ?", key.Platform, key.AccountRef).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAccountNotFound
			}
			return err
		}
		if err := tx.Where(conversationWhere(key), conversationArgs(key)...).First(&out.Conversation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConversationNotFound
			}
			return err
		}
		if out.Conversation.TrackingState != TrackingAdopted {
			return ErrConversationNotTracked
		}
		var tracked TrackedIntent
		if err := tx.Where(conversationWhere(key), conversationArgs(key)...).First(&tracked).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConversationNotTracked
			}
			return err
		}
		if tracked.Status != TrackingAdopted {
			return ErrConversationNotTracked
		}
		if out.Conversation.LastMessageSeq == 0 {
			return errors.New("已收编会话没有可作 guard 的账本尾")
		}
		return tx.Where(conversationWhere(key), conversationArgs(key)...).
			Where(activeMessageCondition).Order("seq DESC").Limit(tailLimit).Find(&out.Tail).Error
	})
	if err != nil {
		return nil, err
	}
	// DB 倒序查询，线上 expectedTail 按时间正序。
	for left, right := 0, len(out.Tail)-1; left < right; left, right = left+1, right-1 {
		out.Tail[left], out.Tail[right] = out.Tail[right], out.Tail[left]
	}
	return &out, nil
}

type CreateEffectIntentRequest struct {
	Intent            EffectIntent
	Command           CmdRecord
	ExpectedTailSeq   int64
	PreviousIntentID  string
	AutomaticActionID string
	Now               time.Time
}

type CreateEffectIntentResult struct {
	Intent  EffectIntent
	Command CmdRecord
	Created bool
}

// CreateEffectIntentAndCmd 在一个 SQLite 单写事务中完成：幂等 HTTP
// intent 复用判定、adopted/身份/静默窗/账本尾/串行域闸、权威意图与
// queued 物理命令双落账。任一步失败均不留“有意图无命令”半态。
func (s *Store) CreateEffectIntentAndCmd(req CreateEffectIntentRequest) (*CreateEffectIntentResult, error) {
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	i, c := req.Intent, req.Command
	if i.IntentID == "" || i.IdemKey == "" || i.Platform == "" || i.AccountRef == "" ||
		i.Primitive == "" || i.TargetRef == "" || i.PayloadHash == "" || i.GuardsHash == "" ||
		c.MsgID == "" || c.IntentID != i.IntentID || c.IdemKey != i.IdemKey ||
		c.Platform != i.Platform || c.AccountRef != i.AccountRef || c.Name != i.Primitive ||
		c.Class != "effectful" || c.Domain == "" {
		return nil, errors.New("发送意图/命令缺少一致的必填字段")
	}
	if i.RootMsgID == "" {
		i.RootMsgID = c.MsgID
	}
	if i.RootMsgID != c.MsgID {
		return nil, ErrEffectIntentConflict
	}
	if i.Status == "" {
		i.Status = EffectIntentDispatching
	}
	if i.Status != EffectIntentDispatching {
		return nil, ErrEffectIntentConflict
	}
	if i.DeadlineMs <= req.Now.UnixMilli() || c.DeadlineMs <= req.Now.UnixMilli() {
		return nil, errors.New("发送意图已过期")
	}
	prepareRootCmd(&c)
	out := &CreateEffectIntentResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var existing EffectIntent
		err := tx.First(&existing, "intent_id = ?", i.IntentID).Error
		if err == nil {
			if !sameEffectIntentMaterial(existing, i) {
				return ErrEffectIntentConflict
			}
			if req.AutomaticActionID != "" {
				if err := validateM5AutomaticIntentLinkTx(tx, req.AutomaticActionID, existing); err != nil {
					return err
				}
			}
			var existingCmd CmdRecord
			if err := tx.First(&existingCmd, "msg_id = ?", existing.RootMsgID).Error; err != nil {
				return fmt.Errorf("%w: 意图根命令丢失", ErrEffectIntentConflict)
			}
			out.Intent, out.Command, out.Created = existing, existingCmd, false
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		head, latest, err := effectIntentHeadTx(tx, i.Platform, i.AccountRef, i.TargetRef)
		if err != nil {
			return err
		}
		if head != nil && head.Generation >= maxSQLiteEffectHeadGeneration {
			return fmt.Errorf("%w: generation 已达 SQLite 上限", ErrEffectIntentHeadCorrupt)
		}
		currentID := ""
		if latest != nil {
			currentID = latest.IntentID
		}
		if currentID != req.PreviousIntentID {
			return &EffectIntentCASConflictError{
				PreviousIntentID: req.PreviousIntentID,
				Current:          latest,
			}
		}

		var account Account
		if err := tx.First(&account, "platform = ? AND account_ref = ?", i.Platform, i.AccountRef).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAccountNotFound
			}
			return err
		}
		if account.IdentityState != IdentityVerified || account.PrincipalFingerprint == nil ||
			*account.PrincipalFingerprint == "" || account.BoundHandID != c.HandID ||
			account.IdentitySession != c.Session || account.IdentityBootID != c.BootIDAtDispatch ||
			*account.PrincipalFingerprint != c.ExpectedPrincipalFingerprint {
			return ErrAccountIdentityNotCurrent
		}
		if account.ManualQuietUntil != nil && req.Now.Before(*account.ManualQuietUntil) {
			return ErrManualQuietActive
		}

		key := ConversationKey{Platform: i.Platform, AccountRef: i.AccountRef, ConversationRef: i.TargetRef}
		var conversation Conversation
		if err := tx.Where(conversationWhere(key), conversationArgs(key)...).First(&conversation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConversationNotFound
			}
			return err
		}
		var tracked TrackedIntent
		if err := tx.Where(conversationWhere(key), conversationArgs(key)...).First(&tracked).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConversationNotTracked
			}
			return err
		}
		if conversation.TrackingState != TrackingAdopted || tracked.Status != TrackingAdopted ||
			conversation.LastMessageSeq != req.ExpectedTailSeq {
			if conversation.LastMessageSeq != req.ExpectedTailSeq {
				return ErrConversationVersionConflict
			}
			return ErrConversationNotTracked
		}

		var busy int64
		frozenStatuses := append(append([]CmdStatus(nil), nonTerminalStatuses...), CmdSuspect)
		if err := tx.Model(&CmdRecord{}).Where("domain = ? AND status IN ?", c.Domain, frozenStatuses).Count(&busy).Error; err != nil {
			return err
		}
		if busy != 0 {
			return ErrDomainBusy
		}
		if req.AutomaticActionID != "" {
			if err := bindM5AutomaticActionTx(
				tx,
				req.AutomaticActionID,
				req.PreviousIntentID,
				&i,
				&c,
				req.Now,
			); err != nil {
				return err
			}
		}

		if err := tx.Create(&i).Error; err != nil {
			return err
		}
		if err := tx.Create(&c).Error; err != nil {
			return err
		}
		if head == nil {
			head = &ConversationEffectHead{
				Platform: i.Platform, AccountRef: i.AccountRef, ConversationRef: i.TargetRef,
				LatestIntentID: i.IntentID, Generation: 1,
			}
			if err := tx.Create(head).Error; err != nil {
				return err
			}
		} else {
			nextGeneration := head.Generation + 1
			updated := tx.Model(&ConversationEffectHead{}).
				Where("platform = ? AND account_ref = ? AND conversation_ref = ? AND latest_intent_id = ? AND generation = ?",
					head.Platform, head.AccountRef, head.ConversationRef, head.LatestIntentID, head.Generation).
				Updates(map[string]any{"latest_intent_id": i.IntentID, "generation": nextGeneration})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrEffectIntentCASConflict
			}
		}
		out.Intent, out.Command, out.Created = i, c, true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func sameEffectIntentMaterial(a, b EffectIntent) bool {
	return a.IntentID == b.IntentID && a.IdemKey == b.IdemKey && a.Platform == b.Platform &&
		a.AccountRef == b.AccountRef && a.Primitive == b.Primitive && a.TargetRef == b.TargetRef &&
		a.PayloadHash == b.PayloadHash && a.GuardsHash == b.GuardsHash &&
		a.SendFingerprint == b.SendFingerprint
}

func effectIntentHeadTx(
	tx *gorm.DB, platform, accountRef, targetRef string,
) (*ConversationEffectHead, *EffectIntent, error) {
	var head ConversationEffectHead
	err := tx.First(&head,
		"platform = ? AND account_ref = ? AND conversation_ref = ?", platform, accountRef, targetRef).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		var orphaned int64
		if countErr := tx.Model(&EffectIntent{}).
			Where("platform = ? AND account_ref = ? AND target_ref = ? AND primitive = ?",
				platform, accountRef, targetRef, primitiveChatSendMessage).
			Count(&orphaned).Error; countErr != nil {
			return nil, nil, countErr
		}
		if orphaned != 0 {
			return nil, nil, fmt.Errorf("%w: 发现 %d 条无 head 意图", ErrEffectIntentHeadCorrupt, orphaned)
		}
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if head.LatestIntentID == "" || head.Generation == 0 {
		return nil, nil, fmt.Errorf("%w: 空 latestIntentId 或零 generation", ErrEffectIntentHeadCorrupt)
	}
	var intent EffectIntent
	if err := tx.First(&intent, "intent_id = ?", head.LatestIntentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, fmt.Errorf("%w: head 指向不存在的 intent %q", ErrEffectIntentHeadCorrupt, head.LatestIntentID)
		}
		return nil, nil, err
	}
	if intent.Platform != platform || intent.AccountRef != accountRef || intent.TargetRef != targetRef {
		return nil, nil, fmt.Errorf("%w: head 与 intent 目标不一致", ErrEffectIntentHeadCorrupt)
	}
	return &head, &intent, nil
}

// LatestEffectIntent 只沿持久单调 head 返回最新副作用意图，不含正文；
// 不读取 created_at/rowid，因此时钟回拨、重启和 VACUUM 都不改变答案。
func (s *Store) LatestEffectIntent(platform, accountRef, targetRef string) (*EffectIntent, error) {
	if platform == "" || accountRef == "" || targetRef == "" {
		return nil, errors.New("查询最新发送意图缺少会话标识")
	}
	_, intent, err := effectIntentHeadTx(s.db, platform, accountRef, targetRef)
	return intent, err
}

func (s *Store) EffectIntentByID(intentID string) (*EffectIntent, error) {
	var out EffectIntent
	err := s.db.First(&out, "intent_id = ?", intentID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) EffectIntentByIdemKey(idemKey string) (*EffectIntent, error) {
	var out EffectIntent
	err := s.db.First(&out, "idem_key = ?", idemKey).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// markProcessedTx 是 result/report 两条 WAL 路径共用的上行 msgId 去重。
// 调用方必须保证后续状态推进与该写入处在同一事务。
func markProcessedTx(tx *gorm.DB, msgID, kind, handID string) (bool, error) {
	inserted := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&ProcessedMsg{
		MsgID: msgID, Kind: kind, HandID: handID, ProcessedAt: time.Now(),
	})
	if inserted.Error != nil {
		return false, inserted.Error
	}
	return inserted.RowsAffected == 0, nil
}

// BeginEffectReconcileForHand 在新会话 welcome 已发出后收编该手所有
// 未终局真实 SX。它不做任何重投；只持久化 pendingReconcile 和新
// session/boot 栅栏，等 outbox 先补投完再由派发器发 query。
func (s *Store) BeginEffectReconcileForHand(handID, session, bootID string, at time.Time) ([]CmdRecord, error) {
	if handID == "" || session == "" || bootID == "" {
		return nil, errors.New("副作用收编缺少 hand/session/boot")
	}
	if at.IsZero() {
		at = time.Now()
	}
	var out []CmdRecord
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var commands []CmdRecord
		if err := tx.Where("hand_id = ? AND intent_id <> ? AND status IN ?", handID, "", nonTerminalStatuses).
			Order("created_at, msg_id").Find(&commands).Error; err != nil {
			return err
		}
		for i := range commands {
			cmd := &commands[i]
			if cmd.Status != CmdVerifying {
				if cmd.Status != CmdPendingReconcile {
					cmd.PreReconcileStatus = cmd.Status
				}
				cmd.Status = CmdPendingReconcile
			}
			cmd.ReconcileSession = session
			cmd.ReconcileBootID = bootID
			cmd.ReconcileNextAt = nil
			cmd.QueryMsgID = ""
			cmd.QuerySentAt = nil
			cmd.ReportState = ""
			cmd.ReportBody = ""
			cmd.ReportedAt = nil
			cmd.RecoveryAuthorized = false
			if err := tx.Save(cmd).Error; err != nil {
				return err
			}
			intentStatus := EffectIntentReconciling
			if cmd.Status == CmdVerifying {
				intentStatus = EffectIntentVerifying
			}
			if err := tx.Model(&EffectIntent{}).Where("intent_id = ?", cmd.IntentID).
				Update("status", intentStatus).Error; err != nil {
				return err
			}
		}
		out = commands
		return nil
	})
	return out, err
}

// PrepareEffectRecoveryAfterBrainRestart 在任何 WS 监听前把遗留的真实
// SX 收编为待对账，而不像 M1 debug effectful 那样立即 suspect。空
// reconcile session 表示必须等手下次 hello 后先补 outbox。
func (s *Store) PrepareEffectRecoveryAfterBrainRestart(at time.Time) ([]CmdRecord, error) {
	if at.IsZero() {
		at = time.Now()
	}
	var out []CmdRecord
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("intent_id <> ? AND status IN ?", "", nonTerminalStatuses).
			Order("created_at, msg_id").Find(&out).Error; err != nil {
			return err
		}
		for i := range out {
			cmd := &out[i]
			if cmd.Status != CmdVerifying {
				if cmd.Status != CmdPendingReconcile {
					cmd.PreReconcileStatus = cmd.Status
				}
				cmd.Status = CmdPendingReconcile
			}
			cmd.ReconcileSession = ""
			cmd.ReconcileBootID = ""
			cmd.ReconcileNextAt = nil
			cmd.QueryMsgID = ""
			cmd.QuerySentAt = nil
			cmd.RecoveryAuthorized = false
			if err := tx.Save(cmd).Error; err != nil {
				return err
			}
			intentStatus := EffectIntentReconciling
			if cmd.Status == CmdVerifying {
				intentStatus = EffectIntentVerifying
			}
			if err := tx.Model(&EffectIntent{}).Where("intent_id = ?", cmd.IntentID).
				Update("status", intentStatus).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

func (s *Store) EffectRecoveryCommandsForHand(handID string) ([]CmdRecord, error) {
	var out []CmdRecord
	err := s.db.Where("hand_id = ? AND intent_id <> ? AND status IN ?", handID, "",
		[]CmdStatus{CmdPendingReconcile, CmdVerifying}).Order("created_at, msg_id").Find(&out).Error
	return out, err
}

func (s *Store) HasEffectRecoveryForHand(handID string) (bool, error) {
	var n int64
	err := s.db.Model(&CmdRecord{}).Where("hand_id = ? AND intent_id <> ? AND status IN ?", handID, "",
		[]CmdStatus{CmdPendingReconcile, CmdVerifying}).Count(&n).Error
	return n != 0, err
}

// RecordRecoveryQuery 只允许当前收编 session 给尚未归类的 SX 盖 query 证词。
func (s *Store) RecordRecoveryQuery(ref, handID, session, bootID, queryMsgID string, at time.Time) error {
	if at.IsZero() {
		at = time.Now()
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var cmd CmdRecord
		if err := tx.First(&cmd, "msg_id = ?", ref).Error; err != nil {
			return err
		}
		if cmd.HandID != handID || cmd.IntentID == "" || cmd.Status != CmdPendingReconcile ||
			cmd.ReconcileSession != session || cmd.ReconcileBootID != bootID || cmd.RecoveryAuthorized {
			return ErrRecoveryStateConflict
		}
		if cmd.ReconcileNextAt != nil && at.Before(*cmd.ReconcileNextAt) {
			return ErrRecoveryStateConflict
		}
		cmd.QueryMsgID = queryMsgID
		cmd.QuerySentAt = &at
		cmd.QueryN++
		cmd.ReconcileNextAt = nil
		return tx.Save(&cmd).Error
	})
}

// ExpireRecoveryQueries 只处理丢失的只读 query/report 交换。未达上限时
// 清掉旧 query 以便同 session 重问；达上限后直接转验证读，从不将
// “没收到 report”解释为零副作用证明。
func (s *Store) ExpireRecoveryQueries(cutoff, at time.Time, maxQueries int) ([]CmdRecord, []CmdRecord, error) {
	if at.IsZero() {
		at = time.Now()
	}
	if maxQueries <= 0 {
		return nil, nil, errors.New("对账 query 上限必须为正数")
	}
	var retry, verify []CmdRecord
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var commands []CmdRecord
		if err := tx.Where("intent_id <> ? AND status = ? AND query_sent_at IS NOT NULL AND query_sent_at <= ?",
			"", CmdPendingReconcile, cutoff).Order("created_at, msg_id").Find(&commands).Error; err != nil {
			return err
		}
		for i := range commands {
			cmd := &commands[i]
			if cmd.QueryN >= maxQueries {
				cmd.Status = CmdVerifying
				cmd.VerificationReason = "recovery report timeout"
				cmd.VerificationNextAt = &at
				cmd.RecoveryAuthorized = false
				cmd.ReviewReady = false
				cmd.ReviewAfterMs = 0
				verify = append(verify, *cmd)
				if err := tx.Model(&EffectIntent{}).Where("intent_id = ?", cmd.IntentID).
					Updates(map[string]any{"status": EffectIntentVerifying, "suspect_reason": cmd.VerificationReason}).Error; err != nil {
					return err
				}
			} else {
				cmd.QueryMsgID = ""
				cmd.QuerySentAt = nil
				retry = append(retry, *cmd)
			}
			if err := tx.Save(cmd).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return retry, verify, err
}

func (s *Store) RecoveryQueriesReady(at time.Time) ([]CmdRecord, error) {
	var out []CmdRecord
	err := s.db.Where("intent_id <> ? AND status = ? AND query_msg_id = ? AND reconcile_next_at IS NOT NULL AND reconcile_next_at <= ?",
		"", CmdPendingReconcile, "", at).Order("created_at, msg_id").Find(&out).Error
	return out, err
}

func (s *Store) ClearRecoveryQuery(ref, queryMsgID string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var cmd CmdRecord
		if err := tx.First(&cmd, "msg_id = ?", ref).Error; err != nil {
			return err
		}
		if cmd.Status != CmdPendingReconcile || cmd.QueryMsgID != queryMsgID {
			return nil
		}
		cmd.QueryMsgID = ""
		cmd.QuerySentAt = nil
		return tx.Save(&cmd).Error
	})
}

type RecoveryReportObservation struct {
	Ref            string
	ReportMsgID    string
	HandID         string
	Session        string
	BootID         string
	State          string
	WitnessStoreID string
	Body           string
	At             time.Time
	NextQueryAt    time.Time
	MaxQueries     int
}

type RecoveryReportResult struct {
	AlreadyProcessed  bool
	Found             bool
	Authorized        bool
	NeedsVerification bool
	Executing         bool
}

// ApplyRecoveryReport 处理 done 以外的四种 report。done 必须走与普通
// result 相同的原子终局路径，因此由 dispatch.applyResultMessage 处理。
func (s *Store) ApplyRecoveryReport(obs RecoveryReportObservation) (*RecoveryReportResult, error) {
	if obs.At.IsZero() {
		obs.At = time.Now()
	}
	if obs.Ref == "" || obs.ReportMsgID == "" || obs.HandID == "" || obs.Session == "" || obs.BootID == "" {
		return nil, errors.New("report 入账缺少关联字段")
	}
	out := &RecoveryReportResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		already, err := markProcessedTx(tx, obs.ReportMsgID, "report", obs.HandID)
		if err != nil {
			return err
		}
		if already {
			out.AlreadyProcessed = true
			return nil
		}
		var cmd CmdRecord
		if err := tx.First(&cmd, "msg_id = ?", obs.Ref).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		out.Found = true
		if cmd.HandID != obs.HandID || cmd.IntentID == "" || cmd.Status != CmdPendingReconcile ||
			cmd.ReconcileSession != obs.Session || cmd.ReconcileBootID != obs.BootID || cmd.QueryMsgID == "" {
			return ErrRecoveryReportSource
		}
		cmd.ReportState = obs.State
		cmd.ReportBody = obs.Body
		cmd.ReportedAt = &obs.At
		cmd.Session = obs.Session
		cmd.BootIDAtDispatch = obs.BootID
		intentStatus := EffectIntentReconciling
		switch obs.State {
		case "unknown":
			// unknown/queued 只在同一持久 witness store 上才是零副作用
			// 证明。扩展数据被清理后新 store 的 unknown 只能说“我不记得”。
			if obs.WitnessStoreID != "" && obs.WitnessStoreID == cmd.WitnessStoreIDAtDispatch {
				cmd.RecoveryAuthorized = true
				out.Authorized = true
			} else {
				cmd.Status = CmdVerifying
				cmd.VerificationReason = "witness store changed or unavailable: " + obs.State
				cmd.VerificationNextAt = &obs.At
				cmd.ReviewReady = false
				cmd.ReviewAfterMs = 0
				intentStatus = EffectIntentVerifying
				out.NeedsVerification = true
			}
		case "queued", "executing":
			// 现行契约断线不丢已 accepted 队列。queued/executing 都证明
			// 原命令仍在手里，只可等原执行/result，不得同时再投一份。
			// 保留 pendingReconcile 与 generation 栅栏，用只读 query 有界复询；
			// 到上限只转验证，不重投 SX。
			if obs.MaxQueries <= 0 || obs.NextQueryAt.IsZero() {
				return ErrRecoveryStateConflict
			}
			if cmd.QueryN >= obs.MaxQueries {
				cmd.Status = CmdVerifying
				cmd.VerificationReason = "report=" + obs.State + " remained without result"
				cmd.VerificationNextAt = &obs.At
				cmd.ReviewReady = false
				cmd.ReviewAfterMs = 0
				intentStatus = EffectIntentVerifying
				out.NeedsVerification = true
			} else {
				cmd.Status = CmdPendingReconcile
				cmd.QueryMsgID = ""
				cmd.QuerySentAt = nil
				cmd.ReconcileNextAt = &obs.NextQueryAt
				intentStatus = EffectIntentReconciling
				out.Executing = true
			}
		case "attempting":
			cmd.Status = CmdVerifying
			cmd.VerificationReason = "report=attempting"
			cmd.VerificationNextAt = &obs.At
			cmd.ReviewReady = false
			cmd.ReviewAfterMs = 0
			intentStatus = EffectIntentVerifying
			out.NeedsVerification = true
		default:
			return fmt.Errorf("%w: 非法 report state %q", ErrRecoveryStateConflict, obs.State)
		}
		if err := tx.Save(&cmd).Error; err != nil {
			return err
		}
		res := tx.Model(&EffectIntent{}).Where("intent_id = ?", cmd.IntentID).Update("status", intentStatus)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrEffectIntentNotFound
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReleaseSafeRecoveriesForHand 只在该手没有尚未归类/尚在验证的 SX 时，
// 原子地把所有拿到零副作用证明的原物理命令放回 queued。
// 恢复仍使用原 msgId/body/idemKey/guards，只更新 session/boot/attempt；这使手侧
// journal.ref 与脑账本始终一致。返回值只是已落账的 queued 命令。
func (s *Store) ReleaseSafeRecoveriesForHand(
	handID, session, bootID, witnessStoreID string,
	at time.Time,
) ([]CmdRecord, []CmdRecord, error) {
	if at.IsZero() {
		at = time.Now()
	}
	var released, verify []CmdRecord
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var recovering []CmdRecord
		if err := tx.Where("hand_id = ? AND intent_id <> ? AND status IN ?", handID, "",
			[]CmdStatus{CmdPendingReconcile, CmdVerifying}).Order("created_at, msg_id").Find(&recovering).Error; err != nil {
			return err
		}
		for i := range recovering {
			if recovering[i].Status == CmdVerifying || !recovering[i].RecoveryAuthorized {
				return nil // 屏障未收束，一条也不释放
			}
		}
		// cap/deadline 会把“已拿到 unknown 证明”的命令改道验证。先把
		// 所有改道项原子标出，再决定整手是否释放；不能按枚举顺序先
		// 重投 A，随后才发现 B 已到 cap 并转 verifying。
		blocked := false
		for i := range recovering {
			parent := &recovering[i]
			var intent EffectIntent
			if err := tx.First(&intent, "intent_id = ?", parent.IntentID).Error; err != nil {
				return err
			}
			if parent.RecoveryRedispatchN >= 1 || at.UnixMilli() >= intent.DeadlineMs {
				parent.Status = CmdVerifying
				parent.RecoveryAuthorized = false
				parent.VerificationReason = "safe recovery cap/deadline reached"
				parent.VerificationNextAt = &at
				parent.ReviewReady = false
				parent.ReviewAfterMs = 0
				if err := tx.Save(parent).Error; err != nil {
					return err
				}
				if err := tx.Model(&EffectIntent{}).Where("intent_id = ?", parent.IntentID).
					Updates(map[string]any{
						"status": EffectIntentVerifying, "suspect_reason": parent.VerificationReason,
					}).Error; err != nil {
					return err
				}
				verify = append(verify, *parent)
				blocked = true
			}
		}
		if blocked {
			return nil
		}

		for i := range recovering {
			parent := &recovering[i]
			parent.Status = CmdQueued
			parent.Session = session
			parent.BootIDAtDispatch = bootID
			parent.WitnessStoreIDAtDispatch = witnessStoreID
			parent.RecoveryRedispatchN++
			parent.RecoveryAuthorized = false
			parent.SuspectReason = ""
			parent.PreReconcileStatus = ""
			parent.ReconcileSession = ""
			parent.ReconcileBootID = ""
			parent.ReconcileNextAt = nil
			parent.QueryMsgID = ""
			parent.QuerySentAt = nil
			parent.ReportState = ""
			parent.ReportBody = ""
			parent.ReportedAt = nil
			parent.SentAt = nil
			parent.NotBeforeAt = nil
			if err := tx.Save(parent).Error; err != nil {
				return err
			}
			if err := tx.Model(&EffectIntent{}).Where("intent_id = ?", parent.IntentID).
				Update("status", EffectIntentDispatching).Error; err != nil {
				return err
			}
			released = append(released, *parent)
		}
		return nil
	})
	return released, verify, err
}

func (s *Store) VerifyingEffectCommandsDue(at time.Time) ([]CmdRecord, error) {
	var out []CmdRecord
	err := s.db.Where("intent_id <> ? AND status = ? AND (verification_next_at IS NULL OR verification_next_at <= ?)",
		"", CmdVerifying, at).Order("created_at, msg_id").Find(&out).Error
	return out, err
}

func (s *Store) MoveEffectToVerification(ref, reason string, at time.Time) error {
	if at.IsZero() {
		at = time.Now()
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var cmd CmdRecord
		if err := tx.First(&cmd, "msg_id = ?", ref).Error; err != nil {
			return err
		}
		if cmd.IntentID == "" || cmd.Status.Terminal() {
			return ErrRecoveryStateConflict
		}
		wasVerifying := cmd.Status == CmdVerifying
		cmd.Status = CmdVerifying
		cmd.VerificationReason = reason
		cmd.VerificationNextAt = &at
		cmd.RecoveryAuthorized = false
		cmd.ReviewReady = false
		cmd.ReviewAfterMs = 0
		if !wasVerifying {
			cmd.VerificationChildMsgID = ""
		}
		if err := tx.Save(&cmd).Error; err != nil {
			return err
		}
		return tx.Model(&EffectIntent{}).Where("intent_id = ?", cmd.IntentID).
			Updates(map[string]any{"status": EffectIntentVerifying, "suspect_reason": reason}).Error
	})
}

// AbortEffectBeforeSend 只用于 Hub 的代际或契约一致性写栅栏明确证明
// “一个字节都未进入 socket”的情形。它终结 intent 而不进验证，但也
// 不会将这条命令放回自动重投队列。
func (s *Store) AbortEffectBeforeSend(ref, errorCode, reason string, at time.Time) error {
	if errorCode == "" {
		return errors.New("发送前终结缺少错误码")
	}
	if at.IsZero() {
		at = time.Now()
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var cmd CmdRecord
		if err := tx.First(&cmd, "msg_id = ?", ref).Error; err != nil {
			return err
		}
		if cmd.IntentID == "" || cmd.Status != CmdQueued {
			return ErrRecoveryStateConflict
		}
		cmd.Status = CmdFailed
		cmd.ErrorCode = errorCode
		cmd.SideEffect = "none"
		cmd.SuspectReason = reason
		cmd.TerminalAt = &at
		if err := tx.Save(&cmd).Error; err != nil {
			return err
		}
		res := tx.Model(&EffectIntent{}).Where("intent_id = ?", cmd.IntentID).Updates(map[string]any{
			"status": EffectIntentFailed, "suspect_reason": reason, "resolved_at": at,
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrEffectIntentNotFound
		}
		return applyM5AutomaticEffectStatusByIDTx(tx, cmd.IntentID, at)
	})
}

func (s *Store) RejectEffectCommand(ref, errorCode, reason string, at time.Time) error {
	if at.IsZero() {
		at = time.Now()
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var cmd CmdRecord
		if err := tx.First(&cmd, "msg_id = ?", ref).Error; err != nil {
			return err
		}
		if cmd.IntentID == "" {
			return ErrRecoveryStateConflict
		}
		if cmd.Status.Terminal() {
			return nil
		}
		cmd.Status = CmdRejected
		cmd.ErrorCode = errorCode
		cmd.SideEffect = "none"
		cmd.SuspectReason = reason
		cmd.RecoveryAuthorized = false
		cmd.TerminalAt = &at
		if err := tx.Save(&cmd).Error; err != nil {
			return err
		}
		res := tx.Model(&EffectIntent{}).Where("intent_id = ?", cmd.IntentID).Updates(map[string]any{
			"status": EffectIntentFailed, "suspect_reason": reason, "resolved_at": at,
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrEffectIntentNotFound
		}
		return applyM5AutomaticEffectStatusByIDTx(tx, cmd.IntentID, at)
	})
}

// CreateVerificationCmd 是 suspect 冻结域的唯一例外入口。它不跳过
// 命令 WAL，只是把“原 SX 正在 verifying”与“同域只有一条为它服务的
// intrusive read”放在同一事务中证明。
func (s *Store) CreateVerificationCmd(parentRef string, child *CmdRecord) error {
	if child == nil || parentRef == "" || child.MsgID == "" || child.Class != "intrusive" ||
		child.VerificationForMsgID != parentRef {
		return errors.New("验证读命令非法")
	}
	prepareRootCmd(child)
	return s.db.Transaction(func(tx *gorm.DB) error {
		var parent CmdRecord
		if err := tx.First(&parent, "msg_id = ?", parentRef).Error; err != nil {
			return err
		}
		if parent.IntentID == "" || parent.Status != CmdVerifying ||
			parent.HandID != child.HandID || parent.Domain != child.Domain ||
			parent.Platform != child.Platform || parent.AccountRef != child.AccountRef {
			return ErrRecoveryStateConflict
		}
		if parent.VerificationChildMsgID != "" {
			return ErrVerificationAlreadyRunning
		}
		var active int64
		if err := tx.Model(&CmdRecord{}).
			Where("verification_for_msg_id = ? AND status IN ?", parentRef, nonTerminalStatuses).
			Count(&active).Error; err != nil {
			return err
		}
		if active != 0 {
			return ErrVerificationAlreadyRunning
		}
		if err := createRootCmd(tx, child); err != nil {
			return err
		}
		parent.VerificationChildMsgID = child.MsgID
		return tx.Save(&parent).Error
	})
}

func (s *Store) VerificationChildForParent(parentRef string) (*CmdRecord, error) {
	var out *CmdRecord
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var parent CmdRecord
		if err := tx.First(&parent, "msg_id = ?", parentRef).Error; err != nil {
			return err
		}
		if parent.VerificationChildMsgID != "" {
			var child CmdRecord
			if err := tx.First(&child, "msg_id = ?", parent.VerificationChildMsgID).Error; err != nil {
				return err
			}
			out = &child
			return nil
		}
		// 兼容旧数据/创建指针前崩溃恢复：只收编仍在途的 child，
		// 绝不把已消费的旧负观测复用成新一轮。
		var child CmdRecord
		err := tx.Where("verification_for_msg_id = ? AND status IN ?", parentRef, nonTerminalStatuses).
			Order("created_at DESC, msg_id DESC").First(&child).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		parent.VerificationChildMsgID = child.LogicalDispatchID
		if err := tx.Save(&parent).Error; err != nil {
			return err
		}
		out = &child
		return nil
	})
	return out, err
}

func (s *Store) DeferEffectVerification(ref, reason string, nextAt time.Time) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var cmd CmdRecord
		if err := tx.First(&cmd, "msg_id = ?", ref).Error; err != nil {
			return err
		}
		if cmd.IntentID == "" || cmd.Status != CmdVerifying || cmd.VerificationChildMsgID == "" {
			return ErrRecoveryStateConflict
		}
		cmd.VerificationReason = reason
		cmd.VerificationNextAt = &nextAt
		return tx.Save(&cmd).Error
	})
}

func (s *Store) ConsumeVerificationChild(parentRef, childLogicalID string) error {
	if parentRef == "" || childLogicalID == "" {
		return ErrRecoveryStateConflict
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var parent CmdRecord
		if err := tx.First(&parent, "msg_id = ?", parentRef).Error; err != nil {
			return err
		}
		if parent.Status != CmdVerifying || parent.VerificationChildMsgID != childLogicalID {
			return ErrRecoveryStateConflict
		}
		parent.VerificationChildMsgID = ""
		return tx.Save(&parent).Error
	})
}

type VerifiedEffectSuccess struct {
	Ref              string
	ConversationKey  ConversationKey
	Text             string
	ContentHash      string
	ObservedAtMs     int64
	ResultBody       string
	ResolutionReason string
	At               time.Time
}

// ResolveEffectVerified 把验证读命中的 SX、权威意图与 origin=self
// 消息事实在同一事务中落地。OutboundIntentID 唯一索引使重复
// 验证/report/result 竞态最多产生一条脑账本消息。
func (s *Store) ResolveEffectVerified(req VerifiedEffectSuccess) (*Message, error) {
	if req.At.IsZero() {
		req.At = time.Now()
	}
	if req.Ref == "" || req.ContentHash == "" || req.ConversationKey.Platform == "" ||
		req.ConversationKey.AccountRef == "" || req.ConversationKey.ConversationRef == "" {
		return nil, errors.New("验证成功缺少关联字段")
	}
	var appended Message
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var cmd CmdRecord
		if err := tx.First(&cmd, "msg_id = ?", req.Ref).Error; err != nil {
			return err
		}
		if cmd.IntentID == "" {
			return ErrRecoveryStateConflict
		}
		if cmd.Status == CmdOk || cmd.Status == CmdResolvedOk {
			// 迟到重复验证幂等返回已有事实。
			if err := tx.First(&appended, "outbound_intent_id = ?", cmd.IntentID).Error; err != nil {
				return err
			}
			if appended.RetractedAt != nil {
				return fmt.Errorf("%w: 已撤回的出站事实不得当作验证成功", ErrRecoveryStateConflict)
			}
			return applyM5AutomaticEffectStatusByIDTx(tx, cmd.IntentID, req.At)
		}
		if cmd.Status != CmdVerifying && cmd.Status != CmdPendingReconcile && cmd.Status != CmdSuspect {
			return ErrRecoveryStateConflict
		}
		var intent EffectIntent
		if err := tx.First(&intent, "intent_id = ?", cmd.IntentID).Error; err != nil {
			return err
		}
		if intent.Platform != req.ConversationKey.Platform || intent.AccountRef != req.ConversationKey.AccountRef ||
			intent.TargetRef != req.ConversationKey.ConversationRef || intent.SendFingerprint != req.ContentHash {
			return ErrEffectIntentConflict
		}
		message, err := appendOutboundMessageTx(tx, &intent, req.Text, req.ContentHash, req.ObservedAtMs, req.At)
		if err != nil {
			return err
		}
		appended = *message
		cmd.Status = CmdOk
		cmd.ResultBody = req.ResultBody
		cmd.SuspectReason = req.ResolutionReason
		cmd.SideEffect = "confirmed"
		cmd.TerminalAt = &req.At
		cmd.VerificationChildMsgID = ""
		if err := tx.Save(&cmd).Error; err != nil {
			return err
		}
		intent.Status = EffectIntentOk
		intent.ResultMessageSeq = &appended.Seq
		intent.SuspectReason = req.ResolutionReason
		intent.ResolvedAt = &req.At
		if err := tx.Save(&intent).Error; err != nil {
			return err
		}
		return applyM5AutomaticEffectStatusTx(tx, &intent, req.At)
	})
	if err != nil {
		return nil, err
	}
	return &appended, nil
}

type VerifiedCardSuccess struct {
	Ref              string
	ConversationKey  ConversationKey
	Card             CardResultMutation
	ResultBody       string
	ResolutionReason string
	At               time.Time
}

// ResolveCardVerified 把 chat.readThread 唯一命中的卡片正证送入与直接
// result 相同的 applyCardResultTx。sourceKey 与 OutboundIntentID 两层唯一
// 约束保证验证读、迟到 result 和巡检收编竞态最多留下一个业务事实。
func (s *Store) ResolveCardVerified(req VerifiedCardSuccess) (*Message, error) {
	if req.At.IsZero() {
		req.At = time.Now()
	}
	if req.Ref == "" || req.ConversationKey.Platform == "" ||
		req.ConversationKey.AccountRef == "" || req.ConversationKey.ConversationRef == "" {
		return nil, errors.New("卡片验证成功缺少关联字段")
	}
	var resolved Message
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var cmd CmdRecord
		if err := tx.First(&cmd, "msg_id = ?", req.Ref).Error; err != nil {
			return err
		}
		if cmd.IntentID == "" {
			return ErrRecoveryStateConflict
		}
		if cmd.Status == CmdOk || cmd.Status == CmdResolvedOk {
			if err := tx.First(&resolved, "outbound_intent_id = ?", cmd.IntentID).Error; err != nil {
				return err
			}
			if resolved.RetractedAt != nil {
				return fmt.Errorf("%w: 已撤回的卡片事实不得当作验证成功", ErrRecoveryStateConflict)
			}
			return applyM5AutomaticEffectStatusByIDTx(tx, cmd.IntentID, req.At)
		}
		if cmd.Status != CmdVerifying && cmd.Status != CmdPendingReconcile && cmd.Status != CmdSuspect {
			return ErrRecoveryStateConflict
		}
		var intent EffectIntent
		if err := tx.First(&intent, "intent_id = ?", cmd.IntentID).Error; err != nil {
			return err
		}
		if intent.Platform != req.ConversationKey.Platform ||
			intent.AccountRef != req.ConversationKey.AccountRef ||
			intent.TargetRef != req.ConversationKey.ConversationRef ||
			intent.SendFingerprint != req.Card.ContentHash {
			return ErrEffectIntentConflict
		}
		message, err := applyCardResultTx(tx, &intent, req.Card, req.At)
		if err != nil {
			return err
		}
		resolved = *message
		cmd.Status = CmdOk
		cmd.ResultBody = req.ResultBody
		cmd.SuspectReason = req.ResolutionReason
		cmd.SideEffect = "confirmed"
		cmd.TerminalAt = &req.At
		cmd.VerificationChildMsgID = ""
		if err := tx.Save(&cmd).Error; err != nil {
			return err
		}
		intent.Status = EffectIntentOk
		intent.ResultMessageSeq = &resolved.Seq
		intent.SuspectReason = req.ResolutionReason
		intent.ResolvedAt = &req.At
		if err := tx.Save(&intent).Error; err != nil {
			return err
		}
		return applyM5AutomaticEffectStatusTx(tx, &intent, req.At)
	})
	if err != nil {
		return nil, err
	}
	return &resolved, nil
}

// RecordVerificationMiss 记录一轮结构化验证读未能确认目标消息。
// “完整窗未看到”仍只是负观测，不授权重投；第 3 轮与错误/歧义
// 一样收敛为 suspect。
func (s *Store) RecordVerificationMiss(ref, reason string, nextAt, reviewAfter, at time.Time, maxAttempts int) (bool, error) {
	if at.IsZero() {
		at = time.Now()
	}
	if maxAttempts <= 0 {
		return false, errors.New("验证轮次上限必须为正数")
	}
	suspect := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var cmd CmdRecord
		if err := tx.First(&cmd, "msg_id = ?", ref).Error; err != nil {
			return err
		}
		if cmd.IntentID == "" || cmd.Status != CmdVerifying {
			return ErrRecoveryStateConflict
		}
		cmd.VerificationN++
		cmd.VerificationReason = reason
		cmd.VerificationChildMsgID = ""
		if cmd.VerificationN >= maxAttempts {
			suspect = true
			cmd.Status = CmdSuspect
			cmd.SuspectReason = "verification exhausted: " + reason
			cmd.TerminalAt = &at
			cmd.VerificationNextAt = nil
			cmd.ReviewReady = true
			cmd.ReviewAfterMs = reviewAfter.UnixMilli()
			if cmd.ReviewAfterMs < at.UnixMilli() {
				cmd.ReviewAfterMs = at.UnixMilli()
			}
		} else {
			cmd.VerificationNextAt = &nextAt
		}
		if err := tx.Save(&cmd).Error; err != nil {
			return err
		}
		updates := map[string]any{
			"status": EffectIntentVerifying, "suspect_reason": reason,
		}
		if suspect {
			updates["status"] = EffectIntentSuspect
		}
		if err := tx.Model(&EffectIntent{}).Where("intent_id = ?", cmd.IntentID).Updates(updates).Error; err != nil {
			return err
		}
		if suspect {
			return applyM5AutomaticEffectStatusByIDTx(tx, cmd.IntentID, at)
		}
		return nil
	})
	return suspect, err
}

func (s *Store) MarkEffectSuspect(ref, reason string, at time.Time) error {
	if at.IsZero() {
		at = time.Now()
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var cmd CmdRecord
		if err := tx.First(&cmd, "msg_id = ?", ref).Error; err != nil {
			return err
		}
		if cmd.IntentID == "" || cmd.Status != CmdVerifying {
			return ErrRecoveryStateConflict
		}
		cmd.Status = CmdSuspect
		cmd.SuspectReason = reason
		cmd.VerificationReason = reason
		cmd.VerificationNextAt = nil
		cmd.RecoveryAuthorized = false
		cmd.ReviewReady = false
		cmd.ReviewAfterMs = 0
		cmd.TerminalAt = &at
		if err := tx.Save(&cmd).Error; err != nil {
			return err
		}
		res := tx.Model(&EffectIntent{}).Where("intent_id = ?", cmd.IntentID).Updates(map[string]any{
			"status": EffectIntentSuspect, "suspect_reason": reason,
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrEffectIntentNotFound
		}
		return applyM5AutomaticEffectStatusByIDTx(tx, cmd.IntentID, at)
	})
}

type ResolveSuspectVerdictRequest struct {
	Ref             string
	Verdict         CmdStatus
	ConversationKey ConversationKey
	Text            string
	ContentHash     string
	At              time.Time
}

// ResolveSuspectVerdict 将人工裁决、权威 intent 与消息账本放在同一
// SQLite 事务。resolvedOk 表示人确认副作用已发生，因而必须追加
// 原 cmd.args 的 self 消息；resolvedFailed 不追加，也不留任何恢复授权。
func (s *Store) ResolveSuspectVerdict(req ResolveSuspectVerdictRequest) error {
	if req.At.IsZero() {
		req.At = time.Now()
	}
	if req.Ref == "" || (req.Verdict != CmdResolvedOk && req.Verdict != CmdResolvedFailed) {
		return ErrRecoveryStateConflict
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var cmd CmdRecord
		if err := tx.First(&cmd, "msg_id = ?", req.Ref).Error; err != nil {
			return err
		}
		if cmd.Status != CmdSuspect {
			return ErrRecoveryStateConflict
		}
		cmd.Status = req.Verdict
		cmd.RecoveryAuthorized = false
		cmd.VerificationNextAt = nil
		cmd.ReviewAfterMs = 0
		cmd.VerificationChildMsgID = ""
		cmd.TerminalAt = &req.At
		if cmd.IntentID == "" {
			return tx.Save(&cmd).Error
		}
		var intent EffectIntent
		if err := tx.First(&intent, "intent_id = ?", cmd.IntentID).Error; err != nil {
			return err
		}
		if intent.IdemKey != cmd.IdemKey || intent.RootMsgID != cmd.LogicalDispatchID {
			return ErrEffectIntentConflict
		}
		if req.Verdict == CmdResolvedOk {
			if req.ContentHash == "" || intent.SendFingerprint != req.ContentHash ||
				intent.Platform != req.ConversationKey.Platform || intent.AccountRef != req.ConversationKey.AccountRef ||
				intent.TargetRef != req.ConversationKey.ConversationRef {
				return ErrEffectIntentConflict
			}
			message, err := appendOutboundMessageTx(tx, &intent, req.Text, req.ContentHash, req.At.UnixMilli(), req.At)
			if err != nil {
				return err
			}
			intent.Status = EffectIntentResolvedOk
			intent.ResultMessageSeq = &message.Seq
		} else {
			if err := retractOutboundMessageTx(tx, &intent, req.At, messageRetractionReasonManualResolvedFailed); err != nil {
				return err
			}
			intent.Status = EffectIntentResolvedFailed
			intent.ResultMessageSeq = nil
		}
		intent.SuspectReason = cmd.SuspectReason
		intent.ResolvedAt = &req.At
		if err := tx.Save(&intent).Error; err != nil {
			return err
		}
		if err := applyM5AutomaticEffectStatusTx(tx, &intent, req.At); err != nil {
			return err
		}
		return tx.Save(&cmd).Error
	})
}

func appendOutboundMessageTx(
	tx *gorm.DB,
	intent *EffectIntent,
	text, contentHash string,
	observedAtMs int64,
	at time.Time,
) (*Message, error) {
	if intent == nil {
		return nil, ErrEffectIntentNotFound
	}
	var existing Message
	err := tx.First(&existing, "outbound_intent_id = ?", intent.IntentID).Error
	if err == nil {
		if existing.RetractedAt != nil {
			return nil, fmt.Errorf("%w: intent 已有被撤回的出站事实", ErrRecoveryStateConflict)
		}
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	key := ConversationKey{Platform: intent.Platform, AccountRef: intent.AccountRef, ConversationRef: intent.TargetRef}
	var conversation Conversation
	if err := tx.Where(conversationWhere(key), conversationArgs(key)...).First(&conversation).Error; err != nil {
		return nil, err
	}
	seq, err := nextPhysicalMessageSeqTx(tx, key)
	if err != nil {
		return nil, err
	}
	textCopy := text
	intentID := intent.IntentID
	// observedAtMs 是脑确认副作用已发生的观察时刻，不是平台消息的
	// 发送时刻。self 事实没有平台时间证据时必须保持未知，不能把
	// result/验证读完成时间投影成消息时间。
	_ = observedAtMs
	message := &Message{
		Platform: intent.Platform, AccountRef: intent.AccountRef, ConversationRef: intent.TargetRef,
		Seq: seq, Direction: "out", Kind: "text", ContentHash: contentHash, Text: &textCopy,
		TsApproxMs: nil, Origin: "self", OutboundIntentID: &intentID,
	}
	if err := tx.Create(message).Error; err != nil {
		return nil, err
	}
	updates := map[string]any{
		"last_message_seq": seq, "last_message_direction": "out", "last_message_kind": "text",
		"last_message_preview": text, "last_synced_at": at,
	}
	if err := tx.Model(&Conversation{}).Where(conversationWhere(key), conversationArgs(key)...).Updates(updates).Error; err != nil {
		return nil, err
	}
	return message, nil
}

func applyCardResultTx(
	tx *gorm.DB,
	intent *EffectIntent,
	card CardResultMutation,
	at time.Time,
) (*Message, error) {
	if tx == nil || intent == nil {
		return nil, ErrEffectIntentNotFound
	}
	if !validMessageSourceKey(card.ContentHash) || !validMessageSourceKey(card.SourceKey) ||
		intent.TargetRef == "" || card.ConversationRef != intent.TargetRef ||
		intent.SendFingerprint != card.ContentHash {
		return nil, ErrEffectIntentConflict
	}
	switch intent.Primitive {
	case primitiveChatSendWechatInvite:
		if card.CardType != "wechatExchange" || card.CardState != "pending" ||
			card.InterviewStartsAtMs != nil || card.InterviewEndsAtMs != nil || card.InterviewMethod != nil {
			return nil, ErrEffectIntentConflict
		}
	case primitiveChatSendInviteCard:
		if card.CardType != "interviewInvite" || card.CardState != "unknown" ||
			validateMessageInterview(
				"card", card.CardType,
				card.InterviewStartsAtMs, card.InterviewEndsAtMs, card.InterviewMethod,
			) != nil {
			return nil, ErrEffectIntentConflict
		}
	default:
		return nil, ErrEffectIntentConflict
	}

	sameSemantic := func(message Message) bool {
		return message.RetractedAt == nil &&
			message.Platform == intent.Platform &&
			message.AccountRef == intent.AccountRef &&
			message.ConversationRef == intent.TargetRef &&
			message.Direction == "out" &&
			message.Kind == "card" &&
			message.ContentHash == card.ContentHash &&
			message.CardType == card.CardType &&
			sameOptionalInt64(message.InterviewStartsAtMs, card.InterviewStartsAtMs) &&
			sameOptionalInt64(message.InterviewEndsAtMs, card.InterviewEndsAtMs) &&
			sameOptionalString(message.InterviewMethod, card.InterviewMethod)
	}

	var byIntent Message
	err := tx.First(&byIntent, "outbound_intent_id = ?", intent.IntentID).Error
	if err == nil {
		if !sameSemantic(byIntent) || byIntent.SourceKey == nil || *byIntent.SourceKey != card.SourceKey {
			return nil, ErrEffectIntentConflict
		}
		return &byIntent, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	key := ConversationKey{
		Platform: intent.Platform, AccountRef: intent.AccountRef, ConversationRef: intent.TargetRef,
	}
	var bySource Message
	err = tx.Where(
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND source_key = ?",
		key.Platform, key.AccountRef, key.ConversationRef, card.SourceKey,
	).First(&bySource).Error
	if err == nil {
		if !sameSemantic(bySource) ||
			(bySource.OutboundIntentID != nil && *bySource.OutboundIntentID != intent.IntentID) {
			return nil, ErrMessageSourceKeyConflict
		}
		if bySource.OutboundIntentID == nil || bySource.Origin != "self" {
			intentID := intent.IntentID
			updated := tx.Model(&Message{}).
				Where(
					"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ? AND "+activeMessageCondition,
					key.Platform, key.AccountRef, key.ConversationRef, bySource.Seq,
				).
				Updates(map[string]any{"outbound_intent_id": intentID, "origin": "self"})
			if updated.Error != nil {
				return nil, updated.Error
			}
			if updated.RowsAffected != 1 {
				return nil, ErrRecoveryStateConflict
			}
			bySource.OutboundIntentID = &intentID
			bySource.Origin = "self"
		}
		return &bySource, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	var conversation Conversation
	if err := tx.Where(conversationWhere(key), conversationArgs(key)...).First(&conversation).Error; err != nil {
		return nil, err
	}
	seq, err := nextPhysicalMessageSeqTx(tx, key)
	if err != nil {
		return nil, err
	}
	sourceKey := card.SourceKey
	intentID := intent.IntentID
	message := &Message{
		Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: key.ConversationRef,
		Seq: seq, Direction: "out", Kind: "card", ContentHash: card.ContentHash,
		CardType: card.CardType, CardState: card.CardState,
		InterviewStartsAtMs: cloneOptionalInt64(card.InterviewStartsAtMs),
		InterviewEndsAtMs:   cloneOptionalInt64(card.InterviewEndsAtMs),
		InterviewMethod:     cloneOptionalString(card.InterviewMethod),
		Origin:              "self", SourceKey: &sourceKey, OutboundIntentID: &intentID,
	}
	if err := tx.Create(message).Error; err != nil {
		return nil, err
	}
	updates := map[string]any{
		"last_message_seq": seq, "last_message_direction": "out", "last_message_kind": "card",
		"last_message_preview": "", "last_synced_at": at,
	}
	if err := tx.Model(&Conversation{}).
		Where(conversationWhere(key), conversationArgs(key)...).
		Updates(updates).Error; err != nil {
		return nil, err
	}
	return message, nil
}

func sameOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneOptionalInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func retractOutboundMessageTx(tx *gorm.DB, intent *EffectIntent, at time.Time, reason string) error {
	if intent == nil {
		return ErrEffectIntentNotFound
	}
	if reason == "" {
		return errors.New("撤回出站消息缺少原因")
	}
	if at.IsZero() {
		at = time.Now()
	}
	var message Message
	err := tx.First(&message, "outbound_intent_id = ?", intent.IntentID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	key := ConversationKey{Platform: intent.Platform, AccountRef: intent.AccountRef, ConversationRef: intent.TargetRef}
	return retractMessageTx(tx, &message, key, at, reason)
}

// retractMessageTx 是业务消息事实的共享撤回写路径。调用方必须先在自己的
// 事务内证明目标行与撤回理由；本函数只负责保留首次撤回事实，并把活动会话尾
// 刷新到仍有效的最高物理序号。它不提交事务，也不生成新的业务事实。
func retractMessageTx(
	tx *gorm.DB,
	message *Message,
	key ConversationKey,
	at time.Time,
	reason string,
) error {
	if message == nil {
		return ErrRecoveryStateConflict
	}
	if reason == "" {
		return errors.New("撤回消息缺少原因")
	}
	if at.IsZero() {
		at = time.Now()
	}
	// 重复撤回是幂等读：首次原因与时间是不可变审计事实，
	// 后续帧不得用另一个理由覆盖。
	if message.RetractedAt != nil {
		return nil
	}
	marked := tx.Model(&Message{}).
		Where("platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ? AND "+activeMessageCondition,
			message.Platform, message.AccountRef, message.ConversationRef, message.Seq).
		Updates(map[string]any{"retracted_at": at, "retraction_reason": reason})
	if marked.Error != nil {
		return marked.Error
	}
	if marked.RowsAffected != 1 {
		return ErrRecoveryStateConflict
	}
	return refreshConversationActiveTailTx(tx, key, at)
}

func refreshConversationActiveTailTx(tx *gorm.DB, key ConversationKey, at time.Time) error {
	var latest Message
	err := tx.Where(conversationWhere(key), conversationArgs(key)...).
		Where(activeMessageCondition).Order("seq DESC").First(&latest).Error
	updates := map[string]any{"last_synced_at": at}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		updates["last_message_seq"] = int64(0)
		updates["last_message_direction"] = ""
		updates["last_message_kind"] = ""
		updates["last_message_preview"] = ""
	} else if err != nil {
		return err
	} else {
		preview := ""
		if latest.Text != nil {
			preview = *latest.Text
		}
		updates["last_message_seq"] = latest.Seq
		updates["last_message_direction"] = latest.Direction
		updates["last_message_kind"] = latest.Kind
		updates["last_message_preview"] = preview
	}
	return tx.Model(&Conversation{}).Where(conversationWhere(key), conversationArgs(key)...).Updates(updates).Error
}
