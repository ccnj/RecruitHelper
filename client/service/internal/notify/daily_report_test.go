package notify

import (
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
)

type fakeDailyLedger struct {
	enqueued    []string
	insertSeen  map[string]bool
	counts      store.DailyReportCounts
	interviews  []store.DailyReportInterview
	countsCalls int
}

func (f *fakeDailyLedger) EnqueueDailyReport(localDate string, _ time.Time) (bool, error) {
	f.enqueued = append(f.enqueued, localDate)
	if f.insertSeen == nil {
		f.insertSeen = map[string]bool{}
	}
	if f.insertSeen[localDate] {
		return false, nil
	}
	f.insertSeen[localDate] = true
	return true, nil
}

func (f *fakeDailyLedger) DailyReportCounts(
	_, _ string, _, _ time.Time,
) (store.DailyReportCounts, error) {
	f.countsCalls++
	return f.counts, nil
}

func (f *fakeDailyLedger) DailyReportInterviews(
	_, _ string, _ int64,
) ([]store.DailyReportInterview, error) {
	return f.interviews, nil
}

func dailyRow(id uint64, localDate string) store.NotificationOutbox {
	return store.NotificationOutbox{
		ID:          id,
		NotifyType:  store.NotificationTypeDailyReport,
		EventKey:    store.DailyReportEventKey(localDate),
		PayloadJSON: `{"localDate":"` + localDate + `"}`,
		Status:      store.NotificationStatusPending,
	}
}

// 台账原则:零也照发、格式与非零完全一致;名单空固定「暂无」;不含独立日期行,
// 段头括注数据所属的昨日日期(2026-08-19 甲方修订)。
func TestRenderDailyReport(t *testing.T) {
	yesterday := time.Date(2026, 8, 17, 0, 0, 0, 0, time.Local)
	zero := renderDailyReport("客户丁", yesterday, store.DailyReportCounts{}, nil)
	for _, want := range []string{
		"【工作日报】客户丁",
		"昨日成果(08-17)",
		"换到微信:0 人",
		"约成面试:0 人",
		"待面试安排",
		"暂无",
	} {
		if !strings.Contains(zero, want) {
			t.Fatalf("零日文案缺少 %q:\n%s", want, zero)
		}
	}

	// 2026-08-18 是周二。方式文案复用约面通知标签表;枚举外省略该段;
	// 姓名缺失兜底「候选人」;职位缺失省略括号。
	starts := time.Date(2026, 8, 18, 14, 0, 0, 0, time.Local)
	text := renderDailyReport("客户丁", yesterday, store.DailyReportCounts{Wechat: 3, Appointments: 1},
		[]store.DailyReportInterview{
			{DisplayName: "张三", PositionTitle: "保障顾问", StartsAtMs: starts.UnixMilli(), Method: "wechatVideo"},
			{DisplayName: "李四", PositionTitle: "销售专员", StartsAtMs: starts.Add(24 * time.Hour).UnixMilli(), Method: "mystery"},
			{DisplayName: "", PositionTitle: "", StartsAtMs: starts.Add(48 * time.Hour).UnixMilli(), Method: "onsite"},
		})
	for _, want := range []string{
		"换到微信:3 人",
		"约成面试:1 人",
		"08-18(周二) 14:00  微信视频  张三(保障顾问)",
		"08-19(周三) 14:00  李四(销售专员)",
		"08-20(周四) 14:00  线下面试  候选人",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("日报文案缺少 %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "暂无") {
		t.Fatalf("有名单时不得出现「暂无」:\n%s", text)
	}
}

// 今天的日报行:读账本、发送、落 sent;未绑定平台账号时不查计数、照发零蛋台账。
func TestTickDailyReportSendsAndMarks(t *testing.T) {
	at := time.Date(2026, 8, 18, 9, 30, 0, 0, time.Local)
	capture := &wecomCapture{}
	server := newWecomServer(t, capture)
	defer server.Close()

	ledger := &fakeLedger{rows: []store.NotificationOutbox{dailyRow(9, "2026-08-18")}}
	daily := &fakeDailyLedger{
		counts: store.DailyReportCounts{Wechat: 2, Appointments: 1},
		interviews: []store.DailyReportInterview{{
			DisplayName: "张三", PositionTitle: "保障顾问",
			StartsAtMs: time.Date(2026, 8, 18, 14, 0, 0, 0, time.Local).UnixMilli(),
			Method:     "wechatVideo",
		}},
	}
	runner := newTestRunner(ledger, &fakeBlobs{}, server.URL, at)
	runner.SetDailyReportDeps(DailyReportDeps{
		Ledger:     daily,
		CustomerID: func() int { return 3 },
		Runtime:    func() (string, string) { return "zhilian", "acc-1" },
	})
	summary := runner.Tick()
	if summary.Sent != 1 || len(ledger.sent) != 1 || ledger.sent[0] != 9 {
		t.Fatalf("日报应发送并落 sent: summary=%+v sent=%v", summary, ledger.sent)
	}
	if len(capture.texts) != 1 || !strings.Contains(capture.texts[0], "昨日成果(08-17)") ||
		!strings.Contains(capture.texts[0], "换到微信:2 人") ||
		!strings.Contains(capture.texts[0], "张三(保障顾问)") {
		t.Fatalf("发出的正文不对: %v", capture.texts)
	}
	// 到点入队也在同一 tick 里发生(槽位 08:33 已过)。
	if len(daily.enqueued) != 1 || daily.enqueued[0] != "2026-08-18" {
		t.Fatalf("应同时完成当日入队: %v", daily.enqueued)
	}

	// 未绑定平台账号:不查计数,零蛋照发。
	ledger2 := &fakeLedger{rows: []store.NotificationOutbox{dailyRow(10, "2026-08-18")}}
	daily2 := &fakeDailyLedger{counts: store.DailyReportCounts{Wechat: 99}}
	capture2 := &wecomCapture{}
	server2 := newWecomServer(t, capture2)
	defer server2.Close()
	runner2 := newTestRunner(ledger2, &fakeBlobs{}, server2.URL, at)
	runner2.SetDailyReportDeps(DailyReportDeps{
		Ledger:     daily2,
		CustomerID: func() int { return 3 },
		Runtime:    func() (string, string) { return "", "" },
	})
	runner2.Tick()
	if daily2.countsCalls != 0 {
		t.Fatalf("未绑定账号不应查计数: %d", daily2.countsCalls)
	}
	if len(capture2.texts) != 1 || !strings.Contains(capture2.texts[0], "换到微信:0 人") {
		t.Fatalf("未绑定账号应发零蛋台账: %v", capture2.texts)
	}
}

// 隔日不补发:昨天的行标 skipped,不发送;发送失败按失败重试留痕。
func TestTickDailyReportStaleAndFailure(t *testing.T) {
	at := time.Date(2026, 8, 18, 9, 30, 0, 0, time.Local)
	capture := &wecomCapture{}
	server := newWecomServer(t, capture)
	defer server.Close()

	ledger := &fakeLedger{rows: []store.NotificationOutbox{dailyRow(11, "2026-08-17")}}
	runner := newTestRunner(ledger, &fakeBlobs{}, server.URL, at)
	runner.SetDailyReportDeps(DailyReportDeps{
		Ledger:     &fakeDailyLedger{},
		CustomerID: func() int { return 0 }, // 未激活:不入队,但已有行照常处理
		Runtime:    func() (string, string) { return "", "" },
	})
	summary := runner.Tick()
	if summary.Dropped != 1 || len(ledger.skipped) != 1 || ledger.skipped[0] != 11 {
		t.Fatalf("陈旧日报应标 skipped: summary=%+v skipped=%v", summary, ledger.skipped)
	}
	if len(capture.texts) != 0 {
		t.Fatalf("陈旧日报不得发送: %v", capture.texts)
	}

	// webhook 报错 → 失败重试留痕。
	captureFail := &wecomCapture{fail: true}
	serverFail := newWecomServer(t, captureFail)
	defer serverFail.Close()
	ledgerFail := &fakeLedger{rows: []store.NotificationOutbox{dailyRow(12, "2026-08-18")}}
	runnerFail := newTestRunner(ledgerFail, &fakeBlobs{}, serverFail.URL, at)
	runnerFail.SetDailyReportDeps(DailyReportDeps{
		Ledger:     &fakeDailyLedger{},
		CustomerID: func() int { return 3 },
		Runtime:    func() (string, string) { return "", "" },
	})
	summaryFail := runnerFail.Tick()
	if summaryFail.Failed != 1 || len(ledgerFail.failed) != 1 || ledgerFail.failed[0] != 12 {
		t.Fatalf("发送失败应落 failed 重试: summary=%+v failed=%v", summaryFail, ledgerFail.failed)
	}
}

// 槽位:8:30+客户id 分钟前不入队;到点入队一次;同日后续 tick 不再重复。
func TestMaybeEnqueueDailyReportSlot(t *testing.T) {
	daily := &fakeDailyLedger{}
	ledger := &fakeLedger{}
	runner := newTestRunner(ledger, &fakeBlobs{}, "http://127.0.0.1:1/unused", time.Time{})
	runner.SetDailyReportDeps(DailyReportDeps{
		Ledger:     daily,
		CustomerID: func() int { return 3 },
		Runtime:    func() (string, string) { return "", "" },
	})
	tickAt := func(at time.Time) {
		runner.now = func() time.Time { return at }
		runner.Tick()
	}

	day := time.Date(2026, 8, 18, 0, 0, 0, 0, time.Local)
	tickAt(day.Add(8*time.Hour + 32*time.Minute)) // 槽位 08:33,未到
	if len(daily.enqueued) != 0 {
		t.Fatalf("槽位前不得入队: %v", daily.enqueued)
	}
	tickAt(day.Add(8*time.Hour + 33*time.Minute)) // 到点
	tickAt(day.Add(8*time.Hour + 34*time.Minute)) // 同日不重复
	if len(daily.enqueued) != 1 || daily.enqueued[0] != "2026-08-18" {
		t.Fatalf("到点应恰入队一次: %v", daily.enqueued)
	}
	tickAt(day.AddDate(0, 0, 1).Add(15 * time.Hour)) // 次日下午开机:随到随发
	if len(daily.enqueued) != 2 || daily.enqueued[1] != "2026-08-19" {
		t.Fatalf("次日应再入队一次: %v", daily.enqueued)
	}

	// 未激活(customerID<=0):永不入队。
	dailyIdle := &fakeDailyLedger{}
	runnerIdle := newTestRunner(&fakeLedger{}, &fakeBlobs{}, "http://127.0.0.1:1/unused", time.Time{})
	runnerIdle.SetDailyReportDeps(DailyReportDeps{
		Ledger:     dailyIdle,
		CustomerID: func() int { return 0 },
	})
	runnerIdle.now = func() time.Time { return day.Add(12 * time.Hour) }
	runnerIdle.Tick()
	if len(dailyIdle.enqueued) != 0 {
		t.Fatalf("未激活不得入队: %v", dailyIdle.enqueued)
	}
}
