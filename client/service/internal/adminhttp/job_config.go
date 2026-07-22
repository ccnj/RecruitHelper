package adminhttp

import (
	"net/http"
	"strings"
	"time"

	"recruithelper/client/service/internal/jobconfig"
	"recruithelper/client/service/internal/m5ai"
)

func (a *API) jobConfigSourceConfig(w http.ResponseWriter, _ *http.Request) {
	if a.jobConfigSource == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "旧后台职位配置源尚未就绪"})
		return
	}
	config, err := a.jobConfigSource.LoadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "旧后台职位配置源读取失败"})
		return
	}
	if config == nil {
		writeJSON(w, http.StatusOK, map[string]any{"config": jobconfig.Config{}.View()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": config.View()})
}

func (a *API) saveJobConfigSourceConfig(w http.ResponseWriter, r *http.Request) {
	if a.jobConfigSource == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "旧后台职位配置源尚未就绪"})
		return
	}
	var request struct {
		BaseURL      string `json:"base_url"`
		MachineID    string `json:"machine_id"`
		LicenseToken string `json:"license_token"`
	}
	if decodeJSON(r, &request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "旧后台职位配置源请求无效"})
		return
	}
	config := jobconfig.Config{
		BaseURL: strings.TrimSpace(request.BaseURL), MachineID: strings.TrimSpace(request.MachineID),
		LicenseToken: strings.TrimSpace(request.LicenseToken),
	}
	if existing, err := a.jobConfigSource.LoadConfig(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "旧后台职位配置源读取失败"})
		return
	} else if existing != nil {
		if config.BaseURL == "" {
			config.BaseURL = existing.BaseURL
		}
		if config.MachineID == "" {
			config.MachineID = existing.MachineID
		}
		if config.LicenseToken == "" {
			config.LicenseToken = existing.LicenseToken
		}
	}
	if err := a.jobConfigSource.SaveConfig(config); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "旧后台职位配置源缺少必填项或地址无效"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": config.View()})
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
	raw, err := a.jobConfigSource.FetchCurrent(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "旧后台当前职位配置读取失败"})
		return
	}
	revisions, err := m5ai.ImportLegacyJobConfigFromBackend(raw, time.Now())
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "旧后台当前职位配置与本地执行约束不兼容"})
		return
	}
	stored, err := a.st.SaveJobAIContextRevisions(revisions)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "职位 AI 上下文未能原子导入"})
		return
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
	writeJSON(w, http.StatusOK, map[string]any{"contexts": views})
}
