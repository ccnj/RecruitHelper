// Package apphttp exposes the loopback-only product UI projection surface.
// It is deliberately separate from the diagnostic /admin surface and never
// logs response bodies.
package apphttp

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
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
	AppCandidateDetail(string) (*store.AppCandidateDetailProjection, error)
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
	CurrentBatchID     string `json:"-"`
	WorkflowMode       string `json:"workflowMode,omitempty"`
	WorkflowStatus     string `json:"workflowStatus,omitempty"`
	CommunicationState string `json:"communicationState,omitempty"`
}

type RuntimeSnapshotProvider func(context.Context) (RuntimeSnapshot, error)

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

type API struct {
	projections ProjectionStore
	bearer      string
	runtime     RuntimeSnapshotProvider
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
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization")
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
	snapshot.CurrentBatchID = strings.TrimSpace(snapshot.CurrentBatchID)
	snapshot.WorkflowMode = strings.TrimSpace(snapshot.WorkflowMode)
	snapshot.WorkflowStatus = strings.TrimSpace(snapshot.WorkflowStatus)
	snapshot.CommunicationState = strings.TrimSpace(snapshot.CommunicationState)
	return snapshot
}

func (a *API) overview(w http.ResponseWriter, r *http.Request) {
	runtime := a.runtimeSnapshot(r.Context())
	overview, err := a.projections.AppOverview(store.AppOverviewRequest{
		Now: a.now(), CurrentBatchID: runtime.CurrentBatchID,
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
	projection, err := a.projections.AppCandidates(store.AppCandidateListQuery{
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
	projection, err := a.projections.AppCandidateDetail(profileID)
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
