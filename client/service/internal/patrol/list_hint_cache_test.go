package patrol

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

func listHintTestSummary(
	conversationRef string,
	peerRef string,
	preview string,
	unread int,
	lastActivityMs *int64,
) protocol.ConversationSummary {
	got := summary(conversationRef, peerRef, preview, unread)
	got.LastMessage.Direction = protocol.MessageDirectionSystem
	got.LastMessage.Kind = protocol.MessageKindSystem
	got.LastActivityTs = lastActivityMs
	return got
}

func installListHintTestRunner(
	t *testing.T,
	h *harness,
	current *protocol.ConversationSummary,
	threadCalls *int,
	threadFailure *error,
) {
	t.Helper()
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{*current},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			*threadCalls++
			if threadFailure != nil && *threadFailure != nil {
				return nil, *threadFailure
			}
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{threadText(0, "authoritative")},
				Peer: ptr(protocol.PeerSummary{
					DisplayName:     "候选人",
					PlatformUserRef: current.Peer.PlatformUserRef,
				}),
				Complete:   true,
				ReachedTop: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}
}

func requireSuccessfulHintTick(t *testing.T, h *harness) {
	t.Helper()
	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("列表提示巡检失败: result=%+v err=%v", result, err)
	}
}

func verificationKeyForHarness(
	t *testing.T,
	h *harness,
	conversationRef string,
) listHintVerificationKey {
	t.Helper()
	account, err := h.db.AccountByKey(h.key)
	if err != nil || account == nil || account.PrincipalFingerprint == nil {
		t.Fatalf("读取账号身份代际失败: account=%+v err=%v", account, err)
	}
	return makeListHintVerificationKey(
		h.key.Platform,
		h.key.AccountRef,
		*account.PrincipalFingerprint,
		conversationRef,
	)
}

func seedVerifiedListHint(
	t *testing.T,
	h *harness,
	current protocol.ConversationSummary,
) (listHintVerificationKey, string) {
	t.Helper()
	key := verificationKeyForHarness(t, h, current.ConversationRef)
	fingerprint := listHintFingerprint(key, current)
	h.manager.mu.Lock()
	h.manager.markListHintVerified(key, fingerprint)
	h.manager.mu.Unlock()
	return key, fingerprint
}

func TestVerifiedListHintSuppressesRepeatedLowFidelityMismatch(t *testing.T) {
	h := newHarness(t)
	key := seedTracked(t, h, "hint-repeat", "peer-hint-repeat", []store.MessageDraft{
		draftText("authoritative"),
	})
	activity := int64(100)
	current := listHintTestSummary(
		key.ConversationRef,
		"peer-hint-repeat",
		"authoritative",
		0,
		&activity,
	)
	threadCalls := 0
	installListHintTestRunner(t, h, &current, &threadCalls, nil)

	requireSuccessfulHintTick(t, h)
	if threadCalls != 1 {
		t.Fatalf("首次未核对提示 readThread=%d, want 1", threadCalls)
	}

	h.clock.Add(h.config.PatrolInterval)
	requireSuccessfulHintTick(t, h)
	if threadCalls != 1 {
		t.Fatalf("相同已核对提示不应重复读取详情: readThread=%d", threadCalls)
	}

	cacheKey := verificationKeyForHarness(t, h, key.ConversationRef)
	h.manager.mu.Lock()
	cached := h.manager.verifiedListHints[cacheKey]
	h.manager.mu.Unlock()
	if len(cached) != 64 || strings.Contains(cached, current.LastMessage.TextPreview) {
		t.Fatalf("缓存必须只保留 64 位摘要: %q", cached)
	}
}

func TestVerifiedListHintFieldChangesTriggerAnotherRead(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*protocol.ConversationSummary)
	}{
		{
			name: "raw direction",
			mutate: func(summary *protocol.ConversationSummary) {
				summary.LastMessage.Direction = protocol.MessageDirectionOut
			},
		},
		{
			name: "raw kind",
			mutate: func(summary *protocol.ConversationSummary) {
				summary.LastMessage.Kind = protocol.MessageKindText
			},
		},
		{
			name: "canonical preview",
			mutate: func(summary *protocol.ConversationSummary) {
				summary.LastMessage.TextPreview = "changed preview"
			},
		},
		{
			name: "unread",
			mutate: func(summary *protocol.ConversationSummary) {
				summary.UnreadCount = 1
			},
		},
		{
			name: "last activity",
			mutate: func(summary *protocol.ConversationSummary) {
				changed := int64(101)
				summary.LastActivityTs = &changed
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			key := seedTracked(t, h, "hint-field-"+test.name, "peer-hint-field", []store.MessageDraft{
				draftText("authoritative"),
			})
			activity := int64(100)
			current := listHintTestSummary(
				key.ConversationRef,
				"peer-hint-field",
				"authoritative",
				0,
				&activity,
			)
			threadCalls := 0
			installListHintTestRunner(t, h, &current, &threadCalls, nil)
			requireSuccessfulHintTick(t, h)

			test.mutate(&current)
			h.clock.Add(h.config.PatrolInterval)
			requireSuccessfulHintTick(t, h)
			if threadCalls != 2 {
				t.Fatalf("fingerprint 字段变化后 readThread=%d, want 2", threadCalls)
			}
		})
	}
}

func TestVerifiedCleanHintLastActivityChangeTriggersRead(t *testing.T) {
	h := newHarness(t)
	key := seedTracked(t, h, "hint-clean-activity", "peer-clean-activity", []store.MessageDraft{
		draftText("authoritative"),
	})
	activity := int64(100)
	current := summary(key.ConversationRef, "peer-clean-activity", "authoritative", 0)
	current.LastActivityTs = &activity
	threadCalls := 0
	installListHintTestRunner(t, h, &current, &threadCalls, nil)

	// The first read is a mandatory low-frequency reconciliation. It records a
	// clean list hint even though the strict summary already matches the ledger.
	h.clock.Add(h.manager.config.TrackedReconcileInterval + time.Minute)
	requireSuccessfulHintTick(t, h)
	if threadCalls != 1 {
		t.Fatalf("到期强制核对 readThread=%d, want 1", threadCalls)
	}

	changed := int64(101)
	current.LastActivityTs = &changed
	h.clock.Add(h.config.PatrolInterval)
	requireSuccessfulHintTick(t, h)
	if threadCalls != 2 {
		t.Fatalf("已登记 clean hint 仅 activity 变化也必须重读: %d", threadCalls)
	}
}

func TestVerifiedListHintNeverSuppressesMandatoryReconciliation(t *testing.T) {
	tests := []struct {
		name    string
		adopted []store.MessageDraft
		expire  bool
	}{
		{name: "pending and empty", adopted: nil},
		{name: "adopted empty", adopted: []store.MessageDraft{}},
		{name: "expired", adopted: []store.MessageDraft{draftText("authoritative")}, expire: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			key := seedTracked(
				t,
				h,
				"hint-force-"+test.name,
				"peer-hint-force",
				test.adopted,
			)
			current := summary(key.ConversationRef, "peer-hint-force", "authoritative", 0)
			seedVerifiedListHint(t, h, current)
			if test.expire {
				h.clock.Add(h.manager.config.TrackedReconcileInterval + time.Minute)
			}
			threadCalls := 0
			installListHintTestRunner(t, h, &current, &threadCalls, nil)

			requireSuccessfulHintTick(t, h)
			if threadCalls != 1 {
				t.Fatalf("强制条件被已核对提示错误抑制: readThread=%d", threadCalls)
			}
		})
	}
}

func TestFailedThreadReadDoesNotVerifyListHint(t *testing.T) {
	h := newHarness(t)
	key := seedTracked(t, h, "hint-read-failure", "peer-hint-failure", []store.MessageDraft{
		draftText("authoritative"),
	})
	activity := int64(100)
	current := listHintTestSummary(
		key.ConversationRef,
		"peer-hint-failure",
		"authoritative",
		0,
		&activity,
	)
	threadCalls := 0
	threadFailure := error(&RunError{
		Code:  protocol.ErrCodeTargetNotFound,
		Cause: errors.New("synthetic target disappeared"),
	})
	installListHintTestRunner(t, h, &current, &threadCalls, &threadFailure)

	requireSuccessfulHintTick(t, h)
	cacheKey := verificationKeyForHarness(t, h, key.ConversationRef)
	h.manager.mu.Lock()
	_, cachedAfterFailure := h.manager.verifiedListHints[cacheKey]
	h.manager.mu.Unlock()
	if cachedAfterFailure {
		t.Fatal("readThread 失败后错误登记了已核对 fingerprint")
	}

	threadFailure = nil
	h.clock.Add(h.config.PatrolInterval)
	requireSuccessfulHintTick(t, h)
	if threadCalls != 2 {
		t.Fatalf("失败提示下一 Tick 未重新读取: readThread=%d", threadCalls)
	}
}

func TestApplyPlanFailureDoesNotVerifyListHint(t *testing.T) {
	h := newHarness(t)
	key := seedTracked(t, h, "hint-apply-failure", "peer-apply-failure", []store.MessageDraft{
		draftText("authoritative"),
	})
	activity := int64(100)
	current := listHintTestSummary(
		key.ConversationRef,
		"peer-apply-failure",
		"platform-new",
		0,
		&activity,
	)
	mutated := false
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{current},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			if !mutated {
				mutated = true
				if _, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
					Key:             key,
					ExpectedTailSeq: 1,
					PlatformUserRef: "peer-apply-failure",
					NewMessages:     []store.MessageDraft{draftText("concurrent")},
					SyncedAt:        h.clock.Now(),
				}); err != nil {
					t.Fatalf("制造 ApplyPlan 并发冲突失败: %v", err)
				}
			}
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{
					threadText(0, "authoritative"),
					threadText(1, "platform-new"),
				},
				Peer: ptr(protocol.PeerSummary{
					DisplayName:     "候选人",
					PlatformUserRef: "peer-apply-failure",
				}),
				Complete:      true,
				AnchorMatched: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 ||
		!errors.Is(result.Rounds[0].Err, store.ErrConversationVersionConflict) {
		t.Fatalf("预期 ApplyPlan 版本冲突: result=%+v err=%v", result, err)
	}
	cacheKey := verificationKeyForHarness(t, h, key.ConversationRef)
	h.manager.mu.Lock()
	_, cached := h.manager.verifiedListHints[cacheKey]
	h.manager.mu.Unlock()
	if cached {
		t.Fatal("ApplyPlan 失败后错误登记了已核对 fingerprint")
	}
}

func TestSourceIdentityConflictDoesNotVerifyListHint(t *testing.T) {
	h := newHarness(t)
	sourceKey := strings.Repeat("8", 64)
	old := draftText("authoritative")
	old.SourceKey = &sourceKey
	key := seedTracked(t, h, "hint-identity-conflict", "peer-identity-conflict", []store.MessageDraft{old})
	current := summary(key.ConversationRef, "peer-identity-conflict", "changed", 1)
	conflicting := threadText(0, "changed")
	conflicting.SourceKey = sourceKey
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{current},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{conflicting},
				Peer: ptr(protocol.PeerSummary{
					DisplayName:     "候选人",
					PlatformUserRef: "peer-identity-conflict",
				}),
				Complete:      true,
				AnchorMatched: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 ||
		!errors.Is(result.Rounds[0].Err, syncledger.ErrSourceKeySemanticConflict) {
		t.Fatalf("预期稳定消息身份冲突: result=%+v err=%v", result, err)
	}
	cacheKey := verificationKeyForHarness(t, h, key.ConversationRef)
	h.manager.mu.Lock()
	_, cached := h.manager.verifiedListHints[cacheKey]
	h.manager.mu.Unlock()
	if cached {
		t.Fatal("稳定身份冲突后错误登记了已核对 fingerprint")
	}
}

func TestListHintAThenFailedBThenARereads(t *testing.T) {
	h := newHarness(t)
	key := seedTracked(t, h, "hint-aba", "peer-hint-aba", []store.MessageDraft{
		draftText("authoritative"),
	})
	activityA := int64(100)
	current := listHintTestSummary(
		key.ConversationRef,
		"peer-hint-aba",
		"authoritative",
		0,
		&activityA,
	)
	threadCalls := 0
	var threadFailure error
	installListHintTestRunner(t, h, &current, &threadCalls, &threadFailure)

	requireSuccessfulHintTick(t, h)

	activityB := int64(200)
	current.LastActivityTs = &activityB
	threadFailure = &RunError{
		Code:  protocol.ErrCodeTargetNotFound,
		Cause: errors.New("synthetic B failure"),
	}
	h.clock.Add(h.config.PatrolInterval)
	requireSuccessfulHintTick(t, h)

	cacheKey := verificationKeyForHarness(t, h, key.ConversationRef)
	h.manager.mu.Lock()
	_, cachedAfterB := h.manager.verifiedListHints[cacheKey]
	h.manager.mu.Unlock()
	if cachedAfterB {
		t.Fatal("B 观察失败后仍保留了旧 A fingerprint")
	}

	current.LastActivityTs = &activityA
	threadFailure = nil
	h.clock.Add(h.config.PatrolInterval)
	requireSuccessfulHintTick(t, h)
	if threadCalls != 3 {
		t.Fatalf("A -> B失败 -> A 应再次读取: readThread=%d", threadCalls)
	}
}

func TestListHintCacheIsolatedByIdentityAndManagerLifetime(t *testing.T) {
	h := newHarness(t)
	key := seedTracked(t, h, "hint-generation", "peer-hint-generation", []store.MessageDraft{
		draftText("authoritative"),
	})
	activity := int64(100)
	current := listHintTestSummary(
		key.ConversationRef,
		"peer-hint-generation",
		"authoritative",
		0,
		&activity,
	)
	threadCalls := 0
	installListHintTestRunner(t, h, &current, &threadCalls, nil)
	requireSuccessfulHintTick(t, h)

	identityA := verificationKeyForHarness(t, h, key.ConversationRef)
	identityB := makeListHintVerificationKey(
		identityA.platform,
		identityA.accountRef,
		"principal-other-generation",
		identityA.conversationRef,
	)
	fingerprintB := listHintFingerprint(identityB, current)
	h.manager.mu.Lock()
	verifiedB, changedB := h.manager.observeListHintFingerprint(identityB, fingerprintB)
	_, identityAStillCached := h.manager.verifiedListHints[identityA]
	h.manager.mu.Unlock()
	if verifiedB || changedB || !identityAStillCached {
		t.Fatalf("身份代际错误共享或失效缓存: verified=%v changed=%v keepA=%v",
			verifiedB, changedB, identityAStillCached)
	}

	restarted, err := NewManager(h.db, h.runner, h.hands, h.config)
	if err != nil {
		t.Fatal(err)
	}
	h.manager = restarted
	h.clock.Add(h.config.PatrolInterval)
	requireSuccessfulHintTick(t, h)
	if threadCalls != 2 {
		t.Fatalf("新 Manager 应自然丢失缓存并多核对一次: readThread=%d", threadCalls)
	}
}
