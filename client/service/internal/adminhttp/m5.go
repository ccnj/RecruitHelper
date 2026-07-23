package adminhttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/internal/ids"
)

type m5TrialView struct {
	SelectionID       string          `json:"selectionId"`
	ProfileID         string          `json:"profileId"`
	Status            string          `json:"status"`
	CaptureState      string          `json:"captureState"`
	LogicalDispatchID *string         `json:"logicalDispatchId,omitempty"`
	Snapshot          *m5SnapshotView `json:"snapshot,omitempty"`
	Reason            string          `json:"reason,omitempty"`
	SelectedAt        time.Time       `json:"selectedAt"`
	EndedAt           *time.Time      `json:"endedAt,omitempty"`
}

type m5SnapshotView struct {
	SnapshotID       string `json:"snapshotId"`
	ContentHash      string `json:"contentHash"`
	SchemaVersion    int    `json:"schemaVersion"`
	Bytes            int    `json:"bytes"`
	BasicItems       int    `json:"basicItems"`
	ExpectationItems int    `json:"expectationItems"`
	SectionsComplete bool   `json:"sectionsComplete"`
}

func (a *API) selectM5Trial(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Platform        string `json:"platform"`
		AccountRef      string `json:"accountRef"`
		ConversationRef string `json:"conversationRef"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法请求体"})
		return
	}
	key, err := validateAccountKey(req.Platform, req.AccountRef)
	req.ConversationRef = strings.TrimSpace(req.ConversationRef)
	if err != nil || req.ConversationRef == "" || len(req.ConversationRef) > 512 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的平台、账号或会话标识"})
		return
	}
	profile, err := a.st.CandidateProfileByConversation(store.ConversationKey{
		Platform: key.Platform, AccountRef: key.AccountRef, ConversationRef: req.ConversationRef,
	})
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "试运行档案查询失败"})
		return
	}
	if profile == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "当前会话没有可用候选人档案"})
		return
	}
	if _, err := a.st.SelectM5TrialProfile(profile.ProfileID, ids.NewTrialSelectionID(), "user", time.Now()); err != nil {
		status := http.StatusConflict
		if errors.Is(err, store.ErrCandidateProfileNotFound) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]string{"error": "当前档案不允许进入 M5 试运行"})
		return
	}
	if a.actor != nil {
		_ = a.actor.RequestImmediate(key)
	}
	a.writeM5TrialStatus(w)
}

func (a *API) m5TrialStatus(w http.ResponseWriter, _ *http.Request) {
	a.writeM5TrialStatus(w)
}

func (a *API) recoverM5ReplyBudget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SelectionID string `json:"selectionId"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法请求体"})
		return
	}
	req.SelectionID = strings.TrimSpace(req.SelectionID)
	if req.SelectionID == "" || len(req.SelectionID) > 512 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的试运行选择标识"})
		return
	}
	result, err := a.st.AuthorizeM5ReplyBudgetRecovery(store.AuthorizeM5ReplyBudgetRecoveryRequest{
		FailedSelectionID: req.SelectionID,
		NewSelectionID:    ids.NewTrialSelectionID(),
		AuthorizedAt:      time.Now(),
	})
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "当前失败轮次不允许预算恢复"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                true,
		"selectionId":       result.Selection.SelectionID,
		"alreadyAuthorized": result.AlreadyAuthorized,
	})
}

func (a *API) writeM5TrialStatus(w http.ResponseWriter) {
	status, err := a.st.M5TrialStatus()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "M5 试运行状态读取失败"})
		return
	}
	if status == nil {
		writeJSON(w, http.StatusOK, map[string]any{"trial": nil})
		return
	}
	view := m5TrialView{
		SelectionID: status.Selection.SelectionID, ProfileID: status.Profile.ProfileID,
		Status: string(status.Selection.Status), CaptureState: string(status.Profile.ResumeCaptureState),
		LogicalDispatchID: status.Profile.ResumeCaptureLogicalDispatchID,
		Reason:            status.Selection.Reason, SelectedAt: status.Selection.SelectedAt, EndedAt: status.Selection.EndedAt,
	}
	if status.Snapshot != nil {
		var body struct {
			Basic           []json.RawMessage `json:"basic"`
			Expectations    []json.RawMessage `json:"expectations"`
			SelfEvaluation  *string           `json:"selfEvaluation"`
			Education       *string           `json:"education"`
			WorkExperiences *string           `json:"workExperiences"`
		}
		complete := json.Unmarshal([]byte(status.Snapshot.ResumeJSON), &body) == nil &&
			body.Basic != nil && body.Expectations != nil && body.SelfEvaluation != nil &&
			body.Education != nil && body.WorkExperiences != nil
		view.Snapshot = &m5SnapshotView{
			SnapshotID: status.Snapshot.SnapshotID, ContentHash: status.Snapshot.ContentHash,
			SchemaVersion: status.Snapshot.SchemaVersion, Bytes: status.ByteSize,
			BasicItems: len(body.Basic), ExpectationItems: len(body.Expectations), SectionsComplete: complete,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"trial": view})
}
