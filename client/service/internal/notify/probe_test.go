package notify

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
)

// 彩排必须一行都不写发件箱:event_key 是唯一索引且入队时撞了就静默跳过,
// 彩排只要占掉一个 key,日后那条真事件入队会被悄悄吃掉、线上永远少发。
func TestSendProbeNeverTouchesOutbox(t *testing.T) {
	capture := &wecomCapture{}
	server := newWecomServer(t, capture)
	defer server.Close()
	ledger := &fakeLedger{
		rows:     []store.NotificationOutbox{},
		snapshot: map[string]*store.NotificationRenderSnapshot{},
		meeting:  map[string]*store.NotificationOutbox{},
	}
	runner := newTestRunner(ledger, &fakeBlobs{}, server.URL, time.Now())

	outcome, err := runner.SendProbe(ProbeRequest{
		NotifyType:  store.NotificationTypeWechatAdded,
		Snapshot:    fullSnapshot(),
		ChatImage:   jpegBytes(),
		ResumeImage: jpegBytes(),
	})
	if err != nil {
		t.Fatalf("彩排应发送成功: %v", err)
	}
	if len(ledger.sent)+len(ledger.failed)+len(ledger.skipped) != 0 {
		t.Fatalf("彩排写了发件箱: sent=%v failed=%v skipped=%v", ledger.sent, ledger.failed, ledger.skipped)
	}
	if len(ledger.rows) != 0 {
		t.Fatalf("彩排新增了发件箱行: %+v", ledger.rows)
	}
	// 正文不带任何测试标记(2026-08-06 甲方明示):与线上通知逐字一致,
	// 首行标题尤其不得被改写成【测试·微信互加】之类。
	if outcome.Content != renderWechatAdded(fullSnapshot(), "客户丙", false) {
		t.Fatalf("彩排正文与线上渲染不一致:\n%s", outcome.Content)
	}
	if len(capture.kinds) != 3 || capture.kinds[0] != "text" {
		t.Fatalf("应先文本后两张图: %v", capture.kinds)
	}
	if !outcome.Chat.Sent || !outcome.Resume.Sent {
		t.Fatalf("两张图都该发出: %+v %+v", outcome.Chat, outcome.Resume)
	}
}

// 截图提示行必须按"本次实际拍到什么"写:快照里的 ChatShot/ResumeShot 是库里的
// 历史取证行,拿它渲染会让正文宣称有简历图、实际却只追发了聊天图。
func TestSendProbeHintFollowsThisRunNotLedgerHistory(t *testing.T) {
	capture := &wecomCapture{}
	server := newWecomServer(t, capture)
	defer server.Close()
	ledger := &fakeLedger{snapshot: map[string]*store.NotificationRenderSnapshot{}}
	runner := newTestRunner(ledger, &fakeBlobs{}, server.URL, time.Now())

	snapshot := fullSnapshot() // 库里两张图都在
	outcome, err := runner.SendProbe(ProbeRequest{
		NotifyType: store.NotificationTypeInterviewAccepted,
		Snapshot:   snapshot,
		ChatImage:  jpegBytes(), // 本次只拍到聊天
	})
	if err != nil {
		t.Fatalf("彩排应发送成功: %v", err)
	}
	if !strings.Contains(outcome.Content, "聊天记录见下图") ||
		strings.Contains(outcome.Content, "简历") {
		t.Fatalf("提示行未跟随本次实拍:\n%s", outcome.Content)
	}
	if len(capture.images) != 1 {
		t.Fatalf("应只追发一张图: %d", len(capture.images))
	}
	if outcome.Resume.Present {
		t.Fatalf("简历本次未拍到,不该标 present: %+v", outcome.Resume)
	}
	// 调用方的快照对象不得被就地改写——它可能还要用于别处。
	if snapshot.ResumeShot == nil {
		t.Fatal("彩排改写了调用方的快照")
	}
}

// 图片超限只降级,不影响已发出的文本主通知(与正式路径同款)。
func TestSendProbeOversizeImageDegrades(t *testing.T) {
	capture := &wecomCapture{}
	server := newWecomServer(t, capture)
	defer server.Close()
	runner := newTestRunner(&fakeLedger{}, &fakeBlobs{}, server.URL, time.Now())

	oversize := append(jpegBytes(), bytes.Repeat([]byte("x"), wecomImageMaxBytes)...)
	outcome, err := runner.SendProbe(ProbeRequest{
		NotifyType: store.NotificationTypeWechatAdded,
		Snapshot:   fullSnapshot(),
		ChatImage:  oversize,
	})
	if err != nil {
		t.Fatalf("图超限不该让整体失败: %v", err)
	}
	if outcome.Chat.Sent || !strings.HasPrefix(outcome.Chat.Skipped, "image_too_large") {
		t.Fatalf("超限图应记 skipped: %+v", outcome.Chat)
	}
	if len(capture.texts) != 1 || len(capture.images) != 0 {
		t.Fatalf("文本应已发出且未发图: texts=%d images=%d", len(capture.texts), len(capture.images))
	}
}

// 文本发不出去就是整体失败,不再追发图——只有图没有正文,运营看不懂。
func TestSendProbeTextFailureStopsImages(t *testing.T) {
	capture := &wecomCapture{fail: true}
	server := newWecomServer(t, capture)
	defer server.Close()
	runner := newTestRunner(&fakeLedger{}, &fakeBlobs{}, server.URL, time.Now())

	_, err := runner.SendProbe(ProbeRequest{
		NotifyType: store.NotificationTypeWechatAdded,
		Snapshot:   fullSnapshot(),
		ChatImage:  jpegBytes(),
	})
	if err == nil {
		t.Fatal("文本失败应返回错误")
	}
	if len(capture.images) != 0 {
		t.Fatalf("文本失败后不该追发图: %d", len(capture.images))
	}
}

func TestSendProbeRejectsUnknownType(t *testing.T) {
	runner := newTestRunner(&fakeLedger{}, &fakeBlobs{}, "http://127.0.0.1:1", time.Now())
	if _, err := runner.SendProbe(ProbeRequest{
		NotifyType: "wecomSomethingElse",
		Snapshot:   fullSnapshot(),
	}); err == nil {
		t.Fatal("未知通知类型应被拒绝")
	}
	if _, err := runner.SendProbe(ProbeRequest{
		NotifyType: store.NotificationTypeWechatAdded,
	}); err == nil {
		t.Fatal("缺快照应被拒绝")
	}
}
