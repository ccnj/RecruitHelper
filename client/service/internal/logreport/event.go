// Package logreport 把脑侧(以及经脑转发的手侧)日志事件推给旧后台
// (AGENTS.md「全局约定·日志上报」,2026-08-06 甲方裁决)。
//
// 它跟 report 包(现场数据上报)是两件事:那个是整包、人点一下或每天一次、
// 15~20MB,解决"事后查得到";这个是事件级、出事就推、KB 级,解决"及时知道"。
//
// **唯一入口是 slog。** 命名事件不另开一套 API,只是在原有的日志调用上多带一个
// logreport.Event(...) 标记;级别 ≥ Error 的日志行自动兜底。这样业务代码不必到处
// 注入上报器依赖,而且上报出去的内容与 brain.log 里的那一行天然一致 —— 排障时
// 不会出现"日志说 A、上报说 B"。
//
// 三条边界按裁决实现,改动前先读 AGENTS.md 那一条:
//   - **装配环节不去查隐私字段**:简历正文、权威 ContactAsset 微信号、API key、
//     ai-traces.db 原文一律不回查。这是"不去取",不是"要滤掉"——
//     日志行和聊天正文里已有的手机号、微信号原样上传,不扫描、不做内容级脱敏。
//   - **不回写日志行**:候选人姓名与聊天正文只存在于上报载荷,brain.log 不变。
//   - **失败绝不影响业务**:入队非阻塞、上传失败即丢弃计数,由整包上报兜底。
package logreport

import (
	"log/slog"
	"time"
)

// EventKey 是标记命名事件的 slog attr 键。
//
// 它是普通可读键,会照常出现在 brain.log 里(event=suspect.created),这是有意的:
// 同一个标记既让上报器认出这条要发,也让翻日志的人一眼看出这是登记在册的事件。
const EventKey = "event"

// Event 返回一个把当前日志行标记为「命名事件」的 attr。
//
//	slog.Warn("命令转 suspect", logreport.Event(EventSuspectCreated), "msgId", id)
//
// 带了它的日志行无论级别都会进上报队列;没带的,只有 ≥ Error 才兜底。
func Event(name string) slog.Attr { return slog.String(EventKey, name) }

// 已登记的命名事件。裁决要求这是一份**封闭清单**,并且明确禁止任何形如
// "脑已启动""我还活着"的周期性存活事件 —— 那是 heartbeat,不在本通道的授权内。
//
// 加新事件的门槛:它得是"人看到就要去做点什么"的事。只是想留个痕迹的,
// 打普通日志即可,整包上报每天会把 brain.log 带走。
const (
	// EventSuspectCreated 一条 effectful 命令转 suspect,永不自动重试、待人工裁决。
	// 排在第一个是因为它是本功能最初的立案理由:2026-08-05 客户机一条招呼 suspect
	// 把同批其余 32 人冻结了 72 分钟,而我方是事后翻包才知道的。
	EventSuspectCreated = "suspect.created"
)

// SourceBrain / SourceHand 区分事件是脑自己产生的,还是手侧经 handLog 转发上来的。
const (
	SourceBrain = "brain"
	SourceHand  = "hand"
)

// Item 是一条待上报的事件。字段与旧后台 client_log_events 表一一对应。
type Item struct {
	OccurredAt time.Time
	Source     string
	Level      string
	EventType  string
	Code       string
	Message    string

	// 节流合并后的信息。MergedCount 为 1 表示没被合并过。
	Fingerprint string
	MergedCount int
	FirstAt     *time.Time
	LastAt      *time.Time

	// Context 装定位标识(profileId、职位名、会话引用、命令名与 idemKey、原语名)
	// 与该会话最近若干条聊天正文。**不含**简历正文、结构化微信号、API key
	// 与 provider 原文 —— 装配时就不去查它们。
	Context map[string]any
}
