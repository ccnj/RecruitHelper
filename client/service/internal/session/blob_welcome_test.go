package session

import (
	"encoding/json"
	"testing"

	"recruithelper/contract/gen/go/protocol"
)

type fakeBlobIssuer struct{ last string }

func (f *fakeBlobIssuer) Rotate(handID string) string {
	f.last = "bt-test-" + handID
	return f.last
}

// welcome 在接线 blob 后必须携带 BlobParams(§13 上行子集);未接线时缺席。
func TestWelcomeCarriesBlobParamsWhenConfigured(t *testing.T) {
	h := newHarness(t)
	issuer := &fakeBlobIssuer{}
	h.hub.SetBlob(issuer, "http://127.0.0.1:1/v1/blobs", protocol.DefaultPayloadBlobMaxBytes)

	c := dial(t, h.wsURL, testOrigin)
	defer c.CloseNow()
	sendHello(t, c, defaultHandID, "boot-blob-1")
	env := readEnv(t, c)
	if env.Kind != protocol.KindWelcome {
		t.Fatalf("首帧应为 welcome,得到 %s", env.Kind)
	}
	var welcome protocol.WelcomeBody
	if err := json.Unmarshal(env.Body, &welcome); err != nil {
		t.Fatalf("解码 welcome: %v", err)
	}
	if welcome.Blob == nil {
		t.Fatal("welcome 缺少 blob 参数")
	}
	if welcome.Blob.Endpoint != "http://127.0.0.1:1/v1/blobs" ||
		welcome.Blob.Token != issuer.last ||
		welcome.Blob.MaxBytes != protocol.DefaultPayloadBlobMaxBytes {
		t.Fatalf("blob 参数不符: %+v (期望 token=%s)", welcome.Blob, issuer.last)
	}
}

func TestWelcomeOmitsBlobParamsWhenUnconfigured(t *testing.T) {
	h := newHarness(t)
	c := dial(t, h.wsURL, testOrigin)
	defer c.CloseNow()
	sendHello(t, c, defaultHandID, "boot-blob-2")
	env := readEnv(t, c)
	if env.Kind != protocol.KindWelcome {
		t.Fatalf("首帧应为 welcome,得到 %s", env.Kind)
	}
	var welcome protocol.WelcomeBody
	if err := json.Unmarshal(env.Body, &welcome); err != nil {
		t.Fatalf("解码 welcome: %v", err)
	}
	if welcome.Blob != nil {
		t.Fatalf("未接线时 welcome 不应携带 blob: %+v", welcome.Blob)
	}
}
