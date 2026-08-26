package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

func TestEventPersistentDedupBeforeSink(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hub := NewHub(st, protocol.DefaultHbGraceMs)
	called := 0
	hub.SetEventSink(EventSinkFunc(func(got SensorEvent) {
		called++
		if got.HandID != "hand-01" || got.Body.Name != protocol.EventPageNavigated {
			t.Fatalf("回调事件错误: %+v", got)
		}
	}))
	data, _ := protocol.Encode(protocol.PageNavigatedEventData{At: time.Now().UnixMilli()})
	body := protocol.EventBody{
		Name: protocol.EventPageNavigated, Context: &protocol.EventContext{Platform: "zhilian", AccountRef: "acct-1"},
		ObservedAt: time.Now().UnixMilli(), Data: data,
	}
	raw, _ := protocol.Encode(body)
	env := &protocol.Envelope{Proto: protocol.ProtoVersion, Kind: protocol.KindEvent, MsgID: "event-1", Body: raw}
	c := &Conn{hub: hub, handID: "hand-01"}
	c.handleEvent(env)
	c.handleEvent(env)
	if called != 1 {
		t.Fatalf("重复 event 只能回调一次,得到 %d", called)
	}
	if already, _ := st.MarkProcessed("event-1", string(protocol.KindEvent), "hand-01"); !already {
		t.Fatal("sink 回调前必须已持久化 event msgId")
	}
}

// 2026-08-26:原名 ...ContextsSensorsAndFeatures,传感那一半随被动未读传感删除。
// contexts 缓存、features 缓存、Get 深拷贝、以及"未就绪 context 使 pageHealth
// 降级"四项覆盖原样保留。
func TestRegistryCachesPingContextsAndFeatures(t *testing.T) {
	r := NewRegistry(10_000)
	now := time.Now()
	r.Online("hand-01", "session-1", "boot-1", []string{"chat.readList@1"}, []string{"progress/1"}, now)
	p := protocol.PingBody{
		Contexts: []protocol.PingContext{{Platform: "zhilian", AccountRef: "acct-1", Ready: true}},
	}
	if !r.HeartbeatReport("hand-01", "session-1", "boot-1", p, now.Add(time.Second)) {
		t.Fatal("ping report 未缓存")
	}
	state, _ := r.Get("hand-01")
	if state.PageHealth != CapabilityReady || len(state.Contexts) != 1 {
		t.Fatalf("页面健康错误: %+v", state)
	}
	if len(state.Features) != 1 || state.Features[0] != "progress/1" {
		t.Fatalf("features 未保存: %+v", state)
	}
	// Get 必须深拷贝，调用方不能污染注册表。
	state.Contexts[0].AccountRef = "mutated"
	again, _ := r.Get("hand-01")
	if again.Contexts[0].AccountRef != "acct-1" {
		t.Fatal("Registry.Get 泄露内部切片")
	}
	// 未就绪 context 必须把 pageHealth 降级——它是诊断台唯一的页面状态来源。
	if !r.HeartbeatReport("hand-01", "session-1", "boot-1", protocol.PingBody{
		Contexts: []protocol.PingContext{{
			Platform: "zhilian", AccountRef: "acct-1", Ready: false,
			Reason: protocol.NotReadyReasonPageAbsent,
		}},
	}, now.Add(2*time.Second)) {
		t.Fatal("未就绪 context 的 ping 未接受")
	}
	degraded, _ := r.Get("hand-01")
	if degraded.PageHealth != CapabilityDegraded {
		t.Fatalf("未就绪 context 未使 pageHealth 降级: %+v", degraded)
	}
}

func TestStaleConnectionFrameCannotAdvanceLedger(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hub := NewHub(st, protocol.DefaultHbGraceMs)
	disp := dispatch.New(st, hub)
	hub.SetDispatcher(disp)
	old := &Conn{hub: hub, handID: "hand-01", session: "session-old", bootID: "boot-same"}
	current := &Conn{hub: hub, handID: "hand-01", session: "session-new", bootID: "boot-same"}
	hub.active["hand-01"] = current
	if err := st.CreateCmd(&store.CmdRecord{
		MsgID: "cmd-stale", Name: protocol.PrimDebugPing, Class: string(protocol.ClassReadonly),
		HandID: "hand-01", Session: "session-new", BootIDAtDispatch: "boot-same",
		Status: store.CmdAccepted, Args: `{}`, LogicalDispatchID: "cmd-stale",
	}); err != nil {
		t.Fatal(err)
	}
	body, _ := protocol.Encode(protocol.ResultBody{
		Ref: "cmd-stale", Status: protocol.ResultStatusOk,
		Data: json.RawMessage(`{"echo":null,"swStartedAt":1}`),
	})
	sessionID := old.session
	env := protocol.Envelope{
		Proto: protocol.ProtoVersion, Kind: protocol.KindResult, MsgID: "result-stale",
		Session: &sessionID, Ts: time.Now().UnixMilli(), Attempt: 1, Body: body,
	}
	raw, _ := json.Marshal(env)
	old.handleSessionFrame(context.Background(), raw)
	record, err := st.CmdByMsgID("cmd-stale")
	if err != nil || record.Status != store.CmdAccepted {
		t.Fatalf("旧连接帧推进了账本: %+v err=%v", record, err)
	}
}

func TestDecodeFrameLimitExactAndPlusOne(t *testing.T) {
	prefix := []byte(`{"proto":1}`)
	exact := append(prefix, []byte(strings.Repeat(" ", int(protocol.DefaultMaxMsgBytes)-len(prefix)))...)
	if _, err := decode(exact); err != nil {
		t.Fatalf("maxMsgBytes 精确边界应允许: %v", err)
	}
	plusOne := append(exact, ' ')
	_, err := decode(plusOne)
	var validation *protocol.ValidationError
	if !errors.As(err, &validation) || validation.Rule != "maxBytes" {
		t.Fatalf("maxMsgBytes+1 应在解码前硬拒绝: %T %v", err, err)
	}
}

func TestHelloUnknownFieldsAndHashWarnOnly(t *testing.T) {
	h := newHarness(t)
	c := dial(t, h.wsURL, testOrigin)
	defer c.Close(websocket.StatusNormalClosure, "")
	hello := map[string]any{
		"handId": "hand-future", "bootId": "b-future", "protoSupported": []int{protocol.ProtoVersion},
		"app":  map[string]any{"extVersion": "0.1.0", "browser": "test", "futureAppField": true},
		"caps": []string{"chat.readList@1"}, "features": []string{"lease/1", "progress/1", "cancel/1"},
		"contractHash": "sha256:older", "futureTopField": map[string]any{"x": 1},
	}
	raw, _ := json.Marshal(hello)
	env := protocol.Envelope{Proto: protocol.ProtoVersion, Kind: protocol.KindHello, MsgID: ids.NewMsgID(), Ts: time.Now().UnixMilli(), Attempt: 1, Body: raw}
	buf, _ := json.Marshal(env)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, buf); err != nil {
		t.Fatal(err)
	}
	welcomeEnv := readEnv(t, c)
	if welcomeEnv.Kind != protocol.KindWelcome {
		t.Fatalf("hash mismatch/未知字段不得拒绝握手,得到 %s", welcomeEnv.Kind)
	}
	var welcome protocol.WelcomeBody
	_ = json.Unmarshal(welcomeEnv.Body, &welcome)
	if welcome.ContractMatch {
		t.Fatal("contractHash mismatch 应在 welcome 标记 false")
	}
	var state HandState
	var ok bool
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, ok = h.hub.Registry().Get("hand-future")
		if ok {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !ok || len(state.Caps) != 1 || len(state.Features) != 3 {
		t.Fatalf("hello caps/features 未保存: %+v", state)
	}
	if matched, current := h.hub.HandContractMatch("hand-future"); !current || matched {
		t.Fatalf("当前活连接应保留 mismatch 结论: current=%v matched=%v", current, matched)
	}

	subID, frameCh, _ := h.hub.Frames().Subscribe()
	defer h.hub.Frames().Unsubscribe(subID)
	meta := protocol.Primitives[protocol.PrimDebugSlowEcho]
	argsRaw, _ := protocol.Encode(protocol.DebugSlowEchoArgs{Ms: 0, Outcome: protocol.DebugSlowOutcomeOk})
	bodyRaw, _ := protocol.Encode(protocol.CmdBody{
		Name: protocol.PrimDebugSlowEcho, Ver: meta.Ver, Args: argsRaw,
		IdemKey: "ik1:test:contract-mismatch", Deadline: time.Now().Add(time.Minute).UnixMilli(),
		ExecBudgetMs: meta.ExecBudgetMs,
	})
	blockedID := ids.NewMsgID()
	blocked := protocol.Envelope{
		Proto: protocol.ProtoVersion, Kind: protocol.KindCmd, MsgID: blockedID,
		Session: &state.SessionID, Ts: time.Now().UnixMilli(), Attempt: 1, Body: bodyRaw,
	}
	if err := h.hub.SendEnvelope("hand-future", blocked); !errors.Is(err, dispatch.ErrContractMismatch) {
		t.Fatalf("mismatch 活连接的 effectful 必须在 socket 前拒绝: %v", err)
	}
	select {
	case frame := <-frameCh:
		if frame.MsgID == blockedID {
			t.Fatalf("被契约闸阻断的 effectful 不得进入出站观测或 socket: %+v", frame)
		}
	default:
	}
	audits, auditErr := h.st.AuditEntries(20)
	if auditErr != nil {
		t.Fatal(auditErr)
	}
	foundBlockedAudit := false
	for _, audit := range audits {
		if audit.Category == "effect_contract_mismatch_blocked" && audit.RefMsgID == blockedID {
			foundBlockedAudit = true
			break
		}
	}
	if !foundBlockedAudit {
		t.Fatal("Hub 最终契约闸触发必须审计")
	}

	pingMeta := protocol.Primitives[protocol.PrimDebugPing]
	pingArgs, _ := protocol.Encode(protocol.DebugPingArgs{})
	pingBody, _ := protocol.Encode(protocol.CmdBody{
		Name: protocol.PrimDebugPing, Ver: pingMeta.Ver, Args: pingArgs,
		Deadline: time.Now().Add(time.Minute).UnixMilli(), ExecBudgetMs: pingMeta.ExecBudgetMs,
	})
	readonlyID := ids.NewMsgID()
	readonly := protocol.Envelope{
		Proto: protocol.ProtoVersion, Kind: protocol.KindCmd, MsgID: readonlyID,
		Session: &state.SessionID, Ts: time.Now().UnixMilli(), Attempt: 1, Body: pingBody,
	}
	if err := h.hub.SendEnvelope("hand-future", readonly); err != nil {
		t.Fatalf("hash mismatch 不得阻断 readonly: %v", err)
	}
	if got := readEnv(t, c); got.MsgID != readonlyID {
		t.Fatalf("readonly 应实际进入当前 socket: got=%s want=%s", got.MsgID, readonlyID)
	}
}
