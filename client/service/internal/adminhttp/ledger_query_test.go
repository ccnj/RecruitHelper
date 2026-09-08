package adminhttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
)

func TestLedgerQueryDefaultsAndClamps(t *testing.T) {
	cases := []struct {
		raw   string
		limit int
		brief bool
	}{
		{"", 50, false},
		{"limit=12&brief=1", 12, true},
		{"limit=0", 50, false},
		{"limit=999", 50, false},
		{"limit=abc&brief=true", 50, true},
		{"brief=0", 50, false},
	}
	for _, c := range cases {
		q, _ := url.ParseQuery(c.raw)
		limit, brief := ledgerQuery(q)
		if limit != c.limit || brief != c.brief {
			t.Fatalf("%q: 得到 limit=%d brief=%v,期望 %d/%v", c.raw, limit, brief, c.limit, c.brief)
		}
	}
}

// brief 模式只拿掉三个大字段,扫读字段原样;limit 真的裁条数。
func TestLedgerBriefDropsBulkFieldsAndLimitApplies(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	base := time.Date(2026, 9, 8, 10, 0, 0, 0, time.Local)
	for i := 0; i < 3; i++ {
		terminalAt := base.Add(time.Duration(i) * time.Minute)
		rec := &store.CmdRecord{
			MsgID: "m-" + string(rune('a'+i)), LogicalDispatchID: "m-" + string(rune('a'+i)),
			Name: "chat.readList", Class: "readonly", Args: `{"jobName":"保险顾问"}`,
			Guards: `{"x":1}`, Domain: "zhilian:acct", Platform: "zhilian", AccountRef: "acct",
			HandID: "hand-1", Status: store.CmdOk, ResultBody: `{"big":"` + string(make([]byte, 2048)) + `"}`,
			TerminalAt: &terminalAt,
		}
		if err := st.CreateCmd(rec); err != nil {
			t.Fatal(err)
		}
	}
	api := New(st, newFakeAdminHub(), nil, nil, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)

	get := func(path string) []map[string]any {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Origin", "http://127.0.0.1:5273")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var body struct {
			Ledger []map[string]any `json:"ledger"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Ledger
	}

	full := get("/admin/ledger")
	if len(full) != 3 {
		t.Fatalf("默认应回全部 3 条,得到 %d", len(full))
	}
	if full[0]["resultBody"] == "" || full[0]["args"] == "" || full[0]["guards"] == "" {
		t.Fatalf("默认模式应带完整字段: %v", full[0])
	}

	brief := get("/admin/ledger?limit=2&brief=1")
	if len(brief) != 2 {
		t.Fatalf("limit=2 应回 2 条,得到 %d", len(brief))
	}
	for _, row := range brief {
		if row["resultBody"] != "" || row["args"] != "" || row["guards"] != "" {
			t.Fatalf("brief 不得带大字段: %v", row)
		}
		if row["name"] != "chat.readList" || row["summary"] != "保险顾问" || row["status"] != "ok" {
			t.Fatalf("brief 的扫读字段应原样: %v", row)
		}
	}
}
