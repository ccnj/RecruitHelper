package adminhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// POST /admin/osscroll —— 开发期 OS 滚轮探针(2026-09-03 立案)。
//
// **这是脑侧唯一的派发入口。** 巡检、工作流、沟通 v4 一律不铸这条命令,
// 门禁盯着(dispatch/osscroll_producer_test.go)。
//
// 用途是考古:人或 Claude 经诊断台指着一个滚动容器(CSS selector),让手以真实滚轮把它
// 朝一个方向滚一段距离——不点击、不输入。selector 在脑侧完全不透明,原样转交;
// index 缺省即"没给",手侧命中不唯一就拒、不猜第一个。
//
//	{"platform":"boss","accountRef":"…","selector":".chat-message-list","direction":"up","distancePx":1200}
//
// 收场 scrolled / edge / stuck / refusedByGate / handServiceUnavailable 全在 result.data.outcome,
// 每簇的 scrollTop 观测在 detail。stuck 里「方向反了」是钉注入器滚轮符号的那条闭环。
type osScrollBody struct {
	Platform   string `json:"platform"`
	AccountRef string `json:"accountRef"`
	Selector   string `json:"selector"`
	Index      *int   `json:"index,omitempty"`
	Direction  string `json:"direction"`
	DistancePx int    `json:"distancePx"`
}

func (a *API) osScroll(w http.ResponseWriter, r *http.Request) {
	var body osScrollBody
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法请求体: " + err.Error()})
		return
	}
	// 契约 deadlineMs 是 300 秒;等待窗口留余量,超时只表示没等到终局,
	// 不表示命令失败——账本里那条 msgId 仍在自己的轨道上收束。
	ctx, cancel := context.WithTimeout(r.Context(), 330*time.Second)
	defer cancel()

	state, err := a.disp.OsScroll(ctx, body.Platform, body.AccountRef, body.Selector, body.Index,
		protocol.OsScrollDirection(body.Direction), body.DistancePx)
	if err != nil {
		writeDispatchError(w, err, "滚轮探针未在等待窗口内终局，请查账本 msgId 状态")
		return
	}
	writeProbeLeaf(w, state)
}

// writeDispatchError 把派发失败翻成状态码:超时 504、手离线/身份不当前 409、其余 400。
// 三条探针端点同一份,别让其中一份先过时。
func writeDispatchError(w http.ResponseWriter, err error, timeoutHint string) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		writeJSON(w, http.StatusGatewayTimeout, map[string]string{"error": timeoutHint})
	case errors.Is(err, dispatch.ErrHandOffline), errors.Is(err, store.ErrAccountIdentityNotCurrent):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
}

// writeProbeLeaf 回显探针的终局:msgId、状态、错误码与 result 原文。
// 诊断值直接回显给同机诊断台(「开发者诊断台明文边界」)。
func writeProbeLeaf(w http.ResponseWriter, state *store.LogicalDispatchState) {
	leaf := state.Leaf
	view := map[string]any{"msgId": leaf.MsgID, "status": string(leaf.Status)}
	if leaf.ErrorCode != "" {
		view["errorCode"] = leaf.ErrorCode
	}
	if leaf.ResultBody != "" {
		view["result"] = json.RawMessage(leaf.ResultBody)
	}
	writeJSON(w, http.StatusOK, view)
}
