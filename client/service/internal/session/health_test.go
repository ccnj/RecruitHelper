package session

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"

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

// pairAndConnect:配对并建立会话,返回该连接与工牌 handId。
func pairAndConnect(t *testing.T, h *harness, boot string) (*websocket.Conn, string) {
	t.Helper()
	h.pm.OpenWindow(30 * time.Second)
	c := dial(t, h.wsURL, testOrigin)
	sendHello(t, c, nil, nil, boot)
	waitPending(t, h.pm, boot)
	creds, err := h.pm.Confirm(testOrigin, boot)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if readEnv(t, c).Kind != protocol.KindWelcome {
		t.Fatalf("应收 welcome")
	}
	return c, creds.HandID
}

// 连接开着但不发心跳 → sweep 后 stalled + 告警(真异常)。
func TestHubStalledAlarms(t *testing.T) {
	h := newHarnessGrace(t, 200) // grace 200ms
	c, handID := pairAndConnect(t, h, "b-stall")
	defer c.Close(websocket.StatusNormalClosure, "")

	time.Sleep(400 * time.Millisecond) // 超 grace 且不发 ping
	h.hub.runSweep(time.Now())

	st, ok := h.hub.Registry().Get(handID)
	if !ok || st.Health != HealthStalled {
		t.Fatalf("应为 stalled,得到 %+v", st)
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

// 连接干净关闭 → 静默下线,sweep 不告警(设计内常态)。
func TestHubCleanCloseSilent(t *testing.T) {
	h := newHarnessGrace(t, 200)
	c, handID := pairAndConnect(t, h, "b-clean")
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
	c, handID := pairAndConnect(t, h, "b-hb")
	defer c.Close(websocket.StatusNormalClosure, "")

	// established 后任何 ping 都刷新心跳(session 值不影响 registry 判定)
	sendPing(t, c, nil)
	time.Sleep(100 * time.Millisecond)
	h.hub.runSweep(time.Now())
	if st, _ := h.hub.Registry().Get(handID); st.Health != HealthReady {
		t.Fatalf("心跳新鲜应 ready,得到 %s", st.Health)
	}
}
