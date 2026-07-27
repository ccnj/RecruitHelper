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

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

const (
	maxIntentIDRunes       = 128
	verificationRetryDelay = 5 * time.Second
)

type SendMessageRequest struct {
	IntentID          string
	PreviousIntentID  string
	AutomaticActionID string
	ExpectedSession   string
	ExpectedBootID    string
	Platform          string
	AccountRef        string
	ConversationRef   string
	Text              string
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

type SendAutomaticCardRequest struct {
	IntentID          string
	PreviousIntentID  string
	AutomaticActionID string
	ExpectedSession   string
	ExpectedBootID    string
	Platform          string
	AccountRef        string
	ConversationRef   string
	Primitive         string
	Interview         *protocol.InterviewDetails
	RequestSourceKey  string
}

// SendMessage 是 chat.sendMessage 唯一产品入口。通用 /admin/cmd 和
// DispatchStructured 都无法铸造 Batch X effectful。调用方必须提供
// 已持久化的业务源：M3 真人意图或 M5 CommunicationAction 的确定性 ID。
// 重试只复用该 ID，脑从它确定性派生 idemKey。
func (d *Dispatcher) SendMessage(req SendMessageRequest) (*SendMessageReceipt, error) {
	req.IntentID = strings.TrimSpace(req.IntentID)
	req.PreviousIntentID = strings.TrimSpace(req.PreviousIntentID)
	req.AutomaticActionID = strings.TrimSpace(req.AutomaticActionID)
	req.ExpectedSession = strings.TrimSpace(req.ExpectedSession)
	req.ExpectedBootID = strings.TrimSpace(req.ExpectedBootID)
	req.Platform = strings.TrimSpace(req.Platform)
	req.AccountRef = strings.TrimSpace(req.AccountRef)
	req.ConversationRef = strings.TrimSpace(req.ConversationRef)
	if req.IntentID == "" || utf8.RuneCountInString(req.IntentID) > maxIntentIDRunes ||
		req.Platform == "" || req.AccountRef == "" || req.ConversationRef == "" {
		return nil, errors.New("缺少有效的 intentId/账号/会话标识")
	}
	if (req.ExpectedSession == "") != (req.ExpectedBootID == "") {
		return nil, errors.New("expected session/boot 必须成对提供")
	}
	if req.AutomaticActionID != "" {
		expectedIntentID, deriveErr := store.M5AutomaticIntentID(req.AutomaticActionID)
		if deriveErr != nil || req.IntentID != expectedIntentID {
			return nil, store.ErrCommunicationActionInvalid
		}
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
		if req.AutomaticActionID != "" {
			if err := d.st.ValidateM5AutomaticIntentLink(req.AutomaticActionID, req.IntentID); err != nil {
				return nil, err
			}
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
	if req.ExpectedSession != "" && (session != req.ExpectedSession || bootID != req.ExpectedBootID) {
		return nil, ErrStaleSession
	}
	if preparation.Account.IdentityState != store.IdentityVerified ||
		preparation.Account.IdentitySession != session || preparation.Account.IdentityBootID != bootID {
		return nil, store.ErrAccountIdentityNotCurrent
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
		previousIntentID: req.PreviousIntentID, automaticActionID: req.AutomaticActionID,
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

// SendAutomaticCard is the only M5 product entry for the two approved
// candidate-visible card primitives. The caller must supply a persisted
// CommunicationAction; the WAL transaction revalidates its exact primitive,
// payload and positive text dependency before any command can be queued.
func (d *Dispatcher) SendAutomaticCard(req SendAutomaticCardRequest) (*SendMessageReceipt, error) {
	req.IntentID = strings.TrimSpace(req.IntentID)
	req.PreviousIntentID = strings.TrimSpace(req.PreviousIntentID)
	req.AutomaticActionID = strings.TrimSpace(req.AutomaticActionID)
	req.ExpectedSession = strings.TrimSpace(req.ExpectedSession)
	req.ExpectedBootID = strings.TrimSpace(req.ExpectedBootID)
	req.Platform = strings.TrimSpace(req.Platform)
	req.AccountRef = strings.TrimSpace(req.AccountRef)
	req.ConversationRef = strings.TrimSpace(req.ConversationRef)
	req.Primitive = strings.TrimSpace(req.Primitive)
	req.RequestSourceKey = strings.TrimSpace(req.RequestSourceKey)
	if req.IntentID == "" ||
		utf8.RuneCountInString(req.IntentID) > maxIntentIDRunes ||
		req.AutomaticActionID == "" ||
		req.Platform == "" ||
		req.AccountRef == "" ||
		req.ConversationRef == "" {
		return nil, errors.New("缺少有效的卡片 intentId/actionId/账号/会话标识")
	}
	if (req.ExpectedSession == "") != (req.ExpectedBootID == "") {
		return nil, errors.New("expected session/boot 必须成对提供")
	}
	expectedIntentID, err := store.M5AutomaticIntentID(req.AutomaticActionID)
	if err != nil || req.IntentID != expectedIntentID {
		return nil, store.ErrCommunicationActionInvalid
	}

	var argsRaw []byte
	var fingerprint string
	switch req.Primitive {
	case protocol.PrimChatSendWechatInvite:
		if req.Interview != nil || req.RequestSourceKey != "" {
			return nil, store.ErrCommunicationActionInvalid
		}
		argsRaw, err = protocol.Encode(protocol.ChatSendWechatInviteArgs{
			ConversationRef: req.ConversationRef,
		})
		fingerprint = syncledger.WechatExchangeContentHash()
	case protocol.PrimChatSendInviteCard:
		if req.Interview == nil ||
			req.RequestSourceKey != "" ||
			req.Interview.StartsAt <= 0 ||
			req.Interview.EndsAt !=
				req.Interview.StartsAt+communication.V4InterviewDurationMs ||
			req.Interview.Method != protocol.InterviewMethodWechatVideo {
			return nil, store.ErrCommunicationActionInvalid
		}
		argsRaw, err = protocol.Encode(protocol.ChatSendInviteCardArgs{
			ConversationRef: req.ConversationRef,
			Interview:       *req.Interview,
		})
		fingerprint = syncledger.InterviewInviteContentHash(
			req.Interview.StartsAt,
			req.Interview.EndsAt,
			string(req.Interview.Method),
		)
	case protocol.PrimChatAcceptWechat:
		if req.Interview != nil {
			return nil, store.ErrCommunicationActionInvalid
		}
		argsRaw, err = protocol.Encode(protocol.ChatAcceptWechatArgs{
			ConversationRef:  req.ConversationRef,
			RequestSourceKey: req.RequestSourceKey,
		})
		if err == nil {
			fingerprint, err = store.AcceptWechatFingerprint(req.RequestSourceKey)
		}
	default:
		return nil, store.ErrCommunicationActionInvalid
	}
	if err != nil {
		return nil, err
	}
	meta := protocol.Primitives[req.Primitive]
	if err := protocol.ValidatePrimitiveArgs(req.Primitive, meta.Ver, argsRaw); err != nil {
		return nil, err
	}
	payloadHash := hashBytes(argsRaw)

	if existing, lookupErr := d.st.EffectIntentByID(req.IntentID); lookupErr != nil {
		return nil, lookupErr
	} else if existing != nil {
		if existing.Platform != req.Platform ||
			existing.AccountRef != req.AccountRef ||
			existing.Primitive != req.Primitive ||
			existing.TargetRef != req.ConversationRef ||
			existing.PayloadHash != payloadHash ||
			existing.SendFingerprint != fingerprint {
			return nil, store.ErrEffectIntentConflict
		}
		if err := d.st.ValidateM5AutomaticIntentLink(req.AutomaticActionID, req.IntentID); err != nil {
			return nil, err
		}
		return d.sendReceipt(existing, false)
	}
	latest, latestErr := d.st.LatestEffectIntent(
		req.Platform,
		req.AccountRef,
		req.ConversationRef,
	)
	if latestErr != nil {
		return nil, latestErr
	}
	latestID := ""
	if latest != nil {
		latestID = latest.IntentID
	}
	if latestID != req.PreviousIntentID {
		conflict := &store.EffectIntentCASConflictError{
			PreviousIntentID: req.PreviousIntentID,
			Current:          latest,
		}
		if latest == nil {
			return nil, conflict
		}
		receipt, receiptErr := d.sendReceipt(latest, false)
		return receipt, errors.Join(conflict, receiptErr)
	}

	key := store.ConversationKey{
		Platform:        req.Platform,
		AccountRef:      req.AccountRef,
		ConversationRef: req.ConversationRef,
	}
	preparation, err := d.st.PrepareSend(key, 5)
	if err != nil {
		return nil, err
	}
	if preparation.Account.BoundHandID == "" ||
		preparation.Account.PrincipalFingerprint == nil {
		return nil, store.ErrAccountIdentityNotCurrent
	}
	session, bootID, online := d.sender.HandSession(preparation.Account.BoundHandID)
	if !online {
		return nil, ErrHandOffline
	}
	if req.ExpectedSession != "" &&
		(session != req.ExpectedSession || bootID != req.ExpectedBootID) {
		return nil, ErrStaleSession
	}
	if preparation.Account.IdentityState != store.IdentityVerified ||
		preparation.Account.IdentitySession != session ||
		preparation.Account.IdentityBootID != bootID {
		return nil, store.ErrAccountIdentityNotCurrent
	}

	anchors := make([]protocol.MessageAnchor, len(preparation.Tail))
	for index := range preparation.Tail {
		anchors[index] = protocol.MessageAnchor{
			Direction:   protocol.MessageDirection(preparation.Tail[index].Direction),
			ContentHash: preparation.Tail[index].ContentHash,
		}
	}
	guardsRaw, err := protocol.Encode(protocol.ChatSendMessageGuards{ExpectedTail: anchors})
	if err != nil {
		return nil, err
	}
	if err := protocol.ValidatePrimitiveGuards(req.Primitive, meta.Ver, guardsRaw); err != nil {
		return nil, err
	}

	now := time.Now()
	deadlineMs := now.UnixMilli() + effectiveDeadlineMs(meta)
	idemKey := BuildEffectIdemKey(
		req.Platform,
		req.AccountRef,
		req.Primitive,
		req.ConversationRef,
		req.IntentID,
	)
	intent := store.EffectIntent{
		IntentID:        req.IntentID,
		IdemKey:         idemKey,
		Platform:        req.Platform,
		AccountRef:      req.AccountRef,
		Primitive:       req.Primitive,
		TargetRef:       req.ConversationRef,
		PayloadHash:     payloadHash,
		GuardsHash:      hashBytes(guardsRaw),
		Status:          store.EffectIntentDispatching,
		DeadlineMs:      deadlineMs,
		SendFingerprint: fingerprint,
	}
	detailed, dispatchErr := d.dispatchDetailed(dispatchRequestForPreparedEffect(
		preparation,
		session,
		bootID,
		req,
		argsRaw,
		guardsRaw,
		intent,
	))
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

func dispatchRequestForPreparedEffect(
	preparation *store.SendPreparation,
	session string,
	bootID string,
	req SendAutomaticCardRequest,
	argsRaw []byte,
	guardsRaw []byte,
	intent store.EffectIntent,
) (DispatchRequest, dispatchOptions) {
	return DispatchRequest{
			HandID:          preparation.Account.BoundHandID,
			ExpectedSession: session,
			ExpectedBootID:  bootID,
			Name:            req.Primitive,
			Args:            argsRaw,
			Guards:          guardsRaw,
			Context: &protocol.CmdContext{
				Platform:                     req.Platform,
				AccountRef:                   req.AccountRef,
				ExpectedPrincipalFingerprint: *preparation.Account.PrincipalFingerprint,
			},
		}, dispatchOptions{
			effectIntent:      &intent,
			expectedTailSeq:   preparation.Conversation.LastMessageSeq,
			previousIntentID:  req.PreviousIntentID,
			automaticActionID: req.AutomaticActionID,
		}
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
	Command          store.CmdRecord
	Intent           store.EffectIntent
	Args             protocol.ChatSendMessageArgs
	Guards           protocol.ChatSendMessageGuards
	GreetingArgs     *protocol.ChatSendGreetingArgs
	WechatInviteArgs *protocol.ChatSendWechatInviteArgs
	InviteCardArgs   *protocol.ChatSendInviteCardArgs
	AcceptWechatArgs *protocol.ChatAcceptWechatArgs
}

type VerificationObservation struct {
	Confirmed       bool
	ContentHash     string
	SourceKey       string
	Interview       *protocol.InterviewDetails
	ConversationRef string
	PeerWechat      string
	ObservedAt      int64
	Reason          string
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
