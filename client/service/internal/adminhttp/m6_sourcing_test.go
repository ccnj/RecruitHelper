package adminhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/appbridge"
	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/patrol"
	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

type sourcingAdminClock struct{ now time.Time }

func (c sourcingAdminClock) Now() time.Time { return c.now }

type sourcingAdminRunner struct{}

func (sourcingAdminRunner) Start(context.Context, patrol.RunRequest) (patrol.RunHandle, error) {
	return nil, nil
}

func TestSourcingSelectionViewContainsOnlySafeAggregate(t *testing.T) {
	view := sourcingSelectionView(store.SourcingBatchSelection{
		BatchID: "batch-selection-view", ContextRevisionHash: "revision-selection-view",
		AlgorithmVersion: "selection-target-v1", MinScore: 5,
		TargetMin: 80, TargetMax: 90, TargetCount: 84,
		MaleRatioLimit: 50, MaleLimit: 42, PoolCount: 3, EligibleCount: 2,
		SelectedCount: 2, MaleSelectedCount: 2, UnknownGenderCount: 0,
		CompletedAt: time.Date(2026, 7, 22, 21, 0, 0, 0, time.UTC),
	})
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{
		"platformUserRef", "displayName", "profileId", "runId", "invocationId", "resume", "prompt",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("配置化筛选管理投影泄漏 %q: %s", forbidden, text)
		}
	}
	for _, required := range []string{`"batchId"`, `"targetCount"`, `"selectedCount"`, `"unknownGenderCount"`} {
		if !strings.Contains(text, required) {
			t.Fatalf("配置化筛选管理投影缺少 %s: %s", required, text)
		}
	}
}

func TestSourcingGreetingGenerationViewContainsOnlySafeAggregate(t *testing.T) {
	view := sourcingGreetingGenerationView(store.SourcingBatchGreetingProgress{
		BatchID: "batch-greeting-view", ContextRevisionHash: "revision-greeting-view",
		SelectedCount: 2, OKCount: 1, FailedCount: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro",
		InputTokens: 100, CachedInputTokens: 20, OutputTokens: 30,
		EstimatedCostMicros: 40, Completed: true,
	})
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{
		"platformUserRef", "displayName", "profileId", "runId", "invocationId",
		"resume", "prompt", "greetingText", "contentHash",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("招呼语生成管理投影泄漏 %q: %s", forbidden, text)
		}
	}
	for _, required := range []string{
		`"batchId"`, `"selectedCount"`, `"okCount"`, `"failedCount"`, `"estimatedCostMicros"`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("招呼语生成管理投影缺少 %s: %s", required, text)
		}
	}
}

func TestSourcingGreetingSendViewContainsOnlySafeAggregate(t *testing.T) {
	view := sourcingGreetingSendView(store.SourcingBatchGreetingSendProgress{
		BatchID: "batch-send-view", ContextRevisionHash: "revision-send-view",
		SelectedCount: 4, ReadyCount: 3, PendingCount: 1, InFlightCount: 0,
		SentCount: 1, FailedCount: 1, SuspectCount: 1, Completed: true,
	})
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]struct{}{
		"batchId": {}, "contextRevisionHash": {}, "selectedCount": {}, "readyCount": {},
		"pendingCount": {}, "inFlightCount": {}, "sentCount": {}, "failedCount": {},
		"suspectCount": {}, "completed": {},
	}
	if len(fields) != len(allowed) {
		t.Fatalf("列表发送投影字段数越界: %s", raw)
	}
	for key := range fields {
		if _, ok := allowed[key]; !ok {
			t.Fatalf("列表发送投影泄漏字段 %q: %s", key, raw)
		}
	}
	for _, forbidden := range []string{
		"profileId", "runId", "invocationId", "intentId", "platformUserRef",
		"greetingText", "text", "accountRef",
	} {
		if _, exists := fields[forbidden]; exists {
			t.Fatalf("列表发送投影泄漏 %q: %s", forbidden, raw)
		}
	}
}

type sourcingAdminHands struct{}

func (sourcingAdminHands) State(context.Context, string) (patrol.HandState, error) {
	return patrol.HandState{Online: true}, nil
}

type sourcingGreetingSendAdminHands struct{}

func (sourcingGreetingSendAdminHands) State(context.Context, string) (patrol.HandState, error) {
	return patrol.HandState{
		Online: true, Session: "session-admin-send", BootID: "boot-admin-send",
	}, nil
}

type sourcingGreetingSendAdminSender struct {
	dispatcher      *dispatch.Dispatcher
	positionRef     string
	platformUserRef string
}

func (s *sourcingGreetingSendAdminSender) SendEnvelope(handID string, env protocol.Envelope) error {
	if env.Kind != protocol.KindCmd {
		return nil
	}
	var body protocol.CmdBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		return err
	}
	var data any
	switch body.Name {
	case protocol.PrimProbePlatform:
		fingerprint := "principal-admin-send"
		data = protocol.ProbePlatformData{
			ContentScriptOk: true, LoginState: protocol.LoginStateIn,
			PageKind: protocol.PageKindRecommend, PrincipalFingerprint: &fingerprint,
		}
	case protocol.PrimCandidateReadSourcingWindow:
		var args protocol.CandidateReadSourcingWindowArgs
		if err := json.Unmarshal(body.Args, &args); err != nil {
			return err
		}
		if args.Move != protocol.SourcingWindowMoveCurrent {
			return fmt.Errorf("管理状态 fixture 不支持窗口动作 %s", args.Move)
		}
		data = protocol.CandidateReadSourcingWindowData{
			PositionRef: s.positionRef, PlatformUserRefs: []string{s.platformUserRef},
			Moved: false, ObservedAt: time.Now().UnixMilli(),
		}
	case protocol.PrimCandidateReadSourcingTargetResume:
		var args protocol.CandidateReadSourcingTargetResumeArgs
		if err := json.Unmarshal(body.Args, &args); err != nil {
			return err
		}
		if args.PlatformUserRef != s.platformUserRef || args.PositionRef != s.positionRef {
			return fmt.Errorf("管理状态 fixture 收到错误定点材料")
		}
		data = protocol.CandidateReadSourcingResumeData{
			PlatformUserRef: s.platformUserRef, PositionRef: s.positionRef,
			ContactState:   protocol.CandidateContactStateUnestablished,
			ObservedAt:     time.Now().UnixMilli(),
			Basic:          []protocol.CandidateResumeLabelValue{},
			Expectations:   []protocol.CandidateResumeLabelValue{},
			SelfEvaluation: "", Education: "", WorkExperiences: "",
		}
	default:
		return fmt.Errorf("管理状态 fixture 收到越界原语 %s", body.Name)
	}
	raw, err := protocol.Encode(data)
	if err != nil {
		return err
	}
	s.dispatcher.OnAck(handID, protocol.AckBody{Ref: env.MsgID, Status: protocol.AckStatusAccepted})
	s.dispatcher.OnResult(handID, "result-"+env.MsgID, protocol.ResultBody{
		Ref: env.MsgID, Status: protocol.ResultStatusOk, Data: raw, ExecMs: 1,
	})
	return nil
}

func (*sourcingGreetingSendAdminSender) HandSession(string) (string, string, bool) {
	return "session-admin-send", "boot-admin-send", true
}

func (*sourcingGreetingSendAdminSender) HandContractMatch(string) (bool, bool) {
	return true, true
}

func (*sourcingGreetingSendAdminSender) HandNegotiation(string) ([]string, []string, bool) {
	return []string{
			protocol.PrimProbePlatform + "@1",
			protocol.PrimCandidateReadSourcingWindow + "@1",
			protocol.PrimCandidateReadSourcingTargetResume + "@1",
		}, []string{
			string(protocol.FeatureLease1), string(protocol.FeatureProgress1), string(protocol.FeatureCancel1),
		}, true
}

func (*sourcingGreetingSendAdminSender) CloseHand(string, string, string) bool { return true }
func (*sourcingGreetingSendAdminSender) HandOfflineMs(string) int64            { return 0 }

type sourcingGreetingSendAdminAdvice struct{}

func (sourcingGreetingSendAdminAdvice) ProviderName() string { return "provider-admin-send" }
func (sourcingGreetingSendAdminAdvice) ModelName() string    { return "model-admin-send" }

func (sourcingGreetingSendAdminAdvice) CompleteJSON(
	_ context.Context,
	request m5ai.CompletionRequest,
) (m5ai.CompletionResponse, error) {
	zero := 0
	response := m5ai.CompletionResponse{
		ReasoningContentEmpty: true,
		Usage: m5ai.CompletionUsage{
			InputTokens: 10, CachedInputTokens: 0, OutputTokens: 4, ReasoningTokens: &zero,
		},
	}
	switch request.Purpose {
	case m5ai.PurposeScoring:
		response.JSONText = `{"score":8}`
	case m5ai.PurposeGreeting:
		response.JSONText = `{"招呼语":"admin-send-greeting-secret"}`
	default:
		return m5ai.CompletionResponse{}, fmt.Errorf("管理状态 fixture 收到越界 AI 用途 %s", request.Purpose)
	}
	return response, nil
}

func sourcingAdminRevision(at time.Time) m5ai.ContextRevision {
	replyPrompt, intentPrompt, facts := "admin-reply-secret", "admin-intent-secret", "admin-facts-secret"
	documents := []m5ai.JobConfigDocument{
		{DocType: "候选人筛选", Content: `{"minScore":5}`},
		{DocType: "多轮沟通", Content: replyPrompt},
		{DocType: "客户事实库", Content: facts},
		{DocType: "意向判断", Content: intentPrompt},
		{DocType: "打分", Content: "admin-score-secret {resume_json}"},
		{DocType: "招呼语", Content: `{"prompt":"admin-greeting-secret {career_state} {resume_summary_json}"}`},
		{DocType: "职位筛选", Content: `[]`},
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].DocType < documents[j].DocType })
	return m5ai.ContextRevision{
		ContextID: "context-sourcing-admin", RevisionHash: "revision-sourcing-admin",
		SourceKind: "localImport", SourceJobRef: "71", DisplayName: "admin-position-secret",
		SourcePackage: m5ai.JobConfigDocumentPackage{Documents: documents},
		Communication: m5ai.CommunicationView{
			ReplyPrompt: replyPrompt, IntentPrompt: intentPrompt,
			CustomerFacts: facts, MappingVersion: m5ai.MappingVersion,
		},
		CreatedAt: at,
	}
}

func TestSourcingGreetingSendStatusReturnsNormalSafeAggregate(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	revision := sourcingAdminRevision(now.Add(-time.Hour))
	if _, _, err := st.SaveJobAIContextRevision(revision); err != nil {
		t.Fatal(err)
	}
	key := store.AccountKey{Platform: "zhilian", AccountRef: "account-admin-send-secret"}
	if err := st.CreateAccount(&store.Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccountPrincipal(
		key, "hand-admin-send", "principal-admin-send",
		"session-admin-send", "boot-admin-send", now,
	); err != nil {
		t.Fatal(err)
	}
	sender := &sourcingGreetingSendAdminSender{
		positionRef: "position-admin-send-secret", platformUserRef: "person-admin-send-secret",
	}
	dispatcher := dispatch.New(st, sender)
	sender.dispatcher = dispatcher
	manager, err := patrol.NewManager(
		st, appbridge.PatrolRunner{Dispatcher: dispatcher}, sourcingGreetingSendAdminHands{},
		patrol.Config{Clock: sourcingAdminClock{now: now}, Location: time.UTC, IdentityFreshFor: time.Hour},
		sourcingGreetingSendAdminAdvice{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.StartSourcing(key, revision.RevisionHash, 1); err != nil {
		t.Fatal(err)
	}
	batch, err := st.ActiveSourcingBatch(key)
	if err != nil || batch == nil {
		t.Fatalf("缺少正式采集批次: batch=%+v err=%v", batch, err)
	}
	if result, err := manager.Tick(context.Background()); err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("单人采集失败: result=%+v err=%v", result, err)
	}
	if progress, err := manager.ScoreCompletedSourcingBatch(context.Background(), batch.BatchID); err != nil || !progress.Completed {
		t.Fatalf("单人评分失败: progress=%+v err=%v", progress, err)
	}
	selection, err := st.SelectCompletedSourcingBatch(batch.BatchID, now)
	if err != nil || selection == nil || selection.SelectedCount != 1 {
		t.Fatalf("单人筛选失败: selection=%+v err=%v", selection, err)
	}
	if progress, err := manager.GenerateSelectedSourcingGreetings(context.Background(), batch.BatchID); err != nil ||
		!progress.Completed || progress.OKCount != 1 {
		t.Fatalf("单人招呼语生成失败: progress=%+v err=%v", progress, err)
	}

	api := New(st, newFakeAdminHub(), dispatcher, manager, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)
	request := httptest.NewRequest(http.MethodGet,
		"/admin/sourcing/greeting-send/status?batchId="+batch.BatchID, nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("列表发送正常状态读取失败: code=%d body=%s", response.Code, response.Body.String())
	}
	var decoded struct {
		Status map[string]any `json:"sourcingGreetingSend"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Status) != 10 || decoded.Status["batchId"] != batch.BatchID ||
		decoded.Status["contextRevisionHash"] != revision.RevisionHash ||
		decoded.Status["selectedCount"] != float64(1) || decoded.Status["readyCount"] != float64(1) ||
		decoded.Status["pendingCount"] != float64(1) || decoded.Status["completed"] != false {
		t.Fatalf("列表发送正常聚合错误: %s", response.Body.String())
	}
	for _, forbiddenKey := range []string{
		"profileId", "runId", "invocationId", "intentId", "platformUserRef",
		"greetingText", "text", "accountRef",
	} {
		if _, exists := decoded.Status[forbiddenKey]; exists {
			t.Fatalf("列表发送状态泄漏字段 %q: %s", forbiddenKey, response.Body.String())
		}
	}
	for _, forbiddenValue := range []string{
		"account-admin-send-secret", "position-admin-send-secret", "person-admin-send-secret",
		"admin-send-greeting-secret", "principal-admin-send",
	} {
		if strings.Contains(response.Body.String(), forbiddenValue) {
			t.Fatalf("列表发送状态泄漏业务值 %q: %s", forbiddenValue, response.Body.String())
		}
	}
}

func TestSourcingStartStatusAndStopExposeOnlyBatchMetadata(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	revision := sourcingAdminRevision(now.Add(-time.Hour))
	if _, _, err := st.SaveJobAIContextRevision(revision); err != nil {
		t.Fatal(err)
	}
	key := store.AccountKey{Platform: "zhilian", AccountRef: "account-sourcing-admin"}
	if err := st.CreateAccount(&store.Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccountPrincipal(
		key, "hand-sourcing-admin", "principal-sourcing-admin",
		"session-sourcing-admin", "boot-sourcing-admin", now,
	); err != nil {
		t.Fatal(err)
	}
	manager, err := patrol.NewManager(st, sourcingAdminRunner{}, sourcingAdminHands{}, patrol.Config{
		Clock: sourcingAdminClock{now: now}, Location: time.UTC,
	})
	if err != nil {
		t.Fatal(err)
	}
	api := New(st, newFakeAdminHub(), nil, manager, nil, "")
	mux := http.NewServeMux()
	api.Routes(mux)

	missingTarget := httptest.NewRequest(http.MethodPost, "/admin/sourcing/start", strings.NewReader(
		`{"platform":"zhilian","accountRef":"account-sourcing-admin","contextRevisionHash":"revision-sourcing-admin"}`,
	))
	missingTarget.Header.Set("Content-Type", "application/json")
	missingTargetResponse := httptest.NewRecorder()
	mux.ServeHTTP(missingTargetResponse, missingTarget)
	if missingTargetResponse.Code != http.StatusBadRequest {
		t.Fatalf("缺少显式 targetCount 未拒绝: code=%d body=%s", missingTargetResponse.Code, missingTargetResponse.Body.String())
	}

	start := httptest.NewRequest(http.MethodPost, "/admin/sourcing/start", strings.NewReader(
		`{"platform":"zhilian","accountRef":"account-sourcing-admin","contextRevisionHash":"revision-sourcing-admin","targetCount":150}`,
	))
	start.Header.Set("Content-Type", "application/json")
	startResponse := httptest.NewRecorder()
	mux.ServeHTTP(startResponse, start)
	startBody := startResponse.Body.String()
	if startResponse.Code != http.StatusOK || !strings.Contains(startBody, `"status":"preparing"`) ||
		!strings.Contains(startBody, `"targetCount":150`) || !strings.Contains(startBody, `"capturedCount":0`) ||
		!strings.Contains(startBody, `"remainingCount":150`) || !strings.Contains(startBody, `"batchId":"sb-`) {
		t.Fatalf("启动正式采集失败: code=%d body=%s", startResponse.Code, startBody)
	}

	status := httptest.NewRequest(http.MethodGet,
		"/admin/sourcing/status?platform=zhilian&accountRef=account-sourcing-admin", nil)
	statusResponse := httptest.NewRecorder()
	mux.ServeHTTP(statusResponse, status)
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), revision.RevisionHash) {
		t.Fatalf("读取正式采集状态失败: code=%d body=%s", statusResponse.Code, statusResponse.Body.String())
	}
	for _, forbidden := range []string{
		"account-sourcing-admin", "principal-sourcing-admin", "admin-score-secret", "admin-greeting-secret",
		"admin-reply-secret", "admin-intent-secret", "admin-facts-secret", "admin-position-secret",
		`"enabled"`, `"latest"`, `"sourceLogicalDispatchId"`,
	} {
		if strings.Contains(startBody, forbidden) || strings.Contains(statusResponse.Body.String(), forbidden) {
			t.Fatalf("采集管理响应泄漏旧状态、配置正文或身份 %q", forbidden)
		}
	}
	account, err := st.AccountByKey(key)
	if err != nil || account == nil || account.EnabledAt == nil || account.SourcingEnabled ||
		account.SourcingContextRevisionHash != "" || account.SourcingStartedAt != nil {
		t.Fatalf("start 未开启 actor 或写入 legacy sourcing 双真相: account=%+v err=%v", account, err)
	}

	stop := httptest.NewRequest(http.MethodPost, "/admin/sourcing/stop", strings.NewReader(
		`{"platform":"zhilian","accountRef":"account-sourcing-admin"}`,
	))
	stop.Header.Set("Content-Type", "application/json")
	stopResponse := httptest.NewRecorder()
	mux.ServeHTTP(stopResponse, stop)
	stopBody := stopResponse.Body.String()
	if stopResponse.Code != http.StatusOK || !strings.Contains(stopBody, `"status":"stopped"`) ||
		!strings.Contains(stopBody, `"reason":"userStopped"`) || !strings.Contains(stopBody, `"endedAt"`) {
		t.Fatalf("停止正式采集失败: code=%d body=%s", stopResponse.Code, stopBody)
	}
	account, err = st.AccountByKey(key)
	if err != nil || account == nil || account.StoppedAt == nil || account.PausedReason != patrol.PauseUserStopped {
		t.Fatalf("stop 未暂停账号 actor: account=%+v err=%v", account, err)
	}

	missingScoringBatch := httptest.NewRequest(http.MethodPost, "/admin/sourcing/scoring/run",
		strings.NewReader(`{"batchId":"missing-scoring-batch"}`))
	missingScoringBatch.Header.Set("Content-Type", "application/json")
	missingScoringBatchResponse := httptest.NewRecorder()
	mux.ServeHTTP(missingScoringBatchResponse, missingScoringBatch)
	if missingScoringBatchResponse.Code != http.StatusNotFound {
		t.Fatalf("统一评分未知批次未返回 404: code=%d body=%s",
			missingScoringBatchResponse.Code, missingScoringBatchResponse.Body.String())
	}

	missingScoringStatus := httptest.NewRequest(http.MethodGet,
		"/admin/sourcing/scoring/status?batchId=missing-scoring-batch", nil)
	missingScoringStatusResponse := httptest.NewRecorder()
	mux.ServeHTTP(missingScoringStatusResponse, missingScoringStatus)
	if missingScoringStatusResponse.Code != http.StatusNotFound {
		t.Fatalf("统一评分状态未知批次未返回 404: code=%d body=%s",
			missingScoringStatusResponse.Code, missingScoringStatusResponse.Body.String())
	}

	invalidScoringRun := httptest.NewRequest(http.MethodPost, "/admin/sourcing/scoring/run",
		strings.NewReader(`{"batchId":""}`))
	invalidScoringRun.Header.Set("Content-Type", "application/json")
	invalidScoringRunResponse := httptest.NewRecorder()
	mux.ServeHTTP(invalidScoringRunResponse, invalidScoringRun)
	if invalidScoringRunResponse.Code != http.StatusBadRequest {
		t.Fatalf("统一评分缺少 batchId 未拒绝: code=%d body=%s",
			invalidScoringRunResponse.Code, invalidScoringRunResponse.Body.String())
	}

	missingSelectionBatch := httptest.NewRequest(http.MethodPost, "/admin/sourcing/selection/run",
		strings.NewReader(`{"batchId":"missing-selection-batch"}`))
	missingSelectionBatch.Header.Set("Content-Type", "application/json")
	missingSelectionBatchResponse := httptest.NewRecorder()
	mux.ServeHTTP(missingSelectionBatchResponse, missingSelectionBatch)
	if missingSelectionBatchResponse.Code != http.StatusNotFound {
		t.Fatalf("配置化筛选未知批次未返回 404: code=%d body=%s",
			missingSelectionBatchResponse.Code, missingSelectionBatchResponse.Body.String())
	}

	missingSelectionStatus := httptest.NewRequest(http.MethodGet,
		"/admin/sourcing/selection/status?batchId=missing-selection-batch", nil)
	missingSelectionStatusResponse := httptest.NewRecorder()
	mux.ServeHTTP(missingSelectionStatusResponse, missingSelectionStatus)
	if missingSelectionStatusResponse.Code != http.StatusNotFound {
		t.Fatalf("配置化筛选状态未知批次未返回 404: code=%d body=%s",
			missingSelectionStatusResponse.Code, missingSelectionStatusResponse.Body.String())
	}

	invalidSelectionRun := httptest.NewRequest(http.MethodPost, "/admin/sourcing/selection/run",
		strings.NewReader(`{"batchId":""}`))
	invalidSelectionRun.Header.Set("Content-Type", "application/json")
	invalidSelectionRunResponse := httptest.NewRecorder()
	mux.ServeHTTP(invalidSelectionRunResponse, invalidSelectionRun)
	if invalidSelectionRunResponse.Code != http.StatusBadRequest {
		t.Fatalf("配置化筛选缺少 batchId 未拒绝: code=%d body=%s",
			invalidSelectionRunResponse.Code, invalidSelectionRunResponse.Body.String())
	}

	missingGreetingBatch := httptest.NewRequest(http.MethodPost,
		"/admin/sourcing/greeting-generation/run",
		strings.NewReader(`{"batchId":"missing-greeting-batch"}`))
	missingGreetingBatch.Header.Set("Content-Type", "application/json")
	missingGreetingBatchResponse := httptest.NewRecorder()
	mux.ServeHTTP(missingGreetingBatchResponse, missingGreetingBatch)
	if missingGreetingBatchResponse.Code != http.StatusNotFound {
		t.Fatalf("招呼语生成未知批次未返回 404: code=%d body=%s",
			missingGreetingBatchResponse.Code, missingGreetingBatchResponse.Body.String())
	}

	missingGreetingStatus := httptest.NewRequest(http.MethodGet,
		"/admin/sourcing/greeting-generation/status?batchId=missing-greeting-batch", nil)
	missingGreetingStatusResponse := httptest.NewRecorder()
	mux.ServeHTTP(missingGreetingStatusResponse, missingGreetingStatus)
	if missingGreetingStatusResponse.Code != http.StatusNotFound {
		t.Fatalf("招呼语生成状态未知批次未返回 404: code=%d body=%s",
			missingGreetingStatusResponse.Code, missingGreetingStatusResponse.Body.String())
	}

	invalidGreetingRun := httptest.NewRequest(http.MethodPost,
		"/admin/sourcing/greeting-generation/run", strings.NewReader(`{"batchId":""}`))
	invalidGreetingRun.Header.Set("Content-Type", "application/json")
	invalidGreetingRunResponse := httptest.NewRecorder()
	mux.ServeHTTP(invalidGreetingRunResponse, invalidGreetingRun)
	if invalidGreetingRunResponse.Code != http.StatusBadRequest {
		t.Fatalf("招呼语生成缺少 batchId 未拒绝: code=%d body=%s",
			invalidGreetingRunResponse.Code, invalidGreetingRunResponse.Body.String())
	}

	missingSendStatus := httptest.NewRequest(http.MethodGet,
		"/admin/sourcing/greeting-send/status?batchId=missing-send-batch", nil)
	missingSendStatusResponse := httptest.NewRecorder()
	mux.ServeHTTP(missingSendStatusResponse, missingSendStatus)
	if missingSendStatusResponse.Code != http.StatusNotFound ||
		missingSendStatusResponse.Body.String() != "{\"error\":\"正式采集批次不存在\"}\n" {
		t.Fatalf("列表发送状态未知批次未固定返回 404: code=%d body=%s",
			missingSendStatusResponse.Code, missingSendStatusResponse.Body.String())
	}

	invalidSendStatus := httptest.NewRequest(http.MethodGet,
		"/admin/sourcing/greeting-send/status", nil)
	invalidSendStatusResponse := httptest.NewRecorder()
	mux.ServeHTTP(invalidSendStatusResponse, invalidSendStatus)
	if invalidSendStatusResponse.Code != http.StatusBadRequest {
		t.Fatalf("列表发送状态缺少 batchId 未拒绝: code=%d body=%s",
			invalidSendStatusResponse.Code, invalidSendStatusResponse.Body.String())
	}

	invalidSendRun := httptest.NewRequest(http.MethodPost,
		"/admin/sourcing/greeting-send/run", strings.NewReader(`{"batchId":""}`))
	invalidSendRun.Header.Set("Content-Type", "application/json")
	invalidSendRunResponse := httptest.NewRecorder()
	mux.ServeHTTP(invalidSendRunResponse, invalidSendRun)
	if invalidSendRunResponse.Code != http.StatusBadRequest {
		t.Fatalf("列表发送缺少 batchId 未拒绝: code=%d body=%s",
			invalidSendRunResponse.Code, invalidSendRunResponse.Body.String())
	}

	missingSendRun := httptest.NewRequest(http.MethodPost,
		"/admin/sourcing/greeting-send/run", strings.NewReader(`{"batchId":"missing-send-batch"}`))
	missingSendRun.Header.Set("Content-Type", "application/json")
	missingSendRunResponse := httptest.NewRecorder()
	mux.ServeHTTP(missingSendRunResponse, missingSendRun)
	if missingSendRunResponse.Code != http.StatusNotFound ||
		missingSendRunResponse.Body.String() != "{\"error\":\"正式采集批次不存在\"}\n" {
		t.Fatalf("列表发送未知批次未固定返回 404: code=%d body=%s",
			missingSendRunResponse.Code, missingSendRunResponse.Body.String())
	}
}
