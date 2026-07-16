package session

import (
	"testing"
	"time"
)

func TestRegistrySweepStallOnceThenRecover(t *testing.T) {
	r := NewRegistry(1000) // grace 1s
	t0 := time.Now()
	r.Online("hand-01", "b-1", []string{"debug.ping@1"}, t0)

	// 未超 grace:不 stall
	if s := r.Sweep(t0.Add(500 * time.Millisecond)); len(s) != 0 {
		t.Fatalf("未超 grace 不应 stall: %v", s)
	}
	// 超 grace:翻转一次
	s := r.Sweep(t0.Add(1500 * time.Millisecond))
	if len(s) != 1 || s[0] != "hand-01" {
		t.Fatalf("应翻转 stall 一次: %v", s)
	}
	// 再 sweep 不重复告警(只在翻转沿)
	if s := r.Sweep(t0.Add(2000 * time.Millisecond)); len(s) != 0 {
		t.Fatalf("stall 不应重复告警: %v", s)
	}
	if st, _ := r.Get("hand-01"); st.Health != HealthStalled {
		t.Fatalf("应为 stalled,得到 %s", st.Health)
	}
	// 心跳恢复 → ready
	if !r.Heartbeat("hand-01", "b-1", t0.Add(2100*time.Millisecond)) {
		t.Fatalf("心跳应刷新")
	}
	if st, _ := r.Get("hand-01"); st.Health != HealthReady {
		t.Fatalf("心跳后应 ready,得到 %s", st.Health)
	}
	// 再度静默 → 再 stall
	if s := r.Sweep(t0.Add(4000 * time.Millisecond)); len(s) != 1 {
		t.Fatalf("恢复后再静默应再 stall: %v", s)
	}
}

func TestRegistryOfflineBootMatch(t *testing.T) {
	r := NewRegistry(1000)
	t0 := time.Now()
	r.Online("hand-01", "b-A", nil, t0)
	// bootID 不匹配(被顶替后旧连接迟到的 Offline)不应下线新连接
	r.Offline("hand-01", "b-B")
	if s, _ := r.Get("hand-01"); !s.Online {
		t.Fatalf("bootID 不匹配不应下线")
	}
	// 匹配才下线
	r.Offline("hand-01", "b-A")
	if s, _ := r.Get("hand-01"); s.Online {
		t.Fatalf("bootID 匹配应下线")
	}
	// 离线手不参与 stall 告警
	if s := r.Sweep(t0.Add(5000 * time.Millisecond)); len(s) != 0 {
		t.Fatalf("离线手不应 stall 告警: %v", s)
	}
}

func TestRegistryHeartbeatUnknownHand(t *testing.T) {
	r := NewRegistry(1000)
	if r.Heartbeat("nope", "b-x", time.Now()) {
		t.Fatalf("未知手心跳应返回 false")
	}
}
