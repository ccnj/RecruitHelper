// Package jobclassreport 把职位类别的"平台可选 / 系统选定 / 最终实发"上报旧后台。
//
// 第十项获准云端出站(AGENTS.md「职位类别审计上报」,2026-08-23 甲方裁决)。
// 纪律:仅供我方事后审计,只上行——响应任何字段不得成为业务裁决、配置或控制
// 指令的来源;失败不重试、不建发件箱、只响亮记日志;成败都不影响类别分配与
// 发布本身;脑侧不为此新增任何持久化,审计数据允许缺口。
// 载荷是白名单:鉴权对、客户端版本、每条记录的观察时刻、后台职位 ID、职位名、
// 平台、阶段(planned / published)、候选全集(名称 + 平台官方释义)、预填、选定、
// 来源、置信度、一句话依据、未分到原因分类。不含候选人任何信息。
package jobclassreport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// UploadTimeout 是单次 HTTP 上传的上限。
const UploadTimeout = 15 * time.Second

// 服务端字段上限(与旧后台 client_job_class_reports 的 pydantic 模型对齐)。
// 超长一律截断而不是整批被 422 拒掉——审计宁可少几个字,不要整条丢。
const (
	maxRecordsPerUpload = 100
	maxCandidates       = 50
	maxClassNameRunes   = 64
	maxDefinitionRunes  = 400
	maxJobNameRunes     = 256
	maxReasonRunes      = 500
	maxProblemRunes     = 64
	maxSourceRunes      = 32
	maxPlatformRunes    = 32
)

const (
	StagePlanned   = "planned"
	StagePublished = "published"
)

// Target 是上报去处与身份,取自已获准的旧后台配置,不新增配置面。
type Target struct {
	BaseURL      string
	MachineID    string
	LicenseToken string
}

func (t Target) valid() error {
	if strings.TrimSpace(t.BaseURL) == "" {
		return errors.New("旧后台地址未配置")
	}
	if strings.TrimSpace(t.MachineID) == "" || strings.TrimSpace(t.LicenseToken) == "" {
		return errors.New("授权未就绪(缺 machineId 或 licenseToken)")
	}
	return nil
}

// Candidate 是平台给出的一个可选类别:名称与平台官方释义。
type Candidate struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
}

// Record 是一条审计记录。字段面是白名单,新增字段须先修 AGENTS.md 条款。
type Record struct {
	JobID          string      `json:"jobId"`
	JobName        string      `json:"jobName,omitempty"`
	Platform       string      `json:"platform"`
	Stage          string      `json:"stage"`
	ObservedAt     int64       `json:"observedAt"`
	Candidates     []Candidate `json:"candidates"`
	PrefilledClass string      `json:"prefilledClass,omitempty"`
	ChosenClass    string      `json:"chosenClass,omitempty"`
	Source         string      `json:"source,omitempty"`
	Confidence     *float64    `json:"confidence,omitempty"`
	Reason         string      `json:"reason,omitempty"`
	Problem        string      `json:"problem,omitempty"`
}

// Payload 是上报正文。
type Payload struct {
	MachineID     string   `json:"machineId"`
	LicenseToken  string   `json:"licenseToken"`
	ClientVersion string   `json:"clientVersion,omitempty"`
	Records       []Record `json:"records"`
}

// Reporter 执行上报。
type Reporter struct {
	// Target 返回 ready=false 表示授权未就绪(未激活),此时静默跳过。
	Target        func() (Target, bool)
	ClientVersion string
	// Upload 默认走 HTTP;测试替换它。
	Upload func(ctx context.Context, target Target, payload Payload) error
}

// Report 同步上报一批记录;超过单次上限按 100 条一批切开逐批上传。
// 错误只交给调用方记日志。
func (r *Reporter) Report(ctx context.Context, records []Record) error {
	if r == nil || r.Target == nil {
		return errors.New("职位类别审计上报未接线")
	}
	target, ready := r.Target()
	if !ready {
		return nil
	}
	if err := target.valid(); err != nil {
		return err
	}
	clean := make([]Record, 0, len(records))
	for _, record := range records {
		if sanitized, ok := sanitize(record); ok {
			clean = append(clean, sanitized)
		}
	}
	if len(clean) == 0 {
		return nil
	}
	upload := r.Upload
	if upload == nil {
		upload = Upload
	}
	for start := 0; start < len(clean); start += maxRecordsPerUpload {
		end := start + maxRecordsPerUpload
		if end > len(clean) {
			end = len(clean)
		}
		payload := Payload{ClientVersion: truncateRunes(r.ClientVersion, 32), Records: clean[start:end]}
		if err := upload(ctx, target, payload); err != nil {
			return err
		}
	}
	return nil
}

// ReportAsync 是 fire-and-forget 形态:自带超时,成败只记日志。
// 两个调用点(阶段 A 分配完成、发布取得正证)都不该被上报拖慢或拖垮。
func (r *Reporter) ReportAsync(records []Record) {
	if r == nil || len(records) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*UploadTimeout)
		defer cancel()
		if err := r.Report(ctx, records); err != nil {
			slog.Warn("职位类别审计上报失败(不影响分配与发布)",
				"errorCode", "jobClassReportFailed", "stage", records[0].Stage,
				"records", len(records), "err", err)
			return
		}
		slog.Info("职位类别审计已上报旧后台", "stage", records[0].Stage, "records", len(records))
	}()
}

// sanitize 把一条记录收进服务端白名单的形状:jobId 必须是正整数(后台职位 ID),
// 文本按上限截断,候选超过 50 条只留前 50,置信度夹到 [0,1]。不合格的整条丢弃
// 并返回 false。
func sanitize(record Record) (Record, bool) {
	jobID := strings.TrimSpace(record.JobID)
	if n, err := strconv.ParseInt(jobID, 10, 64); err != nil || n <= 0 {
		return Record{}, false
	}
	if record.Stage != StagePlanned && record.Stage != StagePublished {
		return Record{}, false
	}
	out := Record{
		JobID:          jobID,
		JobName:        truncateRunes(record.JobName, maxJobNameRunes),
		Platform:       truncateRunes(strings.TrimSpace(record.Platform), maxPlatformRunes),
		Stage:          record.Stage,
		ObservedAt:     record.ObservedAt,
		Candidates:     make([]Candidate, 0, len(record.Candidates)),
		PrefilledClass: truncateRunes(record.PrefilledClass, maxClassNameRunes),
		ChosenClass:    truncateRunes(record.ChosenClass, maxClassNameRunes),
		Source:         truncateRunes(record.Source, maxSourceRunes),
		Reason:         truncateRunes(record.Reason, maxReasonRunes),
		Problem:        truncateRunes(record.Problem, maxProblemRunes),
	}
	if out.Platform == "" {
		return Record{}, false
	}
	if out.ObservedAt < 0 {
		out.ObservedAt = 0
	}
	if record.Confidence != nil {
		value := *record.Confidence
		if value < 0 {
			value = 0
		} else if value > 1 {
			value = 1
		}
		out.Confidence = &value
	}
	for _, candidate := range record.Candidates {
		name := truncateRunes(strings.TrimSpace(candidate.Name), maxClassNameRunes)
		if name == "" {
			continue
		}
		out.Candidates = append(out.Candidates, Candidate{
			Name: name, Definition: truncateRunes(candidate.Definition, maxDefinitionRunes),
		})
		if len(out.Candidates) >= maxCandidates {
			break
		}
	}
	return out, true
}

func truncateRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// Upload 把一批记录 POST 到旧后台。失败只返回错误,不重试。
// 回执只看 HTTP 状态码——只上行,不给回执留可读出的地方。
func Upload(ctx context.Context, target Target, payload Payload) error {
	payload.MachineID = target.MachineID
	payload.LicenseToken = target.LicenseToken
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化职位类别审计: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, UploadTimeout)
	defer cancel()

	endpoint := strings.TrimRight(strings.TrimSpace(target.BaseURL), "/") +
		"/api/v1/client/job-class-report"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := (&http.Client{Timeout: UploadTimeout}).Do(request)
	if err != nil {
		return fmt.Errorf("上传失败: %w", err)
	}
	defer response.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(response.Body, 512))
	if response.StatusCode != http.StatusOK {
		// 错误信息要进普通日志,只带状态码与一小段说明,不整段回显(licenseToken)。
		return fmt.Errorf("上传被拒(HTTP %d): %s", response.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}
