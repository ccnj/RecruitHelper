package adminhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

func TestM5TrialSelectionAndStatusNeverExposeResumeOrPlatformRefs(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hub := newFakeAdminHub()
	seedGreetingAPI(t, st, hub)
	sender := &greetingAPISender{}
	d := dispatch.New(st, sender)
	sender.dispatcher = d
	if _, err := d.SendGreeting(dispatch.SendGreetingRequest{
		IntentID: greetingTestIntent, ProfileID: greetingTestProfile, Text: greetingTestText,
	}); err != nil {
		t.Fatal(err)
	}
	api := New(st, hub, d, nil, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)

	selected := candidatePOST(t, mux, "/admin/m5/trial/select", map[string]string{
		"platform": "zhilian", "accountRef": "account-greeting-api", "conversationRef": greetingConversation,
	})
	if selected.Code != http.StatusOK {
		t.Fatalf("显式选择 M5 试运行失败: code=%d body=%s", selected.Code, selected.Body.String())
	}
	for _, forbidden := range []string{greetingTestUserRef, greetingTestPosition, greetingConversation, greetingTestText} {
		if strings.Contains(selected.Body.String(), forbidden) {
			t.Fatalf("试运行选择响应泄漏业务原文 %q: %s", forbidden, selected.Body.String())
		}
	}

	receipt, err := d.DispatchResumeCapture(dispatch.ResumeCaptureDispatchRequest{
		ProfileID: greetingTestProfile, HandID: greetingTestHand,
		ExpectedSession: greetingTestSession, ExpectedBootID: greetingTestBoot,
		Platform: "zhilian", AccountRef: "account-greeting-api",
		ExpectedPrincipalFingerprint: greetingTestPrincipal,
	})
	if err != nil {
		t.Fatal(err)
	}
	const privateResume = "PRIVATE-RESUME-BODY-MUST-NOT-LEAK-8d63"
	data := protocol.CandidateReadResumeData{
		ConversationRef: greetingConversation, PlatformUserRef: greetingTestUserRef,
		ObservedAt:   time.Now().UnixMilli(),
		Basic:        []protocol.CandidateResumeLabelValue{{Label: "合成", Value: privateResume}},
		Expectations: []protocol.CandidateResumeLabelValue{}, SelfEvaluation: "",
		Education: privateResume, WorkExperiences: privateResume,
	}
	dataRaw, _ := protocol.Encode(data)
	d.OnAck(greetingTestHand, protocol.AckBody{Ref: receipt.LogicalDispatchID, Status: protocol.AckStatusAccepted})
	d.OnResult(greetingTestHand, "result-"+receipt.LogicalDispatchID, protocol.ResultBody{
		Ref: receipt.LogicalDispatchID, Status: protocol.ResultStatusOk, Data: dataRaw, ExecMs: 1,
	})
	if _, err := d.WaitLogical(context.Background(), receipt.LogicalDispatchID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CompleteResumeCapture(store.CompleteResumeCaptureRequest{
		ProfileID: greetingTestProfile, LogicalDispatchID: receipt.LogicalDispatchID,
		SnapshotID: "snapshot-api-safe", Data: data,
	}); err != nil {
		t.Fatal(err)
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/admin/m5/trial", nil)
	status := httptest.NewRecorder()
	mux.ServeHTTP(status, statusReq)
	if status.Code != http.StatusOK {
		t.Fatalf("读取 M5 状态失败: code=%d body=%s", status.Code, status.Body.String())
	}
	for _, forbidden := range []string{
		privateResume, greetingTestUserRef, greetingTestPosition, greetingConversation, greetingTestText,
	} {
		if bytes.Contains(status.Body.Bytes(), []byte(forbidden)) {
			t.Fatalf("M5 状态响应泄漏业务原文 %q: %s", forbidden, status.Body.String())
		}
	}
	var decoded struct {
		Trial struct {
			CaptureState string `json:"captureState"`
			Snapshot     struct {
				SnapshotID       string `json:"snapshotId"`
				Bytes            int    `json:"bytes"`
				BasicItems       int    `json:"basicItems"`
				SectionsComplete bool   `json:"sectionsComplete"`
			} `json:"snapshot"`
		} `json:"trial"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Trial.CaptureState != string(store.ResumeCaptureCaptured) ||
		decoded.Trial.Snapshot.SnapshotID != "snapshot-api-safe" ||
		decoded.Trial.Snapshot.Bytes == 0 || decoded.Trial.Snapshot.BasicItems != 1 ||
		!decoded.Trial.Snapshot.SectionsComplete {
		t.Fatalf("M5 安全状态元数据不完整: %+v", decoded)
	}
}
