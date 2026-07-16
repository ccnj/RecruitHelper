package session

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/pairing"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

const testOrigin = "chrome-extension://testhandaaaaaaaaaaaaaaaaaaaaaaaa"

type harness struct {
	srv   *httptest.Server
	pm    *pairing.Manager
	st    *store.Store
	hub   *Hub
	disp  *dispatch.Dispatcher
	wsURL string
}

func newHarness(t *testing.T) *harness { return newHarnessGrace(t, protocol.DefaultHbGraceMs) }

func newHarnessGrace(t *testing.T, graceMs int64) *harness {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	pm := pairing.New(st)
	t.Cleanup(pm.CloseWindow)
	hub := NewHub(st, pm, graceMs)
	disp := dispatch.New(st, hub)
	hub.SetDispatcher(disp)
	mux := http.NewServeMux()
	mux.HandleFunc(protocol.TransportPath, hub.ServeWS)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &harness{srv: srv, pm: pm, st: st, hub: hub, disp: disp, wsURL: "ws" + strings.TrimPrefix(srv.URL, "http") + protocol.TransportPath}
}

func dial(t *testing.T, url, origin string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {origin}}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return c
}

func sendHello(t *testing.T, c *websocket.Conn, handID, auth *string, bootID string) {
	t.Helper()
	raw, _ := protocol.Encode(protocol.HelloBody{
		HandID: handID, Auth: auth, BootID: bootID,
		ProtoSupported: []int{protocol.ProtoVersion},
		App:            protocol.AppInfo{ExtVersion: "0.1.0", Browser: "test"},
		Caps:           []string{"debug.ping@1"},
	})
	env := protocol.Envelope{Proto: protocol.ProtoVersion, Kind: protocol.KindHello, MsgID: ids.NewMsgID(), Ts: time.Now().UnixMilli(), Attempt: 1, Body: raw}
	buf, _ := json.Marshal(env)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, buf); err != nil {
		t.Fatalf("write hello: %v", err)
	}
}

func readEnv(t *testing.T, c *websocket.Conn) *protocol.Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &env
}

// waitPending:轮询直到某 bootId 出现在待配对列表(Register 已完成)。
func waitPending(t *testing.T, pm *pairing.Manager, bootID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range pm.Pending() {
			if p.BootID == bootID {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("待配对项 %s 未在期限内出现", bootID)
}

func TestPairingThenReturning(t *testing.T) {
	h := newHarness(t)
	h.pm.OpenWindow(30 * time.Second)

	// 配对:无 token hello → 挂起 → Confirm → welcome{issued}
	c := dial(t, h.wsURL, testOrigin)
	boot := "b-pair01"
	sendHello(t, c, nil, nil, boot)
	waitPending(t, h.pm, boot)
	creds, err := h.pm.Confirm(testOrigin, boot)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	env := readEnv(t, c)
	if env.Kind != protocol.KindWelcome {
		t.Fatalf("期望 welcome,得到 %s", env.Kind)
	}
	var wb protocol.WelcomeBody
	_ = json.Unmarshal(env.Body, &wb)
	if wb.Issued == nil || wb.Issued.HandID != creds.HandID {
		t.Fatalf("welcome 应带 issued 且 handId 一致: %+v", wb.Issued)
	}
	if wb.Issued.Auth != creds.Auth {
		t.Fatalf("welcome token 与签发不一致")
	}
	if env.Session != nil {
		t.Fatalf("welcome 信封 session 必须为 null")
	}
	_ = c.Close(websocket.StatusNormalClosure, "")

	// 落库校验:token 只存哈希
	stored, _ := h.st.HandByID(creds.HandID)
	if stored == nil || stored.TokenHash == creds.Auth {
		t.Fatalf("工牌未正确落库或 token 明文入库")
	}

	// 日常握手:凭工牌 → welcome 无 issued
	c2 := dial(t, h.wsURL, testOrigin)
	sendHello(t, c2, &creds.HandID, &creds.Auth, "b-ret01")
	env2 := readEnv(t, c2)
	if env2.Kind != protocol.KindWelcome {
		t.Fatalf("日常握手期望 welcome,得到 %s", env2.Kind)
	}
	var wb2 protocol.WelcomeBody
	_ = json.Unmarshal(env2.Body, &wb2)
	if wb2.Issued != nil {
		t.Fatalf("日常握手 welcome 不应带 issued")
	}
	if wb2.Session == "" {
		t.Fatalf("日常握手应分配 session")
	}
	_ = c2.Close(websocket.StatusNormalClosure, "")
}

func TestReturningBadToken(t *testing.T) {
	h := newHarness(t)
	h.pm.OpenWindow(30 * time.Second)
	c := dial(t, h.wsURL, testOrigin)
	sendHello(t, c, nil, nil, "b-x")
	waitPending(t, h.pm, "b-x")
	creds, _ := h.pm.Confirm(testOrigin, "b-x")
	_ = readEnv(t, c) // welcome
	_ = c.Close(websocket.StatusNormalClosure, "")

	// 错 token
	c2 := dial(t, h.wsURL, testOrigin)
	bad := "deadbeef"
	sendHello(t, c2, &creds.HandID, &bad, "b-y")
	env := readEnv(t, c2)
	if env.Kind != protocol.KindBye {
		t.Fatalf("错 token 期望 bye,得到 %s", env.Kind)
	}
	var bb protocol.ByeBody
	_ = json.Unmarshal(env.Body, &bb)
	if bb.Code != protocol.ByeCodeAuthFailed {
		t.Fatalf("期望 AUTH_FAILED,得到 %s", bb.Code)
	}
}

func TestPairingWindowClosed(t *testing.T) {
	h := newHarness(t)
	// 不开窗
	c := dial(t, h.wsURL, testOrigin)
	sendHello(t, c, nil, nil, "b-nowin")
	env := readEnv(t, c)
	if env.Kind != protocol.KindBye {
		t.Fatalf("窗未开期望 bye,得到 %s", env.Kind)
	}
	var bb protocol.ByeBody
	_ = json.Unmarshal(env.Body, &bb)
	if bb.Code != protocol.ByeCodeAuthFailed {
		t.Fatalf("期望 AUTH_FAILED,得到 %s", bb.Code)
	}
}

func TestSingleActiveSupersede(t *testing.T) {
	h := newHarness(t)
	h.pm.OpenWindow(30 * time.Second)
	c := dial(t, h.wsURL, testOrigin)
	sendHello(t, c, nil, nil, "b-sup")
	waitPending(t, h.pm, "b-sup")
	creds, _ := h.pm.Confirm(testOrigin, "b-sup")
	_ = readEnv(t, c)
	_ = c.Close(websocket.StatusNormalClosure, "")

	// conn1 日常握手
	c1 := dial(t, h.wsURL, testOrigin)
	sendHello(t, c1, &creds.HandID, &creds.Auth, "b-c1")
	if readEnv(t, c1).Kind != protocol.KindWelcome {
		t.Fatalf("conn1 应 welcome")
	}
	// conn2 同 handId 握手 → 顶替
	c2 := dial(t, h.wsURL, testOrigin)
	sendHello(t, c2, &creds.HandID, &creds.Auth, "b-c2")
	if readEnv(t, c2).Kind != protocol.KindWelcome {
		t.Fatalf("conn2 应 welcome")
	}
	// conn1 应收到 bye(SUPERSEDED)
	env := readEnv(t, c1)
	if env.Kind != protocol.KindBye {
		t.Fatalf("conn1 应被顶替收 bye,得到 %s", env.Kind)
	}
	var bb protocol.ByeBody
	_ = json.Unmarshal(env.Body, &bb)
	if bb.Code != protocol.ByeCodeSuperseded {
		t.Fatalf("期望 SUPERSEDED,得到 %s", bb.Code)
	}
}

func TestOriginRejected(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := websocket.Dial(ctx, h.wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://evil.example.com"}}})
	if err == nil {
		t.Fatalf("非扩展 Origin 应被拒绝升级")
	}
}
