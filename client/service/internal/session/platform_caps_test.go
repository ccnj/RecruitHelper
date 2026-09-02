package session

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"

	"recruithelper/contract/gen/go/protocol"
)

func connectHandWithPlatforms(t *testing.T, h *harness, handID, bootID string, platforms []protocol.HelloPlatform) *websocket.Conn {
	t.Helper()
	c := dial(t, h.wsURL, testOrigin)
	raw, err := protocol.Encode(protocol.HelloBody{
		HandID: handID, BootID: bootID,
		ProtoSupported: []int{protocol.ProtoVersion},
		App:            protocol.AppInfo{ExtVersion: "0.1.0", Browser: "test"},
		Caps:           []string{"debug.ping@1", "probe.platform@1", "chat.readThread@1"},
		Features:       []string{},
		ContractHash:   protocol.ContractHash,
		Platforms:      platforms,
	})
	if err != nil {
		t.Fatalf("encode hello: %v", err)
	}
	sendHelloBody(t, c, raw)
	env := readEnv(t, c)
	if env.Kind != protocol.KindWelcome {
		t.Fatalf("hello 应立即收到 welcome，实际 %s", env.Kind)
	}
	var welcome protocol.WelcomeBody
	if err := json.Unmarshal(env.Body, &welcome); err != nil || welcome.Session == "" {
		t.Fatalf("welcome 会话非法: %+v err=%v", welcome, err)
	}
	return c
}

// hello 按平台声明能力(2026-09-02 甲方裁决,规格 §4.1):声明的平台表按平台可查,
// 未声明的平台 declared=false 让派发闸回落并集;重复 id 视为未声明并留痕。
func TestHandPlatformCapsFollowsHelloDeclaration(t *testing.T) {
	h := newHarness(t)
	handID := "hand-platforms"
	c := connectHandWithPlatforms(t, h, handID, "boot-1", []protocol.HelloPlatform{
		{Id: "zhilian", Caps: []string{"debug.ping@1", "probe.platform@1", "chat.readThread@1"}},
		{Id: "boss", Caps: []string{"debug.ping@1", "probe.platform@1"}},
	})
	defer c.Close(websocket.StatusNormalClosure, "")

	caps, declared, ok := h.hub.HandPlatformCaps(handID, "boss")
	if !ok || !declared || len(caps) != 2 || caps[1] != "probe.platform@1" {
		t.Fatalf("boss 平台表应按 hello 声明返回: caps=%v declared=%v ok=%v", caps, declared, ok)
	}
	caps[0] = "mutated"
	again, _, _ := h.hub.HandPlatformCaps(handID, "boss")
	if again[0] != "debug.ping@1" {
		t.Fatal("HandPlatformCaps 必须返回拷贝,调用方不得改到会话内表")
	}
	if _, declared, ok := h.hub.HandPlatformCaps(handID, "lagou"); !ok || declared {
		t.Fatalf("未声明的平台应 declared=false、ok=true: declared=%v ok=%v", declared, ok)
	}
	if got := h.hub.HandPlatforms(handID); len(got) != 2 || got[0] != "zhilian" || got[1] != "boss" {
		t.Fatalf("HandPlatforms 应保序返回声明的平台: %v", got)
	}
	if _, _, ok := h.hub.HandPlatformCaps("hand-unknown", "boss"); ok {
		t.Fatal("不在线的手不得暴露平台表")
	}
}

func TestHandPlatformCapsAbsentForLegacyHello(t *testing.T) {
	h := newHarness(t)
	handID := "hand-legacy"
	c := connectHand(t, h, handID, "boot-legacy")
	defer c.Close(websocket.StatusNormalClosure, "")

	if _, declared, ok := h.hub.HandPlatformCaps(handID, "zhilian"); !ok || declared {
		t.Fatalf("旧手不发 platforms:declared=false、ok=true 让派发闸回落并集: declared=%v ok=%v", declared, ok)
	}
	if got := h.hub.HandPlatforms(handID); got != nil {
		t.Fatalf("旧手 HandPlatforms 应为 nil(未声明): %v", got)
	}
}

func TestHandPlatformCapsDuplicateIDFallsBackToUnion(t *testing.T) {
	h := newHarness(t)
	handID := "hand-dup"
	c := connectHandWithPlatforms(t, h, handID, "boot-dup", []protocol.HelloPlatform{
		{Id: "boss", Caps: []string{"debug.ping@1"}},
		{Id: "boss", Caps: []string{"debug.ping@1", "probe.platform@1"}},
	})
	defer c.Close(websocket.StatusNormalClosure, "")

	if caps, declared, ok := h.hub.HandPlatformCaps(handID, "boss"); !ok || declared || caps != nil {
		t.Fatalf("同 id 重复声明应按未声明回落并集,不取第一条: caps=%v declared=%v ok=%v", caps, declared, ok)
	}
	if got := h.hub.HandPlatforms(handID); len(got) != 1 || got[0] != "boss" {
		t.Fatalf("HandPlatforms 去重: %v", got)
	}
}

func TestHandPlatformCapsHiddenWhenStalled(t *testing.T) {
	h := newHarnessGrace(t, 200)
	handID := "hand-platform-stalled"
	c := connectHandWithPlatforms(t, h, handID, "boot-stalled", []protocol.HelloPlatform{
		{Id: "boss", Caps: []string{"debug.ping@1"}},
	})
	defer c.Close(websocket.StatusNormalClosure, "")

	before, _ := h.hub.Registry().Get(handID)
	if stalled := h.hub.Registry().Sweep(before.LastHbAt.Add(201 * time.Millisecond)); len(stalled) != 1 {
		t.Fatalf("应选中一条 stalled 会话: %+v", stalled)
	}
	if _, _, ok := h.hub.HandPlatformCaps(handID, "boss"); ok {
		t.Fatal("stalled 会话不得暴露平台能力表(与 HandNegotiation 同款)")
	}
	if got := h.hub.HandPlatforms(handID); got != nil {
		t.Fatalf("stalled 会话 HandPlatforms 应为 nil: %v", got)
	}
}
