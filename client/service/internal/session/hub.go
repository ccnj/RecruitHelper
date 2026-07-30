// Package session:WS 会话层(协议规格 §2、§3、§4 握手部分)。
// 2.2 范围:Origin 校验、hello/welcome/bye 握手、本地手自动登记、单活顶替、ping/pong。
// cmd/result 派发在 2.4 接入。
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

const (
	helloTimeout        = 10 * time.Second
	healthSweepInterval = 5 * time.Second
)

type Hub struct {
	st         *store.Store
	reg        *Registry
	frames     *FrameBus
	dispatcher *dispatch.Dispatcher
	eventSink  EventSink
	// handReadyHook:某只手 ready 之后的通知。回调只允许做无阻塞的登记动作,
	// 详见 SetHandReadyHook。
	handReadyHook func(handID string)
	mu            sync.Mutex
	active        map[string]*Conn // handId → 当前活连接(单活)
	takeoverMu    sync.Mutex
	takeovers     map[string]*takeoverGate // 仅串行同 handId 的 welcome→接管→收编

	// blob/1 上行子集(协议规格 §13 激活记录):配置后 welcome 携带 BlobParams。
	blobIssuer   BlobTokenIssuer
	blobEndpoint string
	blobMaxBytes int64
}

// BlobTokenIssuer 为每次 welcome 轮换会话作用域 blob token(旧值即刻作废)。
type BlobTokenIssuer interface {
	Rotate(handID string) string
}

func NewHub(st *store.Store, graceMs int64) *Hub {
	return &Hub{
		st: st, reg: NewRegistry(graceMs), frames: NewFrameBus(),
		active: map[string]*Conn{}, takeovers: map[string]*takeoverGate{},
	}
}

type takeoverGate struct {
	mu   sync.Mutex
	refs int
}

// Frames:帧观测总线访问器(测试页协议观测台)。
func (h *Hub) Frames() *FrameBus { return h.frames }

// SetDispatcher:接线派发器(构造后回填,打破 hub↔dispatcher 循环)。
func (h *Hub) SetDispatcher(d *dispatch.Dispatcher) { h.dispatcher = d }

// SetEventSink 安装传感事件消费者。processed_msgs 的持久去重先于回调发生。
func (h *Hub) SetEventSink(s EventSink) { h.eventSink = s }

// SetHandReadyHook 安装「某只手已经 ready」的通知(构造后回填,同 SetDispatcher)。
//
// 回调在 activate 之外、h.mu 已释放之后触发,那时注册表已经登记新 session/boot。
// 但它仍然跑在该连接的读循环上,所以**只允许做无阻塞的登记动作**(塞一个 channel、
// 置一个标志),不得在里面等待任何东西 —— 尤其不得同步发起需要等待下一次 hello
// 的编排:那会把读循环卡住,而它等的下一个 hello 正需要这个读循环去处理,当场自锁。
func (h *Hub) SetHandReadyHook(fn func(handID string)) { h.handReadyHook = fn }

// SetBlob 接线 blob/1 上行子集:此后每次 welcome 轮换并下发会话作用域 token。
func (h *Hub) SetBlob(issuer BlobTokenIssuer, endpoint string, maxBytes int64) {
	h.blobIssuer = issuer
	h.blobEndpoint = endpoint
	h.blobMaxBytes = maxBytes
}

// Registry:注册表访问器(状态页/测试)。
func (h *Hub) Registry() *Registry { return h.reg }

// SendEnvelope 实现 dispatch.Sender:向某手当前会话发已构造的信封。
// active 选择、session 复核与 socket 写入共用 h.mu 作一个线性化区间：
// 新 hello 要么在本次写完成后接管，要么先接管并使旧 session 响亮失败；
// 绝不允许先取出旧 Conn、接管/收编完成后再向旧 socket 落帧。
func (h *Hub) SendEnvelope(handID string, env protocol.Envelope) error {
	h.mu.Lock()
	blockedByContract := false
	defer func() {
		h.mu.Unlock()
		if blockedByContract {
			h.st.Audit("effect_contract_mismatch_blocked", handID, env.MsgID, "stage=socket")
		}
	}()
	c := h.active[handID]
	if c == nil {
		return dispatch.ErrHandOffline
	}
	if env.Session == nil || *env.Session != c.session {
		return dispatch.ErrStaleSession
	}
	if !h.readyLocked(c) {
		return dispatch.ErrHandOffline
	}
	effectful, err := effectfulCmdEnvelope(env)
	if err != nil {
		return err
	}
	if effectful && !c.contractMatch {
		blockedByContract = true
		return dispatch.ErrContractMismatch
	}
	return c.writeEnvelope(env)
}

// HandSession 实现 dispatch.Sender:取某手当前会话与 bootId。
func (h *Hub) HandSession(handID string) (string, string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.active[handID]
	if c == nil || !h.readyLocked(c) {
		return "", "", false
	}
	return c.session, c.bootID, true
}

// HandContractMatch 返回当前 ready 活连接在 hello 时冻结的契约一致性结论。
// contractHash 仍不参与身份认证或握手拒绝；该读数只供 effectful 构造闸使用。
func (h *Hub) HandContractMatch(handID string) (bool, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.active[handID]
	if c == nil || !h.readyLocked(c) {
		return false, false
	}
	return c.contractMatch, true
}

// WithCurrentHandSession 把“复核当前 session/boot”与一个短提交回调线性化。
// 账号绑定用它封死 probe 结束到 SQLite 提交之间的最后一个顶替窗口。
// fn 只应执行短本地事务，不得做网络 I/O。
func (h *Hub) WithCurrentHandSession(handID, sessionID, bootID string, fn func() error) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.active[handID]
	if c == nil || c.session != sessionID || c.bootID != bootID || !h.readyLocked(c) {
		return false, nil
	}
	return true, fn()
}

// HandNegotiation 实现 dispatch.Sender：caps 与 features 分开返回，调用方不得互相推断。
func (h *Hub) HandNegotiation(handID string) ([]string, []string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.active[handID]
	if c == nil || !h.readyLocked(c) {
		return nil, nil, false
	}
	return append([]string(nil), c.caps...), append([]string(nil), c.features...), true
}

// HandWitness 只返回当前 ready 会话在 hello/最新 ping 中宣告的投递层
// 证词状态。storeId 是判定 report=unknown/queued 能否证明零副作用的栅栏，
// 不是身份凭据。
func (h *Hub) HandWitness(handID string) (dispatch.HandWitness, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.active[handID]
	if c == nil || !h.readyLocked(c) || !containsString(c.features, string(protocol.FeatureWitness1)) ||
		c.witnessStoreID == "" {
		return dispatch.HandWitness{}, false
	}
	return dispatch.HandWitness{
		StoreID: c.witnessStoreID, OutboxPending: c.outboxPending, JournalOpen: c.journalOpen,
	}, true
}

// readyLocked 是所有业务派发入口共用的可用性判据。active 只表示物理链仍在，
// 只有注册表中同一 session/boot 仍为 ready 才能承接命令。调用方必须持有 h.mu；
// 锁序固定为 h.mu → Registry.mu，Registry 不反向调用 Hub。
func (h *Hub) readyLocked(c *Conn) bool {
	health, matched := h.reg.SessionHealth(c.handID, c.session, c.bootID)
	return matched && health == HealthReady
}

// CloseHand 实现 dispatch.Sender:ackTimeout 的唯一动作——仅关闭产生超时证词的
// expectedSession。若其间新 hello 已顶替则 no-op，绝不能用旧命令误关新链。
// 不发 bye(纯关闭,手据 WS close 重连,§7.2.1)。
func (h *Hub) CloseHand(handID, expectedSession, reason string) bool {
	h.mu.Lock()
	c := h.active[handID]
	if c == nil || c.session != expectedSession {
		h.mu.Unlock()
		return false
	}
	delete(h.active, handID)
	h.mu.Unlock()
	h.reg.Offline(handID, c.session, c.bootID)
	h.st.Audit("hand_closed", handID, "", reason)
	c.closeQuiet()
	return true
}

// HandOfflineMs 实现 dispatch.Sender:某手离线时长(毫秒);在线或无记录返回 0。
func (h *Hub) HandOfflineMs(handID string) int64 {
	s, ok := h.reg.Get(handID)
	if !ok || s.Online {
		return 0
	}
	return time.Since(s.LastHbAt).Milliseconds()
}

// StartHealthLoop:周期扫描,把心跳静默的手翻为 stalled 并告警(仅翻转沿一次)。
// 阻塞直到 ctx 取消;由 main 起一个 goroutine 驱动。
func (h *Hub) StartHealthLoop(ctx context.Context) {
	t := time.NewTicker(healthSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			h.runSweep(now)
		}
	}
}

// runSweep:一次健康扫描 + 告警。抽出供测试直接触发。
func (h *Hub) runSweep(now time.Time) {
	for _, stalled := range h.reg.Sweep(now) {
		// 连接开着却心跳静默——真异常,响亮告警(区别于连接干净关闭的静默暂停)。
		if h.closeStalled(stalled) {
			slog.Warn("手心跳静默,判定假死并关闭连接", "handId", stalled.HandID, "session", stalled.SessionID)
			h.st.Audit("hand_stalled", stalled.HandID, "", "连接在但心跳静默超 graceMs，已关闭该假死会话")
			continue
		}
		slog.Warn("手心跳静默,关链前会话已更替", "handId", stalled.HandID, "session", stalled.SessionID)
		h.st.Audit("hand_stalled", stalled.HandID, "", "连接在但心跳静默超 graceMs，关链前该会话已被顶替")
	}
}

// closeStalled 只摘除 Sweep 选中的那条会话。h.mu 把“复核当前 active → 摘除”
// 线性化：若新 hello 已经顶替，session 不匹配便 no-op；若这里先摘除，新 hello
// 随后可安全发布。Offline 也按 session+boot 加栅栏，绝不误清新注册表记录。
func (h *Hub) closeStalled(stalled StalledSession) bool {
	h.mu.Lock()
	c := h.active[stalled.HandID]
	if c == nil || c.session != stalled.SessionID || c.bootID != stalled.BootID {
		h.mu.Unlock()
		return false
	}
	health, matched := h.reg.SessionHealth(stalled.HandID, stalled.SessionID, stalled.BootID)
	if !matched || health != HealthStalled {
		h.mu.Unlock()
		return false
	}
	delete(h.active, stalled.HandID)
	h.mu.Unlock()

	h.reg.Offline(stalled.HandID, stalled.SessionID, stalled.BootID)
	c.closeQuiet()
	return true
}

type Conn struct {
	ws        *websocket.Conn
	hub       *Hub
	origin    string
	cancel    context.CancelFunc
	writeMu   sync.Mutex
	closeOnce sync.Once

	handID         string
	session        string
	bootID         string
	caps           []string
	features       []string
	contractHash   string
	contractMatch  bool
	extVersion     string
	witnessStoreID string
	outboxPending  int
	journalOpen    int
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
	ws.SetReadLimit(protocol.DefaultMaxMsgBytes)
	ctx, cancel := context.WithCancel(r.Context())
	c := &Conn{ws: ws, hub: h, origin: origin, cancel: cancel}
	defer cancel()
	defer func() {
		if c.handID != "" {
			if h.uninstall(c.handID, c) {
				// 只有实际摘掉当前单活连接者才能把注册表置离线；同 boot 顶替的旧
				// 连接结束不得误清新连接。
				h.reg.Offline(c.handID, c.session, c.bootID)
			}
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

// handshake:读 hello,校验，按手自带的 handId 幂等登记/复用后立即回 welcome。
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
	c.hub.frames.observe("in", "", env)
	var hello protocol.HelloBody
	if err := protocol.ValidateKindBody(protocol.KindHello, env.Body); err != nil {
		c.sendBye(ctx, protocol.ByeCodeProtoIncompatible, "hello body 非法")
		return false
	}
	_ = json.Unmarshal(env.Body, &hello)
	if !containsInt(hello.ProtoSupported, protocol.ProtoVersion) {
		c.sendBye(ctx, protocol.ByeCodeProtoIncompatible, "无共同协议版本")
		return false
	}
	c.bootID = hello.BootID
	c.caps = hello.Caps
	c.features = hello.Features
	c.contractHash = hello.ContractHash
	c.contractMatch = hello.ContractHash == protocol.ContractHash
	c.extVersion = hello.App.ExtVersion
	c.witnessStoreID = hello.WitnessStoreId
	c.outboxPending = hello.OutboxPending
	c.journalOpen = hello.JournalOpen
	if !c.contractMatch {
		slog.Warn("手契约指纹不一致,按 warn-only 继续", "origin", c.origin, "got", hello.ContractHash, "want", protocol.ContractHash)
		c.hub.st.Audit("contract_hash_mismatch", hello.HandID, env.MsgID, hello.ContractHash)
	}
	c.handID = hello.HandID
	_, created, previousOrigin, err := c.hub.st.RegisterHand(c.handID, c.origin, time.Now())
	if err != nil {
		slog.Error("本地手登记失败", "handId", c.handID, "err", err)
		c.hub.st.Audit("hand_registration_failed", c.handID, env.MsgID, err.Error())
		c.closeWithStatus(websocket.StatusInternalError, "hand registration failed")
		return false
	}
	if created {
		c.hub.st.Audit("hand_registered", c.handID, env.MsgID, c.origin)
	} else if previousOrigin != c.origin {
		// 本机信任模型下 Origin 不作第二重身份；Chrome 重装等变化只留软证词。
		c.hub.st.Audit("hand_origin_changed", c.handID, env.MsgID,
			fmt.Sprintf("%s -> %s", previousOrigin, c.origin))
		slog.Warn("手 Origin 变化,按本地信任模型继续", "handId", c.handID,
			"previous", previousOrigin, "current", c.origin)
	}
	return c.enterSession(ctx)
}

// enterSession:签发会话、装入活连接表(顶替旧)、发 welcome。
func (c *Conn) enterSession(ctx context.Context) bool {
	// 同手的两个连续 hello 不得交叉执行 OnReconnect：否则较旧的
	// 连接可在更新连接发布后，再用自己的 bootId 收编/终局化账本。
	// 此门栓不包住日常 SendEnvelope；OnReconnect 可正常回调 Hub 而不自锁。
	releaseTakeover := c.hub.lockTakeover(c.handID)
	defer releaseTakeover()
	// 在 welcome 对手可见之前占住派发门。否则手读到 welcome 后
	// 立即触发的新命令，可被紧随其后的重连扫描误当成旧在途命令再发。
	releaseDispatchGate := func() {}
	if c.hub.dispatcher != nil {
		releaseDispatchGate = c.hub.dispatcher.BeginHandTakeover(c.handID)
	}
	defer releaseDispatchGate()
	if err := c.invalidateSourcingFeedsForBootChange(time.Now()); err != nil {
		slog.Error("手换代前终止旧推荐流失败", "handId", c.handID, "err", err)
		c.closeWithStatus(websocket.StatusInternalError, "sourcing feed invalidation failed")
		return false
	}

	c.session = ids.NewSessionID()
	welcome := protocol.WelcomeBody{
		Session:       c.session,
		Proto:         protocol.ProtoVersion,
		Hb:            protocol.HbParams{IntervalMs: protocol.DefaultHbIntervalMs, GraceMs: protocol.DefaultHbGraceMs},
		Limits:        protocol.Limits{MaxMsgBytes: protocol.DefaultMaxMsgBytes, InlineBytes: protocol.DefaultInlineBytes},
		ContractMatch: c.contractMatch,
		Now:           time.Now().UnixMilli(),
		Sensors: &protocol.SensorParams{
			BadgeDebounceMs:        protocol.DefaultSensorsBadgeDebounceMs,
			BadgeMinEmitIntervalMs: protocol.DefaultSensorsBadgeMinEmitIntervalMs,
			ManualQuietMs:          protocol.DefaultSensorsManualQuietMs,
			NavSettleMs:            protocol.DefaultSensorsNavSettleMs,
		},
	}
	if c.hub.blobIssuer != nil {
		// token 熵源失败时宁缺勿滥:不带 blob 字段,本会话按未协商 blob 运行。
		if token := c.hub.blobIssuer.Rotate(c.handID); token != "" {
			welcome.Blob = &protocol.BlobParams{
				Endpoint: c.hub.blobEndpoint,
				Token:    token,
				MaxBytes: c.hub.blobMaxBytes,
			}
		}
	}
	old, err := c.hub.activate(c, func() error {
		return c.send(ctx, protocol.KindWelcome, nil, welcome)
	})
	if err != nil {
		return false
	}
	if old != nil && old != c {
		c.hub.st.Audit("superseded", c.handID, "", "新连接顶替旧连接")
		slog.Info("单活顶替", "handId", c.handID)
		old.closeWith(protocol.ByeCodeSuperseded, "superseded by new connection")
	}
	slog.Info("会话建立", "handId", c.handID, "session", c.session)
	// 重连收编:welcome 之后(手已能处理 session),对该手在途命令按 bootId 收编(§7.2)。
	// 首次登记无在途命令,天然 no-op。
	if c.hub.dispatcher != nil {
		c.hub.dispatcher.OnReconnectWitnessUnderGate(c.handID, c.bootID, c.witnessStoreID, c.outboxPending, c.journalOpen)
	}
	// 放在这里而不是 activate 内:那里持着 h.mu,锁序固定为 h.mu → Registry.mu,
	// 回调进去会把这条纪律撕开。到这一行时锁已释放、注册表已登记新 session/boot,
	// 观察者读到的就是最终态。
	if c.hub.handReadyHook != nil {
		c.hub.handReadyHook(c.handID)
	}
	return true
}

func (c *Conn) invalidateSourcingFeedsForBootChange(at time.Time) error {
	accounts, err := c.hub.st.AccountsBoundToHand(c.handID)
	if err != nil {
		return err
	}
	for i := range accounts {
		account := accounts[i]
		if account.IdentityBootID == "" || account.IdentityBootID == c.bootID {
			continue
		}
		if _, err := c.hub.st.InvalidateSourcingFeed(store.InvalidateSourcingFeedRequest{
			Platform: account.Platform, AccountRef: account.AccountRef,
			Trigger: "handBootChanged", At: at,
		}); err != nil {
			return err
		}
	}
	return nil
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

func (c *Conn) handleSessionFrame(ctx context.Context, data []byte) {
	env, err := decode(data)
	if err != nil {
		slog.Warn("会话帧解码失败", "handId", c.handID, "err", err)
		return
	}
	if !c.hub.isActive(c.handID, c) {
		c.hub.st.Audit("stale_connection_frame", c.handID, env.MsgID, "非当前单活连接的入站帧被拒绝")
		return
	}
	if !c.acceptsEnvelopeSession(env) {
		return
	}
	c.hub.frames.observe("in", c.handID, env)
	switch env.Kind {
	case protocol.KindPing:
		if err := protocol.ValidateKindBody(protocol.KindPing, env.Body); err != nil {
			c.hub.st.Audit("invalid_ping", c.handID, env.MsgID, err.Error())
			return
		}
		var ping protocol.PingBody
		_ = json.Unmarshal(env.Body, &ping)
		c.hub.updateWitness(c, ping)
		c.hub.reg.HeartbeatReport(c.handID, c.session, c.bootID, ping, time.Now())
		if c.hub.dispatcher != nil {
			c.hub.dispatcher.OnWitnessHeartbeat(c.handID, c.session, c.bootID,
				ping.WitnessStoreId, ping.OutboxPending, ping.JournalOpen)
		}
		c.sendPong(ctx, env.Session)
	case protocol.KindAck:
		var ab protocol.AckBody
		if err := protocol.ValidateKindBody(protocol.KindAck, env.Body); err == nil && json.Unmarshal(env.Body, &ab) == nil && c.hub.dispatcher != nil {
			c.hub.dispatcher.OnAck(c.handID, ab)
		}
	case protocol.KindResult:
		var rb protocol.ResultBody
		if err := protocol.ValidateKindBody(protocol.KindResult, env.Body); err == nil && json.Unmarshal(env.Body, &rb) == nil && c.hub.dispatcher != nil {
			c.hub.dispatcher.OnResult(c.handID, env.MsgID, rb)
		}
	case protocol.KindProgress:
		if !containsString(c.features, string(protocol.FeatureProgress1)) {
			c.hub.st.Audit("progress_unnegotiated", c.handID, env.MsgID, string(protocol.FeatureProgress1))
			return
		}
		if err := protocol.ValidateKindBody(protocol.KindProgress, env.Body); err != nil {
			c.hub.st.Audit("progress_invalid", c.handID, env.MsgID, err.Error())
			return
		}
		var progress protocol.ProgressBody
		_ = json.Unmarshal(env.Body, &progress)
		if c.hub.dispatcher != nil && c.hub.dispatcher.OnProgress(c.handID, progress) {
			// 只有 ref/hand/accepted/活租约全部通过的 progress 才兼作链路活性证词。
			c.hub.reg.Heartbeat(c.handID, c.session, c.bootID, time.Now())
		}
	case protocol.KindEvent:
		c.handleEvent(env)
	case protocol.KindReport:
		if !containsString(c.features, string(protocol.FeatureWitness1)) {
			c.hub.st.Audit("report_unnegotiated", c.handID, env.MsgID, string(protocol.FeatureWitness1))
			return
		}
		if err := protocol.ValidateKindBody(protocol.KindReport, env.Body); err != nil {
			c.hub.st.Audit("report_invalid", c.handID, env.MsgID, err.Error())
			return
		}
		var report protocol.ReportBody
		_ = json.Unmarshal(env.Body, &report)
		if c.hub.dispatcher != nil {
			c.hub.dispatcher.OnReport(c.handID, env.MsgID, c.session, c.bootID, report)
		}
	default:
		slog.Debug("暂不处理的会话帧", "handId", c.handID, "kind", env.Kind)
	}
}

func (h *Hub) updateWitness(c *Conn, ping protocol.PingBody) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active[c.handID] != c || !containsString(c.features, string(protocol.FeatureWitness1)) {
		return
	}
	// generated validator 已强制 witness 三字段全有或全无。宣告了
	// witness/1 的手若 ping 故意省略，不用零值覆盖最后一份证词。
	if ping.WitnessStoreId == "" {
		return
	}
	c.witnessStoreID = ping.WitnessStoreId
	c.outboxPending = ping.OutboxPending
	c.journalOpen = ping.JournalOpen
}

// acceptsEnvelopeSession 把 session 围栏放在 kind 语义层：ping/event/ack 等
// 会话控制帧只认当前 session；已经 accepted 的 QueueItem 跨同 boot 会话继续执行，
// 它产生的 progress/result 按协议保留创建时 session，故只要求非空。两者仍先经过
// isActive 硬栅栏，旧 socket 无法借历史 session 投递。
func (c *Conn) acceptsEnvelopeSession(env *protocol.Envelope) bool {
	if env.Session != nil {
		switch env.Kind {
		case protocol.KindProgress, protocol.KindResult:
			return true
		default:
			if *env.Session == c.session {
				return true
			}
		}
	}
	c.hub.st.Audit("stale_session_frame", c.handID, env.MsgID,
		fmt.Sprintf("入站 %s 的 session 与当前连接不符", env.Kind))
	return false
}

func (c *Conn) sendPong(ctx context.Context, session *string) {
	_ = c.send(ctx, protocol.KindPong, session, protocol.PongBody{Now: time.Now().UnixMilli()})
}

func (c *Conn) sendBye(ctx context.Context, code protocol.ByeCode, msg string) {
	_ = c.send(ctx, protocol.KindBye, nil, protocol.ByeBody{Code: code, Message: msg})
	c.closeQuiet()
}

// ---------- 收发底层 ----------

// send:脑主动构造一条消息(生成 msgId)并发送。ctx 参数保留以标注调用上下文,
// 实际写超时由 writeEnvelope 自带(连接关闭时 ws.Write 自会失败)。
func (c *Conn) send(_ context.Context, kind protocol.Kind, session *string, body any) error {
	raw, err := protocol.Encode(body)
	if err != nil {
		return err
	}
	return c.writeEnvelope(protocol.Envelope{
		Proto: protocol.ProtoVersion, Kind: kind, MsgID: ids.NewMsgID(),
		Session: session, Ts: time.Now().UnixMilli(), Attempt: 1, Body: raw,
	})
}

// writeEnvelope:发送一条已构造的信封(供 send 与 dispatch.Sender 共用)。
func (c *Conn) writeEnvelope(env protocol.Envelope) error {
	buf, err := json.Marshal(env)
	if err != nil {
		return err
	}
	if err := protocol.ValidateFrameSize(buf, protocol.DefaultMaxMsgBytes); err != nil {
		return err
	}
	c.hub.frames.observe("out", c.handID, &env)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageText, buf); err != nil {
		slog.Debug("写帧失败", "handId", c.handID, "kind", env.Kind, "err", err)
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

func (c *Conn) closeWithStatus(status websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
		_ = c.ws.Close(status, reason)
	})
}

// ---------- 活连接表(单活顶替) ----------

// activate 先把 welcome 成功写给新手，再在同一个 h.mu 临界区内登记 ready
// 并发布新 active。因此调度器既不会在 welcome 之前向新 session 发 cmd，
// 也看不到 active 已换而注册表尚未就绪的半态。publish 只允许做本连接的
// 有界 socket 写，不得回调 Hub/派发器（锁序固定为 h.mu → Registry.mu / c.writeMu）。
func (h *Hub) activate(c *Conn, publish func() error) (*Conn, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := publish(); err != nil {
		return nil, err
	}
	old := h.active[c.handID]
	h.reg.OnlineWithBuild(
		c.handID, c.session, c.bootID, c.caps, c.features,
		c.contractHash, c.contractMatch, c.extVersion, time.Now(),
	)
	h.active[c.handID] = c
	return old, nil
}

// lockTakeover 是按 handId 分片的短生命周期门栓。refs 在等待 gate.mu 前
// 已增加，因此有等待者时绝不会删除 gate 并造出同 handId 的第二把锁。
func (h *Hub) lockTakeover(handID string) func() {
	h.takeoverMu.Lock()
	gate := h.takeovers[handID]
	if gate == nil {
		gate = &takeoverGate{}
		h.takeovers[handID] = gate
	}
	gate.refs++
	h.takeoverMu.Unlock()

	gate.mu.Lock()
	return func() {
		gate.mu.Unlock()
		h.takeoverMu.Lock()
		gate.refs--
		if gate.refs == 0 {
			delete(h.takeovers, handID)
		}
		h.takeoverMu.Unlock()
	}
}

func (h *Hub) uninstall(handID string, c *Conn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active[handID] == c {
		delete(h.active, handID)
		return true
	}
	return false
}

func (h *Hub) isActive(handID string, c *Conn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.active[handID] == c
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
	if err := protocol.ValidateFrameSize(data, protocol.DefaultMaxMsgBytes); err != nil {
		return nil, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if env.Proto != protocol.ProtoVersion {
		return nil, errors.New("proto 版本不符")
	}
	return &env, nil
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// effectfulCmdEnvelope 从信封自己的 generated CmdBody/原语元数据判类，避免
// 最终 socket 闸信任某个可漏传或误传的调用方布尔值。
func effectfulCmdEnvelope(env protocol.Envelope) (bool, error) {
	if env.Kind != protocol.KindCmd {
		return false, nil
	}
	var body protocol.CmdBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		return false, fmt.Errorf("解析待发送 cmd 以复核契约一致性: %w", err)
	}
	meta, ok := protocol.Primitives[body.Name]
	if !ok || meta.Ver == 0 || body.Ver != meta.Ver {
		return false, fmt.Errorf("待发送 cmd 原语未知或版本不符: %s@%d", body.Name, body.Ver)
	}
	return meta.Class == protocol.ClassEffectful, nil
}
