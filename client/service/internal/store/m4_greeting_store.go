package store

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

var (
	ErrCandidateGreetingCASConflict = errors.New("档案最新招呼意图已变化")
	ErrCandidateGreetingHeadCorrupt = errors.New("候选人招呼 head 缺失或损坏")
	ErrCandidateGreetingFrozen      = errors.New("候选人招呼意图仍在途、已成功或待裁决")
	ErrCandidateProfileNotSelected  = errors.New("候选人档案不在 selected 状态")
)

type CandidateGreetingCASConflictError struct {
	PreviousIntentID string
	Current          *EffectIntent
}

func (e *CandidateGreetingCASConflictError) Error() string {
	current := "<none>"
	previous := ""
	if e != nil {
		previous = e.PreviousIntentID
		if e.Current != nil {
			current = e.Current.IntentID
		}
	}
	return fmt.Sprintf("%s: previous=%q current=%q", ErrCandidateGreetingCASConflict, previous, current)
}

func (e *CandidateGreetingCASConflictError) Unwrap() error { return ErrCandidateGreetingCASConflict }

type CreateGreetingEffectIntentRequest struct {
	Intent           EffectIntent
	Command          CmdRecord
	PreviousIntentID string
	Now              time.Time
}

// CreateGreetingEffectIntentAndCmd 是真人确认招呼后的账本铸造事务。
// 它只建立 EffectIntent/CmdRecord/CandidateGreetingHead，不发送 socket；
// profile 成功推进与 conversation/message 绑定留给批次 4 的完整结果事务。
func (s *Store) CreateGreetingEffectIntentAndCmd(
	req CreateGreetingEffectIntentRequest,
) (*CreateEffectIntentResult, error) {
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	i, c := req.Intent, req.Command
	if err := normalizeAndValidateGreetingIntent(&i, &c, req.Now); err != nil {
		return nil, err
	}
	prepareRootCmd(&c)
	out := &CreateEffectIntentResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var existing EffectIntent
		err := tx.First(&existing, "intent_id = ?", i.IntentID).Error
		if err == nil {
			if !sameEffectIntentMaterial(existing, i) || existing.Primitive != primitiveChatSendGreeting {
				return ErrEffectIntentConflict
			}
			// 精确 HTTP 重试可以返回旧代 intent，但 head 自身必须仍然可证。
			if _, _, err := candidateGreetingHeadTx(tx, i.TargetRef); err != nil {
				return err
			}
			var existingCmd CmdRecord
			if err := tx.First(&existingCmd, "msg_id = ?", existing.RootMsgID).Error; err != nil {
				return fmt.Errorf("%w: 招呼意图根命令丢失", ErrEffectIntentConflict)
			}
			out.Intent, out.Command, out.Created = existing, existingCmd, false
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		head, latest, err := candidateGreetingHeadTx(tx, i.TargetRef)
		if err != nil {
			return err
		}
		currentID := ""
		if latest != nil {
			currentID = latest.IntentID
		}
		if currentID != req.PreviousIntentID {
			return &CandidateGreetingCASConflictError{
				PreviousIntentID: req.PreviousIntentID,
				Current:          latest,
			}
		}
		if latest != nil && latest.Status != EffectIntentFailed && latest.Status != EffectIntentResolvedFailed {
			return ErrCandidateGreetingFrozen
		}
		if head != nil && head.Generation >= maxSQLiteEffectHeadGeneration {
			return fmt.Errorf("%w: generation 已达 SQLite 上限", ErrCandidateGreetingHeadCorrupt)
		}

		var profile CandidateProfile
		if err := tx.First(&profile, "profile_id = ?", i.TargetRef).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCandidateProfileNotFound
			}
			return err
		}
		if profile.Platform != i.Platform || profile.AccountRef != i.AccountRef ||
			profile.MainStatus != CandidateProfileSelected || profile.EndReason != nil ||
			profile.SuccessfulGreetingIntentID != nil || profile.ConversationRef != nil || profile.GreetedAt != nil {
			if profile.MainStatus != CandidateProfileSelected {
				return ErrCandidateProfileNotSelected
			}
			return ErrCandidateProfileState
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

		var busy int64
		frozenStatuses := append(append([]CmdStatus(nil), nonTerminalStatuses...), CmdSuspect)
		if err := tx.Model(&CmdRecord{}).Where("domain = ? AND status IN ?", c.Domain, frozenStatuses).
			Count(&busy).Error; err != nil {
			return err
		}
		if busy != 0 {
			return ErrDomainBusy
		}

		if err := tx.Create(&i).Error; err != nil {
			return err
		}
		if err := createRootCmd(tx, &c); err != nil {
			return err
		}
		if head == nil {
			head = &CandidateGreetingHead{
				ProfileID: i.TargetRef, LatestIntentID: i.IntentID, Generation: 1,
			}
			if err := tx.Create(head).Error; err != nil {
				return err
			}
		} else {
			nextGeneration := head.Generation + 1
			updated := tx.Model(&CandidateGreetingHead{}).
				Where("profile_id = ? AND latest_intent_id = ? AND generation = ?",
					head.ProfileID, head.LatestIntentID, head.Generation).
				Updates(map[string]any{"latest_intent_id": i.IntentID, "generation": nextGeneration})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrCandidateGreetingCASConflict
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

func normalizeAndValidateGreetingIntent(i *EffectIntent, c *CmdRecord, now time.Time) error {
	if i == nil || c == nil || i.IntentID == "" || i.IdemKey == "" || i.Platform == "" ||
		i.AccountRef == "" || i.Primitive != primitiveChatSendGreeting || i.TargetRef == "" ||
		i.PayloadHash == "" || i.GuardsHash == "" || i.SendFingerprint == "" ||
		c.MsgID == "" || c.IntentID != i.IntentID || c.IdemKey != i.IdemKey ||
		c.Platform != i.Platform || c.AccountRef != i.AccountRef || c.Name != i.Primitive ||
		c.Class != "effectful" || c.Domain != i.Platform+":"+i.AccountRef {
		return errors.New("招呼意图/命令缺少一致的必填字段")
	}
	if i.RootMsgID == "" {
		i.RootMsgID = c.MsgID
	}
	if i.RootMsgID != c.MsgID || i.ResultConversationRef != nil {
		return ErrEffectIntentConflict
	}
	if i.Status == "" {
		i.Status = EffectIntentDispatching
	}
	if i.Status != EffectIntentDispatching {
		return ErrEffectIntentConflict
	}
	if i.DeadlineMs <= now.UnixMilli() || c.DeadlineMs <= now.UnixMilli() {
		return errors.New("招呼意图已过期")
	}
	return nil
}

func candidateGreetingHeadTx(
	tx *gorm.DB, profileID string,
) (*CandidateGreetingHead, *EffectIntent, error) {
	var head CandidateGreetingHead
	err := tx.First(&head, "profile_id = ?", profileID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		var orphaned int64
		if countErr := tx.Model(&EffectIntent{}).
			Where("primitive = ? AND target_ref = ?", primitiveChatSendGreeting, profileID).
			Count(&orphaned).Error; countErr != nil {
			return nil, nil, countErr
		}
		if orphaned != 0 {
			return nil, nil, fmt.Errorf("%w: 发现 %d 条无 head 招呼意图", ErrCandidateGreetingHeadCorrupt, orphaned)
		}
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if head.LatestIntentID == "" || head.Generation == 0 {
		return nil, nil, fmt.Errorf("%w: 空 latestIntentId 或零 generation", ErrCandidateGreetingHeadCorrupt)
	}
	var intent EffectIntent
	if err := tx.First(&intent, "intent_id = ?", head.LatestIntentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, fmt.Errorf("%w: head 指向不存在的 intent %q", ErrCandidateGreetingHeadCorrupt, head.LatestIntentID)
		}
		return nil, nil, err
	}
	if intent.Primitive != primitiveChatSendGreeting || intent.TargetRef != profileID {
		return nil, nil, fmt.Errorf("%w: head 与招呼 intent 目标不一致", ErrCandidateGreetingHeadCorrupt)
	}
	return &head, &intent, nil
}

func (s *Store) LatestGreetingEffectIntent(profileID string) (*EffectIntent, error) {
	if profileID == "" {
		return nil, errors.New("查询最新招呼意图缺少 profileId")
	}
	_, intent, err := candidateGreetingHeadTx(s.db, profileID)
	return intent, err
}
