package logreport

import (
	"context"
	"sync"
	"time"
)

const (
	// 队列上限。满了就丢最旧的并计数 —— 丢弃量本身会随下一批上报出去
	// (裁决:"丢弃量本身须作为一条上报如实告知,不得静默丢弃")。
	//
	// 上限存在的理由不是省内存,是防"上报链路卡住时把脑拖垮":后台不可达时
	// 队列只出不进地涨,而这条链路按裁决绝不允许影响业务。
	defaultQueueLimit = 512

	// 攒批:满 20 条或每 30 秒发一次。客户端节流之后正常一批远小于 20 条。
	defaultBatchSize = 20
	defaultFlushWait = 30 * time.Second

	// 单次上传条数上限,与旧后台 client_log_events.MAX_EVENTS_PER_BATCH 对齐。
	// 两边对不上的后果是整批 422,而客户端不重试 —— 那批就永远没了。
	maxUploadBatch = 100
)

// Deps 是 Reporter 的外部依赖。全部用函数注入,这样本包不认识 store、
// jobconfig 这些业务类型(与 report 包同一取舍)。
type Deps struct {
	// Enabled 读开关。默认关闭是硬约束,读失败一律当关 —— 出错时的安全方向是不传。
	Enabled func() bool
	// Target 取上报去处与身份。授权未就绪时返回 false,本轮整批留在队列里等下一次。
	Target func() (Target, bool)
	// Upload 真正发出去。抽成字段是为了测试能注入假的,不必起 HTTP 服务。
	Upload func(context.Context, Target, []Item) error
	// Record 落一次上报结果与计数,供诊断台显示。可为 nil。
	Record func(at time.Time, ok bool, reason string, sent, dropped int64)
	Now    func() time.Time

	QueueLimit int
	BatchSize  int
	FlushWait  time.Duration
	// MergeWindow / RateLimitPerMinute 是两道节流的参数,零值取默认。
	// 只有测试会设它们 —— 生产上没有配置面,裁决要求的是"必须节流",不是"可调"。
	MergeWindow        time.Duration
	RateLimitPerMinute int
}

// Reporter 收事件、攒批、上传。
type Reporter struct {
	deps Deps

	mu      sync.Mutex
	queue   []Item
	dropped int64
	// wake 让"攒够一批"能立刻触发发送,不必干等到下一个 flush 周期。
	// 容量 1 的缓冲 channel:通知的语义是"有活干",多次通知合并成一次即可。
	wake chan struct{}

	// 节流状态(见 throttle.go)。windows 按指纹合并同类事件;rate* 是全局速率闸。
	mergeWindow  time.Duration
	windows      map[string]*mergeWindow
	rateLimit    int
	rateCount    int
	rateWindowAt time.Time
}

func New(deps Deps) *Reporter {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.QueueLimit <= 0 {
		deps.QueueLimit = defaultQueueLimit
	}
	if deps.BatchSize <= 0 {
		deps.BatchSize = defaultBatchSize
	}
	if deps.FlushWait <= 0 {
		deps.FlushWait = defaultFlushWait
	}
	if deps.MergeWindow <= 0 {
		deps.MergeWindow = defaultMergeWindow
	}
	if deps.RateLimitPerMinute <= 0 {
		deps.RateLimitPerMinute = defaultRateLimitPerMinute
	}
	return &Reporter{
		deps:        deps,
		wake:        make(chan struct{}, 1),
		mergeWindow: deps.MergeWindow,
		windows:     make(map[string]*mergeWindow),
		rateLimit:   deps.RateLimitPerMinute,
	}
}

// Report 入队一条事件。**绝不阻塞、绝不返回错误、绝不 panic** —— 它挂在 slog
// 的写路径上,任何一处阻塞都会顺着日志调用蔓延到业务链路里去。
//
// 两道节流在这里生效(见 throttle.go):同指纹窗口内只放行第一条,其余计数、
// 窗口末补一条汇总;全局每分钟额度用完后一律丢弃并计数。
func (r *Reporter) Report(item Item) {
	if r == nil {
		return
	}
	// 规范化两个字段:调用方(尤其是将来接进来的手侧转发)未必填。零值 MergedCount
	// 会撞上后台模型的 ge=1 校验,零值时刻会让前台看到 1970 年。
	if item.MergedCount <= 0 {
		item.MergedCount = 1
	}
	now := r.deps.Now()
	if item.OccurredAt.IsZero() {
		item.OccurredAt = now
	}
	r.mu.Lock()
	if !r.admit(item, now) {
		r.mu.Unlock()
		return
	}
	if !r.allowByRate(now) {
		// 速率闸挡下的照样计入丢弃 —— 裁决要求丢弃量如实告知。
		r.dropped++
		r.mu.Unlock()
		return
	}
	r.pushLocked(item)
	full := len(r.queue) >= r.deps.BatchSize
	r.mu.Unlock()

	if full {
		select {
		case r.wake <- struct{}{}:
		default: // 已经有一个待处理的通知了,合并掉
		}
	}
}

// pushLocked 把一条事件放进队列。队列满时丢最旧的:上报的价值随时间衰减,
// 新的故障比半小时前那条更值得看。调用方必须持有 r.mu。
func (r *Reporter) pushLocked(item Item) {
	if len(r.queue) >= r.deps.QueueLimit {
		r.queue = r.queue[1:]
		r.dropped++
	}
	r.queue = append(r.queue, item)
}

// Run 跑攒批循环,直到 ctx 结束。
func (r *Reporter) Run(ctx context.Context) {
	ticker := time.NewTicker(r.deps.FlushWait)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// 退出前尽力发一次:脑正常关闭时,队列里往往正躺着导致关闭的那条错误。
			// 用独立的短超时 context —— 此时 ctx 已经取消,拿它发请求必然失败。
			flushCtx, cancel := context.WithTimeout(context.Background(), UploadTimeout)
			r.flush(flushCtx)
			cancel()
			return
		case <-ticker.C:
			r.flush(ctx)
		case <-r.wake:
			r.flush(ctx)
		}
	}
}

// flush 发出当前队列。失败即丢弃并计数,不重试、不建发件箱 —— 事件上报只负责
// "快"不负责"全",漏掉的由每日整包上报兜底(立法四问第四问的取舍)。
func (r *Reporter) flush(ctx context.Context) {
	if !r.enabled() {
		// 开关关着时不积压:留着也发不出去,只会把真正需要时的队列位置占满。
		r.drainQuietly()
		return
	}
	target, ok := r.deps.Target()
	if !ok {
		// 授权未就绪(全新安装到激活之间)不是故障,原样留队等下一轮。
		return
	}

	r.mu.Lock()
	// 先收走过期的合并窗口:它们的汇总条要跟这一批一起走,不然"这 5 分钟又发生了
	// N 次"会一直拖到下次有新事件时才发出去。
	r.sweepWindows(r.deps.Now())
	if len(r.queue) == 0 && r.dropped == 0 {
		r.mu.Unlock()
		return
	}
	// 一次最多发 maxUploadBatch 条,与旧后台的批量上限对齐。后台不可达期间队列
	// 会涨到 QueueLimit(512),整队一次性送过去会被整批 422 拒 —— 那是把"后台
	// 暂时连不上"变成"恢复后一条也传不上"。多出来的留在队列里,下面立刻唤醒续发。
	take := len(r.queue)
	if take > maxUploadBatch-1 { // 留一个位置给丢弃通告
		take = maxUploadBatch - 1
	}
	batch := r.queue[:take:take]
	r.queue = r.queue[take:]
	dropped := r.dropped
	r.dropped = 0
	remaining := len(r.queue)
	r.mu.Unlock()

	if remaining > 0 {
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}

	// eventCount 是这批里真实事件的条数,不含下面可能追加的那条丢弃通告。
	// 上传失败时要靠它把计数如实加回去。
	eventCount := int64(len(batch))
	if dropped > 0 {
		batch = append(batch, r.dropNotice(dropped))
	}
	if len(batch) == 0 {
		return
	}

	err := r.deps.Upload(ctx, target, batch)
	if err != nil {
		// 按裁决不重试:批次就此丢弃。但**丢弃计数要加回去**,否则这段空白在
		// 下一次成功上报时无人告知,前台会把它读成"这段时间没出事"。
		// 加回的是"这批的真实事件数 + 本来就欠着的那些",通告条自己不算。
		r.mu.Lock()
		r.dropped += dropped + eventCount
		r.mu.Unlock()
	}
	if r.deps.Record != nil {
		if err != nil {
			r.deps.Record(r.deps.Now(), false, err.Error(), 0, eventCount)
		} else {
			r.deps.Record(r.deps.Now(), true, "", int64(len(batch)), dropped)
		}
	}
}

// dropNotice 把"这里丢了 N 条"本身做成一条事件。裁决明确要求丢弃量如实告知,
// 不得静默丢弃 —— 前台看到一条 400 的丢弃记录,才知道那段时间的空白不是没出事。
func (r *Reporter) dropNotice(dropped int64) Item {
	now := r.deps.Now()
	return Item{
		OccurredAt:  now,
		Source:      SourceBrain,
		Level:       "warn",
		EventType:   EventQueueDropped,
		Message:     "日志上报队列溢出，部分事件被丢弃",
		MergedCount: int(dropped),
		Context:     map[string]any{"droppedCount": dropped},
	}
}

// drainQuietly 在开关关闭时清空队列。这里不计入 dropped:开关关着是人的选择,
// 不是故障,把它算成"丢弃"会让开启后的第一批上报挂着一个莫名其妙的大数字。
func (r *Reporter) drainQuietly() {
	r.mu.Lock()
	r.queue = nil
	r.dropped = 0
	r.mu.Unlock()
}

func (r *Reporter) enabled() bool {
	if r.deps.Enabled == nil {
		return false
	}
	return r.deps.Enabled()
}

// EventQueueDropped 是队列溢出丢弃的事件类型。
const EventQueueDropped = "logreport.queueDropped"
