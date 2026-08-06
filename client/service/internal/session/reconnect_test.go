package session

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/coder/websocket"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

func readUntilKind(t *testing.T, c *websocket.Conn, kind protocol.Kind) *protocol.Envelope {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		e := readEnv(t, c)
		if e.Kind == kind {
			return e
		}
	}
	t.Fatalf("未在期限内读到 %s 帧", kind)
	return nil
}

func waitOffline(t *testing.T, h *harness, handID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s, ok := h.hub.Registry().Get(handID); ok && !s.Online {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func connectHandNegotiated(t *testing.T, h *harness, handID, bootID string, caps, features []string) *websocket.Conn {
	t.Helper()
	c := dial(t, h.wsURL, testOrigin)
	raw, err := protocol.Encode(protocol.HelloBody{
		HandID: handID, BootID: bootID,
		ProtoSupported: []int{protocol.ProtoVersion},
		App:            protocol.AppInfo{ExtVersion: "0.1.0", Browser: "test"},
		Caps:           caps, Features: features, ContractHash: protocol.ContractHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	sendHelloBody(t, c, raw)
	env := readEnv(t, c)
	if env.Kind != protocol.KindWelcome {
		t.Fatalf("hello 应收到 welcome,实际 %s", env.Kind)
	}
	return c
}

func waitAudit(t *testing.T, h *harness, category, refMsgID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := h.st.AuditEntries(200)
		for _, entry := range entries {
			if entry.Category == category && entry.RefMsgID == refMsgID {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("未等到审计 category=%s ref=%s", category, refMsgID)
}

// 真 WS 重连收编:换 bootId 重连 → 在途 effectful 转 suspect(经 enterSession→OnReconnect 钩子)。
func TestReconnectCollectSuspectViaWS(t *testing.T) {
	h := newHarness(t)
	handID := "hand-reconnect-suspect"

	// 以 bootId b-1 连接,派 silent effectful,手 ack 但不回 result
	c1 := connectHand(t, h, handID, "b-1")
	msgID, err := h.disp.Dispatch(handID, protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	cmdEnv := readUntilKind(t, c1, protocol.KindCmd)
	writeMsg(c1, protocol.KindAck, cmdEnv.Session, protocol.AckBody{Ref: cmdEnv.MsgID, Status: protocol.AckStatusAccepted})
	waitCmdStatus(t, h, msgID, store.CmdAccepted)
	_ = c1.Close(websocket.StatusNormalClosure, "")
	waitOffline(t, h, handID)

	// 以新 bootId b-2 重连 → OnReconnect 换代收编 → effectful suspect
	c2 := connectHand(t, h, handID, "b-2")
	defer c2.Close(websocket.StatusNormalClosure, "")
	waitCmdStatus(t, h, msgID, store.CmdSuspect)
}

// 真 WS 重连同代:bootId 未变重连 → 在途命令同 msgId 重发,手收到重发帧。
func TestReconnectSameBootResendViaWS(t *testing.T) {
	h := newHarness(t)
	handID := "hand-reconnect-same-boot"

	c1 := connectHand(t, h, handID, "b-1")
	msgID, _ := h.disp.Dispatch(handID, protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	cmdEnv := readUntilKind(t, c1, protocol.KindCmd)
	writeMsg(c1, protocol.KindAck, cmdEnv.Session, protocol.AckBody{Ref: cmdEnv.MsgID, Status: protocol.AckStatusAccepted})
	waitCmdStatus(t, h, msgID, store.CmdAccepted)
	_ = c1.Close(websocket.StatusNormalClosure, "")
	waitOffline(t, h, handID)

	// 同 bootId b-1 重连 → OnReconnect 同代重发同 msgId
	c2 := connectHand(t, h, handID, "b-1")
	defer c2.Close(websocket.StatusNormalClosure, "")
	resent := readUntilKind(t, c2, protocol.KindCmd)
	if resent.MsgID != msgID {
		t.Fatalf("同代重连应重发同 msgId=%s,得到 %s", msgID, resent.MsgID)
	}
	// 未终局(仍在途)
	if rec, _ := h.st.CmdByMsgID(msgID); rec.Status.Terminal() {
		t.Fatalf("同代重发不应终局化,得到 %s", rec.Status)
	}
}

// 真 WS 固定 accepted QueueItem 跨同 boot 会话继续执行的协议时序：新 active
// socket 可以上交创建时旧 session 的 progress/result；旧物理 socket 和新 socket
// 上的旧 session ack/event 仍被硬拒绝。progress 必须真实续租，result 必须经 WAL
// 去重落终局并以当前 session 回 ack。
func TestSameBootTakeoverAcceptsHistoricalProgressAndResultOnlyFromActiveSocket(t *testing.T) {
	h := newHarness(t)
	const (
		handID = "hand-historical-result"
		bootID = "boot-continuous"
	)
	caps := []string{protocol.PrimNavEnsureSurface + "@1"}
	features := []string{
		string(protocol.FeatureLease1),
		string(protocol.FeatureProgress1),
		string(protocol.FeatureCancel1),
	}
	oldClient := connectHandNegotiated(t, h, handID, bootID, caps, features)
	defer oldClient.Close(websocket.StatusNormalClosure, "")

	msgID, err := h.disp.DispatchStructured(dispatch.DispatchRequest{
		HandID: handID, Name: protocol.PrimNavEnsureSurface,
		Args: json.RawMessage(`{"surface":"im"}`),
		Context: &protocol.CmdContext{
			Platform: "zhilian", AccountRef: "acct-historical",
			ExpectedPrincipalFingerprint: "opaque-fp",
		},
	})
	if err != nil {
		t.Fatalf("派发租约命令: %v", err)
	}
	cmd := readUntilKind(t, oldClient, protocol.KindCmd)
	if cmd.Session == nil {
		t.Fatal("旧命令缺 session")
	}
	oldSession := *cmd.Session

	// 只缩短本测试这次 startLease 的时长。ACK 后紧跟 ping；收到 pong 证明
	// 服务端已按帧序完成 OnAck/startLease，随后立即恢复全局 primitive 元数据。
	originalMeta := protocol.Primitives[protocol.PrimNavEnsureSurface]
	shortMeta := originalMeta
	shortMeta.LeaseMs = 5000
	protocol.Primitives[protocol.PrimNavEnsureSurface] = shortMeta
	metaRestored := false
	defer func() {
		if !metaRestored {
			protocol.Primitives[protocol.PrimNavEnsureSurface] = originalMeta
		}
	}()
	leaseStartedAt := time.Now()
	if err := writeMsgWithID(oldClient, "ack-old-accepted", protocol.KindAck, cmd.Session,
		protocol.AckBody{Ref: msgID, Status: protocol.AckStatusAccepted}); err != nil {
		t.Fatal(err)
	}
	sendPing(t, oldClient, cmd.Session)
	readUntilKind(t, oldClient, protocol.KindPong)
	protocol.Primitives[protocol.PrimNavEnsureSurface] = originalMeta
	metaRestored = true
	waitCmdStatus(t, h, msgID, store.CmdAccepted)

	h.hub.mu.Lock()
	oldServer := h.hub.active[handID]
	h.hub.mu.Unlock()
	if oldServer == nil || oldServer.session != oldSession {
		t.Fatal("未取得旧物理 Conn")
	}

	faultCtx, stopFaultLoop := context.WithCancel(context.Background())
	defer stopFaultLoop()
	go h.disp.RunFaultLoop(faultCtx)

	newClient := connectHandNegotiated(t, h, handID, bootID, caps, features)
	defer newClient.Close(websocket.StatusNormalClosure, "")
	resent := readUntilKind(t, newClient, protocol.KindCmd)
	if resent.MsgID != msgID || resent.Session == nil || *resent.Session == oldSession {
		t.Fatalf("同 boot 收编未以新 session 重发原 msgId: %+v", resent)
	}
	newSession := *resent.Session

	// 例外只给 progress/result。当前 active socket 上的旧 session ack/event 仍拒绝。
	if err := writeMsgWithID(newClient, "ack-with-old-session", protocol.KindAck, &oldSession,
		protocol.AckBody{Ref: msgID, Status: protocol.AckStatusDuplicate}); err != nil {
		t.Fatal(err)
	}
	waitAudit(t, h, "stale_session_frame", "ack-with-old-session")
	if err := writeMsgWithID(newClient, "ping-with-old-session", protocol.KindPing, &oldSession,
		protocol.PingBody{QueueDepth: 0}); err != nil {
		t.Fatal(err)
	}
	waitAudit(t, h, "stale_session_frame", "ping-with-old-session")
	eventData, _ := protocol.Encode(protocol.PageNavigatedEventData{At: time.Now().UnixMilli(), PageKind: protocol.PageKindIm})
	if err := writeMsgWithID(newClient, "event-with-old-session", protocol.KindEvent, &oldSession,
		protocol.EventBody{
			Name:       protocol.EventPageNavigated,
			Context:    &protocol.EventContext{Platform: "zhilian", AccountRef: "acct-historical"},
			ObservedAt: time.Now().UnixMilli(), Data: eventData,
		}); err != nil {
		t.Fatal(err)
	}
	waitAudit(t, h, "stale_session_frame", "event-with-old-session")

	// 被顶替旧物理 Conn 即使伪造“允许历史 session”的 result，也必须先败在
	// active Conn 栅栏，不能推进账本。
	resultData, _ := json.Marshal(protocol.NavEnsureSurfaceData{
		CreatedTab: false, LoginState: protocol.LoginStateIn, Ready: true,
	})
	forgedBody, _ := protocol.Encode(protocol.ResultBody{
		Ref: msgID, Status: protocol.ResultStatusOk, Data: resultData, ExecMs: 1,
	})
	forgedEnv := protocol.Envelope{
		Proto: protocol.ProtoVersion, Kind: protocol.KindResult, MsgID: "result-from-old-socket",
		Session: &oldSession, Ts: time.Now().UnixMilli(), Attempt: 1, Body: forgedBody,
	}
	forgedFrame, _ := json.Marshal(forgedEnv)
	oldServer.handleSessionFrame(context.Background(), forgedFrame)
	waitAudit(t, h, "stale_connection_frame", "result-from-old-socket")
	if record, _ := h.st.CmdByMsgID(msgID); record.Status != store.CmdAccepted {
		t.Fatalf("旧 socket 伪造 result 推进了账本: %s", record.Status)
	}

	// 新 session 的 duplicate ack 合法；以当前 session ping 作 FIFO barrier。
	if err := writeMsgWithID(newClient, "ack-new-duplicate", protocol.KindAck, resent.Session,
		protocol.AckBody{Ref: msgID, Status: protocol.AckStatusDuplicate}); err != nil {
		t.Fatal(err)
	}
	sendPing(t, newClient, resent.Session)
	readUntilKind(t, newClient, protocol.KindPong)

	// 让 progress 明显晚于首次租约建立，再以旧 session 从当前 socket 上报。
	if delay := time.Until(leaseStartedAt.Add(2500 * time.Millisecond)); delay > 0 {
		time.Sleep(delay)
	}
	beforeProgress, _ := h.hub.Registry().Get(handID)
	if err := writeMsgWithID(newClient, "progress-historical-session", protocol.KindProgress, &oldSession,
		protocol.ProgressBody{Ref: msgID, Stage: "still-running", Pct: 50}); err != nil {
		t.Fatal(err)
	}
	progressDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(progressDeadline) {
		state, _ := h.hub.Registry().Get(handID)
		if state.LastHbAt.After(beforeProgress.LastHbAt) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	afterProgress, _ := h.hub.Registry().Get(handID)
	if !afterProgress.LastHbAt.After(beforeProgress.LastHbAt) {
		t.Fatal("旧 session progress 未进入当前 active 的合法处理路径")
	}
	// 当前 session ping 是进度处理完成的 FIFO barrier，确保 OnProgress 已续租。
	sendPing(t, newClient, &newSession)
	readUntilKind(t, newClient, protocol.KindPong)

	// 首次 5s 租约此时必已越界且 fault loop 至少扫过一次；若旧-session
	// progress 没有续租，会出现 cancel_sent。续租后有效期至少到约 7.5s。
	if delay := time.Until(leaseStartedAt.Add(6200 * time.Millisecond)); delay > 0 {
		time.Sleep(delay)
	}
	entries, _ := h.st.AuditEntries(300)
	for _, entry := range entries {
		if entry.Category == "cancel_sent" && entry.RefMsgID == msgID {
			t.Fatal("旧 session progress 未续租，错误触发 lease gap cancel")
		}
	}

	resultMsgID := "result-historical-session"
	result := protocol.ResultBody{
		Ref: msgID, Status: protocol.ResultStatusOk, Data: resultData, ExecMs: 10,
	}
	if err := writeMsgWithID(newClient, resultMsgID, protocol.KindResult, &oldSession, result); err != nil {
		t.Fatal(err)
	}
	waitCmdStatus(t, h, msgID, store.CmdOk)
	ack := readUntilKind(t, newClient, protocol.KindAck)
	var ackBody protocol.AckBody
	if err := json.Unmarshal(ack.Body, &ackBody); err != nil || ackBody.Ref != resultMsgID ||
		ackBody.Status != protocol.AckStatusAccepted || ack.Session == nil || *ack.Session != newSession {
		t.Fatalf("历史 session result 未以当前 session 正确回 ack: env=%+v body=%+v err=%v", ack, ackBody, err)
	}

	// 相同 result msgId 重投仍走 WAL 去重并重新 ack，不重复推进终局。
	if err := writeMsgWithID(newClient, resultMsgID, protocol.KindResult, &oldSession, result); err != nil {
		t.Fatal(err)
	}
	dupAck := readUntilKind(t, newClient, protocol.KindAck)
	var dupAckBody protocol.AckBody
	_ = json.Unmarshal(dupAck.Body, &dupAckBody)
	if dupAckBody.Ref != resultMsgID || dupAckBody.Status != protocol.AckStatusDuplicate {
		t.Fatalf("重复 result 未重新 ack: %+v", dupAckBody)
	}
	if already, _ := h.st.MarkProcessed(resultMsgID, string(protocol.KindResult), handID); !already {
		t.Fatal("历史 session result 未进入 processed_msgs WAL")
	}
}

func TestSameBootSupersededConnectionDoesNotMarkNewConnectionOffline(t *testing.T) {
	h := newHarness(t)
	handID := "hand-same-boot-supersede"
	c1 := connectHand(t, h, handID, "b-same")
	defer c1.Close(websocket.StatusNormalClosure, "")
	c2 := connectHand(t, h, handID, "b-same")
	defer c2.Close(websocket.StatusNormalClosure, "")

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, ok := h.hub.Registry().Get(handID)
		if ok && state.Online && len(h.hub.ActiveHandIDs()) == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	state, _ := h.hub.Registry().Get(handID)
	t.Fatalf("同 boot 旧连接结束误清新连接: %+v active=%v", state, h.hub.ActiveHandIDs())
}

// 真 WS:hub.CloseHand 关闭指定手的连接(ackTimeout 动作的落点)。
func TestCloseHandViaWS(t *testing.T) {
	h := newHarness(t)
	handID := "hand-close"
	c := connectHand(t, h, handID, "b-1")
	defer c.Close(websocket.StatusNormalClosure, "")

	sessionID, _, online := h.hub.HandSession(handID)
	if !online || !h.hub.CloseHand(handID, sessionID, "test") {
		t.Fatal("CloseHand 未命中当前会话")
	}
	// 连接应被关闭:读将失败
	waitOffline(t, h, handID)
	if s, ok := h.hub.Registry().Get(handID); ok && s.Online {
		t.Fatalf("CloseHand 后该手应离线")
	}
}

// 回归 P0：旧实现先在 h.mu 下取 active Conn、解锁后才等 writeMu。
// 新 hello 可在这个窗口完成顶替与 OnReconnect 终局化，随后旧 cmd 却仍落
// 到旧 socket。此测试用真 WS + writeMu/FrameBus barrier 固定该时序。
func TestSendAndTakeoverAreLinearized(t *testing.T) {
	h := newHarness(t)
	const handID = "hand-linearized-takeover"
	oldClient := connectHand(t, h, handID, "boot-old")
	defer oldClient.Close(websocket.StatusNormalClosure, "")

	// 先占住旧 server Conn 的 socket 写锁。SendEnvelope 发布 FrameEvent 后将
	// 阻塞在 writeMu；此时它必须仍持有 h.mu，使接管无法越过。
	h.hub.mu.Lock()
	oldServer := h.hub.active[handID]
	if oldServer == nil {
		h.hub.mu.Unlock()
		t.Fatal("前置旧连接不在 active")
	}
	oldSession := oldServer.session
	oldServer.writeMu.Lock()
	writeLocked := true
	h.hub.mu.Unlock()
	defer func() {
		if writeLocked {
			oldServer.writeMu.Unlock()
		}
	}()

	subID, frameCh, _ := h.hub.Frames().Subscribe()
	defer h.hub.Frames().Unsubscribe(subID)
	type dispatchResult struct {
		msgID string
		err   error
	}
	dispatchDone := make(chan dispatchResult, 1)
	go func() {
		msgID, err := h.disp.Dispatch(handID, protocol.PrimDebugSlowEcho,
			json.RawMessage(`{"ms":0,"outcome":"silent"}`))
		dispatchDone <- dispatchResult{msgID: msgID, err: err}
	}()

	var blockedMsgID string
	barrierDeadline := time.NewTimer(3 * time.Second)
	defer barrierDeadline.Stop()
	for blockedMsgID == "" {
		select {
		case frame := <-frameCh:
			if frame.Dir == "out" && frame.Kind == string(protocol.KindCmd) && frame.HandID == handID {
				blockedMsgID = frame.MsgID
			}
		case <-barrierDeadline.C:
			t.Fatal("未进入被 writeMu 阻塞的发送临界区")
		}
	}

	newClient := dial(t, h.wsURL, testOrigin)
	defer newClient.Close(websocket.StatusNormalClosure, "")
	sendHello(t, newClient, handID, "boot-new")
	type readResult struct {
		env *protocol.Envelope
		err error
	}
	welcomeDone := make(chan readResult, 1)
	go func() {
		_, raw, err := newClient.Read(context.Background())
		if err != nil {
			welcomeDone <- readResult{err: err}
			return
		}
		var env protocol.Envelope
		err = json.Unmarshal(raw, &env)
		welcomeDone <- readResult{env: &env, err: err}
	}()

	select {
	case got := <-welcomeDone:
		t.Fatalf("旧发送尚未落 socket 时新会话已越过接管: env=%+v err=%v", got.env, got.err)
	case <-time.After(75 * time.Millisecond):
		// 预期：activate 在 h.mu 等待旧发送完成。
	}

	oldServer.writeMu.Unlock()
	writeLocked = false

	var sent dispatchResult
	select {
	case sent = <-dispatchDone:
	case <-time.After(3 * time.Second):
		t.Fatal("放行 writeMu 后旧发送未收束")
	}
	if sent.err != nil || sent.msgID != blockedMsgID {
		t.Fatalf("在接管线性化点之前的发送应成功: %+v barrier=%s", sent, blockedMsgID)
	}
	if cmd := readUntilKind(t, oldClient, protocol.KindCmd); cmd.MsgID != sent.msgID {
		t.Fatalf("线性化前的 cmd 未落到旧 socket: got=%s want=%s", cmd.MsgID, sent.msgID)
	}

	var welcome readResult
	select {
	case welcome = <-welcomeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("放行旧发送后新会话未收 welcome")
	}
	if welcome.err != nil || welcome.env == nil || welcome.env.Kind != protocol.KindWelcome {
		t.Fatalf("新会话 welcome 非法: env=%+v err=%v", welcome.env, welcome.err)
	}
	var welcomeBody protocol.WelcomeBody
	if err := json.Unmarshal(welcome.env.Body, &welcomeBody); err != nil || welcomeBody.Session == oldSession {
		t.Fatalf("新 session 未更换: body=%+v err=%v", welcomeBody, err)
	}

	// boot 换代收编必须把线性化前刚发的 effectful 转 suspect；
	// 迟到 markSent 不得把终局状态改回 sent。
	waitCmdStatus(t, h, sent.msgID, store.CmdSuspect)

	// 接管线性化点之后，携旧 session 的任何信封必须在写 socket 前失败。
	stale := protocol.Envelope{
		Proto: protocol.ProtoVersion, Kind: protocol.KindCmd, MsgID: "cmd-stale-after-takeover",
		Session: &oldSession, Ts: time.Now().UnixMilli(), Attempt: 1, Body: json.RawMessage(`{}`),
	}
	if err := h.hub.SendEnvelope(handID, stale); !errors.Is(err, dispatch.ErrStaleSession) {
		t.Fatalf("旧 session 应在 socket 前被拒绝，实际 %v", err)
	}
}

func TestSameHandTakeoverRecoveryCannotInterleave(t *testing.T) {
	h := newHarness(t)
	releaseFirst := h.hub.lockTakeover("hand-takeover-gate")
	secondEntered := make(chan struct{})
	go func() {
		releaseSecond := h.hub.lockTakeover("hand-takeover-gate")
		close(secondEntered)
		releaseSecond()
	}()

	select {
	case <-secondEntered:
		t.Fatal("同 handId 的第二次接管越过了首次收编")
	case <-time.After(75 * time.Millisecond):
	}
	releaseFirst()
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("首次收编结束后第二次接管未放行")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		h.hub.takeoverMu.Lock()
		n := len(h.hub.takeovers)
		h.hub.takeoverMu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("无等待者后短生命周期 takeover gate 未清理")
}
