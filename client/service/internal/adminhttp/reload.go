package adminhttp

import (
	"net/http"
	"strings"
	"time"

	"recruithelper/client/service/internal/session"
	"recruithelper/contract/gen/go/protocol"
)

const reloadReadyTimeout = 30 * time.Second

type reloadHandView struct {
	Ready            bool   `json:"ready"`
	HandID           string `json:"handId"`
	MsgID            string `json:"msgId"`
	PreviousBootID   string `json:"previousBootId"`
	BootID           string `json:"bootId"`
	ContractHash     string `json:"contractHash"`
	ExtensionVersion string `json:"extensionVersion"`
}

// reloadHand 是 §14 部署硬切换里“重载插件”这一个步骤：命令仍走正式
// brain→hand Dispatcher；HTTP 只负责发起并等待新 hello 的构建证词。
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
	req.HandID = strings.TrimSpace(req.HandID)
	if req.HandID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的 handId"})
		return
	}
	before, ok := a.hub.Registry().Get(req.HandID)
	if !ok || !before.Online || before.Health != session.HealthReady {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "所选手当前未就绪"})
		return
	}
	capability := protocol.PrimDebugReload + "@1"
	if !hasString(before.Caps, capability) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "当前手尚未具备一键重载能力；首次启用仍需人工重载一次插件",
		})
		return
	}
	commands, err := a.st.NonTerminalCmdsForHand(req.HandID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if len(commands) != 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "该手仍有未收束命令，请先暂停派发并等待命令完成"})
		return
	}
	now := time.Now()
	if a.actor != nil {
		err = a.actor.InvalidateSourcingFeedsForHand(req.HandID, "adminPluginReload", now)
	} else {
		err = a.st.InvalidateSourcingFeedsForHand(req.HandID, "adminPluginReload", now)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "重载前终止旧推荐流失败"})
		return
	}

	msgID, err := a.disp.Dispatch(req.HandID, protocol.PrimDebugReload, []byte(`{}`))
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "msgId": msgID})
		return
	}

	deadline := time.NewTimer(reloadReadyTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			writeJSON(w, http.StatusGatewayTimeout, map[string]string{
				"error": "等待插件换代超时；未自动重派重载命令，请人工检查后再次点击",
				"msgId": msgID,
			})
			return
		case <-ticker.C:
			current, exists := a.hub.Registry().Get(req.HandID)
			if !exists || !current.Online || current.Health != session.HealthReady || current.BootID == before.BootID {
				continue
			}
			if !current.ContractMatch || current.ContractHash != protocol.ContractHash {
				writeJSON(w, http.StatusConflict, map[string]string{
					"error": "插件已经换代，但 contractHash 与当前脑不一致；保持暂停并检查 plugin/dist",
					"msgId": msgID,
				})
				return
			}
			if !hasString(current.Caps, capability) {
				writeJSON(w, http.StatusConflict, map[string]string{
					"error": "插件已经换代，但新手未声明 debug.reload@1",
					"msgId": msgID,
				})
				return
			}
			writeJSON(w, http.StatusOK, reloadHandView{
				Ready: true, HandID: req.HandID, MsgID: msgID,
				PreviousBootID: before.BootID, BootID: current.BootID,
				ContractHash: current.ContractHash, ExtensionVersion: current.ExtVersion,
			})
			return
		}
	}
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
