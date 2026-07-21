package adminhttp

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

const (
	candidateTestPlatform    = "zhilian"
	candidateTestAccount     = "account-candidate"
	candidateTestHand        = "hand-candidate"
	candidateTestSession     = "session-candidate"
	candidateTestBoot        = "boot-candidate"
	candidateTestFingerprint = "principal-candidate"
)

type candidateReadSender struct {
	dispatcher *dispatch.Dispatcher
	data       protocol.CandidateReadCurrentData
	bodies     []protocol.CmdBody
}

func (s *candidateReadSender) SendEnvelope(handID string, envelope protocol.Envelope) error {
	if envelope.Kind != protocol.KindCmd {
		return nil
	}
	var body protocol.CmdBody
	if err := json.Unmarshal(envelope.Body, &body); err != nil {
		return err
	}
	s.bodies = append(s.bodies, body)
	data, err := protocol.Encode(s.data)
	if err != nil {
		return err
	}
	s.dispatcher.OnAck(handID, protocol.AckBody{Ref: envelope.MsgID, Status: protocol.AckStatusAccepted})
	s.dispatcher.OnResult(handID, "result-"+envelope.MsgID, protocol.ResultBody{
		Ref: envelope.MsgID, Status: protocol.ResultStatusOk, Data: data, ExecMs: 1,
	})
	return nil
}

func (*candidateReadSender) HandSession(string) (string, string, bool) {
	return candidateTestSession, candidateTestBoot, true
}

func (*candidateReadSender) HandNegotiation(string) ([]string, []string, bool) {
	return []string{protocol.PrimCandidateReadCurrent + "@1"}, nil, true
}

func (*candidateReadSender) HandContractMatch(string) (bool, bool) { return true, true }

func (*candidateReadSender) CloseHand(string, string, string) bool { return true }
func (*candidateReadSender) HandOfflineMs(string) int64            { return 0 }

func seedCurrentCandidateAccount(t *testing.T, st *store.Store, hub *fakeAdminHub) store.AccountKey {
	t.Helper()
	key := store.AccountKey{Platform: candidateTestPlatform, AccountRef: candidateTestAccount}
	if err := st.CreateAccount(&store.Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccountPrincipal(
		key, candidateTestHand, candidateTestFingerprint,
		candidateTestSession, candidateTestBoot, time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	hub.set(candidateTestSession, candidateTestBoot, true)
	return key
}

func candidatePOST(t *testing.T, mux http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestCurrentCandidateReadAndSelectUsePersistedProofWithoutLeakingRawRefs(t *testing.T) {
	const rawUserRef = "RAW-PLATFORM-USER-MUST-NOT-LEAK-7ec116"
	const rawPositionRef = "RAW-POSITION-MUST-NOT-LEAK-fdcd67"
	displayName := "候选人甲"
	positionTitle := "后端工程师"

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hub := newFakeAdminHub()
	key := seedCurrentCandidateAccount(t, st, hub)
	sender := &candidateReadSender{data: protocol.CandidateReadCurrentData{
		PlatformUserRef: rawUserRef, DisplayName: &displayName,
		PositionRef: rawPositionRef, PositionTitle: &positionTitle,
		ContactState: protocol.CandidateContactStateUnestablished,
	}}
	dispatcher := dispatch.New(st, sender)
	sender.dispatcher = dispatcher
	api := New(st, hub, dispatcher, nil, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)

	var serviceLog bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&serviceLog, nil)))
	defer slog.SetDefault(oldLogger)

	read := candidatePOST(t, mux, "/admin/candidates/current/read", accountKeyRequest{
		Platform: key.Platform, AccountRef: key.AccountRef,
	})
	if read.Code != http.StatusOK {
		t.Fatalf("读取当前候选人失败: code=%d body=%s", read.Code, read.Body.String())
	}
	if bytes.Contains(read.Body.Bytes(), []byte(rawUserRef)) ||
		bytes.Contains(read.Body.Bytes(), []byte(rawPositionRef)) ||
		strings.Contains(serviceLog.String(), rawUserRef) || strings.Contains(serviceLog.String(), rawPositionRef) {
		t.Fatalf("HTTP 或服务日志泄漏平台原始引用: body=%s log=%s", read.Body.String(), serviceLog.String())
	}
	var preview currentCandidateView
	if err := json.Unmarshal(read.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.SelectionRef == "" || preview.DisplayName == nil || *preview.DisplayName != displayName ||
		preview.PositionTitle == nil || *preview.PositionTitle != positionTitle ||
		preview.ContactState != protocol.CandidateContactStateUnestablished {
		t.Fatalf("安全预览 DTO 错误: %+v", preview)
	}
	var rawPreview map[string]any
	_ = json.Unmarshal(read.Body.Bytes(), &rawPreview)
	if _, exists := rawPreview["platformUserRef"]; exists {
		t.Fatal("预览 DTO 不得返回 platformUserRef")
	}
	if _, exists := rawPreview["positionRef"]; exists {
		t.Fatal("预览 DTO 不得返回 positionRef")
	}
	if len(sender.bodies) != 1 || sender.bodies[0].Name != protocol.PrimCandidateReadCurrent ||
		sender.bodies[0].Context == nil ||
		sender.bodies[0].Context.ExpectedPrincipalFingerprint != candidateTestFingerprint {
		t.Fatalf("读取未走带身份 context 的正式命令: %+v", sender.bodies)
	}
	if candidate, err := st.CandidateByKey(store.CandidateKey{
		Platform: key.Platform, PlatformUserRef: rawUserRef,
	}); err != nil || candidate != nil {
		t.Fatalf("只读步骤不得创建 Candidate: candidate=%+v err=%v", candidate, err)
	}
	if profile, err := st.CandidateProfileByScope(store.CandidateProfileScope{
		Platform: key.Platform, AccountRef: key.AccountRef,
		PlatformUserRef: rawUserRef, PositionRef: rawPositionRef,
	}); err != nil || profile != nil {
		t.Fatalf("只读步骤不得创建 Profile: profile=%+v err=%v", profile, err)
	}
	rows, err := st.RecentCmds(10)
	if err != nil || len(rows) != 1 || rows[0].Name != protocol.PrimCandidateReadCurrent || rows[0].IntentID != "" {
		t.Fatalf("只读步骤只能留下无意图的正式命令: rows=%+v err=%v", rows, err)
	}
	terminalAt := rows[0].TerminalAt
	if terminalAt == nil {
		t.Fatal("候选人读取命令缺少 terminalAt")
	}

	selectResponse := candidatePOST(t, mux, "/admin/candidates/current/select", map[string]string{
		"selectionRef": preview.SelectionRef,
	})
	if selectResponse.Code != http.StatusOK || bytes.Contains(selectResponse.Body.Bytes(), []byte(rawUserRef)) ||
		bytes.Contains(selectResponse.Body.Bytes(), []byte(rawPositionRef)) {
		t.Fatalf("确认收编失败或泄漏引用: code=%d body=%s", selectResponse.Code, selectResponse.Body.String())
	}
	var selected selectedCandidateView
	if err := json.Unmarshal(selectResponse.Body.Bytes(), &selected); err != nil {
		t.Fatal(err)
	}
	if selected.ProfileID == "" || selected.Status != string(store.CandidateProfileSelected) || !selected.Created {
		t.Fatalf("首次确认响应错误: %+v", selected)
	}
	candidate, err := st.CandidateByKey(store.CandidateKey{Platform: key.Platform, PlatformUserRef: rawUserRef})
	if err != nil || candidate == nil || !candidate.FirstSeenAt.Equal(*terminalAt) || !candidate.LastSeenAt.Equal(*terminalAt) {
		t.Fatalf("Candidate 必须以命令 terminalAt 建档: candidate=%+v terminal=%v err=%v", candidate, terminalAt, err)
	}
	profile, err := st.CandidateProfileByID(selected.ProfileID)
	if err != nil || profile == nil || profile.MainStatus != store.CandidateProfileSelected ||
		profile.PlatformUserRef != rawUserRef || profile.PositionRef != rawPositionRef {
		t.Fatalf("selected Profile 未正确落账: profile=%+v err=%v", profile, err)
	}
	if intent, err := st.LatestGreetingEffectIntent(selected.ProfileID); err != nil || intent != nil {
		t.Fatalf("确认只建档，不得创建 greeting intent/head: intent=%+v err=%v", intent, err)
	}

	repeated := candidatePOST(t, mux, "/admin/candidates/current/select", map[string]string{
		"selectionRef": preview.SelectionRef,
	})
	var repeatedView selectedCandidateView
	_ = json.Unmarshal(repeated.Body.Bytes(), &repeatedView)
	if repeated.Code != http.StatusOK || repeatedView.ProfileID != selected.ProfileID || repeatedView.Created {
		t.Fatalf("重复确认必须幂等收编同一档案: code=%d body=%s", repeated.Code, repeated.Body.String())
	}
	rows, _ = st.RecentCmds(10)
	if len(rows) != 1 || rows[0].IntentID != "" {
		t.Fatalf("确认与重试都不得创建 effect command: %+v", rows)
	}
}

func TestSelectCurrentCandidateRejectsInvalidPersistedProofs(t *testing.T) {
	const rawUserRef = "RAW-REJECTED-USER-MUST-NOT-LEAK-a05be3"
	const rawPositionRef = "RAW-REJECTED-POSITION-MUST-NOT-LEAK-6f97d2"

	tests := []struct {
		name         string
		commandName  string
		status       store.CmdStatus
		contactState protocol.CandidateContactState
		accountRef   string
		contextRef   string
	}{
		{name: "wrong command", commandName: protocol.PrimDebugPing, status: store.CmdOk, contactState: protocol.CandidateContactStateUnestablished, accountRef: candidateTestAccount, contextRef: candidateTestAccount},
		{name: "failed read", commandName: protocol.PrimCandidateReadCurrent, status: store.CmdFailed, contactState: protocol.CandidateContactStateUnestablished, accountRef: candidateTestAccount, contextRef: candidateTestAccount},
		{name: "wrong account", commandName: protocol.PrimCandidateReadCurrent, status: store.CmdOk, contactState: protocol.CandidateContactStateUnestablished, accountRef: "other-account", contextRef: "other-account"},
		{name: "unknown relation", commandName: protocol.PrimCandidateReadCurrent, status: store.CmdOk, contactState: protocol.CandidateContactStateUnknown, accountRef: candidateTestAccount, contextRef: candidateTestAccount},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			hub := newFakeAdminHub()
			seedCurrentCandidateAccount(t, st, hub)

			selectionRef := "selection-" + strings.ReplaceAll(test.name, " ", "-")
			contextRaw, _ := protocol.Encode(protocol.CmdContext{
				Platform: candidateTestPlatform, AccountRef: test.contextRef,
				ExpectedPrincipalFingerprint: candidateTestFingerprint,
			})
			dataRaw, _ := protocol.Encode(protocol.CandidateReadCurrentData{
				PlatformUserRef: rawUserRef, DisplayName: nil,
				PositionRef: rawPositionRef, PositionTitle: nil, ContactState: test.contactState,
			})
			resultRaw, _ := protocol.Encode(protocol.ResultBody{
				Ref: selectionRef, Status: protocol.ResultStatusOk, Data: dataRaw, ExecMs: 1,
			})
			if test.status == store.CmdFailed {
				resultRaw, _ = protocol.Encode(protocol.ResultBody{
					Ref: selectionRef, Status: protocol.ResultStatusFailed, ExecMs: 1,
					Error: &protocol.ErrorBody{
						Code: protocol.ErrCodeCtxNotReady, Retryable: protocol.RetryableNo,
						SideEffect: protocol.SideEffectNone,
					},
				})
			}
			terminalAt := time.Now()
			if err := st.CreateCmd(&store.CmdRecord{
				MsgID: selectionRef, LogicalDispatchID: selectionRef,
				Name: test.commandName, Class: string(protocol.ClassReadonly), Args: "{}",
				Domain:   candidateTestPlatform + ":" + test.accountRef,
				Platform: candidateTestPlatform, AccountRef: test.accountRef,
				ExpectedPrincipalFingerprint: candidateTestFingerprint, ContextJSON: string(contextRaw),
				HandID: candidateTestHand, Session: candidateTestSession, BootIDAtDispatch: candidateTestBoot,
				Status: test.status, ResultBody: string(resultRaw), TerminalAt: &terminalAt,
			}); err != nil {
				t.Fatal(err)
			}
			api := New(st, hub, nil, nil, nil, "")
			mux := http.NewServeMux()
			api.Routes(mux)
			response := candidatePOST(t, mux, "/admin/candidates/current/select", map[string]string{
				"selectionRef": selectionRef,
			})
			if response.Code >= 200 && response.Code < 300 {
				t.Fatalf("无效证词不得收编: code=%d body=%s", response.Code, response.Body.String())
			}
			if bytes.Contains(response.Body.Bytes(), []byte(rawUserRef)) || bytes.Contains(response.Body.Bytes(), []byte(rawPositionRef)) {
				t.Fatalf("拒绝响应泄漏平台原始引用: %s", response.Body.String())
			}
			if candidate, err := st.CandidateByKey(store.CandidateKey{
				Platform: candidateTestPlatform, PlatformUserRef: rawUserRef,
			}); err != nil || candidate != nil {
				t.Fatalf("无效证词不得留下 Candidate: candidate=%+v err=%v", candidate, err)
			}
		})
	}

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hub := newFakeAdminHub()
	seedCurrentCandidateAccount(t, st, hub)
	api := New(st, hub, nil, nil, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)
	missing := candidatePOST(t, mux, "/admin/candidates/current/select", map[string]string{
		"selectionRef": "selection-does-not-exist",
	})
	if missing.Code != http.StatusNotFound {
		t.Fatalf("未知 selectionRef 应 404: code=%d body=%s", missing.Code, missing.Body.String())
	}
}

var _ dispatch.Sender = (*candidateReadSender)(nil)
