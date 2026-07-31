package adminhttp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/jobconfig"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// JobClassAdvisor 只要 CompleteJSON 一个方法:本地面不需要 patrol 那整套投放
// 编排,拿到建议就地判完即走。
type JobClassAdvisor interface {
	CompleteJSON(context.Context, m5ai.CompletionRequest) (m5ai.CompletionResponse, error)
}

// SetAdvice 注入 LLM 通道。未注入时类别精确匹配仍可用,只是匹配不上时不能由
// 大模型兜,只能干净失败。
func (a *API) SetAdvice(advice JobClassAdvisor) *API {
	a.advice = advice
	return a
}

// jobClassAttempts 是"选不出来"之前允许的尝试次数(甲方 2026-07-30 定为 3)。
// 它只覆盖 provider 网络失败、返回不是合法 JSON、返回的类别名不在候选清单里
// 这三种；模型给出的合法选择一律采纳,置信度低也照采纳——真正的闸是发布前
// 甲方看得见类别再确认。
const jobClassAttempts = 3

type jobClassCandidateView struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
}

type jobClassResolveView struct {
	JobID   string `json:"jobId"`
	JobName string `json:"jobName"`
	// 平台针对这个职位给出的全部可选类别。它是本次决定的封闭候选集。
	Candidates []jobClassCandidateView `json:"candidates"`
	// 平台自动预填的类别(若有),纯诊断:平台只在自己有把握时才填。
	PrefilledClass string `json:"prefilledClass,omitempty"`
	// 定下来的类别,以及它是怎么来的。发布请求要原样带回这个值。
	JobClass string `json:"jobClass"`
	Source   string `json:"source"`
	// 后台配置的职位类别原值。它是死字段,列在这里只为让运营看见"我填的那个
	// 没有参与发布"——不参与任何判定。
	DeadConfiguredClass string   `json:"deadConfiguredClass,omitempty"`
	Confidence          *float64 `json:"confidence,omitempty"`
	Reason              string   `json:"reason,omitempty"`
	// 模型走了几次尝试、每次为什么失败,失败分类不含任何模型原文。
	Attempts []string `json:"attempts,omitempty"`
}

// 类别只有一个来源。2026-07-31 之前还有一支 configuredExactMatch(后台配置值
// 与平台候选归一化精确匹配),三例真机的配置值全部不在候选里,三战三败后按甲方
// 裁决删除——两条并存路径意味着两套失败模式。
const jobClassSourceModel = "model"

// jobPublishClassCandidates 解析一个职位该用哪个职位类别。
//
// 为什么必须先跑一趟手侧:类别候选是平台读了工作性质/职位名称/职位描述之后才给
// 的,选择器在那之前打不开。而手不能调大模型(插件永远不接触 key),所以只能
// 两趟——本接口是第一趟,拿候选并定下类别;发布是第二趟,带着定好的精确值去选中。
func (a *API) jobPublishClassCandidates(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Platform   string `json:"platform"`
		AccountRef string `json:"accountRef"`
		JobID      string `json:"jobId"`
		// 紧接着还要读关键词词库时置 true:手读完候选把填好三项的表单留在
		// 发布页,下一趟省掉一次填表与描述失焦后的数十秒等待。单独读一次候选
		// 的人不该在页面上留半张表,所以默认 false。
		KeepForm bool `json:"keepForm"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法请求体"})
		return
	}
	key, err := validateAccountKey(req.Platform, req.AccountRef)
	req.JobID = strings.TrimSpace(req.JobID)
	if err != nil || req.JobID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的平台、账号或职位标识"})
		return
	}
	if a.st == nil || a.hub == nil || a.disp == nil || a.jobConfigSource == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "职位类别解析服务尚未就绪"})
		return
	}
	// 与预检、试填、发布同一道闸:这趟也要占用页面并导航。
	batch, err := a.st.ActiveSourcingBatch(key)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "采集批次状态不可读"})
		return
	}
	if batch != nil {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "当前有采集批次在运行，读取职位类别候选会打断推荐流；请先结束批次",
		})
		return
	}
	account, sessionID, bootID, err := a.currentCandidateAccount(key)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "账号身份或手会话当前不可用"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 250*time.Second)
	defer cancel()
	target, spec, failure := a.resolvePublishTarget(ctx, req.JobID)
	if failure != nil {
		writeJSON(w, failure.status, map[string]string{"error": failure.message})
		return
	}

	args, err := protocol.Encode(protocol.JobReadClassCandidatesArgs{
		JobName:        target.JobName,
		EmploymentType: spec.EmploymentType,
		Description:    spec.Description,
		KeepForm:       req.KeepForm,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "类别候选读取命令构造失败"})
		return
	}
	if err := protocol.ValidatePrimitiveArgs(protocol.PrimJobReadClassCandidates, 1, args); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "类别候选读取参数不符合当前契约"})
		return
	}
	logicalRef, err := a.disp.DispatchStructured(dispatch.DispatchRequest{
		HandID: account.BoundHandID, ExpectedSession: sessionID, ExpectedBootID: bootID,
		Name: protocol.PrimJobReadClassCandidates, Args: args,
		Context: &protocol.CmdContext{
			Platform: key.Platform, AccountRef: key.AccountRef,
			ExpectedPrincipalFingerprint: *account.PrincipalFingerprint,
		},
	})
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "类别候选读取未能派发"})
		return
	}
	logical, err := a.disp.WaitLogical(ctx, logicalRef)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "类别候选读取未完成"})
		return
	}
	data, err := parseClassCandidatesProof(logicalRef, logical)
	if err != nil {
		response := map[string]any{"error": "类别候选读取未成功：" + err.Error()}
		if diagnostics := prepareDraftFailureDiagnostics(logical); diagnostics != nil {
			response["diagnostics"] = diagnostics
		}
		writeJSON(w, http.StatusConflict, response)
		return
	}

	view := jobClassResolveView{
		JobID: target.JobID, JobName: target.JobName,
		DeadConfiguredClass: strings.TrimSpace(spec.DeadJobClass),
	}
	names := make([]string, 0, len(data.Candidates))
	for _, candidate := range data.Candidates {
		names = append(names, candidate.Name)
		view.Candidates = append(view.Candidates, jobClassCandidateView{
			Name: candidate.Name, Definition: candidate.Definition,
		})
	}
	if data.PrefilledClass != nil {
		view.PrefilledClass = *data.PrefilledClass
	}
	if len(names) == 0 {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "平台没有给出任何职位类别候选，无法选定；请人工到发布页确认",
			"view":  view,
		})
		return
	}

	if a.advice == nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "LLM 通道未就绪，无法选定职位类别",
			"view":  view,
		})
		return
	}

	suggestion, attempts, err := a.chooseJobClassByModel(
		ctx, target, spec.Description, data.Candidates, names,
	)
	view.Attempts = attempts
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "职位类别未能选定：" + err.Error(),
			"view":  view,
		})
		return
	}
	confidence := suggestion.Confidence
	view.JobClass, view.Source = suggestion.Class, jobClassSourceModel
	view.Confidence, view.Reason = &confidence, suggestion.Reason
	writeJSON(w, http.StatusOK, view)
}

// chooseJobClassByModel 让大模型从平台候选里选一个。
//
// 三种失败都重试:provider 调用失败、返回不是合法 JSON、返回的类别名不在候选
// 清单里。最后一种是最要紧的——模型改了一个字就绝不放行,更不做模糊匹配:
// 类别选错会把职位推给错误的人群,而页面看上去一切正常。
func (a *API) chooseJobClassByModel(
	ctx context.Context,
	target *jobconfig.BackendJobPublishSource,
	description string,
	candidates []protocol.JobClassCandidate,
	names []string,
) (m5ai.JobClassSuggestion, []string, error) {
	inputs := make([]m5ai.JobClassCandidateInput, 0, len(candidates))
	for _, candidate := range candidates {
		inputs = append(inputs, m5ai.JobClassCandidateInput{
			Name: candidate.Name, Definition: candidate.Definition,
		})
	}
	content, err := m5ai.RenderJobClassPrompt(target.JobName, description, inputs)
	if err != nil {
		return m5ai.JobClassSuggestion{}, nil, err
	}
	// 同一职位同一批候选下 invocationId 稳定可复算,便于把 ai-traces 里的记录
	// 与本次决定对上。
	base := hashJobClassInput(target.JobID, content)
	var attempts []string
	for attempt := 1; attempt <= jobClassAttempts; attempt++ {
		started := time.Now()
		response, callErr := a.advice.CompleteJSON(ctx, m5ai.CompletionRequest{
			InvocationID:        "jc-" + base + "-" + strconv.Itoa(attempt),
			Purpose:             m5ai.PurposeJobClass,
			ContextRevisionHash: base,
			PromptRevision:      m5ai.JobClassInputFormatVersion,
			UserContent:         content,
			MaxOutputTokens:     m5ai.JobClassOutputTokenLimit,
		})
		latency := time.Since(started)
		if callErr != nil {
			attempts = append(attempts, "attempt"+strconv.Itoa(attempt)+":providerError")
			a.auditJobClassCall(target, attempt, "providerError", response, latency)
			continue
		}
		suggestion, parseErr := m5ai.ParseJobClassSuggestion(response.JSONText)
		if parseErr != nil {
			attempts = append(attempts, "attempt"+strconv.Itoa(attempt)+":"+parseErr.Error())
			a.auditJobClassCall(target, attempt, parseErr.Error(), response, latency)
			continue
		}
		if !jobconfig.ContainsPlatformJobClass(suggestion.Class, names) {
			attempts = append(attempts, "attempt"+strconv.Itoa(attempt)+":classNotInCandidates")
			a.auditJobClassCall(target, attempt, "classNotInCandidates", response, latency)
			continue
		}
		attempts = append(attempts, "attempt"+strconv.Itoa(attempt)+":ok")
		a.auditJobClassCall(target, attempt, "ok", response, latency)
		return suggestion, attempts, nil
	}
	return m5ai.JobClassSuggestion{}, attempts,
		fmt.Errorf("大模型 %d 次尝试都没给出候选清单内的合法结果", jobClassAttempts)
}

// auditJobClassCall 留一条无正文的诊断痕迹。按 AI provider 数据边界,这里只允许
// 用途、provider/model、输入输出规模、延迟、状态、错误分类与追踪状态;**不写**
// 提示词、模型原文、选定的类别名与理由(那些只经 HTTP 响应交给运营看)。
func (a *API) auditJobClassCall(
	target *jobconfig.BackendJobPublishSource,
	attempt int,
	outcome string,
	response m5ai.CompletionResponse,
	latency time.Duration,
) {
	if a.st == nil {
		return
	}
	status := ""
	if response.Diagnostics.ProviderHTTPStatus != nil {
		status = " httpStatus=" + strconv.Itoa(*response.Diagnostics.ProviderHTTPStatus)
	}
	detail := fmt.Sprintf(
		"purpose=jobClass jobId=%s attempt=%d/%d outcome=%s latencyMs=%d "+
			"inTokens=%d outTokens=%d reqBytes=%d resBytes=%d traceStatus=%s%s",
		target.JobID, attempt, jobClassAttempts, outcome, latency.Milliseconds(),
		response.Usage.InputTokens, response.Usage.OutputTokens,
		response.Diagnostics.RequestBytes, response.Diagnostics.ResponseBytes,
		response.Diagnostics.TraceStatus, status,
	)
	a.st.Audit("job_class_ai", "", "", detail)
	if response.Diagnostics.TraceStatus != "" &&
		response.Diagnostics.TraceStatus != m5ai.TraceStatusComplete {
		// 追踪写入失败不改变业务裁决,但必须响亮报告。
		slog.Error("职位类别 AI 调用未能完整落追踪库",
			"jobId", target.JobID, "attempt", attempt,
			"traceStatus", response.Diagnostics.TraceStatus,
			"traceErrorCode", response.Diagnostics.TraceErrorCode)
	}
}

func hashJobClassInput(jobID, content string) string {
	sum := sha256.Sum256([]byte("jobClass\x00" + jobID + "\x00" + content))
	return hex.EncodeToString(sum[:12])
}

// parseClassCandidatesProof 只接受叶子命令 ok 且 result 符合契约的结果。
func parseClassCandidatesProof(
	msgID string,
	logical *store.LogicalDispatchState,
) (protocol.JobReadClassCandidatesData, error) {
	var zero protocol.JobReadClassCandidatesData
	if logical == nil || !logical.Settled {
		return zero, errors.New("命令未终局")
	}
	leaf := logical.Leaf
	if leaf.Name != protocol.PrimJobReadClassCandidates || leaf.Status != store.CmdOk ||
		leaf.ResultBody == "" {
		return zero, errors.New("未取得成功终局")
	}
	resultRaw := json.RawMessage(leaf.ResultBody)
	if err := protocol.ValidatePrimitiveResult(protocol.PrimJobReadClassCandidates, 1, resultRaw); err != nil {
		return zero, errors.New("结果不符合契约")
	}
	var result protocol.ResultBody
	if err := json.Unmarshal(resultRaw, &result); err != nil ||
		result.Ref != msgID || result.Status != protocol.ResultStatusOk {
		return zero, errors.New("结果关联无效")
	}
	var data protocol.JobReadClassCandidatesData
	if err := json.Unmarshal(result.Data, &data); err != nil {
		return zero, errors.New("数据无法解析")
	}
	return data, nil
}
