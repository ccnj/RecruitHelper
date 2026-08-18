package adminhttp

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/internal/ids"
)

const m5ContextImportMaxBytes = 2 << 20

type m5ContextView struct {
	ContextID      string    `json:"contextId"`
	RevisionHash   string    `json:"revisionHash"`
	DisplayName    string    `json:"displayName"`
	Environment    string    `json:"environment"`
	MappingVersion string    `json:"mappingVersion"`
	DocumentCount  int       `json:"documentCount"`
	CreatedAt      time.Time `json:"createdAt"`
}

type m5ContextBindingView struct {
	BindingID    string    `json:"bindingId"`
	ProfileID    string    `json:"profileId"`
	ContextID    string    `json:"contextId"`
	RevisionHash string    `json:"revisionHash"`
	Status       string    `json:"status"`
	BoundAt      time.Time `json:"boundAt"`
}

func (a *API) m5Contexts(w http.ResponseWriter, _ *http.Request) {
	summaries, err := a.st.JobAIContextRevisionSummaries()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "职位 AI 上下文读取失败", err)
		return
	}
	views := make([]m5ContextView, 0, len(summaries))
	for _, summary := range summaries {
		views = append(views, m5ContextView{
			ContextID: summary.ContextID, RevisionHash: summary.RevisionHash,
			DisplayName: summary.DisplayName, Environment: summary.Environment,
			MappingVersion: summary.MappingVersion, DocumentCount: summary.DocumentCount,
			CreatedAt: summary.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"contexts": views})
}

func (a *API) importM5Contexts(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Bundle json.RawMessage `json:"bundle"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, m5ContextImportMaxBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || len(request.Bundle) == 0 || string(request.Bundle) == "null" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "职位配置整包无效"})
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "职位配置整包无效"})
		return
	}
	revisions, err := m5ai.ImportLegacyJobConfig(request.Bundle, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, "职位配置整包与 M5-A 兼容约束不一致", err)
		return
	}
	stored, err := a.st.SaveJobAIContextRevisions(revisions)
	if err != nil {
		writeError(w, http.StatusConflict, "职位 AI 上下文未能原子导入", err)
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

func (a *API) bindM5Context(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ContextID    string `json:"contextId"`
		RevisionHash string `json:"revisionHash"`
	}
	if decodeJSON(r, &request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "职位上下文绑定请求无效"})
		return
	}
	request.ContextID = strings.TrimSpace(request.ContextID)
	request.RevisionHash = strings.TrimSpace(request.RevisionHash)
	status, err := a.st.M5TrialStatus()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "M5 试运行状态读取失败", err)
		return
	}
	if status == nil || status.Selection.Status != store.M5TrialSelectionActive {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "当前没有可绑定的 M5 试运行档案"})
		return
	}
	binding, err := a.st.BindActiveM5TrialProfileAIContext(store.BindProfileAIContextRequest{
		BindingID: ids.NewAIContextBindingID(), ProfileID: status.Profile.ProfileID,
		ContextID: request.ContextID, RevisionHash: request.RevisionHash,
		Reason: "userBound", BoundBy: "user", BoundAt: time.Now(),
	})
	if err != nil {
		writeError(w, http.StatusConflict, "职位上下文绑定失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"binding": contextBindingView(*binding)})
}

func (a *API) m5ContextBinding(w http.ResponseWriter, _ *http.Request) {
	status, err := a.st.M5TrialStatus()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "M5 试运行状态读取失败", err)
		return
	}
	if status == nil || status.Selection.Status != store.M5TrialSelectionActive {
		writeJSON(w, http.StatusOK, map[string]any{"binding": nil})
		return
	}
	active, err := a.st.ActiveProfileAIContext(status.Profile.ProfileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "职位上下文绑定读取失败", err)
		return
	}
	if active == nil {
		writeJSON(w, http.StatusOK, map[string]any{"binding": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"binding": contextBindingView(active.Binding)})
}

func contextBindingView(binding store.ProfileAIContextBinding) m5ContextBindingView {
	return m5ContextBindingView{
		BindingID: binding.BindingID, ProfileID: binding.ProfileID,
		ContextID: binding.ContextID, RevisionHash: binding.RevisionHash,
		Status: string(binding.Status), BoundAt: binding.BoundAt,
	}
}
