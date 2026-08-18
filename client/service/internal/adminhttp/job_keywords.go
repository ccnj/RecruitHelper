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

// jobKeywordAttempts 是"选不出来"之前允许的尝试次数,与职位类别同为 3。它覆盖
// provider 调用失败、返回不是合法 JSON、数量越界、词重复、自定义超 3、单组超
// 配额这几种;模型给出的合法选择一律采纳——真正的闸是发布前运营在二次确认清单
// 上看得见这几个词再确认。
const jobKeywordAttempts = 3

type jobKeywordSectionView struct {
	Title string   `json:"title"`
	Limit int      `json:"limit,omitempty"`
	Words []string `json:"words"`
}

type jobKeywordPlanView struct {
	JobID    string `json:"jobId"`
	JobName  string `json:"jobName"`
	JobClass string `json:"jobClass"`
	// 平台这一次给出的分组词库,是本次决定的封闭候选集。
	Sections   []jobKeywordSectionView `json:"sections"`
	TotalQuota int                     `json:"totalQuota,omitempty"`
	// 手是否复用了上一趟留下的表单,纯诊断。
	FormReused bool `json:"formReused"`
	// 定下来的关键词,以及它们各自的落点。发布请求要原样带回 keywords。
	Keywords []string `json:"keywords"`
	Matched  []string `json:"matched"`
	Custom   []string `json:"custom"`
	Reason   string   `json:"reason,omitempty"`
	// 后台配置的关键词原值。死字段,列出来只为让运营看见它没有参与发布。
	DeadConfiguredKeywords []string `json:"deadConfiguredKeywords,omitempty"`
	// 模型走了几次尝试、每次为什么失败,失败分类不含任何模型原文。
	Attempts []string `json:"attempts,omitempty"`
}

// jobPublishKeywordPlan 读回平台在已选定类别下的关键词词库,并让大模型选定 3-5 个。
//
// 为什么必须单独一趟:关键词弹层要先有描述、且类别已定才打得开,而分组结构与
// 组内词库随类别和描述一起变——词库只能现读。类别又得先问过模型才定得下来,
// 手不能调大模型,也没法在原语中途回头问脑。三条一撞就是三趟填页,本接口是第
// 二趟。全程零对外副作用:只派发一条 intrusive/platformSideEffect=none 的读取
// 原语,不点弹层的「确定」,不点发布。
func (a *API) jobPublishKeywordPlan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Platform   string `json:"platform"`
		AccountRef string `json:"accountRef"`
		JobID      string `json:"jobId"`
		JobClass   string `json:"jobClass"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法请求体"})
		return
	}
	key, err := validateAccountKey(req.Platform, req.AccountRef)
	req.JobID = strings.TrimSpace(req.JobID)
	req.JobClass = strings.TrimSpace(req.JobClass)
	if err != nil || req.JobID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的平台、账号或职位标识"})
		return
	}
	// 类别必须由调用方带来:关键词弹层在类别定下之前根本打不开,而脑不替它兜
	// 一个默认值——猜错类别会连带把词库换成另一套。
	if req.JobClass == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "缺少职位类别；请先调用 /admin/job-publish/class-candidates 定下类别",
		})
		return
	}
	if a.st == nil || a.hub == nil || a.disp == nil || a.jobConfigSource == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "关键词词库读取服务尚未就绪"})
		return
	}
	if a.currentAdvice() == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "LLM 通道未就绪，无法选定关键词",
		})
		return
	}
	// 与预检、类别、试填、发布同一道闸:这趟也要占用页面并导航。
	batch, err := a.st.ActiveSourcingBatch(key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "采集批次状态不可读", err)
		return
	}
	if batch != nil {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "当前有采集批次在运行，读取关键词词库会打断推荐流；请先结束批次",
		})
		return
	}
	account, sessionID, bootID, err := a.currentCandidateAccount(key)
	if err != nil {
		writeError(w, http.StatusConflict, "账号身份或手会话当前不可用", err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 250*time.Second)
	defer cancel()
	target, spec, failure := a.resolvePublishTarget(ctx, req.JobID)
	if failure != nil {
		writeJSON(w, failure.status, map[string]string{"error": failure.message})
		return
	}

	args, err := protocol.Encode(protocol.JobReadKeywordVocabularyArgs{
		JobName:        target.JobName,
		EmploymentType: spec.EmploymentType,
		Description:    spec.Description,
		JobClass:       req.JobClass,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "词库读取命令构造失败", err)
		return
	}
	if err := protocol.ValidatePrimitiveArgs(protocol.PrimJobReadKeywordVocabulary, 1, args); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "词库读取参数不符合当前契约"})
		return
	}
	logicalRef, err := a.disp.DispatchStructured(dispatch.DispatchRequest{
		HandID: account.BoundHandID, ExpectedSession: sessionID, ExpectedBootID: bootID,
		Name: protocol.PrimJobReadKeywordVocabulary, Args: args,
		Context: &protocol.CmdContext{
			Platform: key.Platform, AccountRef: key.AccountRef,
			ExpectedPrincipalFingerprint: *account.PrincipalFingerprint,
		},
	})
	if err != nil {
		writeError(w, http.StatusConflict, "词库读取未能派发", err)
		return
	}
	logical, err := a.disp.WaitLogical(ctx, logicalRef)
	if err != nil {
		writeError(w, http.StatusConflict, "词库读取未完成", err)
		return
	}
	data, err := parseKeywordVocabularyProof(logicalRef, logical)
	if err != nil {
		response := map[string]any{"error": "关键词词库读取未成功：" + err.Error()}
		if diagnostics := prepareDraftFailureDiagnostics(logical); diagnostics != nil {
			response["diagnostics"] = diagnostics
		}
		writeJSON(w, http.StatusConflict, response)
		return
	}

	// 三个非 omitempty 的数组字段先建成空切片。失败分支会带着这个 view 直接
	// 写回 409,停在 nil 就会序列化成 `null`,而诊断台按数组用它——2026-08-02
	// 白屏就是这么来的。
	view := jobKeywordPlanView{
		JobID: target.JobID, JobName: target.JobName, JobClass: data.JobClass,
		FormReused: data.FormReused, DeadConfiguredKeywords: spec.DeadKeywords,
		Sections: []jobKeywordSectionView{},
		Keywords: []string{}, Matched: []string{}, Custom: []string{},
	}
	// 契约里 totalQuota 与 limit 都是 optional：手读不到就整键省略，落到这里
	// 就是 0。0 表示"这个组件变体没给出上限"，不是"上限为 0"。
	view.TotalQuota = int(data.TotalQuota)
	inputs := make([]m5ai.JobKeywordSectionInput, 0, len(data.Sections))
	for _, section := range data.Sections {
		input := m5ai.JobKeywordSectionInput{
			Title: section.Title, Limit: int(section.Limit), Words: section.Words,
		}
		inputs = append(inputs, input)
		words := section.Words
		if words == nil {
			// 兜底组没有现成词条,契约允许 words 为空数组;真要是解出 nil,
			// 序列化成 null 又会把诊断台掀了。
			words = []string{}
		}
		view.Sections = append(view.Sections, jobKeywordSectionView{
			Title: input.Title, Limit: input.Limit, Words: words,
		})
	}
	// 手侧读到的是"这个类别下平台给了什么",一个分组都没有说明弹层没渲染出来,
	// 已经在手侧轮询失败了;这里再兜一道,免得拿空词库去问模型。
	if len(inputs) == 0 {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "平台没有给出任何关键词分组，无法选词；请人工到发布页确认",
			"view":  view,
		})
		return
	}

	plan, attempts, err := a.chooseJobKeywordsByModel(ctx, target, spec.Description, data.JobClass, inputs)
	view.Attempts = attempts
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "职位关键词未能选定：" + err.Error(),
			"view":  view,
		})
		return
	}
	view.Keywords, view.Matched, view.Custom = plan.Keywords, plan.Matched, plan.Custom
	view.Reason = plan.Reason
	writeJSON(w, http.StatusOK, view)
}

// keywordPlanResult 是选词那一步的完整结论:落点加上给人读的理由。
type keywordPlanResult struct {
	m5ai.JobKeywordsPlan
	Reason string
}

// chooseJobKeywordsByModel 让大模型从平台当次词库里选 3-5 个词。
//
// 六种失败都重试:provider 调用失败、返回不是合法 JSON、数量越界、词重复、
// 自定义超 3、单组超配额。复核一步都不放宽——关键词决定平台把职位匹配给谁,
// 选错的页面看上去一切正常。
func (a *API) chooseJobKeywordsByModel(
	ctx context.Context,
	target *jobconfig.BackendJobPublishSource,
	description string,
	jobClass string,
	sections []m5ai.JobKeywordSectionInput,
) (keywordPlanResult, []string, error) {
	content, err := m5ai.RenderJobKeywordsPrompt(target.JobName, description, jobClass, sections)
	if err != nil {
		return keywordPlanResult{}, nil, err
	}
	// 同一职位同一份词库下 invocationId 稳定可复算,便于把 ai-traces 里的记录
	// 与本次决定对上。
	base := hashJobKeywordsInput(target.JobID, content)
	var attempts []string
	for attempt := 1; attempt <= jobKeywordAttempts; attempt++ {
		started := time.Now()
		response, callErr := a.currentAdvice().CompleteJSON(ctx, m5ai.CompletionRequest{
			InvocationID:        "jk-" + base + "-" + strconv.Itoa(attempt),
			Purpose:             m5ai.PurposeJobKeywords,
			ContextRevisionHash: base,
			PromptRevision:      m5ai.JobKeywordsInputFormatVersion,
			UserContent:         content,
			MaxOutputTokens:     m5ai.JobKeywordsOutputTokenLimit,
		})
		latency := time.Since(started)
		record := func(outcome string) {
			attempts = append(attempts, "attempt"+strconv.Itoa(attempt)+":"+outcome)
			a.auditJobKeywordsCall(target, attempt, outcome, response, latency)
		}
		if callErr != nil {
			record(providerOutcome(callErr))
			continue
		}
		suggestion, parseErr := m5ai.ParseJobKeywordsSuggestion(response.JSONText)
		if parseErr != nil {
			record(parseErr.Error())
			continue
		}
		plan, planErr := m5ai.PlanJobKeywords(suggestion, sections)
		if planErr != nil {
			record(planErr.Error())
			continue
		}
		record("ok")
		return keywordPlanResult{JobKeywordsPlan: plan, Reason: suggestion.Reason}, attempts, nil
	}
	return keywordPlanResult{}, attempts,
		fmt.Errorf("大模型 %d 次尝试都没给出可用的关键词组合", jobKeywordAttempts)
}

// auditJobKeywordsCall 留一条无正文的诊断痕迹。按 AI provider 数据边界,这里只
// 允许用途、尝试、结果分类、延迟、token/字节与追踪状态;**不写**提示词、模型
// 原文、词库与选定的词(那些只经 HTTP 响应交给运营看)。
func (a *API) auditJobKeywordsCall(
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
		"purpose=jobKeywords jobId=%s attempt=%d/%d outcome=%s latencyMs=%d "+
			"inTokens=%d outTokens=%d reqBytes=%d resBytes=%d traceStatus=%s%s",
		target.JobID, attempt, jobKeywordAttempts, outcome, latency.Milliseconds(),
		response.Usage.InputTokens, response.Usage.OutputTokens,
		response.Diagnostics.RequestBytes, response.Diagnostics.ResponseBytes,
		response.Diagnostics.TraceStatus, status,
	)
	a.st.Audit("job_keywords_ai", "", "", detail)
	if response.Diagnostics.TraceStatus != "" &&
		response.Diagnostics.TraceStatus != m5ai.TraceStatusComplete {
		// 追踪写入失败不改变业务裁决,但必须响亮报告。
		slog.Error("职位关键词 AI 调用未能完整落追踪库",
			"jobId", target.JobID, "attempt", attempt,
			"traceStatus", response.Diagnostics.TraceStatus,
			"traceErrorCode", response.Diagnostics.TraceErrorCode)
	}
}

func hashJobKeywordsInput(jobID, content string) string {
	sum := sha256.Sum256([]byte("jobKeywords\x00" + jobID + "\x00" + content))
	return hex.EncodeToString(sum[:12])
}

// parseKeywordVocabularyProof 只接受叶子命令 ok 且 result 符合契约的结果。
func parseKeywordVocabularyProof(
	msgID string,
	logical *store.LogicalDispatchState,
) (protocol.JobReadKeywordVocabularyData, error) {
	var zero protocol.JobReadKeywordVocabularyData
	if logical == nil || !logical.Settled {
		return zero, errors.New("命令未终局")
	}
	leaf := logical.Leaf
	if leaf.Name != protocol.PrimJobReadKeywordVocabulary || leaf.Status != store.CmdOk ||
		leaf.ResultBody == "" {
		return zero, errors.New("未取得成功终局")
	}
	resultRaw := json.RawMessage(leaf.ResultBody)
	if err := protocol.ValidatePrimitiveResult(protocol.PrimJobReadKeywordVocabulary, 1, resultRaw); err != nil {
		return zero, errors.New("结果不符合契约")
	}
	var result protocol.ResultBody
	if err := json.Unmarshal(resultRaw, &result); err != nil ||
		result.Ref != msgID || result.Status != protocol.ResultStatusOk {
		return zero, errors.New("结果关联无效")
	}
	var data protocol.JobReadKeywordVocabularyData
	if err := json.Unmarshal(result.Data, &data); err != nil {
		return zero, errors.New("数据无法解析")
	}
	return data, nil
}
