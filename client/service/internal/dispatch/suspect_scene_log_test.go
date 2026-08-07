package dispatch

// 2026-08-07 甲方裁决立案的招呼 suspect 取证(脑侧半边):验证耗尽转 suspect 时,
// suspect.created 命名事件必须携带手侧终局的 error.message——平台浮层原话与
// 弹窗状态就在其中——让现场经日志上报当天到服务器,而不是等次日诊断包。

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

func TestVerificationExhaustedSuspectLogCarriesHandErrorMessage(t *testing.T) {
	d, st, m := newDisp(t)
	fixture := seedGreetingTarget(t, st, m, "scene-log")
	release := make(chan struct{})
	d.SetEffectVerifier(blockingVerifier{release: release})
	defer close(release)
	receipt, err := d.SendGreeting(sendGreetingRequest(fixture, "intent-scene-log", ""))
	if err != nil {
		t.Fatal(err)
	}
	sceneMessage := "最终发送只调用一次，但未确认同一候选人的关系状态变为已建立" +
		"；发送后第2轮观察到新浮层「今日沟通人数已达上限」"
	d.OnResult(fixture.HandID, "result-scene-log", protocol.ResultBody{
		Ref: receipt.MsgID, Status: protocol.ResultStatusFailed,
		Error: &protocol.ErrorBody{
			Code: protocol.ErrCodePostconditionUnconfirmed, Message: sceneMessage,
			Retryable: protocol.RetryableManualOnly, SideEffect: protocol.SideEffectPossible,
		},
	})
	cmd, _ := st.CmdByMsgID(receipt.MsgID)
	if cmd == nil || cmd.Status != store.CmdVerifying {
		t.Fatalf("歧义失败必须先进验证轨: %+v", cmd)
	}

	var out bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&out, nil)))
	defer slog.SetDefault(previous)
	for i := 0; i < 10; i++ {
		fresh, _ := st.CmdByMsgID(receipt.MsgID)
		if fresh == nil || fresh.Status != store.CmdVerifying {
			break
		}
		d.recordVerificationMiss(*fresh, "本轮未取得同一目标的关系已建立正证")
	}
	final, _ := st.CmdByMsgID(receipt.MsgID)
	if final == nil || final.Status != store.CmdSuspect {
		t.Fatalf("验证耗尽必须转 suspect: %+v", final)
	}
	logged := out.String()
	if !strings.Contains(logged, "event=suspect.created") {
		t.Fatalf("验证耗尽的 suspect 必须发 suspect.created 命名事件: %s", logged)
	}
	if !strings.Contains(logged, "今日沟通人数已达上限") {
		t.Fatalf("手侧现场纪要必须随行 suspect 日志: %s", logged)
	}
}

func TestHandResultErrorMessage(t *testing.T) {
	if got := handResultErrorMessage(""); got != "" {
		t.Fatalf("空 body 应返回空: %q", got)
	}
	if got := handResultErrorMessage("{broken"); got != "" {
		t.Fatalf("坏 JSON 应返回空: %q", got)
	}
	if got := handResultErrorMessage(`{"status":"ok"}`); got != "" {
		t.Fatalf("无 error 字段应返回空: %q", got)
	}
	if got := handResultErrorMessage(`{"error":{"message":"现场纪要"}}`); got != "现场纪要" {
		t.Fatalf("应取出 error.message: %q", got)
	}
}
