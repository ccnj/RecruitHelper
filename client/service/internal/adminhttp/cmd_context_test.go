package adminhttp

import (
	"testing"
	"time"
)

// argsFacts 是通用解析而非逐原语分支，因此必须在"字段缺失、类型不对、
// 根本不是 JSON"时安静降级——suspect 队列不能因为某条命令的 args 长得不同
// 就整列渲染不出来。
func TestArgsFactsDegradesInsteadOfFailing(t *testing.T) {
	cases := []struct {
		name     string
		args     string
		wantConv string
		wantSum  string
	}{
		{"空串", "", "", ""},
		{"非 JSON", "not json at all", "", ""},
		{"JSON 数组不是对象", `["a"]`, "", ""},
		{"发消息取会话与正文", `{"conversationRef":"c-1","text":"你好"}`, "c-1", "你好"},
		{"发布职位取职位名", `{"jobName":"理财顾问"}`, "", "理财顾问"},
		{"只有会话无正文", `{"conversationRef":"c-2"}`, "c-2", ""},
		{"正文为空串不算摘要", `{"conversationRef":"c-3","text":""}`, "c-3", ""},
		{"字段类型不对时不 panic", `{"conversationRef":42,"text":{"a":1}}`, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			facts := argsFacts(c.args)
			if facts.ConversationRef != c.wantConv || facts.Summary() != c.wantSum {
				t.Fatalf("argsFacts(%q) = (%q,%q), 期望 (%q,%q)",
					c.args, facts.ConversationRef, facts.Summary(), c.wantConv, c.wantSum)
			}
		})
	}
}

// SuspectReason 是各处自由拼装的字符串，不是封闭枚举。翻译只能是"认识的
// 翻掉、不认识的原样透出"——硬套映射会把没覆盖到的原因显示成空白，那比
// 显示英文行话更糟：裁决的人会以为系统没记原因。
func TestHumanizeSuspectReasonPassesUnknownThrough(t *testing.T) {
	cases := []struct{ in, want string }{
		{"verification exhausted: 未找到 expectedTail", "发后核验做完仍无法确认：回读会话最近窗口没找到这条消息"},
		{"verification exhausted: 最近窗口未见时间容差内的目标 out/text 指纹", "发后核验做完仍无法确认：最近窗口未见时间容差内的目标 out/text 指纹"},
		{"结构化验证器未接线，禁止在未知副作用下继续", "结构化验证器未接线，禁止在未知副作用下继续"},
		{"", ""},
	}
	for _, c := range cases {
		if got := humanizeSuspectReason(c.in); got != c.want {
			t.Fatalf("humanizeSuspectReason(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

func TestSuspectActionNameFallsBackToPrimitive(t *testing.T) {
	if got := suspectActionName("chat.sendMessage"); got != "发消息" {
		t.Fatalf("已知原语应有中文名，得到 %q", got)
	}
	// 新原语没登记就照原名显示，不猜也不留空。
	if got := suspectActionName("chat.somethingNew"); got != "chat.somethingNew" {
		t.Fatalf("未登记原语应回退原名，得到 %q", got)
	}
}

// 零值时间的 UnixMilli 是个很大的负数，直接送前端会显示成上古日期。
func TestUnixMilliOrZero(t *testing.T) {
	if got := unixMilliOrZero(time.Time{}); got != 0 {
		t.Fatalf("零值时间应回 0，得到 %d", got)
	}
	at := time.UnixMilli(1_700_000_000_000)
	if got := unixMilliOrZero(at); got != 1_700_000_000_000 {
		t.Fatalf("非零时间应原样换算，得到 %d", got)
	}
}
