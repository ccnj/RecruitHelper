package session

import (
	"testing"
	"time"
)

func TestRegistrySweepStallOnceThenRecover(t *testing.T) {
	r := NewRegistry(1000) // grace 1s
	t0 := time.Now()
	r.Online("hand-01", "s-1", "b-1", []string{"debug.ping@1"}, nil, t0)

	// 未超 grace:不 stall
	if s := r.Sweep(t0.Add(500 * time.Millisecond)); len(s) != 0 {
		t.Fatalf("未超 grace 不应 stall: %v", s)
	}
	// 超 grace:翻转一次
	s := r.Sweep(t0.Add(1500 * time.Millisecond))
	if len(s) != 1 || s[0] != (StalledSession{HandID: "hand-01", SessionID: "s-1", BootID: "b-1"}) {
		t.Fatalf("应翻转 stall 一次: %v", s)
	}
	// 再 sweep 不重复告警(只在翻转沿)
	if s := r.Sweep(t0.Add(2000 * time.Millisecond)); len(s) != 0 {
		t.Fatalf("stall 不应重复告警: %v", s)
	}
	if st, _ := r.Get("hand-01"); st.Health != HealthStalled {
		t.Fatalf("应为 stalled,得到 %s", st.Health)
	}
	// stalled 会话已被健康巡检判死，迟到心跳不能在关链窗口内复活它。
	if r.Heartbeat("hand-01", "s-1", "b-1", t0.Add(2100*time.Millisecond)) {
		t.Fatalf("stalled 会话不应被迟到心跳复活")
	}
	if st, _ := r.Get("hand-01"); st.Health != HealthStalled {
		t.Fatalf("迟到心跳后仍应 stalled,得到 %s", st.Health)
	}
	// 只有新会话上线才恢复 ready。
	r.Online("hand-01", "s-2", "b-1", nil, nil, t0.Add(2200*time.Millisecond))
	if st, _ := r.Get("hand-01"); st.Health != HealthReady || st.SessionID != "s-2" {
		t.Fatalf("新会话应恢复 ready,得到 %+v", st)
	}
}

func TestRegistryOfflineAndHeartbeatRequireExactSessionEvenSameBoot(t *testing.T) {
	r := NewRegistry(1000)
	t0 := time.Now()
	r.Online("hand-01", "s-old", "b-same", nil, nil, t0)
	r.Online("hand-01", "s-new", "b-same", nil, nil, t0.Add(time.Millisecond))
	// 同 boot 顶替后，旧 session 的迟到心跳和 Offline 都不得污染新连接。
	if r.Heartbeat("hand-01", "s-old", "b-same", t0.Add(time.Second)) {
		t.Fatal("旧 session 心跳不应命中新会话")
	}
	r.Offline("hand-01", "s-old", "b-same")
	if s, _ := r.Get("hand-01"); !s.Online {
		t.Fatalf("旧 session Offline 不应下线新连接")
	}
	// session+boot 都匹配才下线。
	r.Offline("hand-01", "s-new", "b-same")
	if s, _ := r.Get("hand-01"); s.Online {
		t.Fatalf("完整会话匹配应下线")
	}
	// 离线手不参与 stall 告警
	if s := r.Sweep(t0.Add(5000 * time.Millisecond)); len(s) != 0 {
		t.Fatalf("离线手不应 stall 告警: %v", s)
	}
}

func TestRegistryHeartbeatUnknownHand(t *testing.T) {
	r := NewRegistry(1000)
	if r.Heartbeat("nope", "s-x", "b-x", time.Now()) {
		t.Fatalf("未知手心跳应返回 false")
	}
}

func TestRegistrySweepStallsAtExactGraceBoundary(t *testing.T) {
	r := NewRegistry(1000)
	t0 := time.Now()
	r.Online("hand-boundary", "session-boundary", "boot-boundary", nil, nil, t0)
	stalled := r.Sweep(t0.Add(time.Second))
	if len(stalled) != 1 || stalled[0].SessionID != "session-boundary" {
		t.Fatalf("静默恰好 graceMs 必须 stalled: %+v", stalled)
	}
}
