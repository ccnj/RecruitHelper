package adminhttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"recruithelper/client/service/internal/productapp"
	"recruithelper/client/service/internal/store"
)

// 状态栏开关(2026-09-08 甲方裁决,出口见 docs/boss/状态栏出口-2026-09-08.md)。
//
// 小窗每几秒读一次 GET 决定画客户版还是开发版;POST 是裁决允许的**唯一**开启
// 路径——诊断台里人点一下。主进程不参与,没有 IPC:开关落在脑,重启后照旧。

type statusBarSettingsView struct {
	DetailEnabled bool   `json:"detailEnabled"`
	Position      string `json:"position"`
	// Visible 是按当前绑定平台算出的实际值;VisibleSource 说明它是平台默认还是人拨的;
	// Platform 是算它用的平台(快照缺席按智联),给诊断台把话说全。
	Visible       bool   `json:"visible"`
	VisibleSource string `json:"visibleSource"`
	Platform      string `json:"platform"`
	Error         string `json:"error,omitempty"`
}

func (a *API) statusBarSettingsToView(setting store.StatusBarSetting) statusBarSettingsView {
	platform := a.customerPlatform()
	return statusBarSettingsView{
		DetailEnabled: setting.DetailEnabled,
		Position:      setting.EffectivePosition(),
		Visible:       setting.EffectiveVisible(platform),
		VisibleSource: setting.VisibleSource(),
		Platform:      platform,
	}
}

// customerPlatform 与 productapp 的同名逻辑一致:快照缺席按智联。
func (a *API) customerPlatform() string {
	if a.platformSource == nil {
		return productapp.DefaultPlatform
	}
	platform := strings.TrimSpace(a.platformSource.CustomerPlatform())
	if platform == "" {
		return productapp.DefaultPlatform
	}
	return platform
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
	writeJSON(w, http.StatusOK, a.statusBarSettingsToView(setting))
}

// POST /admin/statusbar/settings —— 显示开关、详细模式、位置档位,三项各自可选、至少一项。
func (a *API) setStatusBarSettings(w http.ResponseWriter, r *http.Request) {
	if a.st == nil {
		writeJSON(w, http.StatusPreconditionFailed, statusBarSettingsView{Error: "存储未装配"})
		return
	}
	var body struct {
		Visible       *bool   `json:"visible"`
		DetailEnabled *bool   `json:"detailEnabled"`
		Position      *string `json:"position"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		(body.Visible == nil && body.DetailEnabled == nil && body.Position == nil) {
		writeJSON(w, http.StatusBadRequest, statusBarSettingsView{Error: "缺少 visible、detailEnabled 或 position"})
		return
	}
	if body.Visible != nil {
		if err := a.st.SetStatusBarVisible(*body.Visible); err != nil {
			writeJSON(w, http.StatusInternalServerError, statusBarSettingsView{Error: err.Error()})
			return
		}
	}
	if body.DetailEnabled != nil {
		if err := a.st.SetStatusBarDetail(*body.DetailEnabled); err != nil {
			writeJSON(w, http.StatusInternalServerError, statusBarSettingsView{Error: err.Error()})
			return
		}
	}
	if body.Position != nil {
		if err := a.st.SetStatusBarPosition(*body.Position); err != nil {
			if errors.Is(err, store.ErrStatusBarPositionInvalid) {
				writeJSON(w, http.StatusBadRequest, statusBarSettingsView{Error: err.Error()})
				return
			}
			writeJSON(w, http.StatusInternalServerError, statusBarSettingsView{Error: err.Error()})
			return
		}
	}
	setting, err := a.st.StatusBarSetting()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, statusBarSettingsView{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, a.statusBarSettingsToView(setting))
}
