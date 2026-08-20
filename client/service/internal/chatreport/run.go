package chatreport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"recruithelper/client/service/internal/store"
)

const (
	// 批大小。服务端上限 2000/请求，这里取 1/4 留足余量。
	profileBatchSize = 500
	messageBatchSize = 500
	// 单轮消息批数上限：500×400=20 万条，远超任何真实首次回填（客户机实测
	// 全部聊天消息 10 天 2.2MB）。这是防 bug 死循环的背板，不是业务约束；
	// 触到时响亮记日志说明还剩多少没传（不许静默截断），余量次日接着传。
	maxMessageBatchesPerRun = 400
)

// Store 是 RunOnce 依赖的账本切面。
type Store interface {
	ChatReportProfileRows() ([]store.ChatReportProfileRow, error)
	ChatReportPendingMessages(limit int) ([]store.ChatReportMessageRow, error)
	AdvanceChatReportCursor(platform, accountRef, conversationRef string, throughSeq int64) error
}

// ErrAlreadyRunning 表示已有一次上报在进行。定时触发与诊断台人工触发共用
// 进行中互斥（条款：与定时触发互斥执行，正在上传时拒绝并提示）。
var ErrAlreadyRunning = errors.New("聊天记录上报正在进行中，稍后再试")

// inFlight 是进程级进行中标记。水位与服务端幂等本身扛得住并发重传，这道闸
// 防的是白传与诊断台手抖连点，不承担正确性。
var inFlight atomic.Bool

// Summary 是一次上报的结果计数，回显给诊断台。
type Summary struct {
	Profiles int
	Messages int
}

// Deps 是一次上报的依赖。Upload 默认走 HTTP，测试替换。
type Deps struct {
	Store  Store
	Target func() (target Target, ready bool)
	Upload func(context.Context, *Payload, Target) error
	Now    func() time.Time
}

func (d Deps) upload(ctx context.Context, payload *Payload, target Target) error {
	if d.Upload != nil {
		return d.Upload(ctx, payload, target)
	}
	return Upload(ctx, payload, target)
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// RunOnce 执行一次完整上报：先档案行（全量分批 UPSERT 刷新），后消息行（游标
// 增量，每批 2xx 后立即推进该批各会话的水位）。任何一批失败即整轮结束——按
// 裁决不重试、不建发件箱，水位没推进的部分次日自愈。
//
// 授权未就绪（没绑定、没 licenseToken）返回错误让调度器记录：与工作状态上报的
// 静默跳过不同，这条通道一天只有一次机会，悄悄跳过会让"数据一直没到"难排查。
func RunOnce(ctx context.Context, deps Deps) (Summary, error) {
	if !inFlight.CompareAndSwap(false, true) {
		return Summary{}, ErrAlreadyRunning
	}
	defer inFlight.Store(false)

	if deps.Store == nil || deps.Target == nil {
		return Summary{}, errors.New("chatreport 依赖未装配")
	}
	target, ready := deps.Target()
	if !ready {
		return Summary{}, errors.New("授权未就绪，本轮不上报")
	}

	reportedAt := deps.now()

	profiles, err := deps.Store.ChatReportProfileRows()
	if err != nil {
		return Summary{}, fmt.Errorf("读取档案投影: %w", err)
	}
	for start := 0; start < len(profiles); start += profileBatchSize {
		end := min(start+profileBatchSize, len(profiles))
		payload := &Payload{
			ReportedAt: reportedAt,
			Profiles:   profileRowsOf(profiles[start:end]),
			Messages:   []MessageRow{},
		}
		if err := deps.upload(ctx, payload, target); err != nil {
			return Summary{}, fmt.Errorf("档案批 %d-%d: %w", start, end, err)
		}
	}
	slog.Info("聊天记录上报:档案行已刷新", "profiles", len(profiles))

	totalMessages := 0
	for batch := 0; ; batch++ {
		if batch >= maxMessageBatchesPerRun {
			// 不许静默截断：把"还有货没发完"喊出来，次日从水位续传。
			if remaining, err := deps.Store.ChatReportPendingMessages(1); err == nil && len(remaining) > 0 {
				slog.Warn("聊天记录上报:达到单轮批数上限，剩余消息次日续传",
					"errorCode", "chatReportBatchCapReached", "sentBatches", batch)
			}
			break
		}
		rows, err := deps.Store.ChatReportPendingMessages(messageBatchSize)
		if err != nil {
			return Summary{Profiles: len(profiles)}, fmt.Errorf("读取待传消息: %w", err)
		}
		if len(rows) == 0 {
			break
		}
		payload := &Payload{
			ReportedAt: reportedAt,
			Profiles:   []ProfileRow{},
			Messages:   messageRowsOf(rows),
		}
		if err := deps.upload(ctx, payload, target); err != nil {
			return Summary{Profiles: len(profiles), Messages: totalMessages},
				fmt.Errorf("消息批(已传 %d 条): %w", totalMessages, err)
		}
		totalMessages += len(rows)
		// 2xx 已落定，推进该批覆盖到的各会话水位。行按会话与 seq 升序取出，
		// 同会话取最后一条即最大 seq。推进失败不回滚上传（服务端已收编），
		// 只报错结束——下次会重传这批，服务端 DO NOTHING 幂等兜底。
		type convKey struct{ platform, account, conversation string }
		maxSeq := make(map[convKey]int64)
		for _, row := range rows {
			key := convKey{row.Platform, row.AccountRef, row.ConversationRef}
			if row.Seq > maxSeq[key] {
				maxSeq[key] = row.Seq
			}
		}
		for key, seq := range maxSeq {
			if err := deps.Store.AdvanceChatReportCursor(key.platform, key.account, key.conversation, seq); err != nil {
				return Summary{Profiles: len(profiles), Messages: totalMessages},
					fmt.Errorf("推进水位 %s: %w", key.conversation, err)
			}
		}
	}
	slog.Info("聊天记录上报:完成", "messages", totalMessages)
	return Summary{Profiles: len(profiles), Messages: totalMessages}, nil
}

func profileRowsOf(rows []store.ChatReportProfileRow) []ProfileRow {
	out := make([]ProfileRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, ProfileRow{
			ProfileID:       row.ProfileID,
			Platform:        row.Platform,
			AccountRef:      row.AccountRef,
			ConversationRef: row.ConversationRef,
			DisplayName:     row.DisplayName,
			BackendJobID:    row.BackendJobID,
			JobName:         row.JobName,
			MainStatus:      row.MainStatus,
			EndReason:       row.EndReason,

			GreetedAtMs:       row.GreetedAtMs,
			CommunicatingAtMs: row.CommunicatingAtMs,
			InterviewedAtMs:   row.InterviewedAtMs,
			WechatAtMs:        row.WechatAtMs,

			UpcomingInterviewStartsAtMs: row.UpcomingInterviewStartsAtMs,
			UpcomingInterviewEndsAtMs:   row.UpcomingInterviewEndsAtMs,
			UpcomingInterviewMethod:     row.UpcomingInterviewMethod,
		})
	}
	return out
}

func messageRowsOf(rows []store.ChatReportMessageRow) []MessageRow {
	out := make([]MessageRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, MessageRow{
			Platform:        row.Platform,
			AccountRef:      row.AccountRef,
			ConversationRef: row.ConversationRef,
			Seq:             row.Seq,
			ProfileID:       row.ProfileID,

			Direction: row.Direction,
			Kind:      row.Kind,
			Text:      row.Text,
			CardType:  row.CardType,
			CardState: row.CardState,

			InterviewStartsAtMs: row.InterviewStartsAtMs,
			InterviewEndsAtMs:   row.InterviewEndsAtMs,
			InterviewMethod:     row.InterviewMethod,
			TsApproxMs:          row.TsApproxMs,

			Provenance: provenanceOf(
				row.Direction,
				row.OutboundIntentID != nil,
				row.EventKind,
				row.ActionKind,
				row.Primitive,
			),

			Retracted:        row.RetractedAt != nil,
			RetractionReason: row.RetractionReason,
		})
	}
	return out
}
