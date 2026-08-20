package chatreport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
)

func TestProvenance(t *testing.T) {
	cases := []struct {
		name      string
		direction string
		hasIntent bool
		event     string
		action    string
		primitive string
		want      string
	}{
		{"入站无出身", "in", false, "", "", "", ""},
		{"出站无意图是人工", "out", false, "", "", "", "manual"},
		{"事件动作最优先", "out", true, "coldPromptFixed", "replyText", "chat.sendMessage", "coldPromptFixed"},
		{"AI轮动作次之", "out", true, "", "interviewInvite", "chat.sendInviteCard", "interviewInvite"},
		{"招呼按原语归类", "out", true, "", "", "chat.sendGreeting", "greeting"},
		{"邀面卡按原语归类", "out", true, "", "", "chat.sendInviteCard", "interviewInvite"},
		{"换微信邀请按原语归类", "out", true, "", "", "chat.sendWechatInvite", "inviteWechat"},
		{"接受换微信按原语归类", "out", true, "", "", "chat.acceptWechat", "acceptWechat"},
		{"推不出记unknown", "out", true, "", "", "chat.sendMessage", "unknown"},
	}
	for _, c := range cases {
		if got := provenanceOf(c.direction, c.hasIntent, c.event, c.action, c.primitive); got != c.want {
			t.Errorf("%s: 得 %q 想要 %q", c.name, got, c.want)
		}
	}
}

// TestPayloadKeyWhitelist 是载荷白名单的看门狗：任何人往载荷结构体加字段，
// 这里会先红。结构化微信号、简历正文、platformUserRef、API key 永远不该出现。
func TestPayloadKeyWhitelist(t *testing.T) {
	text := "您好"
	name := "张三"
	job := "平安健康保障顾问"
	jobID := "16"
	conv := "conv-1"
	reason := "被更强证据推翻"
	method := "onsite"
	ms := int64(1755561600000)
	payload := Payload{
		MachineID: "M1", LicenseToken: "T1", AppVersion: "3.9.0",
		SchemaVersion: SchemaVersion, ReportedAt: time.Now(),
		Profiles: []ProfileRow{{
			ProfileID: "p-1", Platform: "zhilian", AccountRef: "acc-1",
			ConversationRef: &conv, DisplayName: &name, BackendJobID: &jobID,
			JobName: &job, MainStatus: "communicating", EndReason: &reason,
			GreetedAtMs: &ms, CommunicatingAtMs: &ms, InterviewedAtMs: &ms,
			WechatAtMs:                  &ms,
			UpcomingInterviewStartsAtMs: &ms, UpcomingInterviewEndsAtMs: &ms,
			UpcomingInterviewMethod: &method,
		}},
		Messages: []MessageRow{{
			Platform: "zhilian", AccountRef: "acc-1", ConversationRef: conv,
			Seq: 3, ProfileID: &jobID, Direction: "out", Kind: "card",
			Text: &text, CardType: "interviewInvite", CardState: "pending",
			InterviewStartsAtMs: &ms, InterviewEndsAtMs: &ms, InterviewMethod: &method,
			TsApproxMs: &ms, Provenance: "greeting",
			Retracted: true, RetractionReason: reason,
		}},
	}

	allowed := map[string]bool{
		"machineId": true, "licenseToken": true, "appVersion": true,
		"schemaVersion": true, "reportedAt": true, "profiles": true, "messages": true,
		"profileId": true, "platform": true, "accountRef": true, "conversationRef": true,
		"displayName": true, "backendJobId": true, "jobName": true,
		"mainStatus": true, "endReason": true,
		"greetedAtMs": true, "communicatingAtMs": true, "interviewedAtMs": true,
		"wechatAtMs":                  true,
		"upcomingInterviewStartsAtMs": true, "upcomingInterviewEndsAtMs": true,
		"upcomingInterviewMethod": true,
		"seq":                     true, "direction": true, "kind": true, "text": true,
		"cardType": true, "cardState": true,
		"interviewStartsAtMs": true, "interviewEndsAtMs": true, "interviewMethod": true,
		"tsApproxMs": true, "provenance": true,
		"retracted": true, "retractionReason": true,
	}
	for _, key := range jsonKeysOf(t, payload) {
		if !allowed[key] {
			t.Errorf("载荷长出了白名单之外的键 %q——新键必须先过 AGENTS.md 条款再进白名单", key)
		}
	}
}

type fakeStore struct {
	profiles []store.ChatReportProfileRow
	// pending 按调用顺序弹出，模拟"每批之后水位推进、下一批换一拨"。
	pendingBatches [][]store.ChatReportMessageRow
	advanced       map[string]int64
}

func (f *fakeStore) ChatReportProfileRows() ([]store.ChatReportProfileRow, error) {
	return f.profiles, nil
}

func (f *fakeStore) ChatReportPendingMessages(int) ([]store.ChatReportMessageRow, error) {
	if len(f.pendingBatches) == 0 {
		return nil, nil
	}
	batch := f.pendingBatches[0]
	f.pendingBatches = f.pendingBatches[1:]
	return batch, nil
}

func (f *fakeStore) AdvanceChatReportCursor(platform, account, conversation string, seq int64) error {
	if f.advanced == nil {
		f.advanced = map[string]int64{}
	}
	key := platform + "/" + account + "/" + conversation
	if seq > f.advanced[key] {
		f.advanced[key] = seq
	}
	return nil
}

func readyTarget() (Target, bool) {
	return Target{BaseURL: "http://127.0.0.1:1", MachineID: "M1", LicenseToken: "T1"}, true
}

func TestRunOnceUploadsProfilesThenMessagesAndAdvancesCursor(t *testing.T) {
	name := "张三"
	st := &fakeStore{
		profiles: []store.ChatReportProfileRow{{
			ProfileID: "p-1", Platform: "zhilian", AccountRef: "acc-1",
			DisplayName: &name, MainStatus: "communicating",
		}},
		pendingBatches: [][]store.ChatReportMessageRow{
			{
				{Platform: "zhilian", AccountRef: "acc-1", ConversationRef: "conv-1", Seq: 1, Direction: "in", Kind: "text"},
				{Platform: "zhilian", AccountRef: "acc-1", ConversationRef: "conv-1", Seq: 2, Direction: "out", Kind: "text"},
				{Platform: "zhilian", AccountRef: "acc-1", ConversationRef: "conv-2", Seq: 5, Direction: "in", Kind: "text"},
			},
		},
	}
	var uploads []*Payload
	deps := Deps{
		Store:  st,
		Target: readyTarget,
		Upload: func(_ context.Context, payload *Payload, _ Target) error {
			copied := *payload
			uploads = append(uploads, &copied)
			return nil
		},
	}
	summary, err := RunOnce(context.Background(), deps)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if summary.Profiles != 1 || summary.Messages != 3 {
		t.Fatalf("结果计数错误: %+v", summary)
	}
	if len(uploads) != 2 {
		t.Fatalf("想要 2 次上传(档案批+消息批)，得 %d", len(uploads))
	}
	if len(uploads[0].Profiles) != 1 || len(uploads[0].Messages) != 0 {
		t.Fatalf("第一批应只含档案行: %+v", uploads[0])
	}
	if len(uploads[1].Messages) != 3 {
		t.Fatalf("第二批应含 3 条消息，得 %d", len(uploads[1].Messages))
	}
	if st.advanced["zhilian/acc-1/conv-1"] != 2 || st.advanced["zhilian/acc-1/conv-2"] != 5 {
		t.Fatalf("水位推进错误: %+v", st.advanced)
	}
}

func TestRunOnceStopsOnUploadFailureWithoutAdvancing(t *testing.T) {
	st := &fakeStore{
		pendingBatches: [][]store.ChatReportMessageRow{
			{{Platform: "zhilian", AccountRef: "acc-1", ConversationRef: "conv-1", Seq: 1, Direction: "in", Kind: "text"}},
		},
	}
	deps := Deps{
		Store:  st,
		Target: readyTarget,
		Upload: func(context.Context, *Payload, Target) error {
			return errors.New("网络断了")
		},
	}
	if _, err := RunOnce(context.Background(), deps); err == nil {
		t.Fatal("上传失败必须让整轮失败(次日自愈)，不能吞掉")
	}
	if len(st.advanced) != 0 {
		t.Fatalf("失败批不得推进水位: %+v", st.advanced)
	}
}

func TestRunOnceRefusesWhenTargetNotReady(t *testing.T) {
	deps := Deps{
		Store:  &fakeStore{},
		Target: func() (Target, bool) { return Target{}, false },
		Upload: func(context.Context, *Payload, Target) error {
			t.Fatal("授权未就绪不得发起上传")
			return nil
		},
	}
	if _, err := RunOnce(context.Background(), deps); err == nil {
		t.Fatal("授权未就绪应返回错误让调度器记录，而不是静默成功")
	}
}

func TestRunOnceRejectsConcurrentRun(t *testing.T) {
	// 进行中互斥：定时触发与诊断台人工触发不同时跑，手抖连点被拒。
	entered := make(chan struct{})
	release := make(chan struct{})
	st := &fakeStore{
		pendingBatches: [][]store.ChatReportMessageRow{
			{{Platform: "zhilian", AccountRef: "acc-1", ConversationRef: "conv-1", Seq: 1, Direction: "in", Kind: "text"}},
		},
	}
	deps := Deps{
		Store:  st,
		Target: readyTarget,
		Upload: func(context.Context, *Payload, Target) error {
			close(entered)
			<-release
			return nil
		},
	}
	done := make(chan error, 1)
	go func() {
		_, err := RunOnce(context.Background(), deps)
		done <- err
	}()
	<-entered
	if _, err := RunOnce(context.Background(), Deps{Store: &fakeStore{}, Target: readyTarget}); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("并发第二次应拒绝: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("第一次应正常完成: %v", err)
	}
	// 互斥释放后可再跑。
	if _, err := RunOnce(context.Background(), Deps{Store: &fakeStore{}, Target: readyTarget}); err != nil {
		t.Fatalf("释放后应可再跑: %v", err)
	}
}

// jsonKeysOf 递归收集序列化结果里出现的全部键。
func jsonKeysOf(t *testing.T, value any) []string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	seen := map[string]bool{}
	var walk func(node any)
	walk = func(node any) {
		switch typed := node.(type) {
		case map[string]any:
			for key, child := range typed {
				seen[key] = true
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(decoded)
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestUploadPostsToChatReportEndpoint(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"ok":true,"profiles":0,"messages":1}`))
	}))
	defer server.Close()

	payload := &Payload{ReportedAt: time.Now(), Messages: []MessageRow{{
		Platform: "zhilian", AccountRef: "acc-1", ConversationRef: "conv-1",
		Seq: 1, Direction: "in", Kind: "text",
	}}}
	target := Target{BaseURL: server.URL, MachineID: "M1", LicenseToken: "T1", AppVersion: "3.9.0"}
	if err := Upload(context.Background(), payload, target); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if gotPath != "/api/v1/client/chat-report" {
		t.Fatalf("端点路径错误: %q", gotPath)
	}
	body := string(gotBody)
	for _, want := range []string{`"machineId":"M1"`, `"licenseToken":"T1"`, `"schemaVersion":1`} {
		if !strings.Contains(body, want) {
			t.Fatalf("载荷缺少 %s: %s", want, body)
		}
	}
}

func TestUploadTreatsNon2xxAsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"detail":"401"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	payload := &Payload{ReportedAt: time.Now()}
	target := Target{BaseURL: server.URL, MachineID: "M1", LicenseToken: "T1"}
	if err := Upload(context.Background(), payload, target); err == nil {
		t.Fatal("非 2xx 必须判失败——水位只认成功")
	}
}
