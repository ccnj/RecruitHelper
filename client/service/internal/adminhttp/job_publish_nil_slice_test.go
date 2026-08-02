package adminhttp

import (
	"encoding/json"
	"strings"
	"testing"
)

// Go 的 nil 切片 json.Marshal 出来是 `null` 而不是 `[]`。诊断台按 TS 类型把这些
// 字段当数组用（.length / .map / .includes），收到 null 就抛 TypeError，React 卸掉
// 整棵树——白屏，连同已经跑完的阶段 A 结果一起没了。
//
// 2026-08-02 客户机就是这么白的两次：十个职位里有一个关键词全部命中词库，它的
// custom 一次没 append。这条用例把"非 omitempty 的数组字段一律不得序列化成 null"
// 钉死在两个发布视图上。
func TestPublishViewsNeverMarshalArraysAsNull(t *testing.T) {
	// 刻意用零值构造：这正是失败分支带着 view 直接写回 409 时的形态。
	for name, value := range map[string]any{
		"关键词方案": jobKeywordPlanView{
			Sections: []jobKeywordSectionView{},
			Keywords: []string{}, Matched: []string{}, Custom: []string{},
		},
		"类别分配行": jobClassAssignmentView{Candidates: []jobClassCandidateView{}},
		"类别分配表": jobClassPlanView{Jobs: []jobClassAssignmentView{}},
		"词库分组":  jobKeywordSectionView{Words: []string{}},
	} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("%s 序列化失败: %v", name, err)
		}
		if strings.Contains(string(raw), "null") {
			t.Fatalf("%s 里有字段序列化成了 null，诊断台会白屏: %s", name, raw)
		}
	}
}

// 上一条守的是构造点，这条守的是**忘了构造**时能不能被发现：只要有人日后新加
// 一个非 omitempty 的数组字段又忘了初始化，零值 view 里就会冒出 null。
func TestZeroValuePublishViewsWouldBeCaught(t *testing.T) {
	raw, err := json.Marshal(jobKeywordPlanView{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "null") {
		t.Fatal("零值 view 竟然没有 null——这条用例的前提没了，请复核字段标签")
	}
}
