package notify

import (
	"encoding/base64"
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

// timePtr 给发件箱行填 AssetsRequestedAt:非空=取证已为该行派发过。除了专测
// 「等新图」闸门的用例,其余用例都该填上——不填的话它们测的就不再是自己那条
// 路径,而是 15 分钟兜底。
func timePtr(v time.Time) *time.Time { return &v }

// shotsTakenAt 标注两张图的落库时刻。闸门要拿它和最近一次取证派发比,零值
// 会被判成"上一轮的旧图",所以凡是走 tick 的用例都得显式给。
func shotsTakenAt(
	snapshot *store.NotificationRenderSnapshot,
	at time.Time,
) *store.NotificationRenderSnapshot {
	if snapshot.ChatShot != nil {
		snapshot.ChatShot.CreatedAt = at
	}
	if snapshot.ResumeShot != nil {
		snapshot.ResumeShot.CreatedAt = at
	}
	return snapshot
}

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
		InterviewMethod:     "wechatVideo",
		ChatShot:            &store.CandidateScreenshot{BlobRef: "sha256:" + strings.Repeat("a", 64)},
		ResumeShot:          &store.CandidateScreenshot{BlobRef: "sha256:" + strings.Repeat("b", 64)},
	}
}

func TestRenderInterviewAccepted(t *testing.T) {
	// 联系方式 2026-08-06 起为两行、2026-08-07 修订:有号不再跟状态废话,
	// 字段统一"字段: 值",面试时间下带方式行。
	withPhone := fullSnapshot()
	withPhone.PhoneNumber = "13901234567"
	text := renderInterviewAccepted(withPhone, "客户甲")
	for _, want := range []string{
		"【面试确认】测试候选(客户甲)",
		"面试时间: 07-30(周四) 12:30",
		"方式: 微信视频",
		"微信: wx-demo-88",
		"手机号: 13901234567",
		"职位: 资深销售",
		"聊天记录、简历见下图",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("约面文案缺少 %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "联系方式") || strings.Contains(text, "已成功交换微信") {
		t.Fatalf("旧的联系方式单行/状态废话不应再出现:\n%s", text)
	}

	// 手机号与换到的微信号同串也照出两行:同号去重曾上线又被撤回(2026-08-07
	// 甲方裁决),"有时有有时没有"比重复更困扰,行的有无只反映采没采到。
	sameNumber := fullSnapshot()
	sameNumber.WechatID = "13801995730"
	sameNumber.PhoneNumber = "13801995730"
	text = renderInterviewAccepted(sameNumber, "")
	if !strings.Contains(text, "微信: 13801995730") || !strings.Contains(text, "手机号: 13801995730") {
		t.Fatalf("同号也必须两行齐出:\n%s", text)
	}

	// 线下面试方式文案。
	onsite := fullSnapshot()
	onsite.InterviewMethod = "onsite"
	if text := renderInterviewAccepted(onsite, ""); !strings.Contains(text, "方式: 线下面试") {
		t.Fatalf("线下面试方式行缺失:\n%s", text)
	}

	bare := fullSnapshot()
	bare.WechatID = ""
	bare.WechatState = "invited"
	bare.InterviewStartsAtMs = nil
	bare.InterviewMethod = ""
	bare.ChatShot = nil
	bare.ResumeShot = nil
	text = renderInterviewAccepted(bare, "")
	for _, want := range []string{
		"【面试确认】测试候选",
		"面试时间: 未获取到,请在客户端核对",
		"微信: 未获取(已邀微信)",
		"(本次未附截图)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("降级约面文案缺少 %q:\n%s", want, text)
		}
	}
	// 缺号/方式未知时对应行整行省略,不出现残句。
	if strings.Contains(text, "手机号:") || strings.Contains(text, "方式:") {
		t.Fatalf("缺失项不得渲染残行:\n%s", text)
	}
}

func TestRenderWechatAdded(t *testing.T) {
	text := renderWechatAdded(fullSnapshot(), "客户乙", false)
	for _, want := range []string{
		"【微信互加】测试候选(客户乙)",
		"微信: wx-demo-88",
		"当前状态: 已约面 · 面试 07-30(周四) 12:30",
		"职位: 资深销售",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("互加文案缺少 %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "已成功交换微信") {
		t.Fatalf("有号时不得再跟状态废话:\n%s", text)
	}
	onlyChat := fullSnapshot()
	onlyChat.MainStatus = store.CandidateProfileCommunicating
	onlyChat.ResumeShot = nil
	text = renderWechatAdded(onlyChat, "", false)
	if !strings.Contains(text, "当前状态: 沟通中") || !strings.Contains(text, "聊天记录见下图") ||
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
	if got := profileLine(base()); got != "候选人: 35岁/本科/上海 · 期望 20-30k" {
		t.Fatalf("完整画像行不符: %q", got)
	}
	only := base()
	only.Education, only.City = "", ""
	if got := profileLine(only); got != "候选人: 35岁 · 期望 20-30k" {
		t.Fatalf("缺学历/城市未逐项省略: %q", got)
	}
	noSalary := base()
	noSalary.DesiredSalary = "  "
	if got := profileLine(noSalary); got != "候选人: 35岁/本科/上海" {
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
	if !strings.Contains(text, "微信: ") || !strings.Contains(text, "当前状态:") {
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
	rows       []store.NotificationOutbox
	snapshot   map[string]*store.NotificationRenderSnapshot
	meeting    map[string]*store.NotificationOutbox
	captureErr error

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

// 照抄 store 的判据:仍 pending 且没派过取证的行 = 取证轮还没来;最近一次
// 派发时刻则用来判断那一轮拍的图落库没有。
func (f *fakeLedger) NotificationCaptureGateForProfile(
	profileID string,
) (store.NotificationCaptureGate, error) {
	if f.captureErr != nil {
		return store.NotificationCaptureGate{}, f.captureErr
	}
	gate := store.NotificationCaptureGate{}
	for _, row := range f.rows {
		if row.ProfileID != profileID {
			continue
		}
		if row.Status == store.NotificationStatusPending && row.AssetsRequestedAt == nil {
			gate.Pending = true
		}
		if row.AssetsRequestedAt == nil {
			continue
		}
		if gate.LastDispatchAt == nil || row.AssetsRequestedAt.After(*gate.LastDispatchAt) {
			dispatchedAt := *row.AssetsRequestedAt
			gate.LastDispatchAt = &dispatchedAt
		}
	}
	return gate, nil
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
	snapshot := shotsTakenAt(fullSnapshot(), now.Add(-40*time.Second))
	ledger := &fakeLedger{
		rows: []store.NotificationOutbox{{
			ID: 1, NotifyType: store.NotificationTypeInterviewAccepted,
			ProfileID: "p1", Status: store.NotificationStatusPending, CreatedAt: now.Add(-time.Minute),
			AssetsRequestedAt: timePtr(now.Add(-50 * time.Second)),
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
	snapshot := shotsTakenAt(fullSnapshot(), now.Add(-40*time.Second))
	snapshot.ResumeShot = nil
	ledger := &fakeLedger{
		rows: []store.NotificationOutbox{{
			ID: 2, NotifyType: store.NotificationTypeWechatAdded,
			ProfileID: "p1", Status: store.NotificationStatusPending, CreatedAt: now.Add(-time.Minute),
			AssetsRequestedAt: timePtr(now.Add(-50 * time.Second)),
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

// 二合一场景实测复现(2026-08-09 客户机通知 21):约面通知在闸门里等微信号,
// 号一收编就同事务入队了微信互加通知,而那条通知触发的新一轮取证要十几秒才
// 落库。闸门若只问"图存不存在",约面通知会带着八分钟前的旧图发出。
func TestTickWaitsForInFlightCapture(t *testing.T) {
	capture := &wecomCapture{}
	server := newWecomServer(t, capture)
	defer server.Close()
	now := time.Date(2026, 8, 9, 23, 7, 50, 0, time.Local)
	oldChat, newChat := append(jpegBytes(), "-old"...), append(jpegBytes(), "-new"...)
	snapshot := fullSnapshot()
	oldShotAt := now.Add(-8 * time.Minute) // 约面那一轮拍的
	snapshot.ChatShot = &store.CandidateScreenshot{
		BlobRef: "sha256:old-chat", CreatedAt: oldShotAt,
	}
	snapshot.ResumeShot = &store.CandidateScreenshot{
		BlobRef: "sha256:old-resume", CreatedAt: oldShotAt,
	}
	interview := store.NotificationOutbox{
		ID: 50, NotifyType: store.NotificationTypeInterviewAccepted,
		ProfileID: "p1", Status: store.NotificationStatusPending,
		CreatedAt:         now.Add(-8*time.Minute - 33*time.Second),
		AssetsRequestedAt: timePtr(now.Add(-8 * time.Minute)),
	}
	ledger := &fakeLedger{
		rows: []store.NotificationOutbox{interview, {
			// 收编微信号的同事务入队,取证尚未派发。
			ID: 51, NotifyType: store.NotificationTypeWechatAdded,
			ProfileID: "p1", Status: store.NotificationStatusPending,
			PayloadJSON: `{"exchangeInitiator":"peer"}`, CreatedAt: now,
		}},
		snapshot: map[string]*store.NotificationRenderSnapshot{"p1": snapshot},
		meeting:  map[string]*store.NotificationOutbox{"p1": &interview},
	}
	blobs := &fakeBlobs{data: map[string][]byte{
		"sha256:old-chat":   oldChat,
		"sha256:old-resume": jpegBytes(),
		"sha256:new-chat":   newChat,
		"sha256:new-resume": jpegBytes(),
	}}
	runner := newTestRunner(ledger, blobs, server.URL, now)
	// 一、取证轮还没到访这个候选人。
	if summary := runner.Tick(); summary.Sent != 0 || summary.Held != 2 {
		t.Fatalf("取证未派发时约面通知不得发出: %+v", summary)
	}
	if len(capture.kinds) != 0 {
		t.Fatalf("等待期不得发送: %+v", capture.kinds)
	}

	// 二、取证轮到了,标记先落(防进程中断重复平台交互),图还在拍。这一步是
	// 客户机真实翻车点:只看标记会在此放行,发出去的是八分钟前那张。
	ledger.rows[1].AssetsRequestedAt = timePtr(now.Add(12 * time.Second))
	runner.now = func() time.Time { return now.Add(15 * time.Second) }
	if summary := runner.Tick(); summary.Sent != 0 || summary.Held != 2 {
		t.Fatalf("新图未落库时约面通知不得发出: %+v", summary)
	}
	if len(capture.kinds) != 0 {
		t.Fatalf("新图未落库不得发送: %+v", capture.kinds)
	}

	// 三、新图落库。
	snapshot.ChatShot = &store.CandidateScreenshot{
		BlobRef: "sha256:new-chat", CreatedAt: now.Add(20 * time.Second),
	}
	snapshot.ResumeShot = &store.CandidateScreenshot{
		BlobRef: "sha256:new-resume", CreatedAt: now.Add(27 * time.Second),
	}
	runner.now = func() time.Time { return now.Add(30 * time.Second) }
	if summary := runner.Tick(); summary.Sent != 1 {
		t.Fatalf("取证落地后约面通知未发出: %+v", summary)
	}
	if len(ledger.sent) != 1 || ledger.sent[0] != 50 || !ledger.sentWx[0] {
		t.Fatalf("发出的应是带号的约面通知: %+v %+v", ledger.sent, ledger.sentWx)
	}
	want := base64.StdEncoding.EncodeToString(newChat)
	if len(capture.images) == 0 || string(capture.images[0]) != want {
		t.Fatalf("追发的聊天图不是新拍那张(旧图=%q)",
			base64.StdEncoding.EncodeToString(oldChat))
	}
}

// 补号形态实测复现(2026-08-10 客户机补号行 27):约面通知 15 分钟兜底先发且
// 没带号,后来才换到微信。这条补号通知的取证派发到发出只隔 5 秒,而新图 14 秒
// 后才落库——发出去的是两小时前那张(19 号更甚,发的是前一天晚上的)。
func TestTickSupplementWaitsForFreshCapture(t *testing.T) {
	capture := &wecomCapture{}
	server := newWecomServer(t, capture)
	defer server.Close()
	now := time.Date(2026, 8, 10, 16, 27, 55, 0, time.Local)
	oldShotAt := time.Date(2026, 8, 10, 14, 34, 38, 0, time.Local)
	oldChat, newChat := append(jpegBytes(), "-old"...), append(jpegBytes(), "-new"...)
	snapshot := fullSnapshot()
	snapshot.ChatShot = &store.CandidateScreenshot{
		BlobRef: "sha256:old-chat", CreatedAt: oldShotAt,
	}
	snapshot.ResumeShot = &store.CandidateScreenshot{
		BlobRef: "sha256:old-resume", CreatedAt: oldShotAt,
	}
	meeting := store.NotificationOutbox{
		ID: 26, NotifyType: store.NotificationTypeInterviewAccepted,
		ProfileID: "p1", Status: store.NotificationStatusSent, SentWithWechat: false,
		CreatedAt:         oldShotAt.Add(-time.Minute),
		AssetsRequestedAt: timePtr(oldShotAt.Add(-8 * time.Second)),
	}
	ledger := &fakeLedger{
		rows: []store.NotificationOutbox{meeting, {
			ID: 27, NotifyType: store.NotificationTypeWechatAdded,
			ProfileID: "p1", Status: store.NotificationStatusPending,
			CreatedAt: now.Add(-3 * time.Second),
		}},
		snapshot: map[string]*store.NotificationRenderSnapshot{"p1": snapshot},
		meeting:  map[string]*store.NotificationOutbox{"p1": &meeting},
	}
	blobs := &fakeBlobs{data: map[string][]byte{
		"sha256:old-chat":   oldChat,
		"sha256:old-resume": jpegBytes(),
		"sha256:new-chat":   newChat,
		"sha256:new-resume": jpegBytes(),
	}}
	runner := newTestRunner(ledger, blobs, server.URL, now)
	if summary := runner.Tick(); summary.Sent != 0 || summary.Held != 1 {
		t.Fatalf("取证未派发时补号通知不得发出: %+v", summary)
	}

	// 标记先行、图还在拍:客户机就是在这 5 秒里把两小时前的图发了出去。
	ledger.rows[1].AssetsRequestedAt = timePtr(now.Add(6 * time.Second))
	runner.now = func() time.Time { return now.Add(11 * time.Second) }
	if summary := runner.Tick(); summary.Sent != 0 || summary.Held != 1 {
		t.Fatalf("新图未落库时补号通知不得发出: %+v", summary)
	}

	snapshot.ChatShot = &store.CandidateScreenshot{
		BlobRef: "sha256:new-chat", CreatedAt: now.Add(20 * time.Second),
	}
	snapshot.ResumeShot = &store.CandidateScreenshot{
		BlobRef: "sha256:new-resume", CreatedAt: now.Add(28 * time.Second),
	}
	runner.now = func() time.Time { return now.Add(35 * time.Second) }
	if summary := runner.Tick(); summary.Sent != 1 {
		t.Fatalf("取证落地后补号通知未发出: %+v", summary)
	}
	if len(capture.texts) != 1 || !strings.HasPrefix(capture.texts[0], "【面试确认--补微信号】") {
		t.Fatalf("应按补号标题发出: %+v", capture.texts)
	}
	want := base64.StdEncoding.EncodeToString(newChat)
	if len(capture.images) == 0 || string(capture.images[0]) != want {
		t.Fatalf("补号追发的聊天图不是新拍那张(旧图=%q)",
			base64.StdEncoding.EncodeToString(oldChat))
	}
}

// 等待没有自己的驱动力:取证那轮若始终没跑成,15 分钟兜底照常放行,只是图旧。
func TestTickFallsBackWhenCaptureNeverLands(t *testing.T) {
	capture := &wecomCapture{}
	server := newWecomServer(t, capture)
	defer server.Close()
	now := time.Date(2026, 8, 9, 23, 30, 0, 0, time.Local)
	snapshot := fullSnapshot()
	ledger := &fakeLedger{
		rows: []store.NotificationOutbox{{
			ID: 60, NotifyType: store.NotificationTypeInterviewAccepted,
			ProfileID: "p1", Status: store.NotificationStatusPending,
			CreatedAt:         now.Add(-16 * time.Minute),
			AssetsRequestedAt: timePtr(now.Add(-15 * time.Minute)),
		}, {
			ID: 61, NotifyType: store.NotificationTypeWechatAdded,
			ProfileID: "p1", Status: store.NotificationStatusPending,
			CreatedAt: now.Add(-time.Minute), // 取证一直没派发
		}},
		snapshot: map[string]*store.NotificationRenderSnapshot{"p1": snapshot},
		meeting:  map[string]*store.NotificationOutbox{},
	}
	blobs := &fakeBlobs{data: map[string][]byte{
		snapshot.ChatShot.BlobRef:   jpegBytes(),
		snapshot.ResumeShot.BlobRef: jpegBytes(),
	}}
	if summary := newTestRunner(ledger, blobs, server.URL, now).Tick(); summary.Sent != 1 {
		t.Fatalf("到点未兜底发送: %+v", summary)
	}
	if len(ledger.sent) != 1 || ledger.sent[0] != 60 {
		t.Fatalf("兜底发出的应是约面通知: %+v", ledger.sent)
	}
}

// 待取证查询本身失败:退回只看资产有无的旧行为,不许把通知卡住。
func TestTickSendsWhenCaptureLookupFails(t *testing.T) {
	capture := &wecomCapture{}
	server := newWecomServer(t, capture)
	defer server.Close()
	now := time.Date(2026, 8, 9, 23, 40, 0, 0, time.Local)
	snapshot := fullSnapshot()
	ledger := &fakeLedger{
		rows: []store.NotificationOutbox{{
			ID: 70, NotifyType: store.NotificationTypeInterviewAccepted,
			ProfileID: "p1", Status: store.NotificationStatusPending,
			CreatedAt: now.Add(-time.Minute),
		}},
		snapshot:   map[string]*store.NotificationRenderSnapshot{"p1": snapshot},
		meeting:    map[string]*store.NotificationOutbox{},
		captureErr: io.ErrUnexpectedEOF,
	}
	blobs := &fakeBlobs{data: map[string][]byte{
		snapshot.ChatShot.BlobRef:   jpegBytes(),
		snapshot.ResumeShot.BlobRef: jpegBytes(),
	}}
	if summary := newTestRunner(ledger, blobs, server.URL, now).Tick(); summary.Sent != 1 {
		t.Fatalf("查询失败必须退回旧行为发出: %+v", summary)
	}
}

// 微信互加去重矩阵(照抄旧项目 _decide_wechat_added,叠加 2026-08-06 裁决的
// 候选人主动 2 小时并发窗口)。历史行与非候选人主动的行必须保持旧行为不变。
func TestWechatAddedDedupMatrix(t *testing.T) {
	now := time.Date(2026, 7, 28, 16, 0, 0, 0, time.Local)
	peerPayload := `{"exchangeInitiator":"peer"}`
	selfPayload := `{"exchangeInitiator":"self"}`
	baseRow := store.NotificationOutbox{
		ID: 10, NotifyType: store.NotificationTypeWechatAdded,
		ProfileID: "p1", Status: store.NotificationStatusPending, CreatedAt: now.Add(-time.Hour),
	}
	cases := []struct {
		name       string
		payload    string
		createdAt  time.Time
		meeting    *store.NotificationOutbox
		want       decision
		supplement bool
	}{
		// 旧行为:发起方未知(历史行 "{}")一律立即判,不进并发窗口。
		{"无约面通知", "", baseRow.CreatedAt, nil, decisionSend, false},
		{"约面前换到", "", baseRow.CreatedAt, &store.NotificationOutbox{ID: 99, Status: store.NotificationStatusSent, SentWithWechat: true}, decisionSend, false},
		{"约面仍pending", "", baseRow.CreatedAt, &store.NotificationOutbox{ID: 5, Status: store.NotificationStatusPending}, decisionHold, false},
		{"已随约面带出", "", baseRow.CreatedAt, &store.NotificationOutbox{ID: 5, Status: store.NotificationStatusSent, SentWithWechat: true}, decisionDrop, false},
		// 唯一的补号形态:约面通知确实发到运营手上了,但当时没号。
		{"约面发出未带号", "", baseRow.CreatedAt, &store.NotificationOutbox{ID: 5, Status: store.NotificationStatusSent, SentWithWechat: false}, decisionSend, true},
		// 运营从没收到过面试确认,不能叫"补微信号"。
		{"约面终败", "", baseRow.CreatedAt, &store.NotificationOutbox{ID: 5, Status: store.NotificationStatusFailed}, decisionSend, false},
		{"约面过期", "", baseRow.CreatedAt, &store.NotificationOutbox{ID: 5, Status: store.NotificationStatusExpired}, decisionSend, false},
		// 2 小时并发窗口:只对候选人主动的行生效。
		{"候选人主动·无约面·窗口内", peerPayload, now.Add(-time.Hour), nil, decisionHold, false},
		{"候选人主动·无约面·窗口过", peerPayload, now.Add(-2*time.Hour - time.Minute), nil, decisionSend, false},
		{"我方发起·无约面·立即发", selfPayload, now.Add(-time.Minute), nil, decisionSend, false},
		{"候选人主动·窗口内约面pending", peerPayload, now.Add(-time.Hour),
			&store.NotificationOutbox{ID: 99, Status: store.NotificationStatusPending, CreatedAt: now.Add(-30 * time.Minute)}, decisionHold, false},
		{"候选人主动·窗口内约面带号发出", peerPayload, now.Add(-time.Hour),
			&store.NotificationOutbox{ID: 99, Status: store.NotificationStatusSent, SentWithWechat: true, CreatedAt: now.Add(-30 * time.Minute)}, decisionDrop, false},
		// 约面终败时运营没收到过面试确认,并入失败,恢复单独发出。
		{"候选人主动·窗口内约面终败", peerPayload, now.Add(-time.Hour),
			&store.NotificationOutbox{ID: 99, Status: store.NotificationStatusFailed, CreatedAt: now.Add(-30 * time.Minute)}, decisionSend, false},
		// 约面落在窗口之外:微信互加已(应)单独发过,维持独立发送。
		{"候选人主动·约面在窗口外", peerPayload, now.Add(-3 * time.Hour),
			&store.NotificationOutbox{ID: 99, Status: store.NotificationStatusSent, SentWithWechat: true, CreatedAt: now.Add(-10 * time.Minute)}, decisionSend, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ledger := &fakeLedger{meeting: map[string]*store.NotificationOutbox{}}
			if testCase.meeting != nil {
				ledger.meeting["p1"] = testCase.meeting
			}
			runner := newTestRunner(ledger, &fakeBlobs{}, "http://127.0.0.1:1", now)
			row := baseRow
			row.PayloadJSON = testCase.payload
			row.CreatedAt = testCase.createdAt
			got, supplement := runner.decideWechatAdded(row, now)
			if got != testCase.want || supplement != testCase.supplement {
				t.Fatalf("判定不符: got=(%v,%v) want=(%v,%v)",
					got, supplement, testCase.want, testCase.supplement)
			}
		})
	}
}

// 并发窗口在 tick 级生效:候选人主动、资产齐全也要按住;到点无约面则单独发出。
func TestTickHoldsPeerInitiatedWechatThenSendsAlone(t *testing.T) {
	capture := &wecomCapture{}
	server := newWecomServer(t, capture)
	defer server.Close()
	now := time.Date(2026, 8, 6, 14, 0, 0, 0, time.Local)
	snapshot := shotsTakenAt(fullSnapshot(), now.Add(-58*time.Minute))
	ledger := &fakeLedger{
		rows: []store.NotificationOutbox{{
			ID: 40, NotifyType: store.NotificationTypeWechatAdded,
			ProfileID: "p1", Status: store.NotificationStatusPending,
			PayloadJSON: `{"exchangeInitiator":"peer"}`, CreatedAt: now.Add(-time.Hour),
			AssetsRequestedAt: timePtr(now.Add(-59 * time.Minute)),
		}},
		snapshot: map[string]*store.NotificationRenderSnapshot{"p1": snapshot},
		meeting:  map[string]*store.NotificationOutbox{},
	}
	blobs := &fakeBlobs{data: map[string][]byte{
		snapshot.ChatShot.BlobRef:   jpegBytes(),
		snapshot.ResumeShot.BlobRef: jpegBytes(),
	}}
	runner := newTestRunner(ledger, blobs, server.URL, now)
	if summary := runner.Tick(); summary.Held != 1 || summary.Sent != 0 {
		t.Fatalf("窗口内未按住: %+v", summary)
	}
	if len(capture.kinds) != 0 {
		t.Fatalf("按住期间不得发送: %+v", capture.kinds)
	}
	runner.now = func() time.Time { return now.Add(90 * time.Minute) } // 入队起 2.5 小时
	if summary := runner.Tick(); summary.Sent != 1 || summary.Held != 0 {
		t.Fatalf("窗口到点未单独发出: %+v", summary)
	}
	if len(capture.texts) != 1 || !strings.HasPrefix(capture.texts[0], "【微信互加】") {
		t.Fatalf("到点应按独立微信互加发出: %+v", capture.texts)
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
			AssetsRequestedAt: timePtr(now.Add(-59 * time.Minute)),
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
	if !strings.Contains(capture.texts[0], "微信: wx-demo-88") {
		t.Fatalf("补号正文必须带上号:\n%s", capture.texts[0])
	}
}

// 去重 drop 在 tick 内落 skipped;webhook 失败落 failed 且不追图。
func TestTickDropAndFailurePaths(t *testing.T) {
	capture := &wecomCapture{fail: true}
	server := newWecomServer(t, capture)
	defer server.Close()
	now := time.Date(2026, 7, 28, 17, 0, 0, 0, time.Local)
	snapshot := shotsTakenAt(fullSnapshot(), now.Add(-58*time.Minute))
	ledger := &fakeLedger{
		rows: []store.NotificationOutbox{
			{
				ID: 20, NotifyType: store.NotificationTypeWechatAdded,
				ProfileID: "p1", Status: store.NotificationStatusPending, CreatedAt: now.Add(-time.Hour),
				AssetsRequestedAt: timePtr(now.Add(-59 * time.Minute)),
			},
			{
				ID: 21, NotifyType: store.NotificationTypeInterviewAccepted,
				ProfileID: "p2", Status: store.NotificationStatusPending, CreatedAt: now.Add(-time.Hour),
				AssetsRequestedAt: timePtr(now.Add(-59 * time.Minute)),
			},
		},
		snapshot: map[string]*store.NotificationRenderSnapshot{
			"p1": snapshot,
			"p2": shotsTakenAt(fullSnapshot(), now.Add(-58*time.Minute)),
		},
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
