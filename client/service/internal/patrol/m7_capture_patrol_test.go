package patrol

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// 约面成功入队后,巡检为该通知取证一轮(聊天+简历各一次)并落事实行;
// 标记先行,第二轮不再重复派发;截图失败只缺图不阻塞。
func TestCaptureNotificationEvidenceDispatchesOncePerNotification(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PendingInterviewTransition(t, h, "capture-once", "accepted")
	chatRef := "sha256:" + strings.Repeat("c", 64)
	resumeRef := "sha256:" + strings.Repeat("d", 64)
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatCaptureThreadScreenshot:
			return protocol.CaptureScreenshotData{
				ImageBlobRef: chatRef, ByteSize: 1234, Truncated: false,
				CapturedAt: h.clock.Now().UnixMilli(),
			}, nil
		case protocol.PrimCandidateCaptureResumeScreenshot:
			return protocol.CaptureScreenshotData{
				ImageBlobRef: resumeRef, ByteSize: 2345, Truncated: true,
				CapturedAt: h.clock.Now().UnixMilli(),
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	before, err := h.db.CommunicationV4AggregateByProfile(fixture.target.profileID)
	if err != nil {
		t.Fatal(err)
	}
	h.manager.mu.Lock()
	err = fixture.actor.processCommunicationV4CardTransition(
		context.Background(),
		fixture.pending,
		fixture.profile,
		*before,
	)
	h.manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	needing, err := h.db.NotificationsNeedingCapture(fixture.target.profileID)
	if err != nil || len(needing) != 1 ||
		needing[0].NotifyType != store.NotificationTypeInterviewAccepted {
		t.Fatalf("约面成功后应有一条待取证通知: %+v err=%v", needing, err)
	}

	h.manager.mu.Lock()
	err = fixture.actor.captureNotificationEvidence(context.Background(), fixture.target.profileID)
	h.manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	shots, err := h.db.LatestCandidateScreenshots(fixture.target.profileID)
	if err != nil ||
		shots[store.CandidateScreenshotKindChat].BlobRef != chatRef ||
		shots[store.CandidateScreenshotKindResume].BlobRef != resumeRef ||
		!shots[store.CandidateScreenshotKindResume].Truncated {
		t.Fatalf("截图事实行不符: %+v err=%v", shots, err)
	}
	if needing, _ := h.db.NotificationsNeedingCapture(fixture.target.profileID); len(needing) != 0 {
		t.Fatalf("取证派发后仍报待取证: %+v", needing)
	}
	if h.runner.count(protocol.PrimChatCaptureThreadScreenshot) != 1 ||
		h.runner.count(protocol.PrimCandidateCaptureResumeScreenshot) != 1 {
		t.Fatalf("取证派发次数不符: %v", h.runner.names())
	}

	h.manager.mu.Lock()
	err = fixture.actor.captureNotificationEvidence(context.Background(), fixture.target.profileID)
	h.manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if h.runner.count(protocol.PrimChatCaptureThreadScreenshot) != 1 ||
		h.runner.count(protocol.PrimCandidateCaptureResumeScreenshot) != 1 {
		t.Fatalf("已标记的通知被重复取证: %v", h.runner.names())
	}
}

// 截图原语失败:标记仍然落下(不重拍),不写截图行,也不让巡检失败。
func TestCaptureNotificationEvidenceDegradesOnFailure(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PendingInterviewTransition(t, h, "capture-degrade", "accepted")
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatCaptureThreadScreenshot,
			protocol.PrimCandidateCaptureResumeScreenshot:
			return nil, errors.New("标签页不在前台")
		default:
			return defaultHandler(request)
		}
	}
	before, err := h.db.CommunicationV4AggregateByProfile(fixture.target.profileID)
	if err != nil {
		t.Fatal(err)
	}
	h.manager.mu.Lock()
	err = fixture.actor.processCommunicationV4CardTransition(
		context.Background(),
		fixture.pending,
		fixture.profile,
		*before,
	)
	h.manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	h.manager.mu.Lock()
	err = fixture.actor.captureNotificationEvidence(context.Background(), fixture.target.profileID)
	h.manager.mu.Unlock()
	if err != nil {
		t.Fatalf("截图失败不得让巡检失败: %v", err)
	}
	shots, err := h.db.LatestCandidateScreenshots(fixture.target.profileID)
	if err != nil || len(shots) != 0 {
		t.Fatalf("失败不得留下截图行: %+v err=%v", shots, err)
	}
	if needing, _ := h.db.NotificationsNeedingCapture(fixture.target.profileID); len(needing) != 0 {
		t.Fatalf("失败后不得再次取证(缺图降级): %+v", needing)
	}
}

// 该平台没有电话原语(2026-09-02 甲方裁决 2.2):不算失败、不重试,留审计行;
// 取证照常完成(不重拍),通知照常少一行。两种信号(手侧 PROTO_UNSUPPORTED_CMD 与脑闸哨兵)同款。
func TestCaptureNotificationEvidenceSkipsPhoneWhenCapabilityMissing(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"hand rejects at runtime", &RunError{Code: protocol.ErrCodeProtoUnsupportedCmd, Retryable: protocol.RetryableNo}},
		{"brain gate sentinel", ErrHandCapabilityMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			fixture := seedCommunicationV4PendingInterviewTransition(t, h, "capture-nophone", "accepted")
			phoneCalls := 0
			h.runner.handler = func(request RunRequest) (any, error) {
				switch request.Name {
				case protocol.PrimChatCaptureThreadScreenshot, protocol.PrimCandidateCaptureResumeScreenshot:
					return protocol.CaptureScreenshotData{
						ImageBlobRef: "sha256:" + strings.Repeat("e", 64), ByteSize: 10, Truncated: false,
						CapturedAt: h.clock.Now().UnixMilli(),
					}, nil
				case protocol.PrimChatReadPeerPhone, protocol.PrimChatRevealPeerPhone:
					phoneCalls++
					return nil, tc.err
				default:
					return defaultHandler(request)
				}
			}
			before, err := h.db.CommunicationV4AggregateByProfile(fixture.target.profileID)
			if err != nil {
				t.Fatal(err)
			}
			h.manager.mu.Lock()
			err = fixture.actor.processCommunicationV4CardTransition(context.Background(), fixture.pending, fixture.profile, *before)
			h.manager.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			h.manager.mu.Lock()
			err = fixture.actor.captureNotificationEvidence(context.Background(), fixture.target.profileID)
			h.manager.mu.Unlock()
			if err != nil {
				t.Fatalf("能力缺失不得让巡检失败: %v", err)
			}
			if phoneCalls != 1 {
				t.Fatalf("readPeerPhone 只派一次、不重试、不进 reveal: %d", phoneCalls)
			}
			if needing, _ := h.db.NotificationsNeedingCapture(fixture.target.profileID); len(needing) != 0 {
				t.Fatalf("取证应照常完成、不重拍: %+v", needing)
			}
			entries, _ := h.db.AuditEntries(50)
			found := false
			for _, entry := range entries {
				if entry.Category == "peer_phone_capability_skipped" && strings.Contains(entry.Detail, "chat.readPeerPhone@1") {
					found = true
				}
			}
			if !found {
				t.Fatalf("跳过必须留审计行: %+v", entries)
			}
		})
	}
}

// 虚拟号形态(2026-09-09 甲方裁决):取证顺访读到 phoneKind=virtual 时收编为虚拟号观察
// 行,招聘方主叫号与失效时刻随行,不派 reveal(虚拟号形态没有「查看电话」按钮,手也不
// 报 masked);面板姓名首字核对同款适用;通知快照据此带出虚拟号附属事实。
func TestCaptureNotificationEvidenceSavesVirtualPhone(t *testing.T) {
	h := newHarness(t)
	fixture := seedCommunicationV4PendingInterviewTransition(t, h, "capture-vphone", "accepted")
	conversation, err := h.db.ConversationByKey(store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef, ConversationRef: fixture.target.conversationRef,
	})
	if err != nil || conversation == nil || strings.TrimSpace(conversation.PeerDisplayName) == "" {
		t.Fatalf("夹具会话缺对方展示名,首字核对无从谈起: %+v err=%v", conversation, err)
	}
	panelName := string([]rune(strings.TrimSpace(conversation.PeerDisplayName))[:1]) + "先生"
	expires := h.clock.Now().Add(48 * time.Hour).UnixMilli()
	phoneCalls := 0
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatCaptureThreadScreenshot, protocol.PrimCandidateCaptureResumeScreenshot:
			return protocol.CaptureScreenshotData{
				ImageBlobRef: "sha256:" + strings.Repeat("e", 64), ByteSize: 10, Truncated: false,
				CapturedAt: h.clock.Now().UnixMilli(),
			}, nil
		case protocol.PrimChatReadPeerPhone:
			phoneCalls++
			return protocol.ChatReadPeerPhoneData{
				Phone: "18000000001", PhoneKind: protocol.PeerPhoneKindVirtual,
				VirtualCaller: "139****0000", VirtualExpiresAt: expires,
				PanelName: panelName, ObservedAt: h.clock.Now().UnixMilli(),
			}, nil
		case protocol.PrimChatRevealPeerPhone:
			t.Fatal("虚拟号形态不得派 chat.revealPeerPhone")
			return nil, nil
		default:
			return defaultHandler(request)
		}
	}
	before, err := h.db.CommunicationV4AggregateByProfile(fixture.target.profileID)
	if err != nil {
		t.Fatal(err)
	}
	h.manager.mu.Lock()
	err = fixture.actor.processCommunicationV4CardTransition(context.Background(), fixture.pending, fixture.profile, *before)
	h.manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	h.manager.mu.Lock()
	err = fixture.actor.captureNotificationEvidence(context.Background(), fixture.target.profileID)
	h.manager.mu.Unlock()
	if err != nil {
		t.Fatalf("取证不得失败: %v", err)
	}
	if phoneCalls != 1 {
		t.Fatalf("readPeerPhone 只派一次: %d", phoneCalls)
	}
	row, err := h.db.LatestCandidatePhoneObservation(fixture.target.profileID)
	if err != nil || row == nil || row.Phone != "18000000001" || row.Kind != store.CandidatePhoneKindVirtual ||
		row.VirtualCaller != "139****0000" || row.VirtualExpiresAtMs != expires {
		t.Fatalf("虚拟号观察行未按裁决落库: %+v err=%v", row, err)
	}
	snapshot, err := h.db.NotificationRenderSnapshotForProfile(fixture.target.profileID)
	if err != nil || snapshot == nil || snapshot.PhoneNumber != "18000000001" ||
		snapshot.PhoneKind != store.CandidatePhoneKindVirtual || snapshot.PhoneVirtualCaller != "139****0000" ||
		snapshot.PhoneVirtualExpiresAtMs != expires {
		t.Fatalf("通知快照未带虚拟号附属事实: %+v err=%v", snapshot, err)
	}
	if needing, _ := h.db.NotificationsNeedingCapture(fixture.target.profileID); len(needing) != 0 {
		t.Fatalf("取证应照常完成: %+v", needing)
	}
}
