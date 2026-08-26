package session

import (
	"encoding/json"
	"sort"
	"testing"

	"recruithelper/contract/gen/go/protocol"

	"github.com/coder/websocket"
)

// welcome 必备字段。协议规格 §4.2 与 contract.v1.json 的 WelcomeBody 里没标
// optional 的那几个;下面的门禁用它判「optional 块有没有漏掉」。
var welcomeRequiredKeys = []string{"contractMatch", "hb", "limits", "now", "proto", "session"}

func sendHelloWithContractHash(t *testing.T, c *websocket.Conn, handID, bootID, hash string) {
	t.Helper()
	raw, err := protocol.Encode(protocol.HelloBody{
		HandID: handID, BootID: bootID,
		ProtoSupported: []int{protocol.ProtoVersion},
		App:            protocol.AppInfo{ExtVersion: "0.1.0", Browser: "test"},
		Caps:           []string{"debug.ping@1"},
		Features:       []string{},
		ContractHash:   hash,
	})
	if err != nil {
		t.Fatalf("encode hello: %v", err)
	}
	sendHelloBody(t, c, raw)
}

func readWelcomeRaw(t *testing.T, c *websocket.Conn) json.RawMessage {
	t.Helper()
	env := readEnv(t, c)
	if env.Kind != protocol.KindWelcome {
		t.Fatalf("首帧应为 welcome,得到 %s", env.Kind)
	}
	return env.Body
}

// 指纹对不上的手只收必备字段(协议规格 §4.2「契约指纹不一致时省略 optional 块」)。
//
// 这条防的是一个已经真实发生过的死局:optional 块的**内部字段全必填**,所以脑
// 「少发一个字段」会让旧手判 welcome 违约并关链,而 debug.reload 需要完整的
// cmd→result→ack 来回——手活不到那一步,换代通道随之锁死。
func TestWelcomeOmitsOptionalBlocksOnContractMismatch(t *testing.T) {
	h := newHarness(t)
	h.hub.SetBlob(&fakeBlobIssuer{}, "http://127.0.0.1:1/v1/blobs", protocol.DefaultPayloadBlobMaxBytes)

	c := dial(t, h.wsURL, testOrigin)
	defer c.CloseNow()
	sendHelloWithContractHash(t, c, defaultHandID, "boot-mismatch-1", "sha256:definitely-not-ours")

	var welcome protocol.WelcomeBody
	if err := json.Unmarshal(readWelcomeRaw(t, c), &welcome); err != nil {
		t.Fatalf("解码 welcome: %v", err)
	}
	if welcome.ContractMatch {
		t.Fatal("夹具没造出 mismatch,本用例失去意义")
	}
	if welcome.Sensors != nil {
		t.Fatalf("指纹不一致时不得下发 sensors: %+v", welcome.Sensors)
	}
	if welcome.Blob != nil {
		t.Fatalf("指纹不一致时不得下发 blob: %+v", welcome.Blob)
	}
}

// 门禁:指纹不一致的 welcome,**键集必须恰好等于必备集**。
//
// 上一条只盯 sensors/blob 两个已知的块;这一条盯的是「以后又加了一个 optional
// 块、却忘了放进 contractMatch 分支」——那种漏法没有任何症状,直到某次破坏性修订
// 再次把旧手锁死为止。
func TestMismatchedWelcomeCarriesOnlyRequiredKeys(t *testing.T) {
	h := newHarness(t)
	h.hub.SetBlob(&fakeBlobIssuer{}, "http://127.0.0.1:1/v1/blobs", protocol.DefaultPayloadBlobMaxBytes)

	c := dial(t, h.wsURL, testOrigin)
	defer c.CloseNow()
	sendHelloWithContractHash(t, c, defaultHandID, "boot-mismatch-2", "sha256:definitely-not-ours")

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(readWelcomeRaw(t, c), &fields); err != nil {
		t.Fatalf("解码 welcome: %v", err)
	}
	got := make([]string, 0, len(fields))
	for key := range fields {
		got = append(got, key)
	}
	sort.Strings(got)

	if len(got) != len(welcomeRequiredKeys) {
		t.Fatalf("指纹不一致的 welcome 键集应恰好是必备集\n实际: %v\n期望: %v", got, welcomeRequiredKeys)
	}
	for i, key := range welcomeRequiredKeys {
		if got[i] != key {
			t.Fatalf("指纹不一致的 welcome 键集应恰好是必备集\n实际: %v\n期望: %v", got, welcomeRequiredKeys)
		}
	}
}

// 指纹一致的手行为不变 —— 本裁决只改 mismatch 那条路,别把正常路一起削了。
func TestWelcomeCarriesOptionalBlocksOnContractMatch(t *testing.T) {
	h := newHarness(t)
	issuer := &fakeBlobIssuer{}
	h.hub.SetBlob(issuer, "http://127.0.0.1:1/v1/blobs", protocol.DefaultPayloadBlobMaxBytes)

	c := dial(t, h.wsURL, testOrigin)
	defer c.CloseNow()
	sendHelloWithContractHash(t, c, defaultHandID, "boot-match-1", protocol.ContractHash)

	var welcome protocol.WelcomeBody
	if err := json.Unmarshal(readWelcomeRaw(t, c), &welcome); err != nil {
		t.Fatalf("解码 welcome: %v", err)
	}
	if !welcome.ContractMatch {
		t.Fatal("同一份契约的 hello 必须判 match")
	}
	if welcome.Sensors == nil {
		t.Fatal("指纹一致时必须照常下发 sensors")
	}
	if welcome.Sensors.BadgeDebounceMs != protocol.DefaultSensorsBadgeDebounceMs ||
		welcome.Sensors.NavSettleMs != protocol.DefaultSensorsNavSettleMs ||
		welcome.Sensors.ManualQuietMs != protocol.DefaultSensorsManualQuietMs {
		t.Fatalf("sensors 取值不符: %+v", welcome.Sensors)
	}
	if welcome.Blob == nil {
		t.Fatal("指纹一致且已接线时必须照常下发 blob")
	}
}

// 省略 optional 块之后,welcome 仍须通过生成校验器 —— 它们是 optional,缺席合法。
// 这条直接对着 ValidateKindBody 断言,而不是对着结构体,因为把旧手打死的正是
// 校验器而不是读取代码。
func TestMismatchedWelcomeStillValidates(t *testing.T) {
	h := newHarness(t)
	c := dial(t, h.wsURL, testOrigin)
	defer c.CloseNow()
	sendHelloWithContractHash(t, c, defaultHandID, "boot-mismatch-3", "sha256:definitely-not-ours")

	raw := readWelcomeRaw(t, c)
	if err := protocol.ValidateKindBody(protocol.KindWelcome, raw); err != nil {
		t.Fatalf("省略 optional 块的 welcome 必须仍然合法: %v", err)
	}
}
