package adminhttp

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"recruithelper/client/service/internal/store"
)

type sourcingLatestView struct {
	RunID                   string                 `json:"runId"`
	SourceLogicalDispatchID string                 `json:"sourceLogicalDispatchId"`
	ObservedAt              int64                  `json:"observedAt"`
	CapturedAt              string                 `json:"capturedAt"`
	SchemaVersion           int                    `json:"schemaVersion"`
	ContentHash             string                 `json:"contentHash"`
	ResumeBytes             int                    `json:"resumeBytes"`
	Score                   *sourcingScoreView     `json:"score,omitempty"`
	Selection               *sourcingSelectionView `json:"selection,omitempty"`
}

type sourcingScoreView struct {
	InvocationID        string `json:"invocationId"`
	Status              string `json:"status"`
	Score               *int   `json:"score,omitempty"`
	Provider            string `json:"provider"`
	Model               string `json:"model"`
	InputTokens         int    `json:"inputTokens"`
	CachedInputTokens   int    `json:"cachedInputTokens"`
	OutputTokens        int    `json:"outputTokens"`
	ErrorClass          string `json:"errorClass,omitempty"`
	EstimatedCostMicros int64  `json:"estimatedCostMicros"`
	StartedAt           string `json:"startedAt"`
	FinishedAt          string `json:"finishedAt,omitempty"`
}

type sourcingSelectionView struct {
	Outcome   string  `json:"outcome"`
	Score     *int    `json:"score,omitempty"`
	MinScore  int     `json:"minScore"`
	ProfileID *string `json:"profileId,omitempty"`
	DecidedAt string  `json:"decidedAt"`
}

type sourcingStatusView struct {
	Platform            string              `json:"platform"`
	AccountRef          string              `json:"accountRef"`
	Enabled             bool                `json:"enabled"`
	ContextRevisionHash string              `json:"contextRevisionHash,omitempty"`
	StartedAt           *time.Time          `json:"startedAt,omitempty"`
	LastAttemptAt       *time.Time          `json:"lastAttemptAt,omitempty"`
	LastErrorCode       string              `json:"lastErrorCode,omitempty"`
	CaptureCount        int64               `json:"captureCount"`
	Latest              *sourcingLatestView `json:"latest,omitempty"`
}

func (a *API) startSourcing(w http.ResponseWriter, r *http.Request) {
	if !a.requireActor(w) {
		return
	}
	var req struct {
		accountKeyRequest
		ContextRevisionHash string `json:"contextRevisionHash"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	key, err := validateAccountKey(req.Platform, req.AccountRef)
	req.ContextRevisionHash = strings.TrimSpace(req.ContextRevisionHash)
	if err != nil || req.ContextRevisionHash == "" || len(req.ContextRevisionHash) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的账号或职位配置 revision"})
		return
	}
	if err := a.actor.StartSourcing(key, req.ContextRevisionHash); err != nil {
		status := http.StatusConflict
		if errors.Is(err, store.ErrJobAIContextRevisionNotFound) {
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
	status, err := a.st.AccountSourcingStatus(key)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "采集状态读取失败"})
		return
	}
	if status == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "账号不存在"})
		return
	}
	view := sourcingStatusView{
		Platform: status.Platform, AccountRef: status.AccountRef, Enabled: status.Enabled,
		ContextRevisionHash: status.ContextRevisionHash, StartedAt: status.StartedAt,
		LastAttemptAt: status.LastAttemptAt, LastErrorCode: status.LastErrorCode,
		CaptureCount: status.CaptureCount,
	}
	if status.Latest != nil {
		view.Latest = &sourcingLatestView{
			RunID: status.Latest.RunID, SourceLogicalDispatchID: status.Latest.SourceLogicalDispatchID,
			ObservedAt: status.Latest.ObservedAt, CapturedAt: status.Latest.CapturedAt.Format("2006-01-02T15:04:05.000Z07:00"),
			SchemaVersion: status.Latest.SchemaVersion, ContentHash: status.Latest.ContentHash,
			ResumeBytes: status.Latest.ResumeBytes,
		}
		if status.Latest.Score != nil {
			score := status.Latest.Score
			finishedAt := ""
			if score.FinishedAt != nil {
				finishedAt = score.FinishedAt.Format("2006-01-02T15:04:05.000Z07:00")
			}
			view.Latest.Score = &sourcingScoreView{
				InvocationID: score.InvocationID, Status: string(score.Status), Score: score.Score,
				Provider: score.Provider, Model: score.Model,
				InputTokens: score.InputTokens, CachedInputTokens: score.CachedInputTokens,
				OutputTokens: score.OutputTokens, ErrorClass: score.ErrorClass,
				EstimatedCostMicros: score.EstimatedCostMicros,
				StartedAt:           score.StartedAt.Format("2006-01-02T15:04:05.000Z07:00"), FinishedAt: finishedAt,
			}
		}
		if status.Latest.Selection != nil {
			selection := status.Latest.Selection
			view.Latest.Selection = &sourcingSelectionView{
				Outcome: string(selection.Outcome), Score: selection.Score,
				MinScore: selection.MinScore, ProfileID: selection.ProfileID,
				DecidedAt: selection.DecidedAt.Format("2006-01-02T15:04:05.000Z07:00"),
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sourcing": view})
}
