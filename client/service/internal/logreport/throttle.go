package logreport

import (
	"fmt"
	"hash/fnv"
	"time"
)

const (
	// 同指纹合并窗口。窗口内的第一条立刻放行,其余只计数,窗口结束时补一条汇总。
	//
	// 这个"先放行再抑制"的次序是有意的:本功能的价值是"及时知道",如果为了合并
	// 而把第一条也压住 5 分钟,那正好把最该快的那一条变慢了。
	defaultMergeWindow = 5 * time.Minute

	// 全局速率上限:每分钟最多放行这么多条。指纹节流已经压住了"同一个错误刷屏",
	// 这道闸防的是另一种形态 —— 一分钟内几百条**各不相同**的错误(比如掉登录后
	// 每个候选人各报各的),它们指纹都不同,压不住,会把队列冲掉。
	// 120 远高于正常量:正常一天的 Error 行也到不了这个数。
	defaultRateLimitPerMinute = 120
)

// fingerprint 是同类事件的合并键。
//
// 取 eventType + code + message 而**不含** attrs:slog 的 message 是固定模板,
// 变化的部分(msgId、profileId)都在 attrs 里。把 attrs 算进来的话每条都是新指纹,
// 节流就完全失效了 —— 而那正是刷屏时最需要它工作的时候。
func (i Item) fingerprint() string {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(i.EventType))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(i.Code))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(i.Message))
	return fmt.Sprintf("%016x", hash.Sum64())
}

// mergeWindow 记一个指纹当前窗口内被抑制了多少条。
type mergeWindow struct {
	openedAt   time.Time
	lastAt     time.Time
	suppressed int
	// sample 保留窗口内第一条的形态,汇总条按它的类型/级别构造,
	// 这样前台筛"某类事件"时汇总条不会漏掉。
	sample Item
}

// admit 判断一条事件是放行还是被并进当前窗口。
// 返回 true 表示放行(调用方继续入队),false 表示已并入计数。
//
// 调用方必须持有 r.mu。
func (r *Reporter) admit(item Item, now time.Time) bool {
	fingerprint := item.fingerprint()
	if window, exists := r.windows[fingerprint]; exists {
		if now.Sub(window.openedAt) < r.mergeWindow {
			window.suppressed++
			window.lastAt = now
			return false
		}
		// 窗口已过期但还没被 sweep 收走:先把它的汇总补进队列,再开新窗口。
		if summary, ok := window.summary(fingerprint); ok {
			r.pushLocked(summary)
		}
	}
	r.windows[fingerprint] = &mergeWindow{openedAt: now, lastAt: now, sample: item}
	return true
}

// sweepWindows 收走已过期的窗口,把被抑制的条数变成汇总事件入队。
// 调用方必须持有 r.mu。
func (r *Reporter) sweepWindows(now time.Time) {
	for fingerprint, window := range r.windows {
		if now.Sub(window.openedAt) < r.mergeWindow {
			continue
		}
		if summary, ok := window.summary(fingerprint); ok {
			r.pushLocked(summary)
		}
		delete(r.windows, fingerprint)
	}
}

// summary 把一个窗口的抑制计数变成一条汇总事件。没抑制过就没有汇总。
func (w *mergeWindow) summary(fingerprint string) (Item, bool) {
	if w.suppressed <= 0 {
		return Item{}, false
	}
	openedAt := w.openedAt
	lastAt := w.lastAt
	summary := w.sample
	summary.OccurredAt = lastAt
	summary.Fingerprint = fingerprint
	summary.MergedCount = w.suppressed
	summary.FirstAt = &openedAt
	summary.LastAt = &lastAt
	summary.Message = fmt.Sprintf("[合并 %d 条] %s", w.suppressed, w.sample.Message)
	return summary, true
}

// allowByRate 是全局速率闸。返回 false 表示这一分钟的额度用完了,该丢。
// 调用方必须持有 r.mu。
func (r *Reporter) allowByRate(now time.Time) bool {
	if now.Sub(r.rateWindowAt) >= time.Minute {
		r.rateWindowAt = now
		r.rateCount = 0
	}
	if r.rateCount >= r.rateLimit {
		return false
	}
	r.rateCount++
	return true
}
