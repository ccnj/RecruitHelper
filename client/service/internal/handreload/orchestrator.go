// Package handreload 把「让 Chrome 重新读取磁盘上的插件」编排成一条可复用路径:
// 诊断台按钮与客户端换代后的自动触发器走同一个函数、同一套判据。
//
// 判据不因调用方而放宽。自动路径唯一比人工路径多的是「当前没有活跃产品工作流」
// (见 auto.go),因为人工点按钮时人知道自己在做什么,自动触发没有这个前提。
package handreload

import (
	"context"
	"encoding/json"
	"time"

	"recruithelper/client/service/internal/session"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// ReadyTimeout:派发重载后等待新 hello 的上限。超时不重派(协议规格 §14.1),
// 只转人工。
const ReadyTimeout = 30 * time.Second

// Kind 把编排失败分成调用方需要区分的几类。派发前后的区别是关键:派发前失败
// 是暂时条件,下次可以重来;派发后失败意味着一条维护命令已经出去了,按 §14.1
// 一律转人工,不得自动重来。
type Kind string

const (
	KindUnavailable      Kind = "unavailable"      // 编排依赖未就绪
	KindAmbiguousHand    Kind = "ambiguousHand"    // 零只或多只候选手
	KindHandNotReady     Kind = "handNotReady"     // 目标手不在线或不健康
	KindCapabilityAbsent Kind = "capabilityAbsent" // 手未声明 debug.reload@1
	KindCommandsInFlight Kind = "commandsInFlight" // 该手仍有未收束命令
	KindInternal         Kind = "internal"         // 读库或作废推荐流失败

	// 以下三类发生在 debug.reload 已经派发之后。
	KindDispatchRejected Kind = "dispatchRejected"
	KindTimeout          Kind = "timeout"
	KindContractMismatch Kind = "contractMismatch"
	KindCapabilityLost   Kind = "capabilityLost"
)

// Dispatched 报告这一类失败是否发生在维护命令已经派发之后。自动触发器据此
// 决定是「下轮再看」还是「就此停手交人工」。
func (k Kind) Dispatched() bool {
	switch k {
	case KindDispatchRejected, KindTimeout, KindContractMismatch, KindCapabilityLost:
		return true
	}
	return false
}

// Error 带上给人看的中文说明与相关 msgID,便于 HTTP 层直接回显、日志直接落。
type Error struct {
	Kind    Kind
	Message string
	MsgID   string
	Cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

func fail(kind Kind, msgID, message string, cause error) *Error {
	return &Error{Kind: kind, Message: message, MsgID: msgID, Cause: cause}
}

// Result 是换代成功的构建证词:同一只手、新 bootID、期望 contractHash。
type Result struct {
	HandID           string
	MsgID            string
	PreviousBootID   string
	BootID           string
	ContractHash     string
	ExtensionVersion string
}

// Store 是编排与自动触发器需要的账本切面。写成接口是为了让测试能精确构造
// "有未收束命令" "有活跃工作流" 这两种状态;实现方是 *store.Store。
type Store interface {
	NonTerminalCmdsForHand(handID string) ([]store.CmdRecord, error)
	ActiveProductWorkflowRun() (*store.ProductWorkflowRun, error)
}

// FeedInvalidator 在重载前终止旧推荐流。*patrol.Manager 与 *store.Store 都满足
// 它;组装方负责挑一个非 nil 的传进来(注意 typed-nil:别把 nil 的具体指针塞进
// 接口字段)。
type FeedInvalidator interface {
	InvalidateSourcingFeedsForHand(handID, trigger string, at time.Time) error
}

// Dispatcher 只用来送一条 debug.reload。
type Dispatcher interface {
	Dispatch(handID, name string, args json.RawMessage) (string, error)
}

// Orchestrator 持有编排所需的全部依赖。字段可为 nil,Reload 会报 KindUnavailable
// 而不是 panic —— 管理面在服务尚未组装完时也可能被调用。
type Orchestrator struct {
	Store      Store
	Registry   *session.Registry
	Dispatcher Dispatcher
	Feeds      FeedInvalidator
	Now        func() time.Time

	// Timeout 为零时用 ReadyTimeout。测试用它把等待压到毫秒级。
	Timeout time.Duration
	// Poll 为零时用 100ms。
	Poll time.Duration
}

func (o *Orchestrator) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *Orchestrator) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return ReadyTimeout
}

func (o *Orchestrator) poll() time.Duration {
	if o.Poll > 0 {
		return o.Poll
	}
	return 100 * time.Millisecond
}

// Capability 是重载能力的契约名。
func Capability() string { return protocol.PrimDebugReload + "@1" }

// SelectUniqueHand 在调用方没有指定 handID 时挑出唯一一只可重载的在线手。
// 零只或多只都必须响亮拒绝:自动挑错手会把维护命令送到不相干的浏览器。
func (o *Orchestrator) SelectUniqueHand() (string, *Error) {
	if o.Registry == nil {
		return "", fail(KindUnavailable, "", "重载编排尚未就绪", nil)
	}
	capability := Capability()
	candidates := make([]string, 0, 1)
	for _, state := range o.Registry.Snapshot() {
		if state.Online && state.Health == session.HealthReady && hasString(state.Caps, capability) {
			candidates = append(candidates, state.HandID)
		}
	}
	if len(candidates) != 1 {
		return "", fail(KindAmbiguousHand, "",
			"无法唯一选择可重载的在线插件，请检查插件连接状态或显式指定 handId", nil)
	}
	return candidates[0], nil
}

// Reload 走完一次换代:检查前置 → 终止旧推荐流 → 派发 debug.reload → 等新 hello。
//
// 所有拒绝都发生在终止推荐流之前 —— 这个顺序是有意的:被判据挡下的调用不会留下
// 任何副作用,调用方可以放心重试。一旦命令派发出去,后面的超时与歧义都不再自动
// 重来(协议规格 §14.1)。
func (o *Orchestrator) Reload(ctx context.Context, handID string) (Result, *Error) {
	if o.Store == nil || o.Registry == nil || o.Dispatcher == nil {
		return Result{}, fail(KindUnavailable, "", "重载编排尚未就绪", nil)
	}
	capability := Capability()

	before, ok := o.Registry.Get(handID)
	if !ok || !before.Online || before.Health != session.HealthReady {
		return Result{}, fail(KindHandNotReady, "", "所选手当前未就绪", nil)
	}
	if !hasString(before.Caps, capability) {
		return Result{}, fail(KindCapabilityAbsent, "",
			"当前手尚未具备一键重载能力；首次启用仍需人工重载一次插件", nil)
	}
	pending, err := o.Store.NonTerminalCmdsForHand(handID)
	if err != nil {
		return Result{}, fail(KindInternal, "", err.Error(), err)
	}
	if len(pending) != 0 {
		return Result{}, fail(KindCommandsInFlight, "",
			"该手仍有未收束命令，请先暂停派发并等待命令完成", nil)
	}
	if o.Feeds != nil {
		if err := o.Feeds.InvalidateSourcingFeedsForHand(handID, "adminPluginReload", o.now()); err != nil {
			return Result{}, fail(KindInternal, "", "重载前终止旧推荐流失败", err)
		}
	}

	msgID, err := o.Dispatcher.Dispatch(handID, protocol.PrimDebugReload, []byte(`{}`))
	if err != nil {
		return Result{}, fail(KindDispatchRejected, msgID, err.Error(), err)
	}

	deadline := time.NewTimer(o.timeout())
	defer deadline.Stop()
	ticker := time.NewTicker(o.poll())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return Result{}, fail(KindTimeout, msgID, "等待插件换代被取消", ctx.Err())
		case <-deadline.C:
			return Result{}, fail(KindTimeout, msgID,
				"等待插件换代超时；未自动重派重载命令，请人工检查后再次点击", nil)
		case <-ticker.C:
			current, exists := o.Registry.Get(handID)
			if !exists || !current.Online || current.Health != session.HealthReady ||
				current.BootID == before.BootID {
				continue
			}
			if !current.ContractMatch || current.ContractHash != protocol.ContractHash {
				return Result{}, fail(KindContractMismatch, msgID,
					"插件已经换代，但 contractHash 与当前脑不一致；保持暂停并检查 plugin/dist", nil)
			}
			if !hasString(current.Caps, capability) {
				return Result{}, fail(KindCapabilityLost, msgID,
					"插件已经换代，但新手未声明 debug.reload@1", nil)
			}
			return Result{
				HandID: handID, MsgID: msgID,
				PreviousBootID: before.BootID, BootID: current.BootID,
				ContractHash: current.ContractHash, ExtensionVersion: current.ExtVersion,
			}, nil
		}
	}
}

// AsError 把 *Error 还原成 error 接口,同时保住 nil 语义 —— 直接返回一个 nil 的
// *Error 会得到非 nil 的 error。
func AsError(e *Error) error {
	if e == nil {
		return nil
	}
	return e
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
