package dispatch

import (
	"encoding/json"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

func TestWechatAcceptResultParserCrossChecksArgsDataAndEvidence(t *testing.T) {
	conversationRef := "conversation-accept-result"
	requestSourceKey := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	exchangeSourceKey := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	argsRaw, err := protocol.Encode(protocol.ChatAcceptWechatArgs{
		ConversationRef:  conversationRef,
		RequestSourceKey: requestSourceKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	validResult := func() protocol.ResultBody {
		data, encodeErr := protocol.Encode(protocol.ChatAcceptWechatData{
			ConversationRef:   conversationRef,
			RequestSourceKey:  requestSourceKey,
			ExchangeSourceKey: exchangeSourceKey,
			PeerWechat:        "synthetic-wechat-result-parser",
			ObservedAt:        time.Now().UnixMilli(),
		})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		return protocol.ResultBody{
			Ref: "msg-accept-result", Status: protocol.ResultStatusOk,
			Data: data,
			Evidence: []protocol.Evidence{{
				Type: string(protocol.AcceptWechatEvidenceTypeCandidateWechatRequestAcceptedObserved),
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
			MsgID: "msg-accept-result", Name: protocol.PrimChatAcceptWechat,
			Args: string(argsRaw), Status: status,
		}
		body, encodeErr := protocol.Encode(result)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		outcome := ocDone
		plan, parseErr := (&Dispatcher{}).realEffectResultPlan(
			&command,
			result,
			body,
			time.Now().UTC(),
			"",
			"",
			true,
			store.ResultCommandMutation{Save: true},
			&outcome,
		)
		return plan, outcome, parseErr
	}

	plan, outcome, err := parse(t, validResult(), store.CmdQueued)
	if err != nil ||
		outcome != ocDone ||
		!plan.Save ||
		plan.Effect == nil ||
		plan.Effect.IntentStatus != store.EffectIntentOk ||
		plan.Effect.WechatContact == nil ||
		plan.Effect.WechatContact.ConversationRef != conversationRef ||
		plan.Effect.WechatContact.RequestSourceKey != requestSourceKey ||
		plan.Effect.WechatContact.ExchangeSourceKey != exchangeSourceKey ||
		plan.Effect.WechatContact.PeerWechat != "synthetic-wechat-result-parser" {
		t.Fatalf("合法接受结果未生成联系方式事务: plan=%+v outcome=%v err=%v",
			plan, outcome, err)
	}

	testCases := []struct {
		name   string
		mutate func(*protocol.ChatAcceptWechatData, *protocol.ResultBody)
	}{
		{
			name: "conversation mismatch",
			mutate: func(data *protocol.ChatAcceptWechatData, _ *protocol.ResultBody) {
				data.ConversationRef += "-other"
			},
		},
		{
			name: "request source mismatch",
			mutate: func(data *protocol.ChatAcceptWechatData, _ *protocol.ResultBody) {
				data.RequestSourceKey = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
			},
		},
		{
			name: "exchange source invalid",
			mutate: func(data *protocol.ChatAcceptWechatData, _ *protocol.ResultBody) {
				data.ExchangeSourceKey = "not-a-source-key"
			},
		},
		{
			name: "peer missing",
			mutate: func(data *protocol.ChatAcceptWechatData, _ *protocol.ResultBody) {
				data.PeerWechat = ""
			},
		},
		{
			name: "evidence mismatch",
			mutate: func(_ *protocol.ChatAcceptWechatData, result *protocol.ResultBody) {
				result.Evidence[0].Type = "wrongEvidence"
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result := validResult()
			var data protocol.ChatAcceptWechatData
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
				t.Fatal("接受结果与原始意图不一致必须拒绝")
			}
		})
	}

	late, lateOutcome, err := parse(t, validResult(), store.CmdOk)
	if err != nil || lateOutcome != ocLate || late.Save || late.Effect != nil {
		t.Fatalf("终局后迟到 accept result 不得重建联系方式: plan=%+v outcome=%v err=%v",
			late, lateOutcome, err)
	}
}
