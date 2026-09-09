package adminhttp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/notify"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

type recordingProbeSender struct{ calls int }

func (s *recordingProbeSender) SendProbe(notify.ProbeRequest) (notify.ProbeOutcome, error) {
	s.calls++
	return notify.ProbeOutcome{}, nil
}

type emptyBlobs struct{}

func (emptyBlobs) ReadFile(string) ([]byte, error) { return nil, nil }

// 陌生会话必须在动手截图之前就被拒:简历截图要 platformUserRef、正文要状态与
// 微信号,都只能从档案取。查不到就整体拒绝,不派命令、不发半截通知。
func TestNotifyProbeRejectsConversationWithoutProfile(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := seedSendAPI(t, st) // 建了账号与会话,但没有候选人档案
	sender := &probeAPISender{}
	notifySender := &recordingProbeSender{}
	api := New(st, newFakeAdminHub(), dispatch.New(st, sender), nil, nil, "").
		SetNotifyProbeDeps(NotifyProbeDeps{Blobs: emptyBlobs{}, Sender: notifySender})
	mux := http.NewServeMux()
	api.Routes(mux)

	raw, _ := json.Marshal(map[string]any{
		"platform": key.Platform, "accountRef": key.AccountRef,
		"conversationRef": key.ConversationRef,
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/notify/probe", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)

	if response.Code != http.StatusNotFound {
		t.Fatalf("无档案应 404: code=%d body=%s", response.Code, response.Body.String())
	}
	if len(sender.take()) != 0 {
		t.Fatalf("拒绝路径不该派发任何截图命令: %d", len(sender.take()))
	}
	if notifySender.calls != 0 {
		t.Fatalf("拒绝路径不该发出任何通知: %d", notifySender.calls)
	}
}

func TestNotifyProbeRejectsUnknownNotifyType(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := seedSendAPI(t, st)
	sender := &probeAPISender{}
	api := New(st, newFakeAdminHub(), dispatch.New(st, sender), nil, nil, "").
		SetNotifyProbeDeps(NotifyProbeDeps{Blobs: emptyBlobs{}, Sender: &recordingProbeSender{}})
	mux := http.NewServeMux()
	api.Routes(mux)

	raw, _ := json.Marshal(map[string]any{
		"platform": key.Platform, "accountRef": key.AccountRef,
		"conversationRef": key.ConversationRef, "notifyType": "wecomSomethingElse",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/notify/probe", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)

	// 档案先于类型校验:这条会话没档案,先撞 404 也算拒绝在动手之前。
	if response.Code == http.StatusOK {
		t.Fatalf("非法 notifyType 不该 200: %s", response.Body.String())
	}
	if len(sender.take()) != 0 {
		t.Fatalf("拒绝路径不该派发任何截图命令: %d", len(sender.take()))
	}
}

// 未装配发送器时必须明说,不能装作发过了。
func TestNotifyProbeWithoutSenderIsUnavailable(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	api := New(st, newFakeAdminHub(), dispatch.New(st, &probeAPISender{}), nil, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)

	raw, _ := json.Marshal(map[string]any{
		"platform": "zhilian", "accountRef": "a", "conversationRef": "c",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/notify/probe", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("未装配发送器应 503: code=%d", response.Code)
	}
}

// capturingProbeSender 记下最后一次彩排请求,供断言正文快照里的电话四项。
type capturingProbeSender struct {
	calls int
	last  notify.ProbeRequest
}

func (s *capturingProbeSender) SendProbe(req notify.ProbeRequest) (notify.ProbeOutcome, error) {
	s.calls++
	s.last = req
	return notify.ProbeOutcome{}, nil
}

// phoneProbeAPISender 在邀面彩排那只假手之上声明彩排读号与两张截图的能力——
// 脑闸按手声明的能力放行,没声明就在记账前拒绝、一条命令都不派。
type phoneProbeAPISender struct{ probeAPISender }

// takeCmds 只取 cmd 帧——派发过程中脑还会向手发 ack 回执等别的帧。
func (s *phoneProbeAPISender) takeCmds() []protocol.Envelope {
	var cmds []protocol.Envelope
	for _, envelope := range s.take() {
		if envelope.Kind == protocol.KindCmd {
			cmds = append(cmds, envelope)
		}
	}
	return cmds
}

func (*phoneProbeAPISender) HandNegotiation(string) ([]string, []string, bool) {
	caps := make([]string, 0, 3)
	for _, name := range []string{
		protocol.PrimChatReadPeerPhone,
		protocol.PrimChatCaptureThreadScreenshot,
		protocol.PrimCandidateCaptureResumeScreenshot,
	} {
		caps = append(caps, fmt.Sprintf("%s@%d", name, protocol.Primitives[name].Ver))
	}
	return caps, []string{
		string(protocol.FeatureWitness1), string(protocol.FeatureLease1),
		string(protocol.FeatureProgress1), string(protocol.FeatureCancel1),
	}, true
}

// answerProbeCommand 等第 index 条派发命令(take 不清空,按序号取)、核对原语名,然后
// 按给定结果收场;同一时刻不得有第 index+1 条在飞——彩排三步是串行的。
func answerProbeCommand(
	t *testing.T,
	sender *phoneProbeAPISender,
	dispatcher *dispatch.Dispatcher,
	index int,
	wantName string,
	result protocol.ResultBody,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		envelopes := sender.takeCmds()
		if len(envelopes) <= index {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if len(envelopes) != index+1 {
			t.Fatalf("三步应串行,第 %d 步时却已派 %d 条", index+1, len(envelopes))
		}
		var cmd protocol.CmdBody
		if err := json.Unmarshal(envelopes[index].Body, &cmd); err != nil {
			t.Fatal(err)
		}
		if cmd.Name != wantName {
			t.Fatalf("派发顺序错误: got=%s want=%s", cmd.Name, wantName)
		}
		msgID := envelopes[index].MsgID
		dispatcher.OnAck("hand-api", protocol.AckBody{Ref: msgID, Status: protocol.AckStatusAccepted})
		result.Ref = msgID
		dispatcher.OnResult("hand-api", "result-"+msgID, result)
		return
	}
	t.Fatalf("等待派发 %s 超时", wantName)
}

func failedCaptureResult() protocol.ResultBody {
	return protocol.ResultBody{
		Status: protocol.ResultStatusFailed,
		Error: &protocol.ErrorBody{
			Code: protocol.ErrCodeElementUnresolved, Retryable: protocol.RetryableManualOnly,
			SideEffect: protocol.SideEffectNone, Message: "夹具:不拍图",
		},
	}
}

// 彩排现场读一次侧栏电话(AGENTS.md 彩排段 2026-09-09 增补):读到虚拟号且面板姓名
// 首字与会话对方一致 → 只进本次正文(附主叫号与失效时间),不落电话观察事实行、不派
// chat.revealPeerPhone;遮挡形态或首字不符 → 正文沿用库内事实(此处为空),原因回显。
// 读号派在两张截图之前;截图失败只是缺图,通知照发。
func TestNotifyProbeReadsPeerPhoneIntoBodyWithoutPersisting(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// 主动来聊收编要求会话尚未跟踪、无消息,所以不用 seedSendAPI,只建账号、绑手、
	// 落一条带对方展示名(首字核对锚点)的列表行,再收编成档案。
	key := store.ConversationKey{Platform: "zhilian", AccountRef: "account-api", ConversationRef: "conversation-api"}
	if err := st.CreateAccount(&store.Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccountPrincipal(store.AccountKey{Platform: key.Platform, AccountRef: key.AccountRef},
		"hand-api", "fingerprint-api", "session-api", "boot-api", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveConversationList(store.SaveConversationListRequest{
		Platform: key.Platform, AccountRef: key.AccountRef, Complete: true, ObservedAt: time.Now(),
		Entries: []store.ListIndexEntry{{
			ConversationRef: key.ConversationRef, PlatformUserRef: "peer-api", PeerDisplayName: "洪建辉",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	// 收编要有一份在线的职位 AI 上下文可挂,照 dispatch 包同款夹具搭一份最小的。
	replyPrompt := "合成回复:{简历}/{推荐时段}/{对话历史}/{话术_序列}"
	intentPrompt := "合成意向:{回复}/{招呼语}"
	facts := "合成客户事实"
	documents := []m5ai.JobConfigDocument{
		{DocType: "多轮沟通", Content: replyPrompt},
		{DocType: "客户事实库", Content: facts},
		{DocType: "意向判断", Content: intentPrompt},
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].DocType < documents[j].DocType })
	at := time.Now()
	if _, err := st.SaveCurrentLegacyJobAIContext([]m5ai.ContextRevision{{
		ContextID: "context-notify-probe", RevisionHash: "revision-notify-probe",
		SourceKind: "legacyJobConfig", SourceJobRef: "job-notify-probe",
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
		Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: key.ConversationRef,
		PlatformUserRef: "peer-api", DisplayName: "洪建辉", PositionTitle: "客户经理", ObservedAt: at.Add(time.Second),
	})
	if err != nil || adopted == nil || adopted.Profile == nil {
		t.Fatalf("挂档案失败: %+v err=%v", adopted, err)
	}
	profileID := adopted.Profile.ProfileID
	expires := time.Now().Add(48 * time.Hour).Truncate(time.Minute).UnixMilli()

	for _, testCase := range []struct {
		name      string
		phoneData protocol.ChatReadPeerPhoneData
		wantPhone string
		wantKind  store.CandidatePhoneKind
		wantNote  string
	}{
		{
			name: "虚拟号进正文",
			phoneData: protocol.ChatReadPeerPhoneData{
				Phone: "18000000001", PhoneKind: protocol.PeerPhoneKindVirtual,
				VirtualCaller: "139****0000", VirtualExpiresAt: expires, PanelName: "洪先生",
			},
			wantPhone: "18000000001", wantKind: store.CandidatePhoneKindVirtual, wantNote: "虚拟号",
		},
		{
			name:      "遮挡形态不揭示",
			phoneData: protocol.ChatReadPeerPhoneData{Masked: true, PanelName: "洪先生"},
			wantNote:  "遮挡",
		},
		{
			name: "首字不符不收",
			phoneData: protocol.ChatReadPeerPhoneData{
				Phone: "13800000001", PhoneKind: protocol.PeerPhoneKindReal, PanelName: "王先生",
			},
			wantNote: "未通过收编判定",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			sender := &phoneProbeAPISender{}
			dispatcher := dispatch.New(st, sender)
			notifySender := &capturingProbeSender{}
			api := New(st, newFakeAdminHub(), dispatcher, nil, nil, "").
				SetNotifyProbeDeps(NotifyProbeDeps{Blobs: emptyBlobs{}, Sender: notifySender})
			mux := http.NewServeMux()
			api.Routes(mux)

			raw, _ := json.Marshal(map[string]any{
				"platform": key.Platform, "accountRef": key.AccountRef, "conversationRef": key.ConversationRef,
			})
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				req := httptest.NewRequest(http.MethodPost, "/admin/notify/probe", bytes.NewReader(raw))
				req.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, req)
				done <- response
			}()

			phoneData := testCase.phoneData
			phoneData.ObservedAt = time.Now().UnixMilli()
			encoded, _ := protocol.Encode(phoneData)
			answerProbeCommand(t, sender, dispatcher, 0, protocol.PrimChatReadPeerPhone,
				protocol.ResultBody{Status: protocol.ResultStatusOk, Data: encoded})
			answerProbeCommand(t, sender, dispatcher, 1, protocol.PrimChatCaptureThreadScreenshot, failedCaptureResult())
			answerProbeCommand(t, sender, dispatcher, 2, protocol.PrimCandidateCaptureResumeScreenshot, failedCaptureResult())

			response := <-done
			if response.Code != http.StatusOK {
				t.Fatalf("应 200: code=%d body=%s", response.Code, response.Body.String())
			}
			if all := sender.takeCmds(); len(all) != 3 {
				t.Fatalf("三条之外不得再派任何命令(尤其 revealPeerPhone): %d", len(all))
			}
			var view struct {
				PhoneNote string `json:"phoneNote"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(view.PhoneNote, testCase.wantNote) {
				t.Fatalf("phoneNote 应含 %q: %q", testCase.wantNote, view.PhoneNote)
			}
			if strings.Contains(view.PhoneNote, "18000000001") || strings.Contains(view.PhoneNote, "13800000001") {
				t.Fatalf("回显说明不得带号码: %q", view.PhoneNote)
			}
			if notifySender.calls != 1 || notifySender.last.Snapshot == nil {
				t.Fatalf("应恰好发一次: calls=%d", notifySender.calls)
			}
			snapshot := notifySender.last.Snapshot
			if snapshot.PhoneNumber != testCase.wantPhone || snapshot.PhoneKind != testCase.wantKind {
				t.Fatalf("正文快照电话不符: number=%q kind=%q", snapshot.PhoneNumber, snapshot.PhoneKind)
			}
			if testCase.wantKind == store.CandidatePhoneKindVirtual &&
				(snapshot.PhoneVirtualCaller != "139****0000" || snapshot.PhoneVirtualExpiresAtMs != expires) {
				t.Fatalf("虚拟号附属事实未进快照: %+v", snapshot)
			}
			if row, err := st.LatestCandidatePhoneObservation(profileID); err != nil || row != nil {
				t.Fatalf("彩排不得落电话观察事实行: %+v err=%v", row, err)
			}
		})
	}
}
