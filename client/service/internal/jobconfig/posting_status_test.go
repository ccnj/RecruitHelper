package jobconfig

import (
	"testing"

	"recruithelper/contract/gen/go/protocol"
)

func TestFindPostingStatus(t *testing.T) {
	sections := []protocol.JobPostingSection{
		{Label: "在线中", Names: []string{"销售顾问", "客服专员（电话）"}},
		{Label: "未上线", Names: []string{"保障顾问"}},
		{Label: "审核中", Names: []string{"销售顾问"}},
		{Label: "未过审", Names: []string{}},
	}
	cases := []struct {
		name    string
		jobName string
		want    string
	}{
		{"在线职位", "销售顾问", "在线中"},
		{"归一化匹配(全角括号与空白)", "客服专员 (电话)", "在线中"},
		{"未上线职位", "保障顾问", "未上线"},
		{"平台未见", "不存在的职位", PostingStatusNotFound},
		{"空职位名", "  ", PostingStatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FindPostingStatus(c.jobName, sections); got != c.want {
				t.Fatalf("FindPostingStatus(%q)=%q, want %q", c.jobName, got, c.want)
			}
		})
	}
	// 同名职位落在多个分区时按分区顺序取第一个:上面「销售顾问」同时在
	// 在线中与审核中,判在线——与"第一个命中"语义一致。
	if got := FindPostingStatus("销售顾问", sections); got != PostingStatusLabelOnline {
		t.Fatalf("多分区同名应取第一个命中分区,got %q", got)
	}
}
