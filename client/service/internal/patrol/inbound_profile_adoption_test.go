package patrol

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"recruithelper/client/service/internal/dispatch"
	"recruithelper/client/service/internal/m5ai"
	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

func TestPageDrivenPatrolAdoptsInboundProfileAndCompletesFirstReply(t *testing.T) {
	h := newHarness(t)
	savePatrolInboundLegacyJob(t, h, "job-inbound", "客户 经理")

	conversationRef := "conversation-inbound-unique"
	platformUserRef := "peer-inbound-unique"
	positionTitle := " 客户\t经理 "
	sourceKey := strings.Repeat("a", 64)
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{{
					ConversationRef: conversationRef,
					Peer: protocol.PeerSummary{
						PlatformUserRef: platformUserRef,
						DisplayName:     "合成候选人",
					},
					PositionTitle: &positionTitle,
					UnreadCount:   1,
					LastMessage: protocol.LastMessageSummary{
						Direction:   protocol.MessageDirectionIn,
						Kind:        protocol.MessageKindText,
						TextPreview: "想了解一下",
					},
				}},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
			if args.ConversationRef != conversationRef {
				t.Fatalf("深读了错误会话: %+v", args)
			}
			text := "想了解一下"
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{{
					Idx: 0, Direction: protocol.MessageDirectionIn,
					Kind: protocol.MessageKindText, Text: &text,
					ContentHash: syncledger.HashText(text), SourceKey: sourceKey,
				}},
				Peer: &protocol.PeerSummary{
					PlatformUserRef: platformUserRef,
					DisplayName:     "合成候选人",
				},
				Complete: true, ReachedTop: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	advice := &recordingAdviceExecutor{}
	hand := &m5PositiveHand{now: h.clock.Now}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5InboundAutomaticRunner{m5AutomaticReplyRunner: &m5AutomaticReplyRunner{
		base: h.runner, dispatcher: dispatcher,
	}}
	manager, err := NewManager(h.db, runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("页面驱动主动来聊闭环失败: result=%+v err=%v", result, err)
	}
	if h.runner.count(protocol.PrimChatReadThread) != 1 {
		t.Fatalf("主动建档后应在同轮收编消息: calls=%v", h.runner.names())
	}
	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: conversationRef,
	}
	profile, err := h.db.CandidateProfileByConversation(key)
	if err != nil || profile == nil ||
		profile.MainStatus != store.CandidateProfileCommunicating ||
		profile.PlatformUserRef != platformUserRef ||
		profile.PositionRef != "job-inbound" ||
		profile.BackendJobID == nil || *profile.BackendJobID != "job-inbound" ||
		profile.PositionTitle == nil || *profile.PositionTitle != "客户 经理" ||
		profile.ResumeCaptureState != store.ResumeCaptureCaptured ||
		profile.ActiveResumeSnapshotID == nil {
		t.Fatalf("页面职位未唯一绑定到后台 Job.ID: profile=%+v err=%v", profile, err)
	}
	conversation, err := h.db.ConversationByKey(key)
	messages, messagesErr := h.db.MessagesForConversation(key)
	if err != nil || messagesErr != nil || conversation == nil ||
		conversation.TrackingState != store.TrackingAdopted ||
		len(messages) != 2 ||
		messages[0].Direction != "in" ||
		messages[1].Direction != "out" ||
		messages[1].Origin != "self" {
		root, rootErr := h.db.CommunicationV4AggregateByProfile(profile.ProfileID)
		turn, turnErr := h.db.LatestDialogueTurnForProfile(profile.ProfileID)
		var action *store.CommunicationAction
		var actionErr error
		if turn != nil {
			action, actionErr = h.db.CommunicationActionByTurn(turn.TurnID)
		}
		t.Fatalf("主动建档后消息账本未收束: conversation=%+v messages=%+v root=%+v rootErr=%v turn=%+v turnErr=%v action=%+v actionErr=%v advice=%d handCommands=%d err=%v messagesErr=%v",
			conversation, messages, root, rootErr, turn, turnErr, action, actionErr,
			len(advice.requests), hand.commandCount(), err, messagesErr)
	}
	root, rootErr := h.db.CommunicationV4AggregateByProfile(profile.ProfileID)
	if rootErr != nil || root == nil ||
		!store.IsInboundConversationV4Root(root.RootGreetingIntentID) ||
		root.ProjectedThroughSeq != 2 {
		t.Fatalf("主动来聊事实根未推进首轮: root=%+v err=%v", root, rootErr)
	}
	turn, err := h.db.LatestDialogueTurnForProfile(profile.ProfileID)
	if err != nil || turn == nil ||
		turn.HistoryThroughSeq != 0 ||
		turn.Status != store.DialogueTurnCompleted {
		t.Fatalf("主动来聊首轮未完成: turn=%+v err=%v", turn, err)
	}
	action, err := h.db.CommunicationActionByTurn(turn.TurnID)
	if err != nil || action == nil ||
		action.Status != store.CommunicationActionSent ||
		action.EffectIntentID == nil {
		t.Fatalf("主动来聊回复未以 WAL 正证完成: action=%+v err=%v", action, err)
	}
	if len(advice.requests) != 2 ||
		advice.requests[0].Purpose != m5ai.PurposeIntent ||
		advice.requests[1].Purpose != m5ai.PurposeReply ||
		hand.commandCount() != 2 {
		t.Fatalf("主动来聊调用链不完整: advice=%+v handCommands=%d",
			advice.requests, hand.commandCount())
	}
	trial, err := h.db.M5TrialStatus()
	if err != nil || trial != nil {
		t.Fatalf("主动来聊不得伪造一次性试运行: trial=%+v err=%v", trial, err)
	}
	assertInboundAdoptionAudit(
		t,
		h,
		conversationRef,
		"status=adopted",
	)
	assertInboundAdoptionAudit(
		t,
		h,
		conversationRef,
		"status=rooted",
	)
}

func TestInboundResumeCaptureContinuesCurrentVisibleWindow(t *testing.T) {
	h := newHarness(t)
	savePatrolInboundLegacyJob(t, h, "job-resume-fresh", "客户经理")

	conversationRef := "conversation-resume-fresh"
	laterConversationRef := "conversation-after-resume-capture"
	platformUserRef := "peer-resume-fresh"
	laterPlatformUserRef := "peer-after-resume-capture"
	positionTitle := "客户经理"
	inboundText := "想了解一下"
	key := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: conversationRef,
	}
	if err := h.db.SaveConversationList(store.SaveConversationListRequest{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ObservedAt: h.clock.Now(), Complete: true,
		Entries: []store.ListIndexEntry{{
			ConversationRef: conversationRef, PlatformUserRef: platformUserRef,
			PeerDisplayName: "合成候选人", LastMessageDirection: "in",
			LastMessageKind: "text", LastMessagePreview: inboundText,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	adopted, err := h.db.AdoptInboundConversationProfile(
		store.AdoptInboundConversationProfileRequest{
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			ConversationRef: conversationRef, PlatformUserRef: platformUserRef,
			DisplayName: "合成候选人", PositionTitle: positionTitle,
			ObservedAt: h.clock.Now(),
		},
	)
	if err != nil || adopted == nil || adopted.Profile == nil ||
		adopted.Outcome != store.InboundProfileAdopted {
		t.Fatalf("预置主动来聊档案失败: result=%+v err=%v", adopted, err)
	}
	stableSourceKey := strings.Repeat("a", 64)
	inbound := draftText(inboundText)
	inbound.SourceKey = &stableSourceKey
	if _, err := h.db.ApplyConversationChanges(store.ApplyConversationChangesRequest{
		Key: key, ExpectedTailSeq: 0, PlatformUserRef: platformUserRef,
		NewMessages: []store.MessageDraft{inbound},
		Adopt:       true, SyncedAt: h.clock.Now(),
	}); err != nil {
		t.Fatalf("预置已收编会话失败: %v", err)
	}
	seedTracked(
		t,
		h,
		laterConversationRef,
		laterPlatformUserRef,
		[]store.MessageDraft{draftText("旧消息")},
	)

	listCalls := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			if args.Move != protocol.ListWindowMoveReset {
				t.Fatalf("完整可见窗口无需 fresh 重读或 next 续窗: %+v", args)
			}
			listCalls++
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{
					{
						ConversationRef: conversationRef,
						Peer: protocol.PeerSummary{
							PlatformUserRef: platformUserRef,
							DisplayName:     "合成候选人",
						},
						PositionTitle: &positionTitle,
						UnreadCount:   0,
						LastMessage: protocol.LastMessageSummary{
							Direction:   protocol.MessageDirectionIn,
							Kind:        protocol.MessageKindText,
							TextPreview: inboundText,
						},
					},
					{
						ConversationRef: laterConversationRef,
						Peer: protocol.PeerSummary{
							PlatformUserRef: laterPlatformUserRef,
							DisplayName:     "合成候选人",
						},
						UnreadCount: 1,
						LastMessage: protocol.LastMessageSummary{
							Direction:   protocol.MessageDirectionIn,
							Kind:        protocol.MessageKindText,
							TextPreview: "更新消息",
						},
					},
				},
				Complete: true,
			}, nil
		case protocol.PrimChatReadThread:
			args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
			if args.ConversationRef != laterConversationRef {
				t.Fatalf("简历补采会话账本与摘要一致，不应深读: %+v", args)
			}
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{
					threadText(0, "旧消息"),
					threadText(1, "更新消息"),
				},
				Peer: &protocol.PeerSummary{
					PlatformUserRef: laterPlatformUserRef,
					DisplayName:     "合成候选人",
				},
				Complete: true, ReachedTop: true,
			}, nil
		default:
			return defaultHandler(request)
		}
		return nil, errors.New("unreachable")
	}

	hand := &m5PositiveHand{now: h.clock.Now}
	dispatcher := dispatch.New(h.db, hand)
	hand.setDispatcher(dispatcher)
	runner := &m5InboundAutomaticRunner{m5AutomaticReplyRunner: &m5AutomaticReplyRunner{
		base: h.runner, dispatcher: dispatcher,
	}}
	advice := &recordingAdviceExecutor{
		complete: func(_ int, _ m5ai.CompletionRequest) (m5ai.CompletionResponse, error) {
			return m5ai.CompletionResponse{}, errors.New("fixture stops before candidate-visible action")
		},
	}
	manager, err := NewManager(h.db, runner, h.hands, h.config, advice)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("简历补采巡检失败: result=%+v err=%v", result, err)
	}
	if h.runner.count(protocol.PrimChatReadThread) != 1 || hand.commandCount() != 1 {
		t.Fatalf("简历动作后必须继续深读同屏后一会话: calls=%v handCommands=%d",
			h.runner.names(), hand.commandCount())
	}
	if listCalls != 1 {
		t.Fatalf("简历动作后不应重读完整可见窗口: listCalls=%d", listCalls)
	}
	profile, err := h.db.CandidateProfileByID(adopted.Profile.ProfileID)
	if err != nil || profile == nil ||
		profile.ResumeCaptureState != store.ResumeCaptureCaptured {
		t.Fatalf("简历动作未完成: profile=%+v err=%v", profile, err)
	}
	assertInboundAdoptionAudit(
		t,
		h,
		laterConversationRef,
		"status=skipped reason=missingPositionTitle",
	)
}

func TestPageDrivenInboundAdoptionSkipsLocalFailuresAndContinuesLaterRows(t *testing.T) {
	h := newHarness(t)
	savePatrolInboundLegacyJob(t, h, "job-valid", "客户经理")

	displayName, oldPositionTitle := "既有候选人", "既有职位"
	if _, err := h.db.SelectCandidateProfile(store.SelectCandidateProfileRequest{
		ProfileID: "profile-existing-human",
		Scope: store.CandidateProfileScope{
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			PlatformUserRef: "peer-human-conflict", PositionRef: "old-job",
		},
		DisplayName: &displayName, PositionTitle: &oldPositionTitle,
		ObservedAt: h.clock.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	factConflictKey := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: "conversation-fact-conflict",
	}
	if err := h.db.SaveConversationList(store.SaveConversationListRequest{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ObservedAt: h.clock.Now(), Complete: true,
		Entries: []store.ListIndexEntry{{
			ConversationRef: factConflictKey.ConversationRef,
			PlatformUserRef: "peer-fact-conflict",
			PeerDisplayName: "合成候选人",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.TrackConversation(factConflictKey, "fixture", h.clock.Now()); err != nil {
		t.Fatal(err)
	}

	validPosition := "客户经理"
	noMatchPosition := "未配置职位"
	sessions := []protocol.ConversationSummary{
		inboundSummary("conversation-missing-identity", "", "合成候选人", &validPosition),
		inboundSummary("conversation-missing-position", "peer-missing-position", "合成候选人", nil),
		inboundSummary("conversation-no-match", "peer-no-match", "合成候选人", &noMatchPosition),
		inboundSummary("conversation-human-conflict", "peer-human-conflict", "合成候选人", &validPosition),
		inboundSummary(factConflictKey.ConversationRef, "peer-fact-conflict", "合成候选人", &validPosition),
		inboundSummary("conversation-valid-last", "peer-valid-last", "合成候选人", &validPosition),
	}
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{Sessions: sessions, Complete: true}, nil
		case protocol.PrimChatReadThread:
			args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
			text := "合成入站消息"
			peerRef := "peer-valid-last"
			if args.ConversationRef == factConflictKey.ConversationRef {
				peerRef = "peer-fact-conflict"
			}
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{{
					Idx: 0, Direction: protocol.MessageDirectionIn,
					Kind: protocol.MessageKindText, Text: &text,
					ContentHash: syncledger.HashText(text),
				}},
				Peer: &protocol.PeerSummary{
					PlatformUserRef: peerRef,
					DisplayName:     "合成候选人",
				},
				Complete: true, ReachedTop: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("局部失败不应阻断后续有效会话: result=%+v err=%v", result, err)
	}
	validKey := store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: "conversation-valid-last",
	}
	validProfile, err := h.db.CandidateProfileByConversation(validKey)
	if err != nil || validProfile == nil || validProfile.PositionRef != "job-valid" {
		t.Fatalf("前序失败阻断了后续有效建档: profile=%+v err=%v", validProfile, err)
	}
	for _, conversationRef := range []string{
		"conversation-missing-identity",
		"conversation-missing-position",
		"conversation-no-match",
		"conversation-human-conflict",
		factConflictKey.ConversationRef,
	} {
		profile, queryErr := h.db.CandidateProfileByConversation(store.ConversationKey{
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			ConversationRef: conversationRef,
		})
		if queryErr != nil || profile != nil {
			t.Fatalf("保守分支意外建档: conversation=%s profile=%+v err=%v",
				conversationRef, profile, queryErr)
		}
	}
	expected := map[string]string{
		"conversation-missing-identity": "status=skipped reason=missingPlatformUserRef",
		"conversation-missing-position": "status=skipped reason=missingPositionTitle",
		"conversation-no-match":         "status=skipped reason=positionNoMatch",
		"conversation-human-conflict":   "status=manualRequired reason=humanProfileConflict",
		factConflictKey.ConversationRef: "status=manualRequired reason=identityFactConflict",
		validKey.ConversationRef:        "status=adopted",
	}
	audits, err := h.db.AuditEntries(100)
	if err != nil {
		t.Fatal(err)
	}
	for conversationRef, detail := range expected {
		found := false
		for _, audit := range audits {
			if audit.Category == inboundProfileAdoptionAuditCategory &&
				audit.ConversationRef == conversationRef &&
				audit.Detail == detail {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("缺少局部收编审计: conversation=%s detail=%s audits=%+v",
				conversationRef, detail, audits)
		}
	}
	for _, audit := range audits {
		if audit.Category != inboundProfileAdoptionAuditCategory {
			continue
		}
		for _, secret := range []string{
			"peer-", "合成候选人", "客户经理", "未配置职位",
		} {
			if strings.Contains(audit.Detail, secret) {
				t.Fatalf("收编审计泄露页面身份或职位明文: %+v", audit)
			}
		}
	}
}

func savePatrolInboundLegacyJob(
	t *testing.T,
	h *harness,
	jobID string,
	positionTitle string,
) {
	t.Helper()
	documents := []m5ai.JobConfigDocument{
		{DocType: "多轮沟通", Content: "回复={简历}/{推荐时段}/{对话历史}"},
		{DocType: "意向判断", Content: "判断={回复}/{招呼语}"},
		{DocType: "客户事实库", Content: "合成事实"},
	}
	sort.Slice(documents, func(i, j int) bool {
		return documents[i].DocType < documents[j].DocType
	})
	revision := m5ai.ContextRevision{
		ContextID: "context-" + jobID, RevisionHash: "revision-" + jobID,
		SourceKind: "legacyJobConfig", SourceJobRef: jobID,
		DisplayName:   positionTitle,
		SourcePackage: m5ai.JobConfigDocumentPackage{Documents: documents},
		Communication: m5ai.CommunicationView{
			ReplyPrompt:   "回复={简历}/{推荐时段}/{对话历史}",
			IntentPrompt:  "判断={回复}/{招呼语}",
			CustomerFacts: "合成事实", MappingVersion: m5ai.MappingVersion,
		},
		CreatedAt: h.clock.Now(),
	}
	if _, err := h.db.SaveCurrentLegacyJobAIContext(
		[]m5ai.ContextRevision{revision},
		h.clock.Now(),
	); err != nil {
		t.Fatalf("保存合成旧后台职位 %s: %v", jobID, err)
	}
}

func inboundSummary(
	conversationRef string,
	platformUserRef string,
	displayName string,
	positionTitle *string,
) protocol.ConversationSummary {
	return protocol.ConversationSummary{
		ConversationRef: conversationRef,
		Peer: protocol.PeerSummary{
			PlatformUserRef: platformUserRef,
			DisplayName:     displayName,
		},
		PositionTitle: positionTitle,
		UnreadCount:   1,
		LastMessage: protocol.LastMessageSummary{
			Direction:   protocol.MessageDirectionIn,
			Kind:        protocol.MessageKindText,
			TextPreview: "合成入站消息",
		},
	}
}

func assertInboundAdoptionAudit(
	t *testing.T,
	h *harness,
	conversationRef string,
	detail string,
) {
	t.Helper()
	audits, err := h.db.AuditEntries(50)
	if err != nil {
		t.Fatal(err)
	}
	for _, audit := range audits {
		if audit.Category == inboundProfileAdoptionAuditCategory &&
			audit.ConversationRef == conversationRef &&
			audit.Detail == detail {
			return
		}
	}
	t.Fatalf("未找到主动来聊建档审计: conversation=%s detail=%s audits=%+v",
		conversationRef, detail, audits)
}
