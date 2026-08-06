package logreport

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// clock 是可控时钟,用来跨越合并窗口而不必真等 5 分钟。
type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) advance(d time.Duration) { c.now = c.now.Add(d) }

func newThrottleReporter(t *testing.T, deps Deps) (*Reporter, *fakeUploader, *clock) {
	t.Helper()
	tick := &clock{now: time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)}
	deps.Now = tick.Now
	if deps.BatchSize == 0 {
		deps.BatchSize = 1000
	}
	if deps.FlushWait == 0 {
		deps.FlushWait = time.Hour
	}
	reporter, uploader := newTestReporter(deps)
	return reporter, uploader, tick
}

func TestThrottleLetsFirstThroughAndSuppressesRest(t *testing.T) {
	// "先放行再抑制"是有意的次序:本功能的价值是及时知道,若为了合并把第一条
	// 也压住 5 分钟,正好把最该快的那条变慢了。
	reporter, uploader, _ := newThrottleReporter(t, Deps{})

	for index := 0; index < 50; index++ {
		reporter.Report(Item{EventType: FallbackEventType, Message: "AI provider 连接失败"})
	}
	reporter.flush(context.Background())

	items := uploader.all()
	if len(items) != 1 {
		t.Fatalf("同指纹 50 条应只放行 1 条，实得 %d", len(items))
	}
	if items[0].MergedCount != 1 {
		t.Fatalf("放行的第一条不该带合并计数: %+v", items[0])
	}
}

func TestThrottleEmitsSummaryWhenWindowCloses(t *testing.T) {
	reporter, uploader, tick := newThrottleReporter(t, Deps{MergeWindow: 5 * time.Minute})

	for index := 0; index < 50; index++ {
		reporter.Report(Item{EventType: FallbackEventType, Level: "error", Message: "AI provider 连接失败"})
	}
	reporter.flush(context.Background())

	// 窗口未到期，还没有汇总。
	if got := len(uploader.all()); got != 1 {
		t.Fatalf("窗口内只该有第一条，实得 %d", got)
	}

	tick.advance(6 * time.Minute)
	reporter.flush(context.Background())

	items := uploader.all()
	if len(items) != 2 {
		t.Fatalf("窗口结束应补一条汇总，实得 %d 条", len(items))
	}
	summary := items[1]
	if summary.MergedCount != 49 {
		t.Fatalf("汇总应记 49 条被抑制，实得 %d", summary.MergedCount)
	}
	if !strings.HasPrefix(summary.Message, "[合并 49 条]") {
		t.Fatalf("汇总消息应标出合并条数: %s", summary.Message)
	}
	// 汇总条保留原类型与级别，前台按类型筛时才不会漏掉它。
	if summary.EventType != FallbackEventType || summary.Level != "error" {
		t.Fatalf("汇总条丢了原事件的类型或级别: %+v", summary)
	}
	if summary.FirstAt == nil || summary.LastAt == nil || summary.Fingerprint == "" {
		t.Fatalf("汇总条缺时间窗或指纹: %+v", summary)
	}
}

func TestThrottleReopensWindowAfterExpiry(t *testing.T) {
	// 窗口过去之后，同一类错误要能重新被立刻看见 —— 否则一个持续两小时的故障
	// 只会在开头报一次，中间全是静默。
	reporter, uploader, tick := newThrottleReporter(t, Deps{MergeWindow: time.Minute})

	reporter.Report(Item{EventType: FallbackEventType, Message: "掉登录"})
	tick.advance(2 * time.Minute)
	reporter.Report(Item{EventType: FallbackEventType, Message: "掉登录"})
	reporter.flush(context.Background())

	if got := len(uploader.all()); got != 2 {
		t.Fatalf("窗口过期后应重新放行，实得 %d 条", got)
	}
}

func TestThrottleSeparatesDistinctFingerprints(t *testing.T) {
	// 指纹取 eventType+code+message，不含 attrs:slog 的 message 是固定模板，
	// 变化的 msgId/profileId 都在 attrs 里。把 attrs 算进来每条都是新指纹，
	// 节流就完全失效 —— 而那正是刷屏时最需要它工作的时候。
	reporter, uploader, _ := newThrottleReporter(t, Deps{})

	for index := 0; index < 10; index++ {
		reporter.Report(Item{
			EventType: EventSuspectCreated,
			Message:   "命令转 suspect",
			Context:   map[string]any{"msgId": fmt.Sprintf("cmd-%d", index)},
		})
	}
	reporter.Report(Item{EventType: FallbackEventType, Message: "另一类错误"})
	reporter.flush(context.Background())

	items := uploader.all()
	if len(items) != 2 {
		t.Fatalf("attrs 不同但 message 相同的应合并，两类各放行一条，实得 %d", len(items))
	}
}

func TestThrottleRateLimitDropsAndCounts(t *testing.T) {
	// 指纹节流压住"同一个错误刷屏"，这道闸防的是另一种形态:一分钟内几百条
	// 各不相同的错误(比如掉登录后每个候选人各报各的)，指纹都不同，压不住。
	reporter, uploader, _ := newThrottleReporter(t, Deps{RateLimitPerMinute: 5, QueueLimit: 100})

	for index := 0; index < 20; index++ {
		reporter.Report(Item{EventType: FallbackEventType, Message: fmt.Sprintf("错误 %d", index)})
	}
	reporter.flush(context.Background())

	items := uploader.all()
	if len(items) != 6 {
		t.Fatalf("应放行 5 条 + 1 条丢弃通告，实得 %d", len(items))
	}
	notice := items[len(items)-1]
	if notice.EventType != EventQueueDropped || notice.MergedCount != 15 {
		t.Fatalf("被速率闸挡下的 15 条应如实计入丢弃: %+v", notice)
	}
}

func TestThrottleRateWindowResets(t *testing.T) {
	reporter, uploader, tick := newThrottleReporter(t, Deps{RateLimitPerMinute: 2, QueueLimit: 100})

	reporter.Report(Item{Message: "a"})
	reporter.Report(Item{Message: "b"})
	reporter.Report(Item{Message: "c"}) // 超额，丢
	tick.advance(61 * time.Second)
	reporter.Report(Item{Message: "d"}) // 新的一分钟，放行
	reporter.flush(context.Background())

	var passed int
	for _, item := range uploader.all() {
		if item.EventType != EventQueueDropped {
			passed++
		}
	}
	if passed != 3 {
		t.Fatalf("速率窗口应按分钟重置，放行 3 条，实得 %d", passed)
	}
}
