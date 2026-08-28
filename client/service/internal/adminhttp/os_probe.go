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

// POST /admin/osprobe —— 开发期 OS 注入探针(2026-08-28 立案)。
//
// **这是脑侧唯一的派发入口。** 巡检、工作流、沟通 v4 一律不铸这条命令,
// 门禁盯着(dispatch/osprobe_producer_test.go)。
//
// 本轮只有 viewportSpread 一个靶子:走完「定位 → 移光标 → 落点确认 → 喂搭车标定」
// 四段,**只移动、绝不点击**。在坐标被证明对之前,不该让第一次 OS 注入的点击落在
// 真人账号的页面上。
//
// 冷启动必然打偏(窗口粗估偏一两百物理像素),所以 outcome 第一次可能是
// refusedByGate;它会自己重试到标定追上,上游实测两趟就够。
type osProbeBody struct {
	Platform   string `json:"platform"`
	AccountRef string `json:"accountRef"`
	Target     string `json:"target"`
}

func (a *API) osProbe(w http.ResponseWriter, r *http.Request) {
	var body osProbeBody
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法请求体: " + err.Error()})
		return
	}
	target := protocol.OsProbeTarget(body.Target)
	if body.Target == "" {
		target = protocol.OsProbeTargetViewportSpread
	}
	// 契约 deadlineMs 是 300 秒;等待窗口留出余量,超时只表示没等到终局,
	// 不表示命令失败——账本里那条 msgId 仍在自己的轨道上收束。
	ctx, cancel := context.WithTimeout(r.Context(), 330*time.Second)
	defer cancel()

	state, err := a.disp.OsProbe(ctx, body.Platform, body.AccountRef, target)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			writeJSON(w, http.StatusGatewayTimeout, map[string]string{
				"error": "探针未在等待窗口内终局，请查账本 msgId 状态"})
		case errors.Is(err, dispatch.ErrHandOffline), errors.Is(err, store.ErrAccountIdentityNotCurrent):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		return
	}
	leaf := state.Leaf
	view := map[string]any{"msgId": leaf.MsgID, "status": string(leaf.Status)}
	if leaf.ErrorCode != "" {
		view["errorCode"] = leaf.ErrorCode
	}
	// 诊断值直接回显给同机诊断台(「开发者诊断台明文边界」)。它只含整数像素、
	// 毫秒与微秒,不带 selector、坐标、账号或候选人任何信息。
	if leaf.ResultBody != "" {
		view["result"] = json.RawMessage(leaf.ResultBody)
	}
	writeJSON(w, http.StatusOK, view)
}
