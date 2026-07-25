// Package apphttp exposes the loopback-only product UI projection surface.
// It is deliberately separate from the diagnostic /admin surface and never
// logs response bodies.
package apphttp

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"recruithelper/client/service/internal/store"
)

var ErrInvalidConfiguration = errors.New("产品 API 配置无效")

type ProjectionStore interface {
	AppOverview(store.AppOverviewRequest) (*store.AppOverviewProjection, error)
	AppConfirmation(string) (*store.AppConfirmationProjection, error)
	AppCandidates(store.AppCandidateListQuery) (*store.AppCandidateListProjection, error)
	AppCandidateDetail(store.AppCandidateDetailQuery) (*store.AppCandidateDetailProjection, error)
}

type RuntimeSnapshot struct {
	Available          bool   `json:"available"`
	CustomerName       string `json:"customerName,omitempty"`
	CustomerStatus     string `json:"customerStatus,omitempty"`
	Authorized         bool   `json:"authorized"`
	ProviderConfigured bool   `json:"providerConfigured"`
	Provider           string `json:"provider,omitempty"`
	Model              string `json:"model,omitempty"`
	PluginOnline       bool   `json:"pluginOnline"`
	PluginHealth       string `json:"pluginHealth,omitempty"`
	PluginVersion      string `json:"pluginVersion,omitempty"`
	ContractMatch      bool   `json:"contractMatch"`
	Platform           string `json:"-"`
	AccountRef         string `json:"-"`
	CurrentBatchID     string `json:"-"`
	WorkflowMode       string `json:"workflowMode,omitempty"`
	WorkflowStatus     string `json:"workflowStatus,omitempty"`
	CanAddBatch        bool   `json:"canAddBatch"`
	CommunicationState string `json:"communicationState,omitempty"`
}

type RuntimeSnapshotProvider func(context.Context) (RuntimeSnapshot, error)

// WorkflowControl is the ordinary user's narrow write surface. Implementations
// remain in the brain and must reuse the production workflow and effect paths;
// apphttp only validates and forwards user intent.
type WorkflowControl interface {
	Start(context.Context, string, string) error
	Pause(context.Context) error
	Resume(context.Context) error
	ConfirmAll(context.Context, string, []string) error
}

type Option func(*API)

func WithRuntimeSnapshotProvider(provider RuntimeSnapshotProvider) Option {
	return func(api *API) { api.runtime = provider }
}

func WithClock(clock func() time.Time) Option {
	return func(api *API) {
		if clock != nil {
			api.now = clock
		}
	}
}

func WithWorkflowControl(control WorkflowControl) Option {
	return func(api *API) { api.control = control }
}

type API struct {
	projections ProjectionStore
	bearer      string
	runtime     RuntimeSnapshotProvider
	control     WorkflowControl
	now         func() time.Time
}

func New(projections ProjectionStore, bearer string, options ...Option) (*API, error) {
	bearer = strings.TrimSpace(bearer)
	if projections == nil || bearer == "" || len(bearer) < 16 || len(bearer) > 512 {
		return nil, ErrInvalidConfiguration
	}
	api := &API{projections: projections, bearer: bearer, now: time.Now}
	for _, option := range options {
		if option != nil {
			option(api)
		}
	}
	return api, nil
}

func (a *API) Routes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	h := func(next http.HandlerFunc) http.HandlerFunc { return a.guard(next) }
	mux.HandleFunc("GET /app/overview", h(a.overview))
	mux.HandleFunc("GET /app/confirmation", h(a.confirmation))
	mux.HandleFunc("POST /app/workflow/start", h(a.startWorkflow))
	mux.HandleFunc("POST /app/workflow/pause", h(a.pauseWorkflow))
	mux.HandleFunc("POST /app/workflow/resume", h(a.resumeWorkflow))
	mux.HandleFunc("POST /app/confirmation/send", h(a.confirmAll))
	mux.HandleFunc("GET /app/candidates", h(a.candidates))
	mux.HandleFunc("GET /app/candidates/{profileId}", h(a.candidateDetail))
	mux.HandleFunc("OPTIONS /app/", h(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
}

func (a *API) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !requestFromLoopback(r.RemoteAddr) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "产品 API 仅接受本机请求"})
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" && !trustedProductOrigin(origin) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "不受信任的产品端 Origin"})
			return
		}
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		}
		// 浏览器的 CORS preflight 不携带 Authorization；只在已经证明请求
		// 来自 loopback 且 Origin 属于产品壳后放行，实际 GET 仍必须持 bearer。
		if r.Method == http.MethodOptions {
			next(w, r)
			return
		}
		expected := "Bearer " + a.bearer
		provided := r.Header.Get("Authorization")
		if len(provided) != len(expected) ||
			subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "产品端认证失败"})
			return
		}
		next(w, r)
	}
}

func (a *API) startWorkflow(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Mode         string `json:"mode"`
		BackendJobID string `json:"backendJobId,omitempty"`
	}
	if decodeProductJSON(r, &request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "工作流启动请求无效"})
		return
	}
	request.Mode = strings.TrimSpace(request.Mode)
	request.BackendJobID = strings.TrimSpace(request.BackendJobID)
	switch request.Mode {
	case "full":
		if request.BackendJobID == "" || len(request.BackendJobID) > 128 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "工作流启动请求无效"})
			return
		}
	case "replyOnly":
		if request.BackendJobID != "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "工作流启动请求无效"})
			return
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "工作流启动请求无效"})
		return
	}
	if a.control == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "工作流控制尚未就绪"})
		return
	}
	if err := a.control.Start(r.Context(), request.Mode, request.BackendJobID); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "当前状态无法启动工作流"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

func (a *API) pauseWorkflow(w http.ResponseWriter, r *http.Request) {
	if decodeEmptyProductJSON(r) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "暂停请求无效"})
		return
	}
	if a.control == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "工作流控制尚未就绪"})
		return
	}
	if err := a.control.Pause(r.Context()); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "当前状态无法暂停工作流"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

func (a *API) resumeWorkflow(w http.ResponseWriter, r *http.Request) {
	if decodeEmptyProductJSON(r) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "恢复请求无效"})
		return
	}
	if a.control == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "工作流控制尚未就绪"})
		return
	}
	if err := a.control.Resume(r.Context()); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "当前状态无法恢复工作流"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

func (a *API) confirmAll(w http.ResponseWriter, r *http.Request) {
	var request struct {
		BatchID    string   `json:"batchId"`
		ProfileIDs []string `json:"profileIds"`
	}
	if decodeProductJSON(r, &request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "候选确认请求无效"})
		return
	}
	request.BatchID = strings.TrimSpace(request.BatchID)
	if request.BatchID == "" || len(request.BatchID) > 128 ||
		len(request.ProfileIDs) == 0 || len(request.ProfileIDs) > 1000 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "候选确认请求无效"})
		return
	}
	seen := make(map[string]struct{}, len(request.ProfileIDs))
	for index := range request.ProfileIDs {
		request.ProfileIDs[index] = strings.TrimSpace(request.ProfileIDs[index])
		if request.ProfileIDs[index] == "" || len(request.ProfileIDs[index]) > 128 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "候选确认请求无效"})
			return
		}
		if _, exists := seen[request.ProfileIDs[index]]; exists {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "候选确认请求含重复候选人"})
			return
		}
		seen[request.ProfileIDs[index]] = struct{}{}
	}
	if a.control == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "候选确认控制尚未就绪"})
		return
	}
	if err := a.control.ConfirmAll(r.Context(), request.BatchID, request.ProfileIDs); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "当前批次无法确认发送"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

func decodeEmptyProductJSON(r *http.Request) error {
	var request struct{}
	return decodeProductJSON(r, &request)
}

func decodeProductJSON(r *http.Request, target any) error {
	if r == nil || r.Body == nil {
		return errors.New("请求体缺失")
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("请求体包含多余内容")
	}
	return nil
}

func requestFromLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func trustedProductOrigin(origin string) bool {
	return origin == "null" ||
		origin == "http://127.0.0.1:5273" ||
		origin == "http://localhost:5273"
}

func (a *API) runtimeSnapshot(ctx context.Context) RuntimeSnapshot {
	if a.runtime == nil {
		return RuntimeSnapshot{}
	}
	snapshot, err := a.runtime(ctx)
	if err != nil {
		return RuntimeSnapshot{}
	}
	snapshot.CustomerName = strings.TrimSpace(snapshot.CustomerName)
	snapshot.CustomerStatus = strings.TrimSpace(snapshot.CustomerStatus)
	snapshot.Provider = strings.TrimSpace(snapshot.Provider)
	snapshot.Model = strings.TrimSpace(snapshot.Model)
	snapshot.PluginHealth = strings.TrimSpace(snapshot.PluginHealth)
	snapshot.PluginVersion = strings.TrimSpace(snapshot.PluginVersion)
	snapshot.Platform = strings.TrimSpace(snapshot.Platform)
	snapshot.AccountRef = strings.TrimSpace(snapshot.AccountRef)
	snapshot.CurrentBatchID = strings.TrimSpace(snapshot.CurrentBatchID)
	snapshot.WorkflowMode = strings.TrimSpace(snapshot.WorkflowMode)
	snapshot.WorkflowStatus = strings.TrimSpace(snapshot.WorkflowStatus)
	snapshot.CommunicationState = strings.TrimSpace(snapshot.CommunicationState)
	return snapshot
}

func (a *API) overview(w http.ResponseWriter, r *http.Request) {
	runtime := a.runtimeSnapshot(r.Context())
	if runtime.Platform == "" || runtime.AccountRef == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"overview": store.AppOverviewProjection{
				Job:             store.AppJobProjection{SyncStatus: "missing"},
				TodayInterviews: []store.AppInterviewSummary{},
				RefreshedAt:     a.now(),
			},
			"runtime": runtime,
		})
		return
	}
	overview, err := a.projections.AppOverview(store.AppOverviewRequest{
		Now: a.now(), CurrentBatchID: runtime.CurrentBatchID,
		Platform: runtime.Platform, AccountRef: runtime.AccountRef,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "首页数据读取失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"overview": overview,
		"runtime":  runtime,
	})
}

func (a *API) confirmation(w http.ResponseWriter, r *http.Request) {
	runtime := a.runtimeSnapshot(r.Context())
	batchID := runtime.CurrentBatchID
	if batchID == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"confirmation": store.AppConfirmationProjection{
				Candidates: []store.AppConfirmationCandidate{},
				Reason:     "workflowBatchUnavailable",
			},
		})
		return
	}
	if len(batchID) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "批次标识无效"})
		return
	}
	confirmation, err := a.projections.AppConfirmation(batchID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrAppProjectionInvalid) {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]string{"error": "候选确认数据读取失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"confirmation": confirmation})
}

func (a *API) candidates(w http.ResponseWriter, r *http.Request) {
	runtime := a.runtimeSnapshot(r.Context())
	view := store.AppCandidateView(strings.TrimSpace(r.URL.Query().Get("view")))
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	if utf8.RuneCountInString(search) > 100 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "搜索内容过长"})
		return
	}
	limit, ok := optionalNonNegativeInt(r.URL.Query().Get("limit"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "分页参数无效"})
		return
	}
	offset, ok := optionalNonNegativeInt(r.URL.Query().Get("offset"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "分页参数无效"})
		return
	}
	switch view {
	case store.AppCandidateViewCommunicating, store.AppCandidateViewPending,
		store.AppCandidateViewInterviewed, store.AppCandidateViewWechat:
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "候选人视图无效"})
		return
	}
	if limit > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "分页参数无效"})
		return
	}
	if runtime.Platform == "" || runtime.AccountRef == "" {
		if limit == 0 {
			limit = 50
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"candidates": store.AppCandidateListProjection{
				View: view, Items: []store.AppCandidateListItem{}, Limit: limit, Offset: offset,
			},
		})
		return
	}
	projection, err := a.projections.AppCandidates(store.AppCandidateListQuery{
		Platform: runtime.Platform, AccountRef: runtime.AccountRef,
		View: view, Search: search, Limit: limit, Offset: offset,
	})
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrAppProjectionInvalid) {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]string{"error": "候选人列表读取失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": projection})
}

func optionalNonNegativeInt(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, true
	}
	value, err := strconv.Atoi(raw)
	return value, err == nil && value >= 0
}

func (a *API) candidateDetail(w http.ResponseWriter, r *http.Request) {
	profileID := strings.TrimSpace(r.PathValue("profileId"))
	if profileID == "" || len(profileID) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "档案标识无效"})
		return
	}
	runtime := a.runtimeSnapshot(r.Context())
	if runtime.Platform == "" || runtime.AccountRef == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "候选人详情读取失败"})
		return
	}
	projection, err := a.projections.AppCandidateDetail(store.AppCandidateDetailQuery{
		Platform: runtime.Platform, AccountRef: runtime.AccountRef, ProfileID: profileID,
	})
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, store.ErrAppCandidateNotFound):
			status = http.StatusNotFound
		case errors.Is(err, store.ErrAppProjectionInvalid):
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]string{"error": "候选人详情读取失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidate": projection})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
