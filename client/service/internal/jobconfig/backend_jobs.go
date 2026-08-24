package jobconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// PublishParamsDocType 是旧后台职位配置里存放智联发布参数表的文档类型名。
const PublishParamsDocType = "发布参数"

// 发布参数在一个职位上的三种状态。旧后台对空内容刻意放行——若空也拦，存量职位
// 的运营改招呼语点保存都会被这份没碰过的文档挡下（保存是整批提交全部文档）。
// 所以"文档存在"绝不等于"填了参数"：存量职位普遍带着一份空的发布参数。
// 只有 PublishParamsPresent 才代表这个职位真的可以发布。
const (
	PublishParamsPresent = "present"
	PublishParamsEmpty   = "empty"
	PublishParamsAbsent  = "absent"
)

// ErrBackendJobsInvalid 表示旧后台职位列表响应不符合已知形状，而不是
// 某个职位配置不全——后者是列表要如实展示的内容，不是错误。
var ErrBackendJobsInvalid = errors.New("旧后台职位列表响应无效")

// BackendJob 是旧后台一个启用职位在本地诊断面上的最小投影。
//
// 刻意不含任何文档正文与 provider 凭据：旧后台每个 prompt 块都携带
// apiKey/model/baseUrl，整包透传会把 provider key 带进本地读 API。发布参数
// 只投影出三态，正文留在旧后台。
type BackendJob struct {
	JobID         string   `json:"jobId"`
	JobName       string   `json:"jobName"`
	Environment   string   `json:"environment,omitempty"`
	IsCurrent     bool     `json:"isCurrent"`
	DocumentCount int      `json:"documentCount"`
	PublishParams string   `json:"publishParams"`
	MissingDocs   []string `json:"missingDocs,omitempty"`
}

// backendJobsPayload 只声明本地需要的字段。Go 的结构体解码天然丢弃其余部分，
// 这正是不让 provider 凭据进入本地读 API 的构造性保证——别改成 map[string]any。
type backendJob struct {
	ID          json.Number `json:"id"`
	Name        string      `json:"name"`
	Environment string      `json:"environment"`
	// 代招公司(旧后台 jobs.partner_company,2026-08-24 起职位级字段)。
	// 旧后台未升级时字段缺席、未配置时为 null,两者都落成空串=未配置,
	// 失效方向一致:有「职位性质」组的账号会以未配置干净失败,不会乱选。
	PartnerCompany string `json:"partnerCompany"`
}

type backendJobEntry struct {
	Job         *backendJob       `json:"job"`
	Documents   map[string]string `json:"documents"`
	MissingDocs []string          `json:"missingDocs"`
}

type backendJobsPayload struct {
	CurrentJobID json.Number       `json:"currentJobId"`
	Jobs         []backendJobEntry `json:"jobs"`
}

// ParseBackendJobs 把 /client/job-configs 的响应投影成诊断面职位列表。
// 纯函数：不落库、不收编、不做 m5ai 的执行约束校验——那条路径会因为任何一个
// 职位提示词不合格而整批失败，而这张表恰恰要显示配置不全的职位。
func decodeBackendJobs(raw []byte) (backendJobsPayload, string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var payload backendJobsPayload
	if err := decoder.Decode(&payload); err != nil {
		return backendJobsPayload{}, "", fmt.Errorf("%w: JSON 无效", ErrBackendJobsInvalid)
	}
	return payload, strings.TrimSpace(payload.CurrentJobID.String()), nil
}

func backendJobID(job *backendJob, index int) (string, error) {
	if job == nil {
		return "", fmt.Errorf("%w: jobs[%d] 缺少 job", ErrBackendJobsInvalid, index)
	}
	jobID := strings.TrimSpace(job.ID.String())
	if parsed, err := strconv.ParseInt(jobID, 10, 64); err != nil || parsed <= 0 {
		return "", fmt.Errorf("%w: jobs[%d] 的 job id 不是正整数", ErrBackendJobsInvalid, index)
	}
	return jobID, nil
}

func ParseBackendJobs(raw []byte) ([]BackendJob, error) {
	payload, currentJobID, err := decodeBackendJobs(raw)
	if err != nil {
		return nil, err
	}

	jobs := make([]BackendJob, 0, len(payload.Jobs))
	for i := range payload.Jobs {
		entry := payload.Jobs[i]
		jobID, err := backendJobID(entry.Job, i)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, BackendJob{
			JobID:         jobID,
			JobName:       strings.TrimSpace(entry.Job.Name),
			Environment:   strings.TrimSpace(entry.Job.Environment),
			IsCurrent:     currentJobID != "" && jobID == currentJobID,
			DocumentCount: len(entry.Documents),
			PublishParams: publishParamsState(entry.Documents),
			MissingDocs:   entry.MissingDocs,
		})
	}
	return jobs, nil
}

// BackendJobPublishSource 是发布预检用的后台职位投影，**含发布参数正文**。
//
// 与 BackendJob 分成两个类型是刻意的：正文只在脑内参与预检校验，绝不进入
// 任何读 API 响应。类型分离让"正文漏进 UI"成为编译期就写不出来的事，而不是
// 靠每个 handler 记得挑字段。
type BackendJobPublishSource struct {
	JobID          string
	JobName        string
	Environment    string
	IsCurrent      bool
	PublishParams  string
	PartnerCompany string
}

// ParseBackendJobPublishSources 与 ParseBackendJobs 同源，只是多带出发布参数
// 正文供本地校验使用。
func ParseBackendJobPublishSources(raw []byte) ([]BackendJobPublishSource, error) {
	payload, currentJobID, err := decodeBackendJobs(raw)
	if err != nil {
		return nil, err
	}
	out := make([]BackendJobPublishSource, 0, len(payload.Jobs))
	for i := range payload.Jobs {
		entry := payload.Jobs[i]
		jobID, err := backendJobID(entry.Job, i)
		if err != nil {
			return nil, err
		}
		out = append(out, BackendJobPublishSource{
			JobID:          jobID,
			JobName:        strings.TrimSpace(entry.Job.Name),
			Environment:    strings.TrimSpace(entry.Job.Environment),
			IsCurrent:      currentJobID != "" && jobID == currentJobID,
			PublishParams:  entry.Documents[PublishParamsDocType],
			PartnerCompany: strings.TrimSpace(entry.Job.PartnerCompany),
		})
	}
	return out, nil
}

func publishParamsState(documents map[string]string) string {
	content, exists := documents[PublishParamsDocType]
	switch {
	case !exists:
		return PublishParamsAbsent
	case strings.TrimSpace(content) == "":
		return PublishParamsEmpty
	default:
		return PublishParamsPresent
	}
}
