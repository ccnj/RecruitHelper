package adminhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
	"recruithelper/internal/ids"
)

type currentCandidateView struct {
	SelectionRef  string                         `json:"selectionRef"`
	DisplayName   *string                        `json:"displayName"`
	PositionTitle *string                        `json:"positionTitle"`
	ContactState  protocol.CandidateContactState `json:"contactState"`
}

type selectedCandidateView struct {
	ProfileID string `json:"profileId"`
	Status    string `json:"status"`
	Created   bool   `json:"created"`
}

type candidateReadProof struct {
	Leaf    store.CmdRecord
	Context protocol.CmdContext
	Data    protocol.CandidateReadCurrentData
}

func (a *API) readCurrentCandidate(w http.ResponseWriter, r *http.Request) {
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
	if a.st == nil || a.hub == nil || a.disp == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "候选人读取服务尚未就绪"})
		return
	}

	account, sessionID, bootID, err := a.currentCandidateAccount(key)
	if err != nil {
		writeError(w, http.StatusConflict, "账号身份或手会话当前不可用", err)
		return
	}
	args, err := protocol.Encode(protocol.CandidateReadCurrentArgs{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "候选人读取命令构造失败", err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	selectionRef, err := a.disp.DispatchStructured(dispatch.DispatchRequest{
		HandID: account.BoundHandID, ExpectedSession: sessionID, ExpectedBootID: bootID,
		Name: protocol.PrimCandidateReadCurrent, Args: args,
		Context: &protocol.CmdContext{
			Platform: key.Platform, AccountRef: key.AccountRef,
			ExpectedPrincipalFingerprint: *account.PrincipalFingerprint,
		},
	})
	if err != nil {
		writeError(w, http.StatusConflict, "当前候选人读取未能派发", err)
		return
	}
	logical, err := a.disp.WaitLogical(ctx, selectionRef)
	if err != nil {
		writeError(w, http.StatusConflict, "当前候选人读取未完成", err)
		return
	}
	proof, err := parseCandidateReadProof(selectionRef, logical)
	if err != nil || !candidateProofMatchesAccount(proof, account, sessionID, bootID) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "当前候选人读取证词无效"})
		return
	}
	// 命令运行期间若账号绑定或手的 session/boot 已变化，预览本身虽无副作用，
	// 但不把它交给真人继续确认；下一次读取会从 fresh context 重新开始。
	current, currentSession, currentBoot, err := a.currentCandidateAccount(key)
	if err != nil || current.BoundHandID != account.BoundHandID ||
		currentSession != sessionID || currentBoot != bootID ||
		current.PrincipalFingerprint == nil || *current.PrincipalFingerprint != *account.PrincipalFingerprint {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "读取期间账号或手会话已变化"})
		return
	}

	writeJSON(w, http.StatusOK, currentCandidateView{
		SelectionRef: selectionRef, DisplayName: proof.Data.DisplayName,
		PositionTitle: proof.Data.PositionTitle, ContactState: proof.Data.ContactState,
	})
}

func (a *API) selectCurrentCandidate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SelectionRef string `json:"selectionRef"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法请求体"})
		return
	}
	req.SelectionRef = strings.TrimSpace(req.SelectionRef)
	if req.SelectionRef == "" || len(req.SelectionRef) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的候选人读取凭据"})
		return
	}
	if a.st == nil || a.hub == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "候选人收编服务尚未就绪"})
		return
	}

	logical, err := a.st.LogicalDispatch(req.SelectionRef)
	if errors.Is(err, store.ErrLogicalDispatchNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "候选人读取凭据不存在"})
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, "候选人读取凭据不可用", err)
		return
	}
	proof, err := parseCandidateReadProof(req.SelectionRef, logical)
	if err != nil || proof.Data.ContactState != protocol.CandidateContactStateUnestablished {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "当前关系状态不允许收编候选人"})
		return
	}
	key := store.AccountKey{Platform: proof.Context.Platform, AccountRef: proof.Context.AccountRef}
	account, sessionID, bootID, err := a.currentCandidateAccount(key)
	if err != nil || !candidateProofMatchesAccount(proof, account, sessionID, bootID) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "候选人读取凭据与当前账号不匹配"})
		return
	}
	if proof.Leaf.TerminalAt == nil || proof.Leaf.TerminalAt.IsZero() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "候选人读取凭据缺少完成时刻"})
		return
	}

	selected, err := a.st.SelectCandidateProfile(store.SelectCandidateProfileRequest{
		ProfileID: ids.NewProfileID(),
		Scope: store.CandidateProfileScope{
			Platform: proof.Context.Platform, AccountRef: proof.Context.AccountRef,
			PlatformUserRef: proof.Data.PlatformUserRef, PositionRef: proof.Data.PositionRef,
		},
		DisplayName: proof.Data.DisplayName, PositionTitle: proof.Data.PositionTitle,
		ObservedAt: *proof.Leaf.TerminalAt,
	})
	if err != nil {
		writeError(w, http.StatusConflict, "当前候选人无法收编", err)
		return
	}
	writeJSON(w, http.StatusOK, selectedCandidateView{
		ProfileID: selected.Profile.ProfileID,
		Status:    string(selected.Profile.MainStatus), Created: selected.ProfileCreated,
	})
}

// parseCandidateReadProof 在每次预览和每次真人确认时都重新读取持久化结果，
// 并字面调用 generated validator；selectionRef 只寻址证词，不替代证词本身。
func parseCandidateReadProof(selectionRef string, logical *store.LogicalDispatchState) (candidateReadProof, error) {
	if logical == nil || !logical.Settled || logical.LogicalDispatchID != selectionRef {
		return candidateReadProof{}, errors.New("候选人读取逻辑派发未终局")
	}
	leaf := logical.Leaf
	if leaf.LogicalDispatchID != selectionRef || leaf.Name != protocol.PrimCandidateReadCurrent ||
		leaf.Class != string(protocol.ClassReadonly) || leaf.Status != store.CmdOk || leaf.ResultBody == "" {
		return candidateReadProof{}, errors.New("候选人读取叶子不符合要求")
	}
	meta, ok := protocol.Primitives[protocol.PrimCandidateReadCurrent]
	if !ok || meta.Ver != 1 {
		return candidateReadProof{}, errors.New("候选人读取原语版本不可用")
	}
	if err := protocol.ValidatePrimitiveArgs(protocol.PrimCandidateReadCurrent, meta.Ver, json.RawMessage(leaf.Args)); err != nil {
		return candidateReadProof{}, errors.New("候选人读取参数无效")
	}
	resultRaw := json.RawMessage(leaf.ResultBody)
	if err := protocol.ValidatePrimitiveResult(protocol.PrimCandidateReadCurrent, meta.Ver, resultRaw); err != nil {
		return candidateReadProof{}, errors.New("候选人读取结果不符合契约")
	}
	var result protocol.ResultBody
	if err := json.Unmarshal(resultRaw, &result); err != nil || result.Ref != leaf.MsgID || result.Status != protocol.ResultStatusOk {
		return candidateReadProof{}, errors.New("候选人读取结果关联无效")
	}
	var data protocol.CandidateReadCurrentData
	if err := json.Unmarshal(result.Data, &data); err != nil {
		return candidateReadProof{}, errors.New("候选人读取数据无法解析")
	}
	contextRaw := json.RawMessage(leaf.ContextJSON)
	if err := protocol.ValidateSchema("CmdContext", contextRaw); err != nil {
		return candidateReadProof{}, errors.New("候选人读取上下文无效")
	}
	var cmdContext protocol.CmdContext
	if err := json.Unmarshal(contextRaw, &cmdContext); err != nil {
		return candidateReadProof{}, errors.New("候选人读取上下文无法解析")
	}
	if cmdContext.Platform != leaf.Platform || cmdContext.AccountRef != leaf.AccountRef ||
		cmdContext.ExpectedPrincipalFingerprint == "" ||
		cmdContext.ExpectedPrincipalFingerprint != leaf.ExpectedPrincipalFingerprint {
		return candidateReadProof{}, errors.New("候选人读取上下文与账本列不一致")
	}
	return candidateReadProof{Leaf: leaf, Context: cmdContext, Data: data}, nil
}

func (a *API) currentCandidateAccount(key store.AccountKey) (*store.Account, string, string, error) {
	account, err := a.st.AccountByKey(key)
	if err != nil {
		return nil, "", "", err
	}
	if account == nil || account.IdentityState != store.IdentityVerified ||
		account.BoundHandID == "" || account.PrincipalFingerprint == nil || *account.PrincipalFingerprint == "" {
		return nil, "", "", errors.New("账号身份未验证")
	}
	sessionID, bootID, online := a.hub.HandSession(account.BoundHandID)
	if !online || sessionID == "" || bootID == "" ||
		account.IdentitySession != sessionID || account.IdentityBootID != bootID {
		return nil, "", "", errors.New("账号身份不属于当前手会话")
	}
	return account, sessionID, bootID, nil
}

func candidateProofMatchesAccount(
	proof candidateReadProof,
	account *store.Account,
	sessionID, bootID string,
) bool {
	return account != nil && account.PrincipalFingerprint != nil &&
		proof.Context.Platform == account.Platform && proof.Context.AccountRef == account.AccountRef &&
		proof.Context.ExpectedPrincipalFingerprint == *account.PrincipalFingerprint &&
		proof.Leaf.Platform == account.Platform && proof.Leaf.AccountRef == account.AccountRef &&
		proof.Leaf.ExpectedPrincipalFingerprint == *account.PrincipalFingerprint &&
		proof.Leaf.HandID == account.BoundHandID &&
		proof.Leaf.Session == sessionID && proof.Leaf.BootIDAtDispatch == bootID
}

type sendGreetingBody struct {
	IntentID         string `json:"intentId"`
	PreviousIntentID string `json:"previousIntentId"`
	ProfileID        string `json:"profileId"`
	Text             string `json:"text"`
}

func (a *API) sendGreeting(w http.ResponseWriter, r *http.Request) {
	var body sendGreetingBody
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法请求体"})
		return
	}
	receipt, err := a.disp.SendGreeting(dispatch.SendGreetingRequest{
		IntentID: body.IntentID, PreviousIntentID: body.PreviousIntentID,
		ProfileID: body.ProfileID, Text: body.Text,
	})
	if err != nil && receipt != nil && errors.Is(err, store.ErrCandidateGreetingCASConflict) {
		writeJSON(w, http.StatusConflict, sendMessageConflictView{
			Error: err.Error(), Current: sendMessageReceiptView(receipt),
		})
		return
	}
	if err != nil && receipt == nil {
		writeGreetingError(w, err)
		return
	}
	if receipt == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "招呼回执不可用"})
		return
	}
	code := http.StatusOK
	if receipt.Created {
		code = http.StatusAccepted
	}
	view := sendMessageReceiptView(receipt)
	if err != nil {
		writeJSON(w, http.StatusAccepted, view)
		return
	}
	writeJSON(w, code, view)
}

func (a *API) sendGreetingStatus(w http.ResponseWriter, r *http.Request) {
	intentID := strings.TrimSpace(r.URL.Query().Get("intentId"))
	profileID := strings.TrimSpace(r.URL.Query().Get("profileId"))
	var receipt *dispatch.SendMessageReceipt
	var err error
	switch {
	case intentID != "" && profileID != "":
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "intentId 与 profileId 只能二选一"})
		return
	case intentID != "":
		receipt, err = a.disp.SendGreetingStatus(intentID)
	case profileID != "":
		receipt, err = a.disp.LatestGreetingStatus(profileID)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请提供 intentId 或 profileId"})
		return
	}
	if err != nil {
		writeGreetingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sendMessageReceiptView(receipt))
}

func writeGreetingError(w http.ResponseWriter, err error) {
	status := http.StatusConflict
	switch {
	case errors.Is(err, store.ErrEffectIntentNotFound), errors.Is(err, store.ErrCandidateProfileNotFound):
		status = http.StatusNotFound
	case strings.Contains(err.Error(), "缺少有效的 intentId/profileId"),
		strings.Contains(err.Error(), "发送文本不能为空"),
		strings.Contains(err.Error(), "schema"), strings.Contains(err.Error(), "校验"):
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
