package adminhttp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"recruithelper/client/service/internal/patrol"
	"recruithelper/client/service/internal/store"
)

type sourcingStatusView struct {
	BatchID             string                    `json:"batchId"`
	ContextRevisionHash string                    `json:"contextRevisionHash"`
	BackendJobID        *string                   `json:"backendJobId,omitempty"`
	TargetCount         int                       `json:"targetCount"`
	CapturedCount       int64                     `json:"capturedCount"`
	RemainingCount      int                       `json:"remainingCount"`
	Status              store.SourcingBatchStatus `json:"status"`
	Reason              string                    `json:"reason,omitempty"`
	StartedAt           time.Time                 `json:"startedAt"`
	LastAttemptAt       *time.Time                `json:"lastAttemptAt,omitempty"`
	PositionBoundAt     *time.Time                `json:"positionBoundAt,omitempty"`
	EndedAt             *time.Time                `json:"endedAt,omitempty"`
}

type sourcingScoringStatusView struct {
	BatchID             string `json:"batchId"`
	ContextRevisionHash string `json:"contextRevisionHash"`
	TargetCount         int    `json:"targetCount"`
	OKCount             int64  `json:"okCount"`
	FailedCount         int64  `json:"failedCount"`
	InFlightCount       int64  `json:"inFlightCount"`
	PendingCount        int64  `json:"pendingCount"`
	Provider            string `json:"provider,omitempty"`
	Model               string `json:"model,omitempty"`
	Completed           bool   `json:"completed"`
}

type sourcingSelectionStatusView struct {
	BatchID             string    `json:"batchId"`
	ContextRevisionHash string    `json:"contextRevisionHash"`
	AlgorithmVersion    string    `json:"algorithmVersion"`
	MinScore            int       `json:"minScore"`
	TargetMin           int       `json:"targetMin"`
	TargetMax           int       `json:"targetMax"`
	TargetCount         int       `json:"targetCount"`
	MaleRatioLimit      int       `json:"maleRatioLimit"`
	MaleLimit           int       `json:"maleLimit"`
	PoolCount           int       `json:"poolCount"`
	EligibleCount       int       `json:"eligibleCount"`
	SelectedCount       int       `json:"selectedCount"`
	MaleSelectedCount   int       `json:"maleSelectedCount"`
	UnknownGenderCount  int       `json:"unknownGenderCount"`
	CompletedAt         time.Time `json:"completedAt"`
}

type sourcingGreetingGenerationStatusView struct {
	BatchID             string `json:"batchId"`
	ContextRevisionHash string `json:"contextRevisionHash"`
	SelectedCount       int    `json:"selectedCount"`
	OKCount             int64  `json:"okCount"`
	FailedCount         int64  `json:"failedCount"`
	InFlightCount       int64  `json:"inFlightCount"`
	PendingCount        int64  `json:"pendingCount"`
	Provider            string `json:"provider,omitempty"`
	Model               string `json:"model,omitempty"`
	InputTokens         int64  `json:"inputTokens"`
	CachedInputTokens   int64  `json:"cachedInputTokens"`
	OutputTokens        int64  `json:"outputTokens"`
	EstimatedCostMicros int64  `json:"estimatedCostMicros"`
	Completed           bool   `json:"completed"`
}

// sourcingGreetingSendStatusView 是列表直接发送管理面的完整且唯一投影。
// 成员身份、来源 invocation、effect intent 与正文只能留在业务库内。
type sourcingGreetingSendStatusView struct {
	BatchID             string `json:"batchId"`
	ContextRevisionHash string `json:"contextRevisionHash"`
	SelectedCount       int    `json:"selectedCount"`
	ReadyCount          int64  `json:"readyCount"`
	PendingCount        int64  `json:"pendingCount"`
	InFlightCount       int64  `json:"inFlightCount"`
	SentCount           int64  `json:"sentCount"`
	FailedCount         int64  `json:"failedCount"`
	SuspectCount        int64  `json:"suspectCount"`
	AbandonedCount      int64  `json:"abandonedCount"`
	Completed           bool   `json:"completed"`
}

func (a *API) startSourcing(w http.ResponseWriter, r *http.Request) {
	if !a.requireActor(w) {
		return
	}
	if a.jobConfigSource == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "旧后台职位配置源尚未就绪"})
		return
	}
	var req struct {
		accountKeyRequest
		TargetCount int `json:"targetCount"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	key, err := validateAccountKey(req.Platform, req.AccountRef)
	if err != nil || req.TargetCount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的账号或目标采集数"})
		return
	}
	contexts, syncFailure := a.syncCurrentJobConfigNow(r.Context())
	if syncFailure != nil {
		writeJSON(w, syncFailure.status, map[string]string{"error": syncFailure.message})
		return
	}
	if len(contexts) != 1 || strings.TrimSpace(contexts[0].RevisionHash) == "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "旧后台当前职位配置不是唯一可执行职位"})
		return
	}
	// 管理面显式启动的批次不分轮:采到显式 targetCount 即终局,与分轮前语义一致。
	if err := a.actor.StartSourcing(key, contexts[0].RevisionHash, req.TargetCount, 0); err != nil {
		status := http.StatusConflict
		if errors.Is(err, store.ErrJobAIContextRevisionNotFound) || errors.Is(err, store.ErrAccountNotFound) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	a.writeSourcingStatus(w, key)
}

func (a *API) stopSourcing(w http.ResponseWriter, r *http.Request) {
	if !a.requireActor(w) {
		return
	}
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
	if err := a.actor.StopSourcing(key); err != nil {
		status := http.StatusConflict
		if errors.Is(err, store.ErrSourcingBatchNotFound) || errors.Is(err, store.ErrAccountNotFound) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	a.writeSourcingStatus(w, key)
}

func (a *API) sourcingStatus(w http.ResponseWriter, r *http.Request) {
	key, err := keyFromQuery(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a.writeSourcingStatus(w, key)
}

func (a *API) writeSourcingStatus(w http.ResponseWriter, key store.AccountKey) {
	progress, err := a.st.LatestSourcingBatchProgress(key)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "采集状态读取失败"})
		return
	}
	if progress == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "尚无正式采集批次"})
		return
	}
	view := sourcingStatusView{
		BatchID: progress.BatchID, ContextRevisionHash: progress.ContextRevisionHash,
		BackendJobID: progress.BackendJobID,
		TargetCount:  progress.TargetCount, CapturedCount: progress.CapturedCount,
		RemainingCount: progress.RemainingCount, Status: progress.Status, Reason: progress.Reason,
		StartedAt: progress.StartedAt, LastAttemptAt: progress.LastAttemptAt,
		PositionBoundAt: progress.PositionBoundAt, EndedAt: progress.EndedAt,
	}
	writeJSON(w, http.StatusOK, map[string]any{"sourcing": view})
}

func (a *API) runSourcingScoring(w http.ResponseWriter, r *http.Request) {
	if !a.requireActor(w) {
		return
	}
	batchID, ok := sourcingBatchIDFromBody(w, r)
	if !ok {
		return
	}
	progress, err := a.actor.ScoreCompletedSourcingBatch(r.Context(), batchID)
	if err != nil {
		status := http.StatusConflict
		switch {
		case errors.Is(err, store.ErrSourcingBatchNotFound):
			status = http.StatusNotFound
		case errors.Is(err, patrol.ErrSourcingScoringProviderUnavailable):
			status = http.StatusServiceUnavailable
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			status = http.StatusRequestTimeout
		}
		body := map[string]any{"error": err.Error()}
		if progress != nil {
			body["sourcingScoring"] = sourcingScoringView(*progress)
		}
		writeJSON(w, status, body)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sourcingScoring": sourcingScoringView(*progress)})
}

func (a *API) sourcingScoringStatus(w http.ResponseWriter, r *http.Request) {
	batchID := strings.TrimSpace(r.URL.Query().Get("batchId"))
	if batchID == "" || len(batchID) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的正式采集 batchId"})
		return
	}
	progress, err := a.st.SourcingBatchScoringProgress(batchID)
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, store.ErrSourcingBatchNotFound) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sourcingScoring": sourcingScoringView(*progress)})
}

func sourcingBatchIDFromBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req struct {
		BatchID string `json:"batchId"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return "", false
	}
	batchID := strings.TrimSpace(req.BatchID)
	if batchID == "" || len(batchID) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的正式采集 batchId"})
		return "", false
	}
	return batchID, true
}

func sourcingScoringView(progress store.SourcingBatchScoringProgress) sourcingScoringStatusView {
	return sourcingScoringStatusView{
		BatchID: progress.BatchID, ContextRevisionHash: progress.ContextRevisionHash,
		TargetCount: progress.TargetCount, OKCount: progress.OKCount, FailedCount: progress.FailedCount,
		InFlightCount: progress.InFlightCount, PendingCount: progress.PendingCount,
		Provider: progress.Provider, Model: progress.Model, Completed: progress.Completed,
	}
}

func (a *API) runSourcingSelection(w http.ResponseWriter, r *http.Request) {
	if !a.requireActor(w) {
		return
	}
	batchID, ok := sourcingBatchIDFromBody(w, r)
	if !ok {
		return
	}
	selection, err := a.st.SelectCompletedSourcingBatch(batchID, time.Now())
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, store.ErrSourcingBatchNotFound) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sourcingSelection": sourcingSelectionView(*selection)})
}

func (a *API) sourcingSelectionStatus(w http.ResponseWriter, r *http.Request) {
	batchID := strings.TrimSpace(r.URL.Query().Get("batchId"))
	if batchID == "" || len(batchID) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的正式采集 batchId"})
		return
	}
	selection, err := a.st.SourcingBatchSelectionByBatchID(batchID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "筛选状态读取失败"})
		return
	}
	if selection == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "该批次尚无筛选结果"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sourcingSelection": sourcingSelectionView(*selection)})
}

func sourcingSelectionView(selection store.SourcingBatchSelection) sourcingSelectionStatusView {
	return sourcingSelectionStatusView{
		BatchID: selection.BatchID, ContextRevisionHash: selection.ContextRevisionHash,
		AlgorithmVersion: selection.AlgorithmVersion, MinScore: selection.MinScore,
		TargetMin: selection.TargetMin, TargetMax: selection.TargetMax, TargetCount: selection.TargetCount,
		MaleRatioLimit: selection.MaleRatioLimit, MaleLimit: selection.MaleLimit,
		PoolCount: selection.PoolCount, EligibleCount: selection.EligibleCount,
		SelectedCount: selection.SelectedCount, MaleSelectedCount: selection.MaleSelectedCount,
		UnknownGenderCount: selection.UnknownGenderCount, CompletedAt: selection.CompletedAt,
	}
}

func (a *API) runSourcingGreetingGeneration(w http.ResponseWriter, r *http.Request) {
	if !a.requireActor(w) {
		return
	}
	batchID, ok := sourcingBatchIDFromBody(w, r)
	if !ok {
		return
	}
	progress, err := a.actor.GenerateSelectedSourcingGreetings(r.Context(), batchID)
	if err != nil {
		status := http.StatusConflict
		switch {
		case errors.Is(err, store.ErrSourcingBatchNotFound):
			status = http.StatusNotFound
		case errors.Is(err, patrol.ErrSourcingGreetingProviderUnavailable):
			status = http.StatusServiceUnavailable
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			status = http.StatusRequestTimeout
		}
		body := map[string]any{"error": err.Error()}
		if progress != nil {
			body["sourcingGreetingGeneration"] = sourcingGreetingGenerationView(*progress)
		}
		writeJSON(w, status, body)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sourcingGreetingGeneration": sourcingGreetingGenerationView(*progress),
	})
}

func (a *API) sourcingGreetingGenerationStatus(w http.ResponseWriter, r *http.Request) {
	batchID := strings.TrimSpace(r.URL.Query().Get("batchId"))
	if batchID == "" || len(batchID) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的正式采集 batchId"})
		return
	}
	progress, err := a.st.SourcingBatchGreetingProgress(batchID)
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, store.ErrSourcingBatchNotFound) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sourcingGreetingGeneration": sourcingGreetingGenerationView(*progress),
	})
}

func sourcingGreetingGenerationView(
	progress store.SourcingBatchGreetingProgress,
) sourcingGreetingGenerationStatusView {
	return sourcingGreetingGenerationStatusView{
		BatchID: progress.BatchID, ContextRevisionHash: progress.ContextRevisionHash,
		SelectedCount: progress.SelectedCount, OKCount: progress.OKCount,
		FailedCount: progress.FailedCount, InFlightCount: progress.InFlightCount,
		PendingCount: progress.PendingCount, Provider: progress.Provider, Model: progress.Model,
		InputTokens: progress.InputTokens, CachedInputTokens: progress.CachedInputTokens,
		OutputTokens: progress.OutputTokens, EstimatedCostMicros: progress.EstimatedCostMicros,
		Completed: progress.Completed,
	}
}

func (a *API) runSourcingGreetingSend(w http.ResponseWriter, r *http.Request) {
	if !a.requireActor(w) {
		return
	}
	batchID, ok := sourcingBatchIDFromBody(w, r)
	if !ok {
		return
	}
	progress, err := a.actor.SendSelectedSourcingGreetings(r.Context(), batchID)
	if err != nil {
		status, message := sourcingGreetingSendHTTPError(err)
		body := map[string]any{"error": message}
		if progress != nil {
			body["sourcingGreetingSend"] = sourcingGreetingSendView(*progress)
		}
		writeJSON(w, status, body)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sourcingGreetingSend": sourcingGreetingSendView(*progress),
	})
}

func (a *API) sourcingGreetingSendStatus(w http.ResponseWriter, r *http.Request) {
	batchID := strings.TrimSpace(r.URL.Query().Get("batchId"))
	if batchID == "" || len(batchID) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的正式采集 batchId"})
		return
	}
	progress, err := a.st.SourcingBatchGreetingSendProgress(batchID)
	if err != nil {
		status, message := sourcingGreetingSendStatusHTTPError(err)
		writeJSON(w, status, map[string]string{"error": message})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sourcingGreetingSend": sourcingGreetingSendView(*progress),
	})
}

func sourcingGreetingSendView(
	progress store.SourcingBatchGreetingSendProgress,
) sourcingGreetingSendStatusView {
	return sourcingGreetingSendStatusView{
		BatchID: progress.BatchID, ContextRevisionHash: progress.ContextRevisionHash,
		SelectedCount: progress.SelectedCount, ReadyCount: progress.ReadyCount,
		PendingCount: progress.PendingCount, InFlightCount: progress.InFlightCount,
		SentCount: progress.SentCount, FailedCount: progress.FailedCount,
		SuspectCount: progress.SuspectCount, AbandonedCount: progress.AbandonedCount,
		Completed: progress.Completed,
	}
}

// 新发送管理面不直接返回底层 error。底层错误可能携带来源、命令或页面
// 绑定引用；管理端只需要稳定分类和同一份脱敏聚合。
func sourcingGreetingSendHTTPError(err error) (int, string) {
	switch {
	case errors.Is(err, store.ErrSourcingBatchNotFound):
		return http.StatusNotFound, "正式采集批次不存在"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return http.StatusRequestTimeout, "批次招呼发送请求已取消"
	default:
		return http.StatusConflict, "批次招呼发送未完成"
	}
}

func sourcingGreetingSendStatusHTTPError(err error) (int, string) {
	switch {
	case errors.Is(err, store.ErrSourcingBatchNotFound):
		return http.StatusNotFound, "正式采集批次不存在"
	case errors.Is(err, store.ErrSourcingGreetingEffectInvalid),
		errors.Is(err, store.ErrSourcingGreetingEffectConflict),
		errors.Is(err, store.ErrSourcingSelectionNotReady),
		errors.Is(err, store.ErrSourcingSelectionConflict):
		return http.StatusConflict, "批次招呼发送状态尚未就绪"
	default:
		return http.StatusInternalServerError, "批次招呼发送状态读取失败"
	}
}
