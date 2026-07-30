// 开发者 SQL 控制台的执行面(AGENTS.md「开发者 SQL 控制台例外」,2026-07-30
// 甲方裁决)。这里**故意**不做任何限制:不挑语句种类、不挑表和列、不预览、
// 不备份、不看有没有活跃工作流。语句原样交给数据库,成败原样回传。
//
// 唯一的分支是怎么把结果取回来:能产出行的语句要列名和行,其余语句要影响
// 行数。这是回执的形状,不是许可判断——两条路都不拒绝任何输入。
//
// 走 Store 自己的连接是有意的:业务库按 SetMaxOpenConns(1) 串行化,借道这
// 里的写入自然排在脑的写入队列里,不构成第二个写入者。它不解决脑内存态与
// 库不一致的问题,那部分风险按裁决由使用者承担。
package store

import (
	"database/sql"
	"errors"
	"strings"
)

var ErrDevSQLEmpty = errors.New("SQL 语句为空")

// DevSQLResult 是一次执行的完整回执。Query 与 Exec 只会填其中一半。
type DevSQLResult struct {
	// ReturnedRows 为真表示这条语句产出了结果集(Columns/Rows 有效),
	// 否则表示它是写入类语句(RowsAffected 有效)。
	ReturnedRows bool     `json:"returnedRows"`
	Columns      []string `json:"columns"`
	Rows         [][]any  `json:"rows"`
	RowsAffected int64    `json:"rowsAffected"`
}

// ExecuteDevSQL 执行一条裸 SQL。错误原样返回给调用方回显,不做包装或翻译。
func (s *Store) ExecuteDevSQL(statement string) (*DevSQLResult, error) {
	statement = strings.TrimSpace(statement)
	if statement == "" {
		return nil, ErrDevSQLEmpty
	}
	if devSQLReturnsRows(statement) {
		return s.devSQLQuery(statement)
	}
	return s.devSQLExec(statement)
}

func (s *Store) devSQLQuery(statement string) (*DevSQLResult, error) {
	rows, err := s.db.Raw(statement).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := &DevSQLResult{ReturnedRows: true, Columns: columns, Rows: [][]any{}}
	for rows.Next() {
		cells := make([]any, len(columns))
		targets := make([]any, len(columns))
		for index := range cells {
			targets[index] = &cells[index]
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		for index, cell := range cells {
			cells[index] = devSQLCell(cell)
		}
		out.Rows = append(out.Rows, cells)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out.RowsAffected = int64(len(out.Rows))
	return out, nil
}

func (s *Store) devSQLExec(statement string) (*DevSQLResult, error) {
	tx := s.db.Exec(statement)
	if tx.Error != nil {
		return nil, tx.Error
	}
	return &DevSQLResult{RowsAffected: tx.RowsAffected}, nil
}

// devSQLReturnsRows 只决定用哪个驱动 API 取回执,不决定语句准不准执行。
// 判不准最坏的结果是回执形状不理想(写语句走 Query 会显示空结果集,读语句
// 走 Exec 会显示 0 行影响),语句本身照常执行。
func devSQLReturnsRows(statement string) bool {
	head := strings.ToUpper(devSQLFirstKeyword(statement))
	switch head {
	case "SELECT", "WITH", "EXPLAIN", "PRAGMA", "VALUES", "SHOW":
		return true
	}
	// RETURNING 让 UPDATE/INSERT/DELETE 也产出行。
	return strings.Contains(strings.ToUpper(statement), " RETURNING ")
}

// devSQLFirstKeyword 跳过前导的行注释与块注释,取出第一个词。
func devSQLFirstKeyword(statement string) string {
	rest := strings.TrimSpace(statement)
	for {
		switch {
		case strings.HasPrefix(rest, "--"):
			if index := strings.IndexByte(rest, '\n'); index >= 0 {
				rest = strings.TrimSpace(rest[index+1:])
				continue
			}
			return ""
		case strings.HasPrefix(rest, "/*"):
			if index := strings.Index(rest, "*/"); index >= 0 {
				rest = strings.TrimSpace(rest[index+2:])
				continue
			}
			return ""
		}
		break
	}
	if index := strings.IndexAny(rest, " \t\n\r(;"); index >= 0 {
		return rest[:index]
	}
	return rest
}

// devSQLCell 把驱动返回值转成 JSON 里看得懂的形状。SQLite 的 TEXT 常以
// []byte 回来,直接序列化会变成 base64,那样回执就没法读了。
func devSQLCell(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case []byte:
		return string(typed)
	case sql.RawBytes:
		return string(typed)
	default:
		return typed
	}
}
