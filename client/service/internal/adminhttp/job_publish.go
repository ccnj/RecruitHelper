package adminhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/jobconfig"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// 发布前预检的三类结论。ready 是唯一可以进入发布的分类。
const (
	publishVerdictReady    = "ready"
	publishVerdictExisting = "existing"
	publishVerdictBlocked  = "blocked"
)

// jobPublishPrecheckRow 只携带结论与提示，**不含发布参数正文**。
type jobPublishPrecheckRow struct {
	JobID       string                   `json:"jobId"`
	JobName     string                   `json:"jobName"`
	Environment string                   `json:"environment,omitempty"`
	IsCurrent   bool                     `json:"isCurrent"`
	Verdict     string                   `json:"verdict"`
	Issues      []jobconfig.PublishIssue `json:"issues,omitempty"`
	Notices     []jobconfig.PublishIssue `json:"notices,omitempty"`
}

type jobPublishPrecheckView struct {
	Rows            []jobPublishPrecheckRow `json:"rows"`
	PlatformPosting int                     `json:"platformPostingCount"`
	ObservedAt      int64                   `json:"observedAt"`
}

// jobPublishPrecheck 读平台现存职位名，与后台职位逐个比对并做确定性参数校验。
//
// 全程零对外副作用：只派发一条 intrusive/platformSideEffect=none 的读取原语，
// 不触碰发布页表单，不点击任何提交类控件。
func (a *API) jobPublishPrecheck(w http.ResponseWriter, r *http.Request) {
	var req accountKeyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法请求体"})
		return
	}
	key, err := validateAccountKey(req.Platform, req.AccountRef)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的平台或账号标识"})
		return
	}
	if a.st == nil || a.hub == nil || a.disp == nil || a.jobConfigSource == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "发布预检服务尚未就绪"})
		return
	}

	// 读职位列表要把页面导航到职位管理页，会打断推荐流。按「推荐页运行连续性」，
	// 批次运行期间宁可拒绝预检，也不新增标签页管理能力去绕开这条约束。
	batch, err := a.st.ActiveSourcingBatch(key)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "采集批次状态不可读"})
		return
	}
	if batch != nil {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "当前有采集批次在运行，发布预检会打断推荐流；请先结束批次再预检",
		})
		return
	}

	account, sessionID, bootID, err := a.currentCandidateAccount(key)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "账号身份或手会话当前不可用"})
		return
	}
	args, err := protocol.Encode(protocol.JobReadPublishedListArgs{})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "职位清单读取命令构造失败"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 160*time.Second)
	defer cancel()
	logicalRef, err := a.disp.DispatchStructured(dispatch.DispatchRequest{
		HandID: account.BoundHandID, ExpectedSession: sessionID, ExpectedBootID: bootID,
		Name: protocol.PrimJobReadPublishedList, Args: args,
		Context: &protocol.CmdContext{
			Platform: key.Platform, AccountRef: key.AccountRef,
			ExpectedPrincipalFingerprint: *account.PrincipalFingerprint,
		},
	})
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "平台职位清单读取未能派发"})
		return
	}
	logical, err := a.disp.WaitLogical(ctx, logicalRef)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "平台职位清单读取未完成"})
		return
	}
	postings, observedAt, err := parsePublishedListProof(logicalRef, logical, account, key)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "平台职位清单证词无效"})
		return
	}

	raw, err := a.jobConfigSource.FetchAll(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "旧后台职位列表读取失败"})
		return
	}
	sources, err := jobconfig.ParseBackendJobPublishSources(raw)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "旧后台职位列表格式不可识别"})
		return
	}

	rows := make([]jobPublishPrecheckRow, 0, len(sources))
	for _, source := range sources {
		row := jobPublishPrecheckRow{
			JobID: source.JobID, JobName: source.JobName,
			Environment: source.Environment, IsCurrent: source.IsCurrent,
		}
		// 平台已存在优先于参数问题：既然不会发，就没必要再让运营去修参数。
		if jobconfig.MatchesExistingPosting(source.JobName, postings) {
			row.Verdict = publishVerdictExisting
			rows = append(rows, row)
			continue
		}
		spec, issues := jobconfig.ParsePublishSpec(source.PublishParams)
		if len(issues) > 0 {
			row.Verdict = publishVerdictBlocked
			row.Issues = jobconfig.SortIssues(issues)
			rows = append(rows, row)
			continue
		}
		row.Verdict = publishVerdictReady
		row.Notices = spec.DeadFieldNotices(source.JobName)
		rows = append(rows, row)
	}

	writeJSON(w, http.StatusOK, jobPublishPrecheckView{
		Rows: rows, PlatformPosting: len(postings), ObservedAt: observedAt,
	})
}

type jobPublishDraftView struct {
	JobID  string                       `json:"jobId"`
	Report protocol.JobPrepareDraftData `json:"report"`
}

// jobPublishPrepareDraft 对单个职位在平台发布表单上试填一次并回读。
//
// 它**不发布**：手侧原语契约上就不允许点击提交控件，且回读完成后必须主动
// 离开表单。这一步存在的意义是在承担发布风险之前，先把"填不进去"和平台的
// 自动行为暴露出来。
func (a *API) jobPublishPrepareDraft(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Platform   string `json:"platform"`
		AccountRef string `json:"accountRef"`
		JobID      string `json:"jobId"`
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
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "发布试填服务尚未就绪"})
		return
	}
	batch, err := a.st.ActiveSourcingBatch(key)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "采集批次状态不可读"})
		return
	}
	if batch != nil {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "当前有采集批次在运行，发布试填会打断推荐流；请先结束批次",
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
	raw, err := a.jobConfigSource.FetchAll(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "旧后台职位列表读取失败"})
		return
	}
	sources, err := jobconfig.ParseBackendJobPublishSources(raw)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "旧后台职位列表格式不可识别"})
		return
	}
	var target *jobconfig.BackendJobPublishSource
	for i := range sources {
		if sources[i].JobID == req.JobID {
			target = &sources[i]
			break
		}
	}
	if target == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "该职位当前不在后台启用职位中"})
		return
	}
	spec, issues := jobconfig.ParsePublishSpec(target.PublishParams)
	if len(issues) > 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "该职位发布参数未通过预检，先修参数再试填"})
		return
	}
	args, err := json.Marshal(spec.DraftArgs(target.JobName))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "试填命令构造失败"})
		return
	}
	if err := protocol.ValidatePrimitiveArgs(protocol.PrimJobPrepareDraft, 1, args); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "试填参数不符合当前契约"})
		return
	}

	logicalRef, err := a.disp.DispatchStructured(dispatch.DispatchRequest{
		HandID: account.BoundHandID, ExpectedSession: sessionID, ExpectedBootID: bootID,
		Name: protocol.PrimJobPrepareDraft, Args: args,
		Context: &protocol.CmdContext{
			Platform: key.Platform, AccountRef: key.AccountRef,
			ExpectedPrincipalFingerprint: *account.PrincipalFingerprint,
		},
	})
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "发布试填未能派发"})
		return
	}
	logical, err := a.disp.WaitLogical(ctx, logicalRef)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "发布试填未完成"})
		return
	}
	report, err := parsePrepareDraftProof(logicalRef, logical, account, key)
	if err != nil {
		// 试填失败时把手侧的失败现场快照一并透出：链路长、每步都依赖平台异步
		// 行为，只报一句"叶子不符合要求"等于让人靠反复重跑去猜卡在哪。
		response := map[string]any{"error": "发布试填未成功：" + err.Error()}
		if diagnostics := prepareDraftFailureDiagnostics(logical); diagnostics != nil {
			response["diagnostics"] = diagnostics
		}
		writeJSON(w, http.StatusConflict, response)
		return
	}
	// 手侧契约要求试填后必须离开表单。没有这个确认就等于把一个填满的发布表单
	// 留在了页面上，此时宁可报错让人去看一眼，也不回一个"成功"。
	if !report.Discarded {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "试填后未确认离开发布表单，请人工检查页面"})
		return
	}
	writeJSON(w, http.StatusOK, jobPublishDraftView{JobID: target.JobID, Report: report})
}

// prepareDraftFailureDiagnostics 取手侧 failed 结果里的失败现场快照。它是
// 手写进 error.data 的自由结构，只透给人看，不参与任何判定；读不出来就当没有。
func prepareDraftFailureDiagnostics(logical *store.LogicalDispatchState) any {
	if logical == nil || logical.Leaf.ResultBody == "" {
		return nil
	}
	var result struct {
		Status string `json:"status"`
		Error  struct {
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(logical.Leaf.ResultBody), &result); err != nil {
		return nil
	}
	if result.Status != "failed" || len(result.Error.Data) == 0 {
		return nil
	}
	var data map[string]any
	if err := json.Unmarshal(result.Error.Data, &data); err != nil {
		return nil
	}
	if result.Error.Message != "" {
		data["handMessage"] = result.Error.Message
	}
	return data
}

func parsePrepareDraftProof(
	logicalRef string,
	logical *store.LogicalDispatchState,
	account *store.Account,
	key store.AccountKey,
) (protocol.JobPrepareDraftData, error) {
	var zero protocol.JobPrepareDraftData
	if logical == nil || !logical.Settled || logical.LogicalDispatchID != logicalRef {
		return zero, errors.New("逻辑派发未终局")
	}
	leaf := logical.Leaf
	if leaf.LogicalDispatchID != logicalRef || leaf.Name != protocol.PrimJobPrepareDraft ||
		leaf.Status != store.CmdOk || leaf.ResultBody == "" {
		return zero, errors.New("叶子不符合要求")
	}
	resultRaw := json.RawMessage(leaf.ResultBody)
	if err := protocol.ValidatePrimitiveResult(protocol.PrimJobPrepareDraft, 1, resultRaw); err != nil {
		return zero, errors.New("结果不符合契约")
	}
	var result protocol.ResultBody
	if err := json.Unmarshal(resultRaw, &result); err != nil ||
		result.Ref != leaf.MsgID || result.Status != protocol.ResultStatusOk {
		return zero, errors.New("结果关联无效")
	}
	var data protocol.JobPrepareDraftData
	if err := json.Unmarshal(result.Data, &data); err != nil {
		return zero, errors.New("数据无法解析")
	}
	var cmdContext protocol.CmdContext
	if err := json.Unmarshal([]byte(leaf.ContextJSON), &cmdContext); err != nil {
		return zero, errors.New("上下文无法解析")
	}
	if account == nil || account.PrincipalFingerprint == nil ||
		cmdContext.Platform != key.Platform || cmdContext.AccountRef != key.AccountRef ||
		cmdContext.ExpectedPrincipalFingerprint != *account.PrincipalFingerprint {
		return zero, errors.New("上下文与当前账号不符")
	}
	return data, nil
}

// parsePublishedListProof 复核证词链：逻辑派发终局、叶子就是本次读取原语、
// 结果符合契约、上下文绑定的账号与身份指纹与本次请求一致。
func parsePublishedListProof(
	logicalRef string,
	logical *store.LogicalDispatchState,
	account *store.Account,
	key store.AccountKey,
) ([]string, int64, error) {
	if logical == nil || !logical.Settled || logical.LogicalDispatchID != logicalRef {
		return nil, 0, errors.New("职位清单逻辑派发未终局")
	}
	leaf := logical.Leaf
	if leaf.LogicalDispatchID != logicalRef || leaf.Name != protocol.PrimJobReadPublishedList ||
		leaf.Status != store.CmdOk || leaf.ResultBody == "" {
		return nil, 0, errors.New("职位清单叶子不符合要求")
	}
	meta, ok := protocol.Primitives[protocol.PrimJobReadPublishedList]
	if !ok || meta.Ver != 1 {
		return nil, 0, errors.New("职位清单原语版本不可用")
	}
	resultRaw := json.RawMessage(leaf.ResultBody)
	if err := protocol.ValidatePrimitiveResult(protocol.PrimJobReadPublishedList, meta.Ver, resultRaw); err != nil {
		return nil, 0, errors.New("职位清单结果不符合契约")
	}
	var result protocol.ResultBody
	if err := json.Unmarshal(resultRaw, &result); err != nil ||
		result.Ref != leaf.MsgID || result.Status != protocol.ResultStatusOk {
		return nil, 0, errors.New("职位清单结果关联无效")
	}
	var data protocol.JobReadPublishedListData
	if err := json.Unmarshal(result.Data, &data); err != nil {
		return nil, 0, errors.New("职位清单数据无法解析")
	}
	var cmdContext protocol.CmdContext
	if err := json.Unmarshal([]byte(leaf.ContextJSON), &cmdContext); err != nil {
		return nil, 0, errors.New("职位清单上下文无法解析")
	}
	if account == nil || account.PrincipalFingerprint == nil ||
		cmdContext.Platform != key.Platform || cmdContext.AccountRef != key.AccountRef ||
		cmdContext.ExpectedPrincipalFingerprint != *account.PrincipalFingerprint {
		return nil, 0, errors.New("职位清单上下文与当前账号不符")
	}
	return data.PostingNames, data.ObservedAt, nil
}
