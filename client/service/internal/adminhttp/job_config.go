package adminhttp

import (
	"context"
	"errors"
	"log/slog"
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
		writeError(w, http.StatusInternalServerError, "旧后台职位配置源读取失败", err)
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
	if err := a.st.InvalidateCurrentLegacyJobAIContext(time.Now()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "旧职位配置未能安全退出当前激活",
		})
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

// backendJobs 只读投影旧后台该客户的启用职位，用于诊断面的发布参数总览。
// 刻意不经 m5ai 收编路径：那条路径会因为任何一个职位提示词不合格而整批失败，
// 且会写入不可变的 job_ai_context_revisions——看一眼列表不该产生业务事实。
func (a *API) backendJobs(w http.ResponseWriter, r *http.Request) {
	if a.jobConfigSource == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "旧后台职位配置源尚未就绪"})
		return
	}
	raw, err := a.jobConfigSource.FetchAll(r.Context())
	if err != nil {
		switch {
		case errors.Is(err, jobconfig.ErrConfigMissing):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "旧后台尚未激活，无法读取职位列表"})
		case errors.Is(err, jobconfig.ErrMachineMismatch):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "旧后台授权与当前机器不匹配"})
		case errors.Is(err, jobconfig.ErrMachineIdentity):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "当前机器身份不可用"})
		default:
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "旧后台职位列表读取失败"})
		}
		return
	}
	jobs, err := jobconfig.ParseBackendJobs(raw)
	if err != nil {
		writeError(w, http.StatusBadGateway, "旧后台职位列表格式不可识别", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
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
	syncedAt := time.Now()
	raw, err := a.jobConfigSource.FetchCurrent(ctx)
	if err != nil {
		slog.Warn("当前职位同步失败", "entry", "admin", "stage", "fetch", "error", err.Error())
		return nil, &jobConfigSyncFailure{
			status: http.StatusBadGateway, message: "旧后台当前职位配置读取失败",
		}
	}
	// provider 凭据刷新与职位配置导入是两条独立的失败面:凭据取不到不该挡住职位
	// 配置,职位文档不合法也不该让已经拿到的凭据落不了盘。
	m5ai.RefreshBackendProviderConfig(a.providerConfig, raw, a.notifyProviderApplied)
	m5ai.RefreshSmartProviderConfig(a.smartProviderConfig, raw, a.smartProviderApplied)
	m5ai.RefreshSubSmartProviderConfig(a.subSmartProviderConfig, raw, a.subSmartProviderApplied)
	revisions, err := m5ai.ImportLegacyJobConfigFromBackend(raw, syncedAt)
	if err != nil {
		// 导入错误只含文档类型名与占位符名,进日志不碰数据边界;新客户配置不合格
		// 时必须能从脑日志一眼定位缺什么,不靠人肉对后台。
		slog.Warn("当前职位同步失败", "entry", "admin", "stage", "import", "error", err.Error())
		return nil, &jobConfigSyncFailure{
			status: http.StatusConflict, message: "旧后台当前职位配置与本地执行约束不兼容",
		}
	}
	stored, err := a.st.SaveCurrentLegacyJobAIContext(revisions, syncedAt)
	if err != nil {
		return nil, &jobConfigSyncFailure{
			status: http.StatusConflict, message: "职位 AI 上下文未能原子导入: " + err.Error(),
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
