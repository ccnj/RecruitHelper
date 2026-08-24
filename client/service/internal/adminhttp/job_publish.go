package adminhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/jobclassreport"
	"recruithelper/client/service/internal/jobconfig"
	"recruithelper/client/service/internal/m5ai"
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
		writeError(w, http.StatusBadRequest, "缺少有效的平台或账号标识", err)
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
		writeError(w, http.StatusInternalServerError, "采集批次状态不可读", err)
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
		writeError(w, http.StatusConflict, "账号身份或手会话当前不可用", err)
		return
	}
	args, err := protocol.Encode(protocol.JobReadPublishedListArgs{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "职位清单读取命令构造失败", err)
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
		writeError(w, http.StatusConflict, "平台职位清单读取未能派发", err)
		return
	}
	logical, err := a.disp.WaitLogical(ctx, logicalRef)
	if err != nil {
		writeError(w, http.StatusConflict, "平台职位清单读取未完成", err)
		return
	}
	postings, observedAt, err := parsePublishedListProof(logicalRef, logical, account, key)
	if err != nil {
		writeError(w, http.StatusConflict, "平台职位清单证词无效", err)
		return
	}

	raw, err := a.jobConfigSource.FetchAll(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "旧后台职位列表读取失败", err)
		return
	}
	sources, err := jobconfig.ParseBackendJobPublishSources(raw)
	if err != nil {
		writeError(w, http.StatusBadGateway, "旧后台职位列表格式不可识别", err)
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
		// 代招公司 2026-08-24 起来自职位级字段,不再来自发布参数 JSON。
		spec.PartnerCompany = source.PartnerCompany
		row.Verdict = publishVerdictReady
		row.Notices = spec.DeadFieldNotices(source.JobName)
		if notice := spec.PartnerCompanyNotice(); notice != nil {
			row.Notices = append(row.Notices, *notice)
		}
		rows = append(rows, row)
	}

	writeJSON(w, http.StatusOK, jobPublishPrecheckView{
		Rows: rows, PlatformPosting: len(postings), ObservedAt: observedAt,
	})
}

// checkPublishDecisions 复核前两趟定下的两项决定齐备且形状合法，返回空串表示通过。
//
// 这里只做形状检查（非空、数量、无重复）：它们是不是平台原文，已由前两趟各自的
// 逐字核对保证——脑在这一层再核一次也只是把同一份数据拿来比自己，没有新信息。
func checkPublishDecisions(jobClass string, keywords []string) string {
	if strings.TrimSpace(jobClass) == "" {
		return "缺少职位类别；请先调用 /admin/job-publish/class-candidates 定下类别"
	}
	seen := make(map[string]struct{}, len(keywords))
	count := 0
	for _, keyword := range keywords {
		word := strings.TrimSpace(keyword)
		if word == "" {
			return "职位关键词里有空词"
		}
		if _, duplicated := seen[word]; duplicated {
			return "职位关键词重复：" + word
		}
		seen[word] = struct{}{}
		count++
	}
	if count < m5ai.JobKeywordsMin || count > m5ai.JobKeywordsMax {
		return fmt.Sprintf(
			"职位关键词必须是 %d-%d 个；请先调用 /admin/job-publish/keyword-plan 选定",
			m5ai.JobKeywordsMin, m5ai.JobKeywordsMax,
		)
	}
	return ""
}

type jobPublishDraftView struct {
	JobID  string                       `json:"jobId"`
	Report protocol.JobPrepareDraftData `json:"report"`
	// 代招公司实际选中情况与后台配置的比对提示；没配置也没走到那段时为空。
	PartnerCompanyHint string `json:"partnerCompanyHint,omitempty"`
}

// jobPublishFailure 是一条可以直接写回 HTTP 的失败,供三个入口共用同一套
// "定位后台职位 + 参数预检"的前置逻辑。
type jobPublishFailure struct {
	status  int
	message string
}

// resolvePublishTarget 定位后台职位并跑确定性参数预检。三个入口(类别解析、试填、
// 发布)对这一段的要求完全相同,分别抄一遍只会让它们慢慢长歪。
func (a *API) resolvePublishTarget(
	ctx context.Context,
	jobID string,
) (*jobconfig.BackendJobPublishSource, jobconfig.PublishSpec, *jobPublishFailure) {
	raw, err := a.jobConfigSource.FetchAll(ctx)
	if err != nil {
		return nil, jobconfig.PublishSpec{},
			&jobPublishFailure{http.StatusBadGateway, "旧后台职位列表读取失败: " + err.Error()}
	}
	sources, err := jobconfig.ParseBackendJobPublishSources(raw)
	if err != nil {
		return nil, jobconfig.PublishSpec{},
			&jobPublishFailure{http.StatusBadGateway, "旧后台职位列表格式不可识别: " + err.Error()}
	}
	var target *jobconfig.BackendJobPublishSource
	for i := range sources {
		if sources[i].JobID == jobID {
			target = &sources[i]
			break
		}
	}
	if target == nil {
		return nil, jobconfig.PublishSpec{},
			&jobPublishFailure{http.StatusConflict, "该职位当前不在后台启用职位中"}
	}
	spec, issues := jobconfig.ParsePublishSpec(target.PublishParams)
	if len(issues) > 0 {
		return nil, jobconfig.PublishSpec{},
			&jobPublishFailure{http.StatusConflict, "该职位发布参数未通过预检，先修参数再操作"}
	}
	// 代招公司 2026-08-24 起来自职位级字段,不再来自发布参数 JSON。
	spec.PartnerCompany = target.PartnerCompany
	return target, spec, nil
}

// jobPublishPrepareDraft 对单个职位在平台发布表单上试填一次并回读。
//
// 它**不发布**：手侧原语契约上就不允许点击提交控件，且回读完成后必须主动
// 离开表单。这一步存在的意义是在承担发布风险之前，先把"填不进去"和平台的
// 自动行为暴露出来。jobClass 必须由调用方给出（先走 class-candidates 定下来），
// 因为关键词弹层要等类别定了才打得开。
func (a *API) jobPublishPrepareDraft(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Platform   string   `json:"platform"`
		AccountRef string   `json:"accountRef"`
		JobID      string   `json:"jobId"`
		JobClass   string   `json:"jobClass"`
		Keywords   []string `json:"keywords"`
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
	if message := checkPublishDecisions(req.JobClass, req.Keywords); message != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": message})
		return
	}
	if a.st == nil || a.hub == nil || a.disp == nil || a.jobConfigSource == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "发布试填服务尚未就绪"})
		return
	}
	batch, err := a.st.ActiveSourcingBatch(key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "采集批次状态不可读", err)
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
	args, err := json.Marshal(spec.DraftArgs(target.JobName, req.JobClass, req.Keywords))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "试填命令构造失败", err)
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
		writeError(w, http.StatusConflict, "发布试填未能派发", err)
		return
	}
	logical, err := a.disp.WaitLogical(ctx, logicalRef)
	if err != nil {
		writeError(w, http.StatusConflict, "发布试填未完成", err)
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
	writeJSON(w, http.StatusOK, jobPublishDraftView{
		JobID: target.JobID, Report: report,
		PartnerCompanyHint: spec.PartnerCompanyHint(report.PartnerCompany),
	})
}

type jobPublishResultView struct {
	JobID    string `json:"jobId"`
	IntentID string `json:"intentId"`
	Status   string `json:"status"`
	Created  bool   `json:"created"`
	// 取得平台正证时才有；未确认时为空，由 diagnostics 说明现场。
	Report      *protocol.JobPublishDraftData `json:"report,omitempty"`
	Diagnostics any                           `json:"diagnostics,omitempty"`
	// 代招公司实际选中情况与后台配置的比对提示；随 Report 一起给，没配置也没
	// 走到那段时为空。
	PartnerCompanyHint string `json:"partnerCompanyHint,omitempty"`
}

// jobPublishPublish 真正把一个职位发布到平台。
//
// 这是本能力唯一会产生对外副作用的入口：一次请求最多发一个职位，绝不循环。
// 幂等由 intentID（职位 + 发布参数 hash 派生）与 WAL 保证，HTTP 重试只会收编
// 原意图，不会再发一次。
func (a *API) jobPublishPublish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Platform   string   `json:"platform"`
		AccountRef string   `json:"accountRef"`
		JobID      string   `json:"jobId"`
		JobClass   string   `json:"jobClass"`
		Keywords   []string `json:"keywords"`
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
	// 类别与关键词都必须由调用方显式带来:它们是前两趟定下的平台原文,运营在
	// 二次确认清单上看得见、也能改。脑不替它们兜默认值——猜错会把职位推给
	// 错误的人群,而页面看上去一切正常。
	if message := checkPublishDecisions(req.JobClass, req.Keywords); message != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": message})
		return
	}
	if a.st == nil || a.hub == nil || a.disp == nil || a.jobConfigSource == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "职位发布服务尚未就绪"})
		return
	}
	// 与预检同一道闸：发布要占用页面并导航，批次在跑时一律拒绝。
	batch, err := a.st.ActiveSourcingBatch(key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "采集批次状态不可读", err)
		return
	}
	if batch != nil {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "当前有采集批次在运行，发布会打断推荐流；请先结束批次",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 280*time.Second)
	defer cancel()
	// 发布前再跑一次确定性参数预检：参数不合格就不该消耗一次不可逆动作。
	target, spec, failure := a.resolvePublishTarget(ctx, req.JobID)
	if failure != nil {
		writeJSON(w, failure.status, map[string]string{"error": failure.message})
		return
	}
	var args protocol.JobPrepareDraftArgs
	argsRaw, err := json.Marshal(spec.DraftArgs(target.JobName, req.JobClass, req.Keywords))
	if err != nil || json.Unmarshal(argsRaw, &args) != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "发布参数组装失败"})
		return
	}

	receipt, err := a.disp.PublishJob(dispatch.PublishJobRequest{
		Platform: key.Platform, AccountRef: key.AccountRef, JobID: req.JobID, Args: args,
	})
	if receipt == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "职位发布未能派发：" + errText(err)})
		return
	}
	// 已经派发出去了：此后无论如何都不能再发一次，只能读账本看它怎么收束。
	view := jobPublishResultView{
		JobID: req.JobID, IntentID: receipt.IntentID,
		Status: receipt.Status, Created: receipt.Created,
	}
	logical, waitErr := a.disp.WaitLogical(ctx, receipt.MsgID)
	if waitErr != nil {
		view.Diagnostics = map[string]any{
			"note": "发布命令已派发但未在本次请求内收束；按 intentId 查账本，不要重发",
		}
		writeJSON(w, http.StatusAccepted, view)
		return
	}
	report, proofErr := parsePublishDraftProof(receipt.MsgID, logical)
	if proofErr != nil {
		view.Diagnostics = prepareDraftFailureDiagnostics(logical)
		if view.Diagnostics == nil {
			view.Diagnostics = map[string]any{"note": proofErr.Error()}
		}
		if intent, lookupErr := a.disp.PublishJobStatus(receipt.IntentID); lookupErr == nil {
			view.Status = string(intent.Status)
		}
		writeJSON(w, http.StatusConflict, view)
		return
	}
	if intent, lookupErr := a.disp.PublishJobStatus(receipt.IntentID); lookupErr == nil {
		view.Status = string(intent.Status)
	}
	view.Report = &report
	view.PartnerCompanyHint = spec.PartnerCompanyHint(report.PartnerCompany)
	// 职位类别审计上报(AGENTS.md 第十项出站,stage=published):实际发出的类别。
	// fire-and-forget,不影响发布结果。
	if a.jobClassReporter != nil {
		prefilled := ""
		if report.PrefilledClass != nil {
			prefilled = *report.PrefilledClass
		}
		a.jobClassReporter.ReportAsync([]jobclassreport.Record{{
			JobID: req.JobID, JobName: target.JobName, Platform: key.Platform,
			Stage: jobclassreport.StagePublished, ObservedAt: report.ObservedAt,
			Candidates: []jobclassreport.Candidate{}, PrefilledClass: prefilled, ChosenClass: report.JobClass,
		}})
	}
	writeJSON(w, http.StatusOK, view)
}

type jobTakeOfflineResultView struct {
	JobID    string `json:"jobId"`
	JobName  string `json:"jobName"`
	IntentID string `json:"intentId"`
	Status   string `json:"status"`
	Created  bool   `json:"created"`
	// 取得平台正证时才有；未确认时为空，由 diagnostics 说明现场。
	Report      *protocol.JobTakeOfflineData `json:"report,omitempty"`
	Diagnostics any                          `json:"diagnostics,omitempty"`
}

// resolveJobNameOnly 只按后台职位 ID 定位职位名，不跑发布参数预检。
//
// 下线刻意不复用 resolvePublishTarget：那里的参数预检服务于"要不要冒险发布"，
// 而一个发布参数已经不合格、但人早就发上线的职位，照样应该能下线。把预检塞进
// 下线只会让最该下线的那些职位卡住。
func (a *API) resolveJobNameOnly(ctx context.Context, jobID string) (string, *jobPublishFailure) {
	raw, err := a.jobConfigSource.FetchAll(ctx)
	if err != nil {
		return "", &jobPublishFailure{http.StatusBadGateway, "旧后台职位列表读取失败: " + err.Error()}
	}
	sources, err := jobconfig.ParseBackendJobPublishSources(raw)
	if err != nil {
		return "", &jobPublishFailure{http.StatusBadGateway, "旧后台职位列表格式不可识别: " + err.Error()}
	}
	for i := range sources {
		if sources[i].JobID == jobID {
			// 职位名一律取 job.name，不取发布参数里的职位名称——后者会漂移，
			// 而这个名字是平台侧的身份键，错一个字就定位到别人头上。
			return sources[i].JobName, nil
		}
	}
	return "", &jobPublishFailure{http.StatusConflict, "该职位当前不在后台启用职位中"}
}

// jobTakeOffline 把一个已在线的职位下线。
//
// 它与 jobPublishPublish 是两条独立链路：调用方在发布拿到正证之后再调这里，
// 下线失败**不回改发布结论**（甲方 2026-08-13 裁决：下线只是锦上添花，失败
// 记一笔即可）。幂等由 intentID（职位派生）与 WAL 保证，HTTP 重试只会收编原
// 意图；真正防重复点击的是手侧 guards——已下线的行根本没有下线入口。
func (a *API) jobTakeOffline(w http.ResponseWriter, r *http.Request) {
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
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "职位下线服务尚未就绪"})
		return
	}
	// 与发布同一道闸：下线要占用页面并导航到职位管理页，批次在跑时一律拒绝。
	batch, err := a.st.ActiveSourcingBatch(key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "采集批次状态不可读", err)
		return
	}
	if batch != nil {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "当前有采集批次在运行，下线会打断推荐流；请先结束批次",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 200*time.Second)
	defer cancel()
	jobName, failure := a.resolveJobNameOnly(ctx, req.JobID)
	if failure != nil {
		writeJSON(w, failure.status, map[string]string{"error": failure.message})
		return
	}

	receipt, err := a.disp.TakeJobOffline(dispatch.TakeJobOfflineRequest{
		Platform: key.Platform, AccountRef: key.AccountRef, JobID: req.JobID, JobName: jobName,
	})
	if receipt == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "职位下线未能派发：" + errText(err)})
		return
	}
	view := jobTakeOfflineResultView{
		JobID: req.JobID, JobName: jobName, IntentID: receipt.IntentID,
		Status: receipt.Status, Created: receipt.Created,
	}
	logical, waitErr := a.disp.WaitLogical(ctx, receipt.MsgID)
	if waitErr != nil {
		view.Diagnostics = map[string]any{
			"note": "下线命令已派发但未在本次请求内收束；按 intentId 查账本，不要重发",
		}
		writeJSON(w, http.StatusAccepted, view)
		return
	}
	report, proofErr := parseTakeOfflineProof(receipt.MsgID, logical)
	if proofErr != nil {
		view.Diagnostics = prepareDraftFailureDiagnostics(logical)
		if view.Diagnostics == nil {
			view.Diagnostics = map[string]any{"note": proofErr.Error()}
		}
		if intent, lookupErr := a.disp.TakeJobOfflineStatus(receipt.IntentID); lookupErr == nil {
			view.Status = string(intent.Status)
		}
		writeJSON(w, http.StatusConflict, view)
		return
	}
	if intent, lookupErr := a.disp.TakeJobOfflineStatus(receipt.IntentID); lookupErr == nil {
		view.Status = string(intent.Status)
	}
	view.Report = &report
	writeJSON(w, http.StatusOK, view)
}

func parseTakeOfflineProof(
	msgID string,
	logical *store.LogicalDispatchState,
) (protocol.JobTakeOfflineData, error) {
	var zero protocol.JobTakeOfflineData
	if logical == nil || !logical.Settled {
		return zero, errors.New("下线命令未终局")
	}
	leaf := logical.Leaf
	if leaf.Name != protocol.PrimJobTakeOffline || leaf.Status != store.CmdOk || leaf.ResultBody == "" {
		return zero, errors.New("下线未取得成功终局")
	}
	resultRaw := json.RawMessage(leaf.ResultBody)
	if err := protocol.ValidatePrimitiveResult(protocol.PrimJobTakeOffline, 1, resultRaw); err != nil {
		return zero, errors.New("下线结果不符合契约")
	}
	var result protocol.ResultBody
	if err := json.Unmarshal(resultRaw, &result); err != nil ||
		result.Ref != msgID || result.Status != protocol.ResultStatusOk {
		return zero, errors.New("下线结果关联无效")
	}
	var data protocol.JobTakeOfflineData
	if err := json.Unmarshal(result.Data, &data); err != nil {
		return zero, errors.New("下线数据无法解析")
	}
	// 契约要求成功必须带平台正证；没有正证的成功不该存在。
	if !data.OfflineVisible {
		return zero, errors.New("下线结果缺少平台正证")
	}
	return data, nil
}

func errText(err error) string {
	if err == nil {
		return "原因未知"
	}
	return err.Error()
}

func parsePublishDraftProof(
	msgID string,
	logical *store.LogicalDispatchState,
) (protocol.JobPublishDraftData, error) {
	var zero protocol.JobPublishDraftData
	if logical == nil || !logical.Settled {
		return zero, errors.New("发布命令未终局")
	}
	leaf := logical.Leaf
	if leaf.Name != protocol.PrimJobPublishDraft || leaf.Status != store.CmdOk || leaf.ResultBody == "" {
		return zero, errors.New("发布未取得成功终局")
	}
	resultRaw := json.RawMessage(leaf.ResultBody)
	if err := protocol.ValidatePrimitiveResult(protocol.PrimJobPublishDraft, 1, resultRaw); err != nil {
		return zero, errors.New("发布结果不符合契约")
	}
	var result protocol.ResultBody
	if err := json.Unmarshal(resultRaw, &result); err != nil ||
		result.Ref != msgID || result.Status != protocol.ResultStatusOk {
		return zero, errors.New("发布结果关联无效")
	}
	var data protocol.JobPublishDraftData
	if err := json.Unmarshal(result.Data, &data); err != nil {
		return zero, errors.New("发布数据无法解析")
	}
	// 契约要求成功必须带平台正证；没有正证的成功不该存在。
	if !data.PostingVisible {
		return zero, errors.New("发布结果缺少平台正证")
	}
	return data, nil
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
	// 同名预检只关心"名字是否存在于任一分区",取并集,与 2026-08-12 之前的
	// 去重平铺语义一致;分区归属另由采集开启闸与状态上报消费。
	names := make([]string, 0, 8)
	for _, section := range data.Sections {
		names = append(names, section.Names...)
	}
	return names, data.ObservedAt, nil
}
