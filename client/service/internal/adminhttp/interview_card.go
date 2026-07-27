package adminhttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"recruithelper/client/service/internal/communication"
	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// POST /admin/cards/interview —— 2026-07-27 甲方批准的邀面卡冒烟生产者。
// 按"测试页=命令生产者"约定，与生产动作轨共用同一原语、WAL、idemKey、守卫、
// 手端与服务端正证实现，仅意图出生地不同。附带闸：档案 V4 沟通自动化仍为
// active 的会话拒绝直发（计划外出站会破坏轮出站锚）。列入首客前复核清单。
type sendInterviewCardBody struct {
	IntentID         string `json:"intentId"`
	PreviousIntentID string `json:"previousIntentId"`
	Platform         string `json:"platform"`
	AccountRef       string `json:"accountRef"`
	ConversationRef  string `json:"conversationRef"`
	StartsAt         int64  `json:"startsAt"`
}

func (a *API) sendInterviewCard(w http.ResponseWriter, r *http.Request) {
	var body sendInterviewCardBody
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的 intentId/账号/会话标识"})
		return
	}
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
			"error": "该会话档案的 V4 沟通自动化仍为 active，禁止冒烟直发（计划外出站会破坏轮出站锚）",
		})
		return
	}
	receipt, err := a.disp.SendDirectInterviewCard(dispatch.SendDirectInterviewCardRequest{
		IntentID:         body.IntentID,
		PreviousIntentID: body.PreviousIntentID,
		Platform:         body.Platform,
		AccountRef:       body.AccountRef,
		ConversationRef:  body.ConversationRef,
		Interview: protocol.InterviewDetails{
			StartsAt: body.StartsAt,
			EndsAt:   body.StartsAt + communication.V4InterviewDurationMs,
			Method:   protocol.InterviewMethodWechatVideo,
		},
	})
	if err != nil && receipt != nil && errors.Is(err, store.ErrEffectIntentCASConflict) {
		writeJSON(w, http.StatusConflict, sendMessageConflictView{
			Error: err.Error(), Current: sendMessageReceiptView(receipt),
		})
		return
	}
	if err != nil && receipt == nil {
		writeSendInterviewCardError(w, err)
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
		// 与 /admin/messages/send 同义：WAL 已落，socket 结果未知也要回可查回执。
		writeJSON(w, http.StatusAccepted, view)
		return
	}
	writeJSON(w, code, view)
}

func writeSendInterviewCardError(w http.ResponseWriter, err error) {
	if err != nil && strings.Contains(err.Error(), "邀面参数必须是") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeSendMessageError(w, err)
}
