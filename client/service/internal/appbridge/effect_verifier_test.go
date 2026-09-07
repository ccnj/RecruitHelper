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
	observation, err := classifyVerifiedSend(window, "target", nil, dispatchedAt)
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
	observation, err := classifyVerifiedSend(window, "target", nil, dispatchedAt)
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
	observation, err := classifyVerifiedSend(within, "target", nil, dispatchedAt)
	if err != nil || !observation.Confirmed {
		t.Fatalf("容差边界(-5s 整)应命中: observation=%+v err=%v", observation, err)
	}
	outside := []protocol.ThreadMessage{
		timedThreadMessage(protocol.MessageDirectionOut, "target", dispatchedAt-verificationClockToleranceMs-1),
	}
	observation, err = classifyVerifiedSend(outside, "target", nil, dispatchedAt)
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
			observation, err := classifyVerifiedSend(test.window, "target", nil, dispatchedAt)
			if err != nil || observation.Confirmed || observation.Reason == "" {
				t.Fatalf("阴性只能未确认: observation=%+v err=%v", observation, err)
			}
		})
	}
}

// 拒收通知判失败(2026-08-11 甲方裁决)的时钟判据:不早于派发减 5 秒容差才算
// 新鲜;化石通知与字段缺失(含手侧 DOM 降级)一律返回 nil,走原有匹配/miss 路径。
func TestDeliveryRejectedObservationFreshnessQuadrants(t *testing.T) {
	const dispatched = int64(1_700_000_000_000)
	ptr := func(v int64) *int64 { return &v }
	cases := []struct {
		name     string
		ts       *int64
		rejected bool
	}{
		{"字段缺失", nil, false},
		{"零值", ptr(0), false},
		{"化石(容差外)", ptr(dispatched - verificationClockToleranceMs - 1), false},
		{"容差边界内", ptr(dispatched - verificationClockToleranceMs), true},
		{"派发后", ptr(dispatched + 1), true},
	}
	for _, tc := range cases {
		observation := deliveryRejectedObservation(
			protocol.ChatReadThreadData{RejectNoticeTs: tc.ts}, dispatched,
		)
		if (observation != nil) != tc.rejected {
			t.Fatalf("%s: rejected=%v 期望 %v", tc.name, observation != nil, tc.rejected)
		}
		if observation != nil &&
			(*observation.DeliveryRejectedTs != *tc.ts || observation.ObservedAt != *tc.ts) {
			t.Fatalf("%s: 时间戳未原样携带: %+v", tc.name, observation)
		}
	}
}

// 实发正文即事实(2026-09-07,《协议规格-v1》§9.4.1):窗口认行不再要求 hash 等于计划指纹。
// 带稳定 sourceKey、不属账本既有行、时间容差内的我方 out/text 行即本次,正文与 hash 以该行为准。
func TestClassifyVerifiedSendAcceptsDifferentTextByWindow(t *testing.T) {
	dispatchedAt := time.Now().UnixMilli()
	fresh := dispatchedAt + 1_200
	typo := timedThreadMessage(protocol.MessageDirectionOut, "typo-hash", fresh)
	typo.SourceKey = strings.Repeat("5", 64)
	observation, err := classifyVerifiedSend([]protocol.ThreadMessage{typo}, "target", nil, dispatchedAt)
	if err != nil || !observation.Confirmed || observation.ContentHash != "typo-hash" ||
		observation.SentText != "typo-hash" || observation.SourceKey != strings.Repeat("5", 64) {
		t.Fatalf("窗口内账本之外的我方文本行应按实发正文确认: observation=%+v err=%v", observation, err)
	}
}

// 多气泡链:上一条气泡的行也落在派发窗口里,但它的 sourceKey 已在账本,不得被认成本次。
func TestClassifyVerifiedSendSkipsLedgerKnownRows(t *testing.T) {
	dispatchedAt := time.Now().UnixMilli()
	previous := timedThreadMessage(protocol.MessageDirectionOut, "bubble-1", dispatchedAt+300)
	previous.SourceKey = strings.Repeat("6", 64)
	known := map[string]struct{}{previous.SourceKey: {}}
	observation, err := classifyVerifiedSend([]protocol.ThreadMessage{previous}, "target", known, dispatchedAt)
	if err != nil || observation.Confirmed {
		t.Fatalf("账本已知身份的行不得当本次: observation=%+v err=%v", observation, err)
	}
	current := timedThreadMessage(protocol.MessageDirectionOut, "bubble-2", dispatchedAt+2_000)
	current.SourceKey = strings.Repeat("7", 64)
	observation, err = classifyVerifiedSend([]protocol.ThreadMessage{previous, current}, "target", known, dispatchedAt)
	if err != nil || !observation.Confirmed || observation.SourceKey != current.SourceKey ||
		observation.SentText != "bubble-2" {
		t.Fatalf("应跳过账本已知行、认账本之外的那条: observation=%+v err=%v", observation, err)
	}
}

// 没有稳定身份的行没法与账本比对,只在 hash 等于计划指纹时才认(旧口径兜底);
// 两条账本之外的新行并存时取时间最新的那条,即便更早那条恰好同文。
func TestClassifyVerifiedSendNoSourceKeyFallsBackToHashAndNewestWins(t *testing.T) {
	dispatchedAt := time.Now().UnixMilli()
	anonymousOther := timedThreadMessage(protocol.MessageDirectionOut, "other", dispatchedAt+500)
	observation, err := classifyVerifiedSend([]protocol.ThreadMessage{anonymousOther}, "target", nil, dispatchedAt)
	if err != nil || observation.Confirmed {
		t.Fatalf("无身份且 hash 不等的行不得认: observation=%+v err=%v", observation, err)
	}
	older := timedThreadMessage(protocol.MessageDirectionOut, "target", dispatchedAt+500)
	older.SourceKey = strings.Repeat("8", 64)
	newest := timedThreadMessage(protocol.MessageDirectionOut, "typo", dispatchedAt+1_500)
	newest.SourceKey = strings.Repeat("9", 64)
	observation, err = classifyVerifiedSend([]protocol.ThreadMessage{older, newest}, "target", nil, dispatchedAt)
	if err != nil || !observation.Confirmed || observation.SourceKey != newest.SourceKey || observation.ContentHash != "typo" {
		t.Fatalf("账本之外多条取最新: observation=%+v err=%v", observation, err)
	}
}
