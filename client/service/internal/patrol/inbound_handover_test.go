package patrol

import (
	"context"
	"errors"
	"testing"
	"time"

	"recruithelper/client/service/internal/store"
	"recruithelper/client/service/internal/syncledger"
	"recruithelper/contract/gen/go/protocol"
)

func TestParseInboundHandoverCutoff(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		value    string
		location *time.Location
		want     time.Time
	}{
		{
			name: "空值取默认交接日", value: "", location: time.UTC,
			want: time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "空白值同样取默认", value: "   ", location: time.UTC,
			want: time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "显式日期按本地时区取当日零点", value: "2026-08-03", location: shanghai,
			want: time.Date(2026, 8, 3, 0, 0, 0, 0, shanghai),
		},
		{
			name: "location 为空回落系统本地时区", value: "2026-08-03", location: nil,
			want: time.Date(2026, 8, 3, 0, 0, 0, 0, time.Local),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseInboundHandoverCutoff(tc.value, tc.location)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("交接日不符: got=%s want=%s", got, tc.want)
			}
		})
	}
}

// 非法值必须拒绝启动：静默回落到默认会放进一批本应挡住的交接前旧会话。
func TestParseInboundHandoverCutoffRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{
		"2026-8-1",            // 月/日未补零
		"2026-13-01",          // 月份越界
		"2026-02-30",          // 该月无此日
		"2026/07/29",          // 分隔符错误
		"20260729",            // 无分隔符
		"2026-07-29T00:00:00", // 带时间部分
		"今天",
		"abc",
	} {
		if _, err := ParseInboundHandoverCutoff(value, time.UTC); err == nil {
			t.Fatalf("非法交接日未被拒绝: %q", value)
		} else if !errors.Is(err, ErrInboundHandoverDateInvalid) {
			t.Fatalf("非法交接日错误类型不符: value=%q err=%v", value, err)
		}
	}
}

func TestInboundHandoverBlockedBoundary(t *testing.T) {
	cutoff := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	cutoffMs := cutoff.UnixMilli()
	ms := func(value int64) *int64 { return &value }

	for _, tc := range []struct {
		name string
		ts   *int64
		want bool
	}{
		{name: "时间戳缺失保守拦截", ts: nil, want: true},
		{name: "交接日前一毫秒拦截", ts: ms(cutoffMs - 1), want: true},
		{name: "正好交接日零点放行", ts: ms(cutoffMs), want: false},
		{name: "交接日之后放行", ts: ms(cutoffMs + 86_400_000), want: false},
		{name: "远早于交接日拦截", ts: ms(cutoffMs - 30*86_400_000), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := inboundHandoverBlocked(tc.ts, cutoff); got != tc.want {
				t.Fatalf("闸判定不符: got=%v want=%v", got, tc.want)
			}
		})
	}
}

// 交接前的会话与"陌生候选人主动来聊"在列表上无法区分，因此必须在建档这一步
// 就挡住：建档一旦发生，采简历、建 V4 根与 AI 自动回复整条链都会自动跑完。
func TestInboundAdoptionSkipsConversationsBeforeHandover(t *testing.T) {
	h := newHarness(t)
	savePatrolInboundLegacyJob(t, h, "job-handover", "客户经理")

	position := "客户经理"
	beforeHandoverMs := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC).UnixMilli()

	legacy := inboundSummary("conversation-legacy", "peer-legacy", "合成候选人", &position)
	legacy.LastActivityTs = &beforeHandoverMs
	noTimestamp := inboundSummary("conversation-no-ts", "peer-no-ts", "合成候选人", &position)
	noTimestamp.LastActivityTs = nil
	fresh := inboundSummary("conversation-fresh", "peer-fresh", "合成候选人", &position)

	sessions := []protocol.ConversationSummary{legacy, noTimestamp, fresh}
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			return protocol.ChatReadListData{Sessions: sessions, Complete: true}, nil
		case protocol.PrimChatReadThread:
			args := decodeArgs[protocol.ChatReadThreadArgs](t, request)
			if args.ConversationRef != "conversation-fresh" {
				t.Fatalf("交接前会话不得被深读: %+v", args)
			}
			text := "合成入站消息"
			return protocol.ChatReadThreadData{
				Messages: []protocol.ThreadMessage{{
					Idx: 0, Direction: protocol.MessageDirectionIn,
					Kind: protocol.MessageKindText, Text: &text,
					ContentHash: syncledger.HashText(text),
				}},
				Peer: &protocol.PeerSummary{
					PlatformUserRef: "peer-fresh",
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
		t.Fatalf("巡检轮失败: result=%+v err=%v", result, err)
	}

	for _, conversationRef := range []string{"conversation-legacy", "conversation-no-ts"} {
		profile, err := h.db.CandidateProfileByConversation(store.ConversationKey{
			Platform: h.key.Platform, AccountRef: h.key.AccountRef,
			ConversationRef: conversationRef,
		})
		if err != nil {
			t.Fatal(err)
		}
		if profile != nil {
			t.Fatalf("交接前会话被建档: conversation=%s profile=%+v", conversationRef, profile)
		}
		assertInboundAdoptionAudit(t, h, conversationRef, "status=skipped reason=beforeHandover")
	}

	// 闸不得误伤交接之后的真实入站候选人。
	fresher, err := h.db.CandidateProfileByConversation(store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: "conversation-fresh",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fresher == nil {
		t.Fatal("交接之后的入站候选人未建档")
	}
}

// 闸只作用于尚无档案的会话：已建档候选人在闸之前就已返回，其既有处理不受影响。
func TestInboundHandoverGateLeavesExistingProfilesUntouched(t *testing.T) {
	h := newHarness(t)
	seedCommunicationV4PatrolTarget(t, h, "existing-before-handover", "已有入站")

	conversations, err := h.db.ConversationsForAccount(h.key)
	if err != nil {
		t.Fatal(err)
	}
	if len(conversations) != 1 {
		t.Fatalf("预置会话数不符: %+v", conversations)
	}
	conversationRef := conversations[0].ConversationRef
	profileBefore, err := h.db.CandidateProfileByConversation(store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: conversationRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	if profileBefore == nil {
		t.Fatal("预置候选人档案缺失")
	}

	beforeHandoverMs := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC).UnixMilli()
	existing := summary(conversationRef, profileBefore.PlatformUserRef, "已有入站", 0)
	existing.LastActivityTs = &beforeHandoverMs
	h.runner.handler = func(request RunRequest) (any, error) {
		if request.Name == protocol.PrimChatReadList {
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{existing}, Complete: true,
			}, nil
		}
		return defaultHandler(request)
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("巡检轮失败: result=%+v err=%v", result, err)
	}

	audits, err := h.db.AuditEntries(50)
	if err != nil {
		t.Fatal(err)
	}
	for _, audit := range audits {
		if audit.Category == inboundProfileAdoptionAuditCategory &&
			audit.ConversationRef == conversationRef {
			t.Fatalf("已建档会话被交接闸判定: %+v", audit)
		}
	}
	profileAfter, err := h.db.CandidateProfileByConversation(store.ConversationKey{
		Platform: h.key.Platform, AccountRef: h.key.AccountRef,
		ConversationRef: conversationRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 只断言档案身份仍在：本轮照常推进它的主线状态正是"不受闸影响"的表现，
	// 断言状态不变反而会把正常处理判成失败。
	if profileAfter == nil || profileAfter.ProfileID != profileBefore.ProfileID {
		t.Fatalf("已建档候选人档案身份受闸影响: before=%+v after=%+v",
			profileBefore, profileAfter)
	}
}

func TestListStopOlderThanDays(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	cutoff := time.Date(2026, 7, 29, 0, 0, 0, 0, shanghai)
	for _, tc := range []struct {
		name string
		now  time.Time
		want int
	}{
		{
			name: "交接日当天清晨只翻一天",
			now:  time.Date(2026, 7, 29, 0, 30, 0, 0, shanghai), want: 1,
		},
		{
			name: "交接日当天深夜仍是一天",
			now:  time.Date(2026, 7, 29, 23, 59, 59, 0, shanghai), want: 1,
		},
		{
			name: "交接次日翻两天",
			now:  time.Date(2026, 7, 30, 9, 0, 0, 0, shanghai), want: 2,
		},
		{
			name: "第七天翻七天",
			now:  time.Date(2026, 8, 4, 12, 0, 0, 0, shanghai), want: 7,
		},
		{
			name: "第八天到达上界",
			now:  time.Date(2026, 8, 5, 12, 0, 0, 0, shanghai), want: 8,
		},
		{
			name: "远超交接日固定在上界，范围不随时间增长",
			now:  time.Date(2026, 12, 31, 12, 0, 0, 0, shanghai), want: 8,
		},
		{
			name: "交接日被配置到未来时退回上界而非收窄",
			now:  time.Date(2026, 7, 20, 12, 0, 0, 0, shanghai), want: 8,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := listStopOlderThanDays(tc.now, cutoff, shanghai); got != tc.want {
				t.Fatalf("年龄截止不符: got=%d want=%d", got, tc.want)
			}
		})
	}
}

// 核心不变式：手端把参数解释为滚动的 days×24h，本函数推导出的截止必须永远
// 早于交接日 00:00。任何一处让它晚于交接日，都会让当天最早的一批会话在建档
// 闸放行之前就被年龄截止丢掉——那是静默漏人，且表里不留痕迹。
func TestListStopOlderThanDaysNeverCutsInsideHandoverDay(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	cutoff := time.Date(2026, 7, 29, 0, 0, 0, 0, shanghai)
	// 交接日起逐日推进，每天取多个时刻，覆盖上界生效前后两侧。
	for dayOffset := 0; dayOffset < 12; dayOffset++ {
		for _, hour := range []int{0, 1, 8, 12, 18, 23} {
			now := time.Date(2026, 7, 29+dayOffset, hour, 30, 0, 0, shanghai)
			days := listStopOlderThanDays(now, cutoff, shanghai)
			if days < listStopOlderThanDaysMin || days > listStopOlderThanDaysMax {
				t.Fatalf("越界: now=%s days=%d", now, days)
			}
			handCutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
			// 上界生效后（交接日已过去 8 天以上）手端截止本就应当晚于交接日，
			// 那是滚动窗口的既有语义，不适用本不变式。
			if days == listStopOlderThanDaysMax && now.Sub(cutoff) > listStopOlderThanDaysMax*24*time.Hour {
				continue
			}
			if !handCutoff.Before(cutoff) {
				t.Fatalf("手端年龄截止落进交接日之内: now=%s days=%d handCutoff=%s cutoff=%s",
					now, days, handCutoff, cutoff)
			}
		}
	}
}
