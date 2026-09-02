package dispatch

import (
	"encoding/json"
	"errors"
	"testing"

	"recruithelper/contract/gen/go/protocol"
)

// 派发闸按平台判能力(2026-09-02 甲方裁决,规格 §4.1):
// 手声明了含该平台的 platforms 时按该表判,否则按并集判。
func TestNegotiationGateUsesPlatformCapsWhenDeclared(t *testing.T) {
	d, _, m := newDisp(t)
	m.up("hand-01", "b-1")
	union := []string{protocol.PrimNavEnsureSurface + "@1", protocol.PrimAccountReadWechatSetting + "@1"}
	m.negotiate("hand-01", union, allM2Features)
	m.declarePlatform("hand-01", "zhilian", union)
	m.declarePlatform("hand-01", "boss", []string{protocol.PrimNavEnsureSurface + "@1"})

	bossRead := DispatchRequest{
		HandID: "hand-01", Name: protocol.PrimAccountReadWechatSetting,
		Args: json.RawMessage(`{}`),
		Context: &protocol.CmdContext{
			Platform: "boss", AccountRef: "acct-boss", ExpectedPrincipalFingerprint: "fp-boss",
		},
	}
	if _, err := d.DispatchStructured(bossRead); !errors.Is(err, ErrCapability) {
		t.Fatalf("并集有、boss 表没有的原语,带 context.platform=boss 派发应按平台表拒绝: %v", err)
	}
	if m.sentCount() != 0 {
		t.Fatalf("按平台表拒绝不得发出任何帧: sent=%d", m.sentCount())
	}

	zhilianRead := bossRead
	zhilianRead.Context = &protocol.CmdContext{
		Platform: "zhilian", AccountRef: "acct-zl", ExpectedPrincipalFingerprint: "fp-zl",
	}
	if _, err := d.DispatchStructured(zhilianRead); err != nil {
		t.Fatalf("智联表有该原语,应放行: %v", err)
	}
	if m.sentCount() != 1 {
		t.Fatalf("放行后应恰好发出一帧: sent=%d", m.sentCount())
	}
}

func TestNegotiationGateFallsBackToUnionWithoutDeclaration(t *testing.T) {
	d, _, m := newDisp(t)
	m.up("hand-01", "b-1")
	union := []string{protocol.PrimNavEnsureSurface + "@1", protocol.PrimAccountReadWechatSetting + "@1"}
	m.negotiate("hand-01", union, allM2Features)
	// 只声明了智联表;boss 未声明 → 回落并集,与旧手逐字相同。
	m.declarePlatform("hand-01", "zhilian", union)

	bossRead := DispatchRequest{
		HandID: "hand-01", Name: protocol.PrimAccountReadWechatSetting,
		Args: json.RawMessage(`{}`),
		Context: &protocol.CmdContext{
			Platform: "boss", AccountRef: "acct-boss", ExpectedPrincipalFingerprint: "fp-boss",
		},
	}
	if _, err := d.DispatchStructured(bossRead); err != nil {
		t.Fatalf("手未声明 boss 表时应按并集放行: %v", err)
	}

	// 声明了该平台但表里没有 → 拒绝;并集判在此不再兜底(平台表 ⊆ 并集是手侧构造保证)。
	m.declarePlatform("hand-01", "boss", nil)
	if _, err := d.DispatchStructured(bossRead); !errors.Is(err, ErrCapability) {
		t.Fatalf("声明了空 boss 表后应拒绝: %v", err)
	}
}

func TestNegotiationGateReadsArgsPlatformForContextlessProbe(t *testing.T) {
	d, _, m := newDisp(t)
	m.up("hand-01", "b-1")
	union := []string{protocol.PrimProbePlatform + "@1"}
	m.negotiate("hand-01", union, allM2Features)
	m.declarePlatform("hand-01", "zhilian", union)
	m.declarePlatform("hand-01", "boss", nil)

	probe := func(platform string) DispatchRequest {
		return DispatchRequest{
			HandID: "hand-01", Name: protocol.PrimProbePlatform,
			Args: json.RawMessage(`{"platform":"` + platform + `"}`),
		}
	}
	if _, err := d.DispatchStructured(probe("boss")); !errors.Is(err, ErrCapability) {
		t.Fatalf("无 context 的 probe 按 args.platform=boss 查表,boss 表无 probe 应拒绝: %v", err)
	}
	if _, err := d.DispatchStructured(probe("zhilian")); err != nil {
		t.Fatalf("args.platform=zhilian 应按智联表放行: %v", err)
	}
	// 既无 context 也无 args.platform 的命令按并集判(debug.* 平台无关原语的路径)。
	m.negotiate("hand-01", []string{protocol.PrimDebugPing + "@1"}, allM2Features)
	m.declarePlatform("hand-01", "boss", nil)
	if _, err := d.DispatchStructured(DispatchRequest{
		HandID: "hand-01", Name: protocol.PrimDebugPing, Args: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("无平台命令应按并集放行: %v", err)
	}
}

func TestCommandPlatformResolutionOrder(t *testing.T) {
	ctx := &protocol.CmdContext{Platform: "zhilian", AccountRef: "a", ExpectedPrincipalFingerprint: "f"}
	if got := commandPlatform(ctx, json.RawMessage(`{"platform":"boss"}`)); got != "zhilian" {
		t.Fatalf("context 优先于 args: %q", got)
	}
	if got := commandPlatform(nil, json.RawMessage(`{"platform":" boss "}`)); got != "boss" {
		t.Fatalf("无 context 取 args.platform(去首尾空白): %q", got)
	}
	if got := commandPlatform(nil, json.RawMessage(`{"platform":""}`)); got != "" {
		t.Fatalf("空 args.platform 视同无: %q", got)
	}
	if got := commandPlatform(nil, json.RawMessage(`not json`)); got != "" {
		t.Fatalf("args 解析失败视同无,不是校验点: %q", got)
	}
	if got := commandPlatform(nil, nil); got != "" {
		t.Fatalf("无 args: %q", got)
	}
	if got := recordPlatform("", `{"platform":"boss"}`); got != "boss" {
		t.Fatalf("账本 Platform 为空时回落 Args: %q", got)
	}
	if got := recordPlatform("zhilian", `{"platform":"boss"}`); got != "zhilian" {
		t.Fatalf("账本 Platform 优先: %q", got)
	}
}
