package patrol

import (
	"context"
	"errors"
	"strings"
	"testing"

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
