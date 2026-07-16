package session

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// pairGetCreds:配对并返回工牌(handId+token),连接关闭。
func pairGetCreds(t *testing.T, h *harness, boot string) (handID, token string) {
	t.Helper()
	h.pm.OpenWindow(30 * time.Second)
	c := dial(t, h.wsURL, testOrigin)
	sendHello(t, c, nil, nil, boot)
	waitPending(t, h.pm, boot)
	creds, err := h.pm.Confirm(testOrigin, boot)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	_ = readEnv(t, c)
	_ = c.Close(websocket.StatusNormalClosure, "")
	return creds.HandID, creds.Auth
}

// connectHand:以工牌+指定 bootId 建立返回握手,读掉 welcome,返回连接。
func connectHand(t *testing.T, h *harness, handID, token, boot string) *websocket.Conn {
	t.Helper()
	c := dial(t, h.wsURL, testOrigin)
	sendHello(t, c, &handID, &token, boot)
	if readEnv(t, c).Kind != protocol.KindWelcome {
		t.Fatalf("应收 welcome")
	}
	return c
}

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

// 真 WS 重连收编:换 bootId 重连 → 在途 effectful 转 suspect(经 enterSession→OnReconnect 钩子)。
func TestReconnectCollectSuspectViaWS(t *testing.T) {
	h := newHarness(t)
	handID, token := pairGetCreds(t, h, "b-pair")

	// 以 bootId b-1 连接,派 silent effectful,手 ack 但不回 result
	c1 := connectHand(t, h, handID, token, "b-1")
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
	c2 := connectHand(t, h, handID, token, "b-2")
	defer c2.Close(websocket.StatusNormalClosure, "")
	waitCmdStatus(t, h, msgID, store.CmdSuspect)
}

// 真 WS 重连同代:bootId 未变重连 → 在途命令同 msgId 重发,手收到重发帧。
func TestReconnectSameBootResendViaWS(t *testing.T) {
	h := newHarness(t)
	handID, token := pairGetCreds(t, h, "b-pair")

	c1 := connectHand(t, h, handID, token, "b-1")
	msgID, _ := h.disp.Dispatch(handID, protocol.PrimDebugSlowEcho, json.RawMessage(`{"ms":0,"outcome":"silent"}`))
	cmdEnv := readUntilKind(t, c1, protocol.KindCmd)
	writeMsg(c1, protocol.KindAck, cmdEnv.Session, protocol.AckBody{Ref: cmdEnv.MsgID, Status: protocol.AckStatusAccepted})
	waitCmdStatus(t, h, msgID, store.CmdAccepted)
	_ = c1.Close(websocket.StatusNormalClosure, "")
	waitOffline(t, h, handID)

	// 同 bootId b-1 重连 → OnReconnect 同代重发同 msgId
	c2 := connectHand(t, h, handID, token, "b-1")
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

// 真 WS:hub.CloseHand 关闭指定手的连接(ackTimeout 动作的落点)。
func TestCloseHandViaWS(t *testing.T) {
	h := newHarness(t)
	handID, token := pairGetCreds(t, h, "b-pair")
	c := connectHand(t, h, handID, token, "b-1")
	defer c.Close(websocket.StatusNormalClosure, "")

	h.hub.CloseHand(handID, "test")
	// 连接应被关闭:读将失败
	waitOffline(t, h, handID)
	if s, ok := h.hub.Registry().Get(handID); ok && s.Online {
		t.Fatalf("CloseHand 后该手应离线")
	}
}
