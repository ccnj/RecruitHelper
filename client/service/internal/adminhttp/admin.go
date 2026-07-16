// Package adminhttp:本地管理端点。测试页与未来 UI 调的就是这些接口——
// 不存在"开发模式绕过":配对窗、确认、列表均走同一路径(宪法 3)。
package adminhttp

import (
	"encoding/json"
	"net/http"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/pairing"
	"recruithelper/client/service/internal/session"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

type API struct {
	st   *store.Store
	pm   *pairing.Manager
	hub  *session.Hub
	disp *dispatch.Dispatcher
}

func New(st *store.Store, pm *pairing.Manager, hub *session.Hub, disp *dispatch.Dispatcher) *API {
	return &API{st: st, pm: pm, hub: hub, disp: disp}
}

func (a *API) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/health", a.health)
	mux.HandleFunc("POST /admin/pairing/open", a.pairingOpen)
	mux.HandleFunc("GET /admin/pairing/pending", a.pairingPending)
	mux.HandleFunc("POST /admin/pairing/confirm", a.pairingConfirm)
	mux.HandleFunc("GET /admin/hands", a.hands)
	mux.HandleFunc("GET /admin/hands/health", a.handHealth)
	mux.HandleFunc("POST /admin/cmd", a.postCmd)
	mux.HandleFunc("GET /admin/ledger", a.ledger)
}

func (a *API) postCmd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HandID string          `json:"handId"`
		Name   string          `json:"name"`
		Args   json.RawMessage `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法请求体"})
		return
	}
	if req.Args == nil {
		req.Args = json.RawMessage("{}")
	}
	msgID, err := a.disp.Dispatch(req.HandID, req.Name, req.Args)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "msgId": msgID})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"msgId": msgID})
}

func (a *API) ledger(w http.ResponseWriter, _ *http.Request) {
	recs, err := a.st.RecentCmds(50)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	type view struct {
		MsgID     string `json:"msgId"`
		Name      string `json:"name"`
		Class     string `json:"class"`
		Status    string `json:"status"`
		Attempt   int    `json:"attempt"`
		ErrorCode string `json:"errorCode,omitempty"`
	}
	out := make([]view, 0, len(recs))
	for _, r := range recs {
		out = append(out, view{MsgID: r.MsgID, Name: r.Name, Class: r.Class, Status: string(r.Status), Attempt: r.Attempt, ErrorCode: r.ErrorCode})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ledger": out})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"proto":       protocol.ProtoVersion,
		"contract":    protocol.ContractHash,
		"pairingOpen": a.pm.WindowOpen(),
		"activeHands": a.hub.ActiveHandIDs(),
	})
}

func (a *API) handHealth(w http.ResponseWriter, _ *http.Request) {
	states := a.hub.Registry().Snapshot()
	type view struct {
		HandID   string   `json:"handId"`
		Online   bool     `json:"online"`
		Health   string   `json:"health"`
		Caps     []string `json:"caps"`
		LastHbMs int64    `json:"lastHbAgoMs"`
	}
	out := make([]view, 0, len(states))
	for _, s := range states {
		out = append(out, view{
			HandID: s.HandID, Online: s.Online, Health: string(s.Health),
			Caps: s.Caps, LastHbMs: time.Since(s.LastHbAt).Milliseconds(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"hands": out})
}

func (a *API) pairingOpen(w http.ResponseWriter, _ *http.Request) {
	a.pm.OpenWindow(time.Duration(protocol.DefaultPairingWindowMs) * time.Millisecond)
	writeJSON(w, http.StatusOK, map[string]any{"open": true, "windowMs": protocol.DefaultPairingWindowMs})
}

func (a *API) pairingPending(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"open": a.pm.WindowOpen(), "pending": a.pm.Pending()})
}

func (a *API) pairingConfirm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Origin string `json:"origin"`
		BootID string `json:"bootId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法请求体"})
		return
	}
	creds, err := a.pm.Confirm(req.Origin, req.BootID)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	// 只回 handId;token 只经 WS welcome 下发给手,不从管理接口外泄。
	writeJSON(w, http.StatusOK, map[string]string{"handId": creds.HandID})
}

func (a *API) hands(w http.ResponseWriter, _ *http.Request) {
	hands, err := a.st.Hands()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	online := map[string]bool{}
	for _, id := range a.hub.ActiveHandIDs() {
		online[id] = true
	}
	type handView struct {
		HandID     string    `json:"handId"`
		Origin     string    `json:"origin"`
		Online     bool      `json:"online"`
		LastSeenAt time.Time `json:"lastSeenAt"`
	}
	out := make([]handView, 0, len(hands))
	for _, h := range hands {
		out = append(out, handView{HandID: h.HandID, Origin: h.Origin, Online: online[h.HandID], LastSeenAt: h.LastSeenAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"hands": out})
}
