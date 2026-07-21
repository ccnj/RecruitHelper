package dispatch

import (
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

func seedResumeDispatchTarget(
	t *testing.T,
	d *Dispatcher,
	st *store.Store,
	m *mockSender,
	slug string,
) greetingDispatchFixture {
	t.Helper()
	fixture := seedGreetingTarget(t, st, m, slug)
	receipt, err := d.SendGreeting(sendGreetingRequest(fixture, "intent-resume-"+slug, ""))
	if err != nil {
		t.Fatal(err)
	}
	if outcome, _, err := d.applyResultMessage(
		fixture.HandID, "result-resume-greeting-"+slug, validGreetingResult(receipt.MsgID, fixture),
	); err != nil || outcome != ocDone {
		t.Fatalf("建立 greeted/adopted 目标失败: outcome=%v err=%v", outcome, err)
	}
	if _, err := st.SelectM5TrialProfile(fixture.ProfileID, "trial-"+slug, "user", time.Now()); err != nil {
		t.Fatal(err)
	}
	m.negotiate(fixture.HandID, []string{
		protocol.PrimCandidateReadResume + "@1",
	}, allM2Features)
	return fixture
}

func resumeDispatchRequest(fixture greetingDispatchFixture) ResumeCaptureDispatchRequest {
	return ResumeCaptureDispatchRequest{
		ProfileID: fixture.ProfileID, HandID: fixture.HandID,
		ExpectedSession: "s-test", ExpectedBootID: "boot-greeting",
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		ExpectedPrincipalFingerprint: "principal-" + fixture.ProfileID[len("profile-"):],
	}
}

func TestResumeCaptureMismatchBlocksBeforeWALAndInflightReattaches(t *testing.T) {
	d, st, m := newDisp(t)
	fixture := seedResumeDispatchTarget(t, d, st, m, "mismatch")
	req := resumeDispatchRequest(fixture)
	m.setContractMatch(fixture.HandID, false)
	if receipt, err := d.DispatchResumeCapture(req); receipt != nil || !errors.Is(err, ErrContractMismatch) {
		t.Fatalf("contract mismatch 必须在 WAL 前阻断: receipt=%+v err=%v", receipt, err)
	}
	profile, _ := st.CandidateProfileByID(fixture.ProfileID)
	if profile.ResumeCaptureState != store.ResumeCaptureUnattempted || profile.ResumeCaptureLogicalDispatchID != nil {
		t.Fatalf("mismatch 不得制造补采事实: %+v", profile)
	}
	m.setContractMatch(fixture.HandID, true)
	sentBefore := m.sentCount()
	first, err := d.DispatchResumeCapture(req)
	if err != nil || first == nil || !first.Created || first.LogicalDispatchID == "" {
		t.Fatalf("首次补采派发失败: receipt=%+v err=%v", first, err)
	}
	if m.sentCount() != sentBefore+1 {
		t.Fatal("首次补采必须恰好写一个 cmd 信封")
	}
	repeated, err := d.DispatchResumeCapture(req)
	if err != nil || repeated == nil || repeated.Created || repeated.LogicalDispatchID != first.LogicalDispatchID {
		t.Fatalf("重复巡检未附着原 logical: first=%+v repeated=%+v err=%v", first, repeated, err)
	}
	if m.sentCount() != sentBefore+1 {
		t.Fatal("附着 inFlight 不得再次写 socket")
	}
}

func TestResumeCaptureEchoMismatchBecomesFixedFailedResult(t *testing.T) {
	d, st, m := newDisp(t)
	fixture := seedResumeDispatchTarget(t, d, st, m, "echo")
	receipt, err := d.DispatchResumeCapture(resumeDispatchRequest(fixture))
	if err != nil {
		t.Fatal(err)
	}
	dataRaw, _ := protocol.Encode(protocol.CandidateReadResumeData{
		ConversationRef: fixture.ConversationRef, PlatformUserRef: "wrong-target",
		ObservedAt: time.Now().UnixMilli(), Basic: []protocol.CandidateResumeLabelValue{},
		Expectations: []protocol.CandidateResumeLabelValue{}, SelfEvaluation: "", Education: "", WorkExperiences: "",
	})
	if outcome, _, err := d.applyResultMessage(fixture.HandID, "result-resume-echo", protocol.ResultBody{
		Ref: receipt.LogicalDispatchID, Status: protocol.ResultStatusOk, Data: dataRaw,
	}); err != nil || outcome != ocDone {
		t.Fatalf("echo mismatch 入账失败: outcome=%v err=%v", outcome, err)
	}
	cmd, err := st.CmdByMsgID(receipt.LogicalDispatchID)
	if err != nil || cmd == nil || cmd.Status != store.CmdFailed || cmd.ErrorCode != string(protocol.ErrCodeInternalHand) {
		t.Fatalf("echo mismatch 未被固定失败收敛: cmd=%+v err=%v", cmd, err)
	}
}
