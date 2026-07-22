package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

// DispatchRequest 是业务调度唯一的结构化入口。Context 直接使用契约生成类型，脑端
// 不另造一份近似结构；effectful 的 IdemKey 必须由业务意图确定性提供。
type DispatchRequest struct {
	HandID          string
	ExpectedSession string
	ExpectedBootID  string
	Name            string
	Args            json.RawMessage
	Context         *protocol.CmdContext
	IdemKey         string
	Guards          json.RawMessage
}

type dispatchOptions struct {
	legacyDebug            bool
	effectIntent           *store.EffectIntent
	expectedTailSeq        int64
	previousIntentID       string
	automaticActionID      string
	verificationFor        string
	resumeCaptureProfileID string
}

type dispatchResult struct {
	MsgID   string
	Created bool
}

// DispatchStructured 先持久化完整命令上下文，再发送同一份 generated CmdBody。
// 返回根物理 msgId；根的 logicalDispatchId 与它相同。
func (d *Dispatcher) DispatchStructured(req DispatchRequest) (string, error) {
	return d.dispatch(req, dispatchOptions{})
}

// Run 是 actor/调度器接缝：派发后跨物理 replacement chain 等待最终 leaf 终局。
func (d *Dispatcher) Run(ctx context.Context, req DispatchRequest) (*store.LogicalDispatchState, error) {
	logicalID, err := d.DispatchStructured(req)
	if err != nil {
		return nil, err
	}
	return d.WaitLogical(ctx, logicalID)
}

// WaitLogical 只根据持久化谱系的当前 leaf 判定完成。旧物理节点 void 后原子出现 child，
// 因此中间 void 的状态变化最多促使重查，绝不会作为逻辑结果返回。
func (d *Dispatcher) WaitLogical(ctx context.Context, logicalID string) (*store.LogicalDispatchState, error) {
	d.subscribeLogical(logicalID)
	defer d.unsubscribeLogical(logicalID)
	for {
		changed := d.logicalChanged(logicalID)
		state, err := d.st.LogicalDispatch(logicalID)
		if err != nil {
			return nil, err
		}
		if state.Settled {
			return state, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (d *Dispatcher) dispatch(req DispatchRequest, opts dispatchOptions) (string, error) {
	result, err := d.dispatchDetailed(req, opts)
	return result.MsgID, err
}

func (d *Dispatcher) dispatchDetailed(req DispatchRequest, opts dispatchOptions) (dispatchResult, error) {
	meta, ok := protocol.Primitives[req.Name]
	if !ok || meta.Ver == 0 {
		return dispatchResult{}, fmt.Errorf("未知或未激活原语 %q", req.Name)
	}
	if req.HandID == "" {
		return dispatchResult{}, errors.New("handId 不能为空")
	}
	releaseHandGate := d.lockHandGate(req.HandID)
	defer releaseHandGate()
	if opts.legacyDebug && !strings.HasPrefix(req.Name, "debug.") {
		return dispatchResult{}, errors.New("旧 Dispatch 仅供 debug.*；业务命令请使用 DispatchStructured")
	}
	if meta.Batch == protocol.BatchX && meta.Class == protocol.ClassEffectful && opts.effectIntent == nil {
		return dispatchResult{}, errors.New("真实 effectful 只允许经发送意图入口派发")
	}
	session, bootID, online := d.sender.HandSession(req.HandID)
	if !online {
		return dispatchResult{}, ErrHandOffline
	}
	generationBound := req.ExpectedSession != "" || req.ExpectedBootID != ""
	if generationBound {
		if req.ExpectedSession == "" || req.ExpectedBootID == "" {
			return dispatchResult{}, errors.New("expected session/boot 必须成对提供")
		}
		if session != req.ExpectedSession || bootID != req.ExpectedBootID {
			return dispatchResult{}, ErrStaleSession
		}
	}
	identityRead := req.Name == protocol.PrimCandidateReadResume ||
		req.Name == protocol.PrimCandidateReadSourcingResume ||
		req.Name == protocol.PrimCandidateReadSourcingWindow ||
		req.Name == protocol.PrimCandidateReadSourcingTargetResume
	if meta.Class == protocol.ClassEffectful || identityRead {
		matched, current := d.sender.HandContractMatch(req.HandID)
		if !current {
			return dispatchResult{}, ErrHandOffline
		}
		if !matched {
			category := "effect_contract_mismatch_blocked"
			if identityRead {
				category = "resume_contract_mismatch_blocked"
			}
			d.st.Audit(category, req.HandID, "",
				fmt.Sprintf("stage=construct primitive=%s", req.Name))
			return dispatchResult{}, ErrContractMismatch
		}
	}
	if !opts.legacyDebug {
		if err := d.requireNegotiation(req.HandID, req.Name, meta); err != nil {
			return dispatchResult{}, err
		}
	}
	if opts.effectIntent != nil {
		blocked, err := d.st.HasEffectRecoveryForHand(req.HandID)
		if err != nil {
			return dispatchResult{}, err
		}
		if blocked {
			return dispatchResult{}, ErrRecoveryBarrier
		}
	}

	msgID := ids.NewMsgID()
	deadlineMs := time.Now().UnixMilli() + effectiveDeadlineMs(meta)
	idemKey := req.IdemKey
	if opts.legacyDebug && meta.Class == protocol.ClassEffectful && idemKey == "" {
		idemKey = fmt.Sprintf("ik1:debug:%s:%s:-:%s", req.HandID, req.Name, ids.NewMsgID())
	}
	if opts.effectIntent != nil {
		idemKey = opts.effectIntent.IdemKey
		deadlineMs = opts.effectIntent.DeadlineMs
	}
	domain := domainOf(req.HandID, req.Name)
	contextJSON := ""
	platform := ""
	accountRef := ""
	fingerprint := ""
	if req.Context != nil {
		platform = req.Context.Platform
		accountRef = req.Context.AccountRef
		fingerprint = req.Context.ExpectedPrincipalFingerprint
		domain = platform + ":" + accountRef
		raw, err := protocol.Encode(req.Context)
		if err != nil {
			return dispatchResult{}, fmt.Errorf("编码 context: %w", err)
		}
		contextJSON = string(raw)
	}

	body := protocol.CmdBody{
		Name: req.Name, Ver: meta.Ver, Args: req.Args, Context: req.Context, IdemKey: idemKey,
		Deadline: deadlineMs, ExecBudgetMs: effectiveBudgetMs(meta), LeaseMs: meta.LeaseMs,
		Guards: req.Guards,
	}
	bodyRaw, err := protocol.Encode(body)
	if err != nil {
		return dispatchResult{}, err
	}
	if err := protocol.ValidateKindBody(protocol.KindCmd, bodyRaw); err != nil {
		return dispatchResult{}, fmt.Errorf("命令契约校验: %w", err)
	}

	// 旧 debug wrapper 保留 M1 的 suspect 冻结语义；业务入口由 Store 在单写事务里
	// 原子完成“检查 domain + 创建 queued”，避免跨手同账号的 TOCTOU。
	if opts.legacyDebug && meta.Class != protocol.ClassReadonly {
		if frozen, _ := d.st.HasSuspectInDomain(domain); frozen {
			return dispatchResult{}, ErrDomainFrozen
		}
	}
	if meta.Class == protocol.ClassEffectful {
		if frozen, _ := d.st.HasSuspectIdemKey(idemKey); frozen {
			return dispatchResult{}, ErrIdemFrozen
		}
	}
	witnessStoreID := ""
	if meta.Batch == protocol.BatchX && meta.Class == protocol.ClassEffectful {
		witness, ok := d.handWitness(req.HandID)
		if !ok || witness.StoreID == "" {
			return dispatchResult{}, ErrWitnessUnavailable
		}
		witnessStoreID = witness.StoreID
	}

	rec := &store.CmdRecord{
		MsgID: msgID, Name: req.Name, Class: string(meta.Class), IdemKey: idemKey,
		Domain: domain, Platform: platform, AccountRef: accountRef,
		ExpectedPrincipalFingerprint: fingerprint, ContextJSON: contextJSON, Args: string(req.Args), Guards: string(req.Guards),
		HandID: req.HandID, Session: session, BootIDAtDispatch: bootID,
		Status: store.CmdQueued, DeadlineMs: deadlineMs, ExecBudgetMs: body.ExecBudgetMs,
		WitnessStoreIDAtDispatch: witnessStoreID, VerificationForMsgID: opts.verificationFor,
	}
	created := true
	if opts.effectIntent != nil {
		rec.IntentID = opts.effectIntent.IntentID
		var createdResult *store.CreateEffectIntentResult
		var createErr error
		switch opts.effectIntent.Primitive {
		case protocol.PrimChatSendGreeting:
			createdResult, createErr = d.st.CreateGreetingEffectIntentAndCmd(store.CreateGreetingEffectIntentRequest{
				Intent: *opts.effectIntent, Command: *rec,
				PreviousIntentID: opts.previousIntentID, Now: time.Now(),
			})
		case protocol.PrimChatSendMessage:
			createdResult, createErr = d.st.CreateEffectIntentAndCmd(store.CreateEffectIntentRequest{
				Intent: *opts.effectIntent, Command: *rec, ExpectedTailSeq: opts.expectedTailSeq,
				PreviousIntentID: opts.previousIntentID, AutomaticActionID: opts.automaticActionID,
				Now: time.Now(),
			})
		default:
			createErr = fmt.Errorf("真实副作用原语没有账本入口 %q", opts.effectIntent.Primitive)
		}
		if createErr != nil {
			err = createErr
		} else {
			created = createdResult.Created
			rec = &createdResult.Command
			msgID = rec.MsgID
		}
	} else if opts.resumeCaptureProfileID != "" {
		var createdResult *store.CreateResumeCaptureCmdResult
		createdResult, err = d.st.CreateResumeCaptureCmd(store.CreateResumeCaptureCmdRequest{
			ProfileID: opts.resumeCaptureProfileID, Command: *rec, Now: time.Now(),
		})
		if err == nil {
			created = createdResult.Created
			rec = &createdResult.Command
			msgID = rec.LogicalDispatchID
		}
	} else if opts.verificationFor != "" {
		err = d.st.CreateVerificationCmd(opts.verificationFor, rec)
	} else if !opts.legacyDebug && meta.Class != protocol.ClassReadonly {
		err = d.st.CreateCmdIfDomainAvailable(rec)
	} else {
		err = d.st.CreateCmd(rec)
	}
	if err != nil {
		return dispatchResult{}, fmt.Errorf("记账: %w", err)
	}
	if !created {
		return dispatchResult{MsgID: msgID, Created: false}, nil
	}

	env := d.envelope(protocol.KindCmd, msgID, &session, body)
	if err := d.sender.SendEnvelope(req.HandID, env); err != nil {
		if errors.Is(err, ErrContractMismatch) && opts.effectIntent != nil {
			abortErr := d.st.AbortEffectBeforeSend(msgID, contractMismatchBeforeSendCode,
				"contract mismatch before socket write", time.Now())
			d.notifyByMsgID(msgID)
			if abortErr != nil {
				return dispatchResult{MsgID: msgID, Created: true}, errors.Join(err, abortErr)
			}
			return dispatchResult{MsgID: msgID, Created: true}, err
		}
		if generationBound && (errors.Is(err, ErrStaleSession) || errors.Is(err, ErrHandOffline)) {
			var voidErr error
			if opts.effectIntent != nil {
				voidErr = d.st.AbortEffectBeforeSend(msgID, string(protocol.ErrCodeStaleSession),
					"actor generation changed before socket write", time.Now())
				d.notifyByMsgID(msgID)
			} else {
				voidErr = d.voidGenerationBoundBeforeSend(req.HandID, msgID, err)
			}
			if voidErr != nil {
				return dispatchResult{MsgID: msgID, Created: true}, errors.Join(err, voidErr)
			}
			slog.Warn("actor 命令在 socket 写入前代际失效,已 void", "handId", req.HandID, "msgId", msgID, "err", err)
			return dispatchResult{MsgID: msgID, Created: true}, err
		}
		if opts.effectIntent != nil {
			_ = d.st.MoveEffectToVerification(msgID, "socket write outcome unknown", time.Now())
		}
		slog.Warn("cmd 发送失败,留 queued", "handId", req.HandID, "msgId", msgID, "err", err)
		return dispatchResult{MsgID: msgID, Created: true}, err
	}
	d.markSent(msgID, session, session)
	slog.Info("已派发", "handId", req.HandID, "name", req.Name, "msgId", msgID, "class", meta.Class)
	return dispatchResult{MsgID: msgID, Created: true}, nil
}

// generation-bound actor 命令若在 Hub 的线性化写门禁前发现 session 已更替/离线，
// 可证明没有字节落入旧 socket。此时必须终局 void，不能让普通 queued 故障轨道
// 稍后把它重投到未经 fresh probe 的新代。
func (d *Dispatcher) voidGenerationBoundBeforeSend(handID, msgID string, sendErr error) error {
	err := d.st.MutateCmd(msgID, func(record *store.CmdRecord) error {
		if record.Status.Terminal() {
			return nil
		}
		record.Status = store.CmdVoid
		record.ErrorCode = string(protocol.ErrCodeStaleSession)
		record.SuspectReason = "actor generation changed before socket write"
		return nil
	})
	if err != nil {
		return err
	}
	d.st.Audit("actor_generation_dispatch_aborted", handID, msgID, sendErr.Error())
	d.notifyByMsgID(msgID)
	return nil
}

func (d *Dispatcher) requireNegotiation(handID, name string, meta protocol.PrimitiveMeta) error {
	if meta.Batch != protocol.BatchS && meta.Batch != protocol.BatchX {
		return nil
	}
	caps, features, ok := d.sender.HandNegotiation(handID)
	if !ok || !contains(caps, fmt.Sprintf("%s@%d", name, meta.Ver)) {
		return fmt.Errorf("%w: %s@%d", ErrCapability, name, meta.Ver)
	}
	if meta.Batch == protocol.BatchX && meta.Class == protocol.ClassEffectful && !contains(features, string(protocol.FeatureWitness1)) {
		return fmt.Errorf("%w: %s", ErrFeature, protocol.FeatureWitness1)
	}
	if meta.LeaseMs != 0 {
		for _, feature := range []protocol.Feature{protocol.FeatureLease1, protocol.FeatureProgress1, protocol.FeatureCancel1} {
			if !contains(features, string(feature)) {
				return fmt.Errorf("%w: %s", ErrFeature, feature)
			}
		}
	}
	return nil
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func (d *Dispatcher) commandBody(rec store.CmdRecord) (protocol.CmdBody, error) {
	meta, ok := protocol.Primitives[rec.Name]
	if !ok || meta.Ver == 0 {
		return protocol.CmdBody{}, fmt.Errorf("未知或未激活原语 %q", rec.Name)
	}
	var cmdContext *protocol.CmdContext
	if rec.ContextJSON != "" {
		var decoded protocol.CmdContext
		if err := json.Unmarshal([]byte(rec.ContextJSON), &decoded); err != nil {
			return protocol.CmdBody{}, fmt.Errorf("恢复 context: %w", err)
		}
		cmdContext = &decoded
	}
	body := protocol.CmdBody{
		Name: rec.Name, Ver: meta.Ver, Args: json.RawMessage(rec.Args), Context: cmdContext,
		IdemKey: rec.IdemKey, Deadline: rec.DeadlineMs, ExecBudgetMs: rec.ExecBudgetMs,
		LeaseMs: meta.LeaseMs, Guards: json.RawMessage(rec.Guards),
	}
	raw, err := protocol.Encode(body)
	if err != nil {
		return protocol.CmdBody{}, err
	}
	if err := protocol.ValidateKindBody(protocol.KindCmd, raw); err != nil {
		return protocol.CmdBody{}, fmt.Errorf("恢复命令契约校验: %w", err)
	}
	return body, nil
}

// markSent 只在命令仍属于 expectedSession 时记录发送成功。SendEnvelope
// 与 Hub 接管已线性化，但 socket 写返回后到 SQLite 记账前仍可发生新
// session 收编。迟到的旧发送记账不得覆盖收编已写入的新 session。
func (d *Dispatcher) markSent(msgID, expectedSession, sentSession string) {
	now := time.Now()
	_ = d.st.MutateCmd(msgID, func(r *store.CmdRecord) error {
		if r.Status.Terminal() {
			return errAlreadyTerminal
		}
		if r.Session != expectedSession {
			return errSessionAdvanced
		}
		if r.Status == store.CmdQueued {
			r.Status = store.CmdSent
		}
		r.NotBeforeAt = nil
		r.Session = sentSession
		r.Attempt++
		r.SentAt = &now
		return nil
	})
}

func (d *Dispatcher) logicalChanged(logicalID string) <-chan struct{} {
	d.waitMu.Lock()
	defer d.waitMu.Unlock()
	wait := d.waits[logicalID]
	if wait == nil {
		// notify 可能早于 WaitLogical 到达；无订阅者时不保留内存广播沿，
		// 等待者会先查持久化账本，因而不会丢失已发生的状态变化。
		return closedSignal()
	}
	return wait.changed
}

func (d *Dispatcher) subscribeLogical(logicalID string) {
	d.waitMu.Lock()
	defer d.waitMu.Unlock()
	wait := d.waits[logicalID]
	if wait == nil {
		wait = &logicalWait{changed: make(chan struct{})}
		d.waits[logicalID] = wait
	}
	wait.refs++
}

func (d *Dispatcher) unsubscribeLogical(logicalID string) {
	d.waitMu.Lock()
	defer d.waitMu.Unlock()
	wait := d.waits[logicalID]
	if wait == nil {
		return
	}
	wait.refs--
	if wait.refs <= 0 {
		delete(d.waits, logicalID)
	}
}

func (d *Dispatcher) notifyLogical(logicalID string) {
	if logicalID == "" {
		return
	}
	d.waitMu.Lock()
	if wait := d.waits[logicalID]; wait != nil {
		close(wait.changed)
		wait.changed = make(chan struct{})
	}
	d.waitMu.Unlock()
}

func closedSignal() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (d *Dispatcher) notifyByMsgID(msgID string) {
	cmd, err := d.st.CmdByMsgID(msgID)
	if err == nil && cmd != nil {
		d.notifyLogical(cmd.LogicalDispatchID)
	}
}
