package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	contactAssetKindWechat        = "wechat"
	acceptWechatFingerprintDomain = "accept-wechat-v1|"
	contactAssetIdentityDomain    = "contact-asset-v1|"
)

var ErrContactAssetConflict = errors.New("联系方式事实冲突")

// AcceptWechatFingerprint is a domain-separated digest of the private request
// source identity. The raw source key stays in command args and business
// storage; action projections and EffectIntent expose only this digest.
func AcceptWechatFingerprint(requestSourceKey string) (string, error) {
	requestSourceKey = strings.TrimSpace(requestSourceKey)
	if !validMessageSourceKey(requestSourceKey) {
		return "", ErrContactAssetConflict
	}
	sum := sha256.Sum256([]byte(acceptWechatFingerprintDomain + requestSourceKey))
	return hex.EncodeToString(sum[:]), nil
}

type WechatContactAssetRequest struct {
	ProfileID         string
	Platform          string
	AccountRef        string
	ConversationRef   string
	RequestSourceKey  string
	ExchangeSourceKey string
	PeerWechat        string
	EffectIntentID    string
	ObservedAtMs      int64
	RecordedAt        time.Time
}

// RecordObservedWechatContact is the narrow read-only collection seam used by
// an independently observed exchange outcome (for example originType=1). It
// writes only the ContactAsset business fact and never advances communication
// state or creates an outbound Message.
func (s *Store) RecordObservedWechatContact(
	req WechatContactAssetRequest,
) (*ContactAsset, bool, error) {
	var asset *ContactAsset
	created := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		asset, created, err = upsertWechatContactAssetTx(tx, req)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return asset, created, nil
}

func (s *Store) ContactAssetsByProfile(profileID string) ([]ContactAsset, error) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return nil, ErrContactAssetConflict
	}
	var assets []ContactAsset
	err := s.db.
		Where("profile_id = ?", profileID).
		Order("created_at, asset_id").
		Find(&assets).Error
	return assets, err
}

// LatestWechatExchangeRequestSourceKey 返回该会话中最近一张仍有效的换微信
// 「请求」卡的稳定消息键，两种发起形态通用：我方发起的请求卡是 out 方向、
// 候选人主动发起的是 in 方向，而两者的交换「结果」卡都是 accepted 状态，
// 由 card_state='pending' 排除在外——否则形态 A 的结果卡（2026-07-29 起也
// 投影为 out 方向的 wechatExchange）会被误当成我方邀请，锚到非 105 消息上，
// 收号原语必然阴性。
// 取最近一张而不是最早一张：平台正证要求结果消息落在下一张请求卡之前，只有
// 最近一次请求的锚才可能匹配到本次交换结果。方向与 originType 的配对由原语
// 自己核对，这里不替它裁决形态。
func (s *Store) LatestWechatExchangeRequestSourceKey(
	key ConversationKey,
) (string, bool, error) {
	var message Message
	err := s.db.
		Where(
			"platform = ? AND account_ref = ? AND conversation_ref = ? AND "+
				"card_type = ? AND card_state = ? AND source_key IS NOT NULL AND "+
				activeMessageCondition,
			key.Platform,
			key.AccountRef,
			key.ConversationRef,
			"wechatExchange",
			"pending",
		).
		Order("seq DESC").
		First(&message).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	sourceKey := strings.TrimSpace(*message.SourceKey)
	if !validMessageSourceKey(sourceKey) {
		return "", false, nil
	}
	return sourceKey, true, nil
}

func (s *Store) HasWechatContactAssetForRequest(
	profileID string,
	requestSourceKey string,
) (bool, error) {
	profileID = strings.TrimSpace(profileID)
	requestSourceKey = strings.TrimSpace(requestSourceKey)
	if profileID == "" || !validMessageSourceKey(requestSourceKey) {
		return false, ErrContactAssetConflict
	}
	var count int64
	err := s.db.Model(&ContactAsset{}).
		Where(
			"profile_id = ? AND kind = ? AND request_source_key = ?",
			profileID,
			contactAssetKindWechat,
			requestSourceKey,
		).
		Count(&count).Error
	return count != 0, err
}

func upsertWechatContactAssetTx(
	tx *gorm.DB,
	req WechatContactAssetRequest,
) (*ContactAsset, bool, error) {
	req.ProfileID = strings.TrimSpace(req.ProfileID)
	req.Platform = strings.TrimSpace(req.Platform)
	req.AccountRef = strings.TrimSpace(req.AccountRef)
	req.ConversationRef = strings.TrimSpace(req.ConversationRef)
	req.RequestSourceKey = strings.TrimSpace(req.RequestSourceKey)
	req.ExchangeSourceKey = strings.TrimSpace(req.ExchangeSourceKey)
	req.PeerWechat = strings.TrimSpace(req.PeerWechat)
	req.EffectIntentID = strings.TrimSpace(req.EffectIntentID)
	if tx == nil ||
		req.ProfileID == "" ||
		req.Platform == "" ||
		req.AccountRef == "" ||
		req.ConversationRef == "" ||
		req.PeerWechat == "" ||
		!validMessageSourceKey(req.RequestSourceKey) ||
		!validMessageSourceKey(req.ExchangeSourceKey) ||
		req.ObservedAtMs < 0 {
		return nil, false, ErrContactAssetConflict
	}
	if req.RecordedAt.IsZero() {
		req.RecordedAt = time.Now()
	}
	req.RecordedAt = req.RecordedAt.UTC()

	var profile CandidateProfile
	if err := tx.First(&profile, "profile_id = ?", req.ProfileID).Error; err != nil {
		return nil, false, err
	}
	if profile.Platform != req.Platform ||
		profile.AccountRef != req.AccountRef ||
		profile.ConversationRef == nil ||
		*profile.ConversationRef != req.ConversationRef {
		return nil, false, ErrContactAssetConflict
	}

	var existing ContactAsset
	err := tx.First(
		&existing,
		"platform = ? AND account_ref = ? AND conversation_ref = ? AND kind = ? AND source_key = ?",
		req.Platform,
		req.AccountRef,
		req.ConversationRef,
		contactAssetKindWechat,
		req.ExchangeSourceKey,
	).Error
	if err == nil {
		if existing.ProfileID != req.ProfileID ||
			existing.RequestSourceKey != req.RequestSourceKey ||
			existing.Value != req.PeerWechat ||
			(existing.EffectIntentID != nil &&
				req.EffectIntentID != "" &&
				*existing.EffectIntentID != req.EffectIntentID) {
			return nil, false, ErrContactAssetConflict
		}
		if existing.EffectIntentID == nil && req.EffectIntentID != "" {
			intentID := req.EffectIntentID
			updated := tx.Model(&ContactAsset{}).
				Where("asset_id = ? AND effect_intent_id IS NULL", existing.AssetID).
				Updates(map[string]any{
					"effect_intent_id": intentID,
					"updated_at":       req.RecordedAt,
				})
			if updated.Error != nil {
				return nil, false, updated.Error
			}
			if updated.RowsAffected != 1 {
				return nil, false, ErrContactAssetConflict
			}
			existing.EffectIntentID = &intentID
			existing.UpdatedAt = req.RecordedAt
		}
		return &existing, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}

	sum := sha256.Sum256([]byte(
		contactAssetIdentityDomain +
			req.Platform + "\x00" +
			req.AccountRef + "\x00" +
			req.ConversationRef + "\x00" +
			contactAssetKindWechat + "\x00" +
			req.ExchangeSourceKey,
	))
	asset := &ContactAsset{
		AssetID:          hex.EncodeToString(sum[:]),
		ProfileID:        req.ProfileID,
		Platform:         req.Platform,
		AccountRef:       req.AccountRef,
		ConversationRef:  req.ConversationRef,
		Kind:             contactAssetKindWechat,
		SourceKey:        req.ExchangeSourceKey,
		RequestSourceKey: req.RequestSourceKey,
		Value:            req.PeerWechat,
		ObservedAtMs:     req.ObservedAtMs,
		CreatedAt:        req.RecordedAt,
		UpdatedAt:        req.RecordedAt,
	}
	if req.EffectIntentID != "" {
		intentID := req.EffectIntentID
		asset.EffectIntentID = &intentID
	}
	if err := tx.Create(asset).Error; err != nil {
		return nil, false, err
	}
	// 换微信成功的权威时点即 ContactAsset 创建;两条收编路径(候选人主动接受
	// 与我方邀请被接受)在此汇合,运营通知同事务幂等入队(每候选人终身一次)。
	if err := enqueueNotificationTx(
		tx,
		NotificationTypeWechatAdded,
		"wechatAdded:"+req.ProfileID,
		req.ProfileID,
		req.RecordedAt,
	); err != nil {
		return nil, false, err
	}
	return asset, true, nil
}

func contactAssetByEffectIntentTx(
	tx *gorm.DB,
	intentID string,
) (*ContactAsset, error) {
	var asset ContactAsset
	err := tx.First(&asset, "effect_intent_id = ?", intentID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &asset, nil
}

func applyWechatContactResultTx(
	tx *gorm.DB,
	intent *EffectIntent,
	result WechatContactResultMutation,
	at time.Time,
) (*ContactAsset, bool, error) {
	if tx == nil ||
		intent == nil ||
		intent.Primitive != primitiveChatAcceptWechat ||
		intent.TargetRef == "" ||
		result.ConversationRef != intent.TargetRef {
		return nil, false, ErrEffectIntentConflict
	}
	fingerprint, err := AcceptWechatFingerprint(result.RequestSourceKey)
	if err != nil || fingerprint != intent.SendFingerprint {
		return nil, false, ErrEffectIntentConflict
	}
	var action CommunicationV4EventAction
	if err := tx.First(
		&action,
		"effect_intent_id = ?",
		intent.IntentID,
	).Error; err != nil {
		return nil, false, err
	}
	if action.V4Kind != "acceptWechat" ||
		action.EffectKind != CommunicationV4EventEffectAcceptWechat ||
		action.ContentHash != fingerprint {
		return nil, false, ErrEffectIntentConflict
	}
	requestSourceKey, err := communicationV4AcceptWechatRequestSourceTx(
		tx,
		action,
		result.ConversationRef,
	)
	if err != nil || requestSourceKey != result.RequestSourceKey {
		return nil, false, ErrEffectIntentConflict
	}
	return upsertWechatContactAssetTx(tx, WechatContactAssetRequest{
		ProfileID:         action.ProfileID,
		Platform:          intent.Platform,
		AccountRef:        intent.AccountRef,
		ConversationRef:   result.ConversationRef,
		RequestSourceKey:  result.RequestSourceKey,
		ExchangeSourceKey: result.ExchangeSourceKey,
		PeerWechat:        result.PeerWechat,
		EffectIntentID:    intent.IntentID,
		ObservedAtMs:      result.ObservedAtMs,
		RecordedAt:        at,
	})
}

type VerifiedWechatAcceptSuccess struct {
	Ref               string
	ConversationKey   ConversationKey
	RequestSourceKey  string
	ExchangeSourceKey string
	PeerWechat        string
	ObservedAtMs      int64
	ResultBody        string
	ResolutionReason  string
	At                time.Time
}

// ResolveWechatAcceptVerified commits the specialized outcome read exactly
// like a direct positive result, except that it intentionally creates no
// outbound Message. Repeated verification and a later direct result converge
// on the same source-scoped ContactAsset.
func (s *Store) ResolveWechatAcceptVerified(
	req VerifiedWechatAcceptSuccess,
) (*ContactAsset, error) {
	if req.At.IsZero() {
		req.At = time.Now()
	}
	if req.Ref == "" ||
		req.ConversationKey.Platform == "" ||
		req.ConversationKey.AccountRef == "" ||
		req.ConversationKey.ConversationRef == "" {
		return nil, ErrRecoveryStateConflict
	}
	var resolved *ContactAsset
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var cmd CmdRecord
		if err := tx.First(&cmd, "msg_id = ?", req.Ref).Error; err != nil {
			return err
		}
		if cmd.IntentID == "" ||
			cmd.Name != primitiveChatAcceptWechat {
			return ErrRecoveryStateConflict
		}
		if cmd.Status == CmdOk || cmd.Status == CmdResolvedOk {
			var err error
			resolved, err = contactAssetByEffectIntentTx(tx, cmd.IntentID)
			if err != nil {
				return err
			}
			if resolved == nil {
				return ErrRecoveryStateConflict
			}
			return applyM5AutomaticEffectStatusByIDTx(
				tx,
				cmd.IntentID,
				req.At,
			)
		}
		if cmd.Status != CmdVerifying &&
			cmd.Status != CmdPendingReconcile &&
			cmd.Status != CmdSuspect {
			return ErrRecoveryStateConflict
		}
		var intent EffectIntent
		if err := tx.First(
			&intent,
			"intent_id = ?",
			cmd.IntentID,
		).Error; err != nil {
			return err
		}
		fingerprint, err := AcceptWechatFingerprint(req.RequestSourceKey)
		if err != nil ||
			intent.Primitive != primitiveChatAcceptWechat ||
			intent.Platform != req.ConversationKey.Platform ||
			intent.AccountRef != req.ConversationKey.AccountRef ||
			intent.TargetRef != req.ConversationKey.ConversationRef ||
			intent.SendFingerprint != fingerprint {
			return ErrEffectIntentConflict
		}
		resolved, _, err = applyWechatContactResultTx(
			tx,
			&intent,
			WechatContactResultMutation{
				ConversationRef:   req.ConversationKey.ConversationRef,
				RequestSourceKey:  req.RequestSourceKey,
				ExchangeSourceKey: req.ExchangeSourceKey,
				PeerWechat:        req.PeerWechat,
				ObservedAtMs:      req.ObservedAtMs,
			},
			req.At,
		)
		if err != nil {
			return err
		}
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
		intent.ResultMessageSeq = nil
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
	return resolved, nil
}
