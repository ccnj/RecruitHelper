package handinput

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 全部路由只接受 POST。
//
// 2026-08-30 真机首次点击炸在放行前一步:插件的 callHand「有 body 就 POST、没有
// 就 GET」,而手服务的端点全是 POST-only,于是 GET /state 回了 405。那次是客户端
// 的错,但服务端这条性质从没被钉住过——新加路由时忘了它,症状会一模一样地重演。
//
// **加路由要在这张表里加一行。** ServeMux 没有公开的枚举接口,只能手记。
func TestHTTPAllRoutesRefuseGET(t *testing.T) {
	s := NewService(&fakeInjector{})
	mux := http.NewServeMux()
	s.Routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, path := range []string{
		"/handinput/state", "/handinput/play", "/handinput/landing",
		"/handinput/click", "/handinput/reseed", "/handinput/type",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("GET %s 回了 %d,应当是 405", path, resp.StatusCode)
		}
	}
}

func TestHTTPTypeRoundTrip(t *testing.T) {
	f := &fakeInjector{}
	s := NewService(f)
	s.mode = WaitSleep
	mux := http.NewServeMux()
	s.Routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	post := func(body any, out any) int {
		t.Helper()
		b, _ := json.Marshal(body)
		resp, err := http.Post(srv.URL+"/handinput/type", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			_ = json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}

	var res TypeResult
	if code := post(planNi(), &res); code != http.StatusOK {
		t.Fatalf("合法计划回了 %d", code)
	}
	if res.Keys != 6 || res.Status != "ok" {
		t.Fatalf("回执 %+v,应当发了 6 次且 ok", res)
	}

	// 校验不过时:500,回执带上已发出的次数(插件要知道发出去多少),且一个都没发。
	f.keys = nil
	var bad TypeResult
	if code := post(planQuestion(100, 120), &bad); code != http.StatusInternalServerError {
		t.Fatalf("修饰键窗口不足回了 %d,应当是 500", code)
	}
	if bad.Keys != 0 || len(f.keys) != 0 {
		t.Fatalf("校验不过时不该发键:回执 %d 次、实发 %v", bad.Keys, f.keys)
	}
}

// 整份计划必须原样收下,含 macOS 上用不着的 Text 与 Splits。
//
// 那两样是 Windows 那半要的:上屏哪个词经命名管道告诉自研 TIP,而 Splits 是音节
// 边界(只影响 TIP 组字区的显示)。**刻意不为开发机裁字段**——一份计划在两个平台上
// 必须是同一份,否则「在 mac 上验过」就不再说明任何事。
func TestHTTPTypeKeepsTipFieldsThroughJSON(t *testing.T) {
	b, err := json.Marshal(planNi())
	if err != nil {
		t.Fatal(err)
	}
	var back TypePlan
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Words) != 1 {
		t.Fatalf("词数 %d", len(back.Words))
	}
	if back.Words[0].Text != "你" {
		t.Errorf("Text 丢了:%q —— TIP 靠它决定上屏哪个词", back.Words[0].Text)
	}
	if len(back.Words[0].Splits) != 1 || back.Words[0].Splits[0] != 2 {
		t.Errorf("Splits 丢了:%v —— TIP 靠它画组字区的撇号", back.Words[0].Splits)
	}
	if back.Words[0].Commit == nil || back.Words[0].Commit.Code != "Space" {
		t.Errorf("Commit 丢了:%+v", back.Words[0].Commit)
	}
}
