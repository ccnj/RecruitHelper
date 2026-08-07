package store

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// CreateJobPublishEffectIntentRequest 是职位发布专属的账本入口入参。
type CreateJobPublishEffectIntentRequest struct {
	Intent  EffectIntent
	Command CmdRecord
	Now     time.Time
}

// CreateJobPublishEffectIntentAndCmd 为职位发布在同一事务内写入 effect intent
// 与根命令。
//
// 为什么不能复用 CreateEffectIntentAndCmd：那个入口把 TargetRef 当会话引用，
// 会强制校验会话存在、被跟踪、以及 expectedTailSeq 对齐尾序。职位发布的目标是
// 职位而不是会话，这些校验一条都不适用（真机上就是直接撞在"会话不存在"）。
//
// 保留下来的是同样重要的部分：账号身份必须与本次派发的手会话严格一致，
// idemKey/intentID 唯一，以及精确重试收编原意图而不是另铸一个。
// 去掉的是会话尾序与前序意图链——职位发布没有"上一条"的概念。
func (s *Store) CreateJobPublishEffectIntentAndCmd(
	req CreateJobPublishEffectIntentRequest,
) (*CreateEffectIntentResult, error) {
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	i, c := req.Intent, req.Command
	if i.IntentID == "" || i.IdemKey == "" || i.Platform == "" || i.AccountRef == "" ||
		i.Primitive == "" || i.TargetRef == "" || i.PayloadHash == "" || i.GuardsHash == "" ||
		c.MsgID == "" || c.IntentID != i.IntentID || c.IdemKey != i.IdemKey ||
		c.Platform != i.Platform || c.AccountRef != i.AccountRef || c.Name != i.Primitive ||
		c.Class != "effectful" || c.Domain == "" {
		return nil, errors.New("职位发布意图/命令缺少一致的必填字段")
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
		return nil, errors.New("职位发布意图已过期")
	}
	prepareRootCmd(&c)

	out := &CreateEffectIntentResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 精确重试优先收编原意图:同一职位同一份发布参数只允许发一次,重跑准备
		// 阶段等于可能在平台上再发一次。
		var existing EffectIntent
		err := tx.First(&existing, "intent_id = ?", i.IntentID).Error
		if err == nil {
			if !sameEffectIntentMaterial(existing, i) {
				return ErrEffectIntentConflict
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

		var account Account
		if err := tx.First(&account, "platform = ? AND account_ref = ?", i.Platform, i.AccountRef).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAccountNotFound
			}
			return err
		}
		// 身份必须与本次派发所用的手会话逐项一致:发布是不可逆动作,不接受
		// "换了账号或换了会话之后才落账本"。
		if account.IdentityState != IdentityVerified || account.PrincipalFingerprint == nil ||
			*account.PrincipalFingerprint == "" || account.BoundHandID != c.HandID ||
			account.IdentitySession != c.Session || account.IdentityBootID != c.BootIDAtDispatch ||
			*account.PrincipalFingerprint != c.ExpectedPrincipalFingerprint {
			return ErrAccountIdentityNotCurrent
		}

		if err := tx.Create(&i).Error; err != nil {
			return err
		}
		if err := tx.Create(&c).Error; err != nil {
			return err
		}
		out.Intent, out.Command, out.Created = i, c, true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ResolveJobPublishSuspectVerdict 把职位发布 suspect 的人工裁决落成终局。
//
// 为什么不能复用 ResolveSuspectVerdict：那个入口同样把 TargetRef 当会话引用，
// resolvedOk 要往会话里追加一条我方消息、resolvedFailed 要撤回它。职位发布的
// 目标是职位不是会话，两条腿都无处落笔——没有这条路径时，人在诊断台点任一个
// 按钮都只会撞回"真实副作用原语没有人工裁决实现"，suspect 永远清不掉。
//
// 两个方向都只写终局、不铸任何业务消息事实：发布成不成功就是平台上"同名职位
// 在不在"这一个布尔（契约 JobPublishDraftData 里也只有 postingVisible），脑没有
// 别的东西要收编，因而也不必像招呼那样把 resolvedOk 降解成一次正证读取授权——
// 招呼那样做是因为成功必须收编 conversationRef，布尔裁决补不出会话引用。
//
// resolvedFailed 之后该职位的同一份发布参数可以重发：publishAttemptSettled 认这
// 个终局，运营再点发布会递进尝试序号铸新意图；真正防重复发布的仍是点击前的
// expectAbsentOnPlatform 平台实读——判错方向（其实已发布却判未发生）由它兜住，
// 那一次会干净失败，不会在平台上留下第二个同名职位。
func (s *Store) ResolveJobPublishSuspectVerdict(ref string, verdict CmdStatus, at time.Time) error {
	if ref == "" || (verdict != CmdResolvedOk && verdict != CmdResolvedFailed) {
		return ErrRecoveryStateConflict
	}
	if at.IsZero() {
		at = time.Now()
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var cmd CmdRecord
		if err := tx.First(&cmd, "msg_id = ?", ref).Error; err != nil {
			return err
		}
		if cmd.Name != primitiveJobPublishDraft || cmd.IntentID == "" || cmd.Status != CmdSuspect {
			return ErrRecoveryStateConflict
		}
		var intent EffectIntent
		if err := tx.First(&intent, "intent_id = ?", cmd.IntentID).Error; err != nil {
			return err
		}
		if intent.Primitive != primitiveJobPublishDraft || intent.Status != EffectIntentSuspect ||
			intent.IdemKey != cmd.IdemKey || intent.RootMsgID != cmd.LogicalDispatchID {
			return ErrEffectIntentConflict
		}

		cmd.Status = verdict
		cmd.RecoveryAuthorized = false
		cmd.VerificationNextAt = nil
		cmd.VerificationChildMsgID = ""
		cmd.ReviewReady = false
		cmd.ReviewAfterMs = 0
		cmd.TerminalAt = &at
		if err := tx.Save(&cmd).Error; err != nil {
			return err
		}
		if verdict == CmdResolvedOk {
			intent.Status = EffectIntentResolvedOk
		} else {
			intent.Status = EffectIntentResolvedFailed
		}
		intent.SuspectReason = cmd.SuspectReason
		intent.ResolvedAt = &at
		return tx.Save(&intent).Error
	})
}
