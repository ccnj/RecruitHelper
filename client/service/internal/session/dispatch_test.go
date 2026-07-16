package session

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

func bgCtx() context.Context { return context.Background() }

// 一个走真实 WS 的最小假手:ack(accepted) → result(ok)。供派发 happy-path 测试。
func runEchoHand(t *testing.T, c *websocket.Conn) {
	t.Helper()
	go func() {
		for {
			env := readEnvBg(c)
			if env == nil {
				return
			}
			if env.Kind != protocol.KindCmd {
				continue
			}
			var cb protocol.CmdBody
			_ = json.Unmarshal(env.Body, &cb)
			// ack accepted
			writeMsg(c, protocol.KindAck, env.Session, protocol.AckBody{Ref: env.MsgID, Status: protocol.AckStatusAccepted})
			// result ok
			data, _ := json.Marshal(map[string]any{"echo": true})
			writeMsg(c, protocol.KindResult, env.Session, protocol.ResultBody{Ref: env.MsgID, Status: protocol.ResultStatusOk, Data: data})
		}
	}()
}

func readEnvBg(c *websocket.Conn) *protocol.Envelope {
	_, data, err := c.Read(bgCtx())
	if err != nil {
		return nil
	}
	var env protocol.Envelope
	if json.Unmarshal(data, &env) != nil {
		return nil
	}
	return &env
}

func writeMsg(c *websocket.Conn, kind protocol.Kind, session *string, body any) {
	raw, _ := protocol.Encode(body)
	env := protocol.Envelope{Proto: protocol.ProtoVersion, Kind: kind, MsgID: ids.NewMsgID(), Session: session, Ts: time.Now().UnixMilli(), Attempt: 1, Body: raw}
	buf, _ := json.Marshal(env)
	_ = c.Write(bgCtx(), websocket.MessageText, buf)
}

// waitCmdStatus:轮询账本直到某命令到达期望状态。
func waitCmdStatus(t *testing.T, h *harness, msgID string, want store.CmdStatus) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rec, _ := h.st.CmdByMsgID(msgID)
		if rec != nil && rec.Status == want {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	rec, _ := h.st.CmdByMsgID(msgID)
	got := "nil"
	if rec != nil {
		got = string(rec.Status)
	}
	t.Fatalf("命令 %s 未在期限内到达 %s,当前 %s", msgID, want, got)
}

func TestDispatchPingHappyPath(t *testing.T) {
	h := newHarness(t)
	c, handID := pairAndConnect(t, h, "b-disp")
	defer c.Close(websocket.StatusNormalClosure, "")
	runEchoHand(t, c)

	// 派发 debug.ping
	msgID, err := h.disp.Dispatch(handID, protocol.PrimDebugPing, json.RawMessage(`{"echo":"hi"}`))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	// 账本应走到 ok(queued→sent→accepted→ok)
	waitCmdStatus(t, h, msgID, store.CmdOk)

	rec, _ := h.st.CmdByMsgID(msgID)
	if rec.Attempt != 1 {
		t.Fatalf("attempt 应为 1,得到 %d", rec.Attempt)
	}
	if rec.ResultBody == "" {
		t.Fatalf("终局应存 result body")
	}
	if rec.TerminalAt == nil {
		t.Fatalf("终局应盖 TerminalAt")
	}
}

func TestDispatchOfflineHand(t *testing.T) {
	h := newHarness(t)
	// 无连接直接派发 → ErrHandOffline,不记账
	_, err := h.disp.Dispatch("hand-99", protocol.PrimDebugPing, json.RawMessage(`{}`))
	if err == nil {
		t.Fatalf("离线手派发应报错")
	}
	recs, _ := h.st.RecentCmds(10)
	if len(recs) != 0 {
		t.Fatalf("离线派发不应记账,却有 %d 条", len(recs))
	}
}

// result 重复投递:去重,账本只终局化一次,但每次都回 ack。
func TestResultDedup(t *testing.T) {
	h := newHarness(t)
	c, handID := pairAndConnect(t, h, "b-dedup")
	defer c.Close(websocket.StatusNormalClosure, "")

	// 手动驱动:派发 → 手 ack → 手发两次同 msgId 的 result
	msgID, err := h.disp.Dispatch(handID, protocol.PrimDebugPing, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	// 读到 cmd
	var cmdEnv *protocol.Envelope
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && cmdEnv == nil {
		e := readEnv(t, c)
		if e.Kind == protocol.KindCmd {
			cmdEnv = e
		}
	}
	if cmdEnv == nil || cmdEnv.MsgID != msgID {
		t.Fatalf("未读到派发的 cmd")
	}
	writeMsg(c, protocol.KindAck, cmdEnv.Session, protocol.AckBody{Ref: msgID, Status: protocol.AckStatusAccepted})

	// 同一 result msgId 发两次
	resultMsgID := ids.NewMsgID()
	data, _ := json.Marshal(map[string]any{"echo": true})
	res := protocol.ResultBody{Ref: msgID, Status: protocol.ResultStatusOk, Data: data}
	for range 2 {
		raw, _ := protocol.Encode(res)
		env := protocol.Envelope{Proto: protocol.ProtoVersion, Kind: protocol.KindResult, MsgID: resultMsgID, Session: cmdEnv.Session, Ts: time.Now().UnixMilli(), Attempt: 1, Body: raw}
		buf, _ := json.Marshal(env)
		_ = c.Write(bgCtx(), websocket.MessageText, buf)
	}
	waitCmdStatus(t, h, msgID, store.CmdOk)

	// processed 表应只有一条该 result
	already, _ := h.st.MarkProcessed(resultMsgID, "result", handID)
	if !already {
		t.Fatalf("result msgId 应已在 processed 表(去重生效)")
	}
}
