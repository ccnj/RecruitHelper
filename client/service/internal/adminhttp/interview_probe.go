package adminhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// POST /admin/cards/interview/probe —— 邀面编辑器彩排(2026-07-29 甲方裁决)。
// 与 /admin/cards/interview 冒烟直发共享账号身份反查与 active 闸,但派发的
// 是 intrusive debug.probeInterviewEditor:手侧与 chat.sendInviteCard 字面
// 共用同一编辑器准备实现,填毕停留至少 5 秒供有人值守肉眼确认后取消,
// 构造性不含发送路径,不铸 effect intent、不落 WAL。同步等待手侧终局并
// 返回回读值。开发期有人值守工具,列入首客前复核清单。
type probeInterviewEditorBody struct {
	Platform        string `json:"platform"`
	AccountRef      string `json:"accountRef"`
	ConversationRef string `json:"conversationRef"`
	StartsAt        int64  `json:"startsAt"`
}

func (a *API) probeInterviewEditor(w http.ResponseWriter, r *http.Request) {
	var body probeInterviewEditorBody
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法请求体: " + err.Error()})
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体只能包含一个 JSON 对象"})
		return
	}
	if body.Platform == "" || body.AccountRef == "" || body.ConversationRef == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的账号/会话标识"})
		return
	}
	// 彩排虽不出站,但会打开并操作该会话页面上的邀面弹窗;automation active
	// 的会话随时可能被巡检/回复命令使用同一页面,拒绝彩排避免现场打架。
	blocked, err := a.st.CommunicationV4DirectSendBlocked(store.ConversationKey{
		Platform:        body.Platform,
		AccountRef:      body.AccountRef,
		ConversationRef: body.ConversationRef,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if blocked {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "该会话档案的 V4 沟通自动化仍为 active，禁止彩排（弹窗操作会与自动化命令抢占同一页面现场）",
		})
		return
	}
	// 手侧预算 90s + 派发排队余量;超时只表示本次彩排未在窗口内终局。
	ctx, cancel := context.WithTimeout(r.Context(), 130*time.Second)
	defer cancel()
	state, err := a.disp.ProbeInterviewEditor(ctx, dispatch.ProbeInterviewEditorRequest{
		Platform:        body.Platform,
		AccountRef:      body.AccountRef,
		ConversationRef: body.ConversationRef,
		Interview: protocol.InterviewDetails{
			StartsAt: body.StartsAt,
			EndsAt:   body.StartsAt + communication.V4InterviewDurationMs,
			Method:   protocol.InterviewMethodWechatVideo,
		},
	})
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			writeJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "彩排未在等待窗口内终局，请查账本 msgId 状态"})
		case errors.Is(err, dispatch.ErrHandOffline), errors.Is(err, store.ErrAccountIdentityNotCurrent):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		return
	}
	leaf := state.Leaf
	view := map[string]any{
		"msgId":  leaf.MsgID,
		"status": string(leaf.Status),
	}
	if leaf.ErrorCode != "" {
		view["errorCode"] = leaf.ErrorCode
	}
	if leaf.ResultBody != "" {
		var result map[string]any
		if json.Unmarshal([]byte(leaf.ResultBody), &result) == nil {
			if data, ok := result["data"]; ok {
				view["data"] = data
			}
			if errBody, ok := result["error"]; ok {
				view["error"] = errBody
			}
		}
	}
	writeJSON(w, http.StatusOK, view)
}
