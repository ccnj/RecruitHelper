package dispatch

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

type SendGreetingRequest struct {
	IntentID         string
	PreviousIntentID string
	ProfileID        string
	Text             string
}

// SendGreeting 是 chat.sendGreeting 唯一产品入口。平台、账号、候选人与
// 职位引用全部从 profile 账本派生；调用方只能提交内部 profileId 和正文。
func (d *Dispatcher) SendGreeting(req SendGreetingRequest) (*SendMessageReceipt, error) {
	req.IntentID = strings.TrimSpace(req.IntentID)
	req.PreviousIntentID = strings.TrimSpace(req.PreviousIntentID)
	req.ProfileID = strings.TrimSpace(req.ProfileID)
	if req.IntentID == "" || utf8.RuneCountInString(req.IntentID) > maxIntentIDRunes || req.ProfileID == "" {
		return nil, errors.New("缺少有效的 intentId/profileId")
	}
	if strings.TrimSpace(req.Text) == "" {
		return nil, errors.New("发送文本不能为空")
	}

	profilePtr, err := d.st.CandidateProfileByID(req.ProfileID)
	if err != nil {
		return nil, err
	}
	if profilePtr == nil {
		return nil, store.ErrCandidateProfileNotFound
	}
	profile := *profilePtr
	args := protocol.ChatSendGreetingArgs{
		PlatformUserRef: profile.PlatformUserRef,
		PositionRef:     profile.PositionRef,
		Text:            req.Text,
	}
	argsRaw, err := protocol.Encode(args)
	if err != nil {
		return nil, err
	}
	meta := protocol.Primitives[protocol.PrimChatSendGreeting]
	if err := protocol.ValidatePrimitiveArgs(protocol.PrimChatSendGreeting, meta.Ver, argsRaw); err != nil {
		return nil, err
	}
	payloadHash := hashBytes(argsRaw)

	// 精确 HTTP 重试优先收编原意图，避免成功后 profile 已不再 selected
	// 时重跑准备阶段。只比较调用方不可变材料和账本派生目标。
	if existing, lookupErr := d.st.EffectIntentByID(req.IntentID); lookupErr != nil {
		return nil, lookupErr
	} else if existing != nil {
		if existing.Platform != profile.Platform || existing.AccountRef != profile.AccountRef ||
			existing.Primitive != protocol.PrimChatSendGreeting || existing.TargetRef != profile.ProfileID ||
			existing.PayloadHash != payloadHash {
			return nil, store.ErrEffectIntentConflict
		}
		return d.sendReceipt(existing, false)
	}

	latest, err := d.st.LatestGreetingEffectIntent(profile.ProfileID)
	if err != nil {
		return nil, err
	}
	latestID := ""
	if latest != nil {
		latestID = latest.IntentID
	}
	if latestID != req.PreviousIntentID {
		conflict := &store.CandidateGreetingCASConflictError{
			PreviousIntentID: req.PreviousIntentID, Current: latest,
		}
		if latest == nil {
			return nil, conflict
		}
		receipt, receiptErr := d.sendReceipt(latest, false)
		return receipt, errors.Join(conflict, receiptErr)
	}

	preparation, err := d.st.PrepareGreeting(req.ProfileID)
	if err != nil {
		return nil, err
	}
	profile = preparation.Profile
	account := preparation.Account
	if account.BoundHandID == "" || account.PrincipalFingerprint == nil {
		return nil, store.ErrAccountIdentityNotCurrent
	}
	session, bootID, online := d.sender.HandSession(account.BoundHandID)
	if !online {
		return nil, ErrHandOffline
	}
	if account.IdentityState != store.IdentityVerified || account.IdentitySession != session ||
		account.IdentityBootID != bootID {
		return nil, store.ErrAccountIdentityNotCurrent
	}
	guards := protocol.ChatSendGreetingGuards{ExpectUnestablished: true}
	guardsRaw, err := protocol.Encode(guards)
	if err != nil {
		return nil, err
	}
	if err := protocol.ValidatePrimitiveGuards(protocol.PrimChatSendGreeting, meta.Ver, guardsRaw); err != nil {
		return nil, err
	}

	now := time.Now()
	deadlineMs := now.UnixMilli() + effectiveDeadlineMs(meta)
	intent := store.EffectIntent{
		IntentID: req.IntentID,
		IdemKey: BuildEffectIdemKey(profile.Platform, profile.AccountRef,
			protocol.PrimChatSendGreeting, profile.ProfileID, req.IntentID),
		Platform: profile.Platform, AccountRef: profile.AccountRef,
		Primitive: protocol.PrimChatSendGreeting, TargetRef: profile.ProfileID,
		PayloadHash: payloadHash, GuardsHash: hashBytes(guardsRaw),
		Status: store.EffectIntentDispatching, DeadlineMs: deadlineMs,
		SendFingerprint: syncledger.HashText(req.Text),
	}
	detailed, dispatchErr := d.dispatchDetailed(DispatchRequest{
		HandID: account.BoundHandID, ExpectedSession: session, ExpectedBootID: bootID,
		Name: protocol.PrimChatSendGreeting, Args: argsRaw, Guards: guardsRaw,
		Context: &protocol.CmdContext{
			Platform: profile.Platform, AccountRef: profile.AccountRef,
			ExpectedPrincipalFingerprint: *account.PrincipalFingerprint,
		},
	}, dispatchOptions{effectIntent: &intent, previousIntentID: req.PreviousIntentID})
	if dispatchErr != nil {
		var conflict *store.CandidateGreetingCASConflictError
		if errors.As(dispatchErr, &conflict) && conflict.Current != nil {
			receipt, receiptErr := d.sendReceipt(conflict.Current, false)
			return receipt, errors.Join(dispatchErr, receiptErr)
		}
	}
	if detailed.MsgID == "" {
		return nil, dispatchErr
	}
	persisted, lookupErr := d.st.EffectIntentByID(req.IntentID)
	if lookupErr != nil {
		return nil, errors.Join(dispatchErr, lookupErr)
	}
	if persisted == nil {
		return nil, errors.Join(dispatchErr, store.ErrEffectIntentNotFound)
	}
	receipt, receiptErr := d.sendReceipt(persisted, detailed.Created)
	return receipt, errors.Join(dispatchErr, receiptErr)
}

func (d *Dispatcher) SendGreetingStatus(intentID string) (*SendMessageReceipt, error) {
	intent, err := d.st.EffectIntentByID(strings.TrimSpace(intentID))
	if err != nil {
		return nil, err
	}
	if intent == nil || intent.Primitive != protocol.PrimChatSendGreeting {
		return nil, store.ErrEffectIntentNotFound
	}
	return d.sendReceipt(intent, false)
}

func (d *Dispatcher) LatestGreetingStatus(profileID string) (*SendMessageReceipt, error) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return nil, errors.New("查询最新招呼意图缺少 profileId")
	}
	intent, err := d.st.LatestGreetingEffectIntent(profileID)
	if err != nil {
		return nil, err
	}
	if intent == nil {
		return nil, store.ErrEffectIntentNotFound
	}
	return d.sendReceipt(intent, false)
}
