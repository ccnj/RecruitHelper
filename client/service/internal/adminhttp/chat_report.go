// 聊天记录上报的诊断台入口(AGENTS.md「全局约定·聊天记录上报」,2026-08-20
// 甲方裁决增补)。POST /admin/dev/chat-report/run:立即执行一次与每日 00:20
// 完全同款的上报——同一 RunOnce、同一游标水位、同一幂等语义,传完当晚那轮
// 自然没剩多少可传。
//
// 与巡检不冲突:上报对业务账本只读,唯一的写是自己的水位表;进行中互斥在
// chatreport 包内,人工触发与定时触发不会同时跑,正在上传时这里直接拒绝。
package adminhttp

import (
	"context"
	"errors"
	"net/http"

	"recruithelper/client/service/internal/chatreport"
)

// ChatReportRunner 由装配方注入(main.go),闭包里带着 store 与旧后台身份。
type ChatReportRunner func(ctx context.Context) (chatreport.Summary, error)

func (a *API) SetChatReportRunner(run ChatReportRunner) *API {
	a.chatReportRun = run
	return a
}

type chatReportRunResponse struct {
	OK       bool   `json:"ok"`
	Profiles int    `json:"profiles"`
	Messages int    `json:"messages"`
	Error    string `json:"error,omitempty"`
}

func (a *API) devChatReportRun(w http.ResponseWriter, r *http.Request) {
	if a.chatReportRun == nil {
		writeJSON(w, http.StatusServiceUnavailable,
			chatReportRunResponse{Error: "聊天记录上报未装配"})
		return
	}
	summary, err := a.chatReportRun(r.Context())
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, chatreport.ErrAlreadyRunning) {
			status = http.StatusConflict
		}
		// 失败也回已完成的计数:分批上传是走一批推一批水位,报错前传出去的
		// 部分已经落在服务器上,再点一次只会续传剩余。
		writeJSON(w, status, chatReportRunResponse{
			Profiles: summary.Profiles,
			Messages: summary.Messages,
			Error:    err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, chatReportRunResponse{
		OK:       true,
		Profiles: summary.Profiles,
		Messages: summary.Messages,
	})
}
