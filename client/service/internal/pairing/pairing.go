// Package pairing:配对窗管理与工牌签发(协议规格 §2.2)。
//
// 配对是异步的:手发无 token 的 hello 后挂起,注册为"待配对";用户在客户端 UI
// 于 60 秒窗口内确认后,脑签发 handId+token 经该连接下发。窗口关闭(超时或手动)
// 时所有待配对者收到取消。待配对项以 Origin+bootId 为键,支持配对等待中 SW 死亡
// 复活重连后命中同键恢复(规格 §2.2.8;骨架期 Go 假手不触发,机制先就位)。
package pairing

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/internal/ids"
)

var (
	ErrWindowClosed = errors.New("配对窗未开启")
	ErrNoSuchWaiter = errors.New("无此待配对项")
)

// Creds:签发给手的工牌。
type Creds struct {
	HandID string
	Auth   string
}

// HelloInfo:待配对项展示给 UI 的信息(供用户判断是否确认)。
type HelloInfo struct {
	ExtVersion string
	Caps       []string
}

type waiter struct {
	origin    string
	bootID    string
	hello     HelloInfo
	confirm   chan Creds // 签发后经此通知等待中的握手 goroutine
	createdAt time.Time
}

// PendingView:管理端点展示用。
type PendingView struct {
	Origin     string   `json:"origin"`
	BootID     string   `json:"bootId"`
	ExtVersion string   `json:"extVersion"`
	Caps       []string `json:"caps"`
	WaitingMs  int64    `json:"waitingMs"`
}

type Manager struct {
	st *store.Store

	mu        sync.Mutex
	winCtx    context.Context // 非 nil 且未 Done 表示窗口开启
	winCancel context.CancelFunc
	winUntil  time.Time
	waiters   map[string]*waiter // key = origin|bootId
}

func New(st *store.Store) *Manager {
	return &Manager{st: st, waiters: map[string]*waiter{}}
}

func key(origin, bootID string) string { return origin + "|" + bootID }

// OpenWindow 开启(或续期)配对窗。
func (m *Manager) OpenWindow(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.winCancel != nil {
		m.winCancel() // 关旧窗(清掉旧等待者),重开
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.winCtx, m.winCancel, m.winUntil = ctx, cancel, time.Now().Add(d)
	m.waiters = map[string]*waiter{}
	// 到点自动关窗
	t := time.AfterFunc(d, m.CloseWindow)
	go func() { <-ctx.Done(); t.Stop() }()
}

// CloseWindow 关闭配对窗,取消所有待配对(它们的 winCtx.Done 触发,握手 goroutine 发 PAIRING_TIMEOUT)。
func (m *Manager) CloseWindow() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.winCancel != nil {
		m.winCancel()
		m.winCancel, m.winCtx = nil, nil
		m.waiters = map[string]*waiter{}
	}
}

// WindowOpen 报告窗口是否开启(供管理端点/日志)。
func (m *Manager) WindowOpen() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.winCtx != nil && m.winCtx.Err() == nil
}

// Register:注册一个待配对项。返回 confirm 通道与窗口 ctx。
// 窗未开返回 ErrWindowClosed(握手方据此发 bye(AUTH_FAILED))。
func (m *Manager) Register(origin, bootID string, hello HelloInfo) (<-chan Creds, context.Context, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.winCtx == nil || m.winCtx.Err() != nil {
		return nil, nil, ErrWindowClosed
	}
	w := &waiter{origin: origin, bootID: bootID, hello: hello, confirm: make(chan Creds, 1), createdAt: time.Now()}
	m.waiters[key(origin, bootID)] = w
	return w.confirm, m.winCtx, nil
}

// Pending 列出当前待配对项(供 UI)。
func (m *Manager) Pending() []PendingView {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PendingView, 0, len(m.waiters))
	for _, w := range m.waiters {
		out = append(out, PendingView{
			Origin: w.origin, BootID: w.bootID,
			ExtVersion: w.hello.ExtVersion, Caps: w.hello.Caps,
			WaitingMs: time.Since(w.createdAt).Milliseconds(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BootID < out[j].BootID })
	return out
}

// Confirm:用户确认某待配对项 → 签发工牌、落库、经 confirm 通道通知握手方。
func (m *Manager) Confirm(origin, bootID string) (Creds, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.waiters[key(origin, bootID)]
	if !ok {
		return Creds{}, ErrNoSuchWaiter
	}
	handID, err := m.nextHandIDLocked()
	if err != nil {
		return Creds{}, err
	}
	token := ids.NewToken()
	now := time.Now()
	if err := m.st.UpsertHand(&store.PairedHand{
		HandID: handID, TokenHash: store.HashToken(token),
		Origin: origin, Label: handID, CreatedAt: now, LastSeenAt: now,
	}); err != nil {
		return Creds{}, fmt.Errorf("落库工牌: %w", err)
	}
	creds := Creds{HandID: handID, Auth: token}
	w.confirm <- creds
	delete(m.waiters, key(origin, bootID))
	return creds, nil
}

// nextHandIDLocked:生成下一个 hand-NN(基于已有编号最大值+1)。调用方持锁。
func (m *Manager) nextHandIDLocked() (string, error) {
	hands, err := m.st.Hands()
	if err != nil {
		return "", err
	}
	max := 0
	for _, h := range hands {
		if n, ok := strings.CutPrefix(h.HandID, "hand-"); ok {
			if v, err := strconv.Atoi(n); err == nil && v > max {
				max = v
			}
		}
	}
	return fmt.Sprintf("hand-%02d", max+1), nil
}
