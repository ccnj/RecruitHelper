package adminhttp

import (
	"encoding/json"
	"net/http"

	"recruithelper/client/service/internal/store"
)

// 状态栏开关(2026-09-08 甲方裁决,出口见 docs/boss/状态栏出口-2026-09-08.md)。
//
// 小窗每几秒读一次 GET 决定画客户版还是开发版;POST 是裁决允许的**唯一**开启
// 路径——诊断台里人点一下。主进程不参与,没有 IPC:开关落在脑,重启后照旧。

type statusBarSettingsView struct {
	DetailEnabled bool   `json:"detailEnabled"`
	Error         string `json:"error,omitempty"`
}

func statusBarSettingsToView(setting store.StatusBarSetting) statusBarSettingsView {
	return statusBarSettingsView{DetailEnabled: setting.DetailEnabled}
}

// GET /admin/statusbar/settings
func (a *API) statusBarSettings(w http.ResponseWriter, _ *http.Request) {
	if a.st == nil {
		writeJSON(w, http.StatusPreconditionFailed, statusBarSettingsView{Error: "存储未装配"})
		return
	}
	setting, err := a.st.StatusBarSetting()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, statusBarSettingsView{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, statusBarSettingsToView(setting))
}

// POST /admin/statusbar/settings —— 切换详细模式。
func (a *API) setStatusBarSettings(w http.ResponseWriter, r *http.Request) {
	if a.st == nil {
		writeJSON(w, http.StatusPreconditionFailed, statusBarSettingsView{Error: "存储未装配"})
		return
	}
	var body struct {
		DetailEnabled *bool `json:"detailEnabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.DetailEnabled == nil {
		writeJSON(w, http.StatusBadRequest, statusBarSettingsView{Error: "缺少 detailEnabled"})
		return
	}
	if err := a.st.SetStatusBarDetail(*body.DetailEnabled); err != nil {
		writeJSON(w, http.StatusInternalServerError, statusBarSettingsView{Error: err.Error()})
		return
	}
	setting, err := a.st.StatusBarSetting()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, statusBarSettingsView{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, statusBarSettingsToView(setting))
}
