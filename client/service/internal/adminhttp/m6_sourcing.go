package adminhttp

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"recruithelper/client/service/internal/store"
)

type sourcingStatusView struct {
	BatchID             string                    `json:"batchId"`
	ContextRevisionHash string                    `json:"contextRevisionHash"`
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

func (a *API) startSourcing(w http.ResponseWriter, r *http.Request) {
	if !a.requireActor(w) {
		return
	}
	var req struct {
		accountKeyRequest
		ContextRevisionHash string `json:"contextRevisionHash"`
		TargetCount         int    `json:"targetCount"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	key, err := validateAccountKey(req.Platform, req.AccountRef)
	req.ContextRevisionHash = strings.TrimSpace(req.ContextRevisionHash)
	if err != nil || req.ContextRevisionHash == "" || len(req.ContextRevisionHash) > 128 || req.TargetCount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的账号、职位配置 revision 或目标采集数"})
		return
	}
	if err := a.actor.StartSourcing(key, req.ContextRevisionHash, req.TargetCount); err != nil {
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
		TargetCount: progress.TargetCount, CapturedCount: progress.CapturedCount,
		RemainingCount: progress.RemainingCount, Status: progress.Status, Reason: progress.Reason,
		StartedAt: progress.StartedAt, LastAttemptAt: progress.LastAttemptAt,
		PositionBoundAt: progress.PositionBoundAt, EndedAt: progress.EndedAt,
	}
	writeJSON(w, http.StatusOK, map[string]any{"sourcing": view})
}
