package store

import (
	"errors"
	"strconv"
	"time"

	"gorm.io/gorm"
)

// 2026-07-27 枚举修复（fix: 邀面卡枚举接受真机字符串形态）之前，账本中唯一一张
// 邀面卡由旧代码的兜底分支投影，content_hash 携带兜底身份而非精确邀面参数；
// 修复后的重读会对同一 sourceKey 计算出新的规范哈希，触发语义冲突守卫并暂停
// 账号。本修正是解析规范变更后的一次性事实同步（甲方批准）：把该行派生列对齐
// 到新代码对同一平台消息的解析结果，不改动方向、种类、正文与 sourceKey。

var ErrInterviewCardCorrectionGate = errors.New("邀面卡哈希修正前置校验不通过")

type CorrectLegacyInterviewCardRequest struct {
	Platform        string
	AccountRef      string
	ConversationRef string
	Seq             int64
	LegacyHash      string
	CanonicalHash   string
	StartsAtMs      int64
	EndsAtMs        int64
	Now             time.Time
}

type CorrectLegacyInterviewCardResult struct {
	AlreadyCorrected bool
}

func (s *Store) CorrectLegacyInterviewCardContentHash(
	req CorrectLegacyInterviewCardRequest,
) (*CorrectLegacyInterviewCardResult, error) {
	if req.Platform == "" || req.AccountRef == "" || req.ConversationRef == "" ||
		req.Seq <= 0 || req.LegacyHash == "" || req.CanonicalHash == "" ||
		req.LegacyHash == req.CanonicalHash ||
		req.StartsAtMs <= 0 || req.EndsAtMs <= req.StartsAtMs {
		return nil, ErrInterviewCardCorrectionGate
	}
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	out := &CorrectLegacyInterviewCardResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var row Message
		if err := tx.First(&row,
			"platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ?",
			req.Platform, req.AccountRef, req.ConversationRef, req.Seq,
		).Error; err != nil {
			return err
		}
		if row.ContentHash == req.CanonicalHash {
			out.AlreadyCorrected = true
			return nil
		}
		if row.Direction != "out" || row.Kind != "card" ||
			row.CardType != "interviewInvite" ||
			row.ContentHash != req.LegacyHash || row.RetractedAt != nil {
			return ErrInterviewCardCorrectionGate
		}
		method := "wechatVideo"
		if err := tx.Model(&Message{}).
			Where("platform = ? AND account_ref = ? AND conversation_ref = ? AND seq = ? AND content_hash = ?",
				req.Platform, req.AccountRef, req.ConversationRef, req.Seq, req.LegacyHash).
			Updates(map[string]any{
				"content_hash":           req.CanonicalHash,
				"interview_starts_at_ms": req.StartsAtMs,
				"interview_ends_at_ms":   req.EndsAtMs,
				"interview_method":       method,
			}).Error; err != nil {
			return err
		}
		shortHash := func(value string) string {
			if len(value) > 8 {
				return value[:8]
			}
			return value
		}
		return tx.Create(&AuditEntry{
			At: req.Now, Category: "interview_card_hash_correction",
			Platform: req.Platform, AccountRef: req.AccountRef,
			ConversationRef: req.ConversationRef,
			Detail: "解析规范变更事实同步: seq=" + strconv.FormatInt(req.Seq, 10) +
				" 邀面卡哈希 " + shortHash(req.LegacyHash) + "->" + shortHash(req.CanonicalHash) +
				" 并补精确邀面参数列",
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
