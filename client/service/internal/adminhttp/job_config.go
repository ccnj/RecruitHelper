package adminhttp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"recruithelper/client/service/internal/jobconfig"
	"recruithelper/client/service/internal/m5ai"
)

func (a *API) jobConfigSourceConfig(w http.ResponseWriter, r *http.Request) {
	if a.jobConfigSource == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "旧后台职位配置源尚未就绪"})
		return
	}
	view, err := a.jobConfigSource.Status(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "旧后台职位配置源读取失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": view})
}

func (a *API) activateJobConfigSource(w http.ResponseWriter, r *http.Request) {
	if a.jobConfigSource == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "旧后台职位配置源尚未就绪"})
		return
	}
	var request struct {
		BaseURL    string `json:"base_url"`
		InviteCode string `json:"invite_code"`
	}
	if decodeJSON(r, &request) != nil || strings.TrimSpace(request.InviteCode) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "旧后台激活请求无效"})
		return
	}
	result, err := a.jobConfigSource.Bind(r.Context(), request.BaseURL, request.InviteCode)
	if err != nil {
		var rejected *jobconfig.BindRejectedError
		switch {
		case errors.As(err, &rejected):
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "旧后台拒绝激活", "status": rejected.Status,
			})
		case errors.Is(err, jobconfig.ErrConfigInvalid):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "旧后台地址或激活码无效"})
		case errors.Is(err, jobconfig.ErrMachineIdentity):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "当前机器身份不可用"})
		default:
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "旧后台激活请求失败"})
		}
		return
	}
	contexts, syncFailure := a.syncCurrentJobConfigNow(r.Context())
	response := map[string]any{
		"activated": true, "synced": syncFailure == nil,
		"status": result.Status, "customer": result.Customer, "contexts": contexts,
	}
	if syncFailure != nil {
		// bind 已在旧后台生效且凭据已经安全落盘。这里必须返回成功形态，
		// 避免调用方误以为可以重用或替换一次性激活码；同步可单独重试。
		response["syncError"] = syncFailure.message
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *API) syncCurrentJobConfig(w http.ResponseWriter, r *http.Request) {
	if a.jobConfigSource == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "旧后台职位配置源尚未就绪"})
		return
	}
	var request struct{}
	if decodeJSON(r, &request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "职位配置同步请求无效"})
		return
	}
	views, failure := a.syncCurrentJobConfigNow(r.Context())
	if failure != nil {
		writeJSON(w, failure.status, map[string]string{"error": failure.message})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"contexts": views})
}

type jobConfigSyncFailure struct {
	status  int
	message string
}

func (a *API) syncCurrentJobConfigNow(ctx context.Context) ([]m5ContextView, *jobConfigSyncFailure) {
	raw, err := a.jobConfigSource.FetchCurrent(ctx)
	if err != nil {
		return nil, &jobConfigSyncFailure{
			status: http.StatusBadGateway, message: "旧后台当前职位配置读取失败",
		}
	}
	revisions, err := m5ai.ImportLegacyJobConfigFromBackend(raw, time.Now())
	if err != nil {
		return nil, &jobConfigSyncFailure{
			status: http.StatusConflict, message: "旧后台当前职位配置与本地执行约束不兼容",
		}
	}
	stored, err := a.st.SaveJobAIContextRevisions(revisions)
	if err != nil {
		return nil, &jobConfigSyncFailure{
			status: http.StatusConflict, message: "职位 AI 上下文未能原子导入",
		}
	}
	views := make([]m5ContextView, 0, len(stored))
	for _, revision := range stored {
		views = append(views, m5ContextView{
			ContextID: revision.ContextID, RevisionHash: revision.RevisionHash,
			DisplayName: revision.DisplayName, Environment: revision.Environment,
			MappingVersion: revision.Communication.MappingVersion,
			DocumentCount:  len(revision.SourcePackage.Documents), CreatedAt: revision.CreatedAt,
		})
	}
	return views, nil
}
