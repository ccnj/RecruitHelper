package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
)

func int64Ptr(v int64) *int64 { return &v }

func fullSnapshot() *store.NotificationRenderSnapshot {
	// 2026-07-30 12:30 本地时间
	starts := time.Date(2026, 7, 30, 12, 30, 0, 0, time.Local).UnixMilli()
	return &store.NotificationRenderSnapshot{
		ProfileID:           "p1",
		DisplayName:         "测试候选",
		PositionTitle:       "资深销售",
		MainStatus:          store.CandidateProfileInterviewed,
		WechatState:         "exchanged",
		WechatID:            "wx-demo-88",
		InterviewStartsAtMs: int64Ptr(starts),
		ChatShot:            &store.CandidateScreenshot{BlobRef: "sha256:" + strings.Repeat("a", 64)},
		ResumeShot:          &store.CandidateScreenshot{BlobRef: "sha256:" + strings.Repeat("b", 64)},
	}
}

func TestRenderInterviewAccepted(t *testing.T) {
	text := renderInterviewAccepted(fullSnapshot(), "客户甲")
	for _, want := range []string{
		"【面试确认】测试候选(客户甲)",
		"面试时间:07-30(周四) 12:30",
		"联系方式:微信 wx-demo-88(已成功交换微信)",
		"职位:资深销售",
		"聊天记录、简历见下图",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("约面文案缺少 %q:\n%s", want, text)
		}
	}

	bare := fullSnapshot()
	bare.WechatID = ""
	bare.WechatState = "invited"
	bare.InterviewStartsAtMs = nil
	bare.ChatShot = nil
	bare.ResumeShot = nil
	text = renderInterviewAccepted(bare, "")
	for _, want := range []string{
		"【面试确认】测试候选",
		"面试时间:未获取到,请在客户端核对",
		"联系方式:未获取(已邀微信)",
		"(本次未附截图)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("降级约面文案缺少 %q:\n%s", want, text)
		}
	}
	mobile := fullSnapshot()
	mobile.WechatID = "13800138000"
	if text := renderInterviewAccepted(mobile, ""); !strings.Contains(text, "手机 13800138000") {
		t.Fatalf("手机号应标手机: %s", text)
	}
}

func TestRenderWechatAdded(t *testing.T) {
	text := renderWechatAdded(fullSnapshot(), "客户乙", false)
	for _, want := range []string{
		"【微信互加】测试候选(客户乙)",
		"联系方式:微信 wx-demo-88(已成功交换微信)",
		"当前状态:已约面 · 面试 07-30(周四) 12:30",
		"职位:资深销售",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("互加文案缺少 %q:\n%s", want, text)
		}
	}
	onlyChat := fullSnapshot()
	onlyChat.MainStatus = store.CandidateProfileCommunicating
	onlyChat.ResumeShot = nil
	text = renderWechatAdded(onlyChat, "", false)
	if !strings.Contains(text, "当前状态:沟通中") || !strings.Contains(text, "聊天记录见下图") ||
		strings.Contains(text, "简历见下图") && strings.Contains(text, "、简历") {
		t.Fatalf("仅聊天图提示不符: %s", text)
	}
}

// 画像行逐项可缺:简历没采到、字段缺失都只是少一段文字,绝不阻断通知。
func TestProfileLineDegradesPerField(t *testing.T) {
	base := func() *store.NotificationRenderSnapshot {
		s := fullSnapshot()
		s.Age, s.Education, s.City, s.DesiredSalary = "35岁", "本科", "上海", "20-30k"
		return s
	}
	if got := profileLine(base()); got != "候选人:35岁/本科/上海 · 期望 20-30k" {
		t.Fatalf("完整画像行不符: %q", got)
	}
	only := base()
	only.Education, only.City = "", ""
	if got := profileLine(only); got != "候选人:35岁 · 期望 20-30k" {
		t.Fatalf("缺学历/城市未逐项省略: %q", got)
	}
	noSalary := base()
	noSalary.DesiredSalary = "  "
	if got := profileLine(noSalary); got != "候选人:35岁/本科/上海" {
		t.Fatalf("缺薪资未省略后缀: %q", got)
	}
	salaryOnly := base()
	salaryOnly.Age, salaryOnly.Education, salaryOnly.City = "", "", ""
	if got := profileLine(salaryOnly); got != "期望 20-30k" {
		t.Fatalf("只剩薪资时不该留空前缀: %q", got)
	}
	empty := base()
	empty.Age, empty.Education, empty.City, empty.DesiredSalary = "", "", "", ""
	if got := profileLine(empty); got != "" {
		t.Fatalf("四项全缺必须整行省略: %q", got)
	}
	// 整行缺失时通知照常渲染,不能少了关键行、更不能报错。
	text := renderWechatAdded(empty, "客户丁", false)
	if strings.Contains(text, "候选人:") || strings.Contains(text, "期望 ") {
		t.Fatalf("四项全缺不得留下画像残句:\n%s", text)
	}
	if !strings.Contains(text, "联系方式:") || !strings.Contains(text, "当前状态:") {
		t.Fatalf("画像缺失影响了其它行:\n%s", text)
	}
}

func TestTruncateBytesIsRuneSafe(t *testing.T) {
	long := strings.Repeat("很", 2048)
	out := truncateBytes(long, wecomTextLimitBytes)
	if len(out) > wecomTextLimitBytes {
		t.Fatalf("截断后仍超限: %d", len(out))
	}
	if !strings.HasSuffix(out, "…") || strings.ContainsRune(out, '\uFFFD') {
		t.Fatalf("截断破坏 UTF-8: %q", out[len(out)-12:])
	}
}

type wecomCapture struct {
	kinds  []string
	texts  []string
	images [][]byte
	fail   bool
}

func newWecomServer(t *testing.T, capture *wecomCapture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			MsgType string `json:"msgtype"`
			Text    struct {
				Content string `json:"content"`
			} `json:"text"`
			Image struct {
				Base64 string `json:"base64"`
				MD5    string `json:"md5"`
			} `json:"image"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("payload 解码失败: %v", err)
		}
		capture.kinds = append(capture.kinds, payload.MsgType)
		if payload.MsgType == "text" {
			capture.texts = append(capture.texts, payload.Text.Content)
		}
		if payload.MsgType == "image" {
			capture.images = append(capture.images, []byte(payload.Image.Base64))
			if payload.Image.MD5 == "" {
				t.Error("image 缺 md5")
			}
		}
		if capture.fail {
			_, _ = w.Write([]byte(`{"errcode":93000,"errmsg":"invalid webhook"}`))
			return
		}
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
}

func TestSendWecomImageValidation(t *testing.T) {
	client := &http.Client{}
	for name, image := range map[string][]byte{
		"empty":      nil,
		"not-image":  []byte("plain-text"),
		"over-limit": append([]byte{0xff, 0xd8, 0xff}, make([]byte, wecomImageMaxBytes)...),
	} {
		err := sendWecomImage(client, "http://127.0.0.1:1/unused", image)
		if _, skipped := err.(*skippedImageError); !skipped {
			t.Fatalf("%s 应为跳过错误: %v", name, err)
		}
	}
}

type fakeLedger struct {
	rows     []store.NotificationOutbox
	snapshot map[string]*store.NotificationRenderSnapshot
	meeting  map[string]*store.NotificationOutbox

	sent    []uint64
	sentWx  []bool
	failed  []uint64
	skipped []uint64
}

func (f *fakeLedger) ExpireStaleNotifications(time.Duration, time.Time) (int64, error) {
	return 0, nil
}

func (f *fakeLedger) TakePendingNotifications(int, int) ([]store.NotificationOutbox, error) {
	pending := []store.NotificationOutbox{}
	for _, row := range f.rows {
		if row.Status == store.NotificationStatusPending {
			pending = append(pending, row)
		}
	}
	return pending, nil
}

func (f *fakeLedger) NotificationRenderSnapshotForProfile(profileID string) (*store.NotificationRenderSnapshot, error) {
	return f.snapshot[profileID], nil
}

func (f *fakeLedger) InterviewNotificationForProfile(profileID string) (*store.NotificationOutbox, error) {
	return f.meeting[profileID], nil
}

func (f *fakeLedger) MarkNotificationSent(id uint64, sentWithWechat bool, _ time.Time) error {
	f.sent = append(f.sent, id)
	f.sentWx = append(f.sentWx, sentWithWechat)
	for index := range f.rows {
		if f.rows[index].ID == id {
			f.rows[index].Status = store.NotificationStatusSent
		}
	}
	return nil
}

func (f *fakeLedger) MarkNotificationFailed(id uint64, _ string, _ int, _ time.Time) error {
	f.failed = append(f.failed, id)
	return nil
}

func (f *fakeLedger) MarkNotificationSkipped(id uint64, _ string, _ time.Time) error {
	f.skipped = append(f.skipped, id)
	for index := range f.rows {
		if f.rows[index].ID == id {
			f.rows[index].Status = store.NotificationStatusSkipped
		}
	}
	return nil
}

type fakeBlobs struct{ data map[string][]byte }

func (f *fakeBlobs) ReadFile(ref string) ([]byte, error) {
	if blob, ok := f.data[ref]; ok {
		return blob, nil
	}
	return nil, io.ErrUnexpectedEOF
}

func newTestRunner(ledger *fakeLedger, blobs BlobReader, webhookURL string, at time.Time) *Runner {
	runner := NewRunner(ledger, blobs, func() string { return "客户丙" })
	runner.webhookURL = webhookURL
	runner.now = func() time.Time { return at }
	return runner
}

func jpegBytes() []byte { return append([]byte{0xff, 0xd8, 0xff}, []byte("frame")...) }

// 三资产齐 → 立即发文本并按「先聊天后简历」追发两图;约面通知记 sent_with_wechat。
func TestTickSendsTextThenScreenshots(t *testing.T) {
	capture := &wecomCapture{}
	server := newWecomServer(t, capture)
	defer server.Close()
	now := time.Date(2026, 7, 28, 15, 0, 0, 0, time.Local)
	snapshot := fullSnapshot()
	ledger := &fakeLedger{
		rows: []store.NotificationOutbox{{
			ID: 1, NotifyType: store.NotificationTypeInterviewAccepted,
			ProfileID: "p1", Status: store.NotificationStatusPending, CreatedAt: now.Add(-time.Minute),
		}},
		snapshot: map[string]*store.NotificationRenderSnapshot{"p1": snapshot},
		meeting:  map[string]*store.NotificationOutbox{},
	}
	blobs := &fakeBlobs{data: map[string][]byte{
		snapshot.ChatShot.BlobRef:   jpegBytes(),
		snapshot.ResumeShot.BlobRef: jpegBytes(),
	}}
	summary := newTestRunner(ledger, blobs, server.URL, now).Tick()
	if summary.Sent != 1 || summary.Failed != 0 || summary.Held != 0 {
		t.Fatalf("tick 结果不符: %+v", summary)
	}
	if len(ledger.sent) != 1 || ledger.sent[0] != 1 || !ledger.sentWx[0] {
		t.Fatalf("sent 落账不符: %+v %+v", ledger.sent, ledger.sentWx)
	}
	if want := []string{"text", "image", "image"}; strings.Join(capture.kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("发送顺序不符: %+v", capture.kinds)
	}
	if !strings.Contains(capture.texts[0], "(客户丙)") {
		t.Fatalf("正文缺客户名: %s", capture.texts[0])
	}
}

// 缺资产且未到 15 分钟 → hold;到点 → 缺啥也发(纯文本降级)。
func TestTickGateHoldsThenFallsBack(t *testing.T) {
	capture := &wecomCapture{}
	server := newWecomServer(t, capture)
	defer server.Close()
	now := time.Date(2026, 7, 28, 15, 0, 0, 0, time.Local)
	snapshot := fullSnapshot()
	snapshot.ResumeShot = nil
	ledger := &fakeLedger{
		rows: []store.NotificationOutbox{{
			ID: 2, NotifyType: store.NotificationTypeWechatAdded,
			ProfileID: "p1", Status: store.NotificationStatusPending, CreatedAt: now.Add(-time.Minute),
		}},
		snapshot: map[string]*store.NotificationRenderSnapshot{"p1": snapshot},
		meeting:  map[string]*store.NotificationOutbox{},
	}
	blobs := &fakeBlobs{data: map[string][]byte{snapshot.ChatShot.BlobRef: jpegBytes()}}
	runner := newTestRunner(ledger, blobs, server.URL, now)
	if summary := runner.Tick(); summary.Held != 1 || summary.Sent != 0 {
		t.Fatalf("闸门未 hold: %+v", summary)
	}
	if len(capture.kinds) != 0 {
		t.Fatalf("hold 期不得发送: %+v", capture.kinds)
	}
	runner.now = func() time.Time { return now.Add(20 * time.Minute) }
	ledger.rows[0].CreatedAt = now.Add(-16 * time.Minute)
	if summary := runner.Tick(); summary.Sent != 1 {
		t.Fatalf("到点未兜底发送: %+v", summary)
	}
	if want := []string{"text", "image"}; strings.Join(capture.kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("兜底发送只应带就绪的聊天图: %+v", capture.kinds)
	}
}

// 微信互加去重矩阵(照抄旧项目 _decide_wechat_added)。
func TestWechatAddedDedupMatrix(t *testing.T) {
	now := time.Date(2026, 7, 28, 16, 0, 0, 0, time.Local)
	baseRow := store.NotificationOutbox{
		ID: 10, NotifyType: store.NotificationTypeWechatAdded,
		ProfileID: "p1", Status: store.NotificationStatusPending, CreatedAt: now.Add(-time.Hour),
	}
	cases := []struct {
		name       string
		meeting    *store.NotificationOutbox
		rowID      uint64
		want       decision
		supplement bool
	}{
		{"无约面通知", nil, 10, decisionSend, false},
		{"约面前换到", &store.NotificationOutbox{ID: 99, Status: store.NotificationStatusSent, SentWithWechat: true}, 10, decisionSend, false},
		{"约面仍pending", &store.NotificationOutbox{ID: 5, Status: store.NotificationStatusPending}, 10, decisionHold, false},
		{"已随约面带出", &store.NotificationOutbox{ID: 5, Status: store.NotificationStatusSent, SentWithWechat: true}, 10, decisionDrop, false},
		// 唯一的补号形态:约面通知确实发到运营手上了,但当时没号。
		{"约面发出未带号", &store.NotificationOutbox{ID: 5, Status: store.NotificationStatusSent, SentWithWechat: false}, 10, decisionSend, true},
		// 运营从没收到过面试确认,不能叫"补微信号"。
		{"约面终败", &store.NotificationOutbox{ID: 5, Status: store.NotificationStatusFailed}, 10, decisionSend, false},
		{"约面过期", &store.NotificationOutbox{ID: 5, Status: store.NotificationStatusExpired}, 10, decisionSend, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ledger := &fakeLedger{meeting: map[string]*store.NotificationOutbox{}}
			if testCase.meeting != nil {
				ledger.meeting["p1"] = testCase.meeting
			}
			runner := newTestRunner(ledger, &fakeBlobs{}, "http://127.0.0.1:1", now)
			row := baseRow
			row.ID = testCase.rowID
			got, supplement := runner.decideWechatAdded(row)
			if got != testCase.want || supplement != testCase.supplement {
				t.Fatalf("判定不符: got=(%v,%v) want=(%v,%v)",
					got, supplement, testCase.want, testCase.supplement)
			}
		})
	}
}

// 补号形态在 tick 里必须真的改写标题:约面通知已发但未带号 → 「面试确认--补微信号」。
func TestTickRendersInterviewSupplementTitle(t *testing.T) {
	capture := &wecomCapture{}
	server := newWecomServer(t, capture)
	defer server.Close()
	now := time.Date(2026, 7, 28, 18, 0, 0, 0, time.Local)
	snapshot := fullSnapshot()
	snapshot.ChatShot = nil
	snapshot.ResumeShot = nil
	ledger := &fakeLedger{
		rows: []store.NotificationOutbox{{
			ID: 30, NotifyType: store.NotificationTypeWechatAdded,
			ProfileID: "p1", Status: store.NotificationStatusPending, CreatedAt: now.Add(-time.Hour),
		}},
		snapshot: map[string]*store.NotificationRenderSnapshot{"p1": snapshot},
		meeting: map[string]*store.NotificationOutbox{
			"p1": {ID: 7, Status: store.NotificationStatusSent, SentWithWechat: false},
		},
	}
	summary := newTestRunner(ledger, &fakeBlobs{}, server.URL, now).Tick()
	if summary.Sent != 1 || len(capture.texts) != 1 {
		t.Fatalf("补号通知未发出: summary=%+v texts=%+v", summary, capture.texts)
	}
	if !strings.HasPrefix(capture.texts[0], "【面试确认--补微信号】") {
		t.Fatalf("补号标题未改写:\n%s", capture.texts[0])
	}
	if !strings.Contains(capture.texts[0], "微信 wx-demo-88") {
		t.Fatalf("补号正文必须带上号:\n%s", capture.texts[0])
	}
}

// 去重 drop 在 tick 内落 skipped;webhook 失败落 failed 且不追图。
func TestTickDropAndFailurePaths(t *testing.T) {
	capture := &wecomCapture{fail: true}
	server := newWecomServer(t, capture)
	defer server.Close()
	now := time.Date(2026, 7, 28, 17, 0, 0, 0, time.Local)
	snapshot := fullSnapshot()
	ledger := &fakeLedger{
		rows: []store.NotificationOutbox{
			{
				ID: 20, NotifyType: store.NotificationTypeWechatAdded,
				ProfileID: "p1", Status: store.NotificationStatusPending, CreatedAt: now.Add(-time.Hour),
			},
			{
				ID: 21, NotifyType: store.NotificationTypeInterviewAccepted,
				ProfileID: "p2", Status: store.NotificationStatusPending, CreatedAt: now.Add(-time.Hour),
			},
		},
		snapshot: map[string]*store.NotificationRenderSnapshot{"p1": snapshot, "p2": fullSnapshot()},
		meeting: map[string]*store.NotificationOutbox{
			"p1": {ID: 3, Status: store.NotificationStatusSent, SentWithWechat: true},
		},
	}
	blobs := &fakeBlobs{data: map[string][]byte{
		snapshot.ChatShot.BlobRef:   jpegBytes(),
		snapshot.ResumeShot.BlobRef: jpegBytes(),
	}}
	summary := newTestRunner(ledger, blobs, server.URL, now).Tick()
	if summary.Dropped != 1 || summary.Failed != 1 || summary.Sent != 0 {
		t.Fatalf("tick 结果不符: %+v", summary)
	}
	if len(ledger.skipped) != 1 || ledger.skipped[0] != 20 {
		t.Fatalf("去重未落 skipped: %+v", ledger.skipped)
	}
	if len(ledger.failed) != 1 || ledger.failed[0] != 21 {
		t.Fatalf("失败未落账: %+v", ledger.failed)
	}
	for _, kind := range capture.kinds {
		if kind == "image" {
			t.Fatalf("文本失败不得追图: %+v", capture.kinds)
		}
	}
}
