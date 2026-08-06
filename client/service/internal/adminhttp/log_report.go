// 日志上报的诊断台状态页(AGENTS.md「全局约定·日志上报」,2026-08-06 甲方裁决)。
//
// **这里没有开关,只有状态。** 2026-08-06 甲方当日修订:上报常开、不设开关,
// 理由是"只是上报日志,不干过分的事"。诊断台仍要能回答"到底传没传出去、
// 发了多少、丢了多少" —— 上报是后台静默进行的,没有这一页就无从知道。
package adminhttp

import (
	"net/http"
	"time"

	"recruithelper/client/service/internal/store"
)

type logReportSettingsView struct {
	// 上次上报的时刻与结果。上报是后台静默进行的,"到底传没传出去"只能靠这里回答。
	LastAt    string `json:"lastAt,omitempty"`
	LastOK    bool   `json:"lastOk"`
	LastError string `json:"lastError,omitempty"`
	// 累计发出与丢弃。丢弃量是裁决要求"如实告知"的那一半 —— 除了随载荷上报,
	// 本机也要能看见,否则没人知道前台看到的空白是"没出事"还是"没传出去"。
	SentCount    int64  `json:"sentCount"`
	DroppedCount int64  `json:"droppedCount"`
	Error        string `json:"error,omitempty"`
}

// GET /admin/dev/log-report/settings —— 读开关与上次上报结果。
func (a *API) devLogReportSettings(w http.ResponseWriter, _ *http.Request) {
	if a.st == nil {
		writeJSON(w, http.StatusPreconditionFailed, logReportSettingsView{Error: "存储未装配"})
		return
	}
	setting, err := a.st.LogReportSetting()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, logReportSettingsView{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, logReportSettingsToView(setting))
}

func logReportSettingsToView(setting store.LogReportSetting) logReportSettingsView {
	view := logReportSettingsView{
		LastOK:       setting.LastOK,
		LastError:    setting.LastError,
		SentCount:    setting.SentCount,
		DroppedCount: setting.DroppedCount,
	}
	if setting.LastAt != nil {
		view.LastAt = setting.LastAt.Format(time.RFC3339)
	}
	return view
}
