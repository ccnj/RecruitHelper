// 脑服务(braind):招聘自动化客户端的逻辑中枢。
// 2.1 骨架:配置、存储、日志、优雅退出;WS 会话层自 2.2 接入。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"recruithelper/client/service/internal/adminhttp"
	"recruithelper/client/service/internal/aitrace"
	"recruithelper/client/service/internal/appbridge"
	"recruithelper/client/service/internal/apphttp"
	"recruithelper/client/service/internal/blobstore"
	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/handreload"
	"recruithelper/client/service/internal/jobconfig"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/notify"
	"recruithelper/client/service/internal/patrol"
	"recruithelper/client/service/internal/productapp"
	"recruithelper/client/service/internal/report"
	"recruithelper/client/service/internal/productworkflow"
	"recruithelper/client/service/internal/selfupdate"
	"recruithelper/client/service/internal/session"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/workflow"
	"recruithelper/contract/gen/go/protocol"
)

func main() {
	port := flag.Int("port", protocol.DefaultPort, "WS 监听端口")
	dataDir := flag.String("data", "data", "数据目录(SQLite 落盘处)")
	adminToken := flag.String("admin-token", os.Getenv("RECRUITHELPER_ADMIN_TOKEN"), "本地管理面 bearer token")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))
	dailyWindow := workflow.DailyWindowPolicy{
		AllowOutOfWindow: workflow.ParseDevelopmentAllowOutOfWindow(
			os.Getenv(workflow.DevelopmentAllowOutOfWindowEnv),
		),
	}
	if dailyWindow.AllowOutOfWindow {
		slog.Warn("开发期业务窗口覆盖已启用")
	}
	inboundHandoverCutoff, handoverErr := patrol.ParseInboundHandoverCutoff(
		os.Getenv(patrol.InboundHandoverDateEnv), time.Local,
	)
	if handoverErr != nil {
		// 非法日期拒绝启动：静默回落会放进一批本应挡住的交接前旧会话。
		slog.Error("交接日配置无效，脑服务拒绝启动", "err", handoverErr)
		os.Exit(1)
	}
	slog.Info("交接日入站建档闸已生效",
		"handoverDate", inboundHandoverCutoff.Format("2006-01-02"))

	st, err := store.Open(*dataDir)
	if err != nil {
		slog.Error("存储初始化失败", "err", err)
		os.Exit(1)
	}
	defer func() {
		if err := st.Close(); err != nil {
			slog.Error("关闭存储失败", "err", err)
		}
	}()
	var traceRecorder m5ai.TraceRecorder
	traceStore, traceErr := aitrace.Open(*dataDir)
	if traceErr != nil {
		slog.Warn("AI 原文追踪库不可用，业务调用将继续", "errorCode", "traceStoreUnavailable")
	} else {
		traceRecorder = traceStore
		defer func() {
			if err := traceStore.Close(); err != nil {
				slog.Warn("关闭 AI 原文追踪库失败", "errorCode", "traceStoreCloseFailed")
			}
		}()
	}
	providerConfig, err := m5ai.NewProviderConfigStore(*dataDir)
	if err != nil {
		slog.Error("本地模型配置初始化失败", "err", err)
		os.Exit(1)
	}
	jobConfigStore, err := jobconfig.NewConfigStore(*dataDir)
	if err != nil {
		slog.Error("旧后台职位配置源初始化失败", "err", err)
		os.Exit(1)
	}
	jobConfigSource := jobconfig.NewSource(jobConfigStore, nil)
	var advice patrol.AdviceExecutor
	if configured, loadErr := providerConfig.Load(); loadErr != nil {
		slog.Warn("本地模型配置不可用，M5 建议层保持停用", "err", loadErr)
	} else if configured != nil {
		provider, providerErr := m5ai.NewOpenAICompatibleProvider(*configured, nil, traceRecorder)
		if providerErr != nil {
			slog.Warn("本地模型配置未能激活，M5 建议层保持停用", "err", providerErr)
		} else {
			advice = provider
			slog.Info("M5 建议层已就绪", "provider", provider.ProviderName(), "model", provider.ModelName())
		}
	}

	mode, _ := st.JournalMode()
	slog.Info("脑服务存储就绪",
		"data", *dataDir,
		"journalMode", mode,
		"port", *port,
		"proto", protocol.ProtoVersion,
		"contract", protocol.ContractHash,
	)

	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	hub := session.NewHub(st, protocol.DefaultHbGraceMs)
	blobStore, err := blobstore.New(filepath.Join(*dataDir, "blobs"))
	if err != nil {
		slog.Error("blob 存储初始化失败", "err", err)
		os.Exit(1)
	}
	blobTokens := blobstore.NewTokenRegistry()
	hub.SetBlob(
		blobTokens,
		fmt.Sprintf("http://127.0.0.1:%d/v1/blobs", *port),
		protocol.DefaultPayloadBlobMaxBytes,
	)
	disp := dispatch.New(st, hub)
	hub.SetDispatcher(disp)
	disp.SetEffectVerifier(appbridge.EffectVerifier{Dispatcher: disp})
	disp.Recover() // 脑重启扫描:任何 WS 服务开始前,把在途命令收编(readonly/intrusive 作废,effectful suspect)
	// 两段启动收束都是记账性善后,失败不再拒绝启动(2026-08-02 甲方裁决,停机点
	// 体检战役:一次瞬态读库错误不该让脑起不来)。残留的 running 轮行只是审计
	// 事实,不阻塞新轮;未收束的 AI 调用行由巡检的 attempt 游走在运行期逐个
	// 收敛。失败响亮留痕,下次启动自然重试。
	if recovered, recoverErr := st.RecoverRunningPatrolRounds(time.Now()); recoverErr != nil {
		slog.Error("收束上次中断巡检轮失败,继续启动,下次启动重试", "err", recoverErr)
	} else if recovered > 0 {
		slog.Warn("已收束上次中断的巡检轮", "count", recovered)
	}
	if recovered, recoverErr := st.RecoverInterruptedAIInvocations(time.Now()); recoverErr != nil {
		slog.Error("收束上次中断的 AI 调用失败,继续启动,下次启动重试", "err", recoverErr)
	} else if recovered > 0 {
		slog.Warn("已收束上次中断的 AI 调用", "count", recovered)
	}
	runner := &appbridge.PatrolRunner{Dispatcher: disp}
	var actor *patrol.Manager
	patrolConfig := patrol.Config{
		DailyWindow:           dailyWindow,
		InboundHandoverCutoff: inboundHandoverCutoff,
	}
	if advice == nil {
		actor, err = patrol.NewManager(
			st, runner, appbridge.HandAvailability{Hub: hub}, patrolConfig,
		)
	} else {
		actor, err = patrol.NewManager(
			st, runner, appbridge.HandAvailability{Hub: hub}, patrolConfig, advice,
		)
	}
	if err != nil {
		slog.Error("账号 actor 初始化失败", "err", err)
		os.Exit(1)
	}
	productWorkflow, err := productworkflow.NewManager(
		st, actor, productworkflow.Config{
			Clock: wallClock{}, Location: time.Local, DailyWindow: dailyWindow,
		},
	)
	if err != nil {
		slog.Error("产品工作流初始化失败", "err", err)
		os.Exit(1)
	}
	productController, err := productapp.New(
		st, productWorkflow, jobConfigSource, time.Now, dailyWindow, providerConfig,
	)
	if err != nil {
		slog.Error("产品工作流控制面初始化失败", "err", err)
		os.Exit(1)
	}
	// 账号跟随登录:产品页"开始"探测当前 Chrome 登录主体并找回/建档账本根,
	// 绑定经 actor 与命令派发线性化。
	productController.SetAccountResolver(appbridge.LoginAccountResolver{
		Hub: hub, Prober: runner, Binder: actor, Now: time.Now,
	})

	// QoS0 事件绝不阻塞 WS 读循环；队列满时响亮留痕后丢提示，周期对账仍是真相源。
	events := make(chan session.SensorEvent, 128)
	hub.SetEventSink(session.EventSinkFunc(func(event session.SensorEvent) {
		select {
		case events <- event:
		default:
			_ = st.AppendAudit(&store.AuditEntry{
				At: time.Now(), Category: "sensor_event_queue_full", HandID: event.HandID,
				RefMsgID: event.MsgID, Platform: event.Body.Context.Platform,
				AccountRef: event.Body.Context.AccountRef, Detail: "QoS0 事件队列已满，提示被丢弃",
			})
		}
	}))
	var background backgroundGroup
	background.Go(func() { consumeSensorEvents(appCtx, st, actor, events) })
	background.Go(func() { runPatrolLoop(appCtx, actor) })
	// 按"错误码 + 时间窗"节流，不按错误码永久去重。旧写法只在错误码变化时
	// 打印一次：2026-08-05 那次证词库熔断后，同一个 workflowAdvanceFailed
	// 每秒都在发生，日志里却只有开头一行，31 分钟的停摆看上去风平浪静。
	// 现场是靠"怎么半天没动静"察觉的，不是靠告警。
	const workflowWarnInterval = 2 * time.Minute
	lastWorkflowErrorCode := ""
	lastWorkflowWarnAt := time.Time{}
	workflowWarnSuppressed := 0
	background.Go(func() {
		productWorkflow.Run(appCtx, time.Second, func(runErr error) {
			code := productWorkflowErrorCode(runErr)
			now := time.Now()
			if code == lastWorkflowErrorCode && now.Sub(lastWorkflowWarnAt) < workflowWarnInterval {
				workflowWarnSuppressed++
				return
			}
			if code == lastWorkflowErrorCode {
				slog.Warn("产品工作流推进仍在暂停", "errorCode", code,
					"suppressed", workflowWarnSuppressed, "since", lastWorkflowWarnAt.Format(time.RFC3339))
			} else {
				slog.Warn("产品工作流推进暂停", "errorCode", code)
			}
			lastWorkflowErrorCode = code
			lastWorkflowWarnAt = now
			workflowWarnSuppressed = 0
		})
	})
	background.Go(func() { hub.StartHealthLoop(appCtx) })
	background.Go(func() { disp.RunFaultLoop(appCtx) })
	// 客户端换代后,pluginSeed 已把新插件写进固定目录,但 Chrome 里跑的还是旧代码。
	// 改了契约的版本会让 effectful 被禁派——安全,可业务静止;没改契约的版本更隐蔽,
	// 业务带着旧插件代码照常跑。两种都得认出来,现场也没人知道要去
	// chrome://extensions 点一次刷新。这个循环替人点那一下,判据与诊断台按钮完全
	// 相同(handreload 包共用同一条路径),另加"没有活跃产品工作流"一条。
	pluginReloader := handreload.NewAutoReloader(
		&handreload.Orchestrator{
			Store: st, Registry: hub.Registry(), Dispatcher: disp, Feeds: actor,
			Trigger: handreload.TriggerAuto, PluginDir: os.Getenv(handreload.PluginDirEnv),
		},
		st, handreload.DefaultInterval,
	)
	// 手一 ready 就评估,不必干等下一个 tick(真机首验里那 30 秒等待正是这么来的)。
	// ticker 仍在,负责兜住提醒被合并或丢弃的场合。
	hub.SetHandReadyHook(pluginReloader.NotifyHandReady)
	background.Go(func() { pluginReloader.Run(appCtx) })
	// 客户端版本更新源(AGENTS.md 全局约定第四项获准云端出站)。这里只负责"发现":
	// 取清单、下载、校验完整性,到此为止 —— 装不装、什么时候装是下一批的事。
	// 配置不全(开发期不传那两个环境变量)时 New 返回 nil,整个检查不启用。
	updateChecker := selfupdate.New(
		selfupdate.DefaultFeedURL,
		os.Getenv(selfupdate.AppVersionEnv),
		os.Getenv(selfupdate.UpdateDirEnv),
		selfupdate.DefaultInterval,
	)
	var updateInstaller *selfupdate.InstallGate
	if updateChecker != nil {
		background.Go(func() { updateChecker.Run(appCtx) })
		// 上一次交出安装器之后到底装成没有,只能靠"现在跑着的是哪一版"来判断。
		// 读不动那张字条不拦启动 —— 它只是诊断与防循环的依据。
		if _, confirmErr := selfupdate.ConfirmPendingInstall(
			updateChecker.DownloadDir, updateChecker.CurrentVersion,
		); confirmErr != nil {
			slog.Warn("核对上次自动安装结果失败", "err", confirmErr)
		}
		updateInstaller = &selfupdate.InstallGate{
			Store: st, Workflow: productController, Checker: updateChecker,
			StateDir: updateChecker.DownloadDir,
		}
	}
	// 未启用自更新时不要把一个 nil 的具体指针塞进 Option:它会变成"非 nil 的
	// 接口值",让 apphttp 里的 nil 检查失效。
	var updateInstallerOption apphttp.Option
	if updateInstaller != nil {
		updateInstallerOption = apphttp.WithUpdateInstaller(updateInstaller)
	}
	// 运营通知发件箱轮询(AGENTS.md 2026-07-28 裁决):非候选人可见动作,
	// 不受业务运行窗口约束;失败只降级不阻塞业务主线。
	notifyRunner := notify.NewRunner(st, blobStore, func() string {
		view, statusErr := jobConfigSource.Status(context.Background())
		if statusErr != nil {
			return ""
		}
		return view.CustomerName
	})
	background.Go(func() { notifyRunner.Run(appCtx) })
	mux := http.NewServeMux()
	mux.HandleFunc(protocol.TransportPath, hub.ServeWS)
	blobstore.NewHandler(blobStore, blobTokens, protocol.DefaultPayloadBlobMaxBytes).Routes(mux)
	// 现场数据上报(2026-07-31 甲方裁决)。日志由 Electron 写在 userData/logs,
	// 不在脑的数据目录下,所以路径经环境变量传进来 —— 与 PLUGIN_DIR/UPDATE_DIR
	// 同一套做法。开发期直接跑脑时没有这个变量,那时包里就没有日志。
	fieldReportDeps := adminhttp.FieldReportDeps{
		DataDir:    *dataDir,
		LogDir:     strings.TrimSpace(os.Getenv("RECRUITHELPER_LOG_DIR")),
		AppVersion: strings.TrimSpace(os.Getenv("RECRUITHELPER_APP_VERSION")),
	}
	if traceErr == nil {
		fieldReportDeps.TraceSnapshot = traceStore.SnapshotTo
	}

	adminAPI := adminhttp.New(st, hub, disp, actor, runner, *adminToken, providerConfig).
		SetJobConfigSource(jobConfigSource).
		SetFieldReportDeps(fieldReportDeps)

	// 每日自动上传(2026-07-31 补充裁决)。开关默认关闭且每轮重读——这个 goroutine
	// 常驻,但只要没人在诊断台打开开关，它每天到点看一眼就继续睡。
	go report.RunScheduler(appCtx, report.SchedulerDeps{
		Enabled: func() (bool, error) {
			setting, err := st.FieldReportSetting()
			return setting.AutoUploadEnabled, err
		},
		// 静默判定与自动更新、插件重载用同一套账本切面:有活跃工作流或未收束
		// 命令时不动库——快照要抢 SetMaxOpenConns(1) 那唯一的写连接。
		Quiet: func() (bool, string) {
			if run, err := st.ActiveProductWorkflowRun(); err != nil {
				return false, "读取活跃工作流失败"
			} else if run != nil {
				return false, "有活跃工作流"
			}
			pending, err := st.NonTerminalCmds()
			if err != nil {
				return false, "读取未收束命令失败"
			}
			if len(pending) > 0 {
				return false, fmt.Sprintf("仍有 %d 条未收束命令", len(pending))
			}
			return true, ""
		},
		RunOnce: adminAPI.RunFieldReportOnce,
		Record: func(at time.Time, ok bool, reason string) {
			if err := st.RecordFieldReportAutoRun(at, ok, reason); err != nil {
				slog.Warn("现场上报:记录自动上传结果失败", "errorCode", "fieldReportRecordFailed")
			}
		},
	})
	// 职位类别在后台配置值精确匹配不上时改由大模型从平台候选里选,复用同一条
	// provider 通道。未配置 provider 时只是这条兜底不可用,精确匹配照旧工作。
	if advice != nil {
		adminAPI = adminAPI.SetAdvice(advice)
	}
	adminAPI.Routes(mux)
	if *adminToken != "" {
		productAPI, productErr := apphttp.New(
			st,
			*adminToken,
			apphttp.WithRuntimeSnapshotProvider(func(ctx context.Context) (apphttp.RuntimeSnapshot, error) {
				view, statusErr := jobConfigSource.Status(ctx)
				if statusErr != nil {
					return apphttp.RuntimeSnapshot{}, statusErr
				}
				snapshot := apphttp.RuntimeSnapshot{
					Available:      true,
					CustomerName:   view.CustomerName,
					CustomerStatus: view.CustomerStatus,
					Authorized:     view.Configured && view.MachineIdentityReady && view.MachineMatch,
				}
				windowOpen, windowErr := dailyWindow.Evaluate(time.Now(), time.Local)
				if windowErr != nil {
					return apphttp.RuntimeSnapshot{}, windowErr
				}
				snapshot.BusinessWindowOpen = windowOpen
				if configured, loadErr := providerConfig.Load(); loadErr == nil && configured != nil {
					snapshot.ProviderConfigured = true
					snapshot.Provider = configured.Provider
					snapshot.Model = configured.Model
				}
				snapshot.PluginOnline, snapshot.PluginHealth,
					snapshot.PluginVersion, snapshot.ContractMatch =
					productPluginRuntime(hub.Registry().Snapshot())
				productState, stateErr := productController.RuntimeState()
				if stateErr != nil {
					return apphttp.RuntimeSnapshot{}, stateErr
				}
				snapshot.Platform = productState.Platform
				snapshot.AccountRef = productState.AccountRef
				snapshot.CurrentBatchID = productState.CurrentBatchID
				snapshot.WorkflowMode = productState.WorkflowMode
				snapshot.WorkflowStatus = productState.WorkflowStatus
				snapshot.WorkflowStage = productState.WorkflowStage
				snapshot.WorkflowPendingAction = productState.WorkflowPendingAction
				snapshot.CanAddBatch = productState.CanAddBatch
				snapshot.CanEnd = productState.CanEnd
				snapshot.CommunicationState = productState.CommunicationState
				return snapshot, nil
			}),
			apphttp.WithWorkflowControl(productController),
			updateInstallerOption,
			apphttp.WithUpdateStatusProvider(func() apphttp.UpdateStatus {
				status := updateChecker.Status()
				return apphttp.UpdateStatus{
					CurrentVersion: status.CurrentVersion, Available: status.Available,
					Version: status.Version, Ready: status.Ready, Notes: status.Notes,
				}
			}),
		)
		if productErr != nil {
			slog.Error("产品 UI 投影初始化失败", "err", productErr)
			os.Exit(1)
		}
		productAPI.Routes(mux)
	}

	srv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", *port), Handler: mux}
	go func() {
		slog.Info("HTTP/WS 监听", "addr", srv.Addr, "ws", protocol.TransportPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("监听失败", "err", err)
			os.Exit(1)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	s := <-sig
	slog.Info("收到信号,优雅退出", "signal", s.String())
	// 先阻止 actor、事件消费、健康扫描与故障扫描继续生产工作，再关闭
	// HTTP/WS 入口。后台循环必须在存储关闭前显式收束。
	appCancel()

	httpCtx, httpCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := srv.Shutdown(httpCtx); err != nil {
		slog.Warn("HTTP 关闭超时", "err", err)
	}
	httpCancel()

	backgroundCtx, backgroundCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := background.Wait(backgroundCtx); err != nil {
		slog.Warn("后台循环收束超时", "err", err)
	}
	backgroundCancel()
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

func productWorkflowErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, productworkflow.ErrPipelineActorUnavailable):
		return "pipelineActorUnavailable"
	case errors.Is(err, productworkflow.ErrWorkflowPipelineInvalid):
		return "workflowPipelineInvalid"
	case errors.Is(err, patrol.ErrSourcingScoringProviderUnavailable):
		return "scoringProviderUnavailable"
	case errors.Is(err, patrol.ErrSourcingGreetingProviderUnavailable):
		return "greetingProviderUnavailable"
	default:
		return "workflowAdvanceFailed"
	}
}

// productPluginRuntime 把手注册表收窄成普通用户配置页所需的四项状态。
// 它有意不返回 handId、bootId、contractHash、caps 或协商细节。
func productPluginRuntime(states []session.HandState) (online bool, health, version string, contractMatch bool) {
	selected := -1
	for i := range states {
		if selected < 0 ||
			(states[i].Online && !states[selected].Online) ||
			(states[i].Online == states[selected].Online &&
				states[i].SessionAt.After(states[selected].SessionAt)) {
			selected = i
		}
	}
	if selected < 0 {
		return false, string(session.HealthOffline), "", false
	}
	current := states[selected]
	return current.Online && current.Health == session.HealthReady,
		string(current.Health), current.ExtVersion, current.ContractMatch
}

// backgroundGroup 只管理进程级长生命周期循环。所有 Go 调用必须先于 Wait；
// Wait 自带调用方提供的界限，避免某个错误循环把进程退出永久卡住。
type backgroundGroup struct {
	wg sync.WaitGroup
}

func (g *backgroundGroup) Go(run func()) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		run()
	}()
}

func (g *backgroundGroup) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func consumeSensorEvents(ctx context.Context, st *store.Store, actor *patrol.Manager, events <-chan session.SensorEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-events:
			if err := actor.HandleEvent(event.HandID, event.Body); err != nil {
				_ = st.AppendAudit(&store.AuditEntry{
					At: time.Now(), Category: "sensor_event_rejected", HandID: event.HandID,
					RefMsgID: event.MsgID, Platform: event.Body.Context.Platform,
					AccountRef: event.Body.Context.AccountRef, Detail: err.Error(),
				})
				slog.Warn("传感事件未被账号 actor 接受", "handId", event.HandID, "name", event.Body.Name, "err", err)
			}
		}
	}
}

func runPatrolLoop(ctx context.Context, actor *patrol.Manager) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			result, err := actor.Tick(ctx)
			if err != nil {
				slog.Error("账号巡检调度失败", "err", err)
				continue
			}
			for _, round := range result.Rounds {
				if round.Err != nil {
					slog.Warn("账号巡检轮失败", "platform", round.Key.Platform, "accountRef", round.Key.AccountRef,
						"roundId", round.RoundID, "trigger", round.Trigger, "err", round.Err)
					continue
				}
				slog.Info("账号巡检轮完成", "platform", round.Key.Platform, "accountRef", round.Key.AccountRef,
					"roundId", round.RoundID, "trigger", round.Trigger, "projections", len(round.Projections))
			}
		}
	}
}
