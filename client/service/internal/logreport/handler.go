package logreport

import (
	"context"
	"log/slog"
	"time"
)

// Handler 包在真正的 slog handler 外面:原有输出一字不动地照常写 stdout
// (于是 brain.log 完全不变),同时把该上报的行喂给 sink。
//
// 顺序上先写 stdout 再喂 sink,且 sink 必须是非阻塞的 —— 上报这条链路上的任何
// 问题都不许影响日志本身,更不许影响业务。
type Handler struct {
	inner slog.Handler
	sink  func(Item)
	// attrs 累积 WithAttrs 挂上来的字段。不累积的话,slog.With(...) 之后的日志行
	// 上报出去就会缺掉那些字段,而缺的往往正是定位用的 profileId。
	attrs []slog.Attr
}

// NewHandler 包装 inner。sink 为 nil 时退化成纯透传。
func NewHandler(inner slog.Handler, sink func(Item)) *Handler {
	return &Handler{inner: inner, sink: sink}
}

func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	merged = append(merged, attrs...)
	return &Handler{inner: h.inner.WithAttrs(attrs), sink: h.sink, attrs: merged}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	// 分组只透传给 inner。上报侧刻意不实现分组前缀:本仓库零处使用 slog.WithGroup,
	// 为一个没有用户的形态维护一套键名拼接逻辑不划算(防护成本预算第 6 条同源)。
	return &Handler{inner: h.inner.WithGroup(name), sink: h.sink, attrs: h.attrs}
}

func (h *Handler) Handle(ctx context.Context, record slog.Record) error {
	err := h.inner.Handle(ctx, record)
	if h.sink == nil {
		return err
	}
	if item, ok := h.itemFor(record); ok {
		h.sink(item)
	}
	return err
}

// itemFor 判断这一行要不要上报,顺带把它转成上报载荷。
//
// 两个来源:带 Event(...) 标记的命名事件(任何级别),以及级别 ≥ Error 的兜底行。
// 兜底是必要的 —— 排障最需要的恰恰是没预料到、因而没登记成命名事件的那些故障。
func (h *Handler) itemFor(record slog.Record) (Item, bool) {
	eventType, source, code := "", SourceBrain, ""
	fields := make(map[string]any, record.NumAttrs()+len(h.attrs))
	// 三个 attr 键有结构含义,它们进 Item 的对应字段而不是 context:重复放两份
	// 只会让前台的上下文栏里挂着几个已经单独成列的值。
	collect := func(attr slog.Attr) bool {
		switch attr.Key {
		case EventKey:
			eventType = attr.Value.String()
		case SourceKey:
			source = attr.Value.String()
		case CodeKey:
			code = attr.Value.String()
		default:
			fields[attr.Key] = attr.Value.Any()
		}
		return true
	}
	for _, attr := range h.attrs {
		collect(attr)
	}
	record.Attrs(collect)

	named := eventType != ""
	if !named && record.Level < slog.LevelError {
		return Item{}, false
	}
	if !named {
		eventType = FallbackEventType
	}

	at := record.Time
	if at.IsZero() {
		at = time.Now()
	}
	return Item{
		OccurredAt:  at,
		Source:      source,
		Level:       levelName(record.Level),
		EventType:   eventType,
		Code:        code,
		Message:     record.Message,
		MergedCount: 1,
		Context:     fields,
	}, true
}

// FallbackEventType 是没有登记成命名事件、靠级别兜底上来的行的类型。
const FallbackEventType = "log.error"

// levelName 把 slog 级别映射成后台表里的 level 值。
//
// slog 没有 Fatal 级,所以用 Error+4 以上代表"启动失败即退出"那类行,它们比普通
// Error 更该被一眼看见。低于 Warn 的只可能来自命名事件(兜底那条要求 ≥ Error),
// 照实记 info —— 后台刻意没给 level 加枚举约束,宁可存下真值也不要谎报成 warn。
func levelName(level slog.Level) string {
	switch {
	case level >= slog.LevelError+4:
		return "fatal"
	case level >= slog.LevelError:
		return "error"
	case level >= slog.LevelWarn:
		return "warn"
	default:
		return "info"
	}
}
