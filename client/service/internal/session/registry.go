package session

import (
	"log/slog"
	"strconv"
	"sync"
	"time"

	"recruithelper/contract/gen/go/protocol"
)

// Health:手的健康档(协议规格 §11)。骨架期两级:链路 + 行为能力。
// 页面在场(能力级)随感知批次 [S] 接入。
type Health string

const (
	// HealthOffline:无活连接。SW 正常死亡/网络断都归此,静默暂停派发,不告警(设计内常态)。
	HealthOffline Health = "offline"
	// HealthReady:连接在且心跳新鲜。
	HealthReady Health = "ready"
	// HealthStalled:连接开着但心跳静默超 graceMs——真异常(事件循环卡死/心跳代码坏),告警。
	HealthStalled Health = "stalled"
)

// CapabilityHealth 是页面在场与传感器报告的能力级健康，不与 WS 链路健康混为一谈。
type CapabilityHealth string

const (
	CapabilityUnknown  CapabilityHealth = "unknown"
	CapabilityReady    CapabilityHealth = "ready"
	CapabilityDegraded CapabilityHealth = "degraded"
)

// HandState:手注册表里一条(内存;权威账本另在 SQLite)。
type HandState struct {
	HandID    string
	Online    bool
	SessionID string
	BootID    string
	// 构建信息来自当前 hello，只用于部署就绪确认与诊断，不参与身份认证。
	ContractHash  string
	ContractMatch bool
	ExtVersion    string
	Caps          []string
	Features      []string
	LastHbAt      time.Time // 最近一次 ping 到达
	SessionAt     time.Time // 本会话建立时刻
	Health        Health

	Contexts     []protocol.PingContext
	Sensors      *protocol.PingSensors
	PageHealth   CapabilityHealth
	SensorHealth CapabilityHealth
	LastSensorAt time.Time
}

// Registry:内存手注册表。回答"哪只手在线、健康如何、报了什么能力"。
type Registry struct {
	mu      sync.Mutex
	graceMs int64
	states  map[string]*HandState
}

func NewRegistry(graceMs int64) *Registry {
	return &Registry{graceMs: graceMs, states: map[string]*HandState{}}
}

// Online:会话建立时登记(或顶替后刷新)。
func (r *Registry) Online(handID, sessionID, bootID string, caps, features []string, now time.Time) {
	r.OnlineWithBuild(handID, sessionID, bootID, caps, features, "", false, "", now)
}

// OnlineWithBuild 额外保留 hello 的构建证词，供 §14 管理端确认新 SW 已按
// 当前脑契约上线。旧测试/调用可继续使用 Online，不伪造匹配结论。
func (r *Registry) OnlineWithBuild(
	handID, sessionID, bootID string,
	caps, features []string,
	contractHash string,
	contractMatch bool,
	extVersion string,
	now time.Time,
) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states[handID] = &HandState{
		HandID: handID, Online: true, SessionID: sessionID, BootID: bootID,
		ContractHash: contractHash, ContractMatch: contractMatch, ExtVersion: extVersion,
		Caps: append([]string(nil), caps...), Features: append([]string(nil), features...),
		LastHbAt: now, SessionAt: now, Health: HealthReady,
		PageHealth: CapabilityUnknown, SensorHealth: CapabilityUnknown,
	}
}

// Offline:连接结束时下线。仅当 session+boot 都匹配才真正下线，
// 避免同 boot 顶替时旧连接的延迟清理误伤新连接。
func (r *Registry) Offline(handID, sessionID, bootID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.states[handID]; ok && s.SessionID == sessionID && s.BootID == bootID {
		s.Online = false
		s.Health = HealthOffline
	}
}

// Heartbeat:收到 ping 刷新活性。返回 false 表示该手当前无在线记录(忽略)。
func (r *Registry) Heartbeat(handID, sessionID, bootID string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.states[handID]
	if !ok || !s.Online || s.Health != HealthReady || s.SessionID != sessionID || s.BootID != bootID {
		return false
	}
	s.LastHbAt = now
	s.Health = HealthReady
	return true
}

// HeartbeatReport 原子刷新链路 lastSeen 与 ping 搭车的页面/传感器缓存。
func (r *Registry) HeartbeatReport(handID, sessionID, bootID string, ping protocol.PingBody, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.states[handID]
	if !ok || !s.Online || s.Health != HealthReady || s.SessionID != sessionID || s.BootID != bootID {
		return false
	}
	s.LastHbAt = now
	s.Health = HealthReady
	s.Contexts = append([]protocol.PingContext(nil), ping.Contexts...)
	// 未读读数是插队判定的唯一输入，而它走"手主动推送、脑不得反向拉取"的提示
	// 通道：采样触发、双读一致、值必须变化、SW 缓存、心跳周期，任何一环断掉都
	// 表现为同一句"读不到"。这里只记变化沿——读数何时塌、塌成什么、何时恢复
	// ——是事后判断"插队为何没发生"的第一手证据。不变则不打，稳态近乎零噪音。
	prevUnread, prevSensor, prevPage := unreadReadingText(s.Sensors), s.SensorHealth, s.PageHealth
	s.PageHealth = capabilityPageHealth(s.Contexts)
	if ping.Sensors != nil {
		cp := *ping.Sensors
		if ping.Sensors.UnreadTotal != nil {
			reading := *ping.Sensors.UnreadTotal
			cp.UnreadTotal = &reading
		}
		s.Sensors = &cp
		s.LastSensorAt = now
		if cp.UnreadTotal != nil {
			s.SensorHealth = CapabilityReady
		} else {
			s.SensorHealth = CapabilityDegraded
		}
	} else {
		s.Sensors = nil
		s.LastSensorAt = now
		if s.PageHealth == CapabilityReady {
			s.SensorHealth = CapabilityDegraded
		} else {
			s.SensorHealth = CapabilityUnknown
		}
	}
	if next := unreadReadingText(s.Sensors); next != prevUnread ||
		s.SensorHealth != prevSensor || s.PageHealth != prevPage {
		slog.Info("未读读数变化",
			"handId", handID,
			"from", prevUnread, "to", next,
			"sensorHealth", string(s.SensorHealth), "pageHealth", string(s.PageHealth),
			"contexts", len(s.Contexts))
	}
	return true
}

// unreadReadingText 把读数渲染成可 grep 的短文本；缺席与零是两件事，必须
// 分得开——"读不到"会让插队判定直接放弃，"零"则是有效的清空信号。
func unreadReadingText(sensors *protocol.PingSensors) string {
	if sensors == nil {
		return "无传感"
	}
	if sensors.UnreadTotal == nil {
		return "读不到"
	}
	return strconv.Itoa(sensors.UnreadTotal.Value)
}

func capabilityPageHealth(contexts []protocol.PingContext) CapabilityHealth {
	if len(contexts) == 0 {
		return CapabilityUnknown
	}
	for _, c := range contexts {
		if !c.Ready {
			return CapabilityDegraded
		}
	}
	return CapabilityReady
}

// StalledSession 是 Sweep 翻转沿的会话级证词。关链时必须同时匹配
// handId/sessionId/bootId，不能只凭 handId 关掉在 sweep 后刚顶替上来的新链。
type StalledSession struct {
	HandID    string
	SessionID string
	BootID    string
}

// Sweep:巡检所有在线手,把心跳静默超 graceMs 的标记为 stalled。
// 返回本次由 ready 翻转为 stalled 的会话证词(只在翻转沿告警一次)。
func (r *Registry) Sweep(now time.Time) []StalledSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	var newlyStalled []StalledSession
	for _, s := range r.states {
		if !s.Online {
			continue
		}
		silentMs := now.Sub(s.LastHbAt).Milliseconds()
		if silentMs >= r.graceMs {
			if s.Health != HealthStalled {
				s.Health = HealthStalled
				newlyStalled = append(newlyStalled, StalledSession{
					HandID: s.HandID, SessionID: s.SessionID, BootID: s.BootID,
				})
			}
		}
	}
	return newlyStalled
}

// SessionHealth 按完整会话身份读链路健康。matched=false 表示该证词
// 已被新 session/boot 替代，调用方不得对当前链做任何动作。
func (r *Registry) SessionHealth(handID, sessionID, bootID string) (health Health, matched bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.states[handID]
	if !ok || s.SessionID != sessionID || s.BootID != bootID {
		return HealthOffline, false
	}
	return s.Health, true
}

// Snapshot:注册表当前视图(状态页/测试);返回拷贝。
func (r *Registry) Snapshot() []HandState {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]HandState, 0, len(r.states))
	for _, s := range r.states {
		cp := *s
		cp.Caps = append([]string(nil), s.Caps...)
		cp.Features = append([]string(nil), s.Features...)
		cp.Contexts = append([]protocol.PingContext(nil), s.Contexts...)
		cp.Sensors = cloneSensors(s.Sensors)
		out = append(out, cp)
	}
	return out
}

// Get:取单只手的状态拷贝;第二返回值表示是否存在。
func (r *Registry) Get(handID string) (HandState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.states[handID]
	if !ok {
		return HandState{}, false
	}
	cp := *s
	cp.Caps = append([]string(nil), s.Caps...)
	cp.Features = append([]string(nil), s.Features...)
	cp.Contexts = append([]protocol.PingContext(nil), s.Contexts...)
	cp.Sensors = cloneSensors(s.Sensors)
	return cp, true
}

func cloneSensors(in *protocol.PingSensors) *protocol.PingSensors {
	if in == nil {
		return nil
	}
	out := *in
	if in.UnreadTotal != nil {
		reading := *in.UnreadTotal
		out.UnreadTotal = &reading
	}
	return &out
}
