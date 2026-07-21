package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/patrol"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

type resumeRestartSender struct {
	dispatcher     *dispatch.Dispatcher
	completeResume bool
	resumeData     protocol.CandidateReadResumeData
}

func (s *resumeRestartSender) SendEnvelope(handID string, env protocol.Envelope) error {
	if env.Kind != protocol.KindCmd {
		return nil
	}
	var body protocol.CmdBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		return err
	}
	s.dispatcher.OnAck(handID, protocol.AckBody{Ref: env.MsgID, Status: protocol.AckStatusAccepted})
	switch body.Name {
	case protocol.PrimChatSendGreeting:
		var args protocol.ChatSendGreetingArgs
		if err := json.Unmarshal(body.Args, &args); err != nil {
			return err
		}
		data, _ := protocol.Encode(protocol.ChatSendGreetingData{
			PlatformUserRef: args.PlatformUserRef, PositionRef: args.PositionRef,
			ConversationRef: "conversation-resume-restart", ContentHash: syncledger.HashText(args.Text),
			ObservedAt: time.Now().UnixMilli(),
		})
		s.dispatcher.OnResult(handID, "result-"+env.MsgID, protocol.ResultBody{
			Ref: env.MsgID, Status: protocol.ResultStatusOk, Data: data,
			Evidence: []protocol.Evidence{{Type: string(protocol.SendGreetingEvidenceTypeOutboundGreetingObserved)}},
		})
	case protocol.PrimCandidateReadResume:
		if s.completeResume {
			data, _ := protocol.Encode(s.resumeData)
			s.dispatcher.OnResult(handID, "result-"+env.MsgID, protocol.ResultBody{
				Ref: env.MsgID, Status: protocol.ResultStatusOk, Data: data, ExecMs: 1,
			})
		}
	}
	return nil
}

func (*resumeRestartSender) HandSession(string) (string, string, bool) {
	return "session-resume-restart", "boot-resume-restart", true
}
func (*resumeRestartSender) HandNegotiation(string) ([]string, []string, bool) {
	return []string{
			protocol.PrimChatSendGreeting + "@1", protocol.PrimCandidateReadResume + "@1",
		}, []string{
			string(protocol.FeatureWitness1), string(protocol.FeatureLease1),
			string(protocol.FeatureProgress1), string(protocol.FeatureCancel1),
		}, true
}
func (*resumeRestartSender) HandContractMatch(string) (bool, bool) { return true, true }
func (*resumeRestartSender) HandWitness(string) (dispatch.HandWitness, bool) {
	return dispatch.HandWitness{StoreID: "witness-resume-restart"}, true
}
func (*resumeRestartSender) CloseHand(string, string, string) bool { return true }
func (*resumeRestartSender) HandOfflineMs(string) int64            { return 0 }

type resumeRestartFixture struct {
	store      *store.Store
	sender     *resumeRestartSender
	dispatcher *dispatch.Dispatcher
	request    patrol.ResumeCaptureRequest
	data       protocol.CandidateReadResumeData
}

func newResumeRestartFixture(t *testing.T, complete bool) resumeRestartFixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const (
		platform   = "zhilian"
		accountRef = "account-resume-restart"
		handID     = "hand-resume-restart"
		profileID  = "profile-resume-restart"
		userRef    = "person-resume-restart"
		position   = "position-resume-restart"
		principal  = "principal-resume-restart"
	)
	if err := st.CreateAccount(&store.Account{Platform: platform, AccountRef: accountRef}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccountPrincipal(
		store.AccountKey{Platform: platform, AccountRef: accountRef}, handID, principal,
		"session-resume-restart", "boot-resume-restart", time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	display, title := "合成候选人", "合成职位"
	if _, err := st.SelectCandidateProfile(store.SelectCandidateProfileRequest{
		ProfileID: profileID,
		Scope: store.CandidateProfileScope{
			Platform: platform, AccountRef: accountRef, PlatformUserRef: userRef, PositionRef: position,
		},
		DisplayName: &display, PositionTitle: &title, ObservedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	data := protocol.CandidateReadResumeData{
		ConversationRef: "conversation-resume-restart", PlatformUserRef: userRef,
		ObservedAt:   time.Now().UnixMilli(),
		Basic:        []protocol.CandidateResumeLabelValue{{Label: "合成", Value: "值"}},
		Expectations: []protocol.CandidateResumeLabelValue{}, SelfEvaluation: "",
		Education: "合成教育", WorkExperiences: "合成经历",
	}
	sender := &resumeRestartSender{completeResume: complete, resumeData: data}
	d := dispatch.New(st, sender)
	sender.dispatcher = d
	if _, err := d.SendGreeting(dispatch.SendGreetingRequest{
		IntentID: "intent-resume-restart", ProfileID: profileID, Text: "你好",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SelectM5TrialProfile(profileID, "trial-resume-restart", "user", time.Now()); err != nil {
		t.Fatal(err)
	}
	return resumeRestartFixture{
		store: st, sender: sender, dispatcher: d, data: data,
		request: patrol.ResumeCaptureRequest{
			ProfileID: profileID, HandID: handID,
			ExpectedSession: "session-resume-restart", ExpectedBootID: "boot-resume-restart",
			Platform: platform, AccountRef: accountRef, ExpectedPrincipalFingerprint: principal,
		},
	}
}

func candidateResumeCmdCount(t *testing.T, st *store.Store) int {
	t.Helper()
	rows, err := st.RecentCmds(100)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for i := range rows {
		if rows[i].Name == protocol.PrimCandidateReadResume {
			n++
		}
	}
	return n
}

func TestResumeCaptureRestartConsumesPersistedOKWithoutNewCommand(t *testing.T) {
	fixture := newResumeRestartFixture(t, true)
	first, err := (PatrolRunner{Dispatcher: fixture.dispatcher}).StartResumeCapture(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	root := first.LogicalDispatchID()
	if candidateResumeCmdCount(t, fixture.store) != 1 {
		t.Fatal("首次补采必须只有一个物理命令")
	}

	d2 := dispatch.New(fixture.store, fixture.sender)
	fixture.sender.dispatcher = d2
	d2.Recover()
	reattached, err := (PatrolRunner{Dispatcher: d2}).StartResumeCapture(context.Background(), fixture.request)
	if err != nil || reattached.LogicalDispatchID() != root {
		t.Fatalf("重启后未附着原 root: handle=%+v err=%v", reattached, err)
	}
	raw, err := reattached.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var data protocol.CandidateReadResumeData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CompleteResumeCapture(store.CompleteResumeCaptureRequest{
		ProfileID: fixture.request.ProfileID, LogicalDispatchID: root,
		SnapshotID: "snapshot-resume-restart", Data: data,
	}); err != nil {
		t.Fatal(err)
	}
	if candidateResumeCmdCount(t, fixture.store) != 1 {
		t.Fatal("消费已终局结果不得增生物理命令")
	}
	profile, _ := fixture.store.CandidateProfileByID(fixture.request.ProfileID)
	if profile.ResumeCaptureState != store.ResumeCaptureCaptured {
		t.Fatalf("已终局结果未补建快照: %+v", profile)
	}
}

func TestResumeCaptureRestartVoidsInflightAndConvergesManualOnSameRoot(t *testing.T) {
	fixture := newResumeRestartFixture(t, false)
	first, err := (PatrolRunner{Dispatcher: fixture.dispatcher}).StartResumeCapture(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	root := first.LogicalDispatchID()
	d2 := dispatch.New(fixture.store, fixture.sender)
	fixture.sender.dispatcher = d2
	d2.Recover()
	reattached, err := (PatrolRunner{Dispatcher: d2}).StartResumeCapture(context.Background(), fixture.request)
	if err != nil || reattached.LogicalDispatchID() != root {
		t.Fatalf("void 后未附着原 root: handle=%+v err=%v", reattached, err)
	}
	_, err = reattached.Wait(context.Background())
	var runErr *patrol.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("void leaf 应返回脱敏终局错误: %v", err)
	}
	if err := fixture.store.FailResumeCapture(store.FailResumeCaptureRequest{
		ProfileID: fixture.request.ProfileID, LogicalDispatchID: root,
		Reason: "bindingLost", At: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if candidateResumeCmdCount(t, fixture.store) != 1 {
		t.Fatal("脑重启不得为在途 intrusive 创建 replacement 或新 capture")
	}
	lateData, _ := protocol.Encode(fixture.data)
	d2.OnResult(fixture.request.HandID, "late-result-"+root, protocol.ResultBody{
		Ref: root, Status: protocol.ResultStatusOk, Data: lateData,
	})
	cmd, _ := fixture.store.CmdByMsgID(root)
	profile, _ := fixture.store.CandidateProfileByID(fixture.request.ProfileID)
	if cmd.Status != store.CmdVoid || profile.ResumeCaptureState != store.ResumeCaptureManualRequired {
		t.Fatalf("迟到 result 不得复活 void 或人工收敛: cmd=%+v profile=%+v", cmd, profile)
	}
}
