package appbridge

import (
	"strings"
	"testing"
	"time"

	"recruithelper/contract/gen/go/protocol"
)

func timedThreadMessage(direction protocol.MessageDirection, hash string, tsMs int64) protocol.ThreadMessage {
	text := hash
	message := protocol.ThreadMessage{
		Direction: direction, Kind: protocol.MessageKindText, ContentHash: hash, Text: &text,
	}
	if tsMs > 0 {
		ts := tsMs
		message.TsApprox = &ts
	}
	return message
}

func TestClassifyVerifiedSendConfirmsPageVisibleTarget(t *testing.T) {
	dispatchedAt := time.Now().UnixMilli()
	fresh := dispatchedAt + 1_500
	window := []protocol.ThreadMessage{
		timedThreadMessage(protocol.MessageDirectionIn, "a", dispatchedAt-600_000),
		// 历史同文在时间容差外,不得被认领为本次
		timedThreadMessage(protocol.MessageDirectionOut, "target", dispatchedAt-600_000),
		timedThreadMessage(protocol.MessageDirectionIn, "other", dispatchedAt-300_000),
		timedThreadMessage(protocol.MessageDirectionOut, "target", fresh),
	}
	window[1].SourceKey = strings.Repeat("1", 64)
	window[3].SourceKey = strings.Repeat("2", 64)
	observation, err := classifyVerifiedSend(window, "target", dispatchedAt)
	if err != nil || !observation.Confirmed || observation.ContentHash != "target" ||
		observation.ObservedAt != fresh || observation.SourceKey != strings.Repeat("2", 64) {
		t.Fatalf("容差内页面可见目标应确认且取新、并采其 sourceKey: observation=%+v err=%v", observation, err)
	}
}

func TestClassifyVerifiedSendSameTextTakesNewest(t *testing.T) {
	dispatchedAt := time.Now().UnixMilli()
	older := dispatchedAt + 500
	newest := dispatchedAt + 2_000
	window := []protocol.ThreadMessage{
		timedThreadMessage(protocol.MessageDirectionOut, "target", older),
		timedThreadMessage(protocol.MessageDirectionOut, "target", newest),
	}
	window[0].SourceKey = strings.Repeat("3", 64)
	window[1].SourceKey = strings.Repeat("4", 64)
	observation, err := classifyVerifiedSend(window, "target", dispatchedAt)
	if err != nil || !observation.Confirmed || observation.ObservedAt != newest ||
		observation.SourceKey != strings.Repeat("4", 64) {
		// 同文歧义挂最新行的身份是战役出口第 3 项知情接受的兜底,这里钉住现状。
		t.Fatalf("同文多条应取满足条件的最新一条并采其 sourceKey: observation=%+v err=%v", observation, err)
	}
}

func TestClassifyVerifiedSendClockToleranceBoundary(t *testing.T) {
	dispatchedAt := time.Now().UnixMilli()
	within := []protocol.ThreadMessage{
		timedThreadMessage(protocol.MessageDirectionOut, "target", dispatchedAt-verificationClockToleranceMs),
	}
	observation, err := classifyVerifiedSend(within, "target", dispatchedAt)
	if err != nil || !observation.Confirmed {
		t.Fatalf("容差边界(-5s 整)应命中: observation=%+v err=%v", observation, err)
	}
	outside := []protocol.ThreadMessage{
		timedThreadMessage(protocol.MessageDirectionOut, "target", dispatchedAt-verificationClockToleranceMs-1),
	}
	observation, err = classifyVerifiedSend(outside, "target", dispatchedAt)
	if err != nil || observation.Confirmed || observation.Reason == "" {
		t.Fatalf("容差外只能未确认: observation=%+v err=%v", observation, err)
	}
}

func TestClassifyVerifiedSendNegativesStayUnconfirmed(t *testing.T) {
	dispatchedAt := time.Now().UnixMilli()
	fresh := dispatchedAt + 1_000
	inbound := timedThreadMessage(protocol.MessageDirectionIn, "target", fresh)
	wrongHash := timedThreadMessage(protocol.MessageDirectionOut, "other", fresh)
	missingTs := timedThreadMessage(protocol.MessageDirectionOut, "target", 0)
	cardKind := timedThreadMessage(protocol.MessageDirectionOut, "target", fresh)
	cardKind.Kind = protocol.MessageKindCard
	cases := []struct {
		name   string
		window []protocol.ThreadMessage
	}{
		// 真机 2026-07-28:平台历史接口在 IM 页刚导航后的同步窗口内可能对
		// 非空会话返回空成功。空验证窗口只能落"未确认",不得当"未发生"负证。
		{name: "empty window", window: nil},
		{name: "inbound direction", window: []protocol.ThreadMessage{inbound}},
		{name: "wrong hash", window: []protocol.ThreadMessage{wrongHash}},
		{name: "missing ts", window: []protocol.ThreadMessage{missingTs}},
		{name: "card kind", window: []protocol.ThreadMessage{cardKind}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			observation, err := classifyVerifiedSend(test.window, "target", dispatchedAt)
			if err != nil || observation.Confirmed || observation.Reason == "" {
				t.Fatalf("阴性只能未确认: observation=%+v err=%v", observation, err)
			}
		})
	}
}
