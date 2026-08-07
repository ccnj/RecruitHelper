package dispatch

import (
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

type jobPublishFixture struct {
	Platform   string
	AccountRef string
	JobID      string
	HandID     string
	Args       protocol.JobPrepareDraftArgs
}

func (f jobPublishFixture) request() PublishJobRequest {
	return PublishJobRequest{
		Platform: f.Platform, AccountRef: f.AccountRef, JobID: f.JobID, Args: f.Args,
	}
}

func seedJobPublishTarget(
	t *testing.T,
	st *store.Store,
	m *mockSender,
	slug string,
) jobPublishFixture {
	t.Helper()
	fixture := jobPublishFixture{
		Platform: "zhilian", AccountRef: "account-" + slug, JobID: "job-" + slug,
		HandID: "hand-publish",
		Args: protocol.JobPrepareDraftArgs{
			JobName: "储备总监（管理经验优先）", JobClass: "销售管理",
			Description:    "职位描述正文，用于试填与发布的同一份参数。",
			Education:      "本科",
			EmploymentType: "全职",
			Experience:     "5-10年",
			Headcount:      1,
			Keywords:       []string{"团队管理"},
			SalaryMin:      "2万",
			SalaryMax:      "3万",
			SalaryMonths:   "12薪",
			ShowToSeeker:   true,
		},
	}
	const bootID = "boot-publish"
	m.up(fixture.HandID, bootID)
	m.negotiate(fixture.HandID, []string{
		protocol.PrimJobPublishDraft + "@1",
		protocol.PrimJobReadPublishedList + "@1",
	}, append(append([]string(nil), allM2Features...), string(protocol.FeatureWitness1)))
	m.mu.Lock()
	m.witness[fixture.HandID] = HandWitness{StoreID: "witness-publish"}
	m.mu.Unlock()
	if err := st.CreateAccount(&store.Account{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccountPrincipal(
		store.AccountKey{Platform: fixture.Platform, AccountRef: fixture.AccountRef},
		fixture.HandID, "principal-"+slug, "s-test", bootID, time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// 职位发布长期没有人工裁决实现：真人在诊断台点哪个按钮都只撞回"真实副作用原语
// 没有人工裁决实现"，suspect 清不掉；而 publishAttemptSettled 又把 suspect 当
// 未收束，于是这些职位连重发都做不到——一次不确定就把该职位永久锁死。
//
// 两个方向都只写终局、不铸任何消息事实，裁决之后同一份发布参数必须能再发。
func TestJobPublishSuspectVerdictSettlesLedgerAndUnblocksRepublish(t *testing.T) {
	cases := []struct {
		name    string
		verdict store.CmdStatus
		intent  store.EffectIntentStatus
	}{
		{"确认已发生", store.CmdResolvedOk, store.EffectIntentResolvedOk},
		{"确认未发生", store.CmdResolvedFailed, store.EffectIntentResolvedFailed},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			d, st, m := newDisp(t)
			fixture := seedJobPublishTarget(t, st, m, "verdict")
			receipt, err := d.PublishJob(fixture.request())
			if err != nil {
				t.Fatal(err)
			}
			makeEffectSuspectReviewable(t, d, st, receipt.MsgID, time.Now().Add(-time.Second))

			if err := d.Verdict(receipt.MsgID, testCase.verdict); err != nil {
				t.Fatalf("职位发布 suspect 必须可人裁: %v", err)
			}

			cmd, err := st.CmdByMsgID(receipt.MsgID)
			if err != nil {
				t.Fatal(err)
			}
			if cmd.Status != testCase.verdict || cmd.TerminalAt == nil ||
				cmd.ReviewReady || cmd.ReviewAfterMs != 0 ||
				cmd.RecoveryAuthorized || cmd.VerificationNextAt != nil {
				t.Fatalf("裁决后命令必须是干净终局: %+v", cmd)
			}
			intent, err := d.PublishJobStatus(receipt.IntentID)
			if err != nil {
				t.Fatal(err)
			}
			if intent.Status != testCase.intent || intent.ResolvedAt == nil {
				t.Fatalf("裁决后意图状态应为 %s: %+v", testCase.intent, intent)
			}
			// 职位发布没有会话，两个方向都不得铸出任何我方消息事实。
			if intent.ResultMessageSeq != nil || intent.ResultConversationRef != nil {
				t.Fatalf("职位发布裁决不得铸消息事实: %+v", intent)
			}
			if !hasAudit(t, st, "suspect_verdict", receipt.MsgID) {
				t.Fatal("人工裁决必须留审计")
			}

			// 重复点击（或页面轮询里的第二次提交）不得把已终局的意图再翻一次。
			if err := d.Verdict(receipt.MsgID, testCase.verdict); err != ErrNotSuspect {
				t.Fatalf("已终局的 suspect 不得再次裁决: %v", err)
			}

			// 甲方点按钮的目的就是让这个职位能重新发布：裁决终局后同一份发布参数
			// 必须铸出新意图，而不是把原收据还回去。
			again, err := d.PublishJob(fixture.request())
			if err != nil {
				t.Fatal(err)
			}
			if !again.Created || again.IntentID == receipt.IntentID {
				t.Fatalf("裁决终局后应允许递进尝试序号重发: %+v（原 %s）", again, receipt.IntentID)
			}
		})
	}
}

// 裁决前置不因新增分支松动：suspect 尚未 review-ready 时（验证还没跑完、手还在线）
// 一律不许早裁，否则人会替一个仍在收束中的动作下结论。
func TestJobPublishVerdictStillRefusesEarlyReview(t *testing.T) {
	d, st, m := newDisp(t)
	fixture := seedJobPublishTarget(t, st, m, "early")
	receipt, err := d.PublishJob(fixture.request())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MoveEffectToVerification(receipt.MsgID, "test ambiguity", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkEffectSuspect(receipt.MsgID, "test suspect", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := d.Verdict(receipt.MsgID, store.CmdResolvedFailed); err != ErrVerdictNotReady {
		t.Fatalf("手在线且未收束的职位发布 suspect 不得早裁: %v", err)
	}
}
