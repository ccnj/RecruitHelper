package dispatch

import (
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
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

func seedInboundResumeDispatchTarget(
	t *testing.T,
	st *store.Store,
	m *mockSender,
) (greetingDispatchFixture, ResumeCaptureDispatchRequest) {
	t.Helper()
	at := time.Date(2026, 7, 26, 10, 30, 0, 0, time.UTC)
	fixture := greetingDispatchFixture{
		Platform:        "zhilian",
		AccountRef:      "account-inbound-resume-dispatch",
		PlatformUserRef: "person-inbound-resume-dispatch",
		PositionRef:     "71",
		HandID:          "hand-inbound-resume-dispatch",
		ConversationRef: "conversation-inbound-resume-dispatch",
	}
	const (
		bootID    = "boot-inbound-resume-dispatch"
		principal = "principal-inbound-resume-dispatch"
	)
	m.up(fixture.HandID, bootID)
	m.negotiate(fixture.HandID, []string{
		protocol.PrimCandidateReadResume + "@1",
	}, allM2Features)
	if err := st.CreateAccount(&store.Account{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccountPrincipal(
		store.AccountKey{Platform: fixture.Platform, AccountRef: fixture.AccountRef},
		fixture.HandID, principal, "s-test", bootID, at,
	); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveConversationList(store.SaveConversationListRequest{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		ObservedAt: at, Complete: true,
		Entries: []store.ListIndexEntry{{
			ConversationRef: fixture.ConversationRef,
			PlatformUserRef: fixture.PlatformUserRef,
			PeerDisplayName: "候选人甲",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	replyPrompt := "合成回复:{简历}/{推荐时段}/{对话历史}/{话术_序列}"
	intentPrompt := "合成意向:{回复}/{招呼语}"
	facts := "合成客户事实"
	documents := []m5ai.JobConfigDocument{
		{DocType: "多轮沟通", Content: replyPrompt},
		{DocType: "客户事实库", Content: facts},
		{DocType: "意向判断", Content: intentPrompt},
	}
	sort.Slice(documents, func(i, j int) bool {
		return documents[i].DocType < documents[j].DocType
	})
	if _, err := st.SaveCurrentLegacyJobAIContext([]m5ai.ContextRevision{{
		ContextID: "context-inbound-resume-dispatch", RevisionHash: "revision-inbound-resume-dispatch",
		SourceKind: "legacyJobConfig", SourceJobRef: fixture.PositionRef,
		DisplayName: "客户经理", Environment: "online",
		SourcePackage: m5ai.JobConfigDocumentPackage{Documents: documents},
		Communication: m5ai.CommunicationView{
			ReplyPrompt: replyPrompt, IntentPrompt: intentPrompt,
			CustomerFacts: facts, MappingVersion: m5ai.MappingVersion,
		},
		CreatedAt: at,
	}}, at); err != nil {
		t.Fatal(err)
	}
	adopted, err := st.AdoptInboundConversationProfile(store.AdoptInboundConversationProfileRequest{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		ConversationRef: fixture.ConversationRef, PlatformUserRef: fixture.PlatformUserRef,
		DisplayName: "候选人甲", PositionTitle: "客户经理", ObservedAt: at.Add(time.Second),
	})
	if err != nil || adopted == nil || adopted.Profile == nil {
		t.Fatalf("准备主动来聊档案失败: adopted=%+v err=%v", adopted, err)
	}
	fixture.ProfileID = adopted.Profile.ProfileID
	text := "合成主动来聊消息"
	sourceKey := strings.Repeat("a", 64)
	if _, err := st.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: store.ConversationKey{
			Platform: fixture.Platform, AccountRef: fixture.AccountRef,
			ConversationRef: fixture.ConversationRef,
		},
		ExpectedTailSeq: 0, PlatformUserRef: fixture.PlatformUserRef,
		NewMessages: []store.MessageDraft{{
			Direction: "in", Kind: "text", Text: &text,
			ContentHash: strings.Repeat("b", 64), Origin: "external", SourceKey: &sourceKey,
		}},
		Adopt: true, SyncedAt: at.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	return fixture, ResumeCaptureDispatchRequest{
		ProfileID: fixture.ProfileID, HandID: fixture.HandID,
		ExpectedSession: "s-test", ExpectedBootID: bootID,
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
		ExpectedPrincipalFingerprint: principal,
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

func TestInboundResumeCaptureUsesSameDispatcherWithoutTrialSelection(t *testing.T) {
	d, st, m := newDisp(t)
	fixture, req := seedInboundResumeDispatchTarget(t, st, m)
	active, err := st.ActiveM5TrialForAccount(store.AccountKey{
		Platform: fixture.Platform, AccountRef: fixture.AccountRef,
	})
	if err != nil || active != nil {
		t.Fatalf("主动来聊补采不得伪造试运行选择: active=%+v err=%v", active, err)
	}

	sentBefore := m.sentCount()
	first, err := d.DispatchResumeCapture(req)
	if err != nil || first == nil || !first.Created || first.LogicalDispatchID == "" {
		t.Fatalf("主动来聊补采未通过正式派发器: receipt=%+v err=%v", first, err)
	}
	replayed, err := d.DispatchResumeCapture(req)
	if err != nil || replayed == nil || replayed.Created ||
		replayed.LogicalDispatchID != first.LogicalDispatchID {
		t.Fatalf("主动来聊 inFlight 未附着原 logical: first=%+v replayed=%+v err=%v", first, replayed, err)
	}
	if m.sentCount() != sentBefore+1 {
		t.Fatalf("主动来聊补采重复写 socket: before=%d after=%d", sentBefore, m.sentCount())
	}
}

func TestInboundResumeCaptureRejectsRequestAccountDriftBeforeWAL(t *testing.T) {
	d, st, m := newDisp(t)
	_, req := seedInboundResumeDispatchTarget(t, st, m)
	req.AccountRef = "different-account"
	sentBefore := m.sentCount()
	receipt, err := d.DispatchResumeCapture(req)
	if receipt != nil || !errors.Is(err, store.ErrM5TrialNotActive) {
		t.Fatalf("错账号主动来聊补采必须拒绝: receipt=%+v err=%v", receipt, err)
	}
	if m.sentCount() != sentBefore {
		t.Fatal("错账号主动来聊补采不得写 socket")
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
