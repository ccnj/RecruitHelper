// Package adminhttp:本地管理端点。测试页与未来 UI 调的就是这些接口。
package adminhttp

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"mime"
	"net/http"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/jobconfig"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/patrol"
	"recruithelper/client/service/internal/session"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

type API struct {
	st              *store.Store
	hub             AdminHub
	disp            *dispatch.Dispatcher
	actor           *patrol.Manager
	probe           AccountProber
	adminToken      string
	providerConfig  *m5ai.ProviderConfigStore
	jobConfigSource *jobconfig.Source
}

func (a *API) SetJobConfigSource(source *jobconfig.Source) *API {
	a.jobConfigSource = source
	return a
}

type AccountProber interface {
	Probe(context.Context, string) (protocol.ProbePlatformData, error)
}

// AdminHub 是本地管理面所需的只读会话视图，便于对绑定期间的
// session/boot TOCTOU 做不经网络监听器的确定性测试。
type AdminHub interface {
	Frames() *session.FrameBus
	Registry() *session.Registry
	ActiveHandIDs() []string
	HandSession(string) (sessionID, bootID string, online bool)
	HandWitness(string) (dispatch.HandWitness, bool)
	WithCurrentHandSession(handID, sessionID, bootID string, fn func() error) (current bool, err error)
}

func New(
	st *store.Store,
	hub AdminHub,
	disp *dispatch.Dispatcher,
	actor *patrol.Manager,
	probe AccountProber,
	adminToken string,
	providerConfig ...*m5ai.ProviderConfigStore,
) *API {
	api := &API{
		st: st, hub: hub, disp: disp, actor: actor, probe: probe,
		adminToken: adminToken,
	}
	if len(providerConfig) > 0 {
		api.providerConfig = providerConfig[0]
	}
	return api
}

func (a *API) Routes(mux *http.ServeMux) {
	h := func(f http.HandlerFunc) http.HandlerFunc { return a.guard(f) }
	mux.HandleFunc("GET /admin/health", h(a.health))
	mux.HandleFunc("GET /admin/hands", h(a.hands))
	mux.HandleFunc("GET /admin/hands/health", h(a.handHealth))
	mux.HandleFunc("POST /admin/hands/reload", h(a.reloadHand))
	mux.HandleFunc("POST /admin/cmd", h(a.postCmd))
	mux.HandleFunc("POST /admin/messages/send", h(a.sendMessage))
	mux.HandleFunc("GET /admin/messages/send", h(a.sendMessageStatus))
	mux.HandleFunc("GET /admin/ledger", h(a.ledger))
	mux.HandleFunc("GET /admin/suspects", h(a.suspects))
	mux.HandleFunc("POST /admin/suspects/verdict", h(a.verdict))
	mux.HandleFunc("GET /admin/frames", h(a.frames))
	mux.HandleFunc("GET /admin/accounts", h(a.accounts))
	mux.HandleFunc("POST /admin/accounts/bind", h(a.bindAccount))
	mux.HandleFunc("POST /admin/accounts/enable", h(a.enableAccount))
	mux.HandleFunc("POST /admin/accounts/stop", h(a.stopAccount))
	mux.HandleFunc("POST /admin/accounts/pause", h(a.pauseAccount))
	mux.HandleFunc("POST /admin/accounts/run", h(a.runAccount))
	mux.HandleFunc("POST /admin/candidates/current/read", h(a.readCurrentCandidate))
	mux.HandleFunc("POST /admin/candidates/current/select", h(a.selectCurrentCandidate))
	mux.HandleFunc("POST /admin/candidates/greeting/send", h(a.sendGreeting))
	mux.HandleFunc("GET /admin/candidates/greeting/send", h(a.sendGreetingStatus))
	mux.HandleFunc("POST /admin/m5/trial/select", h(a.selectM5Trial))
	mux.HandleFunc("GET /admin/m5/trial", h(a.m5TrialStatus))
	mux.HandleFunc("GET /admin/m5/provider-config", h(a.m5ProviderConfig))
	mux.HandleFunc("POST /admin/m5/provider-config", h(a.saveM5ProviderConfig))
	mux.HandleFunc("GET /admin/m5/contexts", h(a.m5Contexts))
	mux.HandleFunc("POST /admin/m5/contexts/import", h(a.importM5Contexts))
	mux.HandleFunc("GET /admin/job-config/source", h(a.jobConfigSourceConfig))
	mux.HandleFunc("POST /admin/job-config/activate", h(a.activateJobConfigSource))
	mux.HandleFunc("POST /admin/job-config/sync-current", h(a.syncCurrentJobConfig))
	mux.HandleFunc("POST /admin/sourcing/start", h(a.startSourcing))
	mux.HandleFunc("POST /admin/sourcing/stop", h(a.stopSourcing))
	mux.HandleFunc("GET /admin/sourcing/status", h(a.sourcingStatus))
	mux.HandleFunc("POST /admin/sourcing/scoring/run", h(a.runSourcingScoring))
	mux.HandleFunc("GET /admin/sourcing/scoring/status", h(a.sourcingScoringStatus))
	mux.HandleFunc("POST /admin/sourcing/selection/run", h(a.runSourcingSelection))
	mux.HandleFunc("GET /admin/sourcing/selection/status", h(a.sourcingSelectionStatus))
	mux.HandleFunc("POST /admin/sourcing/greeting-generation/run", h(a.runSourcingGreetingGeneration))
	mux.HandleFunc("GET /admin/sourcing/greeting-generation/status", h(a.sourcingGreetingGenerationStatus))
	mux.HandleFunc("POST /admin/sourcing/greeting-send/run", h(a.runSourcingGreetingSend))
	mux.HandleFunc("GET /admin/sourcing/greeting-send/status", h(a.sourcingGreetingSendStatus))
	mux.HandleFunc("GET /admin/m5/context-binding", h(a.m5ContextBinding))
	mux.HandleFunc("POST /admin/m5/context-binding", h(a.bindM5Context))
	mux.HandleFunc("GET /admin/conversations", h(a.conversations))
	mux.HandleFunc("POST /admin/conversations/track", h(a.trackConversation))
	mux.HandleFunc("GET /admin/messages", h(a.messages))
	mux.HandleFunc("GET /admin/audits", h(a.audits))
	// 预检
	mux.HandleFunc("OPTIONS /admin/", h(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
}

// guard 把本地管理面与任意网页隔离：生产 Electron 使用每次启动随机 bearer；
// 无 token 的 go run 开发态只接受无 Origin 的本机工具和固定 Vite Origin。
func (a *API) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		trustedDevOrigin := origin == "http://127.0.0.1:5273" || origin == "http://localhost:5273"
		corsAllowed := trustedDevOrigin || (a.adminToken != "" && origin == "null")
		if corsAllowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		}
		if r.Method == http.MethodOptions {
			if !corsAllowed {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "不受信任的管理端 Origin"})
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if origin != "" && !corsAllowed {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "不受信任的管理端 Origin"})
			return
		}

		// 健康探针允许不带 Origin 的 Electron 主进程访问；所有浏览器请求及其他
		// 管理请求仍经过同一认证边界。
		publicProcessHealth := r.URL.Path == "/admin/health" && origin == ""
		if !publicProcessHealth {
			if a.adminToken != "" {
				expected := "Bearer " + a.adminToken
				provided := r.Header.Get("Authorization")
				if len(provided) != len(expected) ||
					subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
					writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "管理端认证失败"})
					return
				}
			}
		}
		if r.Method == http.MethodPost {
			mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mediaType != "application/json" {
				writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "POST 只接受 application/json"})
				return
			}
		}
		next(w, r)
	}
}

// frames:SSE 协议帧观测流(测试页观测台)。先补发最近历史,再实时推。
func (a *API) frames(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "不支持流式"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	id, ch, backlog := a.hub.Frames().Subscribe()
	defer a.hub.Frames().Unsubscribe(id)

	writeEvent := func(e session.FrameEvent) bool {
		b, _ := json.Marshal(e)
		if _, err := w.Write(append(append([]byte("data: "), b...), '\n', '\n')); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	for _, e := range backlog {
		if !writeEvent(e) {
			return
		}
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-ch:
			if !ok || !writeEvent(e) {
				return
			}
		}
	}
}

func (a *API) suspects(w http.ResponseWriter, _ *http.Request) {
	recs, err := a.st.SuspectCmds()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	type view struct {
		MsgID                string `json:"msgId"`
		Name                 string `json:"name"`
		HandID               string `json:"handId"`
		Reason               string `json:"reason"`
		IdemKey              string `json:"idemKey"`
		ReviewReady          bool   `json:"reviewReady"`
		ReviewAfter          *int64 `json:"reviewAfter,omitempty"`
		VerificationAttempts int    `json:"verificationAttempts"`
	}
	out := make([]view, 0, len(recs))
	for _, r := range recs {
		ready, after := a.disp.SuspectReviewState(r)
		out = append(out, view{
			MsgID: r.MsgID, Name: r.Name, HandID: r.HandID, Reason: r.SuspectReason, IdemKey: r.IdemKey,
			ReviewReady: ready, ReviewAfter: after, VerificationAttempts: r.VerificationN,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"suspects": out})
}

func (a *API) verdict(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MsgID   string `json:"msgId"`
		Verdict string `json:"verdict"` // resolvedOk / resolvedFailed
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法请求体"})
		return
	}
	var v store.CmdStatus
	switch req.Verdict {
	case "resolvedOk":
		v = store.CmdResolvedOk
	case "resolvedFailed":
		v = store.CmdResolvedFailed
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "verdict 只能 resolvedOk / resolvedFailed"})
		return
	}
	if err := a.disp.Verdict(req.MsgID, v); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"msgId": req.MsgID, "verdict": req.Verdict})
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
		"activeHands": a.hub.ActiveHandIDs(),
	})
}

func (a *API) handHealth(w http.ResponseWriter, _ *http.Request) {
	states := a.hub.Registry().Snapshot()
	type view struct {
		HandID        string   `json:"handId"`
		Online        bool     `json:"online"`
		Health        string   `json:"health"`
		Caps          []string `json:"caps"`
		BootID        string   `json:"bootId"`
		ContractHash  string   `json:"contractHash"`
		ContractMatch bool     `json:"contractMatch"`
		ExtVersion    string   `json:"extensionVersion"`
		LastHbMs      int64    `json:"lastHbAgoMs"`
		WitnessReady  bool     `json:"witnessReady"`
		OutboxPending int      `json:"outboxPending"`
		JournalOpen   int      `json:"journalOpen"`
	}
	out := make([]view, 0, len(states))
	for _, s := range states {
		row := view{
			HandID: s.HandID, Online: s.Online, Health: string(s.Health),
			Caps: s.Caps, BootID: s.BootID, ContractHash: s.ContractHash,
			ContractMatch: s.ContractMatch, ExtVersion: s.ExtVersion,
			LastHbMs: time.Since(s.LastHbAt).Milliseconds(),
		}
		if witness, ok := a.hub.HandWitness(s.HandID); ok {
			row.WitnessReady = true
			row.OutboxPending = witness.OutboxPending
			row.JournalOpen = witness.JournalOpen
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"hands": out})
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
