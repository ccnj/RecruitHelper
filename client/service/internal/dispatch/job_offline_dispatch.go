package dispatch

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// TakeJobOfflineRequest 是把一个职位下线所需的全部材料。JobName 必须是后台
// 职位名（job.name），与发布时用的是同一个身份键。
type TakeJobOfflineRequest struct {
	Platform   string
	AccountRef string
	JobID      string
	JobName    string
}

// TakeJobOfflineReceipt 只回执意图身份与状态。下线结果由调用方按 intentID 读
// 账本，不在回执里重复一份可能过期的副本。
type TakeJobOfflineReceipt struct {
	IntentID string
	MsgID    string
	Created  bool
	Status   string
}

// BuildTakeJobOfflineIntentID 由职位与尝试序号派生稳定意图标识。
//
// 与发布不同，下线没有"发布参数"这样的载荷：同一个职位下线两次在业务上就是
// 同一件事，所以身份只取 jobID。attempt 从 1 起，序号 1 不带后缀。
func BuildTakeJobOfflineIntentID(jobID string, attempt int) string {
	sum := sha256.Sum256([]byte("jobOffline\x00" + jobID))
	id := "jo-" + hex.EncodeToString(sum[:12])
	if attempt > 1 {
		id += "-" + strconv.Itoa(attempt)
	}
	return id
}

// maxTakeJobOfflineAttempts 只是防止派生循环无界，不是业务重试预算。
const maxTakeJobOfflineAttempts = 20

// TakeJobOffline 派发唯一一次职位下线。WAL 与 idemKey 唯一性都在
// dispatchDetailed → CreateJobPublishEffectIntentAndCmd 的同一事务内完成。
//
// 它与 PublishJob 是两条独立链路：发布成功而下线失败时，已发布那笔账原样成立。
// 调用方拿到 receipt 后不得因下线未成而回改发布结论。
func (d *Dispatcher) TakeJobOffline(req TakeJobOfflineRequest) (*TakeJobOfflineReceipt, error) {
	req.Platform = strings.TrimSpace(req.Platform)
	req.AccountRef = strings.TrimSpace(req.AccountRef)
	req.JobID = strings.TrimSpace(req.JobID)
	req.JobName = strings.TrimSpace(req.JobName)
	if req.Platform == "" || req.AccountRef == "" || req.JobID == "" {
		return nil, errors.New("缺少有效的平台、账号或职位标识")
	}
	if req.JobName == "" {
		return nil, errors.New("缺少后台职位名")
	}

	meta := protocol.Primitives[protocol.PrimJobTakeOffline]
	argsRaw, err := protocol.Encode(protocol.JobTakeOfflineArgs{JobName: req.JobName})
	if err != nil {
		return nil, err
	}
	if err := protocol.ValidatePrimitiveArgs(protocol.PrimJobTakeOffline, meta.Ver, argsRaw); err != nil {
		return nil, err
	}
	guardsRaw, err := protocol.Encode(protocol.JobTakeOfflineGuards{ExpectOnlineOnPlatform: true})
	if err != nil {
		return nil, err
	}
	if err := protocol.ValidatePrimitiveGuards(protocol.PrimJobTakeOffline, meta.Ver, guardsRaw); err != nil {
		return nil, err
	}
	payloadHash := hashBytes(argsRaw)

	// 精确重试优先收编原意图。命中后不再重跑——那可能在平台上已经点过确认了。
	//
	// 例外与发布同口径：既有意图已终局时递进尝试序号铸新意图。下线的重来在平台
	// 侧是自然幂等的（已下线的行根本没有下线入口，手会在 guards 阶段干净失败），
	// 所以这里比发布更安全；未收束的（在途、suspect）一律直接回原收据。
	intentID := ""
	for attempt := 1; attempt <= maxTakeJobOfflineAttempts; attempt++ {
		candidate := BuildTakeJobOfflineIntentID(req.JobID, attempt)
		existing, lookupErr := d.st.EffectIntentByID(candidate)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if existing == nil {
			intentID = candidate
			break
		}
		if existing.Platform != req.Platform || existing.AccountRef != req.AccountRef ||
			existing.Primitive != protocol.PrimJobTakeOffline || existing.TargetRef != req.JobID {
			return nil, store.ErrEffectIntentConflict
		}
		if !publishAttemptSettled(*existing) {
			return &TakeJobOfflineReceipt{
				IntentID: existing.IntentID, MsgID: existing.RootMsgID,
				Created: false, Status: string(existing.Status),
			}, nil
		}
	}
	if intentID == "" {
		return nil, errors.New("该职位的下线尝试次数已达上限，先查清原因再试")
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
			protocol.PrimJobTakeOffline, req.JobID, intentID),
		Platform: req.Platform, AccountRef: req.AccountRef,
		Primitive: protocol.PrimJobTakeOffline, TargetRef: req.JobID,
		PayloadHash: payloadHash, GuardsHash: hashBytes(guardsRaw),
		Status:     store.EffectIntentDispatching,
		DeadlineMs: now.UnixMilli() + effectiveDeadlineMs(meta),
	}
	detailed, dispatchErr := d.dispatchDetailed(DispatchRequest{
		HandID: account.BoundHandID, ExpectedSession: session, ExpectedBootID: bootID,
		Name: protocol.PrimJobTakeOffline, Args: argsRaw, Guards: guardsRaw,
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
	return &TakeJobOfflineReceipt{
		IntentID: persisted.IntentID, MsgID: detailed.MsgID,
		Created: detailed.Created, Status: string(persisted.Status),
	}, dispatchErr
}

// TakeJobOfflineStatus 按意图标识读当前状态，供 HTTP 超时后继续查。
func (d *Dispatcher) TakeJobOfflineStatus(intentID string) (*store.EffectIntent, error) {
	intent, err := d.st.EffectIntentByID(strings.TrimSpace(intentID))
	if err != nil {
		return nil, err
	}
	if intent == nil || intent.Primitive != protocol.PrimJobTakeOffline {
		return nil, store.ErrEffectIntentNotFound
	}
	return intent, nil
}
