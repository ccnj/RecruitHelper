package dispatch

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

func sourcingDispatchRequest() DispatchRequest {
	args, _ := protocol.Encode(protocol.CandidateReadSourcingResumeArgs{
		ExcludePlatformUserRefs: []string{"excluded-user"},
	})
	return DispatchRequest{
		HandID: "hand-sourcing-dispatch", ExpectedSession: "s-test", ExpectedBootID: "boot-sourcing-dispatch",
		Name: protocol.PrimCandidateReadSourcingResume, Args: args,
		Context: &protocol.CmdContext{
			Platform: "zhilian", AccountRef: "account-sourcing-dispatch",
			ExpectedPrincipalFingerprint: "principal-sourcing-dispatch",
		},
	}
}

func sourcingIdentityDispatchRequest(name string, args json.RawMessage) DispatchRequest {
	return DispatchRequest{
		HandID: "hand-sourcing-dispatch", ExpectedSession: "s-test", ExpectedBootID: "boot-sourcing-dispatch",
		Name: name, Args: args,
		Context: &protocol.CmdContext{
			Platform: "zhilian", AccountRef: "account-sourcing-dispatch",
			ExpectedPrincipalFingerprint: "principal-sourcing-dispatch",
		},
	}
}

func TestSourcingResumeContractMismatchBlocksBeforeWAL(t *testing.T) {
	d, st, sender := newDisp(t)
	sender.up("hand-sourcing-dispatch", "boot-sourcing-dispatch")
	sender.negotiate("hand-sourcing-dispatch", []string{protocol.PrimCandidateReadSourcingResume + "@1"}, allM2Features)
	sender.setContractMatch("hand-sourcing-dispatch", false)
	if msgID, err := d.DispatchStructured(sourcingDispatchRequest()); msgID != "" || !errors.Is(err, ErrContractMismatch) {
		t.Fatalf("错版采集必须在 WAL 前阻断: msg=%q err=%v", msgID, err)
	}
	if rows, _ := st.RecentCmds(10); len(rows) != 0 {
		t.Fatalf("错版阻断不得留下命令: %+v", rows)
	}
}

func TestFormalSourcingIdentityReadsContractMismatchBlockBeforeWAL(t *testing.T) {
	tests := []struct {
		name string
		args any
	}{
		{name: protocol.PrimCandidateReadSourcingWindow, args: protocol.CandidateReadSourcingWindowArgs{Move: protocol.SourcingWindowMoveCurrent}},
		{name: protocol.PrimCandidateReadSourcingTargetResume, args: protocol.CandidateReadSourcingTargetResumeArgs{
			PlatformUserRef: "target-user", PositionRef: "target-position",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d, st, sender := newDisp(t)
			sender.up("hand-sourcing-dispatch", "boot-sourcing-dispatch")
			sender.negotiate("hand-sourcing-dispatch", []string{test.name + "@1"}, allM2Features)
			sender.setContractMatch("hand-sourcing-dispatch", false)
			args, err := protocol.Encode(test.args)
			if err != nil {
				t.Fatal(err)
			}
			if msgID, err := d.DispatchStructured(sourcingIdentityDispatchRequest(test.name, args)); msgID != "" || !errors.Is(err, ErrContractMismatch) {
				t.Fatalf("错版身份读取必须在 WAL 前阻断: msg=%q err=%v", msgID, err)
			}
			if rows, _ := st.RecentCmds(10); len(rows) != 0 {
				t.Fatalf("错版阻断不得留下命令: %+v", rows)
			}
		})
	}
}

func TestSourcingResumeExcludedEchoBecomesFixedFailedResult(t *testing.T) {
	d, st, sender := newDisp(t)
	sender.up("hand-sourcing-dispatch", "boot-sourcing-dispatch")
	sender.negotiate("hand-sourcing-dispatch", []string{protocol.PrimCandidateReadSourcingResume + "@1"}, allM2Features)
	msgID, err := d.DispatchStructured(sourcingDispatchRequest())
	if err != nil {
		t.Fatal(err)
	}
	dataRaw, _ := protocol.Encode(protocol.CandidateReadSourcingResumeData{
		PlatformUserRef: "excluded-user", PositionRef: "position-excluded",
		ContactState: protocol.CandidateContactStateUnestablished, ObservedAt: time.Now().UnixMilli(),
		Basic: []protocol.CandidateResumeLabelValue{}, Expectations: []protocol.CandidateResumeLabelValue{},
		SelfEvaluation: "", Education: "", WorkExperiences: "",
	})
	if outcome, _, err := d.applyResultMessage("hand-sourcing-dispatch", "result-sourcing-excluded", protocol.ResultBody{
		Ref: msgID, Status: protocol.ResultStatusOk, Data: dataRaw,
	}); err != nil || outcome != ocDone {
		t.Fatalf("排除回声终局化失败: outcome=%v err=%v", outcome, err)
	}
	cmd, _ := st.CmdByMsgID(msgID)
	if cmd == nil || cmd.Status != store.CmdFailed || cmd.ErrorCode != string(protocol.ErrCodeInternalHand) {
		raw, _ := json.Marshal(cmd)
		t.Fatalf("排除回声未被固定失败: %s", raw)
	}
}

func TestSourcingTargetResumeBindingMismatchBecomesFixedFailedResult(t *testing.T) {
	d, st, sender := newDisp(t)
	sender.up("hand-sourcing-dispatch", "boot-sourcing-dispatch")
	sender.negotiate("hand-sourcing-dispatch", []string{protocol.PrimCandidateReadSourcingTargetResume + "@1"}, allM2Features)
	args, _ := protocol.Encode(protocol.CandidateReadSourcingTargetResumeArgs{
		PlatformUserRef: "expected-user", PositionRef: "expected-position",
	})
	msgID, err := d.DispatchStructured(sourcingIdentityDispatchRequest(protocol.PrimCandidateReadSourcingTargetResume, args))
	if err != nil {
		t.Fatal(err)
	}
	dataRaw, _ := protocol.Encode(protocol.CandidateReadSourcingResumeData{
		PlatformUserRef: "other-user", PositionRef: "expected-position",
		ContactState: protocol.CandidateContactStateUnestablished, ObservedAt: time.Now().UnixMilli(),
		Basic: []protocol.CandidateResumeLabelValue{}, Expectations: []protocol.CandidateResumeLabelValue{},
		SelfEvaluation: "", Education: "", WorkExperiences: "",
	})
	if outcome, _, err := d.applyResultMessage("hand-sourcing-dispatch", "result-sourcing-target-mismatch", protocol.ResultBody{
		Ref: msgID, Status: protocol.ResultStatusOk, Data: dataRaw,
	}); err != nil || outcome != ocDone {
		t.Fatalf("定点结果错绑终局化失败: outcome=%v err=%v", outcome, err)
	}
	cmd, _ := st.CmdByMsgID(msgID)
	if cmd == nil || cmd.Status != store.CmdFailed || cmd.ErrorCode != string(protocol.ErrCodeInternalHand) {
		raw, _ := json.Marshal(cmd)
		t.Fatalf("定点结果错绑未被固定失败: %s", raw)
	}
}

func TestSourcingWindowDuplicateIdentityBecomesFixedFailedResult(t *testing.T) {
	d, st, sender := newDisp(t)
	sender.up("hand-sourcing-dispatch", "boot-sourcing-dispatch")
	sender.negotiate("hand-sourcing-dispatch", []string{protocol.PrimCandidateReadSourcingWindow + "@1"}, allM2Features)
	args, _ := protocol.Encode(protocol.CandidateReadSourcingWindowArgs{Move: protocol.SourcingWindowMoveCurrent})
	msgID, err := d.DispatchStructured(sourcingIdentityDispatchRequest(protocol.PrimCandidateReadSourcingWindow, args))
	if err != nil {
		t.Fatal(err)
	}
	dataRaw, _ := protocol.Encode(protocol.CandidateReadSourcingWindowData{
		PositionRef: "position-window", PlatformUserRefs: []string{"duplicate-user", "duplicate-user"},
		Moved: false, ObservedAt: time.Now().UnixMilli(),
	})
	if outcome, _, err := d.applyResultMessage("hand-sourcing-dispatch", "result-sourcing-window-duplicate", protocol.ResultBody{
		Ref: msgID, Status: protocol.ResultStatusOk, Data: dataRaw,
	}); err != nil || outcome != ocDone {
		t.Fatalf("重复身份窗口终局化失败: outcome=%v err=%v", outcome, err)
	}
	cmd, _ := st.CmdByMsgID(msgID)
	if cmd == nil || cmd.Status != store.CmdFailed || cmd.ErrorCode != string(protocol.ErrCodeInternalHand) {
		raw, _ := json.Marshal(cmd)
		t.Fatalf("重复身份窗口未被固定失败: %s", raw)
	}
}
