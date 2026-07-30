package dispatch

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
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

// BuildPublishJobIntentID 由职位、本次发布材料与尝试序号派生稳定意图标识。
//
// 同一职位 + 同一份发布参数 + 同一序号 → 同一 intentID → 同一 idemKey → 同一次
// 尝试只允许发一次；HTTP 重试会收编原意图而不是另铸一个。运营改了发布参数再发是
// 新的 intentID，允许发第二次（甲方 2026-07-30 裁决的口径），此时平台侧"同名不
// 重发"那道闸仍然兜着。
//
// attempt 从 1 起。序号 1 不带后缀，好让本裁决之前落下的意图仍然命中同一身份。
func BuildPublishJobIntentID(jobID, payloadHash string, attempt int) string {
	sum := sha256.Sum256([]byte("jobPublish\x00" + jobID + "\x00" + payloadHash))
	id := "jp-" + hex.EncodeToString(sum[:12])
	if attempt > 1 {
		id += "-" + strconv.Itoa(attempt)
	}
	return id
}

// maxPublishJobAttempts 只是防止派生循环无界，不是业务重试预算：每一次尝试都
// 要人在产品上重新点一次发布。
const maxPublishJobAttempts = 20

// publishAttemptSettled 判断某个既有意图是否已经终局，因而运营显式再发时可以
// 递进尝试序号、铸造新意图（甲方 2026-07-30 裁决）。
//
// 账本闸的本意是防误重发（HTTP 重试、连点两下），不是禁止一次新的业务行为：
// 发布 → 下架/删除 → 重新发布是正常招聘动作，卡死它等于删过的职位永远发不了。
// 真正的防重复闸是点击前的 expectAbsentOnPlatform——平台实读，比哈希比对强得多；
// 职位还在时那道闸会让本次干净失败（sideEffect=none），不会产生重复职位。
//
// suspect 例外：它是"结果未知、等人裁决"的未收束态，绝不能被新尝试绕过，否则
// idemKey 冻结形同虚设。在途（dispatching/reconciling/verifying）同理。
func publishAttemptSettled(intent store.EffectIntent) bool {
	switch intent.Status {
	case store.EffectIntentOk, store.EffectIntentResolvedOk,
		store.EffectIntentFailed, store.EffectIntentResolvedFailed:
		return true
	default:
		// dispatching / reconciling / verifying / suspect 一律不许重来。
		return false
	}
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

	// 精确重试优先收编原意图。命中后不再重跑准备阶段——那可能在平台上已经
	// 发出去了，再走一遍就是第二次发布。
	//
	// 例外：既有意图已经终局时，运营显式再发就递进尝试序号铸造新意图。旧意图
	// 是永久终局、不原地复活，也不改写业务事实。未收束的（在途、suspect）一律
	// 直接回原收据，绝不绕过。
	intentID := ""
	superseded := ""
	supersededStatus := ""
	for attempt := 1; attempt <= maxPublishJobAttempts; attempt++ {
		candidate := BuildPublishJobIntentID(req.JobID, payloadHash, attempt)
		existing, lookupErr := d.st.EffectIntentByID(candidate)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if existing == nil {
			intentID = candidate
			break
		}
		if existing.Platform != req.Platform || existing.AccountRef != req.AccountRef ||
			existing.Primitive != protocol.PrimJobPublishDraft || existing.TargetRef != req.JobID ||
			existing.PayloadHash != payloadHash {
			return nil, store.ErrEffectIntentConflict
		}
		if !publishAttemptSettled(*existing) {
			return &PublishJobReceipt{
				IntentID: existing.IntentID, MsgID: existing.RootMsgID,
				Created: false, Status: string(existing.Status),
			}, nil
		}
		superseded, supersededStatus = existing.IntentID, string(existing.Status)
	}
	if intentID == "" {
		return nil, errors.New("该职位同一份发布参数的尝试次数已达上限，先查清原因再发")
	}
	if superseded != "" {
		// 在既有终局意图之后重新发起必须留痕：这是账本上"同一份参数发第二次"
		// 的唯一线索，尤其当上一次是 ok/resolvedOk（职位曾经真的上过线）。
		d.st.Audit("job_publish_reattempt", "", superseded,
			fmt.Sprintf("jobId=%s 既有意图 %s(%s) 已终局，运营显式再发，新意图 %s",
				req.JobID, superseded, supersededStatus, intentID))
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
