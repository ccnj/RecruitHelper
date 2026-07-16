// fakehand:假手——陪跑脑服务的命令行手,吃同一份生成协议(顺便交叉验证契约)。
// 真手是第 3 步的 Chrome 扩展(TS);此工具供 2.2–2.6 联调与验收剧本。
//
// 用法:
//
//	go run ./tools/fakehand -pair              首次配对:发无 token hello,等 welcome{issued},打印工牌
//	go run ./tools/fakehand -hand hand-01 -token <tok>   日常握手,连上后持续心跳
//	go run ./tools/fakehand -hand hand-01 -token <tok> -once   握手成功即退出(验收脚本用)
//
// 关键结果以 EVENT 行输出到 stdout,便于脚本 grep;诊断走 stderr。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

func main() {
	url := flag.String("url", fmt.Sprintf("ws://%s:%d%s", protocol.TransportHost, protocol.DefaultPort, protocol.TransportPath), "脑 WS 地址")
	origin := flag.String("origin", "chrome-extension://fakehandaaaaaaaaaaaaaaaaaaaaaaaa", "扩展 Origin")
	pair := flag.Bool("pair", false, "配对模式(发无 token hello)")
	hand := flag.String("hand", "", "handId(日常握手)")
	token := flag.String("token", "", "令牌(日常握手)")
	boot := flag.String("boot", "", "bootId(默认随机生成)")
	once := flag.Bool("once", false, "收到 welcome/bye 即退出")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	bootID := *boot
	if bootID == "" {
		bootID = ids.NewBootID()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
	defer dialCancel()
	ws, _, err := websocket.Dial(dialCtx, *url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {*origin}},
	})
	if err != nil {
		fmt.Printf("EVENT dial_failed err=%v\n", err)
		os.Exit(1)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")

	h := &fakeHand{ws: ws, bootID: bootID}

	// 组 hello
	var handID, auth *string
	if !*pair {
		if *hand == "" || *token == "" {
			fmt.Println("EVENT usage_error msg=需 -pair 或 -hand+-token")
			os.Exit(2)
		}
		handID, auth = hand, token
	}
	if err := h.sendHello(ctx, handID, auth); err != nil {
		fmt.Printf("EVENT hello_failed err=%v\n", err)
		os.Exit(1)
	}
	slog.Info("已发 hello", "boot", bootID, "pair", *pair)

	// 心跳 ticker(pre-session 也发,保活;Go 手不受 SW 影响,纯为演示协议)
	go h.heartbeat(ctx)

	// 中断信号
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sig; cancel() }()

	// 读循环
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			fmt.Printf("EVENT closed err=%v\n", err)
			return
		}
		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		if h.handle(&env) && *once {
			return
		}
	}
}

type fakeHand struct {
	ws      *websocket.Conn
	bootID  string
	mu      sync.Mutex
	session *string
	writeMu sync.Mutex
}

// handle 处理一帧;返回 true 表示到达可退出的里程碑(welcome/bye)。
func (h *fakeHand) handle(env *protocol.Envelope) bool {
	switch env.Kind {
	case protocol.KindWelcome:
		var wb protocol.WelcomeBody
		_ = json.Unmarshal(env.Body, &wb)
		h.mu.Lock()
		s := wb.Session
		h.session = &s
		h.mu.Unlock()
		if wb.Issued != nil {
			fmt.Printf("EVENT welcome paired=true session=%s handId=%s token=%s\n", wb.Session, wb.Issued.HandID, wb.Issued.Auth)
		} else {
			fmt.Printf("EVENT welcome paired=false session=%s\n", wb.Session)
		}
		return true
	case protocol.KindBye:
		var bb protocol.ByeBody
		_ = json.Unmarshal(env.Body, &bb)
		fmt.Printf("EVENT bye code=%s msg=%s\n", bb.Code, bb.Message)
		return true
	case protocol.KindPong:
		slog.Debug("pong")
		return false
	default:
		slog.Info("收到帧", "kind", env.Kind)
		return false
	}
}

func (h *fakeHand) heartbeat(ctx context.Context) {
	t := time.NewTicker(time.Duration(protocol.DefaultHbIntervalMs) * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.mu.Lock()
			s := h.session
			h.mu.Unlock()
			_ = h.send(ctx, protocol.KindPing, s, protocol.PingBody{QueueDepth: 0})
		}
	}
}

func (h *fakeHand) sendHello(ctx context.Context, handID, auth *string) error {
	var caps []string
	for name, m := range protocol.Primitives {
		if m.Batch == protocol.BatchM1 && len(name) > 6 && name[:6] == "debug." {
			caps = append(caps, fmt.Sprintf("%s@%d", name, m.Ver))
		}
	}
	return h.send(ctx, protocol.KindHello, nil, protocol.HelloBody{
		HandID: handID, Auth: auth, BootID: h.bootID,
		ProtoSupported: []int{protocol.ProtoVersion},
		ContractHash:   protocol.ContractHash,
		App:            protocol.AppInfo{ExtVersion: "0.1.0", Browser: "fakehand"},
		Caps:           caps,
		Features:       []string{},
	})
}

func (h *fakeHand) send(ctx context.Context, kind protocol.Kind, session *string, body any) error {
	if body == nil {
		return nil
	}
	raw, err := protocol.Encode(body)
	if err != nil {
		return err
	}
	env := protocol.Envelope{
		Proto: protocol.ProtoVersion, Kind: kind, MsgID: ids.NewMsgID(),
		Session: session, Ts: time.Now().UnixMilli(), Attempt: 1, Body: raw,
	}
	buf, err := json.Marshal(env)
	if err != nil {
		return err
	}
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return h.ws.Write(wctx, websocket.MessageText, buf)
}
