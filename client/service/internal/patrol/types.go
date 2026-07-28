// Package patrol owns the brain-side account actors. It is the only place that
// decides when perception primitives run; the hand remains a command executor.
package patrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/client/service/internal/workflow"
	"recruithelper/contract/gen/go/protocol"
)

const (
	TriggerTimer = "timer"
	TriggerDirty = "dirty"
	// TriggerCurrentConversation 是真人显式入口：只处理浏览器当前已经打开
	// 的一个会话，不经过列表枚举。
	TriggerCurrentConversation = "manualCurrentConversation"

	surfaceRecoverySuffix = "+surfaceRecovery"

	PauseUserStopped           = "userStopped"
	PauseUserRequested         = "userPaused"
	PauseDailyExpired          = "dailyWindowExpired"
	PauseLoginRequired         = "loginRequired"
	PauseAccountMismatch       = "accountMismatch"
	PauseIdentityInvalid       = "identityInvalid"
	PauseSurfaceDrivenAway     = "surfaceDrivenAway"
	PauseHandManualReview      = "handManualReview"
	PauseSourcingBlocked       = "sourcingBlocked"
	PauseSourcingTargetReached = store.SourcingTargetReachedPauseReason
)

var (
	ErrNilStore                          = errors.New("patrol store 不能为空")
	ErrNilRunner                         = errors.New("patrol runner 不能为空")
	ErrNilHandAvailability               = errors.New("hand availability 不能为空")
	ErrAccountNotBound                   = errors.New("账号尚未绑定手和主体指纹")
	ErrIdentityInvalid                   = errors.New("账号身份绑定已失效，必须真人重新确认")
	ErrIdentityUnobservable              = errors.New("当前页面无法确证账号主体")
	ErrLoginRequired                     = errors.New("招聘平台未登录")
	ErrDailyWindowNotOpen                = errors.New("今日巡检需在本地时间 08:00 后由真人开启")
	ErrDailyWindowExpired                = errors.New("巡检跨过本地日边界，已在 24:00 停止")
	ErrActorPaused                       = errors.New("账号 actor 已停止或暂停，不得派发新命令")
	ErrActorGenerationChanged            = errors.New("账号绑定或手会话已变化，本轮必须停止并由下轮重新探测")
	ErrRoundSupersededBySourcingBatch    = errors.New("活动采集批次已换代，旧巡检轮不得继续派发命令")
	ErrEventHandMismatch                 = errors.New("传感事件来自非绑定手")
	ErrEnsureNotReady                    = errors.New("恢复 IM 页面后仍未就绪")
	ErrPaginationLoop                    = errors.New("分页 cursor 循环")
	ErrPaginationLimit                   = errors.New("分页超过脑侧安全上限")
	ErrPeerChangedInPages                = errors.New("同一线程分页返回了冲突的候选人身份")
	ErrCurrentConversationSourcingActive = errors.New("账号仍有活动采集批次，不能处理当前 IM 会话")
	ErrCurrentConversationUntracked      = errors.New("浏览器当前会话没有可处理的 tracked 候选人档案")
	ErrCurrentConversationV4NotReady     = errors.New("浏览器当前会话没有可自动推进的 V4 根")
	ErrCurrentConversationJobUnbound     = errors.New("浏览器当前会话候选人未绑定后台职位")
	ErrCurrentConversationContextMissing = errors.New("浏览器当前会话职位没有最近成功同步配置")
)

// RunRequest is the narrow seam between the actor and command delivery. The
// dispatch adapter must preserve Args verbatim and put the account fields into
// CmdBody.context. All four M2 primitives use this same Runner method.
type RunRequest struct {
	HandID                       string
	ExpectedSession              string
	ExpectedBootID               string
	Platform                     string
	AccountRef                   string
	ExpectedPrincipalFingerprint string
	Name                         string
	Version                      int
	Args                         json.RawMessage
}

// RunHandle 把短暂的“校验代际+落账+送入当前 socket”与可能持续几十秒的
// 等待结果分开。Manager 只在 Start 周围持 actor 短锁，绝不跨网络等待。
type RunHandle interface {
	LogicalDispatchID() string
	Wait(context.Context) (json.RawMessage, error)
}

type Runner interface {
	Start(context.Context, RunRequest) (RunHandle, error)
}

type ResumeCaptureRequest struct {
	ProfileID                    string
	HandID                       string
	ExpectedSession              string
	ExpectedBootID               string
	Platform                     string
	AccountRef                   string
	ExpectedPrincipalFingerprint string
}

type ResumeCaptureHandle interface {
	LogicalDispatchID() string
	Wait(context.Context) (json.RawMessage, error)
}

type ResumeCaptureRunner interface {
	StartResumeCapture(context.Context, ResumeCaptureRequest) (ResumeCaptureHandle, error)
}

type SourcingResumeRequest struct {
	HandID                       string
	ExpectedSession              string
	ExpectedBootID               string
	Platform                     string
	AccountRef                   string
	ExpectedPrincipalFingerprint string
	ExcludePlatformUserRefs      []string
}

type SourcingResumeHandle interface {
	LogicalDispatchID() string
	Wait(context.Context) (json.RawMessage, error)
}

type SourcingResumeRunner interface {
	StartSourcingResume(context.Context, SourcingResumeRequest) (SourcingResumeHandle, error)
}

// AutomaticGreetingRequest is the only sourcing bridge into the existing
// chat.sendGreeting WAL. The dispatcher re-derives the target, text and stable
// intent from these two immutable source references.
type AutomaticGreetingRequest struct {
	BatchID      string
	InvocationID string
}

type AutomaticGreetingHandle interface {
	Wait(context.Context) error
}

type AutomaticGreetingRunner interface {
	StartAutomaticGreeting(context.Context, AutomaticGreetingRequest) (AutomaticGreetingHandle, error)
}

// AutomaticReplyRequest is the only M5 bridge into the existing
// chat.sendMessage safety rail. IntentID is deterministically derived from
// ActionID; PreviousIntentID preserves the conversation-head CAS.
type AutomaticReplyRequest struct {
	ActionID          string
	IntentID          string
	PreviousIntentID  string
	ExpectedSession   string
	ExpectedBootID    string
	Platform          string
	AccountRef        string
	ConversationRef   string
	Text              string
}

type AutomaticReplyHandle interface {
	Wait(context.Context) error
}

type AutomaticReplyRunner interface {
	StartAutomaticReply(context.Context, AutomaticReplyRequest) (AutomaticReplyHandle, error)
}

type AutomaticCardRequest struct {
	ActionID          string
	IntentID          string
	PreviousIntentID  string
	ExpectedSession   string
	ExpectedBootID    string
	Platform          string
	AccountRef        string
	ConversationRef   string
	Kind              store.CommunicationActionKind
	Interview         *protocol.InterviewDetails
	RequestSourceKey  string
}

type AutomaticCardHandle interface {
	Wait(context.Context) error
}

type AutomaticCardRunner interface {
	StartAutomaticCard(context.Context, AutomaticCardRequest) (AutomaticCardHandle, error)
}

// AdviceExecutor is the provider seam for deterministic communication tests.
// Production deliberately does not wire a real HTTP executor until batch 5;
// when wired, each call still goes through the persisted AIInvocation gate.
type AdviceExecutor interface {
	ProviderName() string
	ModelName() string
	CompleteJSON(context.Context, m5ai.CompletionRequest) (m5ai.CompletionResponse, error)
}

// RunError is the machine-readable failure returned by a Runner adapter.
// Reason is meaningful for CTX_NOT_READY and must come from error.data.reason.
type RunError struct {
	Code       protocol.ErrorCode
	Reason     protocol.NotReadyReason
	Retryable  protocol.Retryable
	SideEffect protocol.SideEffect
	Cause      error
}

func (e *RunError) Error() string {
	if e == nil {
		return "<nil>"
	}
	detail := string(e.Code)
	if e.Reason != "" {
		detail += "/" + string(e.Reason)
	}
	if e.Cause != nil {
		return detail + ": " + e.Cause.Error()
	}
	return detail
}

func (e *RunError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type HandState struct {
	Online      bool
	Session     string
	BootID      string
	UnreadTotal *int
}

type HandAvailability interface {
	State(context.Context, string) (HandState, error)
}

type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// 2026-07-27 甲方调慢拟人节奏：交互 2.5~5 秒、换人 4~8 秒。
// AGENTS.md 冻结的是下限（交互 ≥1 秒、换人 ≥2 秒），本值只准往慢改。
const (
	interactionPaceMin = 2500 * time.Millisecond
	interactionPaceMax = 5 * time.Second
	sourcingPaceMin    = 4 * time.Second
	sourcingPaceMax    = 8 * time.Second
)

func randomInteractionPaceDelay() time.Duration {
	return interactionPaceMin + time.Duration(rand.Int64N(int64(interactionPaceMax-interactionPaceMin)+1))
}

func randomSourcingPaceDelay() time.Duration {
	return sourcingPaceMin + time.Duration(rand.Int64N(int64(sourcingPaceMax-sourcingPaceMin)+1))
}

func defaultSourcingPaceWait(ctx context.Context) error {
	timer := time.NewTimer(randomSourcingPaceDelay())
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func defaultInteractionPaceWait(ctx context.Context) error {
	timer := time.NewTimer(randomInteractionPaceDelay())
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type Config struct {
	Clock                    Clock
	Location                 *time.Location
	PatrolInterval           time.Duration
	IdentityFreshFor         time.Duration
	CoalesceWindow           time.Duration
	MinimumRoundGap          time.Duration
	TrackedReconcileInterval time.Duration
	MaxPages                 int
	DailyWindow              workflow.DailyWindowPolicy
	NewRoundID               func() string
	// SourcingPaceWait 控制脑侧批采与全新自动招呼候选人动作的节奏。
	// 生产默认使用 2～4 秒随机等待；测试可注入无等待实现，手端
	// 仍无业务定时器。
	SourcingPaceWait    func(context.Context) error
	InteractionPaceWait func(context.Context) error
	// SourcingAIRetryWait 控制评分/招呼语生成失败后的重试退避。生产默认
	// 指数退避加抖动：非 429 封顶 4 秒，429（unlimited=true）封顶 60 秒；
	// retrySequence 是该成员本进程内的连续重试序号（从 1 起）。返回非
	// nil（ctx 取消）时调用方停止驱动并保留 inFlight。测试可注入无等待
	// 实现。
	SourcingAIRetryWait func(ctx context.Context, unlimited bool, retrySequence int) error
}

func (c Config) withDefaults() Config {
	if c.Clock == nil {
		c.Clock = realClock{}
	}
	if c.Location == nil {
		c.Location = time.Local
	}
	if c.PatrolInterval <= 0 {
		c.PatrolInterval = 5 * time.Minute
	}
	if c.IdentityFreshFor <= 0 {
		c.IdentityFreshFor = 10 * time.Minute
	}
	if c.CoalesceWindow <= 0 {
		c.CoalesceWindow = time.Duration(protocol.DefaultSensorsPatrolPullForwardMergeMs) * time.Millisecond
	}
	if c.MinimumRoundGap <= 0 {
		c.MinimumRoundGap = time.Duration(protocol.DefaultSensorsPatrolPullForwardMinGapMs) * time.Millisecond
	}
	if c.TrackedReconcileInterval <= 0 {
		c.TrackedReconcileInterval = 30 * time.Minute
	}
	if c.MaxPages <= 0 {
		c.MaxPages = 256
	}
	if c.SourcingPaceWait == nil {
		c.SourcingPaceWait = defaultSourcingPaceWait
	}
	if c.InteractionPaceWait == nil {
		c.InteractionPaceWait = defaultInteractionPaceWait
	}
	if c.SourcingAIRetryWait == nil {
		c.SourcingAIRetryWait = defaultSourcingAIRetryWait
	}
	return c
}

type SkipReason string

const (
	SkipNotEnabled SkipReason = "notEnabled"
	SkipNotDue     SkipReason = "notDue"
	SkipOffline    SkipReason = "handOffline"
	SkipHandState  SkipReason = "handStateError"
)

type AccountSkip struct {
	Key    store.AccountKey
	Reason SkipReason
	Err    error
}

type ConversationProjection struct {
	Key             store.ConversationKey
	Messages        []store.MessageDraft
	CardTransitions []syncledger.CardTransition
}

type RoundOutcome struct {
	Key         store.AccountKey
	RoundID     string
	Trigger     string
	Status      string
	EnsureUsed  bool
	Projections []ConversationProjection
	Err         error
}

type TickResult struct {
	Rounds  []RoundOutcome
	Skipped []AccountSkip
}

func (r TickResult) ProjectionCount() int {
	n := 0
	for _, round := range r.Rounds {
		for _, projection := range round.Projections {
			n += len(projection.Messages) + len(projection.CardTransitions)
		}
	}
	return n
}

func runError(err error) *RunError {
	var target *RunError
	if errors.As(err, &target) {
		return target
	}
	return nil
}

func errorCode(err error) string {
	if typed := runError(err); typed != nil {
		return string(typed.Code)
	}
	if err == nil {
		return ""
	}
	// 日界收束是正常的边界终局，不是脑内部故障；给它专属码，免得诊断
	// 面把每晚 24:00 的自然收束当成 BRAIN_INTERNAL 事故追查。
	if errors.Is(err, ErrDailyWindowExpired) {
		return "DAILY_WINDOW_EXPIRED"
	}
	return "BRAIN_INTERNAL"
}

func validateConfig(c Config) error {
	if c.PatrolInterval <= 0 || c.IdentityFreshFor <= 0 || c.CoalesceWindow <= 0 ||
		c.MinimumRoundGap <= 0 || c.TrackedReconcileInterval <= 0 || c.MaxPages <= 0 ||
		c.SourcingPaceWait == nil || c.InteractionPaceWait == nil {
		return fmt.Errorf("patrol config 含非正参数: %+v", c)
	}
	return nil
}
