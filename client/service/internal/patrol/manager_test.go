package patrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Add(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}

type fakeHands struct {
	mu    sync.Mutex
	state HandState
	err   error
}

func (h *fakeHands) State(context.Context, string) (HandState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state, h.err
}

func (h *fakeHands) set(state HandState) {
	h.mu.Lock()
	h.state = state
	h.mu.Unlock()
}

type fakeRunner struct {
	mu        sync.Mutex
	calls     []RunRequest
	handler   func(RunRequest) (any, error)
	startHook func(RunRequest)
}

type fakeRunHandle struct {
	handler func(RunRequest) (any, error)
	request RunRequest
}

func (h *fakeRunHandle) LogicalDispatchID() string { return "fake-" + h.request.Name }

func (r *fakeRunner) Start(_ context.Context, request RunRequest) (RunHandle, error) {
	r.mu.Lock()
	r.calls = append(r.calls, request)
	handler := r.handler
	hook := r.startHook
	r.mu.Unlock()
	if hook != nil {
		hook(request)
	}
	return &fakeRunHandle{handler: handler, request: request}, nil
}

func (h *fakeRunHandle) Wait(_ context.Context) (json.RawMessage, error) {
	value, err := h.handler(h.request)
	if err != nil {
		return nil, err
	}
	if raw, ok := value.(json.RawMessage); ok {
		return raw, nil
	}
	return protocol.Encode(value)
}

func (r *fakeRunner) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	for i := range r.calls {
		out[i] = r.calls[i].Name
	}
	return out
}

func (r *fakeRunner) count(name string) int {
	n := 0
	for _, got := range r.names() {
		if got == name {
			n++
		}
	}
	return n
}

type harness struct {
	db      *store.Store
	dataDir string
	clock   *fakeClock
	hands   *fakeHands
	runner  *fakeRunner
	manager *Manager
	key     store.AccountKey
	config  Config
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dataDir := t.TempDir()
	db, err := store.Open(dataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := &fakeClock{now: time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)}
	hands := &fakeHands{state: HandState{Online: true, Session: "session-1", BootID: "boot-1"}}
	runner := &fakeRunner{}
	runner.handler = defaultHandler
	key := store.AccountKey{Platform: "zhilian", AccountRef: "account-1"}
	if err := db.CreateAccount(&store.Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := db.BindAccountPrincipal(key, "hand-1", "principal-1", "session-1", "boot-1", clock.Now()); err != nil {
		t.Fatalf("BindAccountPrincipal: %v", err)
	}
	sequence := 0
	config := Config{
		Clock: clock, Location: time.UTC, PatrolInterval: 5 * time.Minute,
		IdentityFreshFor: time.Hour, CoalesceWindow: 25 * time.Second,
		MinimumRoundGap: time.Minute, ManualQuiet: 45 * time.Second, MaxPages: 16,
		NewRoundID: func() string {
			sequence++
			return fmt.Sprintf("round-%03d", sequence)
		},
		InteractionPaceWait: func(ctx context.Context) error {
			return ctx.Err()
		},
	}
	manager, err := NewManager(db, runner, hands, config)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := manager.EnableToday(key); err != nil {
		t.Fatalf("EnableToday: %v", err)
	}
	return &harness{
		db: db, dataDir: dataDir, clock: clock, hands: hands, runner: runner,
		manager: manager, key: key, config: config,
	}
}

func TestSourcingUserPauseInFlightPreservesPreparingBatch(t *testing.T) {
	h := newHarness(t)
	documents := []m5ai.JobConfigDocument{
		{DocType: "多轮沟通", Content: "reply"},
		{DocType: "意向判断", Content: "intent"},
		{DocType: "客户事实库", Content: "facts"},
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].DocType < documents[j].DocType })
	revision := m5ai.ContextRevision{
		ContextID: "context-pause-sourcing", RevisionHash: "revision-pause-sourcing",
		SourceKind: "localImport", SourceJobRef: "17", DisplayName: "synthetic-position",
		SourcePackage: m5ai.JobConfigDocumentPackage{Documents: documents},
		Communication: m5ai.CommunicationView{
			ReplyPrompt: "reply", IntentPrompt: "intent", CustomerFacts: "facts", MappingVersion: m5ai.MappingVersion,
		},
		CreatedAt: h.clock.Now(),
	}
	if _, _, err := h.db.SaveJobAIContextRevision(revision); err != nil {
		t.Fatal(err)
	}
	started, err := h.db.StartSourcingBatch(store.StartSourcingBatchRequest{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ContextRevisionHash: revision.RevisionHash, TargetCount: 2, StartedAt: h.clock.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	waiting := make(chan struct{})
	release := make(chan struct{})
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name != protocol.PrimCandidateReadSourcingWindow {
			return defaultHandler(request)
		}
		close(waiting)
		<-release
		return protocol.CandidateReadSourcingWindowData{
			PositionRef: "position-pause", PlatformUserRefs: []string{"candidate-pause"},
			Moved: false, ObservedAt: h.clock.Now().UnixMilli(),
		}, nil
	}
	tickDone := make(chan TickResult, 1)
	go func() {
		result, _ := h.manager.Tick(context.Background())
		tickDone <- result
	}()
	<-waiting
	if err := h.manager.PauseNow(h.key); err != nil {
		t.Fatal(err)
	}
	close(release)
	result := <-tickDone
	if len(result.Rounds) != 1 || !errors.Is(result.Rounds[0].Err, ErrActorPaused) {
		t.Fatalf("在途暂停未以 actor paused 收束: %+v", result)
	}
	batch, err := h.db.SourcingBatchByID(started.Batch.BatchID)
	if err != nil || batch == nil || batch.Status != store.SourcingBatchPreparing || batch.Reason != "" || batch.EndedAt != nil {
		t.Fatalf("普通暂停改写了正式批次: batch=%+v err=%v", batch, err)
	}
}

func defaultHandler(request RunRequest) (any, error) {
	switch request.Name {
	case protocol.PrimProbePlatform:
		fingerprint := "principal-1"
		return protocol.ProbePlatformData{
			ContentScriptOk: true, LoginState: protocol.LoginStateIn, PageKind: protocol.PageKindIm,
			PrincipalFingerprint: &fingerprint, Surface: &protocol.PlatformSurface{ImListVisible: true},
		}, nil
	case protocol.PrimNavEnsureSurface:
		return protocol.NavEnsureSurfaceData{Ready: true, LoginState: protocol.LoginStateIn}, nil
	case protocol.PrimCandidateSelectSourcingPosition:
		return protocol.CandidateSelectSourcingPositionData{
			PositionRef: "position-fixture", PositionTitle: "synthetic-position",
			ObservedAt: time.Now().UnixMilli(),
		}, nil
	case protocol.PrimChatReadList:
		return protocol.ChatReadListData{Sessions: []protocol.ConversationSummary{}, Complete: true}, nil
	case protocol.PrimChatReadThread:
		return protocol.ChatReadThreadData{
			Messages: []protocol.ThreadMessage{}, Peer: nil, Complete: true, ReachedTop: true,
		}, nil
	default:
		return nil, fmt.Errorf("unexpected primitive %s", request.Name)
	}
}

func decodeArgs[T any](t *testing.T, request RunRequest) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(request.Args, &out); err != nil {
		t.Fatalf("decode args %s: %v", request.Name, err)
	}
	return out
}

func ptr[T any](value T) *T { return &value }

func summary(ref, peer, preview string, unread int) protocol.ConversationSummary {
	return protocol.ConversationSummary{
		ConversationRef: ref, Peer: protocol.PeerSummary{DisplayName: "候选人-" + ref, PlatformUserRef: peer},
		UnreadCount: unread,
		LastMessage: protocol.LastMessageSummary{
			Direction: protocol.MessageDirectionIn, Kind: protocol.MessageKindText, TextPreview: preview,
		},
	}
}

func threadText(index int, text string) protocol.ThreadMessage {
	return protocol.ThreadMessage{
		Idx: index, Direction: protocol.MessageDirectionIn, Kind: protocol.MessageKindText,
		Text: ptr(text), BlobRef: nil, ContentHash: syncledger.HashText(text),
	}
}

func draftText(text string) store.MessageDraft {
	return store.MessageDraft{
		Direction: "in", Kind: "text", Text: ptr(text), ContentHash: syncledger.HashText(text), Origin: "external",
	}
}

func seedTracked(t *testing.T, h *harness, conversationRef, peerRef string, adopted []store.MessageDraft) store.ConversationKey {
	t.Helper()
	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef, ConversationRef: conversationRef,
	}
	roundID := "seed-" + conversationRef
	if err := h.db.CreatePatrolRound(&store.PatrolRound{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef, RoundID: roundID,
		Trigger: "seed", Status: "running", StartedAt: h.clock.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("CreatePatrolRound seed: %v", err)
	}
	if err := h.db.SaveConversationList(store.SaveConversationListRequest{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef, RoundID: roundID,
		ObservedAt: h.clock.Now().Add(-time.Hour), Complete: true,
		Entries: []store.ListIndexEntry{{
			ConversationRef: conversationRef, PlatformUserRef: peerRef, PeerDisplayName: "候选人",
			LastMessageDirection: "in", LastMessageKind: "text", LastMessagePreview: "old",
		}},
	}); err != nil {
		t.Fatalf("SaveConversationList seed: %v", err)
	}
	if _, err := h.db.TrackConversation(key, "test", h.clock.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("TrackConversation: %v", err)
	}
	if adopted != nil {
		if _, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
			Key: key, RoundID: roundID, ExpectedTailSeq: 0, PlatformUserRef: peerRef,
			NewMessages: adopted, Adopt: true, SyncedAt: h.clock.Now(),
		}); err != nil {
			t.Fatalf("ApplyConversationChanges seed: %v", err)
		}
	}
	if err := h.db.MutatePatrolRound(h.key.Platform, h.key.AccountRef, roundID, func(round *store.PatrolRound) error {
		round.Status = "ok"
		round.Stage = "finished"
		finished := h.clock.Now().Add(-time.Hour)
		round.FinishedAt = &finished
		return nil
	}); err != nil {
		t.Fatalf("finish seed round: %v", err)
	}
	return key
}

func eventBody(t *testing.T, h *harness, name protocol.EventName, data any) protocol.EventBody {
	t.Helper()
	raw, err := protocol.Encode(data)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.EventBody{
		Context: protocol.EventContext{Platform: h.key.Platform, AccountRef: h.key.AccountRef},
		Name:    name, ObservedAt: h.clock.Now().UnixMilli(), Data: raw,
	}
}

func seedActiveSourcingBatchForFeedInvalidation(
	t *testing.T,
	h *harness,
	batchID string,
) *store.SourcingBatch {
	t.Helper()
	documents := []m5ai.JobConfigDocument{
		{DocType: "多轮沟通", Content: "reply"},
		{DocType: "客户事实库", Content: "facts"},
		{DocType: "意向判断", Content: "intent"},
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].DocType < documents[j].DocType })
	revision := m5ai.ContextRevision{
		ContextID: "context-" + batchID, RevisionHash: "revision-" + batchID,
		SourceKind: "localImport", SourceJobRef: "17", DisplayName: "synthetic-position",
		SourcePackage: m5ai.JobConfigDocumentPackage{Documents: documents},
		Communication: m5ai.CommunicationView{
			ReplyPrompt: "reply", IntentPrompt: "intent", CustomerFacts: "facts",
			MappingVersion: m5ai.MappingVersion,
		},
		CreatedAt: h.clock.Now().Add(-time.Hour),
	}
	if _, _, err := h.db.SaveJobAIContextRevision(revision); err != nil {
		t.Fatal(err)
	}
	started, err := h.db.StartSourcingBatch(store.StartSourcingBatchRequest{
		BatchID: batchID, Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ContextRevisionHash: revision.RevisionHash, TargetCount: 30,
		StartedAt: h.clock.Now().Add(-time.Minute),
	})
	if err != nil || started == nil || !started.Created {
		t.Fatalf("建立 active 采集批次失败: result=%+v err=%v", started, err)
	}
	return &started.Batch
}

func countAudit(entries []store.AuditEntry, category string) int {
	count := 0
	for _, entry := range entries {
		if entry.Category == category {
			count++
		}
	}
	return count
}

func TestFirstAdoptionPaginatesAndProjectsNoHistory(t *testing.T) {
	h := newHarness(t)
	conversationKey := seedTracked(t, h, "conversation-1", "peer-1", nil)
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Cursor == "" {
				next := "list-page-2"
				return protocol.ChatReadListData{
					Sessions: []protocol.ConversationSummary{summary("other", "peer-other", "irrelevant", 0)},
					Complete: false, NextCursor: &next,
				}, nil
			}
			if args.Cursor != "list-page-2" {
				t.Fatalf("unexpected list cursor %q", args.Cursor)
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{summary("conversation-1", "peer-1", "old-2", 1)}, Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
			if args.Window.Deep {
				t.Fatal("first adoption must not need deep")
			}
			if args.Cursor == "" {
				next := "thread-page-2"
				return protocol.ChatReadThreadData{
					Messages: []protocol.ThreadMessage{threadText(0, "old-2")}, Peer: ptr(protocol.PeerSummary{DisplayName: "候选人", PlatformUserRef: "peer-1"}),
					Complete: false, NextCursor: &next,
				}, nil
			}
			if args.Cursor != "thread-page-2" {
				t.Fatalf("unexpected thread cursor %q", args.Cursor)
			}
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{threadText(0, "old-1")}, Peer: nil,
				Complete: true, ReachedTop: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("round = %+v", result.Rounds)
	}
	if got := result.ProjectionCount(); got != 0 {
		t.Fatalf("first adoption projected %d historical events", got)
	}
	messages, err := h.db.MessagesForConversation(conversationKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Text == nil || *messages[0].Text != "old-1" || *messages[1].Text != "old-2" {
		t.Fatalf("thread pages were not prepended chronologically: %+v", messages)
	}
	conversation, _ := h.db.ConversationByKey(conversationKey)
	if conversation.TrackingState != store.TrackingAdopted || conversation.AdoptedBoundarySeq != 2 {
		t.Fatalf("adoption boundary = %+v", conversation)
	}
	wantNames := []string{protocol.PrimChatReadList, protocol.PrimChatReadList, protocol.PrimChatReadThread, protocol.PrimChatReadThread}
	if got := h.runner.names(); fmt.Sprint(got) != fmt.Sprint(wantNames) {
		t.Fatalf("command order = %v, want %v", got, wantNames)
	}
}

func TestEmptyFirstAdoptionStaysPendingAndRecoveredHistoryIsNotProjected(t *testing.T) {
	h := newHarness(t)
	conversationKey := seedTracked(t, h, "conversation-empty-adoption", "peer-empty-adoption", nil)
	threadReads := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary(conversationKey.ConversationRef, "peer-empty-adoption", "history", 1),
				},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			threadReads++
			if threadReads == 1 {
				return protocol.ChatReadThreadData{
					Messages: []protocol.ThreadMessage{}, Complete: true, ReachedTop: true,
				}, nil
			}
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{threadText(0, "history")},
				Peer:     ptr(protocol.PeerSummary{DisplayName: "候选人", PlatformUserRef: "peer-empty-adoption"}),
				Complete: true, ReachedTop: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	first, err := h.manager.Tick(context.Background())
	if err != nil || len(first.Rounds) != 1 || !errors.Is(first.Rounds[0].Err, syncledger.ErrAdoptionSnapshotEmpty) {
		t.Fatalf("空首次快照应响亮失败: result=%+v err=%v", first, err)
	}
	conversation, _ := h.db.ConversationByKey(conversationKey)
	if conversation.TrackingState != store.TrackingPending || conversation.AdoptedBoundarySeq != 0 || conversation.LastMessageSeq != 0 {
		t.Fatalf("空快照不得完成收编: %+v", conversation)
	}

	h.clock.Add(h.config.MinimumRoundGap)
	if err := h.manager.RequestImmediate(h.key); err != nil {
		t.Fatal(err)
	}
	second, err := h.manager.Tick(context.Background())
	if err != nil || len(second.Rounds) != 1 || second.Rounds[0].Err != nil {
		t.Fatalf("历史恢复后的收编失败: result=%+v err=%v", second, err)
	}
	if second.ProjectionCount() != 0 {
		t.Fatalf("恢复出的首次历史不得投影为新增: %+v", second.Rounds[0].Projections)
	}
	conversation, _ = h.db.ConversationByKey(conversationKey)
	if conversation.TrackingState != store.TrackingAdopted || conversation.AdoptedBoundarySeq != 1 || conversation.LastMessageSeq != 1 {
		t.Fatalf("恢复历史后收编边界错误: %+v", conversation)
	}
}

func TestThreadAnchorAcrossProtocolPagesStopsAndAlignsInBrain(t *testing.T) {
	h := newHarness(t)
	conversationKey := seedTracked(t, h, "conversation-cross-page-anchor", "peer-cross-page", []store.MessageDraft{
		draftText("old-1"), draftText("old-2"),
	})
	threadCalls := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{summary(conversationKey.ConversationRef, "peer-cross-page", "new", 1)},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			threadCalls++
			args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
			switch threadCalls {
			case 1:
				if args.Cursor != "" {
					t.Fatalf("首页 cursor=%q", args.Cursor)
				}
				next := "older-page"
				return protocol.ChatReadThreadData{
					Messages: []protocol.ThreadMessage{threadText(0, "old-2"), threadText(1, "new")},
					Peer:     ptr(protocol.PeerSummary{DisplayName: "候选人", PlatformUserRef: "peer-cross-page"}),
					Complete: false, NextCursor: &next,
				}, nil
			case 2:
				if args.Cursor != "older-page" {
					t.Fatalf("旧页 cursor=%q", args.Cursor)
				}
				next := "must-not-be-read"
				return protocol.ChatReadThreadData{
					Messages: []protocol.ThreadMessage{threadText(0, "older-context"), threadText(1, "old-1")},
					Complete: false, NextCursor: &next,
				}, nil
			default:
				t.Fatal("完整 anchor 已跨页聚合后不得继续读取更老页面")
			}
		default:
			return defaultHandler(request)
		}
		return nil, errors.New("unreachable")
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("Tick cross-page anchor = %+v, %v", result, err)
	}
	if threadCalls != 2 || result.ProjectionCount() != 1 {
		t.Fatalf("threadCalls=%d projection=%d", threadCalls, result.ProjectionCount())
	}
	messages, err := h.db.MessagesForConversation(conversationKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[2].Text == nil || *messages[2].Text != "new" {
		t.Fatalf("跨页 anchor 后账本错误: %+v", messages)
	}
	for _, message := range messages {
		if message.Text != nil && *message.Text == "older-context" {
			t.Fatal("锚点前上下文不得重复写入账本")
		}
	}
}

func TestLoginInitialInPreservesBindingThenProbeOnSessionChange(t *testing.T) {
	h := newHarness(t)
	if err := h.manager.HandleEvent("hand-1", eventBody(t, h, protocol.EventLoginStateChanged, protocol.LoginStateChangedEventData{
		At: h.clock.Now().UnixMilli(), Stable: true, State: protocol.LoginChangeStateIn,
	})); err != nil {
		t.Fatalf("HandleEvent in: %v", err)
	}
	account, _ := h.db.AccountByKey(h.key)
	if account.IdentityState != store.IdentityVerified || account.StoppedAt != nil || account.PausedReason != "" {
		t.Fatalf("initial in destroyed verified binding: %+v", account)
	}
	if len(h.runner.names()) != 0 {
		t.Fatal("event handler must not run commands")
	}

	h.hands.set(HandState{Online: true, Session: "session-2", BootID: "boot-2"})
	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("Tick after session change: %+v, %v", result, err)
	}
	want := []string{protocol.PrimProbePlatform, protocol.PrimChatReadList}
	if got := h.runner.names(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("probe order = %v, want %v", got, want)
	}
	account, _ = h.db.AccountByKey(h.key)
	if account.IdentitySession != "session-2" || account.IdentityBootID != "boot-2" {
		t.Fatalf("fresh identity was not persisted: %+v", account)
	}

	if err := h.manager.HandleEvent("hand-1", eventBody(t, h, protocol.EventLoginStateChanged, protocol.LoginStateChangedEventData{
		At: h.clock.Now().UnixMilli(), Stable: true, State: protocol.LoginChangeStateOut,
	})); err != nil {
		t.Fatalf("HandleEvent out: %v", err)
	}
	if err := h.manager.HandleEvent("hand-1", eventBody(t, h, protocol.EventLoginStateChanged, protocol.LoginStateChangedEventData{
		At: h.clock.Now().UnixMilli(), Stable: true, State: protocol.LoginChangeStateIn,
	})); err != nil {
		t.Fatalf("HandleEvent in after out: %v", err)
	}
	account, _ = h.db.AccountByKey(h.key)
	if account.IdentityState != store.IdentityInvalid || account.PausedReason != PauseLoginRequired {
		t.Fatalf("out -> in must remain manual-only invalid: %+v", account)
	}
}

func TestEventFromNonBoundHandCannotInvalidateAccount(t *testing.T) {
	h := newHarness(t)
	err := h.manager.HandleEvent("hand-stale", eventBody(t, h, protocol.EventLoginStateChanged, protocol.LoginStateChangedEventData{
		At: h.clock.Now().UnixMilli(), Stable: true, State: protocol.LoginChangeStateOut,
	}))
	if !errors.Is(err, ErrEventHandMismatch) {
		t.Fatalf("stale hand event should be rejected: %v", err)
	}
	account, _ := h.db.AccountByKey(h.key)
	if account.IdentityState != store.IdentityVerified || account.StoppedAt != nil || account.PausedReason != "" {
		t.Fatalf("stale hand invalidated account: %+v", account)
	}
}

func TestTrackedExpirySamePreviewAmbiguityDoesNotCreateFalseNew(t *testing.T) {
	h := newHarness(t)
	key := seedTracked(t, h, "same-preview", "peer-same", []store.MessageDraft{draftText("收到")})
	h.clock.Add(31 * time.Minute)
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{summary("same-preview", "peer-same", "收到", 0)},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{threadText(0, "收到"), threadText(1, "收到")},
				Complete: true, AnchorMatched: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("expiry reconciliation failed: %+v err=%v", result, err)
	}
	if h.runner.count(protocol.PrimChatReadThread) != 1 || result.ProjectionCount() != 0 {
		t.Fatalf("同文边界歧义必须宁可少投影: calls=%v projection=%d", h.runner.names(), result.ProjectionCount())
	}
	messages, err := h.db.MessagesForConversation(key)
	if err != nil || len(messages) != 1 {
		t.Fatalf("ledger = %+v err=%v", messages, err)
	}
	rounds, err := h.db.RecentPatrolRounds(h.key, 1)
	if err != nil || len(rounds) != 1 || rounds[0].NewMessageCount != 0 {
		t.Fatalf("同文歧义不得增加轮次消息计数: %+v err=%v", rounds, err)
	}
	audits, err := h.db.AuditEntries(20)
	if err != nil || countAudit(audits, "conversation_alignment_ambiguous") == 0 {
		t.Fatalf("同文歧义必须留审计: %+v err=%v", audits, err)
	}
}

func TestEnableTodayRequiresConfiguredStartHour(t *testing.T) {
	h := newHarness(t)
	h.clock.Add(-2 * time.Hour) // 07:00
	if err := h.manager.EnableToday(h.key); !errors.Is(err, ErrDailyWindowNotOpen) {
		t.Fatalf("08:00 前不得开启巡检: %v", err)
	}
}

func TestRoundCrossingMidnightStopsBeforeUsingStaleObservation(t *testing.T) {
	h := newHarness(t)
	h.clock.Add(14*time.Hour + 59*time.Minute) // 23:59，身份已过期，首步会 probe。
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name != protocol.PrimProbePlatform {
			t.Fatalf("跨日 probe 后不得继续执行 %s", request.Name)
		}
		h.clock.Add(2 * time.Minute) // 原语返回时已过本地 24:00。
		return defaultHandler(request)
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || !errors.Is(result.Rounds[0].Err, ErrDailyWindowExpired) {
		t.Fatalf("跨日轮次应响亮失败: result=%+v err=%v", result, err)
	}
	if got := h.runner.names(); len(got) != 1 || got[0] != protocol.PrimProbePlatform {
		t.Fatalf("跨日后仍下发了命令: %v", got)
	}
	account, _ := h.db.AccountByKey(h.key)
	if account.PausedReason != PauseDailyExpired || account.StoppedAt == nil {
		t.Fatalf("跨日后必须失效当日开启状态: %+v", account)
	}
	rounds, _ := h.db.RecentPatrolRounds(h.key, 1)
	if len(rounds) != 1 || rounds[0].Status != "failed" || rounds[0].ListComplete != nil {
		t.Fatalf("跨日观测不得冒充完整列表: %+v", rounds)
	}
}

func TestLongRoundDoesNotBlockManualInteractionEvent(t *testing.T) {
	h := newHarness(t)
	seedTracked(t, h, "quiet-during-list", "peer-quiet", []store.MessageDraft{draftText("old")})
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadList {
			once.Do(func() { close(started) })
			<-release
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{summary("quiet-during-list", "peer-quiet", "new", 1)},
				Complete: true,
			}, nil
		}
		if request.Name == protocol.PrimChatReadThread {
			t.Fatal("静默窗在 readList 途中生效后不得再派 readThread")
		}
		return defaultHandler(request)
	}
	tickDone := make(chan error, 1)
	go func() {
		_, err := h.manager.Tick(context.Background())
		tickDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("巡检未进入长命令")
	}
	eventDone := make(chan error, 1)
	go func() {
		eventDone <- h.manager.HandleEvent("hand-1", eventBody(t, h, protocol.EventManualInteraction,
			protocol.ManualInteractionEventData{
				At: h.clock.Now().UnixMilli(), Kind: protocol.ManualInteractionKindPointer, PageKind: protocol.PageKindIm,
			}))
	}()
	select {
	case err := <-eventDone:
		if err != nil {
			t.Fatalf("长轮次期间传感事件失败: %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Tick 持有全局锁跨网络命令，阻塞了用户事件")
	}
	account, _ := h.db.AccountByKey(h.key)
	if account.ManualQuietUntil == nil {
		t.Fatal("用户事件未及时打开静默窗")
	}
	close(release)
	select {
	case err := <-tickDone:
		if err != nil {
			t.Fatalf("释放长命令后 Tick 失败: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("长命令释放后 Tick 未结束")
	}
	if h.runner.count(protocol.PrimChatReadThread) != 0 {
		t.Fatal("用户静默窗未阻止后续 intrusive 命令")
	}
	account, _ = h.db.AccountByKey(h.key)
	if !account.DirtyHint {
		t.Fatal("长轮次中到达的用户事件被成功 finish 清掉")
	}
	wantPulled := h.clock.Now().Add(h.config.CoalesceWindow)
	if account.NextPatrolAt == nil || !account.NextPatrolAt.Equal(wantPulled) {
		t.Fatalf("事件拉前时刻被 finish 覆盖: got=%v want=%v", account.NextPatrolAt, wantPulled)
	}
}

func TestRecommendNavigationEventInvalidatesActiveSourcingFeed(t *testing.T) {
	h := newHarness(t)
	started := seedActiveSourcingBatchForFeedInvalidation(t, h, "batch-feed-navigation")
	now := h.clock.Now()

	err := h.manager.HandleEvent("hand-1", eventBody(t, h, protocol.EventManualInteraction,
		protocol.ManualInteractionEventData{
			At: now.UnixMilli(), Kind: protocol.ManualInteractionKindNavigation,
			PageKind: protocol.PageKindRecommend,
		}))
	if err != nil {
		t.Fatal(err)
	}
	batch, err := h.db.SourcingBatchByID(started.BatchID)
	if err != nil || batch == nil || batch.Status != store.SourcingBatchStopped ||
		batch.Reason != store.SourcingFeedChangedReason || batch.EndedAt == nil || !batch.EndedAt.Equal(now) {
		t.Fatalf("推荐页 navigation 未终止旧 active 批次: batch=%+v err=%v", batch, err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil || account.SourcingFeedInvalidatedAt == nil ||
		!account.SourcingFeedInvalidatedAt.Equal(now) || account.StoppedAt == nil ||
		account.PausedReason != store.SourcingFeedChangedReason || account.ManualQuietUntil == nil {
		t.Fatalf("推荐页 navigation 未写 marker、暂停账号或保留手动静默: account=%+v err=%v", account, err)
	}
}

func TestEventCannotSlipBetweenGenerationGateAndCommandStart(t *testing.T) {
	h := newHarness(t)
	startEntered := make(chan struct{})
	releaseStart := make(chan struct{})
	waitEntered := make(chan struct{})
	releaseWait := make(chan struct{})
	var once sync.Once
	h.runner.startHook = func(request RunRequest) {
		if request.Name != protocol.PrimChatReadList {
			return
		}
		once.Do(func() { close(startEntered) })
		<-releaseStart
	}
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadList {
			close(waitEntered)
			<-releaseWait
			return protocol.ChatReadListData{Sessions: []protocol.ConversationSummary{}, Complete: true}, nil
		}
		return defaultHandler(request)
	}

	tickDone := make(chan struct{})
	go func() {
		_, _ = h.manager.Tick(context.Background())
		close(tickDone)
	}()
	select {
	case <-startEntered:
	case <-time.After(time.Second):
		t.Fatal("巡检未进入 Start 临界区")
	}
	eventDone := make(chan error, 1)
	go func() {
		eventDone <- h.manager.HandleEvent("hand-1", eventBody(t, h, protocol.EventManualInteraction,
			protocol.ManualInteractionEventData{
				At: h.clock.Now().UnixMilli(), Kind: protocol.ManualInteractionKindPointer, PageKind: protocol.PageKindIm,
			}))
	}()
	select {
	case err := <-eventDone:
		t.Fatalf("事件钻进 generation gate 与 Start 之间: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseStart)
	select {
	case <-waitEntered:
	case <-time.After(time.Second):
		t.Fatal("Start 返回后未进入无锁 Wait")
	}
	select {
	case err := <-eventDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("网络 Wait 期间事件仍被 actor 锁阻塞")
	}
	close(releaseWait)
	select {
	case <-tickDone:
	case <-time.After(time.Second):
		t.Fatal("释放 Wait 后巡检未结束")
	}
}

func TestHandGenerationChangeDuringReadListStopsBeforeUsingResult(t *testing.T) {
	h := newHarness(t)
	seedTracked(t, h, "generation-session", "peer-generation", []store.MessageDraft{draftText("old")})
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadList {
			h.hands.set(HandState{Online: true, Session: "session-2", BootID: "boot-1"})
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{summary("generation-session", "peer-generation", "new", 1)},
				Complete: true,
			}, nil
		}
		if request.Name == protocol.PrimChatReadThread {
			t.Fatal("hand session 已变化，本轮不得直接继续 readThread")
		}
		return defaultHandler(request)
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || !errors.Is(result.Rounds[0].Err, ErrActorGenerationChanged) {
		t.Fatalf("session 代际变化应中止本轮: result=%+v err=%v", result, err)
	}
	if h.runner.count(protocol.PrimChatReadThread) != 0 {
		t.Fatal("session 变化后仍派发了 readThread")
	}
	account, _ := h.db.AccountByKey(h.key)
	if !account.DirtyHint {
		t.Fatal("代际变化后必须保留 dirty 给下轮 fresh probe")
	}
}

func TestAccountRebindDuringReadListStopsBeforeUsingResult(t *testing.T) {
	h := newHarness(t)
	seedTracked(t, h, "generation-binding", "peer-binding", []store.MessageDraft{draftText("old")})
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadList {
			if _, _, err := h.db.BindAccountObservation(
				h.key, "hand-2", "principal-1", "session-2", "boot-2", h.clock.Now(), false,
			); err != nil {
				t.Fatalf("同一主体迁移到另一手: %v", err)
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{summary("generation-binding", "peer-binding", "new", 1)},
				Complete: true,
			}, nil
		}
		if request.Name == protocol.PrimChatReadThread {
			t.Fatal("账号改绑后本轮不得继续 readThread")
		}
		return defaultHandler(request)
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || !errors.Is(result.Rounds[0].Err, ErrActorGenerationChanged) {
		t.Fatalf("账号改绑应中止旧 actor 轮次: result=%+v err=%v", result, err)
	}
	account, _ := h.db.AccountByKey(h.key)
	if account.BoundHandID != "hand-2" || !account.DirtyHint {
		t.Fatalf("改绑结果或 dirty 丢失: %+v", account)
	}
}

func TestUserActiveQuietStartsWhenFailureIsObserved(t *testing.T) {
	h := newHarness(t)
	var existingUntil time.Time
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadList {
			h.clock.Add(2 * time.Minute)
			existingUntil = h.clock.Now().Add(2 * h.config.ManualQuiet)
			if err := h.db.MutateAccount(h.key, func(account *store.Account) error {
				account.ManualQuietUntil = timePointer(existingUntil)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			return nil, wrapRunError(protocol.ErrCodeUserActive, "", errors.New("manual activity"))
		}
		return defaultHandler(request)
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err == nil {
		t.Fatalf("USER_ACTIVE 应终止本轮: result=%+v err=%v", result, err)
	}
	account, _ := h.db.AccountByKey(h.key)
	want := existingUntil
	if account.ManualQuietUntil == nil || !account.ManualQuietUntil.Equal(want) {
		t.Fatalf("静默窗应从错误被观察时起算且只能延长: got=%v want=%v", account.ManualQuietUntil, want)
	}
}

func TestProbeMismatchPausesBeforeAnyAccountDataRead(t *testing.T) {
	h := newHarness(t)
	h.hands.set(HandState{Online: true, Session: "session-other", BootID: "boot-other"})
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name != protocol.PrimProbePlatform {
			t.Fatalf("mismatch must stop before %s", request.Name)
		}
		fingerprint := "another-principal"
		return protocol.ProbePlatformData{
			ContentScriptOk: true, LoginState: protocol.LoginStateIn, PageKind: protocol.PageKindIm,
			PrincipalFingerprint: &fingerprint, Surface: &protocol.PlatformSurface{ImListVisible: true},
		}, nil
	}
	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err == nil {
		t.Fatalf("Tick mismatch = %+v, %v", result, err)
	}
	if got := h.runner.names(); len(got) != 1 || got[0] != protocol.PrimProbePlatform {
		t.Fatalf("cross-account data command leaked: %v", got)
	}
	account, _ := h.db.AccountByKey(h.key)
	if account.IdentityState != store.IdentityInvalid || account.PausedReason != PauseAccountMismatch || account.StoppedAt == nil {
		t.Fatalf("mismatch was not manual-only paused: %+v", account)
	}
}

func TestManualOnlyThreadFailurePausesInsteadOfRecurringIntrusiveRead(t *testing.T) {
	h := newHarness(t)
	conversationKey := seedTracked(t, h, "conversation-manual-only", "peer-manual-only", []store.MessageDraft{
		draftText("old"),
	})
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					summary(conversationKey.ConversationRef, "peer-manual-only", "new", 1),
				},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			return nil, &RunError{
				Code: protocol.ErrCodeInternalHand, Retryable: protocol.RetryableManualOnly,
				SideEffect: protocol.SideEffectPossible, Cause: errors.New("未知手侧异常"),
			}
		default:
			return defaultHandler(request)
		}
	}

	first, err := h.manager.Tick(context.Background())
	if err != nil || len(first.Rounds) != 1 || first.Rounds[0].Err == nil {
		t.Fatalf("manualOnly 首轮应响亮失败: result=%+v err=%v", first, err)
	}
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil {
		t.Fatalf("AccountByKey: account=%+v err=%v", account, err)
	}
	if account.PausedReason != PauseHandManualReview || account.StoppedAt == nil || !account.DirtyHint {
		t.Fatalf("manualOnly 未停止 actor 并保留待对账提示: %+v", account)
	}
	if got := h.runner.count(protocol.PrimChatReadThread); got != 1 {
		t.Fatalf("首轮 readThread 次数=%d, want 1", got)
	}

	// 即使跨过 minimum gap 和多个正常巡检周期，真人重新开启前也不能把
	// 同一 manualOnly 失败伪装成新的自动对账机会。
	h.clock.Add(3 * h.config.PatrolInterval)
	second, err := h.manager.Tick(context.Background())
	if err != nil || len(second.Rounds) != 0 || h.runner.count(protocol.PrimChatReadThread) != 1 {
		t.Fatalf("暂停后仍自动重复 intrusive: result=%+v err=%v calls=%v", second, err, h.runner.names())
	}

	if err := h.manager.EnableToday(h.key); err != nil {
		t.Fatalf("真人重新开启: %v", err)
	}
	third, err := h.manager.Tick(context.Background())
	if err != nil || len(third.Rounds) != 1 || third.Rounds[0].Err == nil ||
		h.runner.count(protocol.PrimChatReadThread) != 2 {
		t.Fatalf("真人重新开启后未获得一次正常对账机会: result=%+v err=%v calls=%v", third, err, h.runner.names())
	}
}

func TestPageAbsentRecoveryFailureKeepsOpaqueBindingUnobservable(t *testing.T) {
	h := newHarness(t)
	h.hands.set(HandState{Online: true, Session: "session-new", BootID: "boot-new"})
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimProbePlatform:
			return protocol.ProbePlatformData{
				ContentScriptOk: false, LoginState: protocol.LoginStateUnknown,
				PageKind: protocol.PageKindNone, PrincipalFingerprint: nil, Surface: nil,
			}, nil
		case protocol.PrimNavEnsureSurface:
			return protocol.NavEnsureSurfaceData{Ready: false, LoginState: protocol.LoginStateUnknown}, nil
		default:
			t.Fatalf("unobservable identity must not run %s", request.Name)
			return nil, nil
		}
	}
	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err == nil || !result.Rounds[0].EnsureUsed {
		t.Fatalf("Tick page absent = %+v, %v", result, err)
	}
	account, _ := h.db.AccountByKey(h.key)
	if account.IdentityState != store.IdentityUnobservable || account.PrincipalFingerprint == nil ||
		*account.PrincipalFingerprint != "principal-1" {
		t.Fatalf("page absence erased/invalidated binding: %+v", account)
	}
	if account.StoppedAt != nil || account.PausedReason != "" {
		t.Fatalf("unknown login was treated as explicit logout: %+v", account)
	}
}

func TestManualQuietAndEventCoalescingRespectMinimumGap(t *testing.T) {
	h := newHarness(t)
	if err := h.manager.HandleEvent("hand-1", eventBody(t, h, protocol.EventManualInteraction, protocol.ManualInteractionEventData{
		At: h.clock.Now().UnixMilli(), Kind: protocol.ManualInteractionKindPointer, PageKind: protocol.PageKindIm,
	})); err != nil {
		t.Fatal(err)
	}
	h.clock.Add(44 * time.Second)
	before, err := h.manager.Tick(context.Background())
	if err != nil || len(before.Rounds) != 0 || len(h.runner.names()) != 0 {
		t.Fatalf("quiet window dispatched work: %+v %v calls=%v", before, err, h.runner.names())
	}
	h.clock.Add(time.Second)
	after, err := h.manager.Tick(context.Background())
	if err != nil || len(after.Rounds) != 1 || after.Rounds[0].Err != nil {
		t.Fatalf("quiet expiry did not run: %+v %v", after, err)
	}

	// Two unread increases inside the 25s merge window do not slide the
	// schedule, and the previous round imposes the 60s lower bound.
	h.clock.Add(5 * time.Second)
	prev := 0
	if err := h.manager.HandleEvent("hand-1", eventBody(t, h, protocol.EventUnreadBadge, protocol.UnreadBadgeEventData{
		Prev: &prev, Scope: protocol.UnreadScopeTotal, Stable: true, Value: 1,
	})); err != nil {
		t.Fatal(err)
	}
	account, _ := h.db.AccountByKey(h.key)
	firstTarget := *account.NextPatrolAt
	h.clock.Add(5 * time.Second)
	prev = 1
	if err := h.manager.HandleEvent("hand-1", eventBody(t, h, protocol.EventUnreadBadge, protocol.UnreadBadgeEventData{
		Prev: &prev, Scope: protocol.UnreadScopeTotal, Stable: true, Value: 2,
	})); err != nil {
		t.Fatal(err)
	}
	account, _ = h.db.AccountByKey(h.key)
	if !account.NextPatrolAt.Equal(firstTarget) {
		t.Fatalf("merge window slid target: first=%v second=%v", firstTarget, *account.NextPatrolAt)
	}
	if account.LastPatrolAt == nil || account.NextPatrolAt.Before(account.LastPatrolAt.Add(time.Minute)) {
		t.Fatalf("event bypassed 60s minimum: %+v", account)
	}
}

func TestSameDayRestartRestoresActorAndOfflineTickQueuesNothing(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)}
	key := store.AccountKey{Platform: "zhilian", AccountRef: "restart-account"}
	if err := db.CreateAccount(&store.Account{Platform: key.Platform, AccountRef: key.AccountRef}); err != nil {
		t.Fatal(err)
	}
	if err := db.BindAccountPrincipal(key, "hand-r", "principal-1", "session-1", "boot-1", clock.Now()); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{handler: defaultHandler}
	hands := &fakeHands{state: HandState{Online: false, Session: "session-1", BootID: "boot-1"}}
	config := Config{Clock: clock, Location: time.UTC, IdentityFreshFor: time.Hour, NewRoundID: func() string { return "round-restart" }}
	manager, err := NewManager(db, runner, hands, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.EnableToday(key); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manager, err = NewManager(db, runner, hands, config)
	if err != nil {
		t.Fatal(err)
	}
	offline, err := manager.Tick(context.Background())
	if err != nil || len(offline.Rounds) != 0 || runner.count(protocol.PrimChatReadList) != 0 {
		t.Fatalf("offline tick queued work: %+v %v calls=%v", offline, err, runner.names())
	}
	account, _ := db.AccountByKey(key)
	if account.LastPatrolAt != nil {
		t.Fatalf("offline tick created logical work: %+v", account)
	}
	hands.set(HandState{Online: true, Session: "session-1", BootID: "boot-1"})
	online, err := manager.Tick(context.Background())
	if err != nil || len(online.Rounds) != 1 || online.Rounds[0].Err != nil || runner.count(protocol.PrimChatReadList) != 1 {
		t.Fatalf("same-day actor did not recover: %+v %v calls=%v", online, err, runner.names())
	}
}

func TestPeriodicRoundFindsChangeWhenAllEventsAreLost(t *testing.T) {
	h := newHarness(t)
	conversationKey := seedTracked(t, h, "conversation-periodic", "peer-periodic", []store.MessageDraft{draftText("old")})
	newAvailable := false
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			preview := "old"
			if newAvailable {
				preview = "new"
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{summary(conversationKey.ConversationRef, "peer-periodic", preview, 0)}, Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{threadText(0, "old"), threadText(1, "new")},
				Peer:     ptr(protocol.PeerSummary{DisplayName: "候选人", PlatformUserRef: "peer-periodic"}),
				Complete: true, AnchorMatched: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}
	first, err := h.manager.Tick(context.Background())
	if err != nil || first.ProjectionCount() != 0 || h.runner.count(protocol.PrimChatReadThread) != 0 {
		t.Fatalf("clean first round = %+v %v calls=%v", first, err, h.runner.names())
	}
	newAvailable = true
	h.clock.Add(h.config.PatrolInterval)
	second, err := h.manager.Tick(context.Background())
	if err != nil || second.ProjectionCount() != 1 {
		t.Fatalf("periodic truth did not discover lost event: %+v %v", second, err)
	}
	messages, _ := h.db.MessagesForConversation(conversationKey)
	if len(messages) != 2 || messages[1].Text == nil || *messages[1].Text != "new" {
		t.Fatalf("ledger after periodic discovery = %+v", messages)
	}
}

func TestEnsureOncePerRoundAndThreeRoundsPauseAcrossManagerRestart(t *testing.T) {
	h := newHarness(t)
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return nil, &RunError{
				Code: protocol.ErrCodeCtxNotReady, Reason: protocol.NotReadyReasonPageAbsent,
				Cause: errors.New("page closed"),
			}
		case protocol.PrimNavEnsureSurface:
			return protocol.NavEnsureSurfaceData{Ready: true, LoginState: protocol.LoginStateIn}, nil
		default:
			return defaultHandler(request)
		}
	}

	for round := 1; round <= 3; round++ {
		result, err := h.manager.Tick(context.Background())
		if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err == nil || !result.Rounds[0].EnsureUsed {
			t.Fatalf("round %d = %+v, %v", round, result, err)
		}
		if round < 3 {
			h.clock.Add(h.config.PatrolInterval)
			// Recreate the manager to prove the safety count comes from SQLite,
			// not process memory.
			manager, managerErr := NewManager(h.db, h.runner, h.hands, h.config)
			if managerErr != nil {
				t.Fatal(managerErr)
			}
			h.manager = manager
		}
	}
	if got := h.runner.count(protocol.PrimNavEnsureSurface); got != 3 {
		t.Fatalf("ensure count = %d, want one per round", got)
	}
	if got := h.runner.count(protocol.PrimChatReadList); got != 6 {
		t.Fatalf("readList count = %d, want original+single retry per round", got)
	}
	account, _ := h.db.AccountByKey(h.key)
	if account.PausedReason != PauseSurfaceDrivenAway || account.StoppedAt == nil {
		t.Fatalf("third driven-away round did not pause: %+v", account)
	}
}

func TestRepeatedAndLateSnapshotsNeverProjectTwice(t *testing.T) {
	h := newHarness(t)
	conversationKey := seedTracked(t, h, "conversation-repeat", "peer-repeat", []store.MessageDraft{
		draftText("old-1"), draftText("old-2"),
	})
	threadRound := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{summary(conversationKey.ConversationRef, "peer-repeat", "new", 1)}, Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			threadRound++
			messages := []protocol.ThreadMessage{threadText(0, "old-1"), threadText(1, "old-2"), threadText(2, "new")}
			if threadRound == 3 {
				messages = messages[:2] // delayed snapshot wholly before current tail
			}
			return protocol.ChatReadThreadData{
				Messages: messages, Peer: ptr(protocol.PeerSummary{DisplayName: "候选人", PlatformUserRef: "peer-repeat"}),
				Complete: true, ReachedTop: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	wantProjection := []int{1, 0, 0}
	for i, want := range wantProjection {
		result, err := h.manager.Tick(context.Background())
		if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
			t.Fatalf("round %d = %+v, %v", i+1, result, err)
		}
		if got := result.ProjectionCount(); got != want {
			t.Fatalf("round %d projection = %d, want %d", i+1, got, want)
		}
		h.clock.Add(h.config.PatrolInterval)
	}
	messages, _ := h.db.MessagesForConversation(conversationKey)
	if len(messages) != 3 {
		t.Fatalf("duplicate/late snapshots changed ledger: %+v", messages)
	}
}

func TestZeroOverlapDiscardsShallowThenDeepRebaselinesWithoutProjection(t *testing.T) {
	h := newHarness(t)
	conversationKey := seedTracked(t, h, "conversation-deep", "peer-deep", []store.MessageDraft{draftText("ledger-old")})
	threadCalls := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{summary(conversationKey.ConversationRef, "peer-deep", "deep-2", 1)}, Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			threadCalls++
			args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
			if threadCalls == 1 {
				if args.Window.Deep {
					t.Fatal("first thread read unexpectedly deep")
				}
				return protocol.ChatReadThreadData{
					Messages: []protocol.ThreadMessage{threadText(0, "shallow-discard")},
					Peer:     ptr(protocol.PeerSummary{DisplayName: "候选人", PlatformUserRef: "peer-deep"}),
					Complete: true, ReachedTop: true,
				}, nil
			}
			if !args.Window.Deep || args.Cursor != "" {
				t.Fatalf("deep retry must restart without cursor: %+v", args)
			}
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{threadText(0, "deep-1"), threadText(1, "deep-2")},
				Peer:     ptr(protocol.PeerSummary{DisplayName: "候选人", PlatformUserRef: "peer-deep"}),
				Complete: true, ReachedTop: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}
	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("Tick deep = %+v, %v", result, err)
	}
	if result.ProjectionCount() != 0 {
		t.Fatalf("deep zero-overlap emitted historical projection: %+v", result.Rounds[0].Projections)
	}
	messages, _ := h.db.MessagesForConversation(conversationKey)
	if len(messages) != 3 || messages[1].Text == nil || *messages[1].Text != "deep-1" || *messages[2].Text != "deep-2" {
		t.Fatalf("deep baseline = %+v", messages)
	}
	for _, message := range messages {
		if message.Text != nil && *message.Text == "shallow-discard" {
			t.Fatal("shallow zero-overlap aggregate leaked into the ledger")
		}
	}
}

func TestCursorInvalidDiscardsPartialListBeforeSafeRestart(t *testing.T) {
	h := newHarness(t)
	firstPageCalls := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name != protocol.PrimChatReadList {
			return defaultHandler(request)
		}
		args := decodeArgs[protocol.ChatReadListArgs](t, request)
		if args.Cursor == "expired-cursor" {
			return nil, &RunError{Code: protocol.ErrCodeCursorInvalid, Cause: errors.New("expired")}
		}
		firstPageCalls++
		if firstPageCalls == 1 {
			next := "expired-cursor"
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{summary("discard-me", "peer-old", "old", 0)},
				Complete: false, NextCursor: &next,
			}, nil
		}
		return protocol.ChatReadListData{
			Sessions: []protocol.ConversationSummary{summary("keep-me", "peer-new", "new", 0)}, Complete: true,
		}, nil
	}
	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("Tick = %+v, %v", result, err)
	}
	conversations, err := h.db.ConversationsForAccount(h.key)
	if err != nil {
		t.Fatal(err)
	}
	if len(conversations) != 1 || conversations[0].ConversationRef != "keep-me" {
		t.Fatalf("partial aggregate leaked across cursor restart: %+v", conversations)
	}
}
