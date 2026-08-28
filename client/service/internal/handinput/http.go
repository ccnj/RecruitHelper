package handinput

// 手服务的本地 HTTP 面。
//
// # 为什么挂在已有的 mux 上,而不是新开端口
//
// 脑现在已经在 127.0.0.1 上一个 mux 里并列着脑手 WS、blob 数据面、/admin 与 /app。
// 再开一个端口要多一份监听、多一条防火墙面、多一次杀软打量;而插件对这个 origin
// 已经有权限,manifest 一个字都不用改。
//
// 同时它让「坐标不许进协议」变成**结构上可查的**:坐标走的是另一条路径、另一套
// 消息类型,codegen 出来的协议类型永远不出现在这里。
//
// # 为什么是请求/响应,不是第二条 WS
//
// 上游原型是 Go 编排、页面持续上报观测,于是需要一条与执行并发的反向流。
// 我方是**插件编排**——我算计划、让 Go 播、我读落点、让 Go 点,全是插件发起的
// 一问一答,标定样本搭在下一个请求里带回去。不需要推送,也就不需要 WS。
//
// # 鉴权
//
// 没有。与既有脑手 WS 同一姿态(协议规格:零配对、零握手身份 token),边界收在
// loopback。代价是本机任何进程都能调它移鼠标——2026-08-28 甲方裁决接受。

import (
	"encoding/json"
	"net/http"
)

// Routes 把手服务挂到已有的 mux 上。
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("/handinput/state", s.handleState)
	mux.HandleFunc("/handinput/play", s.handlePlay)
	mux.HandleFunc("/handinput/landing", s.handleLanding)
	mux.HandleFunc("/handinput/click", s.handleClick)
}

// state 是 POST 而不是 GET,因为它可以捎一份窗口粗估过来播种。
//
// 次序上这是必须的:编排层在生成计划之前要先问光标在哪,而"光标在哪"的答案要经
// 当前标定反算——没播种就连粗估都没有,只能回 nil。让第一次问状态就把粗估带上,
// 这个先有鸡还是先有蛋的坎就没了。粗估只用来猜往哪个方向先动,不参与任何计算。
func (s *Service) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "只接受 POST", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Hint *WindowHint `json:"hint,omitempty"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			http.Error(w, "请求体解析失败:"+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if req.Hint != nil {
		s.Seed(*req.Hint)
	}
	writeJSON(w, http.StatusOK, s.State())
}

type playRequest struct {
	Points []PlanPoint `json:"points"`
	// Hint 是窗口位置粗估,只在还没标定过时用来播种。
	// 它可能直接是错的(上游实测:窗口一次没动,页面报的 screenX 在 430 与 0 之间跳),
	// 所以**只用来猜往哪个方向先动,不参与任何计算**。
	Hint *WindowHint `json:"hint,omitempty"`
}

func (s *Service) handlePlay(w http.ResponseWriter, r *http.Request) {
	var req playRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Hint != nil {
		s.Seed(*req.Hint)
	}
	res, err := s.Play(req.Points)
	if err != nil {
		// 注入失败也把已发出的部分带回去:插件要知道光标停在哪儿了。
		res.Status = err.Error()
		writeJSON(w, http.StatusInternalServerError, res)
		return
	}
	res.ClickArmed = false // 播放期间不许点击,落点确认之后才点亮
	writeJSON(w, http.StatusOK, res)
}

type landingRequest struct {
	ClientX float64 `json:"clientX"`
	ClientY float64 `json:"clientY"`
}

type landingResponse struct {
	Status     string   `json:"status"`
	ClickArmed bool     `json:"clickArmed"`
	Detail     string   `json:"detail,omitempty"`
	ResidualPx *float64 `json:"residualPx"`
	Samples    int      `json:"samples"`
}

func (s *Service) handleLanding(w http.ResponseWriter, r *http.Request) {
	var req landingRequest
	if !readJSON(w, r, &req) {
		return
	}
	st, err := s.Landing(req.ClientX, req.ClientY)
	resp := landingResponse{Status: st.String(), ResidualPx: finiteOrNil(s.pb.Residual()), Samples: s.pb.N()}
	s.mu.Lock()
	resp.ClickArmed = s.armed
	s.mu.Unlock()
	if err != nil {
		// 「落点对不上」不是服务故障,是标定在说话——200 带原因,让插件按状态裁决。
		resp.Detail = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

type clickRequest struct {
	PressMs float64 `json:"pressMs"`
}

func (s *Service) handleClick(w http.ResponseWriter, r *http.Request) {
	var req clickRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.Click(req.PressMs); err != nil {
		// 未放行是**拒绝**,不是失败:方向永远是宁可不点。
		writeJSON(w, http.StatusConflict, map[string]string{"refused": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"clicked": true})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "只接受 POST", http.StatusMethodNotAllowed)
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(v); err != nil {
		http.Error(w, "请求体解析失败:"+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// writeJSON 先编码再写头。
//
// **顺序不能反。** 直接 json.NewEncoder(w).Encode(v) 会先把 200 发出去,
// 编码这一步再失败就无处可去了——真机第一跑正是这个形态:一个 NaN 让
// /handinput/state 回了 200 加空 body,而日志里一个字都没有。
// 先编码,失败就还能回 500 并说清楚是什么。
func writeJSON(w http.ResponseWriter, code int, v any) {
	buf, err := json.Marshal(v)
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("手服务响应序列化失败:" + err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(buf)
}
