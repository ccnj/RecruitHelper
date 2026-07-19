package session

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/coder/websocket"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

func sendPing(t *testing.T, c *websocket.Conn, session *string) {
	t.Helper()
	raw, _ := protocol.Encode(protocol.PingBody{QueueDepth: 0})
	env := protocol.Envelope{Proto: protocol.ProtoVersion, Kind: protocol.KindPing, MsgID: ids.NewMsgID(), Session: session, Ts: time.Now().UnixMilli(), Attempt: 1, Body: raw}
	buf, _ := json.Marshal(env)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, buf); err != nil {
		t.Fatalf("write ping: %v", err)
	}
}

// 连接开着但不发心跳 → sweep 响亮告警、关闭假死 WS，最终落 offline。
func TestHubStalledClosesConnectionAndGoesOffline(t *testing.T) {
	h := newHarnessGrace(t, 200) // grace 200ms
	handID := "hand-stall"
	c := connectHand(t, h, handID, "b-stall")
	defer c.Close(websocket.StatusNormalClosure, "")

	before, ok := h.hub.Registry().Get(handID)
	if !ok {
		t.Fatal("握手后缺少注册表记录")
	}
	h.hub.runSweep(before.LastHbAt.Add(201 * time.Millisecond))

	st, ok := h.hub.Registry().Get(handID)
	if !ok || st.Online || st.Health != HealthOffline {
		t.Fatalf("假死链关闭后应 offline,得到 %+v", st)
	}
	if _, _, online := h.hub.HandSession(handID); online {
		t.Fatal("假死链关闭后 HandSession 不得仍称在线")
	}
	if len(h.hub.ActiveHandIDs()) != 0 {
		t.Fatalf("假死链仍留在 active: %v", h.hub.ActiveHandIDs())
	}
	readCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := c.Read(readCtx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("sweep 应主动关闭客户端 WS,得到 err=%v", err)
	}
	es, _ := h.st.AuditEntries(20)
	found := false
	for _, e := range es {
		if e.Category == "hand_stalled" && e.HandID == handID {
			found = true
		}
	}
	if !found {
		t.Fatalf("stalled 应产生 hand_stalled 审计")
	}
}

// Sweep 已把会话翻为 stalled、尚未执行 close 的极短窗口，也必须对所有业务入口
// 立即不可用；actor 依赖的 HandSession 与 dispatcher 共用这条判据。
func TestStalledWindowRejectsActorAndDispatcherBeforeClose(t *testing.T) {
	h := newHarnessGrace(t, 200)
	handID := "hand-stalled-window"
	c := connectHand(t, h, handID, "boot-window")
	defer c.Close(websocket.StatusNormalClosure, "")

	before, _ := h.hub.Registry().Get(handID)
	stalled := h.hub.Registry().Sweep(before.LastHbAt.Add(201 * time.Millisecond))
	if len(stalled) != 1 {
		t.Fatalf("应选中一条 stalled 会话: %+v", stalled)
	}
	if _, _, online := h.hub.HandSession(handID); online {
		t.Fatal("actor availability 不得把 stalled 当 online")
	}
	if _, _, ok := h.hub.HandNegotiation(handID); ok {
		t.Fatal("stalled 会话不得暴露协商能力")
	}
	callbackCalled := false
	current, err := h.hub.WithCurrentHandSession(handID, before.SessionID, before.BootID, func() error {
		callbackCalled = true
		return nil
	})
	if err != nil || current || callbackCalled {
		t.Fatalf("stalled 会话不得通过提交栅栏: current=%v called=%v err=%v", current, callbackCalled, err)
	}

	sessionID := before.SessionID
	sendErr := h.hub.SendEnvelope(handID, protocol.Envelope{
		Proto: protocol.ProtoVersion, Kind: protocol.KindCmd, MsgID: ids.NewMsgID(),
		Session: &sessionID, Ts: time.Now().UnixMilli(), Attempt: 1, Body: json.RawMessage(`{}`),
	})
	if !errors.Is(sendErr, dispatch.ErrHandOffline) {
		t.Fatalf("stalled SendEnvelope 应报 offline,得到 %v", sendErr)
	}
	if _, err := h.disp.Dispatch(handID, protocol.PrimDebugPing, json.RawMessage(`{}`)); !errors.Is(err, dispatch.ErrHandOffline) {
		t.Fatalf("stalled dispatcher 应报 offline,得到 %v", err)
	}
	if rows, _ := h.st.RecentCmds(10); len(rows) != 0 {
		t.Fatalf("stalled 派发不得记账,得到 %d 条", len(rows))
	}
	if !h.hub.closeStalled(stalled[0]) {
		t.Fatal("同一 stalled 当前会话应被关闭")
	}
	if state, _ := h.hub.Registry().Get(handID); state.Online || state.Health != HealthOffline {
		t.Fatalf("关闭后应 offline: %+v", state)
	}
}

// 连接干净关闭 → 静默下线,sweep 不告警(设计内常态)。
func TestHubCleanCloseSilent(t *testing.T) {
	h := newHarnessGrace(t, 200)
	handID := "hand-clean"
	c := connectHand(t, h, handID, "b-clean")
	_ = c.Close(websocket.StatusNormalClosure, "")

	// 等服务端感知关闭并下线
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s, _ := h.hub.Registry().Get(handID); !s.Online {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s, _ := h.hub.Registry().Get(handID); s.Online {
		t.Fatalf("干净关闭后应下线")
	}
	time.Sleep(300 * time.Millisecond) // 远超 grace
	h.hub.runSweep(time.Now())

	es, _ := h.st.AuditEntries(20)
	for _, e := range es {
		if e.Category == "hand_stalled" {
			t.Fatalf("干净关闭不应产生 stalled 告警")
		}
	}
}

// 持续心跳 → 保持 ready,不告警。
func TestHubHeartbeatKeepsReady(t *testing.T) {
	h := newHarnessGrace(t, 500)
	handID := "hand-heartbeat"
	c := connectHand(t, h, handID, "b-hb")
	defer c.Close(websocket.StatusNormalClosure, "")

	before, _ := h.hub.Registry().Get(handID)
	sessionID := before.SessionID
	sendPing(t, c, &sessionID)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, _ := h.hub.Registry().Get(handID)
		if state.LastHbAt.After(before.LastHbAt) {
			before = state
			break
		}
		time.Sleep(time.Millisecond)
	}
	h.hub.runSweep(before.LastHbAt.Add(400 * time.Millisecond))
	if st, _ := h.hub.Registry().Get(handID); st.Health != HealthReady {
		t.Fatalf("心跳新鲜应 ready,得到 %s", st.Health)
	}
}

// Sweep 选中旧会话后若新 hello 已顶替，即使 bootId 相同，延迟的关链动作也只能
// 对旧 session no-op，不能关掉新链或把新注册表记录置离线。
func TestSweepCandidateCannotCloseSupersedingSameBootSession(t *testing.T) {
	h := newHarnessGrace(t, 200)
	handID := "hand-sweep-takeover"
	oldClient := connectHand(t, h, handID, "boot-same")
	defer oldClient.Close(websocket.StatusNormalClosure, "")
	oldState, _ := h.hub.Registry().Get(handID)
	stalled := h.hub.Registry().Sweep(oldState.LastHbAt.Add(201 * time.Millisecond))
	if len(stalled) != 1 {
		t.Fatalf("旧会话应被 sweep 选中: %+v", stalled)
	}

	newClient := connectHand(t, h, handID, "boot-same")
	defer newClient.Close(websocket.StatusNormalClosure, "")
	newState, _ := h.hub.Registry().Get(handID)
	if newState.SessionID == stalled[0].SessionID || newState.Health != HealthReady {
		t.Fatalf("新会话未正确顶替: old=%+v new=%+v", stalled[0], newState)
	}
	if h.hub.closeStalled(stalled[0]) {
		t.Fatal("延迟的旧 stalled 证词不应命中新 active")
	}
	gotSession, gotBoot, online := h.hub.HandSession(handID)
	if !online || gotSession != newState.SessionID || gotBoot != "boot-same" {
		t.Fatalf("新链被误伤: session=%s boot=%s online=%v state=%+v", gotSession, gotBoot, online, newState)
	}

	// 新链不只留在 map 中，还应能真实承接命令。
	msgID, err := h.disp.Dispatch(handID, protocol.PrimDebugPing, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("新链派发失败: %v", err)
	}
	cmd := readUntilKind(t, newClient, protocol.KindCmd)
	if cmd.MsgID != msgID || cmd.Session == nil || *cmd.Session != newState.SessionID {
		t.Fatalf("命令未落到新 session: msg=%s env=%+v", msgID, cmd)
	}
}
