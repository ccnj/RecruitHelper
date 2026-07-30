package dispatch

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// PublishJobRequest 是发布一个职位所需的全部材料。JobName 必须是后台职位名
// （job.name），不是发布参数里的职位名称——后者是死字段。
type PublishJobRequest struct {
	Platform   string
	AccountRef string
	JobID      string
	Args       protocol.JobPrepareDraftArgs
}

// PublishJobReceipt 只回执意图身份与状态。发布结果（是否取得平台正证）由
// 调用方按 intentID 读账本，不在回执里重复一份可能过期的副本。
type PublishJobReceipt struct {
	IntentID string
	MsgID    string
	Created  bool
	Status   string
}

// BuildPublishJobIntentID 由职位与本次发布材料派生稳定意图标识。
//
// 同一职位 + 同一份发布参数 → 同一 intentID → 同一 idemKey → 账本闸只允许发
// 一次；HTTP 重试会收编原意图而不是另铸一个。运营改了发布参数再发是新的
// intentID，允许发第二次（甲方 2026-07-30 裁决的口径），此时平台侧"同名不重发"
// 那道闸仍然兜着。
func BuildPublishJobIntentID(jobID, payloadHash string) string {
	sum := sha256.Sum256([]byte("jobPublish\x00" + jobID + "\x00" + payloadHash))
	return "jp-" + hex.EncodeToString(sum[:12])
}

// PublishJob 派发唯一一次职位发布。WAL 与 idemKey 唯一性都在
// dispatchDetailed → CreateEffectIntentAndCmd 的同一事务内完成。
func (d *Dispatcher) PublishJob(req PublishJobRequest) (*PublishJobReceipt, error) {
	req.Platform = strings.TrimSpace(req.Platform)
	req.AccountRef = strings.TrimSpace(req.AccountRef)
	req.JobID = strings.TrimSpace(req.JobID)
	if req.Platform == "" || req.AccountRef == "" || req.JobID == "" {
		return nil, errors.New("缺少有效的平台、账号或职位标识")
	}
	if strings.TrimSpace(req.Args.JobName) == "" {
		return nil, errors.New("缺少后台职位名")
	}

	meta := protocol.Primitives[protocol.PrimJobPublishDraft]
	argsRaw, err := protocol.Encode(req.Args)
	if err != nil {
		return nil, err
	}
	if err := protocol.ValidatePrimitiveArgs(protocol.PrimJobPublishDraft, meta.Ver, argsRaw); err != nil {
		return nil, err
	}
	guardsRaw, err := protocol.Encode(protocol.JobPublishDraftGuards{ExpectAbsentOnPlatform: true})
	if err != nil {
		return nil, err
	}
	if err := protocol.ValidatePrimitiveGuards(protocol.PrimJobPublishDraft, meta.Ver, guardsRaw); err != nil {
		return nil, err
	}
	payloadHash := hashBytes(argsRaw)
	intentID := BuildPublishJobIntentID(req.JobID, payloadHash)

	// 精确重试优先收编原意图。命中后不再重跑准备阶段——那可能在平台上已经
	// 发出去了，再走一遍就是第二次发布。
	if existing, lookupErr := d.st.EffectIntentByID(intentID); lookupErr != nil {
		return nil, lookupErr
	} else if existing != nil {
		if existing.Platform != req.Platform || existing.AccountRef != req.AccountRef ||
			existing.Primitive != protocol.PrimJobPublishDraft || existing.TargetRef != req.JobID ||
			existing.PayloadHash != payloadHash {
			return nil, store.ErrEffectIntentConflict
		}
		return &PublishJobReceipt{
			IntentID: existing.IntentID, MsgID: existing.RootMsgID,
			Created: false, Status: string(existing.Status),
		}, nil
	}

	account, err := d.st.AccountByKey(store.AccountKey{Platform: req.Platform, AccountRef: req.AccountRef})
	if err != nil {
		return nil, err
	}
	if account == nil || account.BoundHandID == "" || account.PrincipalFingerprint == nil ||
		account.IdentityState != store.IdentityVerified {
		return nil, store.ErrAccountIdentityNotCurrent
	}
	session, bootID, online := d.sender.HandSession(account.BoundHandID)
	if !online {
		return nil, ErrHandOffline
	}
	if account.IdentitySession != session || account.IdentityBootID != bootID {
		return nil, store.ErrAccountIdentityNotCurrent
	}

	now := time.Now()
	intent := store.EffectIntent{
		IntentID: intentID,
		IdemKey: BuildEffectIdemKey(req.Platform, req.AccountRef,
			protocol.PrimJobPublishDraft, req.JobID, intentID),
		Platform: req.Platform, AccountRef: req.AccountRef,
		Primitive: protocol.PrimJobPublishDraft, TargetRef: req.JobID,
		PayloadHash: payloadHash, GuardsHash: hashBytes(guardsRaw),
		Status:     store.EffectIntentDispatching,
		DeadlineMs: now.UnixMilli() + effectiveDeadlineMs(meta),
	}
	detailed, dispatchErr := d.dispatchDetailed(DispatchRequest{
		HandID: account.BoundHandID, ExpectedSession: session, ExpectedBootID: bootID,
		Name: protocol.PrimJobPublishDraft, Args: argsRaw, Guards: guardsRaw,
		Context: &protocol.CmdContext{
			Platform: req.Platform, AccountRef: req.AccountRef,
			ExpectedPrincipalFingerprint: *account.PrincipalFingerprint,
		},
	}, dispatchOptions{effectIntent: &intent})
	if detailed.MsgID == "" {
		return nil, dispatchErr
	}
	persisted, lookupErr := d.st.EffectIntentByID(intentID)
	if lookupErr != nil {
		return nil, errors.Join(dispatchErr, lookupErr)
	}
	if persisted == nil {
		return nil, errors.Join(dispatchErr, store.ErrEffectIntentNotFound)
	}
	receipt := &PublishJobReceipt{
		IntentID: persisted.IntentID, MsgID: detailed.MsgID,
		Created: detailed.Created, Status: string(persisted.Status),
	}
	return receipt, dispatchErr
}

// PublishJobStatus 按意图标识读当前状态，供 HTTP 超时后继续查。
func (d *Dispatcher) PublishJobStatus(intentID string) (*store.EffectIntent, error) {
	intent, err := d.st.EffectIntentByID(strings.TrimSpace(intentID))
	if err != nil {
		return nil, err
	}
	if intent == nil || intent.Primitive != protocol.PrimJobPublishDraft {
		return nil, store.ErrEffectIntentNotFound
	}
	return intent, nil
}
