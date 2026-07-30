// 开发者 SQL 控制台端点(AGENTS.md「开发者 SQL 控制台例外」,2026-07-30 甲方
// 裁决)。语句原样下发、结果原样回传,不设护栏。
//
// 边界按裁决收在同机:本文件不把 SQL 文本与结果写进任何日志、审计或上报,
// 它们只经这条 loopback 响应回到同机诊断台。
package adminhttp

import (
	"net/http"
)

func (a *API) devSQL(w http.ResponseWriter, r *http.Request) {
	var request struct {
		SQL string `json:"sql"`
	}
	if decodeJSON(r, &request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "SQL 请求无效"})
		return
	}
	result, err := a.st.ExecuteDevSQL(request.SQL)
	if err != nil {
		// 数据库的原话就是最有用的回执,不翻译、不归类。
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"returnedRows": result.ReturnedRows,
		"columns":      result.Columns,
		"rows":         result.Rows,
		"rowsAffected": result.RowsAffected,
	})
}
