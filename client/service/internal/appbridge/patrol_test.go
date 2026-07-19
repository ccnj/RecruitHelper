package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/patrol"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

type completingSender struct {
	dispatcher *dispatch.Dispatcher
	cmdBodies  []protocol.CmdBody
}

func (s *completingSender) SendEnvelope(handID string, env protocol.Envelope) error {
	if env.Kind != protocol.KindCmd {
		return nil
	}
	var body protocol.CmdBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		return err
	}
	s.cmdBodies = append(s.cmdBodies, body)
	fingerprint := "opaque-observed-principal"
	data, err := json.Marshal(protocol.ProbePlatformData{
		ContentScriptOk:      true,
		LoginState:           protocol.LoginStateIn,
		PageKind:             protocol.PageKindIm,
		PrincipalFingerprint: &fingerprint,
		Surface:              &protocol.PlatformSurface{ImListVisible: true},
	})
	if err != nil {
		return err
	}
	s.dispatcher.OnAck(handID, protocol.AckBody{Ref: env.MsgID, Status: protocol.AckStatusAccepted})
	s.dispatcher.OnResult(handID, "result-"+env.MsgID, protocol.ResultBody{
		Ref: env.MsgID, Status: protocol.ResultStatusOk, Data: data,
		ExecMs: 1, Replayed: false,
	})
	return nil
}

func (*completingSender) HandSession(string) (string, string, bool) {
	return "session-1", "boot-1", true
}

func (*completingSender) HandNegotiation(string) ([]string, []string, bool) {
	return []string{protocol.PrimProbePlatform + "@1"}, nil, true
}

func (*completingSender) CloseHand(string, string, string) bool { return true }
func (*completingSender) HandOfflineMs(string) int64            { return 0 }

func newPatrolRunnerHarness(t *testing.T) (PatrolRunner, *completingSender, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sender := &completingSender{}
	dispatcher := dispatch.New(st, sender)
	sender.dispatcher = dispatcher
	return PatrolRunner{Dispatcher: dispatcher}, sender, st
}

func TestProbeUsesFormalContextlessBindingPathAndActorProbeKeepsContext(t *testing.T) {
	runner, sender, st := newPatrolRunnerHarness(t)
	probe, err := runner.Probe(context.Background(), "hand-1")
	if err != nil || probe.PrincipalFingerprint == nil {
		t.Fatalf("绑定前正式 probe 失败: probe=%+v err=%v", probe, err)
	}
	if len(sender.cmdBodies) != 1 || sender.cmdBodies[0].Name != protocol.PrimProbePlatform || sender.cmdBodies[0].Context != nil {
		t.Fatalf("绑定前 Probe 必须发送唯一的无 context probe.platform: %+v", sender.cmdBodies)
	}
	rows, err := st.RecentCmds(1)
	if err != nil || len(rows) != 1 || rows[0].Domain != "probe:hand-1" || rows[0].ContextJSON != "" {
		t.Fatalf("绑定前 probe 落账错误: rows=%+v err=%v", rows, err)
	}

	_, err = runner.Run(context.Background(), patrol.RunRequest{
		HandID: "hand-1", ExpectedSession: "session-1", ExpectedBootID: "boot-1",
		Name: protocol.PrimProbePlatform, Version: 1,
		Platform: "zhilian", AccountRef: "acct-1",
		ExpectedPrincipalFingerprint: "opaque-expected-principal",
		Args:                         json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("已绑定 actor probe 失败: %v", err)
	}
	if len(sender.cmdBodies) != 2 || sender.cmdBodies[1].Context == nil {
		t.Fatalf("actor probe 必须保留完整 context: %+v", sender.cmdBodies)
	}
	got := sender.cmdBodies[1].Context
	if got.Platform != "zhilian" || got.AccountRef != "acct-1" || got.ExpectedPrincipalFingerprint != "opaque-expected-principal" {
		t.Fatalf("actor probe context 被改写: %+v", got)
	}
}

func TestResultDataTranslatesGeneratedResult(t *testing.T) {
	okBody, err := json.Marshal(protocol.ResultBody{
		Ref: "m-1", Status: protocol.ResultStatusOk, Data: json.RawMessage(`{"value":7}`),
		ExecMs: 1, Replayed: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := resultData(store.CmdRecord{Status: store.CmdOk, ResultBody: string(okBody)})
	if err != nil || string(data) != `{"value":7}` {
		t.Fatalf("成功 data 翻译错误: data=%s err=%v", data, err)
	}

	reason := protocol.NotReadyReasonContentScriptDead
	errorData, _ := json.Marshal(map[string]any{"reason": reason})
	failedBody, err := json.Marshal(protocol.ResultBody{
		Ref: "m-2", Status: protocol.ResultStatusFailed, ExecMs: 2,
		Error: &protocol.ErrorBody{
			Code: protocol.ErrCodeCtxNotReady, Data: errorData,
			Message: "页面脚本未就绪", Retryable: protocol.RetryableAfterRecovery,
			SideEffect: protocol.SideEffectNone,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = resultData(store.CmdRecord{Status: store.CmdFailed, ResultBody: string(failedBody)})
	var runErr *patrol.RunError
	if !errors.As(err, &runErr) || runErr.Code != protocol.ErrCodeCtxNotReady || runErr.Reason != reason ||
		runErr.Retryable != protocol.RetryableAfterRecovery || runErr.SideEffect != protocol.SideEffectNone {
		t.Fatalf("机器错误翻译错误: %#v", err)
	}
}

func TestResultDataRejectsTerminalWithoutResult(t *testing.T) {
	_, err := resultData(store.CmdRecord{Status: store.CmdVoid})
	var runErr *patrol.RunError
	if !errors.As(err, &runErr) || runErr.Code != protocol.ErrCodeCtxLostDuringExec {
		t.Fatalf("void 应转可判定运行错误: %#v", err)
	}
}
