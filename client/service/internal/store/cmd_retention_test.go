package store

import (
	"testing"
	"time"
)

// 置空的边界与不变量:超期的正文清掉,窗口内的一字不动,而**行本身与审计骨架
// 永不删除**——「业务事实行禁止物理 DELETE」不因留存条款放宽。
func TestPurgeCmdResultBodiesKeepsRowsAndAuditSkeleton(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	now := time.Date(2026, 8, 7, 0, 5, 0, 0, time.Local)
	cutoff := now.Add(-CmdResultRetention)

	rows := []struct {
		msgID     string
		createdAt time.Time
		body      string
	}{
		{"m-old", now.Add(-72 * time.Hour), `{"status":"ok","data":{"sessions":[]}}`},
		{"m-just-outside", cutoff.Add(-time.Minute), `{"status":"ok","data":"边界外"}`},
		{"m-just-inside", cutoff.Add(time.Minute), `{"status":"ok","data":"边界内"}`},
		{"m-fresh", now.Add(-time.Hour), `{"status":"ok","data":"新鲜"}`},
	}
	for _, r := range rows {
		rec := &CmdRecord{
			MsgID: r.msgID, Name: "chat.readList", Class: "readonly",
			HandID: "hand-1", LogicalDispatchID: "ld-" + r.msgID,
			Status: CmdOk, Args: `{"scope":"list"}`, Guards: `{"route":"im"}`,
			ErrorCode: "", ResultBody: r.body,
			CreatedAt: r.createdAt, UpdatedAt: r.createdAt,
		}
		if err := s.db.Create(rec).Error; err != nil {
			t.Fatalf("建 %s: %v", r.msgID, err)
		}
	}

	cleared, err := s.PurgeCmdResultBodies(cutoff)
	if err != nil {
		t.Fatalf("PurgeCmdResultBodies: %v", err)
	}
	if cleared != 2 {
		t.Fatalf("应清 2 条(m-old 与 m-just-outside)，实清 %d", cleared)
	}

	var all []CmdRecord
	if err := s.db.Order("msg_id").Find(&all).Error; err != nil {
		t.Fatalf("回读: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("行永不删除，应仍有 4 行，实得 %d", len(all))
	}
	got := map[string]CmdRecord{}
	for _, r := range all {
		got[r.MsgID] = r
	}
	if got["m-old"].ResultBody != "" || got["m-just-outside"].ResultBody != "" {
		t.Fatal("超期行的 result_body 必须已置空")
	}
	if got["m-just-inside"].ResultBody == "" || got["m-fresh"].ResultBody == "" {
		t.Fatal("窗口内的 result_body 不得被动")
	}
	// 审计骨架:被清的那行除正文外一切照旧,否则事后连"这条命令是什么、成没成功"
	// 都答不上来,清理就从瘦身变成了毁证。
	old := got["m-old"]
	if old.Name != "chat.readList" || old.Args != `{"scope":"list"}` ||
		old.Guards != `{"route":"im"}` || old.Status != CmdOk ||
		old.LogicalDispatchID != "ld-m-old" {
		t.Fatalf("审计骨架被破坏: %+v", old)
	}
	// updated_at 不得被清理动作刷新——它记的是这条命令最后一次状态变更的时刻,
	// 覆盖掉就等于把维护动作伪装成业务事实。
	if !old.UpdatedAt.Equal(rows[0].createdAt) {
		t.Fatalf("updated_at 被清理污染: 期望 %v，实得 %v", rows[0].createdAt, old.UpdatedAt)
	}
}

// 重复执行必须幂等,且第二次报告清了 0 条 —— 装配层靠这个 0 决定跳过 VACUUM,
// 数错了就会每天空跑一次重写整库。
func TestPurgeCmdResultBodiesIdempotent(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	now := time.Date(2026, 8, 7, 0, 5, 0, 0, time.Local)
	cutoff := now.Add(-CmdResultRetention)
	rec := &CmdRecord{
		MsgID: "m-1", Name: "chat.readList", Class: "readonly",
		HandID: "hand-1", LogicalDispatchID: "ld-1", Status: CmdOk,
		ResultBody: `{"status":"ok"}`,
		CreatedAt:  now.Add(-72 * time.Hour), UpdatedAt: now.Add(-72 * time.Hour),
	}
	if err := s.db.Create(rec).Error; err != nil {
		t.Fatalf("建行: %v", err)
	}
	first, err := s.PurgeCmdResultBodies(cutoff)
	if err != nil || first != 1 {
		t.Fatalf("首次应清 1 条: cleared=%d err=%v", first, err)
	}
	second, err := s.PurgeCmdResultBodies(cutoff)
	if err != nil {
		t.Fatalf("二次执行: %v", err)
	}
	if second != 0 {
		t.Fatalf("已清空的行不得被重复计数，实得 %d", second)
	}
}

// VACUUM 必须能在这套 driver 与 SetMaxOpenConns(1) 下真的跑通 —— 它是把置空
// 变成"文件真的变小"的唯一一步,失败的话本地与诊断包两头都一分没省。
func TestVacuumDBRuns(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.VacuumDB(); err != nil {
		t.Fatalf("VacuumDB: %v", err)
	}
}
