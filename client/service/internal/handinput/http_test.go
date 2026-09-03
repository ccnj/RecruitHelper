package handinput

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 走真 HTTP 把「移动 -> 读落点 -> 喂标定 -> 放行 -> 点击」整条跑一遍。
//
// 注入器是假的(真机注入只有 Windows 上有),但**编排的形状是真的**:全部由客户端
// 发起、一问一答、标定样本搭在请求里回来——不需要推送,也就不需要第二条 WS。
func TestHTTPRoundTripFromColdStartToClick(t *testing.T) {
	f := &fakeInjector{truth: Calib{ScaleX: 1, ScaleY: 1, OffsetX: 0, OffsetY: 151}}
	s := NewService(f)
	s.mode = WaitSleep
	mux := http.NewServeMux()
	s.Routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	post := func(path string, body any, out any) int {
		t.Helper()
		b, _ := json.Marshal(body)
		resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			_ = json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}

	// 冷启动:先问状态。此时不该放行点击。
	var st State
	rs, err := http.Post(srv.URL+"/handinput/state", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(rs.Body).Decode(&st)
	rs.Body.Close()
	if st.ClickArmed || st.Calibrated {
		t.Fatalf("冷启动就报已标定/已放行:%+v", st)
	}

	// 两趟「正常移动」——第一趟带窗口粗估播种。冷启动的第一次必然打偏,
	// 那正是搭车标定要吃的样本。
	hint := &WindowHint{ScreenX: 0, ScreenY: 151, DPR: 1}
	for i, target := range [][2]float64{{40, 30}, {900, 700}} {
		req := playRequest{Points: []PlanPoint{
			{X: target[0] - 5, Y: target[1] - 5, T: 0}, {X: target[0], Y: target[1], T: 1},
		}}
		if i == 0 {
			req.Hint = hint
		}
		var pr PlayResult
		if code := post("/handinput/play", req, &pr); code != http.StatusOK {
			t.Fatalf("play 返回 %d", code)
		}
		if pr.ClickArmed {
			t.Fatal("播放响应不该报已放行——光标刚动过,落点还没确认")
		}
		last := f.moves[len(f.moves)-1]
		cx, cy := f.truth.ToClient(last[0], last[1])
		var lr landingResponse
		if code := post("/handinput/landing",
			landingRequest{ClientX: math.Floor(cx + 0.5), ClientY: math.Floor(cy + 0.5)}, &lr); code != http.StatusOK {
			t.Fatalf("landing 返回 %d", code)
		}
		if i == 0 && lr.ClickArmed {
			t.Fatal("只有一个样本、跨度不够就放行了点击")
		}
		if i == 1 && !lr.ClickArmed {
			t.Fatalf("两趟落点后应放行,得到 %+v", lr)
		}
	}

	if code := post("/handinput/click", clickRequest{PressMs: 96}, nil); code != http.StatusOK {
		t.Fatalf("放行状态下点击返回 %d", code)
	}
	if f.downs != 1 || f.ups != 1 {
		t.Fatalf("应恰好一按一抬:down=%d up=%d", f.downs, f.ups)
	}

	// 同一次落点确认不能放行第二次点击。**拒绝是 409,不是 500**——
	// 它不是服务故障,是闸在说话,插件要能分开处理。
	if code := post("/handinput/click", clickRequest{PressMs: 96}, nil); code != http.StatusConflict {
		t.Fatalf("重复点击应被拒(409),得到 %d", code)
	}
}

// 落点对不上时 landing 仍回 200:那不是服务故障,是标定在说话。
// 状态与原因都在响应体里,由插件按状态裁决——而 clickArmed 必然是 false。
func TestHTTPLandingDriftIsNotAnError(t *testing.T) {
	s, f := newReadyService(t)
	mux := http.NewServeMux()
	s.Routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f.truth = Calib{ScaleX: 1, ScaleY: 1, OffsetX: 0, OffsetY: 451} // 窗口被拖走
	if _, err := s.Play([]PlanPoint{{X: 500, Y: 400, T: 0}}); err != nil {
		t.Fatal(err)
	}
	last := f.moves[len(f.moves)-1]
	cx, cy := f.truth.ToClient(last[0], last[1])
	b, _ := json.Marshal(landingRequest{ClientX: math.Floor(cx + 0.5), ClientY: math.Floor(cy + 0.5)})
	resp, err := http.Post(srv.URL+"/handinput/landing", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("落点漂移不该是 HTTP 错误,得到 %d", resp.StatusCode)
	}
	var lr landingResponse
	_ = json.NewDecoder(resp.Body).Decode(&lr)
	if lr.Status != PBSuspect.String() || lr.ClickArmed || lr.Detail == "" {
		t.Fatalf("应报存疑、不放行、带原因,得到 %+v", lr)
	}
}

// 真机第一跑的回归:冷启动时 /handinput/state 回了 200 却带着空 body。
//
// 根因是样本不足两个时 Piggyback.Residual() 返回 NaN,而 encoding/json 编不了 NaN;
// 老写法先 WriteHeader 再 Encode,头已经发出去了,错误无处可去,日志里也一个字没有。
//
// **判据必须盯 body,不能只盯状态码**——那正是这个 bug 骗过眼睛的地方。
func TestHTTPStateOnColdStartReturnsBodyNotJustStatus(t *testing.T) {
	f := &fakeInjector{truth: Calib{ScaleX: 1, ScaleY: 1}}
	s := NewService(f)
	mux := http.NewServeMux()
	s.Routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/handinput/state", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("state 返回 %d", resp.StatusCode)
	}
	var st State
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatalf("冷启动的 state 解不开——多半又有 NaN/Inf 混进了序列化:%v", err)
	}
	if st.Platform == "" {
		t.Fatal("body 是空的:200 但什么都没回")
	}
	if st.CursorCSSX != nil || st.CursorCSSY != nil {
		t.Fatal("没播种就报出了光标 CSS 坐标——那时零值标定会除以零,报出来的是编造的")
	}
	if st.ResidualPx != nil {
		t.Fatalf("样本不足时残差必须是 nil 而不是 0——0 的意思是拟合完美,那是撒谎;得到 %v", *st.ResidualPx)
	}
}

// /handinput/scroll 的三种收场要分得开:光标不在我们放它的地方是 409(闸在说话,
// 插件按原因收场不重试),计划排错是 500,滚成了 200——而滚不需要 armed,只需要光标还在。
func TestHTTPScrollSeparatesRefusalFromFailure(t *testing.T) {
	s, f := newReadyService(t)
	mux := http.NewServeMux()
	s.Routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	post := func(body any) (int, ScrollResult) {
		t.Helper()
		b, _ := json.Marshal(body)
		resp, err := http.Post(srv.URL+"/handinput/scroll", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out ScrollResult
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	moved := [2]float64{9_000, 9_000}
	f.cursorAt = &moved
	if code, res := post(ScrollPlan{Ticks: []ScrollTick{{At: 0, Dy: 1}}}); code != http.StatusConflict || res.Status == "" {
		t.Fatalf("光标被挪走应回 409 并带原因,得到 %d %+v", code, res)
	}
	f.cursorAt = nil

	if code, res := post(ScrollPlan{Ticks: []ScrollTick{{At: 0, Dy: 0}}}); code != http.StatusInternalServerError || res.Status == "" {
		t.Fatalf("排错的计划应回 500 并带原因,得到 %d %+v", code, res)
	}
	if len(f.wheels) != 0 {
		t.Fatalf("前两次都不该发出滚轮:%v", f.wheels)
	}

	code, res := post(ScrollPlan{Ticks: []ScrollTick{{At: 0, Dy: -1}, {At: 15, Dy: -1}}})
	if code != http.StatusOK || res.Ticks != 2 || res.Status != "ok" {
		t.Fatalf("光标还在原处应当滚成,得到 %d %+v", code, res)
	}
}
