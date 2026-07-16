package session

import (
	"sync"
	"time"
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

// HandState:手注册表里一条(内存;权威账本另在 SQLite)。
type HandState struct {
	HandID    string
	Online    bool
	BootID    string
	Caps      []string
	LastHbAt  time.Time // 最近一次 ping 到达
	SessionAt time.Time // 本会话建立时刻
	Health    Health
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
func (r *Registry) Online(handID, bootID string, caps []string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states[handID] = &HandState{
		HandID: handID, Online: true, BootID: bootID, Caps: caps,
		LastHbAt: now, SessionAt: now, Health: HealthReady,
	}
}

// Offline:连接结束时下线。仅当传入的 conn 仍是登记的那一只(bootID 匹配)才真正下线,
// 避免"旧连接被顶替后延迟触发 Offline"误清掉新连接。
func (r *Registry) Offline(handID, bootID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.states[handID]; ok && s.BootID == bootID {
		s.Online = false
		s.Health = HealthOffline
	}
}

// Heartbeat:收到 ping 刷新活性。返回 false 表示该手当前无在线记录(忽略)。
func (r *Registry) Heartbeat(handID, bootID string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.states[handID]
	if !ok || !s.Online || s.BootID != bootID {
		return false
	}
	s.LastHbAt = now
	s.Health = HealthReady
	return true
}

// Sweep:巡检所有在线手,把心跳静默超 graceMs 的标记为 stalled。
// 返回本次由 ready 翻转为 stalled 的手(供告警;只在翻转沿告警一次,不重复刷屏)。
func (r *Registry) Sweep(now time.Time) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var newlyStalled []string
	for _, s := range r.states {
		if !s.Online {
			continue
		}
		silentMs := now.Sub(s.LastHbAt).Milliseconds()
		if silentMs > r.graceMs {
			if s.Health != HealthStalled {
				s.Health = HealthStalled
				newlyStalled = append(newlyStalled, s.HandID)
			}
		}
	}
	return newlyStalled
}

// Snapshot:注册表当前视图(状态页/测试);返回拷贝。
func (r *Registry) Snapshot() []HandState {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]HandState, 0, len(r.states))
	for _, s := range r.states {
		cp := *s
		cp.Caps = append([]string(nil), s.Caps...)
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
	return *s, true
}
