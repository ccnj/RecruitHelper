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
	"syscall"
	"time"

	"recruithelper/client/service/internal/adminhttp"
	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/pairing"
	"recruithelper/client/service/internal/session"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

func main() {
	port := flag.Int("port", protocol.DefaultPort, "WS 监听端口")
	dataDir := flag.String("data", "data", "数据目录(SQLite 落盘处)")
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

	pm := pairing.New(st)
	hub := session.NewHub(st, pm, protocol.DefaultHbGraceMs)
	disp := dispatch.New(st, hub)
	hub.SetDispatcher(disp)
	disp.Recover() // 脑重启扫描:任何 WS 服务开始前,把在途命令收编(readonly/intrusive 作废,effectful suspect)
	go hub.StartHealthLoop(appCtx)
	go disp.RunFaultLoop(appCtx)
	mux := http.NewServeMux()
	mux.HandleFunc(protocol.TransportPath, hub.ServeWS)
	adminhttp.New(st, pm, hub, disp).Routes(mux)

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

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		slog.Warn("HTTP 关闭超时", "err", err)
	}
}
