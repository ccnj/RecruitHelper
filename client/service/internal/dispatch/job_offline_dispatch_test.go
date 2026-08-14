package dispatch

import (
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

func seedJobOfflineTarget(
	t *testing.T,
	st *store.Store,
	m *mockSender,
	slug string,
) TakeJobOfflineRequest {
	t.Helper()
	req := TakeJobOfflineRequest{
		Platform: "zhilian", AccountRef: "account-" + slug, JobID: "job-" + slug,
		JobName: "储备总监（管理经验优先）",
	}
	const handID = "hand-offline"
	const bootID = "boot-offline"
	m.up(handID, bootID)
	m.negotiate(handID, []string{
		protocol.PrimJobTakeOffline + "@1",
		protocol.PrimJobReadPublishedList + "@1",
	}, append(append([]string(nil), allM2Features...), string(protocol.FeatureWitness1)))
	m.mu.Lock()
	m.witness[handID] = HandWitness{StoreID: "witness-offline"}
	m.mu.Unlock()
	if err := st.CreateAccount(&store.Account{
		Platform: req.Platform, AccountRef: req.AccountRef,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccountPrincipal(
		store.AccountKey{Platform: req.Platform, AccountRef: req.AccountRef},
		handID, "principal-"+slug, "s-test", bootID, time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	return req
}

// 下线是 effectful，账本闸必须与发布同规格：同一职位在意图未收束期间重复调用
// 只能收编原意图，绝不另铸一条去平台上再点一次。
func TestTakeJobOfflineReusesInFlightIntent(t *testing.T) {
	d, st, m := newDisp(t)
	req := seedJobOfflineTarget(t, st, m, "reuse")

	first, err := d.TakeJobOffline(req)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created {
		t.Fatalf("首次下线应铸新意图: %+v", first)
	}
	again, err := d.TakeJobOffline(req)
	if err != nil {
		t.Fatal(err)
	}
	if again.Created || again.IntentID != first.IntentID || again.MsgID != first.MsgID {
		t.Fatalf("在途意图必须原样收编，不得另铸: %+v（原 %+v）", again, first)
	}
	intent, err := d.TakeJobOfflineStatus(first.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	if intent.Primitive != protocol.PrimJobTakeOffline || intent.TargetRef != req.JobID {
		t.Fatalf("意图身份不对: %+v", intent)
	}
}

// 下线与发布是两条独立意图：同一个职位两者并存，各走各的账，互不收编、
// 互不冻结。发布成功而下线失败时，已发布那笔账必须原样成立。
func TestTakeJobOfflineIntentIsIndependentFromPublish(t *testing.T) {
	d, st, m := newDisp(t)
	publish := seedJobPublishTarget(t, st, m, "independent")
	// 两条链路共用同一个账号与手，需要手同时声明两个原语能力。
	m.negotiate(publish.HandID, []string{
		protocol.PrimJobPublishDraft + "@1",
		protocol.PrimJobTakeOffline + "@1",
		protocol.PrimJobReadPublishedList + "@1",
	}, append(append([]string(nil), allM2Features...), string(protocol.FeatureWitness1)))

	published, err := d.PublishJob(publish.request())
	if err != nil {
		t.Fatal(err)
	}
	offline, err := d.TakeJobOffline(TakeJobOfflineRequest{
		Platform: publish.Platform, AccountRef: publish.AccountRef,
		JobID: publish.JobID, JobName: publish.Args.JobName,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !offline.Created || offline.IntentID == published.IntentID {
		t.Fatalf("下线必须是独立意图: %+v（发布 %+v）", offline, published)
	}
	// 各查各的：两个 Status 入口都只认自己那个原语，串不了台。
	if _, err := d.TakeJobOfflineStatus(published.IntentID); err == nil {
		t.Fatal("下线状态入口不得返回发布意图")
	}
	if _, err := d.PublishJobStatus(offline.IntentID); err == nil {
		t.Fatal("发布状态入口不得返回下线意图")
	}
}

// 本次战役最独特的一条（甲方 2026-08-13 裁决）：下线只是锦上添花，验证耗尽后
// 由脑自动判 resolvedFailed，**不留人工票据**。
//
// 它是全部 effectful 原语里唯一这样收场的，所以既要证明"自动收成了终局"，
// 也要证明"没有留下 review-ready 的 suspect 等人来点"。
func TestTakeJobOfflineUnconfirmedAutoResolvesWithoutManualTicket(t *testing.T) {
	d, st, m := newDisp(t)
	req := seedJobOfflineTarget(t, st, m, "autoresolve")
	receipt, err := d.TakeJobOffline(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MoveEffectToVerification(receipt.MsgID, "test ambiguity", time.Now()); err != nil {
		t.Fatal(err)
	}
	// 跑满验证轮次：最后一轮 miss 会先落成 suspect，再由本原语分支就地自动裁决。
	for i := 0; i < protocol.DefaultVerificationMaxRounds; i++ {
		cmd, cmdErr := st.CmdByMsgID(receipt.MsgID)
		if cmdErr != nil {
			t.Fatal(cmdErr)
		}
		d.recordVerificationMiss(*cmd, "test miss")
	}

	cmd, err := st.CmdByMsgID(receipt.MsgID)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Status != store.CmdResolvedFailed {
		t.Fatalf("下线未确认应自动收成 resolvedFailed，实际 %s: %+v", cmd.Status, cmd)
	}
	if cmd.ReviewReady {
		t.Fatalf("下线不得留下待人裁的 suspect: %+v", cmd)
	}
	if cmd.TerminalAt == nil || cmd.VerificationNextAt != nil || cmd.RecoveryAuthorized {
		t.Fatalf("自动裁决后命令必须是干净终局: %+v", cmd)
	}
	intent, err := d.TakeJobOfflineStatus(receipt.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	if intent.Status != store.EffectIntentResolvedFailed || intent.ResolvedAt == nil {
		t.Fatalf("意图应为 resolvedFailed: %+v", intent)
	}
	// 下线没有会话，任一方向都不得铸出我方消息事实。
	if intent.ResultMessageSeq != nil || intent.ResultConversationRef != nil {
		t.Fatalf("职位下线不得铸消息事实: %+v", intent)
	}
	// "记一笔"必须真的记下来：审计是这条裁决留下的唯一痕迹。
	if !hasAudit(t, st, "job_offline_unconfirmed", receipt.MsgID) {
		t.Fatal("下线未确认必须留审计")
	}
}

// 与上一条对照：同样跑满验证轮次，发布仍然必须停在待人裁的 suspect。
// 这条钉住"不产人工票据"是下线独享的例外，没有顺手放宽发布。
func TestJobPublishUnconfirmedStillWaitsForManualVerdict(t *testing.T) {
	d, st, m := newDisp(t)
	fixture := seedJobPublishTarget(t, st, m, "contrast")
	receipt, err := d.PublishJob(fixture.request())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MoveEffectToVerification(receipt.MsgID, "test ambiguity", time.Now()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < protocol.DefaultVerificationMaxRounds; i++ {
		cmd, cmdErr := st.CmdByMsgID(receipt.MsgID)
		if cmdErr != nil {
			t.Fatal(cmdErr)
		}
		d.recordVerificationMiss(*cmd, "test miss")
	}
	cmd, err := st.CmdByMsgID(receipt.MsgID)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Status != store.CmdSuspect {
		t.Fatalf("发布未确认必须停在 suspect 等人裁，实际 %s", cmd.Status)
	}
}
