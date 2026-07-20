package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

const (
	maxIntentIDRunes       = 128
	verificationRetryDelay = 5 * time.Second
)

type SendMessageRequest struct {
	IntentID         string
	PreviousIntentID string
	Platform         string
	AccountRef       string
	ConversationRef  string
	Text             string
}

type SendMessageReceipt struct {
	IntentID             string
	LogicalDispatchID    string
	MsgID                string
	Status               store.EffectIntentStatus
	CommandStatus        store.CmdStatus
	Created              bool
	VerificationAttempts int
	SuspectReason        string
}

// SendMessage 是 chat.sendMessage 唯一产品入口。通用 /admin/cmd 和
// DispatchStructured 都无法铸造 Batch X effectful。IntentID 由调用方在一次
// 真人确认里产生，HTTP 重试必须复用；脑用它确定性派生 idemKey。
func (d *Dispatcher) SendMessage(req SendMessageRequest) (*SendMessageReceipt, error) {
	req.IntentID = strings.TrimSpace(req.IntentID)
	req.PreviousIntentID = strings.TrimSpace(req.PreviousIntentID)
	req.Platform = strings.TrimSpace(req.Platform)
	req.AccountRef = strings.TrimSpace(req.AccountRef)
	req.ConversationRef = strings.TrimSpace(req.ConversationRef)
	if req.IntentID == "" || utf8.RuneCountInString(req.IntentID) > maxIntentIDRunes ||
		req.Platform == "" || req.AccountRef == "" || req.ConversationRef == "" {
		return nil, errors.New("缺少有效的 intentId/账号/会话标识")
	}
	actualText := req.Text
	if strings.TrimSpace(actualText) == "" {
		return nil, errors.New("发送文本不能为空")
	}
	args := protocol.ChatSendMessageArgs{ConversationRef: req.ConversationRef, Text: actualText}
	argsRaw, err := protocol.Encode(args)
	if err != nil {
		return nil, err
	}
	meta := protocol.Primitives[protocol.PrimChatSendMessage]
	if err := protocol.ValidatePrimitiveArgs(protocol.PrimChatSendMessage, meta.Ver, argsRaw); err != nil {
		return nil, err
	}
	payloadHash := hashBytes(argsRaw)

	// HTTP 重试必须先命中权威 intent，不可在首次发送已改变账本尾后
	// 重算 guards 并误报冲突。这里只比较用户不可变材料。
	if existing, lookupErr := d.st.EffectIntentByID(req.IntentID); lookupErr != nil {
		return nil, lookupErr
	} else if existing != nil {
		if existing.Platform != req.Platform || existing.AccountRef != req.AccountRef ||
			existing.Primitive != protocol.PrimChatSendMessage || existing.TargetRef != req.ConversationRef ||
			existing.PayloadHash != payloadHash {
			return nil, store.ErrEffectIntentConflict
		}
		return d.sendReceipt(existing, false)
	}
	// CAS 冲突也要能在手离线/身份暂不可用时把 current 回给重载后的
	// UI，因此先做一次只读快判；真正创建时 Store 仍在同一写事务里
	// 复查，覆盖多标签并发穿过此快照的竞态。
	latest, latestErr := d.st.LatestEffectIntent(req.Platform, req.AccountRef, req.ConversationRef)
	if latestErr != nil {
		return nil, latestErr
	}
	latestID := ""
	if latest != nil {
		latestID = latest.IntentID
	}
	if latestID != req.PreviousIntentID {
		conflict := &store.EffectIntentCASConflictError{
			PreviousIntentID: req.PreviousIntentID, Current: latest,
		}
		if latest == nil {
			return nil, conflict
		}
		receipt, receiptErr := d.sendReceipt(latest, false)
		return receipt, errors.Join(conflict, receiptErr)
	}

	key := store.ConversationKey{
		Platform: req.Platform, AccountRef: req.AccountRef, ConversationRef: req.ConversationRef,
	}
	preparation, err := d.st.PrepareSend(key, 5)
	if err != nil {
		return nil, err
	}
	if preparation.Account.BoundHandID == "" || preparation.Account.PrincipalFingerprint == nil {
		return nil, store.ErrAccountIdentityNotCurrent
	}
	session, bootID, online := d.sender.HandSession(preparation.Account.BoundHandID)
	if !online {
		return nil, ErrHandOffline
	}
	if preparation.Account.IdentityState != store.IdentityVerified ||
		preparation.Account.IdentitySession != session || preparation.Account.IdentityBootID != bootID {
		return nil, store.ErrAccountIdentityNotCurrent
	}
	if preparation.Account.ManualQuietUntil != nil && time.Now().Before(*preparation.Account.ManualQuietUntil) {
		return nil, store.ErrManualQuietActive
	}
	if err := d.requireNegotiation(preparation.Account.BoundHandID, protocol.PrimChatSendMessage, meta); err != nil {
		return nil, err
	}
	witness, ok := d.handWitness(preparation.Account.BoundHandID)
	if !ok || witness.StoreID == "" {
		return nil, ErrWitnessUnavailable
	}

	anchors := make([]protocol.MessageAnchor, len(preparation.Tail))
	for i := range preparation.Tail {
		anchors[i] = protocol.MessageAnchor{
			Direction:   protocol.MessageDirection(preparation.Tail[i].Direction),
			ContentHash: preparation.Tail[i].ContentHash,
		}
	}
	guards := protocol.ChatSendMessageGuards{ExpectedTail: anchors}
	guardsRaw, err := protocol.Encode(guards)
	if err != nil {
		return nil, err
	}
	if err := protocol.ValidatePrimitiveGuards(protocol.PrimChatSendMessage, meta.Ver, guardsRaw); err != nil {
		return nil, err
	}

	now := time.Now()
	deadlineMs := now.UnixMilli() + effectiveDeadlineMs(meta)
	idemKey := BuildEffectIdemKey(req.Platform, req.AccountRef, protocol.PrimChatSendMessage,
		req.ConversationRef, req.IntentID)
	intent := store.EffectIntent{
		IntentID: req.IntentID, IdemKey: idemKey, Platform: req.Platform, AccountRef: req.AccountRef,
		Primitive: protocol.PrimChatSendMessage, TargetRef: req.ConversationRef,
		PayloadHash: payloadHash, GuardsHash: hashBytes(guardsRaw), Status: store.EffectIntentDispatching,
		DeadlineMs: deadlineMs, SendFingerprint: syncledger.HashText(actualText),
	}
	detailed, dispatchErr := d.dispatchDetailed(DispatchRequest{
		HandID: preparation.Account.BoundHandID, ExpectedSession: session, ExpectedBootID: bootID,
		Name: protocol.PrimChatSendMessage, Args: argsRaw, Guards: guardsRaw,
		Context: &protocol.CmdContext{
			Platform: req.Platform, AccountRef: req.AccountRef,
			ExpectedPrincipalFingerprint: *preparation.Account.PrincipalFingerprint,
		},
	}, dispatchOptions{
		effectIntent: &intent, expectedTailSeq: preparation.Conversation.LastMessageSeq,
		previousIntentID: req.PreviousIntentID,
	})
	if dispatchErr != nil {
		var conflict *store.EffectIntentCASConflictError
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

func BuildEffectIdemKey(platform, accountRef, primitive, targetRef, intentID string) string {
	return fmt.Sprintf("ik1:%s:%s:%s:%s:%s", platform, accountRef, primitive, targetRef, intentID)
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func (d *Dispatcher) SendMessageStatus(intentID string) (*SendMessageReceipt, error) {
	intent, err := d.st.EffectIntentByID(strings.TrimSpace(intentID))
	if err != nil {
		return nil, err
	}
	if intent == nil {
		return nil, store.ErrEffectIntentNotFound
	}
	return d.sendReceipt(intent, false)
}

func (d *Dispatcher) LatestSendMessageStatus(platform, accountRef, conversationRef string) (*SendMessageReceipt, error) {
	platform = strings.TrimSpace(platform)
	accountRef = strings.TrimSpace(accountRef)
	conversationRef = strings.TrimSpace(conversationRef)
	if platform == "" || accountRef == "" || conversationRef == "" {
		return nil, errors.New("查询最新发送意图缺少账号/会话标识")
	}
	intent, err := d.st.LatestEffectIntent(platform, accountRef, conversationRef)
	if err != nil {
		return nil, err
	}
	if intent == nil {
		return nil, store.ErrEffectIntentNotFound
	}
	return d.sendReceipt(intent, false)
}

func (d *Dispatcher) sendReceipt(intent *store.EffectIntent, created bool) (*SendMessageReceipt, error) {
	if intent == nil {
		return nil, store.ErrEffectIntentNotFound
	}
	logical, err := d.st.LogicalDispatch(intent.RootMsgID)
	if err != nil {
		return nil, err
	}
	return &SendMessageReceipt{
		IntentID: intent.IntentID, LogicalDispatchID: intent.RootMsgID,
		MsgID: logical.Leaf.MsgID, Status: intent.Status, CommandStatus: logical.Leaf.Status,
		Created: created, VerificationAttempts: logical.Leaf.VerificationN,
		SuspectReason: intent.SuspectReason,
	}, nil
}

func (d *Dispatcher) handWitness(handID string) (HandWitness, bool) {
	sender, ok := d.sender.(witnessSender)
	if !ok {
		return HandWitness{}, false
	}
	return sender.HandWitness(handID)
}

type VerificationRequest struct {
	Command store.CmdRecord
	Intent  store.EffectIntent
	Args    protocol.ChatSendMessageArgs
	Guards  protocol.ChatSendMessageGuards
}

type VerificationObservation struct {
	Confirmed   bool
	ContentHash string
	ObservedAt  int64
	Reason      string
}

type EffectVerifier interface {
	Verify(context.Context, VerificationRequest) (VerificationObservation, error)
}

func (d *Dispatcher) SetEffectVerifier(verifier EffectVerifier) { d.verifier = verifier }

// RunVerificationRead 为 verifier 提供唯一的冻结域例外。仍经正式
// cmd 信封、Hub、手分发器与 result WAL，不存在“本地直读 DOM”捷径。
func (d *Dispatcher) RunVerificationRead(
	ctx context.Context,
	parentRef string,
	req DispatchRequest,
) (*store.LogicalDispatchState, error) {
	if existing, err := d.st.VerificationChildForParent(parentRef); err != nil {
		return nil, err
	} else if existing != nil {
		return d.waitAndConsumeVerificationChild(ctx, parentRef, existing.LogicalDispatchID)
	}
	result, err := d.dispatchDetailed(req, dispatchOptions{verificationFor: parentRef})
	if err != nil {
		if !errors.Is(err, store.ErrVerificationAlreadyRunning) {
			return nil, err
		}
		existing, lookupErr := d.st.VerificationChildForParent(parentRef)
		if lookupErr != nil {
			return nil, errors.Join(err, lookupErr)
		}
		if existing == nil {
			return nil, err
		}
		return d.waitAndConsumeVerificationChild(ctx, parentRef, existing.LogicalDispatchID)
	}
	return d.waitAndConsumeVerificationChild(ctx, parentRef, result.MsgID)
}

func (d *Dispatcher) waitAndConsumeVerificationChild(
	ctx context.Context, parentRef, logicalID string,
) (*store.LogicalDispatchState, error) {
	state, err := d.WaitLogical(ctx, logicalID)
	if err != nil {
		return nil, err // 超时/取消必须保留指针，供下次复用原 child。
	}
	if err := d.st.ConsumeVerificationChild(parentRef, logicalID); err != nil {
		return nil, err
	}
	return state, nil
}
