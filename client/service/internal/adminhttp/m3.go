package adminhttp

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/store"
)

type sendMessageBody struct {
	IntentID         string `json:"intentId"`
	PreviousIntentID string `json:"previousIntentId"`
	Platform         string `json:"platform"`
	AccountRef       string `json:"accountRef"`
	ConversationRef  string `json:"conversationRef"`
	Text             string `json:"text"`
}

type sendMessageView struct {
	IntentID             string                   `json:"intentId"`
	LogicalDispatchID    string                   `json:"logicalDispatchId"`
	MsgID                string                   `json:"msgId"`
	Status               store.EffectIntentStatus `json:"status"`
	Created              bool                     `json:"created"`
	CommandStatus        store.CmdStatus          `json:"commandStatus"`
	VerificationAttempts int                      `json:"verificationAttempts"`
	SuspectReason        string                   `json:"suspectReason,omitempty"`
}

type sendMessageConflictView struct {
	Error   string          `json:"error"`
	Current sendMessageView `json:"current"`
}

func (a *API) sendMessage(w http.ResponseWriter, r *http.Request) {
	var body sendMessageBody
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
	receipt, err := a.disp.SendMessage(dispatch.SendMessageRequest{
		IntentID: body.IntentID, PreviousIntentID: body.PreviousIntentID,
		Platform: body.Platform, AccountRef: body.AccountRef,
		ConversationRef: body.ConversationRef, Text: body.Text,
	})
	if err != nil && receipt != nil && errors.Is(err, store.ErrEffectIntentCASConflict) {
		writeJSON(w, http.StatusConflict, sendMessageConflictView{
			Error: err.Error(), Current: sendMessageReceiptView(receipt),
		})
		return
	}
	if err != nil && receipt == nil {
		writeSendMessageError(w, err)
		return
	}
	if receipt == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	code := http.StatusOK
	if receipt.Created {
		code = http.StatusAccepted
	}
	view := sendMessageReceiptView(receipt)
	if err != nil {
		// 命令已先落 WAL，但 socket 写结果为失败/未知。返回可查的
		// intent/msgId 而不伪装 HTTP 没创建任何事物。
		writeJSON(w, http.StatusAccepted, view)
		return
	}
	writeJSON(w, code, view)
}

func (a *API) sendMessageStatus(w http.ResponseWriter, r *http.Request) {
	intentID := strings.TrimSpace(r.URL.Query().Get("intentId"))
	platform := strings.TrimSpace(r.URL.Query().Get("platform"))
	accountRef := strings.TrimSpace(r.URL.Query().Get("accountRef"))
	conversationRef := strings.TrimSpace(r.URL.Query().Get("conversationRef"))
	hasTarget := platform != "" || accountRef != "" || conversationRef != ""
	var receipt *dispatch.SendMessageReceipt
	var err error
	switch {
	case intentID != "" && hasTarget:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "intentId 与会话查询参数只能二选一"})
		return
	case intentID != "":
		receipt, err = a.disp.SendMessageStatus(intentID)
	case platform != "" && accountRef != "" && conversationRef != "":
		receipt, err = a.disp.LatestSendMessageStatus(platform, accountRef, conversationRef)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请提供 intentId 或完整 platform/accountRef/conversationRef"})
		return
	}
	if err != nil {
		writeSendMessageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sendMessageReceiptView(receipt))
}

func sendMessageReceiptView(receipt *dispatch.SendMessageReceipt) sendMessageView {
	return sendMessageView{
		IntentID: receipt.IntentID, LogicalDispatchID: receipt.LogicalDispatchID,
		MsgID: receipt.MsgID, Status: receipt.Status, Created: receipt.Created,
		CommandStatus: receipt.CommandStatus, VerificationAttempts: receipt.VerificationAttempts,
		SuspectReason: receipt.SuspectReason,
	}
}

func writeSendMessageError(w http.ResponseWriter, err error) {
	status := http.StatusConflict
	switch {
	case errors.Is(err, store.ErrEffectIntentNotFound),
		errors.Is(err, store.ErrAccountNotFound),
		errors.Is(err, store.ErrConversationNotFound):
		status = http.StatusNotFound
	case isSendRequestError(err):
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func isSendRequestError(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return strings.Contains(text, "缺少有效的 intentId") ||
		strings.Contains(text, "发送文本不能为空") ||
		strings.Contains(text, "契约校验")
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("多余 JSON")
	}
	return err
}
