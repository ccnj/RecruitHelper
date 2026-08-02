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

// SetAdvice 注入 LLM 通道。未注入时类别一步都走不了——2026-07-31 起没有
// "后台配置值精确匹配"那条不依赖模型的旁路了。
func (a *API) SetAdvice(advice JobClassAdvisor) *API {
	a.advice = advice
	return a
}

// jobClassAttempts 是"分不出来"之前允许的尝试次数(甲方 2026-07-30 定为 3)。
// 整批重试:分配相互耦合,只重一个会破坏差异化。3 次之后按 2026-08-01 裁决
// 保留合法的分配、跳过不合法的,不让一个坏职位废掉整批。
const jobClassAttempts = 3

// jobClassChunkSize 是一次模型调用最多带几个职位。
//
// **它守的是输入侧,不是输出侧。** 输出预算 2026-08-01 起全局统一到 10240,
// 一次要多少上限就报多少上限,不再按职位数估算——max_tokens 是上限不是预付
// 额度,模型不吐就不计费,而估算一旦估低就是整批干净失败(2026-08-02 客户机
// 10 个职位全废,就是每条按 40 token 估、算出 432、被 finish_reason=length
// 切断)。把这两个约束绑在一个数上正是那次搞混的根源,现在分开。
//
// 真正约束一次带几个职位的是两样:maxProviderRequestBytes(256 KB,我们自己的保守
// 自限、不是平台限制)与调用
// 延迟。取 12 的依据是实测:客户机 10 个职位的请求是 47.9 KB,attempt 2/3 都在
// 6~9 秒内正常返回过;12 个约 58 KB,占硬闸 23%,仍在验证过的邻域里。超过 12
// 个才分块,块间靠 occupied 延续差异化。
const jobClassChunkSize = 12

type jobClassCandidateView struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
}

// jobClassAssignmentView 是一个职位的分配结论。JobClass 为空即表示没分到,
// 原因看 Problem。
type jobClassAssignmentView struct {
	JobID   string `json:"jobId"`
	JobName string `json:"jobName"`
	// 平台针对这个职位给出的全部可选类别。它是该职位这次决定的封闭候选集。
	Candidates []jobClassCandidateView `json:"candidates"`
	// 平台自动预填的类别(若有),纯诊断:平台只在自己有把握时才填。
	PrefilledClass string `json:"prefilledClass,omitempty"`
	// 定下来的类别。发布请求要原样带回这个值。
	JobClass   string   `json:"jobClass,omitempty"`
	Source     string   `json:"source,omitempty"`
	Confidence *float64 `json:"confidence,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	// 没分到时的原因分类,或读候选阶段的失败说明。
	Problem string `json:"problem,omitempty"`
	// 后台配置的职位类别原值。死字段,列在这里只为让运营看见"我填的那个没有
	// 参与发布"——不参与任何判定。
	DeadConfiguredClass string `json:"deadConfiguredClass,omitempty"`
}

type jobClassPlanView struct {
	Jobs []jobClassAssignmentView `json:"jobs"`
	// 被多个职位共用的类别 → 那几个职位。差异化不是闸,撞车照常放行,但要让
	// 运营在二次确认清单上看见。
	Collisions map[string][]string `json:"collisions,omitempty"`
	// 模型走了几次尝试、每次为什么失败,失败分类不含任何模型原文。
	Attempts []string `json:"attempts,omitempty"`
}

const jobClassSourceModel = "model"

// jobPublishClassPlan 为一批职位统一分配职位类别。
//
// 为什么必须先跑手侧:类别候选是平台读了工作性质/职位名称/职位描述之后才给的,
// 选择器在那之前打不开。而手不能调大模型(插件永远不接触 key),所以只能两趟——
// 本接口是第一趟,逐个职位拿候选、再一次性定下全批类别;关键词词库与真发各是
// 后面的趟次。
//
// 为什么统一分配:类别决定平台把职位推给哪一类求职者。发多个职位的核心诉求是
// 扩充候选人池,若它们全落进同一个类别,平台就把它们推给同一批人,多发的那些
// 等于白发。逐个决策时模型看不见别的职位在选什么,撞车完全没人管。
//
// 全程零对外副作用:只派 intrusive/platformSideEffect=none 的读取原语,不填其余
// 字段、不提交任何东西。
func (a *API) jobPublishClassPlan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Platform   string   `json:"platform"`
		AccountRef string   `json:"accountRef"`
		JobIDs     []string `json:"jobIds"`
		// 已经被别的职位占用、请模型尽量避开的类别。整批分配时留空;运营单独
		// 重跑某一个职位时把其余职位已定的类别传进来。
		Occupied []string `json:"occupied"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法请求体"})
		return
	}
	key, err := validateAccountKey(req.Platform, req.AccountRef)
	jobIDs := trimmedUnique(req.JobIDs)
	if err != nil || len(jobIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的平台、账号或职位标识"})
		return
	}
	if a.st == nil || a.hub == nil || a.disp == nil || a.jobConfigSource == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "职位类别解析服务尚未就绪"})
		return
	}
	if a.advice == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "LLM 通道未就绪，无法分配职位类别",
		})
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

	// 每个职位一趟填页(填三项 + 等描述失焦),真机量级是数十秒。整批超时按职位
	// 数放,单职位仍按原来的 250 秒。
	budget := time.Duration(len(jobIDs)) * 250 * time.Second
	ctx, cancel := context.WithTimeout(r.Context(), budget)
	defer cancel()

	view := jobClassPlanView{Jobs: make([]jobClassAssignmentView, 0, len(jobIDs))}
	inputs := make([]m5ai.JobClassJobInput, 0, len(jobIDs))
	byJobID := make(map[string]int, len(jobIDs))
	for _, jobID := range jobIDs {
		row := jobClassAssignmentView{JobID: jobID}
		target, spec, failure := a.resolvePublishTarget(ctx, jobID)
		if failure != nil {
			row.Problem = failure.message
			byJobID[jobID] = len(view.Jobs)
			view.Jobs = append(view.Jobs, row)
			continue
		}
		row.JobName = target.JobName
		row.DeadConfiguredClass = strings.TrimSpace(spec.DeadJobClass)

		data, readErr := a.readJobClassCandidates(
			ctx, key, account, sessionID, bootID, target.JobName, spec,
		)
		if readErr != nil {
			// 单个职位读不到候选就跳过它,继续读其余——一个坏职位不该让整批
			// 的类别决定都做不成。
			row.Problem = readErr.Error()
			byJobID[jobID] = len(view.Jobs)
			view.Jobs = append(view.Jobs, row)
			continue
		}
		if data.PrefilledClass != nil {
			row.PrefilledClass = *data.PrefilledClass
		}
		candidates := make([]m5ai.JobClassCandidateInput, 0, len(data.Candidates))
		for _, candidate := range data.Candidates {
			row.Candidates = append(row.Candidates, jobClassCandidateView{
				Name: candidate.Name, Definition: candidate.Definition,
			})
			candidates = append(candidates, m5ai.JobClassCandidateInput{
				Name: candidate.Name, Definition: candidate.Definition,
			})
		}
		if len(candidates) == 0 {
			row.Problem = "平台没有给出任何职位类别候选，请人工到发布页确认"
			byJobID[jobID] = len(view.Jobs)
			view.Jobs = append(view.Jobs, row)
			continue
		}
		byJobID[jobID] = len(view.Jobs)
		view.Jobs = append(view.Jobs, row)
		inputs = append(inputs, m5ai.JobClassJobInput{
			JobID: jobID, JobName: target.JobName,
			Description: spec.Description, Candidates: candidates,
		})
	}

	if len(inputs) == 0 {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "没有任何职位取到了类别候选，无法分配",
			"view":  view,
		})
		return
	}

	accepted, problems, attempts, err := a.assignJobClasses(ctx, inputs, req.Occupied)
	view.Attempts = attempts
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "职位类别未能分配：" + err.Error(),
			"view":  view,
		})
		return
	}
	assigned := make(map[string]string, len(accepted))
	for jobID, assignment := range accepted {
		index, known := byJobID[jobID]
		if !known {
			continue
		}
		confidence := assignment.Confidence
		view.Jobs[index].JobClass = assignment.Class
		view.Jobs[index].Source = jobClassSourceModel
		view.Jobs[index].Confidence = &confidence
		view.Jobs[index].Reason = assignment.Reason
		assigned[jobID] = assignment.Class
	}
	for jobID, problem := range problems {
		index, known := byJobID[jobID]
		if !known {
			continue
		}
		view.Jobs[index].Problem = problem
	}
	view.Collisions = m5ai.JobClassCollisions(assigned)
	writeJSON(w, http.StatusOK, view)
}

// readJobClassCandidates 为一个职位派一条读取原语并复核证词。
//
// keepForm 恒为 false:全批分配时 A1 跑完这个职位就去跑下一个,留下的表单会被
// 下一个覆盖,等 A2 回来找它时什么都没了(规格 §2.8,2026-08-01 收窄)。
func (a *API) readJobClassCandidates(
	ctx context.Context,
	key store.AccountKey,
	account *store.Account,
	sessionID string,
	bootID string,
	jobName string,
	spec jobconfig.PublishSpec,
) (protocol.JobReadClassCandidatesData, error) {
	var zero protocol.JobReadClassCandidatesData
	args, err := protocol.Encode(protocol.JobReadClassCandidatesArgs{
		JobName:        jobName,
		EmploymentType: spec.EmploymentType,
		Description:    spec.Description,
	})
	if err != nil {
		return zero, errors.New("类别候选读取命令构造失败")
	}
	if err := protocol.ValidatePrimitiveArgs(protocol.PrimJobReadClassCandidates, 1, args); err != nil {
		return zero, errors.New("类别候选读取参数不符合当前契约")
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
		return zero, errors.New("类别候选读取未能派发")
	}
	logical, err := a.disp.WaitLogical(ctx, logicalRef)
	if err != nil {
		return zero, errors.New("类别候选读取未完成")
	}
	data, err := parseClassCandidatesProof(logicalRef, logical)
	if err != nil {
		return zero, errors.New("类别候选读取未成功：" + err.Error())
	}
	return data, nil
}

// assignJobClasses 为全部职位分配类别,必要时分块调用。
//
// 块内是一次通盘决策;跨块靠把前面已占用的类别传给后面的块来延续差异化。一块
// 整体失败不牵连别的块——那一块的职位标记跳过,继续下一块。
func (a *API) assignJobClasses(
	ctx context.Context,
	jobs []m5ai.JobClassJobInput,
	occupied []string,
) (map[string]m5ai.JobClassAssignment, map[string]string, []string, error) {
	chunk := jobClassChunkSize
	if len(jobs) <= chunk {
		return a.assignJobClassesByModel(ctx, jobs, occupied)
	}

	accepted := make(map[string]m5ai.JobClassAssignment, len(jobs))
	problems := make(map[string]string, len(jobs))
	var attempts []string
	taken := trimmedUnique(occupied)
	for start := 0; start < len(jobs); start += chunk {
		end := start + chunk
		if end > len(jobs) {
			end = len(jobs)
		}
		label := "chunk" + strconv.Itoa(start/chunk+1) + ":"
		got, bad, tries, err := a.assignJobClassesByModel(ctx, jobs[start:end], taken)
		for _, try := range tries {
			attempts = append(attempts, label+try)
		}
		if err != nil {
			for _, job := range jobs[start:end] {
				problems[job.JobID] = "assignFailed"
			}
			continue
		}
		for jobID, assignment := range got {
			accepted[jobID] = assignment
			taken = append(taken, assignment.Class)
		}
		for jobID, problem := range bad {
			problems[jobID] = problem
		}
		taken = trimmedUnique(taken)
	}
	if len(accepted) == 0 {
		return nil, nil, attempts,
			fmt.Errorf("分成 %d 块后仍没有任何职位分到类别", (len(jobs)+chunk-1)/chunk)
	}
	return accepted, problems, attempts, nil
}

// assignJobClassesByModel 让大模型一次性为一块职位分配类别。
//
// 整批重试:分配相互耦合,只重一个会破坏差异化。3 次之后取**成功最多的那一次**
// 的合法部分——不跨轮次拼接,拼出来的组合谁也没通盘看过,差异化无从谈起。
func (a *API) assignJobClassesByModel(
	ctx context.Context,
	jobs []m5ai.JobClassJobInput,
	occupied []string,
) (map[string]m5ai.JobClassAssignment, map[string]string, []string, error) {
	content, err := m5ai.RenderJobClassPrompt(jobs, trimmedUnique(occupied))
	if err != nil {
		return nil, nil, nil, err
	}
	// 同一批职位同一批候选下 invocationId 稳定可复算,便于把 ai-traces 里的
	// 记录与本次决定对上。
	base := hashJobClassInput(jobs, content)
	var attempts []string
	var bestAccepted map[string]m5ai.JobClassAssignment
	var bestProblems map[string]string
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
		record := func(outcome string) {
			attempts = append(attempts, "attempt"+strconv.Itoa(attempt)+":"+outcome)
			a.auditJobClassCall(len(jobs), attempt, outcome, response, latency)
		}
		if callErr != nil {
			record(providerOutcome(callErr))
			continue
		}
		assignments, parseErr := m5ai.ParseJobClassAssignments(response.JSONText)
		if parseErr != nil {
			record(parseErr.Error())
			continue
		}
		accepted, problems := m5ai.ClassifyJobClassAssignments(assignments, jobs)
		if len(problems) == 0 {
			record("ok")
			return accepted, problems, attempts, nil
		}
		record("partial:" + strconv.Itoa(len(accepted)) + "/" + strconv.Itoa(len(jobs)))
		if len(accepted) > len(bestAccepted) {
			bestAccepted, bestProblems = accepted, problems
		}
	}
	if len(bestAccepted) > 0 {
		// 按 2026-08-01 裁决:重试用尽后保留合法的、跳过不合法的,不让一个
		// 坏职位废掉整批的类别决定。
		return bestAccepted, bestProblems, attempts, nil
	}
	return nil, nil, attempts,
		fmt.Errorf("大模型 %d 次尝试都没给出可用的分配", jobClassAttempts)
}

// auditJobClassCall 留一条无正文的诊断痕迹。按 AI provider 数据边界,这里只允许
// 用途、批次规模、尝试、结果分类、延迟、输入输出规模与追踪状态;**不写**提示词、
// 模型原文、职位名、选定的类别与理由(那些只经 HTTP 响应交给运营看)。
func (a *API) auditJobClassCall(
	jobCount int,
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
		"purpose=jobClass jobs=%d attempt=%d/%d outcome=%s latencyMs=%d "+
			"inTokens=%d outTokens=%d reqBytes=%d resBytes=%d traceStatus=%s%s",
		jobCount, attempt, jobClassAttempts, outcome, latency.Milliseconds(),
		response.Usage.InputTokens, response.Usage.OutputTokens,
		response.Diagnostics.RequestBytes, response.Diagnostics.ResponseBytes,
		response.Diagnostics.TraceStatus, status,
	)
	a.st.Audit("job_class_ai", "", "", detail)
	if response.Diagnostics.TraceStatus != "" &&
		response.Diagnostics.TraceStatus != m5ai.TraceStatusComplete {
		// 追踪写入失败不改变业务裁决,但必须响亮报告。
		slog.Error("职位类别 AI 调用未能完整落追踪库",
			"jobs", jobCount, "attempt", attempt,
			"traceStatus", response.Diagnostics.TraceStatus,
			"traceErrorCode", response.Diagnostics.TraceErrorCode)
	}
}

// providerOutcome 把 provider 的失败分类带进审计。
//
// 原来一律记成 "providerError",于是 2026-08-02 那次 10 个职位全失败时,审计上
// 只看得到"调用失败了",看不出是超时、认证不过、还是模型被 max_tokens 切断
// (finish_reason=length)——最后靠翻源码加对延迟才定的案。分类是 provider 早就
// 分好的,不带模型原文,进审计不越数据边界,没有理由丢掉。
func providerOutcome(err error) string {
	var providerErr *m5ai.ProviderError
	if errors.As(err, &providerErr) && providerErr.DetailCode != "" {
		return "providerError:" + providerErr.DetailCode
	}
	return "providerError"
}

func hashJobClassInput(jobs []m5ai.JobClassJobInput, content string) string {
	hasher := sha256.New()
	hasher.Write([]byte("jobClass\x00"))
	for _, job := range jobs {
		hasher.Write([]byte(job.JobID))
		hasher.Write([]byte{0})
	}
	hasher.Write([]byte(content))
	return hex.EncodeToString(hasher.Sum(nil)[:12])
}

// trimmedUnique 去空白、去空串、去重并保持原顺序。
func trimmedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, duplicated := seen[trimmed]; duplicated {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
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
