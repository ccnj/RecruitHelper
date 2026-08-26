package patrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"recruithelper/client/service/internal/logreport"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

// Manager serializes each account actor's scheduling decisions. Events never
// execute commands: they only persist dirty/quiet state, update process-local
// scheduling hints and pull a future tick.
type Manager struct {
	store  *store.Store
	runner Runner
	hands  HandAvailability
	config Config

	// adviceEngine 是可运行期换代的 AI 引擎(模型配置落盘即生效,2026-08-12
	// 甲方裁决)。只经 currentAdvice/SetAdvice 访问;业务函数在入口取一次快照、
	// 函数内从头用到尾,在途调用拿旧引擎自然收尾,新调用拿新引擎。
	// replyAdviceEngine 是回复族三用途(reply/serviceReply/silenceFollowup)的
	// 专用「次聪明ai」引擎(AGENTS.md「LLM provider 直连」2026-08-24 增补)。
	// nil = 未配置 = 特性关闭,回复族沿用 adviceEngine(配置级默认,非运行期
	// 主备切换);非 nil 时回复族只走它。取用一律经 adviceFor,保证账本记的
	// provider/model 就是实际调用的引擎。
	adviceMu          sync.RWMutex
	adviceEngine      AdviceExecutor
	replyAdviceEngine AdviceExecutor

	// startedAt 是本次脑进程的启动时刻(取自注入时钟,构造时记录一次)。
	// 它只服务 Q1/Q2 陈旧 planned 判定(stalePlannedBoundary):崩溃/重启前
	// 生成、从未绑定发送意图的 planned 残留在派发遭遇时刻一律作废。
	startedAt time.Time

	mu         sync.Mutex // 短临界区：UI/事件对 actor 状态的修改
	tickMu     sync.Mutex // 只串行 Tick，不得阻塞传感事件和用户暂停
	scoreMu    sync.Mutex // 串行统一评分；不占用 hand/巡检锁
	greetingMu sync.Mutex // 串行批次招呼语生成；不占用 hand/巡检锁
	gateMu     sync.RWMutex
	// workflowMemberGate 只裁决“能否开始下一位”。已经绑定 WAL 的
	// 唯一 intent 收编路径不得调用它，以免暂停破坏安全内核收敛。
	workflowMemberGate func() error
	// workflowConversationGate 只在页面列表准备开始下一位候选人时读取。
	// 它不会插入当前候选人的 AI、WAL、正文或卡片动作链中，因此“再采
	// 一批/结束”请求可以等待当前候选人自然收束，而不会切断半轮动作。
	workflowConversationGate func() (bool, error)

	// verifiedListHints is a process-local convergence memory for low-fidelity
	// conversation-list hints. It is accessed only while mu is held and is
	// intentionally absent from persistence, diagnostics and product APIs.
	verifiedListHints map[listHintVerificationKey]string
}

func NewManager(db *store.Store, runner Runner, hands HandAvailability, config Config, advice ...AdviceExecutor) (*Manager, error) {
	if db == nil {
		return nil, ErrNilStore
	}
	if runner == nil {
		return nil, ErrNilRunner
	}
	if hands == nil {
		return nil, ErrNilHandAvailability
	}
	config = config.withDefaults()
	if config.NewRoundID == nil {
		config.NewRoundID = ids.NewMsgID
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	manager := &Manager{
		store: db, runner: runner, hands: hands, config: config,
		startedAt:         config.Clock.Now(),
		verifiedListHints: make(map[listHintVerificationKey]string),
	}
	if len(advice) > 0 {
		manager.adviceEngine = advice[0]
	}
	return manager, nil
}

// currentAdvice 取当前 AI 引擎快照;nil 表示引擎尚未装配(模型配置缺失)。
func (m *Manager) currentAdvice() AdviceExecutor {
	m.adviceMu.RLock()
	defer m.adviceMu.RUnlock()
	return m.adviceEngine
}

// SetAdvice 运行期换代 AI 引擎(含从 nil 首次装配)。不做任何静默点等待:
// 甲方 2026-08-12 明示接受在途调用链新旧引擎混用与混模型批次。
func (m *Manager) SetAdvice(advice AdviceExecutor) {
	m.adviceMu.Lock()
	defer m.adviceMu.Unlock()
	m.adviceEngine = advice
}

// SetReplyAdvice 运行期换代回复族专用引擎(含从 nil 首次装配),换代语义与
// SetAdvice 同款:在途调用拿旧引擎收尾,不等静默点。
func (m *Manager) SetReplyAdvice(advice AdviceExecutor) {
	m.adviceMu.Lock()
	defer m.adviceMu.Unlock()
	m.replyAdviceEngine = advice
}

// adviceFor 按用途取引擎:回复族三用途在次聪明已装配时走次聪明,其余用途
// 与"次聪明未配置"一律回到客户级引擎。调用方必须用它的返回值同时取身份
// (ProviderName/ModelName)与发起调用,不得一半问它、一半问 currentAdvice——
// 那会让账本记下没被调用的模型。
func (m *Manager) adviceFor(purpose m5ai.CompletionPurpose) AdviceExecutor {
	m.adviceMu.RLock()
	defer m.adviceMu.RUnlock()
	if m.replyAdviceEngine != nil && isReplyFamilyPurpose(purpose) {
		return m.replyAdviceEngine
	}
	return m.adviceEngine
}

// isReplyFamilyPurpose 是次聪明的封闭用途清单(AGENTS.md 次聪明段):往会话里
// 对候选人说话的三类生成。意向判断是分类不是说话,刻意不在其中。
func isReplyFamilyPurpose(purpose m5ai.CompletionPurpose) bool {
	return purpose == m5ai.PurposeReply || purpose == m5ai.PurposeServiceReply ||
		purpose == m5ai.PurposeSilenceFollowup
}

// SetWorkflowMemberGate installs the single product-workflow member boundary.
// It is intentionally a local brain callback, not a protocol capability.
func (m *Manager) SetWorkflowMemberGate(gate func() error) {
	m.gateMu.Lock()
	m.workflowMemberGate = gate
	m.gateMu.Unlock()
}

// SetWorkflowConversationGate installs the product workflow's coarse
// candidate boundary. It is a local brain callback and does not expand the
// brain-hand contract.
func (m *Manager) SetWorkflowConversationGate(gate func() (bool, error)) {
	m.gateMu.Lock()
	m.workflowConversationGate = gate
	m.gateMu.Unlock()
}

func (m *Manager) mayStartNextWorkflowMember() error {
	m.gateMu.RLock()
	gate := m.workflowMemberGate
	m.gateMu.RUnlock()
	if gate == nil {
		return nil
	}
	return gate()
}

func (m *Manager) mayStartNextConversation() (bool, error) {
	m.gateMu.RLock()
	gate := m.workflowConversationGate
	m.gateMu.RUnlock()
	if gate == nil {
		return true, nil
	}
	return gate()
}

// RunAtPatrolBoundary waits for any current Tick (and therefore its current
// candidate action chain) to finish, then runs action before another Tick can
// begin. Callers must not hold a lock that a patrol round may acquire.
func (m *Manager) RunAtPatrolBoundary(action func() error) error {
	if action == nil {
		return errors.New("巡检边界动作不能为空")
	}
	m.tickMu.Lock()
	defer m.tickMu.Unlock()
	return action()
}

// BindAccountObservationIfCurrent 是生产管理面绑定账号的唯一写入口。锁序固定
// 为 Manager.mu → Hub.mu → Store：改绑既和命令 Start 线性化，也和 probe 所属
// session/boot 的最终提交栅栏线性化，且不形成 Hub.mu → Manager.mu 反向死锁。
// 真人 probe 网络等待仍在调用方、所有锁外完成。
func (m *Manager) BindAccountObservationIfCurrent(
	key store.AccountKey,
	handID, fingerprint, session, bootID string,
	at time.Time,
	reusePrincipal bool,
	withCurrent func(commit func() error) (bool, error),
) (bound *store.Account, created bool, current bool, err error) {
	if withCurrent == nil {
		return nil, false, false, errors.New("当前 hand session 提交栅栏不能为空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err = withCurrent(func() error {
		bound, created, err = m.store.BindAccountObservation(
			key, handID, fingerprint, session, bootID, at, reusePrincipal,
		)
		return err
	})
	return bound, created, current, err
}

// EnableToday is the sole daily actor gate. A temporarily unobservable but
// still-bound identity may be enabled; the first round must probe it again.
func (m *Manager) EnableToday(key store.AccountKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	open, err := m.config.DailyWindow.Evaluate(now, m.config.Location)
	if err != nil {
		return err
	}
	if !open {
		return ErrDailyWindowNotOpen
	}
	return m.store.MutateAccount(key, func(account *store.Account) error {
		return m.enableAccountToday(account, now)
	})
}

// StartSourcing 创建或复用唯一 preparing 正式批次，并开启账号 actor。
// SourcingBatch 是采集运行的唯一事实；Account 上的旧 sourcing_enabled
// 不再授权正式采集，只保留 schema 兼容。
// captureLimit 为 0 表示本批不分轮;大于 0 时是整批累计可采人数的上限。
func (m *Manager) StartSourcing(
	key store.AccountKey,
	revisionHash string,
	targetCount, captureLimit int,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	revisionHash = strings.TrimSpace(revisionHash)
	if revisionHash == "" || len(revisionHash) > 128 {
		return store.ErrJobAIContextRevisionInvalid
	}
	if targetCount <= 0 {
		return store.ErrSourcingBatchInvalid
	}
	if captureLimit != 0 && captureLimit < targetCount {
		return store.ErrSourcingBatchInvalid
	}
	now := m.now()
	// 采集与沟通共用同一业务窗口。凌晨真人点击也不登记预约，更不能先建
	// 批次后等待到点；先拒绝，保证零业务事实和零后续派发。
	open, err := m.config.DailyWindow.Evaluate(now, m.config.Location)
	if err != nil {
		return err
	}
	if !open {
		return ErrDailyWindowNotOpen
	}
	revision, err := m.store.JobAIContextRevisionByHash(revisionHash)
	if err != nil {
		return err
	}
	if revision == nil {
		return store.ErrJobAIContextRevisionNotFound
	}
	if _, err := m5ai.DeriveSourcingView(revision.SourcePackage); err != nil {
		return err
	}
	account, err := m.store.AccountByKey(key)
	if err != nil {
		return err
	}
	if account == nil {
		return store.ErrAccountNotFound
	}
	// 先在副本上完成所有每日门禁校验，避免校验失败后留下空跑批次。
	accountCheck := *account
	if err := m.enableAccountToday(&accountCheck, now); err != nil {
		return err
	}
	started, err := m.store.StartSourcingBatch(store.StartSourcingBatchRequest{
		Platform: key.Platform, AccountRef: key.AccountRef,
		ContextRevisionHash: revisionHash, TargetCount: targetCount,
		CaptureLimit: captureLimit, StartedAt: now,
	})
	if err != nil {
		return err
	}
	if started == nil || started.Batch.EndedAt != nil ||
		(started.Batch.Status != store.SourcingBatchPreparing && started.Batch.Status != store.SourcingBatchCollecting) {
		return store.ErrSourcingBatchStateConflict
	}
	return m.store.MutateAccount(key, func(account *store.Account) error {
		return m.enableAccountToday(account, now)
	})
}

// StopSourcing 把当前正式批次写成显式终态，再停止账号调度。它不删除
// 批次或成员；之后再次采集必须创建一个新批次。
func (m *Manager) StopSourcing(key store.AccountKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	batch, err := m.store.ActiveSourcingBatch(key)
	if err != nil {
		return err
	}
	if batch == nil {
		return store.ErrSourcingBatchNotFound
	}
	now := m.now()
	if _, err := m.store.StopSourcingBatch(store.StopSourcingBatchRequest{
		BatchID: batch.BatchID, Reason: PauseUserStopped, StoppedAt: now,
	}); err != nil {
		return err
	}
	return m.pauseAccount(key, PauseUserStopped, now)
}

// InvalidateSourcingFeedsForHand 与账号 actor 的命令启动共用 Manager.mu。
// 管理端重载因此不会在 actor 已拿到旧推荐流授权、尚未开始命令的缝隙中生效。
func (m *Manager) InvalidateSourcingFeedsForHand(handID, trigger string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if at.IsZero() {
		at = m.now()
	}
	return m.store.InvalidateSourcingFeedsForHand(handID, trigger, at)
}

func (m *Manager) enableAccountToday(account *store.Account, now time.Time) error {
	if account.BoundHandID == "" || account.PrincipalFingerprint == nil || *account.PrincipalFingerprint == "" {
		return ErrAccountNotBound
	}
	if account.IdentityState == store.IdentityInvalid || account.IdentityState == store.IdentityUnbound {
		return ErrIdentityInvalid
	}
	account.EnabledDate = m.localDate(now)
	account.EnabledAt = timePointer(now)
	account.StoppedAt = nil
	account.PausedReason = ""
	account.NextPatrolAt = timePointer(now)
	account.DirtyHint = true
	return nil
}

func (m *Manager) StopToday(key store.AccountKey) error {
	return m.stop(key, PauseUserStopped)
}

func (m *Manager) PauseNow(key store.AccountKey) error {
	return m.stop(key, PauseUserRequested)
}

// HoldAfterSourcing keeps the browser actor stopped while the product workflow
// advances through local AI, selection, confirmation and greeting sending.
// It deliberately does not enable IM; communication owns that later boundary.
func (m *Manager) HoldAfterSourcing(key store.AccountKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pauseAccount(key, PauseSourcingTargetReached, m.now())
}

func (m *Manager) stop(key store.AccountKey, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pauseAccount(key, reason, m.now())
}

// RequestImmediate uses the normal scheduling inlet. It remains subject to the
// daily gate, manual quiet window, availability and minimum round gap in Tick.
func (m *Manager) RequestImmediate(key store.AccountKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	return m.store.MutateAccount(key, func(account *store.Account) error {
		account.DirtyHint = true
		m.pullForward(account, now, 0)
		return nil
	})
}

// handleHandLog 收下手侧自身的故障日志。它不查账号、不过 boundHandId 门禁、
// 不改任何账号状态 —— 唯一去处是脑侧日志,以及后续接上的日志上报队列。
// 按契约与 AGENTS.md「日志上报」,手侧不携带任何候选人明文,因此正文可直接进普通日志。
func (m *Manager) handleHandLog(handID string, event protocol.EventBody) error {
	var data protocol.HandLogEventData
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return err
	}
	platform, accountRef := "", ""
	if event.Context != nil {
		platform, accountRef = event.Context.Platform, event.Context.AccountRef
	}
	// 显式标成 hand,否则前台会把插件的故障当成脑自己的。errorCode 走约定键,
	// 直接成为上报事件的分类字段。
	attrs := []any{
		logreport.Source(logreport.SourceHand),
		logreport.CodeKey, data.Code,
		"handId", handID, "handAt", data.At,
		"platform", platform, "accountRef", accountRef,
	}
	if data.Detail != "" {
		attrs = append(attrs, "detail", data.Detail)
	}
	if data.Level == protocol.HandLogLevelWarn {
		slog.Warn("手侧日志: "+data.Message, attrs...)
	} else {
		slog.Error("手侧日志: "+data.Message, attrs...)
	}
	return nil
}

// HandleEvent validates and decodes generated protocol event types. QoS0
// observations are hints only; no event handler invokes Runner.
func (m *Manager) HandleEvent(handID string, event protocol.EventBody) error {
	if err := protocol.ValidateEventData(string(event.Name), event.Data); err != nil {
		return err
	}
	// handLog 必须在账号解析之前分流:手侧故障恰恰常发生在账号未绑定、掉登录或
	// 状态不正常的时候,按账号门禁拒收等于丢掉最该看的那一条。它也不改任何账号状态。
	if event.Name == protocol.EventHandLog {
		return m.handleHandLog(handID, event)
	}
	// context 自 handLog 起在 schema 层是可选的,拦截责任因此落到这里:
	// 除 handLog 外的事件缺 context 一律拒收,否则下面解引用会让脑 panic。
	if event.Context == nil {
		return ErrEventContextMissing
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	key := store.AccountKey{Platform: event.Context.Platform, AccountRef: event.Context.AccountRef}
	account, err := m.store.AccountByKey(key)
	if err != nil {
		return err
	}
	if account == nil {
		return store.ErrAccountNotFound
	}
	if handID == "" || account.BoundHandID != handID {
		_ = m.store.AppendAudit(&store.AuditEntry{
			At: m.now(), Category: "sensor_event_hand_mismatch", HandID: handID,
			Platform: key.Platform, AccountRef: key.AccountRef,
			Detail: "事件未通过 boundHandId 门禁",
		})
		return ErrEventHandMismatch
	}
	now := m.now()
	switch event.Name {
	case protocol.EventPageNavigated:
		var data protocol.PageNavigatedEventData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return err
		}
		return m.store.MutateAccount(key, func(account *store.Account) error {
			account.DirtyHint = true
			m.pullForward(account, now, m.config.CoalesceWindow)
			return nil
		})
	case protocol.EventManualInteraction:
		var data protocol.ManualInteractionEventData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return err
		}
		if data.Kind == protocol.ManualInteractionKindNavigation && data.PageKind == protocol.PageKindRecommend {
			if _, err := m.store.InvalidateSourcingFeed(store.InvalidateSourcingFeedRequest{
				Platform: key.Platform, AccountRef: key.AccountRef,
				Trigger: "recommendPageNavigation", At: now,
			}); err != nil {
				return err
			}
		}
		return m.store.MutateAccount(key, func(account *store.Account) error {
			account.DirtyHint = true
			m.pullForward(account, now, m.config.CoalesceWindow)
			return nil
		})
	case protocol.EventLoginStateChanged:
		var data protocol.LoginStateChangedEventData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return err
		}
		return m.store.MutateAccount(key, func(account *store.Account) error {
			account.DirtyHint = true
			if data.State == protocol.LoginChangeStateIn {
				if account.IdentityState == store.IdentityInvalid {
					// A later in is not authority to repair the prior explicit out or
					// mismatch. Preserve the original manual-review reason as well.
					return nil
				}
				// The first stable sensor sample after injection may be "in"; it is
				// not evidence that a logout/login transition happened and must not
				// destroy a verified binding. It only accelerates reconciliation.
				m.pullForward(account, now, m.config.CoalesceWindow)
				return nil
			}
			// Explicit out invalidates immediately. If a prior out already made
			// the account invalid, a later in keeps it invalid until a person
			// confirms the intended recruiting account again.
			account.IdentityState = store.IdentityInvalid
			account.IdentityReason = fmt.Sprintf("loginStateChanged:%s", data.State)
			account.IdentitySession = ""
			account.IdentityBootID = ""
			account.StoppedAt = timePointer(now)
			account.PausedReason = PauseLoginRequired
			return nil
		})
	default:
		return fmt.Errorf("不支持的传感事件 %q", event.Name)
	}
}


// Tick runs every due online account synchronously. An offline account creates
// no PatrolRound and therefore cannot leave queued business work behind.
func (m *Manager) Tick(ctx context.Context) (TickResult, error) {
	m.tickMu.Lock()
	defer m.tickMu.Unlock()

	now := m.now()
	accounts, err := m.store.Accounts()
	if err != nil {
		return TickResult{}, err
	}
	result := TickResult{}
	for i := range accounts {
		account := accounts[i]
		key := store.AccountKey{Platform: account.Platform, AccountRef: account.AccountRef}

		if account.EnabledDate != "" && account.EnabledDate != m.localDate(now) && account.StoppedAt == nil {
			if err := m.pauseAccount(key, PauseDailyExpired, now); err != nil {
				return result, err
			}
			result.Skipped = append(result.Skipped, AccountSkip{Key: key, Reason: SkipNotEnabled})
			continue
		}
		if !m.enabledToday(account, now) {
			result.Skipped = append(result.Skipped, AccountSkip{Key: key, Reason: SkipNotEnabled})
			continue
		}
		if !m.due(account, now) {
			result.Skipped = append(result.Skipped, AccountSkip{Key: key, Reason: SkipNotDue})
			continue
		}

		hand, handErr := m.hands.State(ctx, account.BoundHandID)
		if handErr != nil {
			result.Skipped = append(result.Skipped, AccountSkip{Key: key, Reason: SkipHandState, Err: handErr})
			continue
		}
		if !hand.Online {
			result.Skipped = append(result.Skipped, AccountSkip{Key: key, Reason: SkipOffline})
			continue
		}

		trigger := TriggerTimer
		if account.DirtyHint {
			trigger = TriggerDirty
		}
		outcome := m.runAccountRound(ctx, &account, hand, trigger, now)
		result.Rounds = append(result.Rounds, outcome)
	}
	return result, nil
}

func (m *Manager) enabledToday(account store.Account, now time.Time) bool {
	return account.EnabledDate == m.localDate(now) && account.EnabledAt != nil &&
		account.StoppedAt == nil && account.PausedReason == ""
}

func (m *Manager) due(account store.Account, now time.Time) bool {
	if account.NextPatrolAt != nil && now.Before(*account.NextPatrolAt) {
		return false
	}
	if account.LastPatrolAt != nil && now.Before(account.LastPatrolAt.Add(m.config.MinimumRoundGap)) {
		return false
	}
	return true
}

func (m *Manager) pullForward(account *store.Account, now time.Time, delay time.Duration) {
	if account == nil || !m.enabledToday(*account, now) {
		return
	}
	target := now.Add(delay)
	if account.LastPatrolAt != nil {
		minimum := account.LastPatrolAt.Add(m.config.MinimumRoundGap)
		if target.Before(minimum) {
			target = minimum
		}
	}
	if account.NextPatrolAt == nil || account.NextPatrolAt.After(target) {
		account.NextPatrolAt = timePointer(target)
	}
}

func (m *Manager) pauseAccount(key store.AccountKey, reason string, now time.Time) error {
	return m.store.MutateAccount(key, func(account *store.Account) error {
		account.StoppedAt = timePointer(now)
		account.PausedReason = reason
		account.DirtyHint = true
		return nil
	})
}

func (m *Manager) now() time.Time { return m.config.Clock.Now() }

func (m *Manager) localDate(at time.Time) string {
	return at.In(m.config.Location).Format("2006-01-02")
}

// stalePlannedBoundary 返回派发遭遇时刻的 Q1/Q2 陈旧判定界(2026-08-02 甲方
// 裁决,《24点边界裁决》第 4 条修订):当前业务日的本地零点(与统一业务运行
// 窗口同一 Location 口径)与本次脑进程启动时刻,两者取较晚者。CreatedAt 早于
// 该界即陈旧;同轮当场创建的动作两个时刻都晚于,正常派发零影响。
func (m *Manager) stalePlannedBoundary() time.Time {
	local := m.now().In(m.config.Location)
	midnight := time.Date(
		local.Year(), local.Month(), local.Day(),
		0, 0, 0, 0,
		m.config.Location,
	)
	if m.startedAt.After(midnight) {
		return m.startedAt
	}
	return midnight
}

// plannedActionStale 只做时刻比较;"从未绑定发送意图"的红线判据由调用方与
// store 侧的 WHERE 条件双重把守。
func (m *Manager) plannedActionStale(createdAt time.Time) bool {
	return createdAt.Before(m.stalePlannedBoundary())
}

func timePointer(value time.Time) *time.Time {
	copy := value
	return &copy
}

func isRunError(err error, code protocol.ErrorCode) bool {
	typed := runError(err)
	return typed != nil && typed.Code == code
}

func wrapRunError(code protocol.ErrorCode, reason protocol.NotReadyReason, cause error) error {
	if cause == nil {
		cause = errors.New(string(code))
	}
	return &RunError{Code: code, Reason: reason, Cause: cause}
}
