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
	"sync"
	"syscall"
	"time"

	"recruithelper/client/service/internal/adminhttp"
	"recruithelper/client/service/internal/aitrace"
	"recruithelper/client/service/internal/appbridge"
	"recruithelper/client/service/internal/apphttp"
	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/jobconfig"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/patrol"
	"recruithelper/client/service/internal/session"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

func main() {
	port := flag.Int("port", protocol.DefaultPort, "WS 监听端口")
	dataDir := flag.String("data", "data", "数据目录(SQLite 落盘处)")
	adminToken := flag.String("admin-token", os.Getenv("RECRUITHELPER_ADMIN_TOKEN"), "本地管理面 bearer token")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))

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
	disp := dispatch.New(st, hub)
	hub.SetDispatcher(disp)
	disp.SetEffectVerifier(appbridge.EffectVerifier{Dispatcher: disp})
	disp.Recover() // 脑重启扫描:任何 WS 服务开始前,把在途命令收编(readonly/intrusive 作废,effectful suspect)
	if recovered, recoverErr := st.RecoverRunningPatrolRounds(time.Now()); recoverErr != nil {
		slog.Error("收束上次中断巡检轮失败", "err", recoverErr)
		os.Exit(1)
	} else if recovered > 0 {
		slog.Warn("已收束上次中断的巡检轮", "count", recovered)
	}
	if recovered, recoverErr := st.RecoverInterruptedAIInvocations(time.Now()); recoverErr != nil {
		slog.Error("收束上次中断的 AI 调用失败", "err", recoverErr)
		os.Exit(1)
	} else if recovered > 0 {
		slog.Warn("已收束上次中断的 AI 调用", "count", recovered)
	}
	runner := &appbridge.PatrolRunner{Dispatcher: disp}
	var actor *patrol.Manager
	if advice == nil {
		actor, err = patrol.NewManager(st, runner, appbridge.HandAvailability{Hub: hub}, patrol.Config{})
	} else {
		actor, err = patrol.NewManager(st, runner, appbridge.HandAvailability{Hub: hub}, patrol.Config{}, advice)
	}
	if err != nil {
		slog.Error("账号 actor 初始化失败", "err", err)
		os.Exit(1)
	}

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
	background.Go(func() { hub.StartHealthLoop(appCtx) })
	background.Go(func() { disp.RunFaultLoop(appCtx) })
	mux := http.NewServeMux()
	mux.HandleFunc(protocol.TransportPath, hub.ServeWS)
	adminhttp.New(st, hub, disp, actor, runner, *adminToken, providerConfig).
		SetJobConfigSource(jobConfigSource).Routes(mux)
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
				if configured, loadErr := providerConfig.Load(); loadErr == nil && configured != nil {
					snapshot.ProviderConfigured = true
					snapshot.Provider = configured.Provider
					snapshot.Model = configured.Model
				}
				snapshot.PluginOnline, snapshot.PluginHealth,
					snapshot.PluginVersion, snapshot.ContractMatch =
					productPluginRuntime(hub.Registry().Snapshot())
				return snapshot, nil
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
