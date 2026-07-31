// 现场数据上报的诊断台入口(AGENTS.md「全局约定·现场数据上报」,2026-07-31
// 甲方裁决)。POST /admin/dev/report:现场打包 brain.log / brain.db /
// ai-traces.db,上传到旧后台,把回执原样回显在同一页。
//
// 按裁决只由人显式点击触发 —— 这里没有定时器、没有重试、没有发件箱。传失败就
// 报错,人再点一次。
package adminhttp

import (
	"net/http"

	"recruithelper/client/service/internal/report"
)

// FieldReportDeps 是打包上报要用到的外部依赖。用 Setter 注入而不是塞进
// New() 的参数表:那个签名被十几处测试和装配点引用,为一个开发期工具改它
// 不划算。
type FieldReportDeps struct {
	DataDir       string
	LogDir        string
	AppVersion    string
	TraceSnapshot report.SnapshotFunc
}

func (a *API) SetFieldReportDeps(deps FieldReportDeps) *API {
	a.fieldReport = deps
	return a
}

type fieldReportResponse struct {
	OK        bool            `json:"ok"`
	ReportKey string          `json:"reportKey,omitempty"`
	SizeBytes int64           `json:"sizeBytes,omitempty"`
	SHA256    string          `json:"sha256,omitempty"`
	Manifest  report.Manifest `json:"manifest"`
	Error     string          `json:"error,omitempty"`
}

func (a *API) devReport(w http.ResponseWriter, r *http.Request) {
	if a.jobConfigSource == nil {
		writeFieldReportError(w, http.StatusPreconditionFailed, "旧后台配置未装配")
		return
	}
	config, err := a.jobConfigSource.LoadConfig()
	if err != nil || config == nil {
		writeFieldReportError(w, http.StatusPreconditionFailed, "授权未就绪，先在「模型与配置」页完成绑定")
		return
	}

	var brainSnapshot report.SnapshotFunc
	if a.st != nil {
		brainSnapshot = a.st.SnapshotTo
	}

	pack, cleanup, err := report.Build(report.Options{
		DataDir:       a.fieldReport.DataDir,
		LogDir:        a.fieldReport.LogDir,
		AppVersion:    a.fieldReport.AppVersion,
		BrainSnapshot: brainSnapshot,
		TraceSnapshot: a.fieldReport.TraceSnapshot,
	})
	if err != nil {
		writeFieldReportError(w, http.StatusInternalServerError, "打包失败："+err.Error())
		return
	}
	// 临时目录里是候选人全库明文,无论成败都要落地清掉。
	defer cleanup()

	receipt, err := report.Upload(r.Context(), pack, report.Target{
		BaseURL:      config.BaseURL,
		MachineID:    config.MachineID,
		LicenseToken: config.LicenseToken,
		AppVersion:   a.fieldReport.AppVersion,
	})
	if err != nil {
		// 打包成功、上传失败时仍然把清单回显:人能看到本来要传什么。
		writeJSON(w, http.StatusBadGateway, fieldReportResponse{
			Manifest: pack.Manifest,
			Error:    err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, fieldReportResponse{
		OK:        true,
		ReportKey: receipt.ReportKey,
		SizeBytes: receipt.SizeBytes,
		SHA256:    receipt.SHA256,
		Manifest:  pack.Manifest,
	})
}

func writeFieldReportError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, fieldReportResponse{Error: message})
}
