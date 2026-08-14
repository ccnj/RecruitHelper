package adminhttp

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"recruithelper/client/service/internal/store"
)

// 巡检隔离的诊断台出口（2026-08-14 甲方裁决）。
//
// 隔离此前是单向门：ClearConversationPatrolQuarantine 只有测试在调，没有
// 端点、没有列表——被冻的人"安静但永久"地消失，正是 2026-08-02 裁决点名
// 反对的失效形态。列表补可见性，clear 补出口。
//
// clear 限单条、显式 conversationRef，不做批量：误用会让被正当隔离（手明确
// 说 manualOnly）的人恢复自动化，一次只放一个、留审计，错了能追。

func (a *API) patrolQuarantines(w http.ResponseWriter, _ *http.Request) {
	rows, err := a.st.PatrolQuarantinedConversations()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"quarantines": rows})
}

func (a *API) patrolQuarantineClear(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Platform        string `json:"platform"`
		AccountRef      string `json:"accountRef"`
		ConversationRef string `json:"conversationRef"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法请求体"})
		return
	}
	req.ConversationRef = strings.TrimSpace(req.ConversationRef)
	key, err := validateAccountKey(req.Platform, req.AccountRef)
	if err != nil || req.ConversationRef == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的平台、账号或会话标识"})
		return
	}
	conversationKey := store.ConversationKey{
		Platform: key.Platform, AccountRef: key.AccountRef,
		ConversationRef: req.ConversationRef,
	}
	released, profileResumed, err := a.st.ReleasePatrolQuarantine(conversationKey, time.Now())
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if released {
		a.st.Audit("patrol_quarantine_cleared", "", req.ConversationRef,
			fmt.Sprintf("platform=%s accountRef=%s profileResumed=%t",
				key.Platform, key.AccountRef, profileResumed))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"released":       released,
		"profileResumed": profileResumed,
	})
}
