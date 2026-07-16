// Package session:WS 会话层(协议规格 §2、§3、§4 握手部分)。
// 2.2 范围:Origin 校验、hello/welcome/bye 握手、配对挂起、单活顶替、ping/pong(含 pre-session)。
// cmd/result 派发在 2.4 接入。
package session

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"recruithelper/client/service/internal/pairing"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

const helloTimeout = 10 * time.Second

type Hub struct {
	st     *store.Store
	pm     *pairing.Manager
	mu     sync.Mutex
	active map[string]*Conn // handId → 当前活连接(单活)
}

func NewHub(st *store.Store, pm *pairing.Manager) *Hub {
	return &Hub{st: st, pm: pm, active: map[string]*Conn{}}
}

type Conn struct {
	ws        *websocket.Conn
	hub       *Hub
	origin    string
	cancel    context.CancelFunc
	writeMu   sync.Mutex
	closeOnce sync.Once

	handID  string
	session string
	bootID  string
	caps    []string
}

// ServeWS:一条连接的完整生命周期。
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if !strings.HasPrefix(origin, protocol.OriginPrefix) {
		// 挡网页脚本:非扩展 Origin 不升级(规格 §2.2.1)。
		http.Error(w, "forbidden origin", http.StatusForbidden)
		h.st.Audit("origin_rejected", "", "", origin)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	c := &Conn{ws: ws, hub: h, origin: origin, cancel: cancel}
	defer cancel()
	defer func() {
		if c.handID != "" {
			h.uninstall(c.handID, c)
		}
	}()

	frames := make(chan []byte, 4)
	readErr := make(chan error, 1)
	go c.readLoop(ctx, frames, readErr)

	if !c.handshake(ctx, frames, readErr) {
		return
	}
	c.serve(ctx, frames, readErr)
}

// readLoop:把每帧投递到 frames;读错(含对端关闭)投递到 readErr 后退出。
func (c *Conn) readLoop(ctx context.Context, frames chan<- []byte, readErr chan<- error) {
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			select {
			case readErr <- err:
			case <-ctx.Done():
			}
			return
		}
		select {
		case frames <- data:
		case <-ctx.Done():
			return
		}
	}
}

// handshake:读 hello,校验,回 welcome(或配对挂起),返回是否进入正常会话。
func (c *Conn) handshake(ctx context.Context, frames <-chan []byte, readErr <-chan error) bool {
	var data []byte
	select {
	case data = <-frames:
	case <-readErr:
		return false
	case <-time.After(helloTimeout):
		slog.Warn("握手超时:未在期限内收到 hello", "origin", c.origin)
		c.closeQuiet()
		return false
	case <-ctx.Done():
		return false
	}

	env, err := decode(data)
	if err != nil || env.Kind != protocol.KindHello {
		c.sendBye(ctx, protocol.ByeCodeProtoIncompatible, "首帧必须是 hello")
		return false
	}
	var hello protocol.HelloBody
	if err := json.Unmarshal(env.Body, &hello); err != nil {
		c.sendBye(ctx, protocol.ByeCodeProtoIncompatible, "hello body 非法")
		return false
	}
	if !containsInt(hello.ProtoSupported, protocol.ProtoVersion) {
		c.sendBye(ctx, protocol.ByeCodeProtoIncompatible, "无共同协议版本")
		return false
	}
	c.bootID = hello.BootID

	if hello.HandID == nil || hello.Auth == nil {
		return c.handshakePairing(ctx, frames, readErr, hello)
	}
	return c.handshakeReturning(ctx, *hello.HandID, *hello.Auth)
}

// handshakeReturning:日常握手(已有工牌)。
func (c *Conn) handshakeReturning(ctx context.Context, handID, auth string) bool {
	h, err := c.hub.st.HandByID(handID)
	if err != nil {
		slog.Error("查工牌失败", "handId", handID, "err", err)
		c.sendBye(ctx, protocol.ByeCodeAuthFailed, "内部错误")
		return false
	}
	if h == nil || h.TokenHash != store.HashToken(auth) {
		c.hub.st.Audit("auth_failed", handID, "", c.origin)
		c.sendBye(ctx, protocol.ByeCodeAuthFailed, "工牌或令牌无效")
		return false
	}
	c.handID = handID
	return c.enterSession(ctx, nil)
}

// handshakePairing:首次配对——注册待配对,挂起等用户确认或窗口超时;等待期间响应 pre-session ping。
func (c *Conn) handshakePairing(ctx context.Context, frames <-chan []byte, readErr <-chan error, hello protocol.HelloBody) bool {
	confirm, winCtx, err := c.hub.pm.Register(c.origin, c.bootID, pairing.HelloInfo{
		ExtVersion: hello.App.ExtVersion, Caps: hello.Caps,
	})
	if err != nil {
		c.sendBye(ctx, protocol.ByeCodeAuthFailed, "请先在客户端开启配对模式")
		return false
	}
	slog.Info("待配对", "origin", c.origin, "bootId", c.bootID)
	for {
		select {
		case creds := <-confirm:
			c.handID = creds.HandID
			return c.enterSession(ctx, &protocol.IssuedCreds{HandID: creds.HandID, Auth: creds.Auth})
		case <-winCtx.Done():
			c.sendBye(ctx, protocol.ByeCodePairingTimeout, "配对窗已关闭")
			return false
		case data := <-frames:
			c.handlePreSessionFrame(ctx, data)
		case <-readErr:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

// enterSession:签发会话、装入活连接表(顶替旧)、发 welcome。issued 非 nil 表示配对签发。
func (c *Conn) enterSession(ctx context.Context, issued *protocol.IssuedCreds) bool {
	c.session = ids.NewSessionID()
	c.hub.install(c.handID, c)
	c.hub.st.TouchHand(c.handID, time.Now())
	welcome := protocol.WelcomeBody{
		Session:       c.session,
		Proto:         protocol.ProtoVersion,
		Hb:            protocol.HbParams{IntervalMs: protocol.DefaultHbIntervalMs, GraceMs: protocol.DefaultHbGraceMs},
		Limits:        protocol.Limits{MaxMsgBytes: protocol.DefaultMaxMsgBytes, InlineBytes: protocol.DefaultInlineBytes},
		Issued:        issued,
		ContractMatch: true,
		Now:           time.Now().UnixMilli(),
	}
	if err := c.send(ctx, protocol.KindWelcome, nil, welcome); err != nil {
		return false
	}
	slog.Info("会话建立", "handId", c.handID, "session", c.session, "paired", issued != nil)
	return true
}

// serve:正常会话循环。2.2 只处理 ping;cmd/result 在 2.4 接入。
func (c *Conn) serve(ctx context.Context, frames <-chan []byte, readErr <-chan error) {
	for {
		select {
		case data := <-frames:
			c.handleSessionFrame(ctx, data)
		case <-readErr:
			slog.Info("连接关闭", "handId", c.handID)
			return
		case <-ctx.Done():
			return
		}
	}
}

func (c *Conn) handlePreSessionFrame(ctx context.Context, data []byte) {
	env, err := decode(data)
	if err != nil {
		return
	}
	if env.Kind == protocol.KindPing {
		c.sendPong(ctx, env.Session)
	}
}

func (c *Conn) handleSessionFrame(ctx context.Context, data []byte) {
	env, err := decode(data)
	if err != nil {
		slog.Warn("会话帧解码失败", "handId", c.handID, "err", err)
		return
	}
	switch env.Kind {
	case protocol.KindPing:
		c.sendPong(ctx, env.Session)
	default:
		// cmd 派发的回执(ack/result)等在 2.4 接入;此前记日志。
		slog.Debug("暂不处理的会话帧", "handId", c.handID, "kind", env.Kind)
	}
}

func (c *Conn) sendPong(ctx context.Context, session *string) {
	_ = c.send(ctx, protocol.KindPong, session, protocol.PongBody{Now: time.Now().UnixMilli()})
}

func (c *Conn) sendBye(ctx context.Context, code protocol.ByeCode, msg string) {
	_ = c.send(ctx, protocol.KindBye, nil, protocol.ByeBody{Code: code, Message: msg})
	c.closeQuiet()
}

// ---------- 收发底层 ----------

func (c *Conn) send(ctx context.Context, kind protocol.Kind, session *string, body any) error {
	raw, err := protocol.Encode(body)
	if err != nil {
		return err
	}
	env := protocol.Envelope{
		Proto: protocol.ProtoVersion, Kind: kind, MsgID: ids.NewMsgID(),
		Session: session, Ts: time.Now().UnixMilli(), Attempt: 1, Body: raw,
	}
	buf, err := json.Marshal(env)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.ws.Write(wctx, websocket.MessageText, buf); err != nil {
		if ctx.Err() == nil {
			slog.Debug("写帧失败", "handId", c.handID, "kind", kind, "err", err)
		}
		return err
	}
	return nil
}

// closeWith:顶替/主动关闭——发 bye 后关连接(幂等)。
func (c *Conn) closeWith(code protocol.ByeCode, msg string) {
	c.closeOnce.Do(func() {
		bg, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.send(bg, protocol.KindBye, nil, protocol.ByeBody{Code: code, Message: msg})
		if c.cancel != nil {
			c.cancel()
		}
		_ = c.ws.Close(websocket.StatusNormalClosure, string(code))
	})
}

func (c *Conn) closeQuiet() {
	c.closeOnce.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
		_ = c.ws.Close(websocket.StatusNormalClosure, "")
	})
}

// ---------- 活连接表(单活顶替) ----------

func (h *Hub) install(handID string, c *Conn) {
	h.mu.Lock()
	old := h.active[handID]
	h.active[handID] = c
	h.mu.Unlock()
	if old != nil && old != c {
		h.st.Audit("superseded", handID, "", "新连接顶替旧连接")
		slog.Info("单活顶替", "handId", handID)
		old.closeWith(protocol.ByeCodeSuperseded, "superseded by new connection")
	}
}

func (h *Hub) uninstall(handID string, c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active[handID] == c {
		delete(h.active, handID)
	}
}

// ActiveHandIDs:当前在线手(状态页/测试用)。
func (h *Hub) ActiveHandIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.active))
	for id := range h.active {
		out = append(out, id)
	}
	return out
}

// ---------- 工具 ----------

func decode(data []byte) (*protocol.Envelope, error) {
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if env.Proto != protocol.ProtoVersion {
		return nil, errors.New("proto 版本不符")
	}
	return &env, nil
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
