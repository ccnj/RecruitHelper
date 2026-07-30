package adminhttp

import (
	"net/http"
	"strings"

	"recruithelper/client/service/internal/handreload"
)

type reloadHandView struct {
	Ready            bool   `json:"ready"`
	HandID           string `json:"handId"`
	MsgID            string `json:"msgId"`
	PreviousBootID   string `json:"previousBootId"`
	BootID           string `json:"bootId"`
	ContractHash     string `json:"contractHash"`
	ExtensionVersion string `json:"extensionVersion"`
}

// reloadOrchestrator 组装编排依赖。逐个判 nil 再赋值,是因为把一个 nil 的具体
// 指针塞进接口字段会得到"非 nil 的接口值",编排内部的 nil 检查就形同虚设。
func (a *API) reloadOrchestrator() *handreload.Orchestrator {
	orchestrator := &handreload.Orchestrator{Registry: a.hub.Registry()}
	if a.disp != nil {
		orchestrator.Dispatcher = a.disp
	}
	if a.st != nil {
		orchestrator.Store = a.st
		orchestrator.Feeds = a.st
	}
	if a.actor != nil {
		orchestrator.Feeds = a.actor
	}
	return orchestrator
}

// reloadHand 是 §14 部署硬切换里“重载插件”这一个步骤:命令仍走正式
// brain→hand Dispatcher;HTTP 只负责发起并把编排结论翻成状态码。
//
// 判据本身在 handreload 包里,与客户端换代后的自动触发器共用同一条路径 ——
// 人工按钮不比自动路径宽松,自动路径也不比人工按钮宽松。
func (a *API) reloadHand(w http.ResponseWriter, r *http.Request) {
	if a.disp == nil || a.st == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "重载编排尚未就绪"})
		return
	}
	var req struct {
		HandID string `json:"handId"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	orchestrator := a.reloadOrchestrator()

	req.HandID = strings.TrimSpace(req.HandID)
	if req.HandID == "" {
		selected, selectErr := orchestrator.SelectUniqueHand()
		if selectErr != nil {
			writeReloadError(w, selectErr)
			return
		}
		req.HandID = selected
	}

	result, reloadErr := orchestrator.Reload(r.Context(), req.HandID)
	if reloadErr != nil {
		// 请求方自己断了连接:不必再往一个没人读的 ResponseWriter 里写。
		if r.Context().Err() != nil {
			return
		}
		writeReloadError(w, reloadErr)
		return
	}
	writeJSON(w, http.StatusOK, reloadHandView{
		Ready: true, HandID: result.HandID, MsgID: result.MsgID,
		PreviousBootID: result.PreviousBootID, BootID: result.BootID,
		ContractHash: result.ContractHash, ExtensionVersion: result.ExtensionVersion,
	})
}

func writeReloadError(w http.ResponseWriter, err *handreload.Error) {
	body := map[string]string{"error": err.Message}
	if err.MsgID != "" {
		body["msgId"] = err.MsgID
	}
	writeJSON(w, reloadStatusCode(err.Kind), body)
}

func reloadStatusCode(kind handreload.Kind) int {
	switch kind {
	case handreload.KindUnavailable:
		return http.StatusServiceUnavailable
	case handreload.KindInternal:
		return http.StatusInternalServerError
	case handreload.KindTimeout:
		return http.StatusGatewayTimeout
	default:
		return http.StatusConflict
	}
}
