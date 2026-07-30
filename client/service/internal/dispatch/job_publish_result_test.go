package dispatch

import (
	"encoding/json"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// 职位发布的 result 必须能落库：漏接这条分支时 result_body 永远为空，
// 发布接口即使在配对验证读之后也只能报"未取得成功终局"。
func TestJobPublishResultParserClosesOnHandProofAndRoutesUnknownToVerifying(t *testing.T) {
	const jobName = "储备总监（管理经验优先）"
	const msgID = "msg-job-publish-result"
	argsRaw, err := protocol.Encode(protocol.JobPrepareDraftArgs{
		JobName: jobName, Description: "职位描述正文，用于试填与发布的同一份参数。",
		Education: "本科", EmploymentType: "全职", Experience: "5-10年",
		Headcount: 1, Keywords: []string{"团队管理"},
		SalaryMin: "2万", SalaryMax: "3万", SalaryMonths: "12薪",
		ShowToSeeker: true, SyncToMailbox: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	validResult := func() protocol.ResultBody {
		data, encodeErr := protocol.Encode(protocol.JobPublishDraftData{
			JobName: jobName, PostingVisible: true, VerifyRounds: 1,
			Keywords:   protocol.JobPrepareDraftKeywordOutcome{},
			ObservedAt: time.Now().UnixMilli(),
		})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		return protocol.ResultBody{
			Ref: msgID, Status: protocol.ResultStatusOk, Data: data,
			Evidence: []protocol.Evidence{{
				Type: string(protocol.PublishDraftEvidenceTypePlatformPostingObserved),
			}},
		}
	}
	parse := func(
		t *testing.T,
		result protocol.ResultBody,
		status store.CmdStatus,
	) (store.ResultCommandMutation, resultOutcome, error) {
		t.Helper()
		command := store.CmdRecord{
			MsgID: msgID, Name: protocol.PrimJobPublishDraft,
			Args: string(argsRaw), Status: status,
		}
		body, encodeErr := protocol.Encode(result)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		outcome := ocDone
		plan, parseErr := (&Dispatcher{}).realEffectResultPlan(
			&command, result, body, time.Now().UTC(), "", "", true,
			store.ResultCommandMutation{Save: true}, &outcome,
		)
		return plan, outcome, parseErr
	}

	// 手的发后回读正证即终局，不再要求脑多跑一次导航。
	plan, outcome, err := parse(t, validResult(), store.CmdQueued)
	if err != nil || outcome != ocDone || !plan.Save || plan.KeepCommandOpen ||
		plan.Effect == nil || plan.Effect.IntentStatus != store.EffectIntentOk ||
		plan.Effect.ContentHash != "" || plan.Effect.Append || plan.Effect.Card != nil ||
		plan.Effect.Greeting != nil || plan.Effect.WechatContact != nil {
		t.Fatalf("合法发布正证必须直接收成 ok 且不铸消息: plan=%+v outcome=%v err=%v",
			plan, outcome, err)
	}

	testCases := []struct {
		name   string
		mutate func(*protocol.JobPublishDraftData, *protocol.ResultBody)
	}{
		{
			// 手在读不到同名职位时抛 possible，绝不回 ok；真回来了说明手
			// 与契约不一致，不能当成功收编。
			name: "posting not visible",
			mutate: func(data *protocol.JobPublishDraftData, _ *protocol.ResultBody) {
				data.PostingVisible = false
			},
		},
		{
			name: "job name mismatch",
			mutate: func(data *protocol.JobPublishDraftData, _ *protocol.ResultBody) {
				data.JobName += "（其它）"
			},
		},
		{
			name: "evidence mismatch",
			mutate: func(_ *protocol.JobPublishDraftData, result *protocol.ResultBody) {
				result.Evidence[0].Type = "wrongEvidence"
			},
		},
		{
			name: "evidence missing",
			mutate: func(_ *protocol.JobPublishDraftData, result *protocol.ResultBody) {
				result.Evidence = nil
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result := validResult()
			var data protocol.JobPublishDraftData
			if err := json.Unmarshal(result.Data, &data); err != nil {
				t.Fatal(err)
			}
			testCase.mutate(&data, &result)
			dataRaw, err := protocol.Encode(data)
			if err != nil {
				t.Fatal(err)
			}
			result.Data = dataRaw
			if _, _, err := parse(t, result, store.CmdQueued); err == nil {
				t.Fatal("发布正证与原始意图不一致必须拒绝")
			}
		})
	}

	// 结果未知交给配对验证读，不得就地判失败、更不得授权重发。
	unknown := protocol.ResultBody{
		Ref: msgID, Status: protocol.ResultStatusFailed,
		Error: &protocol.ErrorBody{
			Code: protocol.ErrCodePostconditionUnconfirmed, Message: "发布后未在职位列表读到同名职位",
			Retryable: protocol.RetryableManualOnly, SideEffect: protocol.SideEffectPossible,
		},
	}
	unknownPlan, unknownOutcome, err := parse(t, unknown, store.CmdQueued)
	if err != nil || unknownOutcome != ocEffSuspect || !unknownPlan.KeepCommandOpen ||
		unknownPlan.Effect == nil ||
		unknownPlan.Effect.IntentStatus != store.EffectIntentVerifying {
		t.Fatalf("结果未知必须进配对验证读: plan=%+v outcome=%v err=%v",
			unknownPlan, unknownOutcome, err)
	}

	// 点击前就失败的（守卫不过、表单填不进去）是干净失败，可以直接终局。
	clean := protocol.ResultBody{
		Ref: msgID, Status: protocol.ResultStatusFailed,
		Error: &protocol.ErrorBody{
			Code: protocol.ErrCodeGuardFailed, Message: "同名职位已在平台上架",
			Retryable: protocol.RetryableManualOnly, SideEffect: protocol.SideEffectNone,
		},
	}
	cleanPlan, cleanOutcome, err := parse(t, clean, store.CmdQueued)
	if err != nil || cleanOutcome != ocDone || cleanPlan.KeepCommandOpen ||
		cleanPlan.Effect == nil ||
		cleanPlan.Effect.IntentStatus != store.EffectIntentFailed {
		t.Fatalf("干净失败必须直接终局: plan=%+v outcome=%v err=%v",
			cleanPlan, cleanOutcome, err)
	}

	late, lateOutcome, err := parse(t, validResult(), store.CmdOk)
	if err != nil || lateOutcome != ocLate || late.Save || late.Effect != nil {
		t.Fatalf("终局后迟到的发布 result 不得重开账本: plan=%+v outcome=%v err=%v",
			late, lateOutcome, err)
	}
}
