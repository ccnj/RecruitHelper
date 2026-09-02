package dispatch

import (
	"encoding/json"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

func lastSentCmd(t *testing.T, m *mockSender) protocol.CmdBody {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		t.Fatal("没有发出任何帧")
	}
	var body protocol.CmdBody
	if err := json.Unmarshal(m.sent[len(m.sent)-1].Body, &body); err != nil {
		t.Fatalf("解析已发帧: %v", err)
	}
	return body
}

func approxMs(t *testing.T, label string, got, want int64) {
	t.Helper()
	if got < want-2000 || got > want+2000 {
		t.Fatalf("%s: 得到 %d,期望约 %d", label, got, want)
	}
}

// 预算按平台系数(2026-09-02 甲方裁决,批 D 2.3):BOSS 2.0 倍、预算封顶 480000、期限同乘不封顶;
// 智联与无平台命令系数 1.0,数值与契约逐字相同。
func TestBudgetScalesByPlatformAndCapsAtEnvelope(t *testing.T) {
	d, _, m := newDisp(t)
	m.up("hand-01", "b-1")
	m.negotiate("hand-01", []string{
		protocol.PrimNavEnsureSurface + "@1", protocol.PrimChatReadList + "@1",
		protocol.PrimProbePlatform + "@1", protocol.PrimDebugPing + "@1",
	}, allM2Features)
	nav := protocol.Primitives[protocol.PrimNavEnsureSurface]
	readList := protocol.Primitives[protocol.PrimChatReadList]
	probe := protocol.Primitives[protocol.PrimProbePlatform]
	ping := protocol.Primitives[protocol.PrimDebugPing]

	bossNav := businessNav("hand-01", "acct-boss")
	bossNav.Context.Platform = "boss"
	now := time.Now().UnixMilli()
	if _, err := d.DispatchStructured(bossNav); err != nil {
		t.Fatalf("boss nav: %v", err)
	}
	body := lastSentCmd(t, m)
	if body.ExecBudgetMs != nav.ExecBudgetMs*2 {
		t.Fatalf("boss 预算应为契约 2 倍: 得到 %d 契约 %d", body.ExecBudgetMs, nav.ExecBudgetMs)
	}
	approxMs(t, "boss nav deadline", body.Deadline-now, nav.DeadlineMs*2)
	if body.LeaseMs != nav.LeaseMs {
		t.Fatalf("lease 不同比: 得到 %d 契约 %d", body.LeaseMs, nav.LeaseMs)
	}

	zhilianNav := businessNav("hand-01", "acct-zl")
	if _, err := d.DispatchStructured(zhilianNav); err != nil {
		t.Fatalf("zhilian nav: %v", err)
	}
	body = lastSentCmd(t, m)
	if body.ExecBudgetMs != nav.ExecBudgetMs {
		t.Fatalf("智联系数 1.0,预算须与契约逐字相同: %d", body.ExecBudgetMs)
	}
	approxMs(t, "zhilian nav deadline", body.Deadline-now, nav.DeadlineMs)

	bossList := DispatchRequest{
		HandID: "hand-01", Name: protocol.PrimChatReadList,
		Args:    json.RawMessage(`{"filter":"unread","move":"reset"}`),
		Context: &protocol.CmdContext{Platform: "boss", AccountRef: "acct-boss-list", ExpectedPrincipalFingerprint: "fp"},
	}
	if _, err := d.DispatchStructured(bossList); err != nil {
		t.Fatalf("boss readList: %v", err)
	}
	body = lastSentCmd(t, m)
	if body.ExecBudgetMs != execBudgetEnvelopeMaxMs || body.ExecBudgetMs != readList.ExecBudgetMs {
		t.Fatalf("readList 契约预算已在信封上限,boss 下必须封顶不放大: %d", body.ExecBudgetMs)
	}
	approxMs(t, "boss readList deadline(不封顶)", body.Deadline-now, readList.DeadlineMs*2)

	bossProbe := DispatchRequest{
		HandID: "hand-01", Name: protocol.PrimProbePlatform, Args: json.RawMessage(`{"platform":"boss"}`),
	}
	if _, err := d.DispatchStructured(bossProbe); err != nil {
		t.Fatalf("boss probe: %v", err)
	}
	body = lastSentCmd(t, m)
	if body.ExecBudgetMs != probe.ExecBudgetMs*2 {
		t.Fatalf("无 context 的 probe 按 args.platform 取系数: %d", body.ExecBudgetMs)
	}
	approxMs(t, "boss probe deadline", body.Deadline-now, probe.DeadlineMs*2)

	if _, err := d.DispatchStructured(DispatchRequest{
		HandID: "hand-01", Name: protocol.PrimDebugPing, Args: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("ping: %v", err)
	}
	body = lastSentCmd(t, m)
	if body.ExecBudgetMs != ping.ExecBudgetMs {
		t.Fatalf("无平台命令系数 1.0: %d", body.ExecBudgetMs)
	}
	approxMs(t, "ping deadline", body.Deadline-now, ping.DeadlineMs)
}

// 重派 child 继承已放大的预算,期限必须按同一平台重算——否则 BOSS 上 child 期限 < 预算,
// 手侧定时器取 min(deadline,budget) 会在期限处先报 expired。
func TestRedispatchKeepsPlatformScaledDeadline(t *testing.T) {
	d, st, m := newDisp(t)
	m.up("hand-01", "b-1")
	m.negotiate("hand-01", []string{protocol.PrimNavEnsureSurface + "@1"}, allM2Features)
	nav := protocol.Primitives[protocol.PrimNavEnsureSurface]
	req := businessNav("hand-01", "acct-boss")
	req.Context.Platform = "boss"
	if _, err := d.DispatchStructured(req); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	d.sweepFaults(future())
	recs, _ := st.RecentCmds(10)
	var replacement *store.CmdRecord
	for _, r := range recs {
		if r.Status == store.CmdQueued && r.RedispatchN == 1 && r.Name == protocol.PrimNavEnsureSurface {
			copy := r
			replacement = &copy
		}
	}
	if replacement == nil || replacement.NotBeforeAt == nil {
		t.Fatalf("应存在 RedispatchN=1 的 replacement: %+v", recs)
	}
	if replacement.ExecBudgetMs != nav.ExecBudgetMs*2 {
		t.Fatalf("replacement 预算应继承放大值: %d", replacement.ExecBudgetMs)
	}
	if got := replacement.DeadlineMs - replacement.NotBeforeAt.UnixMilli(); got != nav.DeadlineMs*2 {
		t.Fatalf("replacement 期限应按 boss 系数重算: 得到 %d 期望 %d", got, nav.DeadlineMs*2)
	}
	if replacement.Platform != "boss" {
		t.Fatalf("replacement 须继承平台: %q", replacement.Platform)
	}
}

func TestScaleHelpersPinEnvelopeMirror(t *testing.T) {
	// 镜像常量必须与契约信封上限一致:480000 通过、480001 拒绝。
	valid := json.RawMessage(`{"name":"debug.ping","ver":1,"args":{},"deadline":1999999999999,"execBudgetMs":480000}`)
	if err := protocol.ValidateKindBody(protocol.KindCmd, valid); err != nil {
		t.Fatalf("execBudgetMs=480000 应通过契约校验: %v", err)
	}
	over := json.RawMessage(`{"name":"debug.ping","ver":1,"args":{},"deadline":1999999999999,"execBudgetMs":480001}`)
	if err := protocol.ValidateKindBody(protocol.KindCmd, over); err == nil {
		t.Fatal("execBudgetMs=480001 应被契约拒绝——否则镜像常量已漂移")
	}
	if scaleBudgetMs(300000, "boss") != execBudgetEnvelopeMaxMs {
		t.Fatalf("放大越过信封须封顶: %d", scaleBudgetMs(300000, "boss"))
	}
	if scaleDeadlineMs(600000, "boss") != 1200000 {
		t.Fatalf("期限同乘不封顶: %d", scaleDeadlineMs(600000, "boss"))
	}
	if scaleBudgetMs(300000, "lagou") != 300000 || scaleBudgetMs(300000, "") != 300000 {
		t.Fatal("未知平台或无平台一律 1.0")
	}
}
