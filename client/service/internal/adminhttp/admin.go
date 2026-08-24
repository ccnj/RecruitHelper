// Package adminhttp:本地管理端点。测试页与未来 UI 调的就是这些接口。
package adminhttp

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"mime"
	"net/http"
	"sync"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/jobclassreport"
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
	// jobClassReporter 是第十项出站(职位类别审计上报)的 fire-and-forget 上报器;
	// nil 即不上报(测试/未接线),两个调用点都以 nil 安全。
	jobClassReporter *jobclassreport.Reporter
	fieldReport      FieldReportDeps
	notifyProbe      NotifyProbeDeps
	chatReportRun    ChatReportRunner

	// adviceEngine 可运行期换代(模型配置落盘即生效,2026-08-12 甲方裁决),
	// 只经 SetAdvice/currentAdvice 访问。providerApplied 在模型配置任一落盘
	// 成功后被调用,由 main 装配为"重建引擎并换代"。
	adviceMu        sync.RWMutex
	adviceEngine    JobClassAdvisor
	providerApplied func()

	// smartProviderConfig 是发布专用「聪明ai」凭据的落盘(AGENTS.md「LLM provider
	// 直连」2026-08-24 增补),随职位配置同步从响应顶层 smartAi 块刷新;
	// smartProviderApplied 由 main 装配为"重建发布引擎并换代 adviceEngine"。
	// 两者可为 nil(测试构造不注入),刷新函数对 nil 安全。
	smartProviderConfig  *m5ai.ProviderConfigStore
	smartProviderApplied func()

	// subSmartProviderConfig/subSmartProviderApplied 是回复族专用「次聪明ai」
	// 的对应一对(同条款 2026-08-24 增补),来自响应顶层 subSmartAi 块;回调由
	// main 装配为"重建回复引擎并换进 patrol.SetReplyAdvice"。同样 nil 安全。
	subSmartProviderConfig  *m5ai.ProviderConfigStore
	subSmartProviderApplied func()
}

// SetProviderApplied 注入模型配置落盘后的引擎换代回调(装配期一次)。
func (a *API) SetProviderApplied(fn func()) *API {
	a.providerApplied = fn
	return a
}

func (a *API) notifyProviderApplied() {
	if a.providerApplied != nil {
		a.providerApplied()
	}
}

// SetSmartProviderStore 注入聪明ai配置落盘与其换代回调(装配期一次)。
func (a *API) SetSmartProviderStore(store *m5ai.ProviderConfigStore, onApplied func()) *API {
	a.smartProviderConfig = store
	a.smartProviderApplied = onApplied
	return a
}

// SetSubSmartProviderStore 注入次聪明ai配置落盘与其换代回调(装配期一次)。
func (a *API) SetSubSmartProviderStore(store *m5ai.ProviderConfigStore, onApplied func()) *API {
	a.subSmartProviderConfig = store
	a.subSmartProviderApplied = onApplied
	return a
}

func (a *API) SetJobConfigSource(source *jobconfig.Source) *API {
	a.jobConfigSource = source
	return a
}

// SetJobClassReporter 注入职位类别审计上报器(AGENTS.md 第十项出站,2026-08-23)。
func (a *API) SetJobClassReporter(reporter *jobclassreport.Reporter) *API {
	a.jobClassReporter = reporter
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
	maintenance := func(f http.HandlerFunc) http.HandlerFunc { return a.maintenanceGuard(f) }
	mux.HandleFunc("GET /admin/health", h(a.health))
	mux.HandleFunc("GET /admin/hands", h(a.hands))
	mux.HandleFunc("GET /admin/hands/health", h(a.handHealth))
	mux.HandleFunc("POST /admin/hands/reload", maintenance(a.reloadHand))
	mux.HandleFunc("POST /admin/cmd", h(a.postCmd))
	mux.HandleFunc("POST /admin/messages/send", h(a.sendMessage))
	mux.HandleFunc("POST /admin/cards/interview", h(a.sendInterviewCard))
	mux.HandleFunc("POST /admin/cards/interview/probe", h(a.probeInterviewEditor))
	mux.HandleFunc("POST /admin/notify/probe", h(a.notifyProbeSend))
	mux.HandleFunc("GET /admin/messages/send", h(a.sendMessageStatus))
	mux.HandleFunc("GET /admin/ledger", h(a.ledger))
	mux.HandleFunc("GET /admin/suspects", h(a.suspects))
	mux.HandleFunc("POST /admin/suspects/verdict", h(a.verdict))
	mux.HandleFunc("GET /admin/patrol/quarantines", h(a.patrolQuarantines))
	mux.HandleFunc("POST /admin/patrol/quarantines/clear", h(a.patrolQuarantineClear))
	mux.HandleFunc("GET /admin/frames", h(a.frames))
	mux.HandleFunc("GET /admin/accounts", h(a.accounts))
	mux.HandleFunc("POST /admin/accounts/bind", h(a.bindAccount))
	mux.HandleFunc("POST /admin/accounts/enable", h(a.enableAccount))
	mux.HandleFunc("POST /admin/accounts/stop", h(a.stopAccount))
	mux.HandleFunc("POST /admin/accounts/pause", h(a.pauseAccount))
	mux.HandleFunc("POST /admin/accounts/run", h(a.runAccount))
	mux.HandleFunc("POST /admin/conversations/current/process-once", h(a.processCurrentConversationOnce))
	mux.HandleFunc("POST /admin/candidates/current/read", h(a.readCurrentCandidate))
	mux.HandleFunc("POST /admin/candidates/current/select", h(a.selectCurrentCandidate))
	mux.HandleFunc("POST /admin/candidates/greeting/send", h(a.sendGreeting))
	mux.HandleFunc("GET /admin/candidates/greeting/send", h(a.sendGreetingStatus))
	mux.HandleFunc("POST /admin/m5/trial/select", h(a.selectM5Trial))
	mux.HandleFunc("POST /admin/m5/trial/recover-reply-budget", h(a.recoverM5ReplyBudget))
	mux.HandleFunc("GET /admin/m5/trial", h(a.m5TrialStatus))
	mux.HandleFunc("GET /admin/m5/provider-config", h(a.m5ProviderConfig))
	mux.HandleFunc("POST /admin/m5/provider-config", h(a.saveM5ProviderConfig))
	mux.HandleFunc("GET /admin/m5/contexts", h(a.m5Contexts))
	mux.HandleFunc("POST /admin/m5/contexts/import", h(a.importM5Contexts))
	mux.HandleFunc("POST /admin/dev/sql", h(a.devSQL))
	mux.HandleFunc("POST /admin/dev/report", h(a.devReport))
	mux.HandleFunc("GET /admin/dev/report/settings", h(a.devReportSettings))
	mux.HandleFunc("POST /admin/dev/report/settings", h(a.setDevReportSettings))
	mux.HandleFunc("GET /admin/dev/log-report/settings", h(a.devLogReportSettings))
	mux.HandleFunc("POST /admin/dev/chat-report/run", h(a.devChatReportRun))
	mux.HandleFunc("GET /admin/job-config/source", h(a.jobConfigSourceConfig))
	mux.HandleFunc("GET /admin/job-config/backend-jobs", h(a.backendJobs))
	mux.HandleFunc("POST /admin/job-publish/precheck", h(a.jobPublishPrecheck))
	mux.HandleFunc("POST /admin/job-publish/class-plan", h(a.jobPublishClassPlan))
	mux.HandleFunc("POST /admin/job-publish/keyword-plan", h(a.jobPublishKeywordPlan))
	mux.HandleFunc("POST /admin/job-publish/prepare-draft", h(a.jobPublishPrepareDraft))
	mux.HandleFunc("POST /admin/job-publish/publish", h(a.jobPublishPublish))
	mux.HandleFunc("POST /admin/job-publish/take-offline", h(a.jobTakeOffline))
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
	return a.guardWithMaintenanceException(next, false)
}

// maintenanceGuard 只给部署硬切换的插件重载编排放开 bearer。Origin、
// Content-Type 与路由本身仍由同一份管理面守卫约束。
func (a *API) maintenanceGuard(next http.HandlerFunc) http.HandlerFunc {
	return a.guardWithMaintenanceException(next, true)
}

func (a *API) guardWithMaintenanceException(
	next http.HandlerFunc,
	allowReloadWithoutBearer bool,
) http.HandlerFunc {
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
		publicReloadMaintenance := allowReloadWithoutBearer && r.URL.Path == "/admin/hands/reload"
		if !publicProcessHealth && !publicReloadMaintenance {
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
		Action               string `json:"action"`
		HandID               string `json:"handId"`
		Reason               string `json:"reason"`
		ReasonText           string `json:"reasonText"`
		IdemKey              string `json:"idemKey"`
		ReviewReady          bool   `json:"reviewReady"`
		ReviewAfter          *int64 `json:"reviewAfter,omitempty"`
		VerificationAttempts int    `json:"verificationAttempts"`

		// 裁决现场：谁、什么、何时、卡在哪。
		Platform        string `json:"platform"`
		AccountRef      string `json:"accountRef"`
		IntentID        string `json:"intentId"`
		ConversationRef string `json:"conversationRef"`
		PeerDisplayName string `json:"peerDisplayName"`
		Summary         string `json:"summary"`
		DispatchedAtMs  int64  `json:"dispatchedAtMs"`
		DeadlineMs      int64  `json:"deadlineMs"`
		ErrorCode       string `json:"errorCode"`
		SideEffect      string `json:"sideEffect"`

		// 原始现场。摘要挑不出来的线索都在这里，前端折叠显示。
		Args       string `json:"args"`
		Guards     string `json:"guards"`
		ResultBody string `json:"resultBody"`
	}
	out := make([]view, 0, len(recs))
	for _, r := range recs {
		ready, after := a.disp.SuspectReviewState(r)
		facts := argsFacts(r.Args)
		out = append(out, view{
			MsgID: r.MsgID, Name: r.Name, Action: suspectActionName(r.Name),
			HandID: r.HandID, Reason: r.SuspectReason, ReasonText: humanizeSuspectReason(r.SuspectReason),
			IdemKey: r.IdemKey, ReviewReady: ready, ReviewAfter: after, VerificationAttempts: r.VerificationN,

			Platform: r.Platform, AccountRef: r.AccountRef, IntentID: r.IntentID,
			ConversationRef: facts.ConversationRef,
			PeerDisplayName: a.peerDisplayNameFor(r, facts.ConversationRef),
			Summary:         facts.Summary(),
			DispatchedAtMs:  unixMilliOrZero(r.CreatedAt),
			DeadlineMs:      r.DeadlineMs,
			ErrorCode:       r.ErrorCode, SideEffect: r.SideEffect,

			Args: r.Args, Guards: r.Guards, ResultBody: r.ResultBody,
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

		// 扫读用：什么时候、对谁、花了多久。
		Target       string `json:"target"`
		Summary      string `json:"summary"`
		CreatedAtMs  int64  `json:"createdAtMs"`
		TerminalAtMs int64  `json:"terminalAtMs"`

		// 展开用：身份与判据。
		HandID        string `json:"handId"`
		IdemKey       string `json:"idemKey"`
		IntentID      string `json:"intentId"`
		Platform      string `json:"platform"`
		AccountRef    string `json:"accountRef"`
		SideEffect    string `json:"sideEffect"`
		SuspectReason string `json:"suspectReason"`
		DeadlineMs    int64  `json:"deadlineMs"`
		Args          string `json:"args"`
		Guards        string `json:"guards"`
		ResultBody    string `json:"resultBody"`
	}
	out := make([]view, 0, len(recs))
	for _, r := range recs {
		facts := argsFacts(r.Args)
		out = append(out, view{
			MsgID: r.MsgID, Name: r.Name, Class: r.Class, Status: string(r.Status),
			Attempt: r.Attempt, ErrorCode: r.ErrorCode,

			Target: a.cmdTarget(r, facts), Summary: facts.Summary(),
			CreatedAtMs: unixMilliOrZero(r.CreatedAt), TerminalAtMs: terminalMillis(r.TerminalAt),

			HandID: r.HandID, IdemKey: r.IdemKey, IntentID: r.IntentID,
			Platform: r.Platform, AccountRef: r.AccountRef,
			SideEffect: r.SideEffect, SuspectReason: r.SuspectReason, DeadlineMs: r.DeadlineMs,
			Args: r.Args, Guards: r.Guards, ResultBody: r.ResultBody,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ledger": out})
}

// writeError:诊断台错误响应统一收口——固定人话进 error,底层原因进 detail
// 并落一条日志。此前 err 既不进响应也不进日志,诊断台只见一句"不可读"。
// /admin 响应带明文已由「开发者诊断台明文边界」授权;这里记的是错误原因,
// 不是数据端点的响应正文。
func writeError(w http.ResponseWriter, code int, message string, err error) {
	slog.Warn("诊断台请求失败", "status", code, "msg", message, "err", err)
	writeJSON(w, code, map[string]string{"error": message, "detail": err.Error()})
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
