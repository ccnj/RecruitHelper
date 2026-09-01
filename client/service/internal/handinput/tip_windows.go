//go:build windows

package handinput

// 与自研 TSF 输入法(TIP)之间的命名管道。搬自上游 hiBoss
// `lab/engine/inject/tip_windows.go`,2026-08-23 Windows 11 / Chrome 真机验过
// (9 字计划 6 个词,6/6 全部上屏,文案一字不差)。
//
// # 分工:注入器发真实按键,TIP 决定上屏什么词
//
// 两者缺一不可。不发按键,网页看到「有文字没按键」,BOSS 的 keyboardAbnormal 命中;
// 而借来的微软拼音自己决定出哪个词(上游实测 36 个 IME 段错 3 个,且同一个 liaoliao
// 有时出「了了」有时出「聊聊」——候选词错误不是拼音的纯函数,规则式规避不成立),
// 所以上屏内容必须由我们指定。
//
// # 为什么我方是服务端
//
// 反过来(TIP 当服务端)会撞上:TSF 把 TIP 的 DLL 加载进**每一个**用输入法的进程
// (上游真机同时有 explorer / chrome / notepad×4 / conhost),每个实例都去建管道
// 就要处理「谁先抢到」和焦点转移的竞态。由我方建,生命周期就归我方管,
// 而且**「有客户端连上来」本身就是就绪握手**。
//
// # 谁该收词表,由这一侧按 pid 决定,不由 DLL 自己猜
//
// 上游先前的设计是让 DLL 判断自己有没有焦点、只有有焦点的才连管道。那条路走死了:
// 它要求 DLL 能可靠知道自己有没有焦点,而 `ITfKeyEventSink::OnSetFocus` 并不是
// 那个信号(它在 TSF 激活时触发,不在应用之间切焦点时触发;真机实测 chrome.exe
// 全程只收到过一次,却在 70 秒后又开始收键)。
//
// 现在:TIP 只在 chrome.exe 里连、连上就一直连着;本侧注入前查一下前台窗口属于
// 哪个 pid,按 pid 挑连接。「现在要往哪个窗口打字」本来就是注入器自己决定的。
//
// # 管道名不能改
//
// `\\.\pipe\hiboss-tip` 在 TIP 的 Rust 侧是写死的常量(`tip/src/drive.rs` 的
// `PIPE_PATH`)。改名字要同批重新编译并注册 DLL,而 DLL 是上游的成品。
// 名字里的 hiboss 是历史,不是我们该顺手"改成 recruithelper"的东西。
//
// # 管道建一次就不能关,这是被真机打出来的
//
// 2026-09-01 Windows 首验:第一条命令全绿(7/7 上屏、回读逐字相同),**第二条立刻
// 失败,整条 6ms、一个键都没发**。原因是这一版照搬了上游 `play.go` 的形状——
// 那是个一次性 CLI,每跑一次建一次管道、跑完关掉,在那里"每次"等于"每进程"。
// 我们的脑是常驻的,于是变成反复建反复关,而:
//
//   - go-winio 建监听用 `FILE_CREATE`,**同名管道只要还有一个实例存在就建不出来**
//   - 关监听只关掉监听句柄,**已接受的连接还开着**
//   - 而 TIP 的设计正是「连上就一直连着」
//
// 三条撞在一起:第一条命令跑完,TIP 那两条连接仍在 → 实例仍在 → 第二条命令
// 建管道当场失败。所以监听器归注入器持有、进程级只建一次;每条命令只做
// 「清零 → 发词表 → 收 COMMIT → 发 CLEAR」,**不碰监听器**。
//
// 协议见 `tip/src/drive.rs` 的模块注释:行式文本、UTF-8、\t 分隔。

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
)

// user32 已在 inject_windows.go 里声明(SendInput 用的同一个 DLL),复用它。
var (
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
)

const tipPipe = `\\.\pipe\hiboss-tip`

// tipServer 接受 TIP 的连接,并把「这一轮上屏哪些词」发过去。
type tipServer struct {
	ln      io.Closer
	mu      sync.Mutex
	conns   map[uint32]*tipConn // pid → 连接
	logs    []string
	commits atomic.Int32 // 收到多少条 COMMIT —— 用来跟计划里的词数对账
	done    atomic.Bool  // 收到过 DONE
}

type tipConn struct {
	c    io.ReadWriteCloser
	host string
	pid  uint32
}

// listenTip 起管道服务。
func listenTip() (*tipServer, error) {
	ln, err := winio.ListenPipe(tipPipe, &winio.PipeConfig{
		// 上游原样搬来的安全描述符。**它给的是 Everyone 全权(WD = World)**,
		// 不是"当前用户"——上游注释那句话与代码不符,这里如实记下。
		//
		// 没有收窄是因为收窄这件事在 mac 上验不了,而搬错一个 SD 的症状是
		// 「TIP 连不上」,与「输入法没切」「Chrome 启动太早」三者在现场分不开,
		// 正好毁掉一次跑到底的那一趟。
		//
		// 现实风险有界:本机单用户;能连上来的第三方最多冒充 pid 抢走词表
		// (导致上屏错词,方向是"打错"不是"泄露"),或伪造 COMMIT 污染对账。
		// 词表内容就是我方马上要打在屏幕上的那句话,不含额外秘密。
		// 真要收窄,要在 Windows 上改完当场验。
		SecurityDescriptor: "D:P(A;;GA;;;WD)",
		MessageMode:        false,
		InputBufferSize:    64 * 1024,
		OutputBufferSize:   64 * 1024,
	})
	if err != nil {
		return nil, err
	}
	s := &tipServer{ln: ln, conns: map[uint32]*tipConn{}}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(c)
		}
	}()
	return s, nil
}

func (s *tipServer) handle(c io.ReadWriteCloser) {
	defer c.Close()
	sc := bufio.NewScanner(c)
	for sc.Scan() {
		f := strings.Split(strings.TrimRight(sc.Text(), "\r"), "\t")
		switch f[0] {
		case "HELLO":
			tc := &tipConn{c: c}
			if len(f) > 2 {
				tc.host = f[1]
				if n, err := strconv.ParseUint(f[2], 10, 32); err == nil {
					tc.pid = uint32(n)
				}
			}
			s.mu.Lock()
			s.conns[tc.pid] = tc
			n := len(s.conns)
			s.mu.Unlock()
			s.log(fmt.Sprintf("TIP 已连上:%s pid=%d(当前 %d 条)", tc.host, tc.pid, n))
		case "RPT":
			// 回报连接。TIP 对同一个管道开两条连接,各走一个方向——一条同步句柄
			// 不能同时读写(内核会把同一 file object 上的 I/O 串行化,写要等挂起的
			// 读返回)。这一条只送 COMMIT/DONE,**不进 conns**:命令永远走 HELLO 那条。
			if len(f) > 2 {
				s.log(fmt.Sprintf("回报通道就绪:%s pid=%s", f[1], f[2]))
			}
		case "COMMIT":
			if len(f) > 2 {
				s.commits.Add(1)
				s.log(fmt.Sprintf("上屏 #%s %q", f[1], f[2]))
			}
		case "DONE":
			s.done.Store(true)
			s.log("词表已用完")
		default:
			if f[0] != "" {
				s.log("未知回报: " + sc.Text())
			}
		}
	}
	s.mu.Lock()
	for pid, tc := range s.conns {
		if tc.c == c {
			s.log(fmt.Sprintf("TIP 断开:%s pid=%d", tc.host, pid))
			delete(s.conns, pid)
		}
	}
	s.mu.Unlock()
}

// log 攒一条流水。上游是打到 stdout 的,我方是常驻服务,改成环形缓冲——
// 它是「TIP 那半到底发生了什么」的唯一现场,失败时随错误一起带出去。
func (s *tipServer) log(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	const keep = 64
	s.logs = append(s.logs, msg)
	if len(s.logs) > keep {
		s.logs = s.logs[len(s.logs)-keep:]
	}
}

// recent 取最近若干条流水,拼成一行。
func (s *tipServer) recent(n int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	from := len(s.logs) - n
	if from < 0 {
		from = 0
	}
	return strings.Join(s.logs[from:], " / ")
}

// foregroundPid 返回前台窗口所属进程的 pid。
//
// **这就是「该跟哪个 TIP 说话」的答案。** SendInput 的按键只会落在前台窗口,
// 所以词表必须送到那个进程的 TIP 手里。这件事注入器自己就知道,
// 不需要让六七个 DLL 实例各自去猜。
func foregroundPid() (uint32, error) {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return 0, fmt.Errorf("没有前台窗口")
	}
	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return 0, fmt.Errorf("拿不到前台窗口的 pid")
	}
	return pid, nil
}

// sendWords 把词表发给该收的那个 TIP。
//
// 挑法有两级:
//
//  1. **前台窗口所属进程**优先 —— SendInput 的按键只会落在前台窗口,所以这才是正解。
//  2. 若前台进程里没有 TIP,但**总共只连着一条**,就用那一条。TIP 只在 chrome.exe
//     里连管道,所以「只有一条」等价于「只有 Chrome」,不存在发错的可能。
//
// 连着多条却又不在前台 —— 那才是真有歧义,直接报错、不猜。
func (s *tipServer) sendWords(words []PlanWord) error {
	fields := make([]string, 0, len(words))
	for _, w := range words {
		if strings.ContainsAny(w.Text, "\t\n|") {
			return fmt.Errorf("词 %q 含制表符、换行或竖线 —— 协议是行式文本,装不下", w.Text)
		}
		// 词后面挂音节边界:`你好|2`、`吗|`、`招聘顾问|2,4,6`。
		// 竖线安全:排版器的输出字母表是汉字、全角标点、数字与英文字母,竖线不在其中;
		// 真正兜底的仍是上面那个 ContainsAny。
		n := make([]string, len(w.Splits))
		for i, v := range w.Splits {
			n[i] = strconv.Itoa(v)
		}
		fields = append(fields, w.Text+"|"+strings.Join(n, ","))
	}
	tc, err := s.pick()
	if err != nil {
		return err
	}
	_, err = io.WriteString(tc.c, "WORDS\t"+strings.Join(fields, "\t")+"\n")
	return err
}

func (s *tipServer) pick() (*tipConn, error) {
	if pid, err := foregroundPid(); err == nil {
		s.mu.Lock()
		tc := s.conns[pid]
		s.mu.Unlock()
		if tc != nil {
			s.log(fmt.Sprintf("选中:前台是 %s pid=%d", tc.host, tc.pid))
			return tc, nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.conns) == 1 {
		for _, tc := range s.conns {
			s.log(fmt.Sprintf("选中:只连着一条 —— %s pid=%d(前台不是它,但没有歧义)", tc.host, tc.pid))
			return tc, nil
		}
	}
	have := make([]string, 0, len(s.conns))
	for _, tc := range s.conns {
		have = append(have, fmt.Sprintf("%s(pid=%d)", tc.host, tc.pid))
	}
	if len(have) == 0 {
		return nil, fmt.Errorf("没有 TIP 连上来 —— 输入法切到我们这个了吗?Chrome 是在 regsvr32 之后启动的吗?")
	}
	return nil, fmt.Errorf("前台进程里没有 TIP,而连着的有 %d 条:%s —— 有歧义,不猜",
		len(have), strings.Join(have, " "))
}

// clear 让所有连着的 TIP 回到透传。
//
// **它比按住的 Shift 更要紧。** 留在受驱动状态的 TIP 会把我方的词表用在**真人
// 自己敲的字**上:招聘人员随手打一句话,屏幕上出来的是我们上一条消息的内容。
// 所以调用方必须 defer 它,和释放按住的键同一个理由、更高的优先级。
func (s *tipServer) clear() {
	s.mu.Lock()
	cs := make([]io.ReadWriteCloser, 0, len(s.conns))
	for _, tc := range s.conns {
		cs = append(cs, tc.c)
	}
	s.mu.Unlock()
	for _, c := range cs {
		_, _ = io.WriteString(c, "CLEAR\n")
	}
}

// beginRound 把上一条命令的回报清零。
//
// 管道现在是进程级的,计数器因此跨命令共享——不清零的话第二条命令会把第一条的
// COMMIT 算进自己的对账,得出一个"看起来成功"的假结论。
func (s *tipServer) beginRound() {
	s.commits.Store(0)
	s.done.Store(false)
}

func (s *tipServer) close() { _ = s.ln.Close() }

// ── Injector 侧的接线 ───────────────────────────────────────────────────────

// DriveWords 让 TIP 接管这一轮的上屏内容,返回收尾函数与一句对账结论。
//
// **没有 TIP 连上来就报错,不打。** 这是 Windows 与 macOS 刻意不同的地方:
// mac 上没有 TIP 是常态,照打、回读对不上如实报;Windows 上没有 TIP 却照打,
// 出来的是微软拼音的首选词——而那与「TIP 装了但选错」在现场长得一模一样,
// 会把一次真机趟白白烧掉。报错反而给出了可操作的下一步。
//
// 调用时机必须是**焦点已经在目标窗口之后**:sendWords 按前台窗口的 pid 挑连接,
// SendInput 的按键也只落在前台窗口,两者得指同一个进程。
func (w *windowsInjector) DriveWords(words []PlanWord, wait time.Duration) (WordSession, error) {
	s, err := w.tipService()
	if err != nil {
		return nil, err
	}
	// TIP 每 300ms 重试一次连接,所以首次一般一秒内就上来了;之后它一直连着,
	// 这个循环立刻返回。
	deadline := time.Now().Add(wait)
	for {
		s.mu.Lock()
		n := len(s.conns)
		s.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("等了 %s 没有 TIP 连上来 —— 输入法切到我们这个了吗?"+
				"Chrome 是在 regsvr32 之后启动的吗?", wait)
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.beginRound()
	if err := s.sendWords(words); err != nil {
		return nil, fmt.Errorf("%w(现场:%s)", err, s.recent(6))
	}
	return &tipSession{s: s, want: len(words)}, nil
}

// tipService 惰性建管道,**建一次,进程在就一直在**(理由见文件头)。
//
// 惰性而不是开机就建:非 Windows 根本没有这段代码,而 Windows 上没人用键盘线时
// 也不该白占一个命名管道。第一条打字命令建它,失败就在那条命令上如实报。
func (w *windowsInjector) tipService() (*tipServer, error) {
	w.tipMu.Lock()
	defer w.tipMu.Unlock()
	if w.tip != nil {
		return w.tip, nil
	}
	s, err := listenTip()
	if err != nil {
		return nil, fmt.Errorf("起管道失败(%s):%w —— 若是「拒绝访问」,多半是同名管道"+
			"还被别的进程占着(上一个脑没退干净?)", tipPipe, err)
	}
	w.tip = s
	return s, nil
}

// closeTip 在注入器关闭时收掉管道。**只有进程退出这一条路**——
// 命令收尾走 tipSession.Close(),那里只发 CLEAR,不碰监听器。
func (w *windowsInjector) closeTip() {
	w.tipMu.Lock()
	defer w.tipMu.Unlock()
	if w.tip != nil {
		w.tip.clear()
		w.tip.close()
		w.tip = nil
	}
}

type tipSession struct {
	s    *tipServer
	want int
}

// Settle 等 TIP 把这一轮的回报送完,再给结论。
//
// **不能盲等一个固定时长。** 上游先前是 Sleep(400ms),于是对账在 COMMIT 到达之前
// 就打印了——真机上打出「0/6」而实际上屏了 1 个。**结论本身不可信,比没有结论更糟。**
func (d *tipSession) Settle(timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if d.s.done.Load() || int(d.s.commits.Load()) >= d.want {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := int(d.s.commits.Load())
	switch {
	case got == d.want && d.s.done.Load():
		return fmt.Sprintf("TIP 上屏 %d/%d 词,词表已用完", got, d.want)
	case got == d.want:
		return fmt.Sprintf("TIP 上屏 %d/%d 词,但没收到 DONE", got, d.want)
	default:
		return fmt.Sprintf("TIP 只上屏了 %d/%d 词(%s)", got, d.want, d.s.recent(4))
	}
}

// Close 只让 TIP 回到透传,**不关监听器**——它是进程级的,关了下一条命令就建不回来
// (go-winio 用 FILE_CREATE,同名管道有实例就建不出;而 TIP 连上就一直连着)。
//
// CLEAR 本身仍然是必须的:留在受驱动状态的输入法会把我方词表用在真人自己敲的字上。
func (d *tipSession) Close() {
	d.s.clear()
	time.Sleep(100 * time.Millisecond)
}
