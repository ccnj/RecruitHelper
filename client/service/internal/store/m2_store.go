package store

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"recruithelper/client/service/internal/communication"
)

var (
	ErrAccountNotFound             = errors.New("账号不存在")
	ErrPrincipalAlreadyBound       = errors.New("当前平台主体已经绑定到另一个账号")
	ErrAccountPrincipalMismatch    = errors.New("既有账号根不得改绑为另一个平台主体")
	ErrConversationNotFound        = errors.New("会话不存在")
	ErrConversationNotTracked      = errors.New("会话尚未进入 tracked")
	ErrConversationAlreadyAdopted  = errors.New("会话已经完成收编")
	ErrConversationVersionConflict = errors.New("会话账本尾已变化")
	ErrPeerIdentityConflict        = errors.New("候选人 platformUserRef 冲突")
	ErrPeerIdentityRequired        = errors.New("收编会话缺少 platformUserRef")
	ErrTrackingStateCorrupt        = errors.New("conversation 与 tracked intent 状态不一致")
	ErrHistoricalBaselineEmpty     = errors.New("历史基线重建没有可写入消息")
	ErrHistoricalBaselineNoRound   = errors.New("历史基线重建必须归属巡检轮")
	ErrDuplicateConversationEntry  = errors.New("列表快照含重复 conversationRef")
	ErrPatrolRoundNotFound         = errors.New("巡检轮不存在")
	ErrCardTransitionRoundRequired = errors.New("卡片状态跃迁必须归属巡检轮")
	ErrCardTransitionNotFound      = errors.New("卡片状态跃迁事实不存在")
	ErrCardTransitionCorrupt       = errors.New("卡片状态跃迁事实与活动消息账本不一致")
	ErrInvalidMessageSourceKey     = errors.New("消息 sourceKey 必须是 64 位小写 hex")
	ErrDomainBusy                  = errors.New("串行域已有在途命令")
	ErrLogicalDispatchNotFound     = errors.New("逻辑派发不存在")
	ErrLineageConflict             = errors.New("逻辑派发链冲突")
	ErrLineageCorrupt              = errors.New("逻辑派发链损坏")
)

// AccountKey 是所有账号级 repository API 的显式平台维度键。
type AccountKey struct {
	Platform   string
	AccountRef string
}

// ConversationKey 是所有会话/消息 API 的正式身份键。conversationRef 仅在所属平台账号内有意义。
type ConversationKey struct {
	Platform        string
	AccountRef      string
	ConversationRef string
}

// CardTransitionKey 是卡片跃迁事实的稳定确认键。同一消息允许依次发生
// 多个不同跃迁，但完全相同的 from→to 事实只能入账一次。
type CardTransitionKey struct {
	Platform        string
	AccountRef      string
	ConversationRef string
	MessageSeq      int64
	FromState       string
	ToState         string
}

// PendingCardTransition 保留未确认跃迁及其对应的活动卡片消息原始事实。
// 调度层只能从这两个事实做平台中立归一化，不能用 JOIN 投影出的零散字段
// 重建或猜测卡片身份。
type PendingCardTransition struct {
	Transition CardTransitionFact
	Message    Message
}

func (f CardTransitionFact) Key() CardTransitionKey {
	return CardTransitionKey{
		Platform: f.Platform, AccountRef: f.AccountRef, ConversationRef: f.ConversationRef,
		MessageSeq: f.MessageSeq, FromState: f.FromState, ToState: f.ToState,
	}
}

func prepareRootCmd(c *CmdRecord) {
	if c == nil {
		return
	}
	if c.LogicalDispatchID == "" {
		c.LogicalDispatchID = c.MsgID
	}
	if c.ParentMsgID == nil {
		c.LineageDepth = 0
	}
}

func createRootCmd(tx *gorm.DB, c *CmdRecord) error {
	if c == nil || c.MsgID == "" {
		return errors.New("命令/msgId 不能为空")
	}
	if c.ParentMsgID != nil || c.ReplacementMsgID != nil {
		return ErrLineageConflict
	}
	prepareRootCmd(c)
	var n int64
	if err := tx.Model(&CmdRecord{}).Where("logical_dispatch_id = ?", c.LogicalDispatchID).Count(&n).Error; err != nil {
		return err
	}
	if n != 0 {
		return ErrLineageConflict
	}
	return tx.Create(c).Error
}

// ---------- 账号与身份绑定 ----------

func (s *Store) CreateAccount(a *Account) error {
	if a == nil || a.Platform == "" || a.AccountRef == "" {
		return errors.New("账号 platform/accountRef 不能为空")
	}
	if a.IdentityState == "" {
		a.IdentityState = IdentityUnbound
	}
	if !validIdentityState(a.IdentityState) {
		return fmt.Errorf("非法 identity state %q", a.IdentityState)
	}
	return s.db.Create(a).Error
}

func (s *Store) AccountByKey(key AccountKey) (*Account, error) {
	var a Account
	err := s.db.First(&a, "platform = ? AND account_ref = ?", key.Platform, key.AccountRef).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Store) Accounts() ([]Account, error) {
	var out []Account
	err := s.db.Order("platform, account_ref").Find(&out).Error
	return out, err
}

// BindAccountObservation 把真人确认过的 probe 观测原子绑定到账号根。
// reusePrincipal 只用于“绑定当前账号”的幂等入口：同一指纹已存在时复用原 accountRef；
// 指定 accountRef 的重新绑定则不允许偷偷吞并到另一账号。
func (s *Store) BindAccountObservation(
	key AccountKey,
	handID, fingerprint, session, bootID string,
	at time.Time,
	reusePrincipal bool,
) (*Account, bool, error) {
	if key.Platform == "" || key.AccountRef == "" {
		return nil, false, errors.New("账号 platform/accountRef 不能为空")
	}
	if handID == "" || fingerprint == "" || session == "" || bootID == "" {
		return nil, false, errors.New("绑定观测缺少 hand/fingerprint/session/bootId")
	}
	if at.IsZero() {
		at = time.Now()
	}
	var bound Account
	created := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var principal Account
		principalErr := tx.First(&principal,
			"platform = ? AND principal_fingerprint = ?", key.Platform, fingerprint).Error
		if principalErr == nil {
			if principal.AccountRef != key.AccountRef && !reusePrincipal {
				return ErrPrincipalAlreadyBound
			}
			bound = principal
			key.AccountRef = principal.AccountRef
		} else if !errors.Is(principalErr, gorm.ErrRecordNotFound) {
			return principalErr
		} else {
			findErr := tx.First(&bound,
				"platform = ? AND account_ref = ?", key.Platform, key.AccountRef).Error
			if errors.Is(findErr, gorm.ErrRecordNotFound) {
				bound = Account{Platform: key.Platform, AccountRef: key.AccountRef}
				created = true
			} else if findErr != nil {
				return findErr
			} else if bound.PrincipalFingerprint != nil && *bound.PrincipalFingerprint != fingerprint {
				// accountRef 是该平台主体全部 Conversation/Message/TrackedIntent
				// 的账本根。切号必须创建新根，绝不能原地覆盖指纹后复用旧账本。
				return ErrAccountPrincipalMismatch
			}
		}

		fp := fingerprint
		bound.BoundHandID = handID
		bound.PrincipalFingerprint = &fp
		bound.IdentityState = IdentityVerified
		bound.IdentityVerifiedAt = &at
		bound.IdentitySession = session
		bound.IdentityBootID = bootID
		bound.IdentityReason = ""
		if created {
			return tx.Create(&bound).Error
		}
		return tx.Save(&bound).Error
	})
	if err != nil {
		return nil, false, err
	}
	return &bound, created, nil
}

// MutateAccount 在单写事务里更新账号 actor 状态,供每日开启、暂停与调度时钟复用。
func (s *Store) MutateAccount(key AccountKey, mutate func(*Account) error) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var a Account
		if err := tx.First(&a, "platform = ? AND account_ref = ?", key.Platform, key.AccountRef).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAccountNotFound
			}
			return err
		}
		if err := mutate(&a); err != nil {
			return err
		}
		if !validIdentityState(a.IdentityState) {
			return fmt.Errorf("非法 identity state %q", a.IdentityState)
		}
		return tx.Save(&a).Error
	})
}

// BindAccountPrincipal 由真人确认后绑定不透明指纹。相同平台下同一指纹只能绑定一个 accountRef。
func (s *Store) BindAccountPrincipal(key AccountKey, handID, fingerprint, session, bootID string, at time.Time) error {
	if fingerprint == "" {
		return errors.New("principal fingerprint 不能为空")
	}
	if at.IsZero() {
		at = time.Now()
	}
	return s.MutateAccount(key, func(a *Account) error {
		fp := fingerprint
		a.PrincipalFingerprint = &fp
		a.BoundHandID = handID
		a.IdentityState = IdentityVerified
		a.IdentityVerifiedAt = &at
		a.IdentitySession = session
		a.IdentityBootID = bootID
		a.IdentityReason = ""
		return nil
	})
}

// SetAccountIdentityState 只改变可观测/失效状态,不会因页面暂时不在而抹掉已绑定指纹。
func (s *Store) SetAccountIdentityState(key AccountKey, state IdentityState, reason string) error {
	if !validIdentityState(state) {
		return fmt.Errorf("非法 identity state %q", state)
	}
	return s.MutateAccount(key, func(a *Account) error {
		a.IdentityState = state
		a.IdentityReason = reason
		if state != IdentityVerified {
			a.IdentitySession = ""
			a.IdentityBootID = ""
		}
		return nil
	})
}

func validIdentityState(s IdentityState) bool {
	switch s {
	case IdentityUnbound, IdentityVerified, IdentityUnobservable, IdentityInvalid:
		return true
	default:
		return false
	}
}

// ---------- 命令上下文、串行域与逻辑重派链 ----------

// CreateCmdIfDomainAvailable 把“检查域空闲+创建 queued 命令”放在同一个 SQLite 单写事务里。
// readonly 不占域;intrusive/effectful 只受未终态命令的串行约束。
func (s *Store) CreateCmdIfDomainAvailable(c *CmdRecord) error {
	if c == nil {
		return errors.New("命令不能为空")
	}
	prepareRootCmd(c)
	return s.db.Transaction(func(tx *gorm.DB) error {
		return createCmdIfDomainAvailableTx(tx, c)
	})
}

func createCmdIfDomainAvailableTx(tx *gorm.DB, c *CmdRecord) error {
	if c.Class != "readonly" && c.Domain == "" {
		return errors.New("驱动命令 domain 不能为空")
	}
	if c.Class != "readonly" && c.Domain != "" {
		var n int64
		if err := tx.Model(&CmdRecord{}).
			Where("domain = ? AND status IN ?", c.Domain, nonTerminalStatuses).
			Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return ErrDomainBusy
		}
	}
	return createRootCmd(tx, c)
}

// ReplaceCmd 原子完成“旧物理命令终局化 + 建立替代命令 + 推进 logical leaf”。
// 逻辑等待者因而永远看不到旧命令已 void、替代命令却尚未入账的中间态。
func (s *Store) ReplaceCmd(parentMsgID string, parentStatus CmdStatus, reason string, child *CmdRecord) error {
	if child == nil || child.MsgID == "" {
		return errors.New("替代命令/msgId 不能为空")
	}
	if !parentStatus.Terminal() {
		return errors.New("被替代命令必须推进到终局")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var parent CmdRecord
		if err := tx.First(&parent, "msg_id = ?", parentMsgID).Error; err != nil {
			return err
		}
		if parent.Status.Terminal() || parent.ReplacementMsgID != nil {
			return ErrLineageConflict
		}
		if parent.LogicalDispatchID == "" {
			parent.LogicalDispatchID = parent.MsgID
		}
		if err := validateReplacementIntent(&parent, child); err != nil {
			return err
		}

		inheritReplacement(&parent, child)
		parentRef := parent.MsgID
		child.ParentMsgID = &parentRef
		child.ReplacementMsgID = nil
		child.LogicalDispatchID = parent.LogicalDispatchID
		child.LineageDepth = parent.LineageDepth + 1
		if child.Status == "" {
			child.Status = CmdQueued
		}

		childRef := child.MsgID
		parent.ReplacementMsgID = &childRef
		parent.Status = parentStatus
		parent.SuspectReason = reason
		now := time.Now()
		parent.TerminalAt = &now
		if err := tx.Save(&parent).Error; err != nil {
			return err
		}
		if err := tx.Create(child).Error; err != nil {
			return err
		}
		return nil
	})
}

func inheritReplacement(parent, child *CmdRecord) {
	// 替代链只更换物理派发身份与会话锚点,不得更改业务意图。
	// 这里统一从父节点复制,使重派不依赖调用者手抄 context/args。
	child.Name = parent.Name
	child.Class = parent.Class
	child.IdemKey = parent.IdemKey
	child.Domain = parent.Domain
	child.Platform = parent.Platform
	child.AccountRef = parent.AccountRef
	child.ExpectedPrincipalFingerprint = parent.ExpectedPrincipalFingerprint
	child.ContextJSON = parent.ContextJSON
	child.Args = parent.Args
	child.Guards = parent.Guards
	child.IntentID = parent.IntentID
	if child.HandID == "" {
		child.HandID = parent.HandID
	}
	if child.WitnessStoreIDAtDispatch == "" {
		child.WitnessStoreIDAtDispatch = parent.WitnessStoreIDAtDispatch
	}
	child.ExecBudgetMs = parent.ExecBudgetMs
	child.RedispatchN = parent.RedispatchN + 1
	child.Status = CmdQueued
	child.Attempt = 0
	child.SentAt = nil
	child.ErrorCode = ""
	child.SideEffect = ""
	child.ResultBody = ""
	child.SuspectReason = ""
	child.PreReconcileStatus = ""
	child.ReconcileSession = ""
	child.ReconcileBootID = ""
	child.ReconcileNextAt = nil
	child.QueryMsgID = ""
	child.QuerySentAt = nil
	child.ReportState = ""
	child.ReportBody = ""
	child.ReportedAt = nil
	child.RecoveryAuthorized = false
	child.VerificationN = parent.VerificationN
	child.VerificationReason = ""
	child.VerificationNextAt = nil
	child.VerificationForMsgID = parent.VerificationForMsgID
	child.VerificationChildMsgID = ""
	child.TerminalAt = nil
}

func validateReplacementIntent(parent, child *CmdRecord) error {
	conflicts := []bool{
		child.Name != "" && child.Name != parent.Name,
		child.Class != "" && child.Class != parent.Class,
		child.IdemKey != "" && child.IdemKey != parent.IdemKey,
		child.Domain != "" && child.Domain != parent.Domain,
		child.Platform != "" && child.Platform != parent.Platform,
		child.AccountRef != "" && child.AccountRef != parent.AccountRef,
		child.ExpectedPrincipalFingerprint != "" && child.ExpectedPrincipalFingerprint != parent.ExpectedPrincipalFingerprint,
		child.ContextJSON != "" && child.ContextJSON != parent.ContextJSON,
		child.Args != "" && child.Args != parent.Args,
		child.Guards != "" && child.Guards != parent.Guards,
		child.IntentID != "" && child.IntentID != parent.IntentID,
		child.WitnessStoreIDAtDispatch != "" && child.WitnessStoreIDAtDispatch != parent.WitnessStoreIDAtDispatch,
		child.VerificationForMsgID != "" && child.VerificationForMsgID != parent.VerificationForMsgID,
		child.LogicalDispatchID != "" && child.LogicalDispatchID != parent.LogicalDispatchID,
		child.ParentMsgID != nil,
		child.ReplacementMsgID != nil,
		child.Status != "" && child.Status != CmdQueued,
		child.RedispatchN != 0 && child.RedispatchN != parent.RedispatchN+1,
		child.TerminalAt != nil,
	}
	for _, conflict := range conflicts {
		if conflict {
			return ErrLineageConflict
		}
	}
	return nil
}

type LogicalDispatchState struct {
	LogicalDispatchID string
	Leaf              CmdRecord
	Settled           bool
	Length            int
}

// ResultCommandMutation 是 result 入账事务的命令变更计划。Save=false 用于
// 迟到 result：只持久化上行 msgId 去重证词，不触碰已终局命令。
// Replacement 非空时，父命令的终局化与 child 建链在同一 SQLite 事务中完成。
type ResultCommandMutation struct {
	Save              bool
	Replacement       *CmdRecord
	ReplacementReason string
	// KeepCommandOpen 只供真实 SX 的 possible/confirmed 后续验证轨使用。
	// 手的物理 result 已终局，但脑的权威意图仍处于 verifying；
	// 普通命令不得设置。
	KeepCommandOpen bool
	Effect          *EffectResultMutation
}

type EffectResultMutation struct {
	IntentStatus  EffectIntentStatus
	Append        bool
	Retract       bool
	Greeting      *GreetingResultMutation
	Card          *CardResultMutation
	WechatContact *WechatContactResultMutation
	Text          string
	ContentHash   string
	ObservedAtMs  int64
	// PlatformTsMs 是唯一命中的出站消息在平台消息视图中的时间戳,
	// 与 ObservedAtMs(手完成观察的本机时刻)不同源:它只可来自 result
	// data 的 tsApprox 字段,平台不提供可解析时间时保持 nil,任何一层
	// 都不得用本机时钟填充。
	PlatformTsMs *int64
	Reason       string
}

type CardResultMutation struct {
	ConversationRef     string
	CardType            string
	CardState           string
	ContentHash         string
	SourceKey           string
	InterviewStartsAtMs *int64
	InterviewEndsAtMs   *int64
	InterviewMethod     *string
	// PlatformTsMs 语义同 EffectResultMutation.PlatformTsMs。
	PlatformTsMs *int64
}

type WechatContactResultMutation struct {
	ConversationRef   string
	RequestSourceKey  string
	ExchangeSourceKey string
	PeerWechat        string
	ObservedAtMs      int64
}

type ApplyResultMessageResult struct {
	CommandFound     bool
	AlreadyProcessed bool
	Replacement      *CmdRecord
}

// ApplyResultMessage 原子完成三件事：登记上行 result msgId、终局化物理
// 命令、必要时铸造 replacement leaf。手只能在该事务成功后收到 ack，
// 因而不存在“命令已终局但 processed_msgs 未落库”的崩溃窗口。
func (s *Store) ApplyResultMessage(
	ref, resultMsgID, kind, handID string,
	mutate func(*CmdRecord) (ResultCommandMutation, error),
) (*ApplyResultMessageResult, error) {
	if ref == "" || resultMsgID == "" || kind == "" || handID == "" {
		return nil, errors.New("result 入账缺少 ref/msgId/kind/handId")
	}
	out := &ApplyResultMessageResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		inserted := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&ProcessedMsg{
			MsgID: resultMsgID, Kind: kind, HandID: handID, ProcessedAt: time.Now(),
		})
		if inserted.Error != nil {
			return inserted.Error
		}
		if inserted.RowsAffected == 0 {
			out.AlreadyProcessed = true
			return nil
		}

		var command CmdRecord
		if err := tx.First(&command, "msg_id = ?", ref).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		out.CommandFound = true
		wasTerminal := command.Status.Terminal()
		hadReplacement := command.ReplacementMsgID != nil
		plan, err := mutate(&command)
		if err != nil {
			return err
		}
		if !plan.Save {
			if plan.Replacement != nil {
				return ErrLineageConflict
			}
			return nil
		}
		if !command.Status.Terminal() && !plan.KeepCommandOpen {
			return errors.New("result 必须把物理命令推进到终局")
		}
		if plan.KeepCommandOpen && (command.IntentID == "" || command.Status != CmdVerifying) {
			return errors.New("仅真实 SX verifying 可在 result 后保持开放")
		}
		if command.Status.Terminal() && command.TerminalAt == nil {
			now := time.Now()
			command.TerminalAt = &now
		}

		if plan.Replacement != nil {
			if wasTerminal || hadReplacement {
				return ErrLineageConflict
			}
			child := plan.Replacement
			if child.MsgID == "" {
				return errors.New("replacement msgId 不能为空")
			}
			if err := validateReplacementIntent(&command, child); err != nil {
				return err
			}
			inheritReplacement(&command, child)
			parentRef := command.MsgID
			child.ParentMsgID = &parentRef
			child.ReplacementMsgID = nil
			child.LogicalDispatchID = command.LogicalDispatchID
			child.LineageDepth = command.LineageDepth + 1
			childRef := child.MsgID
			command.ReplacementMsgID = &childRef
			command.SuspectReason = plan.ReplacementReason
			out.Replacement = child
		}

		if err := tx.Save(&command).Error; err != nil {
			return err
		}
		if plan.Effect != nil {
			if command.IntentID == "" {
				return ErrEffectIntentConflict
			}
			var intent EffectIntent
			if err := tx.First(&intent, "intent_id = ?", command.IntentID).Error; err != nil {
				return err
			}
			if intent.IdemKey != command.IdemKey || intent.RootMsgID != command.LogicalDispatchID {
				return ErrEffectIntentConflict
			}
			intent.Status = plan.Effect.IntentStatus
			intent.SuspectReason = plan.Effect.Reason
			intent.ResultMsgID = resultMsgID
			effectAt := s.now()
			if plan.Effect.Append && plan.Effect.Retract {
				return ErrEffectIntentConflict
			}
			if plan.Effect.Greeting != nil && (plan.Effect.Append || plan.Effect.Retract ||
				plan.Effect.Card != nil || plan.Effect.WechatContact != nil) {
				return ErrEffectIntentConflict
			}
			if plan.Effect.Card != nil && (plan.Effect.Append || plan.Effect.Retract ||
				plan.Effect.WechatContact != nil) {
				return ErrEffectIntentConflict
			}
			if plan.Effect.WechatContact != nil &&
				(plan.Effect.Append || plan.Effect.Retract) {
				return ErrEffectIntentConflict
			}
			if plan.Effect.Append {
				message, err := appendOutboundMessageTx(tx, &intent, plan.Effect.Text,
					plan.Effect.ContentHash, plan.Effect.PlatformTsMs, effectAt)
				if err != nil {
					return err
				}
				intent.ResultMessageSeq = &message.Seq
				intent.SendFingerprint = plan.Effect.ContentHash
				intent.ResolvedAt = &effectAt
			}
			if plan.Effect.Retract {
				if err := retractOutboundMessageTx(tx, &intent, effectAt, messageRetractionReasonAuthoritativeSafeTerminal); err != nil {
					return err
				}
				intent.ResultMessageSeq = nil
			}
			if plan.Effect.Greeting != nil {
				message, err := applyGreetingResultTx(tx, &intent, *plan.Effect.Greeting, effectAt)
				if err != nil {
					return err
				}
				if message != nil {
					intent.ResultMessageSeq = &message.Seq
				}
			}
			if plan.Effect.Card != nil {
				message, err := applyCardResultTx(tx, &intent, *plan.Effect.Card, effectAt)
				if err != nil {
					return err
				}
				intent.ResultMessageSeq = &message.Seq
				intent.ResolvedAt = &effectAt
			}
			if plan.Effect.WechatContact != nil {
				if _, _, err := applyWechatContactResultTx(
					tx,
					&intent,
					*plan.Effect.WechatContact,
					effectAt,
				); err != nil {
					return err
				}
				intent.ResultMessageSeq = nil
				intent.ResolvedAt = &effectAt
			}
			if plan.Effect.IntentStatus == EffectIntentFailed || plan.Effect.IntentStatus == EffectIntentSuspect ||
				plan.Effect.IntentStatus == EffectIntentResolvedOk || plan.Effect.IntentStatus == EffectIntentResolvedFailed {
				intent.ResolvedAt = &effectAt
			}
			if err := tx.Save(&intent).Error; err != nil {
				return err
			}
			if err := applyM5AutomaticEffectStatusTx(tx, &intent, effectAt); err != nil {
				return err
			}
		}
		if out.Replacement != nil {
			return tx.Create(out.Replacement).Error
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// LogicalDispatch 返回持久化链的当前叶子。Settled 只由最终叶子的终局决定;
// 中间节点即使为 void 也不会完成逻辑等待。
func (s *Store) LogicalDispatch(logicalID string) (*LogicalDispatchState, error) {
	var records []CmdRecord
	if err := s.db.Where("logical_dispatch_id = ?", logicalID).Order("lineage_depth, created_at, msg_id").Find(&records).Error; err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, ErrLogicalDispatchNotFound
	}
	leaf, err := validateLineage(records)
	if err != nil {
		return nil, err
	}
	return &LogicalDispatchState{
		LogicalDispatchID: logicalID,
		Leaf:              *leaf,
		Settled:           leaf.Status.Terminal(),
		Length:            len(records),
	}, nil
}

func (s *Store) CmdLineage(logicalID string) ([]CmdRecord, error) {
	var records []CmdRecord
	err := s.db.Where("logical_dispatch_id = ?", logicalID).Order("lineage_depth, created_at, msg_id").Find(&records).Error
	return records, err
}

func validateLineage(records []CmdRecord) (*CmdRecord, error) {
	byID := make(map[string]*CmdRecord, len(records))
	for i := range records {
		byID[records[i].MsgID] = &records[i]
	}
	var roots, leaves []*CmdRecord
	for i := range records {
		r := &records[i]
		if r.ParentMsgID == nil {
			roots = append(roots, r)
		} else {
			p := byID[*r.ParentMsgID]
			if p == nil || p.ReplacementMsgID == nil || *p.ReplacementMsgID != r.MsgID || r.LineageDepth != p.LineageDepth+1 {
				return nil, ErrLineageCorrupt
			}
		}
		if r.ReplacementMsgID == nil {
			leaves = append(leaves, r)
		} else {
			child := byID[*r.ReplacementMsgID]
			if child == nil || child.ParentMsgID == nil || *child.ParentMsgID != r.MsgID {
				return nil, ErrLineageCorrupt
			}
		}
	}
	if len(roots) != 1 || roots[0].LineageDepth != 0 || len(leaves) != 1 {
		return nil, ErrLineageCorrupt
	}
	return leaves[0], nil
}

// ---------- 巡检轮 ----------

func (s *Store) CreatePatrolRound(r *PatrolRound) error {
	if r == nil || r.Platform == "" || r.AccountRef == "" || r.RoundID == "" {
		return errors.New("巡检轮 platform/accountRef/roundId 不能为空")
	}
	if r.Status == "" {
		r.Status = "running"
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now()
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := requireAccount(tx, AccountKey{Platform: r.Platform, AccountRef: r.AccountRef}); err != nil {
			return err
		}
		return tx.Create(r).Error
	})
}

// BeginPatrolRound 把“创建 running 轮次”和“消费此前的 dirty 调度提示”放进
// 同一个单写事务。调用方会用自己的短锁把传感事件线性化在本事务之前或之后；
// 这样创建轮次失败不会丢提示，轮次开始后到达的新事件也不会被误当成本轮已消费。
func (s *Store) BeginPatrolRound(r *PatrolRound, nextPatrolAt time.Time) error {
	if r == nil || r.Platform == "" || r.AccountRef == "" || r.RoundID == "" {
		return errors.New("巡检轮 platform/accountRef/roundId 不能为空")
	}
	if nextPatrolAt.IsZero() {
		return errors.New("巡检轮 nextPatrolAt 不能为空")
	}
	if r.Status == "" {
		r.Status = "running"
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now()
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		key := AccountKey{Platform: r.Platform, AccountRef: r.AccountRef}
		if err := requireAccount(tx, key); err != nil {
			return err
		}
		if err := tx.Create(r).Error; err != nil {
			return err
		}
		return tx.Model(&Account{}).
			Where("platform = ? AND account_ref = ?", key.Platform, key.AccountRef).
			Updates(map[string]any{
				"dirty_hint":     false,
				"next_patrol_at": nextPatrolAt,
			}).Error
	})
}

func (s *Store) MutatePatrolRound(platform, accountRef, roundID string, mutate func(*PatrolRound) error) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var r PatrolRound
		if err := tx.First(&r, "platform = ? AND account_ref = ? AND round_id = ?", platform, accountRef, roundID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPatrolRoundNotFound
			}
			return err
		}
		if err := mutate(&r); err != nil {
			return err
		}
		return tx.Save(&r).Error
	})
}

func (s *Store) PatrolRoundByKey(platform, accountRef, roundID string) (*PatrolRound, error) {
	var r PatrolRound
	err := s.db.First(&r, "platform = ? AND account_ref = ? AND round_id = ?", platform, accountRef, roundID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) RunningPatrolRounds() ([]PatrolRound, error) {
	var rounds []PatrolRound
	err := s.db.Where("status = ?", "running").Order("started_at").Find(&rounds).Error
	return rounds, err
}

// RecentPatrolRounds 只为账号 actor 恢复少量跨重启安全计数提供最近轮次。
// limit 被刻意限制为很小的窗口，避免这个接口演变成另一套巡检查询面。
func (s *Store) RecentPatrolRounds(key AccountKey, limit int) ([]PatrolRound, error) {
	if limit <= 0 || limit > 10 {
		return nil, errors.New("最近巡检轮 limit 必须在 1..10")
	}
	var rounds []PatrolRound
	err := s.db.Where("platform = ? AND account_ref = ?", key.Platform, key.AccountRef).
		Order("started_at DESC, round_id DESC").Limit(limit).Find(&rounds).Error
	return rounds, err
}

// RecoverRunningPatrolRounds 在脑启动时收束上次进程遗留的 running 轮次，并把账号拉回
// 下一次正常 Tick。命令账本仍由 dispatch.Recover 独立收编，两者不互相猜测。
func (s *Store) RecoverRunningPatrolRounds(at time.Time) (int, error) {
	if at.IsZero() {
		at = time.Now()
	}
	recovered := 0
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var rounds []PatrolRound
		if err := tx.Where("status = ?", "running").Find(&rounds).Error; err != nil {
			return err
		}
		for i := range rounds {
			round := &rounds[i]
			round.Status = "failed"
			round.Stage = "interrupted"
			round.ErrorCode = "BRAIN_RESTART"
			round.FinishedAt = &at
			if err := tx.Save(round).Error; err != nil {
				return err
			}
			if err := tx.Model(&Account{}).
				Where("platform = ? AND account_ref = ?", round.Platform, round.AccountRef).
				Updates(map[string]any{
					"dirty_hint": true, "next_patrol_at": at,
				}).Error; err != nil {
				return err
			}
			recovered++
		}
		return nil
	})
	return recovered, err
}

// ---------- 会话列表索引与 tracked intent ----------

type ListIndexEntry struct {
	ConversationRef      string
	PlatformUserRef      string
	PeerDisplayName      string
	UnreadCount          int
	LastMessageDirection string
	LastMessageKind      string
	LastMessagePreview   string
	LastActivityMs       *int64
}

type SaveConversationListRequest struct {
	Platform   string
	AccountRef string
	RoundID    string
	ObservedAt time.Time
	Complete   bool
	Entries    []ListIndexEntry
}

// SaveConversationList 原子落一份列表快照。已有非空 platformUserRef 不能被另一身份静默覆盖。
func (s *Store) SaveConversationList(req SaveConversationListRequest) error {
	if req.Platform == "" || req.AccountRef == "" {
		return errors.New("列表快照 platform/accountRef 不能为空")
	}
	if req.ObservedAt.IsZero() {
		req.ObservedAt = time.Now()
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := requireAccount(tx, AccountKey{Platform: req.Platform, AccountRef: req.AccountRef}); err != nil {
			return err
		}
		if req.RoundID != "" {
			if err := requirePatrolRound(tx, req.Platform, req.AccountRef, req.RoundID); err != nil {
				return err
			}
		}
		seen := make(map[string]struct{}, len(req.Entries))
		for _, item := range req.Entries {
			if item.ConversationRef == "" {
				return errors.New("conversationRef 不能为空")
			}
			if _, ok := seen[item.ConversationRef]; ok {
				return fmt.Errorf("%w: %s", ErrDuplicateConversationEntry, item.ConversationRef)
			}
			seen[item.ConversationRef] = struct{}{}
			key := ConversationKey{Platform: req.Platform, AccountRef: req.AccountRef, ConversationRef: item.ConversationRef}
			var c Conversation
			err := tx.Where(conversationWhere(key), conversationArgs(key)...).First(&c).Error
			switch {
			case errors.Is(err, gorm.ErrRecordNotFound):
				c = Conversation{
					Platform: req.Platform, AccountRef: req.AccountRef, ConversationRef: item.ConversationRef,
					PlatformUserRef: item.PlatformUserRef, PeerDisplayName: item.PeerDisplayName,
					UnreadCount: item.UnreadCount, LastMessageDirection: item.LastMessageDirection,
					LastMessageKind: item.LastMessageKind, LastMessagePreview: item.LastMessagePreview,
					LastActivityMs: item.LastActivityMs, LastListedRoundID: req.RoundID,
					LastListedAt: &req.ObservedAt, TrackingState: TrackingUntracked,
				}
				if err := tx.Create(&c).Error; err != nil {
					return err
				}
			case err != nil:
				return err
			default:
				if c.PlatformUserRef != "" && item.PlatformUserRef != "" && c.PlatformUserRef != item.PlatformUserRef {
					return fmt.Errorf("%w: conversation=%s", ErrPeerIdentityConflict, item.ConversationRef)
				}
				updates := map[string]any{
					"peer_display_name": item.PeerDisplayName, "unread_count": item.UnreadCount,
					"last_message_direction": item.LastMessageDirection, "last_message_kind": item.LastMessageKind,
					"last_message_preview": item.LastMessagePreview, "last_activity_ms": item.LastActivityMs,
					"last_listed_round_id": req.RoundID, "last_listed_at": req.ObservedAt,
				}
				if item.PlatformUserRef != "" {
					updates["platform_user_ref"] = item.PlatformUserRef
				}
				if err := tx.Model(&Conversation{}).Where(conversationWhere(key), conversationArgs(key)...).Updates(updates).Error; err != nil {
					return err
				}
			}
		}
		if req.RoundID != "" {
			res := tx.Model(&PatrolRound{}).
				Where("platform = ? AND account_ref = ? AND round_id = ?", req.Platform, req.AccountRef, req.RoundID).
				Updates(map[string]any{"list_complete": req.Complete, "stage": "listIndexed"})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return ErrPatrolRoundNotFound
			}
		}
		return nil
	})
}

func (s *Store) ConversationByKey(key ConversationKey) (*Conversation, error) {
	var c Conversation
	err := s.db.Where(conversationWhere(key), conversationArgs(key)...).First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) ConversationsForAccount(key AccountKey) ([]Conversation, error) {
	var out []Conversation
	err := s.db.Where("platform = ? AND account_ref = ?", key.Platform, key.AccountRef).
		Order("last_listed_at DESC, conversation_ref").Find(&out).Error
	return out, err
}

func (s *Store) TrackConversation(key ConversationKey, requestedBy string, at time.Time) (*TrackedIntent, error) {
	if at.IsZero() {
		at = time.Now()
	}
	if requestedBy == "" {
		requestedBy = "user"
	}
	var out TrackedIntent
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var c Conversation
		if err := tx.Where(conversationWhere(key), conversationArgs(key)...).First(&c).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConversationNotFound
			}
			return err
		}
		var existing TrackedIntent
		err := tx.Where(conversationWhere(key), conversationArgs(key)...).First(&existing).Error
		if err == nil {
			if (existing.Status != TrackingPending && existing.Status != TrackingAdopted) || c.TrackingState != existing.Status {
				return ErrTrackingStateCorrupt
			}
			out = existing
			return nil // 幂等:重复点击不创建第二条意图,也不重置收编边界。
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if c.TrackingState != TrackingUntracked {
			return ErrTrackingStateCorrupt
		}
		out = TrackedIntent{
			Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: key.ConversationRef,
			Status: TrackingPending, RequestedBy: requestedBy, RequestedAt: at,
		}
		if err := tx.Create(&out).Error; err != nil {
			return err
		}
		return tx.Model(&Conversation{}).Where(conversationWhere(key), conversationArgs(key)...).
			Update("tracking_state", TrackingPending).Error
	})
	return &out, err
}

func (s *Store) TrackedIntentByConversation(key ConversationKey) (*TrackedIntent, error) {
	var i TrackedIntent
	err := s.db.Where(conversationWhere(key), conversationArgs(key)...).First(&i).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &i, nil
}

func (s *Store) TrackedConversations(key AccountKey) ([]Conversation, error) {
	var out []Conversation
	// TrackedIntent 是正式业务真相;Conversation.tracking_state 是事务内同步的查询投影。
	// 两者不一致的损坏行不得被误当成允许深读的 tracked 会话。
	err := s.db.Model(&Conversation{}).
		Joins("JOIN tracked_intents ON tracked_intents.platform = conversations.platform AND tracked_intents.account_ref = conversations.account_ref AND tracked_intents.conversation_ref = conversations.conversation_ref").
		Where("conversations.platform = ? AND conversations.account_ref = ?", key.Platform, key.AccountRef).
		Where("conversations.tracking_state IN ?", []TrackingState{TrackingPending, TrackingAdopted}).
		Where("tracked_intents.status = conversations.tracking_state").
		Order("conversations.conversation_ref").Find(&out).Error
	return out, err
}

// ---------- 消息账本与收编事务 ----------

const activeMessageCondition = "retracted_at IS NULL"

// nextPhysicalMessageSeqTx 从全部不可变消息事实（包括已撤回事实）
// 分配下一个序号。Conversation.LastMessageSeq 只是活动账本尾，撤回
// 后可以回到更早的活动行，因而绝不能再用它推导物理主键。
func nextPhysicalMessageSeqTx(tx *gorm.DB, key ConversationKey) (int64, error) {
	var maxSeq int64
	if err := tx.Model(&Message{}).
		Where(conversationWhere(key), conversationArgs(key)...).
		Select("COALESCE(MAX(seq), 0)").Scan(&maxSeq).Error; err != nil {
		return 0, err
	}
	return maxSeq + 1, nil
}

type MessageDraft struct {
	Direction           string
	Kind                string
	ContentHash         string
	Text                *string
	BlobRef             string
	CardType            string
	CardState           string
	InterviewStartsAtMs *int64
	InterviewEndsAtMs   *int64
	InterviewMethod     *string
	TsApproxMs          *int64
	Origin              string
	SourceKey           *string
}

type CardStateChange struct {
	Seq         int64
	ContentHash string
	FromState   string
	CardState   string
}

type ApplyConversationChangesRequest struct {
	Key             ConversationKey
	RoundID         string
	ExpectedTailSeq int64
	PlatformUserRef string
	NewMessages     []MessageDraft
	CardChanges     []CardStateChange
	Adopt           bool
	SyncedAt        time.Time
}

type ApplyConversationChangesResult struct {
	Inserted           []Message
	TailSeq            int64
	AdoptedBoundarySeq int64
}

func validateMessageDrafts(messages []MessageDraft) error {
	for _, m := range messages {
		if m.Direction == "" || m.Kind == "" || m.ContentHash == "" || m.Origin == "" {
			return errors.New("新消息 direction/kind/contentHash/origin 不能为空")
		}
		if m.SourceKey != nil && !validMessageSourceKey(*m.SourceKey) {
			return ErrInvalidMessageSourceKey
		}
		if err := validateMessageInterview(
			m.Kind, m.CardType, m.InterviewStartsAtMs, m.InterviewEndsAtMs, m.InterviewMethod,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateMessageInterview(
	kind, cardType string,
	startsAtMs, endsAtMs *int64,
	method *string,
) error {
	if startsAtMs == nil && endsAtMs == nil && method == nil {
		return nil
	}
	if kind != "card" || cardType != "interviewInvite" ||
		!communication.ValidV4InterviewShape(startsAtMs, endsAtMs, method) {
		return errors.New(
			"邀面卡参数形态与 method 不自洽:wechatVideo 必须带晚于开始的 endsAt,onsite 必须缺席 endsAt",
		)
	}
	return nil
}

func validMessageSourceKey(sourceKey string) bool {
	if len(sourceKey) != 64 {
		return false
	}
	for i := 0; i < len(sourceKey); i++ {
		ch := sourceKey[i]
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

// ApplyConversationChanges 原子应用对齐算法的输出。对齐本身在上层纯函数完成;
// store 负责乐观尾校验、卡片状态与跃迁事实、消息 append、收编边界和 round 计数不可分割。
func (s *Store) ApplyConversationChanges(req ApplyConversationChangesRequest) (*ApplyConversationChangesResult, error) {
	if req.SyncedAt.IsZero() {
		req.SyncedAt = time.Now()
	}
	if err := validateMessageDrafts(req.NewMessages); err != nil {
		return nil, err
	}
	if len(req.CardChanges) > 0 && req.RoundID == "" {
		return nil, ErrCardTransitionRoundRequired
	}
	for _, ch := range req.CardChanges {
		if ch.Seq <= 0 || ch.ContentHash == "" || ch.FromState == "" || ch.CardState == "" {
			return nil, errors.New("卡片状态更新 seq/contentHash/fromState/cardState 非法")
		}
	}

	result := &ApplyConversationChangesResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var c Conversation
		if err := tx.Where(conversationWhere(req.Key), conversationArgs(req.Key)...).First(&c).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConversationNotFound
			}
			return err
		}
		if c.LastMessageSeq != req.ExpectedTailSeq {
			return ErrConversationVersionConflict
		}
		var intent TrackedIntent
		intentErr := tx.Where(conversationWhere(req.Key), conversationArgs(req.Key)...).First(&intent).Error
		if intentErr != nil && !errors.Is(intentErr, gorm.ErrRecordNotFound) {
			return intentErr
		}
		if req.Adopt && c.TrackingState == TrackingAdopted {
			return ErrConversationAlreadyAdopted
		}
		expectedTracking := TrackingAdopted
		if req.Adopt {
			expectedTracking = TrackingPending
		}
		if errors.Is(intentErr, gorm.ErrRecordNotFound) {
			if c.TrackingState == TrackingUntracked {
				return ErrConversationNotTracked
			}
			return ErrTrackingStateCorrupt
		}
		if c.TrackingState != intent.Status {
			return ErrTrackingStateCorrupt
		}
		if c.TrackingState != expectedTracking {
			return ErrConversationNotTracked
		}
		if c.PlatformUserRef != "" && req.PlatformUserRef != "" && c.PlatformUserRef != req.PlatformUserRef {
			return ErrPeerIdentityConflict
		}
		effectivePeerRef := c.PlatformUserRef
		if req.PlatformUserRef != "" {
			effectivePeerRef = req.PlatformUserRef
		}
		if effectivePeerRef == "" {
			return ErrPeerIdentityRequired
		}
		if req.RoundID != "" {
			if err := requirePatrolRound(tx, req.Key.Platform, req.Key.AccountRef, req.RoundID); err != nil {
				return err
			}
		}

		seq := c.LastMessageSeq
		if len(req.NewMessages) > 0 {
			nextSeq, err := nextPhysicalMessageSeqTx(tx, req.Key)
			if err != nil {
				return err
			}
			seq = nextSeq - 1
		}
		inserted := make([]Message, 0, len(req.NewMessages))
		for _, draft := range req.NewMessages {
			seq++
			m := Message{
				Platform: req.Key.Platform, AccountRef: req.Key.AccountRef, ConversationRef: req.Key.ConversationRef,
				Seq: seq, Direction: draft.Direction, Kind: draft.Kind, ContentHash: draft.ContentHash,
				Text: draft.Text, BlobRef: draft.BlobRef, CardType: draft.CardType, CardState: draft.CardState,
				InterviewStartsAtMs: draft.InterviewStartsAtMs, InterviewEndsAtMs: draft.InterviewEndsAtMs,
				InterviewMethod: draft.InterviewMethod,
				TsApproxMs:      draft.TsApproxMs, Origin: draft.Origin, FirstSeenRoundID: req.RoundID,
				SourceKey: draft.SourceKey,
			}
			if err := tx.Create(&m).Error; err != nil {
				return err
			}
			inserted = append(inserted, m)
		}

		for _, change := range req.CardChanges {
			var m Message
			if err := tx.First(&m,
				"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ? AND "+activeMessageCondition,
				req.Key.Platform, req.Key.AccountRef, req.Key.ConversationRef, change.Seq).Error; err != nil {
				return err
			}
			if m.ContentHash != change.ContentHash || m.Kind != "card" {
				return fmt.Errorf("%w: card seq=%d", ErrConversationVersionConflict, change.Seq)
			}
			fromState := m.CardState
			if fromState == "" {
				fromState = "unknown"
			}
			// 相同计划在超时/重启后可能重放；已到达目标态即为幂等成功，
			// 不得追加第二条事实。若当前态是其他值，则说明计划已过时。
			if fromState == change.CardState {
				continue
			}
			if fromState != change.FromState {
				return fmt.Errorf("%w: card seq=%d state=%s expected=%s",
					ErrConversationVersionConflict, change.Seq, fromState, change.FromState)
			}
			if err := tx.Model(&Message{}).
				Where("platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ? AND "+activeMessageCondition,
					req.Key.Platform, req.Key.AccountRef, req.Key.ConversationRef, change.Seq).
				Update("card_state", change.CardState).Error; err != nil {
				return err
			}
			if err := tx.Create(&CardTransitionFact{
				Platform: req.Key.Platform, AccountRef: req.Key.AccountRef,
				ConversationRef: req.Key.ConversationRef, MessageSeq: change.Seq,
				RoundID: req.RoundID, ContentHash: m.ContentHash, CardType: m.CardType,
				FromState: fromState, ToState: change.CardState, CreatedAt: req.SyncedAt,
			}).Error; err != nil {
				return err
			}
		}

		updates := map[string]any{
			"last_message_seq": seq, "last_synced_round_id": req.RoundID, "last_synced_at": req.SyncedAt,
		}
		if req.PlatformUserRef != "" {
			updates["platform_user_ref"] = req.PlatformUserRef
		}
		boundary := c.AdoptedBoundarySeq
		if req.Adopt {
			boundary = seq
			updates["tracking_state"] = TrackingAdopted
			updates["adopted_boundary_seq"] = boundary
			if err := tx.Model(&TrackedIntent{}).Where(conversationWhere(req.Key), conversationArgs(req.Key)...).
				Updates(map[string]any{"status": TrackingAdopted, "adopted_at": req.SyncedAt}).Error; err != nil {
				return err
			}
			if err := tx.Create(&AuditEntry{
				At: req.SyncedAt, Category: "conversation_adopted", Platform: req.Key.Platform,
				AccountRef: req.Key.AccountRef, ConversationRef: req.Key.ConversationRef,
				RoundID: req.RoundID, Detail: fmt.Sprintf("adoptedBoundarySeq=%d", boundary),
			}).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&Conversation{}).Where(conversationWhere(req.Key), conversationArgs(req.Key)...).Updates(updates).Error; err != nil {
			return err
		}
		// 首次收编写入的是边界前历史,不是“本轮新增”投影,不得计入 round.NewMessageCount。
		if req.RoundID != "" && len(inserted) > 0 && !req.Adopt {
			if err := tx.Model(&PatrolRound{}).
				Where("platform = ? AND account_ref = ? AND round_id = ?", req.Key.Platform, req.Key.AccountRef, req.RoundID).
				UpdateColumn("new_message_count", gorm.Expr("new_message_count + ?", len(inserted))).Error; err != nil {
				return err
			}
		}
		result.Inserted = inserted
		result.TailSeq = seq
		result.AdoptedBoundarySeq = boundary
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func cardTransitionWhere(key CardTransitionKey) (string, []any) {
	return "platform = ? AND account_ref = ? AND conversation_ref = ? AND message_seq = ? AND from_state = ? AND to_state = ?", []any{
		key.Platform, key.AccountRef, key.ConversationRef, key.MessageSeq, key.FromState, key.ToState,
	}
}

func validateCardTransitionKey(key CardTransitionKey) error {
	if key.Platform == "" || key.AccountRef == "" || key.ConversationRef == "" ||
		key.MessageSeq <= 0 || key.FromState == "" || key.ToState == "" {
		return errors.New("卡片跃迁键 platform/accountRef/conversationRef/messageSeq/fromState/toState 不完整")
	}
	return nil
}

// PendingCardTransitions 按事实创建顺序列出未确认跃迁。读取不会删除或隐式消费；
// 调用方完成下游处理后必须显式 AcknowledgeCardTransition。
func (s *Store) PendingCardTransitions(limit int) ([]CardTransitionFact, error) {
	if limit <= 0 || limit > 500 {
		return nil, errors.New("卡片跃迁 limit 必须在 1..500")
	}
	var facts []CardTransitionFact
	err := s.db.Where("acknowledged_at IS NULL").
		Order("created_at, platform, account_ref, conversation_ref, message_seq, from_state, to_state").
		Limit(limit).Find(&facts).Error
	return facts, err
}

// PendingCardTransitionsForAccount 按账号和事实创建顺序列出仍属于活动消息账本
// 的未确认卡片跃迁。读取不确认事实；调用方只有在下游投影收敛后才可显式 ack。
func (s *Store) PendingCardTransitionsForAccount(
	key AccountKey,
	limit int,
) ([]PendingCardTransition, error) {
	if key.Platform == "" || key.AccountRef == "" {
		return nil, errors.New("账号键不完整")
	}
	if limit <= 0 || limit > 500 {
		return nil, errors.New("卡片跃迁 limit 必须在 1..500")
	}
	var out []PendingCardTransition
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var facts []CardTransitionFact
		if err := tx.Table("card_transition_facts AS transition").
			Select("transition.*").
			Joins(
				"JOIN messages AS message ON "+
					"message.platform = transition.platform AND "+
					"message.account_ref = transition.account_ref AND "+
					"message.conversation_ref = transition.conversation_ref AND "+
					"message.seq = transition.message_seq AND "+
					"message.retracted_at IS NULL",
			).
			Where(
				"transition.platform = ? AND transition.account_ref = ? AND transition.acknowledged_at IS NULL",
				key.Platform,
				key.AccountRef,
			).
			Order(
				"transition.created_at, transition.conversation_ref, transition.message_seq, " +
					"transition.from_state, transition.to_state",
			).
			Limit(limit).
			Find(&facts).Error; err != nil {
			return err
		}
		out = make([]PendingCardTransition, 0, len(facts))
		for index := range facts {
			fact := facts[index]
			var message Message
			if err := tx.First(
				&message,
				"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ? AND "+activeMessageCondition,
				fact.Platform,
				fact.AccountRef,
				fact.ConversationRef,
				fact.MessageSeq,
			).Error; err != nil {
				return err
			}
			if message.Kind != "card" ||
				message.ContentHash != fact.ContentHash ||
				message.CardType != fact.CardType {
				return fmt.Errorf(
					"%w: messageSeq=%d",
					ErrCardTransitionCorrupt,
					fact.MessageSeq,
				)
			}
			out = append(out, PendingCardTransition{
				Transition: fact,
				Message:    message,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CardTransitionByKey 返回追加事实（包括已确认事实）；未找到返回 (nil, nil)。
func (s *Store) CardTransitionByKey(key CardTransitionKey) (*CardTransitionFact, error) {
	if err := validateCardTransitionKey(key); err != nil {
		return nil, err
	}
	where, args := cardTransitionWhere(key)
	var fact CardTransitionFact
	err := s.db.Where(where, args...).First(&fact).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &fact, nil
}

// AcknowledgeCardTransition 在单个事务中持久化显式确认，但保留追加事实。
// 返回 true 表示本次首次确认；重复确认返回 false, nil。
func (s *Store) AcknowledgeCardTransition(key CardTransitionKey, acknowledgedAt time.Time) (bool, error) {
	if err := validateCardTransitionKey(key); err != nil {
		return false, err
	}
	if acknowledgedAt.IsZero() {
		acknowledgedAt = time.Now()
	}
	acknowledgedNow := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		where, args := cardTransitionWhere(key)
		var fact CardTransitionFact
		if err := tx.Where(where, args...).First(&fact).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCardTransitionNotFound
			}
			return err
		}
		if fact.AcknowledgedAt != nil {
			return nil
		}
		result := tx.Model(&CardTransitionFact{}).Where(where+" AND acknowledged_at IS NULL", args...).
			UpdateColumn("acknowledged_at", acknowledgedAt)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("卡片跃迁确认并发冲突")
		}
		acknowledgedNow = true
		return nil
	})
	return acknowledgedNow, err
}

// RebuildConversationBaselineRequest 是 deep 后仍零重叠的专用写入形态。
// 它只允许已 adopted 会话把一段无法对齐的快照收为新历史基线,
// 不重用首次 Track/Adopt 状态转换,也不产生“本轮新增”计数。
type RebuildConversationBaselineRequest struct {
	Key             ConversationKey
	RoundID         string
	ExpectedTailSeq int64
	PlatformUserRef string
	Historical      []MessageDraft
	SyncedAt        time.Time
	AuditDetail     string
}

type RebuildConversationBaselineResult struct {
	Inserted             []Message
	TailSeq              int64
	AdoptedBoundarySeq   int64
	HistoricalFromSeq    int64
	HistoricalThroughSeq int64
}

// RebuildConversationBaseline 原子提交历史消息、conversation 尾和必备审计。
// PatrolRound.NewMessageCount 刻意不变;事件层应与调用方的空投影保持一致。
func (s *Store) RebuildConversationBaseline(req RebuildConversationBaselineRequest) (*RebuildConversationBaselineResult, error) {
	if req.RoundID == "" {
		return nil, ErrHistoricalBaselineNoRound
	}
	if len(req.Historical) == 0 {
		return nil, ErrHistoricalBaselineEmpty
	}
	if err := validateMessageDrafts(req.Historical); err != nil {
		return nil, err
	}
	if req.SyncedAt.IsZero() {
		req.SyncedAt = time.Now()
	}

	result := &RebuildConversationBaselineResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var c Conversation
		if err := tx.Where(conversationWhere(req.Key), conversationArgs(req.Key)...).First(&c).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConversationNotFound
			}
			return err
		}
		if c.LastMessageSeq != req.ExpectedTailSeq {
			return ErrConversationVersionConflict
		}

		var intent TrackedIntent
		if err := tx.Where(conversationWhere(req.Key), conversationArgs(req.Key)...).First(&intent).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if c.TrackingState == TrackingUntracked {
					return ErrConversationNotTracked
				}
				return ErrTrackingStateCorrupt
			}
			return err
		}
		if c.TrackingState != intent.Status {
			return ErrTrackingStateCorrupt
		}
		if c.TrackingState != TrackingAdopted {
			return ErrConversationNotTracked
		}
		if c.PlatformUserRef != "" && req.PlatformUserRef != "" && c.PlatformUserRef != req.PlatformUserRef {
			return ErrPeerIdentityConflict
		}
		effectivePeerRef := c.PlatformUserRef
		if req.PlatformUserRef != "" {
			effectivePeerRef = req.PlatformUserRef
		}
		if effectivePeerRef == "" {
			return ErrPeerIdentityRequired
		}
		if err := requirePatrolRound(tx, req.Key.Platform, req.Key.AccountRef, req.RoundID); err != nil {
			return err
		}

		oldTail := c.LastMessageSeq
		firstSeq, err := nextPhysicalMessageSeqTx(tx, req.Key)
		if err != nil {
			return err
		}
		seq := firstSeq - 1
		inserted := make([]Message, 0, len(req.Historical))
		for _, draft := range req.Historical {
			seq++
			m := Message{
				Platform: req.Key.Platform, AccountRef: req.Key.AccountRef, ConversationRef: req.Key.ConversationRef,
				Seq: seq, Direction: draft.Direction, Kind: draft.Kind, ContentHash: draft.ContentHash,
				Text: draft.Text, BlobRef: draft.BlobRef, CardType: draft.CardType, CardState: draft.CardState,
				InterviewStartsAtMs: draft.InterviewStartsAtMs, InterviewEndsAtMs: draft.InterviewEndsAtMs,
				InterviewMethod: draft.InterviewMethod,
				TsApproxMs:      draft.TsApproxMs, Origin: draft.Origin, FirstSeenRoundID: req.RoundID,
				SourceKey: draft.SourceKey,
			}
			if err := tx.Create(&m).Error; err != nil {
				return err
			}
			inserted = append(inserted, m)
		}

		updates := map[string]any{
			"last_message_seq": seq, "last_synced_round_id": req.RoundID, "last_synced_at": req.SyncedAt,
		}
		if req.PlatformUserRef != "" {
			updates["platform_user_ref"] = req.PlatformUserRef
		}
		updated := tx.Model(&Conversation{}).Where(conversationWhere(req.Key), conversationArgs(req.Key)...).Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrConversationVersionConflict
		}

		detail := fmt.Sprintf("oldTail=%d historicalFrom=%d historicalThrough=%d imported=%d",
			oldTail, firstSeq, seq, len(inserted))
		if req.AuditDetail != "" {
			detail += " " + req.AuditDetail
		}
		// 审计故意放在消息和会话尾之后写:它失败时整个事务必须回滚。
		if err := tx.Create(&AuditEntry{
			At: req.SyncedAt, Category: "conversation_zero_overlap_rebaseline",
			Platform: req.Key.Platform, AccountRef: req.Key.AccountRef,
			ConversationRef: req.Key.ConversationRef, RoundID: req.RoundID, Detail: detail,
		}).Error; err != nil {
			return err
		}

		result.Inserted = inserted
		result.TailSeq = seq
		result.AdoptedBoundarySeq = c.AdoptedBoundarySeq
		result.HistoricalFromSeq = firstSeq
		result.HistoricalThroughSeq = seq
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) MessagesForConversation(key ConversationKey) ([]Message, error) {
	var out []Message
	err := s.db.Where(conversationWhere(key), conversationArgs(key)...).
		Where(activeMessageCondition).Order("seq").Find(&out).Error
	return out, err
}

// RecentMessagesForConversation 是操作 UI 的有界读取；返回仍按 seq 正序，便于直接展示。
func (s *Store) RecentMessagesForConversation(key ConversationKey, limit int) ([]Message, error) {
	if limit <= 0 || limit > 500 {
		return nil, errors.New("消息 limit 必须在 1..500")
	}
	var descending []Message
	if err := s.db.Where(conversationWhere(key), conversationArgs(key)...).
		Where(activeMessageCondition).Order("seq DESC").Limit(limit).Find(&descending).Error; err != nil {
		return nil, err
	}
	for left, right := 0, len(descending)-1; left < right; left, right = left+1, right-1 {
		descending[left], descending[right] = descending[right], descending[left]
	}
	return descending, nil
}

func (s *Store) MessageBySeq(key ConversationKey, seq int64) (*Message, error) {
	var m Message
	err := s.db.First(&m,
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ? AND "+activeMessageCondition,
		key.Platform, key.AccountRef, key.ConversationRef, seq).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// AppendAudit 提供需要错误返回的 M2 审计写入;旧 Audit 仍保留尽力而为语义。
func (s *Store) AppendAudit(e *AuditEntry) error {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	return s.db.Create(e).Error
}

func requireAccount(tx *gorm.DB, key AccountKey) error {
	var n int64
	if err := tx.Model(&Account{}).Where("platform = ? AND account_ref = ?", key.Platform, key.AccountRef).Count(&n).Error; err != nil {
		return err
	}
	if n != 1 {
		return ErrAccountNotFound
	}
	return nil
}

func requirePatrolRound(tx *gorm.DB, platform, accountRef, roundID string) error {
	var n int64
	if err := tx.Model(&PatrolRound{}).
		Where("platform = ? AND account_ref = ? AND round_id = ?", platform, accountRef, roundID).Count(&n).Error; err != nil {
		return err
	}
	if n != 1 {
		return ErrPatrolRoundNotFound
	}
	return nil
}

func conversationWhere(ConversationKey) string {
	return "platform = ? AND account_ref = ? AND conversation_ref = ?"
}

func conversationArgs(key ConversationKey) []any {
	return []any{key.Platform, key.AccountRef, key.ConversationRef}
}
