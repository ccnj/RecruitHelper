package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

// OnReconnect 保留给 M1 测试和不支持 witness/1 的旧手。真实 SX 会话
// 由 Hub 显式调用 OnReconnectWitness，绝不落入 bootId 一刀切重投轨。
func (d *Dispatcher) OnReconnect(handID, newBootID string) {
	release := d.lockHandGate(handID)
	defer release()
	if witness, ok := d.handWitness(handID); ok {
		d.onReconnectWitness(handID, newBootID, witness.StoreID, witness.OutboxPending, witness.JournalOpen)
		return
	}
	d.reconnectLegacy(handID, newBootID)
}

// OnReconnectWitness 激活首个真实 SX 的四阶段恢复。Hub 只在 welcome
// 已发出、新 session 已 ready 后调用：先持久 pendingReconcile；若
// hello 证明 outbox 已空则发 query，否则等后续 ping=0。本方法从不盲重投 SX。
func (d *Dispatcher) OnReconnectWitness(
	handID, newBootID, witnessStoreID string,
	outboxPending, journalOpen int,
) {
	release := d.lockHandGate(handID)
	defer release()
	d.onReconnectWitness(handID, newBootID, witnessStoreID, outboxPending, journalOpen)
}

// OnReconnectWitnessUnderGate 仅供已经 BeginHandTakeover 的 Hub 调用。
func (d *Dispatcher) OnReconnectWitnessUnderGate(
	handID, newBootID, witnessStoreID string,
	outboxPending, journalOpen int,
) {
	d.onReconnectWitness(handID, newBootID, witnessStoreID, outboxPending, journalOpen)
}

func (d *Dispatcher) onReconnectWitness(
	handID, newBootID, witnessStoreID string,
	outboxPending, journalOpen int,
) {
	session, bootID, online := d.sender.HandSession(handID)
	if !online || bootID != newBootID {
		return
	}
	recovering, err := d.st.BeginEffectReconcileForHand(handID, session, newBootID, time.Now())
	if err != nil {
		slog.Error("SX 重连收编失败", "handId", handID, "err", err)
		return
	}
	if len(recovering) != 0 {
		d.st.Audit("effect_reconcile_started", handID, "",
			fmt.Sprintf("commands=%d outboxPending=%d journalOpen=%d witnessStoreId=%s",
				len(recovering), outboxPending, journalOpen, witnessStoreID))
	}

	// M1/S 命令仍遵循原 bootId 收编规则，但必须跳过已进入
	// pendingReconcile/verifying 的真实 SX。
	d.reconnectLegacy(handID, newBootID)
	if outboxPending == 0 {
		d.sendRecoveryQueries(handID, session, newBootID, witnessStoreID)
	}
}

// OnWitnessHeartbeat 把“先补投 outbox”变成可观测屏障。storeId 变化
// 不在这里偷偷继续；report 归类时会把新 store 的 unknown 降为验证读。
func (d *Dispatcher) OnWitnessHeartbeat(
	handID, session, bootID, witnessStoreID string,
	outboxPending, _ int,
) {
	if outboxPending != 0 || witnessStoreID == "" {
		return
	}
	currentSession, currentBoot, online := d.sender.HandSession(handID)
	if !online || currentSession != session || currentBoot != bootID {
		return
	}
	d.sendRecoveryQueries(handID, session, bootID, witnessStoreID)
}

func (d *Dispatcher) reconnectLegacy(handID, newBootID string) {
	cmds, err := d.st.NonTerminalCmdsForHand(handID)
	if err != nil || len(cmds) == 0 {
		return
	}
	session, _, online := d.sender.HandSession(handID)
	if !online {
		return
	}
	// 先冻结换代的 M1 debug effectful，使第二趟 intrusive 重派的域检查
	// 不依赖枚举顺序。真实 SX(IntentID!="")已由四阶段轨收编。
	for _, cmd := range cmds {
		if cmd.IntentID != "" {
			continue
		}
		if cmd.BootIDAtDispatch != newBootID && cmd.Class == string(protocol.ClassEffectful) {
			d.markSuspect(cmd, "bootId 换代且无 result")
		}
	}
	for _, cmd := range cmds {
		if cmd.IntentID != "" {
			continue
		}
		switch {
		case cmd.BootIDAtDispatch == newBootID:
			if allowsAutomaticRedispatch(cmd.Name) {
				d.resendCmd(cmd, session)
			} else {
				d.terminalizeVoid(cmd, "重连时该维护原语禁止普通自动重派")
			}
		case cmd.Class == string(protocol.ClassEffectful):
			// 第一趟已冻结
		default:
			d.voidAndRedispatch(cmd, "bootId 换代", 0)
		}
	}
}

func (d *Dispatcher) sendRecoveryQueries(handID, session, bootID, witnessStoreID string) {
	d.sendRecoveryQueriesAt(handID, session, bootID, witnessStoreID, time.Now())
}

func (d *Dispatcher) sendRecoveryQueriesAt(handID, session, bootID, witnessStoreID string, now time.Time) {
	commands, err := d.st.EffectRecoveryCommandsForHand(handID)
	if err != nil {
		slog.Error("读取 SX 对账队列失败", "handId", handID, "err", err)
		return
	}
	for i := range commands {
		cmd := commands[i]
		if cmd.Status != store.CmdPendingReconcile || cmd.QueryMsgID != "" || cmd.RecoveryAuthorized {
			continue
		}
		if cmd.ReconcileNextAt != nil && now.Before(*cmd.ReconcileNextAt) {
			continue
		}
		queryID := ids.NewMsgID()
		if err := d.st.RecordRecoveryQuery(cmd.MsgID, handID, session, bootID, queryID, now); err != nil {
			d.st.Audit("reconcile_query_record_failed", handID, cmd.MsgID, err.Error())
			continue
		}
		env := d.envelope(protocol.KindQuery, queryID, &session, protocol.QueryBody{Ref: cmd.MsgID})
		if err := d.sender.SendEnvelope(handID, env); err != nil {
			_ = d.st.ClearRecoveryQuery(cmd.MsgID, queryID)
			d.st.Audit("reconcile_query_send_failed", handID, cmd.MsgID, err.Error())
			return
		}
		d.st.Audit("reconcile_query_sent", handID, cmd.MsgID,
			fmt.Sprintf("query=%s witnessStoreId=%s", queryID, witnessStoreID))
	}
}

// OnReport 先依当前对账 session/storeId 围栏验证来源，再将非 done
// 四态交给 Store 单写事务归类。done 内嵌 result 复用普通 result 的
// processed-msg + 命令 + intent + 消息账本原子路径。
func (d *Dispatcher) OnReport(
	handID, reportMsgID, session, bootID string,
	report protocol.ReportBody,
) {
	cmd, err := d.st.CmdByMsgID(report.Ref)
	if err != nil || cmd == nil {
		d.st.Audit("orphan_report", handID, report.Ref, "report ref 无对应命令")
		return
	}
	if cmd.HandID != handID || cmd.IntentID == "" ||
		cmd.ReconcileSession != session || cmd.ReconcileBootID != bootID || cmd.QueryMsgID == "" {
		d.st.Audit("report_source_mismatch", handID, report.Ref, "report 未通过命令/会话/对账栅栏")
		return
	}
	if report.State != protocol.ReportStateDone && cmd.Status != store.CmdPendingReconcile {
		d.st.Audit("report_source_mismatch", handID, report.Ref, "非 done report 只允许推进 pendingReconcile")
		return
	}
	if report.WitnessStoreId == "" {
		d.moveReportToVerification(*cmd, "report witness store missing")
		return
	}
	reportRaw, _ := json.Marshal(report)
	if report.State == protocol.ReportStateDone {
		if report.Result == nil || report.Result.Ref != cmd.MsgID || report.Ref != cmd.MsgID ||
			report.Journal == nil || report.Journal.Ref != cmd.MsgID ||
			report.Journal.IdemKey != cmd.IdemKey || report.WitnessStoreId != cmd.WitnessStoreIDAtDispatch {
			d.st.Audit("report_done_witness_mismatch", handID, cmd.MsgID,
				"done journal/idemKey/witnessStoreId 与脑账本不一致")
			d.moveReportToVerification(*cmd, "done report witness mismatch")
			return
		}
		resultRaw, _ := protocol.Encode(report.Result)
		meta := protocol.Primitives[cmd.Name]
		if err := protocol.ValidatePrimitiveResult(cmd.Name, meta.Ver, resultRaw); err != nil {
			d.st.Audit("report_done_result_invalid", handID, cmd.MsgID, err.Error())
			d.moveReportToVerification(*cmd, "done report primitive result invalid")
			return
		}
		oc, _, persistErr := d.applyResultMessageKind(handID, reportMsgID, string(protocol.KindReport), *report.Result)
		if persistErr != nil {
			slog.Error("done report 原子入账失败", "handId", handID, "ref", cmd.MsgID, "err", persistErr)
			return
		}
		d.clearLease(cmd.MsgID)
		d.notifyByMsgID(cmd.MsgID)
		d.st.Audit("reconcile_report_done", handID, cmd.MsgID, fmt.Sprintf("outcome=%d", oc))
		if oc == ocEffSuspect {
			d.kickVerification(cmd.MsgID)
		}
		d.releaseSafeRecoveries(handID)
		return
	}
	if report.State == protocol.ReportStateAttempting &&
		(report.Journal == nil || report.Journal.Ref != cmd.MsgID || report.Journal.IdemKey != cmd.IdemKey) {
		d.st.Audit("report_attempting_witness_mismatch", handID, cmd.MsgID,
			"attempting journal/ref/idemKey 与脑账本不一致")
		d.moveReportToVerification(*cmd, "attempting report witness mismatch")
		return
	}

	result, err := d.st.ApplyRecoveryReport(store.RecoveryReportObservation{
		Ref: report.Ref, ReportMsgID: reportMsgID, HandID: handID, Session: session, BootID: bootID,
		State: string(report.State), WitnessStoreID: report.WitnessStoreId, Body: string(reportRaw), At: time.Now(),
		NextQueryAt: time.Now().Add(recoveryQueryTimeout), MaxQueries: recoveryQueryMax,
	})
	if err != nil {
		d.st.Audit("reconcile_report_rejected", handID, report.Ref, err.Error())
		return
	}
	if result.AlreadyProcessed {
		return
	}
	d.st.Audit("reconcile_report", handID, report.Ref, string(report.State))
	if result.NeedsVerification {
		d.kickVerification(report.Ref)
	}
	if result.Authorized || result.Executing {
		d.releaseSafeRecoveries(handID)
	}
}

func (d *Dispatcher) moveReportToVerification(cmd store.CmdRecord, reason string) {
	if err := d.st.MoveEffectToVerification(cmd.MsgID, reason, time.Now()); err != nil {
		d.st.Audit("effect_verification_transition_failed", cmd.HandID, cmd.MsgID, err.Error())
		return
	}
	d.kickVerification(cmd.MsgID)
}

func (d *Dispatcher) releaseSafeRecoveries(handID string) {
	session, bootID, online := d.sender.HandSession(handID)
	witness, witnessOK := d.handWitness(handID)
	if !online || !witnessOK || witness.StoreID == "" {
		return
	}
	commands, verify, err := d.st.ReleaseSafeRecoveriesForHand(handID, session, bootID, witness.StoreID, time.Now())
	if err != nil {
		d.st.Audit("effect_recovery_release_failed", handID, "", err.Error())
		return
	}
	// ReleaseSafeRecoveriesForHand 已把本手所有获准项原子放回 queued。
	// 在发送任意一条之前，先按当前 active 手复核整组 capability；只要
	// 一条缺失，就把整组改走验证，避免先重投一部分再发现同代手已降档。
	for i := range commands {
		meta, ok := protocol.Primitives[commands[i].Name]
		if !ok || d.requireNegotiation(handID, commands[i].Name, meta) != nil {
			reason := "安全恢复时当前手缺少原命令 capability，禁止重投并改走验证"
			for j := range commands {
				if moveErr := d.st.MoveEffectToVerification(commands[j].MsgID, reason, time.Now()); moveErr != nil {
					d.st.Audit("effect_safe_redispatch_capability_transition_failed",
						handID, commands[j].MsgID, moveErr.Error())
					continue
				}
				d.st.Audit("effect_safe_redispatch_capability_blocked",
					handID, commands[j].MsgID, reason)
				d.kickVerification(commands[j].MsgID)
			}
			return
		}
	}
	for i := range commands {
		if d.resendCmdAt(commands[i], session, time.Now()) {
			d.st.Audit("effect_safe_redispatch", handID, commands[i].MsgID,
				"report=unknown 且 witness store 连续，同 msgId 唯一安全恢复")
		}
	}
	for i := range verify {
		d.st.Audit("effect_safe_redispatch_blocked", handID, verify[i].MsgID,
			"安全恢复次数/原意图期限已到，整手屏障保留并改走验证")
		d.kickVerification(verify[i].MsgID)
	}
}

// resendCmd: 同 msgId 重发。更新会话/attempt/SentAt，queued→sent，其余不动。
func (d *Dispatcher) resendCmd(cmd store.CmdRecord, session string) {
	d.resendCmdAt(cmd, session, time.Now())
}

func (d *Dispatcher) resendCmdAt(cmd store.CmdRecord, session string, now time.Time) bool {
	if cmd.NotBeforeAt != nil && now.Before(*cmd.NotBeforeAt) {
		return false
	}
	// 真实 SX 的首次派发已经过 capability 闸；重连后的同 msgId 重投
	// 仍须复核当前 active 手，覆盖 release 预检与 socket 写之间的接管竞态。
	if cmd.IntentID != "" {
		meta, ok := protocol.Primitives[cmd.Name]
		if !ok {
			d.moveRecoveryCapabilityMismatchToVerification(cmd, "原命令 metadata 已不存在")
			return false
		}
		if err := d.requireNegotiation(cmd.HandID, cmd.Name, meta); err != nil {
			d.moveRecoveryCapabilityMismatchToVerification(cmd, err.Error())
			return false
		}
	}
	body, err := d.commandBody(cmd)
	if err != nil {
		d.st.Audit("resend_invalid", cmd.HandID, cmd.MsgID, err.Error())
		return false
	}
	env := d.envelope(protocol.KindCmd, cmd.MsgID, &session, body)
	env.Attempt = cmd.Attempt + 1
	if err := d.sender.SendEnvelope(cmd.HandID, env); err != nil {
		return false
	}
	d.markSent(cmd.MsgID, cmd.Session, session)
	d.st.Audit("resend", cmd.HandID, cmd.MsgID, "同 msgId 重发")
	return true
}

func (d *Dispatcher) moveRecoveryCapabilityMismatchToVerification(cmd store.CmdRecord, detail string) {
	reason := "安全恢复写前 capability 复核失败，禁止重投并改走验证"
	if err := d.st.MoveEffectToVerification(cmd.MsgID, reason, time.Now()); err != nil {
		d.st.Audit("effect_safe_redispatch_capability_transition_failed", cmd.HandID, cmd.MsgID, err.Error())
		return
	}
	d.st.Audit("effect_safe_redispatch_capability_blocked", cmd.HandID, cmd.MsgID, reason+": "+detail)
	d.kickVerification(cmd.MsgID)
}

// Recover 在任何 WS 监听前运行。真实 SX 进 pendingReconcile/verifying，
// 等手回来先补 outbox 再 query；M1 debug effectful 保持旧 suspect 演练语义。
func (d *Dispatcher) Recover() {
	realEffects, realErr := d.st.PrepareEffectRecoveryAfterBrainRestart(time.Now())
	if realErr != nil {
		slog.Error("脑重启 SX 待对账收编失败", "err", realErr)
		return
	}
	cmds, err := d.st.NonTerminalCmds()
	if err != nil {
		slog.Error("脑重启扫描失败", "err", err)
		return
	}
	for _, cmd := range cmds {
		if cmd.IntentID != "" {
			continue
		}
		if cmd.Class == string(protocol.ClassEffectful) {
			d.markSuspect(cmd, "脑重启扫描:在途 effectful")
			continue
		}
		d.terminalizeVoid(cmd, "脑重启扫描:在途 readonly/intrusive")
	}
	if len(cmds) > 0 || len(realEffects) > 0 {
		slog.Info("脑重启扫描完成", "在途命令数", len(cmds), "待对账SX", len(realEffects))
	}
}

// Verdict: 人工对 suspect 命令二选一裁决。真实 SX 额外要求已完成
// outbox/query/验证（只有 CmdSuspect 才能到这里）；手仍在线时拒绝人裁，
// 离线时必须持续超过 manualDelay。
func (d *Dispatcher) Verdict(msgID string, verdict store.CmdStatus) error {
	if verdict != store.CmdResolvedOk && verdict != store.CmdResolvedFailed {
		return errors.New("非法裁决,只能 resolvedOk / resolvedFailed")
	}
	cmd, err := d.st.CmdByMsgID(msgID)
	if err != nil {
		return err
	}
	if cmd == nil || cmd.Status != store.CmdSuspect {
		return ErrNotSuspect
	}
	_, bootID, online := d.sender.HandSession(cmd.HandID)
	if cmd.IntentID != "" {
		// 验证耗尽会持久 ReviewReady，证明 outbox/query/验证已收束；
		// 此时在线也可人裁。未收束的真实 SX 在线仍严禁早裁。
		if cmd.ReviewReady && time.Now().UnixMilli() < cmd.ReviewAfterMs {
			return ErrVerdictNotReady
		}
		if !cmd.ReviewReady && online {
			return ErrVerdictNotReady
		}
	} else if online && bootID == cmd.BootIDAtDispatch {
		return ErrVerdictNotReady
	}
	if !cmd.ReviewReady && !online && d.sender.HandOfflineMs(cmd.HandID) < d.manualDelayMs {
		return ErrVerdictNotReady
	}
	resolve := store.ResolveSuspectVerdictRequest{Ref: msgID, Verdict: verdict, At: time.Now()}
	if cmd.IntentID != "" {
		intent, lookupErr := d.st.EffectIntentByID(cmd.IntentID)
		if lookupErr != nil {
			return lookupErr
		}
		if intent == nil {
			return store.ErrEffectIntentNotFound
		}
		switch intent.Primitive {
		case protocol.PrimChatSendMessage:
			var args protocol.ChatSendMessageArgs
			if err := json.Unmarshal([]byte(cmd.Args), &args); err != nil {
				return err
			}
			resolve.ConversationKey = store.ConversationKey{
				Platform: intent.Platform, AccountRef: intent.AccountRef, ConversationRef: intent.TargetRef,
			}
			resolve.Text = args.Text
			resolve.ContentHash = intent.SendFingerprint
		case protocol.PrimChatSendGreeting:
			if verdict == store.CmdResolvedOk {
				// 招呼 intent.TargetRef 是 ProfileID，绝不能把真人的布尔
				// 裁决当作 conversationRef 补写。resolvedOk 在这里仅授权
				// 一次正式 readGreetingOutcome；正证仍由 ResolveGreetingVerified
				// 原子收束，阴性恢复 suspect，且不增加自动验证预算。
				if d.verifier == nil || !cmd.ReviewReady {
					return ErrVerdictNotReady
				}
				err := d.st.BeginGreetingManualVerification(
					msgID, manualGreetingVerdictVerificationReason, time.Now(),
				)
				if err != nil {
					if errors.Is(err, store.ErrRecoveryStateConflict) {
						return ErrNotSuspect
					}
					return err
				}
				d.st.Audit("suspect_verdict_verification", cmd.HandID, msgID,
					"真人 resolvedOk 触发一次 chat.readGreetingOutcome 正证读取")
				d.notifyByMsgID(msgID)
				d.kickVerification(msgID)
				return nil
			}
			err := d.st.ResolveGreetingSuspectFailed(msgID, time.Now())
			if err != nil {
				if errors.Is(err, store.ErrRecoveryStateConflict) {
					return ErrNotSuspect
				}
				return err
			}
			d.st.Audit("suspect_verdict", cmd.HandID, msgID, string(verdict))
			d.clearLease(msgID)
			d.notifyByMsgID(msgID)
			slog.Info("suspect 招呼人工裁决", "handId", cmd.HandID, "msgId", msgID, "verdict", verdict)
			return nil
		default:
			return errors.New("真实副作用原语没有人工裁决实现")
		}
	}
	err = d.st.ResolveSuspectVerdict(resolve)
	if err != nil {
		if errors.Is(err, store.ErrRecoveryStateConflict) {
			return ErrNotSuspect
		}
		return err
	}
	d.st.Audit("suspect_verdict", cmd.HandID, msgID, string(verdict))
	d.clearLease(msgID)
	d.notifyByMsgID(msgID)
	slog.Info("suspect 人工裁决", "handId", cmd.HandID, "msgId", msgID, "verdict", verdict)
	return nil
}

// SuspectReviewState 与 Verdict 共用同一时间闸，供管理面展示，不构成
// 裁决授权。真实 SX 在手仍在线时一律不允许早裁。
func (d *Dispatcher) SuspectReviewState(cmd store.CmdRecord) (bool, *int64) {
	if cmd.Status != store.CmdSuspect {
		return false, nil
	}
	_, bootID, online := d.sender.HandSession(cmd.HandID)
	if cmd.IntentID != "" && cmd.ReviewReady {
		if now := time.Now().UnixMilli(); now >= cmd.ReviewAfterMs {
			return true, nil
		}
		after := cmd.ReviewAfterMs
		return false, &after
	}
	if online && (cmd.IntentID != "" || bootID == cmd.BootIDAtDispatch) {
		return false, nil
	}
	offlineMs := d.sender.HandOfflineMs(cmd.HandID)
	if !cmd.ReviewReady && !online && offlineMs < d.manualDelayMs {
		after := time.Now().UnixMilli() + (d.manualDelayMs - offlineMs)
		return false, &after
	}
	return true, nil
}

// noteAckTimeout / resetWedged:HAND_WEDGED 计数。
func (d *Dispatcher) noteAckTimeout(handID string) {
	d.wmu.Lock()
	d.wedged[handID]++
	n := d.wedged[handID]
	d.wmu.Unlock()
	if n >= 3 {
		d.st.Audit("hand_wedged", handID, "", fmt.Sprintf("连续 %d 次 ackTimeout,疑似手残废", n))
		slog.Warn("HAND_WEDGED:连续 ackTimeout,告警转人工", "handId", handID, "n", n)
	}
}

func (d *Dispatcher) resetWedged(handID string) {
	d.wmu.Lock()
	if d.wedged[handID] != 0 {
		d.wedged[handID] = 0
	}
	d.wmu.Unlock()
}
