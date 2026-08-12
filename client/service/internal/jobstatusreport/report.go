// Package jobstatusreport 把采集开启闸读到的平台职位状态上报旧后台。
//
// 第八项获准云端出站(AGENTS.md「职位平台状态上报」,2026-08-12 甲方裁决)。
// 纪律:观察用途,只上行——响应任何字段不得成为业务裁决、配置或控制指令的
// 来源;失败不重试、不建发件箱、只响亮记日志;成败都不影响采集放行与否。
// 载荷是白名单:每职位仅含后台职位 ID、平台状态原样文案与观察时刻,不含
// 候选人任何信息。
package jobstatusreport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"recruithelper/client/service/internal/jobconfig"
	"recruithelper/contract/gen/go/protocol"
)

// UploadTimeout 是一次上报里单个 HTTP 调用(取职位清单、上传)的上限。
const UploadTimeout = 15 * time.Second

// Target 是上报去处与身份,取自已获准的旧后台配置,不新增配置面。
// machineId 与 licenseToken 是旧后台 verify_client 的鉴权对,缺一即 401。
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

// JobSource 提供本客户全部后台职位。生产实现是 jobconfig.Source。
type JobSource interface {
	FetchAll(ctx context.Context) ([]byte, error)
}

// Reporter 执行一次"取后台职位清单 → 按平台分区判定各职位状态 → 上传"。
type Reporter struct {
	Source JobSource
	// Target 返回 ready=false 表示授权未就绪(未激活),此时静默跳过。
	Target func() (Target, bool)
	// Upload 默认走 HTTP;测试替换它。
	Upload func(ctx context.Context, target Target, payload Payload) error
}

// Payload 是上报正文。字段面是白名单,新增字段须先修 AGENTS.md 条款。
type Payload struct {
	MachineID    string      `json:"machineId"`
	LicenseToken string      `json:"licenseToken"`
	ObservedAt   int64       `json:"observedAt"`
	Jobs         []JobStatus `json:"jobs"`
}

type JobStatus struct {
	JobID  string `json:"jobId"`
	Status string `json:"status"`
}

// Report 同步执行一次上报;错误只交给调用方记日志。
func (r *Reporter) Report(ctx context.Context, data protocol.JobReadPublishedListData) error {
	if r == nil || r.Source == nil || r.Target == nil {
		return errors.New("职位状态上报未接线")
	}
	target, ready := r.Target()
	if !ready {
		return nil
	}
	if err := target.valid(); err != nil {
		return err
	}

	fetchCtx, cancel := context.WithTimeout(ctx, UploadTimeout)
	raw, err := r.Source.FetchAll(fetchCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("读取后台职位清单: %w", err)
	}
	jobs, err := jobconfig.ParseBackendJobs(raw)
	if err != nil {
		return fmt.Errorf("解析后台职位清单: %w", err)
	}

	payload := Payload{ObservedAt: data.ObservedAt, Jobs: make([]JobStatus, 0, len(jobs))}
	seen := make(map[string]struct{}, len(jobs))
	for _, job := range jobs {
		if _, duplicate := seen[job.JobID]; duplicate {
			continue
		}
		seen[job.JobID] = struct{}{}
		payload.Jobs = append(payload.Jobs, JobStatus{
			JobID:  job.JobID,
			Status: jobconfig.FindPostingStatus(job.JobName, data.Sections),
		})
	}
	if len(payload.Jobs) == 0 {
		return nil
	}

	if r.Upload != nil {
		return r.Upload(ctx, target, payload)
	}
	return Upload(ctx, target, payload)
}

// Upload 把一份状态观察 POST 到旧后台。失败只返回错误,不重试。
// 回执只看 HTTP 状态码——只上行,不给回执留可读出的地方。
func Upload(ctx context.Context, target Target, payload Payload) error {
	payload.MachineID = target.MachineID
	payload.LicenseToken = target.LicenseToken
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化职位状态: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, UploadTimeout)
	defer cancel()

	endpoint := strings.TrimRight(strings.TrimSpace(target.BaseURL), "/") +
		"/api/v1/client/job-platform-status"
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
