package adminhttp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"recruithelper/client/service/internal/store"
)

func statusBarRequest(t *testing.T, mux *http.ServeMux, method, body string) statusBarSettingsView {
	t.Helper()
	var reader *bytes.Buffer
	if body != "" {
		reader = bytes.NewBufferString(body)
	} else {
		reader = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, "/admin/statusbar/settings", reader)
	req.Header.Set("Origin", "http://127.0.0.1:5273")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("%s 应 200,得到 %d: %s", method, w.Code, w.Body.String())
	}
	var view statusBarSettingsView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	return view
}

// 新库默认客户版;诊断台点开后重开一个 API(模拟脑重启)仍读到开——开关落在脑里。
func TestStatusBarSettingsDefaultOffAndPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	api := New(st, newFakeAdminHub(), nil, nil, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)

	if got := statusBarRequest(t, mux, http.MethodGet, ""); got.DetailEnabled {
		t.Fatalf("新库应是客户版: %+v", got)
	}
	if got := statusBarRequest(t, mux, http.MethodPost, `{"detailEnabled":true}`); !got.DetailEnabled {
		t.Fatalf("开启后应回开: %+v", got)
	}
	st.Close()

	reopened, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	api2 := New(reopened, newFakeAdminHub(), nil, nil, nil, "")
	mux2 := http.NewServeMux()
	api2.Routes(mux2)
	if got := statusBarRequest(t, mux2, http.MethodGet, ""); !got.DetailEnabled {
		t.Fatalf("重开库后开关应仍为开: %+v", got)
	}
	if got := statusBarRequest(t, mux2, http.MethodPost, `{"detailEnabled":false}`); got.DetailEnabled {
		t.Fatalf("关闭后应回关: %+v", got)
	}
}

func TestStatusBarSettingsRejectsBodyWithoutFlag(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	api := New(st, newFakeAdminHub(), nil, nil, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)
	req := httptest.NewRequest(http.MethodPost, "/admin/statusbar/settings", bytes.NewBufferString(`{}`))
	req.Header.Set("Origin", "http://127.0.0.1:5273")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺字段应 400,得到 %d: %s", w.Code, w.Body.String())
	}
}
