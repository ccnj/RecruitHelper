package session

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

const (
	testOrigin    = "chrome-extension://testhandaaaaaaaaaaaaaaaaaaaaaaaa"
	secondOrigin  = "chrome-extension://secondhandbbbbbbbbbbbbbbbbbbbbbb"
	defaultHandID = "local-hand-test"
)

type harness struct {
	srv   *httptest.Server
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
	hub := NewHub(st, graceMs)
	disp := dispatch.New(st, hub)
	hub.SetDispatcher(disp)
	mux := http.NewServeMux()
	mux.HandleFunc(protocol.TransportPath, hub.ServeWS)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &harness{srv: srv, st: st, hub: hub, disp: disp, wsURL: "ws" + strings.TrimPrefix(srv.URL, "http") + protocol.TransportPath}
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

func sendHello(t *testing.T, c *websocket.Conn, handID, bootID string) {
	t.Helper()
	raw, err := protocol.Encode(protocol.HelloBody{
		HandID: handID, BootID: bootID,
		ProtoSupported: []int{protocol.ProtoVersion},
		App:            protocol.AppInfo{ExtVersion: "0.1.0", Browser: "test"},
		Caps:           []string{"debug.ping@1"},
		Features:       []string{},
		ContractHash:   protocol.ContractHash,
	})
	if err != nil {
		t.Fatalf("encode hello: %v", err)
	}
	sendHelloBody(t, c, raw)
}

func sendHelloBody(t *testing.T, c *websocket.Conn, raw json.RawMessage) {
	t.Helper()
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

func connectHand(t *testing.T, h *harness, handID, bootID string) *websocket.Conn {
	t.Helper()
	c := dial(t, h.wsURL, testOrigin)
	sendHello(t, c, handID, bootID)
	env := readEnv(t, c)
	if env.Kind != protocol.KindWelcome {
		t.Fatalf("hello 应立即收到 welcome，实际 %s", env.Kind)
	}
	var welcome protocol.WelcomeBody
	if err := json.Unmarshal(env.Body, &welcome); err != nil || welcome.Session == "" {
		t.Fatalf("welcome 会话非法: %+v err=%v", welcome, err)
	}
	if env.Session != nil {
		t.Fatal("welcome 信封 session 必须为 null")
	}
	return c
}

func seedSessionSourcingBatchForBootChange(
	t *testing.T,
	h *harness,
	handID, identityBootID, suffix string,
) (store.AccountKey, *store.SourcingBatch) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	documents := []m5ai.JobConfigDocument{
		{DocType: "多轮沟通", Content: "reply"},
		{DocType: "客户事实库", Content: "facts"},
		{DocType: "意向判断", Content: "intent"},
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].DocType < documents[j].DocType })
	revision := m5ai.ContextRevision{
		ContextID:    "context-session-feed-" + suffix,
		RevisionHash: "revision-session-feed-" + suffix,
		SourceKind:   "localImport", DisplayName: "synthetic-position",
		SourcePackage: m5ai.JobConfigDocumentPackage{Documents: documents},
		Communication: m5ai.CommunicationView{
			ReplyPrompt: "reply", IntentPrompt: "intent", CustomerFacts: "facts",
			MappingVersion: m5ai.MappingVersion,
		},
		CreatedAt: now.Add(-time.Hour),
	}
	if _, _, err := h.st.SaveJobAIContextRevision(revision); err != nil {
		t.Fatal(err)
	}
	key := store.AccountKey{Platform: "zhilian", AccountRef: "account-session-feed-" + suffix}
	if err := h.st.CreateAccount(&store.Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.BindAccountPrincipal(
		key, handID, "principal-session-feed-"+suffix,
		"persisted-session-"+suffix, identityBootID, now.Add(-time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	started, err := h.st.StartSourcingBatch(store.StartSourcingBatchRequest{
		BatchID:  "batch-session-feed-" + suffix,
		Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revision.RevisionHash, TargetCount: 30,
		StartedAt: now.Add(-time.Minute),
	})
	if err != nil || started == nil || !started.Created {
		t.Fatalf("建立 active 采集批次失败: result=%+v err=%v", started, err)
	}
	return key, &started.Batch
}

func TestHelloBootChangeInvalidatesSourcingFeedBeforeReady(t *testing.T) {
	tests := []struct {
		name           string
		slug           string
		identityBootID string
		helloBootID    string
		wantChanged    bool
	}{
		{name: "boot变化", slug: "changed", identityBootID: "boot-old", helloBootID: "boot-new", wantChanged: true},
		{name: "同boot重连", slug: "same", identityBootID: "boot-same", helloBootID: "boot-same", wantChanged: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			handID := "hand-session-feed-" + test.slug
			key, started := seedSessionSourcingBatchForBootChange(
				t, h, handID, test.identityBootID, test.slug,
			)
			connection := connectHand(t, h, handID, test.helloBootID)
			defer connection.Close(websocket.StatusNormalClosure, "")

			ready, ok := h.hub.Registry().Get(handID)
			if !ok || !ready.Online || ready.BootID != test.helloBootID {
				t.Fatalf("hello 未进入 ready: state=%+v exists=%t", ready, ok)
			}
			batch, err := h.st.SourcingBatchByID(started.BatchID)
			if err != nil || batch == nil {
				t.Fatalf("读取批次失败: batch=%+v err=%v", batch, err)
			}
			account, err := h.st.AccountByKey(key)
			if err != nil || account == nil {
				t.Fatalf("读取账号失败: account=%+v err=%v", account, err)
			}
			if test.wantChanged {
				if batch.Status != store.SourcingBatchStopped || batch.Reason != store.SourcingFeedChangedReason ||
					account.SourcingFeedInvalidatedAt == nil || account.PausedReason != store.SourcingFeedChangedReason {
					t.Fatalf("新 boot welcome/ready 时旧推荐流仍有效: batch=%+v account=%+v", batch, account)
				}
				return
			}
			if batch.Status != store.SourcingBatchPreparing || batch.EndedAt != nil ||
				account.SourcingFeedInvalidatedAt != nil {
				t.Fatalf("同 boot 重连错误失效推荐流: batch=%+v account=%+v", batch, account)
			}
		})
	}
}

func TestHelloAutoRegistersAndReconnectReusesHand(t *testing.T) {
	h := newHarness(t)
	c := connectHand(t, h, defaultHandID, "boot-first")
	first, err := h.st.HandByID(defaultHandID)
	if err != nil || first == nil {
		t.Fatalf("首次 hello 未自动登记手: %+v err=%v", first, err)
	}
	if first.Origin != testOrigin || first.CreatedAt.IsZero() || first.LastSeenAt.IsZero() {
		t.Fatalf("手登记字段不完整: %+v", first)
	}
	_ = c.Close(websocket.StatusNormalClosure, "")
	waitOffline(t, h, defaultHandID)

	time.Sleep(time.Millisecond)
	c2 := connectHand(t, h, defaultHandID, "boot-second")
	defer c2.Close(websocket.StatusNormalClosure, "")
	second, err := h.st.HandByID(defaultHandID)
	if err != nil || second == nil {
		t.Fatalf("重连后读手失败: %+v err=%v", second, err)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) || !second.LastSeenAt.After(first.LastSeenAt) {
		t.Fatalf("重连应复用同一手并只刷新 lastSeen: first=%+v second=%+v", first, second)
	}
	hands, err := h.st.Hands()
	if err != nil || len(hands) != 1 {
		t.Fatalf("同 handId 重连不得增生记录: n=%d err=%v", len(hands), err)
	}
}

func TestHelloMissingOrInvalidHandIDRejectedLoudly(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		name   string
		handID any
		omit   bool
	}{
		{name: "missing", omit: true},
		{name: "null", handID: nil},
		{name: "empty", handID: ""},
		{name: "too-long", handID: strings.Repeat("h", 129)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{
				"bootId": "boot-invalid", "protoSupported": []int{protocol.ProtoVersion},
				"contractHash": protocol.ContractHash,
				"app":          map[string]any{"extVersion": "0.1.0", "browser": "test"},
				"caps":         []string{}, "features": []string{},
			}
			if !tc.omit {
				body["handId"] = tc.handID
			}
			raw, _ := json.Marshal(body)
			c := dial(t, h.wsURL, testOrigin)
			defer c.Close(websocket.StatusNormalClosure, "")
			sendHelloBody(t, c, raw)
			env := readEnv(t, c)
			if env.Kind != protocol.KindBye {
				t.Fatalf("非法 handId 应收到 bye，实际 %s", env.Kind)
			}
			var bye protocol.ByeBody
			_ = json.Unmarshal(env.Body, &bye)
			if bye.Code != protocol.ByeCodeProtoIncompatible {
				t.Fatalf("非法 handId 应用 PROTO_INCOMPATIBLE 响亮拒绝，实际 %s", bye.Code)
			}
		})
	}
	hands, err := h.st.Hands()
	if err != nil || len(hands) != 0 {
		t.Fatalf("非法 hello 不得落 hands: n=%d err=%v", len(hands), err)
	}
}

func TestSingleActiveSupersede(t *testing.T) {
	h := newHarness(t)
	c1 := connectHand(t, h, "hand-single-active", "boot-one")
	defer c1.Close(websocket.StatusNormalClosure, "")
	c2 := connectHand(t, h, "hand-single-active", "boot-two")
	defer c2.Close(websocket.StatusNormalClosure, "")
	env := readEnv(t, c1)
	if env.Kind != protocol.KindBye {
		t.Fatalf("旧连接应被顶替收 bye，实际 %s", env.Kind)
	}
	var bye protocol.ByeBody
	_ = json.Unmarshal(env.Body, &bye)
	if bye.Code != protocol.ByeCodeSuperseded {
		t.Fatalf("顶替应返回 SUPERSEDED，实际 %s", bye.Code)
	}
	if ids := h.hub.ActiveHandIDs(); len(ids) != 1 || ids[0] != "hand-single-active" {
		t.Fatalf("同 handId 必须单活: %v", ids)
	}
}

func TestOriginChangeIsSoftAuditedAndAccepted(t *testing.T) {
	h := newHarness(t)
	c1 := connectHand(t, h, "hand-origin-change", "boot-one")
	defer c1.Close(websocket.StatusNormalClosure, "")
	c2 := dial(t, h.wsURL, secondOrigin)
	defer c2.Close(websocket.StatusNormalClosure, "")
	sendHello(t, c2, "hand-origin-change", "boot-two")
	if env := readEnv(t, c2); env.Kind != protocol.KindWelcome {
		t.Fatalf("Origin 变化只软审计，不得拒绝: %s", env.Kind)
	}
	hand, err := h.st.HandByID("hand-origin-change")
	if err != nil || hand == nil || hand.Origin != secondOrigin {
		t.Fatalf("应记录最近 Origin: %+v err=%v", hand, err)
	}
	audits, err := h.st.AuditEntries(20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, audit := range audits {
		if audit.Category == "hand_origin_changed" && audit.HandID == "hand-origin-change" &&
			strings.Contains(audit.Detail, testOrigin) && strings.Contains(audit.Detail, secondOrigin) {
			found = true
		}
	}
	if !found {
		t.Fatal("Origin 变化必须留下新旧值软审计")
	}
}

func TestOriginRejected(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := websocket.Dial(ctx, h.wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://evil.example.com"}}})
	if err == nil {
		t.Fatal("非扩展 Origin 应被拒绝升级")
	}
}
