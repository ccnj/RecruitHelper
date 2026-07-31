// 现场数据上报的诊断台入口(AGENTS.md「全局约定·现场数据上报」,2026-07-31
// 甲方裁决)。POST /admin/dev/report:现场打包 brain.log / brain.db /
// ai-traces.db,上传到旧后台,把回执原样回显在同一页。
//
// 按裁决只由人显式点击触发 —— 这里没有定时器、没有重试、没有发件箱。传失败就
// 报错,人再点一次。
package adminhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"recruithelper/client/service/internal/report"
	"recruithelper/client/service/internal/store"
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

// buildAndUploadFieldReport 是打包上传的唯一实现。诊断台点击与每日自动上传都
// 走这里 —— 两条触发路径共用一条代码路径,不存在"自动的那条少一道闸"。
// 返回 manifest 是为了失败时也能回显本来要传什么。
func (a *API) buildAndUploadFieldReport(ctx context.Context) (*report.Receipt, report.Manifest, error) {
	if a.jobConfigSource == nil {
		return nil, report.Manifest{}, errors.New("旧后台配置未装配")
	}
	config, err := a.jobConfigSource.LoadConfig()
	if err != nil || config == nil {
		return nil, report.Manifest{}, errors.New("授权未就绪，先在「模型与配置」页完成绑定")
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
		return nil, report.Manifest{}, fmt.Errorf("打包失败：%w", err)
	}
	// 临时目录里是候选人全库明文,无论成败都要落地清掉。
	defer cleanup()

	receipt, err := report.Upload(ctx, pack, report.Target{
		BaseURL:      config.BaseURL,
		MachineID:    config.MachineID,
		LicenseToken: config.LicenseToken,
		AppVersion:   a.fieldReport.AppVersion,
	})
	if err != nil {
		return nil, pack.Manifest, err
	}
	return receipt, pack.Manifest, nil
}

// RunFieldReportOnce 供每日自动上传调用。装配方把它接进 report.SchedulerDeps。
func (a *API) RunFieldReportOnce(ctx context.Context) error {
	_, _, err := a.buildAndUploadFieldReport(ctx)
	return err
}

func (a *API) devReport(w http.ResponseWriter, r *http.Request) {
	receipt, manifest, err := a.buildAndUploadFieldReport(r.Context())
	if err != nil {
		status := http.StatusBadGateway
		if receipt == nil && manifest.PackedAt == "" {
			// 连包都没打出来:是前置条件或本机问题,不是上传被拒。
			status = http.StatusPreconditionFailed
		}
		writeJSON(w, status, fieldReportResponse{Manifest: manifest, Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, fieldReportResponse{
		OK:        true,
		ReportKey: receipt.ReportKey,
		SizeBytes: receipt.SizeBytes,
		SHA256:    receipt.SHA256,
		Manifest:  manifest,
	})
}

func writeFieldReportError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, fieldReportResponse{Error: message})
}

type fieldReportSettingsView struct {
	AutoUploadEnabled bool   `json:"autoUploadEnabled"`
	LastAutoAt        string `json:"lastAutoAt,omitempty"`
	LastAutoOK        bool   `json:"lastAutoOk"`
	LastAutoError     string `json:"lastAutoError,omitempty"`
	Error             string `json:"error,omitempty"`
}

// GET /admin/dev/report/settings —— 读自动上传开关与上次执行结果。
func (a *API) devReportSettings(w http.ResponseWriter, _ *http.Request) {
	if a.st == nil {
		writeJSON(w, http.StatusPreconditionFailed, fieldReportSettingsView{Error: "存储未装配"})
		return
	}
	setting, err := a.st.FieldReportSetting()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, fieldReportSettingsView{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, fieldReportSettingsToView(setting))
}

// POST /admin/dev/report/settings —— 开关自动上传。
//
// 这是裁决允许的**唯一**开启路径:安装、升级、迁移、配置下发都不得把它翻成开启。
func (a *API) setDevReportSettings(w http.ResponseWriter, r *http.Request) {
	if a.st == nil {
		writeJSON(w, http.StatusPreconditionFailed, fieldReportSettingsView{Error: "存储未装配"})
		return
	}
	var body struct {
		AutoUploadEnabled *bool `json:"autoUploadEnabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AutoUploadEnabled == nil {
		writeJSON(w, http.StatusBadRequest, fieldReportSettingsView{Error: "缺少 autoUploadEnabled"})
		return
	}
	if err := a.st.SetFieldReportAutoUpload(*body.AutoUploadEnabled); err != nil {
		writeJSON(w, http.StatusInternalServerError, fieldReportSettingsView{Error: err.Error()})
		return
	}
	setting, err := a.st.FieldReportSetting()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, fieldReportSettingsView{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, fieldReportSettingsToView(setting))
}

func fieldReportSettingsToView(setting store.FieldReportSetting) fieldReportSettingsView {
	view := fieldReportSettingsView{
		AutoUploadEnabled: setting.AutoUploadEnabled,
		LastAutoOK:        setting.LastAutoOK,
		LastAutoError:     setting.LastAutoError,
	}
	if setting.LastAutoAt != nil {
		view.LastAutoAt = setting.LastAutoAt.Format(time.RFC3339)
	}
	return view
}
