package adminhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"recruithelper/contract/gen/go/protocol"
)

// POST /admin/osclick —— 开发期 OS 点击探针(2026-09-03 立案)。
//
// **这是脑侧唯一的派发入口。** 巡检、工作流、沟通 v4 一律不铸这条命令,
// 门禁盯着(dispatch/osclick_producer_test.go)。
//
// 用途是考古:人或 Claude 经诊断台指着页面上一个元素(CSS selector),让手以拟人轨迹
// 落上去(mode=move,绝不点)或落上去再按一下(mode=click,至多一次)。它替代 CDP 点击——
// CDP 的点击没有 OS 轨迹,不够拟人。
//
//	{"platform":"boss","accountRef":"…","selector":".geek-item","index":2,"mode":"click","expectText":"张先生"}
//
// **click 模式点下去对页面做什么由靶子决定**;expectText 与点前最后一次命中测试是错靶防线,
// 不是对不可逆控件的授权——只对可逆或已裁决的控件用它。
type osClickBody struct {
	Platform   string `json:"platform"`
	AccountRef string `json:"accountRef"`
	Selector   string `json:"selector"`
	Index      *int   `json:"index,omitempty"`
	Mode       string `json:"mode"`
	ExpectText string `json:"expectText,omitempty"`
}

func (a *API) osClick(w http.ResponseWriter, r *http.Request) {
	var body osClickBody
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法请求体: " + err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 330*time.Second)
	defer cancel()

	state, err := a.disp.OsClick(ctx, body.Platform, body.AccountRef, body.Selector, body.Index,
		protocol.OsClickMode(body.Mode), body.ExpectText)
	if err != nil {
		writeDispatchError(w, err, "点击探针未在等待窗口内终局，请查账本 msgId 状态")
		return
	}
	writeProbeLeaf(w, state)
}
