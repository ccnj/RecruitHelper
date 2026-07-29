package adminhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
