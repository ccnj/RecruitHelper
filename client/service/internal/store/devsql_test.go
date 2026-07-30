package store

import (
	"errors"
	"strings"
	"testing"
)

func TestExecuteDevSQLQueryReturnsColumnsAndRows(t *testing.T) {
	s := openTest(t)
	if err := s.db.Exec(
		`INSERT INTO candidates(platform, platform_user_ref, display_name, first_seen_at, last_seen_at)
		 VALUES('zhilian','u-1','某先生','2026-07-30','2026-07-30')`,
	).Error; err != nil {
		t.Fatal(err)
	}

	result, err := s.ExecuteDevSQL(
		"select platform_user_ref, display_name from candidates order by platform_user_ref",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ReturnedRows || len(result.Rows) != 1 || result.RowsAffected != 1 {
		t.Fatalf("查询回执形状不对: %+v", result)
	}
	if len(result.Columns) != 2 ||
		result.Columns[0] != "platform_user_ref" || result.Columns[1] != "display_name" {
		t.Fatalf("列名不对: %+v", result.Columns)
	}
	// TEXT 必须以字符串回来,否则 JSON 序列化成 base64,回执就没法读了。
	if text, ok := result.Rows[0][1].(string); !ok || text != "某先生" {
		t.Fatalf("文本列没有还原成字符串: %#v", result.Rows[0][1])
	}
}

func TestExecuteDevSQLWriteReportsRowsAffected(t *testing.T) {
	s := openTest(t)
	if err := s.db.Exec(
		`INSERT INTO candidates(platform, platform_user_ref, display_name, first_seen_at, last_seen_at)
		 VALUES('zhilian','u-1','旧名','2026-07-30','2026-07-30')`,
	).Error; err != nil {
		t.Fatal(err)
	}

	result, err := s.ExecuteDevSQL("update candidates set display_name='新名' where platform_user_ref='u-1'")
	if err != nil {
		t.Fatal(err)
	}
	if result.ReturnedRows || result.RowsAffected != 1 {
		t.Fatalf("写入回执形状不对: %+v", result)
	}

	readBack, err := s.ExecuteDevSQL("select display_name from candidates where platform_user_ref='u-1'")
	if err != nil || len(readBack.Rows) != 1 || readBack.Rows[0][0] != "新名" {
		t.Fatalf("写入没有真正落库: %+v err=%v", readBack, err)
	}
}

// 裁决明确不设护栏:DELETE 业务事实行、改 WAL 字段这些平时被代码挡住的操作,
// 从这个入口一律照做。这条测试锁住"不拦"这个行为本身。
func TestExecuteDevSQLRefusesNothing(t *testing.T) {
	s := openTest(t)
	if err := s.db.Exec(
		`INSERT INTO candidates(platform, platform_user_ref, display_name, first_seen_at, last_seen_at)
		 VALUES('zhilian','u-1','某先生','2026-07-30','2026-07-30')`,
	).Error; err != nil {
		t.Fatal(err)
	}
	result, err := s.ExecuteDevSQL("delete from candidates where platform_user_ref='u-1'")
	if err != nil || result.RowsAffected != 1 {
		t.Fatalf("DELETE 应当照常执行: %+v err=%v", result, err)
	}
	remaining, err := s.ExecuteDevSQL("select count(*) from candidates")
	if err != nil || remaining.Rows[0][0] != int64(0) {
		t.Fatalf("业务事实行没有被真正删掉: %+v err=%v", remaining, err)
	}
}

func TestExecuteDevSQLSurfacesDatabaseErrorVerbatim(t *testing.T) {
	s := openTest(t)
	if _, err := s.ExecuteDevSQL("select * from 根本没有这张表"); err == nil {
		t.Fatal("语法/对象错误必须原样报上来")
	} else if !strings.Contains(err.Error(), "根本没有这张表") {
		t.Fatalf("错误信息被吞掉了细节: %v", err)
	}

	if _, err := s.ExecuteDevSQL("   \n  "); !errors.Is(err, ErrDevSQLEmpty) {
		t.Fatalf("空语句应当明确拒绝: %v", err)
	}
}

func TestDevSQLReturnsRowsPicksTheRightReceiptShape(t *testing.T) {
	rows := []string{
		"select 1", "  SELECT 1", "with x as (select 1) select * from x",
		"explain query plan select 1", "pragma table_info(candidates)",
		"-- 注释\nselect 1", "/* 块注释 */ select 1",
		"update candidates set display_name='x' RETURNING display_name",
	}
	for _, statement := range rows {
		if !devSQLReturnsRows(statement) {
			t.Fatalf("应判为产出行: %q", statement)
		}
	}
	noRows := []string{
		"update candidates set display_name='x'", "delete from candidates",
		"insert into candidates values(1)", "vacuum", "begin", "commit",
	}
	for _, statement := range noRows {
		if devSQLReturnsRows(statement) {
			t.Fatalf("应判为写入类: %q", statement)
		}
	}
}
