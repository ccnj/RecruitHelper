package adminhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"recruithelper/client/service/internal/session"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

type accountKeyRequest struct {
	Platform   string `json:"platform"`
	AccountRef string `json:"accountRef"`
}

type latestRoundView struct {
	RoundID         string     `json:"roundId"`
	Trigger         string     `json:"trigger"`
	Status          string     `json:"status"`
	Stage           string     `json:"stage"`
	NewMessageCount int        `json:"newMessageCount"`
	ErrorCode       string     `json:"errorCode,omitempty"`
	StartedAt       time.Time  `json:"startedAt"`
	FinishedAt      *time.Time `json:"finishedAt"`
}

type accountView struct {
	Platform         string           `json:"platform"`
	AccountRef       string           `json:"accountRef"`
	HandID           string           `json:"handId"`
	HandOnline       bool             `json:"handOnline"`
	IdentityState    string           `json:"identityState"`
	EnabledToday     bool             `json:"enabledToday"`
	EnabledDate      string           `json:"enabledDate"`
	PausedReason     string           `json:"pausedReason,omitempty"`
	NextPatrolAt     *time.Time       `json:"nextPatrolAt"`
	LastPatrolAt     *time.Time       `json:"lastPatrolAt"`
	ManualQuietUntil *time.Time       `json:"manualQuietUntil"`
	DirtyHint        bool             `json:"dirtyHint"`
	PageHealth       string           `json:"pageHealth"`
	SensorHealth     string           `json:"sensorHealth"`
	UnreadTotal      *int             `json:"unreadTotal"`
	LatestRound      *latestRoundView `json:"latestRound"`
}

func (a *API) accounts(w http.ResponseWriter, _ *http.Request) {
	rows, err := a.st.Accounts()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]accountView, 0, len(rows))
	for i := range rows {
		view, viewErr := a.accountView(rows[i])
		if viewErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": viewErr.Error()})
			return
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out})
}

func (a *API) accountView(account store.Account) (accountView, error) {
	view := accountView{
		Platform: account.Platform, AccountRef: account.AccountRef, HandID: account.BoundHandID,
		IdentityState: string(account.IdentityState), EnabledDate: account.EnabledDate,
		PausedReason: account.PausedReason, NextPatrolAt: account.NextPatrolAt,
		LastPatrolAt: account.LastPatrolAt, ManualQuietUntil: account.ManualQuietUntil,
		DirtyHint: account.DirtyHint, PageHealth: string(session.CapabilityUnknown),
		SensorHealth: string(session.CapabilityUnknown),
	}
	view.EnabledToday = account.EnabledDate == time.Now().In(time.Local).Format("2006-01-02") &&
		account.EnabledAt != nil && account.StoppedAt == nil && account.PausedReason == ""
	if state, ok := a.hub.Registry().Get(account.BoundHandID); ok {
		view.HandOnline = state.Online
		view.PageHealth = string(state.PageHealth)
		view.SensorHealth = string(state.SensorHealth)
		if state.Sensors != nil && state.Sensors.UnreadTotal != nil {
			value := state.Sensors.UnreadTotal.Value
			view.UnreadTotal = &value
		}
	}
	rounds, err := a.st.RecentPatrolRounds(store.AccountKey{
		Platform: account.Platform, AccountRef: account.AccountRef,
	}, 1)
	if err != nil {
		return accountView{}, err
	}
	if len(rounds) != 0 {
		round := rounds[0]
		view.LatestRound = &latestRoundView{
			RoundID: round.RoundID, Trigger: round.Trigger, Status: round.Status,
			Stage: round.Stage, NewMessageCount: round.NewMessageCount,
			ErrorCode: round.ErrorCode, StartedAt: round.StartedAt, FinishedAt: round.FinishedAt,
		}
	}
	return view, nil
}

func (a *API) bindAccount(w http.ResponseWriter, r *http.Request) {
	if a.probe == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "账号探测器尚未就绪"})
		return
	}
	var req struct {
		Platform   string `json:"platform"`
		HandID     string `json:"handId"`
		AccountRef string `json:"accountRef"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	platform, err := validatePlatform(req.Platform)
	req.HandID = strings.TrimSpace(req.HandID)
	req.AccountRef = strings.TrimSpace(req.AccountRef)
	if err != nil || req.HandID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的平台标识或在线手标识"})
		return
	}
	req.Platform = platform
	if req.AccountRef != "" {
		if _, err := validateAccountKey(req.Platform, req.AccountRef); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	sessionID, bootID, online := a.hub.HandSession(req.HandID)
	if !online {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "所选手不在线"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	probe, err := a.probe.Probe(ctx, req.HandID)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "无法探测当前平台账号: " + err.Error()})
		return
	}
	if probe.LoginState != protocol.LoginStateIn || !probe.ContentScriptOk ||
		probe.PrincipalFingerprint == nil || *probe.PrincipalFingerprint == "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "请先在所选 Chrome 手中登录目标平台并打开可识别页面"})
		return
	}
	reusePrincipal := req.AccountRef == ""
	if req.AccountRef == "" {
		req.AccountRef = ids.NewAccountRef()
	}
	var bound *store.Account
	key := store.AccountKey{Platform: req.Platform, AccountRef: req.AccountRef}
	var current bool
	if a.actor != nil {
		bound, _, current, err = a.actor.BindAccountObservationIfCurrent(
			key, req.HandID, *probe.PrincipalFingerprint, sessionID, bootID, time.Now(), reusePrincipal,
			func(commit func() error) (bool, error) {
				return a.hub.WithCurrentHandSession(req.HandID, sessionID, bootID, commit)
			},
		)
	} else {
		// 仅供 handler 单元测试的最小构造；生产 main 始终注入 actor。
		current, err = a.hub.WithCurrentHandSession(req.HandID, sessionID, bootID, func() error {
			var bindErr error
			bound, _, bindErr = a.st.BindAccountObservation(
				key, req.HandID, *probe.PrincipalFingerprint, sessionID, bootID, time.Now(), reusePrincipal,
			)
			return bindErr
		})
	}
	if !current {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "探测期间手已重连，请重新确认当前账号"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	view, err := a.accountView(*bound)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": view})
}

func (a *API) enableAccount(w http.ResponseWriter, r *http.Request) {
	if !a.requireActor(w) {
		return
	}
	a.accountAction(w, r, a.actor.EnableToday)
}

func (a *API) stopAccount(w http.ResponseWriter, r *http.Request) {
	if !a.requireActor(w) {
		return
	}
	a.accountAction(w, r, a.actor.StopToday)
}

func (a *API) pauseAccount(w http.ResponseWriter, r *http.Request) {
	if !a.requireActor(w) {
		return
	}
	a.accountAction(w, r, a.actor.PauseNow)
}

func (a *API) runAccount(w http.ResponseWriter, r *http.Request) {
	if !a.requireActor(w) {
		return
	}
	a.accountAction(w, r, a.actor.RequestImmediate)
}

func (a *API) accountAction(w http.ResponseWriter, r *http.Request, action func(store.AccountKey) error) {
	var req accountKeyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	key, err := validateAccountKey(req.Platform, req.AccountRef)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := action(key); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) requireActor(w http.ResponseWriter) bool {
	if a.actor != nil {
		return true
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "账号 actor 尚未就绪"})
	return false
}

func (a *API) conversations(w http.ResponseWriter, r *http.Request) {
	key, err := keyFromQuery(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	rows, err := a.st.ConversationsForAccount(key)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	type view struct {
		ConversationRef      string              `json:"conversationRef"`
		PeerDisplayName      string              `json:"peerDisplayName"`
		UnreadCount          int                 `json:"unreadCount"`
		LastMessageDirection string              `json:"lastMessageDirection"`
		LastMessageKind      string              `json:"lastMessageKind"`
		LastMessagePreview   string              `json:"lastMessagePreview"`
		LastActivityMs       *int64              `json:"lastActivityMs"`
		TrackingState        store.TrackingState `json:"trackingState"`
		AdoptedBoundarySeq   int64               `json:"adoptedBoundarySeq"`
		LastMessageSeq       int64               `json:"lastMessageSeq"`
		LastSyncedAt         *time.Time          `json:"lastSyncedAt"`
	}
	out := make([]view, 0, len(rows))
	for _, row := range rows {
		out = append(out, view{
			ConversationRef: row.ConversationRef, PeerDisplayName: row.PeerDisplayName,
			UnreadCount: row.UnreadCount, LastMessageDirection: row.LastMessageDirection,
			LastMessageKind: row.LastMessageKind, LastMessagePreview: row.LastMessagePreview,
			LastActivityMs: row.LastActivityMs, TrackingState: row.TrackingState,
			AdoptedBoundarySeq: row.AdoptedBoundarySeq, LastMessageSeq: row.LastMessageSeq,
			LastSyncedAt: row.LastSyncedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": out})
}

func (a *API) trackConversation(w http.ResponseWriter, r *http.Request) {
	if !a.requireActor(w) {
		return
	}
	var req struct {
		accountKeyRequest
		ConversationRef string `json:"conversationRef"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	key, err := validateAccountKey(req.Platform, req.AccountRef)
	req.ConversationRef = strings.TrimSpace(req.ConversationRef)
	if err != nil || req.ConversationRef == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的账号或会话标识"})
		return
	}
	intent, err := a.st.TrackConversation(store.ConversationKey{
		Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: req.ConversationRef,
	}, "operator", time.Now())
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	_ = a.actor.RequestImmediate(key)
	writeJSON(w, http.StatusOK, map[string]any{"trackingState": intent.Status})
}

func (a *API) messages(w http.ResponseWriter, r *http.Request) {
	key, err := keyFromQuery(r)
	conversationRef := r.URL.Query().Get("conversationRef")
	if err != nil || conversationRef == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的账号或会话标识"})
		return
	}
	rows, err := a.st.RecentMessagesForConversation(store.ConversationKey{
		Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: conversationRef,
	}, 200)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	type view struct {
		Seq              int64   `json:"seq"`
		Direction        string  `json:"direction"`
		Kind             string  `json:"kind"`
		Text             *string `json:"text"`
		CardType         string  `json:"cardType"`
		CardState        string  `json:"cardState"`
		TsApproxMs       *int64  `json:"tsApproxMs"`
		Origin           string  `json:"origin"`
		FirstSeenRoundID string  `json:"firstSeenRoundId"`
	}
	out := make([]view, 0, len(rows))
	for _, row := range rows {
		out = append(out, view{
			Seq: row.Seq, Direction: row.Direction, Kind: row.Kind, Text: row.Text,
			CardType: row.CardType, CardState: row.CardState, TsApproxMs: row.TsApproxMs,
			Origin: row.Origin, FirstSeenRoundID: row.FirstSeenRoundID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": out})
}

func (a *API) audits(w http.ResponseWriter, r *http.Request) {
	key, err := keyFromQuery(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	rows, err := a.st.AuditEntries(500)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	type view struct {
		ID              uint      `json:"id"`
		At              time.Time `json:"at"`
		Category        string    `json:"category"`
		ConversationRef string    `json:"conversationRef"`
		RoundID         string    `json:"roundId"`
		Detail          string    `json:"detail"`
	}
	out := make([]view, 0, 50)
	for _, row := range rows {
		if row.Platform != key.Platform || row.AccountRef != key.AccountRef {
			continue
		}
		out = append(out, view{
			ID: row.ID, At: row.At, Category: row.Category,
			ConversationRef: row.ConversationRef, RoundID: row.RoundID, Detail: row.Detail,
		})
		if len(out) == 50 {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"audits": out})
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64*1024+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("非法请求体: " + err.Error())
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("请求体只能包含一个 JSON 对象")
		}
		return errors.New("非法请求体: " + err.Error())
	}
	return nil
}

func validateAccountKey(platform, accountRef string) (store.AccountKey, error) {
	platform, err := validatePlatform(platform)
	if err != nil {
		return store.AccountKey{}, errors.New("缺少有效的平台或账号标识")
	}
	accountRef = strings.TrimSpace(accountRef)
	if accountRef == "" || utf8.RuneCountInString(accountRef) > 256 {
		return store.AccountKey{}, errors.New("缺少有效的平台或账号标识")
	}
	return store.AccountKey{Platform: platform, AccountRef: accountRef}, nil
}

// platform 对脑是不透明的路由键。这里仅实施协议的通用字符串边界，
// 不维护平台枚举；首次绑定由管理面传入，后续动作从已落账账号上下文透传。
func validatePlatform(platform string) (string, error) {
	platform = strings.TrimSpace(platform)
	if platform == "" || utf8.RuneCountInString(platform) > 64 {
		return "", errors.New("缺少有效的平台标识")
	}
	return platform, nil
}

func keyFromQuery(r *http.Request) (store.AccountKey, error) {
	return validateAccountKey(r.URL.Query().Get("platform"), r.URL.Query().Get("accountRef"))
}
