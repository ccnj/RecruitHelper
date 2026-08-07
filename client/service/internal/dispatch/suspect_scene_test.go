package dispatch

// suspect 现场截图取证(2026-08-07 甲方裁决)的脑侧编排测试:招呼验证耗尽转
// suspect 后必须异步派 debug.capturePage,成功则落 SuspectSceneShot 事实行;
// 截图失败只留审计,不重试、不影响 suspect 判定本身。

import (
	"encoding/json"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// driveGreetingToSuspect 把一条招呼推进到验证耗尽转 suspect,返回 suspect 命令。
func driveGreetingToSuspect(t *testing.T, d *Dispatcher, st *store.Store, fixture greetingDispatchFixture, intentID string) *store.CmdRecord {
	t.Helper()
	release := make(chan struct{})
	d.SetEffectVerifier(blockingVerifier{release: release})
	t.Cleanup(func() { close(release) })
	receipt, err := d.SendGreeting(sendGreetingRequest(fixture, intentID, ""))
	if err != nil {
		t.Fatal(err)
	}
	d.OnResult(fixture.HandID, "result-"+intentID, protocol.ResultBody{
		Ref: receipt.MsgID, Status: protocol.ResultStatusFailed,
		Error: &protocol.ErrorBody{
			Code:      protocol.ErrCodePostconditionUnconfirmed,
			Message:   "最终发送只调用一次，但未确认同一候选人的关系状态变为已建立",
			Retryable: protocol.RetryableManualOnly, SideEffect: protocol.SideEffectPossible,
		},
	})
	for i := 0; i < 10; i++ {
		fresh, _ := st.CmdByMsgID(receipt.MsgID)
		if fresh == nil || fresh.Status != store.CmdVerifying {
			break
		}
		d.recordVerificationMiss(*fresh, "本轮未取得同一目标的关系已建立正证")
	}
	final, _ := st.CmdByMsgID(receipt.MsgID)
	if final == nil || final.Status != store.CmdSuspect {
		t.Fatalf("招呼未转 suspect: %+v", final)
	}
	return final
}

// awaitCapturePageCmd 等待异步截图 goroutine 把 debug.capturePage 派到手上。
func awaitCapturePageCmd(t *testing.T, m *mockSender) (msgID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		for _, envelope := range m.sent {
			if envelope.Kind != protocol.KindCmd {
				continue
			}
			var body protocol.CmdBody
			if json.Unmarshal(envelope.Body, &body) == nil && body.Name == protocol.PrimDebugCapturePage {
				msgID = envelope.MsgID
			}
		}
		m.mu.Unlock()
		if msgID != "" {
			return msgID
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("suspect 后未派发 debug.capturePage")
	return ""
}

func TestGreetingSuspectCapturesSceneShot(t *testing.T) {
	d, st, m := newDisp(t)
	fixture := seedGreetingTarget(t, st, m, "scene-shot")
	m.negotiate(fixture.HandID, []string{
		protocol.PrimChatSendGreeting + "@1",
		protocol.PrimChatReadGreetingOutcome + "@1",
		protocol.PrimDebugCapturePage + "@1",
	}, append(append([]string(nil), allM2Features...), string(protocol.FeatureWitness1)))
	suspect := driveGreetingToSuspect(t, d, st, fixture, "intent-scene-shot")

	captureMsgID := awaitCapturePageCmd(t, m)
	data, _ := protocol.Encode(protocol.CaptureScreenshotData{
		ImageBlobRef: "sha256:abcdef", ByteSize: 12345,
		Truncated: false, CapturedAt: time.Now().UnixMilli(),
	})
	d.OnResult(fixture.HandID, "result-scene-shot-capture", protocol.ResultBody{
		Ref: captureMsgID, Status: protocol.ResultStatusOk, Data: data,
	})

	var rows []store.SuspectSceneShot
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rows, _ = st.SuspectSceneShotsByMsgID(suspect.MsgID)
		if len(rows) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(rows) != 1 {
		t.Fatalf("suspect 现场截图事实行未落库: %+v", rows)
	}
	if rows[0].Primitive != protocol.PrimChatSendGreeting || rows[0].BlobRef == "" ||
		rows[0].IntentID != suspect.IntentID {
		t.Fatalf("事实行内容不完整: %+v", rows[0])
	}
	if !hasAudit(t, st, "suspect_scene_captured", suspect.MsgID) {
		t.Fatal("截图成功必须留审计")
	}
}

func TestGreetingSuspectSceneCaptureFailureOnlyAudits(t *testing.T) {
	d, st, m := newDisp(t)
	fixture := seedGreetingTarget(t, st, m, "scene-fail")
	// 刻意不协商 debug.capturePage@1:派发在能力门就失败,等价于手侧无该能力。
	suspect := driveGreetingToSuspect(t, d, st, fixture, "intent-scene-fail")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hasAudit(t, st, "suspect_scene_capture_failed", suspect.MsgID) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !hasAudit(t, st, "suspect_scene_capture_failed", suspect.MsgID) {
		t.Fatal("截图失败必须留审计")
	}
	rows, _ := st.SuspectSceneShotsByMsgID(suspect.MsgID)
	if len(rows) != 0 {
		t.Fatalf("失败不得落事实行: %+v", rows)
	}
	final, _ := st.CmdByMsgID(suspect.MsgID)
	if final == nil || final.Status != store.CmdSuspect {
		t.Fatalf("取证失败不得改变 suspect 判定: %+v", final)
	}
}
