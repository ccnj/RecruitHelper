package adminhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/store"
)

// POST /admin/ostype —— 开发期 OS 打字探针(2026-09-01 立案)。
//
// **这是脑侧唯一的派发入口。** 巡检、工作流、沟通 v4 一律不铸这条命令,
// 门禁盯着(dispatch/ostype_producer_test.go)。
//
// 它把一句中文打进 IM 输入框然后**停手,不点发送**。输入框非空时手侧会拒绝——
// 输入框里已有的内容先以真实按键全选删除再打(2026-09-03 裁决撤销 composer.empty),与发送原语同一条清空路径。
//
// **outcome=typed 但 matched=false 不是失败。** macOS 开发机没有自研 TIP、走系统
// 输入法,上屏词不可控(「聊聊」可能出成「了了」);Windows 上 TIP 说了算,应当
// 逐字相同。两边同一份代码、同一条判定,差别由 matched 如实带出。
//
// planFailed 也是正常收场:排版器闭环自验,排不出合格形状就如实说排不出来;
// 文案含英文字母或半角标点时会更早地显式失败——那两类**当前打不出来**,
// 上游要靠 TSF 切输入法模式解,尚未实现。
type osTypeBody struct {
	Platform   string `json:"platform"`
	AccountRef string `json:"accountRef"`
	Text       string `json:"text"`
}

func (a *API) osType(w http.ResponseWriter, r *http.Request) {
	var body osTypeBody
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

	state, err := a.disp.OsType(ctx, body.Platform, body.AccountRef, body.Text)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			writeJSON(w, http.StatusGatewayTimeout, map[string]string{
				"error": "打字探针未在等待窗口内终局，请查账本 msgId 状态"})
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
	// 诊断值直接回显给同机诊断台(「开发者诊断台明文边界」)。data 里的 detail 会带上
	// 我们自己发出去的那句文案的字数与回读结果——**那是我方文案,不是候选人内容**。
	if leaf.ResultBody != "" {
		view["result"] = json.RawMessage(leaf.ResultBody)
	}
	writeJSON(w, http.StatusOK, view)
}
