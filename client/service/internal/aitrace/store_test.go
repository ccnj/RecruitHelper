package aitrace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSilenceFollowupIsAnAcceptedTracePurpose(t *testing.T) {
	if !validPurpose(PurposeSilenceFollowup) {
		t.Fatal("沉默追问 trace purpose 未登记")
	}
}

func TestStorePersistsSuccessfulTraceAndExactRetriesAreIdempotent(t *testing.T) {
	dir := t.TempDir()
	traceStore := openTestStore(t, dir)
	defer traceStore.Close()

	startedAt := time.Date(2026, 7, 23, 1, 2, 3, 4, time.UTC)
	request := []byte(`{"model":"fixture","messages":[{"role":"user","content":"完整输入"}]}`)
	begin := BeginRecord{
		InvocationID: "inv-success", Purpose: "reply", Provider: "fixture",
		Model: "fixture-v1", ConfigHash: "config-hash", ContextRevisionHash: "context-hash",
		PromptRevision: "prompt-v2", RequestJSON: request, StartedAt: startedAt,
	}
	if err := traceStore.Begin(context.Background(), begin); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := traceStore.Begin(context.Background(), begin); err != nil {
		t.Fatalf("重复 Begin 应幂等: %v", err)
	}

	status := 200
	finishedAt := startedAt.Add(1500 * time.Millisecond)
	response := []byte(`{"choices":[{"message":{"content":"原始回复"}}]}`)
	finish := FinishRecord{
		InvocationID: "inv-success", HTTPStatus: &status,
		RawResponse: response, FinishedAt: finishedAt,
	}
	if err := traceStore.Finish(context.Background(), finish); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if err := traceStore.Finish(context.Background(), finish); err != nil {
		t.Fatalf("重复 Finish 应幂等: %v", err)
	}

	got, err := traceStore.Get(context.Background(), "inv-success")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SchemaVersion != SchemaVersion || got.TraceState != TraceStateCompleted ||
		got.InvocationID != begin.InvocationID || got.Purpose != begin.Purpose ||
		got.Provider != begin.Provider || got.Model != begin.Model ||
		got.ConfigHash != begin.ConfigHash ||
		got.ContextRevisionHash != begin.ContextRevisionHash ||
		got.PromptRevision != begin.PromptRevision ||
		!bytes.Equal(got.RequestJSON, request) || got.RequestBytes != int64(len(request)) ||
		got.RequestHash != testSHA256(request) || !got.StartedAt.Equal(startedAt) {
		t.Fatalf("请求 trace 不符: %#v", got)
	}
	if got.HTTPStatus == nil || *got.HTTPStatus != status || !got.ResponsePresent ||
		!bytes.Equal(got.RawResponse, response) ||
		got.ResponseBytes != int64(len(response)) || got.ResponseHash != testSHA256(response) ||
		got.TransportCode != TransportNone || got.FinishedAt == nil ||
		!got.FinishedAt.Equal(finishedAt) {
		t.Fatalf("响应 trace 不符: %#v", got)
	}

	// Get 必须返回副本，调用方改切片不能污染后续读取。
	got.RequestJSON[0] = 'x'
	got.RawResponse[0] = 'x'
	again, err := traceStore.Get(context.Background(), "inv-success")
	if err != nil || !bytes.Equal(again.RequestJSON, request) || !bytes.Equal(again.RawResponse, response) {
		t.Fatalf("Get 泄露内部切片: got=%#v err=%v", again, err)
	}
}

func TestStorePersistsHTTPFailureAndTransportFailure(t *testing.T) {
	traceStore := openTestStore(t, t.TempDir())
	defer traceStore.Close()
	ctx := context.Background()
	startedAt := time.Date(2026, 7, 23, 2, 0, 0, 0, time.UTC)

	for _, begin := range []BeginRecord{
		{InvocationID: "inv-http", Purpose: "intent", Provider: "fixture", Model: "m",
			ConfigHash: "config", ContextRevisionHash: "context",
			RequestJSON: []byte(`{"request":"http"}`), StartedAt: startedAt},
		{InvocationID: "inv-transport", Purpose: "greeting", Provider: "fixture", Model: "m",
			ConfigHash: "config", ContextRevisionHash: "context",
			RequestJSON: []byte(`{"request":"transport"}`), StartedAt: startedAt},
	} {
		if err := traceStore.Begin(ctx, begin); err != nil {
			t.Fatalf("Begin(%s): %v", begin.InvocationID, err)
		}
	}

	status := 429
	httpRaw := []byte(`{"error":{"message":"provider 原始错误"}}`)
	if err := traceStore.Finish(ctx, FinishRecord{
		InvocationID: "inv-http", HTTPStatus: &status, RawResponse: httpRaw,
		FinishedAt: startedAt.Add(time.Second),
	}); err != nil {
		t.Fatalf("HTTP failure Finish: %v", err)
	}
	if err := traceStore.Finish(ctx, FinishRecord{
		InvocationID: "inv-transport", TransportCode: TransportTimeout,
		FinishedAt: startedAt.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("transport failure Finish: %v", err)
	}

	httpTrace, err := traceStore.Get(ctx, "inv-http")
	if err != nil || httpTrace.HTTPStatus == nil || *httpTrace.HTTPStatus != 429 ||
		httpTrace.TransportCode != TransportNone || !httpTrace.ResponsePresent ||
		!bytes.Equal(httpTrace.RawResponse, httpRaw) {
		t.Fatalf("HTTP failure trace 不符: got=%#v err=%v", httpTrace, err)
	}
	transportTrace, err := traceStore.Get(ctx, "inv-transport")
	if err != nil || transportTrace.HTTPStatus != nil ||
		transportTrace.TransportCode != TransportTimeout ||
		transportTrace.ResponsePresent || transportTrace.RawResponse != nil ||
		transportTrace.ResponseBytes != 0 || transportTrace.ResponseHash != "" {
		t.Fatalf("transport failure trace 不符: got=%#v err=%v", transportTrace, err)
	}
}

func TestStoreDistinguishesObservedEmptyHTTPBodyFromNoResponse(t *testing.T) {
	traceStore := openTestStore(t, t.TempDir())
	defer traceStore.Close()
	ctx := context.Background()
	startedAt := time.Date(2026, 7, 23, 2, 30, 0, 0, time.UTC)
	if err := traceStore.Begin(ctx, BeginRecord{
		InvocationID: "inv-empty-body", Purpose: PurposeIntent, Provider: "fixture", Model: "m",
		ConfigHash: "config", ContextRevisionHash: "context",
		RequestJSON: []byte(`{}`), StartedAt: startedAt,
	}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	status := 204
	if err := traceStore.Finish(ctx, FinishRecord{
		InvocationID: "inv-empty-body", HTTPStatus: &status,
		RawResponse: []byte{}, FinishedAt: startedAt.Add(time.Second),
	}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	got, err := traceStore.Get(ctx, "inv-empty-body")
	if err != nil || !got.ResponsePresent || got.RawResponse == nil ||
		got.ResponseBytes != 0 || got.ResponseHash != testSHA256(nil) {
		t.Fatalf("空 HTTP body 的存在性丢失: got=%#v err=%v", got, err)
	}
}

func TestStoreSurvivesRestartAndDoesNotCreateBrainDatabase(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	startedAt := time.Date(2026, 7, 23, 3, 0, 0, 0, time.UTC)

	first := openTestStore(t, dir)
	if err := first.Begin(ctx, BeginRecord{
		InvocationID: "inv-restart", Purpose: "scoring", Provider: "fixture", Model: "m",
		ConfigHash: "config", ContextRevisionHash: "context",
		RequestJSON: []byte(`{"persistent":true}`), StartedAt: startedAt,
	}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, databaseFilename)); err != nil {
		t.Fatalf("独立 ai-traces.db 不存在: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "brain.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("aitrace 不得创建/复用 brain.db: err=%v", err)
	}

	second := openTestStore(t, dir)
	defer second.Close()
	if err := second.Begin(ctx, BeginRecord{
		InvocationID: "inv-restart", Purpose: PurposeScoring, Provider: "fixture", Model: "m",
		ConfigHash: "config", ContextRevisionHash: "context",
		RequestJSON: []byte(`{"persistent":true}`), StartedAt: startedAt,
	}); err != nil {
		t.Fatalf("重启后重复 Begin 应幂等: %v", err)
	}
	got, err := second.Get(ctx, "inv-restart")
	if err != nil || got.InvocationID != "inv-restart" ||
		!bytes.Equal(got.RequestJSON, []byte(`{"persistent":true}`)) ||
		got.TraceState != TraceStateRequestCaptured {
		t.Fatalf("重启后 trace 不符: got=%#v err=%v", got, err)
	}
}

func TestStoreRestrictsDatabaseAndAuxiliaryFilesToCurrentUser(t *testing.T) {
	dir := t.TempDir()
	traceStore := openTestStore(t, dir)
	defer traceStore.Close()
	if err := traceStore.Begin(context.Background(), BeginRecord{
		InvocationID: "inv-private-mode", Purpose: PurposeReply,
		Provider: "fixture", Model: "m", ConfigHash: "config",
		ContextRevisionHash: "context", RequestJSON: []byte(`{"private":true}`),
		StartedAt: time.Date(2026, 7, 23, 3, 30, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		path := filepath.Join(dir, databaseFilename) + suffix
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) && suffix != "" {
			continue
		}
		if err != nil {
			t.Fatalf("读取 AI trace 文件权限(%s): %v", suffix, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("AI trace 文件%s权限=%#o，期望 0600", suffix, got)
		}
	}
}

func TestStoreRejectsInvocationContentConflicts(t *testing.T) {
	traceStore := openTestStore(t, t.TempDir())
	defer traceStore.Close()
	ctx := context.Background()
	startedAt := time.Date(2026, 7, 23, 4, 0, 0, 0, time.UTC)
	begin := BeginRecord{
		InvocationID: "inv-conflict", Purpose: "reply", Provider: "fixture", Model: "m",
		ConfigHash: "config", ContextRevisionHash: "context",
		RequestJSON: []byte(`{"value":1}`), StartedAt: startedAt,
	}
	if err := traceStore.Begin(ctx, begin); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	changed := begin
	changed.RequestJSON = []byte(`{"value":2}`)
	if err := traceStore.Begin(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("不同请求应 ErrConflict: %v", err)
	}
	status := 200
	finished := FinishRecord{
		InvocationID: "inv-conflict", HTTPStatus: &status,
		RawResponse: []byte(`{"ok":true}`), FinishedAt: startedAt.Add(time.Second),
	}
	if err := traceStore.Finish(ctx, finished); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	changedFinish := finished
	changedFinish.RawResponse = []byte(`{"ok":false}`)
	if err := traceStore.Finish(ctx, changedFinish); !errors.Is(err, ErrConflict) {
		t.Fatalf("不同完成内容应 ErrConflict: %v", err)
	}
	if err := traceStore.Finish(ctx, FinishRecord{
		InvocationID: "missing", TransportCode: TransportNetwork, FinishedAt: startedAt,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未 Begin 的 Finish 应 ErrNotFound: %v", err)
	}
}

func TestStoreValidatesJSONAndSafeTransportCode(t *testing.T) {
	traceStore := openTestStore(t, t.TempDir())
	defer traceStore.Close()
	ctx := context.Background()
	startedAt := time.Date(2026, 7, 23, 5, 0, 0, 0, time.UTC)

	if err := traceStore.Begin(ctx, BeginRecord{
		InvocationID: "wrong-purpose", Purpose: "score", Provider: "fixture", Model: "m",
		ConfigHash: "config", ContextRevisionHash: "context",
		RequestJSON: []byte(`{}`), StartedAt: startedAt,
	}); err == nil {
		t.Fatal("错误 purpose=score 未拒绝，应使用 scoring")
	}
	if err := traceStore.Begin(ctx, BeginRecord{
		InvocationID: "invalid-json", Purpose: "reply", Provider: "fixture", Model: "m",
		ConfigHash: "config", ContextRevisionHash: "context",
		RequestJSON: []byte(`not json`), StartedAt: startedAt,
	}); err == nil {
		t.Fatal("无效 request JSON 未拒绝")
	}
	if err := traceStore.Begin(ctx, BeginRecord{
		InvocationID: "safe-code", Purpose: "reply", Provider: "fixture", Model: "m",
		ConfigHash: "config", ContextRevisionHash: "context",
		RequestJSON: []byte(`{}`), StartedAt: startedAt,
	}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := traceStore.Finish(ctx, FinishRecord{
		InvocationID: "safe-code", TransportCode: TransportCode("dial tcp candidate.example"),
		FinishedAt: startedAt.Add(time.Second),
	}); err == nil {
		t.Fatal("任意原始 transport 错误文字未拒绝")
	}
	if err := traceStore.Finish(ctx, FinishRecord{
		InvocationID: "safe-code", FinishedAt: startedAt.Add(time.Second),
	}); err == nil {
		t.Fatal("无 HTTP 响应且无 transport code 未拒绝")
	}
}

func TestStoreSchemaMatchesStandaloneTraceDesign(t *testing.T) {
	traceStore := openTestStore(t, t.TempDir())
	defer traceStore.Close()
	var columns []struct {
		Name    string
		NotNull int `gorm:"column:notnull"`
	}
	if err := traceStore.db.Raw("PRAGMA table_info(ai_traces)").Scan(&columns).Error; err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	found := make(map[string]int, len(columns))
	for _, column := range columns {
		found[column.Name] = column.NotNull
	}
	for _, name := range []string{
		"schema_version", "invocation_id", "purpose", "provider", "model", "config_hash",
		"context_revision_hash", "prompt_revision", "request_json", "request_hash",
		"request_bytes", "http_status", "response_body", "response_hash", "response_bytes",
		"trace_state", "transport_code", "started_at", "finished_at", "created_at", "updated_at",
	} {
		if _, ok := found[name]; !ok {
			t.Fatalf("ai_traces 缺列 %s；实际=%v", name, found)
		}
	}
	for _, required := range []string{
		"schema_version", "purpose", "provider", "model", "config_hash",
		"context_revision_hash", "request_json", "request_hash", "request_bytes",
		"response_bytes", "trace_state", "started_at", "created_at", "updated_at",
	} {
		if found[required] != 1 {
			t.Fatalf("ai_traces.%s 应 NOT NULL；实际=%d", required, found[required])
		}
	}
	if found["prompt_revision"] != 0 || found["response_body"] != 0 ||
		found["response_hash"] != 0 || found["finished_at"] != 0 {
		t.Fatalf("可空列约束不符: %v", found)
	}
	if _, exists := found["finished"]; exists {
		t.Fatal("不得保留设计外 finished 化石列")
	}
	if _, exists := found["raw_response"]; exists {
		t.Fatal("响应原文列须命名为 response_body")
	}
}

func openTestStore(t *testing.T, dir string) *Store {
	t.Helper()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store
}

func testSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
