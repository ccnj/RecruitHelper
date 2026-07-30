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
