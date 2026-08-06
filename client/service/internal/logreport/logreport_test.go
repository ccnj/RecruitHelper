package logreport

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeUploader 记下每一批上传,并可被指定为失败。
type fakeUploader struct {
	mu      sync.Mutex
	batches [][]Item
	err     error
}

func (f *fakeUploader) upload(_ context.Context, _ Target, items []Item) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	copied := make([]Item, len(items))
	copy(copied, items)
	f.batches = append(f.batches, copied)
	return nil
}

func (f *fakeUploader) all() []Item {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Item
	for _, batch := range f.batches {
		out = append(out, batch...)
	}
	return out
}

func newTestReporter(deps Deps) (*Reporter, *fakeUploader) {
	uploader := &fakeUploader{}
	if deps.Enabled == nil {
		deps.Enabled = func() bool { return true }
	}
	if deps.Target == nil {
		deps.Target = func() (Target, bool) {
			return Target{BaseURL: "http://x", MachineID: "M1", LicenseToken: "T1"}, true
		}
	}
	deps.Upload = uploader.upload
	return New(deps), uploader
}

func TestHandlerPassesThroughAndReportsNamedEvent(t *testing.T) {
	var out bytes.Buffer
	var got []Item
	handler := NewHandler(
		slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}),
		func(item Item) { got = append(got, item) },
	)
	logger := slog.New(handler)

	logger.Warn("命令转 suspect", Event(EventSuspectCreated), "msgId", "cmd-1")

	// stdout 一字不动:brain.log 的内容不因上报而改变。
	if !strings.Contains(out.String(), "命令转 suspect") || !strings.Contains(out.String(), "cmd-1") {
		t.Fatalf("原有日志输出被改动了: %s", out.String())
	}
	if len(got) != 1 {
		t.Fatalf("命名事件应上报 1 条，实得 %d", len(got))
	}
	if got[0].EventType != EventSuspectCreated {
		t.Fatalf("事件类型错: %s", got[0].EventType)
	}
	if got[0].Context["msgId"] != "cmd-1" {
		t.Fatalf("定位标识没带上: %+v", got[0].Context)
	}
	// 标记本身不该混进 context —— 它是"要不要上报"的指令，不是事件字段。
	if _, exists := got[0].Context[EventKey]; exists {
		t.Fatalf("event 标记不该出现在 context 里: %+v", got[0].Context)
	}
}

func TestHandlerReportsErrorLevelWithoutMarker(t *testing.T) {
	var got []Item
	logger := slog.New(NewHandler(
		slog.NewTextHandler(&bytes.Buffer{}, nil),
		func(item Item) { got = append(got, item) },
	))

	logger.Error("没预料到的故障")
	logger.Warn("普通告警，不该上报")
	logger.Info("普通信息，不该上报")

	if len(got) != 1 {
		t.Fatalf("只有 Error 该兜底上报，实得 %d 条", len(got))
	}
	if got[0].EventType != FallbackEventType || got[0].Level != "error" {
		t.Fatalf("兜底行的类型或级别错: %+v", got[0])
	}
}

func TestHandlerKeepsWithAttrs(t *testing.T) {
	// slog.With 之后的日志行,上报也必须带上那些字段 —— 缺的往往正是 profileId。
	var got []Item
	base := slog.New(NewHandler(
		slog.NewTextHandler(&bytes.Buffer{}, nil),
		func(item Item) { got = append(got, item) },
	))

	base.With("profileId", "p-1").Error("出错了", "step", "score")

	if len(got) != 1 {
		t.Fatalf("应上报 1 条，实得 %d", len(got))
	}
	if got[0].Context["profileId"] != "p-1" || got[0].Context["step"] != "score" {
		t.Fatalf("WithAttrs 字段丢失: %+v", got[0].Context)
	}
}

func TestReporterFlushesOnTick(t *testing.T) {
	reporter, uploader := newTestReporter(Deps{FlushWait: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { reporter.Run(ctx); close(done) }()

	reporter.Report(Item{Message: "一条", EventType: EventSuspectCreated})
	waitFor(t, func() bool { return len(uploader.all()) == 1 })
	cancel()
	<-done
}

func TestReporterDropsOldestWhenFull(t *testing.T) {
	// 队列满时丢最旧的:上报价值随时间衰减，新故障比半小时前那条更值得看。
	reporter, uploader := newTestReporter(Deps{QueueLimit: 2, BatchSize: 100, FlushWait: time.Hour})
	reporter.Report(Item{Message: "第一条"})
	reporter.Report(Item{Message: "第二条"})
	reporter.Report(Item{Message: "第三条"})

	reporter.flush(context.Background())

	items := uploader.all()
	if len(items) != 3 {
		t.Fatalf("应有 2 条事件 + 1 条丢弃通告，实得 %d", len(items))
	}
	if items[0].Message != "第二条" || items[1].Message != "第三条" {
		t.Fatalf("丢的应该是最旧那条: %+v", items)
	}
	notice := items[2]
	if notice.EventType != EventQueueDropped || notice.MergedCount != 1 {
		t.Fatalf("丢弃通告不对: %+v", notice)
	}
}

func TestReporterReaddsDropCountWhenUploadFails(t *testing.T) {
	// 上传失败按裁决不重试，但丢弃计数必须加回去 —— 否则这段空白在下一次成功
	// 上报时无人告知，前台会把它读成"这段时间没出事"。
	reporter, uploader := newTestReporter(Deps{BatchSize: 100, FlushWait: time.Hour})
	uploader.err = context.DeadlineExceeded

	reporter.Report(Item{Message: "第一条"})
	reporter.Report(Item{Message: "第二条"})
	reporter.flush(context.Background())

	uploader.mu.Lock()
	uploader.err = nil
	uploader.mu.Unlock()
	reporter.Report(Item{Message: "第三条"})
	reporter.flush(context.Background())

	items := uploader.all()
	if len(items) != 2 {
		t.Fatalf("应有 1 条新事件 + 1 条丢弃通告，实得 %d: %+v", len(items), items)
	}
	if items[1].EventType != EventQueueDropped || items[1].MergedCount != 2 {
		t.Fatalf("失败批次的 2 条应如实计入丢弃: %+v", items[1])
	}
}

func TestReporterSplitsOversizedQueue(t *testing.T) {
	// 后台限批 100 条。整队一次性送过去会被整批 422 拒，那是把"后台暂时连不上"
	// 变成"恢复后一条也传不上"。
	reporter, uploader := newTestReporter(Deps{
		QueueLimit: 300, BatchSize: 1000, FlushWait: time.Hour, RateLimitPerMinute: 1000,
	})
	// 每条 message 不同,好绕开指纹合并 —— 这条测试要验的是切批,不是节流。
	for index := 0; index < 250; index++ {
		reporter.Report(Item{Message: fmt.Sprintf("第 %d 条", index)})
	}

	reporter.flush(context.Background())
	uploader.mu.Lock()
	first := len(uploader.batches[0])
	uploader.mu.Unlock()
	if first > 100 {
		t.Fatalf("单批不得超过后台上限 100，实得 %d", first)
	}

	reporter.flush(context.Background())
	reporter.flush(context.Background())
	if total := len(uploader.all()); total != 250 {
		t.Fatalf("三轮应发完 250 条，实得 %d", total)
	}
}

func TestReporterStaysQuietWhenDisabled(t *testing.T) {
	// 默认关闭是硬约束。关着时也不积压：留着也发不出去，只会把真正需要时的位置占满。
	reporter, uploader := newTestReporter(Deps{
		Enabled:   func() bool { return false },
		BatchSize: 100, FlushWait: time.Hour,
	})
	reporter.Report(Item{Message: "一条"})
	reporter.flush(context.Background())

	if len(uploader.all()) != 0 {
		t.Fatal("开关关闭时不得上报")
	}
	// 关闭期间清掉的不算丢弃：那是人的选择，不是故障。
	reporter.mu.Lock()
	dropped := reporter.dropped
	reporter.mu.Unlock()
	if dropped != 0 {
		t.Fatalf("关闭期间清空不该计入丢弃，实得 %d", dropped)
	}
}

func TestReporterHoldsQueueUntilAuthorized(t *testing.T) {
	// 授权未就绪（全新安装到激活之间）不是故障，事件原样留队等下一轮。
	authorized := false
	reporter, uploader := newTestReporter(Deps{
		Target: func() (Target, bool) {
			if !authorized {
				return Target{}, false
			}
			return Target{BaseURL: "http://x", MachineID: "M1", LicenseToken: "T1"}, true
		},
		BatchSize: 100, FlushWait: time.Hour,
	})

	reporter.Report(Item{Message: "激活前就出的错"})
	reporter.flush(context.Background())
	if len(uploader.all()) != 0 {
		t.Fatal("授权未就绪时不该上报")
	}

	authorized = true
	reporter.flush(context.Background())
	items := uploader.all()
	if len(items) != 1 || items[0].Message != "激活前就出的错" {
		t.Fatalf("授权就绪后应把留队的事件发出去: %+v", items)
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("等待超时")
}
